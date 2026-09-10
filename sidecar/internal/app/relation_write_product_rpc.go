package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
)

func relationCreateTargetRegistration(port interface {
	CreateTarget(context.Context, relation.CreateTargetRequest) (relation.CreateTargetResult, error)
}, gates ...businessWriteGate) productrpc.Registration {
	return productrpc.Registration{Method: "relation.createTarget", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeRelationWriteParams(raw, false); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			object, err := decodeRelationWriteParams(raw, false)
			if err != nil {
				return nil, err
			}
			id, err := relationPreviewText(object, "idempotencyKey")
			if err != nil {
				return nil, err
			}
			relationID, err := relationPreviewText(object, "relationId")
			if err != nil {
				return nil, err
			}
			body := map[string]any{"relationId": relationID, "requestId": id, "idempotencyKey": id, "actor": relationProductActor(), "label": "", "values": map[string]any{}}
			for _, key := range []string{"label", "values"} {
				if value, ok := object[key]; ok {
					body[key] = value
				}
			}
			var input relation.CreateTargetRequest
			if err = decodeRelationTranslated(body, &input); err != nil {
				return nil, err
			}
			input.CallerParams, err = json.Marshal(object)
			if err != nil {
				return nil, err
			}
			var result relation.CreateTargetResult
			err = runBusinessWrite(ctx, gates, "relation.create-target", id, func(writeCtx context.Context) error {
				if err := writeCtx.Err(); err != nil {
					return err
				}
				var err error
				result, err = port.CreateTarget(writeCtx, input)
				return err
			})
			if err != nil {
				return nil, publicSchemaDescribeCatalogError(err)
			}
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			if !relationReceiptCommitted(result.Receipt) {
				return nil, publicSchemaDescribeCatalogError(relationRequestError("relation target creation has not committed"))
			}
			target, err := rendererRelationTarget(result.Target)
			if err != nil {
				return nil, err
			}
			return map[string]any{"outcome": "committed", "target": target, "requestId": id}, nil
		},
	}
}

func relationApplyDeltaRegistration(port interface {
	ApplyDelta(context.Context, relation.DeltaRequest) (relation.DeltaResult, error)
}, gates ...businessWriteGate) productrpc.Registration {
	return productrpc.Registration{Method: "relation.applyDelta", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeRelationPreviewParams(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			object, err := decodeRelationPreviewParams(raw)
			if err != nil {
				return nil, err
			}
			body, err := translateRelationPreview(object)
			if err != nil {
				return nil, err
			}
			var input relation.DeltaRequest
			if err = decodeRelationTranslated(body, &input); err != nil {
				return nil, err
			}
			input.CallerParams, err = json.Marshal(object)
			if err != nil {
				return nil, err
			}
			var result relation.DeltaResult
			err = runBusinessWrite(ctx, gates, "relation.apply-delta", input.IdempotencyKey, func(writeCtx context.Context) error {
				if err := writeCtx.Err(); err != nil {
					return err
				}
				var err error
				result, err = port.ApplyDelta(writeCtx, input)
				return err
			})
			if err != nil {
				return nil, publicSchemaDescribeCatalogError(err)
			}
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			if !relationReceiptCommitted(result.Receipt) || result.Current == nil {
				return nil, publicSchemaDescribeCatalogError(relationRequestError("relation mutation has not committed"))
			}
			current := make([]any, 0, len(result.Current))
			for _, item := range result.Current {
				target, err := rendererRelationTarget(item)
				if err != nil {
					return nil, err
				}
				current = append(current, target)
			}
			return map[string]any{"outcome": "committed", "current": current, "schemaRevision": input.SchemaRevision, "requestId": input.IdempotencyKey}, nil
		},
	}
}

func relationUpdateSingleRegistration(port interface {
	UpdateSingle(context.Context, relation.SingleRequest) (mutation.Receipt, error)
}, gates ...businessWriteGate) productrpc.Registration {
	return productrpc.Registration{Method: "relation.updateSingle", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeRelationWriteParams(raw, true); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			object, err := decodeRelationWriteParams(raw, true)
			if err != nil {
				return nil, err
			}
			// Reuse the existing target translation; the public response still echoes
			// the original scalar target, including null and its renderer shape.
			adds := []any{}
			if object["target"] != nil {
				adds = append(adds, object["target"])
			}
			translated := map[string]any{"relationId": object["relationId"], "sourceItemId": object["sourceItemId"], "expectedSchemaRevision": object["expectedSchemaRevision"], "idempotencyKey": object["idempotencyKey"], "adds": adds, "removes": []any{}}
			body, err := translateRelationPreview(translated)
			if err != nil {
				return nil, err
			}
			var delta relation.DeltaRequest
			if err = decodeRelationTranslated(body, &delta); err != nil {
				return nil, err
			}
			input := relation.SingleRequest{RelationID: delta.RelationID, SourceRecordID: delta.SourceRecordID, SchemaRevision: delta.SchemaRevision, RequestID: delta.RequestID, IdempotencyKey: delta.IdempotencyKey, Actor: delta.Actor}
			if len(delta.Adds) != 0 {
				input.Target = &delta.Adds[0]
			}
			// Preserve validated caller fields omitted by the execution translation,
			// including renderer extras and the opaque expectedDateUpdated value.
			input.CallerParams, err = json.Marshal(object)
			if err != nil {
				return nil, err
			}
			var receipt mutation.Receipt
			err = runBusinessWrite(ctx, gates, "relation.update-single", input.IdempotencyKey, func(writeCtx context.Context) error {
				if err := writeCtx.Err(); err != nil {
					return err
				}
				var err error
				receipt, err = port.UpdateSingle(writeCtx, input)
				return err
			})
			if err != nil {
				return nil, publicSchemaDescribeCatalogError(err)
			}
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			if !relationReceiptCommitted(receipt) {
				return nil, publicSchemaDescribeCatalogError(relationRequestError("single relation update has not committed"))
			}
			return map[string]any{"outcome": "committed", "current": object["target"], "schemaRevision": input.SchemaRevision, "requestId": input.RequestID, "receipt": receipt}, nil
		},
	}
}

func relationProductActor() map[string]any {
	return map[string]any{"type": "user", "id": "local-user", "displayName": nil}
}
func relationReceiptCommitted(receipt mutation.Receipt) bool {
	return receipt.Status == mutation.StatusApplied || receipt.Status == mutation.StatusReplayed
}

func decodeRelationTranslated(body map[string]any, target any) error {
	var compact strings.Builder
	if err := appendDescribeRevision(&compact, body); err != nil {
		return err
	}
	if compact.Len() > maxRelationRequestBytes || mutation.DecodeStrict([]byte(compact.String()), target) != nil {
		return publicSchemaDescribeCatalogError(relationRequestError("relation request body is invalid"))
	}
	return nil
}

func rendererRelationTarget(target relation.TargetRef) (map[string]any, error) {
	if target.TableID == "" || target.RecordID == "" || target.Label == "" {
		return nil, errors.New("PocketBase returned an invalid relation target")
	}
	var secondary any
	if target.SecondaryLabel != "" {
		secondary = target.SecondaryLabel
	}
	return map[string]any{"collection": target.TableID, "itemId": target.RecordID, "label": target.Label, "secondaryLabel": secondary}, nil
}

func decodeRelationWriteParams(raw json.RawMessage, single bool) (map[string]any, error) {
	object, err := decodeRelationProductObject(raw)
	if err != nil {
		return nil, err
	}
	required := []string{"relationId", "idempotencyKey"}
	if single {
		required = append(required, "sourceItemId", "target", "expectedSchemaRevision")
	}
	for _, key := range required {
		if _, ok := object[key]; !ok {
			return nil, errors.New("missing relation parameter")
		}
	}
	for key, value := range object {
		switch key {
		case "relationId", "idempotencyKey":
			if text, ok := value.(string); !ok || text == "" {
				return nil, errors.New("relation identifier must be text")
			}
		case "label":
			if single {
				return nil, errors.New("unknown relation parameter")
			}
			if text, ok := value.(string); !ok || text == "" {
				return nil, errors.New("label must be text")
			}
		case "values":
			if single {
				return nil, errors.New("unknown relation parameter")
			}
			if _, ok := value.(map[string]any); !ok {
				return nil, errors.New("values must be an object")
			}
		case "sourceItemId", "expectedSchemaRevision":
			if !single {
				return nil, errors.New("unknown relation parameter")
			}
			if text, ok := value.(string); !ok || text == "" {
				return nil, errors.New("relation identifier must be text")
			}
		case "expectedDateUpdated":
			if !single {
				return nil, errors.New("unknown relation parameter")
			}
			if value != nil {
				if text, ok := value.(string); !ok || text == "" {
					return nil, errors.New("expectedDateUpdated must be text or null")
				}
			}
		case "target":
			if !single {
				return nil, errors.New("unknown relation parameter")
			}
			if value != nil {
				if _, ok := value.(map[string]any); !ok {
					return nil, errors.New("target must be an object or null")
				}
			}
		default:
			return nil, errors.New("unknown relation parameter")
		}
	}
	return object, nil
}
