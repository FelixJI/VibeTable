package sourceimport

import "testing"

func TestManagementProjectionPreservesDurableMappingsAndProvenance(t *testing.T) {
	durable := Result{Created: 1, Total: 2, NotSubmitted: 1,
		Batches: []Batch{{ID: "batch-1", Created: 1, Mappings: []Mapping{{Kind: "record", LocalID: "row-1"}}}},
		Fields:  []FieldSummary{{Policy: PolicySnapshot, Definition: "SOURCE_FORMULA()"}}}
	page := Project(durable)
	if len(page.Batches[0].Mappings) != 0 || page.Fields[0].Definition != "" || page.Created != 1 || page.NotSubmitted != 1 || page.Fields[0].Policy != PolicySnapshot {
		t.Fatalf("management projection lost execution facts: %#v", page)
	}
	if durable.Batches[0].Mappings[0].LocalID != "row-1" || durable.Fields[0].Definition != "SOURCE_FORMULA()" {
		t.Fatal("management projection mutated durable recovery evidence")
	}
}
