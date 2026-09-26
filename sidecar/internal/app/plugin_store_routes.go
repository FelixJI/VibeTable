package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
)

const pluginStorePath = "/api/vibetable/v1/plugins/store"

// This private session-authenticated seam names operations, never collections,
// SQL or source paths. Workspace binding is checked before any read or write.
type pluginStoreRequest struct {
	Operation        string          `json:"operation"`
	ProjectKey       string          `json:"projectKey"`
	PluginID         string          `json:"pluginId"`
	ItemKey          string          `json:"itemKey"`
	Payload          json.RawMessage `json:"payload"`
	ExpectedRevision *int64          `json:"expectedRevision"`
	LocalPath        string          `json:"localPath"`
	Plan             json.RawMessage `json:"plan"`
	PackageRevision  json.RawMessage `json:"packageRevision"`
}

func registerPluginStoreRoutes(r *router.Router[*core.RequestEvent], service *pluginstore.Service, gates ...businessWriteGate) {
	r.POST(pluginStorePath, func(request *core.RequestEvent) error {
		raw, err := io.ReadAll(io.LimitReader(request.Request.Body, maxProductRPCRequestBytes+1))
		if err != nil || len(raw) > maxProductRPCRequestBytes {
			return writePluginStoreError(request, nil)
		}
		var body pluginStoreRequest
		if decodePluginParams(raw, &body) != nil {
			return writePluginStoreError(request, nil)
		}
		allowed, write := pluginStoreFields(body.Operation)
		if allowed == nil {
			return writePluginStoreError(request, nil)
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return writePluginStoreError(request, nil)
		}
		for key := range fields {
			if key != "operation" && !allowed[key] {
				return writePluginStoreError(request, nil)
			}
		}
		for key := range allowed {
			if _, ok := fields[key]; !ok {
				return writePluginStoreError(request, nil)
			}
		}
		if body.ProjectKey != "" {
			if err := service.ValidateProject(body.ProjectKey); err != nil {
				return writePluginStoreError(request, err)
			}
		}
		if allowed["projectKey"] && body.ProjectKey == "" || allowed["pluginId"] && body.PluginID == "" || allowed["itemKey"] && body.ItemKey == "" {
			return writePluginStoreError(request, nil)
		}
		if allowed["payload"] {
			var identity struct {
				ProjectKey string `json:"projectKey"`
				PluginID   string `json:"pluginId"`
			}
			if json.Unmarshal(body.Payload, &identity) != nil || identity.ProjectKey != body.ProjectKey || identity.PluginID != body.PluginID {
				return writePluginStoreError(request, nil)
			}
		}
		var result any
		apply := func(ctx context.Context) error {
			var err error
			result, err = callPluginStore(ctx, service, body)
			return err
		}
		if write {
			err = runBusinessWrite(request.Request.Context(), gates, "plugin.store."+body.Operation, security.RandomString(24), apply)
		} else {
			err = apply(request.Request.Context())
		}
		if err != nil {
			return writePluginStoreError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})
}

func pluginStoreFields(operation string) (map[string]bool, bool) {
	var keys []string
	write := false
	switch operation {
	case "list_installations", "list_project_audit":
		keys = []string{"projectKey"}
	case "get_installation", "list_package_revisions", "list_audit":
		keys = []string{"projectKey", "pluginId"}
	case "get_private_setting":
		keys = []string{"projectKey", "pluginId", "itemKey"}
	case "is_package_path_referenced":
		keys = []string{"localPath"}
	case "save_installation", "save_private_setting":
		keys, write = []string{"projectKey", "pluginId", "payload", "expectedRevision"}, true
	case "save_package_revision", "record_audit":
		keys, write = []string{"projectKey", "pluginId", "payload"}, true
	case "delete_installation", "delete_package_revisions", "delete_private_settings":
		keys, write = []string{"projectKey", "pluginId"}, true
	case "delete_package_revision":
		keys, write = []string{"projectKey", "pluginId", "itemKey"}, true
	case "commit_install":
		keys, write = []string{"plan", "packageRevision"}, true
	default:
		return nil, false
	}
	fields := make(map[string]bool, len(keys))
	for _, key := range keys {
		fields[key] = true
	}
	return fields, write
}

func callPluginStore(ctx context.Context, service *pluginstore.Service, body pluginStoreRequest) (any, error) {
	switch body.Operation {
	case "get_installation":
		return service.GetInstallation(ctx, body.ProjectKey, body.PluginID)
	case "list_installations":
		return service.ListInstallations(ctx, body.ProjectKey)
	case "save_installation":
		return service.SaveInstallation(ctx, body.Payload, body.ExpectedRevision)
	case "delete_installation":
		return service.DeleteInstallation(ctx, body.ProjectKey, body.PluginID)
	case "list_package_revisions":
		return service.ListPackageRevisions(ctx, body.ProjectKey, body.PluginID)
	case "save_package_revision":
		return service.SavePackageRevision(ctx, body.Payload)
	case "delete_package_revision":
		return service.DeletePackageRevision(ctx, body.ProjectKey, body.PluginID, body.ItemKey)
	case "delete_package_revisions":
		return service.DeletePackageRevisions(ctx, body.ProjectKey, body.PluginID)
	case "is_package_path_referenced":
		return service.IsPackagePathReferenced(ctx, body.LocalPath)
	case "get_private_setting":
		return service.GetPrivateSetting(ctx, body.ProjectKey, body.PluginID, body.ItemKey)
	case "save_private_setting":
		return service.SavePrivateSetting(ctx, body.Payload, body.ExpectedRevision)
	case "delete_private_settings":
		return service.DeletePrivateSettings(ctx, body.ProjectKey, body.PluginID)
	case "record_audit":
		return service.RecordAudit(ctx, body.Payload)
	case "list_audit":
		return service.ListAudit(ctx, body.ProjectKey, body.PluginID)
	case "list_project_audit":
		return service.ListProjectAudit(ctx, body.ProjectKey)
	case "commit_install":
		return service.CommitInstall(ctx, body.Plan, body.PackageRevision)
	default:
		return nil, errors.New("invalid plugin operation")
	}
}

func writePluginStoreError(request *core.RequestEvent, err error) error {
	var domain *pluginstore.Error
	if errors.As(err, &domain) {
		return request.JSON(http.StatusConflict, domain)
	}
	if err == nil {
		return request.JSON(http.StatusBadRequest, map[string]any{"code": "plugin.request_invalid", "message": "plugin store request is invalid"})
	}
	return request.JSON(http.StatusInternalServerError, map[string]any{"code": "plugin.storage_failed", "message": "plugin store operation failed"})
}
