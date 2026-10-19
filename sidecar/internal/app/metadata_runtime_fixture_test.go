package app

import (
	"context"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
)

type metadataRuntimeFixture struct {
	pb       *pocketbase.PocketBase
	runtime  *workspacev2.Runtime
	ledger   *auditledger.Ledger
	metadata string
}

func newMetadataRuntimeFixture(t *testing.T) *metadataRuntimeFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace", ".vibetable")
	for _, name := range []string{"data", "topology", "objects", "audit", "snapshots", "coordination", "quarantine", "temp"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{"contractVersion":"2.0","formatVersion":3,"workspaceId":"11111111-1111-4111-8111-111111111111","displayName":"Metadata Replay","createdAt":"2026-07-28T08:00:00Z","storageMode":"direct","encryptionMode":"convenient","repositoryFormat":"kopia-v4","topologySchemaVersion":1,"businessSchemaVersion":1,"importedFromWorkspaceId":null,"sourceSnapshotId":null}`
	if err := os.WriteFile(filepath.Join(root, "workspace.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := &metadataRuntimeFixture{pb: historyProductStore(t, filepath.Join(root, "data")), metadata: root}
	fixture.open(t)
	t.Cleanup(func() { fixture.close(t) })
	return fixture
}

func (f *metadataRuntimeFixture) open(t *testing.T) {
	t.Helper()
	var err error
	f.ledger, err = auditledger.Open(filepath.Join(f.metadata, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	history, err := audit.New(f.pb, mutation.New(f.pb, mutation.MetadataSchemaSource{}), audit.WithLedgerHistory(f.ledger))
	if err != nil {
		t.Fatal(err)
	}
	f.runtime, err = workspacev2.Open(context.Background(), workspacev2.Options{
		App: f.pb, DataDir: f.pb.DataDir(), WorkspaceID: "11111111-1111-4111-8111-111111111111",
		SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Ledger: f.ledger, Audit: history, DeferBackgroundWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *metadataRuntimeFixture) close(t *testing.T) {
	t.Helper()
	if f.runtime != nil {
		if err := f.runtime.Close(context.Background()); err != nil {
			t.Error(err)
		}
		f.runtime = nil
	}
	if f.ledger != nil {
		if err := f.ledger.Close(); err != nil {
			t.Error(err)
		}
		f.ledger = nil
	}
}

type metadataRuntimeState struct {
	values   map[string]string
	revision uint64
	receipts int
}

func (f *metadataRuntimeFixture) state(t *testing.T) metadataRuntimeState {
	t.Helper()
	state := metadataRuntimeState{values: previewAuthorityState(t, f.pb, "vibetable_shared_settings")}
	var err error
	state.revision, err = writecoordinator.ReadPersistentMutationRevision(context.Background(), filepath.Join(f.metadata, "coordination", "write-coordinator.db"), "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.pb.DB().NewQuery(`SELECT COUNT(*) FROM workspace_v2_mutation_receipts`).Row(&state.receipts); err != nil {
		t.Fatal(err)
	}
	// Preserve the old fixture's observations while sharing runtime lifecycle.
	for _, collection := range []string{"vibetable_interfaces", "vibetable_dashboards", "vibetable_panels"} {
		for key, value := range previewAuthorityState(t, f.pb, collection) {
			state.values[collection+":"+key] = value
		}
	}
	return state
}
