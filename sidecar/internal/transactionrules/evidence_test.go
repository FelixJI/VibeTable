package transactionrules

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
)

func TestChangeEvidenceDeclaration(t *testing.T) {
	declaration := invoiceGuardsDeclaration(t, newKindRule(t, "evidence"))
	// The actual-header rule owns itself: an empty ownerPath needs no
	// artificial self relation.
	self := cloneRule(declaration.Rules[0])
	self.Entity = "invoices"
	self.Evidence.OwnerPath = nil
	if err := invoiceGuardsDeclaration(t, self).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Declaration){
		func(d *Declaration) { d.Rules[0].Kind = "settlementBalance" },
		func(d *Declaration) { d.Rules[0].Evidence.LatestEvidence = "" },
		func(d *Declaration) { d.Rules[0].Evidence.Types = []string{} },
		func(d *Declaration) { d.Rules[0].Evidence.Types = []string{"correction", "correction"} },
		func(d *Declaration) { d.Rules[0].Evidence.Evidence.Entity = "" },
		func(d *Declaration) { d.Rules[0].Evidence.Evidence.Note = "" },
		func(d *Declaration) { d.Rules[0].Evidence.Evidence.Fields = []string{"close_net", "close_net"} },
		func(d *Declaration) { d.Rules[0].Evidence.OwnerPath = []RelationStep{{Field: "invoice"}} },
		func(d *Declaration) { d.Rules[0].Evidence.Fields = []string{""} },
		func(d *Declaration) { d.Rules[0].Fields = []string{"quantity", "quantity"} },
	} {
		wire, _ := json.Marshal(declaration)
		var d Declaration
		if err := json.Unmarshal(wire, &d); err != nil {
			t.Fatal(err)
		}
		mutate(&d)
		if d.Validate() == nil {
			t.Fatal("invalid changeEvidence declaration accepted")
		}
	}
}

func TestChangeEvidenceOwnerPathWireCompatibility(t *testing.T) {
	original := newKindRule(t, "evidence")
	raw, err := json.Marshal(invoiceGuardsDeclaration(t, original))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	spec := wire["rules"].([]any)[0].(map[string]any)["evidence"].(map[string]any)
	decode := func() (Declaration, error) {
		raw, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		return Decode(raw)
	}
	nonEmpty, err := decode()
	if err != nil || !reflect.DeepEqual(nonEmpty.Rules[0].Evidence.OwnerPath, original.Evidence.OwnerPath) {
		t.Fatalf("non-empty owner path changed: %v", err)
	}
	delete(spec, "ownerPath")
	legacy, err := decode()
	if err != nil {
		t.Fatal(err)
	}
	spec["ownerPath"] = []any{}
	explicit, err := decode()
	if err != nil || !explicit.Retains(legacy) || !legacy.Retains(explicit) || explicit.Retains(nonEmpty) {
		t.Fatalf("only the explicit empty path is equivalent to omission: %v", err)
	}
	spec["types"] = []string{"other_correction"}
	differentRule, err := decode()
	if err != nil || differentRule.Retains(legacy) {
		t.Fatalf("a changed evidence type must remain different: %v", err)
	}
	spec["ownerPath"] = nil
	if _, err := decode(); err == nil {
		t.Fatal("explicit null owner path accepted")
	}
}

func evidenceImages() Images {
	return Images{
		"invoices": {
			"keep":  {"latest_evidence": "e1"},
			"other": {"latest_evidence": nil},
		},
		"adjustments": {
			"e1": {"invoice": "keep", "kind": "correction", "note": "price amended", "close_remaining": false, "quantity": "0", "net": "0.00", "tax": "0.00", "gross": "0.00"},
		},
		"actual_lines": {"l1": {"invoice": "keep", "quantity": nil, "net": nil, "tax": nil, "gross": nil}, "l2": {"invoice": "other", "quantity": nil, "net": nil, "tax": nil, "gross": nil}},
		"allocations": {
			"a1": {"line": "l1", "quantity": "4", "net": "33.85", "tax": "6.15", "gross": "40.00"},
		},
	}
}

func TestChangeEvidenceCorrectionLifecycle(t *testing.T) {
	rule := newKindRule(t, "evidence")
	rule.Evidence.AllowInsert = false
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, rule)}
	before := evidenceImages()
	// A same-transaction new evidence bound to the same owner authorizes the
	// correction of existing core fields.
	after := clone(before)
	after["adjustments"]["e2"] = map[string]any{"invoice": "keep", "kind": "correction", "note": "quantity re-keyed", "close_remaining": false, "quantity": "0", "net": "0.00", "tax": "0.00", "gross": "0.00"}
	after["invoices"]["keep"]["latest_evidence"] = "e2"
	after["allocations"]["a1"]["quantity"] = "5"
	if err := registration.Validate(before, after); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(Images)
	}{
		{"no-evidence", "evidence_required", func(i Images) {
			i["invoices"]["keep"]["latest_evidence"] = nil
			i["allocations"]["a1"]["quantity"] = "5"
		}},
		{"reused-old-evidence", "evidence_reused", func(i Images) { i["allocations"]["a1"]["quantity"] = "5" }},
		{"cross-owner", "evidence_invalid", func(i Images) {
			i["adjustments"]["e2"] = clone(i)["adjustments"]["e1"]
			i["adjustments"]["e2"]["note"] = "belongs elsewhere"
			i["adjustments"]["e2"]["invoice"] = "other"
			i["invoices"]["keep"]["latest_evidence"] = "e2"
			i["allocations"]["a1"]["quantity"] = "5"
		}},
		{"unbound-event-cannot-justify", "evidence_invalid", func(i Images) {
			i["adjustments"]["e2"] = clone(i)["adjustments"]["e1"]
			i["adjustments"]["e2"]["note"] = "request side close"
			i["adjustments"]["e2"]["invoice"] = nil
			i["invoices"]["keep"]["latest_evidence"] = "e2"
			i["allocations"]["a1"]["quantity"] = "5"
		}},
		{"evidence-modified", "evidence_frozen", func(i Images) { i["adjustments"]["e1"]["note"] = "rewritten" }},
		{"scope-modified", "evidence_frozen", func(i Images) { i["adjustments"]["e1"]["quantity"] = "1" }},
		{"evidence-deleted", "evidence_frozen", func(i Images) { delete(i["adjustments"], "e1") }},
		{"wrong-type", "evidence_invalid", func(i Images) {
			i["adjustments"]["e2"] = clone(i)["adjustments"]["e1"]
			i["adjustments"]["e2"]["kind"] = "unknown"
			i["invoices"]["keep"]["latest_evidence"] = "e2"
			i["allocations"]["a1"]["quantity"] = "5"
		}},
		{"blank-note", "evidence_invalid", func(i Images) {
			i["adjustments"]["e2"] = clone(i)["adjustments"]["e1"]
			i["adjustments"]["e2"]["note"] = "   "
			i["invoices"]["keep"]["latest_evidence"] = "e2"
			i["allocations"]["a1"]["quantity"] = "5"
		}},
		{"dangling-latest", "evidence_invalid", func(i Images) { i["invoices"]["keep"]["latest_evidence"] = "missing" }},
		{"add-without-evidence", "evidence_reused", func(i Images) {
			i["allocations"]["a2"] = clone(i)["allocations"]["a1"]
		}},
		{"remove-without-evidence", "evidence_reused", func(i Images) { delete(i["allocations"], "a1") }},
		{"owner-erased-with-rows", "evidence_invalid", func(i Images) { delete(i["invoices"], "keep"); delete(i["allocations"], "a1") }},
		{"owner-and-evidence-erased", "evidence_frozen", func(i Images) {
			delete(i["invoices"], "keep")
			delete(i["adjustments"], "e1")
			delete(i["allocations"], "a1")
		}},
		{"rebind-needs-both", "evidence_reused", func(i Images) {
			i["allocations"]["a1"]["line"] = "l2"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := clone(evidenceImages())
			tc.mutate(after)
			if err := registration.Validate(evidenceImages(), after); err == nil || err.Error() != "transaction."+tc.want {
				t.Fatalf("want %s got %v", tc.want, err)
			}
		})
	}
	// Unfreeasing first never helps: re-pointing latest in one transaction
	// authorizes nothing later — the next bare correction still needs fresh
	// evidence in its own transaction.
	unfrozen := clone(evidenceImages())
	unfrozen["invoices"]["keep"]["latest_evidence"] = nil
	if err := registration.Validate(evidenceImages(), unfrozen); err != nil {
		t.Fatal(err)
	}
	bare := clone(unfrozen)
	bare["allocations"]["a1"]["quantity"] = "5"
	if err := registration.Validate(unfrozen, bare); err == nil || err.Error() != "transaction.evidence_required" {
		t.Fatalf("two-step unfreeze bypassed the evidence rule: %v", err)
	}
	// Request-side disposal events without an owner binding stay registrable.
	role := clone(evidenceImages())
	role["adjustments"]["close"] = map[string]any{"invoice": nil, "kind": "requestClose", "note": "closed remaining", "close_remaining": true, "quantity": "0", "net": "0.00", "tax": "0.00", "gross": "0.00"}
	if err := registration.Validate(role, clone(role)); err != nil {
		t.Fatal(err)
	}
}

// The actual-header variant owns itself; deleting the header row erases the
// registration and cannot be based on evidence at all.
func TestChangeEvidenceSelfOwnerLifecycle(t *testing.T) {
	rule := newKindRule(t, "evidence")
	rule.Entity = "invoices"
	rule.Evidence.OwnerPath = nil
	rule.Evidence.Fields = []string{"status"}
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, rule)}
	before := Images{
		"invoices": {"h1": {"status": "issued", "latest_evidence": "e1"}},
		"adjustments": {
			"e1": {"invoice": "h1", "kind": "correction", "note": "registered", "close_remaining": false, "quantity": "0", "net": "0.00", "tax": "0.00", "gross": "0.00"},
		},
	}
	if err := registration.Validate(before, clone(before)); err != nil {
		t.Fatal(err)
	}
	corrected := clone(before)
	corrected["adjustments"]["e2"] = map[string]any{"invoice": "h1", "kind": "correction", "note": "status fixed", "close_remaining": false, "quantity": "0", "net": "0.00", "tax": "0.00", "gross": "0.00"}
	corrected["invoices"]["h1"]["latest_evidence"] = "e2"
	corrected["invoices"]["h1"]["status"] = "voided"
	if err := registration.Validate(before, corrected); err != nil {
		t.Fatal(err)
	}
	erased := clone(before)
	delete(erased["invoices"], "h1")
	if err := registration.Validate(before, erased); err == nil || err.Error() != "transaction.evidence_invalid" {
		t.Fatalf("self-owner erasure accepted: %v", err)
	}
	// Even dropping the evidence together with the header stays rejected.
	gone := clone(erased)
	delete(gone["adjustments"], "e1")
	if err := registration.Validate(before, gone); err == nil || err.Error() != "transaction.evidence_frozen" {
		t.Fatalf("self-owner and evidence erasure accepted: %v", err)
	}
	// New headers born in this transaction carry no evidence obligation.
	fresh := clone(before)
	fresh["invoices"]["h2"] = map[string]any{"status": "draft", "latest_evidence": nil}
	if err := registration.Validate(before, fresh); err != nil {
		t.Fatal(err)
	}
}

func TestChangeEvidenceSameFieldNamesOnBothEntities(t *testing.T) {
	// The natural model declares quantity/net/tax/gross on both the protected
	// actual allocations and the shared adjustments entity; identical names
	// across the two namespaces must register and consume legally.
	rule := newKindRule(t, "evidence")
	if err := invoiceGuardsDeclaration(t, rule).Validate(); err != nil {
		t.Fatal(err)
	}
	// The evidence entity may even declare the very same measure names.
	sameNames := cloneRule(rule)
	sameNames.Evidence.Fields = []string{"quantity", "net", "tax", "gross"}
	if err := (Declaration{Contract: Contract, ModelID: "m", Version: 1, Decimals: invoiceGuardsDecimals(), Rules: []Rule{sameNames}}).Validate(); err != nil {
		t.Fatalf("same-name evidence fact fields rejected: %v", err)
	}
	registration := Registration{Declaration: invoiceGuardsDeclaration(t, rule)}
	before := evidenceImages()
	// A correction on same-named measures with fresh same-named evidence.
	after := clone(before)
	after["adjustments"]["e2"] = map[string]any{"invoice": "keep", "kind": "correction", "note": "re-keyed", "close_remaining": false, "quantity": "1", "net": "1.00", "tax": "0.00", "gross": "1.00"}
	after["invoices"]["keep"]["latest_evidence"] = "e2"
	after["allocations"]["a1"]["quantity"] = "5"
	if err := registration.Validate(before, after); err != nil {
		t.Fatal(err)
	}
	// Internal duplicates inside one list are still rejected.
	duplicate := cloneRule(rule)
	duplicate.Evidence.Fields = []string{"close_quantity", "close_quantity"}
	invalid := Declaration{Contract: Contract, ModelID: "m", Version: 1, Decimals: invoiceGuardsDecimals(), Rules: []Rule{duplicate}}
	if invalid.Validate() == nil {
		t.Fatal("duplicate evidence fact fields accepted")
	}
	crossEntity := cloneRule(rule)
	crossEntity.Fields = []string{"quantity", "net", "tax", "gross", "quantity"}
	invalid = Declaration{Contract: Contract, ModelID: "m", Version: 1, Decimals: invoiceGuardsDecimals(), Rules: []Rule{crossEntity}}
	if invalid.Validate() == nil {
		t.Fatal("duplicate protected fields accepted")
	}
}

func TestChangeEvidenceDependenciesAndPaths(t *testing.T) {
	r := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "evidence")), Bindings: map[string]pluginmodel.Binding{}}
	for entity := range r.dependencies() {
		r.Bindings[entity] = pluginmodel.Binding{TableID: "t_" + entity, Fields: map[string]string{}}
	}
	for entity, fields := range r.dependencies() {
		if len(fields) == 0 {
			t.Fatal("entity without readable dependencies", entity)
		}
		for field := range fields {
			r.Bindings[entity].Fields[field] = "f_" + field
			if !r.ProtectsBinding(r.Bindings[entity].TableID, "f_"+field) {
				t.Fatal("unprotected dependency", entity, field)
			}
		}
	}
	for _, entity := range []string{"allocations", "invoices", "adjustments"} {
		if !r.selectedBy(WriteSet{"t_" + entity: {"*": true}}) {
			t.Fatal("whole-table native write bypasses the evidence guard", entity)
		}
	}
	// The frozen disposal scope participates in dependency protection.
	if !r.selectedBy(WriteSet{"t_adjustments": {"f_quantity": true}}) {
		t.Fatal("disposal scope change bypasses the evidence guard")
	}
	if !r.selectedBy(WriteSet{"t_invoices": {"f_latest_evidence": true}}) {
		t.Fatal("latest evidence rebind bypasses the evidence guard")
	}
}

func TestChangeEvidenceProtectsIntermediateOwnerPath(t *testing.T) {
	r := Registration{Declaration: invoiceGuardsDeclaration(t, newKindRule(t, "evidence"))}
	before := evidenceImages()
	before["actual_lines"]["same_owner"] = map[string]any{"invoice": "keep", "quantity": nil, "net": nil, "tax": nil, "gross": nil}
	after := clone(before)
	after["allocations"]["a1"]["line"] = "same_owner"
	if err := r.Validate(before, after); err == nil || err.Error() != "transaction.evidence_reused" {
		t.Fatalf("same-owner line move bypassed evidence: %v", err)
	}
	after["adjustments"]["new"] = map[string]any{"invoice": "keep", "kind": "correction", "note": "重新归属票面行", "quantity": nil, "net": nil, "tax": nil, "gross": nil}
	after["invoices"]["keep"]["latest_evidence"] = "new"
	if err := r.Validate(before, after); err != nil {
		t.Fatal(err)
	}
}
