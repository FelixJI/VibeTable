//go:build !race

package workspacev2

// Non-race builds enforce the frozen Task415 capacity budgets inside the
// opt-in host fixture producer. Under -race the same producer still runs its
// correctness contracts, but wall-clock budgets stay disabled because race
// instrumentation multiplies timing.
func init() {
	capacityHostFixtureBudgetsEnabled = true
}
