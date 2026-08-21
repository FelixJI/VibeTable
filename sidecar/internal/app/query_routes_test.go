package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestSelectionOpenRouteReturnsProjectionAndMapsProductError(t *testing.T) {
	matched := query.SelectionProjection{
		SchemaSnapshot: v2.SchemaSnapshot{
			Contract: v2.Contract, TableID: "orders", DisplayName: "Orders", Kind: "base",
			SchemaRevision: "schema_0001", DataRevision: 1,
			ArchivePolicy: v2.ArchivePolicy{Mode: "none"},
		},
		CursorWindow: query.CursorWindow{Snapshot: query.QuerySnapshot{
			Table: "orders", SchemaRevision: "schema_0001", DataRevision: 1,
			NormalizedQuery: query.TableQuery{Limit: 10},
		}},
	}
	for _, testCase := range []struct {
		name       string
		port       *selectionRoutePort
		wantStatus int
		wantCode   string
	}{
		{"success", &selectionRoutePort{projection: matched}, http.StatusOK, ""},
		{"product error", &selectionRoutePort{err: &query.ProductError{
			Code: "query.schema.not_found", Path: "table", Message: "missing",
		}}, http.StatusNotFound, "query.schema.not_found"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			r := router.NewRouter(func(
				writer http.ResponseWriter,
				request *http.Request,
			) (*core.RequestEvent, router.EventCleanupFunc) {
				return &core.RequestEvent{Event: router.Event{
					Response: writer,
					Request:  request,
				}}, nil
			})
			registerQueryRoutes(r, testCase.port)
			mux, err := r.BuildMux()
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/vibetable/v1/query",
				bytes.NewBufferString(
					`{"operation":"selection.open","tableId":"orders","query":{"limit":10}}`,
				),
			)
			response := httptest.NewRecorder()

			mux.ServeHTTP(response, request)

			if response.Code != testCase.wantStatus || testCase.port.calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, testCase.port.calls, response.Body)
			}
			if testCase.wantCode == "" {
				var projection query.SelectionProjection
				if err := json.Unmarshal(response.Body.Bytes(), &projection); err != nil ||
					projection.SchemaSnapshot.SchemaRevision != "schema_0001" {
					t.Fatalf("projection=%#v err=%v", projection, err)
				}
			} else {
				var productErr query.ProductError
				if err := json.Unmarshal(response.Body.Bytes(), &productErr); err != nil ||
					productErr.Code != testCase.wantCode {
					t.Fatalf("product error=%#v err=%v", productErr, err)
				}
			}
		})
	}
}

type selectionRoutePort struct {
	projection query.SelectionProjection
	err        error
	calls      int
}

func (port *selectionRoutePort) OpenSelectionProjection(
	context.Context,
	string,
	query.TableQuery,
) (query.SelectionProjection, error) {
	port.calls++
	return port.projection, port.err
}

func (*selectionRoutePort) QueryPage(context.Context, string, query.TableQuery) (query.Page, error) {
	return query.Page{}, nil
}

func (*selectionRoutePort) OpenCursor(context.Context, string, query.TableQuery) (query.CursorWindow, error) {
	return query.CursorWindow{}, nil
}

func (*selectionRoutePort) FetchCursor(context.Context, string) (query.CursorWindow, error) {
	return query.CursorWindow{}, nil
}

func (*selectionRoutePort) ExecuteViewQuery(context.Context, string, query.ViewQuery) (query.ViewResult, error) {
	return query.ViewResult{}, nil
}

func (*selectionRoutePort) ReadRows(context.Context, string, []string) ([]map[string]any, error) {
	return nil, nil
}

func (*selectionRoutePort) Aggregate(context.Context, string, query.AggregateQuery) (query.AggregateResult, error) {
	return query.AggregateResult{}, nil
}

func (*selectionRoutePort) ValidateSnapshot(
	context.Context,
	query.QuerySnapshot,
	*query.TableQuery,
) (query.SnapshotValidation, error) {
	return query.SnapshotValidation{}, nil
}

func TestDecodeQueryRequestIsStrictAndBounded(t *testing.T) {
	var valid queryOperationRequest
	if err := decodeQueryRequest(strings.NewReader(
		`{"operation":"page","tableId":"orders","query":{"offset":0,"limit":10}}`,
	), &valid); err != nil {
		t.Fatalf("decode valid request: %v", err)
	}
	if valid.Query == nil || valid.Query.Limit != 10 {
		t.Fatalf("decoded request = %#v", valid)
	}

	for name, body := range map[string]string{
		"unknown field": `{"operation":"page","tableId":"orders","query":{},"rawSql":"x"}`,
		"trailing":      `{"operation":"page","tableId":"orders","query":{}} {}`,
		"empty":         ``,
		"oversized":     `"` + strings.Repeat("x", maxQueryRequestBytes) + `"`,
	} {
		t.Run(name, func(t *testing.T) {
			var input queryOperationRequest
			err := decodeQueryRequest(strings.NewReader(body), &input)
			var productErr *query.ProductError
			if !errors.As(err, &productErr) ||
				productErr.Code != "query.request.invalid" {
				t.Fatalf("error = %#v, want query.request.invalid", err)
			}
		})
	}
}

func TestDecodeQueryRequestPreservesLargeNumbers(t *testing.T) {
	var input queryOperationRequest
	err := decodeQueryRequest(strings.NewReader(
		`{"operation":"page","tableId":"orders","query":{"filters":[`+
			`{"field":"amount","operator":"eq","value":9007199254740993}],`+
			`"offset":0,"limit":10}}`,
	), &input)
	if err != nil {
		t.Fatalf("decode request: %v", err)
	}
	value := input.Query.Filters[0].Value
	if value != json.Number("9007199254740993") {
		t.Fatalf("large number decoded as %#v (%T)", value, value)
	}
	plan, err := query.Compile(query.TableDescriptor{
		PhysicalName: "orders",
		PrimaryKey:   "id",
		Fields: map[string]query.FieldDescriptor{
			"id":     {PhysicalName: "id", Type: query.FieldTypeText},
			"amount": {PhysicalName: "amount", Type: query.FieldTypeNumber},
		},
	}, *input.Query)
	if err != nil {
		t.Fatalf("compile decoded query: %v", err)
	}
	if got := plan.Params["p0"]; got != int64(9007199254740993) {
		t.Fatalf("large number bound as %#v (%T)", got, got)
	}
}

func TestDecodeQueryRequestRejectsExplicitInvalidTableQueryDefaults(t *testing.T) {
	for name, queryJSON := range map[string]string{
		"zero limit":        `{"limit":0}`,
		"null filters":      `{"filters":null,"limit":10}`,
		"null sorts":        `{"sorts":null,"limit":10}`,
		"null nulls last":   `{"sorts":[{"field":"name","nullsLast":null}],"limit":10}`,
		"unknown query key": `{"rawSql":"select 1","limit":10}`,
	} {
		t.Run(name, func(t *testing.T) {
			var input queryOperationRequest
			err := decodeQueryRequest(strings.NewReader(
				`{"operation":"page","tableId":"orders","query":`+
					queryJSON+`}`,
			), &input)
			var productErr *query.ProductError
			if !errors.As(err, &productErr) ||
				productErr.Code != "query.request.invalid" {
				t.Fatalf("error = %#v, want query.request.invalid", err)
			}
		})
	}
}

func TestDecodeQueryRequestAppliesOnlyOmittedTableQueryDefaults(t *testing.T) {
	var input queryOperationRequest
	if err := decodeQueryRequest(strings.NewReader(
		`{"operation":"page","tableId":"orders","query":{"sorts":[{"field":"name"}]}}`,
	), &input); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if input.Query.Limit != 100 ||
		len(input.Query.Filters) != 0 ||
		len(input.Query.Sorts) != 1 ||
		input.Query.Sorts[0].NullsLast == nil ||
		!*input.Query.Sorts[0].NullsLast {
		t.Fatalf("omitted defaults were not materialized: %#v", input.Query)
	}
}

func TestValidateQueryOperationRequiresExactlyOnePayload(t *testing.T) {
	page := query.TableQuery{}
	view := query.ViewQuery{}
	aggregate := query.AggregateQuery{}
	cursor := "opaque-cursor"
	for name, input := range map[string]queryOperationRequest{
		"view": {
			Operation: "view", TableID: "orders", View: &view,
		},
		"page": {
			Operation: "page", TableID: "orders", Query: &page,
		},
		"cursor open": {
			Operation: "cursor.open", TableID: "orders", Query: &page,
		},
		"selection open": {
			Operation: "selection.open", TableID: "orders", Query: &page,
		},
		"cursor fetch": {
			Operation: "cursor.fetch", Cursor: &cursor,
		},
		"read rows": {
			Operation: "readRows", TableID: "orders", RowIDs: []string{},
		},
		"aggregate": {
			Operation: "aggregate", TableID: "orders", Aggregate: &aggregate,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateQueryOperation(input); err != nil {
				t.Fatalf("validate operation: %v", err)
			}
		})
	}

	for name, input := range map[string]queryOperationRequest{
		"unknown": {Operation: "sql"},
		"missing page query": {
			Operation: "page",
		},
		"mixed page payload": {
			Operation: "page", Query: &page, Aggregate: &aggregate,
		},
		"missing cursor open query": {
			Operation: "cursor.open",
		},
		"mixed cursor open payload": {
			Operation: "cursor.open", Query: &page, Cursor: &cursor,
		},
		"mixed selection open payload": {
			Operation: "selection.open", Query: &page, Cursor: &cursor,
		},
		"missing cursor fetch token": {
			Operation: "cursor.fetch",
		},
		"cursor fetch with table": {
			Operation: "cursor.fetch", TableID: "orders", Cursor: &cursor,
		},
		"mixed view payload": {
			Operation: "view", View: &view, Query: &page,
		},
		"missing view": {
			Operation: "view",
		},
		"missing row ids": {
			Operation: "readRows",
		},
		"missing aggregate": {
			Operation: "aggregate",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateQueryOperation(input)
			var productErr *query.ProductError
			if !errors.As(err, &productErr) ||
				productErr.Code != "query.request.invalid" {
				t.Fatalf("error = %#v, want query.request.invalid", err)
			}
		})
	}
}
