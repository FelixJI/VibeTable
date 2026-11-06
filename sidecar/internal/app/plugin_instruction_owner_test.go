package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

func TestInstructionConsumptionUsesPriorPhaseOwner(t *testing.T) {
	for _, newLine := range []bool{false, true} {
		name := "existing-line"
		if newLine {
			name = "new-line"
		}
		t.Run(name, func(t *testing.T) {
			f := newTransactionFixture(t, "delivery-reservation-only")
			if len(f.declaration.Rules) != 1 || f.declaration.Rules[0].Kind != "reservationBalance" {
				t.Fatal("parentFreeze must not mask this guard")
			}
			seed := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{
				{Entity: "obligations", Kind: "create", TempID: "o", Values: map[string]any{"quantity": "100"}},
				{Entity: "documents", Kind: "create", TempID: "da", Values: map[string]any{"phase": "delivery_confirmed"}},
				{Entity: "lines", Kind: "create", TempID: "la", Values: map[string]any{"purpose": "normal", "real_qty": 100, "document": regressionRef("documents", "da")}},
			}}
			for _, phase := range []string{"closed", "transferred", "open", "draft"} {
				seed.Operations = append(seed.Operations, transactionOperation{Entity: "documents", Kind: "create", TempID: phase, Values: map[string]any{"phase": "instruction_" + phase}})
				if !newLine {
					seed.Operations = append(seed.Operations, transactionOperation{Entity: "lines", Kind: "create", TempID: phase, Values: map[string]any{"purpose": "normal", "real_qty": 100, "document": regressionRef("documents", phase)}})
				}
			}
			ids := f.submit(t, seed).NewIDs
			sequence := 100
			// Direct native requests use allocated IDs and field IDs, without plugin preview/read guards.
			request := func(entity, id string, values map[string]any) mutation.Request {
				binding := f.model.Bindings[entity]
				physical := map[string]any{}
				for key, value := range values {
					physical[binding.Fields[key]] = value
				}
				return mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: id, IdempotencyKey: id,
					TableID: binding.TableID, SchemaRevision: f.model.Revisions[binding.TableID], Actor: mutation.Actor{Type: "user", ID: "local-user"},
					Operations: []mutation.Operation{{Kind: mutation.OperationInsert, RecordID: &id, Values: physical}}}
			}
			consume := func(entry, phase, finalPhase string) error {
				sequence++
				line, instruction, actual := fmt.Sprintf("%015d", sequence*3), fmt.Sprintf("%015d", sequence*3+1), fmt.Sprintf("%015d", sequence*3+2)
				plan := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID}
				var requests []mutation.Request
				var pluginLine any = ids["lines"][phase]
				if newLine {
					plan.Operations = append(plan.Operations, transactionOperation{Entity: "lines", Kind: "create", TempID: "li", Values: map[string]any{"purpose": "normal", "real_qty": 8, "document": ids["documents"][phase]}})
					pluginLine = regressionRef("lines", "li")
					requests = append(requests, request("lines", line, map[string]any{"purpose": "normal", "real_qty": 8, "document": ids["documents"][phase]}))
				} else {
					line = ids["lines"][phase]
				}
				plan.Operations = append(plan.Operations,
					transactionOperation{Entity: "sources", Kind: "create", TempID: "h", Values: map[string]any{"quantity": "8", "scope": ids["obligations"]["o"], "owner_line": pluginLine}},
					transactionOperation{Entity: "sources", Kind: "create", TempID: "a", Values: map[string]any{"quantity": "8", "scope": ids["obligations"]["o"], "owner_line": ids["lines"]["la"], "instruction_source": regressionRef("sources", "h")}})
				requests = append(requests,
					request("sources", instruction, map[string]any{"quantity": "8", "scope": ids["obligations"]["o"], "owner_line": line}),
					request("sources", actual, map[string]any{"quantity": "8", "scope": ids["obligations"]["o"], "owner_line": ids["lines"]["la"], "instruction_source": instruction}))
				if finalPhase != "" {
					doc := ids["documents"][phase]
					_, digest := regressionRow(t, f, "documents", doc)
					plan.Operations = append(plan.Operations, transactionOperation{Entity: "documents", Kind: "update", RecordID: doc, ExpectedDigest: &digest, Values: map[string]any{"phase": "instruction_" + finalPhase}})
				}
				if entry == "plugin" {
					return regressionCommit(f, plan)
				}
				_, err := f.kernel.ApplyPlan(t.Context(), requests,
					func(context.Context, core.App, string, string) error { return nil },
					func(core.App, mutation.PlanReceipt) error { return nil })
				return err
			}
			for _, phase := range []string{"closed", "transferred"} {
				for _, entry := range []string{"plugin", "native"} {
					t.Run(entry+"/"+phase, func(t *testing.T) {
						before := transactionBusinessSnapshot(t, f)
						err := consume(entry, phase, "")
						var product *mutation.ProductError
						if !errors.As(err, &product) || product.Code != "transaction.reservation_invalid" {
							t.Fatalf("old terminal owner consumed: %v", err)
						}
						if transactionBusinessSnapshot(t, f) != before {
							t.Fatal("rejected owner consumption changed business rows")
						}
					})
				}
			}
			if err := consume("native", "open", ""); err != nil {
				t.Fatal("native open owner control", err)
			}
			if err := consume("plugin", "open", "closed"); err != nil {
				t.Fatal("new source consumed then owner closed in same transaction", err)
			}
			if err := consume("plugin", "draft", "open"); err != nil {
				t.Fatal("existing draft owner opened and consumed in same transaction", err)
			}
		})
	}
}
