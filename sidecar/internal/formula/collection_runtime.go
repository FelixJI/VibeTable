package formula

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"

	"github.com/vibetable/vibetable/sidecar/internal/queryfilter"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

type CollectionReadRequest struct {
	TableID string
	Fields  []v2.FieldDefinition
	Filters []queryfilter.FilterExpression
}

// The reader streams bounded pages; yield accounts for each row before it is retained.
type CollectionSourceReader func(context.Context, CollectionReadRequest, func(map[string]any) error) error
type collectionSourceKey struct{}
type collectionEvaluationKey struct{}
type collectionReadCacheKey struct{}
type collectionReadEntry struct {
	values []any
	bytes  int
}
type collectionReadCache struct {
	remaining int
	entries   map[string]collectionReadEntry
}

func WithCollectionSourceReader(ctx context.Context, reader CollectionSourceReader) context.Context {
	return context.WithValue(ctx, collectionSourceKey{}, reader)
}

type collectionEvaluation struct {
	remaining uint64
	limits    Limits
	bytes     collectionBudget
}

func (evaluation *collectionEvaluation) charge(ctx context.Context, cost uint64) *Error {
	if ctx.Err() != nil || cost > evaluation.remaining {
		return formulaError("formula.resource_limit", "collection evaluation exceeded its resource limit", map[string]any{"reason": evaluationResourceReason(ctx.Err()), "cost": cost, "remainingCost": evaluation.remaining})
	}
	evaluation.remaining -= cost
	return nil
}

func (formula *CompiledFormula) collectionActivation(ctx context.Context, activation map[string]any) (context.Context, map[string]any, **Error) {
	var failure *Error
	evaluation, ok := ctx.Value(collectionEvaluationKey{}).(*collectionEvaluation)
	if !ok && len(formula.collections) == 0 {
		return ctx, activation, &failure
	}
	if !ok {
		evaluation = &collectionEvaluation{remaining: formula.limits.Cost, limits: formula.limits, bytes: collectionBudget{remaining: formula.limits.CollectionBytes}}
		ctx = context.WithValue(ctx, collectionEvaluationKey{}, evaluation)
	}
	if len(formula.collections) == 0 {
		return ctx, activation, &failure
	}
	// CEL's native lazy bindings preserve IF/AND/IFERROR evaluation order and
	// memoize each range once for this row, without executing dead branches.
	local := make(map[string]any, len(activation)+len(formula.collections))
	for key, value := range activation {
		local[key] = value
	}
	for _, binding := range formula.collections {
		local[binding.name] = func() any {
			value, err := binding.node.evaluate(ctx, activation, evaluation)
			if err != nil {
				switch err.Code {
				case "formula.divide_by_zero":
					return types.NewErr("divide by zero")
				case "formula.overflow":
					return types.NewErr("numeric overflow")
				default:
					failure = err
				}
				return types.NewErr("%s", err.Error())
			}
			return value
		}
	}
	return ctx, local, &failure
}

func (node *collectionNode) evaluate(ctx context.Context, row map[string]any, evaluation *collectionEvaluation) (any, *Error) {
	if err := evaluation.charge(ctx, 1); err != nil {
		return nil, err
	}
	if node.kind == "value" || node.kind == "lookup" {
		value := row[node.localName]
		if node.program != nil {
			var err *Error
			value, err = node.program.evaluate(ctx, row)
			if err != nil {
				return nil, err
			}
		}
		if node.valueType.ElementType == "" {
			return value, nil
		}
		values, failure := collectionValues(value)
		if failure != nil {
			return nil, failure
		}
		// Wire values round-trip numbers as int64/double and timestamps as
		// RFC3339 text; canonicalize every element back into the one typed
		// list shape before any downstream operation observes it.
		return node.collectionElements(ctx, values, evaluation)
	}
	if node.kind == "local" {
		value := row[node.localName]
		if value == nil {
			return []any{}, nil
		}
		values := []any{}
		if record, ok := value.(map[string]any); ok {
			values = []any{record}
		} else {
			var failure *Error
			values, failure = collectionValues(value)
			if failure != nil {
				return nil, failure
			}
		}
		for index, element := range values {
			if err := evaluation.charge(ctx, 1); err != nil {
				return nil, err
			}
			normalized, failure := normalizeDynamicInputBudget(element, evaluation.limits, 0, node.localName, &evaluation.bytes)
			if failure != nil {
				return nil, failure
			}
			values[index] = normalized
		}
		return values, nil
	}
	if node.kind == "table" {
		reader, ok := ctx.Value(collectionSourceKey{}).(CollectionSourceReader)
		if !ok || reader == nil {
			return nil, formulaError("formula.dependency", "table range has no authoritative source reader", nil)
		}
		filters, failure := node.queryFilters(ctx, row)
		if failure != nil {
			return nil, failure
		}
		request := CollectionReadRequest{TableID: node.tableID, Fields: node.sourceFields, Filters: filters}
		cache, _ := ctx.Value(collectionReadCacheKey{}).(*collectionReadCache)
		// The enclosing Plan.Evaluate owns a read-only snapshot. Never reuse
		// rows across a mutation, transaction, or a later evaluation.
		encoded, encodeErr := json.Marshal(request)
		if encodeErr != nil {
			return nil, formulaError("formula.type", "collection source request cannot be encoded", nil)
		}
		key := string(encoded)
		if cache != nil {
			if entry, ok := cache.entries[key]; ok {
				if entry.bytes > evaluation.bytes.remaining {
					return nil, formulaError("formula.resource_limit", "collection exceeds byte budget", nil)
				}
				if failure := evaluation.charge(ctx, uint64(len(entry.values))); failure != nil {
					return nil, failure
				}
				evaluation.bytes.remaining -= entry.bytes
				return entry.values, nil
			}
		}
		initialBytes := evaluation.bytes.remaining
		values := make([]any, 0)
		err := reader(ctx, request, func(record map[string]any) error {
			if failure := evaluation.charge(ctx, 1); failure != nil {
				return failure
			}
			normalized, failure := normalizeDynamicInputBudget(record, evaluation.limits, 0, node.tableID, &evaluation.bytes)
			if failure != nil {
				return failure
			}
			values = append(values, normalized)
			return nil
		})
		if err != nil {
			var typed *Error
			// The caller's expired context wins over an already mapped
			// dependency failure; retain typed diagnostics while it is live.
			if errors.As(err, &typed) && ctx.Err() == nil {
				return nil, typed
			}
			// Cancellation and bounded-reader resource failures are runtime
			// conditions, not missing dependencies; they must stay hard
			// failures that IFERROR cannot absorb.
			if ctx.Err() != nil || errors.Is(err, context.Canceled) ||
				errors.Is(err, context.DeadlineExceeded) || collectionResourceFailure(err) {
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				return nil, formulaError("formula.resource_limit", "table range read was cancelled or exceeded its resource limit", map[string]any{"reason": evaluationResourceReason(err)})
			}
			return nil, formulaError("formula.dependency", "table range could not be read", map[string]any{"reason": err.Error()})
		}
		used := initialBytes - evaluation.bytes.remaining
		if cache != nil && used+len(key) <= cache.remaining {
			cache.entries[key] = collectionReadEntry{values: values, bytes: used}
			cache.remaining -= used + len(key)
		}
		return values, nil
	}
	input, err := node.child.evaluate(ctx, row, evaluation)
	if err != nil {
		return nil, err
	}
	values, err := collectionValues(input)
	if err != nil {
		return nil, err
	}
	switch node.kind {
	case "FILTER":
		filtered := make([]any, 0)
		for _, value := range values {
			if err := evaluation.charge(ctx, 1); err != nil {
				return nil, err
			}
			activation := make(map[string]any, len(row)+len(node.inputs))
			for name, outer := range row {
				activation[name] = outer
			}
			for name, field := range node.inputs {
				raw := value
				if node.records != nil {
					record, ok := value.(map[string]any)
					if !ok {
						return nil, formulaError("formula.type", "record range contains a non-record", nil)
					}
					raw = record[field.Identity.PhysicalName]
				}
				normalized, err := normalizeInput(field, raw, node.predicate.limits)
				if err != nil {
					return nil, err
				}
				activation[name] = normalized
			}
			matched, err := node.predicate.evaluate(ctx, activation)
			if err != nil {
				return nil, err
			}
			// Type-assert instead of comparing interfaces: a non-boolean
			// predicate result never matches and never panics on
			// non-comparable dynamic values.
			if selected, ok := matched.(bool); ok && selected {
				if node.records == nil {
					canonical, failure := node.collectionElement(value)
					if failure != nil {
						return nil, failure
					}
					if !evaluation.bytes.consume(canonical) {
						return nil, formulaError("formula.resource_limit", "collection exceeds byte budget", nil)
					}
					value = canonical
				}
				filtered = append(filtered, value)
			}
		}
		return filtered, nil
	case "PROJECT":
		projected := make([]any, 0, len(values))
		for _, value := range values {
			if err := evaluation.charge(ctx, 1); err != nil {
				return nil, err
			}
			record, ok := value.(map[string]any)
			if !ok {
				return nil, formulaError("formula.type", "projection source contains a non-record", nil)
			}
			normalized, err := normalizeInput(node.projection, record[node.projection.Identity.PhysicalName], evaluation.limits)
			if err != nil {
				return nil, err
			}
			canonical, failure := node.collectionElement(normalized)
			if failure != nil {
				return nil, failure
			}
			if !evaluation.bytes.consume(canonical) {
				return nil, formulaError("formula.resource_limit", "collection exceeds byte budget", nil)
			}
			projected = append(projected, canonical)
		}
		return projected, nil
	}
	separator := ""
	if node.separator != nil {
		value, err := node.separator.evaluate(ctx, row)
		if err != nil {
			return nil, err
		}
		var ok bool
		separator, ok = value.(string)
		if !ok {
			return nil, formulaError("formula.type", "ARRAYJOIN separator must be text", nil)
		}
	}
	return reduceCollection(ctx, values, node.kind, separator, evaluation)
}

// collectionElement validates and canonicalizes one scalar element against
// the node's declared element type: numbers unify to double, wire RFC3339
// timestamps become time.Time and nil stays the single blank value. Nested
// lists, records and mixed-type elements are type failures.
func (node *collectionNode) collectionElement(value any) (any, *Error) {
	return collectionCanonicalElement(value, node.valueType.ElementType)
}

func (node *collectionNode) collectionElements(ctx context.Context, values []any, evaluation *collectionEvaluation) ([]any, *Error) {
	result := make([]any, len(values))
	for index, value := range values {
		if err := evaluation.charge(ctx, 1); err != nil {
			return nil, err
		}
		canonical, failure := node.collectionElement(value)
		if failure != nil {
			return nil, failure
		}
		if !evaluation.bytes.consume(canonical) {
			return nil, formulaError("formula.resource_limit", "collection exceeds byte budget", nil)
		}
		result[index] = canonical
	}
	return result, nil
}

func collectionCanonicalElement(value any, elementType v2.LogicalType) (any, *Error) {
	if celValue, ok := value.(ref.Val); ok {
		if celValue == types.NullValue {
			return nil, nil
		}
		value = celValue.Value()
	}
	if value == nil {
		return nil, nil
	}
	switch elementType {
	case v2.LogicalNumber:
		number, ok := collectionNumber(value)
		if !ok {
			return nil, formulaError("formula.type", "collection number element has an unsupported value", nil)
		}
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, formulaError("formula.overflow", "collection number element must be finite", nil)
		}
		return number, nil
	case v2.LogicalBool:
		if typed, ok := value.(bool); ok {
			return typed, nil
		}
	case v2.LogicalAutoNumber, v2.LogicalText:
		if typed, ok := value.(string); ok {
			return typed, nil
		}
	case v2.LogicalDate, v2.LogicalDateTime, v2.LogicalAutoDate:
		switch typed := value.(type) {
		case time.Time:
			return typed.UTC(), nil
		case pbtypes.DateTime:
			return typed.Time().UTC(), nil
		case string:
			parsed, err := parseFormulaTimestamp(typed, elementType)
			if err != nil {
				return nil, formulaError("formula.timezone", "collection timestamp element must be RFC3339 with an explicit timezone", nil)
			}
			return parsed.UTC(), nil
		}
	default:
		return nil, formulaError("formula.type", "collection element type is unsupported", nil)
	}
	return nil, formulaError("formula.type", "collection element does not match its declared element type", nil)
}

// collectionElementKey builds the typed identity UNIQUE deduplicates on.
// Keys stay plain strings (no hashing), equal numbers share one key across
// their int64/double/-0.0 spellings, and arbitrary objects never produce a
// key. Null and the empty text value stay distinct values.
func collectionElementKey(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "\x00null", true
	case bool:
		if typed {
			return "bool:true", true
		}
		return "bool:false", true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", false
		}
		if typed == 0 {
			typed = 0 // -0.0 and 0.0 are one number
		}
		return "number:" + strconv.FormatFloat(typed, 'g', -1, 64), true
	case int64:
		return "number:" + strconv.FormatInt(typed, 10), true
	case int:
		return "number:" + strconv.Itoa(typed), true
	case json.Number:
		number, err := strconv.ParseFloat(string(typed), 64)
		if err != nil {
			return "", false
		}
		if number == 0 {
			number = 0
		}
		return "number:" + strconv.FormatFloat(number, 'g', -1, 64), true
	case string:
		return "text:" + typed, true
	case time.Time:
		return "datetime:" + typed.UTC().Format(time.RFC3339Nano), true
	}
	return "", false
}

// collectionElementText renders one scalar element for ARRAYJOIN. Dates use
// one explicit RFC3339 spelling instead of Go's default time formatting,
// blanks join as empty items, and unknown shapes are rejected rather than
// stringified through fmt.Sprint.
func collectionElementText(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "", true
	case string:
		return typed, true
	case bool:
		if typed {
			return "true", true
		}
		return "false", true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", false
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case int:
		return strconv.Itoa(typed), true
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano), true
	}
	return "", false
}

func collectionResourceFailure(err error) bool {
	var formulaErr *Error
	return errors.As(err, &formulaErr) && formulaErr.Code == "formula.resource_limit"
}

func collectionValues(value any) ([]any, *Error) {
	if value == nil {
		return []any{}, nil
	}
	if values, ok := value.([]any); ok {
		return values, nil
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		return nil, formulaError("formula.type", "collection value is not a list", nil)
	}
	values := make([]any, reflected.Len())
	for index := range values {
		values[index] = reflected.Index(index).Interface()
	}
	return values, nil
}

func reduceCollection(ctx context.Context, values []any, operation, separator string, evaluation *collectionEvaluation) (any, *Error) {
	if err := evaluation.charge(ctx, uint64(len(values))); err != nil {
		return nil, err
	}
	switch operation {
	case "COUNT":
		return int64(len(values)), nil
	case "COUNTA":
		var count int64
		for _, value := range values {
			if err := evaluation.charge(ctx, 0); err != nil {
				return nil, err
			}
			if !collectionBlank(value) {
				count++
			}
		}
		return count, nil
	case "UNIQUE":
		unique := make([]any, 0)
		seen := map[string]bool{}
		for _, value := range values {
			if err := evaluation.charge(ctx, 0); err != nil {
				return nil, err
			}
			key, ok := collectionElementKey(value)
			if !ok {
				return nil, formulaError("formula.type", "collection element is not a unique-able scalar", nil)
			}
			if seen[key] {
				continue
			}
			if !evaluation.bytes.consume(key) || !evaluation.bytes.consume(value) {
				return nil, formulaError("formula.resource_limit", "UNIQUE exceeds byte budget", nil)
			}
			seen[key] = true
			unique = append(unique, value)
		}
		return unique, nil
	case "ARRAYJOIN":
		var result strings.Builder
		for index, value := range values {
			if err := evaluation.charge(ctx, 0); err != nil {
				return nil, err
			}
			text, ok := collectionElementText(value)
			if !ok {
				return nil, formulaError("formula.type", "collection element cannot be joined as text", nil)
			}
			addition := len(text)
			if index > 0 {
				addition += len(separator)
			}
			if addition > evaluation.bytes.remaining {
				return nil, formulaError("formula.resource_limit", "ARRAYJOIN exceeds byte budget", nil)
			}
			evaluation.bytes.remaining -= addition
			if index > 0 {
				result.WriteString(separator)
			}
			result.WriteString(text)
		}
		return result.String(), nil
	}
	total, minimum, maximum := 0.0, math.Inf(1), math.Inf(-1)
	count := 0
	for _, value := range values {
		if err := evaluation.charge(ctx, 0); err != nil {
			return nil, err
		}
		if collectionBlank(value) {
			continue
		}
		number, ok := collectionNumber(value)
		if !ok {
			return nil, formulaError("formula.type", "numeric collection contains a non-number", nil)
		}
		if math.IsInf(number, 0) || math.IsNaN(number) {
			return nil, formulaError("formula.overflow", "collection number overflow", nil)
		}
		if operation == "SUM" || operation == "AVERAGE" {
			total += number
			if math.IsInf(total, 0) || math.IsNaN(total) {
				return nil, formulaError("formula.overflow", "collection sum overflow", nil)
			}
		}
		minimum, maximum = math.Min(minimum, number), math.Max(maximum, number)
		count++
	}
	if operation == "SUM" {
		return total, nil
	}
	if count == 0 {
		return nil, nil
	}
	switch operation {
	case "AVERAGE":
		return total / float64(count), nil
	case "MIN":
		return minimum, nil
	case "MAX":
		return maximum, nil
	}
	return nil, formulaError("formula.dependency", "unknown collection aggregate", nil)
}

func collectionBlank(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && text == ""
}

func collectionNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int64:
		return float64(number), true
	case int:
		return float64(number), true
	case json.Number:
		result, err := strconv.ParseFloat(string(number), 64)
		return result, err == nil
	}
	return 0, false
}
