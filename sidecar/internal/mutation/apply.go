package mutation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

const (
	idempotencyTTL          = 24 * time.Hour
	committedPublishTimeout = 5 * time.Second
	maxSafeCounter          = int64(1<<53 - 1)
)

type operationResult struct {
	recordID string
	kind     OperationKind
}

func (kernel *Kernel) Apply(ctx context.Context, request Request) (Receipt, error) {
	if err := validateRequestShape(request); err != nil {
		return Receipt{}, err
	}
	requestHash, err := canonicalHash(request)
	if err != nil {
		return Receipt{}, mutationError("mutation.request.invalid", nil, "mutation request cannot be canonicalized", nil, false)
	}
	leader, err := kernel.coordinator.begin(request.IdempotencyKey, requestHash)
	if err != nil {
		return Receipt{}, err
	}
	if !leader {
		return pendingReceipt(), nil
	}
	defer kernel.coordinator.end(request.IdempotencyKey, requestHash)
	if err := kernel.coordinator.acquire(ctx); err != nil {
		return Receipt{}, err
	}
	gateHeld := true
	defer func() {
		if gateHeld {
			kernel.coordinator.release()
		}
	}()

	var receipt Receipt
	var emittedEventIDs []string
	err = kernel.app.RunInTransaction(func(txApp core.App) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		replayed, found, replayErr := kernel.replayOrReject(txApp, request, requestHash)
		if replayErr != nil {
			return replayErr
		}
		if found {
			receipt = replayed
			return nil
		}

		preview, previewErr := kernel.preview(ctx, txApp, request)
		if previewErr != nil {
			return previewErr
		}
		definition := preview.Definition
		tableMeta, collection, metadataErr := loadTableMetadata(txApp, request.TableID)
		if metadataErr != nil {
			return metadataErr
		}
		dataRevision, revisionErr := storedNonNegativeInteger(
			tableMeta.GetRaw("data_revision"), "mutation.metadata.invalid_data_revision",
		)
		if revisionErr != nil {
			return revisionErr
		}
		if dataRevision >= maxSafeCounter {
			return mutationError(
				"mutation.metadata.data_revision_exhausted", nil,
				"table data revision cannot be incremented", nil, false,
			)
		}
		if guardErr := kernel.validateGlobalGuard(
			ctx, txApp, definition, collection, request, preview.Operations,
		); guardErr != nil {
			return guardErr
		}

		changeSetID := kernel.newID("changeSet")
		recordStates := map[string]*core.Record{}
		deleted := map[string]bool{}
		baseRowRevisions := map[string]int64{}
		finalRows := map[string]map[string]any{}
		computedFields := map[string]map[string]any{}
		operationResults := make([]operationResult, 0, len(preview.Operations))
		recordIDs := make([]string, 0, len(preview.Operations))
		seenRecordIDs := map[string]struct{}{}

		for index, operation := range preview.Operations {
			if err := ctx.Err(); err != nil {
				return err
			}
			record, before, resolveErr := kernel.resolveOperationRecord(
				txApp, collection, definition, operation, recordStates, deleted,
			)
			if resolveErr != nil {
				return resolveErr
			}
			recordID := record.Id
			if _, seen := seenRecordIDs[recordID]; !seen {
				seenRecordIDs[recordID] = struct{}{}
				recordIDs = append(recordIDs, recordID)
				rowRevision, countErr := committedRowRevision(
					ctx, txApp, request.TableID, recordID,
				)
				if countErr != nil {
					return countErr
				}
				baseRowRevisions[recordID] = rowRevision
			}
			if guardErr := validateOperationGuard(
				before,
				baseRowRevisions[recordID],
				operation,
				index,
			); guardErr != nil {
				return guardErr
			}

			after, applyErr := kernel.applyOperation(
				ctx, txApp, definition, record, operation, before, computedFields,
			)
			if applyErr != nil {
				return applyErr
			}
			if operation.Kind == OperationDelete {
				deleted[recordID] = true
				delete(recordStates, recordID)
			} else {
				recordStates[recordID] = record
			}
			if after == nil {
				finalRows[recordID] = map[string]any{"id": recordID}
			} else {
				finalRows[recordID] = after
			}
			if err := kernel.injectFault("after_record"); err != nil {
				return err
			}
			if err := saveAudit(
				txApp, changeSetID, index+1, request, definition,
				recordID, operation.Kind, before, after, dataRevision+1, kernel.now(),
			); err != nil {
				return err
			}
			if err := kernel.injectFault("after_audit"); err != nil {
				return err
			}
			operationResults = append(operationResults, operationResult{
				recordID: recordID, kind: operation.Kind,
			})
		}

		nextDataRevision := dataRevision + 1
		tableMeta.Set("data_revision", nextDataRevision)
		if err := txApp.Save(tableMeta); err != nil {
			return storageFailure()
		}
		eventID := kernel.newID("event")
		event := DataChangedEvent{
			ContractVersion: ContractVersion, Topic: "data.changed",
			EventID: eventID, Sequence: nextDataRevision,
			OccurredAt:     kernel.now().UTC().Format(time.RFC3339),
			SchemaRevision: definition.SchemaRevision,
			DataRevision:   formatRevision("data", nextDataRevision),
			ChangeSetID:    &changeSetID, TableID: request.TableID,
			RecordIDs: recordIDs, Operation: eventOperation(operationResults),
		}
		if err := saveOutbox(txApp, event); err != nil {
			return err
		}
		if err := kernel.injectFault("after_outbox"); err != nil {
			return err
		}

		affectedRows := make([]AffectedRow, 0, len(operationResults))
		for _, result := range operationResults {
			digest, digestErr := canonicalDigest(finalRows[result.recordID])
			if digestErr != nil {
				return storageFailure()
			}
			affectedRows = append(affectedRows, AffectedRow{
				RecordID: result.recordID, Operation: result.kind,
				Revision: formatRevision("row", baseRowRevisions[result.recordID]+1),
				Digest:   digest,
			})
		}
		newRevision := event.DataRevision
		receipt = Receipt{
			ContractVersion: ContractVersion, Status: StatusApplied,
			ChangeSetID: &changeSetID, AffectedRows: affectedRows,
			ComputedFields: computedFields, NewRevision: &newRevision,
			EmittedEvents: []string{eventID}, Warnings: []ProductError{},
		}
		if err := saveIdempotency(txApp, request, requestHash, receipt, kernel.now()); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := kernel.injectFault("before_commit"); err != nil {
			return err
		}
		emittedEventIDs = []string{eventID}
		return nil
	})
	kernel.coordinator.release()
	gateHeld = false
	if err != nil {
		return Receipt{}, err
	}
	if receipt.Status == StatusApplied && kernel.publisher != nil {
		if publishErr := kernel.publishCommitted(ctx, emittedEventIDs); publishErr != nil {
			receipt.Warnings = append(receipt.Warnings, ProductError{
				ContractVersion: ContractVersion,
				Code:            "mutation.realtime.publish_pending",
				Message:         "committed realtime events remain available in the durable outbox",
				Details: map[string]any{
					"eventIds": append([]string(nil), emittedEventIDs...),
				},
				Retryable: true,
			})
		}
	}
	return receipt, nil
}

func validateOperationGuard(
	before map[string]any,
	revision int64,
	operation NormalizedOperation,
	index int,
) error {
	if operation.ExpectedRevision == nil && operation.ExpectedDigest == nil {
		return nil
	}
	if operation.Kind == OperationInsert || before == nil {
		return mutationError(
			"mutation.guard.invalid",
			stringPointer(fmt.Sprintf("operations[%d]", index)),
			"row guards require an existing record", nil, false,
		)
	}
	if operation.ExpectedRevision != nil {
		actual := formatRevision("row", revision)
		if actual != *operation.ExpectedRevision {
			return mutationError(
				"mutation.revision_conflict",
				stringPointer(fmt.Sprintf(
					"operations[%d].expectedRevision", index,
				)),
				"row revision does not match",
				map[string]any{
					"expected": *operation.ExpectedRevision,
					"actual":   actual,
				},
				false,
			)
		}
	}
	if operation.ExpectedDigest != nil {
		actual, err := canonicalDigest(before)
		if err != nil {
			return storageFailure()
		}
		if actual != *operation.ExpectedDigest {
			return mutationError(
				"mutation.digest_conflict",
				stringPointer(fmt.Sprintf(
					"operations[%d].expectedDigest", index,
				)),
				"row digest does not match",
				map[string]any{
					"expected": *operation.ExpectedDigest,
					"actual":   actual,
				},
				false,
			)
		}
	}
	return nil
}

func pendingReceipt() Receipt {
	return Receipt{
		ContractVersion: ContractVersion,
		Status:          StatusPending,
		AffectedRows:    []AffectedRow{},
		ComputedFields:  map[string]map[string]any{},
		EmittedEvents:   []string{},
		Warnings:        []ProductError{},
	}
}

func eventOperation(results []operationResult) DataChangeOperation {
	if len(results) == 0 {
		return DataChangeUpdate
	}
	first := results[0].kind
	for _, result := range results[1:] {
		if result.kind != first {
			return DataChangeUpdate
		}
	}
	switch first {
	case OperationInsert:
		return DataChangeInsert
	case OperationArchive:
		return DataChangeArchive
	case OperationRestore:
		return DataChangeRestore
	case OperationDelete:
		return DataChangeDelete
	default:
		return DataChangeUpdate
	}
}

func (kernel *Kernel) resolveOperationRecord(
	app core.App,
	collection *core.Collection,
	definition schema.TableDefinition,
	operation NormalizedOperation,
	states map[string]*core.Record,
	deleted map[string]bool,
) (*core.Record, map[string]any, error) {
	if operation.Kind == OperationInsert {
		recordID := kernel.newID("record")
		if operation.RecordID != nil {
			recordID = *operation.RecordID
		}
		if recordID == "" {
			return nil, nil, mutationError("mutation.record.missing_id", nil, "insert record id generation failed", nil, false)
		}
		if _, exists := states[recordID]; exists {
			return nil, nil, mutationError("mutation.record.already_exists", nil, "record already exists", nil, false)
		}
		if deleted[recordID] {
			return nil, nil, mutationError(
				"mutation.record.deleted_in_batch", nil,
				"record was already deleted in this batch", nil, false,
			)
		}
		if _, err := app.FindRecordById(collection, recordID); err == nil {
			return nil, nil, mutationError("mutation.record.already_exists", nil, "record already exists", nil, false)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, storageFailure()
		}
		record := core.NewRecord(collection)
		record.Set("id", recordID)
		return record, nil, nil
	}
	recordID := *operation.RecordID
	if deleted[recordID] {
		return nil, nil, mutationError("mutation.record.deleted_in_batch", nil, "record was already deleted in this batch", nil, false)
	}
	record := states[recordID]
	if record == nil {
		found, err := app.FindRecordById(collection, recordID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil, mutationError("mutation.record.not_found", nil, "record was not found", nil, false)
			}
			return nil, nil, storageFailure()
		}
		record = found
	}
	return record, productRow(definition, record), nil
}

func (kernel *Kernel) applyOperation(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	record *core.Record,
	operation NormalizedOperation,
	before map[string]any,
	computedFields map[string]map[string]any,
) (map[string]any, error) {
	var finalizeAttachments AttachmentFinalizer
	switch operation.Kind {
	case OperationInsert, OperationUpdate:
		if operation.Kind == OperationUpdate &&
			directlyUnarchives(definition, before, operation.Values) {
			return nil, mutationError(
				"mutation.archive.requires_operation", nil,
				"archived records require a restore operation", nil, false,
			)
		}
		for name, value := range operation.Values {
			field, exists := fieldByPhysicalName(definition, name)
			if !exists {
				return nil, mutationError(
					"mutation.schema.field_missing", nil,
					"normalized field is missing from the active schema",
					map[string]any{"field": name}, false,
				)
			}
			storageValue, err := encodeFieldStorageValue(record, field, value)
			if err != nil {
				return nil, mutationError(
					"mutation.schema.storage_mismatch", nil,
					"field value cannot be represented by the active storage schema",
					map[string]any{"fieldId": field.FieldID, "cause": err.Error()},
					false,
				)
			}
			record.Set(name, storageValue)
		}
	case OperationArchive:
		if err := kernel.applyArchive(app, definition, record); err != nil {
			return nil, err
		}
	case OperationRestore:
		if err := kernel.applyRestore(app, definition, record); err != nil {
			return nil, err
		}
	case OperationDelete:
		if err := validateRestrictedDelete(
			ctx, app, definition.TableID, record.Id,
		); err != nil {
			return nil, err
		}
		if kernel.attachments != nil {
			if err := kernel.attachments.CleanupRecord(ctx, app, definition, record); err != nil {
				return nil, err
			}
		}
		if err := app.Delete(record); err != nil {
			return nil, storageFailure()
		}
		return nil, nil
	case OperationSetAttachments:
		if kernel.attachments == nil || operation.Attachment == nil {
			return nil, mutationError(
				"mutation.attachment.unavailable", nil,
				"managed attachment service is unavailable", nil, false,
			)
		}
		var err error
		finalizeAttachments, err = kernel.attachments.Prepare(
			ctx, app, definition, record, *operation.Attachment,
		)
		if err != nil {
			return nil, err
		}
	default:
		return nil, mutationError("mutation.operation.unsupported", nil, "operation is not supported", nil, false)
	}
	if kernel.formulas != nil {
		calculated, err := kernel.formulas.Calculate(ctx, app, definition, record)
		if err != nil {
			var formulaErr *formula.Error
			if errors.As(err, &formulaErr) {
				return nil, formulaErr
			}
			var productErr *ProductError
			if errors.As(err, &productErr) {
				return nil, productErr
			}
			return nil, mutationError("mutation.formula.failed", nil, "formula calculation failed", nil, false)
		}
		normalized, err := normalizeComputedFields(definition, calculated)
		if err != nil {
			return nil, err
		}
		for name, value := range normalized {
			record.Set(name, value)
		}
		if len(normalized) > 0 {
			computedFields[record.Id] = mergeMaps(computedFields[record.Id], normalized)
		}
	}
	if err := app.Save(record); err != nil {
		return nil, storageValidationFailure(err)
	}
	if finalizeAttachments != nil {
		if err := finalizeAttachments(app, record); err != nil {
			return nil, err
		}
	}
	_ = before
	return productRow(definition, record), nil
}

func storageValidationFailure(err error) *ProductError {
	var validationErrors validation.Errors
	if !errors.As(err, &validationErrors) {
		return mutationError(
			"mutation.validation.failed", nil,
			"record failed product validation", nil, false,
		)
	}
	path, message := firstStorageValidationError(validationErrors, "")
	return mutationError(
		"mutation.validation.failed", stringPointer(path), message, nil, false,
	)
}

func firstStorageValidationError(
	errs validation.Errors,
	prefix string,
) (string, string) {
	keys := make([]string, 0, len(errs))
	for key := range errs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		var nested validation.Errors
		if errors.As(errs[key], &nested) {
			return firstStorageValidationError(nested, path)
		}
		var fieldError validation.Error
		if errors.As(errs[key], &fieldError) {
			return path, fieldError.Message()
		}
		return path, "field failed storage validation"
	}
	return prefix, "record failed product validation"
}

func directlyUnarchives(
	definition schema.TableDefinition,
	before map[string]any,
	values map[string]any,
) bool {
	field, err := archiveField(definition)
	if err != nil {
		return false
	}
	if _, changesArchiveField := values[field.PhysicalName]; !changesArchiveField {
		return false
	}
	switch definition.ArchivePolicy.Mode {
	case schema.ArchiveModeStatus:
		return schema.ProductValuesEqual(
			before[field.PhysicalName],
			definition.ArchivePolicy.ArchivedValue,
		)
	case schema.ArchiveModeDeletedAt:
		return stringValue(before[field.PhysicalName]) != ""
	default:
		return false
	}
}

func loadTableMetadata(app core.App, tableID string) (*core.Record, *core.Collection, error) {
	meta, err := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": tableID},
	)
	if err != nil {
		return nil, nil, storageFailure()
	}
	collection, err := app.FindCollectionByNameOrId(meta.GetString("collection_id"))
	if err != nil {
		return nil, nil, storageFailure()
	}
	return meta, collection, nil
}

func (kernel *Kernel) validateGlobalGuard(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	collection *core.Collection,
	request Request,
	operations []NormalizedOperation,
) error {
	if request.ExpectedRevision == nil && request.ExpectedDigest == nil {
		return nil
	}
	recordIDs := map[string]struct{}{}
	for _, operation := range operations {
		if operation.Kind == OperationInsert || operation.RecordID == nil {
			return mutationError(
				"mutation.guard.invalid", guardPath(request),
				"global row guards require only operations on one existing record",
				nil, false,
			)
		}
		recordIDs[*operation.RecordID] = struct{}{}
	}
	if len(recordIDs) != 1 {
		return mutationError(
			"mutation.guard.invalid", guardPath(request),
			"global row guards require exactly one existing record", nil, false,
		)
	}
	var recordID string
	for id := range recordIDs {
		recordID = id
	}
	record, err := app.FindRecordById(collection, recordID)
	if err != nil {
		return mutationError("mutation.record.not_found", nil, "guarded record was not found", nil, false)
	}
	if request.ExpectedRevision != nil {
		revision, countErr := committedRowRevision(
			ctx, app, request.TableID, recordID,
		)
		if countErr != nil {
			return countErr
		}
		actual := formatRevision("row", revision)
		if actual != *request.ExpectedRevision {
			return mutationError(
				"mutation.revision_conflict", stringPointer("expectedRevision"),
				"row revision does not match", map[string]any{"expected": *request.ExpectedRevision, "actual": actual}, false,
			)
		}
	}
	if request.ExpectedDigest != nil {
		actual, digestErr := canonicalDigest(productRow(definition, record))
		if digestErr != nil {
			return storageFailure()
		}
		if actual != *request.ExpectedDigest {
			return mutationError(
				"mutation.digest_conflict", stringPointer("expectedDigest"),
				"row digest does not match", map[string]any{"expected": *request.ExpectedDigest, "actual": actual}, false,
			)
		}
	}
	return nil
}

func guardPath(request Request) *string {
	if request.ExpectedRevision != nil {
		return stringPointer("expectedRevision")
	}
	return stringPointer("expectedDigest")
}

func committedRowRevision(
	ctx context.Context,
	app core.App,
	tableID, recordID string,
) (int64, error) {
	var count int64
	if err := app.ConcurrentDB().NewQuery(`
		SELECT COUNT(DISTINCT change_set_id)
		FROM vibetable_audit_events
		WHERE table_id = {:table} AND record_id = {:record}
	`).WithContext(ctx).Bind(dbx.Params{
		"table": tableID, "record": recordID,
	}).Row(&count); err != nil || count < 0 {
		return 0, storageFailure()
	}
	return count, nil
}

func saveAudit(
	app core.App,
	changeSetID string,
	sequence int,
	request Request,
	definition schema.TableDefinition,
	recordID string,
	operation OperationKind,
	before, after map[string]any,
	dataRevision int64,
	occurredAt time.Time,
) error {
	collection, err := app.FindCollectionByNameOrId("vibetable_audit_events")
	if err != nil {
		return storageFailure()
	}
	record := core.NewRecord(collection)
	record.Set("change_set_id", changeSetID)
	record.Set("sequence", sequence)
	record.Set("data_revision", dataRevision)
	record.Set("table_id", request.TableID)
	record.Set("record_id", recordID)
	record.Set("operation", string(operation))
	before = redactSensitiveFields(definition, before)
	after = redactSensitiveFields(definition, after)
	if before != nil {
		raw, err := json.Marshal(before)
		if err != nil {
			return storageFailure()
		}
		record.Set("before_json", pbtypes.JSONRaw(raw))
	}
	if after != nil {
		raw, err := json.Marshal(after)
		if err != nil {
			return storageFailure()
		}
		record.Set("after_json", pbtypes.JSONRaw(raw))
	}
	schemaRevision, err := schema.ParseSchemaRevision(definition.SchemaRevision)
	if err != nil {
		return mutationError("mutation.schema_revision.invalid", nil, "stored schema revision is invalid", nil, false)
	}
	record.Set("schema_revision", schemaRevision)
	record.Set("request_id", request.RequestID)
	record.Set("actor_type", request.Actor.Type)
	record.Set("actor_id", request.Actor.ID)
	if request.Actor.DisplayName != nil {
		record.Set("actor_display_name", *request.Actor.DisplayName)
	}
	record.Set("occurred_at", occurredAt.UTC())
	if err := app.Save(record); err != nil {
		return storageFailure()
	}
	return nil
}

func redactSensitiveFields(
	definition schema.TableDefinition,
	image map[string]any,
) map[string]any {
	if image == nil {
		return nil
	}
	result := make(map[string]any, len(image))
	for key, value := range image {
		result[key] = value
	}
	for _, field := range definition.Fields {
		if field.DataType != schema.DataTypeSecret &&
			field.DataType != schema.DataTypeHash {
			continue
		}
		if value, exists := result[field.PhysicalName]; exists {
			result[field.PhysicalName] = map[string]any{
				"redacted": value != nil && fmt.Sprint(value) != "",
			}
		}
	}
	return result
}

func validateRestrictedDelete(
	ctx context.Context,
	app core.App,
	targetTableID string,
	targetRecordID string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	relations, err := app.FindRecordsByFilter(
		"vibetable_relations",
		"delete_policy='restrict'",
		"",
		0,
		0,
		nil,
	)
	if err != nil {
		return storageFailure()
	}
	source := MetadataSchemaSource{}
	for _, relation := range relations {
		sourceDefinition, err := source.Describe(
			ctx, app, relation.GetString("source_table_id"),
		)
		if err != nil {
			return storageFailure()
		}
		var sourceField *schema.FieldDefinition
		for index := range sourceDefinition.Fields {
			if sourceDefinition.Fields[index].FieldID ==
				relation.GetString("source_field_id") {
				sourceField = &sourceDefinition.Fields[index]
				break
			}
		}
		if sourceField == nil || sourceField.Relation == nil {
			return storageFailure()
		}
		relationSpec := sourceField.Relation
		if !relationTargetsTable(*relationSpec, targetTableID) {
			continue
		}
		referenceTable := sourceDefinition
		referenceField := *sourceField
		discriminatorName := ""
		if relationSpec.EffectiveMode() != "direct" {
			if relationSpec.JunctionTableID == nil {
				return storageFailure()
			}
			referenceTable, err = source.Describe(
				ctx, app, *relationSpec.JunctionTableID,
			)
			if err != nil {
				return storageFailure()
			}
			var found bool
			referenceField, found = mutationSchemaFieldByID(
				referenceTable, relationSpec.JunctionTargetFieldID,
			)
			if !found {
				return storageFailure()
			}
			if relationSpec.EffectiveMode() == "m2a" {
				discriminator, discriminatorFound := mutationSchemaFieldByID(
					referenceTable,
					relationSpec.JunctionDiscriminatorFieldID,
				)
				if !discriminatorFound {
					return storageFailure()
				}
				discriminatorName = discriminator.PhysicalName
			}
		}
		references, countErr := countRelationReferences(
			ctx,
			app,
			referenceTable,
			referenceField.PhysicalName,
			targetTableID,
			targetRecordID,
			discriminatorName,
		)
		if countErr != nil {
			return storageFailure()
		}
		if references > 0 {
			return mutationError(
				"mutation.relation.restricted", nil,
				"referenced record cannot be deleted",
				map[string]any{
					"relationId": relation.GetString("relation_id"),
					"tableId":    sourceDefinition.TableID,
					"fieldId":    sourceField.FieldID,
				},
				false,
			)
		}
	}
	return nil
}

func relationTargetsTable(relation schema.RelationSpec, targetTableID string) bool {
	if relation.TargetTableID == targetTableID {
		return true
	}
	if relation.EffectiveMode() != "m2a" {
		return false
	}
	for _, allowedTableID := range relation.AllowedTargetTableIDs {
		if allowedTableID == targetTableID {
			return true
		}
	}
	return false
}

func mutationSchemaFieldByID(
	definition schema.TableDefinition,
	fieldID string,
) (schema.FieldDefinition, bool) {
	for _, field := range definition.Fields {
		if field.FieldID == fieldID {
			return field, true
		}
	}
	return schema.FieldDefinition{}, false
}

func countRelationReferences(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	fieldName string,
	targetTableID string,
	targetRecordID string,
	discriminatorName string,
) (int, error) {
	predicates := make([]string, 0, 3)
	if definition.TableID == targetTableID {
		predicates = append(predicates, "`id` != {:target}")
	}
	predicates = append(predicates, fmt.Sprintf(
		"(`%s` = {:target} OR "+
			"(json_valid(`%s`) AND EXISTS ("+
			"SELECT 1 FROM json_each(`%s`) WHERE value={:target})))",
		fieldName,
		fieldName,
		fieldName,
	))
	params := dbx.Params{"target": targetRecordID}
	if discriminatorName != "" {
		predicates = append(
			predicates,
			fmt.Sprintf("`%s` = {:targetTable}", discriminatorName),
		)
		params["targetTable"] = targetTableID
	}
	var references int
	sql := fmt.Sprintf(
		"SELECT COUNT(*) FROM `%s` WHERE %s",
		definition.PhysicalName,
		strings.Join(predicates, " AND "),
	)
	if err := app.DB().NewQuery(sql).WithContext(ctx).Bind(params).
		Row(&references); err != nil {
		return 0, err
	}
	return references, nil
}

func saveOutbox(app core.App, event DataChangedEvent) error {
	if _, err := storedNonNegativeInteger(0, "mutation.outbox.invalid_attempts"); err != nil {
		return err
	}
	collection, err := app.FindCollectionByNameOrId("vibetable_outbox")
	if err != nil {
		return storageFailure()
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return storageFailure()
	}
	record := core.NewRecord(collection)
	record.Set("event_id", event.EventID)
	record.Set("topic", event.Topic)
	record.Set("payload_json", pbtypes.JSONRaw(payload))
	record.Set("status", "pending")
	record.Set("attempts", 0)
	if err := app.Save(record); err != nil {
		return storageFailure()
	}
	return nil
}

func saveIdempotency(
	app core.App,
	request Request,
	requestHash string,
	receipt Receipt,
	now time.Time,
) error {
	collection, err := app.FindCollectionByNameOrId("vibetable_idempotency_keys")
	if err != nil {
		return storageFailure()
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return storageFailure()
	}
	record := core.NewRecord(collection)
	record.Set("key", request.IdempotencyKey)
	record.Set("request_hash", requestHash)
	record.Set("status", "applied")
	record.Set("receipt_json", pbtypes.JSONRaw(raw))
	record.Set("expires_at", now.Add(idempotencyTTL))
	if err := app.Save(record); err != nil {
		return storageFailure()
	}
	return nil
}

func (kernel *Kernel) replayOrReject(
	app core.App,
	request Request,
	requestHash string,
) (Receipt, bool, error) {
	record, err := app.FindFirstRecordByFilter(
		"vibetable_idempotency_keys", "key={:key}", dbx.Params{"key": request.IdempotencyKey},
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Receipt{}, false, nil
		}
		return Receipt{}, false, storageFailure()
	}
	if !record.GetDateTime("expires_at").Time().After(kernel.now()) {
		if err := app.Delete(record); err != nil {
			return Receipt{}, false, storageFailure()
		}
		return Receipt{}, false, nil
	}
	if record.GetString("request_hash") != requestHash {
		return Receipt{}, false, mutationError(
			"mutation.idempotency_conflict", stringPointer("idempotencyKey"),
			"idempotency key was already used for a different request", nil, false,
		)
	}
	switch record.GetString("status") {
	case "pending":
		return pendingReceipt(), true, nil
	case "applied":
	default:
		return Receipt{}, false, storageFailure()
	}
	raw, marshalErr := json.Marshal(record.GetRaw("receipt_json"))
	if marshalErr != nil {
		return Receipt{}, false, storageFailure()
	}
	var receipt Receipt
	if err := DecodeStrict(raw, &receipt); err != nil ||
		receipt.ContractVersion != ContractVersion ||
		receipt.Status != StatusApplied {
		return Receipt{}, false, storageFailure()
	}
	receipt.Status = StatusReplayed
	return receipt, true, nil
}

func (kernel *Kernel) injectFault(point string) error {
	if err := kernel.fault(point); err != nil {
		return mutationError("mutation.internal.failed", nil, "mutation could not be committed", nil, true)
	}
	return nil
}

func (kernel *Kernel) publishCommitted(
	requestCtx context.Context,
	eventIDs []string,
) error {
	// A successful transaction owns the outbox events independently from the
	// request that initiated it. Preserve request values while cancellation is
	// owned by the publisher lifecycle, then impose a fresh bound so the
	// synchronous durable drain cannot outlive shutdown or a stuck downstream
	// publisher indefinitely.
	publishCtx, cancel := context.WithTimeout(
		publishLifecycleContext{
			Context: kernel.publishCtx,
			values:  requestCtx,
		},
		committedPublishTimeout,
	)
	defer cancel()

	var publishErrors []error
	for _, eventID := range eventIDs {
		if err := publishCtx.Err(); err != nil {
			publishErrors = append(publishErrors, err)
			break
		}
		record, err := kernel.app.FindFirstRecordByFilter(
			"vibetable_outbox", "event_id={:event}", dbx.Params{"event": eventID},
		)
		if err != nil {
			publishErrors = append(
				publishErrors,
				fmt.Errorf("load committed outbox event %s: %w", eventID, err),
			)
			continue
		}
		if _, err := storedNonNegativeInteger(record.GetRaw("attempts"), "mutation.outbox.invalid_attempts"); err != nil {
			publishErrors = append(
				publishErrors,
				fmt.Errorf("validate committed outbox event %s: %w", eventID, err),
			)
			continue
		}
		raw, err := json.Marshal(record.GetRaw("payload_json"))
		if err != nil {
			publishErrors = append(
				publishErrors,
				fmt.Errorf("encode committed outbox event %s: %w", eventID, err),
			)
			continue
		}
		var event DataChangedEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			publishErrors = append(
				publishErrors,
				fmt.Errorf("decode committed outbox event %s: %w", eventID, err),
			)
			continue
		}
		if err := kernel.publisher.Publish(publishCtx, event); err != nil {
			publishErrors = append(
				publishErrors,
				fmt.Errorf("publish committed outbox event %s: %w", eventID, err),
			)
		}
	}
	return errors.Join(publishErrors...)
}

type publishLifecycleContext struct {
	context.Context
	values context.Context
}

func (ctx publishLifecycleContext) Value(key any) any {
	return ctx.values.Value(key)
}

func productRow(definition schema.TableDefinition, record *core.Record) map[string]any {
	fieldNames := make([]string, 0, len(definition.Fields))
	for _, field := range definition.Fields {
		fieldNames = append(fieldNames, field.PhysicalName)
	}
	row := productrow.FromRecord(fieldNames, record)
	for _, field := range definition.Fields {
		if field.DataType == schema.DataTypeSelect ||
			field.DataType == schema.DataTypeMultiSelect {
			row[field.PhysicalName] = schema.DecodeSelectValueFromStorage(
				field,
				row[field.PhysicalName],
			)
		}
	}
	return row
}

func canonicalDigest(row map[string]any) (string, error) {
	return productrow.Digest(row)
}

func canonicalHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func formatRevision(prefix string, revision int64) string {
	return fmt.Sprintf("%s_%04d", prefix, revision)
}

func storedNonNegativeInteger(value any, code string) (int64, error) {
	var number float64
	switch typed := value.(type) {
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case float32:
		number = float64(typed)
	case float64:
		number = typed
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, mutationError(code, nil, "stored counter is invalid", nil, false)
		}
		number = parsed
	default:
		return 0, mutationError(code, nil, "stored counter is invalid", nil, false)
	}
	if number < 0 || number > float64(maxSafeCounter) ||
		math.Trunc(number) != number {
		return 0, mutationError(code, nil, "stored counter must be a non-negative integer", nil, false)
	}
	return int64(number), nil
}

func normalizeComputedFields(
	definition schema.TableDefinition,
	values map[string]any,
) (map[string]any, error) {
	fields := map[string]schema.FieldDefinition{}
	for _, field := range definition.Fields {
		fields[field.FieldID], fields[field.PhysicalName] = field, field
	}
	result := make(map[string]any, len(values))
	for key, value := range values {
		field, ok := fields[key]
		if !ok || (field.Kind != schema.FieldKindFormula && field.Kind != schema.FieldKindLookup) {
			return nil, mutationError("mutation.formula.invalid_output", nil, "formula calculator returned a non-computed field", nil, false)
		}
		result[field.PhysicalName] = value
	}
	return result, nil
}

func mergeMaps(target, values map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for key, value := range values {
		target[key] = value
	}
	return target
}

func storageFailure() *ProductError {
	return mutationError("mutation.storage.failed", nil, "mutation storage operation failed", nil, true)
}
