package workspacev2

import (
	"context"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"testing"
)

type numberPackageFixture struct{ tableID, physical, schemaRevision string }

func seedAutoNumberPackageFixture(t *testing.T, app core.App) numberPackageFixture {
	t.Helper()
	ctx := context.Background()
	lifecycle, err := schemacore.NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "Package numbering", OperationID: "number-package-table", Actor: v2.Actor{ID: "test", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	defaults, _ := v2.RecommendedDefaults(v2.LogicalAutoNumber)
	draft := v2.FieldDraft{DisplayName: "合同编号", LogicalType: v2.LogicalAutoNumber, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display, AutoNumber: &v2.AutoNumberSpec{Prefix: "HT-", Start: 1, Width: 6}}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID, ExpectedSchemaRev: table.SchemaRevision, Draft: &draft, Actor: v2.Actor{ID: "test", Kind: "user"}})
	if err != nil || !plan.CanApply {
		t.Fatalf("number plan: %#v %v", plan, err)
	}
	field, err := fieldchange.NewExecutor(app, store).Apply(ctx, v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "number-package-field", Actor: plan.Intent.Actor})
	if err != nil {
		t.Fatal(err)
	}
	fixture := numberPackageFixture{table.TableID, field.Definition.Identity.PhysicalName, field.SchemaRevision}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	receipt, err := kernel.Apply(ctx, fixture.request("number-package-seed", []mutation.Operation{{Kind: mutation.OperationInsert, Values: map[string]any{}}, {Kind: mutation.OperationInsert, Values: map[string]any{}}, {Kind: mutation.OperationInsert, Values: map[string]any{}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = kernel.Apply(ctx, fixture.request("number-package-delete", []mutation.Operation{{Kind: mutation.OperationDelete, RecordID: &receipt.AffectedRows[1].RecordID}, {Kind: mutation.OperationDelete, RecordID: &receipt.AffectedRows[2].RecordID}}))
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture numberPackageFixture) request(key string, ops []mutation.Operation) mutation.Request {
	return mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: key, IdempotencyKey: key, TableID: fixture.tableID, SchemaRevision: fixture.schemaRevision, Operations: ops, Actor: mutation.Actor{Type: "user", ID: "test"}}
}

func assertImportedAutoNumberContinues(t *testing.T, runtime *Runtime, app core.App, fixture numberPackageFixture) {
	t.Helper()
	ctx := context.Background()
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	var receipt mutation.Receipt
	err := runtime.CoordinateBusinessWrite(ctx, "mutation.apply", "number-package-new-workspace", func(writeCtx context.Context) error {
		var applyErr error
		receipt, applyErr = kernel.Apply(writeCtx, fixture.request("number-package-new-workspace", []mutation.Operation{{Kind: mutation.OperationInsert, Values: map[string]any{}}}))
		return applyErr
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", map[string]any{"table": fixture.tableID})
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.FindRecordById(metadata.GetString("collection_id"), receipt.AffectedRows[0].RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if record.GetString(fixture.physical) != "HT-000004" {
		t.Fatalf("imported new workspace guessed from live rows: %q", record.GetString(fixture.physical))
	}
	records, err := app.FindRecordsByFilter(metadata.GetString("collection_id"), "", "id", 0, 0)
	if err != nil || len(records) != 2 {
		t.Fatalf("imported rows: %d %v", len(records), err)
	}
	found := false
	for _, r := range records {
		if r.GetString(fixture.physical) == "HT-000001" {
			found = true
		}
	}
	if !found {
		t.Fatal("package import changed the original numbering string")
	}
}
