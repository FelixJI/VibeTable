package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/pocketbase/pocketbase/core"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestFanoutPathRejectsSingleSourceBeforeLoadingOverBudgetTargets(t *testing.T) {
	relation := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "targets", PhysicalName: "targets"},
		LogicalType: v2.LogicalRelation,
		Relation: &v2.RelationSpec{
			TargetTableID: "targets", Cardinality: "many", DeletePolicy: "setNull",
		},
	}
	definition := schemaexecution.Table{
		Snapshot: v2.SchemaSnapshot{TableID: "sources", Fields: []v2.FieldDefinition{relation}},
	}
	record := core.NewRecord(core.NewBaseCollection("sources"))
	ids := make([]string, fanoutTraversalBudget+1)
	for index := range ids {
		ids[index] = fmt.Sprintf("target%09d", index)
	}
	record.Set("targets", ids)
	_, err := (&Service{}).fanoutPathMatches(
		context.Background(), nil,
		fanoutTraversalNode{definition: definition, record: record},
		[]v2.LookupPathStep{{RelationFieldID: "targets"}},
		"targets", map[string]struct{}{},
		map[string]schemaexecution.Table{"sources": definition},
	)
	var productErr *JobError
	if !errors.As(err, &productErr) || productErr.Code != "job.fanout_too_expensive" {
		t.Fatalf("fan-out budget error = %#v", err)
	}
}

func TestFanoutPathHonorsCancellationBeforeTraversingTargets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&Service{}).fanoutPathMatches(
		ctx, nil, fanoutTraversalNode{},
		[]v2.LookupPathStep{{RelationFieldID: "targets"}},
		"targets", map[string]struct{}{}, map[string]schemaexecution.Table{},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled fan-out error = %#v", err)
	}
}

func TestMatchingFanoutBatchStopsOnPersistentCancellationBetweenSourceRecords(t *testing.T) {
	relation := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "targets", PhysicalName: "targets"},
		LogicalType: v2.LogicalRelation,
		Relation: &v2.RelationSpec{
			TargetTableID: "targets", Cardinality: "many", DeletePolicy: "setNull",
		},
	}
	definition := schemaexecution.Table{
		Snapshot: v2.SchemaSnapshot{TableID: "sources", Fields: []v2.FieldDefinition{relation}},
	}
	rows := make([]*core.Record, 3)
	for index := range rows {
		rows[index] = core.NewRecord(core.NewBaseCollection("sources"))
		rows[index].Set("id", fmt.Sprintf("source-%d", index))
		rows[index].Set("targets", []string{"target-1"})
	}
	checks := 0
	_, err := (&Service{}).matchingFanoutBatch(
		context.Background(),
		func() bool {
			checks++
			return checks >= 4
		},
		definition,
		fanoutCursor{
			TableID: "sources", RelationFieldID: "targets",
			ChangedTableID: "targets", TargetRecordIDs: []string{"target-1"},
		},
		rows,
	)
	if !errors.Is(err, errFanoutCancellationRequested) {
		t.Fatalf("persistent fan-out cancellation error = %#v", err)
	}
}
