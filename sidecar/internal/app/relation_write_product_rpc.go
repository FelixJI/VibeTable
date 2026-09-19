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

var relationWriteMethods = []string{"relation.createTarget", "relation.updateSingle", "relation.applyDelta"}

type relationWritePort interface {
	Describe(context.Context, string) (relation.CatalogResult, error)
	CreateTarget(context.Context, relation.CreateTargetRequest) (relation.CreateTargetResult, error)
	ApplyDelta(context.Context, relation.DeltaRequest) (relation.DeltaResult, error)
}

type relationRowReader interface {
	ReadRows(context.Context, string, []string) ([]map[string]any, error)
}

func relationWriteRegistrations(port relationWritePort, rows relationRowReader, gates ...businessWriteGate) []productrpc.Registration {
	registrations := make([]productrpc.Registration, 0, len(relationWriteMethods))
	for _, method := range relationWriteMethods {
		registrations = append(registrations, productrpc.Registration{
			Method: method, Scope: productcapabilities.WorkspaceScope,
			ValidateParams: func(raw json.RawMessage) error { _, err := decodeRelationWriteParams(method, raw); return err },
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				original, err := decodeRelationWriteParams(method, raw)
				if err != nil {
					return nil, err
				}
				if method == "relation.createTarget" {
					key := original["idempotencyKey"].(string)
					label, _ := original["label"].(string)
					values, _ := original["values"].(map[string]any)
					if values == nil {
						values = map[string]any{}
					}
					body := map[string]any{"relationId": original["relationId"], "label": label, "values": values, "requestId": key, "idempotencyKey": key, "actor": relationWriteActor()}
					var input relation.CreateTargetRequest
					if err := decodeRelationWriteBody(body, &input); err != nil {
						return nil, err
					}
					var result relation.CreateTargetResult
					err = runBusinessWrite(ctx, gates, "relation.create-target", key, func(ctx context.Context) error {
						var callErr error
						result, callErr = port.CreateTarget(ctx, input)
						return callErr
					})
					if err != nil {
						return nil, publicSchemaDescribeCatalogError(err)
					}
					target, err := relationWriteTargetResult(result.Target)
					if err != nil {
						return nil, err
					}
					return map[string]any{"outcome": "committed", "target": target, "requestId": key}, nil
				}
				var body map[string]any
				if method == "relation.updateSingle" {
					body, err = translateRelationSingle(ctx, original, port, rows)
				} else {
					body, err = translateRelationWriteDelta(original)
				}
				if err != nil {
					return nil, err
				}
				var input relation.DeltaRequest
				if err := decodeRelationWriteBody(body, &input); err != nil {
					return nil, err
				}
				var result relation.DeltaResult
				err = runBusinessWrite(ctx, gates, "relation.apply-delta", input.IdempotencyKey, func(ctx context.Context) error {
					var callErr error
					result, callErr = port.ApplyDelta(ctx, input)
					return callErr
				})
				if err != nil {
					return nil, publicSchemaDescribeCatalogError(err)
				}
				response := map[string]any{"outcome": "committed", "schemaRevision": original["expectedSchemaRevision"], "requestId": original["idempotencyKey"]}
				if method == "relation.updateSingle" {
					response["current"] = original["target"]
					response["receipt"] = result.Receipt
				} else {
					if result.Current == nil {
						return nil, errors.New("PocketBase returned invalid relation result")
					}
					current := make([]any, 0, len(result.Current))
					for _, target := range result.Current {
						value, err := relationWriteTargetResult(target)
						if err != nil {
							return nil, err
						}
						current = append(current, value)
					}
					response["current"] = current
				}
				return response, nil
			},
		})
	}
	return registrations
}

func decodeRelationWriteParams(method string, raw json.RawMessage) (map[string]any, error) {
	if method == "relation.applyDelta" {
		return decodeRelationPreviewParams(raw)
	}
	invalid := errors.New("invalid relation write parameters")
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, invalid
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
			return nil, invalid
		}
	}
	if err := validateQueryPageValue(value, 0); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, invalid
	}
	required := []string{"relationId", "idempotencyKey"}
	allowed := map[string]bool{"relationId": true, "idempotencyKey": true, "label": true, "values": true}
	if method == "relation.updateSingle" {
		required = []string{"relationId", "sourceItemId", "target", "expectedSchemaRevision", "idempotencyKey"}
		allowed = map[string]bool{"relationId": true, "sourceItemId": true, "target": true, "expectedSchemaRevision": true, "expectedDateUpdated": true, "idempotencyKey": true}
	}
	for _, key := range required {
		if _, ok := object[key]; !ok {
			return nil, invalid
		}
	}
	for key, item := range object {
		if !allowed[key] {
			return nil, invalid
		}
		switch key {
		case "target":
			if item == nil {
				continue
			}
			if _, ok := item.(map[string]any); !ok {
				return nil, invalid
			}
		case "values":
			if _, ok := item.(map[string]any); !ok {
				return nil, invalid
			}
		default:
			if key == "expectedDateUpdated" && item == nil {
				continue
			}
			if text, ok := item.(string); !ok || text == "" {
				return nil, invalid
			}
		}
	}
	var compact strings.Builder
	if err := appendDescribeRevision(&compact, value); err != nil {
		return nil, err
	}
	if compact.Len() > maxRelationRequestBytes {
		return nil, invalid
	}
	return object, nil
}

func relationWriteActor() map[string]any {
	return map[string]any{"type": "user", "id": "local-user", "displayName": nil}
}

func translateRelationWriteTarget(value any) (map[string]any, error) {
	target, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("relation target must be an object")
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
			return nil, errors.New("target label must be text")
		}
	}
	if label == "" {
		label = record
	}
	return map[string]any{"tableId": table, "recordId": record, "label": label}, nil
}

func translateRelationWriteDelta(original map[string]any) (map[string]any, error) {
	body := map[string]any{"expectedDigest": nil, "actor": relationWriteActor()}
	for _, field := range [][2]string{{"relationId", "relationId"}, {"sourceItemId", "sourceRecordId"}, {"expectedSchemaRevision", "schemaRevision"}, {"idempotencyKey", "idempotencyKey"}} {
		value, err := relationPreviewText(original, field[0])
		if err != nil {
			return nil, err
		}
		body[field[1]] = value
	}
	body["requestId"] = body["idempotencyKey"]
	for _, key := range []string{"adds", "removes"} {
		values, ok := original[key].([]any)
		if !ok {
			return nil, errors.New("relation delta targets must be an array")
		}
		targets := make([]any, 0, len(values))
		for _, value := range values {
			target, err := translateRelationWriteTarget(value)
			if err != nil {
				return nil, err
			}
			targets = append(targets, target)
		}
		body[key] = targets
	}
	return body, nil
}

func translateRelationSingle(ctx context.Context, original map[string]any, port relationWritePort, rows relationRowReader) (map[string]any, error) {
	relationID := original["relationId"].(string)
	catalog, err := port.Describe(ctx, strings.SplitN(relationID, ".", 2)[0])
	if err != nil {
		return nil, publicSchemaDescribeCatalogError(err)
	}
	var descriptor *relation.Descriptor
	for _, candidate := range catalog.Relations {
		if candidate.RelationID == relationID {
			descriptor = &candidate
			break
		}
	}
	if descriptor == nil || descriptor.Cardinality != "one" {
		return nil, errors.New("single relation is unavailable")
	}
	if descriptor.SourceTableID == "" || descriptor.TargetTableID == "" || descriptor.PhysicalName == "" {
		return nil, errors.New("invalid relation descriptor")
	}
	source, err := rows.ReadRows(ctx, descriptor.SourceTableID, []string{original["sourceItemId"].(string)})
	if err != nil {
		return nil, publicQueryPageError(err)
	}
	if len(source) != 1 {
		return nil, errors.New("relation source record was not found")
	}
	ids := []string{}
	switch value := source[0][descriptor.PhysicalName].(type) {
	case nil:
	case string:
		if value != "" {
			ids = append(ids, value)
		}
	case []any:
		for _, value := range value {
			id, ok := value.(string)
			if !ok || id == "" {
				return nil, errors.New("invalid relation value")
			}
			ids = append(ids, id)
		}
	case []string:
		for _, id := range value {
			if id == "" {
				return nil, errors.New("invalid relation value")
			}
			ids = append(ids, id)
		}
	default:
		return nil, errors.New("invalid relation value")
	}
	adds, removes := []any{}, []any{}
	desired := ""
	if original["target"] != nil {
		target, err := translateRelationWriteTarget(original["target"])
		if err != nil {
			return nil, err
		}
		if target["tableId"] != descriptor.TargetTableID {
			return nil, errors.New("relation target belongs to another table")
		}
		desired = target["recordId"].(string)
		found := false
		for _, id := range ids {
			if id == desired {
				found = true
			}
		}
		if !found {
			adds = append(adds, target)
		}
	}
	for _, id := range ids {
		if id != desired {
			removes = append(removes, map[string]any{"tableId": descriptor.TargetTableID, "recordId": id, "label": id})
		}
	}
	return translateRelationWriteDelta(map[string]any{"relationId": relationID, "sourceItemId": original["sourceItemId"], "expectedSchemaRevision": original["expectedSchemaRevision"], "idempotencyKey": original["idempotencyKey"], "adds": adds, "removes": removes})
}

func decodeRelationWriteBody(body map[string]any, output any) error {
	var compact strings.Builder
	if err := appendDescribeRevision(&compact, body); err != nil {
		return err
	}
	if compact.Len() > maxRelationRequestBytes || mutation.DecodeStrict([]byte(compact.String()), output) != nil {
		return publicSchemaDescribeCatalogError(relationRequestError("relation request body is invalid"))
	}
	return nil
}

func relationWriteTargetResult(target relation.TargetRef) (map[string]any, error) {
	if target.TableID == "" || target.RecordID == "" || target.Label == "" {
		return nil, errors.New("PocketBase returned an invalid relation target")
	}
	var secondary any
	if target.SecondaryLabel != "" {
		secondary = target.SecondaryLabel
	}
	return map[string]any{"collection": target.TableID, "itemId": target.RecordID, "label": target.Label, "secondaryLabel": secondary}, nil
}
