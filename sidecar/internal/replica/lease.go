package replica

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrLeaseHeld           = errors.New("lease.held")
	ErrStaleClaim          = errors.New("lease.stale_claim")
	ErrTakeoverUnsafe      = errors.New("lease.takeover_unsafe")
	ErrCASConflict         = errors.New("lease.cas_conflict")
	ErrInvalidClaim        = errors.New("lease.claim_invalid")
	ErrWorkspaceMismatch   = errors.New("replica.workspace_mismatch")
	ErrPublicationExists   = errors.New("replica.publication_exists")
	ErrPublicationTampered = errors.New("replica.publication_tampered")
	ErrParentMissing       = errors.New("replica.parent_missing")
)

type CoordinationStrength string

const (
	Strong   CoordinationStrength = "strong"
	Advisory CoordinationStrength = "advisory"
)

type ClaimMode string

const (
	Writable    ClaimMode = "writable"
	Provisional ClaimMode = "provisional"
)

type Claim struct {
	WorkspaceID     string               `json:"workspaceId"`
	DeviceID        string               `json:"deviceId"`
	ClaimID         string               `json:"claimId"`
	FenceEpoch      uint64               `json:"fenceEpoch"`
	Nonce           string               `json:"nonce"`
	Strength        CoordinationStrength `json:"strength"`
	Mode            ClaimMode            `json:"mode"`
	IssuedAt        time.Time            `json:"issuedAt"`
	HeartbeatAt     time.Time            `json:"heartbeatAt"`
	ExpiresAt       time.Time            `json:"expiresAt"`
	PreviousClaimID string               `json:"previousClaimId,omitempty"`
}

type LeaseRecord struct {
	Claim    Claim
	Revision uint64
}

// LeaseCASStore is the minimum provider contract required for a strong lease.
// CompareAndSwap must be linearizable for a workspace key.
type LeaseCASStore interface {
	Load(context.Context, string) (LeaseRecord, bool, error)
	CompareAndSwap(
		context.Context,
		string,
		uint64,
		bool,
		Claim,
	) (LeaseRecord, error)
	Close() error
}

type StrongLease struct {
	store LeaseCASStore
	now   func() time.Time
}

func NewStrongLease() *StrongLease {
	return &StrongLease{
		store: newMemoryLeaseStore(),
		now:   func() time.Time { return time.Now().UTC() },
	}
}

func OpenPersistentStrongLease(path string) (*StrongLease, error) {
	store, err := openSQLiteLeaseStore(path)
	if err != nil {
		return nil, err
	}
	return &StrongLease{
		store: store,
		now:   func() time.Time { return time.Now().UTC() },
	}, nil
}

func NewStrongLeaseWithStore(store LeaseCASStore) (*StrongLease, error) {
	if store == nil {
		return nil, errors.New("lease.cas_store_required")
	}
	return &StrongLease{
		store: store,
		now:   func() time.Time { return time.Now().UTC() },
	}, nil
}

func (lease *StrongLease) Acquire(next Claim, force bool) (Claim, error) {
	return lease.AcquireContext(context.Background(), next, force)
}

func (lease *StrongLease) AcquireContext(
	ctx context.Context,
	next Claim,
	force bool,
) (Claim, error) {
	if lease == nil || lease.store == nil {
		return Claim{}, errors.New("lease.store_required")
	}
	now := lease.now().UTC()
	next = normalizeNewClaim(next, now)
	if err := validateClaim(next, Strong); err != nil ||
		!next.ExpiresAt.After(now) {
		if err != nil {
			return Claim{}, err
		}
		return Claim{}, ErrInvalidClaim
	}
	for attempt := 0; attempt < 3; attempt++ {
		current, found, err := lease.store.Load(ctx, next.WorkspaceID)
		if err != nil {
			return Claim{}, err
		}
		if found {
			if err := validateLeaseRecord(
				current, next.WorkspaceID,
			); err != nil {
				return Claim{}, err
			}
		}
		if found && current.Claim.ExpiresAt.After(now) {
			if force {
				return Claim{}, ErrTakeoverUnsafe
			}
			return Claim{}, ErrLeaseHeld
		}
		expectedRevision := uint64(0)
		if found {
			expectedRevision = current.Revision
			if next.PreviousClaimID != current.Claim.ClaimID {
				return Claim{}, ErrStaleClaim
			}
			next.FenceEpoch = current.Claim.FenceEpoch + 1
		} else {
			if next.PreviousClaimID != "" {
				return Claim{}, ErrStaleClaim
			}
			next.FenceEpoch = 1
		}
		record, err := lease.store.CompareAndSwap(
			ctx,
			next.WorkspaceID,
			expectedRevision,
			found,
			next,
		)
		if errors.Is(err, ErrCASConflict) {
			continue
		}
		if err != nil {
			return Claim{}, err
		}
		if err := validateLeaseRecord(
			record, next.WorkspaceID,
		); err != nil ||
			record.Revision != expectedRevision+1 ||
			!sameLeaseIdentity(record.Claim, next) ||
			!record.Claim.ExpiresAt.Equal(next.ExpiresAt) {
			if err != nil {
				return Claim{}, err
			}
			return Claim{}, ErrCASConflict
		}
		return record.Claim, nil
	}
	return Claim{}, ErrCASConflict
}

func (lease *StrongLease) Renew(
	ctx context.Context,
	current Claim,
	nextExpiry time.Time,
) (Claim, error) {
	if lease == nil || lease.store == nil {
		return Claim{}, errors.New("lease.store_required")
	}
	if err := validateClaim(current, Strong); err != nil {
		return Claim{}, err
	}
	now := lease.now().UTC()
	if !nextExpiry.After(now) {
		return Claim{}, ErrInvalidClaim
	}
	record, found, err := lease.store.Load(ctx, current.WorkspaceID)
	if err != nil {
		return Claim{}, err
	}
	if found {
		if err := validateLeaseRecord(
			record, current.WorkspaceID,
		); err != nil {
			return Claim{}, err
		}
	}
	if !found || !sameLeaseIdentity(record.Claim, current) ||
		!record.Claim.ExpiresAt.Equal(current.ExpiresAt) ||
		!record.Claim.ExpiresAt.After(now) {
		return Claim{}, ErrStaleClaim
	}
	next := record.Claim
	if now.Before(next.HeartbeatAt) {
		return Claim{}, ErrInvalidClaim
	}
	next.HeartbeatAt = now
	next.ExpiresAt = nextExpiry.UTC()
	updated, err := lease.store.CompareAndSwap(
		ctx, current.WorkspaceID, record.Revision, true, next,
	)
	if errors.Is(err, ErrCASConflict) {
		return Claim{}, ErrStaleClaim
	}
	if err != nil {
		return Claim{}, err
	}
	if err := validateLeaseRecord(
		updated, current.WorkspaceID,
	); err != nil ||
		updated.Revision != record.Revision+1 ||
		!sameLeaseIdentity(updated.Claim, next) ||
		!updated.Claim.ExpiresAt.Equal(next.ExpiresAt) {
		if err != nil {
			return Claim{}, err
		}
		return Claim{}, ErrCASConflict
	}
	return updated.Claim, nil
}

func (lease *StrongLease) Validate(claim Claim) error {
	return lease.ValidateContext(context.Background(), claim)
}

func (lease *StrongLease) ValidateContext(
	ctx context.Context,
	claim Claim,
) error {
	if lease == nil || lease.store == nil {
		return errors.New("lease.store_required")
	}
	if err := validateClaim(claim, Strong); err != nil {
		return err
	}
	current, found, err := lease.store.Load(ctx, claim.WorkspaceID)
	if err != nil {
		return err
	}
	if found {
		if err := validateLeaseRecord(
			current, claim.WorkspaceID,
		); err != nil {
			return err
		}
	}
	if !found ||
		!sameLeaseIdentity(current.Claim, claim) ||
		!current.Claim.ExpiresAt.Equal(claim.ExpiresAt) ||
		!current.Claim.ExpiresAt.After(lease.now().UTC()) {
		return ErrStaleClaim
	}
	return nil
}

func (lease *StrongLease) Close() error {
	if lease == nil || lease.store == nil {
		return nil
	}
	err := lease.store.Close()
	lease.store = nil
	return err
}

func normalizeNewClaim(claim Claim, now time.Time) Claim {
	claim.WorkspaceID = strings.TrimSpace(claim.WorkspaceID)
	claim.DeviceID = strings.TrimSpace(claim.DeviceID)
	claim.ClaimID = strings.TrimSpace(claim.ClaimID)
	claim.Nonce = strings.TrimSpace(claim.Nonce)
	claim.PreviousClaimID = strings.TrimSpace(claim.PreviousClaimID)
	if claim.Mode == "" {
		claim.Mode = Writable
	}
	if claim.FenceEpoch == 0 {
		claim.FenceEpoch = 1
	}
	claim.IssuedAt = now
	claim.HeartbeatAt = now
	claim.ExpiresAt = claim.ExpiresAt.UTC()
	return claim
}

func validateClaim(claim Claim, strength CoordinationStrength) error {
	if claim.Strength != strength ||
		claim.WorkspaceID == "" ||
		claim.DeviceID == "" ||
		claim.ClaimID == "" ||
		claim.Nonce == "" ||
		claim.FenceEpoch == 0 ||
		(claim.Mode != Writable && claim.Mode != Provisional) ||
		claim.IssuedAt.IsZero() ||
		claim.HeartbeatAt.IsZero() ||
		claim.ExpiresAt.IsZero() ||
		claim.HeartbeatAt.Before(claim.IssuedAt) ||
		!claim.ExpiresAt.After(claim.HeartbeatAt) {
		return ErrInvalidClaim
	}
	return nil
}

func sameLeaseIdentity(left, right Claim) bool {
	return left.WorkspaceID == right.WorkspaceID &&
		left.DeviceID == right.DeviceID &&
		left.ClaimID == right.ClaimID &&
		left.FenceEpoch == right.FenceEpoch &&
		left.Nonce == right.Nonce &&
		left.Strength == right.Strength &&
		left.Mode == right.Mode &&
		left.IssuedAt.Equal(right.IssuedAt) &&
		left.HeartbeatAt.Equal(right.HeartbeatAt) &&
		left.PreviousClaimID == right.PreviousClaimID
}

func validateLeaseRecord(
	record LeaseRecord,
	workspaceID string,
) error {
	if record.Revision == 0 ||
		record.Claim.WorkspaceID != workspaceID ||
		validateClaim(record.Claim, Strong) != nil {
		return ErrStaleClaim
	}
	return nil
}

type memoryLeaseStore struct {
	mu      sync.Mutex
	records map[string]LeaseRecord
}

func newMemoryLeaseStore() *memoryLeaseStore {
	return &memoryLeaseStore{records: map[string]LeaseRecord{}}
}

func (store *memoryLeaseStore) Load(
	_ context.Context,
	workspaceID string,
) (LeaseRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.records[workspaceID]
	return record, found, nil
}

func (store *memoryLeaseStore) CompareAndSwap(
	_ context.Context,
	workspaceID string,
	expectedRevision uint64,
	expectedExists bool,
	next Claim,
) (LeaseRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, found := store.records[workspaceID]
	if found != expectedExists ||
		(found && current.Revision != expectedRevision) {
		return LeaseRecord{}, ErrCASConflict
	}
	record := LeaseRecord{Claim: next, Revision: expectedRevision + 1}
	store.records[workspaceID] = record
	return record, nil
}

func (store *memoryLeaseStore) Close() error { return nil }

type sqliteLeaseStore struct {
	db *sql.DB
}

func openSQLiteLeaseStore(path string) (*sqliteLeaseStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("lease.database_path_required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// PRAGMA synchronous and busy_timeout are connection-local. Keep one
	// short-lived-operation connection so every write uses FULL semantics.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		PRAGMA busy_timeout=5000;
		CREATE TABLE IF NOT EXISTS strong_leases (
			workspace_id TEXT PRIMARY KEY,
			revision INTEGER NOT NULL,
			claim_hash TEXT NOT NULL,
			claim_json BLOB NOT NULL
		);
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize lease database: %w", err)
	}
	return &sqliteLeaseStore{db: db}, nil
}

func (store *sqliteLeaseStore) Load(
	ctx context.Context,
	workspaceID string,
) (LeaseRecord, bool, error) {
	var (
		record LeaseRecord
		raw    []byte
		hash   string
	)
	err := store.db.QueryRowContext(
		ctx,
		`SELECT revision, claim_hash, claim_json FROM strong_leases
		 WHERE workspace_id = ?`,
		workspaceID,
	).Scan(&record.Revision, &hash, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return LeaseRecord{}, false, nil
	}
	if err != nil {
		return LeaseRecord{}, false, err
	}
	if hash != contentDigest(raw) {
		return LeaseRecord{}, false, errors.Join(
			ErrStaleClaim,
			errors.New("lease.record_corrupt"),
		)
	}
	if err := json.Unmarshal(raw, &record.Claim); err != nil ||
		record.Claim.WorkspaceID != workspaceID ||
		validateClaim(record.Claim, Strong) != nil {
		return LeaseRecord{}, false, errors.Join(
			ErrStaleClaim,
			errors.New("lease.record_corrupt"),
			err,
		)
	}
	return record, true, nil
}

func (store *sqliteLeaseStore) CompareAndSwap(
	ctx context.Context,
	workspaceID string,
	expectedRevision uint64,
	expectedExists bool,
	next Claim,
) (LeaseRecord, error) {
	if next.WorkspaceID != workspaceID {
		return LeaseRecord{}, ErrWorkspaceMismatch
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return LeaseRecord{}, err
	}
	revision := expectedRevision + 1
	var result sql.Result
	if expectedExists {
		result, err = store.db.ExecContext(ctx, `
			UPDATE strong_leases
			SET revision = ?, claim_hash = ?, claim_json = ?
			WHERE workspace_id = ? AND revision = ?`,
			revision, contentDigest(raw), raw,
			workspaceID, expectedRevision,
		)
	} else {
		result, err = store.db.ExecContext(ctx, `
			INSERT INTO strong_leases (
				workspace_id, revision, claim_hash, claim_json
			) VALUES (?, ?, ?, ?)
			ON CONFLICT(workspace_id) DO NOTHING`,
			workspaceID, revision, contentDigest(raw), raw,
		)
	}
	if err != nil {
		return LeaseRecord{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return LeaseRecord{}, err
	}
	if affected != 1 {
		return LeaseRecord{}, ErrCASConflict
	}
	return LeaseRecord{Claim: next, Revision: revision}, nil
}

func contentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (store *sqliteLeaseStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	_, checkpointErr := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	closeErr := store.db.Close()
	store.db = nil
	return errors.Join(checkpointErr, closeErr)
}
