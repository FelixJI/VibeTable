package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func addDeliveryPlanModel(m *pluginmodel.Declaration) {
	all := []string{"read", "query", "create", "update", "delete"}
	for _, key := range []string{"series", "plan_versions", "logical_items", "plan_items"} {
		m.Entities = append(m.Entities, pluginmodel.Entity{Key: key, Label: key, Operations: all,
			Fields: []pluginmodel.Field{{Key: "label", Label: "label", Type: v2.LogicalText}}})
	}
	m.Entities[7].Fields = append(m.Entities[7].Fields,
		pluginmodel.Field{Key: "quantity", Label: "quantity", Type: v2.LogicalText},
		pluginmodel.Field{Key: "share", Label: "share", Type: v2.LogicalNumber, Precision: "integer"})
	for _, pair := range []struct {
		source, target int
		key, reverse   string
	}{
		{4, 5, "current_version", "current_for"}, {5, 4, "series", "versions"},
		{7, 5, "plan_version", "items"}, {7, 6, "logical_item", "items"},
		{7, 0, "scope", "plans"}, {3, 7, "plan_item", "sources"},
	} {
		m.Entities[pair.source].Fields = append(m.Entities[pair.source].Fields, pluginmodel.Field{Key: pair.key, Label: pair.key, Type: v2.LogicalRelation,
			Relation: &pluginmodel.Relation{Entity: m.Entities[pair.target].Key, Field: pair.reverse, Cardinality: "one"}})
		m.Entities[pair.target].Fields = append(m.Entities[pair.target].Fields, pluginmodel.Field{Key: pair.reverse, Label: pair.reverse, Type: v2.LogicalRelation,
			Relation: &pluginmodel.Relation{Entity: m.Entities[pair.source].Key, Field: pair.key, Cardinality: "many"}})
	}
}

func regressionRef(entity, temp string) any { return map[string]any{"entity": entity, "tempId": temp} }

func regressionRow(t *testing.T, f *transactionFixture, entity, id string) (map[string]any, string) {
	t.Helper()
	binding := f.model.Bindings[entity]
	table, err := schemaexecution.Describe(t.Context(), f.pb, binding.TableID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := f.pb.FindRecordById(table.PhysicalName, id)
	if err != nil {
		t.Fatal(err)
	}
	projected := productrow.Project(table.Snapshot.Fields, record)
	digest, err := productrow.Digest(projected)
	if err != nil {
		t.Fatal(err)
	}
	logical := map[string]any{}
	for key, fieldID := range binding.Fields {
		field, _ := table.Field(fieldID)
		logical[key] = projected[field.Identity.PhysicalName]
	}
	return logical, digest
}

func regressionCommit(f *transactionFixture, plan transactionPlan) error {
	raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
	if err != nil {
		return err
	}
	_, err = f.call("transaction.commit", map[string]any{"requestId": raw.(transactionPreview).RequestID})
	return err
}

func regressionReject(t *testing.T, f *transactionFixture, plan transactionPlan, want string) {
	t.Helper()
	before := transactionBusinessSnapshot(t, f)
	err := regressionCommit(f, plan)
	var product *mutation.ProductError
	if !errors.As(err, &product) || product.Code != want {
		t.Fatalf("want %s, got %v", want, err)
	}
	if transactionBusinessSnapshot(t, f) != before {
		t.Fatal("rejected transaction changed business rows")
	}
}

func TestDeliveryGuardNullableRealWrites(t *testing.T) {
	for _, storage := range []string{"text", "integer"} {
		for _, entry := range []string{"plugin", "native"} {
			for _, scenario := range []string{"missing", "roundtrip"} {
				t.Run(storage+"/"+entry+"/"+scenario, func(t *testing.T) {
					f := newTransactionFixture(t, "nullable-"+storage)
					if err := writecoordinator.EnsurePocketBaseReceiptTable(t.Context(), f.pb); err != nil {
						t.Fatal(err)
					}
					binding := f.model.Bindings["headers"]
					write := func(kind, id, digest string, values map[string]any) error {
						if entry == "plugin" {
							op := transactionOperation{Entity: "headers", Kind: kind, Values: values}
							if kind == "create" {
								op.TempID = "h"
							} else {
								op.RecordID, op.ExpectedDigest = id, &digest
							}
							return regressionCommit(f, transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{op}})
						}
						physical := map[string]any{}
						for key, value := range values {
							physical[binding.Fields[key]] = value
						}
						op := map[string]any{"kind": kind, "values": physical}
						if id != "" {
							op["primaryKey"], op["expectedDigest"] = id, digest
						}
						f.input.RequestID = "nullable-" + storage + "-" + kind + "-" + digest
						call := transactionHTTP(t, f, nil)
						plan := map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": binding.TableID, "schemaRevision": f.model.Revisions[binding.TableID], "operations": []any{op}}
						for _, method := range []string{"mutation.preview", "audit.start", "mutation.commit"} {
							request := plan
							if method == "audit.start" {
								request = map[string]any{"taskId": f.input.RequestID, "plan": plan}
							}
							response := call(method, request)
							if response.Code != http.StatusOK {
								return errors.New(response.Body.String())
							}
						}
						return nil
					}
					values := map[string]any{"code": "nullable", "amount": "0.00"}
					if scenario == "missing" {
						before := transactionBusinessSnapshot(t, f)
						if err := write("create", "", "", values); err == nil {
							t.Fatal("missing price committed as null")
						}
						if transactionBusinessSnapshot(t, f) != before {
							t.Fatal("missing-price rejection left a row")
						}
						return
					}
					values["price"] = nil
					if err := write("create", "", "", values); err != nil {
						t.Fatal(err)
					}
					table, err := schemaexecution.Describe(t.Context(), f.pb, binding.TableID)
					if err != nil {
						t.Fatal(err)
					}
					records, err := f.pb.FindAllRecords(table.PhysicalName)
					if err != nil || len(records) != 1 {
						t.Fatalf("records %d: %v", len(records), err)
					}
					id := records[0].Id
					read, digest := regressionRow(t, f, "headers", id)
					if read["price"] != nil {
						t.Fatalf("explicit null became %#v", read["price"])
					}
					zero := any("0.00")
					if storage == "integer" {
						zero = json.Number("0")
					}
					if err := write("update", id, digest, map[string]any{"price": zero}); err != nil {
						t.Fatal(err)
					}
					read, digest = regressionRow(t, f, "headers", id)
					encoded, _ := json.Marshal(read["price"])
					wantZero := `"0.00"`
					if storage == "integer" {
						wantZero = "0"
					}
					if string(encoded) != wantZero {
						t.Fatalf("zero readback: %s", encoded)
					}
					oldPrice := read["price"]
					if err := write("update", id, digest, map[string]any{"code": "omitted-price"}); err != nil {
						t.Fatal(err)
					}
					read, digest = regressionRow(t, f, "headers", id)
					if !reflect.DeepEqual(read["price"], oldPrice) {
						t.Fatal("omitted update changed price")
					}
					if err := write("update", id, digest, map[string]any{"price": nil}); err != nil {
						t.Fatal(err)
					}
					read, digest = regressionRow(t, f, "headers", id)
					if read["price"] != nil {
						t.Fatalf("update null became %#v", read["price"])
					}
					for _, invalid := range []any{"", true} {
						before := transactionBusinessSnapshot(t, f)
						if err := write("update", id, digest, map[string]any{"price": invalid}); err == nil || !strings.Contains(err.Error(), "transaction.decimal_invalid") {
							t.Fatalf("invalid price %#v: %v", invalid, err)
						}
						if transactionBusinessSnapshot(t, f) != before {
							t.Fatal("invalid price changed persisted row")
						}
					}
					if storage == "integer" && entry == "native" {
						request := mutation.Request{ContractVersion: mutation.ContractVersion,
							RequestID: "raw-blank", IdempotencyKey: "raw-blank", TableID: binding.TableID,
							SchemaRevision: f.model.Revisions[binding.TableID], Actor: mutation.Actor{Type: "user", ID: "local-user"},
							Operations: []mutation.Operation{{Kind: mutation.OperationUpdate, RecordID: &id, ExpectedDigest: &digest, Values: map[string]any{}, RawValues: map[string]any{binding.Fields["price"]: ""}}}}
						_, err := f.kernel.Preview(t.Context(), request)
						var product *mutation.ProductError
						if !errors.As(err, &product) || product.Code != "transaction.decimal_invalid" {
							t.Fatalf("raw blank became null: %v", err)
						}
					}
				})
			}
		}
	}
}

func TestDeliveryGuardNativeSameDocumentReparent(t *testing.T) {
	f := newTransactionFixture(t, "delivery")
	seed := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{
		{Entity: "obligations", Kind: "create", TempID: "o", Values: map[string]any{"quantity": "10"}},
		{Entity: "documents", Kind: "create", TempID: "d", Values: map[string]any{"phase": "delivery_confirmed"}},
		{Entity: "lines", Kind: "create", TempID: "l1", Values: map[string]any{"purpose": "normal", "real_qty": 1, "document": regressionRef("documents", "d")}},
		{Entity: "lines", Kind: "create", TempID: "l2", Values: map[string]any{"purpose": "normal", "real_qty": 1, "document": regressionRef("documents", "d")}},
		{Entity: "sources", Kind: "create", TempID: "s", Values: map[string]any{"quantity": "1", "scope": regressionRef("obligations", "o"), "owner_line": regressionRef("lines", "l1")}},
	}}
	ids := f.submit(t, seed).NewIDs
	id := ids["sources"]["s"]
	_, digest := regressionRow(t, f, "sources", id)
	seed.Operations = []transactionOperation{{Entity: "sources", Kind: "update", RecordID: id, ExpectedDigest: &digest, Values: map[string]any{"owner_line": ids["lines"]["l2"]}}}
	regressionReject(t, f, seed, "transaction.parent_frozen")
}

func TestDeliveryGuardNativePlanIdentityAndScope(t *testing.T) {
	for _, scenario := range []string{"scope-consumer", "scope-instruction", "scope-transfer", "scope-native", "identity", "identity-upgrade", "valid-upgrade"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransactionFixture(t, "delivery-plans")
			plan := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{
				{Entity: "obligations", Kind: "create", TempID: "o1", Values: map[string]any{"quantity": "10"}},
				{Entity: "obligations", Kind: "create", TempID: "o2", Values: map[string]any{"quantity": "0"}},
				{Entity: "series", Kind: "create", TempID: "s", Values: map[string]any{"label": "series", "current_version": regressionRef("plan_versions", "v1")}},
				{Entity: "plan_versions", Kind: "create", TempID: "v1", Values: map[string]any{"label": "v1", "series": regressionRef("series", "s")}},
				{Entity: "plan_versions", Kind: "create", TempID: "v2", Values: map[string]any{"label": "v2", "series": regressionRef("series", "s")}},
				{Entity: "logical_items", Kind: "create", TempID: "L1", Values: map[string]any{"label": "L1"}},
				{Entity: "logical_items", Kind: "create", TempID: "L2", Values: map[string]any{"label": "L2"}},
				{Entity: "plan_items", Kind: "create", TempID: "p1", Values: map[string]any{"quantity": "6", "share": 1, "scope": regressionRef("obligations", "o1"), "logical_item": regressionRef("logical_items", "L1"), "plan_version": regressionRef("plan_versions", "v1")}},
				{Entity: "documents", Kind: "create", TempID: "di", Values: map[string]any{"phase": "instruction_open"}},
				{Entity: "documents", Kind: "create", TempID: "da", Values: map[string]any{"phase": "delivery_confirmed"}},
				{Entity: "lines", Kind: "create", TempID: "li", Values: map[string]any{"purpose": "normal", "real_qty": 2, "document": regressionRef("documents", "di")}},
				{Entity: "lines", Kind: "create", TempID: "la", Values: map[string]any{"purpose": "normal", "real_qty": 4, "document": regressionRef("documents", "da")}},
				{Entity: "sources", Kind: "create", TempID: "h", Values: map[string]any{"quantity": "2", "scope": regressionRef("obligations", "o1"), "owner_line": regressionRef("lines", "li"), "plan_item": regressionRef("plan_items", "p1")}},
				{Entity: "sources", Kind: "create", TempID: "a", Values: map[string]any{"quantity": "4", "scope": regressionRef("obligations", "o1"), "owner_line": regressionRef("lines", "la"), "plan_item": regressionRef("plan_items", "p1")}},
			}}
			if scenario == "scope-consumer" || scenario == "scope-instruction" || scenario == "scope-transfer" {
				for i := 12; i < len(plan.Operations); i++ {
					plan.Operations[i].Values["scope"] = regressionRef("obligations", "o2")
				}
				if scenario == "scope-transfer" {
					plan.Operations[8].Values["phase"] = "instruction_transferred"
					plan.Operations[9].Values["phase"] = "instruction_open"
					plan.Operations[13].Values["quantity"] = "2"
					plan.Operations[13].Values["transferred_from"] = regressionRef("sources", "h")
				} else if scenario == "scope-instruction" {
					plan.Operations = plan.Operations[:13]
				}
				regressionReject(t, f, plan, "transaction.reservation_invalid")
				return
			}
			ids := f.submit(t, plan).NewIDs
			if scenario == "scope-native" {
				if err := writecoordinator.EnsurePocketBaseReceiptTable(t.Context(), f.pb); err != nil {
					t.Fatal(err)
				}
				binding := f.model.Bindings["sources"]
				values := map[string]any{}
				for key, value := range map[string]any{"quantity": "1", "scope": ids["obligations"]["o2"], "owner_line": ids["lines"]["li"], "plan_item": ids["plan_items"]["p1"]} {
					values[binding.Fields[key]] = value
				}
				f.input.RequestID = "plan-scope-native"
				call := transactionHTTP(t, f, nil)
				surface := map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": binding.TableID, "schemaRevision": f.model.Revisions[binding.TableID], "operations": []any{map[string]any{"kind": "create", "values": values}}}
				if response := call("mutation.preview", surface); response.Code != http.StatusOK {
					t.Fatalf("native plan preview: %s", response.Body.String())
				}
				if response := call("audit.start", map[string]any{"taskId": f.input.RequestID, "plan": surface}); response.Code != http.StatusOK {
					t.Fatalf("native plan audit: %s", response.Body.String())
				}
				before := transactionBusinessSnapshot(t, f)
				requireHTTPNotCommitted(t, call("mutation.commit", surface), "transaction.reservation_invalid")
				if transactionBusinessSnapshot(t, f) != before {
					t.Fatal("native scope rejection changed business rows")
				}
				return
			}
			plan.Operations = nil
			p1, series := ids["plan_items"]["p1"], ids["series"]["s"]
			if scenario != "valid-upgrade" {
				_, digest := regressionRow(t, f, "plan_items", p1)
				plan.Operations = append(plan.Operations, transactionOperation{Entity: "plan_items", Kind: "update", RecordID: p1, ExpectedDigest: &digest, Values: map[string]any{"logical_item": ids["logical_items"]["L2"]}})
			}
			if scenario != "identity" {
				_, digest := regressionRow(t, f, "series", series)
				logical := ids["logical_items"]["L1"]
				if scenario == "identity-upgrade" {
					logical = ids["logical_items"]["L2"]
				}
				plan.Operations = append(plan.Operations,
					transactionOperation{Entity: "series", Kind: "update", RecordID: series, ExpectedDigest: &digest, Values: map[string]any{"current_version": ids["plan_versions"]["v2"]}},
					transactionOperation{Entity: "plan_items", Kind: "create", TempID: "p2", Values: map[string]any{"quantity": "6", "share": 1, "scope": ids["obligations"]["o1"], "logical_item": logical, "plan_version": ids["plan_versions"]["v2"]}})
			}
			if scenario == "valid-upgrade" {
				f.submit(t, plan)
			} else {
				regressionReject(t, f, plan, "transaction.reservation_frozen")
			}
		})
	}
}
