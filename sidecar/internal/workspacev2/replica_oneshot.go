package workspacev2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/replica"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	_ "modernc.org/sqlite"
)

// ReplicaOneShotOptions contains only trusted host-provided workspace
// identity and absolute paths. The CLI deliberately has no path flags.
type ReplicaOneShotOptions struct {
	DataDir      string
	ActivityRoot string
	ReplicaRoot  string
	WorkspaceID  string
	SessionEpoch uint64
	FenceEpoch   uint64
	ClaimID      string
}

// ReplicaOneShotReceipt is the single strict JSON document emitted by each
// replica one-shot command.
type ReplicaOneShotReceipt struct {
	ActivityRoot             *string `json:"activityRoot"`
	CatalogRevision          uint64  `json:"catalogRevision"`
	CheckpointID             string  `json:"checkpointId"`
	ContractVersion          string  `json:"contractVersion"`
	Healthy                  *bool   `json:"healthy,omitempty"`
	MutationRevision         uint64  `json:"mutationRevision"`
	Operation                string  `json:"operation"`
	ReceiptHash              string  `json:"receiptHash"`
	ReplicaID                string  `json:"replicaId"`
	RequiredMutationRevision uint64  `json:"requiredMutationRevision"`
	Restored                 *bool   `json:"restored,omitempty"`
	SnapshotID               string  `json:"snapshotId"`
	VerifiedAt               string  `json:"verifiedAt"`
	WorkspaceID              string  `json:"workspaceId"`
}

type replicaOneShotSelection struct {
	bundle      replica.FilesystemRecoveryBundle
	identity    replica.RemoteIdentity
	publication replica.Publication
	versions    []replicaOneShotVersion
	verifiedAt  time.Time
}

type replicaOneShotVersion struct {
	bundle      replica.FilesystemRecoveryBundle
	publication replica.Publication
}

func InitializeWorkspaceReplica(
	ctx context.Context,
	options ReplicaOneShotOptions,
) (ReplicaOneShotReceipt, error) {
	paths, manifest, err := validateReplicaOneShotLocal(options)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	releaseRepository, err := objectrepo.AcquireRepositorySession(
		ctx,
		filepath.Join(paths.coordination, "kopia.repository.config"),
	)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	repository, err := openRepository(ctx, paths, manifest, Options{
		WorkspaceID: options.WorkspaceID,
		FenceEpoch:  options.FenceEpoch,
		ClaimID:     options.ClaimID,
	})
	if err != nil {
		return ReplicaOneShotReceipt{},
			errors.Join(err, releaseRepository())
	}
	if _, openErr := replica.OpenFilesystemRemote(
		ctx,
		options.ReplicaRoot,
		options.WorkspaceID,
		repository,
	); openErr != nil {
		if !errors.Is(openErr, replica.ErrRemoteIdentityInvalid) {
			return ReplicaOneShotReceipt{}, errors.Join(
				openErr,
				repository.Close(context.Background()),
				releaseRepository(),
			)
		}
		if _, err := replica.CreateFilesystemRemote(
			ctx,
			options.ReplicaRoot,
			options.WorkspaceID,
			repository,
		); err != nil {
			return ReplicaOneShotReceipt{}, errors.Join(
				err,
				repository.Close(context.Background()),
				releaseRepository(),
			)
		}
	}
	if err := errors.Join(
		repository.Close(context.Background()),
		releaseRepository(),
	); err != nil {
		return ReplicaOneShotReceipt{}, err
	}

	runtime, closeRuntime, err := openReplicaOneShotRuntime(ctx, options)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	defer func() {
		if closeRuntime != nil {
			_ = closeRuntime()
		}
	}()
	records, err := runtime.catalog.List(ctx, options.WorkspaceID)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	if len(records) == 0 {
		token, _ := runtime.coordinator.Current()
		if _, _, err := runtime.snapshots.Capture(
			ctx,
			snapshot.CaptureRequest{
				WorkspaceID: options.WorkspaceID,
				Authority:   token.Authority(),
				Trigger:     snapshot.TriggerProtection,
				Pinned:      true,
			},
		); err != nil {
			return ReplicaOneShotReceipt{}, err
		}
	}
	if runtime.replicaConflict == nil {
		return ReplicaOneShotReceipt{},
			errors.New("replica.production_unavailable")
	}
	runtime.replicaConflict.managerMu.RLock()
	manager := runtime.replicaConflict.manager
	runtime.replicaConflict.managerMu.RUnlock()
	if manager == nil {
		return ReplicaOneShotReceipt{},
			errors.New("replica.production_unavailable")
	}
	if err := runtime.replicaConflict.scanSelectedFiles(ctx); err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	if err := manager.Synchronize(ctx); err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	remote, ok := runtime.replicaConflict.managerOptions.Remote.(*replica.FilesystemRemote)
	if !ok || remote == nil {
		return ReplicaOneShotReceipt{},
			errors.New("replica.filesystem_remote_required")
	}
	publicationKey, err := deriveReplicaPublicationKey(ctx, manifest)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	defer clearBytes(publicationKey)
	selection, err := selectReplicaCheckpoint(
		ctx,
		remote,
		options.WorkspaceID,
		publicationKey,
	)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	if err := closeRuntime(); err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	closeRuntime = nil
	requiredMutationRevision, err := replicaOneShotRequiredMutationRevision(
		ctx,
		options,
	)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	return buildReplicaOneShotReceipt(
		"initialize",
		"",
		selection,
		requiredMutationRevision,
	)
}

func VerifyWorkspaceReplica(
	ctx context.Context,
	options ReplicaOneShotOptions,
) (ReplicaOneShotReceipt, error) {
	requiredMutationRevision, err :=
		replicaOneShotRequiredMutationRevision(ctx, options)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	manifest, err := readReplicaManifest(
		options.ReplicaRoot,
		options.WorkspaceID,
	)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	publicationKey, err := deriveReplicaPublicationKey(ctx, manifest)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	defer clearBytes(publicationKey)
	remote, err := replica.OpenFilesystemRemoteReadOnly(
		ctx,
		options.ReplicaRoot,
		options.WorkspaceID,
	)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	selection, err := selectReplicaCheckpoint(
		ctx,
		remote,
		options.WorkspaceID,
		publicationKey,
	)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	return buildReplicaOneShotReceipt(
		"verify",
		"",
		selection,
		requiredMutationRevision,
	)
}

func RecoverWorkspaceReplica(
	ctx context.Context,
	options ReplicaOneShotOptions,
) (ReplicaOneShotReceipt, error) {
	activityRoot, dataDir, err := validateRecoveryTarget(options)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	manifest, err := readReplicaManifest(
		options.ReplicaRoot,
		options.WorkspaceID,
	)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	publicationKey, err := deriveReplicaPublicationKey(ctx, manifest)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	defer clearBytes(publicationKey)
	remote, err := replica.OpenFilesystemRemoteReadOnly(
		ctx,
		options.ReplicaRoot,
		options.WorkspaceID,
	)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	selection, err := selectReplicaCheckpoint(
		ctx,
		remote,
		options.WorkspaceID,
		publicationKey,
	)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	if err := installReplicaRecovery(
		ctx,
		options,
		activityRoot,
		dataDir,
		manifest,
		selection,
	); err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	requiredMutationRevision, err :=
		replicaOneShotRequiredMutationRevision(ctx, options)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	return buildReplicaOneShotReceipt(
		"recover",
		activityRoot,
		selection,
		requiredMutationRevision,
	)
}

func replicaOneShotRequiredMutationRevision(
	ctx context.Context,
	options ReplicaOneShotOptions,
) (uint64, error) {
	paths, _, err := validateReplicaOneShotLocal(options)
	if err != nil {
		return 0, err
	}
	coordinatorRevision, err :=
		writecoordinator.ReadPersistentMutationRevision(
			ctx,
			filepath.Join(
				paths.coordination,
				"write-coordinator.db",
			),
			options.WorkspaceID,
		)
	if err != nil {
		return 0, fmt.Errorf(
			"read replica-required coordinator high-watermark: %w",
			err,
		)
	}
	fileHistoryRevision, err :=
		filehistory.ReadPersistentMutationRevision(
			ctx,
			filepath.Join(
				paths.topology,
				"filehistory-head.db",
			),
			options.WorkspaceID,
		)
	if err != nil {
		return 0, fmt.Errorf(
			"read replica-required file-history high-watermark: %w",
			err,
		)
	}
	return maxReplicaOneShot(
		coordinatorRevision,
		fileHistoryRevision,
	), nil
}

func validateReplicaOneShotLocal(
	options ReplicaOneShotOptions,
) (workspacePaths, contractsv2.WorkspaceManifest, error) {
	if err := validateReplicaOneShotIdentity(options); err != nil {
		return workspacePaths{}, contractsv2.WorkspaceManifest{}, err
	}
	if !filepath.IsAbs(options.DataDir) ||
		!filepath.IsAbs(options.ReplicaRoot) {
		return workspacePaths{}, contractsv2.WorkspaceManifest{},
			errors.New("replica.path_not_absolute")
	}
	paths, manifest, err := validateBinding(
		options.DataDir,
		options.WorkspaceID,
	)
	if err != nil {
		return workspacePaths{}, contractsv2.WorkspaceManifest{}, err
	}
	if manifest.StorageMode != "mirrored" {
		return workspacePaths{}, contractsv2.WorkspaceManifest{},
			errors.New("replica.workspace_not_mirrored")
	}
	selected, err := readReplicaManifest(
		options.ReplicaRoot,
		options.WorkspaceID,
	)
	if err != nil {
		return workspacePaths{}, contractsv2.WorkspaceManifest{}, err
	}
	localRaw, localErr := json.Marshal(manifest)
	selectedRaw, selectedErr := json.Marshal(selected)
	if localErr != nil || selectedErr != nil ||
		!bytes.Equal(localRaw, selectedRaw) {
		return workspacePaths{}, contractsv2.WorkspaceManifest{},
			errors.New("workspace.identity_mismatch")
	}
	return paths, manifest, nil
}

func validateReplicaOneShotIdentity(options ReplicaOneShotOptions) error {
	if !validUUID(options.WorkspaceID) ||
		!validUUID(options.ClaimID) ||
		options.SessionEpoch == 0 ||
		options.FenceEpoch == 0 {
		return errors.New("workspace.v2.identity_invalid")
	}
	return nil
}

func readReplicaManifest(
	selectedRoot string,
	workspaceID string,
) (contractsv2.WorkspaceManifest, error) {
	if !filepath.IsAbs(selectedRoot) {
		return contractsv2.WorkspaceManifest{},
			errors.New("replica.path_not_absolute")
	}
	raw, err := readFileBounded(
		filepath.Join(
			filepath.Clean(selectedRoot),
			".vibetable",
			"workspace.json",
		),
		1<<20,
	)
	if err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	manifest, err := contractsv2.DecodeStrict[contractsv2.WorkspaceManifest](raw)
	if err != nil ||
		manifest.Validate() != nil ||
		manifest.WorkspaceID != workspaceID ||
		manifest.StorageMode != "mirrored" {
		return contractsv2.WorkspaceManifest{},
			errors.New("workspace.identity_mismatch")
	}
	return manifest, nil
}

func validateRecoveryTarget(
	options ReplicaOneShotOptions,
) (string, string, error) {
	if err := validateReplicaOneShotIdentity(options); err != nil {
		return "", "", err
	}
	if !filepath.IsAbs(options.ActivityRoot) ||
		!filepath.IsAbs(options.DataDir) ||
		!filepath.IsAbs(options.ReplicaRoot) {
		return "", "", errors.New("replica.path_not_absolute")
	}
	activity, err := filepath.Abs(options.ActivityRoot)
	if err != nil {
		return "", "", err
	}
	dataDir, err := filepath.Abs(options.DataDir)
	if err != nil {
		return "", "", err
	}
	expected := filepath.Join(activity, ".vibetable", "data")
	if !sameName(filepath.Clean(dataDir), filepath.Clean(expected)) {
		return "", "", errors.New("replica.recovery_data_dir_mismatch")
	}
	info, err := os.Lstat(activity)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return "", "", err
	case !info.IsDir():
		return "", "", errors.New("replica.recovery_target_invalid")
	default:
		entries, readErr := os.ReadDir(activity)
		if readErr != nil {
			return "", "", readErr
		}
		if len(entries) != 0 {
			return "", "", errors.New("replica.recovery_target_invalid")
		}
	}
	return activity, dataDir, nil
}

func openReplicaOneShotRuntime(
	ctx context.Context,
	options ReplicaOneShotOptions,
) (*Runtime, func() error, error) {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  options.DataDir,
		HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		return nil, nil, err
	}
	paths, _, err := validateBinding(options.DataDir, options.WorkspaceID)
	if err != nil {
		_ = app.ResetBootstrapState()
		return nil, nil, err
	}
	ledger, err := auditledger.Open(paths.audit)
	if err != nil {
		_ = app.ResetBootstrapState()
		return nil, nil, err
	}
	runtime, err := Open(ctx, Options{
		App:                  app,
		DataDir:              options.DataDir,
		WorkspaceID:          options.WorkspaceID,
		SessionEpoch:         options.SessionEpoch,
		FenceEpoch:           options.FenceEpoch,
		ClaimID:              options.ClaimID,
		Ledger:               ledger,
		ReplicaRoot:          options.ReplicaRoot,
		ReplicaDeviceID:      options.ClaimID,
		DisableReplicaWorker: true,
	})
	if err != nil {
		_ = ledger.Close()
		_ = app.ResetBootstrapState()
		return nil, nil, err
	}
	closeRuntime := func() error {
		return errors.Join(
			runtime.Close(context.Background()),
			ledger.Close(),
			app.ResetBootstrapState(),
		)
	}
	return runtime, closeRuntime, nil
}

func selectReplicaCheckpoint(
	ctx context.Context,
	remote *replica.FilesystemRemote,
	workspaceID string,
	publicationKey []byte,
) (replicaOneShotSelection, error) {
	identity, err := remote.VerifyIdentity(ctx)
	if err != nil {
		return replicaOneShotSelection{}, err
	}
	publications, err := remote.ListPublications(ctx, workspaceID)
	if err != nil {
		return replicaOneShotSelection{}, err
	}
	sort.Slice(publications, func(left, right int) bool {
		if publications[left].CreatedAt.Equal(publications[right].CreatedAt) {
			return publications[left].CanonicalHash <
				publications[right].CanonicalHash
		}
		return publications[left].CreatedAt.Before(
			publications[right].CreatedAt,
		)
	})
	dag, err := replica.NewAdvisoryDAG(workspaceID, publicationKey)
	if err != nil {
		return replicaOneShotSelection{}, err
	}
	defer dag.Close()
	pending := append([]replica.Publication(nil), publications...)
	for len(pending) != 0 {
		progress := false
		next := pending[:0]
		for _, publication := range pending {
			err := dag.PublishContext(ctx, publication)
			if err == nil || errors.Is(err, replica.ErrPublicationExists) {
				progress = true
				continue
			}
			if errors.Is(err, replica.ErrParentMissing) {
				next = append(next, publication)
				continue
			}
			return replicaOneShotSelection{}, err
		}
		if !progress {
			return replicaOneShotSelection{}, replica.ErrParentMissing
		}
		pending = next
	}
	heads, err := dag.HeadsContext(ctx)
	if err != nil {
		return replicaOneShotSelection{}, err
	}
	if len(heads) != 1 {
		return replicaOneShotSelection{},
			errors.New("replica.recovery_head_ambiguous")
	}
	publication := heads[0]
	latest := make(map[string]replicaOneShotVersion, len(publications))
	for _, candidate := range publications {
		bundle, err := remote.RecoverCheckpoint(
			ctx,
			candidate.SnapshotID,
			candidate.CatalogRevision,
			publicationKey,
		)
		if err != nil {
			return replicaOneShotSelection{}, err
		}
		if bundle.Snapshot.WorkspaceID != workspaceID ||
			bundle.Snapshot.SnapshotID != candidate.SnapshotID ||
			bundle.Snapshot.CatalogRevision != candidate.CatalogRevision {
			return replicaOneShotSelection{},
				replica.ErrVerificationInvalid
		}
		existing, found := latest[candidate.SnapshotID]
		if !found ||
			existing.publication.CatalogRevision <
				candidate.CatalogRevision {
			latest[candidate.SnapshotID] = replicaOneShotVersion{
				bundle:      bundle,
				publication: candidate,
			}
		}
	}
	versions := make([]replicaOneShotVersion, 0, len(latest))
	for _, version := range latest {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(left, right int) bool {
		if versions[left].bundle.Snapshot.SnapshotSequence !=
			versions[right].bundle.Snapshot.SnapshotSequence {
			return versions[left].bundle.Snapshot.SnapshotSequence <
				versions[right].bundle.Snapshot.SnapshotSequence
		}
		return versions[left].publication.CatalogRevision <
			versions[right].publication.CatalogRevision
	})
	head, found := latest[publication.SnapshotID]
	if !found ||
		head.publication.CatalogRevision != publication.CatalogRevision {
		return replicaOneShotSelection{}, replica.ErrVerificationInvalid
	}
	return replicaOneShotSelection{
		bundle:      head.bundle,
		identity:    identity,
		publication: publication,
		versions:    versions,
		verifiedAt:  time.Now().UTC(),
	}, nil
}

func installReplicaRecovery(
	ctx context.Context,
	options ReplicaOneShotOptions,
	activityRoot string,
	dataDir string,
	manifest contractsv2.WorkspaceManifest,
	selection replicaOneShotSelection,
) (resultErr error) {
	if _, err := os.Lstat(activityRoot); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(activityRoot, 0o700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			_ = os.RemoveAll(activityRoot)
		}
	}()
	metadata := filepath.Join(activityRoot, ".vibetable")
	directories := []string{
		dataDir,
		filepath.Join(metadata, "topology"),
		filepath.Join(metadata, "objects"),
		filepath.Join(metadata, "audit"),
		filepath.Join(metadata, "snapshots"),
		filepath.Join(metadata, "coordination"),
		filepath.Join(metadata, "files"),
		filepath.Join(metadata, "quarantine"),
		filepath.Join(metadata, "temp"),
	}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := writeReplicaRecoveryFile(
		filepath.Join(metadata, "workspace.json"),
		manifestRaw,
	); err != nil {
		return err
	}
	record := selection.bundle.Snapshot
	if err := installReplicaDatabaseAndFiles(
		record,
		selection.bundle.Objects,
		metadata,
	); err != nil {
		return err
	}
	anchor, err := installReplicaAudit(
		ctx,
		record,
		selection.bundle.Objects,
		filepath.Join(metadata, "audit"),
	)
	if err != nil {
		return err
	}
	paths, _, err := validateBinding(dataDir, options.WorkspaceID)
	if err != nil {
		return err
	}
	repository, err := openRepository(ctx, paths, manifest, Options{
		WorkspaceID: options.WorkspaceID,
		FenceEpoch:  options.FenceEpoch,
		ClaimID:     options.ClaimID,
	})
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(
			resultErr,
			repository.Close(context.Background()),
		)
	}()
	catalog, err := snapshot.OpenDurableCatalog(
		filepath.Join(metadata, "snapshots", "catalog.db"),
	)
	if err != nil {
		return err
	}
	versions := selection.versions
	if len(versions) == 0 {
		versions = []replicaOneShotVersion{{
			bundle:      selection.bundle,
			publication: selection.publication,
		}}
	}
	var currentRecord snapshot.Record
	for _, version := range versions {
		allRoots, err := installReplicaRepository(
			ctx,
			repository,
			options,
			version.bundle,
		)
		if err != nil {
			_ = catalog.Close()
			return err
		}
		recovered := version.bundle.Snapshot
		pin, err := repository.Pin(
			ctx,
			objectrepo.Authority{
				WorkspaceID: options.WorkspaceID,
				FenceEpoch:  options.FenceEpoch,
				ClaimID:     options.ClaimID,
			},
			allRoots,
			"recovered-replica:"+recovered.SnapshotID,
			nil,
		)
		if err != nil {
			_ = catalog.Close()
			return err
		}
		recovered.RootPinID = pin.PinID
		recovered.CatalogSessionEpoch = options.SessionEpoch
		recovered.CatalogFenceEpoch = options.FenceEpoch
		recovered.CatalogClaimID = options.ClaimID
		if err := catalog.Publish(ctx, recovered); err != nil {
			_ = catalog.Close()
			return err
		}
		if recovered.SnapshotID == record.SnapshotID &&
			recovered.CatalogRevision == record.CatalogRevision {
			currentRecord = recovered
		}
	}
	if err := catalog.Close(); err != nil {
		return err
	}
	if currentRecord.SnapshotID == "" {
		return replica.ErrVerificationInvalid
	}
	record = currentRecord
	if err := installReplicaFileHead(
		ctx,
		record,
		selection.bundle.Manifests,
		filepath.Join(metadata, "topology", "filehistory-head.db"),
		options,
	); err != nil {
		return err
	}
	if err := installReplicaCoordinator(
		ctx,
		filepath.Join(metadata, "coordination", "write-coordinator.db"),
		options,
		record,
		anchor,
	); err != nil {
		return err
	}
	return installReplicaVerifiedState(
		filepath.Join(metadata, "coordination", "replica-state.db"),
		options.WorkspaceID,
		selection,
	)
}

func installReplicaDatabaseAndFiles(
	record snapshot.Record,
	objects map[objectrepo.ObjectID][]byte,
	metadata string,
) error {
	database, err := replicaObject(record, objects, "database")
	if err != nil {
		return err
	}
	databasePath := filepath.Join(metadata, "data", "data.db")
	if err := writeReplicaRecoveryFile(databasePath, database); err != nil {
		return err
	}
	if err := verifySQLiteDatabase(databasePath); err != nil {
		return err
	}
	settings, err := replicaObject(
		record,
		objects,
		"workspace-settings",
	)
	if err != nil || !json.Valid(settings) {
		return errors.Join(errors.New("replica.settings_invalid"), err)
	}
	if err := writeReplicaRecoveryFile(
		filepath.Join(metadata, "settings.json"),
		settings,
	); err != nil {
		return err
	}
	rootRaw, err := replicaObject(record, objects, "file-state-root")
	if err != nil {
		return err
	}
	var root struct {
		FormatVersion uint64                         `json:"formatVersion"`
		SourceRoot    objectrepo.ManifestID          `json:"sourceRoot"`
		Files         map[string]objectrepo.ObjectID `json:"files"`
		Attachments   map[string]objectrepo.ObjectID `json:"attachments,omitempty"`
	}
	if err := decodeStrictReplicaOneShot(rootRaw, &root); err != nil ||
		root.FormatVersion != 1 ||
		root.Files == nil {
		return errors.New("replica.file_state_invalid")
	}
	for relative, id := range root.Files {
		target, err := replicaRecoveryTarget(
			filepath.Join(metadata, "files"),
			relative,
		)
		if err != nil {
			return err
		}
		content, found := objects[id]
		if !found {
			return replica.ErrVerificationInvalid
		}
		if err := writeReplicaRecoveryFile(target, content); err != nil {
			return err
		}
	}
	for relative, id := range root.Attachments {
		target, err := replicaRecoveryTarget(
			filepath.Join(metadata, "data", "storage"),
			relative,
		)
		if err != nil {
			return err
		}
		content, found := objects[id]
		if !found {
			return replica.ErrVerificationInvalid
		}
		if err := writeReplicaRecoveryFile(target, content); err != nil {
			return err
		}
	}
	return nil
}

func installReplicaAudit(
	ctx context.Context,
	record snapshot.Record,
	objects map[objectrepo.ObjectID][]byte,
	auditRoot string,
) (auditledger.Anchor, error) {
	raw, err := replicaObject(record, objects, "audit-prefix")
	if err != nil {
		return auditledger.Anchor{}, err
	}
	anchor, err := auditledger.VerifyPrefix(raw)
	if err != nil {
		return auditledger.Anchor{}, err
	}
	var prefix auditledger.Prefix
	if err := decodeStrictReplicaOneShot(raw, &prefix); err != nil {
		return auditledger.Anchor{}, err
	}
	ledger, err := auditledger.Open(auditRoot)
	if err != nil {
		return auditledger.Anchor{}, err
	}
	for _, expected := range prefix.Records {
		appended, appendErr := ledger.Append(ctx, expected.Envelope)
		if appendErr != nil {
			_ = ledger.Close()
			return auditledger.Anchor{}, appendErr
		}
		if appended.Record.LedgerSequence != expected.LedgerSequence ||
			appended.Record.PreviousHash != expected.PreviousHash ||
			appended.Record.Hash != expected.Hash {
			_ = ledger.Close()
			return auditledger.Anchor{}, auditledger.ErrChainCorrupt
		}
	}
	if ledger.Anchor() != anchor || ledger.Verify() != nil {
		_ = ledger.Close()
		return auditledger.Anchor{}, auditledger.ErrChainCorrupt
	}
	return anchor, ledger.Close()
}

func installReplicaRepository(
	ctx context.Context,
	repository objectrepo.Repository,
	options ReplicaOneShotOptions,
	bundle replica.FilesystemRecoveryBundle,
) ([]objectrepo.ObjectID, error) {
	names := make(map[objectrepo.ObjectID]string, len(bundle.Objects))
	for name, id := range bundle.Snapshot.ObjectMap {
		names[id] = name
	}
	ids := make([]objectrepo.ObjectID, 0, len(bundle.Objects))
	for id := range bundle.Objects {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	inputs := make([]objectrepo.ObjectInput, 0, len(ids))
	for _, id := range ids {
		name := names[id]
		if name == "" {
			name = "closure:" + string(id)
		}
		inputs = append(inputs, objectrepo.ObjectInput{
			Name: name, Content: bundle.Objects[id],
		})
	}
	manifestIDs := make([]objectrepo.ManifestID, 0, len(bundle.Manifests))
	for id := range bundle.Manifests {
		manifestIDs = append(manifestIDs, id)
	}
	sort.Slice(manifestIDs, func(left, right int) bool {
		return manifestIDs[left] < manifestIDs[right]
	})
	manifests := make([]objectrepo.ManifestInput, 0, len(manifestIDs))
	for _, id := range manifestIDs {
		stored := bundle.Manifests[id]
		manifests = append(manifests, objectrepo.ManifestInput{
			Name:    stored.Name,
			Labels:  stored.Labels,
			Payload: append(json.RawMessage(nil), stored.Payload...),
		})
	}
	receipt, err := repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: objectrepo.Authority{
			WorkspaceID: options.WorkspaceID,
			FenceEpoch:  options.FenceEpoch,
			ClaimID:     options.ClaimID,
		},
		Objects:   inputs,
		Manifests: manifests,
	})
	if err != nil {
		return nil, err
	}
	if !receipt.Durable {
		return nil, errors.New("replica.recovery_repository_not_durable")
	}
	for _, input := range inputs {
		if receipt.Objects[input.Name] != contentIDReplicaOneShot(input.Content) {
			return nil, replica.ErrVerificationInvalid
		}
	}
	for _, expected := range bundle.Manifests {
		if receipt.Manifests[expected.Name] != expected.ID {
			return nil, replica.ErrVerificationInvalid
		}
	}
	report, err := repository.Verify(ctx, ids)
	if err != nil || !report.Valid {
		return nil, errors.Join(replica.ErrVerificationInvalid, err)
	}
	return ids, nil
}

func installReplicaFileHead(
	ctx context.Context,
	record snapshot.Record,
	manifests map[objectrepo.ManifestID]objectrepo.ManifestRecord,
	headPath string,
	options ReplicaOneShotOptions,
) error {
	var historyRoot objectrepo.ManifestID
	for _, manifest := range manifests {
		if manifest.Labels["type"] != "file-state-head" {
			continue
		}
		var state struct {
			FormatVersion uint64                `json:"formatVersion"`
			WorkspaceID   string                `json:"workspaceId"`
			HistoryRoot   objectrepo.ManifestID `json:"historyRoot"`
			FileRevision  uint64                `json:"fileRevision"`
		}
		if err := decodeStrictReplicaOneShot(
			manifest.Payload,
			&state,
		); err != nil {
			return err
		}
		if state.FormatVersion != 1 ||
			state.WorkspaceID != options.WorkspaceID ||
			state.FileRevision != record.FileRevision {
			return errors.New("replica.file_head_invalid")
		}
		historyRoot = state.HistoryRoot
		break
	}
	if record.FileRevision == 0 {
		if historyRoot != "" {
			return errors.New("replica.file_head_invalid")
		}
		return nil
	}
	if historyRoot == "" {
		return errors.New("replica.file_head_missing")
	}
	if _, found := manifests[historyRoot]; !found {
		return errors.New("replica.file_history_manifest_missing")
	}
	store, err := filehistory.OpenPersistentHeadStore(headPath)
	if err != nil {
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", headPath)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO filehistory_heads(
			workspace_id, root_manifest_id, revision, mutation_revision,
			session_epoch, fence_epoch, claim_id
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		options.WorkspaceID,
		historyRoot,
		record.FileRevision,
		maxReplicaOneShot(1, record.MutationRevision),
		options.SessionEpoch,
		options.FenceEpoch,
		options.ClaimID,
	)
	return errors.Join(err, db.Close())
}

func installReplicaCoordinator(
	ctx context.Context,
	databasePath string,
	options ReplicaOneShotOptions,
	record snapshot.Record,
	anchor auditledger.Anchor,
) error {
	coordinator, err := writecoordinator.OpenPersistent(
		databasePath,
		options.WorkspaceID,
		options.FenceEpoch,
		options.ClaimID,
		options.SessionEpoch,
	)
	if err != nil {
		return err
	}
	if err := coordinator.Close(); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `
		UPDATE coordination_state
		SET mutation_revision = ?, snapshot_sequence = ?,
		    audit_source_epoch = ?, audit_source_sequence = ?,
		    audit_chain_hash = ?
		WHERE singleton = 1 AND workspace_id = ?
		      AND session_epoch = ? AND fence_epoch = ? AND claim_id = ?`,
		record.MutationRevision,
		record.SnapshotSequence,
		anchor.SourceEpoch,
		anchor.SourceSequence,
		anchor.Hash,
		options.WorkspaceID,
		options.SessionEpoch,
		options.FenceEpoch,
		options.ClaimID,
	)
	if err == nil {
		var affected int64
		affected, err = result.RowsAffected()
		if err == nil && affected != 1 {
			err = errors.New("replica.coordination_restore_failed")
		}
	}
	return errors.Join(err, db.Close())
}

func installReplicaVerifiedState(
	databasePath string,
	workspaceID string,
	selection replicaOneShotSelection,
) error {
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		CREATE TABLE replica_pending (
			task_id TEXT PRIMARY KEY,
			pin_id TEXT NOT NULL
		);
		CREATE TABLE replica_verified (
			task_id TEXT NOT NULL UNIQUE,
			snapshot_id TEXT NOT NULL,
			catalog_revision INTEGER NOT NULL,
			verified_at INTEGER NOT NULL,
			replication_json BLOB NOT NULL,
			verification_json BLOB NOT NULL,
			PRIMARY KEY(snapshot_id, catalog_revision)
		);
		CREATE TABLE replica_takeover (
			singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
			state TEXT NOT NULL CHECK(state IN ('prepared','completed')),
			previous_json BLOB NOT NULL,
			claim_json BLOB NOT NULL,
			operation_receipt_json BLOB
		);
		CREATE TABLE replica_operation_receipts (
			operation_id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			method TEXT NOT NULL,
			scope TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			result_json BLOB NOT NULL
		);
	`); err != nil {
		return err
	}
	versions := selection.versions
	if len(versions) == 0 {
		versions = []replicaOneShotVersion{{
			bundle:      selection.bundle,
			publication: selection.publication,
		}}
	}
	for _, version := range versions {
		publication := version.publication
		replication := replica.ReplicationReceipt{
			WorkspaceID:     workspaceID,
			ReplicaID:       selection.identity.ReplicaID,
			SnapshotID:      publication.SnapshotID,
			CatalogRevision: publication.CatalogRevision,
			CheckpointID:    publication.CheckpointID,
			CommittedAt:     publication.CreatedAt,
		}
		verification := replica.VerificationReceipt{
			WorkspaceID:      workspaceID,
			ReplicaID:        selection.identity.ReplicaID,
			SnapshotID:       publication.SnapshotID,
			CatalogRevision:  publication.CatalogRevision,
			CheckpointID:     publication.CheckpointID,
			Reopened:         true,
			AllRootsReadable: true,
			VerifiedAt:       selection.verifiedAt,
		}
		replicationRaw, err := json.Marshal(replication)
		if err != nil {
			return err
		}
		verificationRaw, err := json.Marshal(verification)
		if err != nil {
			return err
		}
		taskID := replicaOneShotTaskID(
			selection.identity.ReplicaID,
			publication.SnapshotID,
			publication.CatalogRevision,
		)
		if _, err := db.Exec(`
			INSERT INTO replica_verified(
				task_id, snapshot_id, catalog_revision, verified_at,
				replication_json, verification_json
			) VALUES (?, ?, ?, ?, ?, ?)`,
			taskID,
			publication.SnapshotID,
			publication.CatalogRevision,
			selection.verifiedAt.UnixNano(),
			replicationRaw,
			verificationRaw,
		); err != nil {
			return err
		}
	}
	return nil
}

func buildReplicaOneShotReceipt(
	operation string,
	activityRoot string,
	selection replicaOneShotSelection,
	requiredMutationRevision uint64,
) (ReplicaOneShotReceipt, error) {
	if selection.bundle.Snapshot.MutationRevision <
		requiredMutationRevision {
		return ReplicaOneShotReceipt{},
			errors.New("replica.required_revision_not_covered")
	}
	healthy := true
	receipt := ReplicaOneShotReceipt{
		CatalogRevision:          selection.publication.CatalogRevision,
		CheckpointID:             selection.publication.CheckpointID,
		ContractVersion:          contractsv2.ContractVersion,
		MutationRevision:         selection.bundle.Snapshot.MutationRevision,
		Operation:                operation,
		ReplicaID:                selection.identity.ReplicaID,
		RequiredMutationRevision: requiredMutationRevision,
		SnapshotID:               selection.publication.SnapshotID,
		VerifiedAt: selection.verifiedAt.Format(
			"2006-01-02T15:04:05.0000000Z07:00",
		),
		WorkspaceID: selection.identity.WorkspaceID,
	}
	if operation == "recover" {
		receipt.ActivityRoot = &activityRoot
		receipt.Restored = &healthy
	} else {
		receipt.Healthy = &healthy
	}
	hashInput := receipt
	hashInput.ReceiptHash = ""
	raw, err := json.Marshal(hashInput)
	if err != nil {
		return ReplicaOneShotReceipt{}, err
	}
	sum := sha256.Sum256(raw)
	receipt.ReceiptHash = "sha256:" + hex.EncodeToString(sum[:])
	return receipt, nil
}

func replicaObject(
	record snapshot.Record,
	objects map[objectrepo.ObjectID][]byte,
	name string,
) ([]byte, error) {
	id := record.ObjectMap[name]
	content, found := objects[id]
	if id == "" || !found || contentIDReplicaOneShot(content) != id {
		return nil, replica.ErrVerificationInvalid
	}
	return content, nil
}

func contentIDReplicaOneShot(content []byte) objectrepo.ObjectID {
	sum := sha256.Sum256(content)
	return objectrepo.ObjectID("obj_" + hex.EncodeToString(sum[:]))
}

func replicaRecoveryTarget(root string, relative string) (string, error) {
	if relative == "" ||
		strings.Contains(relative, `\`) ||
		path.IsAbs(relative) ||
		path.Clean(relative) != relative ||
		relative == "." ||
		strings.HasPrefix(relative, "../") {
		return "", errors.New("replica.file_path_invalid")
	}
	target := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	prefix := filepath.Clean(root) + string(filepath.Separator)
	if !strings.HasPrefix(target, prefix) {
		return "", errors.New("replica.file_path_invalid")
	}
	return target, nil
}

func writeReplicaRecoveryFile(target string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(
		target,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func decodeStrictReplicaOneShot[T any](raw []byte, target *T) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("replica.trailing_json")
}

func replicaOneShotTaskID(
	replicaID string,
	snapshotID string,
	catalogRevision uint64,
) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%s\x00%d",
		replicaID,
		snapshotID,
		catalogRevision,
	)))
	return hex.EncodeToString(sum[:16])
}

func maxReplicaOneShot(left uint64, right uint64) uint64 {
	if left > right {
		return left
	}
	return right
}
