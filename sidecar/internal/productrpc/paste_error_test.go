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

func dispatchPasteFailure(t *testing.T, method string, failure error) ResponseEnvelope {
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
		`{"jsonrpc":"2.0","id":"paste-error","method":"`+method+`","wire":`+wire+`,"params":{}}`,
	))
	if string(response.ID) != `"paste-error"` || string(response.Wire) != wire || len(response.Result) != 0 {
		t.Fatalf("paste error lost request identity or returned a result: %#v", response)
	}
	return response
}

func TestDispatchPasteErrorsExposeOnlyPasteMethodsAndSafeDetails(t *testing.T) {
	for _, method := range []string{"table.previewPaste", "table.applyPaste"} {
		t.Run(method, func(t *testing.T) {
			failure := fmt.Errorf("private wrapper: %w", &PasteError{
				Code: "paste_token_unknown", Message: "paste token not found",
				Details: map[string]any{
					"kind": "overridden", "code": "overridden", "message": "overridden",
					"expectedSchemaRevision": "",
					"details":                map[string]any{"sessionSecret": "private-secret", "field": "amount"},
				},
			})
			response := dispatchPasteFailure(t, method, failure)
			if response.Error == nil || response.Error.Code != CodePaste || response.Error.Message != "Paste error" {
				t.Fatalf("paste envelope = %#v", response.Error)
			}
			want := map[string]any{
				"kind": "paste_error", "code": "paste_token_unknown", "message": "paste token not found",
				"expectedSchemaRevision": "",
				"details":                map[string]any{"field": "amount"},
			}
			if !reflect.DeepEqual(response.Error.Data, want) {
				t.Fatalf("paste data = %#v, want %#v", response.Error.Data, want)
			}
			encoded, err := json.Marshal(response)
			if err != nil || strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "overridden") {
				t.Fatalf("paste envelope disclosed private details: %s, %v", encoded, err)
			}
		})
	}
}

func TestDispatchPasteErrorsFailClosedForUnclassifiedOrMalformedFailures(t *testing.T) {
	var absent *PasteError
	for _, sample := range []struct {
		name, method string
		failure      error
	}{
		{"unrelated method", "query.page", &PasteError{Code: "paste_token_unknown", Message: "private"}},
		{"ordinary error", "table.applyPaste", errors.New("private storage failure")},
		{"typed nil", "table.applyPaste", absent},
		{"missing code", "table.previewPaste", &PasteError{Message: "private"}},
		{"blank message", "table.previewPaste", &PasteError{Code: "paste_token_unknown", Message: "  "}},
		{"unserializable details", "table.applyPaste", &PasteError{
			Code: "paste_token_unknown", Message: "private",
			Details: map[string]any{"internal": make(chan int)},
		}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			response := dispatchPasteFailure(t, sample.method, sample.failure)
			assertError(t, response, CodeInternalError, "Internal error")
			if response.Error.Data != nil {
				t.Fatalf("private paste failure exposed data: %#v", response.Error.Data)
			}
		})
	}
}
