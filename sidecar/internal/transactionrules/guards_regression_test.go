package transactionrules

import (
	"encoding/json"
	"os"
	"testing"
)

func TestDeliveryGuardNullableMissingKey(t *testing.T) {
	registration := Registration{Declaration: Declaration{Decimals: []Decimal{{Entity: "prices", Field: "value", Nullable: true, Format: freezeFormat}}}}
	if err := registration.Validate(nil, Images{"prices": {"p": {}}}); err == nil {
		t.Fatal("missing nullable decimal accepted as explicit null")
	}
}

func TestDeliveryGuardNullableDeclarationBoolean(t *testing.T) {
	for _, flag := range []string{"true", "false", "null", "0", `"false"`} {
		raw := []byte(`{"contract":"vibetable.plugin-transactions.v1","modelId":"m","version":1,"decimals":[{"entity":"e","field":"price","storage":"decimalText","scale":2,"digits":8,"nullable":` + flag + `}],"rules":[{"id":"u","kind":"unique","entity":"e","fields":["code"]}]}`)
		_, err := Decode(raw)
		if (err == nil) != (flag == "true" || flag == "false") {
			t.Errorf("nullable %s: %v", flag, err)
		}
	}
}

func TestDeliveryGuardFreezeEveryPathIdentity(t *testing.T) {
	for _, intermediate := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct-child", true: "intermediate-ancestor"}[intermediate], func(t *testing.T) {
			rule := parentFreezeRule()
			before := freezeImages()
			before["lines"]["l3"] = map[string]any{"document": "d1"}
			if intermediate {
				before["owners"] = map[string]map[string]any{"a1": {"owner_line": "l1"}}
				before["sources"]["c1"] = map[string]any{"owner": "a1", "quantity": "5", "scope": "o1"}
				rule.ParentPath = append([]RelationStep{{Field: "owner", Target: "owners"}}, rule.ParentPath...)
			}
			after := clone(before)
			if intermediate {
				after["owners"]["a1"]["owner_line"] = "l3"
			} else {
				after["sources"]["c1"]["owner_line"] = "l3"
			}
			if err := rule.validateParentFreeze(before, after); err == nil || err.Error() != "transaction.parent_frozen" {
				t.Fatalf("same-endpoint path rebinding: %v", err)
			}
		})
	}
}

func TestDeliveryGuardPlanScope(t *testing.T) {
	registration := Registration{Declaration: planDeclaration()}
	for _, role := range []string{"instruction", "consumer", "transfer"} {
		t.Run(role, func(t *testing.T) {
			images := reservationPlanImages()
			images["obligations"]["o2"] = map[string]any{"quantity": "0"}
			if role != "instruction" {
				delete(images["sources"], "h")
			} else {
				delete(images["sources"], "a")
			}
			for _, row := range images["sources"] {
				if row["plan_item"] == "p1" {
					row["scope"] = "o2"
				}
			}
			if role == "transfer" {
				delete(images["sources"], "a")
				images["documents"]["di"]["phase"] = "instruction_transferred"
				images["documents"]["di2"] = map[string]any{"phase": "instruction_open"}
				images["lines"]["li2"] = map[string]any{"document": "di2", "purpose": "normal"}
				images["sources"]["h"] = map[string]any{"owner_line": "li", "scope": "o2", "quantity": "2", "plan_item": "p1"}
				images["sources"]["t"] = map[string]any{"owner_line": "li2", "scope": "o2", "quantity": "2", "plan_item": "p1", "transferred_from": "h"}
			}
			if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.reservation_invalid" {
				t.Fatalf("plan scope mismatch: %v", err)
			}
		})
	}
}

func TestDeliveryGuardHistoricalPlanIdentity(t *testing.T) {
	registration := Registration{Declaration: planDeclaration()}
	for _, field := range []string{"logical_item", "share", "scope"} {
		for _, upgrade := range []bool{false, true} {
			t.Run(field+map[bool]string{false: "-same-version", true: "-upgrade"}[upgrade], func(t *testing.T) {
				before := reservationPlanImages()
				before["obligations"]["o2"] = map[string]any{"quantity": "10"}
				after := clone(before)
				after["plan_items"]["p1"][field] = map[string]any{"logical_item": "L2", "share": float64(2), "scope": "o2"}[field]
				if upgrade {
					after["series"]["s1"]["current_version"] = "v2"
					after["plan_items"]["p2"] = map[string]any{}
					for key, value := range after["plan_items"]["p1"] {
						after["plan_items"]["p2"][key] = value
					}
					after["plan_items"]["p2"]["plan_version"] = "v2"
				}
				if err := registration.Validate(before, after); err == nil {
					t.Fatal("executed historical plan identity was rewritten")
				}
			})
		}
	}
}

func TestDeliveryGuardNewTransferMustOpen(t *testing.T) {
	registration := Registration{Declaration: reservationDeclaration(reservationRule())}
	before := reservationImages()
	after := clone(before)
	after["documents"]["di"]["phase"] = "instruction_transferred"
	after["documents"]["di2"] = map[string]any{"phase": "instruction_closed"}
	after["lines"]["li2"] = map[string]any{"document": "di2", "purpose": "normal"}
	after["sources"]["t"] = map[string]any{"owner_line": "li2", "scope": "o1", "quantity": "2", "transferred_from": "h"}
	if err := registration.Validate(before, after); err == nil || err.Error() != "transaction.reservation_transfer_invalid" {
		t.Fatalf("new closed transfer carrier: %v", err)
	}
}

func TestDeliveryGuardClosedShapeCorpus(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/plugin-transaction-kind-fields.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Legacy []map[string]any `json:"legacy"`
		Parent []map[string]any `json:"parentFreeze"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	base := planDeclaration()
	for _, rule := range []Rule{
		{ID: "u", Kind: "unique", Entity: "sources", Fields: []string{"code"}},
		{ID: "u", Kind: "atMostOne", Entity: "sources", Fields: []string{"a", "b"}},
		{ID: "u", Kind: "reference", Entity: "sources", Field: "scope", Target: "obligations"},
		{ID: "u", Kind: "conservation", Entity: "sources", Relation: "scope", Amount: "quantity", Target: "obligations", Limit: "quantity"},
		{ID: "u", Kind: "balance", Entity: "sources", Relation: "scope", Amount: "quantity", Target: "obligations", Limit: "quantity"},
		{ID: "u", Kind: "state", Entity: "sources", Field: "phase", Transitions: map[string][]string{"draft": {"open"}}},
		{ID: "u", Kind: "sameTarget", Entity: "sources", PathMatch: PathMatch{Left: []RelationStep{{Field: "scope", Target: "obligations"}}, Right: []RelationStep{{Field: "scope", Target: "obligations"}}}},
		parentFreezeRule(),
		reservationPlanRule(),
	} {
		base.Rules = []Rule{rule}
		encoded, _ := json.Marshal(base)
		if _, err := Decode(encoded); err != nil {
			t.Fatalf("valid %s: %v", rule.Kind, err)
		}
		patches := corpus.Legacy
		if rule.Kind == "parentFreeze" {
			patches = corpus.Parent
		}
		for i, patch := range patches {
			t.Run(rule.Kind+"/"+string(rune('a'+i)), func(t *testing.T) {
				var wire map[string]any
				if err := json.Unmarshal(encoded, &wire); err != nil {
					t.Fatal(err)
				}
				for key, value := range patch {
					wire["rules"].([]any)[0].(map[string]any)[key] = value
				}
				invalid, _ := json.Marshal(wire)
				if _, err := Decode(invalid); err == nil {
					t.Fatalf("foreign fields accepted: %s", invalid)
				}
			})
		}
	}
}
