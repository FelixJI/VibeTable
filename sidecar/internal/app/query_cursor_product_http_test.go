package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

type cursorHTTPFixture struct {
	pb                          *pocketbase.PocketBase
	port                        *query.Port
	mux                         http.Handler
	table, textField, jsonField string
	ids, texts                  []string
	payload                     map[string]any
}

func newCursorHTTPFixture(t *testing.T) cursorHTTPFixture {
	t.Helper()
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "Cursor 订单", OperationID: "cursor-http", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	text := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Text", "cursor-text")
	payload := createSchemaProductField(t, pb, table.TableID, v2.LogicalJSON, "Payload", "cursor-json")
	description, err := schemaexecution.Describe(context.Background(), pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(description.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	f := cursorHTTPFixture{pb: pb, table: table.TableID, textField: text.Definition.Identity.PhysicalName, jsonField: payload.Definition.Identity.PhysicalName,
		texts:   []string{"", "a", "中文 Cafe\u0301 👩🏽‍💻 \u200fعربي"},
		payload: map[string]any{"zero": 0, "false": false, "null": nil, "blank": "", "array": []any{}, "object": map[string]any{}},
	}
	for _, value := range f.texts {
		record := core.NewRecord(collection)
		record.Set(f.textField, value)
		record.Set(f.jsonField, f.payload)
		for _, field := range []*v2.FieldDefinition{text.Definition, payload.Definition} {
			if field.Value.Presence.Mode == v2.PresenceCompanion {
				record.Set(field.Value.Presence.PhysicalName, true)
			}
		}
		if err := pb.Save(record); err != nil {
			t.Fatal(err)
		}
		f.ids = append(f.ids, record.Id)
	}
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	f.port = query.NewPort(pb, source)
	f.mux = cursorProductHTTPMux(t, pb, f.port)
	return f
}

func cursorProductHTTPMux(t *testing.T, pb *pocketbase.PocketBase, port interface {
	OpenCursor(context.Context, string, query.TableQuery) (query.CursorWindow, error)
	FetchCursor(context.Context, string) (query.CursorWindow, error)
}) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	},
		unrelatedContentRegistration(t, "contentProfile.commit"),
		unrelatedContentRegistration(t, "contentProfile.delete"),
		unrelatedContentRegistration(t, "contentProfile.load"),
		unrelatedContentRegistration(t, "recordDocumentLink.commit"),
		unrelatedContentRegistration(t, "recordDocumentLink.delete"),
		unrelatedContentRegistration(t, "recordDocumentLink.list"),
		unrelatedContentRegistration(t, "recordDocumentLink.repair"), queryPageRegistration(unrelatedQueryPageMustNotRun{t: t}), productrpc.ReconcileRegistration(catalog), queryValidateSnapshotRegistration(unrelatedQueryValidateSnapshotMustNotRun{t: t}),
		unrelatedPresetRegistration(t, "preset.list"),
		unrelatedPresetRegistration(t, "preset.save"),
		unrelatedPresetRegistration(t, "preset.delete"),
		mutationPreviewRegistration(unrelatedMutationProductMustNotRun{t: t}),
		mutationApplyRegistration(unrelatedMutationProductMustNotRun{t: t}),
		lookupListRegistration(relation.New(pb, nil, nil)),
		queryCursorOpenRegistration(port), queryCursorFetchRegistration(port),
		querySelectionOpenRegistration(unrelatedSelectionMustNotRun{t: t}),
		queryViewRegistration(unrelatedViewMustNotRun{t: t}),
		lookupValuePageRegistration(unrelatedLookupValuePageMustNotRun{t: t}),
		relationSearchTargetsRegistration(unrelatedRelationSearchMustNotRun{t: t}),
		unrelatedRelationInspectRegistration(t),
		relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}),
		fieldSettingsDescribeRegistration(unrelatedFieldSettingsDescribeMustNotRun{t: t}),
		lookupQueryRegistration(unrelatedLookupQueryMustNotRun{t: t}),
		queryReadRowsRegistration(unrelatedQueryReadRowsMustNotRun{t: t}),
		schemaDescribeRegistration(pb, relation.New(pb, nil, nil)), schemaGetTableRegistration(pb),
		schemaListRegistration(catalog), productrpc.AttachmentListRegistration(pb, mustAttachmentManager(t)),
		unrelatedDashboardRegistration(t, "insights.dashboardQueryLimits"),
		unrelatedDashboardRegistration(t, "insights.deleteDashboardWorkspace"),
		unrelatedDashboardRegistration(t, "insights.executeDashboardQuery"),
		unrelatedDashboardRegistration(t, "insights.listDashboards"),
		unrelatedDashboardRegistration(t, "insights.panelManifest"),
		unrelatedDashboardRegistration(t, "insights.readDashboardWorkspace"),
		unrelatedDashboardRegistration(t, "insights.saveDashboardDraft"),
		unrelatedSurfaceRegistration(t, "interface.list"),
		unrelatedSurfaceRegistration(t, "interface.load"),
		unrelatedSurfaceRegistration(t, "interface.commit"),
		unrelatedSurfaceRegistration(t, "interface.delete"),
		historyReadRegistration(unrelatedHistoryReadMustNotRun{t: t}),
		historyPreviewRestoreRegistration(unrelatedHistoryReadMustNotRun{t: t}),
		historyApplyRestoreRegistration(unrelatedHistoryReadMustNotRun{t: t}),
		workCalendarReadRegistration(nil), workCalendarCommitRegistration(nil))
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

func (f cursorHTTPFixture) request(t *testing.T, ctx context.Context, method string, params any) productrpc.ResponseEnvelope {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return schemaProductRequestForMethod(t, f.mux, ctx, method, string(raw), schemaListWire)
}

func cursorHTTPWindow(t *testing.T, response productrpc.ResponseEnvelope) query.CursorWindow {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("cursor HTTP error: %+v", response.Error)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Result, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 6 {
		t.Fatalf("unexpected cursor fields: %s", response.Result)
	}
	for _, name := range []string{"rows", "nextCursor", "hasMore", "filteredRows", "totalRows", "querySnapshot"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("missing %s: %s", name, response.Result)
		}
	}
	var window query.CursorWindow
	if err := json.Unmarshal(response.Result, &window); err != nil {
		t.Fatal(err)
	}
	if window.Rows == nil {
		t.Fatal("cursor rows must be an array")
	}
	return window
}

func TestQueryCursorProductHTTPTraversesPersistedPages(t *testing.T) {
	f := newCursorHTTPFixture(t)
	for _, limit := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("limit-%d", limit), func(t *testing.T) {
			input := query.TableQuery{Limit: limit, Sorts: []query.SortCondition{{Field: f.textField, Direction: query.SortAscending}}}
			window := cursorHTTPWindow(t, f.request(t, context.Background(), "query.cursorOpen", map[string]any{"tableId": f.table, "query": input}))
			seen := []string{}
			for page := 0; ; page++ {
				if page >= len(f.ids) {
					t.Fatal("cursor failed to terminate")
				}
				wantRows := min(limit, len(f.ids)-len(seen))
				if len(window.Rows) != wantRows || window.TotalRows != 3 || window.FilteredRows != 3 {
					t.Fatalf("window counts: %+v", window)
				}
				validation, err := f.port.ValidateSnapshot(context.Background(), window.Snapshot, &input)
				if err != nil || !validation.Valid {
					t.Fatalf("invalid authority snapshot: %+v %v", validation, err)
				}
				for _, row := range window.Rows {
					index := len(seen)
					if row["id"] != f.ids[index] || row[f.textField] != f.texts[index] {
						t.Fatalf("persisted row %d: %+v", index, row)
					}
					actual, err := json.Marshal(row[f.jsonField])
					if err != nil {
						t.Fatal(err)
					}
					expected, err := json.Marshal(f.payload)
					if err != nil {
						t.Fatal(err)
					}
					if string(actual) != string(expected) {
						t.Fatalf("falsy JSON: %s want %s", actual, expected)
					}
					seen = append(seen, row["id"].(string))
				}
				if len(seen) == len(f.ids) {
					if window.HasMore || window.NextCursor != nil {
						t.Fatalf("terminal window: %+v", window)
					}
					break
				}
				if !window.HasMore || window.NextCursor == nil || *window.NextCursor == "" {
					t.Fatalf("missing continuation: %+v", window)
				}
				window = cursorHTTPWindow(t, f.request(t, context.Background(), "query.cursorFetch", map[string]any{"cursor": *window.NextCursor}))
			}
			if !reflect.DeepEqual(seen, f.ids) {
				t.Fatalf("traversal: %v want %v", seen, f.ids)
			}
		})
	}
	input := query.TableQuery{Limit: 2, Filters: []query.FilterExpression{{Field: f.textField, Operator: query.OperatorEqual, Value: "absent"}}}
	empty := cursorHTTPWindow(t, f.request(t, context.Background(), "query.cursorOpen", map[string]any{"tableId": f.table, "query": input}))
	if len(empty.Rows) != 0 || empty.HasMore || empty.NextCursor != nil || empty.FilteredRows != 0 || empty.TotalRows != 3 {
		t.Fatalf("empty window: %+v", empty)
	}
}

func TestQueryCursorProductHTTPContinuesSelectionAuthorityCursor(t *testing.T) {
	f := newCursorHTTPFixture(t)
	input := query.TableQuery{Limit: 2, Sorts: []query.SortCondition{{Field: f.textField, Direction: query.SortAscending}}}
	// This is the real authority seam used downstream of Python selectionOpen,
	// not a simulation or proof of the complete Python transport/Host route.
	selection, err := f.port.OpenSelectionProjection(context.Background(), f.table, input)
	if err != nil {
		t.Fatal(err)
	}
	if selection.CursorWindow.NextCursor == nil || len(selection.CursorWindow.Rows) != 2 {
		t.Fatalf("selection window: %+v", selection)
	}
	window := cursorHTTPWindow(t, f.request(t, context.Background(), "query.cursorFetch", map[string]any{"cursor": *selection.CursorWindow.NextCursor}))
	if len(window.Rows) != 1 || window.Rows[0]["id"] != f.ids[2] || window.HasMore || window.NextCursor != nil || window.FilteredRows != 3 || window.TotalRows != 3 {
		t.Fatalf("selection continuation: %+v", window)
	}
	if !reflect.DeepEqual(window.Snapshot, selection.CursorWindow.Snapshot) {
		t.Fatal("selection continuation replaced its signed snapshot")
	}
}

func TestQueryCursorProductHTTPRejectsStaleInvalidAndCancelledRequests(t *testing.T) {
	f := newCursorHTTPFixture(t)
	params := map[string]any{"tableId": f.table, "query": query.TableQuery{Limit: 1}}
	window := cursorHTTPWindow(t, f.request(t, context.Background(), "query.cursorOpen", params))
	if window.NextCursor == nil {
		t.Fatal("missing cursor")
	}
	for _, sample := range []struct {
		method string
		params any
		code   int
		domain string
	}{
		{"query.cursorOpen", map[string]any{"tableId": f.table}, productrpc.CodeInvalidParams, ""},
		{"query.cursorOpen", map[string]any{"tableId": f.table, "query": map[string]any{"offset": 1, "limit": 1}}, productrpc.CodeProductData, "query.cursor.invalid"},
		{"query.cursorFetch", map[string]any{"cursor": ""}, productrpc.CodeInvalidParams, ""},
		{"query.cursorFetch", map[string]any{"cursor": "not-a-cursor"}, productrpc.CodeProductData, "query.cursor.invalid"},
	} {
		response := f.request(t, context.Background(), sample.method, sample.params)
		if response.Error == nil || response.Error.Code != sample.code || len(response.Result) != 0 {
			t.Fatalf("invalid input: %+v", response)
		}
		if sample.domain != "" && response.Error.Data["code"] != sample.domain {
			t.Fatalf("domain error: %+v", response.Error)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for method, input := range map[string]any{"query.cursorOpen": params, "query.cursorFetch": map[string]any{"cursor": *window.NextCursor}} {
		response := f.request(t, ctx, method, input)
		if response.Error == nil || response.Error.Code != productrpc.CodeInternalError || len(response.Result) != 0 {
			t.Fatalf("cancelled cursor returned data: %+v", response)
		}
	}
	// A real schema mutation changes the authoritative revision; no cursor bytes are edited.
	createSchemaProductField(t, f.pb, f.table, v2.LogicalText, "Added", "cursor-stale-field")
	stale := f.request(t, context.Background(), "query.cursorFetch", map[string]any{"cursor": *window.NextCursor})
	if stale.Error == nil || stale.Error.Code != productrpc.CodeProductData || stale.Error.Data["code"] != "query.cursor_stale" || stale.Error.Data["path"] != "cursor" || len(stale.Result) != 0 {
		t.Fatalf("stale cursor: %+v", stale)
	}
}

type cursorOracleCase struct {
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
		Method string `json:"method"`
		Path   string `json:"path"`
		Body   struct {
			Operation string          `json:"operation"`
			Table     string          `json:"tableId"`
			Query     json.RawMessage `json:"query"`
			Cursor    string          `json:"cursor"`
		} `json:"body"`
	} `json:"authorityRequests"`
	Response json.RawMessage `json:"response"`
}

type cursorOracleProbe struct {
	calls                 int
	method, table, cursor string
	input                 query.TableQuery
	window                query.CursorWindow
	err                   error
}

func (p *cursorOracleProbe) OpenCursor(_ context.Context, table string, input query.TableQuery) (query.CursorWindow, error) {
	p.calls++
	p.method, p.table, p.input = "cursor.open", table, input
	return p.window, p.err
}

func (p *cursorOracleProbe) FetchCursor(_ context.Context, cursor string) (query.CursorWindow, error) {
	p.calls++
	p.method, p.cursor = "cursor.fetch", cursor
	return p.window, p.err
}

func readCursorOracle(t *testing.T, name string) []cursorOracleCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", name))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Producer string             `json:"producerCommit"`
		Cases    []cursorOracleCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Producer != "c97c83336e4aa1bdf993fc46a7de57040219fb03" {
		t.Fatal("unexpected Python producer")
	}
	return corpus.Cases
}

func compareCursorOracle(t *testing.T, mux http.Handler, probe *cursorOracleProbe, sample cursorOracleCase) {
	t.Helper()
	*probe = cursorOracleProbe{}
	if sample.Fixture.Failure != nil {
		if *sample.Fixture.Failure != "product" {
			t.Fatal("unsupported scripted failure")
		}
		// These values are the producer's scripted transport exception input;
		// they are never reconstructed from the frozen expected response.
		probe.err = &query.ProductError{Code: "query.snapshot_stale", Path: "snapshot", Message: "Snapshot is stale", Details: map[string]any{"expected": "data_0", "actual": "data_1"}}
	} else if len(sample.AuthorityRequests) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(sample.Fixture.Response))
		decoder.UseNumber()
		if err := decoder.Decode(&probe.window); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": sample.Name, "method": sample.Request.Method, "params": sample.Request.Params, "wire": json.RawMessage(schemaListWire)})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, productRPCPath, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Product HTTP status: %d %s", response.Code, response.Body)
	}
	decode := func(raw []byte) map[string]any {
		var value map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	actual := decode(response.Body.Bytes())
	if !reflect.DeepEqual(actual["wire"], decode([]byte(schemaListWire))) {
		t.Fatal("Product response changed the request wire")
	}
	// Wire is the new Go envelope field. Compare every remaining envelope,
	// result, and error field against the unchanged Python producer output.
	delete(actual, "wire")
	if !reflect.DeepEqual(actual, decode(sample.Response)) {
		t.Fatalf("complete frozen response: got %s want %s", response.Body, sample.Response)
	}
	if probe.calls != len(sample.AuthorityRequests) {
		t.Fatalf("authority calls %d want %d", probe.calls, len(sample.AuthorityRequests))
	}
	for _, expected := range sample.AuthorityRequests {
		if expected.Method != "POST" || expected.Path != "/api/vibetable/v1/query" {
			t.Fatalf("unexpected producer authority seam: %+v", expected)
		}
		if probe.method != expected.Body.Operation || probe.table != expected.Body.Table || probe.cursor != expected.Body.Cursor {
			t.Fatalf("authority input: %+v want %+v", probe, expected.Body)
		}
		if sample.Request.Method == "query.cursorOpen" {
			var input query.TableQuery
			if err := json.Unmarshal(expected.Body.Query, &input); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(probe.input, input) {
				t.Fatalf("typed query: %+v want %+v", probe.input, input)
			}
		}
	}
}

func TestQueryCursorProductHTTPConsumesOriginalPythonOracle(t *testing.T) {
	// The old transport fixture deliberately bypassed REST validation. Exclusions
	// identify that boundary or a DTO shape the typed Go port cannot express.
	excluded := map[string]string{
		"open-unicode-falsy":            "limit=0 is rejected by REST before the typed Port",
		"open-terminal-window":          "limit=0 is rejected by REST before the typed Port",
		"open-malformed-snapshot":       "limit=0 is rejected by REST; null snapshot is not a typed QuerySnapshot",
		"open-inconsistent-null-cursor": "limit=0 is rejected by REST; legal-input mismatch is in the supplement",
		"open-product-error":            "limit=0 is rejected by REST before the scripted authority error",
		"open-transport-error":          "limit=0 is rejected by REST; HTTP transport failure is absent from the in-process Port",
		"fetch-unicode-cursor":          "partial snapshot lacks required typed QuerySnapshot fields; supplement preserves projection",
		"fetch-terminal-window":         "partial snapshot lacks required typed QuerySnapshot fields; supplement preserves terminal output",
		"fetch-empty-next-cursor":       "partial snapshot lacks required typed QuerySnapshot fields; supplement retains empty-string semantics",
		"fetch-malformed-next-cursor":   "numeric nextCursor cannot be represented by *string",
		"fetch-transport-error":         "HTTP transport failure is absent from the in-process Port; domain unknown-error mapping is tested separately",
	}
	pb := schemaProductStore(t)
	probe := &cursorOracleProbe{}
	mux := cursorProductHTTPMux(t, pb, probe)
	consumed, skipped := 0, 0
	for _, sample := range readCursorOracle(t, "query-window-python-oracle.json") {
		if sample.Request.Method == "query.page" {
			continue
		}
		t.Run(sample.Name, func(t *testing.T) {
			if reason, ok := excluded[sample.Name]; ok {
				skipped++
				t.Skip(reason)
			}
			compareCursorOracle(t, mux, probe, sample)
			consumed++
		})
	}
	if consumed != 6 || skipped != len(excluded) {
		t.Fatalf("original cursor corpus: consumed %d excluded %d", consumed, skipped)
	}
}

func TestQueryCursorProductHTTPConsumesTypedPythonOracle(t *testing.T) {
	cases := readCursorOracle(t, "query-cursor-typed-python-oracle.json")
	if len(cases) != 13 {
		t.Fatalf("typed cursor cases: %d", len(cases))
	}
	pb := schemaProductStore(t)
	probe := &cursorOracleProbe{}
	mux := cursorProductHTTPMux(t, pb, probe)
	for _, sample := range cases {
		t.Run(sample.Name, func(t *testing.T) {
			if sample.Request.Method != "query.cursorOpen" && sample.Request.Method != "query.cursorFetch" {
				t.Fatal("unexpected typed oracle method")
			}
			compareCursorOracle(t, mux, probe, sample)
		})
	}
}
