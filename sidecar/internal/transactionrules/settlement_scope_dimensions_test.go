package transactionrules

import (
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
)

func TestSettlementScopeSelectsAndProtectsItsBindings(t *testing.T) {
	rule := newKindRule(t, "settlement")
	rule.Settlement.ScopeDimensions = []SettlementScopeDimension{{Capacity: "area", Request: "source_area", Actual: "actual_area", Target: "areas"}}
	r := Registration{Declaration: invoiceGuardsDeclaration(t, rule), Bindings: map[string]pluginmodel.Binding{
		"areas":           {TableID: "ta"},
		"allocations":     {TableID: "tc", Fields: map[string]string{"area": "f1", "actual_area": "f2"}},
		"request_sources": {TableID: "tr", Fields: map[string]string{"source_area": "f3"}},
	}}
	for _, tc := range [][2]string{{"tc", "f1"}, {"tc", "f2"}, {"tr", "f3"}, {"ta", "*"}} {
		field := tc[1]
		if field == "*" {
			field = ""
		}
		if !r.selectedBy(WriteSet{tc[0]: {tc[1]: true}}) || !r.ProtectsBinding(tc[0], field) {
			t.Fatalf("unprotected scope table or field %v", tc)
		}
	}
}

func TestSettlementScopeUsesEachOutstandingReservation(t *testing.T) {
	rule := newKindRule(t, "settlement")
	rule.Settlement.ScopeDimensions = []SettlementScopeDimension{{Capacity: "anchor_project", Request: "source_project", Actual: "assigned_project", Target: "projects"}}
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, rule)}
	base := settlementImages()
	base["projects"] = map[string]map[string]any{"a": {}, "b": {}}
	base["allocations"]["orig"]["anchor_project"] = "a"
	base["request_sources"]["s1"]["source_project"] = nil
	delete(base["allocations"], "red")
	check := func(t *testing.T, before, after Images, allowed bool) {
		t.Helper()
		err := registration.Validate(before, after)
		if allowed && err != nil || !allowed && (err == nil || err.Error() != "transaction.settlement_invalid") {
			t.Fatalf("allowed=%v, got %v", allowed, err)
		}
	}
	for _, tc := range []struct {
		name         string
		basis        string
		quantity     string
		net, tax     string
		closedQ      string
		closedN      string
		closedT      string
		closedGross  string
		wantReleased bool
	}{
		{"zero-price units partially closed", "quantity", "10", "0.00", "0.00", "9", "0.00", "0.00", "0.00", false},
		{"all units closed", "quantity", "10", "0.00", "0.00", "10", "0.00", "0.00", "0.00", true},
		{"outstanding net only", "amount", "0", "50.00", "50.00", "0", "49.99", "50.00", "99.99", false},
		{"outstanding tax only", "amount", "0", "50.00", "50.00", "0", "50.00", "49.99", "99.99", false},
		{"amount fully closed", "amount", "0", "50.00", "50.00", "0", "50.00", "50.00", "100.00", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := clone(base)
			before["allocations"]["orig"]["tax"] = "50.00"
			before["allocations"]["orig"]["gross"] = "150.00"
			source := before["request_sources"]["s1"]
			source["quantity"], source["net"], source["tax"], source["gross"] = tc.quantity, tc.net, tc.tax, "0.00"
			if tc.basis == "amount" {
				source["gross"] = "100.00"
				header := before["requests"]["r1"]
				header["basis"], header["purpose"] = "amount", "priceDiscount"
			}
			before["adjustments"]["close"] = map[string]any{"kind": "sourceClose", "source": "s1", "close_remaining": false, "quantity": tc.closedQ, "net": tc.closedN, "tax": tc.closedT, "gross": tc.closedGross}
			after := clone(before)
			after["allocations"]["orig"]["anchor_project"] = "b"
			check(t, before, after, tc.wantReleased)
			after["allocations"]["orig"]["anchor_project"] = nil
			check(t, before, after, tc.wantReleased)
		})
	}
	t.Run("partial and full execution", func(t *testing.T) {
		before := clone(base)
		red := settlementImages()["allocations"]["red"]
		red["assigned_project"] = nil
		red["net"], red["gross"] = "-60.00", "-60.00"
		before["allocations"]["red"] = red
		after := clone(before)
		after["allocations"]["orig"]["anchor_project"] = "b"
		check(t, before, after, false)
		// Once the request is fully executed its own mismatching history is
		// released on admission; the settled actual independently pins the
		// previous inherited scope on subsequent corrections.
		red["quantity"] = "-10"
		before["request_sources"]["s1"]["source_project"] = "b"
		check(t, nil, before, true)
		after = clone(before)
		after["allocations"]["orig"]["anchor_project"] = "b"
		check(t, before, after, false)
	})
	t.Run("canceled, historical and other sources", func(t *testing.T) {
		before := clone(base)
		before["request_sources"]["s1"]["source_project"] = "a"
		before["requests"]["r1"]["status"] = "canceled"
		after := clone(before)
		after["allocations"]["orig"]["anchor_project"] = "b"
		check(t, before, after, true)
		after["requests"]["r2"] = clone(base)["requests"]["r1"]
		after["request_lines"]["rl2"] = map[string]any{"request": "r2"}
		after["request_sources"]["s2"] = clone(base)["request_sources"]["s1"]
		after["request_sources"]["s2"]["line"] = "rl2"
		after["request_sources"]["s2"]["source_project"] = "a"
		check(t, before, after, false)
	})
	t.Run("fixed close survives removal of mistaken actual", func(t *testing.T) {
		partial := clone(base)
		partial["request_sources"]["s1"]["source_project"] = "a"
		red := settlementImages()["allocations"]["red"]
		red["assigned_project"], red["net"], red["gross"] = nil, "-60.00", "-60.00"
		partial["allocations"]["red"] = red
		closed := clone(partial)
		closed["adjustments"]["closed"] = map[string]any{"kind": "sourceClose", "source": "s1", "close_remaining": true, "quantity": "4", "net": "40.00", "tax": "0.00", "gross": "40.00"}
		check(t, partial, closed, true)
		reassigned := clone(closed)
		reassigned["allocations"]["orig"]["anchor_project"] = "b"
		check(t, closed, reassigned, false) // The actual still inherits the old scope.
		delete(reassigned["allocations"], "red")
		check(t, closed, reassigned, true)
		check(t, nil, reassigned, true) // Historical admission after correction.
		// Six units still occupy the budget after reversing the actual. Only
		// attribution protection was released by the fixed four-unit close.
		reassigned["allocations"]["orig"]["quantity"] = "5"
		if err := registration.Validate(nil, reassigned); err == nil || err.Error() != "transaction.settlement_exceeded" {
			t.Fatalf("lost remaining capacity usage: %v", err)
		}
	})
	t.Run("confirmation validates scope even when fully closed in the same commit", func(t *testing.T) {
		draft := clone(base)
		draft["requests"]["r1"]["status"] = "draft"
		draft["request_sources"]["s1"]["source_project"] = "a"
		draft["allocations"]["orig"]["anchor_project"] = "b"
		confirmed := clone(draft)
		confirmed["requests"]["r1"]["status"] = "open"
		confirmed["adjustments"]["close"] = map[string]any{"kind": "sourceClose", "source": "s1", "close_remaining": true, "quantity": "10", "net": "100.00", "tax": "0.00", "gross": "100.00"}
		check(t, draft, confirmed, false)
		confirmed["allocations"]["orig"]["anchor_project"] = "a"
		check(t, draft, confirmed, true)
		corrected := clone(confirmed)
		corrected["allocations"]["orig"]["anchor_project"] = "b"
		check(t, confirmed, corrected, true)
		check(t, nil, corrected, true)
	})
	t.Run("unknowns and malformed relations", func(t *testing.T) {
		before := clone(base)
		before["allocations"]["orig"]["anchor_project"] = nil
		after := clone(before)
		after["allocations"]["orig"]["anchor_project"] = "a"
		check(t, before, after, true)
		check(t, after, clone(after), true)
		for _, value := range []any{"", "missing", 17} {
			after["request_sources"]["s1"]["source_project"] = value
			check(t, before, after, false)
		}
		delete(after["request_sources"]["s1"], "source_project")
		check(t, before, after, false)
	})
}

func TestSettlementScopeRetainsOnlyCumulativeDimensions(t *testing.T) {
	first := SettlementScopeDimension{Capacity: "a", Request: "s", Actual: "r", Target: "areas"}
	second := SettlementScopeDimension{Capacity: "project", Request: "project", Actual: "project", Target: "projects"}
	registered := func(version int, dimensions ...SettlementScopeDimension) Registration {
		rule := newKindRule(t, "settlement")
		rule.Settlement.ScopeDimensions = dimensions
		r := Registration{WorkspaceID: "w", InstallationID: "i", PluginID: "p", Declaration: invoiceGuardsDeclaration(t, rule), Bindings: map[string]pluginmodel.Binding{}}
		r.Declaration.Version = version
		for entity, fields := range r.dependencies() {
			b := pluginmodel.Binding{TableID: entity, Fields: map[string]string{}}
			for field := range fields {
				b.Fields[field] = field
			}
			r.Bindings[entity] = b
		}
		return r
	}
	initial, one, both := registered(1), registered(2, first), registered(3, second, first)
	if guards := activeGuards([]Registration{initial, one, both}); len(guards) != 1 || guards[0].Declaration.Version != 3 {
		t.Fatal("cumulative scopes must spend the read budget only once")
	}
	replaced := first
	replaced.Request = "other"
	changedMeasure := registered(4, first, second)
	changedMeasure.Declaration.Rules[0].Settlement.Capacity.Directions = []string{"another"}
	for _, invalid := range []Registration{registered(3, first, second), registered(4, first), registered(4, replaced, second), changedMeasure} {
		if invalid.Declaration.Retains(both.Declaration) {
			t.Fatal("changed rule, removed mapping or same-version edit authorized recovery")
		}
	}
}
