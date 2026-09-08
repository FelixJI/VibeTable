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

// Keep the Python parameter, handler, and former REST rejection boundaries separate.
func decodeQueryPageParams(raw json.RawMessage) (map[string]any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, errors.New("invalid query.page JSON")
	}
	// encoding/json otherwise replaces invalid UTF-8 and lone surrogate escapes.
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
			return nil, errors.New("query.page requires Unicode scalar values")
		}
	}
	if err := validateQueryPageValue(value, 0); err != nil {
		return nil, err
	}
	var compact strings.Builder
	// This existing serializer matches Python compact UTF-8 JSON, including numbers.
	if err := appendDescribeRevision(&compact, value); err != nil {
		return nil, err
	}
	if compact.Len() > maxQueryRequestBytes {
		return nil, errors.New("query.page parameters exceed 1 MiB")
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 2 {
		return nil, errors.New("query.page requires tableId and query")
	}
	table, ok := object["tableId"].(string)
	if !ok || table == "" {
		return nil, errors.New("query.page tableId must be non-empty text")
	}
	if _, ok := object["query"].(map[string]any); !ok {
		return nil, errors.New("query.page query must be an object")
	}
	return object, nil
}

func validateQueryPageValue(value any, depth int) error {
	if depth > 32 {
		return errors.New("query.page parameters are too deeply nested")
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			switch key {
			case "accessToken", "legacyToken", "password", "pocketBaseToken", "refreshToken", "sessionSecret":
				return errors.New("query.page parameters contain a forbidden field")
			}
			if err := validateQueryPageValue(item, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := validateQueryPageValue(item, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func queryPageRegistration(port interface {
	QueryPage(context.Context, string, query.TableQuery) (query.Page, error)
}) productrpc.Registration {
	return productrpc.Registration{
		Method: "query.page", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeQueryPageParams(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			object, err := decodeQueryPageParams(raw)
			if err != nil {
				return nil, err
			}
			// Python forwards compact JSON with operation added to the former REST body.
			object["operation"] = "page"
			var body strings.Builder
			if err := appendDescribeRevision(&body, object); err != nil {
				return nil, err
			}
			var input queryOperationRequest
			if err := decodeQueryRequest(strings.NewReader(body.String()), &input); err != nil {
				return nil, publicQueryPageError(err)
			}
			if strings.TrimSpace(input.TableID) == "" {
				return nil, publicQueryPageError(invalidQueryRequest("tableId", "tableId is required"))
			}
			if err := validateQueryOperation(input); err != nil {
				return nil, publicQueryPageError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			page, err := port.QueryPage(ctx, input.TableID, *input.Query)
			if err != nil {
				return nil, publicQueryPageError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// The typed Port already fixes integer counters and the snapshot object shape.
			// Retain the Python rejection of a null rows list or a null list element.
			if page.Rows == nil {
				return nil, errors.New("PocketBase returned an invalid query page")
			}
			for _, row := range page.Rows {
				if row == nil {
					return nil, errors.New("PocketBase returned an invalid query page")
				}
			}
			return map[string]any{
				"rows": page.Rows, "offset": page.Offset, "limit": page.Limit,
				"filteredRows": page.FilteredRows, "totalRows": page.TotalRows, "snapshot": page.Snapshot,
			}, nil
		},
	}
}

func publicQueryPageError(err error) error {
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
