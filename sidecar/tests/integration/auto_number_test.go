package integration_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/autonumber"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/workspacedb"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
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
	executor := fieldchange.NewExecutor(app, store, fieldchange.WithAutoNumberReformatter(kernel))
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
	seedClock := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	kernel := mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithClock(func() time.Time { return seedClock }))
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
	executor := fieldchange.NewExecutor(app, store, fieldchange.WithAutoNumberReformatter(kernel))
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
	datedDraft := draft
	datedDraft.AutoNumber = &v2.AutoNumberSpec{Prefix: "HT-", Start: 1, Width: 6, DateFormat: v2.AutoNumberDateFormat}
	datedPlan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID,
		ExpectedSchemaRev: table.SchemaRevision, Draft: &datedDraft, Actor: plan.Intent.Actor})
	if err != nil || !datedPlan.CanApply {
		t.Fatalf("dated backfill preview: %#v, %v", datedPlan, err)
	}
	datedSamples := datedPlan.Steps[len(datedPlan.Steps)-1].Details["samples"].([]map[string]any)
	if datedSamples[0]["value"] != "HT-20261008000001" {
		t.Fatalf("preview ignored the creation audit: %#v", datedSamples)
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
	// start stays immutable; prefix changes go through an in-transaction reformat.
	startChange := draft
	startChange.AutoNumber = &v2.AutoNumberSpec{Prefix: "HT-", Start: 5, Width: 6}
	if _, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: table.TableID, FieldID: receipt.FieldID, ExpectedSchemaRev: receipt.SchemaRevision, Draft: &startChange, Actor: plan.Intent.Actor}); err == nil {
		t.Fatal("start change accepted")
	}
	reformatted := draft
	reformatted.AutoNumber = &v2.AutoNumberSpec{Prefix: "X", Start: 1, Width: 7, DateFormat: v2.AutoNumberDateFormat}
	reformatPlan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: table.TableID, FieldID: receipt.FieldID, ExpectedSchemaRev: receipt.SchemaRevision, Draft: &reformatted, Actor: plan.Intent.Actor})
	if err != nil {
		t.Fatal(err)
	}
	if reformatPlan.CreatesMigration || reformatPlan.Impact.Records != int64(len(records)) {
		t.Fatalf("reformat plan = %#v", reformatPlan)
	}
	foundConfirmation := false
	foundStep := false
	for _, confirmation := range reformatPlan.Confirmations {
		foundConfirmation = foundConfirmation || confirmation == "autoNumberReformat"
	}
	for _, step := range reformatPlan.Steps {
		foundStep = foundStep || step.Kind == "autoNumberReformat"
	}
	if !foundConfirmation || !foundStep {
		t.Fatalf("reformat plan missing confirmation/step: %#v", reformatPlan)
	}
	if _, err := fieldchange.NewExecutor(app, store).Apply(ctx, v2.ApplyRequest{
		PlanID: reformatPlan.PlanID, PlanHash: reformatPlan.PlanHash, OperationID: "reformat-no-authority",
		Actor: plan.Intent.Actor, Confirmations: reformatPlan.Confirmations,
	}); err == nil {
		t.Fatal("stored numbers reformatted without the business authority")
	}
	reformatReceipt, err := executor.Apply(ctx, v2.ApplyRequest{PlanID: reformatPlan.PlanID, PlanHash: reformatPlan.PlanHash, OperationID: "reformat", Actor: plan.Intent.Actor, Confirmations: reformatPlan.Confirmations})
	if err != nil {
		t.Fatal(err)
	}
	records, err = app.FindRecordsByFilter(collection.Name, "", "id", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range records {
		if got := r.GetString(reformatReceipt.Definition.Identity.PhysicalName); got != "X"+seedClock.Format("20060102")+fmt.Sprintf("%07d", i+1) {
			t.Fatalf("reformat value = %s", got)
		}
	}
	loaded, err := autonumber.Load(ctx, app, table.TableID)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("reformat reset allocator: %v %v", loaded, err)
	}
	rename := draft
	rename.DisplayName = "新合同编号"
	rename.AutoNumber = reformatted.AutoNumber
	renamed, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: table.TableID, FieldID: reformatReceipt.FieldID, ExpectedSchemaRev: reformatReceipt.SchemaRevision, Draft: &rename, Actor: plan.Intent.Actor})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Apply(ctx, v2.ApplyRequest{PlanID: renamed.PlanID, PlanHash: renamed.PlanHash, OperationID: "rename", Actor: plan.Intent.Actor, Confirmations: renamed.Confirmations}); err != nil {
		t.Fatal(err)
	}
	fields, err := autonumber.Load(ctx, app, table.TableID)
	if err != nil || len(fields) != 1 {
		t.Fatalf("rename reset allocator: %v %v", fields, err)
	}
}

func TestAutoNumberReformatParksRowsToAvoidIntermediateUniqueCollisions(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "重排冲突", "auto_number_reformat_collision")
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	seed := mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: "seed", IdempotencyKey: "seed", TableID: table.TableID, SchemaRevision: table.SchemaRevision, Actor: mutation.Actor{Type: "user", ID: "local"}}
	for i := 1; i <= 20; i++ {
		id := fmt.Sprintf("aa%013d", i)
		seed.Operations = append(seed.Operations, mutation.Operation{Kind: mutation.OperationInsert, RecordID: &id, Values: map[string]any{}})
	}
	if _, err := kernel.Apply(ctx, seed); err != nil {
		t.Fatal(err)
	}
	draft := autoNumberDraft(t)
	draft.AutoNumber = &v2.AutoNumberSpec{Prefix: "", Start: 1, Width: 1}
	field := createV2IntegrationField(t, ctx, app, table.TableID, draft, "auto_number_reformat_field")
	collection, _ := app.FindCollectionByNameOrId(table.PhysicalName)
	physical := field.Definition.Identity.PhysicalName
	// Force the exact unique-index shape the compiler uses, so a transient
	// duplicate (seq 1 → "11" while seq 11 still stores "11") must fail a
	// naive single-phase rewrite.
	indexName := fmt.Sprintf("uniq_%s_%s", collection.Name, physical)
	hasIndex := false
	for _, index := range collection.Indexes {
		if strings.HasPrefix(index, "CREATE UNIQUE INDEX `"+indexName+"`") {
			hasIndex = true
			break
		}
	}
	if !hasIndex {
		collection.Indexes = append(collection.Indexes, fmt.Sprintf(
			"CREATE UNIQUE INDEX `%s` ON `%s` (`%s`)",
			indexName, collection.Name, physical))
		if err := app.Save(collection); err != nil {
			t.Fatal(err)
		}
	}
	records, err := app.FindRecordsByFilter(collection, "", "id", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for index, record := range records {
		if got := record.GetString(physical); got != strconv.Itoa(index+1) {
			t.Fatalf("backfill value = %s, want %d", got, index+1)
		}
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store, fieldchange.WithAutoNumberReformatter(kernel))
	updated := draft
	updated.AutoNumber = &v2.AutoNumberSpec{Prefix: "1", Start: 1, Width: 1}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: table.TableID, FieldID: field.FieldID,
		ExpectedSchemaRev: field.SchemaRevision, Draft: &updated,
		Actor: v2.Actor{ID: "local", Kind: "user"},
	})
	if err != nil || !plan.CanApply {
		t.Fatalf("reformat plan = %#v, %v", plan, err)
	}
	receipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "reformat_collision",
		Actor: plan.Intent.Actor, Confirmations: plan.Confirmations,
	})
	if err != nil {
		t.Fatalf("reformat under unique index failed: %v", err)
	}
	records, err = app.FindRecordsByFilter(collection, "", "id", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for index, record := range records {
		if got := record.GetString(physical); got != "1"+strconv.Itoa(index+1) {
			t.Fatalf("reformat value = %s, want 1%d", got, index+1)
		}
	}
	loaded, err := autonumber.Load(ctx, app, table.TableID)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("reformat reset allocator: %v %v", loaded, err)
	}
	// A value that no longer parses under the frozen before spec fails the
	// whole change before any write; existing values stay untouched.
	corrupt := records[0]
	corrupt.Set(physical, "not-a-number")
	if err := app.Save(corrupt); err != nil {
		t.Fatal(err)
	}
	broken := draft
	broken.AutoNumber = &v2.AutoNumberSpec{Prefix: "2", Start: 1, Width: 1}
	brokenPlan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: table.TableID, FieldID: receipt.FieldID,
		ExpectedSchemaRev: receipt.SchemaRevision, Draft: &broken,
		Actor: v2.Actor{ID: "local", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: brokenPlan.PlanID, PlanHash: brokenPlan.PlanHash, OperationID: "reformat_invalid",
		Actor: brokenPlan.Intent.Actor, Confirmations: brokenPlan.Confirmations,
	}); err == nil {
		t.Fatal("unparseable stored value accepted")
	}
	after, err := app.FindRecordsByFilter(collection, "", "id", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for index, record := range after {
		want := "not-a-number"
		if index > 0 {
			want = "1" + strconv.Itoa(index+1)
		}
		if got := record.GetString(physical); got != want {
			t.Fatalf("failed reformat mutated values: %s = %s, want %s", record.Id, got, want)
		}
	}
}

func TestAutoNumberReformatIgnoresIdOrderingForCollisionDetection(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "乱序重排", "auto_number_reformat_order")
	draft := autoNumberDraft(t)
	draft.AutoNumber = &v2.AutoNumberSpec{Prefix: "", Start: 1, Width: 1}
	field := createV2IntegrationField(t, ctx, app, table.TableID, draft, "auto_number_reformat_order_field")
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	seed := mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: "seed", IdempotencyKey: "seed", TableID: table.TableID, SchemaRevision: field.SchemaRevision, Actor: mutation.Actor{Type: "user", ID: "local"}}
	// The allocator numbers in insert order; the seq-11 row deliberately gets
	// the lowest-sorting id, so id-asc processing sees old "11" before the
	// row whose new value is also "11".
	ids := []string{"zz0000000000001"}
	for i := 2; i <= 10; i++ {
		ids = append(ids, fmt.Sprintf("mm%013d", i))
	}
	ids = append(ids, "aa0000000000011")
	for _, id := range ids {
		id := id
		seed.Operations = append(seed.Operations, mutation.Operation{Kind: mutation.OperationInsert, RecordID: &id, Values: map[string]any{}})
	}
	if _, err := kernel.Apply(ctx, seed); err != nil {
		t.Fatal(err)
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store, fieldchange.WithAutoNumberReformatter(kernel))
	updated := draft
	updated.AutoNumber = &v2.AutoNumberSpec{Prefix: "1", Start: 1, Width: 1}
	// The seed mutation advanced the schema revision past the field receipt.
	revisions, err := catalog.Revisions(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: table.TableID, FieldID: field.FieldID,
		ExpectedSchemaRev: revisions.Schema, Draft: &updated,
		Actor: v2.Actor{ID: "local", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "reformat_order",
		Actor: plan.Intent.Actor, Confirmations: plan.Confirmations,
	}); err != nil {
		t.Fatalf("id-ordering false conflict: %v", err)
	}
	collection, _ := app.FindCollectionByNameOrId(table.PhysicalName)
	records, err := app.FindRecordsByFilter(collection, "", "id", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"aa0000000000011": "111",
		"zz0000000000001": "11",
	}
	for _, record := range records {
		expected, ok := want[record.Id]
		if ok && record.GetString(field.Definition.Identity.PhysicalName) != expected {
			t.Fatalf("%s = %s, want %s", record.Id, record.GetString(field.Definition.Identity.PhysicalName), expected)
		}
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
	jobService := jobs.New(app, nil)
	defer jobService.Shutdown()
	creationTime := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	kernel := mutation.New(app, mutation.MetadataSchemaSource{},
		mutation.WithClock(func() time.Time { return creationTime }),
		mutation.WithFormulaCalculator(computed.New(lookup.NewCalculator(), formula.NewCalculator(nil))),
		mutation.WithComputationInvalidator(jobService), mutation.WithPublisher(jobService))
	jobService.SetKernel(kernel)
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store, fieldchange.WithFormulaBackfillScheduler(&atomicFormulaScheduler{}), fieldchange.WithAutoNumberReformatter(kernel))
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
	lengthDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Number length")
	lengthDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "size(" + sourceNumber.Definition.Identity.PhysicalName + ")"}
	length := applyCreatedField(t, ctx, catalog, planner, executor, source.TableID, lengthDraft, v2.Actor{ID: "test", Kind: "user"}, "number_length")
	totalDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "All number lengths")
	totalDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v2", Source: fmt.Sprintf("SUM(PROJECT(TABLE(%q), CurrentValue.%s))", source.TableID, length.Definition.Identity.PhysicalName)}
	total := applyCreatedField(t, ctx, catalog, planner, executor, source.TableID, totalDraft, v2.Actor{ID: "test", Kind: "user"}, "number_lengths_total")
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
	insert(source.TableID, "number_text_second_child", map[string]any{link.FieldID: parent.AffectedRows[0].RecordID})
	reformat := func(tableID string, field v2.ApplyReceipt, prefix string) v2.ApplyReceipt {
		t.Helper()
		draft := autoNumberDraft(t)
		draft.AutoNumber = &v2.AutoNumberSpec{Prefix: prefix, Start: 1, Width: 6, DateFormat: v2.AutoNumberDateFormat}
		revisions, err := catalog.Revisions(ctx, tableID)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: tableID,
			FieldID: field.FieldID, ExpectedSchemaRev: revisions.Schema, Draft: &draft,
			Actor: v2.Actor{ID: "test", Kind: "user"}})
		if err != nil {
			t.Fatal(err)
		}
		request := v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "format-" + tableID,
			Actor: plan.Intent.Actor, Confirmations: plan.Confirmations}
		physicalName := map[string]string{source.TableID: source.PhysicalName, target.TableID: target.PhysicalName}[tableID]
		for _, missing := range []string{"calculator", "invalidator"} {
			if missing == "calculator" && tableID != source.TableID {
				continue
			}
			options := []mutation.Option{mutation.WithFormulaCalculator(computed.New(lookup.NewCalculator(), formula.NewCalculator(nil)))}
			if missing == "calculator" {
				options = []mutation.Option{mutation.WithComputationInvalidator(jobService)}
			}
			unavailable := fieldchange.NewExecutor(app, store, fieldchange.WithAutoNumberReformatter(mutation.New(app, mutation.MetadataSchemaSource{}, options...)))
			unavailableRequest := request
			unavailableRequest.OperationID += "-missing-" + missing
			if _, err := unavailable.Apply(ctx, unavailableRequest); err == nil {
				t.Fatalf("format accepted missing %s", missing)
			}
		}
		rowsBefore, err := app.FindRecordsByFilter(physicalName, "", "id", 0, 0)
		if err != nil || len(rowsBefore) == 0 {
			t.Fatalf("format fixture row: %v", err)
		}
		cancelCtx, cancel := context.WithCancel(ctx)
		cancelKernel := mutation.New(app, mutation.MetadataSchemaSource{},
			mutation.WithFormulaCalculator(computed.New(lookup.NewCalculator(), formula.NewCalculator(nil))),
			mutation.WithComputationInvalidator(jobService), mutation.WithFaultInjector(func(point string) error {
				if point == "after_record" {
					cancel()
				}
				return nil
			}))
		cancelRequest := request
		cancelRequest.OperationID += "-cancelled"
		_, cancelErr := fieldchange.NewExecutor(app, store, fieldchange.WithAutoNumberReformatter(cancelKernel)).Apply(cancelCtx, cancelRequest)
		cancel()
		if !errors.Is(cancelErr, context.Canceled) {
			t.Fatalf("mid-format cancellation was not propagated: %v", cancelErr)
		}
		failingKernel := mutation.New(app, mutation.MetadataSchemaSource{},
			mutation.WithFormulaCalculator(computed.New(lookup.NewCalculator(), formula.NewCalculator(nil))),
			mutation.WithComputationInvalidator(failingComputationInvalidator{}))
		failingExecutor := fieldchange.NewExecutor(app, store, fieldchange.WithAutoNumberReformatter(failingKernel))
		failedRequest := request
		failedRequest.OperationID += "-rollback"
		if _, err := failingExecutor.Apply(ctx, failedRequest); err == nil {
			t.Fatal("format committed without its durable invalidation")
		}
		for _, original := range rowsBefore {
			rolledBack, err := app.FindRecordById(original.Collection(), original.Id)
			if err != nil || rolledBack.GetString(field.Definition.Identity.PhysicalName) != original.GetString(field.Definition.Identity.PhysicalName) {
				t.Fatalf("failed invalidation changed the number: %v", err)
			}
		}
		failedAudits, err := app.CountRecords("vibetable_audit_events", dbx.HashExp{"request_id": "field-change:" + failedRequest.OperationID})
		if err != nil || failedAudits != 0 {
			t.Fatalf("failed format committed business audit: %d, %v", failedAudits, err)
		}
		applied, err := executor.Apply(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := executor.Apply(ctx, request); err != nil {
			t.Fatalf("format replay: %v", err)
		}
		audits, err := app.FindRecordsByFilter("vibetable_audit_events", "request_id={:request}", "", 0, 0,
			dbx.Params{"request": "field-change:" + request.OperationID})
		if err != nil || len(audits) != len(rowsBefore) {
			t.Fatalf("one final-value audit per changed row after replay: %d, %v", len(audits), err)
		}
		return applied
	}
	assertReady := func(field v2.ApplyReceipt, want any) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			row, err := app.FindRecordById(source.PhysicalName, record.Id)
			if err != nil {
				t.Fatal(err)
			}
			envelope, valid := relatedcomputation.Decode(row.GetRaw(field.Definition.Identity.PhysicalName))
			fields, err := catalog.Fields(ctx, source.TableID, false)
			if err != nil {
				t.Fatal(err)
			}
			expectation, err := relatedcomputation.ExpectationFor(ctx, app, source.TableID, fields,
				field.FieldID, int64(row.GetInt(relatedcomputation.RowRevisionField)))
			if err != nil {
				t.Fatal(err)
			}
			if valid && envelope.Fresh(expectation) && envelope.Value == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("computed value never became ready: %#v, want %v", envelope, want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	assertReady(total, float64(18))
	sourceReformat := reformat(source.TableID, sourceNumber, "SOURCE-")
	assertReady(calculated, "source-20261025000001")
	assertReady(total, float64(42))
	guarded := mutationRequest(source.TableID, sourceReformat.SchemaRevision, "number-format-edit",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &record.Id,
			Values: map[string]any{sourceName.FieldID: "Updated"}})
	staleRevision := child.AffectedRows[0].Revision
	guarded.ExpectedRevision = &staleRevision
	if _, err := kernel.Apply(ctx, guarded); err == nil {
		t.Fatal("pre-format row guard accepted")
	}
	nextRevision := "row_0002"
	guarded.ExpectedRevision = &nextRevision
	edited, err := kernel.Apply(ctx, guarded)
	if err != nil || edited.AffectedRows[0].Revision != "row_0003" {
		t.Fatalf("ordinary edit after format has wrong revision: %#v, %v", edited, err)
	}
	reformat(target.TableID, targetNumber, "TARGET-")
	assertReady(looked, "TARGET-20261025000001")
	assertReady(calculated, "source-20261025000001")
	jobService.Shutdown()
	if err := writecoordinator.EnsurePocketBaseReceiptTable(ctx, app); err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(t.TempDir(), "number-format.db")
	if _, err := app.DB().NewQuery("VACUUM INTO {:path}").Bind(dbx.Params{"path": snapshotPath}).Execute(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspacedb.ValidateSnapshot(ctx, snapshot, workspacedb.SupportedBusinessSchemaVersion); err != nil {
		t.Fatalf("formatted workspace snapshot rejected: %v", err)
	}
}

type autoNumberUnavailableSchema struct{}

func (autoNumberUnavailableSchema) Describe(context.Context, core.App, string) (schemaexecution.Table, error) {
	return schemaexecution.Table{}, errors.New("schema unavailable")
}

type autoNumberUnavailableCalculator struct{}

func (autoNumberUnavailableCalculator) Calculate(context.Context, core.App, schemaexecution.Table, *core.Record) (map[string]any, error) {
	return nil, errors.New("calculator failed")
}

func TestAutoNumberReformatFailuresRollBackTheWholeBusinessChange(t *testing.T) {
	for _, failure := range []string{"schema", "exhausted", "cancel-after-final-values", "row-save", "calculator", "after-record", "table-save", "outbox", "computed-save"} {
		t.Run(failure, func(t *testing.T) {
			app := bootstrapApp(t, queryTempDir(t))
			defer resetApp(t, app)
			ctx := context.Background()
			table := createV2IntegrationTable(t, ctx, app, "编号失败回滚", "number_failure")
			field := createV2IntegrationField(t, ctx, app, table.TableID, autoNumberDraft(t), "number_failure_field")
			draft := fieldDraftForIntegration(t, v2.LogicalFormula, "编号公式")
			draft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "lower(" + field.Definition.Identity.PhysicalName + ")"}
			calculated := createV2IntegrationFormula(t, ctx, app, table.TableID, draft, "number_failure_formula")
			calculator := computed.New(lookup.NewCalculator(), formula.NewCalculator(nil))
			seedKernel := mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(calculator))
			seed, err := seedKernel.Apply(ctx, mutationRequest(table.TableID, calculated.SchemaRevision, "number_failure_seed", mutation.Operation{Kind: mutation.OperationInsert, Values: map[string]any{}}))
			if err != nil {
				t.Fatal(err)
			}
			recordID := seed.AffectedRows[0].RecordID
			var source mutation.SchemaSource = mutation.MetadataSchemaSource{}
			options := []mutation.Option{mutation.WithFormulaCalculator(calculator)}
			if failure == "schema" {
				source = autoNumberUnavailableSchema{}
			}
			if failure == "calculator" {
				options = []mutation.Option{mutation.WithFormulaCalculator(autoNumberUnavailableCalculator{})}
			}
			if failure == "after-record" {
				options = append(options, mutation.WithFaultInjector(func(point string) error {
					if point == "after_record" {
						return errors.New("after-record failure")
					}
					return nil
				}))
			}
			writeCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			var unbind func()
			switch failure {
			case "cancel-after-final-values", "row-save", "computed-save":
				updates := 0
				hook := app.OnRecordUpdate(table.PhysicalName).BindFunc(func(event *core.RecordEvent) error {
					updates++
					if (failure == "row-save" && updates == 3) || (failure == "computed-save" && updates == 5) {
						return errors.New("row persistence failed")
					}
					err := event.Next()
					if failure == "cancel-after-final-values" && updates == 2 {
						cancel()
					}
					return err
				})
				unbind = func() { app.OnRecordUpdate(table.PhysicalName).Unbind(hook) }
			case "table-save":
				hook := app.OnRecordUpdate("vibetable_tables").BindFunc(func(*core.RecordEvent) error { return errors.New("table revision persistence failed") })
				unbind = func() { app.OnRecordUpdate("vibetable_tables").Unbind(hook) }
			case "outbox":
				hook := app.OnRecordCreate("vibetable_outbox").BindFunc(func(*core.RecordEvent) error { return errors.New("event persistence failed") })
				unbind = func() { app.OnRecordCreate("vibetable_outbox").Unbind(hook) }
			}
			after := *field.Definition
			after.AutoNumber = &v2.AutoNumberSpec{Prefix: "NEW-", Start: 1, Width: 6}
			err = app.RunInTransaction(func(tx core.App) error {
				if failure == "exhausted" {
					metadata, err := tx.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": table.TableID})
					if err != nil {
						return err
					}
					metadata.Set("data_revision", v2.MaxAutoNumber)
					if err := tx.Save(metadata); err != nil {
						return err
					}
				}
				_, err := mutation.New(app, source, options...).ReformatAutoNumber(writeCtx, tx, table.TableID, *field.Definition, after, v2.Actor{ID: "test", Kind: "user"}, "number_failure_format")
				return err
			})
			if unbind != nil {
				unbind()
			}
			if err == nil {
				t.Fatal("format committed after injected failure")
			}
			row, err := app.FindRecordById(table.PhysicalName, recordID)
			if err != nil || row.GetString(field.Definition.Identity.PhysicalName) != "HT-000001" || row.GetInt(relatedcomputation.RowRevisionField) != 1 {
				t.Fatalf("failed format changed the row: %#v, %v", row, err)
			}
			for _, collection := range []string{"vibetable_audit_events", "vibetable_outbox"} {
				count, err := app.CountRecords(collection)
				if err != nil || count != 1 {
					t.Fatalf("failed format persisted %s: %d, %v", collection, count, err)
				}
			}
			metadata, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": table.TableID})
			if err != nil || metadata.GetInt("data_revision") != 1 {
				t.Fatalf("failed format advanced table revision: %#v, %v", metadata, err)
			}
		})
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
