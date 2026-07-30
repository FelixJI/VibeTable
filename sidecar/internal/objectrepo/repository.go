package objectrepo

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

type ObjectID string
type ManifestID string

type Authority struct {
	WorkspaceID string `json:"workspaceId"`
	FenceEpoch  uint64 `json:"fenceEpoch"`
	ClaimID     string `json:"claimId"`
}

type ObjectInput struct {
	Name    string
	Content []byte
}

type ManifestInput struct {
	Name    string
	Labels  map[string]string
	Payload json.RawMessage
}

type CommitRequest struct {
	Authority Authority
	Objects   []ObjectInput
	Manifests []ManifestInput
}

type DurableCommitReceipt struct {
	WorkspaceID string                `json:"workspaceId"`
	FenceEpoch  uint64                `json:"fenceEpoch"`
	ClaimID     string                `json:"claimId"`
	Objects     map[string]ObjectID   `json:"objects"`
	Manifests   map[string]ManifestID `json:"manifests"`
	Revision    uint64                `json:"revision"`
	FlushedAt   time.Time             `json:"flushedAt"`
	Durable     bool                  `json:"durable"`
}

type VerificationReport struct {
	Valid   bool       `json:"valid"`
	Checked []ObjectID `json:"checked"`
	Missing []ObjectID `json:"missing"`
	Corrupt []ObjectID `json:"corrupt"`
}

// StorageInventory measures a set of public objects after deduplication.
// PhysicalBytes is exact only when PhysicalBytesEstimated is false. Kopia
// reports unique packed content payload bytes; pack/index overhead and sharing
// with objects outside the set make that value an estimate of on-disk usage.
type StorageInventory struct {
	RepositoryRevision     uint64
	ObjectCount            uint64
	UniqueContentCount     uint64
	LogicalBytes           uint64
	PhysicalBytes          uint64
	PhysicalBytesEstimated bool
}

type StorageInventorySource interface {
	StorageInventory(context.Context, []ObjectID) (StorageInventory, error)
}

// RepositoryUsageSource reports the real bytes occupied by every blob in the
// repository, including pack/index/format overhead. It is intentionally
// separate from StorageInventory, whose physical size can be an estimate for
// a selected root set.
type RepositoryUsageSource interface {
	RepositoryUsage(context.Context) (uint64, error)
}

type RootPin struct {
	PinID       string     `json:"pinId"`
	WorkspaceID string     `json:"workspaceId"`
	Roots       []ObjectID `json:"roots"`
	Purpose     string     `json:"purpose"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type ManifestRecord struct {
	ID      ManifestID        `json:"id"`
	Name    string            `json:"name"`
	Labels  map[string]string `json:"labels"`
	Payload json.RawMessage   `json:"payload"`
}

type Repository interface {
	AcceptAuthority(
		ctx context.Context,
		expected *Authority,
		next Authority,
	) error
	Commit(context.Context, CommitRequest) (DurableCommitReceipt, error)
	Open(context.Context, ObjectID) (io.ReadCloser, error)
	GetManifest(context.Context, ManifestID) (ManifestRecord, error)
	Verify(context.Context, []ObjectID) (VerificationReport, error)
	Pin(
		ctx context.Context,
		authority Authority,
		roots []ObjectID,
		purpose string,
		expiry *time.Time,
	) (RootPin, error)
	ReleasePin(context.Context, Authority, string) error
	ListPins(context.Context) ([]RootPin, error)
}
