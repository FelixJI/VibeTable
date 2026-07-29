package fieldchange_test

import (
	"context"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestPocketBasePlanStorePersistsAcrossServiceRestart(t *testing.T) {
	t.Parallel()
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(), HideStartBanner: true,
	})
	migrations.Register(app)
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
	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(
		sourceStub{revisions: fieldchange.Revisions{Schema: "schema_1"}},
		nil, store, nil,
		fieldchange.WithClock(func() time.Time { return now }),
	)
	draft := draftFor(v2.LogicalNumber)
	intent := v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: "tbl_orders", ExpectedSchemaRev: "schema_1",
		Draft: &draft, Actor: v2.Actor{ID: "user_local", Kind: "user"},
	}
	first, err := planner.Plan(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}

	restarted := fieldchange.NewPlanner(
		sourceStub{revisions: fieldchange.Revisions{Schema: "schema_1"}},
		nil, fieldchange.NewPocketBasePlanStore(app), nil,
		fieldchange.WithClock(func() time.Time { return now }),
	)
	second, err := restarted.Plan(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanID != second.PlanID ||
		first.After.Identity != second.After.Identity {
		t.Fatalf("persisted plan was not reused:\n%#v\n%#v", first, second)
	}
	loaded, err := store.Load(context.Background(), first.PlanID)
	if err != nil || loaded == nil || loaded.PlanHash != first.PlanHash {
		t.Fatalf("Load() = %#v, %v", loaded, err)
	}
}
