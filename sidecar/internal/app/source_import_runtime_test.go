package app

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/sourceimport"
)

func sourceRuntimePorts(t *testing.T, runtime *dashboardRuntimeFixture) (*sourceImportAuthorityFixture, *sourceImportJournal) {
	t.Helper()
	gate := businessWriteGate(runtime.runtime.CoordinateBusinessWrite)
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: runtime.pb, Event: router.Event{Response: w, Request: request}}, nil
	})
	domain := registerFieldRoutes(r, runtime.pb, nil, nil, nil, nil, gate)
	return &sourceImportAuthorityFixture{app: runtime.pb,
			authority: newSourceImportAuthority(domain, mutation.New(runtime.pb, mutation.MetadataSchemaSource{}), gate)},
		&sourceImportJournal{PocketBaseJournal: sourceimport.NewJournal(runtime.pb), gates: []businessWriteGate{gate}}
}

func sourceRuntimeAdmission(t *testing.T, journal *sourceImportJournal, job string) (*importPlanOwner, sourceLifecycleRequest, sourceimport.Result) {
	t.Helper()
	owner := newImportPlanOwner("11111111-1111-4111-8111-111111111111")
	plan := sourceExecutionPreview(t, sourceExecutionSnapshot(), nil)
	preview, err := owner.mintSource(plan, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := sourceLifecycleRequest{Token: preview.Token, JobID: job, SessionEpoch: 7}
	result, err := owner.startSource(context.Background(), input, 7, journal)
	if err != nil || result.Stage != "preparing" || result.State != "interrupted" || result.Created != 0 || result.NotSubmitted != 6 {
		t.Fatalf("runtime admission: %#v %v", result, err)
	}
	return owner, input, result
}

func TestSourceImportRuntimeAdmissionReplayDoesNotAdvanceRevision(t *testing.T) {
	runtime := newDashboardRuntimeFixture(t)
	_, journal := sourceRuntimePorts(t, runtime)
	owner, input, admitted := sourceRuntimeAdmission(t, journal, "runtime-admission-replay")
	before := runtime.state(t)
	facts := sourceExecutionFacts(t, runtime.pb)
	replayed, err := owner.startSource(context.Background(), input, 7, journal)
	if err != nil || !reflect.DeepEqual(admitted, replayed) {
		t.Fatalf("read-only admission replay must abort its unused runtime intent: %#v %v", replayed, err)
	}
	if !reflect.DeepEqual(before, runtime.state(t)) || !reflect.DeepEqual(facts, sourceExecutionFacts(t, runtime.pb)) {
		t.Fatalf("admission replay changed authority: runtime before=%#v after=%#v; business before=%v after=%v",
			before, runtime.state(t), facts, sourceExecutionFacts(t, runtime.pb))
	}
}

func TestSourceImportRuntimeAdmittedExecutionPersistsAndReopens(t *testing.T) {
	ctx := context.Background()
	runtime := newDashboardRuntimeFixture(t)
	authority, journal := sourceRuntimePorts(t, runtime)
	owner, input, admitted := sourceRuntimeAdmission(t, journal, "runtime-admitted-execution")
	admittedState := runtime.state(t)
	replayed, err := owner.startSource(ctx, input, 7, journal)
	if err != nil || !reflect.DeepEqual(admitted, replayed) || !reflect.DeepEqual(admittedState, runtime.state(t)) {
		t.Fatalf("replayed admission must leave the Runtime ready for execution: %#v %v", replayed, err)
	}
	claim := sourceClaimRequest{Contract: sourceimport.Contract, Token: input.Token, JobID: input.JobID, SessionEpoch: 7, Confirmed: true,
		Observation: sourceimport.Observation{Version: "v1", TableVersions: map[string]string{"a": "a1", "b": "b1", "c": "c1"}}}
	stored, err := owner.claimSource(claim, 7)
	if err != nil {
		t.Fatal(err)
	}
	executor := sourceimport.NewExecutor(authority.authority, journal, nil)
	result, err := executor.Execute(ctx, stored.plan, input.JobID, 7)
	if err != nil || result.State != "succeeded" || result.Created != 6 || result.NotSubmitted != 0 || result.Stage != "settled" || result.StartedAt != admitted.StartedAt {
		t.Fatalf("admission must advance to a distinct execution receipt and real rows: %#v %v", result, err)
	}
	sourceExecutionAssertRows(t, authority, sourceExecutionSnapshot(), result)
	before := runtime.state(t)
	if _, err := executor.Execute(ctx, stored.plan, input.JobID, 7); err == nil {
		t.Fatal("a terminal execution became restartable")
	}
	input.State = "cancelled"
	if _, err := owner.finishSourcePreparation(ctx, input, 7, journal); err == nil {
		t.Fatal("preparation terminal overwrote claimed execution")
	}
	if !reflect.DeepEqual(before, runtime.state(t)) {
		t.Fatal("rejected execution changed runtime revision or receipts")
	}
	runtime.close(t)
	if err := runtime.pb.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	runtime.open(t)
	authority, journal = sourceRuntimePorts(t, runtime)
	reopened, err := journal.Read(ctx, input.JobID)
	if err != nil || !reflect.DeepEqual(result, reopened) || !reflect.DeepEqual(before, runtime.state(t)) {
		t.Fatalf("reopen changed durable execution or runtime receipts: %#v %v", reopened, err)
	}
	sourceExecutionAssertRows(t, authority, sourceExecutionSnapshot(), reopened)
}

func TestSourceImportRuntimePreparationTerminalKeepsZeroBusinessWrites(t *testing.T) {
	for _, state := range []string{"failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			runtime := newDashboardRuntimeFixture(t)
			_, journal := sourceRuntimePorts(t, runtime)
			before := sourceExecutionFacts(t, runtime.pb)
			owner, input, admitted := sourceRuntimeAdmission(t, journal, "runtime-preparation-"+state)
			input.State = state
			result, err := owner.finishSourcePreparation(context.Background(), input, 7, journal)
			if err != nil || result.State != state || result.Created != 0 || result.NotSubmitted != 6 || result.FinishedAt == "" || result.StartedAt != admitted.StartedAt {
				t.Fatalf("runtime preparation settlement: %#v %v", result, err)
			}
			if !reflect.DeepEqual(before, sourceExecutionFacts(t, runtime.pb)) {
				t.Fatal("admission or preparation terminal created business objects")
			}
			settled := runtime.state(t)
			replayed, err := owner.finishSourcePreparation(context.Background(), input, 7, journal)
			if err != nil || !reflect.DeepEqual(result, replayed) || !reflect.DeepEqual(settled, runtime.state(t)) {
				t.Fatalf("preparation finish replay changed durable facts: %#v %v", replayed, err)
			}
		})
	}
}
