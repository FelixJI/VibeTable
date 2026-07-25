package formula

import (
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

func TestCalculatorCacheIncludesFormulaDefinition(t *testing.T) {
	calculator := NewCalculator(NewCompiler(DefaultLimits()))
	first := formulaTable(
		formulaField("value_id", "value", schema.DataTypeInteger, "1"),
	)
	plan, err := calculator.plan(first)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Formulas[0].Source != "1" {
		t.Fatalf("first source = %q", plan.Formulas[0].Source)
	}

	changed := first
	changed.Fields = append([]schema.FieldDefinition(nil), first.Fields...)
	changed.Fields[0] = first.Fields[0]
	changed.Fields[0].Formula = &schema.FormulaSpec{
		Language:   "cel-v1",
		Source:     "2",
		ResultType: schema.DataTypeInteger,
		Version:    2,
		Status:     "ready",
	}
	plan, err = calculator.plan(changed)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Formulas[0].Source != "2" {
		t.Fatalf("stale cached source = %q", plan.Formulas[0].Source)
	}
}
