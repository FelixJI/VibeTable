package schemaapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

const computationDependenciesCollection = "vibetable_computation_dependencies"

// replaceComputationDependencies projects Formula and Lookup dependencies into
// one graph owned by RelatedComputation. The graph is derived and replaced in
// the same schema transaction as the normalized definitions.
func (catalog *Catalog) replaceComputationDependencies(
	ctx context.Context,
	app core.App,
	definition schemaexecution.Table,
) error {
	if err := deleteMetadataByTable(
		app,
		computationDependenciesCollection,
		"source_table_id",
		definition.Snapshot.TableID,
	); err != nil {
		return err
	}
	collection, err := app.FindCollectionByNameOrId(computationDependenciesCollection)
	if err != nil {
		return storageError(err)
	}
	formulaDependencies, err := app.FindRecordsByFilter(
		formulaDepsCollection,
		"source_table_id={:table}",
		"+formula_field_id,+relation_field_id,+target_table_id,+target_field_id",
		0,
		0,
		dbx.Params{"table": definition.Snapshot.TableID},
	)
	if err != nil {
		return storageError(err)
	}
	fields := make(map[string]v2.FieldDefinition, len(definition.Snapshot.Fields))
	for _, field := range definition.Snapshot.Fields {
		fields[field.Identity.FieldID] = field
	}
	for _, dependency := range formulaDependencies {
		field := fields[dependency.GetString("formula_field_id")]
		if field.Formula == nil {
			continue
		}
		pathRaw, _ := json.Marshal([]map[string]string{{
			"relationFieldId": dependency.GetString("relation_field_id"),
		}})
		if err := saveComputationDependency(
			app, collection, definition.Snapshot.TableID, field.Identity.FieldID, "formula",
			dependency.GetString("relation_field_id"),
			dependency.GetString("target_table_id"),
			dependency.GetString("target_field_id"),
			pathRaw, definition.FormulaRuntime[field.Identity.FieldID].Version,
		); err != nil {
			return err
		}
	}
	for _, field := range definition.Snapshot.Fields {
		if field.Lookup == nil {
			continue
		}
		lookupRecord, findErr := app.FindFirstRecordByFilter(
			lookupsCollection,
			"table_id={:table} && field_id={:field}",
			dbx.Params{"table": definition.Snapshot.TableID, "field": field.Identity.FieldID},
		)
		if findErr != nil {
			return storageError(findErr)
		}
		path := field.Lookup.Path
		pathRaw, marshalErr := json.Marshal(path)
		if marshalErr != nil {
			return storageError(marshalErr)
		}
		current := definition
		for index, step := range path {
			relation, ok := schemaFieldByID(current, step.RelationFieldID)
			if !ok || relation.Relation == nil || relation.Relation.TargetTableID == "" {
				return &schemaerror.ProductError{
					Code: "schema.lookup.path_invalid", Path: "lookup.path",
					Message: "lookup dependency path is unavailable",
				}
			}
			targetFieldID := "__path__"
			if index == len(path)-1 {
				targetFieldID = field.Lookup.TargetFieldID
			}
			if err := saveComputationDependency(
				app, collection, definition.Snapshot.TableID, field.Identity.FieldID, "lookup",
				path[0].RelationFieldID, relation.Relation.TargetTableID,
				targetFieldID, pathRaw, max(lookupRecord.GetInt("revision"), 1),
			); err != nil {
				return err
			}
			if index < len(path)-1 {
				next, describeErr := catalog.Describe(ctx, relation.Relation.TargetTableID)
				if describeErr != nil {
					return describeErr
				}
				current = next
			}
		}
	}
	return nil
}

func saveComputationDependency(
	app core.App,
	collection *core.Collection,
	sourceTableID string,
	computedFieldID string,
	computedKind string,
	relationFieldID string,
	targetTableID string,
	targetFieldID string,
	pathRaw []byte,
	definitionVersion int,
) error {
	record := core.NewRecord(collection)
	record.Set("source_table_id", sourceTableID)
	record.Set("computed_field_id", computedFieldID)
	record.Set("computed_kind", computedKind)
	record.Set("relation_field_id", relationFieldID)
	record.Set("target_table_id", targetTableID)
	record.Set("target_field_id", targetFieldID)
	record.Set("path_json", types.JSONRaw(pathRaw))
	record.Set("definition_version", definitionVersion)
	if err := app.Save(record); err != nil {
		return storageError(fmt.Errorf("save computation dependency: %w", err))
	}
	return nil
}

// RebuildComputationDependencies restores the derived graph from authoritative
// Formula and Lookup metadata. Callers must supply an app in a schema transaction.
func (catalog *Catalog) RebuildComputationDependencies(ctx context.Context) error {
	definitions, err := catalog.List(ctx)
	if err != nil {
		return err
	}
	for _, definition := range definitions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := catalog.replaceComputationDependencies(ctx, catalog.app, definition); err != nil {
			return err
		}
	}
	return nil
}
