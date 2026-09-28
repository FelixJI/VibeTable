package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

// Exercise production mutation, durable fan-out and freshness-aware query projection.
func TestConditionalLookupMutationRecalculatesThroughJobsAndQuery(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()

	orders := createV2IntegrationTable(t, ctx, app, "条件订单", "clm_orders")
	materials := createV2IntegrationTable(t, ctx, app, "条件物料", "clm_materials")
	orderCode := createV2IntegrationField(
		t, ctx, app, orders.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "订单编码"), "clm_order_code",
	)
	sourceCode := createV2IntegrationField(
		t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "物料编码"), "clm_source_code",
	)
	sourceTitle := createV2IntegrationField(
		t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "物料名称"), "clm_source_title",
	)
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "条件结果")
	lookupDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{}, TargetFieldID: sourceTitle.FieldID,
		Condition: &v2.LookupCondition{
			SourceTableID: materials.TableID, Match: "all",
			Rules: []v2.LookupConditionRule{{
				SourceFieldID: sourceCode.FieldID, Operator: "eq",
				Operand: &v2.LookupOperand{Kind: "field", FieldID: orderCode.FieldID},
			}},
		},
	}
	lookupField := createV2IntegrationField(
		t, ctx, app, orders.TableID, lookupDraft, "clm_lookup",
	)
	if orderCode.Definition == nil || sourceCode.Definition == nil ||
		sourceTitle.Definition == nil || lookupField.Definition == nil {
		t.Fatalf("conditional lookup fixture omitted field definitions")
	}
	ordersDefinition, err := schemaapi.New(app).Describe(ctx, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	materialsDefinition, err := schemaapi.New(app).Describe(ctx, materials.TableID)
	if err != nil {
		t.Fatal(err)
	}

	// Production wiring: the jobs service is both the in-transaction
	// computation invalidator and the committed-event publisher, and the
	// kernel is the service's mutation kernel for recalculation batches.
	jobService := jobs.New(app, nil)
	defer jobService.Shutdown()
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(computed.New(lookup.NewCalculator())),
		mutation.WithPublisher(jobService),
		mutation.WithComputationInvalidator(jobService),
		mutation.WithPublishContext(jobService.PublishContext()),
	)
	jobService.SetKernel(kernel)

	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(app, querySource)
	orderCollection, err := app.FindCollectionByNameOrId(orders.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}

	orderCodeName := orderCode.Definition.Identity.PhysicalName
	sourceCodeName := sourceCode.Definition.Identity.PhysicalName
	sourceTitleName := sourceTitle.Definition.Identity.PhysicalName
	lookupName := lookupField.Definition.Identity.PhysicalName
	orderID := "clmorder0000001"
	materialID := "clmmaterial0001"

	applyMutation := func(
		stage string,
		table v2IntegrationTable,
		schemaRevision string,
		key string,
		operation mutation.Operation,
	) {
		t.Helper()
		receipt, err := kernel.Apply(ctx, mutationRequest(table.TableID, schemaRevision, key, operation))
		if err != nil {
			t.Fatalf("%s: mutation %s: %v", stage, key, err)
		}
		if len(receipt.Warnings) != 0 {
			t.Fatalf("%s: publish warnings: %+v", stage, receipt.Warnings)
		}
	}

	jobIDSnapshot := func() map[string]struct{} {
		t.Helper()
		records, err := app.FindAllRecords("vibetable_jobs")
		if err != nil {
			t.Fatal(err)
		}
		ids := make(map[string]struct{}, len(records))
		for _, record := range records {
			ids[record.Id] = struct{}{}
		}
		return ids
	}

	// drainConditionalJobs waits until every job enqueued after the snapshot
	// reaches a terminal state and at least one recalculation job exists, so
	// later assertions never read before the asynchronous kernel batch lands.
	drainConditionalJobs := func(stage string, before map[string]struct{}) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			records, err := app.FindAllRecords("vibetable_jobs")
			if err != nil {
				t.Fatal(err)
			}
			created, active, failed := 0, []string{}, []string{}
			for _, record := range records {
				if _, old := before[record.Id]; old {
					continue
				}
				created++
				switch state := record.GetString("state"); state {
				case "complete":
				case "failed", "cancelled":
					failed = append(failed, record.Id+" "+fmt.Sprint(record.GetRaw("error_json")))
				default:
					active = append(active, record.Id+" "+state)
				}
			}
			if created >= 1 && len(active) == 0 {
				if len(failed) > 0 {
					t.Fatalf("%s: conditional recalculation jobs failed: %v", stage, failed)
				}
				return
			}
			if time.Now().After(deadline) {
				for _, record := range records {
					if _, err := jobService.Get(ctx, record.Id); err != nil {
						t.Logf("load job %s: %v", record.Id, err)
					}
				}
				t.Fatalf(
					"%s: conditional recalculation jobs did not settle: created=%d active=%v failed=%v",
					stage, created, active, failed,
				)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	lookupJSON := func(stage string, value any) string {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("%s: lookup value %T is not serializable: %v", stage, value, err)
		}
		return string(encoded)
	}

	// assertLookupValue reads the order row through the query port (the
	// product read path that projects fresh computed envelopes) and
	// cross-checks the stored envelope on the record itself.
	assertLookupValue := func(stage string, want []any) {
		t.Helper()
		wantJSON := lookupJSON(stage, want)
		page, err := port.QueryPage(ctx, orders.TableID, query.TableQuery{Limit: 10})
		if err != nil {
			t.Fatalf("%s: query page: %v", stage, err)
		}
		var queried any
		found := false
		for _, row := range page.Rows {
			if fmt.Sprint(row["id"]) == orderID {
				queried = row[lookupName]
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s: order row missing from query page: %#v", stage, page.Rows)
		}
		if got := lookupJSON(stage, queried); got != wantJSON {
			t.Fatalf("%s: query-port lookup = %s, want %s", stage, got, wantJSON)
		}
		record, err := app.FindRecordById(orderCollection, orderID)
		if err != nil {
			t.Fatalf("%s: reload order: %v", stage, err)
		}
		stored := relatedcomputation.ProjectStored(record.GetRaw(lookupName))
		if got := lookupJSON(stage, stored); got != wantJSON {
			t.Fatalf("%s: stored lookup envelope = %s, want %s", stage, got, wantJSON)
		}
	}

	mutateAndDrain := func(
		stage string,
		table v2IntegrationTable,
		schemaRevision string,
		key string,
		operation mutation.Operation,
	) {
		t.Helper()
		before := jobIDSnapshot()
		applyMutation(stage, table, schemaRevision, key, operation)
		drainConditionalJobs(stage, before)
	}

	// Stage 1: current-table insert with zero matching source rows.
	mutateAndDrain(
		"zero-match order insert", orders,
		ordersDefinition.Snapshot.SchemaRevision, "clm-order-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &orderID,
			Values: map[string]any{orderCodeName: "MAT-1"},
		},
	)
	assertLookupValue("zero-match order insert", []any{})

	// Stage 2: a source insert that starts matching must refresh the order.
	mutateAndDrain(
		"source insert match", materials,
		materialsDefinition.Snapshot.SchemaRevision, "clm-material-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &materialID,
			Values: map[string]any{
				sourceCodeName: "MAT-1", sourceTitleName: "铝板",
			},
		},
	)
	assertLookupValue("source insert match", []any{"铝板"})

	// Stage 3: changing only the returned source value must propagate.
	mutateAndDrain(
		"source return-value change", materials,
		materialsDefinition.Snapshot.SchemaRevision, "clm-material-retitle",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &materialID,
			Values: map[string]any{sourceTitleName: "钢板"},
		},
	)
	assertLookupValue("source return-value change", []any{"钢板"})

	// Stage 4: changing the source match key back to no-match must clear.
	mutateAndDrain(
		"source no-longer-matches", materials,
		materialsDefinition.Snapshot.SchemaRevision, "clm-material-unmatch",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &materialID,
			Values: map[string]any{sourceCodeName: "MAT-2"},
		},
	)
	assertLookupValue("source no-longer-matches", []any{})

	// Stage 5: changing the current-table operand must re-evaluate and match.
	mutateAndDrain(
		"order operand change", orders,
		ordersDefinition.Snapshot.SchemaRevision, "clm-order-recoded",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &orderID,
			Values: map[string]any{orderCodeName: "MAT-2"},
		},
	)
	assertLookupValue("order operand change", []any{"钢板"})
	mutateAndDrain("source deletion", materials,
		materialsDefinition.Snapshot.SchemaRevision, "clm-material-delete",
		mutation.Operation{Kind: mutation.OperationDelete, RecordID: &materialID})
	assertLookupValue("source deletion", []any{})
}
