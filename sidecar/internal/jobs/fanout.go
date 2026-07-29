package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

const (
	formulaFanoutType     = "formula_fanout"
	maxFanoutRecords      = 10_000
	maxRetainedDataEvents = 10_000
)

type DataPublisher interface {
	Publish(context.Context, mutation.DataChangedEvent) error
}

type fanoutCursor struct {
	TableID         string   `json:"tableId"`
	LastRecordID    string   `json:"lastRecordId"`
	RelationFieldID string   `json:"relationFieldId"`
	TargetRecordIDs []string `json:"targetRecordIds"`
	RecordIDs       []string `json:"recordIds"`
}

// Publish durably derives cross-record recalculation jobs before attempting
// live delivery. The retained data outbox is the recovery source if the
// process stops before this method or while fan-out enqueue is incomplete.
func (service *Service) Publish(
	ctx context.Context,
	event mutation.DataChangedEvent,
) error {
	jobIDs, err := service.enqueueFormulaFanout(ctx, event)
	if err != nil {
		return err
	}
	var publishErr error
	if service.dataPublisher != nil {
		publishErr = service.dataPublisher.Publish(ctx, event)
	}
	for _, jobID := range jobIDs {
		service.Start(jobID)
	}
	return publishErr
}

type retainedDataEvent struct {
	EventID     string `db:"event_id"`
	PayloadJSON string `db:"payload_json"`
}

func (service *Service) recoverFormulaFanouts(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dependencies, err := service.app.FindRecordsByFilter(
		"vibetable_formula_dependencies",
		"",
		"",
		0,
		0,
	)
	if err != nil {
		return err
	}
	targetTables := make(map[string]struct{}, len(dependencies))
	for _, dependency := range dependencies {
		if tableID := dependency.GetString("target_table_id"); tableID != "" {
			targetTables[tableID] = struct{}{}
		}
	}
	if len(targetTables) == 0 {
		return nil
	}

	var rows []retainedDataEvent
	if err := service.app.DB().NewQuery(`
		SELECT event_id, payload_json
		FROM vibetable_outbox
		WHERE topic = 'data.changed'
		ORDER BY rowid ASC
		LIMIT {:limit}
	`).Bind(dbx.Params{"limit": maxRetainedDataEvents + 1}).All(&rows); err != nil {
		return err
	}
	if len(rows) > maxRetainedDataEvents {
		return jobError(
			"job.resume_outbox_limit",
			"retained data event recovery exceeds the 10000 event limit",
			false,
		)
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		var event mutation.DataChangedEvent
		if err := mutation.DecodeStrict(
			[]byte(row.PayloadJSON),
			&event,
		); err != nil {
			return jobError(
				"job.resume_outbox_invalid_payload",
				"retained data event payload is malformed",
				false,
			)
		}
		if event.ContractVersion != mutation.ContractVersion ||
			event.Topic != "data.changed" {
			return jobError(
				"job.resume_outbox_contract_mismatch",
				"retained data event contract or topic does not match",
				false,
			)
		}
		if event.EventID != row.EventID {
			return jobError(
				"job.resume_outbox_event_id_mismatch",
				"retained data event identity does not match its outbox row",
				false,
			)
		}
		if _, relevant := targetTables[event.TableID]; !relevant {
			continue
		}
		if _, err := service.enqueueFormulaFanout(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) enqueueFormulaFanout(
	ctx context.Context,
	event mutation.DataChangedEvent,
) ([]string, error) {
	if event.ChangeSetID == nil || *event.ChangeSetID == "" ||
		len(event.RecordIDs) == 0 {
		return []string{}, nil
	}
	dependencies, err := service.app.FindRecordsByFilter(
		"vibetable_formula_dependencies",
		"target_table_id={:table}",
		"+source_table_id,+relation_field_id",
		0,
		0,
		dbx.Params{"table": event.TableID},
	)
	if err != nil {
		return nil, jobError(
			"job.storage_failed",
			"formula dependency metadata could not be read",
			true,
		)
	}
	if len(dependencies) == 0 {
		return []string{}, nil
	}
	changed, err := service.changedTargetFields(
		event.TableID, *event.ChangeSetID,
	)
	if err != nil {
		return nil, err
	}
	type dependencyKey struct {
		tableID         string
		relationFieldID string
	}
	dependenciesByKey := map[dependencyKey][]*core.Record{}
	for _, dependency := range dependencies {
		if _, relevant := changed[dependency.GetString("target_field_id")]; !relevant {
			continue
		}
		key := dependencyKey{
			tableID:         dependency.GetString("source_table_id"),
			relationFieldID: dependency.GetString("relation_field_id"),
		}
		dependenciesByKey[key] = append(dependenciesByKey[key], dependency)
	}
	ordered := make([]dependencyKey, 0, len(dependenciesByKey))
	for key := range dependenciesByKey {
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].tableID == ordered[right].tableID {
			return ordered[left].relationFieldID <
				ordered[right].relationFieldID
		}
		return ordered[left].tableID < ordered[right].tableID
	})
	jobIDs := make([]string, 0, len(ordered))
	for _, key := range ordered {
		jobID, createErr := service.createFanoutJob(
			ctx,
			event,
			key.tableID,
			key.relationFieldID,
			dependenciesByKey[key],
		)
		if createErr != nil {
			return nil, createErr
		}
		if jobID != "" {
			jobIDs = append(jobIDs, jobID)
		}
	}
	return jobIDs, nil
}

func (service *Service) changedTargetFields(
	tableID string,
	changeSetID string,
) (map[string]struct{}, error) {
	definition, err := schemaapi.New(service.app).Describe(
		context.Background(), tableID,
	)
	if err != nil {
		return nil, err
	}
	events, err := service.app.FindRecordsByFilter(
		"vibetable_audit_events",
		"change_set_id={:changeSet}",
		"+sequence",
		0,
		0,
		dbx.Params{"changeSet": changeSetID},
	)
	if err != nil {
		return nil, jobError(
			"job.storage_failed",
			"audit state for formula fan-out could not be read",
			true,
		)
	}
	changed := map[string]struct{}{}
	for _, event := range events {
		before := decodedObject(event.GetRaw("before_json"))
		after := decodedObject(event.GetRaw("after_json"))
		for _, field := range definition.Fields {
			if !reflect.DeepEqual(
				before[field.PhysicalName],
				after[field.PhysicalName],
			) {
				changed[field.FieldID] = struct{}{}
			}
		}
	}
	return changed, nil
}

func (service *Service) createFanoutJob(
	ctx context.Context,
	event mutation.DataChangedEvent,
	sourceTableID string,
	relationFieldID string,
	dependencies []*core.Record,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	existing, err := service.app.FindFirstRecordByFilter(
		"vibetable_jobs",
		"job_type={:type} && source_event_id={:event} && "+
			"source_table_id={:table} && relation_field_id={:field}",
		dbx.Params{
			"type": formulaFanoutType, "event": event.EventID,
			"table": sourceTableID, "field": relationFieldID,
		},
	)
	if err == nil {
		return existing.Id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", jobError(
			"job.storage_failed",
			"formula fan-out idempotency state could not be read",
			true,
		)
	}
	definition, err := schemaapi.New(service.app).Describe(ctx, sourceTableID)
	if err != nil {
		return "", err
	}
	var relationField *schema.FieldDefinition
	for index := range definition.Fields {
		if definition.Fields[index].FieldID == relationFieldID &&
			definition.Fields[index].Relation != nil {
			relationField = &definition.Fields[index]
			break
		}
	}
	if relationField == nil {
		return "", jobError(
			"job.formula_dependency_invalid",
			"formula relation dependency is unavailable",
			false,
		)
	}
	foundRecordIDs := map[string]struct{}{}
	for _, dependency := range dependencies {
		recordIDs, matchErr := service.matchingDependencySourceRecords(
			ctx,
			definition,
			*relationField,
			dependency,
			event.TableID,
			event.RecordIDs,
		)
		if matchErr != nil {
			return "", matchErr
		}
		for _, recordID := range recordIDs {
			foundRecordIDs[recordID] = struct{}{}
		}
		if len(foundRecordIDs) > maxFanoutRecords {
			return "", jobError(
				"job.fanout_limit",
				"formula fan-out exceeds the 10000 record limit",
				false,
			)
		}
	}
	recordIDs := make([]string, 0, len(foundRecordIDs))
	for recordID := range foundRecordIDs {
		recordIDs = append(recordIDs, recordID)
	}
	sort.Strings(recordIDs)
	if len(recordIDs) == 0 {
		return "", nil
	}
	revision, _ := schema.ParseSchemaRevision(definition.SchemaRevision)
	collection, err := service.app.FindCollectionByNameOrId(
		"vibetable_jobs",
	)
	if err != nil {
		return "", jobError(
			"job.storage_failed", "job storage is unavailable", true,
		)
	}
	cursor := fanoutCursor{
		TableID: sourceTableID, RelationFieldID: relationFieldID,
		TargetRecordIDs: append([]string(nil), event.RecordIDs...),
		RecordIDs:       recordIDs,
	}
	cursorRaw, _ := json.Marshal(cursor)
	progressRaw, _ := json.Marshal(Progress{
		Completed: 0, Total: len(recordIDs),
	})
	record := core.NewRecord(collection)
	record.Set("job_type", formulaFanoutType)
	record.Set("state", "queued")
	record.Set("schema_revision", revision)
	record.Set("cursor_json", types.JSONRaw(cursorRaw))
	record.Set("progress_json", types.JSONRaw(progressRaw))
	record.Set("error_json", nil)
	record.Set("source_event_id", event.EventID)
	record.Set("source_table_id", sourceTableID)
	record.Set("relation_field_id", relationFieldID)
	if err := service.app.Save(record); err != nil {
		// A concurrent duplicate resolves to the already-created durable job.
		existing, findErr := service.app.FindFirstRecordByFilter(
			"vibetable_jobs",
			"job_type={:type} && source_event_id={:event} && "+
				"source_table_id={:table} && relation_field_id={:field}",
			dbx.Params{
				"type": formulaFanoutType, "event": event.EventID,
				"table": sourceTableID, "field": relationFieldID,
			},
		)
		if findErr == nil {
			return existing.Id, nil
		}
		return "", jobError(
			"job.storage_failed",
			"formula fan-out job could not be created",
			true,
		)
	}
	if snapshot, getErr := service.Get(ctx, record.Id); getErr == nil {
		service.publish(ctx, snapshot)
	}
	return record.Id, nil
}

func (service *Service) matchingDependencySourceRecords(
	ctx context.Context,
	definition schema.TableDefinition,
	relationField schema.FieldDefinition,
	dependency *core.Record,
	changedTableID string,
	targetRecordIDs []string,
) ([]string, error) {
	computedFieldID := dependency.GetString("formula_field_id")
	for _, field := range definition.Fields {
		if field.FieldID != computedFieldID ||
			field.Kind != schema.FieldKindLookup ||
			field.Lookup == nil {
			continue
		}
		return service.matchingLookupSourceRecords(
			ctx,
			definition,
			field.Lookup.EffectivePath(),
			changedTableID,
			targetRecordIDs,
		)
	}
	return service.matchingSourceRecords(
		definition,
		relationField,
		changedTableID,
		targetRecordIDs,
	)
}

type reverseLookupHop struct {
	source   schema.TableDefinition
	relation schema.FieldDefinition
	step     schema.LookupPathStep
}

func (service *Service) matchingLookupSourceRecords(
	ctx context.Context,
	definition schema.TableDefinition,
	path []schema.LookupPathStep,
	changedTableID string,
	targetRecordIDs []string,
) ([]string, error) {
	if len(path) == 0 {
		return nil, jobError(
			"job.formula_dependency_invalid",
			"lookup dependency path is unavailable",
			false,
		)
	}
	current := definition
	hops := make([]reverseLookupHop, 0, len(path))
	for index, step := range path {
		var relationField *schema.FieldDefinition
		for fieldIndex := range current.Fields {
			field := &current.Fields[fieldIndex]
			if field.FieldID == step.RelationFieldID &&
				field.Relation != nil {
				relationField = field
				break
			}
		}
		if relationField == nil {
			return nil, jobError(
				"job.formula_dependency_invalid",
				"lookup dependency relation is unavailable",
				false,
			)
		}
		hops = append(hops, reverseLookupHop{
			source: current, relation: *relationField, step: step,
		})
		if index == len(path)-1 {
			continue
		}
		targetTableID := relationField.Relation.TargetTableID
		if step.M2ACollection != "" {
			targetTableID = step.M2ACollection
		}
		if relationField.Relation.EffectiveMode() == "m2a" &&
			step.M2ACollection == "" {
			return nil, jobError(
				"job.formula_dependency_invalid",
				"intermediate m2a lookup dependency is ambiguous",
				false,
			)
		}
		target, err := schemaapi.New(service.app).Describe(ctx, targetTableID)
		if err != nil {
			return nil, err
		}
		current = target
	}

	start := len(hops) - 1
	last := hops[start]
	expectedTableID := last.relation.Relation.TargetTableID
	if last.step.M2ACollection != "" {
		expectedTableID = last.step.M2ACollection
	}
	if changedTableID != expectedTableID {
		if last.relation.Relation.EffectiveMode() != "m2a" ||
			last.step.M2ACollection != "" ||
			!containsString(
				last.relation.Relation.AllowedTargetTableIDs,
				changedTableID,
			) {
			foundJunction := false
			for index := len(hops) - 1; index >= 0; index-- {
				relation := hops[index].relation.Relation
				if relation.JunctionTableID != nil &&
					*relation.JunctionTableID == changedTableID {
					start = index
					foundJunction = true
					break
				}
			}
			if !foundJunction {
				return []string{}, nil
			}
		}
	}

	recordIDs := append([]string(nil), targetRecordIDs...)
	currentTargetTableID := changedTableID
	for index := start; index >= 0; index-- {
		hop := hops[index]
		var err error
		recordIDs, err = service.matchingSourceRecords(
			hop.source,
			hop.relation,
			currentTargetTableID,
			recordIDs,
		)
		if err != nil {
			return nil, err
		}
		if len(recordIDs) == 0 {
			return []string{}, nil
		}
		currentTargetTableID = hop.source.TableID
	}
	return recordIDs, nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (service *Service) matchingSourceRecords(
	definition schema.TableDefinition,
	relationField schema.FieldDefinition,
	changedTableID string,
	targetRecordIDs []string,
) ([]string, error) {
	if relationField.Relation != nil &&
		relationField.Relation.EffectiveMode() != "direct" {
		return service.matchingJunctionSourceRecords(
			relationField, changedTableID, targetRecordIDs,
		)
	}
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables",
		"table_id={:table}",
		dbx.Params{"table": definition.TableID},
	)
	if err != nil {
		return nil, jobError(
			"job.storage_failed", "source table storage is unavailable", true,
		)
	}
	collection, err := service.app.FindCollectionByNameOrId(
		meta.GetString("collection_id"),
	)
	if err != nil {
		return nil, jobError(
			"job.storage_failed", "source table storage is unavailable", true,
		)
	}
	rows, err := service.app.FindRecordsByFilter(
		collection, "", "+id", maxFanoutRecords+1, 0,
	)
	if err != nil {
		return nil, jobError(
			"job.storage_failed", "source records could not be read", true,
		)
	}
	if len(rows) > maxFanoutRecords {
		return nil, jobError(
			"job.fanout_limit",
			"formula fan-out exceeds the 10000 record limit",
			false,
		)
	}
	targets := make(map[string]struct{}, len(targetRecordIDs))
	for _, recordID := range targetRecordIDs {
		targets[recordID] = struct{}{}
	}
	result := make([]string, 0)
	for _, row := range rows {
		for _, relationID := range relationIDs(
			row.GetRaw(relationField.PhysicalName),
		) {
			if _, matches := targets[relationID]; matches {
				result = append(result, row.Id)
				break
			}
		}
	}
	return result, nil
}

func (service *Service) matchingJunctionSourceRecords(
	relationField schema.FieldDefinition,
	changedTableID string,
	recordIDs []string,
) ([]string, error) {
	relation := relationField.Relation
	if relation == nil || relation.JunctionTableID == nil {
		return nil, jobError(
			"job.formula_dependency_invalid",
			"junction relation metadata is unavailable",
			false,
		)
	}
	junction, err := schemaapi.New(service.app).Describe(
		context.Background(), *relation.JunctionTableID,
	)
	if err != nil {
		return nil, err
	}
	fieldByID := func(fieldID string) (schema.FieldDefinition, bool) {
		for _, field := range junction.Fields {
			if field.FieldID == fieldID {
				return field, true
			}
		}
		return schema.FieldDefinition{}, false
	}
	sourceField, sourceOK := fieldByID(relation.JunctionSourceFieldID)
	targetField, targetOK := fieldByID(relation.JunctionTargetFieldID)
	if !sourceOK || !targetOK {
		return nil, jobError(
			"job.formula_dependency_invalid",
			"junction dependency fields are unavailable",
			false,
		)
	}
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}",
		dbx.Params{"table": junction.TableID},
	)
	if err != nil {
		return nil, jobError(
			"job.storage_failed", "junction storage is unavailable", true,
		)
	}
	collection, err := service.app.FindCollectionByNameOrId(
		meta.GetString("collection_id"),
	)
	if err != nil {
		return nil, jobError(
			"job.storage_failed", "junction storage is unavailable", true,
		)
	}
	wanted := make(map[string]struct{}, len(recordIDs))
	for _, recordID := range recordIDs {
		wanted[recordID] = struct{}{}
	}
	rows, err := service.app.FindRecordsByFilter(
		collection, "", "+id", maxFanoutRecords+1, 0,
	)
	if err != nil {
		return nil, jobError(
			"job.storage_failed", "junction records could not be read", true,
		)
	}
	if len(rows) > maxFanoutRecords {
		return nil, jobError(
			"job.fanout_limit",
			"formula fan-out exceeds the 10000 junction record limit",
			false,
		)
	}
	var discriminator schema.FieldDefinition
	if relation.EffectiveMode() == "m2a" {
		var discriminatorOK bool
		discriminator, discriminatorOK = fieldByID(
			relation.JunctionDiscriminatorFieldID,
		)
		if !discriminatorOK {
			return nil, jobError(
				"job.formula_dependency_invalid",
				"m2a discriminator field is unavailable",
				false,
			)
		}
	}
	found := map[string]struct{}{}
	for _, row := range rows {
		matches := false
		if changedTableID == junction.TableID {
			_, matches = wanted[row.Id]
		} else {
			for _, targetID := range relationIDs(row.GetRaw(targetField.PhysicalName)) {
				if _, ok := wanted[targetID]; ok {
					matches = true
					break
				}
			}
			if matches && relation.EffectiveMode() == "m2a" &&
				row.GetString(discriminator.PhysicalName) != changedTableID {
				matches = false
			}
		}
		if !matches {
			continue
		}
		for _, sourceID := range relationIDs(row.GetRaw(sourceField.PhysicalName)) {
			if sourceID != "" {
				found[sourceID] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(found))
	for recordID := range found {
		result = append(result, recordID)
	}
	sort.Strings(result)
	return result, nil
}

func (service *Service) runFanout(
	ctx context.Context,
	record *core.Record,
	snapshot Snapshot,
) error {
	if service.kernel == nil {
		return service.fail(
			record, snapshot.TableID,
			jobError(
				"job.kernel_unavailable",
				"mutation kernel is unavailable",
				true,
			),
		)
	}
	raw, _ := json.Marshal(record.GetRaw("cursor_json"))
	var cursor fanoutCursor
	if json.Unmarshal(raw, &cursor) != nil ||
		cursor.TableID != snapshot.TableID ||
		len(cursor.RecordIDs) != snapshot.Progress.Total {
		return service.fail(
			record, snapshot.TableID,
			jobError(
				"job.storage_corrupt",
				"formula fan-out cursor is invalid",
				false,
			),
		)
	}
	for snapshot.Progress.Completed < len(cursor.RecordIDs) {
		if err := ctx.Err(); err != nil {
			return err
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
				record, snapshot.TableID,
				jobError(
					"job.schema_revision_conflict",
					"schema changed during formula fan-out",
					false,
				),
			)
		}
		end := snapshot.Progress.Completed + backfillBatchSize
		if end > len(cursor.RecordIDs) {
			end = len(cursor.RecordIDs)
		}
		batch := cursor.RecordIDs[snapshot.Progress.Completed:end]
		operations := make([]mutation.Operation, 0, len(batch))
		for _, itemID := range batch {
			itemID := itemID
			operations = append(operations, mutation.Operation{
				Kind: mutation.OperationUpdate, RecordID: &itemID,
				Values: map[string]any{},
			})
		}
		key := fmt.Sprintf(
			"fanout_%s_%d_%d", record.Id,
			snapshot.Progress.Completed, end,
		)
		if _, err := service.applyKernelBatch(
			ctx,
			"formula.fanout.batch",
			key,
			mutation.Request{
				ContractVersion: mutation.ContractVersion,
				RequestID:       key,
				IdempotencyKey:  key,
				TableID:         snapshot.TableID,
				SchemaRevision:  snapshot.SchemaRevision,
				Operations:      operations,
				Actor: mutation.Actor{
					Type: "system", ID: "formula-fanout",
				},
			},
		); err != nil {
			return service.failUnlessContextInterrupted(
				ctx, record, snapshot.TableID, err,
			)
		}
		snapshot.Progress.Completed = end
		snapshot.Cursor.LastRecordID = batch[len(batch)-1]
		cursor.LastRecordID = snapshot.Cursor.LastRecordID
		cursorRaw, _ := json.Marshal(cursor)
		progressRaw, _ := json.Marshal(snapshot.Progress)
		record.Set("cursor_json", types.JSONRaw(cursorRaw))
		record.Set("progress_json", types.JSONRaw(progressRaw))
		if err := service.app.Save(record); err != nil {
			return service.failUnlessContextInterrupted(
				ctx, record, snapshot.TableID, err,
			)
		}
		if progress, getErr := service.Get(ctx, record.Id); getErr == nil {
			service.publish(ctx, progress)
		}
	}
	record.Set("state", "complete")
	record.Set("error_json", nil)
	completed, err := service.persistTerminal(ctx, record)
	if err != nil {
		return jobError(
			"job.storage_failed",
			"completed fan-out job state could not be saved",
			true,
		)
	}
	service.publish(ctx, completed)
	return nil
}

func decodedObject(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var decoded map[string]any
	if json.Unmarshal(raw, &decoded) != nil {
		return map[string]any{}
	}
	return decoded
}

func relationIDs(value any) []string {
	switch typed := value.(type) {
	case nil:
		return []string{}
	case string:
		if typed == "" {
			return []string{}
		}
		return []string{typed}
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				result = append(result, text)
			}
		}
		return result
	default:
		return []string{}
	}
}
