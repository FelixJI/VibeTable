package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

// Failed clock backfills must not accumulate without bound (review #394 F5).
// RefreshClock keeps only the two newest terminal clock jobs of a table plus
// its in-flight job, while ordinary backfills and queued/running clock jobs
// are never removed, and every new clock period records a fresh attempt so a
// transient failure recovers instead of freezing the table.
func TestFormulaClockRetention(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	instant := time.Date(2024, 5, 1, 8, 0, 0, 0, time.UTC)
	ctx := formula.WithEvaluationTime(context.Background(), instant)
	table := createV2IntegrationTable(t, ctx, app, "Clock retention", "clock_retention_table")
	draft := fieldDraftForIntegration(t, v2.LogicalFormula, "Now")
	draft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "NOW()"}
	created := createV2IntegrationFormula(t, ctx, app, table.TableID, draft, "clock_retention_formula")
	definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	field := integrationFieldByID(definition, created.FieldID)
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"clockretained01", "clockretained02", "clockretained03"}
	for _, id := range ids {
		record := core.NewRecord(collection)
		record.Id = id
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	calculator := computed.New(formula.NewCalculator(formula.NewAppCompiler(app)))
	service := jobs.New(app, mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(calculator)))
	defer service.Shutdown()
	initial, err := service.StartFormulaBackfill(ctx, table.TableID, definition.Snapshot.SchemaRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Run(ctx, initial.JobID); err != nil {
		t.Fatal(err)
	}
	countClockJobs := func() int {
		t.Helper()
		var count int
		if err := app.DB().NewQuery(
			"SELECT COUNT(*) FROM vibetable_jobs WHERE job_type='formula_backfill' AND source_table_id={:table}" +
				" AND json_extract(cursor_json,'$.clockInstant') IS NOT NULL",
		).Bind(dbx.Params{"table": table.TableID}).Row(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	jobExists := func(jobID string) bool {
		t.Helper()
		var count int
		if err := app.DB().NewQuery("SELECT COUNT(*) FROM vibetable_jobs WHERE id={:id}").
			Bind(dbx.Params{"id": jobID}).Row(&count); err != nil {
			t.Fatal(err)
		}
		return count == 1
	}
	ordinaryJobs := func() int {
		t.Helper()
		var count int
		if err := app.DB().NewQuery(
			"SELECT COUNT(*) FROM vibetable_jobs WHERE job_type='formula_backfill' AND source_table_id={:table}" +
				" AND json_extract(cursor_json,'$.clockInstant') IS NULL AND state='complete'",
		).Bind(dbx.Params{"table": table.TableID}).Row(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	refresh := func(at time.Time) []string {
		t.Helper()
		jobIDs, err := service.RefreshClock(context.Background(), at)
		if err != nil {
			t.Fatal(err)
		}
		return jobIDs
	}
	stateOf := func(jobID string) jobs.Snapshot {
		t.Helper()
		snapshot, err := service.Get(context.Background(), jobID)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}

	// Six failed periods: every minute records a fresh new-period attempt (a
	// failed job never freezes the table) with an inspectable failed state.
	service.SetKernel(mutation.New(app, mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(clockFaultCalculator{inner: calculator, failID: ids[0]})))
	failed := []string{}
	for minute := 1; minute <= 6; minute++ {
		jobIDs := refresh(instant.Add(time.Duration(minute) * time.Minute))
		if len(jobIDs) != 1 {
			t.Fatalf("minute %d recorded %d new-period attempts, want 1", minute, len(jobIDs))
		}
		if err := service.Run(context.Background(), jobIDs[0]); err == nil {
			t.Fatalf("minute %d clock job unexpectedly succeeded", minute)
		}
		snapshot := stateOf(jobIDs[0])
		if snapshot.State != "failed" || snapshot.Error == nil || snapshot.Error.Code == "" {
			t.Fatalf("minute %d terminal snapshot = %s, error %#v", minute, snapshot.State, snapshot.Error)
		}
		failed = append(failed, jobIDs[0])
		if count := countClockJobs(); count > 3 {
			t.Fatalf("minute %d: failed clock jobs accumulated to %d rows", minute, count)
		}
	}
	if count := countClockJobs(); count != 3 {
		t.Fatalf("retained terminal clock jobs = %d, want 3", count)
	}
	for _, jobID := range failed[:3] {
		if jobExists(jobID) {
			t.Fatalf("stale failed clock job %s was retained", jobID)
		}
	}
	for _, jobID := range failed[3:] {
		if !jobExists(jobID) {
			t.Fatalf("newest failed clock job %s was pruned", jobID)
		}
	}
	if count := ordinaryJobs(); count != 1 {
		t.Fatalf("ordinary completed backfill count = %d, want 1", count)
	}

	// A transient failure recovers in the next period with the healthy kernel.
	service.SetKernel(mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(calculator)))
	recovered := refresh(instant.Add(7 * time.Minute))
	if len(recovered) != 1 {
		t.Fatalf("recovery period enqueued %d jobs, want 1", len(recovered))
	}
	if err := service.Run(context.Background(), recovered[0]); err != nil {
		t.Fatal(err)
	}
	if snapshot := stateOf(recovered[0]); snapshot.State != "complete" {
		t.Fatalf("recovered clock job state = %s", snapshot.State)
	}
	for _, id := range ids {
		record, err := app.FindRecordById(collection, id)
		if err != nil {
			t.Fatal(err)
		}
		if got := relatedcomputation.ProjectStored(record.GetRaw(field.Identity.PhysicalName)); got != "2024-05-01T08:07:00Z" {
			t.Fatalf("recovered NOW value = %v", got)
		}
	}

	// A queued clock job is neither deleted nor duplicated by later refreshes.
	queued := refresh(instant.Add(8 * time.Minute))
	if len(queued) != 1 {
		t.Fatalf("queue period enqueued %d jobs, want 1", len(queued))
	}
	if again := refresh(instant.Add(9 * time.Minute)); len(again) != 1 || again[0] != queued[0] {
		t.Fatalf("queued clock job was not reused: %v vs %v", again, queued)
	}
	if count := countClockJobs(); count != 3 {
		t.Fatalf("queued clock job changed retained rows: %d", count)
	}

	// A cancelled terminal clock job is retained by the same two-job bound.
	if _, err := service.Cancel(context.Background(), queued[0]); err != nil {
		t.Fatal(err)
	}
	if snapshot := stateOf(queued[0]); snapshot.State != "cancelled" {
		t.Fatalf("cancelled clock job state = %s", snapshot.State)
	}
	next := refresh(instant.Add(10 * time.Minute))
	if len(next) != 1 || next[0] == queued[0] {
		t.Fatalf("cancelled clock job blocked the next period: %v", next)
	}
	if err := service.Run(context.Background(), next[0]); err != nil {
		t.Fatal(err)
	}
	if !jobExists(queued[0]) {
		t.Fatal("cancelled terminal clock job was pruned within its retention window")
	}
	if count := countClockJobs(); count != 3 {
		t.Fatalf("final retained clock jobs = %d, want 3", count)
	}
	if count := ordinaryJobs(); count != 1 {
		t.Fatalf("ordinary completed backfill was cleaned: %d", count)
	}
	if snapshot := stateOf(initial.JobID); snapshot.State != "complete" {
		t.Fatalf("ordinary backfill job state = %s", snapshot.State)
	}
}
