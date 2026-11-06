package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
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
	legacy := len(withPlans) > 2 && withPlans[2]
	optionalContracts := len(withPlans) > 3 && withPlans[3]
	relationCases := len(withPlans) > 4 && withPlans[4]
	groupContracts := len(withPlans) > 5 && withPlans[5]
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
		if relationCases {
			for _, key := range []string{"projects", "packages", "obligations", "plan_items", "source_versions", "delivery_lines"} {
				m.Entities = append(m.Entities, pluginmodel.Entity{Key: key, Label: key, Operations: []string{"read", "query", "create", "update", "delete"}, Fields: []pluginmodel.Field{{Key: "label", Label: "label", Type: v2.LogicalText}}})
			}
			m.Entities[2].Fields = append(m.Entities[2].Fields, pluginmodel.Field{Key: "source_kind", Label: "source kind", Type: v2.LogicalText})
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
		if relationCases {
			for _, pair := range []struct{ field, target string }{{"project", "projects"}, {"package", "packages"}, {"agreement", "agreements"}, {"obligation", "obligations"}, {"plan_item", "plan_items"}, {"version", "source_versions"}, {"application_source", "request_sources"}, {"delivery_line", "delivery_lines"}, {"actual_line", "actual_lines"}} {
				relation("request_sources", pair.field, pair.target, "kind_"+pair.field+"_sources")
			}
		}
		if groupContracts {
			relation("allocations", "agreement", "agreements", "allocation_snapshots")
			if !relationCases {
				relation("request_sources", "agreement", "agreements", "source_snapshots")
			}
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
	if legacy {
		fillerCount = 44
	}
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
	lineSpec.AllowInsert = false
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
			rule := transactionrules.Rule{ID: entry.id, Kind: "sameTarget", Entity: entry.entity, WhenPresent: entry.first, PathMatch: transactionrules.PathMatch{Left: entry.left, Right: entry.right}}
			if optionalContracts {
				rule.SkipEmptyLeftEnd = true
				rule.SkipEmptyRightEnd = true
			}
			d.Rules = append(d.Rules, rule)
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
	d.Rules = append(d.Rules, transactionrules.Rule{ID: "request_allocation_complete", Kind: "allocationBalance", Entity: "request_sources", Allocation: &transactionrules.AllocationSpec{RequireCompleteWhenAllocated: true, Owner: "line", Parent: transactionrules.AllocationParent{Entity: "request_lines", Quantity: "quantity", Net: "net", Tax: "tax", Gross: "gross"}, Quantity: "quantity", Net: "net", Tax: "tax", Gross: "gross", State: transactionrules.AuthorityRef{Path: []transactionrules.RelationStep{{Field: "line", Target: "request_lines"}, {Field: "request", Target: "requests"}}, Field: "status"}, DraftStates: []string{"draft"}, Purpose: transactionrules.AuthorityRef{Path: []transactionrules.RelationStep{{Field: "line", Target: "request_lines"}, {Field: "request", Target: "requests"}}, Field: "basis"}, ZeroQuantityPurposes: []string{"amount"}}})
	if relationCases || groupContracts {
		name := "state-relations"
		if groupContracts {
			name = "group-same-target"
		}
		raw, err := os.ReadFile("../../../contracts/plugin-" + name + "-rule.json")
		if err != nil {
			t.Fatal(err)
		}
		var rule transactionrules.Rule
		if err = json.Unmarshal(raw, &rule); err != nil {
			t.Fatal(err)
		}
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

func TestInvoiceAuthorityReviewSharedAdjustmentRoles(t *testing.T) {
	f := newInvoiceAuthorityFixture(t, true)
	obligation := f.submit(t, invoicePlan(invoiceCreate("obligations", "o", map[string]any{"quantity": "10", "currency": "CNY", "unit": "piece"}))).NewIDs["obligations"]["o"]
	bluePlan := invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)
	bluePlan.Operations[2].Values["obligation"] = obligation
	blue := f.submit(t, bluePlan).NewIDs["allocations"]["blue"]
	source := f.submit(t, invoiceRequest("r", "priceDiscount", "amount", "0", "100.00", "0.00", "100.00", blue)).NewIDs["request_sources"]["r"]
	other := f.submit(t, invoiceRequest("other", "priceDiscount", "amount", "0", "0.00", "0.00", "0.00", blue)).NewIDs["request_sources"]["other"]
	plan := invoiceFace("red", "red", "priceDiscount", "0", "-100.00", "0.00", "-100.00", source, blue, true)
	event := invoiceMeasures("0", "100.00", "0.00", "100.00")
	event["kind"] = "fulfillAmount"
	event["source"] = source
	event["allocation"] = regressionRef("allocations", "red")
	event["note"] = "两个结算桶共享事件表"
	event["close_remaining"] = false
	plan.Operations = append(plan.Operations, invoiceCreate("adjustments", "fulfilled", event))
	// A real but wrong allocation remains a rejected binding.
	event["allocation"] = blue
	regressionReject(t, f, plan, "transaction.settlement_invalid")
	event["allocation"] = regressionRef("allocations", "red")
	event["source"] = other
	regressionReject(t, f, plan, "transaction.settlement_invalid")
	event["source"] = source
	f.submit(t, plan)
}

func TestInvoiceAuthorityReviewSequentialQuantityClose(t *testing.T) {
	for _, tc := range []struct{ name, q, n, tax, g, first, last, rn, rt, rg string }{{"normal", "10", "100.00", "0.00", "100.00", "4", "6", "60.00", "0.00", "60.00"}, {"round-down", "3", "0.01", "0.01", "0.02", "1", "2", "0.01", "0.01", "0.02"}, {"round-up", "3", "0.01", "0.01", "0.02", "2", "1", "0.00", "0.00", "0.00"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInvoiceAuthorityFixture(t)
			blue := f.submit(t, invoiceFace("blue", "blue", "", "10", "100.00", "100.00", "200.00", nil, nil, true)).NewIDs["allocations"]["blue"]
			source := f.submit(t, invoiceRequest("request", "physicalReturn", "quantity", tc.q, tc.n, tc.tax, tc.g, blue)).NewIDs["request_sources"]["request"]
			partial := map[string]any{"kind": "sourceClose", "source": source, "close_remaining": false, "quantity": tc.first, "net": nil, "tax": nil, "gross": nil, "note": "明确关闭数量"}
			eventID := f.submit(t, invoicePlan(invoiceCreate("adjustments", "partial", partial))).NewIDs["adjustments"]["partial"]
			old, oldDigest := regressionRow(t, f, "adjustments", eventID)
			remaining := invoiceMeasures(tc.last, tc.rn, tc.rt, tc.rg)
			remaining["kind"] = "closeRemaining"
			remaining["source"] = source
			remaining["close_remaining"] = true
			remaining["note"] = "捕获当前剩余四范围"
			f.submit(t, invoicePlan(invoiceCreate("adjustments", "remaining", remaining)))
			kept, digest := regressionRow(t, f, "adjustments", eventID)
			if !reflect.DeepEqual(old, kept) || digest != oldDigest {
				t.Fatal("later close rewrote historical quantity-only fact")
			}
		})
	}
}

func TestInvoiceAuthorityReviewOriginalConfirmationAndDowngrade(t *testing.T) {
	f := newInvoiceAuthorityFixture(t)
	bluePlan := invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)
	bluePlan.Operations[0].Values["status"] = "draft"
	ids := f.submit(t, bluePlan).NewIDs
	blue, head := ids["allocations"]["blue"], ids["invoices"]["blue"]
	regressionReject(t, f, invoiceRequest("too-early", "physicalReturn", "quantity", "10", "100.00", "0.00", "100.00", blue), "transaction.settlement_invalid")
	regressionReject(t, f, invoiceFace("too-early-actual", "red", "physicalReturn", "-1", "-10.00", "0.00", "-10.00", nil, blue, true), "transaction.settlement_invalid")
	_, digest := regressionRow(t, f, "invoices", head)
	confirmation := invoicePlan(invoiceCreate("adjustments", "confirm", map[string]any{"invoice": head, "kind": "correction", "note": "核对真实蓝票确认事实"}), transactionOperation{Entity: "invoices", Kind: "update", RecordID: head, ExpectedDigest: &digest, Values: map[string]any{"status": "issued", "latest_evidence": regressionRef("adjustments", "confirm")}})
	request := invoiceRequest("r", "physicalReturn", "quantity", "10", "100.00", "0.00", "100.00", blue)
	actual := invoiceFace("red", "red", "physicalReturn", "-10", "-100.00", "0.00", "-100.00", regressionRef("request_sources", "r"), blue, true)
	confirmation.Operations = append(confirmation.Operations, request.Operations...)
	confirmation.Operations = append(confirmation.Operations, actual.Operations...)
	f.submit(t, confirmation)
	_, digest = regressionRow(t, f, "invoices", head)
	downgrade := invoicePlan(invoiceCreate("adjustments", "downgrade", map[string]any{"invoice": head, "kind": "correction", "note": "尝试退回草稿"}), transactionOperation{Entity: "invoices", Kind: "update", RecordID: head, ExpectedDigest: &digest, Values: map[string]any{"status": "draft", "latest_evidence": regressionRef("adjustments", "downgrade")}})
	regressionReject(t, f, downgrade, "transaction.settlement_invalid")
}

func TestInvoiceAuthorityReviewPurposeProjection(t *testing.T) {
	f := newInvoiceAuthorityFixture(t)
	blue := f.submit(t, invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)).NewIDs["allocations"]["blue"]
	source := f.submit(t, invoiceRequest("r", "physicalReturn", "quantity", "10", "100.00", "0.00", "100.00", blue)).NewIDs["request_sources"]["r"]
	f.submit(t, invoiceFace("red", "red", "physicalReturn", "-6", "-60.00", "0.00", "-60.00", source, blue, true))
	rule := f.declaration.Rules[1]
	spec := *rule.Settlement
	rule.Settlement = &spec
	spec.Request.Purposes = nil
	spec.Actual.Purposes = nil
	declaration := f.declaration
	declaration.Rules = []transactionrules.Rule{rule}
	registration := transactionrules.Registration{WorkspaceID: f.workspace, PluginID: f.input.PluginID, InstallationID: f.input.InstallationID, Declaration: declaration, Bindings: f.model.Bindings}
	projected, err := registration.Read(t.Context(), f.pb)
	if err != nil {
		t.Fatal(err)
	}
	if err = registration.Validate(projected, projected); err != nil {
		t.Fatalf("declared purpose refs were missing from native read projection: %v", err)
	}
}

func TestInvoiceAuthorityReviewRealDecimalUpgrade(t *testing.T) {
	f := newInvoiceAuthorityFixture(t, false, false, true)
	if len(f.declaration.Decimals) != 64 {
		t.Fatal("legacy fixture must have 64 mappings")
	}
	id := f.submit(t, invoiceFace("kept", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)).NewIDs["invoices"]["kept"]
	oldRow, oldDigest := regressionRow(t, f, "invoices", id)
	oldRules, err := transactionrules.Load(t.Context(), f.pb)
	if err != nil {
		t.Fatal(err)
	}
	store := pluginstore.New(f.pb, f.workspace)
	commitUpgrade := func(declaration transactionrules.Declaration, version, hashDigit string) (json.RawMessage, error) {
		installed, err := store.GetInstallation(t.Context(), f.input.ProjectKey, f.input.PluginID)
		if err != nil {
			return nil, err
		}
		var object map[string]any
		if err = json.Unmarshal(installed, &object); err != nil {
			return nil, err
		}
		manifest := object["manifest"].(map[string]any)
		manifest["version"] = version
		manifest["permissions"].(map[string]any)["transactions"] = declaration
		packageHash := "sha256:" + strings.Repeat(hashDigit, 64)
		plan := map[string]any{"planId": "invoice-upgrade-" + version, "projectKey": f.input.ProjectKey, "projectRevision": "synthetic", "sourceType": "package", "sourceLocation": "host-managed", "packageHash": packageHash, "manifest": manifest, "schemas": map[string]any{}, "targetInstallationId": object["installationId"], "targetRevision": f.input.InstallationRevision}
		revision := map[string]any{"projectKey": f.input.ProjectKey, "pluginId": f.input.PluginID, "version": version, "packageHash": packageHash, "localPath": "synthetic.vtplugin", "manifest": manifest, "state": "current"}
		planRaw, _ := json.Marshal(plan)
		revisionRaw, _ := json.Marshal(revision)
		return store.CommitUpgrade(t.Context(), planRaw, revisionRaw)
	}
	upgraded := f.declaration
	upgraded.Version++
	upgraded.Decimals = append(append([]transactionrules.Decimal{}, upgraded.Decimals...), transactionrules.Decimal{Entity: "invoices", Field: "budget_44", Format: exactdecimal.Format{Storage: "decimalText", Digits: 38}, Nullable: true})
	installed, err := commitUpgrade(upgraded, "1.0.1", "6")
	if err != nil {
		t.Fatalf("real 64 to 65 package upgrade failed: %v", err)
	}
	var snapshot struct {
		Revision    int64  `json:"revision"`
		PackageHash string `json:"packageHash"`
	}
	if err = json.Unmarshal(installed, &snapshot); err != nil {
		t.Fatal(err)
	}
	f.input.InstallationRevision = snapshot.Revision
	f.input.PackageHash = strings.TrimPrefix(snapshot.PackageHash, "sha256:")
	preview, err := modelCall(t, f.pb, f.workspace, f.input, f.domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = modelCall(t, f.pb, f.workspace, f.input, f.domain, "model.commit", map[string]any{"requestId": preview.(modelPreview).RequestID}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.call("transaction.configure", map[string]any{"declaration": upgraded}); err != nil {
		t.Fatalf("65 mapping registration after package upgrade failed: %v", err)
	}
	rules, err := transactionrules.Load(t.Context(), f.pb)
	if err != nil || len(rules) != len(oldRules)+1 {
		t.Fatalf("upgrade lost historical registration: %v", err)
	}
	found := false
	for _, r := range rules {
		if reflect.DeepEqual(r, oldRules[0]) {
			found = true
		}
	}
	if !found {
		t.Fatal("old rule version was replaced")
	}
	row, digest := regressionRow(t, f, "invoices", id)
	if !reflect.DeepEqual(row, oldRow) || digest != oldDigest {
		t.Fatal("successful upgrade changed business row")
	}
	before := transactionBusinessSnapshot(t, f)
	oversized := upgraded
	oversized.Version++
	oversized.Decimals = append(append([]transactionrules.Decimal{}, oversized.Decimals...), transactionrules.Decimal{Entity: "invoices", Field: "number", Format: exactdecimal.Format{Storage: "decimalText", Digits: 38}})
	if _, err = commitUpgrade(oversized, "1.0.2", "7"); err == nil {
		t.Fatal("66 mappings entered actual package upgrade")
	} else {
		var invalid *pluginstore.Error
		if !errors.As(err, &invalid) || invalid.Code != "plugin.request_invalid" {
			t.Fatalf("unexpected upgrade rejection: %v", err)
		}
	}
	kept, err := store.GetInstallation(t.Context(), f.input.ProjectKey, f.input.PluginID)
	if err != nil || string(kept) != string(installed) {
		t.Fatalf("rejected upgrade replaced installed manifest: %v", err)
	}
	afterRules, err := transactionrules.Load(t.Context(), f.pb)
	if err != nil || !reflect.DeepEqual(rules, afterRules) || transactionBusinessSnapshot(t, f) != before {
		t.Fatalf("failed 66 upgrade changed old rules/data/audit: %v", err)
	}
}

func TestInvoiceAuthorityRawThenAllocationNeedsNoCorrectionEvent(t *testing.T) {
	f := newInvoiceAuthorityFixture(t)
	blue := f.submit(t, invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)).NewIDs["allocations"]["blue"]
	source := f.submit(t, invoiceRequest("request", "physicalReturn", "quantity", "10", "100.00", "0.00", "100.00", blue)).NewIDs["request_sources"]["request"]
	raw := invoiceFace("raw", "red", "physicalReturn", "-6", "-60.00", "0.00", "-60.00", nil, nil, false)
	otherLine := invoiceMeasures("-6", "-60.00", "0.00", "-60.00")
	otherLine["invoice"] = regressionRef("invoices", "raw")
	otherLine["purpose"] = "physicalReturn"
	otherLine["unit"] = "piece"
	raw.Operations = append(raw.Operations, invoiceCreate("actual_lines", "second", otherLine))
	ids := f.submit(t, raw).NewIDs
	values := invoiceMeasures("-6", "-60.00", "0.00", "-60.00")
	values["line"] = ids["actual_lines"]["raw"]
	values["original"] = blue
	values["application_source"] = source
	result := f.submit(t, invoicePlan(invoiceCreate("allocations", "attributed", values)))
	allocation := result.NewIDs["allocations"]["attributed"]
	registration := transactionrules.Registration{WorkspaceID: f.workspace, PluginID: f.input.PluginID, InstallationID: f.input.InstallationID, Declaration: f.declaration, Bindings: f.model.Bindings}
	images, err := registration.Read(t.Context(), f.pb)
	if err != nil || len(images["adjustments"]) != 0 {
		t.Fatalf("normal attribution fabricated a correction event: %v", err)
	}
	history, err := audit.New(f.pb, f.kernel)
	if err != nil {
		t.Fatal(err)
	}
	page, err := history.ReadChangeSets(t.Context(), audit.ReadParams{TableID: f.model.Bindings["allocations"].TableID, ItemID: &allocation, Scope: "row", Limit: 10})
	if err != nil || len(page.ChangeSets) != 1 || page.ChangeSets[0].ChangeSetID != result.Receipt.ChangeSetID {
		t.Fatalf("normal attribution lost native audit: %v", err)
	}
	_, digest := regressionRow(t, f, "allocations", allocation)
	for index, change := range []map[string]any{{"quantity": "-5", "net": "-50.00", "gross": "-50.00"}, {"line": ids["actual_lines"]["second"]}} {
		before := transactionBusinessSnapshot(t, f)
		response := reversalNative(t, f, fmt.Sprintf("change-existing-attribution-%d", index), "allocations", map[string]any{"kind": "update", "primaryKey": allocation, "expectedDigest": digest, "values": change})
		requireHTTPNotCommitted(t, response, "transaction.evidence_required")
		if transactionBusinessSnapshot(t, f) != before {
			t.Fatal("rejected attribution correction changed facts")
		}
	}
	regressionReject(t, f, invoicePlan(transactionOperation{Entity: "allocations", Kind: "delete", RecordID: allocation, ExpectedDigest: &digest}), "transaction.evidence_required")
	extra := invoiceMeasures("-1", "-10.00", "0.00", "-10.00")
	extra["invoice"] = ids["invoices"]["raw"]
	extra["purpose"] = "physicalReturn"
	extra["unit"] = "piece"
	regressionReject(t, f, invoicePlan(invoiceCreate("actual_lines", "new-face-line", extra)), "transaction.evidence_required")
}

func TestInvoiceAuthorityInheritedMaterialRemainsKnown(t *testing.T) {
	f := newInvoiceAuthorityFixture(t, false, true)
	ids := f.submit(t, invoicePlan(invoiceCreate("agreements", "a", map[string]any{"label": "a"}), invoiceCreate("materials", "a", map[string]any{"label": "a"}), invoiceCreate("materials", "b", map[string]any{"label": "b"}))).NewIDs
	agreement, a, b := ids["agreements"]["a"], ids["materials"]["a"], ids["materials"]["b"]
	requested := invoiceRequest("blue-source", "normal", "quantity", "10", "100.00", "0.00", "100.00", nil)
	requested.Operations[0].Values["direction"] = "blue"
	requested.Operations[0].Values["agreement"] = agreement
	requested.Operations[1].Values["material"] = a
	source := f.submit(t, requested).NewIDs["request_sources"]["blue-source"]
	original := invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", source, nil, true)
	original.Operations[0].Values["agreement"] = agreement
	original.Operations[1].Values["material"] = nil
	blue := f.submit(t, original).NewIDs["allocations"]["blue"]
	redRequest := invoiceRequest("red-request", "physicalReturn", "quantity", "10", "100.00", "0.00", "100.00", blue)
	redRequest.Operations[0].Values["agreement"] = agreement
	redRequest.Operations[1].Values["material"] = b
	regressionReject(t, f, redRequest, "transaction.settlement_invalid")
	redRequest.Operations[1].Values["material"] = a
	redSource := f.submit(t, redRequest).NewIDs["request_sources"]["red-request"]
	actual := invoiceFace("red", "red", "physicalReturn", "-6", "-60.00", "0.00", "-60.00", redSource, blue, true)
	actual.Operations[0].Values["agreement"] = agreement
	actual.Operations[1].Values["material"] = a
	f.submit(t, actual)
	// With no known source material, explicit matching remains possible.
	original = invoiceFace("unmapped-blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)
	original.Operations[0].Values["agreement"] = agreement
	original.Operations[1].Values["material"] = nil
	unmapped := f.submit(t, original).NewIDs["allocations"]["unmapped-blue"]
	redRequest = invoiceRequest("unmapped-request", "physicalReturn", "quantity", "10", "100.00", "0.00", "100.00", unmapped)
	redRequest.Operations[0].Values["agreement"] = agreement
	redRequest.Operations[1].Values["material"] = b
	chosen := f.submit(t, redRequest).NewIDs["request_sources"]["unmapped-request"]
	actual = invoiceFace("chosen", "red", "physicalReturn", "-6", "-60.00", "0.00", "-60.00", chosen, unmapped, true)
	actual.Operations[0].Values["agreement"] = agreement
	actual.Operations[1].Values["material"] = b
	f.submit(t, actual)
}

func TestInvoiceAuthorityOptionalContractsRejectOnlyKnownConflict(t *testing.T) {
	f := newInvoiceAuthorityFixture(t, false, true, false, true)
	ids := f.submit(t, invoicePlan(invoiceCreate("agreements", "a", map[string]any{"label": "a"}), invoiceCreate("agreements", "b", map[string]any{"label": "b"}))).NewIDs
	a, b := ids["agreements"]["a"], ids["agreements"]["b"]
	original := invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)
	original.Operations[0].Values["agreement"] = nil
	original.Operations[1].Values["material"] = nil
	blueIDs := f.submit(t, original).NewIDs
	blue, head := blueIDs["allocations"]["blue"], blueIDs["invoices"]["blue"]
	requested := invoiceRequest("request", "physicalReturn", "quantity", "10", "100.00", "0.00", "100.00", blue)
	requested.Operations[0].Values["agreement"] = a
	requested.Operations[1].Values["material"] = nil
	source := f.submit(t, requested).NewIDs["request_sources"]["request"]
	actual := invoiceFace("red", "red", "physicalReturn", "-6", "-60.00", "0.00", "-60.00", source, blue, true)
	actual.Operations[0].Values["agreement"] = nil
	actual.Operations[1].Values["material"] = nil
	f.submit(t, actual)
	registration := transactionrules.Registration{WorkspaceID: f.workspace, PluginID: f.input.PluginID, InstallationID: f.input.InstallationID, Declaration: f.declaration, Bindings: f.model.Bindings}
	projected, err := registration.Read(t.Context(), f.pb)
	if err != nil {
		t.Fatal(err)
	}
	if err = registration.Validate(projected, projected); err != nil {
		t.Fatalf("optional terminal contracts failed actual read projection: %v", err)
	}
	_, digest := regressionRow(t, f, "invoices", head)
	before := transactionBusinessSnapshot(t, f)
	response := reversalNative(t, f, "known-contract-conflict", "invoices", map[string]any{"kind": "update", "primaryKey": head, "expectedDigest": digest, "values": map[string]any{"agreement": b}})
	requireHTTPNotCommitted(t, response, "transaction.same_target_violation")
	if before != transactionBusinessSnapshot(t, f) {
		t.Fatal("contract conflict changed facts")
	}
}

func TestInvoiceAuthorityGhostCorrectionHasNoOwner(t *testing.T) {
	f := newInvoiceAuthorityFixture(t)
	regressionReject(t, f, invoicePlan(invoiceCreate("adjustments", "ghost", map[string]any{"invoice": nil, "kind": "correction", "note": "无真实票头的幽灵更正"})), "transaction.evidence_invalid")
}

func TestInvoiceAuthorityRequestAllocationCompleteOnlyWhenPresent(t *testing.T) {
	f := newInvoiceAuthorityFixture(t)
	blue := f.submit(t, invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)).NewIDs["allocations"]["blue"]
	request := invoiceRequest("request", "physicalReturn", "quantity", "10", "100.00", "0.00", "100.00", blue)
	empty := request
	empty.Operations = append([]transactionOperation{}, request.Operations[:2]...)
	ids := f.submit(t, empty).NewIDs
	values := invoiceMeasures("5", "50.00", "0.00", "50.00")
	values["line"] = ids["request_lines"]["request"]
	values["original"] = blue
	regressionReject(t, f, invoicePlan(invoiceCreate("request_sources", "half", values)), "transaction.allocation_mismatch")
	second := invoiceMeasures("5", "50.00", "0.00", "50.00")
	second["line"] = ids["request_lines"]["request"]
	second["original"] = blue
	sources := f.submit(t, invoicePlan(invoiceCreate("request_sources", "half", values), invoiceCreate("request_sources", "rest", second))).NewIDs
	actual := invoiceFace("partial-actual", "red", "physicalReturn", "-6", "-60.00", "0.00", "-60.00", sources["request_sources"]["half"], blue, true)
	actual.Operations[2].Values["quantity"] = "-3"
	actual.Operations[2].Values["net"] = "-30.00"
	actual.Operations[2].Values["gross"] = "-30.00"
	f.submit(t, actual)
	draft := invoiceRequest("unknown-parent", "physicalReturn", "quantity", "1", "10.00", "0.00", "10.00", blue)
	draft.Operations[0].Values["status"] = "draft"
	for _, key := range []string{"quantity", "net", "tax", "gross"} {
		draft.Operations[1].Values[key] = nil
	}
	regressionReject(t, f, draft, "transaction.allocation_invalid")
}

func TestInvoiceAuthorityStateRelationCasesUseCompleteNativeProjection(t *testing.T) {
	f := newInvoiceAuthorityFixture(t, false, true, false, true, true)
	targets := f.submit(t, invoicePlan(invoiceCreate("projects", "p", map[string]any{"label": "p"}), invoiceCreate("packages", "p", map[string]any{"label": "p"}), invoiceCreate("agreements", "a", map[string]any{"label": "a"}), invoiceCreate("obligations", "o", map[string]any{"label": "o"}), invoiceCreate("plan_items", "p", map[string]any{"label": "p"}), invoiceCreate("source_versions", "v", map[string]any{"label": "v"}))).NewIDs
	raw := f.submit(t, invoiceFace("raw", "blue", "", "1", "1.00", "0.00", "1.00", nil, nil, false)).NewIDs
	parent := invoiceRequest("parent", "normal", "quantity", "0", "0.00", "0.00", "0.00", nil)
	parent.Operations = parent.Operations[:2]
	parent.Operations[0].Values["direction"] = "blue"
	parent.Operations[0].Values["status"] = "draft"
	parent.Operations[0].Values["agreement"] = nil
	parent.Operations[1].Values["material"] = nil
	line := f.submit(t, parent).NewIDs["request_lines"]["parent"]
	values := func(kind string) map[string]any {
		v := invoiceMeasures("0", "0.00", "0.00", "0.00")
		v["line"] = line
		v["source_kind"] = kind
		v["original"] = nil
		return v
	}
	regressionReject(t, f, invoicePlan(invoiceCreate("request_sources", "missing-project", values("project"))), "transaction.reference_violation")
	bad := values("planItem")
	bad["obligation"] = targets["obligations"]["o"]
	regressionReject(t, f, invoicePlan(invoiceCreate("request_sources", "missing-plan", bad)), "transaction.reference_violation")
	bad = values("delivery")
	bad["actual_line"] = raw["actual_lines"]["raw"]
	regressionReject(t, f, invoicePlan(invoiceCreate("request_sources", "wrong-delivery", bad)), "transaction.reference_violation")
	valid := values("project")
	valid["project"] = targets["projects"]["p"]
	source := f.submit(t, invoicePlan(invoiceCreate("request_sources", "valid", valid))).NewIDs["request_sources"]["valid"]
	bad = values("project")
	bad["project"] = targets["projects"]["p"]
	bad["application_source"] = source
	regressionReject(t, f, invoicePlan(invoiceCreate("request_sources", "forbidden-application", bad)), "transaction.state_invalid")
	_, digest := regressionRow(t, f, "request_sources", source)
	response := reversalNative(t, f, "kind-only-case-change", "request_sources", map[string]any{"kind": "update", "primaryKey": source, "expectedDigest": digest, "values": map[string]any{"source_kind": "planItem"}})
	requireHTTPNotCommitted(t, response, "transaction.reference_violation")
	f.submit(t, invoicePlan(transactionOperation{Entity: "request_sources", Kind: "update", RecordID: source, ExpectedDigest: &digest, Values: map[string]any{"source_kind": "planItem", "plan_item": targets["plan_items"]["p"], "version": targets["source_versions"]["v"], "project": targets["projects"]["p"], "package": targets["packages"]["p"], "agreement": targets["agreements"]["a"], "obligation": targets["obligations"]["o"]}}))
	registration := transactionrules.Registration{WorkspaceID: f.workspace, PluginID: f.input.PluginID, InstallationID: f.input.InstallationID, Declaration: f.declaration, Bindings: f.model.Bindings}
	projected, err := registration.Read(t.Context(), f.pb)
	if err != nil {
		t.Fatal(err)
	}
	if err = registration.Validate(projected, projected); err != nil {
		t.Fatal(err)
	}
	if !registration.ProtectsBinding(f.model.Bindings["request_sources"].TableID, f.model.Bindings["request_sources"].Fields["actual_line"]) || !registration.ProtectsBinding(f.model.Bindings["delivery_lines"].TableID, "") {
		t.Fatal("inactive case dependencies are not protected")
	}
}

func TestInvoiceAuthorityGroupedContractsNativeEditAndRestore(t *testing.T) {
	f := newInvoiceAuthorityFixture(t, false, true, false, true, false, true)
	agreements := f.submit(t, invoicePlan(invoiceCreate("agreements", "a", map[string]any{"label": "a"}), invoiceCreate("agreements", "b", map[string]any{"label": "b"}))).NewIDs["agreements"]
	a, b := agreements["a"], agreements["b"]
	plan := invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)
	plan.Operations[0].Values["agreement"] = nil
	plan.Operations[1].Values["material"] = nil
	sibling := invoiceMeasures("10", "100.00", "0.00", "100.00")
	sibling["invoice"] = regressionRef("invoices", "blue")
	sibling["purpose"] = nil
	sibling["unit"] = "piece"
	sibling["material"] = nil
	share := invoiceMeasures("10", "100.00", "0.00", "100.00")
	share["line"] = regressionRef("actual_lines", "sibling")
	share["original"] = nil
	share["application_source"] = nil
	plan.Operations = append(plan.Operations, invoiceCreate("actual_lines", "sibling", sibling), invoiceCreate("allocations", "sibling", share))
	ids := f.submit(t, plan).NewIDs
	original, siblingID := ids["allocations"]["blue"], ids["allocations"]["sibling"]
	requested := invoiceRequest("r", "physicalReturn", "quantity", "10", "100.00", "0.00", "100.00", original)
	requested.Operations[0].Values["agreement"] = a
	requested.Operations[1].Values["material"] = nil
	requestID := f.submit(t, requested).NewIDs["requests"]["r"]
	_, digest := regressionRow(t, f, "allocations", siblingID)
	before := transactionBusinessSnapshot(t, f)
	response := reversalNative(t, f, "grouped-known-conflict", "allocations", map[string]any{"kind": "update", "primaryKey": siblingID, "expectedDigest": digest, "values": map[string]any{"agreement": b}})
	requireHTTPNotCommitted(t, response, "transaction.same_target_violation")
	if before != transactionBusinessSnapshot(t, f) {
		t.Fatal("group rejection changed facts")
	}
	f.submit(t, invoicePlan(transactionOperation{Entity: "allocations", Kind: "update", RecordID: siblingID, ExpectedDigest: &digest, Values: map[string]any{"agreement": a}}))
	history, err := audit.New(f.pb, f.kernel)
	if err != nil {
		t.Fatal(err)
	}
	table := f.model.Bindings["allocations"].TableID
	page, err := history.ReadChangeSets(t.Context(), audit.ReadParams{TableID: table, ItemID: &siblingID, Scope: "row", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	target := page.ChangeSets[0].RootRevisionID
	_, digest = regressionRow(t, f, "allocations", siblingID)
	_, requestDigest := regressionRow(t, f, "requests", requestID)
	f.submit(t, invoicePlan(transactionOperation{Entity: "allocations", Kind: "update", RecordID: siblingID, ExpectedDigest: &digest, Values: map[string]any{"agreement": b}}, transactionOperation{Entity: "requests", Kind: "update", RecordID: requestID, ExpectedDigest: &requestDigest, Values: map[string]any{"agreement": b}}))
	restore, err := history.PreviewRestore(t.Context(), audit.PreviewParams{TableID: table, ItemID: siblingID, TargetRevision: target, Scope: "row"})
	if err != nil {
		t.Fatal(err)
	}
	before = transactionBusinessSnapshot(t, f)
	if _, err = history.ApplyRestore(t.Context(), audit.ApplyParams{TableID: table, ItemID: siblingID, Token: restore.Token}); err == nil {
		t.Fatal("history restore bypassed grouped contracts")
	}
	if before != transactionBusinessSnapshot(t, f) {
		t.Fatal("rejected grouped restore changed facts")
	}
}

func TestInvoiceAuthorityGroupReferencesRemainOneWay(t *testing.T) {
	f := newInvoiceAuthorityFixture(t, false, true, false, true, false, true)
	agreements := f.submit(t, invoicePlan(invoiceCreate("agreements", "a", map[string]any{"label": "a"}), invoiceCreate("agreements", "b", map[string]any{"label": "b"}))).NewIDs["agreements"]
	plan := invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)
	plan.Operations[0].Values["agreement"] = nil
	plan.Operations[1].Values["material"] = nil
	blue := f.submit(t, plan).NewIDs["allocations"]["blue"]
	for _, id := range []string{"a", "b"} {
		request := invoiceRequest(id, "physicalReturn", "quantity", "4", "40.00", "0.00", "40.00", blue)
		request.Operations[0].Values["agreement"] = agreements[id]
		request.Operations[1].Values["material"] = nil
		f.submit(t, request)
	}
}

func TestInvoiceAuthorityGroupedAnchorIsSingleLayer(t *testing.T) {
	f := newInvoiceAuthorityFixture(t, false, true, false, true, false, true)
	agreements := f.submit(t, invoicePlan(invoiceCreate("agreements", "a", map[string]any{"label": "a"}), invoiceCreate("agreements", "b", map[string]any{"label": "b"}))).NewIDs["agreements"]
	a, b := agreements["a"], agreements["b"]
	request := invoiceRequest("blue-source", "normal", "quantity", "10", "100.00", "0.00", "100.00", nil)
	request.Operations[0].Values["direction"] = "blue"
	request.Operations[0].Values["agreement"] = b
	request.Operations[1].Values["material"] = nil
	source := f.submit(t, request).NewIDs["request_sources"]["blue-source"]
	original := invoiceFace("known-anchor", "blue", "", "10", "100.00", "0.00", "100.00", source, nil, true)
	original.Operations[0].Values["agreement"] = nil
	original.Operations[1].Values["material"] = nil
	known := f.submit(t, original).NewIDs["allocations"]["known-anchor"]
	red := invoiceFace("conflict", "red", "physicalReturn", "-1", "-10.00", "0.00", "-10.00", nil, known, true)
	red.Operations[0].Values["agreement"] = nil
	red.Operations[1].Values["material"] = nil
	red.Operations[2].Values["agreement"] = a
	regressionReject(t, f, red, "transaction.same_target_violation")
	original = invoiceFace("unknown-anchor", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)
	original.Operations[0].Values["agreement"] = nil
	original.Operations[1].Values["material"] = nil
	line := invoiceMeasures("10", "100.00", "0.00", "100.00")
	line["invoice"] = regressionRef("invoices", "unknown-anchor")
	line["purpose"] = nil
	line["material"] = nil
	line["unit"] = "piece"
	sibling := invoiceMeasures("10", "100.00", "0.00", "100.00")
	sibling["line"] = regressionRef("actual_lines", "sibling")
	sibling["original"] = nil
	sibling["application_source"] = nil
	sibling["agreement"] = b
	original.Operations = append(original.Operations, invoiceCreate("actual_lines", "sibling", line), invoiceCreate("allocations", "sibling", sibling))
	unknown := f.submit(t, original).NewIDs["allocations"]["unknown-anchor"]
	red = invoiceFace("single-layer", "red", "physicalReturn", "-1", "-10.00", "0.00", "-10.00", nil, unknown, true)
	red.Operations[0].Values["agreement"] = nil
	red.Operations[1].Values["material"] = nil
	red.Operations[2].Values["agreement"] = a
	f.submit(t, red)
}

func TestInvoiceAuthoritySettlementPlanSourceBucket(t *testing.T) {
	f := newInvoiceAuthorityFixture(t, true)
	seeded := f.submit(t, invoicePlan(
		invoiceCreate("obligations", "o", map[string]any{"quantity": "16", "currency": "CNY", "unit": "piece"}),
		invoiceCreate("series", "s", map[string]any{"label": "s", "current_version": regressionRef("plan_versions", "v")}),
		invoiceCreate("plan_versions", "v", map[string]any{"label": "v", "series": regressionRef("series", "s")}),
		invoiceCreate("logical_items", "a", map[string]any{"label": "a"}),
		invoiceCreate("logical_items", "b", map[string]any{"label": "b"}),
		invoiceCreate("plan_items", "a", map[string]any{"quantity": "8", "share": 0, "plan_version": regressionRef("plan_versions", "v"), "logical_item": regressionRef("logical_items", "a"), "scope": regressionRef("obligations", "o")}),
		invoiceCreate("plan_items", "b", map[string]any{"quantity": "8", "share": 1, "plan_version": regressionRef("plan_versions", "v"), "logical_item": regressionRef("logical_items", "b"), "scope": regressionRef("obligations", "o")}),
	)).NewIDs
	obligation, a, b := seeded["obligations"]["o"], seeded["plan_items"]["a"], seeded["plan_items"]["b"]
	request := func(id string) transactionPlan {
		p := invoiceRequest(id, "normal", "quantity", "8", "8.00", "0.00", "8.00", nil)
		p.Operations[0].Values["direction"] = "blue"
		p.Operations[2].Values["obligation"] = obligation
		p.Operations[2].Values["plan_item"] = a
		return p
	}
	source := f.submit(t, request("reserved")).NewIDs["request_sources"]["reserved"]
	actual := func(id, q string, item any) transactionPlan {
		p := invoiceFace(id, "blue", "", q, q+".00", "0.00", q+".00", source, nil, true)
		p.Operations[2].Values["obligation"] = obligation
		p.Operations[2].Values["plan_item"] = item
		return p
	}
	regressionReject(t, f, actual("wrong", "8", b), "transaction.settlement_invalid")
	f.submit(t, actual("explicit", "4", a))
	f.submit(t, actual("inherited", "4", nil))
	regressionReject(t, f, request("reused"), "transaction.settlement_exceeded")
}

func TestInvoiceAuthoritySettlementPlanManifestFields(t *testing.T) {
	for _, field := range []string{"quantity", "share"} {
		t.Run(field, func(t *testing.T) {
			f := newInvoiceAuthorityFixture(t, true)
			store := pluginstore.New(f.pb, f.workspace)
			original, err := store.GetInstallation(context.Background(), f.input.ProjectKey, f.input.PluginID)
			if err != nil {
				t.Fatal(err)
			}
			raw := append([]byte(nil), original...)
			var object map[string]any
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			permissions := object["manifest"].(map[string]any)["permissions"].(map[string]any)
			entities := permissions["model"].(map[string]any)["entities"].([]any)
			for _, value := range entities {
				entity := value.(map[string]any)
				if entity["key"] == "plan_items" {
					fields := entity["fields"].([]any)
					for index, value := range fields {
						if value.(map[string]any)["key"] == field {
							entity["fields"] = append(fields[:index], fields[index+1:]...)
							break
						}
					}
				}
			}
			object["revision"] = f.input.InstallationRevision + 1
			raw, _ = json.Marshal(object)
			previous := f.input.InstallationRevision
			if _, err := store.SaveInstallation(context.Background(), raw, &previous); err == nil {
				t.Fatal("installed manifest accepted absent plan " + field)
			} else {
				var invalid *pluginstore.Error
				if !errors.As(err, &invalid) || invalid.Code != "plugin.request_invalid" {
					t.Fatalf("unexpected install rejection: %v", err)
				}
			}
			after, err := store.GetInstallation(context.Background(), f.input.ProjectKey, f.input.PluginID)
			if err != nil || !reflect.DeepEqual(original, after) {
				t.Fatalf("rejected install changed registration: %v", err)
			}
		})
	}
}

func TestInvoiceAuthorityAdmissionPreservesClosedHistory(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint("partial-", partial), func(t *testing.T) {
			f := newInvoiceAuthorityFixture(t)
			blue := f.submit(t, invoiceFace("blue", "blue", "", "10", "100.00", "0.00", "100.00", nil, nil, true)).NewIDs["allocations"]["blue"]
			requested := f.submit(t, invoiceRequest("request", "physicalReturn", "quantity", "10", "100.00", "0.00", "100.00", blue)).NewIDs
			source, request := requested["request_sources"]["request"], requested["requests"]["request"]
			actual := invoiceFace("red", "red", "physicalReturn", "-10", "-100.00", "0.00", "-100.00", source, blue, true)
			if partial {
				for key, value := range invoiceMeasures("-4", "-40.00", "0.00", "-40.00") {
					actual.Operations[2].Values[key] = value
				}
			}
			allocation := f.submit(t, actual).NewIDs["allocations"]["red"]
			_, requestDigest := regressionRow(t, f, "requests", request)
			closePlan := invoicePlan(transactionOperation{Entity: "requests", Kind: "update", RecordID: request, ExpectedDigest: &requestDigest, Values: map[string]any{"status": "closed"}})
			if partial {
				close := invoiceMeasures("6", "60.00", "0.00", "60.00")
				close["kind"], close["source"], close["note"], close["close_remaining"] = "closeRemaining", source, "固定关闭剩余", true
				closePlan.Operations = append(closePlan.Operations, invoiceCreate("adjustments", "close", close))
			}
			f.submit(t, closePlan)
			history, err := transactionrules.Load(t.Context(), f.pb)
			if err != nil {
				t.Fatal(err)
			}
			before, err := history[len(history)-1].Read(t.Context(), f.pb)
			if err != nil {
				t.Fatal(err)
			}
			store := pluginstore.New(f.pb, f.workspace)
			installed, err := store.GetInstallation(t.Context(), f.input.ProjectKey, f.input.PluginID)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if err = json.Unmarshal(installed, &object); err != nil {
				t.Fatal(err)
			}
			upgraded := f.declaration
			upgraded.Version++
			object["manifest"].(map[string]any)["permissions"].(map[string]any)["transactions"] = upgraded
			previous := f.input.InstallationRevision
			f.input.InstallationRevision++
			object["revision"] = f.input.InstallationRevision
			raw, _ := json.Marshal(object)
			if _, err = store.SaveInstallation(t.Context(), raw, &previous); err != nil {
				t.Fatal(err)
			}
			preview, err := modelCall(t, f.pb, f.workspace, f.input, f.domain, "model.preview", modelPreviewRequest{Mode: "create"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = modelCall(t, f.pb, f.workspace, f.input, f.domain, "model.commit", map[string]any{"requestId": preview.(modelPreview).RequestID}); err != nil {
				t.Fatal(err)
			}
			if _, err = f.call("transaction.configure", map[string]any{"declaration": upgraded}); err != nil {
				t.Fatalf("register closed historical source: %v", err)
			}
			registered, err := transactionrules.Load(t.Context(), f.pb)
			if err != nil || len(registered) != len(history)+1 {
				t.Fatalf("new registration not persisted: %v", err)
			}
			after, err := registered[len(registered)-1].Read(t.Context(), f.pb)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("registration changed business facts: %v", err)
			}
			if !reflect.DeepEqual(history, registered[:len(history)]) {
				t.Fatal("registration replaced historical guards")
			}
			f.declaration = upgraded
			regressionReject(t, f, invoiceFace("late", "red", "physicalReturn", "-1", "-1.00", "0.00", "-1.00", source, blue, true), "transaction.settlement_invalid")
			if partial {
				_, digest := regressionRow(t, f, "allocations", allocation)
				regressionReject(t, f, invoicePlan(transactionOperation{Entity: "allocations", Kind: "update", RecordID: allocation, ExpectedDigest: &digest, Values: map[string]any{"net": "-41.00", "gross": "-41.00"}}), "transaction.settlement_invalid")
			}
		})
	}
}
