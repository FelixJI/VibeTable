package transactionrules

import "testing"

func historicalTransferImages(retransferred bool) Images {
	images := reservationImages()
	images["documents"]["di"]["phase"] = "instruction_transferred"
	images["documents"]["dt"] = map[string]any{"phase": "instruction_closed"}
	images["lines"]["lt"] = map[string]any{"purpose": "normal", "document": "dt"}
	images["sources"]["t"] = map[string]any{"quantity": "2", "scope": "o1", "owner_line": "lt", "transferred_from": "h"}
	if retransferred {
		images["documents"]["dt"]["phase"] = "instruction_transferred"
		images["documents"]["du"] = map[string]any{"phase": "instruction_open"}
		images["lines"]["lu"] = map[string]any{"purpose": "normal", "document": "du"}
		images["sources"]["u"] = map[string]any{"quantity": "2", "scope": "o1", "owner_line": "lu", "transferred_from": "t"}
	}
	return images
}

func TestReservationHistoricalTransferAdmission(t *testing.T) {
	registration := Registration{Declaration: reservationDeclaration(reservationRule())}
	for _, retransferred := range []bool{false, true} {
		name := "closed"
		if retransferred {
			name = "retransferred"
		}
		t.Run(name, func(t *testing.T) {
			history := historicalTransferImages(retransferred)
			if err := registration.Validate(nil, history); err != nil {
				t.Fatalf("valid historical registration: %v", err)
			}
			write := clone(history)
			write["sources"]["t"]["note"] = "ordinary edit"
			if err := registration.Validate(history, write); err != nil {
				t.Fatalf("ordinary historical write: %v", err)
			}
			for _, before := range []Images{reservationImages(), {}} {
				if err := registration.Validate(before, history); err == nil || err.Error() != "transaction.reservation_transfer_invalid" {
					t.Fatalf("new transfer accepted a terminal recipient: %v", err)
				}
			}
		})
	}
}

func TestReservationHistoricalTransferStillValidatesFullGraph(t *testing.T) {
	registration := Registration{Declaration: reservationDeclaration(reservationRule())}
	for _, defect := range []string{"quantity", "missing", "duplicate", "self", "cycle", "scope", "purpose", "actual-recipient", "zero-remainder"} {
		t.Run(defect, func(t *testing.T) {
			images := historicalTransferImages(false)
			switch defect {
			case "quantity":
				images["sources"]["t"]["quantity"] = "3"
			case "missing":
				delete(images["sources"], "t")
			case "duplicate":
				images["sources"]["other"] = map[string]any{"quantity": "2", "scope": "o1", "owner_line": "lt", "transferred_from": "h"}
			case "self":
				images["sources"]["t"]["transferred_from"] = "t"
			case "cycle":
				images["documents"]["dt"]["phase"] = "instruction_transferred"
				images["sources"]["h"]["transferred_from"] = "t"
			case "scope":
				images["obligations"]["o2"] = map[string]any{"quantity": "10"}
				images["sources"]["t"]["scope"] = "o2"
			case "purpose":
				images["lines"]["lt"]["purpose"] = "gift"
			case "actual-recipient":
				images["sources"]["t"]["owner_line"] = "ld"
			case "zero-remainder":
				images["sources"]["h"]["quantity"] = "8"
			}
			if err := registration.Validate(nil, images); err == nil || err.Error() != "transaction.reservation_transfer_invalid" {
				t.Fatalf("invalid historical graph: %v", err)
			}
		})
	}
}
