package fieldchange

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/fieldprojection"
	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const (
	fieldMigrationJobType = "field_migration"
	migrationBatchSize    = 100
)

type MigrationOption func(*MigrationService)

type BackfillWriter func(
	ctx context.Context,
	plan v2.FieldChangePlan,
	jobID string,
	recordID string,
	value any,
) error

func WithMigrationFaultInjector(inject func(phase v2.MigrationPhase) error) MigrationOption {
	return func(service *MigrationService) {
		service.fault = inject
	}
}

func WithBackfillWriter(writer BackfillWriter) MigrationOption {
	return func(service *MigrationService) {
		service.backfill = writer
	}
}

func WithMigrationContext(ctx context.Context) MigrationOption {
	return func(service *MigrationService) {
		if ctx == nil {
			return
		}
		service.cancelContext()
		service.ctx, service.cancelContext = context.WithCancel(ctx)
	}
}

func WithMigrationLogger(logger *slog.Logger) MigrationOption {
	return func(service *MigrationService) {
		if logger != nil {
			service.logger = logger
		}
	}
}

type MigrationService struct {
	app           core.App
	store         PlanStore
	fault         func(v2.MigrationPhase) error
	backfill      BackfillWriter
	logger        *slog.Logger
	ctx           context.Context
	cancelContext context.CancelFunc
	wg            sync.WaitGroup
	mu            sync.Mutex
	active        map[string]struct{}
	cancel        map[string]struct{}
}

func NewMigrationService(
	app core.App,
	store PlanStore,
	options ...MigrationOption,
) *MigrationService {
	if store == nil {
		store = NewPocketBasePlanStore(app)
	}
	service := &MigrationService{
		app: app, store: store,
		active: map[string]struct{}{}, cancel: map[string]struct{}{},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	service.ctx, service.cancelContext = context.WithCancel(context.Background())
	for _, option := range options {
		option(service)
	}
	return service
}

func (service *MigrationService) Enqueue(
	ctx context.Context,
	app core.App,
	plan v2.FieldChangePlan,
	operationID string,
) (string, error) {
	if !plan.CreatesMigration || plan.Before == nil || plan.After == nil {
		return "", productError(
			"field.migration.invalid_plan", "planId",
			"migration plan must contain before and after definitions", nil,
		)
	}
	existing, err := app.FindFirstRecordByFilter(
		"vibetable_jobs",
		"job_type={:type} && plan_id={:plan}",
		dbx.Params{"type": fieldMigrationJobType, "plan": plan.PlanID},
	)
	if err == nil {
		return existing.Id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("find field migration job: %w", err)
	}
	collection, err := app.FindCollectionByNameOrId("vibetable_jobs")
	if err != nil {
		return "", fmt.Errorf("load field migration jobs: %w", err)
	}
	revision, err := schema.ParseSchemaRevision(plan.ExpectedSchemaRev)
	if err != nil {
		return "", err
	}
	beforeRaw, _ := json.Marshal(plan.Before)
	afterRaw, _ := json.Marshal(plan.After)
	cursorRaw, _ := json.Marshal(map[string]any{
		"tableId": plan.Intent.TableID, "lastRecordId": "",
		"operationId": operationID,
	})
	progressRaw, _ := json.Marshal(map[string]any{"completed": 0, "total": plan.Impact.Records})
	record := core.NewRecord(collection)
	record.Set("job_type", fieldMigrationJobType)
	record.Set("state", "queued")
	record.Set("schema_revision", revision)
	// vibetable_jobs has a shared idempotency key across all job families.
	// Populate every key component so successive field migrations don't all
	// collide on the legacy empty-string tuple.
	record.Set("source_event_id", operationID)
	record.Set("source_table_id", plan.Intent.TableID)
	record.Set("relation_field_id", plan.Before.Identity.FieldID)
	record.Set("cursor_json", types.JSONRaw(cursorRaw))
	record.Set("progress_json", types.JSONRaw(progressRaw))
	record.Set("error_json", nil)
	record.Set("plan_id", plan.PlanID)
	record.Set("field_id", plan.Before.Identity.FieldID)
	record.Set("before_definition_json", types.JSONRaw(beforeRaw))
	record.Set("after_definition_json", types.JSONRaw(afterRaw))
	record.Set("phase", v2.MigrationPlanned)
	record.Set("shadow_identities_json", types.JSONRaw([]byte(`{}`)))
	record.Set("write_lock_owner", "operation:"+operationID)
	record.Set("cleanup_state", "pending")
	if err := app.Save(record); err != nil {
		return "", fmt.Errorf("save field migration job: %w", err)
	}
	return record.Id, nil
}

func (service *MigrationService) Start(jobID string) bool {
	service.mu.Lock()
	if service.ctx.Err() != nil {
		service.mu.Unlock()
		return false
	}
	if _, exists := service.active[jobID]; exists {
		service.mu.Unlock()
		return false
	}
	service.active[jobID] = struct{}{}
	service.wg.Add(1)
	service.mu.Unlock()
	go func() {
		defer func() {
			service.mu.Lock()
			delete(service.active, jobID)
			service.mu.Unlock()
			service.wg.Done()
		}()
		_ = service.Run(service.ctx, jobID)
	}()
	return true
}

func (service *MigrationService) Shutdown() {
	if service == nil {
		return
	}
	service.mu.Lock()
	service.cancelContext()
	service.mu.Unlock()
	service.wg.Wait()
}

func (service *MigrationService) Run(ctx context.Context, jobID string) (runErr error) {
	started := time.Now()
	defer func() {
		attributes := []any{
			"event", "field_migration_finished",
			"job_id", jobID,
			"duration_ms", time.Since(started).Milliseconds(),
		}
		status, statusErr := service.Status(context.Background(), jobID)
		if statusErr == nil {
			attributes = append(
				attributes,
				"plan_id", status.PlanID,
				"phase", status.Phase,
				"records_scanned", status.Processed,
				"records_total", status.Total,
			)
			if status.Error != nil {
				attributes = append(attributes, "failure_code", status.Error.Code)
			}
		}
		if runErr != nil {
			service.logger.Warn(
				"field migration finished",
				append(attributes, "outcome", "failed", "error", runErr)...,
			)
			return
		}
		service.logger.Info(
			"field migration finished",
			append(attributes, "outcome", "finished")...,
		)
	}()
	record, plan, state, err := service.load(ctx, jobID)
	if err != nil {
		return err
	}
	if state.Phase == v2.MigrationCompleted ||
		state.Phase == v2.MigrationCancelled ||
		state.Phase == v2.MigrationRolledBack {
		return nil
	}
	if record.GetString("state") == "cancelling" {
		return service.cancelAndClean(record, plan)
	}
	if state.Phase == v2.MigrationCleaning {
		switch record.GetString("cleanup_state") {
		case "rollback_retry":
			return service.resumeRollbackCleanup(record, plan)
		case "cancel_retry":
			return service.cancelAndClean(record, plan)
		}
		_, collection, collectionErr := migrationCollection(
			service.app, plan.Intent.TableID,
		)
		if collectionErr != nil {
			return collectionErr
		}
		if cleanupErr := service.cleanupRetired(collection, plan); cleanupErr != nil {
			record.Set("state", "failed")
			record.Set("cleanup_state", "retry")
			_ = service.app.Save(record)
			return cleanupErr
		}
		record.Set("cleanup_state", "complete")
		record.Set("state", "complete")
		record.Set("phase", v2.MigrationCompleted)
		record.Set("write_lock_owner", "")
		record.Set("error_json", nil)
		return service.app.Save(record)
	}
	if !VerifyPlanHash(plan) {
		return service.failAndClean(
			ctx, record, plan,
			productError(
				"field.migration.plan_corrupt", "planId",
				"stored migration plan hash is invalid", nil,
			),
		)
	}
	if err := service.setPhase(record, v2.MigrationValidating); err != nil {
		return err
	}
	if err := service.inject(v2.MigrationValidating); err != nil {
		return service.failAndClean(ctx, record, plan, err)
	}
	revisions, err := NewCatalog(service.app).Revisions(ctx, plan.Intent.TableID)
	if err != nil || revisions.Schema != plan.ExpectedSchemaRev {
		if err == nil {
			err = productError(
				"field.change.schema_conflict", "expectedSchemaRevision",
				"schema changed before migration", nil,
			)
		}
		return service.failAndClean(ctx, record, plan, err)
	}
	table, collection, err := migrationCollection(service.app, plan.Intent.TableID)
	if err != nil {
		return service.failAndClean(ctx, record, plan, err)
	}
	total, err := service.app.CountRecords(collection)
	if err != nil {
		return service.failAndClean(ctx, record, plan, err)
	}
	if err := service.updateProgress(record, state.Processed, int64(total), state.LastRecordID); err != nil {
		return service.failAndClean(ctx, record, plan, err)
	}
	if service.cancelRequested(jobID) {
		return service.cancelBeforeSwitch(record)
	}

	if plan.Intent.Action == v2.ActionBackfill {
		return service.runBackfill(ctx, record, plan, collection, state)
	}
	shadow := shadowNames(jobID, *plan.After)
	if err := service.ensureShadow(record, collection, *plan.After, shadow); err != nil {
		return service.failAndClean(ctx, record, plan, err)
	}
	if err := service.setPhase(record, v2.MigrationCopying); err != nil {
		return err
	}
	if err := service.inject(v2.MigrationCopying); err != nil {
		return service.failAndClean(ctx, record, plan, err)
	}
	processed, cursor, err := service.copyRows(
		ctx, record, plan, collection, shadow, state.Processed, state.LastRecordID,
	)
	if err != nil {
		return service.failAndClean(ctx, record, plan, err)
	}
	if err := service.updateProgress(record, processed, int64(total), cursor); err != nil {
		return service.failAndClean(ctx, record, plan, err)
	}
	if service.cancelRequested(jobID) {
		return service.cancelAndClean(record, plan)
	}
	if err := service.setPhase(record, v2.MigrationVerifying); err != nil {
		return err
	}
	if err := service.inject(v2.MigrationVerifying); err != nil {
		return service.failAndClean(ctx, record, plan, err)
	}
	if err := service.verifyRows(ctx, plan, collection, shadow); err != nil {
		return service.failAndClean(ctx, record, plan, err)
	}
	if service.cancelRequested(jobID) {
		return service.cancelAndClean(record, plan)
	}
	if err := service.beginSwitch(record); err != nil {
		var productErr *ProductError
		if errors.As(err, &productErr) &&
			productErr.Code == "field.migration.cancel_requested" {
			return service.cancelAndClean(record, plan)
		}
		return err
	}
	if err := service.inject(v2.MigrationSwitching); err != nil {
		return service.failAndClean(ctx, record, plan, err)
	}
	if err := service.switchAuthority(
		ctx, table, collection, record, plan, shadow,
	); err != nil {
		return service.failAndClean(ctx, record, plan, err)
	}
	// switchAuthority persists MigrationCleaning in the same transaction as
	// the schema/metadata authority switch. This closes the crash window where
	// recovery could otherwise mistake the new provider for disposable shadow.
	record.Set("phase", v2.MigrationCleaning)
	if err := service.inject(v2.MigrationCleaning); err != nil {
		// Authority is already switched; preserve the new field and make
		// cleanup resumable instead of rolling back through committed writes.
		record.Set("state", "failed")
		record.Set("cleanup_state", "retry")
		return service.app.Save(record)
	}
	if err := service.cleanupRetired(collection, plan); err != nil {
		record.Set("state", "failed")
		record.Set("cleanup_state", "retry")
		_ = service.app.Save(record)
		return err
	}
	record.Set("cleanup_state", "complete")
	record.Set("state", "complete")
	record.Set("phase", v2.MigrationCompleted)
	record.Set("write_lock_owner", "")
	record.Set("error_json", nil)
	if err := service.app.Save(record); err != nil {
		return err
	}
	return service.inject(v2.MigrationCompleted)
}

func (service *MigrationService) Status(
	ctx context.Context,
	jobID string,
) (v2.MigrationStatus, error) {
	record, _, state, err := service.load(ctx, jobID)
	if err != nil {
		return v2.MigrationStatus{}, err
	}
	state.JobID = record.Id
	return state.status(), nil
}

func (service *MigrationService) Cancel(
	ctx context.Context,
	jobID string,
) (v2.MigrationStatus, error) {
	record, plan, state, err := service.load(ctx, jobID)
	if err != nil {
		return v2.MigrationStatus{}, err
	}
	if state.Phase == v2.MigrationSwitching ||
		state.Phase == v2.MigrationCleaning ||
		state.Phase == v2.MigrationCompleted {
		return v2.MigrationStatus{}, productError(
			"field.migration.cancel_too_late", "jobId",
			"migration can no longer be cancelled after switching begins", nil,
		)
	}
	if err := service.app.RunInTransaction(func(app core.App) error {
		current, findErr := app.FindRecordById("vibetable_jobs", jobID)
		if findErr != nil {
			return findErr
		}
		phase := v2.MigrationPhase(current.GetString("phase"))
		if phase == v2.MigrationSwitching ||
			phase == v2.MigrationCleaning ||
			phase == v2.MigrationCompleted {
			return productError(
				"field.migration.cancel_too_late", "jobId",
				"migration can no longer be cancelled after switching begins", nil,
			)
		}
		current.Set("state", "cancelling")
		return app.Save(current)
	}); err != nil {
		return v2.MigrationStatus{}, err
	}
	service.mu.Lock()
	service.cancel[jobID] = struct{}{}
	_, active := service.active[jobID]
	service.mu.Unlock()
	if !active {
		if err := service.cancelAndClean(record, plan); err != nil {
			return v2.MigrationStatus{}, err
		}
		return service.Status(ctx, jobID)
	}
	status := state.status()
	status.CanCancel = false
	return status, nil
}

func (service *MigrationService) ResumePending(ctx context.Context) error {
	records, err := service.app.FindRecordsByFilter(
		"vibetable_jobs",
		"job_type={:type} && (state='queued' || state='running' || state='cancelling' || "+
			"(state='failed' && cleanup_state='retry'))",
		"+id",
		33,
		0,
		dbx.Params{"type": fieldMigrationJobType},
	)
	if err != nil {
		return err
	}
	if len(records) > 32 {
		return productError(
			"field.migration.resume_limit", "",
			"pending field migrations exceed the recovery limit", nil,
		)
	}
	for _, record := range records {
		if record.GetString("state") == "running" {
			record.Set("state", "queued")
			if err := service.app.Save(record); err != nil {
				return err
			}
		}
		service.Start(record.Id)
	}
	return nil
}

func CheckTableWriteFence(
	ctx context.Context,
	app core.App,
	tableID string,
) error {
	if app == nil {
		return nil
	}
	records, err := app.FindRecordsByFilter(
		"vibetable_jobs",
		"job_type={:type} && write_lock_owner!='' && "+
			"(state='queued' || state='running')",
		"+id",
		33,
		0,
		dbx.Params{"type": fieldMigrationJobType},
	)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, _ := json.Marshal(record.GetRaw("cursor_json"))
		var cursor struct {
			TableID string `json:"tableId"`
		}
		if json.Unmarshal(raw, &cursor) == nil && cursor.TableID == tableID {
			return productError(
				"field.migration.write_locked", "tableId",
				"table is temporarily write-locked by a field migration",
				map[string]any{"jobId": record.Id},
			)
		}
	}
	return nil
}

type migrationState struct {
	JobID        string
	PlanID       string
	Phase        v2.MigrationPhase
	Processed    int64
	Total        int64
	LastRecordID string
	UpdatedAt    string
	Error        *v2.Diagnostic
}

func (state migrationState) status() v2.MigrationStatus {
	canCancel := state.Phase != v2.MigrationSwitching &&
		state.Phase != v2.MigrationCleaning &&
		state.Phase != v2.MigrationCompleted &&
		state.Phase != v2.MigrationCancelled &&
		state.Phase != v2.MigrationRolledBack
	return v2.MigrationStatus{
		Contract: v2.Contract, JobID: state.JobID, PlanID: state.PlanID,
		Phase: state.Phase, Processed: state.Processed, Total: state.Total,
		CanCancel: canCancel, Error: state.Error, UpdatedAt: state.UpdatedAt,
	}
}

func (service *MigrationService) load(
	ctx context.Context,
	jobID string,
) (*core.Record, v2.FieldChangePlan, migrationState, error) {
	if err := ctx.Err(); err != nil {
		return nil, v2.FieldChangePlan{}, migrationState{}, err
	}
	record, err := service.app.FindRecordById("vibetable_jobs", jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, v2.FieldChangePlan{}, migrationState{}, productError(
			"field.migration.not_found", "jobId", "field migration job was not found", nil,
		)
	}
	if err != nil || record.GetString("job_type") != fieldMigrationJobType {
		return nil, v2.FieldChangePlan{}, migrationState{}, productError(
			"field.migration.not_found", "jobId", "field migration job was not found", nil,
		)
	}
	plan, err := service.store.Load(ctx, record.GetString("plan_id"))
	if err != nil || plan == nil {
		return nil, v2.FieldChangePlan{}, migrationState{}, productError(
			"field.migration.plan_missing", "planId",
			"field migration plan could not be loaded", nil,
		)
	}
	cursorRaw, _ := json.Marshal(record.GetRaw("cursor_json"))
	progressRaw, _ := json.Marshal(record.GetRaw("progress_json"))
	var cursor struct {
		LastRecordID string `json:"lastRecordId"`
	}
	var progress struct {
		Completed int64 `json:"completed"`
		Total     int64 `json:"total"`
	}
	if json.Unmarshal(cursorRaw, &cursor) != nil ||
		json.Unmarshal(progressRaw, &progress) != nil {
		return nil, v2.FieldChangePlan{}, migrationState{}, productError(
			"field.migration.state_corrupt", "jobId",
			"field migration state is invalid", nil,
		)
	}
	state := migrationState{
		JobID: record.Id, PlanID: plan.PlanID,
		Phase:     v2.MigrationPhase(record.GetString("phase")),
		Processed: progress.Completed, Total: progress.Total,
		LastRecordID: cursor.LastRecordID,
		UpdatedAt: record.GetDateTime("updated").Time().
			UTC().Format(time.RFC3339Nano),
	}
	if record.GetRaw("error_json") != nil {
		raw, _ := json.Marshal(record.GetRaw("error_json"))
		var diagnostic v2.Diagnostic
		if json.Unmarshal(raw, &diagnostic) == nil && diagnostic.Code != "" {
			state.Error = &diagnostic
		}
	}
	return record, *plan, state, nil
}

type migrationShadow struct {
	Value           string `json:"value"`
	Presence        string `json:"presence"`
	RetiredValue    string `json:"retiredValue"`
	RetiredPresence string `json:"retiredPresence"`
}

func shadowNames(jobID string, after v2.FieldDefinition) migrationShadow {
	token := strings.ToLower(strings.ReplaceAll(jobID, "_", ""))
	if len(token) > 18 {
		token = token[len(token)-18:]
	}
	shadow := migrationShadow{
		Value:           "f_mig_" + token,
		RetiredValue:    "f_ret_" + token,
		RetiredPresence: "__vt_ret_has_" + token,
	}
	if after.Value.Presence.Mode == v2.PresenceCompanion {
		shadow.Presence = "__vt_has_" + shadow.Value
	}
	return shadow
}

func (service *MigrationService) ensureShadow(
	job *core.Record,
	collection *core.Collection,
	after v2.FieldDefinition,
	shadow migrationShadow,
) error {
	if collection.Fields.GetById(after.Identity.ProviderFieldID) != nil {
		return nil
	}
	compiledDefinition := cloneDefinition(&after)
	compiledDefinition.Identity.PhysicalName = shadow.Value
	if compiledDefinition.Value.Presence.Mode == v2.PresenceCompanion {
		compiledDefinition.Value.Presence.PhysicalName = shadow.Presence
	}
	compiled, err := v2.CompileField(*compiledDefinition, relationResolver(service.app))
	if err != nil {
		return err
	}
	collection.Fields.Add(compiled.Value)
	if compiled.Presence != nil {
		collection.Fields.Add(compiled.Presence)
	}
	raw, _ := json.Marshal(shadow)
	job.Set("shadow_identities_json", types.JSONRaw(raw))
	if err := service.app.Save(job); err != nil {
		return err
	}
	return service.app.Save(collection)
}

func (service *MigrationService) copyRows(
	ctx context.Context,
	job *core.Record,
	plan v2.FieldChangePlan,
	collection *core.Collection,
	shadow migrationShadow,
	processed int64,
	cursor string,
) (int64, string, error) {
	for {
		filter := ""
		params := []dbx.Params{}
		if cursor != "" {
			filter = "id>{:cursor}"
			params = append(params, dbx.Params{"cursor": cursor})
		}
		records, err := service.app.FindRecordsByFilter(
			collection, filter, "+id", migrationBatchSize, 0, params...,
		)
		if err != nil {
			return processed, cursor, err
		}
		if len(records) == 0 {
			return processed, cursor, nil
		}
		for _, record := range records {
			if service.cancelRequested(job.Id) {
				return processed, cursor, nil
			}
			value := (fieldprojection.Descriptor{
				Definition: *plan.Before,
			}).ProductValue(physicalValues(record, *plan.Before))
			converted, supplied, err := convertMigrationValue(
				*plan.Before, *plan.After, value, plan.Intent.ConversionRule,
			)
			if err != nil {
				return processed, cursor, productError(
					"field.migration.conversion_failed", "conversionRule",
					err.Error(), map[string]any{"recordId": record.Id},
				)
			}
			result, err := fieldvalue.New().NormalizeWrite(
				ctx, *plan.After, fieldvalue.Update,
				fieldvalue.Input{Supplied: supplied, Value: converted},
			)
			if err != nil {
				return processed, cursor, productError(
					"field.migration.conversion_failed", "value",
					err.Error(), map[string]any{"recordId": record.Id},
				)
			}
			for name, physical := range result.PhysicalValues {
				switch name {
				case plan.After.Identity.PhysicalName:
					record.Set(shadow.Value, physical)
				case plan.After.Value.Presence.PhysicalName:
					record.Set(shadow.Presence, physical)
				}
			}
			if err := service.app.Save(record); err != nil {
				return processed, cursor, err
			}
			processed++
			cursor = record.Id
		}
		if err := service.updateProgress(job, processed, 0, cursor); err != nil {
			return processed, cursor, err
		}
	}
}

func (service *MigrationService) verifyRows(
	ctx context.Context,
	plan v2.FieldChangePlan,
	collection *core.Collection,
	shadow migrationShadow,
) error {
	records, err := service.app.FindRecordsByFilter(collection, "", "+id", 0, 0)
	if err != nil {
		return err
	}
	seen := map[string]string{}
	for _, record := range records {
		physical := map[string]any{
			plan.After.Identity.PhysicalName: record.GetRaw(shadow.Value),
		}
		if shadow.Presence != "" {
			physical[plan.After.Value.Presence.PhysicalName] =
				record.GetBool(shadow.Presence)
		}
		value := (fieldprojection.Descriptor{Definition: *plan.After}).
			ProductValue(physical)
		if plan.After.Value.Required && value == nil {
			return productError(
				"field.migration.conversion_failed", "value",
				"required migrated value is missing",
				map[string]any{"recordId": record.Id},
			)
		}
		if plan.After.Constraints.Unique.Enabled {
			key, participates, keyErr := (fieldprojection.Descriptor{
				Definition: *plan.After,
			}).UniqueKey(physical)
			if keyErr != nil {
				return keyErr
			}
			if previous, duplicate := seen[key]; participates && duplicate {
				return productError(
					"field.unique.duplicate_values", "constraints.unique",
					"migrated values are not unique",
					map[string]any{
						"recordId": record.Id, "previousRecordId": previous,
					},
				)
			} else if participates {
				seen[key] = record.Id
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func (service *MigrationService) switchAuthority(
	ctx context.Context,
	table *core.Record,
	collection *core.Collection,
	job *core.Record,
	plan v2.FieldChangePlan,
	shadow migrationShadow,
) error {
	return service.app.RunInTransaction(func(app core.App) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		currentTable, err := NewCatalog(app).tableRecord(app, plan.Intent.TableID)
		if err != nil {
			return err
		}
		currentRevision, _ := storedInteger(currentTable.GetRaw("schema_revision"))
		expected, _ := schema.ParseSchemaRevision(plan.ExpectedSchemaRev)
		if currentRevision != expected {
			return productError(
				"field.change.schema_conflict", "expectedSchemaRevision",
				"schema changed while migration was copying", nil,
			)
		}
		currentCollection, err := app.FindCollectionByNameOrId(table.GetString("collection_id"))
		if err != nil {
			return err
		}
		oldValue := currentCollection.Fields.GetById(plan.Before.Identity.ProviderFieldID)
		newValue := currentCollection.Fields.GetById(plan.After.Identity.ProviderFieldID)
		if oldValue == nil || newValue == nil {
			return errors.New("migration provider fields are missing")
		}
		oldValue.SetName(shadow.RetiredValue)
		oldValue.SetHidden(true)
		newValue.SetName(plan.After.Identity.PhysicalName)
		if plan.Before.Value.Presence.ProviderFieldID != "" {
			if oldPresence := currentCollection.Fields.GetById(
				plan.Before.Value.Presence.ProviderFieldID,
			); oldPresence != nil {
				oldPresence.SetName(shadow.RetiredPresence)
				oldPresence.SetHidden(true)
			}
		}
		if plan.After.Value.Presence.ProviderFieldID != "" {
			newPresence := currentCollection.Fields.GetById(
				plan.After.Value.Presence.ProviderFieldID,
			)
			if newPresence == nil {
				return errors.New("migration presence field is missing")
			}
			newPresence.SetName(plan.After.Value.Presence.PhysicalName)
			newPresence.SetHidden(true)
		}
		removeFieldIndexes(currentCollection, plan.Before.Identity.PhysicalName)
		if index, ok, indexErr := v2.CompileUniqueIndex(
			currentCollection.Name, *plan.After,
		); indexErr != nil {
			return indexErr
		} else if ok {
			currentCollection.Indexes = append(currentCollection.Indexes, index)
		}
		if err := app.Save(currentCollection); err != nil {
			return err
		}
		if err := saveDefinitionMetadata(app, plan.Intent.TableID, *plan.After); err != nil {
			return err
		}
		if err := saveTableRevisionAndLegacy(ctx, app, plan, expected+1); err != nil {
			return err
		}
		audit, auditErr := app.FindFirstRecordByFilter(
			"vibetable_schema_audit",
			"migration_job_id={:job} || operation_id={:operation}",
			dbx.Params{
				"job": job.Id, "operation": migrationOperationID(job),
			},
		)
		if auditErr == nil {
			audit.Set("outcome", "applied")
			audit.Set("migration_job_id", job.Id)
			if err := app.Save(audit); err != nil {
				return err
			}
		}
		currentJob, err := app.FindRecordById("vibetable_jobs", job.Id)
		if err != nil {
			return err
		}
		currentJob.Set("state", "running")
		currentJob.Set("phase", v2.MigrationCleaning)
		currentJob.Set("cleanup_state", "pending")
		if err := app.Save(currentJob); err != nil {
			return err
		}
		return nil
	})
}

func (service *MigrationService) cleanupRetired(
	collection *core.Collection,
	plan v2.FieldChangePlan,
) error {
	current, err := service.app.FindCollectionByNameOrId(collection.Id)
	if err != nil {
		return err
	}
	current.Fields.RemoveById(plan.Before.Identity.ProviderFieldID)
	if plan.Before.Value.Presence.ProviderFieldID != "" {
		current.Fields.RemoveById(plan.Before.Value.Presence.ProviderFieldID)
	}
	return service.app.Save(current)
}

func (service *MigrationService) runBackfill(
	ctx context.Context,
	job *core.Record,
	plan v2.FieldChangePlan,
	collection *core.Collection,
	state migrationState,
) error {
	if plan.After == nil || !plan.After.Value.Default.Enabled {
		return service.failAndClean(
			ctx, job, plan,
			productError(
				"field.migration.backfill_default_required", "draft.value.default",
				"backfill requires an enabled default", nil,
			),
		)
	}
	if err := service.setPhase(job, v2.MigrationCopying); err != nil {
		return err
	}
	records, err := service.app.FindRecordsByFilter(collection, "", "+id", 0, 0)
	if err != nil {
		return service.failAndClean(ctx, job, plan, err)
	}
	processed := state.Processed
	defaultClock := job.GetDateTime("created").Time().UTC()
	defaultKernel := fieldvalue.New(fieldvalue.WithClock(func() time.Time {
		return defaultClock
	}))
	for _, record := range records {
		if record.Id <= state.LastRecordID {
			continue
		}
		if service.cancelRequested(job.Id) {
			return service.cancelBeforeSwitch(job)
		}
		if (fieldprojection.Descriptor{Definition: *plan.Before}).
			ProductValue(physicalValues(record, *plan.Before)) == nil {
			result, normalizeErr := defaultKernel.NormalizeWrite(
				ctx, *plan.After, fieldvalue.Insert,
				fieldvalue.Input{Supplied: false},
			)
			if normalizeErr != nil {
				return service.failAndClean(ctx, job, plan, normalizeErr)
			}
			if service.backfill == nil {
				return service.failAndClean(
					ctx, job, plan,
					productError(
						"field.migration.backfill_unavailable", "",
						"authoritative backfill writer is unavailable", nil,
					),
				)
			}
			if err := service.backfill(
				ctx, plan, job.Id, record.Id, result.ProductValue,
			); err != nil {
				return service.failAndClean(ctx, job, plan, err)
			}
		}
		processed++
		if err := service.updateProgress(
			job, processed, int64(len(records)), record.Id,
		); err != nil {
			return err
		}
	}
	job.Set("state", "complete")
	job.Set("phase", v2.MigrationCompleted)
	job.Set("write_lock_owner", "")
	job.Set("cleanup_state", "complete")
	return service.app.Save(job)
}

func convertMigrationValue(
	before v2.FieldDefinition,
	after v2.FieldDefinition,
	value any,
	rule string,
) (any, bool, error) {
	if value == nil {
		return nil, true, nil
	}
	if optionRule, ok := parseSelectOptionRule(rule); ok {
		return convertSelectOptionValue(
			before.LogicalType, value, optionRule,
		)
	}
	switch {
	case before.LogicalType == v2.LogicalRelation &&
		after.LogicalType == v2.LogicalRelation:
		return convertCardinality(
			value, before.Relation.Cardinality, after.Relation.Cardinality, rule,
		)
	case before.LogicalType == after.LogicalType:
		return value, true, nil
	case before.LogicalType == v2.LogicalSelect &&
		after.LogicalType == v2.LogicalMultiSelect:
		return []string{fmt.Sprint(value)}, true, nil
	case before.LogicalType == v2.LogicalMultiSelect &&
		after.LogicalType == v2.LogicalSelect:
		values, ok := value.([]string)
		if !ok {
			if raw, rawOK := value.([]any); rawOK {
				values = make([]string, len(raw))
				for index, item := range raw {
					values[index] = fmt.Sprint(item)
				}
			}
		}
		if len(values) == 0 {
			return nil, true, nil
		}
		if len(values) > 1 && rule != "first" && rule != "last" && rule != "clear" {
			return nil, false, errors.New("multiSelect requires first, last, or clear rule")
		}
		switch rule {
		case "last":
			return values[len(values)-1], true, nil
		case "clear":
			return nil, true, nil
		default:
			return values[0], true, nil
		}
	case after.LogicalType == v2.LogicalText ||
		after.LogicalType == v2.LogicalEditor:
		return fmt.Sprint(value), true, nil
	case before.LogicalType == v2.LogicalText &&
		after.LogicalType == v2.LogicalNumber:
		number, err := strconv.ParseFloat(fmt.Sprint(value), 64)
		if err == nil {
			return applyRoundingRule(number, rule), true, nil
		}
		if rule == "clear" {
			return nil, true, nil
		}
		return nil, false, errors.New("text value is not a valid number")
	case isTemporalType(before.LogicalType) && isTemporalType(after.LogicalType):
		return convertTemporal(
			before.LogicalType, after.LogicalType, fmt.Sprint(value), rule,
		)
	}
	if rule == "clear" {
		return nil, true, nil
	}
	return nil, false, fmt.Errorf(
		"conversion from %s to %s is not supported",
		before.LogicalType, after.LogicalType,
	)
}

type selectOptionRule struct {
	SourceOptionID      string
	Action              string
	ReplacementOptionID string
}

func parseSelectOptionRule(raw string) (selectOptionRule, bool) {
	parts := strings.Split(raw, ":")
	if len(parts) == 3 && parts[0] == "selectOption" &&
		parts[1] != "" && parts[2] == "clear" {
		return selectOptionRule{
			SourceOptionID: parts[1],
			Action:         "clear",
		}, true
	}
	if len(parts) == 4 && parts[0] == "selectOption" &&
		parts[1] != "" && parts[2] == "replace" && parts[3] != "" {
		return selectOptionRule{
			SourceOptionID:      parts[1],
			Action:              "replace",
			ReplacementOptionID: parts[3],
		}, true
	}
	return selectOptionRule{}, false
}

func convertSelectOptionValue(
	logicalType v2.LogicalType,
	value any,
	rule selectOptionRule,
) (any, bool, error) {
	if logicalType == v2.LogicalSelect {
		current := fmt.Sprint(value)
		if current != rule.SourceOptionID {
			return value, true, nil
		}
		if rule.Action == "clear" {
			return nil, true, nil
		}
		return rule.ReplacementOptionID, true, nil
	}
	if logicalType != v2.LogicalMultiSelect {
		return nil, false, errors.New(
			"select option deletion rule requires select or multiSelect",
		)
	}
	values, ok := value.([]string)
	if !ok {
		raw, rawOK := value.([]any)
		if !rawOK {
			return nil, false, errors.New("multiSelect value must be an array")
		}
		values = make([]string, len(raw))
		for index, item := range raw {
			values[index] = fmt.Sprint(item)
		}
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, optionID := range values {
		if optionID == rule.SourceOptionID {
			if rule.Action == "clear" {
				continue
			}
			optionID = rule.ReplacementOptionID
		}
		if _, duplicate := seen[optionID]; duplicate {
			continue
		}
		seen[optionID] = struct{}{}
		result = append(result, optionID)
	}
	if len(result) == 0 {
		return nil, true, nil
	}
	return result, true, nil
}

func convertCardinality(value any, before, after, rule string) (any, bool, error) {
	if before == after {
		return value, true, nil
	}
	if before == "one" && after == "many" {
		return []string{fmt.Sprint(value)}, true, nil
	}
	values, ok := value.([]string)
	if !ok {
		if raw, rawOK := value.([]any); rawOK {
			values = make([]string, len(raw))
			for index, item := range raw {
				values[index] = fmt.Sprint(item)
			}
		}
	}
	if len(values) == 0 {
		return nil, true, nil
	}
	if len(values) > 1 &&
		rule != "first" && rule != "last" && rule != "clear" {
		return nil, false, errors.New(
			"many relation requires first, last, or clear rule",
		)
	}
	switch rule {
	case "last":
		return values[len(values)-1], true, nil
	case "clear":
		return nil, true, nil
	default:
		return values[0], true, nil
	}
}

func isTemporalType(logicalType v2.LogicalType) bool {
	return logicalType == v2.LogicalDate ||
		logicalType == v2.LogicalDateTime ||
		logicalType == v2.LogicalTime
}

func convertTemporal(
	before v2.LogicalType,
	after v2.LogicalType,
	value string,
	rule string,
) (any, bool, error) {
	if rule == "clear" {
		return nil, true, nil
	}
	switch before {
	case v2.LogicalDate:
		date, err := time.Parse("2006-01-02", value)
		if err != nil {
			return nil, false, errors.New("date value is invalid")
		}
		if after == v2.LogicalDateTime {
			return date.UTC().Format(time.RFC3339Nano), true, nil
		}
		if after == v2.LogicalTime {
			return "00:00:00", true, nil
		}
	case v2.LogicalDateTime:
		dateTime, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil, false, errors.New("dateTime value is invalid")
		}
		if after == v2.LogicalDate {
			return dateTime.UTC().Format("2006-01-02"), true, nil
		}
		if after == v2.LogicalTime {
			return dateTime.UTC().Format("15:04:05"), true, nil
		}
	case v2.LogicalTime:
		parsed, err := time.Parse("15:04:05", value)
		if err != nil {
			return nil, false, errors.New("time value is invalid")
		}
		if rule != "dateFill" {
			return nil, false, errors.New(
				"time conversion requires dateFill or clear rule",
			)
		}
		if after == v2.LogicalDate {
			return "1970-01-01", true, nil
		}
		if after == v2.LogicalDateTime {
			return time.Date(
				1970, time.January, 1,
				parsed.Hour(), parsed.Minute(), parsed.Second(), 0, time.UTC,
			).Format(time.RFC3339Nano), true, nil
		}
	}
	return nil, false, fmt.Errorf(
		"temporal conversion from %s to %s is unsupported",
		before, after,
	)
}

func applyRoundingRule(value float64, rule string) float64 {
	switch rule {
	case "round":
		return math.Round(value)
	case "floor":
		return math.Floor(value)
	case "ceil":
		return math.Ceil(value)
	case "truncate":
		return math.Trunc(value)
	default:
		return value
	}
}

func physicalValues(record *core.Record, definition v2.FieldDefinition) map[string]any {
	values := map[string]any{
		definition.Identity.PhysicalName: record.GetRaw(definition.Identity.PhysicalName),
	}
	if definition.Value.Presence.PhysicalName != "" {
		values[definition.Value.Presence.PhysicalName] =
			record.GetBool(definition.Value.Presence.PhysicalName)
	}
	return values
}

func migrationCollection(
	app core.App,
	tableID string,
) (*core.Record, *core.Collection, error) {
	table, err := NewCatalog(app).tableRecord(app, tableID)
	if err != nil {
		return nil, nil, err
	}
	collection, err := app.FindCollectionByNameOrId(table.GetString("collection_id"))
	return table, collection, err
}

func (service *MigrationService) setPhase(
	record *core.Record,
	phase v2.MigrationPhase,
) error {
	record.Set("state", "running")
	record.Set("phase", phase)
	record.Set("error_json", nil)
	return service.app.Save(record)
}

func (service *MigrationService) beginSwitch(record *core.Record) error {
	err := service.app.RunInTransaction(func(app core.App) error {
		current, err := app.FindRecordById("vibetable_jobs", record.Id)
		if err != nil {
			return err
		}
		if current.GetString("state") == "cancelling" {
			return productError(
				"field.migration.cancel_requested", "jobId",
				"migration cancellation was requested before switching", nil,
			)
		}
		phase := v2.MigrationPhase(current.GetString("phase"))
		if phase == v2.MigrationSwitching ||
			phase == v2.MigrationCleaning ||
			phase == v2.MigrationCompleted {
			return productError(
				"field.migration.invalid_phase", "jobId",
				"migration has already entered the authority switch", nil,
			)
		}
		current.Set("state", "running")
		current.Set("phase", v2.MigrationSwitching)
		current.Set("error_json", nil)
		return app.Save(current)
	})
	if err == nil {
		record.Set("state", "running")
		record.Set("phase", v2.MigrationSwitching)
		record.Set("error_json", nil)
	}
	return err
}

func (service *MigrationService) updateProgress(
	record *core.Record,
	processed int64,
	total int64,
	cursor string,
) error {
	cursorRaw, _ := json.Marshal(map[string]any{
		"tableId":      migrationTableID(record),
		"lastRecordId": cursor,
		"operationId":  migrationOperationID(record),
	})
	if total == 0 {
		raw, _ := json.Marshal(record.GetRaw("progress_json"))
		var current struct {
			Total int64 `json:"total"`
		}
		_ = json.Unmarshal(raw, &current)
		total = current.Total
	}
	progressRaw, _ := json.Marshal(map[string]any{
		"completed": processed, "total": total,
	})
	record.Set("cursor_json", types.JSONRaw(cursorRaw))
	record.Set("progress_json", types.JSONRaw(progressRaw))
	return service.app.Save(record)
}

func migrationTableID(record *core.Record) string {
	raw, _ := json.Marshal(record.GetRaw("cursor_json"))
	var cursor struct {
		TableID string `json:"tableId"`
	}
	_ = json.Unmarshal(raw, &cursor)
	return cursor.TableID
}

func migrationOperationID(record *core.Record) string {
	raw, _ := json.Marshal(record.GetRaw("cursor_json"))
	var cursor struct {
		OperationID string `json:"operationId"`
	}
	_ = json.Unmarshal(raw, &cursor)
	return cursor.OperationID
}

func (service *MigrationService) cancelRequested(jobID string) bool {
	service.mu.Lock()
	_, exists := service.cancel[jobID]
	service.mu.Unlock()
	if exists {
		return true
	}
	record, err := service.app.FindRecordById("vibetable_jobs", jobID)
	return err == nil && record.GetString("state") == "cancelling"
}

func (service *MigrationService) cancelBeforeSwitch(record *core.Record) error {
	record.Set("state", "cancelled")
	record.Set("phase", v2.MigrationCancelled)
	record.Set("write_lock_owner", "")
	return service.app.Save(record)
}

func (service *MigrationService) cancelAndClean(
	record *core.Record,
	plan v2.FieldChangePlan,
) error {
	_, collection, err := migrationCollection(service.app, plan.Intent.TableID)
	if err != nil {
		return service.markCleanupRetry(record, err, "cancel_retry")
	}
	if plan.After != nil && plan.Intent.Action != v2.ActionBackfill {
		collection.Fields.RemoveById(plan.After.Identity.ProviderFieldID)
		if plan.After.Value.Presence.ProviderFieldID != "" {
			collection.Fields.RemoveById(plan.After.Value.Presence.ProviderFieldID)
		}
		if err := service.app.Save(collection); err != nil {
			return service.markCleanupRetry(record, err, "cancel_retry")
		}
	}
	record.Set("cleanup_state", "complete")
	return service.cancelBeforeSwitch(record)
}

func (service *MigrationService) markCleanupRetry(
	record *core.Record,
	cause error,
	cleanupState string,
) error {
	record.Set("state", "failed")
	record.Set("phase", v2.MigrationCleaning)
	record.Set("cleanup_state", cleanupState)
	if err := service.app.Save(record); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (service *MigrationService) markMigrationAuditFailed(
	job *core.Record,
	plan v2.FieldChangePlan,
	errorCode string,
) error {
	audit, err := service.app.FindFirstRecordByFilter(
		"vibetable_schema_audit",
		"migration_job_id={:job} || operation_id={:operation}",
		dbx.Params{
			"job": job.Id, "operation": migrationOperationID(job),
		},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load failed migration audit: %w", err)
	}
	audit.Set("outcome", "failed")
	audit.Set("error_code", errorCode)
	audit.Set("migration_job_id", job.Id)
	if audit.GetString("field_id") == "" && plan.Before != nil {
		audit.Set("field_id", plan.Before.Identity.FieldID)
	}
	if err := service.app.Save(audit); err != nil {
		return fmt.Errorf("save failed migration audit: %w", err)
	}
	return nil
}

func (service *MigrationService) failAndClean(
	ctx context.Context,
	record *core.Record,
	plan v2.FieldChangePlan,
	cause error,
) error {
	_ = ctx
	diagnostic := v2.Diagnostic{
		Code: "field.migration.conversion_failed", Path: "",
		Message: "field migration failed", Details: map[string]any{},
	}
	var productErr *ProductError
	if errors.As(cause, &productErr) {
		diagnostic.Code, diagnostic.Path, diagnostic.Message =
			productErr.Code, productErr.Path, productErr.Message
		if productErr.Details != nil {
			diagnostic.Details = productErr.Details
		}
	}
	raw, _ := json.Marshal(diagnostic)
	record.Set("state", "failed")
	record.Set("phase", v2.MigrationCleaning)
	record.Set("cleanup_state", "rollback_retry")
	record.Set("error_json", types.JSONRaw(raw))
	if err := service.app.Save(record); err != nil {
		return errors.Join(cause, err)
	}
	if err := service.markMigrationAuditFailed(
		record,
		plan,
		diagnostic.Code,
	); err != nil {
		return errors.Join(cause, err)
	}
	if plan.After != nil && plan.Intent.Action != v2.ActionBackfill {
		current, fieldErr := NewCatalog(service.app).Field(
			ctx, plan.Intent.TableID, plan.After.Identity.FieldID,
		)
		if fieldErr == nil &&
			current.Identity.ProviderFieldID == plan.After.Identity.ProviderFieldID {
			// A committed authority switch must never be rolled back by
			// deleting the provider that metadata now declares authoritative.
			record.Set("cleanup_state", "retry")
			return errors.Join(cause, service.app.Save(record))
		}
	}
	if plan.After != nil && plan.Intent.Action != v2.ActionBackfill {
		_, collection, collectionErr := migrationCollection(
			service.app, plan.Intent.TableID,
		)
		if collectionErr != nil {
			return service.markCleanupRetry(
				record, errors.Join(cause, collectionErr), "rollback_retry",
			)
		}
		if collection != nil {
			collection.Fields.RemoveById(plan.After.Identity.ProviderFieldID)
			if plan.After.Value.Presence.ProviderFieldID != "" {
				collection.Fields.RemoveById(plan.After.Value.Presence.ProviderFieldID)
			}
			if err := service.app.Save(collection); err != nil {
				return service.markCleanupRetry(
					record, errors.Join(cause, err), "rollback_retry",
				)
			}
		}
	}
	record.Set("phase", v2.MigrationRolledBack)
	record.Set("cleanup_state", "complete")
	record.Set("write_lock_owner", "")
	if err := service.app.Save(record); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (service *MigrationService) resumeRollbackCleanup(
	record *core.Record,
	plan v2.FieldChangePlan,
) error {
	if plan.After != nil && plan.Intent.Action != v2.ActionBackfill {
		_, collection, err := migrationCollection(service.app, plan.Intent.TableID)
		if err != nil {
			return service.markCleanupRetry(record, err, "rollback_retry")
		}
		collection.Fields.RemoveById(plan.After.Identity.ProviderFieldID)
		if plan.After.Value.Presence.ProviderFieldID != "" {
			collection.Fields.RemoveById(plan.After.Value.Presence.ProviderFieldID)
		}
		if err := service.app.Save(collection); err != nil {
			return service.markCleanupRetry(record, err, "rollback_retry")
		}
	}
	record.Set("state", "failed")
	record.Set("phase", v2.MigrationRolledBack)
	record.Set("cleanup_state", "complete")
	record.Set("write_lock_owner", "")
	return service.app.Save(record)
}

func (service *MigrationService) inject(phase v2.MigrationPhase) error {
	if service.fault == nil {
		return nil
	}
	return service.fault(phase)
}
