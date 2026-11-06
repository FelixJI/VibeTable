package transactionrules

import (
	"encoding/json"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
)

var freezeFormat = exactdecimal.Format{Storage: "decimalText", Scale: 0, Digits: 38}

func parentFreezeRule() Rule {
	return Rule{ID: "freeze", Kind: "parentFreeze", Entity: "sources",
		ParentPath:       []RelationStep{{Field: "owner_line", Target: "lines"}, {Field: "document", Target: "documents"}},
		ParentStateField: "phase", FrozenStates: []string{"delivery_confirmed"}, Fields: []string{"quantity", "scope"}}
}

func freezeImages() Images {
	return Images{
		"documents": {"d1": {"phase": "delivery_confirmed"}, "d2": {"phase": "instruction_open"}},
		"lines":     {"l1": {"document": "d1"}, "l2": {"document": "d2"}},
		"sources":   {"c1": {"owner_line": "l1", "quantity": "5", "scope": "o1", "note": "x"}},
	}
}

func TestParentFreezeClosedDeclaration(t *testing.T) {
	raw, _ := json.Marshal(Declaration{Contract: Contract, ModelID: "m", Version: 1, Rules: []Rule{parentFreezeRule()}})
	if _, err := Decode(append(raw[:len(raw)-1], []byte(`,{"id":"x","kind":"parentFreeze","entity":"sources","parentPath":[{"field":"owner_line","target":"lines"}],"parentStateField":"phase","frozenStates":["delivery_confirmed"],"amount":"q"}]}`)...)); err == nil {
		t.Fatal("foreign field accepted")
	}
	for _, broken := range []Rule{
		{ID: "a", Kind: "parentFreeze", Entity: "sources", ParentStateField: "phase", FrozenStates: []string{"delivery_confirmed"}},
		{ID: "b", Kind: "parentFreeze", Entity: "sources", ParentPath: []RelationStep{{Field: "owner_line", Target: "lines"}, {Field: "document", Target: "documents"}}, FrozenStates: []string{"delivery_confirmed"}},
		{ID: "c", Kind: "parentFreeze", Entity: "sources", ParentPath: []RelationStep{{Field: "owner_line", Target: "lines"}}, ParentStateField: "phase"},
		{ID: "d", Kind: "parentFreeze", Entity: "sources", ParentPath: []RelationStep{{Field: "owner_line", Target: "lines"}}, ParentStateField: "phase", FrozenStates: []string{"delivery_confirmed"}, Field: "phase"},
	} {
		if err := (Declaration{Contract: Contract, ModelID: "m", Version: 1, Rules: []Rule{broken}}).Validate(); err == nil {
			t.Fatal("incomplete parentFreeze accepted", broken.ID)
		}
	}
	if _, err := Decode(raw); err != nil {
		t.Fatal(err)
	}
}

func TestParentFreezeProtectsChildrenUnderFrozenParents(t *testing.T) {
	registration := Registration{Declaration: Declaration{Contract: Contract, ModelID: "m", Version: 1, Rules: []Rule{parentFreezeRule()}}}
	before := freezeImages()
	if err := registration.Validate(before, clone(before)); err != nil {
		t.Fatal(err)
	}
	reject := func(name string, mutate func(images Images)) {
		t.Helper()
		after := clone(before)
		mutate(after)
		if err := registration.Validate(before, after); err == nil || err.Error() != "transaction.parent_frozen" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	reject("core field edited", func(after Images) { after["sources"]["c1"]["quantity"] = "6" })
	reject("deleted", func(after Images) { delete(after["sources"], "c1") })
	reject("moved out", func(after Images) { after["sources"]["c1"]["owner_line"] = "l2" })
	reject("inserted under frozen parent", func(after Images) {
		after["sources"]["c2"] = map[string]any{"owner_line": "l1", "quantity": "1", "scope": "o1"}
	})
	reject("moved in from unfrozen parent", func(after Images) {
		after["sources"]["c3"] = map[string]any{"owner_line": "l1", "quantity": "1", "scope": "o1"}
	})
	reject("intermediate ancestor rebound", func(after Images) { after["lines"]["l1"]["document"] = "d2" })
	if err := func() error {
		after := clone(before)
		after["documents"]["d1"] = nil // parent deletion dangles the child path
		return registration.Validate(before, after)
	}(); err == nil || err.Error() != "transaction.reference_violation" {
		t.Fatalf("parent deletion: %v", err)
	}
	// Extension fields outside the declared freeze set remain editable.
	after := clone(before)
	after["sources"]["c1"]["note"] = "renamed"
	if err := registration.Validate(before, after); err != nil {
		t.Fatal(err)
	}
	// A parent created and confirmed inside the same transaction accepts final child facts.
	after = clone(before)
	after["documents"]["dNew"] = map[string]any{"phase": "delivery_confirmed"}
	after["lines"]["l3"] = map[string]any{"document": "dNew"}
	after["sources"]["c4"] = map[string]any{"owner_line": "l3", "quantity": "7", "scope": "o1"}
	if err := registration.Validate(before, after); err != nil {
		t.Fatal(err)
	}
}

func clone(images Images) Images {
	result := Images{}
	for entity, rows := range images {
		result[entity] = map[string]map[string]any{}
		for id, row := range rows {
			copied := map[string]any{}
			for key, value := range row {
				copied[key] = value
			}
			result[entity][id] = copied
		}
	}
	return result
}

func reservationRule() Rule {
	return Rule{ID: "capacity", Kind: "reservationBalance", Entity: "sources", Reservation: &ReservationSpec{
		Obligation:        ReservationObligation{Entity: "obligations", Quantity: "quantity"},
		Scope:             "scope",
		Quantity:          "quantity",
		Purpose:           ReservationFieldRef{Path: []RelationStep{{Field: "owner_line", Target: "lines"}}, Field: "purpose"},
		NormalPurpose:     "normal",
		Phase:             ReservationPhase{Path: []RelationStep{{Field: "owner_line", Target: "lines"}, {Field: "document", Target: "documents"}}, Field: "phase", InstructionDraft: "instruction_draft", InstructionOpen: "instruction_open", InstructionClosed: "instruction_closed", InstructionTransferred: "instruction_transferred", DeliveryDraft: "delivery_draft", DeliveryConfirmed: "delivery_confirmed"},
		InstructionSource: "instruction_source",
		TransferredFrom:   "transferred_from",
	}}
}

func reservationDeclaration(rule Rule) Declaration {
	return Declaration{Contract: Contract, ModelID: "m", Version: 1,
		Decimals: []Decimal{
			{Entity: "sources", Field: "quantity", Format: freezeFormat, Sign: "nonnegative"},
			{Entity: "obligations", Field: "quantity", Format: freezeFormat, Sign: "nonnegative"},
		},
		Rules: []Rule{rule}}
}

func reservationImages() Images {
	return Images{
		"obligations": {"o1": {"quantity": "10"}},
		"documents": {"di": {"phase": "instruction_open"}, "dg": {"phase": "instruction_open"}, "da": {"phase": "instruction_open"}, "dd": {"phase": "delivery_confirmed"},
			"ddraft": {"phase": "delivery_draft"}},
		"lines": {
			"li":      {"purpose": "normal", "document": "di"},
			"lg":      {"purpose": "gift", "document": "dg"},
			"la":      {"purpose": "afterSales", "document": "da"},
			"ld":      {"purpose": "normal", "document": "dd"},
			"ldg":     {"purpose": "gift", "document": "dd"},
			"lda":     {"purpose": "afterSales", "document": "dd"},
			"ldraft":  {"purpose": "normal", "document": "ddraft"},
			"lrefund": {"purpose": "refund", "document": "dd"},
		},
		"sources": {
			"h": {"owner_line": "li", "quantity": "10", "scope": "o1"},
			"g": {"owner_line": "lg", "quantity": "10", "scope": "o1"},
			"s": {"owner_line": "la", "quantity": "10", "scope": "o1"},
			"a": {"owner_line": "ld", "quantity": "8", "scope": "o1", "instruction_source": "h", "purpose": "normal"},
		},
	}
}

func TestReservationBalanceClosedDeclarationAndQuantities(t *testing.T) {
	if err := reservationDeclaration(reservationRule()).Validate(); err != nil {
		t.Fatal(err)
	}
	nullable := reservationDeclaration(reservationRule())
	nullable.Decimals[0].Nullable = true
	if err := nullable.Validate(); err == nil {
		t.Fatal("nullable reservation quantity accepted")
	}
	signed := reservationDeclaration(reservationRule())
	signed.Decimals[0].Sign = ""
	if err := signed.Validate(); err == nil {
		t.Fatal("unconstrained reservation quantity accepted")
	}
	// Existing formal declarations may keep the stricter positive sign; zero and
	// negative values are still rejected by the decimal layer itself.
	positive := reservationDeclaration(reservationRule())
	positive.Decimals[0].Sign = "positive"
	positive.Decimals[1].Sign = "positive"
	if err := positive.Validate(); err != nil {
		t.Fatal(err)
	}
	registration := Registration{Declaration: positive}
	zeroed := clone(reservationImages())
	zeroed["sources"]["h"]["quantity"] = "0"
	if err := registration.Validate(nil, zeroed); err == nil || err.Error() != "transaction.decimal_sign_violation" {
		t.Fatalf("positive zero quantity: %v", err)
	}
	negative := clone(reservationImages())
	negative["sources"]["h"]["quantity"] = "-1"
	if err := registration.Validate(nil, negative); err == nil {
		t.Fatal("negative quantity accepted")
	}
	scaled := reservationDeclaration(reservationRule())
	scaled.Decimals[1].Format.Scale = 2
	if err := scaled.Validate(); err == nil {
		t.Fatal("mixed reservation quantity formats accepted")
	}
	undeclared := reservationDeclaration(reservationRule())
	undeclared.Decimals = undeclared.Decimals[:1]
	if err := undeclared.Validate(); err == nil {
		t.Fatal("missing obligation quantity format accepted")
	}
	balance := Declaration{Contract: Contract, ModelID: "m", Version: 1,
		Decimals: []Decimal{{Entity: "lines", Field: "amount", Format: freezeFormat, Nullable: true}, {Entity: "heads", Field: "limit", Format: freezeFormat}},
		Rules:    []Rule{{ID: "b", Kind: "balance", Entity: "lines", Target: "heads", Relation: "head", Amount: "amount", Limit: "limit"}}}
	if err := balance.Validate(); err == nil {
		t.Fatal("nullable balance amount accepted")
	}
}

func TestNullableDecimalAcceptsOnlyExplicitNull(t *testing.T) {
	declaration := Declaration{Contract: Contract, ModelID: "m", Version: 1,
		Decimals: []Decimal{{Entity: "prices", Field: "value", Format: exactdecimal.Format{Storage: "decimalText", Scale: 2, Digits: 8}, Nullable: true},
			{Entity: "prices", Field: "strict", Format: exactdecimal.Format{Storage: "decimalText", Scale: 2, Digits: 8}}},
		Rules: []Rule{{ID: "u", Kind: "unique", Entity: "other", Fields: []string{"code"}}}}
	registration := Registration{Declaration: declaration}
	images := Images{"other": {"x": {"code": "1"}}, "prices": {"p": {"value": nil, "strict": "1.00"}}}
	if err := registration.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	images["prices"]["p"]["value"] = ""
	if err := registration.Validate(nil, images); err == nil {
		t.Fatal("empty string treated as null")
	}
	images["prices"]["p"]["value"] = 1.25
	if err := registration.Validate(nil, images); err == nil {
		t.Fatal("wrong type treated as null")
	}
	images["prices"]["p"]["value"] = nil
	images["prices"]["p"]["strict"] = nil
	if err := registration.Validate(nil, images); err == nil {
		t.Fatal("null accepted for a non-nullable decimal")
	}
}

func TestReservationBalanceInstructionBudgetFlows(t *testing.T) {
	registration := Registration{Declaration: reservationDeclaration(reservationRule())}
	before := reservationImages()
	// Gift and afterSales instructions run their own X ledger next to a normal
	// quota that is already fully occupied (E=8 + open Rh=2 = 10).
	if err := registration.Validate(before, clone(before)); err != nil {
		t.Fatal(err)
	}
	// 10 -> 8 leaves an open remainder of 2 against the same obligation.
	after := clone(before)
	after["sources"]["b"] = map[string]any{"owner_line": "ld", "quantity": "2", "scope": "o1", "instruction_source": "h"}
	if err := registration.Validate(before, after); err != nil {
		t.Fatal(err)
	}
	// 8 + 3 exceeds the immutable instruction quantity: consumption is rejected.
	after["sources"]["b"]["quantity"] = "3"
	if err := registration.Validate(before, after); err == nil || err.Error() != "transaction.reservation_exhausted" {
		t.Fatalf("over-consumption: %v", err)
	}
	// Gift execution follows the same ledger: 10 -> 8 works, 8 + 3 exceeds.
	after = clone(before)
	after["sources"]["bg"] = map[string]any{"owner_line": "ldg", "quantity": "8", "scope": "o1", "instruction_source": "g"}
	if err := registration.Validate(before, after); err != nil {
		t.Fatal("gift execution must not consume the normal quota", err)
	}
	after["sources"]["bg"]["quantity"] = "11"
	if err := registration.Validate(before, after); err == nil || err.Error() != "transaction.reservation_exhausted" {
		t.Fatalf("gift over-consumption: %v", err)
	}
	// Cross-purpose consumption is rejected in both directions and between
	// non-normal purposes; a forged purpose copy on the source row is ignored.
	for name, row := range map[string]map[string]any{
		"refund consumes normal":   {"owner_line": "lrefund", "quantity": "2", "scope": "o1", "instruction_source": "h", "purpose": "normal"},
		"normal consumes gift":     {"owner_line": "ld", "quantity": "2", "scope": "o1", "instruction_source": "g"},
		"gift consumes normal":     {"owner_line": "ldg", "quantity": "2", "scope": "o1", "instruction_source": "h"},
		"gift consumes afterSales": {"owner_line": "ldg", "quantity": "2", "scope": "o1", "instruction_source": "s"},
	} {
		invalid := clone(before)
		invalid["sources"]["x"] = row
		if err := registration.Validate(before, invalid); err == nil || err.Error() != "transaction.reservation_invalid" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Over-issuing the real line stays possible; only internal allocation is bounded.
	after = clone(before)
	after["sources"]["a"]["quantity"] = "10"
	if err := registration.Validate(before, after); err != nil {
		t.Fatal(err)
	}
	// A salesReturn records a direct fact without any instruction: it consumes
	// nothing and cannot reduce the recorded normal execution.
	after = clone(before)
	after["sources"]["r"] = map[string]any{"owner_line": "lrefund", "quantity": "5", "scope": "o1"}
	if err := registration.Validate(before, after); err != nil {
		t.Fatal("direct refund fact rejected", err)
	}
	// Flipping the authoritative purpose of a confirmed consuming line would
	// release recorded execution; the reservation rejects it without parentFreeze.
	after = clone(before)
	after["lines"]["ld"]["purpose"] = "refund"
	if err := registration.Validate(before, after); err == nil {
		t.Fatal("consuming line purpose flip released execution")
	}
	// Deleting a confirmed gift consumer is equally blocked.
	after = clone(before)
	after["sources"]["bg"] = map[string]any{"owner_line": "ldg", "quantity": "8", "scope": "o1", "instruction_source": "g"}
	deleted := clone(after)
	delete(deleted["sources"], "bg")
	if err := registration.Validate(after, deleted); err == nil {
		t.Fatal("confirmed gift consumer deleted")
	}
	// New confirmed facts cannot consume instructions already closed before,
	// for the normal and the gift purpose alike.
	alreadyClosed := clone(before)
	alreadyClosed["documents"]["di"]["phase"] = "instruction_closed"
	alreadyClosed["documents"]["dg"]["phase"] = "instruction_closed"
	if err := registration.Validate(alreadyClosed, clone(alreadyClosed)); err != nil {
		t.Fatal(err)
	}
	consumed := clone(alreadyClosed)
	consumed["sources"]["b"] = map[string]any{"owner_line": "ld", "quantity": "2", "scope": "o1", "instruction_source": "h"}
	if err := registration.Validate(alreadyClosed, consumed); err == nil {
		t.Fatal("new consumption of a before-closed normal instruction accepted")
	}
	consumed = clone(alreadyClosed)
	consumed["sources"]["bg"] = map[string]any{"owner_line": "ldg", "quantity": "2", "scope": "o1", "instruction_source": "g"}
	if err := registration.Validate(alreadyClosed, consumed); err == nil {
		t.Fatal("new consumption of a before-closed gift instruction accepted")
	}
	// Same-transaction partial confirmation then close of the original stays legal.
	after = clone(before)
	after["documents"]["di"]["phase"] = "instruction_closed"
	after["sources"]["b"] = map[string]any{"owner_line": "ld", "quantity": "2", "scope": "o1", "instruction_source": "h"}
	if err := registration.Validate(before, after); err != nil {
		t.Fatal("same-transaction partial confirmation then close must stay legal", err)
	}
}

func TestReservationBalanceTransferChains(t *testing.T) {
	registration := Registration{Declaration: reservationDeclaration(reservationRule())}
	before := reservationImages()
	transfer := func(remainder string) Images {
		images := clone(before)
		images["documents"]["di"]["phase"] = "instruction_transferred"
		images["documents"]["di2"] = map[string]any{"phase": "instruction_open"}
		images["lines"]["li2"] = map[string]any{"purpose": "normal", "document": "di2"}
		if remainder != "" {
			images["sources"]["t"] = map[string]any{"owner_line": "li2", "quantity": remainder, "scope": "o1", "transferred_from": "h"}
		}
		return images
	}
	if err := registration.Validate(before, transfer("2")); err != nil {
		t.Fatal(err)
	}
	// An unexecuted instruction transfers its whole quantity without any panic.
	full := clone(before)
	delete(full["sources"], "a")
	fullTransfer := clone(full)
	fullTransfer["documents"]["di"]["phase"] = "instruction_transferred"
	fullTransfer["documents"]["di2"] = map[string]any{"phase": "instruction_open"}
	fullTransfer["lines"]["li2"] = map[string]any{"purpose": "normal", "document": "di2"}
	fullTransfer["sources"]["t"] = map[string]any{"owner_line": "li2", "quantity": "10", "scope": "o1", "transferred_from": "h"}
	if err := registration.Validate(full, fullTransfer); err != nil {
		t.Fatal(err)
	}
	for name, images := range map[string]Images{
		"wrong quantity": transfer("3"),
	} {
		if err := registration.Validate(before, images); err == nil {
			t.Fatal(name, "accepted")
		}
	}
	duplicate := transfer("2")
	duplicate["sources"]["t2"] = map[string]any{"owner_line": "li2", "quantity": "2", "scope": "o1", "transferred_from": "h"}
	if err := registration.Validate(before, duplicate); err == nil {
		t.Fatal("duplicate transfer accepted")
	}
	self := transfer("2")
	self["sources"]["t"]["transferred_from"] = "t"
	if err := registration.Validate(before, self); err == nil {
		t.Fatal("self reference accepted")
	}
	cycle := transfer("2")
	cycle["sources"]["h"]["transferred_from"] = "t"
	if err := registration.Validate(before, cycle); err == nil {
		t.Fatal("cycle accepted")
	}
	scope := transfer("2")
	scope["sources"]["t"]["scope"] = "o2"
	scope["obligations"]["o2"] = map[string]any{"quantity": "10"}
	if err := registration.Validate(before, scope); err == nil {
		t.Fatal("cross-scope transfer accepted")
	}
	crossPurpose := transfer("2")
	crossPurpose["lines"]["li2"]["purpose"] = "gift"
	if err := registration.Validate(before, crossPurpose); err == nil {
		t.Fatal("cross-purpose transfer accepted")
	}
	zero := transfer("0")
	zero["sources"]["a"]["quantity"] = "10"
	if err := registration.Validate(before, zero); err == nil {
		t.Fatal("zero-remainder transfer row accepted")
	}
	// A historical carrier may later close: closing the transfer-in document
	// simply forfeits its remaining reservation.
	carried := transfer("2")
	closed := clone(carried)
	closed["documents"]["di2"]["phase"] = "instruction_closed"
	if err := registration.Validate(carried, closed); err != nil {
		t.Fatal("closing a historical transfer carrier rejected", err)
	}
	// A carrier that has executed part of its quantity may transfer the exact
	// remaining part onward in a later transaction.
	partial := clone(carried)
	partial["sources"]["c"] = map[string]any{"owner_line": "ld", "quantity": "1", "scope": "o1", "instruction_source": "t"}
	chain := clone(partial)
	chain["documents"]["di2"]["phase"] = "instruction_transferred"
	chain["documents"]["di3"] = map[string]any{"phase": "instruction_open"}
	chain["lines"]["li3"] = map[string]any{"purpose": "normal", "document": "di3"}
	chain["sources"]["t2"] = map[string]any{"owner_line": "li3", "quantity": "1", "scope": "o1", "transferred_from": "t"}
	if err := registration.Validate(partial, chain); err != nil {
		t.Fatal("chained re-transfer of the executed carrier rejected", err)
	}
}

func TestReservationDraftInstructionOpensWithConsumption(t *testing.T) {
	registration := Registration{Declaration: reservationDeclaration(reservationRule())}
	for _, purpose := range []string{"normal", "gift", "afterSales"} {
		for _, existingActual := range []bool{false, true} {
			name := purpose + map[bool]string{false: "/new-actual", true: "/confirm-draft"}[existingActual]
			t.Run(name, func(t *testing.T) {
				before := reservationImages()
				delete(before["sources"], "a")
				before["documents"]["di"]["phase"] = "instruction_draft"
				before["lines"]["li"]["purpose"] = purpose
				owner := "ld"
				if existingActual {
					owner = "ldraft"
					before["sources"]["a"] = map[string]any{"owner_line": owner, "scope": "o1", "quantity": "8"}
				}
				before["lines"][owner]["purpose"] = purpose
				if err := registration.Validate(nil, before); err != nil {
					t.Fatalf("illegal fixture before image: %v", err)
				}
				after := clone(before)
				after["documents"]["di"]["phase"] = "instruction_open"
				if existingActual {
					after["documents"]["ddraft"]["phase"] = "delivery_confirmed"
				}
				after["sources"]["a"] = map[string]any{"owner_line": owner, "scope": "o1", "quantity": "8", "instruction_source": "h"}
				if err := registration.Validate(before, after); err != nil {
					t.Fatalf("same-transaction issue/consume: %v", err)
				}
				if err := registration.Validate(nil, after); err != nil {
					t.Fatalf("new open instruction and consumption control: %v", err)
				}
				for _, phase := range []string{"instruction_draft", "instruction_closed", "instruction_transferred"} {
					invalid := clone(after)
					invalid["documents"]["di"]["phase"] = phase
					if err := registration.Validate(before, invalid); err == nil {
						t.Fatalf("draft final %s consumed", phase)
					}
				}
				over := clone(after)
				over["sources"]["a"]["quantity"] = "11"
				if err := registration.Validate(before, over); err == nil || err.Error() != "transaction.reservation_exhausted" {
					t.Fatalf("over-consumption: %v", err)
				}
				cross := clone(after)
				otherPurpose := "normal"
				if purpose == otherPurpose {
					otherPurpose = "gift"
				}
				cross["lines"][owner]["purpose"] = otherPurpose
				if err := registration.Validate(before, cross); err == nil || err.Error() != "transaction.reservation_invalid" {
					t.Fatalf("cross-purpose: %v", err)
				}
			})
		}
	}
}

func TestReservationFinishedInstructionCannotConsumeOrReopen(t *testing.T) {
	registration := Registration{Declaration: reservationDeclaration(reservationRule())}
	for _, phase := range []string{"instruction_closed", "instruction_transferred"} {
		t.Run(phase, func(t *testing.T) {
			before := reservationImages()
			before["documents"]["di"]["phase"] = phase
			if phase == "instruction_transferred" {
				before["documents"]["dt"] = map[string]any{"phase": "instruction_open"}
				before["lines"]["lt"] = map[string]any{"purpose": "normal", "document": "dt"}
				before["sources"]["t"] = map[string]any{"quantity": "2", "scope": "o1", "owner_line": "lt", "transferred_from": "h"}
			}
			if err := registration.Validate(nil, before); err != nil {
				t.Fatalf("illegal fixture: %v", err)
			}
			after := clone(before)
			after["sources"]["b"] = map[string]any{"owner_line": "ld", "quantity": "1", "scope": "o1", "instruction_source": "h"}
			if err := registration.Validate(before, after); err == nil || err.Error() != "transaction.reservation_invalid" {
				t.Fatalf("new consumption: %v", err)
			}
			after = clone(before)
			after["documents"]["di"]["phase"] = "instruction_open"
			if err := registration.Validate(before, after); err == nil || err.Error() != "transaction.reservation_frozen" {
				t.Fatalf("reopened: %v", err)
			}
		})
	}
}

func TestReservationBalancePurposeCapacitySplit(t *testing.T) {
	registration := Registration{Declaration: reservationDeclaration(reservationRule())}
	// Three same-size instructions; only the normal one occupies obligation 10.
	base := reservationImages()
	delete(base["sources"], "a")
	executed := clone(base)
	executed["sources"]["a"] = map[string]any{"owner_line": "ld", "quantity": "8", "scope": "o1", "instruction_source": "h"}
	executed["sources"]["ag"] = map[string]any{"owner_line": "ldg", "quantity": "8", "scope": "o1", "instruction_source": "g"}
	executed["sources"]["as"] = map[string]any{"owner_line": "lda", "quantity": "8", "scope": "o1", "instruction_source": "s"}
	if err := registration.Validate(base, executed); err != nil {
		t.Fatal("per-purpose execution against one obligation", err)
	}
	// The same order transfers each remaining 2 units to a same-purpose carrier;
	// only the normal carrier keeps occupying the normal capacity.
	transferred := clone(executed)
	transferred["documents"]["di"]["phase"] = "instruction_transferred"
	transferred["documents"]["dg"]["phase"] = "instruction_transferred"
	transferred["documents"]["da"]["phase"] = "instruction_transferred"
	transferred["documents"]["di2"] = map[string]any{"phase": "instruction_open"}
	transferred["documents"]["dg2"] = map[string]any{"phase": "instruction_open"}
	transferred["documents"]["da2"] = map[string]any{"phase": "instruction_open"}
	transferred["lines"]["li2"] = map[string]any{"purpose": "normal", "document": "di2"}
	transferred["lines"]["lg2"] = map[string]any{"purpose": "gift", "document": "dg2"}
	transferred["lines"]["la2"] = map[string]any{"purpose": "afterSales", "document": "da2"}
	transferred["sources"]["t"] = map[string]any{"owner_line": "li2", "quantity": "2", "scope": "o1", "transferred_from": "h"}
	transferred["sources"]["tg"] = map[string]any{"owner_line": "lg2", "quantity": "2", "scope": "o1", "transferred_from": "g"}
	transferred["sources"]["ta"] = map[string]any{"owner_line": "la2", "quantity": "2", "scope": "o1", "transferred_from": "s"}
	if err := registration.Validate(executed, transferred); err != nil {
		t.Fatal("per-purpose remainder transfer", err)
	}
	// Confirming the final 2 units of every purpose completes the order while
	// the normal obligation stays exactly at its 10-unit limit.
	final := clone(transferred)
	final["sources"]["f"] = map[string]any{"owner_line": "ld", "quantity": "2", "scope": "o1", "instruction_source": "t"}
	final["sources"]["fg"] = map[string]any{"owner_line": "ldg", "quantity": "2", "scope": "o1", "instruction_source": "tg"}
	final["sources"]["fa"] = map[string]any{"owner_line": "lda", "quantity": "2", "scope": "o1", "instruction_source": "ta"}
	if err := registration.Validate(transferred, final); err != nil {
		t.Fatal("per-purpose completion exceeded the normal obligation", err)
	}
	// One more normal unit cannot be found anywhere; gift capacity is separate.
	over := clone(final)
	over["sources"]["extra"] = map[string]any{"owner_line": "ld", "quantity": "1", "scope": "o1", "instruction_source": "t"}
	if err := registration.Validate(final, over); err == nil || err.Error() != "transaction.reservation_exhausted" {
		t.Fatalf("normal quota exceeded after split: %v", err)
	}
}

func reservationPlanRule() Rule {
	rule := reservationRule()
	rule.Reservation.PlanItem = "plan_item"
	rule.Reservation.Plans = &ReservationPlans{
		Entity:      "plan_items",
		Quantity:    "quantity",
		Scope:       "scope",
		LogicalItem: ReservationLogical{Field: "logical_item", Target: "logical_items"},
		Share:       "share",
		CurrentWhen: PathMatch{
			Left:  []RelationStep{{Field: "plan_version", Target: "plan_versions"}},
			Right: []RelationStep{{Field: "plan_version", Target: "plan_versions"}, {Field: "series", Target: "series"}, {Field: "current_version", Target: "plan_versions"}},
		},
	}
	return rule
}

func reservationPlanImages() Images {
	images := reservationImages()
	images["series"] = map[string]map[string]any{"s1": {"current_version": "v1"}}
	images["plan_versions"] = map[string]map[string]any{"v1": {"series": "s1"}, "v2": {"series": "s1"}}
	images["logical_items"] = map[string]map[string]any{"L1": {"label": "l"}, "L2": {"label": "m"}}
	images["plan_items"] = map[string]map[string]any{
		"p1": {"plan_version": "v1", "logical_item": "L1", "share": float64(1), "scope": "o1", "quantity": "6"},
	}
	images["sources"]["h"]["plan_item"] = "p1"
	images["sources"]["h"]["quantity"] = "2"
	images["sources"]["a"]["plan_item"] = "p1"
	images["sources"]["a"]["quantity"] = "4"
	delete(images["sources"]["a"], "instruction_source")
	return images
}

func planDeclaration() Declaration {
	declaration := reservationDeclaration(reservationPlanRule())
	declaration.Decimals = append(declaration.Decimals, Decimal{Entity: "plan_items", Field: "quantity", Format: freezeFormat, Sign: "nonnegative"})
	return declaration
}

func TestReservationBalanceStablePlanBuckets(t *testing.T) {
	registration := Registration{Declaration: planDeclaration()}
	before := reservationPlanImages()
	if err := registration.Validate(before, clone(before)); err != nil {
		t.Fatal(err)
	}
	// Bucket usage E + open remainder may not exceed the current plan quantity.
	after := clone(before)
	after["sources"]["h3"] = map[string]any{"owner_line": "li", "quantity": "3", "scope": "o1", "plan_item": "p1"}
	if err := registration.Validate(before, after); err == nil || err.Error() != "transaction.reservation_exceeded" {
		t.Fatalf("bucket overflow: %v", err)
	}
	// Planned buckets and direct sources compete for the same obligation capacity.
	after = clone(before)
	after["sources"]["direct"] = map[string]any{"owner_line": "ld", "quantity": "5", "scope": "o1"}
	if err := registration.Validate(before, after); err == nil || err.Error() != "transaction.reservation_exceeded" {
		t.Fatalf("direct vs planned: %v", err)
	}
	after["sources"]["direct"]["quantity"] = "4"
	if err := registration.Validate(before, after); err != nil {
		t.Fatal(err)
	}
	// Replacing the current version must carry executed stable shares forward.
	switchVersion := func(v2Items ...map[string]any) Images {
		images := clone(before)
		images["series"]["s1"]["current_version"] = "v2"
		for i, item := range v2Items {
			images["plan_items"][string(rune('q'+i))] = item
		}
		return images
	}
	if err := registration.Validate(before, switchVersion(map[string]any{"plan_version": "v2", "logical_item": "L1", "share": float64(1), "scope": "o1", "quantity": "6"})); err != nil {
		t.Fatal(err)
	}
	if err := registration.Validate(before, switchVersion()); err == nil || err.Error() != "transaction.reservation_plan_required" {
		t.Fatalf("dropped executed share: %v", err)
	}
	if err := registration.Validate(before, switchVersion(map[string]any{"plan_version": "v2", "logical_item": "L1", "share": float64(1), "scope": "o1", "quantity": "3"})); err == nil {
		t.Fatal("shrunk executed share accepted")
	}
	// Closing a series without any execution keeps the reservation bounded.
	images := reservationPlanImages()
	delete(images["sources"], "a")
	images["sources"]["h"]["quantity"] = "0"
	after = clone(images)
	after["series"]["s1"]["current_version"] = ""
	if err := registration.Validate(images, after); err != nil {
		t.Fatal(err)
	}
}
