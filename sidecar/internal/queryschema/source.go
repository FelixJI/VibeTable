// Package queryschema adapts normalized product schema metadata to QueryPort
// descriptors without leaking PocketBase collection details into the public
// query contract.
package queryschema

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

type Source struct {
	databaseID string
}

func New(dataDir string) (*Source, error) {
	if dataDir == "" {
		return nil, errors.New("query schema data directory is required")
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve query schema data directory: %w", err)
	}
	sum := sha256.Sum256([]byte(
		"vibetable-local-database-v1\x00" + filepath.Clean(absolute),
	))
	return &Source{databaseID: hex.EncodeToString(sum[:])}, nil
}

func (source *Source) DescribeQueryTable(
	ctx context.Context,
	app core.App,
	tableID string,
) (query.TableDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return query.TableDescriptor{}, err
	}
	if source == nil || source.databaseID == "" || app == nil {
		return query.TableDescriptor{}, &query.ProductError{
			Code: "query.schema.unconfigured", Path: "table",
			Message: "query schema source is not configured",
		}
	}
	catalog := schemaapi.New(app)
	definition, err := catalog.Describe(ctx, tableID)
	if err != nil {
		return query.TableDescriptor{}, mapSchemaError(err)
	}
	dataRevision, err := catalog.GetDataRevision(ctx, tableID)
	if err != nil {
		return query.TableDescriptor{}, mapSchemaError(err)
	}
	fields, err := source.describeFields(ctx, app, definition)
	if err != nil {
		return query.TableDescriptor{}, err
	}
	descriptor := query.TableDescriptor{
		DatabaseID: source.databaseID, TableID: definition.TableID,
		PhysicalName: definition.PhysicalName, PrimaryKey: "id",
		SchemaRevision: definition.SchemaRevision, DataRevision: dataRevision,
		Fields: fields, ArchiveMode: query.ArchiveMode(definition.ArchivePolicy.Mode),
		ArchiveValue: definition.ArchivePolicy.ArchivedValue,
	}
	descriptor.DigestFields = make([]string, 0, len(definition.Fields))
	for _, field := range definition.Fields {
		descriptor.DigestFields = append(
			descriptor.DigestFields,
			field.PhysicalName,
		)
	}
	if definition.ArchivePolicy.FieldID != nil {
		for _, field := range definition.Fields {
			if field.FieldID == *definition.ArchivePolicy.FieldID {
				descriptor.ArchiveField = field.PhysicalName
				break
			}
		}
		if descriptor.ArchiveField == "" {
			return query.TableDescriptor{}, &query.ProductError{
				Code: "query.schema.invalid", Path: "archivePolicy.fieldId",
				Message: "archive field is not queryable",
			}
		}
	}
	return descriptor, nil
}

func (source *Source) describeFields(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
) (map[string]query.FieldDescriptor, error) {
	fields := map[string]query.FieldDescriptor{
		"id": {PhysicalName: "id", Type: query.FieldTypeText},
	}
	for _, field := range definition.Fields {
		if field.DataType == schema.DataTypeSecret ||
			field.DataType == schema.DataTypeHash {
			continue
		}
		descriptor, err := source.describeField(ctx, app, field)
		if err != nil {
			return nil, err
		}
		fields[field.PhysicalName] = descriptor
	}
	return fields, nil
}

func (source *Source) describeField(
	ctx context.Context,
	app core.App,
	field schema.FieldDefinition,
) (query.FieldDescriptor, error) {
	fieldType, err := queryFieldType(field)
	if err != nil {
		return query.FieldDescriptor{}, err
	}
	result := query.FieldDescriptor{
		PhysicalName: field.PhysicalName,
		Type:         fieldType,
		Searchable:   isSearchable(field),
	}
	if field.DataType == schema.DataTypeSelect ||
		field.DataType == schema.DataTypeMultiSelect {
		options, optionErr := schema.EnumStorageOptions(field)
		if optionErr != nil {
			return query.FieldDescriptor{}, &query.ProductError{
				Code: "query.schema.invalid", Path: "fields." + field.PhysicalName,
				Message: optionErr.Error(),
			}
		}
		result.Enum = &query.EnumDescriptor{
			Multiple: field.DataType == schema.DataTypeMultiSelect,
			Options:  make([]query.EnumValueDescriptor, len(options)),
		}
		for index, option := range options {
			result.Enum.Options[index] = query.EnumValueDescriptor{
				Value: option.Value, StorageValue: option.StorageValue,
				LegacyStorageValue: option.LegacyStorageValue,
			}
		}
	}
	if field.DataType != schema.DataTypeRelation || field.Relation == nil {
		return result, nil
	}
	target, err := schemaapi.New(app).Describe(ctx, field.Relation.TargetTableID)
	if err != nil {
		return query.FieldDescriptor{}, mapSchemaError(err)
	}
	targetFields := map[string]query.FieldDescriptor{
		"id": {PhysicalName: "id", Type: query.FieldTypeText},
	}
	for _, targetField := range target.Fields {
		if targetField.DataType == schema.DataTypeSecret ||
			targetField.DataType == schema.DataTypeHash ||
			targetField.DataType == schema.DataTypeRelation {
			continue
		}
		targetType, typeErr := queryFieldType(targetField)
		if typeErr != nil {
			return query.FieldDescriptor{}, typeErr
		}
		targetFields[targetField.PhysicalName] = query.FieldDescriptor{
			PhysicalName: targetField.PhysicalName,
			Type:         targetType,
			Searchable:   isSearchable(targetField),
		}
		if targetField.DataType == schema.DataTypeSelect ||
			targetField.DataType == schema.DataTypeMultiSelect {
			options, optionErr := schema.EnumStorageOptions(targetField)
			if optionErr != nil {
				return query.FieldDescriptor{}, &query.ProductError{
					Code:    "query.schema.invalid",
					Path:    "fields." + targetField.PhysicalName,
					Message: optionErr.Error(),
				}
			}
			enum := &query.EnumDescriptor{
				Multiple: targetField.DataType == schema.DataTypeMultiSelect,
				Options:  make([]query.EnumValueDescriptor, len(options)),
			}
			for index, option := range options {
				enum.Options[index] = query.EnumValueDescriptor{
					Value: option.Value, StorageValue: option.StorageValue,
					LegacyStorageValue: option.LegacyStorageValue,
				}
			}
			descriptor := targetFields[targetField.PhysicalName]
			descriptor.Enum = enum
			targetFields[targetField.PhysicalName] = descriptor
		}
	}
	result.Relation = &query.RelationDescriptor{
		TableName: target.PhysicalName, PrimaryKey: "id",
		Fields: targetFields, Multiple: field.Relation.Cardinality == "many",
	}
	return result, nil
}

func queryFieldType(field schema.FieldDefinition) (query.FieldType, error) {
	dataType := field.DataType
	if dataType == schema.DataTypeFormula && field.Formula != nil {
		dataType = field.Formula.ResultType
	}
	if field.DataType == schema.DataTypeLookup {
		return queryFieldTypeForStorage(field.StorageType)
	}
	switch dataType {
	case schema.DataTypeShortText, schema.DataTypeLongText, schema.DataTypeRichText,
		schema.DataTypeTime, schema.DataTypeEmail, schema.DataTypeURL,
		schema.DataTypeUUID, schema.DataTypeSelect, schema.DataTypeHash:
		return query.FieldTypeText, nil
	case schema.DataTypeBoolean:
		return query.FieldTypeBool, nil
	case schema.DataTypeInteger, schema.DataTypeFloat, schema.DataTypeDecimal:
		return query.FieldTypeNumber, nil
	case schema.DataTypeDate, schema.DataTypeDateTime, schema.DataTypeAutoDate:
		return query.FieldTypeDate, nil
	case schema.DataTypeRelation:
		if field.Relation != nil && field.Relation.Cardinality == "many" {
			return query.FieldTypeMultiRelation, nil
		}
		return query.FieldTypeRelation, nil
	case schema.DataTypeJSON, schema.DataTypeGeoPoint, schema.DataTypeGeoJSON,
		schema.DataTypeFile, schema.DataTypeList, schema.DataTypeMultiSelect:
		return query.FieldTypeJSON, nil
	default:
		return "", &query.ProductError{
			Code: "query.schema.unsupported_field", Path: "fields." + field.PhysicalName,
			Message: "field type is not queryable",
		}
	}
}

func queryFieldTypeForStorage(
	storage schema.StorageType,
) (query.FieldType, error) {
	switch storage {
	case schema.StorageText, schema.StorageEditor, schema.StorageEmail,
		schema.StorageURL, schema.StorageSelect:
		return query.FieldTypeText, nil
	case schema.StorageBool:
		return query.FieldTypeBool, nil
	case schema.StorageNumber:
		return query.FieldTypeNumber, nil
	case schema.StorageDate, schema.StorageAutodate:
		return query.FieldTypeDate, nil
	case schema.StorageRelation:
		return query.FieldTypeRelation, nil
	case schema.StorageJSON, schema.StorageGeoPoint, schema.StorageFile:
		return query.FieldTypeJSON, nil
	default:
		return "", &query.ProductError{
			Code: "query.schema.unsupported_field", Path: "storageType",
			Message: "field storage type is not queryable",
		}
	}
}

func isSearchable(field schema.FieldDefinition) bool {
	switch field.DataType {
	case schema.DataTypeShortText, schema.DataTypeLongText, schema.DataTypeRichText,
		schema.DataTypeEmail, schema.DataTypeURL, schema.DataTypeUUID,
		schema.DataTypeSelect:
		return true
	default:
		return false
	}
}

func mapSchemaError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var productErr *schema.ProductError
	if errors.As(err, &productErr) && productErr.Code == "schema.table.not_found" {
		return &query.ProductError{
			Code: "query.table.not_found", Path: "table",
			Message: "table was not found",
		}
	}
	return &query.ProductError{
		Code: "query.schema.failed", Path: "table",
		Message: "query schema could not be loaded",
	}
}
