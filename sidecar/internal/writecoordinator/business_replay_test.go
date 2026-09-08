package writecoordinator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestBusinessReplaySignalRequiresExactIntent(t *testing.T) {
	_, token := newCoordinator(t)
	intent := WriteIntent{Token: token, MutationRevision: 1, AuditSourceEpoch: "business-v2"}
	bound, err := WithBusinessIntent(context.Background(), intent, "mutation.apply", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		name           string
		ctx            context.Context
		kind, identity string
		want           error
	}{
		{"exact", bound, "mutation.apply", "request-1", ErrBusinessReplay},
		{"no intent", context.Background(), "mutation.apply", "request-1", nil},
		{"different operation", bound, "history.apply", "request-1", nil},
		{"different identity", bound, "mutation.apply", "request-2", nil},
		{"identity is not normalized", bound, "mutation.apply", " request-1", nil},
	} {
		t.Run(sample.name, func(t *testing.T) {
			if got := ReplayedBusinessWrite(sample.ctx, sample.kind, sample.identity); got != sample.want {
				t.Fatalf("replay signal = %v, want %v", got, sample.want)
			}
		})
	}
}

func openBusinessReplayCoordinator(t *testing.T, path string) (*WorkspaceWriteCoordinator, Token) {
	t.Helper()
	coordinator, err := OpenPersistent(path, "workspace-1", 1, "claim-a", 1)
	if err != nil {
		t.Fatal(err)
	}

	token, _ := coordinator.Current()
	return coordinator, token
}

func businessReplaySignal(ctx context.Context, intent WriteIntent) error {
	bound, err := WithBusinessIntent(ctx, intent, "mutation.apply", "request-1")
	if err != nil {
		return err
	}
	return ReplayedBusinessWrite(bound, "mutation.apply", "request-1")
}

func TestBusinessReplayAbortsPreparedIntentWithoutAdvancing(t *testing.T) {
	coordinator, token := openBusinessReplayCoordinator(t, filepath.Join(t.TempDir(), "coordination.db"))
	t.Cleanup(func() {
		if err := coordinator.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()
	first, err := coordinator.Write(ctx, token, func(context.Context, WriteIntent) error { return nil })
	if err != nil || first.MutationRevision != 1 {
		t.Fatalf("initial write: %#v, %v", first, err)
	}
	_, before := coordinator.Current()
	receipt, err := coordinator.Write(ctx, token, businessReplaySignal)
	if err != ErrBusinessReplay || receipt != (WriteReceipt{}) {
		t.Fatalf("replay abort: %#v, %v", receipt, err)
	}
	state := coordinator.RecoveryState()
	if state.Token != token || state.Counters != before || state.PendingMutationRevision != 0 {
		t.Fatalf("replay advanced state or left a prepared intent: %#v", state)
	}
	var persisted string
	if err := coordinator.store.db.QueryRow(`SELECT state FROM mutation_intents WHERE mutation_revision=2`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != "failed" {
		t.Fatalf("replay intent was not durably aborted: %q", persisted)
	}
	next, err := coordinator.Write(ctx, token, func(context.Context, WriteIntent) error { return nil })
	if err != nil || next.MutationRevision != 2 {
		t.Fatalf("new write after replay: %#v, %v", next, err)
	}
}

func TestBusinessReplayAbortFailurePreservesPendingRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordination.db")
	coordinator, token := openBusinessReplayCoordinator(t, path)
	storeClosed := false
	t.Cleanup(func() {
		if !storeClosed {
			if err := coordinator.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	ctx := context.Background()
	first, err := coordinator.Write(ctx, token, func(context.Context, WriteIntent) error { return nil })
	if err != nil || first.MutationRevision != 1 {
		t.Fatalf("initial write: %#v, %v", first, err)
	}
	// Prepare revision 2 using the real persistent store, then make the abort
	// fail. No production fault API or replacement transaction is involved.
	receipt, err := coordinator.Write(ctx, token, func(ctx context.Context, intent WriteIntent) error {
		if intent.MutationRevision != 2 {
			t.Fatalf("prepared revision = %d", intent.MutationRevision)
		}
		if err := coordinator.store.db.Close(); err != nil {
			t.Fatal(err)
		}
		storeClosed = true
		return businessReplaySignal(ctx, intent)
	})
	if err == ErrBusinessReplay || !errors.Is(err, ErrBusinessReplay) || receipt != (WriteReceipt{}) {
		t.Fatalf("abort failure was consumed as a successful replay: %#v, %v", receipt, err)
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok || len(joined.Unwrap()) != 2 || joined.Unwrap()[1] == nil {
		t.Fatalf("abort failure lost its persistence error: %v", err)
	}
	state := coordinator.RecoveryState()
	if state.Counters.MutationRevision != 1 || state.PendingMutationRevision != 2 {
		t.Fatalf("failed abort lost the prepared revision: %#v", state)
	}
	reopened, reopenedToken := openBusinessReplayCoordinator(t, path)
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	})
	recovered := reopened.RecoveryState()
	if recovered != state || reopenedToken != token {
		t.Fatalf("reopened recovery = %#v, want %#v", recovered, state)
	}
	called := false
	if _, err := reopened.Write(ctx, token, func(context.Context, WriteIntent) error { called = true; return nil }); !errors.Is(err, ErrRecoveryRequired) || called {
		t.Fatalf("pending recovery admitted a new write: called=%v error=%v", called, err)
	}
	if err := reopened.ResolvePreparedMutation(ctx, token, first.MutationRevision, false); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("old committed revision resolved the new pending intent: %v", err)
	}
	if reopened.RecoveryState() != recovered {
		t.Fatal("wrong revision recovery changed authority")
	}
	var prior string
	if err := reopened.store.db.QueryRow(`SELECT state FROM mutation_intents WHERE mutation_revision=1`).Scan(&prior); err != nil {
		t.Fatal(err)
	}
	if prior != "committed" {
		t.Fatalf("old commit was rewritten: %s", prior)
	}
	if err := reopened.ResolvePreparedMutation(ctx, token, 2, false); err != nil {
		t.Fatal(err)
	}
	state = reopened.RecoveryState()
	if state.Counters.MutationRevision != 1 || state.PendingMutationRevision != 0 {
		t.Fatalf("resolved abort: %#v", state)
	}
	next, err := reopened.Write(ctx, token, func(context.Context, WriteIntent) error { return nil })
	if err != nil || next.MutationRevision != 2 {
		t.Fatalf("write after recovery: %#v, %v", next, err)
	}
}
