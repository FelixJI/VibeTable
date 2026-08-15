package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
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
	definition schemaexecution.Table,
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
	definition schemaexecution.Table,
	record *core.Record,
) (map[string]CellValue, error) {
	return calculator.calculateCells(ctx, app, definition, record, true)
}

func (calculator *Calculator) calculateCells(
	ctx context.Context,
	app core.App,
	definition schemaexecution.Table,
	record *core.Record,
	pageValues bool,
) (map[string]CellValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := map[string]CellValue{}
	for _, field := range definition.Snapshot.Fields {
		if field.LogicalType != v2.LogicalLookup || field.Lookup == nil {
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
				result[field.Identity.PhysicalName] = missingLookupSourceCell()
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
		value := canonicalLookupValue(values)
		visibleProvenance := provenance
		if len(visibleProvenance) > cellProvenancePageSize {
			visibleProvenance = visibleProvenance[:cellProvenancePageSize]
		}
		result[field.Identity.PhysicalName] = CellValue{
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
	definition schemaexecution.Table,
	record *core.Record,
	field v2.FieldDefinition,
	offset int,
	limit int,
) (CellValue, error) {
	if field.LogicalType != v2.LogicalLookup || field.Lookup == nil ||
		offset < 0 || limit < 1 || limit > 500 {
		return CellValue{}, lookupError(
			"lookup.request.invalid", "lookup value page request is invalid",
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
	value := canonicalLookupValue(values)
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

type traversalNode struct {
	definition schemaexecution.Table
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
	definition schemaexecution.Table,
	record *core.Record,
	field v2.FieldDefinition,
	value any,
) lookupPathValue {
	collectionLabel := strings.TrimSpace(definition.Snapshot.DisplayName)
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
			recordLabel = strings.TrimSpace(fmt.Sprint(
				decodeLookupFieldValue(displayField, record),
			))
		}
	}
	if recordLabel == "" {
		recordLabel = strings.TrimSpace(fmt.Sprint(value))
	}
	if recordLabel == "" || recordLabel == "<nil>" {
		recordLabel = "未命名记录"
	}
	return lookupPathValue{
		collection: definition.Snapshot.TableID, collectionLabel: collectionLabel,
		itemID: record.Id, recordLabel: recordLabel,
		fieldID: field.Identity.FieldID, fieldLabel: fieldLabel, value: value,
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
	sourceDefinition schemaexecution.Table,
	record *core.Record,
	lookupField v2.FieldDefinition,
	offset int,
	limit int,
) ([]lookupPathValue, int, bool, error) {
	path := lookupField.Lookup.Path
	if len(path) == 0 {
		return nil, 0, false, lookupError(
			"mutation.lookup.schema_invalid", "lookup path metadata is unavailable",
		)
	}
	if len(path) == 1 {
		relation, found := fieldByID(sourceDefinition, path[0].RelationFieldID)
		if !found || relation.Relation == nil {
			return nil, 0, false, lookupError(
				"mutation.lookup.schema_invalid", "lookup path relation metadata is unavailable",
			)
		}
		ids := relationIDs(record.GetRaw(relation.Identity.PhysicalName))
		total := len(ids)
		target, err := describeLookupTable(
			ctx, app, relation.Relation.TargetTableID, map[string]schemaexecution.Table{},
		)
		if err != nil {
			return nil, 0, false, err
		}
		nodes, err := loadLookupRecords(ctx, app, target, sliceStrings(ids, offset, limit))
		if err != nil {
			return nil, 0, false, err
		}
		values, err := projectLookupNodes(nodes, lookupField)
		return values, total, true, err
	}
	collector := lookupPageCollector{offset: offset, limit: limit}
	err := walkLookupPage(
		ctx, app, traversalNode{definition: sourceDefinition, record: record},
		lookupField, path, 0, map[string]schemaexecution.Table{}, &collector,
	)
	if errors.Is(err, errLookupPageComplete) {
		return collector.values, collector.total, false, nil
	}
	return collector.values, collector.total, err == nil, err
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
	lookupField v2.FieldDefinition,
	path []v2.LookupPathStep,
	pathIndex int,
	definitions map[string]schemaexecution.Table,
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
	ids := relationIDs(source.record.GetRaw(relation.Identity.PhysicalName))
	target, err := describeLookupTable(
		ctx, app, relation.Relation.TargetTableID, definitions,
	)
	if err != nil {
		return err
	}
	if pathIndex == len(path)-1 {
		start, end, stop := collector.rangeFor(len(ids))
		for batchStart := start; batchStart < end; batchStart += lookupTraversalBatch {
			batchEnd := min(batchStart+lookupTraversalBatch, end)
			nodes, loadErr := loadLookupRecords(ctx, app, target, ids[batchStart:batchEnd])
			if loadErr != nil {
				return loadErr
			}
			values, projectErr := projectLookupNodes(nodes, lookupField)
			if projectErr != nil {
				return projectErr
			}
			if appendErr := collector.appendProjected(values); appendErr != nil {
				return appendErr
			}
		}
		if stop {
			return errLookupPageComplete
		}
		return nil
	}
	for start := 0; start < len(ids); start += lookupTraversalBatch {
		end := min(start+lookupTraversalBatch, len(ids))
		nodes, loadErr := loadLookupRecords(ctx, app, target, ids[start:end])
		if loadErr != nil {
			return loadErr
		}
		for _, node := range nodes {
			if walkErr := walkLookupPage(
				ctx, app, node, lookupField, path, pathIndex+1, definitions, collector,
			); walkErr != nil {
				return walkErr
			}
		}
	}
	return nil
}

func lookupPathValues(
	ctx context.Context,
	app core.App,
	sourceDefinition schemaexecution.Table,
	record *core.Record,
	lookupField v2.FieldDefinition,
) ([]lookupPathValue, error) {
	path := lookupField.Lookup.Path
	if len(path) == 0 {
		return nil, lookupError(
			"mutation.lookup.schema_invalid", "lookup path metadata is unavailable",
		)
	}
	values := []lookupPathValue{}
	budget := &materializationBudget{remainingBytes: lookupMaterializationBytes}
	collector := lookupPageCollector{
		offset: 0,
		limit:  int(^uint(0) >> 1),
		sink: func(batch []lookupPathValue) error {
			for _, value := range batch {
				if err := budget.consume(value.value); err != nil {
					return err
				}
			}
			values = append(values, batch...)
			return nil
		},
	}
	err := walkLookupPage(
		ctx, app, traversalNode{definition: sourceDefinition, record: record},
		lookupField, path, 0, map[string]schemaexecution.Table{}, &collector,
	)
	if errors.Is(err, errLookupPageComplete) {
		err = nil
	}
	return values, err
}

func projectLookupNodes(
	nodes []traversalNode,
	lookupField v2.FieldDefinition,
) ([]lookupPathValue, error) {
	values := make([]lookupPathValue, 0, len(nodes))
	for _, node := range nodes {
		targetField, found := fieldByID(node.definition, lookupField.Lookup.TargetFieldID)
		if !found {
			return nil, lookupError(
				"mutation.lookup.schema_invalid", "lookup target field is unavailable",
			)
		}
		value := decodeLookupFieldValue(targetField, node.record)
		values = append(values, describedLookupValue(
			node.definition, node.record, targetField, value,
		))
	}
	return values, nil
}

func decodeLookupFieldValue(field v2.FieldDefinition, record *core.Record) any {
	// Lookup targets are existing provider rows. V2 select storage is the
	// stable optionId, so the physical value is already the product value and
	// preserves the historical behavior for rows created before companions.
	return record.GetRaw(field.Identity.PhysicalName)
}

func canonicalLookupValue(values []any) any {
	if len(values) == 0 {
		return nil
	}
	if len(values) == 1 {
		return values[0]
	}
	return values
}

func describeLookupTable(
	ctx context.Context,
	app core.App,
	tableID string,
	cache map[string]schemaexecution.Table,
) (schemaexecution.Table, error) {
	if target, found := cache[tableID]; found {
		return target, nil
	}
	target, err := schemaexecution.Describe(ctx, app, tableID)
	if err != nil {
		return schemaexecution.Table{}, lookupError(
			"mutation.lookup.schema_invalid",
			"lookup target schema is unavailable",
		)
	}
	cache[tableID] = target
	return target, nil
}

func loadLookupRecords(
	ctx context.Context,
	app core.App,
	target schemaexecution.Table,
	recordIDs []string,
) ([]traversalNode, error) {
	if len(recordIDs) == 0 {
		return []traversalNode{}, nil
	}
	collection, err := app.FindCollectionByNameOrId(target.PhysicalName)
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
	definition schemaexecution.Table,
	fieldID string,
) (v2.FieldDefinition, bool) {
	return definition.Field(fieldID)
}

func lookupError(code, message string) *mutation.ProductError {
	return &mutation.ProductError{
		ContractVersion: mutation.ContractVersion,
		Code:            code,
		Message:         message,
		Details:         map[string]any{},
	}
}
