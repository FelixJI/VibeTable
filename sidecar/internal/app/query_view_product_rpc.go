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

func queryViewRegistration(port interface {
	ExecuteViewQuery(context.Context, string, query.ViewQuery) (query.ViewResult, error)
}) productrpc.Registration {
	return productrpc.Registration{
		Method: "query.view", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			_, err := decodeQueryViewParams(raw)
			return err
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			object, err := decodeQueryViewParams(raw)
			if err != nil {
				return nil, err
			}
			object["operation"] = "view"
			var body strings.Builder
			if err := appendDescribeRevision(&body, object); err != nil {
				return nil, err
			}
			var input queryOperationRequest
			if err := decodeQueryRequest(strings.NewReader(body.String()), &input); err != nil {
				return nil, publicQueryViewError(err)
			}
			if strings.TrimSpace(input.TableID) == "" {
				return nil, publicQueryViewError(invalidQueryRequest("tableId", "tableId is required"))
			}
			if err := validateQueryOperation(input); err != nil {
				return nil, publicQueryViewError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			projection, err := port.ExecuteViewQuery(ctx, input.TableID, *input.View)
			if err != nil {
				return nil, publicQueryViewError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if projection.Page.Rows == nil || projection.GroupRows == nil {
				return nil, errors.New("PocketBase returned an invalid view query result")
			}
			for _, row := range projection.Page.Rows {
				if row == nil {
					return nil, errors.New("PocketBase returned an invalid query page")
				}
			}
			for _, row := range projection.GroupRows {
				if row.Key == nil || row.Summaries == nil ||
					(row.ParentCount != nil) != (len(row.ParentSummaries) != 0) ||
					(row.ParentCount != nil && len(row.Key) != 2) {
					return nil, errors.New("PocketBase returned an invalid view group row")
				}
			}
			return map[string]any{
				"page": map[string]any{
					"rows": projection.Page.Rows, "offset": projection.Page.Offset,
					"limit": projection.Page.Limit, "filteredRows": projection.Page.FilteredRows,
					"totalRows": projection.Page.TotalRows, "snapshot": projection.Page.Snapshot,
				},
				"groupRows": projection.GroupRows, "groupOffset": projection.GroupOffset,
				"groupLimit": projection.GroupLimit, "hasMoreGroups": projection.HasMoreGroups,
			}, nil
		},
	}
}

func decodeQueryViewParams(raw json.RawMessage) (map[string]any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, errors.New("invalid query view JSON")
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
			return nil, errors.New("query view parameters require Unicode scalar values")
		}
	}
	if err := validateQueryViewValue(value, 0); err != nil {
		return nil, err
	}
	var compact strings.Builder
	if err := appendDescribeRevision(&compact, value); err != nil {
		return nil, err
	}
	if compact.Len() > maxQueryRequestBytes {
		return nil, errors.New("query view parameters exceed 1 MiB")
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 2 {
		return nil, errors.New("query.view requires exactly tableId and view")
	}
	table, tableOK := object["tableId"].(string)
	_, queryOK := object["view"].(map[string]any)
	if !tableOK || table == "" || !queryOK {
		return nil, errors.New("query.view requires non-empty tableId text and a view object")
	}
	return object, nil
}

func validateQueryViewValue(value any, depth int) error {
	if depth > 32 {
		return errors.New("query view parameters are too deeply nested")
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			switch key {
			case "accessToken", "legacyToken", "password", "pocketBaseToken", "refreshToken", "sessionSecret":
				return errors.New("query view parameters contain a forbidden field")
			}
			if err := validateQueryViewValue(item, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := validateQueryViewValue(item, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func publicQueryViewError(err error) error {
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
