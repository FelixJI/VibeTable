package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

const (
	formulaBackfillType = "formula_backfill"
	backfillBatchSize   = 100
	maxResumableJobs    = 32
)

type MutationKernel interface {
	Apply(context.Context, mutation.Request) (mutation.Receipt, error)
}

type TaskPublisher interface {
	PersistTaskChanged(context.Context, core.App, Snapshot) error
	PublishTaskChanged(context.Context, Snapshot) error
}

type Service struct {
	app            core.App
	kernel         MutationKernel
	publisher      TaskPublisher
	dataPublisher  DataPublisher
	runContext     context.Context
	cancelRuns     context.CancelFunc
	publishContext context.Context
	cancelPublish  context.CancelFunc
	runWait        sync.WaitGroup
	dispatchMu     sync.Mutex

	mu        sync.Mutex
	running   map[string]struct{}
	scheduled map[string]struct{}
	cancelled map[string]struct{}
	active    int
	stopping  bool
}

type Option func(*Service)

func WithTaskPublisher(publisher TaskPublisher) Option {
	return func(service *Service) {
		service.publisher = publisher
	}
}

func WithDataPublisher(publisher DataPublisher) Option {
	return func(service *Service) {
		service.dataPublisher = publisher
	}
}

func WithRunContext(ctx context.Context) Option {
	return func(service *Service) {
		if ctx != nil {
			service.runContext = ctx
		}
	}
}

type Snapshot struct {
	JobID          string    `json:"jobId"`
	Type           string    `json:"type"`
	State          string    `json:"state"`
	TableID        string    `json:"tableId"`
	SchemaRevision string    `json:"schemaRevision"`
	Cursor         Cursor    `json:"cursor"`
	Progress       Progress  `json:"progress"`
	Error          *JobError `json:"error,omitempty"`
}

type Cursor struct {
	LastRecordID string `json:"lastRecordId"`
}

type Progress struct {
	Completed int `json:"completed"`
	Total     int `json:"total"`
}

type JobError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (err *JobError) Error() string {
	return err.Code + ": " + err.Message
}

func New(
	app core.App,
	kernel MutationKernel,
	options ...Option,
) *Service {
	service := &Service{
		app: app, kernel: kernel,
		running:    map[string]struct{}{},
		scheduled:  map[string]struct{}{},
		cancelled:  map[string]struct{}{},
		runContext: context.Background(),
	}
	for _, option := range options {
		option(service)
	}
	service.runContext, service.cancelRuns = context.WithCancel(service.runContext)
	service.publishContext, service.cancelPublish = context.WithCancel(
		context.Background(),
	)
	return service
}

func (service *Service) Context() context.Context {
	return service.runContext
}

func (service *Service) PublishContext() context.Context {
	return service.publishContext
}

func (service *Service) SetKernel(kernel MutationKernel) {
	service.mu.Lock()
	service.kernel = kernel
	service.mu.Unlock()
}

func (service *Service) Start(jobID string) bool {
	service.mu.Lock()
	if service.stopping || service.active >= maxResumableJobs {
		service.mu.Unlock()
		return false
	}
	if _, exists := service.scheduled[jobID]; exists {
		service.mu.Unlock()
		return false
	}
	service.scheduled[jobID] = struct{}{}
	service.active++
	service.runWait.Add(1)
	runContext := service.runContext
	service.mu.Unlock()
	go func() {
		_ = service.Run(runContext, jobID)
		service.mu.Lock()
		delete(service.scheduled, jobID)
		service.active--
		stopping := service.stopping
		service.mu.Unlock()
		if !stopping {
			_ = service.dispatchPending(runContext)
		}
		service.runWait.Done()
	}()
	return true
}

// Shutdown prevents new background runs, cancels their work, and joins every
// run started by the service. The committed-publish lifecycle remains active
// during the join so a transaction that crossed its final cancellation check
// can durably drain realtime and fan-out work; it is cancelled only after no
// owned run can touch PocketBase storage.
func (service *Service) Shutdown() {
	service.mu.Lock()
	shouldCancel := !service.stopping
	service.stopping = true
	cancelRuns := service.cancelRuns
	service.mu.Unlock()
	if shouldCancel {
		cancelRuns()
	}
	service.runWait.Wait()
	service.cancelPublish()
}

func (service *Service) StartFormulaBackfill(
	ctx context.Context,
	tableID string,
	schemaRevision string,
) (Snapshot, error) {
	if tableID == "" || schemaRevision == "" {
		return Snapshot{}, jobError(
			"job.request.invalid",
			"tableId and schemaRevision are required",
			false,
		)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	definition, err := schemaapi.New(service.app).Describe(ctx, tableID)
	if err != nil {
		return Snapshot{}, err
	}
	if definition.SchemaRevision != schemaRevision {
		return Snapshot{}, jobError(
			"job.schema_revision_conflict",
			"formula backfill schema revision does not match",
			false,
		)
	}
	hasFormula := false
	for _, field := range definition.Fields {
		if field.Kind == schema.FieldKindFormula {
			hasFormula = true
			break
		}
	}
	if !hasFormula {
		return Snapshot{}, jobError(
			"job.formula.none",
			"table has no formula fields to recalculate",
			false,
		)
	}
	if existing, ok := service.findExistingFormulaJob(
		ctx, tableID, schemaRevision,
	); ok {
		return existing, nil
	}
	total, err := service.recordCount(definition)
	if err != nil {
		return Snapshot{}, err
	}
	revision, _ := schema.ParseSchemaRevision(definition.SchemaRevision)
	collection, err := service.app.FindCollectionByNameOrId("vibetable_jobs")
	if err != nil {
		return Snapshot{}, jobError(
			"job.storage_failed", "job storage is unavailable", true,
		)
	}
	record := core.NewRecord(collection)
	record.Set("job_type", formulaBackfillType)
	record.Set("state", "queued")
	record.Set("schema_revision", revision)
	// The shared jobs collection uses these four fields as a composite
	// idempotency key. Formula backfills may legitimately run again after a
	// completed job, so use the fresh record identity for the event component
	// and keep every remaining component non-empty.
	record.Set("source_event_id", "formula_backfill_"+security.RandomString(15))
	record.Set("source_table_id", tableID)
	record.Set("relation_field_id", formulaBackfillType)
	record.Set("cursor_json", types.JSONRaw([]byte(`{"lastRecordId":""}`)))
	progressRaw, _ := json.Marshal(Progress{Completed: 0, Total: total})
	record.Set("progress_json", types.JSONRaw(progressRaw))
	record.Set("error_json", nil)
	cursorMeta, _ := json.Marshal(map[string]any{
		"tableId":      tableID,
		"lastRecordId": "",
	})
	record.Set("cursor_json", types.JSONRaw(cursorMeta))
	if err := service.app.Save(record); err != nil {
		return Snapshot{}, jobError(
			"job.storage_failed", "formula backfill job could not be created", true,
		)
	}
	snapshot, err := service.Get(ctx, record.Id)
	if err == nil {
		service.publish(ctx, snapshot)
	}
	return snapshot, err
}

func (service *Service) Run(
	ctx context.Context,
	jobID string,
) error {
	if !service.claim(jobID) {
		return jobError(
			"job.already_running", "job is already running", true,
		)
	}
	defer service.release(jobID)
	if err := ctx.Err(); err != nil {
		return err
	}
	record, snapshot, err := service.load(jobID)
	if err != nil {
		return err
	}
	if snapshot.State == "complete" {
		return nil
	}
	if snapshot.State == "cancelled" {
		return jobError(
			"job.cancelled",
			"cancelled job must be resumed before it can run",
			false,
		)
	}
	if snapshot.Type != formulaBackfillType &&
		snapshot.Type != formulaFanoutType {
		return jobError(
			"job.type.invalid", "job type is unsupported", false,
		)
	}
	record.Set("state", "running")
	record.Set("error_json", nil)
	if err := service.app.Save(record); err != nil {
		return jobError(
			"job.storage_failed", "job state could not be updated", true,
		)
	}
	if running, getErr := service.Get(ctx, jobID); getErr == nil {
		service.publish(ctx, running)
	}
	if service.cancelRequested(jobID) {
		return service.finishCancellation(ctx, record)
	}
	if snapshot.Type == formulaFanoutType {
		return service.runFanout(ctx, record, snapshot)
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if service.cancelRequested(jobID) {
			return service.finishCancellation(ctx, record)
		}
		definition, err := schemaapi.New(service.app).Describe(
			ctx, snapshot.TableID,
		)
		if err != nil {
			return service.failUnlessContextInterrupted(
				ctx, record, snapshot.TableID, err,
			)
		}
		if definition.SchemaRevision != snapshot.SchemaRevision {
			return service.fail(
				record,
				snapshot.TableID,
				jobError(
					"job.schema_revision_conflict",
					"schema changed during formula backfill",
					false,
				),
			)
		}
		tableMeta, err := service.app.FindFirstRecordByFilter(
			"vibetable_tables",
			"table_id={:table}",
			dbx.Params{"table": snapshot.TableID},
		)
		if err != nil {
			return service.failUnlessContextInterrupted(
				ctx, record, snapshot.TableID, err,
			)
		}
		collection, err := service.app.FindCollectionByNameOrId(
			tableMeta.GetString("collection_id"),
		)
		if err != nil {
			return service.failUnlessContextInterrupted(
				ctx, record, snapshot.TableID, err,
			)
		}
		filter := ""
		params := []dbx.Params{}
		if snapshot.Cursor.LastRecordID != "" {
			filter = "id>{:cursor}"
			params = append(params, dbx.Params{
				"cursor": snapshot.Cursor.LastRecordID,
			})
		}
		records, err := service.app.FindRecordsByFilter(
			collection, filter, "+id", backfillBatchSize, 0, params...,
		)
		if err != nil {
			return service.failUnlessContextInterrupted(
				ctx, record, snapshot.TableID, err,
			)
		}
		if len(records) == 0 {
			if err := service.markFormulaStatus(
				snapshot.TableID, "ready",
			); err != nil {
				return service.failUnlessContextInterrupted(
					ctx, record, snapshot.TableID, err,
				)
			}
			record.Set("state", "complete")
			record.Set("error_json", nil)
			completed, err := service.persistTerminal(ctx, record)
			if err != nil {
				return jobError(
					"job.storage_failed",
					"completed job state could not be saved",
					true,
				)
			}
			service.publish(ctx, completed)
			return nil
		}
		operations := make([]mutation.Operation, 0, len(records))
		for _, item := range records {
			recordID := item.Id
			operations = append(operations, mutation.Operation{
				Kind:     mutation.OperationUpdate,
				RecordID: &recordID,
				Values:   map[string]any{},
			})
		}
		batchStart := records[0].Id
		batchEnd := records[len(records)-1].Id
		_, err = service.kernel.Apply(ctx, mutation.Request{
			ContractVersion: mutation.ContractVersion,
			RequestID: fmt.Sprintf(
				"job_%s_%s_%s", jobID, batchStart, batchEnd,
			),
			IdempotencyKey: fmt.Sprintf(
				"job_%s_%s_%s", jobID, batchStart, batchEnd,
			),
			TableID:        snapshot.TableID,
			SchemaRevision: snapshot.SchemaRevision,
			Operations:     operations,
			Actor: mutation.Actor{
				Type: "system", ID: "formula-backfill",
			},
		})
		if err != nil {
			return service.failUnlessContextInterrupted(
				ctx, record, snapshot.TableID, err,
			)
		}
		// A cancellation can race with the atomic batch. Keep the durable
		// cursor at the previous batch boundary in that case. Resume will
		// replay this batch with the same idempotency key, so no duplicate
		// audit events or mutations are produced.
		if service.cancelRequested(jobID) {
			return service.finishCancellation(ctx, record)
		}
		snapshot.Cursor.LastRecordID = batchEnd
		snapshot.Progress.Completed += len(records)
		if snapshot.Progress.Completed > snapshot.Progress.Total {
			snapshot.Progress.Total = snapshot.Progress.Completed
		}
		if err := saveProgress(record, snapshot); err != nil {
			return service.fail(record, snapshot.TableID, err)
		}
		if err := service.app.Save(record); err != nil {
			return service.failUnlessContextInterrupted(
				ctx, record, snapshot.TableID, err,
			)
		}
		if progress, getErr := service.Get(ctx, jobID); getErr == nil {
			service.publish(ctx, progress)
		}
	}
}

func (service *Service) ResumePending(ctx context.Context) error {
	if err := service.recoverFormulaFanouts(ctx); err != nil {
		return err
	}
	if err := service.preparePendingRecovery(ctx); err != nil {
		return err
	}
	if err := service.dispatchPending(ctx); err != nil {
		return err
	}
	return service.ensureMissingFormulaBackfills(ctx)
}

// preparePendingRecovery is the one startup-wide scan. It both rejects a
// corrupted/unbounded recovery set and turns jobs left "running" by a stopped
// process back into dispatchable work. Steady-state dispatch never performs
// this scan.
func (service *Service) preparePendingRecovery(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	records, err := service.app.FindRecordsByFilter(
		"vibetable_jobs",
		"(job_type={:backfill} || job_type={:fanout}) && "+
			"(state='queued' || state='running')",
		"+id",
		maxRetainedDataEvents+1,
		0,
		dbx.Params{
			"backfill": formulaBackfillType,
			"fanout":   formulaFanoutType,
		},
	)
	if err != nil {
		return err
	}
	if len(records) > maxRetainedDataEvents {
		return jobError(
			"job.resume_limit",
			"pending job recovery exceeds the 10000 job limit",
			false,
		)
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if record.GetString("state") != "running" {
			continue
		}
		record.Set("state", "queued")
		if err := service.app.Save(record); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) dispatchPending(ctx context.Context) error {
	service.dispatchMu.Lock()
	defer service.dispatchMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	service.mu.Lock()
	if service.stopping {
		service.mu.Unlock()
		return nil
	}
	available := maxResumableJobs - service.active
	service.mu.Unlock()
	if available <= 0 {
		return nil
	}

	records, err := service.app.FindRecordsByFilter(
		"vibetable_jobs",
		"(job_type={:backfill} || job_type={:fanout}) && "+
			"state='queued'",
		"+id",
		maxResumableJobs,
		0,
		dbx.Params{
			"backfill": formulaBackfillType,
			"fanout":   formulaFanoutType,
		},
	)
	if err != nil {
		return err
	}
	for _, record := range records {
		if available == 0 {
			break
		}
		if service.Start(record.Id) {
			available--
		}
	}
	return nil
}

func (service *Service) Get(
	ctx context.Context,
	jobID string,
) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	_, snapshot, err := service.load(jobID)
	return snapshot, err
}

func (service *Service) Cancel(
	ctx context.Context,
	jobID string,
) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	record, snapshot, err := service.load(jobID)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.State == "complete" || snapshot.State == "cancelled" {
		return snapshot, nil
	}
	service.mu.Lock()
	service.cancelled[jobID] = struct{}{}
	service.mu.Unlock()
	record.Set("state", "cancelled")
	record.Set("error_json", nil)
	cancelled, err := service.persistTerminal(ctx, record)
	if err != nil {
		return Snapshot{}, jobError(
			"job.storage_failed",
			"cancelled job state could not be saved",
			true,
		)
	}
	service.publish(ctx, cancelled)
	return cancelled, nil
}

func (service *Service) Resume(
	ctx context.Context,
	jobID string,
) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	record, snapshot, err := service.load(jobID)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.State != "cancelled" {
		return Snapshot{}, jobError(
			"job.not_cancelled",
			"only a cancelled job can be resumed",
			false,
		)
	}
	service.mu.Lock()
	delete(service.cancelled, jobID)
	service.mu.Unlock()
	record.Set("state", "queued")
	record.Set("error_json", nil)
	if err := service.app.Save(record); err != nil {
		return Snapshot{}, jobError(
			"job.storage_failed",
			"resumed job state could not be saved",
			true,
		)
	}
	queued, err := service.Get(ctx, jobID)
	if err == nil {
		service.publish(ctx, queued)
	}
	return queued, err
}

func (service *Service) cancelRequested(jobID string) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	_, ok := service.cancelled[jobID]
	return ok
}

func (service *Service) finishCancellation(
	ctx context.Context,
	record *core.Record,
) error {
	record.Set("state", "cancelled")
	record.Set("error_json", nil)
	cancelled, err := service.persistTerminal(ctx, record)
	if err != nil {
		return jobError(
			"job.storage_failed",
			"cancelled job state could not be saved",
			true,
		)
	}
	service.publish(ctx, cancelled)
	return nil
}

func (service *Service) load(
	jobID string,
) (*core.Record, Snapshot, error) {
	record, err := service.app.FindRecordById("vibetable_jobs", jobID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Snapshot{}, jobError(
				"job.not_found", "job was not found", false,
			)
		}
		return nil, Snapshot{}, jobError(
			"job.storage_failed", "job could not be read", true,
		)
	}
	snapshot, err := snapshotFromRecord(record)
	if err != nil {
		return nil, Snapshot{}, err
	}
	return record, snapshot, nil
}

func snapshotFromRecord(record *core.Record) (Snapshot, error) {
	cursorRaw, _ := json.Marshal(record.GetRaw("cursor_json"))
	progressRaw, _ := json.Marshal(record.GetRaw("progress_json"))
	var cursorEnvelope struct {
		TableID      string `json:"tableId"`
		LastRecordID string `json:"lastRecordId"`
	}
	var progress Progress
	if json.Unmarshal(cursorRaw, &cursorEnvelope) != nil ||
		json.Unmarshal(progressRaw, &progress) != nil ||
		cursorEnvelope.TableID == "" {
		return Snapshot{}, jobError(
			"job.storage_corrupt", "job state is invalid", false,
		)
	}
	var storedError *JobError
	if record.GetRaw("error_json") != nil {
		errorRaw, _ := json.Marshal(record.GetRaw("error_json"))
		var decoded JobError
		if json.Unmarshal(errorRaw, &decoded) == nil && decoded.Code != "" {
			storedError = &decoded
		}
	}
	revision := int64(record.GetFloat("schema_revision"))
	return Snapshot{
		JobID:          record.Id,
		Type:           record.GetString("job_type"),
		State:          record.GetString("state"),
		TableID:        cursorEnvelope.TableID,
		SchemaRevision: schema.FormatSchemaRevision(revision),
		Cursor:         Cursor{LastRecordID: cursorEnvelope.LastRecordID},
		Progress:       progress,
		Error:          storedError,
	}, nil
}

func (service *Service) findExistingFormulaJob(
	ctx context.Context,
	tableID string,
	schemaRevision string,
) (Snapshot, bool) {
	revision, err := schema.ParseSchemaRevision(schemaRevision)
	if err != nil {
		return Snapshot{}, false
	}
	records, err := service.app.FindRecordsByFilter(
		"vibetable_jobs",
		"job_type={:type} && schema_revision={:revision} && "+
			"(state='queued' || state='running' || state='cancelled')",
		"+id",
		maxResumableJobs+1,
		0,
		dbx.Params{
			"type":     formulaBackfillType,
			"revision": revision,
		},
	)
	if err != nil || len(records) > maxResumableJobs {
		return Snapshot{}, false
	}
	for _, record := range records {
		snapshot, loadErr := service.Get(ctx, record.Id)
		if loadErr == nil && snapshot.TableID == tableID {
			return snapshot, true
		}
	}
	return Snapshot{}, false
}

func (service *Service) ensureMissingFormulaBackfills(ctx context.Context) error {
	records, err := service.app.FindRecordsByFilter(
		"vibetable_formulas",
		"status='backfilling'",
		"+id",
		1001,
		0,
	)
	if err != nil {
		return err
	}
	if len(records) > 1000 {
		return jobError(
			"job.resume_limit",
			"missing formula backfill recovery exceeds the 1000 formula limit",
			true,
		)
	}
	tableIDs := map[string]struct{}{}
	for _, record := range records {
		if tableID := record.GetString("table_id"); tableID != "" {
			tableIDs[tableID] = struct{}{}
		}
	}
	for tableID := range tableIDs {
		definition, describeErr := schemaapi.New(service.app).Describe(
			ctx, tableID,
		)
		if describeErr != nil {
			return describeErr
		}
		snapshot, startErr := service.StartFormulaBackfill(
			ctx, tableID, definition.SchemaRevision,
		)
		if startErr != nil {
			return startErr
		}
		if snapshot.State != "queued" {
			continue
		}
		service.Start(snapshot.JobID)
	}
	return nil
}

func (service *Service) fail(
	record *core.Record,
	tableID string,
	cause error,
) error {
	structured := sanitizeError(cause)
	record.Set("state", "failed")
	raw, _ := json.Marshal(structured)
	record.Set("error_json", types.JSONRaw(raw))
	failed, persistErr := service.persistTerminal(
		context.Background(),
		record,
	)
	_ = service.markFormulaStatus(tableID, "failed")
	if persistErr == nil {
		service.publish(context.Background(), failed)
	}
	return structured
}

func (service *Service) persistTerminal(
	ctx context.Context,
	record *core.Record,
) (Snapshot, error) {
	snapshot, err := snapshotFromRecord(record)
	if err != nil {
		return Snapshot{}, err
	}
	err = service.app.RunInTransaction(func(txApp core.App) error {
		if err := txApp.Save(record); err != nil {
			return err
		}
		if service.publisher != nil {
			return service.publisher.PersistTaskChanged(
				ctx,
				txApp,
				snapshot,
			)
		}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (service *Service) failUnlessContextInterrupted(
	ctx context.Context,
	record *core.Record,
	tableID string,
	cause error,
) error {
	if ctx != nil {
		switch ctx.Err() {
		case context.Canceled:
			return context.Canceled
		case context.DeadlineExceeded:
			return context.DeadlineExceeded
		}
	}
	return service.fail(record, tableID, cause)
}

func (service *Service) publish(ctx context.Context, snapshot Snapshot) {
	if service.publisher != nil {
		_ = service.publisher.PublishTaskChanged(ctx, snapshot)
	}
}

func (service *Service) markFormulaStatus(
	tableID string,
	status string,
) error {
	return service.app.RunInTransaction(func(txApp core.App) error {
		formulas, err := txApp.FindRecordsByFilter(
			"vibetable_formulas",
			"table_id={:table}",
			"",
			0,
			0,
			dbx.Params{"table": tableID},
		)
		if err != nil {
			return err
		}
		for _, formula := range formulas {
			formula.Set("status", status)
			if err := txApp.Save(formula); err != nil {
				return err
			}
		}
		table, err := txApp.FindFirstRecordByFilter(
			"vibetable_tables",
			"table_id={:table}",
			dbx.Params{"table": tableID},
		)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(table.GetRaw("definition_json"))
		var definition schema.TableDefinition
		if json.Unmarshal(raw, &definition) != nil {
			return errors.New("stored table definition is invalid")
		}
		for index := range definition.Fields {
			if definition.Fields[index].Kind == schema.FieldKindFormula &&
				definition.Fields[index].Formula != nil {
				spec := *definition.Fields[index].Formula
				spec.Status = status
				definition.Fields[index].Formula = &spec
			}
		}
		raw, err = json.Marshal(definition)
		if err != nil {
			return err
		}
		table.Set("definition_json", types.JSONRaw(raw))
		return txApp.Save(table)
	})
}

func (service *Service) recordCount(
	definition schema.TableDefinition,
) (int, error) {
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables",
		"table_id={:table}",
		dbx.Params{"table": definition.TableID},
	)
	if err != nil {
		return 0, jobError(
			"job.storage_failed", "table storage could not be read", true,
		)
	}
	collection, err := service.app.FindCollectionByNameOrId(
		meta.GetString("collection_id"),
	)
	if err != nil {
		return 0, jobError(
			"job.storage_failed", "table storage could not be read", true,
		)
	}
	query := "SELECT COUNT(*) FROM `" +
		strings.ReplaceAll(collection.Name, "`", "``") + "`"
	var total int
	if err := service.app.DB().NewQuery(query).Row(&total); err != nil {
		return 0, jobError(
			"job.storage_failed", "table rows could not be counted", true,
		)
	}
	return total, nil
}

func (service *Service) claim(jobID string) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	if _, exists := service.running[jobID]; exists {
		return false
	}
	service.running[jobID] = struct{}{}
	return true
}

func (service *Service) release(jobID string) {
	service.mu.Lock()
	delete(service.running, jobID)
	service.mu.Unlock()
}

func saveProgress(record *core.Record, snapshot Snapshot) error {
	cursorRaw, err := json.Marshal(map[string]any{
		"tableId":      snapshot.TableID,
		"lastRecordId": snapshot.Cursor.LastRecordID,
	})
	if err != nil {
		return err
	}
	progressRaw, err := json.Marshal(snapshot.Progress)
	if err != nil {
		return err
	}
	record.Set("cursor_json", types.JSONRaw(cursorRaw))
	record.Set("progress_json", types.JSONRaw(progressRaw))
	return nil
}

func sanitizeError(err error) *JobError {
	var existing *JobError
	if errors.As(err, &existing) {
		return existing
	}
	var productErr *mutation.ProductError
	if errors.As(err, &productErr) {
		return &JobError{
			Code:      productErr.Code,
			Message:   productErr.Message,
			Retryable: productErr.Retryable,
		}
	}
	return jobError(
		"job.execution_failed",
		"job execution failed",
		true,
	)
}

func jobError(code, message string, retryable bool) *JobError {
	return &JobError{Code: code, Message: message, Retryable: retryable}
}
