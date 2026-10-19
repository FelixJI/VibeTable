package metadata

import (
	"encoding/json"
	"testing"
)

// diagnoseCodes summarizes one diagnosis by code so the focused checks below
// stay readable while missing/extra diagnostics still fail.
func diagnoseCodes(t *testing.T, namespace Namespace, logicalID, parentID, payload string) map[string]int {
	t.Helper()
	diagnostics := diagnoseLegacyItem(namespace, logicalID, parentID, json.RawMessage(payload))
	codes := map[string]int{}
	for _, diagnostic := range diagnostics {
		codes[diagnostic.Code]++
		if diagnostic.Message == "" {
			t.Fatalf("%s diagnostic %s has no message", namespace, diagnostic.Code)
		}
	}
	return codes
}

// A legitimate retired dashboard panel query uses kind/collection/dimensions/
// measures/timeBucket/topN (and records-query fields); it must stay opaque
// instead of being misdiagnosed through the Interface ViewQuery field set.
func TestDiagnoseLegacyItemKeepsDashboardPanelQueryOpaque(t *testing.T) {
	panel := `{"id":"panel-1","dashboardId":"dash-1","name":"Revenue","type":"bar",` +
		`"position":{"x":0,"y":0,"width":4,"height":3},"options":{"legend":true},` +
		`"query":{"kind":"aggregate","collection":"orders","filters":[],` +
		`"dimensions":["createdAt"],"measures":[{"key":"total","op":"sum","field":"amount"}],` +
		`"timeBucket":{"field":"createdAt","unit":"month","timezone":"UTC"},` +
		`"topN":10,"limit":100,"fields":["region"],` +
		`"sorts":[{"field":"region","direction":"desc","nullsLast":true}]}}`
	codes := diagnoseCodes(t, NamespacePanels, "panel-1", "dash-1", panel)
	if codes["legacy.field_unknown"] != 0 {
		t.Fatalf("legitimate dashboard query misdiagnosed through Interface fields: %+v", codes)
	}
	if codes["legacy.identity_mismatch"] != 0 || codes["legacy.dashboard_manual_rewrite"] != 1 {
		t.Fatalf("panel guidance changed: %+v", codes)
	}
	dashboard := `{"id":"dash-1","name":"Operations","config":{"globalFilters":[{"targetPanels":["panel-1"],` +
		`"query":{"kind":"aggregate","collection":"orders","measures":[{"key":"total","op":"count"}]}}]}}`
	codes = diagnoseCodes(t, NamespaceDashboards, "dash-1", "", dashboard)
	if codes["legacy.field_unknown"] != 0 || codes["legacy.identity_mismatch"] != 0 {
		t.Fatalf("dashboard config query misdiagnosed: %+v", codes)
	}
	if codes["legacy.dashboard_manual_rewrite"] != 1 {
		t.Fatalf("dashboard guidance changed: %+v", codes)
	}
}

// Interface binding queries keep the strict retired ViewQuery field set.
func TestDiagnoseLegacyItemKeepsInterfaceBindingQueryStrict(t *testing.T) {
	definition := `{"contractVersion":"1.0","interfaceId":"interface-1","name":"Orders",` +
		`"bindings":[{"bindingId":"b1","query":{"contractVersion":"1.0","tableId":"orders",` +
		`"fields":[],"filters":[],"sorts":[],"cursor":null,"pageSize":50,"surprise":true}}],` +
		`"actions":[],"pages":[]}`
	diagnostics := diagnoseLegacyItem(NamespaceInterfaces, "interface-1", "", json.RawMessage(definition))
	found := false
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "legacy.field_unknown" {
			found = diagnostic.Path == "$.bindings[0].query.surprise"
		}
	}
	if !found {
		t.Fatalf("Interface query strictness lost: %+v", diagnostics)
	}
}

// The retired dashboard writer stored payload.id == logical_id and panel
// payload.dashboardId == parent_id; every divergence — wrong value, missing
// field, or wrong JSON type — is an identity mismatch, while consistent
// records stay clean. Identities and raw payloads are never rewritten.
func TestDiagnoseLegacyItemChecksDashboardAndPanelIdentity(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		namespace Namespace
		logicalID string
		parentID  string
		payload   string
		mismatch  int
	}{
		{"dashboard consistent", NamespaceDashboards, "dash-1", "",
			`{"id":"dash-1","name":"Operations"}`, 0},
		{"dashboard id diverges", NamespaceDashboards, "dash-1", "",
			`{"id":"other","name":"Operations"}`, 1},
		{"dashboard id missing", NamespaceDashboards, "dash-1", "",
			`{"name":"Operations"}`, 1},
		{"dashboard id wrong type", NamespaceDashboards, "dash-1", "",
			`{"id":7,"name":"Operations"}`, 1},
		{"panel consistent", NamespacePanels, "panel-1", "dash-1",
			`{"id":"panel-1","dashboardId":"dash-1","name":"Revenue","query":{}}`, 0},
		{"panel id diverges", NamespacePanels, "panel-1", "dash-1",
			`{"id":"panel-9","dashboardId":"dash-1","name":"Revenue"}`, 1},
		{"panel parent diverges", NamespacePanels, "panel-1", "dash-1",
			`{"id":"panel-1","dashboardId":"elsewhere","name":"Revenue"}`, 1},
		{"panel parent missing", NamespacePanels, "panel-1", "dash-1",
			`{"id":"panel-1","name":"Revenue"}`, 1},
		{"panel parent wrong type", NamespacePanels, "panel-1", "dash-1",
			`{"id":"panel-1","dashboardId":123,"name":"Revenue"}`, 1},
		{"panel storage parent empty", NamespacePanels, "panel-1", "",
			`{"id":"panel-1","dashboardId":"dash-1","name":"Revenue"}`, 1},
		{"panel both identities diverge", NamespacePanels, "panel-1", "dash-1",
			`{"id":"panel-9","dashboardId":"elsewhere"}`, 2},
	} {
		codes := diagnoseCodes(t, fixture.namespace, fixture.logicalID, fixture.parentID, fixture.payload)
		if codes["legacy.identity_mismatch"] != fixture.mismatch {
			t.Fatalf("%s: identity_mismatch = %d, want %d (%+v)", fixture.name,
				codes["legacy.identity_mismatch"], fixture.mismatch, codes)
		}
		if codes["legacy.dashboard_manual_rewrite"] != 1 {
			t.Fatalf("%s: manual-rewrite guidance lost: %+v", fixture.name, codes)
		}
	}
	// A payload that is not an object keeps its dedicated diagnostics.
	for _, raw := range []string{`"scalar"`, `not-json`} {
		codes := diagnoseCodes(t, NamespaceDashboards, "dash-1", "", raw)
		if len(codes) != 1 {
			t.Fatalf("corrupt dashboard payload produced extra diagnostics: %+v", codes)
		}
		for code := range codes {
			if code != "legacy.payload_unparseable" && code != "legacy.payload_not_object" {
				t.Fatalf("unexpected code %s for %s", code, raw)
			}
		}
	}
	// Interface identity checks keep their existing single-diagnostic contract.
	codes := diagnoseCodes(t, NamespaceInterfaces, "interface-1", "",
		`{"contractVersion":"1.0","interfaceId":"other","actions":[],"pages":[]}`)
	if codes["legacy.identity_mismatch"] != 1 {
		t.Fatalf("interface identity check changed: %+v", codes)
	}
}
