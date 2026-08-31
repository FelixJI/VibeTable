package productrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
)

const (
	testWorkspaceID = "11111111-1111-4111-8111-111111111111"
	testOperationID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	testClaimID     = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

func TestDispatchSuccessEchoesRawWireAndAllowsZeroSequence(t *testing.T) {
	wire := `{"scope":"workspace", "workspaceId":"` + testWorkspaceID +
		`","sessionEpoch":7,"operationId":"` + testOperationID + `","sequence":0}`
	dispatcher := mustTestDispatcher(t, []productcapabilities.RPCDescriptor{{
		Method: "test.read", Scope: productcapabilities.WorkspaceScope,
	}}, Registration{
		Method: "test.read", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			if string(raw) != `{"value":3}` {
				t.Fatalf("params = %s", raw)
			}
			return nil
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if err := ctx.Err(); err != nil {
				t.Fatalf("handler context = %v", err)
			}
			return map[string]any{"doubled": 6}, nil
		},
	})

	response := dispatcher.Dispatch(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":"request-1","method":"test.read","wire":`+
			wire+`,"params":{"value":3}}`,
	))

	if response.Error != nil {
		t.Fatalf("response error = %#v", response.Error)
	}
	if string(response.ID) != `"request-1"` || string(response.Wire) != wire {
		t.Fatalf("response identity id=%s wire=%s", response.ID, response.Wire)
	}
	if string(response.Result) != `{"doubled":6}` {
		t.Fatalf("response result = %s", response.Result)
	}
}

func TestDispatchPassesCanceledContextToHandlerExactlyOnce(t *testing.T) {
	calls := 0
	dispatcher := mustTestDispatcher(t, []productcapabilities.RPCDescriptor{{
		Method: "test.canceled", Scope: productcapabilities.WorkspaceScope,
	}}, Registration{
		Method: "test.canceled", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(json.RawMessage) error { return nil },
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			calls++
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("handler context error = %v, want context.Canceled", ctx.Err())
			}
			return map[string]any{"canceled": true}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	response := dispatcher.Dispatch(ctx, []byte(
		`{"jsonrpc":"2.0","id":"request-1","method":"test.canceled","wire":`+
			workspaceWire(0)+`,"params":{}}`,
	))
	if response.Error != nil || string(response.Result) != `{"canceled":true}` {
		t.Fatalf("response = %#v", response)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}

func TestDispatchGlobalScopeAllowsZeroSequenceAndRejectsWorkspaceWire(t *testing.T) {
	calls := 0
	dispatcher := mustTestDispatcher(t, []productcapabilities.RPCDescriptor{{
		Method: "test.global", Scope: productcapabilities.GlobalScope,
	}}, Registration{
		Method: "test.global", Scope: productcapabilities.GlobalScope,
		ValidateParams: func(json.RawMessage) error { return nil },
		Handler: func(context.Context, json.RawMessage) (any, error) {
			calls++
			return map[string]any{"ok": true}, nil
		},
	})
	wire := `{"scope":"global", "operationId":"` + testOperationID + `","sequence":0}`

	success := dispatcher.Dispatch(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":"request-1","method":"test.global","wire":`+
			wire+`,"params":{}}`,
	))
	if success.Error != nil || string(success.Result) != `{"ok":true}` ||
		string(success.Wire) != wire {
		t.Fatalf("global response = %#v", success)
	}

	rejected := dispatcher.Dispatch(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":"request-2","method":"test.global","wire":`+
			workspaceWire(0)+`,"params":{}}`,
	))
	assertError(t, rejected, CodeInvalidRequest, "Invalid Request")
	if calls != 1 {
		t.Fatalf("handler calls = %d, want only the valid global request", calls)
	}
}

func TestDispatchRejectsClosedEnvelopeViolationsBeforeMethodLookup(t *testing.T) {
	dispatcher := mustTestDispatcher(t, nil)
	validWire := workspaceWire(0)
	for _, testCase := range []struct {
		name     string
		raw      string
		wantID   string
		wantWire string
	}{
		{"malformed", `{`, `null`, `null`},
		{"trailing", `{"jsonrpc":"2.0"} {}`, `null`, `null`},
		{"wrong version", `{"jsonrpc":"1.0","id":"request-1","method":"missing","wire":` + validWire + `,"params":{}}`, `"request-1"`, validWire},
		{"unknown top field", `{"jsonrpc":"2.0","id":"request-1","method":"missing","wire":` + validWire + `,"params":{},"token":"nope"}`, `"request-1"`, validWire},
		{"duplicate jsonrpc", `{"jsonrpc":"1.0","jsonrpc":"2.0","id":"request-1","method":"missing","wire":` + validWire + `,"params":{}}`, `"request-1"`, validWire},
		{"duplicate id", `{"jsonrpc":"2.0","id":"first","id":"request-1","method":"missing","wire":` + validWire + `,"params":{}}`, `null`, validWire},
		{"duplicate method", `{"jsonrpc":"2.0","id":"request-1","method":"first","method":"second","wire":` + validWire + `,"params":{}}`, `"request-1"`, validWire},
		{"duplicate wire", `{"jsonrpc":"2.0","id":"request-1","method":"missing","wire":{},"wire":` + validWire + `,"params":{}}`, `"request-1"`, `null`},
		{"duplicate params", `{"jsonrpc":"2.0","id":"request-1","method":"missing","wire":` + validWire + `,"params":{},"params":{"value":1}}`, `"request-1"`, validWire},
		{"numeric id", `{"jsonrpc":"2.0","id":1,"method":"missing","wire":` + validWire + `,"params":{}}`, `null`, validWire},
		{"string wire", `{"jsonrpc":"2.0","id":"request-1","method":"missing","wire":"workspace","params":{}}`, `"request-1"`, `null`},
		{"numeric wire", `{"jsonrpc":"2.0","id":"request-1","method":"missing","wire":7,"params":{}}`, `"request-1"`, `null`},
		{"boolean wire", `{"jsonrpc":"2.0","id":"request-1","method":"missing","wire":true,"params":{}}`, `"request-1"`, `null`},
		{"array wire", `{"jsonrpc":"2.0","id":"request-1","method":"missing","wire":[],"params":{}}`, `"request-1"`, `null`},
		{"null wire", `{"jsonrpc":"2.0","id":"request-1","method":"missing","wire":null,"params":{}}`, `"request-1"`, `null`},
		{"invalid wire JSON", `{"jsonrpc":"2.0","id":"request-1","method":"missing","wire":invalid,"params":{}}`, `null`, `null`},
		{"array params", `{"jsonrpc":"2.0","id":"request-1","method":"missing","wire":` + validWire + `,"params":[]}`, `"request-1"`, validWire},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := dispatcher.Dispatch(context.Background(), []byte(testCase.raw))
			if response.Error == nil ||
				response.Error.Code != CodeInvalidRequest ||
				response.Error.Message != "Invalid Request" ||
				response.Error.Data != nil || len(response.Result) != 0 {
				t.Fatalf("response = %#v", response)
			}
			if string(response.ID) != testCase.wantID ||
				string(response.Wire) != testCase.wantWire {
				t.Fatalf("identity id=%s wire=%s", response.ID, response.Wire)
			}
		})
	}
}

func TestDispatchMapsOnlyExplicitPublicErrorsAndRedactsDetails(t *testing.T) {
	path := "fields[2].constraints.scale"
	dispatcher := mustTestDispatcher(t, []productcapabilities.RPCDescriptor{{
		Method: "test.publicError", Scope: productcapabilities.WorkspaceScope,
	}}, Registration{
		Method: "test.publicError", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(json.RawMessage) error {
			return nil
		},
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return nil, fmt.Errorf("wrapped: %w", &PublicError{
				Code:      "schema.field.invalid_constraint",
				Path:      &path,
				Message:   "scale 不能大于 precision",
				Retryable: false,
				Details: map[string]any{
					"precision": 8,
					"nested": []any{map[string]any{
						"sessionSecret": "must-not-cross",
						"legacyToken":   "must-not-cross-either",
						"safe":          true,
					}},
				},
			})
		},
	})

	response := dispatcher.Dispatch(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":"request-1","method":"test.publicError","wire":`+
			workspaceWire(0)+`,"params":{}}`,
	))

	if response.Error == nil || response.Error.Code != CodeProductData ||
		response.Error.Message != "Product data error" {
		t.Fatalf("response error = %#v", response.Error)
	}
	wantData := map[string]any{
		"kind":      "product_data_error",
		"message":   "scale 不能大于 precision",
		"code":      "schema.field.invalid_constraint",
		"path":      path,
		"details":   map[string]any{"precision": json.Number("8"), "nested": []any{map[string]any{"safe": true}}},
		"retryable": false,
	}
	if !equalJSONValues(response.Error.Data, wantData) {
		t.Fatalf("error data = %#v, want %#v", response.Error.Data, wantData)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "must-not-cross") ||
		strings.Contains(string(encoded), "sessionSecret") ||
		strings.Contains(string(encoded), "legacyToken") {
		t.Fatalf("secret-bearing details crossed Product RPC: %s", encoded)
	}
}

func TestDispatchFailsClosedWhenPublicErrorDetailsMarshalerPanics(t *testing.T) {
	dispatcher := mustTestDispatcher(t, []productcapabilities.RPCDescriptor{{
		Method: "test.publicDetailsPanic", Scope: productcapabilities.WorkspaceScope,
	}}, Registration{
		Method: "test.publicDetailsPanic", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(json.RawMessage) error { return nil },
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return nil, &PublicError{
				Code:    "schema.field_invalid",
				Message: "public message must not cross after marshal panic",
				Details: map[string]any{
					"unsafe": panickingJSONMarshaler("details-marshal-panic-must-not-cross"),
				},
			}
		},
	})

	response := dispatcher.Dispatch(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":"request-1","method":"test.publicDetailsPanic","wire":`+
			workspaceWire(0)+`,"params":{}}`,
	))
	assertError(t, response, CodeInternalError, "Internal error")
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "must-not-cross") {
		t.Fatalf("Product error detail crossed after marshal panic: %s", encoded)
	}
}

func TestDispatchPublishesOnlyContractProductErrorCodes(t *testing.T) {
	for _, code := range []string{
		"",
		"   ",
		"field_invalid",
		" schema.field_invalid",
		"schema.Field_invalid",
		"pocketbase.internal",
	} {
		t.Run(strconv.Quote(code), func(t *testing.T) {
			dispatcher := mustTestDispatcher(t, []productcapabilities.RPCDescriptor{{
				Method: "test.invalidPublicCode", Scope: productcapabilities.WorkspaceScope,
			}}, Registration{
				Method: "test.invalidPublicCode", Scope: productcapabilities.WorkspaceScope,
				ValidateParams: func(json.RawMessage) error { return nil },
				Handler: func(context.Context, json.RawMessage) (any, error) {
					return nil, &PublicError{
						Code: code, Message: "invalid public code must not cross",
					}
				},
			})

			response := dispatcher.Dispatch(context.Background(), []byte(
				`{"jsonrpc":"2.0","id":"request-1","method":"test.invalidPublicCode","wire":`+
					workspaceWire(0)+`,"params":{}}`,
			))
			assertError(t, response, CodeInternalError, "Internal error")
		})
	}
}

func TestDispatchFailsClosedForInternalErrorsPanicsAndUnserializableResults(t *testing.T) {
	descriptors := []productcapabilities.RPCDescriptor{
		{Method: "test.error", Scope: productcapabilities.WorkspaceScope},
		{Method: "test.marshalPanic", Scope: productcapabilities.WorkspaceScope},
		{Method: "test.panic", Scope: productcapabilities.WorkspaceScope},
		{Method: "test.validatorPanic", Scope: productcapabilities.WorkspaceScope},
		{Method: "test.unserializable", Scope: productcapabilities.WorkspaceScope},
	}
	validator := func(json.RawMessage) error { return nil }
	dispatcher := mustTestDispatcher(t, descriptors,
		Registration{
			Method: "test.error", Scope: productcapabilities.WorkspaceScope,
			ValidateParams: validator,
			Handler: func(context.Context, json.RawMessage) (any, error) {
				return nil, errors.New("internal-error-must-not-cross")
			},
		},
		Registration{
			Method: "test.marshalPanic", Scope: productcapabilities.WorkspaceScope,
			ValidateParams: validator,
			Handler: func(context.Context, json.RawMessage) (any, error) {
				return panickingJSONMarshaler("result-marshal-panic-must-not-cross"), nil
			},
		},
		Registration{
			Method: "test.panic", Scope: productcapabilities.WorkspaceScope,
			ValidateParams: validator,
			Handler: func(context.Context, json.RawMessage) (any, error) {
				panic("panic-value-must-not-cross")
			},
		},
		Registration{
			Method: "test.validatorPanic", Scope: productcapabilities.WorkspaceScope,
			ValidateParams: func(json.RawMessage) error {
				panic("validator-panic-must-not-cross")
			},
			Handler: func(context.Context, json.RawMessage) (any, error) {
				t.Fatal("handler ran after validator panic")
				return nil, nil
			},
		},
		Registration{
			Method: "test.unserializable", Scope: productcapabilities.WorkspaceScope,
			ValidateParams: validator,
			Handler: func(context.Context, json.RawMessage) (any, error) {
				return make(chan int), nil
			},
		},
	)

	for _, method := range []string{
		"test.error", "test.marshalPanic", "test.panic", "test.validatorPanic",
		"test.unserializable",
	} {
		t.Run(method, func(t *testing.T) {
			response := dispatcher.Dispatch(context.Background(), []byte(
				`{"jsonrpc":"2.0","id":"request-1","method":`+
					strconv.Quote(method)+`,"wire":`+workspaceWire(0)+`,"params":{}}`,
			))
			if response.Error == nil || response.Error.Code != CodeInternalError ||
				response.Error.Message != "Internal error" || response.Error.Data != nil {
				t.Fatalf("response = %#v", response)
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "must-not-cross") {
				t.Fatalf("internal detail crossed Product RPC: %s", encoded)
			}
		})
	}
}

type panickingJSONMarshaler string

func (value panickingJSONMarshaler) MarshalJSON() ([]byte, error) {
	panic(string(value))
}

func TestDispatchUsesStableMethodParamsAndCurrentIdentityErrors(t *testing.T) {
	handlerCalled := false
	dispatcher := mustTestDispatcher(t, []productcapabilities.RPCDescriptor{{
		Method: "test.known", Scope: productcapabilities.WorkspaceScope,
	}}, Registration{
		Method: "test.known", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(json.RawMessage) error {
			return errors.New("field detail must not cross")
		},
		Handler: func(context.Context, json.RawMessage) (any, error) {
			handlerCalled = true
			return nil, nil
		},
	})

	unknown := dispatcher.Dispatch(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":"request-1","method":"test.missing","wire":`+
			workspaceWire(0)+`,"params":{}}`,
	))
	assertError(t, unknown, CodeMethodNotFound, "Method not found")

	invalidParams := dispatcher.Dispatch(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":"request-1","method":"test.known","wire":`+
			workspaceWire(0)+`,"params":{"unknown":true}}`,
	))
	assertError(t, invalidParams, CodeInvalidParams, "Invalid params")
	if handlerCalled {
		t.Fatal("handler ran after params validation failed")
	}

	staleWire := strings.Replace(workspaceWire(0), `"sessionEpoch":7`, `"sessionEpoch":6`, 1)
	stale := dispatcher.Dispatch(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":"request-1","method":"test.known","wire":`+
			staleWire+`,"params":{}}`,
	))
	assertError(t, stale, CodeInvalidRequest, "Invalid Request")

	foreignWire := strings.Replace(
		workspaceWire(0),
		testWorkspaceID,
		"22222222-2222-4222-8222-222222222222",
		1,
	)
	foreign := dispatcher.Dispatch(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":"request-1","method":"test.known","wire":`+
			foreignWire+`,"params":{}}`,
	))
	assertError(t, foreign, CodeInvalidRequest, "Invalid Request")
}

func TestNewCapturesValidatedIdentitySnapshot(t *testing.T) {
	identity := testIdentity()
	dispatcher, err := New(identity)
	if err != nil {
		t.Fatal(err)
	}
	identity.WorkspaceID = "22222222-2222-4222-8222-222222222222"
	identity.SessionEpoch = 99
	identity.FenceEpoch = 99
	identity.ClaimID = "33333333-3333-4333-8333-333333333333"
	capabilities := dispatcher.Capabilities()
	if capabilities.WorkspaceID != testWorkspaceID || capabilities.SessionEpoch != 7 ||
		capabilities.FenceEpoch != 3 || capabilities.ClaimID != testClaimID {
		t.Fatalf("capabilities identity changed after construction: %#v", capabilities)
	}

	for _, testCase := range []struct {
		name     string
		identity Identity
	}{
		{"workspace not UUID", Identity{WorkspaceID: "not-a-uuid", SessionEpoch: 7, FenceEpoch: 3, ClaimID: testClaimID}},
		{"workspace not canonical lower-D", Identity{WorkspaceID: "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", SessionEpoch: 7, FenceEpoch: 3, ClaimID: testClaimID}},
		{"nil workspace UUID", Identity{WorkspaceID: "00000000-0000-0000-0000-000000000000", SessionEpoch: 7, FenceEpoch: 3, ClaimID: testClaimID}},
		{"claim not UUID", Identity{WorkspaceID: testWorkspaceID, SessionEpoch: 7, FenceEpoch: 3, ClaimID: "not-a-uuid"}},
		{"claim not canonical lower-D", Identity{WorkspaceID: testWorkspaceID, SessionEpoch: 7, FenceEpoch: 3, ClaimID: "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"}},
		{"nil claim UUID", Identity{WorkspaceID: testWorkspaceID, SessionEpoch: 7, FenceEpoch: 3, ClaimID: "00000000-0000-0000-0000-000000000000"}},
		{"zero session epoch", Identity{WorkspaceID: testWorkspaceID, FenceEpoch: 3, ClaimID: testClaimID}},
		{"zero fence epoch", Identity{WorkspaceID: testWorkspaceID, SessionEpoch: 7, ClaimID: testClaimID}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := New(testCase.identity); err == nil {
				t.Fatal("New accepted invalid Product identity")
			}
		})
	}
}

func TestNewRequiresRegistrationsToExactlyMatchGeneratedGoSidecarPolicy(t *testing.T) {
	identity := testIdentity()
	dispatcher, err := New(identity)
	if err != nil {
		t.Fatal(err)
	}
	if methods := dispatcher.Methods(); len(methods) != 0 {
		t.Fatalf("production registrations = %#v, want empty during L2a", methods)
	}
	_, err = New(identity, Registration{
		Method: "schema.getTable", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(json.RawMessage) error { return nil },
		Handler:        func(context.Context, json.RawMessage) (any, error) { return nil, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "do not match generated goSidecar policy") {
		t.Fatalf("unexpected registry mismatch error: %v", err)
	}

	_, err = newDispatcher(identity, []productcapabilities.RPCDescriptor{{
		Method: "test.read", Scope: productcapabilities.WorkspaceScope,
	}}, []Registration{{
		Method: "test.read", Scope: productcapabilities.GlobalScope,
		ValidateParams: func(json.RawMessage) error { return nil },
		Handler:        func(context.Context, json.RawMessage) (any, error) { return nil, nil },
	}})
	if err == nil || !strings.Contains(err.Error(), "do not match generated goSidecar policy") {
		t.Fatalf("unexpected scope mismatch error: %v", err)
	}
}

func TestCapabilitiesMirrorCurrentIdentityAndActualRegistrations(t *testing.T) {
	identity := Identity{
		WorkspaceID:  testWorkspaceID,
		SessionEpoch: 7,
		FenceEpoch:   3,
		ClaimID:      "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}
	descriptors := []productcapabilities.RPCDescriptor{
		{Method: "z.read", Scope: productcapabilities.WorkspaceScope},
		{Method: "a.read", Scope: productcapabilities.GlobalScope},
	}
	validator := func(json.RawMessage) error { return nil }
	handler := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	dispatcher, err := newDispatcher(
		identity,
		descriptors,
		[]Registration{
			{Method: "z.read", Scope: productcapabilities.WorkspaceScope, ValidateParams: validator, Handler: handler},
			{Method: "a.read", Scope: productcapabilities.GlobalScope, ValidateParams: validator, Handler: handler},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	capabilities := dispatcher.Capabilities()
	if capabilities.ContractVersion != "2.0" || capabilities.WorkspaceID != testWorkspaceID ||
		capabilities.SessionEpoch != 7 || capabilities.FenceEpoch != 3 ||
		capabilities.ClaimID != identity.ClaimID {
		t.Fatalf("identity capabilities = %#v", capabilities)
	}
	if strings.Join(capabilities.RPCMethods, ",") != "a.read,z.read" ||
		len(capabilities.Registrations) != 2 ||
		capabilities.Registrations[0].Method != "a.read" ||
		capabilities.Registrations[0].Scope != productcapabilities.GlobalScope {
		t.Fatalf("registration capabilities = %#v", capabilities)
	}
	raw, err := json.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{
		"claimId", "contractVersion", "fenceEpoch", "registrations",
		"rpcMethods", "sessionEpoch", "workspaceId",
	}
	actualFields := make([]string, 0, len(fields))
	for field := range fields {
		actualFields = append(actualFields, field)
	}
	sort.Strings(actualFields)
	if strings.Join(actualFields, ",") != strings.Join(wantFields, ",") {
		t.Fatalf("capability fields = %v, want %v", actualFields, wantFields)
	}
}

func mustTestDispatcher(
	t *testing.T,
	descriptors []productcapabilities.RPCDescriptor,
	registrations ...Registration,
) *Dispatcher {
	t.Helper()
	dispatcher, err := newDispatcher(
		testIdentity(),
		descriptors,
		registrations,
	)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func testIdentity() Identity {
	return Identity{
		WorkspaceID:  testWorkspaceID,
		SessionEpoch: 7,
		FenceEpoch:   3,
		ClaimID:      testClaimID,
	}
}

func workspaceWire(sequence uint64) string {
	return `{"scope":"workspace","workspaceId":"` + testWorkspaceID +
		`","sessionEpoch":7,"operationId":"` + testOperationID +
		`","sequence":` + strconv.FormatUint(sequence, 10) + `}`
}

func equalJSONValues(left any, right any) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftRaw) == string(rightRaw)
}

func assertError(t *testing.T, response ResponseEnvelope, code int, message string) {
	t.Helper()
	if response.Error == nil || response.Error.Code != code ||
		response.Error.Message != message || response.Error.Data != nil ||
		len(response.Result) != 0 {
		t.Fatalf("response = %#v", response)
	}
}
