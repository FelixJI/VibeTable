package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestRelationLifecycleScaleFixture(t *testing.T) {
	qualifyRelationLifecycleScale(t, smallCalculationChainData())
}

// Reuse #396's frozen data and sizes; this measures one lifecycle observation,
// not a percentile or a replacement for the five existing query budgets.
func TestRelationLifecycleScaleQualification(t *testing.T) {
	raw := os.Getenv("VIBETABLE_CALCULATION_SOURCE_ROWS")
	if raw == "" {
		t.Skip("explicit scale measurement: set VIBETABLE_CALCULATION_SOURCE_ROWS=10000 or 50000")
	}
	size, err := strconv.Atoi(raw)
	if err != nil || (size != 10000 && size != 50000) {
		t.Fatal("VIBETABLE_CALCULATION_SOURCE_ROWS must be exactly 10000 or 50000")
	}
	qualifyRelationLifecycleScale(t, newCalculationChainData(size))
}

func qualifyRelationLifecycleScale(t *testing.T, data calculationChainData) {
	t.Helper()
	instant := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	ctx := formula.WithEvaluationTime(t.Context(), instant)
	f := newCalculationChainFixture(t, ctx, data)
	service := calculationChainJobs(t, f)
	link := createV2IntegrationRelation(t, ctx, f.app,
		f.main.TableID, f.mainKey.Identity.FieldID, f.summary.TableID, f.summaryKey.Identity.FieldID,
		"关联汇总", "关联台账", "one", "scale_relation_pair")
	catalog := fieldchange.NewCatalog(f.app)
	store := fieldchange.NewPocketBasePlanStore(f.app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(f.app, store,
		fieldchange.WithFormulaBackfillScheduler(calculationChainManualDispatch{service}))
	draft := fieldDraftForIntegration(t, v2.LogicalLookup, "关联二级金额")
	draft.Lookup = &v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: link.FieldID}},
		TargetFieldID: f.doubled.Identity.FieldID, Aggregation: v2.LookupAggregationSum,
	}
	linked := applyCreatedField(t, ctx, catalog, planner, executor, f.main.TableID,
		draft, v2.Actor{ID: "local-user", Kind: "user"}, "scale_relation_lookup")
	drainCalculationChain(t, ctx, f, service)
	source, err := queryschema.New(f.app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(f.app, source)
	counts := observeCalculationChainQueries(t, f)
	bindings := map[string]string{}
	summaryID := func(index int) string { return fmt.Sprintf("qg%013d", index) }
	// Separate scalar oracle: sum raw qualifying rows by key, then double.
	sums := func() map[string]float64 {
		result := map[string]float64{}
		for _, row := range f.data.Sources {
			if row.Enabled && row.Day >= "2026-09-10" {
				result[row.Key] += row.Amount
			}
		}
		return result
	}
	check := func() (int, error) {
		want := calculationChainOrdered(calculationChainOracle(f.data, instant), 0)
		summarySums := sums()
		targetValues := map[string]float64{}
		for index, key := range f.data.Keys {
			targetValues[summaryID(index)] = summarySums[key] * 2
		}
		returned := 0
		request := query.TableQuery{
			Sorts: []query.SortCondition{{Field: f.total.Identity.PhysicalName, Direction: query.SortDescending}}, Limit: 100,
		}
		for request.Offset < len(want) {
			page, err := port.QueryPage(ctx, f.main.TableID, request)
			if err != nil {
				return returned, err
			}
			if err := f.checkPage(page, want); err != nil {
				return returned, err
			}
			for _, row := range page.Rows {
				id := row["id"].(string)
				expected := targetValues[bindings[id]]
				if actual, ok := row[linked.Definition.Identity.PhysicalName].(float64); !ok || actual != expected {
					return returned, fmt.Errorf("path lookup %s = %#v, want %v", id, row[linked.Definition.Identity.PhysicalName], expected)
				}
			}
			returned += len(page.Rows)
			request.Offset += request.Limit
		}
		definition, err := schemaexecution.Describe(ctx, f.app, f.summary.TableID)
		if err != nil {
			return returned, err
		}
		page, err := port.QueryPage(ctx, f.summary.TableID, query.TableQuery{Limit: 100})
		if err != nil {
			return returned, err
		}
		if page.ComputedPending || len(page.Rows) != len(f.data.Keys) || page.TotalRows != int64(len(f.data.Keys)) {
			return returned, fmt.Errorf("summary count/pending mismatch: %+v", page)
		}
		seen := map[string]bool{}
		for _, row := range page.Rows {
			id, ok := row["id"].(string)
			doubled, found := targetValues[id]
			if !ok || !found || seen[id] {
				return returned, fmt.Errorf("unexpected summary identity %#v", row["id"])
			}
			seen[id] = true
			record, err := f.app.FindRecordById(f.summary.PhysicalName, id)
			if err != nil {
				return returned, err
			}
			for _, field := range []v2.FieldDefinition{f.summaryLookup, f.doubled} {
				expected := doubled
				if field.Identity.FieldID == f.summaryLookup.Identity.FieldID {
					expected /= 2
				}
				name := field.Identity.PhysicalName
				if actual, ok := row[name].(float64); !ok || actual != expected {
					return returned, fmt.Errorf("summary %s/%s = %#v want %v", id, name, row[name], expected)
				}
				envelope, readable := relatedcomputation.Decode(record.GetRaw(name))
				expectation, err := relatedcomputation.ExpectationFor(ctx, f.app, f.summary.TableID,
					definition.Snapshot.Fields, field.Identity.FieldID, int64(record.GetInt(relatedcomputation.RowRevisionField)))
				if err != nil || !readable || !envelope.Fresh(expectation) {
					return returned, fmt.Errorf("summary %s/%s unreadable/stale envelope: readable=%v error=%v", id, name, readable, err)
				}
			}
		}
		return returned, nil
	}
	measure := func(stage string, run func() (int, error)) {
		t.Helper()
		if !measureCalculationChain(t, counts, stage, 1, run) {
			t.FailNow()
		}
		calculationChainLog(t, map[string]any{
			"kind": "relation-lifecycle-stage", "stage": stage, "sourceRows": len(f.data.Sources),
			"summaryRows": len(f.data.Keys), "mainRows": len(f.data.Main), "samples": 1,
			"branches": []string{"reciprocal WrapValues", "relationPath AllRecords"},
		})
	}
	measure("relation-path-baseline-complete-query", check)
	bind := func(stage string, mainIndex, targetIndex int) {
		t.Helper()
		measure("relation-pair-"+stage+"-complete-query", func() (int, error) {
			id := f.data.Main[mainIndex].ID
			var target any
			if targetIndex >= 0 {
				target = summaryID(targetIndex)
				bindings[id] = target.(string)
			} else {
				delete(bindings, id)
			}
			revisions, err := catalog.Revisions(ctx, f.main.TableID)
			if err != nil {
				return 0, err
			}
			receipt, err := f.kernel.Apply(ctx, mutationRequest(f.main.TableID, revisions.Schema, "scale-pair-"+stage,
				mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &id,
					Values: map[string]any{link.Definition.Identity.PhysicalName: target}}))
			if err != nil || receipt.Status != mutation.StatusApplied || len(receipt.Warnings) != 0 {
				return 0, fmt.Errorf("pair mutation: receipt=%+v error=%v", receipt, err)
			}
			drainCalculationChain(t, ctx, f, service)
			return check()
		})
	}
	bind("bind-many", 0, 2)
	bind("bind-other", 1, 1)
	bind("unbind", 0, -1)
	bind("rebind", 0, 2)
	measure("relation-path-source-edit-fanout-complete-query", func() (int, error) {
		previousJobs, err := f.app.FindAllRecords("vibetable_jobs")
		if err != nil {
			return 0, err
		}
		oldJobs := map[string]bool{}
		for _, job := range previousJobs {
			oldJobs[job.Id] = true
		}
		reached := make(chan struct{}, 1)
		finished := make(chan error, 1)
		startedRun, joined := false, false
		release := make(chan struct{})
		released := false
		defer func() {
			if !released {
				close(release)
			}
			if startedRun && !joined {
				<-finished
			}
		}()
		service.SetBusinessWriteGate(func(ctx context.Context, kind, identity string, apply func(context.Context) error) error {
			if kind == "formula.fanout.batch" {
				select {
				case reached <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return apply(ctx)
		})
		f.data.Sources[1].Amount += 1
		applyCalculationChain(t, ctx, f, "scale-relation-source-edit", mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &f.data.Sources[1].ID,
			Values: map[string]any{f.amount.Identity.PhysicalName: f.data.Sources[1].Amount},
		})
		queued, err := f.app.FindRecordsByFilter("vibetable_jobs", "state='queued'", "+id", 1, 0)
		if err != nil || len(queued) != 1 {
			return 0, fmt.Errorf("missing fanout job: %v", err)
		}
		startedRun = true
		go func() { finished <- service.Run(ctx, queued[0].Id) }()
		select {
		case <-reached:
		case <-time.After(10 * time.Second):
			return 0, fmt.Errorf("fanout never reached installed gate")
		}
		// A raw sort returns updating envelopes rather than evaluating stale sort keys.
		page, err := port.QueryPage(ctx, f.main.TableID, query.TableQuery{Limit: 100})
		if err != nil {
			return 0, err
		}
		for _, index := range []int{0, 1, 2} {
			found := false
			for _, row := range page.Rows {
				if row["id"] != f.data.Main[index].ID {
					continue
				}
				envelope, ok := row[linked.Definition.Identity.PhysicalName].(map[string]any)
				if !ok || envelope["state"] != "updating" {
					return 0, fmt.Errorf("main %s must be stale before drain: %#v", row["id"], row[linked.Definition.Identity.PhysicalName])
				}
				found = true
			}
			if !found {
				return 0, fmt.Errorf("missing stale witness %s", f.data.Main[index].ID)
			}
		}
		close(release)
		released = true
		runErr := <-finished
		joined = true
		if err := runErr; err != nil {
			return 0, err
		}
		drainCalculationChain(t, ctx, f, service)
		allJobs, err := f.app.FindAllRecords("vibetable_jobs")
		if err != nil {
			return 0, err
		}
		pathJobs := 0
		for _, job := range allJobs {
			if oldJobs[job.Id] {
				continue
			}
			var cursor struct {
				AllRecords      bool                  `json:"allRecords"`
				TableID         string                `json:"tableId"`
				FormulaFieldIDs []string              `json:"formulaFieldIds"`
				Paths           [][]v2.LookupPathStep `json:"paths"`
			}
			if err := json.Unmarshal([]byte(job.GetString("cursor_json")), &cursor); err != nil {
				return 0, err
			}
			if cursor.TableID != f.main.TableID || len(cursor.Paths) == 0 {
				continue
			}
			includesLookup := false
			for _, id := range cursor.FormulaFieldIDs {
				if id == linked.FieldID {
					includesLookup = true
				}
			}
			if !includesLookup {
				continue
			}
			snapshot, err := service.Get(ctx, job.Id)
			if err != nil || !cursor.AllRecords || snapshot.State != "complete" || snapshot.Progress.Completed != len(f.data.Main) {
				return 0, fmt.Errorf("path fanout did not process full main table: cursor=%+v job=%+v error=%v", cursor, snapshot, err)
			}
			pathJobs++
			calculationChainLog(t, map[string]any{"kind": "relation-path-fanout", "jobId": job.Id, "allRecords": cursor.AllRecords, "processedMainRows": snapshot.Progress.Completed, "sourceRows": len(f.data.Sources)})
		}
		if pathJobs == 0 {
			return 0, fmt.Errorf("source edit produced no new relation-path fanout job")
		}
		return check()
	})
}
