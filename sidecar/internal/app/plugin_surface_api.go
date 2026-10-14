package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

const pluginSurfaceAPIPath = "/api/vibetable/v3/plugins/surface"

// This seam is session-authenticated Host-only; no Product RPC registration exists.
type pluginSurfaceAPIRequest struct {
	Method               string                            `json:"method"`
	ProjectKey           string                            `json:"projectKey"`
	PluginID             string                            `json:"pluginId"`
	PackageHash          string                            `json:"packageHash"`
	SessionEpoch         uint64                            `json:"sessionEpoch"`
	InstallationRevision int64                             `json:"installationRevision"`
	ActiveCollection     string                            `json:"activeCollection"`
	RequestID            string                            `json:"requestId"`
	Request              json.RawMessage                   `json:"request"`
	Cursor               string                            `json:"cursor,omitempty"`
	Dependencies         map[string]pluginComputedRevision `json:"dependencies,omitempty"`
}

type pluginSurfaceInstallation struct {
	pluginInstallation
	PackageHash string `json:"packageHash"`
}

func surfaceInstallation(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest) (pluginSurfaceInstallation, error) {
	var installed pluginSurfaceInstallation
	raw, err := pluginstore.New(app, workspace).GetInstallation(ctx, input.ProjectKey, input.PluginID)
	if err != nil || json.Unmarshal(raw, &installed) != nil || installed.Status != "enabled" || strings.TrimPrefix(installed.PackageHash, "sha256:") != input.PackageHash {
		return installed, pluginDataError("plugin_read_denied")
	}
	if installed.Manifest.Compatibility.PluginAPI != "3.x" {
		return installed, pluginDataError("plugin_api_unsupported")
	}
	if input.InstallationRevision != installed.Revision {
		return installed, pluginDataError("plugin_cursor_stale")
	}
	return installed, nil
}

func registerPluginSurfaceAPIRoutes(r *router.Router[*core.RequestEvent], app core.App, source *queryschema.Source, kernel *mutation.Kernel, workspace string, epoch uint64, gate businessWriteGate) {
	r.POST(pluginSurfaceAPIPath, func(event *core.RequestEvent) error {
		raw, err := io.ReadAll(io.LimitReader(event.Request.Body, maxMutationRequestBytes+1))
		var input pluginSurfaceAPIRequest
		if err != nil || len(raw) > maxMutationRequestBytes || decodePluginParams(raw, &input) != nil || input.SessionEpoch != epoch || epoch == 0 || input.RequestID == "" {
			return event.JSON(http.StatusBadRequest, map[string]any{"code": "plugin_capability_invalid"})
		}
		ctx, cancel := context.WithTimeout(event.Request.Context(), 15*time.Second)
		defer cancel()
		result, err := executePluginSurfaceAPI(ctx, app, source, kernel, workspace, input, gate)
		if err != nil {
			code := "plugin_request_failed"
			var product *query.ProductError
			var mutationErr *mutation.ProductError
			if errors.As(err, &product) {
				code = product.Code
			}
			if errors.As(err, &mutationErr) {
				code = mutationErr.Code
			}
			if errors.Is(err, context.Canceled) {
				code = "plugin_cancel_requested"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				code = "plugin_timeout"
			}
			return event.JSON(http.StatusConflict, map[string]any{"code": code, "message": "plugin request could not be completed"})
		}
		return event.JSON(http.StatusOK, result)
	})
}

func executePluginSurfaceAPI(ctx context.Context, app core.App, source *queryschema.Source, kernel *mutation.Kernel, workspace string, input pluginSurfaceAPIRequest, gate businessWriteGate) (any, error) {
	installed, err := surfaceInstallation(ctx, app, workspace, input)
	if err != nil {
		return nil, err
	}
	switch input.Method {
	case "data.describe", "data.query":
		operation := "describe"
		if input.Method == "data.query" {
			operation = "query"
		}
		return executePluginData(ctx, app, source, workspace, pluginDataRequest{Operation: operation, ProjectKey: input.ProjectKey, PluginID: input.PluginID, SessionEpoch: input.SessionEpoch, InstallationRevision: input.InstallationRevision, ActiveCollection: input.ActiveCollection, Request: input.Request, Cursor: input.Cursor, Dependencies: input.Dependencies})
	case "context.read":
		if string(input.Request) != "{}" {
			return nil, pluginDataError("plugin_capability_invalid")
		}
		var active any
		if installed.grant(input.ActiveCollection, input.ActiveCollection) != nil {
			active = input.ActiveCollection
		}
		return map[string]any{"contract": "vibetable.command-context.v1", "projectKey": input.ProjectKey, "collection": active, "selectedKeys": []any{}, "querySnapshot": nil, "locale": "zh-CN", "theme": "light", "density": "comfortable", "user": map[string]any{}, "hostVersion": ""}, nil
	case "storage.get", "storage.set", "storage.delete":
		var request struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value,omitempty"`
		}
		if decodePluginParams(input.Request, &request) != nil || len(request.Key) < 1 || len(request.Key) > 128 || input.Method != "storage.set" && request.Value != nil || input.Method == "storage.set" && request.Value == nil {
			return nil, pluginDataError("plugin_capability_invalid")
		}
		var result any
		action := func(ctx context.Context) error {
			return app.RunInTransaction(func(tx core.App) error {
				current, err := surfaceInstallation(ctx, tx, workspace, input)
				if err != nil {
					return err
				}
				if !current.Manifest.Permissions.PrivateStorage {
					return pluginDataError("plugin_storage_denied")
				}
				store := pluginstore.New(tx, workspace)
				if input.Method == "storage.delete" {
					_, err = store.DeletePrivateSetting(ctx, input.ProjectKey, input.PluginID, request.Key)
					return err
				}
				saved, err := store.GetPrivateSetting(ctx, input.ProjectKey, input.PluginID, request.Key)
				if err != nil {
					return err
				}
				var setting struct {
					Revision int64           `json:"revision"`
					Value    json.RawMessage `json:"value"`
				}
				if len(saved) != 0 && string(saved) != "null" {
					if json.Unmarshal(saved, &setting) != nil {
						return pluginDataError("plugin_storage_failed")
					}
				}
				if input.Method == "storage.get" {
					if setting.Value != nil {
						result = setting.Value
					}
					return nil
				}
				var expected *int64
				if setting.Revision > 0 {
					previous := setting.Revision
					expected = &previous
				}
				payload, _ := json.Marshal(map[string]any{"projectKey": input.ProjectKey, "pluginId": input.PluginID, "settingKey": request.Key, "value": request.Value, "revision": setting.Revision + 1})
				_, err = store.SavePrivateSetting(ctx, payload, expected)
				return err
			})
		}
		if input.Method == "storage.get" {
			err = action(ctx)
		} else {
			err = runBusinessWrite(ctx, []businessWriteGate{gate}, "plugin.surface.storage", input.RequestID, action)
		}
		return result, err
	case "mutation.preview", "mutation.commit":
		request, err := surfaceMutationRequest(ctx, app, workspace, input)
		if err != nil {
			return nil, err
		}
		if input.Method == "mutation.preview" {
			preview, err := kernel.Preview(ctx, request)
			if err != nil {
				return nil, err
			}
			return map[string]any{"affectedCount": len(preview.Operations), "collection": request.TableID, "schemaRevision": request.SchemaRevision}, nil
		}
		var receipt mutation.Receipt
		err = runBusinessWrite(ctx, []businessWriteGate{gate}, "plugin.surface.mutation", input.RequestID, func(ctx context.Context) error {
			var applyErr error
			receipt, applyErr = kernel.Apply(ctx, request)
			return applyErr
		})
		return receipt, err
	default:
		return nil, pluginDataError("plugin_api_unsupported")
	}
}

func surfaceMutationRequest(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest) (mutation.Request, error) {
	var plan struct {
		Contract       string `json:"contract"`
		Collection     string `json:"collection"`
		SchemaRevision string `json:"schemaRevision"`
		Operations     []struct {
			Kind           string         `json:"kind"`
			PrimaryKey     *string        `json:"primaryKey,omitempty"`
			ExpectedDigest *string        `json:"expectedDigest,omitempty"`
			Values         map[string]any `json:"values"`
		} `json:"operations"`
	}
	if decodePluginParams(input.Request, &plan) != nil || plan.Contract != "vibetable.surface-mutation-plan.v1" || len(plan.Operations) < 1 || len(plan.Operations) > 1000 || plan.SchemaRevision == "" {
		return mutation.Request{}, pluginDataError("plugin_capability_invalid")
	}
	request := mutation.Request{ContractVersion: "2.0", RequestID: input.RequestID, IdempotencyKey: input.RequestID, TableID: plan.Collection, SchemaRevision: plan.SchemaRevision, Actor: mutation.Actor{Type: "plugin", ID: input.PluginID}}
	for _, item := range plan.Operations {
		kind := mutation.OperationInsert
		if item.Kind == "update" {
			kind = mutation.OperationUpdate
			if item.PrimaryKey == nil || item.ExpectedDigest == nil {
				return request, pluginDataError("plugin_guard_required")
			}
		} else if item.Kind != "create" || item.PrimaryKey != nil || item.ExpectedDigest != nil {
			return request, pluginDataError("plugin_capability_invalid")
		}
		request.Operations = append(request.Operations, mutation.Operation{Kind: kind, RecordID: item.PrimaryKey, ExpectedDigest: item.ExpectedDigest, Values: item.Values})
	}
	request.Authorize = func(ctx context.Context, tx core.App) error {
		installed, err := surfaceInstallation(ctx, tx, workspace, input)
		if err != nil {
			return err
		}
		table, err := schemaexecution.Describe(ctx, tx, plan.Collection)
		if err != nil {
			return pluginDataError("plugin_write_denied")
		}
		for _, operation := range request.Operations {
			name := "create"
			if operation.Kind == mutation.OperationUpdate {
				name = "update"
			}
			var grant *pluginReadGrant
			for _, candidate := range installed.Manifest.Permissions.Data {
				if (candidate.Collection == plan.Collection || candidate.Collection == "$active" && plan.Collection == input.ActiveCollection) && pluginHas(candidate.Operations, name) {
					copy := candidate
					grant = &copy
					break
				}
			}
			if grant == nil {
				return pluginDataError("plugin_write_denied")
			}
			allowed := pluginAllowed(*grant, table.Snapshot)
			for key := range operation.Values {
				field, found := table.Field(key)
				if !found || allowed[field.Identity.FieldID] == "" {
					return pluginDataError("plugin_write_denied")
				}
				if field.Relation != nil && installed.grant(field.Relation.TargetTableID, input.ActiveCollection) == nil {
					return pluginDataError("plugin_write_denied")
				}
			}
		}
		return nil
	}
	if err := request.Authorize(ctx, app); err != nil {
		return request, err
	}
	return request, nil
}
