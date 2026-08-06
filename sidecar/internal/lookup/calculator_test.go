package lookup

import (
	"context"
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

func TestCalculateFieldPageRejectsLegacyAggregateDefinition(t *testing.T) {
	field := schema.FieldDefinition{
		Kind: schema.FieldKindLookup,
		Lookup: &schema.LookupSpec{
			Aggregate: "sum",
		},
	}
	_, err := NewCalculator().CalculateFieldPage(
		context.Background(), nil, schema.TableDefinition{}, nil, field, 0, 100,
	)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "lookup.request.aggregate_unsupported" {
		t.Fatalf("legacy aggregate value page error = %#v", err)
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

func TestLookupPageCollectorSlicesAcrossTraversalBranches(t *testing.T) {
	collector := lookupPageCollector{offset: 3, limit: 3}
	if start, end, stop := collector.rangeFor(2); start != 0 || end != 0 || stop {
		t.Fatalf("first branch range = %d:%d stop=%v", start, end, stop)
	}
	if start, end, stop := collector.rangeFor(4); start != 1 || end != 4 || stop {
		t.Fatalf("second branch range = %d:%d stop=%v", start, end, stop)
	}
	if start, end, stop := collector.rangeFor(10_001); start != 0 || end != 0 || !stop {
		t.Fatalf("after page branch range = %d:%d stop=%v", start, end, stop)
	}
	if collector.total != 10_007 {
		t.Fatalf("total = %d", collector.total)
	}
}

func TestMaterializationBudgetMeasuresBytesInsteadOfRecordCount(t *testing.T) {
	budget := &materializationBudget{remainingBytes: 8}
	if err := budget.consume("abc"); err != nil {
		t.Fatal(err)
	}
	if budget.remainingBytes != 3 {
		t.Fatalf("remaining bytes = %d", budget.remainingBytes)
	}
	err := budget.consume("toolarge")
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "lookup.value.too_expensive" {
		t.Fatalf("materialization error = %#v", err)
	}
}

func TestStreamingAggregateKeepsOnlyBoundedProvenanceAtScale(t *testing.T) {
	state := &streamingAggregateState{
		kind: "avg", storage: schema.StorageNumber, capture: true,
		seen: map[string]struct{}{},
		budget: materializationBudget{
			remainingBytes: lookupMaterializationBytes,
		},
	}
	for start := 0; start < 10_001; start += lookupTraversalBatch {
		end := min(start+lookupTraversalBatch, 10_001)
		batch := make([]lookupPathValue, 0, end-start)
		for index := start; index < end; index++ {
			batch = append(batch, lookupPathValue{
				collection: "items", itemID: "item",
				fieldID: "amount", value: 2,
			})
		}
		if err := state.consume(batch); err != nil {
			t.Fatal(err)
		}
	}
	value, err := state.value()
	if err != nil || value != 2.0 || state.count != 10_001 {
		t.Fatalf("streaming average = %#v count=%d err=%v", value, state.count, err)
	}
	if len(state.provenance) != cellProvenancePageSize || len(state.distinct) != 0 {
		t.Fatalf(
			"retained provenance=%d distinct=%d",
			len(state.provenance),
			len(state.distinct),
		)
	}
}

func TestWalkLookupPageHonorsCancellationBeforeStorage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := walkLookupPage(
		ctx, nil, traversalNode{}, schema.FieldDefinition{},
		[]schema.LookupPathStep{{RelationFieldID: "relation"}}, 0,
		map[string]schema.TableDefinition{}, &lookupPageCollector{limit: 1},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled traversal error = %#v", err)
	}
}
