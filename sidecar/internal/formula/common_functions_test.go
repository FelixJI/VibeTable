package formula

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestCommonFormulaIFSkipsUnselectedError(t *testing.T) {
	plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
		formulaField("result_id", "result", integerType, "IF(false, 1 / 0, 7)"),
	))
	if err != nil {
		t.Fatal(err)
	}
	values, err := plan.Evaluate(context.Background(), map[string]any{}, nil)
	if err != nil || values["result"] != int64(7) {
		t.Fatalf("got %v, %v; want 7", values, err)
	}
}

// TestCommonFormulaSemanticGoldens covers every frozen function with normal,
// arity, type and boundary cases through the production compiler and plan
// evaluation boundary.
func TestCommonFormulaSemanticGoldens(t *testing.T) {
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
		// IF: laziness, branch typing and mixed numeric unification.
		{name: "IF false skips integer division error", inputType: integerType, resultType: integerType, source: "IF(false, 1 / 0, 7)", input: 1, want: int64(7)},
		{name: "IF true propagates division error", inputType: numberType, resultType: numberType, source: "IF(true, 1.0 / 0.0, 7.0)", input: 1.0, wantCode: "formula.divide_by_zero"},
		{name: "IF false skips division error", inputType: numberType, resultType: numberType, source: "IF(false, 1.0 / 0.0, 7.0)", input: 1.0, want: 7.0},
		{name: "IF selects then branch", inputType: boolType, resultType: textType, source: `IF(value, "yes", "no")`, input: true, want: "yes"},
		{name: "IF selects else branch", inputType: boolType, resultType: textType, source: `IF(value, "yes", "no")`, input: false, want: "no"},
		{name: "IF mixed int then widens to double", inputType: boolType, resultType: numberType, source: "IF(value, 1, 2.5)", input: true, want: 1.0},
		{name: "IF mixed double else stays double", inputType: boolType, resultType: numberType, source: "IF(value, 1, 2.5)", input: false, want: 2.5},
		{name: "IF mixed double then widens else", inputType: boolType, resultType: numberType, source: "IF(value, 2.5, 3)", input: false, want: 3.0},
		{name: "IF timestamp branches", inputType: boolType, resultType: dateTimeType, source: `IF(value, timestamp("2024-01-01T00:00:00Z"), timestamp("2025-01-01T00:00:00Z"))`, input: false, want: "2025-01-01T00:00:00Z"},
		{name: "IF list branches", inputType: boolType, resultType: listType, source: `IF(value, ["a"], ["b"])`, input: true, want: []any{"a"}},
		{name: "IF rejects mixed text and number", inputType: boolType, resultType: textType, source: `IF(value, "a", 1)`, input: true, wantCode: "formula.type"},
		{name: "IF rejects int result in double field", inputType: boolType, resultType: numberType, source: "IF(value, 1, 2)", input: true, wantCode: "formula.type"},
		{name: "IF requires three arguments", inputType: boolType, resultType: textType, source: `IF(value, "a")`, input: true, wantCode: "formula.type"},
		{name: "IF rejects four arguments", inputType: boolType, resultType: textType, source: `IF(value, "a", "b", "c")`, input: true, wantCode: "formula.type"},
		{name: "IF nested calls", inputType: numberType, resultType: textType, source: `IF(value > 0.0, IF(value > 1.0, "big", "small"), "neg")`, input: 0.5, want: "small"},

		// IFS: lazy pairs, null on no match, arity and typing boundaries.
		{name: "IFS returns first match", inputType: numberType, resultType: numberType, source: "IFS(value > 2.0, 1.5, value > 1.0, 2.5)", input: 3.0, want: 1.5},
		{name: "IFS returns later match", inputType: numberType, resultType: numberType, source: "IFS(value > 2.0, 1.5, value > 1.0, 2.5)", input: 1.5, want: 2.5},
		{name: "IFS skips unevaluated value error", inputType: numberType, resultType: numberType, source: "IFS(false, 1.0 / 0.0, true, 4.5)", input: 1.0, want: 4.5},
		{name: "IFS propagates matched value error", inputType: numberType, resultType: numberType, source: "IFS(true, 1.0 / 0.0, true, 4.5)", input: 1.0, wantCode: "formula.divide_by_zero"},
		{name: "IFS without match is null", inputType: numberType, resultType: numberType, source: "IFS(false, 1.5, false, 2.5)", input: 1.0, nullable: true, want: nil},
		{name: "IFS null requires nullable field", inputType: numberType, resultType: numberType, source: "IFS(false, 1.5, false, 2.5)", input: 1.0, wantCode: "formula.null"},
		{name: "IFS mixed numeric unifies to double", inputType: numberType, resultType: numberType, source: "IFS(false, 1, true, 2.5)", input: 1.0, want: 2.5},
		{name: "IFS widens selected int to double", inputType: numberType, resultType: numberType, source: "IFS(true, 1, false, 2.5)", input: 1.0, want: 1.0},
		{name: "IFS accepts four pairs", inputType: numberType, resultType: numberType, source: "IFS(false, 1.0, false, 2.0, false, 3.0, true, 4.0)", input: 1.0, want: 4.0},
		{name: "IFS rejects five pairs", inputType: numberType, resultType: numberType, source: "IFS(false, 1.0, false, 2.0, false, 3.0, false, 4.0, true, 5.0)", input: 1.0, wantCode: "formula.type"},
		{name: "IFS rejects odd argument count", inputType: numberType, resultType: numberType, source: "IFS(true, 1.0, false)", input: 1.0, wantCode: "formula.type"},
		{name: "IFS rejects mixed branch types", inputType: numberType, resultType: textType, source: `IFS(true, "a", false, 1)`, input: 1.0, wantCode: "formula.type"},

		// AND/OR/NOT: strict left-to-right short-circuit.
		{name: "AND false skips later error", inputType: numberType, resultType: boolType, source: "AND(false, 1.0 / 0.0 > 0.0)", input: 1.0, want: false},
		{name: "AND true evaluates later error", inputType: numberType, resultType: boolType, source: "AND(true, 1.0 / 0.0 > 0.0)", input: 1.0, wantCode: "formula.divide_by_zero"},
		{name: "AND propagates error immediately", inputType: numberType, resultType: boolType, source: "AND(1.0 / 0.0 > 0.0, false)", input: 1.0, wantCode: "formula.divide_by_zero"},
		{name: "AND all true", inputType: numberType, resultType: boolType, source: "AND(true, value > 0.0, true)", input: 1.0, want: true},
		{name: "AND accepts eight arguments", inputType: numberType, resultType: boolType, source: "AND(true, true, true, true, true, true, true, value > 0.0)", input: 1.0, want: true},
		{name: "AND rejects nine arguments", resultType: boolType, source: "AND(true, true, true, true, true, true, true, true, true)", input: 1.0, wantCode: "formula.type"},
		{name: "AND rejects single argument", resultType: boolType, source: "AND(true)", input: 1.0, wantCode: "formula.type"},
		{name: "OR true skips later error", inputType: numberType, resultType: boolType, source: "OR(true, 1.0 / 0.0 > 0.0)", input: 1.0, want: true},
		{name: "OR false evaluates later error", inputType: numberType, resultType: boolType, source: "OR(false, 1.0 / 0.0 > 0.0)", input: 1.0, wantCode: "formula.divide_by_zero"},
		{name: "OR all false", inputType: numberType, resultType: boolType, source: "OR(false, value > 0.0, false)", input: -1.0, want: false},
		{name: "NOT negates true", inputType: boolType, resultType: boolType, source: "NOT(value)", input: true, want: false},
		{name: "NOT rejects non-boolean", resultType: boolType, source: "NOT(1)", input: 1.0, wantCode: "formula.type"},

		// ISBLANK: only null and empty string.
		{name: "ISBLANK empty string", inputType: textType, resultType: boolType, source: "ISBLANK(value)", input: "", want: true},
		{name: "ISBLANK null", inputType: textType, resultType: boolType, source: "ISBLANK(value)", input: nil, want: true},
		{name: "ISBLANK whitespace is a value", inputType: textType, resultType: boolType, source: "ISBLANK(value)", input: " ", want: false},
		{name: "ISBLANK zero is a value", inputType: numberType, resultType: boolType, source: "ISBLANK(value)", input: 0.0, want: false},
		{name: "ISBLANK false is a value", inputType: boolType, resultType: boolType, source: "ISBLANK(value)", input: false, want: false},

		// IFERROR/ISERROR: absorb only divide-by-zero and numeric overflow.
		{name: "IFERROR absorbs division", inputType: numberType, resultType: numberType, source: "IFERROR(1.0 / 0.0, 9.5)", input: 1.0, want: 9.5},
		{name: "IFERROR absorbs integer division", inputType: integerType, resultType: integerType, source: "IFERROR(1 / 0, 9)", input: 1, want: int64(9)},
		{name: "IFERROR absorbs overflow", inputType: numberType, resultType: numberType, source: "IFERROR(value * value, 0.0)", input: math.MaxFloat64, want: 0.0},
		{name: "IFERROR passes value through", inputType: numberType, resultType: numberType, source: "IFERROR(value, 9.5)", input: 2.0, want: 2.0},
		{name: "IFERROR widens int value in double unify", inputType: numberType, resultType: numberType, source: "IFERROR(1.0 / 0.0, 3)", input: 1.0, want: 3.0},
		{name: "IFERROR widens selected int value", inputType: numberType, resultType: numberType, source: "IFERROR(2, 3.5)", input: 1.0, want: 2.0},
		{name: "IFERROR does not absorb runtime type error", inputType: jsonType, resultType: numberType, source: "IFERROR(value + 1.0, 9.5)", input: "text", wantCode: "formula.runtime"},
		{name: "IFERROR rejects mixed branch types", resultType: textType, source: `IFERROR(1, "a")`, input: 1.0, wantCode: "formula.type"},
		{name: "ISERROR detects division", resultType: boolType, source: "ISERROR(1.0 / 0.0)", input: 1.0, want: true},
		{name: "ISERROR detects integer division", resultType: boolType, source: "ISERROR(1 / 0)", input: 1.0, want: true},
		{name: "ISERROR false for plain value", inputType: numberType, resultType: boolType, source: "ISERROR(value)", input: 2.0, want: false},
		{name: "ISERROR false for null", inputType: textType, resultType: boolType, source: "ISERROR(value)", input: nil, want: false},
		{name: "ISERROR propagates other errors", inputType: jsonType, resultType: boolType, source: "ISERROR(value + 1.0)", input: "text", wantCode: "formula.runtime"},

		// Text functions.
		{name: "CONCATENATE joins text", resultType: textType, source: `CONCATENATE("Vibe", "Table")`, input: 1.0, want: "VibeTable"},
		{name: "CONCATENATE skips null", inputType: textType, resultType: textType, source: `CONCATENATE(value, "!")`, input: nil, want: "!"},
		{name: "CONCATENATE stringifies numbers", resultType: textType, source: `CONCATENATE("n=", 1)`, input: 1.0, want: "n=1"},
		{name: "CONCATENATE accepts eight arguments", resultType: textType, source: `CONCATENATE("a", "b", "c", "d", "e", "f", "g", "h")`, input: 1.0, want: "abcdefgh"},
		{name: "CONCATENATE rejects nine arguments", resultType: textType, source: `CONCATENATE("a", "b", "c", "d", "e", "f", "g", "h", "i")`, input: 1.0, wantCode: "formula.type"},
		{name: "CONCATENATE rejects single argument", resultType: textType, source: `CONCATENATE("a")`, input: 1.0, wantCode: "formula.type"},
		{name: "LEN counts runes", inputType: textType, resultType: integerType, source: `LEN(value)`, input: "中😀e\u0301", want: int64(4)},
		{name: "LEN rejects numbers", inputType: numberType, resultType: integerType, source: "LEN(value)", input: 1.0, wantCode: "formula.type"},
		{name: "LEFT prefix", inputType: textType, resultType: textType, source: "LEFT(value, 2)", input: "中😀e", want: "中😀"},
		{name: "LEFT beyond length", inputType: textType, resultType: textType, source: "LEFT(value, 5)", input: "中😀e", want: "中😀e"},
		{name: "LEFT negative count fails", inputType: textType, resultType: textType, source: "LEFT(value, -1)", input: "abc", wantCode: "formula.runtime"},
		{name: "RIGHT suffix", inputType: textType, resultType: textType, source: "RIGHT(value, 2)", input: "中😀e", want: "😀e"},
		{name: "RIGHT beyond length", inputType: textType, resultType: textType, source: "RIGHT(value, 9)", input: "abc", want: "abc"},
		{name: "RIGHT negative count fails", inputType: textType, resultType: textType, source: "RIGHT(value, -1)", input: "abc", wantCode: "formula.runtime"},
		{name: "MID middle", inputType: textType, resultType: textType, source: "MID(value, 2, 2)", input: "中😀e", want: "😀e"},
		{name: "MID start beyond length is empty", inputType: textType, resultType: textType, source: "MID(value, 5, 2)", input: "中😀e", want: ""},
		{name: "MID count clamps to remainder", inputType: textType, resultType: textType, source: "MID(value, 2, 99)", input: "中😀e", want: "😀e"},
		{name: "MID huge start and count stay safe", inputType: textType, resultType: textType, source: "MID(value, 9223372036854775807, 9223372036854775807)", input: "abc", want: ""},
		{name: "MID start below one fails", inputType: textType, resultType: textType, source: "MID(value, 0, 1)", input: "abc", wantCode: "formula.runtime"},
		{name: "MID negative count fails", inputType: textType, resultType: textType, source: "MID(value, 1, -1)", input: "abc", wantCode: "formula.runtime"},
		{name: "TRIM removes unicode whitespace", inputType: textType, resultType: textType, source: "TRIM(value)", input: "\u3000é中😀\u00a0", want: "é中😀"},
		{name: "UPPER unicode", inputType: textType, resultType: textType, source: "UPPER(value)", input: "é中😀", want: "É中😀"},
		{name: "LOWER unicode", inputType: textType, resultType: textType, source: "LOWER(value)", input: "É中😀", want: "é中😀"},
		{name: "TRIM rejects numbers", inputType: numberType, resultType: textType, source: "TRIM(value)", input: 1.0, wantCode: "formula.type"},

		// Math functions.
		{name: "ABS integer", resultType: integerType, source: "ABS(-3)", input: 1.0, want: int64(3)},
		{name: "ABS double", resultType: numberType, source: "ABS(-3.5)", input: 1.0, want: 3.5},
		{name: "ABS minimum integer overflows", inputType: integerType, resultType: integerType, source: "ABS(value)", input: math.MinInt64, wantCode: "formula.overflow"},
		{name: "ROUND two digits", resultType: numberType, source: "ROUND(3.14159, 2)", input: 1.0, want: 3.14},
		{name: "ROUND integer exact", resultType: integerType, source: "ROUND(125, -1)", input: 1.0, want: int64(130)},
		{name: "ROUND integer negative half away from zero", resultType: integerType, source: "ROUND(-125, -1)", input: 1.0, want: int64(-130)},
		{name: "ROUND positive digits keep integer", resultType: integerType, source: "ROUND(125, 2)", input: 1.0, want: int64(125)},
		{name: "ROUND upper bound digits", resultType: numberType, source: "ROUND(1.5, 15)", input: 1.0, want: 1.5},
		{name: "ROUND lower bound digits", resultType: numberType, source: "ROUND(1500.0, -15)", input: 1.0, want: 0.0},
		{name: "ROUND rejects digit above fifteen", inputType: numberType, resultType: numberType, source: "ROUND(value, 16)", input: 1.5, wantCode: "formula.runtime"},
		{name: "ROUND rejects digit below minus fifteen", inputType: numberType, resultType: numberType, source: "ROUND(value, -16)", input: 1.5, wantCode: "formula.runtime"},
		{name: "MIN int int", resultType: integerType, source: "MIN(3, 7)", input: 1.0, want: int64(3)},
		{name: "MIN int double unifies", resultType: numberType, source: "MIN(3, 7.5)", input: 1.0, want: 3.0},
		{name: "MAX int double unifies", resultType: numberType, source: "MAX(3, 7.5)", input: 1.0, want: 7.5},
		{name: "MIN dispatches dyn numbers", inputType: jsonType, resultType: numberType, source: "MIN(value, 5)", input: 2.5, want: 2.5},
		{name: "MIN rejects dyn text", inputType: jsonType, resultType: numberType, source: `MIN(value, 5)`, input: "x", wantCode: "formula.runtime"},
		{name: "MIN requires two arguments", resultType: integerType, source: "MIN(3)", input: 1.0, wantCode: "formula.type"},
		{name: "MIN rejects three arguments", resultType: integerType, source: "MIN(3, 4, 5)", input: 1.0, wantCode: "formula.type"},
	}
	compiler := NewCompiler(DefaultLimits())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := formulaField("result_id", "result", test.resultType, test.source)
			output.Value.Required = !test.nullable
			inputs := map[string]any{}
			if test.inputType.LogicalType != "" {
				inputs["value"] = test.input
			}
			plan, err := compiler.CompileExecutionTable(formulaTable(
				scalarField("value_id", "value", test.inputType), output,
			))
			if err != nil {
				if test.wantCode == "" {
					t.Fatalf("compile %q: %v", test.source, err)
				}
				assertFormulaCode(t, err, test.wantCode)
				return
			}
			if test.wantCode != "" && strings.HasPrefix(test.wantCode, "formula.type") {
				t.Fatalf("compile %q unexpectedly succeeded", test.source)
			}
			values, err := plan.Evaluate(context.Background(), inputs, nil)
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

// TestCommonFormulaCompileErrorGoldens covers failures that must be rejected
// before any evaluation, including the closed function whitelist.
func TestCommonFormulaCompileErrorGoldens(t *testing.T) {
	tests := []struct {
		name   string
		source string
		code   string
	}{
		{"unknown function", "UNKNOWN(1.0)", "formula.dependency"},
		{"member call rejected", `"a".LEFT(1)`, "formula.type"},
		{"lowercase if rejected", "if(true, 1.0, 2.0)", "formula.syntax"},
		{"ISBLANK requires an argument", "ISBLANK()", "formula.type"},
		{"MID requires three arguments", `MID("a", 1)`, "formula.type"},
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

// TestCommonFormulaInferExecutionSourceGoldens proves the inference path
// agrees with the declared overload results.
func TestCommonFormulaInferExecutionSourceGoldens(t *testing.T) {
	tests := []struct {
		source string
		want   ValueType
	}{
		{`IF(1 > 0, 2, 3)`, integerType},
		{`IF(1 > 0, 2, 3.5)`, numberType},
		{`IF(1 > 0, "a", "b")`, textType},
		{`IF(1 > 0, true, false)`, boolType},
		{`IFERROR(1 / 0, 2.5)`, numberType},
		{`IFERROR(1 / 0, 2)`, integerType},
		{`IFS(1 > 0, 2, 2 > 3, 4)`, integerType},
		{`IFS(1 > 0, 2, 2 > 3, 4.5)`, numberType},
		{`AND(true, false)`, boolType},
		{`OR(false, true)`, boolType},
		{`NOT(true)`, boolType},
		{`ISBLANK("")`, boolType},
		{`ISERROR(1.0 / 0.0)`, boolType},
		{`CONCATENATE("a", "b")`, textType},
		{`LEN("a")`, integerType},
		{`LEFT("abc", 1)`, textType},
		{`RIGHT("abc", 1)`, textType},
		{`MID("abc", 1, 1)`, textType},
		{`TRIM("a")`, textType},
		{`UPPER("a")`, textType},
		{`LOWER("a")`, textType},
		{`ABS(-3)`, integerType},
		{`ABS(-3.5)`, numberType},
		{`ROUND(3.14, 1)`, numberType},
		{`ROUND(3, 1)`, integerType},
		{`MIN(3, 7)`, integerType},
		{`MAX(3, 7.5)`, numberType},
	}
	compiler := NewCompiler(DefaultLimits())
	for _, test := range tests {
		result, err := compiler.InferExecutionSource(formulaTable(), test.source)
		if err != nil {
			t.Fatalf("infer %q: %v", test.source, err)
		}
		if result != test.want {
			t.Fatalf("infer %q = %#v; want %#v", test.source, result, test.want)
		}
	}
}

// TestCommonFormulaResourceAndReferenceNotAbsorbed proves resource and
// reference failures survive IFERROR/ISERROR and that lazy branches keep the
// cost limit active.
func TestCommonFormulaResourceAndReferencesNotAbsorbed(t *testing.T) {
	limits := DefaultLimits()
	limits.Cost = 1
	plan, err := NewCompiler(limits).CompileExecutionTable(formulaTable(
		scalarField("value_id", "value", numberType),
		formulaField("result_id", "result", numberType, "IF(true, value + 1.0, 2.0)"),
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = plan.Evaluate(context.Background(), map[string]any{"value": 1.0}, nil)
	assertFormulaCode(t, err, "formula.resource_limit")

	costPlan, err := NewCompiler(limits).CompileExecutionTable(formulaTable(
		scalarField("value_id", "value", numberType),
		formulaField("result_id", "result", numberType, "IFERROR(value + value, 0.0)"),
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = costPlan.Evaluate(context.Background(), map[string]any{"value": 1.0}, nil)
	assertFormulaCode(t, err, "formula.resource_limit")

	relation := relationField("items_id", "items", "line_items")
	referencePlan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
		relation,
		formulaField("result_id", "result", textType, `IFERROR(items.name, "fallback")`),
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = referencePlan.Evaluate(context.Background(), map[string]any{"items": nil}, nil)
	assertFormulaCode(t, err, "formula.runtime")
}

// TestCommonFormulaInputBoundaryStaysBeforeEvaluation proves normalizeInput
// protections are not bypassed by the new functions.
func TestCommonFormulaInputBoundaryStaysBeforeEvaluation(t *testing.T) {
	plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
		scalarField("value_id", "value", numberType),
		formulaField("result_id", "result", numberType, "IFERROR(value, 1.0)"),
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = plan.Evaluate(context.Background(), map[string]any{"value": math.Inf(1)}, nil)
	assertFormulaCode(t, err, "formula.overflow")
}

// TestCommonFormulaLowercaseSemanticsUnchanged keeps the frozen lowercase
// behavior next to the new uppercase spellings.
func TestCommonFormulaLowercaseSemanticsUnchanged(t *testing.T) {
	tests := []struct {
		source     string
		resultType ValueType
		want       any
	}{
		{"min(8, 3)", integerType, int64(3)},
		{"max(1.5, 2.25)", numberType, 2.25},
		{`upper("é中😀")`, textType, "É中😀"},
		{`concat("a", 1, null)`, textType, "a1"},
		{"abs(-2)", integerType, int64(2)},
		{"round(2.5, 0)", numberType, 3.0},
	}
	for _, test := range tests {
		plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
			formulaField("result_id", "result", test.resultType, test.source),
		))
		if err != nil {
			t.Fatalf("compile %q: %v", test.source, err)
		}
		values, err := plan.Evaluate(context.Background(), map[string]any{}, nil)
		if err != nil || !reflect.DeepEqual(values["result"], test.want) {
			t.Fatalf("%s: got %#v, error %v; want %#v", test.source, values, err, test.want)
		}
	}
}

// TestCommonFormulaAuthorDocumentRoundTrip proves display sources using the
// new functions canonicalize to executable CEL, restore to the same display
// text, and never rewrite string literals.
func TestCommonFormulaAuthorDocumentRoundTrip(t *testing.T) {
	name := scalarField("name_id", "f_name", textType)
	name.DisplayName = "名称"
	price := scalarField("price_id", "f_price", numberType)
	price.DisplayName = "单价"
	definition := V2Table{TableID: "orders", Fields: []v2.FieldDefinition{name, price}}
	display := `IF({单价} > 100.0, LEFT({名称}, 3), CONCATENATE({名称}, "!"))`
	want := `IF(f_price > 100.0, LEFT(f_name, 3), CONCATENATE(f_name, "!"))`
	authored, err := AuthorV2Document(definition, nil, workbench.FormulaAuthorDocument{DisplaySource: display, DocumentRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if authored.CanonicalSource != want {
		t.Fatalf("canonical = %q; want %q", authored.CanonicalSource, want)
	}
	if _, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
		name, price, formulaField("result_id", "f_result", textType, authored.CanonicalSource),
	)); err != nil {
		t.Fatalf("canonical source does not compile: %v", err)
	}
	restored, err := RestoreV2AuthorDocument(definition, nil, authored.CanonicalSource, 2)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Document.DisplaySource != display {
		t.Fatalf("restored display = %q; want %q", restored.Document.DisplaySource, display)
	}
	if len(restored.Document.Tokens) != 3 {
		t.Fatalf("restored tokens = %#v", restored.Document.Tokens)
	}
	roundTrip, err := AuthorV2Document(definition, nil, restored.Document)
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip.CanonicalSource != want {
		t.Fatalf("round trip = %q; want %q", roundTrip.CanonicalSource, want)
	}
}

// TestCommonFormulaAuthorKeepsStringLiterals proves brace-like and text
// content inside string literals is never treated as a reference.
func TestCommonFormulaAuthorKeepsStringLiterals(t *testing.T) {
	name := scalarField("name_id", "f_name", textType)
	name.DisplayName = "名称"
	definition := V2Table{TableID: "orders", Fields: []v2.FieldDefinition{name}}
	display := `CONCATENATE("{名称}", LEFT("AB", 1))`
	want := `CONCATENATE("{名称}", LEFT("AB", 1))`
	authored, err := AuthorV2Document(definition, nil, workbench.FormulaAuthorDocument{DisplaySource: display, DocumentRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if authored.CanonicalSource != want {
		t.Fatalf("canonical = %q; want %q", authored.CanonicalSource, want)
	}
	if len(authored.Document.Tokens) != 0 {
		t.Fatalf("string literals must not bind tokens: %#v", authored.Document.Tokens)
	}
	restored, err := RestoreV2AuthorDocument(definition, nil, authored.CanonicalSource, 2)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Document.DisplaySource != want {
		t.Fatalf("restored display = %q; want %q", restored.Document.DisplaySource, want)
	}
}

// TestCommonFormulaAuthorNumericMinMax proves numeric MIN/MAX display calls
// stay numeric functions while the relation aggregate shorthand is intact.
func TestCommonFormulaAuthorNumericMinMax(t *testing.T) {
	definition, targets := authorFixture()
	numeric, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: "MIN(2.0, 3.0) + MAX({明细}.{金额}, 100.0)", DocumentRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantNumeric := "MIN(2.0, 3.0) + MAX(f_lines.f_amount, 100.0)"
	if numeric.CanonicalSource != wantNumeric {
		t.Fatalf("numeric canonical = %q; want %q", numeric.CanonicalSource, wantNumeric)
	}
	shipping := scalarField("shipping_id", "f_shipping", numberType)
	if _, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
		definition.Fields[0], shipping,
		formulaField("result_id", "f_result", numberType, numeric.CanonicalSource),
	)); err != nil {
		t.Fatalf("numeric canonical does not compile: %v", err)
	}
	aggregate, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: "MIN({明细}.{金额})", DocumentRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.CanonicalSource != `relationMin(f_lines, "f_amount")` {
		t.Fatalf("aggregate canonical = %q", aggregate.CanonicalSource)
	}
}

// TestFunctionCatalogMatchesCompilerEnvironment proves the catalog is the
// closed set of uppercase whitelist functions and every example type-checks
// through the production environment.
func TestFunctionCatalogMatchesCompilerEnvironment(t *testing.T) {
	catalog := FunctionCatalog()
	if len(catalog) == 0 {
		t.Fatal("catalog is empty")
	}
	names := make(map[string]bool, len(catalog))
	for _, entry := range catalog {
		if names[entry.Name] {
			t.Fatalf("duplicate catalog entry %q", entry.Name)
		}
		names[entry.Name] = true
		if entry.Category == "" || entry.Signature == "" || entry.Description == "" || entry.Example == "" {
			t.Fatalf("catalog entry %q has an empty field: %#v", entry.Name, entry)
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"name", "category", "signature", "description", "example"} {
			if _, ok := fields[key]; !ok {
				t.Fatalf("catalog entry %q lacks key %q", entry.Name, key)
			}
		}
		if _, err := NewCompiler(DefaultLimits()).InferExecutionSource(formulaTable(), entry.Example); err != nil {
			t.Fatalf("catalog example for %q does not type-check: %v", entry.Name, err)
		}
	}
	uppercase := make(map[string]bool)
	for name := range allowedFunctions {
		hasLetter, allUpper := false, true
		for _, r := range name {
			if r >= 'a' && r <= 'z' {
				allUpper = false
			}
			if r >= 'A' && r <= 'Z' {
				hasLetter = true
			}
		}
		if hasLetter && allUpper {
			uppercase[name] = true
		}
	}
	if !reflect.DeepEqual(names, uppercase) {
		t.Fatalf("catalog names %#v differ from uppercase whitelist %#v", names, uppercase)
	}
}

func TestCommonFormulaRejectsWrongArityAndTypes(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	for _, source := range []string{
		"IF(true, 1)", "IF(1, 2, 3)", "IFS(true)", "IFS(1, 2)",
		"AND()", "AND(true, 1)", "OR(false)", "OR(false, 1)",
		"NOT()", "NOT(1)", "ISBLANK()", "ISERROR()", "IFERROR(1)", `IFERROR(1, "x")`,
		"CONCATENATE()", "LEN()", "LEN(1)", `LEFT("a")`, "LEFT(1, 1)",
		`RIGHT("a")`, "RIGHT(1, 1)", `MID("a", 1)`, "MID(1, 1, 1)",
		"TRIM()", "TRIM(1)", "UPPER()", "UPPER(1)", "LOWER()", "LOWER(1)",
		"ABS()", `ABS("x")`, "ROUND(1)", `ROUND("x", 1)`,
		"MIN(1)", `MIN("x", 1)`, "MAX(1)", `MAX("x", 1)`,
	} {
		t.Run(source, func(t *testing.T) {
			_, err := compiler.InferExecutionSource(formulaTable(), source)
			assertFormulaCode(t, err, "formula.type")
		})
	}
}

func TestErrorFunctionsDoNotAbsorbUserTextInDiagnostics(t *testing.T) {
	// CEL includes the supplied text in invalid timestamp errors. A substring
	// match would mistake this conversion error for a numeric overflow.
	plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
		formulaField("result_id", "result", boolType, `ISERROR(timestamp("overflow"))`),
	))
	if err != nil {
		t.Fatal(err)
	}
	if values, err := plan.Evaluate(context.Background(), map[string]any{}, nil); err == nil {
		t.Fatalf("conversion error was swallowed: %#v", values)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := plan.Evaluate(ctx, map[string]any{}, nil); err == nil {
		t.Fatal("cancelled evaluation succeeded")
	}
}
