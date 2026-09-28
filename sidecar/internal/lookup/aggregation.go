package lookup

import (
	"encoding/json"
	"math"

	pbtypes "github.com/pocketbase/pocketbase/tools/types"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// lookupPageWindow returns the [offset, offset+limit) slice of a fully
// collected sequence. It only runs for full-set cursors, where the traversal
// kept every value and paging is a pure presentation concern.
func lookupPageWindow[T any](items []T, offset, limit int) []T {
	if len(items) <= offset {
		return nil
	}
	return items[offset:min(offset+limit, len(items))]
}

// lookupAggregationTraversesFullSet reports whether a mode cannot be computed
// from a page: distinct must observe every duplicate, counts and numeric
// summaries must observe every match. Paging only trims provenance.
func lookupAggregationTraversesFullSet(mode string) bool {
	return mode != v2.LookupAggregationValues
}

// aggregateLookupValues is the single reducer shared by the path and condition
// traversals. values is the complete matched set in stable traversal order.
func aggregateLookupValues(mode string, values []any) (any, error) {
	switch mode {
	case "", v2.LookupAggregationValues:
		return canonicalLookupValue(values), nil
	case v2.LookupAggregationDistinct:
		return distinctLookupValues(values)
	case v2.LookupAggregationCountRecords:
		return float64(len(values)), nil
	case v2.LookupAggregationCountNonEmpty:
		nonEmpty := 0
		for _, value := range values {
			if lookupValueNonEmpty(value) {
				nonEmpty++
			}
		}
		return float64(nonEmpty), nil
	case v2.LookupAggregationCountDistinct:
		seen := map[string]struct{}{}
		for _, value := range values {
			if !lookupValueNonEmpty(value) {
				continue
			}
			key, err := lookupValueIdentity(value)
			if err != nil {
				return nil, err
			}
			seen[key] = struct{}{}
		}
		return float64(len(seen)), nil
	case v2.LookupAggregationSum, v2.LookupAggregationAverage,
		v2.LookupAggregationMin, v2.LookupAggregationMax:
		return aggregateLookupNumeric(mode, values)
	default:
		return nil, lookupError(
			"mutation.lookup.schema_invalid", "lookup aggregation is unsupported",
		)
	}
}

// aggregateLookupNumeric reduces numeric summaries. Null is ignored, not zero;
// an empty set yields sum 0 and null average/min/max. Wrong types and
// non-finite numbers — including an accumulated sum that overflows — are
// explicit errors, never silent zeros or infinities.
func aggregateLookupNumeric(mode string, values []any) (any, error) {
	accumulate := mode == v2.LookupAggregationSum || mode == v2.LookupAggregationAverage
	total, count := 0.0, 0
	minimum, maximum := 0.0, 0.0
	for _, value := range values {
		number, present, err := lookupNumericValue(value)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		if count == 0 || number < minimum {
			minimum = number
		}
		if count == 0 || number > maximum {
			maximum = number
		}
		if accumulate {
			total += number
			if math.IsNaN(total) || math.IsInf(total, 0) {
				return nil, lookupError(
					"lookup.aggregation.value_not_finite",
					"numeric lookup aggregation result is not a finite number",
				)
			}
		}
		count++
	}
	if count == 0 {
		if mode == v2.LookupAggregationSum {
			return 0.0, nil
		}
		return nil, nil
	}
	switch mode {
	case v2.LookupAggregationSum:
		return total, nil
	case v2.LookupAggregationAverage:
		return total / float64(count), nil
	case v2.LookupAggregationMin:
		return minimum, nil
	case v2.LookupAggregationMax:
		return maximum, nil
	default:
		return nil, lookupError(
			"mutation.lookup.schema_invalid", "lookup aggregation is unsupported",
		)
	}
}

// distinctLookupValues keeps the first occurrence of each JSON value identity,
// including null, in stable traversal order.
func distinctLookupValues(values []any) ([]any, error) {
	result := make([]any, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key, err := lookupValueIdentity(value)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

// lookupValueIdentity keys values by their canonical JSON encoding, matching
// the dedupe semantics the conditional Lookup scan has always used.
func lookupValueIdentity(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", lookupError(
			"mutation.lookup.invalid_value", "lookup value could not be encoded",
		)
	}
	return string(encoded), nil
}

// lookupValueNonEmpty reports whether a matched value participates in
// non-empty counting. Only null and the empty string are empty; zero and
// false are real values.
func lookupValueNonEmpty(value any) bool {
	if value == nil {
		return false
	}
	if text, ok := value.(string); ok {
		return text != ""
	}
	return true
}

// lookupNumericValue coerces one matched value for numeric summaries.
// Computed envelopes are unwrapped only at the schema-declared read boundary
// (decodeLookupAggregationValue), so a plain JSON value that merely looks
// like an envelope is never mis-unpacked here; it is what it is — a number,
// or an explicit type error. Null is ignored, text/booleans and non-finite
// numbers are errors.
func lookupNumericValue(value any) (float64, bool, error) {
	if value == nil {
		return 0, false, nil
	}
	if raw, ok := value.(pbtypes.JSONRaw); ok {
		// PocketBase represents an unset nullable JSON field as an empty
		// JSONRaw slice; that is storage null, not a numeric value.
		if len(raw) == 0 {
			return 0, false, nil
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return 0, false, lookupError(
				"lookup.aggregation.value_not_numeric",
				"numeric lookup aggregation source value is not a finite number",
			)
		}
		return lookupNumericValue(decoded)
	}
	switch typed := value.(type) {
	case float64:
		return finiteLookupNumber(float64(typed))
	case float32:
		return finiteLookupNumber(float64(typed))
	case int:
		return float64(typed), true, nil
	case int32:
		return float64(typed), true, nil
	case int64:
		return float64(typed), true, nil
	case json.Number:
		number, err := typed.Float64()
		if err != nil {
			return 0, false, lookupError(
				"lookup.aggregation.value_not_numeric",
				"numeric lookup aggregation source value is not a finite number",
			)
		}
		return finiteLookupNumber(number)
	default:
		return 0, false, lookupError(
			"lookup.aggregation.value_not_numeric",
			"numeric lookup aggregation requires number sources; text, boolean and empty-string values are rejected",
		)
	}
}

func finiteLookupNumber(number float64) (float64, bool, error) {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false, lookupError(
			"lookup.aggregation.value_not_finite",
			"numeric lookup aggregation source value is not a finite number",
		)
	}
	return number, true, nil
}
