package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
)

func TestContentProductHTTPUsesRealSchemaAndPreservesScopeErrorsAndReplay(t *testing.T) {
	pb := schemaProductStore(t)
	mux := schemaProductMux(t, pb)
	ctx := context.Background()
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "内容", OperationID: "content-table", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	title := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "标题", "content-title")
	body := createSchemaProductField(t, pb, table.TableID, v2.LogicalEditor, "正文", "content-body")
	profile := map[string]any{"contractVersion": "1.0", "tableId": table.TableID, "titleFieldId": title.FieldID, "bodyFieldId": body.FieldID, "summaryFieldId": nil, "searchableFieldIds": []string{title.FieldID, body.FieldID}}
	call := func(method string, params any, wire string) productrpc.ResponseEnvelope {
		raw, _ := json.Marshal(params)
		return schemaProductRequestForMethod(t, mux, ctx, method, string(raw), wire)
	}
	profile["summaryFieldId"] = ""
	emptyPath := call("contentProfile.commit", map[string]any{"profile": profile, "expectedRevision": nil, "idempotencyKey": "invalid-summary"}, schemaListWire)
	if emptyPath.Error == nil || emptyPath.Error.Code != productrpc.CodeContentModel || emptyPath.Error.Data["code"] != "content_profile.field_missing" || emptyPath.Error.Data["path"] != "" || len(emptyPath.Error.Data) != 4 {
		t.Fatalf("empty field path=%+v", emptyPath.Error)
	}
	profile["summaryFieldId"] = nil
	commit := map[string]any{"profile": profile, "expectedRevision": nil, "idempotencyKey": "profile-create"}
	saved := call("contentProfile.commit", commit, schemaListWire)
	if saved.Error != nil {
		t.Fatalf("commit=%+v", saved.Error)
	}
	loaded := call("contentProfile.load", map[string]any{"tableId": table.TableID}, schemaListWire)
	if string(saved.Result) != string(loaded.Result) {
		t.Fatalf("load=%s saved=%s", loaded.Result, saved.Result)
	}
	replay := call("contentProfile.commit", commit, schemaListWire)
	if replay.Error != nil || string(saved.Result) != string(replay.Result) {
		t.Fatalf("replay=%s %+v", replay.Result, replay.Error)
	}
	missing := call("contentProfile.load", map[string]any{"tableId": "missing"}, schemaListWire)
	if missing.Error == nil || missing.Error.Code != productrpc.CodeContentModel || missing.Error.Data["code"] != "content_profile.not_found" || len(missing.Error.Data) != 3 {
		t.Fatalf("content error=%+v", missing.Error)
	}
	foreign := strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":6`, 1)
	stale := call("contentProfile.commit", commit, foreign)
	if stale.Error == nil || stale.Error.Code != productrpc.CodeInvalidRequest {
		t.Fatalf("stale=%+v", stale)
	}
	invalid := call("recordDocumentLink.list", map[string]any{"tableId": table.TableID, "recordId": "x", "extra": true}, schemaListWire)
	if invalid.Error == nil || invalid.Error.Code != productrpc.CodeInvalidParams {
		t.Fatalf("invalid DTO=%+v", invalid)
	}
	definition, err := schemaapi.New(pb).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	if err := pb.Save(record); err != nil {
		t.Fatal(err)
	}
	link := map[string]any{"contractVersion": "1.0", "linkId": "link-1", "tableId": table.TableID, "recordId": record.Id, "documentId": "22222222-2222-4222-8222-222222222222", "role": "reference", "order": 1}
	linked := call("recordDocumentLink.commit", map[string]any{"link": link, "expectedRevision": nil, "idempotencyKey": "link-create"}, schemaListWire)
	if linked.Error != nil {
		t.Fatalf("link=%+v", linked.Error)
	}
	var snapshot struct{ Revision string }
	if err := json.Unmarshal(linked.Result, &snapshot); err != nil {
		t.Fatal(err)
	}
	repair := map[string]any{"linkId": "link-1", "documentId": "33333333-3333-4333-8333-333333333333", "expectedRevision": snapshot.Revision, "idempotencyKey": "repair"}
	fixed := call("recordDocumentLink.repair", repair, schemaListWire)
	if fixed.Error != nil {
		t.Fatalf("repair=%+v", fixed.Error)
	}
	if err := json.Unmarshal(fixed.Result, &snapshot); err != nil {
		t.Fatal(err)
	}
	listed := call("recordDocumentLink.list", map[string]any{"tableId": table.TableID, "recordId": record.Id}, schemaListWire)
	if listed.Error != nil || !strings.Contains(string(listed.Result), "33333333-3333-4333-8333-333333333333") {
		t.Fatalf("list=%s %+v", listed.Result, listed.Error)
	}
	deletion := map[string]any{"linkId": "link-1", "expectedRevision": snapshot.Revision, "idempotencyKey": "delete-link"}
	for range 2 {
		deleted := call("recordDocumentLink.delete", deletion, schemaListWire)
		if deleted.Error != nil {
			t.Fatalf("delete/replay=%+v", deleted.Error)
		}
	}
	if err := json.Unmarshal(saved.Result, &snapshot); err != nil {
		t.Fatal(err)
	}
	deletedProfile := call("contentProfile.delete", map[string]any{"tableId": table.TableID, "expectedRevision": snapshot.Revision, "idempotencyKey": "delete-profile"}, schemaListWire)
	if deletedProfile.Error != nil {
		t.Fatalf("profile delete=%+v", deletedProfile.Error)
	}
}

func TestContentProductWriteGateReceivesCallerKeyAndCancellation(t *testing.T) {
	called := false
	gate := func(ctx context.Context, kind, key string, apply func(context.Context) error) error {
		called = true
		if kind != "metadata.content_profiles.upsert" || key != "same-request" {
			t.Fatalf("gate %s %s", kind, key)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		return apply(canceled)
	}
	// A nil store proves cancellation is checked before any transaction.
	registration := contentMetadataRegistration("contentProfile.commit", metadata.NewContentService(nil, nil), nil, gate)
	raw := json.RawMessage(`{"profile":{"contractVersion":"1.0","tableId":"t","titleFieldId":"a","bodyFieldId":"b","summaryFieldId":null,"searchableFieldIds":["a"]},"expectedRevision":null,"idempotencyKey":"same-request"}`)
	_, err := registration.Handler(context.Background(), raw)
	if !called || !errors.Is(err, context.Canceled) {
		t.Fatalf("gate called=%v err=%v", called, err)
	}
}

func TestGenericContentWritesAreRetiredWhileMetadataReadsRemain(t *testing.T) {
	pb := schemaProductStore(t)
	r := router.NewRouter(func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: req}}, nil
	})
	registerMetadataRoutes(r, metadata.New(pb))
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	for _, namespace := range []string{"content_profiles", "record_document_links"} {
		for _, operation := range []string{"upsert", "delete"} {
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/vibetable/v1/metadata/"+namespace+"/"+operation, strings.NewReader(`{}`)))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "metadata.namespace.invalid") {
				t.Fatalf("generic %s/%s=%d %s", namespace, operation, response.Code, response.Body)
			}
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/vibetable/v1/metadata/"+namespace, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("read=%d", response.Code)
		}
	}
}

func unrelatedContentRegistration(t *testing.T, method string) productrpc.Registration {
	t.Helper()
	registration := contentMetadataRegistration(method, nil)
	registration.ValidateParams = func(json.RawMessage) error {
		t.Fatal("unrelated content validation must not run: " + method)
		return nil
	}
	registration.Handler = func(context.Context, json.RawMessage) (any, error) {
		t.Fatal("unrelated content handler must not run: " + method)
		return nil, nil
	}
	return registration
}
