package filehistory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

// Restore must preserve the published state when a dependency fails, including
// cancellation after materialization but before the authoritative head CAS.
func TestRestoreFailuresKeepHeadHistoryMaterializationAndReceiptAtomic(t *testing.T) {
	for _, name := range []string{"cancel_after_materialization", "stage_create_failure", "permission_read", "missing_content", "stale_effective", "missing_revision"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			coordinator, err := writecoordinator.OpenPersistent(filepath.Join(root, "coordinator.db"), testWorkspaceID, 1, "claim-a", 1)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := coordinator.Close(); err != nil {
					t.Error(err)
				}
			}()
			token, _ := coordinator.Current()
			memory := objectrepo.NewMemory()
			if err := memory.AcceptAuthority(ctx, nil, token.Authority()); err != nil {
				t.Fatal(err)
			}
			repository := &restoreReadFailureRepository{Repository: memory}
			headStore, err := OpenPersistentHeadStore(filepath.Join(root, "head.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := headStore.Close(); err != nil {
					t.Error(err)
				}
			}()
			materializer, err := OpenMaterializer(filepath.Join(root, "files"), filepath.Join(root, "materializer"), repository)
			if err != nil {
				t.Fatal(err)
			}
			service, err := OpenCurrent(ctx, repository, coordinator, headStore, WithMaterializer(materializer))
			if err != nil {
				t.Fatal(err)
			}
			first, err := service.Save(ctx, SaveRequest{Token: token, DocumentID: testDocumentOne, Path: "restore.txt", Kind: RevisionFormal, Content: []byte("one"), MimeType: "text/plain", CreatedBy: "test-user", DeviceID: testDeviceID})
			if err != nil {
				t.Fatal(err)
			}
			second, err := service.Save(ctx, SaveRequest{Token: token, DocumentID: testDocumentOne, ExpectedEffectiveRevision: stringRef(first.Revision.RevisionID), Kind: RevisionFormal, Content: []byte("two"), MimeType: "text/plain", CreatedBy: "test-user", DeviceID: testDeviceID})
			if err != nil {
				t.Fatal(err)
			}
			beforeHead, found, err := headStore.Load(ctx, testWorkspaceID)
			if err != nil || !found {
				t.Fatalf("before head: found=%v err=%v", found, err)
			}
			beforeRoot := service.Root()
			beforeRecovery := coordinator.RecoveryState()
			request := RestoreRequest{Token: token, DocumentID: testDocumentOne, TargetRevisionID: first.Revision.RevisionID, ExpectedEffectiveRevision: stringRef(second.Revision.RevisionID), CreatedBy: "test-user", DeviceID: testDeviceID}
			operationID := "45555555-5555-4555-8555-555555555555"
			restoreCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			builder := func(publication Publication) (protocolv2.OperationReceipt, error) {
				if name == "cancel_after_materialization" {
					assertMaterializedContent(t, materializer.filesRoot, "restore.txt", "one")
					cancel()
					// The head CAS must observe the actual cancelled context.
				}
				return protocolv2.OperationReceipt{OperationID: operationID, WorkspaceID: testWorkspaceID, Method: "fileHistory.restore", Scope: protocolv2.WorkspaceScope, RequestHash: "sha256:restore-failure", Result: json.RawMessage(`{"restored":true}`)}, nil
			}
			restoreCtx, err = WithOperationReceiptBuilder(restoreCtx, builder)
			if err != nil {
				t.Fatal(err)
			}
			var expected error
			var blockedStage string
			switch name {
			case "cancel_after_materialization":
				expected = context.Canceled
			case "stage_create_failure":
				repository.target = first.Revision.ObjectID
				repository.beforeOpen = func() {
					blockedStage = filepath.Join(materializer.journalRoot,
						fmt.Sprintf("txn-%020d", coordinator.RecoveryState().PendingMutationRevision), "stage-000000")
					// Occupy the synthetic staging destination so the real exclusive
					// file creation fails after the current materialization is backed up.
					if err := os.WriteFile(blockedStage, []byte("occupied"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				expected = os.ErrExist
			case "permission_read":
				repository.target, repository.failure = first.Revision.ObjectID, os.ErrPermission
				expected = os.ErrPermission
			case "missing_content":
				repository.target, repository.failure = first.Revision.ObjectID, objectrepo.ErrNotFound
				expected = objectrepo.ErrNotFound
			case "stale_effective":
				request.ExpectedEffectiveRevision = stringRef(first.Revision.RevisionID)
				expected = ErrRevisionConflict
			case "missing_revision":
				request.TargetRevisionID = "46666666-6666-4666-8666-666666666666"
				expected = ErrRevisionNotFound
			}
			_, restoreErr := service.Restore(restoreCtx, request)
			if !errors.Is(restoreErr, expected) {
				t.Fatalf("restore error=%v want=%v", restoreErr, expected)
			}
			if name == "stage_create_failure" {
				var pathErr *os.PathError
				if blockedStage == "" || !errors.As(restoreErr, &pathErr) || pathErr.Op != "open" || pathErr.Path != blockedStage {
					t.Fatalf("restore did not fail creating the staged file: %v", restoreErr)
				}
			}
			repository.target, repository.failure, repository.beforeOpen = "", nil, nil
			document, err := service.Inspect(testDocumentOne)
			if err != nil || !reflect.DeepEqual(document, second.Document) {
				t.Fatalf("failed restore changed document: %#v err=%v", document, err)
			}
			head, found, err := headStore.Load(ctx, testWorkspaceID)
			if err != nil || !found || head != beforeHead || service.Root() != beforeRoot || coordinator.RecoveryState() != beforeRecovery {
				t.Fatalf("failed restore advanced published state: head=%#v found=%v err=%v recovery=%#v", head, found, err, coordinator.RecoveryState())
			}
			assertMaterializedContent(t, materializer.filesRoot, "restore.txt", "two")
			if _, found, err := headStore.LoadOperationReceipt(ctx, testWorkspaceID, operationID); err != nil || found {
				t.Fatalf("failed restore published receipt: found=%v err=%v", found, err)
			}
			if _, err := os.Lstat(materializer.journalPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed restore left journal: %v", err)
			}
			entries, err := os.ReadDir(materializer.journalRoot)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				// Windows directory enumeration can include a just-deleted name.
				// Reject every entry which still exists; do not wait or retry.
				_, statErr := os.Lstat(filepath.Join(materializer.journalRoot, entry.Name()))
				if !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("failed restore left staged entry %s: %v", entry.Name(), statErr)
				}
			}
			for _, revision := range second.Document.Revisions {
				reader, err := memory.Open(ctx, revision.ObjectID)
				if err != nil {
					t.Fatal(err)
				}
				content, readErr := io.ReadAll(reader)
				closeErr := reader.Close()
				want := "one"
				if revision.RevisionID == second.Revision.RevisionID {
					want = "two"
				}
				if readErr != nil || closeErr != nil || string(content) != want {
					t.Fatalf("old revision content changed: %q read=%v close=%v", content, readErr, closeErr)
				}
			}
			request.TargetRevisionID = first.Revision.RevisionID
			request.ExpectedEffectiveRevision = stringRef(second.Revision.RevisionID)
			restored, err := service.Restore(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if restored.Revision.RevisionOrdinal != second.Document.NextRevisionOrdinal || restored.Revision.FormalVersion == nil || *restored.Revision.FormalVersion != second.Document.NextFormalVersion || restored.Revision.ParentRevisionID == nil || *restored.Revision.ParentRevisionID != second.Revision.RevisionID || restored.Revision.RestoredFromRevisionID == nil || *restored.Revision.RestoredFromRevisionID != first.Revision.RevisionID || !reflect.DeepEqual(restored.Document.Revisions[:2], second.Document.Revisions) {
				t.Fatalf("recovery restore lost numbering or history: %#v", restored)
			}
			assertMaterializedContent(t, materializer.filesRoot, "restore.txt", "one")
		})
	}
}

type restoreReadFailureRepository struct {
	objectrepo.Repository
	target     objectrepo.ObjectID
	failure    error
	beforeOpen func()
}

func (repository *restoreReadFailureRepository) Open(ctx context.Context, id objectrepo.ObjectID) (io.ReadCloser, error) {
	if id == repository.target && repository.beforeOpen != nil {
		repository.beforeOpen()
	}
	if id == repository.target && repository.failure != nil {
		return nil, repository.failure
	}
	return repository.Repository.Open(ctx, id)
}
