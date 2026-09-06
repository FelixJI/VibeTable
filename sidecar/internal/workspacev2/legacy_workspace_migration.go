package workspacev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
)

// VerifyLegacyMigrationCopy mutates only a caller-owned, offline migration copy.
// The host must retain the original under its writer fence. The copy stays at
// format 1 even on success; publishing the returned manifest is a separate step.
func VerifyLegacyMigrationCopy(ctx context.Context, options ReplicaOneShotOptions) (_ contractsv2.WorkspaceManifest, err error) {
	if err := ctx.Err(); err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	if !validUUID(options.WorkspaceID) || !validUUID(options.ClaimID) || options.SessionEpoch == 0 || options.FenceEpoch == 0 || options.ReplicaRoot != "" || options.ActivityRoot != "" {
		return contractsv2.WorkspaceManifest{}, errors.New("workspace.migration.identity_invalid")
	}
	paths, err := resolvePaths(options.DataDir)
	if err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	raw, err := readFileBounded(paths.manifest, 1<<20)
	if err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	var format uint64
	if err := json.Unmarshal(fields["formatVersion"], &format); err != nil || format != 1 {
		return contractsv2.WorkspaceManifest{}, errors.New("workspace.migration.source_format_unsupported")
	}
	fields["formatVersion"], err = json.Marshal(contractsv2.WorkspaceFormatVersion)
	if err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	converted, err := json.Marshal(fields)
	if err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	manifest, err := contractsv2.DecodeStrict[contractsv2.WorkspaceManifest](converted)
	if err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	if manifest.WorkspaceID != options.WorkspaceID {
		return contractsv2.WorkspaceManifest{}, errors.New("workspace.identity_mismatch")
	}
	// Only the direct/convenient legacy layout currently has a formal corpus.
	if manifest.StorageMode != "direct" || manifest.EncryptionMode != "convenient" || manifest.TopologySchemaVersion != 1 || manifest.BusinessSchemaVersion != 1 {
		return contractsv2.WorkspaceManifest{}, errors.New("workspace.migration.source_layout_unsupported")
	}
	authorityRaw, err := readFileBounded(filepath.Join(paths.coordination, "desktop-runtime-authority.json"), 1<<20)
	if err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	var authority struct {
		FormatVersion    int    `json:"formatVersion"`
		WorkspaceID      string `json:"workspaceId"`
		FenceEpoch       uint64 `json:"fenceEpoch"`
		ClaimID          string `json:"claimId"`
		LastSessionEpoch uint64 `json:"lastSessionEpoch"`
	}
	if err := decodeStrictReplicaOneShot(authorityRaw, &authority); err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	if authority.FormatVersion != 1 || authority.WorkspaceID != options.WorkspaceID || authority.FenceEpoch != options.FenceEpoch || authority.ClaimID != options.ClaimID || authority.LastSessionEpoch != options.SessionEpoch {
		return contractsv2.WorkspaceManifest{}, errors.New("workspace.migration.authority_mismatch")
	}
	// Runtime openers may initialize a new database. Migration must never turn
	// missing historical state into an apparently valid empty workspace.
	for _, path := range []string{
		filepath.Join(paths.data, "data.db"),
		filepath.Join(paths.audit, "ledger.db"),
		filepath.Join(paths.coordination, "workspace-v2.db"),
		filepath.Join(paths.coordination, "write-coordinator.db"),
		filepath.Join(paths.coordination, "retention.db"),
		filepath.Join(paths.snapshots, "catalog.db"),
		filepath.Join(paths.topology, "filehistory-head.db"),
	} {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return contractsv2.WorkspaceManifest{}, fmt.Errorf("workspace.migration.source_storage_missing: %w", statErr)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return contractsv2.WorkspaceManifest{}, errors.New("workspace.migration.source_storage_invalid")
		}
	}
	if err := objectrepo.ValidateExistingWorkspaceRepositoryLayout(objectrepo.WorkspaceRepositorySpec{
		Format:           manifest.RepositoryFormat,
		CoordinationRoot: paths.coordination,
		ObjectsRoot:      paths.objects,
	}); err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	openMigrationRuntime := func(ctx context.Context, runtimeOptions Options) (*Runtime, error) {
		runtimeOptions.DeferBackgroundWorkers = true
		return openBoundRuntime(ctx, runtimeOptions, paths, manifest)
	}
	runtime, closeRuntime, err := openBoundOneShotRuntime(ctx, options, paths, openMigrationRuntime, loadReplicaOneShotMigrationManifest)
	if err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	defer func() { err = errors.Join(err, closeRuntime()) }()
	if _, err := runtime.ledger.VerifiedRecords(ctx); err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	inventory, err := runtime.repository.RetentionInventory(ctx)
	if err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	if inventory.CorruptIndex || inventory.UnknownManifest || inventory.PendingPublication {
		return contractsv2.WorkspaceManifest{}, errors.New("workspace.migration.repository_unverified")
	}
	ids := make([]objectrepo.ObjectID, 0, len(inventory.Objects))
	for _, object := range inventory.Objects {
		ids = append(ids, object.ID)
	}
	report, err := runtime.repository.Verify(ctx, ids)
	if err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	if !report.Valid {
		return contractsv2.WorkspaceManifest{}, errors.New("workspace.migration.objects_unverified")
	}
	records, err := runtime.catalog.List(ctx, options.WorkspaceID)
	if err != nil {
		return contractsv2.WorkspaceManifest{}, err
	}
	for _, record := range records {
		if _, err := snapshot.LoadSnapshotBundle(ctx, runtime.repository, record); err != nil {
			return contractsv2.WorkspaceManifest{}, err
		}
	}
	return manifest, nil
}
