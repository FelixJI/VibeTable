package formula

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestPlanCacheLateSchemaRequestDoesNotEvictNewerPlan(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	calls := 0
	cache := newPlanCache(func(definition schemaexecution.Table) (*Plan, *Error) {
		calls++
		return compiler.compileExecutionTable(definition)
	})
	old := formulaTable(formulaField("value_id", "value", integerType, "1"))
	newer := formulaTable(formulaField("value_id", "value", integerType, "2"))
	newer.Snapshot.SchemaRevision = "schema_2"
	first, err := cache.get(newer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.get(old); err != nil {
		t.Fatal(err)
	}
	if plan, err := cache.get(newer); err != nil || plan != first || calls != 2 {
		t.Fatalf("late schema evicted newer plan: plan=%p error=%v calls=%d", plan, err, calls)
	}
}

func TestPlanCacheSharesConcurrentCompilation(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	var calls atomic.Int64
	entered := make(chan struct{}, 32)
	release := make(chan struct{})
	cache := newPlanCache(func(definition schemaexecution.Table) (*Plan, *Error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return compiler.compileExecutionTable(definition)
	})
	definition := formulaTable(formulaField("value_id", "value", integerType, "1"))
	start := make(chan struct{})
	var workers sync.WaitGroup
	plans := make([]*Plan, 32)
	errors := make([]error, len(plans))
	for index := range plans {
		workers.Go(func() {
			<-start
			plans[index], errors[index] = cache.get(definition)
		})
	}
	close(start)
	<-entered
	close(release)
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent requests compiled %d times, want 1", calls.Load())
	}
	for index, plan := range plans {
		if errors[index] != nil || plan == nil || plan != plans[0] {
			t.Fatalf("request %d got plan=%p error=%v", index, plan, errors[index])
		}
	}
}

func TestPlanCacheEvictsLeastRecentlyUsedDefinition(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	var calls int
	cache := newPlanCache(func(definition schemaexecution.Table) (*Plan, *Error) {
		calls++
		return compiler.compileExecutionTable(definition)
	})
	definitions := make([]schemaexecution.Table, planCacheCapacity+1)
	plans := make([]*Plan, len(definitions))
	for index := range definitions {
		definitions[index] = formulaTable(formulaField("value_id", "value", integerType, "1"))
		definitions[index].Snapshot.TableID = fmt.Sprintf("table_%d", index)
	}
	for index := range planCacheCapacity {
		plan, err := cache.get(definitions[index])
		if err != nil {
			t.Fatal(err)
		}
		plans[index] = plan
	}
	if _, err := cache.get(definitions[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.get(definitions[planCacheCapacity]); err != nil {
		t.Fatal(err)
	}
	if plan, err := cache.get(definitions[0]); err != nil || plan != plans[0] {
		t.Fatalf("recently used definition was evicted: plan=%p error=%v", plan, err)
	}
	if plan, err := cache.get(definitions[1]); err != nil || plan == plans[1] {
		t.Fatalf("least recently used definition was retained: plan=%p error=%v", plan, err)
	}
	if calls != planCacheCapacity+2 || len(cache.entries) != planCacheCapacity || cache.lru.Len() != planCacheCapacity {
		t.Fatalf("LRU calls=%d entries=%d order=%d", calls, len(cache.entries), cache.lru.Len())
	}
}

func TestPlanCacheSchemaChangeDoesNotResurrectInflightDefinition(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	var calls atomic.Int64
	entered := make(chan struct{})
	release := make(chan struct{})
	cache := newPlanCache(func(definition schemaexecution.Table) (*Plan, *Error) {
		calls.Add(1)
		if definition.Snapshot.SchemaRevision == "schema_1" {
			close(entered)
			<-release
		}
		return compiler.compileExecutionTable(definition)
	})
	var revision atomic.Value
	revision.Store("schema_1")
	cache.currentRevision = func(string) (string, error) { return revision.Load().(string), nil }
	old := formulaTable(formulaField("value_id", "value", integerType, "1"))
	newer := formulaTable(formulaField("value_id", "value", integerType, "2"))
	newer.Snapshot.SchemaRevision = "schema_2"
	done := make(chan error, 1)
	go func() {
		_, err := cache.get(old)
		done <- err
	}()
	<-entered
	revision.Store("schema_2")
	cache.invalidateTable(old.Snapshot.TableID)
	newPlan, err := cache.get(newer)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if plan, err := cache.get(newer); err != nil || plan != newPlan || calls.Load() != 2 {
		t.Fatalf("new schema cache changed after old compile: plan=%p error=%v calls=%d", plan, err, calls.Load())
	}
	if len(cache.entries) != 1 {
		t.Fatalf("old inflight definition resurrected: %d entries", len(cache.entries))
	}
	for key := range cache.entries {
		if key.revision != newer.Snapshot.SchemaRevision {
			t.Fatalf("retained stale schema %q", key.revision)
		}
	}
}

func TestPlanCacheDoesNotRetainCompileErrors(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	calls := 0
	cache := newPlanCache(func(definition schemaexecution.Table) (*Plan, *Error) {
		calls++
		return compiler.compileExecutionTable(definition)
	})
	invalid := formulaTable(formulaField("value_id", "value", integerType, "1 +"))
	for range 2 {
		if _, err := cache.get(invalid); err == nil {
			t.Fatal("invalid expression was accepted")
		}
	}
	if calls != 2 || len(cache.entries) != 0 || cache.lru.Len() != 0 {
		t.Fatalf("compile error retained: calls=%d entries=%d", calls, len(cache.entries))
	}
}

func TestPlanCacheEvictedInflightEntryStaysEvicted(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	var calls atomic.Int64
	entered := make(chan struct{})
	release := make(chan struct{})
	cache := newPlanCache(func(definition schemaexecution.Table) (*Plan, *Error) {
		calls.Add(1)
		if definition.Snapshot.TableID == "pending_table" {
			close(entered)
			<-release
		}
		return compiler.compileExecutionTable(definition)
	})
	pending := formulaTable(formulaField("value_id", "value", integerType, "1"))
	pending.Snapshot.TableID = "pending_table"
	done := make(chan error, 1)
	go func() {
		_, err := cache.get(pending)
		done <- err
	}()
	<-entered
	for index := range planCacheCapacity {
		definition := formulaTable(formulaField("value_id", "value", integerType, "2"))
		definition.Snapshot.TableID = fmt.Sprintf("ready_%d", index)
		if _, err := cache.get(definition); err != nil {
			close(release)
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	if len(cache.entries) != planCacheCapacity || cache.lru.Len() != planCacheCapacity {
		t.Errorf("pending entry exceeded capacity: entries=%d order=%d", len(cache.entries), cache.lru.Len())
	}
	cache.mu.Unlock()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != planCacheCapacity+1 || len(cache.entries) != planCacheCapacity {
		t.Fatalf("completed evicted flight: calls=%d entries=%d", calls.Load(), len(cache.entries))
	}
	for key := range cache.entries {
		if key.tableID == pending.Snapshot.TableID {
			t.Fatal("evicted flight reinserted its entry")
		}
	}
}

func TestPlanCacheSeparatesConcurrentDraftsAndInvalidatesOnlyTheirTable(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	var calls atomic.Int64
	cache := newPlanCache(func(definition schemaexecution.Table) (*Plan, *Error) {
		calls.Add(1)
		return compiler.compileExecutionTable(definition)
	})
	first := formulaTable(formulaField("value_id", "value", integerType, "1"))
	second := formulaTable(formulaField("value_id", "value", integerType, "2"))
	other := formulaTable(formulaField("value_id", "value", integerType, "3"))
	other.Snapshot.TableID = "other_table"
	definitions := []schemaexecution.Table{first, second, other}
	plans := make([]*Plan, len(definitions))
	errors := make([]error, len(definitions))
	var workers sync.WaitGroup
	for index, definition := range definitions {
		workers.Go(func() { plans[index], errors[index] = cache.get(definition) })
	}
	workers.Wait()
	for index, plan := range plans {
		if errors[index] != nil || plan == nil || plan.Formulas[0].Source != fmt.Sprint(index+1) {
			t.Fatalf("draft %d: plan=%#v error=%v", index, plan, errors[index])
		}
	}
	if calls.Load() != 3 || len(cache.entries) != 3 {
		t.Fatalf("draft definitions collided: calls=%d entries=%d", calls.Load(), len(cache.entries))
	}
	newer := formulaTable(formulaField("value_id", "value", integerType, "4"))
	newer.Snapshot.SchemaRevision = "schema_2"
	cache.invalidateTable(first.Snapshot.TableID)
	if _, err := cache.get(newer); err != nil {
		t.Fatal(err)
	}
	if plan, err := cache.get(other); err != nil || plan != plans[2] || calls.Load() != 4 || len(cache.entries) != 2 {
		t.Fatalf("schema change invalidated another table or retained drafts: calls=%d entries=%d error=%v", calls.Load(), len(cache.entries), err)
	}
}

func TestPlanCacheChecksAuthorityOnlyOnMissAndRejectsStaleAdmission(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	cache := newPlanCache(compiler.compileExecutionTable)
	reads := 0
	var authorityErr error
	cache.currentRevision = func(string) (string, error) {
		reads++
		return "schema_2", authorityErr
	}
	old := formulaTable(formulaField("value_id", "value", integerType, "1"))
	newer := formulaTable(formulaField("value_id", "value", integerType, "2"))
	newer.Snapshot.SchemaRevision = "schema_2"
	first, err := cache.get(newer)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := cache.get(old); err != nil {
			t.Fatal(err)
		}
	}
	authorityErr = errors.New("authority unavailable")
	if plan, err := cache.get(newer); err != nil || plan != first || reads != 3 || len(cache.entries) != 1 {
		t.Fatalf("hot plan queried authority or stale plan admitted: plan=%p err=%v reads=%d entries=%d", plan, err, reads, len(cache.entries))
	}
	if _, err := cache.get(old); err != authorityErr || len(cache.entries) != 1 {
		t.Fatalf("authority error lost: err=%v entries=%d", err, len(cache.entries))
	}
}

func TestPlanCacheInvalidationDuringAuthorityReadPreventsAdmission(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	cache := newPlanCache(compiler.compileExecutionTable)
	entered := make(chan struct{})
	release := make(chan struct{})
	reads := 0
	cache.currentRevision = func(string) (string, error) {
		reads++
		if reads == 1 {
			close(entered)
			<-release
		}
		return "schema_1", nil
	}
	definition := formulaTable(formulaField("value_id", "value", integerType, "1"))
	done := make(chan error, 1)
	go func() {
		_, err := cache.get(definition)
		done <- err
	}()
	<-entered
	cache.invalidateTable(definition.Snapshot.TableID)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if reads != 1 || len(cache.entries) != 0 {
		t.Fatalf("obsolete authority read retried or admitted: reads=%d entries=%d", reads, len(cache.entries))
	}
	if _, err := cache.get(definition); err != nil || reads != 2 || len(cache.entries) != 1 {
		t.Fatalf("fresh request could not enter cache: err=%v reads=%d entries=%d", err, reads, len(cache.entries))
	}
}

func TestPlanCacheInvalidatedFlightCannotPopulateReadmittedEntry(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	cache := newPlanCache(func(definition schemaexecution.Table) (*Plan, *Error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return compiler.compileExecutionTable(definition)
	})
	definition := formulaTable(formulaField("value_id", "value", integerType, "1"))
	done := make(chan error, 2)
	request := func() {
		_, err := cache.get(definition)
		done <- err
	}
	go request()
	<-entered
	cache.invalidateTable(definition.Snapshot.TableID)
	go request()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cache.mu.Lock()
		readmitted := len(cache.entries) == 1
		cache.mu.Unlock()
		if readmitted {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("second request did not enter cache")
		}
		runtime.Gosched()
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	// If the second caller joined the old flight, its entry remains pending.
	// Otherwise it already compiled its own plan. Both require a fresh compile.
	if _, err := cache.get(definition); err != nil || calls.Load() != 2 || len(cache.entries) != 1 {
		t.Fatalf("invalidated flight populated a new entry: err=%v calls=%d entries=%d", err, calls.Load(), len(cache.entries))
	}
}

func TestPlanCacheReusesSchemaAfterDataAndRuntimeChanges(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	calls := 0
	cache := newPlanCache(func(definition schemaexecution.Table) (*Plan, *Error) {
		calls++
		return compiler.compileExecutionTable(definition)
	})
	definition := formulaTable(formulaField("value_id", "value", integerType, "1"))
	first, err := cache.get(definition)
	if err != nil {
		t.Fatal(err)
	}
	definition.Snapshot.DataRevision++
	definition.FormulaRuntime["value_id"] = schemaexecution.FormulaRuntime{Version: 2, Status: "updating"}
	if plan, err := cache.get(definition); err != nil || plan != first || calls != 1 {
		t.Fatalf("data-only change recompiled the schema: plan=%p error=%v calls=%d", plan, err, calls)
	}
}

func TestSharedCompilerPreservesAuthoredDraftsAndDiagnosticLocations(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	var plans []*Plan
	for index, name := range []string{"运费", "运费😀"} {
		definition, targets := authorFixture()
		definition.SchemaRevision = "schema_0001"
		definition.Fields[1].DisplayName = name
		display := fmt.Sprintf("{%s} + %d.0", name, index+1)
		authored, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
			DisplaySource: display, DocumentRevision: int64(index + 1),
		})
		if err != nil {
			t.Fatal(err)
		}
		definition.Fields = append(definition.Fields,
			formulaField("result_id", "result", numberType, authored.CanonicalSource))
		plan, err := compiler.CompileV2Table(definition)
		if err != nil {
			t.Fatal(err)
		}
		if cached, err := compiler.CompileV2Table(definition); err != nil || cached != plan {
			t.Fatalf("authored draft did not reuse its plan: %p/%p, %v", plan, cached, err)
		}
		values, err := plan.Evaluate(context.Background(), map[string]any{"f_shipping": 10.0}, nil)
		if err != nil || values["result"] != float64(11+index) {
			t.Fatalf("authored draft used another source: %#v, %v", values, err)
		}
		plans = append(plans, plan)

		invalid, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
			DisplaySource: "{" + name + "} + missing", DocumentRevision: int64(index + 3),
		})
		if err != nil {
			t.Fatal(err)
		}
		definition.Fields[len(definition.Fields)-1] = formulaField(
			"result_id", "result", numberType, invalid.CanonicalSource,
		)
		_, compileErr := compiler.CompileV2Table(definition)
		assertFormulaCode(t, compileErr, "formula.dependency")
		if compileErr.SourceSpan == nil {
			t.Fatal("shared compiler discarded the author diagnostic span")
		}
		position, ok := invalid.SourceMap.DisplayRange(*compileErr.SourceSpan)
		if !ok || position.Start.Line != 0 || position.Start.Character != int64(7+index*2) {
			t.Fatalf("diagnostic reused another author's UTF-16 position: %#v", position)
		}
	}
	if plans[0] == plans[1] {
		t.Fatal("different authored definitions shared one plan")
	}
}
