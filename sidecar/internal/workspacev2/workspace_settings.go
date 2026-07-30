package workspacev2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

const workspaceSettingsFormatVersion = 1

// workspaceSettingsSnapshot is the versioned, closed snapshot projection of
// settings whose authority belongs to one workspace but lives outside the
// recoverable business database. Device/global UI state, credentials, lease
// state and repository health are deliberately not part of this object.
type workspaceSettingsSnapshot struct {
	FormatVersion int                        `json:"formatVersion"`
	Retention     workspaceRetentionSettings `json:"retention"`
}

type workspaceRetentionSettings struct {
	SnapshotDays         uint64   `json:"snapshotDays"`
	SnapshotCount        uint64   `json:"snapshotCount"`
	SnapshotBuckets      []string `json:"snapshotBuckets"`
	FileRevisionDays     uint64   `json:"fileRevisionDays"`
	FileRevisionCount    uint64   `json:"fileRevisionCount"`
	FileRevisionBuckets  []string `json:"fileRevisionBuckets"`
	RepositoryLimitBytes *uint64  `json:"repositoryLimitBytes"`
}

func snapshotWorkspaceSettings(
	ctx context.Context,
	store *stateStore,
) ([]byte, error) {
	if store == nil {
		return nil, errors.New("workspace.settings_authority_unavailable")
	}
	policy, _, err := store.retention(ctx)
	if err != nil {
		return nil, err
	}
	return json.Marshal(workspaceSettingsSnapshot{
		FormatVersion: workspaceSettingsFormatVersion,
		Retention:     retentionSnapshot(policy),
	})
}

func retentionSnapshot(policy RetentionPolicy) workspaceRetentionSettings {
	return workspaceRetentionSettings{
		SnapshotDays:         policy.SnapshotDays,
		SnapshotCount:        policy.SnapshotCount,
		SnapshotBuckets:      append([]string(nil), policy.SnapshotBuckets...),
		FileRevisionDays:     policy.FileRevisionDays,
		FileRevisionCount:    policy.FileRevisionCount,
		FileRevisionBuckets:  append([]string(nil), policy.FileRevisionBuckets...),
		RepositoryLimitBytes: cloneUint64(policy.RepositoryLimitBytes),
	}
}

func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// decodeWorkspaceSettingsSnapshot accepts the empty object emitted by
// pre-v1 snapshots. It means "this snapshot predates external workspace
// settings" and therefore restores no setting. Every versioned object is
// otherwise decoded as a strict closed object.
func decodeWorkspaceSettingsSnapshot(
	raw []byte,
) (workspaceSettingsSnapshot, bool, error) {
	var legacy map[string]json.RawMessage
	if err := decodeWorkspaceSettingsStrict(raw, &legacy); err != nil {
		return workspaceSettingsSnapshot{}, false,
			errors.New("workspace.settings_invalid")
	}
	if legacy == nil {
		return workspaceSettingsSnapshot{}, false,
			errors.New("workspace.settings_invalid")
	}
	if _, versioned := legacy["formatVersion"]; !versioned {
		return workspaceSettingsSnapshot{}, true, nil
	}
	var value workspaceSettingsSnapshot
	if err := decodeWorkspaceSettingsStrict(raw, &value); err != nil ||
		value.FormatVersion != workspaceSettingsFormatVersion ||
		!validWorkspaceRetentionSettings(value.Retention) {
		return workspaceSettingsSnapshot{}, false,
			errors.New("workspace.settings_invalid")
	}
	return value, false, nil
}

func decodeWorkspaceSettingsStrict(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("workspace.settings_trailing_json")
	}
	return nil
}

func validWorkspaceRetentionSettings(value workspaceRetentionSettings) bool {
	return value.SnapshotDays != 0 &&
		value.SnapshotCount != 0 &&
		value.FileRevisionDays != 0 &&
		value.FileRevisionCount != 0 &&
		validRetentionBuckets(value.SnapshotBuckets) &&
		validRetentionBuckets(value.FileRevisionBuckets) &&
		(value.RepositoryLimitBytes == nil ||
			*value.RepositoryLimitBytes != 0)
}

func replaceWorkspaceSettings(
	ctx context.Context,
	store *stateStore,
	raw []byte,
	mutationRevision uint64,
) error {
	target, legacy, err := decodeWorkspaceSettingsSnapshot(raw)
	if err != nil || legacy {
		return err
	}
	current, currentMutationRevision, err := store.retention(ctx)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(retentionSnapshot(current), target.Retention) {
		return nil
	}
	next := current
	next.PolicyRevision++
	next.SnapshotDays = target.Retention.SnapshotDays
	next.SnapshotCount = target.Retention.SnapshotCount
	next.SnapshotBuckets = append(
		[]string(nil), target.Retention.SnapshotBuckets...,
	)
	next.FileRevisionDays = target.Retention.FileRevisionDays
	next.FileRevisionCount = target.Retention.FileRevisionCount
	next.FileRevisionBuckets = append(
		[]string(nil), target.Retention.FileRevisionBuckets...,
	)
	next.RepositoryLimitBytes = cloneUint64(
		target.Retention.RepositoryLimitBytes,
	)
	if mutationRevision < currentMutationRevision {
		mutationRevision = currentMutationRevision
	}
	return store.updateRetention(
		ctx,
		current.PolicyRevision,
		next,
		mutationRevision,
	)
}

func workspaceSettingsDiffer(currentRaw []byte, targetRaw []byte) (bool, error) {
	current, currentLegacy, err := decodeWorkspaceSettingsSnapshot(currentRaw)
	if err != nil {
		return false, err
	}
	target, targetLegacy, err := decodeWorkspaceSettingsSnapshot(targetRaw)
	if err != nil {
		return false, err
	}
	// A legacy empty object carried no external setting and must not reset a
	// newer workspace to defaults.
	if targetLegacy {
		return false, nil
	}
	if currentLegacy {
		return true, nil
	}
	return !reflect.DeepEqual(current.Retention, target.Retention), nil
}

func (runtime *Runtime) readWorkspaceSettingsObject(
	ctx context.Context,
	id objectrepo.ObjectID,
) ([]byte, error) {
	if runtime == nil || runtime.repository == nil || id == "" {
		return nil, errors.New("workspace.settings_object_invalid")
	}
	reader, err := runtime.repository.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, errors.New("workspace.settings_resource_limit")
	}
	if _, _, err := decodeWorkspaceSettingsSnapshot(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func stagePreviousWorkspaceSettings(
	ctx context.Context,
	paths workspacePaths,
	rollbackRoot string,
) error {
	store, err := openStateStore(
		filepath.Join(paths.coordination, "workspace-v2.db"),
	)
	if err != nil {
		return err
	}
	raw, snapshotErr := snapshotWorkspaceSettings(ctx, store)
	closeErr := store.db.Close()
	if err := errors.Join(snapshotErr, closeErr); err != nil {
		return err
	}
	return atomicReplaceConflictFile(
		filepath.Join(rollbackRoot, "settings.json"),
		raw,
	)
}

func replaceWorkspaceSettingsAtPath(
	ctx context.Context,
	paths workspacePaths,
	raw []byte,
	mutationRevision uint64,
) error {
	store, err := openStateStore(
		filepath.Join(paths.coordination, "workspace-v2.db"),
	)
	if err != nil {
		return err
	}
	replaceErr := replaceWorkspaceSettings(
		ctx,
		store,
		raw,
		mutationRevision,
	)
	return errors.Join(replaceErr, store.db.Close())
}
