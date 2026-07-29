package workspacev2

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func TestConflictCommitKeepsPocketBaseReceiptPreparedAndResumesSameRevision(
	t *testing.T,
) {
	ctx := context.Background()
	runtime, app, closeRuntime := openExternalConflictTestRuntime(t)
	defer closeRuntime()
	local := conflictSettingsForSnapshotDays(t, runtime, 31)
	replica := conflictSettingsForSnapshotDays(t, runtime, 32)
	applyConflictTestSettings(t, runtime, local)
	externalRaw, err := json.Marshal(workspaceConflictExternalStage{
		FormatVersion: 1,
		Settings: &workspaceConflictSettingsStage{
			ExpectedObjectID: commitConflictTestObject(
				t, runtime, "resume-local-settings", local,
			),
			ObjectID: commitConflictTestObject(
				t, runtime, "resume-replica-settings", replica,
			),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	applier, err := filehistory.NewConflictApplier(
		runtime.history, runtime.headStore,
	)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "21111111-1111-4111-8111-111111111111"
	resultRaw := json.RawMessage(
		`{"operationId":"` + operationID +
			`","state":"applied","recoverySnapshotIds":["local","replica"]}`,
	)
	stage, err := applier.Prepare(ctx, filehistory.ConflictStage{
		PlanID:      "21222222-2222-4222-8222-222222222222",
		OperationID: operationID,
		External:    externalRaw,
		OperationReceipt: protocolv2.OperationReceipt{
			OperationID: operationID,
			WorkspaceID: testWorkspaceID,
			Method:      "conflict.apply",
			Scope:       protocolv2.WorkspaceScope,
			RequestHash: "sha256:resume-conflict",
			Result:      resultRaw,
		},
		RecoverySnapshotIDs: []string{"local", "replica"},
	})
	if err != nil {
		t.Fatal(err)
	}
	shutdown := false
	runtime.requestShutdown = func() { shutdown = true }
	owner := &productionReplicaConflict{
		runtime: runtime,
		applier: applier,
		conflictApplyFault: func(point string) error {
			if point == "before_filehistory" {
				return errors.New("fault after PB receipt")
			}
			return nil
		},
	}
	appender := &workspaceConflictAppender{owner: owner}
	token, before := runtime.coordinator.Current()
	_, err = appender.Commit(ctx, conflictresolution.ApplyStage{
		StageID: stage.StageID, OperationID: operationID,
		PlanID: stage.PlanID,
	})
	if !errors.Is(err, writecoordinator.ErrExternalCommitted) {
		t.Fatalf("post-receipt commit error = %v", err)
	}
	prepared := runtime.coordinator.RecoveryState()
	expectedRevision := before.MutationRevision + 1
	if prepared.PendingMutationRevision != expectedRevision ||
		prepared.Counters.MutationRevision != before.MutationRevision ||
		!shutdown {
		t.Fatalf("prepared state=%#v shutdown=%v", prepared, shutdown)
	}
	committed, err := writecoordinator.HasPocketBaseReceipt(
		ctx, app, token, expectedRevision,
	)
	if err != nil || !committed {
		t.Fatalf("PB receipt=%v err=%v", committed, err)
	}
	owner.conflictApplyFault = nil
	receipt, err := appender.Commit(ctx, conflictresolution.ApplyStage{
		StageID: stage.StageID, OperationID: operationID,
		PlanID: stage.PlanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered := runtime.coordinator.RecoveryState()
	if receipt.AuthorityRevision == 0 ||
		recovered.PendingMutationRevision != 0 ||
		recovered.Counters.MutationRevision != expectedRevision {
		t.Fatalf("receipt=%#v recovered=%#v", receipt, recovered)
	}
	head, found, err := runtime.headStore.Load(ctx, testWorkspaceID)
	if err != nil || !found ||
		head.MutationRevision != expectedRevision {
		t.Fatalf("head=%#v found=%v err=%v", head, found, err)
	}
	next, err := writecoordinator.HasPocketBaseReceipt(
		ctx, app, token, expectedRevision+1,
	)
	if err != nil || next {
		t.Fatalf("next revision receipt=%v err=%v", next, err)
	}
}

func TestRuntimeReopensAndResumesConflictAtPocketBaseReceiptRevision(
	t *testing.T,
) {
	ctx := context.Background()
	runtime, app, _ := openExternalConflictTestRuntime(t)
	local := conflictSettingsForSnapshotDays(t, runtime, 31)
	replicaSettings := conflictSettingsForSnapshotDays(t, runtime, 32)
	applyConflictTestSettings(t, runtime, local)
	baseID := commitConflictTestObject(
		t, runtime, "restart-base-settings",
		conflictSettingsForSnapshotDays(t, runtime, 29),
	)
	localID := commitConflictTestObject(
		t, runtime, "restart-local-settings", local,
	)
	replicaID := commitConflictTestObject(
		t, runtime, "restart-replica-settings", replicaSettings,
	)
	conflictPath := joinCoordination(runtime.paths, "conflicts.db")
	engine, err := conflictresolution.OpenEngine(conflictPath)
	if err != nil {
		t.Fatal(err)
	}
	const conflictID = "21555555-5555-4555-8555-555555555555"
	set := conflictresolution.Set{
		ConflictID: conflictID, WorkspaceID: testWorkspaceID,
		State: conflictresolution.StatePending, Revision: 1,
		Base: conflictresolution.Candidate{
			SnapshotID: "base",
			Settings: conflictresolution.SettingsState{
				ObjectID: string(baseID),
			},
			Files: map[string]conflictresolution.FileState{},
		},
		Local: conflictresolution.Candidate{
			SnapshotID: "local",
			Settings: conflictresolution.SettingsState{
				ObjectID: string(localID),
			},
			Files: map[string]conflictresolution.FileState{},
		},
		Replica: conflictresolution.Candidate{
			SnapshotID: "replica",
			Settings: conflictresolution.SettingsState{
				ObjectID: string(replicaID),
			},
			Files: map[string]conflictresolution.FileState{},
		},
		Dependencies: conflictresolution.DependencyGraph{
			Complete: true,
			Edges: map[string][]string{
				conflictresolution.WorkspaceSettingsItemID: {},
			},
		},
		CreatedAt: time.Now().UTC(),
	}
	if err := engine.Add(ctx, set); err != nil {
		t.Fatal(err)
	}
	preview, err := engine.Preview(
		ctx,
		conflictID,
		[]conflictresolution.Choice{{
			ItemID: conflictresolution.WorkspaceSettingsItemID,
			Kind:   conflictresolution.SettingsItem,
			Side:   conflictresolution.Replica,
		}},
	)
	if err != nil || !preview.Valid {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	applier, err := filehistory.NewConflictApplier(
		runtime.history, runtime.headStore,
	)
	if err != nil {
		t.Fatal(err)
	}
	owner := &productionReplicaConflict{
		runtime: runtime, conflicts: engine, applier: applier,
		conflictApplyFault: func(point string) error {
			if point == "before_filehistory" {
				return errors.New("simulated process loss after PB receipt")
			}
			return nil
		},
	}
	dispatcher := protocolv2.New()
	dispatcher.BindSession(protocolv2.Session{
		WorkspaceID: testWorkspaceID, Epoch: 7,
	})
	dispatcher.Register(
		"conflict.apply",
		protocolv2.WorkspaceScope,
		owner.applyConflict,
	)
	const operationID = "21666666-6666-4666-8666-666666666666"
	request := []byte(`{
		"jsonrpc":"2.0","id":"conflict-restart","method":"conflict.apply",
		"wire":{"scope":"workspace","workspaceId":"` + testWorkspaceID + `",
			"sessionEpoch":7,"operationId":"` + operationID + `","sequence":1},
		"params":{"planId":"` + preview.PlanID + `"}
	}`)
	response := dispatcher.DispatchEnvelope(ctx, request)
	if response.Error == nil {
		t.Fatalf("post-receipt response = %#v", response)
	}
	token, beforeRestart := runtime.coordinator.Current()
	prepared := runtime.coordinator.RecoveryState()
	expectedRevision := beforeRestart.MutationRevision + 1
	if prepared.PendingMutationRevision != expectedRevision {
		t.Fatalf("prepared state = %#v", prepared)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	dataDir := runtime.paths.data
	auditDir := runtime.paths.audit
	oldLedger := runtime.ledger
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := oldLedger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}

	reopenedApp := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := reopenedApp.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer reopenedApp.ResetBootstrapState()
	reopenedLedger, err := auditledger.Open(auditDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedLedger.Close()
	reopened, err := Open(ctx, Options{
		App: reopenedApp, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID,
		Ledger: reopenedLedger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(ctx)

	recovery := reopened.coordinator.RecoveryState()
	if recovery.PendingMutationRevision != 0 ||
		recovery.Counters.MutationRevision != expectedRevision {
		t.Fatalf("reopened recovery = %#v", recovery)
	}
	committed, err := writecoordinator.HasPocketBaseReceipt(
		ctx, reopenedApp, token, expectedRevision,
	)
	if err != nil || !committed {
		t.Fatalf("original PB receipt=%v err=%v", committed, err)
	}
	next, err := writecoordinator.HasPocketBaseReceipt(
		ctx, reopenedApp, token, expectedRevision+1,
	)
	if err != nil || next {
		t.Fatalf("next PB receipt=%v err=%v", next, err)
	}
	head, found, err := reopened.headStore.Load(ctx, testWorkspaceID)
	if err != nil || !found ||
		head.MutationRevision != expectedRevision {
		t.Fatalf("reopened head=%#v found=%v err=%v",
			head, found, err,
		)
	}
	currentSettings, err := snapshotWorkspaceSettings(ctx, reopened.state)
	if err != nil || string(currentSettings) != string(replicaSettings) {
		t.Fatalf("settings=%s err=%v", currentSettings, err)
	}
	reopenedEngine, err := conflictresolution.OpenEngine(conflictPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedEngine.Close()
	currentSet, err := reopenedEngine.Inspect(ctx, conflictID)
	if err != nil || currentSet.State != conflictresolution.StateApplied {
		t.Fatalf("conflict set=%#v err=%v", currentSet, err)
	}
}

func TestConflictReplanPinCleanupSurvivesEngineReopen(t *testing.T) {
	ctx := context.Background()
	runtime, _, closeRuntime := openExternalConflictTestRuntime(t)
	defer closeRuntime()
	root := commitConflictTestObject(
		t, runtime, "conflict-pin-root", []byte("protected"),
	)
	token, _ := runtime.coordinator.Current()
	pin, err := runtime.repository.Pin(
		ctx,
		token.Authority(),
		[]objectrepo.ObjectID{root},
		"conflict-candidate",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runtime.paths.coordination, "pin-cleanup.db")
	engine, err := conflictresolution.OpenEngine(path)
	if err != nil {
		t.Fatal(err)
	}
	set := conflictresolution.Set{
		ConflictID:  "21999999-9999-4999-8999-999999999999",
		WorkspaceID: testWorkspaceID,
		State:       conflictresolution.StatePending,
		Revision:    2,
		Base: conflictresolution.Candidate{
			SnapshotID: "base",
			Files:      map[string]conflictresolution.FileState{},
		},
		Local: conflictresolution.Candidate{
			SnapshotID: "local",
			Files:      map[string]conflictresolution.FileState{},
		},
		Replica: conflictresolution.Candidate{
			SnapshotID: "replica",
			Files:      map[string]conflictresolution.FileState{},
		},
		Dependencies: conflictresolution.DependencyGraph{
			Complete: true,
			Edges:    map[string][]string{},
		},
		RootPinIDs:     []string{pin.PinID},
		ReplanRequired: true,
		CreatedAt:      time.Now().UTC(),
	}
	if err := engine.Add(ctx, set); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := conflictresolution.OpenEngine(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	owner := &productionReplicaConflict{
		runtime: runtime, conflicts: reopened,
	}
	if err := owner.releaseTerminalConflictPins(ctx); err != nil {
		t.Fatal(err)
	}
	pins, err := runtime.repository.ListPins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range pins {
		if current.PinID == pin.PinID {
			t.Fatalf("terminal conflict pin still exists: %#v", current)
		}
	}
	if _, err := reopened.Inspect(
		ctx, set.ConflictID,
	); !errors.Is(err, conflictresolution.ErrConflictNotFound) {
		t.Fatalf("replan-required set not deleted: %v", err)
	}
	if err := owner.releaseTerminalConflictPins(ctx); err != nil {
		t.Fatalf("idempotent cleanup after reopen: %v", err)
	}
}

func TestConflictExternalNormalFaultRollsBackSettingsAndRequestsShutdownOnlyAfterReceipt(
	t *testing.T,
) {
	ctx := context.Background()
	runtime, app, closeRuntime := openExternalConflictTestRuntime(t)
	defer closeRuntime()
	localSettings := conflictSettingsForSnapshotDays(t, runtime, 31)
	applyConflictTestSettings(t, runtime, localSettings)
	targetSettings := conflictSettingsForSnapshotDays(t, runtime, 32)
	localID := commitConflictTestObject(
		t, runtime, "local-settings", localSettings,
	)
	targetID := commitConflictTestObject(
		t, runtime, "replica-settings", targetSettings,
	)
	external := workspaceConflictExternalStage{
		FormatVersion: 1,
		Settings: &workspaceConflictSettingsStage{
			ExpectedObjectID: localID,
			ObjectID:         targetID,
		},
	}
	raw, err := json.Marshal(external)
	if err != nil {
		t.Fatal(err)
	}
	stage := filehistory.ConflictStage{
		OperationID: "22222222-2222-4222-8222-222222222222",
		External:    raw,
	}
	token, counters := runtime.coordinator.Current()
	intent := writecoordinator.WriteIntent{
		Token: token, MutationRevision: counters.MutationRevision + 1,
		AuditSourceEpoch: "business-v2",
	}
	shutdown := false
	runtime.requestShutdown = func() { shutdown = true }
	applier, err := filehistory.NewConflictApplier(
		runtime.history, runtime.headStore,
	)
	if err != nil {
		t.Fatal(err)
	}
	owner := &productionReplicaConflict{
		runtime: runtime, applier: applier,
		conflictApplyFault: func(point string) error {
			if point == "after_settings" {
				return errors.New("injected after settings")
			}
			return nil
		},
	}
	appender := &workspaceConflictAppender{owner: owner}
	if _, err := appender.applyExternalStage(ctx, intent, stage); err == nil {
		t.Fatal("after-settings fault unexpectedly succeeded")
	}
	current, err := snapshotWorkspaceSettings(ctx, runtime.state)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(localSettings) {
		t.Fatalf("settings after rollback = %s", current)
	}
	if shutdown {
		t.Fatal("rollback-capable fault requested shutdown")
	}
	committed, err := writecoordinator.HasPocketBaseReceipt(
		ctx, app, token, intent.MutationRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if committed {
		t.Fatal("rolled-back transaction left PB receipt")
	}

	owner.conflictApplyFault = func(point string) error {
		if point == "before_filehistory" {
			return errors.New("injected before filehistory")
		}
		return nil
	}
	if _, err := appender.applyExternalStage(ctx, intent, stage); err == nil {
		t.Fatal("post-receipt fault unexpectedly succeeded")
	}
	if !shutdown {
		t.Fatal("post-receipt fault did not fail closed")
	}
	committed, err = writecoordinator.HasPocketBaseReceipt(
		ctx, app, token, intent.MutationRevision,
	)
	if err != nil || !committed {
		t.Fatalf("PB receipt = %v, %v", committed, err)
	}
	current, err = snapshotWorkspaceSettings(ctx, runtime.state)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(targetSettings) {
		t.Fatalf("rollforward settings = %s", current)
	}

	// Startup-style replay uses the durable identity receipt, skips the stale
	// precondition, and finishes the original coordinator revision.
	owner.conflictApplyFault = nil
	if _, err := appender.applyExternalStage(ctx, intent, stage); err != nil {
		t.Fatalf("receipt-backed replay: %v", err)
	}
	nextReceipt, err := writecoordinator.HasPocketBaseReceipt(
		ctx, app, token, intent.MutationRevision+1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if nextReceipt {
		t.Fatal("replay created a second PB identity receipt")
	}
}

func TestConflictExternalExpectedSettingsCASRejectsPostPreviewEdit(
	t *testing.T,
) {
	runtime, _, closeRuntime := openExternalConflictTestRuntime(t)
	defer closeRuntime()
	previewed := conflictSettingsForSnapshotDays(t, runtime, 31)
	applyConflictTestSettings(t, runtime, previewed)
	external := workspaceConflictExternalStage{
		FormatVersion: 1,
		Settings: &workspaceConflictSettingsStage{
			ExpectedObjectID: objectrepo.ObjectID(
				conflictObjectID(previewed),
			),
			ObjectID: "obj_target",
		},
	}
	applyConflictTestSettings(
		t,
		runtime,
		conflictSettingsForSnapshotDays(t, runtime, 32),
	)
	appender := &workspaceConflictAppender{
		owner: &productionReplicaConflict{runtime: runtime},
	}
	err := appender.validateExternalExpected(
		context.Background(), external,
	)
	if !errors.Is(err, conflictresolution.ErrStalePlan) {
		t.Fatalf("post-preview edit accepted: %v", err)
	}
}

func TestConflictExternalLegacySettingsChoiceIsSemanticNoOpWithDurableRollbackBaseline(
	t *testing.T,
) {
	ctx := context.Background()
	runtime, _, closeRuntime := openExternalConflictTestRuntime(t)
	defer closeRuntime()
	current := conflictSettingsForSnapshotDays(t, runtime, 31)
	applyConflictTestSettings(t, runtime, current)
	currentID := commitConflictTestObject(
		t,
		runtime,
		"legacy-settings-current",
		current,
	)
	legacyID := commitConflictTestObject(
		t,
		runtime,
		"legacy-settings-marker",
		[]byte(`{}`),
	)
	appender := &workspaceConflictAppender{
		owner: &productionReplicaConflict{runtime: runtime},
	}

	staged, err := appender.stageConflictSettings(
		ctx,
		conflictresolution.SettingsState{ObjectID: string(currentID)},
		conflictresolution.SettingsState{ObjectID: string(legacyID)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.Previous) == 0 ||
		string(staged.Previous) != string(current) {
		t.Fatalf("durable rollback baseline = %s", staged.Previous)
	}
	external := workspaceConflictExternalStage{
		FormatVersion: 1,
		Settings:      &staged,
	}
	if err := appender.validateExternalExpected(ctx, external); err != nil {
		t.Fatalf("legacy expected precondition = %v", err)
	}
	if err := replaceWorkspaceSettings(
		ctx,
		runtime.state,
		[]byte(`{}`),
		99,
	); err != nil {
		t.Fatal(err)
	}
	if err := appender.validateExternalChosen(ctx, external); err != nil {
		t.Fatalf("legacy chosen semantic validation = %v", err)
	}

	target := conflictSettingsForSnapshotDays(t, runtime, 32)
	applyConflictTestSettings(t, runtime, target)
	if err := appender.restoreExternalExpected(ctx, external); err != nil {
		t.Fatalf("restore durable rollback baseline = %v", err)
	}
	restored, err := snapshotWorkspaceSettings(ctx, runtime.state)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(current) {
		t.Fatalf("restored settings = %s", restored)
	}
}

func TestConflictExternalLegacyExpectedAcceptsVersionedCurrent(
	t *testing.T,
) {
	ctx := context.Background()
	runtime, _, closeRuntime := openExternalConflictTestRuntime(t)
	defer closeRuntime()
	current := conflictSettingsForSnapshotDays(t, runtime, 31)
	applyConflictTestSettings(t, runtime, current)
	legacyID := commitConflictTestObject(
		t,
		runtime,
		"legacy-settings-expected",
		[]byte(`{}`),
	)
	appender := &workspaceConflictAppender{
		owner: &productionReplicaConflict{runtime: runtime},
	}
	external := workspaceConflictExternalStage{
		FormatVersion: 1,
		Settings: &workspaceConflictSettingsStage{
			ExpectedObjectID: legacyID,
			ObjectID:         legacyID,
		},
	}
	if err := appender.validateExternalExpected(ctx, external); err != nil {
		t.Fatalf("legacy expected did not match versioned live state: %v", err)
	}
}

func TestConflictExternalAttachmentFaultRestoresOldFilesAndTableTransaction(
	t *testing.T,
) {
	ctx := context.Background()
	runtime, app, closeRuntime := openExternalConflictTestRuntime(t)
	defer closeRuntime()
	collection := core.NewBaseCollection("conflict_records")
	collection.Fields.Add(&core.TextField{Name: "title"})
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("title", "local")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	attachmentKey := collection.Id + "/" + record.Id + "/old.txt"
	filesystem, err := app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Upload([]byte("old-attachment"), attachmentKey); err != nil {
		_ = filesystem.Close()
		t.Fatal(err)
	}
	if err := filesystem.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := runtime.frozenSource.snapshotDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := runtime.frozenSource.snapshotAttachments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	attachmentIDs := map[string]string{}
	for key, content := range attachments {
		attachmentIDs[key] = string(commitConflictTestObject(
			t, runtime, "expected-attachment:"+key, content,
		))
	}
	projection, err := conflictresolution.ProjectSQLiteDatabase(
		ctx, database, "expected-database", attachmentIDs,
	)
	if err != nil {
		t.Fatal(err)
	}
	expected := projection.Tables[collection.Id]
	external := workspaceConflictExternalStage{
		FormatVersion: 1,
		Tables: []workspaceConflictTableStage{{
			TableID:  collection.Id,
			Expected: expected,
			Chosen: conflictresolution.TableState{
				TableID: collection.Id,
				Deleted: true,
			},
			Deleted: true,
		}},
	}
	raw, err := json.Marshal(external)
	if err != nil {
		t.Fatal(err)
	}
	token, counters := runtime.coordinator.Current()
	intent := writecoordinator.WriteIntent{
		Token: token, MutationRevision: counters.MutationRevision + 1,
		AuditSourceEpoch: "business-v2",
	}
	applier, err := filehistory.NewConflictApplier(
		runtime.history, runtime.headStore,
	)
	if err != nil {
		t.Fatal(err)
	}
	owner := &productionReplicaConflict{
		runtime: runtime, applier: applier,
		conflictApplyFault: func(point string) error {
			if point == "after_attachments" {
				return errors.New("injected after attachments")
			}
			return nil
		},
	}
	appender := &workspaceConflictAppender{owner: owner}
	_, err = appender.applyExternalStage(
		ctx,
		intent,
		filehistory.ConflictStage{
			OperationID: "33333333-3333-4333-8333-333333333333",
			External:    raw,
		},
	)
	if err == nil {
		t.Fatal("after-attachments fault unexpectedly succeeded")
	}
	if _, err := app.FindCollectionByNameOrId(collection.Id); err != nil {
		t.Fatalf("table transaction did not roll back: %v", err)
	}
	filesystem, err = app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := filesystem.GetReader(attachmentKey)
	if err != nil {
		_ = filesystem.Close()
		t.Fatalf("attachment backup not restored: %v", err)
	}
	content := make([]byte, len("old-attachment"))
	if _, err := reader.Read(content); err != nil {
		_ = reader.Close()
		_ = filesystem.Close()
		t.Fatal(err)
	}
	_ = reader.Close()
	_ = filesystem.Close()
	if string(content) != "old-attachment" {
		t.Fatalf("restored attachment = %q", content)
	}

	record.Set("title", "edited after preview")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	owner.conflictApplyFault = nil
	err = appender.validateExternalExpected(ctx, external)
	if !errors.Is(err, conflictresolution.ErrStalePlan) {
		t.Fatalf("post-preview table edit accepted: %v", err)
	}
}

func openExternalConflictTestRuntime(
	t *testing.T,
) (*Runtime, *pocketbase.PocketBase, func()) {
	t.Helper()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	outbox := core.NewBaseCollection("vibetable_audit_outbox")
	outbox.Fields.Add(
		&core.TextField{Name: "event_id", Required: true},
		&core.TextField{Name: "source_epoch", Required: true},
		&core.NumberField{Name: "source_sequence", Required: true},
		&core.TextField{Name: "mutation_identity", Required: true},
		&core.TextField{Name: "payload_hash", Required: true},
		&core.JSONField{Name: "payload_json", Required: true},
		&core.DateField{Name: "occurred_at", Required: true},
		&core.TextField{Name: "status", Required: true},
		&core.NumberField{Name: "attempts"},
	)
	outbox.AddIndex(
		"uniq_vibetable_audit_outbox_event_id",
		true,
		"`event_id`",
		"",
	)
	outbox.AddIndex(
		"uniq_vibetable_audit_outbox_source_epoch_source_sequence",
		true,
		"`source_epoch`, `source_sequence`",
		"",
	)
	if err := app.Save(outbox); err != nil {
		t.Fatal(err)
	}
	ledger, err := auditledger.Open(
		filepath.Join(root, ".vibetable", "audit"),
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := Open(context.Background(), Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime, app, func() {
		_ = runtime.Close(context.Background())
		_ = ledger.Close()
		_ = app.ResetBootstrapState()
	}
}

func commitConflictTestObject(
	t *testing.T,
	runtime *Runtime,
	name string,
	content []byte,
) objectrepo.ObjectID {
	t.Helper()
	token, _ := runtime.coordinator.Current()
	receipt, err := runtime.repository.Commit(
		context.Background(),
		objectrepo.CommitRequest{
			Authority: token.Authority(),
			Objects: []objectrepo.ObjectInput{{
				Name: name, Content: content,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return receipt.Objects[name]
}

func conflictSettingsForSnapshotDays(
	t *testing.T,
	runtime *Runtime,
	days uint64,
) []byte {
	t.Helper()
	raw, err := snapshotWorkspaceSettings(
		context.Background(),
		runtime.state,
	)
	if err != nil {
		t.Fatal(err)
	}
	value, legacy, err := decodeWorkspaceSettingsSnapshot(raw)
	if err != nil || legacy {
		t.Fatalf("current workspace settings: legacy=%v err=%v", legacy, err)
	}
	value.Retention.SnapshotDays = days
	raw, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func applyConflictTestSettings(
	t *testing.T,
	runtime *Runtime,
	raw []byte,
) {
	t.Helper()
	_, counters := runtime.coordinator.Current()
	if err := replaceWorkspaceSettings(
		context.Background(),
		runtime.state,
		raw,
		counters.MutationRevision,
	); err != nil {
		t.Fatal(err)
	}
}
