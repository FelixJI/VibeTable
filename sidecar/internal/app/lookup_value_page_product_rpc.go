package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	lookupcalc "github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
)

func lookupValuePageRegistration(port interface {
	Describe(context.Context, string) (relation.CatalogResult, error)
	LookupValuePage(context.Context, relation.LookupValuePageRequest) (lookupcalc.CellValue, error)
}) productrpc.Registration {
	return productrpc.Registration{
		Method: "lookup.valuePage", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeLookupValuePageParams(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			params, err := decodeLookupValuePageParams(raw)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			catalog, err := port.Describe(ctx, params["collection"].(string))
			if err != nil {
				return nil, publicSchemaDescribeCatalogError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if catalog.Lookups == nil || catalog.SchemaRevision == "" {
				return nil, errors.New("PocketBase returned an invalid lookup catalog")
			}
			if params["schemaRevision"] != catalog.SchemaRevision || params["permissionRevision"] != catalog.SchemaRevision {
				return nil, errors.New("Lookup value page revisions are stale")
			}
			revision, err := describeRevision(map[string]any{"schemaRevision": catalog.SchemaRevision, "lookups": catalog.Lookups})
			if err != nil {
				return nil, err
			}
			if params["lookupRevision"] != revision {
				return nil, errors.New("Lookup value page revisions are stale")
			}
			var selected *relation.LookupDescriptor
			for index := range catalog.Lookups {
				if catalog.Lookups[index].PhysicalName == params["fieldRef"] {
					selected = &catalog.Lookups[index]
					break
				}
			}
			if selected == nil {
				return nil, errors.New("fieldRef does not identify a Lookup")
			}
			offset, _ := new(big.Int).SetString(string(params["offset"].(json.Number)), 10)
			limit, _ := new(big.Int).SetString(string(params["limit"].(json.Number)), 10)
			if offset.Sign() < 0 || limit.Sign() <= 0 || limit.Cmp(big.NewInt(500)) > 0 {
				return nil, errors.New("Lookup value page paging is invalid")
			}
			// The original Python handler reads fieldId only after paging validation.
			if selected.FieldID == "" {
				return nil, errors.New("Lookup fieldId must be non-empty text")
			}
			// Product integers are unbounded; the former REST decoder rejects overflow.
			if !offset.IsInt64() {
				return nil, publicSchemaDescribeCatalogError(relationRequestError("relation request body is invalid"))
			}
			input := relation.LookupValuePageRequest{
				TableID: params["collection"].(string), SchemaRevision: catalog.SchemaRevision,
				SourceRecordID: params["sourceRecordId"].(string), FieldID: selected.FieldID,
				Offset: int(offset.Int64()), Limit: int(limit.Int64()),
			}
			var body strings.Builder
			if err := appendDescribeRevision(&body, map[string]any{
				"tableId": input.TableID, "schemaRevision": input.SchemaRevision,
				"sourceRecordId": input.SourceRecordID, "fieldId": input.FieldID,
				"offset": params["offset"], "limit": params["limit"],
			}); err != nil {
				return nil, err
			}
			if body.Len() > maxRelationRequestBytes {
				return nil, publicSchemaDescribeCatalogError(relationRequestError("relation request body is invalid"))
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			result, err := port.LookupValuePage(ctx, input)
			if err != nil {
				var mutationError *mutation.ProductError
				if errors.As(err, &mutationError) {
					return nil, publicSchemaDescribeCatalogError(err)
				}
				return nil, publicQueryViewError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// Python only validates a finite JSON object, not CellValue field semantics.
			encoded, err := json.Marshal(result)
			if err != nil {
				return nil, err
			}
			return json.RawMessage(encoded), nil
		},
	}
}

func decodeLookupValuePageParams(raw json.RawMessage) (map[string]any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, errors.New("invalid lookup.valuePage JSON")
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
			return nil, errors.New("lookup.valuePage requires Unicode scalar values")
		}
	}
	if err := validateQueryViewValue(value, 0); err != nil {
		return nil, err
	}
	var compact strings.Builder
	if err := appendDescribeRevision(&compact, value); err != nil {
		return nil, err
	}
	if compact.Len() > maxRelationRequestBytes {
		return nil, errors.New("lookup.valuePage parameters exceed 1 MiB")
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 8 {
		return nil, errors.New("lookup.valuePage requires exactly eight fields")
	}
	for _, key := range []string{"collection", "fieldRef", "sourceRecordId", "schemaRevision", "permissionRevision", "lookupRevision"} {
		text, ok := object[key].(string)
		if !ok || text == "" {
			return nil, errors.New("lookup.valuePage requires non-empty text")
		}
	}
	for _, key := range []string{"offset", "limit"} {
		number, ok := object[key].(json.Number)
		if !ok || strings.ContainsAny(string(number), ".eE") {
			return nil, errors.New("lookup.valuePage requires integer paging")
		}
		if _, ok := new(big.Int).SetString(string(number), 10); !ok {
			return nil, errors.New("lookup.valuePage requires integer paging")
		}
	}
	return object, nil
}
