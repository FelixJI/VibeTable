package integration_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestLegacyMigrationRebuildsFormulaAndLookupDependencies(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	target := createV2IntegrationTable(t, ctx, app, "Lines", "migration_lines_table")
	amount := createV2IntegrationField(t, ctx, app, target.TableID,
		fieldDraftForIntegration(t, v2.LogicalNumber, "Amount"), "migration_amount")
	source := createV2IntegrationTable(t, ctx, app, "Orders", "migration_orders_table")
	name := createV2IntegrationField(t, ctx, app, source.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Order"), "migration_order_name")
	lines := createV2IntegrationRelation(t, ctx, app, source.TableID, name.FieldID,
		target.TableID, amount.FieldID, "Lines", "Orders", "many", "migration_lines_relation")
	sumDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Line sum")
	sumDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "SUM({Lines}.{Amount})"}
	sum := createV2IntegrationFormula(t, ctx, app, source.TableID, sumDraft, "migration_sum")
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Line amounts")
	lookupDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{{RelationFieldID: lines.FieldID}}, TargetFieldID: amount.FieldID,
	}
	lookup := createV2IntegrationField(t, ctx, app, source.TableID, lookupDraft, "migration_lookup")
	metadata := func() map[string][]string {
		t.Helper()
		result := map[string][]string{}
		for _, collection := range []string{"vibetable_tables", "vibetable_fields", "vibetable_formulas", "vibetable_lookups", "vibetable_formula_dependencies"} {
			records, err := app.FindRecordsByFilter(collection, "", "+id", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range records {
				raw, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				result[collection] = append(result[collection], string(raw))
			}
		}
		return result
	}
	before := metadata()
	graph, err := app.FindCollectionByNameOrId("vibetable_computation_dependencies")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Delete(graph); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().NewQuery("DELETE FROM _migrations WHERE file = {:file}").Bind(dbx.Params{
		"file": "2026090601_computation_dependencies.go",
	}).Execute(); err != nil {
		t.Fatal(err)
	}
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	edges, err := app.FindRecordsByFilter("vibetable_computation_dependencies", "source_table_id={:table}", "+computed_kind", 0, 0, dbx.Params{"table": source.TableID})
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 2 {
		t.Fatalf("restored dependency count = %d, want formula and lookup", len(edges))
	}
	for index, expected := range []struct{ kind, field string }{{"formula", sum.FieldID}, {"lookup", lookup.FieldID}} {
		edge := edges[index]
		if edge.GetString("computed_kind") != expected.kind || edge.GetString("computed_field_id") != expected.field ||
			edge.GetString("relation_field_id") != lines.FieldID || edge.GetString("target_table_id") != target.TableID ||
			edge.GetString("target_field_id") != amount.FieldID || edge.GetInt("definition_version") < 1 {
			t.Fatalf("restored %s edge = %#v", expected.kind, edge.PublicExport())
		}
		var path []v2.LookupPathStep
		if err := json.Unmarshal([]byte(edge.GetString("path_json")), &path); err != nil ||
			!reflect.DeepEqual(path, lookupDraft.Lookup.Path) {
			t.Fatalf("restored path = %#v, error %v", path, err)
		}
	}
	if !reflect.DeepEqual(before, metadata()) {
		t.Fatal("migration changed authoritative schema or computed metadata")
	}
	// A pending migration on an already complete database must preserve graph records too.
	edgeID := edges[0].Id
	if _, err := app.DB().NewQuery("DELETE FROM _migrations WHERE file = {:file}").Bind(dbx.Params{
		"file": "2026090601_computation_dependencies.go",
	}).Execute(); err != nil {
		t.Fatal(err)
	}
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.FindRecordById("vibetable_computation_dependencies", edgeID); err != nil {
		t.Fatalf("existing graph was replaced: %v", err)
	}

	// A failed graph write must roll back earlier edges and the new collection.
	graph, err = app.FindCollectionByNameOrId("vibetable_computation_dependencies")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Delete(graph); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().NewQuery("DELETE FROM _migrations WHERE file = {:file}").Bind(dbx.Params{
		"file": "2026090601_computation_dependencies.go",
	}).Execute(); err != nil {
		t.Fatal(err)
	}
	injected := false
	app.OnRecordCreate("vibetable_computation_dependencies").BindFunc(func(event *core.RecordEvent) error {
		if event.Record.GetString("computed_kind") == "lookup" {
			injected = true
			return context.Canceled
		}
		return event.Next()
	})
	if err := app.RunAllMigrations(); err == nil || !injected {
		t.Fatalf("migration failure = %v", err)
	}
	if app.HasTable("vibetable_computation_dependencies") {
		t.Fatal("failed migration left a partial graph")
	}
	var applied int
	if err := app.DB().NewQuery("SELECT count(*) FROM _migrations WHERE file = {:file}").Bind(dbx.Params{
		"file": "2026090601_computation_dependencies.go",
	}).Row(&applied); err != nil || applied != 0 {
		t.Fatalf("failed migration recorded as applied: %d, %v", applied, err)
	}
	if !reflect.DeepEqual(before, metadata()) {
		t.Fatal("failed migration changed authoritative metadata")
	}
}
