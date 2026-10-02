package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func TestE2EFileRestoreReadOnlyDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	db, err := openE2EFileRestoreReadOnly(path)
	if db != nil {
		_ = db.Close()
	}
	if err == nil {
		t.Fatal("read-only open accepted a nonexistent database")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only open created a database: %v", err)
	}
}

func TestE2EFileRestoreBarrierRequiresAbsoluteTestDirectory(t *testing.T) {
	for _, directory := range []string{"", "relative-controls"} {
		t.Setenv(e2eFileRestoreBarrierEnvironment, directory)
		if newE2EFileRestoreBarrierFromEnvironment("", "") != nil {
			t.Fatal("invalid directory enabled Restore barrier")
		}
	}
}

func TestE2EFileRestoreBarrierIgnoresUnrelatedWritesAndMatchesRealRestore(t *testing.T) {
	ctx := context.Background()
	root, controls := t.TempDir(), t.TempDir()
	metadata := filepath.Join(root, ".vibetable")
	dataDir := filepath.Join(metadata, "data")
	for _, directory := range []string{dataDir, filepath.Join(metadata, "topology"), filepath.Join(metadata, "coordination")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	workspaceID := "11111111-1111-4111-8111-111111111111"
	documentID := "22222222-2222-4222-8222-222222222222"
	claimID := "33333333-3333-4333-8333-333333333333"
	operationID := "44444444-4444-4444-8444-444444444444"
	coordinatorPath := filepath.Join(metadata, "coordination", "write-coordinator.db")
	coordinator, err := writecoordinator.OpenPersistent(coordinatorPath, workspaceID, 3, claimID, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	token, _ := coordinator.Current()
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(ctx, nil, token.Authority()); err != nil {
		t.Fatal(err)
	}
	headStore, err := filehistory.OpenPersistentHeadStore(filepath.Join(metadata, "topology", "filehistory-head.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer headStore.Close()
	materializer, err := filehistory.OpenMaterializer(filepath.Join(root, "files"), filepath.Join(metadata, "coordination", "file-materializer"), repository)
	if err != nil {
		t.Fatal(err)
	}
	service, err := filehistory.OpenCurrent(ctx, repository, coordinator, headStore, filehistory.WithMaterializer(materializer))
	if err != nil {
		t.Fatal(err)
	}
	save := func(id, filePath, content string, expected *string) filehistory.SaveResult {
		t.Helper()
		result, err := service.Save(ctx, filehistory.SaveRequest{Token: token, DocumentID: id, Path: filePath,
			Kind: filehistory.RevisionFormal, Content: []byte(content), MimeType: "text/plain",
			CreatedBy: "test", DeviceID: claimID, ExpectedEffectiveRevision: expected})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := save(documentID, "restore-crash-44.txt", "historical", nil)
	second := save(documentID, "", "newer", &first.Revision.RevisionID)
	arm := e2eFileRestoreArm{WorkspaceID: workspaceID, OperationID: operationID, DocumentID: documentID,
		HistoricalRevisionID: first.Revision.RevisionID, ExpectedEffectiveRevisionID: second.Revision.RevisionID,
		RelativePath: "restore-crash-44.txt", ObjectID: string(first.Revision.ObjectID)}
	raw, err := json.Marshal(arm)
	if err != nil {
		t.Fatal(err)
	}
	armPath := filepath.Join(controls, "file-restore-barrier.arm.json")
	if err := os.WriteFile(armPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(e2eFileRestoreBarrierEnvironment, controls)
	inject := newE2EFileRestoreBarrierFromEnvironment(dataDir, workspaceID)
	stopAfterPublication := errors.New("test stop before coordinator finish")
	coordinator.WithPersistenceFaultInjector(func(point writecoordinator.PersistenceFaultPoint) error {
		if err := inject(point); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(controls, "file-restore-barrier.claimed.json")); err == nil {
			return stopAfterPublication
		}
		return nil
	})
	// A real unrelated Save goes through the same finish hook. It cannot consume
	// the Restore lease merely because startup or a watcher commits first.
	save("55555555-5555-4555-8555-555555555555", "unrelated.txt", "watcher-like", nil)
	if _, err := os.Stat(armPath); err != nil {
		t.Fatalf("unrelated write consumed Restore arm: %v", err)
	}
	if err := inject(writecoordinator.PersistenceFaultPoint("unrelated")); err != nil {
		t.Fatal(err)
	}
	restoreCtx, err := filehistory.WithOperationReceiptBuilder(ctx, func(publication filehistory.Publication) (protocolv2.OperationReceipt, error) {
		for _, document := range publication.Documents {
			if document.DocumentID != documentID {
				continue
			}
			for _, revision := range document.Revisions {
				if revision.RevisionID != document.EffectiveRevisionID {
					continue
				}
				result, err := json.Marshal(map[string]any{"revisionId": revision.RevisionID, "revisionOrdinal": revision.RevisionOrdinal,
					"formalVersion": revision.FormalVersion, "localSequence": uint64(0)})
				return protocolv2.OperationReceipt{WorkspaceID: workspaceID, OperationID: operationID,
					Method: "fileHistory.restore", Scope: protocolv2.WorkspaceScope, RequestHash: "existing-receipt-contract", Result: result}, err
			}
		}
		return protocolv2.OperationReceipt{}, errors.New("no Restore revision")
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		_, err := service.Restore(restoreCtx, filehistory.RestoreRequest{Token: token, DocumentID: documentID,
			TargetRevisionID: first.Revision.RevisionID, ExpectedEffectiveRevision: &second.Revision.RevisionID,
			CreatedBy: "operation:" + operationID, DeviceID: claimID})
		completed <- err
	}()
	readyPath := filepath.Join(controls, "file-restore-barrier.ready.json")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		select {
		case err := <-completed:
			t.Fatalf("Restore returned before readiness: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("real Restore did not reach the persistence point")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Independently read persisted state while the mutex-held hook is blocked.
	ready, matched, err := inspectE2EFileRestorePublication(dataDir, arm)
	if err != nil || !matched || ready.MutationRevision != 4 {
		t.Fatalf("publication=%#v matched=%v err=%v", ready, matched, err)
	}
	content, err := os.ReadFile(filepath.Join(root, "files", "restore-crash-44.txt"))
	if err != nil || string(content) != "historical" {
		t.Fatalf("Restore materialization=%q err=%v", content, err)
	}
	if err := os.WriteFile(filepath.Join(controls, "file-restore-barrier.release"), []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		if !errors.Is(err, stopAfterPublication) {
			t.Fatalf("Restore fault=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Restore did not release")
	}
	if err := coordinator.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := writecoordinator.OpenPersistent(coordinatorPath, workspaceID, 3, claimID, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, err = filehistory.OpenCurrent(ctx, repository, reopened, headStore, filehistory.WithMaterializer(materializer))
	if err != nil {
		t.Fatal(err)
	}
	if reopened.RecoveryState().PendingMutationRevision != 0 {
		t.Fatal("published Restore did not resolve its prepared intent")
	}
	if _, err := os.Stat(filepath.Join(metadata, "coordination", "file-materializer", "journal.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered journal still exists: %v", err)
	}
	// This is a persistent-store/seam test, not a real-process crash acceptance.
}
