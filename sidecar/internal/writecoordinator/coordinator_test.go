package writecoordinator

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

func newCoordinator(t *testing.T) (*WorkspaceWriteCoordinator, Token) {
	t.Helper()
	coordinator, err := New("workspace-1", 1, "claim-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := coordinator.Current()
	return coordinator, token
}

func TestWriteAdvancesOnlyCommittedMutationsAndRejectsStaleTokens(t *testing.T) {
	coordinator, token := newCoordinator(t)
	failed := errors.New("write failed")
	if _, err := coordinator.Write(
		context.Background(), token,
		func(context.Context, WriteIntent) error { return failed },
	); !errors.Is(err, failed) {
		t.Fatalf("failed write = %v", err)
	}
	_, counters := coordinator.Current()
	if counters.MutationRevision != 0 {
		t.Fatalf("failed write advanced revision: %#v", counters)
	}

	receipt, err := coordinator.Write(
		context.Background(), token,
		func(_ context.Context, intent WriteIntent) error {
			if intent.MutationRevision != 1 || intent.Token != token {
				t.Fatalf("write intent = %#v", intent)
			}
			return nil
		},
	)
	if err != nil || receipt.MutationRevision != 1 {
		t.Fatalf("write receipt = %#v, %v", receipt, err)
	}
	rotated, err := coordinator.RotateSession(context.Background(), token)
	if err != nil || rotated.SessionEpoch != 2 {
		t.Fatalf("rotated token = %#v, %v", rotated, err)
	}
	if _, err := coordinator.Write(
		context.Background(), token,
		func(context.Context, WriteIntent) error { return nil },
	); !errors.Is(err, ErrStaleToken) {
		t.Fatalf("stale session write error = %v", err)
	}
}

func TestCaptureFreezesUnderWriteGateAndFailedIntentConsumesSequence(t *testing.T) {
	coordinator, token := newCoordinator(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	captureDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Capture(
			context.Background(), token,
			func(_ context.Context, intent CaptureIntent) (FrozenRoots, error) {
				if intent.SnapshotSequence != 1 || intent.MutationRevision != 0 {
					t.Errorf("capture intent = %#v", intent)
				}
				close(entered)
				<-release
				return FrozenRoots{DatabaseView: "view-1"}, nil
			},
		)
		captureDone <- err
	}()
	<-entered

	writeEntered := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		_, _ = coordinator.Write(
			context.Background(), token,
			func(context.Context, WriteIntent) error {
				close(writeEntered)
				return nil
			},
		)
	}()
	select {
	case <-writeEntered:
		t.Fatal("write entered while capture barrier held the gate")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-captureDone; err != nil {
		t.Fatal(err)
	}
	wait.Wait()

	failed := errors.New("freeze failed")
	_, err := coordinator.Capture(
		context.Background(), token,
		func(context.Context, CaptureIntent) (FrozenRoots, error) {
			return FrozenRoots{}, failed
		},
	)
	if !errors.Is(err, failed) {
		t.Fatalf("failed capture = %v", err)
	}
	_, counters := coordinator.Current()
	if counters.MutationRevision != 1 || counters.SnapshotSequence != 2 {
		t.Fatalf("counters = %#v", counters)
	}
}

func TestFenceTransferInvalidatesOldAuthorityWithoutChangingOtherCounters(t *testing.T) {
	coordinator, token := newCoordinator(t)
	next, err := coordinator.TransferFence(context.Background(), token, "claim-b")
	if err != nil {
		t.Fatal(err)
	}
	if next.FenceEpoch != 2 || next.ClaimID != "claim-b" ||
		next.SessionEpoch != token.SessionEpoch {
		t.Fatalf("next token = %#v", next)
	}
	if _, err := coordinator.TransferFence(
		context.Background(), token, "claim-c",
	); !errors.Is(err, ErrStaleToken) {
		t.Fatalf("stale fence transfer error = %v", err)
	}
	_, counters := coordinator.Current()
	if counters.FenceEpoch != 2 ||
		counters.MutationRevision != 0 ||
		counters.SnapshotSequence != 0 {
		t.Fatalf("counters = %#v", counters)
	}
}

func TestPersistentCoordinatorReopensCountersDrainAndCaptureIntent(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "coordination", "coordination.db")
	coordinator, err := OpenPersistent(
		databasePath, "workspace-1", 1, "claim-a", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := coordinator.Current()
	if _, err := coordinator.Write(
		context.Background(),
		token,
		func(context.Context, WriteIntent) error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	highWatermark, err := coordinator.Drain(
		context.Background(),
		token,
		time.Now().Add(time.Second),
		func(context.Context) (HighWatermark, error) {
			return HighWatermark{
				SourceEpoch: "audit-1", SourceSequence: 4, ChainHash: "sha256:audit",
			}, nil
		},
	)
	if err != nil || highWatermark.SourceSequence != 4 {
		t.Fatalf("drain = %#v, %v", highWatermark, err)
	}
	handle, err := coordinator.Capture(
		context.Background(),
		token,
		func(context.Context, CaptureIntent) (FrozenRoots, error) {
			return FrozenRoots{
				DatabaseView: "sqlite-view",
				TopologyRoot: "manifest_topology",
				FileRoot:     "manifest_files",
				AuditAnchor:  "sha256:audit",
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := coordinator.CaptureIntent(
		context.Background(), handle.SnapshotSequence,
	)
	if err != nil ||
		intent.State != "captured" ||
		intent.MutationRevision != 1 ||
		intent.TopologyRoot != "manifest_topology" {
		t.Fatalf("capture intent = %#v, %v", intent, err)
	}
	if err := coordinator.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPersistent(
		databasePath, "workspace-1", 1, "claim-a", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, counters := reopened.Current()
	if counters.MutationRevision != 1 ||
		counters.SnapshotSequence != 1 ||
		reopened.HighWatermark() != highWatermark {
		t.Fatalf("reopened state = %#v %#v", counters, reopened.HighWatermark())
	}
}

func TestPersistentCoordinatorFailsClosedUntilPreparedMutationResolved(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "coordination.db")
	coordinator, err := OpenPersistent(
		databasePath, "workspace-1", 1, "claim-a", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := coordinator.Current()
	if err := coordinator.store.prepareMutation(
		context.Background(), token, 1, time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPersistent(
		databasePath, "workspace-1", 1, "claim-a", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Write(
		context.Background(),
		token,
		func(context.Context, WriteIntent) error { return nil },
	); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("prepared mutation did not fail closed: %v", err)
	}
	if err := reopened.ResolvePreparedMutation(
		context.Background(), token, 1, false,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Write(
		context.Background(),
		token,
		func(context.Context, WriteIntent) error { return nil },
	); err != nil {
		t.Fatalf("resolved coordinator remained blocked: %v", err)
	}
}

func TestReadPersistentMutationRevisionCoversCommittedApplyBeforeFinish(
	t *testing.T,
) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "coordination.db")
	coordinator, err := OpenPersistent(
		databasePath, "workspace-1", 1, "claim-a", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	crash := errors.New("simulated process crash before finish")
	coordinator.WithPersistenceFaultInjector(
		func(point PersistenceFaultPoint) error {
			if point == FaultBeforeFinishCommittedMutation {
				return crash
			}
			return nil
		},
	)
	token, _ := coordinator.Current()
	authoritativeCommitted := false
	if _, err := coordinator.Write(
		context.Background(),
		token,
		func(context.Context, WriteIntent) error {
			authoritativeCommitted = true
			return nil
		},
	); !errors.Is(err, crash) {
		t.Fatalf("write error=%v", err)
	}
	if !authoritativeCommitted {
		t.Fatal("authoritative apply did not commit")
	}
	defer coordinator.Close()

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
	if !slices.Contains(before, "coordination.db-wal") {
		t.Fatalf("crash fixture did not retain WAL: %v", before)
	}
	required, err := ReadPersistentMutationRevision(
		context.Background(),
		databasePath,
		"workspace-1",
	)
	if err != nil || required != 1 {
		t.Fatalf("required=%d err=%v", required, err)
	}
	after := entryNames()
	if !slices.Equal(before, after) {
		t.Fatalf("read-only reader changed files: before=%v after=%v", before, after)
	}
	if _, err := ReadPersistentMutationRevision(
		context.Background(),
		databasePath,
		"workspace-other",
	); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("identity mismatch error=%v", err)
	}

	missing := filepath.Join(directory, "missing.db")
	if _, err := ReadPersistentMutationRevision(
		context.Background(),
		missing,
		"workspace-1",
	); err == nil {
		t.Fatal("missing coordination database accepted")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only reader created missing database: %v", err)
	}
}

func TestResumePreparedMutationReusesRevisionAndKeepsFailedReplayPending(
	t *testing.T,
) {
	path := filepath.Join(t.TempDir(), "coordination.db")
	coordinator, err := OpenPersistent(
		path, "workspace-1", 1, "claim-a", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	crash := errors.New("crash after authoritative commit")
	coordinator.WithPersistenceFaultInjector(
		func(point PersistenceFaultPoint) error {
			if point == FaultBeforeFinishCommittedMutation {
				return crash
			}
			return nil
		},
	)
	token, _ := coordinator.Current()
	if _, err := coordinator.Write(
		context.Background(),
		token,
		func(context.Context, WriteIntent) error { return nil },
	); !errors.Is(err, crash) {
		t.Fatalf("prepared write error = %v", err)
	}
	coordinator.WithPersistenceFaultInjector(nil)
	replayFailure := errors.New("replay incomplete")
	if _, err := coordinator.ResumePreparedMutation(
		context.Background(),
		token,
		1,
		func(_ context.Context, intent WriteIntent) error {
			if intent.MutationRevision != 1 {
				t.Fatalf("replay revision = %d", intent.MutationRevision)
			}
			return replayFailure
		},
	); !errors.Is(err, replayFailure) {
		t.Fatalf("failed replay error = %v", err)
	}
	if pending := coordinator.RecoveryState().PendingMutationRevision; pending != 1 {
		t.Fatalf("failed replay pending revision = %d", pending)
	}
	receipt, err := coordinator.ResumePreparedMutation(
		context.Background(),
		token,
		1,
		func(_ context.Context, intent WriteIntent) error {
			if intent.MutationRevision != 1 {
				t.Fatalf("successful replay revision = %d",
					intent.MutationRevision,
				)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.MutationRevision != 1 {
		t.Fatalf("receipt revision = %d", receipt.MutationRevision)
	}
	state := coordinator.RecoveryState()
	if state.PendingMutationRevision != 0 ||
		state.Counters.MutationRevision != 1 {
		t.Fatalf("recovered state = %#v", state)
	}
	if err := coordinator.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriteKeepsPreparedRevisionWhenExternalDomainCommitted(t *testing.T) {
	coordinator, err := OpenPersistent(
		filepath.Join(t.TempDir(), "coordination.db"),
		"workspace-1", 1, "claim-a", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	token, _ := coordinator.Current()
	externalFailure := errors.Join(
		ErrExternalCommitted,
		errors.New("filehistory publication failed"),
	)
	if _, err := coordinator.Write(
		context.Background(),
		token,
		func(context.Context, WriteIntent) error {
			return externalFailure
		},
	); !errors.Is(err, ErrExternalCommitted) {
		t.Fatalf("write error = %v", err)
	}
	state := coordinator.RecoveryState()
	if state.PendingMutationRevision != 1 ||
		state.Counters.MutationRevision != 0 {
		t.Fatalf("external-committed state = %#v", state)
	}
	if _, err := coordinator.Write(
		context.Background(),
		token,
		func(context.Context, WriteIntent) error { return nil },
	); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("new revision bypassed prepared recovery: %v", err)
	}
}
