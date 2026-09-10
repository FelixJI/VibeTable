package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func queryViewHTTPMux(t *testing.T, pb *pocketbase.PocketBase, port interface {
	ExecuteViewQuery(context.Context, string, query.ViewQuery) (query.ViewResult, error)
}) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	},
		mutationPreviewRegistration(unrelatedMutationProductMustNotRun{t: t}),
		mutationApplyRegistration(unrelatedMutationProductMustNotRun{t: t}), productrpc.ReconcileRegistration(catalog), queryValidateSnapshotRegistration(unrelatedQueryValidateSnapshotMustNotRun{t: t}),
		lookupListRegistration(relation.New(pb, nil, nil)),
		relationSearchTargetsRegistration(unrelatedRelationSearchMustNotRun{t: t}),
		unrelatedRelationInspectRegistration(t),
		relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}),
		fieldSettingsDescribeRegistration(unrelatedFieldSettingsDescribeMustNotRun{t: t}),
		lookupQueryRegistration(unrelatedLookupQueryMustNotRun{t: t}),
		queryReadRowsRegistration(unrelatedQueryReadRowsMustNotRun{t: t}),
		queryCursorOpenRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		queryCursorFetchRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		querySelectionOpenRegistration(unrelatedSelectionMustNotRun{t: t}),
		queryViewRegistration(port), lookupValuePageRegistration(unrelatedLookupValuePageMustNotRun{t: t}), queryPageRegistration(unrelatedPageForViewMustNotRun{t: t}), schemaDescribeRegistration(pb, relation.New(pb, nil, nil)), schemaGetTableRegistration(pb),
		schemaListRegistration(catalog), productrpc.AttachmentListRegistration(pb, mustAttachmentManager(t)),
		historyReadRegistration(unrelatedHistoryReadMustNotRun{t: t}),
		historyPreviewRestoreRegistration(unrelatedHistoryReadMustNotRun{t: t}),
		historyApplyRestoreRegistration(unrelatedHistoryReadMustNotRun{t: t}),
		workCalendarReadRegistration(nil), workCalendarCommitRegistration(nil))
	if err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(w http.ResponseWriter, r *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: r}}, nil
	})
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

type viewOracleCase struct {
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

func readViewOracle(t *testing.T) []viewOracleCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "query-view-python-oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []viewOracleCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil || len(corpus.Cases) != 33 {
		t.Fatalf("view oracle %d: %v", len(corpus.Cases), err)
	}
	return corpus.Cases
}

func viewWireJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestQueryViewProductHTTPReplaysOriginalPythonOracle(t *testing.T) {
	// A named classification is mandatory for every frozen sample; no silent skips.
	excluded := map[string]string{
		"unknown-view-members-forwarded": "Python scripted transport bypassed REST AST rejection; covered separately by the reused REST decoder",
		"paired-null-parent-preserved":   "Go parent fields have omitempty and cannot marshal paired explicit null",
		"extra-group-member-preserved":   "typed GroupRow cannot emit an unknown member",
		"empty-snapshot-preserved":       "typed QuerySnapshot always emits its fixed fields",
		"wrong-snapshot-type":            "typed QuerySnapshot cannot be an array",
		"boolean-page-count":             "typed Page.TotalRows cannot be bool",
		"boolean-group-count":            "typed GroupRow.Count cannot be bool",
		"boolean-parent-count":           "typed ParentCount cannot be bool",
		"boolean-group-offset":           "typed GroupOffset cannot be bool",
		"non-boolean-has-more":           "typed HasMoreGroups cannot be number",
		"transport-error":                "in-process Port has no Python HTTP transport exception",
	}
	probe := &queryViewProbe{}
	mux := queryViewHTTPMux(t, schemaProductStore(t), probe)
	for _, sample := range readViewOracle(t) {
		t.Run(sample.Name, func(t *testing.T) {
			if reason, skip := excluded[sample.Name]; skip {
				delete(excluded, sample.Name)
				t.Skip(reason)
			}
			*probe = queryViewProbe{}
			if sample.Fixture.Failure != nil {
				probe.err = &query.ProductError{Code: "query.snapshot_stale", Path: "snapshot", Message: "Snapshot is stale", Details: map[string]any{"expected": "data_0", "actual": "data_1"}}
			} else if len(sample.AuthorityRequests) != 0 {
				decoder := json.NewDecoder(bytes.NewReader(sample.Fixture.Response))
				decoder.UseNumber()
				if err := decoder.Decode(&probe.result); err != nil {
					t.Fatalf("unclassified typed boundary: %v", err)
				}
			}
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
			got := viewWireJSON(t, recorder.Body.Bytes())
			if !reflect.DeepEqual(got["wire"], viewWireJSON(t, []byte(schemaListWire))) {
				t.Fatal("wire changed")
			}
			delete(got, "wire")
			if want := viewWireJSON(t, sample.Response); !reflect.DeepEqual(got, want) {
				t.Fatalf("frozen Python mismatch\ngot=%v\nwant=%v", got, want)
			}
			if probe.calls != len(sample.AuthorityRequests) {
				t.Fatalf("Port calls %d authority calls %d", probe.calls, len(sample.AuthorityRequests))
			}
		})
	}
	if len(excluded) != 0 {
		t.Fatalf("unknown exclusions: %v", excluded)
	}
}

func TestQueryViewProductHTTPRealGroupingAndRevision(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "分组订单", OperationID: "view-http", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	region := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "地区", "view-region").Definition
	city := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "城市", "view-city").Definition
	amount := createSchemaProductField(t, pb, table.TableID, v2.LogicalNumber, "金额", "view-amount").Definition
	description, err := schemaexecution.Describe(context.Background(), pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(description.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	for _, values := range []struct {
		region, city string
		amount       int
	}{{"A", "a", 10}, {"A", "b", 20}, {"B", "c", 5}} {
		record := core.NewRecord(collection)
		for _, value := range []struct {
			field *v2.FieldDefinition
			value any
		}{{region, values.region}, {city, values.city}, {amount, values.amount}} {
			record.Set(value.field.Identity.PhysicalName, value.value)
			if value.field.Value.Presence.Mode == v2.PresenceCompanion {
				record.Set(value.field.Value.Presence.PhysicalName, true)
			}
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
	mux := queryViewHTTPMux(t, pb, port)
	read := func(offset int) map[string]any {
		t.Helper()
		params, err := json.Marshal(map[string]any{"tableId": table.TableID, "view": query.ViewQuery{
			Query: query.TableQuery{Limit: 1}, Groups: []query.GroupSpec{{Field: region.Identity.PhysicalName}, {Field: city.Identity.PhysicalName}},
			Summaries: []query.SummarySpec{{Field: amount.Identity.PhysicalName, Function: query.AggregateSum}}, GroupOffset: offset, GroupLimit: 1,
		}})
		if err != nil {
			t.Fatal(err)
		}
		envelope := schemaProductRequestForMethod(t, mux, context.Background(), "query.view", string(params), schemaListWire)
		if envelope.Error != nil {
			t.Fatalf("view product error %+v", envelope.Error)
		}
		return viewWireJSON(t, envelope.Result)
	}
	first := read(0)
	last := read(2)
	firstPage := first["page"].(map[string]any)
	lastPage := last["page"].(map[string]any)
	if len(firstPage["rows"].([]any)) != 1 || firstPage["filteredRows"] != json.Number("3") || firstPage["totalRows"] != json.Number("3") {
		t.Fatalf("page: %v", firstPage)
	}
	group := first["groupRows"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(group["key"], []any{"A", "a"}) || group["count"] != json.Number("1") || group["parentCount"] != json.Number("2") {
		t.Fatalf("group: %v", group)
	}
	number := func(v any) float64 {
		t.Helper()
		n, ok := v.(json.Number)
		if !ok {
			t.Fatalf("not a number %v", v)
		}
		f, e := n.Float64()
		if e != nil {
			t.Fatal(e)
		}
		return f
	}
	if number(group["summaries"].([]any)[0]) != 10 || number(group["parentSummaries"].([]any)[0]) != 30 || first["hasMoreGroups"] != true || last["hasMoreGroups"] != false || last["groupOffset"] != json.Number("2") {
		t.Fatalf("independent grouping pages: %v / %v", first, last)
	}
	firstSnapshot := firstPage["snapshot"].(map[string]any)
	lastSnapshot := lastPage["snapshot"].(map[string]any)
	for _, name := range []string{"table", "schemaRevision", "dataRevision", "normalizedQuery"} {
		if !reflect.DeepEqual(firstSnapshot[name], lastSnapshot[name]) {
			t.Fatalf("group pagination changed %s: %v / %v", name, firstSnapshot, lastSnapshot)
		}
	}
	snap := firstPage["snapshot"].(map[string]any)
	if snap["table"] != table.TableID {
		t.Fatalf("snapshot table: %v", snap)
	}
	rawSnapshot, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot query.QuerySnapshot
	if err := json.Unmarshal(rawSnapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	validation, err := port.ValidateSnapshot(context.Background(), snapshot, nil)
	if err != nil || !validation.Valid || snapshot.SchemaRevision != validation.CurrentSchemaRevision || snapshot.DataRevision != validation.CurrentDataRevision {
		t.Fatalf("snapshot pairing: %+v / %+v / %v", snapshot, validation, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := queryViewRegistration(port).Handler(ctx, json.RawMessage(`{"tableId":"`+table.TableID+`","view":{}}`)); err != context.Canceled {
		t.Fatalf("real Port cancelled request: %v", err)
	}
}

type unrelatedPageForViewMustNotRun struct{ t *testing.T }

func (probe unrelatedPageForViewMustNotRun) QueryPage(context.Context, string, query.TableQuery) (query.Page, error) {
	probe.t.Fatal("view fixture must not execute query.page")
	return query.Page{}, nil
}
