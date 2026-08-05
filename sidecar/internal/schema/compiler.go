package schema

import (
	"fmt"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

type RelationCollectionResolver func(tableID string) (string, error)

func CompileField(definition FieldDefinition, resolveRelation RelationCollectionResolver) (core.Field, error) {
	dataType := effectiveDataType(definition)
	required := !definition.Nullable || boolConstraint(definition, ConstraintRequired) ||
		(enumConstraint(definition) != nil && enumConstraint(definition).MinSelected == 1)
	name := definition.PhysicalName
	length := constraintFor(definition, ConstraintLength)
	pattern := constraintFor(definition, ConstraintPattern)
	rangeConstraint := constraintFor(definition, ConstraintRange)

	switch dataType {
	case DataTypeShortText, DataTypeTime, DataTypeUUID:
		field := &core.TextField{Name: name, Required: required}
		if length != nil {
			if length.MinLength != nil {
				field.Min = *length.MinLength
			}
			if length.MaxLength != nil {
				field.Max = *length.MaxLength
			}
		}
		if pattern != nil {
			field.Pattern = pattern.Pattern
		}
		return field, nil
	case DataTypeHash:
		field := &core.PasswordField{Name: name, Required: required, Hidden: true}
		if length != nil {
			if length.MinLength != nil {
				field.Min = *length.MinLength
			}
			if length.MaxLength != nil {
				field.Max = *length.MaxLength
			}
		}
		if pattern != nil {
			field.Pattern = pattern.Pattern
		}
		return field, nil
	case DataTypeSecret:
		field := &core.TextField{Name: name, Required: required, Hidden: true}
		if length != nil {
			if length.MinLength != nil {
				field.Min = *length.MinLength
			}
			if length.MaxLength != nil {
				field.Max = *length.MaxLength
			}
		}
		if pattern != nil {
			field.Pattern = pattern.Pattern
		}
		return field, nil
	case DataTypeLongText, DataTypeRichText:
		return &core.EditorField{Name: name, Required: required}, nil
	case DataTypeBoolean:
		return &core.BoolField{Name: name, Required: required}, nil
	case DataTypeInteger, DataTypeFloat:
		field := &core.NumberField{Name: name, Required: required, OnlyInt: dataType == DataTypeInteger}
		if rangeConstraint != nil {
			field.Min, field.Max = rangeConstraint.Min, rangeConstraint.Max
		}
		return field, nil
	case DataTypeDecimal:
		return nil, unsupported("dataType", "exact decimal storage is not supported by PocketBase number fields")
	case DataTypeDate, DataTypeDateTime:
		return &core.DateField{Name: name, Required: required}, nil
	case DataTypeAutoDate:
		if definition.AutoDate == nil {
			return nil, productError(
				"schema.field.autodate_role_required",
				"autoDate.role",
				"automatic date role is required",
				nil,
			)
		}
		switch definition.AutoDate.Role {
		case AutoDateRoleCreatedAt:
			return &core.AutodateField{
				Name: name, OnCreate: true, OnUpdate: false, System: false,
			}, nil
		case AutoDateRoleUpdatedAt:
			return &core.AutodateField{
				Name: name, OnCreate: true, OnUpdate: true, System: false,
			}, nil
		default:
			return nil, productError(
				"schema.field.autodate_role_invalid",
				"autoDate.role",
				"automatic date role must be createdAt or updatedAt",
				map[string]any{"role": definition.AutoDate.Role},
			)
		}
	case DataTypeEmail:
		return &core.EmailField{Name: name, Required: required}, nil
	case DataTypeURL:
		return &core.URLField{Name: name, Required: required}, nil
	case DataTypeSelect, DataTypeMultiSelect:
		enum := enumConstraint(definition)
		if enum == nil {
			return nil, fmt.Errorf("enum constraint is required")
		}
		values, err := EnumStorageValues(definition)
		if err != nil {
			return nil, err
		}
		maxSelect := 1
		if dataType == DataTypeMultiSelect {
			if enum.MaxSelected != nil {
				maxSelect = *enum.MaxSelected
			} else {
				maxSelect = len(values)
			}
		}
		return &core.SelectField{Name: name, Required: required, Values: values, MaxSelect: maxSelect}, nil
	case DataTypeJSON, DataTypeGeoJSON, DataTypeList:
		return &core.JSONField{Name: name, Required: required}, nil
	case DataTypeGeoPoint:
		return &core.GeoPointField{Name: name, Required: required}, nil
	case DataTypeFile:
		policy := definition.AttachmentPolicy
		if policy == nil {
			return nil, fmt.Errorf("attachment policy is required")
		}
		return &core.FileField{
			Name: name, Required: required, MaxSelect: policy.MaxFiles,
			MaxSize: policy.MaxBytesPerFile, MimeTypes: append([]string(nil), policy.AllowedMIMETypes...),
			Thumbs: append([]string(nil), policy.ThumbnailVariants...), Protected: policy.Protected,
		}, nil
	case DataTypeRelation:
		if definition.Relation == nil || resolveRelation == nil {
			return nil, fmt.Errorf("relation resolver is required")
		}
		if relationMode(*definition.Relation) != "direct" {
			// Junction-backed relations are projections. Junction rows are the
			// sole authority and are mutated through the same MutationKernel;
			// this local JSON field is read-only and never accepted as input.
			return &core.JSONField{Name: name}, nil
		}
		collectionID, err := resolveRelation(definition.Relation.TargetTableID)
		if err != nil {
			return nil, err
		}
		// PocketBase treats MaxSelect <= 1 as scalar. Product-level many
		// relations intentionally have no fixed record-count cap, so represent
		// that contract with PocketBase's maximum JSON-safe integer.
		maxSelect := int(1<<53 - 1)
		if definition.Relation.Cardinality == "one" {
			maxSelect = 1
		}
		return &core.RelationField{
			Name: name, Required: required, CollectionId: collectionID,
			CascadeDelete: definition.Relation.DeletePolicy == "cascade",
			MinSelect:     boolToInt(required), MaxSelect: maxSelect,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported compiled data type %q", dataType)
	}
}

func CompileIndexes(definition TableDefinition) []string {
	fieldNames := make(map[string]string, len(definition.Fields))
	for _, field := range definition.Fields {
		fieldNames[field.FieldID] = field.PhysicalName
	}
	result := make([]string, 0, len(definition.Indexes))
	for _, index := range definition.Indexes {
		result = append(result, compileIndex(index.Name, index.FieldIDs, index.Unique, definition.PhysicalName, fieldNames))
	}
	for fieldIndex, field := range definition.Fields {
		if boolConstraint(field, ConstraintUnique) {
			result = append(result, compileIndex(
				fmt.Sprintf("uniq_%s_%s", definition.PhysicalName, field.PhysicalName),
				[]string{field.FieldID}, true, definition.PhysicalName, fieldNames,
			))
		}
		for constraintIndex, constraint := range field.Constraints {
			if constraint.Kind != ConstraintIndex {
				continue
			}
			result = append(result, compileIndex(
				fmt.Sprintf("idx_%s_%d_%d", definition.PhysicalName, fieldIndex, constraintIndex),
				constraint.FieldIDs, constraint.Unique, definition.PhysicalName, fieldNames,
			))
		}
	}
	return result
}

func compileIndex(name string, fieldIDs []string, unique bool, tableName string, fieldNames map[string]string) string {
	columns := make([]string, 0, len(fieldIDs))
	for _, fieldID := range fieldIDs {
		columns = append(columns, "`"+fieldNames[fieldID]+"`")
	}
	prefix := "CREATE "
	if unique {
		prefix += "UNIQUE "
	}
	return fmt.Sprintf("%sINDEX `%s` ON `%s` (%s)", prefix, name, tableName, strings.Join(columns, ", "))
}

func boolConstraint(field FieldDefinition, kind ConstraintKind) bool {
	constraint := constraintFor(field, kind)
	if constraint == nil {
		return false
	}
	value, _ := constraint.Value.(bool)
	return value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
