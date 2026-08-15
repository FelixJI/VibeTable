package formula

import (
	"context"
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestInferV2SourcePreservesIntegerStorageWithinLogicalNumber(t *testing.T) {
	t.Parallel()
	definition := V2Table{
		TableID: "orders",
		Fields: []v2.FieldDefinition{
			{
				Identity: v2.FieldIdentity{
					FieldID: "items_id", PhysicalName: "items",
					ProviderFieldID: "pb_items_id",
				},
				DisplayName: "Items", LogicalType: v2.LogicalRelation,
				Relation: &v2.RelationSpec{
					TargetTableID: "line_items", Cardinality: "many",
					DeletePolicy: "setNull",
				},
			},
		},
	}
	compiler := NewCompiler(DefaultLimits())
	logical, onlyInt, formulaErr := compiler.InferV2Source(
		definition, "relationCount(items)",
	)
	if formulaErr != nil || logical != v2.LogicalNumber || !onlyInt {
		t.Fatalf("COUNT inference = %s onlyInt=%v, error=%#v", logical, onlyInt, formulaErr)
	}
	logical, onlyInt, formulaErr = compiler.InferV2Source(
		definition, `relationSum(items, "amount")`,
	)
	if formulaErr != nil || logical != v2.LogicalNumber || onlyInt {
		t.Fatalf("SUM inference = %s onlyInt=%v, error=%#v", logical, onlyInt, formulaErr)
	}
}

func TestCompileV2TableCarriesFormulaIntegerPrecisionInValueType(t *testing.T) {
	t.Parallel()
	definition := V2Table{
		TableID: "orders",
		Fields: []v2.FieldDefinition{
			{
				Identity:    v2.FieldIdentity{FieldID: "fld_quantity", PhysicalName: "f_quantity"},
				DisplayName: "Quantity", LogicalType: v2.LogicalNumber,
				Storage: v2.StorageSpec{Kind: v2.StorageNumber, Options: v2.StorageOptions{OnlyInt: true}},
			},
			{
				Identity:    v2.FieldIdentity{FieldID: "fld_doubled", PhysicalName: "f_doubled"},
				DisplayName: "Doubled", LogicalType: v2.LogicalFormula,
				Storage: v2.StorageSpec{Kind: v2.StorageComputed, Options: v2.StorageOptions{OnlyInt: true}},
				Formula: &v2.FormulaSpec{Language: "cel-v1", Source: "f_quantity * 2", ResultType: v2.LogicalNumber},
			},
		},
	}
	plan, formulaErr := NewCompiler(DefaultLimits()).CompileV2Table(definition)
	if formulaErr != nil {
		t.Fatalf("CompileV2Table() error = %#v", formulaErr)
	}
	if got, want := plan.Formulas[0].ResultType, (ValueType{LogicalType: v2.LogicalNumber, OnlyInt: true}); got != want {
		t.Fatalf("formula result type = %#v, want %#v", got, want)
	}
}

func TestInferV2SourcePromotesMixedIntegerAndDecimalArithmetic(t *testing.T) {
	t.Parallel()
	definition := V2Table{
		TableID: "orders",
		Fields: []v2.FieldDefinition{{
			Identity: v2.FieldIdentity{
				FieldID: "fld_quantity", PhysicalName: "f_quantity",
				ProviderFieldID: "pb_quantity",
			},
			DisplayName: "Quantity", LogicalType: v2.LogicalNumber,
			Storage: v2.StorageSpec{
				Kind: v2.StorageNumber, Options: v2.StorageOptions{OnlyInt: true},
			},
		}},
	}
	compiler := NewCompiler(DefaultLimits())
	logical, onlyInt, formulaErr := compiler.InferV2Source(definition, "f_quantity * 2")
	if formulaErr != nil || logical != v2.LogicalNumber || !onlyInt {
		t.Fatalf("integer arithmetic = %s onlyInt=%v, error=%#v", logical, onlyInt, formulaErr)
	}
	logical, onlyInt, formulaErr = compiler.InferV2Source(definition, "f_quantity * 2.0")
	if formulaErr != nil || logical != v2.LogicalNumber || onlyInt {
		t.Fatalf("mixed arithmetic = %s onlyInt=%v, error=%#v", logical, onlyInt, formulaErr)
	}
	definition.Fields = append(definition.Fields, v2.FieldDefinition{
		Identity: v2.FieldIdentity{
			FieldID: "fld_doubled", PhysicalName: "f_doubled",
			ProviderFieldID: "pb_doubled",
		},
		DisplayName: "Doubled", LogicalType: v2.LogicalFormula,
		Storage: v2.StorageSpec{Kind: v2.StorageComputed},
		Formula: &v2.FormulaSpec{
			Language: "cel-v1", Source: "f_quantity * 2.0", ResultType: v2.LogicalNumber,
		},
	})
	plan, formulaErr := compiler.CompileV2Table(definition)
	if formulaErr != nil {
		t.Fatalf("CompileV2Table() error = %#v", formulaErr)
	}
	result, formulaErr := plan.Evaluate(
		context.Background(), map[string]any{"f_quantity": int64(4)}, nil,
	)
	if formulaErr != nil || result["f_doubled"] != float64(8) {
		t.Fatalf("mixed arithmetic result = %#v, error=%#v", result, formulaErr)
	}
}

func TestInferExecutionSourceMapsCELScalarTypesToSchemaV2ValueTypes(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(DefaultLimits())
	table := schemaexecution.Table{Snapshot: v2.SchemaSnapshot{
		Contract: v2.Contract,
		TableID:  "tbl_formula_types",
	}}
	tests := []struct {
		name   string
		source string
		want   ValueType
	}{
		{name: "bool", source: "true", want: ValueType{LogicalType: v2.LogicalBool}},
		{name: "integer", source: "1", want: ValueType{LogicalType: v2.LogicalNumber, OnlyInt: true}},
		{name: "decimal", source: "1.5", want: ValueType{LogicalType: v2.LogicalNumber}},
		{name: "text", source: `"Ada"`, want: ValueType{LogicalType: v2.LogicalText}},
		{
			name: "timestamp", source: `timestamp("2026-08-13T00:00:00Z")`,
			want: ValueType{LogicalType: v2.LogicalDateTime},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, formulaErr := compiler.InferExecutionSource(table, test.source)
			if formulaErr != nil {
				t.Fatalf("InferExecutionSource(%q) error = %#v", test.source, formulaErr)
			}
			if got != test.want {
				t.Fatalf("InferExecutionSource(%q) = %#v, want %#v", test.source, got, test.want)
			}
		})
	}
}

func TestInferExecutionSourceRejectsNonScalarCELResult(t *testing.T) {
	t.Parallel()
	got, formulaErr := NewCompiler(DefaultLimits()).InferExecutionSource(
		schemaexecution.Table{Snapshot: v2.SchemaSnapshot{
			Contract: v2.Contract,
			TableID:  "tbl_formula_types",
		}},
		"[1, 2]",
	)
	if formulaErr == nil || formulaErr.Code != "formula.type" {
		t.Fatalf("InferExecutionSource() = %#v, error %#v; want formula.type", got, formulaErr)
	}
}
