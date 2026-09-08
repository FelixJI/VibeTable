package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"

	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
)

func TestSchemaDescribeProductHTTPReadsAuthoritativeTable(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{
		DisplayName: "订单 📦 é", OperationID: "describe-product-table",
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := schemaProductMux(t, pb)
	for _, generation := range []string{"1", "-1", "-0", "900719925474099312345"} {
		response := schemaProductRequestForMethod(t, mux, context.Background(), "schema.describe", fmt.Sprintf(
			`{"collection":%q,"requestGeneration":%s,"accepts":["vibetable.relation-capabilities.v1","vibetable.lookup-query.v1"]}`,
			table.TableID, generation,
		), schemaListWire)
		if response.Error != nil {
			t.Fatalf("schema.describe error: %#v", response.Error)
		}
		var result struct {
			Contract   string      `json:"contract"`
			Collection string      `json:"collection"`
			Generation json.Number `json:"requestGeneration"`
			Schema     struct {
				Collection     string           `json:"collection"`
				Columns        []map[string]any `json:"columns"`
				SchemaRevision string           `json:"schemaRevision"`
				LookupRevision string           `json:"lookupRevision"`
			} `json:"schema"`
		}
		if err := json.Unmarshal(response.Result, &result); err != nil {
			t.Fatal(err)
		}
		wantGeneration := generation
		if generation == "-0" {
			wantGeneration = "0"
		}
		if result.Contract != "vibetable.schema-describe.v1" || result.Collection != table.TableID || result.Schema.Collection != table.TableID || result.Generation.String() != wantGeneration {
			t.Fatalf("describe identity/generation: %#v", result)
		}
		if len(result.Schema.Columns) != 1 || result.Schema.Columns[0]["fieldId"] != "id" || result.Schema.Columns[0]["editable"] != false || result.Schema.SchemaRevision == "" || result.Schema.LookupRevision == "" {
			t.Fatalf("empty authoritative table projection: %#v", result.Schema)
		}
	}
}

func TestSchemaDescribeProductHTTPRejectsClosedParams(t *testing.T) {
	mux := schemaProductMux(t, schemaProductStore(t))
	for _, params := range []string{
		`{}`, `null`, `[]`,
		`{"collection":"orders","requestGeneration":1,"accepts":[],"extra":1}`,
		`{"collection":"","requestGeneration":1,"accepts":[]}`,
		`{"collection":null,"requestGeneration":1,"accepts":[]}`,
		`{"collection":"orders","requestGeneration":true,"accepts":[]}`,
		`{"collection":"orders","requestGeneration":"1","accepts":[]}`,
		`{"collection":"orders","requestGeneration":1.0,"accepts":[]}`,
		`{"collection":"orders","requestGeneration":1e0,"accepts":[]}`,
		`{"collection":"orders","requestGeneration":1,"accepts":null}`,
	} {
		response := schemaProductRequestForMethod(t, mux, context.Background(), "schema.describe", params, schemaListWire)
		wantCode := productrpc.CodeInvalidParams
		if params == "null" || params == "[]" {
			wantCode = productrpc.CodeInvalidRequest
		}
		if response.Error == nil || response.Error.Code != wantCode {
			t.Fatalf("params=%s error=%#v", params, response.Error)
		}
	}
}

func TestSchemaDescribeProductHTTPPreservesValidationAndMissingTableErrors(t *testing.T) {
	mux := schemaProductMux(t, schemaProductStore(t))
	for _, tableID := range []string{"../orders", "orders?admin=true", "orders#fragment", "%2fadmin", " "} {
		params := fmt.Sprintf(`{"collection":%q,"requestGeneration":1,"accepts":["vibetable.relation-capabilities.v1","vibetable.lookup-query.v1"]}`, tableID)
		response := schemaProductRequestForMethod(t, mux, context.Background(), "schema.describe", params, schemaListWire)
		if response.Error == nil || response.Error.Code != productrpc.CodeInternalError || response.Error.Data != nil {
			t.Fatalf("path validation changed: table=%q response=%#v", tableID, response)
		}
	}
	for _, accepts := range []string{`[]`, `[1,true]`, `["vibetable.lookup-query.v1","vibetable.relation-capabilities.v1"]`} {
		params := fmt.Sprintf(`{"collection":"orders","requestGeneration":1,"accepts":%s}`, accepts)
		response := schemaProductRequestForMethod(t, mux, context.Background(), "schema.describe", params, schemaListWire)
		if response.Error == nil || response.Error.Code != productrpc.CodeInternalError || response.Error.Data != nil {
			t.Fatalf("accepts handler error changed: accepts=%s response=%#v", accepts, response)
		}
	}
	params := `{"collection":"tbl_missing","requestGeneration":1,"accepts":["vibetable.relation-capabilities.v1","vibetable.lookup-query.v1"]}`
	missing := schemaProductRequestForMethod(t, mux, context.Background(), "schema.describe", params, schemaListWire)
	if missing.Error == nil || missing.Error.Code != productrpc.CodeProductData || missing.Error.Data["code"] != "schema.table.not_found" || missing.Error.Data["path"] != "tableId" {
		t.Fatalf("missing table public error changed: %#v", missing)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := schemaProductRequestForMethod(t, mux, ctx, "schema.describe", params, schemaListWire)
	if cancelled.Error == nil || cancelled.Error.Code != productrpc.CodeInternalError || len(cancelled.Result) != 0 {
		t.Fatalf("cancelled describe returned data: %#v", cancelled)
	}
}

func TestSchemaDescribeFilterFamiliesRetainQueryOperators(t *testing.T) {
	for _, sample := range []struct {
		kinds []v2.LogicalType
		want  []string
	}{
		{[]v2.LogicalType{v2.LogicalEditor, v2.LogicalEmail, v2.LogicalURL}, []string{"eq", "ne", "in", "contains", "starts_with", "ends_with", "is_null", "is_not_null"}},
		{[]v2.LogicalType{v2.LogicalDateTime, v2.LogicalAutoDate}, []string{"eq", "ne", "in", "gt", "lt", "gte", "lte", "between", "is_null", "is_not_null"}},
		{[]v2.LogicalType{v2.LogicalGeoPoint}, []string{"contains", "is_null", "is_not_null"}},
	} {
		for _, kind := range sample.kinds {
			got, err := describeFilterOperators(kind)
			if err != nil || !reflect.DeepEqual(got, sample.want) {
				t.Fatalf("operators %s=%v err=%v", kind, got, err)
			}
		}
	}
}

func TestSchemaDescribeAndLookupListProductHTTPCancelAfterCatalogRead(t *testing.T) {
	for _, method := range []string{"schema.describe", "lookup.list"} {
		t.Run(method, func(t *testing.T) {
			pb := schemaProductStore(t)
			lifecycle, err := schemacore.NewTableLifecycle(pb)
			if err != nil {
				t.Fatal(err)
			}
			table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{
				DisplayName: "取消读取", OperationID: "describe-cancel-table",
				Actor: v2.Actor{ID: "local-user", Kind: "user"},
			})
			if err != nil {
				t.Fatal(err)
			}
			mux := schemaProductMux(t, pb)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db := pb.ConcurrentDB().(*dbx.DB)
			original := db.QueryLogFunc
			defer func() { db.QueryLogFunc = original }()
			cancelledAfterRead := false
			db.QueryLogFunc = func(queryCtx context.Context, elapsed time.Duration, query string, rows *sql.Rows, queryErr error) {
				if original != nil {
					original(queryCtx, elapsed, query, rows, queryErr)
				}
				if queryErr == nil && strings.Contains(query, "FROM `vibetable_lookups`") {
					cancelledAfterRead = true
					cancel()
				}
			}
			params := fmt.Sprintf(`{"collection":%q,"requestGeneration":1,"accepts":["vibetable.relation-capabilities.v1","vibetable.lookup-query.v1"]}`, table.TableID)
			if method == "lookup.list" {
				params = fmt.Sprintf(`{"collection":%q}`, table.TableID)
			}
			response := schemaProductRequestForMethod(t, mux, ctx, method, params, schemaListWire)
			if !cancelledAfterRead {
				t.Fatal("catalog read cancellation seam was not reached")
			}
			if response.Error == nil || response.Error.Code != productrpc.CodeInternalError || len(response.Result) != 0 {
				t.Fatalf("cancelled catalog read: error=%#v result bytes=%d", response.Error, len(response.Result))
			}
		})
	}
}
