package transactionrules

import "testing"

func TestSettlementSourceScopeAndClosedConsumption(t *testing.T) {
	rule := newKindRule(t, "settlement")
	rule.Settlement.Capacity.Currency = AuthorityRef{Path: rule.Settlement.Capacity.Direction.Path, Field: "currency"}
	rule.Settlement.Request.Currency = AuthorityRef{Path: rule.Settlement.Request.Direction.Path, Field: "currency"}
	rule.Settlement.Actual.Currency = AuthorityRef{Path: rule.Settlement.Actual.Direction.Path, Field: "currency"}
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, rule)}
	base := settlementImages()
	delete(base["allocations"], "red")
	base["requests"]["r1"]["currency"] = "CNY"
	base["invoices"]["redInv"]["currency"] = "CNY"
	base["invoices"]["blueInv"]["currency"] = "CNY"
	for _, tc := range []struct {
		name   string
		mutate func(Images)
	}{
		{"closed-before", func(i Images) { i["requests"]["r1"]["status"] = "closed" }},
		{"closeRemaining-before", func(i Images) {
			i["adjustments"]["c"] = map[string]any{"kind": "closeRemaining", "source": "s1", "close_remaining": true, "quantity": "10", "net": "100.00", "tax": "0.00", "gross": "100.00"}
		}},
		{"currency", func(i Images) { i["invoices"]["redInv"]["currency"] = "USD" }},
		{"direction", func(i Images) { i["requests"]["r1"]["direction"] = "blue" }},
		{"missing-original", func(i Images) { i["request_sources"]["s1"]["original"] = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := clone(base)
			tc.mutate(before)
			after := clone(before)
			after["allocations"]["red"] = map[string]any{"line": "redLine", "application_source": "s1", "original": "orig", "quantity": "-1", "net": "-1.00", "tax": "0.00", "gross": "-1.00"}
			if err := registration.Validate(before, after); err == nil || err.Error() != "transaction.settlement_invalid" {
				t.Fatalf("invalid source consumption accepted: %v", err)
			}
		})
	}
	// A draft confirmed in this transaction is a valid source, and the source's
	// existing execution remains valid after a later status close.
	before := clone(base)
	before["requests"]["r1"]["status"] = "draft"
	after := clone(base)
	after["allocations"]["red"] = map[string]any{"line": "redLine", "application_source": "s1", "original": "orig", "quantity": "-1", "net": "-10.00", "tax": "0.00", "gross": "-10.00"}
	if err := registration.Validate(before, after); err != nil {
		t.Fatal(err)
	}
	closed := clone(after)
	closed["requests"]["r1"]["status"] = "closed"
	if err := registration.Validate(after, closed); err != nil {
		t.Fatalf("closing destroyed history: %v", err)
	}
}

func TestSettlementClosedAmountSourceRejectsNewZeroQuantityConsumption(t *testing.T) {
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	before := settlementImages()
	delete(before["allocations"], "red")
	before["requests"]["r1"]["purpose"] = "priceDiscount"
	before["requests"]["r1"]["basis"] = "amount"
	before["request_sources"]["s1"]["quantity"] = "0"
	before["adjustments"]["c"] = map[string]any{"kind": "closeRemaining", "source": "s1", "close_remaining": true, "quantity": "0", "net": "100.00", "tax": "0.00", "gross": "100.00"}
	after := clone(before)
	after["actual_lines"]["redLine"]["purpose"] = "priceDiscount"
	after["allocations"]["red"] = map[string]any{"line": "redLine", "application_source": "s1", "original": "orig", "quantity": "0", "net": "-1.00", "tax": "0.00", "gross": "-1.00"}
	if err := registration.Validate(before, after); err == nil || err.Error() != "transaction.settlement_invalid" {
		t.Fatalf("closed amount source consumed: %v", err)
	}
}

func TestSettlementFixedCloseAndFulfillmentScopes(t *testing.T) {
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	for _, tc := range []struct {
		name, want string
		prepare    func(Images)
	}{
		{"fake-close-range", "settlement_invalid", func(i Images) {
			i["adjustments"]["close"] = map[string]any{"kind": "closeRemaining", "source": "s1", "close_remaining": true, "quantity": "4", "net": "0.00", "tax": "0.00", "gross": "0.00"}
		}},
		{"oversized-close-money", "settlement_exceeded", func(i Images) {
			i["adjustments"]["close"] = map[string]any{"kind": "sourceClose", "source": "s1", "close_remaining": false, "quantity": "4", "net": "101.00", "tax": "0.00", "gross": "101.00"}
		}},
		{"wrong-flag-type", "settlement_invalid", func(i Images) {
			i["adjustments"]["close"] = map[string]any{"kind": "sourceClose", "source": "s1", "close_remaining": "true", "quantity": "4", "net": "40.00", "tax": "0.00", "gross": "40.00"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := settlementImages()
			tc.prepare(i)
			if err := registration.Validate(Images{}, i); err == nil || err.Error() != "transaction."+tc.want {
				t.Fatalf("invalid close accepted: %v", err)
			}
		})
	}
	i := settlementImages()
	i["requests"]["r1"]["basis"] = "amount"
	i["requests"]["r1"]["purpose"] = "priceDiscount"
	i["request_sources"]["s1"]["quantity"] = "0"
	i["allocations"]["red"]["quantity"] = "0"
	i["actual_lines"]["redLine"]["purpose"] = "priceDiscount"
	i["allocations"]["red"]["net"] = "-10.00"
	i["allocations"]["red"]["gross"] = "-10.00"
	i["adjustments"]["f"] = map[string]any{"kind": "fulfillAmount", "source": "s1", "allocation": "red", "close_remaining": false, "quantity": "0", "net": "100.00", "tax": "0.00", "gross": "100.00"}
	if err := registration.Validate(nil, i); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("fulfillment exceeds real effect: %v", err)
	}
}

func TestSettlementGrossIsSumOfRoundedNetAndTaxRemainders(t *testing.T) {
	r := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	i := settlementImages()
	i["request_sources"]["s1"]["quantity"] = "3"
	i["request_sources"]["s1"]["net"] = "0.01"
	i["request_sources"]["s1"]["tax"] = "0.01"
	i["request_sources"]["s1"]["gross"] = "0.02"
	i["allocations"]["red"]["quantity"] = "-1"
	i["allocations"]["red"]["net"] = "0.00"
	i["allocations"]["red"]["tax"] = "0.00"
	i["allocations"]["red"]["gross"] = "0.00"
	i["allocations"]["orig"]["net"] = "0.01"
	i["allocations"]["orig"]["tax"] = "0.01"
	i["allocations"]["orig"]["gross"] = "0.02"
	if err := r.Validate(nil, i); err != nil {
		t.Fatal(err)
	}
	// Isolate the gross dimension: both rounded remainders are one minor unit.
	i["allocations"]["orig"]["gross"] = "0.01"
	if err := r.Validate(nil, i); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("gross independently rounded down: %v", err)
	}
}

func TestSettlementMaterialNullAndThreeWayConsistency(t *testing.T) {
	rule := newKindRule(t, "settlement")
	rule.Settlement.Capacity.Material = AuthorityRef{Path: rule.Settlement.Capacity.Direction.Path[:1], Field: "material"}
	rule.Settlement.Request.Material = AuthorityRef{Path: rule.Settlement.Request.Direction.Path[:1], Field: "material"}
	rule.Settlement.Actual.Material = AuthorityRef{Path: rule.Settlement.Actual.Direction.Path[:1], Field: "material"}
	r := Registration{Declaration: invoiceGuardsDeclaration(t, rule)}
	for _, scenario := range []string{"all-known", "raw-null", "capacity-null", "known-conflict", "null-capacity-conflict", "missing", "wrong-type", "dangling-path"} {
		t.Run(scenario, func(t *testing.T) {
			i := settlementImages()
			i["allocations"]["red"]["net"] = "-60.00"
			i["allocations"]["red"]["gross"] = "-60.00"
			i["actual_lines"]["blueLine"]["material"] = "m1"
			i["actual_lines"]["redLine"]["material"] = "m1"
			i["request_lines"]["rl1"]["material"] = "m1"
			valid := true
			switch scenario {
			case "raw-null":
				i["actual_lines"]["redLine"]["material"] = nil
			case "capacity-null":
				i["actual_lines"]["blueLine"]["material"] = nil
			case "known-conflict":
				i["actual_lines"]["redLine"]["material"] = "m2"
				valid = false
			case "null-capacity-conflict":
				i["actual_lines"]["blueLine"]["material"] = nil
				i["actual_lines"]["redLine"]["material"] = "m2"
				valid = false
			case "missing":
				delete(i["actual_lines"]["redLine"], "material")
				valid = false
			case "wrong-type":
				i["actual_lines"]["redLine"]["material"] = 42
				valid = false
			case "dangling-path":
				i["allocations"]["red"]["line"] = "missing"
				valid = false
			}
			err := r.Validate(nil, i)
			if valid && err != nil || !valid && (err == nil || err.Error() != "transaction.settlement_invalid") {
				t.Fatalf("valid=%v got %v", valid, err)
			}
		})
	}
}

func TestSettlementRequestPurposeAndBasisShareZeroQuantitySemantics(t *testing.T) {
	r := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	for _, tc := range []struct{ purpose, basis, quantity string }{{"priceDiscount", "quantity", "10"}, {"physicalReturn", "amount", "0"}} {
		i := settlementImages()
		delete(i["allocations"], "red")
		i["requests"]["r1"]["purpose"] = tc.purpose
		i["requests"]["r1"]["basis"] = tc.basis
		i["request_sources"]["s1"]["quantity"] = tc.quantity
		if err := r.Validate(nil, i); err == nil || err.Error() != "transaction.settlement_invalid" {
			t.Fatalf("formal mismatched purpose/basis accepted: %v", err)
		}
		i["requests"]["r1"]["status"] = "draft"
		i["requests"]["r1"]["basis"] = nil
		if err := r.Validate(nil, i); err != nil {
			t.Fatalf("draft completeness was forced: %v", err)
		}
	}
}
