package restore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeRuntime struct {
	failHealthGeneration int
	generation           int
	healthGenerations    []int
	running              bool
	stops                int
	starts               int
	events               *[]string
}

func (*fakeRuntime) Protect(context.Context) error { return nil }
func (runtime *fakeRuntime) Stop(context.Context) error {
	runtime.stops++
	runtime.running = false
	if runtime.events != nil {
		*runtime.events = append(*runtime.events, "stop")
	}
	return nil
}
func (runtime *fakeRuntime) Start(context.Context) error {
	runtime.starts++
	runtime.generation++
	runtime.running = true
	if runtime.events != nil {
		*runtime.events = append(*runtime.events, "start")
	}
	return nil
}
func (runtime *fakeRuntime) Health(context.Context) error {
	runtime.healthGenerations = append(runtime.healthGenerations, runtime.generation)
	if runtime.generation == runtime.failHealthGeneration {
		return errors.New("unhealthy")
	}
	return nil
}

type fakeInstaller struct {
	rollbacks int
	leaves    int
	audit     int
	events    *[]string
	runtime   *fakeRuntime
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
	if installer.runtime != nil && installer.runtime.running {
		return errors.New("rollback while runtime is running")
	}
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
	writes := 0
	coordinator := newWithPersistenceGate(
		filepath.Join(t.TempDir(), "journal.json"),
		runtime,
		installer,
		&fakeAfter{},
		func(journal Journal, _ func(Journal) error) error {
			writes++
			if journal.Stage == StageDataInstalled {
				return errors.New("disk full")
			}
			return nil
		},
	)
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
	runtime := &fakeRuntime{failHealthGeneration: 1, events: &events}
	installer := &fakeInstaller{events: &events, runtime: runtime}
	coordinator := New(filepath.Join(t.TempDir(), "journal.json"), runtime, installer, &fakeAfter{})
	if err := coordinator.Restore(context.Background(), "w", "s", "root"); err == nil {
		t.Fatal("expected health failure")
	}
	if installer.rollbacks != 1 || runtime.starts != 2 || runtime.stops != 2 {
		t.Fatalf(
			"rollback did not restore runtime: events=%#v runtime=%#v installer=%#v",
			events, runtime, installer,
		)
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
	if !runtime.running || runtime.generation != 2 ||
		len(runtime.healthGenerations) != 2 ||
		runtime.healthGenerations[0] != 1 || runtime.healthGenerations[1] != 2 {
		t.Fatalf("rollback runtime health generations = %#v", runtime)
	}
	if _, err := coordinator.readJournal(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("healthy rollback left a journal behind: %v", err)
	}
}

func TestStageVerifiedPersistenceFailureStopsBeforeRollback(t *testing.T) {
	events := []string{}
	runtime := &fakeRuntime{events: &events}
	installer := &fakeInstaller{events: &events, runtime: runtime}
	coordinator := newWithPersistenceGate(
		filepath.Join(t.TempDir(), "journal.json"),
		runtime,
		installer,
		&fakeAfter{},
		func(journal Journal, _ func(Journal) error) error {
			if journal.Stage == StageVerified {
				return errors.New("kill before verified journal sync")
			}
			return nil
		},
	)
	err := coordinator.Restore(context.Background(), "w", "s", "root")
	if err == nil || err.Error() != "kill before verified journal sync" {
		t.Fatalf(
			"verified persistence error = %v, events=%#v runtime=%#v installer=%#v",
			err, events, runtime, installer,
		)
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
	if !runtime.running || installer.rollbacks != 1 {
		t.Fatalf("rollback did not restore the previous runtime: %#v %#v", runtime, installer)
	}
	if _, err := coordinator.readJournal(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed rollback left a journal behind: %v", err)
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
