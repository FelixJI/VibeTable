package metadata

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"github.com/google/uuid"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type VersionError struct{ Code, Message string }

func (e *VersionError) Error() string         { return e.Code + ": " + e.Message }
func versionError(code, message string) error { return &VersionError{Code: code, Message: message} }

type VersionHistory interface {
	ReadBusinessHistory(context.Context, audit.ReadParams) (audit.Page, error)
	PreviewBusinessHistoryRestore(context.Context, audit.PreviewParams) (audit.Preview, error)
}
type VersionRestoreCommitter interface {
	ApplyRestoreWithCommit(context.Context, audit.ApplyParams, func(core.App, audit.RestoreResult) error) (audit.RestoreResult, error)
}
type ContentVersions struct {
	metadata *Service
	history  VersionHistory
	restorer VersionRestoreCommitter
}

func NewContentVersions(app core.App, history VersionHistory, restorer VersionRestoreCommitter) *ContentVersions {
	return &ContentVersions{metadata: New(app), history: history, restorer: restorer}
}
func VersionWriteKind(method string) string {
	return "metadata.content_versions." + method[len("version."):]
}
func versionIdentity(p VersionParams) string { return p.Collection + ":" + p.ItemID }
func versionDecode(raw json.RawMessage) (map[string]any, error) {
	var payload map[string]any
	if !json.Valid(raw) {
		return nil, versionError("version_storage_invalid", "Stored named revision is invalid.")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload == nil {
		return nil, versionError("version_storage_invalid", "Stored named revision is invalid.")
	}
	return payload, nil
}
func versionText(value any) string { v, _ := value.(string); return v }
func versionEntry(item Item, payload map[string]any) map[string]any {
	outdated := false
	switch value := payload["outdated"].(type) {
	case bool:
		outdated = value
	case string:
		outdated = value != ""
	case json.Number:
		number, _ := value.Float64()
		outdated = number != 0
	case []any:
		outdated = len(value) != 0
	case map[string]any:
		outdated = len(value) != 0
	}
	return map[string]any{"id": item.LogicalID, "key": versionText(payload["key"]), "name": versionText(payload["name"]), "outdated": outdated, "mainHash": versionText(payload["mainHash"]), "revision": item.Revision, "changeSetId": nil, "emittedEvents": []string{}}
}
func validateVersionRecord(ctx context.Context, app core.App, p VersionParams) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	definition, err := schemaexecution.Describe(ctx, app, p.Collection)
	if err != nil {
		return versionError("version_record_unavailable", "The named revision record is unavailable.")
	}
	if _, err := app.FindRecordById(definition.PhysicalName, p.ItemID); err != nil {
		return versionError("version_record_unavailable", "The named revision record is unavailable.")
	}
	return nil
}
func findVersion(app core.App, p VersionParams) (Item, map[string]any, error) {
	collection, err := resolveCollection(app, NamespaceContentVersions)
	if err != nil {
		return Item{}, nil, err
	}
	record, err := findRecord(app, collection, p.VersionID)
	if err != nil {
		return Item{}, nil, err
	}
	if record == nil {
		return Item{}, nil, versionError("version_not_found", "Named revision was not found.")
	}
	item, err := itemFromRecord(NamespaceContentVersions, record)
	if err != nil {
		return Item{}, nil, err
	}
	payload, err := versionDecode(item.Payload)
	if err != nil {
		return Item{}, nil, err
	}
	if versionText(payload["scope"]) != versionIdentity(p) {
		return Item{}, nil, versionError("version_not_found", "Named revision was not found.")
	}
	return item, payload, nil
}
func (s *ContentVersions) List(ctx context.Context, p VersionParams) (any, error) {
	var result any
	err := s.metadata.app.RunInTransaction(func(tx core.App) error {
		if err := validateVersionRecord(ctx, tx, p); err != nil {
			return err
		}
		items, err := New(tx).List(ctx, NamespaceContentVersions)
		if err != nil {
			return err
		}
		versions := []map[string]any{}
		for _, item := range items {
			payload, err := versionDecode(item.Payload)
			if err != nil {
				return err
			}
			if versionText(payload["scope"]) == versionIdentity(p) {
				versions = append(versions, versionEntry(item, payload))
			}
		}
		sort.SliceStable(versions, func(i, j int) bool {
			a, b := versionText(versions[i]["key"]), versionText(versions[j]["key"])
			if a == "" {
				a = versions[i]["id"].(string)
			}
			if b == "" {
				b = versions[j]["id"].(string)
			}
			return a < b
		})
		result = map[string]any{"collection": p.Collection, "itemId": p.ItemID, "versions": versions}
		return nil
	})
	return result, versionPersistence(err)
}
func (s *ContentVersions) latest(ctx context.Context, p VersionParams) (string, string, string, error) {
	page, err := s.history.ReadBusinessHistory(ctx, audit.ReadParams{TableID: p.Collection, ItemID: &p.ItemID, Scope: "row", Limit: 1, Actions: []string{}})
	if err != nil {
		return "", "", "", err
	}
	if len(page.ChangeSets) == 0 {
		return "", "", "", versionError("version_audit_missing", "An audit revision is required before naming a revision.")
	}
	change := page.ChangeSets[0]
	revision := ""
	for _, record := range change.RecordChanges {
		if record.ItemID == p.ItemID && record.RevisionID != "" {
			revision = record.RevisionID
			break
		}
	}
	if revision == "" && change.ItemID != nil && *change.ItemID == p.ItemID {
		revision = change.RootRevisionID
	}
	if change.ChangeSetID == "" || revision == "" {
		return "", "", "", versionError("version_audit_invalid", "The audit revision does not identify this record.")
	}
	preview, err := s.history.PreviewBusinessHistoryRestore(ctx, audit.PreviewParams{TableID: p.Collection, ItemID: p.ItemID, TargetRevision: revision, Scope: "row"})
	if err != nil {
		return "", "", "", err
	}
	if preview.Collection != p.Collection || preview.ItemID != p.ItemID || preview.TargetRevision != revision || preview.CurrentHash == "" {
		return "", "", "", versionError("version_audit_invalid", "The audit revision does not identify this record.")
	}
	return change.ChangeSetID, revision, preview.CurrentHash, nil
}
func (s *ContentVersions) Compare(ctx context.Context, p VersionParams) (any, error) {
	if err := validateVersionRecord(ctx, s.metadata.app, p); err != nil {
		return nil, err
	}
	item, payload, err := findVersion(s.metadata.app, p)
	if err != nil {
		return nil, versionPersistence(err)
	}
	revision := versionText(payload["revisionId"])
	if revision == "" {
		return nil, versionError("version_revision_missing", "Named revision has no audit revision.")
	}
	preview, err := s.history.PreviewBusinessHistoryRestore(ctx, audit.PreviewParams{TableID: p.Collection, ItemID: p.ItemID, TargetRevision: revision, Scope: "row"})
	if err != nil {
		return nil, versionPersistence(err)
	}
	differences := map[string]any{}
	for _, change := range preview.ScalarChanges {
		differences[change.Field] = map[string]any{"main": change.Before, "version": change.After}
	}
	for _, change := range preview.RelationChanges {
		before, after := change.BeforeItemID, change.AfterItemID
		if change.Kind == "m2m" {
			before, after = change.BeforeDisplayValue, change.AfterDisplayValue
		}
		differences[change.Field] = map[string]any{"main": before, "version": after}
	}
	return map[string]any{"collection": p.Collection, "itemId": p.ItemID, "versionId": p.VersionID, "outdated": preview.CurrentHash != versionText(payload["mainHash"]), "mainHash": preview.CurrentHash, "versionRevision": item.Revision, "differences": differences}, nil
}
func (s *ContentVersions) loadReceipt(app core.App, key, digest string) (json.RawMessage, bool, error) {
	record, err := app.FindFirstRecordByFilter("vibetable_idempotency_keys", "key={:key}", dbx.Params{"key": key})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, storageError()
	}
	if !record.GetDateTime("expires_at").Time().After(s.metadata.now()) {
		return nil, false, versionError("version_operation_expired", "The named revision operation has expired.")
	}
	if record.GetString("request_hash") != digest {
		return nil, false, versionError("version_idempotency_conflict", "This operation was used with different named revision parameters.")
	}
	if record.GetString("status") != "applied" {
		return nil, false, storageError()
	}
	raw, err := json.Marshal(record.GetRaw("receipt_json"))
	if err != nil || !json.Valid(raw) || bytes.Equal(raw, []byte("null")) {
		return nil, false, storageError()
	}
	return raw, true, nil
}
func (s *ContentVersions) storeReceipt(app core.App, key, digest string, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	collection, err := app.FindCollectionByNameOrId("vibetable_idempotency_keys")
	if err != nil {
		return err
	}
	record := core.NewRecord(collection)
	record.Set("key", key)
	record.Set("request_hash", digest)
	record.Set("status", "applied")
	record.Set("receipt_json", pbtypes.JSONRaw(raw))
	record.Set("expires_at", s.metadata.now().Add(idempotencyTTL))
	return app.Save(record)
}
func (s *ContentVersions) Write(ctx context.Context, method string, p VersionParams) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(p.Values) != 0 {
		return nil, versionError("version_values_not_allowed", "Named revisions do not accept arbitrary values.")
	}
	digest, err := hashValue(struct {
		Method string
		Params VersionParams
	}{method, p})
	if err != nil {
		return nil, err
	}
	key := "metadata:" + method + ":" + p.OperationID
	raw, found, err := s.loadReceipt(s.metadata.app, key, digest)
	if err != nil {
		return nil, versionPersistence(err)
	}
	if found {
		return raw, writecoordinator.ReplayedBusinessWrite(ctx, VersionWriteKind(method), p.OperationID)
	}
	if err := validateVersionRecord(ctx, s.metadata.app, p); err != nil {
		return nil, err
	}
	if method == "version.promote" {
		return s.promote(ctx, p, key, digest)
	}
	if method == "version.save" {
		current, _, err := findVersion(s.metadata.app, p)
		if err != nil {
			return nil, versionPersistence(err)
		}
		if current.Revision != p.ExpectedRevision {
			return nil, versionError("version_edit_conflict", "Named revision changed; reload before continuing.")
		}
	}
	var auditChange, revision, mainHash string
	if method == "version.create" || method == "version.save" {
		auditChange, revision, mainHash, err = s.latest(ctx, p)
		if err != nil {
			return nil, versionPersistence(err)
		}
	}
	var result any
	err = s.metadata.app.RunInTransaction(func(tx core.App) (transactionErr error) {
		defer func() {
			if transactionErr == nil {
				transactionErr = writecoordinator.PersistPocketBaseReceipt(ctx, tx, s.metadata.now())
			}
		}()
		if err := validateVersionRecord(ctx, tx, p); err != nil {
			return err
		}
		var old Item
		payload := map[string]any{}
		if method != "version.create" {
			var err error
			old, payload, err = findVersion(tx, p)
			if err != nil {
				return err
			}
			if old.Revision != p.ExpectedRevision {
				return versionError("version_edit_conflict", "Named revision changed; reload before continuing.")
			}
		}
		var changes []metadataChange
		if method == "version.delete" {
			change, err := s.metadata.delete(tx, NamespaceContentVersions, ItemDelete{LogicalID: p.VersionID, ExpectedRevision: p.ExpectedRevision}, "version", "")
			if err != nil {
				return err
			}
			changes = []metadataChange{change}
			result = map[string]any{"deleted": p.VersionID}
		} else {
			id := p.VersionID
			expected := p.ExpectedRevision
			if method == "version.create" {
				id = uuid.NewSHA1(uuid.NameSpaceURL, []byte("vibetable:version:"+p.OperationID)).String()
				expected = ""
				name := p.Key
				if name == "" {
					name = id[:8]
				}
				payload = map[string]any{"id": id, "key": name, "name": p.Name, "outdated": false}
			}
			payload["scope"] = versionIdentity(p)
			payload["changeSetId"] = auditChange
			payload["revisionId"] = revision
			payload["mainHash"] = mainHash
			encoded, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			saved, change, err := s.metadata.upsert(tx, NamespaceContentVersions, ItemMutation{LogicalID: id, Payload: encoded, ExpectedRevision: expected}, "version", "")
			if err != nil {
				return err
			}
			changes = []metadataChange{change}
			if method == "version.create" {
				result = versionEntry(saved, payload)
			} else {
				result = map[string]any{"saved": id, "revisionId": revision, "metadataRevision": saved.Revision}
			}
		}
		trace := s.metadata.newID("changeSet")
		events, err := s.metadata.saveTrace(tx, trace, key, changes)
		if err != nil {
			return err
		}
		output := result.(map[string]any)
		output["changeSetId"] = trace
		output["emittedEvents"] = events
		if method != "version.create" {
			output["status"] = "applied"
		}
		if err := s.storeReceipt(tx, key, digest, result); err != nil {
			return err
		}
		return ctx.Err()
	})
	return result, versionPersistence(err)
}
func (s *ContentVersions) promote(ctx context.Context, p VersionParams, key, digest string) (any, error) {
	item, payload, err := findVersion(s.metadata.app, p)
	if err != nil {
		return nil, versionPersistence(err)
	}
	if item.Revision != p.ExpectedRevision {
		return nil, versionError("version_edit_conflict", "Named revision changed; compare it again.")
	}
	revision := versionText(payload["revisionId"])
	if revision == "" {
		return nil, versionError("version_revision_missing", "Named revision has no audit revision.")
	}
	preview, err := s.history.PreviewBusinessHistoryRestore(ctx, audit.PreviewParams{TableID: p.Collection, ItemID: p.ItemID, TargetRevision: revision, Scope: "row"})
	if err != nil {
		return nil, versionPersistence(err)
	}
	if preview.CurrentHash != p.MainHash {
		return nil, versionError("version_main_conflict", "The record changed after comparison.")
	}
	if !preview.CanApply || preview.Token == "" {
		return nil, versionError("version_not_restorable", "Named revision cannot be restored.")
	}
	var result any
	_, err = s.restorer.ApplyRestoreWithCommit(ctx, audit.ApplyParams{TableID: p.Collection, ItemID: p.ItemID, Token: preview.Token}, func(tx core.App, restored audit.RestoreResult) error {
		current, currentPayload, err := findVersion(tx, p)
		if err != nil {
			return err
		}
		if current.Revision != p.ExpectedRevision || versionText(currentPayload["revisionId"]) != revision {
			return versionError("version_edit_conflict", "Named revision changed; compare it again.")
		}
		result = map[string]any{"promoted": p.VersionID, "restoredToRevision": revision, "result": restored}
		return s.storeReceipt(tx, key, digest, result)
	})
	return result, versionPersistence(err)
}
func versionPersistence(err error) error {
	if err == nil || err == writecoordinator.ErrBusinessReplay || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var domain *VersionError
	if errors.As(err, &domain) {
		return domain
	}
	if IsError(err, "metadata.revision_conflict") {
		return versionError("version_edit_conflict", "Named revision changed; reload before continuing.")
	}
	var history *audit.Error
	if errors.As(err, &history) {
		switch history.Code {
		case "restore_conflict", "schema_drift":
			return versionError("version_main_conflict", "The record changed after comparison.")
		case "revision_not_found", "revision_not_restorable", "restore_no_fields":
			return versionError("version_not_restorable", "Named revision cannot be restored.")
		}
	}
	return versionError("version_persistence_failed", "Named revision operation failed.")
}
