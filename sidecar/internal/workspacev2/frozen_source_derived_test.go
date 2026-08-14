package workspacev2

import (
	"testing"

	contracts "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/workspacesearch"
)

func TestComputationWatermarkDoesNotClaimPendingJobsAreFresh(t *testing.T) {
	if actual := computationWatermark(19, 2); actual != 0 {
		t.Fatalf("pending computation watermark = %d", actual)
	}
	if actual := computationWatermark(19, 0); actual != 19 {
		t.Fatalf("settled computation watermark = %d", actual)
	}
}

func TestSearchProjectionPendingIncludesDurableLagAndDegradedStates(t *testing.T) {
	ready := contracts.SearchStatus{State: "ready", Generation: 4}
	checkpoint := workspacesearch.ProjectionCheckpoint{
		BusinessOutboxRowID: 8, FileHeadRevision: 3,
	}
	if searchProjectionPending(ready, checkpoint, 8, 3) {
		t.Fatal("matching durable tails were reported pending")
	}
	if !searchProjectionPending(ready, checkpoint, 9, 3) {
		t.Fatal("business outbox lag was not captured")
	}
	if !searchProjectionPending(ready, checkpoint, 8, 4) {
		t.Fatal("file-history lag was not captured")
	}
	ready.State = "degraded"
	if !searchProjectionPending(ready, checkpoint, 8, 3) {
		t.Fatal("degraded search state was not captured")
	}
}
