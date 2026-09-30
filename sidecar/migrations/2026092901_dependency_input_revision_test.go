package migrations

import (
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
)

func TestDependencyInputMigrationFreshSchema(t *testing.T) {
	app := newMigratedTestApp(t)
	collection, err := app.FindCollectionByNameOrId("vibetable_computation_dependencies")
	if err != nil {
		t.Fatal(err)
	}
	field, ok := collection.Fields.GetByName(relatedcomputation.InputRevisionField).(*core.NumberField)
	if !ok || !field.OnlyInt || field.Required || field.Min == nil || *field.Min != 0 {
		t.Fatalf("invalid input revision field: %#v", field)
	}
	edge := core.NewRecord(collection)
	for name, value := range map[string]any{"source_table_id": "tbl_outer", "computed_field_id": "fld_total", "computed_kind": "formula", "target_table_id": "tbl_source", "target_field_id": "fld_amount", "path_json": `{"language":"cel-v2","source":"1.0"}`, "definition_version": 1} {
		edge.Set(name, value)
	}
	for _, invalid := range []float64{-1, 0.5} {
		edge.Set(relatedcomputation.InputRevisionField, invalid)
		if err := app.Save(edge); err == nil {
			t.Fatalf("accepted invalid input revision %v", invalid)
		}
	}
	edge.Set(relatedcomputation.InputRevisionField, 0)
	if err := app.Save(edge); err != nil {
		t.Fatal(err)
	}
	edge.Set(relatedcomputation.InputRevisionField, 7)
	if err := app.Save(edge); err != nil {
		t.Fatal(err)
	}
	for _, migration := range core.AppMigrations.Items() {
		if strings.HasPrefix(migration.File, "2026092901_") {
			if err := migration.Up(app); err != nil {
				t.Fatal(err)
			}
		}
	}
	edge, err = app.FindRecordById(collection, edge.Id)
	if err != nil || edge.GetInt(relatedcomputation.InputRevisionField) != 7 {
		t.Fatalf("reapplying migration changed input revision: %v, %v", edge, err)
	}
}
