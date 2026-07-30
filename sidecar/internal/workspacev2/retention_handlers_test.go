package workspacev2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/retention"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func TestFileHistoryRetentionRootsPreserveEntirePublishedRootClosure(
	t *testing.T,
) {
	t.Parallel()
	parent := "parent"
	branchPoint := "branch-point"
	restoreSource := "restore-source"
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	documents := []filehistory.Document{{
		DocumentID:          "document",
		EffectiveRevisionID: "effective",
		Status:              filehistory.DocumentActive,
		Revisions: []filehistory.Revision{
			{
				RevisionID: "parent",
				ObjectID:   "obj_parent",
				CreatedAt:  now.Add(-200 * 24 * time.Hour),
			},
			{
				RevisionID:       "branch-point",
				ParentRevisionID: &parent,
				ObjectID:         "obj_branch",
				CreatedAt:        now.Add(-190 * 24 * time.Hour),
			},
			{
				RevisionID:       "formal",
				ParentRevisionID: &branchPoint,
				FormalVersion:    uint64PointerForRetentionTest(1),
				ObjectID:         "obj_formal",
				CreatedAt:        now.Add(-180 * 24 * time.Hour),
			},
			{
				RevisionID:       "restore-source",
				ParentRevisionID: &branchPoint,
				ObjectID:         "obj_restore_source",
				CreatedAt:        now.Add(-170 * 24 * time.Hour),
			},
			{
				RevisionID:             "effective",
				ParentRevisionID:       &restoreSource,
				RestoredFromRevisionID: &restoreSource,
				ObjectID:               "obj_effective",
				CreatedAt:              now.Add(-160 * 24 * time.Hour),
			},
		},
	}}
	roots, err := fileHistoryRetentionRoots(
		documents,
		RetentionPolicy{},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []objectrepo.ObjectID{
		"obj_branch",
		"obj_effective",
		"obj_formal",
		"obj_parent",
		"obj_restore_source",
	}
	if len(roots) != len(want) {
		t.Fatalf("roots = %#v, want %#v", roots, want)
	}
	for index := range want {
		if roots[index] != want[index] {
			t.Fatalf("roots = %#v, want %#v", roots, want)
		}
	}
}

func TestDeletedDocumentFailsSafeByRetainingEveryRevision(t *testing.T) {
	t.Parallel()
	parent := "old"
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	roots, err := fileHistoryRetentionRoots(
		[]filehistory.Document{{
			DocumentID:          "document",
			EffectiveRevisionID: "new",
			Status:              filehistory.DocumentDeleted,
			Revisions: []filehistory.Revision{
				{
					RevisionID: "old",
					ObjectID:   "obj_old",
					CreatedAt:  now.Add(-400 * 24 * time.Hour),
				},
				{
					RevisionID:       "new",
					ParentRevisionID: &parent,
					ObjectID:         "obj_new",
					CreatedAt:        now.Add(-399 * 24 * time.Hour),
				},
			},
		}},
		RetentionPolicy{},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 ||
		roots[0] != "obj_new" ||
		roots[1] != "obj_old" {
		t.Fatalf("deleted document roots = %#v", roots)
	}
}

func TestRetentionPolicyMapsOnlyEnabledSnapshotBuckets(t *testing.T) {
	t.Parallel()
	converted := retentionPolicy(RetentionPolicy{
		SnapshotDays:  30,
		SnapshotCount: 50,
		SnapshotBuckets: []string{
			"daily",
			"monthly",
		},
		TrashMonths: 3,
	})
	if converted.KeepHourlyFor != 0 ||
		converted.KeepWeeklyFor != 0 ||
		converted.KeepDailyFor != 30*24*time.Hour ||
		converted.KeepMonthlyFor != 30*24*time.Hour ||
		converted.TrashGrace != 90*24*time.Hour ||
		converted.MinimumRecent != 50 {
		t.Fatalf("converted policy = %#v", converted)
	}
}

func TestBackgroundRetentionPersistsReportsAndClearsMaintenanceFailures(
	t *testing.T,
) {
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "retention.db")
	composition, err := openProductionRetentionStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	integrityFailure := errors.New("injected background integrity failure")
	sweepFailure := errors.New("injected background sweep failure")
	var (
		logs       []string
		sweepCalls int
	)
	composition.logf = func(format string, arguments ...any) {
		logs = append(logs, fmt.Sprintf(format, arguments...))
	}
	composition.backgroundIntegrity = func(
		context.Context,
		time.Time,
	) error {
		return integrityFailure
	}
	composition.backgroundSweep = func(
		context.Context,
	) (retention.MaintenanceResult, error) {
		sweepCalls++
		return retention.MaintenanceResult{}, nil
	}
	composition.runBackgroundMaintenance(ctx, now)
	if sweepCalls != 0 {
		t.Fatal("sweep ran after background integrity failure")
	}
	state, err := composition.store.MaintenanceState(ctx)
	if err != nil ||
		state.Failure != integrityFailure.Error() ||
		state.Stage != retention.MaintenanceIntegrity ||
		state.FailedAt == nil ||
		!state.FailedAt.Equal(now) {
		t.Fatalf("integrity maintenance state = %#v, %v", state, err)
	}
	if err := composition.close(); err != nil {
		t.Fatal(err)
	}

	composition, err = openProductionRetentionStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer composition.close()
	state, err = composition.store.MaintenanceState(ctx)
	if err != nil ||
		state.Failure != integrityFailure.Error() ||
		state.Stage != retention.MaintenanceIntegrity {
		t.Fatalf("reopened maintenance state = %#v, %v", state, err)
	}
	composition.logf = func(format string, arguments ...any) {
		logs = append(logs, fmt.Sprintf(format, arguments...))
	}
	composition.backgroundIntegrity = func(
		context.Context,
		time.Time,
	) error {
		return nil
	}
	composition.backgroundSweep = func(
		context.Context,
	) (retention.MaintenanceResult, error) {
		sweepCalls++
		return retention.MaintenanceResult{}, sweepFailure
	}
	composition.runBackgroundMaintenance(ctx, now.Add(time.Hour))
	state, err = composition.store.MaintenanceState(ctx)
	if err != nil ||
		state.Failure != sweepFailure.Error() ||
		state.Stage != retention.MaintenanceSweep {
		t.Fatalf("sweep maintenance state = %#v, %v", state, err)
	}

	composition.backgroundSweep = func(
		context.Context,
	) (retention.MaintenanceResult, error) {
		sweepCalls++
		return retention.MaintenanceResult{}, nil
	}
	composition.runBackgroundMaintenance(ctx, now.Add(2*time.Hour))
	state, err = composition.store.MaintenanceState(ctx)
	if err != nil ||
		state.Failure != "" ||
		state.Stage != "" ||
		state.FailedAt != nil {
		t.Fatalf("cleared maintenance state = %#v, %v", state, err)
	}
	if len(logs) != 2 ||
		!strings.Contains(logs[0], integrityFailure.Error()) ||
		!strings.Contains(logs[1], sweepFailure.Error()) {
		t.Fatalf("background maintenance logs = %#v", logs)
	}
}

func TestProductionRetentionHandlersUseRepositoryInventoryAndCoordinator(
	t *testing.T,
) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  dataDir,
		HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	defer app.ResetBootstrapState()
	ledger, err := auditledger.Open(
		filepath.Join(root, ".vibetable", "audit"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	token, _ := runtime.coordinator.Current()
	receipt, err := runtime.repository.Commit(
		ctx,
		objectrepo.CommitRequest{
			Authority: token.Authority(),
			Objects: []objectrepo.ObjectInput{{
				Name: "unreachable",
				Content: []byte(
					"retention-production-unreachable",
				),
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	garbage := receipt.Objects["unreachable"]
	preview := dispatch(t, runtime, 1, "retention.plan", `{}`)
	if preview.Error != nil {
		t.Fatalf("retention.plan error = %#v", preview.Error)
	}
	plan := preview.Result.(map[string]any)
	planID, ok := plan["planId"].(string)
	if !ok || !validUUID(planID) ||
		plan["reclaimableBytes"].(int64) !=
			int64(len("retention-production-unreachable")) {
		t.Fatalf("retention.plan result = %#v", plan)
	}
	if _, err := runtime.state.db.ExecContext(
		ctx,
		`DELETE FROM rpc_operation_receipts
		  WHERE workspace_id = ? AND operation_id = ?`,
		testWorkspaceID,
		retentionOperationID(1),
	); err != nil {
		t.Fatal(err)
	}
	previewReplay := runtime.Dispatcher().DispatchEnvelope(
		ctx,
		retentionRequestJSON(t, 2, 1, "retention.plan", `{}`),
	)
	if previewReplay.Error != nil ||
		previewReplay.Result.(map[string]any)["planId"] != planID {
		t.Fatalf("authority plan receipt replay = %#v", previewReplay)
	}
	applied := dispatch(
		t,
		runtime,
		3,
		"retention.apply",
		`{"planId":"`+planID+`"}`,
	)
	if applied.Error != nil {
		t.Fatalf("retention.apply error = %#v", applied.Error)
	}
	result := applied.Result.(map[string]any)
	if result["deletedObjects"].(int) != 1 ||
		result["reclaimedBytes"].(int64) != 0 {
		t.Fatalf("retention.apply result = %#v", result)
	}
	_, counters := runtime.coordinator.Current()
	if counters.MutationRevision != 1 {
		t.Fatalf("retention apply bypassed coordinator: %#v", counters)
	}
	if _, err := runtime.state.db.ExecContext(
		ctx,
		`DELETE FROM rpc_operation_receipts
		  WHERE workspace_id = ? AND operation_id = ?`,
		testWorkspaceID,
		retentionOperationID(3),
	); err != nil {
		t.Fatal(err)
	}
	applyReplay := runtime.Dispatcher().DispatchEnvelope(
		ctx,
		retentionRequestJSON(
			t,
			4,
			3,
			"retention.apply",
			`{"planId":"`+planID+`"}`,
		),
	)
	if applyReplay.Error != nil ||
		fmt.Sprint(applyReplay.Result.(map[string]any)["deletedObjects"]) !=
			fmt.Sprint(result["deletedObjects"]) {
		t.Fatalf("authority apply receipt replay = %#v", applyReplay)
	}
	_, counters = runtime.coordinator.Current()
	if counters.MutationRevision != 1 {
		t.Fatalf("authority replay repeated mutation: %#v", counters)
	}
	secondPreview := dispatch(t, runtime, 5, "retention.plan", `{}`)
	if secondPreview.Error != nil {
		t.Fatalf("second retention.plan error = %#v", secondPreview.Error)
	}
	secondPlan := secondPreview.Result.(map[string]any)
	if secondPlan["reclaimableBytes"].(int64) != 0 {
		t.Fatalf(
			"logical tombstone was replanned: %#v (%s)",
			secondPlan,
			garbage,
		)
	}
	token, _ = runtime.coordinator.Current()
	crashCommit, err := runtime.repository.Commit(
		ctx,
		objectrepo.CommitRequest{
			Authority: token.Authority(),
			Objects: []objectrepo.ObjectInput{{
				Name:    "crash-unreachable",
				Content: []byte("retention-crash-recovery"),
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	crashGarbage := crashCommit.Objects["crash-unreachable"]
	crashPreview := dispatch(t, runtime, 6, "retention.plan", `{}`)
	if crashPreview.Error != nil {
		t.Fatalf("crash retention.plan error = %#v", crashPreview.Error)
	}
	crashPlanID := crashPreview.Result.(map[string]any)["planId"].(string)
	injected := errors.New("injected coordinator finish failure")
	runtime.coordinator.WithPersistenceFaultInjector(
		func(point writecoordinator.PersistenceFaultPoint) error {
			if point == writecoordinator.FaultBeforeFinishCommittedMutation {
				return injected
			}
			return nil
		},
	)
	crashApply := dispatch(
		t,
		runtime,
		7,
		"retention.apply",
		`{"planId":"`+crashPlanID+`"}`,
	)
	if crashApply.Error == nil {
		t.Fatal("coordinator finish fault was not surfaced")
	}
	recovery := runtime.coordinator.RecoveryState()
	if recovery.PendingMutationRevision != 2 {
		t.Fatalf("pending retention mutation = %#v", recovery)
	}
	if _, found, err := runtime.retention.store.LoadOperationReceipt(
		ctx,
		testWorkspaceID,
		retentionOperationID(7),
	); err != nil || !found {
		t.Fatalf("authority receipt before restart: found=%v err=%v", found, err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(ctx)
	_, recoveredCounters := reopened.coordinator.Current()
	if recoveredCounters.MutationRevision != 2 ||
		reopened.coordinator.RecoveryState().PendingMutationRevision != 0 {
		t.Fatalf("retention recovery counters = %#v state=%#v",
			recoveredCounters,
			reopened.coordinator.RecoveryState(),
		)
	}
	crashReplay := reopened.Dispatcher().DispatchEnvelope(
		ctx,
		retentionRequestJSON(
			t,
			7,
			7,
			"retention.apply",
			`{"planId":"`+crashPlanID+`"}`,
		),
	)
	if crashReplay.Error != nil ||
		fmt.Sprint(
			crashReplay.Result.(map[string]any)["deletedObjects"],
		) != "1" {
		t.Fatalf("crash authority receipt replay = %#v", crashReplay)
	}
	finalPreview := dispatch(t, reopened, 8, "retention.plan", `{}`)
	if finalPreview.Error != nil ||
		finalPreview.Result.(map[string]any)["reclaimableBytes"].(int64) != 0 {
		t.Fatalf("post-recovery retention.plan = %#v", finalPreview)
	}
	if reopened.retention.cancel != nil {
		reopened.retention.cancel()
		reopened.retention.wg.Wait()
		reopened.retention.cancel = nil
	}
	retentionDB, err := sql.Open(
		"sqlite",
		filepath.Join(
			root,
			".vibetable",
			"coordination",
			"retention.db",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retentionDB.ExecContext(
		ctx,
		`UPDATE retention_tombstones SET grace_until = ?
		  WHERE object_id = ?`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
		string(crashGarbage),
	); err != nil {
		_ = retentionDB.Close()
		t.Fatal(err)
	}
	if err := retentionDB.Close(); err != nil {
		t.Fatal(err)
	}
	physicalInventory, err := reopened.repository.RetentionInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, _ = reopened.coordinator.Current()
	if _, err := reopened.repository.RetireAndMaintain(
		ctx,
		objectrepo.RetentionMaintenanceRequest{
			Authority:        token.Authority(),
			ExpectedRevision: physicalInventory.Revision,
			ObjectIDs:        []objectrepo.ObjectID{crashGarbage},
		},
	); err != nil {
		t.Fatal(err)
	}
	// Simulate process death after repository verification returned but before
	// retention.db could mark the logical tombstone maintained.
	if err := reopened.Close(ctx); err != nil {
		t.Fatal(err)
	}
	recoveredMaintenance, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer recoveredMaintenance.Close(ctx)
	maintenance, err := recoveredMaintenance.retention.sweep(ctx)
	if err != nil ||
		(maintenance.DeletedObjects != 0 &&
			(maintenance.DeletedObjects != 1 ||
				!maintenance.VerificationRun)) {
		t.Fatalf("completed maintenance receipt recovery = %#v, %v",
			maintenance,
			err,
		)
	}
	tombstones, err := recoveredMaintenance.retention.store.AllTombstones(ctx)
	if err != nil {
		t.Fatal(err)
	}
	maintained := false
	for _, item := range tombstones {
		if item.ObjectID == crashGarbage && item.MaintainedAt != nil {
			maintained = true
		}
	}
	if !maintained {
		t.Fatalf("physical completion was not acknowledged: %#v", tombstones)
	}
	inventoryAfterMaintenance, err :=
		recoveredMaintenance.retention.source.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, exposed := inventoryAfterMaintenance.Nodes[crashGarbage]; exposed {
		t.Fatalf("completed receipt leaked as reclaimable object: %s",
			crashGarbage,
		)
	}
}

func TestRetainedSnapshotProtectsHistoryOnlyObjectsThroughMaintenance(
	t *testing.T,
) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  dataDir,
		HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	defer app.ResetBootstrapState()
	ledger, err := auditledger.Open(
		filepath.Join(root, ".vibetable", "audit"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	// The fixture writes history directly to construct exact retention roots.
	// A production files/ watcher would race the materialized files back into
	// history and invalidate the expected revision chain.
	stopFileWatcher(t, runtime)
	if runtime.retention.cancel != nil {
		runtime.retention.cancel()
		runtime.retention.wg.Wait()
		runtime.retention.cancel = nil
	}
	token, _ := runtime.coordinator.Current()
	documentID := "22222222-2222-4222-8222-222222222222"
	save := func(
		content string,
		expected *string,
	) filehistory.SaveResult {
		t.Helper()
		result, err := runtime.history.Save(
			ctx,
			filehistory.SaveRequest{
				Token:                     token,
				DocumentID:                documentID,
				Path:                      "history-protected.txt",
				ExpectedEffectiveRevision: expected,
				Kind:                      filehistory.RevisionAutosave,
				Content:                   []byte(content),
				MimeType:                  "text/plain",
				CreatedBy:                 "retention-test",
				DeviceID:                  testClaimID,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := save("history-only-before-snapshot", nil)
	second := save(
		"effective-at-snapshot",
		&first.Revision.RevisionID,
	)
	record, created, err := runtime.snapshots.Capture(
		ctx,
		snapshot.CaptureRequest{
			WorkspaceID: testWorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerManual,
		},
	)
	if err != nil || !created {
		t.Fatalf("snapshot capture = %#v, %v, %v", record, created, err)
	}
	if err := snapshot.ValidateSnapshotBundle(
		ctx,
		runtime.repository,
		record,
	); err != nil {
		t.Fatalf("fresh snapshot failed validation: %v", err)
	}
	historyOnly := first.Revision.ObjectID
	if containsRetentionObjectID(record.Objects, historyOnly) {
		t.Fatalf(
			"fixture object unexpectedly remained a catalog root: %s",
			historyOnly,
		)
	}
	historyIDs, err := snapshot.HistoryObjectIDs(
		ctx,
		runtime.repository,
		record,
	)
	if err != nil ||
		!containsRetentionObjectID(historyIDs, historyOnly) {
		t.Fatalf("snapshot history roots = %#v, %v", historyIDs, err)
	}
	assertPinContains := func(pinID string) {
		t.Helper()
		pins, err := runtime.repository.ListPins(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, pin := range pins {
			if pin.PinID == pinID &&
				containsRetentionObjectID(pin.Roots, historyOnly) {
				return
			}
		}
		t.Fatalf(
			"snapshot pin %q omitted history object %s: %#v",
			pinID,
			historyOnly,
			pins,
		)
	}
	assertPinContains(record.RootPinID)
	pinned := dispatch(
		t,
		runtime,
		1,
		"snapshot.update",
		fmt.Sprintf(
			`{"snapshotId":%q,"action":"pin",`+
				`"expectedCatalogRevision":%d}`,
			record.SnapshotID,
			record.CatalogRevision,
		),
	)
	if pinned.Error != nil {
		t.Fatalf("snapshot.update pin = %#v", pinned.Error)
	}
	record, err = runtime.snapshotRecord(ctx, record.SnapshotID)
	if err != nil || !record.Pinned {
		t.Fatalf("pinned snapshot = %#v, %v", record, err)
	}
	assertPinContains(record.RootPinID)
	// Simulate the end of the replacement reader pin. From this
	// point onward, only the retained Snapshot graph may protect its closure.
	token, _ = runtime.coordinator.Current()
	if err := runtime.repository.ReleasePin(
		ctx,
		token.Authority(),
		record.RootPinID,
	); err != nil {
		t.Fatal(err)
	}
	third := save(
		"effective-after-snapshot",
		&second.Revision.RevisionID,
	)
	fourth := save(
		"second-autosave-after-snapshot",
		&third.Revision.RevisionID,
	)
	inventory, err := runtime.retention.source.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshotRoot := record.ObjectMap["file-state-root"]
	if !containsRetentionObjectID(
		inventory.Nodes[snapshotRoot].Children,
		historyOnly,
	) {
		t.Fatalf(
			"snapshot retention graph omitted %s: %#v",
			historyOnly,
			inventory.Nodes[snapshotRoot],
		)
	}
	for _, liveRoot := range []objectrepo.ObjectID{
		third.Revision.ObjectID,
		fourth.Revision.ObjectID,
	} {
		if !containsRetentionObjectID(inventory.Roots, liveRoot) {
			t.Fatalf(
				"live FileHistory root omitted revision %s: %#v",
				liveRoot,
				inventory.Roots,
			)
		}
	}
	update := dispatch(
		t,
		runtime,
		2,
		"retention.update",
		`{"expectedRevision":1,"snapshotDays":1,"snapshotCount":1,`+
			`"snapshotBuckets":[],"fileRevisionDays":1,`+
			`"fileRevisionCount":1,"fileRevisionBuckets":[],`+
			`"repositoryLimitBytes":null}`,
	)
	if update.Error != nil {
		t.Fatalf("retention.update = %#v", update.Error)
	}
	token, _ = runtime.coordinator.Current()
	garbageContent := []byte("maintenance-garbage")
	garbageCommit, err := runtime.repository.Commit(
		ctx,
		objectrepo.CommitRequest{
			Authority: token.Authority(),
			Objects: []objectrepo.ObjectInput{{
				Name:    "maintenance-garbage",
				Content: garbageContent,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	garbage := garbageCommit.Objects["maintenance-garbage"]
	planned := dispatch(t, runtime, 3, "retention.plan", `{}`)
	if planned.Error != nil {
		t.Fatalf("retention.plan = %#v", planned.Error)
	}
	plan := planned.Result.(map[string]any)
	if plan["reclaimableBytes"].(int64) != int64(len(garbageContent)) {
		t.Fatalf(
			"tightened policy reclaimed protected history: %#v",
			plan,
		)
	}
	applied := dispatch(
		t,
		runtime,
		4,
		"retention.apply",
		`{"planId":"`+plan["planId"].(string)+`"}`,
	)
	if applied.Error != nil {
		t.Fatalf("retention.apply = %#v", applied.Error)
	}
	retentionDB, err := sql.Open(
		"sqlite",
		filepath.Join(
			root,
			".vibetable",
			"coordination",
			"retention.db",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retentionDB.ExecContext(
		ctx,
		`UPDATE retention_tombstones SET grace_until = ?
		  WHERE object_id = ?`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
		string(garbage),
	); err != nil {
		_ = retentionDB.Close()
		t.Fatal(err)
	}
	if err := retentionDB.Close(); err != nil {
		t.Fatal(err)
	}
	maintenance, err := runtime.retention.sweep(ctx)
	if err != nil ||
		maintenance.DeletedObjects != 1 ||
		!maintenance.VerificationRun {
		t.Fatalf("retention maintenance = %#v, %v", maintenance, err)
	}
	if err := snapshot.ValidateSnapshotBundle(
		ctx,
		runtime.repository,
		record,
	); err != nil {
		t.Fatalf("old snapshot failed validation after cleanup: %v", err)
	}
	reader, err := runtime.repository.Open(ctx, historyOnly)
	if err != nil {
		t.Fatalf("history object missing after maintenance: %v", err)
	}
	raw, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if string(raw) != "history-only-before-snapshot" {
		t.Fatalf("history object content = %q", raw)
	}
	if _, err := filehistory.Open(
		ctx,
		runtime.repository,
		runtime.coordinator,
		runtime.history.Root(),
	); err != nil {
		t.Fatalf("current FileHistory root failed to reopen: %v", err)
	}
}

func retentionOperationID(sequence uint64) string {
	return fmt.Sprintf("bbbbbbbb-bbbb-4bbb-8bbb-%012d", sequence)
}

func retentionRequestJSON(
	t *testing.T,
	sequence uint64,
	operationSequence uint64,
	method string,
	params string,
) []byte {
	t.Helper()
	raw := string(requestJSON(t, sequence, 7, method, params))
	return []byte(strings.Replace(
		raw,
		retentionOperationID(sequence),
		retentionOperationID(operationSequence),
		1,
	))
}

func uint64PointerForRetentionTest(value uint64) *uint64 {
	return &value
}

func containsRetentionObjectID(
	values []objectrepo.ObjectID,
	target objectrepo.ObjectID,
) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
