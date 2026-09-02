package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

const schemaListWire = `{"scope":"workspace","workspaceId":"11111111-1111-4111-8111-111111111111","sessionEpoch":7,"operationId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","sequence":1}`

func TestSchemaListProductHTTPMatchesRealCatalogREST(t *testing.T) {
	pb := schemaProductStore(t)
	mux := schemaProductMux(t, schemaapi.New(pb))
	assertSchemaListParity(t, mux, `{"tables":[]}`)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for index, name := range []string{"订单 📦", "客户 é", "日本語"} {
		receipt, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{
			DisplayName: name, OperationID: fmt.Sprintf("schema-list-%d", index),
			Actor: v2.Actor{ID: "local-user", Kind: "user"},
		})
		if err != nil {
			t.Fatal(err)
		}
		names[receipt.TableID] = name
	}
	// Read the persisted physical names, independently of the Catalog projection.
	records, err := pb.FindAllRecords("vibetable_tables")
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].GetString("physical_name") < records[j].GetString("physical_name")
	})
	tables := []map[string]string{}
	for _, record := range records {
		id := record.GetString("table_id")
		tables = append(tables, map[string]string{"tableId": id, "displayName": names[id], "kind": "base"})
	}
	expected, err := json.Marshal(map[string]any{"tables": tables})
	if err != nil {
		t.Fatal(err)
	}
	assertSchemaListParity(t, mux, string(expected))
}

func TestSchemaListProductHTTPRejectsInvalidParamsAndStaleScopeBeforeStorage(t *testing.T) {
	pb := schemaProductStore(t)
	mux := schemaProductMux(t, schemaapi.New(pb))
	if _, err := pb.DB().NewQuery("DROP TABLE vibetable_tables").Execute(); err != nil {
		t.Fatal(err)
	}
	for _, params := range []string{`null`, `[]`, `true`, `7`, `"text"`, `{"extra":true}`} {
		t.Run(params, func(t *testing.T) {
			response := schemaProductRequest(t, mux, context.Background(), params, schemaListWire)
			code := productrpc.CodeInvalidRequest
			if strings.HasPrefix(params, "{") {
				code = productrpc.CodeInvalidParams
			}
			if response.Error == nil || response.Error.Code != code {
				t.Fatalf("invalid params reached storage: %+v", response)
			}
		})
	}
	for _, wire := range []string{
		strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":8`, 1),
		strings.Replace(schemaListWire, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1),
		`{"scope":"global","operationId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","sequence":1}`,
	} {
		response := schemaProductRequest(t, mux, context.Background(), `{}`, wire)
		if response.Error == nil || response.Error.Code != productrpc.CodeInvalidRequest {
			t.Fatalf("stale scope reached storage: %+v", response)
		}
	}
}

func TestSchemaListProductHTTPPreservesPublicStorageErrorAndCancellation(t *testing.T) {
	pb := schemaProductStore(t)
	mux := schemaProductMux(t, schemaapi.New(pb))
	if _, err := pb.DB().NewQuery("DROP TABLE vibetable_tables").Execute(); err != nil {
		t.Fatal(err)
	}
	response := schemaProductRequest(t, mux, context.Background(), `{}`, schemaListWire)
	want := &productrpc.ErrorObject{
		Code: productrpc.CodeProductData, Message: "Product data error",
		Data: map[string]any{
			"kind": "product_data_error", "code": "schema.storage.failed", "path": "",
			"message": "schema storage operation failed", "details": map[string]any{}, "retryable": false,
		},
	}
	if !reflect.DeepEqual(response.Error, want) {
		t.Fatalf("storage error exposed or changed: %+v", response.Error)
	}
	rest := httptest.NewRecorder()
	mux.ServeHTTP(rest, httptest.NewRequest(http.MethodGet, "/api/vibetable/v2/schema/tables", nil))
	var restError map[string]any
	if err := json.Unmarshal(rest.Body.Bytes(), &restError); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"code", "path", "message", "details", "retryable"} {
		if !reflect.DeepEqual(response.Error.Data[key], restError[key]) {
			t.Fatalf("error parity %s: Product=%v REST=%v", key, response.Error.Data, restError)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response = schemaProductRequest(t, mux, ctx, `{}`, schemaListWire)
	// The real Catalog sees cancellation before the broken store; it cannot publish success.
	if response.Error == nil || response.Error.Code != productrpc.CodeInternalError || response.Error.Data != nil {
		t.Fatalf("canceled catalog read = %+v", response)
	}
}

func schemaProductStore(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	pb := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir(), HideStartBanner: true})
	migrations.Register(pb)
	if err := pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pb.ResetBootstrapState(); err != nil {
			t.Error(err)
		}
	})
	if err := pb.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return pb
}

func assertSchemaListParity(t *testing.T, mux http.Handler, expected string) {
	t.Helper()
	rest := httptest.NewRecorder()
	mux.ServeHTTP(rest, httptest.NewRequest(http.MethodGet, "/api/vibetable/v2/schema/tables", nil))
	if rest.Code != http.StatusOK {
		t.Fatalf("REST catalog = %d %s", rest.Code, rest.Body)
	}
	envelope := schemaProductRequest(t, mux, context.Background(), `{}`, schemaListWire)
	var got, restResult, want any
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("Product HTTP must return the REST catalog: %+v (%v)", envelope, err)
	}
	if err := json.Unmarshal(rest.Body.Bytes(), &restResult); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal(err)
	}
	if envelope.Error != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(got, restResult) {
		t.Fatalf("Product=%s REST=%s want=%s", envelope.Result, rest.Body, expected)
	}
}

func schemaProductRequest(t *testing.T, mux http.Handler, ctx context.Context, params, wire string) productrpc.ResponseEnvelope {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, productRPCPath, bytes.NewBufferString(
		`{"jsonrpc":"2.0","id":"schema-list","method":"schema.list","wire":`+wire+`,"params":`+params+`}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(response, request)
	var envelope productrpc.ResponseEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	expectedStatus := http.StatusOK
	if envelope.Error != nil && envelope.Error.Code == productrpc.CodeInvalidRequest {
		expectedStatus = http.StatusBadRequest
	}
	if response.Code != expectedStatus || string(envelope.ID) != `"schema-list"` || string(envelope.Wire) != wire {
		t.Fatalf("Product envelope changed: %d %s", response.Code, response.Body)
	}
	return envelope
}

func schemaProductMux(t *testing.T, catalog schemaapi.SchemaCatalog) http.Handler {
	t.Helper()
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}, schemaListRegistration(catalog))
	if err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: request}}, nil
	})
	registerSchemaRoutes(r, catalog, nil)
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	return mux
}
