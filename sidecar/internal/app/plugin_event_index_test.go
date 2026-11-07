package app

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
)

func TestPluginEventIndexPreservesPocketBaseQueries(t *testing.T) {
	f := newTransactionFixture(t)
	const migrationFile = "2026110701_plugin_event_index.go"
	const indexName = "idx_vibetable_plugin_records_event_lookup"
	list := core.MigrationsList{}
	for _, migration := range core.AppMigrations.Items() {
		if migration.File == migrationFile {
			list.Register(migration.Up, migration.Down, migration.File)
		}
	}
	runner := core.NewMigrationsRunner(f.pb, list)
	if reverted, err := runner.Down(1); err != nil || !reflect.DeepEqual(reverted, []string{migrationFile}) {
		t.Fatalf("restore old schema: %v, %v", reverted, err)
	}
	raw, err := f.call("transaction.preview", map[string]any{"plan": threeTablePlan()})
	if err != nil {
		t.Fatal(err)
	}
	requestID := raw.(transactionPreview).RequestID
	for i, fixture := range []struct{ id, payload string }{
		{"eventbadjson001", "[malformed"},
		{"eventjsonfive01", "{'eventType':'model.progress','details':{}}"},
	} {
		if _, err := f.pb.DB().NewQuery(`
INSERT INTO vibetable_plugin_records(id,kind,project_key,plugin_id,item_key,payload_json,seq)
VALUES ({:id},'audit',{:project},{:plugin},{:id},{:payload},{:seq})`).
			Bind(dbx.Params{"id": fixture.id, "project": f.input.ProjectKey, "plugin": f.input.PluginID, "payload": fixture.payload, "seq": 1000000 + i}).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		states  []pluginmodel.State
		rules   []transactionrules.Registration
		preview transactionPreview
	}
	events := []string{"model.progress", "transaction.rules", "transaction.preview"}
	read := func(wantIndex bool) result {
		t.Helper()
		var snap result
		db := f.pb.NonconcurrentDB().(*dbx.DB)
		previous := db.QueryLogFunc
		queries := map[string]string{}
		var mu sync.Mutex
		db.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
			if previous != nil {
				previous(ctx, elapsed, statement, rows, err)
			}
			if err != nil || !strings.HasPrefix(statement, "SELECT") || !strings.Contains(statement, "vibetable_plugin_records") {
				return
			}
			for _, event := range events {
				if strings.Contains(statement, "IS '"+event+"'") {
					mu.Lock()
					queries[event] = statement
					mu.Unlock()
				}
			}
		}
		defer func() { db.QueryLogFunc = previous }()
		ctx := t.Context()
		err := f.pb.RunInTransaction(func(tx core.App) error {
			var err error
			snap.states, err = pluginmodel.States(ctx, tx, f.input.ProjectKey, f.input.PluginID)
			if err != nil {
				return err
			}
			snap.rules, err = transactionrules.Load(ctx, tx)
			if err != nil {
				return err
			}
			found, err := loadTransactionEvent(ctx, tx, f.workspace, f.input, requestID, "preview", &snap.preview)
			if err == nil && !found {
				return errors.New("preview was lost")
			}
			return err
		})
		if err != nil || len(snap.states) == 0 || len(snap.rules) != 1 {
			t.Fatalf("read event results: %v", err)
		}
		for _, event := range events {
			mu.Lock()
			statement := queries[event]
			mu.Unlock()
			if statement == "" {
				t.Fatalf("missing executed %s query", event)
			}
			// DBX interpolates its query log. Rebind only the fixture's known values
			// so EXPLAIN also exercises the parameterized comparison.
			params := dbx.Params{}
			for key, value := range map[string]string{"kind": "audit", "event": event, "project": f.input.ProjectKey, "plugin": f.input.PluginID, "request": requestID} {
				literal := "'" + strings.ReplaceAll(value, "'", "''") + "'"
				if strings.Contains(statement, literal) {
					params[key] = value
					statement = strings.ReplaceAll(statement, literal, "{:"+key+"}")
				}
			}
			var plan []struct {
				Detail string `db:"detail"`
			}
			if err := db.NewQuery("EXPLAIN QUERY PLAN " + statement).Bind(params).All(&plan); err != nil {
				t.Fatal(err)
			}
			used := false
			for _, step := range plan {
				used = used || strings.Contains(step.Detail, indexName) && strings.Contains(step.Detail, "<expr>=?")
			}
			if used != wantIndex {
				t.Fatalf("%s uses event expression index = %v, want %v. Plan %v", event, used, wantIndex, plan)
			}
		}
		return snap
	}
	before := read(false)
	if applied, err := runner.Up(); err != nil || !reflect.DeepEqual(applied, []string{migrationFile}) {
		t.Fatalf("upgrade old schema: %v, %v", applied, err)
	}
	if after := read(true); !reflect.DeepEqual(before, after) {
		t.Fatal("index changed history order, rules, or preview")
	}
}
