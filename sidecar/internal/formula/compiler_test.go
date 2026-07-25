package formula

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	pbtypes "github.com/pocketbase/pocketbase/tools/types"

	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

func TestCompileAndEvaluateFormulaPlan(t *testing.T) {
	definition := formulaTable(
		scalarField("quantity_id", "quantity", schema.DataTypeFloat),
		scalarField("unit_price_id", "unit_price", schema.DataTypeFloat),
		scalarField("title_id", "title", schema.DataTypeShortText),
		formulaField("subtotal_id", "subtotal", schema.DataTypeFloat, "quantity * unit_price"),
		formulaField("label_id", "label", schema.DataTypeShortText, `concat(upper(trim(title)), ":", subtotal)`),
	)
	plan, formulaErr := NewCompiler(DefaultLimits()).CompileTable(definition)
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
		scalarField("quantity_id", "quantity", schema.DataTypeFloat),
		scalarField("unit_price_id", "unit_price", schema.DataTypeFloat),
		formulaField(
			"subtotal_id", "subtotal",
			schema.DataTypeFloat, "quantity * unit_price",
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
			plan, formulaErr := NewCompiler(DefaultLimits()).CompileTable(definition)
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
			scalarField("title_id", "title", schema.DataTypeShortText),
			formulaField("bad_id", "bad", schema.DataTypeFloat, "title"),
		)
		_, err := compiler.CompileTable(definition)
		assertFormulaCode(t, err, "formula.type")
	})
	t.Run("unknown identifier", func(t *testing.T) {
		definition := formulaTable(
			formulaField("bad_id", "bad", schema.DataTypeFloat, "missing + 1.0"),
		)
		_, err := compiler.CompileTable(definition)
		assertFormulaCode(t, err, "formula.dependency")
	})
	t.Run("hash identifier is sensitive", func(t *testing.T) {
		definition := formulaTable(
			scalarField("hash_id", "password_hash", schema.DataTypeHash),
			formulaField(
				"bad_id", "bad", schema.DataTypeShortText, "password_hash",
			),
		)
		_, err := compiler.CompileTable(definition)
		assertFormulaCode(t, err, "formula.dependency")
	})
	t.Run("invalid operation type", func(t *testing.T) {
		definition := formulaTable(
			formulaField("bad_id", "bad", schema.DataTypeInteger, "1 + true"),
		)
		_, err := compiler.CompileTable(definition)
		assertFormulaCode(t, err, "formula.type")
	})
	t.Run("dynamic json indexing", func(t *testing.T) {
		definition := formulaTable(
			scalarField("metadata_id", "metadata", schema.DataTypeJSON),
			scalarField("key_id", "key_name", schema.DataTypeShortText),
			formulaField("bad_id", "bad", schema.DataTypeShortText, "metadata[key_name]"),
		)
		_, err := compiler.CompileTable(definition)
		assertFormulaCode(t, err, "formula.dependency")
	})
	t.Run("function outside whitelist", func(t *testing.T) {
		definition := formulaTable(
			scalarField("title_id", "title", schema.DataTypeShortText),
			formulaField("bad_id", "bad", schema.DataTypeBoolean, `title.matches(".*")`),
		)
		_, err := compiler.CompileTable(definition)
		assertFormulaCode(t, err, "formula.dependency")
	})
	t.Run("cycle", func(t *testing.T) {
		definition := formulaTable(
			formulaField("a_id", "a_value", schema.DataTypeFloat, "b_value + 1.0"),
			formulaField("b_id", "b_value", schema.DataTypeFloat, "a_value + 1.0"),
		)
		_, err := compiler.CompileTable(definition)
		assertFormulaCode(t, err, "formula.cycle")
	})
}

func TestFormulaRuntimeErrorsAndIncrementalEvaluation(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	t.Run("divide by zero", func(t *testing.T) {
		plan, err := compiler.CompileTable(formulaTable(
			scalarField("amount_id", "amount", schema.DataTypeFloat),
			scalarField("divisor_id", "divisor", schema.DataTypeFloat),
			formulaField("result_id", "result", schema.DataTypeFloat, "amount / divisor"),
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
		plan, err := compiler.CompileTable(formulaTable(
			scalarField("amount_id", "amount", schema.DataTypeFloat),
			formulaField("result_id", "result", schema.DataTypeFloat, "amount + 1.0"),
		))
		if err != nil {
			t.Fatal(err)
		}
		_, evalErr := plan.Evaluate(context.Background(), map[string]any{"amount": nil}, nil)
		assertFormulaCode(t, evalErr, "formula.null")
	})
	t.Run("integer overflow", func(t *testing.T) {
		plan, err := compiler.CompileTable(formulaTable(
			scalarField("number_id", "number", schema.DataTypeInteger),
			formulaField("result_id", "result", schema.DataTypeInteger, "number * 2"),
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
		plan, err := compiler.CompileTable(formulaTable(
			scalarField("a_id", "a_value", schema.DataTypeFloat),
			scalarField("unrelated_id", "unrelated", schema.DataTypeFloat),
			formulaField("b_id", "b_value", schema.DataTypeFloat, "a_value + 1.0"),
			formulaField("c_id", "c_value", schema.DataTypeFloat, "b_value * 2.0"),
			formulaField("d_id", "d_value", schema.DataTypeFloat, "unrelated + 1.0"),
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
		plan, err := compiler.CompileTable(formulaTable(
			scalarField("count_id", "count", schema.DataTypeInteger),
			formulaField("next_id", "next", schema.DataTypeInteger, "count + 1"),
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
		_, err := NewCompiler(limits).CompileTable(formulaTable(
			formulaField("value_id", "value", schema.DataTypeFloat, "1.0 + 2.0"),
		))
		assertFormulaCode(t, err, "formula.resource_limit")
	})
	t.Run("comprehension", func(t *testing.T) {
		_, err := NewCompiler(DefaultLimits()).CompileTable(formulaTable(
			formulaField("value_id", "value", schema.DataTypeInteger, "[1, 2].map(x, x + 1).size()"),
		))
		assertFormulaCode(t, err, "formula.resource_limit")
	})
	t.Run("timestamp normalized UTC", func(t *testing.T) {
		plan, err := NewCompiler(DefaultLimits()).CompileTable(formulaTable(
			formulaField("value_id", "value", schema.DataTypeDateTime, `timestamp("2026-07-24T08:30:00+08:00")`),
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
	plan, formulaErr := NewCompiler(DefaultLimits()).CompileTable(formulaTable(
		scalarField("count_id", "count", schema.DataTypeInteger),
		scalarField("price_id", "price", schema.DataTypeFloat),
		scalarField("payload_id", "payload", schema.DataTypeJSON),
		scalarField("created_id", "created_at", schema.DataTypeDateTime),
		formulaField("next_id", "next", schema.DataTypeInteger, "count + 1"),
		formulaField("total_id", "total", schema.DataTypeFloat, "price * 2.0"),
		formulaField("customer_id", "customer", schema.DataTypeShortText, "payload.customer.name"),
		formulaField("payload_copy_id", "payload_copy", schema.DataTypeJSON, "payload"),
		formulaField("created_copy_id", "created_copy", schema.DataTypeDateTime, "created_at"),
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
	field := scalarField("payload_id", "payload", schema.DataTypeJSON)

	normalized, formulaErr := normalizeInput(field, pbtypes.JSONRaw{}, DefaultLimits())

	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	if normalized != nil {
		t.Fatalf("unset JSON normalized to %#v, want nil", normalized)
	}
}

func TestFormulaInputCollectionsAreRecursivelyBounded(t *testing.T) {
	limits := DefaultLimits()
	limits.CollectionSize = 2
	plan, formulaErr := NewCompiler(limits).CompileTable(formulaTable(
		scalarField("payload_id", "payload", schema.DataTypeJSON),
		formulaField("value_id", "value", schema.DataTypeShortText, "payload.name"),
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
	plan, formulaErr := NewCompiler(DefaultLimits()).CompileTable(formulaTable(
		formulaField("values_id", "values", schema.DataTypeList, "[1, 2]"),
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
	plan, formulaErr := NewCompiler(DefaultLimits()).CompileTable(formulaTable(
		formulaField("low_id", "low", schema.DataTypeInteger, "min(8, 3)"),
		formulaField("high_id", "high", schema.DataTypeFloat, "max(1.5, 2.25)"),
		formulaField(
			"day_id", "day", schema.DataTypeShortText,
			`formatDate(dateAdd(timestamp("2026-07-24T08:30:00+08:00"), duration("24h")), "yyyy-MM-dd")`,
		),
		formulaField(
			"previous_id", "previous", schema.DataTypeDateTime,
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

func TestFormulaEvaluateRejectsUnknownPreviewInputs(t *testing.T) {
	plan, formulaErr := NewCompiler(DefaultLimits()).CompileTable(formulaTable(
		formulaField("value_id", "value", schema.DataTypeInteger, "1"),
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

func formulaTable(fields ...schema.FieldDefinition) schema.TableDefinition {
	return schema.TableDefinition{
		ContractVersion: schema.ContractVersion,
		TableID:         "orders_id",
		PhysicalName:    "orders",
		DisplayName:     "Orders",
		Kind:            schema.TableKindBase,
		SchemaRevision:  "schema_1",
		ArchivePolicy:   schema.ArchivePolicy{Mode: schema.ArchiveModeNone},
		Fields:          fields,
		Indexes:         []schema.IndexDefinition{},
	}
}

func scalarField(id, name string, dataType schema.DataType) schema.FieldDefinition {
	storage := schema.StorageText
	switch dataType {
	case schema.DataTypeBoolean:
		storage = schema.StorageBool
	case schema.DataTypeInteger, schema.DataTypeFloat, schema.DataTypeDecimal:
		storage = schema.StorageNumber
	case schema.DataTypeDate, schema.DataTypeDateTime:
		storage = schema.StorageDate
	case schema.DataTypeJSON:
		storage = schema.StorageJSON
	}
	return schema.FieldDefinition{
		FieldID: id, PhysicalName: name, DisplayName: name,
		Kind: schema.FieldKindScalar, DataType: dataType, StorageType: storage,
		Nullable: true, Constraints: []schema.FieldConstraint{},
		Editor: schema.EditorDefinition{Kind: "text", Config: map[string]any{}},
	}
}

func formulaField(id, name string, resultType schema.DataType, source string) schema.FieldDefinition {
	field := scalarField(id, name, schema.DataTypeFormula)
	field.Kind = schema.FieldKindFormula
	field.DataType = schema.DataTypeFormula
	field.ReadOnly = true
	field.Nullable = false
	field.Formula = &schema.FormulaSpec{
		Language: "cel-v1", Source: source, ResultType: resultType,
		Version: 1, Status: "ready",
	}
	storage, _ := schema.CapabilityFor(resultType)
	field.StorageType = storage.Storage
	return field
}

func assertFormulaCode(t *testing.T, err *Error, code string) {
	t.Helper()
	if err == nil || err.Code != code {
		t.Fatalf("error = %#v, want %s", err, code)
	}
}

var _ = time.RFC3339
