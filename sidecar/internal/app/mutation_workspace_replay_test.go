package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type workspaceMutationReplayFixture struct {
	pb                  *pocketbase.PocketBase
	runtime             *workspacev2.Runtime
	mux                 http.Handler
	definition          schemaexecution.Table
	field, coordination string
	request             mutation.Request
}

func newWorkspaceMutationReplayFixture(t *testing.T) workspaceMutationReplayFixture {
	t.Helper()
	ctx := context.Background()
	metadata := filepath.Join(t.TempDir(), "workspace", ".vibetable")
	for _, name := range []string{"data", "topology", "objects", "audit", "snapshots", "coordination", "quarantine", "temp"} {
		if err := os.MkdirAll(filepath.Join(metadata, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	workspaceID := "11111111-1111-4111-8111-111111111111"
	manifest := `{"contractVersion":"2.0","formatVersion":2,"workspaceId":"` + workspaceID + `","displayName":"Mutation Product","createdAt":"2026-07-28T08:00:00Z","storageMode":"direct","encryptionMode":"convenient","repositoryFormat":"kopia-v3","topologySchemaVersion":1,"businessSchemaVersion":1,"importedFromWorkspaceId":null,"sourceSnapshotId":null}`
	if err := os.WriteFile(filepath.Join(metadata, "workspace.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(metadata, "data")
	pb := historyProductStore(t, dataDir)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(ctx, v2.TableCreateIntent{
		DisplayName: "Mutation HTTP", OperationID: "mutation-http-table", Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	field := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Title", "mutation-http-field")
	definition, err := schemaexecution.Describe(ctx, pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := auditledger.Open(filepath.Join(metadata, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ledger.Close(); err != nil {
			t.Error(err)
		}
	})
	kernel := mutation.New(pb, mutation.MetadataSchemaSource{})
	history, err := audit.New(pb, kernel, audit.WithLedgerHistory(ledger))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspacev2.Open(ctx, workspacev2.Options{
		App: pb, DataDir: dataDir, WorkspaceID: workspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Ledger: ledger, Audit: history, DeferBackgroundWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: request}}, nil
	})
	registerMutationRoutes(r, kernel, nil, runtime.CoordinateBusinessWrite)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	recordID := "mutprod00000001"
	return workspaceMutationReplayFixture{
		pb: pb, runtime: runtime, coordination: filepath.Join(metadata, "coordination", "write-coordinator.db"), mux: mux, definition: definition,
		field: field.Definition.Identity.PhysicalName,
		request: mutation.Request{
			ContractVersion: mutation.ContractVersion, RequestID: "mutation-http-request",
			IdempotencyKey: "mutation-http-insert", TableID: table.TableID, SchemaRevision: field.SchemaRevision,
			Actor: mutation.Actor{Type: "user", ID: "local-user"},
			Operations: []mutation.Operation{{Kind: mutation.OperationInsert, RecordID: &recordID,
				Values: map[string]any{field.Definition.Identity.PhysicalName: "中文 Cafe\u0301 👩🏽‍💻"}}},
		},
	}
}

func (f workspaceMutationReplayFixture) call(t *testing.T, ctx context.Context, input mutation.Request) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/vibetable/v1/mutations/apply", bytes.NewReader(raw)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	f.mux.ServeHTTP(response, request)
	return response
}

func workspaceMutationReplayReceipt(t *testing.T, response *httptest.ResponseRecorder) mutation.Receipt {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("REST status=%d body=%s", response.Code, response.Body)
	}
	var receipt mutation.Receipt
	if err := mutation.DecodeStrict(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

type workspaceMutationReplayState struct {
	business map[string]string
	revision uint64
	proofs   int
}

func (f workspaceMutationReplayFixture) state(t *testing.T) workspaceMutationReplayState {
	t.Helper()
	state := workspaceMutationReplayState{business: previewAuthorityState(t, f.pb, f.definition.PhysicalName)}
	var err error
	state.revision, err = writecoordinator.ReadPersistentMutationRevision(context.Background(), f.coordination, "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.pb.DB().NewQuery(`SELECT COUNT(*) FROM workspace_v2_mutation_receipts`).Row(&state.proofs); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestWorkspaceMutationReplayPreservesReceiptAndAuthority(t *testing.T) {
	f := newWorkspaceMutationReplayFixture(t)
	initial := f.state(t)
	original := workspaceMutationReplayReceipt(t, f.call(t, context.Background(), f.request))
	if original.Status != mutation.StatusApplied || original.ChangeSetID == nil || original.NewRevision == nil ||
		len(original.AffectedRows) != 1 || len(original.EmittedEvents) != 1 ||
		original.AffectedRows[0].RecordID != *f.request.Operations[0].RecordID ||
		original.AffectedRows[0].Revision == "" || original.AffectedRows[0].Digest == "" {
		t.Fatalf("incomplete initial receipt: %#v", original)
	}
	record, err := f.pb.FindRecordById(f.definition.PhysicalName, original.AffectedRows[0].RecordID)
	if err != nil || record.GetString(f.field) != f.request.Operations[0].Values[f.field] {
		t.Fatalf("initial record: %#v, %v", record, err)
	}
	committed := f.state(t)
	if committed.revision != initial.revision+1 || committed.proofs != initial.proofs+1 {
		t.Fatalf("initial gate failed to commit exactly once: before=%+v after=%+v", initial, committed)
	}
	replayed := workspaceMutationReplayReceipt(t, f.call(t, context.Background(), f.request))
	original.Status = mutation.StatusReplayed
	if !reflect.DeepEqual(original, replayed) {
		t.Fatalf("replay receipt changed: %#v != %#v", replayed, original)
	}
	if after := f.state(t); !reflect.DeepEqual(committed, after) {
		t.Fatal("replay changed business rows, audit, events, idempotency, proofs or coordination revision")
	}

	next := f.request
	next.RequestID, next.IdempotencyKey = "workspace-replay-next", "workspace-replay-next"
	next.Operations = []mutation.Operation{{Kind: mutation.OperationUpdate, RecordID: f.request.Operations[0].RecordID,
		Values: map[string]any{f.field: "after replay"}, ExpectedRevision: &original.AffectedRows[0].Revision}}
	advanced := workspaceMutationReplayReceipt(t, f.call(t, context.Background(), next))
	if advanced.Status != mutation.StatusApplied || len(advanced.AffectedRows) != 1 || advanced.NewRevision == nil ||
		*advanced.NewRevision == *original.NewRevision {
		t.Fatalf("new write failed after replay: %#v", advanced)
	}
	after := f.state(t)
	if after.revision != committed.revision+1 || after.proofs != committed.proofs+1 {
		t.Fatal("new write did not advance exactly once after replay")
	}
	record, err = f.pb.FindRecordById(f.definition.PhysicalName, original.AffectedRows[0].RecordID)
	if err != nil || record.GetString(f.field) != "after replay" {
		t.Fatalf("new write missing: %#v, %v", record, err)
	}
}

func TestWorkspaceMutationReplayRejectsChangedPayloadCanceledAndClosedGate(t *testing.T) {
	f := newWorkspaceMutationReplayFixture(t)
	workspaceMutationReplayReceipt(t, f.call(t, context.Background(), f.request))
	before := f.state(t)
	changed := f.request
	changed.Operations = []mutation.Operation{{Kind: mutation.OperationInsert, RecordID: f.request.Operations[0].RecordID,
		Values: map[string]any{f.field: "different payload"}}}
	response := f.call(t, context.Background(), changed)
	var conflict mutation.ProductError
	if err := json.Unmarshal(response.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusConflict || conflict.Code != "mutation.idempotency_conflict" ||
		conflict.Path == nil || *conflict.Path != "idempotencyKey" {
		t.Fatalf("changed request replayed: %d %s", response.Code, response.Body)
	}
	if !reflect.DeepEqual(before, f.state(t)) {
		t.Fatal("idempotency conflict changed authority")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response = f.call(t, ctx, f.request)
	if response.Code == http.StatusOK {
		t.Fatalf("canceled request returned a replay: %s", response.Body)
	}
	if !reflect.DeepEqual(before, f.state(t)) {
		t.Fatal("canceled replay changed authority")
	}

	if err := f.runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Closing drains existing audit entries; compare the stable retired state.
	before = f.state(t)
	response = f.call(t, context.Background(), f.request)
	var unavailable mutation.ProductError
	if err := json.Unmarshal(response.Body.Bytes(), &unavailable); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusInternalServerError || unavailable.Code != "mutation.internal.failed" || !unavailable.Retryable {
		t.Fatalf("closed gate returned a replay: %d %s", response.Code, response.Body)
	}
	if !reflect.DeepEqual(before, f.state(t)) {
		t.Fatal("closed gate changed authority")
	}
}

func TestWorkspaceMutationReplaySerializesConcurrentSameKey(t *testing.T) {
	f := newWorkspaceMutationReplayFixture(t)
	initial := f.state(t)
	raw, err := json.Marshal(f.request)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			<-start
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/vibetable/v1/mutations/apply", bytes.NewReader(raw))
			request.Header.Set("Content-Type", "application/json")
			f.mux.ServeHTTP(response, request)
			responses <- response
		}()
	}
	close(start)
	first, second := <-responses, <-responses
	left := workspaceMutationReplayReceipt(t, first)
	right := workspaceMutationReplayReceipt(t, second)
	if left.Status == mutation.StatusReplayed {
		left, right = right, left
	}
	if left.Status != mutation.StatusApplied || right.Status != mutation.StatusReplayed {
		t.Fatalf("concurrent statuses: %s, %s", left.Status, right.Status)
	}
	left.Status = mutation.StatusReplayed
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("concurrent replay changed receipt: %#v != %#v", left, right)
	}
	committed := f.state(t)
	if committed.revision != initial.revision+1 || committed.proofs != initial.proofs+1 {
		t.Fatal("concurrent same-key requests did not commit exactly once")
	}
	workspaceMutationReplayReceipt(t, f.call(t, context.Background(), f.request))
	if !reflect.DeepEqual(committed, f.state(t)) {
		t.Fatal("subsequent replay changed committed authority")
	}
}
func TestWorkspaceMutationReplayCancellationDuringReceiptRead(t *testing.T) {
	f := newWorkspaceMutationReplayFixture(t)
	workspaceMutationReplayReceipt(t, f.call(t, context.Background(), f.request))
	before := f.state(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The existing clock seam runs while checking the stored receipt expiry.
	// Cancel at that read boundary, after admission but before replay returns.
	kernel := mutation.New(f.pb, mutation.MetadataSchemaSource{}, mutation.WithClock(func() time.Time {
		cancel()
		return time.Now()
	}))
	var receipt mutation.Receipt
	var kernelErr error
	err := f.runtime.CoordinateBusinessWrite(ctx, "mutation.apply", f.request.IdempotencyKey, func(writeCtx context.Context) error {
		receipt, kernelErr = kernel.Apply(writeCtx, f.request)
		return kernelErr
	})
	if !errors.Is(kernelErr, context.Canceled) || !errors.Is(err, context.Canceled) ||
		!reflect.DeepEqual(receipt, mutation.Receipt{}) {
		t.Fatalf("canceled receipt read exposed replay: receipt=%#v kernel=%v runtime=%v", receipt, kernelErr, err)
	}
	if !reflect.DeepEqual(before, f.state(t)) {
		t.Fatal("canceled receipt read changed authority")
	}
	replayed := workspaceMutationReplayReceipt(t, f.call(t, context.Background(), f.request))
	if replayed.Status != mutation.StatusReplayed || !reflect.DeepEqual(before, f.state(t)) {
		t.Fatal("canceled receipt read left a pending gate or changed the later replay")
	}
}
