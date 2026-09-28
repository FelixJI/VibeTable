package relatedcomputation_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestClockFreshnessTraversesLocalAndRelatedDependencies(t *testing.T) {
	app := computationTestApp(t)
	fields := []v2.FieldDefinition{
		{Identity: v2.FieldIdentity{FieldID: "fld_now", PhysicalName: "now_value"}, LogicalType: v2.LogicalFormula, Formula: &v2.FormulaSpec{Source: "NOW()", Language: "cel-v1", ResultType: v2.LogicalDateTime}},
		{Identity: v2.FieldIdentity{FieldID: "fld_label", PhysicalName: "label"}, LogicalType: v2.LogicalFormula, Formula: &v2.FormulaSpec{Source: `TEXT(now_value, "HH:mm")`, Language: "cel-v1", ResultType: v2.LogicalText}},
		{Identity: v2.FieldIdentity{FieldID: "fld_static", PhysicalName: "static_value"}, LogicalType: v2.LogicalFormula, Formula: &v2.FormulaSpec{Source: "7", Language: "cel-v1", ResultType: v2.LogicalNumber}},
	}
	for _, field := range fields {
		saveInternalRecord(t, app, "vibetable_formulas", map[string]any{"table_id": "tbl_clock", "field_id": field.Identity.FieldID, "source": field.Formula.Source, "language": "cel-v1", "result_type": string(field.Formula.ResultType), "version": 1, "status": "ready"})
		saveInternalRecord(t, app, "vibetable_fields", map[string]any{"table_id": "tbl_clock", "field_id": field.Identity.FieldID, "physical_name": field.Identity.PhysicalName, "display_name": field.Identity.PhysicalName, "kind": "formula", "data_type": "text", "storage_type": "json", "schema_model_version": 2, "lifecycle_state": "active", "definition_v2_json": field})
	}
	lookup := v2.FieldDefinition{Identity: v2.FieldIdentity{FieldID: "fld_lookup", PhysicalName: "lookup_value"}, LogicalType: v2.LogicalLookup, Lookup: &v2.LookupSpec{}}
	for index, target := range []string{"fld_label", "fld_now", "__path__"} {
		saveInternalRecord(t, app, "vibetable_computation_dependencies", map[string]any{"source_table_id": "tbl_other", "computed_field_id": "fld_lookup", "computed_kind": "lookup", "relation_field_id": fmt.Sprintf("fld_relation%d", index), "target_table_id": "tbl_clock", "target_field_id": target, "path_json": []any{map[string]any{"relationFieldId": "relation"}}, "definition_version": 1})
	}
	instant := time.Date(2024, 1, 1, 12, 1, 59, 0, time.UTC)
	ctx := relatedcomputation.WithClockCache(formula.WithEvaluationTime(context.Background(), instant))
	refs, err := relatedcomputation.ClockReferencesFor(ctx, app, "tbl_other", []v2.FieldDefinition{lookup}, "fld_lookup")
	if err != nil || len(refs) != 1 || refs[0].Function != "NOW" {
		t.Fatalf("related clock references=%#v %v", refs, err)
	}
	// Reusing a graph within one transaction deduplicates shared paths.
	again, err := relatedcomputation.ClockReferencesFor(ctx, app, "tbl_other", []v2.FieldDefinition{lookup}, "fld_lookup")
	if err != nil || len(again) != 1 {
		t.Fatalf("cached references=%#v %v", again, err)
	}
	old, err := relatedcomputation.ExpectationFor(ctx, app, "tbl_clock", fields, "fld_label", 2)
	if err != nil {
		t.Fatal(err)
	}
	next := relatedcomputation.WithClockCache(formula.WithEvaluationTime(context.Background(), instant.Add(time.Second)))
	current, err := relatedcomputation.ExpectationFor(next, app, "tbl_clock", fields, "fld_label", 2)
	if err != nil {
		t.Fatal(err)
	}
	if relatedcomputation.Ready("12:01", relatedcomputation.CellVersion{1, 2, old.DependencyWatermark}).Fresh(current) {
		t.Fatal("derived old minute was fresh")
	}
	stable, err := relatedcomputation.ExpectationFor(next, app, "tbl_clock", fields, "fld_static", 2)
	if err != nil || strings.Contains(stable.DependencyWatermark, "|") {
		t.Fatalf("ordinary field became volatile: %#v %v", stable, err)
	}
	// A new transaction sees edited definitions instead of the prior cache.
	fields[0].Formula.Source = "DATE(2024,1,1)"
	refs, err = relatedcomputation.ClockReferencesFor(relatedcomputation.WithClockCache(next), app, "tbl_clock", fields, "fld_label")
	if err != nil || len(refs) != 0 {
		t.Fatalf("schema edit reused clock references: %#v %v", refs, err)
	}
}

func TestClockDependencyMetadataFailsClosed(t *testing.T) {
	app := computationTestApp(t)
	field := v2.FieldDefinition{Identity: v2.FieldIdentity{FieldID: "f", PhysicalName: "value"}, LogicalType: v2.LogicalFormula, Formula: &v2.FormulaSpec{Source: "value"}}
	fields := []v2.FieldDefinition{field}
	if _, err := relatedcomputation.ClockReferencesFor(context.Background(), app, "table", fields, "f"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle=%v", err)
	}
	fields[0].Formula.Source = "NOW("
	if _, err := relatedcomputation.ClockReferencesFor(context.Background(), app, "table", fields, "f"); err == nil {
		t.Fatal("corrupt formula accepted")
	}
	fields[0].Formula.Source = "NOW()"
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := relatedcomputation.ClockReferencesFor(cancelled, app, "table", fields, "f"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	saveInternalRecord(t, app, "vibetable_computation_dependencies", map[string]any{"source_table_id": "table", "computed_field_id": "f", "computed_kind": "formula", "relation_field_id": "relation", "target_table_id": "broken", "target_field_id": "target", "path_json": []any{map[string]any{"relationFieldId": "relation"}}, "definition_version": 1})
	saveInternalRecord(t, app, "vibetable_fields", map[string]any{"table_id": "broken", "field_id": "target", "physical_name": "bad", "display_name": "Bad", "kind": "formula", "data_type": "text", "storage_type": "json", "schema_model_version": 2, "lifecycle_state": "active", "definition_v2_json": map[string]any{"unexpected": true}})
	if _, err := relatedcomputation.ClockReferencesFor(context.Background(), app, "table", fields, "f"); err == nil {
		t.Fatal("corrupt related field was silently dropped")
	}
	if _, err := app.DB().NewQuery("DROP TABLE vibetable_computation_dependencies").Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := relatedcomputation.ClockReferencesFor(context.Background(), app, "table", fields, "f"); err == nil {
		t.Fatal("unavailable dependency storage was accepted")
	}
}
