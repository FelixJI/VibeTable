package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

// Exercise the actual HTTP envelope, dispatcher, shared domain entry and
// app-scoped compiler; no transport replies or domain results are scripted.
func TestFormulaProductHTTPLifecycleMatchesSharedRESTDomain(t *testing.T) {
	pb := schemaProductStore(t)
	mux := schemaProductMux(t, pb)
	table, amount, formulaField := createFormulaProductTable(t, pb)
	before := formulaTestRevisions(t, pb, table.TableID)
	call := func(method string, params string) productrpc.ResponseEnvelope {
		t.Helper()
		return schemaProductRequestForMethod(t, mux, context.Background(), method, params, schemaListWire)
	}
	assertError := func(response productrpc.ResponseEnvelope, code string) {
		t.Helper()
		if response.Error == nil || response.Error.Code != productrpc.CodeProductData {
			t.Fatalf("expected %s: %+v", code, response)
		}
		raw, err := json.Marshal(response.Error.Data)
		if err != nil {
			t.Fatal(err)
		}
		var data struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		if data.Code != code {
			t.Fatalf("expected %s: %s", code, raw)
		}
	}
	assertRESTParity := func(path string, body string, response productrpc.ResponseEnvelope) {
		t.Helper()
		if response.Error != nil {
			t.Fatalf("Product formula call failed: %+v", response.Error)
		}
		rest := httptest.NewRecorder()
		restRequest := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		restRequest.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(rest, restRequest)
		if rest.Code != http.StatusOK {
			t.Fatalf("REST %s = %d %s", path, rest.Code, rest.Body)
		}
		var product, restResult any
		if err := json.Unmarshal(response.Result, &product); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(rest.Body.Bytes(), &restResult); err != nil {
			t.Fatal(err)
		}
		if !jsonEqual(product, restResult) {
			t.Fatalf("Product=%s REST=%s", response.Result, rest.Body)
		}
	}

	wire := formulaTestWireField(t, formulaField)
	fieldJSON, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	validateParams := `{"tableId":"` + table.TableID + `","field":` + string(fieldJSON) + `}`
	assertRESTParity(
		"/api/vibetable/v1/formulas/validate", validateParams,
		call("formula.validate", validateParams),
	)

	draftParams := `{"tableId":"` + table.TableID + `","displaySource":"{` + amount.DisplayName + `} * 2"}`
	assertRESTParity(
		"/api/vibetable/v1/formulas/draft/validate", draftParams,
		call("formula.draft.validate", draftParams),
	)

	previewParams := `{"tableId":"` + table.TableID + `","field":` + string(fieldJSON) +
		`,"row":{"` + amount.Identity.PhysicalName + `":4},"changedFieldIds":["` +
		amount.Identity.FieldID + `"]}`
	preview := call("formula.preview", previewParams)
	assertRESTParity(
		"/api/vibetable/v1/formulas/preview", previewParams, preview,
	)
	var values map[string]any
	if err := json.Unmarshal(preview.Result, &values); err != nil {
		t.Fatal(err)
	}
	previewValues, ok := values["values"].(map[string]any)
	if !ok || previewValues[formulaField.Identity.PhysicalName] != float64(8) {
		t.Fatalf("preview values = %#v", values)
	}

	// A constant draft exercises the empty dependency slices produced by the
	// real compiler and rendered as arrays by PocketBase's JSON v2 writer.
	constant := `{"tableId":"` + table.TableID + `","displaySource":"1"}`
	constantResponse := call("formula.draft.validate", constant)
	assertRESTParity("/api/vibetable/v1/formulas/draft/validate", constant, constantResponse)
	var constantResult map[string]json.RawMessage
	if err := json.Unmarshal(constantResponse.Result, &constantResult); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"dependencies", "relationAggregatePaths"} {
		if string(constantResult[key]) != "[]" {
			t.Fatalf("%s = %s", key, constantResult[key])
		}
	}

	// Preserve the DTO-vs-domain stage in the actual dispatcher envelope.
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		code   int
	}{
		{"identity pattern", func(field map[string]any) { field["identity"].(map[string]any)["fieldId"] = "bad" }, productrpc.CodeInternalError},
		{"numeric range", func(field map[string]any) { field["display"].(map[string]any)["displayScale"] = 16 }, productrpc.CodeInternalError},
		{"Python alias", func(field map[string]any) { field["json_"] = nil }, productrpc.CodeProductData},
		{"Literal bool", func(field map[string]any) { field["display"].(map[string]any)["indent"] = false }, productrpc.CodeProductData},
		{"Literal float", func(field map[string]any) { field["display"].(map[string]any)["indent"] = json.Number("2.0") }, productrpc.CodeProductData},
	} {
		t.Run(test.name, func(t *testing.T) {
			field := formulaTestWireField(t, formulaField)
			test.mutate(field)
			params, err := json.Marshal(map[string]any{"tableId": table.TableID, "field": field})
			if err != nil {
				t.Fatal(err)
			}
			response := call("formula.validate", string(params))
			if response.Error == nil || response.Error.Code != test.code {
				t.Fatalf("error = %+v", response.Error)
			}
			if test.code == productrpc.CodeProductData {
				rest := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/api/vibetable/v1/formulas/validate", strings.NewReader(string(params)))
				req.Header.Set("Content-Type", "application/json")
				mux.ServeHTTP(rest, req)
				if rest.Code != http.StatusBadRequest {
					t.Fatalf("REST %d: %s", rest.Code, rest.Body)
				}
				var failure map[string]any
				if err := json.Unmarshal(rest.Body.Bytes(), &failure); err != nil {
					t.Fatal(err)
				}
				want := map[string]any{"kind": "product_data_error", "code": failure["code"], "message": failure["message"], "path": failure["path"], "details": failure["details"], "retryable": false}
				if response.Error.Message != "Product data error" || !jsonEqual(response.Error.Data, want) {
					t.Fatalf("REST=%s Product=%+v", rest.Body, response.Error)
				}
			}
		})
	}

	// Closed transport params keep the retired Python rejection codes.
	for _, params := range []struct {
		value string
		code  int
	}{
		{`{"tableId":"orders","displaySource":"1","extra":true}`, productrpc.CodeInvalidParams},
		{`{"tableId":"","displaySource":"1"}`, productrpc.CodeInvalidParams},
		{`{"tableId":false,"displaySource":"1"}`, productrpc.CodeInvalidParams},
		{`{"tableId":"orders"}`, productrpc.CodeInvalidParams},
		{`[]`, productrpc.CodeInvalidRequest},
	} {
		response := call("formula.draft.validate", params.value)
		if response.Error == nil || response.Error.Code != params.code {
			t.Fatalf("closed params %s = %+v", params.value, response)
		}
	}

	// Domain failures keep the public product data projection.
	assertError(call("formula.draft.validate",
		`{"tableId":"`+table.TableID+`","displaySource":"1 + ("}`), "formula.syntax")
	assertError(call("formula.draft.validate",
		`{"tableId":"tbl_missing","displaySource":"1"}`), "formula.runtime")

	// A stale workspace session is rejected before any formula work.
	staleWire := strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":6`, 1)
	stale := httptest.NewRecorder()
	staleRequest := httptest.NewRequest(http.MethodPost, productRPCPath, strings.NewReader(
		`{"jsonrpc":"2.0","id":"stale-formula","method":"formula.draft.validate","wire":`+staleWire+
			`,"params":{"tableId":"`+table.TableID+`","displaySource":"1"}}`))
	staleRequest.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(stale, staleRequest)
	var staleEnvelope productrpc.ResponseEnvelope
	if err := json.Unmarshal(stale.Body.Bytes(), &staleEnvelope); err != nil {
		t.Fatal(err)
	}
	if stale.Code != http.StatusBadRequest || staleEnvelope.Error == nil ||
		staleEnvelope.Error.Code != productrpc.CodeInvalidRequest {
		t.Fatalf("stale session accepted: %d %s", stale.Code, stale.Body)
	}

	// Read-only authority: the whole lifecycle must not move revisions.
	if after := formulaTestRevisions(t, pb, table.TableID); after != before {
		t.Fatalf("formula calls wrote revisions: %s -> %s", before, after)
	}
}

func jsonEqual(left, right any) bool {
	leftEncoded, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightEncoded, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return string(leftEncoded) == string(rightEncoded)
}
