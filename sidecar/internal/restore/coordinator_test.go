package restore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeRuntime struct {
	failHealth bool
	stops      int
	starts     int
	events     *[]string
}

func (*fakeRuntime) Protect(context.Context) error { return nil }
func (runtime *fakeRuntime) Stop(context.Context) error {
	runtime.stops++
	if runtime.events != nil {
		*runtime.events = append(*runtime.events, "stop")
	}
	return nil
}
func (runtime *fakeRuntime) Start(context.Context) error {
	runtime.starts++
	if runtime.events != nil {
		*runtime.events = append(*runtime.events, "start")
	}
	return nil
}
func (runtime *fakeRuntime) Health(context.Context) error {
	if runtime.failHealth {
		return errors.New("unhealthy")
	}
	return nil
}

type fakeInstaller struct {
	rollbacks int
	leaves    int
	audit     int
	events    *[]string
}

func (*fakeInstaller) Stage(context.Context, string, string) (string, error) { return "staging", nil }
func (*fakeInstaller) InstallData(context.Context, string, string) error     { return nil }
func (installer *fakeInstaller) InstallFilesAsRestoreLeaves(context.Context, string, string) error {
	installer.leaves++
	return nil
}
func (installer *fakeInstaller) CommitAuditEpoch(context.Context, string, string) error {
	installer.audit++
	return nil
}
func (installer *fakeInstaller) Rollback(context.Context, string, string) error {
	installer.rollbacks++
	if installer.events != nil {
		*installer.events = append(*installer.events, "rollback")
	}
	return nil
}

type fakeAfter struct{ captures int }

func (after *fakeAfter) CaptureRecovery(context.Context, string) error { after.captures++; return nil }

func TestRestoreUsesJournalCreatesLeavesAndCapturesRecoverySnapshot(t *testing.T) {
	runtime, installer, after := &fakeRuntime{}, &fakeInstaller{}, &fakeAfter{}
	journal := filepath.Join(t.TempDir(), "restore", "journal.json")
	coordinator := New(journal, runtime, installer, after)
	if err := coordinator.Restore(context.Background(), "w", "s", "root"); err != nil {
		t.Fatal(err)
	}
	if runtime.stops != 1 || runtime.starts != 1 || installer.leaves != 1 ||
		installer.audit != 1 || after.captures != 1 {
		t.Fatalf("incomplete restore: %#v %#v %#v", runtime, installer, after)
	}
	if err := coordinator.Recover(context.Background(), "w"); err != nil {
		t.Fatalf("completed journal should be gone: %v", err)
	}
}

func TestJournalPersistenceFailureAfterMutationRollsBack(t *testing.T) {
	runtime, installer := &fakeRuntime{}, &fakeInstaller{}
	coordinator := New(filepath.Join(t.TempDir(), "journal.json"), runtime, installer, &fakeAfter{})
	writes := 0
	realPersist := coordinator.persist
	coordinator.persist = func(journal Journal) error {
		writes++
		if journal.Stage == StageDataInstalled {
			return errors.New("disk full")
		}
		return realPersist(journal)
	}
	if err := coordinator.Restore(context.Background(), "w", "s", "root"); err == nil {
		t.Fatal("expected journal persistence failure")
	}
	if writes < 3 || installer.rollbacks != 1 || runtime.starts != 1 ||
		runtime.stops != 2 {
		t.Fatalf("persistence failure left mixed state: %#v %#v", runtime, installer)
	}
	if _, err := coordinator.readJournal(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback left a journal behind: %v", err)
	}
}

func TestRecoverRetriesPendingRecoverySnapshotBeforeCommitting(t *testing.T) {
	runtime, installer, after := &fakeRuntime{}, &fakeInstaller{}, &fakeAfter{}
	coordinator := New(filepath.Join(t.TempDir(), "journal.json"), runtime, installer, after)
	journal := Journal{
		FormatVersion: 2, WorkspaceID: "w", SnapshotID: "s",
		Stage: StageRecoverySnapshotPending, PreviousRoot: "root", StagingRoot: "stage",
	}
	if err := coordinator.writeJournal(journal); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background(), "w"); err != nil {
		t.Fatal(err)
	}
	if after.captures != 1 || installer.rollbacks != 0 {
		t.Fatalf("pending recovery did not resume: %#v %#v", after, installer)
	}
	if _, err := coordinator.readJournal(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed recovery left a journal behind: %v", err)
	}
}

func TestFailedHealthRollsBackWithoutMixedState(t *testing.T) {
	events := []string{}
	runtime := &fakeRuntime{failHealth: true, events: &events}
	installer := &fakeInstaller{events: &events}
	coordinator := New(filepath.Join(t.TempDir(), "journal.json"), runtime, installer, &fakeAfter{})
	if err := coordinator.Restore(context.Background(), "w", "s", "root"); err == nil {
		t.Fatal("expected health failure")
	}
	if installer.rollbacks != 1 || runtime.starts != 2 || runtime.stops != 2 {
		t.Fatalf("rollback did not restore runtime: %#v %#v", runtime, installer)
	}
	want := []string{"stop", "start", "stop", "rollback", "start"}
	if len(events) != len(want) {
		t.Fatalf("unexpected rollback ordering: %#v", events)
	}
	for index := range want {
		if events[index] != want[index] {
			t.Fatalf("rollback touched live state: got %#v want %#v", events, want)
		}
	}
}

func TestStageVerifiedPersistenceFailureStopsBeforeRollback(t *testing.T) {
	events := []string{}
	runtime := &fakeRuntime{events: &events}
	installer := &fakeInstaller{events: &events}
	coordinator := New(filepath.Join(t.TempDir(), "journal.json"), runtime, installer, &fakeAfter{})
	realPersist := coordinator.persist
	coordinator.persist = func(journal Journal) error {
		if journal.Stage == StageVerified {
			return errors.New("kill before verified journal sync")
		}
		return realPersist(journal)
	}
	if err := coordinator.Restore(context.Background(), "w", "s", "root"); err == nil {
		t.Fatal("expected verified journal persistence failure")
	}
	want := []string{"stop", "start", "stop", "rollback", "start"}
	if len(events) != len(want) {
		t.Fatalf("unexpected rollback ordering: %#v", events)
	}
	for index := range want {
		if events[index] != want[index] {
			t.Fatalf("rollback touched live state: got %#v want %#v", events, want)
		}
	}
}

func TestRecoverRejectsUnknownJournalStageWithoutMutatingWorkspace(t *testing.T) {
	runtime, installer := &fakeRuntime{}, &fakeInstaller{}
	coordinator := New(filepath.Join(t.TempDir(), "journal.json"), runtime, installer, &fakeAfter{})
	journal := Journal{
		FormatVersion: 2, WorkspaceID: "w", SnapshotID: "s",
		Stage: "future-or-corrupt", PreviousRoot: "root", StagingRoot: "stage",
	}
	if err := coordinator.writeJournal(journal); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background(), "w"); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("unknown stage must fail closed: %v", err)
	}
	if runtime.starts != 0 || runtime.stops != 0 || installer.rollbacks != 0 {
		t.Fatalf("corrupt journal mutated state: %#v %#v", runtime, installer)
	}
}
