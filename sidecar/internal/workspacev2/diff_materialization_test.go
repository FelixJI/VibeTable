package workspacev2

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
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
