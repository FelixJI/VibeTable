package computationplan

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/formula"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func graphTable(id string, fields ...v2.FieldDefinition) schemaexecution.Table {
	return schemaexecution.Table{Snapshot: v2.SchemaSnapshot{TableID: id, Fields: fields}}
}

func graphFormula(id, source string) v2.FieldDefinition {
	return v2.FieldDefinition{
		Identity: v2.FieldIdentity{FieldID: id, PhysicalName: "f_" + id}, LogicalType: v2.LogicalFormula,
		Formula: &v2.FormulaSpec{Language: "cel-v1", Source: source, ResultType: v2.LogicalNumber},
	}
}

func graphRelation(id, target string) v2.FieldDefinition {
	return v2.FieldDefinition{
		Identity: v2.FieldIdentity{FieldID: id, PhysicalName: "f_" + id}, LogicalType: v2.LogicalRelation,
		Relation: &v2.RelationSpec{TargetTableID: target, Cardinality: "one"},
	}
}

func graphLookup(id, relation, target string) v2.FieldDefinition {
	return v2.FieldDefinition{
		Identity: v2.FieldIdentity{FieldID: id, PhysicalName: "f_" + id}, LogicalType: v2.LogicalLookup,
		Lookup: &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: relation}}, TargetFieldID: target},
	}
}

func TestValidateKeepsLegalComputedTargetsAndLoadsReachableTablesOnce(t *testing.T) {
	root := graphTable("a", graphRelation("r", "b"), graphRelation("unused", "unrelated"),
		graphLookup("l1", "r", "value"), graphLookup("l2", "r", "value"),
		graphFormula("total", "double(f_l1) + double(f_l2) + double(f_r.f_value)"))
	// An unrelated invalid expression is not on the reachable dependency path.
	target := graphTable("b", graphFormula("value", "1.0"), graphFormula("unrelated", "missing + 1.0"))
	calls := []string{}
	err := Validate(context.Background(), root, func(_ context.Context, id string) (schemaexecution.Table, error) {
		calls = append(calls, id)
		return target, nil
	})
	if err != nil || !reflect.DeepEqual(calls, []string{"b"}) {
		t.Fatalf("legal shared dependencies: calls=%v err=%v", calls, err)
	}
}

func TestValidateCandidateOverridesStoredSchemaAndReportsClosedCycle(t *testing.T) {
	root := graphTable("a", graphRelation("r", "b"), graphFormula("value", "double(f_r.f_lookup) + 1.0"))
	target := graphTable("b", graphRelation("back", "a"), graphLookup("lookup", "back", "value"))
	err := Validate(context.Background(), root, func(_ context.Context, id string) (schemaexecution.Table, error) {
		if id != "b" {
			t.Fatalf("candidate was reread from storage: %s", id)
		}
		return target, nil
	})
	var cycleErr *schemaerror.ProductError
	if !errors.As(err, &cycleErr) || cycleErr.Code != "schema.computation.cycle" {
		t.Fatalf("mixed cycle = %v", err)
	}
	expected := []map[string]string{{"tableId": "a", "fieldId": "value"}, {"tableId": "b", "fieldId": "lookup"}, {"tableId": "a", "fieldId": "value"}}
	if !reflect.DeepEqual(cycleErr.Details["cycle"], expected) {
		t.Fatalf("closed cycle = %#v", cycleErr.Details)
	}
}

func TestValidateCountAndJSONPropertiesDoNotInventTargetValueEdges(t *testing.T) {
	relation := graphRelation("r", "not_loaded")
	relation.Relation.Cardinality = "many"
	jsonField := v2.FieldDefinition{Identity: v2.FieldIdentity{FieldID: "json", PhysicalName: "f_json"}, LogicalType: v2.LogicalJSON}
	root := graphTable("a", relation, jsonField,
		graphFormula("count", "double(relationCount(f_r))"), graphFormula("property", "double(f_json.amount)"))
	err := Validate(context.Background(), root, func(context.Context, string) (schemaexecution.Table, error) {
		t.Fatal("membership/JSON property tried to load a target value")
		return schemaexecution.Table{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidatePreservesLocalFormulaCyclePriority(t *testing.T) {
	root := graphTable("a", graphFormula("first", "f_second + 1.0"), graphFormula("second", "f_first + 1.0"), graphLookup("invalid", "missing", "value"))
	err := Validate(context.Background(), root, nil)
	var formulaErr *formula.Error
	if !errors.As(err, &formulaErr) || formulaErr.Code != "formula.cycle" {
		t.Fatalf("local cycle priority = %v", err)
	}
}

func TestValidateExcludesRetiredFieldsAndRejectsRetiredTargets(t *testing.T) {
	retired := graphFormula("old", "missing + 1.0")
	retired.Lifecycle.State = v2.LifecycleRetired
	root := graphTable("a", retired, graphFormula("current", "1.0"))
	if err := Validate(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	root.Snapshot.Fields = append(root.Snapshot.Fields, graphRelation("self", "a"), graphLookup("lookup", "self", "old"))
	err := Validate(context.Background(), root, nil)
	var productErr *schemaerror.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "schema.lookup.target_field_not_found" {
		t.Fatalf("retired target = %v", err)
	}
}

func TestValidatePropagatesReadErrorsAndCancellation(t *testing.T) {
	root := graphTable("a", graphRelation("r", "b"), graphLookup("lookup", "r", "value"))
	sentinel := errors.New("schema read failed")
	err := Validate(context.Background(), root, func(context.Context, string) (schemaexecution.Table, error) {
		return schemaexecution.Table{}, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("read failure = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Validate(ctx, root, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("early cancellation = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	err = Validate(ctx, root, func(context.Context, string) (schemaexecution.Table, error) {
		cancel()
		return graphTable("b", graphFormula("value", "1.0")), nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation after target read = %v", err)
	}
	err = Validate(context.Background(), root, func(context.Context, string) (schemaexecution.Table, error) {
		return graphTable("wrong"), nil
	})
	var productErr *schemaerror.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "schema.computation.schema_invalid" {
		t.Fatalf("wrong identity = %v", err)
	}
}
