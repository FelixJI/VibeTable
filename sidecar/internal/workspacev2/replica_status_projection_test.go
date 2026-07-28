package workspacev2

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestReplicaStatusProjectionSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workspace-v2.db")
	store, err := openStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	updatedAt := time.Date(
		2026, 7, 28, 13, 0, 0, 0, time.UTC,
	)
	if err := store.putReplicaStatus(
		ctx,
		replicaStatusProjection{
			CoordinationStrength: "advisory",
			SyncState:            "failed",
			PendingSync:          true,
			UpdatedAt:            updatedAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	status, found, err := reopened.replicaStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !found ||
		status.CoordinationStrength != "advisory" ||
		status.SyncState != "failed" ||
		!status.PendingSync ||
		!status.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("reopened replica status = %#v, found=%v", status, found)
	}
	if err := reopened.putReplicaStatus(
		ctx,
		replicaStatusProjection{
			CoordinationStrength: "advisory",
			SyncState:            "unknown",
			UpdatedAt:            time.Now().UTC(),
		},
	); err == nil {
		t.Fatal("invalid replica status was persisted")
	}
}
