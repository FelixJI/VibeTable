package app

import (
	"context"
	"errors"
	"reflect"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
)

// This error is returned only after the installation, action and manifest were authenticated.
type modelBindingStale struct{ diagnostics []v2.Diagnostic }

func (e *modelBindingStale) Error() string { return "plugin_model_binding_stale" }
func (e *modelBindingStale) Unwrap() error { return pluginDataError("plugin_model_binding_stale") }

func modelDiagnostic(code, path string) v2.Diagnostic {
	return v2.Diagnostic{Code: code, Path: path, Message: code, Details: map[string]any{}}
}

func modelDiagnostics(ctx context.Context, app core.App, s pluginmodel.State) ([]v2.Diagnostic, error) {
	var diagnostics []v2.Diagnostic
	tables := modelTableIDs(s)
	for _, entity := range s.Model.Entities {
		binding := s.Bindings[entity.Key]
		table, err := schemaexecution.Describe(ctx, app, binding.TableID)
		if errors.Is(err, schemaexecution.ErrTableNotFound) || err == nil && table.Kind != "base" {
			diagnostics = append(diagnostics, modelDiagnostic("plugin_model_table_missing", entity.Key))
			continue
		}
		if err != nil {
			return nil, err
		}
		collection, err := app.FindCollectionByNameOrId(table.PhysicalName)
		if err != nil {
			return nil, err
		}
		for _, f := range entity.Fields {
			actual, ok := table.Field(binding.Fields[f.Key])
			code := "plugin_model_field_missing"
			if ok && actual.Identity.FieldID == binding.Fields[f.Key] {
				code = f.Compatibility(actual, tables)
				if code == "" && f.Relation != nil && actual.Relation.ReciprocalFieldID != s.Bindings[f.Relation.Entity].Fields[f.Relation.Field] {
					code = "plugin_model_relation_mismatch"
				}
				physical := collection.Fields.GetByName(actual.Identity.PhysicalName)
				if physical == nil || physical.GetHidden() {
					code = "plugin_model_field_access_denied"
				}
			}
			if code != "" {
				diagnostics = append(diagnostics, modelDiagnostic(code, entity.Key+"."+f.Key))
			}
		}
	}
	return diagnostics, nil
}

func latestModelState(states []pluginmodel.State, modelID string) pluginmodel.State {
	for i := len(states) - 1; i >= 0; i-- {
		if states[i].Model.ModelID == modelID {
			return states[i]
		}
	}
	return pluginmodel.State{}
}

type modelBindingChange struct {
	Path        string `json:"path"`
	OldTable    string `json:"oldTableId"`
	NewTable    string `json:"newTableId"`
	OldField    string `json:"oldFieldId"`
	NewField    string `json:"newFieldId,omitempty"`
	Relation    bool   `json:"relation"`
	OldRevision string `json:"oldSchemaRevision"`
}

// These checks run at preview and again in the one transaction that appends the replacement.
func checkModelRepair(ctx context.Context, app core.App, old, next pluginmodel.State) ([]modelBindingChange, []v2.Diagnostic, error) {
	changes := []modelBindingChange{}
	diagnostics := []v2.Diagnostic{}
	rules, err := transactionrules.Load(ctx, app)
	if err != nil {
		return nil, nil, err
	}
	if reflect.DeepEqual(old.Bindings, next.Bindings) {
		return changes, diagnostics, nil
	}
	catalog, err := schemaapi.New(app).List(ctx)
	if err != nil {
		return nil, nil, err
	}
	relations := map[string]map[string]bool{}
	addRelation := func(table, field string) {
		if relations[table] == nil {
			relations[table] = map[string]bool{}
		}
		relations[table][field] = true
	}
	for _, t := range catalog {
		for _, f := range t.Snapshot.Fields {
			if r := f.Relation; r != nil {
				addRelation(t.Snapshot.TableID, f.Identity.FieldID)
				addRelation(r.TargetTableID, r.DisplayField)
				addRelation(r.TargetTableID, r.ReciprocalFieldID)
			}
		}
	}
	revisions := map[string]string{}
	checked := map[string]string{}
	checkTable := func(id string) (string, error) {
		if code, ok := checked[id]; ok {
			return code, nil
		}
		if err := fieldchange.CheckTableWriteFence(ctx, app, id); err != nil {
			return "", err
		}
		table, err := schemaexecution.Describe(ctx, app, id)
		if errors.Is(err, schemaexecution.ErrTableNotFound) {
			return "plugin_model_history_unknown", nil
		}
		if err != nil {
			return "", err
		}
		revisions[id] = table.Snapshot.SchemaRevision
		rows, err := app.CountRecords(table.PhysicalName)
		if err != nil {
			return "", err
		}
		code := ""
		if table.Snapshot.DataRevision != 0 || rows != 0 {
			code = "plugin_model_history_present"
		}
		checked[id] = code
		return code, nil
	}
	for _, entity := range next.Model.Entities {
		a, exists := old.Bindings[entity.Key]
		b := next.Bindings[entity.Key]
		if !exists || reflect.DeepEqual(a, b) {
			continue
		}
		for _, f := range entity.Fields {
			oldField, newField := a.Fields[f.Key], b.Fields[f.Key]
			if a.TableID == b.TableID && oldField == newField {
				continue
			}
			path := entity.Key + "." + f.Key
			for _, end := range []struct{ table, field string }{{a.TableID, oldField}, {b.TableID, newField}} {
				if end.table == "" {
					continue
				}
				code, err := checkTable(end.table)
				if err != nil {
					return nil, nil, err
				}
				if code != "" {
					diagnostics = append(diagnostics, modelDiagnostic(code, path))
					break
				}
				if a.TableID != b.TableID {
					end.field = ""
				}
				referenced := relations[end.table]
				if end.field == "" && len(referenced) > 0 || referenced[end.field] {
					diagnostics = append(diagnostics, modelDiagnostic("plugin_model_relation_protected", path))
				}
				for _, rule := range rules {
					if rule.ProtectsBinding(end.table, end.field) {
						diagnostics = append(diagnostics, modelDiagnostic("plugin_model_rule_protected", path))
						break
					}
				}
			}
			changes = append(changes, modelBindingChange{Path: path, OldTable: a.TableID, NewTable: b.TableID, OldField: oldField, NewField: newField, Relation: f.Relation != nil, OldRevision: revisions[a.TableID]})
		}
	}
	return changes, diagnostics, nil
}
