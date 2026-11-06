package app

import (
	"net/http"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func TestConfirmedIncrementWithoutParentFreeze(t *testing.T) {
	for _, entry := range []string{"plugin", "native"} {
		for _, phase := range []string{"instruction_open", "instruction_closed", "instruction_transferred"} {
			t.Run(entry+"/"+phase, func(t *testing.T) {
				f := newTransactionFixture(t, "delivery-reservation-only")
				if len(f.declaration.Rules) != 1 || f.declaration.Rules[0].Kind != "reservationBalance" {
					t.Fatal("fixture must not mask the reservation guard with parentFreeze")
				}
				seed := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{
					{Entity: "obligations", Kind: "create", TempID: "o", Values: map[string]any{"quantity": "10"}},
					{Entity: "documents", Kind: "create", TempID: "di", Values: map[string]any{"phase": phase}},
					{Entity: "lines", Kind: "create", TempID: "li", Values: map[string]any{"purpose": "normal", "real_qty": 10, "document": regressionRef("documents", "di")}},
					{Entity: "sources", Kind: "create", TempID: "h", Values: map[string]any{"quantity": "10", "scope": regressionRef("obligations", "o"), "owner_line": regressionRef("lines", "li")}},
					{Entity: "documents", Kind: "create", TempID: "da", Values: map[string]any{"phase": "delivery_confirmed"}},
					{Entity: "lines", Kind: "create", TempID: "la", Values: map[string]any{"purpose": "normal", "real_qty": 8, "document": regressionRef("documents", "da")}},
					{Entity: "sources", Kind: "create", TempID: "a", Values: map[string]any{"quantity": "8", "scope": regressionRef("obligations", "o"), "owner_line": regressionRef("lines", "la"), "instruction_source": regressionRef("sources", "h")}},
				}}
				if phase == "instruction_transferred" {
					seed.Operations = append(seed.Operations,
						transactionOperation{Entity: "documents", Kind: "create", TempID: "dt", Values: map[string]any{"phase": "instruction_open"}},
						transactionOperation{Entity: "lines", Kind: "create", TempID: "lt", Values: map[string]any{"purpose": "normal", "real_qty": 2, "document": regressionRef("documents", "dt")}},
						transactionOperation{Entity: "sources", Kind: "create", TempID: "t", Values: map[string]any{"quantity": "2", "scope": regressionRef("obligations", "o"), "owner_line": regressionRef("lines", "lt"), "transferred_from": regressionRef("sources", "h")}})
				}
				ids := f.submit(t, seed).NewIDs
				source := ids["sources"]["a"]
				_, digest := regressionRow(t, f, "sources", source)
				unchanged := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
					{Entity: "sources", Kind: "update", RecordID: source, ExpectedDigest: &digest, Values: map[string]any{"quantity": "8", "note": "history retained"}},
				}}
				f.submit(t, unchanged)
				_, digest = regressionRow(t, f, "sources", source)
				if entry == "plugin" {
					plan := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
						{Entity: "sources", Kind: "update", RecordID: source, ExpectedDigest: &digest, Values: map[string]any{"quantity": "9", "note": "increment"}},
					}}
					if phase != "instruction_open" {
						regressionReject(t, f, plan, "transaction.reservation_invalid")
						return
					}
					doc := ids["documents"]["di"]
					_, docDigest := regressionRow(t, f, "documents", doc)
					plan.Operations = append(plan.Operations, transactionOperation{Entity: "documents", Kind: "update", RecordID: doc, ExpectedDigest: &docDigest, Values: map[string]any{"phase": "instruction_closed"}})
					f.submit(t, plan)
				} else {
					if err := writecoordinator.EnsurePocketBaseReceiptTable(t.Context(), f.pb); err != nil {
						t.Fatal(err)
					}
					binding := f.model.Bindings["sources"]
					plan := map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": binding.TableID, "schemaRevision": f.model.Revisions[binding.TableID], "operations": []any{
						map[string]any{"kind": "update", "primaryKey": source, "expectedDigest": digest, "values": map[string]any{binding.Fields["quantity"]: "9", binding.Fields["note"]: "increment"}},
					}}
					f.input.RequestID = "increment-" + phase
					call := transactionHTTP(t, f, nil)
					if response := call("mutation.preview", plan); response.Code != http.StatusOK {
						t.Fatal(response.Body.String())
					}
					if response := call("audit.start", map[string]any{"taskId": f.input.RequestID, "plan": plan}); response.Code != http.StatusOK {
						t.Fatal(response.Body.String())
					}
					before := transactionBusinessSnapshot(t, f)
					response := call("mutation.commit", plan)
					if phase != "instruction_open" {
						requireHTTPNotCommitted(t, response, "transaction.reservation_invalid")
						if transactionBusinessSnapshot(t, f) != before {
							t.Fatal("rejected increment changed business rows")
						}
						return
					}
					if response.Code != http.StatusOK {
						t.Fatal(response.Body.String())
					}
				}
				row, _ := regressionRow(t, f, "sources", source)
				if row["quantity"] != "9" {
					t.Fatal("open increment not durable", row)
				}
			})
		}
	}
}
