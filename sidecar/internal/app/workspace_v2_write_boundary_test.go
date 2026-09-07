package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

func TestWorkspaceV2WriteBoundaryFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		path    string
		allowed bool
	}{
		{"v2 rpc", http.MethodPost, workspaceV2RPCPath, true},
		{"product rpc", http.MethodPost, productRPCPath, true},
		{"product rpc extra segment", http.MethodPost, productRPCPath + "/extra", false},
		{"host drain", http.MethodPost, workspaceV2DrainPath, true},
		{"shutdown", http.MethodPost, shutdownPath, true},
		{"query", http.MethodPost, "/api/vibetable/v1/query", true},
		{"formula preview", http.MethodPost, "/api/vibetable/v1/formulas/preview", true},
		{"formula draft validate", http.MethodPost, "/api/vibetable/v1/formulas/draft/validate", true},
		{"mutation preview", http.MethodPost, "/api/vibetable/v1/mutations/preview", true},
		{"import preview", http.MethodPost, "/api/vibetable/v2/import-preview", true},
		{"read record", http.MethodGet, "/api/collections/items/records/id", true},
		{"coordinated mutation", http.MethodPost, "/api/vibetable/v1/mutations/apply", true},
		{"coordinated field plan", http.MethodPost, "/api/vibetable/v2/field-change/plan", true},
		{"coordinated field apply", http.MethodPost, "/api/vibetable/v2/field-change/apply", true},
		{"coordinated table create", http.MethodPost, "/api/vibetable/v2/schema/tables", true},
		{"coordinated table settings", http.MethodPost, "/api/vibetable/v2/schema/table-settings", true},
		{"table settings with extra segment", http.MethodPost, "/api/vibetable/v2/schema/table-settings/extra", false},
		{"coordinated field cancel", http.MethodPost, "/api/vibetable/v2/field-change/cancel/job-1", true},
		{"field cancel without job", http.MethodPost, "/api/vibetable/v2/field-change/cancel/", false},
		{"field cancel with extra segment", http.MethodPost, "/api/vibetable/v2/field-change/cancel/job-1/extra", false},
		{"legacy restore preview", http.MethodPost, "/api/vibetable/v1/history/restore-preview", false},
		{"legacy restore apply", http.MethodPost, "/api/vibetable/v1/history/restore-apply", false},
		{"removed v2 restore preview route", http.MethodPost, "/api/vibetable/v2/history/restore-preview", false},
		{"removed v2 restore apply route", http.MethodPost, "/api/vibetable/v2/history/restore-apply", false},
		{"removed schema validate", http.MethodPost, "/api/vibetable/v1/schema/validate", false},
		{"removed schema apply", http.MethodPost, "/api/vibetable/v1/schema/apply", false},
		{"coordinated schema delete", http.MethodPost, "/api/vibetable/v1/schema/delete", true},
		{"coordinated relation apply", http.MethodPost, "/api/vibetable/v1/relations/apply-delta", true},
		{"coordinated dashboard commit", http.MethodPost, "/api/vibetable/v1/metadata/dashboards/commit", true},
		{"coordinated interface upsert", http.MethodPost, "/api/vibetable/v1/metadata/interfaces/upsert", true},
		{"coordinated interface delete", http.MethodPost, "/api/vibetable/v1/metadata/interfaces/delete", true},
		{"coordinated content profile upsert", http.MethodPost, "/api/vibetable/v1/metadata/content_profiles/upsert", true},
		{"metadata mutation with extra segment", http.MethodPost, "/api/vibetable/v1/metadata/interfaces/upsert/extra", false},
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

func TestWorkspaceV2WriteRejectionDrainsBodyWithinLimit(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		body          string
		contentLength int64
		unread        int
		close         bool
	}{
		{"known length", "{}", 2, 0, false},
		{"unknown length", "{}", -1, 0, false},
		{"bounded unknown length", strings.Repeat("x", (1<<20)+1), -1, 1, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body := strings.NewReader(testCase.body)
			response := &rejectionBodyResponse{
				ResponseRecorder: httptest.NewRecorder(), body: body, t: t, unread: testCase.unread,
			}
			r := router.NewRouter(func(writer http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
				return &core.RequestEvent{Event: router.Event{Response: writer, Request: request}}, nil
			})
			bindWorkspaceV2WriteBoundary(&core.ServeEvent{Router: r})
			called := false
			r.POST("/api/vibetable/v1/history/restore-apply", func(event *core.RequestEvent) error {
				called = true
				return event.NoContent(http.StatusOK)
			})
			mux, err := r.BuildMux()
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/vibetable/v1/history/restore-apply", body)
			request.ContentLength = testCase.contentLength
			request.Close = true
			mux.ServeHTTP(response, request)
			if (response.Header().Get("Connection") == "close") != testCase.close {
				t.Fatal("bounded rejection did not close unread connection")
			}
			if called {
				t.Fatal("rejected write reached handler")
			}
			if response.Code != http.StatusLocked || !strings.Contains(response.Body.String(), "workspace.v1_write_disabled") {
				t.Fatalf("rejection = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

type rejectionBodyResponse struct {
	*httptest.ResponseRecorder
	body   *strings.Reader
	t      *testing.T
	unread int
}

func (response *rejectionBodyResponse) WriteHeader(status int) {
	if response.body.Len() != response.unread {
		response.t.Errorf("rejection response started with %d unread request bytes, want %d", response.body.Len(), response.unread)
	}
	response.ResponseRecorder.WriteHeader(status)
}
