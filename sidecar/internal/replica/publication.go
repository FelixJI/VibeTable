package replica

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const minimumPublicationKeyBytes = 32

type Publication struct {
	PublicationID           string    `json:"publicationId"`
	WorkspaceID             string    `json:"workspaceId"`
	Claim                   Claim     `json:"claim"`
	PreviousPublicationHash string    `json:"previousPublicationHash,omitempty"`
	SnapshotID              string    `json:"snapshotId"`
	CreatedAt               time.Time `json:"createdAt"`
	CanonicalHash           string    `json:"canonicalHash"`
	MAC                     string    `json:"mac"`
}

type publicationStore interface {
	Create(context.Context, Publication) error
	List(context.Context, string) ([]Publication, error)
	Close() error
}

type AdvisoryDAG struct {
	workspaceID string
	macKey      []byte
	store       publicationStore
}

func NewAdvisoryDAG(workspaceID string, macKey []byte) (*AdvisoryDAG, error) {
	if err := validateDAGIdentity(workspaceID, macKey); err != nil {
		return nil, err
	}
	return &AdvisoryDAG{
		workspaceID: workspaceID,
		macKey:      bytes.Clone(macKey),
		store:       newMemoryPublicationStore(),
	}, nil
}

func OpenPersistentAdvisoryDAG(
	path string,
	workspaceID string,
	macKey []byte,
) (*AdvisoryDAG, error) {
	if err := validateDAGIdentity(workspaceID, macKey); err != nil {
		return nil, err
	}
	store, err := openSQLitePublicationStore(path)
	if err != nil {
		return nil, err
	}
	return &AdvisoryDAG{
		workspaceID: workspaceID,
		macKey:      bytes.Clone(macKey),
		store:       store,
	}, nil
}

func validateDAGIdentity(workspaceID string, macKey []byte) error {
	if strings.TrimSpace(workspaceID) == "" {
		return ErrWorkspaceMismatch
	}
	if len(macKey) < minimumPublicationKeyBytes {
		return errors.New("replica.publication_key_invalid")
	}
	return nil
}

// SealPublication binds all publication fields, including the full lease
// identity, to a canonical SHA-256 hash and workspace authentication key.
func SealPublication(
	publication Publication,
	macKey []byte,
) (Publication, error) {
	if len(macKey) < minimumPublicationKeyBytes {
		return Publication{}, errors.New("replica.publication_key_invalid")
	}
	publication.CreatedAt = publication.CreatedAt.UTC()
	publication.Claim.IssuedAt = publication.Claim.IssuedAt.UTC()
	publication.Claim.HeartbeatAt = publication.Claim.HeartbeatAt.UTC()
	publication.Claim.ExpiresAt = publication.Claim.ExpiresAt.UTC()
	expectedID := publication.Claim.ClaimID + "/" + publication.SnapshotID
	if publication.PublicationID == "" {
		publication.PublicationID = expectedID
	} else if publication.PublicationID != expectedID {
		return Publication{}, ErrPublicationTampered
	}
	publication.CanonicalHash = ""
	publication.MAC = ""
	raw, err := canonicalPublicationPayload(publication)
	if err != nil {
		return Publication{}, err
	}
	hash := sha256.Sum256(raw)
	publication.CanonicalHash = "sha256:" + hex.EncodeToString(hash[:])
	authenticator := hmac.New(sha256.New, macKey)
	_, _ = authenticator.Write([]byte(publication.CanonicalHash))
	publication.MAC = "hmac-sha256:" +
		hex.EncodeToString(authenticator.Sum(nil))
	return publication, nil
}

func (dag *AdvisoryDAG) Publish(publication Publication) error {
	return dag.PublishContext(context.Background(), publication)
}

func (dag *AdvisoryDAG) PublishContext(
	ctx context.Context,
	publication Publication,
) error {
	if dag == nil || dag.store == nil {
		return errors.New("replica.publication_store_required")
	}
	if err := dag.verify(publication); err != nil {
		return err
	}
	publications, err := dag.verifiedPublications(ctx)
	if err != nil {
		return err
	}
	if publication.PreviousPublicationHash != "" {
		found := false
		for _, existing := range publications {
			if existing.CanonicalHash ==
				publication.PreviousPublicationHash {
				if publication.CreatedAt.Before(existing.CreatedAt) {
					return ErrPublicationTampered
				}
				found = true
				break
			}
		}
		if !found {
			return ErrParentMissing
		}
	}
	return dag.store.Create(ctx, publication)
}

func (dag *AdvisoryDAG) Heads() ([]Publication, error) {
	return dag.HeadsContext(context.Background())
}

func (dag *AdvisoryDAG) HeadsContext(
	ctx context.Context,
) ([]Publication, error) {
	publications, err := dag.verifiedPublications(ctx)
	if err != nil {
		return nil, err
	}
	referenced := make(map[string]struct{}, len(publications))
	for _, publication := range publications {
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
			return heads[left].Claim.FenceEpoch >
				heads[right].Claim.FenceEpoch
		}
		return heads[left].CanonicalHash < heads[right].CanonicalHash
	})
	return heads, nil
}

func (dag *AdvisoryDAG) Winner() (
	Publication,
	[]Publication,
	bool,
	error,
) {
	heads, err := dag.Heads()
	if err != nil {
		return Publication{}, nil, false, err
	}
	if len(heads) == 0 {
		return Publication{}, nil, false, nil
	}
	return heads[0], append([]Publication(nil), heads[1:]...), true, nil
}

func (dag *AdvisoryDAG) Close() error {
	if dag == nil || dag.store == nil {
		return nil
	}
	err := dag.store.Close()
	dag.store = nil
	return err
}

func (dag *AdvisoryDAG) verifiedPublications(
	ctx context.Context,
) ([]Publication, error) {
	if dag == nil || dag.store == nil {
		return nil, errors.New("replica.publication_store_required")
	}
	publications, err := dag.store.List(ctx, dag.workspaceID)
	if err != nil {
		return nil, err
	}
	hashes := make(map[string]struct{}, len(publications))
	for _, publication := range publications {
		if err := dag.verify(publication); err != nil {
			return nil, err
		}
		if _, duplicate := hashes[publication.CanonicalHash]; duplicate {
			return nil, ErrPublicationTampered
		}
		hashes[publication.CanonicalHash] = struct{}{}
	}
	for _, publication := range publications {
		if publication.PreviousPublicationHash == "" {
			continue
		}
		if _, found := hashes[publication.PreviousPublicationHash]; !found {
			return nil, ErrParentMissing
		}
	}
	return publications, nil
}

func (dag *AdvisoryDAG) verify(publication Publication) error {
	if publication.WorkspaceID != dag.workspaceID ||
		publication.Claim.WorkspaceID != dag.workspaceID {
		return ErrWorkspaceMismatch
	}
	if err := validateClaim(publication.Claim, Advisory); err != nil {
		return err
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
		CreatedAt:               publication.CreatedAt,
	}, dag.macKey)
	if err != nil {
		return err
	}
	if !hmac.Equal(
		[]byte(expected.CanonicalHash),
		[]byte(publication.CanonicalHash),
	) || !hmac.Equal([]byte(expected.MAC), []byte(publication.MAC)) {
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
	if publication.PublicationID !=
		publication.Claim.ClaimID+"/"+publication.SnapshotID ||
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
		CreatedAt               time.Time `json:"createdAt"`
	}{
		PublicationID:           publication.PublicationID,
		WorkspaceID:             publication.WorkspaceID,
		Claim:                   publication.Claim,
		PreviousPublicationHash: publication.PreviousPublicationHash,
		SnapshotID:              publication.SnapshotID,
		CreatedAt:               publication.CreatedAt.UTC(),
	})
}

func validSHA256(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	const prefix = "sha256:"
	if len(value) != len(prefix)+sha256.Size*2 ||
		!strings.HasPrefix(value, prefix) {
		return false
	}
	_, err := hex.DecodeString(value[len(prefix):])
	return err == nil
}

type memoryPublicationStore struct {
	mu           sync.Mutex
	publications map[string]Publication
}

func newMemoryPublicationStore() *memoryPublicationStore {
	return &memoryPublicationStore{
		publications: map[string]Publication{},
	}
}

func (store *memoryPublicationStore) Create(
	_ context.Context,
	publication Publication,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := publication.WorkspaceID + "\x00" + publication.PublicationID
	if existing, found := store.publications[key]; found {
		if existing.CanonicalHash == publication.CanonicalHash &&
			existing.MAC == publication.MAC {
			return nil
		}
		return ErrPublicationExists
	}
	for _, existing := range store.publications {
		if existing.WorkspaceID == publication.WorkspaceID &&
			existing.CanonicalHash == publication.CanonicalHash {
			return ErrPublicationExists
		}
	}
	store.publications[key] = publication
	return nil
}

func (store *memoryPublicationStore) List(
	_ context.Context,
	workspaceID string,
) ([]Publication, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var publications []Publication
	for _, publication := range store.publications {
		if publication.WorkspaceID == workspaceID {
			publications = append(publications, publication)
		}
	}
	return publications, nil
}

func (store *memoryPublicationStore) Close() error { return nil }

type sqlitePublicationStore struct {
	db *sql.DB
}

func openSQLitePublicationStore(
	path string,
) (*sqlitePublicationStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("replica.publication_path_required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		PRAGMA busy_timeout=5000;
		CREATE TABLE IF NOT EXISTS advisory_publications (
			workspace_id TEXT NOT NULL,
			publication_id TEXT NOT NULL,
			canonical_hash TEXT NOT NULL,
			publication_json BLOB NOT NULL,
			PRIMARY KEY(workspace_id, publication_id),
			UNIQUE(workspace_id, canonical_hash)
		);
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &sqlitePublicationStore{db: db}, nil
}

func (store *sqlitePublicationStore) Create(
	ctx context.Context,
	publication Publication,
) error {
	raw, err := json.Marshal(publication)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO advisory_publications (
			workspace_id, publication_id, canonical_hash, publication_json
		) VALUES (?, ?, ?, ?)`,
		publication.WorkspaceID,
		publication.PublicationID,
		publication.CanonicalHash,
		raw,
	)
	if err == nil {
		return nil
	}
	var existingRaw []byte
	queryErr := store.db.QueryRowContext(ctx, `
		SELECT publication_json FROM advisory_publications
		WHERE workspace_id = ? AND publication_id = ?`,
		publication.WorkspaceID,
		publication.PublicationID,
	).Scan(&existingRaw)
	if queryErr == nil && bytes.Equal(existingRaw, raw) {
		return nil
	}
	return errors.Join(ErrPublicationExists, err)
}

func (store *sqlitePublicationStore) List(
	ctx context.Context,
	workspaceID string,
) ([]Publication, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT publication_json FROM advisory_publications
		WHERE workspace_id = ? ORDER BY publication_id`,
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var publications []Publication
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var publication Publication
		if err := json.Unmarshal(raw, &publication); err != nil {
			return nil, errors.Join(ErrPublicationTampered, err)
		}
		publications = append(publications, publication)
	}
	return publications, rows.Err()
}

func (store *sqlitePublicationStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	_, checkpointErr := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	closeErr := store.db.Close()
	store.db = nil
	return errors.Join(checkpointErr, closeErr)
}
