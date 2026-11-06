package transactionrules

import (
	"encoding/json"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
)

func TestSettlementBalanceDeclaration(t *testing.T) {
	declaration := invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))
	for _, mutate := range []func(*Declaration){
		func(d *Declaration) { d.Rules[0].Kind = "allocationBalance" },
		func(d *Declaration) { d.Rules[0].Settlement.Capacity.Entity = "" },
		func(d *Declaration) { d.Rules[0].Settlement.Request.Directions = []string{} },
		func(d *Declaration) { d.Rules[0].Settlement.Actual.Directions = []string{} },
		func(d *Declaration) { d.Rules[0].Settlement.Request.Direction.Field = "" },
		func(d *Declaration) {
			d.Rules[0].Settlement.Actual.Purposes = []string{"priceDiscount", "priceDiscount"}
		},
		func(d *Declaration) { d.Rules[0].Settlement.Request.Purpose.Field = "" },
		func(d *Declaration) { d.Rules[0].Settlement.Request.OpenStates = []string{"open", "open"} },
		func(d *Declaration) { d.Rules[0].Settlement.Request.DraftStates = []string{"open"} },
		func(d *Declaration) { d.Rules[0].Settlement.Actual.DraftStates = []string{"issued"} },
		func(d *Declaration) { d.Rules[0].Settlement.Actual.SettledStates = []string{} },
		func(d *Declaration) { d.Rules[0].Settlement.Actual.Quantity = "" },
		func(d *Declaration) { d.Rules[0].Settlement.Capacity.Gross = "" },
		func(d *Declaration) { d.Rules[0].Settlement.Request.Gross = "" },
		func(d *Declaration) { d.Rules[0].Settlement.Actual.Gross = "" },
		func(d *Declaration) { d.Rules[0].Settlement.Adjustment.Target = "" },
		func(d *Declaration) { d.Rules[0].Settlement.Adjustment.CloseRemaining = "" },
		func(d *Declaration) { d.Rules[0].Settlement.Adjustment.CloseQuantity = "" },
		func(d *Declaration) { d.Rules[0].Fields = []string{"quantity"} },
		func(d *Declaration) { d.Rules[0].Settlement = nil },
		func(d *Declaration) { d.Decimals = []Decimal{d.Decimals[0]} },
		func(d *Declaration) {
			d.Decimals[12] = Decimal{Entity: "request_sources", Field: "quantity", Format: exactdecimal.Format{Storage: "decimalText", Scale: 1, Digits: 38}, Sign: "nonnegative", Nullable: true}
		},
		func(d *Declaration) { d.Decimals[12].Field = "missing" },
		func(d *Declaration) { d.Decimals[12].Sign = "" },
		func(d *Declaration) {
			d.Decimals[13] = Decimal{Entity: "request_sources", Field: "net", Format: exactdecimal.Format{Storage: "decimalText", Scale: 3, Digits: 38}}
		},
		func(d *Declaration) { d.Decimals[17].Sign = ""; d.Decimals[17].Nullable = true },
	} {
		wire, _ := json.Marshal(declaration)
		var d Declaration
		if err := json.Unmarshal(wire, &d); err != nil {
			t.Fatal(err)
		}
		mutate(&d)
		if d.Validate() == nil {
			t.Fatal("invalid settlement declaration accepted")
		}
	}
	// Amount-type buckets drop the quantity measure everywhere at once.
	amount := cloneRule(declaration.Rules[0])
	amount.Settlement.Capacity.Quantity = ""
	amount.Settlement.Request.Quantity = ""
	amount.Settlement.Actual.Quantity = ""
	amount.Settlement.Adjustment.CloseQuantity = ""
	if err := invoiceGuardsDeclaration(t, amount).Validate(); err != nil {
		t.Fatal(err)
	}
}

// settlementImages reproduces the authority contract example on the natural
// seven-entity shape: the original blue allocation (same invoice_allocations
// entity, same four signed columns) is the capacity; the open red request
// source 10/100 and the settled red allocation with the one-shot signed
// quantity -6 and net -90 share it, whose remaining reservation of 40 pushes
// the budget to 130.
func settlementImages() Images {
	return Images{
		"requests":        {"r1": {"status": "open", "direction": "red", "purpose": "physicalReturn", "basis": "quantity"}},
		"request_lines":   {"rl1": {"request": "r1"}},
		"request_sources": {"s1": {"line": "rl1", "original": "orig", "quantity": "10", "net": "100.00", "tax": "0.00", "gross": "100.00"}},
		"invoices":        {"blueInv": {"status": "issued", "direction": "blue", "amount_convention": "signed"}, "redInv": {"status": "issued", "direction": "red", "amount_convention": "magnitude"}},
		"actual_lines": {
			"blueLine": {"invoice": "blueInv", "purpose": nil, "quantity": "10", "net": "100.00", "tax": "0.00", "gross": "100.00"},
			"redLine":  {"invoice": "redInv", "purpose": "physicalReturn", "quantity": "6", "net": "90.00", "tax": "0.00", "gross": "90.00"},
		},
		"allocations": {
			"orig": {"line": "blueLine", "quantity": "10", "net": "100.00", "tax": "0.00", "gross": "100.00"},
			"red":  {"line": "redLine", "application_source": "s1", "original": "orig", "quantity": "-6", "net": "-90.00", "tax": "0.00", "gross": "-90.00"},
		},
		"adjustments": {},
	}
}
func TestSettlementBalanceRedBudgetAndRawFacts(t *testing.T) {
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	// 90 executed + 40 reserved exceeds the original 100: the internal
	// allocation is rejected...
	if err := registration.Validate(nil, settlementImages()); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("want settlement_exceeded got %v", err)
	}
	// ...while the same real invoice face without any internal allocation row
	// stays a registrable fact.
	images := settlementImages()
	delete(images["allocations"], "red")
	if err := registration.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	// An explicit closeRemaining disposal releases only the unexecuted part.
	images = settlementImages()
	images["adjustments"]["a1"] = map[string]any{"kind": "sourceClose", "source": "s1", "close_remaining": true, "quantity": "4", "net": "40.00", "tax": "0.00", "gross": "40.00"}
	if err := registration.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	// Shrinking the original below the executed magnitude is rejected and
	// keeps the whole graph unchanged for the caller.
	images["allocations"]["orig"]["net"] = "89.00"
	images["allocations"]["orig"]["gross"] = "89.00"
	if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("shrunk blueprint accepted: %v", err)
	}
}

func TestSettlementBalanceRequestConsumption(t *testing.T) {
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	// E+C may never exceed Q, with or without an explicit close.
	images := settlementImages()
	images["allocations"]["red"]["quantity"] = "-11"
	if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("over-consumed request accepted: %v", err)
	}
	images["adjustments"]["a1"] = map[string]any{"kind": "sourceClose", "source": "s1", "close_remaining": false, "quantity": "5", "net": nil, "tax": nil, "gross": nil}
	if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("close beyond execution accepted: %v", err)
	}
	// priceDiscount keeps a zero quantity effect and never divides by zero.
	images = settlementImages()
	images["requests"]["r1"]["purpose"] = "priceDiscount"
	images["requests"]["r1"]["basis"] = "amount"
	images["request_sources"]["s1"]["quantity"] = "0"
	images["actual_lines"]["redLine"]["purpose"] = "priceDiscount"
	images["allocations"]["red"]["quantity"] = "0"
	images["allocations"]["red"]["net"] = "0.00"
	images["allocations"]["red"]["gross"] = "0.00"
	if err := registration.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	images["allocations"]["red"]["quantity"] = "-1"
	if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.settlement_invalid" {
		t.Fatalf("non-zero discount effect accepted: %v", err)
	}
	// Ungoverned rows keep registering freely: blue rows are another bucket.
	images = settlementImages()
	images["invoices"]["redInv"]["direction"] = "blue"
	images["invoices"]["redInv"]["status"] = "draft"
	if err := registration.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	// Draft requests stay fully unknown and reserve nothing.
	images = settlementImages()
	images["requests"]["r1"]["status"] = "draft"
	images["request_sources"]["s1"]["net"] = nil
	images["request_sources"]["s1"]["tax"] = nil
	images["request_sources"]["s1"]["gross"] = nil
	if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.settlement_invalid" {
		t.Fatalf("draft source accepted actual consumption: %v", err)
	}
	delete(images["allocations"], "red")
	if err := registration.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	// A non-positive quantity request can never feed the proportional split.
	images = settlementImages()
	images["request_sources"]["s1"]["quantity"] = "0"
	if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.settlement_invalid" {
		t.Fatalf("zero quantity request accepted: %v", err)
	}
}

func TestSettlementBalanceProportionalRounding(t *testing.T) {
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	// Q=3, N=1.00, E=1: remaining net is exactly 1.00 - roundHalfAway(0.33..) = 0.67,
	// so the consumed budget is 0.33 + 0.67 = 1.00 — pinned by the capacity.
	images := settlementImages()
	images["request_sources"]["s1"]["quantity"] = "3"
	images["request_sources"]["s1"]["net"] = "1.00"
	images["request_sources"]["s1"]["gross"] = "1.00"
	images["allocations"]["red"]["quantity"] = "-1"
	images["allocations"]["red"]["net"] = "-0.33"
	images["allocations"]["red"]["gross"] = "-0.33"
	images["allocations"]["orig"]["net"] = "1.00"
	images["allocations"]["orig"]["gross"] = "1.00"
	if err := registration.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	images["allocations"]["orig"]["net"] = "0.99"
	images["allocations"]["orig"]["gross"] = "0.99"
	if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("rounded reservation under-reserved: %v", err)
	}
	// Exact halves round away from zero: remaining of 0.5 minor is 1 minor
	// only with round-half-away, so a 0.01 capacity must still hold.
	images = settlementImages()
	images["request_sources"]["s1"]["quantity"] = "2"
	images["request_sources"]["s1"]["net"] = "0.01"
	images["request_sources"]["s1"]["gross"] = "0.01"
	images["allocations"]["red"]["quantity"] = "-1"
	images["allocations"]["red"]["net"] = "-0.01"
	images["allocations"]["red"]["gross"] = "-0.01"
	images["allocations"]["orig"]["net"] = "0.01"
	images["allocations"]["orig"]["gross"] = "0.01"
	if err := registration.Validate(nil, images); err != nil {
		t.Fatalf("half-away rounding lost the cent: %v", err)
	}
}

func TestSettlementBalanceAmountTypeClosesOnlyThroughEvents(t *testing.T) {
	rule := newKindRule(t, "settlement")
	rule.Settlement.Capacity.Quantity = ""
	rule.Settlement.Request.Quantity = ""
	rule.Settlement.Actual.Quantity = ""
	rule.Settlement.Adjustment.CloseQuantity = ""
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, rule)}
	images := settlementImages()
	images["request_sources"]["s1"]["quantity"] = nil
	images["allocations"]["red"]["quantity"] = nil
	delete(images["allocations"], "red")
	delete(images["adjustments"], "a1")
	delete(images, "adjustments")
	images["adjustments"] = map[string]map[string]any{}
	if err := registration.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	// Amount-type reservations only release through explicit amounts or a
	// closeRemaining event; nothing is released implicitly.
	images["allocations"]["orig"]["net"] = "99.99"
	images["allocations"]["orig"]["gross"] = "99.99"
	if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("amount reservation released implicitly: %v", err)
	}
	images["adjustments"]["a1"] = map[string]any{"kind": "sourceClose", "source": "s1", "close_remaining": true, "quantity": "0", "net": "100.00", "tax": "0.00", "gross": "100.00"}
	if err := registration.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
}

func TestInvoiceGuardsAllocationAndSettlementShareSignedFields(t *testing.T) {
	// Natural seven-entity shape: the same invoice_allocations entity serves
	// as the original blue capacity (orig) and the red internal allocations
	// with one-shot signed effects; allocationBalance guards them against the
	// actual lines while settlementBalance guards the shared original budget.
	// One registration, one set of facts, no second writable effect copy.
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "allocation"), newKindRule(t, "settlement"))}
	base := settlementImages()
	// A second red line on the same invoice mixes physicalReturn and
	// priceDiscount; its allocation carries quantity zero.
	base["actual_lines"]["redLine2"] = map[string]any{"invoice": "redInv", "purpose": "priceDiscount", "quantity": "0", "net": "10.00", "tax": "0.00", "gross": "10.00"}
	base["allocations"]["g2"] = map[string]any{"line": "redLine2", "original": "orig", "quantity": "0", "net": "-10.00", "tax": "0.00", "gross": "-10.00"}
	// The blue line coexists with a null redKind and no original.
	base["allocations"]["blueDirect"] = map[string]any{"line": "blueLine", "quantity": "0", "net": "0.00", "tax": "0.00", "gross": "0.00"}
	// 90+10 executed plus the 40 reservation exceeds the original 100.
	if err := registration.Validate(nil, base); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("red budget 100+40 over original 100 accepted: %v", err)
	}
	// An explicit closeRemaining capturing the remaining 4/40 releases the
	// reservation: executed 100 fits the original 100 exactly.
	closed := clone(base)
	closed["adjustments"]["a1"] = map[string]any{"kind": "sourceClose", "source": "s1", "close_remaining": true, "quantity": "4", "net": "40.00", "tax": "0.00", "gross": "40.00"}
	if err := registration.Validate(nil, closed); err != nil {
		t.Fatal(err)
	}
	// An allocation beyond the actual line's normalized effect is rejected by
	// the allocation guard while the settlement guard stays satisfied.
	over := clone(closed)
	over["allocations"]["extra"] = map[string]any{"line": "redLine", "original": "orig", "quantity": "-1", "net": "-10.00", "tax": "0.00", "gross": "-10.00"}
	if err := registration.Validate(nil, over); err == nil || err.Error() != "transaction.allocation_exceeded" {
		t.Fatalf("aggregate over the actual effect accepted: %v", err)
	}
	// The raw red invoice faces without any internal allocation stay
	// registrable facts.
	face := clone(base)
	delete(face["allocations"], "red")
	delete(face["allocations"], "g2")
	if err := registration.Validate(nil, face); err != nil {
		t.Fatal(err)
	}
	// Shrinking the original below the executed magnitude is rejected.
	shrunk := clone(closed)
	shrunk["allocations"]["orig"]["net"] = "99.99"
	shrunk["allocations"]["orig"]["gross"] = "99.99"
	if err := registration.Validate(nil, shrunk); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("shrunk original accepted: %v", err)
	}
}
func TestSettlementBalanceAmountPurposesCarryExplicitZeroQuantity(t *testing.T) {
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	images := settlementImages()
	// Drop the quantity-type red allocation; the amount bucket below owns
	// the original budget in this scenario.
	delete(images["allocations"], "red")
	// s1 is no longer open, so only the amount bucket reserves the original.
	images["requests"]["r1"]["status"] = "approved"
	images["requests"]["r2"] = map[string]any{"status": "open", "direction": "red", "purpose": "priceDiscount", "basis": "amount"}
	images["request_lines"]["rl2"] = map[string]any{"request": "r2"}
	images["request_sources"]["s2"] = map[string]any{"line": "rl2", "original": "orig", "quantity": "0", "net": "10.00", "tax": "0.00", "gross": "10.00"}
	images["actual_lines"]["redLine2"] = map[string]any{"invoice": "redInv", "purpose": "priceDiscount", "quantity": "0", "net": "4.00", "tax": "0.00", "gross": "4.00"}
	images["allocations"]["g2"] = map[string]any{"line": "redLine2", "application_source": "s2", "original": "orig", "quantity": "0", "net": "-4.00", "tax": "0.00", "gross": "-4.00"}
	// The s1 request stays open without any allocation, reserving 10/100;
	// the amount reservation 10 plus executed 4 fits the original 100.
	if err := registration.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	// A null quantity on a formal amount-type source stays unknown, not zero.
	null := clone(images)
	null["request_sources"]["s2"]["quantity"] = nil
	if err := registration.Validate(nil, null); err == nil || err.Error() != "transaction.settlement_invalid" {
		t.Fatalf("null amount quantity accepted: %v", err)
	}
	// A positive quantity on an amount-type source is a carrier conflict.
	positive := clone(images)
	positive["request_sources"]["s2"]["quantity"] = "1"
	if err := registration.Validate(nil, positive); err == nil || err.Error() != "transaction.settlement_invalid" {
		t.Fatalf("positive amount quantity accepted: %v", err)
	}
	// Any quantity execution against an amount-basis source's Q=0 is exceeded.
	consumed := clone(images)
	consumed["allocations"]["g3"] = map[string]any{"line": "redLine", "application_source": "s2", "original": "orig", "quantity": "-1", "net": "0.00", "tax": "0.00", "gross": "0.00"}
	if err := registration.Validate(nil, consumed); err == nil || err.Error() != "transaction.settlement_invalid" {
		t.Fatalf("quantity execution against amount source accepted: %v", err)
	}
	// Closing through explicit amounts releases exactly that scope.
	closing := clone(images)
	closing["allocations"]["orig"]["net"] = "13.99"
	closing["allocations"]["orig"]["gross"] = "13.99"
	if err := registration.Validate(nil, closing); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("amount reservation under-reserved: %v", err)
	}
	closing["adjustments"]["a2"] = map[string]any{"kind": "sourceClose", "source": "s2", "close_remaining": false, "quantity": nil, "net": "0.01", "tax": "0.00", "gross": "0.01"}
	if err := registration.Validate(nil, closing); err != nil {
		t.Fatal(err)
	}
}

// Domain acceptance: an amount-basis reservation of 100 with a same-
// transaction actual allocation of 100 plus a fulfillAmount event of 100 ends
// up occupying exactly 100 on the original; the fulfillment consumes the
// reservation instead of stacking on the executed actual.
func TestSettlementBalanceSameTransactionAllocationAndFulfill(t *testing.T) {
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	images := settlementImages()
	delete(images["allocations"], "red")
	images["requests"]["r1"]["purpose"] = "priceDiscount"
	images["requests"]["r1"]["basis"] = "amount"
	images["request_sources"]["s1"]["quantity"] = "0"
	images["request_sources"]["s1"]["net"] = "100.00"
	images["request_sources"]["s1"]["gross"] = "100.00"
	images["actual_lines"]["redLine"]["purpose"] = "priceDiscount"
	images["actual_lines"]["redLine"]["quantity"] = "0"
	images["actual_lines"]["redLine"]["net"] = "100.00"
	images["actual_lines"]["redLine"]["gross"] = "100.00"
	images["allocations"]["g1"] = map[string]any{"line": "redLine", "application_source": "s1", "original": "orig", "quantity": "0", "net": "-100.00", "tax": "0.00", "gross": "-100.00"}
	// Before the fulfillment the reservation keeps 100 occupied on top of the
	// executed 100, exceeding the original 100.
	if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("actual 100 plus open reservation 100 accepted on original 100: %v", err)
	}
	// The fulfillAmount event links the real allocation and consumes exactly
	// the reserved scope in the same transaction: final occupancy is 100.
	images["adjustments"]["f1"] = map[string]any{"kind": "fulfillAmount", "source": "s1", "allocation": "g1", "close_remaining": false, "quantity": "0", "net": "100.00", "tax": "0.00", "gross": "100.00"}
	if err := registration.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	// A fulfillment event without the real allocation link is rejected.
	broken := clone(images)
	broken["adjustments"]["f1"]["allocation"] = "missing"
	if err := registration.Validate(nil, broken); err == nil || err.Error() != "transaction.settlement_invalid" {
		t.Fatalf("fulfillment without real allocation accepted: %v", err)
	}
}

func TestSettlementBalanceClosedReferences(t *testing.T) {
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "settlement"))}
	for _, tc := range []struct {
		name, want string
		mutate     func(Images)
	}{
		{"dangling-request", "settlement_invalid", func(i Images) { i["allocations"]["red"]["application_source"] = "missing" }},
		{"dangling-capacity", "settlement_invalid", func(i Images) { i["allocations"]["red"]["original"] = "missing" }},
		{"dangling-source", "settlement_invalid", func(i Images) {
			i["adjustments"]["a1"] = map[string]any{"kind": "sourceClose", "source": "missing", "close_remaining": true, "quantity": nil, "net": nil, "tax": nil, "gross": nil}
		}},
		{"settled-null-effect", "settlement_invalid", func(i Images) { i["allocations"]["red"]["quantity"] = nil }},
		{"settled-null-net", "settlement_invalid", func(i Images) { i["allocations"]["red"]["net"] = nil }},
		{"red-signed-quantity-consumes-magnitude", "settlement_exceeded", func(i Images) { i["allocations"]["red"]["quantity"] = "-11" }},
		{"negative-request-measure", "settlement_invalid", func(i Images) { i["request_sources"]["s1"]["net"] = "-1.00" }},
		{"negative-close-quantity", "decimal_sign_violation", func(i Images) {
			i["adjustments"]["a1"] = map[string]any{"source": "s1", "quantity": "-1", "net": nil, "tax": nil, "gross": nil}
		}},
		{"cross-direction-source-rejected", "settlement_invalid", func(i Images) {
			i["requests"]["r1"]["direction"] = "blue"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			images := settlementImages()
			tc.mutate(images)
			err := registration.Validate(nil, images)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || err.Error() != "transaction."+tc.want {
				t.Fatalf("want %s got %v", tc.want, err)
			}
		})
	}
}
