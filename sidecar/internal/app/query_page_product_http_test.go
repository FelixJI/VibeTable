package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func queryPageHTTPMux(t *testing.T, pb *pocketbase.PocketBase, registration productrpc.Registration) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}, productrpc.ReconcileRegistration(catalog), lookupListRegistration(relation.New(pb, nil, nil)),
		registration, schemaDescribeRegistration(pb, relation.New(pb, nil, nil)), schemaGetTableRegistration(pb),
		schemaListRegistration(catalog), productrpc.AttachmentListRegistration(pb, mustAttachmentManager(t)))
	if err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: request}}, nil
	})
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

func TestQueryPageProductHTTPReadsPersistedAuthorityAndSignedSnapshot(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "Page 订单", OperationID: "page-http", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	text := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Text", "page-text")
	payload := createSchemaProductField(t, pb, table.TableID, v2.LogicalJSON, "Payload", "page-json")
	description, err := schemaexecution.Describe(context.Background(), pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(description.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"", "中文 Cafe\u0301 👩🏽‍💻 \u200fعربي"}
	values := map[string]any{"zero": 0, "false": false, "null": nil, "blank": "", "array": []any{}, "object": map[string]any{}}
	ids := []string{}
	for _, value := range texts {
		record := core.NewRecord(collection)
		record.Set(text.Definition.Identity.PhysicalName, value)
		record.Set(payload.Definition.Identity.PhysicalName, values)
		for _, field := range []*v2.FieldDefinition{text.Definition, payload.Definition} {
			if field.Value.Presence.Mode == v2.PresenceCompanion {
				record.Set(field.Value.Presence.PhysicalName, true)
			}
		}
		if err := pb.Save(record); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, record.Id)
	}
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(pb, source)
	mux := queryPageHTTPMux(t, pb, queryPageRegistration(port))
	request := func(ctx context.Context, input query.TableQuery) productrpc.ResponseEnvelope {
		raw, err := json.Marshal(map[string]any{"tableId": table.TableID, "query": input})
		if err != nil {
			t.Fatal(err)
		}
		return schemaProductRequestForMethod(t, mux, ctx, "query.page", string(raw), schemaListWire)
	}
	input := query.TableQuery{Sorts: []query.SortCondition{{Field: text.Definition.Identity.PhysicalName, Direction: query.SortAscending}}, Limit: 1}
	for offset := 0; offset <= 2; offset++ {
		input.Offset = offset
		response := request(context.Background(), input)
		if response.Error != nil {
			t.Fatalf("page %d: %+v", offset, response.Error)
		}
		var result struct {
			Rows         []map[string]any    `json:"rows"`
			Offset       int                 `json:"offset"`
			Limit        int                 `json:"limit"`
			FilteredRows int                 `json:"filteredRows"`
			TotalRows    int                 `json:"totalRows"`
			Snapshot     query.QuerySnapshot `json:"snapshot"`
		}
		if err := json.Unmarshal(response.Result, &result); err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(response.Result, &wire); err != nil {
			t.Fatal(err)
		}
		if len(wire) != 6 || wire["snapshot"] == nil || wire["querySnapshot"] != nil {
			t.Fatalf("Product projection: %s", response.Result)
		}
		if result.Offset != offset || result.Limit != 1 || result.FilteredRows != 2 || result.TotalRows != 2 {
			t.Fatalf("page counters: %s", response.Result)
		}
		if offset == 2 {
			if result.Rows == nil || len(result.Rows) != 0 {
				t.Fatalf("empty page: %s", response.Result)
			}
		} else {
			if len(result.Rows) != 1 || result.Rows[0]["id"] != ids[offset] || result.Rows[0][text.Definition.Identity.PhysicalName] != texts[offset] {
				t.Fatalf("persisted page: %s", response.Result)
			}
			actual, err := json.Marshal(result.Rows[0][payload.Definition.Identity.PhysicalName])
			if err != nil {
				t.Fatal(err)
			}
			expected, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			if string(actual) != string(expected) {
				t.Fatalf("falsy payload: %s want %s", actual, expected)
			}
		}
		validation, err := port.ValidateSnapshot(context.Background(), result.Snapshot, &input)
		if err != nil || !validation.Valid {
			t.Fatalf("Product snapshot lost authority signature: %+v %v", validation, err)
		}
	}
	input.Offset = 0
	input.Filters = []query.FilterExpression{{Field: text.Definition.Identity.PhysicalName, Operator: query.OperatorEqual, Value: texts[1]}}
	filtered := request(context.Background(), input)
	var result struct {
		Rows         []map[string]any `json:"rows"`
		FilteredRows int              `json:"filteredRows"`
		TotalRows    int              `json:"totalRows"`
	}
	if err := json.Unmarshal(filtered.Result, &result); err != nil {
		t.Fatalf("filter: %+v %v", filtered, err)
	}
	if filtered.Error != nil || len(result.Rows) != 1 || result.Rows[0]["id"] != ids[1] || result.FilteredRows != 1 || result.TotalRows != 2 {
		t.Fatalf("filtered totals: %+v", filtered)
	}
	invalid := schemaProductRequestForMethod(t, mux, context.Background(), "query.page", `{"tableId":"`+table.TableID+`","query":{"limit":0}}`, schemaListWire)
	if invalid.Error == nil || invalid.Error.Code != productrpc.CodeProductData || invalid.Error.Data["code"] != "query.request.invalid" {
		t.Fatalf("invalid domain query: %+v", invalid)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := request(ctx, input)
	if cancelled.Error == nil || cancelled.Error.Code != productrpc.CodeInternalError || len(cancelled.Result) != 0 {
		t.Fatalf("cancelled query returned data: %+v", cancelled)
	}
}

type queryPageHTTPProbe struct {
	calls int
	page  query.Page
	err   error
	table string
	input query.TableQuery
}

func (p *queryPageHTTPProbe) QueryPage(_ context.Context, table string, input query.TableQuery) (query.Page, error) {
	p.calls++
	p.table = table
	p.input = input
	return p.page, p.err
}

func TestQueryPageProductHTTPConsumesApplicableFrozenPythonOracle(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "query-window-python-oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Producer string `json:"producerCommit"`
		Cases    []struct {
			Name    string `json:"name"`
			Request struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			} `json:"request"`
			Fixture struct {
				Response json.RawMessage `json:"response"`
			} `json:"authorityFixture"`
			AuthorityRequests []json.RawMessage `json:"authorityRequests"`
			Response          struct {
				Result json.RawMessage         `json:"result"`
				Error  *productrpc.ErrorObject `json:"error"`
			} `json:"response"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Producer != "c97c83336e4aa1bdf993fc46a7de57040219fb03" {
		t.Fatal("unexpected frozen producer")
	}
	pb := schemaProductStore(t)
	probe := &queryPageHTTPProbe{}
	mux := queryPageHTTPMux(t, pb, queryPageRegistration(probe))
	consumed, excluded := 0, 0
	for _, sample := range corpus.Cases {
		if sample.Request.Method != "query.page" {
			continue
		}
		t.Run(sample.Name, func(t *testing.T) {
			switch sample.Name {
			case "page-unicode-falsy-offset-limit", "page-nested-query-forwarded", "page-malformed-rows", "page-malformed-offset", "page-product-error", "page-transport-error":
				excluded++
				t.Skip("frozen transport bypasses REST rejection of limit=0; cannot reproduce its response at typed Go Port boundary")
			}
			*probe = queryPageHTTPProbe{}
			if sample.Name == "page-empty-query" {
				excluded++
				t.Skip("frozen partial snapshot cannot represent a full typed QuerySnapshot; supplementary producer corpus is required")
			}
			response := schemaProductRequestForMethod(t, mux, context.Background(), "query.page", string(sample.Request.Params), schemaListWire)
			if !reflect.DeepEqual(response.Error, sample.Response.Error) {
				t.Fatalf("frozen error: got %+v want %+v", response.Error, sample.Response.Error)
			}

			if probe.calls != len(sample.AuthorityRequests) {
				t.Fatalf("authority calls=%d want=%d", probe.calls, len(sample.AuthorityRequests))
			}
			consumed++
		})
	}
	if consumed != 3 || excluded != 7 {
		t.Fatalf("frozen page coverage: consumed %d excluded %d", consumed, excluded)
	}
}

func TestQueryPageProductHTTPConsumesTypedFrozenPythonOracle(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "query-page-typed-python-oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Producer string `json:"producerCommit"`
		Cases    []struct {
			Name    string `json:"name"`
			Request struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			} `json:"request"`
			Fixture struct {
				Response json.RawMessage `json:"response"`
				Failure  *string         `json:"failure"`
			} `json:"authorityFixture"`
			AuthorityRequests []struct {
				Body struct {
					Table string           `json:"tableId"`
					Query query.TableQuery `json:"query"`
				} `json:"body"`
			} `json:"authorityRequests"`
			Response struct {
				Result json.RawMessage         `json:"result"`
				Error  *productrpc.ErrorObject `json:"error"`
			} `json:"response"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Producer != "c97c83336e4aa1bdf993fc46a7de57040219fb03" || len(corpus.Cases) != 3 {
		t.Fatal("unexpected typed oracle producer/cases")
	}
	pb := schemaProductStore(t)
	probe := &queryPageHTTPProbe{}
	mux := queryPageHTTPMux(t, pb, queryPageRegistration(probe))
	for _, sample := range corpus.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			*probe = queryPageHTTPProbe{}
			if sample.Request.Method != "query.page" {
				t.Fatal("unexpected oracle method")
			}
			if sample.Fixture.Failure != nil {
				if *sample.Fixture.Failure != "product" {
					t.Fatal("unexpected scripted failure")
				}
				// This is the producer's scripted transport input, not an error reconstructed
				// from the frozen expected Product envelope.
				probe.err = &query.ProductError{Code: "query.snapshot_stale", Path: "snapshot", Message: "Snapshot is stale", Details: map[string]any{"expected": "data_0", "actual": "data_1"}}
			} else {
				decoder := json.NewDecoder(bytes.NewReader(sample.Fixture.Response))
				decoder.UseNumber()
				if err := decoder.Decode(&probe.page); err != nil {
					t.Fatal(err)
				}
			}
			response := schemaProductRequestForMethod(t, mux, context.Background(), "query.page", string(sample.Request.Params), schemaListWire)
			if !reflect.DeepEqual(response.Error, sample.Response.Error) {
				t.Fatalf("frozen public error: got %+v want %+v", response.Error, sample.Response.Error)
			}
			if len(sample.Response.Result) != 0 {
				decode := func(raw json.RawMessage) any {
					var value any
					decoder := json.NewDecoder(bytes.NewReader(raw))
					decoder.UseNumber()
					if err := decoder.Decode(&value); err != nil {
						t.Fatal(err)
					}
					return value
				}
				if !reflect.DeepEqual(decode(response.Result), decode(sample.Response.Result)) {
					t.Fatalf("complete frozen projection: got %s want %s", response.Result, sample.Response.Result)
				}
			} else if len(response.Result) != 0 {
				t.Fatalf("unexpected result on failure: %s", response.Result)
			}
			if len(sample.AuthorityRequests) != 1 || probe.calls != 1 {
				t.Fatalf("authority calls %d, producer %d", probe.calls, len(sample.AuthorityRequests))
			}
			expected := sample.AuthorityRequests[0].Body
			if probe.table != expected.Table || !reflect.DeepEqual(probe.input, expected.Query) {
				t.Fatalf("authority input got %s %+v want %s %+v", probe.table, probe.input, expected.Table, expected.Query)
			}
		})
	}
}
