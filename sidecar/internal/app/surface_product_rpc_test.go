package app

import (
	"context"
	"encoding/json"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"testing"
)

func unrelatedSurfaceRegistration(t *testing.T, method string) productrpc.Registration {
	t.Helper()
	switch method {
	case "interface.list", "interface.load", "interface.commit", "interface.delete":
	default:
		t.Fatalf("unexpected Surface fixture method %s", method)
	}
	return surfaceRegistration(method, func(context.Context, json.RawMessage) (any, error) {
		t.Fatalf("unrelated Surface method %s ran", method)
		return nil, nil
	})
}
