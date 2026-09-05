package integration_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

type pairUpdateFixture struct {
	app            core.App
	catalog        *fieldchange.Catalog
	planner        *fieldchange.Planner
	executor       *fieldchange.Executor
	source, target v2IntegrationTable
	pair           v2.ApplyReceipt
	actor          v2.Actor
}

func newPairUpdateFixture(t *testing.T, self bool) pairUpdateFixture {
	t.Helper()
	ctx := context.Background()
	app := bootstrapApp(t, queryTempDir(t))
	t.Cleanup(func() { resetApp(t, app) })
	source, sourceName := createV2IntegrationTableWithField(t, ctx, app, "Sources", "Name", "pair_source")
	target, targetName := source, sourceName
	if !self {
		target, targetName = createV2IntegrationTableWithField(t, ctx, app, "Targets", "Name", "pair_target")
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, nil)
	executor := fieldchange.NewExecutor(app, store)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	draft := fieldDraftForIntegration(t, v2.LogicalRelation, "Targets")
	draft.Relation = &v2.RelationSpec{TargetTableID: target.TableID, Cardinality: "many", DeletePolicy: "setNull", DisplayField: targetName.FieldID}
	rev, err := catalog.Revisions(ctx, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: source.TableID, ExpectedSchemaRev: rev.Schema,
		Draft: &draft, Actor: actor,
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "Sources", ReciprocalCardinality: "many", SourceDisplayFieldID: sourceName.FieldID},
	})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := executor.Apply(ctx, v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "pair_create", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	return pairUpdateFixture{app, catalog, planner, executor, source, target, pair, actor}
}

func (f pairUpdateFixture) plan(t *testing.T, patch v2.RelationPairPatch) v2.FieldChangePlan {
	t.Helper()
	ctx := context.Background()
	rev, err := f.catalog.Revisions(ctx, f.source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: f.source.TableID, FieldID: f.pair.FieldID,
		ExpectedSchemaRev: rev.Schema, Actor: f.actor, RelationPairPatch: &patch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func (f pairUpdateFixture) insert(t *testing.T, table v2IntegrationTable, id string, values map[string]any) {
	t.Helper()
	ctx := context.Background()
	rev, err := f.catalog.Revisions(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mutation.New(f.app, mutation.MetadataSchemaSource{}).Apply(ctx, mutationRequest(
		table.TableID, rev.Schema, "insert-"+id,
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &id, Values: values},
	))
	if err != nil {
		t.Fatal(err)
	}
}

func TestRelationPairPatchPreservesLinksAndIdentities(t *testing.T) {
	for _, self := range []bool{false, true} {
		t.Run(map[bool]string{false: "two_tables", true: "self_relation"}[self], func(t *testing.T) {
			f := newPairUpdateFixture(t, self)
			f.insert(t, f.target, "pairtarget00001", map[string]any{})
			f.insert(t, f.source, "pairsource00001", map[string]any{f.pair.Definition.Identity.PhysicalName: []string{"pairtarget00001"}})
			f.insert(t, f.source, "pairempty000001", map[string]any{})
			for index, cardinality := range []string{"one", "many"} {
				sourceName, reverseName, policy := "Renamed targets", "Renamed sources", "restrict"
				plan := f.plan(t, v2.RelationPairPatch{SourceDisplayName: &sourceName, ReciprocalDisplayName: &reverseName, SourceCardinality: &cardinality, ReciprocalCardinality: &cardinality, DeletePolicy: &policy})
				if !plan.CanApply || plan.CreatesMigration || len(plan.RelatedChanges) != 1 {
					t.Fatalf("pair patch not atomic/applicable: %#v", plan)
				}
				if plan.After.Identity != f.pair.Definition.Identity || plan.RelatedChanges[0].After.Identity != f.pair.Related[0].Definition.Identity {
					t.Fatal("pair patch changed field/provider identity")
				}
				if plan.ExpectedDataRevision == nil || plan.RelatedChanges[0].ExpectedDataRevision == nil {
					t.Fatal("cardinality scan did not freeze both data revisions")
				}
				receipt, err := f.executor.Apply(context.Background(), v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: []string{"pair_to_one", "pair_to_many"}[index], Actor: f.actor})
				if err != nil {
					t.Fatal(err)
				}
				if receipt.MigrationJobID != "" || receipt.Definition.Relation.PairID != f.pair.Definition.Relation.PairID || receipt.Related[0].Definition.Relation.PairID != receipt.Definition.Relation.PairID {
					t.Fatalf("pair identity or atomic completion lost: %#v", receipt)
				}
				if receipt.Definition.DisplayName != sourceName || receipt.Related[0].Definition.DisplayName != reverseName || receipt.Related[0].Definition.Relation.DeletePolicy != policy {
					t.Fatalf("pair settings not applied: %#v", receipt)
				}
				for _, endpoint := range []struct {
					table, id, field string
					want             []string
				}{
					{f.source.PhysicalName, "pairsource00001", f.pair.Definition.Identity.PhysicalName, []string{"pairtarget00001"}},
					{f.target.PhysicalName, "pairtarget00001", f.pair.Related[0].Definition.Identity.PhysicalName, []string{"pairsource00001"}},
					{f.source.PhysicalName, "pairempty000001", f.pair.Definition.Identity.PhysicalName, []string{}},
				} {
					record, err := f.app.FindRecordById(endpoint.table, endpoint.id)
					if err != nil {
						t.Fatal(err)
					}
					if got := record.GetStringSlice(endpoint.field); !reflect.DeepEqual(got, endpoint.want) {
						t.Fatalf("%s.%s links = %#v, want %#v", endpoint.id, endpoint.field, got, endpoint.want)
					}
				}
			}
		})
	}
}

func TestRelationPairPatchRejectsReciprocalDataChange(t *testing.T) {
	f := newPairUpdateFixture(t, false)
	one := "one"
	plan := f.plan(t, v2.RelationPairPatch{ReciprocalCardinality: &one})
	if !plan.CanApply || plan.RelatedChanges[0].ExpectedDataRevision == nil {
		t.Fatalf("invalid frozen patch: %#v", plan)
	}
	f.insert(t, f.target, "pairtarget00001", map[string]any{})
	_, err := f.executor.Apply(context.Background(), v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "pair_stale_data", Actor: f.actor})
	var productErr *fieldchange.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "field.change.data_conflict" {
		t.Fatalf("stale target data accepted: %v", err)
	}
	for _, endpoint := range []struct{ table, field string }{{f.source.TableID, f.pair.FieldID}, {f.target.TableID, f.pair.Related[0].FieldID}} {
		field, err := f.catalog.Field(context.Background(), endpoint.table, endpoint.field)
		if err != nil {
			t.Fatal(err)
		}
		if field.Relation.Cardinality != "many" {
			t.Fatal("stale pair plan changed one side")
		}
	}
}

func TestRelationPairPatchRejectsAmbiguousCardinality(t *testing.T) {
	for _, reciprocal := range []bool{false, true} {
		t.Run(map[bool]string{false: "source", true: "reciprocal"}[reciprocal], func(t *testing.T) {
			f := newPairUpdateFixture(t, false)
			f.insert(t, f.target, "pairtarget00001", map[string]any{})
			f.insert(t, f.target, "pairtarget00002", map[string]any{})
			links := []string{"pairtarget00001", "pairtarget00002"}
			f.insert(t, f.source, "pairsource00001", map[string]any{f.pair.Definition.Identity.PhysicalName: links})
			if reciprocal {
				f.insert(t, f.source, "pairsource00002", map[string]any{f.pair.Definition.Identity.PhysicalName: []string{"pairtarget00001"}})
			}
			one := "one"
			patch := v2.RelationPairPatch{SourceCardinality: &one}
			if reciprocal {
				patch = v2.RelationPairPatch{ReciprocalCardinality: &one}
			}
			plan := f.plan(t, patch)
			if plan.CanApply || len(plan.Errors) == 0 {
				t.Fatalf("ambiguous cardinality accepted: %#v", plan)
			}
			_, err := f.executor.Apply(context.Background(), v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "pair_ambiguous", Actor: f.actor})
			if err == nil {
				t.Fatal("blocked cardinality plan applied")
			}
			record, err := f.app.FindRecordById(f.source.PhysicalName, "pairsource00001")
			if err != nil {
				t.Fatal(err)
			}
			if got := record.GetStringSlice(f.pair.Definition.Identity.PhysicalName); !reflect.DeepEqual(got, links) {
				t.Fatalf("ambiguous plan discarded links: %#v", got)
			}
		})
	}
}

func TestRelationPairPatchRejectsTargetSchemaChange(t *testing.T) {
	f := newPairUpdateFixture(t, false)
	name := "Renamed reverse"
	plan := f.plan(t, v2.RelationPairPatch{ReciprocalDisplayName: &name})
	applyCreatedField(t, context.Background(), f.catalog, f.planner, f.executor, f.target.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Concurrent"), f.actor, "pair_concurrent_field")
	_, err := f.executor.Apply(context.Background(), v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "pair_stale_schema", Actor: f.actor})
	var productErr *fieldchange.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "field.change.schema_conflict" {
		t.Fatalf("stale target schema accepted: %v", err)
	}
	field, err := f.catalog.Field(context.Background(), f.target.TableID, f.pair.Related[0].FieldID)
	if err != nil {
		t.Fatal(err)
	}
	if field.DisplayName != f.pair.Related[0].Definition.DisplayName {
		t.Fatal("stale plan changed reciprocal field")
	}
}

func TestRelationPairPatchRollsBackWhenSecondSchemaSaveFails(t *testing.T) {
	f := newPairUpdateFixture(t, false)
	one := "one"
	plan := f.plan(t, v2.RelationPairPatch{SourceCardinality: &one, ReciprocalCardinality: &one})
	forced := errors.New("forced reciprocal schema save failure")
	observed := false
	f.app.OnCollectionUpdate().BindFunc(func(event *core.CollectionEvent) error {
		if event.Collection.Name == f.target.PhysicalName {
			observed = true
			return forced
		}
		return event.Next()
	})
	_, err := f.executor.Apply(context.Background(), v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "pair_faulted", Actor: f.actor})
	if err == nil || !observed {
		t.Fatalf("second-side failure not reached: %v / %v", observed, err)
	}
	for _, endpoint := range []struct {
		table      v2IntegrationTable
		definition *v2.FieldDefinition
		revision   string
	}{
		{f.source, f.pair.Definition, plan.ExpectedSchemaRev},
		{f.target, f.pair.Related[0].Definition, plan.RelatedChanges[0].ExpectedSchemaRevision},
	} {
		field, err := f.catalog.Field(context.Background(), endpoint.table.TableID, endpoint.definition.Identity.FieldID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(field, endpoint.definition) {
			t.Fatal("failed second side left modified metadata")
		}
		rev, err := f.catalog.Revisions(context.Background(), endpoint.table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		if rev.Schema != endpoint.revision {
			t.Fatal("failed second side advanced schema revision")
		}
		collection, err := f.app.FindCollectionByNameOrId(endpoint.table.PhysicalName)
		if err != nil {
			t.Fatal(err)
		}
		relation := collection.Fields.GetById(endpoint.definition.Identity.ProviderFieldID).(*core.RelationField)
		if relation.MaxSelect <= 1 {
			t.Fatal("failed second side left changed provider cardinality")
		}
	}
}

func TestRelationPairUpdateCannotRetargetOrReplaceIdentities(t *testing.T) {
	f := newPairUpdateFixture(t, false)
	ctx := context.Background()
	other, otherName := createV2IntegrationTableWithField(t, ctx, f.app, "Other", "Name", "pair_other")
	for _, change := range []string{"target", "pair", "reciprocal"} {
		t.Run(change, func(t *testing.T) {
			draft := fieldDraftForIntegration(t, v2.LogicalRelation, f.pair.Definition.DisplayName)
			relation := *f.pair.Definition.Relation
			draft.Relation = &relation
			switch change {
			case "target":
				relation.TargetTableID, relation.DisplayField = other.TableID, otherName.FieldID
			case "pair":
				relation.PairID += "_replacement"
			case "reciprocal":
				relation.ReciprocalFieldID = otherName.FieldID
			}
			rev, err := f.catalog.Revisions(ctx, f.source.TableID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.planner.Plan(ctx, v2.FieldChangeIntent{
				Action: v2.ActionUpdate, TableID: f.source.TableID, FieldID: f.pair.FieldID,
				ExpectedSchemaRev: rev.Schema, Draft: &draft, Actor: f.actor,
			})
			var productErr *fieldchange.ProductError
			if !errors.As(err, &productErr) {
				t.Fatalf("ordinary update accepted %s replacement: %v", change, err)
			}
		})
	}
}

func TestRelationPairPatchScansPastFirstCardinalityPage(t *testing.T) {
	f := newPairUpdateFixture(t, false)
	f.insert(t, f.target, "pairtarget00001", map[string]any{})
	f.insert(t, f.target, "pairtarget00002", map[string]any{})
	ctx := context.Background()
	rev, err := f.catalog.Revisions(ctx, f.source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	for start := 0; start < 257; start += 64 {
		operations := []mutation.Operation{}
		for index := start; index < min(start+64, 257); index++ {
			id := fmt.Sprintf("pairrow%08d", index)
			values := map[string]any{}
			if index == 256 {
				values[f.pair.Definition.Identity.PhysicalName] = []string{"pairtarget00001", "pairtarget00002"}
			}
			operations = append(operations, mutation.Operation{Kind: mutation.OperationInsert, RecordID: &id, Values: values})
		}
		_, err := mutation.New(f.app, mutation.MetadataSchemaSource{}).Apply(ctx,
			mutationRequest(f.source.TableID, rev.Schema, fmt.Sprintf("pair_page_%d", start), operations...))
		if err != nil {
			t.Fatal(err)
		}
	}
	one := "one"
	plan := f.plan(t, v2.RelationPairPatch{SourceCardinality: &one})
	if plan.CanApply || plan.Impact.Records != 257 || plan.Impact.Ambiguous != 1 {
		t.Fatalf("cardinality scan missed page boundary conflict: %#v", plan)
	}
}
