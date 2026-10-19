package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/metadata"
)

func TestMetadataMutationUsesIdempotentBusinessWriteGate(t *testing.T) {
	t.Parallel()
	called := false
	gate := businessWriteGate(func(
		ctx context.Context,
		kind string,
		identity string,
		apply func(context.Context) error,
	) error {
		called = true
		if kind != "metadata.presets.upsert" || identity != "interface-save-1" {
			t.Fatalf("unexpected gate identity: %s %s", kind, identity)
		}
		return apply(ctx)
	})
	request := metadata.UpsertRequest{
		Namespace:      metadata.NamespacePresets,
		LogicalID:      "interface-1",
		IdempotencyKey: "interface-save-1",
	}

	_, err := metadataMutationWithGate(
		context.Background(), request, []businessWriteGate{nil, gate},
		func(_ context.Context, got metadata.UpsertRequest) (metadata.MutationReceipt, error) {
			if got.LogicalID != request.LogicalID {
				t.Fatalf("logical id = %q, want %q", got.LogicalID, request.LogicalID)
			}
			return metadata.MutationReceipt{
				ReceiptTrace: metadata.ReceiptTrace{Status: metadata.StatusApplied},
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("metadataMutationWithGate() error = %v", err)
	}
	if !called {
		t.Fatal("idempotent business gate was not called")
	}
}

func TestDecodeMetadataBodyIsStrictAndBounded(t *testing.T) {
	var body metadataUpsertBody
	if err := decodeMetadataBody(strings.NewReader(
		`{"logicalId":"layout","payload":{"a":1},`+
			`"expectedRevision":"","idempotencyKey":"create-layout"}`,
	), &body); err != nil {
		t.Fatal(err)
	}
	if body.LogicalID != "layout" ||
		string(body.Payload) != `{"a":1}` {
		t.Fatalf("body = %#v", body)
	}
	for name, raw := range map[string]string{
		"empty": "",
		"unknown": `{"logicalId":"x","payload":{},` +
			`"expectedRevision":"","idempotencyKey":"x","collection":"_superusers"}`,
		"trailing": `{"logicalId":"x","payload":{},` +
			`"expectedRevision":"","idempotencyKey":"x"} {}`,
		"oversized": `"` + strings.Repeat(
			"x", maxMetadataRequestBytes,
		) + `"`,
	} {
		t.Run(name, func(t *testing.T) {
			var target metadataUpsertBody
			err := decodeMetadataBody(
				strings.NewReader(raw), &target,
			)
			var productErr *metadata.Error
			if !errors.As(err, &productErr) ||
				productErr.Code != "metadata.request.invalid" {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestMetadataHTTPStatusUsesSafeProductProjection(t *testing.T) {
	for code, want := range map[string]int{
		"metadata.namespace.invalid":    http.StatusBadRequest,
		"metadata.dashboard.invalid":    http.StatusBadRequest,
		"metadata.not_found":            http.StatusNotFound,
		"metadata.revision_conflict":    http.StatusConflict,
		"metadata.idempotency_conflict": http.StatusConflict,
		"metadata.storage.failed":       http.StatusInternalServerError,
		"metadata.unclassified.product": http.StatusUnprocessableEntity,
	} {
		if got := metadataHTTPStatus(&metadata.Error{
			Code: code,
		}); got != want {
			t.Fatalf("%s status = %d, want %d", code, got, want)
		}
	}
}

// Old direct metadata routes are stable tombstones even for malformed payloads.
// Refusal must happen before admission and leave original rows and trace stores alone.
func TestRetiredLayoutMetadataRoutesRejectWithoutMutation(t *testing.T) {
	f := newLegacyLayoutFixture(t)
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: f.pb, Event: router.Event{Response: w, Request: request}}, nil
	})
	registerMetadataRoutes(r, metadata.New(f.pb), func(context.Context, string, string, func(context.Context) error) error {
		t.Fatal("retired mutation entered write gate")
		return nil
	})
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	state := func() map[string]map[string]string {
		result := map[string]map[string]string{}
		for _, collection := range []string{"vibetable_interfaces", "vibetable_dashboards", "vibetable_panels", "vibetable_audit_events", "vibetable_idempotency_keys"} {
			result[collection] = previewAuthorityState(t, f.pb, collection)
		}
		return result
	}
	before := state()
	for _, path := range []string{"interfaces/upsert", "interfaces/delete", "dashboards/upsert", "dashboards/delete", "panels/upsert", "panels/delete", "dashboards/commit"} {
		for _, body := range []string{`{}`, `{"logicalId":"dash-1","payload":{"name":"overwrite"},"idempotencyKey":"reuse"}`, `not-json`} {
			response, err := server.Client().Post(server.URL+"/api/vibetable/v1/metadata/"+path, "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			decodeErr := json.NewDecoder(response.Body).Decode(&result)
			response.Body.Close()
			if decodeErr != nil || response.StatusCode != http.StatusForbidden || result["code"] != "layout.retired" {
				t.Fatalf("%s: status=%d result=%v error=%v", path, response.StatusCode, result, decodeErr)
			}
		}
	}
	if !reflect.DeepEqual(before, state()) {
		t.Fatal("retired requests changed authority or audit")
	}
}
