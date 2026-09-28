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
	if input.TableID == "" || input.SchemaRevision == "" || input.SourceSchemaRevision == "" || input.Lookup.Condition == nil {
		return input, errors.New("lookup draft requires table revisions and a condition")
	}
	return input, v2.ValidateLookupConditionShape(input.Lookup)
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
				plan, err := queryschema.PrepareLookupCondition(ctx, tx, current, input.Lookup)
				if err != nil {
					return err
				}
				if current.Snapshot.SchemaRevision != input.SchemaRevision || plan.Target.Snapshot.SchemaRevision != input.SourceSchemaRevision {
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
