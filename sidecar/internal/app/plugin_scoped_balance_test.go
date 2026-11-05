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

func TestScopedBalanceConcurrentCurrentWritesAndForeignVersionRollback(t *testing.T) {
	f := newTransactionFixture(t, "scoped")
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
	previews := []transactionPreview{preview(planFor("6.00")), preview(planFor("5.00"))}
	errs := make([]error, 2)
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
	for _, err := range errs {
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
