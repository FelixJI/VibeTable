package relatedcomputation

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// The graph budget guard is covered here because the external test package
// cannot reach clockFieldKey/newClockCache. Both budget branches reject
// before any store access, so no PocketBase app is needed; asserting the
// budget error itself keeps this a real regression for the guard (dropping
// the check would reach the nil store and fail loudly).
func TestClockDependencyGraphBudgetFailsClosed(t *testing.T) {
	visiting := map[clockFieldKey]bool{}
	for i := 0; i < 4096; i++ {
		visiting[clockFieldKey{"table", fmt.Sprint(i)}] = true
	}
	if _, err := newClockCache().visit(context.Background(), nil, clockFieldKey{"table", "extra"}, visiting); err == nil ||
		!strings.Contains(err.Error(), "computed clock dependency graph exceeds 4096 fields") {
		t.Fatalf("graph limit was ignored: %v", err)
	}
	// A wide graph must obey the same bound as a deep chain.
	wide := newClockCache()
	for key := range visiting {
		wide.references[key] = nil
	}
	if _, err := wide.visit(context.Background(), nil, clockFieldKey{"table", "extra"}, map[clockFieldKey]bool{}); err == nil ||
		!strings.Contains(err.Error(), "computed clock dependency graph exceeds 4096 fields") {
		t.Fatalf("wide graph limit was ignored: %v", err)
	}
}
