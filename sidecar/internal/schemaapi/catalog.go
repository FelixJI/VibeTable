package schemaapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/autodateobs"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const (
	tablesCollection      = "vibetable_tables"
	fieldsCollection      = "vibetable_fields"
	formulasCollection    = "vibetable_formulas"
	relationsCollection   = "vibetable_relations"
	lookupsCollection     = "vibetable_lookups"
	formulaDepsCollection = "vibetable_formula_dependencies"
	maxSafeRevision       = int64(1<<53 - 1)
)

type Change struct {
	Definition       schema.TableDefinition `json:"definition"`
	ExpectedRevision int64                  `json:"expectedRevision"`
	OperationID      string                 `json:"operationId,omitempty"`
}

type DeleteResult struct {
	Deleted bool   `json:"deleted"`
	TableID string `json:"tableId"`
}

type ValidationResult struct {
	Definition   schema.TableDefinition                `json:"definition"`
	Capabilities map[schema.DataType]schema.Capability `json:"capabilities"`
}

type SchemaCatalog interface {
	List(ctx context.Context) ([]schema.TableDefinition, error)
	Describe(ctx context.Context, tableID string) (schema.TableDefinition, error)
	InspectAutoDates(ctx context.Context) ([]AutoDateDiagnostic, error)
	ValidateChange(ctx context.Context, change Change) (ValidationResult, error)
	ApplyChange(ctx context.Context, change Change) (schema.TableDefinition, error)
	DeleteTable(ctx context.Context, tableID string, expectedRevision int64) (DeleteResult, error)
	GetRevision(ctx context.Context, tableID string) (int64, error)
	GetDataRevision(ctx context.Context, tableID string) (int64, error)
}

type AutoDateDiagnostic struct {
	TableID       string              `json:"tableId"`
	FieldID       string              `json:"fieldId"`
	PhysicalName  string              `json:"physicalName"`
	DisplayName   string              `json:"displayName"`
	OnCreate      bool                `json:"onCreate"`
	OnUpdate      bool                `json:"onUpdate"`
	DeclaredRole  schema.AutoDateRole `json:"declaredRole,omitempty"`
	SuggestedRole schema.AutoDateRole `json:"suggestedRole,omitempty"`
	Status        string              `json:"status"`
}

type Catalog struct {
	app                     core.App
	autoDateProducerEnabled bool
}

func New(app core.App) *Catalog {
	raw, configured := os.LookupEnv("VIBETABLE_AUTODATE_FIELDS_ENABLED")
	enabled := !configured || strings.EqualFold(strings.TrimSpace(raw), "true") ||
		strings.TrimSpace(raw) == "1" ||
		strings.EqualFold(strings.TrimSpace(raw), "yes") ||
		strings.EqualFold(strings.TrimSpace(raw), "on")
	return &Catalog{app: app, autoDateProducerEnabled: enabled}
}

func (catalog *Catalog) List(ctx context.Context) ([]schema.TableDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := catalog.app.FindAllRecords(tablesCollection)
	if err != nil {
		return nil, storageError(err)
	}
	definitions := make([]schema.TableDefinition, 0, len(records))
	for _, record := range records {
		definition, _, decodeErr := decodeStoredTable(record)
		if decodeErr != nil {
			return nil, decodeErr
		}
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool {
		if definitions[i].PhysicalName == definitions[j].PhysicalName {
			return definitions[i].TableID < definitions[j].TableID
		}
		return definitions[i].PhysicalName < definitions[j].PhysicalName
	})
	return definitions, nil
}

func (catalog *Catalog) Describe(
	ctx context.Context,
	tableID string,
) (schema.TableDefinition, error) {
	if err := ctx.Err(); err != nil {
		return schema.TableDefinition{}, err
	}
	record, err := catalog.findTable(catalog.app, tableID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return schema.TableDefinition{}, &schema.ProductError{
				Code: "schema.table.not_found", Path: "tableId", Message: "table was not found",
			}
		}
		return schema.TableDefinition{}, storageError(err)
	}
	definition, _, err := decodeStoredTable(record)
	return definition, err
}

// InspectAutoDates reports the actual PocketBase switch combination for every
// normalized autoDate field. It is intentionally read-only: callers can
// review legacy and conflicting fields before any metadata migration.
func (catalog *Catalog) InspectAutoDates(
	ctx context.Context,
) ([]AutoDateDiagnostic, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := catalog.app.FindAllRecords(tablesCollection)
	if err != nil {
		return nil, storageError(err)
	}
	result := []AutoDateDiagnostic{}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		definition, _, decodeErr := decodeStoredTable(record)
		if decodeErr != nil {
			return nil, decodeErr
		}
		collection, findErr := catalog.app.FindCollectionByNameOrId(
			record.GetString("collection_id"),
		)
		if findErr != nil {
			return nil, storageError(findErr)
		}
		defined := make(map[string]schema.FieldDefinition)
		for _, field := range definition.Fields {
			if field.DataType == schema.DataTypeAutoDate {
				defined[field.PhysicalName] = field
			}
		}
		seen := make(map[string]struct{})
		for _, physical := range collection.Fields {
			pbField, ok := physical.(*core.AutodateField)
			if !ok || pbField.GetSystem() {
				continue
			}
			field, tracked := defined[pbField.GetName()]
			diagnostic := AutoDateDiagnostic{
				TableID:      definition.TableID,
				FieldID:      pbField.GetId(),
				PhysicalName: pbField.GetName(),
				DisplayName:  pbField.GetName(),
			}
			if tracked {
				seen[field.PhysicalName] = struct{}{}
				diagnostic.FieldID = field.FieldID
				diagnostic.DisplayName = field.DisplayName
			}
			if tracked && field.AutoDate != nil {
				diagnostic.DeclaredRole = field.AutoDate.Role
			}
			diagnostic.OnCreate = pbField.OnCreate
			diagnostic.OnUpdate = pbField.OnUpdate
			switch {
			case pbField.OnCreate && !pbField.OnUpdate:
				diagnostic.SuggestedRole = schema.AutoDateRoleCreatedAt
			case pbField.OnCreate && pbField.OnUpdate:
				diagnostic.SuggestedRole = schema.AutoDateRoleUpdatedAt
			case !pbField.OnCreate && pbField.OnUpdate:
				diagnostic.Status = "legacyUpdateOnly"
			default:
				diagnostic.Status = "invalid"
			}
			if diagnostic.Status == "" {
				switch {
				case !tracked:
					diagnostic.Status = "untrackedPhysicalField"
				case diagnostic.DeclaredRole == "":
					diagnostic.Status = "legacy"
				case diagnostic.DeclaredRole != diagnostic.SuggestedRole:
					diagnostic.Status = "conflict"
				default:
					diagnostic.Status = "configured"
				}
			}
			result = append(result, diagnostic)
		}
		for physicalName, field := range defined {
			if _, ok := seen[physicalName]; ok {
				continue
			}
			declaredRole := schema.AutoDateRole("")
			if field.AutoDate != nil {
				declaredRole = field.AutoDate.Role
			}
			result = append(result, AutoDateDiagnostic{
				TableID: definition.TableID, FieldID: field.FieldID,
				PhysicalName: field.PhysicalName, DisplayName: field.DisplayName,
				DeclaredRole: declaredRole, Status: "missingPhysicalField",
			})
		}
	}
	byRole := map[string][]int{}
	for index, diagnostic := range result {
		if diagnostic.SuggestedRole == "" {
			continue
		}
		key := diagnostic.TableID + "\x00" + string(diagnostic.SuggestedRole)
		byRole[key] = append(byRole[key], index)
	}
	for _, indexes := range byRole {
		if len(indexes) < 2 {
			continue
		}
		for _, index := range indexes {
			result[index].Status = "duplicateRole"
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TableID == result[j].TableID {
			return result[i].FieldID < result[j].FieldID
		}
		return result[i].TableID < result[j].TableID
	})
	return result, nil
}

func (catalog *Catalog) ValidateChange(
	ctx context.Context,
	change Change,
) (ValidationResult, error) {
	if err := ctx.Err(); err != nil {
		return ValidationResult{}, err
	}
	normalized, err := normalize(change.Definition)
	if err != nil {
		return ValidationResult{}, err
	}
	if err := catalog.validateAutoDateProducerGate(normalized); err != nil {
		return ValidationResult{}, err
	}
	if err := catalog.validateRelationReferences(ctx, normalized); err != nil {
		return ValidationResult{}, err
	}
	if err := catalog.validateLookupReferences(ctx, normalized); err != nil {
		return ValidationResult{}, err
	}
	if err := catalog.validateFormulaReferences(ctx, normalized); err != nil {
		return ValidationResult{}, err
	}
	if err := catalog.validateViewReference(ctx, normalized); err != nil {
		return ValidationResult{}, err
	}
	revision, err := schema.ParseSchemaRevision(normalized.SchemaRevision)
	if err != nil {
		return ValidationResult{}, &schema.ProductError{
			Code: "schema.revision.invalid", Path: "definition.schemaRevision", Message: err.Error(),
		}
	}
	if revision != change.ExpectedRevision {
		return ValidationResult{}, &schema.ProductError{
			Code: "schema.definition_revision_mismatch", Path: "definition.schemaRevision",
			Message: "definition schemaRevision must represent expectedRevision",
			Details: map[string]any{"definition": revision, "expected": change.ExpectedRevision},
		}
	}
	if normalized.Kind == schema.TableKindView {
		if _, err := catalog.compileViewQuery(ctx, catalog.app, normalized); err != nil {
			return ValidationResult{}, err
		}
	} else {
		if _, err := catalog.compileFields(catalog.app, normalized, "__self__"); err != nil {
			return ValidationResult{}, err
		}
	}
	capabilities := make(map[schema.DataType]schema.Capability)
	for _, field := range normalized.Fields {
		capability, _ := schema.CapabilityFor(field.DataType)
		capabilities[field.DataType] = capability
	}
	return ValidationResult{Definition: normalized, Capabilities: capabilities}, nil
}

func (catalog *Catalog) validateAutoDateProducerGate(
	next schema.TableDefinition,
) error {
	if catalog.autoDateProducerEnabled {
		return nil
	}
	previous := schema.TableDefinition{}
	record, err := catalog.findTable(catalog.app, next.TableID)
	if err == nil {
		decoded, _, decodeErr := decodeStoredTable(record)
		if decodeErr != nil {
			return decodeErr
		}
		previous = decoded
	} else if !errors.Is(err, sql.ErrNoRows) {
		return storageError(err)
	}
	previousRoles := make(map[string]schema.AutoDateRole)
	for _, field := range previous.Fields {
		if field.DataType == schema.DataTypeAutoDate && field.AutoDate != nil {
			previousRoles[field.FieldID] = field.AutoDate.Role
		}
	}
	for index, field := range next.Fields {
		if field.DataType != schema.DataTypeAutoDate || field.AutoDate == nil {
			continue
		}
		if previousRoles[field.FieldID] != field.AutoDate.Role {
			return &schema.ProductError{
				Code:    "schema.field.autodate_producer_disabled",
				Path:    fmt.Sprintf("fields[%d].autoDate", index),
				Message: "automatic date field creation is disabled for this release",
			}
		}
	}
	return nil
}

func (catalog *Catalog) validateViewReference(
	ctx context.Context,
	definition schema.TableDefinition,
) error {
	if definition.Kind != schema.TableKindView {
		return nil
	}
	if definition.View == nil {
		return &schema.ProductError{
			Code: "schema.view.invalid", Path: "view",
			Message: "view source is required",
		}
	}
	source, err := catalog.Describe(ctx, definition.View.SourceTableID)
	if err != nil {
		var productErr *schema.ProductError
		if errors.As(err, &productErr) && productErr.Code == "schema.table.not_found" {
			return &schema.ProductError{
				Code: "schema.view.source_not_found", Path: "view.sourceTableId",
				Message: "view source table was not found",
			}
		}
		return err
	}
	sourceFields := make(map[string]schema.FieldDefinition, len(source.Fields))
	for _, field := range source.Fields {
		sourceFields[field.PhysicalName] = field
	}
	for index, field := range definition.Fields {
		prefix := fmt.Sprintf("fields[%d]", index)
		sourceField, found := sourceFields[field.PhysicalName]
		if !found {
			return &schema.ProductError{
				Code: "schema.view.field_not_found", Path: prefix + ".physicalName",
				Message: "view field does not exist in its source table",
			}
		}
		if sourceField.DataType == schema.DataTypeSecret ||
			sourceField.DataType == schema.DataTypeHash {
			return &schema.ProductError{
				Code: "schema.view.sensitive_field", Path: prefix + ".physicalName",
				Message: "sensitive fields cannot be projected through a view",
			}
		}
		sourceType := sourceField.DataType
		if sourceType == schema.DataTypeFormula && sourceField.Formula != nil {
			sourceType = sourceField.Formula.ResultType
		}
		if sourceType == schema.DataTypeLookup {
			sourceType = field.DataType
		}
		if field.DataType != sourceType {
			return &schema.ProductError{
				Code: "schema.view.field_mismatch", Path: prefix + ".dataType",
				Message: "view field data type must match its source field",
			}
		}
		if field.StorageType != sourceField.StorageType {
			return &schema.ProductError{
				Code: "schema.view.field_mismatch", Path: prefix + ".storageType",
				Message: "view field storage type must match its source field",
			}
		}
	}
	return nil
}

func (catalog *Catalog) validateRelationReferences(
	ctx context.Context,
	definition schema.TableDefinition,
) error {
	for index, field := range definition.Fields {
		if field.Kind != schema.FieldKindRelation || field.Relation == nil ||
			field.Relation.EffectiveMode() == "direct" {
			continue
		}
		relation := field.Relation
		prefix := fmt.Sprintf("definition.fields[%d].relation", index)
		junction, err := catalog.Describe(ctx, *relation.JunctionTableID)
		if err != nil {
			return &schema.ProductError{
				Code:    "schema.relation.junction_not_found",
				Path:    prefix + ".junctionTableId",
				Message: "junction table was not found",
			}
		}
		source, sourceOK := schemaFieldByID(
			junction, relation.JunctionSourceFieldID,
		)
		if !sourceOK || source.Relation == nil ||
			source.Relation.EffectiveMode() != "direct" ||
			source.Relation.Cardinality != "one" ||
			source.Relation.TargetTableID != definition.TableID {
			return &schema.ProductError{
				Code:    "schema.relation.junction_source_invalid",
				Path:    prefix + ".junctionSourceFieldId",
				Message: "junction source must be a direct single relation to the source table",
			}
		}
		target, targetOK := schemaFieldByID(
			junction, relation.JunctionTargetFieldID,
		)
		if relation.EffectiveMode() == "junction" {
			if !targetOK || target.Relation == nil ||
				target.Relation.EffectiveMode() != "direct" ||
				target.Relation.Cardinality != "one" ||
				target.Relation.TargetTableID != relation.TargetTableID {
				return &schema.ProductError{
					Code:    "schema.relation.junction_target_invalid",
					Path:    prefix + ".junctionTargetFieldId",
					Message: "junction target must be a direct single relation to the target table",
				}
			}
			continue
		}
		if !targetOK || target.Kind != schema.FieldKindScalar ||
			(target.StorageType != schema.StorageText &&
				target.StorageType != schema.StorageJSON) {
			return &schema.ProductError{
				Code:    "schema.relation.m2a_target_invalid",
				Path:    prefix + ".junctionTargetFieldId",
				Message: "m2a target id must use scalar text or JSON storage",
			}
		}
		discriminator, discriminatorOK := schemaFieldByID(
			junction, relation.JunctionDiscriminatorFieldID,
		)
		if !discriminatorOK || discriminator.Kind != schema.FieldKindScalar ||
			(discriminator.StorageType != schema.StorageText &&
				discriminator.StorageType != schema.StorageSelect) {
			return &schema.ProductError{
				Code:    "schema.relation.m2a_discriminator_invalid",
				Path:    prefix + ".junctionDiscriminatorFieldId",
				Message: "m2a discriminator must use scalar text or select storage",
			}
		}
		for allowedIndex, tableID := range relation.AllowedTargetTableIDs {
			if _, err := catalog.Describe(ctx, tableID); err != nil {
				return &schema.ProductError{
					Code: "schema.relation.m2a_target_not_found",
					Path: fmt.Sprintf(
						"%s.allowedTargetTableIds[%d]", prefix, allowedIndex,
					),
					Message: "allowed m2a target table was not found",
				}
			}
		}
	}
	return nil
}

func schemaFieldByID(
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

func (catalog *Catalog) validateFormulaReferences(
	ctx context.Context,
	definition schema.TableDefinition,
) error {
	plan, formulaErr := formula.NewCompiler(
		formula.DefaultLimits(),
	).CompileTable(definition)
	if formulaErr != nil {
		return formulaErr
	}
	fieldsByName := make(map[string]schema.FieldDefinition, len(definition.Fields))
	for _, field := range definition.Fields {
		fieldsByName[field.PhysicalName] = field
	}
	for _, compiled := range plan.Formulas {
		for _, reference := range compiled.ReferencePaths {
			parts := strings.Split(reference, ".")
			root, exists := fieldsByName[parts[0]]
			if !exists || root.Kind != schema.FieldKindRelation ||
				root.Relation == nil {
				continue
			}
			if root.Relation.Cardinality != "one" {
				return &schema.ProductError{
					Code: "schema.formula.relation_cardinality",
					Path: formulaPath(definition, compiled.FieldID),
					Message: "relation dereference requires cardinality one; " +
						"use a Lookup aggregate for many relations",
					Details: map[string]any{"reference": reference},
				}
			}
			if len(parts) != 2 {
				return &schema.ProductError{
					Code:    "schema.formula.relation_path_invalid",
					Path:    formulaPath(definition, compiled.FieldID),
					Message: "relation formula paths must select one target field",
					Details: map[string]any{"reference": reference},
				}
			}
			target := definition
			if root.Relation.TargetTableID != definition.TableID {
				var err error
				target, err = catalog.Describe(
					ctx, root.Relation.TargetTableID,
				)
				if err != nil {
					return &schema.ProductError{
						Code:    "schema.formula.target_table_not_found",
						Path:    formulaPath(definition, compiled.FieldID),
						Message: "formula relation target table was not found",
						Details: map[string]any{"reference": reference},
					}
				}
			}
			found := false
			for _, targetField := range target.Fields {
				if targetField.PhysicalName == parts[1] &&
					targetField.DataType != schema.DataTypeSecret &&
					targetField.DataType != schema.DataTypeHash {
					found = true
					break
				}
			}
			if !found {
				return &schema.ProductError{
					Code:    "schema.formula.target_field_not_found",
					Path:    formulaPath(definition, compiled.FieldID),
					Message: "formula relation target field was not found",
					Details: map[string]any{"reference": reference},
				}
			}
		}
	}
	return nil
}

func physicalNameByFieldID(
	definition schema.TableDefinition,
	fieldID string,
) string {
	for _, field := range definition.Fields {
		if field.FieldID == fieldID {
			return field.PhysicalName
		}
	}
	return ""
}

func stringSliceContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func formulaPath(
	definition schema.TableDefinition,
	fieldID string,
) string {
	for index, field := range definition.Fields {
		if field.FieldID == fieldID {
			return fmt.Sprintf(
				"definition.fields[%d].formula.source", index,
			)
		}
	}
	return "definition.fields.formula.source"
}

func (catalog *Catalog) validateLookupReferences(
	ctx context.Context,
	definition schema.TableDefinition,
) error {
	for index, field := range definition.Fields {
		if field.Kind != schema.FieldKindLookup || field.Lookup == nil {
			continue
		}
		prefix := fmt.Sprintf("definition.fields[%d].lookup", index)
		path := field.Lookup.EffectivePath()
		current := definition
		var relation schema.FieldDefinition
		var target schema.TableDefinition
		for pathIndex, step := range path {
			var exists bool
			relation, exists = schemaFieldByID(current, step.RelationFieldID)
			if !exists || relation.Kind != schema.FieldKindRelation ||
				relation.Relation == nil {
				return &schema.ProductError{
					Code: "schema.lookup.relation_not_found",
					Path: fmt.Sprintf(
						"%s.path[%d].relationFieldId", prefix, pathIndex,
					),
					Message: "lookup path relation field was not found",
				}
			}
			mode := relation.Relation.EffectiveMode()
			targetTableID := relation.Relation.TargetTableID
			if mode == "m2a" {
				if step.M2ACollection == "" && pathIndex < len(path)-1 {
					return &schema.ProductError{
						Code: "schema.lookup.path_ambiguous",
						Path: fmt.Sprintf(
							"%s.path[%d].m2aCollection", prefix, pathIndex,
						),
						Message: "an intermediate m2a path step must select a collection",
					}
				}
				if step.M2ACollection != "" {
					if !stringSliceContains(
						relation.Relation.AllowedTargetTableIDs,
						step.M2ACollection,
					) {
						return &schema.ProductError{
							Code: "schema.lookup.m2a_collection_invalid",
							Path: fmt.Sprintf(
								"%s.path[%d].m2aCollection", prefix, pathIndex,
							),
							Message: "lookup path m2a collection is not allowed",
						}
					}
					targetTableID = step.M2ACollection
				}
			} else if step.M2ACollection != "" {
				return &schema.ProductError{
					Code: "schema.lookup.m2a_collection_invalid",
					Path: fmt.Sprintf(
						"%s.path[%d].m2aCollection", prefix, pathIndex,
					),
					Message: "m2aCollection is only valid for m2a relations",
				}
			}
			if mode == "m2a" && step.M2ACollection == "" {
				target = schema.TableDefinition{}
				continue
			}
			var describeErr error
			if targetTableID == definition.TableID {
				target = definition
			} else {
				target, describeErr = catalog.Describe(ctx, targetTableID)
				if describeErr != nil {
					return describeErr
				}
			}
			current = target
		}
		mode := relation.Relation.EffectiveMode()
		finalStep := path[len(path)-1]
		if field.Lookup.JunctionFieldID != "" {
			if mode == "direct" || relation.Relation.JunctionTableID == nil {
				return &schema.ProductError{
					Code:    "schema.lookup.junction_field_invalid",
					Path:    prefix + ".junctionFieldId",
					Message: "junction lookup requires a junction-backed relation",
				}
			}
			junction, describeErr := catalog.Describe(
				ctx, *relation.Relation.JunctionTableID,
			)
			if describeErr != nil {
				return describeErr
			}
			junctionField, found := schemaFieldByID(
				junction, field.Lookup.JunctionFieldID,
			)
			if !found || junctionField.DataType == schema.DataTypeSecret ||
				junctionField.DataType == schema.DataTypeHash ||
				junctionField.Kind != schema.FieldKindScalar {
				return &schema.ProductError{
					Code:    "schema.lookup.junction_field_invalid",
					Path:    prefix + ".junctionFieldId",
					Message: "junction lookup field was not found or is restricted",
				}
			}
			if err := validateLookupOutput(
				field, junctionField, prefix,
			); err != nil {
				return err
			}
			continue
		}
		if mode == "m2a" && finalStep.M2ACollection == "" {
			for allowedIndex, tableID := range relation.Relation.AllowedTargetTableIDs {
				targetFieldID, mapped := field.Lookup.TargetFieldIDs[tableID]
				if !mapped {
					return &schema.ProductError{
						Code: "schema.lookup.m2a_mapping_incomplete",
						Path: fmt.Sprintf(
							"%s.targetFieldIds.%s", prefix, tableID,
						),
						Message: "m2a lookup must map every allowed target table",
						Details: map[string]any{"allowedIndex": allowedIndex},
					}
				}
				target, describeErr := catalog.Describe(ctx, tableID)
				if describeErr != nil {
					return describeErr
				}
				targetField, found := schemaFieldByID(target, targetFieldID)
				if !found || targetField.DataType == schema.DataTypeSecret ||
					targetField.DataType == schema.DataTypeHash {
					return &schema.ProductError{
						Code: "schema.lookup.target_field_not_found",
						Path: fmt.Sprintf(
							"%s.targetFieldIds.%s", prefix, tableID,
						),
						Message: "m2a lookup target field was not found",
					}
				}
				if err := validateLookupOutput(
					field, targetField, prefix,
				); err != nil {
					return err
				}
			}
			if len(field.Lookup.TargetFieldIDs) !=
				len(relation.Relation.AllowedTargetTableIDs) {
				return &schema.ProductError{
					Code:    "schema.lookup.m2a_mapping_invalid",
					Path:    prefix + ".targetFieldIds",
					Message: "m2a lookup mapping contains a non-allowed table",
				}
			}
			continue
		}
		if len(field.Lookup.TargetFieldIDs) != 0 {
			return &schema.ProductError{
				Code:    "schema.lookup.m2a_mapping_invalid",
				Path:    prefix + ".targetFieldIds",
				Message: "lookup target field mapping is only valid for an unselected final m2a relation",
			}
		}
		var targetField *schema.FieldDefinition
		for targetIndex := range target.Fields {
			if target.Fields[targetIndex].FieldID == field.Lookup.TargetFieldID {
				targetField = &target.Fields[targetIndex]
				break
			}
		}
		if targetField == nil ||
			targetField.DataType == schema.DataTypeSecret ||
			targetField.DataType == schema.DataTypeHash {
			return &schema.ProductError{
				Code:    "schema.lookup.target_field_not_found",
				Path:    prefix + ".targetFieldId",
				Message: "lookup target field was not found",
			}
		}
		if err := validateLookupOutput(
			field, *targetField, prefix,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateLookupOutput(
	output schema.FieldDefinition,
	target schema.FieldDefinition,
	prefix string,
) error {
	switch output.Lookup.Aggregate {
	case "count", "countNonNull":
		if output.StorageType != schema.StorageNumber {
			return &schema.ProductError{
				Code:    "schema.lookup.output_type_mismatch",
				Path:    prefix + ".aggregate",
				Message: "count lookup must use number storage",
			}
		}
	case "sum", "avg":
		if output.StorageType != schema.StorageNumber ||
			target.StorageType != schema.StorageNumber {
			return &schema.ProductError{
				Code:    "schema.lookup.output_type_mismatch",
				Path:    prefix + ".aggregate",
				Message: "sum lookup requires numeric target and output storage",
			}
		}
	case "none":
		if output.StorageType != target.StorageType &&
			output.StorageType != schema.StorageJSON {
			return &schema.ProductError{
				Code:    "schema.lookup.output_type_mismatch",
				Path:    prefix + ".targetFieldId",
				Message: "values lookup must use target or JSON storage",
			}
		}
	case "distinct":
		if output.StorageType != schema.StorageJSON {
			return &schema.ProductError{
				Code:    "schema.lookup.output_type_mismatch",
				Path:    prefix + ".aggregate",
				Message: "distinct lookup must use JSON storage",
			}
		}
	case "first", "min", "max":
		if output.StorageType != target.StorageType {
			return &schema.ProductError{
				Code:    "schema.lookup.output_type_mismatch",
				Path:    prefix + ".targetFieldId",
				Message: "lookup output storage must match its target field",
			}
		}
	}
	return nil
}

func (catalog *Catalog) ApplyChange(
	ctx context.Context,
	change Change,
) (schema.TableDefinition, error) {
	validation, err := catalog.ValidateChange(ctx, change)
	if err != nil {
		return schema.TableDefinition{}, err
	}
	definition := validation.Definition
	requestHash := ""
	schemaCreateStarted := false
	if change.OperationID != "" {
		raw, marshalErr := json.Marshal(struct {
			Definition       schema.TableDefinition `json:"definition"`
			ExpectedRevision int64                  `json:"expectedRevision"`
		}{definition, change.ExpectedRevision})
		if marshalErr != nil {
			return schema.TableDefinition{}, storageError(marshalErr)
		}
		requestHash = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	err = catalog.app.RunInTransaction(func(txApp core.App) (transactionErr error) {
		defer func() {
			if transactionErr == nil {
				transactionErr = writecoordinator.PersistPocketBaseReceipt(
					ctx,
					txApp,
					time.Now().UTC(),
				)
			}
		}()
		if change.OperationID != "" {
			stored, findErr := txApp.FindFirstRecordByFilter(
				"vibetable_idempotency_keys",
				"key={:key}",
				dbx.Params{"key": "schema:" + change.OperationID},
			)
			if findErr == nil {
				if !stored.GetDateTime("expires_at").Time().After(
					time.Now().UTC(),
				) {
					if deleteErr := txApp.Delete(stored); deleteErr != nil {
						return storageError(deleteErr)
					}
					findErr = sql.ErrNoRows
				} else {
					if stored.GetString("request_hash") != requestHash {
						return &schema.ProductError{
							Code: "schema.idempotency_conflict", Path: "operationId",
							Message: "operation id was used for another schema change",
						}
					}
					if stored.GetString("status") != "applied" {
						return storageError(errors.New(
							"schema idempotency status is invalid",
						))
					}
					raw, marshalErr := json.Marshal(stored.GetRaw("receipt_json"))
					var replayed schema.TableDefinition
					if marshalErr != nil || json.Unmarshal(raw, &replayed) != nil ||
						!validSchemaReplay(
							replayed,
							definition.TableID,
							change.ExpectedRevision,
						) {
						return storageError(errors.New(
							"schema idempotency receipt is invalid",
						))
					}
					definition = replayed
					return nil
				}
			}
			if !errors.Is(findErr, sql.ErrNoRows) {
				return storageError(findErr)
			}
		}
		existing, findErr := catalog.findTable(txApp, definition.TableID)
		isCreate := errors.Is(findErr, sql.ErrNoRows)
		if findErr != nil && !isCreate {
			return storageError(findErr)
		}

		currentRevision := int64(0)
		var previousDefinition schema.TableDefinition
		if !isCreate {
			storedDefinition, _, decodeErr := decodeStoredTable(existing)
			if decodeErr != nil {
				return decodeErr
			}
			currentRevision, _ = schema.ParseSchemaRevision(storedDefinition.SchemaRevision)
			if compatibilityErr := validateCompatibleAlter(storedDefinition, definition); compatibilityErr != nil {
				return compatibilityErr
			}
			previousDefinition = storedDefinition
		}
		if currentRevision != change.ExpectedRevision {
			return &schema.ProductError{
				Code:    "schema.revision_conflict",
				Path:    "expectedRevision",
				Message: "schema revision does not match",
				Details: map[string]any{
					"expected": change.ExpectedRevision,
					"actual":   currentRevision,
				},
			}
		}
		preparedDefinition, formulaPlan, formulaErr := catalog.prepareFormulaState(txApp, definition)
		if formulaErr != nil {
			return formulaErr
		}
		definition = preparedDefinition

		var collection *core.Collection
		preservedFieldIDs := map[string]string{}
		if isCreate {
			if definition.Kind == schema.TableKindView {
				collection = core.NewViewCollection(definition.PhysicalName)
			} else {
				collection = core.NewBaseCollection(definition.PhysicalName)
			}
		} else {
			collection, findErr = txApp.FindCollectionByNameOrId(existing.GetString("collection_id"))
			if findErr != nil {
				return storageError(findErr)
			}
			if metadataErr := validateAutoDateMetadataCompletion(
				previousDefinition,
				definition,
				collection,
			); metadataErr != nil {
				return metadataErr
			}
			if hasNewAutoDateField(previousDefinition, definition) {
				recordCount, countErr := txApp.CountRecords(collection)
				if countErr != nil {
					return storageError(countErr)
				}
				if recordCount != 0 {
					autodateobs.Increment(autodateobs.BackfillRequired)
					return &schema.ProductError{
						Code:    "schema.field.autodate_backfill_required",
						Path:    "fields",
						Message: "automatic date fields require a trustworthy backfill for existing records",
						Details: map[string]any{"recordCount": recordCount},
					}
				}
			}
			collection.Name = definition.PhysicalName
			if definition.Kind == schema.TableKindBase {
				preservedFieldIDs, findErr = catalog.fieldIDsByProductID(txApp, definition.TableID, collection)
				if findErr != nil {
					return findErr
				}
				for index := len(collection.Fields) - 1; index >= 0; index-- {
					if !collection.Fields[index].GetSystem() {
						collection.Fields.RemoveById(collection.Fields[index].GetId())
					}
				}
				collection.Indexes = nil
			}
		}
		if isCreate && definition.Kind == schema.TableKindBase {
			// Persist the collection shell first so PocketBase can resolve a
			// relation whose target is this same collection. The surrounding
			// transaction keeps the two-phase create atomic.
			schemaCreateStarted = true
			if saveErr := txApp.Save(collection); saveErr != nil {
				return storageError(saveErr)
			}
		}
		if definition.Kind == schema.TableKindView {
			viewQuery, compileErr := catalog.compileViewQuery(
				ctx, txApp, definition,
			)
			if compileErr != nil {
				return compileErr
			}
			collection.ViewQuery = viewQuery
		} else {
			fields, compileErr := catalog.compileFields(txApp, definition, collection.Id)
			if compileErr != nil {
				return compileErr
			}
			for index, fieldDefinition := range definition.Fields {
				if fieldID := preservedFieldIDs[fieldDefinition.FieldID]; fieldID != "" {
					fields[index].SetId(fieldID)
				}
			}
			collection.Fields.Add(fields...)
			collection.Indexes = schema.CompileIndexes(definition)
		}
		if saveErr := txApp.Save(collection); saveErr != nil {
			return storageError(saveErr)
		}

		nextRevision := currentRevision + 1
		definition.SchemaRevision = schema.FormatSchemaRevision(nextRevision)
		definitionRaw, marshalErr := json.Marshal(definition)
		if marshalErr != nil {
			return storageError(marshalErr)
		}
		if isCreate {
			metaCollection, collectionErr := txApp.FindCollectionByNameOrId(tablesCollection)
			if collectionErr != nil {
				return storageError(collectionErr)
			}
			existing = core.NewRecord(metaCollection)
		}
		existing.Set("table_id", definition.TableID)
		existing.Set("collection_id", collection.Id)
		existing.Set("physical_name", definition.PhysicalName)
		existing.Set("display_name", definition.DisplayName)
		existing.Set("kind", string(definition.Kind))
		existing.Set("schema_revision", nextRevision)
		if isCreate {
			existing.Set("data_revision", 0)
		}
		archiveRaw, _ := json.Marshal(definition.ArchivePolicy)
		existing.Set("archive_policy", string(archiveRaw))
		existing.Set("definition_json", types.JSONRaw(definitionRaw))
		if saveErr := txApp.Save(existing); saveErr != nil {
			return storageError(saveErr)
		}
		if saveErr := catalog.replaceFieldMetadata(txApp, definition); saveErr != nil {
			return saveErr
		}
		if saveErr := catalog.replaceFormulaMetadata(txApp, definition, formulaPlan); saveErr != nil {
			return saveErr
		}
		if saveErr := catalog.replaceFormulaDependencyMetadata(
			txApp, definition, formulaPlan,
		); saveErr != nil {
			return saveErr
		}
		if saveErr := catalog.replaceRelationMetadata(txApp, definition); saveErr != nil {
			return saveErr
		}
		if saveErr := catalog.replaceLookupMetadata(txApp, definition); saveErr != nil {
			return saveErr
		}
		if change.OperationID != "" {
			collection, findErr := txApp.FindCollectionByNameOrId(
				"vibetable_idempotency_keys",
			)
			if findErr != nil {
				return storageError(findErr)
			}
			raw, marshalErr := json.Marshal(definition)
			if marshalErr != nil {
				return storageError(marshalErr)
			}
			stored := core.NewRecord(collection)
			stored.Set("key", "schema:"+change.OperationID)
			stored.Set("request_hash", requestHash)
			stored.Set("status", "applied")
			stored.Set("receipt_json", types.JSONRaw(raw))
			stored.Set("expires_at", time.Now().UTC().Add(24*time.Hour))
			if saveErr := txApp.Save(stored); saveErr != nil {
				return storageError(saveErr)
			}
		}
		return nil
	})
	if err != nil {
		if schemaCreateStarted {
			autodateobs.Increment(autodateobs.SchemaCreateRollback)
		}
		return schema.TableDefinition{}, err
	}
	return definition, nil
}

func (catalog *Catalog) compileViewQuery(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
) (string, error) {
	if definition.View == nil {
		return "", &schema.ProductError{
			Code: "schema.view.invalid", Path: "view",
			Message: "view source is required",
		}
	}
	sourceRecord, err := catalog.findTable(app, definition.View.SourceTableID)
	if err != nil {
		return "", &schema.ProductError{
			Code: "schema.view.source_not_found", Path: "view.sourceTableId",
			Message: "view source table was not found",
		}
	}
	source, _, err := decodeStoredTable(sourceRecord)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	columns := make([]string, 0, len(definition.Fields))
	columns = append(columns, "`id`")
	for _, field := range definition.Fields {
		columns = append(columns, "`"+field.PhysicalName+"`")
	}
	return "SELECT " + strings.Join(columns, ", ") +
		" FROM `" + source.PhysicalName + "`", nil
}

func validSchemaReplay(
	definition schema.TableDefinition,
	tableID string,
	expectedRevision int64,
) bool {
	if definition.ContractVersion != schema.ContractVersion ||
		definition.TableID != tableID ||
		definition.PhysicalName == "" {
		return false
	}
	revision, err := schema.ParseSchemaRevision(
		definition.SchemaRevision,
	)
	return err == nil &&
		expectedRevision < maxSafeRevision &&
		revision == expectedRevision+1
}

func (catalog *Catalog) DeleteTable(
	ctx context.Context,
	tableID string,
	expectedRevision int64,
) (DeleteResult, error) {
	if err := ctx.Err(); err != nil {
		return DeleteResult{}, err
	}
	if strings.TrimSpace(tableID) == "" {
		return DeleteResult{}, &schema.ProductError{
			Code: "schema.table_id.invalid", Path: "tableId",
			Message: "tableId is required",
		}
	}
	err := catalog.app.RunInTransaction(func(txApp core.App) (transactionErr error) {
		defer func() {
			if transactionErr == nil {
				transactionErr = writecoordinator.PersistPocketBaseReceipt(
					ctx,
					txApp,
					time.Now().UTC(),
				)
			}
		}()
		record, findErr := catalog.findTable(txApp, tableID)
		if findErr != nil {
			if errors.Is(findErr, sql.ErrNoRows) {
				return &schema.ProductError{
					Code: "schema.table.not_found", Path: "tableId",
					Message: "table was not found",
				}
			}
			return storageError(findErr)
		}
		definition, _, decodeErr := decodeStoredTable(record)
		if decodeErr != nil {
			return decodeErr
		}
		currentRevision, parseErr := schema.ParseSchemaRevision(definition.SchemaRevision)
		if parseErr != nil {
			return storageError(parseErr)
		}
		if currentRevision != expectedRevision {
			return &schema.ProductError{
				Code: "schema.revision_conflict", Path: "expectedRevision",
				Message: "schema revision does not match",
				Details: map[string]any{
					"expected": expectedRevision,
					"actual":   currentRevision,
				},
			}
		}
		if referenceErr := catalog.rejectReferencedDelete(txApp, tableID); referenceErr != nil {
			return referenceErr
		}

		collection, collectionErr := txApp.FindCollectionByNameOrId(
			record.GetString("collection_id"),
		)
		if collectionErr != nil {
			return storageError(collectionErr)
		}
		for _, cleanup := range []struct {
			collection string
			field      string
		}{
			{fieldsCollection, "table_id"},
			{formulasCollection, "table_id"},
			{formulaDepsCollection, "source_table_id"},
			{relationsCollection, "source_table_id"},
			{lookupsCollection, "table_id"},
			{"vibetable_audit_events", "table_id"},
			{"vibetable_jobs", "source_table_id"},
			{"vibetable_attachment_meta", "table_id"},
			{"vibetable_attachment_versions", "table_id"},
		} {
			if cleanupErr := deleteMetadataByTable(
				txApp, cleanup.collection, cleanup.field, tableID,
			); cleanupErr != nil {
				return cleanupErr
			}
		}
		if deleteErr := txApp.Delete(collection); deleteErr != nil {
			return storageError(deleteErr)
		}
		if deleteErr := txApp.Delete(record); deleteErr != nil {
			return storageError(deleteErr)
		}
		return nil
	})
	if err != nil {
		return DeleteResult{}, err
	}
	return DeleteResult{Deleted: true, TableID: tableID}, nil
}

func (catalog *Catalog) rejectReferencedDelete(app core.App, tableID string) error {
	records, err := app.FindAllRecords(tablesCollection)
	if err != nil {
		return storageError(err)
	}
	for _, record := range records {
		definition, _, decodeErr := decodeStoredTable(record)
		if decodeErr != nil {
			return decodeErr
		}
		if definition.TableID == tableID {
			continue
		}
		if definition.Kind == schema.TableKindView &&
			definition.View != nil &&
			definition.View.SourceTableID == tableID {
			return &schema.ProductError{
				Code: "schema.table.referenced", Path: "tableId",
				Message: "table is referenced by another table",
				Details: map[string]any{
					"sourceTableId": definition.TableID,
					"referenceKind": "view",
				},
			}
		}
		for _, field := range definition.Fields {
			if field.Relation == nil {
				continue
			}
			relation := field.Relation
			references := relation.TargetTableID == tableID
			if relation.JunctionTableID != nil && *relation.JunctionTableID == tableID {
				references = true
			}
			for _, allowed := range relation.AllowedTargetTableIDs {
				if allowed == tableID {
					references = true
					break
				}
			}
			if references {
				return &schema.ProductError{
					Code: "schema.table.referenced", Path: "tableId",
					Message: "table is referenced by another table",
					Details: map[string]any{
						"sourceTableId": definition.TableID,
						"sourceFieldId": field.FieldID,
					},
				}
			}
		}
	}
	return nil
}

func (catalog *Catalog) replaceFormulaDependencyMetadata(
	app core.App,
	definition schema.TableDefinition,
	plan *formula.Plan,
) error {
	if err := deleteMetadataByTable(
		app, formulaDepsCollection, "source_table_id", definition.TableID,
	); err != nil {
		return err
	}
	collection, err := app.FindCollectionByNameOrId(formulaDepsCollection)
	if err != nil {
		return storageError(err)
	}
	fieldsByName := make(map[string]schema.FieldDefinition, len(definition.Fields))
	targetCache := map[string]schema.TableDefinition{
		definition.TableID: definition,
	}
	for _, field := range definition.Fields {
		fieldsByName[field.PhysicalName] = field
	}
	for _, lookupField := range definition.Fields {
		if lookupField.Kind != schema.FieldKindLookup ||
			lookupField.Lookup == nil {
			continue
		}
		current := definition
		path := lookupField.Lookup.EffectivePath()
		pathRelations := make([]schema.FieldDefinition, 0, len(path))
		for _, step := range path {
			relationField, exists := schemaFieldByID(current, step.RelationFieldID)
			if !exists || relationField.Relation == nil {
				return &schema.ProductError{
					Code:    "schema.lookup.relation_not_found",
					Path:    "definition.fields.lookup.path",
					Message: "lookup path relation field was not found",
				}
			}
			pathRelations = append(pathRelations, relationField)
			targetTableID := relationField.Relation.TargetTableID
			if step.M2ACollection != "" {
				targetTableID = step.M2ACollection
			}
			if relationField.Relation.EffectiveMode() == "m2a" &&
				step.M2ACollection == "" {
				continue
			}
			target := targetCache[targetTableID]
			if target.TableID == "" {
				var describeErr error
				target, describeErr = New(app).Describe(
					context.Background(), targetTableID,
				)
				if describeErr != nil {
					return describeErr
				}
				targetCache[target.TableID] = target
			}
			current = target
		}
		relationField := pathRelations[len(pathRelations)-1]
		rootRelationField := pathRelations[0]
		saveDependency := func(targetTableID, targetFieldID string) error {
			record := core.NewRecord(collection)
			record.Set("source_table_id", definition.TableID)
			record.Set("formula_field_id", lookupField.FieldID)
			record.Set("relation_field_id", rootRelationField.FieldID)
			record.Set("target_table_id", targetTableID)
			record.Set("target_field_id", targetFieldID)
			record.Set("dependency_kind", "lookup")
			if err := app.Save(record); err != nil {
				return storageError(err)
			}
			return nil
		}
		for _, pathRelation := range pathRelations {
			if pathRelation.Relation.EffectiveMode() == "direct" ||
				pathRelation.Relation.JunctionTableID == nil {
				continue
			}
			junctionTableID := *pathRelation.Relation.JunctionTableID
			junctionDependencies := []string{
				pathRelation.Relation.JunctionSourceFieldID,
				pathRelation.Relation.JunctionTargetFieldID,
			}
			if pathRelation.Relation.JunctionDiscriminatorFieldID != "" {
				junctionDependencies = append(
					junctionDependencies,
					pathRelation.Relation.JunctionDiscriminatorFieldID,
				)
			}
			if pathRelation.FieldID == relationField.FieldID &&
				lookupField.Lookup.JunctionFieldID != "" {
				junctionDependencies = append(
					junctionDependencies,
					lookupField.Lookup.JunctionFieldID,
				)
			}
			seenJunctionFields := map[string]struct{}{}
			for _, fieldID := range junctionDependencies {
				if fieldID == "" {
					continue
				}
				if _, duplicate := seenJunctionFields[fieldID]; duplicate {
					continue
				}
				seenJunctionFields[fieldID] = struct{}{}
				if err := saveDependency(junctionTableID, fieldID); err != nil {
					return err
				}
			}
		}
		if lookupField.Lookup.JunctionFieldID != "" {
			continue
		}
		finalStep := path[len(path)-1]
		if relationField.Relation.EffectiveMode() == "m2a" &&
			finalStep.M2ACollection == "" {
			for _, tableID := range relationField.Relation.AllowedTargetTableIDs {
				if err := saveDependency(
					tableID, lookupField.Lookup.TargetFieldIDs[tableID],
				); err != nil {
					return err
				}
			}
			continue
		}
		targetTableID := relationField.Relation.TargetTableID
		if finalStep.M2ACollection != "" {
			targetTableID = finalStep.M2ACollection
		}
		if err := saveDependency(
			targetTableID,
			lookupField.Lookup.TargetFieldID,
		); err != nil {
			return err
		}
	}
	for _, compiled := range plan.Formulas {
		for _, reference := range compiled.ReferencePaths {
			parts := strings.Split(reference, ".")
			relationField, exists := fieldsByName[parts[0]]
			if !exists || relationField.Kind != schema.FieldKindRelation ||
				relationField.Relation == nil || len(parts) != 2 {
				continue
			}
			target := targetCache[relationField.Relation.TargetTableID]
			if target.TableID == "" {
				target, err = New(app).Describe(
					context.Background(),
					relationField.Relation.TargetTableID,
				)
				if err != nil {
					return err
				}
				targetCache[target.TableID] = target
			}
			targetFieldID := ""
			for _, targetField := range target.Fields {
				if targetField.PhysicalName == parts[1] {
					targetFieldID = targetField.FieldID
					break
				}
			}
			record := core.NewRecord(collection)
			record.Set("source_table_id", definition.TableID)
			record.Set("formula_field_id", compiled.FieldID)
			record.Set("relation_field_id", relationField.FieldID)
			record.Set(
				"target_table_id",
				relationField.Relation.TargetTableID,
			)
			record.Set("target_field_id", targetFieldID)
			record.Set("dependency_kind", "relation")
			if err := app.Save(record); err != nil {
				return storageError(err)
			}
		}
	}
	return nil
}

func (catalog *Catalog) GetRevision(
	ctx context.Context,
	tableID string,
) (int64, error) {
	definition, err := catalog.Describe(ctx, tableID)
	if err != nil {
		return 0, err
	}
	return schema.ParseSchemaRevision(definition.SchemaRevision)
}

func (catalog *Catalog) GetDataRevision(
	ctx context.Context,
	tableID string,
) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	record, err := catalog.findTable(catalog.app, tableID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, &schema.ProductError{
				Code: "schema.table.not_found", Path: "tableId",
				Message: "table was not found",
			}
		}
		return 0, storageError(err)
	}
	_, dataRevision, err := decodeStoredTable(record)
	return dataRevision, err
}

func normalize(definition schema.TableDefinition) (schema.TableDefinition, error) {
	for index := range definition.Fields {
		capabilityType := definition.Fields[index].DataType
		if capabilityType == schema.DataTypeFormula && definition.Fields[index].Formula != nil {
			capabilityType = definition.Fields[index].Formula.ResultType
		}
		capability, err := schema.CapabilityFor(capabilityType)
		if err != nil {
			return schema.TableDefinition{}, &schema.ProductError{
				Code:    "schema.field.unsupported_type",
				Path:    fmt.Sprintf("fields[%d].dataType", index),
				Message: err.Error(),
			}
		}
		if definition.Fields[index].StorageType == "" {
			definition.Fields[index].StorageType = capability.Storage
		}
		if definition.Kind == schema.TableKindView ||
			definition.Fields[index].Kind == schema.FieldKindFormula ||
			definition.Fields[index].Kind == schema.FieldKindLookup ||
			definition.Fields[index].Kind == schema.FieldKindSystem {
			definition.Fields[index].ReadOnly = true
		}
	}
	if err := schema.Validate(definition); err != nil {
		return schema.TableDefinition{}, err
	}
	if _, formulaErr := formula.NewCompiler(formula.DefaultLimits()).CompileTable(definition); formulaErr != nil {
		return schema.TableDefinition{}, formulaErr
	}
	return definition, nil
}

func (catalog *Catalog) fieldIDsByProductID(
	app core.App,
	tableID string,
	collection *core.Collection,
) (map[string]string, error) {
	records, err := app.FindRecordsByFilter(
		fieldsCollection, "table_id={:table}", "", 0, 0, dbx.Params{"table": tableID},
	)
	if err != nil {
		return nil, storageError(err)
	}
	result := make(map[string]string, len(records))
	for _, record := range records {
		field := collection.Fields.GetByName(record.GetString("physical_name"))
		if field != nil {
			result[record.GetString("field_id")] = field.GetId()
		}
	}
	return result, nil
}

func (catalog *Catalog) compileFields(
	app core.App,
	definition schema.TableDefinition,
	selfCollectionID string,
) ([]core.Field, error) {
	fields := make([]core.Field, 0, len(definition.Fields))
	resolve := func(tableID string) (string, error) {
		if tableID == definition.TableID && selfCollectionID != "" {
			return selfCollectionID, nil
		}
		record, err := catalog.findTable(app, tableID)
		if err != nil {
			return "", &schema.ProductError{
				Code:    "schema.relation.target_not_found",
				Path:    "relation.targetTableId",
				Message: "relation target table was not found",
				Details: map[string]any{"tableId": tableID},
			}
		}
		return record.GetString("collection_id"), nil
	}
	for index, definition := range definition.Fields {
		field, err := schema.CompileField(definition, resolve)
		if err != nil {
			if productErr, ok := err.(*schema.ProductError); ok {
				if productErr.Path == "relation.targetTableId" {
					productErr.Path = fmt.Sprintf("fields[%d].relation.targetTableId", index)
				}
				return nil, productErr
			}
			return nil, &schema.ProductError{
				Code:    "schema.field.compile_failed",
				Path:    fmt.Sprintf("fields[%d]", index),
				Message: err.Error(),
			}
		}
		fields = append(fields, field)
	}
	return fields, nil
}

func (catalog *Catalog) replaceFieldMetadata(
	app core.App,
	definition schema.TableDefinition,
) error {
	records, err := app.FindRecordsByFilter(
		fieldsCollection,
		"table_id={:table}",
		"",
		0,
		0,
		dbx.Params{"table": definition.TableID},
	)
	if err != nil {
		return storageError(err)
	}
	for _, record := range records {
		if err := app.Delete(record); err != nil {
			return storageError(err)
		}
	}
	collection, err := app.FindCollectionByNameOrId(fieldsCollection)
	if err != nil {
		return storageError(err)
	}
	for _, field := range definition.Fields {
		constraints, _ := json.Marshal(field.Constraints)
		record := core.NewRecord(collection)
		record.Set("table_id", definition.TableID)
		record.Set("field_id", field.FieldID)
		record.Set("physical_name", field.PhysicalName)
		record.Set("display_name", field.DisplayName)
		record.Set("kind", string(field.Kind))
		record.Set("data_type", string(field.DataType))
		record.Set("storage_type", string(field.StorageType))
		record.Set("constraints_json", types.JSONRaw(constraints))
		editor, _ := json.Marshal(field.Editor)
		record.Set("editor_json", types.JSONRaw(editor))
		if err := app.Save(record); err != nil {
			return storageError(err)
		}
	}
	return nil
}

func (catalog *Catalog) prepareFormulaState(
	app core.App,
	definition schema.TableDefinition,
) (schema.TableDefinition, *formula.Plan, error) {
	plan, formulaErr := formula.NewCompiler(formula.DefaultLimits()).CompileTable(definition)
	if formulaErr != nil {
		return schema.TableDefinition{}, nil, formulaErr
	}
	existing, err := app.FindRecordsByFilter(
		formulasCollection,
		"table_id={:table}",
		"",
		0,
		0,
		dbx.Params{"table": definition.TableID},
	)
	if err != nil {
		return schema.TableDefinition{}, nil, storageError(err)
	}
	existingByFieldID := make(map[string]*core.Record, len(existing))
	for _, record := range existing {
		existingByFieldID[record.GetString("field_id")] = record
	}
	compiledByFieldID := make(map[string]*formula.CompiledFormula, len(plan.Formulas))
	for _, compiled := range plan.Formulas {
		compiledByFieldID[compiled.FieldID] = compiled
	}
	for index := range definition.Fields {
		field := &definition.Fields[index]
		if field.Kind != schema.FieldKindFormula || field.Formula == nil {
			continue
		}
		// Formula version and lifecycle status are server-owned. Copy the
		// spec before mutating it so callers' nested values aren't aliased.
		spec := *field.Formula
		field.Formula = &spec
		compiled := compiledByFieldID[field.FieldID]
		record := existingByFieldID[field.FieldID]
		if record == nil {
			spec.Version = 1
			spec.Status = "backfilling"
			continue
		}
		version := record.GetInt("version")
		if version < 1 {
			version = 1
		}
		unchanged := record.GetString("source") == spec.Source &&
			record.GetString("language") == spec.Language &&
			record.GetString("result_type") == string(spec.ResultType) &&
			record.GetString("ast_hash") == compiled.ASTHash
		if unchanged {
			spec.Version = version
			spec.Status = record.GetString("status")
			if spec.Status == "" {
				spec.Status = "backfilling"
			}
			continue
		}
		spec.Version = version + 1
		spec.Status = "backfilling"
	}
	return definition, plan, nil
}

func (catalog *Catalog) replaceFormulaMetadata(
	app core.App,
	definition schema.TableDefinition,
	plan *formula.Plan,
) error {
	records, err := app.FindRecordsByFilter(
		formulasCollection,
		"table_id={:table}",
		"",
		0,
		0,
		dbx.Params{"table": definition.TableID},
	)
	if err != nil {
		return storageError(err)
	}
	for _, record := range records {
		if err := app.Delete(record); err != nil {
			return storageError(err)
		}
	}
	collection, err := app.FindCollectionByNameOrId(formulasCollection)
	if err != nil {
		return storageError(err)
	}
	fieldsByID := make(map[string]schema.FieldDefinition, len(definition.Fields))
	for _, field := range definition.Fields {
		fieldsByID[field.FieldID] = field
	}
	for _, compiled := range plan.Formulas {
		field := fieldsByID[compiled.FieldID]
		dependencies, marshalErr := json.Marshal(compiled.Dependencies)
		if marshalErr != nil {
			return storageError(marshalErr)
		}
		record := core.NewRecord(collection)
		record.Set("table_id", definition.TableID)
		record.Set("field_id", compiled.FieldID)
		record.Set("source", compiled.Source)
		record.Set("language", field.Formula.Language)
		record.Set("result_type", string(compiled.ResultType))
		record.Set("ast_hash", compiled.ASTHash)
		record.Set("dependencies_json", types.JSONRaw(dependencies))
		record.Set("version", field.Formula.Version)
		record.Set("status", field.Formula.Status)
		if err := app.Save(record); err != nil {
			return storageError(err)
		}
	}
	return nil
}

func (catalog *Catalog) replaceRelationMetadata(
	app core.App,
	definition schema.TableDefinition,
) error {
	if err := deleteMetadataByTable(
		app, relationsCollection, "source_table_id", definition.TableID,
	); err != nil {
		return err
	}
	collection, err := app.FindCollectionByNameOrId(relationsCollection)
	if err != nil {
		return storageError(err)
	}
	for _, field := range definition.Fields {
		if field.Kind != schema.FieldKindRelation || field.Relation == nil {
			continue
		}
		record := core.NewRecord(collection)
		record.Set("relation_id", definition.TableID+"."+field.FieldID)
		record.Set("source_table_id", definition.TableID)
		record.Set("source_field_id", field.FieldID)
		record.Set("target_table_id", field.Relation.TargetTableID)
		record.Set("cardinality", field.Relation.Cardinality)
		if field.Relation.JunctionTableID != nil {
			record.Set("junction_table_id", *field.Relation.JunctionTableID)
		}
		record.Set("delete_policy", field.Relation.DeletePolicy)
		if err := app.Save(record); err != nil {
			return storageError(err)
		}
	}
	return nil
}

func (catalog *Catalog) replaceLookupMetadata(
	app core.App,
	definition schema.TableDefinition,
) error {
	existing, err := app.FindRecordsByFilter(
		lookupsCollection,
		"table_id={:table}",
		"",
		0,
		0,
		dbx.Params{"table": definition.TableID},
	)
	if err != nil {
		return storageError(err)
	}
	existingByID := make(map[string]*core.Record, len(existing))
	for _, record := range existing {
		existingByID[record.GetString("lookup_id")] = record
		if err := app.Delete(record); err != nil {
			return storageError(err)
		}
	}
	collection, err := app.FindCollectionByNameOrId(lookupsCollection)
	if err != nil {
		return storageError(err)
	}
	for _, field := range definition.Fields {
		if field.Kind != schema.FieldKindLookup || field.Lookup == nil {
			continue
		}
		pathRaw, marshalErr := json.Marshal(map[string]any{
			"relationFieldId": field.Lookup.RelationFieldID,
			"path":            field.Lookup.EffectivePath(),
			"targetFieldId":   field.Lookup.TargetFieldID,
			"junctionFieldId": field.Lookup.JunctionFieldID,
			"targetFieldIds":  field.Lookup.TargetFieldIDs,
			"aggregate":       field.Lookup.Aggregate,
			"physicalName":    field.PhysicalName,
			"displayName":     field.DisplayName,
			"outputType":      field.StorageType,
		})
		if marshalErr != nil {
			return storageError(marshalErr)
		}
		lookupID := definition.TableID + "." + field.FieldID
		revision := 1
		if previous := existingByID[lookupID]; previous != nil {
			revision = previous.GetInt("revision")
			if revision < 1 {
				revision = 1
			}
			if previous.GetString("path_json") != string(pathRaw) {
				revision++
			}
		}
		record := core.NewRecord(collection)
		record.Set("lookup_id", lookupID)
		record.Set("table_id", definition.TableID)
		record.Set("field_id", field.FieldID)
		record.Set("relation_field_id", field.Lookup.RelationFieldID)
		record.Set("target_field_id", field.Lookup.TargetFieldID)
		record.Set("path_json", types.JSONRaw(pathRaw))
		record.Set("aggregate", field.Lookup.Aggregate)
		record.Set("output_type", string(field.StorageType))
		record.Set("revision", revision)
		if err := app.Save(record); err != nil {
			return storageError(err)
		}
	}
	return nil
}

func deleteMetadataByTable(
	app core.App,
	collectionName string,
	tableField string,
	tableID string,
) error {
	records, err := app.FindRecordsByFilter(
		collectionName,
		tableField+"={:table}",
		"",
		0,
		0,
		dbx.Params{"table": tableID},
	)
	if err != nil {
		return storageError(err)
	}
	for _, record := range records {
		if err := app.Delete(record); err != nil {
			return storageError(err)
		}
	}
	return nil
}

func (catalog *Catalog) findTable(app core.App, tableID string) (*core.Record, error) {
	return app.FindFirstRecordByFilter(
		tablesCollection,
		"table_id={:table}",
		dbx.Params{"table": tableID},
	)
}

func storageError(err error) *schema.ProductError {
	return &schema.ProductError{
		Code: "schema.storage.failed", Path: "", Message: "schema storage operation failed",
	}
}

func decodeStoredTable(
	record *core.Record,
) (schema.TableDefinition, int64, error) {
	storedSchemaRevision, err := parseStoredRevision(
		record.GetRaw("schema_revision"),
		"schema.metadata.invalid_schema_revision",
		"schemaRevision",
	)
	if err != nil {
		return schema.TableDefinition{}, 0, err
	}
	dataRevision, err := parseStoredRevision(
		record.GetRaw("data_revision"),
		"schema.metadata.invalid_data_revision",
		"dataRevision",
	)
	if err != nil {
		return schema.TableDefinition{}, 0, err
	}
	var definition schema.TableDefinition
	raw, marshalErr := json.Marshal(record.GetRaw("definition_json"))
	if marshalErr == nil {
		marshalErr = json.Unmarshal(raw, &definition)
	}
	if marshalErr != nil {
		return schema.TableDefinition{}, 0, storageError(marshalErr)
	}
	definitionRevision, parseErr := schema.ParseSchemaRevision(definition.SchemaRevision)
	if parseErr != nil || definitionRevision != storedSchemaRevision {
		return schema.TableDefinition{}, 0, &schema.ProductError{
			Code: "schema.metadata.revision_mismatch", Path: "schemaRevision",
			Message: "stored schema revision metadata is inconsistent",
		}
	}
	return definition, dataRevision, nil
}

func parseStoredRevision(
	value any,
	code string,
	path string,
) (int64, error) {
	var revision int64
	switch number := value.(type) {
	case float64:
		if number < 0 || number > float64(maxSafeRevision) ||
			math.Trunc(number) != number {
			return 0, invalidStoredRevision(code, path)
		}
		revision = int64(number)
	case float32:
		converted := float64(number)
		if converted < 0 || converted > float64(maxSafeRevision) ||
			math.Trunc(converted) != converted {
			return 0, invalidStoredRevision(code, path)
		}
		revision = int64(number)
	case int:
		revision = int64(number)
	case int64:
		revision = number
	case int32:
		revision = int64(number)
	case uint:
		if uint64(number) > uint64(maxSafeRevision) {
			return 0, invalidStoredRevision(code, path)
		}
		revision = int64(number)
	case uint64:
		if number > uint64(maxSafeRevision) {
			return 0, invalidStoredRevision(code, path)
		}
		revision = int64(number)
	case uint32:
		revision = int64(number)
	case json.Number:
		parsed, parseErr := number.Int64()
		if parseErr != nil {
			return 0, invalidStoredRevision(code, path)
		}
		revision = parsed
	default:
		return 0, invalidStoredRevision(code, path)
	}
	if revision < 0 || revision > maxSafeRevision {
		return 0, invalidStoredRevision(code, path)
	}
	return revision, nil
}

func validateStoredDataRevision(value any) error {
	_, err := parseStoredRevision(
		value,
		"schema.metadata.invalid_data_revision",
		"dataRevision",
	)
	return err
}

func invalidStoredRevision(code, path string) *schema.ProductError {
	return &schema.ProductError{
		Code: code, Path: path,
		Message: "stored revision must be a present non-negative safe integer",
	}
}

func validateCompatibleAlter(
	previous schema.TableDefinition,
	next schema.TableDefinition,
) error {
	previousByID := make(map[string]schema.FieldDefinition, len(previous.Fields))
	for _, field := range previous.Fields {
		previousByID[field.FieldID] = field
	}
	for index, field := range next.Fields {
		old, exists := previousByID[field.FieldID]
		if !exists {
			continue
		}
		incompatible := old.Kind != field.Kind ||
			old.DataType != field.DataType ||
			old.StorageType != field.StorageType
		if !incompatible && field.DataType == schema.DataTypeFormula {
			incompatible = old.Formula == nil || field.Formula == nil ||
				old.Formula.ResultType != field.Formula.ResultType
		}
		if !incompatible && field.DataType == schema.DataTypeRelation {
			incompatible = old.Relation == nil || field.Relation == nil ||
				old.Relation.TargetTableID != field.Relation.TargetTableID ||
				old.Relation.Cardinality != field.Relation.Cardinality
		}
		if !incompatible && field.DataType == schema.DataTypeAutoDate {
			if field.AutoDate == nil ||
				(old.AutoDate != nil && old.AutoDate.Role != field.AutoDate.Role) {
				autodateobs.Increment(autodateobs.RoleImmutable)
				return &schema.ProductError{
					Code:    "schema.field.autodate_role_immutable",
					Path:    fmt.Sprintf("fields[%d].autoDate.role", index),
					Message: "changing an automatic date role requires an explicit data migration",
					Details: map[string]any{"fieldId": field.FieldID},
				}
			}
		}
		if incompatible {
			return &schema.ProductError{
				Code:    "schema.field.type_change_unsupported",
				Path:    fmt.Sprintf("fields[%d].dataType", index),
				Message: "changing the persisted field type requires an explicit data migration",
				Details: map[string]any{"fieldId": field.FieldID},
			}
		}
	}
	if previous.Kind != next.Kind {
		return &schema.ProductError{
			Code: "schema.table.kind_change_unsupported", Path: "kind",
			Message: "changing table kind requires an explicit migration",
		}
	}
	return nil
}

func validateAutoDateMetadataCompletion(
	previous schema.TableDefinition,
	next schema.TableDefinition,
	collection *core.Collection,
) error {
	previousByID := make(map[string]schema.FieldDefinition, len(previous.Fields))
	for _, field := range previous.Fields {
		previousByID[field.FieldID] = field
	}
	for index, field := range next.Fields {
		old, exists := previousByID[field.FieldID]
		if !exists || old.DataType != schema.DataTypeAutoDate ||
			field.AutoDate == nil {
			continue
		}
		pbField, ok := collection.Fields.GetByName(
			field.PhysicalName,
		).(*core.AutodateField)
		if !ok {
			return &schema.ProductError{
				Code:    "schema.field.autodate_role_conflict",
				Path:    fmt.Sprintf("fields[%d].autoDate.role", index),
				Message: "legacy automatic date field is missing from PocketBase",
				Details: map[string]any{"fieldId": field.FieldID},
			}
		}
		var actual schema.AutoDateRole
		switch {
		case pbField.OnCreate && !pbField.OnUpdate:
			actual = schema.AutoDateRoleCreatedAt
		case pbField.OnCreate && pbField.OnUpdate:
			actual = schema.AutoDateRoleUpdatedAt
		default:
			return &schema.ProductError{
				Code:    "schema.field.autodate_role_conflict",
				Path:    fmt.Sprintf("fields[%d].autoDate.role", index),
				Message: "legacy automatic date switches do not map to a supported role",
				Details: map[string]any{
					"fieldId":  field.FieldID,
					"onCreate": pbField.OnCreate,
					"onUpdate": pbField.OnUpdate,
				},
			}
		}
		expected := field.AutoDate.Role
		if old.AutoDate != nil {
			expected = old.AutoDate.Role
		}
		if expected != actual {
			return &schema.ProductError{
				Code:    "schema.field.autodate_role_conflict",
				Path:    fmt.Sprintf("fields[%d].autoDate.role", index),
				Message: "declared automatic date role conflicts with PocketBase switches",
				Details: map[string]any{
					"fieldId":      field.FieldID,
					"actualRole":   actual,
					"declaredRole": expected,
				},
			}
		}
	}
	return nil
}

func hasNewAutoDateField(
	previous schema.TableDefinition,
	next schema.TableDefinition,
) bool {
	previousIDs := make(map[string]struct{}, len(previous.Fields))
	for _, field := range previous.Fields {
		previousIDs[field.FieldID] = struct{}{}
	}
	for _, field := range next.Fields {
		if field.DataType == schema.DataTypeAutoDate {
			if _, exists := previousIDs[field.FieldID]; !exists {
				return true
			}
		}
	}
	return false
}
