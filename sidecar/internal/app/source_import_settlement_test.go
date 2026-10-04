package app

import (
	"context"
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/sourceimport"
)

type sourceSettlementFault struct {
	sourceimport.Journal
	finishFails bool
	readFails   bool
	lostAck     bool
	finished    bool
	finishCalls int
}

func (fault *sourceSettlementFault) Finish(ctx context.Context, result sourceimport.Result) error {
	fault.finished = true
	fault.finishCalls++
	if fault.finishFails {
		return errors.New("synthetic settlement failed before commit")
	}
	if err := fault.Journal.Finish(ctx, result); err != nil {
		return err
	}
	if fault.lostAck {
		return errors.New("synthetic settlement acknowledgement lost")
	}
	return nil
}

func (fault *sourceSettlementFault) Read(ctx context.Context, job string) (sourceimport.Result, error) {
	if fault.finished && fault.readFails {
		return sourceimport.Result{}, errors.New("synthetic final result read failed")
	}
	return fault.Journal.Read(ctx, job)
}

func TestSourceImportSettlementNeverReturnsUnconfirmedSuccess(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		finishFails bool
		readFails   bool
		wantState   string
	}{
		{"finish rejected", true, false, "interrupted"},
		{"final read unavailable", false, true, "unknown"},
		{"settlement and read unavailable", true, true, "unknown"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newSourceImportAuthorityFixture(t)
			plan := sourceExecutionPreview(t, sourceExecutionOrdinarySnapshot(3), nil)
			journal := sourceimport.NewJournal(fixture.app)
			fault := &sourceSettlementFault{Journal: journal, finishFails: scenario.finishFails, readFails: scenario.readFails}
			result, err := sourceimport.NewExecutor(fixture.authority, fault, nil).Execute(context.Background(), plan, "settle-job", 7)
			if err == nil || result.State != scenario.wantState || result.Created != 5 || result.NotSubmitted != 0 || len(result.Targets) != 3 {
				t.Fatalf("unconfirmed settlement: err=%v result=%#v", err, result)
			}
			if result.FinishedAt != "" || fault.finishCalls != 1 {
				t.Fatalf("unconfirmed terminal time or repeated settlement: %#v, calls=%d", result, fault.finishCalls)
			}
			persisted, err := journal.Read(context.Background(), "settle-job")
			if err != nil || persisted.Created != result.Created || len(persisted.Batches) != len(result.Batches) {
				t.Fatalf("committed batches lost after settlement failure: %#v %v", persisted, err)
			}
			if scenario.finishFails && persisted.State != "interrupted" {
				t.Fatalf("failed settlement changed persisted terminal state: %#v", persisted)
			}
		})
	}
}

func TestSourceImportSettlementLostAckReadsOriginalDurableJob(t *testing.T) {
	fixture := newSourceImportAuthorityFixture(t)
	plan := sourceExecutionPreview(t, sourceExecutionOrdinarySnapshot(3), nil)
	fault := &sourceSettlementFault{Journal: sourceimport.NewJournal(fixture.app), lostAck: true}
	probe := &sourceExecutionProbe{Authority: fixture.authority}
	result, err := sourceimport.NewExecutor(probe, fault, nil).Execute(context.Background(), plan, "settle-ack-job", 7)
	if err != nil || result.State != "succeeded" || result.FinishedAt == "" || result.Created != 5 || fault.finishCalls != 1 || probe.recordCalls != 3 {
		t.Fatalf("lost ack did not resolve from original durable job: %#v %v calls=%d submits=%d", result, err, fault.finishCalls, probe.recordCalls)
	}
}
