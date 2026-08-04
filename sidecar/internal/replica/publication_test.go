package replica

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func advisoryPublication(
	t *testing.T,
	claimID, snapshotID string,
	revision, fence uint64,
	previous string,
) Publication {
	t.Helper()
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC).Add(time.Duration(revision) * time.Minute)
	sealed, err := SealPublication(Publication{
		WorkspaceID: "workspace-1",
		Claim: Claim{
			WorkspaceID: "workspace-1",
			DeviceID:    "device-1",
			ClaimID:     claimID,
			FenceEpoch:  fence,
			Nonce:       "sha256:" + strings.Repeat(string(rune('a'+revision%5)), 64),
			Strength:    Advisory,
			Mode:        Provisional,
			IssuedAt:    now.Add(-time.Minute),
			HeartbeatAt: now.Add(-time.Minute),
			ExpiresAt:   now.Add(time.Hour),
		},
		PreviousPublicationHash: previous,
		SnapshotID:              snapshotID,
		CatalogRevision:         revision,
		CheckpointID:            "sha256:" + strings.Repeat(string(rune('a'+revision%5)), 64),
		CreatedAt:               now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestAdvisoryDAGDetectsCorruptionAndMissingParent(t *testing.T) {
	dag, err := NewAdvisoryDAG("workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	root := advisoryPublication(t, "claim-1", "snapshot-1", 1, 1, "")
	if err := dag.Publish(root); err != nil {
		t.Fatal(err)
	}

	tampered := root
	tampered.SnapshotID = "changed"
	if err := dag.Publish(tampered); !errors.Is(err, ErrPublicationTampered) {
		t.Fatalf("tampered publication error = %v", err)
	}
	child := advisoryPublication(
		t, "claim-2", "snapshot-2", 2, 2,
		"sha256:"+strings.Repeat("f", 64),
	)
	if err := dag.Publish(child); !errors.Is(err, ErrParentMissing) {
		t.Fatalf("missing parent error = %v", err)
	}
}

func TestAdvisoryDAGKeepsConcurrentImmutableHeads(t *testing.T) {
	dag, err := NewAdvisoryDAG("workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	root := advisoryPublication(t, "claim-1", "snapshot-1", 1, 1, "")
	left := advisoryPublication(t, "claim-2", "snapshot-2", 2, 2, root.CanonicalHash)
	right := advisoryPublication(t, "claim-3", "snapshot-3", 3, 3, root.CanonicalHash)
	for _, publication := range []Publication{root, left, right} {
		if err := dag.Publish(publication); err != nil {
			t.Fatal(err)
		}
	}
	heads, err := dag.Heads()
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 2 || heads[0].CanonicalHash != right.CanonicalHash {
		t.Fatalf("heads = %#v", heads)
	}
}

func TestSealPublicationIsDeterministic(t *testing.T) {
	publication := advisoryPublication(t, "claim-1", "snapshot-1", 1, 1, "")
	resealed, err := SealPublication(Publication{
		PublicationID:           publication.PublicationID,
		WorkspaceID:             publication.WorkspaceID,
		Claim:                   publication.Claim,
		PreviousPublicationHash: publication.PreviousPublicationHash,
		SnapshotID:              publication.SnapshotID,
		CatalogRevision:         publication.CatalogRevision,
		CheckpointID:            publication.CheckpointID,
		CreatedAt:               publication.CreatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resealed.CanonicalHash != publication.CanonicalHash {
		t.Fatalf("hash changed: %s != %s", resealed.CanonicalHash, publication.CanonicalHash)
	}
}
