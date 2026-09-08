package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
)

func decodeLookupListParams(raw json.RawMessage) (string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || len(object) != 1 {
		return "", errors.New("lookup.list requires collection")
	}
	var collection *string
	if err := json.Unmarshal(object["collection"], &collection); err != nil || collection == nil || *collection == "" {
		return "", errors.New("lookup.list collection must be a non-empty string")
	}
	if !lookupStringHasUnicodeScalars(object["collection"]) {
		return "", errors.New("lookup.list collection must contain Unicode scalar values")
	}
	// Match ProductParams' compact UTF-8 JSON budget, independent of wire escaping.
	size := len(`{"collection":""}`) + len(*collection)
	for _, character := range *collection {
		switch character {
		case '"', '\\', '\b', '\f', '\n', '\r', '\t':
			size++
		default:
			if character < 0x20 {
				size += 5
			}
		}
	}
	if size > 1<<20 {
		return "", errors.New("lookup.list parameters exceed the safe size limit")
	}
	return *collection, nil
}

// The value has already passed JSON string decoding. Check its original wire
// form because encoding/json replaces invalid UTF-8 and unpaired surrogates.
func lookupStringHasUnicodeScalars(value json.RawMessage) bool {
	if !utf8.Valid(value) {
		return false
	}
	for index := 1; index < len(value)-1; index++ {
		if value[index] != '\\' {
			continue
		}
		index++
		if value[index] != 'u' {
			continue
		}
		code, err := strconv.ParseUint(string(value[index+1:index+5]), 16, 16)
		if err != nil {
			return false
		}
		index += 4
		if code < 0xd800 || code > 0xdfff {
			continue
		}
		if code >= 0xdc00 || index+7 >= len(value) || value[index+1] != '\\' || value[index+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(value[index+3:index+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		index += 6
	}
	return true
}

func lookupListRegistration(service *relation.Service) productrpc.Registration {
	return productrpc.Registration{
		Method: "lookup.list", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			_, err := decodeLookupListParams(raw)
			return err
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			collection, err := decodeLookupListParams(raw)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			catalog, err := service.Describe(ctx, collection)
			if err != nil {
				return nil, publicSchemaDescribeCatalogError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return projectLookupList(catalog)
		},
	}
}

// projectLookupList preserves the Product projection consumed by the lookup panel.
func projectLookupList(catalog relation.CatalogResult) (map[string]any, error) {
	revision, err := describeRevision(map[string]any{"schemaRevision": catalog.SchemaRevision, "lookups": catalog.Lookups})
	if err != nil {
		return nil, err
	}
	definitions := make([]any, 0, len(catalog.Lookups))
	for _, lookup := range catalog.Lookups {
		if lookup.ResultCardinality != "one" && lookup.ResultCardinality != "many" {
			return nil, errors.New("PocketBase returned an invalid Lookup result cardinality")
		}
		outputType, err := lookupOutputType(lookup.OutputStorage)
		if err != nil {
			return nil, err
		}
		for _, value := range []string{lookup.TableID, lookup.RelationFieldID, lookup.LookupID, lookup.PhysicalName, lookup.DisplayName, lookup.TargetFieldID} {
			if value == "" {
				return nil, errors.New("PocketBase returned an incomplete Lookup definition")
			}
		}
		path := lookup.Path
		if path == nil {
			path = []relation.LookupPathDescriptor{{RelationID: lookup.TableID + "." + lookup.RelationFieldID}}
		}
		if len(path) == 0 {
			return nil, errors.New("PocketBase returned an invalid Lookup path")
		}
		dependencies := make([]string, 0, len(path))
		for _, step := range path {
			if step.RelationID == "" {
				return nil, errors.New("PocketBase returned an invalid Lookup path")
			}
			dependencies = append(dependencies, step.RelationID)
		}
		definitions = append(definitions, map[string]any{
			"lookupId": lookup.LookupID, "collection": lookup.TableID,
			"fieldKey": lookup.PhysicalName, "displayName": lookup.DisplayName,
			"path": path, "source": map[string]any{"kind": "target_field", "fieldRef": lookup.TargetFieldID},
			"outputType": outputType, "outputScale": nil, "revision": lookup.Revision,
			"state": "valid", "diagnostics": []any{}, "dependencies": dependencies,
		})
	}
	return map[string]any{"collection": catalog.TableID, "definitions": definitions, "lookupRevision": revision}, nil
}

func lookupOutputType(storage string) (string, error) {
	switch storage {
	case "text", "shortText", "longText", "richText", "editor", "email", "url", "uuid", "select", "multiSelect", "list", "hash", "secret", "relation", "file", "formula", "lookup":
		return "text", nil
	case "integer":
		return "integer", nil
	case "number", "float", "decimal":
		return "decimal", nil
	case "boolean", "bool":
		return "boolean", nil
	case "date":
		return "date", nil
	case "dateTime", "datetime", "autoDate", "autodate":
		return "datetime", nil
	case "time":
		return "time", nil
	case "json", "geoPoint", "geoJson":
		return "json", nil
	default:
		return "", errors.New("PocketBase returned an unknown data type")
	}
}
