package migrations

import (
	"errors"
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
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
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
