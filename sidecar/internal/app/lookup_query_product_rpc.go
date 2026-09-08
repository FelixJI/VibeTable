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
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
)

func lookupQueryRegistration(port interface {
	Describe(context.Context, string) (relation.CatalogResult, error)
	QueryLookups(context.Context, relation.LookupQueryRequest) (relation.LookupQueryResult, error)
}) productrpc.Registration {
	return productrpc.Registration{
		Method: "lookup.query", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeLookupQueryParams(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			params, err := decodeLookupQueryParams(raw)
			if err != nil {
				return nil, err
			}
			queryBody := params["query"].(map[string]any)
			groups := []any{}
			if value, exists := queryBody["groups"]; exists {
				var ok bool
				groups, ok = value.([]any)
				if !ok {
					return nil, errors.New("query.groups must be an array")
				}
			}
			delete(queryBody, "groups")
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			table := params["collection"].(string)
			catalog, err := port.Describe(ctx, table)
			if err != nil {
				return nil, publicSchemaDescribeCatalogError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if catalog.Lookups == nil || catalog.SchemaRevision == "" {
				return nil, errors.New("PocketBase returned an invalid lookup catalog")
			}
			revision, err := describeRevision(map[string]any{"schemaRevision": catalog.SchemaRevision, "lookups": catalog.Lookups})
			if err != nil {
				return nil, err
			}
			if params["schemaRevision"] != catalog.SchemaRevision || params["permissionRevision"] != catalog.SchemaRevision || params["lookupRevision"] != revision {
				return nil, errors.New("Lookup query revisions are stale")
			}
			specs := make([]any, 0, len(groups))
			for _, value := range groups {
				group, ok := value.(map[string]any)
				if !ok {
					return nil, errors.New("Lookup groups must contain objects")
				}
				direction := "asc"
				if value, exists := group["direction"]; exists {
					direction, ok = value.(string)
					if !ok || (direction != "asc" && direction != "desc") {
						return nil, errors.New("Lookup group direction is invalid")
					}
				}
				field, ok := group["fieldRef"].(string)
				if !ok || field == "" {
					return nil, errors.New("fieldRef must be a non-empty string")
				}
				specs = append(specs, map[string]any{"field": field, "direction": direction})
			}
			// Preserve the former REST decoder after catalog/revisions/group translation.
			var body strings.Builder
			if err := appendDescribeRevision(&body, map[string]any{
				"tableId": table, "schemaRevision": catalog.SchemaRevision, "query": queryBody,
				"groups": specs, "groupLimit": json.Number("5000"),
			}); err != nil {
				return nil, err
			}
			var input relation.LookupQueryRequest
			if body.Len() > maxRelationRequestBytes || mutation.DecodeStrict([]byte(body.String()), &input) != nil {
				return nil, publicSchemaDescribeCatalogError(relationRequestError("relation request body is invalid"))
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			view, err := port.QueryLookups(ctx, input)
			if err != nil {
				var productError *mutation.ProductError
				if errors.As(err, &productError) {
					return nil, publicSchemaDescribeCatalogError(err)
				}
				return nil, publicQueryViewError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return projectLookupQuery(params, catalog, revision, view)
		},
	}
}

func decodeLookupQueryParams(raw json.RawMessage) (map[string]any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, errors.New("invalid lookup query JSON")
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
			return nil, errors.New("lookup query requires Unicode scalar values")
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
		return nil, errors.New("lookup query parameters exceed 1 MiB")
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 8 {
		return nil, errors.New("lookup query requires eight parameters")
	}
	for _, key := range []string{"contract", "collection", "schemaRevision", "permissionRevision", "lookupRevision"} {
		text, ok := object[key].(string)
		if !ok || text == "" {
			return nil, errors.New("lookup query requires non-empty text parameters")
		}
	}
	if _, ok := object["query"].(map[string]any); !ok {
		return nil, errors.New("query must be an object")
	}
	if _, ok := object["fieldRefs"].([]any); !ok {
		return nil, errors.New("fieldRefs must be an array")
	}
	generation, ok := object["requestGeneration"].(json.Number)
	if !ok || strings.ContainsAny(generation.String(), ".eE") {
		return nil, errors.New("requestGeneration must be an integer")
	}
	if generation == "-0" {
		object["requestGeneration"] = json.Number("0")
	}
	return object, nil
}

func projectLookupQuery(params map[string]any, catalog relation.CatalogResult, revision string, view relation.LookupQueryResult) (map[string]any, error) {
	// Match the Python client's flat-view checks, including the current REST omitempty pair.
	if view.GroupRows == nil {
		return nil, errors.New("PocketBase returned an invalid lookup view query result")
	}
	for _, row := range view.GroupRows {
		if row.Key == nil || row.Summaries == nil ||
			(row.ParentCount != nil) != (len(row.ParentSummaries) != 0) ||
			(row.ParentCount != nil && len(row.Key) != 2) {
			return nil, errors.New("PocketBase returned an invalid lookup view query result")
		}
	}
	if view.Rows == nil {
		return nil, errors.New("PocketBase returned an invalid query page")
	}
	for _, row := range view.Rows {
		if row == nil {
			return nil, errors.New("PocketBase returned an invalid query page")
		}
	}
	if view.HasMoreGroups {
		return nil, errors.New("Lookup group result exceeds the bounded window")
	}
	// The old handler renders all definitions before selecting columns; last physical name wins.
	lookupList, err := projectLookupList(catalog)
	if err != nil {
		return nil, err
	}
	definitions := map[string]map[string]any{}
	for _, value := range lookupList["definitions"].([]any) {
		definition := value.(map[string]any)
		definitions[definition["fieldKey"].(string)] = definition
	}
	columns := []any{}
	for _, value := range params["fieldRefs"].([]any) {
		field, ok := value.(string)
		definition, found := definitions[field]
		if !ok || !found {
			return nil, errors.New("fieldRefs contains an unknown Lookup")
		}
		columns = append(columns, map[string]any{"fieldRef": field, "title": definition["displayName"],
			"outputType": definition["outputType"], "nullable": true, "scale": definition["outputScale"], "state": definition["state"]})
	}
	groups, err := lookupQueryGroupNodes(view.GroupRows)
	if err != nil {
		return nil, err
	}
	return map[string]any{"contract": "vibetable.lookup-query.v1", "collection": params["collection"],
		"requestGeneration": params["requestGeneration"], "schemaRevision": catalog.SchemaRevision,
		"permissionRevision": catalog.SchemaRevision, "lookupRevision": revision, "columns": columns,
		"rows": view.Rows, "groups": groups, "offset": view.Offset, "limit": view.Limit,
		"filteredRows": view.FilteredRows, "totalRows": view.TotalRows, "snapshot": view.Snapshot}, nil
}

func lookupQueryGroupNodes(rows []query.GroupRow) ([]any, error) {
	result := []any{}
	parents := map[string]bool{}
	appendNode := func(path []any, key any, count int64) {
		result = append(result, map[string]any{"path": path, "key": key, "count": count, "aggregates": map[string]any{}, "childCursor": nil})
	}
	for _, row := range rows {
		if len(row.Key) != 1 && len(row.Key) != 2 {
			return nil, errors.New("Lookup group rows are invalid")
		}
		if len(row.Key) == 2 {
			if row.ParentCount == nil {
				return nil, errors.New("Lookup parent group row is invalid")
			}
			// Convert the typed service's dynamic value to its actual JSON number shape.
			encoded, err := json.Marshal(row.Key[0])
			if err != nil {
				return nil, err
			}
			decoder := json.NewDecoder(bytes.NewReader(encoded))
			decoder.UseNumber()
			var key any
			if err := decoder.Decode(&key); err != nil {
				return nil, err
			}
			var identity strings.Builder
			if err := appendDescribeRevision(&identity, key); err != nil {
				return nil, err
			}
			if !parents[identity.String()] {
				parents[identity.String()] = true
				appendNode([]any{}, row.Key[0], *row.ParentCount)
			}
		}
		appendNode(row.Key[:len(row.Key)-1], row.Key[len(row.Key)-1], row.Count)
	}
	return result, nil
}
