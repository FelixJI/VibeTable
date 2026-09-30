package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// normalizeStored unwraps bare JSON scalars that some computed-cell writers
// persist without the envelope object, so assertions compare logical values.
func normalizeStored(value any) any {
	if raw, ok := value.(types.JSONRaw); ok {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err == nil {
			return decoded
		}
	}
	return value
}

// rematHarness wires the authoritative recalculation stack once for both
// lifecycle tests: kernel with formula+lookup calculators, the durable job
// service as publisher/invalidator, and the shared drain/apply helpers.
type rematHarness struct {
	kernel     *mutation.Kernel
	jobService *jobs.Service
	drain      func(stage string)
	apply      func(stage string, table v2IntegrationTable, key string, operation ...mutation.Operation)
}

func newRematHarness(t *testing.T, app *pocketbase.PocketBase) rematHarness {
	t.Helper()
	jobService := jobs.New(app, nil)
	t.Cleanup(func() { jobService.Shutdown() })
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(computed.New(
			lookup.NewCalculator(),
			formula.NewCalculator(formula.NewCompiler(formula.DefaultLimits())),
		)),
		mutation.WithPublisher(jobService),
		mutation.WithComputationInvalidator(jobService),
		mutation.WithPublishContext(jobService.PublishContext()),
	)
	jobService.SetKernel(kernel)
	drain := func(stage string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			records, err := app.FindAllRecords("vibetable_jobs")
			if err != nil {
				t.Fatal(err)
			}
			active, failed := []string{}, []string{}
			for _, record := range records {
				switch state := record.GetString("state"); state {
				case "complete":
				case "failed", "cancelled":
					failed = append(failed, record.Id)
				default:
					active = append(active, record.Id+" "+state)
				}
			}
			if len(active) == 0 {
				if len(failed) > 0 {
					t.Fatalf("%s: recalculation jobs failed: %v", stage, failed)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: recalculation jobs did not settle: %v", stage, active)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	apply := func(stage string, table v2IntegrationTable, key string, operation ...mutation.Operation) {
		t.Helper()
		revisions, err := fieldchange.NewCatalog(app).Revisions(context.Background(), table.TableID)
		if err != nil {
			t.Fatalf("%s: load revision: %v", stage, err)
		}
		if _, err := kernel.Apply(context.Background(), mutationRequest(
			table.TableID, revisions.Schema, key, operation...,
		)); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		drain(stage)
	}
	return rematHarness{kernel: kernel, jobService: jobService, drain: drain, apply: apply}
}

func storedValue(t *testing.T, app *pocketbase.PocketBase, table, recordID, fieldName string) any {
	t.Helper()
	record, err := app.FindRecordById(table, recordID)
	if err != nil {
		t.Fatal(err)
	}
	return normalizeStored(relatedcomputation.ProjectStored(record.GetRaw(fieldName)))
}

// TestComputedFieldRestoreRematerializesStoredValues pins #414 AC5 for both
// computed kinds: after a lookup or formula field is retired, a dependency
// value changes, and the field is restored, the stored cell and the query
// surface must expose the fresh oracle value instead of a silently stale
// snapshot from before retirement.
func TestComputedFieldRestoreRematerializesStoredValues(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "恢复订单", "restore_remat_orders")
	materials := createV2IntegrationTable(t, ctx, app, "恢复物料", "restore_remat_materials")
	orderCode := createV2IntegrationField(t, ctx, app, orders.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "订单编码"), "restore_order_code")
	sourceCode := createV2IntegrationField(t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "物料编码"), "restore_source_code")
	amount := createV2IntegrationField(t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalNumber, "数量"), "restore_amount")
	condition := &v2.LookupCondition{
		SourceTableID: materials.TableID, Match: "all",
		Rules: []v2.LookupConditionRule{{
			SourceFieldID: sourceCode.FieldID, Operator: "eq",
			Operand: &v2.LookupOperand{Kind: "field", FieldID: orderCode.FieldID},
		}},
	}
	sumDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "恢复求和")
	sumDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{}, TargetFieldID: amount.FieldID,
		Aggregation: v2.LookupAggregationSum, Condition: condition,
	}
	sumLookup := createV2IntegrationField(t, ctx, app, orders.TableID, sumDraft, "restore_sum_lookup")
	harness := newRematHarness(t, app)
	orderID := "restoreremat001"
	materialID := "restorematt0001"
	orderCodeName := orderCode.Definition.Identity.PhysicalName
	sourceCodeName := sourceCode.Definition.Identity.PhysicalName
	amountName := amount.Definition.Identity.PhysicalName
	sumName := sumLookup.Definition.Identity.PhysicalName
	harness.apply("order insert", orders, "restore-order-insert",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &orderID,
			Values: map[string]any{orderCodeName: "MAT-R"}})
	if got := storedValue(t, app, orders.PhysicalName, orderID, sumName); got != float64(0) {
		t.Fatalf("zero-match oracle = %#v", got)
	}
	harness.apply("material insert", materials, "restore-material-insert",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &materialID,
			Values: map[string]any{sourceCodeName: "MAT-R", amountName: float64(3)}})
	if got := storedValue(t, app, orders.PhysicalName, orderID, sumName); got != float64(3) {
		t.Fatalf("pre-retire oracle = %#v", got)
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(
		app, store, fieldchange.WithFormulaBackfillScheduler(harness.jobService),
	)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	lifecycle := func(tableID, fieldID string, action v2.ChangeAction, operationID string) {
		t.Helper()
		revisions, err := catalog.Revisions(ctx, tableID)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
			Action: action, TableID: tableID, FieldID: fieldID,
			ExpectedSchemaRev: revisions.Schema, Actor: actor,
		})
		if err != nil {
			t.Fatalf("%s plan: %#v", action, err)
		}
		if !plan.CanApply {
			t.Fatalf("%s plan blocked: %#v", action, plan.Errors)
		}
		if _, err := executor.Apply(ctx, v2.ApplyRequest{
			PlanID: plan.PlanID, PlanHash: plan.PlanHash,
			OperationID: operationID, Actor: actor, Confirmations: plan.Confirmations,
		}); err != nil {
			t.Fatalf("%s apply: %#v", action, err)
		}
		harness.drain(operationID)
	}
	// Lookup phase: retire hides the column from invalidation, the dependency
	// moves underneath it, restore must rematerialize to the fresh oracle.
	lifecycle(orders.TableID, sumLookup.FieldID, v2.ActionRetire, "restore-sum-retire")
	harness.apply("revalue while retired", materials, "restore-material-revalue",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &materialID,
			Values: map[string]any{amountName: float64(9)}})
	lifecycle(orders.TableID, sumLookup.FieldID, v2.ActionRestore, "restore-sum-restore")
	if got := storedValue(t, app, orders.PhysicalName, orderID, sumName); got != float64(9) {
		t.Fatalf("restored lookup kept a stale stored value: got %#v, want 9", got)
	}
	// Formula phase: the same retire/restore contract for a cel formula whose
	// source column changes while the formula field is hidden.
	doubledDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "数量翻倍")
	doubledDraft.Formula = &v2.FormulaDraftSpec{
		Language: "cel-v1", Source: amountName + " * 2.0",
	}
	doubledRevisions, err := catalog.Revisions(ctx, materials.TableID)
	if err != nil {
		t.Fatal(err)
	}
	doubledPlan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: materials.TableID,
		ExpectedSchemaRev: doubledRevisions.Schema, Draft: &doubledDraft, Actor: actor,
	})
	if err != nil || !doubledPlan.CanApply {
		t.Fatalf("doubled formula plan: %#v %v", doubledPlan, err)
	}
	doubledReceipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: doubledPlan.PlanID, PlanHash: doubledPlan.PlanHash,
		OperationID: "restore-doubled-create", Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.drain("restore-doubled-create")
	doubledName := doubledReceipt.Definition.Identity.PhysicalName
	if got := storedValue(t, app, materials.PhysicalName, materialID, doubledName); got != float64(18) {
		t.Fatalf("doubled formula backfill = %#v, want 18", got)
	}
	lifecycle(materials.TableID, doubledReceipt.FieldID, v2.ActionRetire, "restore-doubled-retire")
	harness.apply("revalue while formula retired", materials, "restore-material-revalue-two",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &materialID,
			Values: map[string]any{amountName: float64(11)}})
	if got := storedValue(t, app, orders.PhysicalName, orderID, sumName); got != float64(11) {
		t.Fatalf("active lookup after revalue = %#v, want 11", got)
	}
	lifecycle(materials.TableID, doubledReceipt.FieldID, v2.ActionRestore, "restore-doubled-restore")
	if got := storedValue(t, app, materials.PhysicalName, materialID, doubledName); got != float64(22) {
		t.Fatalf("restored formula kept a stale stored value: got %#v, want 22", got)
	}
	if got := storedValue(t, app, orders.PhysicalName, orderID, sumName); got != float64(11) {
		t.Fatalf("lookup after formula restore = %#v, want 11", got)
	}
	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	page, err := query.NewPort(app, querySource).QueryPage(
		ctx, orders.TableID, query.TableQuery{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range page.Rows {
		if fmt.Sprint(row["id"]) == orderID {
			found = true
			if fmt.Sprint(row[sumName]) != "11" {
				t.Fatalf("query surface after restore = %#v, want 11", row[sumName])
			}
		}
	}
	if !found {
		t.Fatalf("restored order row missing from query page: %#v", page.Rows)
	}
}

// TestSetNullTargetDeleteKeepsLinksAndComputedConsistent pins #414 AC2/AC4:
// deleting a linked target through the product mutation path must leave no
// dangling link on any source row and must refresh every dependent
// aggregation — one-hop and multi-hop paths — to the remaining-set oracle.
// It also records the evidence that re-inserting the same record id does not
// silently resurrect links, and that archive/restore target events keep their
// links, so membership matching stays valid for them.
func TestSetNullTargetDeleteKeepsLinksAndComputedConsistent(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "清链订单", "setnull_orders")
	shipments := createV2IntegrationTable(t, ctx, app, "清链运单", "setnull_shipments")
	materials := createV2IntegrationTable(t, ctx, app, "清链物料", "setnull_materials")
	orderCode := createV2IntegrationField(t, ctx, app, orders.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "订单编码"), "setnull_order_code")
	shipCode := createV2IntegrationField(t, ctx, app, shipments.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "运单编码"), "setnull_ship_code")
	sourceCode := createV2IntegrationField(t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "物料编码"), "setnull_source_code")
	amount := createV2IntegrationField(t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalNumber, "数量"), "setnull_amount")
	archivedAt := createV2IntegrationField(t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalDateTime, "归档时间"), "setnull_archived_at")
	direct := createV2IntegrationRelation(t, ctx, app, orders.TableID, orderCode.FieldID,
		materials.TableID, sourceCode.FieldID, "直连物料", "直连订单", "many", "setnull_direct")
	shipLink := createV2IntegrationRelation(t, ctx, app, shipments.TableID, shipCode.FieldID,
		materials.TableID, sourceCode.FieldID, "运单物料", "运单", "many", "setnull_ship_link")
	orderShip := createV2IntegrationRelation(t, ctx, app, orders.TableID, orderCode.FieldID,
		shipments.TableID, shipCode.FieldID, "运单", "订单", "many", "setnull_order_ship")
	directSumDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "直连求和")
	directSumDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{{RelationFieldID: direct.FieldID}}, TargetFieldID: amount.FieldID,
		Aggregation: v2.LookupAggregationSum,
	}
	directSum := createV2IntegrationField(t, ctx, app, orders.TableID, directSumDraft, "setnull_direct_sum")
	shipSumDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "运单求和")
	shipSumDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{{RelationFieldID: shipLink.FieldID}}, TargetFieldID: amount.FieldID,
		Aggregation: v2.LookupAggregationSum,
	}
	shipSum := createV2IntegrationField(t, ctx, app, shipments.TableID, shipSumDraft, "setnull_ship_sum")
	chainSumDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "跨表求和")
	chainSumDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{
			{RelationFieldID: orderShip.FieldID}, {RelationFieldID: shipLink.FieldID},
		},
		TargetFieldID: amount.FieldID, Aggregation: v2.LookupAggregationSum,
	}
	chainSum := createV2IntegrationField(t, ctx, app, orders.TableID, chainSumDraft, "setnull_chain_sum")
	lifecycleCore, err := schemacore.NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	materialsRevisions, err := fieldchange.NewCatalog(app).Revisions(ctx, materials.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycleCore.Configure(ctx, v2.TableSettingsIntent{
		TableID: materials.TableID, ExpectedSchemaRev: materialsRevisions.Schema,
		ArchivePolicy: v2.ArchivePolicy{Mode: "deletedAt", FieldID: &archivedAt.FieldID},
		OperationID:   "setnull_archive_policy", Actor: v2.Actor{ID: "local-user", Kind: "user"},
	}); err != nil {
		t.Fatal(err)
	}
	harness := newRematHarness(t, app)
	orderID := "setnulorder0001"
	shipmentID := "setnulship00001"
	firstMaterial := "setnulmatrl0001"
	secondMaterial := "setnulmatrl0002"
	shipCodeName := shipCode.Definition.Identity.PhysicalName
	sourceCodeName := sourceCode.Definition.Identity.PhysicalName
	amountName := amount.Definition.Identity.PhysicalName
	directName := direct.Definition.Identity.PhysicalName
	shipLinkName := shipLink.Definition.Identity.PhysicalName
	orderShipName := orderShip.Definition.Identity.PhysicalName
	directSumName := directSum.Definition.Identity.PhysicalName
	shipSumName := shipSum.Definition.Identity.PhysicalName
	chainSumName := chainSum.Definition.Identity.PhysicalName
	assertDirectShip := func(stage string, wantDirect, wantShip any) {
		t.Helper()
		if got := storedValue(t, app, orders.PhysicalName, orderID, directSumName); got != wantDirect {
			t.Fatalf("%s: direct sum = %#v, want %#v", stage, got, wantDirect)
		}
		if got := storedValue(t, app, shipments.PhysicalName, shipmentID, shipSumName); got != wantShip {
			t.Fatalf("%s: shipment sum = %#v, want %#v", stage, got, wantShip)
		}
	}
	assertChain := func(stage string, want any) {
		t.Helper()
		got := storedValue(t, app, orders.PhysicalName, orderID, chainSumName)
		if got != want {
			t.Fatalf("%s: chain sum = %#v, want %#v", stage, got, want)
		}
	}
	harness.apply("first material insert", materials, "setnull-material-one",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &firstMaterial,
			Values: map[string]any{sourceCodeName: "MAT-D1", amountName: float64(1)}})
	harness.apply("second material insert", materials, "setnull-material-two",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &secondMaterial,
			Values: map[string]any{sourceCodeName: "MAT-D2", amountName: float64(7)}})
	harness.apply("shipment insert", shipments, "setnull-shipment-insert",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &shipmentID,
			Values: map[string]any{shipCodeName: "SHP-1",
				shipLinkName: []string{firstMaterial, secondMaterial}}})
	harness.apply("order insert", orders, "setnull-order-insert",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &orderID,
			Values: map[string]any{orderCode.Definition.Identity.PhysicalName: "ORD-1",
				directName:    []string{firstMaterial, secondMaterial},
				orderShipName: []string{shipmentID}}})
	assertDirectShip("linked set", float64(8), float64(8))
	if got := storedValue(t, app, orders.PhysicalName, orderID, chainSumName); got != float64(8) {
		t.Fatalf("linked set chain sum = %#v, want 8", got)
	}
	// Delete: PocketBase clears every referencing link in the same
	// transaction; the fan-out must still refresh all three aggregates.
	harness.apply("target delete", materials, "setnull-material-delete",
		mutation.Operation{Kind: mutation.OperationDelete, RecordID: &firstMaterial})
	if _, err := app.FindRecordById(materials.PhysicalName, firstMaterial); err == nil {
		t.Fatal("deleted target still readable")
	}
	if got := sourceLinks(t, app, orders.PhysicalName, orderID, directName); len(got) != 1 || got[0] != secondMaterial {
		t.Fatalf("order direct links after setNull delete = %#v, want [%s]", got, secondMaterial)
	}
	if got := sourceLinks(t, app, shipments.PhysicalName, shipmentID, shipLinkName); len(got) != 1 || got[0] != secondMaterial {
		t.Fatalf("shipment links after setNull delete = %#v, want [%s]", got, secondMaterial)
	}
	if got := sourceLinks(t, app, orders.PhysicalName, orderID, orderShipName); len(got) != 1 || got[0] != shipmentID {
		t.Fatalf("order shipment links changed by material delete = %#v", got)
	}
	assertDirectShip("remaining set", float64(7), float64(7))
	if got := storedValue(t, app, orders.PhysicalName, orderID, chainSumName); got != float64(7) {
		t.Fatalf("remaining set chain sum = %#v, want 7", got)
	}
	// Re-inserting the same record id must not silently resurrect links.
	harness.apply("target reinsert", materials, "setnull-material-reinsert",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &firstMaterial,
			Values: map[string]any{sourceCodeName: "MAT-D1", amountName: float64(5)}})
	assertDirectShip("after reinsert", float64(7), float64(7))
	if got := sourceLinks(t, app, shipments.PhysicalName, shipmentID, shipLinkName); len(got) != 1 {
		t.Fatalf("reinsert resurrected shipment links = %#v", got)
	}
	if got := storedValue(t, app, orders.PhysicalName, orderID, chainSumName); got != float64(7) {
		t.Fatalf("after reinsert chain sum = %#v, want 7", got)
	}
	// Re-linking through the ordinary update path propagates to the one-hop
	// aggregate on the changed table. The two-hop aggregate crossing the
	// changed intermediate hop is recorded as observed evidence below.
	harness.apply("shipment relink", shipments, "setnull-shipment-relink",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &shipmentID,
			Values: map[string]any{shipLinkName: []string{secondMaterial, firstMaterial}}})
	assertDirectShip("after relink", float64(7), float64(12))
	assertChain("after relink", float64(12))
	// Archive keeps links and the row readable, so membership matching stays
	// valid for archive/restore events; the aggregates must not drift.
	harness.apply("target archive", materials, "setnull-material-archive",
		mutation.Operation{Kind: mutation.OperationArchive, RecordID: &secondMaterial})
	if got := sourceLinks(t, app, shipments.PhysicalName, shipmentID, shipLinkName); len(got) != 2 {
		t.Fatalf("archive changed shipment links = %#v", got)
	}
	assertDirectShip("after archive", float64(7), float64(12))
	assertChain("after archive", float64(12))
	harness.apply("target restore", materials, "setnull-material-restore",
		mutation.Operation{Kind: mutation.OperationRestore, RecordID: &secondMaterial})
	assertDirectShip("after restore event", float64(7), float64(12))
	assertChain("after restore event", float64(12))
	// The query surface must agree with the stored aggregates.
	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	page, err := query.NewPort(app, querySource).QueryPage(
		ctx, orders.TableID, query.TableQuery{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range page.Rows {
		if fmt.Sprint(row["id"]) == orderID {
			found = true
			if fmt.Sprint(row[directSumName]) != "7" {
				t.Fatalf("query direct sum = %#v, want 7", row[directSumName])
			}
		}
	}
	if !found {
		t.Fatalf("source row missing from query page: %#v", page.Rows)
	}
}

// TestSetNullMixedBatchDeleteAndUnrelatedUpdateRefreshesAggregate pins the
// fresh-review finding for #414: a single mutation batch that deletes a
// linked target and updates an unrelated record reports the aggregate event
// operation "update", so the delete fallback must come from the batch audit
// scan, not only the event kind. The remaining-set oracle (7), cleaned
// links, and the query surface must all agree after the mixed batch.
func TestSetNullMixedBatchDeleteAndUnrelatedUpdateRefreshesAggregate(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "混批订单", "mixed_orders")
	materials := createV2IntegrationTable(t, ctx, app, "混批物料", "mixed_materials")
	orderCode := createV2IntegrationField(t, ctx, app, orders.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "订单编码"), "mixed_order_code")
	sourceCode := createV2IntegrationField(t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "物料编码"), "mixed_source_code")
	amount := createV2IntegrationField(t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalNumber, "数量"), "mixed_amount")
	direct := createV2IntegrationRelation(t, ctx, app, orders.TableID, orderCode.FieldID,
		materials.TableID, sourceCode.FieldID, "直连物料", "直连订单", "many", "mixed_direct")
	directSumDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "直连求和")
	directSumDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{{RelationFieldID: direct.FieldID}}, TargetFieldID: amount.FieldID,
		Aggregation: v2.LookupAggregationSum,
	}
	directSum := createV2IntegrationField(t, ctx, app, orders.TableID, directSumDraft, "mixed_direct_sum")
	harness := newRematHarness(t, app)
	orderID := "mixedorder00001"
	firstMaterial := "mixedmatrl00001"
	secondMaterial := "mixedmatrl00002"
	unlinkedMaterial := "mixedmatrl00003"
	sourceCodeName := sourceCode.Definition.Identity.PhysicalName
	amountName := amount.Definition.Identity.PhysicalName
	directName := direct.Definition.Identity.PhysicalName
	directSumName := directSum.Definition.Identity.PhysicalName
	for _, material := range []struct {
		id     string
		code   string
		amount float64
	}{
		{firstMaterial, "MAT-M1", 1},
		{secondMaterial, "MAT-M2", 7},
		{unlinkedMaterial, "MAT-M3", 5},
	} {
		harness.apply("material insert "+material.id, materials, "mixed-insert-"+material.id,
			mutation.Operation{Kind: mutation.OperationInsert, RecordID: &material.id,
				Values: map[string]any{sourceCodeName: material.code, amountName: material.amount}})
	}
	harness.apply("order insert", orders, "mixed-order-insert",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &orderID,
			Values: map[string]any{
				orderCode.Definition.Identity.PhysicalName: "ORD-M",
				directName: []string{firstMaterial, secondMaterial},
			}})
	if got := storedValue(t, app, orders.PhysicalName, orderID, directSumName); got != float64(8) {
		t.Fatalf("linked set oracle = %#v, want 8", got)
	}
	// One batch: delete the linked target and revalue the unrelated record.
	// The aggregate event reports operation "update"; the delete fallback
	// must be derived from the batch audit scan.
	harness.apply("mixed batch", materials, "mixed-batch-delete-update",
		mutation.Operation{Kind: mutation.OperationDelete, RecordID: &firstMaterial},
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &unlinkedMaterial,
			Values: map[string]any{amountName: float64(6)}})
	if _, err := app.FindRecordById(materials.PhysicalName, firstMaterial); err == nil {
		t.Fatal("deleted target still readable")
	}
	if got := sourceLinks(t, app, orders.PhysicalName, orderID, directName); len(got) != 1 || got[0] != secondMaterial {
		t.Fatalf("order direct links after mixed batch = %#v, want [%s]", got, secondMaterial)
	}
	if got := storedValue(t, app, orders.PhysicalName, orderID, directSumName); got != float64(7) {
		t.Fatalf("remaining set oracle after mixed batch = %#v, want 7", got)
	}
	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	page, err := query.NewPort(app, querySource).QueryPage(
		ctx, orders.TableID, query.TableQuery{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range page.Rows {
		if fmt.Sprint(row["id"]) == orderID {
			found = true
			if fmt.Sprint(row[directSumName]) != "7" {
				t.Fatalf("query surface after mixed batch = %#v, want 7", row[directSumName])
			}
		}
	}
	if !found {
		t.Fatalf("source row missing from query page: %#v", page.Rows)
	}
}

// TestRelationPickerSaveWithFormulaOverPathLookup pins the S38 production
// failure (#414): a cel formula referencing a same-table path Lookup reads the
// value the Lookup calculator just materialized on the record, and PocketBase
// hands that JSON-column scalar back as pbtypes.JSONRaw. normalizeInput must
// unwrap the storage wrapper and normalize by the declared field type, so the
// picker save commits and both stored cells match the independent oracle.
// Old implementation: formula.null "formula used null in an unsupported
// operation" on the picker save; the field-creation backfill job fails with
// the same code and leaves the formula column null.
func TestRelationPickerSaveWithFormulaOverPathLookup(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "取值订单", "picker_orders")
	materials := createV2IntegrationTable(t, ctx, app, "取值物料", "picker_materials")
	orderCode := createV2IntegrationField(t, ctx, app, orders.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "订单编码"), "picker_order_code")
	sourceCode := createV2IntegrationField(t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "物料编码"), "picker_source_code")
	amount := createV2IntegrationField(t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalNumber, "数量"), "picker_amount")
	link := createV2IntegrationRelation(t, ctx, app, orders.TableID, orderCode.FieldID,
		materials.TableID, sourceCode.FieldID, "关联合同", "订单", "one", "picker_relation")
	linkedDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "关联金额")
	linkedDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{{RelationFieldID: link.FieldID}}, TargetFieldID: amount.FieldID,
		Aggregation: v2.LookupAggregationSum,
	}
	linked := createV2IntegrationField(t, ctx, app, orders.TableID, linkedDraft, "picker_linked_lookup")
	harness := newRematHarness(t, app)
	orderID := "pickerorder0001"
	materialID := "pickermatrl0001"
	sourceCodeName := sourceCode.Definition.Identity.PhysicalName
	amountName := amount.Definition.Identity.PhysicalName
	harness.apply("material insert", materials, "picker-material-insert",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &materialID,
			Values: map[string]any{sourceCodeName: "MAT-P", amountName: float64(7)}})
	harness.apply("order insert", orders, "picker-order-insert",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &orderID,
			Values: map[string]any{
				orderCode.Definition.Identity.PhysicalName: "ORD-P",
			}})
	// The S38 chain shape: a cel formula reading the same-table path Lookup.
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(
		app, store, fieldchange.WithFormulaBackfillScheduler(harness.jobService),
	)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	totalDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "关系总额")
	totalDraft.Formula = &v2.FormulaDraftSpec{
		Language: "cel-v1", Source: linked.Definition.Identity.PhysicalName + " + 1.0",
	}
	revisions, err := catalog.Revisions(ctx, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: orders.TableID,
		ExpectedSchemaRev: revisions.Schema, Draft: &totalDraft, Actor: actor,
	})
	if err != nil || !plan.CanApply {
		t.Fatalf("total formula plan: %#v %v", plan, err)
	}
	totalReceipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "picker-total-create", Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	totalName := totalReceipt.Definition.Identity.PhysicalName
	linkedName := linked.Definition.Identity.PhysicalName
	linkName := link.Definition.Identity.PhysicalName
	// The relation picker save itself: bind the first target through the real
	// relation service on the production composite calculator (harness).
	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	service := relation.New(app, query.NewPort(app, querySource), harness.kernel)
	definition, err := schemaexecution.Describe(ctx, app, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyDelta(ctx, relation.DeltaRequest{
		RelationID:     orders.TableID + "." + link.FieldID,
		SourceRecordID: orderID,
		SchemaRevision: definition.Snapshot.SchemaRevision,
		Adds: []relation.TargetRef{{
			TableID: materials.TableID, RecordID: materialID, Label: materialID,
		}},
		RequestID: "picker-save", IdempotencyKey: "picker-save",
		Actor: mutation.Actor{Type: "user", ID: "local-user"},
	}); err != nil {
		t.Fatalf("relation picker save failed: %#v", err)
	}
	if got := storedValue(t, app, orders.PhysicalName, orderID, linkedName); got != float64(7) {
		t.Fatalf("path lookup after picker save = %#v, want 7", got)
	}
	if got := storedValue(t, app, orders.PhysicalName, orderID, totalName); got != float64(8) {
		t.Fatalf("formula over path lookup = %#v, want 8", got)
	}
	if got := sourceLinks(t, app, orders.PhysicalName, orderID, linkName); len(got) != 1 || got[0] != materialID {
		t.Fatalf("links after picker save = %#v", got)
	}
}
