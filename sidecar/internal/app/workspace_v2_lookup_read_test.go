package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	lookupcalc "github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestWorkspaceV2LookupValuePageReadsThroughBoundaryWithoutWrites(t *testing.T) {
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

	service := relation.New(pb, nil, nil)
	r := router.NewRouter(func(writer http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: pb, Event: router.Event{Response: writer, Request: request}}, nil
	})
	bindWorkspaceV2WriteBoundary(&core.ServeEvent{Router: r})
	registerRelationRoutes(r, service)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	input := relation.LookupValuePageRequest{
		TableID: table.TableID, SchemaRevision: definition.Snapshot.SchemaRevision,
		SourceRecordID: sourceID, FieldID: lookup.FieldID, Offset: 0, Limit: 100,
	}
	before := previewAuthorityState(t, pb, definition.PhysicalName)
	send := func(request relation.LookupValuePageRequest) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		httpRequest := httptest.NewRequest(http.MethodPost, "/api/vibetable/v1/lookups/value-page", bytes.NewReader(body))
		httpRequest.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(response, httpRequest)
		if after := previewAuthorityState(t, pb, definition.PhysicalName); !reflect.DeepEqual(before, after) {
			t.Fatal("lookup page changed rows, metadata, audit, outbox or idempotency records")
		}
		return response
	}
	seen := map[string]bool{}
	for _, offset := range []int{0, 100} {
		input.Offset = offset
		response := send(input)
		if response.Code != http.StatusOK {
			t.Fatalf("read-only lookup page POST = %d %s", response.Code, response.Body.String())
		}
		var page lookupcalc.CellValue
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		count := 100
		if offset == 100 {
			count = 1
		}
		if page.State != "ok" || len(page.Provenance) != count || page.ProvenanceTotal != 101 || !page.ProvenanceTotalKnown || page.ProvenanceOffset != offset || page.ProvenanceLimit != 100 || page.ProvenanceHasMore != (offset == 0) {
			t.Fatalf("unexpected page: %s", response.Body.String())
		}
		for _, item := range page.Provenance {
			if seen[item.ItemID] || item.Collection != table.TableID || item.FieldID != label.FieldID || item.Value != values[item.ItemID] {
				t.Fatal("changed or repeated provenance", item)
			}
			seen[item.ItemID] = true
		}
	}
	if len(seen) != 101 {
		t.Fatal("missing provenance", seen)
	}
	input.Offset = 0
	for _, invalid := range []struct {
		name   string
		change func(*relation.LookupValuePageRequest)
		code   string
	}{
		{"stale schema", func(p *relation.LookupValuePageRequest) { p.SchemaRevision = "stale" }, "lookup.schema_revision_conflict"},
		{"negative offset", func(p *relation.LookupValuePageRequest) { p.Offset = -1 }, "lookup.request.invalid"},
		{"zero limit", func(p *relation.LookupValuePageRequest) { p.Limit = 0 }, "lookup.request.invalid"},
		{"limit above 500", func(p *relation.LookupValuePageRequest) { p.Limit = 501 }, "lookup.request.invalid"},
		{"missing source", func(p *relation.LookupValuePageRequest) { p.SourceRecordID = "lvp000000009999" }, "lookup.storage_failed"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			changed := input
			invalid.change(&changed)
			response := send(changed)
			var failure struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
				t.Fatal(err)
			}
			if response.Code < 400 || response.Code == http.StatusLocked || failure.Code != invalid.code {
				t.Fatalf("domain rejection = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestWorkspaceV2LookupValuePageBoundaryDoesNotAdmitWritesOrSimilarPaths(t *testing.T) {
	const path = "/api/vibetable/v1/lookups/value-page"
	for _, test := range []struct{ method, path string }{
		{http.MethodPut, path}, {http.MethodPatch, path}, {http.MethodDelete, path},
		{http.MethodPost, path + "/extra"}, {http.MethodPost, path + "-extra"},
		{http.MethodPost, "/api/vibetable/v1/lookups/apply"},
		{http.MethodPost, "/api/vibetable/v1/plugin/write"},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			r := router.NewRouter(func(writer http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
				return &core.RequestEvent{Event: router.Event{Response: writer, Request: request}}, nil
			})
			bindWorkspaceV2WriteBoundary(&core.ServeEvent{Router: r})
			called := false
			r.Route(test.method, test.path, func(e *core.RequestEvent) error {
				called = true
				return e.NoContent(http.StatusOK)
			})
			mux, err := r.BuildMux()
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(test.method, test.path, strings.NewReader("{}")))
			if called || response.Code != http.StatusLocked || !strings.Contains(response.Body.String(), "workspace.v1_write_disabled") {
				t.Fatalf("write escaped boundary: called=%v status=%d %s", called, response.Code, response.Body.String())
			}
		})
	}
}
