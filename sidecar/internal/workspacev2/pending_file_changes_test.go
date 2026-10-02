package workspacev2

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

func TestWatcherPendingCopyPersistsAndAppliesWithIdentityCAS(t *testing.T) {
	runtime, root := newPendingChangeRuntime(t)
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

func TestWatcherPendingDeleteRejectsMovedIdentity(t *testing.T) {
	for _, target := range []string{"current-missing", "renamed.txt", "archive/original.txt"} {
		t.Run(target, func(t *testing.T) {
			runtime, root := newPendingChangeRuntime(t)
			token, _ := runtime.coordinator.Current()
			content := []byte("synthetic pending identity regression")
			first, err := runtime.history.Save(context.Background(), filehistory.SaveRequest{
				Token: token, DocumentID: "22222222-2222-4222-8222-222222222222",
				Path: "original.txt", Kind: filehistory.RevisionFormal, Content: content,
				MimeType: "text/plain", CreatedBy: "test", DeviceID: testClaimID,
			})
			if err != nil {
				t.Fatal(err)
			}
			originalPath := filepath.Join(root, "files", "original.txt")
			targetPath := filepath.Join(root, "files", filepath.FromSlash(target))
			if target == "current-missing" {
				targetPath = filepath.Join(root, "removed-from-files.txt")
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(originalPath, targetPath); err != nil {
				t.Fatal(err)
			}
			if err := runtime.watcher.Rescan(context.Background()); err != nil {
				t.Fatal(err)
			}
			listed := dispatch(t, runtime, 1, "fileHistory.listPendingChanges", `{}`)
			if listed.Error != nil {
				t.Fatalf("list pending = %#v", listed.Error)
			}
			changes := listed.Result.(map[string]any)["changes"].([]pendingFileChange)
			if target == "current-missing" {
				if len(changes) != 1 || !changes[0].Missing || changes[0].RelativePath != "original.txt" {
					t.Fatalf("current missing observation = %#v", changes)
				}
				params, err := json.Marshal(map[string]any{
					"changeId": changes[0].ChangeID, "action": "delete",
					"documentId":                  first.Document.DocumentID,
					"expectedEffectiveRevisionId": first.Document.EffectiveRevisionID,
				})
				if err != nil {
					t.Fatal(err)
				}
				deleted := dispatch(t, runtime, 2, "fileHistory.applyPendingChange", string(params))
				if deleted.Error != nil {
					t.Fatalf("current missing delete = %+v", deleted.Error)
				}
				final, err := runtime.history.Inspect(first.Document.DocumentID)
				if err != nil || final.Status != filehistory.DocumentDeleted ||
					final.RelativePath != "original.txt" || final.EffectiveRevisionID != first.Document.EffectiveRevisionID ||
					!reflect.DeepEqual(final.Revisions, first.Document.Revisions) {
					t.Fatalf("current missing deletion = %#v, %v", final, err)
				}
				remaining, err := runtime.state.listPendingFileChanges(context.Background())
				if err != nil || len(remaining) != 0 {
					t.Fatalf("confirmed missing observation retained = %#v, %v", remaining, err)
				}
				preserved, err := os.ReadFile(targetPath)
				if err != nil || string(preserved) != string(content) {
					t.Fatalf("delete touched the file outside files/: %q, %v", preserved, err)
				}
				return
			}
			var missing, moved pendingFileChange
			for _, change := range changes {
				switch change.RelativePath {
				case "original.txt":
					missing = change
				case target:
					moved = change
				}
			}
			if len(changes) != 2 || !missing.Missing || moved.Missing || moved.ChangeID == "" {
				t.Fatalf("move observations = %#v", changes)
			}
			params, err := json.Marshal(map[string]any{
				"changeId": moved.ChangeID, "action": "move",
				"documentId":                  first.Document.DocumentID,
				"expectedEffectiveRevisionId": first.Document.EffectiveRevisionID,
			})
			if err != nil {
				t.Fatal(err)
			}
			applied := dispatch(t, runtime, 2, "fileHistory.applyPendingChange", string(params))
			if applied.Error != nil {
				t.Fatalf("confirm move = %#v", applied.Error)
			}
			before, err := runtime.history.Inspect(first.Document.DocumentID)
			if err != nil || before.RelativePath != target || before.Status != filehistory.DocumentActive ||
				before.EffectiveRevisionID != first.Document.EffectiveRevisionID {
				t.Fatalf("moved identity = %#v, %v", before, err)
			}
			staleObservation, err := runtime.state.pendingFileChange(context.Background(), missing.ChangeID)
			if err != nil || !reflect.DeepEqual(staleObservation, missing) {
				t.Fatalf("old missing observation = %#v, %v", staleObservation, err)
			}
			params, err = json.Marshal(map[string]any{
				"changeId": missing.ChangeID, "action": "delete",
				"documentId":                  before.DocumentID,
				"expectedEffectiveRevisionId": before.EffectiveRevisionID,
			})
			if err != nil {
				t.Fatal(err)
			}
			rejected := dispatch(t, runtime, 3, "fileHistory.applyPendingChange", string(params))
			if rejected.Error == nil || rejected.Error.Code != "file_history.pending_change_stale" {
				t.Errorf("old missing delete must be stale: result=%#v error=%+v", rejected.Result, rejected.Error)
			}
			after, err := runtime.history.Inspect(before.DocumentID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Errorf("old missing delete changed the moved document: before=%#v after=%#v err=%v", before, after, err)
			}
			materialized, err := os.ReadFile(targetPath)
			if err != nil || string(materialized) != string(content) {
				t.Errorf("old missing delete changed the moved bytes: %q, %v", materialized, err)
			}
			if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
				t.Errorf("old missing delete recreated the old path: %v", err)
			}
			remaining, err := runtime.state.pendingFileChange(context.Background(), missing.ChangeID)
			if err != nil || !reflect.DeepEqual(remaining, staleObservation) {
				t.Errorf("rejected delete changed the old pending observation: %#v, %v", remaining, err)
			}
		})
	}
}

func newPendingChangeRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	t.Cleanup(func() { app.ResetBootstrapState() })
	ledger, err := auditledger.Open(filepath.Join(root, ".vibetable", "audit"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ledger.Close(); err != nil {
			t.Errorf("close pending-change ledger: %v", err)
		}
	})
	runtime, err := Open(context.Background(), Options{
		App: app, DataDir: dataDir, WorkspaceID: testWorkspaceID,
		SessionEpoch: 7, FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		DeferBackgroundWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Errorf("close pending-change runtime: %v", err)
		}
	})
	return runtime, root
}

// coordinator.acquire evaluates ctx.Done before selecting the write gate.
// This supplies a deterministic external Rename after the caller's observation,
// without a production hook, timing sleep, or a second dispatched UI operation.
type pendingDeleteInterleaveContext struct {
	context.Context
	once   sync.Once
	rename func()
}

func (ctx *pendingDeleteInterleaveContext) Done() <-chan struct{} {
	ctx.once.Do(ctx.rename)
	return ctx.Context.Done()
}

func TestPendingDeletePathBindingRejectsRenameBeforeWriteGate(t *testing.T) {
	runtime, root := newPendingChangeRuntime(t)
	ctx := context.Background()
	token, _ := runtime.coordinator.Current()
	content := []byte("F1 immutable synthetic pending deletion")
	first, err := runtime.history.Save(ctx, filehistory.SaveRequest{
		Token: token, DocumentID: "22222222-2222-4222-8222-222222222222",
		Path: "original.txt", Kind: filehistory.RevisionFormal, Content: content,
		MimeType: "text/plain", CreatedBy: "test", DeviceID: testClaimID,
	})
	if err != nil {
		t.Fatal(err)
	}
	originalPath := filepath.Join(root, "files", "original.txt")
	removedPath := filepath.Join(root, "external-original.txt")
	if err := os.Rename(originalPath, removedPath); err != nil {
		t.Fatal(err)
	}
	if err := runtime.watcher.Rescan(ctx); err != nil {
		t.Fatal(err)
	}
	changes, err := runtime.state.listPendingFileChanges(ctx)
	if err != nil || len(changes) != 1 || !changes[0].Missing {
		t.Fatalf("missing pending = %#v, %v", changes, err)
	}
	pending := changes[0]
	// The old handler's preflight succeeds, but neither path nor effective CAS
	// prevents an external Rename from committing before Delete's own gate.
	observed, err := runtime.history.Inspect(first.Document.DocumentID)
	if err != nil || observed.RelativePath != pending.RelativePath {
		t.Fatalf("preflight path = %#v, %v", observed, err)
	}
	if _, err := runtime.watcher.ReadStable(ctx, pending.RelativePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("observed path is not truly missing: %v", err)
	}
	beforeHead, found, err := runtime.headStore.Load(ctx, testWorkspaceID)
	if err != nil || !found {
		t.Fatalf("initial head found=%v err=%v", found, err)
	}
	beforeRoot := runtime.history.Root()
	beforeRecovery := runtime.coordinator.RecoveryState()
	before := observed
	operationID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	builderCalled := false
	mutationContext, err := filehistory.WithOperationReceiptBuilder(
		&pendingDeleteInterleaveContext{Context: ctx, rename: func() {
			moved, err := runtime.ingestor.Ingest(ctx, filehistory.ExternalChange{
				Token: token, Kind: filehistory.ExternalRename,
				DocumentID: observed.DocumentID, SourcePath: observed.RelativePath,
				TargetPath:                "archive/renamed.txt",
				ExpectedEffectiveRevision: &observed.EffectiveRevisionID,
				CreatedBy:                 "replica-selected-root", DeviceID: testClaimID,
			})
			if err != nil || moved.Mutation == nil || moved.Confirmation != nil {
				t.Fatalf("background rename = %#v, %v", moved, err)
			}
			before = moved.Mutation.Document
			if before.RelativePath != "archive/renamed.txt" ||
				before.EffectiveRevisionID != observed.EffectiveRevisionID ||
				!reflect.DeepEqual(before.Revisions, observed.Revisions) {
				t.Fatalf("rename changed revision identity: %#v", before)
			}
			head, found, err := runtime.headStore.Load(ctx, testWorkspaceID)
			if err != nil || !found {
				t.Fatalf("renamed head found=%v err=%v", found, err)
			}
			beforeHead = head
			beforeRoot = runtime.history.Root()
			beforeRecovery = runtime.coordinator.RecoveryState()
			t.Log("controlled order: missing/path preflight -> direct ingestor Rename -> Delete write gate")
		}},
		func(filehistory.Publication) (protocolv2.OperationReceipt, error) {
			builderCalled = true
			return protocolv2.OperationReceipt{OperationID: operationID, WorkspaceID: testWorkspaceID,
				Method: "fileHistory.applyPendingChange", Scope: protocolv2.WorkspaceScope,
				RequestHash: "sha256:pending-delete-interleave", Result: json.RawMessage(`{"state":"applied"}`)}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.history.Delete(mutationContext, token, observed.DocumentID,
		&observed.EffectiveRevisionID, pending.RelativePath)
	if !errors.Is(err, filehistory.ErrPathStale) {
		t.Errorf("delete after rename error=%v, want path stale", err)
	}
	after, err := runtime.history.Inspect(observed.DocumentID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Errorf("stale delete changed document/history: before=%#v after=%#v err=%v", before, after, err)
	}
	afterHead, found, err := runtime.headStore.Load(ctx, testWorkspaceID)
	if err != nil || !found || afterHead != beforeHead || runtime.history.Root() != beforeRoot ||
		runtime.coordinator.RecoveryState() != beforeRecovery {
		t.Errorf("stale delete advanced head/root/coordinator state")
	}
	for _, candidate := range []string{filepath.Join(root, "files", "archive", "renamed.txt"), removedPath} {
		raw, err := os.ReadFile(candidate)
		if err != nil || string(raw) != string(content) {
			t.Errorf("stale delete changed synthetic bytes %s: %q, %v", candidate, raw, err)
		}
	}
	if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
		t.Errorf("stale delete recreated old missing path: %v", err)
	}
	remaining, err := runtime.state.pendingFileChange(ctx, pending.ChangeID)
	if err != nil || !reflect.DeepEqual(remaining, pending) {
		t.Errorf("stale delete changed pending: %#v, %v", remaining, err)
	}
	if builderCalled {
		t.Error("stale delete reached operation receipt publication")
	}
	if _, found, err := runtime.headStore.LoadOperationReceipt(ctx, testWorkspaceID, operationID); err != nil || found {
		t.Errorf("stale delete persisted authority receipt: found=%v err=%v", found, err)
	}
	if _, found, err := runtime.state.loadOperationReceipt(ctx, testWorkspaceID, operationID); err != nil || found {
		t.Errorf("stale delete persisted replay receipt: found=%v err=%v", found, err)
	}
}
