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

	"github.com/pocketbase/dbx"
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

type unrelatedQueryValidateSnapshotMustNotRun struct{ t *testing.T }

func (stub unrelatedQueryValidateSnapshotMustNotRun) ValidateSnapshot(context.Context, query.QuerySnapshot, *query.TableQuery) (query.SnapshotValidation, error) {
	stub.t.Helper()
	stub.t.Fatal("unrelated query.validateSnapshot authority was invoked")
	return query.SnapshotValidation{}, nil
}

func queryValidateSnapshotHTTPMux(t *testing.T, pb *pocketbase.PocketBase, snapshotRegistration, pageRegistration productrpc.Registration) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	},
		unrelatedPresetRegistration(t, "preset.list"),
		unrelatedPresetRegistration(t, "preset.save"),
		unrelatedPresetRegistration(t, "preset.delete"),
		mutationPreviewRegistration(unrelatedMutationProductMustNotRun{t: t}),
		mutationApplyRegistration(unrelatedMutationProductMustNotRun{t: t}), productrpc.ReconcileRegistration(catalog), snapshotRegistration,
		lookupListRegistration(relation.New(pb, nil, nil)),
		relationSearchTargetsRegistration(unrelatedRelationSearchMustNotRun{t: t}),
		unrelatedRelationInspectRegistration(t),
		relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}),
		lookupQueryRegistration(unrelatedLookupQueryMustNotRun{t: t}),
		fieldSettingsDescribeRegistration(unrelatedFieldSettingsDescribeMustNotRun{t: t}),
		pageRegistration, queryReadRowsRegistration(unrelatedQueryReadRowsMustNotRun{t: t}),
		queryCursorOpenRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		queryCursorFetchRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		querySelectionOpenRegistration(unrelatedSelectionMustNotRun{t: t}),
		queryViewRegistration(unrelatedViewMustNotRun{t: t}),
		lookupValuePageRegistration(unrelatedLookupValuePageMustNotRun{t: t}),
		schemaDescribeRegistration(pb, relation.New(pb, nil, nil)), schemaGetTableRegistration(pb),
		schemaListRegistration(catalog), productrpc.AttachmentListRegistration(pb, mustAttachmentManager(t)),
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

func TestQueryValidateSnapshotProductHTTPValidatesPageAndReadOnlyChanges(t *testing.T) {
	pb := schemaProductStore(t)
	ctx := context.Background()
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "快照 Cafe\u0301 👩🏽‍💻", OperationID: "snapshot-http", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	field := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Text", "snapshot-text")
	description, err := schemaexecution.Describe(ctx, pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(description.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set(field.Definition.Identity.PhysicalName, "中文 Cafe\u0301 👩🏽‍💻")
	if field.Definition.Value.Presence.Mode == v2.PresenceCompanion {
		record.Set(field.Definition.Value.Presence.PhysicalName, true)
	}
	if err := pb.Save(record); err != nil {
		t.Fatal(err)
	}
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(pb, source)
	mux := queryValidateSnapshotHTTPMux(t, pb, queryValidateSnapshotRegistration(port), queryPageRegistration(port))
	input := query.TableQuery{Limit: 50}
	raw, err := json.Marshal(map[string]any{"tableId": table.TableID, "query": input})
	if err != nil {
		t.Fatal(err)
	}
	page := schemaProductRequestForMethod(t, mux, ctx, "query.page", string(raw), schemaListWire)
	if page.Error != nil {
		t.Fatal(page.Error)
	}
	var issued struct {
		Snapshot query.QuerySnapshot `json:"snapshot"`
		Rows     []map[string]any    `json:"rows"`
	}
	if err := json.Unmarshal(page.Result, &issued); err != nil {
		t.Fatal(err)
	}
	if len(issued.Rows) != 1 || issued.Rows[0]["id"] != record.Id {
		t.Fatalf("page did not read persisted row: %s", page.Result)
	}
	send := func(snapshot query.QuerySnapshot, current *query.TableQuery) productrpc.ResponseEnvelope {
		t.Helper()
		params := map[string]any{"snapshot": snapshot}
		if current != nil {
			params["currentQuery"] = current
		}
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		before := valuePageAuthorityState(t, pb, description.PhysicalName)
		response := schemaProductRequestForMethod(t, mux, ctx, "query.validateSnapshot", string(raw), schemaListWire)
		after := valuePageAuthorityState(t, pb, description.PhysicalName)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("snapshot validation wrote authority state")
		}
		return response
	}
	assertValidation := func(current *query.TableQuery, reason string) {
		t.Helper()
		response := send(issued.Snapshot, current)
		if response.Error != nil {
			t.Fatal(response.Error)
		}
		var result query.SnapshotValidation
		if err := json.Unmarshal(response.Result, &result); err != nil {
			t.Fatal(err)
		}
		descriptor, err := source.DescribeQueryTable(ctx, pb, table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		expected := query.SnapshotValidation{Valid: reason == "", Reason: reason, CurrentDataRevision: descriptor.DataRevision, CurrentSchemaRevision: descriptor.SchemaRevision}
		if !reflect.DeepEqual(result, expected) {
			t.Fatalf("got %+v want %+v", result, expected)
		}
		expectedRaw, err := json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(viewWireJSON(t, response.Result), viewWireJSON(t, expectedRaw)) {
			t.Fatalf("wire shape: %s", response.Result)
		}
	}
	assertValidation(nil, "")
	assertValidation(&input, "")
	changed := input
	changed.Limit = 1
	assertValidation(&changed, "query_changed")
	forged := issued.Snapshot
	forged.SnapshotID = "invalid"
	invalid := send(forged, nil)
	expectedError := &productrpc.ErrorObject{Code: productrpc.CodeProductData, Message: "Product data error", Data: map[string]any{"kind": "product_data_error", "message": "query snapshot id is invalid", "code": "query.snapshot.invalid", "path": "snapshotId", "details": map[string]any{}, "retryable": false}}
	if !reflect.DeepEqual(invalid.Error, expectedError) {
		t.Fatalf("domain error: %+v", invalid.Error)
	}
	if _, err := pb.DB().NewQuery("UPDATE vibetable_tables SET data_revision=data_revision+1 WHERE table_id={:table}").Bind(dbx.Params{"table": table.TableID}).Execute(); err != nil {
		t.Fatal(err)
	}
	assertValidation(nil, "application_write")
	createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Second", "snapshot-schema-change")
	assertValidation(nil, "schema_changed")
}

type querySnapshotOraclePort struct {
	calls  int
	result query.SnapshotValidation
	err    error
}

func (port *querySnapshotOraclePort) ValidateSnapshot(context.Context, query.QuerySnapshot, *query.TableQuery) (query.SnapshotValidation, error) {
	port.calls++
	return port.result, port.err
}

func TestQueryValidateSnapshotProductHTTPMatchesOriginalPythonWire(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "query-validate-snapshot-python-oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []struct {
			Name    string `json:"name"`
			Request struct {
				Params json.RawMessage `json:"params"`
			} `json:"request"`
			Fixture struct {
				Response json.RawMessage `json:"response"`
				Failure  *string         `json:"failure"`
			} `json:"authorityFixture"`
			Calls    []json.RawMessage `json:"authorityRequests"`
			Response struct {
				Result json.RawMessage         `json:"result"`
				Error  *productrpc.ErrorObject `json:"error"`
			} `json:"response"`
			Boundary *string `json:"typedGoBoundary"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	pb := schemaProductStore(t)
	port := &querySnapshotOraclePort{}
	mux := queryValidateSnapshotHTTPMux(t, pb, queryValidateSnapshotRegistration(port), queryPageRegistration(unrelatedPageForViewMustNotRun{t: t}))
	consumed := 0
	for _, sample := range corpus.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			if sample.Boundary != nil {
				return
			}
			consumed++
			*port = querySnapshotOraclePort{}
			if sample.Fixture.Failure != nil {
				switch *sample.Fixture.Failure {
				case "domain":
					port.err = &query.ProductError{Code: "query.snapshot_invalid", Path: "snapshot", Message: "snapshot signature is invalid", Details: map[string]any{"reason": "invalid_signature"}}
				case "invalid-snapshot-id":
					port.err = &query.ProductError{Code: "query.snapshot.invalid", Path: "snapshotId", Message: "query snapshot id is invalid"}
				default:
					t.Fatalf("unhandled expressible failure %s", *sample.Fixture.Failure)
				}
			} else if err := json.Unmarshal(sample.Fixture.Response, &port.result); err != nil {
				t.Fatal(err)
			}
			response := schemaProductRequestForMethod(t, mux, context.Background(), "query.validateSnapshot", string(sample.Request.Params), schemaListWire)
			if !reflect.DeepEqual(response.Error, sample.Response.Error) {
				t.Fatalf("got %+v want %+v", response.Error, sample.Response.Error)
			}
			if len(sample.Response.Result) > 0 {
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
					t.Fatalf("got %s want %s", response.Result, sample.Response.Result)
				}
			}
			if port.calls != len(sample.Calls) {
				t.Fatalf("authority calls=%d want=%d", port.calls, len(sample.Calls))
			}
		})
	}
	if consumed != 20 {
		t.Fatalf("consumed %d cases; want 20", consumed)
	}
}
