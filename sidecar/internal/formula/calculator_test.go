package formula

import (
	"context"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestCalculatorCacheIncludesFormulaDefinition(t *testing.T) {
	calculator := NewCalculator(NewCompiler(DefaultLimits()))
	first := formulaTable(
		formulaField("value_id", "value", integerType, "1"),
	)
	plan, err := calculator.plan(first)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Formulas[0].Source != "1" {
		t.Fatalf("first source = %q", plan.Formulas[0].Source)
	}

	changed := first
	changed.Snapshot.Fields = append([]v2.FieldDefinition(nil), first.Snapshot.Fields...)
	changed.Snapshot.Fields[0] = first.Snapshot.Fields[0]
	changed.Snapshot.Fields[0].Formula = &v2.FormulaSpec{
		Language: "cel-v1", Source: "2", ResultType: v2.LogicalNumber,
	}
	changed.FormulaRuntime[changed.Snapshot.Fields[0].Identity.FieldID] =
		schemaexecution.FormulaRuntime{Version: 2, Status: "ready"}
	plan, err = calculator.plan(changed)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Formulas[0].Source != "2" {
		t.Fatalf("stale cached source = %q", plan.Formulas[0].Source)
	}
}

func TestCalculatorEvaluatesSchemaV2IntegerFormula(t *testing.T) {
	t.Parallel()
	table := schemaexecution.Table{Snapshot: v2.SchemaSnapshot{
		Contract:       v2.Contract,
		TableID:        "tbl_calculator_v2",
		SchemaRevision: "schema_0002",
		Fields: []v2.FieldDefinition{
			{
				Contract: v2.Contract,
				Identity: v2.FieldIdentity{
					FieldID: "fld_quantity", PhysicalName: "f_quantity",
					ProviderFieldID: "pb_quantity",
				},
				DisplayName: "Quantity", LogicalType: v2.LogicalNumber,
				Lifecycle: v2.Lifecycle{State: v2.LifecycleActive},
				Storage: v2.StorageSpec{
					Kind: v2.StorageNumber, Options: v2.StorageOptions{OnlyInt: true},
				},
			},
			{
				Contract: v2.Contract,
				Identity: v2.FieldIdentity{
					FieldID: "fld_doubled", PhysicalName: "f_doubled",
					ProviderFieldID: "pb_doubled",
				},
				DisplayName: "Doubled", LogicalType: v2.LogicalFormula,
				Lifecycle: v2.Lifecycle{State: v2.LifecycleActive},
				Storage: v2.StorageSpec{
					Kind: v2.StorageComputed, Options: v2.StorageOptions{OnlyInt: true},
				},
				Formula: &v2.FormulaSpec{
					Language: "cel-v1", Source: "f_quantity * 2",
					ResultType: v2.LogicalNumber,
				},
			},
		},
	}}
	collection := core.NewBaseCollection("calculator_v2_records")
	collection.Fields.Add(&core.NumberField{Name: "f_quantity", OnlyInt: true})
	record := core.NewRecord(collection)
	record.Set("f_quantity", 21)

	got, err := NewCalculator(nil).Calculate(context.Background(), nil, table, record)
	if err != nil {
		t.Fatal(err)
	}
	if got["f_doubled"] != int64(42) {
		t.Fatalf("calculated values = %#v", got)
	}
}
