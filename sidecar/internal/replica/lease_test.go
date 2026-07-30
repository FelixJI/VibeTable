package replica

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func sqliteTestPath(t *testing.T, name string) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "vibetable-replica-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var removeErr error
		for attempt := 0; attempt < 20; attempt++ {
			removeErr = os.RemoveAll(directory)
			if removeErr == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Errorf("remove sqlite test directory: %v", removeErr)
	})
	return filepath.Join(directory, name)
}

func testClaim(
	workspaceID string,
	deviceID string,
	claimID string,
	nonce string,
	strength CoordinationStrength,
	now time.Time,
) Claim {
	return Claim{
		WorkspaceID: workspaceID,
		DeviceID:    deviceID,
		ClaimID:     claimID,
		FenceEpoch:  1,
		Nonce:       nonce,
		Strength:    strength,
		Mode:        Writable,
		IssuedAt:    now,
		HeartbeatAt: now,
		ExpiresAt:   now.Add(time.Minute),
	}
}

func TestPersistentStrongLeaseBindsIdentityAndSurvivesRestart(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	path := sqliteTestPath(t, filepath.Join("coordination", "lease.db"))
	lease, err := OpenPersistentStrongLease(path)
	if err != nil {
		t.Fatal(err)
	}
	lease.now = func() time.Time { return now }
	first, err := lease.Acquire(
		testClaim("w", "device-a", "claim-a", "nonce-a", Strong, now),
		false,
	)
	if err != nil || first.FenceEpoch != 1 {
		t.Fatalf("first claim: %#v %v", first, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenPersistentStrongLease(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return now }
	if err := reopened.Validate(first); err != nil {
		t.Fatalf("reopened lease rejected current claim: %v", err)
	}
	mutations := map[string]func(Claim) Claim{
		"workspace": func(value Claim) Claim {
			value.WorkspaceID = "other"
			return value
		},
		"device": func(value Claim) Claim {
			value.DeviceID = "device-b"
			return value
		},
		"nonce": func(value Claim) Claim {
			value.Nonce = "nonce-b"
			return value
		},
		"expiry": func(value Claim) Claim {
			value.ExpiresAt = value.ExpiresAt.Add(time.Second)
			return value
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if err := reopened.Validate(mutate(first)); !errors.Is(err, ErrStaleClaim) {
				t.Fatalf("mutated claim must fail closed, got %v", err)
			}
		})
	}
}

func TestStrongLeaseFencesExpiredOwnerAndRejectsLiveTakeover(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	lease := NewStrongLease()
	lease.now = func() time.Time { return now }
	first, err := lease.Acquire(
		testClaim("w", "a", "a1", "nonce-a", Strong, now),
		false,
	)
	if err != nil || first.FenceEpoch != 1 {
		t.Fatalf("first claim: %#v %v", first, err)
	}
	if _, err := lease.Acquire(
		testClaim("w", "b", "b1", "nonce-b", Strong, now),
		true,
	); !errors.Is(err, ErrTakeoverUnsafe) {
		t.Fatalf("live takeover must not be promised: %v", err)
	}
	now = now.Add(2 * time.Minute)
	next := testClaim("w", "b", "b1", "nonce-b", Strong, now)
	next.PreviousClaimID = first.ClaimID
	second, err := lease.Acquire(next, false)
	if err != nil || second.FenceEpoch != 2 ||
		!errors.Is(lease.Validate(first), ErrStaleClaim) {
		t.Fatalf("fencing failed: %#v %v", second, err)
	}
}

func TestPersistentStrongLeaseCASAllowsOnlyOneConcurrentOwner(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	path := sqliteTestPath(t, "lease.db")
	left, err := OpenPersistentStrongLease(path)
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := OpenPersistentStrongLease(path)
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	left.now = func() time.Time { return now }
	right.now = func() time.Time { return now }

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, candidate := range []struct {
		lease *StrongLease
		claim Claim
	}{
		{left, testClaim("w", "a", "a", "nonce-a", Strong, now)},
		{right, testClaim("w", "b", "b", "nonce-b", Strong, now)},
	} {
		wait.Add(1)
		go func(candidate struct {
			lease *StrongLease
			claim Claim
		}) {
			defer wait.Done()
			<-start
			_, err := candidate.lease.AcquireContext(
				context.Background(), candidate.claim, false,
			)
			results <- err
		}(candidate)
	}
	close(start)
	wait.Wait()
	close(results)
	var succeeded, rejected int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrLeaseHeld):
			rejected++
		default:
			t.Fatalf("unexpected concurrent acquire error: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded=%d rejected=%d", succeeded, rejected)
	}
}

func TestPersistentStrongLeaseFailsClosedOnDiskTampering(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	path := sqliteTestPath(t, "lease.db")
	lease, err := OpenPersistentStrongLease(path)
	if err != nil {
		t.Fatal(err)
	}
	lease.now = func() time.Time { return now }
	claim, err := lease.Acquire(
		testClaim("w", "device-a", "claim-a", "nonce-a", Strong, now),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	tamperedClaim := claim
	tamperedClaim.DeviceID = "attacker"
	raw, err := json.Marshal(tamperedClaim)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`UPDATE strong_leases SET claim_json = ? WHERE workspace_id = ?`,
		raw, claim.WorkspaceID,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPersistentStrongLease(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return now }
	if err := reopened.Validate(claim); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("tampered persisted claim did not fail closed: %v", err)
	}
}
