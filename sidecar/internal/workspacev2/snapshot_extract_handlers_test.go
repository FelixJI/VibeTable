package workspacev2

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
)

func TestSnapshotExtractUsesDurablePlanAndDoesNotChangeEffectiveRevision(
	t *testing.T,
) {
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
	runtime, err := Open(context.Background(), Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())

	token, _ := runtime.coordinator.Current()
	documentID := "22222222-2222-4222-8222-222222222222"
	saved, err := runtime.history.Save(
		context.Background(),
		filehistory.SaveRequest{
			Token: token, DocumentID: documentID,
			Path: "plans/q3.txt", Kind: filehistory.RevisionFormal,
			Content: []byte("snapshot copy"), MimeType: "text/plain",
			CreatedBy: "test", DeviceID: testClaimID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	record, created, err := runtime.snapshots.Capture(
		context.Background(),
		snapshot.CaptureRequest{
			WorkspaceID: testWorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerManual,
			Pinned:      true,
		},
	)
	if err != nil || !created {
		t.Fatalf("capture = %#v, %v, %v", record, created, err)
	}
	previewRaw, err := json.Marshal(previewSnapshotExtractParams{
		SnapshotID: record.SnapshotID,
		DocumentID: documentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	previewResult, err := runtime.previewSnapshotExtract(
		context.Background(),
		nil,
		previewRaw,
	)
	if err != nil {
		t.Fatal(err)
	}
	planID := previewResult.(map[string]any)["planId"].(string)
	target := filepath.Join(t.TempDir(), "q3-copy.txt")
	grantID := "host-path-grant://cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	grantRaw, err := json.Marshal(pathGrantEnvelope{
		GrantID: grantID, Method: "snapshot.applyExtract",
		OperationID: testOperationID, Purpose: "snapshot-extract",
		Path: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPathGrantHeader(
		context.Background(),
		base64.RawURLEncoding.EncodeToString(grantRaw),
	)
	wireRaw := json.RawMessage(`{
		"scope":"workspace",
		"workspaceId":"11111111-1111-4111-8111-111111111111",
		"sessionEpoch":7,
		"sequence":1,
		"operationId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	}`)
	paramsRaw, err := json.Marshal(applySnapshotExtractParams{
		PlanID: planID, PathGrant: grantID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.applySnapshotExtract(
		ctx,
		wireRaw,
		paramsRaw,
	); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "snapshot copy" {
		t.Fatalf("extracted = %q, %v", content, err)
	}
	current, err := runtime.history.Inspect(documentID)
	if err != nil ||
		current.EffectiveRevisionID != saved.Revision.RevisionID ||
		len(current.Revisions) != 1 {
		t.Fatalf("extract changed current history = %#v, %v", current, err)
	}
	if _, err := runtime.state.snapshotExtractPlan(
		context.Background(),
		planID,
	); err == nil || err.Error() != "snapshot.extract_plan_not_found" {
		t.Fatalf("consumed extract plan remained available: %v", err)
	}
	verification, err := runtime.verifyRepository(
		context.Background(),
		nil,
		json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	result := verification.(map[string]any)
	if result["state"] != "verified" ||
		result["snapshotCount"] != 1 ||
		result["objectCount"].(int) == 0 {
		t.Fatalf("verification = %#v", result)
	}
}
