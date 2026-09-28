package formula

import (
	"context"
	"fmt"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"reflect"
	"strings"
	"testing"
)

func TestCollectionFilterSumVerticalSlice(t *testing.T) {
	field := formulaField("total_id", "total", numberType, "SUM(FILTER([1.0, 2.0, 0.0], CurrentValue > 0.0))")
	field.Formula.Language = "cel-v2"
	plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(field))
	if err != nil {
		t.Fatal(err)
	}
	values, err := plan.Evaluate(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if values["total"] != 3.0 {
		t.Fatalf("total = %#v", values["total"])
	}
}

func collectionTableTestDefinition(source string) schemaexecution.Table {
	field := formulaField("total_id", "total", numberType, source)
	field.Formula.Language = "cel-v2"
	definition := formulaTable(scalarField("contract_id", "contract", textType), field)
	definition.FormulaSources = map[string]v2.SchemaSnapshot{"shipments": {
		TableID: "shipments", SchemaRevision: "schema_1", Fields: []v2.FieldDefinition{
			scalarField("source_contract_id", "contract", textType),
			scalarField("amount_id", "amount", numberType),
		},
	}}
	return definition
}

func TestCollectionTableFilterProjectionKeepsOuterScopeAndFullSet(t *testing.T) {
	for _, count := range []int{0, 1, 200} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			definition := collectionTableTestDefinition("SUM(PROJECT(FILTER(TABLE(\"shipments\"), CurrentValue.contract == contract && CurrentValue.amount >= 0.0), CurrentValue.amount))")
			plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(definition)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(plan.Formulas[0].Dependencies, []string{"contract_id"}) {
				t.Fatalf("dependencies=%v", plan.Formulas[0].Dependencies)
			}
			reads := 0
			ctx := WithCollectionSourceReader(context.Background(), func(ctx context.Context, request CollectionReadRequest, yield func(map[string]any) error) error {
				reads++
				if request.TableID != "shipments" {
					t.Fatalf("table=%s", request.TableID)
				}
				rows := make([]map[string]any, 0, count+1)
				for index := 0; index < count; index++ {
					rows = append(rows, map[string]any{"contract": "A", "amount": 2.0})
				}
				rows = append(rows, map[string]any{"contract": "B", "amount": 999.0})
				return collectionRowsReader(rows, nil)(ctx, request, yield)
			})
			result, err := plan.Evaluate(ctx, map[string]any{"contract": "A"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result["total"] != float64(count*2) || reads != 1 {
				t.Fatalf("result=%v reads=%d", result, reads)
			}
		})
	}
}

func TestCollectionLazyBranchAndResourceFailure(t *testing.T) {
	definition := collectionTableTestDefinition("IF(false, SUM(PROJECT(TABLE(\"shipments\"), CurrentValue.amount)), 7.0)")
	plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(definition)
	if err != nil {
		t.Fatal(err)
	}
	result, err := plan.Evaluate(context.Background(), map[string]any{"contract": "A"}, nil)
	if err != nil || result["total"] != 7.0 {
		t.Fatalf("dead branch=%v, %v", result, err)
	}
	definition = collectionTableTestDefinition("IFERROR(SUM(PROJECT(TABLE(\"shipments\"), CurrentValue.amount)), 7.0)")
	limits := DefaultLimits()
	limits.CollectionBytes = 32
	plan, err = NewCompiler(limits).CompileExecutionTable(definition)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithCollectionSourceReader(context.Background(), collectionRowsReader([]map[string]any{{"amount": 1.0, "contract": strings.Repeat("large", 100)}}, nil))
	_, err = plan.Evaluate(ctx, map[string]any{"contract": "A"}, nil)
	assertFormulaCode(t, err, "formula.resource_limit")
}

func TestCollectionSourceSchemaParticipatesInPlanCache(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	compilations := 0
	compiler.cache.compile = func(definition schemaexecution.Table) (*Plan, *Error) {
		compilations++
		return compiler.compileExecutionTable(definition)
	}
	definition := collectionTableTestDefinition("SUM(PROJECT(TABLE(\"shipments\"), CurrentValue.amount))")
	if _, err := compiler.CompileExecutionTable(definition); err != nil {
		t.Fatal(err)
	}
	if _, err := compiler.CompileExecutionTable(definition); err != nil || compilations != 1 {
		t.Fatalf("same snapshot recompiled: compilations=%d err=%v", compilations, err)
	}
	source := definition.FormulaSources["shipments"]
	source.Fields = append([]v2.FieldDefinition(nil), source.Fields...)
	source.Fields[1] = scalarField("amount_id", "amount", textType)
	// A transaction-local candidate can change source shape before a revision
	// commits. Full source snapshots, not revision strings alone, own the key.
	definition.FormulaSources["shipments"] = source
	_, err := compiler.CompileExecutionTable(definition)
	assertFormulaCode(t, err, "formula.type")
	if compilations != 2 {
		t.Fatalf("candidate source reused old plan: %d", compilations)
	}
	source.SchemaRevision = "schema_2"
	definition.FormulaSources["shipments"] = source
	_, err = compiler.CompileExecutionTable(definition)
	assertFormulaCode(t, err, "formula.type")
	if compilations != 3 {
		t.Fatalf("committed source reused old plan: %d", compilations)
	}
}

func TestCollectionSandboxStillRejectsUserLoopsAndNestedRanges(t *testing.T) {
	for _, source := range []string{
		"SUM([1.0].map(x,x))",
		"SUM(FILTER(FILTER([1.0], CurrentValue > 0.0), CurrentValue > 0.0))",
		"SUM(FILTER([1.0], [true][int(CurrentValue)]))",
	} {
		field := formulaField("total_id", "total", numberType, source)
		field.Formula.Language = "cel-v2"
		if _, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(field)); err == nil {
			t.Fatalf("accepted %s", source)
		}
	}
}

func collectionCatalogDefinition() schemaexecution.Table {
	definition := formulaTable()
	definition.FormulaSources = map[string]v2.SchemaSnapshot{
		"tbl_example": {TableID: "tbl_example", Fields: []v2.FieldDefinition{scalarField("amount_id", "amount", numberType)}},
	}
	return definition
}

func collectionRowsReader(rows []map[string]any, failure error) CollectionSourceReader {
	return func(ctx context.Context, request CollectionReadRequest, yield func(map[string]any) error) error {
		if failure != nil {
			return failure
		}
		for _, row := range rows {
			if err := yield(row); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestCollectionLookupListsAndDateFormulaChains(t *testing.T) {
	t.Run("lookup keeps the full typed scalar list", func(t *testing.T) {
		definition := collectionTableTestDefinition("SUM(amounts)")
		lookup := scalarField("lookup_id", "amounts", ValueType{LogicalType: v2.LogicalLookup})
		lookup.Lookup = &v2.LookupSpec{Condition: &v2.LookupCondition{SourceTableID: "shipments"}, TargetFieldID: "amount_id"}
		definition.Snapshot.Fields = append(definition.Snapshot.Fields, lookup)
		plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(definition)
		if err != nil {
			t.Fatal(err)
		}
		result, err := plan.Evaluate(context.Background(), map[string]any{"amounts": []any{2.0, nil, 0.0, 3.0}}, nil)
		if err != nil || result["total"] != 5.0 {
			t.Fatalf("result=%v err=%v", result, err)
		}
		_, err = plan.Evaluate(context.Background(), map[string]any{"amounts": []any{2.0, "3"}}, nil)
		assertFormulaCode(t, err, "formula.type")
	})
	t.Run("date list round trips between formulas", func(t *testing.T) {
		dates := formulaField("dates_id", "dates", ValueType{LogicalType: v2.LogicalJSON, ElementType: v2.LogicalDateTime},
			"PROJECT(TABLE(\"shipments\"), CurrentValue.day)")
		dates.Formula.Language, dates.Formula.ResultElementType = "cel-v2", v2.LogicalDateTime
		joined := formulaField("joined_id", "joined", textType, "ARRAYJOIN(dates, \",\" )")
		joined.Formula.Language = "cel-v2"
		definition := formulaTable(dates, joined)
		definition.FormulaSources = map[string]v2.SchemaSnapshot{"shipments": {
			TableID: "shipments", Fields: []v2.FieldDefinition{scalarField("day_id", "day", dateTimeType)},
		}}
		plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(definition)
		if err != nil {
			t.Fatal(err)
		}
		ctx := WithCollectionSourceReader(context.Background(), collectionRowsReader([]map[string]any{
			{"day": "2026-09-01T08:00:00+08:00"}, {"day": nil},
		}, nil))
		result, err := plan.Evaluate(ctx, nil, nil)
		if err != nil || result["joined"] != "2026-09-01T00:00:00Z," {
			t.Fatalf("result=%v err=%v", result, err)
		}
	})
}

func TestCollectionTableReadSelectsFieldsAndBindsOuterPredicate(t *testing.T) {
	definition := collectionTableTestDefinition("SUM(PROJECT(FILTER(TABLE(\"shipments\"), CurrentValue.contract == contract), CurrentValue.amount))")
	source := definition.FormulaSources["shipments"]
	source.Fields = append(source.Fields, scalarField("unrelated_id", "unrelated", textType))
	definition.FormulaSources["shipments"] = source
	plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(definition)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithCollectionSourceReader(context.Background(), func(ctx context.Context, request CollectionReadRequest, yield func(map[string]any) error) error {
		if len(request.Fields) != 2 || len(request.Filters) != 1 || request.Filters[0].Field != "contract" || request.Filters[0].Value != "A" {
			t.Fatalf("unbounded or unbound source request: %#v", request)
		}
		return yield(map[string]any{"contract": "A", "amount": 3.0})
	})
	result, err := plan.Evaluate(ctx, map[string]any{"contract": "A"}, nil)
	if err != nil || result["total"] != 3.0 {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

func TestCollectionSourceReuseIsLimitedToOneEvaluation(t *testing.T) {
	definition := collectionTableTestDefinition("SUMIF(TABLE(\"shipments\"), CurrentValue.contract == contract, CurrentValue.amount)")
	second := definition.Snapshot.Fields[1]
	second.Identity = v2.FieldIdentity{FieldID: "second_id", PhysicalName: "second"}
	definition.Snapshot.Fields = append(definition.Snapshot.Fields, second)
	plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(definition)
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	ctx := WithCollectionSourceReader(context.Background(), func(ctx context.Context, request CollectionReadRequest, yield func(map[string]any) error) error {
		reads++
		return yield(map[string]any{"contract": "A", "amount": float64(reads)})
	})
	for want := 1; want <= 2; want++ {
		result, err := plan.Evaluate(ctx, map[string]any{"contract": "A"}, nil)
		if err != nil || reads != want || result["total"] != float64(want) || result["second"] != float64(want) {
			t.Fatalf("read-only evaluation %d: reads=%d result=%v err=%v", want, reads, result, err)
		}
	}
}

func TestCollectionIfErrorAbsorbsArithmeticButNotResources(t *testing.T) {
	field := formulaField("result_id", "result", numberType, "IFERROR(SUM([1e308, 1e308]), 7.0)")
	field.Formula.Language = "cel-v2"
	plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(field))
	if err != nil {
		t.Fatal(err)
	}
	result, err := plan.Evaluate(context.Background(), nil, nil)
	if err != nil || result["result"] != 7.0 {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

func TestCollectionBranchInferencePreservesElementType(t *testing.T) {
	source := "IF(true, UNIQUE([1.0, 1.0]), UNIQUE([2.0]))"
	compiler := NewCompiler(DefaultLimits())
	inferred, err := compiler.InferExecutionSource(formulaTable(), source)
	if err != nil || inferred.ElementType != v2.LogicalNumber {
		t.Fatalf("inferred=%v err=%v", inferred, err)
	}
	field := formulaField("result_id", "result", inferred, source)
	field.Formula.Language, field.Formula.ResultElementType = "cel-v2", inferred.ElementType
	plan, err := compiler.CompileExecutionTable(formulaTable(field))
	if err != nil {
		t.Fatal(err)
	}
	result, err := plan.Evaluate(context.Background(), nil, nil)
	if err != nil || !reflect.DeepEqual(result["result"], []any{1.0}) {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

func TestCollectionSourceReuseIncludesRecursiveEvaluation(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	innerDefinition := collectionTableTestDefinition(`SUM(PROJECT(TABLE("shipments"), CurrentValue.amount))`)
	inner, err := compiler.CompileExecutionTable(innerDefinition)
	if err != nil {
		t.Fatal(err)
	}
	outerDefinition := collectionTableTestDefinition(`SUM(PROJECT(TABLE("shipments"), CurrentValue.amount)) + SUM(PROJECT(TABLE("summaries"), CurrentValue.amount))`)
	outerDefinition.FormulaSources["summaries"] = v2.SchemaSnapshot{TableID: "summaries", SchemaRevision: "schema_1", Fields: []v2.FieldDefinition{scalarField("amount_id", "amount", numberType)}}
	outer, err := compiler.CompileExecutionTable(outerDefinition)
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	ctx := WithCollectionSourceReader(context.Background(), func(ctx context.Context, request CollectionReadRequest, yield func(map[string]any) error) error {
		if request.TableID == "shipments" {
			reads++
			return yield(map[string]any{"amount": float64(reads)})
		}
		// A TABLE computed-source projection recursively evaluates a formula
		// against the same read-only snapshot and therefore the same range.
		result, err := inner.Evaluate(ctx, nil, nil)
		if err != nil {
			return err
		}
		return yield(map[string]any{"amount": result["total"]})
	})
	for batch := 1; batch <= 2; batch++ {
		result, err := outer.Evaluate(ctx, nil, nil)
		if err != nil || reads != batch || result["total"] != float64(2*batch) {
			t.Fatalf("batch %d: reads=%d result=%v err=%v", batch, reads, result, err)
		}
	}
}
