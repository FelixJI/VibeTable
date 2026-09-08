package workspacev2

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func TestBusinessReplayConsumesOnlyStandaloneSignal(t *testing.T) {
	abortFailure := errors.New("abort persistence failed")
	for _, test := range []struct {
		name                    string
		joined, cancel, pending bool
	}{
		{name: "replayed"},
		{name: "existing replica pending", pending: true},
		{name: "abort failure", joined: true},
		{name: "cancelled replay", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator, err := writecoordinator.New("replay-workspace", 1, "claim", 1)
			if err != nil {
				t.Fatal(err)
			}
			runtime := &Runtime{coordinator: coordinator}
			pendingPath := filepath.Join(t.TempDir(), "replica-pending.json")
			if test.pending {
				if err := os.WriteFile(pendingPath, []byte("existing pending"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runtime.replicaConflict = &productionReplicaConflict{runtime: runtime, pendingPath: pendingPath}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := false
			err = runtime.CoordinateBusinessWrite(ctx, "mutation.apply", "key", func(writeCtx context.Context) error {
				entered = true
				signal := writecoordinator.ReplayedBusinessWrite(writeCtx, "mutation.apply", "key")
				if signal != writecoordinator.ErrBusinessReplay {
					t.Fatal("missing exact bound intent")
				}
				if test.cancel {
					cancel()
				}
				if test.joined {
					return errors.Join(signal, abortFailure)
				}
				return signal
			})
			if !entered {
				t.Fatal("write callback did not run")
			}
			switch {
			case test.joined:
				if !errors.Is(err, abortFailure) || err == writecoordinator.ErrBusinessReplay {
					t.Fatalf("joined failure consumed: %v", err)
				}
			case test.cancel:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled replay = %v", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			_, counters := coordinator.Current()
			if counters.MutationRevision != 0 {
				t.Fatalf("replay advanced revision: %+v", counters)
			}
			content, readErr := os.ReadFile(pendingPath)
			if test.pending {
				if readErr != nil || string(content) != "existing pending" {
					t.Fatalf("existing replica state changed: %q %v", content, readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("replay created replica work: %q %v", content, readErr)
			}
		})
	}
}
