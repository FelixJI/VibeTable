package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/query"
)

func querySelectionOpenRegistration(port interface {
	OpenSelectionProjection(context.Context, string, query.TableQuery) (query.SelectionProjection, error)
}) productrpc.Registration {
	return productrpc.Registration{
		Method: "query.selectionOpen", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			_, err := decodeQuerySelectionParams(raw)
			return err
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			object, err := decodeQuerySelectionParams(raw)
			if err != nil {
				return nil, err
			}
			object["operation"] = "selection.open"
			var body strings.Builder
			if err := appendDescribeRevision(&body, object); err != nil {
				return nil, err
			}
			var input queryOperationRequest
			if err := decodeQueryRequest(strings.NewReader(body.String()), &input); err != nil {
				return nil, publicQuerySelectionError(err)
			}
			if strings.TrimSpace(input.TableID) == "" {
				return nil, publicQuerySelectionError(invalidQueryRequest("tableId", "tableId is required"))
			}
			if err := validateQueryOperation(input); err != nil {
				return nil, publicQuerySelectionError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			projection, err := port.OpenSelectionProjection(ctx, input.TableID, *input.Query)
			if err != nil {
				return nil, publicQuerySelectionError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if projection.CursorWindow.Rows == nil || projection.CursorWindow.FilteredRows < 0 ||
				projection.CursorWindow.TotalRows < 0 || projection.CursorWindow.HasMore != (projection.CursorWindow.NextCursor != nil) {
				return nil, errors.New("PocketBase returned an invalid selection cursor window")
			}
			for _, row := range projection.CursorWindow.Rows {
				if row == nil {
					return nil, errors.New("PocketBase returned an invalid selection cursor window")
				}
			}
			if projection.SchemaSnapshot.TableID != input.TableID ||
				projection.CursorWindow.Snapshot.Table != input.TableID ||
				projection.CursorWindow.Snapshot.SchemaRevision == "" ||
				projection.SchemaSnapshot.SchemaRevision != projection.CursorWindow.Snapshot.SchemaRevision ||
				projection.SchemaSnapshot.DataRevision != projection.CursorWindow.Snapshot.DataRevision {
				return nil, errors.New("PocketBase returned mismatched selection revisions")
			}
			schema, err := schemaSnapshotProductResult(projection.SchemaSnapshot)
			if err != nil {
				return nil, err
			}
			return map[string]any{"schemaSnapshot": schema, "cursorWindow": projection.CursorWindow}, nil
		},
	}
}

func decodeQuerySelectionParams(raw json.RawMessage) (map[string]any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, errors.New("invalid query selection JSON")
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
			return nil, errors.New("query selection parameters require Unicode scalar values")
		}
	}
	if err := validateQuerySelectionValue(value, 0); err != nil {
		return nil, err
	}
	var compact strings.Builder
	if err := appendDescribeRevision(&compact, value); err != nil {
		return nil, err
	}
	if compact.Len() > maxQueryRequestBytes {
		return nil, errors.New("query selection parameters exceed 1 MiB")
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 2 {
		return nil, errors.New("query.selectionOpen requires exactly tableId and query")
	}
	table, tableOK := object["tableId"].(string)
	_, queryOK := object["query"].(map[string]any)
	if !tableOK || table == "" || !queryOK {
		return nil, errors.New("query.selectionOpen requires non-empty tableId text and a query object")
	}
	return object, nil
}

func validateQuerySelectionValue(value any, depth int) error {
	if depth > 32 {
		return errors.New("query selection parameters are too deeply nested")
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			switch key {
			case "accessToken", "legacyToken", "password", "pocketBaseToken", "refreshToken", "sessionSecret":
				return errors.New("query selection parameters contain a forbidden field")
			}
			if err := validateQuerySelectionValue(item, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := validateQuerySelectionValue(item, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func publicQuerySelectionError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var source *query.ProductError
	if !errors.As(err, &source) {
		source = &query.ProductError{Code: "query.internal.failed", Message: "query operation failed"}
	}
	return &productrpc.PublicError{Code: source.Code, Path: &source.Path, Message: source.Message,
		Details: source.Details, Retryable: source.Retryable}
}
