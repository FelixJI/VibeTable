package formula

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"

	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

type Plan struct {
	Formulas []*CompiledFormula
	byID     map[string]*CompiledFormula
	byName   map[string]*CompiledFormula
	fields   map[string]schema.FieldDefinition
	fieldIDs map[string]struct{}
	limits   Limits
}

func (compiler *Compiler) CompileTable(definition schema.TableDefinition) (*Plan, *Error) {
	formulas := make([]*CompiledFormula, 0)
	for index, field := range definition.Fields {
		if field.Kind != schema.FieldKindFormula {
			continue
		}
		compiled, err := compiler.Compile(definition, field)
		if err != nil {
			if err.Path == nil {
				path := fmt.Sprintf("fields[%d].formula.source", index)
				err.Path = &path
			}
			return nil, err
		}
		formulas = append(formulas, compiled)
	}
	ordered, err := topologicalOrder(formulas)
	if err != nil {
		return nil, err
	}
	plan := &Plan{
		Formulas: ordered,
		byID:     make(map[string]*CompiledFormula, len(ordered)),
		byName:   make(map[string]*CompiledFormula, len(ordered)),
		fields:   make(map[string]schema.FieldDefinition, len(definition.Fields)),
		fieldIDs: make(map[string]struct{}, len(definition.Fields)),
		limits:   compiler.limits,
	}
	for _, field := range definition.Fields {
		plan.fields[field.PhysicalName] = field
		plan.fieldIDs[field.FieldID] = struct{}{}
	}
	for _, formula := range ordered {
		plan.byID[formula.FieldID] = formula
		plan.byName[formula.PhysicalName] = formula
	}
	return plan, nil
}

func topologicalOrder(formulas []*CompiledFormula) ([]*CompiledFormula, *Error) {
	byID := make(map[string]*CompiledFormula, len(formulas))
	for _, formula := range formulas {
		byID[formula.FieldID] = formula
	}
	indegree := make(map[string]int, len(formulas))
	dependents := make(map[string][]string, len(formulas))
	for _, formula := range formulas {
		for _, dependency := range formula.Dependencies {
			if _, isFormula := byID[dependency]; !isFormula {
				continue
			}
			indegree[formula.FieldID]++
			dependents[dependency] = append(dependents[dependency], formula.FieldID)
		}
	}
	queue := make([]string, 0, len(formulas))
	for _, formula := range formulas {
		if indegree[formula.FieldID] == 0 {
			queue = append(queue, formula.FieldID)
		}
	}
	sort.Strings(queue)
	result := make([]*CompiledFormula, 0, len(formulas))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		result = append(result, byID[id])
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
				sort.Strings(queue)
			}
		}
	}
	if len(result) != len(formulas) {
		cycle := make([]string, 0)
		for id, degree := range indegree {
			if degree > 0 {
				cycle = append(cycle, id)
			}
		}
		sort.Strings(cycle)
		return nil, formulaError("formula.cycle", "formula dependency cycle detected", map[string]any{
			"fieldIds": cycle,
		})
	}
	return result, nil
}

func (plan *Plan) Evaluate(
	ctx context.Context,
	row map[string]any,
	changedFieldIDs []string,
) (map[string]any, *Error) {
	activation := make(map[string]any, len(row))
	for key, value := range row {
		field, declared := plan.fields[key]
		if !declared {
			return nil, formulaError("formula.dependency", "formula input references an unknown field", map[string]any{
				"field": key,
			})
		}
		normalized, err := normalizeInput(field, value, plan.limits)
		if err != nil {
			return nil, err
		}
		activation[key] = normalized
	}
	for _, fieldID := range changedFieldIDs {
		if _, exists := plan.fieldIDs[fieldID]; !exists {
			return nil, formulaError("formula.dependency", "changedFieldIds references an unknown field", map[string]any{
				"fieldId": fieldID,
			})
		}
	}
	impacted := plan.impacted(changedFieldIDs)
	outputs := make(map[string]any)
	for _, formula := range plan.Formulas {
		if impacted != nil {
			if _, ok := impacted[formula.FieldID]; !ok {
				continue
			}
		}
		value, err := formula.evaluate(ctx, activation)
		if err != nil {
			return nil, err
		}
		activation[formula.PhysicalName] = value
		outputs[formula.PhysicalName] = value
	}
	return outputs, nil
}

func (plan *Plan) impacted(changedFieldIDs []string) map[string]struct{} {
	// Preview callers use an empty closed-contract array to request a complete
	// evaluation. Treat nil and [] identically; otherwise constant formulas
	// and first-paint previews could never produce a value.
	if len(changedFieldIDs) == 0 {
		return nil
	}
	changed := make(map[string]struct{}, len(changedFieldIDs)+len(plan.Formulas))
	for _, id := range changedFieldIDs {
		changed[id] = struct{}{}
	}
	impacted := make(map[string]struct{})
	for _, formula := range plan.Formulas {
		for _, dependency := range formula.Dependencies {
			if _, ok := changed[dependency]; ok {
				impacted[formula.FieldID] = struct{}{}
				changed[formula.FieldID] = struct{}{}
				break
			}
		}
	}
	return impacted
}

func (formula *CompiledFormula) evaluate(
	parent context.Context,
	activation map[string]any,
) (any, *Error) {
	ctx, cancel := context.WithTimeout(parent, formula.limits.EvalTimeout)
	defer cancel()
	result, _, err := formula.program.ContextEval(ctx, activation)
	if err != nil {
		message := err.Error()
		switch {
		case ctx.Err() != nil || strings.Contains(message, "cost limit exceeded"):
			return nil, formulaError("formula.resource_limit", "formula evaluation exceeded its resource limit", nil)
		case strings.Contains(strings.ToLower(message), "divide by zero"):
			return nil, formulaError("formula.divide_by_zero", "formula divided by zero", nil)
		case strings.Contains(strings.ToLower(message), "overflow"):
			return nil, formulaError("formula.overflow", "formula numeric result overflowed", nil)
		case formula.Nullable && formula.hasNullDependency(activation):
			// A nullable computed field follows nullable propagation semantics:
			// dereferencing a missing optional relation (for example,
			// author.name while author is null) produces null. Resource and
			// arithmetic failures above remain hard failures.
			return nil, nil
		case strings.Contains(strings.ToLower(message), "no such overload") &&
			containsNull(activation):
			return nil, formulaError("formula.null", "formula used null in an unsupported operation", nil)
		default:
			return nil, formulaError("formula.runtime", "formula evaluation failed", map[string]any{
				"reason": message,
			})
		}
	}
	if result == types.NullValue {
		if !formula.Nullable {
			return nil, formulaError("formula.null", "formula produced null for a non-nullable field", nil)
		}
		return nil, nil
	}
	value := result.Value()
	if err := formula.validateRuntimeResult(value); err != nil {
		return nil, err
	}
	normalized, normalizeErr := normalizeOutput(value, formula.limits, 0)
	if normalizeErr != nil {
		return nil, normalizeErr
	}
	return normalized, nil
}

func (formula *CompiledFormula) hasNullDependency(activation map[string]any) bool {
	for _, name := range formula.dependencyNames {
		if value, exists := activation[name]; exists && value == nil {
			return true
		}
	}
	return false
}

func (formula *CompiledFormula) validateRuntimeResult(value any) *Error {
	switch formula.ResultType {
	case schema.DataTypeInteger:
		number, ok := value.(int64)
		if !ok {
			return formulaError("formula.type", "formula returned a non-integer value", nil)
		}
		if number > 1<<53-1 || number < -(1<<53-1) {
			return formulaError("formula.overflow", "formula integer exceeds the JSON safe range", nil)
		}
	case schema.DataTypeFloat, schema.DataTypeDecimal:
		number, ok := value.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
			if ok && math.IsInf(number, 0) && formula.hasDivision {
				return formulaError("formula.divide_by_zero", "formula divided by zero", nil)
			}
			return formulaError("formula.overflow", "formula returned a non-finite number", nil)
		}
	case schema.DataTypeDate, schema.DataTypeDateTime, schema.DataTypeAutoDate:
		if _, ok := value.(time.Time); !ok {
			return formulaError("formula.type", "formula returned a non-timestamp value", nil)
		}
	case schema.DataTypeBoolean:
		if _, ok := value.(bool); !ok {
			return formulaError("formula.type", "formula returned a non-boolean value", nil)
		}
	case schema.DataTypeShortText, schema.DataTypeLongText, schema.DataTypeRichText,
		schema.DataTypeTime, schema.DataTypeEmail, schema.DataTypeURL, schema.DataTypeUUID,
		schema.DataTypeSelect, schema.DataTypeHash:
		if _, ok := value.(string); !ok {
			return formulaError("formula.type", "formula returned a non-string value", nil)
		}
	case schema.DataTypeMultiSelect, schema.DataTypeList:
		valueType := reflect.TypeOf(value)
		if valueType == nil ||
			(valueType.Kind() != reflect.Slice && valueType.Kind() != reflect.Array) {
			return formulaError("formula.type", "formula returned a non-list value", nil)
		}
	}
	return nil
}

func containsNull(values map[string]any) bool {
	for _, value := range values {
		if value == nil {
			return true
		}
	}
	return false
}

func normalizeInput(
	field schema.FieldDefinition,
	value any,
	limits Limits,
) (any, *Error) {
	if value == nil {
		return nil, nil
	}
	dataType := field.DataType
	if field.Kind == schema.FieldKindFormula && field.Formula != nil {
		dataType = field.Formula.ResultType
	}
	switch dataType {
	case schema.DataTypeInteger:
		switch typed := value.(type) {
		case int:
			return int64(typed), nil
		case int64:
			return typed, nil
		case float64:
			if math.Trunc(typed) != typed || typed > math.MaxInt64 || typed < math.MinInt64 {
				return nil, formulaError("formula.type", "integer input is invalid", map[string]any{"fieldId": field.FieldID})
			}
			return int64(typed), nil
		case json.Number:
			number, err := typed.Int64()
			if err != nil {
				return nil, formulaError("formula.type", "integer input is invalid", map[string]any{"fieldId": field.FieldID})
			}
			return number, nil
		}
	case schema.DataTypeFloat, schema.DataTypeDecimal:
		switch typed := value.(type) {
		case int:
			return float64(typed), nil
		case int64:
			return float64(typed), nil
		case float32:
			return float64(typed), nil
		case float64:
			if math.IsNaN(typed) || math.IsInf(typed, 0) {
				return nil, formulaError("formula.overflow", "numeric input must be finite", map[string]any{"fieldId": field.FieldID})
			}
			return typed, nil
		case json.Number:
			number, err := typed.Float64()
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, formulaError("formula.overflow", "numeric input must be finite", map[string]any{"fieldId": field.FieldID})
			}
			return number, nil
		}
	case schema.DataTypeDate, schema.DataTypeDateTime, schema.DataTypeAutoDate:
		switch typed := value.(type) {
		case time.Time:
			return typed.UTC(), nil
		case pbtypes.DateTime:
			return typed.Time().UTC(), nil
		case string:
			parsed, err := time.Parse(time.RFC3339Nano, typed)
			if err != nil {
				return nil, formulaError("formula.timezone", "timestamp input must be RFC3339 with an explicit timezone", map[string]any{
					"fieldId": field.FieldID,
				})
			}
			return parsed.UTC(), nil
		}
	}
	if raw, ok := value.(pbtypes.JSONRaw); ok {
		// PocketBase represents an unset nullable JSON field as an empty
		// JSONRaw byte slice. It is a storage-level null sentinel, not malformed
		// user JSON; preserve the nullable field semantics for formulas and
		// mutation recomputation.
		if len(bytes.TrimSpace(raw)) == 0 {
			return nil, nil
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, formulaError("formula.type", "JSON input is invalid", map[string]any{"fieldId": field.FieldID})
		}
		value = decoded
	}
	return normalizeDynamicInput(value, limits, 0, field.FieldID)
}

func normalizeDynamicInput(value any, limits Limits, depth int, fieldID string) (any, *Error) {
	if depth > limits.RecursionDepth {
		return nil, resourceInputError(fieldID, "formula input exceeds the recursion limit", limits.RecursionDepth)
	}
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), nil
	case pbtypes.DateTime:
		return typed.Time().UTC(), nil
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer, nil
		}
		number, err := typed.Float64()
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, formulaError("formula.type", "JSON number input is invalid", map[string]any{"fieldId": fieldID})
		}
		return number, nil
	case []any:
		if len(typed) > limits.CollectionSize {
			return nil, resourceInputError(fieldID, "formula input collection exceeds the size limit", limits.CollectionSize)
		}
		result := make([]any, len(typed))
		for index, item := range typed {
			normalized, err := normalizeDynamicInput(item, limits, depth+1, fieldID)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	case map[string]any:
		if len(typed) > limits.CollectionSize {
			return nil, resourceInputError(fieldID, "formula input collection exceeds the size limit", limits.CollectionSize)
		}
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeDynamicInput(item, limits, depth+1, fieldID)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	default:
		valueType := reflect.TypeOf(value)
		if valueType != nil && (valueType.Kind() == reflect.Slice || valueType.Kind() == reflect.Array) {
			length := reflect.ValueOf(value).Len()
			if length > limits.CollectionSize {
				return nil, resourceInputError(fieldID, "formula input collection exceeds the size limit", limits.CollectionSize)
			}
			result := make([]any, length)
			for index := 0; index < length; index++ {
				normalized, err := normalizeDynamicInput(
					reflect.ValueOf(value).Index(index).Interface(), limits, depth+1, fieldID,
				)
				if err != nil {
					return nil, err
				}
				result[index] = normalized
			}
			return result, nil
		}
		return typed, nil
	}
}

func normalizeOutput(value any, limits Limits, depth int) (any, *Error) {
	if depth > limits.RecursionDepth {
		return nil, formulaError("formula.resource_limit", "formula output exceeds the recursion limit", nil)
	}
	if wrapped, ok := value.(ref.Val); ok {
		return normalizeOutput(wrapped.Value(), limits, depth)
	}
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano), nil
	case []any:
		if len(typed) > limits.CollectionSize {
			return nil, formulaError("formula.resource_limit", "formula output collection exceeds the size limit", nil)
		}
		result := make([]any, len(typed))
		for index, item := range typed {
			normalized, err := normalizeOutput(item, limits, depth+1)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	case map[string]any:
		if len(typed) > limits.CollectionSize {
			return nil, formulaError("formula.resource_limit", "formula output collection exceeds the size limit", nil)
		}
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeOutput(item, limits, depth+1)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	default:
		valueOf := reflect.ValueOf(value)
		if !valueOf.IsValid() {
			return nil, nil
		}
		switch valueOf.Kind() {
		case reflect.Slice, reflect.Array:
			if valueOf.Len() > limits.CollectionSize {
				return nil, formulaError("formula.resource_limit", "formula output collection exceeds the size limit", nil)
			}
			result := make([]any, valueOf.Len())
			for index := 0; index < valueOf.Len(); index++ {
				normalized, err := normalizeOutput(valueOf.Index(index).Interface(), limits, depth+1)
				if err != nil {
					return nil, err
				}
				result[index] = normalized
			}
			return result, nil
		case reflect.Map:
			if valueOf.Len() > limits.CollectionSize {
				return nil, formulaError("formula.resource_limit", "formula output collection exceeds the size limit", nil)
			}
			result := make(map[string]any, valueOf.Len())
			iterator := valueOf.MapRange()
			for iterator.Next() {
				key, err := normalizeOutput(iterator.Key().Interface(), limits, depth+1)
				if err != nil {
					return nil, err
				}
				stringKey, ok := key.(string)
				if !ok {
					return nil, formulaError("formula.type", "formula JSON object key is not a string", nil)
				}
				normalized, err := normalizeOutput(iterator.Value().Interface(), limits, depth+1)
				if err != nil {
					return nil, err
				}
				result[stringKey] = normalized
			}
			return result, nil
		default:
			return typed, nil
		}
	}
}

func resourceInputError(fieldID, message string, limit int) *Error {
	return formulaError("formula.resource_limit", message, map[string]any{
		"fieldId": fieldID,
		"limit":   limit,
	})
}

func (plan *Plan) Formula(fieldID string) (*CompiledFormula, bool) {
	formula, ok := plan.byID[fieldID]
	return formula, ok
}

func (plan *Plan) String() string {
	names := make([]string, 0, len(plan.Formulas))
	for _, formula := range plan.Formulas {
		names = append(names, formula.PhysicalName)
	}
	return fmt.Sprintf("formula plan [%s]", strings.Join(names, ", "))
}
