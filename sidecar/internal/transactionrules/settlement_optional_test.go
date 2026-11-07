package transactionrules

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
)

// externalSettlementRule rewrites the natural red-bucket corpus rule into the
// normal bucket: the obligations entity is an external capacity that only
// declares the quantity subset while request/actual keep the full four
// measures. All mutations stay on the wire shape so the same test source runs
// unchanged before and after the optional-capacity implementation.
func externalSettlementRule(t *testing.T, mutate func(spec map[string]any)) (Rule, error) {
	t.Helper()
	raw, err := os.ReadFile("../../../contracts/plugin-settlement-rule.json")
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	spec := object["settlement"].(map[string]any)
	capacity := map[string]any{"entity": "obligations", "quantity": "quantity"}
	spec["capacity"] = capacity
	request := spec["request"].(map[string]any)
	request["directions"] = []any{"blue"}
	delete(request, "purpose")
	delete(request, "purposes")
	request["capacity"] = "obligation"
	actual := spec["actual"].(map[string]any)
	actual["directions"] = []any{"blue"}
	delete(actual, "purpose")
	delete(actual, "purposes")
	delete(actual, "zeroQuantityPurposes")
	actual["capacity"] = "obligation"
	// The real normal blue path: request/actual declare the paired scope refs
	// while the capacity keeps its own refs optional (the candidate shape omits
	// capacity.currency and no red purpose exists anywhere).
	lineStep := func(field, target string) map[string]any { return map[string]any{"field": field, "target": target} }
	request["currency"] = map[string]any{"path": []any{lineStep("line", "request_lines"), lineStep("request", "requests")}, "field": "currency"}
	actual["currency"] = map[string]any{"path": []any{lineStep("line", "actual_lines"), lineStep("invoice", "invoices")}, "field": "currency"}
	request["unit"] = map[string]any{"path": []any{lineStep("line", "request_lines")}, "field": "unit"}
	actual["unit"] = map[string]any{"path": []any{lineStep("line", "actual_lines")}, "field": "unit"}
	request["material"] = map[string]any{"path": []any{lineStep("line", "request_lines")}, "field": "material"}
	actual["material"] = map[string]any{"path": []any{lineStep("line", "actual_lines")}, "field": "material"}
	if mutate == nil {
		capacity["optional"] = true
	} else {
		mutate(spec)
	}
	encoded, _ := json.Marshal(object)
	var rule Rule
	err = json.Unmarshal(encoded, &rule)
	return rule, err
}

func externalSettlementRegistration(t *testing.T, mutate func(spec map[string]any)) Registration {
	t.Helper()
	rule, err := externalSettlementRule(t, mutate)
	if err != nil {
		t.Fatalf("external rule did not decode: %v", err)
	}
	d := Declaration{Contract: Contract, ModelID: "m", Version: 1, Decimals: invoiceGuardsDecimals(), Rules: []Rule{rule}}
	d.Decimals = append(d.Decimals,
		Decimal{Entity: "obligations", Field: "quantity", Format: exactdecimal.Format{Storage: "decimalText", Scale: 0, Digits: 38}, Sign: "nonnegative"},
		Decimal{Entity: "obligations", Field: "net", Format: invoiceMoney, Sign: "nonnegative", Nullable: true},
		Decimal{Entity: "obligations", Field: "gross", Format: invoiceMoney, Sign: "nonnegative", Nullable: true})
	return Registration{Declaration: d}
}

// optionalImages builds the blue normal bucket: one null-capacity request
// source with the full four measures and its blue executions/closures.
func optionalImages() Images {
	return Images{
		"requests":        {"r1": {"status": "open", "direction": "blue", "basis": "quantity", "currency": "CNY"}},
		"request_lines":   {"rl1": {"request": "r1", "unit": "piece", "material": nil}},
		"request_sources": {"s1": {"line": "rl1", "obligation": nil, "quantity": "10", "net": "10000.00", "tax": "0.00", "gross": "10000.00"}},
		"invoices":        {"blueInv": {"status": "issued", "direction": "blue", "amount_convention": "signed", "currency": "CNY"}},
		"actual_lines":    {"blueLine": {"invoice": "blueInv", "purpose": nil, "quantity": "6", "net": "6000.00", "tax": "0.00", "gross": "6000.00", "unit": "piece", "material": nil}},
		"allocations":     {"a6": {"line": "blueLine", "application_source": "s1", "obligation": nil, "quantity": "6", "net": "6000.00", "tax": "0.00", "gross": "6000.00"}},
		"obligations":     {},
		"adjustments":     {},
	}
}

func TestSettlementOptionalExternalDeclaration(t *testing.T) {
	// optional:true plus a quantity-only external capacity subset is a legal
	// declaration while request/actual keep the full four measures.
	externalSettlementRegistration(t, nil)
	// Only literal true is accepted; false, null and wrong types stay closed.
	for _, value := range []any{false, nil, "true", 1} {
		rule, err := externalSettlementRule(t, func(spec map[string]any) {
			spec["capacity"].(map[string]any)["optional"] = value
		})
		if err == nil && rule.Settlement != nil {
			t.Fatalf("optional=%v accepted", value)
		}
	}
	// The red bucket keeps the four-measure shared original-blue bucket and
	// never enables optional.
	red := newKindRule(t, "settlement")
	var object map[string]any
	raw, _ := json.Marshal(red)
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	spec := object["settlement"].(map[string]any)
	spec["capacity"].(map[string]any)["optional"] = true
	encoded, _ := json.Marshal(object)
	var mutated Rule
	if err := json.Unmarshal(encoded, &mutated); err != nil {
		t.Fatal(err)
	}
	d := Declaration{Contract: Contract, ModelID: "m", Version: 1, Decimals: invoiceGuardsDecimals(), Rules: []Rule{mutated}}
	if d.Validate() == nil {
		t.Fatal("red bucket accepted optional capacity")
	}
	// A capacity currency ref without the request/actual pair is rejected.
	orphan := externalSettlementRegistration(t, func(spec map[string]any) {
		spec["capacity"].(map[string]any)["currency"] = map[string]any{"field": "currency"}
		delete(spec["request"].(map[string]any), "currency")
		delete(spec["actual"].(map[string]any), "currency")
	})
	if orphan.Declaration.Validate() == nil {
		t.Fatal("capacity-only scope ref accepted")
	}
	// The external capacity may widen inside the request/actual set.
	sub := externalSettlementRegistration(t, func(spec map[string]any) {
		spec["capacity"].(map[string]any)["net"] = "net"
	})
	if err := sub.Declaration.Validate(); err != nil {
		t.Fatalf("capacity subset wider than request/actual accepted no more: %v", err)
	}
	// A capacity measure outside the request/actual set is rejected.
	drift := externalSettlementRegistration(t, func(spec map[string]any) {
		spec["capacity"].(map[string]any)["net"] = "net"
		delete(spec["request"].(map[string]any), "net")
		delete(spec["actual"].(map[string]any), "net")
		spec["adjustment"].(map[string]any)["closeNet"] = ""
	})
	if drift.Declaration.Validate() == nil {
		t.Fatal("capacity measure without the source measure accepted")
	}
	// Request and actual must declare the identical full measure set.
	mismatch := externalSettlementRegistration(t, func(spec map[string]any) {
		delete(spec["actual"].(map[string]any), "gross")
	})
	if mismatch.Declaration.Validate() == nil {
		t.Fatal("request/actual measure sets may drift")
	}
}

func TestSettlementOptionalNullCapacityQuantitySource(t *testing.T) {
	registration := externalSettlementRegistration(t, nil)
	images := optionalImages()
	// Executed 6 plus an explicit closeRemaining of the proportional fixed
	// remaining scope Q4/N4000/T0/G4000 settles the null-capacity source.
	images["adjustments"]["close"] = map[string]any{"kind": "closeRemaining", "source": "s1", "close_remaining": true, "quantity": "4", "net": "4000.00", "tax": "0.00", "gross": "4000.00"}
	if err := registration.Validate(nil, images); err != nil {
		t.Fatalf("null capacity source with E6+C4 rejected: %v", err)
	}
	// E6+C5 exceeds the source quantity itself.
	over := clone(images)
	over["adjustments"]["close"]["quantity"] = "5"
	if err := registration.Validate(nil, over); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("close 5 after execution 6 accepted: %v", err)
	}
	// Missing, empty, mistyped or dangling capacity refs still fail closed.
	for name, value := range map[string]any{"missing": nil, "empty": "", "typed": 7, "dangling": "o404"} {
		invalid := clone(optionalImages())
		row := invalid["request_sources"]["s1"]
		if name == "missing" {
			delete(row, "obligation")
		} else {
			row["obligation"] = value
			if name == "dangling" {
				invalid["obligations"]["o404"] = map[string]any{"quantity": "10"}
			}
		}
		if err := registration.Validate(nil, invalid); err == nil {
			t.Fatalf("capacity %s accepted", name)
		}
	}
	// A null-capacity source still needs its executions to match the source
	// quantity; the same row is not skipped wholesale.
	invalid := clone(optionalImages())
	invalid["allocations"]["a9"] = map[string]any{"line": "blueLine", "application_source": "s1", "obligation": nil, "quantity": "9", "net": "9000.00", "tax": "0.00", "gross": "9000.00"}
	if err := registration.Validate(nil, invalid); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("6+9 executed against quantity 10 accepted: %v", err)
	}
}

func TestSettlementOptionalAmountFulfillmentBudget(t *testing.T) {
	registration := externalSettlementRegistration(t, nil)
	amount := optionalImages()
	amount["requests"]["r1"]["basis"] = "amount"
	amount["actual_lines"]["blueLine"]["quantity"] = "0"
	for _, row := range amount["request_sources"] {
		row["quantity"] = "0"
	}
	amount["allocations"]["a6"]["quantity"] = "0"
	// A real amount actual without a fulfill event never settles the source;
	// the commit boundary compares the new close against the real before image.
	pending := clone(amount)
	pending["adjustments"]["close"] = map[string]any{"kind": "closeRemaining", "source": "s1", "close_remaining": true, "quantity": "0", "net": "4000.00", "tax": "0.00", "gross": "4000.00"}
	if err := registration.Validate(clone(amount), pending); err == nil {
		t.Fatal("actual 6000 auto-settled the amount source without fulfill")
	}
	// fulfill 6000 plus closeRemaining 4000 settles exactly 10000.
	settled := clone(amount)
	settled["adjustments"]["fulfill"] = map[string]any{"kind": "fulfillAmount", "source": "s1", "close_remaining": false, "allocation": "a6", "quantity": nil, "net": "6000.00", "tax": "0.00", "gross": "6000.00"}
	settled["adjustments"]["close"] = map[string]any{"kind": "closeRemaining", "source": "s1", "close_remaining": true, "quantity": "0", "net": "4000.00", "tax": "0.00", "gross": "4000.00"}
	if err := registration.Validate(clone(amount), settled); err != nil {
		t.Fatalf("fulfill 6000 + close 4000 rejected: %v", err)
	}
	// fulfill may not exceed its real actual attribution.
	overActual := clone(settled)
	overActual["adjustments"]["fulfill"]["net"] = "6001.00"
	overActual["adjustments"]["fulfill"]["gross"] = "6001.00"
	if err := registration.Validate(clone(amount), overActual); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("fulfill 6001 over actual 6000 accepted: %v", err)
	}
	// Cumulative fulfill plus close may not exceed the source expectation.
	overSource := clone(settled)
	overSource["adjustments"]["close"]["net"] = "4001.00"
	overSource["adjustments"]["close"]["gross"] = "4001.00"
	if err := registration.Validate(clone(amount), overSource); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("fulfill 6000 + close 4001 over source 10000 accepted: %v", err)
	}
}

func TestSettlementOptionalCapacityPairsAndErasure(t *testing.T) {
	registration := externalSettlementRegistration(t, nil)
	images := optionalImages()
	images["obligations"]["o1"] = map[string]any{"quantity": "10", "net": "10000.00", "gross": nil}
	// A known source capacity with a null actual capacity is a mismatch.
	images["request_sources"]["s1"]["obligation"] = "o1"
	if err := registration.Validate(nil, images); err == nil {
		t.Fatal("known source capacity with null actual capacity accepted")
	}
	// The reverse mismatch is equally rejected.
	reverse := optionalImages()
	reverse["obligations"]["o1"] = map[string]any{"quantity": "10", "net": "10000.00", "gross": nil}
	reverse["allocations"]["a6"]["obligation"] = "o1"
	if err := registration.Validate(nil, reverse); err == nil {
		t.Fatal("null source capacity with known actual capacity accepted")
	}
	// Preserved known capacity cannot be erased on either side.
	before := optionalImages()
	before["obligations"]["o1"] = map[string]any{"quantity": "10", "net": "10000.00", "gross": nil}
	before["request_sources"]["s1"]["obligation"] = "o1"
	before["allocations"]["a6"]["obligation"] = "o1"
	if err := registration.Validate(nil, before); err != nil {
		t.Fatalf("known capacity pair rejected: %v", err)
	}
	erasedSource := clone(before)
	erasedSource["request_sources"]["s1"]["obligation"] = nil
	erasedSource["allocations"]["a6"]["obligation"] = nil
	if err := registration.Validate(before, erasedSource); err == nil {
		t.Fatal("preserved known capacity erased to null")
	}
}

func TestSettlementOptionalRealNormalScopeGuards(t *testing.T) {
	registration := externalSettlementRegistration(t, nil)
	// The real normal declaration pairs request/actual scope refs and omits
	// the capacity refs entirely.
	if err := registration.Declaration.Validate(); err != nil {
		t.Fatalf("real normal declaration rejected: %v", err)
	}
	// Blue amount source: a Q0 actual with a null unit and unknown material
	// passes because the amount basis skips the quantity scope.
	amount := optionalImages()
	amount["requests"]["r1"]["basis"] = "amount"
	for _, row := range amount["request_sources"] {
		row["quantity"] = "0"
	}
	amount["actual_lines"]["blueLine"]["quantity"] = "0"
	amount["actual_lines"]["blueLine"]["unit"] = nil
	amount["allocations"]["a6"]["quantity"] = "0"
	if err := registration.Validate(nil, amount); err != nil {
		t.Fatalf("amount-basis Q0 actual with null unit rejected: %v", err)
	}
	// The amount basis still forces an explicit zero quantity on its actual.
	nonZero := clone(amount)
	nonZero["actual_lines"]["blueLine"]["quantity"] = "2"
	nonZero["allocations"]["a6"]["quantity"] = "2"
	if err := registration.Validate(nil, nonZero); err == nil {
		t.Fatal("amount-basis non-zero actual accepted")
	}
	// A quantity source keeps the explicit unit equality even with a null
	// capacity, including zero-quantity actuals.
	conflict := clone(optionalImages())
	conflict["actual_lines"]["blueLine"]["unit"] = "box"
	if err := registration.Validate(nil, conflict); err == nil {
		t.Fatal("unit conflict against the source accepted")
	}
	empty := clone(optionalImages())
	empty["actual_lines"]["blueLine"]["unit"] = ""
	if err := registration.Validate(nil, empty); err == nil {
		t.Fatal("empty actual unit accepted on a quantity source")
	}
	// Known material and currency conflicts are rejected even without a
	// capacity; the capacity currency ref stays an optional extra check.
	material := clone(optionalImages())
	material["request_lines"]["rl1"]["material"] = "m1"
	material["actual_lines"]["blueLine"]["material"] = "m2"
	if err := registration.Validate(nil, material); err == nil {
		t.Fatal("known material conflict accepted")
	}
	currency := clone(optionalImages())
	currency["invoices"]["blueInv"]["currency"] = "USD"
	if err := registration.Validate(nil, currency); err == nil {
		t.Fatal("currency conflict accepted")
	}
}

func TestSettlementOptionalCloseScopeFollowsSourceMeasures(t *testing.T) {
	// A net-only external capacity subset must not disable the fixed close
	// capture of a quantity source: Q10/N10000 with E6 fixes Q4/N4000.
	registration := externalSettlementRegistration(t, func(spec map[string]any) {
		capacity := spec["capacity"].(map[string]any)
		delete(capacity, "quantity")
		capacity["net"] = "net"
		capacity["optional"] = true
	})
	before := optionalImages()
	after := clone(before)
	after["adjustments"]["close"] = map[string]any{"kind": "closeRemaining", "source": "s1", "close_remaining": true, "quantity": "4", "net": "3999.00", "tax": "0.00", "gross": "3999.00"}
	if err := registration.Validate(before, after); err == nil {
		t.Fatal("closeRemaining below the proportional fixed net accepted")
	}
	exact := clone(after)
	exact["adjustments"]["close"]["net"] = "4000.00"
	exact["adjustments"]["close"]["gross"] = "4000.00"
	if err := registration.Validate(before, exact); err != nil {
		t.Fatalf("proportional fixed close rejected: %v", err)
	}
}

func TestSettlementOptionalGrossRemainderUsesSourceMeasures(t *testing.T) {
	// A gross-only external capacity subset still compares the source's gross
	// remainder, which is the sum of the rounded net and tax remainders.
	registration := externalSettlementRegistration(t, func(spec map[string]any) {
		capacity := spec["capacity"].(map[string]any)
		delete(capacity, "quantity")
		capacity["gross"] = "gross"
		capacity["optional"] = true
	})
	images := optionalImages()
	images["obligations"]["o1"] = map[string]any{"quantity": "10", "gross": "0.01", "net": nil}
	for _, row := range images["request_sources"] {
		row["obligation"] = "o1"
		row["quantity"] = "3"
		row["net"] = "0.01"
		row["tax"] = "0.01"
		row["gross"] = "0.02"
	}
	images["requests"]["r1"]["basis"] = "quantity"
	images["request_lines"]["rl1"]["unit"] = "piece"
	images["actual_lines"]["blueLine"]["quantity"] = "1"
	images["actual_lines"]["blueLine"]["net"] = "0.00"
	images["actual_lines"]["blueLine"]["tax"] = "0.00"
	images["actual_lines"]["blueLine"]["gross"] = "0.00"
	images["allocations"]["a6"]["obligation"] = "o1"
	images["allocations"]["a6"]["quantity"] = "1"
	images["allocations"]["a6"]["net"] = "0.00"
	images["allocations"]["a6"]["tax"] = "0.00"
	images["allocations"]["a6"]["gross"] = "0.00"
	if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("gross remainder N+T over a gross-only capacity accepted: %v", err)
	}
	wide := clone(images)
	wide["obligations"]["o1"]["gross"] = "0.02"
	if err := registration.Validate(nil, wide); err != nil {
		t.Fatalf("legal gross remainder rejected: %v", err)
	}
}

func TestSettlementOptionalPlansCarryAmountSources(t *testing.T) {
	// An amount source with a legal plan item carries no quantity reservation
	// into the plan bucket while its money still settles only through events.
	rule := newKindRule(t, "settlement")
	s := rule.Settlement
	s.Capacity = SettlementCapacity{Entity: "obligations", Quantity: "quantity", Optional: true}
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
	s.Plans = reservationPlanRule().Reservation.Plans
	d := Declaration{Contract: Contract, ModelID: "m", Version: 1, Decimals: invoiceGuardsDecimals(), Rules: []Rule{rule}}
	for _, entity := range []string{"obligations", "plan_items"} {
		d.Decimals = append(d.Decimals, Decimal{Entity: entity, Field: "quantity", Format: exactdecimal.Format{Storage: "decimalText", Digits: 38}, Sign: "nonnegative"})
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	registration := Registration{Declaration: d}
	images := settlementImages()
	delete(images["allocations"], "red")
	images["requests"]["r1"]["direction"] = "blue"
	images["requests"]["r1"]["basis"] = "amount"
	images["request_sources"]["s1"]["obligation"] = "o1"
	images["request_sources"]["s1"]["plan_item"] = "p1"
	images["request_sources"]["s1"]["quantity"] = "0"
	images["actual_lines"]["blueLine"]["quantity"] = "0"
	images["allocations"]["orig"]["application_source"] = "s1"
	images["allocations"]["orig"]["obligation"] = "o1"
	images["allocations"]["orig"]["quantity"] = "0"
	for _, entity := range []string{"series", "plan_versions", "logical_items", "plan_items"} {
		images[entity] = reservationPlanImages()[entity]
	}
	images["obligations"] = map[string]map[string]any{"o1": {"quantity": "16"}}
	images["plan_items"]["p1"]["quantity"] = "8"
	if err := registration.Validate(nil, images); err != nil {
		t.Fatalf("amount source with plan item rejected: %v", err)
	}
	// The money still settles only through explicit events within the budget.
	over := clone(images)
	over["adjustments"]["fulfill"] = map[string]any{"kind": "fulfillAmount", "source": "s1", "close_remaining": false, "allocation": "orig", "quantity": nil, "net": "100.01", "tax": "0.00", "gross": "100.01"}
	if err := registration.Validate(images, over); err == nil || err.Error() != "transaction.settlement_exceeded" {
		t.Fatalf("fulfill over the actual attribution accepted: %v", err)
	}
	exact := clone(images)
	exact["adjustments"]["fulfill"] = map[string]any{"kind": "fulfillAmount", "source": "s1", "close_remaining": false, "allocation": "orig", "quantity": nil, "net": "100.00", "tax": "0.00", "gross": "100.00"}
	exact["adjustments"]["close"] = map[string]any{"kind": "closeRemaining", "source": "s1", "close_remaining": true, "quantity": "0", "net": "0.00", "tax": "0.00", "gross": "0.00"}
	if err := registration.Validate(images, exact); err != nil {
		t.Fatalf("fulfill plus close rejected: %v", err)
	}
}
