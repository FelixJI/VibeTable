package transactionrules

import "testing"

func TestReservationConfirmedIncrementRequiresOpenInstruction(t *testing.T) {
	registration := Registration{Declaration: reservationDeclaration(reservationRule())}
	for _, purpose := range []string{"normal", "gift", "afterSales"} {
		for _, phase := range []string{"instruction_open", "instruction_closed", "instruction_transferred"} {
			t.Run(purpose+"/"+phase, func(t *testing.T) {
				before := reservationImages()
				before["lines"]["li"]["purpose"] = purpose
				before["lines"]["ld"]["purpose"] = purpose
				before["documents"]["di"]["phase"] = phase
				if phase == "instruction_transferred" {
					before["documents"]["dt"] = map[string]any{"phase": "instruction_open"}
					before["lines"]["lt"] = map[string]any{"purpose": purpose, "document": "dt"}
					before["sources"]["t"] = map[string]any{"quantity": "2", "scope": "o1", "owner_line": "lt", "transferred_from": "h"}
				}
				if err := registration.Validate(nil, before); err != nil {
					t.Fatalf("illegal fixture: %v", err)
				}
				if err := registration.Validate(before, clone(before)); err != nil {
					t.Fatalf("unchanged history: %v", err)
				}
				after := clone(before)
				after["sources"]["a"]["quantity"] = "9"
				err := registration.Validate(before, after)
				if phase != "instruction_open" {
					if err == nil || err.Error() != "transaction.reservation_invalid" {
						t.Fatalf("terminal increment: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("open increment: %v", err)
				}
				after["documents"]["di"]["phase"] = "instruction_closed"
				if err := registration.Validate(before, after); err != nil {
					t.Fatalf("increment then close in one transaction: %v", err)
				}
			})
		}
	}
}
