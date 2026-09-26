package app

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func newImportPlanTestOwner(t *testing.T) (*importPlanOwner, *time.Time) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	owner := newImportPlanOwner("11111111-1111-4111-8111-111111111111")
	owner.now = func() time.Time { return now }
	return owner, &now
}

func mintTestImportPlan(t *testing.T, owner *importPlanOwner, rows []json.RawMessage) importPlanTokenReply {
	t.Helper()
	reply, err := owner.mint(importPlanMintRequest{
		Contract:       importPlanContract,
		Collection:     "vibetable_demo",
		GrantID:        "grant-1",
		SchemaRevision: "schema-1",
		CapabilityHash: "cap-1",
		SourceHash:     "sha-1",
		Mode:           "create_only",
		Rows:           rows,
	})
	if err != nil {
		t.Fatalf("mint import plan: %v", err)
	}
	return reply
}

func importPlanCode(t *testing.T, err error) string {
	t.Helper()
	var productErr *v2.ProductError
	if !errors.As(err, &productErr) {
		t.Errorf("expected product error, got %v", err)
		return ""
	}
	return productErr.Code
}

func stageTestImportPlan(owner *importPlanOwner, token string) (importPlanStagedReply, error) {
	return owner.stage(importPlanStageRequest{
		Contract:       importPlanContract,
		Token:          token,
		GrantID:        "grant-1",
		Collection:     "vibetable_demo",
		Mode:           "create_only",
		CapabilityHash: "cap-1",
	})
}

func TestImportPlanMintStageAndSettleCommittedLifecycle(t *testing.T) {
	owner, _ := newImportPlanTestOwner(t)
	rows := []json.RawMessage{json.RawMessage(`{"sourceRow":2,"values":{"number":"A-1"}}`)}
	minted := mintTestImportPlan(t, owner, rows)
	if minted.Consumed || minted.Token == "" {
		t.Fatalf("unexpected minted token %+v", minted)
	}
	staged, err := stageTestImportPlan(owner, minted.Token)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if staged.Attempt != 1 {
		t.Fatalf("first claim attempt = %d, want 1", staged.Attempt)
	}
	if len(staged.Rows) != 1 || string(staged.Rows[0]) != string(rows[0]) {
		t.Fatalf("staged rows did not round-trip: %s", staged.Rows)
	}
	if staged.Collection != "vibetable_demo" || staged.SchemaRevision != "schema-1" || staged.SourceHash != "sha-1" {
		t.Fatalf("staged bindings lost: %+v", staged)
	}
	key, err := owner.bind(importPlanBindRequest{Contract: importPlanContract, Token: minted.Token, IdempotencyPrefix: "imp-abc", Attempt: staged.Attempt})
	if err != nil || key != "imp-abc-0" {
		t.Fatalf("bind: %v %q", err, key)
	}
	settled, err := owner.settle(importPlanSettleRequest{Contract: importPlanContract, Token: minted.Token, Outcome: "committed", Attempt: staged.Attempt})
	if err != nil || !settled.Consumed {
		t.Fatalf("settle committed: %v %+v", err, settled)
	}
	// Committed is terminal and idempotent, including a duplicated settle.
	settled, err = owner.settle(importPlanSettleRequest{Contract: importPlanContract, Token: minted.Token, Outcome: "committed", Attempt: staged.Attempt})
	if err != nil || !settled.Consumed {
		t.Fatalf("repeated committed settle: %v %+v", err, settled)
	}
	_, err = stageTestImportPlan(owner, minted.Token)
	if code := importPlanCode(t, err); code != "import_token_consumed" {
		t.Fatalf("consumed token staged with %s", code)
	}
}

func TestImportPlanStageRejectsForeignContract(t *testing.T) {
	owner, _ := newImportPlanTestOwner(t)
	minted := mintTestImportPlan(t, owner, nil)
	_, err := owner.stage(importPlanStageRequest{
		Contract:       "vibetable.import-plans.v0",
		Token:          minted.Token,
		GrantID:        "grant-1",
		Collection:     "vibetable_demo",
		Mode:           "create_only",
		CapabilityHash: "cap-1",
	})
	if code := importPlanCode(t, err); code != "import_plan_invalid" {
		t.Fatalf("expected contract rejection, got %s", code)
	}
}

func TestImportPlanStageValidatesEveryFrozenBinding(t *testing.T) {
	owner, now := newImportPlanTestOwner(t)
	minted := mintTestImportPlan(t, owner, nil)
	if _, err := stageTestImportPlan(owner, "imp1.not-a-real-token"); importPlanCode(t, err) != "import_token_unknown" {
		t.Fatal("expected pattern mismatch to be unknown")
	}
	cases := []struct {
		name  string
		stage importPlanStageRequest
		code  string
	}{
		{"expired", importPlanStageRequest{importPlanContract, minted.Token, "grant-1", "vibetable_demo", "create_only", "cap-1"}, "import_token_expired"},
		{"grant mismatch", importPlanStageRequest{importPlanContract, minted.Token, "grant-2", "vibetable_demo", "create_only", "cap-1"}, "import_grant_mismatch"},
		{"collection mismatch", importPlanStageRequest{importPlanContract, minted.Token, "grant-1", "other", "create_only", "cap-1"}, "import_plan_mismatch"},
		{"mode mismatch", importPlanStageRequest{importPlanContract, minted.Token, "grant-1", "vibetable_demo", "upsert", "cap-1"}, "import_plan_mismatch"},
		{"capability mismatch", importPlanStageRequest{importPlanContract, minted.Token, "grant-1", "vibetable_demo", "create_only", "cap-2"}, "schema_mismatch"},
	}
	for _, testCase := range cases {
		if testCase.name == "expired" {
			*now = now.Add(importPlanTTL + time.Second)
		}
		_, err := owner.stage(testCase.stage)
		if code := importPlanCode(t, err); code != testCase.code {
			t.Fatalf("%s: expected %s, got %s", testCase.name, testCase.code, code)
		}
		if testCase.name == "expired" {
			*now = now.Add(-(importPlanTTL + time.Second))
		}
	}
}

func TestImportPlanExpiredTokensStayExpiredForTheProcessLifetime(t *testing.T) {
	owner, now := newImportPlanTestOwner(t)
	expired := mintTestImportPlan(t, owner, nil)
	*now = now.Add(importPlanTTL + time.Second)
	// Later previews must not collapse the retired plan to unknown; the
	// pre-migration Python store kept it until the process ended too.
	live := mintTestImportPlan(t, owner, nil)
	_, err := stageTestImportPlan(owner, expired.Token)
	if code := importPlanCode(t, err); code != "import_token_expired" {
		t.Fatalf("retired plan answered %s, want import_token_expired", code)
	}
	if _, err := stageTestImportPlan(owner, live.Token); err != nil {
		t.Fatalf("live plan rejected after later mints: %v", err)
	}
}

func TestImportPlanRetiredWorkspaceCannotStage(t *testing.T) {
	owner, _ := newImportPlanTestOwner(t)
	minted := mintTestImportPlan(t, owner, nil)
	owner.workspaceID = "22222222-2222-4222-8222-222222222222"
	_, err := stageTestImportPlan(owner, minted.Token)
	if code := importPlanCode(t, err); code != "import_token_unknown" {
		t.Fatalf("expected retired workspace to reject with unknown, got %s", code)
	}
}

func TestImportPlanBindIsSingleAssignment(t *testing.T) {
	owner, _ := newImportPlanTestOwner(t)
	minted := mintTestImportPlan(t, owner, nil)
	staged, err := stageTestImportPlan(owner, minted.Token)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := owner.bind(importPlanBindRequest{importPlanContract, minted.Token, "imp-first", staged.Attempt}); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	_, err = owner.bind(importPlanBindRequest{importPlanContract, minted.Token, "imp-second", staged.Attempt})
	if code := importPlanCode(t, err); code != "import_idempotency_mismatch" {
		t.Fatalf("expected idempotency mismatch, got %s", code)
	}
	key, err := owner.bind(importPlanBindRequest{importPlanContract, minted.Token, "imp-first", staged.Attempt})
	if err != nil || key != "imp-first-0" {
		t.Fatalf("re-bind same prefix: %v %q", err, key)
	}
}

func TestImportPlanConcurrentStageConsumesTokenOnce(t *testing.T) {
	owner, _ := newImportPlanTestOwner(t)
	minted := mintTestImportPlan(t, owner, nil)
	const attempts = 16
	var mutex sync.Mutex
	stagedCount := 0
	busy := 0
	var group sync.WaitGroup
	for index := 0; index < attempts; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := stageTestImportPlan(owner, minted.Token)
			mutex.Lock()
			defer mutex.Unlock()
			if err != nil {
				if code := importPlanCode(t, err); code == "import_token_busy" {
					busy++
				}
				return
			}
			stagedCount++
		}()
	}
	group.Wait()
	if stagedCount != 1 || busy != attempts-1 {
		t.Fatalf("expected exactly one staged claim, got staged=%d busy=%d", stagedCount, busy)
	}
}

// TestImportPlanStaleAttemptCannotReleaseOrBindNewerClaim reproduces the lost
// settle/bind race: an attempt whose HTTP reply timed out must not release or
// rebind the claim of a later restage of the same token.
func TestImportPlanStaleAttemptCannotReleaseOrBindNewerClaim(t *testing.T) {
	owner, _ := newImportPlanTestOwner(t)
	minted := mintTestImportPlan(t, owner, nil)
	first, err := stageTestImportPlan(owner, minted.Token)
	if err != nil {
		t.Fatalf("first stage: %v", err)
	}
	if _, err := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "unknown", first.Attempt}); err != nil {
		t.Fatalf("first settle: %v", err)
	}
	second, err := stageTestImportPlan(owner, minted.Token)
	if err != nil {
		t.Fatalf("restage after unknown: %v", err)
	}
	if second.Attempt == first.Attempt {
		t.Fatal("restage must mint a new attempt lease")
	}
	if _, err := owner.bind(importPlanBindRequest{importPlanContract, minted.Token, "imp-stale", first.Attempt}); importPlanCode(t, err) != "import_plan_stale" {
		t.Fatal("stale bind must not bind the newer claim")
	}
	if _, err := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "rejected", first.Attempt}); importPlanCode(t, err) != "import_plan_stale" {
		t.Fatal("stale settle must not release the newer claim")
	}
	if _, err := stageTestImportPlan(owner, minted.Token); importPlanCode(t, err) != "import_token_busy" {
		t.Fatal("newer claim must still be exclusively held after stale attempts")
	}
	// Only the current attempt's settle releases the claim for a retry.
	if _, err := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "rejected", second.Attempt}); err != nil {
		t.Fatalf("current settle: %v", err)
	}
	if _, err := stageTestImportPlan(owner, minted.Token); err != nil {
		t.Fatalf("current settle must release the claim: %v", err)
	}
}

func TestImportPlanStaleCommittedSettleIsTerminalTruth(t *testing.T) {
	owner, _ := newImportPlanTestOwner(t)
	minted := mintTestImportPlan(t, owner, nil)
	first, err := stageTestImportPlan(owner, minted.Token)
	if err != nil {
		t.Fatalf("first stage: %v", err)
	}
	// The real flow binds before submitting; the receipt of that submission
	// was lost, so Python settled unknown and retried.
	if _, err := owner.bind(importPlanBindRequest{importPlanContract, minted.Token, "imp-late", first.Attempt}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, err := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "unknown", first.Attempt}); err != nil {
		t.Fatalf("unknown settle: %v", err)
	}
	if _, err := stageTestImportPlan(owner, minted.Token); err != nil {
		t.Fatalf("restage: %v", err)
	}
	settled, err := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "committed", first.Attempt})
	if err != nil || !settled.Consumed {
		t.Fatalf("stale committed settle: %v %+v", err, settled)
	}
	if _, err := stageTestImportPlan(owner, minted.Token); importPlanCode(t, err) != "import_token_consumed" {
		t.Fatal("terminal committed truth must consume the token")
	}
}

func TestImportPlanCommittedSettleRejectsUnissuedOrUnboundClaims(t *testing.T) {
	owner, _ := newImportPlanTestOwner(t)
	minted := mintTestImportPlan(t, owner, nil)
	// Never staged, never bound: attempt 0 must not consume the token.
	if _, err := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "committed", 0}); importPlanCode(t, err) != "import_plan_invalid" {
		t.Fatal("unissued attempt must be rejected")
	}
	staged, err := stageTestImportPlan(owner, minted.Token)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	// Issued attempt but the plan never bound a prefix: no business submission
	// could have happened, so committed is still rejected.
	if _, err := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "committed", staged.Attempt}); importPlanCode(t, err) != "import_plan_invalid" {
		t.Fatal("unbound claim must be rejected")
	}
	// An attempt beyond the issued counter is pseudo-committed too.
	if _, err := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "committed", staged.Attempt + 5}); importPlanCode(t, err) != "import_plan_invalid" {
		t.Fatal("unissued future attempt must be rejected")
	}
	// None of the rejections consumed the token or broke the claim.
	if _, err := owner.bind(importPlanBindRequest{importPlanContract, minted.Token, "imp-real", staged.Attempt}); err != nil {
		t.Fatalf("bind after rejections: %v", err)
	}
	settled, err := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "committed", staged.Attempt})
	if err != nil || !settled.Consumed {
		t.Fatalf("genuine committed after rejections: %v %+v", err, settled)
	}
}

// TestImportPlanConcurrentStageBindSettleKeepsClaimIdentitySerializes the
// claim lifecycle under real parallelism: goroutines race stage→bind→settle
// on one token, and every bind/settle must operate on exactly its own staged
// attempt. Under -race this regression catches a claim issued or snapshotted
// outside the owner's critical section.
func TestImportPlanConcurrentStageBindSettleKeepsClaimIdentity(t *testing.T) {
	owner, _ := newImportPlanTestOwner(t)
	minted := mintTestImportPlan(t, owner, nil)
	const workers = 8
	const rounds = 25
	var mutex sync.Mutex
	stale := 0
	claims := 0
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for round := 0; round < rounds; round++ {
				staged, err := stageTestImportPlan(owner, minted.Token)
				if err != nil {
					continue // busy while another worker holds the claim
				}
				mutex.Lock()
				claims++
				mutex.Unlock()
				if _, bindErr := owner.bind(importPlanBindRequest{importPlanContract, minted.Token, "imp-race", staged.Attempt}); bindErr != nil {
					var productErr *v2.ProductError
					if errors.As(bindErr, &productErr) && productErr.Code == "import_plan_stale" {
						mutex.Lock()
						stale++
						mutex.Unlock()
					}
					continue
				}
				if _, settleErr := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "rejected", staged.Attempt}); settleErr != nil {
					var productErr *v2.ProductError
					if errors.As(settleErr, &productErr) && productErr.Code == "import_plan_stale" {
						mutex.Lock()
						stale++
						mutex.Unlock()
					}
				}
			}
		}()
	}
	group.Wait()
	if stale != 0 {
		t.Fatalf("claim identity leaked across attempts: stale=%d", stale)
	}
	if claims == 0 {
		t.Fatal("no worker ever claimed the plan")
	}
	// The token is still usable and consumable after the race.
	final, err := stageTestImportPlan(owner, minted.Token)
	if err != nil {
		t.Fatalf("final stage: %v", err)
	}
	if _, err := owner.bind(importPlanBindRequest{importPlanContract, minted.Token, "imp-race", final.Attempt}); err != nil {
		t.Fatalf("final bind: %v", err)
	}
	settled, err := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "committed", final.Attempt})
	if err != nil || !settled.Consumed {
		t.Fatalf("final commit: %v %+v", err, settled)
	}
}

func TestImportPlanRejectedSettleReleasesClaimAndKeepsPrefix(t *testing.T) {
	owner, _ := newImportPlanTestOwner(t)
	minted := mintTestImportPlan(t, owner, nil)
	staged, err := stageTestImportPlan(owner, minted.Token)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := owner.bind(importPlanBindRequest{importPlanContract, minted.Token, "imp-retry", staged.Attempt}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, err := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "rejected", staged.Attempt}); err != nil {
		t.Fatalf("settle rejected: %v", err)
	}
	restaged, err := stageTestImportPlan(owner, minted.Token)
	if err != nil {
		t.Fatalf("rejected plan must be restageable: %v", err)
	}
	if _, err := owner.bind(importPlanBindRequest{importPlanContract, minted.Token, "imp-other", 99}); importPlanCode(t, err) != "import_plan_stale" {
		t.Fatal("restaged claim must reject a foreign attempt bind")
	}
	if _, err := owner.bind(importPlanBindRequest{importPlanContract, minted.Token, "imp-other", restaged.Attempt}); importPlanCode(t, err) != "import_idempotency_mismatch" {
		t.Fatal("rejected settle must keep the bound prefix")
	}
}

func TestImportPlanUnknownSettleReleasesClaimWithoutReplayOrConsumption(t *testing.T) {
	owner, _ := newImportPlanTestOwner(t)
	minted := mintTestImportPlan(t, owner, nil)
	staged, err := stageTestImportPlan(owner, minted.Token)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	settled, err := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "unknown", staged.Attempt})
	if err != nil || settled.Consumed {
		t.Fatalf("unknown settle must not consume: %v %+v", err, settled)
	}
	if _, err := stageTestImportPlan(owner, minted.Token); err != nil {
		t.Fatalf("unknown settle must allow a same-token retry: %v", err)
	}
}

func TestImportPlanSettleRejectsUnknownOutcomeAndForeignContract(t *testing.T) {
	owner, _ := newImportPlanTestOwner(t)
	minted := mintTestImportPlan(t, owner, nil)
	if _, err := owner.settle(importPlanSettleRequest{importPlanContract, minted.Token, "replayed", 1}); importPlanCode(t, err) != "import_plan_invalid" {
		t.Fatal("expected settle outcome validation")
	}
	if _, err := owner.mint(importPlanMintRequest{Contract: "other", Collection: "c", GrantID: "g", SchemaRevision: "s", CapabilityHash: "cap", SourceHash: "h", Mode: "create_only"}); importPlanCode(t, err) != "import_plan_invalid" {
		t.Fatal("expected contract validation")
	}
}

func TestImportPlanMintRequiresWorkspaceIdentity(t *testing.T) {
	owner := newImportPlanOwner("")
	_, err := owner.mint(importPlanMintRequest{Contract: importPlanContract})
	if importPlanCode(t, err) != "import_plan_invalid" || len(owner.plans) != 0 {
		t.Fatalf("unbound owner minted a plan: %v", err)
	}
}
