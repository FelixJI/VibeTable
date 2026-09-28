package formula

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func collectionRuntimeDefinition(resultType ValueType, source string, sourceFields ...v2.FieldDefinition) schemaexecution.Table {
	field := formulaField("runtime_id", "runtime_value", resultType, source)
	field.Formula.Language = "cel-v2"
	field.Formula.ResultElementType = resultType.ElementType
	definition := formulaTable(scalarField("contract_id", "contract", textType), field)
	definition.FormulaSources = map[string]v2.SchemaSnapshot{"shipments": {
		TableID:        "shipments",
		SchemaRevision: "schema_1",
		Fields: append([]v2.FieldDefinition{
			scalarField("source_contract_id", "contract", textType),
		}, sourceFields...),
	}}
	return definition
}

func collectionRuntimeEvaluate(t *testing.T, resultType ValueType, source string, rows []map[string]any, readerErr error) (map[string]any, *Error) {
	t.Helper()
	plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(collectionRuntimeDefinition(resultType, source,
		scalarField("day_id", "day", dateTimeType),
		scalarField("amount_id", "amount", numberType)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithCollectionSourceReader(context.Background(), collectionRowsReader(rows, readerErr))
	return plan.Evaluate(ctx, map[string]any{"contract": "A"}, nil)
}

func TestCollectionRuntimeNullableScalarListValueRules(t *testing.T) {
	numberList := ValueType{LogicalType: v2.LogicalJSON, ElementType: v2.LogicalNumber}
	textList := ValueType{LogicalType: v2.LogicalJSON, ElementType: v2.LogicalText}
	cases := []struct {
		name       string
		resultType ValueType
		source     string
		want       any
		wantCode   string
	}{
		{
			name:       "unique keeps first occurrence order",
			resultType: numberList,
			source:     "UNIQUE([3.0, 1.0, 3.0, 1.0, 3.0])",
			want:       []any{3.0, 1.0},
		},
		{
			name:       "unique treats equal numbers as one",
			resultType: numberList,
			source:     "UNIQUE([0.0, -0.0, 1.0])",
			want:       []any{0.0, 1.0},
		},
		{
			name:       "unique keeps empty text as a value",
			resultType: textList,
			source:     `UNIQUE(["a", "", "a", "b"])`,
			want:       []any{"a", "", "b"},
		},
		{
			name:       "counta excludes blanks but keeps zero",
			resultType: integerType,
			source:     `COUNTA(["", "a", ""]) + COUNT([0.0])`,
			want:       int64(2),
		},
		{
			name:       "sum of an empty number list is zero",
			resultType: numberType,
			source:     "SUM(FILTER([1.0, 2.0], CurrentValue > 9.0))",
			want:       0.0,
		},
		{
			name:       "min of an empty number list is null",
			resultType: numberType,
			source:     "MIN(FILTER([1.0, 2.0], CurrentValue > 9.0))",
			wantCode:   "formula.null",
		},
		{
			name:       "arrayjoin renders numbers without losing zero",
			resultType: textType,
			source:     `ARRAYJOIN([0.0, 1.5, -2.0], "|")`,
			want:       "0|1.5|-2",
		},
		{
			name:       "arrayjoin keeps blank text items",
			resultType: textType,
			source:     `ARRAYJOIN(["a", "", "b"], ",")`,
			want:       "a,,b",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(
				collectionRuntimeDefinition(testCase.resultType, testCase.source))
			if err != nil {
				t.Fatal(err)
			}
			result, evalErr := plan.Evaluate(context.Background(), map[string]any{"contract": "A"}, nil)
			if testCase.wantCode != "" {
				assertFormulaCode(t, evalErr, testCase.wantCode)
				return
			}
			if evalErr != nil {
				t.Fatal(evalErr)
			}
			if !reflect.DeepEqual(result["runtime_value"], testCase.want) {
				t.Fatalf("value = %#v, want %#v", result["runtime_value"], testCase.want)
			}
		})
	}
}

func TestCollectionRuntimeTableSourceBoundaries(t *testing.T) {
	t.Run("dates join through explicit RFC3339 with null gaps", func(t *testing.T) {
		zone := time.FixedZone("shift", 5*60*60)
		rows := []map[string]any{
			{"contract": "A", "day": time.Date(2026, 1, 2, 3, 0, 0, 0, zone)},
			{"contract": "A", "day": nil},
			{"contract": "A", "day": "2026-01-04T05:00:00+05:00"},
		}
		result, evalErr := collectionRuntimeEvaluate(t, textType,
			`ARRAYJOIN(PROJECT(TABLE("shipments"), CurrentValue.day), ",")`, rows, nil)
		if evalErr != nil {
			t.Fatal(evalErr)
		}
		if result["runtime_value"] != "2026-01-01T22:00:00Z,,2026-01-04T00:00:00Z" {
			t.Fatalf("joined = %#v", result["runtime_value"])
		}
	})
	t.Run("unique unifies equal integer and double amounts", func(t *testing.T) {
		rows := []map[string]any{
			{"contract": "A", "amount": int64(1)},
			{"contract": "A", "amount": 1.0},
			{"contract": "A", "amount": json.Number("1.00")},
		}
		result, evalErr := collectionRuntimeEvaluate(t,
			ValueType{LogicalType: v2.LogicalJSON, ElementType: v2.LogicalNumber},
			`UNIQUE(PROJECT(TABLE("shipments"), CurrentValue.amount))`, rows, nil)
		if evalErr != nil {
			t.Fatal(evalErr)
		}
		if !reflect.DeepEqual(result["runtime_value"], []any{1.0}) {
			t.Fatalf("unique = %#v", result["runtime_value"])
		}
	})
	t.Run("text in a number column is a type failure", func(t *testing.T) {
		rows := []map[string]any{{"contract": "A", "amount": "oops"}}
		_, evalErr := collectionRuntimeEvaluate(t, numberType,
			`SUM(PROJECT(TABLE("shipments"), CurrentValue.amount))`, rows, nil)
		assertFormulaCode(t, evalErr, "formula.type")
	})
	t.Run("cancelled reader is a resource failure not a dependency failure", func(t *testing.T) {
		rows := []map[string]any{{"contract": "A", "amount": 1.0}}
		_, evalErr := collectionRuntimeEvaluate(t, numberType,
			`SUM(PROJECT(TABLE("shipments"), CurrentValue.amount))`, rows, context.Canceled)
		assertFormulaCode(t, evalErr, "formula.resource_limit")
	})
	t.Run("failing reader stays a dependency failure", func(t *testing.T) {
		_, evalErr := collectionRuntimeEvaluate(t, numberType,
			`SUM(PROJECT(TABLE("shipments"), CurrentValue.amount))`, nil, errors.New("source unavailable"))
		assertFormulaCode(t, evalErr, "formula.dependency")
	})
	t.Run("cancellation during the source read is not absorbed by IFERROR", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(collectionRuntimeDefinition(numberType,
			`IFERROR(SUM(PROJECT(TABLE("shipments"), CurrentValue.amount)), 7.0)`,
			scalarField("day_id", "day", dateTimeType),
			scalarField("amount_id", "amount", numberType)))
		if err != nil {
			t.Fatal(err)
		}
		ctx := WithCollectionSourceReader(parent, func(ctx context.Context, request CollectionReadRequest, yield func(map[string]any) error) error {
			cancel()
			return yield(map[string]any{"contract": "A", "amount": 1.0})
		})
		_, evalErr := plan.Evaluate(ctx, map[string]any{"contract": "A"}, nil)
		assertFormulaCode(t, evalErr, "formula.resource_limit")
	})
}

func TestCollectionRuntimeChargesValueListBytesAgainstSharedBudget(t *testing.T) {
	limits := DefaultLimits()
	limits.CollectionBytes = 48
	textList := ValueType{LogicalType: v2.LogicalJSON, ElementType: v2.LogicalText}
	plan, err := NewCompiler(limits).CompileExecutionTable(collectionRuntimeDefinition(textList,
		`UNIQUE(["aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb"])`))
	if err != nil {
		t.Fatal(err)
	}
	_, evalErr := plan.Evaluate(context.Background(), map[string]any{"contract": "A"}, nil)
	assertFormulaCode(t, evalErr, "formula.resource_limit")
}

func TestCollectionExtremaDoNotAccumulateAnUnusedSum(t *testing.T) {
	for _, operation := range []string{"MIN", "MAX"} {
		t.Run(operation, func(t *testing.T) {
			values, err := collectionRuntimeEvaluate(t, numberType, operation+"([1.0e308, 1.0e308])", nil, nil)
			if err != nil || values["runtime_value"] != 1.0e308 {
				t.Fatalf("finite extrema=%v err=%v", values, err)
			}
		})
	}
}
