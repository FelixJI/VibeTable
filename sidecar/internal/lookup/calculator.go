package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

const (
	cellProvenancePageSize     = 100
	lookupTraversalBatch       = 256
	lookupMaterializationBytes = 32 << 20
)

type Calculator struct{}

func NewCalculator() *Calculator {
	return &Calculator{}
}

type ValueProvenance struct {
	Collection      string `json:"collection"`
	CollectionLabel string `json:"collectionLabel"`
	ItemID          string `json:"itemId"`
	RecordLabel     string `json:"recordLabel"`
	FieldID         string `json:"fieldId"`
	FieldLabel      string `json:"fieldLabel"`
	Value           any    `json:"value"`
}

type CellValue struct {
	State                string            `json:"state"`
	Value                any               `json:"value"`
	Provenance           []ValueProvenance `json:"provenance"`
	ProvenanceTotal      int               `json:"provenanceTotal"`
	ProvenanceTotalKnown bool              `json:"provenanceTotalKnown"`
	ProvenanceOffset     int               `json:"provenanceOffset"`
	ProvenanceLimit      int               `json:"provenanceLimit"`
	ProvenanceHasMore    bool              `json:"provenanceHasMore"`
	Diagnostic           *Diagnostic       `json:"diagnostic,omitempty"`
}

type Diagnostic struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	PathIndex *int   `json:"pathIndex,omitempty"`
}

func (calculator *Calculator) Calculate(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	record *core.Record,
) (map[string]any, error) {
	cells, err := calculator.calculateCells(ctx, app, definition, record, false)
	if err != nil {
		return nil, err
	}
	result := make(map[string]any, len(cells))
	for physicalName, cell := range cells {
		result[physicalName] = cell.Value
	}
	return result, nil
}

func (calculator *Calculator) CalculateCells(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	record *core.Record,
) (map[string]CellValue, error) {
	return calculator.calculateCells(ctx, app, definition, record, true)
}

func (calculator *Calculator) calculateCells(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	record *core.Record,
	pageValues bool,
) (map[string]CellValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := map[string]CellValue{}
	for _, field := range definition.Fields {
		if field.Kind != schema.FieldKindLookup || field.Lookup == nil {
			continue
		}
		if field.Lookup.Aggregate != "none" {
			cell, err := calculateStreamingAggregate(
				ctx, app, definition, record, field, pageValues,
			)
			if err != nil {
				if isMissingLookupSource(err) {
					result[field.PhysicalName] = missingLookupSourceCell()
					continue
				}
				return nil, err
			}
			result[field.PhysicalName] = cell
			continue
		}
		var resolved []lookupPathValue
		var total int
		var totalKnown bool
		var err error
		if pageValues {
			resolved, total, totalKnown, err = lookupPathValuesPage(
				ctx, app, definition, record, field, 0, cellProvenancePageSize,
			)
		} else {
			resolved, err = lookupPathValues(ctx, app, definition, record, field)
			total = len(resolved)
			totalKnown = true
		}
		if err != nil {
			if isMissingLookupSource(err) {
				result[field.PhysicalName] = missingLookupSourceCell()
				continue
			}
			return nil, err
		}
		values := make([]any, 0, len(resolved))
		provenance := make([]ValueProvenance, 0, len(resolved))
		for _, item := range resolved {
			values = append(values, item.value)
			provenance = append(provenance, ValueProvenance{
				Collection: item.collection, CollectionLabel: item.collectionLabel,
				ItemID: item.itemID, RecordLabel: item.recordLabel,
				FieldID: item.fieldID, FieldLabel: item.fieldLabel, Value: item.value,
			})
		}
		value, err := aggregate("none", field.StorageType, values)
		if err != nil {
			return nil, err
		}
		visibleProvenance := provenance
		if len(visibleProvenance) > cellProvenancePageSize {
			visibleProvenance = visibleProvenance[:cellProvenancePageSize]
		}
		result[field.PhysicalName] = CellValue{
			State: "ok", Value: value, Provenance: visibleProvenance,
			ProvenanceTotal: total, ProvenanceTotalKnown: totalKnown, ProvenanceOffset: 0,
			ProvenanceLimit:   cellProvenancePageSize,
			ProvenanceHasMore: !totalKnown || total > len(visibleProvenance),
		}
	}
	return result, nil
}

func (calculator *Calculator) CalculateFieldPage(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	record *core.Record,
	field schema.FieldDefinition,
	offset int,
	limit int,
) (CellValue, error) {
	if field.Kind != schema.FieldKindLookup || field.Lookup == nil ||
		offset < 0 || limit < 1 || limit > 500 {
		return CellValue{}, lookupError(
			"lookup.request.invalid", "lookup value page request is invalid",
		)
	}
	if field.Lookup.Aggregate != "none" {
		return CellValue{}, lookupError(
			"lookup.request.aggregate_unsupported",
			"lookup value pages are unavailable for legacy aggregate definitions",
		)
	}
	resolved, total, totalKnown, err := lookupPathValuesPage(
		ctx, app, definition, record, field, offset, limit,
	)
	if err != nil {
		if isMissingLookupSource(err) {
			return missingLookupSourceCell(), nil
		}
		return CellValue{}, err
	}
	values, provenance := resolvedValues(resolved)
	value, err := aggregate(field.Lookup.Aggregate, field.StorageType, values)
	if err != nil {
		return CellValue{}, err
	}
	return CellValue{
		State: "ok", Value: value, Provenance: provenance,
		ProvenanceTotal: total, ProvenanceTotalKnown: totalKnown,
		ProvenanceOffset: offset, ProvenanceLimit: limit,
		ProvenanceHasMore: !totalKnown || offset+len(provenance) < total,
	}, nil
}

func isMissingLookupSource(err error) bool {
	var productErr *mutation.ProductError
	return errors.As(err, &productErr) &&
		productErr.Code == "mutation.lookup.target_not_found"
}

func missingLookupSourceCell() CellValue {
	return CellValue{
		State: "invalid", Value: nil, Provenance: []ValueProvenance{},
		ProvenanceTotalKnown: true, ProvenanceLimit: cellProvenancePageSize,
		Diagnostic: &Diagnostic{
			Code:    "lookup.value.source_missing",
			Message: "关联的来源记录已不存在，请重新选择关联记录",
		},
	}
}

func resolvedValues(resolved []lookupPathValue) ([]any, []ValueProvenance) {
	values := make([]any, 0, len(resolved))
	provenance := make([]ValueProvenance, 0, len(resolved))
	for _, item := range resolved {
		values = append(values, item.value)
		provenance = append(provenance, ValueProvenance{
			Collection: item.collection, CollectionLabel: item.collectionLabel,
			ItemID: item.itemID, RecordLabel: item.recordLabel,
			FieldID: item.fieldID, FieldLabel: item.fieldLabel, Value: item.value,
		})
	}
	return values, provenance
}

type streamingAggregateState struct {
	kind       string
	storage    schema.StorageType
	count      int
	nonNull    int
	total      float64
	first      any
	best       any
	hasFirst   bool
	hasBest    bool
	distinct   []any
	seen       map[string]struct{}
	budget     materializationBudget
	provenance []ValueProvenance
	capture    bool
}

func calculateStreamingAggregate(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	record *core.Record,
	field schema.FieldDefinition,
	captureProvenance bool,
) (CellValue, error) {
	state := &streamingAggregateState{
		kind: field.Lookup.Aggregate, storage: field.StorageType,
		seen: map[string]struct{}{},
		budget: materializationBudget{
			remainingBytes: lookupMaterializationBytes,
		},
		capture: captureProvenance,
	}
	total, err := streamLookupPathValues(
		ctx, app, definition, record, field, state.consume,
	)
	if err != nil {
		return CellValue{}, err
	}
	value, err := state.value()
	if err != nil {
		return CellValue{}, err
	}
	return CellValue{
		State: "ok", Value: value, Provenance: state.provenance,
		ProvenanceTotal: total, ProvenanceTotalKnown: true, ProvenanceOffset: 0,
		ProvenanceLimit:   cellProvenancePageSize,
		ProvenanceHasMore: captureProvenance && total > len(state.provenance),
	}, nil
}

func streamLookupPathValues(
	ctx context.Context,
	app core.App,
	sourceDefinition schema.TableDefinition,
	record *core.Record,
	lookupField schema.FieldDefinition,
	sink func([]lookupPathValue) error,
) (int, error) {
	path := lookupField.Lookup.EffectivePath()
	if len(path) == 0 {
		return 0, lookupError(
			"mutation.lookup.schema_invalid", "lookup path metadata is unavailable",
		)
	}
	collector := lookupPageCollector{
		offset: 0,
		limit:  int(^uint(0) >> 1),
		sink:   sink,
	}
	err := walkLookupPage(
		ctx, app, traversalNode{definition: sourceDefinition, record: record},
		lookupField, path, 0, map[string]schema.TableDefinition{}, &collector,
	)
	return collector.total, err
}

func (state *streamingAggregateState) consume(values []lookupPathValue) error {
	for _, item := range values {
		state.count++
		if item.value != nil {
			state.nonNull++
		}
		if state.capture && len(state.provenance) < cellProvenancePageSize {
			state.provenance = append(state.provenance, ValueProvenance{
				Collection: item.collection, CollectionLabel: item.collectionLabel,
				ItemID: item.itemID, RecordLabel: item.recordLabel,
				FieldID: item.fieldID, FieldLabel: item.fieldLabel, Value: item.value,
			})
		}
		switch state.kind {
		case "count", "countNonNull":
			continue
		case "first":
			if !state.hasFirst {
				state.first = item.value
				state.hasFirst = true
			}
		case "distinct":
			encoded, err := json.Marshal(item.value)
			if err != nil {
				return lookupError(
					"mutation.lookup.invalid_value",
					"lookup distinct encountered an invalid value",
				)
			}
			key := string(encoded)
			if _, exists := state.seen[key]; exists {
				continue
			}
			if err := state.budget.consume(item.value); err != nil {
				return err
			}
			state.seen[key] = struct{}{}
			state.distinct = append(state.distinct, item.value)
		case "sum", "avg":
			number, ok := finiteNumber(item.value)
			if !ok {
				return lookupError(
					"mutation.lookup.invalid_value",
					"lookup numeric aggregate encountered a non-numeric value",
				)
			}
			state.total += number
			if math.IsInf(state.total, 0) || math.IsNaN(state.total) {
				return lookupError(
					"mutation.lookup.invalid_value",
					"lookup numeric aggregate exceeded the numeric range",
				)
			}
		case "min", "max":
			if !state.hasBest {
				state.best = item.value
				state.hasBest = true
				continue
			}
			comparison, err := compare(state.storage, item.value, state.best)
			if err != nil {
				return err
			}
			if (state.kind == "min" && comparison < 0) ||
				(state.kind == "max" && comparison > 0) {
				state.best = item.value
			}
		default:
			return lookupError(
				"mutation.lookup.schema_invalid", "lookup aggregate is unsupported",
			)
		}
	}
	return nil
}

func (state *streamingAggregateState) value() (any, error) {
	switch state.kind {
	case "count":
		return state.count, nil
	case "countNonNull":
		return state.nonNull, nil
	case "first":
		return state.first, nil
	case "distinct":
		return state.distinct, nil
	case "sum":
		return state.total, nil
	case "avg":
		if state.count == 0 {
			return nil, nil
		}
		return state.total / float64(state.count), nil
	case "min", "max":
		return state.best, nil
	default:
		return nil, lookupError(
			"mutation.lookup.schema_invalid", "lookup aggregate is unsupported",
		)
	}
}

type traversalNode struct {
	definition schema.TableDefinition
	record     *core.Record
}

type junctionNode struct {
	definition schema.TableDefinition
	record     *core.Record
}

type lookupPathValue struct {
	collection      string
	collectionLabel string
	itemID          string
	recordLabel     string
	fieldID         string
	fieldLabel      string
	value           any
}

func describedLookupValue(
	definition schema.TableDefinition,
	record *core.Record,
	field schema.FieldDefinition,
	value any,
) lookupPathValue {
	collectionLabel := strings.TrimSpace(definition.DisplayName)
	if collectionLabel == "" {
		collectionLabel = "关联表"
	}
	fieldLabel := strings.TrimSpace(field.DisplayName)
	if fieldLabel == "" {
		fieldLabel = "关联字段"
	}
	recordLabel := ""
	if definition.PrimaryDisplayFieldID != "" {
		if displayField, found := fieldByID(definition, definition.PrimaryDisplayFieldID); found {
			recordLabel = strings.TrimSpace(fmt.Sprint(decodeLookupFieldValue(
				displayField, record.GetRaw(displayField.PhysicalName),
			)))
		}
	}
	if recordLabel == "" {
		recordLabel = strings.TrimSpace(fmt.Sprint(value))
	}
	if recordLabel == "" || recordLabel == "<nil>" {
		recordLabel = "未命名记录"
	}
	return lookupPathValue{
		collection: definition.TableID, collectionLabel: collectionLabel,
		itemID: record.Id, recordLabel: recordLabel,
		fieldID: field.FieldID, fieldLabel: fieldLabel, value: value,
	}
}

type materializationBudget struct {
	remainingBytes int
}

func (budget *materializationBudget) consume(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return lookupError(
			"mutation.lookup.invalid_value", "lookup value could not be measured",
		)
	}
	cost := max(len(raw), 1)
	if cost > budget.remainingBytes {
		return lookupError(
			"lookup.value.too_expensive",
			"lookup materialization exceeds the memory budget; use paged source details",
		)
	}
	budget.remainingBytes -= cost
	return nil
}

func lookupPathValuesPage(
	ctx context.Context,
	app core.App,
	sourceDefinition schema.TableDefinition,
	record *core.Record,
	lookupField schema.FieldDefinition,
	offset int,
	limit int,
) ([]lookupPathValue, int, bool, error) {
	path := lookupField.Lookup.EffectivePath()
	if len(path) == 0 {
		return nil, 0, false, lookupError(
			"mutation.lookup.schema_invalid", "lookup path metadata is unavailable",
		)
	}
	if len(path) > 1 {
		collector := lookupPageCollector{offset: offset, limit: limit}
		err := walkLookupPage(
			ctx, app, traversalNode{definition: sourceDefinition, record: record},
			lookupField, path, 0, map[string]schema.TableDefinition{}, &collector,
		)
		if errors.Is(err, errLookupPageComplete) {
			return collector.values, collector.total, false, nil
		}
		return collector.values, collector.total, true, err
	}
	relation, found := fieldByID(sourceDefinition, path[0].RelationFieldID)
	if !found || relation.Relation == nil {
		return nil, 0, false, lookupError(
			"mutation.lookup.schema_invalid",
			"lookup path relation metadata is unavailable",
		)
	}
	if relation.Relation.EffectiveMode() == "direct" {
		ids := relationIDs(record.GetRaw(relation.PhysicalName))
		total := len(ids)
		ids = sliceStrings(ids, offset, limit)
		target, err := describeLookupTable(
			ctx, app, relation.Relation.TargetTableID, map[string]schema.TableDefinition{},
		)
		if err != nil {
			return nil, 0, false, err
		}
		nodes, err := loadLookupRecords(ctx, app, target, ids)
		if err != nil {
			return nil, 0, false, err
		}
		values, err := projectLookupNodes(nodes, lookupField)
		return values, total, true, err
	}
	values, total, err := lookupJunctionValuesPage(
		ctx, app, sourceDefinition, record, lookupField, relation, path[0], offset, limit,
	)
	return values, total, true, err
}

func sliceStrings(values []string, offset int, limit int) []string {
	if offset >= len(values) {
		return []string{}
	}
	end := offset + limit
	if end > len(values) {
		end = len(values)
	}
	return values[offset:end]
}

type lookupPageCollector struct {
	offset int
	limit  int
	total  int
	values []lookupPathValue
	sink   func([]lookupPathValue) error
}

var errLookupPageComplete = errors.New("lookup page is complete")

func (collector *lookupPageCollector) rangeFor(count int) (int, int, bool) {
	startAt := collector.total
	collector.total += count
	pageStart := collector.offset
	pageEnd := collector.offset + collector.limit
	stop := collector.total > pageEnd
	if startAt >= pageEnd || collector.total <= pageStart {
		return 0, 0, stop
	}
	start := max(pageStart-startAt, 0)
	end := min(pageEnd-startAt, count)
	return start, end, stop
}

func (collector *lookupPageCollector) appendProjected(values []lookupPathValue) error {
	if collector.sink != nil {
		return collector.sink(values)
	}
	collector.values = append(collector.values, values...)
	return nil
}

func walkLookupPage(
	ctx context.Context,
	app core.App,
	source traversalNode,
	lookupField schema.FieldDefinition,
	path []schema.LookupPathStep,
	pathIndex int,
	definitions map[string]schema.TableDefinition,
	collector *lookupPageCollector,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	step := path[pathIndex]
	relation, found := fieldByID(source.definition, step.RelationFieldID)
	if !found || relation.Relation == nil {
		return lookupError(
			"mutation.lookup.schema_invalid", "lookup path relation metadata is unavailable",
		)
	}
	terminal := pathIndex == len(path)-1
	if relation.Relation.EffectiveMode() == "direct" {
		ids := relationIDs(source.record.GetRaw(relation.PhysicalName))
		target, err := describeLookupTable(
			ctx, app, relation.Relation.TargetTableID, definitions,
		)
		if err != nil {
			return err
		}
		if terminal {
			start, end, stop := collector.rangeFor(len(ids))
			if start == end {
				if stop {
					return errLookupPageComplete
				}
				return nil
			}
			for batchStart := start; batchStart < end; batchStart += lookupTraversalBatch {
				batchEnd := min(batchStart+lookupTraversalBatch, end)
				nodes, err := loadLookupRecords(ctx, app, target, ids[batchStart:batchEnd])
				if err != nil {
					return err
				}
				values, err := projectLookupNodes(nodes, lookupField)
				if err != nil {
					return err
				}
				if err := collector.appendProjected(values); err != nil {
					return err
				}
			}
			if stop {
				return errLookupPageComplete
			}
			return nil
		}
		for start := 0; start < len(ids); start += lookupTraversalBatch {
			end := min(start+lookupTraversalBatch, len(ids))
			nodes, err := loadLookupRecords(ctx, app, target, ids[start:end])
			if err != nil {
				return err
			}
			for _, node := range nodes {
				if err := walkLookupPage(
					ctx, app, node, lookupField, path, pathIndex+1, definitions, collector,
				); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walkJunctionLookupPage(
		ctx, app, source, lookupField, path, pathIndex, relation, definitions, collector,
	)
}

func walkJunctionLookupPage(
	ctx context.Context,
	app core.App,
	source traversalNode,
	lookupField schema.FieldDefinition,
	path []schema.LookupPathStep,
	pathIndex int,
	relation schema.FieldDefinition,
	definitions map[string]schema.TableDefinition,
	collector *lookupPageCollector,
) error {
	if relation.Relation.JunctionTableID == nil {
		return lookupError(
			"mutation.lookup.schema_invalid", "lookup junction schema is unavailable",
		)
	}
	junction, err := describeLookupTable(
		ctx, app, *relation.Relation.JunctionTableID, definitions,
	)
	if err != nil {
		return err
	}
	sourceField, sourceOK := fieldByID(junction, relation.Relation.JunctionSourceFieldID)
	targetIDField, targetOK := fieldByID(junction, relation.Relation.JunctionTargetFieldID)
	if !sourceOK || !targetOK {
		return lookupError(
			"mutation.lookup.schema_invalid", "lookup junction fields are unavailable",
		)
	}
	step := path[pathIndex]
	filter := sourceField.PhysicalName + "={:source}"
	params := dbx.Params{"source": source.record.Id}
	expressions := []dbx.Expression{dbx.HashExp{sourceField.PhysicalName: source.record.Id}}
	if relation.Relation.EffectiveMode() == "m2a" && step.M2ACollection != "" {
		discriminator, found := fieldByID(
			junction, relation.Relation.JunctionDiscriminatorFieldID,
		)
		if !found {
			return lookupError(
				"mutation.lookup.schema_invalid", "lookup m2a discriminator is unavailable",
			)
		}
		filter += " && " + discriminator.PhysicalName + "={:targetTable}"
		params["targetTable"] = step.M2ACollection
		expressions = append(
			expressions, dbx.HashExp{discriminator.PhysicalName: step.M2ACollection},
		)
	}
	collection, err := lookupCollection(app, junction.TableID)
	if err != nil {
		return err
	}
	total64, err := app.CountRecords(collection, expressions...)
	if err != nil {
		return lookupError(
			"mutation.lookup.storage_failed", "lookup junction storage is unavailable",
		)
	}
	terminal := pathIndex == len(path)-1
	if terminal {
		start, end, stop := collector.rangeFor(int(total64))
		if start == end {
			if stop {
				return errLookupPageComplete
			}
			return nil
		}
		var junctionField *schema.FieldDefinition
		if lookupField.Lookup.JunctionFieldID != "" {
			field, found := fieldByID(junction, lookupField.Lookup.JunctionFieldID)
			if !found {
				return lookupError(
					"mutation.lookup.schema_invalid", "lookup junction field is unavailable",
				)
			}
			junctionField = &field
		}
		for batchOffset := start; batchOffset < end; batchOffset += lookupTraversalBatch {
			if err := ctx.Err(); err != nil {
				return err
			}
			batchLimit := min(lookupTraversalBatch, end-batchOffset)
			rows, err := app.FindRecordsByFilter(
				collection, filter, "+id", batchLimit, batchOffset, params,
			)
			if err != nil {
				return lookupError(
					"mutation.lookup.storage_failed", "lookup junction storage is unavailable",
				)
			}
			if junctionField != nil {
				for _, row := range rows {
					value := decodeLookupFieldValue(
						*junctionField, row.GetRaw(junctionField.PhysicalName),
					)
					if err := collector.appendProjected([]lookupPathValue{
						describedLookupValue(junction, row, *junctionField, value),
					}); err != nil {
						return err
					}
				}
				continue
			}
			if err := appendJunctionTargets(
				ctx, app, rows, junction, targetIDField, relation, lookupField,
				definitions, collector,
			); err != nil {
				return err
			}
		}
		if stop {
			return errLookupPageComplete
		}
		return nil
	}
	for offset := 0; offset < int(total64); offset += lookupTraversalBatch {
		rows, err := app.FindRecordsByFilter(
			collection, filter, "+id", min(lookupTraversalBatch, int(total64)-offset), offset, params,
		)
		if err != nil {
			return lookupError(
				"mutation.lookup.storage_failed", "lookup junction storage is unavailable",
			)
		}
		for _, row := range rows {
			tableID, err := junctionTargetTable(junction, row, relation, step)
			if err != nil {
				return err
			}
			if tableID == "" {
				continue
			}
			target, err := describeLookupTable(ctx, app, tableID, definitions)
			if err != nil {
				return err
			}
			nodes, err := loadLookupRecords(
				ctx, app, target, []string{row.GetString(targetIDField.PhysicalName)},
			)
			if err != nil {
				return err
			}
			for _, node := range nodes {
				if err := walkLookupPage(
					ctx, app, node, lookupField, path, pathIndex+1, definitions, collector,
				); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func appendJunctionTargets(
	ctx context.Context,
	app core.App,
	rows []*core.Record,
	junction schema.TableDefinition,
	targetIDField schema.FieldDefinition,
	relation schema.FieldDefinition,
	lookupField schema.FieldDefinition,
	definitions map[string]schema.TableDefinition,
	collector *lookupPageCollector,
) error {
	for _, row := range rows {
		tableID, err := junctionTargetTable(
			junction, row, relation, schema.LookupPathStep{},
		)
		if err != nil {
			return err
		}
		if tableID == "" {
			continue
		}
		target, err := describeLookupTable(ctx, app, tableID, definitions)
		if err != nil {
			return err
		}
		nodes, err := loadLookupRecords(
			ctx, app, target, []string{row.GetString(targetIDField.PhysicalName)},
		)
		if err != nil {
			return err
		}
		values, err := projectLookupNodes(nodes, lookupField)
		if err != nil {
			return err
		}
		if err := collector.appendProjected(values); err != nil {
			return err
		}
	}
	return nil
}

func junctionTargetTable(
	junction schema.TableDefinition,
	row *core.Record,
	relation schema.FieldDefinition,
	step schema.LookupPathStep,
) (string, error) {
	tableID := relation.Relation.TargetTableID
	if relation.Relation.EffectiveMode() != "m2a" {
		return tableID, nil
	}
	discriminator, found := fieldByID(
		junction, relation.Relation.JunctionDiscriminatorFieldID,
	)
	if !found {
		return "", lookupError(
			"mutation.lookup.schema_invalid", "lookup m2a discriminator is unavailable",
		)
	}
	tableID = row.GetString(discriminator.PhysicalName)
	if !lookupContains(relation.Relation.AllowedTargetTableIDs, tableID) ||
		(step.M2ACollection != "" && step.M2ACollection != tableID) {
		return "", nil
	}
	return tableID, nil
}

func decodeLookupFieldValue(field schema.FieldDefinition, value any) any {
	if field.DataType == schema.DataTypeSelect || field.DataType == schema.DataTypeMultiSelect {
		return schema.DecodeSelectValueFromStorage(field, value)
	}
	return value
}

func projectLookupNodes(
	nodes []traversalNode,
	lookupField schema.FieldDefinition,
) ([]lookupPathValue, error) {
	values := make([]lookupPathValue, 0, len(nodes))
	for _, node := range nodes {
		fieldID := lookupField.Lookup.TargetFieldID
		if len(lookupField.Lookup.TargetFieldIDs) != 0 {
			fieldID = lookupField.Lookup.TargetFieldIDs[node.definition.TableID]
		}
		targetField, found := fieldByID(node.definition, fieldID)
		if !found {
			return nil, lookupError(
				"mutation.lookup.schema_invalid",
				"lookup target field is unavailable",
			)
		}
		value := node.record.GetRaw(targetField.PhysicalName)
		if targetField.DataType == schema.DataTypeSelect ||
			targetField.DataType == schema.DataTypeMultiSelect {
			value = schema.DecodeSelectValueFromStorage(targetField, value)
		}
		values = append(values, describedLookupValue(
			node.definition, node.record, targetField, value,
		))
	}
	return values, nil
}

func lookupJunctionValuesPage(
	ctx context.Context,
	app core.App,
	_ schema.TableDefinition,
	record *core.Record,
	lookupField schema.FieldDefinition,
	relation schema.FieldDefinition,
	step schema.LookupPathStep,
	offset int,
	limit int,
) ([]lookupPathValue, int, error) {
	if relation.Relation.JunctionTableID == nil {
		return nil, 0, lookupError(
			"mutation.lookup.schema_invalid", "lookup junction schema is unavailable",
		)
	}
	definitions := map[string]schema.TableDefinition{}
	junction, err := describeLookupTable(
		ctx, app, *relation.Relation.JunctionTableID, definitions,
	)
	if err != nil {
		return nil, 0, err
	}
	sourceField, sourceOK := fieldByID(junction, relation.Relation.JunctionSourceFieldID)
	targetIDField, targetOK := fieldByID(junction, relation.Relation.JunctionTargetFieldID)
	if !sourceOK || !targetOK {
		return nil, 0, lookupError(
			"mutation.lookup.schema_invalid", "lookup junction fields are unavailable",
		)
	}
	collection, err := lookupCollection(app, junction.TableID)
	if err != nil {
		return nil, 0, err
	}
	filter := sourceField.PhysicalName + "={:source}"
	params := dbx.Params{"source": record.Id}
	expressions := []dbx.Expression{dbx.HashExp{sourceField.PhysicalName: record.Id}}
	if relation.Relation.EffectiveMode() == "m2a" && step.M2ACollection != "" {
		discriminator, found := fieldByID(
			junction, relation.Relation.JunctionDiscriminatorFieldID,
		)
		if !found {
			return nil, 0, lookupError(
				"mutation.lookup.schema_invalid", "lookup m2a discriminator is unavailable",
			)
		}
		filter += " && " + discriminator.PhysicalName + "={:targetTable}"
		params["targetTable"] = step.M2ACollection
		expressions = append(
			expressions, dbx.HashExp{discriminator.PhysicalName: step.M2ACollection},
		)
	}
	total64, err := app.CountRecords(collection, expressions...)
	if err != nil {
		return nil, 0, lookupError(
			"mutation.lookup.storage_failed", "lookup junction storage is unavailable",
		)
	}
	rows, err := app.FindRecordsByFilter(
		collection, filter, "+id", limit, offset, params,
	)
	if err != nil {
		return nil, 0, lookupError(
			"mutation.lookup.storage_failed", "lookup junction storage is unavailable",
		)
	}
	if lookupField.Lookup.JunctionFieldID != "" {
		field, found := fieldByID(junction, lookupField.Lookup.JunctionFieldID)
		if !found {
			return nil, 0, lookupError(
				"mutation.lookup.schema_invalid", "lookup junction field is unavailable",
			)
		}
		values := make([]lookupPathValue, 0, len(rows))
		for _, row := range rows {
			value := row.GetRaw(field.PhysicalName)
			if field.DataType == schema.DataTypeSelect ||
				field.DataType == schema.DataTypeMultiSelect {
				value = schema.DecodeSelectValueFromStorage(field, value)
			}
			values = append(values, describedLookupValue(junction, row, field, value))
		}
		return values, int(total64), nil
	}
	nodes := make([]traversalNode, 0, len(rows))
	for _, row := range rows {
		tableID := relation.Relation.TargetTableID
		if relation.Relation.EffectiveMode() == "m2a" {
			discriminator, found := fieldByID(
				junction, relation.Relation.JunctionDiscriminatorFieldID,
			)
			if !found {
				return nil, 0, lookupError(
					"mutation.lookup.schema_invalid", "lookup m2a discriminator is unavailable",
				)
			}
			tableID = row.GetString(discriminator.PhysicalName)
		}
		target, describeErr := describeLookupTable(ctx, app, tableID, definitions)
		if describeErr != nil {
			return nil, 0, describeErr
		}
		loaded, loadErr := loadLookupRecords(
			ctx, app, target, []string{row.GetString(targetIDField.PhysicalName)},
		)
		if loadErr != nil {
			return nil, 0, loadErr
		}
		nodes = append(nodes, loaded...)
	}
	values, err := projectLookupNodes(nodes, lookupField)
	return values, int(total64), err
}

func lookupPathValues(
	ctx context.Context,
	app core.App,
	sourceDefinition schema.TableDefinition,
	record *core.Record,
	lookupField schema.FieldDefinition,
) ([]lookupPathValue, error) {
	path := lookupField.Lookup.EffectivePath()
	if len(path) == 0 {
		return nil, lookupError(
			"mutation.lookup.schema_invalid",
			"lookup path metadata is unavailable",
		)
	}
	nodes := []traversalNode{{
		definition: sourceDefinition,
		record:     record,
	}}
	budget := &materializationBudget{remainingBytes: lookupMaterializationBytes}
	var junctions []junctionNode
	for index, step := range path {
		var err error
		nodes, junctions, err = traverseRelation(
			ctx,
			app,
			nodes,
			step,
			index == len(path)-1 && lookupField.Lookup.JunctionFieldID != "",
			budget,
		)
		if err != nil {
			return nil, err
		}
	}
	if lookupField.Lookup.JunctionFieldID != "" {
		values := make([]lookupPathValue, 0, len(junctions))
		for _, junction := range junctions {
			field, found := fieldByID(
				junction.definition,
				lookupField.Lookup.JunctionFieldID,
			)
			if !found {
				return nil, lookupError(
					"mutation.lookup.schema_invalid",
					"lookup junction field is unavailable",
				)
			}
			value := junction.record.GetRaw(field.PhysicalName)
			if field.DataType == schema.DataTypeSelect ||
				field.DataType == schema.DataTypeMultiSelect {
				value = schema.DecodeSelectValueFromStorage(field, value)
			}
			if err := budget.consume(value); err != nil {
				return nil, err
			}
			values = append(values, describedLookupValue(
				junction.definition, junction.record, field, value,
			))
		}
		return values, nil
	}
	values := make([]lookupPathValue, 0, len(nodes))
	for _, node := range nodes {
		fieldID := lookupField.Lookup.TargetFieldID
		if len(lookupField.Lookup.TargetFieldIDs) != 0 {
			fieldID = lookupField.Lookup.TargetFieldIDs[node.definition.TableID]
		}
		targetField, found := fieldByID(node.definition, fieldID)
		if !found {
			return nil, lookupError(
				"mutation.lookup.schema_invalid",
				"lookup target field is unavailable",
			)
		}
		value := node.record.GetRaw(targetField.PhysicalName)
		if targetField.DataType == schema.DataTypeSelect ||
			targetField.DataType == schema.DataTypeMultiSelect {
			value = schema.DecodeSelectValueFromStorage(targetField, value)
		}
		if err := budget.consume(value); err != nil {
			return nil, err
		}
		values = append(values, describedLookupValue(
			node.definition, node.record, targetField, value,
		))
	}
	return values, nil
}

func traverseRelation(
	ctx context.Context,
	app core.App,
	sources []traversalNode,
	step schema.LookupPathStep,
	junctionOnly bool,
	budget *materializationBudget,
) ([]traversalNode, []junctionNode, error) {
	targets := []traversalNode{}
	junctions := []junctionNode{}
	definitions := map[string]schema.TableDefinition{}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		relation, found := fieldByID(source.definition, step.RelationFieldID)
		if !found || relation.Relation == nil {
			return nil, nil, lookupError(
				"mutation.lookup.schema_invalid",
				"lookup path relation metadata is unavailable",
			)
		}
		mode := relation.Relation.EffectiveMode()
		if mode == "direct" {
			ids := relationIDs(source.record.GetRaw(relation.PhysicalName))
			target, err := describeLookupTable(
				ctx, app, relation.Relation.TargetTableID, definitions,
			)
			if err != nil {
				return nil, nil, err
			}
			for start := 0; start < len(ids); start += lookupTraversalBatch {
				end := min(start+lookupTraversalBatch, len(ids))
				loaded, err := loadLookupRecords(ctx, app, target, ids[start:end])
				if err != nil {
					return nil, nil, err
				}
				for _, node := range loaded {
					if err := budget.consume(node.record); err != nil {
						return nil, nil, err
					}
					targets = append(targets, node)
				}
			}
			continue
		}
		if relation.Relation.JunctionTableID == nil {
			return nil, nil, lookupError(
				"mutation.lookup.schema_invalid",
				"lookup junction schema is unavailable",
			)
		}
		junction, err := describeLookupTable(
			ctx, app, *relation.Relation.JunctionTableID, definitions,
		)
		if err != nil {
			return nil, nil, err
		}
		sourceField, sourceOK := fieldByID(
			junction, relation.Relation.JunctionSourceFieldID,
		)
		targetIDField, targetOK := fieldByID(
			junction, relation.Relation.JunctionTargetFieldID,
		)
		if !sourceOK || !targetOK {
			return nil, nil, lookupError(
				"mutation.lookup.schema_invalid",
				"lookup junction fields are unavailable",
			)
		}
		rows, err := lookupJunctionRows(
			ctx, app, junction, sourceField, source.record.Id, budget,
		)
		if err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			tableID := relation.Relation.TargetTableID
			if mode == "m2a" {
				discriminator, found := fieldByID(
					junction,
					relation.Relation.JunctionDiscriminatorFieldID,
				)
				if !found {
					return nil, nil, lookupError(
						"mutation.lookup.schema_invalid",
						"lookup m2a discriminator is unavailable",
					)
				}
				tableID = row.GetString(discriminator.PhysicalName)
				if !lookupContains(
					relation.Relation.AllowedTargetTableIDs, tableID,
				) {
					continue
				}
				if step.M2ACollection != "" &&
					step.M2ACollection != tableID {
					continue
				}
			}
			junctions = append(junctions, junctionNode{
				definition: junction,
				record:     row,
			})
			if junctionOnly {
				continue
			}
			target, err := describeLookupTable(
				ctx, app, tableID, definitions,
			)
			if err != nil {
				return nil, nil, err
			}
			loaded, err := loadLookupRecords(
				ctx,
				app,
				target,
				[]string{row.GetString(targetIDField.PhysicalName)},
			)
			if err != nil {
				return nil, nil, err
			}
			for _, node := range loaded {
				if err := budget.consume(node.record); err != nil {
					return nil, nil, err
				}
			}
			targets = append(targets, loaded...)
		}
	}
	return targets, junctions, nil
}

func describeLookupTable(
	ctx context.Context,
	app core.App,
	tableID string,
	cache map[string]schema.TableDefinition,
) (schema.TableDefinition, error) {
	if target, found := cache[tableID]; found {
		return target, nil
	}
	target, err := schemaapi.New(app).Describe(ctx, tableID)
	if err != nil {
		return schema.TableDefinition{}, lookupError(
			"mutation.lookup.schema_invalid",
			"lookup target schema is unavailable",
		)
	}
	cache[tableID] = target
	return target, nil
}

func lookupJunctionRows(
	ctx context.Context,
	app core.App,
	junction schema.TableDefinition,
	sourceField schema.FieldDefinition,
	sourceID string,
	budget *materializationBudget,
) ([]*core.Record, error) {
	collection, err := lookupCollection(app, junction.TableID)
	if err != nil {
		return nil, err
	}
	result := []*core.Record{}
	for offset := 0; ; offset += lookupTraversalBatch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, err := app.FindRecordsByFilter(
			collection,
			sourceField.PhysicalName+"={:source}",
			"+id",
			lookupTraversalBatch,
			offset,
			dbx.Params{"source": sourceID},
		)
		if err != nil {
			return nil, lookupError(
				"mutation.lookup.storage_failed",
				"lookup junction storage is unavailable",
			)
		}
		for _, row := range rows {
			if err := budget.consume(row); err != nil {
				return nil, err
			}
			result = append(result, row)
		}
		if len(rows) < lookupTraversalBatch {
			return result, nil
		}
	}
}

func lookupCollection(app core.App, tableID string) (*core.Collection, error) {
	meta, err := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": tableID},
	)
	if err != nil {
		return nil, lookupError(
			"mutation.lookup.storage_failed", "lookup storage is unavailable",
		)
	}
	collection, err := app.FindCollectionByNameOrId(meta.GetString("collection_id"))
	if err != nil {
		return nil, lookupError(
			"mutation.lookup.storage_failed", "lookup storage is unavailable",
		)
	}
	return collection, nil
}

func lookupContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func loadLookupRecords(
	ctx context.Context,
	app core.App,
	target schema.TableDefinition,
	recordIDs []string,
) ([]traversalNode, error) {
	if len(recordIDs) == 0 {
		return []traversalNode{}, nil
	}
	meta, err := app.FindFirstRecordByFilter(
		"vibetable_tables",
		"table_id={:table}",
		dbx.Params{"table": target.TableID},
	)
	if err != nil {
		return nil, lookupError(
			"mutation.lookup.storage_failed",
			"lookup target storage is unavailable",
		)
	}
	collection, err := app.FindCollectionByNameOrId(meta.GetString("collection_id"))
	if err != nil {
		return nil, lookupError(
			"mutation.lookup.storage_failed",
			"lookup target storage is unavailable",
		)
	}
	values := make([]traversalNode, 0, len(recordIDs))
	for _, recordID := range recordIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		targetRecord, err := app.FindRecordById(collection, recordID)
		if err != nil {
			return nil, lookupError(
				"mutation.lookup.target_not_found",
				"lookup relation references a missing record",
			)
		}
		values = append(values, traversalNode{
			definition: target,
			record:     targetRecord,
		})
	}
	return values, nil
}

func aggregate(
	kind string,
	storage schema.StorageType,
	values []any,
) (any, error) {
	switch kind {
	case "count":
		return len(values), nil
	case "countNonNull":
		count := 0
		for _, value := range values {
			if value != nil {
				count++
			}
		}
		return count, nil
	case "distinct":
		result := make([]any, 0, len(values))
		seen := map[string]struct{}{}
		for _, value := range values {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, lookupError(
					"mutation.lookup.invalid_value",
					"lookup distinct encountered an invalid value",
				)
			}
			key := string(encoded)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, value)
		}
		return result, nil
	case "none":
		if len(values) == 0 {
			return nil, nil
		}
		if len(values) == 1 {
			return values[0], nil
		}
		return values, nil
	case "first":
		if len(values) == 0 {
			return nil, nil
		}
		return values[0], nil
	case "sum":
		total := 0.0
		for _, value := range values {
			number, ok := finiteNumber(value)
			if !ok {
				return nil, lookupError(
					"mutation.lookup.invalid_value",
					"lookup sum encountered a non-numeric value",
				)
			}
			total += number
			if math.IsInf(total, 0) || math.IsNaN(total) {
				return nil, lookupError(
					"mutation.lookup.invalid_value",
					"lookup sum exceeded the numeric range",
				)
			}
		}
		return total, nil
	case "avg":
		if len(values) == 0 {
			return nil, nil
		}
		total := 0.0
		for _, value := range values {
			number, ok := finiteNumber(value)
			if !ok {
				return nil, lookupError(
					"mutation.lookup.invalid_value",
					"lookup average encountered a non-numeric value",
				)
			}
			total += number
			if math.IsInf(total, 0) || math.IsNaN(total) {
				return nil, lookupError(
					"mutation.lookup.invalid_value",
					"lookup average exceeded the numeric range",
				)
			}
		}
		return total / float64(len(values)), nil
	case "min", "max":
		if len(values) == 0 {
			return nil, nil
		}
		best := values[0]
		for _, candidate := range values[1:] {
			comparison, err := compare(storage, candidate, best)
			if err != nil {
				return nil, err
			}
			if (kind == "min" && comparison < 0) ||
				(kind == "max" && comparison > 0) {
				best = candidate
			}
		}
		return best, nil
	default:
		return nil, lookupError(
			"mutation.lookup.schema_invalid",
			"lookup aggregate is unsupported",
		)
	}
}

func compare(storage schema.StorageType, left, right any) (int, error) {
	if storage == schema.StorageNumber {
		leftNumber, leftOK := finiteNumber(left)
		rightNumber, rightOK := finiteNumber(right)
		if !leftOK || !rightOK {
			return 0, lookupError(
				"mutation.lookup.invalid_value",
				"lookup comparison encountered a non-numeric value",
			)
		}
		switch {
		case leftNumber < rightNumber:
			return -1, nil
		case leftNumber > rightNumber:
			return 1, nil
		default:
			return 0, nil
		}
	}
	leftText, rightText := fmt.Sprint(left), fmt.Sprint(right)
	switch {
	case leftText < rightText:
		return -1, nil
	case leftText > rightText:
		return 1, nil
	default:
		return 0, nil
	}
}

func finiteNumber(value any) (float64, bool) {
	var number float64
	var err error
	switch typed := value.(type) {
	case json.Number:
		number, err = typed.Float64()
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case int32:
		number = float64(typed)
	case uint:
		number = float64(typed)
	case uint64:
		number = float64(typed)
	case string:
		number, err = strconv.ParseFloat(typed, 64)
	default:
		return 0, false
	}
	return number, err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func relationIDs(value any) []string {
	if value == nil {
		return []string{}
	}
	if text, ok := value.(string); ok {
		if text == "" {
			return []string{}
		}
		return []string{text}
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Array && reflected.Kind() != reflect.Slice {
		return []string{}
	}
	result := make([]string, 0, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		text, ok := reflected.Index(index).Interface().(string)
		if ok && text != "" {
			result = append(result, text)
		}
	}
	return result
}

func fieldByID(
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

func lookupError(code, message string) *mutation.ProductError {
	return &mutation.ProductError{
		ContractVersion: mutation.ContractVersion,
		Code:            code,
		Message:         message,
		Details:         map[string]any{},
	}
}
