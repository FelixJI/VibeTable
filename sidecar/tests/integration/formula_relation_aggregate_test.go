package integration_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestFormulaRelationAggregatesMoreThanTenThousandRecords(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	target := createV2IntegrationTable(
		t, ctx, app, "Formula scale lines", "formula_scale_lines_table",
	)
	amount := createV2IntegrationField(
		t, ctx, app, target.TableID,
		fieldDraftForIntegration(t, v2.LogicalNumber, "Amount"),
		"formula_scale_amount",
	)
	source := createV2IntegrationTable(
		t, ctx, app, "Formula scale orders", "formula_scale_orders_table",
	)
	orderName := createV2IntegrationField(
		t, ctx, app, source.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Order"),
		"formula_scale_order_name",
	)
	lines := createV2IntegrationRelation(
		t, ctx, app, source.TableID, orderName.FieldID,
		target.TableID, amount.FieldID, "Lines", "Orders", "many",
		"formula_scale_lines_relation",
	)
	sumDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Line sum")
	sumDraft.Formula = &v2.FormulaDraftSpec{
		Language: "cel-v1", Source: "SUM({Lines}.{Amount})",
	}
	sum := createV2IntegrationFormula(
		t, ctx, app, source.TableID, sumDraft, "formula_scale_sum",
	)
	countDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Line count")
	countDraft.Formula = &v2.FormulaDraftSpec{
		Language: "cel-v1", Source: "COUNT({Lines})",
	}
	count := createV2IntegrationFormula(
		t, ctx, app, source.TableID, countDraft, "formula_scale_count",
	)
	if amount.Definition == nil || lines.Definition == nil || sum.Definition == nil ||
		count.Definition == nil {
		t.Fatal("V2 formula scale fixture omitted field definitions")
	}
	runtimeSource, err := schemaexecution.Describe(ctx, app, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	targetTable := target.PhysicalName
	amountColumn := amount.Definition.Identity.PhysicalName
	if _, err := app.DB().NewQuery(fmt.Sprintf(`
		WITH RECURSIVE sequence(value) AS (
			SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value < 10001
		)
		INSERT INTO %s (id, %s)
		SELECT printf('rel%%012d', value), value FROM sequence
	`, targetTable, amountColumn)).WithContext(ctx).Execute(); err != nil {
		t.Fatalf("seed 10001 relation targets: %v", err)
	}
	var storedSum, storedMin, storedMax float64
	if err := app.DB().NewQuery(fmt.Sprintf(
		"SELECT SUM(%s), MIN(%s), MAX(%s) FROM %s",
		amountColumn, amountColumn, amountColumn, targetTable,
	)).Row(&storedSum, &storedMin, &storedMax); err != nil {
		t.Fatal(err)
	}
	if storedSum != 50_015_001 || storedMin != 1 || storedMax != 10_001 {
		t.Fatalf("seeded values = sum %v min %v max %v", storedSum, storedMin, storedMax)
	}
	recordIDs := make([]string, 10_001)
	for index := range recordIDs {
		recordIDs[index] = fmt.Sprintf("rel%012d", index+1)
	}
	collection, err := app.FindCollectionByNameOrId(source.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set(lines.Definition.Identity.PhysicalName, recordIDs)

	calculated, err := formula.NewCalculator(nil).Calculate(ctx, app, runtimeSource, record)
	if err != nil {
		t.Fatalf("calculate 10001 relation aggregates: %v", err)
	}
	if calculated[sum.Definition.Identity.PhysicalName] != 50_015_001.0 {
		t.Fatalf("line sum = %#v", calculated[sum.Definition.Identity.PhysicalName])
	}
	if calculated[count.Definition.Identity.PhysicalName] != int64(10_001) {
		t.Fatalf("line count = %#v", calculated[count.Definition.Identity.PhysicalName])
	}
}

func TestLookupDirectRelationPagesMoreThanTenThousandSources(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	target := createV2IntegrationTable(
		t, ctx, app, "Lookup scale lines", "lookup_scale_lines_table",
	)
	sku := createV2IntegrationField(
		t, ctx, app, target.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "SKU"), "lookup_scale_sku",
	)
	source := createV2IntegrationTable(
		t, ctx, app, "Lookup scale orders", "lookup_scale_orders_table",
	)
	orderName := createV2IntegrationField(
		t, ctx, app, source.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Order"), "lookup_scale_order_name",
	)
	lines := createV2IntegrationRelation(
		t, ctx, app, source.TableID, orderName.FieldID,
		target.TableID, sku.FieldID, "Lines", "Orders", "many",
		"lookup_scale_lines_relation",
	)
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Line SKUs")
	lookupDraft.Lookup = &v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: lines.FieldID}},
		TargetFieldID: sku.FieldID,
	}
	lookupField := createV2IntegrationField(
		t, ctx, app, source.TableID, lookupDraft, "lookup_scale_lookup",
	)
	if sku.Definition == nil || lines.Definition == nil || lookupField.Definition == nil {
		t.Fatal("V2 direct Lookup fixture omitted field definitions")
	}
	runtimeSource, err := schemaexecution.Describe(ctx, app, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeLookup, found := runtimeSource.Field(lookupField.FieldID)
	if !found {
		t.Fatal("runtime Lookup projection missing")
	}
	if _, err := app.DB().NewQuery(fmt.Sprintf(`
		WITH RECURSIVE sequence(value) AS (
			SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value < 10001
		)
		INSERT INTO %s (id, %s)
		SELECT printf('src%%012d', value), printf('SKU-%%05d', value) FROM sequence
	`, target.PhysicalName, sku.Definition.Identity.PhysicalName)).WithContext(ctx).Execute(); err != nil {
		t.Fatalf("seed 10001 lookup targets: %v", err)
	}
	recordIDs := make([]string, 10_001)
	for index := range recordIDs {
		recordIDs[index] = fmt.Sprintf("src%012d", index+1)
	}
	collection, err := app.FindCollectionByNameOrId(source.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set(lines.Definition.Identity.PhysicalName, recordIDs)
	calculator := lookup.NewCalculator()
	first, err := calculator.CalculateFieldPage(
		ctx, app, runtimeSource, record, runtimeLookup, 0, 100,
	)
	if err != nil {
		t.Fatalf("calculate first Lookup page: %v", err)
	}
	if first.ProvenanceTotal != 10_001 || !first.ProvenanceTotalKnown ||
		len(first.Provenance) != 100 || !first.ProvenanceHasMore ||
		first.Provenance[0].FieldID != sku.FieldID {
		t.Fatalf("first Lookup page = %#v", first)
	}
	last, err := calculator.CalculateFieldPage(
		ctx, app, runtimeSource, record, runtimeLookup, 10_000, 100,
	)
	if err != nil || last.ProvenanceTotal != 10_001 || !last.ProvenanceTotalKnown ||
		len(last.Provenance) != 1 || last.ProvenanceHasMore ||
		last.Provenance[0].Value != "SKU-10001" {
		t.Fatalf("last Lookup page = %#v, err=%v", last, err)
	}
}

func TestLookupMultiHopPagesTerminalValuesWithoutMaterializingAllLeaves(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	target := createV2IntegrationTable(
		t, ctx, app, "Lookup stream targets", "lookup_stream_targets_table",
	)
	sku := createV2IntegrationField(
		t, ctx, app, target.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "SKU"), "lookup_stream_sku",
	)
	middle := createV2IntegrationTable(
		t, ctx, app, "Lookup stream batches", "lookup_stream_batches_table",
	)
	batchName := createV2IntegrationField(
		t, ctx, app, middle.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Batch"), "lookup_stream_batch_name",
	)
	items := createV2IntegrationRelation(
		t, ctx, app, middle.TableID, batchName.FieldID,
		target.TableID, sku.FieldID, "Items", "Batches", "many",
		"lookup_stream_items_relation",
	)
	source := createV2IntegrationTable(
		t, ctx, app, "Lookup stream orders", "lookup_stream_orders_table",
	)
	orderName := createV2IntegrationField(
		t, ctx, app, source.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Order"), "lookup_stream_order_name",
	)
	batches := createV2IntegrationRelation(
		t, ctx, app, source.TableID, orderName.FieldID,
		middle.TableID, batchName.FieldID, "Batches", "Orders", "many",
		"lookup_stream_batches_relation",
	)
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "SKUs")
	lookupDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{
			{RelationFieldID: batches.FieldID},
			{RelationFieldID: items.FieldID},
		},
		TargetFieldID: sku.FieldID,
	}
	lookupField := createV2IntegrationField(
		t, ctx, app, source.TableID, lookupDraft, "lookup_stream_lookup",
	)
	if sku.Definition == nil || items.Definition == nil || batches.Definition == nil ||
		lookupField.Definition == nil {
		t.Fatal("V2 multi-hop Lookup fixture omitted field definitions")
	}
	runtimeSource, err := schemaexecution.Describe(ctx, app, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeLookup, found := runtimeSource.Field(lookupField.FieldID)
	if !found {
		t.Fatal("runtime multi-hop Lookup projection missing")
	}
	targetCollection, err := app.FindCollectionByNameOrId(target.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	targetIDs := make([]string, 7)
	for index := range targetIDs {
		targetIDs[index] = fmt.Sprintf("tgt%012d", index+1)
		record := core.NewRecord(targetCollection)
		record.Id = targetIDs[index]
		record.Set(sku.Definition.Identity.PhysicalName, fmt.Sprintf("SKU-%02d", index+1))
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	middleCollection, err := app.FindCollectionByNameOrId(middle.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	middleIDs := []string{"mid000000000001", "mid000000000002", "mid000000000003"}
	for index, ids := range [][]string{targetIDs[:2], targetIDs[2:6], targetIDs[6:]} {
		record := core.NewRecord(middleCollection)
		record.Id = middleIDs[index]
		record.Set(items.Definition.Identity.PhysicalName, ids)
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	sourceCollection, err := app.FindCollectionByNameOrId(source.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(sourceCollection)
	record.Set(batches.Definition.Identity.PhysicalName, middleIDs)
	page, err := lookup.NewCalculator().CalculateFieldPage(
		ctx, app, runtimeSource, record, runtimeLookup, 3, 2,
	)
	if err != nil {
		t.Fatalf("calculate multi-hop Lookup page: %v", err)
	}
	if page.ProvenanceTotal < 6 || page.ProvenanceTotalKnown || len(page.Provenance) != 2 ||
		page.Provenance[0].Value != "SKU-04" || page.Provenance[1].Value != "SKU-05" ||
		!page.ProvenanceHasMore {
		t.Fatalf("multi-hop Lookup page = %#v", page)
	}
	last, err := lookup.NewCalculator().CalculateFieldPage(
		ctx, app, runtimeSource, record, runtimeLookup, 6, 2,
	)
	if err != nil || last.ProvenanceTotal != 7 || !last.ProvenanceTotalKnown ||
		len(last.Provenance) != 1 || last.Provenance[0].Value != "SKU-07" ||
		last.ProvenanceHasMore {
		t.Fatalf("multi-hop final Lookup page = %#v, err=%v", last, err)
	}
}

func createV2IntegrationRelation(
	t *testing.T,
	ctx context.Context,
	app core.App,
	sourceTableID string,
	sourceDisplayFieldID string,
	targetTableID string,
	targetDisplayFieldID string,
	displayName string,
	reciprocalDisplayName string,
	cardinality string,
	operationID string,
) v2.ApplyReceipt {
	t.Helper()
	draft := fieldDraftForIntegration(t, v2.LogicalRelation, displayName)
	draft.Relation = &v2.RelationSpec{
		TargetTableID: targetTableID, Cardinality: cardinality,
		DeletePolicy: "setNull", DisplayField: targetDisplayFieldID,
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store)
	revisions, err := catalog.Revisions(ctx, sourceTableID)
	if err != nil {
		t.Fatal(err)
	}
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: sourceTableID,
		ExpectedSchemaRev: revisions.Schema, Draft: &draft, Actor: actor,
		RelationPair: &v2.RelationPairDraft{
			ReciprocalDisplayName: reciprocalDisplayName,
			ReciprocalCardinality: "many",
			SourceDisplayFieldID:  sourceDisplayFieldID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply {
		t.Fatalf("relation create plan blocked: %#v", plan.Errors)
	}
	receipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: operationID, Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func createV2IntegrationFormula(
	t *testing.T,
	ctx context.Context,
	app core.App,
	tableID string,
	draft v2.FieldDraft,
	operationID string,
) v2.ApplyReceipt {
	t.Helper()
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(
		app,
		store,
		fieldchange.WithFormulaBackfillScheduler(&atomicFormulaScheduler{}),
	)
	return applyCreatedField(
		t, ctx, catalog, planner, executor, tableID, draft,
		v2.Actor{ID: "local-user", Kind: "user"}, operationID,
	)
}
