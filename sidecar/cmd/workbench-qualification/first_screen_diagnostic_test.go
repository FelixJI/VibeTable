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
		sample := stop()
		if sample.Sampled || len(sample.Stack) != 0 || sample.TriggerLag != 0 || sample.StackDuration != 0 {
			t.Fatalf("stopped watcher reported evidence: %#v", sample)
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
		sample := stop()
		if !sample.Sampled || len(sample.Stack) == 0 || len(sample.Stack) > firstScreenStackBytes ||
			!bytes.Contains(sample.Stack, []byte("watchFirstScreen")) {
			t.Fatalf("missing or unbounded sampler stack: %#v", sample)
		}
	})
}

func TestFirstScreenStopBeforeDeadlineNeverRunsCapture(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		captures := 0
		stop := watchFirstScreenWithStackWriter(time.Second, func([]byte, bool) int {
			captures++
			return 0
		})
		sample := stop()
		if sample.Sampled || captures != 0 {
			t.Fatalf("stopped watcher sampled: %#v captures=%d", sample, captures)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if captures != 0 {
			t.Fatalf("capture ran after Stop: %d", captures)
		}
	})
}

func TestFirstScreenTimingFieldsCoverDistinctIntervals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			budget      = time.Second
			captureCost = 150 * time.Millisecond
		)
		captures := 0
		stop := watchFirstScreenWithStackWriter(budget, func(stack []byte, _ bool) int {
			captures++
			time.Sleep(captureCost)
			return copy(stack, "watchFirstScreen synthetic stack\n")
		})
		time.Sleep(2 * budget)
		synctest.Wait()
		sample := stop()
		// TriggerLag covers arming to callback entry and must stay exactly the
		// budget; StackDuration covers only the capture call and must equal the
		// synthetic capture cost, proving the fields measure distinct intervals.
		if !sample.Sampled {
			t.Fatalf("over-budget watcher was not sampled: %#v", sample)
		}
		if sample.TriggerLag != budget {
			t.Fatalf("trigger lag = %v, want exactly %v", sample.TriggerLag, budget)
		}
		if sample.StackDuration != captureCost {
			t.Fatalf("stack duration = %v, want exactly %v", sample.StackDuration, captureCost)
		}
		if captures != 1 {
			t.Fatalf("capture calls = %d, want 1", captures)
		}
		// Single shot: nothing may fire again after the joined sample.
		time.Sleep(2 * budget)
		synctest.Wait()
		if captures != 1 {
			t.Fatalf("capture fired again after join: %d", captures)
		}
	})
}
