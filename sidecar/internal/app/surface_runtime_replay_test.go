package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type surfaceRuntimeFixture struct {
	pb       *pocketbase.PocketBase
	runtime  *workspacev2.Runtime
	ledger   *auditledger.Ledger
	metadata string
}

func newSurfaceRuntimeFixture(t *testing.T) *surfaceRuntimeFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace", ".vibetable")
	for _, name := range []string{"data", "topology", "objects", "audit", "snapshots", "coordination", "quarantine", "temp"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{"contractVersion":"2.0","formatVersion":2,"workspaceId":"11111111-1111-4111-8111-111111111111","displayName":"Surface Replay","createdAt":"2026-07-28T08:00:00Z","storageMode":"direct","encryptionMode":"convenient","repositoryFormat":"kopia-v3","topologySchemaVersion":1,"businessSchemaVersion":1,"importedFromWorkspaceId":null,"sourceSnapshotId":null}`
	if err := os.WriteFile(filepath.Join(root, "workspace.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := &surfaceRuntimeFixture{pb: historyProductStore(t, filepath.Join(root, "data")), metadata: root}
	fixture.open(t)
	t.Cleanup(func() { fixture.close(t) })
	return fixture
}

func (f *surfaceRuntimeFixture) open(t *testing.T) {
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

func (f *surfaceRuntimeFixture) close(t *testing.T) {
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

func (f *surfaceRuntimeFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	// These are the two real production gates, not an apply-through test double.
	return surfaceHTTPServer(t, f.pb, f.runtime.CoordinateBusinessWrite, f.runtime.CoordinateIdempotentBusinessWrite)
}

type surfaceRuntimeState struct {
	values   map[string]string
	revision uint64
	receipts int
}

func (f *surfaceRuntimeFixture) state(t *testing.T) surfaceRuntimeState {
	t.Helper()
	state := surfaceRuntimeState{values: previewAuthorityState(t, f.pb, "vibetable_interfaces")}
	var err error
	state.revision, err = writecoordinator.ReadPersistentMutationRevision(context.Background(), filepath.Join(f.metadata, "coordination", "write-coordinator.db"), "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.pb.DB().NewQuery(`SELECT COUNT(*) FROM workspace_v2_mutation_receipts`).Row(&state.receipts); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestSurfaceProductRealRuntimeGatePreservesReplayAndRejectsPayloadReuse(t *testing.T) {
	f := newSurfaceRuntimeFixture(t)
	server := f.server(t)
	create := `{"definition":` + surfaceHTTPDefinition + `,"expectedRevision":null,"idempotencyKey":"surface-runtime-create"}`
	initial := f.state(t)
	first := surfaceHTTPRequest(t, server, "interface.commit", create)
	revision := surfaceHTTPRevision(t, first)
	committed := f.state(t)
	if committed.revision != initial.revision+1 || committed.receipts != initial.receipts+1 {
		t.Fatal("first commit did not allocate exactly one workspace revision/receipt")
	}
	replay := surfaceHTTPRequest(t, server, "interface.commit", create)
	if replay.Error != nil || !bytes.Equal(first.Result, replay.Result) {
		t.Errorf("real gate lost commit receipt: %s %#v", replay.Result, replay.Error)
	}
	conflict := surfaceHTTPRequest(t, server, "interface.commit", strings.Replace(create, `"name":"Orders"`, `"name":"Changed"`, 1))
	if conflict.Error == nil || conflict.Error.Data["code"] != "surface.idempotency_conflict" {
		t.Errorf("real gate bypassed commit digest: %s %#v", conflict.Result, conflict.Error)
	}
	if !reflect.DeepEqual(committed, f.state(t)) {
		t.Error("commit replay/conflict changed authority or coordinator revision")
	}
	update := strings.Replace(create, `"expectedRevision":null`, `"expectedRevision":"`+revision+`"`, 1)
	update = strings.Replace(update, "surface-runtime-create", "surface-runtime-update", 1)
	update = strings.Replace(update, `"name":"Orders"`, `"name":"Updated"`, 1)
	revision = surfaceHTTPRevision(t, surfaceHTTPRequest(t, server, "interface.commit", update))
	advanced := f.state(t)
	if advanced.revision != committed.revision+1 || advanced.receipts != committed.receipts+1 {
		t.Fatal("new commit after replay did not allocate exactly one revision/receipt")
	}
	deletion := `{"interfaceId":"interface-orders","expectedRevision":"` + revision + `","idempotencyKey":"surface-runtime-delete"}`
	deleted := surfaceHTTPRequest(t, server, "interface.delete", deletion)
	if deleted.Error != nil || string(deleted.Result) != `{"interfaceId":"interface-orders"}` {
		t.Fatalf("delete: %s %#v", deleted.Result, deleted.Error)
	}
	afterDelete := f.state(t)
	replay = surfaceHTTPRequest(t, server, "interface.delete", deletion)
	if replay.Error != nil || !bytes.Equal(deleted.Result, replay.Result) {
		t.Errorf("real gate lost delete receipt: %s %#v", replay.Result, replay.Error)
	}
	conflict = surfaceHTTPRequest(t, server, "interface.delete", strings.Replace(deletion, revision, "different-revision", 1))
	if conflict.Error == nil || conflict.Error.Data["code"] != "surface.idempotency_conflict" {
		t.Errorf("real gate bypassed delete digest: %s %#v", conflict.Result, conflict.Error)
	}
	if !reflect.DeepEqual(afterDelete, f.state(t)) {
		t.Error("delete replay/conflict changed authority or coordinator revision")
	}
	server.Close()
	f.close(t)
	if err := f.pb.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}
	if err := f.pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	f.open(t)
	server = f.server(t)
	restarted := f.state(t)
	for _, sample := range []struct {
		method, params string
		result         json.RawMessage
	}{{"interface.commit", create, first.Result}, {"interface.delete", deletion, deleted.Result}} {
		response := surfaceHTTPRequest(t, server, sample.method, sample.params)
		if response.Error != nil || !bytes.Equal(response.Result, sample.result) {
			t.Errorf("durable gate replay %s: %s %#v", sample.method, response.Result, response.Error)
		}
	}
	if !reflect.DeepEqual(restarted, f.state(t)) {
		t.Error("restart replay changed authority or coordinator revision")
	}
	missing := surfaceHTTPRequest(t, server, "interface.load", `{"interfaceId":"interface-orders"}`)
	if missing.Error == nil || missing.Error.Data["code"] != "surface.not_found" {
		t.Errorf("replay resurrected deleted interface: %#v", missing)
	}
	f.close(t)
	retired := f.state(t)
	rejected := surfaceHTTPRequest(t, server, "interface.commit", create)
	if rejected.Error == nil {
		t.Errorf("closed Runtime gate returned success: %s", rejected.Result)
	}
	if !reflect.DeepEqual(retired, f.state(t)) {
		t.Error("closed gate changed authority")
	}
}

func TestSurfaceProductRealRuntimeGateRejectsStaleScopeAndCancellation(t *testing.T) {
	f := newSurfaceRuntimeFixture(t)
	server := f.server(t)
	create := `{"definition":` + surfaceHTTPDefinition + `,"expectedRevision":null,"idempotencyKey":"surface-runtime-rejection"}`
	surfaceHTTPRevision(t, surfaceHTTPRequest(t, server, "interface.commit", create))
	before := f.state(t)
	for _, wire := range []string{
		strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":8`, 1),
		strings.Replace(schemaListWire, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1),
	} {
		raw := `{"jsonrpc":"2.0","id":"scope-reject","method":"interface.commit","wire":` + wire + `,"params":` + create + `}`
		response, err := server.Client().Post(server.URL+productRPCPath, "application/json", strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		var envelope productrpc.ResponseEnvelope
		decodeErr := json.NewDecoder(response.Body).Decode(&envelope)
		response.Body.Close()
		if decodeErr != nil || response.StatusCode != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != productrpc.CodeInvalidRequest {
			t.Fatalf("stale workspace scope not rejected: HTTP %d %#v %v", response.StatusCode, envelope.Error, decodeErr)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	registration := surfaceCommitRegistration(metadata.NewSurface(f.pb), f.runtime.CoordinateBusinessWrite, f.runtime.CoordinateIdempotentBusinessWrite)
	if result, err := registration.Handler(ctx, json.RawMessage(create)); err == nil {
		t.Fatalf("canceled request returned replay: %#v", result)
	}
	if !reflect.DeepEqual(before, f.state(t)) {
		t.Fatal("rejected scope/cancellation changed authority or mutation revision")
	}
}
