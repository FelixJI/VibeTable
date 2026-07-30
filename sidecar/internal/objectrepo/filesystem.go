package objectrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type FilesystemRepository struct {
	mu   sync.Mutex
	root string
	now  func() time.Time
}

func OpenFilesystem(root string) (*FilesystemRepository, error) {
	normalized, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, directory := range []string{
		filepath.Join(normalized, "objects"),
		filepath.Join(normalized, "manifests"),
		filepath.Join(normalized, "coordination"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}
	return &FilesystemRepository{
		root: normalized,
		now:  func() time.Time { return time.Now().UTC() },
	}, nil
}

func (repository *FilesystemRepository) AcceptAuthority(
	ctx context.Context,
	expected *Authority,
	next Authority,
) error {
	if err := validateAuthority(next); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	release, err := acquireProcessLock(ctx, repository.lockPath())
	if err != nil {
		return err
	}
	defer release()
	current, found, err := repository.readAuthority()
	if err != nil {
		return err
	}
	if expected == nil {
		if found {
			return ErrStaleAuthority
		}
	} else if !found || !authorityEqual(current, *expected) {
		return ErrStaleAuthority
	}
	if found && (next.WorkspaceID != current.WorkspaceID ||
		next.FenceEpoch < current.FenceEpoch ||
		(next.FenceEpoch == current.FenceEpoch && next.ClaimID != current.ClaimID)) {
		return ErrStaleAuthority
	}
	return writeJSONAtomic(repository.authorityPath(), next)
}

func (repository *FilesystemRepository) Commit(
	ctx context.Context,
	request CommitRequest,
) (DurableCommitReceipt, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	release, err := acquireProcessLock(ctx, repository.lockPath())
	if err != nil {
		return DurableCommitReceipt{}, err
	}
	defer release()
	if err := repository.requireAuthority(request.Authority); err != nil {
		return DurableCommitReceipt{}, err
	}
	revision, err := repository.readRevision()
	if err != nil {
		return DurableCommitReceipt{}, err
	}
	receipt := DurableCommitReceipt{
		WorkspaceID: request.Authority.WorkspaceID,
		FenceEpoch:  request.Authority.FenceEpoch,
		ClaimID:     request.Authority.ClaimID,
		Objects:     map[string]ObjectID{},
		Manifests:   map[string]ManifestID{},
		Revision:    revision + 1,
		FlushedAt:   repository.now(),
		Durable:     true,
	}
	for _, input := range request.Objects {
		if input.Name == "" {
			return DurableCommitReceipt{}, errors.New("repository.object_name_invalid")
		}
		id := objectID(input.Content)
		if err := writeImmutable(
			repository.objectPath(id),
			input.Content,
		); err != nil {
			return DurableCommitReceipt{}, err
		}
		receipt.Objects[input.Name] = id
	}
	for _, input := range request.Manifests {
		raw, err := canonicalManifest(input)
		if err != nil {
			return DurableCommitReceipt{}, err
		}
		id := manifestID(raw)
		record, err := json.Marshal(ManifestRecord{
			ID: id, Name: input.Name, Labels: input.Labels, Payload: input.Payload,
		})
		if err != nil {
			return DurableCommitReceipt{}, err
		}
		if err := writeImmutable(repository.manifestPath(id), record); err != nil {
			return DurableCommitReceipt{}, err
		}
		receipt.Manifests[input.Name] = id
	}
	if err := writeJSONAtomic(repository.revisionPath(), struct {
		Revision uint64 `json:"revision"`
	}{receipt.Revision}); err != nil {
		return DurableCommitReceipt{}, err
	}
	return receipt, nil
}

func (repository *FilesystemRepository) Open(
	_ context.Context,
	id ObjectID,
) (io.ReadCloser, error) {
	if !validateObjectID(id) {
		return nil, ErrNotFound
	}
	raw, err := os.ReadFile(repository.objectPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if objectID(raw) != id {
		return nil, ErrCorrupt
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (repository *FilesystemRepository) GetManifest(
	_ context.Context,
	id ManifestID,
) (ManifestRecord, error) {
	raw, err := os.ReadFile(repository.manifestPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return ManifestRecord{}, ErrNotFound
	}
	if err != nil {
		return ManifestRecord{}, err
	}
	var record ManifestRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || record.ID != id {
		return ManifestRecord{}, ErrCorrupt
	}
	canonical, err := canonicalManifest(ManifestInput{
		Name: record.Name, Labels: record.Labels, Payload: record.Payload,
	})
	if err != nil || manifestID(canonical) != id {
		return ManifestRecord{}, ErrCorrupt
	}
	return record, nil
}

func (repository *FilesystemRepository) Verify(
	ctx context.Context,
	roots []ObjectID,
) (VerificationReport, error) {
	report := VerificationReport{Valid: true}
	for _, id := range roots {
		report.Checked = append(report.Checked, id)
		stream, err := repository.Open(ctx, id)
		if errors.Is(err, ErrNotFound) {
			report.Missing = append(report.Missing, id)
			report.Valid = false
			continue
		}
		if errors.Is(err, ErrCorrupt) {
			report.Corrupt = append(report.Corrupt, id)
			report.Valid = false
			continue
		}
		if err != nil {
			return VerificationReport{}, err
		}
		_ = stream.Close()
	}
	return report, nil
}

func (repository *FilesystemRepository) Pin(
	ctx context.Context,
	authority Authority,
	roots []ObjectID,
	purpose string,
	expiry *time.Time,
) (RootPin, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	release, err := acquireProcessLock(ctx, repository.lockPath())
	if err != nil {
		return RootPin{}, err
	}
	defer release()
	if err := repository.requireAuthority(authority); err != nil {
		return RootPin{}, err
	}
	if strings.TrimSpace(purpose) == "" {
		return RootPin{}, errors.New("repository.pin_purpose_invalid")
	}
	for _, root := range roots {
		if _, err := os.Stat(repository.objectPath(root)); err != nil {
			return RootPin{}, ErrNotFound
		}
	}
	pins, err := repository.readPins()
	if err != nil {
		return RootPin{}, err
	}
	pin := RootPin{
		PinID:       uuid.NewString(),
		WorkspaceID: authority.WorkspaceID,
		Roots:       append([]ObjectID(nil), roots...),
		Purpose:     purpose,
		ExpiresAt:   expiry,
		CreatedAt:   repository.now(),
	}
	pins = append(pins, pin)
	if err := writeJSONAtomic(repository.pinsPath(), pins); err != nil {
		return RootPin{}, err
	}
	return pin, nil
}

func (repository *FilesystemRepository) ReleasePin(
	ctx context.Context,
	authority Authority,
	pinID string,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	release, err := acquireProcessLock(ctx, repository.lockPath())
	if err != nil {
		return err
	}
	defer release()
	if err := repository.requireAuthority(authority); err != nil {
		return err
	}
	pins, err := repository.readPins()
	if err != nil {
		return err
	}
	filtered := pins[:0]
	for _, pin := range pins {
		if pin.PinID != pinID {
			filtered = append(filtered, pin)
		}
	}
	return writeJSONAtomic(repository.pinsPath(), filtered)
}

func (repository *FilesystemRepository) ListPins(
	_ context.Context,
) ([]RootPin, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.readPins()
}

func (repository *FilesystemRepository) requireAuthority(authority Authority) error {
	if err := validateAuthority(authority); err != nil {
		return err
	}
	current, found, err := repository.readAuthority()
	if err != nil {
		return err
	}
	if !found || !authorityEqual(current, authority) {
		return ErrStaleAuthority
	}
	return nil
}

func (repository *FilesystemRepository) readAuthority() (Authority, bool, error) {
	var authority Authority
	found, err := readJSON(repository.authorityPath(), &authority)
	return authority, found, err
}

func (repository *FilesystemRepository) readRevision() (uint64, error) {
	var value struct {
		Revision uint64 `json:"revision"`
	}
	found, err := readJSON(repository.revisionPath(), &value)
	if err != nil || !found {
		return 0, err
	}
	return value.Revision, nil
}

func (repository *FilesystemRepository) readPins() ([]RootPin, error) {
	var pins []RootPin
	found, err := readJSON(repository.pinsPath(), &pins)
	if err != nil {
		return nil, err
	}
	if !found {
		return []RootPin{}, nil
	}
	sort.Slice(pins, func(left, right int) bool {
		return pins[left].PinID < pins[right].PinID
	})
	return pins, nil
}

func (repository *FilesystemRepository) objectPath(id ObjectID) string {
	return filepath.Join(repository.root, "objects", string(id))
}

func (repository *FilesystemRepository) manifestPath(id ManifestID) string {
	return filepath.Join(repository.root, "manifests", string(id)+".json")
}

func (repository *FilesystemRepository) authorityPath() string {
	return filepath.Join(repository.root, "coordination", "authority.json")
}

func (repository *FilesystemRepository) revisionPath() string {
	return filepath.Join(repository.root, "coordination", "revision.json")
}

func (repository *FilesystemRepository) pinsPath() string {
	return filepath.Join(repository.root, "coordination", "pins.json")
}

func (repository *FilesystemRepository) lockPath() string {
	return filepath.Join(repository.root, "coordination", "repository.lock")
}

func writeImmutable(path string, raw []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, raw) {
			return nil
		}
		return ErrCorrupt
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeFileAtomic(path, raw)
}

func writeJSONAtomic(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw)
}

func writeFileAtomic(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".commit-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceFileDurable(name, path)
}

func readJSON(path string, target any) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false, ErrCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return false, ErrCorrupt
	}
	return true, nil
}
