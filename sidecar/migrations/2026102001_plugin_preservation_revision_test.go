package migrations

import (
	"errors"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

func TestPluginPreservationRevisionTracksRawWritesAndRollsBackAtomically(t *testing.T) {
	app := newMigratedTestApp(t)
	revision := func() int64 {
		t.Helper()
		var value int64
		if err := app.DB().NewQuery("SELECT revision FROM vibetable_plugin_preservation_revision WHERE singleton = 1").Row(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if revision() != 0 {
		t.Fatal("nonzero initial revision")
	}
	insert := `INSERT INTO vibetable_plugin_records(id,kind,project_key,plugin_id,item_key,payload_json,seq) VALUES ('preserved000001','setting','local:test','com.example.test','private','{}',0)`
	if _, err := app.DB().NewQuery(insert).Execute(); err != nil {
		t.Fatal(err)
	}
	if revision() != 1 {
		t.Fatal("insert did not advance revision")
	}
	rollback := errors.New("rollback the caller-owned transaction")
	if err := app.RunInTransaction(func(tx core.App) error {
		if _, err := tx.DB().NewQuery(`UPDATE vibetable_plugin_records SET payload_json='{"value":1}'`).Execute(); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if revision() != 1 {
		t.Fatal("rolled-back write advanced revision")
	}
	if _, err := app.DB().NewQuery(`UPDATE vibetable_plugin_records SET payload_json='{"value":2}'`).Execute(); err != nil {
		t.Fatal(err)
	}
	if revision() != 2 {
		t.Fatal("update did not advance revision")
	}
	if _, err := app.DB().NewQuery(`DELETE FROM vibetable_plugin_records`).Execute(); err != nil {
		t.Fatal(err)
	}
	if revision() != 3 {
		t.Fatal("delete did not advance revision")
	}
	list := core.MigrationsList{}
	for _, migration := range core.AppMigrations.Items() {
		if strings.HasPrefix(migration.File, "2026102001_") {
			list.Register(migration.Up, migration.Down, migration.File)
		}
	}
	runner := core.NewMigrationsRunner(app, list)
	if reverted, err := runner.Down(1); err != nil || len(reverted) != 1 {
		t.Fatalf("Down(1) = %v, %v", reverted, err)
	}
	if applied, err := runner.Up(); err != nil || len(applied) != 1 {
		t.Fatalf("Up() after rollback = %v, %v", applied, err)
	}
	if revision() != 3 {
		t.Fatal("migration replay reset the revision")
	}
	if _, err := app.DB().NewQuery(`UPDATE vibetable_plugin_preservation_revision SET revision=9223372036854775807`).Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().NewQuery(insert).Execute(); err == nil {
		t.Fatal("overflow silently lost the monotonic counter")
	}
}

func TestPluginPreservationRevisionRejectsIncompatibleSchemaOnReapply(t *testing.T) {
	for _, corrupt := range []string{"table", "trigger", "counter"} {
		t.Run(corrupt, func(t *testing.T) {
			app := newMigratedTestApp(t)
			list := core.MigrationsList{}
			for _, migration := range core.AppMigrations.Items() {
				if strings.HasPrefix(migration.File, "2026102001_") {
					list.Register(migration.Up, migration.Down, migration.File)
				}
			}
			runner := core.NewMigrationsRunner(app, list)
			if _, err := runner.Down(1); err != nil {
				t.Fatal(err)
			}
			statement := ""
			switch corrupt {
			case "table":
				statement = `ALTER TABLE vibetable_plugin_preservation_revision ADD COLUMN unexpected TEXT`
			case "trigger":
				if _, err := app.DB().NewQuery(`DROP TRIGGER plugin_preservation_update`).Execute(); err != nil {
					t.Fatal(err)
				}
				statement = `CREATE TRIGGER plugin_preservation_update AFTER UPDATE ON vibetable_plugin_records BEGIN SELECT 1; END`
			case "counter":
				statement = `DELETE FROM vibetable_plugin_preservation_revision`
			}
			if _, err := app.DB().NewQuery(statement).Execute(); err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Up(); err == nil {
				t.Fatal("accepted incompatible preservation state")
			}
		})
	}
}
