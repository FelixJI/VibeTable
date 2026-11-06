package transactionrules

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
)

func reversalDeclaration(t *testing.T) Declaration {
	t.Helper()
	raw, err := os.ReadFile("../../../contracts/plugin-reversal-rule.json")
	if err != nil {
		t.Fatal(err)
	}
	wire := map[string]any{"contract": Contract, "modelId": "m", "version": 1,
		"decimals": []any{map[string]any{"entity": "lines", "field": "quantity", "storage": "decimalText", "scale": 0, "digits": 38, "sign": "positive"}},
		"rules":    []json.RawMessage{raw}}
	encoded, _ := json.Marshal(wire)
	declaration, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return declaration
}

func TestReversalBalanceDeclaration(t *testing.T) {
	declaration := reversalDeclaration(t)
	if err := declaration.Validate(); err != nil {
		t.Fatal(err)
	}
}

func reversalImages() Images {
	return Images{"documents": {"original": {"phase": "delivery_confirmed", "agreement": nil}, "return": {"phase": "delivery_confirmed", "agreement": nil}},
		"lines": {
			"original": {"document": "original", "purpose": "normal", "quantity": "10", "material": "m", "project": "p", "package": nil, "unit": "u", "currency": "CNY"},
			"return":   {"document": "return", "purpose": "salesReturn", "quantity": "6", "original_line": "original", "material": "m", "project": "p", "package": nil, "unit": "u", "currency": "CNY"},
		}}
}

func TestReversalBalanceFinalGraphAndSnapshots(t *testing.T) {
	registration := Registration{Declaration: reversalDeclaration(t)}
	if err := registration.Validate(nil, reversalImages()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(Images)
	}{
		{"missing", "reversal_invalid", func(i Images) { delete(i["lines"]["return"], "original_line") }},
		{"self", "reversal_invalid", func(i Images) { i["lines"]["return"]["original_line"] = "return" }},
		{"not-normal", "reversal_invalid", func(i Images) { i["lines"]["original"]["purpose"] = "gift" }},
		{"original-draft", "reversal_invalid", func(i Images) { i["documents"]["original"]["phase"] = "delivery_draft" }},
		{"unit", "reversal_mismatch", func(i Images) { i["lines"]["return"]["unit"] = "other" }},
		{"currency", "reversal_mismatch", func(i Images) { i["lines"]["return"]["currency"] = "USD" }},
		{"material", "reversal_mismatch", func(i Images) { i["lines"]["return"]["material"] = "other" }},
		{"project", "reversal_mismatch", func(i Images) { i["lines"]["return"]["project"] = "other" }},
		{"null-package", "reversal_mismatch", func(i Images) { i["lines"]["return"]["package"] = "other" }},
		{"null-agreement", "reversal_mismatch", func(i Images) { i["documents"]["return"]["agreement"] = "other" }},
		{"single-overflow", "reversal_exceeded", func(i Images) { i["lines"]["return"]["quantity"] = "11" }},
		{"aggregate", "reversal_exceeded", func(i Images) { i["lines"]["r2"] = clone(i)["lines"]["return"] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			images := reversalImages()
			tc.mutate(images)
			if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction."+tc.want {
				t.Fatalf("want %s got %v", tc.want, err)
			}
		})
	}
	for _, phase := range []string{"instruction_draft", "delivery_draft"} {
		images := reversalImages()
		images["documents"]["return"]["phase"] = phase
		images["documents"]["original"]["phase"] = "delivery_draft"
		images["lines"]["return"]["quantity"] = "11"
		if err := registration.Validate(nil, images); err != nil {
			t.Fatalf("editable %s: %v", phase, err)
		}
	}
	for _, phase := range []string{"instruction_open", "instruction_closed", "instruction_transferred"} {
		images := reversalImages()
		images["documents"]["return"]["phase"] = phase
		images["lines"]["r2"] = clone(images)["lines"]["return"]
		if err := registration.Validate(nil, images); err != nil {
			t.Fatalf("instructions are not confirmed accumulation: %v", err)
		}
		images["lines"]["return"]["quantity"] = "11"
		if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.reversal_exceeded" {
			t.Fatal(err)
		}
	}
}

func TestReversalBalanceClosedBoundsAndDependencies(t *testing.T) {
	base := reversalDeclaration(t)
	for _, mutate := range []func(*Declaration){
		func(d *Declaration) { d.Decimals[0].Nullable = true },
		func(d *Declaration) { d.Decimals[0].Sign = "" },
		func(d *Declaration) { d.Rules[0].Reversal.OwnerPath = nil },
		func(d *Declaration) { d.Rules[0].Reversal.RequiredStates = []string{"draft"} },
		func(d *Declaration) { d.Rules[0].Reversal.SameFields = []string{"unit", "unit"} },
		func(d *Declaration) { d.Rules[0].Reversal.OriginalPurpose = "salesReturn" },
	} {
		wire, _ := json.Marshal(base)
		var d Declaration
		if err := json.Unmarshal(wire, &d); err != nil {
			t.Fatal(err)
		}
		mutate(&d)
		if d.Validate() == nil {
			t.Fatal("invalid reversal declaration accepted")
		}
	}
	r := Registration{Declaration: base, Bindings: map[string]pluginmodel.Binding{
		"lines": {TableID: "tl", Fields: map[string]string{}}, "documents": {TableID: "td", Fields: map[string]string{}},
	}}
	for entity, fields := range r.dependencies() {
		for field := range fields {
			r.Bindings[entity].Fields[field] = "f_" + field
			if !r.ProtectsBinding(r.Bindings[entity].TableID, "f_"+field) {
				t.Fatal("unprotected dependency", entity, field)
			}
		}
	}
	for _, field := range []string{"phase", "agreement"} {
		if !r.selectedBy(WriteSet{"td": {"f_" + field: true}}) {
			t.Fatal("owner-only change bypasses guard", field)
		}
	}
}
