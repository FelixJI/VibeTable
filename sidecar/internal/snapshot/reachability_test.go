package snapshot

import (
	"context"
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

func TestReachabilityObjectIDsRejectsSnapshotWithoutTypedFileStateRoot(
	t *testing.T,
) {
	record := Record{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		Objects:     []objectrepo.ObjectID{"obj_database"},
		ObjectMap: map[string]objectrepo.ObjectID{
			"database": "obj_database",
		},
	}

	_, err := ReachabilityObjectIDs(
		context.Background(),
		objectrepo.NewMemory(),
		record,
	)
	if !errors.Is(err, ErrBundleInvalid) {
		t.Fatalf("rootless snapshot error = %v", err)
	}
}
