package productrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
)

func dispatchPresetHandlerError(t *testing.T, method string, failure error) ResponseEnvelope {
	t.Helper()
	dispatcher := mustTestDispatcher(t, []productcapabilities.RPCDescriptor{{
		Method: method, Scope: productcapabilities.WorkspaceScope,
	}}, Registration{
		Method: method, Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(json.RawMessage) error { return nil },
		Handler:        func(context.Context, json.RawMessage) (any, error) { return nil, failure },
	})
	wire := workspaceWire(0)
	response := dispatcher.Dispatch(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":"preset-error","method":"`+method+`","wire":`+wire+`,"params":{}}`,
	))
	if string(response.ID) != `"preset-error"` || string(response.Wire) != wire {
		t.Fatalf("error lost request identity: %#v", response)
	}
	encoded, err := json.Marshal(response)
	if err != nil || strings.Contains(string(encoded), "private") {
		t.Fatalf("error response exposed private handler details: %s, %v", encoded, err)
	}
	return response
}

func TestDispatchPresetWrappedConflictsKeepOnlyFixedPublicProjection(t *testing.T) {
	for _, test := range []struct{ method, code, field, message string }{
		{"preset.save", "preset_edit_conflict", "expectedRevision", "Preset changed elsewhere."},
		{"preset.delete", "preset_idempotency_conflict", "operationId", "Operation was used for another Preset request."},
	} {
		t.Run(test.method, func(t *testing.T) {
			// Real callers can wrap a domain failure with private storage context.
			// Its Error() text must not become the RPC public message.
			failure := fmt.Errorf("private storage context: %w", &PresetError{
				Code: test.code, Field: test.field, Message: "private domain detail",
			})
			response := dispatchPresetHandlerError(t, test.method, failure)
			want := map[string]any{
				"kind": "insights_error", "code": test.code, "field": test.field, "message": test.message,
			}
			if response.Error == nil || response.Error.Code != -32080 ||
				response.Error.Message != "Insights error" || len(response.Result) != 0 ||
				!reflect.DeepEqual(response.Error.Data, want) {
				t.Fatalf("wrapped preset failure = %#v, want %#v", response, want)
			}
		})
	}
}

func TestDispatchPresetRejectsUnclassifiedOrMismatchedErrors(t *testing.T) {
	var absent *PresetError
	for _, test := range []struct {
		name, method string
		failure      error
	}{
		{"private failure", "preset.save", errors.New("private storage failure")},
		{"typed nil", "preset.delete", absent},
		{"unknown code", "preset.save", &PresetError{Code: "private_unknown", Field: "expectedRevision", Message: "private detail"}},
		{"edit wrong field", "preset.save", &PresetError{Code: "preset_edit_conflict", Field: "operationId", Message: "private detail"}},
		{"idempotency wrong field", "preset.delete", &PresetError{Code: "preset_idempotency_conflict", Field: "expectedRevision", Message: "private detail"}},
		{"read method", "preset.list", &PresetError{Code: "preset_edit_conflict", Field: "expectedRevision", Message: "private detail"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := dispatchPresetHandlerError(t, test.method, test.failure)
			assertError(t, response, CodeInternalError, "Internal error")
		})
	}
}
