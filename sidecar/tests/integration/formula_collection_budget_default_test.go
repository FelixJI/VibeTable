//go:build !race

package integration_test

import "github.com/vibetable/vibetable/sidecar/internal/formula"

// Normal CI exercises the full collection fixtures with the production budget.
const collectionTestEvalTimeout = formula.DefaultEvalTimeout
