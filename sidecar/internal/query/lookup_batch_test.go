package query_test

import (
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"testing"
)

func TestLookupMatchBatchRejectsUnboundedOrIncompletePredicates(t *testing.T) {
	valid := []query.FilterExpression{{Field: "amount", Operator: query.OperatorEqual, Value: 0}}
	for _, test := range []struct {
		groups [][]query.FilterExpression
		limit  int
	}{
		{nil, 100}, {make([][]query.FilterExpression, 65), 100},
		{[][]query.FilterExpression{valid}, 0}, {[][]query.FilterExpression{valid}, 257},
		{[][]query.FilterExpression{{}}, 100},
		{[][]query.FilterExpression{make([]query.FilterExpression, 51)}, 100},
		{[][]query.FilterExpression{{{Field: "missing", Operator: query.OperatorEqual, Value: 1}}}, 100},
	} {
		if _, err := query.CompileMatchBatch(descriptorFixture(), test.groups, "", test.limit); err == nil {
			t.Fatalf("accepted invalid batch: %+v", test)
		}
	}
	descriptor := descriptorFixture()
	descriptor.PrimaryKey = ""
	if _, err := query.CompileMatchBatch(descriptor, [][]query.FilterExpression{valid}, "", 1); err == nil {
		t.Fatal("invalid descriptor accepted")
	}
	descriptor = descriptorFixture()
	descriptor.ArchiveMode = query.ArchiveModeDeletedAt
	descriptor.ArchiveField = "missing"
	if _, err := query.CompileMatchBatch(descriptor, [][]query.FilterExpression{valid}, "", 1); err == nil {
		t.Fatal("invalid archive policy accepted")
	}
	// A nil predicate group represents SQL unknown, never an unrestricted scan.
	if _, err := query.CompileMatchBatch(descriptorFixture(), [][]query.FilterExpression{nil}, "", 256); err != nil {
		t.Fatal(err)
	}
}
