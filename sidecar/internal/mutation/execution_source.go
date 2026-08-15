package mutation

import (
	"context"
	"errors"

	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// MetadataSchemaSource preserves the public mutation construction seam while
// loading only the authoritative Schema V2 execution snapshot.
type MetadataSchemaSource struct{}

func (MetadataSchemaSource) Describe(
	ctx context.Context,
	app core.App,
	tableID string,
) (schemaexecution.Table, error) {
	definition, err := schemaexecution.Describe(ctx, app, tableID)
	if err != nil {
		if ctx.Err() != nil {
			return schemaexecution.Table{}, ctx.Err()
		}
		if errors.Is(err, schemaexecution.ErrTableNotFound) {
			return schemaexecution.Table{}, mutationError(
				"mutation.table.not_found", stringPointer("tableId"),
				"table was not found", nil, false,
			)
		}
		return schemaexecution.Table{}, storageFailure()
	}
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		return schemaexecution.Table{}, storageFailure()
	}
	roles := map[string]string{}
	for _, field := range definition.Snapshot.Fields {
		if field.LogicalType != v2.LogicalAutoDate {
			continue
		}
		pbField, ok := collection.Fields.GetByName(
			field.Identity.PhysicalName,
		).(*core.AutodateField)
		if !ok {
			return schemaexecution.Table{}, invalidAutoDateSchema(
				field, "automatic date field is missing from PocketBase", nil,
			)
		}
		actual := ""
		switch {
		case pbField.OnCreate && !pbField.OnUpdate:
			actual = "createdAt"
		case pbField.OnCreate && pbField.OnUpdate:
			actual = "updatedAt"
		default:
			return schemaexecution.Table{}, invalidAutoDateSchema(
				field, "automatic date switches do not map to a supported role",
				map[string]any{"onCreate": pbField.OnCreate, "onUpdate": pbField.OnUpdate},
			)
		}
		if field.AutoDate == nil || field.AutoDate.Role != actual {
			return schemaexecution.Table{}, invalidAutoDateSchema(
				field, "automatic date role conflicts with PocketBase switches",
				map[string]any{"actualRole": actual},
			)
		}
		if previousFieldID, duplicate := roles[actual]; duplicate {
			return schemaexecution.Table{}, invalidAutoDateSchema(
				field, "automatic date role is duplicated",
				map[string]any{"previousFieldId": previousFieldID, "role": actual},
			)
		}
		roles[actual] = field.Identity.FieldID
	}
	return definition, nil
}

func invalidAutoDateSchema(
	field v2.FieldDefinition,
	message string,
	details map[string]any,
) *ProductError {
	if details == nil {
		details = map[string]any{}
	}
	details["fieldId"] = field.Identity.FieldID
	return mutationError(
		"mutation.schema.autodate_invalid",
		stringPointer(field.Identity.PhysicalName),
		message,
		details,
		false,
	)
}
