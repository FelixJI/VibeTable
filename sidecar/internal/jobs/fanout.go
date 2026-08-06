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
	TableID           string   `json:"tableId"`
	LastRecordID      string   `json:"lastRecordId"`
	RelationFieldID   string   `json:"relationFieldId"`
	ChangedTableID    string   `json:"changedTableId"`
	TargetRecordIDs   []string `json:"targetRecordIds"`
	FormulaFieldIDs   []string `json:"formulaFieldIds"`
	DiscoveryComplete bool     `json:"discoveryComplete"`
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
	total, err := service.recordCount(definition)
	if err != nil {
		return "", err
	}
	if total == 0 {
		return "", nil
	}
	formulaFields := make([]string, 0, len(dependencies))
	seenFormulaFields := map[string]struct{}{}
	for _, dependency := range dependencies {
		fieldID := dependency.GetString("formula_field_id")
		if fieldID == "" {
			continue
		}
		if _, exists := seenFormulaFields[fieldID]; exists {
			continue
		}
		seenFormulaFields[fieldID] = struct{}{}
		formulaFields = append(formulaFields, fieldID)
	}
	sort.Strings(formulaFields)
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
		ChangedTableID:  event.TableID,
		TargetRecordIDs: append([]string(nil), event.RecordIDs...),
		FormulaFieldIDs: formulaFields,
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

type fanoutTraversalNode struct {
	definition schema.TableDefinition
	record     *core.Record
}

type fanoutJunctionEdge struct {
	junctionID  string
	targetID    string
	targetTable string
}

func (service *Service) fanoutSourcePage(
	definition schema.TableDefinition,
	lastRecordID string,
) ([]*core.Record, error) {
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": definition.TableID},
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
	definition schema.TableDefinition,
	cursor fanoutCursor,
	rows []*core.Record,
) ([]string, error) {
	targets := make(map[string]struct{}, len(cursor.TargetRecordIDs))
	for _, recordID := range cursor.TargetRecordIDs {
		targets[recordID] = struct{}{}
	}
	formulaFields := make(map[string]struct{}, len(cursor.FormulaFieldIDs))
	for _, fieldID := range cursor.FormulaFieldIDs {
		formulaFields[fieldID] = struct{}{}
	}
	paths := make([][]schema.LookupPathStep, 0, len(formulaFields)+1)
	for _, field := range definition.Fields {
		if _, selected := formulaFields[field.FieldID]; !selected {
			continue
		}
		if field.Kind == schema.FieldKindLookup && field.Lookup != nil {
			paths = append(paths, field.Lookup.EffectivePath())
		}
	}
	if len(paths) == 0 {
		paths = append(paths, []schema.LookupPathStep{{
			RelationFieldID: cursor.RelationFieldID,
		}})
	}
	definitions := map[string]schema.TableDefinition{definition.TableID: definition}
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
	path []schema.LookupPathStep,
	changedTableID string,
	targets map[string]struct{},
	definitions map[string]schema.TableDefinition,
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
			if step.M2ACollection != "" {
				targetTableID = step.M2ACollection
			} else if relation.EffectiveMode() == "m2a" && last {
				targetTableID = changedTableID
			}
			if relation.EffectiveMode() == "direct" {
				ids := relationIDs(node.record.GetRaw(relationField.PhysicalName))
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
				continue
			}
			edges, err := service.fanoutJunctionEdges(
				ctx, cancelRequested, node, relationField, targetTableID, definitions,
				fanoutTraversalBudget-visited,
			)
			if err != nil {
				return false, err
			}
			if err := consumeBudget(len(edges)); err != nil {
				return false, err
			}
			if relation.JunctionTableID != nil && changedTableID == *relation.JunctionTableID {
				for _, edge := range edges {
					if _, matches := targets[edge.junctionID]; matches {
						return true, nil
					}
				}
			}
			if last {
				for _, edge := range edges {
					if edge.targetTable == changedTableID {
						if _, matches := targets[edge.targetID]; matches {
							return true, nil
						}
					}
				}
				continue
			}
			ids := make([]string, 0, len(edges))
			for _, edge := range edges {
				ids = append(ids, edge.targetID)
			}
			loaded, err := service.loadFanoutNodes(
				ctx, cancelRequested, targetTableID, ids, definitions,
			)
			if err != nil {
				return false, err
			}
			next = append(next, loaded...)
		}
		nodes = next
		if len(nodes) == 0 && !last {
			return false, nil
		}
	}
	return false, nil
}

func relationFieldForFanout(
	definition schema.TableDefinition,
	fieldID string,
) (schema.FieldDefinition, bool) {
	for _, field := range definition.Fields {
		if field.FieldID == fieldID && field.Relation != nil {
			return field, true
		}
	}
	return schema.FieldDefinition{}, false
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
	definitions map[string]schema.TableDefinition,
) ([]fanoutTraversalNode, error) {
	definition, ok := definitions[tableID]
	if !ok {
		var err error
		definition, err = schemaapi.New(service.app).Describe(ctx, tableID)
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

func (service *Service) fanoutJunctionEdges(
	ctx context.Context,
	cancelRequested fanoutCancelCheck,
	node fanoutTraversalNode,
	relationField schema.FieldDefinition,
	targetTableID string,
	definitions map[string]schema.TableDefinition,
	remainingBudget int,
) ([]fanoutJunctionEdge, error) {
	relation := relationField.Relation
	if relation == nil || relation.JunctionTableID == nil {
		return nil, jobError("job.formula_dependency_invalid", "junction metadata is unavailable", false)
	}
	junction, ok := definitions[*relation.JunctionTableID]
	if !ok {
		var err error
		junction, err = schemaapi.New(service.app).Describe(ctx, *relation.JunctionTableID)
		if err != nil {
			return nil, err
		}
		definitions[junction.TableID] = junction
	}
	sourceField, sourceOK := relationFieldForFanout(junction, relation.JunctionSourceFieldID)
	targetField, targetOK := relationFieldForFanout(junction, relation.JunctionTargetFieldID)
	if !sourceOK || !targetOK {
		return nil, jobError("job.formula_dependency_invalid", "junction fields are unavailable", false)
	}
	var discriminator schema.FieldDefinition
	if relation.EffectiveMode() == "m2a" {
		for _, field := range junction.Fields {
			if field.FieldID == relation.JunctionDiscriminatorFieldID {
				discriminator = field
				break
			}
		}
		if discriminator.FieldID == "" {
			return nil, jobError("job.formula_dependency_invalid", "m2a discriminator is unavailable", false)
		}
	}
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": junction.TableID},
	)
	if err != nil {
		return nil, jobError("job.storage_failed", "junction storage is unavailable", true)
	}
	collection, err := service.app.FindCollectionByNameOrId(meta.GetString("collection_id"))
	if err != nil {
		return nil, jobError("job.storage_failed", "junction storage is unavailable", true)
	}
	filter := sourceField.PhysicalName + "={:source}"
	params := dbx.Params{"source": node.record.Id}
	expressions := []dbx.Expression{dbx.HashExp{sourceField.PhysicalName: node.record.Id}}
	if relation.EffectiveMode() == "m2a" && targetTableID != "" {
		filter += " && " + discriminator.PhysicalName + "={:targetTable}"
		params["targetTable"] = targetTableID
		expressions = append(
			expressions, dbx.HashExp{discriminator.PhysicalName: targetTableID},
		)
	}
	total, err := service.app.CountRecords(collection, expressions...)
	if err != nil {
		return nil, jobError("job.storage_failed", "junction edges could not be read", true)
	}
	if total > int64(remainingBudget) {
		return nil, jobError(
			"job.fanout_too_expensive",
			"one source record exceeds the formula fan-out traversal budget", false,
		)
	}
	result := make([]fanoutJunctionEdge, 0, int(total))
	for offset := 0; offset < int(total); offset += fanoutJunctionBatch {
		if err := checkFanoutInterrupted(ctx, cancelRequested); err != nil {
			return nil, err
		}
		rows, err := service.app.FindRecordsByFilter(
			collection, filter, "+id",
			min(fanoutJunctionBatch, int(total)-offset), offset, params,
		)
		if err != nil {
			return nil, jobError("job.storage_failed", "junction edges could not be read", true)
		}
		for _, row := range rows {
			if err := checkFanoutInterrupted(ctx, cancelRequested); err != nil {
				return nil, err
			}
			edgeTable := targetTableID
			if relation.EffectiveMode() == "m2a" {
				edgeTable = row.GetString(discriminator.PhysicalName)
			}
			ids := relationIDs(row.GetRaw(targetField.PhysicalName))
			if len(ids) > remainingBudget-len(result) {
				return nil, jobError(
					"job.fanout_too_expensive",
					"one source record exceeds the formula fan-out traversal budget", false,
				)
			}
			for _, targetID := range ids {
				result = append(result, fanoutJunctionEdge{
					junctionID: row.Id, targetID: targetID, targetTable: edgeTable,
				})
			}
		}
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
