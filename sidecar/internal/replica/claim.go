package replica

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrStaleClaim          = errors.New("lease.stale_claim")
	ErrTakeoverUnsafe      = errors.New("lease.takeover_unsafe")
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

// Claim records local advisory ownership. It is not a distributed lock: SMB
// writers may publish concurrent immutable heads and the conflict model keeps
// every head visible until the user resolves it.
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

func validateClaim(claim Claim, strength CoordinationStrength) error {
	if claim.Strength != strength ||
		strings.TrimSpace(claim.WorkspaceID) == "" ||
		strings.TrimSpace(claim.DeviceID) == "" ||
		strings.TrimSpace(claim.ClaimID) == "" ||
		strings.TrimSpace(claim.Nonce) == "" ||
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

func sameClaimIdentity(left, right Claim) bool {
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
