package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

type firstBatchPauseKernel struct {
	inner   jobs.MutationKernel
	paused  chan struct{}
	release chan struct{}
	once    sync.Once
}

type recordingDataPublisher struct {
	mu    sync.Mutex
	calls int
}

type cancelDuringApplyKernel struct {
	entered chan struct{}
	once    sync.Once
}

type businessDeadlineKernel struct{}

func (businessDeadlineKernel) Apply(
	context.Context,
	mutation.Request,
) (mutation.Receipt, error) {
	return mutation.Receipt{}, context.DeadlineExceeded
}

type crashAfterTerminalPersistPublisher struct {
	hub *realtime.Hub
}

func (publisher crashAfterTerminalPersistPublisher) PersistTaskChanged(
	ctx context.Context,
	app core.App,
	snapshot jobs.Snapshot,
) error {
	return publisher.hub.PersistTaskChanged(ctx, app, snapshot)
}

func (crashAfterTerminalPersistPublisher) PublishTaskChanged(
	context.Context,
	jobs.Snapshot,
) error {
	return errors.New("simulated crash after terminal commit")
}

func (kernel *cancelDuringApplyKernel) Apply(
	ctx context.Context,
	_ mutation.Request,
) (mutation.Receipt, error) {
	kernel.once.Do(func() { close(kernel.entered) })
	<-ctx.Done()
	return mutation.Receipt{}, ctx.Err()
}

func (publisher *recordingDataPublisher) Publish(
	_ context.Context,
	_ mutation.DataChangedEvent,
) error {
	publisher.mu.Lock()
	publisher.calls++
	publisher.mu.Unlock()
	return nil
}

func (publisher *recordingDataPublisher) callCount() int {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return publisher.calls
}

func (kernel *firstBatchPauseKernel) Apply(
	ctx context.Context,
	request mutation.Request,
) (mutation.Receipt, error) {
	receipt, err := kernel.inner.Apply(ctx, request)
	if err != nil {
		return receipt, err
	}
	kernel.once.Do(func() {
		close(kernel.paused)
		<-kernel.release
	})
	return receipt, nil
}

func TestFormulaBackfillJobRecalculatesAndMarksMetadataReady(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("job_notes", "job_notes", []schema.FieldDefinition{
			field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText),
			formulaField(
				"computed_id", "computed",
				schema.DataTypeShortText, "upper(title)",
			),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId("job_notes")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		record := core.NewRecord(collection)
		record.Id = fmt.Sprintf("jobrecord%06d", index+1)
		record.Set("title", fmt.Sprintf("note-%d", index+1))
		record.Set("computed", "STALE")
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(
			formula.NewCalculator(
				formula.NewCompiler(formula.DefaultLimits()),
			),
		),
	)
	hub := realtime.New(app)
	service := jobs.New(app, kernel, jobs.WithTaskPublisher(hub))
	defer service.Shutdown()
	batchGateCalls := 0
	service.SetBusinessWriteGate(func(
		ctx context.Context,
		kind string,
		identity string,
		apply func(context.Context) error,
	) error {
		batchGateCalls++
		if kind != "formula.backfill.batch" ||
			!strings.HasPrefix(identity, "job_") {
			t.Fatalf("background write gate = %q %q", kind, identity)
		}
		return apply(ctx)
	})
	started, err := service.StartFormulaBackfill(
		ctx, "job_notes", definition.SchemaRevision,
	)
	if err != nil || started.State != "queued" ||
		started.Progress.Total != 3 {
		t.Fatalf("started job = %#v, err=%v", started, err)
	}
	if err := service.Run(ctx, started.JobID); err != nil {
		t.Fatalf("run formula backfill: %#v", err)
	}
	if batchGateCalls != 1 {
		t.Fatalf("coordinated backfill batches = %d", batchGateCalls)
	}
	completed, err := service.Get(ctx, started.JobID)
	if err != nil || completed.State != "complete" ||
		completed.Progress.Completed != 3 ||
		completed.Cursor.LastRecordID == "" ||
		completed.Error != nil {
		t.Fatalf("completed job = %#v, err=%v", completed, err)
	}
	for index := 0; index < 3; index++ {
		recordID := fmt.Sprintf("jobrecord%06d", index+1)
		record, err := app.FindRecordById(collection, recordID)
		if err != nil ||
			record.GetString("computed") != fmt.Sprintf("NOTE-%d", index+1) {
			t.Fatalf("backfilled record %q = %#v, err=%v", recordID, record, err)
		}
	}
	formulaMeta, err := app.FindFirstRecordByFilter(
		"vibetable_formulas",
		"table_id='job_notes' && field_id='computed_id'",
	)
	if err != nil || formulaMeta.GetString("status") != "ready" {
		t.Fatalf("formula metadata = %#v, err=%v", formulaMeta, err)
	}
	refreshed, err := schemaapi.New(app).Describe(ctx, "job_notes")
	if err != nil || refreshed.Fields[1].Formula == nil ||
		refreshed.Fields[1].Formula.Status != "ready" {
		t.Fatalf("refreshed schema = %#v, err=%v", refreshed, err)
	}
	if err := service.Run(ctx, started.JobID); err != nil {
		t.Fatalf("completed job replay: %v", err)
	}
	subscription, err := hub.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	taskStates := []string{}
	for _, event := range subscription.Backlog {
		if event.Topic != "task.changed" {
			continue
		}
		var changed realtime.TaskChangedEvent
		if err := json.Unmarshal(event.Payload, &changed); err != nil {
			t.Fatal(err)
		}
		if changed.TaskID != started.JobID ||
			changed.TaskType != "formulaBackfill" {
			t.Fatalf("task event = %#v", changed)
		}
		taskStates = append(taskStates, changed.State)
		if changed.State == "succeeded" &&
			(changed.Progress != 1 || changed.Cursor == nil) {
			t.Fatalf("completed task event = %#v", changed)
		}
	}
	if fmt.Sprint(taskStates) != "[pending running running succeeded]" {
		t.Fatalf("task states = %#v", taskStates)
	}
	second, err := service.StartFormulaBackfill(
		ctx, "job_notes", definition.SchemaRevision,
	)
	if err != nil || second.JobID == "" || second.JobID == started.JobID {
		t.Fatalf("second formula backfill = %#v, err=%v", second, err)
	}
}

func TestFormulaBackfillTenThousandRowsCancelsResumesWithoutDuplicateAudit(
	t *testing.T,
) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("scale_notes", "scale_notes", []schema.FieldDefinition{
			field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText),
			formulaField(
				"computed_id", "computed",
				schema.DataTypeShortText, "upper(title)",
			),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId("scale_notes")
	if err != nil {
		t.Fatal(err)
	}
	const rowCount = 10_000
	if err := app.RunInTransaction(func(txApp core.App) error {
		for index := 0; index < rowCount; index++ {
			record := core.NewRecord(collection)
			record.Id = fmt.Sprintf("scale%010d", index+1)
			record.Set("title", fmt.Sprintf("note-%d", index+1))
			record.Set("computed", "STALE")
			if err := txApp.Save(record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed %d rows: %v", rowCount, err)
	}

	realKernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(
			formula.NewCalculator(
				formula.NewCompiler(formula.DefaultLimits()),
			),
		),
	)
	pausedKernel := &firstBatchPauseKernel{
		inner: realKernel, paused: make(chan struct{}), release: make(chan struct{}),
	}
	service := jobs.New(app, pausedKernel)
	defer service.Shutdown()
	started, err := service.StartFormulaBackfill(
		ctx, "scale_notes", definition.SchemaRevision,
	)
	if err != nil || started.Progress.Total != rowCount {
		t.Fatalf("start 10k backfill = %#v, err=%v", started, err)
	}
	runResult := make(chan error, 1)
	go func() {
		runResult <- service.Run(ctx, started.JobID)
	}()
	<-pausedKernel.paused
	cancelled, err := service.Cancel(ctx, started.JobID)
	if err != nil || cancelled.State != "cancelled" {
		t.Fatalf("cancelled snapshot = %#v, err=%v", cancelled, err)
	}
	deduplicated, err := service.StartFormulaBackfill(
		ctx, "scale_notes", definition.SchemaRevision,
	)
	if err != nil || deduplicated.JobID != started.JobID ||
		deduplicated.State != "cancelled" {
		t.Fatalf("cancelled job dedupe = %#v, err=%v", deduplicated, err)
	}
	close(pausedKernel.release)
	if err := <-runResult; err != nil {
		t.Fatalf("cancelled run returned %v", err)
	}

	resumed, err := service.Resume(ctx, started.JobID)
	if err != nil || resumed.State != "queued" {
		t.Fatalf("resumed snapshot = %#v, err=%v", resumed, err)
	}
	if err := service.Run(ctx, started.JobID); err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	completed, err := service.Get(ctx, started.JobID)
	if err != nil || completed.State != "complete" ||
		completed.Progress.Completed != rowCount {
		t.Fatalf("completed 10k backfill = %#v, err=%v", completed, err)
	}
	assertRecordCount(t, app, "vibetable_audit_events", rowCount)
	assertRecordCount(t, app, "vibetable_idempotency_keys", rowCount/100)
}

func TestFormulaBackfillStartupRecoveryQueuesMissingJob(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"recovery_notes", "recovery_notes",
			[]schema.FieldDefinition{
				field(
					"title_id", "title",
					schema.FieldKindScalar, schema.DataTypeShortText,
				),
				formulaField(
					"computed_id", "computed",
					schema.DataTypeShortText, "upper(title)",
				),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId("recovery_notes")
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("title", "recover me")
	record.Set("computed", "STALE")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(
			formula.NewCalculator(
				formula.NewCompiler(formula.DefaultLimits()),
			),
		),
	)
	service := jobs.New(app, kernel)
	defer service.Shutdown()
	service.ResumePending(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		formulaMeta, findErr := app.FindFirstRecordByFilter(
			"vibetable_formulas",
			"table_id='recovery_notes' && field_id='computed_id'",
		)
		if findErr == nil && formulaMeta.GetString("status") == "ready" {
			updated, readErr := app.FindRecordById(collection, record.Id)
			if readErr != nil || updated.GetString("computed") != "RECOVER ME" {
				t.Fatalf("recovered record = %#v, err=%v", updated, readErr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf(
		"missing backfill for schema revision %s was not recovered",
		definition.SchemaRevision,
	)
}

func TestFormulaBackfillShutdownJoinsCommittedRunBeforeStorageTeardown(
	t *testing.T,
) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"shutdown_notes",
			"shutdown_notes",
			[]schema.FieldDefinition{
				field(
					"title_id",
					"title",
					schema.FieldKindScalar,
					schema.DataTypeShortText,
				),
				formulaField(
					"computed_id",
					"computed",
					schema.DataTypeShortText,
					"upper(title)",
				),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("title", "survives shutdown")
	record.Set("computed", "STALE")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}

	beforeCommit := make(chan struct{})
	releaseCommit := make(chan struct{})
	var pauseOnce sync.Once
	dataPublisher := &recordingDataPublisher{}
	service := jobs.New(
		app,
		nil,
		jobs.WithDataPublisher(dataPublisher),
	)
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(
			formula.NewCalculator(
				formula.NewCompiler(formula.DefaultLimits()),
			),
		),
		mutation.WithPublisher(service),
		mutation.WithPublishContext(service.PublishContext()),
		mutation.WithFaultInjector(func(point string) error {
			if point == "before_commit" {
				pauseOnce.Do(func() {
					close(beforeCommit)
					<-releaseCommit
				})
			}
			return nil
		}),
	)
	service.SetKernel(kernel)
	service.ResumePending(ctx)

	select {
	case <-beforeCommit:
	case <-time.After(2 * time.Second):
		t.Fatal("background backfill did not reach the commit barrier")
	}
	shutdownDone := make(chan struct{})
	go func() {
		service.Shutdown()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
		t.Fatal("service shutdown returned before its background run")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseCommit)
	select {
	case <-shutdownDone:
	case <-time.After(2 * time.Second):
		t.Fatal("service shutdown did not join the committed background run")
	}
	if service.Start("job-after-shutdown") {
		t.Fatal("service accepted a background run after shutdown")
	}

	if got := dataPublisher.callCount(); got != 1 {
		t.Fatalf("shutdown lifecycle published %d live events, want 1", got)
	}
	if err := service.PublishContext().Err(); err != context.Canceled {
		t.Fatalf("publish lifecycle error after shutdown = %v", err)
	}
	assertRecordCount(t, app, "vibetable_outbox", 1)
	catchupCtx, cancelCatchup := context.WithCancel(ctx)
	defer cancelCatchup()
	catchup, err := realtime.New(app).Subscribe(catchupCtx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer catchup.Close()
	if len(catchup.Backlog) != 1 ||
		catchup.Backlog[0].Topic != "data.changed" {
		t.Fatalf("durable shutdown backlog = %#v", catchup.Backlog)
	}
}

func TestFormulaBackfillLifecycleCancellationRemainsResumable(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"cancel_backfill_notes",
			"cancel_backfill_notes",
			[]schema.FieldDefinition{
				field(
					"title_id",
					"title",
					schema.FieldKindScalar,
					schema.DataTypeShortText,
				),
				formulaField(
					"computed_id",
					"computed",
					schema.DataTypeShortText,
					"upper(title)",
				),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("title", "resume me")
	record.Set("computed", "STALE")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}

	blockingKernel := &cancelDuringApplyKernel{entered: make(chan struct{})}
	interrupted := jobs.New(app, blockingKernel)
	started, err := interrupted.StartFormulaBackfill(
		ctx,
		definition.TableID,
		definition.SchemaRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !interrupted.Start(started.JobID) {
		t.Fatal("formula backfill did not start")
	}
	select {
	case <-blockingKernel.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("formula backfill did not enter mutation apply")
	}
	interrupted.Shutdown()

	preserved, err := interrupted.Get(ctx, started.JobID)
	if err != nil || preserved.State != "running" || preserved.Error != nil {
		t.Fatalf("interrupted backfill = %#v, err=%v", preserved, err)
	}
	formulaMeta, err := app.FindFirstRecordByFilter(
		"vibetable_formulas",
		"table_id={:table}",
		dbx.Params{"table": definition.TableID},
	)
	if err != nil || formulaMeta.GetString("status") != "backfilling" {
		t.Fatalf("interrupted formula metadata = %#v, err=%v", formulaMeta, err)
	}

	realKernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(
			formula.NewCalculator(
				formula.NewCompiler(formula.DefaultLimits()),
			),
		),
	)
	restarted := jobs.New(app, realKernel)
	defer restarted.Shutdown()
	restarted.ResumePending(ctx)
	waitForJobState(t, restarted, started.JobID, "complete")
	updated, err := app.FindRecordById(collection, record.Id)
	if err != nil || updated.GetString("computed") != "RESUME ME" {
		t.Fatalf("resumed backfill record = %#v, err=%v", updated, err)
	}
}

func TestFormulaFanoutLifecycleCancellationRemainsResumable(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"cancel_fanout_notes",
			"cancel_fanout_notes",
			[]schema.FieldDefinition{
				field(
					"title_id",
					"title",
					schema.FieldKindScalar,
					schema.DataTypeShortText,
				),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(collection)
	row.Id = "cancelfanout001"
	row.Set("title", "unchanged")
	if err := app.Save(row); err != nil {
		t.Fatal(err)
	}
	revision, err := schema.ParseSchemaRevision(definition.SchemaRevision)
	if err != nil {
		t.Fatal(err)
	}
	jobCollection, err := app.FindCollectionByNameOrId("vibetable_jobs")
	if err != nil {
		t.Fatal(err)
	}
	job := core.NewRecord(jobCollection)
	job.Set("job_type", "formula_fanout")
	job.Set("state", "queued")
	job.Set("schema_revision", revision)
	job.Set("cursor_json", types.JSONRaw([]byte(fmt.Sprintf(
		`{"tableId":%q,"lastRecordId":"","relationFieldId":"source",`+
			`"targetRecordIds":[],"recordIds":[%q]}`,
		definition.TableID,
		row.Id,
	))))
	job.Set(
		"progress_json",
		types.JSONRaw([]byte(`{"completed":0,"total":1}`)),
	)
	job.Set("error_json", nil)
	if err := app.Save(job); err != nil {
		t.Fatal(err)
	}

	blockingKernel := &cancelDuringApplyKernel{entered: make(chan struct{})}
	interrupted := jobs.New(app, blockingKernel)
	if !interrupted.Start(job.Id) {
		t.Fatal("formula fan-out did not start")
	}
	select {
	case <-blockingKernel.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("formula fan-out did not enter mutation apply")
	}
	interrupted.Shutdown()

	preserved, err := interrupted.Get(ctx, job.Id)
	if err != nil || preserved.State != "running" || preserved.Error != nil {
		t.Fatalf("interrupted fan-out = %#v, err=%v", preserved, err)
	}

	realKernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
	)
	restarted := jobs.New(app, realKernel)
	fanoutGate := make(chan [2]string, 1)
	restarted.SetBusinessWriteGate(func(
		ctx context.Context,
		kind string,
		identity string,
		apply func(context.Context) error,
	) error {
		fanoutGate <- [2]string{kind, identity}
		return apply(ctx)
	})
	defer restarted.Shutdown()
	restarted.ResumePending(ctx)
	waitForJobState(t, restarted, job.Id, "complete")
	select {
	case observed := <-fanoutGate:
		if observed[0] != "formula.fanout.batch" ||
			!strings.HasPrefix(observed[1], "fanout_") {
			t.Fatalf("fanout write gate = %#v", observed)
		}
	case <-time.After(time.Second):
		t.Fatal("fanout batch bypassed the business write gate")
	}
	updated, err := app.FindRecordById(collection, row.Id)
	if err != nil || updated.GetString("title") != "unchanged" {
		t.Fatalf("resumed fan-out record = %#v, err=%v", updated, err)
	}

	deadlineJob := core.NewRecord(jobCollection)
	deadlineJob.Set("job_type", "formula_fanout")
	deadlineJob.Set("state", "queued")
	deadlineJob.Set("schema_revision", revision)
	deadlineJob.Set(
		"cursor_json",
		types.JSONRaw([]byte(fmt.Sprintf(
			`{"tableId":%q,"lastRecordId":"","relationFieldId":"deadline",`+
				`"targetRecordIds":[],"recordIds":[%q]}`,
			definition.TableID,
			row.Id,
		))),
	)
	deadlineJob.Set(
		"progress_json",
		types.JSONRaw([]byte(`{"completed":0,"total":1}`)),
	)
	deadlineJob.Set("error_json", nil)
	deadlineJob.Set("source_event_id", "business-deadline")
	deadlineJob.Set("source_table_id", definition.TableID)
	deadlineJob.Set("relation_field_id", "deadline")
	if err := app.Save(deadlineJob); err != nil {
		t.Fatal(err)
	}
	businessFailure := jobs.New(app, businessDeadlineKernel{})
	if err := businessFailure.Run(ctx, deadlineJob.Id); err == nil {
		t.Fatal("business deadline unexpectedly succeeded")
	}
	failedDeadline, err := businessFailure.Get(ctx, deadlineJob.Id)
	if err != nil || failedDeadline.State != "failed" {
		t.Fatalf("business deadline job = %#v, err=%v", failedDeadline, err)
	}
}

func TestTerminalTaskEventsCommitAtomicallyBeforeLivePublish(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"terminal_event_notes",
			"terminal_event_notes",
			[]schema.FieldDefinition{
				field(
					"title_id",
					"title",
					schema.FieldKindScalar,
					schema.DataTypeShortText,
				),
				formulaField(
					"computed_id",
					"computed",
					schema.DataTypeShortText,
					"upper(title)",
				),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.New(app)
	publisher := crashAfterTerminalPersistPublisher{hub: hub}
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(
			formula.NewCalculator(
				formula.NewCompiler(formula.DefaultLimits()),
			),
		),
	)
	service := jobs.New(
		app,
		kernel,
		jobs.WithTaskPublisher(publisher),
	)
	defer service.Shutdown()

	completed, err := service.StartFormulaBackfill(
		ctx,
		definition.TableID,
		definition.SchemaRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Run(ctx, completed.JobID); err != nil {
		t.Fatal(err)
	}

	revision, err := schema.ParseSchemaRevision(definition.SchemaRevision)
	if err != nil {
		t.Fatal(err)
	}
	jobCollection, err := app.FindCollectionByNameOrId("vibetable_jobs")
	if err != nil {
		t.Fatal(err)
	}
	cancelled := core.NewRecord(jobCollection)
	cancelled.Set("job_type", "formula_fanout")
	cancelled.Set("state", "queued")
	cancelled.Set("schema_revision", revision)
	cancelled.Set(
		"cursor_json",
		types.JSONRaw([]byte(fmt.Sprintf(
			`{"tableId":%q,"lastRecordId":"","relationFieldId":"cancel",`+
				`"targetRecordIds":[],"recordIds":[]}`,
			definition.TableID,
		))),
	)
	cancelled.Set(
		"progress_json",
		types.JSONRaw([]byte(`{"completed":0,"total":0}`)),
	)
	cancelled.Set("error_json", nil)
	cancelled.Set("source_event_id", "terminal-cancelled")
	cancelled.Set("source_table_id", definition.TableID)
	cancelled.Set("relation_field_id", "cancel")
	if err := app.Save(cancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Cancel(ctx, cancelled.Id); err != nil {
		t.Fatal(err)
	}

	failed := core.NewRecord(jobCollection)
	failed.Set("job_type", "formula_fanout")
	failed.Set("state", "queued")
	failed.Set("schema_revision", revision+1)
	failed.Set(
		"cursor_json",
		types.JSONRaw([]byte(fmt.Sprintf(
			`{"tableId":%q,"lastRecordId":"","relationFieldId":"failed",`+
				`"targetRecordIds":[],"recordIds":["missingrecord"]}`,
			definition.TableID,
		))),
	)
	failed.Set(
		"progress_json",
		types.JSONRaw([]byte(`{"completed":0,"total":1}`)),
	)
	failed.Set("error_json", nil)
	failed.Set("source_event_id", "terminal-failed")
	failed.Set("source_table_id", definition.TableID)
	failed.Set("relation_field_id", "failed")
	if err := app.Save(failed); err != nil {
		t.Fatal(err)
	}
	if err := service.Run(ctx, failed.Id); err == nil {
		t.Fatal("schema-drifted job unexpectedly succeeded")
	}

	var terminalRows int
	if err := app.DB().NewQuery(`
		SELECT COUNT(*) FROM vibetable_outbox
		WHERE topic = 'task.changed'
	`).Row(&terminalRows); err != nil {
		t.Fatal(err)
	}
	if terminalRows != 3 {
		t.Fatalf("terminal task outbox rows = %d, want 3", terminalRows)
	}

	restartedHub := realtime.New(app)
	restarted, err := restartedHub.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	states := map[string]bool{}
	for _, event := range restarted.Backlog {
		if event.Topic != "task.changed" {
			continue
		}
		var changed realtime.TaskChangedEvent
		if err := json.Unmarshal(event.Payload, &changed); err != nil {
			t.Fatal(err)
		}
		states[changed.State] = true
	}
	for _, state := range []string{"succeeded", "cancelled", "failed"} {
		if !states[state] {
			t.Fatalf("restart task states = %#v, missing %q", states, state)
		}
	}
}

func TestResumePendingDrainsJobsBeyondConcurrencyWindow(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"resume_window_notes",
			"resume_window_notes",
			[]schema.FieldDefinition{
				field(
					"title_id",
					"title",
					schema.FieldKindScalar,
					schema.DataTypeShortText,
				),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := schema.ParseSchemaRevision(definition.SchemaRevision)
	if err != nil {
		t.Fatal(err)
	}
	jobCollection, err := app.FindCollectionByNameOrId("vibetable_jobs")
	if err != nil {
		t.Fatal(err)
	}
	const jobCount = 40
	for index := 0; index < jobCount; index++ {
		job := core.NewRecord(jobCollection)
		job.Set("job_type", "formula_fanout")
		job.Set("state", "queued")
		job.Set("schema_revision", revision)
		job.Set(
			"cursor_json",
			types.JSONRaw([]byte(fmt.Sprintf(
				`{"tableId":%q,"lastRecordId":"","relationFieldId":%q,`+
					`"targetRecordIds":[],"recordIds":[]}`,
				definition.TableID,
				fmt.Sprintf("field-%02d", index),
			))),
		)
		job.Set(
			"progress_json",
			types.JSONRaw([]byte(`{"completed":0,"total":0}`)),
		)
		job.Set("error_json", nil)
		job.Set("source_event_id", fmt.Sprintf("resume-event-%02d", index))
		job.Set("source_table_id", definition.TableID)
		job.Set("relation_field_id", fmt.Sprintf("field-%02d", index))
		if err := app.Save(job); err != nil {
			t.Fatal(err)
		}
	}
	service := jobs.New(
		app,
		mutation.New(app, mutation.MetadataSchemaSource{}),
	)
	defer service.Shutdown()
	if err := service.ResumePending(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var completed int
		if err := app.DB().NewQuery(`
			SELECT COUNT(*) FROM vibetable_jobs
			WHERE job_type = 'formula_fanout' AND state = 'complete'
		`).Row(&completed); err != nil {
			t.Fatal(err)
		}
		if completed == jobCount {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("pending job dispatcher left queued jobs behind")
}

func TestResumePendingFailsClosedOnInvalidRetainedDataEvent(t *testing.T) {
	tests := []struct {
		name        string
		rowEventID  string
		payload     func(t *testing.T) string
		wantErrCode string
	}{
		{
			name:       "malformed payload",
			rowEventID: "resume-invalid-payload",
			payload: func(*testing.T) string {
				return "{"
			},
			wantErrCode: "job.resume_outbox_invalid_payload",
		},
		{
			name:       "event id mismatch",
			rowEventID: "resume-row-event",
			payload: func(t *testing.T) string {
				t.Helper()
				event := realtimeDataEvent(1)
				raw, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			},
			wantErrCode: "job.resume_outbox_event_id_mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := bootstrapApp(t, queryTempDir(t))
			defer resetApp(t, app)
			seedFormulaDependency(t, app)
			_, err := app.DB().NewQuery(`
				INSERT INTO vibetable_outbox (
					id, event_id, topic, payload_json, status, attempts
				) VALUES (
					{:id}, {:event}, 'data.changed', {:payload}, 'pending', 0
				)
			`).Bind(dbx.Params{
				"id":      "resumeoutbox001",
				"event":   test.rowEventID,
				"payload": test.payload(t),
			}).Execute()
			if err != nil {
				t.Fatal(err)
			}

			service := jobs.New(
				app,
				mutation.New(app, mutation.MetadataSchemaSource{}),
			)
			defer service.Shutdown()
			err = service.ResumePending(context.Background())
			var productErr *jobs.JobError
			if !errors.As(err, &productErr) ||
				productErr.Code != test.wantErrCode {
				t.Fatalf(
					"ResumePending() error = %#v, want %q",
					err,
					test.wantErrCode,
				)
			}
		})
	}
}

func TestResumePendingRejectsCorruptedOutboxRetentionOverflow(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	seedFormulaDependency(t, app)
	if _, err := app.DB().NewQuery(
		"DROP TRIGGER IF EXISTS vibetable_outbox_retain_latest",
	).Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().NewQuery(`
		WITH RECURSIVE sequence(value) AS (
			SELECT 1
			UNION ALL
			SELECT value + 1 FROM sequence WHERE value < 10001
		)
		INSERT INTO vibetable_outbox (
			id, event_id, topic, payload_json, status, attempts
		)
		SELECT
			printf('outbox%09d', value),
			printf('resume-event-%05d', value),
			'data.changed',
			'{}',
			'pending',
			0
		FROM sequence
	`).Execute(); err != nil {
		t.Fatal(err)
	}

	service := jobs.New(
		app,
		mutation.New(app, mutation.MetadataSchemaSource{}),
	)
	defer service.Shutdown()
	err := service.ResumePending(context.Background())
	var productErr *jobs.JobError
	if !errors.As(err, &productErr) ||
		productErr.Code != "job.resume_outbox_limit" {
		t.Fatalf(
			"ResumePending() error = %#v, want job.resume_outbox_limit",
			err,
		)
	}
}

func TestResumePendingRejectsJobRecoveryOverflow(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	if _, err := app.DB().NewQuery(`
		WITH RECURSIVE sequence(value) AS (
			SELECT 1
			UNION ALL
			SELECT value + 1 FROM sequence WHERE value < 10001
		)
		INSERT INTO vibetable_jobs (
			id, job_type, state, cursor_json, progress_json,
			schema_revision, source_event_id, source_table_id,
			relation_field_id
		)
		SELECT
			printf('job%012d', value),
			'formula_fanout',
			'queued',
			'{}',
			'{}',
			1,
			printf('event-%05d', value),
			'table',
			printf('field-%05d', value)
		FROM sequence
	`).Execute(); err != nil {
		t.Fatal(err)
	}

	service := jobs.New(
		app,
		mutation.New(app, mutation.MetadataSchemaSource{}),
	)
	defer service.Shutdown()
	err := service.ResumePending(context.Background())
	var productErr *jobs.JobError
	if !errors.As(err, &productErr) ||
		productErr.Code != "job.resume_limit" {
		t.Fatalf("ResumePending() error = %#v, want job.resume_limit", err)
	}
}

func seedFormulaDependency(t *testing.T, app core.App) {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId(
		"vibetable_formula_dependencies",
	)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("source_table_id", "resume_source")
	record.Set("formula_field_id", "resume_formula")
	record.Set("relation_field_id", "resume_relation")
	record.Set("target_table_id", "resume_target")
	record.Set("target_field_id", "resume_target_field")
	record.Set("dependency_kind", "formula")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
}

func waitForJobState(
	t *testing.T,
	service *jobs.Service,
	jobID string,
	wantState string,
) jobs.Snapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := service.Get(context.Background(), jobID)
		if err == nil && snapshot.State == wantState {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, err := service.Get(context.Background(), jobID)
	t.Fatalf(
		"job %s state = %q, want %q, err=%v",
		jobID,
		snapshot.State,
		wantState,
		err,
	)
	return jobs.Snapshot{}
}

func TestFormulaBackfillJobFailsClosedOnSchemaDrift(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	catalog := schemaapi.New(app)
	definition, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("drift_notes", "drift_notes", []schema.FieldDefinition{
			field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText),
			formulaField(
				"computed_id", "computed",
				schema.DataTypeShortText, "upper(title)",
			),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(
			formula.NewCalculator(
				formula.NewCompiler(formula.DefaultLimits()),
			),
		),
	)
	service := jobs.New(app, kernel)
	started, err := service.StartFormulaBackfill(
		ctx, definition.TableID, definition.SchemaRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	definition.DisplayName = "Renamed"
	if _, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: definition, ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	err = service.Run(ctx, started.JobID)
	var jobErr *jobs.JobError
	if !errors.As(err, &jobErr) ||
		jobErr.Code != "job.schema_revision_conflict" {
		t.Fatalf("schema drift error = %#v", err)
	}
	failed, getErr := service.Get(ctx, started.JobID)
	if getErr != nil || failed.State != "failed" ||
		failed.Error == nil ||
		failed.Error.Code != "job.schema_revision_conflict" {
		t.Fatalf("failed job = %#v, err=%v", failed, getErr)
	}
}
