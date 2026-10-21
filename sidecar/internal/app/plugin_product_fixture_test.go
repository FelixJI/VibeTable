package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

func unrelatedPluginCatalogRegistration(t *testing.T, method string) productrpc.Registration {
	t.Helper()
	return productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(json.RawMessage) error { t.Fatalf("unrelated plugin validator invoked: %s", method); return nil },
		Handler: func(context.Context, json.RawMessage) (any, error) {
			t.Fatalf("unrelated plugin handler invoked: %s", method)
			return nil, nil
		},
	}
}

// unrelatedPluginPreservationRegistration keeps dispatcher fixtures exact for
// the generated pluginPreservation.snapshot route without reading anything.
func unrelatedPluginPreservationRegistration(t *testing.T) productrpc.Registration {
	t.Helper()
	return productrpc.Registration{Method: "pluginPreservation.snapshot", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(json.RawMessage) error { t.Fatalf("unrelated preservation validator invoked"); return nil },
		Handler: func(context.Context, json.RawMessage) (any, error) {
			t.Fatalf("unrelated preservation handler invoked")
			return nil, nil
		},
	}
}
