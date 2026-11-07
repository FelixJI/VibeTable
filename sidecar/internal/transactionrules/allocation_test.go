package transactionrules

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
)

var (
	invoiceQuantity = exactdecimal.Format{Storage: "decimalText", Scale: 0, Digits: 38}
	invoiceMoney    = exactdecimal.Format{Storage: "decimalText", Scale: 2, Digits: 38}
)

// invoiceGuardsDecimals covers every entity field referenced by the fixed new
// rule corpus in contracts/plugin-{allocation,settlement,evidence}-rule.json.
func invoiceGuardsDecimals() []Decimal {
	return []Decimal{
		{Entity: "allocations", Field: "quantity", Format: invoiceQuantity, Nullable: true},
		{Entity: "allocations", Field: "net", Format: invoiceMoney, Nullable: true},
		{Entity: "allocations", Field: "tax", Format: invoiceMoney, Nullable: true},
		{Entity: "allocations", Field: "gross", Format: invoiceMoney, Nullable: true},
		{Entity: "actual_lines", Field: "quantity", Format: invoiceQuantity, Nullable: true},
		{Entity: "actual_lines", Field: "net", Format: invoiceMoney, Nullable: true},
		{Entity: "actual_lines", Field: "tax", Format: invoiceMoney, Nullable: true},
		{Entity: "actual_lines", Field: "gross", Format: invoiceMoney, Nullable: true},
		{Entity: "blueprint_lines", Field: "quantity", Format: invoiceQuantity, Sign: "nonnegative"},
		{Entity: "blueprint_lines", Field: "net", Format: invoiceMoney},
		{Entity: "blueprint_lines", Field: "tax", Format: invoiceMoney},
		{Entity: "blueprint_lines", Field: "gross", Format: invoiceMoney},
		{Entity: "request_sources", Field: "quantity", Format: invoiceQuantity, Sign: "nonnegative", Nullable: true},
		{Entity: "request_sources", Field: "net", Format: invoiceMoney, Nullable: true},
		{Entity: "request_sources", Field: "tax", Format: invoiceMoney, Nullable: true},
		{Entity: "request_sources", Field: "gross", Format: invoiceMoney, Nullable: true},
		{Entity: "adjustments", Field: "quantity", Format: invoiceQuantity, Sign: "nonnegative", Nullable: true},
		{Entity: "adjustments", Field: "net", Format: invoiceMoney, Sign: "nonnegative", Nullable: true},
		{Entity: "adjustments", Field: "tax", Format: invoiceMoney, Sign: "nonnegative", Nullable: true},
		{Entity: "adjustments", Field: "gross", Format: invoiceMoney, Sign: "nonnegative", Nullable: true},
	}
}

func newKindRule(t *testing.T, name string) Rule {
	t.Helper()
	raw, err := os.ReadFile("../../../contracts/plugin-" + name + "-rule.json")
	if err != nil {
		t.Fatal(err)
	}
	var rule Rule
	if err := json.Unmarshal(raw, &rule); err != nil {
		t.Fatal(err)
	}
	return rule
}

func invoiceGuardsDeclaration(t *testing.T, rules ...Rule) Declaration {
	t.Helper()
	declaration := Declaration{Contract: Contract, ModelID: "m", Version: 1, Decimals: invoiceGuardsDecimals(), Rules: rules}
	if err := declaration.Validate(); err != nil {
		t.Fatal(err)
	}
	return declaration
}

func TestAllocationBalanceDeclaration(t *testing.T) {
	declaration := invoiceGuardsDeclaration(t, newKindRule(t, "allocation"))
	// A notification owner without any direction mapping registers legally.
	plain := cloneRule(declaration.Rules[0])
	plain.Allocation.Parent.Direction = nil
	if err := invoiceGuardsDeclaration(t, plain).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Declaration){
		func(d *Declaration) { d.Rules[0].Kind = "reversalBalance" },
		func(d *Declaration) { d.Rules[0].Allocation.Owner = "" },
		func(d *Declaration) { d.Rules[0].Allocation.Parent.Entity = "" },
		func(d *Declaration) { d.Rules[0].Allocation.Parent.Gross = "" },
		func(d *Declaration) { d.Rules[0].Allocation.State.Field = "" },
		func(d *Declaration) { d.Rules[0].Allocation.Purpose.Field = "" },
		func(d *Declaration) { d.Rules[0].Allocation.DraftStates = []string{"draft", "draft"} },
		func(d *Declaration) { d.Rules[0].Allocation.Parent.Direction.RedDirections = []string{} },
		func(d *Declaration) { d.Rules[0].Allocation.Parent.Direction.ConventionField = "" },
		func(d *Declaration) { d.Rules[0].Allocation.Parent.Direction.Path = []RelationStep{{Field: "invoice"}} },
		func(d *Declaration) { d.Rules[0].Fields = []string{"quantity"} },
		func(d *Declaration) { d.Rules[0].Reservation = &ReservationSpec{} },
		func(d *Declaration) { d.Decimals = []Decimal{d.Decimals[0]} },
		func(d *Declaration) {
			d.Decimals[1] = Decimal{Entity: "allocations", Field: "net", Format: exactdecimal.Format{Storage: "decimalText", Scale: 3, Digits: 38}}
		},
	} {
		wire, _ := json.Marshal(declaration)
		var d Declaration
		if err := json.Unmarshal(wire, &d); err != nil {
			t.Fatal(err)
		}
		mutate(&d)
		if d.Validate() == nil {
			t.Fatal("invalid allocation declaration accepted")
		}
	}
}

func cloneRule(rule Rule) Rule {
	wire, _ := json.Marshal(rule)
	var copy Rule
	if err := json.Unmarshal(wire, &copy); err != nil {
		panic(err)
	}
	return copy
}

func allocationImages() Images {
	return Images{
		"invoices": {
			"blue":  {"status": "issued", "direction": "blue", "amount_convention": "signed"},
			"redS":  {"status": "issued", "direction": "red", "amount_convention": "signed"},
			"redM":  {"status": "issued", "direction": "red", "amount_convention": "magnitude"},
			"draft": {"status": "draft", "direction": "blue", "amount_convention": "signed"},
		},
		"actual_lines": {
			"blueSigned":   {"invoice": "blue", "purpose": nil, "quantity": "10", "net": "84.62", "tax": "15.38", "gross": "100.00"},
			"redSigned":    {"invoice": "redS", "purpose": "physicalReturn", "quantity": "-10", "net": "-90.00", "tax": "-10.00", "gross": "-100.00"},
			"redAnomaly":   {"invoice": "redS", "purpose": "physicalReturn", "quantity": "10", "net": "90.00", "tax": "10.00", "gross": "100.00"},
			"redMagnitude": {"invoice": "redM", "purpose": "physicalReturn", "quantity": "10", "net": "90.00", "tax": "10.00", "gross": "100.00"},
			"draftLine":    {"invoice": "draft", "purpose": "physicalReturn", "quantity": "3", "net": nil, "tax": nil, "gross": nil},
		},
		"allocations": {
			"bluePartial":      {"line": "blueSigned", "quantity": "4", "net": "33.85", "tax": "6.15", "gross": "40.00"},
			"redSignedChild":   {"line": "redSigned", "quantity": "-5", "net": "-45.00", "tax": "-5.00", "gross": "-50.00"},
			"redFromMagnitude": {"line": "redMagnitude", "quantity": "-5", "net": "-45.00", "tax": "-5.00", "gross": "-50.00"},
			"discount":         {"line": "redMagnitude", "quantity": "0", "net": "-10.00", "tax": "0.00", "gross": "-10.00"},
			"draftKnownQty":    {"line": "draftLine", "quantity": "3", "net": nil, "tax": nil, "gross": nil},
			"draftUnknown":     {"line": "draftLine", "quantity": nil, "net": nil, "tax": nil, "gross": nil},
		},
	}
}

func TestAllocationBalanceFinalGraph(t *testing.T) {
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "allocation"))}
	if err := registration.Validate(nil, allocationImages()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(Images)
	}{
		{"missing-owner", "allocation_invalid", func(i Images) { delete(i["allocations"]["bluePartial"], "line") }},
		{"dangling-owner", "allocation_invalid", func(i Images) { i["allocations"]["bluePartial"]["line"] = "missing" }},
		{"identity", "allocation_mismatch", func(i Images) { i["allocations"]["bluePartial"]["tax"] = "6.16" }},
		{"parent-identity", "allocation_mismatch", func(i Images) { i["actual_lines"]["blueSigned"]["tax"] = "15.39" }},
		{"wrong-direction", "allocation_mismatch", func(i Images) {
			i["allocations"]["bluePartial"]["quantity"] = "-4"
			i["allocations"]["bluePartial"]["net"] = "-33.85"
			i["allocations"]["bluePartial"]["tax"] = "-6.15"
			i["allocations"]["bluePartial"]["gross"] = "-40.00"
		}},
		{"signed-anomaly-not-absed", "allocation_mismatch", func(i Images) {
			i["allocations"]["anomaly"] = clone(i)["allocations"]["redSignedChild"]
			i["allocations"]["anomaly"]["line"] = "redAnomaly"
		}},
		{"unknown-convention", "allocation_invalid", func(i Images) { i["invoices"]["redM"]["amount_convention"] = "raw" }},
		{"single-overflow", "allocation_exceeded", func(i Images) {
			i["allocations"]["bluePartial"]["quantity"] = "11"
			i["allocations"]["bluePartial"]["net"] = "92.31"
			i["allocations"]["bluePartial"]["tax"] = "16.69"
			i["allocations"]["bluePartial"]["gross"] = "109.00"
		}},
		{"aggregate-overflow", "allocation_exceeded", func(i Images) {
			i["allocations"]["extra"] = clone(i)["allocations"]["bluePartial"]
			i["allocations"]["extra"]["quantity"] = "7"
			i["allocations"]["extra"]["net"] = "59.24"
			i["allocations"]["extra"]["tax"] = "10.76"
			i["allocations"]["extra"]["gross"] = "70.00"
		}},
		{"red-aggregate-overflow", "allocation_exceeded", func(i Images) {
			i["allocations"]["extra"] = clone(i)["allocations"]["redSignedChild"]
			i["allocations"]["extra"]["quantity"] = "-6"
			i["allocations"]["extra"]["net"] = "-54.00"
			i["allocations"]["extra"]["tax"] = "-6.00"
			i["allocations"]["extra"]["gross"] = "-60.00"
		}},
		{"null-not-zero", "allocation_invalid", func(i Images) { i["allocations"]["bluePartial"]["tax"] = nil }},
		{"partial-null-draft", "allocation_invalid", func(i Images) { i["allocations"]["draftKnownQty"]["net"] = "10.00" }},
		{"draft-money-without-quantity", "allocation_invalid", func(i Images) { i["allocations"]["draftUnknown"]["net"] = "10.00" }},
		{"non-draft-null-money", "allocation_invalid", func(i Images) {
			i["invoices"]["draft"]["status"] = "issued"
			i["allocations"]["draftKnownQty"]["net"] = nil
		}},
		{"non-draft-null-quantity", "allocation_invalid", func(i Images) {
			i["invoices"]["draft"]["status"] = "issued"
			i["allocations"]["draftKnownQty"]["quantity"] = nil
		}},
		{"draft-known-quantity-over-parent", "allocation_exceeded", func(i Images) { i["allocations"]["draftKnownQty"]["quantity"] = "4" }},
		{"discount-quantity", "allocation_mismatch", func(i Images) { i["allocations"]["discount"]["quantity"] = "1" }},
		{"dangling-state-path", "allocation_invalid", func(i Images) { delete(i["actual_lines"]["redSigned"], "invoice") }},
		{"purpose-number-type", "allocation_invalid", func(i Images) { i["actual_lines"]["redSigned"]["purpose"] = 7 }},
		{"purpose-missing-key", "allocation_invalid", func(i Images) { delete(i["actual_lines"]["redSigned"], "purpose") }},
		{"missing-state-path", "allocation_invalid", func(i Images) { i["invoices"]["blue"]["status"] = 7 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			images := allocationImages()
			tc.mutate(images)
			if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction."+tc.want {
				t.Fatalf("want %s got %v", tc.want, err)
			}
		})
	}
}

// The same registration keeps validating notification owners that store raw
// signed faces without any direction mapping.
func TestAllocationBalanceNotificationOwnerWithoutDirection(t *testing.T) {
	rule := newKindRule(t, "allocation")
	rule.Allocation.Parent.Direction = nil
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, rule)}
	images := Images{
		"invoices": {"notice": {"status": "issued", "direction": "blue"}},
		"actual_lines": {
			"positive": {"invoice": "notice", "purpose": nil, "quantity": "10", "net": "84.62", "tax": "15.38", "gross": "100.00"},
			"negative": {"invoice": "notice", "purpose": nil, "quantity": "-4", "net": "-33.85", "tax": "-6.15", "gross": "-40.00"},
		},
		"allocations": {
			"onPositive": {"line": "positive", "quantity": "3", "net": "25.39", "tax": "4.61", "gross": "30.00"},
			"onNegative": {"line": "negative", "quantity": "-2", "net": "-16.93", "tax": "-3.07", "gross": "-20.00"},
		},
	}
	if err := registration.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	images["allocations"]["onNegative"]["quantity"] = "2"
	images["allocations"]["onNegative"]["net"] = "16.93"
	images["allocations"]["onNegative"]["tax"] = "3.07"
	images["allocations"]["onNegative"]["gross"] = "20.00"
	if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.allocation_mismatch" {
		t.Fatalf("raw sign flip on a direction-less owner must stay visible: %v", err)
	}
}

func TestAllocationBalanceNullableBluePurposeOnlyNullMisses(t *testing.T) {
	// Only an explicit JSON null blue purpose legally misses the zero-quantity
	// filter; missing keys, wrong types and broken paths all fail closed, and a
	// selected red row with a null or unknown redKind is rejected downstream.
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "allocation"))}
	base := allocationImages()
	// blueSigned keeps purpose nil and passes in the base fixture.
	if err := registration.Validate(nil, base); err != nil {
		t.Fatal(err)
	}
	broken := clone(base)
	broken["actual_lines"]["blueSigned"]["purpose"] = 7
	if err := registration.Validate(nil, broken); err == nil || err.Error() != "transaction.allocation_invalid" {
		t.Fatalf("numeric purpose accepted: %v", err)
	}
	missing := clone(base)
	delete(missing["actual_lines"]["blueSigned"], "purpose")
	if err := registration.Validate(nil, missing); err == nil || err.Error() != "transaction.allocation_invalid" {
		t.Fatalf("missing purpose key accepted: %v", err)
	}
	dangling := clone(base)
	dangling["allocations"]["bluePartial"]["line"] = "gone"
	if err := registration.Validate(nil, dangling); err == nil || err.Error() != "transaction.allocation_invalid" {
		t.Fatalf("dangling purpose path accepted: %v", err)
	}
}

func TestAllocationBalanceClosedBoundsAndDependencies(t *testing.T) {
	base := invoiceGuardsDeclaration(t, newKindRule(t, "allocation"))
	r := Registration{Declaration: base, Bindings: map[string]pluginmodel.Binding{}}
	for entity := range r.dependencies() {
		r.Bindings[entity] = pluginmodel.Binding{TableID: "t_" + entity, Fields: map[string]string{}}
	}
	for entity, fields := range r.dependencies() {
		for field := range fields {
			r.Bindings[entity].Fields[field] = "f_" + field
			if !r.ProtectsBinding(r.Bindings[entity].TableID, "f_"+field) {
				t.Fatal("unprotected dependency", entity, field)
			}
		}
	}
	for _, field := range []string{"f_amount_convention", "f_direction", "f_status"} {
		if !r.selectedBy(WriteSet{"t_invoices": {field: true}}) {
			t.Fatal("owner-only authority change bypasses the allocation guard", field)
		}
	}
	for _, table := range []string{"t_allocations", "t_actual_lines", "t_invoices"} {
		if !r.selectedBy(WriteSet{table: {"*": true}}) {
			t.Fatal("whole-table native write bypasses the allocation guard", table)
		}
	}
}
