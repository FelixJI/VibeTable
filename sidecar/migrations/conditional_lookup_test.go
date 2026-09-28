package migrations

import (
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"strings"
	"testing"
)

func TestConditionalLookupMigrationPreservesDataOnRefusedDowngrade(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir(), HideStartBanner: true})
	Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer app.ResetBootstrapState()
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	list := core.MigrationsList{}
	for _, migration := range core.AppMigrations.Items() {
		if strings.HasPrefix(migration.File, "2026092801_") {
			list.Register(migration.Up, migration.Down, migration.File)
		}
	}
	runner := core.NewMigrationsRunner(app, list)
	collection, err := app.FindCollectionByNameOrId("vibetable_computation_dependencies")
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	for key, value := range map[string]any{
		"source_table_id": "current", "computed_field_id": "lookup", "computed_kind": "lookup",
		"relation_field_id": "", "target_table_id": "source", "target_field_id": "title",
		"path_json": map[string]any{"condition": true}, "definition_version": 1,
	} {
		record.Set(key, value)
	}
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Down(1); err == nil || !strings.Contains(err.Error(), "cannot downgrade") {
		t.Fatalf("downgrade=%v", err)
	}
	for _, name := range []string{"vibetable_lookups", "vibetable_computation_dependencies"} {
		current, err := app.FindCollectionByNameOrId(name)
		if err != nil {
			t.Fatal(err)
		}
		if current.Fields.GetByName("relation_field_id").(*core.TextField).Required {
			t.Fatal("failed downgrade partially altered", name)
		}
	}
	if _, err := app.FindRecordById(collection, record.Id); err != nil {
		t.Fatal("refused downgrade lost data", err)
	}
	if err := app.Delete(record); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Down(1); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(); err != nil {
		t.Fatal(err)
	}
}
