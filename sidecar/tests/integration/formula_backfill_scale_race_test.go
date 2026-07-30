//go:build race

package integration_test

// Race instrumentation makes each SQLite mutation substantially more
// expensive. One thousand rows still crosses multiple 100-row batches and
// exercises cancellation, resume, idempotency and audit de-duplication.
const formulaBackfillScaleRows = 1_000
