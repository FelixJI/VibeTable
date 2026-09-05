package workspacev2

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

func TestMaterializeDiffPairWritesOnlyFixedFilesAndPostCASDetectsStale(t *testing.T) {
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
	ledger, err := auditledger.Open(filepath.Join(root, ".vibetable", "audit"))
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
	stopFileWatcher(t, runtime)
	token, _ := runtime.coordinator.Current()
	documentID := "22222222-2222-4222-8222-222222222222"
	first, err := runtime.history.Save(context.Background(), filehistory.SaveRequest{
		Token: token, DocumentID: documentID, Path: "plans/q3.txt",
		Kind: filehistory.RevisionFormal, Content: []byte("before"),
		MimeType: "text/plain", CreatedBy: "test", DeviceID: testClaimID,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.history.Save(context.Background(), filehistory.SaveRequest{
		Token: token, DocumentID: documentID,
		ExpectedEffectiveRevision: &first.Revision.RevisionID,
		Kind:                      filehistory.RevisionFormal, Content: []byte("after"),
		MimeType: "text/plain", CreatedBy: "test", DeviceID: testClaimID,
	})
	if err != nil {
		t.Fatal(err)
	}
	repositoryBefore := snapshotRepositoryFiles(t, runtime.paths.objects)
	historicalObjectBefore := readRepositoryObject(t, runtime, first.Revision.ObjectID)
	effectiveObjectBefore := readRepositoryObject(t, runtime, second.Revision.ObjectID)
	effectiveFile := filepath.Join(root, "files", "plans", "q3.txt")
	effectiveFileBefore := snapshotSourceFile(t, effectiveFile)

	destination := filepath.Join(t.TempDir(), "diff-session")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	grantID := "host-path-grant://cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	grant, err := json.Marshal(pathGrantEnvelope{
		GrantID: grantID, Method: "fileHistory.materializeDiffPair",
		OperationID: testOperationID, Purpose: "document-diff-materialize",
		Path: destination,
	})
	if err != nil {
		t.Fatal(err)
	}
	wire := json.RawMessage(`{
		"scope":"workspace",
		"workspaceId":"11111111-1111-4111-8111-111111111111",
		"sessionEpoch":7,
		"sequence":1,
		"operationId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	}`)
	params, err := json.Marshal(materializeDiffPairParams{
		DocumentID:                  documentID,
		HistoricalRevisionID:        first.Revision.RevisionID,
		ExpectedEffectiveRevisionID: second.Revision.RevisionID,
		PathGrant:                   grantID,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongEpoch := runtime.Dispatcher().DispatchEnvelope(
		WithPathGrantHeader(
			context.Background(),
			base64.RawURLEncoding.EncodeToString(grant),
		),
		requestJSON(t, 1, 8, "fileHistory.materializeDiffPair", string(params)),
	)
	if wrongEpoch.Error == nil {
		t.Fatal("wrong session epoch was accepted")
	}
	wrongWorkspaceRequest := requestJSON(
		t, 1, 7, "fileHistory.materializeDiffPair", string(params),
	)
	wrongWorkspaceRequest = []byte(strings.Replace(
		string(wrongWorkspaceRequest),
		testWorkspaceID,
		"99999999-9999-4999-8999-999999999999",
		1,
	))
	wrongWorkspace := runtime.Dispatcher().DispatchEnvelope(
		WithPathGrantHeader(
			context.Background(),
			base64.RawURLEncoding.EncodeToString(grant),
		),
		wrongWorkspaceRequest,
	)
	if wrongWorkspace.Error == nil {
		t.Fatal("wrong workspace was accepted")
	}
	result, err := runtime.materializeDiffPair(
		WithPathGrantHeader(
			context.Background(),
			base64.RawURLEncoding.EncodeToString(grant),
		),
		wire,
		params,
	)
	if err != nil {
		t.Fatal(err)
	}
	projection := result.(materializeDiffPairResult)
	if projection.DocumentID != documentID ||
		projection.HistoricalRevisionID != first.Revision.RevisionID ||
		projection.HistoricalContentHash != first.Revision.ContentHash ||
		projection.EffectiveRevisionID != second.Revision.RevisionID ||
		projection.EffectiveContentHash != second.Revision.ContentHash {
		t.Fatalf("result = %#v", projection)
	}
	for name, expected := range map[string]string{
		DiffHistoricalFileName: "before",
		DiffEffectiveFileName:  "after",
	} {
		content, readErr := os.ReadFile(filepath.Join(destination, name))
		if readErr != nil || string(content) != expected {
			t.Fatalf("%s = %q, %v", name, content, readErr)
		}
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
	assertParams := json.RawMessage(`{"documentId":"` + documentID +
		`","historicalRevisionId":"` + first.Revision.RevisionID +
		`","expectedHistoricalContentHash":"` + first.Revision.ContentHash +
		`","expectedEffectiveRevisionId":"` + second.Revision.RevisionID +
		`","expectedEffectiveContentHash":"` + second.Revision.ContentHash + `"}`)
	assertion, err := runtime.assertEffectiveRevision(
		context.Background(), wire, assertParams,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertRepositoryFilesUnchanged(t, runtime.paths.objects, repositoryBefore)
	if actual := readRepositoryObject(t, runtime, first.Revision.ObjectID); !bytes.Equal(actual, historicalObjectBefore) {
		t.Fatalf("historical repository object changed: %q", actual)
	}
	if actual := readRepositoryObject(t, runtime, second.Revision.ObjectID); !bytes.Equal(actual, effectiveObjectBefore) {
		t.Fatalf("effective repository object changed: %q", actual)
	}
	assertSourceFileUnchanged(t, effectiveFile, effectiveFileBefore)
	if actual := assertion.(map[string]any); actual["documentId"] != documentID ||
		actual["historicalRevisionId"] != first.Revision.RevisionID ||
		actual["historicalContentHash"] != first.Revision.ContentHash ||
		actual["effectiveRevisionId"] != second.Revision.RevisionID ||
		actual["effectiveContentHash"] != second.Revision.ContentHash ||
		actual["stable"] != true || len(actual) != 6 {
		t.Fatalf("assertion = %#v", actual)
	}
	invalidHashParams := json.RawMessage(`{"documentId":"` + documentID +
		`","historicalRevisionId":"` + first.Revision.RevisionID +
		`","expectedHistoricalContentHash":"sha256:not-a-valid-hash","expectedEffectiveRevisionId":"` + second.Revision.RevisionID +
		`","expectedEffectiveContentHash":"` + second.Revision.ContentHash + `"}`)
	if _, err := runtime.assertEffectiveRevision(
		context.Background(), wire, invalidHashParams,
	); err == nil || err.Error() != "file_history.request_invalid" {
		t.Fatalf("invalid hash error = %v", err)
	}

	third, err := runtime.history.Save(context.Background(), filehistory.SaveRequest{
		Token: token, DocumentID: documentID,
		ExpectedEffectiveRevision: &second.Revision.RevisionID,
		Kind:                      filehistory.RevisionFormal, Content: []byte("latest"),
		MimeType: "text/plain", CreatedBy: "test", DeviceID: testClaimID,
	})
	if err != nil || third.Revision.RevisionID == "" {
		t.Fatal(err)
	}
	if _, err := runtime.assertEffectiveRevision(
		context.Background(), wire, assertParams,
	); !errors.Is(err, filehistory.ErrEffectiveRevisionStale) {
		t.Fatalf("post CAS error = %v", err)
	}
	publicResponse := runtime.Dispatcher().DispatchEnvelope(
		context.Background(),
		requestJSON(t, 1, 7, "fileHistory.assertEffectiveRevision", string(assertParams)),
	)
	if publicResponse.Error == nil ||
		publicResponse.Error.Code != "filehistory.effective_revision_stale" {
		t.Fatalf("public post CAS error = %#v", publicResponse.Error)
	}
}

type sourceFileSnapshot struct {
	content []byte
	modTime time.Time
}

func snapshotRepositoryFiles(t *testing.T, root string) map[string]sourceFileSnapshot {
	t.Helper()
	files := map[string]sourceFileSnapshot{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("repository source contains a symlink")
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[relative] = snapshotSourceFile(t, path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func assertRepositoryFilesUnchanged(
	t *testing.T,
	root string,
	before map[string]sourceFileSnapshot,
) {
	t.Helper()
	after := snapshotRepositoryFiles(t, root)
	if len(after) != len(before) {
		t.Fatalf("repository file count changed: before=%d after=%d", len(before), len(after))
	}
	for path, expected := range before {
		actual, ok := after[path]
		if !ok || !bytes.Equal(actual.content, expected.content) || !actual.modTime.Equal(expected.modTime) {
			t.Fatalf("repository source changed: %s", path)
		}
	}
}

func snapshotSourceFile(t *testing.T, path string) sourceFileSnapshot {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return sourceFileSnapshot{content: content, modTime: info.ModTime()}
}

func assertSourceFileUnchanged(t *testing.T, path string, before sourceFileSnapshot) {
	t.Helper()
	after := snapshotSourceFile(t, path)
	if !bytes.Equal(after.content, before.content) || !after.modTime.Equal(before.modTime) {
		t.Fatalf("source file changed: %s", path)
	}
}

func readRepositoryObject(t *testing.T, runtime *Runtime, id objectrepo.ObjectID) []byte {
	t.Helper()
	reader, err := runtime.repository.Open(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	content, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatal(errors.Join(readErr, closeErr))
	}
	return content
}
