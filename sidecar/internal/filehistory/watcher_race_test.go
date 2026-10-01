package filehistory

// Watcher race regressions. Seam: scanStableFiles walks the files root
// lexically, so "a.txt" is snapshotted into the scan map strictly before
// "zz-oversize.bin" trips the synchronous file-too-large WatchEvent inside
// the walk; the seam callback runs between the snapshot and Rescan's
// List/Ingest pass.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const raceOversize = "0123456789abcdef0123456789abcdef"

type raceSeam struct {
	events []WatchEvent
	inject func()
	fired  bool
}

func (seam *raceSeam) record(event WatchEvent) {
	seam.events = append(seam.events, event)
	if event.Err != nil &&
		filepath.Base(event.Path) == "zz-oversize.bin" &&
		!seam.fired &&
		seam.inject != nil {
		seam.fired = true
		seam.inject()
	}
}

func raceSetup(t *testing.T) (historyFixture, string, string) {
	t.Helper()
	fixture := newHistoryFixture(t)
	tempRoot := t.TempDir()
	filesRoot := filepath.Join(tempRoot, "files")
	journalRoot := filepath.Join(tempRoot, "journal")
	for _, root := range []string{filesRoot, journalRoot} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(filesRoot, "zz-oversize.bin"),
		[]byte(raceOversize), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	materializer, err := OpenMaterializer(
		filesRoot, journalRoot, fixture.repository,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.materializer = materializer
	return fixture, filesRoot, journalRoot
}

func raceWatcher(
	t *testing.T,
	fixture historyFixture,
	filesRoot string,
	seam *raceSeam,
) *Watcher {
	t.Helper()
	ingestor, err := NewIngestor(fixture.service, nil)
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := NewWatcher(
		filesRoot,
		ingestor,
		func() writecoordinator.Token { return fixture.token },
		seam.record,
	)
	if err != nil {
		t.Fatal(err)
	}
	watcher.maxSize = 8
	return watcher
}

func raceWriteDisk(t *testing.T, filesRoot string, content string) {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(filesRoot, "a.txt"), []byte(content), 0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func raceReadDisk(t *testing.T, filesRoot string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(filesRoot, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func raceSaveFormal(
	t *testing.T,
	fixture historyFixture,
	documentID string,
	content string,
) Revision {
	t.Helper()
	result, err := fixture.save(context.Background(), SaveRequest{
		Token: fixture.token, DocumentID: documentID,
		Path: "a.txt", Kind: RevisionFormal, Content: []byte(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Revision
}

func raceEffective(t *testing.T, fixture historyFixture) Revision {
	t.Helper()
	for _, document := range fixture.service.List() {
		if document.RelativePath != "a.txt" {
			continue
		}
		effective := revisionByID(
			document, document.EffectiveRevisionID,
		)
		if effective == nil {
			t.Fatal("a.txt has no effective revision")
		}
		return *effective
	}
	t.Fatal("a.txt document missing")
	return Revision{}
}

// firstErrNotifyContext reports the first Err() call so the test can
// cancel only after the pre-lock check has already passed with a live
// context. Its fields are touched only by the reader goroutine.
type firstErrNotifyContext struct {
	context.Context
	firstErr chan struct{}
	notified bool
}

func (ctx *firstErrNotifyContext) Err() error {
	err := ctx.Context.Err()
	if !ctx.notified {
		ctx.notified = true
		close(ctx.firstErr)
	}
	return err
}

// A stable read on an already-cancelled context returns before any IO, and
// a cancellation landing while the read queues for the history read lock is
// honored once the lock is granted.
func TestWatcherStableReadCancellation(t *testing.T) {
	t.Run("pre-cancelled", func(t *testing.T) {
		fixture, filesRoot, _ := raceSetup(t)
		raceWriteDisk(t, filesRoot, "data")
		watcher := raceWatcher(t, fixture, filesRoot, &raceSeam{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		content, err := watcher.ReadStable(ctx, "a.txt")
		if !errors.Is(err, context.Canceled) || content != nil {
			t.Fatalf(
				"pre-cancelled read = (%#v, %v); want (nil, context.Canceled)",
				content, err,
			)
		}
	})
	t.Run("cancelled-while-waiting-for-read-lock", func(t *testing.T) {
		fixture, filesRoot, _ := raceSetup(t)
		raceWriteDisk(t, filesRoot, "data")
		watcher := raceWatcher(t, fixture, filesRoot, &raceSeam{})
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		ctx := &firstErrNotifyContext{
			Context: parent, firstErr: make(chan struct{}),
		}
		type readOutcome struct {
			content []byte
			err     error
		}
		outcome := make(chan readOutcome, 1)
		fixture.service.mu.Lock()
		go func() {
			content, err := watcher.ReadStable(ctx, "a.txt")
			outcome <- readOutcome{content: content, err: err}
		}()
		<-ctx.firstErr // pre-lock check passed with a live context
		cancel()
		fixture.service.mu.Unlock() // the reader acquires the lock only now
		result := <-outcome
		if !errors.Is(result.err, context.Canceled) ||
			result.content != nil {
			t.Fatalf(
				"cancelled lock-wait read = (%#v, %v); "+
					"want (nil, context.Canceled)",
				result.content, result.err,
			)
		}
	})
}

// A file snapshot taken before a mid-scan formal commit must never replace
// the racing revision as effective or materialize its stale bytes back over
// the newly committed content.
func TestWatcherRescanStaleSnapshotNeverBindsMidScanFormalCommit(t *testing.T) {
	for _, kind := range []string{
		"formal-save", "restore", "untracked-import",
	} {
		t.Run(kind, func(t *testing.T) {
			fixture, filesRoot, _ := raceSetup(t)
			documentID := uuid.NewString()
			var first, racing Revision
			var racingContent string
			switch kind {
			case "formal-save":
				first = raceSaveFormal(t, fixture, documentID, "orig")
				racingContent = "upg1"
			case "restore":
				first = raceSaveFormal(t, fixture, documentID, "old1")
				raceSaveFormal(t, fixture, documentID, "new1")
				racingContent = "old1"
			case "untracked-import":
				raceWriteDisk(t, filesRoot, "orig")
				racingContent = "upg1"
			}
			seam := &raceSeam{}
			seam.inject = func() {
				switch kind {
				case "formal-save":
					racing = raceSaveFormal(
						t, fixture, documentID, "upg1",
					)
				case "restore":
					result, err := fixture.restore(
						context.Background(),
						RestoreRequest{
							Token:            fixture.token,
							DocumentID:       documentID,
							TargetRevisionID: first.RevisionID,
						},
					)
					if err != nil {
						t.Fatal(err)
					}
					racing = result.Revision
				case "untracked-import":
					imported := uuid.NewString()
					raceSaveFormal(t, fixture, imported, "orig")
					racing = raceSaveFormal(t, fixture, imported, "upg1")
				}
			}
			watcher := raceWatcher(t, fixture, filesRoot, seam)
			if err := watcher.Rescan(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !seam.fired || racing.RevisionID == "" {
				t.Fatalf(
					"seam did not fire or racing commit missing: fired=%v "+
						"events=%#v", seam.fired, seam.events,
				)
			}
			effective := raceEffective(t, fixture)
			if effective.RevisionID != racing.RevisionID ||
				effective.ContentHash != racing.ContentHash {
				t.Fatalf(
					"stale snapshot beat racing commit: effective=%s/%s "+
						"want=%s/%s; events=%#v",
					effective.RevisionID, effective.ContentHash,
					racing.RevisionID, racing.ContentHash, seam.events,
				)
			}
			if disk := raceReadDisk(t, filesRoot); disk != racingContent {
				t.Fatalf(
					"materialized disk=%q want racing content %q",
					disk, racingContent,
				)
			}
		})
	}
}

// True external edits (with no racing transaction) must still be ingested
// as watcher autosaves, including an external revert to historical bytes.
func TestWatcherRescanStillAutosavesTrueExternalChanges(t *testing.T) {
	cases := []struct {
		name      string
		revisions []string
		disk      string
	}{
		{"external-change", []string{"orig"}, "ext1"},
		{"external-revert", []string{"old1", "new1"}, "old1"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture, filesRoot, _ := raceSetup(t)
			documentID := uuid.NewString()
			var parent Revision
			for _, content := range testCase.revisions {
				parent = raceSaveFormal(t, fixture, documentID, content)
			}
			raceWriteDisk(t, filesRoot, testCase.disk)
			watcher := raceWatcher(t, fixture, filesRoot, &raceSeam{})
			if err := watcher.Rescan(context.Background()); err != nil {
				t.Fatal(err)
			}
			effective := raceEffective(t, fixture)
			if effective.Kind != RevisionAutosave ||
				effective.ContentHash != contentHash([]byte(testCase.disk)) ||
				effective.CreatedBy != "workspace-file-watcher" ||
				effective.ParentRevisionID == nil ||
				*effective.ParentRevisionID != parent.RevisionID {
				t.Fatalf(
					"external change %q was not autosaved: %#v",
					testCase.disk, effective,
				)
			}
			if disk := raceReadDisk(t, filesRoot); disk != testCase.disk {
				t.Fatalf("post-autosave disk=%q want %q",
					disk, testCase.disk)
			}
		})
	}
}

// The watcher snapshot is stale relative to a further mid-scan disk write;
// the ingest of the stale bytes enters commit and its materializer apply
// has already staged a transaction when the operation-receipt builder
// (existing ctx seam) makes the publication fail. The failed ingest must
// keep the old head, the old effective revision, and materialization
// consistent with the last external bytes, and a follow-up rescan must
// autosave the real external content.
func TestWatcherIngestRollbackKeepsOldRootAndMaterializationConsistent(
	t *testing.T,
) {
	ctx := context.Background()
	fixture, filesRoot, journalRoot := raceSetup(t)
	headStore, err := OpenPersistentHeadStore(
		filepath.Join(filepath.Dir(filesRoot), "filehistory-head.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = headStore.Close() })
	fixture.service.headStore = headStore

	documentID := uuid.NewString()
	first := raceSaveFormal(t, fixture, documentID, "orig")
	head, found, err := headStore.Load(ctx, testWorkspaceID)
	if err != nil || !found {
		t.Fatalf("head load failed: found=%v err=%v", found, err)
	}
	raceWriteDisk(t, filesRoot, "ext1")
	seam := &raceSeam{}
	seam.inject = func() { raceWriteDisk(t, filesRoot, "ext2") }
	watcher := raceWatcher(t, fixture, filesRoot, seam)

	builderCalled := false
	builderContext, err := WithOperationReceiptBuilder(
		ctx,
		func(Publication) (protocolv2.OperationReceipt, error) {
			builderCalled = true
			return protocolv2.OperationReceipt{},
				errors.New("race-test receipt failure")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := watcher.Rescan(builderContext); err != nil {
		t.Fatal(err)
	}
	if !builderCalled {
		t.Fatal("receipt builder never invoked; ingest failed elsewhere")
	}

	for _, document := range fixture.service.List() {
		if document.RelativePath != "a.txt" {
			continue
		}
		if len(document.Revisions) != 1 ||
			document.EffectiveRevisionID != first.RevisionID {
			t.Fatalf("failed ingest mutated revisions: %#v", document)
		}
	}
	after, _, loadErr := headStore.Load(ctx, testWorkspaceID)
	if loadErr != nil || after != head {
		t.Fatalf(
			"failed ingest moved head: before=%#v after=%#v err=%v",
			head, after, loadErr,
		)
	}
	if disk := raceReadDisk(t, filesRoot); disk != "ext2" {
		t.Fatalf("rollback left disk=%q; want restored external bytes ext2",
			disk)
	}
	if _, err := os.Lstat(
		filepath.Join(journalRoot, "journal.json"),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal survived rollback: %v", err)
	}
	foundIngestError := false
	for _, event := range seam.events {
		if event.Path == "a.txt" && event.Err != nil {
			foundIngestError = true
		}
	}
	if !foundIngestError {
		t.Fatalf("no ingest error event after failed save: %#v",
			seam.events)
	}

	if err := watcher.Rescan(ctx); err != nil {
		t.Fatal(err)
	}
	effective := raceEffective(t, fixture)
	if effective.Kind != RevisionAutosave ||
		effective.ContentHash != contentHash([]byte("ext2")) ||
		effective.CreatedBy != "workspace-file-watcher" ||
		effective.ParentRevisionID == nil ||
		*effective.ParentRevisionID != first.RevisionID {
		t.Fatalf("follow-up rescan did not autosave external bytes: %#v",
			effective,
		)
	}
	if disk := raceReadDisk(t, filesRoot); disk != "ext2" {
		t.Fatalf("post-autosave disk=%q want ext2", disk)
	}
}
