package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
)

func TestLookupListProjectionMatchesFrozenPythonConsumer(t *testing.T) {
	wire, err := os.ReadFile("testdata/schema_describe_oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus describeOracleCorpus
	if err := json.Unmarshal(wire, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, sample := range corpus.Cases {
		t.Run(sample.TableID, func(t *testing.T) {
			result, err := projectLookupList(sample.Catalog)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(actual, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(sample.LookupList, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("lookup.list projection differs: got %s; want %s", actual, sample.LookupList)
			}
		})
	}
}

func TestLookupListParamsRejectEmptyCollection(t *testing.T) {
	registration := lookupListRegistration(nil)
	for _, raw := range []string{`{"collection":""}`, `{}`, `null`, `[]`, `{"collection":null}`, `{"collection":2}`, `{"collection":true}`, `{"collection":"x","extra":1}`, `{"other":"x"}`} {
		if registration.ValidateParams(json.RawMessage(raw)) == nil {
			t.Fatalf("accepted invalid params %s", raw)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registration.Handler(ctx, json.RawMessage(`{"collection":"orders"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestLookupListHandlerReadsCurrentAuthority(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{
		DisplayName: "Lookup 订单", OperationID: "lookup-list-table",
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	service := relation.New(pb, nil, nil)
	registration := lookupListRegistration(service)
	raw, err := json.Marshal(map[string]string{"collection": table.TableID})
	if err != nil {
		t.Fatal(err)
	}
	got, err := registration.Handler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	response := schemaProductRequestForMethod(t, schemaProductMux(t, pb), context.Background(), "lookup.list", string(raw), schemaListWire)
	if response.Error != nil {
		t.Fatalf("Product lookup.list: %#v", response.Error)
	}
	var httpResult map[string]any
	if err := json.Unmarshal(response.Result, &httpResult); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(httpResult, got) {
		t.Fatalf("Product response diverged: %s", response.Result)
	}
	result := got.(map[string]any)
	if result["collection"] != table.TableID || len(result["definitions"].([]any)) != 0 || result["lookupRevision"] == "" {
		t.Fatalf("empty current table result = %v", result)
	}
}

func TestLookupListParamsKeepPythonSemanticBudget(t *testing.T) {
	registration := lookupListRegistration(nil)
	for _, sample := range []struct {
		character    string
		encodedBytes int
	}{
		{"a", 1}, {"é", 2}, {"\u2028", 3}, {"<", 1}, {"\n", 2}, {"\x00", 6},
	} {
		available := (1 << 20) - len(`{"collection":""}`)
		collection := strings.Repeat(sample.character, available/sample.encodedBytes) + strings.Repeat("a", available%sample.encodedBytes)
		raw, err := json.Marshal(map[string]string{"collection": collection})
		if err != nil {
			t.Fatal(err)
		}
		if err := registration.ValidateParams(raw); err != nil {
			t.Fatalf("exact budget %q: %v", sample.character, err)
		}
		raw, err = json.Marshal(map[string]string{"collection": collection + "a"})
		if err != nil {
			t.Fatal(err)
		}
		if registration.ValidateParams(raw) == nil {
			t.Fatalf("over budget accepted %q", sample.character)
		}
	}
}

func TestLookupListProductHTTPPreservesErrorsAndScope(t *testing.T) {
	pb := schemaProductStore(t)
	mux := schemaProductMux(t, pb)
	for _, params := range []string{`{}`, `{"collection":""}`, `{"collection":null}`, `{"collection":true}`, `{"collection":"orders","extra":true}`} {
		response := schemaProductRequestForMethod(t, mux, context.Background(), "lookup.list", params, schemaListWire)
		if response.Error == nil || response.Error.Code != productrpc.CodeInvalidParams {
			t.Fatalf("closed params %s: %#v", params, response.Error)
		}
	}
	params := `{"collection":"tbl_missing"}`
	missing := schemaProductRequestForMethod(t, mux, context.Background(), "lookup.list", params, schemaListWire)
	if missing.Error == nil || missing.Error.Code != productrpc.CodeProductData || missing.Error.Data["code"] != "mutation.internal.failed" || missing.Error.Data["retryable"] != true {
		t.Fatalf("missing table: %#v", missing.Error)
	}
	// The old Python owner reads this REST endpoint, whose error mapping differs
	// from schema.getTable's field route mapping.
	rest := httptest.NewRecorder()
	mux.ServeHTTP(rest, httptest.NewRequest(http.MethodGet, "/api/vibetable/v1/lookups/describe?tableId=tbl_missing", nil))
	if rest.Code != http.StatusInternalServerError {
		t.Fatalf("Lookup REST status=%d body=%s", rest.Code, rest.Body)
	}
	var oldError map[string]any
	if err := json.Unmarshal(rest.Body.Bytes(), &oldError); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"code", "path", "message", "details", "retryable"} {
		if !reflect.DeepEqual(missing.Error.Data[key], oldError[key]) {
			t.Fatalf("Lookup REST error %s: got=%v want=%v", key, missing.Error.Data[key], oldError[key])
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := schemaProductRequestForMethod(t, mux, ctx, "lookup.list", params, schemaListWire)
	if canceled.Error == nil || canceled.Error.Code != productrpc.CodeInternalError || len(canceled.Result) != 0 {
		t.Fatalf("canceled read: %#v", canceled)
	}
	if _, err := pb.DB().NewQuery("DROP TABLE vibetable_tables").Execute(); err != nil {
		t.Fatal(err)
	}
	for _, wire := range []string{
		strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":8`, 1),
		strings.Replace(schemaListWire, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1),
		`{"scope":"global","operationId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","sequence":1}`,
	} {
		response := schemaProductRequestForMethod(t, mux, context.Background(), "lookup.list", params, wire)
		if response.Error == nil || response.Error.Code != productrpc.CodeInvalidRequest {
			t.Fatalf("stale scope reached storage: %#v", response.Error)
		}
	}
	storage := schemaProductRequestForMethod(t, mux, context.Background(), "lookup.list", params, schemaListWire)
	if storage.Error == nil || storage.Error.Code != productrpc.CodeProductData || storage.Error.Data["code"] != "mutation.internal.failed" || storage.Error.Data["retryable"] != true {
		t.Fatalf("public storage error: %#v", storage.Error)
	}
}

func TestLookupListProjectionRejectsBrokenDefinitions(t *testing.T) {
	wire, err := os.ReadFile("testdata/schema_describe_oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus describeOracleCorpus
	if err := json.Unmarshal(wire, &corpus); err != nil {
		t.Fatal(err)
	}
	original := corpus.Cases[0].Catalog
	if len(original.Lookups) == 0 {
		t.Fatal("non-empty frozen lookup fixture required")
	}
	for _, sample := range []struct {
		name            string
		breakDefinition func(*relation.LookupDescriptor)
	}{
		{"cardinality", func(d *relation.LookupDescriptor) { d.ResultCardinality = "unknown" }},
		{"output", func(d *relation.LookupDescriptor) { d.OutputStorage = "unknown" }},
		{"target", func(d *relation.LookupDescriptor) { d.TargetFieldID = "" }},
		{"empty path", func(d *relation.LookupDescriptor) { d.Path = []relation.LookupPathDescriptor{} }},
		{"empty relation", func(d *relation.LookupDescriptor) { d.Path = []relation.LookupPathDescriptor{{RelationID: ""}} }},
	} {
		t.Run(sample.name, func(t *testing.T) {
			catalog := original
			catalog.Lookups = append([]relation.LookupDescriptor(nil), original.Lookups...)
			sample.breakDefinition(&catalog.Lookups[0])
			if result, err := projectLookupList(catalog); err == nil || result != nil {
				t.Fatalf("broken definition accepted: result=%v err=%v", result, err)
			}
		})
	}
}

func TestLookupListProductHTTPReadsPersistedRelationDefinitions(t *testing.T) {
	for _, cardinality := range []string{"one", "many"} {
		t.Run(cardinality, func(t *testing.T) {
			pb := schemaProductStore(t)
			lifecycle, err := schemacore.NewTableLifecycle(pb)
			if err != nil {
				t.Fatal(err)
			}
			table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "Lookup 订单", OperationID: "lookup-nonempty-" + cardinality, Actor: v2.Actor{ID: "local-user", Kind: "user"}})
			if err != nil {
				t.Fatal(err)
			}
			label := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "客户 é", "lookup-target")
			defaults, err := v2.RecommendedDefaults(v2.LogicalRelation)
			if err != nil {
				t.Fatal(err)
			}
			related := applySchemaProductField(t, pb, v2.FieldChangeIntent{
				Action: v2.ActionCreate, TableID: table.TableID,
				Draft: &v2.FieldDraft{DisplayName: "关联客户", LogicalType: v2.LogicalRelation,
					Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
					Relation: &v2.RelationSpec{TargetTableID: table.TableID, Cardinality: cardinality, DeletePolicy: "setNull", DisplayField: label.FieldID}},
				RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "关联来源", ReciprocalCardinality: "many", SourceDisplayFieldID: label.FieldID},
			}, "lookup-relation")
			defaults, err = v2.RecommendedDefaults(v2.LogicalLookup)
			if err != nil {
				t.Fatal(err)
			}
			lookup := applySchemaProductField(t, pb, v2.FieldChangeIntent{
				Action: v2.ActionCreate, TableID: table.TableID,
				Draft: &v2.FieldDraft{DisplayName: "客户名称", LogicalType: v2.LogicalLookup,
					Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
					Lookup: &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: related.FieldID}}, TargetFieldID: label.FieldID}},
			}, "lookup-definition")
			mux := schemaProductMux(t, pb)
			response := schemaProductRequestForMethod(t, mux, context.Background(), "lookup.list", fmt.Sprintf(`{"collection":%q}`, table.TableID), schemaListWire)
			if response.Error != nil {
				t.Fatalf("lookup.list: %#v", response.Error)
			}
			var result struct {
				Collection  string           `json:"collection"`
				Definitions []map[string]any `json:"definitions"`
				Revision    string           `json:"lookupRevision"`
			}
			if err := json.Unmarshal(response.Result, &result); err != nil {
				t.Fatal(err)
			}
			relationID := table.TableID + "." + related.FieldID
			want := map[string]any{
				"lookupId": table.TableID + "." + lookup.FieldID, "collection": table.TableID,
				"fieldKey": lookup.Definition.Identity.PhysicalName, "displayName": "客户名称",
				"path":       []any{map[string]any{"relationId": relationID}},
				"source":     map[string]any{"kind": "target_field", "fieldRef": label.FieldID},
				"outputType": "text", "outputScale": nil, "revision": float64(1),
				"state": "valid", "diagnostics": []any{}, "dependencies": []any{relationID},
			}
			if result.Collection != table.TableID || len(result.Definitions) != 1 || !reflect.DeepEqual(result.Definitions[0], want) {
				t.Fatalf("persisted Lookup projection: %s", response.Result)
			}
			described := schemaProductRequestForMethod(t, mux, context.Background(), "schema.describe", fmt.Sprintf(`{"collection":%q,"requestGeneration":1,"accepts":["vibetable.relation-capabilities.v1","vibetable.lookup-query.v1"]}`, table.TableID), schemaListWire)
			if described.Error != nil {
				t.Fatalf("schema.describe: %#v", described.Error)
			}
			var schema struct {
				Schema struct {
					Revision string `json:"lookupRevision"`
				} `json:"schema"`
			}
			if err := json.Unmarshal(described.Result, &schema); err != nil {
				t.Fatal(err)
			}
			if result.Revision == "" || result.Revision != schema.Schema.Revision {
				t.Fatalf("Lookup/schema revision disagreement: %q / %q", result.Revision, schema.Schema.Revision)
			}
		})
	}
}
