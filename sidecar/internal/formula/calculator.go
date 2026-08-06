package formula

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

// Calculator implements mutation.FormulaCalculator without creating a package
// dependency from the formula runtime back to the mutation kernel.
type Calculator struct {
	compiler *Compiler
	mu       sync.RWMutex
	cache    map[string]*Plan
}

func NewCalculator(compiler *Compiler) *Calculator {
	if compiler == nil {
		compiler = NewCompiler(DefaultLimits())
	}
	return &Calculator{compiler: compiler, cache: map[string]*Plan{}}
}

func (calculator *Calculator) Calculate(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	record *core.Record,
) (map[string]any, error) {
	plan, err := calculator.plan(definition)
	if err != nil {
		return nil, err
	}
	row := make(map[string]any, len(definition.Fields))
	aggregateTargets, countRelations := relationAggregateRequirements(plan)
	for _, field := range definition.Fields {
		value := record.GetRaw(field.PhysicalName)
		if field.DataType == schema.DataTypeSelect ||
			field.DataType == schema.DataTypeMultiSelect {
			value = schema.DecodeSelectValueFromStorage(field, value)
		}
		if field.Kind == schema.FieldKindRelation && field.Relation != nil {
			var err error
			recordIDs := relationRecordIDs(value)
			targets, aggregates := aggregateTargets[field.PhysicalName]
			_, counts := countRelations[field.PhysicalName]
			if field.Relation.Cardinality != "one" && (aggregates || counts) {
				value, err = calculator.resolveRelationAggregates(
					ctx, app, field, recordIDs, targets,
				)
			} else {
				value, err = calculator.resolveRelation(ctx, app, field, recordIDs)
			}
			if err != nil {
				return nil, err
			}
		}
		row[field.PhysicalName] = value
	}
	result, formulaErr := plan.Evaluate(ctx, row, nil)
	if formulaErr != nil {
		return nil, formulaErr
	}
	return result, nil
}

func (calculator *Calculator) resolveRelation(
	ctx context.Context,
	app core.App,
	field schema.FieldDefinition,
	recordIDs []string,
) (any, error) {
	if len(recordIDs) > calculator.compiler.limits.CollectionSize {
		return nil, formulaError(
			"formula.resource_limit",
			"relation exceeds the formula collection limit",
			map[string]any{
				"fieldId": field.FieldID,
				"limit":   calculator.compiler.limits.CollectionSize,
			},
		)
	}
	target, collection, targetErr := relationTarget(app, field)
	if targetErr != nil {
		return nil, targetErr
	}
	values := make([]any, 0, len(recordIDs))
	for _, recordID := range recordIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		targetRecord, findErr := app.FindRecordById(collection, recordID)
		if findErr != nil {
			return nil, formulaError(
				"formula.dependency",
				"relation references a missing target record",
				map[string]any{
					"fieldId":  field.FieldID,
					"recordId": recordID,
				},
			)
		}
		value := map[string]any{"id": targetRecord.Id}
		for _, targetField := range target.Fields {
			if targetField.DataType == schema.DataTypeSecret ||
				targetField.DataType == schema.DataTypeHash {
				continue
			}
			targetValue := targetRecord.GetRaw(targetField.PhysicalName)
			if targetField.DataType == schema.DataTypeSelect ||
				targetField.DataType == schema.DataTypeMultiSelect {
				targetValue = schema.DecodeSelectValueFromStorage(targetField, targetValue)
			}
			value[targetField.PhysicalName] = targetValue
		}
		values = append(values, value)
	}
	if field.Relation.Cardinality == "one" {
		if len(values) == 0 {
			return nil, nil
		}
		return values[0], nil
	}
	return values, nil
}

func relationTarget(
	app core.App,
	field schema.FieldDefinition,
) (schema.TableDefinition, *core.Collection, error) {
	meta, err := app.FindFirstRecordByFilter(
		"vibetable_tables",
		"table_id={:table}",
		dbx.Params{"table": field.Relation.TargetTableID},
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return schema.TableDefinition{}, nil, formulaError(
				"formula.dependency",
				"relation target table is unavailable",
				map[string]any{"fieldId": field.FieldID},
			)
		}
		return schema.TableDefinition{}, nil, err
	}
	var target schema.TableDefinition
	raw, marshalErr := json.Marshal(meta.GetRaw("definition_json"))
	if marshalErr != nil || json.Unmarshal(raw, &target) != nil {
		return schema.TableDefinition{}, nil, formulaError(
			"formula.dependency",
			"relation target schema is invalid",
			map[string]any{"fieldId": field.FieldID},
		)
	}
	collection, err := app.FindCollectionByNameOrId(
		meta.GetString("collection_id"),
	)
	if err != nil {
		return schema.TableDefinition{}, nil, formulaError(
			"formula.dependency",
			"relation target storage is unavailable",
			map[string]any{"fieldId": field.FieldID},
		)
	}
	return target, collection, nil
}

type relationSQLAggregate struct {
	Matched int             `db:"matched"`
	Count   int             `db:"value_count"`
	Sum     sql.NullFloat64 `db:"value_sum"`
	Min     sql.NullFloat64 `db:"value_min"`
	Max     sql.NullFloat64 `db:"value_max"`
}

func (calculator *Calculator) resolveRelationAggregates(
	ctx context.Context,
	app core.App,
	field schema.FieldDefinition,
	recordIDs []string,
	targetNames []string,
) (any, error) {
	target, collection, err := relationTarget(app, field)
	if err != nil {
		return nil, err
	}
	recordIDs = uniqueSortedStrings(recordIDs)
	fields := make(map[string]schema.FieldDefinition, len(target.Fields))
	for _, candidate := range target.Fields {
		fields[candidate.PhysicalName] = candidate
	}
	carrierFields := make(map[string]any, len(targetNames))
	for _, targetName := range targetNames {
		targetField, exists := fields[targetName]
		if !exists || targetField.Kind == schema.FieldKindRelation ||
			targetField.DataType == schema.DataTypeSecret ||
			targetField.DataType == schema.DataTypeHash {
			return nil, formulaError(
				"formula.dependency", "relation aggregate target field is unavailable",
				map[string]any{"fieldId": field.FieldID, "target": targetName},
			)
		}
		stats, aggregateErr := aggregateRelationField(
			ctx, app, collection.Name, targetField, recordIDs,
		)
		if aggregateErr != nil {
			return nil, aggregateErr
		}
		carrierFields[targetName] = stats
	}
	if len(targetNames) == 0 {
		matched, countErr := countRelationRecords(ctx, app, collection.Name, recordIDs)
		if countErr != nil {
			return nil, countErr
		}
		if matched != len(recordIDs) {
			return nil, missingRelationTargetError(field, len(recordIDs), matched)
		}
	}
	return map[string]any{
		precomputedRelationMarker:   true,
		precomputedRelationCountKey: int64(len(recordIDs)),
		precomputedRelationFields:   carrierFields,
	}, nil
}

func aggregateRelationField(
	ctx context.Context,
	app core.App,
	collectionName string,
	field schema.FieldDefinition,
	recordIDs []string,
) (map[string]any, error) {
	numeric := isNumericDataType(field.DataType)
	total := relationSQLAggregate{}
	for start := 0; start < len(recordIDs); start += 400 {
		end := min(start+400, len(recordIDs))
		placeholders, params := relationIDParams(recordIDs[start:end])
		column := quoteSQLiteIdentifier(field.PhysicalName)
		numericColumn := "CAST(" + column + " AS REAL)"
		query := fmt.Sprintf(
			"SELECT COUNT(*) AS matched, COUNT(%s) AS value_count, "+
				"SUM(%s) AS value_sum, MIN(%s) AS value_min, MAX(%s) AS value_max "+
				"FROM %s WHERE `id` IN (%s)",
			column, numericColumn, numericColumn, numericColumn,
			quoteSQLiteIdentifier(collectionName), strings.Join(placeholders, ","),
		)
		var matched, count int
		var sum, minimum, maximum sql.NullFloat64
		if queryErr := app.DB().NewQuery(query).WithContext(ctx).Bind(params).Row(
			&matched, &count, &sum, &minimum, &maximum,
		); queryErr != nil {
			return nil, queryErr
		}
		total.Matched += matched
		total.Count += count
		if sum.Valid {
			total.Sum.Valid = true
			total.Sum.Float64 += sum.Float64
		}
		if minimum.Valid && (!total.Min.Valid || minimum.Float64 < total.Min.Float64) {
			total.Min = minimum
		}
		if maximum.Valid && (!total.Max.Valid || maximum.Float64 > total.Max.Float64) {
			total.Max = maximum
		}
	}
	if total.Matched != len(recordIDs) {
		return nil, formulaError(
			"formula.dependency", "relation references missing target records",
			map[string]any{"expected": len(recordIDs), "matched": total.Matched},
		)
	}
	minimum, maximum := any(nil), any(nil)
	if total.Min.Valid {
		minimum = total.Min.Float64
	}
	if total.Max.Valid {
		maximum = total.Max.Float64
	}
	return map[string]any{
		"numeric": numeric,
		"count":   int64(total.Count),
		"sum":     total.Sum.Float64,
		"min":     minimum,
		"max":     maximum,
	}, nil
}

func countRelationRecords(
	ctx context.Context,
	app core.App,
	collectionName string,
	recordIDs []string,
) (int, error) {
	total := 0
	for start := 0; start < len(recordIDs); start += 400 {
		end := min(start+400, len(recordIDs))
		placeholders, params := relationIDParams(recordIDs[start:end])
		query := fmt.Sprintf(
			"SELECT COUNT(*) FROM %s WHERE `id` IN (%s)",
			quoteSQLiteIdentifier(collectionName), strings.Join(placeholders, ","),
		)
		var count int
		if err := app.DB().NewQuery(query).WithContext(ctx).Bind(params).Row(&count); err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func relationIDParams(ids []string) ([]string, dbx.Params) {
	params := make(dbx.Params, len(ids))
	placeholders := make([]string, len(ids))
	for index, id := range ids {
		name := fmt.Sprintf("relation_id_%d", index)
		params[name] = id
		placeholders[index] = "{:" + name + "}"
	}
	return placeholders, params
}

func relationAggregateRequirements(plan *Plan) (map[string][]string, map[string]struct{}) {
	targets := map[string]map[string]struct{}{}
	counts := map[string]struct{}{}
	for _, compiled := range plan.Formulas {
		for _, path := range compiled.RelationAggregatePaths {
			parts := strings.Split(path, ".")
			if len(parts) != 2 {
				continue
			}
			if targets[parts[0]] == nil {
				targets[parts[0]] = map[string]struct{}{}
			}
			targets[parts[0]][parts[1]] = struct{}{}
		}
		for _, name := range compiled.RelationCountNames {
			counts[name] = struct{}{}
		}
	}
	result := make(map[string][]string, len(targets))
	for relationName, names := range targets {
		for name := range names {
			result[relationName] = append(result[relationName], name)
		}
		sort.Strings(result[relationName])
	}
	return result, counts
}

func uniqueSortedStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func quoteSQLiteIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func isNumericDataType(value schema.DataType) bool {
	return value == schema.DataTypeInteger || value == schema.DataTypeFloat ||
		value == schema.DataTypeDecimal
}

func missingRelationTargetError(field schema.FieldDefinition, expected, matched int) error {
	return formulaError(
		"formula.dependency", "relation references missing target records",
		map[string]any{"fieldId": field.FieldID, "expected": expected, "matched": matched},
	)
}

func relationRecordIDs(value any) []string {
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
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() ||
			(reflected.Kind() != reflect.Array &&
				reflected.Kind() != reflect.Slice) {
			return []string{}
		}
		result := make([]string, 0, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			if text, ok := reflected.Index(index).Interface().(string); ok &&
				text != "" {
				result = append(result, text)
			}
		}
		return result
	}
}

func (calculator *Calculator) plan(definition schema.TableDefinition) (*Plan, error) {
	raw, marshalErr := json.Marshal(definition)
	if marshalErr != nil {
		return nil, fmt.Errorf("hash formula definition: %w", marshalErr)
	}
	sum := sha256.Sum256(raw)
	key := definition.TableID + "\x00" + definition.SchemaRevision + "\x00" + hex.EncodeToString(sum[:])
	calculator.mu.RLock()
	plan := calculator.cache[key]
	calculator.mu.RUnlock()
	if plan != nil {
		return plan, nil
	}
	compiled, err := calculator.compiler.CompileTable(definition)
	if err != nil {
		return nil, err
	}
	calculator.mu.Lock()
	if existing := calculator.cache[key]; existing != nil {
		compiled = existing
	} else {
		calculator.cache[key] = compiled
	}
	calculator.mu.Unlock()
	return compiled, nil
}
