package transactionrules

import (
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
)

func TestRulesClosedDeclarationAndStateProtection(t *testing.T) {
	if _, err := Decode([]byte(`{"contract":"vibetable.plugin-transactions.v1","modelId":"m","version":1,"decimals":[],"rules":[{"id":"u","kind":"unique","entity":"e","fields":["code"],"omitOnImport":true}]}`)); err == nil {
		t.Fatal("unknown bypass field accepted")
	}
	r := Registration{Declaration: Declaration{Contract: Contract, ModelID: "m", Version: 1, Rules: []Rule{{ID: "state", Kind: "state", Entity: "e", Field: "status", Fields: []string{"amount"}, Transitions: map[string][]string{"draft": {"posted"}, "posted": {}}, FrozenStates: []string{"posted"}}}}}
	before := Images{"e": {"one": {"status": "posted", "amount": "10.00"}}}
	if err := r.Validate(before, Images{"e": {"one": {"status": "posted", "amount": "9.00"}}}); err == nil {
		t.Fatal("frozen amount edited")
	}
	if err := r.Validate(before, Images{"e": {}}); err == nil {
		t.Fatal("frozen row deleted")
	}
	if err := r.Validate(before, Images{"e": {"one": {"status": "draft", "amount": "10.00"}}}); err == nil {
		t.Fatal("reverse transition accepted")
	}
	if err := r.Validate(before, before); err != nil {
		t.Fatal(err)
	}
}

func TestRulesExactConservationNullNegativeAndOverflow(t *testing.T) {
	f := exactdecimal.Format{Storage: "decimalText", Scale: 2, Digits: 4}
	r := Registration{Declaration: Declaration{Decimals: []Decimal{{Entity: "lines", Field: "amount", Format: f}, {Entity: "headers", Field: "amount", Format: f}}, Rules: []Rule{{ID: "total", Kind: "conservation", Entity: "lines", Relation: "header", Amount: "amount", Target: "headers", Limit: "amount"}}}}
	images := Images{"headers": {"h": {"amount": "0.10"}}, "lines": {"a": {"header": "h", "amount": "0.30"}, "b": {"header": "h", "amount": "-0.20"}}}
	if err := r.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	images["lines"]["b"]["amount"] = nil
	if err := r.Validate(nil, images); err == nil {
		t.Fatal("null treated as zero")
	}
	images["lines"]["a"]["amount"] = "99.99"
	images["lines"]["b"]["amount"] = "0.01"
	if err := r.Validate(nil, images); err == nil {
		t.Fatal("sum overflow accepted")
	}
}
