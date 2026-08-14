package fieldchange

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/backupreceipt"
	"github.com/vibetable/vibetable/sidecar/internal/fieldresource"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaaudit"
)

const fieldOperationTTL = 24 * time.Hour

type Executor struct {
	app             core.App
	store           PlanStore
	migration       MigrationScheduler
	formulaBackfill FormulaBackfillScheduler
	clock           func() time.Time
	protectionProof ProtectionSnapshotVerifier
	logger          *slog.Logger
}

type ProtectionSnapshotVerifier func(context.Context, string) error

type MigrationScheduler interface {
	Enqueue(
		ctx context.Context,
		app core.App,
		plan v2.FieldChangePlan,
		operationID string,
	) (string, error)
	Start(jobID string) bool
}

type FormulaBackfillScheduler interface {
	EnqueueFormulaBackfill(
		ctx context.Context,
		app core.App,
		tableID string,
		schemaRevision string,
	) (string, error)
	Start(jobID string) bool
}

type ExecutorOption func(*Executor)

func WithExecutorClock(clock func() time.Time) ExecutorOption {
	return func(executor *Executor) {
		executor.clock = clock
	}
}

func WithMigrationScheduler(scheduler MigrationScheduler) ExecutorOption {
	return func(executor *Executor) {
		executor.migration = scheduler
	}
}

func WithFormulaBackfillScheduler(scheduler FormulaBackfillScheduler) ExecutorOption {
	return func(executor *Executor) {
		executor.formulaBackfill = scheduler
	}
}

func WithProtectionSnapshotVerifier(
	verifier ProtectionSnapshotVerifier,
) ExecutorOption {
	return func(executor *Executor) {
		executor.protectionProof = verifier
	}
}

func WithExecutorLogger(logger *slog.Logger) ExecutorOption {
	return func(executor *Executor) {
		if logger != nil {
			executor.logger = logger
		}
	}
}

func NewExecutor(
	app core.App,
	store PlanStore,
	options ...ExecutorOption,
) *Executor {
	if store == nil {
		store = NewPocketBasePlanStore(app)
	}
	executor := &Executor{
		app: app, store: store, clock: time.Now,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, option := range options {
		option(executor)
	}
	return executor
}

func (executor *Executor) Apply(
	ctx context.Context,
	request v2.ApplyRequest,
) (v2.ApplyReceipt, error) {
	started := time.Now()
	receipt, err := executor.apply(ctx, request)
	attributes := []any{
		"event", "field_change_applied",
		"plan_id", request.PlanID,
		"operation_id", request.OperationID,
		"duration_ms", time.Since(started).Milliseconds(),
	}
	if err != nil {
		attributes[1] = "field_change_rejected"
		executor.logger.Warn(
			"field change apply rejected",
			append(attributes, "outcome", "rejected", "error", err)...,
		)
		return v2.ApplyReceipt{}, err
	}
	executor.logger.Info(
		"field change applied",
		append(
			attributes,
			"outcome", "applied",
			"table_id", receipt.TableID,
			"field_id", receipt.FieldID,
			"action", receipt.Action,
			"migration_job_id", receipt.MigrationJobID,
		)...,
	)
	return receipt, nil
}

func (executor *Executor) apply(
	ctx context.Context,
	request v2.ApplyRequest,
) (v2.ApplyReceipt, error) {
	if err := validateApplyRequest(request); err != nil {
		return v2.ApplyReceipt{}, err
	}
	plan, err := executor.store.Load(ctx, request.PlanID)
	if err != nil {
		return v2.ApplyReceipt{}, err
	}
	if plan == nil {
		return v2.ApplyReceipt{}, productError(
			"field.change.plan_not_found", "planId", "field change plan was not found", nil,
		)
	}
	if request.PlanHash != plan.PlanHash || !VerifyPlanHash(*plan) {
		return v2.ApplyReceipt{}, productError(
			"field.change.plan_hash_mismatch", "planHash",
			"field change plan hash does not match the frozen plan", nil,
		)
	}
	if !plan.CanApply || len(plan.Errors) != 0 {
		return v2.ApplyReceipt{}, productError(
			"field.change.plan_blocked", "planId",
			"field change plan contains blocking diagnostics", nil,
		)
	}
	if request.Actor != plan.Intent.Actor {
		return v2.ApplyReceipt{}, productError(
			"field.change.actor_mismatch", "actor",
			"apply actor must match the actor that created the plan", nil,
		)
	}
	if err := validateConfirmations(*plan, request.Confirmations); err != nil {
		return v2.ApplyReceipt{}, err
	}

	requestHash, err := canonicalHash(request)
	if err != nil {
		return v2.ApplyReceipt{}, err
	}
	var receipt v2.ApplyReceipt
	var formulaBackfillJobID string
	err = executor.app.RunInTransaction(func(txApp core.App) error {
		replayed, replayErr := loadReplay(
			txApp, request.OperationID, requestHash, executor.clock().UTC(),
		)
		if replayErr != nil {
			return replayErr
		}
		if replayed != nil {
			receipt = *replayed
			return nil
		}
		expiresAt, expiryErr := time.Parse(time.RFC3339Nano, plan.ExpiresAt)
		if expiryErr != nil || !expiresAt.After(executor.clock().UTC()) {
			return productError(
				"field.change.plan_expired", "planId",
				"field change plan has expired", nil,
			)
		}
		revisions, revisionErr := NewCatalog(txApp).Revisions(
			ctx, plan.Intent.TableID,
		)
		if revisionErr != nil {
			return revisionErr
		}
		if revisions.Schema != plan.ExpectedSchemaRev {
			return productError(
				"field.change.schema_conflict", "expectedSchemaRevision",
				"schema revision changed after planning",
				map[string]any{
					"expected": plan.ExpectedSchemaRev,
					"actual":   revisions.Schema,
				},
			)
		}
		if plan.ExpectedDataRevision != nil &&
			revisions.Data != *plan.ExpectedDataRevision {
			return productError(
				"field.change.data_conflict", "expectedDataRevision",
				"data revision changed after planning",
				map[string]any{
					"expected": *plan.ExpectedDataRevision,
					"actual":   revisions.Data,
				},
			)
		}
		for _, related := range plan.RelatedChanges {
			relatedRevisions, relatedRevisionErr := NewCatalog(txApp).Revisions(
				ctx, related.TableID,
			)
			if relatedRevisionErr != nil {
				return relatedRevisionErr
			}
			if relatedRevisions.Schema != related.ExpectedSchemaRevision {
				return productError(
					"field.change.schema_conflict", "relatedChanges",
					"reciprocal table schema revision changed after planning",
					map[string]any{
						"tableId":  related.TableID,
						"expected": related.ExpectedSchemaRevision,
						"actual":   relatedRevisions.Schema,
					},
				)
			}
		}
		if plan.Intent.Action == v2.ActionPurge {
			var verifyErr error
			if executor.protectionProof != nil {
				verifyErr = executor.protectionProof(
					ctx,
					request.ProtectionSnapshotID,
				)
			} else {
				verifyErr = backupreceipt.Verify(
					ctx, txApp, plan.Intent.BackupReceipt,
				)
			}
			if verifyErr != nil {
				return productError(
					"field.purge.backup_required", "protectionSnapshotId",
					"a current verified protection snapshot is required", nil,
				)
			}
		}
		if plan.CreatesMigration {
			if executor.migration == nil {
				return productError(
					"field.migration.unavailable", "planId",
					"field migration service is unavailable", nil,
				)
			}
			jobID, enqueueErr := executor.migration.Enqueue(
				ctx, txApp, *plan, request.OperationID,
			)
			if enqueueErr != nil {
				return enqueueErr
			}
			receipt = v2.ApplyReceipt{
				Contract: v2.Contract, OperationID: request.OperationID,
				PlanID: plan.PlanID, Action: plan.Intent.Action,
				TableID: plan.Intent.TableID, FieldID: fieldIDAfter(*plan),
				SchemaRevision: revisions.Schema, Definition: plan.Before,
				MigrationJobID: jobID,
			}
			if auditErr := saveSchemaAudit(
				ctx, txApp, *plan, request, receipt, executor.clock().UTC(),
			); auditErr != nil {
				return auditErr
			}
			if replayErr := saveReplay(
				txApp, request.OperationID, requestHash, receipt,
				executor.clock().UTC(),
			); replayErr != nil {
				return replayErr
			}
			return markPlanApplied(txApp, plan.PlanID, request.OperationID)
		}
		applied, applyErr := executor.applyFrozenPlan(ctx, txApp, *plan)
		if applyErr != nil {
			return applyErr
		}
		nextRevision, parseErr := v2.ParseSchemaRevision(revisions.Schema)
		if parseErr != nil {
			return parseErr
		}
		nextRevision++
		receipt = v2.ApplyReceipt{
			Contract: v2.Contract, OperationID: request.OperationID,
			PlanID: plan.PlanID, Action: plan.Intent.Action,
			TableID: plan.Intent.TableID, FieldID: fieldIDAfter(*plan),
			SchemaRevision: v2.FormatSchemaRevision(nextRevision),
			Definition:     applied,
		}
		for _, related := range plan.RelatedChanges {
			relatedPlan := pairedPlan(*plan, related)
			relatedDefinition, relatedApplyErr := executor.applyFrozenPlan(
				ctx, txApp, relatedPlan,
			)
			if relatedApplyErr != nil {
				return relatedApplyErr
			}
			relatedRevision, relatedParseErr := v2.ParseSchemaRevision(
				related.ExpectedSchemaRevision,
			)
			if relatedParseErr != nil {
				return relatedParseErr
			}
			relatedRevision++
			if saveErr := saveTableRevisionAndComputedMetadata(
				ctx, txApp, relatedPlan, relatedRevision,
			); saveErr != nil {
				return saveErr
			}
			receipt.Related = append(receipt.Related, v2.RelatedApplyReceipt{
				TableID: related.TableID, FieldID: related.FieldID,
				SchemaRevision: v2.FormatSchemaRevision(relatedRevision),
				Definition:     relatedDefinition,
			})
		}
		if saveErr := saveTableRevisionAndComputedMetadata(
			ctx, txApp, *plan, nextRevision,
		); saveErr != nil {
			return saveErr
		}
		if formulaNeedsBackfill(plan.Before, plan.After) {
			if executor.formulaBackfill == nil {
				return productError(
					"formula.backfill.unavailable",
					"planId",
					"formula schema changes require a durable backfill scheduler",
					nil,
				)
			}
			formulaBackfillJobID, err = executor.formulaBackfill.EnqueueFormulaBackfill(
				ctx,
				txApp,
				plan.Intent.TableID,
				receipt.SchemaRevision,
			)
			if err != nil {
				return err
			}
		}
		if auditErr := saveSchemaAudit(
			ctx, txApp, *plan, request, receipt, executor.clock().UTC(),
		); auditErr != nil {
			return auditErr
		}
		if replayErr := saveReplay(
			txApp, request.OperationID, requestHash, receipt,
			executor.clock().UTC(),
		); replayErr != nil {
			return replayErr
		}
		return markPlanApplied(txApp, plan.PlanID, request.OperationID)
	})
	if err != nil {
		_ = executor.app.RunInTransaction(func(txApp core.App) error {
			return saveFailedSchemaAudit(
				txApp,
				*plan,
				request,
				stableFieldErrorCode(err),
				executor.clock().UTC(),
			)
		})
		return v2.ApplyReceipt{}, err
	}
	if receipt.MigrationJobID != "" {
		executor.migration.Start(receipt.MigrationJobID)
	}
	if formulaBackfillJobID != "" {
		executor.formulaBackfill.Start(formulaBackfillJobID)
	}
	if plan.Intent.Action == v2.ActionPurge {
		// The cleanup set committed with the schema change. A transient blob
		// storage error leaves durable queued work instead of converting an
		// already-committed purge into a misleading failed apply.
		_ = fieldresource.RunPendingAttachmentCleanup(ctx, executor.app)
	}
	return receipt, nil
}

func pairedPlan(
	plan v2.FieldChangePlan,
	related v2.RelatedFieldChange,
) v2.FieldChangePlan {
	plan.Intent.TableID = related.TableID
	plan.Intent.FieldID = related.FieldID
	plan.Intent.RelationPair = nil
	plan.Before = related.Before
	plan.After = related.After
	plan.ExpectedSchemaRev = related.ExpectedSchemaRevision
	plan.ExpectedDataRevision = nil
	plan.RelatedChanges = nil
	return plan
}

func (executor *Executor) applyFrozenPlan(
	ctx context.Context,
	app core.App,
	plan v2.FieldChangePlan,
) (*v2.FieldDefinition, error) {
	table, err := NewCatalog(app).tableRecord(app, plan.Intent.TableID)
	if err != nil {
		return nil, err
	}
	collection, err := app.FindCollectionByNameOrId(table.GetString("collection_id"))
	if err != nil {
		return nil, fmt.Errorf("load field collection: %w", err)
	}
	if plan.CreatesMigration {
		count, countErr := app.CountRecords(collection)
		if countErr != nil {
			return nil, fmt.Errorf("count migration records: %w", countErr)
		}
		if count != 0 {
			return nil, productError(
				"field.migration.required", "planId",
				"non-empty table conversion requires the migration executor",
				map[string]any{"records": count},
			)
		}
	}

	switch plan.Intent.Action {
	case v2.ActionCreate:
		if plan.After == nil {
			return nil, errors.New("create plan has no after definition")
		}
		if err := addCompiledField(app, collection, *plan.After); err != nil {
			return nil, err
		}
		if err := saveDefinitionMetadata(app, plan.Intent.TableID, *plan.After); err != nil {
			return nil, err
		}
		return cloneDefinition(plan.After), nil
	case v2.ActionUpdate, v2.ActionConvert:
		if plan.Before == nil || plan.After == nil {
			return nil, errors.New("update plan is incomplete")
		}
		if err := replaceCompiledField(app, collection, *plan.Before, *plan.After); err != nil {
			return nil, err
		}
		if formulaDefinitionChanged(plan.Before, plan.After) {
			if err := clearComputedPhysicalValue(
				ctx, app, collection.Name, plan.After.Identity.PhysicalName,
			); err != nil {
				return nil, err
			}
		}
		if err := saveDefinitionMetadata(app, plan.Intent.TableID, *plan.After); err != nil {
			return nil, err
		}
		return cloneDefinition(plan.After), nil
	case v2.ActionRetire, v2.ActionRestore:
		if plan.After == nil {
			return nil, errors.New("lifecycle plan has no after definition")
		}
		if err := saveDefinitionMetadata(app, plan.Intent.TableID, *plan.After); err != nil {
			return nil, err
		}
		return cloneDefinition(plan.After), nil
	case v2.ActionPurge:
		if plan.Before == nil {
			return nil, errors.New("purge plan has no before definition")
		}
		if plan.Before.LogicalType == v2.LogicalFile {
			if err := fieldresource.StageAttachmentPurge(
				ctx,
				app,
				plan.Intent.TableID,
				plan.Before.Identity.FieldID,
				plan.PlanID,
			); err != nil {
				return nil, fmt.Errorf("purge field attachments: %w", err)
			}
		}
		removeCompiledField(collection, *plan.Before)
		removeFieldIndexes(collection, plan.Before.Identity.PhysicalName)
		if err := app.Save(collection); err != nil {
			return nil, fmt.Errorf("purge physical field: %w", err)
		}
		if err := purgeDerivedMetadata(
			app, plan.Intent.TableID, plan.Before.Identity.FieldID,
		); err != nil {
			return nil, err
		}
		record, findErr := app.FindFirstRecordByFilter(
			"vibetable_fields",
			"table_id={:table} && field_id={:field}",
			dbx.Params{
				"table": plan.Intent.TableID,
				"field": plan.Before.Identity.FieldID,
			},
		)
		if findErr != nil {
			return nil, fmt.Errorf("load purged field metadata: %w", findErr)
		}
		if err := app.Delete(record); err != nil {
			return nil, fmt.Errorf("delete purged field metadata: %w", err)
		}
		retired := cloneDefinition(plan.Before)
		retired.Lifecycle.State = v2.LifecycleRetired
		if err := syncRelationMetadata(
			app, plan.Intent.TableID, *retired,
		); err != nil {
			return nil, err
		}
		return nil, nil
	case v2.ActionBackfill:
		return nil, productError(
			"field.migration.required", "action",
			"default backfill requires the migration executor", nil,
		)
	default:
		return nil, productError(
			"field.contract.invalid", "action", "unsupported field action", nil,
		)
	}
}

func purgeDerivedMetadata(
	app core.App,
	tableID string,
	fieldID string,
) error {
	targets := []struct {
		collection string
		filter     string
		params     dbx.Params
	}{
		{
			collection: "vibetable_formulas",
			filter:     "table_id={:table} && field_id={:field}",
			params:     dbx.Params{"table": tableID, "field": fieldID},
		},
		{
			collection: "vibetable_formula_dependencies",
			filter:     "source_table_id={:table} && formula_field_id={:field}",
			params:     dbx.Params{"table": tableID, "field": fieldID},
		},
		{
			collection: "vibetable_lookups",
			filter:     "table_id={:table} && field_id={:field}",
			params:     dbx.Params{"table": tableID, "field": fieldID},
		},
	}
	for _, target := range targets {
		records, err := app.FindRecordsByFilter(
			target.collection,
			target.filter,
			"id",
			0,
			0,
			target.params,
		)
		if err != nil {
			return fmt.Errorf(
				"load %s metadata for purge: %w",
				target.collection,
				err,
			)
		}
		for _, record := range records {
			if err := app.Delete(record); err != nil {
				return fmt.Errorf(
					"delete %s metadata for purge: %w",
					target.collection,
					err,
				)
			}
		}
	}
	return nil
}

func addCompiledField(
	app core.App,
	collection *core.Collection,
	definition v2.FieldDefinition,
) error {
	compiled, err := v2.CompileField(definition, relationResolver(app))
	if err != nil {
		return err
	}
	if collection.Fields.GetById(definition.Identity.ProviderFieldID) != nil ||
		collection.Fields.GetByName(definition.Identity.PhysicalName) != nil {
		return productError(
			"field.identity.conflict", "identity", "allocated field identity already exists", nil,
		)
	}
	collection.Fields.Add(compiled.Value)
	if compiled.Presence != nil {
		collection.Fields.Add(compiled.Presence)
	}
	removeFieldIndexes(collection, definition.Identity.PhysicalName)
	if index, ok, indexErr := v2.CompileUniqueIndex(collection.Name, definition); indexErr != nil {
		return indexErr
	} else if ok {
		collection.Indexes = append(collection.Indexes, index)
	}
	if err := app.Save(collection); err != nil {
		return fmt.Errorf("save compiled field: %w", err)
	}
	return nil
}

func replaceCompiledField(
	app core.App,
	collection *core.Collection,
	before v2.FieldDefinition,
	after v2.FieldDefinition,
) error {
	removeCompiledField(collection, before)
	return addCompiledField(app, collection, after)
}

func removeCompiledField(
	collection *core.Collection,
	definition v2.FieldDefinition,
) {
	collection.Fields.RemoveById(definition.Identity.ProviderFieldID)
	if definition.Value.Presence.ProviderFieldID != "" {
		collection.Fields.RemoveById(definition.Value.Presence.ProviderFieldID)
	}
}

func removeFieldIndexes(collection *core.Collection, physicalName string) {
	prefix := "CREATE UNIQUE INDEX `uniq_" + collection.Name + "_" + physicalName + "`"
	filtered := collection.Indexes[:0]
	for _, index := range collection.Indexes {
		if !strings.HasPrefix(index, prefix) {
			filtered = append(filtered, index)
		}
	}
	collection.Indexes = filtered
}

func relationResolver(app core.App) v2.RelationCollectionResolver {
	return func(tableID string) (string, error) {
		record, err := NewCatalog(app).tableRecord(app, tableID)
		if err != nil {
			return "", err
		}
		return record.GetString("collection_id"), nil
	}
}

func saveDefinitionMetadata(
	app core.App,
	tableID string,
	definition v2.FieldDefinition,
) error {
	record, err := app.FindFirstRecordByFilter(
		"vibetable_fields",
		"table_id={:table} && field_id={:field}",
		dbx.Params{"table": tableID, "field": definition.Identity.FieldID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		collection, collectionErr := app.FindCollectionByNameOrId("vibetable_fields")
		if collectionErr != nil {
			return fmt.Errorf("load field metadata collection: %w", collectionErr)
		}
		record = core.NewRecord(collection)
	} else if err != nil {
		return fmt.Errorf("load field metadata: %w", err)
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		return fmt.Errorf("encode field definition: %w", err)
	}
	sum := sha256.Sum256(raw)
	identity, _ := json.Marshal(definition.Identity)
	value, _ := json.Marshal(definition.Value)
	constraints, _ := json.Marshal(definition.Constraints)
	storage, _ := json.Marshal(definition.Storage)
	display, _ := json.Marshal(definition.Display)
	record.Set("table_id", tableID)
	record.Set("field_id", definition.Identity.FieldID)
	record.Set("physical_name", definition.Identity.PhysicalName)
	record.Set("display_name", definition.DisplayName)
	record.Set("kind", storedFieldKind(definition))
	record.Set("data_type", string(definition.LogicalType))
	record.Set("storage_type", string(definition.Storage.Kind))
	record.Set("constraints_json", types.JSONRaw(constraints))
	record.Set("editor_json", types.JSONRaw(display))
	record.Set("schema_model_version", 2)
	record.Set("lifecycle_state", definition.Lifecycle.State)
	if definition.Lifecycle.RetiredAt != nil {
		record.Set("retired_at", *definition.Lifecycle.RetiredAt)
	} else {
		record.Set("retired_at", "")
	}
	record.Set("identity_json", types.JSONRaw(identity))
	record.Set("value_semantics_json", types.JSONRaw(value))
	record.Set("constraints_v2_json", types.JSONRaw(constraints))
	record.Set("storage_v2_json", types.JSONRaw(storage))
	record.Set("display_v2_json", types.JSONRaw(display))
	record.Set("recommended_defaults_version", definition.Value.Default.DefaultsVersion)
	record.Set("definition_hash", hex.EncodeToString(sum[:]))
	record.Set("definition_v2_json", types.JSONRaw(raw))
	if err := app.Save(record); err != nil {
		return fmt.Errorf("save field metadata: %w", err)
	}
	return syncRelationMetadata(app, tableID, definition)
}

func syncRelationMetadata(
	app core.App,
	tableID string,
	definition v2.FieldDefinition,
) error {
	record, err := app.FindFirstRecordByFilter(
		"vibetable_relations",
		"source_table_id={:table} && source_field_id={:field}",
		dbx.Params{
			"table": tableID,
			"field": definition.Identity.FieldID,
		},
	)
	shouldExist := definition.LogicalType == v2.LogicalRelation &&
		definition.Relation != nil &&
		definition.Lifecycle.State == v2.LifecycleActive
	if !shouldExist {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load relation metadata: %w", err)
		}
		if err := app.Delete(record); err != nil {
			return fmt.Errorf("delete relation metadata: %w", err)
		}
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		collection, collectionErr := app.FindCollectionByNameOrId(
			"vibetable_relations",
		)
		if collectionErr != nil {
			return fmt.Errorf("load relation metadata collection: %w", collectionErr)
		}
		record = core.NewRecord(collection)
	} else if err != nil {
		return fmt.Errorf("load relation metadata: %w", err)
	}
	record.Set("relation_id", tableID+"."+definition.Identity.FieldID)
	record.Set("source_table_id", tableID)
	record.Set("source_field_id", definition.Identity.FieldID)
	record.Set("target_table_id", definition.Relation.TargetTableID)
	record.Set("cardinality", definition.Relation.Cardinality)
	record.Set("delete_policy", definition.Relation.DeletePolicy)
	record.Set("pair_id", definition.Relation.PairID)
	record.Set("reciprocal_field_id", definition.Relation.ReciprocalFieldID)
	if err := app.Save(record); err != nil {
		return fmt.Errorf("save relation metadata: %w", err)
	}
	return nil
}

func storedFieldKind(definition v2.FieldDefinition) string {
	switch definition.LogicalType {
	case v2.LogicalRelation:
		return "relation"
	case v2.LogicalFile:
		return "attachment"
	case v2.LogicalFormula:
		return "formula"
	case v2.LogicalLookup:
		return "lookup"
	default:
		return "scalar"
	}
}

func saveTableRevisionAndComputedMetadata(
	ctx context.Context,
	app core.App,
	plan v2.FieldChangePlan,
	nextRevision int64,
) error {
	catalog := NewCatalog(app)
	record, err := catalog.tableRecord(app, plan.Intent.TableID)
	if err != nil {
		return err
	}
	record.Set("schema_revision", nextRevision)
	fields, err := catalog.Fields(ctx, plan.Intent.TableID, false)
	if err != nil {
		return err
	}
	record.Set(
		"primary_display_field_id",
		selectPrimaryDisplayField(fields, record.GetString("primary_display_field_id")),
	)
	if err := app.Save(record); err != nil {
		return fmt.Errorf("save table schema revision: %w", err)
	}
	return schemaapi.New(app).SyncComputedMetadataForTable(ctx, plan.Intent.TableID)
}

func selectPrimaryDisplayField(
	fields []v2.FieldDefinition,
	current string,
) string {
	for _, field := range fields {
		if field.Identity.FieldID == current && isDisplayField(field) {
			return current
		}
	}
	for _, field := range fields {
		if isDisplayField(field) {
			return field.Identity.FieldID
		}
	}
	if len(fields) != 0 {
		return fields[0].Identity.FieldID
	}
	return ""
}

func isDisplayField(field v2.FieldDefinition) bool {
	return field.Lifecycle.State == v2.LifecycleActive &&
		field.LogicalType != v2.LogicalRelation &&
		field.LogicalType != v2.LogicalFormula &&
		field.LogicalType != v2.LogicalLookup &&
		field.LogicalType != v2.LogicalAutoDate
}

func formulaDefinitionChanged(before, after *v2.FieldDefinition) bool {
	if before == nil || after == nil || before.Formula == nil || after.Formula == nil {
		return false
	}
	return before.Formula.Language != after.Formula.Language ||
		before.Formula.Source != after.Formula.Source ||
		before.Formula.ResultType != after.Formula.ResultType
}

func formulaNeedsBackfill(before, after *v2.FieldDefinition) bool {
	if after == nil || after.Formula == nil {
		return false
	}
	return before == nil || before.Formula == nil || formulaDefinitionChanged(before, after)
}

func clearComputedPhysicalValue(
	ctx context.Context,
	app core.App,
	collectionName string,
	physicalName string,
) error {
	query := fmt.Sprintf(
		"UPDATE `%s` SET `%s` = NULL",
		strings.ReplaceAll(collectionName, "`", "``"),
		strings.ReplaceAll(physicalName, "`", "``"),
	)
	if _, err := app.DB().NewQuery(query).WithContext(ctx).Execute(); err != nil {
		return fmt.Errorf("clear stale computed values: %w", err)
	}
	return nil
}

func validateApplyRequest(request v2.ApplyRequest) error {
	if request.PlanID == "" {
		return productError("field.contract.invalid", "planId", "planId is required", nil)
	}
	if request.PlanHash == "" {
		return productError("field.contract.invalid", "planHash", "planHash is required", nil)
	}
	if request.OperationID == "" {
		return productError(
			"field.contract.invalid", "operationId", "operationId is required", nil,
		)
	}
	if request.Actor.ID == "" || request.Actor.Kind == "" {
		return productError("field.contract.invalid", "actor", "actor is required", nil)
	}
	return nil
}

func validateConfirmations(plan v2.FieldChangePlan, supplied []string) error {
	present := make(map[string]struct{}, len(supplied))
	for _, confirmation := range supplied {
		present[confirmation] = struct{}{}
	}
	for _, required := range plan.Confirmations {
		if _, ok := present[required]; !ok {
			return productError(
				"field.change.confirmation_required", "confirmations",
				"required confirmation is missing",
				map[string]any{"required": required},
			)
		}
	}
	if plan.Intent.Action == v2.ActionPurge {
		if plan.Intent.BackupReceipt == "" {
			return productError(
				"field.purge.backup_required", "backupReceipt",
				"a verified backup receipt is required", nil,
			)
		}
		if plan.Before == nil || plan.Intent.Confirmation != plan.Before.DisplayName {
			return productError(
				"field.purge.confirmation_invalid", "confirmation",
				"field display name confirmation does not match", nil,
			)
		}
	}
	return nil
}

func loadReplay(
	app core.App,
	operationID string,
	requestHash string,
	now time.Time,
) (*v2.ApplyReceipt, error) {
	record, err := app.FindFirstRecordByFilter(
		"vibetable_idempotency_keys",
		"key={:key}",
		dbx.Params{"key": "field-v2:" + operationID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load field operation replay: %w", err)
	}
	if !record.GetDateTime("expires_at").Time().After(now) {
		if err := app.Delete(record); err != nil {
			return nil, fmt.Errorf("delete expired field operation: %w", err)
		}
		return nil, nil
	}
	if record.GetString("request_hash") != requestHash {
		return nil, productError(
			"field.change.operation_conflict", "operationId",
			"operationId was used for another field change", nil,
		)
	}
	raw, err := json.Marshal(record.GetRaw("receipt_json"))
	if err != nil {
		return nil, fmt.Errorf("encode replayed field receipt: %w", err)
	}
	var receipt v2.ApplyReceipt
	if err := v2.StrictDecode(raw, &receipt); err != nil {
		return nil, fmt.Errorf("decode replayed field receipt: %w", err)
	}
	return &receipt, nil
}

func saveReplay(
	app core.App,
	operationID string,
	requestHash string,
	receipt v2.ApplyReceipt,
	now time.Time,
) error {
	collection, err := app.FindCollectionByNameOrId("vibetable_idempotency_keys")
	if err != nil {
		return fmt.Errorf("load field idempotency collection: %w", err)
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode field receipt: %w", err)
	}
	record := core.NewRecord(collection)
	record.Set("key", "field-v2:"+operationID)
	record.Set("request_hash", requestHash)
	record.Set("status", "applied")
	record.Set("receipt_json", types.JSONRaw(raw))
	record.Set("expires_at", now.Add(fieldOperationTTL))
	if err := app.Save(record); err != nil {
		return fmt.Errorf("save field operation replay: %w", err)
	}
	return nil
}

func saveSchemaAudit(
	ctx context.Context,
	app core.App,
	plan v2.FieldChangePlan,
	request v2.ApplyRequest,
	receipt v2.ApplyReceipt,
	now time.Time,
) error {
	collection, err := app.FindCollectionByNameOrId("vibetable_schema_audit")
	if err != nil {
		return fmt.Errorf("load schema audit collection: %w", err)
	}
	beforeRaw, _ := json.Marshal(plan.Before)
	afterRaw, _ := json.Marshal(plan.After)
	actorRaw, _ := json.Marshal(request.Actor)
	beforeHash, _ := canonicalHash(plan.Before)
	afterHash, _ := canonicalHash(plan.After)
	record := core.NewRecord(collection)
	record.Set("operation_id", request.OperationID)
	record.Set("plan_id", plan.PlanID)
	record.Set("action", plan.Intent.Action)
	record.Set("table_id", plan.Intent.TableID)
	record.Set("field_id", receipt.FieldID)
	record.Set("before_hash", beforeHash)
	record.Set("after_hash", afterHash)
	record.Set("before_definition_json", types.JSONRaw(beforeRaw))
	record.Set("after_definition_json", types.JSONRaw(afterRaw))
	outcome := "applied"
	if receipt.MigrationJobID != "" {
		outcome = "queued"
		record.Set("migration_job_id", receipt.MigrationJobID)
	}
	record.Set("outcome", outcome)
	record.Set("actor_json", types.JSONRaw(actorRaw))
	record.Set("occurred_at", now)
	record.Set("backup_receipt", plan.Intent.BackupReceipt)
	if err := app.Save(record); err != nil {
		return fmt.Errorf("save schema audit: %w", err)
	}
	return schemaaudit.SaveOutbox(ctx, app, schemaaudit.Event{
		OperationID:    request.OperationID,
		PlanID:         plan.PlanID,
		Action:         string(plan.Intent.Action),
		TableID:        plan.Intent.TableID,
		FieldID:        receipt.FieldID,
		SchemaRevision: receipt.SchemaRevision,
		BeforeHash:     beforeHash,
		AfterHash:      afterHash,
		Actor:          request.Actor,
		Outcome:        "applied",
		MigrationJobID: receipt.MigrationJobID,
	}, now)
}

func saveFailedSchemaAudit(
	app core.App,
	plan v2.FieldChangePlan,
	request v2.ApplyRequest,
	errorCode string,
	now time.Time,
) error {
	if existing, err := app.FindFirstRecordByFilter(
		"vibetable_schema_audit",
		"operation_id={:operation}",
		dbx.Params{"operation": request.OperationID},
	); err == nil && existing != nil {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load failed schema audit: %w", err)
	}
	collection, err := app.FindCollectionByNameOrId("vibetable_schema_audit")
	if err != nil {
		return fmt.Errorf("load schema audit collection: %w", err)
	}
	beforeRaw, _ := json.Marshal(plan.Before)
	afterRaw, _ := json.Marshal(plan.After)
	actorRaw, _ := json.Marshal(request.Actor)
	beforeHash, _ := canonicalHash(plan.Before)
	afterHash, _ := canonicalHash(plan.After)
	record := core.NewRecord(collection)
	record.Set("operation_id", request.OperationID)
	record.Set("plan_id", plan.PlanID)
	record.Set("action", plan.Intent.Action)
	record.Set("table_id", plan.Intent.TableID)
	record.Set("field_id", fieldIDAfter(plan))
	record.Set("before_hash", beforeHash)
	record.Set("after_hash", afterHash)
	record.Set("before_definition_json", types.JSONRaw(beforeRaw))
	record.Set("after_definition_json", types.JSONRaw(afterRaw))
	record.Set("outcome", "failed")
	record.Set("error_code", errorCode)
	record.Set("actor_json", types.JSONRaw(actorRaw))
	record.Set("occurred_at", now)
	record.Set("backup_receipt", plan.Intent.BackupReceipt)
	if err := app.Save(record); err != nil {
		return fmt.Errorf("save failed schema audit: %w", err)
	}
	return nil
}

func stableFieldErrorCode(err error) string {
	var productErr *ProductError
	if errors.As(err, &productErr) && productErr.Code != "" {
		return productErr.Code
	}
	return "field.change.apply_failed"
}

func markPlanApplied(app core.App, planID string, operationID string) error {
	record, err := app.FindFirstRecordByFilter(
		"vibetable_schema_change_plans",
		"plan_id={:plan}",
		dbx.Params{"plan": planID},
	)
	if err != nil {
		return fmt.Errorf("load applied field plan: %w", err)
	}
	existing := record.GetString("applied_operation_id")
	if existing != "" && existing != operationID {
		return productError(
			"field.change.operation_conflict", "operationId",
			"plan was already applied by another operation", nil,
		)
	}
	record.Set("status", "applied")
	record.Set("applied_operation_id", operationID)
	if err := app.Save(record); err != nil {
		return fmt.Errorf("mark field plan applied: %w", err)
	}
	return nil
}

func fieldIDAfter(plan v2.FieldChangePlan) string {
	if plan.After != nil {
		return plan.After.Identity.FieldID
	}
	if plan.Before != nil {
		return plan.Before.Identity.FieldID
	}
	return plan.Intent.FieldID
}
