package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
)

func addVersionModel(m *pluginmodel.Declaration) {
	m.Entities = append(m.Entities, pluginmodel.Entity{Key: "versions", Label: "版本", Operations: []string{"read", "query", "create", "update", "delete"}, Fields: []pluginmodel.Field{{Key: "label", Label: "版本名", Type: v2.LogicalText}}})
	addPair := func(source, target int, key, reverse string) {
		m.Entities[source].Fields = append(m.Entities[source].Fields, pluginmodel.Field{Key: key, Label: key, Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: m.Entities[target].Key, Field: reverse, Cardinality: "one"}})
		m.Entities[target].Fields = append(m.Entities[target].Fields, pluginmodel.Field{Key: reverse, Label: reverse, Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: m.Entities[source].Key, Field: key, Cardinality: "many"}})
	}
	addPair(1, 3, "current", "current_for")
	addPair(2, 3, "version", "entries")
	addPair(3, 1, "owner", "versions")
}

func TestNullableTransactionSourcesCompareOptionalOwnerAndExcludeDoubleUse(t *testing.T) {
	f := newTransactionFixture(t, "scoped")
	f.input = changeInstalledModel(t, f.pb, f.workspace, f.input, func(m *pluginmodel.Declaration) {
		m.Version++
		for _, role := range []string{"exception", "quota"} {
			m.Entities[2].Fields = append(m.Entities[2].Fields, pluginmodel.Field{Key: role, Label: role, Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "versions", Field: role + "_uses", Cardinality: "one"}})
			m.Entities[3].Fields = append(m.Entities[3].Fields, pluginmodel.Field{Key: role + "_uses", Label: role + "_uses", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "allocations", Field: role, Cardinality: "many"}})
		}
	})
	d := f.declaration
	d.Version++
	d.Rules = append(d.Rules, transactionrules.Rule{ID: "one_source", Kind: "atMostOne", Entity: "allocations", Fields: []string{"quota", "exception"}}, transactionrules.Rule{ID: "exception_owner", Kind: "sameTarget", Entity: "allocations", WhenPresent: "exception", SkipEmptyLeftEnd: true, PathMatch: transactionrules.PathMatch{
		Left: []transactionrules.RelationStep{{Field: "exception", Target: "versions"}, {Field: "owner", Target: "lines"}}, Right: []transactionrules.RelationStep{{Field: "line", Target: "lines"}},
	}})
	if err := f.configureUpgradedRules(t, d); err != nil {
		t.Fatal(err)
	}
	seed := threeTablePlan()
	seed.Operations = seed.Operations[:2]
	seed.Operations[1].Values["current"] = map[string]any{"entity": "versions", "tempId": "local"}
	for _, key := range []string{"local", "independent"} {
		values := map[string]any{"label": key}
		if key == "local" {
			values["owner"] = map[string]any{"entity": "lines", "tempId": "l"}
		}
		seed.Operations = append(seed.Operations, transactionOperation{Entity: "versions", Kind: "create", TempID: key, Values: values})
	}
	ids := f.submit(t, seed).NewIDs
	foreign := threeTablePlan()
	foreign.Operations = foreign.Operations[:2]
	foreign.Operations[0].Values["code"] = "foreign"
	foreign.Operations = append(foreign.Operations, transactionOperation{Entity: "versions", Kind: "create", TempID: "foreign", Values: map[string]any{"label": "foreign", "owner": map[string]any{"entity": "lines", "tempId": "l"}}})
	foreignID := f.submit(t, foreign).NewIDs["versions"]["foreign"]
	versions := ids["versions"]
	state, err := modelCall(t, f.pb, f.workspace, f.input, f.domain, "model.read", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	model := state.(pluginmodel.State)
	binding := model.Bindings["allocations"]
	for _, tc := range []struct{ name, exception, quota, want string }{
		{"none", "", "", ""}, {"quota", "", versions["local"], ""}, {"independent", versions["independent"], "", ""}, {"specific", versions["local"], "", ""},
		{"wrong owner", foreignID, "", "transaction.same_target_violation"}, {"both", versions["local"], versions["local"], "transaction.at_most_one_violation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]any{"amount": "0.01", "version": versions["local"], "line": ids["lines"]["l"]}
			if tc.exception != "" {
				values["exception"] = tc.exception
			}
			if tc.quota != "" {
				values["quota"] = tc.quota
			}
			p := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{{Entity: "allocations", Kind: "create", TempID: "a", Values: values}}}
			before := transactionBusinessSnapshot(t, f)
			result, err := f.call("transaction.preview", map[string]any{"plan": p})
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.call("transaction.commit", map[string]any{"requestId": result.(transactionPreview).RequestID})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var denied *mutation.ProductError
			if !errors.As(err, &denied) || denied.Code != tc.want || before != transactionBusinessSnapshot(t, f) {
				t.Fatalf("unexpected write outcome: %v", err)
			}
			// Ordinary workspace edits enter the mutation kernel without a plugin transaction.
			physical := map[string]any{}
			for key, value := range values {
				physical[binding.Fields[key]] = value
			}
			request := mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: tc.name, IdempotencyKey: tc.name, TableID: binding.TableID, SchemaRevision: model.Revisions[binding.TableID], Actor: mutation.Actor{Type: "user", ID: "local-user"}, Operations: []mutation.Operation{{Kind: mutation.OperationInsert, Values: physical}}}
			_, err = f.kernel.Apply(t.Context(), request)
			if !errors.As(err, &denied) || denied.Code != tc.want || before != transactionBusinessSnapshot(t, f) {
				t.Fatalf("native edit: %v", err)
			}
		})
	}
}

func TestScopedBalanceConcurrentCurrentWritesAndForeignVersionRollback(t *testing.T) {
	f := newTransactionFixture(t, "scoped")
	f.input = changeInstalledModel(t, f.pb, f.workspace, f.input, func(m *pluginmodel.Declaration) {
		m.Version++
	})
	d := f.declaration
	d.Version++
	for i := range d.Decimals {
		if d.Decimals[i].Entity == "allocations" {
			d.Decimals[i].Sign = "positive"
		}
	}
	if err := f.configureUpgradedRules(t, d); err != nil {
		t.Fatal(err)
	}
	seedPlan := threeTablePlan()
	ref := func(entity, temp string) any { return map[string]any{"entity": entity, "tempId": temp} }
	seedPlan.Operations[1].Values["current"] = ref("versions", "v2")
	seedPlan.Operations[2].Values["version"] = ref("versions", "v1")
	seedPlan.Operations[2].Values["amount"] = "10.00"
	for _, version := range []string{"v1", "v2"} {
		seedPlan.Operations = append(seedPlan.Operations, transactionOperation{Entity: "versions", Kind: "create", TempID: version, Values: map[string]any{"label": version, "owner": ref("lines", "l")}})
	}
	seed := f.submit(t, seedPlan)
	line := seed.NewIDs["lines"]["l"]
	current := seed.NewIDs["versions"]["v2"]
	planFor := func(q string) transactionPlan {
		return transactionPlan{Contract: seedPlan.Contract, ModelID: seedPlan.ModelID, Operations: []transactionOperation{{Entity: "allocations", Kind: "create", TempID: "next", Values: map[string]any{"version": current, "line": line, "amount": q}}}}
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
	// Race a signed offset against two otherwise valid current writes.
	offset := planFor("11.00")
	negative := planFor("-1.00").Operations[0]
	negative.TempID = "offset"
	offset.Operations = append(offset.Operations, negative)
	previews := []transactionPreview{preview(planFor("6.00")), preview(planFor("5.00")), preview(offset)}
	errs := make([]error, len(previews))
	var wg sync.WaitGroup
	for i, p := range previews {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = commit(p) }()
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("expected one current allocation: %v", errs)
	}
	verifyRejected := func(err error, code string) {
		t.Helper()
		var e *mutation.ProductError
		if !errors.As(err, &e) || e.Code != code {
			t.Fatalf("expected %s got %v", code, err)
		}
	}
	verifyRejected(errs[2], "transaction.decimal_sign_violation")
	verifyRejected(commit(preview(planFor("0.00"))), "transaction.decimal_sign_violation")
	for _, err := range errs[:2] {
		if err != nil {
			verifyRejected(err, "transaction.balance_violation")
		}
	}

	// No entry references this candidate version: ownership must be checked on the pointer itself.
	empty := threeTablePlan()
	empty.Operations = empty.Operations[:2]
	empty.Operations[0].Values["code"] = "EMPTY"
	empty.Operations[1].Values["current"] = ref("versions", "empty")
	empty.Operations = append(empty.Operations, transactionOperation{Entity: "versions", Kind: "create", TempID: "empty", Values: map[string]any{"label": "empty", "owner": line}})
	verifyRejected(commit(preview(empty)), "transaction.same_target_violation")
	delete(empty.Operations[1].Values, "current")
	f.submit(t, empty)
	foreign := threeTablePlan()
	foreign.Operations[0].Values["code"] = "SYN-2"
	foreign.Operations[2].Values["version"] = current
	p := preview(foreign)
	verifyRejected(commit(p), "transaction.same_target_violation")
	for entity, count := range map[string]int{"allocations": 2, "lines": 2, "headers": 2, "versions": 3} {
		table, err := schemaexecution.Describe(context.Background(), f.pb, f.model.Bindings[entity].TableID)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := f.pb.FindAllRecords(table.PhysicalName)
		if err != nil || len(rows) != count {
			t.Fatalf("%s after rejection: %d %v", entity, len(rows), err)
		}
	}
}
