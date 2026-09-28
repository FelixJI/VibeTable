package formula

import (
	"context"
	"testing"

	"github.com/google/cel-go/cel"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func aggregatedLookupField(id, name, aggregation string) v2.FieldDefinition {
	field := scalarField(id, name, ValueType{LogicalType: v2.LogicalJSON})
	field.LogicalType = v2.LogicalLookup
	field.Storage = v2.StorageSpec{Kind: v2.StorageComputed}
	field.Lookup = &v2.LookupSpec{
		TargetFieldID: "fld_amount0001", Aggregation: aggregation,
	}
	return field
}

// TestNumericLookupAggregationsAreNumberFormulaInputs pins the formula typing
// seam: counts and numeric summaries declare double variables, so downstream
// formulas type-check, infer number results and evaluate numerically, while
// collection-shaped lookups stay dynamic.
func TestNumericLookupAggregationsAreNumberFormulaInputs(t *testing.T) {
	t.Parallel()
	total := aggregatedLookupField("fld_total00001", "f_total00001", v2.LookupAggregationSum)
	double, err := celTypeForField(total)
	if err != nil || double.String() != "double" {
		t.Fatalf("sum lookup CEL type = %v, %v", double, err)
	}
	count := aggregatedLookupField("fld_count0001", "f_count0001", v2.LookupAggregationCountRecords)
	if typed, err := celTypeForField(count); err != nil || typed.String() != "double" {
		t.Fatalf("count lookup CEL type = %v, %v", typed, err)
	}
	distinct := aggregatedLookupField("fld_distinct001", "f_distinct001", v2.LookupAggregationDistinct)
	if typed, err := celTypeForField(distinct); err != nil || typed != cel.DynType {
		t.Fatalf("distinct lookup CEL type = %v, %v", typed, err)
	}
	values := aggregatedLookupField("fld_values0001", "f_values0001", v2.LookupAggregationValues)
	if typed, err := celTypeForField(values); err != nil || typed != cel.DynType {
		t.Fatalf("values lookup CEL type = %v, %v", typed, err)
	}

	plan, compileErr := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
		total,
		formulaField("fld_bonus00001", "f_bonus00001", numberType, "f_total00001 + 1.0"),
	))
	if compileErr != nil {
		t.Fatal(compileErr)
	}
	result, evaluateErr := plan.Evaluate(context.Background(), map[string]any{"f_total00001": 39.5}, nil)
	if evaluateErr != nil {
		t.Fatal(evaluateErr)
	}
	if result["f_bonus00001"] != 40.5 {
		t.Fatalf("downstream numeric reference = %#v", result)
	}

	// Inference must classify an aggregated lookup reference as a number.
	inferred, inferErr := NewCompiler(DefaultLimits()).InferExecutionSource(formulaTable(total), "f_total00001 * 2.0")
	if inferErr != nil {
		t.Fatal(inferErr)
	}
	if inferred.LogicalType != v2.LogicalNumber || inferred.OnlyInt {
		t.Fatalf("inferred aggregate reference type = %#v", inferred)
	}
}
