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
	"github.com/vibetable/vibetable/sidecar/internal/fieldprojection"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
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
	descriptor, _, err := source.describeSelectionTable(ctx, app, tableID)
	return descriptor, err
}

// DescribeSelectionTable derives the execution descriptor and public schema
// snapshot from the same caller-owned app handle. QueryPort passes a
// transactional handle here for atomic selection projection.
func (source *Source) DescribeSelectionTable(
	ctx context.Context,
	app core.App,
	tableID string,
) (query.TableDescriptor, v2.SchemaSnapshot, error) {
	return source.describeSelectionTable(ctx, app, tableID)
}

func (source *Source) describeSelectionTable(
	ctx context.Context,
	app core.App,
	tableID string,
) (query.TableDescriptor, v2.SchemaSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return query.TableDescriptor{}, v2.SchemaSnapshot{}, err
	}
	if source == nil || source.databaseID == "" || app == nil {
		return query.TableDescriptor{}, v2.SchemaSnapshot{}, &query.ProductError{
			Code: "query.schema.unconfigured", Path: "table",
			Message: "query schema source is not configured",
		}
	}
	table, err := schemaexecution.Describe(ctx, app, tableID)
	if err != nil {
		return query.TableDescriptor{}, v2.SchemaSnapshot{}, mapSchemaError(err)
	}
	fields, err := source.describeFields(ctx, app, table)
	if err != nil {
		return query.TableDescriptor{}, v2.SchemaSnapshot{}, err
	}
	descriptor := query.TableDescriptor{
		DatabaseID: source.databaseID, TableID: table.Snapshot.TableID,
		PhysicalName: table.PhysicalName, PrimaryKey: "id",
		SchemaRevision:  table.Snapshot.SchemaRevision,
		DataRevision:    table.Snapshot.DataRevision,
		Fields:          fields,
		ArchiveMode:     query.ArchiveMode(table.ArchivePolicy.Mode),
		RowRevisionName: relatedcomputation.RowRevisionField,
		ArchiveValue:    table.ArchivePolicy.ArchivedValue,
	}
	descriptor.DigestFields = make([]string, 0, len(table.Snapshot.Fields))
	descriptor.PresenceFields = make(map[string]string)
	for _, field := range table.Snapshot.Fields {
		descriptor.DigestFields = append(
			descriptor.DigestFields,
			field.Identity.PhysicalName,
		)
		if field.Value.Presence.Mode == v2.PresenceCompanion {
			descriptor.PresenceFields[field.Identity.PhysicalName] =
				field.Value.Presence.PhysicalName
		}
	}
	descriptor.DigestProjector = func(record *core.Record) map[string]any {
		return projectRecord(table.Snapshot.Fields, record)
	}
	if table.ArchivePolicy.FieldID != nil {
		field, ok := table.Field(*table.ArchivePolicy.FieldID)
		if !ok {
			return query.TableDescriptor{}, v2.SchemaSnapshot{}, &query.ProductError{
				Code: "query.schema.invalid", Path: "archivePolicy.fieldId",
				Message: "archive field is not queryable",
			}
		}
		descriptor.ArchiveField = field.Identity.PhysicalName
	}
	return descriptor, table.Snapshot, nil
}

func projectRecord(fields []v2.FieldDefinition, record *core.Record) map[string]any {
	fieldNames := make([]string, 0, len(fields))
	physical := make(map[string]any)
	for _, field := range fields {
		name := field.Identity.PhysicalName
		fieldNames = append(fieldNames, name)
		physical[name] = record.GetRaw(name)
		if field.Value.Presence.PhysicalName != "" {
			physical[field.Value.Presence.PhysicalName] =
				record.GetRaw(field.Value.Presence.PhysicalName)
		}
	}
	row := productrow.FromRecord(fieldNames, record)
	for _, field := range fields {
		name := field.Identity.PhysicalName
		if isComputed(field) {
			row[name] = relatedcomputation.ProjectStored(row[name])
			continue
		}
		row[name] = (fieldprojection.Descriptor{Definition: field}).ProductValue(physical)
	}
	return row
}

func (source *Source) describeFields(
	ctx context.Context,
	app core.App,
	table schemaexecution.Table,
) (map[string]query.FieldDescriptor, error) {
	fields := map[string]query.FieldDescriptor{
		"id": {PhysicalName: "id", Type: query.FieldTypeText},
	}
	for _, field := range table.Snapshot.Fields {
		descriptor, err := source.describeField(ctx, app, table, field)
		if err != nil {
			return nil, err
		}
		fields[field.Identity.PhysicalName] = descriptor
	}
	return fields, nil
}

func (source *Source) describeField(
	ctx context.Context,
	app core.App,
	table schemaexecution.Table,
	field v2.FieldDefinition,
) (query.FieldDescriptor, error) {
	fieldType, err := queryFieldType(field)
	if err == nil && field.LogicalType == v2.LogicalLookup && field.Lookup != nil {
		fieldType, err = source.lookupResultFieldType(ctx, app, table, *field.Lookup)
	}
	if err != nil {
		return query.FieldDescriptor{}, err
	}
	computed := isComputed(field)
	computedReady := field.LogicalType != v2.LogicalFormula
	formulaStatus := ""
	if field.LogicalType == v2.LogicalFormula {
		formulaStatus = table.FormulaRuntime[field.Identity.FieldID].Status
		computedReady = formulaStatus == "ready"
	}
	result := query.FieldDescriptor{
		PhysicalName:     field.Identity.PhysicalName,
		Type:             fieldType,
		AutoDate:         field.LogicalType == v2.LogicalAutoDate,
		Searchable:       isSearchable(field),
		ComputedEnvelope: computed,
		ComputedReady:    computedReady,
	}
	if result.ComputedEnvelope && result.ComputedReady {
		expectation, expectationErr := relatedcomputation.ExpectationFor(
			ctx, app, table.Snapshot.TableID, table.Snapshot.Fields,
			field.Identity.FieldID, 1,
		)
		if expectationErr != nil {
			return query.FieldDescriptor{}, &query.ProductError{
				Code:    "query.computed.version_unavailable",
				Path:    "fields." + field.Identity.PhysicalName,
				Message: "computed field version is unavailable",
			}
		}
		result.ComputedDefinitionVersion = expectation.DefinitionVersion
		result.ComputedDependencyWatermark = expectation.DependencyWatermark
	}
	if field.LogicalType == v2.LogicalFormula && !result.ComputedReady {
		result.ComputedStatus = "updating"
		result.ComputedError = &query.ComputedDiagnostic{
			Code: "calculation.pending", Path: "fields." + field.Identity.PhysicalName,
			Message: "formula value is being recalculated", Details: map[string]any{},
		}
		if formulaStatus == "failed" {
			result.ComputedStatus = "failed"
			result.ComputedError.Code = "calculation.failed"
			result.ComputedError.Message = "formula recalculation failed"
		} else if formulaStatus == "cancelled" {
			result.ComputedStatus = "cancelled"
			result.ComputedError.Code = "calculation.cancelled"
			result.ComputedError.Message = "formula recalculation was cancelled"
		}
	}
	if field.LogicalType == v2.LogicalSelect ||
		field.LogicalType == v2.LogicalMultiSelect {
		if field.Select == nil {
			return query.FieldDescriptor{}, &query.ProductError{
				Code: "query.schema.invalid", Path: "fields." + field.Identity.PhysicalName,
				Message: "select options are unavailable",
			}
		}
		result.Enum = enumDescriptor(field)
	}
	if field.LogicalType != v2.LogicalRelation || field.Relation == nil {
		return result, nil
	}
	target, err := schemaexecution.Describe(ctx, app, field.Relation.TargetTableID)
	if err != nil {
		return query.FieldDescriptor{}, mapSchemaError(err)
	}
	targetFields := map[string]query.FieldDescriptor{
		"id": {PhysicalName: "id", Type: query.FieldTypeText},
	}
	for _, targetField := range target.Snapshot.Fields {
		if targetField.LogicalType == v2.LogicalRelation {
			continue
		}
		targetDescriptor, describeErr := source.describeField(
			ctx, app, target, targetField,
		)
		if describeErr != nil {
			return query.FieldDescriptor{}, describeErr
		}
		targetFields[targetField.Identity.PhysicalName] = targetDescriptor
	}
	result.Relation = &query.RelationDescriptor{
		TableName: target.PhysicalName, PrimaryKey: "id",
		RowRevisionName: relatedcomputation.RowRevisionField,
		Fields:          targetFields,
		Multiple:        field.Relation.Cardinality == "many",
	}
	return result, nil
}

func (source *Source) lookupResultFieldType(
	ctx context.Context,
	app core.App,
	table schemaexecution.Table,
	spec v2.LookupSpec,
) (query.FieldType, error) {
	current := table
	resultMany := false
	for index, step := range spec.Path {
		relationField, found := current.Field(step.RelationFieldID)
		if !found || relationField.Relation == nil {
			return "", &query.ProductError{
				Code:    "query.schema.invalid",
				Path:    fmt.Sprintf("fields.lookup.path[%d]", index),
				Message: "lookup path relation metadata is unavailable",
			}
		}
		if relationField.Relation.Cardinality == "many" {
			resultMany = true
		}
		target, err := schemaexecution.Describe(
			ctx, app, relationField.Relation.TargetTableID,
		)
		if err != nil {
			return "", mapSchemaError(err)
		}
		current = target
	}
	targetField, found := current.Field(spec.TargetFieldID)
	if !found {
		return "", &query.ProductError{
			Code: "query.schema.invalid", Path: "fields.lookup.targetFieldId",
			Message: "lookup target field is unavailable",
		}
	}
	if resultMany {
		return query.FieldTypeJSON, nil
	}
	return queryFieldType(targetField)
}

func enumDescriptor(field v2.FieldDefinition) *query.EnumDescriptor {
	options := make([]query.EnumValueDescriptor, 0, len(field.Select.Options))
	for _, option := range field.Select.Options {
		if option.State != v2.OptionActive {
			continue
		}
		options = append(options, query.EnumValueDescriptor{
			Value: option.OptionID, StorageValue: option.OptionID,
		})
	}
	return &query.EnumDescriptor{
		Multiple: field.LogicalType == v2.LogicalMultiSelect,
		Options:  options,
	}
}

func queryFieldType(field v2.FieldDefinition) (query.FieldType, error) {
	logicalType := field.LogicalType
	if logicalType == v2.LogicalFormula {
		if field.Formula == nil {
			return unsupportedField(field)
		}
		logicalType = field.Formula.ResultType
	}
	switch logicalType {
	case v2.LogicalText, v2.LogicalEditor, v2.LogicalTime,
		v2.LogicalEmail, v2.LogicalURL, v2.LogicalSelect:
		return query.FieldTypeText, nil
	case v2.LogicalBool:
		return query.FieldTypeBool, nil
	case v2.LogicalNumber:
		return query.FieldTypeNumber, nil
	case v2.LogicalDate, v2.LogicalDateTime, v2.LogicalAutoDate:
		return query.FieldTypeDate, nil
	case v2.LogicalRelation:
		if field.Relation != nil && field.Relation.Cardinality == "many" {
			return query.FieldTypeMultiRelation, nil
		}
		return query.FieldTypeRelation, nil
	case v2.LogicalJSON, v2.LogicalGeoPoint, v2.LogicalFile,
		v2.LogicalMultiSelect, v2.LogicalLookup:
		return query.FieldTypeJSON, nil
	default:
		return unsupportedField(field)
	}
}

func unsupportedField(field v2.FieldDefinition) (query.FieldType, error) {
	return "", &query.ProductError{
		Code:    "query.schema.unsupported_field",
		Path:    "fields." + field.Identity.PhysicalName,
		Message: "field type is not queryable",
	}
}

func isSearchable(field v2.FieldDefinition) bool {
	logicalType := field.LogicalType
	if logicalType == v2.LogicalFormula && field.Formula != nil {
		logicalType = field.Formula.ResultType
	}
	switch logicalType {
	case v2.LogicalText, v2.LogicalEditor, v2.LogicalEmail,
		v2.LogicalURL, v2.LogicalSelect:
		return true
	default:
		return false
	}
}

func isComputed(field v2.FieldDefinition) bool {
	return field.LogicalType == v2.LogicalFormula || field.LogicalType == v2.LogicalLookup
}

func mapSchemaError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, schemaexecution.ErrTableNotFound) {
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
