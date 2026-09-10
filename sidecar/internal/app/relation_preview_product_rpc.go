package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
)

func relationPreviewDeltaRegistration(port interface {
	PreviewDelta(context.Context, relation.DeltaRequest) (relation.DeltaPreview, error)
}) productrpc.Registration {
	return productrpc.Registration{
		Method: "relation.previewDelta", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			_, err := decodeRelationPreviewParams(raw)
			return err
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			original, err := decodeRelationPreviewParams(raw)
			if err != nil {
				return nil, err
			}
			translated, err := translateRelationPreview(original)
			if err != nil {
				return nil, err
			}
			var body strings.Builder
			if err := appendDescribeRevision(&body, translated); err != nil {
				return nil, err
			}
			var input relation.DeltaRequest
			if body.Len() > maxRelationRequestBytes || mutation.DecodeStrict([]byte(body.String()), &input) != nil {
				return nil, publicSchemaDescribeCatalogError(relationRequestError("relation request body is invalid"))
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			result, err := port.PreviewDelta(ctx, input)
			if err != nil {
				return nil, publicSchemaDescribeCatalogError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if result.Current == nil {
				return nil, errors.New("PocketBase returned invalid relation preview")
			}
			current := make([]any, 0, len(result.Current))
			for _, target := range result.Current {
				if target.TableID == "" || target.RecordID == "" || target.Label == "" {
					return nil, errors.New("PocketBase returned an invalid relation preview target")
				}
				var secondary any
				if target.SecondaryLabel != "" {
					secondary = target.SecondaryLabel
				}
				current = append(current, map[string]any{
					"collection": target.TableID, "itemId": target.RecordID,
					"label": target.Label, "secondaryLabel": secondary,
				})
			}
			return map[string]any{
				"delta": original, "current": current, "diagnostics": []any{}, "canApply": result.CanApply,
			}, nil
		},
	}
}

func decodeRelationProductObject(raw json.RawMessage) (map[string]any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, errors.New("invalid relation preview JSON")
	}
	for index := 0; index < len(raw); index++ {
		if raw[index] != '"' {
			continue
		}
		start := index
		for index++; index < len(raw); index++ {
			if raw[index] == '\\' {
				index++
				continue
			}
			if raw[index] == '"' {
				break
			}
		}
		if !lookupStringHasUnicodeScalars(raw[start : index+1]) {
			return nil, errors.New("relation preview requires Unicode scalar values")
		}
	}
	// The existing recursive Product guard has the same depth and credential contract.
	if err := validateQueryViewValue(value, 0); err != nil {
		return nil, err
	}
	var compact strings.Builder
	if err := appendDescribeRevision(&compact, value); err != nil {
		return nil, err
	}
	if compact.Len() > maxRelationRequestBytes {
		return nil, errors.New("relation preview parameters exceed 1 MiB")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("relation preview requires an object")
	}
	return object, nil
}

func decodeRelationPreviewParams(raw json.RawMessage) (map[string]any, error) {
	object, err := decodeRelationProductObject(raw)
	if err != nil {
		return nil, err
	}
	for key := range object {
		switch key {
		case "relationId", "sourceItemId", "expectedSchemaRevision", "adds", "removes", "idempotencyKey", "expectedDateUpdated":
		default:
			return nil, errors.New("relation preview contains an unknown parameter")
		}
	}
	for _, key := range []string{"relationId", "sourceItemId", "expectedSchemaRevision", "adds", "removes", "idempotencyKey"} {
		if _, exists := object[key]; !exists {
			return nil, errors.New("relation preview omits a required parameter")
		}
	}
	// Python supplies no field_types for this method: value errors belong to the handler.
	return object, nil
}

func translateRelationPreview(original map[string]any) (map[string]any, error) {
	body := map[string]any{
		"expectedDigest": nil,
		"actor":          map[string]any{"type": "user", "id": "local-user", "displayName": nil},
	}
	for _, field := range [][2]string{
		{"relationId", "relationId"}, {"sourceItemId", "sourceRecordId"},
		{"expectedSchemaRevision", "schemaRevision"}, {"idempotencyKey", "idempotencyKey"},
	} {
		value, err := relationPreviewText(original, field[0])
		if err != nil {
			return nil, err
		}
		body[field[1]] = value
	}
	body["requestId"] = body["idempotencyKey"]
	for _, field := range []string{"adds", "removes"} {
		targets, ok := original[field].([]any)
		if !ok {
			return nil, errors.New("relation delta targets must be an array")
		}
		translated := make([]any, 0, len(targets))
		for _, value := range targets {
			target, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("relation delta target must be an object")
			}
			if nested, ok := target["target"].(map[string]any); ok {
				target = nested
			}
			table, err := relationPreviewText(target, "tableId", "collection")
			if err != nil {
				return nil, err
			}
			record, err := relationPreviewText(target, "recordId", "itemId")
			if err != nil {
				return nil, err
			}
			label := ""
			if value, exists := target["label"]; exists {
				var ok bool
				label, ok = value.(string)
				if !ok {
					return nil, errors.New("relation target label must be text")
				}
			}
			if label == "" {
				label = record
			}
			translated = append(translated, map[string]any{"tableId": table, "recordId": record, "label": label})
		}
		body[field] = translated
	}
	return body, nil
}

func relationPreviewText(object map[string]any, keys ...string) (string, error) {
	for _, key := range keys {
		if text, ok := object[key].(string); ok && text != "" {
			return text, nil
		}
	}
	return "", errors.New("relation preview requires non-empty text")
}
