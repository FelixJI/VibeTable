package pluginstore

import "testing"

// Persistent field protection (#591) is a closed manifest suggestion. Go stores
// it with the installation and decodes it strictly on every read, so the
// authoritative install boundary refuses anything the stored snapshot could not
// decode or bind to the sibling model. Protection itself stays OFF until the
// host records an explicit enable event.

func fieldProtectionTestManifest(protection any, operations []string) map[string]any {
	manifest := presentationTestManifest()
	// Native surface actions require their page to be declared in the manifest.
	manifest["ui"] = map[string]any{"customViews": []any{map[string]any{
		"viewId": "open", "actionId": "open", "entry": "ui/index.html",
	}}}
	if operations == nil {
		operations = []string{"read", "query", "create", "update"}
	}
	manifest["permissions"] = map[string]any{
		"model": map[string]any{
			"contract": "vibetable.plugin-model.v1",
			"modelId":  "sample_model",
			"version":  1,
			"entities": []any{map[string]any{
				"key": "task", "label": "Task", "operations": operations,
				"fields": []any{map[string]any{"key": "title", "label": "Title", "type": "text"}},
			}},
		},
		"fieldProtection": protection,
	}
	return manifest
}

func TestFieldProtectionManifestDeclarationIsClosedAndBound(t *testing.T) {
	check := func(name string, manifest map[string]any, wantValid bool) {
		t.Helper()
		err := validateManifest(manifest, manifest["pluginId"].(string), manifest["version"].(string))
		if wantValid && err != nil {
			t.Fatal(name, " rejected:", err)
		}
		if !wantValid && err == nil {
			t.Fatal(name, " accepted")
		}
	}
	declared := func(fields ...any) map[string]any {
		return map[string]any{"contract": "vibetable.plugin-field-protection.v1", "fields": fields}
	}
	entry := func(entity, field string) map[string]any {
		return map[string]any{"entity": entity, "field": field}
	}

	check("bound suggestion", fieldProtectionTestManifest(declared(entry("task", "title")), nil), true)
	lifecycle := fieldProtectionTestManifest(declared(entry("task", "title")), nil)
	lifecycle["compatibility"].(map[string]any)["surfaceLifecycle"] = surfaceLifecycleContract
	check("bound suggestion and background preparation", lifecycle, true)
	check("bound suggestion on delete-only entity", fieldProtectionTestManifest(
		declared(entry("task", "title")), []string{"read", "query", "delete"}), true)
	check("missing model", func() map[string]any {
		manifest := fieldProtectionTestManifest(declared(entry("task", "title")), nil)
		delete(manifest["permissions"].(map[string]any), "model")
		return manifest
	}(), false)
	check("null declaration", fieldProtectionTestManifest(nil, nil), false)
	check("unknown contract", fieldProtectionTestManifest(
		map[string]any{"contract": "vibetable.plugin-field-protection.v2", "fields": []any{entry("task", "title")}}, nil), false)
	check("unknown key", fieldProtectionTestManifest(
		map[string]any{"contract": "vibetable.plugin-field-protection.v1", "fields": []any{entry("task", "title")}, "on": true}, nil), false)
	check("missing fields", fieldProtectionTestManifest(
		map[string]any{"contract": "vibetable.plugin-field-protection.v1"}, nil), false)
	check("empty fields", fieldProtectionTestManifest(declared(), nil), false)
	check("unknown entry key", fieldProtectionTestManifest(
		declared(map[string]any{"entity": "task", "field": "title", "label": "Title"}), nil), false)
	check("blank entry", fieldProtectionTestManifest(declared(entry("task", "")), nil), false)
	check("duplicate", fieldProtectionTestManifest(declared(entry("task", "title"), entry("task", "title")), nil), false)
	check("unknown entity", fieldProtectionTestManifest(declared(entry("missing", "title")), nil), false)
	check("unknown field", fieldProtectionTestManifest(declared(entry("task", "missing")), nil), false)
	// Protection means "only this plugin may change the field"; on an entity
	// the plugin never writes it would freeze the field for everyone.
	check("read-only entity", fieldProtectionTestManifest(
		declared(entry("task", "title")), []string{"read", "query"}), false)
	legacy := fieldProtectionTestManifest(declared(entry("task", "title")), nil)
	legacy["$schema"] = "vibetable.plugin-manifest.v1"
	legacy["compatibility"].(map[string]any)["pluginApi"] = "2.x"
	delete(legacy["permissions"].(map[string]any), "model")
	check("legacy package", legacy, false)
}
