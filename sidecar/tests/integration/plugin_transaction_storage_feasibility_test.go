package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"

	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// This proves PocketBase storage feasibility, not the plugin transaction kernel contract.
func TestPluginTransactionStorageFeasibility(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		cardinality string
		failUnique  bool
	}{
		{"commit_many", "many", false},
		{"commit_one", "one", false},
		{"rollback_second_phase_unique", "many", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			f := newPairUpdateFixture(t, false)
			if scenario.cardinality == "one" {
				plan := f.plan(t, v2.RelationPairPatch{
					SourceCardinality: &scenario.cardinality, ReciprocalCardinality: &scenario.cardinality,
				})
				var err error
				f.pair, err = f.executor.Apply(ctx, v2.ApplyRequest{
					PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "storage_pair_one", Actor: f.actor,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			setValue := func(record *core.Record, field *v2.FieldDefinition, mode fieldvalue.WriteMode, value any) error {
				normalized, err := fieldvalue.New().NormalizeWrite(ctx, *field, mode, fieldvalue.Input{Supplied: true, Value: value})
				if err != nil {
					return err
				}
				for name, physical := range normalized.PhysicalValues {
					record.Set(name, physical)
				}
				return nil
			}
			endpoints := []struct {
				table    v2IntegrationTable
				relation *v2.FieldDefinition
				code     *v2.FieldDefinition
				record   *core.Record
				id       string
				targetID string
			}{
				{table: f.source, relation: f.pair.Definition, id: "000000000000001", targetID: "000000000000002"},
				{table: f.target, relation: f.pair.Related[0].Definition, id: "000000000000002", targetID: "000000000000001"},
			}
			for index := range endpoints {
				endpoint := &endpoints[index]
				draft := fieldDraftForIntegration(t, v2.LogicalText, "Required unique code")
				draft.Value.Required = true
				draft.Constraints.Unique.Enabled = true
				endpoint.code = createV2IntegrationField(t, ctx, f.app, endpoint.table.TableID, draft, fmt.Sprintf("storage_code_%d", index)).Definition
				collection, err := f.app.FindCollectionByNameOrId(endpoint.table.PhysicalName)
				if err != nil {
					t.Fatal(err)
				}
				endpoint.record = core.NewRecord(collection)
				endpoint.record.Id = endpoint.id
				if err := setValue(endpoint.record, endpoint.code, fieldvalue.Insert, "new-code"); err != nil {
					t.Fatal(err)
				}
			}
			// Keep an existing row to exercise the compiled partial unique index and rollback isolation.
			existing := core.NewRecord(endpoints[1].record.Collection())
			existing.Id = "000000000000003"
			if err := setValue(existing, endpoints[1].code, fieldvalue.Insert, "existing-code"); err != nil {
				t.Fatal(err)
			}
			if err := f.app.Save(existing); err != nil {
				t.Fatal(err)
			}

			secondPhaseSaves := 0
			err := f.app.RunInTransaction(func(tx core.App) error {
				for _, endpoint := range endpoints {
					if err := tx.Save(endpoint.record); err != nil {
						return fmt.Errorf("save legal base row %s: %w", endpoint.id, err)
					}
					stored, err := tx.FindRecordById(endpoint.table.PhysicalName, endpoint.id)
					if err != nil {
						return err
					}
					if len(stored.GetStringSlice(endpoint.relation.Identity.PhysicalName)) != 0 ||
						stored.GetString(endpoint.code.Identity.PhysicalName) != "new-code" ||
						!stored.GetBool(endpoint.code.Value.Presence.PhysicalName) {
						return fmt.Errorf("base row %s did not preserve valid non-relation values", endpoint.id)
					}
				}
				for index, endpoint := range endpoints {
					var links any = []string{endpoint.targetID}
					if scenario.cardinality == "one" {
						links = endpoint.targetID
					}
					if err := setValue(endpoint.record, endpoint.relation, fieldvalue.Update, links); err != nil {
						return err
					}
					if scenario.failUnique && index == 1 {
						if err := setValue(endpoint.record, endpoint.code, fieldvalue.Update, "existing-code"); err != nil {
							return err
						}
					}
					if err := tx.Save(endpoint.record); err != nil {
						return fmt.Errorf("save final relation %s: %w", endpoint.id, err)
					}
					secondPhaseSaves++
				}
				return nil
			})
			if scenario.failUnique {
				var fields validation.Errors
				if !errors.As(err, &fields) {
					t.Fatalf("expected second-phase unique validation failure, got %v", err)
				}
				var unique validation.Error
				if !errors.As(fields[endpoints[1].code.Identity.PhysicalName], &unique) || unique.Code() != "validation_not_unique" || secondPhaseSaves != 1 {
					t.Fatalf("expected unique failure after first final relation save, got %v; saves=%d", err, secondPhaseSaves)
				}
				t.Logf("second-phase failure after one final relation save: %v", err)
			} else if err != nil {
				t.Fatal(err)
			}
			for index, endpoint := range endpoints {
				stored, err := f.app.FindRecordById(endpoint.table.PhysicalName, endpoint.id)
				wantCount := int64(1 + index)
				if scenario.failUnique {
					wantCount = int64(index)
					if !errors.Is(err, sql.ErrNoRows) {
						t.Fatalf("rolled-back row %s remains: %v", endpoint.id, err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if got := stored.GetStringSlice(endpoint.relation.Identity.PhysicalName); !reflect.DeepEqual(got, []string{endpoint.targetID}) {
						t.Fatalf("final %s links = %v, want %s", endpoint.id, got, endpoint.targetID)
					}
					if stored.GetString(endpoint.code.Identity.PhysicalName) != "new-code" || !stored.GetBool(endpoint.code.Value.Presence.PhysicalName) {
						t.Fatalf("final row %s lost required value/presence", endpoint.id)
					}
				}
				if count, err := f.app.CountRecords(endpoint.table.PhysicalName); err != nil || count != wantCount {
					t.Fatalf("table %s count = %d, want %d: %v", endpoint.table.TableID, count, wantCount, err)
				}
			}
			preserved, err := f.app.FindRecordById(f.target.PhysicalName, existing.Id)
			if err != nil {
				t.Fatal(err)
			}
			if preserved.GetString(endpoints[1].code.Identity.PhysicalName) != "existing-code" ||
				!preserved.GetBool(endpoints[1].code.Value.Presence.PhysicalName) ||
				len(preserved.GetStringSlice(endpoints[1].relation.Identity.PhysicalName)) != 0 {
				t.Fatal("transaction changed the existing unrelated row")
			}
		})
	}
}
