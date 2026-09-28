package formula

import (
	"context"
	"testing"
)

func TestRecursiveSourcesShareCollectionCostAndRetainedBytes(t *testing.T) {
	limits := DefaultLimits()
	evaluation := &collectionEvaluation{remaining: 2, limits: limits, bytes: collectionBudget{remaining: 32}}
	ctx := context.WithValue(context.Background(), collectionEvaluationKey{}, evaluation)
	for index := 0; index < 2; index++ {
		child := EnsureSourceEvaluationBudget(ctx)
		if _, err := ChargeSourceEvaluation(child); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ChargeSourceEvaluation(EnsureSourceEvaluationBudget(ctx)); err == nil {
		t.Fatal("sibling computed reads reset the collection cost budget")
	}
	if _, err := RetainSourceValue(ctx, "1234567890"); err != nil {
		t.Fatal(err)
	}
	if _, err := RetainSourceValue(ctx, "1234567890"); err == nil {
		t.Fatal("recursive source memoization escaped the shared byte budget")
	}
}
