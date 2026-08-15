package filehistory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func TestMaterializerTracksEffectiveLeafRenameRestoreAndDelete(t *testing.T) {
	ctx := context.Background()
	coordinator, err := writecoordinator.New(
		testWorkspaceID, 1, "claim-a", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := coordinator.Current()
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(ctx, nil, token.Authority()); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	filesRoot := filepath.Join(root, "files")
	materializer, err := OpenMaterializer(
		filesRoot,
		filepath.Join(root, ".vibetable", "coordination", "file-materializer"),
		repository,
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(
		repository, coordinator, WithMaterializer(materializer),
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Save(ctx, SaveRequest{
		Token: token, DocumentID: testDocumentOne, Path: "reports/q3.txt",
		Kind: RevisionFormal, Content: []byte("one"), MimeType: "text/plain",
		CreatedBy: "test-user", DeviceID: testDeviceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMaterializedContent(t, filesRoot, "reports/q3.txt", "one")
	second, err := service.Save(ctx, SaveRequest{
		Token: token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(first.Revision.RevisionID),
		Kind:                      RevisionFormal, Content: []byte("two"), MimeType: "text/plain",
		CreatedBy: "test-user", DeviceID: testDeviceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMaterializedContent(t, filesRoot, "reports/q3.txt", "two")
	restored, err := service.Restore(ctx, RestoreRequest{
		Token: token, DocumentID: testDocumentOne,
		TargetRevisionID:          first.Revision.RevisionID,
		ExpectedEffectiveRevision: stringRef(second.Revision.RevisionID),
		CreatedBy:                 "test-user",
		DeviceID:                  testDeviceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMaterializedContent(t, filesRoot, "reports/q3.txt", "one")
	renamed, err := service.Rename(
		ctx,
		token,
		testDocumentOne,
		"archive/q3.txt",
		stringRef(restored.Revision.RevisionID),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(filesRoot, "reports", "q3.txt")); !errors.Is(
		err, os.ErrNotExist,
	) {
		t.Fatalf("old materialized path still exists: %v", err)
	}
	assertMaterializedContent(t, filesRoot, "archive/q3.txt", "one")
	if _, err := service.Delete(
		ctx,
		token,
		testDocumentOne,
		stringRef(renamed.Document.EffectiveRevisionID),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(filesRoot, "archive", "q3.txt")); !errors.Is(
		err, os.ErrNotExist,
	) {
		t.Fatalf("deleted document remains materialized: %v", err)
	}
	if _, err := os.Lstat(materializer.journalPath); !errors.Is(
		err, os.ErrNotExist,
	) {
		t.Fatalf("successful mutation left journal: %v", err)
	}
}

func TestMaterializerJournalWritesUseDistinctTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	materializer := &Materializer{
		journalRoot: root,
		journalPath: filepath.Join(root, "journal.json"),
	}
	var sources []string
	replaceFile := func(source string, destination string) error {
		sources = append(sources, source)
		raw, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, raw, 0o600)
	}
	for _, state := range []string{"prepared", "applied"} {
		if err := materializer.writeJournalWithReplace(
			materializerJournal{State: state}, replaceFile,
		); err != nil {
			t.Fatal(err)
		}
	}
	if len(sources) != 2 || sources[0] == sources[1] {
		t.Fatalf("journal temp sources = %#v", sources)
	}
}

func TestFormalRelinkRematerializesMissingSameContentLeaf(t *testing.T) {
	ctx := context.Background()
	coordinator, err := writecoordinator.New(
		testWorkspaceID,
		1,
		"claim-a",
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := coordinator.Current()
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(
		ctx,
		nil,
		token.Authority(),
	); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	filesRoot := filepath.Join(root, "files")
	materializer, err := OpenMaterializer(
		filesRoot,
		filepath.Join(root, "materializer"),
		repository,
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(
		repository,
		coordinator,
		WithMaterializer(materializer),
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Save(ctx, SaveRequest{
		Token: token, DocumentID: testDocumentOne, Path: "relink.txt",
		Kind: RevisionFormal, Content: []byte("same"),
		MimeType: "text/plain", CreatedBy: "test-user",
		DeviceID: testDeviceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filesRoot, "relink.txt")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	relinked, err := service.Save(ctx, SaveRequest{
		Token: token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(first.Revision.RevisionID),
		Kind:                      RevisionFormal,
		Content:                   []byte("same"),
		MimeType:                  "text/plain",
		CreatedBy:                 "test-user",
		DeviceID:                  testDeviceID,
		Comment:                   "relinked",
	})
	if err != nil {
		t.Fatal(err)
	}
	if relinked.Revision.FormalVersion == nil ||
		*relinked.Revision.FormalVersion != 2 {
		t.Fatalf("relink did not create next formal revision: %#v", relinked)
	}
	assertMaterializedContent(t, filesRoot, "relink.txt", "same")
}

func TestPersistentHeadPublishesAndDrainsAuditOutboxAtomically(t *testing.T) {
	ctx := context.Background()
	coordinator, err := writecoordinator.New(
		testWorkspaceID, 1, testDeviceID, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := coordinator.Current()
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(ctx, nil, token.Authority()); err != nil {
		t.Fatal(err)
	}
	headStore, err := OpenPersistentHeadStore(
		filepath.Join(t.TempDir(), "head.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer headStore.Close()
	service, err := New(
		repository,
		coordinator,
		WithHeadStore(headStore),
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Save(ctx, SaveRequest{
		Token: token, DocumentID: testDocumentOne, Path: "audit.txt",
		Kind: RevisionFormal, Content: []byte("one"), MimeType: "text/plain",
		CreatedBy: "test-user", DeviceID: testDeviceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Save(ctx, SaveRequest{
		Token: token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(first.Revision.RevisionID),
		Kind:                      RevisionFormal, Content: []byte("two"), MimeType: "text/plain",
		CreatedBy: "test-user", DeviceID: testDeviceID,
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := headStore.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 ||
		pending[0].SourceSequence != 1 ||
		pending[1].SourceSequence != 2 ||
		pending[0].PayloadHash == "" {
		t.Fatalf("pending audit = %#v", pending)
	}
	ledger, err := auditledger.Open(filepath.Join(t.TempDir(), "audit"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	drainer, err := auditledger.NewDrainer(ledger, 10)
	if err != nil {
		t.Fatal(err)
	}
	report, err := drainer.Drain(ctx, headStore)
	if err != nil {
		t.Fatal(err)
	}
	if report.Appended != 2 || report.Acknowledged != 2 {
		t.Fatalf("drain report = %#v", report)
	}
	pending, err = headStore.Pending(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after drain = %#v, %v", pending, err)
	}
}

func TestMaterializerRecoversCrashAfterHeadBeforeCoordinatorFinish(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	coordinatorPath := filepath.Join(root, "coordinator.db")
	headPath := filepath.Join(root, "head.db")
	filesRoot := filepath.Join(root, "files")
	journalRoot := filepath.Join(root, "materializer")
	coordinator, err := writecoordinator.OpenPersistent(
		coordinatorPath, testWorkspaceID, 1, "claim-a", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := coordinator.Current()
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(ctx, nil, token.Authority()); err != nil {
		t.Fatal(err)
	}
	headStore, err := OpenPersistentHeadStore(headPath)
	if err != nil {
		t.Fatal(err)
	}
	materializer, err := OpenMaterializer(filesRoot, journalRoot, repository)
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenCurrent(
		ctx,
		repository,
		coordinator,
		headStore,
		WithMaterializer(materializer),
	)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.WithPersistenceFaultInjector(
		func(point writecoordinator.PersistenceFaultPoint) error {
			if point == writecoordinator.FaultBeforeFinishCommittedMutation {
				return errInjectedFinishMutation
			}
			return nil
		},
	)
	_, err = service.Save(ctx, SaveRequest{
		Token: token, DocumentID: testDocumentOne, Path: "crash.txt",
		Kind: RevisionFormal, Content: []byte("committed"), MimeType: "text/plain",
		CreatedBy: "test-user", DeviceID: testDeviceID,
	})
	if !errors.Is(err, errInjectedFinishMutation) {
		t.Fatalf("save error = %v", err)
	}
	assertMaterializedContent(t, filesRoot, "crash.txt", "committed")
	if _, err := os.Stat(materializer.journalPath); err != nil {
		t.Fatalf("pending committed mutation lost journal: %v", err)
	}
	if err := headStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Close(); err != nil {
		t.Fatal(err)
	}

	coordinator, err = writecoordinator.OpenPersistent(
		coordinatorPath, testWorkspaceID, 1, "claim-a", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	headStore, err = OpenPersistentHeadStore(headPath)
	if err != nil {
		t.Fatal(err)
	}
	defer headStore.Close()
	materializer, err = OpenMaterializer(filesRoot, journalRoot, repository)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenCurrent(
		ctx,
		repository,
		coordinator,
		headStore,
		WithMaterializer(materializer),
	)
	if err != nil {
		t.Fatal(err)
	}
	document, err := reopened.Inspect(testDocumentOne)
	if err != nil || document.RelativePath != "crash.txt" {
		t.Fatalf("reopened document = %#v, %v", document, err)
	}
	assertMaterializedContent(t, filesRoot, "crash.txt", "committed")
	if _, err := os.Lstat(materializer.journalPath); !errors.Is(
		err, os.ErrNotExist,
	) {
		t.Fatalf("recovery did not finalize journal: %v", err)
	}
	recovery := coordinator.RecoveryState()
	if recovery.PendingMutationRevision != 0 ||
		recovery.Counters.MutationRevision != 1 {
		t.Fatalf("coordinator recovery = %#v", recovery)
	}
}

func assertMaterializedContent(
	t *testing.T,
	root string,
	relative string,
	expected string,
) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != expected {
		t.Fatalf("materialized %s = %q, want %q", relative, raw, expected)
	}
}
