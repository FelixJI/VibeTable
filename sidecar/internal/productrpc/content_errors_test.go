package productrpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
)

func TestContentErrorEnvelopeDoesNotBroadenOtherProductErrors(t *testing.T) {
	for _, sample := range []struct {
		err    error
		code   int
		kind   string
		method string
	}{
		{&ContentModelError{Code: "content_profile.not_found", Message: "Content profile not found."}, CodeContentModel, "content_model_error", "contentProfile.load"},
		{&ContentModelError{Code: "schema.invalid", Message: "not content"}, CodeInternalError, "", "contentProfile.load"},
		{&ContentModelError{Code: "content_model.future_error", Message: "unknown"}, CodeInternalError, "", "contentProfile.load"},
		{&ContentModelError{Code: "content_profile.not_found", Message: "wrong method"}, CodeInternalError, "", "query.page"},
		{&PublicError{Code: "schema.invalid", Message: "bad field"}, CodeProductData, "product_data_error", "query.page"},
	} {
		d := mustTestDispatcher(t, []productcapabilities.RPCDescriptor{{Method: sample.method, Scope: productcapabilities.WorkspaceScope}}, Registration{Method: sample.method, Scope: productcapabilities.WorkspaceScope, ValidateParams: func(json.RawMessage) error { return nil }, Handler: func(context.Context, json.RawMessage) (any, error) { return nil, sample.err }})
		response := d.Dispatch(context.Background(), []byte(`{"jsonrpc":"2.0","id":"x","method":"`+sample.method+`","wire":`+workspaceWire(1)+`,"params":{}}`))
		if response.Error == nil || response.Error.Code != sample.code {
			t.Fatalf("response=%+v", response)
		}
		if sample.kind != "" && response.Error.Data["kind"] != sample.kind {
			t.Fatalf("data=%+v", response.Error.Data)
		}
	}
}
