// Package filehistory owns the authoritative document topology and immutable
// revision tree. File bytes and tree roots are committed through objectrepo.
package filehistory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const (
	rootFormatVersion = 2
	contractVersion   = "2.0"
)

var (
	ErrDocumentNotFound     = errors.New("filehistory.document_not_found")
	ErrRevisionNotFound     = errors.New("filehistory.revision_not_found")
	ErrRevisionConflict     = errors.New("filehistory.revision_conflict")
	ErrNotLeaf              = errors.New("filehistory.revision_not_leaf")
	ErrHeadRecoveryUnproven = errors.New(
		"filehistory.head_recovery_unproven",
	)
	ErrDocumentDeleted = errors.New("filehistory.document_deleted")
	ErrPathInvalid     = errors.New("filehistory.path_invalid")
	ErrPathConflict    = errors.New("filehistory.path_conflict")
	ErrStateCorrupt    = errors.New("filehistory.state_corrupt")
	errNoOp            = errors.New("filehistory.no_op")
)

type RevisionKind string

const (
	RevisionAutosave RevisionKind = "autosave"
	RevisionFormal   RevisionKind = "formal"
	RevisionRestore  RevisionKind = "restore"
)

type Revision struct {
	ContractVersion        string              `json:"contractVersion"`
	RevisionID             string              `json:"revisionId"`
	DocumentID             string              `json:"documentId"`
	ParentRevisionID       *string             `json:"parentRevisionId"`
	RestoredFromRevisionID *string             `json:"restoredFromRevisionId"`
	Kind                   RevisionKind        `json:"kind"`
	RevisionOrdinal        uint64              `json:"revisionOrdinal"`
	FormalVersion          *uint64             `json:"formalVersion"`
	ObjectID               objectrepo.ObjectID `json:"objectId"`
	ContentHash            string              `json:"contentHash"`
	Size                   int64               `json:"size"`
	MimeType               string              `json:"mimeType"`
	CreatedAt              time.Time           `json:"createdAt"`
	CreatedBy              string              `json:"createdBy"`
	DeviceID               string              `json:"deviceId"`
	Comment                *string             `json:"comment"`
}

func (revision Revision) VersionLabel() string {
	if revision.FormalVersion == nil {
		return ""
	}
	return fmt.Sprintf("V%d", *revision.FormalVersion)
}

type DocumentStatus string

const (
	DocumentActive  DocumentStatus = "active"
	DocumentDeleted DocumentStatus = "deleted"
)

type Document struct {
	ContractVersion     string         `json:"contractVersion"`
	WorkspaceID         string         `json:"workspaceId"`
	DocumentID          string         `json:"documentId"`
	RelativePath        string         `json:"relativePath"`
	Status              DocumentStatus `json:"status"`
	TopologyRevision    uint64         `json:"topologyRevision"`
	EffectiveRevisionID string         `json:"effectiveRevisionId"`
	NextRevisionOrdinal uint64         `json:"nextRevisionOrdinal"`
	NextFormalVersion   uint64         `json:"nextFormalVersion"`
	Revisions           []Revision     `json:"revisions"`
}

type SaveRequest struct {
	Token                     writecoordinator.Token
	DocumentID                string
	Path                      string
	ParentRevisionID          string
	ExpectedEffectiveRevision *string
	Kind                      RevisionKind
	Content                   []byte
	MimeType                  string
	CreatedBy                 string
	DeviceID                  string
	Comment                   string
}

type SaveResult struct {
	Document         Document
	Revision         Revision
	Root             objectrepo.ManifestID
	MutationRevision uint64
	NoOp             bool
}

type RestoreRequest struct {
	Token                     writecoordinator.Token
	DocumentID                string
	TargetRevisionID          string
	ExpectedEffectiveRevision *string
	CreatedBy                 string
	DeviceID                  string
	Comment                   string
}

type MutationResult struct {
	Document         Document
	Root             objectrepo.ManifestID
	MutationRevision uint64
	NoOp             bool
}

// StagedSnapshotRestore is an immutable candidate publication. Creating it
// commits only repository objects/manifests; the authoritative head and the
// materialized files remain untouched until an offline restore installer
// publishes NextHead.
type StagedSnapshotRestore struct {
	PreviousHead CurrentHead
	NextHead     CurrentHead
	Documents    []Document
	Audit        auditledger.Envelope
}

type rootPayload struct {
	FormatVersion int        `json:"formatVersion"`
	WorkspaceID   string     `json:"workspaceId"`
	Documents     []Document `json:"documents"`
}

type Option func(*Service)

func WithClock(clock func() time.Time) Option {
	return func(service *Service) {
		if clock != nil {
			service.now = clock
		}
	}
}

func WithIDGenerator(generator func() (string, error)) Option {
	return func(service *Service) {
		if generator != nil {
			service.newID = generator
		}
	}
}

func WithHeadStore(store HeadStore) Option {
	return func(service *Service) {
		if store != nil {
			service.headStore = store
		}
	}
}

func WithMaterializer(materializer *Materializer) Option {
	return func(service *Service) {
		service.materializer = materializer
	}
}

type Service struct {
	repository   objectrepo.Repository
	coordinator  *writecoordinator.WorkspaceWriteCoordinator
	now          func() time.Time
	newID        func() (string, error)
	headStore    HeadStore
	materializer *Materializer

	mu                   sync.RWMutex
	documents            map[string]Document
	root                 objectrepo.ManifestID
	headRevision         uint64
	headMutationRevision uint64
	headSessionEpoch     uint64
	headFenceEpoch       uint64
	headClaimID          string
}

func New(
	repository objectrepo.Repository,
	coordinator *writecoordinator.WorkspaceWriteCoordinator,
	options ...Option,
) (*Service, error) {
	if repository == nil || coordinator == nil {
		return nil, errors.New("filehistory.dependencies_required")
	}
	service := &Service{
		repository:  repository,
		coordinator: coordinator,
		now:         func() time.Time { return time.Now().UTC() },
		newID:       randomRevisionID,
		headStore:   newMemoryHeadStore(),
		documents:   map[string]Document{},
	}
	for _, option := range options {
		option(service)
	}
	token, _ := coordinator.Current()
	if !validUUID(token.WorkspaceID) {
		return nil, errors.New("filehistory.workspace_id_invalid")
	}
	return service, nil
}

func Open(
	ctx context.Context,
	repository objectrepo.Repository,
	coordinator *writecoordinator.WorkspaceWriteCoordinator,
	root objectrepo.ManifestID,
	options ...Option,
) (*Service, error) {
	service, err := New(repository, coordinator, options...)
	if err != nil {
		return nil, err
	}
	record, err := repository.GetManifest(ctx, root)
	if err != nil {
		return nil, err
	}
	if record.Labels["type"] != "filehistory-root" {
		return nil, ErrStateCorrupt
	}
	var payload rootPayload
	if err := decodeStrict(record.Payload, &payload); err != nil {
		return nil, errors.Join(ErrStateCorrupt, err)
	}
	token, counters := coordinator.Current()
	if payload.FormatVersion != rootFormatVersion ||
		payload.WorkspaceID != token.WorkspaceID {
		return nil, ErrStateCorrupt
	}
	documents := make(map[string]Document, len(payload.Documents))
	for _, document := range payload.Documents {
		if document.WorkspaceID != token.WorkspaceID {
			return nil, ErrStateCorrupt
		}
		if err := validateDocument(document); err != nil {
			return nil, errors.Join(ErrStateCorrupt, err)
		}
		if _, exists := documents[document.DocumentID]; exists {
			return nil, ErrStateCorrupt
		}
		documents[document.DocumentID] = cloneDocument(document)
	}
	if err := validatePaths(documents); err != nil {
		return nil, errors.Join(ErrStateCorrupt, err)
	}
	roots := reachableObjects(documents)
	report, err := repository.Verify(ctx, roots)
	if err != nil {
		return nil, err
	}
	if !report.Valid {
		return nil, ErrStateCorrupt
	}
	if err := verifyRevisionObjects(
		ctx, repository, documents,
	); err != nil {
		return nil, err
	}
	service.documents = documents
	service.root = root
	head, found, err := service.headStore.Load(ctx, token.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if found {
		if head.Root != root {
			return nil, ErrHeadConflict
		}
		service.installHeadLocked(head)
	} else {
		if counters.MutationRevision == 0 {
			return nil, ErrStateCorrupt
		}
		nextHead := CurrentHead{
			WorkspaceID:      token.WorkspaceID,
			Root:             root,
			Revision:         1,
			MutationRevision: counters.MutationRevision,
			SessionEpoch:     token.SessionEpoch,
			FenceEpoch:       token.FenceEpoch,
			ClaimID:          token.ClaimID,
		}
		head, err = service.headStore.CompareAndSwap(
			ctx,
			CurrentHead{WorkspaceID: token.WorkspaceID},
			nextHead,
		)
		if err != nil {
			return nil, err
		}
		service.installHeadLocked(head)
	}
	return service, nil
}

func OpenCurrent(
	ctx context.Context,
	repository objectrepo.Repository,
	coordinator *writecoordinator.WorkspaceWriteCoordinator,
	headStore HeadStore,
	options ...Option,
) (*Service, error) {
	if repository == nil || coordinator == nil || headStore == nil {
		return nil, errors.New("filehistory.head_store_required")
	}
	recovery := coordinator.RecoveryState()
	if !validUUID(recovery.Token.WorkspaceID) {
		return nil, errors.New("filehistory.workspace_id_invalid")
	}
	head, found, err := headStore.Load(ctx, recovery.Token.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var materializer *Materializer
	probe, err := New(repository, coordinator, options...)
	if err != nil {
		return nil, err
	}
	materializer = probe.materializer
	if materializer != nil {
		if err := materializer.Recover(head, found); err != nil {
			return nil, err
		}
	}
	if recovery.PendingMutationRevision != 0 {
		if !found ||
			head.MutationRevision != recovery.PendingMutationRevision ||
			head.WorkspaceID != recovery.Token.WorkspaceID ||
			head.SessionEpoch != recovery.Token.SessionEpoch ||
			head.FenceEpoch != recovery.Token.FenceEpoch ||
			head.ClaimID != recovery.Token.ClaimID {
			return nil, fmt.Errorf(
				"%w: mutationRevision=%d",
				errors.Join(
					ErrHeadRecoveryUnproven,
					writecoordinator.ErrRecoveryRequired,
				),
				recovery.PendingMutationRevision,
			)
		}
		if err := coordinator.ResolvePreparedMutation(
			ctx,
			recovery.Token,
			recovery.PendingMutationRevision,
			true,
		); err != nil {
			return nil, err
		}
	}
	options = append(options, WithHeadStore(headStore))
	if !found {
		return New(repository, coordinator, options...)
	}
	return Open(
		ctx,
		repository,
		coordinator,
		head.Root,
		options...,
	)
}

func (service *Service) Save(
	ctx context.Context,
	request SaveRequest,
) (SaveResult, error) {
	if err := validateSaveRequest(request); err != nil {
		return SaveResult{}, errors.New("filehistory.save_invalid")
	}
	var (
		result           SaveResult
		pendingDocuments map[string]Document
		pendingHead      CurrentHead
		stateLocked      bool
	)
	receipt, err := service.coordinator.Write(
		ctx,
		request.Token,
		func(ctx context.Context, intent writecoordinator.WriteIntent) error {
			service.mu.Lock()
			stateLocked = true

			next := cloneDocuments(service.documents)
			document, exists := next[request.DocumentID]
			if !exists {
				normalized, err := normalizePath(request.Path)
				if err != nil {
					return err
				}
				if request.ParentRevisionID != "" {
					return ErrRevisionNotFound
				}
				document = Document{
					ContractVersion:     contractVersion,
					WorkspaceID:         request.Token.WorkspaceID,
					DocumentID:          request.DocumentID,
					RelativePath:        normalized,
					Status:              DocumentActive,
					TopologyRevision:    1,
					NextRevisionOrdinal: 1,
					NextFormalVersion:   1,
					Revisions:           []Revision{},
				}
			} else {
				if document.Status == DocumentDeleted {
					return ErrDocumentDeleted
				}
				if err := requireExpected(document, request.ExpectedEffectiveRevision); err != nil {
					return err
				}
				if request.Path != "" &&
					request.Path != document.RelativePath {
					return ErrPathConflict
				}
			}
			if err := pathAvailable(
				next, document.DocumentID, document.RelativePath,
			); err != nil {
				return err
			}

			parentID := request.ParentRevisionID
			if exists && parentID == "" {
				parentID = document.EffectiveRevisionID
			}
			var parent *Revision
			if parentID != "" {
				found := revisionByID(document, parentID)
				if found == nil {
					return ErrRevisionNotFound
				}
				parent = found
			}
			contentID := contentObjectID(request.Content)
			if parent != nil &&
				parent.RevisionID == document.EffectiveRevisionID &&
				parent.ContentHash == contentHash(request.Content) &&
				request.Kind == RevisionAutosave {
				result = SaveResult{
					Document:         cloneDocument(document),
					Revision:         *cloneRevision(parent),
					Root:             service.root,
					MutationRevision: intent.MutationRevision - 1,
					NoOp:             true,
				}
				return errNoOp
			}

			revisionID, err := service.newID()
			if err != nil || !validUUID(revisionID) {
				if err == nil {
					err = errors.New("invalid revision id")
				}
				return fmt.Errorf("create file revision id: %w", err)
			}
			if revisionByID(document, revisionID) != nil {
				return errors.New("filehistory.revision_id_conflict")
			}
			revision := Revision{
				ContractVersion: contractVersion,
				RevisionID:      revisionID,
				DocumentID:      document.DocumentID,
				Kind:            request.Kind,
				RevisionOrdinal: document.NextRevisionOrdinal,
				ObjectID:        contentID,
				ContentHash:     contentHash(request.Content),
				Size:            int64(len(request.Content)),
				MimeType:        request.MimeType,
				CreatedAt:       service.now().UTC(),
				CreatedBy:       request.CreatedBy,
				DeviceID:        request.DeviceID,
				Comment:         optionalString(request.Comment),
			}
			document.NextRevisionOrdinal++
			if parent != nil {
				revision.ParentRevisionID = stringPointer(parent.RevisionID)
			}
			if request.Kind == RevisionFormal {
				revision.FormalVersion = uint64Pointer(
					document.NextFormalVersion,
				)
				document.NextFormalVersion++
			}
			document.Revisions = append(document.Revisions, revision)
			document.EffectiveRevisionID = revision.RevisionID
			next[document.DocumentID] = document

			head, err := service.commit(
				ctx,
				intent,
				next,
				&objectrepo.ObjectInput{Name: "content", Content: request.Content},
			)
			if err != nil {
				return err
			}
			pendingDocuments = next
			pendingHead = head
			result = SaveResult{
				Document: cloneDocument(document),
				Revision: revision,
				Root:     head.Root,
			}
			return nil
		},
	)
	if stateLocked {
		defer service.mu.Unlock()
	}
	if errors.Is(err, errNoOp) {
		return result, nil
	}
	if err != nil {
		return SaveResult{}, err
	}
	service.documents = pendingDocuments
	service.installHeadLocked(pendingHead)
	result.MutationRevision = receipt.MutationRevision
	if service.materializer != nil {
		_ = service.materializer.Finalize(receipt.MutationRevision)
	}
	return result, nil
}

func (service *Service) Restore(
	ctx context.Context,
	request RestoreRequest,
) (SaveResult, error) {
	if !validUUID(request.DocumentID) ||
		!validUUID(request.TargetRevisionID) ||
		(request.ExpectedEffectiveRevision != nil &&
			!validUUID(*request.ExpectedEffectiveRevision)) ||
		strings.TrimSpace(request.CreatedBy) == "" ||
		!validUUID(request.DeviceID) {
		return SaveResult{}, errors.New("filehistory.restore_invalid")
	}
	var (
		result           SaveResult
		pendingDocuments map[string]Document
		pendingHead      CurrentHead
		stateLocked      bool
	)
	receipt, err := service.coordinator.Write(
		ctx,
		request.Token,
		func(ctx context.Context, intent writecoordinator.WriteIntent) error {
			service.mu.Lock()
			stateLocked = true

			next := cloneDocuments(service.documents)
			document, exists := next[request.DocumentID]
			if !exists {
				return ErrDocumentNotFound
			}
			if document.Status == DocumentDeleted {
				return ErrDocumentDeleted
			}
			if err := requireExpected(document, request.ExpectedEffectiveRevision); err != nil {
				return err
			}
			target := revisionByID(document, request.TargetRevisionID)
			if target == nil {
				return ErrRevisionNotFound
			}
			parent := revisionByID(document, document.EffectiveRevisionID)
			if parent == nil {
				return ErrStateCorrupt
			}
			revisionID, err := service.newID()
			if err != nil || !validUUID(revisionID) {
				if err == nil {
					err = errors.New("invalid revision id")
				}
				return fmt.Errorf("create restore revision id: %w", err)
			}
			if revisionByID(document, revisionID) != nil {
				return errors.New("filehistory.revision_id_conflict")
			}
			revision := Revision{
				ContractVersion:        contractVersion,
				RevisionID:             revisionID,
				DocumentID:             document.DocumentID,
				ParentRevisionID:       stringPointer(parent.RevisionID),
				RestoredFromRevisionID: stringPointer(target.RevisionID),
				Kind:                   RevisionRestore,
				RevisionOrdinal:        document.NextRevisionOrdinal,
				FormalVersion: uint64Pointer(
					document.NextFormalVersion,
				),
				ObjectID:    target.ObjectID,
				ContentHash: target.ContentHash,
				Size:        target.Size,
				MimeType:    target.MimeType,
				CreatedAt:   service.now().UTC(),
				CreatedBy:   request.CreatedBy,
				DeviceID:    request.DeviceID,
				Comment:     optionalString(request.Comment),
			}
			document.NextRevisionOrdinal++
			document.NextFormalVersion++
			document.Revisions = append(document.Revisions, revision)
			document.EffectiveRevisionID = revision.RevisionID
			next[document.DocumentID] = document
			head, err := service.commit(
				ctx, intent, next, nil,
			)
			if err != nil {
				return err
			}
			pendingDocuments = next
			pendingHead = head
			result = SaveResult{
				Document: cloneDocument(document),
				Revision: revision,
				Root:     head.Root,
			}
			return nil
		},
	)
	if stateLocked {
		defer service.mu.Unlock()
	}
	if err != nil {
		return SaveResult{}, err
	}
	service.documents = pendingDocuments
	service.installHeadLocked(pendingHead)
	result.MutationRevision = receipt.MutationRevision
	if service.materializer != nil {
		_ = service.materializer.Finalize(receipt.MutationRevision)
	}
	return result, nil
}

func (service *Service) ActivateLeaf(
	ctx context.Context,
	token writecoordinator.Token,
	documentID string,
	revisionID string,
	expectedEffective *string,
) (MutationResult, error) {
	if !validUUID(documentID) || !validUUID(revisionID) {
		return MutationResult{}, ErrRevisionNotFound
	}
	return service.mutateDocument(
		ctx, token, documentID, expectedEffective, false,
		func(document *Document, _ map[string]Document) (bool, error) {
			if revisionByID(*document, revisionID) == nil {
				return false, ErrRevisionNotFound
			}
			if !isLeaf(*document, revisionID) {
				return false, ErrNotLeaf
			}
			if document.EffectiveRevisionID == revisionID {
				return false, nil
			}
			document.EffectiveRevisionID = revisionID
			return true, nil
		},
	)
}

func (service *Service) Rename(
	ctx context.Context,
	token writecoordinator.Token,
	documentID string,
	newPath string,
	expectedEffective *string,
) (MutationResult, error) {
	if !validUUID(documentID) {
		return MutationResult{}, ErrDocumentNotFound
	}
	normalized, err := normalizePath(newPath)
	if err != nil {
		return MutationResult{}, err
	}
	return service.mutateDocument(
		ctx, token, documentID, expectedEffective, false,
		func(document *Document, documents map[string]Document) (bool, error) {
			if document.RelativePath == normalized {
				return false, nil
			}
			if err := pathAvailable(documents, document.DocumentID, normalized); err != nil {
				return false, err
			}
			document.RelativePath = normalized
			document.TopologyRevision++
			return true, nil
		},
	)
}

func (service *Service) Delete(
	ctx context.Context,
	token writecoordinator.Token,
	documentID string,
	expectedEffective *string,
) (MutationResult, error) {
	if !validUUID(documentID) {
		return MutationResult{}, ErrDocumentNotFound
	}
	return service.mutateDocument(
		ctx, token, documentID, expectedEffective, true,
		func(document *Document, _ map[string]Document) (bool, error) {
			if document.Status == DocumentDeleted {
				return false, nil
			}
			document.Status = DocumentDeleted
			document.TopologyRevision++
			return true, nil
		},
	)
}

func (service *Service) Inspect(documentID string) (Document, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	document, exists := service.documents[documentID]
	if !exists {
		return Document{}, ErrDocumentNotFound
	}
	return cloneDocument(document), nil
}

func (service *Service) List() []Document {
	service.mu.RLock()
	defer service.mu.RUnlock()
	result := make([]Document, 0, len(service.documents))
	for _, document := range service.documents {
		result = append(result, cloneDocument(document))
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].DocumentID < result[right].DocumentID
	})
	return result
}

func (service *Service) Root() objectrepo.ManifestID {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.root
}

func (service *Service) StageSnapshotRestore(
	ctx context.Context,
	intent writecoordinator.WriteIntent,
	sourceRoot objectrepo.ManifestID,
) (StagedSnapshotRestore, error) {
	if sourceRoot == "" ||
		intent.Token.WorkspaceID == "" ||
		intent.MutationRevision == 0 {
		return StagedSnapshotRestore{}, ErrStateCorrupt
	}
	sourceDocuments, err := loadDocumentsFromRoot(
		ctx,
		service.repository,
		intent.Token.WorkspaceID,
		sourceRoot,
	)
	if err != nil {
		return StagedSnapshotRestore{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	next := cloneDocuments(service.documents)
	for documentID, current := range next {
		source, exists := sourceDocuments[documentID]
		if !exists {
			if current.Status == DocumentActive {
				current.Status = DocumentDeleted
				current.TopologyRevision++
				next[documentID] = current
			}
			continue
		}
		merged, mergeErr := mergeSnapshotDocument(
			service,
			current,
			source,
			intent.Token.ClaimID,
		)
		if mergeErr != nil {
			return StagedSnapshotRestore{}, mergeErr
		}
		next[documentID] = merged
		delete(sourceDocuments, documentID)
	}
	for documentID, source := range sourceDocuments {
		restored, restoreErr := mergeSnapshotDocument(
			service,
			Document{},
			source,
			intent.Token.ClaimID,
		)
		if restoreErr != nil {
			return StagedSnapshotRestore{}, restoreErr
		}
		next[documentID] = restored
	}
	if err := validatePaths(next); err != nil {
		return StagedSnapshotRestore{}, err
	}
	payload := rootPayload{
		FormatVersion: rootFormatVersion,
		WorkspaceID:   intent.Token.WorkspaceID,
		Documents:     sortedDocuments(next),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return StagedSnapshotRestore{}, err
	}
	receipt, err := service.repository.Commit(
		ctx,
		objectrepo.CommitRequest{
			Authority: intent.Token.Authority(),
			Manifests: []objectrepo.ManifestInput{{
				Name: "filehistory-root",
				Labels: map[string]string{
					"type":        "filehistory-root",
					"workspaceId": intent.Token.WorkspaceID,
				},
				Payload: raw,
			}},
		},
	)
	if err != nil {
		return StagedSnapshotRestore{}, err
	}
	root := receipt.Manifests["filehistory-root"]
	if !receipt.Durable || root == "" {
		return StagedSnapshotRestore{},
			errors.New("filehistory.root_missing")
	}
	previous := CurrentHead{
		WorkspaceID:      intent.Token.WorkspaceID,
		Root:             service.root,
		Revision:         service.headRevision,
		MutationRevision: service.headMutationRevision,
		SessionEpoch:     service.headSessionEpoch,
		FenceEpoch:       service.headFenceEpoch,
		ClaimID:          service.headClaimID,
	}
	nextHead := CurrentHead{
		WorkspaceID:      intent.Token.WorkspaceID,
		Root:             root,
		Revision:         previous.Revision + 1,
		MutationRevision: intent.MutationRevision,
		SessionEpoch:     intent.Token.SessionEpoch,
		FenceEpoch:       intent.Token.FenceEpoch,
		ClaimID:          intent.Token.ClaimID,
	}
	auditPayload, err := json.Marshal(map[string]any{
		"type":             "fileHistory.snapshotRestored",
		"workspaceId":      intent.Token.WorkspaceID,
		"mutationRevision": intent.MutationRevision,
		"previousRoot":     previous.Root,
		"root":             nextHead.Root,
		"headRevision":     nextHead.Revision,
		"documentCount":    len(next),
	})
	if err != nil {
		return StagedSnapshotRestore{}, err
	}
	envelope, err := auditledger.NewEnvelope(
		fmt.Sprintf(
			"filehistory-head:%s:%020d",
			intent.Token.WorkspaceID,
			nextHead.Revision,
		),
		"filehistory:"+intent.Token.WorkspaceID,
		nextHead.Revision,
		fmt.Sprintf(
			"filehistory:%s:%d:%d:%s:%d",
			intent.Token.WorkspaceID,
			intent.Token.SessionEpoch,
			intent.Token.FenceEpoch,
			intent.Token.ClaimID,
			intent.MutationRevision,
		),
		auditPayload,
		service.now().UTC(),
	)
	if err != nil {
		return StagedSnapshotRestore{}, err
	}
	return StagedSnapshotRestore{
		PreviousHead: previous,
		NextHead:     nextHead,
		Documents:    sortedDocuments(next),
		Audit:        envelope,
	}, nil
}

func mergeSnapshotDocument(
	service *Service,
	current Document,
	source Document,
	deviceID string,
) (Document, error) {
	sourceRevision := revisionByID(source, source.EffectiveRevisionID)
	if source.Status != DocumentActive ||
		sourceRevision == nil {
		return Document{}, ErrStateCorrupt
	}
	if current.DocumentID == "" {
		current = cloneDocument(source)
	} else {
		if current.DocumentID != source.DocumentID ||
			current.WorkspaceID != source.WorkspaceID {
			return Document{}, ErrStateCorrupt
		}
		known := make(map[string]Revision, len(current.Revisions))
		for _, revision := range current.Revisions {
			known[revision.RevisionID] = revision
		}
		for _, revision := range source.Revisions {
			if existing, ok := known[revision.RevisionID]; ok {
				left, _ := json.Marshal(existing)
				right, _ := json.Marshal(revision)
				if !bytes.Equal(left, right) {
					return Document{}, ErrStateCorrupt
				}
				continue
			}
			current.Revisions = append(current.Revisions, *cloneRevision(&revision))
		}
	}
	parent := current.EffectiveRevisionID
	revisionID, err := service.newID()
	if err != nil {
		return Document{}, err
	}
	formalVersion := current.NextFormalVersion
	restoredFrom := sourceRevision.RevisionID
	restore := Revision{
		ContractVersion:        contractVersion,
		RevisionID:             revisionID,
		DocumentID:             current.DocumentID,
		ParentRevisionID:       optionalString(parent),
		RestoredFromRevisionID: &restoredFrom,
		Kind:                   RevisionRestore,
		RevisionOrdinal:        current.NextRevisionOrdinal,
		FormalVersion:          &formalVersion,
		ObjectID:               sourceRevision.ObjectID,
		ContentHash:            sourceRevision.ContentHash,
		Size:                   sourceRevision.Size,
		MimeType:               sourceRevision.MimeType,
		CreatedAt:              service.now().UTC(),
		CreatedBy:              "snapshot-restore",
		DeviceID:               deviceID,
	}
	current.RelativePath = source.RelativePath
	current.Status = DocumentActive
	current.TopologyRevision++
	current.EffectiveRevisionID = revisionID
	current.NextRevisionOrdinal++
	current.NextFormalVersion++
	current.Revisions = append(current.Revisions, restore)
	if err := validateDocument(current); err != nil {
		return Document{}, err
	}
	return current, nil
}

func loadDocumentsFromRoot(
	ctx context.Context,
	repository objectrepo.Repository,
	workspaceID string,
	root objectrepo.ManifestID,
) (map[string]Document, error) {
	record, err := repository.GetManifest(ctx, root)
	if err != nil {
		return nil, err
	}
	if record.Labels["type"] != "filehistory-root" {
		return nil, ErrStateCorrupt
	}
	var payload rootPayload
	if err := decodeStrict(record.Payload, &payload); err != nil ||
		payload.FormatVersion != rootFormatVersion ||
		payload.WorkspaceID != workspaceID {
		return nil, errors.Join(ErrStateCorrupt, err)
	}
	documents := make(map[string]Document, len(payload.Documents))
	for _, document := range payload.Documents {
		if document.WorkspaceID != workspaceID ||
			validateDocument(document) != nil {
			return nil, ErrStateCorrupt
		}
		if _, exists := documents[document.DocumentID]; exists {
			return nil, ErrStateCorrupt
		}
		documents[document.DocumentID] = cloneDocument(document)
	}
	if err := validatePaths(documents); err != nil {
		return nil, errors.Join(ErrStateCorrupt, err)
	}
	report, err := repository.Verify(ctx, reachableObjects(documents))
	if err != nil {
		return nil, err
	}
	if !report.Valid {
		return nil, ErrStateCorrupt
	}
	if err := verifyRevisionObjects(ctx, repository, documents); err != nil {
		return nil, err
	}
	return documents, nil
}

func (service *Service) mutateDocument(
	ctx context.Context,
	token writecoordinator.Token,
	documentID string,
	expectedEffective *string,
	allowDeleted bool,
	mutate func(*Document, map[string]Document) (bool, error),
) (MutationResult, error) {
	if !validUUID(documentID) ||
		(expectedEffective != nil &&
			!validUUID(*expectedEffective)) {
		return MutationResult{}, ErrRevisionConflict
	}
	var (
		result           MutationResult
		pendingDocuments map[string]Document
		pendingHead      CurrentHead
		stateLocked      bool
	)
	receipt, err := service.coordinator.Write(
		ctx,
		token,
		func(ctx context.Context, intent writecoordinator.WriteIntent) error {
			service.mu.Lock()
			stateLocked = true
			next := cloneDocuments(service.documents)
			document, exists := next[documentID]
			if !exists {
				return ErrDocumentNotFound
			}
			if document.Status == DocumentDeleted && !allowDeleted {
				return ErrDocumentDeleted
			}
			if err := requireExpected(document, expectedEffective); err != nil {
				return err
			}
			changed, err := mutate(&document, next)
			if err != nil {
				return err
			}
			if !changed {
				result = MutationResult{
					Document:         cloneDocument(document),
					Root:             service.root,
					MutationRevision: intent.MutationRevision - 1,
					NoOp:             true,
				}
				return errNoOp
			}
			if !isLeaf(document, document.EffectiveRevisionID) {
				return ErrNotLeaf
			}
			next[documentID] = document
			head, err := service.commit(
				ctx, intent, next, nil,
			)
			if err != nil {
				return err
			}
			pendingDocuments = next
			pendingHead = head
			result = MutationResult{
				Document: cloneDocument(document), Root: head.Root,
			}
			return nil
		},
	)
	if stateLocked {
		defer service.mu.Unlock()
	}
	if errors.Is(err, errNoOp) {
		return result, nil
	}
	if err != nil {
		return MutationResult{}, err
	}
	service.documents = pendingDocuments
	service.installHeadLocked(pendingHead)
	result.MutationRevision = receipt.MutationRevision
	if service.materializer != nil {
		_ = service.materializer.Finalize(receipt.MutationRevision)
	}
	return result, nil
}

func (service *Service) commit(
	ctx context.Context,
	intent writecoordinator.WriteIntent,
	documents map[string]Document,
	object *objectrepo.ObjectInput,
) (CurrentHead, error) {
	authority := intent.Token.Authority()
	if err := validatePaths(documents); err != nil {
		return CurrentHead{}, err
	}
	for _, document := range documents {
		if document.WorkspaceID != authority.WorkspaceID {
			return CurrentHead{}, ErrStateCorrupt
		}
		if err := validateDocument(document); err != nil {
			return CurrentHead{}, err
		}
	}
	payload := rootPayload{
		FormatVersion: rootFormatVersion,
		WorkspaceID:   authority.WorkspaceID,
		Documents:     sortedDocuments(documents),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return CurrentHead{}, fmt.Errorf(
			"encode file history root: %w", err,
		)
	}
	request := objectrepo.CommitRequest{
		Authority: authority,
		Manifests: []objectrepo.ManifestInput{{
			Name: "filehistory-root",
			Labels: map[string]string{
				"type":        "filehistory-root",
				"workspaceId": authority.WorkspaceID,
			},
			Payload: raw,
		}},
	}
	if object != nil {
		request.Objects = []objectrepo.ObjectInput{*object}
	}
	receipt, err := service.repository.Commit(ctx, request)
	if err != nil {
		return CurrentHead{}, err
	}
	if !receipt.Durable ||
		receipt.WorkspaceID != authority.WorkspaceID ||
		receipt.FenceEpoch != authority.FenceEpoch ||
		receipt.ClaimID != authority.ClaimID {
		return CurrentHead{}, errors.New(
			"filehistory.repository_receipt_invalid",
		)
	}
	root, exists := receipt.Manifests["filehistory-root"]
	if !exists || root == "" {
		return CurrentHead{}, errors.New("filehistory.root_missing")
	}
	if object != nil {
		expected := contentObjectID(object.Content)
		if receipt.Objects[object.Name] != expected {
			return CurrentHead{}, errors.New(
				"filehistory.content_receipt_invalid",
			)
		}
	}
	expectedHead := CurrentHead{
		WorkspaceID:      authority.WorkspaceID,
		Root:             service.root,
		Revision:         service.headRevision,
		MutationRevision: service.headMutationRevision,
		SessionEpoch:     service.headSessionEpoch,
		FenceEpoch:       service.headFenceEpoch,
		ClaimID:          service.headClaimID,
	}
	nextHead := CurrentHead{
		WorkspaceID:      authority.WorkspaceID,
		Root:             root,
		Revision:         service.headRevision + 1,
		MutationRevision: intent.MutationRevision,
		SessionEpoch:     intent.Token.SessionEpoch,
		FenceEpoch:       intent.Token.FenceEpoch,
		ClaimID:          intent.Token.ClaimID,
	}
	auditPayload, err := json.Marshal(map[string]any{
		"type":             "fileHistory.headPublished",
		"workspaceId":      authority.WorkspaceID,
		"mutationRevision": intent.MutationRevision,
		"previousRoot":     expectedHead.Root,
		"root":             nextHead.Root,
		"headRevision":     nextHead.Revision,
		"documentCount":    len(documents),
	})
	if err != nil {
		return CurrentHead{}, err
	}
	envelope, err := auditledger.NewEnvelope(
		fmt.Sprintf(
			"filehistory-head:%s:%020d",
			authority.WorkspaceID,
			nextHead.Revision,
		),
		"filehistory:"+authority.WorkspaceID,
		nextHead.Revision,
		fmt.Sprintf(
			"filehistory:%s:%d:%d:%s:%d",
			authority.WorkspaceID,
			intent.Token.SessionEpoch,
			intent.Token.FenceEpoch,
			intent.Token.ClaimID,
			intent.MutationRevision,
		),
		auditPayload,
		service.now().UTC(),
	)
	if err != nil {
		return CurrentHead{}, err
	}
	if service.materializer != nil {
		if err := service.materializer.PrepareAndApply(
			ctx,
			intent,
			service.documents,
			documents,
		); err != nil {
			return CurrentHead{}, err
		}
	}
	var head CurrentHead
	if builder, requested := operationReceiptBuilder(ctx); requested {
		store, ok := service.headStore.(OperationReceiptHeadStore)
		if !ok {
			if service.materializer != nil {
				_ = service.materializer.Rollback(intent.MutationRevision)
			}
			return CurrentHead{},
				errors.New("filehistory.operation_receipt_store_required")
		}
		receipt, receiptErr := builder(Publication{
			Head:      nextHead,
			Documents: sortedDocuments(documents),
		})
		if receiptErr != nil {
			if service.materializer != nil {
				_ = service.materializer.Rollback(intent.MutationRevision)
			}
			return CurrentHead{}, receiptErr
		}
		head, err = store.CompareAndSwapWithAuditAndReceipt(
			ctx,
			expectedHead,
			nextHead,
			envelope,
			receipt,
		)
	} else if audited, ok := service.headStore.(AuditedHeadStore); ok {
		head, err = audited.CompareAndSwapWithAudit(
			ctx, expectedHead, nextHead, envelope,
		)
	} else {
		head, err = service.headStore.CompareAndSwap(
			ctx, expectedHead, nextHead,
		)
	}
	if err != nil {
		if service.materializer != nil {
			err = errors.Join(
				err,
				service.materializer.Rollback(intent.MutationRevision),
			)
		}
		return CurrentHead{}, err
	}
	if head.WorkspaceID != authority.WorkspaceID ||
		head.Root != root ||
		head != nextHead {
		return CurrentHead{}, ErrHeadConflict
	}
	return head, nil
}

func (service *Service) installHeadLocked(head CurrentHead) {
	service.root = head.Root
	service.headRevision = head.Revision
	service.headMutationRevision = head.MutationRevision
	service.headSessionEpoch = head.SessionEpoch
	service.headFenceEpoch = head.FenceEpoch
	service.headClaimID = head.ClaimID
}

func requireExpected(document Document, expected *string) error {
	if expected != nil && *expected != document.EffectiveRevisionID {
		return ErrRevisionConflict
	}
	return nil
}

func normalizePath(value string) (string, error) {
	if value == "" ||
		strings.Contains(value, "\\") ||
		strings.Contains(value, "\x00") ||
		strings.Contains(value, ":") ||
		strings.HasPrefix(value, "/") {
		return "", ErrPathInvalid
	}
	normalized := path.Clean(value)
	if normalized == "." ||
		normalized == ".." ||
		strings.HasPrefix(normalized, "../") ||
		normalized != value {
		return "", ErrPathInvalid
	}
	return normalized, nil
}

func pathAvailable(
	documents map[string]Document,
	documentID string,
	candidate string,
) error {
	for id, document := range documents {
		if id != documentID && document.RelativePath == candidate {
			return ErrPathConflict
		}
	}
	return nil
}

func validatePaths(documents map[string]Document) error {
	seen := map[string]string{}
	for id, document := range documents {
		normalized, err := normalizePath(document.RelativePath)
		if err != nil || normalized != document.RelativePath {
			return ErrPathInvalid
		}
		if other, exists := seen[document.RelativePath]; exists &&
			other != id {
			return ErrPathConflict
		}
		seen[document.RelativePath] = id
	}
	return nil
}

func validateDocument(document Document) error {
	if document.ContractVersion != contractVersion ||
		!validUUID(document.WorkspaceID) ||
		!validUUID(document.DocumentID) ||
		(document.Status != DocumentActive &&
			document.Status != DocumentDeleted) ||
		document.TopologyRevision == 0 ||
		len(document.Revisions) == 0 ||
		document.NextRevisionOrdinal <=
			uint64(len(document.Revisions)) ||
		document.NextFormalVersion == 0 {
		return ErrStateCorrupt
	}
	revisions := map[string]Revision{}
	children := map[string]int{}
	sequences := map[uint64]struct{}{}
	formalVersions := map[uint64]struct{}{}
	var maxSequence uint64
	var maxFormal uint64
	roots := 0
	for _, revision := range document.Revisions {
		if revision.ContractVersion != contractVersion ||
			!validUUID(revision.RevisionID) ||
			revision.DocumentID != document.DocumentID ||
			revision.RevisionOrdinal == 0 ||
			revision.ObjectID == "" ||
			!validContentHash(revision.ContentHash) ||
			revision.ObjectID != objectIDFromHash(revision.ContentHash) ||
			revision.Size < 0 ||
			!validMimeType(revision.MimeType) ||
			revision.CreatedAt.IsZero() ||
			strings.TrimSpace(revision.CreatedBy) == "" ||
			!validUUID(revision.DeviceID) ||
			(revision.Kind != RevisionAutosave &&
				revision.Kind != RevisionFormal &&
				revision.Kind != RevisionRestore) {
			return ErrStateCorrupt
		}
		if _, exists := revisions[revision.RevisionID]; exists {
			return ErrStateCorrupt
		}
		if _, exists := sequences[revision.RevisionOrdinal]; exists {
			return ErrStateCorrupt
		}
		sequences[revision.RevisionOrdinal] = struct{}{}
		if revision.Kind == RevisionAutosave &&
			revision.FormalVersion != nil {
			return ErrStateCorrupt
		}
		if revision.Kind != RevisionAutosave &&
			(revision.FormalVersion == nil ||
				*revision.FormalVersion == 0) {
			return ErrStateCorrupt
		}
		if revision.FormalVersion != nil {
			formalVersion := *revision.FormalVersion
			if _, exists := formalVersions[formalVersion]; exists {
				return ErrStateCorrupt
			}
			formalVersions[formalVersion] = struct{}{}
		}
		if revision.ParentRevisionID == nil {
			roots++
		}
		revisions[revision.RevisionID] = revision
		if revision.RevisionOrdinal > maxSequence {
			maxSequence = revision.RevisionOrdinal
		}
		if revision.FormalVersion != nil &&
			*revision.FormalVersion > maxFormal {
			maxFormal = *revision.FormalVersion
		}
	}
	for _, revision := range document.Revisions {
		if revision.ParentRevisionID != nil {
			if _, exists := revisions[*revision.ParentRevisionID]; !exists ||
				*revision.ParentRevisionID == revision.RevisionID {
				return ErrStateCorrupt
			}
			children[*revision.ParentRevisionID]++
		}
		if revision.RestoredFromRevisionID != nil {
			if revision.Kind != RevisionRestore {
				return ErrStateCorrupt
			}
			if _, exists := revisions[*revision.RestoredFromRevisionID]; !exists {
				return ErrStateCorrupt
			}
			if *revision.RestoredFromRevisionID == revision.RevisionID {
				return ErrStateCorrupt
			}
		} else if revision.Kind == RevisionRestore {
			return ErrStateCorrupt
		}
		if revision.ParentRevisionID != nil {
			parent := revisions[*revision.ParentRevisionID]
			if parent.RevisionOrdinal >= revision.RevisionOrdinal ||
				parent.CreatedAt.After(revision.CreatedAt) {
				return ErrStateCorrupt
			}
		}
	}
	if roots != 1 ||
		maxSequence != uint64(len(sequences)) ||
		maxFormal != uint64(len(formalVersions)) {
		return ErrStateCorrupt
	}
	for revisionID := range revisions {
		seen := map[string]struct{}{}
		current := revisionID
		for current != "" {
			if _, exists := seen[current]; exists {
				return ErrStateCorrupt
			}
			seen[current] = struct{}{}
			revision := revisions[current]
			if revision.ParentRevisionID == nil {
				current = ""
			} else {
				current = *revision.ParentRevisionID
			}
		}
	}
	if _, exists := revisions[document.EffectiveRevisionID]; !exists ||
		children[document.EffectiveRevisionID] != 0 ||
		document.NextRevisionOrdinal != maxSequence+1 ||
		document.NextFormalVersion != maxFormal+1 {
		return ErrStateCorrupt
	}
	return nil
}

func revisionByID(document Document, revisionID string) *Revision {
	for index := range document.Revisions {
		if document.Revisions[index].RevisionID == revisionID {
			return &document.Revisions[index]
		}
	}
	return nil
}

func isLeaf(document Document, revisionID string) bool {
	if revisionByID(document, revisionID) == nil {
		return false
	}
	for _, revision := range document.Revisions {
		if revision.ParentRevisionID != nil &&
			*revision.ParentRevisionID == revisionID {
			return false
		}
	}
	return true
}

func sortedDocuments(documents map[string]Document) []Document {
	result := make([]Document, 0, len(documents))
	for _, document := range documents {
		result = append(result, cloneDocument(document))
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].DocumentID < result[right].DocumentID
	})
	return result
}

func reachableObjects(documents map[string]Document) []objectrepo.ObjectID {
	unique := map[objectrepo.ObjectID]struct{}{}
	for _, document := range documents {
		for _, revision := range document.Revisions {
			unique[revision.ObjectID] = struct{}{}
		}
	}
	result := make([]objectrepo.ObjectID, 0, len(unique))
	for id := range unique {
		result = append(result, id)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left] < result[right]
	})
	return result
}

func cloneDocuments(source map[string]Document) map[string]Document {
	result := make(map[string]Document, len(source))
	for id, document := range source {
		result[id] = cloneDocument(document)
	}
	return result
}

func cloneDocument(document Document) Document {
	source := document.Revisions
	document.Revisions = make([]Revision, len(source))
	for index, revision := range source {
		document.Revisions[index] = *cloneRevision(&revision)
	}
	return document
}

func cloneRevision(revision *Revision) *Revision {
	if revision == nil {
		return nil
	}
	result := *revision
	if revision.ParentRevisionID != nil {
		result.ParentRevisionID = stringPointer(*revision.ParentRevisionID)
	}
	if revision.RestoredFromRevisionID != nil {
		result.RestoredFromRevisionID = stringPointer(*revision.RestoredFromRevisionID)
	}
	if revision.FormalVersion != nil {
		result.FormalVersion = uint64Pointer(*revision.FormalVersion)
	}
	if revision.Comment != nil {
		result.Comment = stringPointer(*revision.Comment)
	}
	return &result
}

func contentObjectID(content []byte) objectrepo.ObjectID {
	sum := sha256.Sum256(content)
	return objectrepo.ObjectID("obj_" + hex.EncodeToString(sum[:]))
}

func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func objectIDFromHash(hash string) objectrepo.ObjectID {
	return objectrepo.ObjectID("obj_" + strings.TrimPrefix(hash, "sha256:"))
}

func randomRevisionID() (string, error) {
	return uuid.NewString(), nil
}

func stringPointer(value string) *string {
	return &value
}

func uint64Pointer(value uint64) *uint64 {
	return &value
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return stringPointer(value)
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON")
		}
		return err
	}
	return nil
}

func validateSaveRequest(request SaveRequest) error {
	if !validUUID(request.Token.WorkspaceID) ||
		!validUUID(request.DocumentID) ||
		(request.ParentRevisionID != "" &&
			!validUUID(request.ParentRevisionID)) ||
		(request.ExpectedEffectiveRevision != nil &&
			!validUUID(*request.ExpectedEffectiveRevision)) ||
		(request.Kind != RevisionAutosave &&
			request.Kind != RevisionFormal) ||
		!validMimeType(request.MimeType) ||
		strings.TrimSpace(request.CreatedBy) == "" ||
		!validUUID(request.DeviceID) {
		return errors.New("filehistory.save_invalid")
	}
	return nil
}

func verifyRevisionObjects(
	ctx context.Context,
	repository objectrepo.Repository,
	documents map[string]Document,
) error {
	type objectMetadata struct {
		hash string
		size int64
	}
	verified := map[objectrepo.ObjectID]objectMetadata{}
	for _, document := range documents {
		for _, revision := range document.Revisions {
			if metadata, found := verified[revision.ObjectID]; found {
				if metadata.hash != revision.ContentHash ||
					metadata.size != revision.Size {
					return ErrStateCorrupt
				}
				continue
			}
			reader, err := repository.Open(ctx, revision.ObjectID)
			if err != nil {
				return errors.Join(ErrStateCorrupt, err)
			}
			content, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if err := errors.Join(readErr, closeErr); err != nil {
				return errors.Join(ErrStateCorrupt, err)
			}
			if int64(len(content)) != revision.Size ||
				contentHash(content) != revision.ContentHash {
				return ErrStateCorrupt
			}
			verified[revision.ObjectID] = objectMetadata{
				hash: revision.ContentHash,
				size: revision.Size,
			}
		}
	}
	return nil
}

func validUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil
}

func validMimeType(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	_, _, err := mime.ParseMediaType(value)
	return err == nil
}

func validContentHash(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+sha256.Size*2 ||
		!strings.HasPrefix(value, prefix) {
		return false
	}
	_, err := hex.DecodeString(value[len(prefix):])
	return err == nil
}
