package app

import (
	"reflect"
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func withoutCommonDisplayPresets(presets []string) []string {
	kept := make([]string, 0, len(presets))
	for _, preset := range presets {
		if preset != "phone" && preset != "progress" && preset != "rating" {
			kept = append(kept, preset)
		}
	}
	return kept
}

// Upgrade only the three new menu entries in captured inputs; all historical
// capability members and preset order must still match the frozen fixture.
func withCommonDisplayPresets(t *testing.T, snapshot v2.SchemaSnapshot) v2.SchemaSnapshot {
	t.Helper()
	snapshot.Capabilities = append([]v2.Capability{}, snapshot.Capabilities...)
	for i, old := range snapshot.Capabilities {
		current, err := v2.CapabilityFor(old.LogicalType)
		if err != nil {
			t.Fatal(err)
		}
		historical := current
		if old.LogicalType == v2.LogicalText || old.LogicalType == v2.LogicalNumber {
			historical.DisplayPresets = withoutCommonDisplayPresets(current.DisplayPresets)
		}
		if !reflect.DeepEqual(historical, old) {
			t.Fatalf("historical capability changed: %s", old.LogicalType)
		}
		old.DisplayPresets = current.DisplayPresets
		snapshot.Capabilities[i] = old
	}
	return snapshot
}

func TestCommonDisplayCapabilitiesExposeOnlyDeclaredPresets(t *testing.T) {
	text, err := v2.CapabilityFor(v2.LogicalText)
	if err != nil {
		t.Fatal(err)
	}
	number, err := v2.CapabilityFor(v2.LogicalNumber)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(text.DisplayPresets, []string{"phone"}) || !reflect.DeepEqual(number.DisplayPresets, []string{"number", "integer", "currency", "percent", "unit", "progress", "rating"}) {
		t.Fatal("business display menu differs from the declared contract")
	}
	if number.Recommended.Storage.Options.OnlyInt || number.Recommended.Constraints.Range.Min != nil || number.Recommended.Constraints.Range.Max != nil {
		t.Fatal("display menu changed default number constraints")
	}
}

func assertAndStripCommonDisplayPresets(t *testing.T, snapshot map[string]any) {
	t.Helper()
	for _, raw := range snapshot["capabilities"].([]any) {
		capability := raw.(map[string]any)
		kind := capability["logicalType"]
		if kind != "text" && kind != "number" {
			continue
		}
		presets := capability["displayPresets"].([]any)
		if kind == "text" && !reflect.DeepEqual(presets, []any{"phone"}) {
			t.Fatalf("phone producer omitted new menu: %#v", presets)
		}
		if kind == "number" && !reflect.DeepEqual(presets, []any{"number", "integer", "currency", "percent", "unit", "progress", "rating"}) {
			t.Fatalf("number producer omitted business menus: %#v", presets)
		}
		kept := []any{}
		for _, preset := range presets {
			if preset != "phone" && preset != "progress" && preset != "rating" {
				kept = append(kept, preset)
			}
		}
		capability["displayPresets"] = kept
	}
}
