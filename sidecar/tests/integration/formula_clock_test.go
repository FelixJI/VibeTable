package integration_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type clockRecordingCalculator struct {
	inner *formula.Calculator
	calls map[string]int
}

func (calculator *clockRecordingCalculator) Calculate(ctx context.Context, app core.App, table schemaexecution.Table, record *core.Record) (map[string]any, error) {
	values, err := calculator.inner.Calculate(ctx, app, table, record)
	for field := range values {
		calculator.calls[field]++
	}
	return values, err
}

func TestFormulaClockRefreshAcrossMinuteDayAndReopen(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	instant := time.Date(2024, 3, 10, 6, 59, 59, 0, time.UTC)
	ctx := formula.WithEvaluationTime(context.Background(), instant)
	table := createV2IntegrationTable(t, ctx, app, "Clock notes", "clock_table")
	updatedDraft := fieldDraftForIntegration(t, v2.LogicalAutoDate, "Modified")
	updatedDraft.AutoDate = &v2.AutoDateSpec{Role: "updatedAt"}
	modified := createV2IntegrationField(t, ctx, app, table.TableID, updatedDraft, "clock_modified")
	fields := map[string]*v2.FieldDefinition{}
	for index, spec := range []struct{ name, source string }{
		{"Now", "NOW()"}, {"Today", `TODAY("America/New_York")`},
		{"Label", `formatDate({Today}, "yyyy-MM-dd")`}, {"Ordinary", "7"},
	} {
		draft := fieldDraftForIntegration(t, v2.LogicalFormula, spec.name)
		draft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: spec.source}
		created := createV2IntegrationFormula(t, ctx, app, table.TableID, draft, fmt.Sprintf("clock_field_%d", index))
		definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		fields[spec.name] = integrationFieldByID(definition, created.FieldID)
	}
	definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	// 101 rows force two existing backfill batches across one fixed instant.
	for i := 0; i < 101; i++ {
		record := core.NewRecord(collection)
		record.Id = fmt.Sprintf("clockrow%07d", i)
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	calculator := &clockRecordingCalculator{inner: formula.NewCalculator(formula.NewAppCompiler(app)), calls: map[string]int{}}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithClock(func() time.Time { return instant }), mutation.WithFormulaCalculator(computed.New(calculator)))
	service := jobs.New(app, kernel, jobs.WithClock(func() time.Time { return instant }))
	defer service.Shutdown()
	receipts := map[string]bool{}
	service.SetBusinessWriteGate(func(ctx context.Context, kind, identity string, apply func(context.Context) error) error {
		key := kind + ":" + identity
		if receipts[key] {
			return nil
		}
		if err := apply(ctx); err != nil {
			return err
		}
		receipts[key] = true
		return nil
	})
	initial, err := service.StartFormulaBackfill(ctx, table.TableID, definition.Snapshot.SchemaRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Run(ctx, initial.JobID); err != nil {
		t.Fatal(err)
	}
	refresh := func(owner *jobs.Service, at time.Time) []string {
		t.Helper()
		ids, err := owner.RefreshClock(context.Background(), at)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if err := owner.Run(context.Background(), id); err != nil {
				t.Fatal(err)
			}
		}
		return ids
	}
	countRows := func(table string) int {
		t.Helper()
		var count int
		if err := app.DB().NewQuery("SELECT COUNT(*) FROM `" + table + "`").Row(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	auditBefore, keysBefore := countRows("vibetable_audit_events"), countRows("vibetable_idempotency_keys")
	original, err := app.FindRecordById(collection, "clockrow0000000")
	if err != nil {
		t.Fatal(err)
	}
	originalUpdated := original.GetString(modified.Definition.Identity.PhysicalName)
	originalRevision := original.GetInt(relatedcomputation.RowRevisionField)
	refresh(service, instant)
	calculator.calls = map[string]int{}
	source, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(app, source)
	read := func(at time.Time) query.Page {
		t.Helper()
		page, err := port.QueryPage(formula.WithEvaluationTime(context.Background(), at), table.TableID, query.TableQuery{Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	assertValue := func(page query.Page, name string, want any) {
		t.Helper()
		if len(page.Rows) != 101 {
			t.Fatalf("rows=%d", len(page.Rows))
		}
		for _, row := range page.Rows {
			if got := row[fields[name].Identity.PhysicalName]; got != want {
				t.Fatalf("%s=%#v want %#v", name, got, want)
			}
		}
	}
	before := read(instant)
	window, err := port.OpenCursor(ctx, table.TableID, query.TableQuery{Limit: 1})
	if err != nil || window.NextCursor == nil {
		t.Fatalf("cursor: %#v %v", window, err)
	}
	next := instant.Add(2 * time.Second)
	validation, err := port.ValidateSnapshot(formula.WithEvaluationTime(context.Background(), next), before.Snapshot, nil)
	if err != nil || validation.Valid || validation.Reason != "query_changed" {
		t.Fatalf("clock snapshot remained valid: %#v %v", validation, err)
	}
	if _, err := port.FetchCursor(formula.WithEvaluationTime(context.Background(), next), *window.NextCursor); err == nil {
		t.Fatal("cursor crossed clock period without invalidation")
	}

	pending := read(next)
	if !pending.ComputedPending {
		t.Fatal("query hid pending computed state")
	}
	empty, err := port.QueryPage(formula.WithEvaluationTime(context.Background(), next), table.TableID, query.TableQuery{Limit: 1, Filters: []query.FilterExpression{{Field: fields["Now"].Identity.PhysicalName, Operator: query.OperatorEqual, Value: "2024-03-10T07:00:00Z"}}})
	if err != nil || len(empty.Rows) != 0 || !empty.ComputedPending {
		t.Fatalf("filtered empty page lost pending state: %#v %v", empty, err)
	}
	if _, ok := pending.Rows[0][fields["Now"].Identity.PhysicalName].(map[string]any); !ok {
		t.Fatal("expired NOW remained a scalar")
	}
	assertValue(pending, "Today", "2024-03-10T00:00:00Z")
	if len(refresh(service, next)) != 1 {
		t.Fatal("minute did not create exactly one clock job")
	}
	assertValue(read(next), "Now", "2024-03-10T07:00:00Z")
	if read(next).ComputedPending {
		t.Fatal("fresh page reported pending")
	}
	if calculator.calls[fields["Ordinary"].Identity.PhysicalName] != 0 || calculator.calls[fields["Today"].Identity.PhysicalName] != 0 {
		t.Fatalf("unchanged cells recalculated: %#v", calculator.calls)
	}
	if len(refresh(service, next)) != 0 {
		t.Fatal("same period enqueued twice")
	}
	midnight := time.Date(2024, 3, 11, 4, 0, 0, 0, time.UTC)
	calculator.calls = map[string]int{}
	if len(refresh(service, midnight)) != 1 {
		t.Fatal("local midnight did not refresh")
	}
	assertValue(read(midnight), "Today", "2024-03-11T00:00:00Z")
	assertValue(read(midnight), "Label", "2024-03-11")
	if calculator.calls[fields["Ordinary"].Identity.PhysicalName] != 0 {
		t.Fatal("ordinary formula recalculated at midnight")
	}
	// Returning to a formerly processed date must not replay its old enqueue
	// receipt and leave the newer stored values permanently stale.
	if len(refresh(service, instant)) != 1 {
		t.Fatal("backwards clock did not enqueue")
	}
	assertValue(read(instant), "Label", "2024-03-10")
	service.Shutdown()
	if _, err := service.RefreshClock(context.Background(), midnight.Add(time.Minute)); err == nil {
		t.Fatal("closed workspace accepted clock work")
	}
	reopened := jobs.New(app, kernel)
	defer reopened.Shutdown()
	future := midnight.Add(48 * time.Hour)
	refresh(reopened, future)
	assertValue(read(future), "Label", "2024-03-13")
	// A filtered/sorted query uses the same descriptor time as row projection.
	filtered, err := port.QueryPage(formula.WithEvaluationTime(context.Background(), future), table.TableID, query.TableQuery{Limit: 200, Filters: []query.FilterExpression{{Field: fields["Label"].Identity.PhysicalName, Operator: query.OperatorEqual, Value: "2024-03-13"}}, Sorts: []query.SortCondition{{Field: fields["Now"].Identity.PhysicalName, Direction: query.SortAscending}}})
	if err != nil || len(filtered.Rows) != 101 {
		t.Fatalf("filter/sort split the clock: rows=%d err=%v", len(filtered.Rows), err)
	}
	record, err := app.FindRecordById(collection, "clockrow0000000")
	if err != nil {
		t.Fatal(err)
	}
	if countRows("vibetable_audit_events") != auditBefore || countRows("vibetable_idempotency_keys") != keysBefore {
		t.Fatal("clock cache refresh created user history or mutation idempotency rows")
	}
	if record.GetString(modified.Definition.Identity.PhysicalName) != originalUpdated || record.GetInt(relatedcomputation.RowRevisionField) != originalRevision {
		t.Fatal("clock cache refresh changed business row version or auto-date")
	}
	var clockJobs int
	if err := app.DB().NewQuery("SELECT COUNT(*) FROM vibetable_jobs WHERE json_extract(cursor_json,'$.clockInstant') IS NOT NULL").Row(&clockJobs); err != nil {
		t.Fatal(err)
	}
	if clockJobs > 3 {
		t.Fatalf("completed clock backfills accumulated: %d", clockJobs)
	}
	envelope, ok := relatedcomputation.Decode(record.GetRaw(fields["Ordinary"].Identity.PhysicalName))
	if !ok || envelope.State != "ready" {
		t.Fatal("ordinary envelope was lost")
	}
}

func TestFormulaClockLifecycleWakeAndShutdown(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	var wall, reads atomic.Int64
	wall.Store(time.Date(2024, 12, 31, 23, 59, 0, 0, time.UTC).Unix())
	now := func() time.Time { reads.Add(1); return time.Unix(wall.Load(), 0).UTC() }
	ctx := formula.WithEvaluationTime(context.Background(), now())
	table := createV2IntegrationTable(t, ctx, app, "Wake clock", "wake_table")
	draft := fieldDraftForIntegration(t, v2.LogicalFormula, "Now")
	draft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "NOW()"}
	created := createV2IntegrationFormula(t, ctx, app, table.TableID, draft, "wake_formula")
	definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	field := integrationFieldByID(definition, created.FieldID)
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Id = "wakeclock000001"
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithClock(now), mutation.WithFormulaCalculator(computed.New(formula.NewCalculator(formula.NewAppCompiler(app)))))
	service := jobs.New(app, kernel, jobs.WithClock(now))
	defer service.Shutdown()
	initial, err := service.StartFormulaBackfill(ctx, table.TableID, definition.Snapshot.SchemaRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Run(ctx, initial.JobID); err != nil {
		t.Fatal(err)
	}
	if err := service.StartClockUpdates(context.Background()); err != nil {
		t.Fatal(err)
	}
	// No real overnight wait: moving only the injectable wall clock models a
	// machine returning from sleep; the owned wake ticker must notice it.
	wall.Add(2 * 60 * 60)
	deadline := time.Now().Add(4 * time.Second)
	for {
		current, err := app.FindRecordById(collection, record.Id)
		if err != nil {
			t.Fatal(err)
		}
		if relatedcomputation.ProjectStored(current.GetRaw(field.Identity.PhysicalName)) == "2025-01-01T01:59:00Z" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wake clock did not calibrate within its next ticker")
		}
		time.Sleep(20 * time.Millisecond)
	}
	service.Shutdown()
	stoppedReads := reads.Load()
	wall.Add(60)
	time.Sleep(1100 * time.Millisecond)
	if reads.Load() != stoppedReads {
		t.Fatal("clock goroutine survived shutdown")
	}
	current, err := app.FindRecordById(collection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := relatedcomputation.ProjectStored(current.GetRaw(field.Identity.PhysicalName)); got != "2025-01-01T01:59:00Z" {
		t.Fatalf("closed workspace was overwritten: %v", got)
	}
}
