package app

import (
	"context"
	"encoding/json"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"testing"
)

func unrelatedRelationWriteRegistrations(t *testing.T) []productrpc.Registration {
	t.Helper()
	result := make([]productrpc.Registration, 0, len(relationWriteMethods)+2)
	for _, method := range append(append([]string{}, relationWriteMethods...), "table.applyPaste", "table.previewPaste") {
		result = append(result, productrpc.Registration{
			Method: method, Scope: productcapabilities.WorkspaceScope,
			ValidateParams: func(json.RawMessage) error { t.Fatalf("unrelated %s validation", method); return nil },
			Handler: func(context.Context, json.RawMessage) (any, error) {
				t.Fatalf("unrelated %s handler", method)
				return nil, nil
			},
		})
	}
	return result
}
