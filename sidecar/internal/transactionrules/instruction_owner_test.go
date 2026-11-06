package transactionrules

import "testing"

func TestReservationConsumptionUsesPriorPhaseOwner(t *testing.T) {
	registration := Registration{Declaration: reservationDeclaration(reservationRule())}
	for _, purpose := range []string{"normal", "gift", "afterSales"} {
		for _, newLine := range []bool{false, true} {
			lineMode := "existing-line"
			if newLine {
				lineMode = "new-line"
			}
			for _, tc := range []struct {
				name, beforePhase, afterPhase string
				allowed                       bool
			}{
				{"closed", "instruction_closed", "instruction_closed", false},
				{"closed-reopened", "instruction_closed", "instruction_open", false},
				{"transferred", "instruction_transferred", "instruction_transferred", false},
				{"transferred-reopened", "instruction_transferred", "instruction_open", false},
				{"actual-owner", "delivery_draft", "instruction_open", false},
				{"open", "instruction_open", "instruction_open", true},
				{"open-closed", "instruction_open", "instruction_closed", true},
				{"draft-open", "instruction_draft", "instruction_open", true},
			} {
				t.Run(purpose+"/"+lineMode+"/"+tc.name, func(t *testing.T) {
					before := reservationImages()
					delete(before["sources"], "h")
					delete(before["sources"], "a")
					before["documents"]["di"]["phase"] = tc.beforePhase
					before["lines"]["ld"]["purpose"] = purpose
					if newLine {
						delete(before["lines"], "li")
					}
					if err := registration.Validate(nil, before); err != nil {
						t.Fatalf("illegal before image: %v", err)
					}
					after := clone(before)
					after["documents"]["di"]["phase"] = tc.afterPhase
					after["lines"]["li"] = map[string]any{"purpose": purpose, "document": "di"}
					after["sources"]["h"] = map[string]any{"quantity": "8", "scope": "o1", "owner_line": "li"}
					after["sources"]["a"] = map[string]any{"quantity": "8", "scope": "o1", "owner_line": "ld", "instruction_source": "h"}
					err := registration.Validate(before, after)
					if tc.allowed {
						if err != nil {
							t.Fatalf("legal owner activation/consumption: %v", err)
						}
					} else if err == nil || err.Error() != "transaction.reservation_invalid" {
						t.Fatalf("new source consumed a previously ineligible owner: %v", err)
					}
					// Registration validates an existing complete historical graph, not a new write.
					if err := registration.Validate(nil, after); err != nil {
						t.Fatalf("historical registration: %v", err)
					}
				})
			}
		}
		for _, phase := range []string{"instruction_open", "instruction_closed", "instruction_transferred"} {
			t.Run(purpose+"/new-owner/"+phase, func(t *testing.T) {
				before := reservationImages()
				delete(before["sources"], "h")
				delete(before["sources"], "a")
				delete(before["lines"], "li")
				delete(before["documents"], "di")
				before["lines"]["ld"]["purpose"] = purpose
				after := clone(before)
				after["documents"]["di"] = map[string]any{"phase": phase}
				after["lines"]["li"] = map[string]any{"purpose": purpose, "document": "di"}
				after["sources"]["h"] = map[string]any{"quantity": "8", "scope": "o1", "owner_line": "li"}
				after["sources"]["a"] = map[string]any{"quantity": "8", "scope": "o1", "owner_line": "ld", "instruction_source": "h"}
				if err := registration.Validate(before, after); err != nil {
					t.Fatalf("same-transaction new owner and consumption: %v", err)
				}
			})
		}
	}
}

func TestReservationConsumptionPhaseOwnerRebinding(t *testing.T) {
	registration := Registration{Declaration: reservationDeclaration(reservationRule())}
	before := reservationImages()
	before["documents"]["old-closed"] = map[string]any{"phase": "instruction_closed"}
	after := clone(before)
	after["lines"]["li"]["document"] = "old-closed"
	after["sources"]["a"]["quantity"] = "9"
	if err := registration.Validate(before, after); err == nil || err.Error() != "transaction.reservation_invalid" {
		t.Fatalf("existing source moved under a closed owner and increased: %v", err)
	}
	// The original source's terminal state remains authoritative even if its new owner is new.
	before["documents"]["di"]["phase"] = "instruction_closed"
	after = clone(before)
	after["documents"]["new-owner"] = map[string]any{"phase": "instruction_closed"}
	after["lines"]["li"]["document"] = "new-owner"
	after["sources"]["a"]["quantity"] = "9"
	if err := registration.Validate(before, after); err == nil || err.Error() != "transaction.reservation_invalid" {
		t.Fatalf("terminal source moved to new owner and increased: %v", err)
	}
	// An existing actual draft cannot turn into an instruction by changing its owner role.
	before = reservationImages()
	delete(before["sources"], "a")
	before["documents"]["di"]["phase"] = "delivery_draft"
	after = clone(before)
	after["documents"]["di"]["phase"] = "instruction_open"
	if err := registration.Validate(before, after); err == nil || err.Error() != "transaction.reservation_frozen" {
		t.Fatalf("existing source role conversion: %v", err)
	}
}
