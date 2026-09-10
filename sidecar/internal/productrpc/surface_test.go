package productrpc

import (
	"context"
	"encoding/json"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"testing"
)

func TestSurfaceErrorProjectionIsClosedByMethodCodeAndPathPresence(t *testing.T) {
	for _, sample := range []struct {
		method, code string
		expected     int
	}{
		{"interface.list", "surface.storage_invalid", CodeSurface}, {"interface.load", "surface.not_found", CodeSurface}, {"interface.commit", "surface.name_required", CodeSurface}, {"interface.delete", "surface.idempotency_conflict", CodeSurface}, {"schema.list", "surface.not_found", CodeInternalError}, {"interface.load", "surface.future_error", CodeInternalError}, {"interface.load", "contentProfile.not_found", CodeInternalError},
	} {
		t.Run(sample.method+"/"+sample.code, func(t *testing.T) {
			path := ""
			dispatcher := mustTestDispatcher(t, []productcapabilities.RPCDescriptor{{Method: sample.method, Scope: productcapabilities.WorkspaceScope}}, Registration{Method: sample.method, Scope: productcapabilities.WorkspaceScope, ValidateParams: func(json.RawMessage) error { return nil }, Handler: func(context.Context, json.RawMessage) (any, error) {
				return nil, &SurfaceError{Code: sample.code, Message: "Interface rejected.", Path: &path}
			}})
			response := dispatcher.Dispatch(context.Background(), []byte(`{"jsonrpc":"2.0","id":"error","method":"`+sample.method+`","wire":{"scope":"workspace","workspaceId":"`+testWorkspaceID+`","sessionEpoch":7,"operationId":"`+testOperationID+`","sequence":0},"params":{}}`))
			if response.Error == nil || response.Error.Code != sample.expected {
				t.Fatalf("error %#v", response.Error)
			}
			if sample.expected == CodeSurface {
				if response.Error.Message != "Interface error" || response.Error.Data["path"] != "" || len(response.Error.Data) != 4 {
					t.Fatalf("lost explicit empty path %#v", response.Error)
				}
			} else if response.Error.Data != nil {
				t.Fatalf("leaked domain data %#v", response.Error)
			}
		})
	}
}
