package replica

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
)

const (
	filesystemReplicaFormat        = 1
	maxFilesystemRecoveryEntries   = 10_000
	maxFilesystemRecoveryReadBytes = snapshot.MaxBundleMaterializedBytes
)

type filesystemReplicaIdentity struct {
	FormatVersion uint64               `json:"formatVersion"`
	WorkspaceID   string               `json:"workspaceId"`
	ReplicaID     string               `json:"replicaId"`
	Strength      CoordinationStrength `json:"strength"`
}

type filesystemCheckpoint struct {
	FormatVersion uint64     `json:"formatVersion"`
	Checkpoint    Checkpoint `json:"checkpoint"`
	CheckpointID  string     `json:"checkpointId"`
	CommittedAt   time.Time  `json:"committedAt"`
}

// FilesystemRecoveryBundle is the independently readable recovery boundary
// stored at the selected mirrored root. None of its contents are read from the
// activity-root repository.
type FilesystemRecoveryBundle struct {
	Snapshot  snapshot.Record
	Objects   map[objectrepo.ObjectID][]byte
	Manifests map[objectrepo.ManifestID]objectrepo.ManifestRecord
}

// FilesystemRemote is an advisory-only replica for user-selected mirrored
// roots. It never overwrites a shared head and all public artifacts are
// immutable, so ordinary sync-folder semantics cannot be mistaken for strong
// linearizable coordination.
type FilesystemRemote struct {
	root       string
	home       string
	workspace  string
	replicaID  string
	repository objectrepo.Repository
	now        func() time.Time
}

func OpenFilesystemRemote(
	ctx context.Context,
	selectedRoot string,
	workspaceID string,
	repository objectrepo.Repository,
) (*FilesystemRemote, error) {
	return openFilesystemRemote(
		ctx,
		selectedRoot,
		workspaceID,
		repository,
		false,
		false,
	)
}

// OpenFilesystemRemoteReadOnly verifies an existing remote without creating
// directories or requiring a local activity repository.
func OpenFilesystemRemoteReadOnly(
	ctx context.Context,
	selectedRoot string,
	workspaceID string,
) (*FilesystemRemote, error) {
	return openFilesystemRemote(
		ctx,
		selectedRoot,
		workspaceID,
		nil,
		false,
		true,
	)
}

// CreateFilesystemRemote initializes a new immutable replica identity. Normal
// open/relink/recovery paths must use OpenFilesystemRemote and fail closed when
// that identity is missing.
func CreateFilesystemRemote(
	ctx context.Context,
	selectedRoot string,
	workspaceID string,
	repository objectrepo.Repository,
) (*FilesystemRemote, error) {
	return openFilesystemRemote(
		ctx,
		selectedRoot,
		workspaceID,
		repository,
		true,
		false,
	)
}

func openFilesystemRemote(
	ctx context.Context,
	selectedRoot string,
	workspaceID string,
	repository objectrepo.Repository,
	create bool,
	readOnly bool,
) (*FilesystemRemote, error) {
	if strings.TrimSpace(selectedRoot) == "" ||
		strings.TrimSpace(workspaceID) == "" ||
		(repository == nil && !readOnly) ||
		(create && readOnly) {
		return nil, ErrReplicationUnavailable
	}
	root, err := filepath.Abs(selectedRoot)
	if err != nil {
		return nil, err
	}
	manifestRaw, err := os.ReadFile(
		filepath.Join(root, ".vibetable", "workspace.json"),
	)
	if err != nil {
		return nil, errors.Join(ErrReplicationUnavailable, err)
	}
	var manifest contractsv2.WorkspaceManifest
	if err := decodeFilesystemJSON(manifestRaw, &manifest); err != nil ||
		manifest.Validate() != nil ||
		manifest.WorkspaceID != workspaceID ||
		manifest.StorageMode != "mirrored" {
		return nil, ErrRemoteIdentityInvalid
	}
	home := filepath.Join(
		root,
		".vibetable",
		"replica-v2",
		workspaceID,
	)
	if readOnly {
		info, err := os.Stat(home)
		if err != nil {
			return nil, errors.Join(ErrRemoteUnavailable, err)
		}
		if !info.IsDir() {
			return nil, ErrRemoteIdentityInvalid
		}
	} else {
		if err := os.MkdirAll(home, 0o700); err != nil {
			return nil, err
		}
	}
	remote := &FilesystemRemote{
		root: root, home: home, workspace: workspaceID,
		repository: repository,
		now:        func() time.Time { return time.Now().UTC() },
	}
	var identity filesystemReplicaIdentity
	if create {
		identity, err = remote.createIdentity(ctx)
	} else {
		identity, err = remote.loadIdentity(ctx)
	}
	if err != nil {
		return nil, err
	}
	remote.replicaID = identity.ReplicaID
	return remote, nil
}

func (remote *FilesystemRemote) VerifyIdentity(
	context.Context,
) (RemoteIdentity, error) {
	if remote == nil {
		return RemoteIdentity{}, ErrRemoteUnavailable
	}
	raw, err := os.ReadFile(remote.identityPath())
	if err != nil {
		return RemoteIdentity{}, errors.Join(ErrRemoteUnavailable, err)
	}
	var identity filesystemReplicaIdentity
	if err := decodeFilesystemJSON(raw, &identity); err != nil ||
		identity.FormatVersion != filesystemReplicaFormat ||
		identity.WorkspaceID != remote.workspace ||
		identity.ReplicaID != remote.replicaID ||
		identity.Strength != Advisory {
		return RemoteIdentity{}, ErrRemoteIdentityInvalid
	}
	return RemoteIdentity{
		WorkspaceID: identity.WorkspaceID,
		ReplicaID:   identity.ReplicaID,
		Strength:    Advisory,
	}, nil
}

func (remote *FilesystemRemote) ReplicateCheckpoint(
	ctx context.Context,
	checkpoint Checkpoint,
) (ReplicationReceipt, error) {
	if err := remote.validateCheckpoint(checkpoint); err != nil {
		return ReplicationReceipt{}, err
	}
	roots := sortedRoots(checkpoint.Roots)
	objects := make(map[objectrepo.ObjectID][]byte, len(roots))
	for _, root := range roots {
		reader, err := remote.repository.Open(ctx, root)
		if err != nil {
			return ReplicationReceipt{}, err
		}
		content, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return ReplicationReceipt{}, err
		}
		if filesystemObjectID(content) != root {
			return ReplicationReceipt{}, objectrepo.ErrCorrupt
		}
		objects[root] = append([]byte(nil), content...)
		path, err := remote.objectPath(root)
		if err != nil {
			return ReplicationReceipt{}, err
		}
		if err := writeImmutable(path, content); err != nil {
			return ReplicationReceipt{}, err
		}
	}
	if err := validateFilesystemRecoveryClosure(
		checkpoint,
		objects,
	); err != nil {
		return ReplicationReceipt{}, err
	}
	for _, manifest := range checkpoint.Manifests {
		if err := objectrepo.VerifyManifestRecord(manifest); err != nil {
			return ReplicationReceipt{}, err
		}
		raw, err := json.Marshal(manifest)
		if err != nil {
			return ReplicationReceipt{}, err
		}
		path, err := remote.manifestPath(manifest.ID)
		if err != nil {
			return ReplicationReceipt{}, err
		}
		if err := writeImmutable(path, raw); err != nil {
			return ReplicationReceipt{}, err
		}
	}
	checkpoint.Roots = roots
	committedAt := remote.now().UTC()
	rawCheckpoint, err := json.Marshal(checkpoint)
	if err != nil {
		return ReplicationReceipt{}, err
	}
	checkpointID := filesystemDigest(rawCheckpoint)
	record := filesystemCheckpoint{
		FormatVersion: filesystemReplicaFormat,
		Checkpoint:    checkpoint,
		CheckpointID:  checkpointID,
		CommittedAt:   committedAt,
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return ReplicationReceipt{}, err
	}
	if err := writeImmutable(remote.checkpointPath(checkpoint), raw); err != nil {
		return ReplicationReceipt{}, err
	}
	return ReplicationReceipt{
		WorkspaceID:     checkpoint.WorkspaceID,
		ReplicaID:       checkpoint.ReplicaID,
		SnapshotID:      checkpoint.SnapshotID,
		CatalogRevision: checkpoint.CatalogRevision,
		CheckpointID:    checkpointID,
		RootDigest:      checkpoint.RootDigest,
		CommittedAt:     committedAt,
	}, nil
}

func (remote *FilesystemRemote) ReopenAndVerifyRoots(
	ctx context.Context,
	checkpoint Checkpoint,
	replication ReplicationReceipt,
) (VerificationReceipt, error) {
	bundle, stored, err := remote.recoverCheckpoint(
		ctx,
		checkpoint.SnapshotID,
		checkpoint.CatalogRevision,
	)
	if err != nil {
		return VerificationReceipt{}, err
	}
	if stored.CheckpointID != replication.CheckpointID {
		return VerificationReceipt{}, ErrVerificationInvalid
	}
	rawCheckpoint, err := json.Marshal(stored.Checkpoint)
	expectedCheckpoint, expectedErr := json.Marshal(checkpoint)
	if err != nil || expectedErr != nil ||
		!bytes.Equal(rawCheckpoint, expectedCheckpoint) ||
		filesystemDigest(rawCheckpoint) != stored.CheckpointID {
		return VerificationReceipt{}, ErrVerificationInvalid
	}
	if bundle.Snapshot.SnapshotID != checkpoint.SnapshotID {
		return VerificationReceipt{}, ErrVerificationInvalid
	}
	return VerificationReceipt{
		WorkspaceID:      checkpoint.WorkspaceID,
		ReplicaID:        checkpoint.ReplicaID,
		SnapshotID:       checkpoint.SnapshotID,
		CatalogRevision:  checkpoint.CatalogRevision,
		CheckpointID:     replication.CheckpointID,
		RootDigest:       checkpoint.RootDigest,
		Reopened:         true,
		AllRootsReadable: true,
		VerifiedAt:       remote.now().UTC(),
	}, nil
}

// RecoverCheckpoint reopens a complete snapshot recovery bundle using only
// immutable artifacts at the selected mirrored root.
func (remote *FilesystemRemote) RecoverCheckpoint(
	ctx context.Context,
	snapshotID string,
	catalogRevision uint64,
) (FilesystemRecoveryBundle, error) {
	bundle, stored, err := remote.recoverCheckpoint(
		ctx,
		snapshotID,
		catalogRevision,
	)
	if err != nil {
		return FilesystemRecoveryBundle{}, err
	}
	if err := remote.verifyRecoveryPublication(
		ctx,
		snapshotID,
		catalogRevision,
		stored.CheckpointID,
	); err != nil {
		return FilesystemRecoveryBundle{}, err
	}
	return bundle, nil
}

func (remote *FilesystemRemote) recoverCheckpoint(
	ctx context.Context,
	snapshotID string,
	catalogRevision uint64,
) (FilesystemRecoveryBundle, filesystemCheckpoint, error) {
	if _, err := remote.VerifyIdentity(ctx); err != nil {
		return FilesystemRecoveryBundle{}, filesystemCheckpoint{}, err
	}
	path := remote.checkpointPathParts(snapshotID, catalogRevision)
	budget := filesystemRecoveryReadBudget{
		remaining: maxFilesystemRecoveryReadBytes,
	}
	raw, err := budget.readFile(ctx, path)
	if err != nil {
		return FilesystemRecoveryBundle{}, filesystemCheckpoint{}, err
	}
	var stored filesystemCheckpoint
	if err := decodeFilesystemJSON(raw, &stored); err != nil ||
		stored.FormatVersion != filesystemReplicaFormat {
		return FilesystemRecoveryBundle{}, filesystemCheckpoint{},
			ErrVerificationInvalid
	}
	if err := validateFilesystemRecoveryEntryLimits(
		stored.Checkpoint,
	); err != nil {
		return FilesystemRecoveryBundle{}, filesystemCheckpoint{}, err
	}
	rawCheckpoint, err := json.Marshal(stored.Checkpoint)
	if err != nil ||
		filesystemDigest(rawCheckpoint) != stored.CheckpointID ||
		stored.Checkpoint.SnapshotID != snapshotID ||
		stored.Checkpoint.CatalogRevision != catalogRevision ||
		remote.validateCheckpoint(stored.Checkpoint) != nil {
		return FilesystemRecoveryBundle{}, filesystemCheckpoint{},
			ErrVerificationInvalid
	}
	objects := make(
		map[objectrepo.ObjectID][]byte,
		len(stored.Checkpoint.Roots),
	)
	for _, root := range sortedRoots(stored.Checkpoint.Roots) {
		objectPath, err := remote.objectPath(root)
		if err != nil {
			return FilesystemRecoveryBundle{}, filesystemCheckpoint{}, err
		}
		content, err := budget.readFile(ctx, objectPath)
		if err != nil {
			return FilesystemRecoveryBundle{}, filesystemCheckpoint{}, err
		}
		if filesystemObjectID(content) != root {
			return FilesystemRecoveryBundle{}, filesystemCheckpoint{},
				ErrVerificationInvalid
		}
		objects[root] = content
	}
	if err := validateFilesystemRecoveryClosure(
		stored.Checkpoint,
		objects,
	); err != nil {
		return FilesystemRecoveryBundle{}, filesystemCheckpoint{}, err
	}
	manifests := make(
		map[objectrepo.ManifestID]objectrepo.ManifestRecord,
		len(stored.Checkpoint.Manifests),
	)
	for _, expected := range stored.Checkpoint.Manifests {
		manifestPath, err := remote.manifestPath(expected.ID)
		if err != nil {
			return FilesystemRecoveryBundle{}, filesystemCheckpoint{}, err
		}
		raw, err := budget.readFile(ctx, manifestPath)
		if err != nil {
			return FilesystemRecoveryBundle{}, filesystemCheckpoint{}, err
		}
		var actual objectrepo.ManifestRecord
		if err := decodeFilesystemJSON(raw, &actual); err != nil ||
			objectrepo.VerifyManifestRecord(actual) != nil {
			return FilesystemRecoveryBundle{}, filesystemCheckpoint{},
				ErrVerificationInvalid
		}
		expectedRaw, expectedErr := json.Marshal(expected)
		actualRaw, actualErr := json.Marshal(actual)
		if expectedErr != nil || actualErr != nil ||
			!bytes.Equal(expectedRaw, actualRaw) {
			return FilesystemRecoveryBundle{}, filesystemCheckpoint{},
				ErrVerificationInvalid
		}
		manifests[actual.ID] = actual
	}
	if err := validateFilesystemSnapshotMetadata(
		ctx,
		stored.Checkpoint,
		objects,
		manifests,
	); err != nil {
		return FilesystemRecoveryBundle{}, filesystemCheckpoint{}, err
	}
	return FilesystemRecoveryBundle{
		Snapshot:  stored.Checkpoint.Snapshot,
		Objects:   objects,
		Manifests: manifests,
	}, stored, nil
}

func (remote *FilesystemRemote) AppendPublication(
	_ context.Context,
	publication Publication,
) error {
	if remote == nil ||
		publication.WorkspaceID != remote.workspace ||
		publication.Claim.WorkspaceID != remote.workspace ||
		publication.Claim.Strength != Advisory ||
		publication.CatalogRevision == 0 ||
		publication.CheckpointID == "" ||
		publication.Claim.Nonce != publication.CheckpointID ||
		publication.PublicationID != fmt.Sprintf(
			"%s/%s/%020d",
			publication.Claim.ClaimID,
			publication.SnapshotID,
			publication.CatalogRevision,
		) {
		return ErrPublicationTampered
	}
	if _, err := uuid.Parse(publication.Claim.ClaimID); err != nil {
		return ErrPublicationTampered
	}
	if _, err := uuid.Parse(publication.SnapshotID); err != nil {
		return ErrPublicationTampered
	}
	sealed, err := SealPublication(publication)
	if err != nil || sealed.CanonicalHash != publication.CanonicalHash {
		return ErrPublicationTampered
	}
	raw, err := json.Marshal(publication)
	if err != nil {
		return err
	}
	return writeImmutable(
		filepath.Join(
			remote.home,
			"publications",
			publication.Claim.ClaimID,
			fmt.Sprintf(
				"%s-%020d.json",
				publication.SnapshotID,
				publication.CatalogRevision,
			),
		),
		raw,
	)
}

func (remote *FilesystemRemote) ListPublications(
	_ context.Context,
	workspaceID string,
) ([]Publication, error) {
	if remote == nil || workspaceID != remote.workspace {
		return nil, ErrWorkspaceMismatch
	}
	root := filepath.Join(remote.home, "publications")
	var result []Publication
	err := filepath.WalkDir(
		root,
		func(path string, entry os.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var publication Publication
			if err := decodeFilesystemJSON(raw, &publication); err != nil ||
				publication.WorkspaceID != workspaceID {
				return ErrPublicationTampered
			}
			result = append(result, publication)
			return nil
		},
	)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].PublicationID < result[right].PublicationID
	})
	return result, err
}

func (remote *FilesystemRemote) DiscoverConflicts(
	ctx context.Context,
	scan ConflictScan,
) ([]IncomingConflict, error) {
	if remote == nil ||
		scan.WorkspaceID != remote.workspace ||
		scan.ReplicaID != remote.replicaID ||
		scan.LocalSnapshotID == "" ||
		scan.LocalCatalogRevision == 0 {
		return nil, ErrReplicationUnavailable
	}
	publications, err := remote.ListPublications(ctx, remote.workspace)
	if err != nil {
		return nil, err
	}
	sort.Slice(publications, func(i, j int) bool {
		if publications[i].CreatedAt.Equal(publications[j].CreatedAt) {
			return publications[i].CanonicalHash <
				publications[j].CanonicalHash
		}
		return publications[i].CreatedAt.Before(publications[j].CreatedAt)
	})
	for _, publication := range publications {
		if err := remote.verifyRecoveryPublication(
			ctx,
			publication.SnapshotID,
			publication.CatalogRevision,
			publication.CheckpointID,
		); err != nil {
			return nil, err
		}
	}
	branches, err := filesystemConflictBranches(publications, scan)
	if err != nil {
		return nil, err
	}
	var result []IncomingConflict
	for _, branch := range branches {
		publication := branch.Head
		base := branch.Base
		bundle, stored, err := remote.recoverCheckpoint(
			ctx,
			publication.SnapshotID,
			publication.CatalogRevision,
		)
		if err != nil {
			return nil, err
		}
		if stored.CheckpointID != publication.CheckpointID {
			return nil, ErrVerificationInvalid
		}
		replicaCandidate, err := filesystemConflictCandidate(bundle)
		if err != nil {
			return nil, err
		}
		replicaCandidate.SnapshotID = publication.SnapshotID
		conflictID := uuid.NewSHA1(
			uuid.NameSpaceOID,
			[]byte(
				remote.workspace+"\x00"+
					scan.LocalSnapshotID+"\x00"+
					publication.CanonicalHash,
			),
		).String()
		checkpoint := stored.Checkpoint
		result = append(result, IncomingConflict{
			Set: conflictresolution.Set{
				ConflictID:  conflictID,
				WorkspaceID: remote.workspace,
				State:       conflictresolution.StatePending,
				Revision:    1,
				Base: conflictresolution.Candidate{
					SnapshotID: base.SnapshotID,
				},
				Local: conflictresolution.Candidate{
					SnapshotID: scan.LocalSnapshotID,
				},
				Replica: replicaCandidate,
				Dependencies: conflictresolution.DependencyGraph{
					Complete: false,
					Edges:    map[string][]string{},
				},
				CreatedAt: publication.CreatedAt.UTC(),
			},
			ReplicaSnapshot: bundle.Snapshot,
			Roots:           append([]objectrepo.ObjectID(nil), checkpoint.Roots...),
			Verification: VerificationReceipt{
				WorkspaceID:      checkpoint.WorkspaceID,
				ReplicaID:        checkpoint.ReplicaID,
				SnapshotID:       checkpoint.SnapshotID,
				CatalogRevision:  checkpoint.CatalogRevision,
				CheckpointID:     stored.CheckpointID,
				RootDigest:       checkpoint.RootDigest,
				Reopened:         true,
				AllRootsReadable: true,
				VerifiedAt:       remote.now().UTC(),
			},
			RecoveryBundle: &bundle,
		})
	}
	return result, nil
}

type filesystemConflictBranch struct {
	Head Publication
	Base Publication
}

func filesystemConflictBranches(
	publications []Publication,
	scan ConflictScan,
) ([]filesystemConflictBranch, error) {
	byHash := make(map[string]Publication, len(publications))
	referenced := make(map[string]struct{}, len(publications))
	for _, publication := range publications {
		byHash[publication.CanonicalHash] = publication
		if publication.PreviousPublicationHash != "" {
			referenced[publication.PreviousPublicationHash] = struct{}{}
		}
	}
	localAncestors := map[string]struct{}{}
	for _, publication := range publications {
		if publication.SnapshotID != scan.LocalSnapshotID ||
			publication.CatalogRevision != scan.LocalCatalogRevision {
			continue
		}
		current := publication
		for {
			if _, seen := localAncestors[current.CanonicalHash]; seen {
				break
			}
			localAncestors[current.CanonicalHash] = struct{}{}
			if current.PreviousPublicationHash == "" {
				break
			}
			parent, exists := byHash[current.PreviousPublicationHash]
			if !exists {
				return nil, ErrPublicationTampered
			}
			current = parent
		}
	}
	if len(localAncestors) == 0 {
		return nil, ErrVerificationInvalid
	}
	var result []filesystemConflictBranch
	for _, publication := range publications {
		if _, isParent := referenced[publication.CanonicalHash]; isParent {
			continue
		}
		if _, local := localAncestors[publication.CanonicalHash]; local {
			continue
		}
		if publication.SnapshotID == scan.LocalSnapshotID {
			continue
		}
		current := publication
		var base Publication
		foundBase := false
		for current.PreviousPublicationHash != "" {
			parent, exists := byHash[current.PreviousPublicationHash]
			if !exists {
				return nil, ErrPublicationTampered
			}
			if _, local := localAncestors[parent.CanonicalHash]; local {
				base = parent
				foundBase = true
				break
			}
			current = parent
		}
		if !foundBase {
			return nil, ErrVerificationInvalid
		}
		if base.SnapshotID == publication.SnapshotID {
			continue
		}
		result = append(result, filesystemConflictBranch{
			Head: publication,
			Base: base,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Head.CanonicalHash <
			result[j].Head.CanonicalHash
	})
	return result, nil
}

func filesystemConflictCandidate(
	bundle FilesystemRecoveryBundle,
) (conflictresolution.Candidate, error) {
	var history objectrepo.ManifestRecord
	for _, manifest := range bundle.Manifests {
		if manifest.Name == "filehistory-root" {
			history = manifest
			break
		}
	}
	if history.ID == "" {
		return conflictresolution.Candidate{}, ErrVerificationInvalid
	}
	var root struct {
		FormatVersion int    `json:"formatVersion"`
		WorkspaceID   string `json:"workspaceId"`
		Documents     []struct {
			DocumentID          string `json:"documentId"`
			RelativePath        string `json:"relativePath"`
			Status              string `json:"status"`
			EffectiveRevisionID string `json:"effectiveRevisionId"`
			Revisions           []struct {
				RevisionID string              `json:"revisionId"`
				ObjectID   objectrepo.ObjectID `json:"objectId"`
				MimeType   string              `json:"mimeType"`
			} `json:"revisions"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(history.Payload, &root); err != nil ||
		(root.FormatVersion != 2 && root.FormatVersion != 3) ||
		root.WorkspaceID != bundle.Snapshot.WorkspaceID {
		return conflictresolution.Candidate{}, ErrVerificationInvalid
	}
	databaseID := bundle.Snapshot.ObjectMap["database"]
	settingsID := bundle.Snapshot.ObjectMap["workspace-settings"]
	fileStateRootID := bundle.Snapshot.ObjectMap["file-state-root"]
	database, databaseExists := bundle.Objects[databaseID]
	_, settingsExists := bundle.Objects[settingsID]
	fileStateRoot, fileStateRootExists := bundle.Objects[fileStateRootID]
	if databaseID == "" || settingsID == "" || fileStateRootID == "" ||
		!databaseExists || !settingsExists || !fileStateRootExists {
		return conflictresolution.Candidate{}, ErrVerificationInvalid
	}
	var fileState struct {
		FormatVersion uint64                         `json:"formatVersion"`
		SourceRoot    objectrepo.ManifestID          `json:"sourceRoot"`
		Files         map[string]objectrepo.ObjectID `json:"files"`
		Attachments   map[string]objectrepo.ObjectID `json:"attachments,omitempty"`
	}
	if err := decodeFilesystemJSON(fileStateRoot, &fileState); err != nil ||
		fileState.FormatVersion != 1 ||
		fileState.SourceRoot == "" ||
		fileState.Files == nil {
		return conflictresolution.Candidate{}, ErrVerificationInvalid
	}
	attachmentObjects := make(
		map[string]string, len(fileState.Attachments),
	)
	for key, id := range fileState.Attachments {
		if strings.TrimSpace(key) == "" || id == "" {
			return conflictresolution.Candidate{}, ErrVerificationInvalid
		}
		if _, exists := bundle.Objects[id]; !exists {
			return conflictresolution.Candidate{}, ErrVerificationInvalid
		}
		attachmentObjects[key] = string(id)
	}
	projection, err := conflictresolution.ProjectSQLiteDatabase(
		context.Background(),
		database,
		string(databaseID),
		attachmentObjects,
	)
	if err != nil {
		return conflictresolution.Candidate{},
			errors.Join(ErrVerificationInvalid, err)
	}
	candidate := conflictresolution.Candidate{
		SnapshotID:               bundle.Snapshot.SnapshotID,
		BusinessDatabaseObjectID: string(databaseID),
		Settings: conflictresolution.SettingsState{
			ObjectID: string(settingsID),
		},
		AttachmentObjects: attachmentObjects,
		Revision:          bundle.Snapshot.CatalogRevision,
		Files:             map[string]conflictresolution.FileState{},
		Tables:            projection.Tables,
	}
	for _, document := range root.Documents {
		state := conflictresolution.FileState{
			DocumentID: document.DocumentID,
			Path:       document.RelativePath,
			Deleted:    document.Status == "deleted",
		}
		for _, revision := range document.Revisions {
			if revision.RevisionID == document.EffectiveRevisionID {
				state.ContentID = string(revision.ObjectID)
				state.MimeType = revision.MimeType
				break
			}
		}
		if !state.Deleted &&
			(state.ContentID == "" ||
				strings.TrimSpace(state.MimeType) == "") {
			return conflictresolution.Candidate{}, ErrVerificationInvalid
		}
		candidate.Files[document.DocumentID] = state
	}
	return candidate, nil
}

func (remote *FilesystemRemote) loadIdentity(
	_ context.Context,
) (filesystemReplicaIdentity, error) {
	path := remote.identityPath()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return filesystemReplicaIdentity{}, ErrRemoteIdentityInvalid
	}
	if err != nil {
		return filesystemReplicaIdentity{}, err
	}
	var identity filesystemReplicaIdentity
	if err := decodeFilesystemJSON(raw, &identity); err != nil ||
		identity.FormatVersion != filesystemReplicaFormat ||
		identity.WorkspaceID != remote.workspace ||
		identity.ReplicaID == "" ||
		identity.Strength != Advisory {
		return filesystemReplicaIdentity{}, ErrRemoteIdentityInvalid
	}
	return identity, nil
}

func (remote *FilesystemRemote) createIdentity(
	_ context.Context,
) (filesystemReplicaIdentity, error) {
	path := remote.identityPath()
	if _, err := os.Stat(path); err == nil {
		return filesystemReplicaIdentity{}, ErrRemoteIdentityInvalid
	} else if !errors.Is(err, os.ErrNotExist) {
		return filesystemReplicaIdentity{}, err
	}
	identity := filesystemReplicaIdentity{
		FormatVersion: filesystemReplicaFormat,
		WorkspaceID:   remote.workspace,
		ReplicaID:     uuid.NewString(),
		Strength:      Advisory,
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return filesystemReplicaIdentity{}, err
	}
	if err := writeImmutable(path, raw); err != nil {
		return filesystemReplicaIdentity{}, err
	}
	return identity, nil
}

func (remote *FilesystemRemote) verifyRecoveryPublication(
	ctx context.Context,
	snapshotID string,
	catalogRevision uint64,
	checkpointID string,
) error {
	publications, err := remote.ListPublications(ctx, remote.workspace)
	if err != nil {
		return err
	}
	dag, err := NewAdvisoryDAG(remote.workspace)
	if err != nil {
		return err
	}
	pending := append([]Publication(nil), publications...)
	for len(pending) > 0 {
		progress := false
		next := pending[:0]
		for _, publication := range pending {
			err := dag.PublishContext(ctx, publication)
			if err == nil || errors.Is(err, ErrPublicationExists) {
				progress = true
				continue
			}
			if errors.Is(err, ErrParentMissing) {
				next = append(next, publication)
				continue
			}
			return err
		}
		if !progress {
			return ErrPublicationTampered
		}
		pending = next
	}
	if _, err := dag.HeadsContext(ctx); err != nil {
		return err
	}
	for _, publication := range publications {
		if publication.SnapshotID == snapshotID &&
			publication.CatalogRevision == catalogRevision &&
			publication.CheckpointID == checkpointID &&
			publication.Claim.Nonce == checkpointID {
			return nil
		}
	}
	return ErrVerificationInvalid
}

func (remote *FilesystemRemote) validateCheckpoint(
	checkpoint Checkpoint,
) error {
	if remote == nil ||
		checkpoint.WorkspaceID != remote.workspace ||
		checkpoint.ReplicaID != remote.replicaID ||
		checkpoint.SnapshotID == "" ||
		checkpoint.CatalogRevision == 0 ||
		len(checkpoint.Roots) == 0 ||
		checkpoint.RootDigest != rootsDigest(sortedRoots(checkpoint.Roots)) ||
		checkpoint.Snapshot.WorkspaceID != checkpoint.WorkspaceID ||
		checkpoint.Snapshot.SnapshotID != checkpoint.SnapshotID ||
		checkpoint.Snapshot.CatalogRevision != checkpoint.CatalogRevision ||
		checkpoint.Snapshot.FenceEpoch != checkpoint.FenceEpoch ||
		checkpoint.Snapshot.ClaimID != checkpoint.ClaimID ||
		checkpoint.Snapshot.ManifestID == "" ||
		checkpoint.Snapshot.SealID == "" ||
		len(checkpoint.Manifests) < 4 {
		return ErrVerificationInvalid
	}
	rootSet := make(map[objectrepo.ObjectID]struct{}, len(checkpoint.Roots))
	for _, id := range checkpoint.Roots {
		rootSet[id] = struct{}{}
	}
	for _, id := range checkpoint.Snapshot.Objects {
		if _, exists := rootSet[id]; !exists {
			return ErrVerificationInvalid
		}
	}
	return nil
}

func (remote *FilesystemRemote) identityPath() string {
	return filepath.Join(remote.home, "identity.json")
}

func (remote *FilesystemRemote) checkpointPath(checkpoint Checkpoint) string {
	return remote.checkpointPathParts(
		checkpoint.SnapshotID,
		checkpoint.CatalogRevision,
	)
}

func (remote *FilesystemRemote) checkpointPathParts(
	snapshotID string,
	catalogRevision uint64,
) string {
	return filepath.Join(
		remote.home,
		"checkpoints",
		fmt.Sprintf(
			"%s-%020d.json",
			snapshotID,
			catalogRevision,
		),
	)
}

func (remote *FilesystemRemote) objectPath(
	id objectrepo.ObjectID,
) (string, error) {
	value := string(id)
	const prefix = "obj_"
	if len(value) != len(prefix)+sha256.Size*2 ||
		!strings.HasPrefix(value, prefix) {
		return "", objectrepo.ErrCorrupt
	}
	digest := value[len(prefix):]
	if _, err := hex.DecodeString(digest); err != nil {
		return "", objectrepo.ErrCorrupt
	}
	return filepath.Join(
		remote.home,
		"objects",
		digest[:2],
		digest+".blob",
	), nil
}

func (remote *FilesystemRemote) manifestPath(
	id objectrepo.ManifestID,
) (string, error) {
	value := string(id)
	const prefix = "manifest_"
	if len(value) != len(prefix)+sha256.Size*2 ||
		!strings.HasPrefix(value, prefix) {
		return "", objectrepo.ErrCorrupt
	}
	digest := value[len(prefix):]
	if _, err := hex.DecodeString(digest); err != nil {
		return "", objectrepo.ErrCorrupt
	}
	return filepath.Join(
		remote.home,
		"manifests",
		digest[:2],
		digest+".json",
	), nil
}

func validateFilesystemRecoveryClosure(
	checkpoint Checkpoint,
	objects map[objectrepo.ObjectID][]byte,
) error {
	if len(objects) != len(sortedRoots(checkpoint.Roots)) {
		return ErrVerificationInvalid
	}
	roots := make(map[objectrepo.ObjectID]struct{}, len(checkpoint.Roots))
	for _, id := range checkpoint.Roots {
		if _, duplicate := roots[id]; duplicate {
			return ErrVerificationInvalid
		}
		roots[id] = struct{}{}
		if _, exists := objects[id]; !exists {
			return ErrVerificationInvalid
		}
	}
	for _, id := range checkpoint.Snapshot.ObjectMap {
		if _, exists := roots[id]; !exists {
			return ErrVerificationInvalid
		}
	}
	fileRaw, exists := objects[checkpoint.Snapshot.ObjectMap["file-state-root"]]
	if !exists {
		return ErrVerificationInvalid
	}
	var fileState struct {
		FormatVersion uint64                         `json:"formatVersion"`
		SourceRoot    objectrepo.ManifestID          `json:"sourceRoot"`
		Files         map[string]objectrepo.ObjectID `json:"files"`
		Attachments   map[string]objectrepo.ObjectID `json:"attachments,omitempty"`
	}
	if err := decodeFilesystemJSON(fileRaw, &fileState); err != nil ||
		fileState.FormatVersion != 1 ||
		fileState.SourceRoot == "" ||
		fileState.Files == nil {
		return ErrVerificationInvalid
	}
	for _, id := range fileState.Files {
		if _, exists := roots[id]; !exists {
			return ErrVerificationInvalid
		}
	}
	for _, id := range fileState.Attachments {
		if _, exists := roots[id]; !exists {
			return ErrVerificationInvalid
		}
	}
	return nil
}

func validateFilesystemSnapshotMetadata(
	ctx context.Context,
	checkpoint Checkpoint,
	objects map[objectrepo.ObjectID][]byte,
	manifests map[objectrepo.ManifestID]objectrepo.ManifestRecord,
) error {
	record := checkpoint.Snapshot
	snapshotRecord, exists := manifests[record.ManifestID]
	if !exists {
		return ErrVerificationInvalid
	}
	sealRecord, exists := manifests[record.SealID]
	if !exists {
		return ErrVerificationInvalid
	}
	var topologyRef struct {
		ManifestID objectrepo.ManifestID `json:"manifestId"`
	}
	if err := decodeFilesystemJSON(
		objects[record.ObjectMap["topology-root"]],
		&topologyRef,
	); err != nil || topologyRef.ManifestID == "" {
		return ErrVerificationInvalid
	}
	var fileRef struct {
		FormatVersion uint64                         `json:"formatVersion"`
		SourceRoot    objectrepo.ManifestID          `json:"sourceRoot"`
		Files         map[string]objectrepo.ObjectID `json:"files"`
		Attachments   map[string]objectrepo.ObjectID `json:"attachments,omitempty"`
	}
	if err := decodeFilesystemJSON(
		objects[record.ObjectMap["file-state-root"]],
		&fileRef,
	); err != nil ||
		fileRef.FormatVersion != 1 ||
		fileRef.SourceRoot == "" ||
		fileRef.Files == nil {
		return ErrVerificationInvalid
	}
	topology, exists := manifests[topologyRef.ManifestID]
	if !exists {
		return ErrVerificationInvalid
	}
	files, exists := manifests[fileRef.SourceRoot]
	if !exists {
		return ErrVerificationInvalid
	}
	var fileHead struct {
		FormatVersion uint64                `json:"formatVersion"`
		WorkspaceID   string                `json:"workspaceId"`
		HistoryRoot   objectrepo.ManifestID `json:"historyRoot"`
		FileRevision  uint64                `json:"fileRevision"`
	}
	if err := decodeFilesystemJSON(files.Payload, &fileHead); err != nil {
		return ErrVerificationInvalid
	}
	required := map[objectrepo.ManifestID]struct{}{
		record.ManifestID:      {},
		record.SealID:          {},
		topologyRef.ManifestID: {},
		fileRef.SourceRoot:     {},
	}
	var historyRoot *objectrepo.ManifestRecord
	historyObjects := map[objectrepo.ObjectID][]byte{}
	if fileHead.HistoryRoot != "" {
		history, exists := manifests[fileHead.HistoryRoot]
		if !exists {
			return ErrVerificationInvalid
		}
		historyRoot = &history
		historyIDs, err := snapshot.FileHistoryObjectIDs(
			history.Payload,
			record.WorkspaceID,
		)
		if err != nil {
			return errors.Join(ErrVerificationInvalid, err)
		}
		current := make(
			map[objectrepo.ObjectID]struct{},
			len(record.ObjectMap),
		)
		for _, id := range record.ObjectMap {
			current[id] = struct{}{}
		}
		for _, id := range historyIDs {
			if _, isCurrent := current[id]; isCurrent {
				continue
			}
			raw, exists := objects[id]
			if !exists {
				return ErrVerificationInvalid
			}
			historyObjects[id] = raw
		}
		required[fileHead.HistoryRoot] = struct{}{}
	}
	if len(manifests) != len(required) {
		return ErrVerificationInvalid
	}
	for id := range required {
		if _, exists := manifests[id]; !exists {
			return ErrVerificationInvalid
		}
	}
	currentObjects := make(map[string][]byte, len(record.ObjectMap))
	for name, id := range record.ObjectMap {
		raw, exists := objects[id]
		if !exists {
			return ErrVerificationInvalid
		}
		currentObjects[name] = raw
	}
	if err := snapshot.ValidateSnapshotBundleData(
		ctx,
		snapshot.SnapshotBundle{
			Record:         record,
			Manifest:       snapshotRecord,
			Seal:           sealRecord,
			Objects:        currentObjects,
			TopologyHead:   topology,
			FileStateHead:  files,
			HistoryRoot:    historyRoot,
			HistoryObjects: historyObjects,
		},
	); err != nil {
		if errors.Is(err, snapshot.ErrBundleInvalid) ||
			errors.Is(err, snapshot.ErrBundleResourceLimit) {
			return errors.Join(ErrVerificationInvalid, err)
		}
		return err
	}
	return nil
}

func validateFilesystemRecoveryEntryLimits(checkpoint Checkpoint) error {
	if len(checkpoint.Roots) > maxFilesystemRecoveryEntries ||
		len(checkpoint.Manifests) > maxFilesystemRecoveryEntries ||
		len(checkpoint.Snapshot.ObjectMap) > maxFilesystemRecoveryEntries ||
		len(checkpoint.Snapshot.Objects) > maxFilesystemRecoveryEntries {
		return errors.Join(
			ErrVerificationInvalid,
			snapshot.ErrBundleResourceLimit,
		)
	}
	return nil
}

type filesystemRecoveryReadBudget struct {
	remaining int64
}

func (budget *filesystemRecoveryReadBudget) readFile(
	ctx context.Context,
	path string,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.Join(ErrRemoteUnavailable, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(ErrRemoteUnavailable, err)
	}
	size := info.Size()
	if size < 0 || size > budget.remaining {
		return nil, errors.Join(
			ErrVerificationInvalid,
			snapshot.ErrBundleResourceLimit,
		)
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(file, raw); err != nil {
		return nil, errors.Join(ErrRemoteUnavailable, err)
	}
	var trailing [1]byte
	read, err := file.Read(trailing[:])
	if read != 0 || !errors.Is(err, io.EOF) {
		return nil, ErrVerificationInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	budget.remaining -= size
	return raw, nil
}

func filesystemDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func filesystemObjectID(content []byte) objectrepo.ObjectID {
	sum := sha256.Sum256(content)
	return objectrepo.ObjectID("obj_" + hex.EncodeToString(sum[:]))
}

func writeImmutable(path string, content []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, content) {
			return nil
		}
		return errors.New("replica.immutable_conflict")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp := filepath.Join(
		filepath.Dir(path),
		"."+filepath.Base(path)+"."+uuid.NewString()+".tmp",
	)
	file, err := os.OpenFile(
		temp,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return err
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(temp)
		}
	}()
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := publishImmutable(temp, path); err != nil {
		if existing, readErr := os.ReadFile(path); readErr == nil &&
			bytes.Equal(existing, content) {
			return nil
		}
		return err
	}
	removeTemp = false
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func decodeFilesystemJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("replica.trailing_json")
	}
	return nil
}
