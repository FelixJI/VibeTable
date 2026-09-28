package migrations

import (
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

func TestClockRevisionMigrationAddsOptionalCounterIdempotently(t *testing.T) {
	t.Parallel()
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(), HideStartBanner: true,
	})
	Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	}()
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}

	tables, err := app.FindCollectionByNameOrId("vibetable_tables")
	if err != nil {
		t.Fatal(err)
	}
	field, ok := tables.Fields.GetByName("clock_revision").(*core.NumberField)
	if !ok || !field.OnlyInt || field.Min == nil || *field.Min != 0 {
		t.Fatalf("clock_revision field = %#v", tables.Fields.GetByName("clock_revision"))
	}
	if field.Required {
		t.Fatal("clock_revision must stay optional so absent means zero")
	}
	// Existing rows keep their data and read as zero through the optional field.
	record := core.NewRecord(tables)
	record.Set("table_id", "tbl_existing")
	record.Set("collection_id", "existing")
	record.Set("physical_name", "existing")
	record.Set("display_name", "Existing")
	record.Set("kind", "base")
	record.Set("schema_revision", 1)
	record.Set("data_revision", 7)
	record.Set("archive_policy", `{"mode":"none"}`)
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	if got := record.GetInt("clock_revision"); got != 0 {
		t.Fatalf("migrated row clock_revision = %d, want 0", got)
	}
	// Re-running the full set must not duplicate or reject the counter.
	if err := app.RunAllMigrations(); err != nil {
		t.Fatalf("second RunAllMigrations(): %v", err)
	}
	again, err := app.FindCollectionByNameOrId("vibetable_tables")
	if err != nil || again.Fields.GetByName("clock_revision") == nil {
		t.Fatalf("clock_revision did not survive the idempotent rerun: %v", err)
	}

	// A pre-existing incompatible shape of the same name fails closed instead
	// of silently redefining stored metadata.
	incompatible := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(), HideStartBanner: true,
	})
	Register(incompatible)
	if err := incompatible.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := incompatible.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	}()
	baseline := core.MigrationsList{}
	for _, migration := range core.AppMigrations.Items() {
		if !strings.HasPrefix(migration.File, "2026092801_") {
			baseline.Register(migration.Up, migration.Down, migration.File)
		}
	}
	if _, err := core.NewMigrationsRunner(incompatible, baseline).Up(); err != nil {
		t.Fatalf("apply baseline migrations: %v", err)
	}
	metadata, err := incompatible.FindCollectionByNameOrId("vibetable_tables")
	if err != nil {
		t.Fatal(err)
	}
	metadata.Fields.Add(&core.TextField{Name: "clock_revision"})
	if err := incompatible.Save(metadata); err != nil {
		t.Fatal(err)
	}
	clockOnly := core.MigrationsList{}
	for _, migration := range core.AppMigrations.Items() {
		if strings.HasPrefix(migration.File, "2026092801_") {
			clockOnly.Register(migration.Up, migration.Down, migration.File)
		}
	}
	if _, err := core.NewMigrationsRunner(incompatible, clockOnly).Up(); err == nil {
		t.Fatal("incompatible clock_revision shape was accepted")
	}
}
