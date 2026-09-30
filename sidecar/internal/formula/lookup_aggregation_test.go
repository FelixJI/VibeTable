package formula

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/cel-go/cel"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"

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
		formulaField("fld_double0001", "f_double0001", numberType, "f_total00001 * 2.0"),
	))
	if compileErr != nil {
		t.Fatal(compileErr)
	}
	for _, test := range []struct {
		name  string
		input any
		value float64
	}{
		{"stored fractional", 39.5, 39.5},
		{"wire integer", json.Number("991"), 991},
		{"wire fractional", json.Number("39.5"), 39.5},
		{"integer scalar", int64(991), 991},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, evaluateErr := plan.Evaluate(context.Background(), map[string]any{"f_total00001": test.input}, nil)
			if evaluateErr != nil {
				t.Fatalf("numeric lookup input %#v (%T): error=%#v", test.input, test.input, evaluateErr)
			}
			if result["f_bonus00001"] != test.value+1 || result["f_double0001"] != test.value*2 {
				t.Fatalf("downstream numeric reference = %#v", result)
			}
		})
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

// TestNormalizeInputUnwrapsPocketBaseJSONRawByDeclaredType pins the storage
// seam behind the S38 relation-picker failure: PocketBase hands computed JSON
// columns back as pbtypes.JSONRaw, so normalizeInput must unwrap the storage
// wrapper first and then normalize by the declared field type — a numeric SUM
// lookup cell becomes the declared double, an OnlyInt number stays int64,
// the null sentinel stays nil, and generic dynamic JSON keeps the existing
// json.Number-to-int64 semantics untouched.
func TestNormalizeInputUnwrapsPocketBaseJSONRawByDeclaredType(t *testing.T) {
	limits := DefaultLimits()
	sumLookup := aggregatedLookupField("fld_total00001", "f_total00001", v2.LookupAggregationSum)
	onlyIntNumber := scalarField("fld_qty000001", "f_qty000001", ValueType{
		LogicalType: v2.LogicalNumber, OnlyInt: true,
	})
	jsonPayload := scalarField("fld_payload001", "f_payload001", ValueType{
		LogicalType: v2.LogicalJSON,
	})

	if got, err := normalizeInput(sumLookup, pbtypes.JSONRaw("14"), limits); err != nil || got != float64(14) {
		t.Fatalf("numeric SUM lookup JSONRaw(14) = %#v, %v; want float64(14)", got, err)
	}
	if got, err := normalizeInput(onlyIntNumber, pbtypes.JSONRaw("3"), limits); err != nil || got != int64(3) {
		t.Fatalf("OnlyInt number JSONRaw(3) = %#v, %v; want int64(3)", got, err)
	}
	if got, err := normalizeInput(sumLookup, pbtypes.JSONRaw("   "), limits); err != nil || got != nil {
		t.Fatalf("empty JSONRaw sentinel = %#v, %v; want nil", got, err)
	}
	got, err := normalizeInput(jsonPayload, pbtypes.JSONRaw(`{"a": 3}`), limits)
	if err != nil {
		t.Fatalf("generic JSON error = %#v", err)
	}
	object, ok := got.(map[string]any)
	if !ok || object["a"] != int64(3) {
		t.Fatalf("generic JSON integer = %#v; want map[a:int64(3)]", got)
	}
}
