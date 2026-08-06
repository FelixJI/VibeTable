package mutation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

func validateM2AJunctionValues(
	ctx context.Context,
	app core.App,
	junction schema.TableDefinition,
	operation Operation,
	values map[string]any,
) error {
	// Pure mutation-pipeline unit tests intentionally run without a PocketBase
	// app. Production kernels always provide one; only the storage-backed M2A
	// cross-table validation is unavailable in that isolated test seam.
	if app == nil {
		return nil
	}
	tableRecords, err := app.FindAllRecords("vibetable_tables")
	if err != nil {
		return mutationError(
			"mutation.relation.storage_failed", nil,
			"relation metadata could not be read", nil, true,
		)
	}
	for _, tableRecord := range tableRecords {
		raw, marshalErr := json.Marshal(tableRecord.GetRaw("definition_json"))
		if marshalErr != nil {
			return mutationError(
				"mutation.relation.storage_failed", nil,
				"relation metadata could not be decoded", nil, true,
			)
		}
		var owner schema.TableDefinition
		if unmarshalErr := json.Unmarshal(raw, &owner); unmarshalErr != nil {
			return mutationError(
				"mutation.relation.storage_failed", nil,
				"relation metadata could not be decoded", nil, true,
			)
		}
		for _, projection := range owner.Fields {
			relation := projection.Relation
			if relation == nil || relation.EffectiveMode() != "m2a" ||
				relation.JunctionTableID == nil ||
				*relation.JunctionTableID != junction.TableID {
				continue
			}
			targetField, targetOK := relationSchemaField(
				junction, relation.JunctionTargetFieldID,
			)
			discriminatorField, discriminatorOK := relationSchemaField(
				junction, relation.JunctionDiscriminatorFieldID,
			)
			if !targetOK || !discriminatorOK {
				return mutationError(
					"mutation.relation.schema_invalid", nil,
					"m2a junction fields are unavailable", nil, false,
				)
			}
			targetID := stringValue(values[targetField.PhysicalName])
			targetTableID := stringValue(
				values[discriminatorField.PhysicalName],
			)
			if operation.Kind != OperationInsert &&
				(targetID == "" || targetTableID == "") &&
				operation.RecordID != nil {
				meta, metaErr := app.FindFirstRecordByFilter(
					"vibetable_tables", "table_id={:table}",
					dbx.Params{"table": junction.TableID},
				)
				if metaErr != nil {
					return storageFailure()
				}
				collection, collectionErr := app.FindCollectionByNameOrId(
					meta.GetString("collection_id"),
				)
				if collectionErr != nil {
					return storageFailure()
				}
				current, currentErr := app.FindRecordById(
					collection, *operation.RecordID,
				)
				if currentErr != nil {
					continue
				}
				if targetID == "" {
					targetID = current.GetString(targetField.PhysicalName)
				}
				if targetTableID == "" {
					targetTableID = current.GetString(
						discriminatorField.PhysicalName,
					)
				}
			}
			if targetID == "" && targetTableID == "" {
				continue
			}
			if targetID == "" || targetTableID == "" {
				return mutationError(
					"mutation.relation.m2a_target_invalid", nil,
					"m2a target id and discriminator must be changed together",
					nil, false,
				)
			}
			if !stringIn(relation.AllowedTargetTableIDs, targetTableID) {
				return mutationError(
					"mutation.relation.m2a_target_not_allowed", nil,
					"m2a target table is not allowed",
					map[string]any{"tableId": targetTableID}, false,
				)
			}
			targetMeta, targetMetaErr := app.FindFirstRecordByFilter(
				"vibetable_tables", "table_id={:table}",
				dbx.Params{"table": targetTableID},
			)
			if targetMetaErr != nil {
				return mutationError(
					"mutation.relation.target_table_not_found", nil,
					"relation target table was not found", nil, false,
				)
			}
			targetCollection, collectionErr := app.FindCollectionByNameOrId(
				targetMeta.GetString("collection_id"),
			)
			if collectionErr != nil {
				return storageFailure()
			}
			if _, targetErr := app.FindRecordById(
				targetCollection, targetID,
			); targetErr != nil {
				if errors.Is(targetErr, sql.ErrNoRows) {
					return mutationError(
						"mutation.relation.target_not_found", nil,
						"relation target record was not found",
						map[string]any{"recordId": targetID}, false,
					)
				}
				return storageFailure()
			}
		}
	}
	return ctx.Err()
}

func relationSchemaField(
	definition schema.TableDefinition,
	fieldID string,
) (schema.FieldDefinition, bool) {
	for _, field := range definition.Fields {
		if field.FieldID == fieldID {
			return field, true
		}
	}
	return schema.FieldDefinition{}, false
}

func stringIn(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func validateRelationValue(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	field schema.FieldDefinition,
	value any,
	pendingRecordIDs map[string]struct{},
) (any, error) {
	if field.Relation == nil {
		return nil, mutationError(
			"mutation.relation.schema_invalid", nil,
			"relation metadata is unavailable", nil, false,
		)
	}
	ids, err := normalizeRelationIDs(value)
	if err != nil {
		return nil, err
	}
	if field.Relation.Cardinality == "one" && len(ids) > 1 {
		return nil, mutationError(
			"mutation.relation.cardinality", nil,
			"single relation accepts at most one target", nil, false,
		)
	}
	if len(ids) == 0 {
		if field.Relation.Cardinality == "one" {
			return nil, nil
		}
		return []string{}, nil
	}
	targetMeta, err := app.FindFirstRecordByFilter(
		"vibetable_tables",
		"table_id={:table}",
		dbx.Params{"table": field.Relation.TargetTableID},
	)
	if err != nil {
		return nil, mutationError(
			"mutation.relation.target_table_not_found", nil,
			"relation target table was not found", nil, false,
		)
	}
	collection, err := app.FindCollectionByNameOrId(
		targetMeta.GetString("collection_id"),
	)
	if err != nil {
		return nil, mutationError(
			"mutation.relation.storage_failed", nil,
			"relation target storage is unavailable", nil, true,
		)
	}
	seen := map[string]struct{}{}
	for _, recordID := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, duplicate := seen[recordID]; duplicate {
			return nil, mutationError(
				"mutation.relation.duplicate", nil,
				"relation contains a duplicate target", nil, false,
			)
		}
		seen[recordID] = struct{}{}
		if field.Relation.TargetTableID == definition.TableID {
			if _, pending := pendingRecordIDs[recordID]; pending {
				continue
			}
		}
		if _, err := app.FindRecordById(collection, recordID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, mutationError(
					"mutation.relation.target_not_found", nil,
					"relation target record was not found",
					map[string]any{"recordId": recordID},
					false,
				)
			}
			return nil, mutationError(
				"mutation.relation.storage_failed", nil,
				"relation target record could not be read", nil, true,
			)
		}
	}
	if field.Relation.Cardinality == "one" {
		return ids[0], nil
	}
	return ids, nil
}

func normalizeRelationIDs(value any) ([]string, error) {
	if value == nil {
		return []string{}, nil
	}
	if text, ok := value.(string); ok {
		if text == "" {
			return []string{}, nil
		}
		return []string{text}, nil
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Array && reflected.Kind() != reflect.Slice {
		return nil, mutationError(
			"mutation.relation.invalid_value", nil,
			"relation value must be a record id or an array of record ids",
			nil, false,
		)
	}
	result := make([]string, 0, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		item := reflected.Index(index)
		if item.Kind() == reflect.Interface && !item.IsNil() {
			item = item.Elem()
		}
		if !item.IsValid() || item.Kind() != reflect.String ||
			item.String() == "" {
			return nil, mutationError(
				"mutation.relation.invalid_value", nil,
				"relation target ids must be non-empty strings",
				nil, false,
			)
		}
		result = append(result, item.String())
	}
	return result, nil
}

func withMutationPath(err error, path string) error {
	var productErr *ProductError
	if !errors.As(err, &productErr) {
		return err
	}
	copied := *productErr
	copied.Path = &path
	return &copied
}
