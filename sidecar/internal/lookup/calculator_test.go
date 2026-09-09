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

func TestCalculateCellsBatchSharesPathAndKeepsOrderedDuplicateSources(t *testing.T) {
	definition := schemaexecution.Table{
		PhysicalName: "lookup_rows",
		Snapshot: v2.SchemaSnapshot{TableID: "rows", Fields: []v2.FieldDefinition{
			{Identity: v2.FieldIdentity{FieldID: "links", PhysicalName: "links"}, Relation: &v2.RelationSpec{TargetTableID: "rows"}},
			{Identity: v2.FieldIdentity{FieldID: "name", PhysicalName: "name"}},
			{Identity: v2.FieldIdentity{FieldID: "score", PhysicalName: "score"}},
			{Identity: v2.FieldIdentity{FieldID: "lookup_name", PhysicalName: "f_lookup_name"}, LogicalType: v2.LogicalLookup, Lookup: &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: "links"}, {RelationFieldID: "links"}}, TargetFieldID: "name"}},
			{Identity: v2.FieldIdentity{FieldID: "lookup_score", PhysicalName: "f_lookup_score"}, LogicalType: v2.LogicalLookup, Lookup: &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: "links"}, {RelationFieldID: "links"}}, TargetFieldID: "score"}},
		}},
	}
	collection := core.NewBaseCollection("lookup_rows")
	var records []*core.Record
	for _, id := range []string{"root", "a", "b", "one", "two"} {
		record := core.NewRecord(collection)
		record.Id = id
		record.Set("name", id)
		record.Set("score", len(id))
		records = append(records, record)
	}
	records[0].Set("links", []string{"b", "a"})
	records[1].Set("links", []string{"one", "two"})
	records[2].Set("links", []string{"two", "one"})
	got, err := NewCalculator().CalculateCellsBatch(context.Background(), nil, definition, records, nil)
	if err != nil {
		t.Fatal(err)
	}
	cell := got["root"]["f_lookup_name"]
	if !reflect.DeepEqual(cell.Value, []any{"two", "one", "one", "two"}) || cell.ProvenanceTotal != 4 || !cell.ProvenanceTotalKnown {
		t.Fatalf("ordered duplicate projection = %#v", cell)
	}
	for i, id := range []string{"two", "one", "one", "two"} {
		if cell.Provenance[i].ItemID != id || got["root"]["f_lookup_score"].Provenance[i].ItemID != id {
			t.Fatalf("source order diverged at %d", i)
		}
	}
	selected, err := NewCalculator().CalculateCellsBatch(context.Background(), nil, definition, records, map[string]bool{"lookup_score": true})
	if err != nil || len(selected["root"]) != 1 || selected["root"]["f_lookup_score"].State != "ok" {
		t.Fatalf("stable field selection = %#v, %v", selected, err)
	}
}

func TestCalculateCellsBatchHonorsCancellationBeforeStorage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewCalculator().CalculateCellsBatch(ctx, nil, schemaexecution.Table{}, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled page error = %#v", err)
	}
}
