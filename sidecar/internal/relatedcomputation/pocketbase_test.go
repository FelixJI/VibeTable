package relatedcomputation

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestFormulaExpectationAndWrapValuesReadTransactionBoundDependencyRevision(t *testing.T) {
	app := computationTestApp(t)
	saveInternalRecord(t, app, "vibetable_tables", map[string]any{
		"table_id": "tbl_customers", "collection_id": "customers", "physical_name": "customers",
		"display_name": "Customers", "kind": "base", "schema_revision": 2,
		"data_revision": 7, "archive_policy": `{"mode":"none"}`,
	})
	saveInternalRecord(t, app, "vibetable_formula_dependencies", map[string]any{
		"source_table_id": "tbl_orders", "formula_field_id": "fld_total",
		"relation_field_id": "fld_customer", "target_table_id": "tbl_customers",
		"target_field_id": "fld_balance", "dependency_kind": "relation",
	})
	saveInternalRecord(t, app, "vibetable_formulas", map[string]any{
		"table_id": "tbl_orders", "field_id": "fld_total",
		"source": "1 + 1", "language": "vibetable-formula-v1", "result_type": "number",
		"version": 3, "status": "ready",
	})
	field := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_total", PhysicalName: "total"},
		LogicalType: v2.LogicalFormula, Formula: &v2.FormulaSpec{},
	}
	fields := []v2.FieldDefinition{field}

	expectation, err := ExpectationFor(context.Background(), app, "tbl_orders", fields, "fld_total", 11)
	if err != nil {
		t.Fatal(err)
	}
	wantWatermark := Watermark(map[string]int64{"tbl_customers": 7})
	if expectation != (Expectation{3, 11, wantWatermark}) {
		t.Fatalf("expectation = %#v", expectation)
	}
	wrapped, err := WrapValues(
		context.Background(), app, "tbl_orders", fields, 11, map[string]any{"fld_total": 42.5},
	)
	if err != nil {
		t.Fatal(err)
	}
	envelope, ok := Decode(wrapped["total"])
	if !ok || !envelope.Fresh(expectation) || ProjectStored(wrapped["total"]) != 42.5 {
		t.Fatalf("wrapped value = %#v", wrapped)
	}
	if ProjectStored("plain") != "plain" {
		t.Fatal("plain stored value should project unchanged")
	}
}

func TestExpectationRejectsCancelledInvalidAndUnavailableComputedDefinitions(t *testing.T) {
	app := computationTestApp(t)
	fields := []v2.FieldDefinition{}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ExpectationFor(cancelled, app, "tbl_orders", fields, "missing", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}
	formula := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_formula", PhysicalName: "formula"},
		LogicalType: v2.LogicalFormula, Formula: &v2.FormulaSpec{},
	}
	fields = append(fields, formula)
	if _, err := ExpectationFor(context.Background(), app, "tbl_orders", fields, "fld_formula", 1); err == nil {
		t.Fatal("formula without authoritative metadata was accepted")
	}
	if _, err := ExpectationFor(context.Background(), app, "tbl_orders", fields, "missing", 1); err == nil {
		t.Fatal("non-computed field was accepted")
	}
	if _, err := WrapValues(context.Background(), app, "tbl_orders", fields, 1, map[string]any{"missing": 1}); err == nil {
		t.Fatal("unavailable computed field was accepted")
	}
}

func TestFormulaExpectationRejectsMissingDependencyMetadata(t *testing.T) {
	app := computationTestApp(t)
	field := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_formula", PhysicalName: "formula"},
		LogicalType: v2.LogicalFormula,
		Formula:     &v2.FormulaSpec{},
	}
	saveInternalRecord(t, app, "vibetable_formulas", map[string]any{
		"table_id": "tbl_orders", "field_id": "fld_formula",
		"source": "1", "language": "vibetable-formula-v1", "result_type": "number",
		"version": 1, "status": "ready",
	})
	saveInternalRecord(t, app, "vibetable_formula_dependencies", map[string]any{
		"source_table_id": "tbl_orders", "formula_field_id": "fld_formula",
		"relation_field_id": "fld_customer", "target_table_id": "tbl_missing",
		"target_field_id": "fld_balance", "dependency_kind": "relation",
	})
	if _, err := ExpectationFor(
		context.Background(), app, "tbl_orders", []v2.FieldDefinition{field}, "fld_formula", 1,
	); err == nil {
		t.Fatal("formula with missing authoritative dependency metadata was accepted")
	}
}

func TestWrapValuesRejectsNonJSONComputedOutput(t *testing.T) {
	app := computationTestApp(t)
	field := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_lookup", PhysicalName: "lookup"},
		LogicalType: v2.LogicalLookup,
		Lookup:      &v2.LookupSpec{},
	}
	if _, err := WrapValues(
		context.Background(), app, "tbl_orders", []v2.FieldDefinition{field}, 1,
		map[string]any{"fld_lookup": math.Inf(1)},
	); err == nil {
		t.Fatal("non-JSON computed output was accepted")
	}
}

func TestLookupDefinitionVersionDefaultsAndRejectsCorruptStoredRevision(t *testing.T) {
	app := computationTestApp(t)
	field := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_lookup", PhysicalName: "customer_name"},
		LogicalType: v2.LogicalLookup, Lookup: &v2.LookupSpec{},
	}
	fields := []v2.FieldDefinition{field}
	expectation, err := ExpectationFor(context.Background(), app, "tbl_orders", fields, "fld_lookup", 4)
	if err != nil {
		t.Fatal(err)
	}
	if expectation.DefinitionVersion != 1 || expectation.SourceDataRevision != 4 ||
		expectation.DependencyWatermark != Watermark(map[string]int64{}) {
		t.Fatalf("default lookup expectation = %#v", expectation)
	}

	saveInternalRecord(t, app, "vibetable_lookups", map[string]any{
		"lookup_id": "lookup_orders_customer", "path_json": []any{map[string]any{
			"relationFieldId": "fld_customer",
		}},
		"table_id": "tbl_orders", "field_id": "fld_lookup", "relation_field_id": "fld_customer",
		"target_field_id": "fld_name", "output_type": "text", "revision": -1,
	})
	if _, err := ExpectationFor(context.Background(), app, "tbl_orders", fields, "fld_lookup", 4); err == nil {
		t.Fatal("corrupt lookup revision was accepted")
	}
}

func TestDefinitionVersionsAndFormulaDependenciesFailClosed(t *testing.T) {
	app := computationTestApp(t)
	formulaField := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_formula", PhysicalName: "formula"},
		LogicalType: v2.LogicalFormula, Formula: &v2.FormulaSpec{},
	}
	saveInternalRecord(t, app, "vibetable_formulas", map[string]any{
		"table_id": "tbl_orders", "field_id": "fld_formula",
		"source": "1", "language": "cel-v1", "result_type": "number",
		"version": -1, "status": "ready",
	})
	if _, err := definitionVersion(app, "tbl_orders", formulaField); err == nil ||
		!strings.Contains(err.Error(), "formula version is invalid") {
		t.Fatalf("invalid formula version error = %v", err)
	}
	plainField := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_plain", PhysicalName: "plain"},
		LogicalType: v2.LogicalText,
	}
	if _, err := definitionVersion(app, "tbl_orders", plainField); err == nil ||
		!strings.Contains(err.Error(), "field is not computed") {
		t.Fatalf("plain field definition version error = %v", err)
	}
	if tables, err := dependencyTables(
		context.Background(), app, "tbl_orders", nil, plainField,
	); err != nil || len(tables) != 0 {
		t.Fatalf("plain field dependencies = %#v, %v", tables, err)
	}
	lookupField := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_lookup", PhysicalName: "lookup"},
		LogicalType: v2.LogicalLookup, Lookup: &v2.LookupSpec{},
	}
	saveInternalRecord(t, app, "vibetable_lookups", map[string]any{
		"lookup_id": "lookup_orders_customer", "path_json": []any{map[string]any{
			"relationFieldId": "fld_customer",
		}},
		"table_id": "tbl_orders", "field_id": "fld_lookup",
		"relation_field_id": "fld_customer", "target_field_id": "fld_name",
		"output_type": "text", "revision": 3,
	})
	if version, err := definitionVersion(app, "tbl_orders", lookupField); err != nil || version != 3 {
		t.Fatalf("lookup definition version = %d, %v", version, err)
	}

	for index, target := range []string{"tbl_orders", "tbl_customers", "tbl_customers"} {
		saveInternalRecord(t, app, "vibetable_formula_dependencies", map[string]any{
			"source_table_id": "tbl_orders", "formula_field_id": "fld_formula",
			"relation_field_id": "fld_relation_" + string(rune('a'+index)),
			"target_table_id":   target, "target_field_id": "fld_name",
			"dependency_kind": "relation",
		})
	}
	tables, err := dependencyTables(
		context.Background(), app, "tbl_orders", nil, formulaField,
	)
	if err != nil || len(tables) != 1 || tables[0] != "tbl_customers" {
		t.Fatalf("formula dependency tables = %#v, %v", tables, err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := describeTableFields(cancelled, app, "tbl_customers"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled describeTableFields() error = %v", err)
	}
}

func TestLookupDependencyPathRejectsNonStrictNestedV2Definition(t *testing.T) {
	app := computationTestApp(t)
	saveInternalRecord(t, app, "vibetable_fields", map[string]any{
		"table_id": "tbl_customers", "field_id": "fld_region", "physical_name": "region",
		"display_name": "Region", "kind": "relation", "data_type": "relation",
		"storage_type": "relation", "schema_model_version": 2, "lifecycle_state": "active",
		"definition_v2_json": map[string]any{
			"contract": "schema.v2", "unexpected": true,
		},
	})
	relation := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_customer", PhysicalName: "customer"},
		LogicalType: v2.LogicalRelation,
		Relation:    &v2.RelationSpec{TargetTableID: "tbl_customers"},
	}
	lookup := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_lookup", PhysicalName: "lookup"},
		LogicalType: v2.LogicalLookup,
		Lookup: &v2.LookupSpec{Path: []v2.LookupPathStep{
			{RelationFieldID: "fld_customer"},
			{RelationFieldID: "fld_region"},
		}},
	}
	if _, err := dependencyTables(
		context.Background(), app, "tbl_orders", []v2.FieldDefinition{relation, lookup}, lookup,
	); err == nil {
		t.Fatal("lookup accepted a nested definition with unknown Schema V2 members")
	}
}

func TestLookupDependencyPathFailsClosedWhenRelationIsMissing(t *testing.T) {
	app := computationTestApp(t)
	field := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_lookup", PhysicalName: "lookup"},
		LogicalType: v2.LogicalLookup,
		Lookup:      &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: "fld_missing"}}},
	}
	fields := []v2.FieldDefinition{field}

	if _, err := ExpectationFor(context.Background(), app, "tbl_orders", fields, "fld_lookup", 1); err == nil {
		t.Fatal("lookup path with a missing relation was accepted")
	}
}

func TestLookupDependencyPathDescribesEachAuthoritativeTarget(t *testing.T) {
	app := computationTestApp(t)
	if err := app.Save(core.NewBaseCollection("customers")); err != nil {
		t.Fatal(err)
	}
	saveInternalRecord(t, app, "vibetable_tables", map[string]any{
		"table_id": "tbl_customers", "collection_id": "customers", "physical_name": "customers",
		"display_name": "Customers", "kind": "base", "schema_revision": 2,
		"data_revision": 9, "archive_policy": `{"mode":"none"}`,
	})
	if err := app.Save(core.NewBaseCollection("regions")); err != nil {
		t.Fatal(err)
	}
	saveInternalRecord(t, app, "vibetable_tables", map[string]any{
		"table_id": "tbl_regions", "collection_id": "regions", "physical_name": "regions",
		"display_name": "Regions", "kind": "base", "schema_revision": 1,
		"data_revision": 4, "archive_policy": `{"mode":"none"}`,
	})
	customerRegion := v2.FieldDefinition{
		Contract:    v2.Contract,
		Identity:    v2.FieldIdentity{FieldID: "fld_region", PhysicalName: "region"},
		LogicalType: v2.LogicalRelation,
		Relation:    &v2.RelationSpec{TargetTableID: "tbl_regions"},
		Lifecycle:   v2.Lifecycle{State: v2.LifecycleActive},
	}
	saveInternalRecord(t, app, "vibetable_fields", map[string]any{
		"table_id": "tbl_customers", "field_id": "fld_region", "physical_name": "region",
		"display_name": "Region", "kind": "relation", "data_type": "relation",
		"storage_type": "relation", "schema_model_version": 2, "lifecycle_state": "active",
		"definition_v2_json": customerRegion,
	})
	relation := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_customer", PhysicalName: "customer"},
		LogicalType: v2.LogicalRelation,
		Relation:    &v2.RelationSpec{TargetTableID: "tbl_customers"},
	}
	lookup := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_lookup", PhysicalName: "customer_name"},
		LogicalType: v2.LogicalLookup,
		Lookup: &v2.LookupSpec{Path: []v2.LookupPathStep{
			{RelationFieldID: "fld_customer"},
			{RelationFieldID: "fld_region"},
		}},
	}
	fields := []v2.FieldDefinition{relation, lookup}

	expectation, err := ExpectationFor(context.Background(), app, "tbl_orders", fields, "fld_lookup", 5)
	if err != nil {
		t.Fatal(err)
	}
	if expectation.DefinitionVersion != 1 ||
		expectation.DependencyWatermark != Watermark(map[string]int64{
			"tbl_customers": 9,
			"tbl_regions":   4,
		}) {
		t.Fatalf("lookup expectation = %#v", expectation)
	}
}

func computationTestApp(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(), HideStartBanner: true,
	})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	})
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return app
}

func saveInternalRecord(
	t *testing.T,
	app core.App,
	collectionName string,
	values map[string]any,
) {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId(collectionName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	for name, value := range values {
		record.Set(name, value)
	}
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
}
