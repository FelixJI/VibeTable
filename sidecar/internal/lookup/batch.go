package lookup

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// CalculateCellsBatch projects a page with one request-local schema and record
// cache. Selection uses stable field IDs; returned cells use physical names.
func (calculator *Calculator) CalculateCellsBatch(
	ctx context.Context, app core.App, definition schemaexecution.Table,
	records []*core.Record, selected map[string]bool,
) (map[string]map[string]CellValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	groups := [][]v2.FieldDefinition{}
	paths := map[string]int{}
	for _, field := range definition.Snapshot.Fields {
		if field.LogicalType != v2.LogicalLookup || field.Lookup == nil ||
			(selected != nil && !selected[field.Identity.FieldID]) {
			continue
		}
		raw, err := json.Marshal(field.Lookup.Path)
		if err != nil {
			return nil, err
		}
		key := string(raw)
		index, found := paths[key]
		if !found {
			index = len(groups)
			paths[key] = index
			groups = append(groups, nil)
		}
		groups[index] = append(groups[index], field)
	}
	return calculateLookupGroups(ctx, app, definition, records, groups, 0, cellProvenancePageSize)
}

// Both grid cells and source details execute the same bounded projection plan.
func calculateLookupGroups(
	ctx context.Context, app core.App, definition schemaexecution.Table,
	records []*core.Record, groups [][]v2.FieldDefinition, offset, limit int,
) (map[string]map[string]CellValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make(map[string]map[string]CellValue, len(records))
	for _, record := range records {
		result[record.Id] = map[string]CellValue{}
	}
	definitions := map[string]schemaexecution.Table{definition.Snapshot.TableID: definition}
	cache := map[string]map[string]*core.Record{definition.PhysicalName: {}}
	for _, record := range records {
		cache[definition.PhysicalName][record.Id] = record
	}
	readBudget := materializationBudget{remainingBytes: lookupMaterializationBytes}
	valueBudget := materializationBudget{remainingBytes: lookupMaterializationBytes}
	var cursors []*lookupBatchCursor
	for _, fields := range groups {
		if len(fields[0].Lookup.Path) == 0 {
			return nil, lookupError("mutation.lookup.schema_invalid", "lookup path metadata is unavailable")
		}
		for _, record := range records {
			cursors = append(cursors, &lookupBatchCursor{
				recordID: record.Id, fields: fields,
				source:    traversalNode{definition: definition, record: record},
				collector: lookupPageCollector{offset: offset, limit: limit},
				values:    make([][]lookupPathValue, len(fields)),
			})
		}
	}
	for len(cursors) > 0 {
		pending := map[string]map[string]bool{}
		targets := map[string]schemaexecution.Table{}
		active := cursors[:0]
		for _, cursor := range cursors {
			target, ids, err := cursor.advance(ctx, app, definitions, cache, &valueBudget)
			if err != nil && !isMissingLookupSource(err) {
				return nil, err
			}
			if err != nil || cursor.complete {
				for index, field := range cursor.fields {
					cell := missingLookupSourceCell()
					if err == nil {
						values, provenance := resolvedValues(cursor.values[index])
						cell = CellValue{
							State: "ok", Value: canonicalLookupValue(values), Provenance: provenance,
							ProvenanceTotal: cursor.collector.total, ProvenanceTotalKnown: !cursor.stopped,
							ProvenanceOffset: offset, ProvenanceLimit: limit,
							ProvenanceHasMore: cursor.stopped || cursor.collector.total > offset+len(provenance),
						}
					}
					result[cursor.recordID][field.Identity.PhysicalName] = cell
				}
				continue
			}
			active = append(active, cursor)
			if pending[target.PhysicalName] == nil {
				pending[target.PhysicalName] = map[string]bool{}
				targets[target.PhysicalName] = target
			}
			for _, id := range ids {
				pending[target.PhysicalName][id] = true
			}
		}
		cursors = active
		for name, ids := range pending {
			if cache[name] == nil {
				cache[name] = map[string]*core.Record{}
			}
			batch := make([]string, 0, lookupTraversalBatch)
			flush := func() error {
				loaded, err := queryLookupRecords(ctx, app, targets[name], batch)
				if err != nil {
					return err
				}
				for _, id := range batch {
					record := loaded[id]
					if record != nil {
						if err := readBudget.consume(record.PublicExport()); err != nil {
							return err
						}
					}
					// A nil entry records a missing source, so only cursors that
					// consume that source become invalid.
					cache[name][id] = record
				}
				batch = batch[:0]
				return nil
			}
			for id := range ids {
				batch = append(batch, id)
				if len(batch) == lookupTraversalBatch {
					if err := flush(); err != nil {
						return nil, err
					}
				}
			}
			if len(batch) > 0 {
				if err := flush(); err != nil {
					return nil, err
				}
			}
		}
	}
	return result, nil
}

type lookupBatchCursor struct {
	recordID  string
	fields    []v2.FieldDefinition
	source    traversalNode
	collector lookupPageCollector
	values    [][]lookupPathValue
	stopped   bool
	complete  bool
}

// advance discovers a bounded frontier in the cached path prefix. Unknown
// intermediate subtrees are deferred together; terminal rows are requested only
// after that prefix determines their exact page positions. Replaying the cached
// prefix preserves DFS order without retaining a fully expanded relation graph.
func (cursor *lookupBatchCursor) advance(
	ctx context.Context, app core.App, definitions map[string]schemaexecution.Table,
	cache map[string]map[string]*core.Record, budget *materializationBudget,
) (schemaexecution.Table, []string, error) {
	path := cursor.fields[0].Lookup.Path
	cursor.collector.total = 0
	cursor.stopped = false
	type leafReference struct {
		target schemaexecution.Table
		id     string
	}
	var leaves []leafReference
	var pendingTarget schemaexecution.Table
	var pending []string
	seen := map[string]bool{}
	frontierComplete := errors.New("lookup frontier is complete")
	var scan func(traversalNode, int) error
	scan = func(source traversalNode, pathIndex int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		relation, found := fieldByID(source.definition, path[pathIndex].RelationFieldID)
		if !found || relation.Relation == nil {
			return lookupError("mutation.lookup.schema_invalid", "lookup path relation metadata is unavailable")
		}
		target, err := describeLookupTable(ctx, app, relation.Relation.TargetTableID, definitions)
		if err != nil {
			return err
		}
		ids := relationIDs(source.record.GetRaw(relation.Identity.PhysicalName))
		if pathIndex == len(path)-1 {
			start, end, stop := cursor.collector.rangeFor(len(ids))
			for _, id := range ids[start:end] {
				leaves = append(leaves, leafReference{target: target, id: id})
			}
			if stop && len(path) > 1 {
				return errLookupPageComplete
			}
			return nil
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			record, loaded := cache[target.PhysicalName][id]
			if !loaded {
				if len(pending) == 0 {
					pendingTarget = target
				}
				if target.PhysicalName == pendingTarget.PhysicalName && !seen[id] {
					pending = append(pending, id)
					seen[id] = true
					if len(pending) == lookupTraversalBatch {
						return frontierComplete
					}
				}
				continue
			}
			if record == nil {
				// An unresolved earlier branch may fill the page before this
				// missing record is visited. Reconsider it after that frontier.
				if len(pending) > 0 {
					continue
				}
				return lookupError("mutation.lookup.target_not_found", "lookup relation references a missing record")
			}
			if err := scan(traversalNode{definition: target, record: record}, pathIndex+1); err != nil {
				return err
			}
		}
		return nil
	}
	err := scan(cursor.source, 0)
	if errors.Is(err, errLookupPageComplete) {
		cursor.stopped = true
	} else if err != nil && !errors.Is(err, frontierComplete) {
		return schemaexecution.Table{}, nil, err
	}
	if len(pending) > 0 {
		return pendingTarget, pending, nil
	}
	for _, leaf := range leaves {
		if _, loaded := cache[leaf.target.PhysicalName][leaf.id]; !loaded {
			pendingTarget = leaf.target
			pending = append(pending, leaf.id)
		}
	}
	if len(pending) > 0 {
		return pendingTarget, pending, nil
	}
	for _, leaf := range leaves {
		if err := ctx.Err(); err != nil {
			return schemaexecution.Table{}, nil, err
		}
		record := cache[leaf.target.PhysicalName][leaf.id]
		if record == nil {
			return schemaexecution.Table{}, nil, lookupError("mutation.lookup.target_not_found", "lookup relation references a missing record")
		}
		node := traversalNode{definition: leaf.target, record: record}
		for index, field := range cursor.fields {
			projected, err := projectLookupNodes([]traversalNode{node}, field)
			if err != nil {
				return schemaexecution.Table{}, nil, err
			}
			if err := budget.consume(projected[0].value); err != nil {
				return schemaexecution.Table{}, nil, err
			}
			cursor.values[index] = append(cursor.values[index], projected[0])
		}
	}
	cursor.complete = true
	return schemaexecution.Table{}, nil, nil
}
