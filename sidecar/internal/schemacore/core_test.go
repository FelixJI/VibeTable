package schemacore

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

type sourceStub struct {
	revisions   fieldchange.Revisions
	name        string
	fields      []v2.FieldDefinition
	revisionErr error
	nameErr     error
	fieldsErr   error
}

func (source sourceStub) Revisions(context.Context, string) (fieldchange.Revisions, error) {
	return source.revisions, source.revisionErr
}

func (source sourceStub) TableDisplayName(context.Context, string) (string, error) {
	return source.name, source.nameErr
}

func (source sourceStub) Fields(
	context.Context,
	string,
	bool,
) ([]v2.FieldDefinition, error) {
	return source.fields, source.fieldsErr
}

type plannerStub struct{ received v2.FieldChangeIntent }

func (stub *plannerStub) Plan(
	_ context.Context,
	intent v2.FieldChangeIntent,
) (v2.FieldChangePlan, error) {
	stub.received = intent
	return v2.FieldChangePlan{Contract: v2.Contract, Intent: intent}, nil
}

type executorStub struct{ received v2.ApplyRequest }

func (stub *executorStub) Apply(
	_ context.Context,
	request v2.ApplyRequest,
) (v2.ApplyReceipt, error) {
	stub.received = request
	return v2.ApplyReceipt{Contract: v2.Contract, OperationID: request.OperationID}, nil
}

func TestCoreDescribeReturnsOneRevisionBoundV2Snapshot(t *testing.T) {
	field := v2.FieldDefinition{
		Contract:    v2.Contract,
		Identity:    v2.FieldIdentity{FieldID: "fld_12345678"},
		LogicalType: v2.LogicalText,
	}
	core, err := New(sourceStub{
		revisions: fieldchange.Revisions{Schema: "schema_4", Data: 9},
		name:      "订单", fields: []v2.FieldDefinition{field},
	}, &plannerStub{}, &executorStub{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := core.Describe(context.Background(), "tbl_orders")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Contract != v2.Contract || snapshot.TableID != "tbl_orders" ||
		snapshot.DisplayName != "订单" || snapshot.SchemaRevision != "schema_4" ||
		snapshot.DataRevision != 9 || !reflect.DeepEqual(snapshot.Fields, []v2.FieldDefinition{field}) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if len(snapshot.Capabilities) != len(v2.LogicalTypes) {
		t.Fatalf("capabilities = %d, want %d", len(snapshot.Capabilities), len(v2.LogicalTypes))
	}
}

func TestCorePlanAndApplyForwardOnlyV2Contracts(t *testing.T) {
	planner := &plannerStub{}
	executor := &executorStub{}
	core, err := New(sourceStub{}, planner, executor)
	if err != nil {
		t.Fatal(err)
	}
	intent := v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: "tbl_orders"}
	plan, err := core.Plan(context.Background(), intent)
	if err != nil || plan.Contract != v2.Contract || planner.received.TableID != intent.TableID {
		t.Fatalf("Plan() = %#v, %v", plan, err)
	}
	request := v2.ApplyRequest{PlanID: "plan-1", OperationID: "operation-1"}
	receipt, err := core.Apply(context.Background(), request)
	if err != nil || receipt.Contract != v2.Contract ||
		!reflect.DeepEqual(executor.received, request) {
		t.Fatalf("Apply() = %#v, %v", receipt, err)
	}
}

func TestCoreRejectsMissingDependencies(t *testing.T) {
	type dependencies struct {
		source   SnapshotSource
		planner  Planner
		executor Executor
	}
	for name, values := range map[string]dependencies{
		"source":   {planner: &plannerStub{}, executor: &executorStub{}},
		"planner":  {source: sourceStub{}, executor: &executorStub{}},
		"executor": {source: sourceStub{}, planner: &plannerStub{}},
	} {
		t.Run(name, func(t *testing.T) {
			if core, err := New(values.source, values.planner, values.executor); err == nil || core != nil {
				t.Fatalf("New() = %#v, %v", core, err)
			}
		})
	}
}

func TestCoreDescribePropagatesEachAuthorityReadFailure(t *testing.T) {
	want := errors.New("authority unavailable")
	for name, source := range map[string]sourceStub{
		"revisions": {revisionErr: want},
		"name":      {nameErr: want},
		"fields":    {fieldsErr: want},
	} {
		t.Run(name, func(t *testing.T) {
			core, err := New(source, &plannerStub{}, &executorStub{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := core.Describe(context.Background(), "tbl_orders"); !errors.Is(err, want) {
				t.Fatalf("Describe() error = %v", err)
			}
		})
	}
}
