package workspacev2

import (
	"context"
	"errors"

	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
)

// PreserveRecoverySnapshot publishes through the existing local snapshot
// authority. Source sequence/identity remain immutable; a local sequence and
// independent retention pin make the foreign source publicly restorable.
func (runtime *Runtime) PreserveRecoverySnapshot(ctx context.Context, sourceRecord snapshot.Record) error {
	runtime.recoverySnapshotMu.Lock()
	defer runtime.recoverySnapshotMu.Unlock()
	if sourceRecord.WorkspaceID != runtime.manifest.WorkspaceID {
		return errors.New("conflict.recovery_source_invalid")
	}
	bundle, err := snapshot.LoadSnapshotBundle(ctx, runtime.repository, sourceRecord)
	if err != nil {
		return err
	}
	source, err := importedSourceFromBundle(bundle)
	if err != nil {
		return err
	}
	defer source.clearSensitive()
	records, err := runtime.catalog.List(ctx, sourceRecord.WorkspaceID)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.SnapshotID == sourceRecord.SnapshotID && record.Pinned {
			if record.ManifestID != sourceRecord.ManifestID || record.SealID != sourceRecord.SealID {
				return errors.New("conflict.recovery_source_invalid")
			}
			return runtime.verifyRecoverySnapshot(ctx, record)
		}
		if record.LocalRecovery && record.SourceWorkspaceID == sourceRecord.WorkspaceID && record.SourceSnapshotID == sourceRecord.SnapshotID {
			if record.RecoverySourceManifestID != string(sourceRecord.ManifestID) || !record.Pinned {
				return errors.New("conflict.recovery_source_invalid")
			}
			return runtime.verifyRecoverySnapshot(ctx, record)
		}
	}
	source.repository = runtime.repository
	token, _ := runtime.coordinator.Current()
	barrier, err := snapshot.NewCoordinatedBarrier(runtime.coordinator, token, &source)
	if err != nil {
		return err
	}
	publisher := snapshot.NewCoordinator(runtime.repository, barrier, runtime.catalog)
	record, created, err := publisher.Capture(ctx, snapshot.CaptureRequest{
		WorkspaceID: runtime.manifest.WorkspaceID, Authority: token.Authority(), Trigger: snapshot.TriggerProtection,
		Pinned: true, LocalRecovery: true, RecoverySourceManifestID: string(sourceRecord.ManifestID),
	})
	if err != nil {
		return err
	}
	if !created || record.SnapshotID == "" {
		return errors.New("conflict.recovery_publication_failed")
	}
	return nil
}

// Resolve only persisted, independently verified public recovery identities.
// In particular, never return an unregistered remote source ID in an apply receipt.
func (runtime *Runtime) conflictRecoveryIDs(ctx context.Context, sourceIDs ...string) ([]string, error) {
	records, err := runtime.catalog.List(ctx, runtime.manifest.WorkspaceID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		var found *snapshot.Record
		for index := range records {
			record := &records[index]
			if record.Pinned && (record.SnapshotID == sourceID || (record.LocalRecovery && record.SourceWorkspaceID == runtime.manifest.WorkspaceID && record.SourceSnapshotID == sourceID)) {
				found = record
				break
			}
		}
		if found == nil {
			return nil, errors.New("conflict.recovery_snapshot_unpublished")
		}
		if err := runtime.verifyRecoverySnapshot(ctx, *found); err != nil {
			return nil, err
		}
		ids = append(ids, found.SnapshotID)
	}
	return uniqueStrings(ids), nil
}

func (runtime *Runtime) verifyRecoverySnapshot(ctx context.Context, record snapshot.Record) error {
	current, err := runtime.snapshotRecord(ctx, record.SnapshotID)
	if err != nil {
		return err
	}
	record = current
	if !record.Pinned {
		return errors.New("conflict.recovery_snapshot_unprotected")
	}
	if _, err := snapshot.LoadSnapshotBundle(ctx, runtime.repository, record); err != nil {
		return err
	}
	pins, err := runtime.repository.ListPins(ctx)
	if err != nil {
		return err
	}
	for _, pin := range pins {
		if pin.PinID == record.RootPinID && pin.WorkspaceID == record.WorkspaceID && pin.ExpiresAt == nil {
			return nil
		}
	}
	return errors.New("conflict.recovery_snapshot_unprotected")
}
