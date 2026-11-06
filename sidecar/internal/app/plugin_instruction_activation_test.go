package app

import "testing"

func TestInstructionDraftActivationAndConsumptionAtomic(t *testing.T) {
	for _, mode := range []string{"new-actual", "confirm-draft", "new-open"} {
		t.Run(mode, func(t *testing.T) {
			f := newTransactionFixture(t, "delivery")
			seed := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{
				{Entity: "obligations", Kind: "create", TempID: "o", Values: map[string]any{"quantity": "10"}},
				{Entity: "documents", Kind: "create", TempID: "di", Values: map[string]any{"phase": "instruction_draft"}},
				{Entity: "lines", Kind: "create", TempID: "li", Values: map[string]any{"purpose": "normal", "real_qty": 10, "document": regressionRef("documents", "di")}},
				{Entity: "sources", Kind: "create", TempID: "h", Values: map[string]any{"quantity": "10", "scope": regressionRef("obligations", "o"), "owner_line": regressionRef("lines", "li")}},
			}}
			actual := func(quantity, purpose string, scope, instruction any) []transactionOperation {
				return []transactionOperation{
					{Entity: "documents", Kind: "create", TempID: "da", Values: map[string]any{"phase": "delivery_confirmed"}},
					{Entity: "lines", Kind: "create", TempID: "la", Values: map[string]any{"purpose": purpose, "real_qty": 8, "document": regressionRef("documents", "da")}},
					{Entity: "sources", Kind: "create", TempID: "a", Values: map[string]any{"quantity": quantity, "scope": scope, "owner_line": regressionRef("lines", "la"), "instruction_source": instruction}},
				}
			}
			if mode == "new-open" {
				seed.Operations[1].Values["phase"] = "instruction_open"
				seed.Operations = append(seed.Operations, actual("8", "normal", regressionRef("obligations", "o"), regressionRef("sources", "h"))...)
				f.submit(t, seed)
				return
			}
			if mode == "confirm-draft" {
				pending := actual("8", "normal", regressionRef("obligations", "o"), nil)
				pending[0].Values["phase"] = "delivery_draft"
				delete(pending[2].Values, "instruction_source")
				seed.Operations = append(seed.Operations, pending...)
			}
			seeded := f.submit(t, seed)
			ids := seeded.NewIDs
			doc, instruction, scope := ids["documents"]["di"], ids["sources"]["h"], ids["obligations"]["o"]
			_, digest := regressionRow(t, f, "documents", doc)
			plan := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
				{Entity: "documents", Kind: "update", RecordID: doc, ExpectedDigest: &digest, Values: map[string]any{"phase": "instruction_open"}},
			}}
			if mode == "confirm-draft" {
				actualDoc, source := ids["documents"]["da"], ids["sources"]["a"]
				_, actualDigest := regressionRow(t, f, "documents", actualDoc)
				_, sourceDigest := regressionRow(t, f, "sources", source)
				plan.Operations = append(plan.Operations,
					transactionOperation{Entity: "documents", Kind: "update", RecordID: actualDoc, ExpectedDigest: &actualDigest, Values: map[string]any{"phase": "delivery_confirmed"}},
					transactionOperation{Entity: "sources", Kind: "update", RecordID: source, ExpectedDigest: &sourceDigest, Values: map[string]any{"instruction_source": instruction}})
			} else {
				plan.Operations = append(plan.Operations, actual("8", "normal", scope, instruction)...)
			}
			f.submit(t, plan)
			row, _ := regressionRow(t, f, "documents", doc)
			if row["phase"] != "instruction_open" {
				t.Fatal("instruction activation was not durable")
			}
			_, digest = regressionRow(t, f, "documents", doc)
			f.submit(t, transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
				{Entity: "documents", Kind: "update", RecordID: doc, ExpectedDigest: &digest, Values: map[string]any{"phase": "instruction_closed"}},
			}})
			regressionReject(t, f, transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: actual("1", "normal", scope, instruction)}, "transaction.reservation_invalid")
			_, digest = regressionRow(t, f, "documents", doc)
			regressionReject(t, f, transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
				{Entity: "documents", Kind: "update", RecordID: doc, ExpectedDigest: &digest, Values: map[string]any{"phase": "instruction_open"}},
			}}, "transaction.reservation_frozen")
		})
	}
}
