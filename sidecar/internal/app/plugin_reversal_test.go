package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func addReversalModel(m *pluginmodel.Declaration) {
	m.Entities[1].Fields = append(m.Entities[1].Fields, pluginmodel.Field{Key: "agreement", Label: "agreement", Type: v2.LogicalText})
	for _, key := range []string{"quantity", "amount", "material", "project", "package", "unit", "currency", "note"} {
		m.Entities[2].Fields = append(m.Entities[2].Fields, pluginmodel.Field{Key: key, Label: key, Type: v2.LogicalText})
	}
	m.Entities[2].Fields = append(m.Entities[2].Fields,
		pluginmodel.Field{Key: "original_line", Label: "original", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "lines", Field: "reversals", Cardinality: "one"}},
		pluginmodel.Field{Key: "reversals", Label: "returns", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "lines", Field: "original_line", Cardinality: "many"}})
}

func reversalLine(purpose, quantity string, owner any) map[string]any {
	return map[string]any{"purpose": purpose, "quantity": quantity, "real_qty": 12, "document": owner, "amount": nil,
		"material": "m", "project": "p", "package": nil, "unit": "u", "currency": "CNY"}
}

func reversalPlan(phase, purpose, quantity string, original any) transactionPlan {
	line := reversalLine(purpose, quantity, regressionRef("documents", "d"))
	if original != nil {
		line["original_line"] = original
	}
	return transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{
		{Entity: "documents", Kind: "create", TempID: "d", Values: map[string]any{"phase": phase, "agreement": nil}},
		{Entity: "lines", Kind: "create", TempID: "l", Values: line},
	}}
}

func reversalNative(t *testing.T, f *transactionFixture, name, entity string, ops ...map[string]any) *httptest.ResponseRecorder {
	return reversalNativePrepare(t, f, name, entity, ops...)()
}

func reversalNativePrepare(t *testing.T, f *transactionFixture, name, entity string, ops ...map[string]any) func() *httptest.ResponseRecorder {
	t.Helper()
	if err := writecoordinator.EnsurePocketBaseReceiptTable(t.Context(), f.pb); err != nil {
		t.Fatal(err)
	}
	session := *f
	session.input.RequestID = name
	binding := f.model.Bindings[entity]
	for _, op := range ops {
		values := map[string]any{}
		for key, value := range op["values"].(map[string]any) {
			values[binding.Fields[key]] = value
		}
		op["values"] = values
	}
	plan := map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": binding.TableID, "schemaRevision": f.model.Revisions[binding.TableID], "operations": ops}
	call := transactionHTTP(t, &session, nil)
	if response := call("mutation.preview", plan); response.Code != http.StatusOK {
		t.Fatalf("%s preview: %s", name, response.Body.String())
	}
	if response := call("audit.start", map[string]any{"taskId": name, "plan": plan}); response.Code != http.StatusOK {
		t.Fatalf("%s audit: %s", name, response.Body.String())
	}
	return func() *httptest.ResponseRecorder { return call("mutation.commit", plan) }
}

func TestReversalNativePhaseOnlyRejectsMissingOriginal(t *testing.T) {
	f := newTransactionFixture(t, "delivery-reversal")
	seed := f.submit(t, reversalPlan("delivery_draft", "salesReturn", "6", nil))
	id := seed.NewIDs["documents"]["d"]
	_, digest := regressionRow(t, f, "documents", id)
	before := transactionBusinessSnapshot(t, f)
	response := reversalNative(t, f, "confirm-missing-original", "documents", map[string]any{"kind": "update", "primaryKey": id, "expectedDigest": digest, "values": map[string]any{"phase": "delivery_confirmed"}})
	requireHTTPNotCommitted(t, response, "transaction.reversal_invalid")
	if before != transactionBusinessSnapshot(t, f) {
		t.Fatal("rejected confirmation changed rows")
	}
	regressionReject(t, f, reversalPlan("delivery_confirmed", "salesReturn", "6", nil), "transaction.reversal_invalid")
	original := f.submit(t, reversalPlan("delivery_confirmed", "normal", "10", nil)).NewIDs["lines"]["l"]
	line := seed.NewIDs["lines"]["l"]
	_, lineDigest := regressionRow(t, f, "lines", line)
	f.submit(t, transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{{Entity: "lines", Kind: "update", RecordID: line, ExpectedDigest: &lineDigest, Values: map[string]any{"original_line": original}}}})
	_, digest = regressionRow(t, f, "documents", id)
	response = reversalNative(t, f, "confirm-repaired", "documents", map[string]any{"kind": "update", "primaryKey": id, "expectedDigest": digest, "values": map[string]any{"phase": "delivery_confirmed"}})
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
}

func TestReversalNativeCompetingAndStalePreviews(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale-preview", true: "concurrent"}[concurrent], func(t *testing.T) {
			f := newTransactionFixture(t, "delivery-reversal")
			original := f.submit(t, reversalPlan("delivery_confirmed", "normal", "10", nil)).NewIDs["lines"]["l"]
			seeds := []transactionResult{f.submit(t, reversalPlan("delivery_draft", "salesReturn", "6", original)), f.submit(t, reversalPlan("delivery_draft", "salesReturn", "6", original))}
			commits := []func() *httptest.ResponseRecorder{}
			for i, seed := range seeds {
				id := seed.NewIDs["documents"]["d"]
				_, digest := regressionRow(t, f, "documents", id)
				commits = append(commits, reversalNativePrepare(t, f, []string{"first-return", "second-return"}[i], "documents", map[string]any{"kind": "update", "primaryKey": id, "expectedDigest": digest, "values": map[string]any{"phase": "delivery_confirmed"}}))
			}
			responses := make([]*httptest.ResponseRecorder, 2)
			if concurrent {
				var group sync.WaitGroup
				for i := range commits {
					group.Add(1)
					go func() { defer group.Done(); responses[i] = commits[i]() }()
				}
				group.Wait()
			} else {
				responses[0], responses[1] = commits[0](), commits[1]()
			}
			failed := -1
			for i, response := range responses {
				if response.Code != http.StatusOK {
					requireHTTPNotCommitted(t, response, "transaction.reversal_exceeded")
					if failed != -1 {
						t.Fatal("both contenders rejected")
					}
					failed = i
				}
			}
			if failed == -1 {
				t.Fatal("two 6-unit confirmations consumed original10")
			}
			id := seeds[failed].NewIDs["lines"]["l"]
			_, digest := regressionRow(t, f, "lines", id)
			response := reversalNative(t, f, "repair-four", "lines", map[string]any{"kind": "update", "primaryKey": id, "expectedDigest": digest, "values": map[string]any{"quantity": "4"}})
			if response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			doc := seeds[failed].NewIDs["documents"]["d"]
			_, digest = regressionRow(t, f, "documents", doc)
			response = reversalNative(t, f, "confirm-four", "documents", map[string]any{"kind": "update", "primaryKey": doc, "expectedDigest": digest, "values": map[string]any{"phase": "delivery_confirmed"}})
			if response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			regressionReject(t, f, reversalPlan("delivery_confirmed", "salesReturn", "1", original), "transaction.reversal_exceeded")
		})
	}
}

func TestReversalNativeSnapshotsDraftsAndSameTransaction(t *testing.T) {
	for _, scenario := range []string{"self", "original-draft", "original-gift", "unit", "currency", "package-null", "agreement-null", "draft-overflow", "draft-quantity-overflow", "same-transaction"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransactionFixture(t, "delivery-reversal")
			phase, purpose := "delivery_confirmed", "normal"
			if scenario == "original-draft" || scenario == "draft-overflow" {
				phase = "delivery_draft"
			}
			if scenario == "original-gift" {
				purpose = "gift"
			}
			originalPlan := reversalPlan(phase, purpose, "10", nil)
			if scenario == "same-transaction" {
				returned := reversalPlan("delivery_confirmed", "salesReturn", "10", regressionRef("lines", "l"))
				returned.Operations[0].TempID = "rd"
				returned.Operations[1].TempID = "rl"
				returned.Operations[1].Values["document"] = regressionRef("documents", "rd")
				originalPlan.Operations = append(originalPlan.Operations, returned.Operations...)
				result := f.submit(t, originalPlan)
				for _, key := range []string{"l", "rl"} {
					row, _ := regressionRow(t, f, "lines", result.NewIDs["lines"][key])
					if row["amount"] != nil {
						t.Fatal("unknown amount became zero")
					}
				}
				return
			}
			original := f.submit(t, originalPlan).NewIDs["lines"]["l"]
			plan := reversalPlan("delivery_confirmed", "salesReturn", "6", original)
			want := "transaction.reversal_invalid"
			switch scenario {
			case "self":
				plan.Operations[1].Values["original_line"] = regressionRef("lines", "l")
			case "unit", "currency":
				plan.Operations[1].Values[scenario] = "other"
				want = "transaction.reversal_mismatch"
			case "package-null":
				plan.Operations[1].Values["package"] = "bundle"
				want = "transaction.reversal_mismatch"
			case "agreement-null":
				plan.Operations[0].Values["agreement"] = "agreement"
				want = "transaction.reversal_mismatch"
				plan.Operations[0].Values["phase"] = "delivery_draft"
				result := f.submit(t, plan)
				doc := result.NewIDs["documents"]["d"]
				_, digest := regressionRow(t, f, "documents", doc)
				before := transactionBusinessSnapshot(t, f)
				response := reversalNative(t, f, "confirm-mismatched-owner", "documents", map[string]any{"kind": "update", "primaryKey": doc, "expectedDigest": digest, "values": map[string]any{"phase": "delivery_confirmed"}})
				requireHTTPNotCommitted(t, response, want)
				if transactionBusinessSnapshot(t, f) != before {
					t.Fatal("snapshot mismatch changed business data")
				}
				return
			case "draft-overflow", "draft-quantity-overflow":
				plan.Operations[0].Values["phase"] = "delivery_draft"
				plan.Operations[1].Values["quantity"] = "11"
				result := f.submit(t, plan)
				doc := result.NewIDs["documents"]["d"]
				_, digest := regressionRow(t, f, "documents", doc)
				response := reversalNative(t, f, "draft-waits-original", "documents", map[string]any{"kind": "update", "primaryKey": doc, "expectedDigest": digest, "values": map[string]any{"phase": "delivery_confirmed"}})
				if scenario == "draft-quantity-overflow" {
					want = "transaction.reversal_exceeded"
				}
				requireHTTPNotCommitted(t, response, want)
				row, _ := regressionRow(t, f, "lines", result.NewIDs["lines"]["l"])
				if row["quantity"] != "11" {
					t.Fatal("draft input truncated")
				}
				return
			}
			regressionReject(t, f, plan, want)
		})
	}
}

func TestReversalAdmissionRejectsExistingInvalidConfirmation(t *testing.T) {
	f := newTransactionFixture(t, "delivery-reversal-legacy")
	f.submit(t, reversalPlan("delivery_confirmed", "salesReturn", "6", nil))
	declaration := f.declaration
	declaration.Version++
	raw, err := os.ReadFile("../../../contracts/plugin-reversal-rule.json")
	if err != nil {
		t.Fatal(err)
	}
	var rule transactionrules.Rule
	if err = json.Unmarshal(raw, &rule); err != nil {
		t.Fatal(err)
	}
	declaration.Rules = append(declaration.Rules, rule)
	f.prepareUpgradedRules(t, declaration)
	before := transactionBusinessSnapshot(t, f)
	_, err = f.call("transaction.configure", map[string]any{"declaration": declaration})
	if surfaceErrorCode(t, err) != "transaction.reversal_invalid" {
		t.Fatalf("invalid existing confirmation admission: %v", err)
	}
	if before != transactionBusinessSnapshot(t, f) {
		t.Fatal("rule registration cleaned or changed invalid business data")
	}
}

func TestReversalConfirmedFreezeAndNormalOverIssue(t *testing.T) {
	f := newTransactionFixture(t, "delivery-reversal")
	plan := reversalPlan("delivery_confirmed", "normal", "12", nil)
	plan.Operations = append(plan.Operations,
		transactionOperation{Entity: "obligations", Kind: "create", TempID: "o", Values: map[string]any{"quantity": "10"}},
		transactionOperation{Entity: "sources", Kind: "create", TempID: "s", Values: map[string]any{"quantity": "10", "scope": regressionRef("obligations", "o"), "owner_line": regressionRef("lines", "l"), "instruction_source": regressionRef("sources", "h")}},
		transactionOperation{Entity: "documents", Kind: "create", TempID: "di", Values: map[string]any{"phase": "instruction_open", "agreement": nil}},
		transactionOperation{Entity: "lines", Kind: "create", TempID: "li", Values: reversalLine("normal", "10", regressionRef("documents", "di"))},
		transactionOperation{Entity: "sources", Kind: "create", TempID: "h", Values: map[string]any{"quantity": "10", "scope": regressionRef("obligations", "o"), "owner_line": regressionRef("lines", "li")}})
	result := f.submit(t, plan)
	original := result.NewIDs["lines"]["l"]
	returned := f.submit(t, reversalPlan("delivery_confirmed", "salesReturn", "6", original))
	id := returned.NewIDs["lines"]["l"]
	for _, op := range []transactionOperation{{Kind: "update", Values: map[string]any{"quantity": "5"}}, {Kind: "delete"}} {
		_, digest := regressionRow(t, f, "lines", id)
		op.Entity = "lines"
		op.RecordID = id
		op.ExpectedDigest = &digest
		regressionReject(t, f, transactionPlan{Contract: plan.Contract, ModelID: plan.ModelID, Operations: []transactionOperation{op}}, "transaction.parent_frozen")
	}
	_, digest := regressionRow(t, f, "lines", id)
	response := reversalNative(t, f, "return-extension", "lines", map[string]any{"kind": "update", "primaryKey": id, "expectedDigest": digest, "values": map[string]any{"note": "amended"}})
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	row, _ := regressionRow(t, f, "lines", original)
	if row["quantity"] != "12" {
		t.Fatal("normal real over-issue truncated")
	}
	row, _ = regressionRow(t, f, "lines", id)
	if row["note"] != "amended" {
		t.Fatal("extension did not persist")
	}
	extra := reversalPlan("delivery_confirmed", "normal", "1", nil)
	extra.Operations = append(extra.Operations, transactionOperation{Entity: "sources", Kind: "create", TempID: "extra", Values: map[string]any{"quantity": "1", "scope": result.NewIDs["obligations"]["o"], "owner_line": regressionRef("lines", "l"), "instruction_source": result.NewIDs["sources"]["h"]}})
	regressionReject(t, f, extra, "transaction.reservation_exhausted")
}
