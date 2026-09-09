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

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
)

type unrelatedMutationProductMustNotRun struct{ t *testing.T }

func (p unrelatedMutationProductMustNotRun) Preview(context.Context, mutation.Request) (mutation.PreviewResult, error) {
	p.t.Helper()
	p.t.Fatal("unrelated fixture must not invoke mutation.preview")
	return mutation.PreviewResult{}, nil
}

func (p unrelatedMutationProductMustNotRun) Apply(context.Context, mutation.Request) (mutation.Receipt, error) {
	p.t.Helper()
	p.t.Fatal("unrelated fixture must not invoke mutation.apply")
	return mutation.Receipt{}, nil
}

// Count entry without replacing the authority, so rejected scope/gate requests
// cannot accidentally exercise a permissive fixture instead of the real kernel.
type mutationHTTPAuthority struct {
	kernel            *mutation.Kernel
	previews, applies int
}

func (a *mutationHTTPAuthority) Preview(ctx context.Context, request mutation.Request) (mutation.PreviewResult, error) {
	a.previews++
	return a.kernel.Preview(ctx, request)
}

func (a *mutationHTTPAuthority) Apply(ctx context.Context, request mutation.Request) (mutation.Receipt, error) {
	a.applies++
	return a.kernel.Apply(ctx, request)
}

type mutationHTTPFixture struct {
	pb         *pocketbase.PocketBase
	runtime    *workspacev2.Runtime
	authority  *mutationHTTPAuthority
	mux        http.Handler
	definition schemaexecution.Table
	field      string
	request    mutation.Request
}

func newMutationHTTPFixture(t *testing.T) mutationHTTPFixture {
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
	authority := &mutationHTTPAuthority{kernel: kernel}
	registrations := []productrpc.Registration{
		mutationPreviewRegistration(authority), mutationApplyRegistration(authority, runtime.CoordinateBusinessWrite),
	}
	for _, descriptor := range productcapabilities.CurrentOwnerRPCDescriptors(productcapabilities.GoSidecar) {
		if descriptor.Method == "mutation.preview" || descriptor.Method == "mutation.apply" {
			continue
		}
		registrations = append(registrations, productrpc.Registration{
			Method: descriptor.Method, Scope: descriptor.Scope,
			ValidateParams: func(json.RawMessage) error { t.Fatal("unexpected unrelated validation"); return nil },
			Handler: func(context.Context, json.RawMessage) (any, error) {
				t.Fatal("unexpected unrelated method")
				return nil, nil
			},
		})
	}
	capabilities := runtime.Capabilities()
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: capabilities.WorkspaceID, SessionEpoch: capabilities.SessionEpoch,
		FenceEpoch: capabilities.FenceEpoch, ClaimID: capabilities.ClaimID,
	}, registrations...)
	if err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: request}}, nil
	})
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	recordID := "mutprod00000001"
	return mutationHTTPFixture{
		pb: pb, runtime: runtime, authority: authority, mux: mux, definition: definition,
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

func (f mutationHTTPFixture) call(t *testing.T, method string, request mutation.Request, wire string) productrpc.ResponseEnvelope {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return schemaProductRequestForMethod(t, f.mux, context.Background(), method, string(raw), wire)
}

func (f mutationHTTPFixture) state(t *testing.T) map[string]string {
	t.Helper()
	return previewAuthorityState(t, f.pb, f.definition.PhysicalName)
}

func TestMutationProductHTTPPreviewApplyAndIdempotentReplay(t *testing.T) {
	f := newMutationHTTPFixture(t)
	before := f.state(t)
	preview := f.call(t, "mutation.preview", f.request, schemaListWire)
	if preview.Error != nil {
		t.Fatalf("preview: %+v", preview.Error)
	}
	var result mutation.PreviewResult
	if err := json.Unmarshal(preview.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Definition.Snapshot.TableID != f.request.TableID || len(result.Operations) != 1 ||
		result.Operations[0].Values[f.field] != f.request.Operations[0].Values[f.field] {
		t.Fatalf("preview projection: %#v", result)
	}
	if !reflect.DeepEqual(before, f.state(t)) {
		t.Fatal("preview changed authority")
	}
	applied := f.call(t, "mutation.apply", f.request, schemaListWire)
	if applied.Error != nil {
		t.Fatalf("apply: %+v", applied.Error)
	}
	var receipt mutation.Receipt
	if err := mutation.DecodeStrict(applied.Result, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != mutation.StatusApplied || receipt.ChangeSetID == nil || receipt.NewRevision == nil ||
		len(receipt.AffectedRows) != 1 || receipt.AffectedRows[0].RecordID != *f.request.Operations[0].RecordID ||
		receipt.AffectedRows[0].Revision == "" || receipt.AffectedRows[0].Digest == "" || len(receipt.EmittedEvents) != 1 {
		t.Fatalf("incomplete committed receipt: %#v", receipt)
	}
	record, err := f.pb.FindRecordById(f.definition.PhysicalName, receipt.AffectedRows[0].RecordID)
	if err != nil || record.GetString(f.field) != f.request.Operations[0].Values[f.field] {
		t.Fatalf("committed record: %#v, %v", record, err)
	}
	var proofs int
	if err := f.pb.DB().NewQuery(`SELECT COUNT(*) FROM workspace_v2_mutation_receipts WHERE kind='mutation.apply' AND identity={:identity} AND session_epoch=7`).Bind(dbx.Params{"identity": f.request.IdempotencyKey}).Row(&proofs); err != nil {
		t.Fatal(err)
	}
	if proofs != 1 {
		t.Fatalf("workspace gate commit proofs = %d", proofs)
	}
	after := f.state(t)
	repeated := f.call(t, "mutation.apply", f.request, schemaListWire)
	if repeated.Error != nil {
		t.Fatalf("idempotent replay: %+v", repeated.Error)
	}
	var replay mutation.Receipt
	if err := mutation.DecodeStrict(repeated.Result, &replay); err != nil {
		t.Fatal(err)
	}
	receipt.Status = mutation.StatusReplayed
	if !reflect.DeepEqual(receipt, replay) || !reflect.DeepEqual(after, f.state(t)) {
		t.Fatal("replay duplicated a record, revision, audit, event, or idempotency effect")
	}
	if f.authority.previews != 1 || f.authority.applies != 2 {
		t.Fatalf("authority calls: %+v", f.authority)
	}
}

func TestMutationProductHTTPRejectsStaleRevisionEpochAndRetiredGate(t *testing.T) {
	f := newMutationHTTPFixture(t)
	inserted := f.call(t, "mutation.apply", f.request, schemaListWire)
	if inserted.Error != nil {
		t.Fatal(inserted.Error)
	}
	request := f.request
	request.RequestID, request.IdempotencyKey = "mutation-http-stale", "mutation-http-stale"
	stale := "row_0000"
	request.Operations = []mutation.Operation{{Kind: mutation.OperationUpdate,
		RecordID: f.request.Operations[0].RecordID, Values: map[string]any{f.field: "must not persist"}, ExpectedRevision: &stale}}
	before := f.state(t)
	rejected := f.call(t, "mutation.apply", request, schemaListWire)
	if rejected.Error == nil || rejected.Error.Code != productrpc.CodeProductData ||
		rejected.Error.Data["code"] != "mutation.revision_conflict" || rejected.Error.Data["path"] != "operations[0].expectedRevision" ||
		rejected.Error.Data["retryable"] != false {
		t.Fatalf("stale revision: %+v", rejected.Error)
	}
	if !reflect.DeepEqual(before, f.state(t)) {
		t.Fatal("stale revision changed authority")
	}
	request.Operations[0].ExpectedRevision = nil
	calls := f.authority.applies
	oldWire := strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":6`, 1)
	rejected = f.call(t, "mutation.apply", request, oldWire)
	if rejected.Error == nil || rejected.Error.Code != productrpc.CodeInvalidRequest || f.authority.applies != calls {
		t.Fatalf("old epoch reached authority: %+v", rejected.Error)
	}
	if !reflect.DeepEqual(before, f.state(t)) {
		t.Fatal("old epoch changed authority")
	}
	if err := f.runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	before = f.state(t)
	rejected = f.call(t, "mutation.apply", request, schemaListWire)
	if rejected.Error == nil || rejected.Error.Code != productrpc.CodeProductData ||
		rejected.Error.Data["code"] != "mutation.internal.failed" || rejected.Error.Data["retryable"] != true ||
		f.authority.applies != calls {
		t.Fatalf("retired workspace gate: %+v", rejected.Error)
	}
	if !reflect.DeepEqual(before, f.state(t)) {
		t.Fatal("retired gate changed authority")
	}
}

func TestMutationExistingRESTReplaysThroughWorkspaceGate(t *testing.T) {
	f := newMutationHTTPFixture(t)
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: request}}, nil
	})
	registerMutationRoutes(r, f.authority, nil, f.runtime.CoordinateBusinessWrite)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(f.request)
	if err != nil {
		t.Fatal(err)
	}
	var original mutation.Receipt
	var committed map[string]string
	for attempt := 0; attempt < 2; attempt++ {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/vibetable/v1/mutations/apply", bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("existing REST attempt %d: status=%d body=%s", attempt+1, response.Code, response.Body)
		}
		var receipt mutation.Receipt
		if err := mutation.DecodeStrict(response.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			if receipt.Status != mutation.StatusApplied {
				t.Fatalf("initial REST status: %s", receipt.Status)
			}
			original, committed = receipt, f.state(t)
		} else {
			original.Status = mutation.StatusReplayed
			if !reflect.DeepEqual(original, receipt) || !reflect.DeepEqual(committed, f.state(t)) {
				t.Fatal("existing REST replay changed receipt or authority")
			}
		}
	}
}
