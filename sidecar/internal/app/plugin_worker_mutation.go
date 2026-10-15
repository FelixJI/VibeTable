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
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const pluginWorkerMutationPath = "/api/vibetable/v2/plugins/worker/mutation"

// Only the host executor supplies this envelope. A Worker returns a business
// plan, never these creation identities; no renderer registration exists.
type pluginWorkerMutationRequest struct {
	ProjectKey           string           `json:"projectKey"`
	PluginID             string           `json:"pluginId"`
	InstallationID       string           `json:"installationId"`
	InstallationRevision int64            `json:"installationRevision"`
	PackageHash          string           `json:"packageHash"`
	ActionID             string           `json:"actionId"`
	SessionEpoch         uint64           `json:"sessionEpoch"`
	ActiveCollection     string           `json:"activeCollection"`
	Request              mutation.Request `json:"request"`
}

func registerPluginWorkerMutationRoutes(r *router.Router[*core.RequestEvent], app core.App, kernel *mutation.Kernel, workspace string, epoch uint64, gate businessWriteGate) {
	r.POST(pluginWorkerMutationPath, func(event *core.RequestEvent) error {
		raw, err := io.ReadAll(io.LimitReader(event.Request.Body, maxMutationRequestBytes+1))
		var input pluginWorkerMutationRequest
		if err != nil || len(raw) > maxMutationRequestBytes || decodePluginParams(raw, &input) != nil || epoch == 0 || input.SessionEpoch != epoch {
			return event.JSON(http.StatusBadRequest, map[string]any{"code": "plugin_capability_invalid"})
		}
		receipt, err := executePluginWorkerMutation(event.Request.Context(), app, kernel, workspace, input, gate)
		if err != nil {
			code := "plugin_request_failed"
			var denied *query.ProductError
			var product *mutation.ProductError
			if errors.As(err, &denied) {
				code = denied.Code
			}
			if errors.As(err, &product) {
				code = product.Code
			}
			outcome := pluginCommitOutcome(event.Request.Context(), app, workspace, "plugin.worker.mutation", input.Request.IdempotencyKey, receipt, code)
			if outcome == "unknown" {
				code = "plugin_commit_unknown"
			}
			return event.JSON(http.StatusConflict, map[string]any{"code": code, "message": "plugin mutation could not be completed", "details": map[string]any{"commitOutcome": outcome}})
		}
		return event.JSON(http.StatusOK, receipt)
	})
}

func executePluginWorkerMutation(ctx context.Context, app core.App, kernel *mutation.Kernel, workspace string, input pluginWorkerMutationRequest, gate businessWriteGate) (mutation.Receipt, error) {
	request := input.Request
	request.Actor = mutation.Actor{Type: "plugin", ID: input.PluginID}
	request.Authorize = func(ctx context.Context, tx core.App) error {
		raw, err := pluginstore.New(tx, workspace).GetInstallation(ctx, input.ProjectKey, input.PluginID)
		var installed pluginSurfaceInstallation
		if err != nil || json.Unmarshal(raw, &installed) != nil || installed.Status != "enabled" || input.InstallationID == "" || installed.InstallationID != input.InstallationID || installed.Revision != input.InstallationRevision || strings.TrimPrefix(installed.PackageHash, "sha256:") != strings.TrimPrefix(input.PackageHash, "sha256:") {
			return pluginDataError("plugin_cursor_stale")
		}
		if !pluginHas([]string{"1.x", "2.x"}, installed.Manifest.Compatibility.PluginAPI) {
			return pluginDataError("plugin_api_unsupported")
		}
		var declaration struct {
			Manifest struct {
				Actions []struct {
					ActionID    string `json:"actionId"`
					WorkerEntry string `json:"workerEntry"`
					Risk        string `json:"risk"`
				} `json:"actions"`
			} `json:"manifest"`
		}
		if json.Unmarshal(raw, &declaration) != nil {
			return pluginDataError("plugin_write_denied")
		}
		granted := false
		for _, action := range declaration.Manifest.Actions {
			if action.ActionID == input.ActionID && action.WorkerEntry != "" && pluginHas([]string{"write", "destructive"}, action.Risk) {
				granted = true
			}
		}
		if !granted {
			return pluginDataError("plugin_write_denied")
		}
		table, err := schemaexecution.Describe(ctx, tx, request.TableID)
		if err != nil {
			return pluginDataError("plugin_write_denied")
		}
		for _, operation := range request.Operations {
			name := "create"
			if operation.Kind == mutation.OperationUpdate {
				name = "update"
			} else if operation.Kind != mutation.OperationInsert {
				return pluginDataError("plugin_write_denied")
			}
			var grant *pluginReadGrant
			for _, candidate := range installed.Manifest.Permissions.Data {
				if (candidate.Collection == request.TableID || candidate.Collection == "$active" && request.TableID == input.ActiveCollection) && pluginHas(candidate.Operations, name) {
					copy := candidate
					grant = &copy
					break
				}
			}
			if grant == nil {
				return pluginDataError("plugin_write_denied")
			}
			allowed := pluginAllowed(*grant, table.Snapshot)
			if operation.Kind == mutation.OperationUpdate && allowed["id"] == "" {
				return pluginDataError("plugin_write_denied")
			}
			for key := range operation.Values {
				field, found := table.Field(key)
				if !found || allowed[field.Identity.FieldID] == "" || field.Relation != nil && installed.grant(field.Relation.TargetTableID, input.ActiveCollection) == nil {
					return pluginDataError("plugin_write_denied")
				}
			}
		}
		return nil
	}
	var receipt mutation.Receipt
	err := runBusinessWrite(ctx, []businessWriteGate{gate}, "plugin.worker.mutation", request.IdempotencyKey, func(ctx context.Context) error {
		var err error
		receipt, err = kernel.Apply(mutation.WithBusinessReplay(ctx, "plugin.worker.mutation"), request)
		return err
	})
	return receipt, err
}

// A rejection is definitive only when there is no committed/replayed receipt.
// Keep the same conservative acknowledgement boundary as native surfaces.
func pluginCommitOutcome(ctx context.Context, app core.App, workspace, kind, id string, result any, code string) string {
	if !surfaceCommitRejection(code) {
		return "unknown"
	}
	if receipt, ok := result.(mutation.Receipt); ok && receipt.Status != "" {
		return "unknown"
	}
	proofContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	committed, err := writecoordinator.HasPocketBaseReceiptIdentity(proofContext, app, workspace, kind, id)
	if err != nil || committed {
		return "unknown"
	}
	return "not-committed"
}
