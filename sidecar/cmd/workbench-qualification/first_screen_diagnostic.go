package main

import (
	"runtime"
	"time"
)

const firstScreenStackBytes = 64 << 10

// Sampling starts only after the original SLO is already exceeded. Capture once,
// then join the callback outside the measured query so no sampler is left behind.
// Scheduler delays can postpone or prevent the sample; it is diagnostic evidence,
// not a replacement for the query's wall-clock measurement.
func watchFirstScreen(budget time.Duration) func() []byte {
	sample := make(chan []byte, 1)
	timer := time.AfterFunc(budget, func() {
		stack := make([]byte, firstScreenStackBytes)
		sample <- stack[:runtime.Stack(stack, true)]
	})
	return func() []byte {
		if timer.Stop() {
			return nil
		}
		return <-sample
	}
}
