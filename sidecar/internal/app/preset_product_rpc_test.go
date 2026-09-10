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

func presetProductMux(t *testing.T, pb *pocketbase.PocketBase) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	},
		presetRegistration("preset.list", metadata.NewPreset(pb)),
		presetRegistration("preset.save", metadata.NewPreset(pb)),
		presetRegistration("preset.delete", metadata.NewPreset(pb)),
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
	} {
		_, err := store.Upsert(context.Background(), metadata.UpsertRequest{Namespace: metadata.NamespacePresets, LogicalID: fixture.id, Payload: json.RawMessage(fixture.payload), IdempotencyKey: fmt.Sprintf("seed-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
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

func TestPresetProductConcurrentCASAndIdempotentGate(t *testing.T) {
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
	ordinary := func(context.Context, string, string, func(context.Context) error) error {
		t.Fatal("used non-idempotent coordinator")
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
	_, err = presetRegistration("preset.save", service, ordinary, replayGate).Handler(context.Background(), json.RawMessage(`{"collection":"orders","name":"cancelled","view":{},"presetId":null,"expectedRevision":null,"operationId":"gate"}`))
	if !errors.Is(err, context.Canceled) || calls != 1 || !reflect.DeepEqual(before, presetCounts(t, pb)) {
		t.Fatalf("gate context was lost: %v calls=%d", err, calls)
	}
}
