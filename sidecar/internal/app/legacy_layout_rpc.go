package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

// retiredLayoutMethods are the closed public Interface/Insights product RPCs
// of the retired visual layout builder (Goal #480 / #485). They stay
// registered so a direct request receives an explicit, stable rejection
// instead of a generic method-not-found, with regression coverage in
// legacy_layout_rpc_test.go.
var retiredLayoutMethods = []string{
	"interface.list",
	"interface.load",
	"interface.commit",
	"interface.delete",
	"insights.dashboardQueryLimits",
	"insights.deleteDashboardWorkspace",
	"insights.executeDashboardQuery",
	"insights.listDashboards",
	"insights.panelManifest",
	"insights.readDashboardWorkspace",
	"insights.saveDashboardDraft",
}

func retiredLayoutRegistrations() []productrpc.Registration {
	registrations := make([]productrpc.Registration, 0, len(retiredLayoutMethods))
	for _, method := range retiredLayoutMethods {
		registrations = append(registrations, retiredLayoutRegistration(method))
	}
	return registrations
}

func retiredLayoutRegistration(method string) productrpc.Registration {
	return productrpc.Registration{
		Method:         method,
		Scope:          productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { return nil },
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, &productrpc.PublicError{
				Code:    "layout.retired",
				Message: fmt.Sprintf("%s retired with the visual layout builder (Goal #480); use installed code plugins or the read-only legacyLayout.snapshot preservation channel.", method),
				Details: map[string]any{"method": method, "preservationRpc": "legacyLayout.snapshot"},
			}
		},
	}
}

// legacyLayoutSnapshotRegistration exposes the #485 Host-owned read-only
// preservation snapshot: raw payloads of the three original collections with
// stable ids, revisions and per-item diagnostics. It never writes, migrates
// or executes legacy definitions.
func legacyLayoutSnapshotRegistration(service *metadata.LegacyPreservationService) productrpc.Registration {
	return productrpc.Registration{
		Method: "legacyLayout.snapshot",
		Scope:  productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			var params map[string]json.RawMessage
			if err := json.Unmarshal(raw, &params); err != nil || params == nil || len(params) != 0 {
				return &productrpc.PublicError{Code: "legacyLayout.params.invalid", Message: "Preservation accepts only an empty object."}
			}
			return nil
		},
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			return service.Snapshot(ctx)
		},
	}
}
