package objectrepo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

type FaultPoint string

const (
	FaultBeforeObjectWrite   FaultPoint = "before-object-write"
	FaultBeforeManifestWrite FaultPoint = "before-manifest-write"
	FaultBeforeFlush         FaultPoint = "before-flush"
	FaultBeforePinWrite      FaultPoint = "before-pin-write"
)

type FaultInjector func(FaultPoint) error

type MemoryRepository struct {
	mu        sync.RWMutex
	authority *Authority
	objects   map[ObjectID][]byte
	manifests map[ManifestID]ManifestRecord
	pins      map[string]RootPin
	revision  uint64
	now       func() time.Time
	fault     FaultInjector
}

func NewMemory() *MemoryRepository {
	return &MemoryRepository{
		objects:   map[ObjectID][]byte{},
		manifests: map[ManifestID]ManifestRecord{},
		pins:      map[string]RootPin{},
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (repository *MemoryRepository) WithFaultInjector(
	injector FaultInjector,
) *MemoryRepository {
	repository.fault = injector
	return repository
}

func (repository *MemoryRepository) WithNow(now func() time.Time) *MemoryRepository {
	if now != nil {
		repository.now = now
	}
	return repository
}

func (repository *MemoryRepository) AcceptAuthority(
	_ context.Context,
	expected *Authority,
	next Authority,
) error {
	if err := validateAuthority(next); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if expected == nil {
		if repository.authority != nil {
			return ErrStaleAuthority
		}
	} else if repository.authority == nil ||
		!authorityEqual(*repository.authority, *expected) {
		return ErrStaleAuthority
	}
	if repository.authority != nil {
		if next.WorkspaceID != repository.authority.WorkspaceID ||
			next.FenceEpoch < repository.authority.FenceEpoch ||
			(next.FenceEpoch == repository.authority.FenceEpoch &&
				next.ClaimID != repository.authority.ClaimID) {
			return ErrStaleAuthority
		}
	}
	copy := next
	repository.authority = &copy
	return nil
}

func (repository *MemoryRepository) Commit(
	_ context.Context,
	request CommitRequest,
) (DurableCommitReceipt, error) {
	if err := validateAuthority(request.Authority); err != nil {
		return DurableCommitReceipt{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.authority == nil ||
		!authorityEqual(*repository.authority, request.Authority) {
		return DurableCommitReceipt{}, ErrStaleAuthority
	}
	objects := cloneObjects(repository.objects)
	manifests := cloneManifests(repository.manifests)
	receipt := DurableCommitReceipt{
		WorkspaceID: request.Authority.WorkspaceID,
		FenceEpoch:  request.Authority.FenceEpoch,
		ClaimID:     request.Authority.ClaimID,
		Objects:     map[string]ObjectID{},
		Manifests:   map[string]ManifestID{},
		Revision:    repository.revision + 1,
		FlushedAt:   repository.now(),
		Durable:     true,
	}
	for _, input := range request.Objects {
		if err := repository.inject(FaultBeforeObjectWrite); err != nil {
			return DurableCommitReceipt{}, err
		}
		if input.Name == "" {
			return DurableCommitReceipt{}, errors.New("repository.object_name_invalid")
		}
		id := objectID(input.Content)
		objects[id] = bytes.Clone(input.Content)
		receipt.Objects[input.Name] = id
	}
	for _, input := range request.Manifests {
		if err := repository.inject(FaultBeforeManifestWrite); err != nil {
			return DurableCommitReceipt{}, err
		}
		raw, err := canonicalManifest(input)
		if err != nil {
			return DurableCommitReceipt{}, err
		}
		id := manifestID(raw)
		manifests[id] = ManifestRecord{
			ID: id, Name: input.Name, Labels: cloneLabels(input.Labels), Payload: bytes.Clone(input.Payload),
		}
		receipt.Manifests[input.Name] = id
	}
	if err := repository.inject(FaultBeforeFlush); err != nil {
		return DurableCommitReceipt{}, err
	}
	repository.objects = objects
	repository.manifests = manifests
	repository.revision = receipt.Revision
	return receipt, nil
}

func (repository *MemoryRepository) Open(
	_ context.Context,
	id ObjectID,
) (io.ReadCloser, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	content, ok := repository.objects[id]
	if !ok {
		return nil, ErrNotFound
	}
	if objectID(content) != id {
		return nil, ErrCorrupt
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(content))), nil
}

func (repository *MemoryRepository) GetManifest(
	_ context.Context,
	id ManifestID,
) (ManifestRecord, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	record, ok := repository.manifests[id]
	if !ok {
		return ManifestRecord{}, ErrNotFound
	}
	raw, err := canonicalManifest(ManifestInput{
		Name: record.Name, Labels: record.Labels, Payload: record.Payload,
	})
	if err != nil || manifestID(raw) != id {
		return ManifestRecord{}, ErrCorrupt
	}
	record.Labels = cloneLabels(record.Labels)
	record.Payload = bytes.Clone(record.Payload)
	return record, nil
}

func (repository *MemoryRepository) Verify(
	_ context.Context,
	roots []ObjectID,
) (VerificationReport, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	report := VerificationReport{Valid: true}
	for _, id := range roots {
		report.Checked = append(report.Checked, id)
		content, ok := repository.objects[id]
		if !ok {
			report.Missing = append(report.Missing, id)
			report.Valid = false
		} else if objectID(content) != id {
			report.Corrupt = append(report.Corrupt, id)
			report.Valid = false
		}
	}
	return report, nil
}

func (repository *MemoryRepository) StorageInventory(
	_ context.Context,
	roots []ObjectID,
) (StorageInventory, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	seen := make(map[ObjectID]struct{}, len(roots))
	result := StorageInventory{RepositoryRevision: repository.revision}
	for _, id := range roots {
		if _, exists := seen[id]; exists {
			continue
		}
		content, exists := repository.objects[id]
		if !exists {
			return StorageInventory{}, ErrNotFound
		}
		if objectID(content) != id {
			return StorageInventory{}, ErrCorrupt
		}
		seen[id] = struct{}{}
		size := uint64(len(content))
		if ^uint64(0)-result.LogicalBytes < size {
			return StorageInventory{}, errors.New(
				"repository.inventory_size_overflow",
			)
		}
		result.LogicalBytes += size
		result.PhysicalBytes += size
	}
	result.ObjectCount = uint64(len(seen))
	result.UniqueContentCount = result.ObjectCount
	return result, nil
}

func (repository *MemoryRepository) Pin(
	_ context.Context,
	authority Authority,
	roots []ObjectID,
	purpose string,
	expiry *time.Time,
) (RootPin, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.authority == nil ||
		!authorityEqual(*repository.authority, authority) {
		return RootPin{}, ErrStaleAuthority
	}
	if purpose == "" {
		return RootPin{}, errors.New("repository.pin_purpose_invalid")
	}
	for _, root := range roots {
		if _, ok := repository.objects[root]; !ok {
			return RootPin{}, ErrNotFound
		}
	}
	if err := repository.inject(FaultBeforePinWrite); err != nil {
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
	repository.pins[pin.PinID] = pin
	return pin, nil
}

func (repository *MemoryRepository) ReleasePin(
	_ context.Context,
	authority Authority,
	pinID string,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.authority == nil ||
		!authorityEqual(*repository.authority, authority) {
		return ErrStaleAuthority
	}
	delete(repository.pins, pinID)
	return nil
}

func (repository *MemoryRepository) ListPins(
	_ context.Context,
) ([]RootPin, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]RootPin, 0, len(repository.pins))
	for _, pin := range repository.pins {
		pin.Roots = append([]ObjectID(nil), pin.Roots...)
		result = append(result, pin)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].PinID < result[right].PinID
	})
	return result, nil
}

func (repository *MemoryRepository) inject(point FaultPoint) error {
	if repository.fault == nil {
		return nil
	}
	return repository.fault(point)
}

func cloneObjects(source map[ObjectID][]byte) map[ObjectID][]byte {
	result := make(map[ObjectID][]byte, len(source))
	for id, content := range source {
		result[id] = bytes.Clone(content)
	}
	return result
}

func cloneManifests(
	source map[ManifestID]ManifestRecord,
) map[ManifestID]ManifestRecord {
	result := make(map[ManifestID]ManifestRecord, len(source))
	for id, record := range source {
		record.Labels = cloneLabels(record.Labels)
		record.Payload = bytes.Clone(record.Payload)
		result[id] = record
	}
	return result
}

func cloneLabels(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
