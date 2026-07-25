package mutation

import (
	"encoding/json"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

func (kernel *Kernel) applyArchive(
	_ core.App,
	definition schema.TableDefinition,
	record *core.Record,
) error {
	field, err := archiveField(definition)
	if err != nil {
		return err
	}
	switch definition.ArchivePolicy.Mode {
	case schema.ArchiveModeStatus:
		current := schema.DecodeSelectValueFromStorage(
			field,
			record.GetRaw(field.PhysicalName),
		)
		if schema.ProductValuesEqual(current, definition.ArchivePolicy.ArchivedValue) {
			return mutationError("mutation.archive.already_archived", nil, "record is already archived", nil, false)
		}
		storageValue, encodeErr := encodeFieldStorageValue(
			record,
			field,
			definition.ArchivePolicy.ArchivedValue,
		)
		if encodeErr != nil {
			return mutationError(
				"mutation.schema.storage_mismatch", nil,
				"archive value cannot be represented by the active storage schema",
				map[string]any{"fieldId": field.FieldID, "cause": encodeErr.Error()},
				false,
			)
		}
		record.Set(field.PhysicalName, storageValue)
	case schema.ArchiveModeDeletedAt:
		if record.GetString(field.PhysicalName) != "" {
			return mutationError("mutation.archive.already_archived", nil, "record is already archived", nil, false)
		}
		record.Set(field.PhysicalName, kernel.now().UTC())
	default:
		return mutationError("mutation.archive.unsupported", nil, "table has no archive policy", nil, false)
	}
	return nil
}

func (kernel *Kernel) applyRestore(
	app core.App,
	definition schema.TableDefinition,
	record *core.Record,
) error {
	field, err := archiveField(definition)
	if err != nil {
		return err
	}
	switch definition.ArchivePolicy.Mode {
	case schema.ArchiveModeStatus:
		current := schema.DecodeSelectValueFromStorage(
			field,
			record.GetRaw(field.PhysicalName),
		)
		if !schema.ProductValuesEqual(current, definition.ArchivePolicy.ArchivedValue) {
			return mutationError("mutation.restore.not_archived", nil, "record is not archived", nil, false)
		}
		records, err := app.FindRecordsByFilter(
			"vibetable_audit_events",
			"table_id={:table} && record_id={:record}",
			"-occurred_at,-sequence", 0, 0,
			dbx.Params{"table": definition.TableID, "record": record.Id},
		)
		if err != nil {
			return mutationError("mutation.restore.unavailable", nil, "archive history is unavailable", nil, false)
		}
		var archiveRecord *core.Record
		for _, candidate := range records {
			if candidate.GetString("operation") == string(OperationArchive) {
				archiveRecord = candidate
				break
			}
		}
		if archiveRecord == nil {
			return mutationError("mutation.restore.unavailable", nil, "archive history is unavailable", nil, false)
		}
		raw, _ := json.Marshal(archiveRecord.GetRaw("before_json"))
		var before map[string]any
		if json.Unmarshal(raw, &before) != nil {
			return storageFailure()
		}
		previous, exists := before[field.PhysicalName]
		if !exists {
			return mutationError(
				"mutation.restore.unavailable", nil,
				"archive history has no previous archive value", nil, false,
			)
		}
		storageValue, encodeErr := encodeFieldStorageValue(record, field, previous)
		if encodeErr != nil {
			return mutationError(
				"mutation.schema.storage_mismatch", nil,
				"restored value cannot be represented by the active storage schema",
				map[string]any{"fieldId": field.FieldID, "cause": encodeErr.Error()},
				false,
			)
		}
		record.Set(field.PhysicalName, storageValue)
	case schema.ArchiveModeDeletedAt:
		if record.GetString(field.PhysicalName) == "" {
			return mutationError("mutation.restore.not_archived", nil, "record is not archived", nil, false)
		}
		record.Set(field.PhysicalName, nil)
	default:
		return mutationError("mutation.archive.unsupported", nil, "table has no archive policy", nil, false)
	}
	return nil
}

func archiveField(definition schema.TableDefinition) (schema.FieldDefinition, error) {
	if definition.ArchivePolicy.FieldID == nil {
		return schema.FieldDefinition{}, mutationError("mutation.archive.invalid_policy", nil, "archive policy has no field", nil, false)
	}
	for _, field := range definition.Fields {
		if field.FieldID == *definition.ArchivePolicy.FieldID {
			return field, nil
		}
	}
	return schema.FieldDefinition{}, mutationError("mutation.archive.invalid_policy", nil, "archive field does not exist", nil, false)
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}
