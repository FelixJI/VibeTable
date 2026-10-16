package snapshot

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type FrozenViewSource interface {
	Freeze(
		context.Context,
		writecoordinator.CaptureIntent,
	) (BarrierView, writecoordinator.FrozenRoots, error)
}

// CoordinatedBarrier captures every mutable source under the same
// WorkspaceWriteCoordinator gate used by authoritative mutations.
type CoordinatedBarrier struct {
	coordinator *writecoordinator.WorkspaceWriteCoordinator
	token       writecoordinator.Token
	source      FrozenViewSource
	logger      *slog.Logger
}

func NewCoordinatedBarrier(
	coordinator *writecoordinator.WorkspaceWriteCoordinator,
	token writecoordinator.Token,
	source FrozenViewSource,
) (*CoordinatedBarrier, error) {
	if coordinator == nil || source == nil {
		return nil, errors.New("snapshot.barrier_dependencies_required")
	}
	return &CoordinatedBarrier{coordinator: coordinator, token: token, source: source}, nil
}

// WithLogger records content-free write-gate phase diagnostics. A nil logger
// (the default) disables all phase logging without changing freeze behavior.
func (barrier *CoordinatedBarrier) WithLogger(logger *slog.Logger) *CoordinatedBarrier {
	barrier.logger = logger
	return barrier
}

// Freeze records the closed write-gate phase set when a logger is attached:
// gate waiting, gate acquired (callback entry, so the reported wait covers
// gate queuing plus the coordinator's capture-intent preparation) and freeze
// returned (source freeze plus capture persistence and gate release). Each
// duration clock starts after its boundary-entry log write completes and is
// read before the boundary-exit log is emitted, so entry-log cost never lands
// inside its own interval.
func (barrier *CoordinatedBarrier) Freeze(ctx context.Context) (BarrierView, func(), error) {
	var view BarrierView
	if barrier.logger != nil {
		barrier.logger.Info(
			"snapshot.capture.gate_waiting",
			slog.String("module", "snapshot"),
			slog.String("workspaceId", barrier.token.WorkspaceID),
			slog.String("operationId", operationIDFromContext(ctx)),
		)
	}
	gateStart := time.Now()
	var gateAcquiredAt time.Time
	var freezeStart time.Time
	handle, err := barrier.coordinator.Capture(
		ctx,
		barrier.token,
		func(ctx context.Context, intent writecoordinator.CaptureIntent) (writecoordinator.FrozenRoots, error) {
			gateAcquiredAt = time.Now()
			if barrier.logger != nil {
				barrier.logger.Info(
					"snapshot.capture.gate_acquired",
					slog.String("module", "snapshot"),
					slog.String("workspaceId", barrier.token.WorkspaceID),
					slog.String("operationId", operationIDFromContext(ctx)),
					slog.Float64(
						"durationMs",
						PhaseDurationMs(gateAcquiredAt.Sub(gateStart)),
					),
				)
			}
			// Clock for freeze_returned starts only after the gate_acquired log
			// write completes, so that write's cost stays outside its interval.
			freezeStart = time.Now()
			captured, roots, err := barrier.source.Freeze(ctx, intent)
			if err != nil {
				return writecoordinator.FrozenRoots{}, err
			}
			view = captured
			return roots, nil
		},
	)
	if err != nil {
		return BarrierView{}, func() {}, err
	}
	if barrier.logger != nil && !gateAcquiredAt.IsZero() {
		freezeElapsed := time.Since(freezeStart)
		barrier.logger.Info(
			"snapshot.capture.freeze_returned",
			slog.String("module", "snapshot"),
			slog.String("workspaceId", barrier.token.WorkspaceID),
			slog.String("operationId", operationIDFromContext(ctx)),
			slog.Float64("durationMs", PhaseDurationMs(freezeElapsed)),
		)
	}
	view.MutationRevision = handle.MutationRevision
	view.SnapshotSequence = handle.SnapshotSequence
	view.TopologyRoot = string(handle.TopologyRoot)
	view.FileRoot = string(handle.FileRoot)
	view.AuditAnchor = handle.AuditAnchor
	return view, func() {}, nil
}
