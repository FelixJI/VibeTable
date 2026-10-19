package app

import (
	"context"
	"encoding/json"
	"github.com/pocketbase/dbx"
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

	fixture := &legacyLayoutFixture{
		pb:   pb,
		wire: schemaListWire,
	}
	rejection := retiredLayoutRegistration("interface.commit")
	fixture.handler = rejection.Handler
	snapshotRegistration := legacyLayoutSnapshotRegistration(metadata.NewLegacyPreservation(pb))
	fixture.snapshot = snapshotRegistration.Handler
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
			LogicalId   string          `json:"logicalId"`
			Revision    string          `json:"revision"`
			Payload     json.RawMessage `json:"payload"`
			Diagnostics []struct {
				Code     string `json:"code"`
				Severity string `json:"severity"`
			} `json:"diagnostics"`
		} `json:"interfaces"`
		Dashboards []struct {
			LogicalId string `json:"logicalId"`
		} `json:"dashboards"`
		Panels []struct {
			LogicalId string `json:"logicalId"`
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
		byID[item.LogicalId] = item.Payload
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
	first, err := metadata.NewLegacyPreservation(fixture.pb).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := metadata.NewLegacyPreservation(fixture.pb).Snapshot(context.Background())
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
		if item.LogicalId == "interface-corrupt" && (item.RawPayload != broken || string(item.Payload) != "null" || item.Revision != "") {
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
	if _, err := json.Marshal(first); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := metadata.NewLegacyPreservation(fixture.pb).Snapshot(canceled); err == nil {
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
	if err := validate(json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
}
