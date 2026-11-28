package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/fieldprotection"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

// Persistent field protection (#591): Host-only Product RPCs owned by
// the Go sidecar. The workspace manager sees the actual table/field list a
// plugin suggests protecting, enables protection, and releases it again —
// including while the plugin is disabled or after it was uninstalled.
// Nothing here runs plugin code or writes business data; the only durable
// effect is an immutable plugin audit event replayed by fieldprotection.Load.

type pluginFieldProtectionDescribeParams struct {
	ProjectKey string `json:"projectKey"`
	PluginID   string `json:"pluginId"`
}

type pluginFieldProtectionListParams struct {
	ProjectKey string `json:"projectKey"`
}

type pluginFieldProtectionListItem struct {
	PluginID       string `json:"pluginId"`
	Installed      bool   `json:"installed"`
	ProtectedCount int    `json:"protectedCount"`
}

type pluginFieldProtectionSelection struct {
	fieldprotection.DeclaredField
	fieldprotection.FieldRef
}

type pluginFieldProtectionEnableParams struct {
	ProjectKey           string                           `json:"projectKey"`
	PluginID             string                           `json:"pluginId"`
	InstallationID       string                           `json:"installationId"`
	InstallationRevision int64                            `json:"installationRevision"`
	Fields               []pluginFieldProtectionSelection `json:"fields"`
}

type pluginFieldProtectionReleaseParams struct {
	ProjectKey string `json:"projectKey"`
	PluginID   string `json:"pluginId"`
	// nil releases every field the plugin protects; a list releases exactly
	// those fields and each must belong to the plugin.
	Fields *[]fieldprotection.FieldRef `json:"fields,omitempty"`
}

type pluginFieldProtectionInstallation struct {
	InstallationID string `json:"installationId"`
	Revision       int64  `json:"revision"`
	Status         string `json:"status"`
	Version        string `json:"version"`
	PluginAPI      string `json:"pluginApi"`
}

type pluginFieldProtectionOwner struct {
	PluginID       string `json:"pluginId"`
	InstallationID string `json:"installationId"`
}

type pluginFieldProtectionSuggested struct {
	Entity     string                      `json:"entity"`
	Field      string                      `json:"field"`
	TableID    string                      `json:"tableId"`
	FieldID    string                      `json:"fieldId"`
	TableName  string                      `json:"tableName"`
	FieldName  string                      `json:"fieldName"`
	FieldType  string                      `json:"fieldType"`
	State      string                      `json:"state"`
	Owner      *pluginFieldProtectionOwner `json:"owner"`
	OwnerStale bool                        `json:"ownerStale"`
	Diagnostic string                      `json:"diagnostic"`
	Eligible   bool                        `json:"eligible"`
}

type pluginFieldProtectionDescribeResult struct {
	Contract        string                             `json:"contract"`
	ProjectKey      string                             `json:"projectKey"`
	PluginID        string                             `json:"pluginId"`
	Installation    *pluginFieldProtectionInstallation `json:"installation"`
	Declared        bool                               `json:"declared"`
	Suggested       []pluginFieldProtectionSuggested   `json:"suggested"`
	Protected       []fieldprotection.Entry            `json:"protected"`
	Foreign         []fieldprotection.Entry            `json:"foreign"`
	BlockingReasons []string                           `json:"blockingReasons"`
	Events          int                                `json:"events"`
	EventsRemaining int                                `json:"eventsRemaining"`
	// Limits mirror fieldprotection so the manager UI can show "selected n of max".
	MaxProtectedFields int `json:"maxProtectedFields"`
	MaxEvents          int `json:"maxEvents"`
}

const (
	protectionStateUnprotected    = "unprotected"
	protectionStateProtected      = "protected"
	protectionStateProtectedOther = "protected_by_other"
)

func fieldProtectionPublicError(code, message string, details map[string]any) error {
	return &productrpc.PublicError{Code: code, Message: message, Details: details}
}

func fieldProtectionParamsInvalid() error {
	return &productrpc.PublicError{
		Code:    "pluginFieldProtection.params.invalid",
		Message: "Field protection requires the closed project, plugin, installation and field request.",
	}
}

func decodeFieldProtectionParams(raw json.RawMessage, target any, allowed ...string) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return fieldProtectionParamsInvalid()
	}
	for key, value := range fields {
		known := false
		for _, candidate := range allowed {
			if candidate == key {
				known = true
				break
			}
		}
		// An explicit null is only meaningful for the optional release list.
		if !known || (key != "fields" || len(allowed) == 3) && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fieldProtectionParamsInvalid()
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return fieldProtectionParamsInvalid()
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fieldProtectionParamsInvalid()
	}
	return nil
}

func validateFieldProtectionIdentity(projectKey, pluginID string) error {
	if !pluginProjectKeyPattern.MatchString(projectKey) || !pluginIDPattern.MatchString(pluginID) {
		return fieldProtectionParamsInvalid()
	}
	return nil
}

func decodeFieldProtectionDescribe(raw json.RawMessage) (pluginFieldProtectionDescribeParams, error) {
	var params pluginFieldProtectionDescribeParams
	err := decodeFieldProtectionParams(raw, &params, "projectKey", "pluginId")
	if err == nil {
		err = validateFieldProtectionIdentity(params.ProjectKey, params.PluginID)
	}
	return params, err
}

func decodeFieldProtectionEnable(raw json.RawMessage) (pluginFieldProtectionEnableParams, error) {
	var params pluginFieldProtectionEnableParams
	err := decodeFieldProtectionParams(raw, &params, "projectKey", "pluginId", "installationId", "installationRevision", "fields")
	if err == nil {
		err = validateFieldProtectionIdentity(params.ProjectKey, params.PluginID)
	}
	if err == nil && (params.InstallationID == "" || params.InstallationRevision < 1 ||
		len(params.Fields) == 0 || len(params.Fields) > fieldprotection.MaxProtectedFields) {
		err = fieldProtectionParamsInvalid()
	}
	if err != nil {
		return params, err
	}
	seen := map[fieldprotection.DeclaredField]bool{}
	for _, field := range params.Fields {
		if field.Entity == "" || field.Field == "" || len(field.Entity) > 64 || len(field.Field) > 64 ||
			field.TableID == "" || field.FieldID == "" || len(field.TableID) > 64 || len(field.FieldID) > 64 ||
			seen[field.DeclaredField] {
			return params, fieldProtectionParamsInvalid()
		}
		seen[field.DeclaredField] = true
	}
	return params, nil
}

func decodeFieldProtectionRelease(raw json.RawMessage) (pluginFieldProtectionReleaseParams, error) {
	var params pluginFieldProtectionReleaseParams
	err := decodeFieldProtectionParams(raw, &params, "projectKey", "pluginId", "fields")
	if err == nil {
		err = validateFieldProtectionIdentity(params.ProjectKey, params.PluginID)
	}
	if err == nil && params.Fields != nil && (len(*params.Fields) == 0 || len(*params.Fields) > fieldprotection.MaxProtectedFields) {
		err = fieldProtectionParamsInvalid()
	}
	if err != nil {
		return params, err
	}
	if params.Fields != nil {
		seen := map[fieldprotection.FieldRef]bool{}
		for _, ref := range *params.Fields {
			if ref.TableID == "" || ref.FieldID == "" || len(ref.TableID) > 64 || len(ref.FieldID) > 64 || seen[ref] {
				return params, fieldProtectionParamsInvalid()
			}
			seen[ref] = true
		}
	}
	return params, nil
}

// fieldProtectionRegistrations wires describe/enable/release. The write
// methods run under the business write gate and persist one PocketBase
// receipt inside the same transaction as the audit event.
func fieldProtectionRegistrations(app core.App, workspace string, gates ...businessWriteGate) []productrpc.Registration {
	service := pluginstore.New(app, workspace)
	decodeList := func(raw json.RawMessage) (pluginFieldProtectionListParams, error) {
		var params pluginFieldProtectionListParams
		err := decodeFieldProtectionParams(raw, &params, "projectKey")
		if err == nil {
			err = service.ValidateProject(params.ProjectKey)
		}
		return params, err
	}
	list := productrpc.Registration{
		Method: "plugin.fieldProtection.list",
		Scope:  productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			_, err := decodeList(raw)
			return err
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			params, err := decodeList(raw)
			if err != nil {
				return nil, err
			}
			var result []pluginFieldProtectionListItem
			err = app.RunInTransaction(func(tx core.App) error {
				result, err = fieldProtectionList(ctx, tx, workspace, params.ProjectKey)
				return err
			})
			return result, fieldProtectionDescribeError(err)
		},
	}
	describe := productrpc.Registration{
		Method: "plugin.fieldProtection.describe",
		Scope:  productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			params, err := decodeFieldProtectionDescribe(raw)
			if err != nil {
				return err
			}
			return service.ValidateProject(params.ProjectKey)
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			params, err := decodeFieldProtectionDescribe(raw)
			if err != nil {
				return nil, err
			}
			if err := service.ValidateProject(params.ProjectKey); err != nil {
				return nil, pluginPublicError(err)
			}
			var result *pluginFieldProtectionDescribeResult
			err = app.RunInTransaction(func(tx core.App) error {
				result, err = fieldProtectionDescribe(ctx, tx, workspace, params.ProjectKey, params.PluginID)
				return err
			})
			if err != nil {
				return nil, fieldProtectionDescribeError(err)
			}
			return result, nil
		},
	}
	enable := productrpc.Registration{
		Method: "plugin.fieldProtection.enable",
		Scope:  productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			params, err := decodeFieldProtectionEnable(raw)
			if err != nil {
				return err
			}
			return service.ValidateProject(params.ProjectKey)
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			params, err := decodeFieldProtectionEnable(raw)
			if err != nil {
				return nil, err
			}
			operationID, ok := productrpc.OperationID(ctx)
			if !ok {
				operationID = "field-protection:" + params.PluginID
			}
			var result *pluginFieldProtectionDescribeResult
			enableErr := runBusinessWrite(ctx, gates, "plugin.fieldProtection.enable", operationID, func(writeCtx context.Context) error {
				return app.RunInTransaction(func(tx core.App) error {
					if err := enableFieldProtection(writeCtx, tx, workspace, params); err != nil {
						return err
					}
					var err error
					result, err = fieldProtectionDescribe(writeCtx, tx, workspace, params.ProjectKey, params.PluginID)
					if err != nil {
						return err
					}
					return writecoordinator.PersistPocketBaseReceipt(writeCtx, tx, time.Now().UTC())
				})
			})
			if enableErr != nil {
				return nil, fieldProtectionDescribeError(enableErr)
			}
			return result, nil
		},
	}
	release := productrpc.Registration{
		Method: "plugin.fieldProtection.release",
		Scope:  productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			params, err := decodeFieldProtectionRelease(raw)
			if err != nil {
				return err
			}
			return service.ValidateProject(params.ProjectKey)
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			params, err := decodeFieldProtectionRelease(raw)
			if err != nil {
				return nil, err
			}
			operationID, ok := productrpc.OperationID(ctx)
			if !ok {
				operationID = "field-protection-release:" + params.PluginID
			}
			var result *pluginFieldProtectionDescribeResult
			releaseErr := runBusinessWrite(ctx, gates, "plugin.fieldProtection.release", operationID, func(writeCtx context.Context) error {
				return app.RunInTransaction(func(tx core.App) error {
					if err := releaseFieldProtection(writeCtx, tx, workspace, params); err != nil {
						return err
					}
					var err error
					result, err = fieldProtectionDescribe(writeCtx, tx, workspace, params.ProjectKey, params.PluginID)
					if err != nil {
						return err
					}
					return writecoordinator.PersistPocketBaseReceipt(writeCtx, tx, time.Now().UTC())
				})
			})
			if releaseErr != nil {
				return nil, fieldProtectionDescribeError(releaseErr)
			}
			return result, nil
		},
	}
	return []productrpc.Registration{list, describe, enable, release}
}

func fieldProtectionList(ctx context.Context, app core.App, workspace, projectKey string) ([]pluginFieldProtectionListItem, error) {
	installed, err := pluginstore.New(app, workspace).ListInstallations(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	registry, err := fieldprotection.Load(ctx, app)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, entry := range registry.Entries() {
		counts[entry.PluginID]++
	}
	result := make([]pluginFieldProtectionListItem, 0)
	for _, raw := range installed {
		var installation struct {
			PluginID string `json:"pluginId"`
			pluginInstallation
		}
		if err := json.Unmarshal(raw, &installation); err != nil {
			return nil, err
		}
		if count := counts[installation.PluginID]; count > 0 || installation.Manifest.Permissions.FieldProtection != nil {
			result = append(result, pluginFieldProtectionListItem{PluginID: installation.PluginID, Installed: true, ProtectedCount: count})
		}
		delete(counts, installation.PluginID)
	}
	for pluginID, count := range counts {
		result = append(result, pluginFieldProtectionListItem{PluginID: pluginID, ProtectedCount: count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PluginID < result[j].PluginID })
	return result, nil
}

func fieldProtectionDescribeError(err error) error {
	var public *productrpc.PublicError
	if errors.As(err, &public) {
		return err
	}
	var protection *fieldprotection.Error
	if errors.As(err, &protection) {
		return fieldProtectionPublicError("field_protection.registry_invalid", protection.Message, nil)
	}
	return err
}

// fieldProtectionInstallation loads the stored installation snapshot without
// requiring the plugin to be runnable.
func fieldProtectionInstallation(ctx context.Context, app core.App, workspace, projectKey, pluginID string) (*pluginInstallation, string, error) {
	raw, err := pluginstore.New(app, workspace).GetInstallation(ctx, projectKey, pluginID)
	if err != nil {
		return nil, "", err
	}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, "", nil
	}
	var snapshot struct {
		Version     string `json:"version"`
		DisplayName any    `json:"displayName"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return nil, "", &fieldprotection.Error{Code: "field_protection.installation_invalid", Message: "installation snapshot is invalid"}
	}
	var installed pluginInstallation
	if json.Unmarshal(raw, &installed) != nil {
		return nil, "", &fieldprotection.Error{Code: "field_protection.installation_invalid", Message: "installation snapshot is invalid"}
	}
	return &installed, snapshot.Version, nil
}

// fieldProtectionState resolves the suggested fields against the newest
// completed model state of the installed manifest's model, independent of the
// installation revision, so an in-place upgrade keeps the list resolvable.
func fieldProtectionState(ctx context.Context, app core.App, projectKey, pluginID string, installed *pluginInstallation) (*pluginmodel.State, error) {
	if installed == nil || installed.Manifest.Permissions.Model == nil || installed.Manifest.Permissions.FieldProtection == nil {
		return nil, nil
	}
	if err := installed.Manifest.Permissions.FieldProtection.Validate(installed.Manifest.Permissions.Model); err != nil {
		return nil, err
	}
	states, err := pluginmodel.States(ctx, app, projectKey, pluginID)
	if err != nil {
		return nil, err
	}
	modelID := installed.Manifest.Permissions.Model.ModelID
	for i := len(states) - 1; i >= 0; i-- {
		if states[i].Model.ModelID == modelID && states[i].Status == "completed" {
			return &states[i], nil
		}
	}
	return nil, nil
}

type fieldProtectionResolution struct {
	suggested []pluginFieldProtectionSuggested
	tables    map[string]schemaexecution.Table
}

// resolveFieldProtection builds the manager view of the declared fields: the
// actual table and field each one is bound to, with diagnostics when the
// binding drifted (table deleted, field retired, type changed).
func resolveFieldProtection(ctx context.Context, app core.App, installed *pluginInstallation, state *pluginmodel.State, registry *fieldprotection.Registry, pluginID string, installationID string) (fieldProtectionResolution, error) {
	resolution := fieldProtectionResolution{suggested: []pluginFieldProtectionSuggested{}, tables: map[string]schemaexecution.Table{}}
	if installed == nil || installed.Manifest.Permissions.FieldProtection == nil {
		return resolution, nil
	}
	tables := map[string]string{}
	if state != nil {
		tables = modelTableIDs(*state)
	}
	for _, declared := range installed.Manifest.Permissions.FieldProtection.Fields {
		item := pluginFieldProtectionSuggested{Entity: declared.Entity, Field: declared.Field, State: protectionStateUnprotected}
		resolution.suggested = append(resolution.suggested, item)
		if state == nil || state.Bindings[declared.Entity].TableID == "" || state.Bindings[declared.Entity].Fields[declared.Field] == "" {
			resolution.suggested[len(resolution.suggested)-1].Diagnostic = "plugin_model_not_bound"
			continue
		}
		binding := state.Bindings[declared.Entity]
		item.TableID, item.FieldID = binding.TableID, binding.Fields[declared.Field]
		table, cached := resolution.tables[binding.TableID]
		if !cached {
			described, collection, err := describeModelTable(ctx, app, binding.TableID)
			if err != nil {
				return resolution, err
			}
			table = described
			resolution.tables[binding.TableID] = described
			if collection == nil {
				item.Diagnostic = "plugin_model_table_missing"
			}
		}
		if item.Diagnostic == "" {
			actual, found := table.Field(item.FieldID)
			declaredField := modelField(*installed.Manifest.Permissions.Model, declared.Entity, declared.Field)
			switch {
			case !found || actual.Identity.FieldID != item.FieldID:
				item.Diagnostic = "plugin_model_field_missing"
			case declaredField.Compatibility(actual, tables) != "":
				item.Diagnostic = declaredField.Compatibility(actual, tables)
			case declaredField.Relation != nil && actual.Relation.ReciprocalFieldID != state.Bindings[declaredField.Relation.Entity].Fields[declaredField.Relation.Field]:
				item.Diagnostic = "plugin_model_relation_mismatch"
			default:
				item.TableName, item.FieldName, item.FieldType = table.Snapshot.DisplayName, actual.DisplayName, string(actual.LogicalType)
			}
		}
		if item.TableID != "" {
			if entry, protected := registry.Protected(item.TableID, item.FieldID); protected {
				item.Owner = &pluginFieldProtectionOwner{PluginID: entry.PluginID, InstallationID: entry.InstallationID}
				if entry.PluginID == pluginID {
					item.State = protectionStateProtected
					item.OwnerStale = installationID != "" && entry.InstallationID != installationID
				} else {
					item.State = protectionStateProtectedOther
				}
			}
		}
		// Only fields the plugin writes, bound to a live compatible field and not
		// owned by another plugin are selectable; extension and projection
		// fields never appear here because they are outside the model.
		item.Eligible = item.Diagnostic == "" && item.State != protectionStateProtectedOther
		resolution.suggested[len(resolution.suggested)-1] = item
	}
	return resolution, nil
}

func fieldProtectionDescribe(ctx context.Context, app core.App, workspace, projectKey, pluginID string) (*pluginFieldProtectionDescribeResult, error) {
	installed, version, err := fieldProtectionInstallation(ctx, app, workspace, projectKey, pluginID)
	if err != nil {
		return nil, err
	}
	registry, err := fieldprotection.Load(ctx, app)
	if err != nil {
		return nil, err
	}
	result := &pluginFieldProtectionDescribeResult{
		Contract: fieldprotection.Contract, ProjectKey: projectKey, PluginID: pluginID,
		Suggested:          []pluginFieldProtectionSuggested{},
		Protected:          registry.ForPlugin(pluginID),
		Foreign:            []fieldprotection.Entry{},
		BlockingReasons:    []string{},
		Events:             registry.Events(),
		EventsRemaining:    max(fieldprotection.MaxEvents-registry.EnableEvents(), 0),
		MaxProtectedFields: fieldprotection.MaxProtectedFields, MaxEvents: fieldprotection.MaxEvents,
	}
	// Protected entries outlive their table or field; flag those so the manager
	// can still release them while seeing they no longer bind to anything.
	live := map[string]schemaexecution.Table{}
	for index := range result.Protected {
		table, cached := live[result.Protected[index].TableID]
		if !cached {
			described, collection, err := describeModelTable(ctx, app, result.Protected[index].TableID)
			if err != nil {
				return nil, err
			}
			if collection == nil {
				described = schemaexecution.Table{}
			}
			live[result.Protected[index].TableID] = described
			table = described
		}
		if field, found := table.Field(result.Protected[index].FieldID); found && field.Identity.FieldID == result.Protected[index].FieldID {
			result.Protected[index].TableName = table.Snapshot.DisplayName
			result.Protected[index].FieldName = field.DisplayName
			result.Protected[index].FieldType = string(field.LogicalType)
		} else {
			result.Protected[index].Orphaned = true
		}
	}
	if installed == nil {
		result.Installation = nil
		result.BlockingReasons = append(result.BlockingReasons, "plugin_not_installed")
	} else {
		result.Installation = &pluginFieldProtectionInstallation{
			InstallationID: installed.InstallationID, Revision: installed.Revision,
			Status: installed.Status, Version: version, PluginAPI: installed.Manifest.Compatibility.PluginAPI,
		}
		if installed.Status != "enabled" {
			result.BlockingReasons = append(result.BlockingReasons, "plugin_disabled")
		}
		if installed.Manifest.Compatibility.PluginAPI != "3.x" {
			result.BlockingReasons = append(result.BlockingReasons, "plugin_api_unsupported")
		}
	}
	if installed == nil || installed.Manifest.Permissions.FieldProtection == nil {
		result.BlockingReasons = append(result.BlockingReasons, "no_declaration")
		return result, nil
	}
	result.Declared = true
	if installed.Manifest.Permissions.Model == nil {
		result.BlockingReasons = append(result.BlockingReasons, "model_required")
		return result, nil
	}
	state, err := fieldProtectionState(ctx, app, projectKey, pluginID, installed)
	if err != nil {
		return nil, err
	}
	if state == nil {
		result.BlockingReasons = append(result.BlockingReasons, "model_not_bound")
	}
	resolution, err := resolveFieldProtection(ctx, app, installed, state, registry, pluginID, installed.InstallationID)
	if err != nil {
		return nil, err
	}
	result.Suggested = resolution.suggested
	for _, item := range result.Suggested {
		if item.State == protectionStateProtectedOther && item.Owner != nil {
			if entry, ok := registry.Protected(item.TableID, item.FieldID); ok {
				result.Foreign = append(result.Foreign, entry)
			}
		}
	}
	if result.EventsRemaining == 0 {
		result.BlockingReasons = append(result.BlockingReasons, "events_exhausted")
	}
	return result, nil
}

// enableFieldProtection appends the enable event inside the caller's
// transaction. The installation must be current (id and revision CAS),
// enabled and on plugin API 3.x; every field must be declared, bound and
// free of another plugin's protection.
func enableFieldProtection(ctx context.Context, tx core.App, workspace string, params pluginFieldProtectionEnableParams) error {
	installed, version, err := fieldProtectionInstallation(ctx, tx, workspace, params.ProjectKey, params.PluginID)
	if err != nil {
		return err
	}
	if installed == nil {
		return fieldProtectionPublicError("field_protection.installation_stale", "The plugin installation no longer exists.", nil)
	}
	if installed.Status != "enabled" || installed.Manifest.Compatibility.PluginAPI != "3.x" {
		return fieldProtectionPublicError("field_protection.installation_unavailable", "Protection can only be enabled for an installed, enabled plugin on API 3.x.", nil)
	}
	if installed.InstallationID != params.InstallationID || installed.Revision != params.InstallationRevision {
		return fieldProtectionPublicError("field_protection.installation_stale", "The plugin installation changed; refresh and retry.", nil)
	}
	declaration := installed.Manifest.Permissions.FieldProtection
	if declaration == nil || installed.Manifest.Permissions.Model == nil {
		return fieldProtectionPublicError("field_protection.declaration_invalid", "The plugin does not declare field protection.", nil)
	}
	if err := declaration.Validate(installed.Manifest.Permissions.Model); err != nil {
		return fieldProtectionPublicError("field_protection.declaration_invalid", "The plugin's field protection declaration is invalid.", nil)
	}
	state, err := fieldProtectionState(ctx, tx, params.ProjectKey, params.PluginID, installed)
	if err != nil {
		return err
	}
	if state == nil {
		return fieldProtectionPublicError("field_protection.binding_stale", "The plugin model has no completed binding yet.", nil)
	}
	registry, err := fieldprotection.Load(ctx, tx)
	if err != nil {
		return err
	}
	if registry.EnableEvents() >= fieldprotection.MaxEvents {
		return fieldProtectionPublicError("field_protection.budget_exceeded", "The field protection event budget of this workspace is exhausted.", nil)
	}
	resolution, err := resolveFieldProtection(ctx, tx, installed, state, registry, params.PluginID, installed.InstallationID)
	if err != nil {
		return err
	}
	byDeclared := map[fieldprotection.DeclaredField]pluginFieldProtectionSuggested{}
	for _, item := range resolution.suggested {
		byDeclared[fieldprotection.DeclaredField{Entity: item.Entity, Field: item.Field}] = item
	}
	owned := len(registry.ForPlugin(params.PluginID))
	additional := 0
	entries := make([]fieldprotection.Entry, 0, len(params.Fields))
	requested := map[fieldprotection.FieldRef]bool{}
	for _, selected := range params.Fields {
		declared := selected.DeclaredField
		item, ok := byDeclared[declared]
		if !ok {
			return fieldProtectionPublicError("field_protection.field_not_declared", "A requested field is not declared for protection by the plugin.", map[string]any{"entity": declared.Entity, "field": declared.Field})
		}
		if item.Diagnostic != "" || item.TableID != selected.TableID || item.FieldID != selected.FieldID {
			return fieldProtectionPublicError("field_protection.binding_stale", "A selected field mapping changed; refresh and select the field again.", map[string]any{"entity": declared.Entity, "field": declared.Field, "diagnostic": item.Diagnostic})
		}
		ref := fieldprotection.FieldRef{TableID: item.TableID, FieldID: item.FieldID}
		if requested[ref] {
			continue
		}
		requested[ref] = true
		if conflict, protected := registry.OwnerConflict(ref, params.PluginID); protected {
			return fieldProtectionPublicError("field_protection.owner_conflict", "Another plugin already protects this field.", map[string]any{"tableId": ref.TableID, "fieldId": ref.FieldID, "pluginId": conflict.PluginID, "installationId": conflict.InstallationID})
		}
		if _, alreadyProtected := registry.Protected(ref.TableID, ref.FieldID); !alreadyProtected {
			additional++
		}
		entries = append(entries, fieldprotection.Entry{
			TableID: item.TableID, FieldID: item.FieldID, Entity: item.Entity, Field: item.Field,
			TableName: item.TableName, FieldName: item.FieldName, FieldType: item.FieldType,
		})
	}
	if owned+additional > fieldprotection.MaxProtectedFields {
		return fieldProtectionPublicError("field_protection.budget_exceeded", "The plugin exceeds the protected field budget.", nil)
	}
	for index := range entries {
		entries[index].PluginID, entries[index].InstallationID = params.PluginID, installed.InstallationID
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].TableID != entries[j].TableID {
			return entries[i].TableID < entries[j].TableID
		}
		return entries[i].FieldID < entries[j].FieldID
	})
	event, err := fieldprotection.NewEnabledEvent(
		"fieldProtection:enable:"+security.RandomString(12), params.ProjectKey, params.PluginID,
		version, installed.PackageHash,
		fieldprotection.EnabledDetails{
			InstallationID: installed.InstallationID, InstallationRevision: installed.Revision,
			ModelID: state.Model.ModelID, ModelVersion: state.Model.Version, Fields: entries,
		},
		time.Now().UTC().Truncate(time.Second).Format(time.RFC3339),
	)
	if err != nil {
		return err
	}
	if _, err := pluginstore.New(tx, workspace).RecordAuditInTransaction(ctx, event); err != nil {
		return pluginPublicError(err)
	}
	return nil
}

// The plugin can observe its declaration and total existing protection. Host
// management keeps the full old mappings and other owners' identities.
func surfaceFieldProtectionStatus(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest) (any, error) {
	var params map[string]json.RawMessage
	if decodePluginParams(input.Request, &params) != nil || params == nil || len(params) != 0 {
		return nil, pluginDataError("plugin_capability_invalid")
	}
	type statusField struct {
		fieldprotection.DeclaredField
		fieldprotection.FieldRef
		State      string `json:"state"`
		OwnerStale bool   `json:"ownerStale"`
		Diagnostic string `json:"diagnostic"`
	}
	result := struct {
		Contract              string        `json:"contract"`
		Declared              bool          `json:"declared"`
		ActiveProtectionCount int           `json:"activeProtectionCount"`
		CurrentProtectedCount int           `json:"currentProtectedCount"`
		Fields                []statusField `json:"fields"`
	}{Contract: fieldprotection.Contract, Fields: []statusField{}}
	err := app.RunInTransaction(func(tx core.App) error {
		if _, err := surfaceInstallation(ctx, tx, workspace, input); err != nil {
			return err
		}
		manager, err := fieldProtectionDescribe(ctx, tx, workspace, input.ProjectKey, input.PluginID)
		if err != nil {
			return fieldProtectionDescribeError(err)
		}
		result.Declared = manager.Declared
		result.ActiveProtectionCount = len(manager.Protected)
		current := map[fieldprotection.FieldRef]struct{}{}
		for _, item := range manager.Suggested {
			ref := fieldprotection.FieldRef{TableID: item.TableID, FieldID: item.FieldID}
			if item.State == protectionStateProtected && !item.OwnerStale && item.Diagnostic == "" {
				current[ref] = struct{}{}
			}
			result.Fields = append(result.Fields, statusField{
				DeclaredField: fieldprotection.DeclaredField{Entity: item.Entity, Field: item.Field},
				FieldRef:      ref, State: item.State, OwnerStale: item.OwnerStale, Diagnostic: item.Diagnostic,
			})
		}
		result.CurrentProtectedCount = len(current)
		return nil
	})
	return result, err
}

// releaseFieldProtection appends the release event. It never needs the
// installation: an administrator releases while the plugin is disabled or
// after it was uninstalled. Each listed field must belong to the plugin.
func releaseFieldProtection(ctx context.Context, tx core.App, workspace string, params pluginFieldProtectionReleaseParams) error {
	registry, err := fieldprotection.Load(ctx, tx)
	if err != nil {
		return err
	}
	owned := registry.ForPlugin(params.PluginID)
	installed, version, err := fieldProtectionInstallation(ctx, tx, workspace, params.ProjectKey, params.PluginID)
	if err != nil {
		return err
	}
	details := fieldprotection.ReleasedDetails{Fields: []fieldprotection.FieldRef{}}
	if params.Fields == nil {
		if len(owned) == 0 {
			// Nothing is protected by this plugin: no event, no budget use.
			return nil
		}
		details.All = true
		for _, entry := range owned {
			details.Fields = append(details.Fields, fieldprotection.FieldRef{TableID: entry.TableID, FieldID: entry.FieldID})
		}
	} else {
		for _, ref := range *params.Fields {
			found := false
			for _, entry := range owned {
				if entry.TableID == ref.TableID && entry.FieldID == ref.FieldID {
					found = true
				}
			}
			if !found {
				return fieldProtectionPublicError("field_protection.not_owner", "A requested field is not protected by this plugin.", map[string]any{"tableId": ref.TableID, "fieldId": ref.FieldID})
			}
			details.Fields = append(details.Fields, ref)
		}
		sort.Slice(details.Fields, func(i, j int) bool {
			if details.Fields[i].TableID != details.Fields[j].TableID {
				return details.Fields[i].TableID < details.Fields[j].TableID
			}
			return details.Fields[i].FieldID < details.Fields[j].FieldID
		})
	}
	// After an uninstall no installation record remains; the plugin's own
	// protection history supplies the version and hash of its last change so the
	// audit envelope stays complete and truthful.
	pluginVersion, packageHash := "", ""
	if installed != nil {
		pluginVersion, packageHash = version, installed.PackageHash
	} else {
		pluginVersion, packageHash = registry.Identity(params.PluginID).PluginVersion, registry.Identity(params.PluginID).PackageHash
	}
	if pluginVersion == "" || packageHash == "" {
		return fieldProtectionPublicError("field_protection.registry_invalid", "The plugin version that protected these fields is unknown.", nil)
	}
	event, err := fieldprotection.NewReleasedEvent(
		"fieldProtection:release:"+security.RandomString(12), params.ProjectKey, params.PluginID,
		pluginVersion, packageHash, details,
		time.Now().UTC().Truncate(time.Second).Format(time.RFC3339),
	)
	if err != nil {
		return err
	}
	if _, err := pluginstore.New(tx, workspace).RecordAuditInTransaction(ctx, event); err != nil {
		return pluginPublicError(err)
	}
	return nil
}
