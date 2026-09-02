package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
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
			var productError *schemaerror.ProductError
			if errors.As(err, &productError) {
				return nil, &productrpc.PublicError{
					Code: productError.Code, Path: &productError.Path,
					Message: productError.Message, Details: productError.Details,
					Retryable: productError.Retryable,
				}
			}
			return result, err
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
