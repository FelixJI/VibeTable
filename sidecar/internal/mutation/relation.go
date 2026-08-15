package mutation

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func relationSchemaField(
	definition schemaexecution.Table,
	fieldID string,
) (v2.FieldDefinition, bool) {
	for _, field := range definition.Snapshot.Fields {
		if field.Identity.FieldID == fieldID {
			return field, true
		}
	}
	return v2.FieldDefinition{}, false
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
	definition schemaexecution.Table,
	field v2.FieldDefinition,
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
		if field.Relation.TargetTableID == definition.Snapshot.TableID {
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
