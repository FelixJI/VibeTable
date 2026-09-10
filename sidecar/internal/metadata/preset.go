package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/google/uuid"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

// PresetService owns persisted view definitions without querying their live schema.
type PresetService struct{ metadata *Service }

func NewPreset(app core.App) *PresetService { return &PresetService{metadata: New(app)} }

type PresetError struct{ Code, Message, Field string }

func (e *PresetError) Error() string { return e.Message }
func presetMutationError(err error) error {
	var storage *Error
	if errors.As(err, &storage) {
		switch storage.Code {
		case "metadata.revision_conflict":
			return &PresetError{"preset_edit_conflict", "Preset changed elsewhere.", "expectedRevision"}
		case "metadata.idempotency_conflict":
			return &PresetError{"preset_idempotency_conflict", "Operation was used for another Preset request.", "operationId"}
		}
	}
	return err
}
func (service *PresetService) List(ctx context.Context, collection string) (map[string]any, error) {
	items, err := service.metadata.List(ctx, NamespacePresets)
	if err != nil {
		return nil, err
	}
	type entry struct {
		key   string
		value map[string]any
	}
	entries := []entry{}
	for _, item := range items {
		payload, err := decodePresetObject(item.Payload)
		if err != nil {
			return nil, storageError()
		}
		if payload["scope"] != collection {
			continue
		}
		view, ok := payload["view"].(map[string]any)
		if !ok {
			view = map[string]any{}
		}
		normalized, err := normalizePresetView(view)
		if err != nil {
			return nil, storageError()
		}
		name, _ := payload["name"].(string)
		scope := "personal"
		if payload["presetScope"] == "system" || payload["presetScope"] == "role" {
			scope = payload["presetScope"].(string)
		}
		userID, _ := payload["userId"].(string)
		if _, valid := presetString(name, 0, 256); !valid {
			return nil, storageError()
		}
		if _, valid := presetString(userID, 0, 128); !valid {
			return nil, storageError()
		}
		var user any
		if userID != "" {
			user = userID
		}
		key, _ := payload["key"].(string)
		if key == "" {
			key = item.LogicalID
		}
		entries = append(entries, entry{key, map[string]any{"id": item.LogicalID, "collection": collection, "name": name, "scope": scope, "view": normalized, "userId": user, "revision": item.Revision, "changeSetId": nil, "emittedEvents": []string{}}})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	values := make([]map[string]any, len(entries))
	for i, entry := range entries {
		values[i] = entry.value
	}
	return map[string]any{"collection": collection, "presets": values}, nil
}
func (service *PresetService) Save(ctx context.Context, request PresetRequest) (map[string]any, error) {
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("vibetable:preset:"+request.OperationID)).String()
	if request.PresetID != nil {
		id = *request.PresetID
	}
	key := "preset:save:" + request.OperationID
	if !logicalIDPattern.MatchString(id) {
		return nil, invalidRequest("presetId", "invalid Preset ID")
	}
	if err := validateIdempotencyKey(key); err != nil {
		return nil, err
	}
	digest, err := hashValue(request)
	if err != nil {
		return nil, err
	}
	result, err := executeIdempotent(service.metadata, ctx, key, digest, func(tx core.App) (presetReceipt, []metadataChange, error) {
		current, err := presetCurrent(tx, id)
		if err != nil {
			return nil, nil, err
		}
		expected := ""
		if request.ExpectedRevision != nil {
			expected = *request.ExpectedRevision
		}
		if (current == nil && expected != "") || (current != nil && expected != current.Revision) {
			return nil, nil, &PresetError{"preset_edit_conflict", "Preset changed elsewhere.", "expectedRevision"}
		}
		payload := map[string]any{}
		if current != nil {
			payload, err = decodePresetObject(current.Payload)
			if err != nil {
				return nil, nil, storageError()
			}
			delete(payload, "id")
			delete(payload, "revision")
		}
		// Preserve extension fields just as the old adapter did; known authored fields win.
		payload["scope"] = request.Collection
		payload["name"] = request.Name
		payload["presetScope"] = "system"
		payload["view"] = request.View
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, err
		}
		item, change, err := service.metadata.upsert(tx, NamespacePresets, ItemMutation{LogicalID: id, Payload: raw, ExpectedRevision: expected}, "", "")
		if err != nil {
			return nil, nil, err
		}
		return map[string]any{"id": id, "collection": request.Collection, "name": request.Name, "scope": "system", "view": request.View, "userId": nil, "revision": item.Revision, "changeSetId": nil, "emittedEvents": []string{}}, []metadataChange{change}, nil
	}, decoratePresetReceipt, func(*presetReceipt) {}, func() error { return writecoordinator.ReplayedBusinessWrite(ctx, "metadata.presets.upsert", key) })
	return result, presetMutationError(err)
}
func (service *PresetService) Delete(ctx context.Context, request PresetRequest) (map[string]any, error) {
	if request.PresetID == nil || request.ExpectedRevision == nil {
		return nil, errPresetParams
	}
	key := "preset:delete:" + request.OperationID
	if err := validateIdempotencyKey(key); err != nil {
		return nil, err
	}
	digest, err := hashValue(request)
	if err != nil {
		return nil, err
	}
	result, err := executeIdempotent(service.metadata, ctx, key, digest, func(tx core.App) (presetReceipt, []metadataChange, error) {
		current, err := presetCurrent(tx, *request.PresetID)
		if err != nil {
			return nil, nil, err
		}
		if current == nil || current.Revision != *request.ExpectedRevision {
			return nil, nil, &PresetError{"preset_edit_conflict", "Preset changed elsewhere.", "expectedRevision"}
		}
		change, err := service.metadata.delete(tx, NamespacePresets, ItemDelete{LogicalID: *request.PresetID, ExpectedRevision: *request.ExpectedRevision}, "", "")
		return map[string]any{"deleted": *request.PresetID, "status": StatusApplied}, []metadataChange{change}, err
	}, decoratePresetReceipt, func(*presetReceipt) {}, func() error { return writecoordinator.ReplayedBusinessWrite(ctx, "metadata.presets.delete", key) })
	return result, presetMutationError(err)
}
func decoratePresetReceipt(result *presetReceipt, id string, events []string) {
	(*result)["changeSetId"] = id
	(*result)["emittedEvents"] = events
}
func presetCurrent(app core.App, id string) (*Item, error) {
	collection, err := resolveCollection(app, NamespacePresets)
	if err != nil {
		return nil, err
	}
	record, err := findRecord(app, collection, id)
	if err != nil || record == nil {
		return nil, err
	}
	item, err := itemFromRecord(NamespacePresets, record)
	return &item, err
}
