package lookup

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestCanonicalLookupValuePreservesOneOrManyCardinality(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		values []any
		want   any
	}{
		{name: "empty", values: []any{}, want: nil},
		{name: "one", values: []any{"Ada"}, want: "Ada"},
		{name: "many", values: []any{"Ada", "Grace"}, want: []any{"Ada", "Grace"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := canonicalLookupValue(testCase.values); !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("canonical lookup value = %#v, want %#v", got, testCase.want)
			}
		})
	}
}

func TestLookupDefinitionRejectsRemovedAggregateField(t *testing.T) {
	var lookup v2.LookupSpec
	err := v2.StrictDecode(
		[]byte(`{"relationFieldId":"customer","targetFieldId":"name","aggregate":"sum"}`),
		&lookup,
	)
	if err == nil {
		t.Fatal("removed Lookup aggregate field was accepted")
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

func TestDecodeLookupFieldValuePreservesV2OptionIdentity(t *testing.T) {
	record := core.NewRecord(core.NewBaseCollection("lookup_targets"))
	record.Set("f_status", "opt_in_progress")
	field := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{PhysicalName: "f_status"},
		LogicalType: v2.LogicalSelect,
		Select: &v2.SelectSpec{Options: []v2.SelectOption{{
			OptionID: "opt_in_progress", Label: "进行中", State: v2.OptionActive,
		}}},
	}
	if got := decodeLookupFieldValue(field, record); got != "opt_in_progress" {
		t.Fatalf("decoded Lookup option = %#v", got)
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

func TestWalkLookupPageHonorsCancellationBeforeStorage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := walkLookupPage(
		ctx, nil, traversalNode{}, v2.FieldDefinition{},
		[]v2.LookupPathStep{{RelationFieldID: "relation"}}, 0,
		map[string]schemaexecution.Table{}, &lookupPageCollector{limit: 1},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled traversal error = %#v", err)
	}
}
