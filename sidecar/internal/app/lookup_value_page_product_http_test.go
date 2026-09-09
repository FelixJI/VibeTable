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
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	lookupcalc "github.com/vibetable/vibetable/sidecar/internal/lookup"
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

type unrelatedLookupValuePageMustNotRun struct{ t *testing.T }

func (p unrelatedLookupValuePageMustNotRun) Describe(context.Context, string) (relation.CatalogResult, error) {
	p.t.Fatal("unrelated lookup.valuePage must not describe")
	return relation.CatalogResult{}, nil
}
func (p unrelatedLookupValuePageMustNotRun) LookupValuePage(context.Context, relation.LookupValuePageRequest) (lookupcalc.CellValue, error) {
	p.t.Fatal("unrelated lookup.valuePage must not read a page")
	return lookupcalc.CellValue{}, nil
}
func lookupValuePageHTTPMux(t *testing.T, pb *pocketbase.PocketBase, registration productrpc.Registration) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		mutationPreviewRegistration(unrelatedMutationProductMustNotRun{t: t}),
		mutationApplyRegistration(unrelatedMutationProductMustNotRun{t: t}),
		lookupQueryRegistration(unrelatedLookupQueryMustNotRun{t: t}),
		fieldSettingsDescribeRegistration(unrelatedFieldSettingsDescribeMustNotRun{t: t}),
		queryValidateSnapshotRegistration(unrelatedQueryValidateSnapshotMustNotRun{t: t}),
		registration, productrpc.ReconcileRegistration(catalog), lookupListRegistration(relation.New(pb, nil, nil)),
		queryPageRegistration(unrelatedQueryPageMustNotRun{t: t}), queryViewRegistration(unrelatedViewMustNotRun{t: t}),
		relationSearchTargetsRegistration(unrelatedRelationSearchMustNotRun{t: t}),
		unrelatedRelationInspectRegistration(t),
		relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}),
		queryReadRowsRegistration(unrelatedQueryReadRowsMustNotRun{t: t}), querySelectionOpenRegistration(unrelatedSelectionMustNotRun{t: t}),
		queryCursorOpenRegistration(unrelatedQueryCursorMustNotRun{t: t}), queryCursorFetchRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		schemaDescribeRegistration(pb, relation.New(pb, nil, nil)), schemaGetTableRegistration(pb), schemaListRegistration(catalog),
		productrpc.AttachmentListRegistration(pb, mustAttachmentManager(t)), historyReadRegistration(unrelatedHistoryReadMustNotRun{t: t}))
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

func TestLookupValuePageProductHTTPReplaysOriginalPython(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "lookup-value-page-python-oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Producer   string            `json:"producerCommit"`
		Boundaries map[string]string `json:"typedGoBoundaries"`
		Cases      []struct {
			Name    string `json:"name"`
			Request struct {
				Params json.RawMessage `json:"params"`
			} `json:"request"`
			Fixture struct {
				Catalog, Page json.RawMessage
				Failure       *string
			} `json:"authorityFixture"`
			Requests []struct {
				Method string
				Query  struct {
					TableID string `json:"tableId"`
				}
				Body json.RawMessage
			} `json:"authorityRequests"`
			Response json.RawMessage `json:"response"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Producer != "a19ccd5366d62be6338f628d06b6c5a37484f20f" || len(corpus.Cases) != 39 {
		t.Fatal("wrong original producer/cases")
	}
	excluded := map[string]string{
		"empty-object-result": "CellValue always emits required fields", "extra-result-fields": "CellValue has no undeclared top-level fields",
		"non-object-result": "CellValue cannot be an array", "wrong-result-field-types": "CellValue has typed state/count/provenance",
		"catalog-non-object": "CatalogResult cannot be an array", "catalog-missing-lookups": "CatalogResult emits lookups",
		"catalog-wrong-lookups": "Lookups is a slice", "catalog-missing-schema": "CatalogResult emits schemaRevision",
		"catalog-nontext-schema": "SchemaRevision is text", "catalog-mixed-members": "Lookups cannot contain non-object members",
		"first-matching-field-missing-id": "LookupDescriptor emits fieldId", "catalog-transport-error": "No Python HTTP transport in Describe",
		"page-transport-error": "No Python HTTP transport in LookupValuePage",
	}
	if len(corpus.Boundaries) != len(excluded) {
		t.Fatal("unreviewed typed boundary")
	}
	for name := range excluded {
		if corpus.Boundaries[name] == "" {
			t.Fatal(name)
		}
	}
	p := &valuePageProbe{}
	mux := lookupValuePageHTTPMux(t, schemaProductStore(t), lookupValuePageRegistration(p))
	replayed := 0
	for _, sample := range corpus.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			if reason, skip := excluded[sample.Name]; skip {
				delete(excluded, sample.Name)
				t.Skip(reason)
			}
			replayed++
			*p = valuePageProbe{}
			if err := json.Unmarshal(sample.Fixture.Catalog, &p.catalog); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(sample.Fixture.Page, &p.result); err != nil {
				t.Fatal(err)
			}
			if sample.Fixture.Failure != nil {
				phase := "page"
				if strings.HasPrefix(*sample.Fixture.Failure, "catalog-") {
					phase = "catalog"
				}
				path := "schemaRevision"
				failure := &mutation.ProductError{Code: "lookup.schema_revision_conflict", Message: phase + " revision conflict", Path: &path, Details: map[string]any{"phase": phase}}
				if phase == "catalog" {
					p.catalogErr = failure
				} else {
					p.pageErr = failure
				}
			}
			body := valuePageJSON(t, map[string]any{"jsonrpc": "2.0", "id": sample.Name, "method": "lookup.valuePage", "params": sample.Request.Params, "wire": json.RawMessage(schemaListWire)})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, productRPCPath, bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(recorder, request)
			actual := viewWireJSON(t, recorder.Body.Bytes())
			if !reflect.DeepEqual(actual["wire"], viewWireJSON(t, json.RawMessage(schemaListWire))) {
				t.Fatal("wire identity changed", actual)
			}
			delete(actual, "wire")
			if !reflect.DeepEqual(actual, viewWireJSON(t, sample.Response)) {
				t.Fatalf("got=%s want=%s", recorder.Body.String(), sample.Response)
			}
			if len(p.calls) != len(sample.Requests) {
				t.Fatal(p.calls, sample.Requests)
			}
			if len(sample.Requests) > 0 && (p.calls[0] != "describe" || p.collection != sample.Requests[0].Query.TableID) {
				t.Fatal("catalog request changed", p.calls, p.collection)
			}
			if len(sample.Requests) > 1 {
				var expected relation.LookupValuePageRequest
				if err := json.Unmarshal(sample.Requests[1].Body, &expected); err != nil {
					t.Fatal(err)
				}
				if p.calls[1] != "page" || p.request != expected {
					t.Fatal("page request changed", p.calls, p.request, expected)
				}
			}
		})
	}
	if replayed != 26 || len(excluded) != 0 {
		t.Fatal("incomplete original replay", replayed, excluded)
	}
}

func TestLookupValuePageProductHTTPReads101ProvenanceWithoutWrites(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "Lookup 来源", OperationID: "value-page-table", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	label := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "来源值", "value-page-label")
	defaults, err := v2.RecommendedDefaults(v2.LogicalRelation)
	if err != nil {
		t.Fatal(err)
	}
	related := applySchemaProductField(t, pb, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID,
		Draft: &v2.FieldDraft{DisplayName: "关联来源", LogicalType: v2.LogicalRelation, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
			Relation: &v2.RelationSpec{TargetTableID: table.TableID, Cardinality: "many", DeletePolicy: "setNull", DisplayField: label.FieldID}},
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "反向", ReciprocalCardinality: "many", SourceDisplayFieldID: label.FieldID},
	}, "value-page-relation")
	defaults, err = v2.RecommendedDefaults(v2.LogicalLookup)
	if err != nil {
		t.Fatal(err)
	}
	lookup := applySchemaProductField(t, pb, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID,
		Draft: &v2.FieldDraft{DisplayName: "来源名称", LogicalType: v2.LogicalLookup, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
			Lookup: &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: related.FieldID}}, TargetFieldID: label.FieldID}},
	}, "value-page-lookup")
	definition, err := schemaexecution.Describe(context.Background(), pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 101)
	values := map[string]string{}
	for index := range ids {
		id := fmt.Sprintf("lvp%012d", index)
		ids[index] = id
		values[id] = fmt.Sprintf("中文 Cafe\u0301 👩🏽‍💻 %03d", index)
		row := core.NewRecord(collection)
		row.Id = id
		row.Set(label.Definition.Identity.PhysicalName, values[id])
		if label.Definition.Value.Presence.Mode == v2.PresenceCompanion {
			row.Set(label.Definition.Value.Presence.PhysicalName, true)
		}
		if err := pb.Save(row); err != nil {
			t.Fatal(err)
		}
	}
	sourceID := "lvp000000001000"
	row := core.NewRecord(collection)
	row.Id = sourceID
	row.Set(related.Definition.Identity.PhysicalName, ids)
	if related.Definition.Value.Presence.Mode == v2.PresenceCompanion {
		row.Set(related.Definition.Value.Presence.PhysicalName, true)
	}
	if err := pb.Save(row); err != nil {
		t.Fatal(err)
	}
	schema, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	service := relation.New(pb, query.NewPort(pb, schema), mutation.New(pb, mutation.MetadataSchemaSource{}))
	catalog, err := service.Describe(context.Background(), table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	params := valuePageParams(t, catalog)
	params["collection"] = table.TableID
	params["fieldRef"] = lookup.Definition.Identity.PhysicalName
	params["sourceRecordId"] = sourceID
	params["limit"] = 100
	mux := lookupValuePageHTTPMux(t, pb, lookupValuePageRegistration(service))
	before := valuePageAuthorityState(t, pb, definition.PhysicalName)
	send := func() productrpc.ResponseEnvelope {
		t.Helper()
		reply := schemaProductRequestForMethod(t, mux, context.Background(), "lookup.valuePage", string(valuePageJSON(t, params)), schemaListWire)
		if after := valuePageAuthorityState(t, pb, definition.PhysicalName); !reflect.DeepEqual(before, after) {
			t.Fatal("lookup page wrote rows, metadata, audit, outbox or receipts")
		}
		return reply
	}
	seen := map[string]bool{}
	for _, offset := range []int{0, 100} {
		params["offset"] = offset
		reply := send()
		if reply.Error != nil {
			t.Fatal(reply.Error)
		}
		var result lookupcalc.CellValue
		if err := json.Unmarshal(reply.Result, &result); err != nil {
			t.Fatal(err)
		}
		count := 100
		if offset == 100 {
			count = 1
		}
		if result.State != "ok" || len(result.Provenance) != count || result.ProvenanceOffset != offset || result.ProvenanceLimit != 100 || result.ProvenanceTotal != 101 || !result.ProvenanceTotalKnown || result.ProvenanceHasMore != (offset == 0) {
			t.Fatalf("unexpected page: %s", reply.Result)
		}
		for _, item := range result.Provenance {
			if seen[item.ItemID] || item.Collection != table.TableID || item.FieldID != label.FieldID || item.Value != values[item.ItemID] {
				t.Fatal("provenance changed", item)
			}
			seen[item.ItemID] = true
		}
	}
	if len(seen) != 101 {
		t.Fatal("provenance omitted or repeated", seen)
	}
	params["offset"] = 0
	for _, key := range []string{"schemaRevision", "permissionRevision", "lookupRevision"} {
		saved := params[key]
		params[key] = "stale"
		reply := send()
		params[key] = saved
		if reply.Error == nil || reply.Error.Code != -32603 {
			t.Fatal("stale revision accepted", key, reply)
		}
	}
	for _, limit := range []int{0, 501} {
		params["limit"] = limit
		reply := send()
		if reply.Error == nil || reply.Error.Code != -32603 {
			t.Fatal("invalid paging accepted", reply)
		}
	}
	params["limit"] = 100
	params["sourceRecordId"] = "lvp000000009999"
	reply := send()
	if reply.Error == nil || reply.Error.Data == nil {
		t.Fatal("missing source accepted")
	}
	if data := viewWireJSON(t, valuePageJSON(t, reply.Error.Data)); data["code"] != "lookup.storage_failed" {
		t.Fatal(data)
	}
}

func valuePageAuthorityState(t *testing.T, pb *pocketbase.PocketBase, physical string) map[string]string {
	t.Helper()
	state := map[string]string{}
	for _, name := range []string{physical, "vibetable_tables", "vibetable_fields", "vibetable_relations", "vibetable_audit_events", "vibetable_audit_outbox", "vibetable_outbox", "vibetable_idempotency_keys"} {
		records, err := pb.FindRecordsByFilter(name, "", "+id", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		state[name] = string(valuePageJSON(t, records))
	}
	return state
}
