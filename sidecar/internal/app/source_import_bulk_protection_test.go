package app

import (
	"context"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/sourceimport"
)

func TestSourceImportRuntimeBulkProtectionSnapshot(t *testing.T) {
	for _, workers := range []bool{false, true} {
		name := "workersDisabled"
		if workers {
			name = "workersEnabled"
		}
		t.Run(name, func(t *testing.T) { sourceImportBulkProtectionSnapshot(t, workers) })
	}
}

func sourceImportBulkProtectionSnapshot(t *testing.T, workers bool) {
	t.Helper()
	fixture := newMetadataRuntimeFixture(t)
	if workers {
		if err := fixture.runtime.StartBackgroundWorkers(context.Background(), true); err != nil {
			t.Fatal(err)
		}
	}
	authority, journal := sourceRuntimePorts(t, fixture)
	source := sourceExecutionOrdinarySnapshot(2504)
	plan := sourceExecutionPreview(t, source, nil)
	owner := newImportPlanOwner("11111111-1111-4111-8111-111111111111")
	preview, err := owner.mintSource(plan, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := sourceLifecycleRequest{Token: preview.Token, JobID: "bulk-protection", SessionEpoch: 7}
	if _, err := owner.startSource(context.Background(), input, 7, journal); err != nil {
		t.Fatal(err)
	}
	stored, err := owner.claimSource(sourceClaimRequest{
		Contract: sourceimport.Contract, Token: input.Token, JobID: input.JobID,
		SessionEpoch: 7, Confirmed: true,
		Observation: sourceimport.Observation{Version: "v1", TableVersions: map[string]string{"a": "a1", "b": "b1", "c": "c1"}},
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	executor := sourceimport.NewExecutor(authority.authority, journal, nil)
	started := time.Now()
	result, err := executor.Execute(context.Background(), stored.plan, input.JobID, 7)
	if err != nil || result.State != "succeeded" || result.Created != 2506 {
		t.Fatalf("bulk import: state=%s created=%d err=%v", result.State, result.Created, err)
	}
	var pendingBefore, pendingAfter int
	if err := fixture.pb.DB().NewQuery("SELECT COUNT(*) FROM vibetable_audit_outbox WHERE status='pending'").Row(&pendingBefore); err != nil {
		t.Fatal(err)
	}
	t.Logf("import=%s pendingBefore=%d", time.Since(started), pendingBefore)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started = time.Now()
	response := fixture.runtime.Dispatcher().DispatchEnvelope(ctx, []byte(`{"jsonrpc":"2.0","id":"bulk-protection","method":"snapshot.request","wire":{"scope":"workspace","workspaceId":"11111111-1111-4111-8111-111111111111","sessionEpoch":7,"operationId":"bbbbbbbb-bbbb-4bbb-8bbb-000000000001","sequence":1},"params":{"trigger":"protection","urgency":"foreground"}}`))
	elapsed := time.Since(started)
	if err := fixture.pb.DB().NewQuery("SELECT COUNT(*) FROM vibetable_audit_outbox WHERE status='pending'").Row(&pendingAfter); err != nil {
		t.Fatal(err)
	}
	t.Logf("protection=%s pendingAfter=%d error=%#v", elapsed, pendingAfter, response.Error)
	if response.Error != nil {
		t.Fatalf("protection after bulk import: %#v", response.Error)
	}
	if pendingAfter != 0 {
		t.Fatalf("protection left %d pending audit events", pendingAfter)
	}
}
