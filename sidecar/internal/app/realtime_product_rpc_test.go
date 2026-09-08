package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
)

func TestReconcileProductHTTPMatchesAuthoritativeRealtimeRoute(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{
		DisplayName: "订单", OperationID: "reconcile-product-route",
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := schemaProductMux(t, pb)
	params := `{"tableId":"` + receipt.TableID + `","schemaRevision":"schema_0001","dataRevision":"data_0000"}`
	product := schemaProductRequestForMethod(
		t, mux, context.Background(), "events.reconcile", params, schemaListWire,
	)
	if product.Error != nil {
		t.Fatalf("Product reconcile error = %+v", product.Error)
	}
	rest := httptest.NewRecorder()
	mux.ServeHTTP(rest, httptest.NewRequest(
		http.MethodPost, "/api/vibetable/v1/events/reconcile", bytes.NewBufferString(params),
	))
	if rest.Code != http.StatusOK {
		t.Fatalf("realtime REST = %d %s", rest.Code, rest.Body)
	}
	var got, want any
	if err := json.Unmarshal(product.Result, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rest.Body.Bytes(), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Product=%s REST=%s", product.Result, rest.Body)
	}
}

func TestReconcileProductHTTPRejectsClosedParamsAndPreservesSourceFailure(t *testing.T) {
	pb := schemaProductStore(t)
	mux := schemaProductMux(t, pb)
	for _, params := range []string{
		`{}`,
		`{"tableId":"orders","schemaRevision":"schema_0001","dataRevision":"data_0000","extra":true}`,
		`{"tableId":"orders","schemaRevision":1,"dataRevision":"data_0000"}`,
	} {
		t.Run(params, func(t *testing.T) {
			response := schemaProductRequestForMethod(
				t, mux, context.Background(), "events.reconcile", params, schemaListWire,
			)
			if response.Error == nil || response.Error.Code != productrpc.CodeInvalidParams {
				t.Fatalf("closed params response = %+v", response)
			}
		})
	}
	params := `{"tableId":"missing","schemaRevision":"schema_0001","dataRevision":"data_0000"}`
	product := schemaProductRequestForMethod(
		t, mux, context.Background(), "events.reconcile", params, schemaListWire,
	)
	if product.Error == nil || product.Error.Code != productrpc.CodeProductData {
		t.Fatalf("source failure Product response = %+v", product)
	}
	rest := httptest.NewRecorder()
	mux.ServeHTTP(rest, httptest.NewRequest(
		http.MethodPost, "/api/vibetable/v1/events/reconcile", bytes.NewBufferString(params),
	))
	var restError map[string]any
	if rest.Code != http.StatusInternalServerError || json.Unmarshal(rest.Body.Bytes(), &restError) != nil {
		t.Fatalf("source failure REST = %d %s", rest.Code, rest.Body)
	}
	for _, key := range []string{"code", "message", "retryable"} {
		if !reflect.DeepEqual(product.Error.Data[key], restError[key]) {
			t.Fatalf("source failure %s: Product=%v REST=%v", key, product.Error.Data, restError)
		}
	}
}
