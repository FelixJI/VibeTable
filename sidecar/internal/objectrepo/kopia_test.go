package objectrepo

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	kopiamanifest "github.com/kopia/kopia/repo/manifest"
)

func kopiaStateManifestEntries(
	t *testing.T,
	ctx context.Context,
	repository WorkspaceRepository,
) []*kopiamanifest.EntryMetadata {
	t.Helper()
	adapter, ok := repository.(*KopiaRepository)
	if !ok {
		t.Fatalf("workspaceRepository built %T, want *KopiaRepository", repository)
	}
	entries, err := adapter.repository.FindManifests(ctx, map[string]string{
		"type": kopiaStateManifestType,
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// TestKopiaAcceptAuthoritySameLeaseLeavesDurableStateUnchanged proves the
// same-lease AcceptAuthority performed by every Runtime.Open is a durable
// no-op: the internal state manifest keeps its ID instead of being replaced
// by a byte-identical rewrite. The previous implementation always went
// through publishState (writer manifest scan plus flush) even when current
// and next authorities matched, so this exact assertion failed on it.
func TestKopiaAcceptAuthoritySameLeaseLeavesDurableStateUnchanged(t *testing.T) {
	ctx := context.Background()
	repository, authority := workspaceRepository(t)
	before := kopiaStateManifestEntries(t, ctx, repository)
	if len(before) != 1 {
		t.Fatalf("state manifests = %d, want exactly 1", len(before))
	}
	if err := repository.AcceptAuthority(ctx, &authority, authority); err != nil {
		t.Fatal(err)
	}
	after := kopiaStateManifestEntries(t, ctx, repository)
	if len(after) != 1 {
		t.Fatalf(
			"same-lease accept changed the state manifest count: before=%d after=%d",
			len(before), len(after),
		)
	}
	if after[0].ID != before[0].ID {
		t.Fatalf(
			"same-lease accept rewrote durable state: manifest %v -> %v",
			before[0].ID, after[0].ID,
		)
	}
}

// TestKopiaCommitOwnsManifestPayloadBytes proves the dropped defensive
// payload copy in Commit is safe: kopia's manifest manager synchronously
// encodes the payload into its own pendingEntry bytes, so mutating the
// caller's slice after Commit cannot change the durable manifest, and the
// stored record still attests under VerifyManifestRecord.
func TestKopiaCommitOwnsManifestPayloadBytes(t *testing.T) {
	ctx := context.Background()
	repository, authority := workspaceRepository(t)
	payload := []byte(`{"seed":"original"}`)
	receipt, err := repository.Commit(ctx, CommitRequest{
		Authority: authority,
		Manifests: []ManifestInput{{
			Name: "payload-ownership",
			Labels: map[string]string{
				"type": "payload-ownership", "workspaceId": authority.WorkspaceID,
			},
			Payload: payload,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	copy(payload, []byte(`{"seed":"mutated`))
	id := receipt.Manifests["payload-ownership"]
	record, err := repository.GetManifest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if string(record.Payload) != `{"seed":"original"}` {
		t.Fatalf("durable payload changed after caller mutation: %s", record.Payload)
	}
	if err := VerifyManifestRecord(record); err != nil {
		t.Fatalf("mutated-caller record no longer attests: %v", err)
	}
}

// TestKopiaAcceptAuthorityStillRejectsAfterAnotherHandleAdvanced keeps the
// lease contract intact now that the same-lease path returns early: a second
// repository handle may escalate the fence, and the first handle's stale
// re-acceptance with its old expected authority must still fail closed.
func TestKopiaAcceptAuthorityStillRejectsAfterAnotherHandleAdvanced(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	spec := WorkspaceRepositorySpec{
		Format:           workspaceRepositoryFormat,
		CoordinationRoot: filepath.Join(root, "coordination"),
		ObjectsRoot:      filepath.Join(root, "objects"),
		Password:         []byte("same-lease-contract-password"),
	}
	first, created, err := OpenOrCreateWorkspaceRepository(ctx, spec)
	if err != nil || !created {
		t.Fatalf("create = %v created=%v", err, created)
	}
	t.Cleanup(func() { _ = first.Close(ctx) })
	original := Authority{
		WorkspaceID: "same-lease-contract-workspace",
		FenceEpoch:  1,
		ClaimID:     "claim-one",
	}
	if err := first.AcceptAuthority(ctx, nil, original); err != nil {
		t.Fatal(err)
	}
	second, reopened, err := OpenOrCreateWorkspaceRepository(ctx, spec)
	if err != nil || reopened {
		t.Fatalf("reopen = %v reopened=%v", err, reopened)
	}
	t.Cleanup(func() { _ = second.Close(ctx) })
	advanced := Authority{
		WorkspaceID: original.WorkspaceID,
		FenceEpoch:  original.FenceEpoch + 1,
		ClaimID:     "claim-two",
	}
	if err := second.AcceptAuthority(ctx, &original, advanced); err != nil {
		t.Fatal(err)
	}
	if err := first.AcceptAuthority(ctx, &original, original); !errors.Is(
		err, ErrStaleAuthority,
	) {
		t.Fatalf("stale same-lease re-acceptance error = %v", err)
	}
}
