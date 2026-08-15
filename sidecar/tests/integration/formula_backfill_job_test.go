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
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
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

type formulaBackfillFixture struct {
	definition schemaexecution.Table
	title      *v2.FieldDefinition
	computed   *v2.FieldDefinition
}

func createFormulaBackfillFixture(
	t *testing.T,
	ctx context.Context,
	app core.App,
	displayName string,
	operationPrefix string,
) formulaBackfillFixture {
	t.Helper()
	table := createV2IntegrationTable(
		t, ctx, app, displayName, operationPrefix+"_table",
	)
	title := createV2IntegrationField(
		t, ctx, app, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"),
		operationPrefix+"_title",
	)
	computedDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Computed")
	computedDraft.Formula = &v2.FormulaDraftSpec{
		Language: "cel-v1", Source: "upper({Title})",
	}
	computed := createV2IntegrationFormula(
		t, ctx, app, table.TableID, computedDraft, operationPrefix+"_computed",
	)
	definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	titleDefinition := integrationFieldByID(definition, title.FieldID)
	computedDefinition := integrationFieldByID(definition, computed.FieldID)
	if titleDefinition == nil || computedDefinition == nil {
		t.Fatalf("Schema V2 fixture omitted fields: %#v", definition.Snapshot.Fields)
	}
	return formulaBackfillFixture{
		definition: definition, title: titleDefinition, computed: computedDefinition,
	}
}

type countingFanoutKernel struct {
	mu             sync.Mutex
	operationCount int
	batchCount     int
	expectedCount  int
	completed      chan struct{}
	completedOnce  sync.Once
}

func (kernel *countingFanoutKernel) Apply(
	_ context.Context,
	request mutation.Request,
) (mutation.Receipt, error) {
	kernel.mu.Lock()
	kernel.operationCount += len(request.Operations)
	kernel.batchCount++
	reachedExpected := kernel.expectedCount > 0 && kernel.operationCount >= kernel.expectedCount
	kernel.mu.Unlock()
	if reachedExpected {
		kernel.completedOnce.Do(func() { close(kernel.completed) })
	}
	return mutation.Receipt{
		ContractVersion: mutation.ContractVersion,
		Status:          mutation.StatusApplied,
		ComputedFields:  map[string]map[string]any{},
		EmittedEvents:   []string{},
		Warnings:        []mutation.ProductError{},
	}, nil
}

func (kernel *countingFanoutKernel) counts() (int, int) {
	kernel.mu.Lock()
	defer kernel.mu.Unlock()
	return kernel.operationCount, kernel.batchCount
}

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
	fixture := createFormulaBackfillFixture(t, ctx, app, "Job notes", "job_notes")
	definition := fixture.definition
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		record := core.NewRecord(collection)
		record.Id = fmt.Sprintf("jobrecord%06d", index+1)
		record.Set(fixture.title.Identity.PhysicalName, fmt.Sprintf("note-%d", index+1))
		record.Set(fixture.computed.Identity.PhysicalName, "STALE")
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
		ctx, definition.Snapshot.TableID, definition.Snapshot.SchemaRevision,
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
			relatedcomputation.ProjectStored(record.GetRaw(fixture.computed.Identity.PhysicalName)) != fmt.Sprintf("NOTE-%d", index+1) {
			t.Fatalf("backfilled record %q = %#v, err=%v", recordID, record, err)
		}
	}
	formulaMeta, err := app.FindFirstRecordByFilter(
		"vibetable_formulas",
		"table_id={:table} && field_id={:field}",
		dbx.Params{"table": definition.Snapshot.TableID, "field": fixture.computed.Identity.FieldID},
	)
	if err != nil || formulaMeta.GetString("status") != "ready" {
		t.Fatalf("formula metadata = %#v, err=%v", formulaMeta, err)
	}
	refreshed, err := schemaapi.New(app).Describe(ctx, definition.Snapshot.TableID)
	if err != nil ||
		refreshed.FormulaRuntime[fixture.computed.Identity.FieldID].Status != "ready" {
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
		ctx, definition.Snapshot.TableID, definition.Snapshot.SchemaRevision,
	)
	if err != nil || second.JobID == "" || second.JobID == started.JobID {
		t.Fatalf("second formula backfill = %#v, err=%v", second, err)
	}
}

func TestFormulaBackfillScaleCancelsResumesWithoutDuplicateAudit(
	t *testing.T,
) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	fixture := createFormulaBackfillFixture(t, ctx, app, "Scale notes", "scale_notes")
	definition := fixture.definition
	var initialIdempotencyCount int
	if err := app.DB().NewQuery("SELECT COUNT(*) FROM vibetable_idempotency_keys").Row(&initialIdempotencyCount); err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	const rowCount = formulaBackfillScaleRows
	if err := app.RunInTransaction(func(txApp core.App) error {
		for index := 0; index < rowCount; index++ {
			record := core.NewRecord(collection)
			record.Id = fmt.Sprintf("scale%010d", index+1)
			record.Set(fixture.title.Identity.PhysicalName, fmt.Sprintf("note-%d", index+1))
			record.Set(fixture.computed.Identity.PhysicalName, "STALE")
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
		ctx, definition.Snapshot.TableID, definition.Snapshot.SchemaRevision,
	)
	if err != nil || started.Progress.Total != rowCount {
		t.Fatalf("start %d-row backfill = %#v, err=%v", rowCount, started, err)
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
	cancelledDefinition, err := schemaapi.New(app).Describe(ctx, definition.Snapshot.TableID)
	if err != nil ||
		cancelledDefinition.FormulaRuntime[fixture.computed.Identity.FieldID].Status != "cancelled" {
		t.Fatalf("cancelled formula definition = %#v, err=%v", cancelledDefinition, err)
	}
	deduplicated, err := service.StartFormulaBackfill(
		ctx, definition.Snapshot.TableID, definition.Snapshot.SchemaRevision,
	)
	if err != nil || deduplicated.JobID != started.JobID ||
		deduplicated.State != "cancelled" {
		t.Fatalf("cancelled job dedupe = %#v, err=%v", deduplicated, err)
	}
	resumed, err := service.Resume(ctx, started.JobID)
	if err != nil || resumed.State != "queued" {
		t.Fatalf("resumed snapshot = %#v, err=%v", resumed, err)
	}
	resumedDefinition, err := schemaapi.New(app).Describe(ctx, definition.Snapshot.TableID)
	if err != nil ||
		resumedDefinition.FormulaRuntime[fixture.computed.Identity.FieldID].Status != "backfilling" {
		t.Fatalf("resumed formula definition = %#v, err=%v", resumedDefinition, err)
	}
	close(pausedKernel.release)
	if err := <-runResult; err != nil {
		t.Fatalf("cancelled run returned %v", err)
	}
	stillQueued, err := service.Get(ctx, started.JobID)
	if err != nil || stillQueued.State != "queued" {
		t.Fatalf("old worker overwrote resumed job = %#v, err=%v", stillQueued, err)
	}
	stillBackfilling, err := schemaapi.New(app).Describe(ctx, definition.Snapshot.TableID)
	if err != nil ||
		stillBackfilling.FormulaRuntime[fixture.computed.Identity.FieldID].Status != "backfilling" {
		t.Fatalf(
			"old worker overwrote resumed formula definition = %#v, err=%v",
			stillBackfilling,
			err,
		)
	}
	if err := service.Run(ctx, started.JobID); err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	completed, err := service.Get(ctx, started.JobID)
	if err != nil || completed.State != "complete" ||
		completed.Progress.Completed != rowCount {
		t.Fatalf("completed %d-row backfill = %#v, err=%v", rowCount, completed, err)
	}
	readyDefinition, err := schemaapi.New(app).Describe(ctx, definition.Snapshot.TableID)
	if err != nil ||
		readyDefinition.FormulaRuntime[fixture.computed.Identity.FieldID].Status != "ready" {
		t.Fatalf("completed formula definition = %#v, err=%v", readyDefinition, err)
	}
	assertRecordCount(t, app, "vibetable_audit_events", rowCount)
	assertRecordCount(t, app, "vibetable_idempotency_keys", initialIdempotencyCount+rowCount/100)
}

func TestFormulaBackfillStartupRecoveryQueuesMissingJob(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture := createFormulaBackfillFixture(t, ctx, app, "Recovery notes", "recovery_notes")
	definition := fixture.definition
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set(fixture.title.Identity.PhysicalName, "recover me")
	record.Set(fixture.computed.Identity.PhysicalName, "STALE")
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
			"table_id={:table} && field_id={:field}",
			dbx.Params{
				"table": definition.Snapshot.TableID,
				"field": fixture.computed.Identity.FieldID,
			},
		)
		if findErr == nil && formulaMeta.GetString("status") == "ready" {
			updated, readErr := app.FindRecordById(collection, record.Id)
			if readErr != nil ||
				relatedcomputation.ProjectStored(updated.GetRaw(fixture.computed.Identity.PhysicalName)) != "RECOVER ME" {
				t.Fatalf("recovered record = %#v, err=%v", updated, readErr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf(
		"missing backfill for schema revision %s was not recovered",
		definition.Snapshot.SchemaRevision,
	)
}

func TestFormulaBackfillShutdownJoinsCommittedRunBeforeStorageTeardown(
	t *testing.T,
) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	fixture := createFormulaBackfillFixture(t, ctx, app, "Shutdown notes", "shutdown_notes")
	definition := fixture.definition
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set(fixture.title.Identity.PhysicalName, "survives shutdown")
	record.Set(fixture.computed.Identity.PhysicalName, "STALE")
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
	fixture := createFormulaBackfillFixture(
		t, ctx, app, "Cancel backfill notes", "cancel_backfill_notes",
	)
	definition := fixture.definition
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set(fixture.title.Identity.PhysicalName, "resume me")
	record.Set(fixture.computed.Identity.PhysicalName, "STALE")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}

	blockingKernel := &cancelDuringApplyKernel{entered: make(chan struct{})}
	interrupted := jobs.New(app, blockingKernel)
	started, err := interrupted.StartFormulaBackfill(
		ctx,
		definition.Snapshot.TableID,
		definition.Snapshot.SchemaRevision,
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
		dbx.Params{"table": definition.Snapshot.TableID},
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
	if err != nil ||
		relatedcomputation.ProjectStored(updated.GetRaw(fixture.computed.Identity.PhysicalName)) != "RESUME ME" {
		t.Fatalf("resumed backfill record = %#v, err=%v", updated, err)
	}
}

func TestFormulaFanoutLifecycleCancellationRemainsResumable(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(
		t, ctx, app, "Cancel fanout notes", "cancel_fanout_notes_table",
	)
	title := createV2IntegrationField(
		t, ctx, app, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"),
		"cancel_fanout_notes_title",
	)
	source := createV2IntegrationRelation(
		t, ctx, app, table.TableID, title.FieldID, table.TableID, title.FieldID,
		"Source", "Referenced by", "one", "cancel_fanout_notes_source",
	)
	definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	titleDefinition := integrationFieldByID(definition, title.FieldID)
	sourceDefinition := integrationFieldByID(definition, source.FieldID)
	if titleDefinition == nil || sourceDefinition == nil {
		t.Fatalf("Schema V2 fanout fixture omitted fields: %#v", definition.Snapshot.Fields)
	}
	var initialIdempotencyCount int
	if err := app.DB().NewQuery("SELECT COUNT(*) FROM vibetable_idempotency_keys").Row(&initialIdempotencyCount); err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(collection)
	row.Id = "cancelfanout001"
	row.Set(titleDefinition.Identity.PhysicalName, "unchanged")
	if err := app.Save(row); err != nil {
		t.Fatal(err)
	}
	row.Set(sourceDefinition.Identity.PhysicalName, row.Id)
	if err := app.Save(row); err != nil {
		t.Fatal(err)
	}
	revision, err := v2.ParseSchemaRevision(definition.Snapshot.SchemaRevision)
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
		`{"tableId":%q,"lastRecordId":"","relationFieldId":%q,`+
			`"changedTableId":%q,"targetRecordIds":[%q],"formulaFieldIds":[]}`,
		definition.Snapshot.TableID,
		source.FieldID,
		definition.Snapshot.TableID,
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

	realKernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
	)
	blockingKernel := &firstBatchPauseKernel{
		inner: realKernel, paused: make(chan struct{}), release: make(chan struct{}),
	}
	interrupted := jobs.New(app, blockingKernel)
	if !interrupted.Start(job.Id) {
		t.Fatal("formula fan-out did not start")
	}
	select {
	case <-blockingKernel.paused:
	case <-time.After(2 * time.Second):
		t.Fatal("formula fan-out did not enter mutation apply")
	}
	cancelled, err := interrupted.Cancel(ctx, job.Id)
	if err != nil || cancelled.State != "cancelled" {
		t.Fatalf("cancelled fan-out = %#v, err=%v", cancelled, err)
	}
	close(blockingKernel.release)
	interrupted.Shutdown()

	preserved, err := interrupted.Get(ctx, job.Id)
	if err != nil || preserved.State != "cancelled" || preserved.Error != nil ||
		preserved.Cursor.LastRecordID != "" || preserved.Progress.Completed != 0 {
		t.Fatalf("cancelled fan-out = %#v, err=%v", preserved, err)
	}
	assertRecordCount(t, app, "vibetable_idempotency_keys", initialIdempotencyCount+1)
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
	resumed, err := restarted.Resume(ctx, job.Id)
	if err != nil || resumed.State != "queued" {
		t.Fatalf("resumed fan-out = %#v, err=%v", resumed, err)
	}
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
	if err != nil || updated.GetString(titleDefinition.Identity.PhysicalName) != "unchanged" {
		t.Fatalf("resumed fan-out record = %#v, err=%v", updated, err)
	}
	assertRecordCount(t, app, "vibetable_idempotency_keys", initialIdempotencyCount+1)

	deadlineJob := core.NewRecord(jobCollection)
	deadlineJob.Set("job_type", "formula_fanout")
	deadlineJob.Set("state", "queued")
	deadlineJob.Set("schema_revision", revision)
	deadlineJob.Set(
		"cursor_json",
		types.JSONRaw([]byte(fmt.Sprintf(
			`{"tableId":%q,"lastRecordId":"","relationFieldId":%q,`+
				`"changedTableId":%q,"targetRecordIds":[%q],"formulaFieldIds":[]}`,
			definition.Snapshot.TableID,
			source.FieldID,
			definition.Snapshot.TableID,
			row.Id,
		))),
	)
	deadlineJob.Set(
		"progress_json",
		types.JSONRaw([]byte(`{"completed":0,"total":1}`)),
	)
	deadlineJob.Set("error_json", nil)
	deadlineJob.Set("source_event_id", "business-deadline")
	deadlineJob.Set("source_table_id", definition.Snapshot.TableID)
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

func TestFormulaFanoutPagesMoreThanTenThousandSourceRecords(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	authors := createV2IntegrationTable(
		t, ctx, app, "Fanout scale authors", "fanout_scale_authors_table",
	)
	name := createV2IntegrationField(
		t, ctx, app, authors.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"),
		"fanout_scale_authors_name",
	)
	articles := createV2IntegrationTable(
		t, ctx, app, "Fanout scale articles", "fanout_scale_articles_table",
	)
	articleTitle := createV2IntegrationField(
		t, ctx, app, articles.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Article"),
		"fanout_scale_articles_title",
	)
	authorRelation := createV2IntegrationRelation(
		t, ctx, app, articles.TableID, articleTitle.FieldID, authors.TableID, name.FieldID,
		"Author", "Articles", "one", "fanout_scale_articles_author",
	)
	authorNameDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Author name")
	authorNameDraft.Formula = &v2.FormulaDraftSpec{
		Language: "cel-v1", Source: `concat({Author}.{Name}, "")`,
	}
	authorName := createV2IntegrationFormula(
		t, ctx, app, articles.TableID, authorNameDraft, "fanout_scale_articles_author_name",
	)
	if name.Definition == nil || authorRelation.Definition == nil || authorName.Definition == nil {
		t.Fatalf("Schema V2 fanout scale fixture omitted field definitions: %#v %#v %#v", name, authorRelation, authorName)
	}
	authorID := "scaleauthor0001"
	authorCollection, err := app.FindCollectionByNameOrId(authors.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	author := core.NewRecord(authorCollection)
	author.Id = authorID
	author.Set(name.Definition.Identity.PhysicalName, "Before")
	if err := app.Save(author); err != nil {
		t.Fatal(err)
	}
	otherAuthorID := "scaleauthor0002"
	otherAuthor := core.NewRecord(authorCollection)
	otherAuthor.Id = otherAuthorID
	otherAuthor.Set(name.Definition.Identity.PhysicalName, "Other")
	if err := app.Save(otherAuthor); err != nil {
		t.Fatal(err)
	}
	seedQuery := fmt.Sprintf(`
		WITH RECURSIVE sequence(value) AS (
			SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value < 10001
		)
		INSERT INTO "%s" (id, "%s", "%s")
		SELECT
			printf('src%%012d', value),
			CASE WHEN value <= 10000 THEN {:author} ELSE {:otherAuthor} END,
			CASE WHEN value <= 10000 THEN 'Before' ELSE 'Other' END
		FROM sequence
	`, articles.PhysicalName, authorRelation.Definition.Identity.PhysicalName, authorName.Definition.Identity.PhysicalName)
	if _, err := app.DB().NewQuery(seedQuery).WithContext(ctx).Bind(dbx.Params{
		"author": authorID, "otherAuthor": otherAuthorID,
	}).Execute(); err != nil {
		t.Fatalf("seed 10001 fan-out sources: %v", err)
	}
	authorsDefinition, err := schemaapi.New(app).Describe(ctx, authors.TableID)
	if err != nil {
		t.Fatal(err)
	}

	realKernel := mutation.New(app, mutation.MetadataSchemaSource{})
	receipt, err := realKernel.Apply(ctx, mutationRequest(
		authors.TableID, authorsDefinition.Snapshot.SchemaRevision, "fanout-scale-target-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &authorID,
			Values: map[string]any{name.Definition.Identity.PhysicalName: "After"},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.EmittedEvents) != 1 {
		t.Fatalf("target mutation emitted events = %#v", receipt.EmittedEvents)
	}
	outbox, err := app.FindFirstRecordByFilter(
		"vibetable_outbox", "event_id={:event}",
		dbx.Params{"event": receipt.EmittedEvents[0]},
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(outbox.GetRaw("payload_json"))
	if err != nil {
		t.Fatal(err)
	}
	var committedEvent mutation.DataChangedEvent
	if err := mutation.DecodeStrict(raw, &committedEvent); err != nil {
		t.Fatal(err)
	}

	countingKernel := &countingFanoutKernel{
		expectedCount: 10_000,
		completed:     make(chan struct{}),
	}
	service := jobs.New(app, countingKernel)
	defer service.Shutdown()
	if err := service.Publish(ctx, committedEvent); err != nil {
		t.Fatal(err)
	}
	job, err := app.FindFirstRecordByFilter(
		"vibetable_jobs",
		"job_type='formula_fanout' && source_event_id={:event}",
		dbx.Params{"event": committedEvent.EventID},
	)
	if err != nil {
		t.Fatal(err)
	}
	testDeadline, ok := t.Deadline()
	if !ok {
		t.Fatal("10,000-row fan-out test requires the go test deadline")
	}
	completionWait := time.Until(testDeadline) - 5*time.Second
	if completionWait <= 0 {
		t.Fatal("10,000-row fan-out test deadline leaves no completion window")
	}
	completionTimer := time.NewTimer(completionWait)
	defer completionTimer.Stop()
	select {
	case <-countingKernel.completed:
	case <-completionTimer.C:
		operationCount, batchCount := countingKernel.counts()
		t.Fatalf(
			"10,000-row fan-out did not reach the counting kernel: operations=%d batches=%d",
			operationCount,
			batchCount,
		)
	}
	completed := waitForJobState(t, service, job.Id, "complete")
	if completed.Progress.Completed != 10_001 || completed.Progress.Total != 10_001 {
		t.Fatalf("10,001-row fan-out progress = %#v", completed.Progress)
	}
	operationCount, batchCount := countingKernel.counts()
	if operationCount != 10_000 || batchCount != 100 {
		t.Fatalf(
			"fan-out kernel counts = operations %d batches %d, want 10000 and 100",
			operationCount, batchCount,
		)
	}
	storedJob, err := app.FindRecordById("vibetable_jobs", job.Id)
	if err != nil {
		t.Fatal(err)
	}
	cursorRaw, err := json.Marshal(storedJob.GetRaw("cursor_json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cursorRaw), "recordIds") || len(cursorRaw) > 1_024 {
		t.Fatalf("fan-out cursor retained source record ids: %s", cursorRaw)
	}
	if !strings.Contains(string(cursorRaw), `"discoveryComplete":true`) ||
		!strings.Contains(string(cursorRaw), `"lastRecordId":"src000000010001"`) {
		t.Fatalf("fan-out cursor did not persist terminal page = %s", cursorRaw)
	}
	_ = articles
}

func TestTerminalTaskEventsCommitAtomicallyBeforeLivePublish(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	fixture := createFormulaBackfillFixture(
		t, ctx, app, "Terminal event notes", "terminal_event_notes",
	)
	definition := fixture.definition
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
		definition.Snapshot.TableID,
		definition.Snapshot.SchemaRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Run(ctx, completed.JobID); err != nil {
		t.Fatal(err)
	}

	revision, err := v2.ParseSchemaRevision(definition.Snapshot.SchemaRevision)
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
			definition.Snapshot.TableID,
		))),
	)
	cancelled.Set(
		"progress_json",
		types.JSONRaw([]byte(`{"completed":0,"total":0}`)),
	)
	cancelled.Set("error_json", nil)
	cancelled.Set("source_event_id", "terminal-cancelled")
	cancelled.Set("source_table_id", definition.Snapshot.TableID)
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
			definition.Snapshot.TableID,
		))),
	)
	failed.Set(
		"progress_json",
		types.JSONRaw([]byte(`{"completed":0,"total":1}`)),
	)
	failed.Set("error_json", nil)
	failed.Set("source_event_id", "terminal-failed")
	failed.Set("source_table_id", definition.Snapshot.TableID)
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
	table := createV2IntegrationTable(
		t, ctx, app, "Resume window notes", "resume_window_notes_table",
	)
	createV2IntegrationField(
		t, ctx, app, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"),
		"resume_window_notes_title",
	)
	definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := v2.ParseSchemaRevision(definition.Snapshot.SchemaRevision)
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
				definition.Snapshot.TableID,
				fmt.Sprintf("field-%02d", index),
			))),
		)
		job.Set(
			"progress_json",
			types.JSONRaw([]byte(`{"completed":0,"total":0}`)),
		)
		job.Set("error_json", nil)
		job.Set("source_event_id", fmt.Sprintf("resume-event-%02d", index))
		job.Set("source_table_id", definition.Snapshot.TableID)
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
		"vibetable_computation_dependencies",
	)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("source_table_id", "resume_source")
	record.Set("computed_field_id", "resume_formula")
	record.Set("computed_kind", "formula")
	record.Set("relation_field_id", "resume_relation")
	record.Set("target_table_id", "resume_target")
	record.Set("target_field_id", "resume_target_field")
	record.Set("path_json", types.JSONRaw(`[{"relationFieldId":"resume_relation"}]`))
	record.Set("definition_version", 1)
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
	fixture := createFormulaBackfillFixture(t, ctx, app, "Drift notes", "drift_notes")
	definition := fixture.definition
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
		ctx, definition.Snapshot.TableID, definition.Snapshot.SchemaRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	createV2IntegrationField(
		t, ctx, app, definition.Snapshot.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Renamed"),
		"drift_notes_schema_change",
	)
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
