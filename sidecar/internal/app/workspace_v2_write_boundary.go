package app

import (
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

const workspaceV2FieldCancelPrefix = "/api/vibetable/v2/field-change/cancel/"

var workspaceV2ReadOnlyPosts = map[string]struct{}{
	"/api/vibetable/v1/formulas/validate":        {},
	"/api/vibetable/v1/formulas/preview":         {},
	"/api/vibetable/v1/formulas/draft/validate":  {},
	"/api/vibetable/v1/query":                    {},
	"/api/vibetable/v1/query/validate-snapshot":  {},
	"/api/vibetable/v1/relations/search-targets": {},
	"/api/vibetable/v1/relations/preview-delta":  {},
	"/api/vibetable/v1/lookups/query":            {},
	"/api/vibetable/v1/lookups/preview":          {},
	"/api/vibetable/v1/mutations/preview":        {},
	"/api/vibetable/v1/schema/validate":          {},
	"/api/vibetable/v1/events/reconcile":         {},
	"/api/vibetable/v2/import-preview":           {},
}

var workspaceV2CoordinatedPosts = map[string]struct{}{
	"/api/vibetable/v1/mutations/apply":            {},
	"/api/vibetable/v1/schema/apply":               {},
	"/api/vibetable/v1/schema/delete":              {},
	"/api/vibetable/v1/relations/apply-delta":      {},
	"/api/vibetable/v1/metadata/dashboards/commit": {},
	"/api/vibetable/v2/field-change/plan":          {},
	"/api/vibetable/v2/field-change/apply":         {},
}

func bindWorkspaceV2WriteBoundary(event *core.ServeEvent) {
	event.Router.Bind(&hook.Handler[*core.RequestEvent]{
		Id:       "workspaceV2WriteBoundary",
		Priority: -9_000,
		Func: func(request *core.RequestEvent) error {
			if workspaceV2RequestAllowed(
				request.Request.Method,
				request.Request.URL.Path,
			) {
				return request.Next()
			}
			return request.JSON(http.StatusLocked, map[string]any{
				"code":      "workspace.v1_write_disabled",
				"message":   "legacy writes are disabled for workspace v2",
				"retryable": false,
			})
		},
	})
}

func workspaceV2RequestAllowed(method string, path string) bool {
	switch method {
	case http.MethodHead, http.MethodOptions:
		return true
	case http.MethodGet:
		// This legacy bootstrap lazily creates a superuser and therefore is
		// not a read despite its historical HTTP verb.
		return path != "/api/vibetable/v1/admin/bootstrap"
	case http.MethodPost:
		if path == workspaceV2RPCPath ||
			path == workspaceV2DrainPath ||
			path == shutdownPath ||
			isWorkspaceV2FieldCancelPath(path) {
			return true
		}
		_, allowed := workspaceV2ReadOnlyPosts[path]
		if allowed {
			return true
		}
		_, allowed = workspaceV2CoordinatedPosts[path]
		return allowed
	default:
		return false
	}
}

func isWorkspaceV2FieldCancelPath(path string) bool {
	jobID, found := strings.CutPrefix(path, workspaceV2FieldCancelPrefix)
	return found && jobID != "" && !strings.Contains(jobID, "/")
}
