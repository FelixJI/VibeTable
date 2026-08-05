package lookup

import (
	"errors"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

func TestAggregateLookupValues(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		storage schema.StorageType
		values  []any
		want    any
	}{
		{name: "empty first", kind: "first", storage: schema.StorageText, values: []any{}, want: nil},
		{name: "first", kind: "first", storage: schema.StorageText, values: []any{"b", "a"}, want: "b"},
		{name: "many", kind: "none", storage: schema.StorageText, values: []any{"b", "a"}, want: []any{"b", "a"}},
		{name: "count", kind: "count", storage: schema.StorageNumber, values: []any{1, 2}, want: 2},
		{name: "non-null count", kind: "countNonNull", storage: schema.StorageNumber, values: []any{1, nil, 2}, want: 2},
		{name: "distinct", kind: "distinct", storage: schema.StorageJSON, values: []any{"a", "a", "b"}, want: []any{"a", "b"}},
		{name: "sum", kind: "sum", storage: schema.StorageNumber, values: []any{1, "2.5"}, want: 3.5},
		{name: "average", kind: "avg", storage: schema.StorageNumber, values: []any{2, 4}, want: 3.0},
		{name: "minimum", kind: "min", storage: schema.StorageText, values: []any{"b", "a"}, want: "a"},
		{name: "maximum", kind: "max", storage: schema.StorageNumber, values: []any{2, 7, 1}, want: 7},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := aggregate(testCase.kind, testCase.storage, testCase.values)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("aggregate = %#v, want %#v", got, testCase.want)
			}
		})
	}
}

func TestAggregateLookupRejectsInvalidNumericValues(t *testing.T) {
	for _, values := range [][]any{{"not-number"}, {1e308, 1e308}} {
		_, err := aggregate("sum", schema.StorageNumber, values)
		var productErr *mutation.ProductError
		if !errors.As(err, &productErr) ||
			productErr.Code != "mutation.lookup.invalid_value" {
			t.Fatalf("error = %#v", err)
		}
	}
}

func TestRelationIDsAcceptProviderNeutralCollections(t *testing.T) {
	for _, testCase := range []struct {
		value any
		want  []string
	}{
		{value: nil, want: []string{}},
		{value: "one", want: []string{"one"}},
		{value: []string{"one", "two"}, want: []string{"one", "two"}},
		{value: [2]string{"one", "two"}, want: []string{"one", "two"}},
	} {
		if got := relationIDs(testCase.value); !reflect.DeepEqual(got, testCase.want) {
			t.Fatalf("relationIDs(%#v) = %#v, want %#v", testCase.value, got, testCase.want)
		}
	}
}

func TestFanoutBudgetIsSharedAcrossPathHops(t *testing.T) {
	budget := &fanoutBudget{remaining: 3}
	if err := budget.consume(2); err != nil {
		t.Fatal(err)
	}
	err := budget.consume(2)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "lookup.value.too_expensive" {
		t.Fatalf("fanout error = %#v", err)
	}
	if budget.remaining != 1 {
		t.Fatalf("remaining budget = %d", budget.remaining)
	}
}

func TestFanoutBudgetSupportsInventoryScaleRelations(t *testing.T) {
	budget := &fanoutBudget{remaining: maxTraversalCost}
	if err := budget.consume(10_001); err != nil {
		t.Fatalf("10,001 related inventory records must fit the resource budget: %v", err)
	}
	if budget.remaining != maxTraversalCost-10_001 {
		t.Fatalf("remaining budget = %d", budget.remaining)
	}
}
