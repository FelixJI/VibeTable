package integration_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestProgressDisplayAndRatingPreflightKeepRawValues(t *testing.T) {
	dataDir := queryTempDir(t)
	app := bootstrapApp(t, dataDir)
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "Display presets", "display_presets")
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	draft := fieldDraftForIntegration(t, v2.LogicalNumber, "Progress")
	created := applyCreatedField(t, ctx, catalog, planner, executor, table.TableID, draft, actor, "create_display_number")
	revisions, err := catalog.Revisions(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	rowID := "displayrow00001"
	_, err = mutation.New(app, mutation.MetadataSchemaSource{}).Apply(ctx, mutationRequest(table.TableID, revisions.Schema, "display_number_seed", mutation.Operation{
		Kind: mutation.OperationInsert, RecordID: &rowID, Values: map[string]any{created.Definition.Identity.PhysicalName: 1.5},
	}))
	if err != nil {
		t.Fatal(err)
	}
	draft.Display.Preset = "progress"
	for index, bounds := range [][2]float64{{0, 1}, {1, 2}} {
		draft.Display.ProgressStart, draft.Display.ProgressTarget = &bounds[0], &bounds[1]
		revisions, err = catalog.Revisions(ctx, table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: table.TableID, FieldID: created.Definition.Identity.FieldID, ExpectedSchemaRev: revisions.Schema, Draft: &draft, Actor: actor})
		if err != nil || !plan.CanApply || len(plan.Impact.Failures) != 0 {
			t.Fatalf("display plan: %#v %v", plan, err)
		}
		_, err = executor.Apply(ctx, v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: []string{"display_default", "display_bounds"}[index], Actor: actor})
		if err != nil {
			t.Fatal(err)
		}
		after, err := catalog.Revisions(ctx, table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		row, err := app.FindRecordById(table.PhysicalName, rowID)
		if err != nil {
			t.Fatal(err)
		}
		if row.GetFloat(created.Definition.Identity.PhysicalName) != 1.5 || after.Data != revisions.Data {
			t.Fatal("display changed raw value or data revision")
		}
	}
	revisions, err = catalog.Revisions(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	unknown := draft
	unknown.Display.Preset = "future"
	if _, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: table.TableID, FieldID: created.Definition.Identity.FieldID, ExpectedSchemaRev: revisions.Schema, Draft: &unknown, Actor: actor}); err == nil {
		t.Fatal("unknown display preset entered a persisted plan")
	}
	unchanged, err := catalog.Revisions(ctx, table.TableID)
	if err != nil || unchanged != revisions {
		t.Fatal("unknown display preset wrote schema/data")
	}
	before, err := catalog.Field(ctx, table.TableID, created.Definition.Identity.FieldID)
	if err != nil {
		t.Fatal(err)
	}
	draft.Display.Preset, draft.Display.ProgressStart, draft.Display.ProgressTarget = "rating", nil, nil
	draft.Storage.Options.OnlyInt = true
	draft.Constraints.Range.Min, draft.Constraints.Range.Max = 0, 5
	blocked, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: table.TableID, FieldID: created.Definition.Identity.FieldID, ExpectedSchemaRev: revisions.Schema, Draft: &draft, Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if blocked.CanApply || len(blocked.Impact.Failures) == 0 {
		t.Fatalf("incompatible 1.5 did not fail real preflight: %#v", blocked)
	}
	_, err = executor.Apply(ctx, v2.ApplyRequest{PlanID: blocked.PlanID, PlanHash: blocked.PlanHash, OperationID: "blocked_rating", Actor: actor})
	if err == nil {
		t.Fatal("blocked rating applied")
	}
	after, err := catalog.Revisions(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := catalog.Field(ctx, table.TableID, created.Definition.Identity.FieldID)
	if err != nil {
		t.Fatal(err)
	}
	if after != revisions || !reflect.DeepEqual(definition, before) {
		t.Fatal("rating preflight wrote schema/data")
	}
	rating := applyCreatedField(t, ctx, catalog, planner, executor, table.TableID, draft, actor, "create_explicit_rating")
	for index, value := range []any{0, 5, nil, -1, 6, 0.5} {
		rev, err := catalog.Revisions(ctx, table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = mutation.New(app, mutation.MetadataSchemaSource{}).Apply(ctx, mutationRequest(table.TableID, rev.Schema,
			[]string{"rating_zero", "rating_five", "rating_null", "rating_negative", "rating_over", "rating_fraction"}[index], mutation.Operation{
				Kind: mutation.OperationInsert, Values: map[string]any{rating.Definition.Identity.PhysicalName: value},
			}))
		if (index >= 3) != (err != nil) {
			t.Fatalf("rating value %#v error=%v", value, err)
		}
		if index >= 3 {
			next, err := catalog.Revisions(ctx, table.TableID)
			if err != nil || next != rev {
				t.Fatal("invalid rating changed data")
			}
		}
	}

}
