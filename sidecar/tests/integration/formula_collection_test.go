package integration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

func TestFormulaCollectionAuthoritativeSourceAndSchemaDependencies(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "合同", "collection_orders")
	shipments := createV2IntegrationTable(t, ctx, app, "出货", "collection_shipments")
	code := createV2IntegrationField(t, ctx, app, orders.TableID, fieldDraftForIntegration(t, v2.LogicalText, "合同"), "collection_order_code")
	sourceCode := createV2IntegrationField(t, ctx, app, shipments.TableID, fieldDraftForIntegration(t, v2.LogicalText, "合同"), "collection_source_code")
	amount := createV2IntegrationField(t, ctx, app, shipments.TableID, fieldDraftForIntegration(t, v2.LogicalNumber, "金额"), "collection_source_amount")
	draft := fieldDraftForIntegration(t, v2.LogicalFormula, "已发货金额")
	draft.Formula = &v2.FormulaDraftSpec{Language: "cel-v2", Source: fmt.Sprintf(
		"SUMIF(TABLE(%q), CurrentValue.%s == %s, CurrentValue.%s)",
		shipments.TableID, sourceCode.Definition.Identity.PhysicalName, code.Definition.Identity.PhysicalName, amount.Definition.Identity.PhysicalName)}
	total := createV2IntegrationFormula(t, ctx, app, orders.TableID, draft, "collection_total")
	if total.Definition.Formula.Language != "cel-v2" {
		t.Fatal("collection was stored as cel-v1")
	}
	dependencies, err := app.FindAllRecords("vibetable_computation_dependencies", dbx.HashExp{"source_table_id": orders.TableID, "computed_field_id": total.FieldID})
	if err != nil || len(dependencies) != 3 {
		t.Fatalf("membership/predicate/projection edges = %d: %v", len(dependencies), err)
	}
	definition, err := schemaapi.New(app).Describe(ctx, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId(orders.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(collection)
	row.Set(code.Definition.Identity.PhysicalName, "A")
	calculator := computed.New(formula.NewCalculator(nil))
	check := func(want float64) {
		t.Helper()
		result, err := calculator.Calculate(ctx, app, definition, row)
		if err != nil || result[total.Definition.Identity.PhysicalName] != want {
			t.Fatalf("sum = %v, %#v; want %v", result, err, want)
		}
	}
	check(0)
	sourceCollection, err := app.FindCollectionByNameOrId(shipments.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	// More matches than a QueryPort page; insertion order is deliberately reversed.
	for index := 300; index >= 0; index-- {
		record := core.NewRecord(sourceCollection)
		record.Id = fmt.Sprintf("shipment%07d", index)
		record.Set(sourceCode.Definition.Identity.PhysicalName, "A")
		if name := sourceCode.Definition.Value.Presence.PhysicalName; name != "" {
			record.Set(name, true)
		}
		record.Set(amount.Definition.Identity.PhysicalName, 2.0)
		if name := amount.Definition.Value.Presence.PhysicalName; name != "" {
			record.Set(name, true)
		}
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	check(602)
	first, err := app.FindRecordById(sourceCollection, "shipment0000000")
	if err != nil {
		t.Fatal(err)
	}
	first.Set(sourceCode.Definition.Identity.PhysicalName, "B")
	if err := app.Save(first); err != nil {
		t.Fatal(err)
	}
	check(600)
	if err := app.Delete(first); err != nil {
		t.Fatal(err)
	}
	check(600)
	// A dependent formula prevents deleting its source table.
	revision, err := schemaapi.New(app).GetRevision(ctx, shipments.TableID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = schemaapi.New(app).DeleteTable(ctx, shipments.TableID, revision)
	var schemaErr *schemaerror.ProductError
	if !errors.As(err, &schemaErr) || schemaErr.Code != "schema.table.referenced" {
		t.Fatalf("source delete = %v", err)
	}
	// Field retirement must expose the same incoming formula dependency.
	catalog := fieldchange.NewCatalog(app)
	revs, err := catalog.Revisions(ctx, shipments.TableID)
	if err != nil {
		t.Fatal(err)
	}
	planner := fieldchange.NewPlanner(catalog, catalog, fieldchange.NewPocketBasePlanStore(app), v2.NewIdentityAllocator(nil))
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionRetire, TableID: shipments.TableID,
		FieldID: amount.FieldID, ExpectedSchemaRev: revs.Schema, Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil || plan.CanApply || len(plan.Impact.Dependencies) == 0 {
		t.Fatalf("source retirement = %#v, %v", plan, err)
	}
}
