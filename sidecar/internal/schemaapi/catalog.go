package schemaapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
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

type DeleteResult struct {
	Deleted bool   `json:"deleted"`
	TableID string `json:"tableId"`
}

type SchemaCatalog interface {
	List(ctx context.Context) ([]schemaexecution.Table, error)
	Describe(ctx context.Context, tableID string) (schemaexecution.Table, error)
	InspectAutoDates(ctx context.Context) ([]AutoDateDiagnostic, error)
	DeleteTable(ctx context.Context, tableID string, expectedRevision int64) (DeleteResult, error)
	GetRevision(ctx context.Context, tableID string) (int64, error)
	GetDataRevision(ctx context.Context, tableID string) (int64, error)
}

type AutoDateDiagnostic struct {
	TableID       string `json:"tableId"`
	FieldID       string `json:"fieldId"`
	PhysicalName  string `json:"physicalName"`
	DisplayName   string `json:"displayName"`
	OnCreate      bool   `json:"onCreate"`
	OnUpdate      bool   `json:"onUpdate"`
	DeclaredRole  string `json:"declaredRole,omitempty"`
	SuggestedRole string `json:"suggestedRole,omitempty"`
	Status        string `json:"status"`
}

type Catalog struct {
	app core.App
}

func New(app core.App) *Catalog {
	return &Catalog{app: app}
}

func (catalog *Catalog) List(ctx context.Context) ([]schemaexecution.Table, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := catalog.app.FindAllRecords(tablesCollection)
	if err != nil {
		return nil, storageError(err)
	}
	definitions := make([]schemaexecution.Table, 0, len(records))
	for _, record := range records {
		definition, decodeErr := catalog.describe(ctx, catalog.app, record.GetString("table_id"))
		if decodeErr != nil {
			return nil, decodeErr
		}
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool {
		if definitions[i].PhysicalName == definitions[j].PhysicalName {
			return definitions[i].Snapshot.TableID < definitions[j].Snapshot.TableID
		}
		return definitions[i].PhysicalName < definitions[j].PhysicalName
	})
	return definitions, nil
}

func (catalog *Catalog) Describe(
	ctx context.Context,
	tableID string,
) (schemaexecution.Table, error) {
	return catalog.describe(ctx, catalog.app, tableID)
}

func (catalog *Catalog) describe(
	ctx context.Context,
	app core.App,
	tableID string,
) (schemaexecution.Table, error) {
	if err := ctx.Err(); err != nil {
		return schemaexecution.Table{}, err
	}
	record, err := catalog.findTable(app, tableID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return schemaexecution.Table{}, &schemaerror.ProductError{
				Code: "schema.table.not_found", Path: "tableId", Message: "table was not found",
			}
		}
		return schemaexecution.Table{}, storageError(err)
	}
	storedSchemaRevision, err := parseStoredRevision(
		record.GetRaw("schema_revision"),
		"schema.metadata.invalid_schema_revision",
		"schemaRevision",
	)
	if err != nil {
		return schemaexecution.Table{}, err
	}
	storedDataRevision, err := parseStoredRevision(
		record.GetRaw("data_revision"),
		"schema.metadata.invalid_data_revision",
		"dataRevision",
	)
	if err != nil {
		return schemaexecution.Table{}, err
	}
	definition, err := schemaexecution.Describe(ctx, app, tableID)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return schemaexecution.Table{}, err
	}
	if err == nil {
		definitionRevision, parseErr := v2.ParseSchemaRevision(definition.Snapshot.SchemaRevision)
		if parseErr != nil || definitionRevision != storedSchemaRevision ||
			definition.Snapshot.DataRevision != storedDataRevision {
			return schemaexecution.Table{}, &schemaerror.ProductError{
				Code: "schema.metadata.revision_mismatch", Path: "schemaRevision",
				Message: "stored schema revision metadata is inconsistent",
			}
		}
		return definition, nil
	}
	if errors.Is(err, schemaexecution.ErrTableNotFound) {
		return schemaexecution.Table{}, &schemaerror.ProductError{
			Code: "schema.table.not_found", Path: "tableId", Message: "table was not found",
		}
	}
	return schemaexecution.Table{}, storageError(err)
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
		definition, decodeErr := catalog.describe(ctx, catalog.app, record.GetString("table_id"))
		if decodeErr != nil {
			return nil, decodeErr
		}
		collection, findErr := catalog.app.FindCollectionByNameOrId(
			record.GetString("collection_id"),
		)
		if findErr != nil {
			return nil, storageError(findErr)
		}
		defined := make(map[string]v2.FieldDefinition)
		for _, field := range definition.Snapshot.Fields {
			if field.LogicalType == v2.LogicalAutoDate {
				defined[field.Identity.PhysicalName] = field
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
				TableID:      definition.Snapshot.TableID,
				FieldID:      pbField.GetId(),
				PhysicalName: pbField.GetName(),
				DisplayName:  pbField.GetName(),
			}
			if tracked {
				seen[field.Identity.PhysicalName] = struct{}{}
				diagnostic.FieldID = field.Identity.FieldID
				diagnostic.DisplayName = field.DisplayName
			}
			if tracked && field.AutoDate != nil {
				diagnostic.DeclaredRole = field.AutoDate.Role
			}
			diagnostic.OnCreate = pbField.OnCreate
			diagnostic.OnUpdate = pbField.OnUpdate
			switch {
			case pbField.OnCreate && !pbField.OnUpdate:
				diagnostic.SuggestedRole = "createdAt"
			case pbField.OnCreate && pbField.OnUpdate:
				diagnostic.SuggestedRole = "updatedAt"
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
			declaredRole := ""
			if field.AutoDate != nil {
				declaredRole = field.AutoDate.Role
			}
			result = append(result, AutoDateDiagnostic{
				TableID: definition.Snapshot.TableID, FieldID: field.Identity.FieldID,
				PhysicalName: field.Identity.PhysicalName, DisplayName: field.DisplayName,
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

func schemaFieldByID(
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

func (catalog *Catalog) validateFormulaReferences(
	ctx context.Context,
	definition schemaexecution.Table,
) error {
	plan, formulaErr := formula.NewCompiler(
		formula.DefaultLimits(),
	).CompileExecutionTable(definition)
	if formulaErr != nil {
		return formulaErr
	}
	fieldsByName := make(map[string]v2.FieldDefinition, len(definition.Snapshot.Fields))
	for _, field := range definition.Snapshot.Fields {
		fieldsByName[field.Identity.PhysicalName] = field
	}
	for _, compiled := range plan.Formulas {
		for _, reference := range compiled.ReferencePaths {
			if err := catalog.validateFormulaReference(
				ctx, definition, fieldsByName, compiled.FieldID, reference, false,
			); err != nil {
				return err
			}
		}
		for _, reference := range compiled.RelationAggregatePaths {
			if err := catalog.validateFormulaReference(
				ctx, definition, fieldsByName, compiled.FieldID, reference, true,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func (catalog *Catalog) validateFormulaReference(
	ctx context.Context,
	definition schemaexecution.Table,
	fieldsByName map[string]v2.FieldDefinition,
	formulaFieldID string,
	reference string,
	aggregate bool,
) error {
	parts := strings.Split(reference, ".")
	if len(parts) != 2 {
		return &schemaerror.ProductError{
			Code:    "schema.formula.relation_path_invalid",
			Path:    formulaPath(definition, formulaFieldID),
			Message: "relation formula paths must select one target field",
			Details: map[string]any{"reference": reference},
		}
	}
	root, exists := fieldsByName[parts[0]]
	if !exists || root.LogicalType != v2.LogicalRelation || root.Relation == nil {
		return nil
	}
	if !aggregate && root.Relation.Cardinality != "one" {
		return &schemaerror.ProductError{
			Code:    "schema.formula.relation_cardinality",
			Path:    formulaPath(definition, formulaFieldID),
			Message: "many relations require a relation aggregate formula function",
			Details: map[string]any{"reference": reference},
		}
	}
	target := definition
	if root.Relation.TargetTableID != definition.Snapshot.TableID {
		var err error
		target, err = catalog.Describe(ctx, root.Relation.TargetTableID)
		if err != nil {
			return &schemaerror.ProductError{
				Code:    "schema.formula.target_table_not_found",
				Path:    formulaPath(definition, formulaFieldID),
				Message: "formula relation target table was not found",
				Details: map[string]any{"reference": reference},
			}
		}
	}
	for _, targetField := range target.Snapshot.Fields {
		if targetField.Identity.PhysicalName == parts[1] &&
			targetField.LogicalType != v2.LogicalRelation {
			return nil
		}
	}
	return &schemaerror.ProductError{
		Code:    "schema.formula.target_field_not_found",
		Path:    formulaPath(definition, formulaFieldID),
		Message: "formula relation target field was not found",
		Details: map[string]any{"reference": reference},
	}
}

func formulaPath(
	definition schemaexecution.Table,
	fieldID string,
) string {
	for index, field := range definition.Snapshot.Fields {
		if field.Identity.FieldID == fieldID {
			return fmt.Sprintf(
				"definition.fields[%d].formula.source", index,
			)
		}
	}
	return "definition.fields.formula.source"
}

func (catalog *Catalog) validateLookupReferences(
	ctx context.Context,
	definition schemaexecution.Table,
) error {
	for index, field := range definition.Snapshot.Fields {
		if field.LogicalType != v2.LogicalLookup || field.Lookup == nil {
			continue
		}
		prefix := fmt.Sprintf("definition.fields[%d].lookup", index)
		path := field.Lookup.Path
		current := definition
		var relation v2.FieldDefinition
		var target schemaexecution.Table
		for pathIndex, step := range path {
			var exists bool
			relation, exists = schemaFieldByID(current, step.RelationFieldID)
			if !exists || relation.LogicalType != v2.LogicalRelation ||
				relation.Relation == nil {
				return &schemaerror.ProductError{
					Code: "schema.lookup.relation_not_found",
					Path: fmt.Sprintf(
						"%s.path[%d].relationFieldId", prefix, pathIndex,
					),
					Message: "lookup path relation field was not found",
				}
			}
			targetTableID := relation.Relation.TargetTableID
			var describeErr error
			if targetTableID == definition.Snapshot.TableID {
				target = definition
			} else {
				target, describeErr = catalog.Describe(ctx, targetTableID)
				if describeErr != nil {
					return describeErr
				}
			}
			current = target
		}
		var targetField *v2.FieldDefinition
		for targetIndex := range target.Snapshot.Fields {
			if target.Snapshot.Fields[targetIndex].Identity.FieldID == field.Lookup.TargetFieldID {
				targetField = &target.Snapshot.Fields[targetIndex]
				break
			}
		}
		if targetField == nil {
			return &schemaerror.ProductError{
				Code:    "schema.lookup.target_field_not_found",
				Path:    prefix + ".targetFieldId",
				Message: "lookup target field was not found",
			}
		}
	}
	return nil
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
		return DeleteResult{}, &schemaerror.ProductError{
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
				return &schemaerror.ProductError{
					Code: "schema.table.not_found", Path: "tableId",
					Message: "table was not found",
				}
			}
			return storageError(findErr)
		}
		definition, decodeErr := catalog.describe(ctx, txApp, tableID)
		if decodeErr != nil {
			return decodeErr
		}
		currentRevision, parseErr := v2.ParseSchemaRevision(definition.Snapshot.SchemaRevision)
		if parseErr != nil {
			return storageError(parseErr)
		}
		if currentRevision != expectedRevision {
			return &schemaerror.ProductError{
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
		definition, decodeErr := catalog.describe(context.Background(), app, record.GetString("table_id"))
		if decodeErr != nil {
			return decodeErr
		}
		if definition.Snapshot.TableID == tableID {
			continue
		}
		if definition.Kind == "view" &&
			viewSourceTableID(record) == tableID {
			return &schemaerror.ProductError{
				Code: "schema.table.referenced", Path: "tableId",
				Message: "table is referenced by another table",
				Details: map[string]any{
					"sourceTableId": definition.Snapshot.TableID,
					"referenceKind": "view",
				},
			}
		}
		for _, field := range definition.Snapshot.Fields {
			if field.Relation == nil {
				continue
			}
			relation := field.Relation
			references := relation.TargetTableID == tableID
			if references {
				return &schemaerror.ProductError{
					Code: "schema.table.referenced", Path: "tableId",
					Message: "table is referenced by another table",
					Details: map[string]any{
						"sourceTableId": definition.Snapshot.TableID,
						"sourceFieldId": field.Identity.FieldID,
					},
				}
			}
		}
	}
	return nil
}

func viewSourceTableID(record *core.Record) string {
	raw, err := json.Marshal(record.GetRaw("view_v2_json"))
	if err != nil || string(raw) == "null" {
		return ""
	}
	var view struct {
		SourceTableID string `json:"sourceTableId"`
	}
	if json.Unmarshal(raw, &view) != nil {
		return ""
	}
	return view.SourceTableID
}

func (catalog *Catalog) replaceFormulaDependencyMetadata(
	app core.App,
	definition schemaexecution.Table,
	plan *formula.Plan,
) error {
	if err := deleteMetadataByTable(
		app, formulaDepsCollection, "source_table_id", definition.Snapshot.TableID,
	); err != nil {
		return err
	}
	collection, err := app.FindCollectionByNameOrId(formulaDepsCollection)
	if err != nil {
		return storageError(err)
	}
	fieldsByName := make(map[string]v2.FieldDefinition, len(definition.Snapshot.Fields))
	targetCache := map[string]schemaexecution.Table{
		definition.Snapshot.TableID: definition,
	}
	for _, field := range definition.Snapshot.Fields {
		fieldsByName[field.Identity.PhysicalName] = field
	}
	for _, lookupField := range definition.Snapshot.Fields {
		if lookupField.LogicalType != v2.LogicalLookup ||
			lookupField.Lookup == nil {
			continue
		}
		current := definition
		path := lookupField.Lookup.Path
		pathRelations := make([]v2.FieldDefinition, 0, len(path))
		for _, step := range path {
			relationField, exists := schemaFieldByID(current, step.RelationFieldID)
			if !exists || relationField.Relation == nil {
				return &schemaerror.ProductError{
					Code:    "schema.lookup.relation_not_found",
					Path:    "definition.fields.lookup.path",
					Message: "lookup path relation field was not found",
				}
			}
			pathRelations = append(pathRelations, relationField)
			targetTableID := relationField.Relation.TargetTableID
			target := targetCache[targetTableID]
			if target.Snapshot.TableID == "" {
				var describeErr error
				target, describeErr = New(app).Describe(
					context.Background(), targetTableID,
				)
				if describeErr != nil {
					return describeErr
				}
				targetCache[target.Snapshot.TableID] = target
			}
			current = target
		}
		relationField := pathRelations[len(pathRelations)-1]
		rootRelationField := pathRelations[0]
		saveDependency := func(targetTableID, targetFieldID string) error {
			record := core.NewRecord(collection)
			record.Set("source_table_id", definition.Snapshot.TableID)
			record.Set("formula_field_id", lookupField.Identity.FieldID)
			record.Set("relation_field_id", rootRelationField.Identity.FieldID)
			record.Set("target_table_id", targetTableID)
			record.Set("target_field_id", targetFieldID)
			record.Set("dependency_kind", "lookup")
			if err := app.Save(record); err != nil {
				return storageError(err)
			}
			return nil
		}
		targetTableID := relationField.Relation.TargetTableID
		if err := saveDependency(
			targetTableID,
			lookupField.Lookup.TargetFieldID,
		); err != nil {
			return err
		}
	}
	for _, compiled := range plan.Formulas {
		references := append(
			append([]string(nil), compiled.ReferencePaths...),
			compiled.RelationAggregatePaths...,
		)
		seenReferences := map[string]struct{}{}
		for _, reference := range references {
			if _, duplicate := seenReferences[reference]; duplicate {
				continue
			}
			seenReferences[reference] = struct{}{}
			parts := strings.Split(reference, ".")
			relationField, exists := fieldsByName[parts[0]]
			if !exists || relationField.LogicalType != v2.LogicalRelation ||
				relationField.Relation == nil || len(parts) != 2 {
				continue
			}
			target := targetCache[relationField.Relation.TargetTableID]
			if target.Snapshot.TableID == "" {
				target, err = New(app).Describe(
					context.Background(),
					relationField.Relation.TargetTableID,
				)
				if err != nil {
					return err
				}
				targetCache[target.Snapshot.TableID] = target
			}
			targetFieldID := ""
			for _, targetField := range target.Snapshot.Fields {
				if targetField.Identity.PhysicalName == parts[1] {
					targetFieldID = targetField.Identity.FieldID
					break
				}
			}
			record := core.NewRecord(collection)
			record.Set("source_table_id", definition.Snapshot.TableID)
			record.Set("formula_field_id", compiled.FieldID)
			record.Set("relation_field_id", relationField.Identity.FieldID)
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

// SyncComputedMetadata is the shared transactional seam for schemaapi and the
// v2 FieldChange executor. It compiles the whole table before replacing any
// Formula/Lookup metadata, so dependency fan-out never observes a partial plan.
func (catalog *Catalog) SyncComputedMetadata(
	ctx context.Context,
	definition schemaexecution.Table,
) error {
	if err := catalog.validateFormulaReferences(ctx, definition); err != nil {
		return err
	}
	if err := catalog.validateLookupReferences(ctx, definition); err != nil {
		return err
	}
	prepared, plan, formulaErr := catalog.prepareFormulaState(
		catalog.app,
		definition,
	)
	if formulaErr != nil {
		return formulaErr
	}
	if err := catalog.replaceFormulaMetadata(catalog.app, prepared, plan); err != nil {
		return err
	}
	if err := catalog.replaceFormulaDependencyMetadata(
		catalog.app, prepared, plan,
	); err != nil {
		return err
	}
	if err := catalog.replaceLookupMetadata(catalog.app, prepared); err != nil {
		return err
	}
	return catalog.replaceComputationDependencies(ctx, catalog.app, prepared)
}

// SyncComputedMetadataForTable hides the derived execution projection from
// Schema V2 writers. Callers provide only the stable table identity; this
// module derives its private runtime plan from authoritative V2 rows.
func (catalog *Catalog) SyncComputedMetadataForTable(
	ctx context.Context,
	tableID string,
) error {
	definition, err := catalog.Describe(ctx, tableID)
	if err != nil {
		return err
	}
	return catalog.SyncComputedMetadata(ctx, definition)
}

func (catalog *Catalog) GetRevision(
	ctx context.Context,
	tableID string,
) (int64, error) {
	definition, err := catalog.Describe(ctx, tableID)
	if err != nil {
		return 0, err
	}
	return v2.ParseSchemaRevision(definition.Snapshot.SchemaRevision)
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
			return 0, &schemaerror.ProductError{
				Code: "schema.table.not_found", Path: "tableId",
				Message: "table was not found",
			}
		}
		return 0, storageError(err)
	}
	return parseStoredRevision(
		record.GetRaw("data_revision"),
		"schema.metadata.invalid_data_revision",
		"dataRevision",
	)
}

func (catalog *Catalog) prepareFormulaState(
	app core.App,
	definition schemaexecution.Table,
) (schemaexecution.Table, *formula.Plan, error) {
	plan, formulaErr := formula.NewCompiler(formula.DefaultLimits()).CompileExecutionTable(definition)
	if formulaErr != nil {
		return schemaexecution.Table{}, nil, formulaErr
	}
	existing, err := app.FindRecordsByFilter(
		formulasCollection,
		"table_id={:table}",
		"",
		0,
		0,
		dbx.Params{"table": definition.Snapshot.TableID},
	)
	if err != nil {
		return schemaexecution.Table{}, nil, storageError(err)
	}
	existingByFieldID := make(map[string]*core.Record, len(existing))
	for _, record := range existing {
		existingByFieldID[record.GetString("field_id")] = record
	}
	compiledByFieldID := make(map[string]*formula.CompiledFormula, len(plan.Formulas))
	for _, compiled := range plan.Formulas {
		compiledByFieldID[compiled.FieldID] = compiled
	}
	preparedRuntime := make(map[string]schemaexecution.FormulaRuntime, len(definition.FormulaRuntime))
	for fieldID, runtime := range definition.FormulaRuntime {
		preparedRuntime[fieldID] = runtime
	}
	for index := range definition.Snapshot.Fields {
		field := &definition.Snapshot.Fields[index]
		if field.LogicalType != v2.LogicalFormula || field.Formula == nil {
			continue
		}
		compiled := compiledByFieldID[field.Identity.FieldID]
		record := existingByFieldID[field.Identity.FieldID]
		if record == nil {
			preparedRuntime[field.Identity.FieldID] = schemaexecution.FormulaRuntime{
				Version: 1, Status: "backfilling",
			}
			continue
		}
		version := record.GetInt("version")
		if version < 1 {
			version = 1
		}
		resultType := string(field.Formula.ResultType)
		unchanged := record.GetString("source") == field.Formula.Source &&
			record.GetString("language") == field.Formula.Language &&
			record.GetString("result_type") == string(resultType) &&
			record.GetString("ast_hash") == compiled.ASTHash
		if unchanged {
			status := record.GetString("status")
			if status == "" {
				status = "backfilling"
			}
			preparedRuntime[field.Identity.FieldID] = schemaexecution.FormulaRuntime{
				Version: version, Status: status,
			}
			continue
		}
		preparedRuntime[field.Identity.FieldID] = schemaexecution.FormulaRuntime{
			Version: version + 1, Status: "backfilling",
		}
	}
	definition.FormulaRuntime = preparedRuntime
	return definition, plan, nil
}

func (catalog *Catalog) replaceFormulaMetadata(
	app core.App,
	definition schemaexecution.Table,
	plan *formula.Plan,
) error {
	records, err := app.FindRecordsByFilter(
		formulasCollection,
		"table_id={:table}",
		"",
		0,
		0,
		dbx.Params{"table": definition.Snapshot.TableID},
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
	fieldsByID := make(map[string]v2.FieldDefinition, len(definition.Snapshot.Fields))
	for _, field := range definition.Snapshot.Fields {
		fieldsByID[field.Identity.FieldID] = field
	}
	for _, compiled := range plan.Formulas {
		field := fieldsByID[compiled.FieldID]
		resultType := string(field.Formula.ResultType)
		dependencies, marshalErr := json.Marshal(compiled.Dependencies)
		if marshalErr != nil {
			return storageError(marshalErr)
		}
		record := core.NewRecord(collection)
		record.Set("table_id", definition.Snapshot.TableID)
		record.Set("field_id", compiled.FieldID)
		record.Set("source", compiled.Source)
		record.Set("language", field.Formula.Language)
		record.Set("result_type", string(resultType))
		record.Set("ast_hash", compiled.ASTHash)
		record.Set("dependencies_json", types.JSONRaw(dependencies))
		runtime := definition.FormulaRuntime[field.Identity.FieldID]
		record.Set("version", runtime.Version)
		record.Set("status", runtime.Status)
		if err := app.Save(record); err != nil {
			return storageError(err)
		}
	}
	return nil
}

func (catalog *Catalog) replaceLookupMetadata(
	app core.App,
	definition schemaexecution.Table,
) error {
	existing, err := app.FindRecordsByFilter(
		lookupsCollection,
		"table_id={:table}",
		"",
		0,
		0,
		dbx.Params{"table": definition.Snapshot.TableID},
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
	for _, field := range definition.Snapshot.Fields {
		if field.LogicalType != v2.LogicalLookup || field.Lookup == nil {
			continue
		}
		pathRaw, marshalErr := json.Marshal(map[string]any{
			"relationFieldId": field.Lookup.Path[0].RelationFieldID,
			"path":            field.Lookup.Path,
			"targetFieldId":   field.Lookup.TargetFieldID,
			"physicalName":    field.Identity.PhysicalName,
			"displayName":     field.DisplayName,
			"outputType":      v2.LogicalJSON,
		})
		if marshalErr != nil {
			return storageError(marshalErr)
		}
		lookupID := definition.Snapshot.TableID + "." + field.Identity.FieldID
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
		record.Set("table_id", definition.Snapshot.TableID)
		record.Set("field_id", field.Identity.FieldID)
		record.Set("relation_field_id", field.Lookup.Path[0].RelationFieldID)
		record.Set("target_field_id", field.Lookup.TargetFieldID)
		record.Set("path_json", types.JSONRaw(pathRaw))
		record.Set("output_type", string(v2.LogicalJSON))
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

func storageError(err error) *schemaerror.ProductError {
	productErr := &schemaerror.ProductError{
		Code: "schema.storage.failed", Path: "", Message: "schema storage operation failed",
	}
	return schemaerror.WithCause(productErr, err)
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

func invalidStoredRevision(code, path string) *schemaerror.ProductError {
	return &schemaerror.ProductError{
		Code: code, Path: path,
		Message: "stored revision must be a present non-negative safe integer",
	}
}
