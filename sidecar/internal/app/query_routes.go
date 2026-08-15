package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/query"
)

const maxQueryRequestBytes = 1 << 20

type queryOperationRequest struct {
	Operation string                `json:"operation"`
	TableID   string                `json:"tableId"`
	Query     *query.TableQuery     `json:"query,omitempty"`
	View      *query.ViewQuery      `json:"view,omitempty"`
	RowIDs    []string              `json:"rowIds,omitempty"`
	Aggregate *query.AggregateQuery `json:"aggregate,omitempty"`
	Cursor    *string               `json:"cursor,omitempty"`
}

type snapshotValidationRequest struct {
	Snapshot     query.QuerySnapshot `json:"snapshot"`
	CurrentQuery *query.TableQuery   `json:"currentQuery,omitempty"`
}

func registerQueryRoutes(
	r *router.Router[*core.RequestEvent],
	port query.QueryPort,
) {
	r.POST("/api/vibetable/v1/query", func(request *core.RequestEvent) error {
		var input queryOperationRequest
		if err := decodeQueryRequest(request.Request.Body, &input); err != nil {
			return writeQueryError(request, err)
		}
		if input.Operation != "cursor.fetch" && strings.TrimSpace(input.TableID) == "" {
			return writeQueryError(request, invalidQueryRequest(
				"tableId", "tableId is required",
			))
		}
		if err := validateQueryOperation(input); err != nil {
			return writeQueryError(request, err)
		}

		switch input.Operation {
		case "view":
			result, err := port.ExecuteViewQuery(
				request.Request.Context(), input.TableID, *input.View,
			)
			if err != nil {
				return writeQueryError(request, err)
			}
			return request.JSON(http.StatusOK, result)
		case "page":
			result, err := port.QueryPage(
				request.Request.Context(), input.TableID, *input.Query,
			)
			if err != nil {
				return writeQueryError(request, err)
			}
			return request.JSON(http.StatusOK, result)
		case "cursor.open":
			result, err := port.OpenCursor(
				request.Request.Context(), input.TableID, *input.Query,
			)
			if err != nil {
				return writeQueryError(request, err)
			}
			return request.JSON(http.StatusOK, result)
		case "cursor.fetch":
			result, err := port.FetchCursor(request.Request.Context(), *input.Cursor)
			if err != nil {
				return writeQueryError(request, err)
			}
			return request.JSON(http.StatusOK, result)
		case "readRows":
			result, err := port.ReadRows(
				request.Request.Context(), input.TableID, input.RowIDs,
			)
			if err != nil {
				return writeQueryError(request, err)
			}
			return request.JSON(http.StatusOK, map[string]any{"rows": result})
		case "aggregate":
			result, err := port.Aggregate(
				request.Request.Context(), input.TableID, *input.Aggregate,
			)
			if err != nil {
				return writeQueryError(request, err)
			}
			return request.JSON(http.StatusOK, result)
		}
		return writeQueryError(request, invalidQueryRequest(
			"operation", "operation must be view, page, cursor.open, cursor.fetch, readRows, or aggregate",
		))
	})

	r.POST(
		"/api/vibetable/v1/query/validate-snapshot",
		func(request *core.RequestEvent) error {
			var input snapshotValidationRequest
			if err := decodeQueryRequest(request.Request.Body, &input); err != nil {
				return writeQueryError(request, err)
			}
			result, err := port.ValidateSnapshot(
				request.Request.Context(), input.Snapshot, input.CurrentQuery,
			)
			if err != nil {
				return writeQueryError(request, err)
			}
			return request.JSON(http.StatusOK, result)
		},
	)
}

func validateQueryOperation(input queryOperationRequest) error {
	switch input.Operation {
	case "view":
		if input.View == nil || input.Query != nil || input.Aggregate != nil || input.RowIDs != nil || input.Cursor != nil {
			return invalidQueryRequest("operation", "view requires only view")
		}
	case "page":
		if input.Query == nil || input.View != nil || input.Aggregate != nil || input.RowIDs != nil || input.Cursor != nil {
			return invalidQueryRequest("operation", "page requires only query")
		}
	case "cursor.open":
		if input.Query == nil || input.View != nil || input.Aggregate != nil || input.RowIDs != nil || input.Cursor != nil {
			return invalidQueryRequest("operation", "cursor.open requires only query")
		}
	case "cursor.fetch":
		if input.Cursor == nil || strings.TrimSpace(*input.Cursor) == "" || input.TableID != "" ||
			input.Query != nil || input.View != nil || input.Aggregate != nil || input.RowIDs != nil {
			return invalidQueryRequest("operation", "cursor.fetch requires only cursor")
		}
	case "readRows":
		if input.Query != nil || input.View != nil || input.Aggregate != nil || input.RowIDs == nil || input.Cursor != nil {
			return invalidQueryRequest("operation", "readRows requires only rowIds")
		}
	case "aggregate":
		if input.Query != nil || input.View != nil || input.Aggregate == nil || input.RowIDs != nil || input.Cursor != nil {
			return invalidQueryRequest("operation", "aggregate requires only aggregate")
		}
	default:
		return invalidQueryRequest(
			"operation", "operation must be view, page, cursor.open, cursor.fetch, readRows, or aggregate",
		)
	}
	return nil
}

func decodeQueryRequest(reader io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(reader, maxQueryRequestBytes+1))
	if err != nil {
		return invalidQueryRequest("", "query request body is invalid")
	}
	if len(raw) > maxQueryRequestBytes {
		return invalidQueryRequest("", "query request exceeds the 1 MiB limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return invalidQueryRequest("", "query request body is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return invalidQueryRequest("", "query request must contain one JSON value")
	}
	return nil
}

func invalidQueryRequest(path, message string) *query.ProductError {
	return &query.ProductError{
		Code: "query.request.invalid", Path: path, Message: message,
	}
}

func writeQueryError(request *core.RequestEvent, err error) error {
	if cancellation := queryContextError(err); cancellation != nil {
		return cancellation
	}
	var productError *query.ProductError
	if !errors.As(err, &productError) {
		productError = &query.ProductError{
			Code: "query.internal.failed", Message: "query operation failed",
		}
	}
	status := http.StatusUnprocessableEntity
	switch {
	case productError.Code == "query.request.invalid":
		status = http.StatusBadRequest
	case strings.HasSuffix(productError.Code, ".not_found"):
		status = http.StatusNotFound
	case productError.Code == "query.storage.failed" ||
		productError.Code == "query.internal.failed" ||
		productError.Code == "query.port.unconfigured":
		status = http.StatusInternalServerError
	}
	if err := request.JSON(status, productError); err != nil {
		return fmt.Errorf("write query error response: %w", err)
	}
	return nil
}

func queryContextError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return nil
	}
}
