package schemaapi_test

import (
	"context"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
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
