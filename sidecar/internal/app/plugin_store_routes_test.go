package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
)

func TestPluginPrivateHTTPMatchesWorkerShapeAndPublicOwnerUsesDurableGate(t *testing.T) {
	f := newWorkspaceMutationReplayFixture(t)
	hub := realtime.New(f.pb)
	service := pluginstore.New(f.pb, "11111111-1111-4111-8111-111111111111", func() {
		if err := hub.PublishPending(); err != nil {
			t.Error(err)
		}
	})
	r := router.NewRouter(func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: f.pb, Event: router.Event{Response: w, Request: req}}, nil
	})
	bindWorkspaceV2WriteBoundary(&core.ServeEvent{Router: r})
	registerPluginStoreRoutes(r, service, f.runtime.CoordinateBusinessWrite)
	registrations := PluginSharedStateRegistrations(service, f.runtime.CoordinateBusinessWrite)
	for _, descriptor := range productcapabilities.CurrentOwnerRPCDescriptors(productcapabilities.GoSidecar) {
		if !strings.HasPrefix(descriptor.Method, "plugin.") {
			registrations = append(registrations, productrpc.Registration{Method: descriptor.Method, Scope: descriptor.Scope, ValidateParams: func(json.RawMessage) error { t.Fatal("unrelated validation"); return nil }, Handler: func(context.Context, json.RawMessage) (any, error) { t.Fatal("unrelated handler"); return nil, nil }})
		}
	}
	dispatcher, err := productrpc.New(productrpc.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, registrations...)
	if err != nil {
		t.Fatal(err)
	}
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	call := func(body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		response := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, pluginStorePath, bytes.NewReader(raw))
		mux.ServeHTTP(response, req)
		return response
	}
	raw, err := os.ReadFile("../../../contracts/v2/plugin-catalog-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	// Read only the named object; other oracle cases intentionally have array results.
	var document map[string]json.RawMessage
	_ = json.Unmarshal(raw, &document)
	var cases []map[string]json.RawMessage
	_ = json.Unmarshal(document["cases"], &cases)
	var snapshot map[string]any
	for _, item := range cases {
		if string(item["name"]) == `"install-snapshot"` {
			_ = json.Unmarshal(item["result"], &snapshot)
		}
	}
	snapshot["projectKey"] = service.ProjectKey()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, err := hub.SubscribeRecoverable(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	response := call(map[string]any{"operation": "save_installation", "projectKey": service.ProjectKey(), "pluginId": "com.example.reader", "payload": snapshot, "expectedRevision": nil})
	if response.Code != http.StatusOK {
		t.Fatalf("save worker shape: %d %s", response.Code, response.Body)
	}
	select {
	case event := <-sub.Events:
		if event.Topic != "plugin.catalog.changed" || bytes.Contains(event.Payload, []byte("Downloads")) {
			t.Fatalf("bad live event: %s", event.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("committed plugin event was not published")
	}
	before := f.state(t)
	for _, body := range []map[string]any{
		{"operation": "list_installations", "projectKey": "local:foreign"},
		{"operation": "list_installations", "projectKey": service.ProjectKey(), "sourcePath": "C:/untrusted/plugins.db"},
		{"operation": "save_installation", "projectKey": service.ProjectKey(), "pluginId": "other.plugin", "payload": snapshot, "expectedRevision": 1},
		{"operation": "arbitrary_sql", "projectKey": service.ProjectKey()},
	} {
		if result := call(body); result.Code == http.StatusOK {
			t.Fatalf("accepted malformed body: %#v", body)
		}
	}
	if after := f.state(t); after.revision != before.revision {
		t.Fatal("rejected request advanced coordinator")
	}
	params := `{"projectKey":"` + service.ProjectKey() + `","pluginId":"com.example.reader","enabled":true}`
	result := schemaProductRequestForMethod(t, mux, context.Background(), "plugin.setEnabled", params, schemaListWire)
	if result.Error != nil {
		t.Fatalf("public enabled: %+v", result.Error)
	}
	if after := f.state(t); after.revision != before.revision+1 {
		t.Fatalf("public mutation receipt not committed: %d -> %d", before.revision, after.revision)
	}
	for _, method := range []string{"plugin.listCatalog", "plugin.listAudit", "plugin.listPendingCleanup"} {
		body := `{"projectKey":"` + service.ProjectKey() + `"}`
		if method == "plugin.listAudit" {
			body = `{"projectKey":"` + service.ProjectKey() + `","pluginId":"com.example.reader"}`
		}
		result := schemaProductRequestForMethod(t, mux, ctx, method, body, schemaListWire)
		if result.Error != nil {
			t.Fatalf("%s: %+v", method, result.Error)
		}
	}
	invalid := schemaProductRequestForMethod(t, mux, ctx, "plugin.setEnabled", `{"projectKey":"`+service.ProjectKey()+`","pluginId":"com.example.reader"}`, schemaListWire)
	if invalid.Error == nil || invalid.Error.Code != productrpc.CodeInvalidParams {
		t.Fatalf("missing enabled accepted: %+v", invalid)
	}
	stale := schemaProductRequestForMethod(t, mux, ctx, "plugin.setEnabled", params, strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":6`, 1))
	if stale.Error == nil {
		t.Fatal("stale workspace write accepted")
	}
}
