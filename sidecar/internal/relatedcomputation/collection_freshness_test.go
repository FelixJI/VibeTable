package relatedcomputation_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestCollectionSourceEditInvalidatesTransitiveStoredCells(t *testing.T) {
	app := computationTestApp(t)
	// leaf.amount -> middle.total (TABLE) -> middle.adjusted (local scalar)
	// -> outer.total (TABLE). The intermediate rows are not edited when leaf changes.
	amount := v2.FieldDefinition{Identity: v2.FieldIdentity{FieldID: "fld_amount", PhysicalName: "amount"}, LogicalType: v2.LogicalNumber}
	computedField := func(id, name, language, source string) v2.FieldDefinition {
		return v2.FieldDefinition{Identity: v2.FieldIdentity{FieldID: id, PhysicalName: name}, LogicalType: v2.LogicalFormula,
			Formula: &v2.FormulaSpec{Language: language, Source: source, ResultType: v2.LogicalNumber}}
	}
	total := computedField("fld_total", "total", "cel-v2", `SUM(PROJECT(TABLE("tbl_leaf"), CurrentValue.amount))`)
	adjusted := computedField("fld_adjusted", "adjusted", "cel-v1", "total + 1.0")
	outer := computedField("fld_outer", "total", "cel-v2", `SUM(PROJECT(TABLE("tbl_middle"), CurrentValue.adjusted))`)
	fields := map[string][]v2.FieldDefinition{"tbl_leaf": {amount}, "tbl_middle": {total, adjusted}, "tbl_outer": {outer}}
	rows := map[string]*core.Record{}
	for _, tableID := range []string{"tbl_leaf", "tbl_middle", "tbl_outer"} {
		collection := core.NewBaseCollection(tableID)
		collection.Fields.Add(&core.NumberField{Name: relatedcomputation.RowRevisionField})
		for _, field := range fields[tableID] {
			if field.Formula == nil {
				collection.Fields.Add(&core.NumberField{Name: field.Identity.PhysicalName})
			} else {
				collection.Fields.Add(&core.JSONField{Name: field.Identity.PhysicalName})
				saveInternalRecord(t, app, "vibetable_formulas", map[string]any{
					"table_id": tableID, "field_id": field.Identity.FieldID, "source": field.Formula.Source,
					"language": field.Formula.Language, "result_type": "number", "version": 1, "status": "ready",
				})
			}
			saveInternalRecord(t, app, "vibetable_fields", map[string]any{
				"table_id": tableID, "field_id": field.Identity.FieldID, "physical_name": field.Identity.PhysicalName,
				"display_name": field.Identity.PhysicalName, "kind": string(field.LogicalType), "data_type": "number", "storage_type": "json",
				"schema_model_version": 2, "lifecycle_state": "active", "definition_v2_json": field,
			})
		}
		if err := app.Save(collection); err != nil {
			t.Fatal(err)
		}
		saveInternalRecord(t, app, "vibetable_tables", map[string]any{
			"table_id": tableID, "collection_id": collection.Id, "physical_name": tableID,
			"display_name": tableID, "kind": "base", "schema_revision": 1, "data_revision": 1, "archive_policy": `{"mode":"none"}`,
		})
		row := core.NewRecord(collection)
		row.Set(relatedcomputation.RowRevisionField, 1)
		if tableID == "tbl_leaf" {
			row.Set("amount", 7.0)
		}
		if err := app.Save(row); err != nil {
			t.Fatal(err)
		}
		rows[tableID] = row
	}
	// Collection membership and projections live in the unified graph, not the
	// legacy relation-formula dependency table. Include both persisted edge kinds.
	for _, edge := range []struct{ table, field, target, projection string }{
		{"tbl_middle", total.Identity.FieldID, "tbl_leaf", amount.Identity.FieldID},
		{"tbl_outer", outer.Identity.FieldID, "tbl_middle", adjusted.Identity.FieldID},
	} {
		for _, targetField := range []string{"__path__", edge.projection} {
			saveInternalRecord(t, app, "vibetable_computation_dependencies", map[string]any{
				"source_table_id": edge.table, "computed_field_id": edge.field, "computed_kind": "formula",
				"relation_field_id": "", "target_table_id": edge.target, "target_field_id": targetField,
				"path_json": fields[edge.table][0].Formula, "definition_version": 1,
				relatedcomputation.InputRevisionField: 1,
			})
		}
	}
	materialize := func(amount float64) {
		t.Helper()
		ctx := relatedcomputation.WithClockCache(context.Background())
		for _, tableID := range []string{"tbl_middle", "tbl_outer"} {
			values := map[string]any{"total": amount + 1}
			if tableID == "tbl_middle" {
				values = map[string]any{"total": amount, "adjusted": amount + 1}
			}
			wrapped, err := relatedcomputation.WrapValues(ctx, app, tableID, fields[tableID], 1, values)
			if err != nil {
				t.Fatal(err)
			}
			for name, value := range wrapped {
				rows[tableID].Set(name, value)
			}
			if err := app.Save(rows[tableID]); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertReadable := func(wantFresh bool, amount float64) {
		t.Helper()
		ctx := relatedcomputation.WithClockCache(context.Background())
		// Exercise the same transaction-local graph cache shared by clock discovery
		// and subsequent freshness reads, without caching revision values across batches.
		if _, err := relatedcomputation.ClockReferencesFor(ctx, app, "tbl_outer", fields["tbl_outer"], outer.Identity.FieldID); err != nil {
			t.Fatal(err)
		}
		reader := relatedcomputation.NewSourceReader()
		for _, tableID := range []string{"tbl_middle", "tbl_outer"} {
			record, err := app.FindRecordById(tableID, rows[tableID].Id)
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range fields[tableID] {
				expectation, err := relatedcomputation.ExpectationFor(ctx, app, tableID, fields[tableID], field.Identity.FieldID, 1)
				if err != nil {
					t.Fatal(err)
				}
				envelope, ok := relatedcomputation.Decode(record.GetRaw(field.Identity.PhysicalName))
				if !ok || envelope.Fresh(expectation) != wantFresh {
					t.Fatalf("%s/%s freshness=%t, want %t", tableID, field.Identity.FieldID, envelope.Fresh(expectation), wantFresh)
				}
				value, err := reader.Read(ctx, app, tableID, fields[tableID], field, record)
				if !wantFresh {
					var dependencyErr *formula.Error
					if !errors.As(err, &dependencyErr) || dependencyErr.Code != "formula.dependency" || value != nil {
						t.Fatalf("%s/%s returned stale scalar: %#v, %v", tableID, field.Identity.FieldID, value, err)
					}
				} else {
					want := amount + 1
					if tableID == "tbl_middle" && field.Identity.FieldID == total.Identity.FieldID {
						want = amount
					}
					if err != nil || value != want {
						t.Fatalf("%s/%s = %#v, %v; want %v", tableID, field.Identity.FieldID, value, err, want)
					}
				}
			}
		}
	}
	materialize(7)
	assertReadable(true, 7)
	// Commit a leaf business edit. No formula row, definition version, intermediate
	// table revision or outer revision changes, so only transitive freshness can reject it.
	if err := app.RunInTransaction(func(txApp core.App) error {
		leaf, err := txApp.FindRecordById("tbl_leaf", rows["tbl_leaf"].Id)
		if err != nil {
			return err
		}
		leaf.Set("amount", 9.0)
		leaf.Set(relatedcomputation.RowRevisionField, 2)
		if err := txApp.Save(leaf); err != nil {
			return err
		}
		if err := relatedcomputation.AdvanceInputRevisions(context.Background(), txApp, "tbl_leaf", fields["tbl_leaf"],
			map[string]any{"amount": 7.0}, map[string]any{"amount": 9.0}, "update", 2); err != nil {
			return err
		}
		table, err := txApp.FindFirstRecordByFilter("vibetable_tables", "table_id='tbl_leaf'")
		if err != nil {
			return err
		}
		table.Set("data_revision", 2)
		return txApp.Save(table)
	}); err != nil {
		t.Fatal(err)
	}
	assertReadable(false, 7)
	for _, tableID := range []string{"tbl_middle", "tbl_outer"} {
		table, err := app.FindFirstRecordByFilter("vibetable_tables", fmt.Sprintf("table_id='%s'", tableID))
		if err != nil || table.GetInt("data_revision") != 1 {
			t.Fatalf("intermediate authority changed: %s, %v", tableID, err)
		}
	}
	materialize(9)
	assertReadable(true, 9)
	// An upstream definition edit does not advance any business data revision.
	// Its old same-table and cross-table consumers must nevertheless be stale.
	definition, err := app.FindFirstRecordByFilter("vibetable_formulas", "table_id='tbl_middle' && field_id='fld_total'")
	if err != nil {
		t.Fatal(err)
	}
	definition.Set("version", 2)
	if err := app.Save(definition); err != nil {
		t.Fatal(err)
	}
	assertReadable(false, 9)
	materialize(9)
	assertReadable(true, 9)
}
