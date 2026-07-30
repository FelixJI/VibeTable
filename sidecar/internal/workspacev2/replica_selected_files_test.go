package workspacev2

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
)

func TestSelectedFilesThreeWayReconcileRenameAndConflict(t *testing.T) {
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

	conflicts, err := conflictresolution.OpenEngine(
		filepath.Join(
			root,
			".vibetable",
			"coordination",
			"selected-files-conflicts.db",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conflicts.Close()
	selectedRoot := filepath.Join(t.TempDir(), "selected")
	if err := os.MkdirAll(
		filepath.Join(selectedRoot, "files"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	owner := &productionReplicaConflict{
		runtime:           runtime,
		conflicts:         conflicts,
		selectedRoot:      selectedRoot,
		activityFilesRoot: filepath.Join(root, "files"),
		selectedFilesBasePath: filepath.Join(
			root,
			".vibetable",
			"coordination",
			"selected-files-base.json",
		),
	}

	token, _ := runtime.coordinator.Current()
	const documentID = "22222222-2222-4222-8222-222222222222"
	first, err := runtime.history.Save(ctx, filehistory.SaveRequest{
		Token: token, DocumentID: documentID,
		Path: "docs/report.txt", Kind: filehistory.RevisionFormal,
		Content: []byte("local-v1"), MimeType: "text/plain",
		CreatedBy: "test", DeviceID: testClaimID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.scanSelectedFiles(ctx); err != nil {
		t.Fatal(err)
	}
	assertSelectedContent(
		t,
		filepath.Join(selectedRoot, "files", "docs", "report.txt"),
		"local-v1",
	)

	remotePath := filepath.Join(
		selectedRoot,
		"files",
		"docs",
		"report.txt",
	)
	if err := os.WriteFile(remotePath, []byte("remote-v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := owner.scanSelectedFiles(ctx); err != nil {
		t.Fatal(err)
	}
	assertSelectedContent(
		t,
		filepath.Join(root, "files", "docs", "report.txt"),
		"remote-v2",
	)

	renamedRemote := filepath.Join(
		selectedRoot,
		"files",
		"renamed",
		"report.txt",
	)
	if err := os.MkdirAll(filepath.Dir(renamedRemote), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(remotePath, renamedRemote); err != nil {
		t.Fatal(err)
	}
	if err := owner.scanSelectedFiles(ctx); err != nil {
		t.Fatal(err)
	}
	document, err := runtime.history.Inspect(documentID)
	if err != nil {
		t.Fatal(err)
	}
	if document.RelativePath != "renamed/report.txt" {
		t.Fatalf("renamed path = %q", document.RelativePath)
	}
	if _, err := os.Stat(
		filepath.Join(root, "files", "docs", "report.txt"),
	); !os.IsNotExist(err) {
		t.Fatalf("old activity path remains: %v", err)
	}

	token, _ = runtime.coordinator.Current()
	local, err := runtime.history.Save(ctx, filehistory.SaveRequest{
		Token: token, DocumentID: documentID,
		Path:                      "renamed/report.txt",
		ExpectedEffectiveRevision: &document.EffectiveRevisionID,
		Kind:                      filehistory.RevisionFormal,
		Content:                   []byte("local-v3"), MimeType: "text/plain",
		CreatedBy: "test", DeviceID: testClaimID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if local.Revision.RevisionID == first.Revision.RevisionID {
		t.Fatal("local revision did not advance")
	}
	if err := os.WriteFile(
		renamedRemote,
		[]byte("remote-v3"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := owner.scanSelectedFiles(ctx); err != nil {
		t.Fatal(err)
	}
	assertSelectedContent(
		t,
		filepath.Join(root, "files", "renamed", "report.txt"),
		"local-v3",
	)
	assertSelectedContent(t, renamedRemote, "remote-v3")
	sets, _, err := conflicts.List(
		ctx,
		testWorkspaceID,
		nil,
		10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 ||
		sets[0].State != conflictresolution.StatePending ||
		sets[0].Base.Files[documentID].ContentID == "" ||
		sets[0].Local.Files[documentID].ContentID == "" ||
		sets[0].Replica.Files[documentID].ContentID == "" {
		t.Fatalf("selected-file conflict = %#v", sets)
	}
}

func assertSelectedContent(t *testing.T, path string, expected string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != expected {
		t.Fatalf("%s = %q, want %q", path, raw, expected)
	}
}
