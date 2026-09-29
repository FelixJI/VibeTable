//go:build race

package integration_test

import "time"

// Race instrumentation typically costs 2-20x runtime (Go race detector docs).
// Keep all correctness assertions, with a bounded 1s instrumentation budget.
// The formula package tests the actual 250ms deadline under race separately.
const collectionTestEvalTimeout = time.Second
