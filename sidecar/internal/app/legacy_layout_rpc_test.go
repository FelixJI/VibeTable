package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/router"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

// legacyLayoutFixture seeds a synthetic retired-layout workspace: one valid
// record-manager-replaceable Interface, one Interface with unsupported chart
// elements and a legacy plugin action, one dashboard with a panel, and one
// corrupt payload. It is created by this test only — never a real workspace.
type legacyLayoutFixture struct {
	pb       *pocketbase.PocketBase
	wire     string
	handler  func(ctx context.Context, raw json.RawMessage) (any, error)
	snapshot func(ctx context.Context, raw json.RawMessage) (any, error)
}

func newLegacyLayoutFixture(t *testing.T) *legacyLayoutFixture {
	t.Helper()
	pb := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir(), HideStartBanner: true})
	migrations.Register(pb)
	if err := pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pb.ResetBootstrapState(); err != nil {
			t.Error(err)
		}
	})
	if err := pb.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}

	replaceable := `{"contractVersion":"1.0","interfaceId":"interface-orders","name":"Orders","bindings":[],"actions":[],"pages":[{"pageId":"main","title":"Orders","elements":[{"elementId":"e1","kind":"record-list"}]}]}`
	complexInterface := `{"contractVersion":"1.0","interfaceId":"interface-ops","name":"Ops","bindings":[],"actions":[{"actionId":"a1","kind":"plugin","pluginId":"legacy.worker","pluginActionId":"run"}],"pages":[{"pageId":"main","title":"Ops","elements":[{"elementId":"e1","kind":"chart"}]}]}`
	dashboard := `{"id":"dash-1","name":"Operations","note":"","configVersion":1}`
	panel := `{"id":"panel-1","dashboardId":"dash-1","name":"Revenue","type":"bar","position":{"x":0,"y":0,"width":4,"height":3},"options":{},"query":null}`

	seedRecord(t, pb, "vibetable_interfaces", "interface-orders", json.RawMessage(replaceable))
	seedRecord(t, pb, "vibetable_interfaces", "interface-ops", json.RawMessage(complexInterface))
	seedRecord(t, pb, "vibetable_dashboards", "dash-1", json.RawMessage(dashboard))
	seedRecord(t, pb, "vibetable_panels", "panel-1", json.RawMessage(panel))
	seedRecord(t, pb, "vibetable_interfaces", "interface-corrupt", json.RawMessage(`"not-an-object"`))
	// Mirror the retired dashboard writer: a healthy panel stores
	// parent_id == payload.dashboardId next to logical_id == payload.id.
	if _, err := pb.DB().NewQuery("UPDATE vibetable_panels SET parent_id='dash-1' WHERE logical_id='panel-1'").Execute(); err != nil {
		t.Fatal(err)
	}

	fixture := &legacyLayoutFixture{
		pb:   pb,
		wire: schemaListWire,
	}
	rejection := retiredLayoutRegistration("interface.commit")
	fixture.handler = rejection.Handler
	fixture.snapshot = func(ctx context.Context, _ json.RawMessage) (any, error) { return collectLegacyPages(ctx, pb) }
	return fixture
}

func seedRecord(t *testing.T, pb *pocketbase.PocketBase, collectionName, logicalID string, payload json.RawMessage) {
	t.Helper()
	collection, err := pb.FindCollectionByNameOrId(collectionName)
	if err != nil {
		t.Fatalf("seed %s: %v", collectionName, err)
	}
	record := core.NewRecord(collection)
	record.Set("logical_id", logicalID)
	record.Set("payload_json", json.RawMessage(payload))
	if err := pb.Save(record); err != nil {
		t.Fatalf("seed %s/%s: %v", collectionName, logicalID, err)
	}
}

func TestRetiredLayoutRegistrationsRejectDirectRequests(t *testing.T) {
	_ = newLegacyLayoutFixture(t)
	for _, method := range retiredLayoutMethods {
		registration := retiredLayoutRegistration(method)
		if registration.Method != method {
			t.Fatalf("method mismatch %s", registration.Method)
		}
		if err := registration.ValidateParams(nil); err != nil {
			t.Fatalf("validator rejected nil params for %s: %v", method, err)
		}
		result, err := registration.Handler(context.Background(), nil)
		if err == nil {
			t.Fatalf("%s unexpectedly succeeded: %#v", method, result)
		}
		var public *productrpc.PublicError
		if !asPublicError(err, &public) || public.Code != "layout.retired" {
			t.Fatalf("%s error is not the stable public layout.retired rejection: %v", method, err)
		}
		if !strings.Contains(public.Message, "legacyLayout.snapshot") {
			t.Fatalf("%s rejection does not point at the preservation channel", method)
		}
	}
}

func asPublicError(err error, target **productrpc.PublicError) bool {
	public, ok := err.(*productrpc.PublicError)
	if ok {
		*target = public
	}
	return ok
}

// unrelatedLegacyLayoutSnapshotRegistration keeps dispatcher fixtures exact
// for the generated legacyLayout.snapshot route without reading anything.
func unrelatedLegacyLayoutSnapshotRegistration(t *testing.T) productrpc.Registration {
	t.Helper()
	return productrpc.Registration{
		Method:         "legacyLayout.snapshot",
		Scope:          productcapabilities.WorkspaceScope,
		ValidateParams: func(json.RawMessage) error { return nil },
		Handler: func(context.Context, json.RawMessage) (any, error) {
			t.Fatalf("unrelated legacyLayout.snapshot ran")
			return nil, nil
		},
	}
}

func TestLegacyLayoutSnapshotPreservesRawPayloadsAndDiagnosesEachItem(t *testing.T) {
	fixture := newLegacyLayoutFixture(t)
	result, err := fixture.snapshot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		ContractVersion string `json:"contractVersion"`
		Interfaces      []struct {
			LogicalId   string `json:"logicalId"`
			Revision    string `json:"revision"`
			Payload     string `json:"rawPayload"`
			Diagnostics []struct {
				Code     string `json:"code"`
				Severity string `json:"severity"`
			} `json:"diagnostics"`
		} `json:"interfaces"`
		Dashboards []struct {
			LogicalId   string `json:"logicalId"`
			Diagnostics []struct {
				Code string `json:"code"`
			} `json:"diagnostics"`
		} `json:"dashboards"`
		Panels []struct {
			LogicalId   string `json:"logicalId"`
			Diagnostics []struct {
				Code string `json:"code"`
			} `json:"diagnostics"`
		} `json:"panels"`
		Diagnostics []string `json:"diagnostics"`
	}
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ContractVersion != "1.0" {
		t.Fatalf("contract version %q", snapshot.ContractVersion)
	}
	if len(snapshot.Interfaces) != 3 || len(snapshot.Dashboards) != 1 || len(snapshot.Panels) != 1 {
		t.Fatalf("counts mismatch: %+v", snapshot)
	}
	byID := map[string]json.RawMessage{}
	diagByCode := map[string]string{}
	for _, item := range snapshot.Interfaces {
		byID[item.LogicalId] = json.RawMessage(item.Payload)
		for _, diagnostic := range item.Diagnostics {
			diagByCode[diagnostic.Code] = item.LogicalId
			if diagnostic.Severity != "warning" && diagnostic.Severity != "error" {
				t.Fatalf("bad severity for %s", item.LogicalId)
			}
		}
		if item.LogicalId != "interface-corrupt" && item.Revision == "" {
			t.Fatalf("missing payload-derived revision for %s", item.LogicalId)
		}
	}
	// Replaceable Interface: valid, identity-consistent, record-manager kinds.
	if diagByCode["legacy.element_manual_rewrite"] != "interface-ops" {
		t.Fatalf("chart element not diagnosed for manual rewrite: %+v", diagByCode)
	}
	if diagByCode["legacy.plugin_action_unsupported"] != "interface-ops" {
		t.Fatalf("legacy plugin action not diagnosed: %+v", diagByCode)
	}
	var opsPayload struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(byID["interface-ops"], &opsPayload); err != nil || opsPayload.Name != "Ops" {
		t.Fatalf("raw interface payload not preserved verbatim: %s", byID["interface-ops"])
	}
	// Corrupt payload stays preserved as-is with a per-item diagnostic.
	if string(byID["interface-corrupt"]) != `"not-an-object"` {
		t.Fatalf("corrupt payload not verbatim: %s", byID["interface-corrupt"])
	}
	if diagByCode["legacy.payload_not_object"] != "interface-corrupt" {
		t.Fatalf("corrupt payload not diagnosed: %+v", diagByCode)
	}
	// Identity-consistent dashboard and panel records stay clean; the panel
	// query with kind/collection/dimensions/measures stays opaque instead of
	// being misdiagnosed through the Interface ViewQuery field set.
	for _, dashboard := range snapshot.Dashboards {
		for _, diagnostic := range dashboard.Diagnostics {
			if diagnostic.Code == "legacy.identity_mismatch" || diagnostic.Code == "legacy.field_unknown" {
				t.Fatalf("consistent dashboard %s misdiagnosed: %s", dashboard.LogicalId, diagnostic.Code)
			}
		}
	}
	for _, panel := range snapshot.Panels {
		for _, diagnostic := range panel.Diagnostics {
			if diagnostic.Code == "legacy.identity_mismatch" || diagnostic.Code == "legacy.field_unknown" {
				t.Fatalf("consistent panel %s misdiagnosed: %s", panel.LogicalId, diagnostic.Code)
			}
		}
	}
	if len(snapshot.Diagnostics) == 0 {
		t.Fatal("no aggregate diagnostics reported")
	}
}

func TestLegacyLayoutSnapshotKeepsBrokenBytesParentAndUnknownPaths(t *testing.T) {
	fixture := newLegacyLayoutFixture(t)
	broken := "{broken JSON with original whitespace  "
	if _, err := fixture.pb.DB().NewQuery("UPDATE vibetable_interfaces SET payload_json={:payload} WHERE logical_id='interface-corrupt'").Bind(dbx.Params{"payload": broken}).Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pb.DB().NewQuery("UPDATE vibetable_panels SET parent_id='independent-parent' WHERE logical_id='panel-1'").Execute(); err != nil {
		t.Fatal(err)
	}
	raw := `{"contractVersion":"future","interfaceId":"interface-ops","unknown":{"retain":true},"pages":[{"pageId":"main","elements":[{"elementId":"section","kind":"section","children":[{"elementId":"future","kind":"future-widget","extra":7}]}]}]}`
	if _, err := fixture.pb.DB().NewQuery("UPDATE vibetable_interfaces SET payload_json={:payload} WHERE logical_id='interface-ops'").Bind(dbx.Params{"payload": raw}).Execute(); err != nil {
		t.Fatal(err)
	}
	first, err := collectLegacyPages(context.Background(), fixture.pb)
	if err != nil {
		t.Fatal(err)
	}
	second, err := collectLegacyPages(context.Background(), fixture.pb)
	if err != nil {
		t.Fatal(err)
	}
	first.CollectedAt = ""
	second.CollectedAt = ""
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeat preservation changed original definitions")
	}
	paths := map[string]bool{}
	for _, item := range first.Interfaces {
		if item.RecordID == "" {
			t.Fatal("missing stable storage id")
		}
		if item.LogicalId == "interface-corrupt" && (item.RawPayload != broken || item.Revision != "") {
			t.Fatalf("lost broken bytes: %+v", item)
		}
		if item.LogicalId == "interface-ops" && item.RawPayload != raw {
			t.Fatal("changed raw payload")
		}
		for _, diagnostic := range item.Diagnostics {
			paths[diagnostic.Path] = true
		}
	}
	if !paths["$.unknown"] || !paths["$.pages[0].elements[0].children[0].kind"] || !paths["$.pages[0].elements[0].children[0].extra"] {
		t.Fatalf("missing actionable unknown paths: %+v", paths)
	}
	if first.Panels[0].ParentID != "independent-parent" {
		t.Fatal("parent must come from storage, not payload")
	}
	// The diverging storage parent versus payload.dashboardId must surface as
	// an identity mismatch, not just generic manual-rewrite guidance, while
	// both identities stay preserved unchanged.
	panelMismatch := false
	for _, diagnostic := range first.Panels[0].Diagnostics {
		if diagnostic.Code == "legacy.identity_mismatch" {
			panelMismatch = true
		}
	}
	if !panelMismatch {
		t.Fatalf("storage/payload parent divergence not diagnosed: %+v", first.Panels[0].Diagnostics)
	}
	if _, err := json.Marshal(first); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collectLegacyPages(canceled, fixture.pb); err == nil {
		t.Fatal("cancelled read succeeded")
	}
}

func TestLegacyLayoutSnapshotRejectsCallerPayload(t *testing.T) {
	validate := legacyLayoutSnapshotRegistration(nil).ValidateParams
	for _, raw := range []string{`null`, `[]`, `{"path":"C:/escape"}`, `{"payload":{}}`, `{} {}`} {
		if validate(json.RawMessage(raw)) == nil {
			t.Fatalf("accepted caller data: %s", raw)
		}
	}
	if err := validate(json.RawMessage(`{"collection":"interfaces","afterRecordId":"","recordOffset":0}`)); err != nil {
		t.Fatal(err)
	}
}

// Materialization belongs only to small test fixtures; production Host streams.
func collectLegacyPages(ctx context.Context, pb *pocketbase.PocketBase) (metadata.LegacyLayoutSnapshot, error) {
	result := metadata.LegacyLayoutSnapshot{ContractVersion: "1.0"}
	service := metadata.NewLegacyPreservation(pb)
	for _, ns := range []metadata.Namespace{metadata.NamespaceInterfaces, metadata.NamespaceDashboards, metadata.NamespacePanels} {
		request := metadata.LegacyLayoutPageRequest{Collection: ns}
		var record bytes.Buffer
		for {
			page, err := service.Page(ctx, request)
			if err != nil {
				return result, err
			}
			if page.CollectionDone {
				break
			}
			record.Write(page.Chunk)
			if page.RecordDone {
				var item metadata.LegacyLayoutItem
				if err := json.Unmarshal(record.Bytes(), &item); err != nil {
					return result, err
				}
				switch ns {
				case metadata.NamespaceInterfaces:
					result.Interfaces = append(result.Interfaces, item)
				case metadata.NamespaceDashboards:
					result.Dashboards = append(result.Dashboards, item)
				case metadata.NamespacePanels:
					result.Panels = append(result.Panels, item)
				}
				for _, d := range item.Diagnostics {
					result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s/%s: %s", ns, item.LogicalId, d.Code))
				}
				record.Reset()
			}
			request.AfterRecordID = page.NextAfterRecordID
			request.RecordOffset = page.NextRecordOffset
		}
	}
	return result, nil
}

func legacyPageServer(t *testing.T, pb *pocketbase.PocketBase) *httptest.Server {
	t.Helper()
	registrations := []productrpc.Registration{legacyLayoutSnapshotRegistration(metadata.NewLegacyPreservation(pb))}
	for _, descriptor := range productcapabilities.RPCDescriptors() {
		if descriptor.Owner != productcapabilities.GoSidecar || descriptor.Method == "legacyLayout.snapshot" {
			continue
		}
		registrations = append(registrations, productrpc.Registration{Method: descriptor.Method, Scope: descriptor.Scope,
			ValidateParams: func(json.RawMessage) error { t.Fatal("unrelated validation"); return nil },
			Handler:        func(context.Context, json.RawMessage) (any, error) { t.Fatal("unrelated handler"); return nil, nil }})
	}
	dispatcher, err := productrpc.New(productrpc.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, registrations...)
	if err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: pb, Event: router.Event{Response: w, Request: request}}, nil
	})
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestLegacyLayoutHTTPPagesPreserveMoreThanSixteenMiB(t *testing.T) {
	fixture := newLegacyLayoutFixture(t)
	collection, err := fixture.pb.FindCollectionByNameOrId("vibetable_interfaces")
	if err != nil {
		t.Fatal(err)
	}
	if collection.Fields.GetByName("payload_json").(*core.JSONField).CalculateMaxBodySize() != 1<<20 {
		t.Fatal("historical JSON budget changed")
	}
	expected := map[string]string{}
	for index := 0; index < 20; index++ {
		id := fmt.Sprintf("large-%02d", index)
		raw, _ := json.Marshal(map[string]any{"contractVersion": "1.0", "interfaceId": id, "name": strings.Repeat("x", 900000) + "中😀<&", "pages": []any{}})
		seedRecord(t, fixture.pb, "vibetable_interfaces", id, raw)
		var stored struct {
			Payload string `db:"payload_json"`
		}
		if err := fixture.pb.DB().NewQuery("SELECT payload_json FROM vibetable_interfaces WHERE logical_id={:id}").Bind(dbx.Params{"id": id}).One(&stored); err != nil {
			t.Fatal(err)
		}
		expected[id] = stored.Payload
	}
	before := previewAuthorityState(t, fixture.pb, "vibetable_interfaces")
	server := legacyPageServer(t, fixture.pb)
	request := metadata.LegacyLayoutPageRequest{Collection: metadata.NamespaceInterfaces}
	var current bytes.Buffer
	total, pages, found := 0, 0, 0
	for {
		raw, _ := json.Marshal(request)
		response := metadataHTTPRequest(t, server, "legacyLayout.snapshot", string(raw))
		if response.Error != nil {
			t.Fatalf("HTTP page rejected: %+v", response.Error)
		}
		if len(response.Result) >= 4<<20 {
			t.Fatal("page exceeded real product HTTP response budget")
		}
		var page metadata.LegacyLayoutPage
		if err := json.Unmarshal(response.Result, &page); err != nil {
			t.Fatal(err)
		}
		if page.CollectionDone {
			break
		}
		pages++
		total += len(page.Chunk)
		current.Write(page.Chunk)
		if len(page.Chunk) > metadata.LegacyChunkBytes {
			t.Fatal("unbounded page")
		}
		if page.RecordDone {
			var item metadata.LegacyLayoutItem
			if err := json.Unmarshal(current.Bytes(), &item); err != nil {
				t.Fatal(err)
			}
			if raw, ok := expected[item.LogicalId]; ok {
				found++
				if item.RawPayload != raw || item.RecordID == "" || item.Revision == "" {
					t.Fatalf("record changed: %s", item.LogicalId)
				}
			}
			current.Reset()
		}
		request.AfterRecordID = page.NextAfterRecordID
		request.RecordOffset = page.NextRecordOffset
	}
	t.Logf("preserved %d bytes across %d bounded HTTP pages; %d large originals reconciled", total, pages, found)
	if total <= 16<<20 || found != 20 || pages <= 20 {
		t.Fatalf("insufficient multi-page proof bytes=%d found=%d pages=%d", total, found, pages)
	}
	if !reflect.DeepEqual(before, previewAuthorityState(t, fixture.pb, "vibetable_interfaces")) {
		t.Fatal("preservation wrote original records")
	}
	for _, raw := range []string{
		`{"Collection":"interfaces"}`, `{"collection":"interfaces","RECORDOFFSET":0}`,
		`{"collection":"interfaces","limit":999999}`, `{"collection":"sharedSettings"}`,
		`{"collection":"interfaces","recordOffset":1}`, `{"collection":"interfaces","recordOffset":null}`,
		`{"collection":"interfaces","afterRecordId":null}`, `{"collection":"interfaces","afterRecordId":"../bad"}`,
	} {
		if legacyLayoutSnapshotRegistration(nil).ValidateParams(json.RawMessage(raw)) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestLegacyLayoutDiagnosticPreviewIsExplicitAndExportIsComplete(t *testing.T) {
	fixture := newLegacyLayoutFixture(t)
	value := map[string]any{"contractVersion": "1.0", "interfaceId": "many-diagnostics", "pages": []any{}}
	for index := 0; index < 120; index++ {
		value[fmt.Sprintf("unknown-%03d", index)] = true
	}
	value[strings.Repeat("long", 200)] = true
	raw, _ := json.Marshal(value)
	seedRecord(t, fixture.pb, "vibetable_interfaces", "many-diagnostics", raw)
	service := metadata.NewLegacyPreservation(fixture.pb)
	request := metadata.LegacyLayoutPageRequest{Collection: metadata.NamespaceInterfaces}
	found := false
	for {
		page, err := service.Page(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if page.CollectionDone {
			break
		}
		if page.LogicalID == "many-diagnostics" {
			if page.DiagnosticCount != 121 || len(page.Diagnostics) != 100 || !page.PreviewTruncated {
				t.Fatalf("preview lost totals: %+v", page)
			}
			var item metadata.LegacyLayoutItem
			if !page.RecordDone || json.Unmarshal(page.Chunk, &item) != nil || len(item.Diagnostics) != 121 {
				t.Fatal("full export diagnostics were truncated")
			}
			found = true
		}
		request.AfterRecordID = page.NextAfterRecordID
		request.RecordOffset = page.NextRecordOffset
	}
	if !found {
		t.Fatal("missing diagnostic fixture")
	}
}
