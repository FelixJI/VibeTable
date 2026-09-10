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

type unrelatedRelationPreviewMustNotRun struct{ t *testing.T }

func (p unrelatedRelationPreviewMustNotRun) PreviewDelta(context.Context, relation.DeltaRequest) (relation.DeltaPreview, error) {
	p.t.Fatal("unrelated relation.previewDelta was called")
	return relation.DeltaPreview{}, nil
}

func relationPreviewHTTPMux(t *testing.T, pb *pocketbase.PocketBase, registration productrpc.Registration) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	},
		unrelatedPresetRegistration(t, "preset.list"),
		unrelatedPresetRegistration(t, "preset.save"),
		unrelatedPresetRegistration(t, "preset.delete"),
		mutationPreviewRegistration(unrelatedMutationProductMustNotRun{t: t}),
		mutationApplyRegistration(unrelatedMutationProductMustNotRun{t: t}), registration, productrpc.ReconcileRegistration(catalog), queryValidateSnapshotRegistration(unrelatedQueryValidateSnapshotMustNotRun{t: t}),
		lookupListRegistration(relation.New(pb, nil, nil)),
		lookupQueryRegistration(unrelatedLookupQueryMustNotRun{t: t}),
		queryReadRowsRegistration(unrelatedQueryReadRowsMustNotRun{t: t}),
		queryCursorOpenRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		queryCursorFetchRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		querySelectionOpenRegistration(unrelatedSelectionMustNotRun{t: t}),
		queryPageRegistration(unrelatedPageForViewMustNotRun{t: t}), queryViewRegistration(unrelatedViewMustNotRun{t: t}),
		lookupValuePageRegistration(unrelatedLookupValuePageMustNotRun{t: t}),
		relationSearchTargetsRegistration(unrelatedRelationSearchMustNotRun{t: t}),
		unrelatedRelationInspectRegistration(t),
		fieldSettingsDescribeRegistration(unrelatedFieldSettingsDescribeMustNotRun{t: t}),
		schemaDescribeRegistration(pb, relation.New(pb, nil, nil)), schemaGetTableRegistration(pb), schemaListRegistration(catalog),
		productrpc.AttachmentListRegistration(pb, mustAttachmentManager(t)),
		unrelatedSurfaceRegistration(t, "interface.list"),
		unrelatedSurfaceRegistration(t, "interface.load"),
		unrelatedSurfaceRegistration(t, "interface.commit"),
		unrelatedSurfaceRegistration(t, "interface.delete"),
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

func TestRelationPreviewProductHTTPReplaysFrozenPython(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "relation-preview-python-oracle.json"))
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
				Response json.RawMessage `json:"response"`
				Failure  *string         `json:"failure"`
			} `json:"authorityFixture"`
			Requests []struct {
				Body json.RawMessage `json:"body"`
			} `json:"authorityRequests"`
			Response json.RawMessage `json:"response"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Producer != "6ed36810f3753caed5e2e8ca27a4d4ad2117d41d" || len(corpus.Cases) != 37 {
		t.Fatal("wrong frozen producer/cases")
	}
	excluded := map[string]string{
		"non-object-result":                        "DeltaPreview cannot be an array",
		"non-object-current-members-filtered":      "[]TargetRef cannot contain non-object members",
		"non-array-current":                        "Current cannot be an object",
		"current-nontext-label":                    "TargetRef.Label cannot be bool",
		"secondary-label-falsy-becomes-null":       "TargetRef.SecondaryLabel cannot contain arbitrary JSON",
		"secondary-label-truthy-nontext-preserved": "TargetRef.SecondaryLabel cannot contain truthy non-text JSON",
		"can-apply-missing-is-false":               "CanApply is always emitted",
		"can-apply-one-is-false":                   "CanApply cannot be integer",
		"can-apply-string-is-false":                "CanApply cannot be string",
		"can-apply-null-is-false":                  "CanApply cannot be null",
		"transport-error":                          "in-process PreviewDelta has no Python HTTP transport boundary",
	}
	if len(corpus.Boundaries) != len(excluded) {
		t.Fatal("unreviewed typed boundary")
	}
	for name := range excluded {
		if corpus.Boundaries[name] == "" {
			t.Fatal(name)
		}
	}
	p := &relationPreviewProbe{}
	mux := relationPreviewHTTPMux(t, schemaProductStore(t), relationPreviewDeltaRegistration(p))
	replayed := 0
	for _, sample := range corpus.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			if reason, skip := excluded[sample.Name]; skip {
				delete(excluded, sample.Name)
				t.Skip(reason)
			}
			replayed++
			*p = relationPreviewProbe{}
			if sample.Fixture.Failure != nil {
				path := "removes[0]"
				p.err = &mutation.ProductError{Code: "relation.target_not_linked", Path: &path, Message: "remove target is not linked", Details: map[string]any{"recordId": "author-1"}}
			} else if len(sample.Requests) != 0 {
				if err := json.Unmarshal(sample.Fixture.Response, &p.result); err != nil {
					t.Fatalf("unclassified DTO input: %v", err)
				}
			}
			body := previewJSON(t, map[string]any{"jsonrpc": "2.0", "id": sample.Name, "method": "relation.previewDelta", "params": sample.Request.Params, "wire": json.RawMessage(schemaListWire)})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, productRPCPath, bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK && recorder.Code != http.StatusBadRequest {
				t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body)
			}
			got := viewWireJSON(t, recorder.Body.Bytes())
			if !reflect.DeepEqual(got["wire"], viewWireJSON(t, []byte(schemaListWire))) {
				t.Fatal("wire changed")
			}
			delete(got, "wire")
			if want := viewWireJSON(t, sample.Response); !reflect.DeepEqual(got, want) {
				t.Fatalf("frozen mismatch\ngot=%v\nwant=%v", got, want)
			}
			if p.calls != len(sample.Requests) {
				t.Fatalf("calls %d want %d", p.calls, len(sample.Requests))
			}
			if p.calls == 1 {
				var input relation.DeltaRequest
				if err := json.Unmarshal(sample.Requests[0].Body, &input); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(p.request, input) {
					t.Fatalf("translated request %+v want %+v", p.request, input)
				}
			}
		})
	}
	if len(excluded) != 0 || replayed != 26 {
		t.Fatalf("replayed=%d unknown=%v", replayed, excluded)
	}
}

func TestRelationPreviewProductHTTPPreservesAuthorityOnSuccessAndFailure(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "预览关系", OperationID: "preview-table", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	label := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "标题", "preview-label")
	defaults, err := v2.RecommendedDefaults(v2.LogicalRelation)
	if err != nil {
		t.Fatal(err)
	}
	related := applySchemaProductField(t, pb, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID,
		Draft: &v2.FieldDraft{DisplayName: "关联", LogicalType: v2.LogicalRelation, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
			Relation: &v2.RelationSpec{TargetTableID: table.TableID, Cardinality: "many", DeletePolicy: "setNull", DisplayField: label.FieldID}},
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "反向", ReciprocalCardinality: "many", SourceDisplayFieldID: label.FieldID},
	}, "preview-relation")
	description, err := schemaexecution.Describe(context.Background(), pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(description.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range [][2]string{{"previewtarget01", "中文 A"}, {"previewtarget02", "Cafe\u0301 B"}, {"previewsource01", "来源"}} {
		row := core.NewRecord(collection)
		row.Id = seed[0]
		row.Set(label.Definition.Identity.PhysicalName, seed[1])
		if label.Definition.Value.Presence.Mode == v2.PresenceCompanion {
			row.Set(label.Definition.Value.Presence.PhysicalName, true)
		}
		if seed[0] == "previewsource01" {
			row.Set(related.Definition.Identity.PhysicalName, []string{"previewtarget01"})
			if related.Definition.Value.Presence.Mode == v2.PresenceCompanion {
				row.Set(related.Definition.Value.Presence.PhysicalName, true)
			}
		}
		if err := pb.Save(row); err != nil {
			t.Fatal(err)
		}
	}
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	service := relation.New(pb, query.NewPort(pb, source), mutation.New(pb, mutation.MetadataSchemaSource{}))
	mux := relationPreviewHTTPMux(t, pb, relationPreviewDeltaRegistration(service))
	before := previewAuthorityState(t, pb, description.PhysicalName)
	params := map[string]any{
		"relationId": table.TableID + "." + related.FieldID, "sourceItemId": "previewsource01",
		"expectedSchemaRevision": description.Snapshot.SchemaRevision, "idempotencyKey": "preview-only",
		"adds":    []any{map[string]any{"collection": table.TableID, "itemId": "previewtarget02", "label": "B"}},
		"removes": []any{map[string]any{"collection": table.TableID, "itemId": "previewtarget01", "label": "A"}},
	}
	send := func() productrpc.ResponseEnvelope {
		t.Helper()
		return schemaProductRequestForMethod(t, mux, context.Background(), "relation.previewDelta", string(previewJSON(t, params)), schemaListWire)
	}
	response := send()
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	result := viewWireJSON(t, response.Result)
	expected := []any{map[string]any{"collection": table.TableID, "itemId": "previewtarget01", "label": "previewtarget01", "secondaryLabel": nil}}
	if len(result) != 4 || result["canApply"] != true || !reflect.DeepEqual(result["current"], expected) {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(result["delta"], viewWireJSON(t, previewJSON(t, params))) {
		t.Fatal("delta echo changed")
	}
	if after := previewAuthorityState(t, pb, description.PhysicalName); !reflect.DeepEqual(before, after) {
		t.Fatal("successful preview wrote authority, revision, receipt or audit state")
	}
	// Duplicate add is rejected by the actual Service before kernel application.
	params["adds"] = []any{map[string]any{"collection": table.TableID, "itemId": "previewtarget01"}}
	params["removes"] = []any{}
	response = send()
	if response.Error == nil || response.Error.Data == nil {
		t.Fatal("duplicate accepted")
	}
	if data := viewWireJSON(t, previewJSON(t, response.Error.Data)); data["code"] != "relation.target_duplicate" {
		t.Fatal(data)
	}
	if after := previewAuthorityState(t, pb, description.PhysicalName); !reflect.DeepEqual(before, after) {
		t.Fatal("failed preview wrote authority, revision, receipt or audit state")
	}
	// An absent target passes delta assembly and fails the real Kernel.Preview check.
	params["adds"] = []any{map[string]any{"collection": table.TableID, "itemId": "previewmissing1"}}
	response = send()
	if response.Error == nil || response.Error.Data == nil {
		t.Fatal("missing target accepted")
	}
	if data := viewWireJSON(t, previewJSON(t, response.Error.Data)); data["code"] != "mutation.relation.target_not_found" {
		t.Fatal(data)
	}
	if after := previewAuthorityState(t, pb, description.PhysicalName); !reflect.DeepEqual(before, after) {
		t.Fatal("kernel preview failure wrote authority")
	}
}

func previewAuthorityState(t *testing.T, pb *pocketbase.PocketBase, physical string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, name := range []string{physical, "vibetable_tables", "vibetable_fields", "vibetable_relations", "vibetable_audit_events", "vibetable_audit_outbox", "vibetable_outbox", "vibetable_idempotency_keys"} {
		records, err := pb.FindRecordsByFilter(name, "", "+id", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		result[name] = string(previewJSON(t, records))
	}
	return result
}
