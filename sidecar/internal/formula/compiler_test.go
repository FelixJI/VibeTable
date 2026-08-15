package formula

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	pbtypes "github.com/pocketbase/pocketbase/tools/types"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestCompileAndEvaluateFormulaPlan(t *testing.T) {
	definition := formulaTable(
		scalarField("quantity_id", "quantity", numberType),
		scalarField("unit_price_id", "unit_price", numberType),
		scalarField("title_id", "title", textType),
		formulaField("subtotal_id", "subtotal", numberType, "quantity * unit_price"),
		formulaField("label_id", "label", textType, `concat(upper(trim(title)), ":", subtotal)`),
	)
	plan, formulaErr := NewCompiler(DefaultLimits()).CompileExecutionTable(definition)
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	if got := plan.Formulas[0].PhysicalName; got != "subtotal" {
		t.Fatalf("topological order starts with %q", got)
	}
	if got := strings.Join(plan.Formulas[0].Dependencies, ","); got != "quantity_id,unit_price_id" {
		t.Fatalf("dependencies = %q", got)
	}
	result, formulaErr := plan.Evaluate(context.Background(), map[string]any{
		"quantity": 3.0, "unit_price": 4.25, "title": "  item ",
	}, nil)
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	if result["subtotal"] != 12.75 || result["label"] != "ITEM:12.75" {
		t.Fatalf("unexpected result %#v", result)
	}
	if len(plan.Formulas[0].ASTHash) != 64 {
		t.Fatalf("invalid AST hash %q", plan.Formulas[0].ASTHash)
	}
}

func TestSingleRecordFormulaPreviewRecordsP50AndP95(t *testing.T) {
	definition := formulaTable(
		scalarField("quantity_id", "quantity", numberType),
		scalarField("unit_price_id", "unit_price", numberType),
		formulaField(
			"subtotal_id", "subtotal",
			numberType, "quantity * unit_price",
		),
	)
	const (
		samples           = 100
		previewsPerSample = 20
	)
	durations := make([]time.Duration, 0, samples)
	for index := 0; index < samples+5; index++ {
		startedAt := time.Now()
		for preview := 0; preview < previewsPerSample; preview++ {
			plan, formulaErr := NewCompiler(DefaultLimits()).CompileExecutionTable(definition)
			if formulaErr != nil {
				t.Fatal(formulaErr)
			}
			values, formulaErr := plan.Evaluate(context.Background(), map[string]any{
				"quantity": 3.0, "unit_price": 4.25,
			}, nil)
			if formulaErr != nil || values["subtotal"] != 12.75 {
				t.Fatalf("preview values=%#v error=%v", values, formulaErr)
			}
		}
		if index >= 5 {
			durations = append(
				durations,
				time.Since(startedAt)/previewsPerSample,
			)
		}
	}
	sort.Slice(durations, func(left, right int) bool {
		return durations[left] < durations[right]
	})
	p50 := durations[(samples*50/100)-1]
	p95 := durations[(samples*95/100)-1]
	t.Logf("single-record formula preview p50=%s p95=%s", p50, p95)
	if p95 > 100*time.Millisecond {
		t.Fatalf("formula preview p95=%s exceeds 100ms local gate", p95)
	}
}

func TestFormulaTypeDependencyAndCycleErrors(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	t.Run("type", func(t *testing.T) {
		definition := formulaTable(
			scalarField("title_id", "title", textType),
			formulaField("bad_id", "bad", numberType, "title"),
		)
		_, err := compiler.CompileExecutionTable(definition)
		assertFormulaCode(t, err, "formula.type")
	})
	t.Run("unknown identifier", func(t *testing.T) {
		definition := formulaTable(
			formulaField("bad_id", "bad", numberType, "missing + 1.0"),
		)
		_, err := compiler.CompileExecutionTable(definition)
		assertFormulaCode(t, err, "formula.dependency")
	})
	t.Run("invalid operation type", func(t *testing.T) {
		definition := formulaTable(
			formulaField("bad_id", "bad", integerType, "1 + true"),
		)
		_, err := compiler.CompileExecutionTable(definition)
		assertFormulaCode(t, err, "formula.type")
	})
	t.Run("dynamic json indexing", func(t *testing.T) {
		definition := formulaTable(
			scalarField("metadata_id", "metadata", jsonType),
			scalarField("key_id", "key_name", textType),
			formulaField("bad_id", "bad", textType, "metadata[key_name]"),
		)
		_, err := compiler.CompileExecutionTable(definition)
		assertFormulaCode(t, err, "formula.dependency")
	})
	t.Run("function outside whitelist", func(t *testing.T) {
		definition := formulaTable(
			scalarField("title_id", "title", textType),
			formulaField("bad_id", "bad", boolType, `title.matches(".*")`),
		)
		_, err := compiler.CompileExecutionTable(definition)
		assertFormulaCode(t, err, "formula.dependency")
	})
	t.Run("cycle", func(t *testing.T) {
		definition := formulaTable(
			formulaField("a_id", "a_value", numberType, "b_value + 1.0"),
			formulaField("b_id", "b_value", numberType, "a_value + 1.0"),
		)
		_, err := compiler.CompileExecutionTable(definition)
		assertFormulaCode(t, err, "formula.cycle")
	})
}

func TestFormulaRuntimeErrorsAndIncrementalEvaluation(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	t.Run("divide by zero", func(t *testing.T) {
		plan, err := compiler.CompileExecutionTable(formulaTable(
			scalarField("amount_id", "amount", numberType),
			scalarField("divisor_id", "divisor", numberType),
			formulaField("result_id", "result", numberType, "amount / divisor"),
		))
		if err != nil {
			t.Fatal(err)
		}
		_, evalErr := plan.Evaluate(context.Background(), map[string]any{
			"amount": 1.0, "divisor": 0.0,
		}, nil)
		assertFormulaCode(t, evalErr, "formula.divide_by_zero")
	})
	t.Run("null", func(t *testing.T) {
		plan, err := compiler.CompileExecutionTable(formulaTable(
			scalarField("amount_id", "amount", numberType),
			formulaField("result_id", "result", numberType, "amount + 1.0"),
		))
		if err != nil {
			t.Fatal(err)
		}
		_, evalErr := plan.Evaluate(context.Background(), map[string]any{"amount": nil}, nil)
		assertFormulaCode(t, evalErr, "formula.null")
	})
	t.Run("integer overflow", func(t *testing.T) {
		plan, err := compiler.CompileExecutionTable(formulaTable(
			scalarField("number_id", "number", integerType),
			formulaField("result_id", "result", integerType, "number * 2"),
		))
		if err != nil {
			t.Fatal(err)
		}
		_, evalErr := plan.Evaluate(context.Background(), map[string]any{
			"number": int64(1<<62 + 1),
		}, nil)
		assertFormulaCode(t, evalErr, "formula.overflow")
	})
	t.Run("incremental downstream", func(t *testing.T) {
		plan, err := compiler.CompileExecutionTable(formulaTable(
			scalarField("a_id", "a_value", numberType),
			scalarField("unrelated_id", "unrelated", numberType),
			formulaField("b_id", "b_value", numberType, "a_value + 1.0"),
			formulaField("c_id", "c_value", numberType, "b_value * 2.0"),
			formulaField("d_id", "d_value", numberType, "unrelated + 1.0"),
		))
		if err != nil {
			t.Fatal(err)
		}
		result, evalErr := plan.Evaluate(context.Background(), map[string]any{
			"a_value": 2.0, "unrelated": 9.0,
		}, []string{"a_id"})
		if evalErr != nil {
			t.Fatal(evalErr)
		}
		if len(result) != 2 || result["b_value"] != 3.0 || result["c_value"] != 6.0 {
			t.Fatalf("incremental result %#v", result)
		}
	})
	t.Run("PocketBase number normalization", func(t *testing.T) {
		plan, err := compiler.CompileExecutionTable(formulaTable(
			scalarField("count_id", "count", integerType),
			formulaField("next_id", "next", integerType, "count + 1"),
		))
		if err != nil {
			t.Fatal(err)
		}
		result, evalErr := plan.Evaluate(context.Background(), map[string]any{"count": float64(4)}, nil)
		if evalErr != nil {
			t.Fatal(evalErr)
		}
		if result["next"] != int64(5) {
			t.Fatalf("normalized integer result %#v", result)
		}
	})
}

func TestFormulaResourceLimitsAndUTC(t *testing.T) {
	t.Run("source", func(t *testing.T) {
		limits := DefaultLimits()
		limits.SourceBytes = 8
		_, err := NewCompiler(limits).CompileExecutionTable(formulaTable(
			formulaField("value_id", "value", numberType, "1.0 + 2.0"),
		))
		assertFormulaCode(t, err, "formula.resource_limit")
	})
	t.Run("comprehension", func(t *testing.T) {
		_, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
			formulaField("value_id", "value", integerType, "[1, 2].map(x, x + 1).size()"),
		))
		assertFormulaCode(t, err, "formula.resource_limit")
	})
	t.Run("timestamp normalized UTC", func(t *testing.T) {
		plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
			formulaField("value_id", "value", dateTimeType, `timestamp("2026-07-24T08:30:00+08:00")`),
		))
		if err != nil {
			t.Fatal(err)
		}
		result, evalErr := plan.Evaluate(context.Background(), map[string]any{}, nil)
		if evalErr != nil {
			t.Fatal(evalErr)
		}
		if result["value"] != "2026-07-24T00:30:00Z" {
			t.Fatalf("timestamp = %#v", result["value"])
		}
	})
}

func TestFormulaNormalizesWireAndPocketBaseValues(t *testing.T) {
	dateTime, err := pbtypes.ParseDateTime("2026-07-24 08:30:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	plan, formulaErr := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
		scalarField("count_id", "count", integerType),
		scalarField("price_id", "price", numberType),
		scalarField("payload_id", "payload", jsonType),
		scalarField("created_id", "created_at", dateTimeType),
		formulaField("next_id", "next", integerType, "count + 1"),
		formulaField("total_id", "total", numberType, "price * 2.0"),
		formulaField("customer_id", "customer", textType, "payload.customer.name"),
		formulaField("payload_copy_id", "payload_copy", jsonType, "payload"),
		formulaField("created_copy_id", "created_copy", dateTimeType, "created_at"),
	))
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	result, formulaErr := plan.Evaluate(context.Background(), map[string]any{
		"count":      json.Number("4"),
		"price":      json.Number("2.25"),
		"payload":    pbtypes.JSONRaw(`{"customer":{"name":"Ada"}}`),
		"created_at": dateTime,
	}, nil)
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	if result["next"] != int64(5) ||
		result["total"] != 4.5 ||
		result["customer"] != "Ada" ||
		result["created_copy"] != "2026-07-24T08:30:00Z" {
		t.Fatalf("unexpected normalized result %#v", result)
	}
	payloadCopy, ok := result["payload_copy"].(map[string]any)
	if !ok || payloadCopy["customer"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("JSON output is not provider-neutral: %#v", result["payload_copy"])
	}
}

func TestFormulaNormalizesUnsetPocketBaseJSONAsNull(t *testing.T) {
	field := scalarField("payload_id", "payload", jsonType)

	normalized, formulaErr := normalizeInput(field, pbtypes.JSONRaw{}, DefaultLimits())

	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	if normalized != nil {
		t.Fatalf("unset JSON normalized to %#v, want nil", normalized)
	}
}

func TestFormulaInputCollectionsAreRecursivelyByteBounded(t *testing.T) {
	limits := DefaultLimits()
	limits.CollectionBytes = 64
	plan, formulaErr := NewCompiler(limits).CompileExecutionTable(formulaTable(
		scalarField("payload_id", "payload", jsonType),
		formulaField("value_id", "value", textType, "payload.name"),
	))
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	_, formulaErr = plan.Evaluate(context.Background(), map[string]any{
		"payload": pbtypes.JSONRaw(`{"name":"ok","nested":[1,2,3]}`),
	}, nil)
	assertFormulaCode(t, formulaErr, "formula.resource_limit")
}

func TestFormulaCollectionOutputIsProviderNeutral(t *testing.T) {
	plan, formulaErr := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
		formulaField("values_id", "values", listType, "[1, 2]"),
	))
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	result, formulaErr := plan.Evaluate(context.Background(), map[string]any{}, nil)
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	values, ok := result["values"].([]any)
	if !ok || len(values) != 2 || values[0] != int64(1) || values[1] != int64(2) {
		t.Fatalf("list output = %#v (%T)", result["values"], result["values"])
	}
}

func TestFormulaDeterministicMathAndDateFunctions(t *testing.T) {
	plan, formulaErr := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
		formulaField("low_id", "low", integerType, "min(8, 3)"),
		formulaField("high_id", "high", numberType, "max(1.5, 2.25)"),
		formulaField(
			"day_id", "day", textType,
			`formatDate(dateAdd(timestamp("2026-07-24T08:30:00+08:00"), duration("24h")), "yyyy-MM-dd")`,
		),
		formulaField(
			"previous_id", "previous", dateTimeType,
			`dateSubtract(timestamp("2026-07-25T00:30:00Z"), duration("24h"))`,
		),
	))
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	result, formulaErr := plan.Evaluate(context.Background(), map[string]any{}, nil)
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	if result["low"] != int64(3) || result["high"] != 2.25 ||
		result["day"] != "2026-07-25" ||
		result["previous"] != "2026-07-24T00:30:00Z" {
		t.Fatalf("unexpected function result %#v", result)
	}
}

func TestFormulaAggregatesManyRelationWithoutLookupAndInfersType(t *testing.T) {
	items := relationField("items_id", "items", "line_items")
	definition := formulaTable(
		items,
		formulaField(
			"total_id", "total", numberType,
			`relationSum(items, "amount") + 2.0`,
		),
	)
	compiler := NewCompiler(DefaultLimits())
	inferred, formulaErr := compiler.InferExecutionSource(
		definition, `relationSum(items, "amount") + 2.0`,
	)
	if formulaErr != nil || inferred != numberType {
		t.Fatalf("inferred type = %#v, error = %#v", inferred, formulaErr)
	}
	plan, formulaErr := compiler.CompileExecutionTable(definition)
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	if got := strings.Join(plan.Formulas[0].RelationAggregatePaths, ","); got != "items.amount" {
		t.Fatalf("aggregate references = %q", got)
	}
	result, formulaErr := plan.Evaluate(context.Background(), map[string]any{
		"items": []any{
			map[string]any{"amount": 3.5},
			map[string]any{"amount": 4.0},
			map[string]any{"amount": nil},
		},
	}, nil)
	if formulaErr != nil || result["total"] != 9.5 {
		t.Fatalf("aggregate result = %#v, error = %#v", result, formulaErr)
	}
}

func TestFormulaEvaluatesEveryRelationAggregateFromPrecomputedCarrier(t *testing.T) {
	items := relationField("items_id", "items", "line_items")
	plan, formulaErr := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
		items,
		formulaField("sum_id", "sum", numberType, `relationSum(items, "amount")`),
		formulaField("avg_id", "avg", numberType, `relationAverage(items, "amount")`),
		formulaField("min_id", "min", numberType, `relationMin(items, "amount")`),
		formulaField("max_id", "max", numberType, `relationMax(items, "amount")`),
		formulaField("count_id", "count", integerType, "relationCount(items)"),
		formulaField(
			"count_values_id", "count_values", integerType,
			`relationCountValues(items, "amount")`,
		),
	))
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	countFormula, exists := plan.Formula("count_id")
	if !exists {
		t.Fatal("count formula was not compiled")
	}
	if got := strings.Join(countFormula.RelationCountNames, ","); got != "items" {
		t.Fatalf("count relation references = %q", got)
	}
	result, formulaErr := plan.Evaluate(context.Background(), map[string]any{
		"items": map[string]any{
			precomputedRelationMarker:   true,
			precomputedRelationCountKey: int64(3),
			precomputedRelationFields: map[string]any{
				"amount": map[string]any{
					"numeric": true, "count": int64(2), "sum": 7.5,
					"min": 3.5, "max": 4.0,
				},
			},
		},
	}, nil)
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	if result["sum"] != 7.5 || result["avg"] != 3.75 || result["min"] != 3.5 ||
		result["max"] != 4.0 || result["count"] != int64(3) ||
		result["count_values"] != int64(2) {
		t.Fatalf("precomputed aggregate result = %#v", result)
	}
}

func TestFormulaEvaluateRejectsUnknownPreviewInputs(t *testing.T) {
	plan, formulaErr := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
		formulaField("value_id", "value", integerType, "1"),
	))
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	_, formulaErr = plan.Evaluate(
		context.Background(),
		map[string]any{"undeclared": "ignored today"},
		nil,
	)
	assertFormulaCode(t, formulaErr, "formula.dependency")
}

func TestFormulaErrorWireShape(t *testing.T) {
	raw, err := json.Marshal(formulaError("formula.cycle", "cycle", nil))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"contractVersion", "code", "path", "message", "details", "sourceSpan"} {
		if _, ok := value[key]; !ok {
			t.Fatalf("missing required key %q in %s", key, raw)
		}
	}
}

func formulaTable(fields ...v2.FieldDefinition) schemaexecution.Table {
	runtime := make(map[string]schemaexecution.FormulaRuntime)
	for _, field := range fields {
		if field.LogicalType == v2.LogicalFormula {
			runtime[field.Identity.FieldID] = schemaexecution.FormulaRuntime{
				Version: 1,
				Status:  "ready",
			}
		}
	}
	return schemaexecution.Table{
		Snapshot: v2.SchemaSnapshot{
			Contract:       v2.Contract,
			TableID:        "orders_id",
			DisplayName:    "Orders",
			SchemaRevision: "schema_1",
			Fields:         fields,
		},
		PhysicalName:   "orders",
		FormulaRuntime: runtime,
	}
}

var (
	boolType     = ValueType{LogicalType: v2.LogicalBool}
	integerType  = ValueType{LogicalType: v2.LogicalNumber, OnlyInt: true}
	numberType   = ValueType{LogicalType: v2.LogicalNumber}
	textType     = ValueType{LogicalType: v2.LogicalText}
	dateTimeType = ValueType{LogicalType: v2.LogicalDateTime}
	jsonType     = ValueType{LogicalType: v2.LogicalJSON}
	listType     = ValueType{LogicalType: v2.LogicalMultiSelect}
)

func scalarField(id, name string, valueType ValueType) v2.FieldDefinition {
	storage := v2.StorageText
	switch valueType.LogicalType {
	case v2.LogicalBool:
		storage = v2.StorageBool
	case v2.LogicalNumber:
		storage = v2.StorageNumber
	case v2.LogicalDate, v2.LogicalDateTime:
		storage = v2.StorageDate
	case v2.LogicalJSON:
		storage = v2.StorageJSON
	case v2.LogicalMultiSelect:
		storage = v2.StorageSelect
	case v2.LogicalRelation:
		storage = v2.StorageRelation
	}
	return v2.FieldDefinition{
		Contract:    v2.Contract,
		Identity:    v2.FieldIdentity{FieldID: id, PhysicalName: name},
		DisplayName: name,
		LogicalType: valueType.LogicalType,
		Lifecycle:   v2.Lifecycle{State: v2.LifecycleActive},
		Storage: v2.StorageSpec{
			Kind: storage, Options: v2.StorageOptions{OnlyInt: valueType.OnlyInt},
		},
	}
}

func relationField(id, name, targetTableID string) v2.FieldDefinition {
	field := scalarField(id, name, ValueType{LogicalType: v2.LogicalRelation})
	field.Relation = &v2.RelationSpec{
		TargetTableID: targetTableID, Cardinality: "many", DeletePolicy: "setNull",
	}
	return field
}

func formulaField(id, name string, resultType ValueType, source string) v2.FieldDefinition {
	field := scalarField(id, name, resultType)
	field.LogicalType = v2.LogicalFormula
	field.Value.Required = true
	field.Storage = v2.StorageSpec{
		Kind: v2.StorageComputed, Options: v2.StorageOptions{OnlyInt: resultType.OnlyInt},
	}
	field.Formula = &v2.FormulaSpec{
		Language: "cel-v1", Source: source, ResultType: resultType.LogicalType,
	}
	return field
}

func assertFormulaCode(t *testing.T, err *Error, code string) {
	t.Helper()
	if err == nil || err.Code != code {
		t.Fatalf("error = %#v, want %s", err, code)
	}
}

var _ = time.RFC3339
