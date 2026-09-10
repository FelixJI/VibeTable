package app

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestContentProductRuntimeGateRestoresResultsAndRejectsChangedReplay(t *testing.T) {
	f := newWorkspaceMutationReplayFixture(t)
	source, err := queryschema.New(f.pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	service := metadata.NewContentService(f.pb, source)
	body := createSchemaProductField(t, f.pb, f.request.TableID, v2.LogicalEditor, "Body", "content-runtime-body")
	title := f.definition.Snapshot.Fields[0].Identity.FieldID
	collection, err := f.pb.FindCollectionByNameOrId(f.definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	if err := f.pb.Save(record); err != nil {
		t.Fatal(err)
	}
	// Supply both real gates so selecting the background shortcut reproduces production's bug.
	registrations := []productrpc.Registration{}
	for _, descriptor := range productcapabilities.CurrentOwnerRPCDescriptors(productcapabilities.GoSidecar) {
		if strings.HasPrefix(descriptor.Method, "contentProfile.") || strings.HasPrefix(descriptor.Method, "recordDocumentLink.") {
			registrations = append(registrations, contentMetadataRegistration(descriptor.Method, service, f.runtime.CoordinateBusinessWrite, f.runtime.CoordinateIdempotentBusinessWrite))
		} else {
			registrations = append(registrations, productrpc.Registration{Method: descriptor.Method, Scope: descriptor.Scope,
				ValidateParams: func(json.RawMessage) error { t.Fatal("unrelated validation"); return nil },
				Handler:        func(context.Context, json.RawMessage) (any, error) { t.Fatal("unrelated handler"); return nil, nil }})
		}
	}
	dispatcher, err := productrpc.New(productrpc.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, registrations...)
	if err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: req}}, nil
	})
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	call := func(method string, params map[string]any, wire string) productrpc.ResponseEnvelope {
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		return schemaProductRequestForMethod(t, mux, context.Background(), method, string(raw), wire)
	}
	state := func() workspaceMutationReplayState {
		observed := f.state(t)
		for _, name := range []string{"vibetable_content_profiles", "vibetable_record_document_links"} {
			records, err := f.pb.FindRecordsByFilter(name, "", "+id", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			observed.business[name] = string(previewJSON(t, records))
		}
		return observed
	}
	type savedCall struct {
		method string
		params map[string]any
		result json.RawMessage
	}
	saved := []savedCall{}
	check := func(method string, params map[string]any) string {
		original := call(method, params, schemaListWire)
		if original.Error != nil || string(original.Result) == "null" {
			t.Fatalf("%s first: %s %+v", method, original.Result, original.Error)
		}
		before := state()
		repeated := call(method, params, schemaListWire)
		if repeated.Error != nil || string(repeated.Result) != string(original.Result) {
			t.Errorf("%s replay: %s %+v; want %s", method, repeated.Result, repeated.Error, original.Result)
		}
		changed := map[string]any{}
		for key, value := range params {
			changed[key] = value
		}
		changed["expectedRevision"] = "changed-revision"
		conflict := call(method, changed, schemaListWire)
		if conflict.Error == nil || conflict.Error.Data["code"] != "content_model.idempotency_conflict" {
			t.Errorf("%s changed replay: %s %+v", method, conflict.Result, conflict.Error)
		}
		if !reflect.DeepEqual(before, state()) {
			t.Errorf("%s replay advanced revision, receipts or authority", method)
		}
		saved = append(saved, savedCall{method, params, original.Result})
		var snapshot struct{ Revision string }
		if err := json.Unmarshal(original.Result, &snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot.Revision
	}
	profile := map[string]any{"contractVersion": "1.0", "tableId": f.request.TableID, "titleFieldId": title, "bodyFieldId": body.FieldID, "summaryFieldId": nil, "searchableFieldIds": []string{title, body.FieldID}}
	profileRevision := check("contentProfile.commit", map[string]any{"profile": profile, "expectedRevision": nil, "idempotencyKey": "runtime-profile"})
	link := map[string]any{"contractVersion": "1.0", "linkId": "runtime-link", "tableId": f.request.TableID, "recordId": record.Id, "documentId": "22222222-2222-4222-8222-222222222222", "role": "reference", "order": 1}
	revision := check("recordDocumentLink.commit", map[string]any{"link": link, "expectedRevision": nil, "idempotencyKey": "runtime-link"})
	revision = check("recordDocumentLink.repair", map[string]any{"linkId": "runtime-link", "documentId": "33333333-3333-4333-8333-333333333333", "expectedRevision": revision, "idempotencyKey": "runtime-repair"})
	check("recordDocumentLink.delete", map[string]any{"linkId": "runtime-link", "expectedRevision": revision, "idempotencyKey": "runtime-delete-link"})
	check("contentProfile.delete", map[string]any{"tableId": f.request.TableID, "expectedRevision": profileRevision, "idempotencyKey": "runtime-delete-profile"})
	before := state()
	for _, item := range saved {
		result := call(item.method, item.params, schemaListWire)
		if result.Error != nil || string(result.Result) != string(item.result) {
			t.Errorf("after deletion %s replay: %s %+v", item.method, result.Result, result.Error)
		}
		stale := call(item.method, item.params, strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":6`, 1))
		if stale.Error == nil || stale.Error.Code != productrpc.CodeInvalidRequest {
			t.Errorf("%s stale scope accepted", item.method)
		}
	}
	if !reflect.DeepEqual(before, state()) {
		t.Error("post-delete replay changed authority")
	}
	if err := f.runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	before = state()
	for _, item := range saved {
		result := call(item.method, item.params, schemaListWire)
		if result.Error == nil {
			t.Errorf("retired Runtime accepted %s replay: %s", item.method, result.Result)
		}
	}
	if !reflect.DeepEqual(before, state()) {
		t.Error("retired gate changed authority")
	}
}
