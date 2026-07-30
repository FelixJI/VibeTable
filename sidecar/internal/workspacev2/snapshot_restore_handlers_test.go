package workspacev2

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func TestSnapshotRestoreAcceptsSnapshotWithoutFileHistory(
	t *testing.T,
) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	ledger, err := auditledger.Open(filepath.Join(root, ".vibetable", "audit"))
	if err != nil {
		t.Fatal(err)
	}
	shutdownRequested := false
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		RequestShutdown: func() { shutdownRequested = true },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = runtime.Close(ctx)
		_ = ledger.Close()
		_ = app.ResetBootstrapState()
	})
	token, _ := runtime.coordinator.Current()
	target, created, err := runtime.snapshots.Capture(
		ctx,
		snapshot.CaptureRequest{
			WorkspaceID: testWorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerManual,
			Pinned:      true,
		},
	)
	if err != nil || !created {
		t.Fatalf("capture target = %#v, %v, %v", target, created, err)
	}
	added, err := runtime.history.Save(
		ctx,
		filehistory.SaveRequest{
			Token:      token,
			DocumentID: "22222222-2222-4222-8222-222222222222",
			Path:       "plans/added-after-empty-snapshot.txt",
			Kind:       filehistory.RevisionFormal,
			Content:    []byte("preserve this revision"),
			MimeType:   "text/plain",
			CreatedBy:  "test",
			DeviceID:   testClaimID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := runtime.history.PreviewSnapshotRestore(ctx, "")
	if err != nil ||
		len(diff.AddedAfterSnapshot) != 1 ||
		diff.AddedAfterSnapshot[0] != "plans/added-after-empty-snapshot.txt" {
		t.Fatalf("empty-root restore diff = %#v, %v", diff, err)
	}
	staged, err := runtime.history.StageSnapshotRestore(
		ctx,
		writecoordinator.WriteIntent{
			Token:            token,
			MutationRevision: added.MutationRevision + 1,
		},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.Documents) != 1 ||
		staged.Documents[0].Status != filehistory.DocumentDeleted ||
		staged.Documents[0].EffectiveRevisionID !=
			added.Document.EffectiveRevisionID ||
		len(staged.Documents[0].Revisions) !=
			len(added.Document.Revisions) {
		t.Fatalf(
			"empty-root staged restore did not soft-delete with revisions intact: %#v",
			staged.Documents,
		)
	}
	previewRaw, _ := json.Marshal(previewSnapshotRestoreParams{
		SnapshotID: target.SnapshotID,
		TargetMode: "currentWorkspace",
	})
	preview, err := runtime.previewSnapshotRestore(ctx, nil, previewRaw)
	if err != nil {
		t.Fatal(err)
	}
	previewMap := preview.(map[string]any)
	changes := previewMap["changes"].([]string)
	if !containsRestorePreviewPrefix(
		changes,
		"files:added-after-snapshot:",
	) {
		t.Fatalf("empty-root restore preview omitted added file: %#v", changes)
	}
	planID := previewMap["planId"].(string)
	wire := json.RawMessage(`{
		"scope":"workspace",
		"workspaceId":"11111111-1111-4111-8111-111111111111",
		"sessionEpoch":7,
		"sequence":1,
		"operationId":"cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	}`)
	applyRaw, _ := json.Marshal(applySnapshotRestoreParams{
		PlanID: planID, Confirmed: true,
	})
	if _, err := runtime.applySnapshotRestore(ctx, wire, applyRaw); err != nil {
		t.Fatal(err)
	}
	if !shutdownRequested {
		t.Fatal("restore did not request process shutdown")
	}
}

func TestSnapshotRestoreStagesOfflineInstallAndCommitsAfterHealthyOpen(
	t *testing.T,
) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	if _, err := app.DB().NewQuery(`
		CREATE TABLE restore_probe (value TEXT NOT NULL);
		INSERT INTO restore_probe(value) VALUES ('snapshot');
	`).Execute(); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(root, ".vibetable", "audit")
	ledger, err := auditledger.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	shutdownRequested := false
	options := Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		RequestShutdown: func() { shutdownRequested = true },
	}
	runtime, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := runtime.coordinator.Current()
	documentID := "22222222-2222-4222-8222-222222222222"
	first, err := runtime.history.Save(
		ctx,
		filehistory.SaveRequest{
			Token: token, DocumentID: documentID,
			Path: "plans/q3.txt", Kind: filehistory.RevisionFormal,
			Content: []byte("snapshot"), MimeType: "text/plain",
			CreatedBy: "test", DeviceID: testClaimID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	target, created, err := runtime.snapshots.Capture(
		ctx,
		snapshot.CaptureRequest{
			WorkspaceID: testWorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerManual,
			Pinned:      true,
		},
	)
	if err != nil || !created {
		t.Fatalf("capture target = %#v, %v, %v", target, created, err)
	}
	bundlePreview, err := runtime.buildSnapshotRestorePreview(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if bundlePreview.HistoryRoot == "" ||
		bundlePreview.HistoryRoot != runtime.history.Root() {
		t.Fatalf(
			"restore preview history root = %q, current = %q",
			bundlePreview.HistoryRoot,
			runtime.history.Root(),
		)
	}
	invalid := target
	invalid.ObjectMap = make(
		map[string]objectrepo.ObjectID,
		len(target.ObjectMap),
	)
	for name, id := range target.ObjectMap {
		invalid.ObjectMap[name] = id
	}
	invalid.ObjectMap["file-state-root"] = invalid.ObjectMap["database"]
	if _, err := runtime.buildSnapshotRestorePreview(ctx, invalid); err == nil {
		t.Fatal("restore preview accepted an invalid snapshot bundle")
	}
	if _, err := app.DB().NewQuery(
		`UPDATE restore_probe SET value = 'later'`,
	).Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.history.Save(
		ctx,
		filehistory.SaveRequest{
			Token: token, DocumentID: documentID,
			ExpectedEffectiveRevision: &first.Revision.RevisionID,
			Kind:                      filehistory.RevisionFormal,
			Content:                   []byte("later"), MimeType: "text/plain",
			CreatedBy: "test", DeviceID: testClaimID,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.history.Save(
		ctx,
		filehistory.SaveRequest{
			Token:      token,
			DocumentID: "33333333-3333-4333-8333-333333333333",
			Path:       "plans/added-after-snapshot.txt",
			Kind:       filehistory.RevisionFormal,
			Content:    []byte("newer"),
			MimeType:   "text/plain",
			CreatedBy:  "test",
			DeviceID:   testClaimID,
		},
	); err != nil {
		t.Fatal(err)
	}
	updateSettingsRaw, _ := json.Marshal(updateRetentionParams{
		ExpectedRevision:    1,
		SnapshotDays:        45,
		SnapshotCount:       60,
		SnapshotBuckets:     []string{"daily", "weekly"},
		FileRevisionDays:    40,
		FileRevisionCount:   120,
		FileRevisionBuckets: []string{"daily", "monthly"},
	})
	if _, err := runtime.updateRetention(
		ctx,
		nil,
		updateSettingsRaw,
	); err != nil {
		t.Fatal(err)
	}
	previewRaw, _ := json.Marshal(previewSnapshotRestoreParams{
		SnapshotID: target.SnapshotID,
		TargetMode: "currentWorkspace",
	})
	preview, err := runtime.previewSnapshotRestore(ctx, nil, previewRaw)
	if err != nil {
		t.Fatal(err)
	}
	previewMap := preview.(map[string]any)
	changes, ok := previewMap["changes"].([]string)
	if !ok || len(changes) < 2 ||
		!containsRestorePreviewChange(changes, "database:replace") ||
		!containsRestorePreviewPrefix(
			changes,
			"files:effective-pointers:",
		) ||
		!containsRestorePreviewPrefix(
			changes,
			"files:added-after-snapshot:",
		) ||
		!containsRestorePreviewChange(
			changes,
			"workspace-settings:retention",
		) {
		t.Fatalf("restore preview changes is not string[]: %#v", previewMap)
	}
	planID := previewMap["planId"].(string)
	newWorkspaceRaw, _ := json.Marshal(previewSnapshotRestoreParams{
		SnapshotID: target.SnapshotID,
		TargetMode: "newWorkspace",
	})
	if _, err := runtime.previewSnapshotRestore(
		ctx,
		nil,
		newWorkspaceRaw,
	); err == nil || err.Error() != "restore.new_workspace_broker_required" {
		t.Fatalf("new workspace restore boundary = %v", err)
	}
	wire := json.RawMessage(`{
		"scope":"workspace",
		"workspaceId":"11111111-1111-4111-8111-111111111111",
		"sessionEpoch":7,
		"sequence":1,
		"operationId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	}`)
	applyRaw, _ := json.Marshal(applySnapshotRestoreParams{
		PlanID: planID, Confirmed: true,
	})
	currentSettings, mutationRevision, err := runtime.state.retention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changedAfterPreview := currentSettings
	changedAfterPreview.PolicyRevision++
	changedAfterPreview.SnapshotCount++
	if err := runtime.state.updateRetention(
		ctx,
		currentSettings.PolicyRevision,
		changedAfterPreview,
		mutationRevision,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.applySnapshotRestore(
		ctx,
		wire,
		applyRaw,
	); err == nil || err.Error() != "restore.plan_stale" {
		t.Fatalf("unbound settings change was accepted: %v", err)
	}
	currentSettings.PolicyRevision = changedAfterPreview.PolicyRevision + 1
	if err := runtime.state.updateRetention(
		ctx,
		changedAfterPreview.PolicyRevision,
		currentSettings,
		mutationRevision,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.applySnapshotRestore(ctx, wire, applyRaw); err != nil {
		t.Fatal(err)
	}
	if !shutdownRequested {
		t.Fatal("restore did not request process shutdown")
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}

	installed, err := ApplyPendingSnapshotRestore(
		ctx,
		dataDir,
		testWorkspaceID,
	)
	if err != nil || !installed {
		t.Fatalf("offline install = %v, %v", installed, err)
	}
	reopenedApp := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := reopenedApp.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer reopenedApp.ResetBootstrapState()
	reopenedLedger, err := auditledger.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedLedger.Close()
	reopened, err := Open(ctx, Options{
		App: reopenedApp, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: reopenedLedger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(ctx)
	if err := reopened.CompletePendingSnapshotRestore(ctx); err != nil {
		t.Fatal(err)
	}
	restoredSettings, _, err := reopened.state.retention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if restoredSettings.SnapshotDays != 30 ||
		restoredSettings.SnapshotCount != 50 ||
		restoredSettings.FileRevisionDays != 30 ||
		restoredSettings.FileRevisionCount != 100 {
		t.Fatalf("restored retention settings = %#v", restoredSettings)
	}
	replayRequest, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "restore-replay",
		"method":  "snapshot.applyRestore",
		"wire":    json.RawMessage(wire),
		"params":  json.RawMessage(applyRaw),
	})
	if err != nil {
		t.Fatal(err)
	}
	replay := reopened.Dispatcher().DispatchEnvelope(ctx, replayRequest)
	if replay.Error != nil ||
		replay.Result.(map[string]any)["state"] != "prepared" {
		t.Fatalf("restore journal receipt replay = %#v", replay)
	}
	var databaseValue string
	if err := reopenedApp.DB().NewQuery(
		`SELECT value FROM restore_probe`,
	).Row(&databaseValue); err != nil || databaseValue != "snapshot" {
		t.Fatalf("restored database value = %q, %v", databaseValue, err)
	}
	document, err := reopened.history.Inspect(documentID)
	if err != nil {
		t.Fatal(err)
	}
	effective := document.Revisions[len(document.Revisions)-1]
	if effective.Kind != filehistory.RevisionRestore ||
		effective.RestoredFromRevisionID == nil ||
		*effective.RestoredFromRevisionID != first.Revision.RevisionID {
		t.Fatalf("restored file history = %#v", document)
	}
	raw, err := os.ReadFile(filepath.Join(root, "files", "plans", "q3.txt"))
	if err != nil || string(raw) != "snapshot" {
		t.Fatalf("materialized restored file = %q, %v", raw, err)
	}
	records, err := reopened.catalog.List(ctx, testWorkspaceID)
	if err != nil || len(records) < 3 ||
		records[len(records)-1].Trigger != snapshot.TriggerRestore {
		t.Fatalf("recovery snapshot = %#v, %v", records, err)
	}
	if _, err := os.Lstat(filepath.Join(
		root,
		".vibetable",
		"coordination",
		restoreJournalName,
	)); !os.IsNotExist(err) {
		t.Fatalf("committed restore left journal: %v", err)
	}
}

func containsRestorePreviewChange(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsRestorePreviewPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if len(value) >= len(prefix) && value[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func TestInterruptedInstalledSnapshotRestoreRollsBackBeforeReadiness(
	t *testing.T,
) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	if _, err := app.DB().NewQuery(`
		CREATE TABLE restore_probe (value TEXT NOT NULL);
		INSERT INTO restore_probe(value) VALUES ('snapshot');
	`).Execute(); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(root, ".vibetable", "audit")
	ledger, err := auditledger.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		RequestShutdown: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	token, _ := runtime.coordinator.Current()
	if _, err := runtime.history.Save(
		ctx,
		filehistory.SaveRequest{
			Token:      token,
			DocumentID: "22222222-2222-4222-8222-222222222222",
			Path:       "probe.txt",
			Kind:       filehistory.RevisionFormal,
			Content:    []byte("snapshot"),
			MimeType:   "text/plain",
			CreatedBy:  "test",
			DeviceID:   testClaimID,
		},
	); err != nil {
		t.Fatal(err)
	}
	target, _, err := runtime.snapshots.Capture(
		ctx,
		snapshot.CaptureRequest{
			WorkspaceID: testWorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerManual,
			Pinned:      true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().NewQuery(
		`UPDATE restore_probe SET value = 'old-live'`,
	).Execute(); err != nil {
		t.Fatal(err)
	}
	updateSettingsRaw, _ := json.Marshal(updateRetentionParams{
		ExpectedRevision:    1,
		SnapshotDays:        45,
		SnapshotCount:       60,
		SnapshotBuckets:     []string{"daily", "weekly"},
		FileRevisionDays:    40,
		FileRevisionCount:   120,
		FileRevisionBuckets: []string{"daily", "monthly"},
	})
	if _, err := runtime.updateRetention(
		ctx,
		nil,
		updateSettingsRaw,
	); err != nil {
		t.Fatal(err)
	}
	previewRaw, _ := json.Marshal(previewSnapshotRestoreParams{
		SnapshotID: target.SnapshotID,
		TargetMode: "currentWorkspace",
	})
	preview, err := runtime.previewSnapshotRestore(ctx, nil, previewRaw)
	if err != nil {
		t.Fatal(err)
	}
	planID := preview.(map[string]any)["planId"].(string)
	wire := json.RawMessage(`{
		"scope":"workspace",
		"workspaceId":"11111111-1111-4111-8111-111111111111",
		"sessionEpoch":7,
		"sequence":1,
		"operationId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	}`)
	applyRaw, _ := json.Marshal(applySnapshotRestoreParams{
		PlanID: planID, Confirmed: true,
	})
	if _, err := runtime.applySnapshotRestore(
		ctx,
		wire,
		applyRaw,
	); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}
	paths := mustResolvePaths(t, dataDir)
	statePath := filepath.Join(paths.coordination, "workspace-v2.db")
	stateBackup := statePath + ".restore-test-backup"
	if err := os.Rename(statePath, stateBackup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if installed, err := ApplyPendingSnapshotRestore(
		ctx,
		dataDir,
		testWorkspaceID,
	); err == nil || installed {
		t.Fatalf("settings staging fault = %v, %v", installed, err)
	}
	rollback := restoreRollbackRoot(
		paths,
		"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
	)
	if _, err := os.Lstat(rollback); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed requested attempt left rollback directory: %v", err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stateBackup, statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rollback, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(rollback, "stale"),
		[]byte("stale requested attempt"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if installed, err := ApplyPendingSnapshotRestore(
		ctx,
		dataDir,
		testWorkspaceID,
	); err != nil || !installed {
		t.Fatalf("initial offline install = %v, %v", installed, err)
	}
	// Simulate process death after installation but before Runtime health and
	// CompletePendingSnapshotRestore. The next startup must restore old-live.
	if installed, err := ApplyPendingSnapshotRestore(
		ctx,
		dataDir,
		testWorkspaceID,
	); err != nil || installed {
		t.Fatalf("interrupted restore recovery = %v, %v", installed, err)
	}
	rolledBackApp := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := rolledBackApp.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer rolledBackApp.ResetBootstrapState()
	var value string
	if err := rolledBackApp.DB().NewQuery(
		`SELECT value FROM restore_probe`,
	).Row(&value); err != nil || value != "old-live" {
		t.Fatalf("rollback database value = %q, %v", value, err)
	}
	rolledBackLedger, err := auditledger.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rolledBackLedger.Close()
	rolledBack, err := Open(ctx, Options{
		App: rolledBackApp, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: rolledBackLedger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rolledBack.Close(ctx)
	rolledBackSettings, _, err := rolledBack.state.retention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBackSettings.SnapshotDays != 45 ||
		rolledBackSettings.SnapshotCount != 60 ||
		rolledBackSettings.FileRevisionDays != 40 ||
		rolledBackSettings.FileRevisionCount != 120 {
		t.Fatalf(
			"rollback retention settings = %#v",
			rolledBackSettings,
		)
	}
	if _, found, err := readRestoreJournal(mustResolvePaths(t, dataDir)); err != nil || found {
		t.Fatalf("rollback left restore journal: found=%v err=%v", found, err)
	}
}

func TestProtectionSnapshotReceiptReusesPublishedSnapshotAfterRetry(
	t *testing.T,
) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
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
	before, err := runtime.catalog.List(ctx, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	first, err := runtime.protectionSnapshotForOperation(
		ctx,
		token,
		operationID,
		"snapshot.applyRestore.protection",
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.protectionSnapshotForOperation(
		ctx,
		token,
		operationID,
		"snapshot.applyRestore.protection",
	)
	if err != nil {
		t.Fatal(err)
	}
	after, err := runtime.catalog.List(ctx, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if first.SnapshotID != second.SnapshotID ||
		!first.Pinned ||
		len(after) != len(before)+1 {
		t.Fatalf(
			"protection retry first=%#v second=%#v before=%d after=%d",
			first,
			second,
			len(before),
			len(after),
		)
	}
}

func mustResolvePaths(t *testing.T, dataDir string) workspacePaths {
	t.Helper()
	paths, err := resolvePaths(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}
