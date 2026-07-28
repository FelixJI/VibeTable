package workspacev2

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
)

func TestWatcherPendingCopyPersistsAndAppliesWithIdentityCAS(t *testing.T) {
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
		App: app, DataDir: dataDir, WorkspaceID: testWorkspaceID,
		SessionEpoch: 7, FenceEpoch: 3, ClaimID: testClaimID,
		Ledger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	token, _ := runtime.coordinator.Current()
	first, err := runtime.history.Save(
		context.Background(),
		filehistory.SaveRequest{
			Token:      token,
			DocumentID: "22222222-2222-4222-8222-222222222222",
			Path:       "original.txt", Kind: filehistory.RevisionFormal,
			Content: []byte("same"), MimeType: "text/plain",
			CreatedBy: "test", DeviceID: testClaimID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "files", "copy.txt"),
		[]byte("same"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := runtime.watcher.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	listed := dispatch(
		t,
		runtime,
		1,
		"fileHistory.listPendingChanges",
		`{}`,
	)
	if listed.Error != nil {
		t.Fatalf("list pending = %#v", listed.Error)
	}
	changes := listed.Result.(map[string]any)["changes"].([]pendingFileChange)
	if len(changes) != 1 ||
		changes[0].RelativePath != "copy.txt" ||
		changes[0].Missing {
		t.Fatalf("pending changes = %#v", changes)
	}
	params, err := json.Marshal(map[string]any{
		"changeId":                    changes[0].ChangeID,
		"action":                      "copy",
		"documentId":                  first.Document.DocumentID,
		"expectedEffectiveRevisionId": first.Document.EffectiveRevisionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	applied := dispatch(
		t,
		runtime,
		2,
		"fileHistory.applyPendingChange",
		string(params),
	)
	if applied.Error != nil {
		t.Fatalf("apply pending = %#v", applied.Error)
	}
	documents := runtime.history.List()
	if len(documents) != 2 {
		t.Fatalf("copy did not create a new document: %#v", documents)
	}
	remaining, err := runtime.state.listPendingFileChanges(
		context.Background(),
	)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("pending change not cleared: %#v %v", remaining, err)
	}
}
