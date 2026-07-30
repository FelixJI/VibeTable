package workspacev2

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
)

func TestImportedRestoreAuditIsLocalAndIdempotentAcrossCompletionReplay(
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
	if _, err := runtime.history.Save(ctx, filehistory.SaveRequest{
		Token:      token,
		DocumentID: "77777777-7777-4777-8777-777777777777",
		Path:       "import-audit.txt", Kind: filehistory.RevisionFormal,
		Content:  []byte("target-local"),
		MimeType: "text/plain", CreatedBy: "test",
		DeviceID: testClaimID,
	}); err != nil {
		t.Fatal(err)
	}
	head, found, err := runtime.headStore.Load(ctx, testWorkspaceID)
	if err != nil || !found {
		t.Fatalf("head=%#v found=%v err=%v", head, found, err)
	}
	next := head
	next.Revision++
	next.MutationRevision++
	occurredAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	journal := pendingSnapshotRestore{
		FormatVersion:        1,
		OperationID:          "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		WorkspaceID:          testWorkspaceID,
		SnapshotID:           "22222222-2222-4222-8222-222222222222",
		SourceWorkspaceID:    "99999999-9999-4999-8999-999999999999",
		SourceSnapshotID:     "88888888-8888-4888-8888-888888888888",
		CompletionOccurredAt: occurredAt.Format(time.RFC3339Nano),
		ProtectionSnapshotID: "33333333-3333-4333-8333-333333333333",
		Phase:                restorePhaseInstalled,
		PreviousHead:         head,
		NextHead:             next,
		DatabaseHash:         "sha256:database",
		SettingsHash:         "sha256:settings",
		Files:                map[string]restoreStagedFile{},
		Sequence:             1,
		Method:               "snapshot.applyRestore",
		Scope:                "workspace",
		RequestHash:          "sha256:request",
		Result:               json.RawMessage(`{"state":"prepared"}`),
	}
	paths, err := resolvePaths(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRestoreJournal(paths, journal); err != nil {
		t.Fatal(err)
	}
	restoredPayload, _ := json.Marshal(map[string]any{
		"type":                 "workspace.snapshotRestored",
		"workspaceId":          journal.WorkspaceID,
		"snapshotId":           journal.SnapshotID,
		"protectionSnapshotId": journal.ProtectionSnapshotID,
		"operationId":          journal.OperationID,
	})
	restoredEnvelope, _ := auditledger.NewEnvelope(
		"snapshot-restore:"+journal.OperationID,
		"snapshot-restore:"+journal.OperationID,
		1,
		"snapshot-restore:"+journal.OperationID,
		restoredPayload,
		occurredAt,
	)
	if _, err := ledger.Append(ctx, restoredEnvelope); err != nil {
		t.Fatal(err)
	}
	importPayload, _ := json.Marshal(map[string]any{
		"type":              "workspace.snapshotImported",
		"sourceWorkspaceId": journal.SourceWorkspaceID,
		"sourceSnapshotId":  journal.SourceSnapshotID,
		"targetWorkspaceId": journal.WorkspaceID,
		"targetSnapshotId":  journal.SnapshotID,
		"operationId":       journal.OperationID,
	})
	importEnvelope, _ := auditledger.NewEnvelope(
		"snapshot-import:"+journal.OperationID,
		"snapshot-import:"+journal.OperationID,
		1,
		"snapshot-import:"+journal.OperationID,
		importPayload,
		occurredAt,
	)
	if _, err := ledger.Append(ctx, importEnvelope); err != nil {
		t.Fatal(err)
	}
	restoreReceipt := protocolv2.OperationReceipt{
		OperationID: journal.OperationID,
		WorkspaceID: journal.WorkspaceID,
		Method:      journal.Method,
		Scope:       protocolv2.WorkspaceScope,
		RequestHash: journal.RequestHash,
		Result:      append(json.RawMessage(nil), journal.Result...),
	}
	captureContext, err := snapshot.WithOperationReceiptBuilder(
		ctx,
		func(snapshot.Record) (protocolv2.OperationReceipt, error) {
			return restoreReceipt, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovery, _, err := runtime.snapshots.Capture(
		captureContext,
		snapshot.CaptureRequest{
			WorkspaceID: testWorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerRestore,
			Pinned:      true,
		},
	); err != nil || recovery.SnapshotID == "" {
		t.Fatalf("pre-crash recovery snapshot=%#v err=%v", recovery, err)
	}
	beforeCompletion, err := runtime.catalog.List(ctx, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}

	// Models a process dying after both local audit appends and the recovery
	// snapshot catalog publication, but before the central receipt and journal
	// completion were committed.
	if err := runtime.CompletePendingSnapshotRestore(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CompletePendingSnapshotRestore(ctx); err != nil {
		t.Fatal(err)
	}
	afterCompletion, err := runtime.catalog.List(ctx, testWorkspaceID)
	if err != nil ||
		len(afterCompletion) != len(beforeCompletion) {
		t.Fatalf(
			"recovery snapshot repeated: before=%d after=%d err=%v",
			len(beforeCompletion),
			len(afterCompletion),
			err,
		)
	}
	var imported, restored, unexpectedSource int
	for _, record := range ledger.Records(0, 100) {
		var payload map[string]any
		if json.Unmarshal(record.Envelope.Payload, &payload) != nil {
			t.Fatal("invalid local audit payload")
		}
		switch payload["type"] {
		case "workspace.snapshotImported":
			imported++
			if payload["sourceWorkspaceId"] !=
				journal.SourceWorkspaceID ||
				payload["sourceSnapshotId"] != journal.SourceSnapshotID ||
				payload["targetWorkspaceId"] != journal.WorkspaceID {
				t.Fatalf("import payload = %#v", payload)
			}
		case "workspace.snapshotRestored":
			restored++
		case "source.fake":
			unexpectedSource++
		}
	}
	if imported != 1 || restored != 1 || unexpectedSource != 0 {
		t.Fatalf(
			"local audit counts imported=%d restored=%d source=%d records=%#v",
			imported,
			restored,
			unexpectedSource,
			ledger.Records(0, 100),
		)
	}
	if err := ledger.Verify(); err != nil {
		t.Fatalf("target local ledger invalid: %v", err)
	}
}
