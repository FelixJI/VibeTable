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
			assertResultCardinalityWiring(t, sample.Catalog, result)
			// The frozen wire predates `resultCardinality`. Project away exactly
			// that new key; every historical definition field stays compared.
			stripResultCardinalityField(result)
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

// stripResultCardinalityField removes only the newly added resultCardinality
// key from every projected definition.
func stripResultCardinalityField(result map[string]any) {
	definitions, _ := result["definitions"].([]any)
	for _, definition := range definitions {
		if record, ok := definition.(map[string]any); ok {
			delete(record, "resultCardinality")
		}
	}
}

// assertResultCardinalityWiring pins the new resultCardinality projection
// against the frozen catalog inputs: each definition carries its descriptor's
// authoritative cardinality.
func assertResultCardinalityWiring(t *testing.T, catalog relation.CatalogResult, result map[string]any) {
	t.Helper()
	wantByLookup := map[string]string{}
	for _, lookup := range catalog.Lookups {
		wantByLookup[lookup.LookupID] = lookup.ResultCardinality
	}
	definitions, _ := result["definitions"].([]any)
	for _, definition := range definitions {
		record, ok := definition.(map[string]any)
		if !ok {
			t.Fatalf("definition is not an object: %#v", definition)
		}
		lookupID, _ := record["lookupId"].(string)
		want, found := wantByLookup[lookupID]
		if !found {
			t.Fatalf("definition %s missing from catalog", lookupID)
		}
		if record["resultCardinality"] != want {
			t.Fatalf("definition %s resultCardinality = %v, want %s", lookupID, record["resultCardinality"], want)
		}
	}
}

// TestLookupListProjectsResultCardinalityWithExplicitExpectations locks the
// scalar/list shape knowledge with hand-written expectations from the frozen
// corpus: numeric SUM-style outputs stay scalar, many-path numeric lookups
// keep the list cardinality.
func TestLookupListProjectsResultCardinalityWithExplicitExpectations(t *testing.T) {
	wire, err := os.ReadFile("testdata/schema_describe_oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus describeOracleCorpus
	if err := json.Unmarshal(wire, &corpus); err != nil {
		t.Fatal(err)
	}
	scalar, err := projectLookupList(corpus.Cases[0].Catalog)
	if err != nil {
		t.Fatal(err)
	}
	scalarDefinitions := scalar["definitions"].([]any)
	scalarByField := map[string]map[string]any{}
	for _, definition := range scalarDefinitions {
		record := definition.(map[string]any)
		scalarByField[record["lookupId"].(string)] = record
	}
	integerList := scalarByField["tbl_d7b01a33031b5f638043.fld_2x4sjnrvqywynr7h8vvf"]
	if integerList["resultCardinality"] != "one" || integerList["outputType"] != "integer" {
		t.Fatalf("scalar integer lookup = %#v", integerList)
	}
	listed, err := projectLookupList(corpus.Cases[1].Catalog)
	if err != nil {
		t.Fatal(err)
	}
	listByField := map[string]map[string]any{}
	for _, definition := range listed["definitions"].([]any) {
		record := definition.(map[string]any)
		listByField[record["lookupId"].(string)] = record
	}
	numericList := listByField["tbl_84ab97161767528bbb4e.fld_m0vyx9azrr3nr405wjcr"]
	if numericList["resultCardinality"] != "many" || numericList["outputType"] != "decimal" {
		t.Fatalf("numeric list lookup = %#v", numericList)
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

func TestLookupListParamsPreserveUnicodeScalarValues(t *testing.T) {
	registration := lookupListRegistration(nil)
	for _, raw := range []string{`{"collection":"\ud800"}`, `{"collection":"\udc00"}`, `{"collection":"\ud800x"}`, `{"collection":"\ud800\ud800"}`, "{\"collection\":\"\xff\"}"} {
		if registration.ValidateParams(json.RawMessage(raw)) == nil {
			t.Errorf("accepted malformed Unicode: %q", raw)
		}
	}
	for _, raw := range []string{`{"collection":"\ud83d\ude00"}`, `{"collection":"\ufffd"}`, `{"collection":"\\ud800"}`, `{"collection":"正常"}`} {
		if err := registration.ValidateParams(json.RawMessage(raw)); err != nil {
			t.Errorf("rejected Unicode scalar or literal escape: %q: %v", raw, err)
		}
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

func TestLookupListProductHTTPKeepsSemanticBudgetAcrossWireEscaping(t *testing.T) {
	pb := schemaProductStore(t)
	mux := schemaProductMux(t, pb)
	available := (1 << 20) - len(`{"collection":""}`)
	for _, sample := range []struct {
		name          string
		wireCharacter string
		semanticBytes int
	}{
		{"ascii", "a", 1},
		{"escaped-unicode", `\u00e9`, 2},
	} {
		t.Run(sample.name, func(t *testing.T) {
			value := strings.Repeat(sample.wireCharacter, available/sample.semanticBytes) + strings.Repeat("a", available%sample.semanticBytes)
			for _, boundary := range []struct {
				name   string
				suffix string
				code   int
			}{
				{"exact", "", productrpc.CodeProductData},
				{"one-byte-over", "a", productrpc.CodeInvalidParams},
			} {
				t.Run(boundary.name, func(t *testing.T) {
					params := `{"collection":"` + value + boundary.suffix + `"}`
					response := schemaProductRequestForMethod(t, mux, context.Background(), "lookup.list", params, schemaListWire)
					if response.Error == nil || response.Error.Code != boundary.code || len(response.Result) != 0 {
						t.Fatalf("semantic boundary: %#v", response.Error)
					}
					// An accepted parameter reaches the real catalog and reports the
					// missing table; exceeding the parameter budget never reaches it.
					if boundary.suffix == "" && response.Error.Data["code"] != "mutation.internal.failed" {
						t.Fatalf("catalog error: %#v", response.Error)
					}
				})
			}
		})
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
				"outputType": "text", "outputScale": nil, "resultCardinality": cardinality,
				"revision": float64(1),
				"state":    "valid", "diagnostics": []any{}, "dependencies": []any{relationID},
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

// TestLookupListProjectionKeepsNumericAggregationDecimal verifies the public
// list projection end of the numeric aggregation typing: decimal storage and
// one cardinality survive the wire projection together with the aggregation.
func TestLookupListProjectionKeepsNumericAggregationDecimal(t *testing.T) {
	catalog := relation.CatalogResult{
		TableID: "tbl_types000001", SchemaRevision: "schema_1",
		Lookups: []relation.LookupDescriptor{{
			Aggregation:       v2.LookupAggregationCountRecords,
			LookupID:          "tbl_types000001.fld_count00001",
			TableID:           "tbl_types000001",
			FieldID:           "fld_count00001",
			PhysicalName:      "f_count0000001",
			DisplayName:       "备注计数",
			RelationFieldID:   "fld_link000001",
			TargetFieldID:     "fld_notes000001",
			Path:              []relation.LookupPathDescriptor{{RelationID: "tbl_types000001.fld_link000001"}},
			ResultCardinality: "one",
			OutputStorage:     "decimal",
			Revision:          1,
		}},
	}
	result, err := projectLookupList(catalog)
	if err != nil {
		t.Fatal(err)
	}
	definitions, ok := result["definitions"].([]any)
	if !ok || len(definitions) != 1 {
		t.Fatalf("projected definitions = %#v", result)
	}
	projected, ok := definitions[0].(map[string]any)
	if !ok {
		t.Fatalf("projected definition = %#v", definitions[0])
	}
	if projected["outputType"] != "decimal" || projected["aggregation"] != v2.LookupAggregationCountRecords {
		t.Fatalf("numeric aggregation projection = %#v", projected)
	}
	if projected["resultCardinality"] != "one" {
		t.Fatalf("numeric aggregation cardinality = %#v", projected)
	}
}
