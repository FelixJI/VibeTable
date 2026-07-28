package workspacev2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

const selectedFilesBaseFormat = 1

type selectedFileVersion struct {
	Hash       string              `json:"hash"`
	ObjectID   objectrepo.ObjectID `json:"objectId"`
	DocumentID string              `json:"documentId"`
}

type selectedFilesBase struct {
	FormatVersion int                            `json:"formatVersion"`
	WorkspaceID   string                         `json:"workspaceId"`
	SnapshotID    string                         `json:"snapshotId"`
	Entries       map[string]selectedFileVersion `json:"entries"`
	UpdatedAt     time.Time                      `json:"updatedAt"`
}

type scannedSelectedFile struct {
	Path    string
	Hash    string
	Content []byte
}

func (owner *productionReplicaConflict) scanSelectedFiles(
	ctx context.Context,
) error {
	if owner == nil ||
		owner.selectedRoot == "" ||
		owner.activityFilesRoot == "" {
		return nil
	}
	remoteRoot := filepath.Join(owner.selectedRoot, "files")
	remote, err := scanSelectedTree(remoteRoot)
	if err != nil {
		return err
	}
	local, documents, err := owner.scanLocalSelectedTree()
	if err != nil {
		return err
	}
	base, found, err := owner.loadSelectedFilesBase()
	if err != nil {
		return err
	}
	if !found {
		base = selectedFilesBase{
			FormatVersion: selectedFilesBaseFormat,
			WorkspaceID:   owner.runtime.manifest.WorkspaceID,
			Entries:       map[string]selectedFileVersion{},
		}
	}
	if err := owner.reconcileSelectedRenames(
		ctx, base, local, remote, documents,
	); err != nil {
		return err
	}
	// Rename ingestion changes the activity tree, so scan again before
	// evaluating per-path three-way changes.
	local, documents, err = owner.scanLocalSelectedTree()
	if err != nil {
		return err
	}
	paths := unionSelectedPaths(base.Entries, local, remote)
	conflicted := false
	for _, relative := range paths {
		baseEntry, hadBase := base.Entries[relative]
		localFile, hasLocal := local[relative]
		remoteFile, hasRemote := remote[relative]
		localHash := ""
		if hasLocal {
			localHash = localFile.Hash
		}
		remoteHash := ""
		if hasRemote {
			remoteHash = remoteFile.Hash
		}
		baseHash := ""
		if hadBase {
			baseHash = baseEntry.Hash
		}
		localChanged := localHash != baseHash
		remoteChanged := remoteHash != baseHash
		switch {
		case !localChanged && !remoteChanged:
			continue
		case remoteChanged && !localChanged:
			if err := owner.ingestSelectedRemote(
				ctx,
				relative,
				remoteFile,
				hasRemote,
				baseEntry.DocumentID,
			); err != nil {
				return err
			}
		case localChanged && !remoteChanged:
			if err := publishSelectedLocal(
				remoteRoot,
				relative,
				localFile,
				hasLocal,
				remoteFile,
				hasRemote,
			); err != nil {
				return err
			}
		case localHash == remoteHash:
			continue
		default:
			conflicted = true
			if err := owner.persistSelectedConflict(
				ctx,
				relative,
				base.SnapshotID,
				baseEntry,
				localFile,
				hasLocal,
				remoteFile,
				hasRemote,
				documents,
			); err != nil {
				return err
			}
		}
	}
	if conflicted {
		return nil
	}
	local, documents, err = owner.scanLocalSelectedTree()
	if err != nil {
		return err
	}
	remote, err = scanSelectedTree(remoteRoot)
	if err != nil {
		return err
	}
	if !sameSelectedTrees(local, remote) {
		return errors.New("replica.selected_files_not_converged")
	}
	snapshotID, err := owner.latestOrProtectionSnapshot(ctx)
	if err != nil {
		return err
	}
	next := selectedFilesBase{
		FormatVersion: selectedFilesBaseFormat,
		WorkspaceID:   owner.runtime.manifest.WorkspaceID,
		SnapshotID:    snapshotID,
		Entries:       map[string]selectedFileVersion{},
		UpdatedAt:     time.Now().UTC(),
	}
	for relative, file := range local {
		document := documents[relative]
		next.Entries[relative] = selectedFileVersion{
			Hash:       file.Hash,
			ObjectID:   effectiveObjectID(document),
			DocumentID: document.DocumentID,
		}
	}
	return owner.storeSelectedFilesBase(next)
}

func (owner *productionReplicaConflict) scanLocalSelectedTree() (
	map[string]scannedSelectedFile,
	map[string]filehistory.Document,
	error,
) {
	files, err := scanSelectedTree(owner.activityFilesRoot)
	if err != nil {
		return nil, nil, err
	}
	documents := map[string]filehistory.Document{}
	for _, document := range owner.runtime.history.List() {
		if document.Status == filehistory.DocumentActive {
			documents[filepath.ToSlash(document.RelativePath)] = document
		}
	}
	for relative := range files {
		if _, exists := documents[relative]; !exists {
			return nil, nil, errors.New(
				"replica.selected_files_identity_missing",
			)
		}
	}
	return files, documents, nil
}

func scanSelectedTree(root string) (
	map[string]scannedSelectedFile,
	error,
) {
	result := map[string]scannedSelectedFile{}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("replica.selected_files_root_invalid")
	}
	err = filepath.WalkDir(
		root,
		func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == root {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("replica.selected_files_symlink")
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				if err != nil {
					return err
				}
				return errors.New("replica.selected_files_entry_invalid")
			}
			const maximumSelectedFile = int64(512 << 20)
			if info.Size() > maximumSelectedFile {
				return errors.New("replica.selected_files_resource_limit")
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil || relative == "." ||
				strings.HasPrefix(relative, "..") ||
				filepath.IsAbs(relative) {
				return errors.New("replica.selected_files_path_invalid")
			}
			relative = filepath.ToSlash(relative)
			sum := sha256.Sum256(content)
			result[relative] = scannedSelectedFile{
				Path:    relative,
				Hash:    "sha256:" + hex.EncodeToString(sum[:]),
				Content: content,
			}
			return nil
		},
	)
	return result, err
}

func (owner *productionReplicaConflict) ingestSelectedRemote(
	ctx context.Context,
	relative string,
	remote scannedSelectedFile,
	exists bool,
	documentID string,
) error {
	token, _ := owner.runtime.coordinator.Current()
	if !exists {
		if documentID == "" {
			return nil
		}
		result, err := owner.runtime.ingestor.Ingest(
			ctx,
			filehistory.ExternalChange{
				Token:      token,
				Kind:       filehistory.ExternalDelete,
				DocumentID: documentID,
				SourcePath: relative,
				ExpectedEffectiveRevision: expectedEffective(
					owner.runtime.history,
					documentID,
				),
				CreatedBy: "replica-selected-root",
				DeviceID:  token.ClaimID,
			},
		)
		if err != nil {
			return err
		}
		if result.Confirmation != nil {
			return errors.New("replica.selected_files_identity_ambiguous")
		}
		return nil
	}
	result, err := owner.runtime.ingestor.Ingest(
		ctx,
		filehistory.ExternalChange{
			Token:           token,
			Kind:            filehistory.ExternalStableSave,
			DocumentID:      documentID,
			SourcePath:      relative,
			TargetPath:      relative,
			Content:         remote.Content,
			ContentProvided: true,
			RevisionKind:    filehistory.RevisionFormal,
			ExpectedEffectiveRevision: expectedEffective(
				owner.runtime.history,
				documentID,
			),
			MimeType:  mime.TypeByExtension(filepath.Ext(relative)),
			CreatedBy: "replica-selected-root",
			DeviceID:  token.ClaimID,
			Comment:   "Imported from selected mirrored location",
		},
	)
	if err != nil {
		return err
	}
	if result.Confirmation != nil {
		return errors.New("replica.selected_files_identity_ambiguous")
	}
	return nil
}

func expectedEffective(
	history *filehistory.Service,
	documentID string,
) *string {
	if documentID == "" {
		return nil
	}
	document, err := history.Inspect(documentID)
	if err != nil || document.EffectiveRevisionID == "" {
		return nil
	}
	value := document.EffectiveRevisionID
	return &value
}

func publishSelectedLocal(
	root string,
	relative string,
	local scannedSelectedFile,
	hasLocal bool,
	remote scannedSelectedFile,
	hasRemote bool,
) error {
	target := filepath.Join(root, filepath.FromSlash(relative))
	if !pathWithin(root, target) {
		return errors.New("replica.selected_files_path_invalid")
	}
	if !hasLocal {
		if !hasRemote {
			return nil
		}
		current, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(current)
		if "sha256:"+hex.EncodeToString(sum[:]) != remote.Hash {
			return errors.New("replica.selected_files_changed")
		}
		return os.Remove(target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	temp := filepath.Join(
		filepath.Dir(target),
		"."+filepath.Base(target)+"."+uuid.NewString()+".tmp",
	)
	file, err := os.OpenFile(
		temp,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(temp)
		}
	}()
	if _, err := file.Write(local.Content); err != nil {
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
	if err := replaceGrantedFile(temp, target); err != nil {
		return err
	}
	remove = false
	return nil
}

func (owner *productionReplicaConflict) reconcileSelectedRenames(
	ctx context.Context,
	base selectedFilesBase,
	local map[string]scannedSelectedFile,
	remote map[string]scannedSelectedFile,
	documents map[string]filehistory.Document,
) error {
	for oldPath, entry := range base.Entries {
		if _, remoteStillExists := remote[oldPath]; remoteStillExists {
			continue
		}
		localOld, localStillExists := local[oldPath]
		if !localStillExists || localOld.Hash != entry.Hash {
			continue
		}
		for newPath, remoteNew := range remote {
			if _, existed := base.Entries[newPath]; existed ||
				remoteNew.Hash != entry.Hash {
				continue
			}
			token, _ := owner.runtime.coordinator.Current()
			result, err := owner.runtime.ingestor.Ingest(
				ctx,
				filehistory.ExternalChange{
					Token:      token,
					Kind:       filehistory.ExternalRename,
					DocumentID: entry.DocumentID,
					SourcePath: oldPath,
					TargetPath: newPath,
					ExpectedEffectiveRevision: expectedEffective(
						owner.runtime.history,
						entry.DocumentID,
					),
					CreatedBy: "replica-selected-root",
					DeviceID:  token.ClaimID,
				},
			)
			if err != nil {
				return err
			}
			if result.Confirmation != nil {
				return errors.New(
					"replica.selected_files_identity_ambiguous",
				)
			}
			delete(documents, oldPath)
			return nil
		}
	}
	return nil
}

func (owner *productionReplicaConflict) persistSelectedConflict(
	ctx context.Context,
	relative string,
	baseSnapshotID string,
	base selectedFileVersion,
	local scannedSelectedFile,
	hasLocal bool,
	remote scannedSelectedFile,
	hasRemote bool,
	documents map[string]filehistory.Document,
) error {
	document := documents[relative]
	documentID := base.DocumentID
	if documentID == "" {
		documentID = document.DocumentID
	}
	if documentID == "" {
		documentID = uuid.NewSHA1(
			uuid.NameSpaceURL,
			[]byte(owner.runtime.manifest.WorkspaceID+"\x00"+relative),
		).String()
	}
	conflictID := uuid.NewSHA1(
		uuid.NameSpaceOID,
		[]byte(
			owner.runtime.manifest.WorkspaceID+"\x00"+
				relative+"\x00"+base.Hash+"\x00"+
				local.Hash+"\x00"+remote.Hash,
		),
	).String()
	if _, err := owner.conflicts.Inspect(ctx, conflictID); err == nil {
		return nil
	} else if !errors.Is(err, conflictresolution.ErrConflictNotFound) {
		return err
	}
	token, _ := owner.runtime.coordinator.Current()
	var remoteObject objectrepo.ObjectID
	if hasRemote {
		receipt, err := owner.runtime.repository.Commit(
			ctx,
			objectrepo.CommitRequest{
				Authority: token.Authority(),
				Objects: []objectrepo.ObjectInput{{
					Name:    "selected-conflict:" + conflictID,
					Content: remote.Content,
				}},
			},
		)
		if err != nil {
			return err
		}
		remoteObject = receipt.Objects["selected-conflict:"+conflictID]
	}
	protection, err := owner.runtime.protectionSnapshotForOperation(
		ctx,
		token,
		conflictID,
		"replica.selectedFilesConflict",
	)
	if err != nil {
		return err
	}
	roots := append([]objectrepo.ObjectID(nil), protection.Objects...)
	if base.ObjectID != "" {
		roots = append(roots, base.ObjectID)
	}
	localObject := effectiveObjectID(document)
	if localObject != "" {
		roots = append(roots, localObject)
	}
	if remoteObject != "" {
		roots = append(roots, remoteObject)
	}
	pin, err := owner.runtime.repository.Pin(
		ctx,
		token.Authority(),
		roots,
		"conflict:"+conflictID,
		nil,
	)
	if err != nil {
		return err
	}
	if baseSnapshotID == "" {
		return errors.New("replica.selected_files_base_snapshot_missing")
	}
	set := conflictresolution.Set{
		ConflictID:  conflictID,
		WorkspaceID: owner.runtime.manifest.WorkspaceID,
		State:       conflictresolution.StatePending,
		Revision:    1,
		Base: conflictresolution.Candidate{
			SnapshotID: baseSnapshotID,
			Files: map[string]conflictresolution.FileState{
				documentID: {
					DocumentID: documentID,
					Path:       relative,
					ContentID:  string(base.ObjectID),
					Deleted:    base.Hash == "",
				},
			},
		},
		Local: conflictresolution.Candidate{
			SnapshotID: protection.SnapshotID,
			Revision:   protection.CatalogRevision,
			Files: map[string]conflictresolution.FileState{
				documentID: {
					DocumentID: documentID,
					Path:       relative,
					ContentID:  string(localObject),
					Deleted:    !hasLocal,
				},
			},
		},
		Replica: conflictresolution.Candidate{
			SnapshotID: uuid.NewSHA1(
				uuid.NameSpaceX500,
				[]byte(conflictID+"\x00replica"),
			).String(),
			Revision: 1,
			Files: map[string]conflictresolution.FileState{
				documentID: {
					DocumentID: documentID,
					Path:       relative,
					ContentID:  string(remoteObject),
					Deleted:    !hasRemote,
				},
			},
		},
		Dependencies: conflictresolution.DependencyGraph{
			Complete: true,
			Edges:    map[string][]string{documentID: {}},
		},
		RootPinIDs: []string{pin.PinID},
		CreatedAt:  time.Now().UTC(),
	}
	if err := owner.conflicts.Add(ctx, set); err != nil {
		return err
	}
	return nil
}

func (owner *productionReplicaConflict) latestOrProtectionSnapshot(
	ctx context.Context,
) (string, error) {
	records, err := owner.runtime.catalog.List(
		ctx,
		owner.runtime.manifest.WorkspaceID,
	)
	if err != nil {
		return "", err
	}
	if len(records) > 0 {
		sort.Slice(records, func(i, j int) bool {
			return records[i].CatalogRevision <
				records[j].CatalogRevision
		})
		return records[len(records)-1].SnapshotID, nil
	}
	token, _ := owner.runtime.coordinator.Current()
	operationID := uuid.NewSHA1(
		uuid.NameSpaceOID,
		[]byte(owner.runtime.manifest.WorkspaceID+"\x00selected-files-base"),
	).String()
	record, err := owner.runtime.protectionSnapshotForOperation(
		ctx,
		token,
		operationID,
		"replica.selectedFilesBaseline",
	)
	return record.SnapshotID, err
}

func (owner *productionReplicaConflict) loadSelectedFilesBase() (
	selectedFilesBase,
	bool,
	error,
) {
	raw, err := os.ReadFile(owner.selectedFilesBasePath)
	if errors.Is(err, os.ErrNotExist) {
		return selectedFilesBase{}, false, nil
	}
	if err != nil {
		return selectedFilesBase{}, false, err
	}
	var result selectedFilesBase
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil ||
		decoder.Decode(&struct{}{}) != io.EOF ||
		result.FormatVersion != selectedFilesBaseFormat ||
		result.WorkspaceID != owner.runtime.manifest.WorkspaceID ||
		result.Entries == nil {
		return selectedFilesBase{}, false,
			errors.New("replica.selected_files_base_corrupt")
	}
	return result, true, nil
}

func (owner *productionReplicaConflict) storeSelectedFilesBase(
	state selectedFilesBase,
) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	path := owner.selectedFilesBasePath
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
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(temp)
		}
	}()
	if _, err := file.Write(raw); err != nil {
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
	if err := replaceGrantedFile(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	remove = false
	return nil
}

func unionSelectedPaths(
	base map[string]selectedFileVersion,
	local map[string]scannedSelectedFile,
	remote map[string]scannedSelectedFile,
) []string {
	set := map[string]struct{}{}
	for value := range base {
		set[value] = struct{}{}
	}
	for value := range local {
		set[value] = struct{}{}
	}
	for value := range remote {
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sameSelectedTrees(
	left map[string]scannedSelectedFile,
	right map[string]scannedSelectedFile,
) bool {
	if len(left) != len(right) {
		return false
	}
	for path, value := range left {
		if right[path].Hash != value.Hash {
			return false
		}
	}
	return true
}

func effectiveObjectID(
	document filehistory.Document,
) objectrepo.ObjectID {
	for _, revision := range document.Revisions {
		if revision.RevisionID == document.EffectiveRevisionID {
			return revision.ObjectID
		}
	}
	return ""
}

func pathWithin(root string, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}
