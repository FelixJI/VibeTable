package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const pluginSurfaceAPIPath = "/api/vibetable/v3/plugins/surface"

var pluginSurfaceStorageKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// This seam is session-authenticated Host-only; no Product RPC registration exists.
type pluginSurfaceAPIRequest struct {
	ActionID             string                            `json:"actionId"`
	Entry                string                            `json:"entry"`
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
	InvocationContext    json.RawMessage                   `json:"invocationContext,omitempty"`
}

type pluginSurfaceInstallation struct {
	pluginInstallation
	PackageHash string `json:"packageHash"`
	Version     string `json:"version"`
	ActionRisk  string `json:"-"`
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
	var native struct {
		Manifest struct {
			Actions []struct {
				ActionID     string `json:"actionId"`
				Risk         string `json:"risk"`
				Presentation string `json:"presentation"`
				Entry        string `json:"entry"`
			} `json:"actions"`
			UI struct {
				CustomViews []struct {
					ActionID string `json:"actionId"`
					Entry    string `json:"entry"`
				} `json:"customViews"`
			} `json:"ui"`
		} `json:"manifest"`
	}
	if json.Unmarshal(raw, &native) != nil {
		return installed, pluginDataError("plugin_capability_invalid")
	}
	bound := false
	for _, view := range native.Manifest.UI.CustomViews {
		if view.ActionID == input.ActionID && view.Entry == input.Entry && input.Entry != "" {
			bound = true
		}
	}
	for _, action := range native.Manifest.Actions {
		if action.ActionID != input.ActionID {
			continue
		}
		// application/headless actions bind through the presentation entry
		// declared on the action itself instead of a legacy custom view.
		if action.Presentation == "application" || action.Presentation == "headless" {
			if action.Entry != "" && action.Entry == input.Entry {
				bound = true
			} else {
				bound = false
			}
		}
		if bound && pluginHas([]string{"read", "write", "destructive"}, action.Risk) {
			installed.ActionRisk = action.Risk
			return installed, nil
		}
	}
	return installed, pluginDataError("plugin_capability_invalid")
}

func registerPluginSurfaceAPIRoutes(r *router.Router[*core.RequestEvent], app core.App, source *queryschema.Source, kernel *mutation.Kernel, workspace string, epoch uint64, gate businessWriteGate) {
	r.POST(pluginSurfaceAPIPath, func(event *core.RequestEvent) error {
		raw, err := io.ReadAll(io.LimitReader(event.Request.Body, maxMutationRequestBytes+1))
		var input pluginSurfaceAPIRequest
		if err != nil || len(raw) > maxMutationRequestBytes || decodePluginParams(raw, &input) != nil || input.SessionEpoch != epoch || epoch == 0 || input.RequestID == "" {
			return event.JSON(http.StatusBadRequest, map[string]any{"code": "plugin_capability_invalid"})
		}
		budget := 15 * time.Second
		if input.Method == "mutation.commit" {
			budget = 120 * time.Second
		}
		ctx, cancel := context.WithTimeout(event.Request.Context(), budget)
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
			failure := map[string]any{"code": code, "message": "plugin request could not be completed"}
			if input.Method == "mutation.commit" {
				outcome := surfaceCommitOutcome(ctx, app, workspace, input, result, code)
				failure["commitOutcome"] = outcome
				if outcome == "unknown" {
					failure["code"] = "plugin_commit_unknown"
				}
			}
			return event.JSON(http.StatusConflict, failure)
		}
		return event.JSON(http.StatusOK, result)
	})
}

func surfaceCommitRejection(code string) bool {
	switch code {
	case "plugin_read_denied", "plugin_write_denied", "plugin_cursor_stale", "plugin_api_unsupported", "plugin_capability_invalid", "plugin_guard_required", "plugin_audit_invalid",
		"mutation.digest_conflict", "mutation.revision_conflict", "mutation.schema_revision_conflict", "mutation.idempotency_conflict":
		return true
	default:
		return false
	}
}

// Error type/status alone cannot establish rollback: the workspace writer can
// fail after PB committed. Only an exact rejection plus absent recovery proof can.
func surfaceCommitOutcome(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest, result any, code string) string {
	if !surfaceCommitRejection(code) {
		return "unknown"
	}
	if receipt, ok := result.(mutation.Receipt); ok && receipt.Status != "" {
		return "unknown"
	}
	proofContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	// The route already validates the current session. A completion from any
	// session or imported snapshot is stronger evidence than a current receipt.
	committed, err := writecoordinator.HasPocketBaseReceiptIdentity(proofContext, app, workspace, "plugin.surface.mutation", input.RequestID)
	if err != nil || committed {
		return "unknown"
	}
	return "not-committed"
}

func executePluginSurfaceAPI(ctx context.Context, app core.App, source *queryschema.Source, kernel *mutation.Kernel, workspace string, input pluginSurfaceAPIRequest, gate businessWriteGate) (any, error) {
	if input.Method == "audit.finish" {
		var terminal struct {
			Outcome   string `json:"outcome"`
			ErrorCode string `json:"errorCode"`
		}
		if decodePluginParams(input.Request, &terminal) != nil || !pluginHas([]string{"rejected", "cancelled", "failed", "unknown"}, terminal.Outcome) {
			return nil, pluginDataError("plugin_audit_invalid")
		}
		return nil, runBusinessWrite(ctx, []businessWriteGate{gate}, "plugin.surface.audit.finish", input.RequestID+":"+terminal.Outcome, func(ctx context.Context) error {
			return surfaceAuditFinish(ctx, app, workspace, input, terminal.Outcome, terminal.ErrorCode)
		})
	}
	installed, err := surfaceInstallation(ctx, app, workspace, input)
	if err != nil {
		return nil, err
	}
	switch input.Method {
	case "audit.start":
		if installed.ActionRisk == "read" {
			return nil, pluginDataError("plugin_write_denied")
		}
		var start struct {
			TaskID string          `json:"taskId"`
			Plan   json.RawMessage `json:"plan"`
		}
		if decodePluginParams(input.Request, &start) != nil || start.TaskID == "" {
			return nil, pluginDataError("plugin_audit_invalid")
		}
		planInput := input
		planInput.Request = start.Plan
		request, err := surfaceMutationRequest(ctx, app, workspace, planInput)
		if err != nil {
			return nil, err
		}
		if _, err = kernel.Preview(ctx, request); err != nil {
			return nil, err
		}
		now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
		event, _ := json.Marshal(map[string]any{"eventId": input.RequestID + ":started", "projectKey": input.ProjectKey, "pluginId": input.PluginID, "pluginVersion": installed.Version, "packageHash": installed.PackageHash,
			"eventType": "action.started", "outcome": "unknown", "actionId": input.ActionID, "runId": input.RequestID, "actor": "local-user", "risk": installed.ActionRisk, "targetCollection": request.TableID, "targetCount": len(request.Operations), "startedAt": now, "finishedAt": nil, "durationMs": nil, "errorCode": nil,
			"details": map[string]any{"taskId": start.TaskID, "state": "running", "installationRevision": input.InstallationRevision, "sessionEpoch": input.SessionEpoch, "plan": start.Plan, "entry": input.Entry}})
		err = runBusinessWrite(ctx, []businessWriteGate{gate}, "plugin.surface.audit.start", input.RequestID+":started", func(ctx context.Context) error {
			if _, err := surfaceInstallation(ctx, app, workspace, input); err != nil {
				return err
			}
			_, err := pluginstore.New(app, workspace).RecordAudit(ctx, event)
			return err
		})
		return nil, err
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
		return surfaceCommandContext(ctx, app, source, installed, input)
	case "storage.get", "storage.set", "storage.delete":
		var request struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value,omitempty"`
		}
		if decodePluginParams(input.Request, &request) != nil || !pluginSurfaceStorageKey.MatchString(request.Key) || !surfaceStorageValueFits(request.Value) || input.Method != "storage.set" && request.Value != nil || input.Method == "storage.set" && request.Value == nil {
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
		if installed.ActionRisk == "read" {
			return nil, pluginDataError("plugin_write_denied")
		}
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
		start, err := surfaceAuditStarted(ctx, app, workspace, input)
		if err != nil {
			return nil, err
		}
		var recorded, submitted any
		recordedDecoder := json.NewDecoder(bytes.NewReader(start["details"].(map[string]any)["plan"].(json.RawMessage)))
		recordedDecoder.UseNumber()
		submittedDecoder := json.NewDecoder(bytes.NewReader(input.Request))
		submittedDecoder.UseNumber()
		if recordedDecoder.Decode(&recorded) != nil || submittedDecoder.Decode(&submitted) != nil {
			return nil, pluginDataError("plugin_audit_invalid")
		}
		if !reflect.DeepEqual(recorded, submitted) {
			return nil, pluginDataError("plugin_audit_invalid")
		}
		err = runBusinessWrite(ctx, []businessWriteGate{gate}, "plugin.surface.mutation", input.RequestID, func(ctx context.Context) error {
			var applyErr error
			receipt, applyErr = kernel.ApplyWithCommit(mutation.WithBusinessReplay(ctx, "plugin.surface.mutation"), request, func(tx core.App, receipt mutation.Receipt) error {
				return surfaceAuditFinish(ctx, tx, workspace, input, "succeeded", "")
			})
			return applyErr
		})
		return receipt, err
	default:
		return nil, pluginDataError("plugin_api_unsupported")
	}
}

func surfaceStorageValueFits(raw json.RawMessage) bool {
	if raw == nil {
		return true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value) == nil && encoded.Len()-1 <= 65536
}

func surfaceAuditStarted(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest) (map[string]any, error) {
	raw, err := pluginstore.New(app, workspace).GetAudit(ctx, input.ProjectKey, input.PluginID, input.RequestID+":started")
	if err != nil {
		return nil, err
	}
	var event map[string]json.RawMessage
	if json.Unmarshal(raw, &event) != nil {
		return nil, pluginDataError("plugin_audit_invalid")
	}
	var hash, action, run, eventType, risk string
	_ = json.Unmarshal(event["packageHash"], &hash)
	_ = json.Unmarshal(event["actionId"], &action)
	_ = json.Unmarshal(event["runId"], &run)
	_ = json.Unmarshal(event["eventType"], &eventType)
	_ = json.Unmarshal(event["risk"], &risk)
	var details map[string]json.RawMessage
	_ = json.Unmarshal(event["details"], &details)
	var revision int64
	var entry string
	_ = json.Unmarshal(details["entry"], &entry)
	var epoch uint64
	_ = json.Unmarshal(details["installationRevision"], &revision)
	_ = json.Unmarshal(details["sessionEpoch"], &epoch)
	if strings.TrimPrefix(hash, "sha256:") != input.PackageHash || action != input.ActionID || run != input.RequestID || eventType != "action.started" || !pluginHas([]string{"write", "destructive"}, risk) || entry != input.Entry || revision != input.InstallationRevision || epoch != input.SessionEpoch {
		return nil, pluginDataError("plugin_audit_invalid")
	}
	result := map[string]any{}
	for key, value := range event {
		result[key] = value
	}
	result["details"] = map[string]any{"plan": details["plan"], "taskId": details["taskId"]}
	return result, nil
}

func surfaceAuditFinish(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest, outcome, code string) error {
	start, err := surfaceAuditStarted(ctx, app, workspace, input)
	if err != nil {
		return err
	}
	var started time.Time
	var timestamp string
	_ = json.Unmarshal(start["startedAt"].(json.RawMessage), &timestamp)
	started, _ = time.Parse(time.RFC3339, timestamp)
	now := time.Now().UTC()
	start["eventId"] = input.RequestID + ":terminal"
	start["eventType"] = "action"
	start["outcome"] = outcome
	if outcome == "unknown" {
		start["eventId"] = input.RequestID + ":observed"
		start["eventType"] = "action.observed"
	}
	start["finishedAt"] = now.Truncate(time.Second).Format(time.RFC3339)
	start["durationMs"] = max(0, now.Sub(started).Milliseconds())
	start["errorCode"] = nil
	if code != "" {
		start["errorCode"] = code
	}
	details := start["details"].(map[string]any)
	delete(details, "plan")
	details["state"] = outcome
	raw, _ := json.Marshal(start)
	store := pluginstore.New(app, workspace)
	if outcome == "succeeded" {
		_, err = store.RecordAuditInTransaction(ctx, raw)
	} else {
		_, err = store.RecordAudit(ctx, raw)
	}
	return err
}

// Invocation data is supplied only by Host; business identities and predicates
// still require the installation's current canonical field grants.
func surfaceCommandContext(ctx context.Context, app core.App, source *queryschema.Source, installed pluginSurfaceInstallation, input pluginSurfaceAPIRequest) (map[string]any, error) {
	var invocation struct {
		SelectedKeys  []any           `json:"selectedKeys"`
		QuerySnapshot json.RawMessage `json:"querySnapshot"`
		User          map[string]any  `json:"user"`
	}
	if len(input.InvocationContext) != 0 && decodePluginParams(input.InvocationContext, &invocation) != nil {
		return nil, pluginDataError("plugin_capability_invalid")
	}
	selected := []any{}
	var snapshot any
	var active any
	grant := installed.grant(input.ActiveCollection, input.ActiveCollection)
	if grant != nil {
		active = input.ActiveCollection
		_, schema, err := source.DescribeSelectionTable(ctx, app, input.ActiveCollection)
		if err != nil {
			return nil, pluginDataError("plugin_query_failed")
		}
		allowed := pluginAllowed(*grant, schema)
		if allowed["id"] != "" {
			selected = append(selected, invocation.SelectedKeys...)
		}
		var candidate query.QuerySnapshot
		if len(invocation.QuerySnapshot) != 0 && string(invocation.QuerySnapshot) != "null" && decodePluginParams(invocation.QuerySnapshot, &candidate) == nil && candidate.Table == input.ActiveCollection {
			aliases := map[string]string{}
			for id, name := range allowed {
				aliases[id] = id
				aliases[name] = id
			}
			valid := candidate.NormalizedQuery.Keyword == "" || len(allowed) == len(schema.Fields)+1
			check := func(field string) (string, bool) {
				id, ok := aliases[field]
				if !ok {
					return "", false
				}
				for _, field := range schema.Fields {
					if field.Identity.FieldID == id && field.Relation != nil && installed.grant(field.Relation.TargetTableID, input.ActiveCollection) == nil {
						return "", false
					}
				}
				return id, true
			}
			var walk func([]query.FilterExpression)
			walk = func(filters []query.FilterExpression) {
				for i := range filters {
					if len(filters[i].Filters) != 0 {
						walk(filters[i].Filters)
						continue
					}
					id, ok := check(filters[i].Field)
					valid = valid && ok
					filters[i].Field = id
				}
			}
			walk(candidate.NormalizedQuery.Filters)
			for i := range candidate.NormalizedQuery.Sorts {
				id, ok := check(candidate.NormalizedQuery.Sorts[i].Field)
				valid = valid && ok
				candidate.NormalizedQuery.Sorts[i].Field = id
			}
			if valid {
				snapshot = map[string]any{"table": input.ActiveCollection, "schemaRevision": candidate.SchemaRevision, "dataRevision": candidate.DataRevision, "normalizedQuery": candidate.NormalizedQuery}
			}
		}
	}
	if invocation.User == nil {
		invocation.User = map[string]any{}
	}
	return map[string]any{"contract": "vibetable.command-context.v1", "projectKey": input.ProjectKey, "collection": active, "selectedKeys": selected, "querySnapshot": snapshot, "locale": "zh-CN", "theme": "light", "density": "comfortable", "user": invocation.User, "hostVersion": ""}, nil
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
		if installed.ActionRisk != "write" && installed.ActionRisk != "destructive" {
			return pluginDataError("plugin_write_denied")
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
			if operation.Kind == mutation.OperationUpdate && allowed["id"] == "" {
				return pluginDataError("plugin_write_denied")
			}
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
