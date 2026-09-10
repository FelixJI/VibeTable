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
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type unrelatedFieldSettingsDescribeMustNotRun struct{ t *testing.T }

func (p unrelatedFieldSettingsDescribeMustNotRun) DescribeFieldSettings(context.Context, string, string) (fieldSettingsDescribeResult, error) {
	p.t.Helper()
	p.t.Fatal("unrelated fixture must not invoke field.settings.describe")
	return fieldSettingsDescribeResult{}, nil
}
func fieldSettingsDescribeHTTPMux(t *testing.T, pb *pocketbase.PocketBase, registration productrpc.Registration) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		unrelatedPresetRegistration(t, "preset.list"),
		unrelatedPresetRegistration(t, "preset.save"),
		unrelatedPresetRegistration(t, "preset.delete"),
		mutationPreviewRegistration(unrelatedMutationProductMustNotRun{t: t}),
		mutationApplyRegistration(unrelatedMutationProductMustNotRun{t: t}),
		unrelatedRelationInspectRegistration(t),
		registration, relationSearchTargetsRegistration(unrelatedRelationSearchMustNotRun{t: t}), productrpc.ReconcileRegistration(catalog), lookupListRegistration(relation.New(pb, nil, nil)),
		queryPageRegistration(relationSearchUnrelatedPage{t: t}), schemaDescribeRegistration(pb, relation.New(pb, nil, nil)), schemaGetTableRegistration(pb), schemaListRegistration(catalog),
		queryViewRegistration(unrelatedViewMustNotRun{t: t}),
		lookupQueryRegistration(unrelatedLookupQueryMustNotRun{t: t}),
		queryValidateSnapshotRegistration(unrelatedQueryValidateSnapshotMustNotRun{t: t}),
		lookupValuePageRegistration(unrelatedLookupValuePageMustNotRun{t: t}),
		relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}),
		queryReadRowsRegistration(unrelatedQueryReadRowsMustNotRun{t: t}),
		queryCursorOpenRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		queryCursorFetchRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		querySelectionOpenRegistration(unrelatedSelectionMustNotRun{t: t}),
		productrpc.AttachmentListRegistration(pb, mustAttachmentManager(t)), historyReadRegistration(unrelatedHistoryReadMustNotRun{t: t}),
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

func TestFieldSettingsDescribeProductHTTPReplaysFrozenPython(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "field-settings-describe-python-oracle.json"))
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
			Requests []struct {
				Path  string            `json:"path"`
				Query map[string]string `json:"query"`
			} `json:"authorityRequests"`
			Response json.RawMessage `json:"response"`
			Boundary *string         `json:"typedGoBoundary"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &corpus); err != nil || len(corpus.Cases) != 30 {
		t.Fatalf("corpus %v count%d", err, len(corpus.Cases))
	}
	probe := &fieldSettingsDescribeProbe{}
	mux := fieldSettingsDescribeHTTPMux(t, schemaProductStore(t), fieldSettingsDescribeRegistration(probe))
	excluded := map[string]bool{"empty-response-object": true, "dynamic-response-pass-through": true, "wrong-shaped-response-object": true, "null-response": true, "array-response": true, "transport-error": true}
	replayed, boundaries := 0, 0
	for _, sample := range corpus.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			if (sample.Boundary != nil) != excluded[sample.Name] {
				t.Fatal("unreviewed typed boundary classification")
			}
			if sample.Boundary != nil {
				boundaries++
				t.Skip(*sample.Boundary)
			}
			replayed++
			*probe = fieldSettingsDescribeProbe{}
			if len(sample.Requests) > 0 {
				if sample.Fixture.Failure != nil {
					probe.err = &v2.ProductError{Code: "field.not_found", Path: "fieldId", Message: "field was not found", Details: map[string]any{"fieldId": "missing"}}
				} else if err := json.Unmarshal(sample.Fixture.Response, &probe.result); err != nil {
					t.Fatal(err)
				}
			}
			body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": sample.Name, "method": "field.settings.describe", "params": sample.Request.Params, "wire": json.RawMessage(schemaListWire)})
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, productRPCPath, bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(recorder, request)
			got := relationSearchWireJSON(t, recorder.Body.Bytes())
			if !reflect.DeepEqual(got["wire"], relationSearchWireJSON(t, []byte(schemaListWire))) {
				t.Fatal("wire changed")
			}
			delete(got, "wire")
			if want := relationSearchWireJSON(t, sample.Response); !reflect.DeepEqual(got, want) {
				t.Fatalf("Python wire mismatch\ngot=%v\nwant=%v", got, want)
			}
			if probe.calls != len(sample.Requests) {
				t.Fatalf("calls%d want%d", probe.calls, len(sample.Requests))
			}
			if probe.calls == 1 {
				if sample.Requests[0].Path != "/api/vibetable/v2/field-settings/"+probe.table || sample.Requests[0].Query["fieldId"] != probe.field {
					t.Fatal("request values changed")
				}
			}
		})
	}
	if replayed != 24 || boundaries != 6 {
		t.Fatalf("replay%d boundaries%d", replayed, boundaries)
	}
}
func TestFieldSettingsDescribeProductHTTPReadsRealAuthorityWithoutWrites(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "字段设置", OperationID: "settings-table", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	field := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "中文 Cafe\u0301", "settings-field")
	description, err := schemaexecution.Describe(context.Background(), pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(description.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(collection)
	row.Set(field.Definition.Identity.PhysicalName, "中文 Cafe\u0301")
	if field.Definition.Value.Presence.Mode == v2.PresenceCompanion {
		row.Set(field.Definition.Value.Presence.PhysicalName, true)
	}
	if err := pb.Save(row); err != nil {
		t.Fatal(err)
	}
	catalog := fieldchange.NewCatalog(pb)
	store := fieldchange.NewPocketBasePlanStore(pb)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(pb, store)
	schema, err := schemacore.New(catalog, planner, executor)
	if err != nil {
		t.Fatal(err)
	}
	mux := fieldSettingsDescribeHTTPMux(t, pb, fieldSettingsDescribeRegistration(fieldSettingsDescribeDomain{schema: schema, fields: catalog}))
	before := previewAuthorityState(t, pb, description.PhysicalName)
	for _, id := range []string{"", field.FieldID, "missing"} {
		params := map[string]string{"tableId": table.TableID}
		if id != "" {
			params["fieldId"] = id
		}
		raw, _ := json.Marshal(params)
		response := schemaProductRequestForMethod(t, mux, context.Background(), "field.settings.describe", string(raw), schemaListWire)
		if id == "missing" {
			if response.Error == nil || response.Error.Code != productrpc.CodeProductData {
				t.Fatalf("missing field:%+v", response)
			}
		} else {
			if response.Error != nil {
				t.Fatal(response.Error)
			}
			var result fieldSettingsDescribeResult
			if err = json.Unmarshal(response.Result, &result); err != nil {
				t.Fatal(err)
			}
			if result.TableID != table.TableID || result.FieldID != id || len(result.Capabilities) == 0 || result.SchemaRevision == "" {
				t.Fatalf("invalid description:%+v", result)
			}
			if id != "" && (result.Definition == nil || !reflect.DeepEqual(result.Definition, field.Definition)) {
				t.Fatal("field definition changed")
			}
		}
	}
	if after := previewAuthorityState(t, pb, description.PhysicalName); !reflect.DeepEqual(before, after) {
		t.Fatal("read altered authority")
	}
}
