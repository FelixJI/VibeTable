package lookup

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

const (
	maxTraversalCost       = 100000
	cellProvenancePageSize = 100
)

type Calculator struct{}

func NewCalculator() *Calculator {
	return &Calculator{}
}

type ValueProvenance struct {
	Collection string `json:"collection"`
	ItemID     string `json:"itemId"`
	FieldID    string `json:"fieldId"`
	Value      any    `json:"value"`
}

type CellValue struct {
	State             string            `json:"state"`
	Value             any               `json:"value"`
	Provenance        []ValueProvenance `json:"provenance"`
	ProvenanceTotal   int               `json:"provenanceTotal"`
	ProvenanceOffset  int               `json:"provenanceOffset"`
	ProvenanceLimit   int               `json:"provenanceLimit"`
	ProvenanceHasMore bool              `json:"provenanceHasMore"`
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
	budget := &fanoutBudget{remaining: maxTraversalCost}
	result := map[string]CellValue{}
	for _, field := range definition.Fields {
		if field.Kind != schema.FieldKindLookup || field.Lookup == nil {
			continue
		}
		var resolved []lookupPathValue
		var total int
		var err error
		if pageValues && field.Lookup.Aggregate == "none" {
			resolved, total, err = lookupPathValuesPage(
				ctx, app, definition, record, field, 0, cellProvenancePageSize, budget,
			)
		} else {
			resolved, err = lookupPathValues(
				ctx, app, definition, record, field, budget,
			)
			total = len(resolved)
		}
		if err != nil {
			return nil, err
		}
		values := make([]any, 0, len(resolved))
		provenance := make([]ValueProvenance, 0, len(resolved))
		for _, item := range resolved {
			values = append(values, item.value)
			provenance = append(provenance, ValueProvenance{
				Collection: item.collection,
				ItemID:     item.itemID,
				FieldID:    item.fieldID,
				Value:      item.value,
			})
		}
		value, err := aggregate(field.Lookup.Aggregate, field.StorageType, values)
		if err != nil {
			return nil, err
		}
		visibleProvenance := provenance
		if len(visibleProvenance) > cellProvenancePageSize {
			visibleProvenance = visibleProvenance[:cellProvenancePageSize]
		}
		result[field.PhysicalName] = CellValue{
			State: "ok", Value: value, Provenance: visibleProvenance,
			ProvenanceTotal: total, ProvenanceOffset: 0,
			ProvenanceLimit:   cellProvenancePageSize,
			ProvenanceHasMore: total > len(visibleProvenance),
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
	resolved, total, err := lookupPathValuesPage(
		ctx, app, definition, record, field, offset, limit,
		&fanoutBudget{remaining: maxTraversalCost},
	)
	if err != nil {
		return CellValue{}, err
	}
	values, provenance := resolvedValues(resolved)
	value, err := aggregate(field.Lookup.Aggregate, field.StorageType, values)
	if err != nil {
		return CellValue{}, err
	}
	return CellValue{
		State: "ok", Value: value, Provenance: provenance,
		ProvenanceTotal: total, ProvenanceOffset: offset, ProvenanceLimit: limit,
		ProvenanceHasMore: offset+len(provenance) < total,
	}, nil
}

func resolvedValues(resolved []lookupPathValue) ([]any, []ValueProvenance) {
	values := make([]any, 0, len(resolved))
	provenance := make([]ValueProvenance, 0, len(resolved))
	for _, item := range resolved {
		values = append(values, item.value)
		provenance = append(provenance, ValueProvenance{
			Collection: item.collection,
			ItemID:     item.itemID,
			FieldID:    item.fieldID,
			Value:      item.value,
		})
	}
	return values, provenance
}

type fanoutBudget struct {
	remaining int
}

func (budget *fanoutBudget) consume(count int) error {
	if count < 0 || count > budget.remaining {
		return lookupError(
			"lookup.value.too_expensive",
			"lookup path exceeds the multi-hop traversal cost budget",
		)
	}
	budget.remaining -= count
	return nil
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
	collection string
	itemID     string
	fieldID    string
	value      any
}

func lookupPathValuesPage(
	ctx context.Context,
	app core.App,
	sourceDefinition schema.TableDefinition,
	record *core.Record,
	lookupField schema.FieldDefinition,
	offset int,
	limit int,
	budget *fanoutBudget,
) ([]lookupPathValue, int, error) {
	path := lookupField.Lookup.EffectivePath()
	if len(path) != 1 {
		resolved, err := lookupPathValues(
			ctx, app, sourceDefinition, record, lookupField, budget,
		)
		if err != nil {
			return nil, 0, err
		}
		total := len(resolved)
		return sliceLookupValues(resolved, offset, limit), total, nil
	}
	relation, found := fieldByID(sourceDefinition, path[0].RelationFieldID)
	if !found || relation.Relation == nil {
		return nil, 0, lookupError(
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
			return nil, 0, err
		}
		nodes, err := loadLookupRecords(ctx, app, target, ids)
		if err != nil {
			return nil, 0, err
		}
		values, err := projectLookupNodes(nodes, lookupField)
		return values, total, err
	}
	return lookupJunctionValuesPage(
		ctx, app, sourceDefinition, record, lookupField, relation, path[0], offset, limit,
	)
}

func sliceLookupValues(values []lookupPathValue, offset int, limit int) []lookupPathValue {
	if offset >= len(values) {
		return []lookupPathValue{}
	}
	end := offset + limit
	if end > len(values) {
		end = len(values)
	}
	return values[offset:end]
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
		values = append(values, lookupPathValue{
			collection: node.definition.TableID,
			itemID:     node.record.Id,
			fieldID:    targetField.FieldID,
			value:      value,
		})
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
			values = append(values, lookupPathValue{
				collection: junction.TableID, itemID: row.Id,
				fieldID: field.FieldID, value: value,
			})
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
	budget *fanoutBudget,
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
	var junctions []junctionNode
	for index, step := range path {
		var err error
		nodes, junctions, err = traverseRelation(
			ctx,
			app,
			nodes,
			step,
			index == len(path)-1 && lookupField.Lookup.JunctionFieldID != "",
			len(path) > 1,
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
			values = append(values, lookupPathValue{
				collection: junction.definition.TableID,
				itemID:     junction.record.Id,
				fieldID:    field.FieldID,
				value:      value,
			})
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
		values = append(values, lookupPathValue{
			collection: node.definition.TableID,
			itemID:     node.record.Id,
			fieldID:    targetField.FieldID,
			value:      value,
		})
	}
	return values, nil
}

func traverseRelation(
	ctx context.Context,
	app core.App,
	sources []traversalNode,
	step schema.LookupPathStep,
	junctionOnly bool,
	chargeTraversal bool,
	budget *fanoutBudget,
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
			if chargeTraversal {
				if err := budget.consume(len(ids)); err != nil {
					return nil, nil, err
				}
			}
			target, err := describeLookupTable(
				ctx, app, relation.Relation.TargetTableID, definitions,
			)
			if err != nil {
				return nil, nil, err
			}
			loaded, err := loadLookupRecords(ctx, app, target, ids)
			if err != nil {
				return nil, nil, err
			}
			targets = append(targets, loaded...)
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
		remaining := budget.remaining
		if !chargeTraversal {
			remaining = math.MaxInt - 1
		}
		rows, err := lookupJunctionRows(
			app, junction, sourceField, source.record.Id, remaining,
		)
		if err != nil {
			return nil, nil, err
		}
		if chargeTraversal {
			if err := budget.consume(len(rows)); err != nil {
				return nil, nil, err
			}
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
	app core.App,
	junction schema.TableDefinition,
	sourceField schema.FieldDefinition,
	sourceID string,
	remaining int,
) ([]*core.Record, error) {
	collection, err := lookupCollection(app, junction.TableID)
	if err != nil {
		return nil, err
	}
	rows, err := app.FindRecordsByFilter(
		collection,
		sourceField.PhysicalName+"={:source}",
		"+id",
		remaining+1,
		0,
		dbx.Params{"source": sourceID},
	)
	if err != nil {
		return nil, lookupError(
			"mutation.lookup.storage_failed",
			"lookup junction storage is unavailable",
		)
	}
	if len(rows) > remaining {
		return nil, lookupError(
			"lookup.value.too_expensive",
			"lookup path exceeds the multi-hop traversal cost budget",
		)
	}
	return rows, nil
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
