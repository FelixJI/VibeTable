package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestFieldComputationPlanRejectsMixedCycle(t *testing.T) {
	for _, crossTable := range []bool{false, true} {
		t.Run(fmt.Sprintf("cross_table_%v", crossTable), func(t *testing.T) {
			app := bootstrapApp(t, queryTempDir(t))
			defer resetApp(t, app)
			ctx := context.Background()
			table, display := createV2IntegrationTableWithField(t, ctx, app, "Sources", "Name", "mixed_source")
			formulaDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Formula")
			formulaDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "1.0"}
			formula := createV2IntegrationFormula(t, ctx, app, table.TableID, formulaDraft, "mixed_formula")
			target := table
			targetDisplay := display
			if crossTable {
				target, targetDisplay = createV2IntegrationTableWithField(t, ctx, app, "Targets", "Name", "mixed_target")
			}
			link := createV2IntegrationRelation(t, ctx, app, table.TableID, display.FieldID,
				target.TableID, targetDisplay.FieldID, "Target", "Sources", "one", "mixed_link")
			lookupRelationID := link.FieldID
			if crossTable {
				lookupRelationID = link.Related[0].FieldID
			}
			lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Lookup")
			lookupDraft.Lookup = &v2.LookupSpec{
				Path: []v2.LookupPathStep{{RelationFieldID: lookupRelationID}}, TargetFieldID: formula.FieldID,
			}
			lookup := createV2IntegrationField(t, ctx, app, target.TableID, lookupDraft, "mixed_lookup")
			reference := lookup.Definition.Identity.PhysicalName
			if crossTable {
				reference = link.Definition.Identity.PhysicalName + "." + reference
			}
			plan, err := planComputationFormulaUpdate(t, ctx, app, table.TableID, *formula.Definition, "double("+reference+") + 1.0")
			var cycleErr *schemaerror.ProductError
			if !errors.As(err, &cycleErr) || cycleErr.Code != "schema.computation.cycle" || plan.CanApply {
				t.Fatalf("mixed cycle rejection = %#v, canApply=%v", err, plan.CanApply)
			}
			cycle, ok := cycleErr.Details["cycle"].([]map[string]string)
			if !ok || len(cycle) != 3 || !reflect.DeepEqual(cycle[0], cycle[2]) {
				t.Fatalf("mixed cycle path = %#v", cycleErr.Details)
			}
			found := map[string]bool{}
			for _, item := range cycle[:2] {
				found[item["tableId"]+"/"+item["fieldId"]] = true
			}
			if len(found) != 2 || !found[table.TableID+"/"+formula.FieldID] || !found[target.TableID+"/"+lookup.FieldID] {
				t.Fatalf("mixed cycle identities = %#v", cycle)
			}
		})
	}
}

func TestFieldComputationApplyRechecksOtherTableAndRollsBack(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	source, display := createV2IntegrationTableWithField(t, ctx, app, "Sources", "Name", "frozen_source")
	target, targetDisplay := createV2IntegrationTableWithField(t, ctx, app, "Targets", "Name", "frozen_target")
	formulaDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Formula")
	formulaDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "1.0"}
	formula := createV2IntegrationFormula(t, ctx, app, source.TableID, formulaDraft, "frozen_formula")
	link := createV2IntegrationRelation(t, ctx, app, source.TableID, display.FieldID,
		target.TableID, targetDisplay.FieldID, "Target", "Sources", "one", "frozen_link")
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Lookup")
	lookupDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{{RelationFieldID: link.Related[0].FieldID}}, TargetFieldID: formula.FieldID,
	}
	lookup := createV2IntegrationField(t, ctx, app, target.TableID, lookupDraft, "frozen_lookup")
	other := createV2IntegrationFormula(t, ctx, app, target.TableID, formulaDraft, "frozen_other_formula")
	frozen, err := planComputationFormulaUpdate(t, ctx, app, source.TableID, *formula.Definition,
		"double("+link.Definition.Identity.PhysicalName+"."+other.Definition.Identity.PhysicalName+") + 1.0")
	if err != nil || !frozen.CanApply {
		t.Fatalf("acyclic frozen plan = %#v, %v", frozen, err)
	}
	// B changes legally while A still contains the constant formula. A's frozen
	// candidate would close the cycle if its commit trusted only A's revision.
	store := fieldchange.NewPocketBasePlanStore(app)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	changed, err := planComputationFormulaUpdate(t, ctx, app, target.TableID, *other.Definition,
		"double("+lookup.Definition.Identity.PhysicalName+") + 1.0")
	if err != nil || !changed.CanApply {
		t.Fatalf("acyclic target change = %#v, %v", changed, err)
	}
	executor := fieldchange.NewExecutor(app, store, fieldchange.WithFormulaBackfillScheduler(&atomicFormulaScheduler{}))
	if _, err := executor.Apply(ctx, v2.ApplyRequest{PlanID: changed.PlanID, PlanHash: changed.PlanHash, OperationID: "frozen_target_change", Actor: actor}); err != nil {
		t.Fatal(err)
	}
	before := computationMetadataSnapshot(t, app, source.TableID, target.TableID)
	_, err = executor.Apply(ctx, v2.ApplyRequest{PlanID: frozen.PlanID, PlanHash: frozen.PlanHash, OperationID: "frozen_cycle_commit", Actor: actor})
	var cycleErr *schemaerror.ProductError
	if !errors.As(err, &cycleErr) || cycleErr.Code != "schema.computation.cycle" {
		t.Fatalf("commit accepted a cycle formed after planning: %v", err)
	}
	if after := computationMetadataSnapshot(t, app, source.TableID, target.TableID); before != after {
		t.Fatal("rejected commit changed schema, revisions, computed metadata or successful audit")
	}
	// Failed attempts retain their existing failure audit outside the rolled-back
	// schema transaction; this is not a successful schema publication.
	audits, err := app.FindRecordsByFilter("vibetable_schema_audit", "operation_id={:operation}", "+id", 0, 0, dbx.Params{"operation": "frozen_cycle_commit"})
	if err != nil || len(audits) != 1 || audits[0].GetString("outcome") != "failed" {
		t.Fatalf("failed schema attempt audit = %v, %v", audits, err)
	}
	if _, err := app.FindFirstRecordByFilter("vibetable_idempotency_keys", "key={:key}", dbx.Params{"key": "field-v2:frozen_cycle_commit"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rejected commit retained a successful receipt: %v", err)
	}
}

func computationMetadataSnapshot(t *testing.T, app core.App, tableIDs ...string) string {
	t.Helper()
	result := map[string][]map[string]any{}
	for _, name := range []string{"vibetable_tables", "vibetable_fields", "vibetable_formulas", "vibetable_lookups", "vibetable_formula_dependencies", "vibetable_computation_dependencies", "vibetable_schema_audit"} {
		column := "table_id"
		if name == "vibetable_formula_dependencies" || name == "vibetable_computation_dependencies" {
			column = "source_table_id"
		}
		for _, tableID := range tableIDs {
			filter := column + "={:table}"
			if name == "vibetable_schema_audit" {
				filter += " && outcome!='failed'"
			}
			records, err := app.FindRecordsByFilter(name, filter, "+id", 0, 0, dbx.Params{"table": tableID})
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range records {
				result[name] = append(result[name], record.PublicExport())
			}
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestFieldComputationMetadataKeepsVersionsAndPathSemantics(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	source, sourceName := createV2IntegrationTableWithField(t, ctx, app, "Source", "Name", "graph_meta_source")
	middle, middleName := createV2IntegrationTableWithField(t, ctx, app, "Middle", "Name", "graph_meta_middle")
	target, targetName := createV2IntegrationTableWithField(t, ctx, app, "Target", "Name", "graph_meta_target")
	draft := fieldDraftForIntegration(t, v2.LogicalFormula, "Computed value")
	draft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "1.0"}
	terminal := createV2IntegrationFormula(t, ctx, app, target.TableID, draft, "graph_meta_terminal")
	middleValue := createV2IntegrationFormula(t, ctx, app, middle.TableID, draft, "graph_meta_middle_value")
	first := createV2IntegrationRelation(t, ctx, app, source.TableID, sourceName.FieldID, middle.TableID, middleName.FieldID, "Middle", "Sources", "one", "graph_meta_first")
	second := createV2IntegrationRelation(t, ctx, app, middle.TableID, middleName.FieldID, target.TableID, targetName.FieldID, "Target", "Middles", "one", "graph_meta_second")
	path := []v2.LookupPathStep{{RelationFieldID: first.FieldID}, {RelationFieldID: second.FieldID}}
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Lookup")
	lookupDraft.Lookup = &v2.LookupSpec{Path: path, TargetFieldID: terminal.FieldID}
	lookup := createV2IntegrationField(t, ctx, app, source.TableID, lookupDraft, "graph_meta_lookup")
	draft.Formula.Source = "double(" + first.Definition.Identity.PhysicalName + "." + middleValue.Definition.Identity.PhysicalName + ")"
	projected := createV2IntegrationFormula(t, ctx, app, source.TableID, draft, "graph_meta_projected")
	countDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Count")
	countDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "double(relationCount(" + first.Definition.Identity.PhysicalName + "))"}
	count := createV2IntegrationFormula(t, ctx, app, source.TableID, countDraft, "graph_meta_count")
	updated, err := planComputationFormulaUpdate(t, ctx, app, source.TableID, *projected.Definition, draft.Formula.Source+" + 1.0")
	if err != nil || !updated.CanApply {
		t.Fatalf("legal formula update = %#v, %v", updated, err)
	}
	executor := fieldchange.NewExecutor(app, fieldchange.NewPocketBasePlanStore(app), fieldchange.WithFormulaBackfillScheduler(&atomicFormulaScheduler{}))
	if _, err := executor.Apply(ctx, v2.ApplyRequest{PlanID: updated.PlanID, PlanHash: updated.PlanHash, OperationID: "graph_meta_update", Actor: v2.Actor{ID: "local-user", Kind: "user"}}); err != nil {
		t.Fatal(err)
	}
	definition, err := schemaexecution.Describe(ctx, app, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if definition.FormulaRuntime[projected.FieldID].Version != 2 || definition.FormulaRuntime[count.FieldID].Version != 1 {
		t.Fatalf("formula versions = %#v", definition.FormulaRuntime)
	}
	lookupRecord, err := app.FindFirstRecordByFilter("vibetable_lookups", "table_id={:table} && field_id={:field}", dbx.Params{"table": source.TableID, "field": lookup.FieldID})
	if err != nil || lookupRecord.GetInt("revision") != 1 {
		t.Fatalf("unchanged Lookup revision: record=%v err=%v", lookupRecord, err)
	}
	edges, err := app.FindRecordsByFilter("vibetable_computation_dependencies", "source_table_id={:table}", "+computed_kind,+target_field_id", 0, 0, dbx.Params{"table": source.TableID})
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 3 {
		t.Fatalf("external dependency count = %d, want two Lookup hops and one Formula reference (no COUNT value edge)", len(edges))
	}
	seen := map[string]bool{}
	for _, edge := range edges {
		if edge.GetString("relation_field_id") != first.FieldID {
			t.Fatalf("dependency lost first hop: %v", edge.PublicExport())
		}
		var actualPath []v2.LookupPathStep
		raw, err := json.Marshal(edge.GetRaw("path_json"))
		if err != nil || json.Unmarshal(raw, &actualPath) != nil {
			t.Fatalf("dependency path = %s, %v", raw, err)
		}
		if edge.GetString("computed_kind") == "formula" {
			if edge.GetString("computed_field_id") != projected.FieldID || edge.GetString("target_table_id") != middle.TableID || edge.GetString("target_field_id") != middleValue.FieldID || edge.GetInt("definition_version") != 2 || !reflect.DeepEqual(actualPath, path[:1]) {
				t.Fatalf("Formula dependency changed: %v", edge.PublicExport())
			}
		} else {
			if edge.GetString("computed_field_id") != lookup.FieldID || edge.GetInt("definition_version") != 1 || !reflect.DeepEqual(actualPath, path) {
				t.Fatalf("Lookup path/version changed: %v", edge.PublicExport())
			}
			seen[edge.GetString("target_table_id")+"/"+edge.GetString("target_field_id")] = true
		}
	}
	if !seen[middle.TableID+"/__path__"] || !seen[target.TableID+"/"+terminal.FieldID] {
		t.Fatalf("Lookup hop targets = %#v", seen)
	}
	legacy, err := app.FindRecordsByFilter("vibetable_formula_dependencies", "source_table_id={:table}", "+formula_field_id", 0, 0, dbx.Params{"table": source.TableID})
	if err != nil || len(legacy) != 2 {
		t.Fatalf("legacy dependency projection = %d, %v", len(legacy), err)
	}
	for _, edge := range legacy {
		if edge.GetString("relation_field_id") != first.FieldID {
			t.Fatalf("legacy root relation changed: %v", edge.PublicExport())
		}
		switch edge.GetString("formula_field_id") {
		case lookup.FieldID:
			if edge.GetString("dependency_kind") != "lookup" || edge.GetString("target_table_id") != target.TableID || edge.GetString("target_field_id") != terminal.FieldID {
				t.Fatalf("legacy Lookup edge changed: %v", edge.PublicExport())
			}
		case projected.FieldID:
			if edge.GetString("dependency_kind") != "relation" || edge.GetString("target_table_id") != middle.TableID || edge.GetString("target_field_id") != middleValue.FieldID {
				t.Fatalf("legacy Formula edge changed: %v", edge.PublicExport())
			}
		default:
			t.Fatalf("unexpected legacy dependency (including COUNT): %v", edge.PublicExport())
		}
	}
	t.Run("preflight_does_not_commit_parent_transaction", func(t *testing.T) {
		collection, err := app.FindCollectionByNameOrId(source.PhysicalName)
		if err != nil {
			t.Fatal(err)
		}
		marker := core.NewRecord(collection)
		marker.Set(sourceName.Definition.Identity.PhysicalName, "must roll back")
		rollback := errors.New("abort parent transaction")
		err = app.RunInTransaction(func(txApp core.App) error {
			if err := txApp.Save(marker); err != nil {
				return err
			}
			_, _, _, err := fieldchange.NewCatalog(txApp).Check(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: source.TableID}, updated.After, updated.After, []v2.ChangeClass{v2.ClassSchema})
			if err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("nested preflight = %v", err)
		}
		if _, err := app.FindRecordById(collection, marker.Id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("read preflight committed its parent write: %v", err)
		}
	})
}

func planComputationFormulaUpdate(t *testing.T, ctx context.Context, app core.App, tableID string, before v2.FieldDefinition, source string) (v2.FieldChangePlan, error) {
	t.Helper()
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	revisions, err := catalog.Revisions(ctx, tableID)
	if err != nil {
		t.Fatal(err)
	}
	draft := v2.FieldDraft{
		DisplayName: before.DisplayName, Help: before.Help, LogicalType: before.LogicalType,
		Value: before.Value, Constraints: before.Constraints, Storage: before.Storage, Display: before.Display,
		Formula: &v2.FormulaDraftSpec{Language: "cel-v1", Source: source},
	}
	return planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: tableID, FieldID: before.Identity.FieldID,
		ExpectedSchemaRev: revisions.Schema, Draft: &draft, Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
}
