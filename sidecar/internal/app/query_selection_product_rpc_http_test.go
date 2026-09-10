package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

type unrelatedQueryPageMustNotRun struct{ t *testing.T }

func (port unrelatedQueryPageMustNotRun) QueryPage(context.Context, string, query.TableQuery) (query.Page, error) {
	port.t.Helper()
	port.t.Fatal("unrelated Product fixture unexpectedly invoked query.page")
	return query.Page{}, errors.New("unexpected query.page invocation")
}

func selectionProductHTTPMux(t *testing.T, pb *pocketbase.PocketBase, port interface {
	OpenSelectionProjection(context.Context, string, query.TableQuery) (query.SelectionProjection, error)
}) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	},
		mutationPreviewRegistration(unrelatedMutationProductMustNotRun{t: t}),
		mutationApplyRegistration(unrelatedMutationProductMustNotRun{t: t}), productrpc.ReconcileRegistration(catalog), queryValidateSnapshotRegistration(unrelatedQueryValidateSnapshotMustNotRun{t: t}),
		lookupListRegistration(relation.New(pb, nil, nil)),
		querySelectionOpenRegistration(port), queryPageRegistration(unrelatedQueryPageMustNotRun{t: t}), schemaDescribeRegistration(pb, relation.New(pb, nil, nil)),
		queryReadRowsRegistration(unrelatedQueryReadRowsMustNotRun{t: t}),
		queryViewRegistration(unrelatedViewMustNotRun{t: t}),
		lookupValuePageRegistration(unrelatedLookupValuePageMustNotRun{t: t}),
		relationSearchTargetsRegistration(unrelatedRelationSearchMustNotRun{t: t}),
		unrelatedRelationInspectRegistration(t),
		relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}),
		fieldSettingsDescribeRegistration(unrelatedFieldSettingsDescribeMustNotRun{t: t}),
		lookupQueryRegistration(unrelatedLookupQueryMustNotRun{t: t}),
		queryCursorOpenRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		queryCursorFetchRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		schemaGetTableRegistration(pb), schemaListRegistration(catalog),
		productrpc.AttachmentListRegistration(pb, mustAttachmentManager(t)),
		historyReadRegistration(unrelatedHistoryReadMustNotRun{t: t}),
		historyPreviewRestoreRegistration(unrelatedHistoryReadMustNotRun{t: t}),
		historyApplyRestoreRegistration(unrelatedHistoryReadMustNotRun{t: t}))
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

func TestQuerySelectionProductHTTPReadsAtomicSchemaAndCursorProjection(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "选择订单", OperationID: "selection-http", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	field := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "文本", "selection-text")
	description, err := schemaexecution.Describe(context.Background(), pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(description.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"甲", "乙", "丙"} {
		record := core.NewRecord(collection)
		record.Set(field.Definition.Identity.PhysicalName, value)
		if field.Definition.Value.Presence.Mode == v2.PresenceCompanion {
			record.Set(field.Definition.Value.Presence.PhysicalName, true)
		}
		if err := pb.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(pb, source)
	mux := selectionProductHTTPMux(t, pb, port)
	response := schemaProductRequestForMethod(t, mux, context.Background(), "query.selectionOpen", `{"tableId":"`+table.TableID+`","query":{"limit":2,"sorts":[{"field":"`+field.Definition.Identity.PhysicalName+`"}]}}`, schemaListWire)
	if response.Error != nil {
		t.Fatalf("selection Product error: %+v", response.Error)
	}
	var result struct {
		SchemaSnapshot map[string]any     `json:"schemaSnapshot"`
		CursorWindow   query.CursorWindow `json:"cursorWindow"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaSnapshot["tableId"] != table.TableID || len(result.CursorWindow.Rows) != 2 || !result.CursorWindow.HasMore || result.CursorWindow.NextCursor == nil {
		t.Fatalf("selection projection = %#v", result)
	}
	if result.SchemaSnapshot["schemaRevision"] != result.CursorWindow.Snapshot.SchemaRevision || result.SchemaSnapshot["dataRevision"] != float64(result.CursorWindow.Snapshot.DataRevision) {
		t.Fatalf("revision pairing = schema %#v cursor %#v", result.SchemaSnapshot, result.CursorWindow.Snapshot)
	}
	continued, err := port.FetchCursor(context.Background(), *result.CursorWindow.NextCursor)
	if err != nil || len(continued.Rows) != 1 || continued.HasMore || continued.NextCursor != nil || !reflect.DeepEqual(continued.Snapshot, result.CursorWindow.Snapshot) {
		t.Fatalf("selection continuation = %#v, %v", continued, err)
	}
}

type selectionOracleCase struct {
	Name    string `json:"name"`
	Request struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	} `json:"request"`
	Fixture struct {
		Response json.RawMessage `json:"response"`
		Failure  *string         `json:"failure"`
	} `json:"authorityFixture"`
	AuthorityRequests []json.RawMessage `json:"authorityRequests"`
	Response          json.RawMessage   `json:"response"`
}

func readSelectionOracle(t *testing.T, name string, count int) []selectionOracleCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", name))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []selectionOracleCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil || len(corpus.Cases) != count {
		t.Fatalf("selection oracle: %d %v", len(corpus.Cases), err)
	}
	return corpus.Cases
}

func selectionOracleResponse(t *testing.T, mux http.Handler, sample selectionOracleCase) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": sample.Name, "method": sample.Request.Method, "params": sample.Request.Params, "wire": json.RawMessage(schemaListWire)})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, productRPCPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK && recorder.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body)
	}
	decoder := json.NewDecoder(recorder.Body)
	decoder.UseNumber()
	var actual map[string]any
	if err := decoder.Decode(&actual); err != nil {
		t.Fatal(err)
	}
	return actual
}

func TestQuerySelectionProductHTTPReplaysExpressiblePythonOracle(t *testing.T) {
	// Cases outside this set require malformed JSON authority DTO fields that the
	// typed Go SelectionPort cannot produce; each remains named to prevent a
	// silent expansion of this migration seam.
	expressible := map[string]bool{
		"product-error": true,
		"missing-query": true, "null-query": true, "array-query": true,
		"unknown-param": true, "empty-table": true, "null-table": true,
	}
	probe := &querySelectionProbe{}
	mux := selectionProductHTTPMux(t, schemaProductStore(t), probe)
	excluded := map[string]string{
		"complete-schema-defaults-unicode-falsy": "authority schema has non-canonical capability list",
		"terminal-window":                        "authority schema has non-canonical capability list",
		"empty-records":                          "authority schema has non-canonical capability list",
		"empty-query-forwarded":                  "authority schema has non-canonical capability list",
		"transport-error":                        "HTTP transport exception is absent from typed in-process SelectionPort",
		"missing-schema":                         "typed SelectionProjection always contains SchemaSnapshot",
		"schema-table-mismatch":                  "covered by canonical baseline mutation test",
		"cursor-table-mismatch":                  "covered by canonical baseline mutation test",
		"schema-revision-mismatch":               "covered by canonical baseline mutation test",
		"data-revision-mismatch":                 "covered by canonical baseline mutation test",
		"boolean-data-revision":                  "DataRevision is typed int64",
		"incomplete-schema":                      "covered by canonical baseline mutation test",
		"unknown-schema-field":                   "unknown JSON fields cannot cross typed SchemaSnapshot",
		"incomplete-query-snapshot":              "typed QuerySnapshot always emits its non-omitempty fields",
		"unknown-query-snapshot-field":           "unknown JSON fields cannot cross typed QuerySnapshot",
		"more-without-cursor":                    "covered by canonical baseline mutation test",
		"terminal-with-cursor":                   "covered by canonical baseline mutation test",
		"non-string-cursor":                      "NextCursor is typed *string",
		"non-boolean-has-more":                   "HasMore is typed bool",
		"malformed-row":                          "Rows element is typed map[string]any",
		"negative-row-count":                     "covered by canonical baseline mutation test",
	}
	for _, sample := range readSelectionOracle(t, "query-selection-python-oracle.json", 28) {
		if reason, skipped := excluded[sample.Name]; skipped {
			t.Run(sample.Name, func(t *testing.T) { t.Skip(reason) })
			continue
		}
		if !expressible[sample.Name] {
			t.Fatalf("oracle case %q has no replay classification", sample.Name)
		}
		t.Run(sample.Name, func(t *testing.T) {
			*probe = querySelectionProbe{}
			if sample.Fixture.Failure != nil {
				probe.err = &query.ProductError{Code: "query.snapshot_stale", Path: "snapshot", Message: "Snapshot is stale", Details: map[string]any{"expected": "data_0", "actual": "data_1"}}
			} else if len(sample.AuthorityRequests) != 0 {
				decoder := json.NewDecoder(bytes.NewReader(sample.Fixture.Response))
				decoder.UseNumber()
				if err := decoder.Decode(&probe.result); err != nil {
					t.Fatal(err)
				}
			}
			if len(sample.AuthorityRequests) != 0 && sample.Fixture.Failure == nil {
				if _, err := querySelectionOpenRegistration(probe).Handler(context.Background(), sample.Request.Params); err != nil {
					t.Fatalf("typed authority projection rejected: %v", err)
				}
				*probe = querySelectionProbe{result: probe.result}
			}
			actual := selectionOracleResponse(t, mux, sample)
			if !reflect.DeepEqual(actual["wire"], mustSelectionJSON(t, schemaListWire)) {
				t.Fatal("response changed wire")
			}
			delete(actual, "wire")
			want := mustSelectionJSON(t, string(sample.Response))
			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("frozen response mismatch\ngot=%v\nwant=%v", actual, want)
			}
			if len(sample.AuthorityRequests) == 0 && probe.calls != 0 {
				t.Fatalf("rejected params invoked Port %d times", probe.calls)
			}
			if len(sample.AuthorityRequests) != 0 && probe.calls != 1 {
				t.Fatalf("authority calls %d", probe.calls)
			}
		})
	}
}

func TestQuerySelectionProductHTTPReplaysTypedPythonOracle(t *testing.T) {
	probe := &querySelectionProbe{}
	mux := selectionProductHTTPMux(t, schemaProductStore(t), probe)
	for _, sample := range readSelectionOracle(t, "query-selection-typed-python-oracle.json", 4) {
		t.Run(sample.Name, func(t *testing.T) {
			*probe = querySelectionProbe{}
			decoder := json.NewDecoder(bytes.NewReader(sample.Fixture.Response))
			decoder.UseNumber()
			if err := decoder.Decode(&probe.result); err != nil {
				t.Fatal(err)
			}
			actual := selectionOracleResponse(t, mux, sample)
			if !reflect.DeepEqual(actual["wire"], mustSelectionJSON(t, schemaListWire)) {
				t.Fatal("response changed wire")
			}
			delete(actual, "wire")
			if want := mustSelectionJSON(t, string(sample.Response)); !reflect.DeepEqual(actual, want) {
				t.Fatalf("frozen canonical response mismatch\ngot=%v\nwant=%v", actual, want)
			}
			if probe.calls != 1 {
				t.Fatalf("authority calls %d", probe.calls)
			}
		})
	}
}

func TestQuerySelectionProductRejectsCanonicalBaselineMutations(t *testing.T) {
	sample := readSelectionOracle(t, "query-selection-typed-python-oracle.json", 4)[0]
	for name, mutate := range map[string]func(*query.SelectionProjection){
		"incomplete-schema":        func(value *query.SelectionProjection) { value.SchemaSnapshot.Fields[0] = v2.FieldDefinition{} },
		"schema-table-mismatch":    func(value *query.SelectionProjection) { value.SchemaSnapshot.TableID = "other" },
		"cursor-table-mismatch":    func(value *query.SelectionProjection) { value.CursorWindow.Snapshot.Table = "other" },
		"schema-revision-mismatch": func(value *query.SelectionProjection) { value.CursorWindow.Snapshot.SchemaRevision = "other" },
		"data-revision-mismatch":   func(value *query.SelectionProjection) { value.CursorWindow.Snapshot.DataRevision++ },
		"negative-row-count":       func(value *query.SelectionProjection) { value.CursorWindow.TotalRows = -1 },
		"more-without-cursor": func(value *query.SelectionProjection) {
			value.CursorWindow.HasMore, value.CursorWindow.NextCursor = true, nil
		},
		"terminal-with-cursor": func(value *query.SelectionProjection) {
			next := "next"
			value.CursorWindow.HasMore, value.CursorWindow.NextCursor = false, &next
		},
		"nil-row": func(value *query.SelectionProjection) { value.CursorWindow.Rows = []map[string]any{nil} },
	} {
		t.Run(name, func(t *testing.T) {
			var value query.SelectionProjection
			decoder := json.NewDecoder(bytes.NewReader(sample.Fixture.Response))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				t.Fatal(err)
			}
			baseline := &querySelectionProbe{result: value}
			if _, err := querySelectionOpenRegistration(baseline).Handler(context.Background(), sample.Request.Params); err != nil {
				t.Fatalf("canonical baseline rejected before %s mutation: %v", name, err)
			}
			mutate(&value)
			probe := &querySelectionProbe{result: value}
			_, err := querySelectionOpenRegistration(probe).Handler(context.Background(), sample.Request.Params)
			var public *productrpc.PublicError
			if err == nil || errors.As(err, &public) {
				t.Fatalf("mutation was not an internal authority failure: %v", err)
			}
		})
	}
}

func mustSelectionJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
