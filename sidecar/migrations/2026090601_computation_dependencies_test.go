package migrations

import (
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"testing"
)

func TestPendingMigrationRestoresLegacyComputationDependencies(t *testing.T) {
	app := newMigratedTestApp(t)
	legacy, err := app.FindCollectionByNameOrId("vibetable_computation_dependencies")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Delete(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().NewQuery("DELETE FROM _migrations WHERE file = {:file}").Bind(dbx.Params{"file": "2026090601_computation_dependencies.go"}).Execute(); err != nil {
		t.Fatal(err)
	}
	business := core.NewBaseCollection("legacy_notes")
	business.Fields.Add(&core.TextField{Name: "note"})
	if err := app.Save(business); err != nil {
		t.Fatal(err)
	}
	note := core.NewRecord(business)
	note.Set("note", "历史业务内容")
	if err := app.Save(note); err != nil {
		t.Fatal(err)
	}
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	graph, err := app.FindCollectionByNameOrId("vibetable_computation_dependencies")
	if err != nil {
		t.Fatalf("legacy migration did not create the job recovery dependency graph: %v", err)
	}
	originalID := graph.Id
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	graph, err = app.FindCollectionByNameOrId("vibetable_computation_dependencies")
	if err != nil || graph.Id != originalID {
		t.Fatalf("migration is not idempotent: %v", err)
	}
	restored, err := app.FindRecordById(business.Id, note.Id)
	if err != nil || restored.GetString("note") != "历史业务内容" {
		t.Fatalf("legacy business data changed: %v", err)
	}
}
