package transactionrules

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSettlementReviewSequentialQuantityClosures(t *testing.T) {
	r := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	for _, tc := range []struct{ q, n, tax, g, first, last, rn, rt, rg string }{{"10", "100.00", "0.00", "100.00", "4", "6", "60.00", "0.00", "60.00"}, {"3", "0.01", "0.01", "0.02", "1", "2", "0.01", "0.01", "0.02"}, {"3", "0.01", "0.01", "0.02", "2", "1", "0.00", "0.00", "0.00"}} {
		t.Run(tc.q+"-"+tc.first, func(t *testing.T) {
			before := settlementImages()
			delete(before["allocations"], "red")
			before["allocations"]["orig"]["tax"] = "100.00"
			before["allocations"]["orig"]["gross"] = "200.00"
			source := before["request_sources"]["s1"]
			source["quantity"] = tc.q
			source["net"] = tc.n
			source["tax"] = tc.tax
			source["gross"] = tc.g
			before["adjustments"]["partial"] = map[string]any{"kind": "sourceClose", "source": "s1", "close_remaining": false, "quantity": tc.first, "net": nil, "tax": nil, "gross": nil}
			if err := r.Validate(nil, before); err != nil {
				t.Fatal(err)
			}
			after := clone(before)
			after["adjustments"]["remaining"] = map[string]any{"kind": "closeRemaining", "source": "s1", "close_remaining": true, "quantity": tc.last, "net": tc.rn, "tax": tc.rt, "gross": tc.rg}
			if tc.q == "10" {
				bad := clone(after)
				bad["adjustments"]["remaining"]["net"] = "100.00"
				bad["adjustments"]["remaining"]["gross"] = "100.00"
				if err := r.Validate(before, bad); err == nil {
					t.Fatal("final capture ignored the earlier quantity closure")
				}
			}
			if err := r.Validate(before, after); err != nil {
				t.Fatalf("fixed remaining after a quantity-only close rejected: %v", err)
			}
			if err := r.Validate(nil, after); err != nil {
				t.Fatalf("same-transaction closure rejected: %v", err)
			}
		})
	}
}

func TestSettlementReviewOriginalMustBeSettled(t *testing.T) {
	r := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	before := settlementImages()
	delete(before["allocations"], "red")
	before["invoices"]["blueInv"]["status"] = "draft"
	if err := r.Validate(nil, before); err == nil {
		t.Fatal("draft blue allocation admitted as original capacity")
	}
	after := clone(before)
	after["invoices"]["blueInv"]["status"] = "issued"
	if err := r.Validate(before, after); err != nil {
		t.Fatalf("same-transaction confirmation rejected: %v", err)
	}
	if err := r.Validate(after, before); err == nil {
		t.Fatal("existing red reservation allowed original to become draft")
	}
}

func TestSettlementReviewAmountOnlyStillChecksPurpose(t *testing.T) {
	rule := newKindRule(t, "settlement")
	rule.Settlement.Capacity.Quantity = ""
	rule.Settlement.Request.Quantity = ""
	rule.Settlement.Actual.Quantity = ""
	rule.Settlement.Adjustment.CloseQuantity = ""
	r := Registration{Declaration: invoiceGuardsDeclaration(t, rule)}
	for _, purpose := range []any{nil, "unknown"} {
		i := settlementImages()
		i["request_sources"] = map[string]map[string]any{}
		i["allocations"]["red"]["application_source"] = nil
		i["actual_lines"]["redLine"]["purpose"] = purpose
		if err := r.Validate(nil, i); err == nil {
			t.Fatalf("amount-only actual accepted purpose=%v", purpose)
		}
	}
}

func TestSettlementReviewPlansCannotSilentlyDropMoney(t *testing.T) {
	rule := newKindRule(t, "settlement")
	rule.Settlement.Request.PlanItem = "plan_item"
	rule.Settlement.Plans = &ReservationPlans{Entity: "plans", Quantity: "quantity", Scope: "scope", LogicalItem: ReservationLogical{Field: "logical", Target: "logical_items"}, Share: "share", CurrentWhen: PathMatch{Left: []RelationStep{{Field: "version", Target: "versions"}}, Right: []RelationStep{{Field: "current", Target: "versions"}}}}
	if rule.Settlement.valid() {
		t.Fatal("plans plus money accepted although only quantity is enforced")
	}
}

func TestSettlementReviewOptionalAuthorityRefsRoundTripAndRejectEmpty(t *testing.T) {
	rule := newKindRule(t, "settlement")
	raw, err := json.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip Rule
	if err = json.Unmarshal(raw, &roundtrip); err != nil {
		t.Fatalf("omitted refs did not round-trip: %v", err)
	}
	var object map[string]any
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	spec := object["settlement"].(map[string]any)
	for _, entry := range []string{"capacity", "request", "actual"} {
		for _, field := range []string{"currency", "unit", "material"} {
			role := spec[entry].(map[string]any)
			if _, present := role[field]; present {
				t.Errorf("omitted %s.%s was serialized as a placeholder", entry, field)
			}
			role[field] = map[string]any{}
			invalid, _ := json.Marshal(object)
			var r Rule
			if json.Unmarshal(invalid, &r) == nil {
				t.Errorf("accepted empty %s.%s authority", entry, field)
			}
			delete(role, field)
		}
	}
	for _, field := range []string{"purpose", "basis"} {
		role := spec["request"].(map[string]any)
		old := role[field]
		role[field] = map[string]any{}
		invalid, _ := json.Marshal(object)
		var r Rule
		if json.Unmarshal(invalid, &r) == nil {
			t.Errorf("accepted empty request.%s", field)
		}
		role[field] = old
	}
	rule.Settlement.Capacity.Directions = nil
	for n := 0; n < 33; n++ {
		rule.Settlement.Capacity.Directions = append(rule.Settlement.Capacity.Directions, strings.Repeat("x", n+1))
	}
	if rule.Settlement.valid() {
		t.Fatal("33 capacity directions accepted")
	}
}
