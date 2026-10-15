package main

import (
	"runtime"
	"time"
)

const firstScreenStackBytes = 64 << 10

// firstScreenSample is the diagnostic evidence gathered after the first-screen
// SLO window. Sampled is false when Stop won before the callback ran, which can
// also happen for an over-budget query whose timer fire was delayed, so false
// makes no claim about the query duration; the timing fields are then zero.
// TriggerLag is the wall clock from arming to the callback actually running.
// StackDuration covers only the stack capture call interval (runtime.Stack in
// production): buffer allocation and other callback overhead are excluded,
// while scheduler suspension during the call is included.
type firstScreenSample struct {
	Sampled       bool
	TriggerLag    time.Duration
	StackDuration time.Duration
	Stack         []byte
}

// Sampling starts only after the original SLO is already exceeded. Capture once,
// then join the callback outside the measured query so no sampler is left behind.
// Scheduler delays can postpone or prevent the sample; it is diagnostic evidence,
// not a replacement for the query's wall-clock measurement.
func watchFirstScreen(budget time.Duration) func() firstScreenSample {
	return watchFirstScreenWithStackWriter(budget, runtime.Stack)
}

// watchFirstScreenWithStackWriter is the sampling seam for tests; production
// always passes runtime.Stack via watchFirstScreen.
func watchFirstScreenWithStackWriter(
	budget time.Duration, writeStack func([]byte, bool) int,
) func() firstScreenSample {
	sample := make(chan firstScreenSample, 1)
	armed := time.Now()
	timer := time.AfterFunc(budget, func() {
		captured := firstScreenSample{Sampled: true, TriggerLag: time.Since(armed)}
		stack := make([]byte, firstScreenStackBytes)
		started := time.Now()
		written := writeStack(stack, true)
		captured.StackDuration = time.Since(started)
		captured.Stack = stack[:written]
		sample <- captured
	})
	return func() firstScreenSample {
		if timer.Stop() {
			return firstScreenSample{}
		}
		return <-sample
	}
}
