package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

func TestWorkCalendarProductDispatcherUsesAuthorityAndWriteGate(t *testing.T) {
	pb := schemaProductStore(t)
	service := metadata.New(pb)
	writes := 0
	gate := businessWriteGate(func(ctx context.Context, kind, key string, apply func(context.Context) error) error {
		if kind != "settings.workCalendar.commit" || key != "calendar-1" {
			t.Fatalf("unexpected gate %s %s", kind, key)
		}
		writes++
		return apply(ctx)
	})
	registrations := []productrpc.Registration{workCalendarReadRegistration(service), workCalendarCommitRegistration(service, gate)}
	for _, descriptor := range productcapabilities.CurrentOwnerRPCDescriptors(productcapabilities.GoSidecar) {
		if descriptor.Method == "settings.readWorkCalendar" || descriptor.Method == "settings.commitWorkCalendar" {
			continue
		}
		registrations = append(registrations, productrpc.Registration{Method: descriptor.Method, Scope: descriptor.Scope, ValidateParams: func(json.RawMessage) error { t.Fatal("unrelated validator"); return nil }, Handler: func(context.Context, json.RawMessage) (any, error) { t.Fatal("unrelated handler"); return nil, nil }})
	}
	dispatcher, err := productrpc.New(productrpc.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, registrations...)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := func(method, params string) productrpc.ResponseEnvelope {
		t.Helper()
		return dispatcher.Dispatch(context.Background(), []byte(`{"jsonrpc":"2.0","id":"calendar","method":"`+method+`","wire":`+schemaListWire+`,"params":`+params+`}`))
	}
	empty := dispatch("settings.readWorkCalendar", `{}`)
	if empty.Error != nil || string(empty.Result) != `{"overrides":[],"revision":""}` {
		t.Fatalf("empty=%#v", empty)
	}
	saved := dispatch("settings.commitWorkCalendar", `{"overrides":[{"date":"2026-09-10","kind":"holiday","name":"公司假日"}],"expectedRevision":"","idempotencyKey":"calendar-1"}`)
	if saved.Error != nil || writes != 1 {
		t.Fatalf("saved=%#v, writes=%d", saved, writes)
	}
	var receipt metadata.WorkCalendarReceipt
	if err := json.Unmarshal(saved.Result, &receipt); err != nil || receipt.Revision == "" || len(receipt.Overrides) != 1 {
		t.Fatalf("receipt=%#v %v", receipt, err)
	}
	current := dispatch("settings.readWorkCalendar", `{}`)
	var value metadata.WorkCalendarResult
	if err := json.Unmarshal(current.Result, &value); err != nil || value.Revision != receipt.Revision {
		t.Fatalf("read=%#v %v", value, err)
	}
	for _, params := range []string{`null`, `{"overrides":[],"expectedRevision":null,"idempotencyKey":"x"}`, `{"overrides":[{"date":"2026-09-10","kind":"holiday"}],"expectedRevision":"","idempotencyKey":"x"}`, `{"overrides":[],"expectedRevision":"","idempotencyKey":"x","unknown":1}`} {
		invalid := dispatch("settings.commitWorkCalendar", params)
		expectedCode := productrpc.CodeInvalidParams
		if params == "null" {
			expectedCode = productrpc.CodeInvalidRequest
		}
		if invalid.Error == nil || invalid.Error.Code != expectedCode {
			t.Fatalf("params=%s invalid=%#v", params, invalid.Error)
		}
	}
	if writes != 1 {
		t.Fatal("invalid requests entered gate")
	}
}

func TestWorkCalendarPublicErrorsAreClosed(t *testing.T) {
	for _, code := range []string{"settings.calendar.invalid", "settings.calendar.revision_conflict", "settings.calendar.corrupt", "metadata.idempotency_conflict", "metadata.storage.failed", "metadata.request.invalid"} {
		raw := &metadata.Error{Code: code, Path: "expectedRevision", Message: "private database path"}
		var public *productrpc.PublicError
		if !errors.As(workCalendarPublicError(raw), &public) || public.Code != code || public.Message == raw.Message {
			t.Fatalf("invalid projection %s", code)
		}
	}
	var public *productrpc.PublicError
	if errors.As(workCalendarPublicError(&metadata.Error{Code: "untrusted.code", Message: "secret"}), &public) {
		t.Fatal("unknown domain code leaked")
	}
}

func TestWorkCalendarRejectsMalformedWireBeforeAuthority(t *testing.T) {
	invalid := []string{
		``, `{`, `[]`, `{"accessToken":"secret"}`,
		`{"overrides":[],"expectedRevision":4,"idempotencyKey":"key"}`,
		`{"overrides":[],"expectedRevision":"","idempotencyKey":false}`,
		`{"overrides":null,"expectedRevision":"","idempotencyKey":"key"}`,
		`{"overrides":[null],"expectedRevision":"","idempotencyKey":"key"}`,
		`{"overrides":[{"date":"2026-09-10","kind":"holiday","name":"\ud800"}],"expectedRevision":"","idempotencyKey":"key"}`,
	}
	for _, raw := range invalid {
		if _, err := decodeWorkCalendarCommit(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if err := workCalendarReadRegistration(nil).ValidateParams(json.RawMessage(`{"unknown":true}`)); err == nil {
		t.Fatal("read accepted extra field")
	}
	if err := workCalendarReadRegistration(nil).ValidateParams(json.RawMessage(`null`)); err == nil {
		t.Fatal("read accepted null")
	}
	if _, err := workCalendarReadRegistration(nil).Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing read dependency succeeded")
	}
	if _, err := workCalendarCommitRegistration(nil).Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("invalid handler shape succeeded")
	}
	if _, err := workCalendarCommitRegistration(nil).Handler(context.Background(), json.RawMessage(`{"overrides":[],"expectedRevision":"","idempotencyKey":"key"}`)); err == nil {
		t.Fatal("missing write dependency succeeded")
	}
	if !errors.Is(workCalendarPublicError(context.Canceled), context.Canceled) {
		t.Fatal("cancellation changed")
	}
}

func TestWorkCalendarGenericHTTPWritesAreClosedButReadsRemainAvailable(t *testing.T) {
	pb := schemaProductStore(t)
	service := metadata.New(pb)
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: request}}, nil
	})
	registerMetadataRoutes(r, service, businessWriteGate(func(context.Context, string, string, func(context.Context) error) error {
		t.Fatal("generic calendar write reached authority")
		return nil
	}))
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"upsert", "delete"} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/vibetable/v1/metadata/shared_settings/"+operation, strings.NewReader(`{}`)))
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d body=%s", operation, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/vibetable/v1/metadata/shared_settings", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatalf("metadata read=%d %s", response.Code, response.Body.String())
	}
}
