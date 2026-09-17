package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

// unrelatedSchemaFieldChangeRegistrations keeps dispatcher fixtures exact for
// the seven generated Go-owned routes without wiring a real domain.
func unrelatedSchemaFieldChangeRegistrations(t *testing.T) []productrpc.Registration {
	t.Helper()
	registrations := make([]productrpc.Registration, 0, len(schemaFieldChangeMethods))
	for _, method := range schemaFieldChangeMethods {
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
