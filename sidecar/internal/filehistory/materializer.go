package filehistory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const materializerJournalVersion = 1

var (
	ErrMaterializerCorrupt = errors.New("filehistory.materializer_corrupt")
	ErrUnsafeFilePath      = errors.New("filehistory.unsafe_file_path")
)

type materializerJournal struct {
	Version          int                     `json:"version"`
	WorkspaceID      string                  `json:"workspaceId"`
	MutationRevision uint64                  `json:"mutationRevision"`
	SessionEpoch     uint64                  `json:"sessionEpoch"`
	FenceEpoch       uint64                  `json:"fenceEpoch"`
	ClaimID          string                  `json:"claimId"`
	State            string                  `json:"state"`
	Transaction      string                  `json:"transaction"`
	Operations       []materializerOperation `json:"operations"`
}

type materializerOperation struct {
	Path        string `json:"path"`
	Desired     bool   `json:"desired"`
	Existed     bool   `json:"existed"`
	Stage       string `json:"stage,omitempty"`
	Backup      string `json:"backup,omitempty"`
	ObjectID    string `json:"objectId,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

type materializedLeaf struct {
	ObjectID    objectrepo.ObjectID
	ContentHash string
	Size        int64
}

// Materializer makes the authoritative effective leaf of every active
// document visible under files/. Its journal is deliberately independent from
// the topology head so startup can prove which side of the commit boundary won.
type Materializer struct {
	filesRoot   string
	journalRoot string
	journalPath string
	repository  objectrepo.Repository
}

func OpenMaterializer(
	filesRoot string,
	journalRoot string,
	repository objectrepo.Repository,
) (*Materializer, error) {
	if repository == nil {
		return nil, errors.New("filehistory.materializer_repository_required")
	}
	filesRoot, err := cleanAbsoluteRoot(filesRoot)
	if err != nil {
		return nil, err
	}
	journalRoot, err = cleanAbsoluteRoot(journalRoot)
	if err != nil {
		return nil, err
	}
	if filesRoot == journalRoot || pathWithin(filesRoot, journalRoot) ||
		pathWithin(journalRoot, filesRoot) {
		return nil, errors.New("filehistory.materializer_roots_overlap")
	}
	if err := ensureSafeDirectory(filesRoot); err != nil {
		return nil, err
	}
	if err := ensureSafeDirectory(journalRoot); err != nil {
		return nil, err
	}
	return &Materializer{
		filesRoot:   filesRoot,
		journalRoot: journalRoot,
		journalPath: filepath.Join(journalRoot, "journal.json"),
		repository:  repository,
	}, nil
}

func (materializer *Materializer) PrepareAndApply(
	ctx context.Context,
	intent writecoordinator.WriteIntent,
	current map[string]Document,
	next map[string]Document,
) error {
	if materializer == nil {
		return nil
	}
	if _, err := os.Lstat(materializer.journalPath); err == nil {
		return errors.Join(ErrMaterializerCorrupt, errors.New("journal already exists"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	operations, err := materializer.plan(current, next)
	if err != nil || len(operations) == 0 {
		return err
	}
	transaction := fmt.Sprintf("txn-%020d", intent.MutationRevision)
	transactionPath, err := materializer.transactionPath(transaction)
	if err != nil {
		return err
	}
	if err := os.Mkdir(transactionPath, 0o700); err != nil {
		return err
	}
	journal := materializerJournal{
		Version:          materializerJournalVersion,
		WorkspaceID:      intent.Token.WorkspaceID,
		MutationRevision: intent.MutationRevision,
		SessionEpoch:     intent.Token.SessionEpoch,
		FenceEpoch:       intent.Token.FenceEpoch,
		ClaimID:          intent.Token.ClaimID,
		State:            "prepared",
		Transaction:      transaction,
		Operations:       operations,
	}
	if err := materializer.stage(ctx, transactionPath, &journal); err != nil {
		_ = removeTransaction(transactionPath)
		return err
	}
	if err := materializer.writeJournal(journal); err != nil {
		_ = removeTransaction(transactionPath)
		return err
	}
	if err := materializer.apply(transactionPath, journal); err != nil {
		rollbackErr := materializer.rollback(transactionPath, journal)
		return errors.Join(err, rollbackErr)
	}
	journal.State = "applied"
	if err := materializer.writeJournal(journal); err != nil {
		rollbackErr := materializer.rollback(transactionPath, journal)
		return errors.Join(err, rollbackErr)
	}
	return nil
}

func (materializer *Materializer) Rollback(
	mutationRevision uint64,
) error {
	if materializer == nil {
		return nil
	}
	journal, found, err := materializer.loadJournal()
	if err != nil || !found {
		return err
	}
	if journal.MutationRevision != mutationRevision {
		return ErrMaterializerCorrupt
	}
	transactionPath, err := materializer.transactionPath(journal.Transaction)
	if err != nil {
		return err
	}
	return materializer.rollback(transactionPath, journal)
}

func (materializer *Materializer) Finalize(
	mutationRevision uint64,
) error {
	if materializer == nil {
		return nil
	}
	journal, found, err := materializer.loadJournal()
	if err != nil || !found {
		return err
	}
	if journal.MutationRevision != mutationRevision || journal.State != "applied" {
		return ErrMaterializerCorrupt
	}
	transactionPath, err := materializer.transactionPath(journal.Transaction)
	if err != nil {
		return err
	}
	return materializer.removeJournalAndTransaction(transactionPath)
}

func (materializer *Materializer) Recover(
	head CurrentHead,
	found bool,
) error {
	if materializer == nil {
		return nil
	}
	journal, journalFound, err := materializer.loadJournal()
	if err != nil || !journalFound {
		return err
	}
	transactionPath, err := materializer.transactionPath(journal.Transaction)
	if err != nil {
		return err
	}
	committed := found &&
		head.WorkspaceID == journal.WorkspaceID &&
		head.MutationRevision == journal.MutationRevision &&
		head.SessionEpoch == journal.SessionEpoch &&
		head.FenceEpoch == journal.FenceEpoch &&
		head.ClaimID == journal.ClaimID
	if committed {
		if journal.State != "applied" {
			return ErrMaterializerCorrupt
		}
		return materializer.removeJournalAndTransaction(transactionPath)
	}
	return materializer.rollback(transactionPath, journal)
}

func (materializer *Materializer) plan(
	current map[string]Document,
	next map[string]Document,
) ([]materializerOperation, error) {
	currentLeaves, err := activeLeaves(current)
	if err != nil {
		return nil, err
	}
	nextLeaves, err := activeLeaves(next)
	if err != nil {
		return nil, err
	}
	pathSet := make(map[string]struct{}, len(currentLeaves)+len(nextLeaves))
	for relative := range currentLeaves {
		pathSet[relative] = struct{}{}
	}
	for relative := range nextLeaves {
		pathSet[relative] = struct{}{}
	}
	paths := make([]string, 0, len(pathSet))
	for relative := range pathSet {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	operations := make([]materializerOperation, 0, len(paths))
	for _, relative := range paths {
		oldLeaf, oldExists := currentLeaves[relative]
		newLeaf, newExists := nextLeaves[relative]
		if oldExists && newExists && oldLeaf == newLeaf {
			matches, matchErr := materializer.diskLeafMatches(
				relative,
				newLeaf,
			)
			if matchErr != nil {
				return nil, matchErr
			}
			if matches {
				continue
			}
		}
		operation := materializerOperation{
			Path:    relative,
			Desired: newExists,
		}
		if newExists {
			operation.ObjectID = string(newLeaf.ObjectID)
			operation.ContentHash = newLeaf.ContentHash
			operation.Size = newLeaf.Size
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func (materializer *Materializer) diskLeafMatches(
	relative string,
	leaf materializedLeaf,
) (bool, error) {
	target, err := materializer.targetPath(relative)
	if err != nil {
		return false, err
	}
	info, err := safeLstat(target, materializer.filesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != leaf.Size {
		if !info.Mode().IsRegular() {
			return false, ErrUnsafeFilePath
		}
		return false, nil
	}
	file, err := os.Open(target)
	if err != nil {
		return false, err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(
		hasher,
		io.LimitReader(file, leaf.Size+1),
	)
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return false, err
	}
	actual := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	return written == leaf.Size && actual == leaf.ContentHash, nil
}

func activeLeaves(
	documents map[string]Document,
) (map[string]materializedLeaf, error) {
	result := make(map[string]materializedLeaf)
	for _, document := range documents {
		if document.Status != DocumentActive {
			continue
		}
		relative, err := normalizePath(document.RelativePath)
		if err != nil || relative != document.RelativePath {
			return nil, ErrUnsafeFilePath
		}
		revision := revisionByID(document, document.EffectiveRevisionID)
		if revision == nil {
			return nil, ErrStateCorrupt
		}
		if _, exists := result[relative]; exists {
			return nil, ErrPathConflict
		}
		result[relative] = materializedLeaf{
			ObjectID: revision.ObjectID, ContentHash: revision.ContentHash,
			Size: revision.Size,
		}
	}
	return result, nil
}

func (materializer *Materializer) stage(
	ctx context.Context,
	transactionPath string,
	journal *materializerJournal,
) error {
	for index := range journal.Operations {
		operation := &journal.Operations[index]
		target, err := materializer.targetPath(operation.Path)
		if err != nil {
			return err
		}
		info, err := safeLstat(target, materializer.filesRoot)
		switch {
		case err == nil:
			if !info.Mode().IsRegular() {
				return ErrUnsafeFilePath
			}
			operation.Existed = true
			operation.Backup = fmt.Sprintf("backup-%06d", index)
			if err := copyRegularFile(
				target,
				filepath.Join(transactionPath, operation.Backup),
				-1,
				"",
			); err != nil {
				return err
			}
		case errors.Is(err, os.ErrNotExist):
		default:
			return err
		}
		if operation.Desired {
			operation.Stage = fmt.Sprintf("stage-%06d", index)
			reader, err := materializer.repository.Open(
				ctx, objectrepo.ObjectID(operation.ObjectID),
			)
			if err != nil {
				return err
			}
			stagePath := filepath.Join(transactionPath, operation.Stage)
			copyErr := copyReaderVerified(
				reader, stagePath, operation.Size, operation.ContentHash,
			)
			closeErr := reader.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
		}
	}
	return syncDirectoryDurable(transactionPath)
}

func (materializer *Materializer) apply(
	transactionPath string,
	journal materializerJournal,
) error {
	for _, operation := range journal.Operations {
		target, err := materializer.targetPath(operation.Path)
		if err != nil {
			return err
		}
		if operation.Desired {
			if err := ensureSafeParent(materializer.filesRoot, target); err != nil {
				return err
			}
			stage := filepath.Join(transactionPath, operation.Stage)
			if err := replaceMaterializedFile(stage, target); err != nil {
				return err
			}
		} else {
			info, err := safeLstat(target, materializer.filesRoot)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return ErrUnsafeFilePath
			}
			if err := os.Remove(target); err != nil {
				return err
			}
			if err := syncDirectoryDurable(filepath.Dir(target)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (materializer *Materializer) rollback(
	transactionPath string,
	journal materializerJournal,
) error {
	for index := len(journal.Operations) - 1; index >= 0; index-- {
		operation := journal.Operations[index]
		target, err := materializer.targetPath(operation.Path)
		if err != nil {
			return err
		}
		if operation.Existed {
			if err := ensureSafeParent(materializer.filesRoot, target); err != nil {
				return err
			}
			backup := filepath.Join(transactionPath, operation.Backup)
			restoreCopy := filepath.Join(
				transactionPath,
				fmt.Sprintf("restore-%06d", index),
			)
			if err := copyRegularFile(backup, restoreCopy, -1, ""); err != nil {
				return err
			}
			if err := replaceMaterializedFile(restoreCopy, target); err != nil {
				return err
			}
			continue
		}
		info, err := safeLstat(target, materializer.filesRoot)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrUnsafeFilePath
		}
		if err := os.Remove(target); err != nil {
			return err
		}
		if err := syncDirectoryDurable(filepath.Dir(target)); err != nil {
			return err
		}
	}
	return materializer.removeJournalAndTransaction(transactionPath)
}

func (materializer *Materializer) targetPath(relative string) (string, error) {
	normalized, err := normalizePath(relative)
	if err != nil || normalized != relative {
		return "", ErrUnsafeFilePath
	}
	target := filepath.Join(
		materializer.filesRoot,
		filepath.FromSlash(normalized),
	)
	if !pathWithin(materializer.filesRoot, target) {
		return "", ErrUnsafeFilePath
	}
	return target, nil
}

func (materializer *Materializer) transactionPath(
	transaction string,
) (string, error) {
	if !strings.HasPrefix(transaction, "txn-") ||
		filepath.Base(transaction) != transaction {
		return "", ErrMaterializerCorrupt
	}
	result := filepath.Join(materializer.journalRoot, transaction)
	if !pathWithin(materializer.journalRoot, result) {
		return "", ErrMaterializerCorrupt
	}
	return result, nil
}

func (materializer *Materializer) writeJournal(
	journal materializerJournal,
) error {
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	temp := materializer.journalPath + ".tmp"
	file, err := os.OpenFile(
		temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600,
	)
	if errors.Is(err, os.ErrExist) {
		if removeErr := os.Remove(temp); removeErr != nil {
			return errors.Join(err, removeErr)
		}
		file, err = os.OpenFile(
			temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600,
		)
	}
	if err != nil {
		return err
	}
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := replaceMaterializedFile(temp, materializer.journalPath); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

func (materializer *Materializer) loadJournal() (materializerJournal, bool, error) {
	raw, err := os.ReadFile(materializer.journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return materializerJournal{}, false, nil
	}
	if err != nil {
		return materializerJournal{}, false, err
	}
	var journal materializerJournal
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return materializerJournal{}, false, ErrMaterializerCorrupt
	}
	if journal.Version != materializerJournalVersion ||
		!validUUID(journal.WorkspaceID) ||
		journal.MutationRevision == 0 ||
		journal.SessionEpoch == 0 ||
		journal.FenceEpoch == 0 ||
		strings.TrimSpace(journal.ClaimID) == "" ||
		(journal.State != "prepared" && journal.State != "applied") ||
		len(journal.Operations) == 0 {
		return materializerJournal{}, false, ErrMaterializerCorrupt
	}
	for _, operation := range journal.Operations {
		if _, err := materializer.targetPath(operation.Path); err != nil {
			return materializerJournal{}, false, ErrMaterializerCorrupt
		}
		if operation.Desired &&
			(operation.Stage == "" || operation.ObjectID == "" ||
				operation.ContentHash == "" || operation.Size < 0) {
			return materializerJournal{}, false, ErrMaterializerCorrupt
		}
		if operation.Existed && operation.Backup == "" {
			return materializerJournal{}, false, ErrMaterializerCorrupt
		}
	}
	return journal, true, nil
}

func (materializer *Materializer) removeJournalAndTransaction(
	transactionPath string,
) error {
	if !pathWithin(materializer.journalRoot, transactionPath) {
		return ErrMaterializerCorrupt
	}
	removeJournalErr := os.Remove(materializer.journalPath)
	if errors.Is(removeJournalErr, os.ErrNotExist) {
		removeJournalErr = nil
	}
	removeTransactionErr := removeTransaction(transactionPath)
	syncErr := syncDirectoryDurable(materializer.journalRoot)
	return errors.Join(removeJournalErr, removeTransactionErr, syncErr)
}

func copyReaderVerified(
	reader io.Reader,
	destination string,
	expectedSize int64,
	expectedHash string,
) error {
	if expectedSize < 0 {
		return ErrStateCorrupt
	}
	file, err := os.OpenFile(
		destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600,
	)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(
		io.MultiWriter(file, hasher),
		io.LimitReader(reader, expectedSize+1),
	)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		_ = os.Remove(destination)
		return err
	}
	actualHash := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if written != expectedSize || actualHash != expectedHash {
		_ = os.Remove(destination)
		return ErrStateCorrupt
	}
	return nil
}

func copyRegularFile(
	source string,
	destination string,
	maxSize int64,
	expectedHash string,
) error {
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	info, err := sourceFile.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = sourceFile.Close()
		return errors.Join(ErrUnsafeFilePath, err)
	}
	if maxSize < 0 {
		maxSize = info.Size()
	}
	err = copyReaderVerified(
		sourceFile,
		destination,
		min(info.Size(), maxSize),
		hashOrComputed(expectedHash, source),
	)
	closeErr := sourceFile.Close()
	return errors.Join(err, closeErr)
}

func hashOrComputed(expected string, source string) string {
	if expected != "" {
		return expected
	}
	file, err := os.Open(source)
	if err != nil {
		return ""
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return ""
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

func cleanAbsoluteRoot(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", ErrUnsafeFilePath
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func pathWithin(root string, candidate string) bool {
	relative, err := filepath.Rel(root, filepath.Clean(candidate))
	return err == nil && relative != "." &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}

func ensureSafeDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || pathHasReparsePoint(directory) {
		return errors.Join(ErrUnsafeFilePath, err)
	}
	return nil
}

func ensureSafeParent(root string, target string) error {
	parent := filepath.Dir(target)
	if !pathWithin(root, target) {
		return ErrUnsafeFilePath
	}
	relative, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	current := root
	for _, component := range strings.FieldsFunc(
		relative,
		func(value rune) bool {
			return value == '/' || value == '\\'
		},
	) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil || !info.IsDir() || pathHasReparsePoint(current) {
			return errors.Join(ErrUnsafeFilePath, err)
		}
	}
	return nil
}

func safeLstat(path string, root string) (os.FileInfo, error) {
	if !pathWithin(root, path) {
		return nil, ErrUnsafeFilePath
	}
	parent := filepath.Dir(path)
	if _, err := os.Lstat(parent); err == nil {
		if err := verifyExistingParents(root, parent); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err == nil && pathHasReparsePoint(path) {
		return nil, ErrUnsafeFilePath
	}
	return info, err
}

func verifyExistingParents(root string, parent string) error {
	current := parent
	for {
		if current == root {
			return nil
		}
		if !pathWithin(root, current) {
			return ErrUnsafeFilePath
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || pathHasReparsePoint(current) {
			return errors.Join(ErrUnsafeFilePath, err)
		}
		current = filepath.Dir(current)
	}
}

func removeTransaction(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || pathHasReparsePoint(path) {
		return errors.Join(ErrMaterializerCorrupt, err)
	}
	return os.RemoveAll(path)
}
