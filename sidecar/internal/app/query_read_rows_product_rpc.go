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
func decodeQueryReadRowsParams(raw json.RawMessage) (map[string]any, int, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, 0, errors.New("invalid query.readRows JSON")
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
			return nil, 0, errors.New("query.readRows requires Unicode scalar values")
		}
	}
	if err := validateQueryReadRowsValue(value, 0); err != nil {
		return nil, 0, err
	}
	var compact strings.Builder
	// This existing serializer matches Python compact UTF-8 JSON, including numbers.
	if err := appendDescribeRevision(&compact, value); err != nil {
		return nil, 0, err
	}
	if compact.Len() > maxQueryRequestBytes {
		return nil, 0, errors.New("query.readRows parameters exceed 1 MiB")
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 2 {
		return nil, 0, errors.New("query.readRows requires tableId and rowIds")
	}
	table, ok := object["tableId"].(string)
	if !ok || table == "" {
		return nil, 0, errors.New("query.readRows tableId must be non-empty text")
	}
	if _, ok := object["rowIds"].([]any); !ok {
		return nil, 0, errors.New("query.readRows rowIds must be an array")
	}
	return object, compact.Len(), nil
}

func validateQueryReadRowsValue(value any, depth int) error {
	if depth > 32 {
		return errors.New("query.readRows parameters are too deeply nested")
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			switch key {
			case "accessToken", "legacyToken", "password", "pocketBaseToken", "refreshToken", "sessionSecret":
				return errors.New("query.readRows parameters contain a forbidden field")
			}
			if err := validateQueryReadRowsValue(item, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := validateQueryReadRowsValue(item, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func queryReadRowsRegistration(port interface {
	ReadRows(context.Context, string, []string) ([]map[string]any, error)
}) productrpc.Registration {
	return productrpc.Registration{
		Method: "query.readRows", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, _, err := decodeQueryReadRowsParams(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			object, size, err := decodeQueryReadRowsParams(raw)
			if err != nil {
				return nil, err
			}
			values := object["rowIds"].([]any)
			ids := make([]string, len(values))
			for index, value := range values {
				id, ok := value.(string)
				if !ok || id == "" {
					return nil, errors.New("rowIds must contain non-empty strings")
				}
				ids[index] = id
			}
			// The former HTTP body added operation after the ProductParams budget check.
			if size+len(`,"operation":"readRows"`) > maxQueryRequestBytes {
				return nil, publicQueryReadRowsError(invalidQueryRequest("", "query request exceeds the 1 MiB limit"))
			}
			table := object["tableId"].(string)
			if strings.TrimSpace(table) == "" {
				return nil, publicQueryReadRowsError(invalidQueryRequest("tableId", "tableId is required"))
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			rows, err := port.ReadRows(ctx, table, ids)
			if err != nil {
				return nil, publicQueryReadRowsError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// Python rejects a null rows list or null list element after decoding REST JSON.
			if rows == nil {
				return nil, errors.New("PocketBase returned invalid rows")
			}
			for _, row := range rows {
				if row == nil {
					return nil, errors.New("PocketBase returned invalid rows")
				}
			}
			return map[string]any{"rows": rows}, nil
		},
	}
}

func publicQueryReadRowsError(err error) error {
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
