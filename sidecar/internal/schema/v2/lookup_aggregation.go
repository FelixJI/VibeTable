package v2

// Lookup aggregation modes. They describe how the complete matched set of a
// Lookup field is reduced before it is materialized; the definitions are
// shared by schema validation, query typing, formula typing and the runtime
// aggregator so no layer re-implements the classification.
const (
	LookupAggregationValues        = "values"
	LookupAggregationDistinct      = "distinct"
	LookupAggregationCountRecords  = "countRecords"
	LookupAggregationCountNonEmpty = "countNonEmpty"
	LookupAggregationCountDistinct = "countDistinct"
	LookupAggregationSum           = "sum"
	LookupAggregationAverage       = "average"
	LookupAggregationMin           = "min"
	LookupAggregationMax           = "max"
)

// ValidLookupAggregation reports whether the text is one of the contract
// enumeration values.
func ValidLookupAggregation(aggregation string) bool {
	switch aggregation {
	case LookupAggregationValues, LookupAggregationDistinct,
		LookupAggregationCountRecords, LookupAggregationCountNonEmpty,
		LookupAggregationCountDistinct, LookupAggregationSum,
		LookupAggregationAverage, LookupAggregationMin, LookupAggregationMax:
		return true
	default:
		return false
	}
}

// ResolvedLookupAggregation returns the effective aggregation mode. An absent
// aggregation keeps the historical values shape, and the legacy
// condition.distinct flag is equivalent to the explicit "distinct" mode.
func ResolvedLookupAggregation(spec LookupSpec) string {
	if spec.Aggregation != "" {
		return spec.Aggregation
	}
	if spec.Condition != nil && spec.Condition.Distinct {
		return LookupAggregationDistinct
	}
	return LookupAggregationValues
}

// LookupAggregationNumeric reports whether the mode materializes a number
// instead of a value collection.
func LookupAggregationNumeric(mode string) bool {
	switch mode {
	case LookupAggregationCountRecords, LookupAggregationCountNonEmpty,
		LookupAggregationCountDistinct, LookupAggregationSum,
		LookupAggregationAverage, LookupAggregationMin, LookupAggregationMax:
		return true
	default:
		return false
	}
}

// LookupAggregationRequiresNumericSource reports whether the mode may only
// read from declared number sources. Counts are total functions over any
// source type; only the numeric summaries constrain the source.
func LookupAggregationRequiresNumericSource(mode string) bool {
	switch mode {
	case LookupAggregationSum, LookupAggregationAverage,
		LookupAggregationMin, LookupAggregationMax:
		return true
	default:
		return false
	}
}

// LookupFieldTargetNumeric reports whether a resolved Lookup target field can
// feed numeric summaries. Number fields qualify, and path lookups may also
// read an already-supported numeric formula result; condition lookups never
// resolve formula targets, so the same check stays closed for them.
func LookupFieldTargetNumeric(field FieldDefinition) bool {
	logicalType := field.LogicalType
	if logicalType == LogicalFormula && field.Formula != nil {
		logicalType = field.Formula.ResultType
	}
	return logicalType == LogicalNumber
}

// ValidateLookupAggregationShape checks direct Go callers as well as decoded
// wire requests. The enumeration and the mutual exclusion with the legacy
// condition.distinct flag are structural; source typing is resolved by the
// cross-table validators.
func ValidateLookupAggregationShape(spec LookupSpec) error {
	if spec.Aggregation == "" {
		return nil
	}
	fail := func(message string) error {
		return &ProductError{
			Code: "lookup.aggregation.invalid", Path: "lookup.aggregation",
			Message: message,
		}
	}
	if !ValidLookupAggregation(spec.Aggregation) {
		return fail("unsupported lookup aggregation")
	}
	if spec.Condition != nil && spec.Condition.Distinct {
		return fail("explicit aggregation cannot combine with condition distinct")
	}
	return nil
}
