package integration_test

import (
	"context"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

var testBackupReceiptKey = []byte("0123456789abcdef0123456789abcdef")

var expectedInternalCollections = []string{
	"vibetable_tables", "vibetable_fields", "vibetable_formulas", "vibetable_relations",
	"vibetable_lookups", "vibetable_audit_events", "vibetable_idempotency_keys",
	"vibetable_outbox", "vibetable_jobs", "vibetable_attachment_meta",
	"vibetable_attachment_versions", "vibetable_shared_settings", "vibetable_dashboards",
	"vibetable_panels", "vibetable_presets",
	"vibetable_content_versions", "vibetable_workspace_index",
	"vibetable_schema_change_plans", "vibetable_schema_audit",
}

func TestSchemaCatalogFreshMigrationReadsV2SchemaAcrossRestart(t *testing.T) {
	dataDir := t.TempDir()
	app := bootstrapApp(t, dataDir)
	defer func() {
		if app != nil {
			resetApp(t, app)
		}
	}()
	if err := app.RunAllMigrations(); err != nil {
		t.Fatalf("second RunAllMigrations(): %v", err)
	}
	for _, name := range expectedInternalCollections {
		if !app.HasTable(name) {
			t.Fatalf("internal collection %q was not migrated", name)
		}
	}
	fieldsMeta, err := app.FindCollectionByNameOrId("vibetable_fields")
	if err != nil {
		t.Fatal(err)
	}
	if fieldsMeta.GetIndex("uniq_vibetable_fields_table_id_field_id") == "" {
		t.Fatal("composite field metadata uniqueness index is missing")
	}

	ctx := context.Background()
	table, title := createV2IntegrationTableWithField(
		t, ctx, app, "Orders", "Title", "schema_catalog_restart",
	)
	resetApp(t, app)
	app = nil
	app = bootstrapApp(t, dataDir)
	described, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if described.Snapshot.SchemaRevision != title.SchemaRevision ||
		len(described.Snapshot.Fields) != 1 ||
		described.Snapshot.Fields[0].Identity.FieldID != title.FieldID {
		t.Fatalf("restart definition mismatch: %#v", described)
	}
	if got := described.Snapshot.Capabilities[0].FilterOperators; len(got) != 6 ||
		got[0] != "eq" || got[4] != "contains" || got[5] != "startsWith" {
		t.Fatalf("sidecar filter capabilities = %#v", got)
	}
	listed, err := schemaapi.New(app).List(ctx)
	if err != nil || len(listed) != 1 ||
		len(listed[0].Snapshot.Capabilities[0].FilterOperators) != 6 {
		t.Fatalf("listed sidecar filter capabilities = %#v, err=%v", listed, err)
	}
}

func TestEmptyBaseTableBootstrapsFirstFieldOnlyThroughV2Planner(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "Empty table", "op_empty_table")
	if table.SchemaRevision != "schema_0001" {
		t.Fatalf("empty bootstrap changed shape: %#v", table)
	}

	recommended, err := v2.RecommendedDefaults(v2.LogicalText)
	if err != nil {
		t.Fatal(err)
	}
	draft := v2.FieldDraft{
		DisplayName: "Title", LogicalType: v2.LogicalText,
		Value: recommended.Value, Constraints: recommended.Constraints,
		Storage: recommended.Storage, Display: recommended.Display,
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, nil)
	actor := v2.Actor{ID: "user_local", Kind: "user"}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID,
		ExpectedSchemaRev: table.SchemaRevision, Draft: &draft, Actor: actor,
	})
	if err != nil || plan.After == nil || plan.After.Identity.FieldID == "" ||
		plan.After.Identity.PhysicalName == "" || plan.After.Identity.ProviderFieldID == "" {
		t.Fatalf("planner did not freeze opaque field identities: %#v, %v", plan, err)
	}
	receipt, err := fieldchange.NewExecutor(app, store).Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "op_empty_first_field", Actor: actor,
		Confirmations: plan.Confirmations,
	})
	if err != nil || receipt.SchemaRevision != "schema_0002" ||
		receipt.FieldID != plan.After.Identity.FieldID {
		t.Fatalf("first field receipt = %#v, %v", receipt, err)
	}
	reloaded, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil || len(reloaded.Snapshot.Fields) != 1 ||
		reloaded.Snapshot.Fields[0].Identity.PhysicalName != plan.After.Identity.PhysicalName {
		t.Fatalf("catalog did not reflect v2 field: %#v, %v", reloaded.Snapshot.Fields, err)
	}
	collection, err := app.FindCollectionByNameOrId(table.PhysicalName)
	if err != nil || collection.Fields.GetByName(plan.After.Identity.PhysicalName) == nil {
		t.Fatalf("compiled provider field is missing: %v", err)
	}
}

func bootstrapApp(t *testing.T, dataDir string) *pocketbase.PocketBase {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: dataDir, HideStartBanner: true})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap(): %v", err)
	}
	if err := app.RunAllMigrations(); err != nil {
		resetApp(t, app)
		t.Fatalf("RunAllMigrations(): %v", err)
	}
	return app
}

func resetApp(t *testing.T, app *pocketbase.PocketBase) {
	t.Helper()
	if err := app.ResetBootstrapState(); err != nil {
		t.Errorf("ResetBootstrapState(): %v", err)
	}
}
