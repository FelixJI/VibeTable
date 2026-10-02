package schemaapi_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestListIncludesNewlyCreatedEmptyTable(t *testing.T) {
	app := schemaLifecycleStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{
		DisplayName: "Realtime Recovery",
		OperationID: "operation-create-empty-table-12345678",
		Actor:       v2.Actor{ID: "desktop-host", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}

	tables, err := schemaapi.New(app).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables[0].Snapshot.TableID != receipt.TableID ||
		tables[0].Snapshot.DisplayName != receipt.DisplayName || len(tables[0].Snapshot.Fields) != 0 {
		t.Fatalf("List() = %#v", tables)
	}
}

func schemaLifecycleStore(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(), HideStartBanner: true,
	})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		event := &core.TerminateEvent{App: app}
		if err := app.OnTerminate().Trigger(event, func(event *core.TerminateEvent) error {
			return event.App.ResetBootstrapState()
		}); err != nil {
			t.Errorf("terminate fixture: %v", err)
		}
	})
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestSchemaLifecycleStoreTerminatesBeforeReset(t *testing.T) {
	var app *pocketbase.PocketBase
	terminated := false
	bootstrappedAtTermination := false
	t.Run("fixture", func(t *testing.T) {
		app = schemaLifecycleStore(t)
		app.OnTerminate().BindFunc(func(event *core.TerminateEvent) error {
			terminated = true
			bootstrappedAtTermination = event.App.IsBootstrapped()
			return event.Next()
		})
	})
	if !terminated || !bootstrappedAtTermination {
		t.Errorf("termination before reset: called=%v bootstrapped=%v", terminated, bootstrappedAtTermination)
	}
	if app == nil || app.IsBootstrapped() {
		t.Error("fixture did not reset bootstrap state")
	}
}

// Commit on an independent connection after the first metadata SELECT has
// opened its read snapshot. Catalog must not compare two different snapshots.
func TestDescribeKeepsRevisionValidationInReadSnapshot(t *testing.T) {
	app := schemaLifecycleStore(t)
	ctx := context.Background()
	lifecycle, err := schemacore.NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := lifecycle.Create(ctx, v2.TableCreateIntent{
		DisplayName: "Concurrent catalog", OperationID: "catalog-concurrent-revision-12345678",
		Actor: v2.Actor{ID: "desktop-host", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := core.DefaultDBConnect(filepath.Join(app.DataDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Errorf("close writer: %v", err)
		}
	})
	injected := false
	var injectErr error
	for _, db := range []*dbx.DB{app.ConcurrentDB().(*dbx.DB), app.NonconcurrentDB().(*dbx.DB)} {
		previous := db.QueryLogFunc
		db.QueryLogFunc = func(logCtx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, queryErr error) {
			if !injected && strings.Contains(statement, "vibetable_tables") {
				injected = true
				_, injectErr = writer.NewQuery("UPDATE vibetable_tables SET data_revision = data_revision + 1 WHERE table_id = {:table}").Bind(dbx.Params{"table": receipt.TableID}).Execute()
			}
			if previous != nil {
				previous(logCtx, elapsed, statement, rows, queryErr)
			}
		}
		t.Cleanup(func() { db.QueryLogFunc = previous })
	}
	definition, err := schemaapi.New(app).Describe(ctx, receipt.TableID)
	if !injected || injectErr != nil {
		t.Fatalf("interleaved write: injected=%v err=%v", injected, injectErr)
	}
	if err != nil {
		t.Fatalf("Describe rejected a committed business write: %v", err)
	}
	committed, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": receipt.TableID})
	if err != nil {
		t.Fatal(err)
	}
	if definition.Snapshot.DataRevision != 0 || committed.GetInt("data_revision") != 1 {
		t.Fatalf("snapshot revision=%d committed=%d; want 0 and 1", definition.Snapshot.DataRevision, committed.GetInt("data_revision"))
	}
}

func TestDescribeRejectsInvalidPersistedRevisions(t *testing.T) {
	for _, field := range []string{"schema_revision", "data_revision"} {
		for _, value := range []float64{-1, 1.5, 1 << 53} {
			t.Run(fmt.Sprintf("%s/%g", field, value), func(t *testing.T) {
				app := schemaLifecycleStore(t)
				lifecycle, err := schemacore.NewTableLifecycle(app)
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{
					DisplayName: "Invalid revision", OperationID: "catalog-invalid-revision-12345678",
					Actor: v2.Actor{ID: "desktop-host", Kind: "user"},
				})
				if err != nil {
					t.Fatal(err)
				}
				_, err = app.NonconcurrentDB().Update("vibetable_tables", dbx.Params{field: value},
					dbx.HashExp{"table_id": receipt.TableID}).Execute()
				if err != nil {
					t.Fatal(err)
				}
				_, err = schemaapi.New(app).Describe(context.Background(), receipt.TableID)
				var productErr *schemaerror.ProductError
				if !errors.As(err, &productErr) || productErr.Code != "schema.metadata.invalid_"+field {
					t.Fatalf("Describe() error = %v; want invalid_%s", err, field)
				}
			})
		}
	}
}
