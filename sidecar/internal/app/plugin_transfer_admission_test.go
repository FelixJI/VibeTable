package app

import (
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
)

func TestHistoricalTransferRuleUpgrade(t *testing.T) {
	for _, mode := range []string{"closed", "retransferred"} {
		t.Run(mode, func(t *testing.T) {
			f := newTransactionFixture(t, "delivery-reservation-only")
			seed := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{
				{Entity: "obligations", Kind: "create", TempID: "o", Values: map[string]any{"quantity": "10"}},
				{Entity: "documents", Kind: "create", TempID: "di", Values: map[string]any{"phase": "instruction_open"}},
				{Entity: "lines", Kind: "create", TempID: "li", Values: map[string]any{"purpose": "normal", "real_qty": 10, "document": regressionRef("documents", "di")}},
				{Entity: "sources", Kind: "create", TempID: "h", Values: map[string]any{"quantity": "10", "scope": regressionRef("obligations", "o"), "owner_line": regressionRef("lines", "li")}},
				{Entity: "documents", Kind: "create", TempID: "da", Values: map[string]any{"phase": "delivery_confirmed"}},
				{Entity: "lines", Kind: "create", TempID: "la", Values: map[string]any{"purpose": "normal", "real_qty": 8, "document": regressionRef("documents", "da")}},
				{Entity: "sources", Kind: "create", TempID: "a", Values: map[string]any{"quantity": "8", "scope": regressionRef("obligations", "o"), "owner_line": regressionRef("lines", "la"), "instruction_source": regressionRef("sources", "h")}},
			}}
			ids := f.submit(t, seed).NewIDs
			transfer := func(source, document, suffix, quantity string) transactionResult {
				_, digest := regressionRow(t, f, "documents", document)
				return f.submit(t, transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
					{Entity: "documents", Kind: "update", RecordID: document, ExpectedDigest: &digest, Values: map[string]any{"phase": "instruction_transferred"}},
					{Entity: "documents", Kind: "create", TempID: suffix, Values: map[string]any{"phase": "instruction_open"}},
					{Entity: "lines", Kind: "create", TempID: suffix, Values: map[string]any{"purpose": "normal", "real_qty": 2, "document": regressionRef("documents", suffix)}},
					{Entity: "sources", Kind: "create", TempID: suffix, Values: map[string]any{"quantity": quantity, "scope": ids["obligations"]["o"], "owner_line": regressionRef("lines", suffix), "transferred_from": source}},
				}})
			}
			moved := transfer(ids["sources"]["h"], ids["documents"]["di"], "t", "2")
			carrier, doc := moved.NewIDs["sources"]["t"], moved.NewIDs["documents"]["t"]
			if mode == "retransferred" {
				transfer(carrier, doc, "u", "2")
			} else {
				_, digest := regressionRow(t, f, "documents", doc)
				f.submit(t, transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
					{Entity: "documents", Kind: "update", RecordID: doc, ExpectedDigest: &digest, Values: map[string]any{"phase": "instruction_closed"}},
				}})
			}
			declaration := f.declaration
			declaration.Version++
			f.prepareUpgradedRules(t, declaration)
			before := transactionBusinessSnapshot(t, f)
			if _, err := f.call("transaction.configure", map[string]any{"declaration": declaration}); err != nil {
				t.Fatalf("valid historical transfer upgrade: %v", err)
			}
			if before != transactionBusinessSnapshot(t, f) {
				t.Fatal("registration changed historical business facts")
			}
			f.declaration = declaration
			_, digest := regressionRow(t, f, "sources", carrier)
			f.submit(t, transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
				{Entity: "sources", Kind: "update", RecordID: carrier, ExpectedDigest: &digest, Values: map[string]any{"quantity": "2", "note": "history remains editable"}},
			}})
			binding := f.model.Bindings["sources"]
			_, digest = regressionRow(t, f, "sources", carrier)
			_, err := f.kernel.Apply(t.Context(), mutation.Request{ContractVersion: mutation.ContractVersion,
				RequestID: "native-history-" + mode, IdempotencyKey: "native-history-" + mode,
				TableID: binding.TableID, SchemaRevision: f.model.Revisions[binding.TableID], Actor: mutation.Actor{Type: "user", ID: "local-user"},
				Operations: []mutation.Operation{{Kind: mutation.OperationUpdate, RecordID: &carrier, ExpectedDigest: &digest, Values: map[string]any{binding.Fields["quantity"]: "2", binding.Fields["note"]: "native history edit"}}}})
			if err != nil {
				t.Fatal("ordinary native write after upgrade", err)
			}
			table, err := schemaexecution.Describe(t.Context(), f.pb, binding.TableID)
			if err != nil {
				t.Fatal(err)
			}
			field, _ := table.Field(binding.Fields["quantity"])
			record, err := f.pb.FindRecordById(table.PhysicalName, carrier)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate an already-corrupt legacy quantity only in this isolated PB fixture.
			record.Set(field.Identity.PhysicalName, "3")
			if err := f.pb.Save(record); err != nil {
				t.Fatal(err)
			}
			declaration.Version++
			f.prepareUpgradedRules(t, declaration)
			before = transactionBusinessSnapshot(t, f)
			_, err = f.call("transaction.configure", map[string]any{"declaration": declaration})
			if surfaceErrorCode(t, err) != "transaction.reservation_transfer_invalid" {
				t.Fatalf("corrupt historical quantity admitted: %v", err)
			}
			if before != transactionBusinessSnapshot(t, f) {
				t.Fatal("rejected upgrade cleaned historical data")
			}
			registered, err := transactionrules.Load(t.Context(), f.pb)
			if err != nil || len(registered) != 2 {
				t.Fatalf("rejected upgrade persisted: count=%d error=%v", len(registered), err)
			}
		})
	}
}
