package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type unrelatedLookupQueryMustNotRun struct{ t *testing.T }

func (p unrelatedLookupQueryMustNotRun) Describe(context.Context, string) (relation.CatalogResult, error) {
	p.t.Helper()
	p.t.Fatal("unrelated fixture must not invoke lookup.query Describe")
	return relation.CatalogResult{}, nil
}
func (p unrelatedLookupQueryMustNotRun) QueryLookups(context.Context, relation.LookupQueryRequest) (relation.LookupQueryResult, error) {
	p.t.Helper()
	p.t.Fatal("unrelated fixture must not invoke lookup.query QueryLookups")
	return relation.LookupQueryResult{}, nil
}

func lookupQueryHTTPMux(t *testing.T, pb *pocketbase.PocketBase, registration productrpc.Registration) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		unrelatedPresetRegistration(t, "preset.list"),
		unrelatedPresetRegistration(t, "preset.save"),
		unrelatedPresetRegistration(t, "preset.delete"),
		mutationPreviewRegistration(unrelatedMutationProductMustNotRun{t: t}),
		mutationApplyRegistration(unrelatedMutationProductMustNotRun{t: t}),
		lookupValuePageRegistration(unrelatedLookupValuePageMustNotRun{t: t}),
		registration, relationSearchTargetsRegistration(unrelatedRelationSearchMustNotRun{t: t}),
		unrelatedRelationInspectRegistration(t), productrpc.ReconcileRegistration(catalog), queryValidateSnapshotRegistration(unrelatedQueryValidateSnapshotMustNotRun{t: t}),
		lookupListRegistration(relation.New(pb, nil, nil)),
		queryPageRegistration(relationSearchUnrelatedPage{t: t}), schemaDescribeRegistration(pb, relation.New(pb, nil, nil)), schemaGetTableRegistration(pb), schemaListRegistration(catalog),
		queryViewRegistration(unrelatedViewMustNotRun{t: t}),
		fieldSettingsDescribeRegistration(unrelatedFieldSettingsDescribeMustNotRun{t: t}),
		relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}),
		queryReadRowsRegistration(unrelatedQueryReadRowsMustNotRun{t: t}),
		queryCursorOpenRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		queryCursorFetchRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		querySelectionOpenRegistration(unrelatedSelectionMustNotRun{t: t}),
		productrpc.AttachmentListRegistration(pb, mustAttachmentManager(t)),
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

func TestLookupQueryProductHTTPReplaysFrozenPython(t *testing.T) {
	// Exclusions describe input representation, never erase dynamic rows or public domain errors.
	pathGap := "original catalog omits LookupDescriptor.path; typed path is emitted and changes lookupRevision"
	excluded := map[string]string{
		"dynamic-rows": pathGap + "; original snapshot.normalizedQuery omits required typed offset/limit",
		"empty-selection-and-negative-generation":       pathGap,
		"contract-and-whitespace-are-not-normalized":    pathGap,
		"group-translation-and-parent-first-occurrence": pathGap + "; parentSummaries empty array cannot survive omitempty with parentCount",
		"single-group-null-key":                         pathGap,
		"duplicate-physical-last-wins":                  pathGap,
		"duplicate-fieldrefs-preserved":                 pathGap,
		"group-direction-before-query-error":            pathGap,
		"group-nonobject":                               pathGap,
		"group-empty-field":                             pathGap,
		"unknown-field-after-query":                     pathGap,
		"query-error-before-unknown-field":              pathGap + "; public domain error separately replayed with complete typed catalog",
		"window-before-column-projection":               pathGap + "; unselected descriptor omits typed required fields",
		"catalog-unselected-malformed":                  pathGap + "; unselected descriptor omits typed required fields",
		"catalog-nonobject":                             "typed CatalogResult cannot be an array root",
		"view-nonobject":                                pathGap + "; typed LookupQueryResult cannot be an array root",
		"view-missing-rows":                             pathGap + "; typed Page always emits rows",
		"view-boolean-count":                            pathGap + "; typed TotalRows cannot be bool",
		"view-missing-groupRows":                        pathGap + "; typed result always emits groupRows",
		"view-nonboolean-window":                        pathGap + "; typed HasMoreGroups cannot be integer",
		"group-missing-summaries":                       pathGap + "; typed GroupRow always emits summaries",
		"group-boolean-count":                           pathGap + "; typed GroupRow.Count cannot be bool",
		"group-empty-key":                               pathGap,
		"group-missing-parent":                          pathGap,
		"snapshot-open-object":                          pathGap + "; typed QuerySnapshot cannot retain arbitrary missing/extra fields",
		"negative-authority-counts":                     pathGap,
		"page-product-error":                            pathGap + "; public domain error separately replayed with complete typed catalog",
		"catalog-transport-error":                       "direct domain service has no first-hop Python HTTP transport outage",
		"page-transport-error":                          pathGap + "; direct domain service has no second-hop Python HTTP transport outage",
	}
	probe := &lookupQueryProbe{}
	mux := lookupQueryHTTPMux(t, schemaProductStore(t), lookupQueryRegistration(probe))
	replayed := 0
	for _, sample := range lookupQueryOracle(t) {
		t.Run(sample.Name, func(t *testing.T) {
			if reason, found := excluded[sample.Name]; found {
				delete(excluded, sample.Name)
				t.Skip(reason)
			}
			*probe = lookupQueryProbe{}
			if len(sample.AuthorityRequests) > 0 {
				if sample.Fixture.Failure != nil {
					path := "schemaRevision"
					probe.describeErr = &mutation.ProductError{Code: "lookup.schema_revision_conflict", Path: &path, Message: "catalog revision conflict", Details: map[string]any{"phase": "catalog"}}
				} else {
					if err := json.Unmarshal(sample.Fixture.Catalog, &probe.catalog); err != nil {
						t.Fatal(err)
					}
					if len(sample.AuthorityRequests) > 1 {
						if err := json.Unmarshal(sample.Fixture.Page, &probe.view); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			body := lookupQueryBytes(t, map[string]any{"jsonrpc": "2.0", "id": sample.Name,
				"method": "lookup.query", "params": sample.Request.Params, "wire": json.RawMessage(schemaListWire)})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, productRPCPath, bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK && recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
			}
			actual := relationSearchWireJSON(t, recorder.Body.Bytes())
			if !reflect.DeepEqual(actual["wire"], relationSearchWireJSON(t, []byte(schemaListWire))) {
				t.Fatal("wire changed")
			}
			delete(actual, "wire")
			if want := relationSearchWireJSON(t, sample.Response); !reflect.DeepEqual(actual, want) {
				t.Fatalf("complete original mismatch got=%s want=%s", recorder.Body, sample.Response)
			}
			if len(probe.calls) != len(sample.AuthorityRequests) {
				t.Fatalf("calls=%v want=%d", probe.calls, len(sample.AuthorityRequests))
			}
			if len(probe.calls) == 2 {
				var request struct {
					Body relation.LookupQueryRequest `json:"body"`
				}
				if err := json.Unmarshal(sample.AuthorityRequests[1], &request); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(probe.input, request.Body) {
					t.Fatalf("translated query mismatch %+v / %+v", probe.input, request.Body)
				}
			}
			replayed++
		})
	}
	if len(excluded) != 0 || replayed != 10 {
		t.Fatalf("unaccounted corpus exclusions=%v replayed=%d", excluded, replayed)
	}
}

func TestLookupQueryProductHTTPSecondHopPublicError(t *testing.T) {
	// Supplemental complete typed input; the original page error's path-omitting catalog is unchanged.
	probe, params := lookupQueryTypedFixture(t, false)
	path := "schemaRevision"
	probe.queryErr = &mutation.ProductError{Code: "lookup.schema_revision_conflict", Path: &path, Message: "page revision conflict", Details: map[string]any{"phase": "page"}}
	mux := lookupQueryHTTPMux(t, schemaProductStore(t), lookupQueryRegistration(probe))
	response := schemaProductRequestForMethod(t, mux, context.Background(), "lookup.query", string(lookupQueryBytes(t, params)), schemaListWire)
	var original map[string]any
	for _, sample := range lookupQueryOracle(t) {
		if sample.Name == "page-product-error" {
			original = relationSearchWireJSON(t, sample.Response)
		}
	}
	actual := relationSearchWireJSON(t, lookupQueryBytes(t, response))
	if !reflect.DeepEqual(actual["error"], original["error"]) || len(probe.calls) != 2 {
		t.Fatalf("error=%v calls=%v", actual, probe.calls)
	}
}

func TestLookupQueryProductHTTPUsesRealAuthorityWithoutWrites(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "Lookup查询", OperationID: "lookup-query-table", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	label := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "名称", "lookup-query-label")
	defaults, err := v2.RecommendedDefaults(v2.LogicalRelation)
	if err != nil {
		t.Fatal(err)
	}
	related := applySchemaProductField(t, pb, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID,
		Draft: &v2.FieldDraft{DisplayName: "客户", LogicalType: v2.LogicalRelation, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
			Relation: &v2.RelationSpec{TargetTableID: table.TableID, Cardinality: "one", DeletePolicy: "setNull", DisplayField: label.FieldID}},
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "来源", ReciprocalCardinality: "many", SourceDisplayFieldID: label.FieldID}}, "lookup-query-relation")
	defaults, err = v2.RecommendedDefaults(v2.LogicalLookup)
	if err != nil {
		t.Fatal(err)
	}
	lookup := applySchemaProductField(t, pb, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID,
		Draft: &v2.FieldDraft{DisplayName: "客户名", LogicalType: v2.LogicalLookup, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
			Lookup: &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: related.FieldID}}, TargetFieldID: label.FieldID}}}, "lookup-query-field")
	definition, err := schemaexecution.Describe(context.Background(), pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	for index, text := range []string{"Cafe\u0301 👩🏽‍💻", "中文", "订单"} {
		record := core.NewRecord(collection)
		record.Id = fmt.Sprintf("lookupquery%04d", index+1)
		record.Set(label.Definition.Identity.PhysicalName, text)
		if label.Definition.Value.Presence.Mode == v2.PresenceCompanion {
			record.Set(label.Definition.Value.Presence.PhysicalName, true)
		}
		if index == 2 {
			record.Set(related.Definition.Identity.PhysicalName, "lookupquery0001")
		}
		if err := pb.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	service := relation.New(pb, query.NewPort(pb, source), nil)
	catalog, err := service.Describe(context.Background(), table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := describeRevision(map[string]any{"schemaRevision": catalog.SchemaRevision, "lookups": catalog.Lookups})
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]any{"contract": "vibetable.lookup-query.v1", "collection": table.TableID, "fieldRefs": []any{lookup.Definition.Identity.PhysicalName}, "query": map[string]any{"offset": 0, "limit": 50}, "requestGeneration": 1, "schemaRevision": catalog.SchemaRevision, "permissionRevision": catalog.SchemaRevision, "lookupRevision": revision}
	mux := lookupQueryHTTPMux(t, pb, lookupQueryRegistration(service))
	before := previewAuthorityState(t, pb, definition.PhysicalName)
	read := func() productrpc.ResponseEnvelope {
		t.Helper()
		return schemaProductRequestForMethod(t, mux, context.Background(), "lookup.query", string(lookupQueryBytes(t, params)), schemaListWire)
	}
	response := read()
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	result := relationSearchWireJSON(t, response.Result)
	if result["totalRows"] != json.Number("3") || len(result["rows"].([]any)) != 3 {
		t.Fatal(result)
	}
	found := false
	for _, value := range result["rows"].([]any) {
		row := value.(map[string]any)
		if row["id"] == "lookupquery0003" {
			cell := row[lookup.Definition.Identity.PhysicalName].(map[string]any)
			if cell["value"] != "Cafe\u0301 👩🏽‍💻" || cell["state"] != "ok" {
				t.Fatal(cell)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("source row missing")
	}
	params["query"] = map[string]any{"offset": 1, "limit": 1}
	response = read()
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	result = relationSearchWireJSON(t, response.Result)
	if result["offset"] != json.Number("1") || result["limit"] != json.Number("1") || len(result["rows"].([]any)) != 1 || result["totalRows"] != json.Number("3") {
		t.Fatal(result)
	}
	params["query"] = map[string]any{"offset": 0, "limit": 50, "groups": []any{map[string]any{"fieldRef": label.Definition.Identity.PhysicalName}}}
	response = read()
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	result = relationSearchWireJSON(t, response.Result)
	if len(result["groups"].([]any)) != 3 {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(before, previewAuthorityState(t, pb, definition.PhysicalName)) {
		t.Fatal("successful query/group changed authority or metadata/audit/receipt")
	}
	for _, change := range []func(){
		func() { params["schemaRevision"] = "stale" },
		func() {
			params["schemaRevision"] = catalog.SchemaRevision
			params["query"] = map[string]any{"limit": 0}
		},
		func() { params["query"] = map[string]any{"limit": 50}; params["fieldRefs"] = []any{"missing"} },
	} {
		change()
		if response := read(); response.Error == nil {
			t.Fatal("invalid read accepted")
		}
		if !reflect.DeepEqual(before, previewAuthorityState(t, pb, definition.PhysicalName)) {
			t.Fatal("failed query changed authority")
		}
	}
}
