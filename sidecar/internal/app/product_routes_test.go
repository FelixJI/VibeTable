package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

func TestProductRPCRouteEnforcesMediaTypeAndBodyBudgets(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		contentType   string
		body          string
		wantStatus    int
		wantCalls     int
		wantErrorCode int
	}{
		{"valid", "application/json; charset=utf-8", `{}`, http.StatusOK, 1, 0},
		{"wrong content type", "text/plain", `{}`, http.StatusUnsupportedMediaType, 0, productrpc.CodeInvalidRequest},
		{"empty", "application/json", ``, http.StatusBadRequest, 0, productrpc.CodeInvalidRequest},
		{"oversized", "application/json", strings.Repeat("x", maxProductRPCRequestBytes+1), http.StatusBadRequest, 0, productrpc.CodeInvalidRequest},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fake := &fakeProductRPC{}
			response := serveProductRequest(
				t,
				fake,
				http.MethodPost,
				productRPCPath,
				testCase.contentType,
				testCase.body,
				context.Background(),
			)
			if response.Code != testCase.wantStatus || fake.calls != testCase.wantCalls {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, fake.calls, response.Body)
			}
			if testCase.wantErrorCode != 0 {
				var envelope productrpc.ResponseEnvelope
				if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Error == nil || envelope.Error.Code != testCase.wantErrorCode {
					t.Fatalf("error envelope = %#v", envelope)
				}
			}
		})
	}
}

// The transport budgets the encoded envelope; owners validate decoded params.
func TestProductRPCRouteAllowsOneMiBSemanticParamsInsideEnvelope(t *testing.T) {
	for _, sample := range []struct {
		name, character   string
		bytesPerCharacter int
	}{
		{"ascii", "x", 1}, {"escaped unicode", `\u00e9`, 2},
	} {
		t.Run(sample.name, func(t *testing.T) {
			available := (1 << 20) - len(`{"collection":""}`)
			params := `{"collection":"` + strings.Repeat(sample.character, available/sample.bytesPerCharacter) + strings.Repeat("x", available%sample.bytesPerCharacter) + `"}`
			body := `{"jsonrpc":"2.0","id":"request-1","method":"test.read","wire":{},"params":` + params + `}`
			if len(body) <= 1<<20 || len(body) > 4<<20 {
				t.Fatalf("fixture envelope size=%d", len(body))
			}
			fake := &fakeProductRPC{}
			response := serveProductRequest(t, fake, http.MethodPost, productRPCPath, "application/json", body, context.Background())
			if response.Code != http.StatusOK || fake.calls != 1 {
				t.Fatalf("legal envelope status=%d calls=%d", response.Code, fake.calls)
			}
		})
	}
}

func TestProductRPCRouteEnforcesFourMiBTransportCeiling(t *testing.T) {
	for _, size := range []int{4 << 20, (4 << 20) + 1} {
		fake := &fakeProductRPC{}
		response := serveProductRequest(t, fake, http.MethodPost, productRPCPath, "application/json", strings.Repeat(" ", size), context.Background())
		wantStatus, wantCalls := http.StatusOK, 1
		if size > 4<<20 {
			wantStatus, wantCalls = http.StatusBadRequest, 0
		}
		if response.Code != wantStatus || fake.calls != wantCalls {
			t.Fatalf("bytes=%d status=%d calls=%d", size, response.Code, fake.calls)
		}
	}
}

func TestProductRPCRouteFailsClosedBeforeWritingOversizedResponse(t *testing.T) {
	fake := &fakeProductRPC{response: productrpc.ResponseEnvelope{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"request-1"`),
		Wire:    json.RawMessage(`{}`),
		Result:  json.RawMessage(`"` + strings.Repeat("x", maxProductRPCResponseBytes) + `"`),
	}}
	response := serveProductRequest(
		t, fake, http.MethodPost, productRPCPath, "application/json", `{}`,
		context.Background(),
	)
	if response.Code != http.StatusOK || response.Body.Len() > maxProductRPCResponseBytes {
		t.Fatalf("status=%d bytes=%d", response.Code, response.Body.Len())
	}
	var envelope productrpc.ResponseEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil || envelope.Error.Code != productrpc.CodeInternalError ||
		envelope.Error.Message != "Internal error" || len(envelope.Result) != 0 ||
		string(envelope.ID) != `"request-1"` || string(envelope.Wire) != `{}` {
		t.Fatalf("oversized response envelope = %#v", envelope)
	}
}

func TestProductRPCRouteDropsInvalidIdentityFromOversizeFallback(t *testing.T) {
	for _, testCase := range []struct {
		name string
		id   json.RawMessage
		wire json.RawMessage
	}{
		{"object id and string wire", json.RawMessage(`{"idMarker":"id-object-must-not-cross"}`), json.RawMessage(`"wire-string-must-not-cross"`)},
		{"number id and array wire", json.RawMessage(`7`), json.RawMessage(`[]`)},
		{"null id and number wire", json.RawMessage(`null`), json.RawMessage(`7`)},
		{"empty string id and null wire", json.RawMessage(`""`), json.RawMessage(`null`)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fake := &fakeProductRPC{response: productrpc.ResponseEnvelope{
				JSONRPC: "2.0",
				ID:      testCase.id,
				Wire:    testCase.wire,
				Result: json.RawMessage(
					`"` + strings.Repeat("x", maxProductRPCResponseBytes) + `"`,
				),
			}}
			response := serveProductRequest(
				t, fake, http.MethodPost, productRPCPath, "application/json", `{}`,
				context.Background(),
			)
			if response.Code != http.StatusOK || response.Body.Len() > maxProductRPCResponseBytes {
				t.Fatalf("status=%d bytes=%d", response.Code, response.Body.Len())
			}
			var envelope productrpc.ResponseEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error == nil || envelope.Error.Code != productrpc.CodeInternalError ||
				envelope.Error.Message != "Internal error" || len(envelope.Result) != 0 ||
				string(envelope.ID) != `null` || string(envelope.Wire) != `null` {
				t.Fatalf("invalid identity fallback envelope = %#v", envelope)
			}
			if strings.Contains(response.Body.String(), "must-not-cross") {
				t.Fatalf("invalid identity marker crossed fallback response: %s", response.Body)
			}
		})
	}
}

func TestProductRPCRouteMapsOnlyInvalidRequestToHTTPBadRequest(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		code       int
		wantStatus int
	}{
		{"invalid request", productrpc.CodeInvalidRequest, http.StatusBadRequest},
		{"method not found", productrpc.CodeMethodNotFound, http.StatusOK},
		{"invalid params", productrpc.CodeInvalidParams, http.StatusOK},
		{"internal error", productrpc.CodeInternalError, http.StatusOK},
		{"public Product error", productrpc.CodeProductData, http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fake := &fakeProductRPC{response: productrpc.ResponseEnvelope{
				JSONRPC: "2.0",
				ID:      json.RawMessage(`"request-1"`),
				Wire:    json.RawMessage(`{}`),
				Error: &productrpc.ErrorObject{
					Code:    testCase.code,
					Message: "stable message",
				},
			}}
			response := serveProductRequest(
				t, fake, http.MethodPost, productRPCPath, "application/json", `{}`,
				context.Background(),
			)
			if response.Code != testCase.wantStatus {
				t.Fatalf("status=%d, want %d; body=%s", response.Code, testCase.wantStatus, response.Body)
			}
		})
	}
}

func TestProductRoutesPassRequestContextAndExposeClosedCapabilities(t *testing.T) {
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &fakeProductRPC{
		capabilities: productrpc.CapabilityDocument{
			ContractVersion: "2.0",
			WorkspaceID:     "11111111-1111-4111-8111-111111111111",
			SessionEpoch:    7,
			FenceEpoch:      3,
			ClaimID:         "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			RPCMethods:      []string{},
			Registrations:   []productrpc.Method{},
		},
	}
	response := serveProductRequest(
		t, fake, http.MethodPost, productRPCPath, "application/json", `{}`,
		requestContext,
	)
	if response.Code != http.StatusOK || !fake.sawCancellation {
		t.Fatalf("status=%d sawCancellation=%v", response.Code, fake.sawCancellation)
	}

	response = serveProductRequest(
		t, fake, http.MethodGet, productCapabilitiesPath, "", "",
		context.Background(),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("capabilities status=%d body=%s", response.Code, response.Body)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 7 || string(fields["rpcMethods"]) != `[]` ||
		string(fields["registrations"]) != `[]` {
		t.Fatalf("capability fields = %#v", fields)
	}
}

type fakeProductRPC struct {
	response        productrpc.ResponseEnvelope
	capabilities    productrpc.CapabilityDocument
	calls           int
	sawCancellation bool
}

func (fake *fakeProductRPC) Dispatch(
	ctx context.Context,
	_ []byte,
) productrpc.ResponseEnvelope {
	fake.calls++
	fake.sawCancellation = ctx.Err() != nil
	if fake.response.JSONRPC != "" {
		return fake.response
	}
	return productrpc.ResponseEnvelope{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"request-1"`),
		Wire:    json.RawMessage(`{}`),
		Result:  json.RawMessage(`{}`),
	}
}

func (fake *fakeProductRPC) Capabilities() productrpc.CapabilityDocument {
	return fake.capabilities
}

func serveProductRequest(
	t *testing.T,
	dispatcher productRPCDispatcher,
	method string,
	path string,
	contentType string,
	body string,
	ctx context.Context,
) *httptest.ResponseRecorder {
	t.Helper()
	r := router.NewRouter(func(
		writer http.ResponseWriter,
		request *http.Request,
	) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{
			Response: writer,
			Request:  request,
		}}, nil
	})
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}
