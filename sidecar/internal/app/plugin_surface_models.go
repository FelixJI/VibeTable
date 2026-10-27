package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type modelStep struct {
	Kind        string `json:"kind"`
	Entity      string `json:"entity"`
	Field       string `json:"field,omitempty"`
	OperationID string `json:"operationId"`
}
type modelPreview struct {
	pluginmodel.State
	Mode        string          `json:"mode"`
	ExpiresAt   time.Time       `json:"expiresAt"`
	Steps       []modelStep     `json:"steps"`
	Diagnostics []v2.Diagnostic `json:"diagnostics"`
}
type modelPreviewRequest struct {
	Mode     string                         `json:"mode"`
	Mappings map[string]pluginmodel.Binding `json:"mappings,omitempty"`
}

func resolveModelGrants(ctx context.Context, app core.App, project, plugin string, installed *pluginInstallation) error {
	if installed.Manifest.Permissions.Model == nil {
		return nil
	}
	if err := installed.Manifest.Permissions.Model.Validate(); err != nil {
		return pluginDataError("plugin_model_invalid")
	}
	states, err := pluginmodel.States(ctx, app, project, plugin)
	if err != nil {
		return err
	}
	for i := len(states) - 1; i >= 0; i-- {
		s := states[i]
		if s.Status != "completed" || s.InstallationID != installed.InstallationID || s.InstallationRevision != installed.Revision || !pluginmodel.Equal(s.Model, *installed.Manifest.Permissions.Model) {
			continue
		}
		if err := modelValidateBindings(ctx, app, s); err != nil {
			return err
		}
		for _, e := range s.Model.Entities {
			b := s.Bindings[e.Key]
			fields := []string{"id"}
			for _, f := range e.Fields {
				fields = append(fields, b.Fields[f.Key])
			}
			installed.ModelGrants = append(installed.ModelGrants, pluginReadGrant{Collection: b.TableID, Fields: fields, Operations: e.Operations})
		}
		break
	}
	return nil
}

func executeSurfaceModel(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest, domain *schemaFieldChangeDomain) (any, error) {
	installed, err := surfaceInstallation(ctx, app, workspace, input)
	if err != nil {
		return nil, err
	}
	model := installed.Manifest.Permissions.Model
	if model == nil || model.Validate() != nil {
		return nil, pluginDataError("plugin_model_denied")
	}
	states, err := pluginmodel.States(ctx, app, input.ProjectKey, input.PluginID)
	if err != nil {
		return nil, err
	}
	switch input.Method {
	case "model.read", "model.status":
		var request struct {
			RequestID string `json:"requestId,omitempty"`
		}
		if decodePluginParams(input.Request, &request) != nil || input.Method == "model.status" && request.RequestID == "" {
			return nil, pluginDataError("plugin_capability_invalid")
		}
		for i := len(states) - 1; i >= 0; i-- {
			s := states[i]
			if s.Model.ModelID == model.ModelID && (request.RequestID == "" || s.RequestID == request.RequestID) && s.InstallationID == input.InstallationID {
				if s.InstallationRevision != input.InstallationRevision || !pluginmodel.Equal(s.Model, *model) {
					if input.Method == "model.read" {
						break
					}
					return nil, pluginDataError("plugin_plan_stale")
				}
				return modelPublicState(s), nil
			}
		}
		return map[string]any{"contract": pluginmodel.Contract, "status": "unconfigured", "bindings": map[string]any{}}, nil
	case "model.catalog":
		// This method is Host-only. The frame API never forwards it.
		tables, err := schemaapi.New(app).List(ctx)
		if err != nil {
			return nil, err
		}
		catalog := []v2.SchemaSnapshot{}
		for _, t := range tables {
			if t.Snapshot.Kind == "base" {
				catalog = append(catalog, t.Snapshot)
			}
		}
		return map[string]any{"model": model, "tables": catalog}, nil
	case "model.preview":
		if installed.ActionRisk == "read" {
			return nil, pluginDataError("plugin_write_denied")
		}
		if domain == nil {
			return nil, pluginDataError("plugin_api_unsupported")
		}
		var request modelPreviewRequest
		if decodePluginParams(input.Request, &request) != nil || (request.Mode != "create" && request.Mode != "map") {
			return nil, pluginDataError("plugin_capability_invalid")
		}
		return previewSurfaceModel(ctx, app, workspace, input, installed, request, states, domain)
	case "model.commit":
		if installed.ActionRisk == "read" {
			return nil, pluginDataError("plugin_write_denied")
		}
		if domain == nil {
			return nil, pluginDataError("plugin_api_unsupported")
		}
		var request struct {
			RequestID string `json:"requestId"`
		}
		if decodePluginParams(input.Request, &request) != nil || request.RequestID == "" {
			return nil, pluginDataError("plugin_capability_invalid")
		}
		return commitSurfaceModel(ctx, app, workspace, input, installed, request.RequestID, domain)
	}
	return nil, pluginDataError("plugin_capability_invalid")
}

func previewSurfaceModel(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest, installed pluginSurfaceInstallation, request modelPreviewRequest, states []pluginmodel.State, domain *schemaFieldChangeDomain) (modelPreview, error) {
	model := *installed.Manifest.Permissions.Model
	plan := modelPreview{State: pluginmodel.State{Contract: pluginmodel.Contract, RequestID: security.RandomString(24), InstallationID: input.InstallationID, InstallationRevision: input.InstallationRevision, Model: model, Status: "pending", Bindings: map[string]pluginmodel.Binding{}, Revisions: map[string]string{}}, Mode: request.Mode, ExpiresAt: time.Now().UTC().Add(30 * time.Minute), Steps: []modelStep{}, Diagnostics: []v2.Diagnostic{}}
	diagnose := func(code, path string) {
		plan.Diagnostics = append(plan.Diagnostics, v2.Diagnostic{Code: code, Path: path, Message: code, Details: map[string]any{}})
	}
	for i := len(states) - 1; i >= 0; i-- {
		s := states[i]
		if s.Model.ModelID != model.ModelID {
			continue
		}
		if model.Version < s.Model.Version || model.Version == s.Model.Version && !pluginmodel.Equal(model, s.Model) {
			return plan, pluginDataError("plugin_model_version_conflict")
		}
		if s.InstallationID == input.InstallationID && s.InstallationRevision == input.InstallationRevision && pluginmodel.Equal(s.Model, model) {
			if s.Status == "completed" {
				plan.State = s
				return plan, nil
			}
			// Resuming uses the original accepted plan and canonical identities.
			prior, err := loadModelPreview(ctx, app, workspace, input, s.RequestID)
			if err != nil {
				return plan, err
			}
			prior.State = s
			if err := modelValidateRevisions(ctx, app, s); err != nil {
				prior.Status = "blocked"
				prior.Diagnostics = []v2.Diagnostic{{Code: "plugin_plan_stale", Path: "schemaRevision", Message: "schema changed after the accepted plan", Details: map[string]any{}}}
			}
			return prior, nil
		}
		// A previous revision may have stopped before all planned tables existed.
		// Only transactionally persisted tables can be carried into a new plan.
		// Missing previously persisted tables remain bound and fail diagnostics;
		// they must never be silently recreated after deletion or corruption.
		plan.Bindings = modelPublicState(s).Bindings
		break
	}
	tables, err := schemaapi.New(app).List(ctx)
	if err != nil {
		return plan, err
	}
	byID := map[string]v2.SchemaSnapshot{}
	names := map[string]bool{}
	for _, t := range tables {
		byID[t.Snapshot.TableID] = t.Snapshot
		names[t.Snapshot.DisplayName] = true
	}
	entityKeys := map[string]bool{}
	usedTables := map[string]bool{}
	for _, e := range model.Entities {
		entityKeys[e.Key] = true
		b, bound := plan.Bindings[e.Key]
		if mapped, ok := request.Mappings[e.Key]; ok {
			if bound && !reflect.DeepEqual(mapped, b) {
				diagnose("plugin_model_binding_conflict", e.Key)
			} else {
				b = mapped
			}
		}
		if b.TableID == "" {
			if request.Mode == "map" {
				diagnose("plugin_model_mapping_required", e.Key)
				continue
			}
			if names[e.Label] {
				diagnose("plugin_model_name_conflict", e.Key)
			}
			op := "model-" + plan.RequestID + "-" + e.Key
			b = pluginmodel.Binding{TableID: schemacore.PreviewTableID(op), Fields: map[string]string{}}
			plan.Steps = append(plan.Steps, modelStep{Kind: "table", Entity: e.Key, OperationID: op})
		} else {
			schema, ok := byID[b.TableID]
			if !ok || schema.Kind != "base" {
				diagnose("plugin_model_table_missing", e.Key)
			} else {
				plan.Revisions[b.TableID] = schema.SchemaRevision
			}
		}
		if usedTables[b.TableID] {
			diagnose("plugin_model_duplicate_mapping", e.Key)
		}
		usedTables[b.TableID] = true
		if b.Fields == nil {
			b.Fields = map[string]string{}
		}
		plan.Bindings[e.Key] = b
	}
	for key := range request.Mappings {
		if !entityKeys[key] {
			diagnose("plugin_model_mapping_unknown", key)
		}
	}
	for key := range plan.Bindings {
		if !entityKeys[key] {
			delete(plan.Bindings, key)
		}
	}
	tableIDs := modelTableIDs(plan.State)
	seenPairs := map[string]bool{}
	for _, e := range model.Entities {
		b := plan.Bindings[e.Key]
		used := map[string]bool{}
		keys := map[string]bool{}
		for _, f := range e.Fields {
			keys[f.Key] = true
			path := e.Key + "." + f.Key
			id := b.Fields[f.Key]
			if id != "" {
				if used[id] {
					diagnose("plugin_model_duplicate_mapping", path)
				}
				used[id] = true
				found := false
				for _, existing := range byID[b.TableID].Fields {
					if existing.Identity.FieldID == id {
						found = true
						if code := f.Compatibility(existing, tableIDs); code != "" {
							diagnose(code, path)
						}
						if f.Relation != nil && (existing.Relation == nil || existing.Relation.ReciprocalFieldID != plan.Bindings[f.Relation.Entity].Fields[f.Relation.Field]) {
							diagnose("plugin_model_relation_mismatch", path)
						}
					}
				}
				if !found {
					diagnose("plugin_model_field_missing", path)
				}
				continue
			}
			if request.Mode == "map" {
				diagnose("plugin_model_mapping_required", path)
				continue
			}
			for _, existing := range byID[b.TableID].Fields {
				if existing.DisplayName == f.Label {
					diagnose("plugin_model_name_conflict", path)
				}
			}
			if f.Relation != nil {
				other := f.Relation.Entity + "." + f.Relation.Field
				if seenPairs[path] {
					continue
				}
				seenPairs[path], seenPairs[other] = true, true
				if plan.Bindings[f.Relation.Entity].Fields[f.Relation.Field] != "" {
					diagnose("plugin_model_relation_mismatch", path)
				}
				continue
			}
			plan.Steps = append(plan.Steps, modelStep{Kind: "field", Entity: e.Key, Field: f.Key, OperationID: "model-" + plan.RequestID + "-" + path})
		}
		for key := range b.Fields {
			if !keys[key] {
				if _, specified := request.Mappings[e.Key].Fields[key]; specified {
					diagnose("plugin_model_mapping_unknown", e.Key+"."+key)
				}
				delete(b.Fields, key)
			}
		}
	}
	seenPairs = map[string]bool{}
	for _, e := range model.Entities {
		for _, f := range e.Fields {
			if f.Relation != nil && plan.Bindings[e.Key].Fields[f.Key] == "" && !seenPairs[e.Key+"."+f.Key] {
				seenPairs[e.Key+"."+f.Key], seenPairs[f.Relation.Entity+"."+f.Relation.Field] = true, true
				plan.Steps = append(plan.Steps, modelStep{Kind: "pair", Entity: e.Key, Field: f.Key, OperationID: "model-" + plan.RequestID + "-" + e.Key + "." + f.Key})
			}
		}
	}
	plan.Total = len(plan.Steps)
	if len(plan.Diagnostics) > 0 {
		plan.Status = "blocked"
		return plan, nil
	}
	err = runBusinessWrite(ctx, domain.gates, "plugin.model.preview", plan.RequestID, func(ctx context.Context) error {
		if _, err := surfaceInstallation(ctx, app, workspace, input); err != nil {
			return err
		}
		raw := modelAudit(input, installed, plan.RequestID+":preview", "model.preview", plan)
		_, err := pluginstore.New(app, workspace).RecordAudit(ctx, raw)
		return err
	})
	return plan, err
}

func modelAudit(input pluginSurfaceAPIRequest, installed pluginSurfaceInstallation, id, kind string, detail any) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"eventId": id, "projectKey": input.ProjectKey, "pluginId": input.PluginID, "pluginVersion": installed.Version, "packageHash": installed.PackageHash, "eventType": kind, "outcome": "succeeded", "actor": "local-user", "startedAt": time.Now().UTC().Truncate(time.Second).Format(time.RFC3339), "details": detail})
	return raw
}
func loadModelPreview(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest, id string) (modelPreview, error) {
	raw, err := pluginstore.New(app, workspace).GetAudit(ctx, input.ProjectKey, input.PluginID, id+":preview")
	var event struct {
		Details modelPreview `json:"details"`
	}
	if err != nil {
		return event.Details, err
	}
	if len(raw) == 0 || json.Unmarshal(raw, &event) != nil {
		return event.Details, pluginDataError("plugin_plan_stale")
	}
	return event.Details, nil
}
func modelTableIDs(s pluginmodel.State) map[string]string {
	out := map[string]string{}
	for k, b := range s.Bindings {
		out[k] = b.TableID
	}
	return out
}
func modelField(d pluginmodel.Declaration, entity, field string) pluginmodel.Field {
	for _, e := range d.Entities {
		if e.Key == entity {
			for _, f := range e.Fields {
				if f.Key == field {
					return f
				}
			}
		}
	}
	return pluginmodel.Field{}
}
func modelLabel(d pluginmodel.Declaration, entity string) string {
	for _, e := range d.Entities {
		if e.Key == entity {
			return e.Label
		}
	}
	return ""
}
func modelDisplayField(s pluginmodel.State, entity string) string {
	for _, e := range s.Model.Entities {
		if e.Key == entity {
			for _, f := range e.Fields {
				if f.Type == v2.LogicalText {
					return s.Bindings[entity].Fields[f.Key]
				}
			}
			for _, f := range e.Fields {
				if f.Type != v2.LogicalRelation {
					return s.Bindings[entity].Fields[f.Key]
				}
			}
		}
	}
	return ""
}

func modelValidateRevisions(ctx context.Context, app core.App, s pluginmodel.State) error {
	for table, revision := range s.Revisions {
		if err := fieldchange.CheckTableWriteFence(ctx, app, table); err != nil {
			return err
		}
		current, err := schemaexecution.Describe(ctx, app, table)
		if err != nil || current.Snapshot.SchemaRevision != revision {
			return pluginDataError("plugin_plan_stale")
		}
	}
	return nil
}
func modelValidateBindings(ctx context.Context, app core.App, s pluginmodel.State) error {
	tables := modelTableIDs(s)
	for _, e := range s.Model.Entities {
		b := s.Bindings[e.Key]
		schema, err := schemaexecution.Describe(ctx, app, b.TableID)
		if err != nil {
			return pluginDataError("plugin_model_binding_stale")
		}
		for _, f := range e.Fields {
			found := false
			for _, actual := range schema.Snapshot.Fields {
				if actual.Identity.FieldID == b.Fields[f.Key] {
					found = true
					if f.Compatibility(actual, tables) != "" {
						return pluginDataError("plugin_model_binding_stale")
					}
					if f.Relation != nil && actual.Relation.ReciprocalFieldID != s.Bindings[f.Relation.Entity].Fields[f.Relation.Field] {
						return pluginDataError("plugin_model_binding_stale")
					}
				}
			}
			if !found {
				return pluginDataError("plugin_model_binding_stale")
			}
		}
	}
	return nil
}

func commitSurfaceModel(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest, installed pluginSurfaceInstallation, id string, domain *schemaFieldChangeDomain) (pluginmodel.State, error) {
	plan, err := loadModelPreview(ctx, app, workspace, input, id)
	if err != nil {
		return plan.State, err
	}
	if plan.InstallationID != input.InstallationID || plan.InstallationRevision != input.InstallationRevision || !pluginmodel.Equal(plan.Model, *installed.Manifest.Permissions.Model) {
		return plan.State, pluginDataError("plugin_plan_stale")
	}
	states, err := pluginmodel.States(ctx, app, input.ProjectKey, input.PluginID)
	if err != nil {
		return plan.State, err
	}
	started := false
	for _, s := range states {
		if s.RequestID == id {
			plan.State = s
			started = true
		}
	}
	if plan.Status == "completed" {
		return plan.State, nil
	}
	if !started && !time.Now().Before(plan.ExpiresAt) {
		return plan.State, pluginDataError("plugin_plan_stale")
	}
	save := func(ctx context.Context, tx core.App, s pluginmodel.State, suffix string) error {
		if _, err := surfaceInstallation(ctx, tx, workspace, input); err != nil {
			return err
		}
		_, err := pluginstore.New(tx, workspace).RecordAuditInTransaction(ctx, modelAudit(input, installed, id+":"+suffix, "model.progress", s))
		return err
	}
	if !started {
		err = runBusinessWrite(ctx, domain.gates, "plugin.model.accept", id, func(ctx context.Context) error {
			return app.RunInTransaction(func(tx core.App) error {
				if err := modelValidateRevisions(ctx, tx, plan.State); err != nil {
					return err
				}
				if err := save(ctx, tx, plan.State, "accepted"); err != nil {
					return err
				}
				return writecoordinator.PersistPocketBaseReceipt(ctx, tx, time.Now().UTC())
			})
		})
		if err != nil {
			return plan.State, err
		}
	}
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	for plan.Completed < len(plan.Steps) {
		if err = ctx.Err(); err != nil {
			break
		}
		if err = modelValidateRevisions(ctx, app, plan.State); err != nil {
			break
		}
		step := plan.Steps[plan.Completed]
		previousCompleted := plan.Completed
		// A step receipt and its logical binding are committed in the same schema transaction.
		project := func(tx core.App, next pluginmodel.State) error {
			if err := modelValidateRevisions(ctx, tx, next); err != nil {
				return err
			}
			next.Completed++
			next.Status = "partial"
			next.ErrorCode = ""
			if err := save(ctx, tx, next, fmt.Sprintf("step:%d", next.Completed)); err != nil {
				return err
			}
			plan.State = next
			return nil
		}
		if step.Kind == "table" {
			_, err = domain.CreateTableWithCommit(ctx, v2.TableCreateIntent{DisplayName: modelLabel(plan.Model, step.Entity), OperationID: step.OperationID, Actor: actor}, func(tx core.App, r v2.TableCreateReceipt) error {
				next := cloneModelState(plan.State)
				next.Revisions[r.TableID] = r.SchemaRevision
				return project(tx, next)
			})
		} else {
			f := modelField(plan.Model, step.Entity, step.Field)
			draft, draftErr := f.Draft(modelTableIDs(plan.State))
			if draftErr != nil {
				err = draftErr
				break
			}
			b := plan.Bindings[step.Entity]
			intent := v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: b.TableID, ExpectedSchemaRev: plan.Revisions[b.TableID], Draft: &draft, Actor: actor}
			if f.Relation != nil {
				reverse := modelField(plan.Model, f.Relation.Entity, f.Relation.Field)
				intent.Draft.Relation.DisplayField = modelDisplayField(plan.State, f.Relation.Entity)
				intent.RelationPair = &v2.RelationPairDraft{ReciprocalDisplayName: reverse.Label, ReciprocalCardinality: reverse.Relation.Cardinality, SourceDisplayFieldID: modelDisplayField(plan.State, step.Entity)}
			}
			var p v2.FieldChangePlan
			p, err = domain.Plan(ctx, intent)
			if err != nil {
				break
			}
			if !p.CanApply || p.CreatesMigration || len(p.Confirmations) > 0 {
				err = pluginDataError("plugin_model_schema_rejected")
				break
			}
			_, err = domain.ApplyWithCommit(ctx, v2.ApplyRequest{PlanID: p.PlanID, PlanHash: p.PlanHash, OperationID: step.OperationID, Actor: actor, Confirmations: []string{}}, func(tx core.App, r v2.ApplyReceipt) error {
				next := cloneModelState(plan.State)
				next.Bindings[step.Entity].Fields[step.Field] = r.FieldID
				next.Revisions[r.TableID] = r.SchemaRevision
				if f.Relation != nil {
					if len(r.Related) != 1 {
						return pluginDataError("plugin_model_schema_rejected")
					}
					next.Bindings[f.Relation.Entity].Fields[f.Relation.Field] = r.Related[0].FieldID
					next.Revisions[r.Related[0].TableID] = r.Related[0].SchemaRevision
				}
				return project(tx, next)
			})
		}
		if err != nil {
			break
		}
		if plan.Completed == previousCompleted {
			current, readErr := pluginmodel.States(ctx, app, input.ProjectKey, input.PluginID)
			if readErr != nil {
				err = readErr
				break
			}
			for _, s := range current {
				if s.RequestID == id && s.Completed > plan.Completed {
					plan.State = s
				}
			}
			if plan.Completed == previousCompleted {
				err = pluginDataError("plugin_model_progress_missing")
				break
			}
		}
	}
	if err != nil {
		// Read durable progress even when the writer failed after the business commit.
		recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		failureCode := "plugin_model_step_failed"
		var product *query.ProductError
		if errors.As(err, &product) {
			failureCode = product.Code
		}
		if errors.Is(err, context.Canceled) {
			failureCode = "plugin_cancel_requested"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			failureCode = "plugin_timeout"
		}
		_ = runBusinessWrite(recoveryContext, domain.gates, "plugin.model.failure", id, func(ctx context.Context) error {
			return app.RunInTransaction(func(tx core.App) error {
				// Read after admission and inside the transaction: another caller
				// may have committed more steps (or completed) while this one waited.
				current, readErr := pluginmodel.States(ctx, tx, input.ProjectKey, input.PluginID)
				if readErr != nil {
					return readErr
				}
				for _, s := range current {
					if s.RequestID == id {
						plan.State = s
					}
				}
				if plan.Status == "completed" {
					return nil
				}
				plan.ErrorCode = failureCode
				plan.Status = "partial"
				if plan.Completed == 0 {
					plan.Status = "failed"
				}
				if e := save(ctx, tx, plan.State, "failed:"+security.RandomString(12)); e != nil {
					return e
				}
				return writecoordinator.PersistPocketBaseReceipt(ctx, tx, time.Now().UTC())
			})
		})
		if plan.Status == "completed" {
			return plan.State, nil
		}
		return plan.State, err
	}
	err = runBusinessWrite(ctx, domain.gates, "plugin.model.complete", id, func(ctx context.Context) error {
		return app.RunInTransaction(func(tx core.App) error {
			if err := modelValidateRevisions(ctx, tx, plan.State); err != nil {
				return err
			}
			if err := modelValidateBindings(ctx, tx, plan.State); err != nil {
				return err
			}
			next := cloneModelState(plan.State)
			next.Status = "completed"
			next.ErrorCode = ""
			if err := save(ctx, tx, next, "completed"); err != nil {
				return err
			}
			if err := writecoordinator.PersistPocketBaseReceipt(ctx, tx, time.Now().UTC()); err != nil {
				return err
			}
			plan.State = next
			return nil
		})
	})
	return plan.State, err
}
func cloneModelState(s pluginmodel.State) pluginmodel.State {
	raw, _ := json.Marshal(s)
	var clone pluginmodel.State
	_ = json.Unmarshal(raw, &clone)
	return clone
}

func modelPublicState(s pluginmodel.State) pluginmodel.State {
	result := cloneModelState(s)
	for key, binding := range result.Bindings {
		if result.Revisions[binding.TableID] == "" {
			delete(result.Bindings, key)
		}
	}
	return result
}
