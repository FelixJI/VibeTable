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
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

const (
	formulaFanoutType     = "formula_fanout"
	fanoutDiscoveryBatch  = 500
	fanoutJunctionBatch   = 500
	fanoutTraversalBudget = 100_000
	maxRetainedDataEvents = 10_000
)

var errFanoutCancellationRequested = errors.New("formula fan-out cancellation requested")

type fanoutCancelCheck func() bool

func checkFanoutInterrupted(ctx context.Context, cancelRequested fanoutCancelCheck) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cancelRequested != nil && cancelRequested() {
		return errFanoutCancellationRequested
	}
	return nil
}

type DataPublisher interface {
	Publish(context.Context, mutation.DataChangedEvent) error
}

type fanoutCursor struct {
	TableID           string                `json:"tableId"`
	LastRecordID      string                `json:"lastRecordId"`
	RelationFieldID   string                `json:"relationFieldId"`
	ChangedTableID    string                `json:"changedTableId"`
	TargetRecordIDs   []string              `json:"targetRecordIds"`
	FormulaFieldIDs   []string              `json:"formulaFieldIds"`
	Paths             [][]v2.LookupPathStep `json:"paths"`
	DiscoveryComplete bool                  `json:"discoveryComplete"`
}

// Publish durably derives cross-record recalculation jobs before attempting
// live delivery. The retained data outbox is the recovery source if the
// process stops before this method or while fan-out enqueue is incomplete.
func (service *Service) Publish(
	ctx context.Context,
	event mutation.DataChangedEvent,
) error {
	jobIDs, err := service.EnqueueInvalidations(ctx, service.app, event)
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

// EnqueueInvalidations is safe to call with the mutation transaction app.
// It creates idempotent durable jobs only; Publish starts them after commit.
func (service *Service) EnqueueInvalidations(
	ctx context.Context,
	app core.App,
	event mutation.DataChangedEvent,
) ([]string, error) {
	if app == nil {
		return nil, jobError("job.storage_failed", "job storage is unavailable", true)
	}
	return service.enqueueFormulaFanout(ctx, app, event)
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
		"vibetable_computation_dependencies",
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
		if _, err := service.enqueueFormulaFanout(ctx, service.app, event); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) enqueueFormulaFanout(
	ctx context.Context,
	app core.App,
	event mutation.DataChangedEvent,
) ([]string, error) {
	if event.ChangeSetID == nil || *event.ChangeSetID == "" ||
		len(event.RecordIDs) == 0 {
		return []string{}, nil
	}
	dependencies, err := app.FindRecordsByFilter(
		"vibetable_computation_dependencies",
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
		app,
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
		targetFieldID := dependency.GetString("target_field_id")
		if _, relevant := changed[targetFieldID]; !relevant && targetFieldID != "__path__" {
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
			app,
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
	app core.App,
	tableID string,
	changeSetID string,
) (map[string]struct{}, error) {
	definition, err := schemaexecution.Describe(context.Background(), app, tableID)
	if err != nil {
		return nil, err
	}
	events, err := app.FindRecordsByFilter(
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
		for _, field := range definition.Snapshot.Fields {
			if !reflect.DeepEqual(
				before[field.Identity.PhysicalName],
				after[field.Identity.PhysicalName],
			) {
				changed[field.Identity.FieldID] = struct{}{}
			}
		}
	}
	return changed, nil
}

func (service *Service) createFanoutJob(
	ctx context.Context,
	app core.App,
	event mutation.DataChangedEvent,
	sourceTableID string,
	relationFieldID string,
	dependencies []*core.Record,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	existing, err := app.FindFirstRecordByFilter(
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
	definition, err := schemaexecution.Describe(ctx, app, sourceTableID)
	if err != nil {
		return "", err
	}
	var relationField *v2.FieldDefinition
	for index := range definition.Snapshot.Fields {
		if definition.Snapshot.Fields[index].Identity.FieldID == relationFieldID &&
			definition.Snapshot.Fields[index].Relation != nil {
			relationField = &definition.Snapshot.Fields[index]
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
	total, err := recordCountWithApp(app, definition)
	if err != nil {
		return "", err
	}
	if total == 0 {
		return "", nil
	}
	formulaFields := make([]string, 0, len(dependencies))
	seenFormulaFields := map[string]struct{}{}
	paths := make([][]v2.LookupPathStep, 0, len(dependencies))
	seenPaths := map[string]struct{}{}
	for _, dependency := range dependencies {
		fieldID := dependency.GetString("computed_field_id")
		if fieldID == "" {
			continue
		}
		if _, exists := seenFormulaFields[fieldID]; exists {
			continue
		}
		seenFormulaFields[fieldID] = struct{}{}
		formulaFields = append(formulaFields, fieldID)
		pathRaw, marshalErr := json.Marshal(dependency.GetRaw("path_json"))
		var path []v2.LookupPathStep
		if marshalErr != nil || json.Unmarshal(pathRaw, &path) != nil || len(path) == 0 {
			return "", jobError(
				"job.formula_dependency_invalid",
				"computed dependency path is unavailable",
				false,
			)
		}
		canonical, _ := json.Marshal(path)
		if _, exists := seenPaths[string(canonical)]; !exists {
			seenPaths[string(canonical)] = struct{}{}
			paths = append(paths, path)
		}
	}
	sort.Strings(formulaFields)
	revision, _ := v2.ParseSchemaRevision(definition.Snapshot.SchemaRevision)
	collection, err := app.FindCollectionByNameOrId(
		"vibetable_jobs",
	)
	if err != nil {
		return "", jobError(
			"job.storage_failed", "job storage is unavailable", true,
		)
	}
	cursor := fanoutCursor{
		TableID: sourceTableID, RelationFieldID: relationFieldID,
		ChangedTableID:  event.TableID,
		TargetRecordIDs: append([]string(nil), event.RecordIDs...),
		FormulaFieldIDs: formulaFields, Paths: paths,
	}
	cursorRaw, _ := json.Marshal(cursor)
	progressRaw, _ := json.Marshal(Progress{
		Completed: 0, Total: total,
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
	if err := app.Save(record); err != nil {
		// A concurrent duplicate resolves to the already-created durable job.
		existing, findErr := app.FindFirstRecordByFilter(
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
	return record.Id, nil
}

type fanoutTraversalNode struct {
	definition schemaexecution.Table
	record     *core.Record
}

func (service *Service) fanoutSourcePage(
	definition schemaexecution.Table,
	lastRecordID string,
) ([]*core.Record, error) {
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}",
		dbx.Params{"table": definition.Snapshot.TableID},
	)
	if err != nil {
		return nil, jobError("job.storage_failed", "source table storage is unavailable", true)
	}
	collection, err := service.app.FindCollectionByNameOrId(meta.GetString("collection_id"))
	if err != nil {
		return nil, jobError("job.storage_failed", "source table storage is unavailable", true)
	}
	filter := ""
	params := dbx.Params{}
	if lastRecordID != "" {
		filter = "id>{:cursor}"
		params["cursor"] = lastRecordID
	}
	rows, err := service.app.FindRecordsByFilter(
		collection, filter, "+id", fanoutDiscoveryBatch, 0, params,
	)
	if err != nil {
		return nil, jobError("job.storage_failed", "source records could not be scanned", true)
	}
	return rows, nil
}

func (service *Service) matchingFanoutBatch(
	ctx context.Context,
	cancelRequested fanoutCancelCheck,
	definition schemaexecution.Table,
	cursor fanoutCursor,
	rows []*core.Record,
) ([]string, error) {
	targets := make(map[string]struct{}, len(cursor.TargetRecordIDs))
	for _, recordID := range cursor.TargetRecordIDs {
		targets[recordID] = struct{}{}
	}
	paths := cursor.Paths
	if len(paths) == 0 {
		paths = append(paths, []v2.LookupPathStep{{
			RelationFieldID: cursor.RelationFieldID,
		}})
	}
	definitions := map[string]schemaexecution.Table{
		definition.Snapshot.TableID: definition,
	}
	result := make([]string, 0)
	for _, row := range rows {
		if err := checkFanoutInterrupted(ctx, cancelRequested); err != nil {
			return nil, err
		}
		for _, path := range paths {
			matches, err := service.fanoutPathMatches(
				ctx, cancelRequested,
				fanoutTraversalNode{definition: definition, record: row},
				path, cursor.ChangedTableID, targets, definitions,
			)
			if err != nil {
				return nil, err
			}
			if matches {
				result = append(result, row.Id)
				break
			}
		}
	}
	return result, nil
}

func (service *Service) fanoutPathMatches(
	ctx context.Context,
	cancelRequested fanoutCancelCheck,
	root fanoutTraversalNode,
	path []v2.LookupPathStep,
	changedTableID string,
	targets map[string]struct{},
	definitions map[string]schemaexecution.Table,
) (bool, error) {
	if err := checkFanoutInterrupted(ctx, cancelRequested); err != nil {
		return false, err
	}
	if len(path) == 0 {
		return false, jobError(
			"job.formula_dependency_invalid", "formula dependency path is unavailable", false,
		)
	}
	nodes := []fanoutTraversalNode{root}
	visited := 1
	consumeBudget := func(count int) error {
		if count < 0 || count > fanoutTraversalBudget-visited {
			return jobError(
				"job.fanout_too_expensive",
				"one source record exceeds the formula fan-out traversal budget", false,
			)
		}
		visited += count
		return nil
	}
	for index, step := range path {
		if err := checkFanoutInterrupted(ctx, cancelRequested); err != nil {
			return false, err
		}
		last := index == len(path)-1
		next := make([]fanoutTraversalNode, 0)
		for _, node := range nodes {
			if err := checkFanoutInterrupted(ctx, cancelRequested); err != nil {
				return false, err
			}
			relationField, ok := relationFieldForFanout(node.definition, step.RelationFieldID)
			if !ok || relationField.Relation == nil {
				return false, jobError(
					"job.formula_dependency_invalid",
					"formula dependency relation is unavailable", false,
				)
			}
			relation := relationField.Relation
			targetTableID := relation.TargetTableID
			ids := relationIDs(node.record.GetRaw(relationField.Identity.PhysicalName))
			if err := consumeBudget(len(ids)); err != nil {
				return false, err
			}
			if last && targetTableID == changedTableID && intersectsFanoutTargets(ids, targets) {
				return true, nil
			}
			if !last {
				loaded, err := service.loadFanoutNodes(
					ctx, cancelRequested, targetTableID, ids, definitions,
				)
				if err != nil {
					return false, err
				}
				next = append(next, loaded...)
			}
		}
		nodes = next
		if len(nodes) == 0 && !last {
			return false, nil
		}
	}
	return false, nil
}

func relationFieldForFanout(
	definition schemaexecution.Table,
	fieldID string,
) (v2.FieldDefinition, bool) {
	for _, field := range definition.Snapshot.Fields {
		if field.Identity.FieldID == fieldID && field.Relation != nil {
			return field, true
		}
	}
	return v2.FieldDefinition{}, false
}

func intersectsFanoutTargets(ids []string, targets map[string]struct{}) bool {
	for _, id := range ids {
		if _, exists := targets[id]; exists {
			return true
		}
	}
	return false
}

func (service *Service) loadFanoutNodes(
	ctx context.Context,
	cancelRequested fanoutCancelCheck,
	tableID string,
	ids []string,
	definitions map[string]schemaexecution.Table,
) ([]fanoutTraversalNode, error) {
	definition, ok := definitions[tableID]
	if !ok {
		var err error
		definition, err = schemaexecution.Describe(ctx, service.app, tableID)
		if err != nil {
			return nil, err
		}
		definitions[tableID] = definition
	}
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": tableID},
	)
	if err != nil {
		return nil, jobError("job.storage_failed", "fan-out target storage is unavailable", true)
	}
	collection, err := service.app.FindCollectionByNameOrId(meta.GetString("collection_id"))
	if err != nil {
		return nil, jobError("job.storage_failed", "fan-out target storage is unavailable", true)
	}
	result := make([]fanoutTraversalNode, 0, len(ids))
	for _, id := range ids {
		if err := checkFanoutInterrupted(ctx, cancelRequested); err != nil {
			return nil, err
		}
		record, err := service.app.FindRecordById(collection, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, jobError("job.storage_failed", "fan-out target record could not be read", true)
		}
		result = append(result, fanoutTraversalNode{definition: definition, record: record})
	}
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
		cursor.TableID != snapshot.TableID {
		return service.fail(
			record, snapshot.TableID,
			jobError(
				"job.storage_corrupt",
				"formula fan-out cursor is invalid",
				false,
			),
		)
	}
	cancelRequested := func() bool { return service.cancelRequested(record.Id) }
	for !cursor.DiscoveryComplete {
		if err := checkFanoutInterrupted(ctx, cancelRequested); err != nil {
			if errors.Is(err, errFanoutCancellationRequested) {
				return service.finishCancellation(ctx, record)
			}
			return err
		}
		definition, err := schemaexecution.Describe(ctx, service.app, snapshot.TableID)
		if err != nil {
			return service.failUnlessContextInterrupted(
				ctx, record, snapshot.TableID, err,
			)
		}
		if definition.Snapshot.SchemaRevision != snapshot.SchemaRevision {
			return service.fail(
				record, snapshot.TableID,
				jobError(
					"job.schema_revision_conflict",
					"schema changed during formula fan-out",
					false,
				),
			)
		}
		rows, err := service.fanoutSourcePage(definition, cursor.LastRecordID)
		if err != nil {
			return service.failUnlessContextInterrupted(
				ctx, record, snapshot.TableID, err,
			)
		}
		if len(rows) == 0 {
			cursor.DiscoveryComplete = true
			snapshot.Progress.Total = snapshot.Progress.Completed
			cursorRaw, _ := json.Marshal(cursor)
			progressRaw, _ := json.Marshal(snapshot.Progress)
			current, cancelled, saveErr := service.saveRunningState(
				record.Id,
				func(current *core.Record) error {
					current.Set("cursor_json", types.JSONRaw(cursorRaw))
					current.Set("progress_json", types.JSONRaw(progressRaw))
					return nil
				},
			)
			if saveErr != nil {
				return service.failUnlessContextInterrupted(
					ctx, record, snapshot.TableID, saveErr,
				)
			}
			if cancelled {
				return nil
			}
			record = current
			break
		}
		matches, err := service.matchingFanoutBatch(
			ctx, cancelRequested, definition, cursor, rows,
		)
		if err != nil {
			if errors.Is(err, errFanoutCancellationRequested) {
				return service.finishCancellation(ctx, record)
			}
			return service.failUnlessContextInterrupted(
				ctx, record, snapshot.TableID, err,
			)
		}
		for start := 0; start < len(matches); start += backfillBatchSize {
			if err := checkFanoutInterrupted(ctx, cancelRequested); err != nil {
				if errors.Is(err, errFanoutCancellationRequested) {
					return service.finishCancellation(ctx, record)
				}
				return err
			}
			end := min(start+backfillBatchSize, len(matches))
			batch := matches[start:end]
			operations := make([]mutation.Operation, 0, len(batch))
			for _, itemID := range batch {
				itemID := itemID
				operations = append(operations, mutation.Operation{
					Kind: mutation.OperationUpdate, RecordID: &itemID,
					Values: map[string]any{},
				})
			}
			key := fmt.Sprintf(
				"fanout_%s_%s_%s_%d", record.Id,
				rows[0].Id, rows[len(rows)-1].Id, start,
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
			if cancelRequested() {
				return service.finishCancellation(ctx, record)
			}
		}
		cursor.LastRecordID = rows[len(rows)-1].Id
		snapshot.Cursor.LastRecordID = cursor.LastRecordID
		snapshot.Progress.Completed += len(rows)
		if snapshot.Progress.Completed > snapshot.Progress.Total {
			snapshot.Progress.Total = snapshot.Progress.Completed
		}
		cursorRaw, _ := json.Marshal(cursor)
		progressRaw, _ := json.Marshal(snapshot.Progress)
		current, cancelled, err := service.saveRunningState(
			record.Id,
			func(current *core.Record) error {
				current.Set("cursor_json", types.JSONRaw(cursorRaw))
				current.Set("progress_json", types.JSONRaw(progressRaw))
				return nil
			},
		)
		if err != nil {
			return service.failUnlessContextInterrupted(
				ctx, record, snapshot.TableID, err,
			)
		}
		if cancelled {
			return nil
		}
		record = current
		if progress, getErr := service.Get(ctx, record.Id); getErr == nil {
			service.publish(ctx, progress)
		}
	}
	return service.finishCompletion(ctx, record.Id, "")
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
