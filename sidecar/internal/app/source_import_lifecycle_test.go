package app

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/sourceimport"
)

func TestSourceImportPreparationTerminalIsDurableAndCreatesNoBusinessObjects(t *testing.T) {
	for _, state := range []string{"failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			fixture := newSourceImportAuthorityFixture(t)
			plan := sourceExecutionPreview(t, sourceExecutionSnapshot(), nil)
			before := sourceExecutionFacts(t, fixture.app)
			owner := newImportPlanOwner("workspace-one")
			preview, err := owner.mintSource(plan, 7, nil)
			if err != nil {
				t.Fatal(err)
			}
			journal := sourceimport.NewJournal(fixture.app)
			input := sourceLifecycleRequest{Token: preview.Token, JobID: "preparation-job", SessionEpoch: 7}
			started, err := owner.startSource(ctx, input, 7, journal)
			if err != nil || started.State != "interrupted" || started.Stage != "preparing" || started.Total != 6 || started.NotSubmitted != 6 || started.Created != 0 || len(started.Targets) != 0 || len(started.Batches) != 0 {
				t.Fatalf("admission did not persist exact plan facts: %#v %v", started, err)
			}
			replayed, err := owner.startSource(ctx, input, 7, journal)
			if err != nil || !reflect.DeepEqual(started, replayed) {
				t.Fatalf("admission replay changed original job: %#v %v", replayed, err)
			}
			another, err := owner.mintSource(plan, 7, nil)
			if err != nil {
				t.Fatal(err)
			}
			stolen := input
			stolen.Token = another.Token
			if _, err := owner.startSource(ctx, stolen, 7, journal); err == nil {
				t.Fatal("another plan stole the admitted job")
			}
			stolen.State = "cancelled"
			if _, err := owner.finishSourcePreparation(ctx, stolen, 7, journal); err == nil {
				t.Fatal("another plan cancelled the admitted job")
			}
			other := input
			other.JobID = "other-job"
			if _, err := owner.startSource(ctx, other, 7, journal); err == nil {
				t.Fatal("a confirmed token admitted another job")
			}
			input.State = state
			finished, err := owner.finishSourcePreparation(ctx, input, 7, journal)
			if err != nil || finished.State != state || finished.FinishedAt == "" || finished.StartedAt != started.StartedAt || finished.Created != 0 || finished.NotSubmitted != 6 || finished.UnknownRecords != 0 || len(finished.Targets) != 0 || len(finished.Batches) != 0 || len(finished.Diagnostics) == 0 {
				t.Fatalf("pre-execution terminal facts lost: %#v %v", finished, err)
			}
			claim := sourceClaimRequest{Contract: sourceimport.Contract, Token: preview.Token, JobID: input.JobID, SessionEpoch: 7, Confirmed: true,
				Observation: sourceimport.Observation{Version: "v1", TableVersions: map[string]string{"a": "a1", "b": "b1", "c": "c1"}}}
			if _, err := owner.claimSource(claim, 7); err == nil {
				t.Fatal("terminal admission became restartable")
			}
			if after := sourceExecutionFacts(t, fixture.app); !reflect.DeepEqual(before, after) {
				t.Fatalf("admission/failed download changed business facts: before=%v after=%v", before, after)
			}
			if err := owner.discardSource(preview.Token, 7); err != nil {
				t.Fatal(err)
			}
			if err := fixture.app.ResetBootstrapState(); err != nil {
				t.Fatal(err)
			}
			if err := fixture.app.Bootstrap(); err != nil {
				t.Fatal(err)
			}
			reopened := sourceimport.NewJournal(fixture.app)
			persisted, err := reopened.Read(ctx, input.JobID)
			if err != nil || !reflect.DeepEqual(persisted, finished) {
				t.Fatalf("reopened preparation report changed: %#v %v", persisted, err)
			}
			entries, err := reopened.List(ctx)
			if err != nil || len(entries) != 1 || entries[0].JobID != input.JobID || entries[0].State != state {
				t.Fatalf("terminal disappeared from Go history: %#v %v", entries, err)
			}
		})
	}
}

func TestSourceImportAdmissionAdvancesOnlyOnceAndPreparationCannotOverwriteExecution(t *testing.T) {
	ctx := context.Background()
	fixture := newSourceImportAuthorityFixture(t)
	plan := sourceExecutionPreview(t, sourceExecutionSnapshot(), nil)
	owner := newImportPlanOwner("workspace-one")
	preview, err := owner.mintSource(plan, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	journal := sourceimport.NewJournal(fixture.app)
	input := sourceLifecycleRequest{Token: preview.Token, JobID: "admitted-execution", SessionEpoch: 7}
	started, err := owner.startSource(ctx, input, 7, journal)
	if err != nil {
		t.Fatal(err)
	}
	claim := sourceClaimRequest{Contract: sourceimport.Contract, Token: preview.Token, JobID: input.JobID, SessionEpoch: 7, Confirmed: true,
		Observation: sourceimport.Observation{Version: "v1", TableVersions: map[string]string{"a": "a1", "b": "b1", "c": "c1"}}}
	other := claim
	other.JobID = "replacement-job"
	if _, err := owner.claimSource(other, 7); err == nil {
		t.Fatal("execute changed the admitted job identity")
	}
	stored, err := owner.claimSource(claim, 7)
	if err != nil {
		t.Fatal(err)
	}
	result, err := sourceimport.NewExecutor(fixture.authority, journal, nil).Execute(ctx, stored.plan, input.JobID, 7)
	if err != nil || result.State != "succeeded" || result.Created != 6 || result.StartedAt != started.StartedAt {
		t.Fatalf("admitted execution did not preserve authoritative task: %#v %v", result, err)
	}
	sourceExecutionAssertRows(t, fixture, sourceExecutionSnapshot(), result)
	input.State = "cancelled"
	if _, err := owner.finishSourcePreparation(ctx, input, 7, journal); err == nil {
		t.Fatal("preparation finish overwrote a claimed execution")
	}
	if _, err := sourceimport.NewExecutor(fixture.authority, journal, nil).Execute(ctx, stored.plan, input.JobID, 7); err == nil {
		t.Fatal("same admitted job executed twice")
	}
	persisted, err := journal.Read(ctx, input.JobID)
	if err != nil || !reflect.DeepEqual(persisted, result) {
		t.Fatal("failed restart or preparation finish changed successful authority result")
	}
}

func TestSourceImportExpiredAdmissionCanSettleWithoutReopeningItsPlan(t *testing.T) {
	ctx := context.Background()
	fixture := newSourceImportAuthorityFixture(t)
	owner := newImportPlanOwner("workspace-one")
	now := time.Now()
	owner.now = func() time.Time { return now }
	plan := sourceExecutionPreview(t, sourceExecutionOrdinarySnapshot(3), nil)
	preview, err := owner.mintSource(plan, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	journal := sourceimport.NewJournal(fixture.app)
	input := sourceLifecycleRequest{Token: preview.Token, JobID: "expired-admission", SessionEpoch: 7}
	if _, err := owner.startSource(ctx, input, 7, journal); err != nil {
		t.Fatal(err)
	}
	now = now.Add(importPlanTTL + time.Second)
	if _, err := owner.mintSource(plan, 7, nil); err != nil {
		t.Fatal(err)
	}
	input.State = "failed"
	input.SessionEpoch = 8
	if _, err := owner.finishSourcePreparation(ctx, input, 7, journal); err == nil {
		t.Fatal("retired epoch wrote a terminal report")
	}
	input.SessionEpoch = 7
	result, err := owner.finishSourcePreparation(ctx, input, 7, journal)
	if err != nil || result.State != "failed" || result.NotSubmitted != 5 {
		t.Fatalf("expired plan erased the admitted task: %#v %v", result, err)
	}
}

func TestSourceImportLifecycleHTTPIsPrivateAndCountsComeFromGoPlan(t *testing.T) {
	fixture := newSourceImportAuthorityFixture(t)
	r, encoded := importPlanHTTPRouter(t)
	owner := newImportPlanOwner("workspace-one")
	plan := sourceExecutionPreview(t, sourceExecutionSnapshot(), nil)
	preview, err := owner.mintSource(plan, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	registerSourceImportRoutes(r, fixture.app, owner, fixture.authority, nil, 7)
	body := map[string]any{"token": preview.Token, "jobId": "private-admission", "sessionEpoch": 7}
	for _, secret := range []string{"", encoded + "x"} {
		response, _ := importPlanHTTPCall(t, r, sourceImportPath+"/start", body, secret)
		if response.Code != http.StatusUnauthorized {
			t.Fatal("untrusted session admitted a task")
		}
	}
	body["total"] = 123
	response, _ := importPlanHTTPCall(t, r, sourceImportPath+"/start", body, encoded)
	if response.Code == http.StatusOK {
		t.Fatal("caller supplied authoritative counts")
	}
	delete(body, "total")
	response, payload := importPlanHTTPCall(t, r, sourceImportPath+"/start", body, encoded)
	if response.Code != http.StatusOK || payload["total"] != float64(6) || payload["notSubmitted"] != float64(6) {
		t.Fatalf("private Go admission: %d %v", response.Code, payload)
	}
	body["state"] = "succeeded"
	response, _ = importPlanHTTPCall(t, r, sourceImportPath+"/finish", body, encoded)
	if response.Code == http.StatusOK {
		t.Fatal("Host fabricated preparation success")
	}
	body["state"] = "failed"
	response, payload = importPlanHTTPCall(t, r, sourceImportPath+"/finish", body, encoded)
	if response.Code != http.StatusOK || payload["state"] != "failed" || payload["created"] != float64(0) {
		t.Fatalf("private Go finish: %d %v", response.Code, payload)
	}
}
