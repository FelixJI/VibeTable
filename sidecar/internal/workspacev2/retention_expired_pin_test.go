package workspacev2

import (
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

func TestActiveRetentionPinRootsExcludesExpiredPins(t *testing.T) {
	now := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Nanosecond)
	permanentRoot := objectrepo.ObjectID("permanent-root")
	futureRoot := objectrepo.ObjectID("future-root")
	exactExpiryRoot := objectrepo.ObjectID("exact-expiry-root")
	pastRoot := objectrepo.ObjectID("past-root")
	foreignRoot := objectrepo.ObjectID("foreign-root")

	roots, foreignPin := activeRetentionPinRoots(
		[]objectrepo.RootPin{
			{WorkspaceID: testWorkspaceID, Roots: []objectrepo.ObjectID{permanentRoot}},
			{
				WorkspaceID: testWorkspaceID,
				Roots:       []objectrepo.ObjectID{futureRoot},
				ExpiresAt:   &future,
			},
			{
				WorkspaceID: testWorkspaceID,
				Roots:       []objectrepo.ObjectID{exactExpiryRoot},
				ExpiresAt:   &now,
			},
			{
				WorkspaceID: testWorkspaceID,
				Roots:       []objectrepo.ObjectID{pastRoot},
				ExpiresAt:   &past,
			},
			{
				WorkspaceID: "22222222-2222-4222-8222-222222222222",
				Roots:       []objectrepo.ObjectID{foreignRoot},
				ExpiresAt:   &past,
			},
		},
		testWorkspaceID,
		now,
	)

	if !foreignPin {
		t.Fatal("foreign workspace pin did not fail closed")
	}
	if len(roots) != 2 || roots[0] != permanentRoot || roots[1] != futureRoot {
		t.Fatalf("active roots = %#v", roots)
	}
}
