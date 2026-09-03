package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

var (
	productSchemaTableID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	productSchemaEmptyPath = ""
)

func schemaListRegistration(catalog schemaapi.SchemaCatalog) productrpc.Registration {
	return productrpc.Registration{
		Method: "schema.list", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			var params map[string]json.RawMessage
			if err := json.Unmarshal(raw, &params); err != nil || params == nil || len(params) != 0 {
				return errors.New("schema.list requires an empty object")
			}
			return nil
		},
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			result, err := listSchemaTables(ctx, catalog)
			return result, publicSchemaProductError(err)
		},
	}
}

func schemaGetTableRegistration(catalog schemaapi.SchemaCatalog) productrpc.Registration {
	return productrpc.Registration{
		Method: "schema.getTable", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			var params struct {
				TableID string `json:"tableId"`
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil || object == nil || len(object) != 1 {
				return errors.New("schema.getTable requires exactly tableId")
			}
			if _, found := object["tableId"]; !found || json.Unmarshal(raw, &params) != nil || params.TableID == "" {
				return errors.New("schema.getTable tableId must be a non-empty string")
			}
			return nil
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var params struct {
				TableID string `json:"tableId"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, err
			}
			// Keep the former Python adapter's _text then _path_segment sequence:
			// whitespace is a non-empty parameter, but not a valid sidecar table path.
			if !productSchemaTableID.MatchString(params.TableID) {
				return nil, errors.New("table id is invalid")
			}
			table, err := catalog.Describe(ctx, params.TableID)
			if err != nil {
				return nil, publicSchemaGetTableError(err)
			}
			return schemaSnapshotProductResult(table.Snapshot)
		},
	}
}

// Both transports project the same authoritative catalog, including its order.
func listSchemaTables(ctx context.Context, catalog schemaapi.SchemaCatalog) (map[string]any, error) {
	definitions, err := catalog.List(ctx)
	if err != nil {
		return nil, err
	}
	tables := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		tables = append(tables, map[string]any{
			"tableId":     definition.Snapshot.TableID,
			"displayName": definition.Snapshot.DisplayName,
			"kind":        definition.Snapshot.Kind,
		})
	}
	return map[string]any{"tables": tables}, nil
}

func publicSchemaProductError(err error) error {
	if err == nil {
		return nil
	}
	var productError *schemaerror.ProductError
	if !errors.As(err, &productError) {
		return err
	}
	return &productrpc.PublicError{
		Code: productError.Code, Path: &productError.Path,
		Message: productError.Message, Details: productError.Details,
		Retryable: productError.Retryable,
	}
}

func publicSchemaGetTableError(err error) error {
	var productError *schemaerror.ProductError
	if errors.As(err, &productError) && productError.Code == "schema.table.not_found" {
		return publicSchemaProductError(err)
	}
	// The former Python owner delegated this request to the public field route.
	// That route exposes every Describe failure except a missing table as this
	// stable retryable field error; keep its public contract while sharing the
	// same schema catalog authority.
	return &productrpc.PublicError{
		Code: "field.internal.failed", Path: &productSchemaEmptyPath,
		Message: "field settings operation failed", Details: map[string]any{}, Retryable: true,
	}
}

// schemaSnapshotProductResult keeps schema.getTable's existing public
// shape: the former Python handler validated the REST response then serialized
// SchemaSnapshotV2 with model_dump, which emits nullable defaults omitted by
// Go's REST JSON tags.
func schemaSnapshotProductResult(snapshot v2.SchemaSnapshot) (map[string]any, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal schema Product snapshot: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode schema Product snapshot: %w", err)
	}
	fields, ok := result["fields"].([]any)
	if !ok {
		return nil, errors.New("schema Product snapshot fields are invalid")
	}
	for _, rawField := range fields {
		field, ok := rawField.(map[string]any)
		if !ok {
			return nil, errors.New("schema Product snapshot field is invalid")
		}
		for _, name := range []string{"select", "relation", "file", "json", "autoDate", "formula", "lookup"} {
			if _, found := field[name]; !found {
				field[name] = nil
			}
		}
		addPythonSchemaFieldDefaults(field)
	}
	capabilities, ok := result["capabilities"].([]any)
	if !ok {
		return nil, errors.New("schema Product snapshot capabilities are invalid")
	}
	for _, rawCapability := range capabilities {
		capability, ok := rawCapability.(map[string]any)
		if !ok {
			return nil, errors.New("schema Product snapshot capability is invalid")
		}
		addPythonSchemaRecommendedDefaults(capability["recommended"].(map[string]any))
	}
	return result, nil
}

func addPythonSchemaFieldDefaults(field map[string]any) {
	addPythonSchemaValueDefaults(field["value"].(map[string]any))
	display := field["display"].(map[string]any)
	if _, found := display["indent"]; !found {
		display["indent"] = nil
	}
	if relation, ok := field["relation"].(map[string]any); ok {
		for _, name := range []string{"pairId", "reciprocalFieldId"} {
			if _, found := relation[name]; !found {
				relation[name] = nil
			}
		}
	}
}

func addPythonSchemaRecommendedDefaults(recommended map[string]any) {
	for _, name := range []string{"file", "json"} {
		if _, found := recommended[name]; !found {
			recommended[name] = nil
		}
	}
	addPythonSchemaValueDefaults(recommended["value"].(map[string]any))
}

func addPythonSchemaValueDefaults(value map[string]any) {
	presence := value["presence"].(map[string]any)
	for _, name := range []string{"providerFieldId", "physicalName"} {
		if _, found := presence[name]; !found {
			presence[name] = nil
		}
	}
}
