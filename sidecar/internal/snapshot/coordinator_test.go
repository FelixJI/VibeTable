package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

type fakeBarrier struct {
	view BarrierView
}

func (barrier fakeBarrier) Freeze(context.Context) (BarrierView, func(), error) {
	return barrier.view, func() {}, nil
}

func TestCapturePublishesOnlyVerifiedCompleteSnapshotAndDeduplicatesRevision(t *testing.T) {
	ctx := context.Background()
	authority := objectrepo.Authority{WorkspaceID: "workspace-1", FenceEpoch: 7, ClaimID: "claim-a"}
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	catalog := NewMemoryCatalog()
	coordinator := NewCoordinator(repository, fakeBarrier{view: BarrierView{
		MutationRevision: 11, SnapshotSequence: 1,
		SchemaRevision: 3, FileRevision: 8, AuditRevision: 13,
		AuditAnchor: digest([]byte("anchor")), Database: []byte("sqlite-view"),
		Files: map[string][]byte{"b.txt": []byte("b"), "a.txt": []byte("a")},
	}}, catalog)

	first, created, err := coordinator.Capture(ctx, CaptureRequest{
		WorkspaceID: "workspace-1", Authority: authority, Trigger: TriggerManual,
	})
	if err != nil || !created {
		t.Fatalf("capture failed: created=%v err=%v", created, err)
	}
	if !first.Pinned ||
		first.SealID == "" ||
		first.RootPinID == "" ||
		first.ObjectMap["database"] == "" ||
		first.ObjectMap["file-state-root"] == "" ||
		len(first.Objects) != 7 {
		t.Fatalf("unexpected record: %#v", first)
	}
	manifestRecord, err := repository.GetManifest(ctx, first.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestRecord.Payload, &manifest); err != nil ||
		manifest.BusinessDatabaseObjectID != first.ObjectMap["database"] ||
		manifest.FileStateRootObjectID != first.ObjectMap["file-state-root"] {
		t.Fatalf("manifest = %#v, %v", manifest, err)
	}
	sealRecord, err := repository.GetManifest(ctx, first.SealID)
	if err != nil {
		t.Fatal(err)
	}
	var seal Seal
	if err := json.Unmarshal(sealRecord.Payload, &seal); err != nil ||
		!seal.Verified ||
		seal.ManifestHash != digest(manifestRecord.Payload) {
		t.Fatalf("seal = %#v, %v", seal, err)
	}
	second, created, err := coordinator.Capture(ctx, CaptureRequest{
		WorkspaceID: "workspace-1", Authority: authority, Trigger: TriggerAutomatic,
	})
	if err != nil || created || second.SnapshotID != first.SnapshotID {
		t.Fatalf("same revision should reuse snapshot: %#v %v %v", second, created, err)
	}
}

func TestCatalogFailureNeverPublishesPartialRecord(t *testing.T) {
	ctx := context.Background()
	authority := objectrepo.Authority{WorkspaceID: "workspace-1", FenceEpoch: 1, ClaimID: "claim"}
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	catalog := NewMemoryCatalog().WithPublishError(errors.New("disk full"))
	coordinator := NewCoordinator(repository, fakeBarrier{view: BarrierView{
		MutationRevision: 1, SnapshotSequence: 1,
		AuditAnchor: digest([]byte("anchor")),
		Database:    []byte("db"), Files: map[string][]byte{},
	}}, catalog)
	if _, _, err := coordinator.Capture(ctx, CaptureRequest{
		WorkspaceID: "workspace-1", Authority: authority, Trigger: TriggerAutomatic,
	}); err == nil {
		t.Fatal("expected publish failure")
	}
	records, err := catalog.List(ctx, "workspace-1")
	if err != nil || len(records) != 0 {
		t.Fatalf("partial catalog publish: %#v %v", records, err)
	}
	pins, err := repository.ListPins(ctx)
	if err != nil || len(pins) != 0 {
		t.Fatalf("failed publication leaked root pin: %#v %v", pins, err)
	}
}

type sequenceBarrier struct {
	view BarrierView
}

func (barrier *sequenceBarrier) Freeze(context.Context) (BarrierView, func(), error) {
	barrier.view.SnapshotSequence++
	return barrier.view, func() {}, nil
}

func TestManualAndProtectionCapturesDoNotDeduplicateUnchangedMutation(t *testing.T) {
	ctx := context.Background()
	authority := objectrepo.Authority{
		WorkspaceID: "workspace-1", FenceEpoch: 1, ClaimID: "claim",
	}
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	barrier := &sequenceBarrier{view: BarrierView{
		MutationRevision: 1,
		AuditAnchor:      digest([]byte("anchor")),
		Database:         []byte("db"),
	}}
	catalog := NewMemoryCatalog()
	coordinator := NewCoordinator(repository, barrier, catalog)
	manual, created, err := coordinator.Capture(ctx, CaptureRequest{
		WorkspaceID: "workspace-1", Authority: authority, Trigger: TriggerManual,
	})
	if err != nil || !created {
		t.Fatalf("manual capture = %#v %v %v", manual, created, err)
	}
	protection, created, err := coordinator.Capture(ctx, CaptureRequest{
		WorkspaceID: "workspace-1", Authority: authority, Trigger: TriggerProtection,
	})
	if err != nil || !created ||
		protection.SnapshotID == manual.SnapshotID ||
		protection.SnapshotSequence != manual.SnapshotSequence+1 ||
		!protection.Pinned {
		t.Fatalf("protection capture = %#v %v %v", protection, created, err)
	}
}

func TestDurableCatalogPublishesAtomicallyAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshots", "catalog.db")
	catalog, err := OpenDurableCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		SnapshotID: "snapshot-1", WorkspaceID: "workspace-1",
		ManifestID: "manifest_snapshot", SealID: "manifest_seal",
		SnapshotSequence: 1, MutationRevision: 3,
		ObjectMap: map[string]objectrepo.ObjectID{"database": "obj_database"},
		RootPinID: "pin-1",
	}
	if err := catalog.Publish(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDurableCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	last, found, err := reopened.Last(context.Background(), "workspace-1")
	if err != nil || !found ||
		last.SnapshotID != record.SnapshotID ||
		last.ObjectMap["database"] != "obj_database" {
		t.Fatalf("reopened catalog = %#v %v %v", last, found, err)
	}
}

func TestStaleAuthorityCannotCapture(t *testing.T) {
	ctx := context.Background()
	current := objectrepo.Authority{WorkspaceID: "workspace-1", FenceEpoch: 2, ClaimID: "new"}
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(ctx, nil, current); err != nil {
		t.Fatal(err)
	}
	coordinator := NewCoordinator(repository, fakeBarrier{view: BarrierView{
		MutationRevision: 1, SnapshotSequence: 1,
		AuditAnchor: digest([]byte("anchor")), Database: []byte("db"),
	}}, NewMemoryCatalog())
	_, _, err := coordinator.Capture(ctx, CaptureRequest{
		WorkspaceID: "workspace-1",
		Authority:   objectrepo.Authority{WorkspaceID: "workspace-1", FenceEpoch: 1, ClaimID: "old"},
	})
	if !errors.Is(err, objectrepo.ErrStaleAuthority) {
		t.Fatalf("expected stale authority, got %v", err)
	}
}
