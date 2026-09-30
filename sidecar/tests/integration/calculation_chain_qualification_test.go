package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

const calculationChainSeed = uint64(396)

type calculationChainSource struct {
	ID, Key, Day, Wide, Note string
	Amount                   float64
	Enabled                  bool
}

type calculationChainMain struct{ ID, Key, Note string }

type calculationChainData struct {
	Sources []calculationChainSource
	Keys    []string
	Main    []calculationChainMain
}

// The first 201 sources guarantee 0/1/200 matches at both qualification sizes.
// Remaining keys exercise >64 distinct conditions; the first 100 main rows
// deliberately repeat one condition. Wide text is stored, not returned by SUM.
func newCalculationChainData(sourceRows int) calculationChainData {
	random := rand.New(rand.NewPCG(calculationChainSeed, 390))
	data := calculationChainData{Keys: []string{"empty", "single", "many"}}
	for index := range 64 {
		data.Keys = append(data.Keys, fmt.Sprintf("group-%02d", index))
	}
	for index := range sourceRows {
		row := calculationChainSource{
			ID: fmt.Sprintf("qs%013d", index), Key: data.Keys[3+random.IntN(64)],
			Amount: float64(1 + random.IntN(99)), Enabled: random.IntN(4) != 0,
			Day:  fmt.Sprintf("2026-09-%02d", 1+random.IntN(28)),
			Wide: strings.Repeat("w", 1024) + fmt.Sprintf("-%d", index), Note: "unrelated",
		}
		if index < 201 {
			row.Key, row.Enabled, row.Day = "many", true, "2026-09-20"
			row.Amount = float64(1 + index%7)
			if index == 0 {
				row.Key, row.Amount = "single", 7
			}
		}
		data.Sources = append(data.Sources, row)
	}
	for index := range 1000 {
		key := "many"
		if index >= 100 {
			key = data.Keys[(index-100)%len(data.Keys)]
		}
		data.Main = append(data.Main, calculationChainMain{
			ID: fmt.Sprintf("qm%013d", index), Key: key, Note: "unrelated",
		})
	}
	return data
}

type calculationChainExpected struct {
	ID, Key              string
	Matches              int
	Lookup, Total, Clock float64
}

// Independent scalar oracle: no product calculator, query, CEL, or stored
// computed value is used. Recompute it after changing the raw fixture.
func calculationChainOracle(data calculationChainData, instant time.Time) []calculationChainExpected {
	sums, counts := map[string]float64{}, map[string]int{}
	for _, row := range data.Sources {
		if row.Enabled && row.Day >= "2026-09-10" {
			sums[row.Key] += row.Amount
			counts[row.Key]++
		}
	}
	clock := 0.0
	if instant.UTC().Format("2006-01-02") >= "2026-09-30" {
		clock = 1
	}
	result := make([]calculationChainExpected, 0, len(data.Main))
	for _, row := range data.Main {
		result = append(result, calculationChainExpected{
			ID: row.ID, Key: row.Key, Matches: counts[row.Key],
			Lookup: sums[row.Key], Total: sums[row.Key] * 2, Clock: clock,
		})
	}
	return result
}

func calculationChainOrdered(values []calculationChainExpected, minimum float64) []calculationChainExpected {
	result := make([]calculationChainExpected, 0, len(values))
	for _, value := range values {
		if value.Total >= minimum {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Total == result[right].Total {
			return result[left].ID < result[right].ID
		}
		return result[left].Total > result[right].Total
	})
	return result
}

func calculationChainMatchingKey(values []calculationChainExpected, key string) []calculationChainExpected {
	result := make([]calculationChainExpected, 0)
	for _, value := range values {
		if value.Key == key {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func calculationChainPercentile(samples []float64, percent int) float64 {
	ordered := append([]float64(nil), samples...)
	sort.Float64s(ordered)
	return ordered[(len(ordered)*percent+99)/100-1]
}

func TestCalculationChainFixtureOracle(t *testing.T) {
	data := newCalculationChainData(201)
	if !reflect.DeepEqual(data, newCalculationChainData(201)) || len(data.Main) != 1000 || len(data.Keys) != 67 {
		t.Fatal("fixed seed fixture changed shape or is not deterministic")
	}
	for _, row := range data.Main[:100] {
		if row.Key != "many" {
			t.Fatal("100-row repeated-condition window is missing")
		}
	}
	for _, row := range data.Sources {
		if len(row.Wide) < 1024 {
			t.Fatal("wide-text fixture is missing")
		}
	}
	instant := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	seen := map[string]int{}
	for _, row := range calculationChainOracle(data, instant) {
		seen[row.Key] = row.Matches
		if row.Clock != 0 || (row.Key == "single" && (row.Lookup != 7 || row.Total != 14)) {
			t.Fatalf("oracle scalar = %+v", row)
		}
	}
	if seen["empty"] != 0 || seen["single"] != 1 || seen["many"] != 200 {
		t.Fatalf("match boundaries = %v", seen)
	}
	// Hand-calculated data pins predicate, sum, downstream transform, clock,
	// full-result filtering and stable tie sorting without reusing generation.
	hand := calculationChainData{
		Sources: []calculationChainSource{
			{Key: "a", Amount: 3, Enabled: true, Day: "2026-09-10"},
			{Key: "a", Amount: 7, Enabled: true, Day: "2026-09-20"},
			{Key: "a", Amount: 100, Enabled: false, Day: "2026-09-20"},
			{Key: "a", Amount: 100, Enabled: true, Day: "2026-09-09"},
		},
		Main: []calculationChainMain{{ID: "b", Key: "a"}, {ID: "a", Key: "a"}, {ID: "c", Key: "none"}},
	}
	got := calculationChainOrdered(calculationChainOracle(hand, instant.AddDate(0, 0, 1)), 1)
	want := []calculationChainExpected{
		{ID: "a", Key: "a", Matches: 2, Lookup: 10, Total: 20, Clock: 1},
		{ID: "b", Key: "a", Matches: 2, Lookup: 10, Total: 20, Clock: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("oracle = %+v, want %+v", got, want)
	}
	if calculationChainPercentile([]float64{4, 1, 3, 2}, 50) != 2 ||
		calculationChainPercentile([]float64{4, 1, 3, 2}, 95) != 4 {
		t.Fatal("percentiles must use nearest rank")
	}
}

type calculationChainFixture struct {
	app                                               core.App
	compiler                                          *formula.Compiler
	kernel                                            *mutation.Kernel
	data                                              calculationChainData
	source, summary, main                             v2IntegrationTable
	sourceKey, amount, enabled, day, wide, sourceNote v2.FieldDefinition
	summaryKey, summaryLookup, doubled                v2.FieldDefinition
	mainKey, mainNote, directLookup, total, clock     v2.FieldDefinition
}

func newCalculationChainFixture(t *testing.T, ctx context.Context, data calculationChainData) *calculationChainFixture {
	t.Helper()
	app := bootstrapApp(t, queryTempDir(t))
	t.Cleanup(func() {
		if app.IsBootstrapped() {
			resetApp(t, app)
		}
	})
	f := &calculationChainFixture{app: app, data: data}
	f.source = createV2IntegrationTable(t, ctx, app, "规模来源", "scale_source")
	f.summary = createV2IntegrationTable(t, ctx, app, "条件汇总", "scale_summary")
	f.main = createV2IntegrationTable(t, ctx, app, "规模台账", "scale_main")
	add := func(table v2IntegrationTable, kind v2.LogicalType, name, key string) v2.FieldDefinition {
		return *createV2IntegrationField(t, ctx, app, table.TableID, fieldDraftForIntegration(t, kind, name), key).Definition
	}
	f.sourceKey = add(f.source, v2.LogicalText, "条件", "scale_source_key")
	f.amount = add(f.source, v2.LogicalNumber, "金额", "scale_amount")
	f.enabled = add(f.source, v2.LogicalBool, "启用", "scale_enabled")
	f.day = add(f.source, v2.LogicalDate, "日期", "scale_day")
	f.wide = add(f.source, v2.LogicalText, "宽文本", "scale_wide")
	f.sourceNote = add(f.source, v2.LogicalText, "无关备注", "scale_source_note")
	f.summaryKey = add(f.summary, v2.LogicalText, "条件", "scale_summary_key")
	f.mainKey = add(f.main, v2.LogicalText, "条件", "scale_main_key")
	f.mainNote = add(f.main, v2.LogicalText, "无关备注", "scale_main_note")
	addLookup := func(table v2IntegrationTable, key v2.FieldDefinition, operation string) v2.FieldDefinition {
		draft := fieldDraftForIntegration(t, v2.LogicalLookup, "条件金额")
		draft.Lookup = &v2.LookupSpec{
			Path: []v2.LookupPathStep{}, TargetFieldID: f.amount.Identity.FieldID, Aggregation: v2.LookupAggregationSum,
			Condition: &v2.LookupCondition{SourceTableID: f.source.TableID, Match: "all", Rules: []v2.LookupConditionRule{
				{SourceFieldID: f.sourceKey.Identity.FieldID, Operator: "eq", Operand: &v2.LookupOperand{Kind: "field", FieldID: key.Identity.FieldID}},
				{SourceFieldID: f.enabled.Identity.FieldID, Operator: "eq", Operand: &v2.LookupOperand{Kind: "constant", Value: true}},
				{SourceFieldID: f.day.Identity.FieldID, Operator: "gte", Operand: &v2.LookupOperand{Kind: "constant", Value: "2026-09-10"}},
			}},
		}
		return *createV2IntegrationField(t, ctx, app, table.TableID, draft, operation).Definition
	}
	f.summaryLookup = addLookup(f.summary, f.summaryKey, "scale_summary_lookup")
	f.directLookup = addLookup(f.main, f.mainKey, "scale_direct_lookup")
	addFormula := func(table v2IntegrationTable, name, key, source string) v2.FieldDefinition {
		draft := fieldDraftForIntegration(t, v2.LogicalFormula, name)
		draft.Formula = &v2.FormulaDraftSpec{Language: "cel-v2", Source: source}
		return *createV2IntegrationFormula(t, ctx, app, table.TableID, draft, key).Definition
	}
	f.doubled = addFormula(f.summary, "二级金额", "scale_double", f.summaryLookup.Identity.PhysicalName+" * 2.0")
	f.total = addFormula(f.main, "三级金额", "scale_total", fmt.Sprintf(
		"SUMIF(TABLE(%q), CurrentValue.%s == %s, CurrentValue.%s)",
		f.summary.TableID, f.summaryKey.Identity.PhysicalName, f.mainKey.Identity.PhysicalName, f.doubled.Identity.PhysicalName))
	f.clock = addFormula(f.main, "日期标记", "scale_clock", `IF(TODAY("UTC") >= DATE(2026, 9, 30), 1.0, 0.0)`)
	// Raw sources are synthetic fixture setup through validated PocketBase Save;
	// dependent rows use the real mutation kernel in dependency order. This does
	// not measure bulk import/audit performance. Later changes must use the
	// kernel and production jobs invalidator.
	f.compiler = formula.NewAppCompilerWithLimits(app, formula.Limits{EvalTimeout: collectionTestEvalTimeout})
	f.kernel = mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(
		computed.New(lookup.NewCalculator(), formula.NewCalculator(f.compiler))))
	// The schema helper uses a fake scheduler. Complete real empty-table jobs
	// before importing rows so QueryPort sees ready formula runtime metadata.
	service := jobs.New(app, f.kernel)
	defer service.Shutdown()
	for _, table := range []v2IntegrationTable{f.summary, f.main} {
		beforeCompilations := f.compiler.PlanCompilationCount()
		definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		started, err := service.StartFormulaBackfill(ctx, table.TableID, definition.Snapshot.SchemaRevision)
		if err != nil || started.Progress.Total != 0 {
			t.Fatalf("initialize empty formula table: job=%+v error=%v", started, err)
		}
		if err := service.Run(ctx, started.JobID); err != nil {
			t.Fatalf("initialize formula runtime: %v", err)
		}
		ready, err := schemaapi.New(app).Describe(ctx, table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		for fieldID, state := range ready.FormulaRuntime {
			if state.Status != "ready" {
				t.Fatalf("formula %s runtime = %s, want ready", fieldID, state.Status)
			}
		}
		calculationChainLog(t, map[string]any{
			"kind": "initialize", "tableId": table.TableID, "rows": 0,
			"planCompilations":      f.compiler.PlanCompilationCount() - beforeCompilations,
			"planCompilationsTotal": f.compiler.PlanCompilationCount(),
		})
	}
	f.seedRawSources(t, ctx)
	f.seed(t, ctx, f.summary, len(data.Keys), func(index int) (string, map[string]any) {
		return fmt.Sprintf("qg%013d", index), map[string]any{f.summaryKey.Identity.PhysicalName: data.Keys[index]}
	})
	f.seed(t, ctx, f.main, len(data.Main), func(index int) (string, map[string]any) {
		row := data.Main[index]
		return row.ID, map[string]any{f.mainKey.Identity.PhysicalName: row.Key, f.mainNote.Identity.PhysicalName: row.Note}
	})
	for table, want := range map[string]int{f.source.PhysicalName: len(data.Sources), f.summary.PhysicalName: len(data.Keys), f.main.PhysicalName: len(data.Main)} {
		if count, err := app.CountRecords(table); err != nil || count != int64(want) {
			t.Fatalf("seeded table %s rows=%d want=%d error=%v", table, count, want, err)
		}
	}
	return f
}

func (f *calculationChainFixture) seedRawSources(t *testing.T, ctx context.Context) {
	t.Helper()
	// Keep schema-default row/table revisions: zero is a valid initial raw
	// fixture state. Do not fabricate audit events, revisions or computed cells.
	fields := []v2.FieldDefinition{f.sourceKey, f.amount, f.enabled, f.day, f.wide, f.sourceNote}
	for start := 0; start < len(f.data.Sources); start += 500 {
		end := min(start+500, len(f.data.Sources))
		beforeCompilations := f.compiler.PlanCompilationCount()
		started := time.Now()
		err := f.app.RunInTransaction(func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId(f.source.PhysicalName)
			if err != nil {
				return err
			}
			for _, row := range f.data.Sources[start:end] {
				if err := ctx.Err(); err != nil {
					return err
				}
				record := core.NewRecord(collection)
				record.Id = row.ID
				values := []any{row.Key, row.Amount, row.Enabled, row.Day, row.Wide, row.Note}
				for index, field := range fields {
					record.Set(field.Identity.PhysicalName, values[index])
					if field.Value.Presence.Mode == v2.PresenceCompanion {
						record.Set(field.Value.Presence.PhysicalName, true)
					}
				}
				if err := app.Save(record); err != nil {
					return err
				}
			}
			return nil
		})
		calculationChainLog(t, map[string]any{
			"kind": "seed", "method": "validated-pocketbase-save-raw-fixture", "tableId": f.source.TableID,
			"offset": start, "requestedRows": end - start, "elapsedMs": float64(time.Since(started).Microseconds()) / 1000,
			"error": calculationChainError(err), "planCompilations": f.compiler.PlanCompilationCount() - beforeCompilations,
			"planCompilationsTotal": f.compiler.PlanCompilationCount(),
		})
		if err != nil {
			t.Fatalf("raw source fixture %d: %v", start, err)
		}
	}
}

func (f *calculationChainFixture) seed(t *testing.T, ctx context.Context, table v2IntegrationTable, count int, values func(int) (string, map[string]any)) {
	t.Helper()
	definition, err := schemaapi.New(f.app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	for start := 0; start < count; start += 500 {
		operations := make([]mutation.Operation, 0, min(500, count-start))
		for index := start; index < min(start+500, count); index++ {
			id, row := values(index)
			operations = append(operations, mutation.Operation{Kind: mutation.OperationInsert, RecordID: &id, Values: row})
		}
		started := time.Now()
		beforeCompilations := f.compiler.PlanCompilationCount()
		receipt, err := f.kernel.Apply(ctx, mutationRequest(table.TableID, definition.Snapshot.SchemaRevision,
			fmt.Sprintf("scale_%s_%d", table.TableID, start), operations...))
		calculationChainLog(t, map[string]any{
			"kind": "seed", "method": "mutation-kernel", "tableId": table.TableID, "offset": start, "requestedRows": len(operations),
			"elapsedMs": float64(time.Since(started).Microseconds()) / 1000, "error": calculationChainError(err),
			"status": receipt.Status, "warnings": receipt.Warnings,
			"planCompilations":      f.compiler.PlanCompilationCount() - beforeCompilations,
			"planCompilationsTotal": f.compiler.PlanCompilationCount(),
		})
		if err != nil || receipt.Status != mutation.StatusApplied || len(receipt.Warnings) != 0 {
			t.Fatalf("seed %s/%d: %v; status=%s warnings=%v", table.TableID, start, err, receipt.Status, receipt.Warnings)
		}
	}
}

type calculationChainQueries struct {
	calls, source, summary, matches atomic.Int64
	compiler                        *formula.Compiler
	mu                              sync.Mutex
	sourceSQL                       map[string]int
}

func observeCalculationChainQueries(t *testing.T, f *calculationChainFixture) *calculationChainQueries {
	t.Helper()
	counts := &calculationChainQueries{compiler: f.compiler, sourceSQL: map[string]int{}}
	seen := map[*dbx.DB]bool{}
	for _, database := range []*dbx.DB{f.app.ConcurrentDB().(*dbx.DB), f.app.NonconcurrentDB().(*dbx.DB)} {
		if seen[database] {
			continue
		}
		seen[database] = true
		previous := database.QueryLogFunc
		database.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
			counts.calls.Add(1)
			if strings.Contains(statement, f.source.PhysicalName) {
				counts.source.Add(1)
				counts.mu.Lock()
				counts.sourceSQL[statement]++
				counts.mu.Unlock()
			}
			if strings.Contains(statement, f.summary.PhysicalName) {
				counts.summary.Add(1)
			}
			if strings.Contains(statement, " AS matches FROM ") {
				counts.matches.Add(1)
			}
			if previous != nil {
				previous(ctx, elapsed, statement, rows, err)
			}
		}
		t.Cleanup(func() { database.QueryLogFunc = previous })
	}
	return counts
}

func (counts *calculationChainQueries) snapshot() [4]int64 {
	return [4]int64{counts.calls.Load(), counts.source.Load(), counts.summary.Load(), counts.matches.Load()}
}

func calculationChainError(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func calculationChainLog(t *testing.T, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("CALCULATION_CHAIN %s", encoded)
}

// Each raw observation is emitted before failing. A failed series has no p50/
// p95 summary: it is not a successful-samples-only latency claim or a retry.
func calculationChainSample(t *testing.T, counts *calculationChainQueries, name, phase string, sample int, run func() (int, error)) (float64, error) {
	t.Helper()
	before := counts.snapshot()
	beforeCompilations := counts.compiler.PlanCompilationCount()
	var memoryBefore, memoryAfter runtime.MemStats
	runtime.ReadMemStats(&memoryBefore)
	started := time.Now()
	returned, err := run()
	elapsed := float64(time.Since(started).Microseconds()) / 1000
	runtime.ReadMemStats(&memoryAfter)
	after := counts.snapshot()
	calculationChainLog(t, map[string]any{
		"kind": "sample", "operation": name, "sample": sample, "phase": phase,
		"elapsedMs": elapsed, "returnedItems": returned, "error": calculationChainError(err),
		"queryCalls": after[0] - before[0], "queriesMentioningSource": after[1] - before[1],
		"queriesMentioningSummary": after[2] - before[2], "lookupMatchQueries": after[3] - before[3],
		"planCompilations": counts.compiler.PlanCompilationCount() - beforeCompilations,
		"heapAllocBefore":  memoryBefore.HeapAlloc, "heapAllocAfter": memoryAfter.HeapAlloc,
		"totalAllocDelta": memoryAfter.TotalAlloc - memoryBefore.TotalAlloc,
	})
	return elapsed, err
}

func measureCalculationChain(t *testing.T, counts *calculationChainQueries, name string, samples int, run func() (int, error)) bool {
	t.Helper()
	var hot []float64
	for sample := range samples {
		phase := "warm"
		if sample == 0 {
			phase = "first-sample"
		}
		elapsed, err := calculationChainSample(t, counts, name, phase, sample, run)
		if err != nil {
			calculationChainLog(t, map[string]any{"kind": "summary", "operation": name, "status": "failed", "attempted": sample + 1, "p50Ms": nil, "p95Ms": nil})
			t.Errorf("%s sample %d: %v", name, sample, err)
			return false
		}
		if sample > 0 {
			hot = append(hot, elapsed)
		}
	}
	if len(hot) != 0 {
		calculationChainLog(t, map[string]any{
			"kind": "summary", "operation": name, "status": "passed", "warmSamples": len(hot),
			"p50Ms": calculationChainPercentile(hot, 50), "p95Ms": calculationChainPercentile(hot, 95),
			"percentileMethod": "nearest-rank", "rawWarmMs": hot,
		})
	}
	return true
}

// Reopen the same synthetic database for each application-cold observation.
// OS file caches and the Go process remain warm. Schema/bootstrap time is
// recorded separately; the timed query includes its oracle comparison.
func measureCalculationChainCold(t *testing.T, ctx context.Context, f *calculationChainFixture, name string, samples int, input query.TableQuery, want []calculationChainExpected) {
	t.Helper()
	var cold []float64
	for sample := range samples {
		elapsed, err := func() (elapsed float64, err error) {
			calculationChainLog(t, map[string]any{"kind": "reopen-start", "operation": name, "sample": sample})
			started := time.Now()
			app := bootstrapApp(t, f.app.DataDir())
			defer func() {
				if closeErr := app.ResetBootstrapState(); closeErr != nil && err == nil {
					err = fmt.Errorf("close application-cold instance: %w", closeErr)
				}
				if app.IsBootstrapped() && err == nil {
					err = fmt.Errorf("application-cold instance remained open")
				}
			}()
			compiler := formula.NewAppCompilerWithLimits(app, formula.Limits{EvalTimeout: collectionTestEvalTimeout})
			if app.Store() == f.app.Store() || compiler == f.compiler || compiler.PlanCompilationCount() != 0 || formula.CompilerFor(app) != compiler {
				return 0, fmt.Errorf("application-cold instance reused compiler/store state")
			}
			source, err := queryschema.New(app.DataDir())
			if err != nil {
				return 0, err
			}
			calculationChainLog(t, map[string]any{
				"kind": "reopen", "operation": name, "sample": sample,
				"bootstrapMs": float64(time.Since(started).Microseconds()) / 1000,
			})
			opened := *f
			opened.app, opened.compiler = app, compiler
			counts := observeCalculationChainQueries(t, &opened)
			return calculationChainSample(t, counts, name, "application-cold", sample, func() (int, error) {
				page, err := query.NewPort(app, source).QueryPage(ctx, f.main.TableID, input)
				if err == nil {
					err = f.checkPage(page, want)
				}
				return len(page.Rows), err
			})
		}()
		if err != nil {
			calculationChainLog(t, map[string]any{"kind": "summary", "operation": name, "phase": "application-cold", "status": "failed", "attempted": sample + 1, "error": err.Error(), "p50Ms": nil, "p95Ms": nil})
			t.Fatalf("%s application-cold sample %d: %v", name, sample, err)
		}
		cold = append(cold, elapsed)
	}
	calculationChainLog(t, map[string]any{
		"kind": "summary", "operation": name, "phase": "application-cold", "status": "passed", "coldSamples": len(cold),
		"p50Ms": calculationChainPercentile(cold, 50), "p95Ms": calculationChainPercentile(cold, 95),
		"percentileMethod": "nearest-rank", "rawColdMs": cold,
	})
}

func (f *calculationChainFixture) checkPage(page query.Page, want []calculationChainExpected) error {
	if page.ComputedPending || page.TotalRows != int64(len(f.data.Main)) || page.FilteredRows != int64(len(want)) {
		return fmt.Errorf("page pending=%v total=%d filtered=%d, want total=%d filtered=%d", page.ComputedPending, page.TotalRows, page.FilteredRows, len(f.data.Main), len(want))
	}
	end := min(page.Offset+page.Limit, len(want))
	if page.Offset > end || len(page.Rows) != end-page.Offset {
		return fmt.Errorf("page offset=%d limit=%d returned=%d, want end=%d", page.Offset, page.Limit, len(page.Rows), end)
	}
	for index, row := range page.Rows {
		expected := want[page.Offset+index]
		if row["id"] != expected.ID || row[f.mainKey.Identity.PhysicalName] != expected.Key {
			return fmt.Errorf("row %d identity/order = %v, want %s/%s", page.Offset+index, row["id"], expected.ID, expected.Key)
		}
		for field, value := range map[string]float64{
			f.directLookup.Identity.PhysicalName: expected.Lookup,
			f.total.Identity.PhysicalName:        expected.Total, f.clock.Identity.PhysicalName: expected.Clock,
		} {
			if actual, ok := row[field].(float64); !ok || actual != value {
				return fmt.Errorf("row %s field %s = %#v, want %v", expected.ID, field, row[field], value)
			}
		}
	}
	return nil
}

func (f *calculationChainFixture) rawQuery() query.TableQuery {
	// The ordinary scalar sort has one equal key here; QueryPort's stable ID
	// tie-break must still match the independently sorted complete result.
	return query.TableQuery{
		Filters: []query.FilterExpression{{Field: f.mainKey.Identity.PhysicalName, Operator: query.OperatorEqual, Value: "many"}},
		Sorts:   []query.SortCondition{{Field: f.mainKey.Identity.PhysicalName, Direction: query.SortAscending}}, Limit: 100,
	}
}

func recordCalculationChainMatchPlan(t *testing.T, ctx context.Context, f *calculationChainFixture, rows []*core.Record) {
	t.Helper()
	definition, err := schemaapi.New(f.app).Describe(ctx, f.main.TableID)
	if err != nil {
		t.Fatal(err)
	}
	// Obtain executable SQL and parameters from the same production preparation
	// and compiler path as lookup; never execute DBX logging SQL.
	condition, err := queryschema.PrepareLookupCondition(ctx, f.app, definition, *f.directLookup.Lookup)
	if err != nil {
		t.Fatal(err)
	}
	filters := condition.Filters(rows[0])
	for _, row := range rows[1:] {
		if !reflect.DeepEqual(condition.Filters(row), filters) {
			t.Fatal("repeated-condition window produced different predicates")
		}
	}
	matchQuery, err := query.CompileMatchBatch(condition.Descriptor, [][]query.FilterExpression{filters}, "", 256)
	if err != nil {
		t.Fatal(err)
	}
	var explain []struct {
		ID      int    `db:"id"`
		Parent  int    `db:"parent"`
		NotUsed int    `db:"notused"`
		Detail  string `db:"detail"`
	}
	if err := f.app.DB().NewQuery("EXPLAIN QUERY PLAN " + matchQuery.SQL).WithContext(ctx).Bind(dbx.Params(matchQuery.Params)).All(&explain); err != nil {
		t.Fatal(err)
	}
	calculationChainLog(t, map[string]any{
		"kind": "match-query-plan", "sql": matchQuery.SQL, "params": matchQuery.Params, "plan": explain,
		"equalConditionRows": len(rows),
		"scope":              "diagnostic outside timed samples, same production predicate compiler and first-page limit; plan strategy is not measured SQLite visited rows; scale samples separately assert one actual match-query callback per 100-condition request",
	})
}

func TestCalculationChainFixtureQuery(t *testing.T) {
	instant := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	ctx := formula.WithEvaluationTime(t.Context(), instant)
	data := newCalculationChainData(201)
	data.Main = []calculationChainMain{
		{ID: "qm0000000000000", Key: "empty"},
		{ID: "qm0000000000001", Key: "single"},
		{ID: "qm0000000000002", Key: "many"},
	}
	f := newCalculationChainFixture(t, ctx, data)
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
	if err := f.checkPage(page, calculationChainOrdered(calculationChainOracle(data, instant), 0)); err != nil {
		t.Fatal(err)
	}
	row, err := f.app.FindRecordById(f.main.PhysicalName, data.Main[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	recordCalculationChainMatchPlan(t, ctx, f, []*core.Record{row})
	if err := f.app.ResetBootstrapState(); err != nil || f.app.IsBootstrapped() {
		t.Fatalf("close seeded app: %v", err)
	}
	measureCalculationChainCold(t, ctx, f, "raw-field-sorted-window", 1, f.rawQuery(), calculationChainMatchingKey(calculationChainOracle(data, instant), "many"))
}

// Explicit preparation/measurement entry, not a default CI qualification pass.
// Run each size in a separate process with VIBETABLE_CALCULATION_SOURCE_ROWS set
// to 10000 or 50000; preserve -v output. Do not run beside builds or other E2E.
// No performance threshold is invented here: #396 freezes measured budgets.
func TestCalculationChainQualification(t *testing.T) {
	rawSize := os.Getenv("VIBETABLE_CALCULATION_SOURCE_ROWS")
	if rawSize == "" {
		t.Skip("explicit scale measurement: set VIBETABLE_CALCULATION_SOURCE_ROWS=10000 or 50000")
	}
	size, err := strconv.Atoi(rawSize)
	if err != nil || (size != 10000 && size != 50000) {
		t.Fatal("VIBETABLE_CALCULATION_SOURCE_ROWS must be exactly 10000 or 50000")
	}
	instant := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	ctx := formula.WithEvaluationTime(t.Context(), instant)
	calculationChainLog(t, map[string]any{
		"kind": "environment", "startedAt": time.Now().UTC().Format(time.RFC3339Nano),
		"sourceRows": size, "summaryRows": 67, "mainRows": 1000, "windowRows": 100, "seed": calculationChainSeed,
		"sourceSeedMethod": "validated PocketBase Save in transactions, raw fields and presence only; schema-default revisions untouched; not bulk-import/audit qualification; summary/main through real mutation kernel",
		"go":               runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"cpuCount": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0),
		"processor": os.Getenv("PROCESSOR_IDENTIFIER"), "evaluationTime": instant,
		"limits": formula.DefaultLimits(), "sqliteExaminedRows": nil, "peakRSSBytes": nil,
		"measurementScope": "first sample per operation after in-process initialization/seed and preceding operations is not cold; application-cold means a new app/compiler/query instance after closing the old app on the same DB, OS/Go runtime remain warm, bootstrap separately recorded; queryCalls counts DBX QueryLogFunc callbacks, not executed/visited SQLite rows; returnedItems counts API results; planCompilations counts actual execution-plan attempts including failures, not CEL authoring/expression compiles; heap endpoints are not peak RSS; initialization/seed/oracle excluded from query time, result validation included; SQLite scans/RSS not observed internally; caller must record Git revision and process RSS with this log",
	})
	data := newCalculationChainData(size)
	expected := calculationChainOracle(data, instant)
	f := newCalculationChainFixture(t, ctx, data)
	counts := observeCalculationChainQueries(t, f)
	querySource, err := queryschema.New(f.app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(f.app, querySource)
	rawExpected := calculationChainMatchingKey(expected, "many")
	if !measureCalculationChain(t, counts, "raw-field-sorted-window", 21, func() (int, error) {
		page, err := port.QueryPage(ctx, f.main.TableID, f.rawQuery())
		if err == nil {
			err = f.checkPage(page, rawExpected)
		}
		return len(page.Rows), err
	}) {
		return
	}
	filtered := calculationChainOrdered(expected, 1)
	input := query.TableQuery{
		Filters: []query.FilterExpression{{Field: f.total.Identity.PhysicalName, Operator: query.OperatorGreaterEq, Value: 1.0}},
		Sorts:   []query.SortCondition{{Field: f.total.Identity.PhysicalName, Direction: query.SortDescending}}, Limit: 100,
	}
	if !measureCalculationChain(t, counts, "filtered-sorted-window", 21, func() (int, error) {
		page, err := port.QueryPage(ctx, f.main.TableID, input)
		if err == nil {
			err = f.checkPage(page, filtered)
		}
		return len(page.Rows), err
	}) {
		return
	}
	// Full-result checks are reusable for future export comparison; this is a
	// QueryPort read, not evidence that CSV/XLSX export itself was exercised.
	for _, minimum := range []float64{0, 1} {
		want := calculationChainOrdered(expected, minimum)
		if !measureCalculationChain(t, counts, fmt.Sprintf("complete-query-min-%g", minimum), 1, func() (int, error) {
			request := input
			request.Filters = []query.FilterExpression{{Field: f.total.Identity.PhysicalName, Operator: query.OperatorGreaterEq, Value: minimum}}
			returned := 0
			for request.Offset < len(want) {
				page, err := port.QueryPage(ctx, f.main.TableID, request)
				returned += len(page.Rows)
				if err != nil {
					return returned, err
				}
				if err := f.checkPage(page, want); err != nil {
					return returned, err
				}
				request.Offset += request.Limit
			}
			return returned, nil
		}) {
			return
		}
	}
	definition, err := schemaapi.New(f.app).Describe(ctx, f.main.TableID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.app.FindRecordsByFilter(f.main.PhysicalName, "", "+id", 100, 0)
	if err != nil || len(rows) != 100 {
		t.Fatalf("load repeated condition rows: count=%d error=%v", len(rows), err)
	}
	calculator := lookup.NewCalculator()
	if !measureCalculationChain(t, counts, "100-repeated-lookup-conditions", 21, func() (int, error) {
		beforeMatches := counts.matches.Load()
		cells, err := calculator.CalculateCellsBatch(ctx, f.app, definition, rows, map[string]bool{f.directLookup.Identity.FieldID: true})
		if err != nil {
			return len(cells), err
		}
		if executed := counts.matches.Load() - beforeMatches; executed != 1 {
			return len(cells), fmt.Errorf("100 repeated conditions executed %d match queries, want 1", executed)
		}
		for _, row := range rows {
			cell := cells[row.Id][f.directLookup.Identity.PhysicalName]
			if cell.Value != expected[0].Lookup || cell.ProvenanceTotal != 200 || !cell.ProvenanceTotalKnown {
				return len(cells), fmt.Errorf("repeated lookup %s: value=%v total=%d known=%v", row.Id, cell.Value, cell.ProvenanceTotal, cell.ProvenanceTotalKnown)
			}
		}
		return len(cells), nil
	}) {
		return
	}
	counts.mu.Lock()
	calculationChainLog(t, map[string]any{
		"kind": "source-sql", "statementsAndCallbacks": counts.sourceSQL,
		"scope": "DBX logging SQL with substituted synthetic values, not executable bound SQL; callback totals across observed warm operations; not SQLite visited rows or EXPLAIN plans",
	})
	counts.mu.Unlock()
	recordCalculationChainMatchPlan(t, ctx, f, rows)
	if err := f.app.ResetBootstrapState(); err != nil || f.app.IsBootstrapped() {
		t.Fatalf("close seeded app before application-cold samples: %v", err)
	}
	measureCalculationChainCold(t, ctx, f, "raw-field-sorted-window", 20, f.rawQuery(), rawExpected)
	measureCalculationChainCold(t, ctx, f, "filtered-sorted-window", 20, input, filtered)
	// Reuse the frozen fixture for the affected invalidation path after the
	// unchanged warm/cold observations. This is one sample, not a p95 claim.
	f.app = bootstrapApp(t, f.app.DataDir())
	f.compiler = formula.NewAppCompilerWithLimits(f.app, formula.Limits{EvalTimeout: collectionTestEvalTimeout})
	service := calculationChainJobs(t, f)
	counts = observeCalculationChainQueries(t, f)
	if !measureCalculationChain(t, counts, "source-edit-fanout-complete-query", 1, func() (int, error) {
		f.data.Sources[1].Amount += 1
		applyCalculationChain(t, ctx, f, "qualification-source-edit", mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &f.data.Sources[1].ID,
			Values: map[string]any{f.amount.Identity.PhysicalName: f.data.Sources[1].Amount},
		})
		definition, err := schemaexecution.Describe(ctx, f.app, f.main.TableID)
		if err != nil {
			return 0, err
		}
		record, err := f.app.FindRecordById(f.main.PhysicalName, f.data.Main[0].ID)
		if err != nil {
			return 0, err
		}
		value, staleErr := relatedcomputation.NewSourceReader().Read(ctx, f.app,
			f.main.TableID, definition.Snapshot.Fields, f.total, record)
		var dependencyErr *formula.Error
		if value != nil || !errors.As(staleErr, &dependencyErr) || dependencyErr.Code != "formula.dependency" {
			return 0, fmt.Errorf("source edit must reject stale total before fanout: value=%v error=%v", value, staleErr)
		}
		drainCalculationChain(t, ctx, f, service)
		querySource, err := queryschema.New(f.app.DataDir())
		if err != nil {
			return 0, err
		}
		want := calculationChainOrdered(calculationChainOracle(f.data, instant), 0)
		request := input
		request.Filters = []query.FilterExpression{{Field: f.total.Identity.PhysicalName, Operator: query.OperatorGreaterEq, Value: 0.0}}
		returned := 0
		for request.Offset < len(want) {
			page, err := query.NewPort(f.app, querySource).QueryPage(ctx, f.main.TableID, request)
			if err != nil {
				return returned, err
			}
			if err := f.checkPage(page, want); err != nil {
				return returned, err
			}
			returned += len(page.Rows)
			request.Offset += request.Limit
		}
		return returned, nil
	}) {
		return
	}
}
