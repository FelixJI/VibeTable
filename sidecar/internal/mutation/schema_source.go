package mutation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

type MetadataSchemaSource struct{}

func (MetadataSchemaSource) Describe(
	ctx context.Context,
	app core.App,
	tableID string,
) (schema.TableDefinition, error) {
	if err := ctx.Err(); err != nil {
		return schema.TableDefinition{}, err
	}
	record, err := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": tableID},
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return schema.TableDefinition{}, mutationError(
				"mutation.table.not_found", stringPointer("tableId"),
				"table was not found", nil, false,
			)
		}
		return schema.TableDefinition{}, storageFailure()
	}
	raw, err := json.Marshal(record.GetRaw("definition_json"))
	if err != nil {
		return schema.TableDefinition{}, storageFailure()
	}
	var definition schema.TableDefinition
	if err := json.Unmarshal(raw, &definition); err != nil {
		return schema.TableDefinition{}, storageFailure()
	}
	collection, err := app.FindCollectionByNameOrId(record.GetString("collection_id"))
	if err != nil {
		return schema.TableDefinition{}, storageFailure()
	}
	roles := map[schema.AutoDateRole]string{}
	for index := range definition.Fields {
		field := &definition.Fields[index]
		if field.DataType != schema.DataTypeAutoDate {
			continue
		}
		pbField, ok := collection.Fields.GetByName(
			field.PhysicalName,
		).(*core.AutodateField)
		if !ok {
			return schema.TableDefinition{}, invalidAutoDateSchema(
				field,
				"automatic date field is missing from PocketBase",
				nil,
			)
		}
		var actual schema.AutoDateRole
		switch {
		case pbField.OnCreate && !pbField.OnUpdate:
			actual = schema.AutoDateRoleCreatedAt
		case pbField.OnCreate && pbField.OnUpdate:
			actual = schema.AutoDateRoleUpdatedAt
		default:
			return schema.TableDefinition{}, invalidAutoDateSchema(
				field,
				"automatic date switches do not map to a supported role",
				map[string]any{
					"onCreate": pbField.OnCreate,
					"onUpdate": pbField.OnUpdate,
				},
			)
		}
		if field.AutoDate == nil {
			field.AutoDate = &schema.AutoDateSpec{Role: actual}
		} else if field.AutoDate.Role != actual {
			return schema.TableDefinition{}, invalidAutoDateSchema(
				field,
				"automatic date role conflicts with PocketBase switches",
				map[string]any{"actualRole": actual},
			)
		}
		if previousFieldID, duplicate := roles[actual]; duplicate {
			return schema.TableDefinition{}, invalidAutoDateSchema(
				field,
				"automatic date role is duplicated",
				map[string]any{"previousFieldId": previousFieldID, "role": actual},
			)
		}
		roles[actual] = field.FieldID
	}
	return definition, nil
}

func invalidAutoDateSchema(
	field *schema.FieldDefinition,
	message string,
	details map[string]any,
) *ProductError {
	if details == nil {
		details = map[string]any{}
	}
	details["fieldId"] = field.FieldID
	return mutationError(
		"mutation.schema.autodate_invalid",
		stringPointer(field.PhysicalName),
		message,
		details,
		false,
	)
}
