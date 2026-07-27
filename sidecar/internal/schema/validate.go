package schema

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/autodateobs"
)

var physicalNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func Validate(definition TableDefinition) error {
	if definition.ContractVersion != ContractVersion {
		return productError("schema.contract.unsupported_version", "contractVersion", "contract version must be 1.0", nil)
	}
	if _, err := ParseSchemaRevision(definition.SchemaRevision); err != nil {
		return productError("schema.revision.invalid", "schemaRevision", err.Error(), nil)
	}
	if !physicalNamePattern.MatchString(definition.PhysicalName) {
		return productError("schema.table.invalid_name", "physicalName", "physical name must use lowercase letters, digits, and underscores", nil)
	}
	if definition.TableID == "" {
		return productError("schema.table.missing_id", "tableId", "table id is required", nil)
	}
	if definition.DisplayName == "" {
		return productError("schema.table.missing_display_name", "displayName", "display name is required", nil)
	}
	if definition.Kind != TableKindBase && definition.Kind != TableKindView {
		return productError("schema.table.invalid_kind", "kind", "table kind must be base or view", nil)
	}
	if definition.Kind == TableKindBase && definition.View != nil {
		return productError("schema.view.invalid", "view", "base tables cannot declare a view source", nil)
	}
	if definition.Kind == TableKindView {
		if definition.View == nil || definition.View.SourceTableID == "" {
			return productError("schema.view.invalid", "view.sourceTableId", "view source table is required", nil)
		}
		if definition.View.SourceTableID == definition.TableID {
			return productError("schema.view.invalid", "view.sourceTableId", "view cannot select from itself", nil)
		}
		if definition.ArchivePolicy.Mode != ArchiveModeNone {
			return productError("schema.view.invalid", "archivePolicy", "views cannot define an archive policy", nil)
		}
		if len(definition.Indexes) != 0 {
			return productError("schema.view.invalid", "indexes", "views cannot define indexes", nil)
		}
	}
	if err := validateArchivePolicy(definition); err != nil {
		return err
	}
	if len(definition.Fields) == 0 {
		return productError("schema.table.fields_required", "fields", "at least one field is required", nil)
	}

	fieldIDs := make(map[string]string, len(definition.Fields))
	for index, field := range definition.Fields {
		prefix := fmt.Sprintf("fields[%d]", index)
		if field.FieldID == "" {
			return productError("schema.field.missing_id", prefix+".fieldId", "field id is required", nil)
		}
		if _, exists := fieldIDs[field.FieldID]; exists {
			return productError("schema.field.duplicate_id", prefix+".fieldId", "field id must be unique within its table", nil)
		}
		fieldIDs[field.FieldID] = field.PhysicalName
	}

	fieldNames := make(map[string]struct{}, len(definition.Fields))
	autoDateRoles := make(map[AutoDateRole]int, 2)
	for index, field := range definition.Fields {
		prefix := fmt.Sprintf("fields[%d]", index)
		if !physicalNamePattern.MatchString(field.PhysicalName) {
			return productError("schema.field.invalid_name", prefix+".physicalName", "invalid physical field name", nil)
		}
		if field.DisplayName == "" {
			return productError("schema.field.missing_display_name", prefix+".displayName", "display name is required", nil)
		}
		if _, exists := fieldNames[strings.ToLower(field.PhysicalName)]; exists {
			return productError("schema.field.duplicate_name", prefix+".physicalName", "field name must be unique", nil)
		}
		fieldNames[strings.ToLower(field.PhysicalName)] = struct{}{}
		capability, err := CapabilityFor(effectiveDataType(field))
		if err != nil {
			return productError("schema.field.unsupported_type", prefix+".dataType", err.Error(), nil)
		}
		if field.StorageType != capability.Storage {
			return productError("schema.field.storage_mismatch", prefix+".storageType", "storage type does not match the product data type", map[string]any{"expected": capability.Storage})
		}
		if field.Editor.Kind == "" || field.Editor.Config == nil {
			return productError("schema.field.invalid_editor", prefix+".editor", "editor kind and config are required", nil)
		}
		if err := validateFieldKind(field, prefix); err != nil {
			return err
		}
		if err := validateAutoDate(field, prefix); err != nil {
			return err
		}
		if field.AutoDate != nil {
			if previousIndex, exists := autoDateRoles[field.AutoDate.Role]; exists {
				autodateobs.Increment(autodateobs.RoleDuplicate)
				return productError(
					"schema.field.autodate_role_duplicate",
					prefix+".autoDate.role",
					"automatic date roles must be unique within a table",
					map[string]any{
						"role":          field.AutoDate.Role,
						"previousIndex": previousIndex,
					},
				)
			}
			autoDateRoles[field.AutoDate.Role] = index
		}
		if effectiveDataType(field) == DataTypeDecimal {
			return unsupported(prefix+".dataType", "exact decimal storage is not supported by PocketBase number fields")
		}
		if err := validateConstraints(field, prefix, fieldIDs, definition.Fields); err != nil {
			return err
		}
		if definition.Kind == TableKindView && !field.ReadOnly {
			return productError("schema.view.field_not_read_only", prefix+".readOnly", "view fields must be read-only", nil)
		}
		if definition.Kind == TableKindView {
			if field.Kind == FieldKindFormula || field.DataType == DataTypeFormula ||
				field.Kind == FieldKindLookup || field.DataType == DataTypeLookup {
				return productError(
					"schema.view.invalid_field", prefix+".kind",
					"view fields are projections and cannot masquerade as formula or lookup fields",
					nil,
				)
			}
			if field.DefaultValue != nil {
				return productError(
					"schema.view.invalid_field", prefix+".defaultValue",
					"view fields cannot define defaults", nil,
				)
			}
		}
	}
	indexNames := make(map[string]struct{}, len(definition.Indexes))
	for index, idx := range definition.Indexes {
		if !physicalNamePattern.MatchString(idx.Name) || len(idx.FieldIDs) == 0 {
			return productError("schema.index.invalid", fmt.Sprintf("indexes[%d]", index), "index name and fieldIds are required", nil)
		}
		normalizedName := strings.ToLower(idx.Name)
		if _, duplicate := indexNames[normalizedName]; duplicate {
			return productError("schema.index.duplicate_name", fmt.Sprintf("indexes[%d].name", index), "index names must be unique", nil)
		}
		indexNames[normalizedName] = struct{}{}
		seen := map[string]struct{}{}
		for fieldIndex, fieldID := range idx.FieldIDs {
			if _, ok := fieldIDs[fieldID]; !ok {
				return productError("schema.index.unknown_field", fmt.Sprintf("indexes[%d].fieldIds[%d]", index, fieldIndex), "index references an unknown field", map[string]any{"fieldId": fieldID})
			}
			if _, duplicate := seen[fieldID]; duplicate {
				return productError("schema.index.duplicate_field", fmt.Sprintf("indexes[%d].fieldIds[%d]", index, fieldIndex), "index fieldIds must be unique", nil)
			}
			seen[fieldID] = struct{}{}
		}
		if err := validateIndexableFields(
			idx.FieldIDs,
			definition.Fields,
			fmt.Sprintf("indexes[%d].fieldIds", index),
		); err != nil {
			return err
		}
	}
	if definition.ArchivePolicy.FieldID != nil {
		if _, ok := fieldIDs[*definition.ArchivePolicy.FieldID]; !ok {
			return productError("schema.table.invalid_archive_policy", "archivePolicy.fieldId", "archive field does not exist", nil)
		}
	}
	return nil
}

func validateAutoDate(field FieldDefinition, prefix string) error {
	if field.DataType != DataTypeAutoDate {
		if field.AutoDate != nil {
			return productError(
				"schema.field.autodate_config_forbidden",
				prefix+".autoDate",
				"automatic date configuration is only valid for autoDate fields",
				nil,
			)
		}
		return nil
	}
	if field.AutoDate == nil || field.AutoDate.Role == "" {
		autodateobs.Increment(autodateobs.RoleRequired)
		return productError(
			"schema.field.autodate_role_required",
			prefix+".autoDate.role",
			"automatic date role is required",
			nil,
		)
	}
	switch field.AutoDate.Role {
	case AutoDateRoleCreatedAt, AutoDateRoleUpdatedAt:
	default:
		return productError(
			"schema.field.autodate_role_invalid",
			prefix+".autoDate.role",
			"automatic date role must be createdAt or updatedAt",
			map[string]any{"role": field.AutoDate.Role},
		)
	}
	if field.Nullable {
		return productError(
			"schema.field.autodate_nullable_forbidden",
			prefix+".nullable",
			"automatic date fields cannot be nullable",
			nil,
		)
	}
	if len(field.Constraints) != 0 {
		return productError(
			"schema.field.autodate_constraints_forbidden",
			prefix+".constraints",
			"automatic date fields cannot define constraints",
			nil,
		)
	}
	return nil
}

func validateArchivePolicy(definition TableDefinition) error {
	policy := definition.ArchivePolicy
	switch policy.Mode {
	case ArchiveModeNone:
		if policy.FieldID != nil || policy.ArchivedValue != nil {
			return productError("schema.table.invalid_archive_policy", "archivePolicy", "none archive policy cannot reference a field or archived value", nil)
		}
	case ArchiveModeStatus:
		if policy.FieldID == nil || *policy.FieldID == "" {
			return productError("schema.table.invalid_archive_policy", "archivePolicy.fieldId", "status archive policy requires a fieldId", nil)
		}
	case ArchiveModeDeletedAt:
		if policy.FieldID == nil || *policy.FieldID == "" {
			return productError("schema.table.invalid_archive_policy", "archivePolicy.fieldId", "deletedAt archive policy requires a fieldId", nil)
		}
	default:
		return productError("schema.table.invalid_archive_policy", "archivePolicy.mode", "unknown archive policy", nil)
	}
	return nil
}

func effectiveDataType(field FieldDefinition) DataType {
	if field.DataType == DataTypeFormula && field.Formula != nil {
		return field.Formula.ResultType
	}
	if field.DataType == DataTypeLookup {
		switch field.StorageType {
		case StorageText:
			return DataTypeShortText
		case StorageEditor:
			return DataTypeLongText
		case StorageBool:
			return DataTypeBoolean
		case StorageNumber:
			return DataTypeFloat
		case StorageDate:
			return DataTypeDateTime
		case StorageEmail:
			return DataTypeEmail
		case StorageURL:
			return DataTypeURL
		case StorageSelect:
			return DataTypeSelect
		case StorageGeoPoint:
			return DataTypeGeoPoint
		default:
			return DataTypeJSON
		}
	}
	return field.DataType
}

func validateConstraints(
	field FieldDefinition,
	prefix string,
	fieldIDs map[string]string,
	fields []FieldDefinition,
) error {
	seen := make(map[ConstraintKind]struct{}, len(field.Constraints))
	for index, constraint := range field.Constraints {
		path := fmt.Sprintf("%s.constraints[%d]", prefix, index)
		if _, duplicate := seen[constraint.Kind]; duplicate {
			return productError(
				"schema.field.duplicate_constraint",
				path+".kind",
				"each constraint kind may appear at most once per field",
				map[string]any{"kind": constraint.Kind},
			)
		}
		seen[constraint.Kind] = struct{}{}
		switch constraint.Kind {
		case ConstraintRequired:
			if _, ok := constraint.Value.(bool); !ok {
				return productError("schema.field.invalid_constraint", path+".value", "constraint value must be boolean", nil)
			}
			required := constraint.Value.(bool)
			if required == field.Nullable {
				return productError(
					"schema.field.invalid_constraint",
					path+".value",
					"required constraint must agree with nullable",
					nil,
				)
			}
		case ConstraintUnique:
			if _, ok := constraint.Value.(bool); !ok {
				return productError("schema.field.invalid_constraint", path+".value", "constraint value must be boolean", nil)
			}
			if constraint.Value.(bool) && !isIndexableField(field) {
				return unsupported(path, "field type cannot enforce a stable unique constraint")
			}
		case ConstraintDefault:
			if field.DefaultValue == nil || !jsonValuesEqual(constraint.Value, field.DefaultValue) {
				return productError(
					"schema.field.invalid_constraint", path+".value",
					"default constraint must equal defaultValue", nil,
				)
			}
		case ConstraintIndex:
			if len(constraint.FieldIDs) == 0 {
				return productError("schema.field.invalid_constraint", path+".fieldIds", "index fieldIds are required", nil)
			}
			indexSeen := make(map[string]struct{}, len(constraint.FieldIDs))
			for fieldIndex, fieldID := range constraint.FieldIDs {
				if _, ok := fieldIDs[fieldID]; !ok {
					return productError(
						"schema.index.unknown_field",
						fmt.Sprintf("%s.fieldIds[%d]", path, fieldIndex),
						"index references an unknown field",
						map[string]any{"fieldId": fieldID},
					)
				}
				if _, duplicate := indexSeen[fieldID]; duplicate {
					return productError(
						"schema.index.duplicate_field",
						fmt.Sprintf("%s.fieldIds[%d]", path, fieldIndex),
						"index fieldIds must be unique",
						nil,
					)
				}
				indexSeen[fieldID] = struct{}{}
			}
			if err := validateIndexableFields(
				constraint.FieldIDs,
				fields,
				path+".fieldIds",
			); err != nil {
				return err
			}
		case ConstraintRange:
			if !isNumericType(effectiveDataType(field)) {
				return productError("schema.field.invalid_constraint", path, "range constraint requires a numeric field", nil)
			}
			if constraint.Min != nil && constraint.Max != nil && *constraint.Min > *constraint.Max {
				return productError("schema.field.invalid_constraint", path+".max", "max cannot be less than min", nil)
			}
		case ConstraintLength:
			if constraint.MinLength != nil && *constraint.MinLength < 0 || constraint.MaxLength != nil && *constraint.MaxLength < 0 {
				return productError("schema.field.invalid_constraint", path, "length bounds cannot be negative", nil)
			}
			if constraint.MinLength != nil && constraint.MaxLength != nil && *constraint.MinLength > *constraint.MaxLength {
				return productError("schema.field.invalid_constraint", path+".maxLength", "maxLength cannot be less than minLength", nil)
			}
			if !isPocketBaseTextType(effectiveDataType(field)) &&
				!isTextLikeButNotPocketBaseText(effectiveDataType(field)) {
				return productError("schema.field.invalid_constraint", path, "length constraint requires a text field", nil)
			}
		case ConstraintPattern:
			if !isPocketBaseTextType(effectiveDataType(field)) &&
				!isTextLikeButNotPocketBaseText(effectiveDataType(field)) {
				return productError("schema.field.invalid_constraint", path, "pattern constraint requires a text field", nil)
			}
			if _, err := regexp.Compile(constraint.Pattern); err != nil {
				return productError("schema.field.invalid_constraint", path+".pattern", "pattern is not a valid regular expression", nil)
			}
			if len(constraint.Flags) > 0 {
				return productError("schema.field.invalid_constraint", path+".flags", "pattern flags are not supported in contract v1", nil)
			}
		case ConstraintPrecisionScale:
			if effectiveDataType(field) != DataTypeDecimal {
				return productError("schema.field.invalid_constraint", path, "precisionScale constraint requires a decimal field", nil)
			}
			if constraint.Precision <= 0 || constraint.Scale < 0 || constraint.Scale > constraint.Precision {
				return productError("schema.field.invalid_constraint", path, "precision and scale are invalid", nil)
			}
			return unsupported(path, "exact decimal precision and scale are not supported by PocketBase number fields")
		case ConstraintEnum:
			if field.DataType != DataTypeSelect && field.DataType != DataTypeMultiSelect {
				return productError("schema.field.invalid_constraint", path, "enum constraint requires a select field", nil)
			}
			if len(constraint.Options) == 0 {
				return productError("schema.field.invalid_constraint", path+".options", "enum options are required", nil)
			}
			for optionIndex, option := range constraint.Options {
				if strings.TrimSpace(option.DisplayName) == "" {
					return productError(
						"schema.field.invalid_constraint",
						fmt.Sprintf("%s.options[%d].displayName", path, optionIndex),
						"enum option displayName is required",
						nil,
					)
				}
			}
			if _, err := EnumStorageOptions(field); err != nil {
				return productError(
					"schema.field.invalid_constraint",
					path+".options",
					err.Error(),
					nil,
				)
			}
			if constraint.MinSelected < 0 ||
				constraint.MaxSelected != nil && *constraint.MaxSelected < constraint.MinSelected {
				return productError("schema.field.invalid_constraint", path, "selection bounds are invalid", nil)
			}
			if field.DataType == DataTypeSelect &&
				(constraint.Multiple || constraint.MinSelected > 1 ||
					constraint.MaxSelected == nil || *constraint.MaxSelected != 1) {
				return productError("schema.field.invalid_constraint", path, "single select must use multiple=false and maxSelected=1", nil)
			}
			if field.DataType == DataTypeMultiSelect && !constraint.Multiple {
				return productError("schema.field.invalid_constraint", path+".multiple", "multiSelect requires multiple=true", nil)
			}
		case ConstraintJSONSchema:
			if field.DataType != DataTypeJSON && field.DataType != DataTypeGeoJSON {
				return productError("schema.field.invalid_constraint", path, "jsonSchema constraint requires a JSON field", nil)
			}
			if err := ValidateJSONSchema(constraint.Schema); err != nil {
				return productError(
					"schema.field.invalid_constraint", path+".schema",
					"JSON Schema is invalid", map[string]any{"cause": err.Error()},
				)
			}
		case ConstraintRelation:
			if field.DataType != DataTypeRelation {
				return productError("schema.field.invalid_constraint", path, "relation constraint requires a relation field", nil)
			}
			if field.Relation == nil || constraint.TargetTableID != field.Relation.TargetTableID ||
				constraint.Cardinality != field.Relation.Cardinality ||
				constraint.DeletePolicy != field.Relation.DeletePolicy {
				return productError("schema.field.invalid_constraint", path, "relation constraint must match relation definition", nil)
			}
		case ConstraintAttachment:
			if field.DataType != DataTypeFile {
				return productError("schema.field.invalid_constraint", path, "attachment constraint requires a file field", nil)
			}
			if field.AttachmentPolicy == nil || constraint.Policy == nil || !reflect.DeepEqual(constraint.Policy, field.AttachmentPolicy) {
				return productError("schema.field.invalid_constraint", path, "attachment constraint must match attachmentPolicy", nil)
			}
		default:
			return productError("schema.field.invalid_constraint", path+".kind", "unknown constraint kind", nil)
		}
	}
	if field.DataType == DataTypeSelect || field.DataType == DataTypeMultiSelect {
		if enumConstraint(field) == nil {
			return productError("schema.field.invalid_constraint", prefix+".constraints", "select fields require an enum constraint", nil)
		}
	}
	if field.DataType == DataTypeRelation && field.Relation == nil {
		return productError("schema.field.invalid_relation", prefix+".relation", "relation configuration is required", nil)
	}
	if field.Relation != nil {
		if field.Relation.Cardinality != "one" && field.Relation.Cardinality != "many" {
			return productError("schema.field.invalid_relation", prefix+".relation.cardinality", "cardinality must be one or many", nil)
		}
		switch field.Relation.DeletePolicy {
		case "cascade", "setNull", "restrict":
		default:
			return productError("schema.field.invalid_relation", prefix+".relation.deletePolicy", "unknown delete policy", nil)
		}
		mode := relationMode(*field.Relation)
		switch mode {
		case "direct":
			if field.Relation.JunctionTableID != nil ||
				field.Relation.JunctionSourceFieldID != "" ||
				field.Relation.JunctionTargetFieldID != "" ||
				field.Relation.JunctionDiscriminatorFieldID != "" ||
				len(field.Relation.AllowedTargetTableIDs) != 0 {
				return productError("schema.field.invalid_relation", prefix+".relation", "direct relation cannot declare junction metadata", nil)
			}
		case "junction", "m2a":
			if field.Relation.Cardinality != "many" {
				return productError("schema.field.invalid_relation", prefix+".relation.cardinality", "junction and m2a relations must have many cardinality", nil)
			}
			if !field.ReadOnly {
				return productError("schema.field.invalid_relation", prefix+".readOnly", "junction and m2a relation projections must be read-only", nil)
			}
			if field.Relation.JunctionTableID == nil ||
				*field.Relation.JunctionTableID == "" ||
				field.Relation.JunctionSourceFieldID == "" ||
				field.Relation.JunctionTargetFieldID == "" ||
				field.Relation.JunctionSourceFieldID == field.Relation.JunctionTargetFieldID {
				return productError("schema.field.invalid_relation", prefix+".relation", "junction table and distinct source/target fields are required", nil)
			}
			if mode == "junction" {
				if field.Relation.JunctionDiscriminatorFieldID != "" ||
					len(field.Relation.AllowedTargetTableIDs) != 0 {
					return productError("schema.field.invalid_relation", prefix+".relation", "junction relation cannot declare m2a metadata", nil)
				}
				break
			}
			if field.Relation.JunctionDiscriminatorFieldID == "" ||
				field.Relation.JunctionDiscriminatorFieldID == field.Relation.JunctionSourceFieldID ||
				field.Relation.JunctionDiscriminatorFieldID == field.Relation.JunctionTargetFieldID {
				return productError("schema.field.invalid_relation", prefix+".relation.junctionDiscriminatorFieldId", "m2a discriminator field is required and must be distinct", nil)
			}
			seenTargets := map[string]struct{}{}
			containsDefault := false
			for index, tableID := range field.Relation.AllowedTargetTableIDs {
				if tableID == "" {
					return productError("schema.field.invalid_relation", fmt.Sprintf("%s.relation.allowedTargetTableIds[%d]", prefix, index), "allowed target table id is required", nil)
				}
				if _, duplicate := seenTargets[tableID]; duplicate {
					return productError("schema.field.invalid_relation", fmt.Sprintf("%s.relation.allowedTargetTableIds[%d]", prefix, index), "allowed target table ids must be unique", nil)
				}
				seenTargets[tableID] = struct{}{}
				containsDefault = containsDefault || tableID == field.Relation.TargetTableID
			}
			if len(seenTargets) == 0 || !containsDefault {
				return productError("schema.field.invalid_relation", prefix+".relation.allowedTargetTableIds", "m2a allowlist must include targetTableId", nil)
			}
		default:
			return productError("schema.field.invalid_relation", prefix+".relation.mode", "mode must be direct, junction, or m2a", nil)
		}
	}
	if field.DataType == DataTypeFile && field.AttachmentPolicy == nil {
		return productError("schema.field.invalid_attachment_policy", prefix+".attachmentPolicy", "attachment policy is required", nil)
	}
	if field.AttachmentPolicy != nil && (field.AttachmentPolicy.MaxFiles < 1 || field.AttachmentPolicy.MaxBytesPerFile < 1) {
		return productError("schema.field.invalid_attachment_policy", prefix+".attachmentPolicy", "maxFiles and maxBytesPerFile must be positive", nil)
	}
	if field.DataType == DataTypeLookup && field.Lookup == nil {
		return productError("schema.field.invalid_lookup", prefix+".lookup", "lookup configuration is required", nil)
	}
	if field.Lookup != nil {
		path := field.Lookup.EffectivePath()
		if len(path) == 0 {
			return productError("schema.field.invalid_lookup", prefix+".lookup.path", "lookup path must contain at least one relation", nil)
		}
		if field.Lookup.RelationFieldID != path[0].RelationFieldID {
			return productError("schema.field.invalid_lookup", prefix+".lookup.relationFieldId", "lookup relationFieldId must match the first path step", nil)
		}
		for index, step := range path {
			if step.RelationFieldID == "" {
				return productError("schema.field.invalid_lookup", fmt.Sprintf("%s.lookup.path[%d].relationFieldId", prefix, index), "lookup path relationFieldId is required", nil)
			}
		}
		switch field.Lookup.Aggregate {
		case "none", "first", "distinct", "count", "countNonNull", "sum", "avg", "min", "max":
		default:
			return productError("schema.field.invalid_lookup", prefix+".lookup.aggregate", "unknown lookup aggregate", nil)
		}
		if field.Lookup.JunctionFieldID != "" &&
			len(field.Lookup.TargetFieldIDs) != 0 {
			return productError("schema.field.invalid_lookup", prefix+".lookup", "junctionFieldId and targetFieldIds are mutually exclusive", nil)
		}
		for tableID, fieldID := range field.Lookup.TargetFieldIDs {
			if tableID == "" || fieldID == "" {
				return productError("schema.field.invalid_lookup", prefix+".lookup.targetFieldIds", "m2a target field mapping requires non-empty table and field ids", nil)
			}
		}
	}
	if field.DataType == DataTypeFormula && field.Formula == nil {
		return productError("schema.field.invalid_formula", prefix+".formula", "formula configuration is required", nil)
	}
	if field.Formula != nil {
		if field.Formula.Language != "cel-v1" {
			return productError("schema.field.invalid_formula", prefix+".formula.language", "formula language must be cel-v1", nil)
		}
		if field.Formula.Source == "" {
			return productError("schema.field.invalid_formula", prefix+".formula.source", "formula source is required", nil)
		}
		if field.Formula.Version < 1 {
			return productError("schema.field.invalid_formula", prefix+".formula.version", "formula version must be positive", nil)
		}
		switch field.Formula.Status {
		case "draft", "backfilling", "ready", "failed":
		default:
			return productError("schema.field.invalid_formula", prefix+".formula.status", "unknown formula status", nil)
		}
	}
	if field.DefaultValue != nil {
		switch field.DataType {
		case DataTypeHash, DataTypeSecret, DataTypeFile, DataTypeRelation,
			DataTypeAutoDate, DataTypeFormula, DataTypeLookup:
			return productError(
				"schema.field.invalid_default", prefix+".defaultValue",
				"field type cannot define a static default", nil,
			)
		}
		if err := ValidateFieldValue(field, field.DefaultValue); err != nil {
			return productError(
				"schema.field.invalid_default", prefix+".defaultValue",
				err.Error(), nil,
			)
		}
	}
	return nil
}

func relationMode(relation RelationSpec) string {
	return relation.EffectiveMode()
}

func isNumericType(dataType DataType) bool {
	return dataType == DataTypeInteger || dataType == DataTypeFloat ||
		dataType == DataTypeDecimal
}

func isPocketBaseTextType(dataType DataType) bool {
	switch dataType {
	case DataTypeShortText, DataTypeTime, DataTypeUUID, DataTypeHash, DataTypeSecret:
		return true
	default:
		return false
	}
}

func isTextLikeButNotPocketBaseText(dataType DataType) bool {
	switch dataType {
	case DataTypeLongText, DataTypeRichText, DataTypeEmail, DataTypeURL:
		return true
	default:
		return false
	}
}

func isIndexableField(field FieldDefinition) bool {
	dataType := effectiveDataType(field)
	switch dataType {
	case DataTypeJSON, DataTypeGeoJSON, DataTypeList, DataTypeFile, DataTypeGeoPoint:
		return false
	case DataTypeRelation:
		return field.Relation != nil && field.Relation.Cardinality == "one"
	case DataTypeMultiSelect:
		return false
	default:
		return true
	}
}

func validateIndexableFields(
	fieldIDs []string,
	fields []FieldDefinition,
	path string,
) error {
	byID := make(map[string]FieldDefinition, len(fields))
	for _, field := range fields {
		byID[field.FieldID] = field
	}
	for index, fieldID := range fieldIDs {
		if field, ok := byID[fieldID]; ok && !isIndexableField(field) {
			return unsupported(
				fmt.Sprintf("%s[%d]", path, index),
				"field type cannot enforce a stable index",
			)
		}
	}
	return nil
}

func validateFieldKind(field FieldDefinition, prefix string) error {
	expected := FieldKindScalar
	switch field.DataType {
	case DataTypeRelation:
		expected = FieldKindRelation
	case DataTypeLookup:
		expected = FieldKindLookup
	case DataTypeFormula:
		expected = FieldKindFormula
	case DataTypeFile:
		expected = FieldKindAttachment
	case DataTypeAutoDate:
		expected = FieldKindSystem
	}
	if field.Kind != expected {
		return productError("schema.field.kind_mismatch", prefix+".kind", "field kind does not match its data type", map[string]any{"expected": expected})
	}
	if (field.Kind == FieldKindLookup || field.Kind == FieldKindFormula || field.Kind == FieldKindSystem) && !field.ReadOnly {
		return productError("schema.field.read_only_required", prefix+".readOnly", "computed and system fields must be read-only", nil)
	}
	return nil
}

func constraintFor(field FieldDefinition, kind ConstraintKind) *FieldConstraint {
	for index := range field.Constraints {
		if field.Constraints[index].Kind == kind {
			return &field.Constraints[index]
		}
	}
	return nil
}

func enumConstraint(field FieldDefinition) *FieldConstraint {
	return constraintFor(field, ConstraintEnum)
}

func unsupported(path, message string) *ProductError {
	return productError("schema.constraint.unsupported", path, message, nil)
}

func productError(code, path, message string, details map[string]any) *ProductError {
	return &ProductError{Code: code, Path: path, Message: message, Details: details}
}
