package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// Counts real reads without retaining SQL text or fixture values.
func TestTransactionAuthorityQueryBaseline(t *testing.T) {
	f := newTransactionFixture(t)
	type counts struct {
		Queries int `json:"queries"`
		History int `json:"modelHistoryQueries"`
		Fields  int `json:"fieldQueries"`
	}
	var lock sync.Mutex
	phase := "preview"
	measured := map[string]*counts{"preview": {}, "commit": {}}
	seen := map[*dbx.DB]bool{}
	for _, database := range []*dbx.DB{f.pb.ConcurrentDB().(*dbx.DB), f.pb.NonconcurrentDB().(*dbx.DB)} {
		if seen[database] {
			continue
		}
		seen[database] = true
		previous := database.QueryLogFunc
		database.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
			if previous != nil {
				previous(ctx, elapsed, statement, rows, err)
			}
			lock.Lock()
			defer lock.Unlock()
			current := measured[phase]
			current.Queries++
			if strings.Contains(statement, "vibetable_plugin_records") && strings.Contains(statement, "LIMIT 256") {
				current.History++
			}
			if strings.Contains(statement, "FROM `vibetable_fields`") {
				current.Fields++
			}
		}
		t.Cleanup(func() { database.QueryLogFunc = previous })
	}
	started := time.Now()
	raw, err := f.call("transaction.preview", map[string]any{"plan": threeTablePlan()})
	if err != nil {
		t.Fatal(err)
	}
	previewTime := time.Since(started)
	lock.Lock()
	phase = "commit"
	lock.Unlock()
	started = time.Now()
	raw, err = f.call("transaction.commit", map[string]any{"requestId": raw.(transactionPreview).RequestID})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.(transactionResult).Receipt.Rows) != 3 {
		t.Fatal("three-table transaction lost an authoritative receipt row")
	}
	lock.Lock()
	defer lock.Unlock()
	wire, err := json.Marshal(measured)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("AUTHORITY_QUERY_COUNTS %s previewMs=%d commitMs=%d", wire, previewTime.Milliseconds(), time.Since(started).Milliseconds())
}

func TestTransactionAuthorityReadsRemainBoundedWithinOneTransaction(t *testing.T) {
	f := newTransactionFixture(t)
	authorityCtx := withTransactionAuthority(t.Context(), f.workspace, f.input)
	var lock sync.Mutex
	history := 0
	seen := map[*dbx.DB]bool{}
	for _, database := range []*dbx.DB{f.pb.ConcurrentDB().(*dbx.DB), f.pb.NonconcurrentDB().(*dbx.DB)} {
		if seen[database] {
			continue
		}
		seen[database] = true
		previous := database.QueryLogFunc
		database.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
			if previous != nil {
				previous(ctx, elapsed, statement, rows, err)
			}
			if strings.Contains(statement, "vibetable_plugin_records") && strings.Contains(statement, "LIMIT 256") {
				lock.Lock()
				history++
				lock.Unlock()
			}
		}
		t.Cleanup(func() { database.QueryLogFunc = previous })
	}
	authorize := transactionAuthorize(f.input, f.workspace)
	err := f.pb.RunInTransaction(func(tx core.App) error {
		if err := beginTransactionAuthority(authorityCtx, tx); err != nil {
			return err
		}
		first := 0
		for i := range 6 {
			for _, binding := range f.model.Bindings {
				if err := authorize(authorityCtx, tx, binding.TableID, "insert"); err != nil {
					return err
				}
			}
			lock.Lock()
			observed := history
			lock.Unlock()
			if i == 0 {
				first = observed
			} else if observed != first {
				t.Fatalf("unchanged transaction reparsed model history: first=%d now=%d", first, observed)
			}
		}
		if err := authorize(authorityCtx, tx, "outside-authorized-model", "insert"); err == nil {
			t.Fatal("cached authority granted an unrelated table")
		}
		cancelled, cancel := context.WithCancel(authorityCtx)
		cancel()
		if err := authorize(cancelled, tx, f.model.Bindings["headers"].TableID, "insert"); !errors.Is(err, context.Canceled) {
			t.Fatalf("cached authority ignored cancellation: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	lock.Lock()
	previousHistory := history
	lock.Unlock()
	if err := f.pb.RunInTransaction(func(tx core.App) error {
		if err := beginTransactionAuthority(authorityCtx, tx); err != nil {
			return err
		}
		return authorize(authorityCtx, tx, f.model.Bindings["headers"].TableID, "insert")
	}); err != nil {
		t.Fatal(err)
	}
	lock.Lock()
	defer lock.Unlock()
	if history <= previousHistory {
		t.Fatal("a new unchanged transaction reused the previous transaction's model history")
	}
}

func TestTransactionAuthorityRefreshesAfterWritesAndTransactionEnd(t *testing.T) {
	for _, change := range []string{"installation", "history", "schema", "ddl", "savepoint"} {
		t.Run(change, func(t *testing.T) {
			f := newTransactionFixture(t)
			authorityCtx := withTransactionAuthority(t.Context(), f.workspace, f.input)
			var history atomic.Int64
			database := f.pb.NonconcurrentDB().(*dbx.DB)
			previous := database.QueryLogFunc
			database.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
				if previous != nil {
					previous(ctx, elapsed, statement, rows, err)
				}
				if strings.Contains(statement, "vibetable_plugin_records") && strings.Contains(statement, "LIMIT 256") {
					history.Add(1)
				}
			}
			t.Cleanup(func() { database.QueryLogFunc = previous })
			authorize := transactionAuthorize(f.input, f.workspace)
			tableID := f.model.Bindings["headers"].TableID
			rollback := errors.New("rollback synthetic authority change")
			err := f.pb.RunInTransaction(func(tx core.App) error {
				if err := beginTransactionAuthority(authorityCtx, tx); err != nil {
					return err
				}
				if err := authorize(authorityCtx, tx, tableID, "insert"); err != nil {
					return err
				}
				switch change {
				case "installation", "savepoint":
					if change == "savepoint" {
						if _, err := tx.DB().NewQuery("SAVEPOINT authority_test").Execute(); err != nil {
							return err
						}
					}
					if _, err := pluginstore.New(tx, f.workspace).SetEnabled(t.Context(), f.input.ProjectKey, f.input.PluginID, false); err != nil {
						return err
					}
				case "history":
					installed, err := surfaceInstallation(t.Context(), tx, f.workspace, f.input)
					if err != nil {
						return err
					}
					_, err = pluginstore.New(tx, f.workspace).RecordAuditInTransaction(t.Context(), modelAudit(f.input, installed, "malformed-authority-history", "model.progress", map[string]any{"contract": "invalid"}))
					if err != nil {
						return err
					}
				case "schema":
					table, err := schemaexecution.Describe(t.Context(), tx, tableID)
					if err != nil {
						return err
					}
					collection, err := tx.FindCollectionByNameOrId(table.CollectionID)
					if err != nil {
						return err
					}
					field, _ := table.Field(f.model.Bindings["headers"].Fields["code"])
					collection.Fields.GetById(field.Identity.ProviderFieldID).SetHidden(true)
					if err := tx.Save(collection); err != nil {
						return err
					}
				case "ddl":
					before := history.Load()
					if _, err := tx.DB().NewQuery("CREATE TABLE authority_ddl_test (id TEXT)").Execute(); err != nil {
						return err
					}
					if err := authorize(authorityCtx, tx, tableID, "insert"); err != nil {
						return err
					}
					if history.Load() <= before {
						t.Fatal("DDL without DML did not refresh model authorization")
					}
					return rollback
				}
				if err := authorize(authorityCtx, tx, tableID, "insert"); err == nil {
					t.Fatal("same-transaction authority change reused an old grant")
				}
				if change == "savepoint" {
					if _, err := tx.DB().NewQuery("ROLLBACK TO authority_test").Execute(); err != nil {
						return err
					}
					if err := authorize(authorityCtx, tx, tableID, "insert"); err != nil {
						return err
					}
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				t.Fatal(err)
			}
			if err := f.pb.RunInTransaction(func(tx core.App) error {
				if err := beginTransactionAuthority(authorityCtx, tx); err != nil {
					return err
				}
				return authorize(authorityCtx, tx, tableID, "insert")
			}); err != nil {
				t.Fatalf("rollback leaked a retired authorization snapshot: %v", err)
			}
			if _, err := pluginstore.New(f.pb, f.workspace).SetEnabled(t.Context(), f.input.ProjectKey, f.input.PluginID, false); err != nil {
				t.Fatal(err)
			}
			if err := f.pb.RunInTransaction(func(tx core.App) error {
				if err := beginTransactionAuthority(authorityCtx, tx); err != nil {
					return err
				}
				return authorize(authorityCtx, tx, tableID, "insert")
			}); err == nil {
				t.Fatal("new transaction reused authorization from before committed revocation")
			}
		})
	}
}

func TestTransactionAuthorityCannotRetainSavepointPermissionExpansion(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(map[bool]string{false: "first-read-after-write", true: "already-cached"}[warm], func(t *testing.T) {
			f := newTransactionFixture(t)
			ctx := withTransactionAuthority(t.Context(), f.workspace, f.input)
			authorize := transactionAuthorize(f.input, f.workspace)
			const target = "synthetic-unbound-table"
			err := f.pb.RunInTransaction(func(tx core.App) error {
				if err := beginTransactionAuthority(ctx, tx); err != nil {
					return err
				}
				if warm {
					if err := authorize(ctx, tx, f.model.Bindings["headers"].TableID, "insert"); err != nil {
						return err
					}
				}
				if err := authorize(t.Context(), tx, target, "insert"); err == nil {
					t.Fatal("baseline installation already grants the expansion target")
				}
				if _, err := tx.DB().NewQuery("SAVEPOINT expand_authority").Execute(); err != nil {
					return err
				}
				record, err := tx.FindFirstRecordByFilter("vibetable_plugin_records", "kind='installation' && project_key={:project} && plugin_id={:plugin}", dbx.Params{"project": f.input.ProjectKey, "plugin": f.input.PluginID})
				if err != nil {
					return err
				}
				raw, err := json.Marshal(record.GetRaw("payload_json"))
				if err != nil {
					return err
				}
				var installed map[string]any
				if err := json.Unmarshal(raw, &installed); err != nil {
					return err
				}
				installed["manifest"].(map[string]any)["permissions"].(map[string]any)["data"] = []pluginReadGrant{{Collection: target, Operations: []string{"create"}, Fields: []string{"id"}}}
				record.Set("payload_json", installed)
				if err := tx.Save(record); err != nil {
					return err
				}
				if err := authorize(ctx, tx, target, "insert"); err != nil {
					return err
				}
				if _, err := tx.DB().NewQuery("ROLLBACK TO expand_authority").Execute(); err != nil {
					return err
				}
				if err := authorize(ctx, tx, target, "insert"); err == nil {
					t.Fatal("rolled-back permission expansion retained a usable grant")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTransactionAuthorityHookChangesFailAtomically(t *testing.T) {
	for _, change := range []string{"installation", "history", "epoch", "cancel"} {
		t.Run(change, func(t *testing.T) {
			f := newTransactionFixture(t)
			raw, err := f.call("transaction.preview", map[string]any{"plan": threeTablePlan()})
			if err != nil {
				t.Fatal(err)
			}
			before := transactionBusinessSnapshot(t, f)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			changed := false
			f.kernel = mutation.New(f.pb, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(transactionFormulaFunc(func(_ context.Context, tx core.App, _ schemaexecution.Table, _ *core.Record) (map[string]any, error) {
				if !changed {
					changed = true
					switch change {
					case "installation":
						if _, err := pluginstore.New(tx, f.workspace).SetEnabled(ctx, f.input.ProjectKey, f.input.PluginID, false); err != nil {
							return nil, err
						}
					case "history":
						installed, err := surfaceInstallation(ctx, tx, f.workspace, f.input)
						if err != nil {
							return nil, err
						}
						if _, err := pluginstore.New(tx, f.workspace).RecordAuditInTransaction(ctx, modelAudit(f.input, installed, "hook-malformed-authority-history", "model.progress", map[string]any{"contract": "invalid"})); err != nil {
							return nil, err
						}
					case "epoch":
						f.epoch++
					case "cancel":
						cancel()
					}
				}
				return nil, nil
			})))
			if _, err := f.callWith(ctx, "transaction.commit", map[string]any{"requestId": raw.(transactionPreview).RequestID}, nil); err == nil || !changed {
				t.Fatalf("hook authority change was not rejected: changed=%v err=%v", changed, err)
			}
			if after := transactionBusinessSnapshot(t, f); after != before {
				t.Fatal("authority change left partial data, history or outbox")
			}
		})
	}
}

func TestTransactionAuthorityPreflightRevalidatesBeforeApply(t *testing.T) {
	for _, scenario := range []struct {
		change string
		code   string
	}{{"revoke", "plugin_read_denied"}, {"schema_revision", "mutation.schema_revision_conflict"}} {
		t.Run(scenario.change, func(t *testing.T) {
			f := newTransactionFixture(t)
			plan := threeTablePlan()
			raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
			if err != nil {
				t.Fatal(err)
			}
			preview := raw.(transactionPreview)
			tableID := f.model.Bindings["headers"].TableID
			var beforeApply string
			gate := func(ctx context.Context, kind, identity string, apply func(context.Context) error) error {
				if kind != "plugin.transaction.commit" || identity != preview.RequestID {
					t.Fatalf("unexpected business write: %s", kind)
				}
				if scenario.change == "revoke" {
					_, err = pluginstore.New(f.pb, f.workspace).SetEnabled(ctx, f.input.ProjectKey, f.input.PluginID, false)
				} else {
					table, readErr := f.pb.FindFirstRecordByData("vibetable_tables", "table_id", tableID)
					if readErr != nil {
						t.Fatal(readErr)
					}
					table.Set("display_name", "Synthetic compatible rename")
					table.Set("schema_revision", table.GetInt("schema_revision")+1)
					err = f.pb.Save(table)
				}
				if err != nil {
					t.Fatal(err)
				}
				beforeApply = transactionBusinessSnapshot(t, f)
				return apply(ctx)
			}
			_, err = f.callWith(t.Context(), "transaction.commit", map[string]any{"requestId": preview.RequestID}, gate)
			if beforeApply == "" || err == nil || !strings.Contains(err.Error(), scenario.code) {
				t.Fatalf("apply did not reject current %s: %v", scenario.change, err)
			}
			if beforeApply != transactionBusinessSnapshot(t, f) {
				t.Fatal("rejected apply changed rows, clocks, audit or outbox")
			}
			if scenario.change == "schema_revision" {
				current, err := schemaexecution.Describe(t.Context(), f.pb, tableID)
				if err != nil {
					t.Fatal(err)
				}
				fresh, err := f.call("transaction.preview", map[string]any{"plan": plan})
				if err != nil {
					t.Fatal(err)
				}
				want := current.Snapshot.SchemaRevision
				if got := fresh.(transactionPreview).Model.Revisions[tableID]; got != want || got == preview.Model.Revisions[tableID] {
					t.Fatalf("new preview got schema revision %q, want current revision %q", got, want)
				}
			}
		})
	}
}

func TestTransactionAuthorityPreflightStatusAndReplayCheckCurrentInstallation(t *testing.T) {
	f := newTransactionFixture(t)
	result := f.submit(t, threeTablePlan())
	input := f.input
	ports := &pluginSurfaceIO{transactionEpoch: func() uint64 { return f.epoch }}
	call := func() (any, error) {
		return executePluginSurfaceAPI(t.Context(), f.pb, nil, f.kernel, f.workspace, input, nil, ports)
	}
	input.Request, _ = json.Marshal(map[string]any{"requestId": result.RequestID, "previewRevision": 1})
	for _, method := range []string{"transaction.status", "transaction.commit"} {
		input.Method = method
		got, err := call()
		if receipt, ok := got.(transactionResult); err != nil || !ok || receipt.Receipt.ChangeSetID != result.Receipt.ChangeSetID {
			t.Fatalf("%s did not read the original applied result: %v", method, err)
		}
	}
	appliedRequest := input.Request
	input.Request = json.RawMessage("{")
	if _, err := call(); err == nil || !strings.Contains(err.Error(), "transaction.request_invalid") {
		t.Fatalf("authorized malformed request returned %v", err)
	}
	if _, err := pluginstore.New(f.pb, f.workspace).SetEnabled(t.Context(), input.ProjectKey, input.PluginID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := call(); err == nil || !strings.Contains(err.Error(), "plugin_read_denied") {
		t.Fatalf("installation authorization must precede request decoding: %v", err)
	}
	input.Request = appliedRequest
	for _, method := range []string{"transaction.status", "transaction.commit"} {
		input.Method = method
		if _, err := call(); err == nil || !strings.Contains(err.Error(), "plugin_read_denied") {
			t.Fatalf("%s reused authority across calls: %v", method, err)
		}
	}
}

func TestTransactionAuthorityProgressRetainsOldBindingsAndRejectsOldCorruption(t *testing.T) {
	f := newTransactionFixture(t)
	original := f.model.Bindings["headers"]
	later := f.model
	later.RequestID = "later-synthetic-model"
	later.Bindings = map[string]pluginmodel.Binding{"unrelated": {TableID: "synthetic-unrelated", Fields: map[string]string{"name": "synthetic-field"}}}
	installed, err := surfaceInstallation(t.Context(), f.pb, f.workspace, f.input)
	if err != nil {
		t.Fatal(err)
	}
	appendProgress := func(id string, detail any) {
		t.Helper()
		err := f.pb.RunInTransaction(func(tx core.App) error {
			_, err := pluginstore.New(tx, f.workspace).RecordAuditInTransaction(t.Context(), modelAudit(f.input, installed, id, "model.progress", detail))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendProgress("later-valid-synthetic-progress", later)
	if _, err := pluginmodel.States(t.Context(), f.pb, f.input.ProjectKey, f.input.PluginID); err != nil {
		t.Fatal(err)
	}
	if err := pluginmodel.ProtectTable(t.Context(), f.pb, original.TableID); err == nil || !strings.Contains(err.Error(), "plugin.model.core_protected") {
		t.Fatalf("later history removed old table protection: %v", err)
	}
	if err := pluginmodel.ProtectField(t.Context(), f.pb, original.TableID, original.Fields["code"], &v2.FieldDefinition{}, nil); err == nil || !strings.Contains(err.Error(), "plugin.model.core_protected") {
		t.Fatalf("later history removed old field protection: %v", err)
	}
	appendProgress("bad-synthetic-progress", map[string]any{"contract": "invalid"})
	appendProgress("newest-valid-synthetic-progress", later)
	if states, err := pluginmodel.States(t.Context(), f.pb, f.input.ProjectKey, f.input.PluginID); err == nil || states != nil {
		t.Fatal("newest valid progress hid old corrupt history")
	}
}
