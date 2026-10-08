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

type unrelatedRelationSearchMustNotRun struct{ t *testing.T }

func (probe unrelatedRelationSearchMustNotRun) SearchTargets(context.Context, relation.SearchRequest) (relation.SearchResult, error) {
	probe.t.Helper()
	probe.t.Fatal("unrelated fixture must not invoke relation.searchTargets")
	return relation.SearchResult{}, nil
}

type relationSearchUnrelatedPage struct{ t *testing.T }

func (probe relationSearchUnrelatedPage) QueryPage(context.Context, string, query.TableQuery) (query.Page, error) {
	probe.t.Helper()
	probe.t.Fatal("relation search fixture must not invoke Product query.page")
	return query.Page{}, nil
}

func relationSearchHTTPMux(t *testing.T, pb *pocketbase.PocketBase, registration productrpc.Registration) http.Handler {
	t.Helper()
	catalog := schemaapi.New(pb)
	dispatcher, err := productrpc.New(productrpc.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		append([]productrpc.Registration{
			unrelatedContentRegistration(t, "contentProfile.commit"),
			unrelatedContentRegistration(t, "contentProfile.delete"),
			unrelatedContentRegistration(t, "contentProfile.load"),
			unrelatedContentRegistration(t, "recordDocumentLink.commit"),
			unrelatedContentRegistration(t, "recordDocumentLink.delete"),
			unrelatedContentRegistration(t, "recordDocumentLink.list"),
			unrelatedContentRegistration(t, "recordDocumentLink.repair"),
			unrelatedPluginCatalogRegistration(t, "plugin.listCatalog"),
			unrelatedPluginCatalogRegistration(t, "plugin.listAudit"),
			unrelatedPluginCatalogRegistration(t, "plugin.listPendingCleanup"),
			unrelatedPluginCatalogRegistration(t, "plugin.setEnabled"),
			unrelatedPresetRegistration(t, "preset.list"),
			unrelatedPresetRegistration(t, "preset.save"),
			unrelatedPresetRegistration(t, "preset.delete"),
			unrelatedContentVersionRegistration(t, "version.list"),
			unrelatedContentVersionRegistration(t, "version.create"),
			unrelatedContentVersionRegistration(t, "version.save"),
			unrelatedContentVersionRegistration(t, "version.compare"),
			unrelatedContentVersionRegistration(t, "version.promote"),
			unrelatedContentVersionRegistration(t, "version.delete"),
			mutationPreviewRegistration(unrelatedMutationProductMustNotRun{t: t}),
			mutationApplyRegistration(unrelatedMutationProductMustNotRun{t: t}),
			registration, unrelatedRelationInspectRegistration(t), productrpc.ReconcileRegistration(catalog), queryValidateSnapshotRegistration(unrelatedQueryValidateSnapshotMustNotRun{t: t}),
			lookupDraftPreviewRegistration(pb), lookupListRegistration(relation.New(pb, nil, nil)),
			queryPageRegistration(relationSearchUnrelatedPage{t: t}), schemaDescribeRegistration(pb, relation.New(pb, nil, nil)), schemaGetTableRegistration(pb), schemaListRegistration(catalog),
			queryViewRegistration(unrelatedViewMustNotRun{t: t}),
			lookupValuePageRegistration(unrelatedLookupValuePageMustNotRun{t: t}),
			relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}),
			fieldSettingsDescribeRegistration(unrelatedFieldSettingsDescribeMustNotRun{t: t}),
			lookupQueryRegistration(unrelatedLookupQueryMustNotRun{t: t}),
			queryReadRowsRegistration(unrelatedQueryReadRowsMustNotRun{t: t}),
			queryCursorOpenRegistration(unrelatedQueryCursorMustNotRun{t: t}),
			queryCursorFetchRegistration(unrelatedQueryCursorMustNotRun{t: t}),
			querySelectionOpenRegistration(unrelatedSelectionMustNotRun{t: t}),
			productrpc.AttachmentListRegistration(pb, mustAttachmentManager(t)), productrpc.AttachmentTokenRegistration(pb, mustAttachmentManager(t)), unrelatedDashboardRegistration(t, "insights.dashboardQueryLimits"),
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
			workCalendarReadRegistration(nil), workCalendarCommitRegistration(nil),
		}, append(unrelatedSchemaFieldChangeRegistrations(t),
			append(unrelatedFormulaProductRegistrations(t), unrelatedRelationWriteRegistrations(t)...)...)...)...)
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

func relationSearchWireJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestRelationSearchProductHTTPReplaysFrozenPython(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "relation-search-python-oracle.json"))
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
			AuthorityRequests []json.RawMessage `json:"authorityRequests"`
			Response          json.RawMessage   `json:"response"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil || len(corpus.Cases) != 30 {
		t.Fatalf("frozen corpus: %v cases=%d", err, len(corpus.Cases))
	}
	excluded := map[string]string{
		"non-object-items-filtered":     "typed []TargetRef cannot represent mixed non-object items",
		"all-non-object-items-filtered": "typed []TargetRef cannot represent non-object items to filter",
		"non-object-result":             "typed SearchResult cannot be an array",
		"item-non-text-label":           "typed TargetRef.Label cannot be bool",
		"missing-total":                 "typed SearchResult always emits its integer total; absence cannot be represented",
		"boolean-total":                 "typed SearchResult.Total cannot be bool",
		"string-total":                  "typed SearchResult.Total cannot be string",
		"transport-error":               "in-process Service has no Python HTTP transport failure",
	}
	probe := &relationSearchProbe{}
	mux := relationSearchHTTPMux(t, schemaProductStore(t), relationSearchTargetsRegistration(probe))
	for _, sample := range corpus.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			if reason, skip := excluded[sample.Name]; skip {
				delete(excluded, sample.Name)
				t.Skip(reason)
			}
			*probe = relationSearchProbe{}
			if sample.Fixture.Failure != nil {
				path := "relationId"
				probe.err = &mutation.ProductError{Code: "relation.not_found", Path: &path, Message: "Relation was not found", Details: map[string]any{"relationId": "rel-orders"}}
			} else if len(sample.AuthorityRequests) != 0 {
				if err := json.Unmarshal(sample.Fixture.Response, &probe.result); err != nil {
					t.Fatalf("unclassified typed input: %v", err)
				}
			}
			body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": sample.Name, "method": "relation.searchTargets", "params": sample.Request.Params, "wire": json.RawMessage(schemaListWire)})
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
			got := relationSearchWireJSON(t, recorder.Body.Bytes())
			if !reflect.DeepEqual(got["wire"], relationSearchWireJSON(t, []byte(schemaListWire))) {
				t.Fatal("wire changed")
			}
			delete(got, "wire")
			// The frozen Python wire predates the relation display projection keys.
			// Project away exactly those three explicitly authorized additive keys
			// so every historical field — including the filtered authority `extra`
			// and `snapshot` canaries — stays compared byte-for-byte; the new keys'
			// values, types, 0/false handling and controlled producer are asserted
			// by TestRelationSearchProductDefaultsAndProjection and the real-authority
			// display projection tests below.
			stripRelationDisplayProjectionKeys(got)
			if want := relationSearchWireJSON(t, sample.Response); !reflect.DeepEqual(got, want) {
				t.Fatalf("frozen Python mismatch\ngot=%v\nwant=%v", got, want)
			}
			if probe.calls != len(sample.AuthorityRequests) {
				t.Fatalf("calls %d want %d", probe.calls, len(sample.AuthorityRequests))
			}
			if probe.calls == 1 {
				var authority struct {
					Body relation.SearchRequest `json:"body"`
				}
				if err := json.Unmarshal(sample.AuthorityRequests[0], &authority); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(probe.request, authority.Body) {
					t.Fatalf("authority input %v want %v", probe.request, authority.Body)
				}
			}
		})
	}
	if len(excluded) != 0 {
		t.Fatalf("unknown exclusions: %v", excluded)
	}
}

func TestRelationSearchProductHTTPUsesRealRelationAuthority(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "搜索候选", OperationID: "search-table", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	label := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "标题", "search-label")
	defaults, err := v2.RecommendedDefaults(v2.LogicalRelation)
	if err != nil {
		t.Fatal(err)
	}
	related := applySchemaProductField(t, pb, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID,
		Draft: &v2.FieldDraft{DisplayName: "候选关系", LogicalType: v2.LogicalRelation, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
			Relation: &v2.RelationSpec{TargetTableID: table.TableID, Cardinality: "one", DeletePolicy: "setNull", DisplayField: label.FieldID}},
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "来源", ReciprocalCardinality: "many", SourceDisplayFieldID: label.FieldID}}, "search-relation")
	description, err := schemaexecution.Describe(context.Background(), pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(description.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	for index, text := range []string{"中文 Alpha", "中文 Beta", "Cafe\u0301"} {
		record := core.NewRecord(collection)
		record.Id = fmt.Sprintf("searchtarget%03d", index+1)
		record.Set(label.Definition.Identity.PhysicalName, text)
		if label.Definition.Value.Presence.Mode == v2.PresenceCompanion {
			record.Set(label.Definition.Value.Presence.PhysicalName, true)
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
	mux := relationSearchHTTPMux(t, pb, relationSearchTargetsRegistration(service))
	relationID := table.TableID + "." + related.FieldID
	read := func(params map[string]any) productrpc.ResponseEnvelope {
		t.Helper()
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		return schemaProductRequestForMethod(t, mux, context.Background(), "relation.searchTargets", string(raw), schemaListWire)
	}
	all := read(map[string]any{"relationId": relationID})
	if all.Error != nil {
		t.Fatal(all.Error)
	}
	result := relationSearchWireJSON(t, all.Result)
	items := result["items"].([]any)
	if len(result) != 2 || result["total"] != json.Number("3") || len(items) != 3 {
		t.Fatal(result)
	}
	for index, item := range items {
		target := item.(map[string]any)
		if len(target) != 4 || target["collection"] != table.TableID || target["itemId"] != fmt.Sprintf("searchtarget%03d", index+1) {
			t.Fatal(target)
		}
		// The relation display field IS the target's global primary display
		// field here; the label still carries its raw typed scalar so every
		// surface formats it through the shared display contract.
		if target["displayValue"] == nil || target["secondaryLabel"] != nil {
			t.Fatal(target)
		}
	}
	filtered := read(map[string]any{"relationId": relationID, "query": "中文", "offset": 1, "limit": 1})
	if filtered.Error != nil {
		t.Fatal(filtered.Error)
	}
	got := relationSearchWireJSON(t, filtered.Result)
	if got["total"] != json.Number("2") || !reflect.DeepEqual(got["items"], []any{map[string]any{"collection": table.TableID, "itemId": "searchtarget002", "label": "中文 Beta", "displayValue": "中文 Beta"}}) {
		t.Fatal(got)
	}
	empty := read(map[string]any{"relationId": relationID, "query": "does not exist"})
	if empty.Error != nil {
		t.Fatal(empty.Error)
	}
	if got := relationSearchWireJSON(t, empty.Result); got["total"] != json.Number("0") || len(got["items"].([]any)) != 0 {
		t.Fatal(got)
	}
	if max := read(map[string]any{"relationId": relationID, "limit": 100}); max.Error != nil {
		t.Fatal(max.Error)
	}
	for _, params := range []map[string]any{{"relationId": relationID, "limit": 101}, {"relationId": relationID, "offset": -1}} {
		rejected := read(params)
		if rejected.Error == nil || rejected.Error.Data == nil {
			t.Fatalf("accepted invalid domain paging: %+v", rejected)
		}
		data, err := json.Marshal(rejected.Error.Data)
		if err != nil {
			t.Fatal(err)
		}
		if relationSearchWireJSON(t, data)["code"] != "relation.request.invalid" {
			t.Fatal(string(data))
		}
	}
}

// stripRelationDisplayProjectionKeys removes only the three explicitly
// authorized display projection keys (secondaryLabel, displayValue,
// secondaryValue) from every mapped result item. It must not touch any other
// key: the frozen corpus keeps proving that authority-only fields such as
// `extra` and `snapshot` never reach the public response.
func stripRelationDisplayProjectionKeys(response map[string]any) {
	result, ok := response["result"].(map[string]any)
	if !ok {
		return
	}
	items, ok := result["items"].([]any)
	if !ok {
		return
	}
	for _, item := range items {
		if record, ok := item.(map[string]any); ok {
			delete(record, "secondaryLabel")
			delete(record, "displayValue")
			delete(record, "secondaryValue")
		}
	}
}

// TestRelationSearchUsesRelationDisplayFieldProjection proves with the real
// Go service that search labels follow the relation's own displayFieldId
// (contract name), the target table's global primary display field (contract
// number) only backs the auxiliary label and the empty-label fallback, 0 and
// false are valid typed display values, and IDs remain the fallback — never
// a passthrough of arbitrary authority data.
func TestRelationSearchUsesRelationDisplayFieldProjection(t *testing.T) {
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "合同", OperationID: "contracts-table", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	number := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "合同编号", "contract-number")
	name := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "合同名称", "contract-name")
	units := createSchemaProductField(t, pb, table.TableID, v2.LogicalNumber, "数量", "contract-units")
	defaults, err := v2.RecommendedDefaults(v2.LogicalRelation)
	if err != nil {
		t.Fatal(err)
	}
	byName := applySchemaProductField(t, pb, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID,
		Draft: &v2.FieldDraft{DisplayName: "按名称", LogicalType: v2.LogicalRelation, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
			Relation: &v2.RelationSpec{TargetTableID: table.TableID, Cardinality: "one", DeletePolicy: "setNull", DisplayField: name.FieldID}},
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "来源", ReciprocalCardinality: "many", SourceDisplayFieldID: number.FieldID}}, "by-name-relation")
	byUnits := applySchemaProductField(t, pb, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID,
		Draft: &v2.FieldDraft{DisplayName: "按数量", LogicalType: v2.LogicalRelation, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
			Relation: &v2.RelationSpec{TargetTableID: table.TableID, Cardinality: "one", DeletePolicy: "setNull", DisplayField: units.FieldID}},
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "来源", ReciprocalCardinality: "many", SourceDisplayFieldID: number.FieldID}}, "by-units-relation")
	// 同字段：显示字段即目标表全局主显示字段，仍必须携带 raw typed scalar。
	byCode := applySchemaProductField(t, pb, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID,
		Draft: &v2.FieldDraft{DisplayName: "按编号", LogicalType: v2.LogicalRelation, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
			Relation: &v2.RelationSpec{TargetTableID: table.TableID, Cardinality: "one", DeletePolicy: "setNull", DisplayField: number.FieldID}},
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "来源", ReciprocalCardinality: "many", SourceDisplayFieldID: number.FieldID}}, "by-code-relation")
	description, err := schemaexecution.Describe(context.Background(), pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(description.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(id string, values map[string]any) {
		record := core.NewRecord(collection)
		record.Id = id
		for physical, value := range values {
			record.Set(physical, value)
		}
		for _, field := range description.Snapshot.Fields {
			if field.Value.Presence.Mode == v2.PresenceCompanion {
				_, seeded := values[field.Identity.PhysicalName]
				if seeded {
					record.Set(field.Value.Presence.PhysicalName, true)
				}
			}
		}
		if err := pb.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	seed("srchnmfull00001", map[string]any{number.Definition.Identity.PhysicalName: "CT-001", name.Definition.Identity.PhysicalName: "城轨一期"})
	seed("srchnmmiss00002", map[string]any{number.Definition.Identity.PhysicalName: "CT-002"})
	seed("srchbothmiss003", map[string]any{})
	seed("srchzerounit004", map[string]any{number.Definition.Identity.PhysicalName: "CT-004", units.Definition.Identity.PhysicalName: 0})
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	service := relation.New(pb, query.NewPort(pb, source), nil)
	mux := relationSearchHTTPMux(t, pb, relationSearchTargetsRegistration(service))
	read := func(relationID string) map[string]any {
		t.Helper()
		response := schemaProductRequestForMethod(t, mux, context.Background(), "relation.searchTargets",
			string(previewJSON(t, map[string]any{"relationId": relationID, "limit": 50})), schemaListWire)
		if response.Error != nil {
			t.Fatal(response.Error)
		}
		return relationSearchWireJSON(t, response.Result)
	}
	byNameResult := read(table.TableID + "." + byName.FieldID)
	items := byNameResult["items"].([]any)
	if len(items) != 4 {
		t.Fatal(byNameResult)
	}
	byID := map[string]map[string]any{}
	for _, item := range items {
		target := item.(map[string]any)
		byID[target["itemId"].(string)] = target
	}
	if got := byID["srchnmfull00001"]; got["label"] != "城轨一期" || got["secondaryLabel"] != "CT-001" ||
		got["displayValue"] != "城轨一期" || got["secondaryValue"] != "CT-001" {
		t.Fatalf("configured display field projection: %#v", got)
	}
	if got := byID["srchnmmiss00002"]; got["label"] != "CT-002" || got["secondaryLabel"] != nil ||
		got["displayValue"] != nil || got["secondaryValue"] != "CT-002" {
		t.Fatalf("empty display label falls back to the valid global primary display value: %#v", got)
	}
	if got := byID["srchbothmiss003"]; got["label"] != "srchbothmiss003" || got["secondaryLabel"] != nil ||
		got["displayValue"] != nil || got["secondaryValue"] != nil {
		t.Fatalf("empty labels fall back to the record ID: %#v", got)
	}
	byUnitsResult := read(table.TableID + "." + byUnits.FieldID)
	unitItems := byUnitsResult["items"].([]any)
	zero := map[string]any{}
	for _, item := range unitItems {
		if target, ok := item.(map[string]any); ok && target["itemId"] == "srchzerounit004" {
			zero = target
		}
	}
	if zero["label"] != "0" || zero["displayValue"] != json.Number("0") {
		t.Fatalf("numeric 0 is a valid typed display value: %#v", zero)
	}
	if zero["secondaryLabel"] != "CT-004" {
		t.Fatalf("numeric display keeps the global primary display as auxiliary: %#v", zero)
	}
	byCodeResult := read(table.TableID + "." + byCode.FieldID)
	codeItems := byCodeResult["items"].([]any)
	codeByID := map[string]map[string]any{}
	for _, item := range codeItems {
		if target, ok := item.(map[string]any); ok {
			codeByID[target["itemId"].(string)] = target
		}
	}
	if got := codeByID["srchnmfull00001"]; got["label"] != "CT-001" || got["displayValue"] != "CT-001" ||
		got["secondaryLabel"] != nil || got["secondaryValue"] != nil {
		t.Fatalf("same-field display projection must still carry the raw typed scalar: %#v", got)
	}
	// 直接按已选 ID 批量刷新（AC2）：与关键词搜索分离，同一投影与预算。
	idsResponse := schemaProductRequestForMethod(t, mux, context.Background(), "relation.searchTargets",
		string(previewJSON(t, map[string]any{
			"relationId": table.TableID + "." + byName.FieldID,
			"targetItemIds": []string{"srchnmmiss00002", "srchnmfull00001"},
			"limit":         2,
		})), schemaListWire)
	if idsResponse.Error != nil {
		t.Fatal(idsResponse.Error)
	}
	idsResult := relationSearchWireJSON(t, idsResponse.Result)
	idItems, _ := idsResult["items"].([]any)
	if len(idItems) != 2 {
		t.Fatalf("id refresh items = %#v", idsResult)
	}
	refreshed := map[string]map[string]any{}
	for _, item := range idItems {
		if target, ok := item.(map[string]any); ok {
			refreshed[target["itemId"].(string)] = target
		}
	}
	if got := refreshed["srchnmfull00001"]; got["label"] != "城轨一期" || got["secondaryLabel"] != "CT-001" {
		t.Fatalf("id refresh display projection: %#v", got)
	}
	if got := refreshed["srchnmmiss00002"]; got["label"] != "CT-002" || got["displayValue"] != nil || got["secondaryValue"] != "CT-002" {
		t.Fatalf("id refresh fallback projection: %#v", got)
	}
	for _, item := range items {
		for key := range item.(map[string]any) {
			switch key {
			case "collection", "itemId", "label", "secondaryLabel", "displayValue", "secondaryValue":
			default:
				t.Fatalf("authority-only field %q leaked into the public response", key)
			}
		}
	}
}
