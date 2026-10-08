package integration_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/autonumber"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func autoNumberDraft(t *testing.T) v2.FieldDraft {
	draft := fieldDraftForIntegration(t, v2.LogicalAutoNumber, "合同编号")
	draft.AutoNumber = &v2.AutoNumberSpec{Prefix: "HT-", Start: 1, Width: 6}
	return draft
}

func TestAutoNumberConcurrentReplayRollbackDeleteAndRetirement(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "合同", "auto_number_concurrency")
	field := createV2IntegrationField(t, ctx, app, table.TableID, autoNumberDraft(t), "auto_number_create")
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	request := func(key string, ops ...mutation.Operation) mutation.Request {
		return mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: key, IdempotencyKey: key, TableID: table.TableID, SchemaRevision: field.SchemaRevision, Actor: mutation.Actor{Type: "user", ID: "local"}, Operations: ops}
	}
	insert := func(key string) (mutation.Receipt, error) {
		return kernel.Apply(ctx, request(key, mutation.Operation{Kind: mutation.OperationInsert, Values: map[string]any{}}))
	}
	const count = 32
	var wg sync.WaitGroup
	failures := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := insert(fmt.Sprintf("concurrent_%d", i))
			if err != nil {
				failures <- err
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId(table.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	records, err := app.FindRecordsByFilter(collection, "", "id", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	var highest, middle string
	for _, r := range records {
		value := r.GetString(field.Definition.Identity.PhysicalName)
		if used[value] {
			t.Fatalf("duplicate %s", value)
		}
		used[value] = true
		if value == "HT-000032" {
			highest = r.Id
		}
		if value == "HT-000016" {
			middle = r.Id
		}
	}
	if len(used) != count || highest == "" || middle == "" {
		t.Fatalf("unexpected numbers: %v", used)
	}
	replay, err := insert("concurrent_0")
	if err != nil || replay.Status != mutation.StatusReplayed {
		t.Fatalf("replay=%#v %v", replay, err)
	}
	failKernel := mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithFaultInjector(func(point string) error {
		if point == "after_record" {
			return errors.New("injected")
		}
		return nil
	}))
	if _, err := failKernel.Apply(ctx, request("rollback", mutation.Operation{Kind: mutation.OperationInsert, Values: map[string]any{}})); err == nil {
		t.Fatal("injected failure accepted")
	}
	if _, err := kernel.Apply(ctx, request("delete", mutation.Operation{Kind: mutation.OperationDelete, RecordID: &highest}, mutation.Operation{Kind: mutation.OperationDelete, RecordID: &middle})); err != nil {
		t.Fatal(err)
	}
	next, err := insert("after_delete")
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.FindRecordById(collection, next.AffectedRows[0].RecordID)
	if err != nil || row.GetString(field.Definition.Identity.PhysicalName) != "HT-000033" {
		t.Fatalf("next number=%v %v", row, err)
	}
	if _, err := kernel.Preview(ctx, request("forge", mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &row.Id, Values: map[string]any{field.Definition.Identity.FieldID: "HT-000001"}})); err == nil {
		t.Fatal("manual number override accepted")
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store)
	lifecycle := func(action v2.ChangeAction) {
		t.Helper()
		revisions, err := catalog.Revisions(ctx, table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: action, TableID: table.TableID, FieldID: field.Definition.Identity.FieldID, ExpectedSchemaRev: revisions.Schema, Actor: v2.Actor{ID: "local", Kind: "user"}})
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := executor.Apply(ctx, v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: string(action) + "_number", Actor: plan.Intent.Actor})
		if err != nil {
			t.Fatal(err)
		}
		field.SchemaRevision = receipt.SchemaRevision
	}
	lifecycle(v2.ActionRetire)
	hidden, err := insert("while_retired")
	if err != nil {
		t.Fatal(err)
	}
	lifecycle(v2.ActionRestore)
	hiddenRow, err := app.FindRecordById(collection, hidden.AffectedRows[0].RecordID)
	if err != nil || hiddenRow.GetString(field.Definition.Identity.PhysicalName) != "HT-000034" {
		t.Fatalf("retired field missed allocation: %v %v", hiddenRow, err)
	}
	if _, err := insert("after_restore"); err != nil {
		t.Fatal(err)
	}
	metadata, err := app.FindFirstRecordByFilter("vibetable_fields", "field_id={:field}", dbx.Params{"field": field.Definition.Identity.FieldID})
	if err != nil {
		t.Fatal(err)
	}
	high, err := autonumber.DecodeState([]byte(metadata.GetString(autonumber.StateColumn)), *field.Definition.AutoNumber)
	if err != nil || high != 35 {
		t.Fatalf("highWater=%d %v", high, err)
	}
	metadata.Set(autonumber.StateColumn, nil)
	if err := app.Save(metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := insert("missing_state"); err == nil {
		t.Fatal("missing allocator state accepted")
	}
	assertRecordCount(t, app, collection.Name, count+1)
}

func TestAutoNumberBackfillFrozenPreviewAndAtomicFailure(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "历史合同", "auto_number_backfill")
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	seed := mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: "seed", IdempotencyKey: "seed", TableID: table.TableID, SchemaRevision: table.SchemaRevision, Actor: mutation.Actor{Type: "user", ID: "local"}}
	for _, id := range []string{"zz0000000000001", "aa0000000000001", "mm0000000000001"} {
		id := id
		seed.Operations = append(seed.Operations, mutation.Operation{Kind: mutation.OperationInsert, RecordID: &id, Values: map[string]any{}})
	}
	if _, err := kernel.Apply(ctx, seed); err != nil {
		t.Fatal(err)
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store)
	draft := autoNumberDraft(t)
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID, ExpectedSchemaRev: table.SchemaRevision, Draft: &draft, Actor: v2.Actor{ID: "local", Kind: "user"}})
	if err != nil || !plan.CanApply || plan.ExpectedDataRevision == nil || plan.Impact.Records != 3 {
		t.Fatalf("preview=%#v %v", plan, err)
	}
	step := plan.Steps[len(plan.Steps)-1]
	samples := step.Details["samples"].([]map[string]any)
	if step.Kind != "autoNumberBackfill" || step.Details["order"] != "id asc" || samples[0]["recordId"] != "aa0000000000001" || samples[0]["value"] != "HT-000001" {
		t.Fatalf("preview order=%#v", step)
	}
	collection, _ := app.FindCollectionByNameOrId(table.PhysicalName)
	if collection.Fields.GetByName(plan.After.Identity.PhysicalName) != nil {
		t.Fatal("preview wrote field")
	}
	changedID := "vv0000000000001"
	seed.RequestID, seed.IdempotencyKey = "stale_insert", "stale_insert"
	seed.Operations = []mutation.Operation{{Kind: mutation.OperationInsert, RecordID: &changedID, Values: map[string]any{}}}
	if _, err := kernel.Apply(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Apply(ctx, v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "backfill_stale", Actor: plan.Intent.Actor}); err == nil {
		t.Fatal("stale data revision backfill applied")
	}
	collection, _ = app.FindCollectionByNameOrId(table.PhysicalName)
	if collection.Fields.GetByName(plan.After.Identity.PhysicalName) != nil {
		t.Fatal("stale backfill wrote its field")
	}
	seed.RequestID, seed.IdempotencyKey = "stale_delete", "stale_delete"
	seed.Operations = []mutation.Operation{{Kind: mutation.OperationDelete, RecordID: &changedID}}
	if _, err := kernel.Apply(ctx, seed); err != nil {
		t.Fatal(err)
	}
	plan, err = planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID, ExpectedSchemaRev: table.SchemaRevision, Draft: &draft, Actor: v2.Actor{ID: "local", Kind: "user"}})
	if err != nil || !plan.CanApply {
		t.Fatalf("fresh backfill preview: %#v %v", plan, err)
	}
	req := v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "backfill", Actor: plan.Intent.Actor}
	if _, err := executor.ApplyWithCommit(ctx, req, func(core.App, v2.ApplyReceipt) error { return errors.New("commit failed") }); err == nil {
		t.Fatal("commit failure accepted")
	}
	collection, _ = app.FindCollectionByNameOrId(table.PhysicalName)
	if collection.Fields.GetByName(plan.After.Identity.PhysicalName) != nil {
		t.Fatal("failed backfill left a field")
	}
	req.OperationID = "backfill_success"
	receipt, err := executor.Apply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	records, err := app.FindRecordsByFilter(collection.Name, "", "id", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range records {
		if r.GetString(receipt.Definition.Identity.PhysicalName) != fmt.Sprintf("HT-%06d", i+1) {
			t.Fatalf("unstable backfill: %v", r)
		}
	}
	updated := draft
	updated.AutoNumber = &v2.AutoNumberSpec{Prefix: "X", Start: 1, Width: 6}
	if _, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: table.TableID, FieldID: receipt.FieldID, ExpectedSchemaRev: receipt.SchemaRevision, Draft: &updated, Actor: plan.Intent.Actor}); err == nil {
		t.Fatal("numbering configuration changed")
	}
	rename := draft
	rename.DisplayName = "新合同编号"
	renamed, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: table.TableID, FieldID: receipt.FieldID, ExpectedSchemaRev: receipt.SchemaRevision, Draft: &rename, Actor: plan.Intent.Actor})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Apply(ctx, v2.ApplyRequest{PlanID: renamed.PlanID, PlanHash: renamed.PlanHash, OperationID: "rename", Actor: plan.Intent.Actor}); err != nil {
		t.Fatal(err)
	}
	fields, err := autonumber.Load(ctx, app, table.TableID)
	if err != nil || len(fields) != 1 {
		t.Fatalf("rename reset allocator: %v %v", fields, err)
	}
}

func TestAutoNumberTextFormulaAndLookupConsumption(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	target := createV2IntegrationTable(t, ctx, app, "Contracts", "number_text_target")
	targetNumber := createV2IntegrationField(t, ctx, app, target.TableID, autoNumberDraft(t), "number_text_number")
	source := createV2IntegrationTable(t, ctx, app, "Links", "number_text_source")
	sourceName := createV2IntegrationField(t, ctx, app, source.TableID, fieldDraftForIntegration(t, v2.LogicalText, "Name"), "number_text_name")
	sourceNumber := createV2IntegrationField(t, ctx, app, source.TableID, autoNumberDraft(t), "number_text_source_number")
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store, fieldchange.WithFormulaBackfillScheduler(&atomicFormulaScheduler{}))
	relationDraft := fieldDraftForIntegration(t, v2.LogicalRelation, "Contract")
	relationDraft.Relation = &v2.RelationSpec{TargetTableID: target.TableID, Cardinality: "one", DeletePolicy: "setNull", DisplayField: targetNumber.FieldID}
	revisions, _ := catalog.Revisions(ctx, source.TableID)
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: source.TableID, ExpectedSchemaRev: revisions.Schema, Draft: &relationDraft, Actor: v2.Actor{ID: "test", Kind: "user"}, RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "Links", ReciprocalCardinality: "many", SourceDisplayFieldID: sourceName.FieldID}})
	if err != nil || !plan.CanApply {
		t.Fatalf("relation with number label: %#v %v", plan, err)
	}
	link, err := executor.Apply(ctx, v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "number_text_link", Actor: plan.Intent.Actor})
	if err != nil {
		t.Fatal(err)
	}
	formulaDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Number formula")
	formulaDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "lower(" + sourceNumber.Definition.Identity.PhysicalName + ")"}
	calculated := applyCreatedField(t, ctx, catalog, planner, executor, source.TableID, formulaDraft, v2.Actor{ID: "test", Kind: "user"}, "number_text_formula")
	if calculated.Definition.Formula.ResultType != v2.LogicalText {
		t.Fatalf("number formula inferred %#v", calculated.Definition.Formula)
	}
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Original number")
	lookupDraft.Lookup = &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: link.FieldID}}, TargetFieldID: targetNumber.FieldID}
	looked := applyCreatedField(t, ctx, catalog, planner, executor, source.TableID, lookupDraft, v2.Actor{ID: "test", Kind: "user"}, "number_text_lookup")
	kernel := mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(computed.New(lookup.NewCalculator(), formula.NewCalculator(nil))))
	insert := func(tableID, key string, values map[string]any) mutation.Receipt {
		revs, err := catalog.Revisions(ctx, tableID)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := kernel.Apply(ctx, mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: key, IdempotencyKey: key, TableID: tableID, SchemaRevision: revs.Schema, Operations: []mutation.Operation{{Kind: mutation.OperationInsert, Values: values}}, Actor: mutation.Actor{Type: "plugin", ID: "number-plugin"}})
		if err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	parent := insert(target.TableID, "number_text_parent", map[string]any{})
	child := insert(source.TableID, "number_text_child", map[string]any{link.FieldID: parent.AffectedRows[0].RecordID})
	record, err := app.FindRecordById(source.PhysicalName, child.AffectedRows[0].RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if record.GetString(sourceNumber.Definition.Identity.PhysicalName) != "HT-000001" || child.ComputedFields[record.Id][calculated.Definition.Identity.PhysicalName] != "ht-000001" || child.ComputedFields[record.Id][looked.Definition.Identity.PhysicalName] != "HT-000001" {
		t.Fatalf("number text consumers: %#v", child.ComputedFields)
	}
	for _, expected := range []struct{ field, value string }{{calculated.Definition.Identity.PhysicalName, "ht-000001"}, {looked.Definition.Identity.PhysicalName, "HT-000001"}} {
		envelope, ok := relatedcomputation.Decode(record.GetRaw(expected.field))
		if !ok || envelope.State != "ready" || envelope.Value != expected.value {
			t.Fatalf("stored number text consumer: %#v", envelope)
		}
	}
}

func TestAutoNumberSafeRangeBatchExhaustionRollsBack(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "Last safe numbers", "number_exhaust_table")
	draft := autoNumberDraft(t)
	draft.AutoNumber.Start = v2.MaxAutoNumber - 1
	field := createV2IntegrationField(t, ctx, app, table.TableID, draft, "number_exhaust_field")
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	req := mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: "number_exhaust_batch", IdempotencyKey: "number_exhaust_batch", TableID: table.TableID, SchemaRevision: field.SchemaRevision, Actor: mutation.Actor{Type: "user", ID: "test"}, Operations: []mutation.Operation{{Kind: mutation.OperationInsert, Values: map[string]any{}}, {Kind: mutation.OperationInsert, Values: map[string]any{}}, {Kind: mutation.OperationInsert, Values: map[string]any{}}}}
	if _, err := kernel.Apply(ctx, req); err == nil {
		t.Fatal("overflow batch accepted")
	}
	records, err := app.FindAllRecords(table.PhysicalName)
	if err != nil || len(records) != 0 {
		t.Fatalf("overflow batch partial rows: %d %v", len(records), err)
	}
	metadata, err := app.FindFirstRecordByFilter("vibetable_fields", "table_id={:table} && field_id={:field}", dbx.Params{"table": table.TableID, "field": field.FieldID})
	if err != nil {
		t.Fatal(err)
	}
	high, err := autonumber.DecodeState([]byte(metadata.GetString(autonumber.StateColumn)), *draft.AutoNumber)
	if err != nil || high != v2.MaxAutoNumber-2 {
		t.Fatalf("overflow advanced state: %d %v", high, err)
	}
	req.RequestID, req.IdempotencyKey = "number_final_two", "number_final_two"
	req.Operations = req.Operations[:2]
	if _, err := kernel.Apply(ctx, req); err != nil {
		t.Fatal(err)
	}
	req.RequestID, req.IdempotencyKey = "number_exhausted", "number_exhausted"
	req.Operations = req.Operations[:1]
	if _, err := kernel.Apply(ctx, req); err == nil {
		t.Fatal("exhausted field accepted a new record")
	}
	records, err = app.FindAllRecords(table.PhysicalName)
	if err != nil || len(records) != 2 {
		t.Fatalf("final rows: %d %v", len(records), err)
	}
}

func TestAutoNumberHistoryRestoresDeletedWholeRowWithFreshNumber(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "Number history", "number_history_table")
	field := createV2IntegrationField(t, ctx, app, table.TableID, autoNumberDraft(t), "number_history_field")
	physical := field.Definition.Identity.PhysicalName
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	recordID := "numberhistory01"
	for _, step := range []struct {
		key       string
		operation mutation.Operation
	}{
		{"number-history-insert", mutation.Operation{Kind: mutation.OperationInsert, RecordID: &recordID, Values: map[string]any{}}},
		{"number-history-delete", mutation.Operation{Kind: mutation.OperationDelete, RecordID: &recordID}},
	} {
		if _, err := kernel.Apply(ctx, mutationRequest(table.TableID, field.SchemaRevision, step.key, step.operation)); err != nil {
			t.Fatalf("%s: %v", step.key, err)
		}
	}
	target, err := app.FindFirstRecordByFilter("vibetable_audit_events", "request_id='req-number-history-delete'")
	if err != nil {
		t.Fatal(err)
	}
	service, err := audit.New(app, kernel)
	if err != nil {
		t.Fatal(err)
	}
	params := audit.PreviewParams{TableID: table.TableID, ItemID: recordID, TargetRevision: target.Id, Scope: "row"}
	preview, err := service.PreviewRestore(ctx, params)
	if err != nil || !preview.CanApply || len(preview.Restorable) != 0 {
		t.Fatalf("generated-only deleted whole row preview: %#v %v", preview, err)
	}
	conflictPreview, err := service.PreviewRestore(ctx, params)
	if err != nil || !conflictPreview.CanApply {
		t.Fatalf("second deleted preview: %#v %v", conflictPreview, err)
	}
	requireRejected := func(preview audit.Preview, want string) {
		t.Helper()
		_, err := service.ApplyRestore(ctx, audit.ApplyParams{TableID: table.TableID, ItemID: recordID, Token: preview.Token})
		var failure *audit.Error
		if !errors.As(err, &failure) || failure.Code != want {
			t.Fatalf("restore rejection: got %v want %s", err, want)
		}
	}
	for _, scope := range []string{"cell", "row"} {
		selected := params
		selected.Scope, selected.Field = scope, &field.FieldID
		single, err := service.PreviewRestore(ctx, selected)
		if err != nil || single.CanApply {
			t.Fatalf("field-specific restore must stay blocked: %#v %v", single, err)
		}
		requireRejected(single, "restore_no_fields")
	}
	result, err := service.ApplyRestore(ctx, audit.ApplyParams{TableID: table.TableID, ItemID: recordID, Token: preview.Token})
	if err != nil {
		t.Fatalf("restore generated-only deleted row: %v", err)
	}
	if result.ItemID != recordID || result.Item["id"] != recordID || result.Item[physical] != "HT-000002" || result.NewRevisionID == nil {
		t.Fatalf("restore did not preserve identity and allocate a fresh number: %#v", result)
	}
	record, err := app.FindRecordById(table.PhysicalName, recordID)
	if err != nil || record.GetString(physical) != "HT-000002" {
		t.Fatalf("authority stored wrong restored number: %#v %v", record, err)
	}
	requireRejected(preview, "restore_token_unknown")
	requireRejected(conflictPreview, "restore_conflict")
	unchanged, err := service.PreviewRestore(ctx, params)
	if err != nil || unchanged.CanApply {
		t.Fatalf("existing row generated-only patch must stay blocked: %#v %v", unchanged, err)
	}
	requireRejected(unchanged, "restore_no_fields")
	next, err := kernel.Apply(ctx, mutationRequest(table.TableID, field.SchemaRevision, "number-history-next", mutation.Operation{Kind: mutation.OperationInsert, Values: map[string]any{}}))
	if err != nil {
		t.Fatal(err)
	}
	nextRecord, err := app.FindRecordById(table.PhysicalName, next.AffectedRows[0].RecordID)
	if err != nil || nextRecord.GetString(physical) != "HT-000003" {
		t.Fatalf("blocked restores consumed a number: %#v %v", nextRecord, err)
	}
}
