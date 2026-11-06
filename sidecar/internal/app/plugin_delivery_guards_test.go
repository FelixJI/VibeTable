package app

import (
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

// Real PocketBase admission, rollback and concurrency coverage for the native
// delivery guards: dual confirmation windows race a shared 10-unit instruction
// budget, over-issue keeps the real line while the internal allocation stays
// bounded, transfers hold inside one authoritative transaction, the owner line
// is the only purpose authority, and ordinary single-table edits hit the same
// durable registration through preview/audit/commit.
func TestDeliveryGuardsNativeReservationAdmissionAndConcurrency(t *testing.T) {
	f := newTransactionFixture(t, "delivery")
	ref := func(entity, temp string) any { return map[string]any{"entity": entity, "tempId": temp} }
	digestOf := func(result transactionResult, id string) string {
		t.Helper()
		for _, row := range result.Receipt.Rows {
			if row.RecordID == id {
				return row.Digest
			}
		}
		t.Fatal("missing receipt row", id)
		return ""
	}
	preview := func(plan transactionPlan) transactionPreview {
		t.Helper()
		raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
		if err != nil {
			t.Fatal(err)
		}
		return raw.(transactionPreview)
	}
	commit := func(p transactionPreview) error {
		_, err := f.call("transaction.commit", map[string]any{"requestId": p.RequestID})
		return err
	}
	code := func(err error) string {
		var product *mutation.ProductError
		if errors.As(err, &product) {
			return product.Code
		}
		return ""
	}

	seed := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{
		{Entity: "obligations", Kind: "create", TempID: "o", Values: map[string]any{"quantity": "10"}},
		{Entity: "documents", Kind: "create", TempID: "di", Values: map[string]any{"phase": "instruction_open"}},
		{Entity: "lines", Kind: "create", TempID: "l1", Values: map[string]any{"purpose": "normal", "real_qty": 10.0, "document": ref("documents", "di")}},
		{Entity: "sources", Kind: "create", TempID: "h", Values: map[string]any{"quantity": "10", "scope": ref("obligations", "o"), "owner_line": ref("lines", "l1")}},
	}}
	seeded := f.submit(t, seed)
	ids := seeded.NewIDs
	obligation, instruction := ids["obligations"]["o"], ids["sources"]["h"]
	instructionDocument := ids["documents"]["di"]
	// A confirmed window carries the full real over-issue; the line itself is
	// never bounded by the commercial obligation, only its source allocation.
	window := func(source, quantity, purpose string) transactionPlan {
		return transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
			{Entity: "documents", Kind: "create", TempID: "d", Values: map[string]any{"phase": "delivery_confirmed"}},
			{Entity: "lines", Kind: "create", TempID: "l", Values: map[string]any{"purpose": purpose, "real_qty": 12.0, "document": ref("documents", "d")}},
			{Entity: "sources", Kind: "create", TempID: "a", Values: map[string]any{"quantity": quantity, "scope": obligation, "owner_line": ref("lines", "l"), "instruction_source": source}},
		}}
	}
	// Dual-window race: two 8-unit confirmations against one 10-unit budget.
	first, second := preview(window(instruction, "8", "normal")), preview(window(instruction, "8", "normal"))
	errs := make([]error, 2)
	var group sync.WaitGroup
	for i, p := range []transactionPreview{first, second} {
		group.Add(1)
		go func() { defer group.Done(); errs[i] = commit(p) }()
	}
	group.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("expected exactly one racing window to apply: %v", errs)
	}
	for _, err := range errs {
		if err != nil && code(err) != "transaction.reservation_exhausted" {
			t.Fatalf("racing window rejected with %v", err)
		}
	}
	if err := commit(preview(window(instruction, "3", "normal"))); code(err) != "transaction.reservation_exhausted" {
		t.Fatalf("over-budget follow-up: %v", err)
	}
	// Cross-purpose consumption is rejected natively in both directions: a
	// salesReturn line cannot consume the normal instruction, and a normal line
	// cannot consume a gift instruction once it exists.
	if err := commit(preview(window(instruction, "8", "refund"))); code(err) != "transaction.reservation_invalid" {
		t.Fatalf("cross-purpose refund consumption: %v", err)
	}
	// A gift instruction runs its own ledger while the normal quota is full.
	giftSeed := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
		{Entity: "documents", Kind: "create", TempID: "dg", Values: map[string]any{"phase": "instruction_open"}},
		{Entity: "lines", Kind: "create", TempID: "lg", Values: map[string]any{"purpose": "gift", "real_qty": 10.0, "document": ref("documents", "dg")}},
		{Entity: "sources", Kind: "create", TempID: "g", Values: map[string]any{"quantity": "10", "scope": obligation, "owner_line": ref("lines", "lg")}},
	}}
	giftSeeded := f.submit(t, giftSeed)
	giftIDs := giftSeeded.NewIDs
	giftInstruction, giftDocument := giftIDs["sources"]["g"], giftIDs["documents"]["dg"]
	if err := commit(preview(window(giftInstruction, "8", "normal"))); code(err) != "transaction.reservation_invalid" {
		t.Fatalf("cross-purpose normal consumption of gift: %v", err)
	}
	giftWindow := f.submit(t, window(giftInstruction, "8", "gift"))
	giftLine := giftWindow.NewIDs["lines"]["l"]
	giftLineDigest := digestOf(giftWindow, giftLine)
	// Re-classifying the confirmed gift line as normal is blocked by the frozen
	// parent document and would otherwise move recorded gift execution.
	flip := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
		{Entity: "lines", Kind: "update", RecordID: giftLine, ExpectedDigest: &giftLineDigest, Values: map[string]any{"purpose": "normal"}},
	}}
	if err := commit(preview(flip)); code(err) != "transaction.parent_frozen" {
		t.Fatalf("line purpose flip on confirmed document: %v", err)
	}
	// Same-transaction transfer moves the exact remaining 2 units; the new
	// instruction enters open while the old one leaves in one commit.
	digest := digestOf(seeded, instructionDocument)
	transfer := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
		{Entity: "documents", Kind: "update", RecordID: instructionDocument, ExpectedDigest: &digest, Values: map[string]any{"phase": "instruction_transferred"}},
		{Entity: "documents", Kind: "create", TempID: "di2", Values: map[string]any{"phase": "instruction_open"}},
		{Entity: "lines", Kind: "create", TempID: "l2", Values: map[string]any{"purpose": "normal", "real_qty": 0.0, "document": ref("documents", "di2")}},
		{Entity: "sources", Kind: "create", TempID: "t", Values: map[string]any{"quantity": "2", "scope": obligation, "owner_line": ref("lines", "l2"), "transferred_from": instruction}},
	}}
	moved := f.submit(t, transfer)
	reopened := moved.NewIDs["sources"]["t"]
	finalWindow := f.submit(t, window(reopened, "2", "normal"))
	allocated := finalWindow.NewIDs["sources"]["a"]
	allocatedOwner := finalWindow.NewIDs["lines"]["l"]
	allocatedDigest := digestOf(finalWindow, allocated)
	allocatedLineDigest := digestOf(finalWindow, allocatedOwner)
	if err := commit(preview(window(reopened, "1", "normal"))); code(err) != "transaction.reservation_exhausted" {
		t.Fatalf("transferred remainder exhausted: %v", err)
	}
	// Closing a fully consumed transfer carrier stays legal.
	carrierDoc := moved.NewIDs["documents"]["di2"]
	carrierDigest := digestOf(moved, carrierDoc)
	closeCarrier := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
		{Entity: "documents", Kind: "update", RecordID: carrierDoc, ExpectedDigest: &carrierDigest, Values: map[string]any{"phase": "instruction_closed"}},
	}}
	f.submit(t, closeCarrier)
	// The gift remainder transfers, partially executes and transfers onward.
	giftDigest := digestOf(giftSeeded, giftDocument)
	giftTransfer := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
		{Entity: "documents", Kind: "update", RecordID: giftDocument, ExpectedDigest: &giftDigest, Values: map[string]any{"phase": "instruction_transferred"}},
		{Entity: "documents", Kind: "create", TempID: "dg2", Values: map[string]any{"phase": "instruction_open"}},
		{Entity: "lines", Kind: "create", TempID: "lg2", Values: map[string]any{"purpose": "gift", "real_qty": 0.0, "document": ref("documents", "dg2")}},
		{Entity: "sources", Kind: "create", TempID: "g2", Values: map[string]any{"quantity": "2", "scope": obligation, "owner_line": ref("lines", "lg2"), "transferred_from": giftInstruction}},
	}}
	giftMoved := f.submit(t, giftTransfer)
	giftCarrier := giftMoved.NewIDs["sources"]["g2"]
	giftCarrierDoc := giftMoved.NewIDs["documents"]["dg2"]
	f.submit(t, window(giftCarrier, "1", "gift"))
	giftCarrierDigest := digestOf(giftMoved, giftCarrierDoc)
	giftChain := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
		{Entity: "documents", Kind: "update", RecordID: giftCarrierDoc, ExpectedDigest: &giftCarrierDigest, Values: map[string]any{"phase": "instruction_transferred"}},
		{Entity: "documents", Kind: "create", TempID: "dg3", Values: map[string]any{"phase": "instruction_open"}},
		{Entity: "lines", Kind: "create", TempID: "lg3", Values: map[string]any{"purpose": "gift", "real_qty": 0.0, "document": ref("documents", "dg3")}},
		{Entity: "sources", Kind: "create", TempID: "g3", Values: map[string]any{"quantity": "1", "scope": obligation, "owner_line": ref("lines", "lg3"), "transferred_from": giftCarrier}},
	}}
	f.submit(t, giftChain)
	// Refunding through a purpose flip on the confirmed allocation's line is
	// blocked by the parent freeze and the monotonic native reservation.
	refund := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
		{Entity: "lines", Kind: "update", RecordID: allocatedOwner, ExpectedDigest: &allocatedLineDigest, Values: map[string]any{"purpose": "refund"}},
	}}
	if err := commit(preview(refund)); code(err) != "transaction.parent_frozen" {
		t.Fatalf("refund release: %v", err)
	}
	// Extension fields outside the declared freeze set stay editable through a
	// normal plugin transaction.
	extended := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{
		{Entity: "sources", Kind: "update", RecordID: allocated, ExpectedDigest: &allocatedDigest, Values: map[string]any{"note": "amended"}},
	}}
	amended := f.submit(t, extended)
	currentDigest := digestOf(amended, allocated)

	// Ordinary single-table writes reach the same durable registration through
	// the public preview/audit/commit flow.
	if err := writecoordinator.EnsurePocketBaseReceiptTable(t.Context(), f.pb); err != nil {
		t.Fatal(err)
	}
	sources := f.model.Bindings["sources"]
	physical := func(values map[string]any) map[string]any {
		out := map[string]any{}
		for key, value := range values {
			out[sources.Fields[key]] = value
		}
		return out
	}
	surface := func(operations ...map[string]any) map[string]any {
		return map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": sources.TableID, "schemaRevision": f.model.Revisions[sources.TableID], "operations": operations}
	}
	requireSingleTable := func(name, want string, operations ...map[string]any) {
		t.Helper()
		session := *f
		session.input.RequestID = name
		call := transactionHTTP(t, &session, nil)
		plan := surface(operations...)
		if response := call("mutation.preview", plan); response.Code != http.StatusOK {
			t.Fatalf("%s preview: %s", name, response.Body.String())
		}
		if response := call("audit.start", map[string]any{"taskId": name, "plan": plan}); response.Code != http.StatusOK {
			t.Fatalf("%s audit: %s", name, response.Body.String())
		}
		before := transactionBusinessSnapshot(t, f)
		for range 2 {
			requireHTTPNotCommitted(t, call("mutation.commit", plan), want)
		}
		if before != transactionBusinessSnapshot(t, f) {
			t.Fatalf("%s changed business rows", name)
		}
	}
	commitSingleTable := func(name string, operations ...map[string]any) {
		t.Helper()
		session := *f
		session.input.RequestID = name
		call := transactionHTTP(t, &session, nil)
		plan := surface(operations...)
		if response := call("mutation.preview", plan); response.Code != http.StatusOK {
			t.Fatalf("%s preview: %s", name, response.Body.String())
		}
		if response := call("audit.start", map[string]any{"taskId": name, "plan": plan}); response.Code != http.StatusOK {
			t.Fatalf("%s audit: %s", name, response.Body.String())
		}
		if response := call("mutation.commit", plan); response.Code != http.StatusOK {
			t.Fatalf("%s commit: %s", name, response.Body.String())
		}
	}
	// A real update of a frozen core field is rejected by the durable rules.
	requireSingleTable("frozen-core-update", "transaction.parent_frozen",
		map[string]any{"kind": "update", "primaryKey": allocated, "expectedDigest": currentDigest, "values": physical(map[string]any{"quantity": "1"})})
	// A complete, otherwise valid create into the frozen parent is rejected.
	requireSingleTable("frozen-parent-create", "transaction.parent_frozen",
		map[string]any{"kind": "create", "values": physical(map[string]any{"quantity": "1", "scope": obligation, "owner_line": allocatedOwner})})
	// An extension field update on the same frozen row commits normally.
	commitSingleTable("extension-update",
		map[string]any{"kind": "update", "primaryKey": allocated, "expectedDigest": currentDigest, "values": physical(map[string]any{"note": "single-table"})})
	// Authoritative re-read proves the persisted value, not just a success reply.
	sourceTable, err := schemaexecution.Describe(t.Context(), f.pb, sources.TableID)
	if err != nil {
		t.Fatal(err)
	}
	noteField, _ := sourceTable.Field(sources.Fields["note"])
	record, err := f.pb.FindRecordById(sourceTable.PhysicalName, allocated)
	if err != nil {
		t.Fatal(err)
	}
	if value := record.GetString(noteField.Identity.PhysicalName); value != "single-table" {
		t.Fatalf("extension update not durable: %q", value)
	}
}
