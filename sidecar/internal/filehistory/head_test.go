package filehistory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func historySQLitePath(t *testing.T, name string) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "vibetable-filehistory-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var removeErr error
		for attempt := 0; attempt < 20; attempt++ {
			removeErr = os.RemoveAll(directory)
			if removeErr == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Errorf("remove sqlite test directory: %v", removeErr)
	})
	return filepath.Join(directory, name)
}

func TestPersistentHeadReopensCurrentRoot(t *testing.T) {
	fixture := newHistoryFixture(t)
	path := historySQLitePath(t, "head.db")
	store, err := OpenPersistentHeadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var (
		journalMode string
		synchronous int
	)
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" || synchronous != 2 {
		t.Fatalf("head durability = %s/%d", journalMode, synchronous)
	}
	service, err := OpenCurrent(
		context.Background(),
		fixture.repository,
		fixture.coordinator,
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := service.Save(context.Background(), SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "current.txt", Kind: RevisionFormal,
		Content: []byte("durable"), MimeType: "text/plain",
		CreatedBy: "test-user", DeviceID: testDeviceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedStore, err := OpenPersistentHeadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedStore.Close()
	reopened, err := OpenCurrent(
		context.Background(),
		fixture.repository,
		fixture.coordinator,
		reopenedStore,
	)
	if err != nil {
		t.Fatal(err)
	}
	document, err := reopened.Inspect(testDocumentOne)
	if err != nil ||
		reopened.Root() != saved.Root ||
		document.EffectiveRevisionID != saved.Revision.RevisionID {
		t.Fatalf("reopened = %#v root=%s err=%v", document, reopened.Root(), err)
	}
}

func TestReadPersistentMutationRevisionIsStrictlyReadOnly(t *testing.T) {
	fixture := newHistoryFixture(t)
	path := historySQLitePath(t, "head.db")
	store, err := OpenPersistentHeadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenCurrent(
		context.Background(),
		fixture.repository,
		fixture.coordinator,
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Save(context.Background(), SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "read-only.txt", Kind: RevisionFormal,
		Content: []byte("durable"), MimeType: "text/plain",
		CreatedBy: "test-user", DeviceID: testDeviceID,
	}); err != nil {
		t.Fatal(err)
	}
	head, found, err := store.Load(
		context.Background(),
		fixture.token.WorkspaceID,
	)
	if err != nil || !found {
		t.Fatalf("head=%#v found=%v err=%v", head, found, err)
	}
	defer store.Close()

	directory := filepath.Dir(path)
	entryNames := func() []string {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return names
	}
	before := entryNames()
	if !slices.Contains(before, "head.db-wal") {
		t.Fatalf("file-history fixture did not retain WAL: %v", before)
	}
	revision, err := ReadPersistentMutationRevision(
		context.Background(),
		path,
		fixture.token.WorkspaceID,
	)
	if err != nil || revision != head.MutationRevision {
		t.Fatalf("revision=%d head=%#v err=%v", revision, head, err)
	}
	after := entryNames()
	if !slices.Equal(before, after) {
		t.Fatalf("read-only reader changed files: before=%v after=%v", before, after)
	}

	missing := filepath.Join(directory, "missing.db")
	revision, err = ReadPersistentMutationRevision(
		context.Background(),
		missing,
		fixture.token.WorkspaceID,
	)
	if err != nil || revision != 0 {
		t.Fatalf("missing head revision=%d error=%v", revision, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only reader created missing database: %v", err)
	}
}

func TestConcurrentServicesCASHeadAndLeaveLoserObjectsUnreachable(t *testing.T) {
	fixture := newHistoryFixture(t)
	store, err := OpenPersistentHeadStore(
		historySQLitePath(t, "head.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	left, err := OpenCurrent(
		context.Background(),
		fixture.repository,
		fixture.coordinator,
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	right, err := OpenCurrent(
		context.Background(),
		fixture.repository,
		fixture.coordinator,
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		documentID string
		content    string
		result     SaveResult
		err        error
		localRoot  string
		localCount int
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var wait sync.WaitGroup
	for _, candidate := range []struct {
		service    *Service
		documentID string
		path       string
		content    string
	}{
		{left, testDocumentOne, "left.txt", "left"},
		{right, testDocumentTwo, "right.txt", "right"},
	} {
		wait.Add(1)
		go func(candidate struct {
			service    *Service
			documentID string
			path       string
			content    string
		}) {
			defer wait.Done()
			<-start
			result, err := candidate.service.Save(
				context.Background(),
				SaveRequest{
					Token:      fixture.token,
					DocumentID: candidate.documentID,
					Path:       candidate.path,
					Kind:       RevisionFormal,
					Content:    []byte(candidate.content),
					MimeType:   "text/plain",
					CreatedBy:  "test-user",
					DeviceID:   testDeviceID,
				},
			)
			outcomes <- outcome{
				documentID: candidate.documentID,
				content:    candidate.content,
				result:     result,
				err:        err,
				localRoot:  string(candidate.service.Root()),
				localCount: len(candidate.service.List()),
			}
		}(candidate)
	}
	close(start)
	wait.Wait()
	close(outcomes)
	var winner, loser outcome
	for outcome := range outcomes {
		if outcome.err == nil {
			winner = outcome
		} else if errors.Is(outcome.err, ErrHeadConflict) {
			loser = outcome
		} else {
			t.Fatalf("unexpected save error: %v", outcome.err)
		}
	}
	if winner.documentID == "" || loser.documentID == "" {
		t.Fatalf("winner=%#v loser=%#v", winner, loser)
	}
	head, found, err := store.Load(
		context.Background(), fixture.token.WorkspaceID,
	)
	if err != nil || !found || head.Root != winner.result.Root {
		t.Fatalf("head=%#v found=%v err=%v", head, found, err)
	}
	if loser.result.Root != "" {
		t.Fatalf("loser exposed unpublished root: %#v", loser.result)
	}
	if loser.localRoot != "" || loser.localCount != 0 {
		t.Fatalf("loser installed stale memory state: %#v", loser)
	}
	reader, err := fixture.repository.Open(
		context.Background(), contentObjectID([]byte(loser.content)),
	)
	if err != nil {
		t.Fatalf("loser object was not durably committed: %v", err)
	}
	_ = reader.Close()
	current, err := OpenCurrent(
		context.Background(),
		fixture.repository,
		fixture.coordinator,
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.Inspect(winner.documentID); err != nil {
		t.Fatalf("winner missing from current head: %v", err)
	}
	if _, err := current.Inspect(loser.documentID); !errors.Is(
		err, ErrDocumentNotFound,
	) {
		t.Fatalf("loser entered current head: %v", err)
	}
}
