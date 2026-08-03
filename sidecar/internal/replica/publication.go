package replica

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Publication struct {
	PublicationID           string    `json:"publicationId"`
	WorkspaceID             string    `json:"workspaceId"`
	Claim                   Claim     `json:"claim"`
	PreviousPublicationHash string    `json:"previousPublicationHash,omitempty"`
	SnapshotID              string    `json:"snapshotId"`
	CatalogRevision         uint64    `json:"catalogRevision"`
	CheckpointID            string    `json:"checkpointId"`
	CreatedAt               time.Time `json:"createdAt"`
	CanonicalHash           string    `json:"canonicalHash"`
}

// AdvisoryDAG is a verified in-memory view rebuilt from immutable remote
// publications. It is deliberately a cache, never a second persistent truth.
type AdvisoryDAG struct {
	workspaceID  string
	mu           sync.Mutex
	publications map[string]Publication
}

func NewAdvisoryDAG(workspaceID string) (*AdvisoryDAG, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, ErrWorkspaceMismatch
	}
	return &AdvisoryDAG{
		workspaceID:  workspaceID,
		publications: map[string]Publication{},
	}, nil
}

func SealPublication(publication Publication) (Publication, error) {
	publication.CreatedAt = publication.CreatedAt.UTC()
	publication.Claim.IssuedAt = publication.Claim.IssuedAt.UTC()
	publication.Claim.HeartbeatAt = publication.Claim.HeartbeatAt.UTC()
	publication.Claim.ExpiresAt = publication.Claim.ExpiresAt.UTC()
	expectedID := fmt.Sprintf(
		"%s/%s/%020d",
		publication.Claim.ClaimID,
		publication.SnapshotID,
		publication.CatalogRevision,
	)
	if publication.PublicationID == "" {
		publication.PublicationID = expectedID
	} else if publication.PublicationID != expectedID {
		return Publication{}, ErrPublicationTampered
	}
	publication.CanonicalHash = ""
	raw, err := canonicalPublicationPayload(publication)
	if err != nil {
		return Publication{}, err
	}
	digest := sha256.Sum256(raw)
	publication.CanonicalHash = "sha256:" + hex.EncodeToString(digest[:])
	return publication, nil
}

func (dag *AdvisoryDAG) Publish(publication Publication) error {
	return dag.PublishContext(context.Background(), publication)
}

func (dag *AdvisoryDAG) PublishContext(ctx context.Context, publication Publication) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := dag.verify(publication); err != nil {
		return err
	}
	dag.mu.Lock()
	defer dag.mu.Unlock()
	if publication.PreviousPublicationHash != "" {
		parentFound := false
		for _, existing := range dag.publications {
			if existing.CanonicalHash == publication.PreviousPublicationHash {
				if publication.CreatedAt.Before(existing.CreatedAt) {
					return ErrPublicationTampered
				}
				parentFound = true
				break
			}
		}
		if !parentFound {
			return ErrParentMissing
		}
	}
	key := publication.PublicationID
	if existing, found := dag.publications[key]; found {
		if existing.CanonicalHash == publication.CanonicalHash {
			return nil
		}
		return ErrPublicationExists
	}
	for _, existing := range dag.publications {
		if existing.CanonicalHash == publication.CanonicalHash {
			return ErrPublicationExists
		}
	}
	dag.publications[key] = publication
	return nil
}

func (dag *AdvisoryDAG) Heads() ([]Publication, error) {
	return dag.HeadsContext(context.Background())
}

func (dag *AdvisoryDAG) HeadsContext(ctx context.Context) ([]Publication, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dag.mu.Lock()
	defer dag.mu.Unlock()
	publications := make([]Publication, 0, len(dag.publications))
	referenced := make(map[string]struct{}, len(dag.publications))
	for _, publication := range dag.publications {
		if err := dag.verify(publication); err != nil {
			return nil, err
		}
		publications = append(publications, publication)
		if publication.PreviousPublicationHash != "" {
			referenced[publication.PreviousPublicationHash] = struct{}{}
		}
	}
	heads := make([]Publication, 0, len(publications))
	for _, publication := range publications {
		if _, isParent := referenced[publication.CanonicalHash]; !isParent {
			heads = append(heads, publication)
		}
	}
	sort.Slice(heads, func(left, right int) bool {
		if heads[left].Claim.FenceEpoch != heads[right].Claim.FenceEpoch {
			return heads[left].Claim.FenceEpoch > heads[right].Claim.FenceEpoch
		}
		return heads[left].CanonicalHash < heads[right].CanonicalHash
	})
	return heads, nil
}

func (dag *AdvisoryDAG) Winner() (Publication, []Publication, bool, error) {
	heads, err := dag.Heads()
	if err != nil || len(heads) == 0 {
		return Publication{}, nil, false, err
	}
	return heads[0], append([]Publication(nil), heads[1:]...), true, nil
}

func (dag *AdvisoryDAG) verify(publication Publication) error {
	if dag == nil || publication.WorkspaceID != dag.workspaceID ||
		publication.Claim.WorkspaceID != dag.workspaceID {
		return ErrWorkspaceMismatch
	}
	if !validSHA256(publication.PreviousPublicationHash, true) ||
		!validSHA256(publication.CanonicalHash, false) {
		return ErrPublicationTampered
	}
	expected, err := SealPublication(Publication{
		PublicationID:           publication.PublicationID,
		WorkspaceID:             publication.WorkspaceID,
		Claim:                   publication.Claim,
		PreviousPublicationHash: publication.PreviousPublicationHash,
		SnapshotID:              publication.SnapshotID,
		CatalogRevision:         publication.CatalogRevision,
		CheckpointID:            publication.CheckpointID,
		CreatedAt:               publication.CreatedAt,
	})
	if err != nil || expected.CanonicalHash != publication.CanonicalHash {
		return ErrPublicationTampered
	}
	return nil
}

func canonicalPublicationPayload(publication Publication) ([]byte, error) {
	if strings.TrimSpace(publication.PublicationID) == "" ||
		strings.TrimSpace(publication.WorkspaceID) == "" ||
		strings.TrimSpace(publication.SnapshotID) == "" ||
		publication.CreatedAt.IsZero() ||
		!validSHA256(publication.PreviousPublicationHash, true) {
		return nil, ErrPublicationTampered
	}
	expectedID := fmt.Sprintf("%s/%s/%020d", publication.Claim.ClaimID,
		publication.SnapshotID, publication.CatalogRevision)
	if publication.PublicationID != expectedID || publication.CatalogRevision == 0 ||
		!validSHA256(publication.CheckpointID, false) ||
		publication.Claim.Nonce != publication.CheckpointID ||
		publication.CreatedAt.Before(publication.Claim.IssuedAt) ||
		publication.CreatedAt.After(publication.Claim.ExpiresAt) {
		return nil, ErrPublicationTampered
	}
	if publication.Claim.WorkspaceID != publication.WorkspaceID {
		return nil, ErrWorkspaceMismatch
	}
	if err := validateClaim(publication.Claim, Advisory); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		PublicationID           string    `json:"publicationId"`
		WorkspaceID             string    `json:"workspaceId"`
		Claim                   Claim     `json:"claim"`
		PreviousPublicationHash string    `json:"previousPublicationHash,omitempty"`
		SnapshotID              string    `json:"snapshotId"`
		CatalogRevision         uint64    `json:"catalogRevision"`
		CheckpointID            string    `json:"checkpointId"`
		CreatedAt               time.Time `json:"createdAt"`
	}{publication.PublicationID, publication.WorkspaceID, publication.Claim,
		publication.PreviousPublicationHash, publication.SnapshotID,
		publication.CatalogRevision, publication.CheckpointID, publication.CreatedAt})
}

func validSHA256(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	const prefix = "sha256:"
	if len(value) != len(prefix)+sha256.Size*2 || !strings.HasPrefix(value, prefix) {
		return false
	}
	_, err := hex.DecodeString(value[len(prefix):])
	return err == nil
}
