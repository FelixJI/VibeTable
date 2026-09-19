package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

// unrelatedFormulaProductRegistrations keeps dispatcher fixtures exact for
// the three generated Go-owned formula routes without wiring the domain.
func unrelatedFormulaProductRegistrations(t *testing.T) []productrpc.Registration {
	t.Helper()
	registrations := make([]productrpc.Registration, 0, len(formulaProductMethods))
	for _, method := range formulaProductMethods {
		registrations = append(registrations, productrpc.Registration{
			Method: method,
			Scope:  productcapabilities.WorkspaceScope,
			ValidateParams: func(json.RawMessage) error {
				t.Helper()
				t.Fatalf("unrelated fixture must not invoke %s", method)
				return nil
			},
			Handler: func(context.Context, json.RawMessage) (any, error) {
				t.Helper()
				t.Fatalf("unrelated fixture must not invoke %s", method)
				return nil, nil
			},
		})
	}
	return registrations
}
