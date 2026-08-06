package schema

import (
	"reflect"
	"testing"
)

func TestQueryFilterOperatorsAreDerivedBySidecarSchema(t *testing.T) {
	numeric := QueryFilterOperators(FieldDefinition{DataType: DataTypeDecimal})
	if !reflect.DeepEqual(numeric, []string{
		"eq", "ne", "gt", "gte", "lt", "lte", "between", "in", "is_null", "is_not_null",
	}) {
		t.Fatalf("numeric operators = %#v", numeric)
	}
	relation := QueryFilterOperators(FieldDefinition{
		Kind: FieldKindRelation, DataType: DataTypeRelation,
	})
	if !reflect.DeepEqual(relation, []string{"eq", "ne", "in", "is_null", "is_not_null"}) {
		t.Fatalf("relation operators = %#v", relation)
	}
	formula := QueryFilterOperators(FieldDefinition{
		Kind: FieldKindFormula, DataType: DataTypeFormula,
		Formula: &FormulaSpec{ResultType: DataTypeBoolean},
	})
	if !reflect.DeepEqual(formula, []string{"eq", "ne", "in", "is_null", "is_not_null"}) {
		t.Fatalf("formula operators = %#v", formula)
	}
}
