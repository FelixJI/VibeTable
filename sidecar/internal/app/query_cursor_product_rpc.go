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

func queryCursorOpenRegistration(port interface {
	OpenCursor(context.Context, string, query.TableQuery) (query.CursorWindow, error)
}) productrpc.Registration {
	return queryCursorRegistration("query.cursorOpen", "cursor.open", func(ctx context.Context, input queryOperationRequest) (query.CursorWindow, error) {
		return port.OpenCursor(ctx, input.TableID, *input.Query)
	})
}

func queryCursorFetchRegistration(port interface {
	FetchCursor(context.Context, string) (query.CursorWindow, error)
}) productrpc.Registration {
	return queryCursorRegistration("query.cursorFetch", "cursor.fetch", func(ctx context.Context, input queryOperationRequest) (query.CursorWindow, error) {
		return port.FetchCursor(ctx, *input.Cursor)
	})
}

func queryCursorRegistration(method, operation string, invoke func(context.Context, queryOperationRequest) (query.CursorWindow, error)) productrpc.Registration {
	return productrpc.Registration{
		Method: method, Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeQueryCursorParams(raw, operation); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			object, err := decodeQueryCursorParams(raw, operation)
			if err != nil {
				return nil, err
			}
			// Preserve the former Python-to-REST body budget and typed defaults.
			object["operation"] = operation
			var body strings.Builder
			if err := appendDescribeRevision(&body, object); err != nil {
				return nil, err
			}
			var input queryOperationRequest
			if err := decodeQueryRequest(strings.NewReader(body.String()), &input); err != nil {
				return nil, publicQueryCursorError(err)
			}
			if operation != "cursor.fetch" && strings.TrimSpace(input.TableID) == "" {
				return nil, publicQueryCursorError(invalidQueryRequest("tableId", "tableId is required"))
			}
			if err := validateQueryOperation(input); err != nil {
				return nil, publicQueryCursorError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			window, err := invoke(ctx, input)
			if err != nil {
				return nil, publicQueryCursorError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// Typed counters and snapshots retain the Python projection shape. The
			// remaining invariants are not guaranteed by the Go DTO's field types.
			if window.Rows == nil || window.HasMore != (window.NextCursor != nil) {
				return nil, errors.New("PocketBase returned an invalid query cursor window")
			}
			for _, row := range window.Rows {
				if row == nil {
					return nil, errors.New("PocketBase returned an invalid query cursor window")
				}
			}
			return window, nil
		},
	}
}

// Product validation precedes the former REST boundary, retaining its error class.
func decodeQueryCursorParams(raw json.RawMessage, operation string) (map[string]any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, errors.New("invalid query cursor JSON")
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
			return nil, errors.New("query cursor parameters require Unicode scalar values")
		}
	}
	if err := validateQueryCursorValue(value, 0); err != nil {
		return nil, err
	}
	var compact strings.Builder
	// Reuse the existing Python compact UTF-8 serializer, including numeric form.
	if err := appendDescribeRevision(&compact, value); err != nil {
		return nil, err
	}
	if compact.Len() > maxQueryRequestBytes {
		return nil, errors.New("query cursor parameters exceed 1 MiB")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("query cursor parameters must be an object")
	}
	if operation == "cursor.fetch" {
		cursor, ok := object["cursor"].(string)
		if len(object) != 1 || !ok || cursor == "" {
			return nil, errors.New("query.cursorFetch requires non-empty cursor text")
		}
	} else {
		table, tableOK := object["tableId"].(string)
		_, queryOK := object["query"].(map[string]any)
		if len(object) != 2 || !tableOK || table == "" || !queryOK {
			return nil, errors.New("query.cursorOpen requires non-empty tableId text and a query object")
		}
	}
	return object, nil
}

func validateQueryCursorValue(value any, depth int) error {
	if depth > 32 {
		return errors.New("query cursor parameters are too deeply nested")
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			switch key {
			case "accessToken", "legacyToken", "password", "pocketBaseToken", "refreshToken", "sessionSecret":
				return errors.New("query cursor parameters contain a forbidden field")
			}
			if err := validateQueryCursorValue(item, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := validateQueryCursorValue(item, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func publicQueryCursorError(err error) error {
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
