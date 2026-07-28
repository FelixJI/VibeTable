package replica

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func publicationKey() []byte {
	return bytes.Repeat([]byte{0x5a}, minimumPublicationKeyBytes)
}

func testPublication(
	t *testing.T,
	id string,
	device string,
	snapshot string,
	previous string,
	now time.Time,
) Publication {
	t.Helper()
	checkpointID := "sha256:" + strings.Repeat("a", 64)
	sealed, err := SealPublication(Publication{
		WorkspaceID: "workspace-1",
		Claim: testClaim(
			"workspace-1",
			device,
			id,
			checkpointID,
			Advisory,
			now,
		),
		PreviousPublicationHash: previous,
		SnapshotID:              snapshot,
		CatalogRevision:         1,
		CheckpointID:            checkpointID,
		CreatedAt:               now,
	}, publicationKey())
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestAdvisoryPublicationIsAuthenticatedImmutableAndDeterministic(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	dag, err := NewAdvisoryDAG("workspace-1", publicationKey())
	if err != nil {
		t.Fatal(err)
	}
	left := testPublication(t, "left", "a", "snapshot-a", "", now)
	right := testPublication(t, "right", "b", "snapshot-b", "", now)
	if err := dag.Publish(left); err != nil {
		t.Fatal(err)
	}
	if err := dag.Publish(right); err != nil {
		t.Fatal(err)
	}
	if err := dag.Publish(left); err != nil {
		t.Fatalf("exact retry must be idempotent: %v", err)
	}
	replacement := testPublication(
		t, "left", "a", "snapshot-a", "", now.Add(time.Second),
	)
	if err := dag.Publish(replacement); !errors.Is(err, ErrPublicationExists) {
		t.Fatalf("immutable publication was overwritten: %v", err)
	}
	winner, losers, ok, err := dag.Winner()
	if err != nil || !ok || len(losers) != 1 {
		t.Fatalf("winner = %#v losers=%#v ok=%v err=%v", winner, losers, ok, err)
	}
	wantWinner := left
	if right.CanonicalHash < left.CanonicalHash {
		wantWinner = right
	}
	if winner.CanonicalHash != wantWinner.CanonicalHash {
		t.Fatalf("winner = %s, want %s", winner.CanonicalHash, wantWinner.CanonicalHash)
	}
}

func TestAdvisoryPublicationRejectsTamperingWorkspaceDeviceAndMissingParent(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	dag, err := NewAdvisoryDAG("workspace-1", publicationKey())
	if err != nil {
		t.Fatal(err)
	}
	valid := testPublication(t, "valid", "a", "snapshot-a", "", now)
	tamperedDevice := valid
	tamperedDevice.Claim.DeviceID = "other-device"
	if err := dag.Publish(tamperedDevice); !errors.Is(err, ErrPublicationTampered) {
		t.Fatalf("device tampering accepted: %v", err)
	}
	wrongWorkspace := valid
	wrongWorkspace.WorkspaceID = "workspace-2"
	if err := dag.Publish(wrongWorkspace); !errors.Is(err, ErrWorkspaceMismatch) {
		t.Fatalf("wrong workspace accepted: %v", err)
	}
	missingParent := testPublication(
		t,
		"child",
		"a",
		"snapshot-child",
		"sha256:"+string(bytes.Repeat([]byte{'a'}, 64)),
		now.Add(time.Second),
	)
	if err := dag.Publish(missingParent); !errors.Is(err, ErrParentMissing) {
		t.Fatalf("missing parent accepted: %v", err)
	}
}

func TestAdvisoryPublicationPreviousHashFormsImmutableDAG(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	dag, err := NewAdvisoryDAG("workspace-1", publicationKey())
	if err != nil {
		t.Fatal(err)
	}
	parent := testPublication(t, "parent", "a", "snapshot-a", "", now)
	child := testPublication(
		t,
		"child",
		"a",
		"snapshot-b",
		parent.CanonicalHash,
		now.Add(time.Second),
	)
	if err := dag.Publish(parent); err != nil {
		t.Fatal(err)
	}
	if err := dag.Publish(child); err != nil {
		t.Fatal(err)
	}
	heads, err := dag.Heads()
	if err != nil || len(heads) != 1 ||
		heads[0].CanonicalHash != child.CanonicalHash {
		t.Fatalf("heads = %#v, %v", heads, err)
	}
}

func TestPersistentAdvisoryPublicationReopensAndDetectsDiskTampering(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	path := sqliteTestPath(t, "publications.db")
	dag, err := OpenPersistentAdvisoryDAG(
		path, "workspace-1", publicationKey(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer dag.Close()
	publication := testPublication(t, "p1", "a", "snapshot-a", "", now)
	if err := dag.Publish(publication); err != nil {
		t.Fatal(err)
	}
	if err := dag.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPersistentAdvisoryDAG(
		path, "workspace-1", publicationKey(),
	)
	if err != nil {
		t.Fatal(err)
	}
	heads, err := reopened.Heads()
	if err != nil || len(heads) != 1 ||
		heads[0].CanonicalHash != publication.CanonicalHash {
		t.Fatalf("reopened heads = %#v, %v", heads, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(func() Publication {
		copy := publication
		copy.SnapshotID = "tampered"
		return copy
	}())
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`UPDATE advisory_publications SET publication_json = ?
		 WHERE workspace_id = ? AND publication_id = ?`,
		raw, publication.WorkspaceID, publication.PublicationID,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	tampered, err := OpenPersistentAdvisoryDAG(
		path, "workspace-1", publicationKey(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer tampered.Close()
	if _, err := tampered.Heads(); !errors.Is(err, ErrPublicationTampered) {
		t.Fatalf("disk tampering was not detected: %v", err)
	}
}

func TestPersistentAdvisoryConditionalCreateCannotRaceOverwrite(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	path := sqliteTestPath(t, "publications.db")
	left, err := OpenPersistentAdvisoryDAG(
		path, "workspace-1", publicationKey(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := OpenPersistentAdvisoryDAG(
		path, "workspace-1", publicationKey(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, candidate := range []struct {
		dag         *AdvisoryDAG
		publication Publication
	}{
		{left, testPublication(t, "same-id", "a", "snapshot-a", "", now)},
		{right, testPublication(t, "same-id", "b", "snapshot-a", "", now)},
	} {
		wait.Add(1)
		go func(candidate struct {
			dag         *AdvisoryDAG
			publication Publication
		}) {
			defer wait.Done()
			<-start
			results <- candidate.dag.PublishContext(
				context.Background(), candidate.publication,
			)
		}(candidate)
	}
	close(start)
	wait.Wait()
	close(results)
	var succeeded, rejected int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrPublicationExists):
			rejected++
		default:
			t.Fatalf("unexpected publish error: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded=%d rejected=%d", succeeded, rejected)
	}
}
