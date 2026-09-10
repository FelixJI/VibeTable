package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func unrelatedPresetRegistration(t *testing.T, method string) productrpc.Registration {
	return productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope, ValidateParams: func(json.RawMessage) error { return nil }, Handler: func(context.Context, json.RawMessage) (any, error) {
		t.Helper()
		t.Fatalf("unrelated fixture invoked %s", method)
		return nil, nil
	}}
}

func presetProductMux(t *testing.T, pb *pocketbase.PocketBase, gates ...businessWriteGate) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	},
		presetRegistration("preset.list", metadata.NewPreset(pb), gates...),
		presetRegistration("preset.save", metadata.NewPreset(pb), gates...),
		presetRegistration("preset.delete", metadata.NewPreset(pb), gates...),
		mutationPreviewRegistration(unrelatedMutationProductMustNotRun{t: t}),
		mutationApplyRegistration(unrelatedMutationProductMustNotRun{t: t}),
		productrpc.ReconcileRegistration(catalog),
		queryValidateSnapshotRegistration(unrelatedQueryValidateSnapshotMustNotRun{t: t}),
		lookupListRegistration(relation.New(pb, nil, nil)),
		relationSearchTargetsRegistration(unrelatedRelationSearchMustNotRun{t: t}),
		unrelatedRelationInspectRegistration(t),
		queryReadRowsRegistration(unrelatedQueryReadRowsMustNotRun{t: t}),
		queryPageRegistration(unrelatedQueryPageMustNotRun{t: t}),
		queryCursorOpenRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		queryCursorFetchRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		schemaDescribeRegistration(pb, relation.New(pb, nil, nil)),
		schemaGetTableRegistration(pb),
		schemaListRegistration(catalog),
		queryViewRegistration(unrelatedViewMustNotRun{t: t}),
		lookupQueryRegistration(unrelatedLookupQueryMustNotRun{t: t}),
		lookupValuePageRegistration(unrelatedLookupValuePageMustNotRun{t: t}),
		relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}),
		fieldSettingsDescribeRegistration(unrelatedFieldSettingsDescribeMustNotRun{t: t}),
		productrpc.AttachmentListRegistration(pb, mustAttachmentManager(t)),
		historyReadRegistration(unrelatedHistoryReadMustNotRun{t: t}),
		querySelectionOpenRegistration(unrelatedSelectionMustNotRun{t: t}),
	)
	if err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: request}}, nil
	})
	registerFieldRoutes(r, pb, nil, nil, nil, nil)
	registerSchemaRoutes(r, catalog, nil)
	registerRelationRoutes(r, relation.New(pb, nil, nil))
	registerRealtimeRoutes(r, nil, catalog)
	registerMetadataRoutes(r, metadata.New(pb))
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

func presetHTTPCall(t *testing.T, mux http.Handler, method string, params any) productrpc.ResponseEnvelope {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return schemaProductRequestForMethod(t, mux, context.Background(), method, string(raw), schemaListWire)
}
func presetHTTPResult(t *testing.T, reply productrpc.ResponseEnvelope) map[string]any {
	t.Helper()
	if reply.Error != nil {
		t.Fatalf("unexpected Product error: %+v", reply.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(reply.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func presetCounts(t *testing.T, pb *pocketbase.PocketBase) []int {
	t.Helper()
	counts := []int{}
	for _, name := range []string{"vibetable_presets", "vibetable_audit_events", "vibetable_outbox", "vibetable_idempotency_keys"} {
		records, err := pb.FindAllRecords(name)
		if err != nil {
			t.Fatal(err)
		}
		counts = append(counts, len(records))
	}
	return counts
}
func TestPresetProductHTTPLifecycleReplayCASAndRestart(t *testing.T) {
	pb := schemaProductStore(t)
	mux := presetProductMux(t, pb)
	create := map[string]any{"collection": "orders", "name": "Gallery 中文", "view": map[string]any{"kind": "gallery", "coverField": "file", "columns": []any{map[string]any{"future": []any{1, true, nil}}}}, "presetId": nil, "expectedRevision": nil, "operationId": "operation-fixed"}
	saved := presetHTTPResult(t, presetHTTPCall(t, mux, "preset.save", create))
	id := saved["id"].(string)
	if id != "f19d099f-e4b1-584c-bb1a-f2cd33d73ab7" {
		t.Fatalf("UUIDv5 changed: %s", id)
	}
	counts := presetCounts(t, pb)
	if counts[0] != 1 || counts[1] != 1 || counts[2] != 1 || counts[3] != 1 {
		t.Fatalf("transaction missing trace/receipt: %v", counts)
	}
	replay := presetHTTPResult(t, presetHTTPCall(t, mux, "preset.save", create))
	if !reflect.DeepEqual(saved, replay) || !reflect.DeepEqual(counts, presetCounts(t, pb)) {
		t.Fatal("create replay changed receipt or wrote again")
	}
	update := map[string]any{"collection": "orders", "name": "Calendar", "view": map[string]any{"kind": "calendar", "dateField": "date"}, "presetId": id, "expectedRevision": saved["revision"], "operationId": "update"}
	updated := presetHTTPResult(t, presetHTTPCall(t, mux, "preset.save", update))
	stale := map[string]any{}
	for k, v := range update {
		stale[k] = v
	}
	stale["operationId"] = "stale"
	conflict := presetHTTPCall(t, mux, "preset.save", stale)
	if conflict.Error == nil || conflict.Error.Code != -32080 {
		t.Fatalf("CAS conflict=%+v", conflict)
	}
	if !reflect.DeepEqual(updated, presetHTTPResult(t, presetHTTPCall(t, mux, "preset.save", update))) {
		t.Fatal("update replay was blocked by current revision")
	}
	changed := map[string]any{}
	for k, v := range create {
		changed[k] = v
	}
	changed["name"] = "changed"
	if reply := presetHTTPCall(t, mux, "preset.save", changed); reply.Error == nil || reply.Error.Code != -32080 {
		t.Fatal("changed operation was accepted")
	}
	deletion := map[string]any{"presetId": id, "expectedRevision": updated["revision"], "operationId": "delete"}
	deleted := presetHTTPResult(t, presetHTTPCall(t, mux, "preset.delete", deletion))
	if deleted["deleted"] != id || deleted["status"] != "applied" {
		t.Fatalf("delete DTO=%v", deleted)
	}
	counts = presetCounts(t, pb)
	if !reflect.DeepEqual(deleted, presetHTTPResult(t, presetHTTPCall(t, mux, "preset.delete", deletion))) {
		t.Fatal("delete replay blocked by missing current")
	}
	if !reflect.DeepEqual(saved, presetHTTPResult(t, presetHTTPCall(t, mux, "preset.save", create))) {
		t.Fatal("create replay after delete changed")
	}
	if !reflect.DeepEqual(counts, presetCounts(t, pb)) {
		t.Fatal("replays wrote again")
	}
	if err := pb.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}
	if err := pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	mux = presetProductMux(t, pb)
	if !reflect.DeepEqual(deleted, presetHTTPResult(t, presetHTTPCall(t, mux, "preset.delete", deletion))) {
		t.Fatal("durable replay lost after restart")
	}
	listed := presetHTTPResult(t, presetHTTPCall(t, mux, "preset.list", map[string]any{"collection": "orders"}))
	if len(listed["presets"].([]any)) != 0 {
		t.Fatal("deleted view returned after restart")
	}
}
func TestPresetProductHTTPRollsBackAuditFailureAndClosesGenericWrites(t *testing.T) {
	pb := schemaProductStore(t)
	mux := presetProductMux(t, pb)
	pb.OnRecordCreate("vibetable_audit_events").BindFunc(func(*core.RecordEvent) error { return errors.New("injected audit failure") })
	before := presetCounts(t, pb)
	reply := presetHTTPCall(t, mux, "preset.save", map[string]any{"collection": "orders", "name": "rollback", "view": map[string]any{}, "presetId": nil, "expectedRevision": nil, "operationId": "rollback"})
	if reply.Error == nil || reply.Error.Code != -32603 {
		t.Fatalf("rollback reply=%+v", reply)
	}
	if !reflect.DeepEqual(before, presetCounts(t, pb)) {
		t.Fatal("audit failure leaked mutation/receipt/outbox")
	}
	for _, verb := range []string{"upsert", "delete"} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/vibetable/v1/metadata/presets/%s", verb), strings.NewReader(`{}`)))
		if response.Code != http.StatusForbidden {
			t.Fatalf("generic %s = %d %s", verb, response.Code, response.Body)
		}
	}
}

func TestPresetProductHTTPKeepsLegacyProjectionAndExtensionPayload(t *testing.T) {
	pb := schemaProductStore(t)
	mux := presetProductMux(t, pb)
	store := metadata.New(pb)
	for i, fixture := range []struct {
		id      string
		payload string
	}{
		{"z", `{"scope":"orders","key":"a","name":"legacy","presetScope":"role","userId":"user-1","view":{"kind":"kanban"},"extension":{"future":[1,true]},"id":"payload-id","revision":"payload-revision"}`},
		{"a", `{"scope":"orders","name":"default","presetScope":"unknown","view":null}`},
		{"other", `{"scope":"other","name":"hidden"}`},
		{"sort-a", `{"scope":"numeric-sort","key":2}`},
		{"sort-z", `{"scope":"numeric-sort","key":1}`},
		{"sort-m", `{"scope":"numeric-sort","key":""}`},
		{"sort-b", `{"scope":"numeric-sort","key":"0"}`},
	} {
		_, err := store.Upsert(context.Background(), metadata.UpsertRequest{Namespace: metadata.NamespacePresets, LogicalID: fixture.id, Payload: json.RawMessage(fixture.payload), IdempotencyKey: fmt.Sprintf("seed-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Intentional development-time narrowing: only nonempty string keys order views.
	numeric := presetHTTPResult(t, presetHTTPCall(t, mux, "preset.list", map[string]any{"collection": "numeric-sort"}))
	sortedIDs := []string{}
	for _, value := range numeric["presets"].([]any) {
		sortedIDs = append(sortedIDs, value.(map[string]any)["id"].(string))
	}
	if !reflect.DeepEqual(sortedIDs, []string{"sort-b", "sort-a", "sort-m", "sort-z"}) {
		t.Fatalf("non-string/empty key must use logical ID: %v", sortedIDs)
	}
	listed := presetHTTPResult(t, presetHTTPCall(t, mux, "preset.list", map[string]any{"collection": "orders"}))
	entries := listed["presets"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["id"] != "a" || entries[1].(map[string]any)["id"] != "z" {
		t.Fatalf("key/id stable order: %v", entries)
	}
	first := entries[0].(map[string]any)
	legacy := entries[1].(map[string]any)
	if first["scope"] != "personal" || first["view"].(map[string]any)["kind"] != "table" || legacy["scope"] != "role" || legacy["userId"] != "user-1" {
		t.Fatal("legacy projection changed")
	}
	update := map[string]any{"collection": "orders", "name": "updated", "view": map[string]any{"kind": "timeline"}, "presetId": "z", "expectedRevision": legacy["revision"], "operationId": "extension-update"}
	result := presetHTTPResult(t, presetHTTPCall(t, mux, "preset.save", update))
	if result["scope"] != "system" || result["userId"] != nil {
		t.Fatal("save projection changed")
	}
	items, err := store.List(context.Background(), metadata.NamespacePresets)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.LogicalID == "z" {
			var payload map[string]any
			if err := json.Unmarshal(item.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["extension"] == nil || payload["userId"] != "user-1" || payload["presetScope"] != "system" || payload["id"] != nil || payload["revision"] != nil {
				t.Fatalf("legacy merge changed: %v", payload)
			}
		}
	}
}

func TestPresetProductConcurrentCASAndCoordinatorCancellation(t *testing.T) {
	pb := schemaProductStore(t)
	service := metadata.NewPreset(pb)
	request, err := metadata.DecodePresetRequest("preset.save", []byte(`{"collection":"orders","name":"initial","view":{},"presetId":null,"expectedRevision":null,"operationId":"race-create"}`))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := service.Save(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	id := saved["id"].(string)
	revision := saved["revision"].(string)
	request.PresetID = &id
	request.ExpectedRevision = &revision
	start := make(chan struct{})
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		copy := request
		copy.Name = fmt.Sprintf("winner-%d", i)
		copy.OperationID = fmt.Sprintf("race-%d", i)
		go func() { <-start; _, err := service.Save(context.Background(), copy); done <- err }()
	}
	close(start)
	successes, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-done
		var conflict *metadata.PresetError
		if err == nil {
			successes++
		} else if errors.As(err, &conflict) && conflict.Code == "preset_edit_conflict" {
			conflicts++
		} else {
			t.Fatalf("unexpected CAS failure: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS winners=%d conflicts=%d", successes, conflicts)
	}
	before := presetCounts(t, pb)
	calls := 0
	background := func(context.Context, string, string, func(context.Context) error) error {
		t.Fatal("used background idempotent coordinator")
		return nil
	}
	replayGate := func(_ context.Context, kind, key string, apply func(context.Context) error) error {
		calls++
		if kind != "metadata.presets.upsert" || key != "preset:save:gate" {
			t.Fatal("wrong coordinator identity")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return apply(ctx)
	}
	_, err = presetRegistration("preset.save", service, replayGate, background).Handler(context.Background(), json.RawMessage(`{"collection":"orders","name":"cancelled","view":{},"presetId":null,"expectedRevision":null,"operationId":"gate"}`))
	if !errors.Is(err, context.Canceled) || calls != 1 || !reflect.DeepEqual(before, presetCounts(t, pb)) {
		t.Fatalf("gate context was lost: %v calls=%d", err, calls)
	}
}

func TestPresetProductHTTPPreservesLargeIntegersAcrossPersistenceAndReplay(t *testing.T) {
	pb := schemaProductStore(t)
	mux := presetProductMux(t, pb)
	seed, err := metadata.New(pb).Upsert(context.Background(), metadata.UpsertRequest{Namespace: metadata.NamespacePresets, LogicalID: "integer-view", Payload: json.RawMessage(`{"scope":"orders","name":"old","extension":{"counter":9007199254740993}}`), IdempotencyKey: "integer-seed"})
	if err != nil {
		t.Fatal(err)
	}
	params := json.RawMessage(fmt.Sprintf(`{"collection":"orders","name":"numbers","view":{"filters":[{"field":"count","operator":"eq","value":9007199254740993}],"columns":[{"custom":{"counter":9007199254740993}}]},"presetId":"integer-view","expectedRevision":%q,"operationId":"integer-update"}`, seed.Item.Revision))
	first := presetHTTPCall(t, mux, "preset.save", params)
	if first.Error != nil {
		t.Fatalf("save: %+v", first.Error)
	}
	assertView := func(t *testing.T, raw json.RawMessage, list bool) {
		t.Helper()
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		var value map[string]any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		if list {
			value = value["presets"].([]any)[0].(map[string]any)
		}
		view := value["view"].(map[string]any)
		filter := view["filters"].([]any)[0].(map[string]any)["value"]
		column := view["columns"].([]any)[0].(map[string]any)["custom"].(map[string]any)["counter"]
		for _, number := range []any{filter, column} {
			if number != json.Number("9007199254740993") {
				t.Errorf("large integer changed: %v (%T)", number, number)
			}
		}
	}
	t.Run("first-save", func(t *testing.T) { assertView(t, first.Result, false) })
	t.Run("list", func(t *testing.T) {
		reply := presetHTTPCall(t, mux, "preset.list", map[string]any{"collection": "orders"})
		if reply.Error != nil {
			t.Fatal(reply.Error)
		}
		assertView(t, reply.Result, true)
	})
	t.Run("preserved-extension", func(t *testing.T) {
		items, err := metadata.New(pb).List(context.Background(), metadata.NamespacePresets)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(strings.NewReader(string(items[0].Payload)))
		decoder.UseNumber()
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if number := payload["extension"].(map[string]any)["counter"]; number != json.Number("9007199254740993") {
			t.Fatalf("stored extension changed: %v", number)
		}
	})
	t.Run("replay", func(t *testing.T) {
		reply := presetHTTPCall(t, mux, "preset.save", params)
		if reply.Error != nil {
			t.Fatal(reply.Error)
		}
		assertView(t, reply.Result, false)
		if string(first.Result) != string(reply.Result) {
			t.Errorf("replay differs from original JSON: %s vs %s", first.Result, reply.Result)
		}
	})
	if err := pb.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}
	if err := pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	mux = presetProductMux(t, pb)
	t.Run("restart-replay", func(t *testing.T) {
		reply := presetHTTPCall(t, mux, "preset.save", params)
		if reply.Error != nil {
			t.Fatal(reply.Error)
		}
		assertView(t, reply.Result, false)
	})
}

func TestPresetProductRealWorkspaceGateReplaysAndRejectsChangedRequests(t *testing.T) {
	fixture := newWorkspaceMutationReplayFixture(t)
	mux := presetProductMux(t, fixture.pb, fixture.runtime.CoordinateBusinessWrite, fixture.runtime.CoordinateIdempotentBusinessWrite)
	original := map[string]any{"collection": "orders", "name": "created", "view": map[string]any{}, "presetId": nil, "expectedRevision": nil, "operationId": "workspace-preset-create"}
	first := presetHTTPCall(t, mux, "preset.save", original)
	saved := presetHTTPResult(t, first)
	assertReplay := func(t *testing.T, method string, params any, want json.RawMessage) {
		t.Helper()
		before := fixture.state(t)
		traceBefore := presetCounts(t, fixture.pb)
		reply := presetHTTPCall(t, mux, method, params)
		if reply.Error != nil || string(reply.Result) != string(want) {
			t.Errorf("real gate replay=%s error=%+v want=%s", reply.Result, reply.Error, want)
		}
		after := fixture.state(t)
		if !reflect.DeepEqual(traceBefore, presetCounts(t, fixture.pb)) {
			t.Error("replay duplicated metadata/audit/outbox/receipt")
		}
		if !reflect.DeepEqual(before, after) {
			t.Errorf("replay advanced workspace authority: before=%+v after=%+v", before, after)
		}
	}
	t.Run("create-replay", func(t *testing.T) { assertReplay(t, "preset.save", original, first.Result) })
	t.Run("changed-request", func(t *testing.T) {
		changed := map[string]any{}
		for k, v := range original {
			changed[k] = v
		}
		changed["name"] = "different"
		before := fixture.state(t)
		reply := presetHTTPCall(t, mux, "preset.save", changed)
		if reply.Error == nil || reply.Error.Code != -32080 {
			t.Errorf("changed request accepted: result=%s error=%+v", reply.Result, reply.Error)
		}
		if !reflect.DeepEqual(before, fixture.state(t)) {
			t.Error("conflict advanced workspace authority")
		}
	})
	update := map[string]any{"collection": "orders", "name": "updated", "view": map[string]any{}, "presetId": saved["id"], "expectedRevision": saved["revision"], "operationId": "workspace-preset-update"}
	updateReply := presetHTTPCall(t, mux, "preset.save", update)
	updated := presetHTTPResult(t, updateReply)
	t.Run("update-replay", func(t *testing.T) { assertReplay(t, "preset.save", update, updateReply.Result) })
	deletion := map[string]any{"presetId": updated["id"], "expectedRevision": updated["revision"], "operationId": "workspace-preset-delete"}
	deleteReply := presetHTTPCall(t, mux, "preset.delete", deletion)
	_ = presetHTTPResult(t, deleteReply)
	t.Run("delete-replay", func(t *testing.T) { assertReplay(t, "preset.delete", deletion, deleteReply.Result) })
	t.Run("old-create-after-delete", func(t *testing.T) { assertReplay(t, "preset.save", original, first.Result) })
	t.Run("stale-epoch", func(t *testing.T) {
		raw, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		before := fixture.state(t)
		reply := schemaProductRequestForMethod(t, mux, context.Background(), "preset.save", string(raw), strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":8`, 1))
		if reply.Error == nil {
			t.Error("stale epoch replay accepted")
		}
		if !reflect.DeepEqual(before, fixture.state(t)) {
			t.Error("stale epoch changed authority")
		}
	})
}
