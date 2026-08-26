package snapshot

import (
	"context"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type fakeFrozenSource struct{}

func (fakeFrozenSource) Freeze(
	_ context.Context,
	intent writecoordinator.CaptureIntent,
) (BarrierView, writecoordinator.FrozenRoots, error) {
	return BarrierView{
		BusinessSchemaVersion: 1,
		Database:              []byte("db"),
		Files:                 map[string][]byte{"file": []byte("content")},
	}, writecoordinator.FrozenRoots{
		DatabaseView: "fixed-read-view",
		TopologyRoot: objectrepo.ManifestID("manifest_topology"),
		FileRoot:     objectrepo.ManifestID("manifest_files"),
		AuditAnchor:  digest([]byte("audit")),
	}, nil
}

func TestCoordinatedBarrierUsesSharedMutationCountersAndRoots(t *testing.T) {
	coordinator, err := writecoordinator.New("workspace", 3, "claim", 9)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := coordinator.Current()
	if _, err := coordinator.Write(context.Background(), token, func(
		context.Context,
		writecoordinator.WriteIntent,
	) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	barrier, err := NewCoordinatedBarrier(coordinator, token, fakeFrozenSource{})
	if err != nil {
		t.Fatal(err)
	}
	view, release, err := barrier.Freeze(context.Background())
	defer release()
	if err != nil {
		t.Fatal(err)
	}
	if view.MutationRevision != 1 || view.SnapshotSequence != 1 ||
		view.TopologyRoot != "manifest_topology" ||
		view.FileRoot != "manifest_files" ||
		view.AuditAnchor != digest([]byte("audit")) {
		t.Fatalf("inconsistent coordinated barrier: %#v", view)
	}
}
