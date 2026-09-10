package app

import (
	"context"
	"encoding/json"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"testing"
)

func unrelatedDashboardRegistration(t *testing.T, method string) productrpc.Registration {
	return productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope, ValidateParams: func(json.RawMessage) error { t.Fatal("unexpected Dashboard validation"); return nil }, Handler: func(context.Context, json.RawMessage) (any, error) {
		t.Fatal("unexpected Dashboard invocation")
		return nil, nil
	}}
}
