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
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

func relationWriteMux(t *testing.T, fixture relationWriteFixture) http.Handler {
	t.Helper()
	registrations := []productrpc.Registration{}
	for _, descriptor := range productcapabilities.CurrentOwnerRPCDescriptors(productcapabilities.GoSidecar) {
		registration, found := fixture.registrations[descriptor.Method]
		if !found {
			method := descriptor.Method
			registration = productrpc.Registration{Method: method, Scope: descriptor.Scope, ValidateParams: func(json.RawMessage) error { t.Fatalf("unrelated validator %s", method); return nil }, Handler: func(context.Context, json.RawMessage) (any, error) {
				t.Fatalf("unrelated method %s", method)
				return nil, nil
			}}
		}
		registrations = append(registrations, registration)
	}
	dispatcher, err := productrpc.New(productrpc.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, registrations...)
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
	return mux
}

func TestRelationWriteHTTPRejectsStaleWireBeforeAuthority(t *testing.T) {
	f := newRelationWriteFixture(t, "one")
	mux := relationWriteMux(t, f)
	before := f.state(t)
	params := map[string]map[string]any{
		"relation.updateSingle": f.params,
		"relation.createTarget": {"relationId": f.params["relationId"], "label": "new", "idempotencyKey": "http-create"},
		"relation.applyDelta":   {"relationId": f.params["relationId"], "sourceItemId": "writesource0001", "expectedSchemaRevision": f.params["expectedSchemaRevision"], "adds": []any{}, "removes": []any{}, "idempotencyKey": "http-delta"},
	}
	for method, param := range params {
		raw, _ := json.Marshal(param)
		for _, wire := range []string{strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":6`, 1), strings.Replace(schemaListWire, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1)} {
			response := schemaProductRequestForMethod(t, mux, context.Background(), method, string(raw), wire)
			if response.Error == nil {
				t.Fatalf("stale %s reached authority", method)
			}
		}
	}
	if after := f.state(t); !reflect.DeepEqual(before, after) {
		t.Fatal("stale wire changed authority")
	}
	raw, _ := json.Marshal(f.params)
	response := schemaProductRequestForMethod(t, mux, context.Background(), "relation.updateSingle", string(raw), schemaListWire)
	if response.Error != nil {
		t.Fatalf("%+v", response.Error)
	}
	result := viewWireJSON(t, response.Result)
	if result["outcome"] != "committed" || result["requestId"] != "single-replay" {
		t.Fatal(result)
	}
	committed := f.state(t)
	response = schemaProductRequestForMethod(t, mux, context.Background(), "relation.updateSingle", string(raw), schemaListWire)
	if response.Error != nil {
		t.Fatalf("%+v", response.Error)
	}
	if after := f.state(t); !reflect.DeepEqual(committed, after) {
		t.Fatal("HTTP replay wrote authority")
	}
}
