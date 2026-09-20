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

func dispatchVersionHandlerError(t *testing.T, method string, failure error) ResponseEnvelope {
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
		`{"jsonrpc":"2.0","id":"version-error","method":"`+method+`","wire":`+wire+`,"params":{}}`,
	))
	if string(response.ID) != `"version-error"` || string(response.Wire) != wire || len(response.Result) != 0 {
		t.Fatalf("error lost request identity or returned a result: %#v", response)
	}
	encoded, err := json.Marshal(response)
	if err != nil || strings.Contains(string(encoded), "private") {
		t.Fatalf("error response exposed private handler details: %s, %v", encoded, err)
	}
	return response
}

func TestDispatchVersionWrappedDomainErrorsKeepPublicEnvelope(t *testing.T) {
	for _, test := range []struct{ method, code string }{
		{"version.list", "version_record_unavailable"},
		{"version.create", "version_audit_missing"},
		{"version.save", "version_edit_conflict"},
		{"version.compare", "version_not_restorable"},
		{"version.promote", "version_main_conflict"},
		{"version.delete", "version_idempotency_conflict"},
	} {
		t.Run(test.method, func(t *testing.T) {
			const message = "Named revision operation rejected."
			failure := fmt.Errorf("private storage context: %w", &VersionError{Code: test.code, Message: message})
			response := dispatchVersionHandlerError(t, test.method, failure)
			want := map[string]any{"kind": "insights_error", "code": test.code, "message": message}
			if response.Error == nil || response.Error.Code != -32080 ||
				response.Error.Message != "Insights error" || !reflect.DeepEqual(response.Error.Data, want) {
				t.Fatalf("version failure = %#v, want %#v", response, want)
			}
		})
	}
}

func TestDispatchVersionRejectsUnclassifiedOrMismatchedErrors(t *testing.T) {
	var absent *VersionError
	for _, test := range []struct {
		name, method string
		failure      error
	}{
		{"private failure", "version.list", errors.New("private storage failure")},
		{"typed nil", "version.delete", absent},
		{"empty message", "version.save", &VersionError{Code: "version_edit_conflict"}},
		{"unknown code", "version.promote", &VersionError{Code: "version_future_error", Message: "private detail"}},
		{"unrelated method", "query.page", &VersionError{Code: "version_not_found", Message: "private detail"}},
		{"unknown version method", "version.future", &VersionError{Code: "version_not_found", Message: "private detail"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := dispatchVersionHandlerError(t, test.method, test.failure)
			assertError(t, response, CodeInternalError, "Internal error")
			if response.Error.Data != nil {
				t.Fatalf("unclassified error exposed domain data: %#v", response.Error.Data)
			}
		})
	}
}
