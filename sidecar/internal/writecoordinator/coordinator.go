// Package writecoordinator owns the single authoritative write gate for a
// workspace. Captures freeze all mutable roots while holding the same gate.
package writecoordinator

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

var (
	ErrInvalidIdentity  = errors.New("workspace.write.invalid_identity")
	ErrStaleToken       = errors.New("workspace.write.stale_token")
	ErrCounterExhausted = errors.New("workspace.write.counter_exhausted")
	ErrRecoveryRequired = errors.New("workspace.write.recovery_required")
)

type Token struct {
	WorkspaceID  string
	SessionEpoch uint64
	FenceEpoch   uint64
	ClaimID      string
}

func (token Token) Authority() objectrepo.Authority {
	return objectrepo.Authority{
		WorkspaceID: token.WorkspaceID,
		FenceEpoch:  token.FenceEpoch,
		ClaimID:     token.ClaimID,
	}
}

type Counters struct {
	SessionEpoch     uint64
	FenceEpoch       uint64
	MutationRevision uint64
	SnapshotSequence uint64
}

type WriteIntent struct {
	Token            Token
	MutationRevision uint64
}

type WriteReceipt struct {
	Token            Token
	MutationRevision uint64
}

type RecoveryState struct {
	Token                   Token
	Counters                Counters
	PendingMutationRevision uint64
}

type PersistenceFaultPoint string

const FaultBeforeFinishCommittedMutation PersistenceFaultPoint = "before-finish-committed-mutation"

type PersistenceFaultInjector func(PersistenceFaultPoint) error

type CaptureIntent struct {
	Token            Token
	MutationRevision uint64
	SnapshotSequence uint64
}

type FrozenRoots struct {
	DatabaseView string
	TopologyRoot objectrepo.ManifestID
	FileRoot     objectrepo.ManifestID
	AuditAnchor  string
}

type CaptureHandle struct {
	CaptureIntent
	FrozenRoots
	CapturedAt time.Time
}

type HighWatermark struct {
	SourceEpoch    string `json:"sourceEpoch"`
	SourceSequence uint64 `json:"sourceSequence"`
	ChainHash      string `json:"chainHash"`
}

type CaptureIntentRecord struct {
	CaptureIntent
	FrozenRoots
	State      string    `json:"state"`
	PreparedAt time.Time `json:"preparedAt"`
	CapturedAt time.Time `json:"capturedAt,omitempty"`
}

type WorkspaceWriteCoordinator struct {
	gate  chan struct{}
	now   func() time.Time
	store *persistentStore

	mu               sync.RWMutex
	token            Token
	mutationRevision uint64
	snapshotSequence uint64
	highWatermark    HighWatermark
	pendingMutation  uint64
}

func New(
	workspaceID string,
	fenceEpoch uint64,
	claimID string,
	sessionEpoch uint64,
) (*WorkspaceWriteCoordinator, error) {
	if strings.TrimSpace(workspaceID) == "" ||
		strings.TrimSpace(claimID) == "" ||
		fenceEpoch == 0 ||
		sessionEpoch == 0 {
		return nil, ErrInvalidIdentity
	}
	return &WorkspaceWriteCoordinator{
		gate: make(chan struct{}, 1),
		now:  func() time.Time { return time.Now().UTC() },
		token: Token{
			WorkspaceID:  workspaceID,
			SessionEpoch: sessionEpoch,
			FenceEpoch:   fenceEpoch,
			ClaimID:      claimID,
		},
	}, nil
}

// OpenPersistent opens or initializes the durable coordination database. An
// existing database must match the supplied workspace authority and session.
// Prepared mutations fail closed until recovery explicitly resolves whether
// their external authoritative transaction committed.
func OpenPersistent(
	databasePath string,
	workspaceID string,
	fenceEpoch uint64,
	claimID string,
	sessionEpoch uint64,
) (*WorkspaceWriteCoordinator, error) {
	coordinator, err := New(workspaceID, fenceEpoch, claimID, sessionEpoch)
	if err != nil {
		return nil, err
	}
	store, state, pendingMutation, err := openPersistentStore(
		databasePath, coordinator.token,
	)
	if err != nil {
		return nil, err
	}
	coordinator.store = store
	coordinator.token = state.Token
	coordinator.mutationRevision = state.MutationRevision
	coordinator.snapshotSequence = state.SnapshotSequence
	coordinator.highWatermark = state.HighWatermark
	coordinator.pendingMutation = pendingMutation
	return coordinator, nil
}

func (coordinator *WorkspaceWriteCoordinator) WithClock(
	clock func() time.Time,
) *WorkspaceWriteCoordinator {
	if clock != nil {
		coordinator.now = clock
	}
	return coordinator
}

// WithPersistenceFaultInjector configures a test seam for durable coordination
// failures. It has no effect when persistence is disabled.
func (coordinator *WorkspaceWriteCoordinator) WithPersistenceFaultInjector(
	injector PersistenceFaultInjector,
) *WorkspaceWriteCoordinator {
	if coordinator.store != nil {
		coordinator.store.setFaultInjector(injector)
	}
	return coordinator
}

func (coordinator *WorkspaceWriteCoordinator) Current() (Token, Counters) {
	coordinator.mu.RLock()
	defer coordinator.mu.RUnlock()
	return coordinator.token, coordinator.countersLocked()
}

func (coordinator *WorkspaceWriteCoordinator) RecoveryState() RecoveryState {
	coordinator.mu.RLock()
	defer coordinator.mu.RUnlock()
	return RecoveryState{
		Token:                   coordinator.token,
		Counters:                coordinator.countersLocked(),
		PendingMutationRevision: coordinator.pendingMutation,
	}
}

func (coordinator *WorkspaceWriteCoordinator) Write(
	ctx context.Context,
	token Token,
	apply func(context.Context, WriteIntent) error,
) (WriteReceipt, error) {
	if apply == nil {
		return WriteReceipt{}, errors.New("workspace.write.callback_required")
	}
	if err := coordinator.acquire(ctx); err != nil {
		return WriteReceipt{}, err
	}
	defer coordinator.release()

	coordinator.mu.Lock()
	if coordinator.pendingMutation != 0 {
		coordinator.mu.Unlock()
		return WriteReceipt{}, fmt.Errorf(
			"%w: mutationRevision=%d",
			ErrRecoveryRequired,
			coordinator.pendingMutation,
		)
	}
	if !tokensEqual(coordinator.token, token) {
		coordinator.mu.Unlock()
		return WriteReceipt{}, ErrStaleToken
	}
	if coordinator.mutationRevision == math.MaxUint64 {
		coordinator.mu.Unlock()
		return WriteReceipt{}, ErrCounterExhausted
	}
	nextRevision := coordinator.mutationRevision + 1
	currentToken := coordinator.token
	currentSequence := coordinator.snapshotSequence
	coordinator.mu.Unlock()

	if coordinator.store != nil {
		if err := coordinator.store.prepareMutation(
			ctx, currentToken, nextRevision, coordinator.now(),
		); err != nil {
			return WriteReceipt{}, err
		}
		coordinator.mu.Lock()
		coordinator.pendingMutation = nextRevision
		coordinator.mu.Unlock()
	}
	if err := apply(ctx, WriteIntent{
		Token:            currentToken,
		MutationRevision: nextRevision,
	}); err != nil {
		if coordinator.store != nil {
			if persistErr := coordinator.store.finishMutation(
				context.WithoutCancel(ctx),
				currentToken,
				nextRevision,
				currentSequence,
				false,
				coordinator.now(),
			); persistErr != nil {
				return WriteReceipt{}, errors.Join(err, persistErr)
			}
			coordinator.mu.Lock()
			if coordinator.pendingMutation == nextRevision {
				coordinator.pendingMutation = 0
			}
			coordinator.mu.Unlock()
		}
		return WriteReceipt{}, err
	}

	coordinator.mu.Lock()
	if coordinator.store != nil {
		if err := coordinator.store.finishMutation(
			context.WithoutCancel(ctx),
			currentToken,
			nextRevision,
			coordinator.snapshotSequence,
			true,
			coordinator.now(),
		); err != nil {
			coordinator.mu.Unlock()
			return WriteReceipt{}, err
		}
	}
	coordinator.mutationRevision = nextRevision
	if coordinator.pendingMutation == nextRevision {
		coordinator.pendingMutation = 0
	}
	receipt := WriteReceipt{
		Token:            coordinator.token,
		MutationRevision: coordinator.mutationRevision,
	}
	coordinator.mu.Unlock()
	return receipt, nil
}

func (coordinator *WorkspaceWriteCoordinator) Capture(
	ctx context.Context,
	token Token,
	freeze func(context.Context, CaptureIntent) (FrozenRoots, error),
) (CaptureHandle, error) {
	if freeze == nil {
		return CaptureHandle{}, errors.New("snapshot.capture.freeze_required")
	}
	if err := coordinator.acquire(ctx); err != nil {
		return CaptureHandle{}, err
	}
	defer coordinator.release()

	coordinator.mu.Lock()
	if coordinator.pendingMutation != 0 {
		coordinator.mu.Unlock()
		return CaptureHandle{}, fmt.Errorf(
			"%w: mutationRevision=%d",
			ErrRecoveryRequired,
			coordinator.pendingMutation,
		)
	}
	if !tokensEqual(coordinator.token, token) {
		coordinator.mu.Unlock()
		return CaptureHandle{}, ErrStaleToken
	}
	if coordinator.snapshotSequence == math.MaxUint64 {
		coordinator.mu.Unlock()
		return CaptureHandle{}, ErrCounterExhausted
	}
	// A sequence belongs to the capture intent, not to the eventual published
	// snapshot. Failed captures deliberately consume their sequence.
	nextSequence := coordinator.snapshotSequence + 1
	intent := CaptureIntent{
		Token:            coordinator.token,
		MutationRevision: coordinator.mutationRevision,
		SnapshotSequence: nextSequence,
	}
	if coordinator.store != nil {
		if err := coordinator.store.prepareCapture(
			ctx, intent, coordinator.highWatermark, coordinator.now(),
		); err != nil {
			coordinator.mu.Unlock()
			return CaptureHandle{}, err
		}
	}
	coordinator.snapshotSequence = nextSequence
	coordinator.mu.Unlock()

	roots, err := freeze(ctx, intent)
	if err != nil {
		if coordinator.store != nil {
			if persistErr := coordinator.store.finishCapture(
				context.WithoutCancel(ctx),
				intent,
				FrozenRoots{},
				"failed",
				coordinator.now(),
			); persistErr != nil {
				return CaptureHandle{}, errors.Join(err, persistErr)
			}
		}
		return CaptureHandle{}, err
	}
	capturedAt := coordinator.now()
	if coordinator.store != nil {
		if err := coordinator.store.finishCapture(
			context.WithoutCancel(ctx),
			intent,
			roots,
			"captured",
			capturedAt,
		); err != nil {
			return CaptureHandle{}, err
		}
	}
	return CaptureHandle{
		CaptureIntent: intent,
		FrozenRoots:   roots,
		CapturedAt:    capturedAt,
	}, nil
}

// Drain executes the source outbox drain while holding the mutation gate and
// persists the resulting high-watermark before releasing the gate.
func (coordinator *WorkspaceWriteCoordinator) Drain(
	ctx context.Context,
	token Token,
	deadline time.Time,
	drain func(context.Context) (HighWatermark, error),
) (HighWatermark, error) {
	if drain == nil {
		return HighWatermark{}, errors.New("workspace.write.drain_required")
	}
	if !deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	if err := coordinator.acquire(ctx); err != nil {
		return HighWatermark{}, err
	}
	defer coordinator.release()

	coordinator.mu.RLock()
	if coordinator.pendingMutation != 0 {
		pending := coordinator.pendingMutation
		coordinator.mu.RUnlock()
		return HighWatermark{}, fmt.Errorf(
			"%w: mutationRevision=%d", ErrRecoveryRequired, pending,
		)
	}
	if !tokensEqual(coordinator.token, token) {
		coordinator.mu.RUnlock()
		return HighWatermark{}, ErrStaleToken
	}
	currentToken := coordinator.token
	mutationRevision := coordinator.mutationRevision
	snapshotSequence := coordinator.snapshotSequence
	coordinator.mu.RUnlock()

	highWatermark, err := drain(ctx)
	if err != nil {
		return HighWatermark{}, err
	}
	if strings.TrimSpace(highWatermark.SourceEpoch) == "" ||
		highWatermark.SourceSequence == 0 ||
		strings.TrimSpace(highWatermark.ChainHash) == "" {
		return HighWatermark{}, errors.New("workspace.write.high_watermark_invalid")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.store != nil {
		if err := coordinator.store.persistState(
			context.WithoutCancel(ctx),
			persistedState{
				Token:            currentToken,
				MutationRevision: mutationRevision,
				SnapshotSequence: snapshotSequence,
				HighWatermark:    highWatermark,
			},
		); err != nil {
			return HighWatermark{}, err
		}
	}
	coordinator.highWatermark = highWatermark
	return highWatermark, nil
}

func (coordinator *WorkspaceWriteCoordinator) HighWatermark() HighWatermark {
	coordinator.mu.RLock()
	defer coordinator.mu.RUnlock()
	return coordinator.highWatermark
}

func (coordinator *WorkspaceWriteCoordinator) RotateSession(
	ctx context.Context,
	expected Token,
) (Token, error) {
	if err := coordinator.acquire(ctx); err != nil {
		return Token{}, err
	}
	defer coordinator.release()

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !tokensEqual(coordinator.token, expected) {
		return Token{}, ErrStaleToken
	}
	if coordinator.pendingMutation != 0 {
		return Token{}, ErrRecoveryRequired
	}
	if coordinator.token.SessionEpoch == math.MaxUint64 {
		return Token{}, ErrCounterExhausted
	}
	next := coordinator.token
	next.SessionEpoch++
	if coordinator.store != nil {
		if err := coordinator.store.persistState(
			context.WithoutCancel(ctx),
			persistedState{
				Token:            next,
				MutationRevision: coordinator.mutationRevision,
				SnapshotSequence: coordinator.snapshotSequence,
				HighWatermark:    coordinator.highWatermark,
			},
		); err != nil {
			return Token{}, err
		}
	}
	coordinator.token = next
	return coordinator.token, nil
}

func (coordinator *WorkspaceWriteCoordinator) TransferFence(
	ctx context.Context,
	expected Token,
	nextClaimID string,
) (Token, error) {
	if strings.TrimSpace(nextClaimID) == "" {
		return Token{}, ErrInvalidIdentity
	}
	if err := coordinator.acquire(ctx); err != nil {
		return Token{}, err
	}
	defer coordinator.release()

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !tokensEqual(coordinator.token, expected) {
		return Token{}, ErrStaleToken
	}
	if coordinator.pendingMutation != 0 {
		return Token{}, ErrRecoveryRequired
	}
	if coordinator.token.FenceEpoch == math.MaxUint64 {
		return Token{}, ErrCounterExhausted
	}
	next := coordinator.token
	next.FenceEpoch++
	next.ClaimID = nextClaimID
	if coordinator.store != nil {
		if err := coordinator.store.persistState(
			context.WithoutCancel(ctx),
			persistedState{
				Token:            next,
				MutationRevision: coordinator.mutationRevision,
				SnapshotSequence: coordinator.snapshotSequence,
				HighWatermark:    coordinator.highWatermark,
			},
		); err != nil {
			return Token{}, err
		}
	}
	coordinator.token = next
	return coordinator.token, nil
}

func (coordinator *WorkspaceWriteCoordinator) CaptureIntent(
	ctx context.Context,
	snapshotSequence uint64,
) (CaptureIntentRecord, error) {
	if coordinator.store == nil {
		return CaptureIntentRecord{}, errors.New("workspace.write.persistence_disabled")
	}
	return coordinator.store.captureIntent(ctx, snapshotSequence)
}

// ResolvePreparedMutation is used by startup recovery after checking the
// external business transaction's durable mutation identity.
func (coordinator *WorkspaceWriteCoordinator) ResolvePreparedMutation(
	ctx context.Context,
	token Token,
	mutationRevision uint64,
	committed bool,
) error {
	if coordinator.store == nil {
		return errors.New("workspace.write.persistence_disabled")
	}
	if err := coordinator.acquire(ctx); err != nil {
		return err
	}
	defer coordinator.release()
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !tokensEqual(coordinator.token, token) {
		return ErrStaleToken
	}
	if err := coordinator.store.finishMutation(
		ctx,
		token,
		mutationRevision,
		coordinator.snapshotSequence,
		committed,
		coordinator.now(),
	); err != nil {
		return err
	}
	if committed && mutationRevision > coordinator.mutationRevision {
		coordinator.mutationRevision = mutationRevision
	}
	if coordinator.pendingMutation == mutationRevision {
		coordinator.pendingMutation = 0
	}
	return nil
}

func (coordinator *WorkspaceWriteCoordinator) Close() error {
	if coordinator.store == nil {
		return nil
	}
	return coordinator.store.close()
}

func (coordinator *WorkspaceWriteCoordinator) acquire(ctx context.Context) error {
	select {
	case coordinator.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (coordinator *WorkspaceWriteCoordinator) release() {
	<-coordinator.gate
}

func (coordinator *WorkspaceWriteCoordinator) countersLocked() Counters {
	return Counters{
		SessionEpoch:     coordinator.token.SessionEpoch,
		FenceEpoch:       coordinator.token.FenceEpoch,
		MutationRevision: coordinator.mutationRevision,
		SnapshotSequence: coordinator.snapshotSequence,
	}
}

func tokensEqual(left, right Token) bool {
	return left == right
}
