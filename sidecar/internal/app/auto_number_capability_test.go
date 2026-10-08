package app

import (
	"encoding/json"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"reflect"
	"testing"
)

// Frozen authority fixtures predate autoNumber. Preserve every old capability
// verbatim and insert only the new capability, never regenerate the oracle.
func withAutoNumberCapability(t *testing.T, snapshot v2.SchemaSnapshot) v2.SchemaSnapshot {
	t.Helper()
	old := snapshot.Capabilities
	if len(old) != len(v2.LogicalTypes)-1 {
		t.Fatalf("unexpected historical capability count %d", len(old))
	}
	snapshot.Capabilities = make([]v2.Capability, 0, len(v2.LogicalTypes))
	i := 0
	for _, kind := range v2.LogicalTypes {
		expected, err := v2.CapabilityFor(kind)
		if err != nil {
			t.Fatal(err)
		}
		if kind == v2.LogicalAutoNumber {
			snapshot.Capabilities = append(snapshot.Capabilities, expected)
			continue
		}
		if !reflect.DeepEqual(old[i], expected) {
			t.Fatalf("historical capability %s changed", kind)
		}
		snapshot.Capabilities = append(snapshot.Capabilities, old[i])
		i++
	}
	return snapshot
}

func assertAndStripAutoNumberCapability(t *testing.T, snapshot map[string]any) {
	t.Helper()
	caps := snapshot["capabilities"].([]any)
	kept := make([]any, 0, len(caps)-1)
	found := 0
	expected, err := v2.CapabilityFor(v2.LogicalAutoNumber)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range caps {
		entry := raw.(map[string]any)
		if entry["logicalType"] != "autoNumber" {
			kept = append(kept, raw)
			continue
		}
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		var got v2.Capability
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("new capability differs from authority: %#v", got)
		}
		if !got.UserCreatable || got.SupportsDefault || got.SupportsRequired || got.SupportsUnique || got.NeedsPresence || got.CompileStrategy != "pocketbase-text" || got.Recommended.Display.Kind != v2.DisplayReadonly || !reflect.DeepEqual(got.AdvancedSettings, []string{"prefix", "start", "width"}) || len(got.ConversionTargets) != 0 {
			t.Fatalf("autoNumber readonly contract invalid: %#v", got)
		}
		found++
	}
	if found != 1 {
		t.Fatalf("expected exactly one autoNumber capability, got %d", found)
	}
	snapshot["capabilities"] = kept
}

func assertDescribeAutoNumberRevision(t *testing.T, snapshot v2.SchemaSnapshot, result map[string]any) {
	t.Helper()
	definition, err := schemaSnapshotProductResult(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := normalizeDescribeRanges(definition); err != nil {
		t.Fatal(err)
	}
	current, err := describeRevision(definition)
	if err != nil {
		t.Fatal(err)
	}
	schema := result["schema"].(map[string]any)
	if schema["capabilityHash"] != current {
		t.Fatal("production revision does not include the current capabilities")
	}
	assertAndStripAutoNumberCapability(t, definition)
	historical, err := describeRevision(definition)
	if err != nil {
		t.Fatal(err)
	}
	if historical == current {
		t.Fatal("new capability did not change the revision token")
	}
	// Compare this old-collection token to the untouched captured oracle below.
	schema["capabilityHash"] = historical
}
