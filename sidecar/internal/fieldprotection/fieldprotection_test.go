package fieldprotection

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
)

func testModel() *pluginmodel.Declaration {
	writes := []string{"read", "query", "create", "update", "delete"}
	return &pluginmodel.Declaration{ModelID: "synthetic", Version: 1, Entities: []pluginmodel.Entity{
		{Key: "headers", Label: "单头", Operations: writes, Fields: []pluginmodel.Field{{Key: "code", Label: "单号", Type: "text"}, {Key: "amount", Label: "金额", Type: "text"}}},
		{Key: "lines", Label: "明细", Operations: writes, Fields: []pluginmodel.Field{{Key: "header", Label: "单头", Type: "relation"}}},
		{Key: "notes", Label: "备注", Operations: []string{"read", "query"}, Fields: []pluginmodel.Field{{Key: "body", Label: "内容", Type: "text"}}},
	}}
}

func errorCode(err error) string {
	var protection *Error
	if errors.As(err, &protection) {
		return protection.Code
	}
	return ""
}

func TestDeclarationValidatesAgainstSiblingModel(t *testing.T) {
	model := testModel()
	cases := []struct {
		name        string
		declaration Declaration
		model       *pluginmodel.Declaration
		code        string
	}{
		{"wrong contract", Declaration{Contract: "vibetable.plugin-field-protection.v2", Fields: []DeclaredField{{"headers", "code"}}}, model, "plugin_field_protection.contract_invalid"},
		{"model missing", Declaration{Contract: Contract, Fields: []DeclaredField{{"headers", "code"}}}, nil, "plugin_field_protection.model_required"},
		{"no fields", Declaration{Contract: Contract, Fields: nil}, model, "plugin_field_protection.invalid"},
		{"empty entry", Declaration{Contract: Contract, Fields: []DeclaredField{{"headers", ""}}}, model, "plugin_field_protection.invalid"},
		{"duplicate", Declaration{Contract: Contract, Fields: []DeclaredField{{"headers", "code"}, {"headers", "code"}}}, model, "plugin_field_protection.duplicate"},
		{"unknown entity", Declaration{Contract: Contract, Fields: []DeclaredField{{"missing", "code"}}}, model, "plugin_field_protection.field_unknown"},
		{"unknown field", Declaration{Contract: Contract, Fields: []DeclaredField{{"headers", "missing"}}}, model, "plugin_field_protection.field_unknown"},
		{"read-only entity", Declaration{Contract: Contract, Fields: []DeclaredField{{"notes", "body"}}}, model, "plugin_field_protection.entity_read_only"},
	}
	for _, tc := range cases {
		if code := errorCode(tc.declaration.Validate(tc.model)); code != tc.code {
			t.Errorf("%s: code=%q want %q", tc.name, code, tc.code)
		}
	}
	valid := Declaration{Contract: Contract, Fields: []DeclaredField{{"headers", "code"}, {"lines", "header"}}}
	if err := valid.Validate(model); err != nil {
		t.Fatalf("valid declaration refused: %v", err)
	}
	if !valid.Includes("lines", "header") || valid.Includes("headers", "amount") {
		t.Fatal("Includes must follow the declared set exactly")
	}
	tooMany := Declaration{Contract: Contract}
	for i := 0; i < MaxProtectedFields+1; i++ {
		tooMany.Fields = append(tooMany.Fields, DeclaredField{"headers", "code"})
	}
	if code := errorCode(tooMany.Validate(model)); code != "plugin_field_protection.invalid" {
		t.Fatalf("over budget code=%q", code)
	}
	// The cap is the model quota (45 entities x 64 fields): the real #21 scale
	// (527 declared fields) and any full model subset fit without dropping fields.
	if MaxProtectedFields != 45*64 || MaxProtectedFields < 527 {
		t.Fatalf("MaxProtectedFields=%d must cover the model quota and the #21 scale", MaxProtectedFields)
	}
}

func TestDeclarationDecodingIsClosed(t *testing.T) {
	cases := map[string]string{
		"unknown key":    `{"contract":"vibetable.plugin-field-protection.v1","fields":[{"entity":"headers","field":"code"}],"protect":true}`,
		"missing fields": `{"contract":"vibetable.plugin-field-protection.v1"}`,
		"null fields":    `{"contract":"vibetable.plugin-field-protection.v1","fields":null}`,
		"empty field":    `{"contract":"vibetable.plugin-field-protection.v1","fields":[{"entity":"headers"}]}`,
		"unknown inner":  `{"contract":"vibetable.plugin-field-protection.v1","fields":[{"entity":"headers","field":"code","extra":1}]}`,
		"trailing":       `{"contract":"vibetable.plugin-field-protection.v1","fields":[]} {}`,
	}
	for name, raw := range cases {
		var declaration Declaration
		if err := json.Unmarshal([]byte(raw), &declaration); err == nil {
			t.Errorf("%s decoded", name)
		}
	}
	var declaration Declaration
	if err := json.Unmarshal([]byte(`{"contract":"vibetable.plugin-field-protection.v1","fields":[{"entity":"headers","field":"code"}]}`), &declaration); err != nil {
		t.Fatal(err)
	}
	if declaration.Fields[0] != (DeclaredField{Entity: "headers", Field: "code"}) {
		t.Fatalf("decoded = %#v", declaration)
	}
}

func enabled(eventID, plugin, installation string, refs ...FieldRef) (json.RawMessage, error) {
	entries := []Entry{}
	for _, ref := range refs {
		entries = append(entries, Entry{TableID: ref.TableID, FieldID: ref.FieldID, Entity: "headers", Field: "code", TableName: "单头", FieldName: "单号", FieldType: "text"})
	}
	return NewEnabledEvent(eventID, "local:project", plugin, "1.0.0", "hash", EnabledDetails{
		InstallationID: installation, InstallationRevision: 3, ModelID: "synthetic", ModelVersion: 1, Fields: entries,
	}, "2026-01-01T00:00:00Z")
}

func apply(t *testing.T, registry *Registry, event json.RawMessage, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.apply(json.RawMessage(event)); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryReplayLatestEventWins(t *testing.T) {
	registry := newRegistry()
	code, amount, lineHeader := FieldRef{"tbl", "fld_code"}, FieldRef{"tbl", "fld_amount"}, FieldRef{"tbl_lines", "fld_header"}
	event, err := enabled("evt1", "plugin.a", "inst_a1", code, amount)
	apply(t, registry, event, err)
	event, err = enabled("evt2", "plugin.b", "inst_b1", lineHeader)
	apply(t, registry, event, err)
	if registry.Count() != 3 || registry.Events() != 2 || registry.EnableEvents() != 2 {
		t.Fatalf("count=%d events=%d", registry.Count(), registry.Events())
	}
	released, err := NewReleasedEvent("evt3", "local:project", "plugin.a", "1.0.0", "hash", ReleasedDetails{Fields: []FieldRef{code}}, "2026-01-01T00:00:01Z")
	apply(t, registry, released, err)
	if _, protected := registry.Protected(code.TableID, code.FieldID); protected {
		t.Fatal("released field still protected")
	}
	if entry, ok := registry.Protected(amount.TableID, amount.FieldID); !ok || entry.PluginID != "plugin.a" {
		t.Fatalf("remaining entry = %#v", entry)
	}
	if got := len(registry.ForPlugin("plugin.a")); got != 1 {
		t.Fatalf("plugin.a entries = %d", got)
	}
	released, err = NewReleasedEvent("evt4", "local:project", "plugin.a", "1.0.0", "hash", ReleasedDetails{All: true}, "2026-01-01T00:00:02Z")
	apply(t, registry, released, err)
	if got := len(registry.ForPlugin("plugin.a")); got != 0 {
		t.Fatalf("release all left %d entries", got)
	}
	// Another plugin's entries are never touched by a foreign release.
	if registry.EnableEvents() != 2 || registry.Events() != 4 {
		t.Fatalf("releases must not consume the enable budget: events=%d enables=%d", registry.Events(), registry.EnableEvents())
	}
	if entry, ok := registry.Protected(lineHeader.TableID, lineHeader.FieldID); !ok || entry.PluginID != "plugin.b" {
		t.Fatalf("plugin.b entry lost: %#v", entry)
	}
	if conflict, found := registry.OwnerConflict(lineHeader, "plugin.c"); !found || conflict.PluginID != "plugin.b" {
		t.Fatal("owner conflict must report the protecting plugin")
	}
	if _, found := registry.OwnerConflict(lineHeader, "plugin.b"); found {
		t.Fatal("the owner itself never conflicts (reinstall adoption)")
	}
}

func TestRegistryAllowsOnlyOwningInstallation(t *testing.T) {
	entry := Entry{PluginID: "plugin.a", InstallationID: "inst_a1"}
	if Allows(entry, nil) || Allows(entry, &Owner{PluginID: "plugin.a", InstallationID: "inst_a2"}) || Allows(entry, &Owner{PluginID: "plugin.b", InstallationID: "inst_a1"}) {
		t.Fatal("a foreign writer was allowed")
	}
	if !Allows(entry, &Owner{PluginID: "plugin.a", InstallationID: "inst_a1"}) {
		t.Fatal("the owning installation was refused")
	}
}

func TestRegistryFailsClosedOnCorruptEvents(t *testing.T) {
	cases := map[string]string{
		"not an event":  `"string"`,
		"unknown type":  `{"eventId":"e","projectKey":"local:p","pluginId":"plugin.a","pluginVersion":"1","packageHash":"h","eventType":"fieldProtection.other","outcome":"succeeded","actor":"local-user","startedAt":"2026-01-01T00:00:00Z","details":{}}`,
		"no details":    `{"eventId":"e","projectKey":"local:p","pluginId":"plugin.a","pluginVersion":"1","packageHash":"h","eventType":"fieldProtection.enabled","outcome":"succeeded","actor":"local-user","startedAt":"2026-01-01T00:00:00Z"}`,
		"no plugin":     `{"eventId":"e","projectKey":"local:p","pluginId":"","pluginVersion":"1","packageHash":"h","eventType":"fieldProtection.enabled","outcome":"succeeded","actor":"local-user","startedAt":"2026-01-01T00:00:00Z","details":{}}`,
		"empty release": `{"eventId":"e","projectKey":"local:p","pluginId":"plugin.a","pluginVersion":"1","packageHash":"h","eventType":"fieldProtection.released","outcome":"succeeded","actor":"local-user","startedAt":"2026-01-01T00:00:00Z","details":{"fields":[]}}`,
		"no install":    `{"eventId":"e","projectKey":"local:p","pluginId":"plugin.a","pluginVersion":"1","packageHash":"h","eventType":"fieldProtection.enabled","outcome":"succeeded","actor":"local-user","startedAt":"2026-01-01T00:00:00Z","details":{"installationId":"","modelId":"m","fields":[{"tableId":"t","fieldId":"f","entity":"e","field":"f"}]}}`,
	}
	for name, raw := range cases {
		if err := newRegistry().apply(json.RawMessage(raw)); errorCode(err) != "field_protection.events_corrupt" {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}

func TestEventsUsePluginAuditEnvelope(t *testing.T) {
	raw, err := enabled("evt1", "plugin.a", "inst_a1", FieldRef{"tbl", "fld_code"})
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if json.Unmarshal(raw, &event) != nil {
		t.Fatal("event must be a JSON object")
	}
	if event["eventType"] != EventEnabled || event["actor"] != "local-user" || event["outcome"] != "succeeded" {
		t.Fatalf("envelope = %v", event)
	}
	for _, key := range []string{"eventId", "projectKey", "pluginId", "pluginVersion", "packageHash", "startedAt", "details"} {
		if _, ok := event[key]; !ok {
			t.Errorf("missing envelope key %s", key)
		}
	}
	if details, _ := event["details"].(map[string]any); details["contract"] != Contract {
		t.Fatalf("details = %v", event["details"])
	}
	if !strings.Contains(string(raw), `"pluginVersion":"1.0.0"`) || !strings.Contains(string(raw), `"packageHash":"hash"`) {
		t.Fatal("the event must carry the plugin version and package hash for audit")
	}
}
