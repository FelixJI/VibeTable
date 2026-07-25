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
	for _, field := range definition.Fields {
		value := record.GetRaw(field.PhysicalName)
		if field.DataType == schema.DataTypeSelect ||
			field.DataType == schema.DataTypeMultiSelect {
			value = schema.DecodeSelectValueFromStorage(field, value)
		}
		if field.Kind == schema.FieldKindRelation && field.Relation != nil {
			var err error
			value, err = calculator.resolveRelation(
				ctx, app, field, relationRecordIDs(value),
			)
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
	meta, err := app.FindFirstRecordByFilter(
		"vibetable_tables",
		"table_id={:table}",
		dbx.Params{"table": field.Relation.TargetTableID},
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, formulaError(
				"formula.dependency",
				"relation target table is unavailable",
				map[string]any{"fieldId": field.FieldID},
			)
		}
		return nil, err
	}
	var target schema.TableDefinition
	raw, marshalErr := json.Marshal(meta.GetRaw("definition_json"))
	if marshalErr != nil || json.Unmarshal(raw, &target) != nil {
		return nil, formulaError(
			"formula.dependency",
			"relation target schema is invalid",
			map[string]any{"fieldId": field.FieldID},
		)
	}
	collection, err := app.FindCollectionByNameOrId(
		meta.GetString("collection_id"),
	)
	if err != nil {
		return nil, formulaError(
			"formula.dependency",
			"relation target storage is unavailable",
			map[string]any{"fieldId": field.FieldID},
		)
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
				targetValue = schema.DecodeSelectValueFromStorage(
					targetField,
					targetValue,
				)
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
