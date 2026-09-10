package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

const surfaceHTTPDefinition = `{"contractVersion":"1.0","interfaceId":"interface-orders","name":"Orders","bindings":[],"actions":[],"pages":[{"pageId":"main","title":"Orders","elements":[]}]}`

func surfaceHTTPServer(t *testing.T, pb *pocketbase.PocketBase, gates ...businessWriteGate) *httptest.Server {
	t.Helper()
	service := metadata.NewSurface(pb)
	registrations := []productrpc.Registration{surfaceListRegistration(service), surfaceLoadRegistration(service), surfaceCommitRegistration(service, gates...), surfaceDeleteRegistration(service, gates...)}
	// Independent unrelated capabilities: a call to any of them is a test failure.
	for _, method := range []string{"contentProfile.commit", "contentProfile.delete", "contentProfile.load", "events.reconcile", "field.settings.describe", "file.list", "history.applyRestore", "history.previewRestore", "history.read", "lookup.list", "lookup.query", "lookup.valuePage", "mutation.apply", "mutation.preview", "query.cursorFetch", "query.cursorOpen", "query.page", "query.readRows", "query.selectionOpen", "query.validateSnapshot", "query.view", "recordDocumentLink.commit", "recordDocumentLink.delete", "recordDocumentLink.list", "recordDocumentLink.repair", "relation.inspectPair", "relation.previewDelta", "relation.searchTargets", "schema.describe", "schema.getTable", "schema.list"} {
		registrations = append(registrations, productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope, ValidateParams: func(json.RawMessage) error { return nil }, Handler: func(context.Context, json.RawMessage) (any, error) {
			t.Errorf("unrelated method %s ran", method)
			return nil, errors.New("unrelated")
		}})
	}
	dispatcher, err := productrpc.New(productrpc.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, registrations...)
	if err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: pb, Event: router.Event{Response: w, Request: request}}, nil
	})
	registerProductRoutes(r, dispatcher)
	registerMetadataRoutes(r, metadata.New(pb))
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}
func surfaceHTTPRequest(t *testing.T, server *httptest.Server, method, params string) productrpc.ResponseEnvelope {
	t.Helper()
	raw := `{"jsonrpc":"2.0","id":"surface-http","method":"` + method + `","wire":` + schemaListWire + `,"params":` + params + `}`
	response, err := server.Client().Post(server.URL+productRPCPath, "application/json", bytes.NewBufferString(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status %d", response.StatusCode)
	}
	var envelope productrpc.ResponseEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}
func surfaceHTTPRevision(t *testing.T, response productrpc.ResponseEnvelope) string {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("unexpected Product failure %#v", response.Error)
	}
	var result struct {
		Revision string `json:"revision"`
	}
	if json.Unmarshal(response.Result, &result) != nil || result.Revision == "" {
		t.Fatalf("missing revision %s", response.Result)
	}
	return result.Revision
}
func TestSurfaceProductHTTPAtomicCASReplayAndGenericWriteClosure(t *testing.T) {
	pb := schemaProductStore(t)
	var gateKinds []string
	gate := businessWriteGate(func(ctx context.Context, kind, key string, apply func(context.Context) error) error {
		gateKinds = append(gateKinds, kind)
		return apply(ctx)
	})
	server := surfaceHTTPServer(t, pb, gate)
	create := `{"definition":` + surfaceHTTPDefinition + `,"expectedRevision":null,"idempotencyKey":"create"}`
	first := surfaceHTTPRequest(t, server, "interface.commit", create)
	revision := surfaceHTTPRevision(t, first)
	if len(gateKinds) != 1 || gateKinds[0] != "metadata.interfaces.upsert" {
		t.Fatalf("coordinator identity %v", gateKinds)
	}
	replay := surfaceHTTPRequest(t, server, "interface.commit", create)
	if !bytes.Equal(first.Result, replay.Result) || replay.Error != nil {
		t.Fatalf("commit replay %s %#v", replay.Result, replay.Error)
	}
	stale := surfaceHTTPRequest(t, server, "interface.commit", `{"definition":`+surfaceHTTPDefinition+`,"expectedRevision":"stale","idempotencyKey":"stale"}`)
	if stale.Error == nil || stale.Error.Code != -32170 || stale.Error.Data["code"] != "surface.edit_conflict" || stale.Error.Data["path"] != "expectedRevision" {
		t.Fatalf("stale CAS %#v", stale.Error)
	}
	for _, operation := range []string{"upsert", "delete"} {
		response, err := server.Client().Post(server.URL+"/api/vibetable/v1/metadata/interfaces/"+operation, "application/json", bytes.NewBufferString(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("generic %s status %d", operation, response.StatusCode)
		}
	}
	response, err := server.Client().Get(server.URL + "/api/vibetable/v1/metadata/interfaces")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !bytes.Contains(raw, []byte("interface-orders")) {
		t.Fatalf("snapshot/internal read lost %s", raw)
	}
	deletion := `{"interfaceId":"interface-orders","expectedRevision":"` + revision + `","idempotencyKey":"delete"}`
	deleted := surfaceHTTPRequest(t, server, "interface.delete", deletion)
	if deleted.Error != nil || string(deleted.Result) != `{"interfaceId":"interface-orders"}` {
		t.Fatalf("delete %#v", deleted)
	}
	server.Close()
	if err := pb.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}
	if err := pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	server = surfaceHTTPServer(t, pb, gate)
	for _, item := range []struct {
		method, params string
		want           json.RawMessage
	}{{"interface.commit", create, first.Result}, {"interface.delete", deletion, deleted.Result}} {
		result := surfaceHTTPRequest(t, server, item.method, item.params)
		if result.Error != nil || !bytes.Equal(result.Result, item.want) {
			t.Fatalf("restart replay = %#v %s", result.Error, result.Result)
		}
	}
	missing := surfaceHTTPRequest(t, server, "interface.load", `{"interfaceId":"interface-orders"}`)
	if missing.Error == nil || missing.Error.Data["code"] != "surface.not_found" {
		t.Fatalf("deleted load %#v", missing)
	}
	traces, err := pb.FindRecordsByFilter("vibetable_audit_events", "", "", 0, 0)
	if err != nil || len(traces) != 2 {
		t.Fatalf("audit replay side effect: %d %v", len(traces), err)
	}
}
func TestSurfaceProductHTTPConcurrentCASHasOneWinner(t *testing.T) {
	pb := schemaProductStore(t)
	server := surfaceHTTPServer(t, pb)
	first := surfaceHTTPRequest(t, server, "interface.commit", `{"definition":`+surfaceHTTPDefinition+`,"expectedRevision":null,"idempotencyKey":"first"}`)
	revision := surfaceHTTPRevision(t, first)
	results := make(chan productrpc.ResponseEnvelope, 2)
	var group sync.WaitGroup
	for _, key := range []string{"a", "b"} {
		group.Go(func() {
			definition := bytes.ReplaceAll([]byte(surfaceHTTPDefinition), []byte(`"name":"Orders"`), []byte(`"name":"`+key+`"`))
			results <- surfaceHTTPRequest(t, server, "interface.commit", `{"definition":`+string(definition)+`,"expectedRevision":"`+revision+`","idempotencyKey":"`+key+`"}`)
		})
	}
	group.Wait()
	close(results)
	success, conflicts := 0, 0
	for result := range results {
		if result.Error == nil {
			success++
		} else if result.Error.Data["code"] == "surface.edit_conflict" {
			conflicts++
		} else {
			t.Fatalf("unexpected error %#v", result.Error)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("CAS success=%d conflict=%d", success, conflicts)
	}
}
func TestSurfaceProductHTTPRollsBackAggregateWhenAuditFails(t *testing.T) {
	pb := schemaProductStore(t)
	pb.OnRecordCreate("vibetable_audit_events").BindFunc(func(*core.RecordEvent) error { return errors.New("injected audit failure") })
	server := surfaceHTTPServer(t, pb)
	result := surfaceHTTPRequest(t, server, "interface.commit", `{"definition":`+surfaceHTTPDefinition+`,"expectedRevision":null,"idempotencyKey":"rollback"}`)
	if result.Error == nil || result.Error.Data["code"] != "surface.persistence_failed" {
		t.Fatalf("audit failure %#v", result.Error)
	}
	for _, collection := range []string{"vibetable_interfaces", "vibetable_idempotency_keys", "vibetable_audit_events", "vibetable_outbox"} {
		records, err := pb.FindRecordsByFilter(collection, "", "", 0, 0)
		if err != nil || len(records) != 0 {
			t.Fatalf("transaction leaked %s: %d %v", collection, len(records), err)
		}
	}
}
