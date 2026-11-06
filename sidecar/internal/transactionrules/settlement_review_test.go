package transactionrules

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
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

func TestSettlementReviewActualKeepsSourcePlanBucket(t *testing.T) {
	rule := newKindRule(t, "settlement")
	s := rule.Settlement
	s.Capacity = SettlementCapacity{Entity: "obligations", Quantity: "quantity"}
	s.Request.Capacity = "obligation"
	s.Request.PlanItem = "plan_item"
	s.Request.Directions = []string{"blue"}
	s.Request.Purpose = AuthorityRef{}
	s.Request.Purposes = nil
	s.Actual.Capacity = "obligation"
	s.Actual.PlanItem = "plan_item"
	s.Actual.Directions = []string{"blue"}
	s.Actual.Purpose = AuthorityRef{}
	s.Actual.Purposes = nil
	s.Actual.ZeroQuantityPurposes = nil
	s.Capacity.Net, s.Capacity.Tax, s.Capacity.Gross = "", "", ""
	s.Request.Net, s.Request.Tax, s.Request.Gross = "", "", ""
	s.Actual.Net, s.Actual.Tax, s.Actual.Gross = "", "", ""
	s.Adjustment.CloseNet, s.Adjustment.CloseTax, s.Adjustment.CloseGross = "", "", ""
	s.Plans = reservationPlanRule().Reservation.Plans
	d := Declaration{Contract: Contract, ModelID: "m", Version: 1, Decimals: invoiceGuardsDecimals(), Rules: []Rule{rule}}
	for _, entity := range []string{"obligations", "plan_items"} {
		d.Decimals = append(d.Decimals, Decimal{Entity: entity, Field: "quantity", Format: exactdecimal.Format{Storage: "decimalText", Digits: 38}, Sign: "nonnegative"})
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	r := Registration{Declaration: d}
	images := settlementImages()
	delete(images["allocations"], "red")
	images["allocations"]["orig"]["quantity"] = "8"
	images["allocations"]["orig"]["application_source"] = "s1"
	images["allocations"]["orig"]["obligation"] = "o1"
	images["allocations"]["orig"]["plan_item"] = "p1"
	images["requests"]["r1"]["direction"] = "blue"
	images["request_sources"]["s1"]["quantity"] = "8"
	images["request_sources"]["s1"]["obligation"] = "o1"
	images["request_sources"]["s1"]["plan_item"] = "p1"
	for _, entity := range []string{"series", "plan_versions", "logical_items", "plan_items"} {
		images[entity] = reservationPlanImages()[entity]
	}
	images["obligations"] = map[string]map[string]any{"o1": {"quantity": "16"}}
	images["plan_items"]["p1"]["quantity"] = "8"
	for _, tc := range []struct {
		name, logical string
		share         float64
		source        any
		want          bool
	}{
		{"explicit same bucket", "L1", 1, "p1", true},
		{"different logical", "L2", 1, "p1", false},
		{"different share", "L1", 2, "p1", false},
		{"direct source cannot move to plan", "L1", 1, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := clone(images)
			version := "v2"
			if tc.logical != "L1" || tc.share != 1 {
				version = "v1"
			}
			i["plan_items"]["old"] = map[string]any{"plan_version": version, "logical_item": tc.logical, "share": tc.share, "scope": "o1", "quantity": "8"}
			i["allocations"]["orig"]["plan_item"] = "old"
			i["request_sources"]["s1"]["plan_item"] = tc.source
			err := r.Validate(nil, i)
			if tc.want && err != nil || !tc.want && (err == nil || err.Error() != "transaction.settlement_invalid") {
				t.Fatalf("want valid=%v got %v", tc.want, err)
			}
		})
	}
	images["allocations"]["orig"]["plan_item"] = nil
	if err := r.Validate(nil, images); err != nil {
		t.Fatalf("source plan inheritance: %v", err)
	}
}

func TestSettlementReviewAdmissionPreservesClosedHistory(t *testing.T) {
	r := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	closed := settlementImages()
	closed["requests"]["r1"]["status"] = "closed"
	closed["adjustments"]["close"] = map[string]any{"kind": "closeRemaining", "source": "s1", "close_remaining": true, "quantity": "4", "net": "40.00", "tax": "0.00", "gross": "40.00"}
	if err := r.Validate(closed, closed); err != nil {
		t.Fatalf("unchanged history: %v", err)
	}
	if err := r.Validate(nil, closed); err != nil {
		t.Fatalf("admission treated historical execution as new: %v", err)
	}
	for _, state := range []any{"draft", "", nil} {
		invalid := clone(closed)
		invalid["requests"]["r1"]["status"] = state
		if err := r.Validate(nil, invalid); err == nil {
			t.Fatalf("admission accepted source state %v", state)
		}
	}
	if err := r.Validate(Images{}, closed); err == nil || err.Error() != "transaction.settlement_invalid" {
		t.Fatalf("real initial write to closed source: %v", err)
	}
	increased := clone(closed)
	increased["allocations"]["red"]["net"] = "-91.00"
	increased["allocations"]["red"]["gross"] = "-91.00"
	if err := r.Validate(closed, increased); err == nil || err.Error() != "transaction.settlement_invalid" {
		t.Fatalf("increased closed consumption: %v", err)
	}
	corrupt := clone(closed)
	corrupt["allocations"]["orig"]["net"] = "80.00"
	corrupt["allocations"]["orig"]["gross"] = "80.00"
	if err := r.Validate(nil, corrupt); err == nil {
		t.Fatal("admission skipped capacity")
	}
	corrupt = clone(closed)
	corrupt["allocations"]["red"]["original"] = "missing"
	if err := r.Validate(nil, corrupt); err == nil {
		t.Fatal("admission skipped source/capacity relation")
	}
}

func TestSettlementReviewAdmissionKeepsFixedCloseAfterCorrection(t *testing.T) {
	r := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	closed := settlementImages()
	closed["requests"]["r1"]["status"] = "closed"
	closed["adjustments"]["close"] = map[string]any{"kind": "closeRemaining", "source": "s1", "close_remaining": true, "quantity": "4", "net": "40.00", "tax": "0.00", "gross": "40.00"}
	corrected := clone(closed)
	corrected["allocations"]["red"]["quantity"] = "-5"
	corrected["allocations"]["red"]["net"] = "-80.00"
	corrected["allocations"]["red"]["gross"] = "-80.00"
	if err := r.Validate(closed, corrected); err != nil {
		t.Fatalf("decreased historical allocation: %v", err)
	}
	if err := r.Validate(nil, corrected); err != nil {
		t.Fatalf("admission recalculated immutable close: %v", err)
	}
	beforeClose := clone(corrected)
	beforeClose["requests"]["r1"]["status"] = "open"
	beforeClose["allocations"]["red"]["net"] = "-50.00"
	beforeClose["allocations"]["red"]["gross"] = "-50.00"
	delete(beforeClose["adjustments"], "close")
	if err := r.Validate(nil, beforeClose); err != nil {
		t.Fatalf("valid graph before new close: %v", err)
	}
	if err := r.Validate(beforeClose, corrected); err == nil {
		t.Fatal("new close accepted stale remaining range")
	}
	exact := clone(corrected)
	exact["adjustments"]["close"]["quantity"] = "5"
	exact["adjustments"]["close"]["net"] = "50.00"
	exact["adjustments"]["close"]["gross"] = "50.00"
	if err := r.Validate(beforeClose, exact); err != nil {
		t.Fatalf("new exact close: %v", err)
	}
}
