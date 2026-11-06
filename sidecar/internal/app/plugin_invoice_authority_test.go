package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
)

// The seven invoice entities use one signed allocation carrier for both
// allocationBalance and the original-blue settlement capacity.
func newInvoiceAuthorityFixture(t *testing.T, withPlans ...bool) *transactionFixture {
	t.Helper()
	normal := len(withPlans) > 0 && withPlans[0]
	scoped := len(withPlans) > 1 && withPlans[1]
	pb, workspace, input, domain := modelFixture(t)
	input = changeInstalledModel(t, pb, workspace, input, func(m *pluginmodel.Declaration) {
		m.Entities = nil
		for _, entry := range []struct {
			key    string
			fields []string
		}{
			{"requests", []string{"status", "direction", "purpose", "basis", "currency"}},
			{"request_lines", []string{"unit", "quantity", "net", "tax", "gross"}},
			{"request_sources", []string{"quantity", "net", "tax", "gross"}},
			{"invoices", []string{"number", "status", "direction", "amount_convention", "currency"}},
			{"actual_lines", []string{"unit", "purpose", "quantity", "net", "tax", "gross"}},
			{"allocations", []string{"quantity", "net", "tax", "gross"}},
			{"adjustments", []string{"kind", "note", "date", "quantity", "net", "tax", "gross"}},
		} {
			entity := pluginmodel.Entity{Key: entry.key, Label: entry.key, Operations: []string{"read", "query", "create", "update", "delete"}}
			for _, field := range entry.fields {
				entity.Fields = append(entity.Fields, pluginmodel.Field{Key: field, Label: field, Type: v2.LogicalText})
			}
			if entry.key == "adjustments" {
				entity.Fields = append(entity.Fields, pluginmodel.Field{Key: "close_remaining", Label: "close remaining", Type: v2.LogicalBool})
			}
			if entry.key == "invoices" {
				for i := 0; i < 45; i++ {
					key := fmt.Sprintf("budget_%d", i)
					entity.Fields = append(entity.Fields, pluginmodel.Field{Key: key, Label: key, Type: v2.LogicalText})
				}
			}
			m.Entities = append(m.Entities, entity)
		}
		if normal {
			for _, entry := range []struct {
				key    string
				fields []string
			}{{"obligations", []string{"quantity", "currency", "unit"}}, {"series", []string{"label"}}, {"plan_versions", []string{"label"}}, {"logical_items", []string{"label"}}, {"plan_items", []string{"quantity", "share"}}} {
				entity := pluginmodel.Entity{Key: entry.key, Label: entry.key, Operations: []string{"read", "query", "create", "update", "delete"}}
				for _, key := range entry.fields {
					field := pluginmodel.Field{Key: key, Label: key, Type: v2.LogicalText}
					if key == "share" {
						field.Type = v2.LogicalNumber
						field.Precision = "integer"
					}
					entity.Fields = append(entity.Fields, field)
				}
				m.Entities = append(m.Entities, entity)
			}
		}
		if scoped {
			for _, key := range []string{"agreements", "materials"} {
				m.Entities = append(m.Entities, pluginmodel.Entity{Key: key, Label: key, Operations: []string{"read", "query", "create", "update", "delete"}, Fields: []pluginmodel.Field{{Key: "label", Label: "label", Type: v2.LogicalText}}})
			}
		}
		relation := func(source, field, target, reverse string) {
			for i := range m.Entities {
				if m.Entities[i].Key == source {
					m.Entities[i].Fields = append(m.Entities[i].Fields, pluginmodel.Field{Key: field, Label: field, Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: target, Field: reverse, Cardinality: "one"}})
				}
				if m.Entities[i].Key == target {
					m.Entities[i].Fields = append(m.Entities[i].Fields, pluginmodel.Field{Key: reverse, Label: reverse, Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: source, Field: field, Cardinality: "many"}})
				}
			}
		}
		relation("request_lines", "request", "requests", "lines")
		relation("request_sources", "line", "request_lines", "sources")
		relation("actual_lines", "invoice", "invoices", "lines")
		relation("allocations", "line", "actual_lines", "allocations")
		relation("request_sources", "original", "allocations", "requests")
		relation("allocations", "original", "allocations", "reversals")
		relation("allocations", "application_source", "request_sources", "actuals")
		relation("adjustments", "source", "request_sources", "adjustments")
		relation("adjustments", "allocation", "allocations", "adjustments")
		relation("adjustments", "invoice", "invoices", "evidence")
		relation("invoices", "latest_evidence", "adjustments", "latest_for")
		if scoped {
			relation("invoices", "agreement", "agreements", "invoices")
			relation("requests", "agreement", "agreements", "requests")
			relation("actual_lines", "material", "materials", "actual_lines")
			relation("request_lines", "material", "materials", "request_lines")
		}
		if normal {
			relation("series", "current_version", "plan_versions", "current_for")
			relation("plan_versions", "series", "series", "versions")
			relation("plan_items", "plan_version", "plan_versions", "items")
			relation("plan_items", "logical_item", "logical_items", "items")
			relation("plan_items", "scope", "obligations", "plans")
			relation("request_sources", "obligation", "obligations", "requests")
			relation("allocations", "obligation", "obligations", "actuals")
			relation("request_sources", "plan_item", "plan_items", "requests")
			relation("allocations", "plan_item", "plan_items", "actuals")
		}
	})
	d := transactionrules.Declaration{Contract: transactionrules.Contract, ModelID: "synthetic", Version: 1}
	for _, entity := range []string{"request_lines", "request_sources", "actual_lines", "allocations", "adjustments"} {
		for i, field := range []string{"quantity", "net", "tax", "gross"} {
			scale := 2
			if i == 0 {
				scale = 0
			}
			decimal := transactionrules.Decimal{Entity: entity, Field: field, Format: exactdecimal.Format{Storage: "decimalText", Scale: scale, Digits: 38}, Nullable: true}
			if entity == "request_sources" || entity == "request_lines" || entity == "adjustments" {
				decimal.Sign = "nonnegative"
			}
			d.Decimals = append(d.Decimals, decimal)
		}
	}
	fillerCount := 45
	if normal {
		fillerCount = 43
	}
	for i := 0; i < fillerCount; i++ {
		d.Decimals = append(d.Decimals, transactionrules.Decimal{Entity: "invoices", Field: fmt.Sprintf("budget_%d", i), Format: exactdecimal.Format{Storage: "decimalText", Digits: 38}, Nullable: true})
	}
	for _, name := range []string{"allocation", "settlement", "evidence"} {
		raw, err := os.ReadFile("../../../contracts/plugin-" + name + "-rule.json")
		if err != nil {
			t.Fatal(err)
		}
		var rule transactionrules.Rule
		if err = json.Unmarshal(raw, &rule); err != nil {
			t.Fatal(err)
		}
		if rule.Settlement != nil {
			rule.Settlement.Capacity.Currency = transactionrules.AuthorityRef{Path: rule.Settlement.Capacity.Direction.Path, Field: "currency"}
			rule.Settlement.Request.Currency = transactionrules.AuthorityRef{Path: rule.Settlement.Request.Direction.Path, Field: "currency"}
			rule.Settlement.Actual.Currency = transactionrules.AuthorityRef{Path: rule.Settlement.Actual.Direction.Path, Field: "currency"}
			rule.Settlement.Capacity.Unit = transactionrules.AuthorityRef{Path: rule.Settlement.Capacity.Direction.Path[:1], Field: "unit"}
			rule.Settlement.Request.Unit = transactionrules.AuthorityRef{Path: rule.Settlement.Request.Direction.Path[:1], Field: "unit"}
			rule.Settlement.Actual.Unit = transactionrules.AuthorityRef{Path: rule.Settlement.Actual.Direction.Path[:1], Field: "unit"}
			if scoped {
				rule.Settlement.Capacity.Material = transactionrules.AuthorityRef{Path: rule.Settlement.Capacity.Direction.Path[:1], Field: "material"}
				rule.Settlement.Request.Material = transactionrules.AuthorityRef{Path: rule.Settlement.Request.Direction.Path[:1], Field: "material"}
				rule.Settlement.Actual.Material = transactionrules.AuthorityRef{Path: rule.Settlement.Actual.Direction.Path[:1], Field: "material"}
			}
		}
		d.Rules = append(d.Rules, rule)
	}
	// Protect face lines and headers without a second history API or self-FK.
	lineEvidence := d.Rules[2]
	lineSpec := *lineEvidence.Evidence
	lineEvidence.ID = "line_evidence"
	lineEvidence.Entity = "actual_lines"
	lineSpec.OwnerPath = []transactionrules.RelationStep{{Field: "invoice", Target: "invoices"}}
	lineSpec.Fields = []string{"invoice", "purpose", "quantity", "net", "tax", "gross"}
	lineEvidence.Evidence = &lineSpec
	headEvidence := lineEvidence
	headSpec := lineSpec
	headEvidence.ID = "header_evidence"
	headEvidence.Entity = "invoices"
	headSpec.OwnerPath = []transactionrules.RelationStep{}
	headSpec.Fields = []string{"number", "status", "direction", "amount_convention", "currency"}
	headEvidence.Evidence = &headSpec
	d.Rules = append(d.Rules, lineEvidence, headEvidence)
	if scoped {
		for _, entry := range []struct {
			id, entity, first string
			left, right       []transactionrules.RelationStep
		}{
			{"allocation_contract", "allocations", "original", []transactionrules.RelationStep{{Field: "original", Target: "allocations"}, {Field: "line", Target: "actual_lines"}, {Field: "invoice", Target: "invoices"}, {Field: "agreement", Target: "agreements"}}, []transactionrules.RelationStep{{Field: "line", Target: "actual_lines"}, {Field: "invoice", Target: "invoices"}, {Field: "agreement", Target: "agreements"}}},
			{"request_contract", "request_sources", "original", []transactionrules.RelationStep{{Field: "original", Target: "allocations"}, {Field: "line", Target: "actual_lines"}, {Field: "invoice", Target: "invoices"}, {Field: "agreement", Target: "agreements"}}, []transactionrules.RelationStep{{Field: "line", Target: "request_lines"}, {Field: "request", Target: "requests"}, {Field: "agreement", Target: "agreements"}}},
			{"source_contract", "allocations", "application_source", []transactionrules.RelationStep{{Field: "application_source", Target: "request_sources"}, {Field: "line", Target: "request_lines"}, {Field: "request", Target: "requests"}, {Field: "agreement", Target: "agreements"}}, []transactionrules.RelationStep{{Field: "line", Target: "actual_lines"}, {Field: "invoice", Target: "invoices"}, {Field: "agreement", Target: "agreements"}}},
		} {
			d.Rules = append(d.Rules, transactionrules.Rule{ID: entry.id, Kind: "sameTarget", Entity: entry.entity, WhenPresent: entry.first, PathMatch: transactionrules.PathMatch{Left: entry.left, Right: entry.right}})
		}
	}
	if normal {
		for _, entity := range []string{"obligations", "plan_items"} {
			d.Decimals = append(d.Decimals, transactionrules.Decimal{Entity: entity, Field: "quantity", Format: exactdecimal.Format{Storage: "decimalText", Digits: 38}, Sign: "nonnegative"})
		}
		raw, _ := json.Marshal(d.Rules[1])
		var rule transactionrules.Rule
		if err := json.Unmarshal(raw, &rule); err != nil {
			t.Fatal(err)
		}
		rule.ID = "normal_invoice_capacity"
		spec := rule.Settlement
		spec.Capacity = transactionrules.SettlementCapacity{Entity: "obligations", Quantity: "quantity", Currency: transactionrules.AuthorityRef{Field: "currency"}, Unit: transactionrules.AuthorityRef{Field: "unit"}}
		spec.Request.Directions = []string{"blue"}
		spec.Request.Purposes = nil
		spec.Request.Purpose = transactionrules.AuthorityRef{}
		spec.Request.Capacity = "obligation"
		spec.Request.PlanItem = "plan_item"
		spec.Request.Net = ""
		spec.Request.Tax = ""
		spec.Request.Gross = ""
		spec.Actual.Directions = []string{"blue"}
		spec.Actual.Purposes = nil
		spec.Actual.Purpose = transactionrules.AuthorityRef{}
		spec.Actual.ZeroQuantityPurposes = nil
		spec.Actual.Capacity = "obligation"
		spec.Actual.PlanItem = "plan_item"
		spec.Actual.Net = ""
		spec.Actual.Tax = ""
		spec.Actual.Gross = ""
		spec.Adjustment.CloseNet = ""
		spec.Adjustment.CloseTax = ""
		spec.Adjustment.CloseGross = ""
		spec.Plans = &transactionrules.ReservationPlans{Entity: "plan_items", Quantity: "quantity", Scope: "scope", LogicalItem: transactionrules.ReservationLogical{Field: "logical_item", Target: "logical_items"}, Share: "share", CurrentWhen: transactionrules.PathMatch{Left: []transactionrules.RelationStep{{Field: "plan_version", Target: "plan_versions"}}, Right: []transactionrules.RelationStep{{Field: "plan_version", Target: "plan_versions"}, {Field: "series", Target: "series"}, {Field: "current_version", Target: "plan_versions"}}}}
		d.Rules = append(d.Rules, rule)
	}
	store := pluginstore.New(pb, workspace)
	raw, err := store.GetInstallation(context.Background(), input.ProjectKey, input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err = json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	obj["manifest"].(map[string]any)["permissions"].(map[string]any)["transactions"] = d
	previous := input.InstallationRevision
	input.InstallationRevision++
	obj["revision"] = input.InstallationRevision
	raw, _ = json.Marshal(obj)
	if _, err = store.SaveInstallation(context.Background(), raw, &previous); err != nil {
		t.Fatal(err)
	}
	created, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": created.(modelPreview).RequestID})
	if err != nil {
		t.Fatal(err)
	}
	f := &transactionFixture{pb: pb, workspace: workspace, input: input, model: completed.(pluginmodel.State), domain: domain, kernel: mutation.New(pb, mutation.MetadataSchemaSource{}), epoch: 1, declaration: d}
	if _, err = f.call("transaction.configure", map[string]any{"declaration": d}); err != nil {
		t.Fatal(err)
	}
	return f
}

func invoiceCreate(entity, id string, values map[string]any) transactionOperation {
	if entity == "invoices" {
		for i := 0; i < 45; i++ {
			values[fmt.Sprintf("budget_%d", i)] = nil
		}
	}
	if entity == "adjustments" {
		for _, field := range []string{"quantity", "net", "tax", "gross"} {
			if _, present := values[field]; !present {
				values[field] = nil
			}
		}
	}
	return transactionOperation{Entity: entity, Kind: "create", TempID: id, Values: values}
}
func invoicePlan(ops ...transactionOperation) transactionPlan {
	return transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: ops}
}
func invoiceMeasures(q, n, t, g string) map[string]any {
	return map[string]any{"quantity": q, "net": n, "tax": t, "gross": g}
}
func invoiceFace(id, direction, purpose, q, n, t, g string, source, original any, allocated bool) transactionPlan {
	line := invoiceMeasures(q, n, t, g)
	line["invoice"] = regressionRef("invoices", id)
	line["unit"] = "piece"
	line["purpose"] = nil
	if purpose != "" {
		line["purpose"] = purpose
	}
	plan := invoicePlan(invoiceCreate("invoices", id, map[string]any{"number": id, "status": "issued", "direction": direction, "amount_convention": "signed", "currency": "CNY"}), invoiceCreate("actual_lines", id, line))
	if allocated {
		allocation := invoiceMeasures(q, n, t, g)
		allocation["line"] = regressionRef("actual_lines", id)
		allocation["original"] = original
		allocation["application_source"] = source
		plan.Operations = append(plan.Operations, invoiceCreate("allocations", id, allocation))
	}
	return plan
}
func invoiceRequest(id, purpose, basis, q, n, t, g string, original any) transactionPlan {
	line := invoiceMeasures(q, n, t, g)
	line["request"] = regressionRef("requests", id)
	line["unit"] = "piece"
	source := invoiceMeasures(q, n, t, g)
	source["line"] = regressionRef("request_lines", id)
	source["original"] = original
	return invoicePlan(invoiceCreate("requests", id, map[string]any{"status": "open", "direction": "red", "purpose": purpose, "basis": basis, "currency": "CNY"}), invoiceCreate("request_lines", id, line), invoiceCreate("request_sources", id, source))
}

func TestInvoiceAuthorityNaturalAtomicAmountSettlement(t *testing.T) {
	f := newInvoiceAuthorityFixture(t)
	blue := f.submit(t, invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)).NewIDs["allocations"]["blue"]
	source := f.submit(t, invoiceRequest("request", "priceDiscount", "amount", "0", "100.00", "0.00", "100.00", blue)).NewIDs["request_sources"]["request"]
	plan := invoiceFace("discount", "red", "priceDiscount", "0", "-100.00", "0.00", "-100.00", source, blue, true)
	regressionReject(t, f, plan, "transaction.settlement_exceeded")
	event := invoiceMeasures("0", "100.00", "0.00", "100.00")
	event["kind"] = "fulfillAmount"
	event["source"] = source
	event["allocation"] = regressionRef("allocations", "discount")
	event["note"] = "明确兑现"
	event["close_remaining"] = false
	plan.Operations = append(plan.Operations, invoiceCreate("adjustments", "fulfilled", event))
	result := f.submit(t, plan)
	if result.Receipt.ChangeSetID == "" {
		t.Fatal("missing atomic change set")
	}
	// A close after fulfillment preserves historical execution.
	closed := invoiceMeasures("0", "0.00", "0.00", "0.00")
	closed["kind"] = "closeRemaining"
	closed["source"] = source
	closed["note"] = "剩余范围为零"
	closed["close_remaining"] = true
	f.submit(t, invoicePlan(invoiceCreate("adjustments", "closed", closed)))
	// The next amount-only consumption cannot bypass the close with Q=0.
	regressionReject(t, f, invoiceFace("late", "red", "priceDiscount", "0", "-0.01", "0.00", "-0.01", source, blue, true), "transaction.settlement_invalid")
}

func TestInvoiceAuthorityIndependentPreviewsCompete(t *testing.T) {
	for _, actual := range []bool{false, true} {
		t.Run(fmt.Sprint("actual-", actual), func(t *testing.T) {
			f := newInvoiceAuthorityFixture(t)
			blue := f.submit(t, invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)).NewIDs["allocations"]["blue"]
			previews := make([]transactionPreview, 2)
			for i, id := range []string{"one", "two"} {
				plan := invoiceRequest(id, "physicalReturn", "quantity", "6", "60.00", "0.00", "60.00", blue)
				if actual {
					plan = invoiceFace(id, "red", "physicalReturn", "-6", "-60.00", "0.00", "-60.00", nil, blue, true)
				}
				preview, err := f.call("transaction.preview", map[string]any{"plan": plan})
				if err != nil {
					t.Fatal(err)
				}
				previews[i] = preview.(transactionPreview)
			}
			var group sync.WaitGroup
			errs := make([]error, 2)
			results := make([]any, 2)
			for i := range previews {
				group.Add(1)
				go func() {
					defer group.Done()
					results[i], errs[i] = f.call("transaction.commit", map[string]any{"requestId": previews[i].RequestID})
				}()
			}
			group.Wait()
			succeeded := 0
			for i, err := range errs {
				if err == nil {
					succeeded++
					if results[i].(transactionResult).Receipt.ChangeSetID == "" {
						t.Fatal("missing receipt")
					}
				} else {
					var product *mutation.ProductError
					if !errors.As(err, &product) || product.Code != "transaction.settlement_exceeded" {
						t.Fatalf("unexpected contender rejection: %v", err)
					}
				}
			}
			if succeeded != 1 {
				t.Fatalf("two independent previews consumed 12 of 10: success=%d errors=%v", succeeded, errs)
			}
		})
	}
}

func TestInvoiceAuthorityRawPreservationAndFourDimensions(t *testing.T) {
	f := newInvoiceAuthorityFixture(t)
	blue := f.submit(t, invoiceFace("blue", "blue", "", "10", "100.00", "10.00", "110.00", nil, nil, true)).NewIDs["allocations"]["blue"]
	// An anomalous raw red sign is retained, while its internal attribution fails.
	raw := f.submit(t, invoiceFace("anomaly", "red", "physicalReturn", "2", "20.00", "2.00", "22.00", nil, nil, false))
	if _, ok := raw.NewIDs["invoices"]["anomaly"]; !ok {
		t.Fatal("raw invoice lost")
	}
	regressionReject(t, f, invoiceFace("positive-red", "red", "physicalReturn", "2", "20.00", "2.00", "22.00", nil, blue, true), "transaction.allocation_mismatch")
	regressionReject(t, f, invoiceFace("invalid", "red", "physicalReturn", "-2", "-20.00", "-2.00", "-22.00", nil, nil, true), "transaction.settlement_invalid")
	f.submit(t, invoiceFace("return", "red", "physicalReturn", "-6", "-60.00", "-6.00", "-66.00", nil, blue, true))
	// A different red purpose uses the same original budget, including tax.
	regressionReject(t, f, invoiceFace("tax-over", "red", "priceDiscount", "0", "-1.00", "-4.01", "-5.01", nil, blue, true), "transaction.settlement_exceeded")
	f.submit(t, invoiceFace("discount", "red", "priceDiscount", "0", "-40.00", "-4.00", "-44.00", nil, blue, true))
	regressionReject(t, f, invoiceFace("cent-over", "red", "priceDiscount", "0", "-0.01", "0.00", "-0.01", nil, blue, true), "transaction.settlement_exceeded")
}

func TestInvoiceAuthorityNativeCorrectionAndHistory(t *testing.T) {
	f := newInvoiceAuthorityFixture(t)
	initial := f.submit(t, invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true))
	id := initial.NewIDs["invoices"]["blue"]
	_, digest := regressionRow(t, f, "invoices", id)
	before := transactionBusinessSnapshot(t, f)
	response := reversalNative(t, f, "native-number-change", "invoices", map[string]any{"kind": "update", "primaryKey": id, "expectedDigest": digest, "values": map[string]any{"number": "corrected"}})
	requireHTTPNotCommitted(t, response, "transaction.evidence_required")
	if transactionBusinessSnapshot(t, f) != before {
		t.Fatal("rejected native edit changed data")
	}
	history, err := audit.New(f.pb, f.kernel)
	if err != nil {
		t.Fatal(err)
	}
	table := f.model.Bindings["invoices"].TableID
	page, err := history.ReadChangeSets(t.Context(), audit.ReadParams{TableID: table, ItemID: &id, Scope: "row", Limit: 10})
	if err != nil || len(page.ChangeSets) != 1 {
		t.Fatalf("history %v %v", page, err)
	}
	original := page.ChangeSets[0].RootRevisionID
	corrected := f.submit(t, invoicePlan(invoiceCreate("adjustments", "correction", map[string]any{"invoice": id, "kind": "correction", "note": "票号重新核对", "date": "2026-11-06"}), transactionOperation{Entity: "invoices", Kind: "update", RecordID: id, ExpectedDigest: &digest, Values: map[string]any{"number": "corrected", "latest_evidence": regressionRef("adjustments", "correction")}}))
	eventID := corrected.NewIDs["adjustments"]["correction"]
	for _, entry := range []struct{ entity, id string }{{"invoices", id}, {"adjustments", eventID}} {
		p, err := history.ReadChangeSets(t.Context(), audit.ReadParams{TableID: f.model.Bindings[entry.entity].TableID, ItemID: &entry.id, Scope: "row", Limit: 10})
		if err != nil || len(p.ChangeSets) == 0 || p.ChangeSets[0].ChangeSetID != corrected.Receipt.ChangeSetID {
			t.Fatalf("correction evidence not in same change set: %v %v", p, err)
		}
		if entry.entity == "invoices" {
			changes := append([]audit.ScalarFieldChange{}, p.ChangeSets[0].ScalarChanges...)
			for _, record := range p.ChangeSets[0].RecordChanges {
				changes = append(changes, record.ScalarChanges...)
			}
			found := false
			for _, change := range changes {
				if change.Before == "blue" && change.After == "corrected" {
					found = true
				}
			}
			if !found {
				t.Fatal("same change set did not retain the original and corrected face facts")
			}
		}
	}
	restored, err := history.PreviewRestore(t.Context(), audit.PreviewParams{TableID: table, ItemID: id, TargetRevision: original, Scope: "row"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = history.ApplyRestore(t.Context(), audit.ApplyParams{TableID: table, ItemID: id, Token: restored.Token}); err == nil {
		t.Fatal("history restore bypassed evidence")
	}
	_, eventDigest := regressionRow(t, f, "adjustments", eventID)
	response = reversalNative(t, f, "native-evidence-edit", "adjustments", map[string]any{"kind": "update", "primaryKey": eventID, "expectedDigest": eventDigest, "values": map[string]any{"note": "改写旧依据"}})
	requireHTTPNotCommitted(t, response, "transaction.evidence_frozen")
}

func TestInvoiceAuthorityDecimalBudgetRetainsRegistration(t *testing.T) {
	f := newInvoiceAuthorityFixture(t)
	f.submit(t, invoiceFace("kept", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true))
	before := transactionBusinessSnapshot(t, f)
	previousRules, err := transactionrules.Load(t.Context(), f.pb)
	if err != nil {
		t.Fatal(err)
	}
	declaration := f.declaration
	if len(declaration.Decimals) != 65 {
		t.Fatal("fixture must register exactly 65 mappings")
	}
	declaration.Version++
	declaration.Decimals = append(declaration.Decimals, transactionrules.Decimal{Entity: "invoices", Field: "number", Format: exactdecimal.Format{Storage: "decimalText", Digits: 38}})
	if err := declaration.Validate(); err == nil || err.Error() != "transaction.rules_invalid" {
		t.Fatalf("66 mapping authority budget accepted: %v", err)
	}
	if _, err := f.call("transaction.configure", map[string]any{"declaration": declaration}); err == nil {
		t.Fatal("66 mappings accepted")
	}
	if transactionBusinessSnapshot(t, f) != before {
		t.Fatal("rejected declaration damaged old data")
	}
	currentRules, err := transactionrules.Load(t.Context(), f.pb)
	if err != nil || !reflect.DeepEqual(previousRules, currentRules) {
		t.Fatalf("rejected configure replaced old registration: %v", err)
	}
	if _, err := f.call("transaction.configure", map[string]any{"declaration": f.declaration}); err != nil {
		t.Fatalf("65 mapping registration lost: %v", err)
	}
}

func TestInvoiceAuthorityNormalPlansShareObligationCapacity(t *testing.T) {
	f := newInvoiceAuthorityFixture(t, true)
	seeded := f.submit(t, invoicePlan(
		invoiceCreate("obligations", "o", map[string]any{"quantity": "10", "currency": "CNY", "unit": "piece"}),
		invoiceCreate("series", "series", map[string]any{"label": "series", "current_version": regressionRef("plan_versions", "v")}),
		invoiceCreate("plan_versions", "v", map[string]any{"label": "v", "series": regressionRef("series", "series")}),
		invoiceCreate("logical_items", "logical", map[string]any{"label": "logical"}),
		invoiceCreate("plan_items", "p", map[string]any{"quantity": "8", "share": 0, "plan_version": regressionRef("plan_versions", "v"), "logical_item": regressionRef("logical_items", "logical"), "scope": regressionRef("obligations", "o")}),
	))
	obligation := seeded.NewIDs["obligations"]["o"]
	item := seeded.NewIDs["plan_items"]["p"]
	request := func(id, q string, planItem any) transactionPlan {
		p := invoiceRequest(id, "normal", "quantity", q, q+".00", "0.00", q+".00", nil)
		p.Operations[0].Values["direction"] = "blue"
		p.Operations[2].Values["obligation"] = obligation
		p.Operations[2].Values["plan_item"] = planItem
		return p
	}
	regressionReject(t, f, request("over", "6", nil), "transaction.settlement_exceeded")
	f.submit(t, request("direct", "2", nil))
	reserved := f.submit(t, request("planned", "8", item)).NewIDs["request_sources"]["planned"]
	actual := invoiceFace("blue", "blue", "", "6", "6.00", "0.00", "6.00", reserved, nil, true)
	actual.Operations[2].Values["obligation"] = obligation
	f.submit(t, actual)
	regressionReject(t, f, request("extra", "1", nil), "transaction.settlement_exceeded")
}

func TestInvoiceAuthorityTypedScopeAndContractRelations(t *testing.T) {
	f := newInvoiceAuthorityFixture(t, false, true)
	ids := f.submit(t, invoicePlan(invoiceCreate("agreements", "a", map[string]any{"label": "a"}), invoiceCreate("agreements", "b", map[string]any{"label": "b"}), invoiceCreate("materials", "a", map[string]any{"label": "a"}), invoiceCreate("materials", "b", map[string]any{"label": "b"}))).NewIDs
	agreement, otherAgreement := ids["agreements"]["a"], ids["agreements"]["b"]
	material, otherMaterial := ids["materials"]["a"], ids["materials"]["b"]
	bluePlan := invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)
	bluePlan.Operations[0].Values["agreement"] = agreement
	bluePlan.Operations[1].Values["material"] = material
	blue := f.submit(t, bluePlan).NewIDs["allocations"]["blue"]
	request := invoiceRequest("request", "physicalReturn", "quantity", "10", "100.00", "0.00", "100.00", blue)
	request.Operations[0].Values["agreement"] = agreement
	request.Operations[1].Values["material"] = material
	source := f.submit(t, request).NewIDs["request_sources"]["request"]
	for _, scenario := range []string{"agreement", "currency", "unit", "material"} {
		plan := invoiceFace("wrong-"+scenario, "red", "physicalReturn", "-4", "-40.00", "0.00", "-40.00", source, blue, true)
		plan.Operations[0].Values["agreement"] = agreement
		plan.Operations[1].Values["material"] = material
		code := "transaction.settlement_invalid"
		switch scenario {
		case "agreement":
			plan.Operations[0].Values["agreement"] = otherAgreement
			code = "transaction.same_target_violation"
		case "currency":
			plan.Operations[0].Values["currency"] = "USD"
		case "unit":
			plan.Operations[1].Values["unit"] = "kg"
		case "material":
			plan.Operations[1].Values["material"] = otherMaterial
		}
		regressionReject(t, f, plan, code)
	}
	// Explicitly unmapped raw material is retained; the user selected the exact
	// source and capacity for this internal share.
	matched := invoiceFace("matched", "red", "physicalReturn", "-4", "-40.00", "0.00", "-40.00", source, blue, true)
	matched.Operations[0].Values["agreement"] = agreement
	matched.Operations[1].Values["material"] = nil
	f.submit(t, matched)
	close := invoiceMeasures("6", "60.00", "0.00", "60.00")
	close["kind"] = "closeRemaining"
	close["source"] = source
	close["note"] = "明确关闭剩余"
	close["close_remaining"] = true
	f.submit(t, invoicePlan(invoiceCreate("adjustments", "closed", close)))
	amountRequest := invoiceRequest("amount", "priceDiscount", "amount", "0", "60.00", "0.00", "60.00", blue)
	amountRequest.Operations[0].Values["agreement"] = agreement
	amountRequest.Operations[1].Values["material"] = otherMaterial
	amountRequest.Operations[1].Values["unit"] = "kg"
	amountSource := f.submit(t, amountRequest).NewIDs["request_sources"]["amount"]
	amount := invoiceFace("discount", "red", "priceDiscount", "0", "-60.00", "0.00", "-60.00", amountSource, blue, true)
	amount.Operations[0].Values["agreement"] = agreement
	amount.Operations[1].Values["unit"] = nil
	amount.Operations[1].Values["material"] = nil
	event := invoiceMeasures("0", "60.00", "0.00", "60.00")
	event["kind"] = "fulfillAmount"
	event["source"] = amountSource
	event["allocation"] = regressionRef("allocations", "discount")
	event["note"] = "金额兑现"
	event["close_remaining"] = false
	amount.Operations = append(amount.Operations, invoiceCreate("adjustments", "fulfilled", event))
	f.submit(t, amount)
}

func TestInvoiceAuthoritySignedBlueMoneyAndQuantityDirection(t *testing.T) {
	f := newInvoiceAuthorityFixture(t, true)
	obligation := f.submit(t, invoicePlan(invoiceCreate("obligations", "o", map[string]any{"quantity": "10", "currency": "CNY", "unit": "piece"}))).NewIDs["obligations"]["o"]
	f.submit(t, invoiceFace("raw-negative", "blue", "", "-10", "-100.00", "0.00", "-100.00", nil, nil, false))
	negativeQuantity := invoiceFace("bad-quantity", "blue", "", "-10", "-100.00", "0.00", "-100.00", nil, nil, true)
	negativeQuantity.Operations[2].Values["obligation"] = obligation
	regressionReject(t, f, negativeQuantity, "transaction.allocation_mismatch")
	// Signed blue money remains signed while the quantity-only normal bucket
	// consumes the legal positive quantity, without laundering negative Q via ABS.
	signedMoney := invoiceFace("signed-money", "blue", "", "10", "-100.00", "0.00", "-100.00", nil, nil, true)
	signedMoney.Operations[2].Values["obligation"] = obligation
	f.submit(t, signedMoney)
}

func TestInvoiceAuthorityFormalRequestPurposeRequiresMatchingBasis(t *testing.T) {
	f := newInvoiceAuthorityFixture(t)
	blue := f.submit(t, invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)).NewIDs["allocations"]["blue"]
	regressionReject(t, f, invoiceRequest("bad-discount", "priceDiscount", "quantity", "10", "100.00", "0.00", "100.00", blue), "transaction.settlement_invalid")
	regressionReject(t, f, invoiceRequest("bad-return", "physicalReturn", "amount", "0", "100.00", "0.00", "100.00", blue), "transaction.settlement_invalid")
	draft := invoiceRequest("draft", "priceDiscount", "quantity", "10", "100.00", "0.00", "100.00", blue)
	draft.Operations[0].Values["status"] = "draft"
	draft.Operations[0].Values["basis"] = nil
	f.submit(t, draft)
}

func TestInvoiceAuthoritySameInvoiceLineMoveNeedsNewEvidence(t *testing.T) {
	f := newInvoiceAuthorityFixture(t)
	plan := invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)
	second := invoiceMeasures("10", "100.00", "0.00", "100.00")
	second["invoice"] = regressionRef("invoices", "blue")
	second["purpose"] = nil
	second["unit"] = "piece"
	plan.Operations = append(plan.Operations, invoiceCreate("actual_lines", "second", second))
	ids := f.submit(t, plan).NewIDs
	allocation, secondLine, invoice := ids["allocations"]["blue"], ids["actual_lines"]["second"], ids["invoices"]["blue"]
	_, digest := regressionRow(t, f, "allocations", allocation)
	before := transactionBusinessSnapshot(t, f)
	response := reversalNative(t, f, "same-invoice-line-move", "allocations", map[string]any{"kind": "update", "primaryKey": allocation, "expectedDigest": digest, "values": map[string]any{"line": secondLine}})
	requireHTTPNotCommitted(t, response, "transaction.evidence_required")
	if before != transactionBusinessSnapshot(t, f) {
		t.Fatal("rejected reattribution changed facts")
	}
	_, headDigest := regressionRow(t, f, "invoices", invoice)
	f.submit(t, invoicePlan(invoiceCreate("adjustments", "e", map[string]any{"invoice": invoice, "kind": "correction", "note": "明确更正内部票面行归属"}), transactionOperation{Entity: "invoices", Kind: "update", RecordID: invoice, ExpectedDigest: &headDigest, Values: map[string]any{"latest_evidence": regressionRef("adjustments", "e")}}, transactionOperation{Entity: "allocations", Kind: "update", RecordID: allocation, ExpectedDigest: &digest, Values: map[string]any{"line": secondLine}}))
}
