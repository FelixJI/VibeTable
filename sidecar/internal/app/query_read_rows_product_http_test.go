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

// Keep the real Product dispatcher/HTTP boundary while selecting only the readRows dependency.
func queryReadRowsHTTPMux(t *testing.T, pb *pocketbase.PocketBase, registration productrpc.Registration) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}, queryPageRegistration(unrelatedQueryPageMustNotRun{t: t}), productrpc.ReconcileRegistration(catalog), queryValidateSnapshotRegistration(unrelatedQueryValidateSnapshotMustNotRun{t: t}),
		lookupListRegistration(relation.New(pb, nil, nil)),
		registration, queryCursorOpenRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		querySelectionOpenRegistration(unrelatedSelectionMustNotRun{t: t}),
		queryViewRegistration(unrelatedViewMustNotRun{t: t}),
		lookupValuePageRegistration(unrelatedLookupValuePageMustNotRun{t: t}),
		relationSearchTargetsRegistration(unrelatedRelationSearchMustNotRun{t: t}),
		unrelatedRelationInspectRegistration(t),
		relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}),
		fieldSettingsDescribeRegistration(unrelatedFieldSettingsDescribeMustNotRun{t: t}),
		lookupQueryRegistration(unrelatedLookupQueryMustNotRun{t: t}),
		queryCursorFetchRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		schemaDescribeRegistration(pb, relation.New(pb, nil, nil)), schemaGetTableRegistration(pb),
		schemaListRegistration(catalog), productrpc.AttachmentListRegistration(pb, mustAttachmentManager(t)),
		historyReadRegistration(unrelatedHistoryReadMustNotRun{t: t}))
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

func TestQueryReadRowsProductHTTPReadsPersistedAuthority(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "ReadRows 订单", OperationID: "read-rows-http", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	text := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Text", "read-rows-text")
	payload := createSchemaProductField(t, pb, table.TableID, v2.LogicalJSON, "Payload", "read-rows-json")
	description, err := schemaexecution.Describe(context.Background(), pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(description.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"中文 Cafe\u0301 👩🏽‍💻 \u200fعربي", ""}
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
	mux := queryReadRowsHTTPMux(t, pb, queryReadRowsRegistration(query.NewPort(pb, source)))
	request := func(ctx context.Context, rows []string) productrpc.ResponseEnvelope {
		raw, err := json.Marshal(map[string]any{"tableId": table.TableID, "rowIds": rows})
		if err != nil {
			t.Fatal(err)
		}
		return schemaProductRequestForMethod(t, mux, ctx, "query.readRows", string(raw), schemaListWire)
	}
	response := request(context.Background(), []string{ids[1], "missing00000000", ids[0], ids[1]})
	if response.Error != nil {
		t.Fatalf("real authority: %+v", response.Error)
	}
	var result struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("row count: %s", response.Result)
	}
	expectedPayload, _ := json.Marshal(values)
	for index, want := range []int{1, 0, 1} {
		row := result.Rows[index]
		if row["id"] != ids[want] || row[text.Definition.Identity.PhysicalName] != texts[want] {
			t.Fatalf("order/Unicode/blank: %s", response.Result)
		}
		actualPayload, _ := json.Marshal(row[payload.Definition.Identity.PhysicalName])
		if string(actualPayload) != string(expectedPayload) {
			t.Fatalf("falsy JSON: got %s want %s", actualPayload, expectedPayload)
		}
	}
	for _, count := range []int{200, 201} {
		selected := make([]string, count)
		for index := range selected {
			selected[index] = ids[0]
		}
		response := request(context.Background(), selected)
		if count == 201 {
			if response.Error == nil || response.Error.Code != productrpc.CodeProductData || response.Error.Data["code"] != "query.rows.limit" {
				t.Fatalf("201 rows: %+v", response)
			}
		} else {
			if response.Error != nil {
				t.Fatalf("200 rows: %+v", response.Error)
			}
			if err := json.Unmarshal(response.Result, &result); err != nil || len(result.Rows) != 200 {
				t.Fatalf("200 duplicate rows: %s %v", response.Result, err)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response = request(ctx, ids)
	if response.Error == nil || response.Error.Code != productrpc.CodeInternalError || len(response.Result) != 0 {
		t.Fatalf("cancelled request returned data: %+v", response)
	}
}

func TestQueryReadRowsProductHTTPConsumesFrozenPythonOracle(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "query-read-python-oracle.json"))
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
	if corpus.Producer != "b55f878641bf74c0b49b04222a217c48abf544a7" {
		t.Fatal("unexpected oracle producer")
	}
	pb := schemaProductStore(t)
	probe := &queryReadRowsProbe{}
	mux := queryReadRowsHTTPMux(t, pb, queryReadRowsRegistration(probe))
	consumed := 0
	for _, sample := range corpus.Cases {
		if sample.Request.Method != "query.readRows" {
			continue
		}
		t.Run(sample.Name, func(t *testing.T) {
			if sample.Fixture.Failure != nil && *sample.Fixture.Failure == "transport" {
				t.Skip("frozen Python HTTP transport failure is not a Go domain result")
			}
			consumed++
			*probe = queryReadRowsProbe{}
			if sample.Fixture.Failure != nil {
				// Independent scripted producer input, not reconstructed from expected output.
				probe.err = &query.ProductError{Code: "query.snapshot_stale", Path: "snapshot", Message: "Snapshot is stale", Details: map[string]any{"expected": "data_0", "actual": "data_1"}}
			} else {
				var fixture struct {
					Rows []map[string]any `json:"rows"`
				}
				decoder := json.NewDecoder(bytes.NewReader(sample.Fixture.Response))
				decoder.UseNumber()
				if err := decoder.Decode(&fixture); err != nil {
					t.Fatal(err)
				}
				probe.rows = fixture.Rows
			}
			response := schemaProductRequestForMethod(t, mux, context.Background(), "query.readRows", string(sample.Request.Params), schemaListWire)
			if !reflect.DeepEqual(response.Error, sample.Response.Error) {
				t.Fatalf("frozen error: got %+v want %+v", response.Error, sample.Response.Error)
			}
			if len(sample.Response.Result) != 0 {
				// Number tokens preserve false/0 and the frozen -0.0 distinction.
				decode := func(raw []byte) any {
					var value any
					decoder := json.NewDecoder(bytes.NewReader(raw))
					decoder.UseNumber()
					if err := decoder.Decode(&value); err != nil {
						t.Fatal(err)
					}
					return value
				}
				if !reflect.DeepEqual(decode(response.Result), decode(sample.Response.Result)) {
					t.Fatalf("frozen result: got %s want %s", response.Result, sample.Response.Result)
				}
			}
			if probe.calls != len(sample.AuthorityRequests) {
				t.Fatalf("authority calls=%d want=%d", probe.calls, len(sample.AuthorityRequests))
			}
		})
	}
	if consumed != 12 {
		t.Fatalf("consumed %d frozen readRows cases; want 12", consumed)
	}
}
