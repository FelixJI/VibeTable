package app

import (
	"net/http"
	"testing"
)

func TestWorkspaceV2WriteBoundaryFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		path    string
		allowed bool
	}{
		{"v2 rpc", http.MethodPost, workspaceV2RPCPath, true},
		{"host drain", http.MethodPost, workspaceV2DrainPath, true},
		{"shutdown", http.MethodPost, shutdownPath, true},
		{"query", http.MethodPost, "/api/vibetable/v1/query", true},
		{"formula preview", http.MethodPost, "/api/vibetable/v1/formulas/preview", true},
		{"read record", http.MethodGet, "/api/collections/items/records/id", true},
		{"coordinated mutation", http.MethodPost, "/api/vibetable/v1/mutations/apply", true},
		{"legacy restore preview", http.MethodPost, "/api/vibetable/v1/history/restore-preview", false},
		{"legacy restore apply", http.MethodPost, "/api/vibetable/v1/history/restore-apply", false},
		{"removed v2 restore preview route", http.MethodPost, "/api/vibetable/v2/history/restore-preview", false},
		{"removed v2 restore apply route", http.MethodPost, "/api/vibetable/v2/history/restore-apply", false},
		{"coordinated schema apply", http.MethodPost, "/api/vibetable/v1/schema/apply", true},
		{"coordinated schema delete", http.MethodPost, "/api/vibetable/v1/schema/delete", true},
		{"coordinated relation apply", http.MethodPost, "/api/vibetable/v1/relations/apply-delta", true},
		{"metadata upsert", http.MethodPost, "/api/vibetable/v1/metadata/grid/upsert", false},
		{"job resume", http.MethodPost, "/api/vibetable/v1/jobs/id/resume", false},
		{"direct record create", http.MethodPost, "/api/collections/items/records", false},
		{"direct record patch", http.MethodPatch, "/api/collections/items/records/id", false},
		{"direct record delete", http.MethodDelete, "/api/collections/items/records/id", false},
		{"superuser bootstrap get", http.MethodGet, "/api/vibetable/v1/admin/bootstrap", false},
		{"unknown post", http.MethodPost, "/api/vibetable/v1/plugin/write", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := workspaceV2RequestAllowed(
				test.method,
				test.path,
			); actual != test.allowed {
				t.Fatalf(
					"workspaceV2RequestAllowed(%q, %q) = %v, want %v",
					test.method,
					test.path,
					actual,
					test.allowed,
				)
			}
		})
	}
}
