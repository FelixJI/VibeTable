package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func smallCalculationChainData() calculationChainData {
	data := newCalculationChainData(201)
	data.Main = []calculationChainMain{
		{ID: "qm0000000000000", Key: "empty"},
		{ID: "qm0000000000001", Key: "single"},
		{ID: "qm0000000000002", Key: "many"},
	}
	return data
}

func calculationChainJobs(t *testing.T, f *calculationChainFixture) *jobs.Service {
	t.Helper()
	service := jobs.New(f.app, nil)
	t.Cleanup(service.Shutdown)
	f.kernel = mutation.New(f.app, mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(computed.New(lookup.NewCalculator(), formula.NewCalculator(f.compiler))),
		mutation.WithComputationInvalidator(service))
	service.SetKernel(f.kernel)
	service.SetBusinessWriteGate(func(ctx context.Context, kind, identity string, apply func(context.Context) error) error {
		err := apply(ctx)
		if err != nil {
			t.Logf("calculation chain job %s/%s: %#v", kind, identity, err)
		}
		return err
	})
	return service
}

func drainCalculationChain(t *testing.T, ctx context.Context, f *calculationChainFixture, service *jobs.Service) {
	t.Helper()
	for range 24 {
		pending, err := f.app.FindRecordsByFilter("vibetable_jobs", "state='queued'", "+id", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 0 {
			return
		}
		for _, job := range pending {
			if err := service.Run(ctx, job.Id); err != nil {
				t.Fatalf("drain %s/%s: %#v", job.GetString("job_type"), job.Id, err)
			}
			finished, err := service.Get(ctx, job.Id)
			if err != nil || finished.State != "complete" {
				t.Fatalf("job did not complete: %+v, %v", finished, err)
			}
		}
	}
	t.Fatal("three-table jobs did not converge")
}

func applyCalculationChain(t *testing.T, ctx context.Context, f *calculationChainFixture, key string, operation mutation.Operation) {
	t.Helper()
	definition, err := schemaapi.New(f.app).Describe(ctx, f.source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := f.kernel.Apply(ctx, mutationRequest(f.source.TableID, definition.Snapshot.SchemaRevision, key, operation))
	if err != nil || receipt.Status != mutation.StatusApplied || len(receipt.Warnings) != 0 {
		t.Fatalf("%s: receipt=%+v error=%#v", key, receipt, err)
	}
}

func checkCalculationChain(t *testing.T, ctx context.Context, f *calculationChainFixture, instant time.Time, factor float64) {
	t.Helper()
	expected := calculationChainOracle(f.data, instant)
	for index := range expected {
		expected[index].Total = expected[index].Lookup * factor
	}
	source, err := queryschema.New(f.app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	page, err := query.NewPort(f.app, source).QueryPage(ctx, f.main.TableID, query.TableQuery{
		Filters: []query.FilterExpression{{Field: f.total.Identity.PhysicalName, Operator: query.OperatorGreaterEq, Value: 0.0}},
		Sorts:   []query.SortCondition{{Field: f.total.Identity.PhysicalName, Direction: query.SortDescending}}, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.checkPage(page, calculationChainOrdered(expected, 0)); err != nil {
		t.Fatal(err)
	}
}

// Persist real backfill jobs but dispatch them in the explicit drain below,
// rather than starting concurrent runners during deterministic assertions.
type calculationChainManualDispatch struct{ *jobs.Service }

func (calculationChainManualDispatch) Start(string) bool { return false }

func TestCalculationChainChangesPropagateAndRefresh(t *testing.T) {
	instant := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	ctx := formula.WithEvaluationTime(t.Context(), instant)
	f := newCalculationChainFixture(t, ctx, smallCalculationChainData())
	service := calculationChainJobs(t, f)
	check := func() { drainCalculationChain(t, ctx, f, service); checkCalculationChain(t, ctx, f, instant, 2) }
	check()

	f.data.Sources[1].Amount += 10
	applyCalculationChain(t, ctx, f, "chain_amount", mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &f.data.Sources[1].ID,
		Values: map[string]any{f.amount.Identity.PhysicalName: f.data.Sources[1].Amount}})
	// The transitive source edit is visible before any fanout runner executes.
	mainDefinition, err := schemaapi.New(f.app).Describe(ctx, f.main.TableID)
	if err != nil {
		t.Fatal(err)
	}
	mainRecord, err := f.app.FindRecordById(f.main.PhysicalName, f.data.Main[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	value, staleErr := relatedcomputation.NewSourceReader().Read(ctx, f.app, f.main.TableID, mainDefinition.Snapshot.Fields, f.total, mainRecord)
	var dependencyErr *formula.Error
	if !errors.As(staleErr, &dependencyErr) || dependencyErr.Code != "formula.dependency" || value != nil {
		t.Fatalf("transitive stale read before fanout: value=%v error=%v", value, staleErr)
	}
	check()
	inserted := calculationChainSource{ID: "qs0000000000201", Key: "empty", Amount: 5, Day: "2026-09-20", Wide: strings.Repeat("w", 1024)}
	f.data.Sources = append(f.data.Sources, inserted)
	applyCalculationChain(t, ctx, f, "chain_insert_unmatched", mutation.Operation{Kind: mutation.OperationInsert, RecordID: &inserted.ID,
		Values: map[string]any{f.sourceKey.Identity.PhysicalName: inserted.Key, f.amount.Identity.PhysicalName: inserted.Amount,
			f.enabled.Identity.PhysicalName: false, f.day.Identity.PhysicalName: inserted.Day, f.wide.Identity.PhysicalName: inserted.Wide}})
	check()
	f.data.Sources[201].Enabled = true
	applyCalculationChain(t, ctx, f, "chain_enter", mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &inserted.ID,
		Values: map[string]any{f.enabled.Identity.PhysicalName: true}})
	check()
	f.data.Sources[201].Key = "many"
	applyCalculationChain(t, ctx, f, "chain_move_condition", mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &inserted.ID,
		Values: map[string]any{f.sourceKey.Identity.PhysicalName: "many"}})
	check()
	f.data.Sources[201].Enabled = false
	applyCalculationChain(t, ctx, f, "chain_exit", mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &inserted.ID,
		Values: map[string]any{f.enabled.Identity.PhysicalName: false}})
	check()
	f.data.Sources = f.data.Sources[:201]
	applyCalculationChain(t, ctx, f, "chain_delete", mutation.Operation{Kind: mutation.OperationDelete, RecordID: &inserted.ID})
	check()

	beforeJobs, err := f.app.CountRecords("vibetable_jobs", dbx.HashExp{"job_type": "formula_fanout"})
	if err != nil {
		t.Fatal(err)
	}
	beforeCompiles := f.compiler.PlanCompilationCount()
	applyCalculationChain(t, ctx, f, "chain_unrelated", mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &f.data.Sources[0].ID,
		Values: map[string]any{f.sourceNote.Identity.PhysicalName: "unrelated edit"}})
	afterJobs, err := f.app.CountRecords("vibetable_jobs", dbx.HashExp{"job_type": "formula_fanout"})
	if err != nil || afterJobs != beforeJobs || f.compiler.PlanCompilationCount() != beforeCompiles {
		pending, readErr := f.app.FindRecordsByFilter("vibetable_jobs", "state='queued'", "+id", 0, 0)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, job := range pending {
			t.Logf("unrelated update note=%s job=%s sourceEvent=%s cursor=%s", f.sourceNote.Identity.FieldID, job.Id, job.GetString("source_event_id"), job.GetString("cursor_json"))
			event, readErr := f.app.FindFirstRecordByFilter("vibetable_outbox", "event_id={:id}", dbx.Params{"id": job.GetString("source_event_id")})
			if readErr != nil {
				t.Fatal(readErr)
			}
			t.Logf("unrelated source event=%s", event.GetString("payload_json"))
			var payload mutation.DataChangedEvent
			if err := json.Unmarshal([]byte(event.GetString("payload_json")), &payload); err != nil || payload.ChangeSetID == nil {
				t.Fatalf("source event change set: %v", err)
			}
			audits, readErr := f.app.FindRecordsByFilter("vibetable_audit_events", "change_set_id={:id}", "+sequence", 0, 0, dbx.Params{"id": *payload.ChangeSetID})
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, audit := range audits {
				var before, after map[string]any
				if err := json.Unmarshal([]byte(audit.GetString("before_json")), &before); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(audit.GetString("after_json")), &after); err != nil {
					t.Fatal(err)
				}
				for _, field := range []v2.FieldDefinition{f.sourceKey, f.amount, f.enabled, f.day, f.wide, f.sourceNote} {
					name := field.Identity.PhysicalName
					if !reflect.DeepEqual(before[name], after[name]) {
						t.Logf("audit changed field=%s before=%v after=%v operation=%s", field.Identity.FieldID, before[name], after[name], audit.GetString("operation"))
					}
				}
			}
		}
		dependencies, readErr := f.app.FindRecordsByFilter("vibetable_computation_dependencies", "target_table_id={:table}", "+source_table_id,+target_field_id", 0, 0, dbx.Params{"table": f.source.TableID})
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, dependency := range dependencies {
			t.Logf("source dependency table=%s computed=%s targetField=%s relation=%s", dependency.GetString("source_table_id"), dependency.GetString("computed_field_id"), dependency.GetString("target_field_id"), dependency.GetString("relation_field_id"))
		}
		t.Errorf("unrelated field triggered work: jobs %d -> %d, compiles %d -> %d, %v", beforeJobs, afterJobs, beforeCompiles, f.compiler.PlanCompilationCount(), err)
	}
	check()

	catalog := fieldchange.NewCatalog(f.app)
	store := fieldchange.NewPocketBasePlanStore(f.app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	mainRevisions, err := catalog.Revisions(ctx, f.main.TableID)
	if err != nil {
		t.Fatal(err)
	}
	rename := v2.FieldDraft{DisplayName: "改名后的无关备注", Help: f.mainNote.Help, LogicalType: f.mainNote.LogicalType,
		Value: f.mainNote.Value, Constraints: f.mainNote.Constraints, Storage: f.mainNote.Storage, Display: f.mainNote.Display}
	renamePlan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: f.main.TableID,
		FieldID: f.mainNote.Identity.FieldID, ExpectedSchemaRev: mainRevisions.Schema, Draft: &rename, Actor: actor})
	if err != nil || !renamePlan.CanApply {
		t.Fatalf("unrelated schema rename: %+v, %v", renamePlan, err)
	}
	if _, err := fieldchange.NewExecutor(f.app, store, fieldchange.WithFormulaBackfillScheduler(calculationChainManualDispatch{service})).Apply(ctx,
		v2.ApplyRequest{PlanID: renamePlan.PlanID, PlanHash: renamePlan.PlanHash, OperationID: "chain_rename", Actor: actor}); err != nil {
		t.Fatal(err)
	}
	checkCalculationChain(t, ctx, f, instant, 2)
	revisions, err := catalog.Revisions(ctx, f.summary.TableID)
	if err != nil {
		t.Fatal(err)
	}
	draft := v2.FieldDraft{DisplayName: f.doubled.DisplayName, Help: f.doubled.Help, LogicalType: f.doubled.LogicalType,
		Value: f.doubled.Value, Constraints: f.doubled.Constraints, Storage: f.doubled.Storage, Display: f.doubled.Display,
		Formula: &v2.FormulaDraftSpec{Language: "cel-v2", Source: f.summaryLookup.Identity.PhysicalName + " * 3.0"}}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: f.summary.TableID,
		FieldID: f.doubled.Identity.FieldID, ExpectedSchemaRev: revisions.Schema, Draft: &draft, Actor: actor})
	if err != nil || !plan.CanApply {
		t.Fatalf("formula update plan: %+v, %v", plan, err)
	}
	if _, err := fieldchange.NewExecutor(f.app, store, fieldchange.WithFormulaBackfillScheduler(calculationChainManualDispatch{service})).Apply(ctx,
		v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "chain_expression", Actor: actor}); err != nil {
		t.Fatal(err)
	}
	// A changed upstream definition must not leave the stored third-layer
	// value readable while its real backfill job is still queued.
	mainDefinition, err = schemaapi.New(f.app).Describe(ctx, f.main.TableID)
	if err != nil {
		t.Fatal(err)
	}
	mainRecord, err = f.app.FindRecordById(f.main.PhysicalName, f.data.Main[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	value, staleErr = relatedcomputation.NewSourceReader().Read(ctx, f.app, f.main.TableID, mainDefinition.Snapshot.Fields, f.total, mainRecord)
	dependencyErr = nil
	if !errors.As(staleErr, &dependencyErr) || dependencyErr.Code != "formula.dependency" || value != nil {
		t.Fatalf("transitive stale read after schema change before backfill: value=%v error=%v", value, staleErr)
	}
	drainCalculationChain(t, ctx, f, service)
	checkCalculationChain(t, ctx, f, instant, 3)
	if f.compiler.PlanCompilationCount() <= beforeCompiles {
		t.Fatal("schema change did not compile the new execution plan")
	}

	instant = instant.AddDate(0, 0, 1)
	ctx = formula.WithEvaluationTime(t.Context(), instant)
	ids, err := service.RefreshClock(ctx, instant)
	if err != nil || len(ids) == 0 {
		t.Fatalf("next clock period: jobs=%v error=%v", ids, err)
	}
	drainCalculationChain(t, ctx, f, service)
	checkCalculationChain(t, ctx, f, instant, 3)
}

func TestCalculationChainEquivalentDefinitionRefreshesDownstream(t *testing.T) {
	instant := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	ctx := formula.WithEvaluationTime(t.Context(), instant)
	f := newCalculationChainFixture(t, ctx, smallCalculationChainData())
	service := calculationChainJobs(t, f)
	checkCalculationChain(t, ctx, f, instant, 2)
	catalog := fieldchange.NewCatalog(f.app)
	store := fieldchange.NewPocketBasePlanStore(f.app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	revisions, err := catalog.Revisions(ctx, f.summary.TableID)
	if err != nil {
		t.Fatal(err)
	}
	draft := v2.FieldDraft{DisplayName: f.doubled.DisplayName, Help: f.doubled.Help, LogicalType: f.doubled.LogicalType,
		Value: f.doubled.Value, Constraints: f.doubled.Constraints, Storage: f.doubled.Storage, Display: f.doubled.Display,
		Formula: &v2.FormulaDraftSpec{Language: "cel-v2", Source: f.summaryLookup.Identity.PhysicalName + " * 2.0 + 0.0"}}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: f.summary.TableID,
		FieldID: f.doubled.Identity.FieldID, ExpectedSchemaRev: revisions.Schema, Draft: &draft, Actor: actor})
	if err != nil || !plan.CanApply {
		t.Fatalf("equivalent formula update plan: %+v, %v", plan, err)
	}
	if _, err := fieldchange.NewExecutor(f.app, store, fieldchange.WithFormulaBackfillScheduler(calculationChainManualDispatch{service})).Apply(ctx,
		v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "chain_equivalent_expression", Actor: actor}); err != nil {
		t.Fatal(err)
	}
	definition, err := schemaapi.New(f.app).Describe(ctx, f.main.TableID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := f.app.FindRecordById(f.main.PhysicalName, f.data.Main[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	value, staleErr := relatedcomputation.NewSourceReader().Read(ctx, f.app, f.main.TableID, definition.Snapshot.Fields, f.total, record)
	var dependencyErr *formula.Error
	if !errors.As(staleErr, &dependencyErr) || dependencyErr.Code != "formula.dependency" || value != nil {
		t.Fatalf("equivalent definition accepted old downstream value before backfill: value=%v error=%v", value, staleErr)
	}
	drainCalculationChain(t, ctx, f, service)
	checkCalculationChain(t, ctx, f, instant, 2)
}

func TestCalculationChainWorkspaceIsolationAndInFlightCancellation(t *testing.T) {
	instant := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	ctx := formula.WithEvaluationTime(t.Context(), instant)
	a := newCalculationChainFixture(t, ctx, smallCalculationChainData())
	directory := a.app.DataDir()
	if err := a.app.ResetBootstrapState(); err != nil || a.app.IsBootstrapped() {
		t.Fatalf("close synthetic app before copying: %v", err)
	}
	otherDirectory := queryTempDir(t)
	// Fixed, closed synthetic SQLite files only. Never copy a live WAL snapshot.
	for _, name := range []string{"data.db", "auxiliary.db"} {
		if info, err := os.Stat(filepath.Join(directory, name+"-wal")); err == nil && info.Size() != 0 {
			t.Fatalf("closed synthetic database retained WAL: %s", name)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(otherDirectory, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	appA, appB := bootstrapApp(t, directory), bootstrapApp(t, otherDirectory)
	t.Cleanup(func() { resetApp(t, appA); resetApp(t, appB) })
	a.app, a.compiler = appA, formula.NewAppCompilerWithLimits(appA, formula.Limits{EvalTimeout: collectionTestEvalTimeout})
	b := *a
	b.app, b.compiler, b.data = appB, formula.NewAppCompilerWithLimits(appB, formula.Limits{EvalTimeout: collectionTestEvalTimeout}), smallCalculationChainData()
	serviceA, serviceB := calculationChainJobs(t, a), calculationChainJobs(t, &b)
	for index, item := range []struct {
		f       *calculationChainFixture
		service *jobs.Service
	}{{a, serviceA}, {&b, serviceB}} {
		item.f.data.Sources[0].Amount += float64(1 + 99*index)
		applyCalculationChain(t, ctx, item.f, "chain_isolation", mutation.Operation{Kind: mutation.OperationUpdate,
			RecordID: &item.f.data.Sources[0].ID, Values: map[string]any{item.f.amount.Identity.PhysicalName: item.f.data.Sources[0].Amount}})
		drainCalculationChain(t, ctx, item.f, item.service)
	}
	for _, table := range []string{a.source.TableID, a.summary.TableID, a.main.TableID} {
		left, err := schemaapi.New(a.app).Describe(ctx, table)
		if err != nil {
			t.Fatal(err)
		}
		right, err := schemaapi.New(b.app).Describe(ctx, table)
		if err != nil {
			t.Fatal(err)
		}
		if left.Snapshot.SchemaRevision != right.Snapshot.SchemaRevision || left.Snapshot.DataRevision != right.Snapshot.DataRevision || !reflect.DeepEqual(left.Snapshot.Fields, right.Snapshot.Fields) {
			t.Fatalf("workspace collision fixture revisions/identities differ for %s", table)
		}
	}
	checkCalculationChain(t, ctx, a, instant, 2)
	checkCalculationChain(t, ctx, &b, instant, 2)
	checkCalculationChain(t, ctx, a, instant, 2)

	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	var entered atomic.Bool
	seen := map[*dbx.DB]bool{}
	for _, db := range []*dbx.DB{a.app.ConcurrentDB().(*dbx.DB), a.app.NonconcurrentDB().(*dbx.DB)} {
		if seen[db] {
			continue
		}
		seen[db] = true
		previous := db.QueryLogFunc
		db.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
			if strings.Contains(statement, " FROM \""+a.main.PhysicalName+"\"") && entered.CompareAndSwap(false, true) {
				cancel()
			}
			if previous != nil {
				previous(ctx, elapsed, statement, rows, err)
			}
		}
		defer func() { db.QueryLogFunc = previous }()
	}
	source, err := queryschema.New(a.app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = query.NewPort(a.app, source).QueryPage(canceled, a.main.TableID, query.TableQuery{
		Sorts: []query.SortCondition{{Field: a.total.Identity.PhysicalName, Direction: query.SortDescending}}, Limit: 100,
	})
	if !entered.Load() || !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight query cancellation: entered=%v error=%v", entered.Load(), err)
	}
	checkCalculationChain(t, ctx, a, instant, 2)
}
