package migrations

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

const pluginEventMigrationFile = "2026110701_plugin_event_index.go"
const pluginRecordsTable = "vibetable_plugin_records"

func newLegacyPluginEventApp(t *testing.T) (*pocketbase.PocketBase, *core.MigrationsRunner) {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir(), HideStartBanner: true})
	Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Error(err)
		}
	})
	legacy, event := core.MigrationsList{}, core.MigrationsList{}
	for _, migration := range core.AppMigrations.Items() {
		if migration.File == pluginEventMigrationFile {
			event.Register(migration.Up, migration.Down, migration.File)
			break
		}
		legacy.Register(migration.Up, migration.Down, migration.File)
	}
	if len(event.Items()) != 1 {
		t.Fatal("plugin event index migration not registered")
	}
	if _, err := core.NewMigrationsRunner(app, legacy).Up(); err != nil {
		t.Fatal(err)
	}
	return app, core.NewMigrationsRunner(app, event)
}

func insertPluginEventMigrationRow(t *testing.T, app core.App, id string, payload any, seq int) {
	t.Helper()
	if _, err := app.DB().NewQuery(`
INSERT INTO vibetable_plugin_records(id,kind,project_key,plugin_id,item_key,payload_json,seq)
VALUES ({:id},'audit','synthetic-project','com.example.synthetic',{:id},{:payload},{:seq})`).
		Bind(dbx.Params{"id": id, "payload": payload, "seq": seq}).Execute(); err != nil {
		t.Fatal(err)
	}
}

type pluginEventPreservation struct {
	Rows []struct {
		ID      string `db:"id"`
		Kind    string `db:"kind"`
		Project string `db:"project_key"`
		Plugin  string `db:"plugin_id"`
		Key     string `db:"item_key"`
		Payload string `db:"payload"`
		Seq     int    `db:"seq"`
	}
	Triggers []string
	Revision int64
}

func readPluginEventPreservation(t *testing.T, app core.App) pluginEventPreservation {
	t.Helper()
	var state pluginEventPreservation
	if err := app.DB().NewQuery(`
SELECT id, kind, project_key, plugin_id, item_key, quote(payload_json) AS payload, seq
FROM vibetable_plugin_records ORDER BY seq, id`).All(&state.Rows); err != nil {
		t.Fatal(err)
	}
	if err := app.DB().NewQuery(`
SELECT sql FROM sqlite_schema WHERE type='trigger' AND tbl_name='vibetable_plugin_records' ORDER BY name`).Column(&state.Triggers); err != nil {
		t.Fatal(err)
	}
	if len(state.Triggers) != 3 {
		t.Fatalf("preservation trigger count = %d, want 3", len(state.Triggers))
	}
	if err := app.DB().NewQuery("SELECT revision FROM vibetable_plugin_preservation_revision WHERE singleton=1").Row(&state.Revision); err != nil {
		t.Fatal(err)
	}
	return state
}

func assertPluginEventIndexPresence(t *testing.T, app core.App, enabled bool) {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId(pluginRecordsTable)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := app.FindCachedCollectionByNameOrId(pluginRecordsTable)
	if err != nil {
		t.Fatal(err)
	}
	var physicalCount int
	if err := app.DB().NewQuery("SELECT COUNT(*) FROM sqlite_schema WHERE name={:name} AND type='index' AND tbl_name={:table}").
		Bind(dbx.Params{"name": pluginEventIndexName, "table": pluginRecordsTable}).Row(&physicalCount); err != nil {
		t.Fatal(err)
	}
	if (collection.GetIndex(pluginEventIndexName) != "") != enabled || (cached.GetIndex(pluginEventIndexName) != "") != enabled || (physicalCount == 1) != enabled {
		t.Fatalf("metadata, cache and physical index must agree on enabled=%v", enabled)
	}
}

func TestPluginEventIndexMigrationPreservesRowsAndReplays(t *testing.T) {
	app, runner := newLegacyPluginEventApp(t)
	insertPluginEventMigrationRow(t, app, "eventoldrow0001", `{"eventType":"model.progress","details":{"contract":"vibetable.plugin-model.v1"}}`, 3)
	insertPluginEventMigrationRow(t, app, "eventoldrow0002", `{"eventType":"transaction.rules","details":{}}`, 8)
	insertPluginEventMigrationRow(t, app, "eventoldrow0003", "[malformed", 9)
	insertPluginEventMigrationRow(t, app, "eventoldrow0004", "{'eventType':'model.progress'}", 12)
	before := readPluginEventPreservation(t, app)
	assertPreserved := func(enabled bool) {
		t.Helper()
		assertPluginEventIndexPresence(t, app, enabled)
		if actual := readPluginEventPreservation(t, app); !reflect.DeepEqual(before, actual) {
			t.Fatal("migration changed records, sequence, or preservation state")
		}
	}
	assertPreserved(false)
	if applied, err := runner.Up(); err != nil || !reflect.DeepEqual(applied, []string{pluginEventMigrationFile}) {
		t.Fatalf("old database upgrade = %v, %v", applied, err)
	}
	assertPreserved(true)
	if err := setPluginEventIndex(app, true); err != nil {
		t.Fatal(err)
	}
	if applied, err := runner.Up(); err != nil || len(applied) != 0 {
		t.Fatalf("idempotent upgrade = %v, %v", applied, err)
	}
	assertPreserved(true)

	collection, err := app.FindCollectionByNameOrId(pluginRecordsTable)
	if err != nil {
		t.Fatal(err)
	}
	collection.Fields.Add(&core.TextField{Name: "temporary_schema_field"})
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	assertPreserved(true)
	collection.Fields.RemoveByName("temporary_schema_field")
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	assertPreserved(true)

	if reverted, err := runner.Down(1); err != nil || !reflect.DeepEqual(reverted, []string{pluginEventMigrationFile}) {
		t.Fatalf("rollback = %v, %v", reverted, err)
	}
	assertPreserved(false)
	if err := setPluginEventIndex(app, false); err != nil {
		t.Fatal(err)
	}
	if applied, err := runner.Up(); err != nil || !reflect.DeepEqual(applied, []string{pluginEventMigrationFile}) {
		t.Fatalf("replay = %v, %v", applied, err)
	}
	assertPreserved(true)

	if _, err := app.DB().NewQuery("UPDATE vibetable_plugin_records SET seq=13 WHERE id='eventoldrow0004'").Execute(); err != nil {
		t.Fatal(err)
	}
	if revision := readPluginEventPreservation(t, app).Revision; revision != before.Revision+1 {
		t.Fatal("preservation trigger did not observe a write after replay")
	}
}

func TestPluginEventIndexMigrationRejectsMismatchedIndex(t *testing.T) {
	for _, scenario := range []string{"wrong metadata", "physical only", "other table", "missing physical"} {
		t.Run(scenario, func(t *testing.T) {
			app, runner := newLegacyPluginEventApp(t)
			insertPluginEventMigrationRow(t, app, "eventoldrow0001", `{"eventType":"model.progress"}`, 1)
			if scenario == "wrong metadata" || scenario == "missing physical" {
				collection, err := app.FindCollectionByNameOrId(pluginRecordsTable)
				if err != nil {
					t.Fatal(err)
				}
				columns := pluginEventIndexColumns
				if scenario == "wrong metadata" {
					columns = "`seq`"
				}
				collection.AddIndex(pluginEventIndexName, false, columns, "")
				if err := app.Save(collection); err != nil {
					t.Fatal(err)
				}
			}
			var statement string
			switch scenario {
			case "physical only":
				statement = "CREATE INDEX " + pluginEventIndexName + " ON vibetable_plugin_records (" + pluginEventIndexColumns + ")"
			case "other table":
				statement = "CREATE INDEX " + pluginEventIndexName + " ON vibetable_plugin_import_markers (source_key)"
			case "missing physical":
				statement = "DROP INDEX " + pluginEventIndexName
			}
			if statement != "" {
				if _, err := app.DB().NewQuery(statement).Execute(); err != nil {
					t.Fatal(err)
				}
			}
			before := readPluginEventPreservation(t, app)
			if _, err := runner.Up(); err == nil || !strings.Contains(err.Error(), "index") {
				t.Fatalf("expected incompatible index failure, got %v", err)
			}
			if after := readPluginEventPreservation(t, app); !reflect.DeepEqual(before, after) {
				t.Fatal("rejected migration changed preserved data")
			}
		})
	}
}

func TestPluginEventIndexMigrationRollsBackIndexBuildFailure(t *testing.T) {
	app, runner := newLegacyPluginEventApp(t)
	insertPluginEventMigrationRow(t, app, "eventoldrow0001", `{"eventType":"model.progress"}`, 3)
	insertPluginEventMigrationRow(t, app, "eventoldrow0002", []byte{0x80}, 8)
	before := readPluginEventPreservation(t, app)
	if _, err := runner.Up(); err == nil || !strings.Contains(err.Error(), "Failed to create index "+pluginEventIndexName) {
		t.Fatalf("expected an expression index build failure, got %v", err)
	}
	assertPluginEventIndexPresence(t, app, false)
	if after := readPluginEventPreservation(t, app); !reflect.DeepEqual(before, after) {
		t.Fatal("failed index build changed records or triggers")
	}
	collection, err := app.FindCollectionByNameOrId(pluginRecordsTable)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePluginRecordPhysicalIndexes(app, collection); err != nil {
		t.Fatal(err)
	}
}
