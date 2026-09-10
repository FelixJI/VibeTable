package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

func calendarRuntimeServer(t *testing.T, f *surfaceRuntimeFixture) *httptest.Server {
	t.Helper()
	service := metadata.New(f.pb)
	registrations := []productrpc.Registration{workCalendarReadRegistration(service), workCalendarCommitRegistration(service, f.runtime.CoordinateBusinessWrite, f.runtime.CoordinateIdempotentBusinessWrite)}
	for _, method := range []string{"contentProfile.commit", "contentProfile.delete", "contentProfile.load", "recordDocumentLink.commit", "recordDocumentLink.delete", "recordDocumentLink.list", "recordDocumentLink.repair", "events.reconcile", "field.settings.describe", "file.list", "history.applyRestore", "history.previewRestore", "history.read", "insights.dashboardQueryLimits", "insights.deleteDashboardWorkspace", "insights.executeDashboardQuery", "insights.listDashboards", "insights.panelManifest", "insights.readDashboardWorkspace", "insights.saveDashboardDraft", "interface.commit", "interface.delete", "interface.list", "interface.load", "lookup.list", "lookup.query", "lookup.valuePage", "mutation.apply", "mutation.preview", "preset.delete", "preset.list", "preset.save", "query.cursorFetch", "query.cursorOpen", "query.page", "query.readRows", "query.selectionOpen", "query.validateSnapshot", "query.view", "relation.inspectPair", "relation.previewDelta", "relation.searchTargets", "schema.describe", "schema.getTable", "schema.list"} {
		registrations = append(registrations, productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope, ValidateParams: func(json.RawMessage) error { t.Fatal("unrelated validator"); return nil }, Handler: func(context.Context, json.RawMessage) (any, error) { t.Fatal("unrelated handler"); return nil, nil }})
	}
	registrations = append(registrations,
		unrelatedRelationWriteRegistration(t, "relation.createTarget"),
		unrelatedRelationWriteRegistration(t, "relation.updateSingle"),
		unrelatedRelationWriteRegistration(t, "relation.applyDelta"),
	)
	dispatcher, err := productrpc.New(productrpc.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, registrations...)
	if err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: f.pb, Event: router.Event{Response: w, Request: request}}, nil
	})
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestWorkCalendarRealRuntimeDurableReplayAndAdmission(t *testing.T) {
	f := newSurfaceRuntimeFixture(t)
	server := calendarRuntimeServer(t, f)
	state := func() surfaceRuntimeState {
		s := f.state(t)
		s.values = previewAuthorityState(t, f.pb, "vibetable_shared_settings")
		return s
	}
	request := `{"overrides":[{"date":"2026-09-10","kind":"holiday","name":"Original"}],"expectedRevision":"","idempotencyKey":"calendar-runtime-first"}`
	call := func(raw string) productrpc.ResponseEnvelope {
		return surfaceHTTPRequest(t, server, "settings.commitWorkCalendar", raw)
	}
	before := state()
	first := call(request)
	var original metadata.WorkCalendarReceipt
	if first.Error != nil || json.Unmarshal(first.Result, &original) != nil || original.Revision == "" {
		t.Fatalf("first=%s %#v", first.Result, first.Error)
	}
	committed := state()
	if committed.revision != before.revision+1 || committed.receipts != before.receipts+1 {
		t.Fatal("first commit must advance exactly once")
	}
	replay := func() {
		response := call(request)
		var result metadata.WorkCalendarReceipt
		expected := original
		expected.Status = metadata.StatusReplayed
		if response.Error != nil || json.Unmarshal(response.Result, &result) != nil || !reflect.DeepEqual(result, expected) {
			t.Errorf("lost original replay result: %s %#v; want %#v", response.Result, response.Error, expected)
		}
	}
	replay()
	conflict := call(strings.Replace(request, "Original", "Changed", 1))
	if conflict.Error == nil || conflict.Error.Data["code"] != "metadata.idempotency_conflict" {
		t.Errorf("payload reuse bypassed digest: %s %#v", conflict.Result, conflict.Error)
	}
	if !reflect.DeepEqual(committed, state()) {
		t.Error("replay/conflict advanced authority or receipt")
	}
	update := strings.Replace(request, `"expectedRevision":""`, `"expectedRevision":"`+original.Revision+`"`, 1)
	update = strings.Replace(update, "calendar-runtime-first", "calendar-runtime-next", 1)
	update = strings.Replace(update, "Original", "New", 1)
	if response := call(update); response.Error != nil {
		t.Fatalf("new write=%#v", response.Error)
	}
	advanced := state()
	if advanced.revision != committed.revision+1 || advanced.receipts != committed.receipts+1 {
		t.Fatal("new write must advance exactly once")
	}
	replay()
	if !reflect.DeepEqual(advanced, state()) {
		t.Error("replay after newer state changed authority")
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
	server = calendarRuntimeServer(t, f)
	reopened := state()
	replay()
	if !reflect.DeepEqual(reopened, state()) {
		t.Error("reopen replay advanced revision")
	}
	raw := `{"jsonrpc":"2.0","id":"calendar-stale","method":"settings.commitWorkCalendar","wire":` + strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":8`, 1) + `,"params":` + request + `}`
	response, err := server.Client().Post(server.URL+productRPCPath, "application/json", strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var invalid productrpc.ResponseEnvelope
	decodeErr := json.NewDecoder(response.Body).Decode(&invalid)
	response.Body.Close()
	if decodeErr != nil || response.StatusCode != http.StatusBadRequest || invalid.Error == nil || invalid.Error.Code != productrpc.CodeInvalidRequest {
		t.Fatalf("stale scope=%#v %v", invalid, decodeErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	registration := workCalendarCommitRegistration(metadata.New(f.pb), f.runtime.CoordinateBusinessWrite, f.runtime.CoordinateIdempotentBusinessWrite)
	if _, err := registration.Handler(ctx, json.RawMessage(request)); err == nil {
		t.Error("canceled replay accepted")
	}
	if !reflect.DeepEqual(reopened, state()) {
		t.Error("scope/cancellation changed authority")
	}
	f.close(t)
	if rejected := call(request); rejected.Error == nil {
		t.Errorf("closed gate replay accepted: %s", rejected.Result)
	}
	if !reflect.DeepEqual(reopened, state()) {
		t.Error("closed gate changed authority")
	}
}
