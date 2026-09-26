// Package pluginstore owns the durable plugin shared catalog: installation
// identity with revision CAS, package revisions, private settings and the
// audit trail. Payloads are decoded, validated and re-stored as canonical
// JSON documents: the public wire contract (camelCase snapshot semantics
// produced by the previous owner) is preserved semantically, not
// byte-for-byte; only identity, ordering and CAS state are interpreted
// here. Public error shapes are mapped through PublicError to the frozen
// oracle contract.
package pluginstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"
	mutationpkg "github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const (
	recordsCollection  = "vibetable_plugin_records"
	markersCollection  = "vibetable_plugin_import_markers"
	maxPayloadBytes    = 4 << 20
	listPageSize       = 1_000
	PluginCatalogTable = "plugin:catalog"
	// ConflictItemID projects the whole plugin shared catalog as one
	// synchronized workspace settings item, mirroring metadata namespaces.
	ConflictItemID = "plugin:shared-state"
)

// NamespaceForCollection resolves the conflict-projection allowlist entry
// for the plugin shared-state collection. It does not authorize writes.
func NamespaceForCollection(collection string) (string, bool) {
	if collection == recordsCollection {
		return ConflictItemID, true
	}
	return "", false
}

// Kinds mirror the legacy four-kind plugin_records schema.
const (
	KindInstallation = "installation"
	KindRevision     = "revision"
	KindSetting      = "setting"
	KindAudit        = "audit"
)

type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (err *Error) Error() string { return err.Code + ": " + err.Message }

func storageError() *Error {
	return &Error{Code: "plugin.storage_failed", Message: "plugin storage operation failed"}
}

func requestError(code, message string, details map[string]any) *Error {
	return &Error{Code: code, Message: message, Details: details}
}

func revisionConflict(kind string, expected, actual any) *Error {
	label := "plugin"
	if kind == KindSetting {
		label = "setting"
	}
	format := func(value any) any {
		if value == nil {
			return "None"
		}
		return value
	}
	return &Error{
		Code:    "plugin.revision_conflict",
		Message: fmt.Sprintf("%s revision mismatch: expected %v, found %v", label, format(expected), format(actual)),
		Details: map[string]any{"kind": kind, "expected": expected, "actual": actual},
	}
}

func notFound() *Error {
	return &Error{Code: "plugin.not_found", Message: "plugin is not installed"}
}

// Frozen public error codes from the Python catalog oracle. The internal
// store codes are never exposed directly on the public wire.
const (
	PublicCodeAlreadyInstalled = "plugin_already_installed"
	PublicCodeNotFound         = "plugin_not_found"
	PublicCodeBlocked          = "plugin_blocked"
)

// PublicErrorView is the public wire shape of one plugin shared-state
// error. Code is the frozen rpcErrorData.code; a store CAS conflict keeps
// Code empty and carries only the frozen conflict message.
type PublicErrorView struct {
	Code    string
	Message string
}

// PublicError maps internal store errors to the frozen public contract.
// The second return is false for errors with no public equivalent (the
// caller must fail closed instead of inventing a code).
func PublicError(err error) (PublicErrorView, bool) {
	var storeErr *Error
	if !errors.As(err, &storeErr) {
		return PublicErrorView{}, false
	}
	switch storeErr.Code {
	case "plugin.already_installed":
		return PublicErrorView{Code: PublicCodeAlreadyInstalled, Message: "plugin is already installed"}, true
	case "plugin.not_found":
		return PublicErrorView{Code: PublicCodeNotFound, Message: "plugin is not installed"}, true
	case "plugin_blocked":
		return PublicErrorView{Code: PublicCodeBlocked, Message: "plugin has blocking reasons"}, true
	case "plugin.revision_conflict":
		// PluginStoreConflictError carries only the frozen message.
		return PublicErrorView{Code: "", Message: storeErr.Message}, true
	default:
		return PublicErrorView{}, false
	}
}

type Service struct {
	app         core.App
	workspaceID string
	now         func() time.Time
	newID       func() string
	onCommitted func()
}

// legacyImportVersion identifies the fixed legacy plugins.db schema (the
// four-kind plugin_records table) that ImportLegacySQLite understands.
const legacyImportVersion = "legacy-plugins-db-v1"

// New builds the workspace-scoped plugin shared catalog owner. The stable
// workspace UUID anchors import markers so relocating a workspace never
// changes migration identity; file paths are not identity.
func New(app core.App, workspaceID string, onCommitted ...func()) *Service {
	service := &Service{
		app:         app,
		workspaceID: workspaceID,
		now:         func() time.Time { return time.Now().UTC() },
		newID: func() string {
			return "evt_plugin_" + security.RandomString(12)
		},
	}
	if len(onCommitted) > 0 {
		service.onCommitted = onCommitted[0]
	}
	return service
}

// ProjectKey is the workspace's plugin project identity ("local:"+UUID).
func (service *Service) ProjectKey() string {
	return "local:" + strings.ToLower(strings.ReplaceAll(service.workspaceID, "-", ""))
}

// ImportMarkerKey is the stable import completion identity for this
// workspace and legacy schema version.
func (service *Service) ImportMarkerKey() string {
	return legacyImportVersion + ":" + strings.TrimPrefix(service.ProjectKey(), "local:")
}

// transact keeps the authoritative data, notification and recovery receipt
// inside the same PocketBase commit prepared by the workspace writer gate.
func (service *Service) transact(ctx context.Context, apply func(core.App) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := service.app.RunInTransaction(func(tx core.App) error {
		if err := apply(tx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return writecoordinator.PersistPocketBaseReceipt(ctx, tx, service.now())
	})
	if err == nil && service.onCommitted != nil {
		service.onCommitted()
	}
	return err
}

func (service *Service) ValidateProject(projectKey string) error {
	if projectKey != service.ProjectKey() {
		return requestError("plugin.request_invalid", "project does not belong to this workspace", nil)
	}
	return nil
}

type record struct {
	Kind       string
	ProjectKey string
	PluginID   string
	ItemKey    string
	Payload    json.RawMessage
	Seq        int64
}

// ---- reads ----

func (service *Service) ListInstallations(
	ctx context.Context,
	projectKey string,
) ([]json.RawMessage, error) {
	rows, err := service.list(ctx, KindInstallation, projectKey, "")
	if err != nil {
		return nil, err
	}
	result := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.Payload)
	}
	sort.Slice(result, func(i, j int) bool {
		return snapshotID(result[i]) < snapshotID(result[j])
	})
	return result, nil
}

func (service *Service) GetInstallation(
	ctx context.Context,
	projectKey string,
	pluginID string,
) (json.RawMessage, error) {
	rows, err := service.list(ctx, KindInstallation, projectKey, pluginID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0].Payload, nil
}

func (service *Service) ListPackageRevisions(
	ctx context.Context,
	projectKey string,
	pluginID string,
) ([]json.RawMessage, error) {
	rows, err := service.list(ctx, KindRevision, projectKey, pluginID)
	if err != nil {
		return nil, err
	}
	result := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.Payload)
	}
	return result, nil
}

func (service *Service) IsPackagePathReferenced(
	ctx context.Context,
	localPath string,
) (bool, error) {
	if localPath == "" {
		return false, requestError("plugin.request_invalid", "localPath is required", nil)
	}
	rows, err := service.list(ctx, KindRevision, "", "")
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if jsonText(row.Payload, "localPath") == localPath {
			return true, nil
		}
	}
	return false, nil
}

func (service *Service) GetPrivateSetting(
	ctx context.Context,
	projectKey string,
	pluginID string,
	settingKey string,
) (json.RawMessage, error) {
	rows, err := service.list(ctx, KindSetting, projectKey, pluginID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.ItemKey == settingKey {
			return row.Payload, nil
		}
	}
	return nil, nil
}

func (service *Service) ListAudit(
	ctx context.Context,
	projectKey string,
	pluginID string,
) ([]json.RawMessage, error) {
	return service.listAudit(ctx, projectKey, pluginID)
}

func (service *Service) ListProjectAudit(
	ctx context.Context,
	projectKey string,
) ([]json.RawMessage, error) {
	return service.listAudit(ctx, projectKey, "")
}

func (service *Service) listAudit(
	ctx context.Context,
	projectKey string,
	pluginID string,
) ([]json.RawMessage, error) {
	rows, err := service.list(ctx, KindAudit, projectKey, pluginID)
	if err != nil {
		return nil, err
	}
	result := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.Payload)
	}
	return result, nil
}

func (service *Service) list(
	ctx context.Context,
	kind string,
	projectKey string,
	pluginID string,
) ([]record, error) {
	if projectKey != "" {
		if err := service.ValidateProject(projectKey); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	collection, err := service.app.FindCollectionByNameOrId(recordsCollection)
	if err != nil {
		return nil, storageError()
	}
	filter := "kind={:kind}"
	params := dbx.Params{"kind": kind}
	if projectKey != "" {
		filter += " && project_key={:project}"
		params["project"] = projectKey
	}
	if pluginID != "" {
		filter += " && plugin_id={:plugin}"
		params["plugin"] = pluginID
	}
	// Page until a short page so a list answer is always the complete set;
	// the previous owner had no record ceiling and partial reads are never
	// reported as complete.
	result := make([]record, 0)
	for offset := 0; ; offset += listPageSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, pageErr := service.app.FindRecordsByFilter(
			collection, filter, "+seq, +id", listPageSize, offset, params,
		)
		if pageErr != nil {
			return nil, storageError()
		}
		for _, stored := range page {
			row, decodeErr := recordFromStored(stored)
			if decodeErr != nil {
				return nil, decodeErr
			}
			if err := service.validateLegacyRow(row); err != nil {
				return nil, storageError()
			}
			result = append(result, row)
		}
		if len(page) < listPageSize {
			return result, nil
		}
	}
}

// ---- writes ----

// SaveInstallation persists one installation snapshot with integer CAS on
// the snapshot revision. expectedRevision == nil means the installation
// must not exist yet (create), matching the previous owner's semantics.
func (service *Service) SaveInstallation(
	ctx context.Context,
	snapshot json.RawMessage,
	expectedRevision *int64,
) (json.RawMessage, error) {
	identity, err := validateSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	var saved json.RawMessage
	if err := service.ValidateProject(identity.projectKey); err != nil {
		return nil, err
	}
	err = service.transact(ctx, func(txApp core.App) error {
		current, findErr := findInstallation(txApp, identity.projectKey, identity.pluginID)
		if findErr != nil {
			return findErr
		}
		var actual *int64
		if current != nil {
			value := jsonInt(current.Payload, "revision")
			actual = &value
		}
		if !revisionMatches(actual, expectedRevision) {
			return revisionConflict(KindInstallation, deref(expectedRevision), deref(actual))
		}
		row := record{
			Kind: KindInstallation, ProjectKey: identity.projectKey,
			PluginID: identity.pluginID, ItemKey: "current",
			Payload: snapshot, Seq: 0,
		}
		if saveErr := saveRecord(txApp, row, current != nil); saveErr != nil {
			return saveErr
		}
		event, eventErr := catalogChangedEvent(service.newID(), service.now(), snapshot)
		if eventErr != nil {
			return eventErr
		}
		if err := saveOutboxEvent(txApp, event); err != nil {
			return err
		}
		saved = snapshot
		return nil
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

func (service *Service) DeleteInstallation(
	ctx context.Context,
	projectKey string,
	pluginID string,
) (bool, error) {
	count, err := service.deleteWhere(ctx, KindInstallation, projectKey, pluginID, "")
	return count > 0, err
}

func (service *Service) SavePackageRevision(
	ctx context.Context,
	revision json.RawMessage,
) (json.RawMessage, error) {
	identity, hash, err := validateRevisionPayload(revision)
	if err != nil {
		return nil, err
	}
	if err := service.ValidateProject(identity.projectKey); err != nil {
		return nil, err
	}
	err = service.transact(ctx, func(txApp core.App) error {
		stored, findErr := findRecord(
			txApp, KindRevision, identity.projectKey,
			identity.pluginID, hash,
		)
		if findErr != nil {
			return findErr
		}
		seq := int64(0)
		if stored != nil {
			seq = int64(stored.GetInt("seq"))
		} else {
			var err error
			seq, err = nextProjectSeq(txApp, KindRevision, identity.projectKey)
			if err != nil {
				return err
			}
		}
		return saveRecord(txApp, record{
			Kind: KindRevision, ProjectKey: identity.projectKey,
			PluginID: identity.pluginID, ItemKey: hash,
			Payload: revision, Seq: seq,
		}, stored != nil)
	})
	if err != nil {
		return nil, err
	}
	return revision, nil
}

func (service *Service) DeletePackageRevision(
	ctx context.Context,
	projectKey string,
	pluginID string,
	packageHash string,
) (bool, error) {
	count, err := service.deleteWhere(ctx, KindRevision, projectKey, pluginID, packageHash)
	return count > 0, err
}

func (service *Service) DeletePackageRevisions(
	ctx context.Context,
	projectKey string,
	pluginID string,
) (int, error) {
	count, err := service.deleteWhere(ctx, KindRevision, projectKey, pluginID, "")
	return int(count), err
}

// SavePrivateSetting persists one private setting with integer CAS.
func (service *Service) SavePrivateSetting(
	ctx context.Context,
	setting json.RawMessage,
	expectedRevision *int64,
) (json.RawMessage, error) {
	projectKey, pluginID, settingKey, err := validateSettingPayload(setting)
	if err != nil {
		return nil, err
	}
	var saved json.RawMessage
	if err := service.ValidateProject(projectKey); err != nil {
		return nil, err
	}
	err = service.transact(ctx, func(txApp core.App) error {
		stored, findErr := findRecord(
			txApp, KindSetting, projectKey, pluginID, settingKey,
		)
		if findErr != nil {
			return findErr
		}
		var actual *int64
		if stored != nil {
			payload, decodeErr := recordFromStored(stored)
			if decodeErr != nil {
				return decodeErr
			}
			value := jsonInt(payload.Payload, "revision")
			actual = &value
		}
		if !revisionMatches(actual, expectedRevision) {
			return revisionConflict(KindSetting, deref(expectedRevision), deref(actual))
		}
		if saveErr := saveRecord(txApp, record{
			Kind: KindSetting, ProjectKey: projectKey,
			PluginID: pluginID, ItemKey: settingKey,
			Payload: setting, Seq: 0,
		}, stored != nil); saveErr != nil {
			return saveErr
		}
		saved = setting
		return nil
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

func (service *Service) DeletePrivateSettings(
	ctx context.Context,
	projectKey string,
	pluginID string,
) (int, error) {
	count, err := service.deleteWhere(ctx, KindSetting, projectKey, pluginID, "")
	return int(count), err
}

// RecordAudit appends one audit event with a per-project monotonic
// sequence so list order matches the previous owner's insertion order.
func (service *Service) RecordAudit(
	ctx context.Context,
	event json.RawMessage,
) (json.RawMessage, error) {
	projectKey, pluginID, eventID, err := validateAuditPayload(event)
	if err != nil {
		return nil, err
	}
	if err := service.ValidateProject(projectKey); err != nil {
		return nil, err
	}
	err = service.transact(ctx, func(txApp core.App) error {
		stored, findErr := findRecord(
			txApp, KindAudit, projectKey, pluginID, eventID,
		)
		if findErr != nil {
			return findErr
		}
		if stored != nil {
			// Audit events are immutable; replaying the same event id is a
			// no-op instead of silently rewriting history.
			return nil
		}
		seq, seqErr := nextProjectSeq(txApp, KindAudit, projectKey)
		if seqErr != nil {
			return seqErr
		}
		return saveRecord(txApp, record{
			Kind: KindAudit, ProjectKey: projectKey,
			PluginID: pluginID, ItemKey: eventID,
			Payload: event, Seq: seq,
		}, false)
	})
	if err != nil {
		return nil, err
	}
	return event, nil
}

// SetEnabled applies the Go-owned enable/disable transition: it validates
// blocking reasons, bumps the installation revision with CAS, records the
// lifecycle audit event and emits the durable plugin catalog change in one
// transaction. It is the sole authority for this public mutation.
func (service *Service) SetEnabled(
	ctx context.Context,
	projectKey string,
	pluginID string,
	enabled bool,
) (json.RawMessage, error) {
	if projectKey == "" || pluginID == "" {
		return nil, requestError("plugin.request_invalid", "project and plugin identity is required", nil)
	}
	var result json.RawMessage
	if err := service.ValidateProject(projectKey); err != nil {
		return nil, err
	}
	err := service.transact(ctx, func(txApp core.App) error {
		current, findErr := findInstallation(txApp, projectKey, pluginID)
		if findErr != nil {
			return findErr
		}
		if current == nil {
			return notFound()
		}
		blocking := jsonStrings(current.Payload, "blockingReasons")
		if enabled && len(blocking) > 0 {
			return &Error{
				Code:    "plugin_blocked",
				Message: "plugin has blocking reasons",
				Details: map[string]any{"blockingReasons": blocking},
			}
		}
		revision := jsonInt(current.Payload, "revision")
		disabledReason := any("disabled_by_user")
		if enabled {
			disabledReason = nil
		}
		updated, updateErr := updateSnapshot(
			current.Payload,
			map[string]any{
				"status":         map[bool]string{true: "enabled", false: "disabled"}[enabled],
				"disabledReason": disabledReason,
				"revision":       revision + 1,
			},
		)
		if updateErr != nil {
			return updateErr
		}
		if saveErr := saveRecord(txApp, record{
			Kind: KindInstallation, ProjectKey: projectKey,
			PluginID: pluginID, ItemKey: "current",
			Payload: updated, Seq: 0,
		}, true); saveErr != nil {
			return saveErr
		}
		audit, auditErr := lifecycleAudit(
			service.now(), updated,
			map[bool]string{true: "enable", false: "disable"}[enabled],
		)
		if auditErr != nil {
			return auditErr
		}
		if _, recordErr := service.recordAuditTx(txApp, audit); recordErr != nil {
			return recordErr
		}
		event, eventErr := catalogChangedEvent(service.newID(), service.now(), updated)
		if eventErr != nil {
			return eventErr
		}
		if err := saveOutboxEvent(txApp, event); err != nil {
			return err
		}
		result = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (service *Service) recordAuditTx(
	txApp core.App,
	event json.RawMessage,
) (json.RawMessage, error) {
	projectKey, pluginID, eventID, err := validateAuditPayload(event)
	if err != nil {
		return nil, err
	}
	seq, err := nextProjectSeq(txApp, KindAudit, projectKey)
	if err != nil {
		return nil, err
	}
	if err := saveRecord(txApp, record{
		Kind: KindAudit, ProjectKey: projectKey,
		PluginID: pluginID, ItemKey: eventID,
		Payload: event, Seq: seq,
	}, false); err != nil {
		return nil, err
	}
	return event, nil
}

func (service *Service) deleteWhere(
	ctx context.Context,
	kind string,
	projectKey string,
	pluginID string,
	itemKey string,
) (int64, error) {
	if projectKey == "" || pluginID == "" {
		return 0, requestError("plugin.request_invalid", "project and plugin identity is required", nil)
	}
	var removed int64
	if err := service.ValidateProject(projectKey); err != nil {
		return 0, err
	}
	err := service.transact(ctx, func(txApp core.App) error {
		filter := "kind={:kind} && project_key={:project} && plugin_id={:plugin}"
		params := dbx.Params{"kind": kind, "project": projectKey, "plugin": pluginID}
		if itemKey != "" {
			filter += " && item_key={:item}"
			params["item"] = itemKey
		}
		records, err := txApp.FindRecordsByFilter(
			recordsCollection, filter, "", 0, 0, params,
		)
		if err != nil {
			return storageError()
		}
		for _, stored := range records {
			if err := txApp.Delete(stored); err != nil {
				return storageError()
			}
			removed++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// ---- legacy import ----

// ImportLegacySQLite imports the four record kinds from a legacy Python
// plugins.db file. The source is opened read-only and never written; the
// import and the durable completion marker commit in one transaction. The
// marker is anchored to the stable workspace UUID plus the fixed legacy
// schema version, so a completed import can never be re-run against the
// same workspace (relocation keeps identity; the file path is not
// identity), and any target conflict without a marker fails closed without
// touching the source file.
func (service *Service) ImportLegacySQLite(
	ctx context.Context,
	sourcePath string,
) (ImportResult, error) {
	if sourcePath == "" {
		return ImportResult{}, requestError("plugin.request_invalid", "source path is required", nil)
	}
	if service.workspaceID == "" {
		return ImportResult{}, storageError()
	}
	markerKey := service.ImportMarkerKey()
	marker, err := service.findMarker(markerKey)
	if err != nil {
		return ImportResult{}, err
	}
	if marker != nil {
		return ImportResult{
			Status:      ImportStatusAlreadyComplete,
			RecordCount: marker.RecordCount,
		}, nil
	}
	var rows []record
	var readErr error
	if _, statErr := os.Stat(sourcePath); statErr == nil {
		rows, readErr = readLegacyPluginRecords(ctx, sourcePath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return ImportResult{}, legacySourceInvalid(statErr)
	}
	// A new workspace has no old store. Persist the same completion marker
	// so a copied legacy file can never later override the Go catalog.
	if readErr != nil {
		return ImportResult{}, readErr
	}
	for index := range rows {
		if validateErr := service.validateLegacyRow(rows[index]); validateErr != nil {
			return ImportResult{}, validateErr
		}
	}
	var imported int64
	err = service.transact(ctx, func(txApp core.App) error {
		// Recheck under the writer transaction; an earlier importer may have
		// committed while the read-only source was being decoded.
		txService := *service
		txService.app = txApp
		if existing, err := txService.findMarker(markerKey); err != nil {
			return err
		} else if existing != nil {
			imported = existing.RecordCount
			return writecoordinator.ReplayedBusinessWrite(ctx, "plugin.import", markerKey)
		}
		count, err := txApp.CountRecords(recordsCollection)
		if err != nil {
			return storageError()
		}
		if count != 0 {
			return requestError("plugin.import_conflict", "plugin target is populated without an import completion marker", nil)
		}
		for _, row := range rows {
			stored, findErr := findRecord(txApp, row.Kind, row.ProjectKey, row.PluginID, row.ItemKey)
			if findErr != nil {
				return findErr
			}
			if stored != nil {
				return &Error{
					Code: "plugin.import_conflict",
					Message: fmt.Sprintf(
						"target already holds %s record %s/%s/%s without a completed import marker",
						row.Kind, row.ProjectKey, row.PluginID, row.ItemKey,
					),
					Details: map[string]any{
						"kind": row.Kind, "projectKey": row.ProjectKey,
						"pluginId": row.PluginID, "itemKey": row.ItemKey,
					},
				}
			}
			if row.Kind == KindAudit || row.Kind == KindRevision {
				seq, seqErr := nextProjectSeq(txApp, row.Kind, row.ProjectKey)
				if seqErr != nil {
					return seqErr
				}
				row.Seq = seq
			}
			if saveErr := saveRecord(txApp, row, false); saveErr != nil {
				return saveErr
			}
			imported++
		}
		collection, findErr := txApp.FindCollectionByNameOrId(markersCollection)
		if findErr != nil {
			return storageError()
		}
		marker := core.NewRecord(collection)
		marker.Set("source_key", markerKey)
		marker.Set("source_path", sourcePath)
		marker.Set("record_count", imported)
		marker.Set("completed_at", service.now())
		if err := txApp.Save(marker); err != nil {
			return storageError()
		}
		return nil
	})
	if err != nil {
		return ImportResult{}, err
	}
	return ImportResult{Status: ImportStatusImported, RecordCount: imported}, nil
}

// validateLegacyRow proves a legacy row belongs to this workspace and that
// its column identity matches the payload's own embedded identity. Column
// values alone never authorize an import.
func (service *Service) validateLegacyRow(row record) error {
	projectKey := service.ProjectKey()
	if row.ProjectKey != projectKey {
		return requestError(
			"plugin.import_source_invalid",
			"legacy record does not belong to this workspace",
			map[string]any{"kind": row.Kind, "projectKey": row.ProjectKey},
		)
	}
	switch row.Kind {
	case KindInstallation:
		identity, err := validateSnapshot(row.Payload)
		if err != nil {
			return err
		}
		if identity.projectKey != row.ProjectKey || identity.pluginID != row.PluginID ||
			row.ItemKey != "current" {
			return rowIdentityInvalid(row)
		}
	case KindRevision:
		identity, itemKey, err := validateRevisionPayload(row.Payload)
		if err != nil {
			return err
		}
		if identity.projectKey != row.ProjectKey || identity.pluginID != row.PluginID ||
			itemKey != row.ItemKey {
			return rowIdentityInvalid(row)
		}
	case KindSetting:
		projectKey, pluginID, settingKey, err := validateSettingPayload(row.Payload)
		if err != nil {
			return err
		}
		if projectKey != row.ProjectKey || pluginID != row.PluginID ||
			settingKey != row.ItemKey {
			return rowIdentityInvalid(row)
		}
	case KindAudit:
		projectKey, pluginID, eventID, err := validateAuditPayload(row.Payload)
		if err != nil {
			return err
		}
		if projectKey != row.ProjectKey || pluginID != row.PluginID ||
			eventID != row.ItemKey {
			return rowIdentityInvalid(row)
		}
	default:
		return rowIdentityInvalid(row)
	}
	return nil
}

func rowIdentityInvalid(row record) *Error {
	return requestError(
		"plugin.import_source_invalid",
		"legacy record identity does not match its payload",
		map[string]any{
			"kind": row.Kind, "projectKey": row.ProjectKey,
			"pluginId": row.PluginID, "itemKey": row.ItemKey,
		},
	)
}

func (service *Service) findMarker(sourceKey string) (*ImportMarker, error) {
	stored, err := service.app.FindFirstRecordByFilter(
		markersCollection, "source_key={:key}", dbx.Params{"key": sourceKey},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError()
	}
	return &ImportMarker{
		SourceKey:   stored.GetString("source_key"),
		RecordCount: int64(stored.GetInt("record_count")),
	}, nil
}

type ImportMarker struct {
	SourceKey   string
	RecordCount int64
}

func (service *Service) LegacyImportComplete() (bool, error) {
	marker, err := service.findMarker(service.ImportMarkerKey())
	return marker != nil, err
}

const (
	ImportStatusImported        = "imported"
	ImportStatusAlreadyComplete = "already_complete"
)

type ImportResult struct {
	Status      string `json:"status"`
	RecordCount int64  `json:"recordCount"`
}

// ---- storage helpers ----

func recordFromStored(stored *core.Record) (record, error) {
	raw, err := json.Marshal(stored.GetRaw("payload_json"))
	if err != nil {
		return record{}, storageError()
	}
	if !json.Valid(raw) || len(raw) > maxPayloadBytes {
		return record{}, storageError()
	}
	return record{
		Kind:       stored.GetString("kind"),
		ProjectKey: stored.GetString("project_key"),
		PluginID:   stored.GetString("plugin_id"),
		ItemKey:    stored.GetString("item_key"),
		Payload:    raw,
		Seq:        int64(stored.GetInt("seq")),
	}, nil
}

func findRecord(
	app core.App,
	kind string,
	projectKey string,
	pluginID string,
	itemKey string,
) (*core.Record, error) {
	stored, err := app.FindFirstRecordByFilter(
		recordsCollection,
		"kind={:kind} && project_key={:project} && plugin_id={:plugin} && item_key={:item}",
		dbx.Params{
			"kind": kind, "project": projectKey,
			"plugin": pluginID, "item": itemKey,
		},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError()
	}
	return stored, nil
}

func findInstallation(
	app core.App,
	projectKey string,
	pluginID string,
) (*record, error) {
	stored, err := findRecord(app, KindInstallation, projectKey, pluginID, "current")
	if err != nil || stored == nil {
		return nil, err
	}
	decoded, decodeErr := recordFromStored(stored)
	if decodeErr != nil {
		return nil, decodeErr
	}
	return &decoded, nil
}

func saveRecord(app core.App, row record, exists bool) error {
	collection, err := app.FindCollectionByNameOrId(recordsCollection)
	if err != nil {
		return storageError()
	}
	var stored *core.Record
	if exists {
		found, findErr := findRecord(app, row.Kind, row.ProjectKey, row.PluginID, row.ItemKey)
		if findErr != nil {
			return findErr
		}
		if found == nil {
			return storageError()
		}
		stored = found
	} else {
		stored = core.NewRecord(collection)
	}
	stored.Set("kind", row.Kind)
	stored.Set("project_key", row.ProjectKey)
	stored.Set("plugin_id", row.PluginID)
	stored.Set("item_key", row.ItemKey)
	stored.Set("payload_json", pbtypes.JSONRaw(row.Payload))
	stored.Set("seq", row.Seq)
	if err := app.Save(stored); err != nil {
		return storageError()
	}
	return nil
}

func nextProjectSeq(
	app core.App,
	kind string,
	projectKey string,
) (int64, error) {
	var current int64
	err := app.ConcurrentDB().NewQuery(
		"SELECT COALESCE(MAX(seq), 0) FROM " + recordsCollection +
			" WHERE kind={:kind} AND project_key={:project}",
	).Bind(dbx.Params{"kind": kind, "project": projectKey}).Row(&current)
	if err != nil || current < 0 {
		return 0, storageError()
	}
	return current + 1, nil
}

func saveOutboxEvent(
	app core.App,
	event CatalogChangedEvent,
) error {
	collection, err := app.FindCollectionByNameOrId("vibetable_outbox")
	if err != nil {
		return storageError()
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return storageError()
	}
	stored := core.NewRecord(collection)
	stored.Set("event_id", event.EventID)
	stored.Set("topic", event.Topic)
	stored.Set("payload_json", pbtypes.JSONRaw(raw))
	stored.Set("status", "pending")
	stored.Set("attempts", 0)
	if err := app.Save(stored); err != nil {
		return storageError()
	}
	return nil
}

func catalogChangedEvent(
	eventID string,
	occurredAt time.Time,
	snapshot json.RawMessage,
) (CatalogChangedEvent, error) {
	revision := jsonInt(snapshot, "revision")
	pluginID := snapshotID(snapshot)
	// Shared absolute paths describe another machine after replication. They
	// never cross SSE and never authorize resource access on this machine.
	projected, err := updateSnapshot(snapshot, map[string]any{"sourceLocation": "host-managed", "developmentSourceLocation": nil})
	if err != nil {
		return CatalogChangedEvent{}, err
	}
	return CatalogChangedEvent{
		ContractVersion: mutationpkg.ContractVersion,
		Topic:           "plugin.catalog.changed",
		EventID:         eventID,
		OccurredAt:      occurredAt.Format(time.RFC3339),
		Contract:        "vibetable.plugin-event.v1", EventType: "plugin.catalog.changed",
		ProjectKey: jsonText(snapshot, "projectKey"), EntityID: pluginID,
		Revision: revision, Snapshot: projected,
	}, nil
}

type CatalogChangedEvent struct {
	ContractVersion string          `json:"contractVersion"`
	Topic           string          `json:"topic"`
	EventID         string          `json:"eventId"`
	OccurredAt      string          `json:"occurredAt"`
	Contract        string          `json:"contract"`
	EventType       string          `json:"eventType"`
	ProjectKey      string          `json:"projectKey"`
	EntityID        string          `json:"entityId"`
	Revision        int64           `json:"revision"`
	Snapshot        json.RawMessage `json:"snapshot"`
}

// ---- payload helpers ----

type payloadIdentity struct {
	projectKey string
	pluginID   string
}

func decodePayload(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > maxPayloadBytes || !json.Valid(raw) {
		return nil, requestError("plugin.request_invalid", "payload must be one JSON object", nil)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, requestError("plugin.request_invalid", "payload must be one JSON object", nil)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, requestError("plugin.request_invalid", "payload must be one JSON object", nil)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, requestError("plugin.request_invalid", "payload must be one JSON object", nil)
	}
	return object, nil
}

func validateSnapshot(raw json.RawMessage) (payloadIdentity, error) {
	object, err := decodePayload(raw)
	if err != nil {
		return payloadIdentity{}, err
	}
	if err := validateSnapshotFields(object); err != nil {
		return payloadIdentity{}, err
	}
	projectKey := textValue(object["projectKey"])
	pluginID := textValue(object["pluginId"])
	if projectKey == "" || pluginID == "" {
		return payloadIdentity{}, requestError(
			"plugin.request_invalid", "projectKey and pluginId are required", nil,
		)
	}
	if !hasInt(object, "revision") {
		return payloadIdentity{}, requestError(
			"plugin.request_invalid", "snapshot revision is required", nil,
		)
	}
	return payloadIdentity{projectKey: projectKey, pluginID: pluginID}, nil
}

func validateRevisionPayload(raw json.RawMessage) (payloadIdentity, string, error) {
	object, err := decodePayload(raw)
	if err != nil {
		return payloadIdentity{}, "", err
	}
	if err := validateObject(object, "projectKey pluginId version packageHash localPath manifest state", "projectKey pluginId version packageHash localPath state"); err != nil {
		return payloadIdentity{}, "", err
	}
	if state := object["state"]; state != "current" && state != "rollback" && state != "retired" {
		return payloadIdentity{}, "", invalidPayload()
	}
	if err := validateManifest(object["manifest"], textValue(object["pluginId"]), textValue(object["version"])); err != nil {
		return payloadIdentity{}, "", err
	}
	projectKey := textValue(object["projectKey"])
	pluginID := textValue(object["pluginId"])
	packageHash := textValue(object["packageHash"])
	localPath := textValue(object["localPath"])
	if projectKey == "" || pluginID == "" || packageHash == "" || localPath == "" {
		return payloadIdentity{}, "", requestError(
			"plugin.request_invalid",
			"projectKey, pluginId, packageHash and localPath are required", nil,
		)
	}
	return payloadIdentity{projectKey, pluginID}, packageHash, nil
}

func validateSettingPayload(raw json.RawMessage) (string, string, string, error) {
	object, err := decodePayload(raw)
	if err != nil {
		return "", "", "", err
	}
	if err := validateObject(object, "projectKey pluginId settingKey value revision", "projectKey pluginId settingKey"); err != nil {
		return "", "", "", err
	}
	if _, ok := object["value"]; !ok {
		return "", "", "", invalidPayload()
	}
	projectKey := textValue(object["projectKey"])
	pluginID := textValue(object["pluginId"])
	settingKey := textValue(object["settingKey"])
	if projectKey == "" || pluginID == "" || settingKey == "" {
		return "", "", "", requestError(
			"plugin.request_invalid",
			"projectKey, pluginId and settingKey are required", nil,
		)
	}
	if !hasInt(object, "revision") {
		return "", "", "", requestError(
			"plugin.request_invalid", "setting revision is required", nil,
		)
	}
	return projectKey, pluginID, settingKey, nil
}

func validateAuditPayload(raw json.RawMessage) (string, string, string, error) {
	object, err := decodePayload(raw)
	if err != nil {
		return "", "", "", err
	}
	if err := validateObject(object, "eventId projectKey pluginId pluginVersion packageHash eventType outcome actionId runId actor risk targetCollection targetCount startedAt finishedAt durationMs errorCode details", "eventId projectKey pluginId pluginVersion packageHash eventType outcome actor startedAt"); err != nil {
		return "", "", "", err
	}
	if _, err := time.Parse(time.RFC3339, textValue(object["startedAt"])); err != nil {
		return "", "", "", invalidPayload()
	}
	if _, ok := object["details"].(map[string]any); !ok {
		return "", "", "", invalidPayload()
	}
	projectKey := textValue(object["projectKey"])
	pluginID := textValue(object["pluginId"])
	eventID := textValue(object["eventId"])
	eventType := textValue(object["eventType"])
	if projectKey == "" || pluginID == "" || eventID == "" || eventType == "" {
		return "", "", "", requestError(
			"plugin.request_invalid",
			"projectKey, pluginId, eventId and eventType are required", nil,
		)
	}
	return projectKey, pluginID, eventID, nil
}

func lifecycleAudit(
	occurredAt time.Time,
	snapshot json.RawMessage,
	eventType string,
) (json.RawMessage, error) {
	object, err := decodePayload(snapshot)
	if err != nil {
		return nil, err
	}
	timestamp := occurredAt.Truncate(time.Second).Format("2006-01-02T15:04:05Z07:00")
	// Field set and order follow the previous owner's serialized
	// PluginAuditEvent (camelCase aliases, explicit nulls, default actor).
	event := map[string]any{
		"eventId":          "audit-" + security.RandomString(16),
		"projectKey":       textValue(object["projectKey"]),
		"pluginId":         textValue(object["pluginId"]),
		"pluginVersion":    textValue(object["version"]),
		"packageHash":      textValue(object["packageHash"]),
		"eventType":        eventType,
		"outcome":          "succeeded",
		"actionId":         nil,
		"runId":            nil,
		"actor":            "local-user",
		"risk":             nil,
		"targetCollection": nil,
		"targetCount":      nil,
		"startedAt":        timestamp,
		"finishedAt":       timestamp,
		"durationMs":       0,
		"errorCode":        nil,
		"details":          map[string]any{},
	}
	return json.Marshal(event)
}

func updateSnapshot(
	raw json.RawMessage,
	changes map[string]any,
) (json.RawMessage, error) {
	object, err := decodePayload(raw)
	if err != nil {
		return nil, err
	}
	for key, value := range changes {
		if value == nil {
			object[key] = nil
			continue
		}
		object[key] = value
	}
	return json.Marshal(object)
}

func clearKey(raw json.RawMessage, key string) json.RawMessage {
	object, err := decodePayload(raw)
	if err != nil {
		return raw
	}
	if _, exists := object[key]; exists {
		object[key] = nil
		updated, marshalErr := json.Marshal(object)
		if marshalErr != nil {
			return raw
		}
		return updated
	}
	return raw
}

func jsonInt(raw json.RawMessage, key string) int64 {
	object, err := decodePayload(raw)
	if err != nil {
		return 0
	}
	number, ok := object[key].(json.Number)
	if !ok {
		return 0
	}
	value, err := number.Int64()
	if err != nil {
		return 0
	}
	return value
}

func hasInt(object map[string]any, key string) bool {
	number, ok := object[key].(json.Number)
	if !ok {
		return false
	}
	value, err := number.Int64()
	return err == nil && value > 0
}

func jsonText(raw json.RawMessage, key string) string {
	object, err := decodePayload(raw)
	if err != nil {
		return ""
	}
	return textValue(object[key])
}

func jsonStrings(raw json.RawMessage, key string) []string {
	object, err := decodePayload(raw)
	if err != nil {
		return nil
	}
	rawList, ok := object[key].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(rawList))
	for _, item := range rawList {
		if value := textValue(item); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func snapshotID(raw json.RawMessage) string {
	return jsonText(raw, "pluginId")
}

func textValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func revisionMatches(actual *int64, expected *int64) bool {
	if actual == nil || expected == nil {
		return actual == nil && expected == nil
	}
	return *actual == *expected
}

func deref(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
