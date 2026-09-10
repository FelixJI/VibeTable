package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type dashboardRuntimeFixture struct {
	pb       *pocketbase.PocketBase
	runtime  *workspacev2.Runtime
	ledger   *auditledger.Ledger
	metadata string
}

func newDashboardRuntimeFixture(t *testing.T) *dashboardRuntimeFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace", ".vibetable")
	for _, name := range []string{"data", "topology", "objects", "audit", "snapshots", "coordination", "quarantine", "temp"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{"contractVersion":"2.0","formatVersion":2,"workspaceId":"11111111-1111-4111-8111-111111111111","displayName":"Dashboard Replay","createdAt":"2026-07-28T08:00:00Z","storageMode":"direct","encryptionMode":"convenient","repositoryFormat":"kopia-v3","topologySchemaVersion":1,"businessSchemaVersion":1,"importedFromWorkspaceId":null,"sourceSnapshotId":null}`
	if err := os.WriteFile(filepath.Join(root, "workspace.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := &dashboardRuntimeFixture{pb: historyProductStore(t, filepath.Join(root, "data")), metadata: root}
	fixture.open(t)
	t.Cleanup(func() { fixture.close(t) })
	return fixture
}

func (f *dashboardRuntimeFixture) open(t *testing.T) {
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

func (f *dashboardRuntimeFixture) close(t *testing.T) {
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

func (f *dashboardRuntimeFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	// These are the two real production gates, not an apply-through test double.
	return dashboardHTTPServer(t, f.pb, f.runtime.CoordinateBusinessWrite, f.runtime.CoordinateIdempotentBusinessWrite)
}

type dashboardRuntimeState struct {
	values   map[string]string
	revision uint64
	receipts int
}

func (f *dashboardRuntimeFixture) state(t *testing.T) dashboardRuntimeState {
	t.Helper()
	state := dashboardRuntimeState{values: previewAuthorityState(t, f.pb, "vibetable_dashboards")}
	var err error
	state.revision, err = writecoordinator.ReadPersistentMutationRevision(context.Background(), filepath.Join(f.metadata, "coordination", "write-coordinator.db"), "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.pb.DB().NewQuery(`SELECT COUNT(*) FROM workspace_v2_mutation_receipts`).Row(&state.receipts); err != nil {
		t.Fatal(err)
	}
	for key, value := range previewAuthorityState(t, f.pb, "vibetable_panels") {
		state.values["panels:"+key] = value
	}
	return state
}

func dashboardHTTPServer(t *testing.T, pb *pocketbase.PocketBase, gates ...businessWriteGate) *httptest.Server {
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	service := metadata.NewDashboard(pb, query.NewPort(pb, source))
	registrations := []productrpc.Registration{}
	for _, method := range []string{"insights.dashboardQueryLimits", "insights.deleteDashboardWorkspace", "insights.executeDashboardQuery", "insights.listDashboards", "insights.panelManifest", "insights.readDashboardWorkspace", "insights.saveDashboardDraft"} {
		registrations = append(registrations, dashboardRegistration(method, service, gates...))
	}
	for _, method := range []string{"contentProfile.commit", "contentProfile.delete", "contentProfile.load", "recordDocumentLink.commit", "recordDocumentLink.delete", "recordDocumentLink.list", "recordDocumentLink.repair"} {
		registrations = append(registrations, unrelatedContentRegistration(t, method))
	}
	for _, method := range []string{"preset.delete", "preset.list", "preset.save"} {
		registrations = append(registrations, unrelatedPresetRegistration(t, method))
	}
	for _, method := range []string{"events.reconcile", "field.settings.describe", "file.list", "history.applyRestore", "history.previewRestore", "history.read", "interface.commit", "interface.delete", "interface.list", "interface.load", "lookup.list", "lookup.query", "lookup.valuePage", "mutation.apply", "mutation.preview", "query.cursorFetch", "query.cursorOpen", "query.page", "query.readRows", "query.selectionOpen", "query.validateSnapshot", "query.view", "relation.inspectPair", "relation.previewDelta", "relation.searchTargets", "schema.describe", "schema.getTable", "schema.list"} {
		registrations = append(registrations, productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope, ValidateParams: func(json.RawMessage) error { t.Fatal("unexpected unrelated validation"); return nil }, Handler: func(context.Context, json.RawMessage) (any, error) {
			t.Fatal("unexpected unrelated call")
			return nil, nil
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
func dashboardHTTPRequest(t *testing.T, server *httptest.Server, method string, params map[string]any, wire string, ctx context.Context) productrpc.ResponseEnvelope {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "dashboard-http", "method": method, "params": params, "wire": json.RawMessage(wire)})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+productRPCPath, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result productrpc.ResponseEnvelope
	if json.NewDecoder(response.Body).Decode(&result) != nil {
		t.Fatal("invalid Product response")
	}
	return result
}
func dashboardResult(t *testing.T, response productrpc.ResponseEnvelope) map[string]any {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("unexpected Dashboard error: %#v", response.Error)
	}
	var result map[string]any
	if json.Unmarshal(response.Result, &result) != nil || result == nil {
		t.Fatalf("invalid result %s", response.Result)
	}
	return result
}
func TestDashboardProductRealRuntimeDurableReplayAndCascade(t *testing.T) {
	f := newDashboardRuntimeFixture(t)
	server := f.server(t)
	before := f.state(t)
	key := "44444444-4444-4444-8444-444444444444"
	create := map[string]any{"name": "中文 <&>", "idempotencyKey": key, "panels": []any{map[string]any{"clientId": "local", "type": "label", "options": map[string]any{"text": "persisted"}}}, "config": map[string]any{"globalFilters": []any{map[string]any{"key": "state", "type": "enum", "defaultValue": map[string]any{"large": json.Number("9007199254740993"), "fraction": json.Number("1.0")}, "targetPanels": []any{"local"}, "fieldBindings": map[string]any{"local": "fld_title"}}}}}
	first := dashboardHTTPRequest(t, server, "insights.saveDashboardDraft", create, schemaListWire, context.Background())
	result := dashboardResult(t, first)
	workspace := result["workspace"].(map[string]any)
	id := workspace["dashboard"].(map[string]any)["id"].(string)
	after := f.state(t)
	if after.revision != before.revision+1 || after.receipts != before.receipts+1 {
		t.Fatalf("first write advances exactly once: %#v -> %#v", before, after)
	}
	repeat := func(method string, p map[string]any, want json.RawMessage) {
		t.Helper()
		response := dashboardHTTPRequest(t, server, method, p, schemaListWire, context.Background())
		if response.Error != nil || !bytes.Equal(response.Result, want) {
			t.Fatalf("replay lost original result: %s %#v", response.Result, response.Error)
		}
	}
	repeat("insights.saveDashboardDraft", create, first.Result)
	if !reflect.DeepEqual(after, f.state(t)) {
		t.Fatal("replay changed authority/trace/revision/receipt")
	}
	create["name"] = "changed"
	conflict := dashboardHTTPRequest(t, server, "insights.saveDashboardDraft", create, schemaListWire, context.Background())
	if conflict.Error == nil || conflict.Error.Data["code"] != "dashboard_idempotency_conflict" {
		t.Fatalf("payload reuse %#v", conflict.Error)
	}
	create["name"] = "中文 <&>"
	omitted := map[string]any{"dashboardId": id, "name": "omitted", "expectedRevision": workspace["revision"], "idempotencyKey": "55555555-5555-4555-8555-555555555555", "panels": []any{}}
	rejected := dashboardHTTPRequest(t, server, "insights.saveDashboardDraft", omitted, schemaListWire, context.Background())
	if rejected.Error == nil || rejected.Error.Data["code"] != "dashboard_panel_membership_invalid" {
		t.Fatalf("omitted existing panel %#v", rejected.Error)
	}
	omitted["expectedRevision"] = strings.Repeat("0", 64)
	rejected = dashboardHTTPRequest(t, server, "insights.saveDashboardDraft", omitted, schemaListWire, context.Background())
	if rejected.Error == nil || rejected.Error.Data["code"] != "dashboard_edit_conflict" {
		t.Fatalf("stale CAS %#v", rejected.Error)
	}
	for _, wire := range []string{strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":8`, 1), strings.Replace(schemaListWire, "11111111-1111-4111-8111-111111111111", "99999999-9999-4999-8999-999999999999", 1)} {
		rejected = dashboardHTTPRequest(t, server, "insights.saveDashboardDraft", create, wire, context.Background())
		if rejected.Error == nil || rejected.Error.Code != productrpc.CodeInvalidRequest {
			t.Fatalf("bad scope %#v", rejected.Error)
		}
	}
	if !reflect.DeepEqual(after, f.state(t)) {
		t.Fatal("rejections changed persisted authority")
	}
	for _, path := range []string{"dashboards/upsert", "dashboards/delete", "panels/upsert", "panels/delete", "dashboards/commit"} {
		response, err := server.Client().Post(server.URL+"/api/vibetable/v1/metadata/"+path, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("generic write %s status %d", path, response.StatusCode)
		}
	}
	deleteParams := map[string]any{"dashboardId": id}
	deleted := dashboardHTTPRequest(t, server, "insights.deleteDashboardWorkspace", deleteParams, schemaListWire, context.Background())
	dashboardResult(t, deleted)
	empty := dashboardResult(t, dashboardHTTPRequest(t, server, "insights.listDashboards", map[string]any{}, schemaListWire, context.Background()))
	if len(empty["dashboards"].([]any)) != 0 {
		t.Fatal("dashboard survived deletion")
	}
	panels, err := f.pb.FindAllRecords("vibetable_panels")
	if err != nil || len(panels) != 0 {
		t.Fatalf("cascade left panels: %d %v", len(panels), err)
	}
	deletedState := f.state(t)
	repeat("insights.saveDashboardDraft", create, first.Result)
	repeat("insights.deleteDashboardWorkspace", deleteParams, deleted.Result)
	if !reflect.DeepEqual(deletedState, f.state(t)) {
		t.Fatal("deleted replay resurrected aggregate or advanced revision")
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
	repeat("insights.saveDashboardDraft", create, first.Result)
	repeat("insights.deleteDashboardWorkspace", deleteParams, deleted.Result)
	if !reflect.DeepEqual(deletedState, f.state(t)) {
		t.Fatal("restart replay changed authority")
	}
	f.close(t)
	rejected = dashboardHTTPRequest(t, server, "insights.saveDashboardDraft", create, schemaListWire, context.Background())
	if rejected.Error == nil {
		t.Fatal("closed Runtime gate allowed write")
	}
}

func TestDashboardProductRealRuntimeCASRollbackAndCancellation(t *testing.T) {
	f := newDashboardRuntimeFixture(t)
	server := f.server(t)
	create := map[string]any{"name": "original", "idempotencyKey": "44444444-4444-4444-8444-444444444444"}
	first := dashboardResult(t, dashboardHTTPRequest(t, server, "insights.saveDashboardDraft", create, schemaListWire, context.Background()))
	workspace := first["workspace"].(map[string]any)
	id := workspace["dashboard"].(map[string]any)["id"].(string)
	before := f.state(t)
	requests := []map[string]any{}
	for _, key := range []string{"55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666"} {
		requests = append(requests, map[string]any{"dashboardId": id, "expectedRevision": workspace["revision"], "name": key, "idempotencyKey": key})
	}
	responses := make(chan productrpc.ResponseEnvelope, 2)
	for _, request := range requests {
		go func() {
			responses <- dashboardHTTPRequest(t, server, "insights.saveDashboardDraft", request, schemaListWire, context.Background())
		}()
	}
	success, conflicts := 0, 0
	for range 2 {
		response := <-responses
		if response.Error == nil {
			success++
		} else if response.Error.Data["code"] == "dashboard_edit_conflict" {
			conflicts++
		} else {
			t.Fatalf("unexpected CAS error %#v", response.Error)
		}
	}
	after := f.state(t)
	if success != 1 || conflicts != 1 || after.revision != before.revision+1 || after.receipts != before.receipts+1 {
		t.Fatalf("CAS winner=%d conflict=%d before=%#v after=%#v", success, conflicts, before, after)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	params, err := json.Marshal(map[string]any{"name": "cancelled", "idempotencyKey": "77777777-7777-4777-8777-777777777777"})
	if err != nil {
		t.Fatal(err)
	}
	registration := dashboardRegistration("insights.saveDashboardDraft", metadata.NewDashboard(f.pb, nil), f.runtime.CoordinateBusinessWrite, f.runtime.CoordinateIdempotentBusinessWrite)
	if _, err := registration.Handler(ctx, params); err == nil {
		t.Fatal("cancelled request succeeded")
	}
	if !reflect.DeepEqual(after, f.state(t)) {
		t.Fatal("cancelled request changed authority")
	}
	f.pb.OnRecordCreate("vibetable_audit_events").BindFunc(func(*core.RecordEvent) error { return fmt.Errorf("injected Dashboard audit failure") })
	failed := dashboardHTTPRequest(t, server, "insights.saveDashboardDraft", map[string]any{"name": "rollback", "idempotencyKey": "88888888-8888-4888-8888-888888888888", "panels": []any{map[string]any{"clientId": "new", "type": "label"}}}, schemaListWire, context.Background())
	if failed.Error == nil || failed.Error.Data["code"] != "dashboard_persistence_failed" {
		t.Fatalf("rollback failure %#v", failed.Error)
	}
	if !reflect.DeepEqual(after, f.state(t)) {
		t.Fatal("failed audit leaked Dashboard/Panel/receipt/outbox changes")
	}
	for _, namespace := range []string{"dashboards", "panels"} {
		response, err := server.Client().Get(server.URL + "/api/vibetable/v1/metadata/" + namespace)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("internal read %s removed", namespace)
		}
	}
}

func TestDashboardProductQueryUsesPersistedGoAuthority(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "Dashboard records", OperationID: "dashboard-query-table", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	text := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Title", "dashboard-title")
	description, err := schemaexecution.Describe(context.Background(), pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(description.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"Alpha", "中文 \"quoted\""} {
		row := core.NewRecord(collection)
		row.Set(text.Definition.Identity.PhysicalName, value)
		if text.Definition.Value.Presence.Mode == v2.PresenceCompanion {
			row.Set(text.Definition.Value.Presence.PhysicalName, true)
		}
		if err := pb.Save(row); err != nil {
			t.Fatal(err)
		}
	}
	server := dashboardHTTPServer(t, pb)
	field := text.Definition.Identity.PhysicalName
	records := dashboardHTTPRequest(t, server, "insights.executeDashboardQuery", map[string]any{"panelType": "list", "query": map[string]any{"kind": "records", "collection": table.TableID, "fields": []any{field}, "limit": 20, "sorts": []any{map[string]any{"field": field, "direction": "asc"}}}}, schemaListWire, context.Background())
	if records.Error != nil {
		t.Fatalf("records: %+v", records.Error)
	}
	var result struct {
		Rows      []map[string]any
		Truncated bool
		MaxPoints int
	}
	raw, _ := json.Marshal(records.Result)
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Truncated || result.MaxPoints != 20 || !reflect.DeepEqual(result.Rows, []map[string]any{{field: "Alpha"}, {field: "中文 \"quoted\""}}) {
		t.Fatalf("projected records: %s", raw)
	}
	aggregate := dashboardHTTPRequest(t, server, "insights.executeDashboardQuery", map[string]any{"panelType": "metric", "query": map[string]any{"kind": "aggregate", "collection": table.TableID, "measures": []any{map[string]any{"key": "total", "op": "count"}}, "limit": 100}}, schemaListWire, context.Background())
	if aggregate.Error != nil {
		t.Fatalf("aggregate: %+v", aggregate.Error)
	}
	raw, _ = json.Marshal(aggregate.Result)
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Rows) != 1 || result.Rows[0]["total"] != float64(2) {
		t.Fatalf("aggregate rows: %s %v", raw, err)
	}
}
