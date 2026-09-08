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

func relationSearchTargetsRegistration(port interface {
	SearchTargets(context.Context, relation.SearchRequest) (relation.SearchResult, error)
}) productrpc.Registration {
	return productrpc.Registration{
		Method: "relation.searchTargets", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeRelationSearchParams(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			object, err := decodeRelationSearchParams(raw)
			if err != nil {
				return nil, err
			}
			for key, value := range map[string]any{"query": "", "offset": json.Number("0"), "limit": json.Number("50")} {
				if _, exists := object[key]; !exists {
					object[key] = value
				}
			}
			// Python adds defaults before the former REST body's independent budget.
			var body strings.Builder
			if err := appendDescribeRevision(&body, object); err != nil {
				return nil, err
			}
			var input relation.SearchRequest
			if body.Len() > maxRelationRequestBytes || mutation.DecodeStrict([]byte(body.String()), &input) != nil {
				return nil, publicSchemaDescribeCatalogError(relationRequestError("relation request body is invalid"))
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			result, err := port.SearchTargets(ctx, input)
			if err != nil {
				return nil, publicSchemaDescribeCatalogError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if result.Items == nil {
				return nil, errors.New("PocketBase returned invalid relation targets")
			}
			items := make([]map[string]any, 0, len(result.Items))
			for _, item := range result.Items {
				if item.TableID == "" || item.RecordID == "" || item.Label == "" {
					return nil, errors.New("PocketBase returned an invalid relation target")
				}
				items = append(items, map[string]any{"collection": item.TableID, "itemId": item.RecordID, "label": item.Label})
			}
			return map[string]any{"items": items, "total": result.Total}, nil
		},
	}
}

func decodeRelationSearchParams(raw json.RawMessage) (map[string]any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, errors.New("invalid relation search JSON")
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
			return nil, errors.New("relation search requires Unicode scalar values")
		}
	}
	if err := validateRelationSearchValue(value, 0); err != nil {
		return nil, err
	}
	var compact strings.Builder
	if err := appendDescribeRevision(&compact, value); err != nil {
		return nil, err
	}
	if compact.Len() > maxRelationRequestBytes {
		return nil, errors.New("relation search parameters exceed 1 MiB")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("relation search requires an object")
	}
	id, ok := object["relationId"].(string)
	if !ok || id == "" {
		return nil, errors.New("relationId must be non-empty text")
	}
	for key, item := range object {
		switch key {
		case "relationId", "query":
			text, ok := item.(string)
			if !ok || text == "" {
				return nil, errors.New("relation search text must not be empty")
			}
		case "offset", "limit":
			number, ok := item.(json.Number)
			if !ok || strings.ContainsAny(number.String(), ".eE") {
				return nil, errors.New("relation search paging must be integer")
			}
		default:
			return nil, errors.New("relation search contains an unknown parameter")
		}
	}
	return object, nil
}

func validateRelationSearchValue(value any, depth int) error {
	if depth > 32 {
		return errors.New("relation search parameters are too deeply nested")
	}
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			switch key {
			case "accessToken", "legacyToken", "password", "pocketBaseToken", "refreshToken", "sessionSecret":
				return errors.New("relation search parameters contain a forbidden field")
			}
			if err := validateRelationSearchValue(item, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range value {
			if err := validateRelationSearchValue(item, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
