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
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

const schemaListWire = `{"scope":"workspace","workspaceId":"11111111-1111-4111-8111-111111111111","sessionEpoch":7,"operationId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","sequence":1}`

func TestSchemaListProductHTTPMatchesRealCatalogREST(t *testing.T) {
	pb := schemaProductStore(t)
	mux := schemaProductMux(t, pb)
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

func TestSchemaGetTableProductHTTPMatchesPythonSnapshotProjection(t *testing.T) {
	pb := schemaProductStore(t)
	mux := schemaProductMux(t, pb)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{
		DisplayName: "订单 📦", OperationID: "schema-get-table",
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	file := createSchemaProductField(t, pb, receipt.TableID, v2.LogicalFile, "客户 é", "schema-file")

	rest := httptest.NewRecorder()
	mux.ServeHTTP(rest, httptest.NewRequest(
		http.MethodGet, "/api/vibetable/v2/schema/tables/"+receipt.TableID, nil,
	))
	if rest.Code != http.StatusOK {
		t.Fatalf("REST schema snapshot = %d %s", rest.Code, rest.Body)
	}
	product := schemaProductRequestForMethod(
		t, mux, context.Background(), "schema.getTable",
		`{"tableId":"`+receipt.TableID+`"}`, schemaListWire,
	)
	if product.Error != nil {
		t.Fatalf("Product schema.getTable = %+v", product.Error)
	}
	var snapshot, restSnapshot map[string]any
	if err := json.Unmarshal(product.Result, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rest.Body.Bytes(), &restSnapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot["displayName"] != "订单 📦" || snapshot["tableId"] != receipt.TableID {
		t.Fatalf("Product schema identity = %#v", snapshot)
	}
	fields, ok := snapshot["fields"].([]any)
	if !ok || len(fields) != 1 {
		t.Fatalf("Product fields = %#v", snapshot["fields"])
	}
	field, ok := fields[0].(map[string]any)
	if !ok || field["logicalType"] != string(v2.LogicalFile) || field["file"] == nil {
		t.Fatalf("Product file field = %#v", fields)
	}
	if snapshot["schemaRevision"] != restSnapshot["schemaRevision"] ||
		snapshot["dataRevision"] != restSnapshot["dataRevision"] {
		t.Fatalf("Product snapshot disagrees with authoritative REST: Product=%#v REST=%#v", snapshot, restSnapshot)
	}
	assertSchemaSnapshotModelDumpDefaults(t, field)
	assertSchemaCapabilityModelDumpDefaults(t, snapshot["capabilities"])
	identity := field["identity"].(map[string]any)
	if identity["fieldId"] != file.FieldID {
		t.Fatalf("Product field identity = %#v", identity)
	}
}

func TestSchemaGetTableProductHTTPPreservesClosedParamsAndSchemaErrors(t *testing.T) {
	for _, params := range []struct {
		value string
		code  int
	}{
		{`null`, productrpc.CodeInvalidRequest},
		{`[]`, productrpc.CodeInvalidRequest},
		{`{"tableId":""}`, productrpc.CodeInvalidParams},
		{`{"tableId":7}`, productrpc.CodeInvalidParams},
		{`{"tableId":"orders","extra":true}`, productrpc.CodeInvalidParams},
	} {
		t.Run(params.value, func(t *testing.T) {
			pb := schemaProductStore(t)
			response := schemaProductRequestForMethod(
				t, schemaProductMux(t, pb), context.Background(), "schema.getTable", params.value, schemaListWire,
			)
			if response.Error == nil || response.Error.Code != params.code {
				t.Fatalf("closed params response = %+v", response)
			}
		})
	}

	pb := schemaProductStore(t)
	mux := schemaProductMux(t, pb)
	invalidPath := schemaProductRequestForMethod(
		t, mux, context.Background(), "schema.getTable", `{"tableId":" "}`, schemaListWire,
	)
	if invalidPath.Error == nil || invalidPath.Error.Code != productrpc.CodeInternalError {
		t.Fatalf("non-empty invalid path response = %+v", invalidPath)
	}
	missing := schemaProductRequestForMethod(
		t, mux, context.Background(), "schema.getTable", `{"tableId":"tbl_missing"}`, schemaListWire,
	)
	if missing.Error == nil || missing.Error.Code != productrpc.CodeProductData ||
		missing.Error.Data["code"] != "schema.table.not_found" ||
		missing.Error.Data["path"] != "tableId" {
		t.Fatalf("missing table response = %+v", missing)
	}
	if _, err := pb.DB().NewQuery("DROP TABLE vibetable_tables").Execute(); err != nil {
		t.Fatal(err)
	}
	restStorage := httptest.NewRecorder()
	mux.ServeHTTP(restStorage, httptest.NewRequest(
		http.MethodGet, "/api/vibetable/v2/schema/tables/tbl_storage", nil,
	))
	if restStorage.Code != http.StatusInternalServerError {
		t.Fatalf("former Python schema.getTable REST error = %d %s", restStorage.Code, restStorage.Body)
	}
	var restError map[string]any
	if err := json.Unmarshal(restStorage.Body.Bytes(), &restError); err != nil {
		t.Fatal(err)
	}
	if restError["code"] != "field.internal.failed" ||
		restError["message"] != "field settings operation failed" ||
		restError["retryable"] != true {
		t.Fatalf("former Python schema.getTable REST error changed: %#v", restError)
	}
	storage := schemaProductRequestForMethod(
		t, mux, context.Background(), "schema.getTable", `{"tableId":"tbl_storage"}`, schemaListWire,
	)
	wantStorage := &productrpc.ErrorObject{
		Code: productrpc.CodeProductData, Message: "Product data error",
		Data: map[string]any{
			"kind": "product_data_error", "code": "field.internal.failed", "path": "",
			"message": "field settings operation failed", "details": map[string]any{}, "retryable": true,
		},
	}
	if !reflect.DeepEqual(storage.Error, wantStorage) {
		t.Fatalf("storage error exposed or changed: %+v", storage.Error)
	}
}

func TestSchemaListProductHTTPRejectsInvalidParamsAndStaleScopeBeforeStorage(t *testing.T) {
	pb := schemaProductStore(t)
	mux := schemaProductMux(t, pb)
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
	mux := schemaProductMux(t, pb)
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

func createSchemaProductField(
	t *testing.T,
	pb *pocketbase.PocketBase,
	tableID string,
	logicalType v2.LogicalType,
	displayName string,
	operationID string,
) v2.ApplyReceipt {
	t.Helper()
	recommended, err := v2.RecommendedDefaults(logicalType)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fieldchange.NewCatalog(pb)
	store := fieldchange.NewPocketBasePlanStore(pb)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(pb, store)
	revisions, err := catalog.Revisions(context.Background(), tableID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: tableID, ExpectedSchemaRev: revisions.Schema,
		Draft: &v2.FieldDraft{
			DisplayName: displayName, LogicalType: logicalType,
			Value: recommended.Value, Constraints: recommended.Constraints,
			Storage: recommended.Storage, Display: recommended.Display,
			File: recommended.File, JSON: recommended.JSON,
		},
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := executor.Apply(context.Background(), v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: operationID,
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
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
	return schemaProductRequestForMethod(t, mux, ctx, "schema.list", params, wire)
}

func schemaProductRequestForMethod(
	t *testing.T,
	mux http.Handler,
	ctx context.Context,
	method string,
	params string,
	wire string,
) productrpc.ResponseEnvelope {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, productRPCPath, bytes.NewBufferString(
		`{"jsonrpc":"2.0","id":"schema-request","method":"`+method+`","wire":`+wire+`,"params":`+params+`}`)).WithContext(ctx)
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
	if response.Code != expectedStatus || string(envelope.ID) != `"schema-request"` || string(envelope.Wire) != wire {
		t.Fatalf("Product envelope changed: %d %s", response.Code, response.Body)
	}
	return envelope
}

func assertSchemaSnapshotModelDumpDefaults(t *testing.T, field map[string]any) {
	t.Helper()
	for _, name := range []string{"select", "relation", "file", "json", "autoDate", "formula", "lookup"} {
		if _, found := field[name]; !found {
			t.Fatalf("Python model_dump omitted field default %q from %#v", name, field)
		}
	}
	value := field["value"].(map[string]any)
	presence := value["presence"].(map[string]any)
	for _, name := range []string{"providerFieldId", "physicalName"} {
		if _, found := presence[name]; !found {
			t.Fatalf("Python model_dump omitted presence default %q from %#v", name, presence)
		}
	}
	display := field["display"].(map[string]any)
	if _, found := display["indent"]; !found {
		t.Fatalf("Python model_dump omitted display indent default from %#v", display)
	}
	if relation, ok := field["relation"].(map[string]any); ok {
		for _, name := range []string{"pairId", "reciprocalFieldId"} {
			if _, found := relation[name]; !found {
				t.Fatalf("Python model_dump omitted relation default %q from %#v", name, relation)
			}
		}
	}
}

func assertSchemaCapabilityModelDumpDefaults(t *testing.T, rawCapabilities any) {
	t.Helper()
	capabilities, ok := rawCapabilities.([]any)
	if !ok || len(capabilities) == 0 {
		t.Fatalf("Product capabilities = %#v", rawCapabilities)
	}
	for _, rawCapability := range capabilities {
		capability, ok := rawCapability.(map[string]any)
		if !ok {
			t.Fatalf("Product capability = %#v", rawCapability)
		}
		recommended := capability["recommended"].(map[string]any)
		for _, name := range []string{"file", "json"} {
			if _, found := recommended[name]; !found {
				t.Fatalf("Python model_dump omitted recommended default %q from %#v", name, recommended)
			}
		}
		presence := recommended["value"].(map[string]any)["presence"].(map[string]any)
		for _, name := range []string{"providerFieldId", "physicalName"} {
			if _, found := presence[name]; !found {
				t.Fatalf("Python model_dump omitted recommended presence default %q from %#v", name, presence)
			}
		}
	}
}

func schemaProductMux(t *testing.T, pb *pocketbase.PocketBase) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}, schemaGetTableRegistration(catalog), schemaListRegistration(catalog))
	if err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: request}}, nil
	})
	registerFieldRoutes(r, pb, nil, nil, nil, nil)
	registerSchemaRoutes(r, catalog, nil)
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	return mux
}
