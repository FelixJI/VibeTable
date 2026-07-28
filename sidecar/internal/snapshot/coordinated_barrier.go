package snapshot

import (
	"context"
	"errors"

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

func (barrier *CoordinatedBarrier) Freeze(ctx context.Context) (BarrierView, func(), error) {
	var view BarrierView
	handle, err := barrier.coordinator.Capture(
		ctx,
		barrier.token,
		func(ctx context.Context, intent writecoordinator.CaptureIntent) (writecoordinator.FrozenRoots, error) {
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
	view.MutationRevision = handle.MutationRevision
	view.SnapshotSequence = handle.SnapshotSequence
	view.TopologyRoot = string(handle.TopologyRoot)
	view.FileRoot = string(handle.FileRoot)
	view.AuditAnchor = handle.AuditAnchor
	return view, func() {}, nil
}
