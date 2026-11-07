package transactionrules

import "testing"

func groupImages() Images {
	return Images{
		"invoices":     {"blue": {"agreement": nil}, "red": {"agreement": nil}},
		"actual_lines": {"b1": {"invoice": "blue"}, "b2": {"invoice": "blue"}, "r": {"invoice": "red"}},
		"allocations":  {"b1": {"line": "b1", "agreement": nil, "application_source": nil, "original": nil}, "b2": {"line": "b2", "agreement": nil, "application_source": nil, "original": nil}, "r": {"line": "r", "agreement": nil, "application_source": nil, "original": "b1"}},
		"requests":     {}, "request_lines": {}, "request_sources": {}, "agreements": {"A": {}, "B": {}},
	}
}
func groupSource(i Images, id, agreement string, original any) {
	i["requests"][id] = map[string]any{"agreement": agreement}
	i["request_lines"][id] = map[string]any{"request": id}
	i["request_sources"][id] = map[string]any{"line": id, "agreement": nil, "original": original}
}
func TestGroupSameTargetPreciseInheritanceAndReferences(t *testing.T) {
	rule := newKindRule(t, "group-same-target")
	d := Declaration{Contract: Contract, ModelID: "m", Version: 1, Rules: []Rule{rule}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	r := Registration{Declaration: d}
	for _, tc := range []struct {
		name  string
		valid bool
		edit  func(Images)
	}{
		{"unknown", true, func(Images) {}},
		{"same-invoice-conflict", false, func(i Images) { i["allocations"]["b1"]["agreement"] = "A"; i["allocations"]["b2"]["agreement"] = "B" }},
		{"different-invoices", true, func(i Images) {
			delete(i["allocations"], "r")
			i["actual_lines"]["b2"]["invoice"] = "red"
			i["allocations"]["b1"]["agreement"] = "A"
			i["allocations"]["b2"]["agreement"] = "B"
		}},
		{"anchor-request-four-hops", false, func(i Images) {
			groupSource(i, "bs", "B", nil)
			i["allocations"]["b1"]["application_source"] = "bs"
			i["allocations"]["r"]["agreement"] = "A"
		}},
		{"reference-sees-blue-sibling", false, func(i Images) {
			delete(i["allocations"], "r")
			i["allocations"]["b2"]["agreement"] = "B"
			groupSource(i, "s", "A", "b1")
		}},
		{"references-do-not-union", true, func(i Images) {
			delete(i["allocations"], "r")
			groupSource(i, "a", "A", "b1")
			groupSource(i, "b", "B", "b1")
		}},
		{"direct-red-does-not-inherit-sibling", true, func(i Images) { i["allocations"]["b2"]["agreement"] = "B"; i["allocations"]["r"]["agreement"] = "A" }},
		{"missing-related", false, func(i Images) { delete(i["allocations"]["r"], "original") }},
		{"bad-anchor", false, func(i Images) { delete(i["allocations"], "b1") }},
		{"bad-target", false, func(i Images) { i["allocations"]["b2"]["agreement"] = "missing" }},
		{"missing-source-request", false, func(i Images) {
			groupSource(i, "s", "A", nil)
			i["allocations"]["r"]["application_source"] = "s"
			delete(i["requests"], "s")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := groupImages()
			tc.edit(i)
			if err := r.Validate(nil, i); (err == nil) != tc.valid {
				t.Fatalf("valid=%v got %v", tc.valid, err)
			}
		})
	}
	invalid := groupImages()
	invalid["allocations"]["b1"]["agreement"] = "A"
	invalid["allocations"]["b2"]["agreement"] = "B"
	repaired := clone(invalid)
	repaired["allocations"]["b2"]["agreement"] = "A"
	if err := r.Validate(invalid, repaired); err != nil {
		t.Fatalf("atomic final graph repair rejected: %v", err)
	}
}
