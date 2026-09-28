package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type lookupDraftPreviewParams struct {
	TableID              string        `json:"tableId"`
	SchemaRevision       string        `json:"schemaRevision"`
	SourceSchemaRevision string        `json:"sourceSchemaRevision"`
	Lookup               v2.LookupSpec `json:"lookup"`
}

func decodeLookupDraftPreview(raw json.RawMessage) (lookupDraftPreviewParams, error) {
	var input lookupDraftPreviewParams
	if len(raw) > maxRelationRequestBytes {
		return input, errors.New("lookup draft exceeds request size limit")
	}
	if err := v2.StrictDecode(raw, &input); err != nil {
		return input, err
	}
	// Both established sources are previewable: an existing relation path or a
	// condition against another table. One of them must be fully specified.
	if input.TableID == "" || input.SchemaRevision == "" || input.SourceSchemaRevision == "" ||
		(len(input.Lookup.Path) == 0 && input.Lookup.Condition == nil) {
		return input, errors.New("lookup draft requires table revisions and a path or a condition")
	}
	return input, v2.ValidateLookupConditionShape(input.Lookup)
}

// preparePathDraftPreview resolves a path draft against the current schema and
// returns the terminal target table so callers can reject stale revisions.
func preparePathDraftPreview(
	ctx context.Context,
	tx core.App,
	current schemaexecution.Table,
	spec v2.LookupSpec,
) (schemaexecution.Table, error) {
	currentTable := current
	for _, step := range spec.Path {
		relation, found := currentTable.Field(step.RelationFieldID)
		if !found || relation.LogicalType != v2.LogicalRelation || relation.Relation == nil {
			return schemaexecution.Table{}, errors.New("lookup draft path relation is unavailable")
		}
		target, err := schemaexecution.Describe(ctx, tx, relation.Relation.TargetTableID)
		if err != nil {
			return schemaexecution.Table{}, err
		}
		currentTable = target
	}
	targetField, found := currentTable.Field(spec.TargetFieldID)
	if !found || targetField.LogicalType == v2.LogicalRelation {
		return schemaexecution.Table{}, errors.New("lookup draft target field is unavailable")
	}
	if v2.LookupAggregationRequiresNumericSource(v2.ResolvedLookupAggregation(spec)) &&
		!v2.LookupFieldTargetNumeric(targetField) {
		return schemaexecution.Table{}, errors.New("lookup draft numeric aggregation requires a number target field")
	}
	return currentTable, nil
}

func lookupDraftPreviewRegistration(app core.App) productrpc.Registration {
	return productrpc.Registration{
		Method: "lookup.draft.preview", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeLookupDraftPreview(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			input, err := decodeLookupDraftPreview(raw)
			if err != nil {
				return nil, err
			}
			var cell *lookup.CellValue
			err = app.RunInTransaction(func(tx core.App) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				current, err := schemaexecution.Describe(ctx, tx, input.TableID)
				if err != nil {
					return err
				}
				var sourceRevision string
				if input.Lookup.Condition != nil {
					plan, err := queryschema.PrepareLookupCondition(ctx, tx, current, input.Lookup)
					if err != nil {
						return err
					}
					sourceRevision = plan.Target.Snapshot.SchemaRevision
				} else {
					target, err := preparePathDraftPreview(ctx, tx, current, input.Lookup)
					if err != nil {
						return err
					}
					sourceRevision = target.Snapshot.SchemaRevision
				}
				if current.Snapshot.SchemaRevision != input.SchemaRevision || sourceRevision != input.SourceSchemaRevision {
					return errors.New("lookup draft schema revisions are stale")
				}
				collection, err := tx.FindCollectionByNameOrId(current.PhysicalName)
				if err != nil {
					return err
				}
				var records []*core.Record
				if err := tx.RecordQuery(collection).WithContext(ctx).OrderBy("id").Limit(1).All(&records); err != nil {
					return err
				}
				if len(records) == 0 {
					return nil
				}
				field := v2.FieldDefinition{LogicalType: v2.LogicalLookup, Lookup: &input.Lookup}
				projected, err := lookup.NewCalculator().CalculateFieldPage(ctx, tx, current, records[0], field, 0, 100)
				if err != nil {
					return err
				}
				cell = &projected
				return ctx.Err()
			})
			if err != nil {
				return nil, publicSchemaDescribeCatalogError(err)
			}
			return map[string]any{"cell": cell}, nil
		},
	}
}
