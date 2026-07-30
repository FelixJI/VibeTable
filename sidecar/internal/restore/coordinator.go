package restore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

var (
	ErrWorkspaceMismatch = errors.New("restore.workspace_mismatch")
	ErrJournalCorrupt    = errors.New("restore.journal_corrupt")
	ErrPlanInvalid       = errors.New("restore.plan_invalid")
)

type Stage string

const (
	StagePrepared                Stage = "prepared"
	StageServicesStopped         Stage = "services-stopped"
	StageDataInstalled           Stage = "data-installed"
	StageTopologyCommitted       Stage = "topology-committed"
	StageAuditEpochCommitted     Stage = "audit-epoch-committed"
	StageVerified                Stage = "verified"
	StageRecoverySnapshotPending Stage = "recovery-snapshot-pending"
	StageCommitted               Stage = "committed"
	StageRollbackServicesStopped Stage = "rollback-services-stopped"
	StageRollbackDataRestored    Stage = "rollback-data-restored"
	StageRollbackVerified        Stage = "rollback-verified"
)

type Plan struct {
	WorkspaceID   string
	SnapshotID    string
	WorkspaceRoot string
	ManifestHash  string
	SealHash      string
	FenceEpoch    uint64
	ClaimID       string
	LedgerAnchor  string
}

type Journal struct {
	FormatVersion int    `json:"formatVersion"`
	WorkspaceID   string `json:"workspaceId"`
	SnapshotID    string `json:"snapshotId"`
	Stage         Stage  `json:"stage"`
	PreviousRoot  string `json:"previousRoot"`
	StagingRoot   string `json:"stagingRoot"`
	ManifestHash  string `json:"manifestHash,omitempty"`
	SealHash      string `json:"sealHash,omitempty"`
	FenceEpoch    uint64 `json:"fenceEpoch,omitempty"`
	ClaimID       string `json:"claimId,omitempty"`
	LedgerAnchor  string `json:"ledgerAnchor,omitempty"`
}

type Runtime interface {
	Protect(context.Context) error
	Stop(context.Context) error
	Start(context.Context) error
	Health(context.Context) error
}

type Installer interface {
	// Stage must inspect the immutable manifest/seal and fully verify every
	// referenced object before returning a staging root.
	Stage(context.Context, string, string) (string, error)
	InstallData(context.Context, string, string) error
	InstallFilesAsRestoreLeaves(context.Context, string, string) error
	CommitAuditEpoch(context.Context, string, string) error
	Rollback(context.Context, string, string) error
}

type SnapshotAfterRestore interface {
	CaptureRecovery(context.Context, string) error
}

type Coordinator struct {
	journalPath string
	runtime     Runtime
	installer   Installer
	after       SnapshotAfterRestore
	persist     func(Journal) error
}

func New(journalPath string, runtime Runtime, installer Installer, after SnapshotAfterRestore) *Coordinator {
	coordinator := &Coordinator{
		journalPath: journalPath, runtime: runtime, installer: installer, after: after,
	}
	coordinator.persist = coordinator.writeJournal
	return coordinator
}

func (coordinator *Coordinator) Restore(
	ctx context.Context,
	workspaceID string,
	snapshotID string,
	workspaceRoot string,
) error {
	return coordinator.Apply(ctx, Plan{
		WorkspaceID: workspaceID, SnapshotID: snapshotID, WorkspaceRoot: workspaceRoot,
	})
}

func (coordinator *Coordinator) Apply(ctx context.Context, plan Plan) error {
	if plan.WorkspaceID == "" || plan.SnapshotID == "" || plan.WorkspaceRoot == "" {
		return ErrPlanInvalid
	}
	if err := coordinator.runtime.Protect(ctx); err != nil {
		return err
	}
	stagingRoot, err := coordinator.installer.Stage(ctx, plan.SnapshotID, plan.WorkspaceRoot)
	if err != nil {
		return err
	}
	journal := Journal{
		FormatVersion: 2, WorkspaceID: plan.WorkspaceID, SnapshotID: plan.SnapshotID,
		Stage: StagePrepared, PreviousRoot: plan.WorkspaceRoot, StagingRoot: stagingRoot,
		ManifestHash: plan.ManifestHash, SealHash: plan.SealHash, FenceEpoch: plan.FenceEpoch,
		ClaimID: plan.ClaimID, LedgerAnchor: plan.LedgerAnchor,
	}
	if err := coordinator.persist(journal); err != nil {
		return err
	}
	if err := coordinator.runtime.Stop(ctx); err != nil {
		return coordinator.rollback(ctx, journal, err)
	}
	if err := coordinator.advance(ctx, &journal, StageServicesStopped); err != nil {
		return err
	}
	if err := coordinator.installer.InstallData(ctx, stagingRoot, plan.WorkspaceRoot); err != nil {
		return coordinator.rollback(ctx, journal, err)
	}
	if err := coordinator.advance(ctx, &journal, StageDataInstalled); err != nil {
		return err
	}
	if err := coordinator.installer.InstallFilesAsRestoreLeaves(
		ctx,
		stagingRoot,
		plan.WorkspaceRoot,
	); err != nil {
		return coordinator.rollback(ctx, journal, err)
	}
	if err := coordinator.advance(ctx, &journal, StageTopologyCommitted); err != nil {
		return err
	}
	if err := coordinator.installer.CommitAuditEpoch(
		ctx,
		plan.WorkspaceID,
		plan.SnapshotID,
	); err != nil {
		return coordinator.rollback(ctx, journal, err)
	}
	if err := coordinator.advance(ctx, &journal, StageAuditEpochCommitted); err != nil {
		return err
	}
	if err := coordinator.runtime.Start(ctx); err != nil {
		return coordinator.rollback(ctx, journal, err)
	}
	if err := coordinator.runtime.Health(ctx); err != nil {
		return coordinator.rollback(ctx, journal, err)
	}
	if err := coordinator.advance(ctx, &journal, StageVerified); err != nil {
		return err
	}
	if err := coordinator.advance(ctx, &journal, StageRecoverySnapshotPending); err != nil {
		return err
	}
	if err := coordinator.after.CaptureRecovery(ctx, plan.WorkspaceID); err != nil {
		return err
	}
	if err := coordinator.advance(ctx, &journal, StageCommitted); err != nil {
		return err
	}
	return coordinator.removeJournal()
}

func (coordinator *Coordinator) Recover(ctx context.Context, workspaceID string) error {
	journal, err := coordinator.readJournal()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if journal.WorkspaceID != workspaceID {
		return ErrWorkspaceMismatch
	}
	switch journal.Stage {
	case StageCommitted:
		return coordinator.removeJournal()
	case StageVerified, StageRecoverySnapshotPending:
		if journal.Stage == StageVerified {
			if err := coordinator.advance(ctx, &journal, StageRecoverySnapshotPending); err != nil {
				return err
			}
		}
		if err := coordinator.after.CaptureRecovery(ctx, workspaceID); err != nil {
			return err
		}
		if err := coordinator.advance(ctx, &journal, StageCommitted); err != nil {
			return err
		}
		return coordinator.removeJournal()
	default:
		return coordinator.rollback(ctx, journal, nil)
	}
}

func (coordinator *Coordinator) readJournal() (Journal, error) {
	raw, err := os.ReadFile(coordinator.journalPath)
	if err != nil {
		return Journal{}, err
	}
	var journal Journal
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil || journal.FormatVersion != 2 ||
		journal.WorkspaceID == "" || journal.SnapshotID == "" ||
		journal.PreviousRoot == "" || journal.StagingRoot == "" {
		return Journal{}, ErrJournalCorrupt
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Journal{}, ErrJournalCorrupt
	}
	if !validStage(journal.Stage) {
		return Journal{}, ErrJournalCorrupt
	}
	return journal, nil
}

func validStage(stage Stage) bool {
	switch stage {
	case StagePrepared,
		StageServicesStopped,
		StageDataInstalled,
		StageTopologyCommitted,
		StageAuditEpochCommitted,
		StageVerified,
		StageRecoverySnapshotPending,
		StageCommitted,
		StageRollbackServicesStopped,
		StageRollbackDataRestored,
		StageRollbackVerified:
		return true
	default:
		return false
	}
}

func (coordinator *Coordinator) advance(
	ctx context.Context,
	journal *Journal,
	stage Stage,
) error {
	journal.Stage = stage
	if err := coordinator.persist(*journal); err != nil {
		if stage != StageCommitted {
			return coordinator.rollback(ctx, *journal, err)
		}
		return err
	}
	return nil
}

func (coordinator *Coordinator) rollback(ctx context.Context, journal Journal, cause error) error {
	// Stop is deliberately idempotent. A failure can arrive after the restored
	// runtime has already started (Health or StageVerified persistence), and
	// overwriting its live database/files would create an observable mixed
	// state.
	if err := coordinator.runtime.Stop(ctx); err != nil {
		return errors.Join(cause, err)
	}
	journal.Stage = StageRollbackServicesStopped
	if err := coordinator.persist(journal); err != nil {
		return errors.Join(cause, err)
	}
	if err := coordinator.installer.Rollback(
		ctx,
		journal.StagingRoot,
		journal.PreviousRoot,
	); err != nil {
		return errors.Join(cause, err)
	}
	journal.Stage = StageRollbackDataRestored
	if err := coordinator.persist(journal); err != nil {
		return errors.Join(cause, err)
	}
	if err := coordinator.runtime.Start(ctx); err != nil {
		return errors.Join(cause, err)
	}
	if err := coordinator.runtime.Health(ctx); err != nil {
		return errors.Join(cause, err)
	}
	journal.Stage = StageRollbackVerified
	if err := coordinator.persist(journal); err != nil {
		return errors.Join(cause, err)
	}
	return errors.Join(cause, coordinator.removeJournal())
}

func (coordinator *Coordinator) writeJournal(journal Journal) error {
	directory := filepath.Dir(coordinator.journalPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".restore-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceJournalFile(tempName, coordinator.journalPath)
}

func (coordinator *Coordinator) removeJournal() error {
	if err := os.Remove(coordinator.journalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(coordinator.journalPath))
}
