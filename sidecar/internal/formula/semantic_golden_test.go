package formula

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// These goldens exercise the production compiler and registry, including the
// provider-neutral input/output boundary shared by preview and calculation.
func TestCELV1ScalarSemanticGoldens(t *testing.T) {
	tests := []struct {
		name       string
		inputType  ValueType
		resultType ValueType
		source     string
		input      any
		nullable   bool
		want       any
		wantCode   string
	}{
		{name: "binary64 rounding", inputType: numberType, resultType: numberType, source: "value + 0.2", input: json.Number("0.1"), want: 0.30000000000000004},
		{name: "binary64 integer rounding", inputType: numberType, resultType: numberType, source: "value", input: json.Number("9007199254740993"), want: float64(9007199254740992)},
		{name: "integer maximum safe output", inputType: integerType, resultType: integerType, source: "value", input: json.Number("9007199254740991"), want: int64(9007199254740991)},
		{name: "integer minimum safe output", inputType: integerType, resultType: integerType, source: "value", input: json.Number("-9007199254740991"), want: int64(-9007199254740991)},
		{name: "integer unsafe output", inputType: integerType, resultType: integerType, source: "value + 1", input: int64(9007199254740991), wantCode: "formula.overflow"},
		{name: "integer fractional binding", inputType: integerType, resultType: boolType, source: "value < 0", input: 1.5, wantCode: "formula.type"},
		{name: "integer positive binding overflow", inputType: integerType, resultType: boolType, source: "value < 0", input: math.Ldexp(1, 63), wantCode: "formula.type"},
		{name: "integer negative binding overflow", inputType: integerType, resultType: boolType, source: "value < 0", input: math.Nextafter(-math.Ldexp(1, 63), math.Inf(-1)), wantCode: "formula.type"},
		{name: "integer exact JSON binding maximum", inputType: integerType, resultType: boolType, source: "value > 0", input: json.Number("9223372036854775807"), want: true},
		{name: "integer JSON binding overflow", inputType: integerType, resultType: boolType, source: "value > 0", input: json.Number("9223372036854775808"), wantCode: "formula.type"},
		{name: "positive divide by zero", inputType: numberType, resultType: numberType, source: "value / 0.0", input: 1.0, wantCode: "formula.divide_by_zero"},
		{name: "negative zero divisor", inputType: numberType, resultType: numberType, source: "1.0 / value", input: math.Copysign(0, -1), wantCode: "formula.divide_by_zero"},
		{name: "zero divided by zero", inputType: numberType, resultType: numberType, source: "value / 0.0", input: 0.0, wantCode: "formula.divide_by_zero"},
		{name: "division error before boolean projection", inputType: numberType, resultType: boolType, source: "value / 0.0 > 0.0", input: 1.0, wantCode: "formula.divide_by_zero"},
		{name: "division overflow before boolean projection", inputType: numberType, resultType: boolType, source: "value / 0.5 > 0.0", input: math.MaxFloat64, wantCode: "formula.overflow"},
		{name: "multiplication overflow before boolean projection", inputType: numberType, resultType: boolType, source: "value * 2.0 > 0.0", input: math.MaxFloat64, wantCode: "formula.overflow"},
		{name: "mixed multiplication overflow before boolean projection", inputType: numberType, resultType: boolType, source: "2 * value > 0.0", input: math.MaxFloat64, wantCode: "formula.overflow"},
		{name: "addition overflow before boolean projection", inputType: numberType, resultType: boolType, source: "value + value > 0.0", input: math.MaxFloat64, wantCode: "formula.overflow"},
		{name: "subtraction overflow before boolean projection", inputType: numberType, resultType: boolType, source: "value - -value > 0.0", input: math.MaxFloat64, wantCode: "formula.overflow"},
		{name: "round overflow before boolean projection", inputType: numberType, resultType: boolType, source: "round(value, 1) > 0.0", input: math.MaxFloat64, wantCode: "formula.overflow"},
		{name: "conversion infinity before text projection", inputType: textType, resultType: textType, source: "string(double(value))", input: "Infinity", wantCode: "formula.overflow"},
		{name: "false conjunction does not evaluate overflow", inputType: numberType, resultType: boolType, source: "false && value * value > 0.0", input: math.MaxFloat64, want: false},
		{name: "true disjunction does not evaluate overflow", inputType: numberType, resultType: boolType, source: "true || value * value > 0.0", input: math.MaxFloat64, want: true},
		{name: "integer division truncates toward zero", inputType: integerType, resultType: integerType, source: "value / 2", input: int64(-7), want: int64(-3)},
		{name: "unsigned division preserved", inputType: numberType, resultType: boolType, source: "7u / 2u == 3u", input: 1.0, want: true},
		{name: "unselected division stays lazy", inputType: numberType, resultType: numberType, source: "false ? value / 0.0 : 2.0", input: 1.0, want: 2.0},
		{name: "overflow with nonzero divisor", inputType: numberType, resultType: numberType, source: "(value * value) / 1.0", input: math.MaxFloat64, wantCode: "formula.overflow"},
		{name: "NaN input rejected before comparison", inputType: numberType, resultType: boolType, source: "value == value", input: math.NaN(), wantCode: "formula.overflow"},
		{name: "positive infinity input", inputType: numberType, resultType: boolType, source: "value > 0.0", input: math.Inf(1), wantCode: "formula.overflow"},
		{name: "negative infinity input", inputType: numberType, resultType: boolType, source: "value < 0.0", input: math.Inf(-1), wantCode: "formula.overflow"},
		{name: "float32 NaN binding", inputType: numberType, resultType: boolType, source: "value == value", input: float32(math.NaN()), wantCode: "formula.overflow"},
		{name: "float32 infinity binding", inputType: numberType, resultType: boolType, source: "value > 0.0", input: float32(math.Inf(1)), wantCode: "formula.overflow"},
		{name: "empty text is a value", inputType: textType, resultType: textType, source: `coalesce(value, "fallback")`, input: "", want: ""},
		{name: "null text uses fallback", inputType: textType, resultType: textType, source: `coalesce(value, "fallback")`, input: nil, want: "fallback"},
		{name: "nullable text preserves null", inputType: textType, resultType: textType, source: "value", input: nil, nullable: true, want: nil},
		{name: "required text rejects null", inputType: textType, resultType: textType, source: "value", input: nil, wantCode: "formula.null"},
		{name: "Unicode length counts code points", inputType: textType, resultType: integerType, source: "length(value)", input: "中😀e\u0301", want: int64(4)},
		{name: "Unicode casing and whitespace", inputType: textType, resultType: textType, source: "upper(trim(value))", input: "\u3000é中😀\u00a0", want: "É中😀"},
		{name: "Unicode has no implicit normalization", inputType: textType, resultType: boolType, source: `value == "é"`, input: "e\u0301", want: false},
		{name: "false condition", inputType: boolType, resultType: textType, source: `value ? "yes" : "no"`, input: false, want: "no"},
		{name: "required null condition", inputType: boolType, resultType: textType, source: `value ? "yes" : "no"`, input: nil, wantCode: "formula.null"},
		{name: "nullable null condition", inputType: boolType, resultType: textType, source: `value ? "yes" : "no"`, input: nil, nullable: true, want: nil},
		{name: "explicit null condition fallback", inputType: boolType, resultType: textType, source: `coalesce(value, false) ? "yes" : "no"`, input: nil, want: "no"},
		{name: "UTC previous year", inputType: dateTimeType, resultType: dateTimeType, source: "value", input: "2024-01-01T00:30:00+01:00", want: "2023-12-31T23:30:00Z"},
		{name: "UTC equal instants", inputType: dateTimeType, resultType: boolType, source: `value == timestamp("2024-02-29T23:30:00Z")`, input: "2024-03-01T00:30:00+01:00", want: true},
		{name: "UTC nanosecond ordering", inputType: dateTimeType, resultType: boolType, source: `value < timestamp("2024-03-01T00:00:00Z")`, input: "2024-02-29T23:59:59.999999999Z", want: true},
		{name: "UTC leap day formatting", inputType: dateTimeType, resultType: textType, source: `formatDate(value, "yyyy-MM-dd")`, input: "2024-03-01T00:30:00+01:00", want: "2024-02-29"},
		{name: "millisecond format truncates fraction", inputType: dateTimeType, resultType: textType, source: `formatDate(value, "yyyy-MM-dd'T'HH:mm:ss.SSS'Z'")`, input: "2024-01-01T00:00:00.123456789Z", want: "2024-01-01T00:00:00.123Z"},
		{name: "date uses UTC timestamp boundary", inputType: ValueType{LogicalType: v2.LogicalDate}, resultType: ValueType{LogicalType: v2.LogicalDate}, source: "value", input: "2024-03-01T00:00:00+08:00", want: "2024-02-29T16:00:00Z"},
		{name: "timezone required", inputType: dateTimeType, resultType: dateTimeType, source: "value", input: "2024-01-01T00:00:00", wantCode: "formula.timezone"},
		{name: "invalid calendar date", inputType: dateTimeType, resultType: dateTimeType, source: "value", input: "2023-02-29T00:00:00Z", wantCode: "formula.timezone"},
		{name: "unsupported format runtime error", inputType: dateTimeType, resultType: textType, source: `formatDate(value, "relative")`, input: "2024-01-01T00:00:00Z", wantCode: "formula.runtime"},
	}
	compiler := NewCompiler(DefaultLimits())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := formulaField("result_id", "result", test.resultType, test.source)
			output.Value.Required = !test.nullable
			plan, err := compiler.CompileExecutionTable(formulaTable(
				scalarField("value_id", "value", test.inputType), output,
			))
			if err != nil {
				t.Fatalf("compile %q: %v", test.source, err)
			}
			values, err := plan.Evaluate(context.Background(), map[string]any{"value": test.input}, nil)
			if test.wantCode != "" {
				assertFormulaCode(t, err, test.wantCode)
				return
			}
			if err != nil || !reflect.DeepEqual(values, map[string]any{"result": test.want}) {
				t.Fatalf("%s with %#v: got %#v, error %v; want %#v", test.source, test.input, values, err, test.want)
			}
		})
	}
}

func TestCELV1FiniteCallPreservesCostLimit(t *testing.T) {
	limits := DefaultLimits()
	limits.Cost = 1 // The identifier and addition each cost one unit.
	plan, err := NewCompiler(limits).CompileExecutionTable(formulaTable(
		scalarField("value_id", "value", numberType),
		formulaField("result_id", "result", numberType, "value + 1.0"),
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = plan.Evaluate(context.Background(), map[string]any{"value": 1.0}, nil)
	assertFormulaCode(t, err, "formula.resource_limit")
}

func TestCELV1RelationAggregateSemanticGoldens(t *testing.T) {
	// Empty one, empty many and the calculator's aggregate carrier must agree.
	tests := []struct {
		name        string
		cardinality string
		input       any
		want        map[string]any
	}{
		{name: "empty one", cardinality: "one", input: nil},
		{name: "empty many", cardinality: "many", input: []any{}},
		{name: "empty precomputed many", cardinality: "many", input: map[string]any{
			precomputedRelationMarker: true, precomputedRelationCountKey: int64(0),
			precomputedRelationFields: map[string]any{"amount": map[string]any{
				"numeric": true, "count": int64(0), "sum": 0.0, "min": nil, "max": nil,
			}},
		}},
		{name: "null and missing target values", cardinality: "many", input: []any{
			map[string]any{"amount": nil}, map[string]any{},
		}, want: map[string]any{"sum": 0.0, "count": int64(2), "count_values": int64(0), "avg": nil, "min": nil, "max": nil}},
		{name: "zeros count as values", cardinality: "many", input: []any{
			map[string]any{"amount": nil}, map[string]any{"amount": 0.0}, map[string]any{"amount": 6.0},
		}, want: map[string]any{"sum": 6.0, "count": int64(3), "count_values": int64(2), "avg": 3.0, "min": 0.0, "max": 6.0}},
	}
	compiler := NewCompiler(DefaultLimits())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			relation := relationField("items_id", "items", "line_items")
			relation.Relation.Cardinality = test.cardinality
			fields := []v2.FieldDefinition{relation,
				formulaField("sum_id", "sum", numberType, `relationSum(items, "amount")`),
				formulaField("count_id", "count", integerType, "relationCount(items)"),
				formulaField("count_values_id", "count_values", integerType, `relationCountValues(items, "amount")`),
			}
			for _, operation := range []struct{ name, function string }{{"avg", "relationAverage"}, {"min", "relationMin"}, {"max", "relationMax"}} {
				field := formulaField(operation.name+"_id", operation.name, numberType, operation.function+`(items, "amount")`)
				field.Value.Required = false
				fields = append(fields, field)
			}
			plan, err := compiler.CompileExecutionTable(formulaTable(fields...))
			if err != nil {
				t.Fatal(err)
			}
			values, err := plan.Evaluate(context.Background(), map[string]any{"items": test.input}, nil)
			want := test.want
			if want == nil {
				want = map[string]any{"sum": 0.0, "count": int64(0), "count_values": int64(0), "avg": nil, "min": nil, "max": nil}
			}
			if err != nil || !reflect.DeepEqual(values, want) {
				t.Fatalf("got %#v, error %v; want %#v", values, err, want)
			}
		})
	}
}

func TestCELV1CompileErrorSemanticGoldens(t *testing.T) {
	tests := []struct {
		name   string
		source string
		code   string
	}{
		{"syntax", "1.0 +", "formula.syntax"},
		{"type", `1.0 + "text"`, "formula.type"},
		{"reference", "missing + 1.0", "formula.dependency"},
		{"self cycle", "result + 1.0", "formula.cycle"},
		{"resource", "[1, 2].map(x, x + 1).size()", "formula.resource_limit"},
	}
	compiler := NewCompiler(DefaultLimits())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := compiler.CompileExecutionTable(formulaTable(
				formulaField("result_id", "result", numberType, test.source),
			))
			assertFormulaCode(t, err, test.code)
		})
	}
}
