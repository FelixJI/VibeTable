//go:build !race

package integration_test

// The normal CI suite retains the full capacity contract.
const formulaBackfillScaleRows = 10_000
