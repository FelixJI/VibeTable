package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/query"
)

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
	aggregate := query.AggregateQuery{}
	for name, input := range map[string]queryOperationRequest{
		"page": {
			Operation: "page", TableID: "orders", Query: &page,
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
