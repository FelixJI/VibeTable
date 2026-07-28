package objectrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	kopiarepo "github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/blob/filesystem"
	kopiacontent "github.com/kopia/kopia/repo/content"
	kopiaformat "github.com/kopia/kopia/repo/format"
	kopiamanifest "github.com/kopia/kopia/repo/manifest"
	kopiaobject "github.com/kopia/kopia/repo/object"
)

const kopiaStateManifestType = "vibetable-workspace-state-v2"

type kopiaState struct {
	FormatVersion int               `json:"formatVersion"`
	Authority     *Authority        `json:"authority"`
	Revision      uint64            `json:"revision"`
	Objects       map[string]string `json:"objects"`
	Manifests     map[string]string `json:"manifests"`
	Pins          []RootPin         `json:"pins"`
}

type KopiaRepository struct {
	mu         sync.Mutex
	repository kopiarepo.Repository
	lockPath   string
	state      kopiaState
	now        func() time.Time
}

func CreateKopiaFilesystem(
	ctx context.Context,
	storageRoot string,
	configFile string,
	password string,
) (*KopiaRepository, error) {
	if password == "" {
		return nil, ErrKeyMissing
	}
	if err := os.MkdirAll(storageRoot, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(configFile), 0o700); err != nil {
		return nil, err
	}
	storage, err := filesystem.New(ctx, &filesystem.Options{Path: storageRoot}, true)
	if err != nil {
		return nil, err
	}
	defer storage.Close(ctx)
	if err := kopiarepo.Initialize(ctx, storage, &kopiarepo.NewRepositoryOptions{
		BlockFormat: kopiaformat.ContentFormat{
			EnablePasswordChange: true,
		},
	}, password); err != nil {
		return nil, err
	}
	if err := kopiarepo.Connect(ctx, configFile, storage, password, &kopiarepo.ConnectOptions{}); err != nil {
		return nil, err
	}
	return OpenKopia(ctx, configFile, password)
}

func OpenKopia(ctx context.Context, configFile string, password string) (*KopiaRepository, error) {
	normalizedConfig, err := filepath.Abs(configFile)
	if err != nil {
		return nil, err
	}
	repository, err := kopiarepo.Open(ctx, configFile, password, nil)
	if err != nil {
		return nil, errors.Join(ErrKeyMissing, err)
	}
	result := &KopiaRepository{
		repository: repository,
		lockPath:   normalizedConfig + ".vibetable.lock",
		state: kopiaState{
			FormatVersion: 2,
			Objects:       map[string]string{},
			Manifests:     map[string]string{},
			Pins:          []RootPin{},
		},
		now: func() time.Time { return time.Now().UTC() },
	}
	if err := result.loadState(ctx); err != nil {
		_ = repository.Close(ctx)
		return nil, err
	}
	return result, nil
}

func (repository *KopiaRepository) Close(ctx context.Context) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.repository == nil {
		return nil
	}
	err := repository.repository.Close(ctx)
	repository.repository = nil
	return err
}

func (repository *KopiaRepository) AcceptAuthority(
	ctx context.Context,
	expected *Authority,
	next Authority,
) error {
	if err := validateAuthority(next); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	release, err := acquireProcessLock(ctx, repository.lockPath)
	if err != nil {
		return err
	}
	defer release()
	if err := repository.loadState(ctx); err != nil {
		return err
	}
	current := repository.state.Authority
	if expected == nil {
		if current != nil {
			return ErrStaleAuthority
		}
	} else if current == nil || !authorityEqual(*current, *expected) {
		return ErrStaleAuthority
	}
	if current != nil && (next.WorkspaceID != current.WorkspaceID ||
		next.FenceEpoch < current.FenceEpoch ||
		(next.FenceEpoch == current.FenceEpoch && next.ClaimID != current.ClaimID)) {
		return ErrStaleAuthority
	}
	nextState := cloneKopiaState(repository.state)
	copy := next
	nextState.Authority = &copy
	if err := repository.publishState(ctx, nextState, "accept authority"); err != nil {
		return err
	}
	repository.state = nextState
	return nil
}

func (repository *KopiaRepository) Commit(
	ctx context.Context,
	request CommitRequest,
) (DurableCommitReceipt, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	release, err := acquireProcessLock(ctx, repository.lockPath)
	if err != nil {
		return DurableCommitReceipt{}, err
	}
	defer release()
	if err := repository.loadState(ctx); err != nil {
		return DurableCommitReceipt{}, err
	}
	if err := repository.requireAuthority(request.Authority); err != nil {
		return DurableCommitReceipt{}, err
	}
	next := cloneKopiaState(repository.state)
	receipt := DurableCommitReceipt{
		WorkspaceID: request.Authority.WorkspaceID,
		FenceEpoch:  request.Authority.FenceEpoch,
		ClaimID:     request.Authority.ClaimID,
		Objects:     map[string]ObjectID{},
		Manifests:   map[string]ManifestID{},
		Revision:    next.Revision + 1,
		FlushedAt:   repository.now(),
		Durable:     true,
	}
	sessionCtx, writer, err := repository.repository.NewWriter(ctx, kopiarepo.WriteSessionOptions{
		Purpose: "VibeTable repository commit",
	})
	if err != nil {
		return DurableCommitReceipt{}, err
	}
	for _, input := range request.Objects {
		if input.Name == "" {
			return DurableCommitReceipt{}, errors.New("repository.object_name_invalid")
		}
		publicID := objectID(input.Content)
		if _, exists := next.Objects[string(publicID)]; !exists {
			objectWriter := writer.NewObjectWriter(sessionCtx, kopiaobject.WriterOptions{
				Description: "VibeTable " + input.Name,
			})
			if _, err := objectWriter.Write(input.Content); err != nil {
				_ = objectWriter.Close()
				return DurableCommitReceipt{}, err
			}
			internalID, err := objectWriter.Result()
			closeErr := objectWriter.Close()
			if err != nil || closeErr != nil {
				return DurableCommitReceipt{}, errors.Join(err, closeErr)
			}
			next.Objects[string(publicID)] = internalID.String()
		}
		receipt.Objects[input.Name] = publicID
	}
	for _, input := range request.Manifests {
		canonical, err := canonicalManifest(input)
		if err != nil {
			return DurableCommitReceipt{}, err
		}
		publicID := manifestID(canonical)
		if _, exists := next.Manifests[string(publicID)]; !exists {
			labels := cloneLabels(input.Labels)
			labels["type"] = "vibetable-manifest"
			labels["vibetable.publicId"] = string(publicID)
			internalID, err := writer.PutManifest(sessionCtx, labels, ManifestRecord{
				ID: publicID, Name: input.Name, Labels: cloneLabels(input.Labels),
				Payload: append(json.RawMessage(nil), input.Payload...),
			})
			if err != nil {
				return DurableCommitReceipt{}, err
			}
			next.Manifests[string(publicID)] = string(internalID)
		}
		receipt.Manifests[input.Name] = publicID
	}
	next.Revision = receipt.Revision
	if err := putKopiaState(sessionCtx, writer, next); err != nil {
		return DurableCommitReceipt{}, err
	}
	if err := writer.Flush(sessionCtx); err != nil {
		return DurableCommitReceipt{}, err
	}
	repository.state = next
	return receipt, nil
}

func (repository *KopiaRepository) Open(
	ctx context.Context,
	id ObjectID,
) (io.ReadCloser, error) {
	repository.mu.Lock()
	internal, ok := repository.state.Objects[string(id)]
	kopia := repository.repository
	repository.mu.Unlock()
	if !ok || kopia == nil {
		return nil, ErrNotFound
	}
	internalID, err := kopiaobject.ParseID(internal)
	if err != nil {
		return nil, ErrCorrupt
	}
	reader, err := kopia.OpenObject(ctx, internalID)
	if err != nil {
		return nil, errors.Join(ErrCorrupt, err)
	}
	return reader, nil
}

func (repository *KopiaRepository) GetManifest(
	ctx context.Context,
	id ManifestID,
) (ManifestRecord, error) {
	repository.mu.Lock()
	internal, ok := repository.state.Manifests[string(id)]
	kopia := repository.repository
	repository.mu.Unlock()
	if !ok || kopia == nil {
		return ManifestRecord{}, ErrNotFound
	}
	var record ManifestRecord
	if _, err := kopia.GetManifest(ctx, kopiamanifest.ID(internal), &record); err != nil {
		return ManifestRecord{}, errors.Join(ErrCorrupt, err)
	}
	canonical, err := canonicalManifest(ManifestInput{
		Name: record.Name, Labels: record.Labels, Payload: record.Payload,
	})
	if err != nil || record.ID != id || manifestID(canonical) != id {
		return ManifestRecord{}, ErrCorrupt
	}
	return record, nil
}

func (repository *KopiaRepository) Verify(
	ctx context.Context,
	roots []ObjectID,
) (VerificationReport, error) {
	report := VerificationReport{Valid: true}
	repository.mu.Lock()
	mappings := make(map[ObjectID]string, len(roots))
	for _, id := range roots {
		if internal, ok := repository.state.Objects[string(id)]; ok {
			mappings[id] = internal
		}
	}
	kopia := repository.repository
	repository.mu.Unlock()
	for _, id := range roots {
		report.Checked = append(report.Checked, id)
		internal, ok := mappings[id]
		if !ok {
			report.Missing = append(report.Missing, id)
			report.Valid = false
			continue
		}
		internalID, err := kopiaobject.ParseID(internal)
		if err != nil {
			report.Corrupt = append(report.Corrupt, id)
			report.Valid = false
			continue
		}
		if _, err := kopia.VerifyObject(ctx, internalID); err != nil {
			report.Corrupt = append(report.Corrupt, id)
			report.Valid = false
		}
	}
	return report, nil
}

func (repository *KopiaRepository) StorageInventory(
	ctx context.Context,
	roots []ObjectID,
) (StorageInventory, error) {
	repository.mu.Lock()
	mappings := make(map[ObjectID]string, len(roots))
	for _, id := range roots {
		if internal, ok := repository.state.Objects[string(id)]; ok {
			mappings[id] = internal
		}
	}
	kopia := repository.repository
	revision := repository.state.Revision
	repository.mu.Unlock()
	if kopia == nil {
		return StorageInventory{}, errors.New("repository.closed")
	}
	seenObjects := make(map[ObjectID]struct{}, len(roots))
	seenContents := map[kopiacontent.ID]struct{}{}
	result := StorageInventory{
		RepositoryRevision:     revision,
		PhysicalBytesEstimated: true,
	}
	for _, id := range roots {
		if _, exists := seenObjects[id]; exists {
			continue
		}
		internal, exists := mappings[id]
		if !exists {
			return StorageInventory{}, ErrNotFound
		}
		internalID, err := kopiaobject.ParseID(internal)
		if err != nil {
			return StorageInventory{}, ErrCorrupt
		}
		reader, err := kopia.OpenObject(ctx, internalID)
		if err != nil {
			return StorageInventory{}, errors.Join(ErrCorrupt, err)
		}
		length := reader.Length()
		closeErr := reader.Close()
		if length < 0 || closeErr != nil ||
			uint64(length) > ^uint64(0)-result.LogicalBytes {
			return StorageInventory{}, errors.Join(
				errors.New("repository.inventory_size_overflow"),
				closeErr,
			)
		}
		result.LogicalBytes += uint64(length)
		contents, err := kopia.VerifyObject(ctx, internalID)
		if err != nil {
			return StorageInventory{}, errors.Join(ErrCorrupt, err)
		}
		for _, contentID := range contents {
			seenContents[contentID] = struct{}{}
		}
		seenObjects[id] = struct{}{}
	}
	for contentID := range seenContents {
		info, err := kopia.ContentInfo(ctx, contentID)
		if err != nil {
			return StorageInventory{}, errors.Join(ErrCorrupt, err)
		}
		size := uint64(info.PackedLength)
		if size > ^uint64(0)-result.PhysicalBytes {
			return StorageInventory{}, errors.New(
				"repository.inventory_size_overflow",
			)
		}
		result.PhysicalBytes += size
	}
	result.ObjectCount = uint64(len(seenObjects))
	result.UniqueContentCount = uint64(len(seenContents))
	return result, nil
}

func (repository *KopiaRepository) Pin(
	ctx context.Context,
	authority Authority,
	roots []ObjectID,
	purpose string,
	expiry *time.Time,
) (RootPin, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	release, err := acquireProcessLock(ctx, repository.lockPath)
	if err != nil {
		return RootPin{}, err
	}
	defer release()
	if err := repository.loadState(ctx); err != nil {
		return RootPin{}, err
	}
	if err := repository.requireAuthority(authority); err != nil || purpose == "" {
		if err != nil {
			return RootPin{}, err
		}
		return RootPin{}, errors.New("repository.pin_purpose_invalid")
	}
	for _, root := range roots {
		if _, ok := repository.state.Objects[string(root)]; !ok {
			return RootPin{}, ErrNotFound
		}
	}
	next := cloneKopiaState(repository.state)
	pin := RootPin{
		PinID: newPinID(), WorkspaceID: authority.WorkspaceID,
		Roots: append([]ObjectID(nil), roots...), Purpose: purpose,
		ExpiresAt: expiry, CreatedAt: repository.now(),
	}
	next.Pins = append(next.Pins, pin)
	if err := repository.publishState(ctx, next, "pin roots"); err != nil {
		return RootPin{}, err
	}
	repository.state = next
	return pin, nil
}

func (repository *KopiaRepository) ReleasePin(
	ctx context.Context,
	authority Authority,
	pinID string,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	release, err := acquireProcessLock(ctx, repository.lockPath)
	if err != nil {
		return err
	}
	defer release()
	if err := repository.loadState(ctx); err != nil {
		return err
	}
	if err := repository.requireAuthority(authority); err != nil {
		return err
	}
	next := cloneKopiaState(repository.state)
	filtered := next.Pins[:0]
	for _, pin := range next.Pins {
		if pin.PinID != pinID {
			filtered = append(filtered, pin)
		}
	}
	next.Pins = filtered
	if err := repository.publishState(ctx, next, "release pin"); err != nil {
		return err
	}
	repository.state = next
	return nil
}

func (repository *KopiaRepository) ListPins(context.Context) ([]RootPin, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	result := append([]RootPin(nil), repository.state.Pins...)
	sort.Slice(result, func(i, j int) bool { return result[i].PinID < result[j].PinID })
	return result, nil
}

func (repository *KopiaRepository) requireAuthority(authority Authority) error {
	if repository.repository == nil || repository.state.Authority == nil ||
		!authorityEqual(*repository.state.Authority, authority) {
		return ErrStaleAuthority
	}
	return nil
}

func (repository *KopiaRepository) publishState(
	ctx context.Context,
	state kopiaState,
	purpose string,
) error {
	sessionCtx, writer, err := repository.repository.NewWriter(ctx, kopiarepo.WriteSessionOptions{
		Purpose: "VibeTable " + purpose,
	})
	if err != nil {
		return err
	}
	if err := putKopiaState(sessionCtx, writer, state); err != nil {
		return err
	}
	return writer.Flush(sessionCtx)
}

func putKopiaState(
	ctx context.Context,
	writer kopiarepo.RepositoryWriter,
	state kopiaState,
) error {
	_, err := writer.ReplaceManifests(ctx, map[string]string{
		"type": kopiaStateManifestType,
	}, state)
	return err
}

func (repository *KopiaRepository) loadState(ctx context.Context) error {
	if err := repository.repository.Refresh(ctx); err != nil {
		return err
	}
	entries, err := repository.repository.FindManifests(ctx, map[string]string{
		"type": kopiaStateManifestType,
	})
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		if repository.state.Authority != nil || repository.state.Revision != 0 {
			return errors.Join(
				ErrCorrupt,
				errors.New("repository.kopia_state_disappeared"),
			)
		}
		return nil
	}
	if len(entries) != 1 {
		return errors.Join(ErrCorrupt, errors.New("repository.kopia_state_count_invalid"))
	}
	var state kopiaState
	if _, err := repository.repository.GetManifest(ctx, entries[0].ID, &state); err != nil {
		return errors.Join(ErrCorrupt, err)
	}
	if state.FormatVersion != 2 || state.Objects == nil || state.Manifests == nil {
		return errors.Join(
			ErrCorrupt,
			fmt.Errorf(
				"repository.kopia_state_shape_invalid: format=%d objectsNil=%v manifestsNil=%v pinsNil=%v",
				state.FormatVersion,
				state.Objects == nil,
				state.Manifests == nil,
				state.Pins == nil,
			),
		)
	}
	if state.Pins == nil {
		state.Pins = []RootPin{}
	}
	repository.state = state
	return nil
}

func cloneKopiaState(source kopiaState) kopiaState {
	result := source
	if source.Authority != nil {
		copy := *source.Authority
		result.Authority = &copy
	}
	result.Objects = make(map[string]string, len(source.Objects))
	for key, value := range source.Objects {
		result.Objects[key] = value
	}
	result.Manifests = make(map[string]string, len(source.Manifests))
	for key, value := range source.Manifests {
		result.Manifests[key] = value
	}
	result.Pins = append([]RootPin(nil), source.Pins...)
	return result
}

func newPinID() string {
	return time.Now().UTC().Format("20060102T150405.000000000Z")
}
