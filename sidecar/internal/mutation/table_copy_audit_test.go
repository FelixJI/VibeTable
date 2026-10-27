package mutation_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestCopiedRowAuditNormalizesHostAndRejectsUnavailableBindings(t *testing.T) {
	for _, scenario := range []string{"host", "missing-table", "unavailable-rows"} {
		t.Run(scenario, func(t *testing.T) {
			app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir(), HideStartBanner: true})
			migrations.Register(app)
			if err := app.Bootstrap(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := app.ResetBootstrapState(); err != nil {
					t.Error(err)
				}
			})
			if err := app.RunAllMigrations(); err != nil {
				t.Fatal(err)
			}
			lifecycle, _ := schemacore.NewTableLifecycle(app)
			ctx := context.Background()
			actor := v2.Actor{ID: "desktop-host", Kind: "host"}
			source, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "审计来源", OperationID: "copy-audit-source", Actor: actor})
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": source.TableID})
			if err != nil {
				t.Fatal(err)
			}
			collection, err := app.FindCollectionByNameOrId(metadata.GetString("collection_id"))
			if err != nil {
				t.Fatal(err)
			}
			row := core.NewRecord(collection)
			if err := app.Save(row); err != nil {
				t.Fatal(err)
			}
			if scenario == "host" {
				copy, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "宿主副本", OperationID: "host-copy", SourceTableID: source.TableID, Actor: actor})
				if err != nil {
					t.Fatal(err)
				}
				audit, err := app.FindFirstRecordByFilter("vibetable_audit_events", "table_id={:table}", dbx.Params{"table": copy.TableID})
				if err != nil {
					t.Fatal(err)
				}
				if audit.GetString("actor_type") != "system" || audit.GetString("actor_id") != actor.ID || audit.GetString("operation") != "insert" || audit.GetString("record_id") == row.Id {
					t.Fatalf("host copy audit=%#v", audit.PublicExport())
				}
				outbox, err := app.FindFirstRecordByFilter("vibetable_audit_outbox", "event_id='table-copy:host-copy:1'")
				if err != nil {
					t.Fatal(err)
				}
				var payload struct {
					Actor    mutation.Actor `json:"actor"`
					RecordID string         `json:"recordId"`
				}
				if err := json.Unmarshal([]byte(outbox.GetString("payload_json")), &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Actor.Type != "system" || payload.Actor.ID != actor.ID || payload.RecordID != audit.GetString("record_id") {
					t.Fatalf("host outbox payload=%s", outbox.GetString("payload_json"))
				}
				return
			}
			tableID := "tbl_missing"
			if scenario == "unavailable-rows" {
				tableID = source.TableID
				metadata.Set("physical_name", "missing_copy_records")
				if err := app.Save(metadata); err != nil {
					t.Fatal(err)
				}
			}
			before := map[string]int64{}
			for _, name := range []string{"vibetable_audit_events", "vibetable_audit_outbox"} {
				count, err := app.CountRecords(name)
				if err != nil {
					t.Fatal(err)
				}
				before[name] = count
			}
			err = app.RunInTransaction(func(tx core.App) error {
				return mutation.AuditCopiedRows(ctx, tx, tableID, "unavailable-copy", actor, time.Now())
			})
			if err == nil || (scenario == "missing-table" && !errors.Is(err, schemaexecution.ErrTableNotFound)) {
				t.Fatalf("unavailable binding error=%v", err)
			}
			for name, want := range before {
				if count, err := app.CountRecords(name); err != nil || count != want {
					t.Fatalf("unavailable copy wrote %s: count=%d want=%d error=%v", name, count, want, err)
				}
			}
			if _, err := app.FindRecordById(collection, row.Id); err != nil {
				t.Fatalf("unavailable audit changed the source row: %v", err)
			}
		})
	}
}
