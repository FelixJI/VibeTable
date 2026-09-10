package workspacev2

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/retention"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
)

func TestRetentionSeparatesSnapshotsWithSharedFileRoot(t *testing.T) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: dataDir, HideStartBanner: true})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	defer app.ResetBootstrapState()
	ledger, err := auditledger.Open(filepath.Join(root, ".vibetable", "audit"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir, WorkspaceID: testWorkspaceID,
		SessionEpoch: 7, FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		DeferBackgroundWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	now := time.Now().UTC()
	installRetentionTestClock(runtime, &now)
	capture := func() snapshot.Record {
		t.Helper()
		token, _ := runtime.coordinator.Current()
		record, created, err := runtime.snapshots.Capture(ctx, snapshot.CaptureRequest{
			WorkspaceID: testWorkspaceID, Authority: token.Authority(), Trigger: snapshot.TriggerManual,
		})
		if err != nil || !created {
			t.Fatalf("capture = %#v, %v, %v", record, created, err)
		}
		return record
	}
	old := capture()
	// Change only database content; both snapshots must share the immutable file root.
	if _, err := app.DB().NewQuery("CREATE TABLE retention_fixture (value TEXT)").Execute(); err != nil {
		t.Fatal(err)
	}
	latest := capture()
	if old.ObjectMap["file-state-root"] != latest.ObjectMap["file-state-root"] ||
		old.ObjectMap["database"] == latest.ObjectMap["database"] {
		t.Fatal("fixture must share file root but have distinct database objects")
	}
	unpin := dispatch(t, runtime, 1, "snapshot.update", fmt.Sprintf(
		`{"snapshotId":%q,"action":"unpin","expectedCatalogRevision":%d}`,
		old.SnapshotID, old.CatalogRevision,
	))
	if unpin.Error != nil {
		t.Fatalf("unpin old snapshot: %#v", unpin.Error)
	}
	// Advance only this unit fixture's injected clock beyond the reader-pin lifetime.
	now = now.Add(25 * time.Hour)
	policy := retention.Policy{MinimumRecent: 1, TrashGrace: 90 * 24 * time.Hour}
	// A live restore plan protects every object of its source snapshot.
    if _, err := runtime.state.db.ExecContext(ctx, `INSERT INTO snapshot_restore_plans
        (plan_id, snapshot_id, catalog_revision, mutation_revision, diff_hash, target_mode, expires_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`, "fixture-restore", old.SnapshotID,
        old.CatalogRevision, 1, "fixture", "current", now.Add(time.Hour).Format(time.RFC3339Nano)); err != nil { t.Fatal(err) }
    protected, err := runtime.retention.production.Plan(ctx, policy)
    if err != nil || protected.ReclaimableBytes != 0 { t.Fatalf("pending restore lost source closure: %#v, %v", protected, err) }
    now = now.Add(2 * time.Hour)
    plan, err := runtime.retention.production.Plan(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ReclaimableBytes == 0 {
		t.Fatal("old snapshot exclusive objects remain reachable through the shared file root")
	}
	token, _ := runtime.coordinator.Current()
	_, err = runtime.retention.production.Apply(ctx, plan.PlanID, retention.MutationIdentity{
		WorkspaceID: testWorkspaceID, MutationRevision: 2,
		SessionEpoch: token.SessionEpoch, FenceEpoch: token.FenceEpoch, ClaimID: testClaimID,
	})
	if err != nil {
		t.Fatal(err)
	}
	tombstoned, err := runtime.retention.store.TombstonedSnapshotIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := tombstoned[old.SnapshotID]; !found {
		t.Fatal("partly reclaimed old snapshot is still visible")
	}
	if _, found := tombstoned[latest.SnapshotID]; found {
		t.Fatal("retained latest snapshot was tombstoned")
	}
	if err := snapshot.ValidateSnapshotBundle(ctx, runtime.repository, latest); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.retention.production.Plan(ctx, policy); err != nil {
		t.Fatalf("next inventory after partial snapshot retirement: %v", err)
	}
}
