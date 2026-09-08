package main

import (
	"bytes"
	"testing"
	"testing/synctest"
	"time"
)

func TestFirstScreenFinishingWithinBudgetCancelsSampling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stop := watchFirstScreen(time.Second)
		if stack := stop(); len(stack) != 0 {
			t.Fatalf("fast query produced a stack: %q", stack)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
	})
}

func TestFirstScreenOverBudgetCapturesOneBoundedStack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stop := watchFirstScreen(time.Second)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		stack := stop()
		if len(stack) == 0 || len(stack) > firstScreenStackBytes ||
			!bytes.Contains(stack, []byte("watchFirstScreen")) {
			t.Fatalf("missing or unbounded sampler stack: %d bytes", len(stack))
		}
	})
}
