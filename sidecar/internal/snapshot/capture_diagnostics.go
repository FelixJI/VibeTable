package snapshot

import (
	"context"
	"errors"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

// CaptureErrorCode maps a capture failure onto the closed diagnostics code
// set: only the two named context outcomes are distinguishable, every other
// error collapses to one fixed code. Error text is deliberately never echoed
// or parsed — arbitrary producers can embed dotted identifiers, storage
// messages or file system paths that must not reach diagnostics.
func CaptureErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "context.canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "context.deadline_exceeded"
	}
	return "snapshot.capture_failed"
}

// PhaseDurationMs converts a measured phase interval to the diagnostics
// durationMs unit. Callers must start the clock after their boundary-entry log
// write completes and read the value before emitting the boundary-exit log.
func PhaseDurationMs(spent time.Duration) float64 {
	return float64(spent.Nanoseconds()) / float64(time.Millisecond)
}

// operationIDFromContext surfaces the stable dispatcher operation identity for
// phase correlation. Captures that bypass Dispatch (for example the idle
// scheduler) return an empty string, which the diagnostics envelope drops.
func operationIDFromContext(ctx context.Context) string {
	if operation, dispatched := protocolv2.OperationFromContext(ctx); dispatched {
		return operation.OperationID
	}
	return ""
}
