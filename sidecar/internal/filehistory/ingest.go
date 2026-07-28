package filehistory

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type ExternalChangeKind string

const (
	ExternalStableSave ExternalChangeKind = "stable-save"
	ExternalRename     ExternalChangeKind = "rename"
	ExternalMove       ExternalChangeKind = "move"
	ExternalCopy       ExternalChangeKind = "copy"
	ExternalDelete     ExternalChangeKind = "delete"
)

type ExternalChange struct {
	Token                     writecoordinator.Token
	Kind                      ExternalChangeKind
	DocumentID                string
	SourceDocumentID          string
	NewDocumentID             string
	SourcePath                string
	TargetPath                string
	Content                   []byte
	ContentProvided           bool
	RevisionKind              RevisionKind
	ExpectedEffectiveRevision *string
	MimeType                  string
	CreatedBy                 string
	DeviceID                  string
	Comment                   string
}

type IdentityResolution struct {
	DocumentID           string
	NewDocument          bool
	RequiresConfirmation bool
	CandidateDocumentIDs []string
	Reason               string
}

type IdentityResolver interface {
	Resolve(
		context.Context,
		ExternalChange,
		[]Document,
	) (IdentityResolution, error)
}

type ConservativeIdentityResolver struct{}

func (ConservativeIdentityResolver) Resolve(
	_ context.Context,
	change ExternalChange,
	documents []Document,
) (IdentityResolution, error) {
	byID := make(map[string]Document, len(documents))
	for _, document := range documents {
		byID[document.DocumentID] = document
	}
	requestedNewID := ""
	if change.DocumentID != "" {
		if !validUUID(change.DocumentID) {
			return IdentityResolution{}, errors.New(
				"filehistory.external_document_id_invalid",
			)
		}
		if document, found := byID[change.DocumentID]; found {
			if document.Status == DocumentDeleted {
				return confirmation(
					"matched document is deleted",
					[]string{document.DocumentID},
				), nil
			}
			return IdentityResolution{
				DocumentID: document.DocumentID,
			}, nil
		}
		if change.Kind == ExternalStableSave {
			requestedNewID = change.DocumentID
		} else {
			return confirmation(
				"stable identity is unknown", nil,
			), nil
		}
	}

	sourcePath := change.SourcePath
	if change.Kind == ExternalStableSave && sourcePath == "" {
		sourcePath = change.TargetPath
	}
	if sourcePath != "" {
		var matches []string
		for _, document := range documents {
			if document.RelativePath == sourcePath {
				matches = append(matches, document.DocumentID)
			}
		}
		if len(matches) == 1 {
			document := byID[matches[0]]
			if document.Status == DocumentDeleted {
				return confirmation(
					"path belongs to a deleted document", matches,
				), nil
			}
			return IdentityResolution{
				DocumentID: document.DocumentID,
			}, nil
		}
		if len(matches) > 1 {
			return confirmation("path identity is ambiguous", matches), nil
		}
	}

	if change.ContentProvided {
		hash := contentHash(change.Content)
		var candidates []string
		for _, document := range documents {
			if document.Status != DocumentActive {
				continue
			}
			effective := revisionByID(
				document, document.EffectiveRevisionID,
			)
			if effective != nil && effective.ContentHash == hash {
				candidates = append(candidates, document.DocumentID)
			}
		}
		if len(candidates) > 0 {
			return confirmation(
				"same content may be a copy, rename, or move",
				candidates,
			), nil
		}
	}

	if change.Kind == ExternalStableSave {
		return IdentityResolution{
			DocumentID:  requestedNewID,
			NewDocument: true,
		}, nil
	}
	return confirmation("existing document identity is unknown", nil), nil
}

type IdentityConfirmation struct {
	Reason               string
	CandidateDocumentIDs []string
}

type IngestResult struct {
	Save         *SaveResult
	Mutation     *MutationResult
	Confirmation *IdentityConfirmation
}

type Ingestor struct {
	service  *Service
	resolver IdentityResolver
	newID    func() string
}

func NewIngestor(
	service *Service,
	resolver IdentityResolver,
) (*Ingestor, error) {
	if service == nil {
		return nil, errors.New("filehistory.ingestor_service_required")
	}
	if resolver == nil {
		resolver = ConservativeIdentityResolver{}
	}
	return &Ingestor{
		service: service, resolver: resolver,
		newID: uuid.NewString,
	}, nil
}

func (ingestor *Ingestor) Ingest(
	ctx context.Context,
	change ExternalChange,
) (IngestResult, error) {
	if ingestor == nil || ingestor.service == nil ||
		(change.Kind != ExternalStableSave &&
			change.Kind != ExternalRename &&
			change.Kind != ExternalMove &&
			change.Kind != ExternalCopy &&
			change.Kind != ExternalDelete) {
		return IngestResult{}, errors.New(
			"filehistory.external_change_invalid",
		)
	}
	if change.Kind == ExternalCopy {
		return ingestor.copy(ctx, change)
	}
	resolution, err := ingestor.resolver.Resolve(
		ctx, change, ingestor.service.List(),
	)
	if err != nil {
		return IngestResult{}, err
	}
	if resolution.RequiresConfirmation {
		return IngestResult{
			Confirmation: &IdentityConfirmation{
				Reason: resolution.Reason,
				CandidateDocumentIDs: append(
					[]string(nil),
					resolution.CandidateDocumentIDs...,
				),
			},
		}, nil
	}
	documentID := resolution.DocumentID
	if resolution.NewDocument && documentID == "" {
		documentID = ingestor.newID()
	}
	if !validUUID(documentID) {
		return IngestResult{}, errors.New(
			"filehistory.external_document_id_invalid",
		)
	}

	switch change.Kind {
	case ExternalStableSave:
		if !change.ContentProvided {
			return IngestResult{}, errors.New(
				"filehistory.external_content_required",
			)
		}
		kind := change.RevisionKind
		if kind == "" {
			kind = RevisionAutosave
		}
		expected := change.ExpectedEffectiveRevision
		if !resolution.NewDocument && expected == nil {
			document, err := ingestor.service.Inspect(documentID)
			if err != nil {
				return IngestResult{}, err
			}
			expected = stringPointer(document.EffectiveRevisionID)
		}
		result, err := ingestor.service.Save(ctx, SaveRequest{
			Token: change.Token, DocumentID: documentID,
			Path:                      change.TargetPath,
			ExpectedEffectiveRevision: expected,
			Kind:                      kind, Content: change.Content,
			MimeType:  change.MimeType,
			CreatedBy: change.CreatedBy,
			DeviceID:  change.DeviceID,
			Comment:   change.Comment,
		})
		if err != nil {
			return IngestResult{}, err
		}
		return IngestResult{Save: &result}, nil
	case ExternalRename, ExternalMove:
		if strings.TrimSpace(change.TargetPath) == "" {
			return IngestResult{}, ErrPathInvalid
		}
		expected, err := ingestor.expected(
			documentID, change.ExpectedEffectiveRevision,
		)
		if err != nil {
			return IngestResult{}, err
		}
		result, err := ingestor.service.Rename(
			ctx, change.Token, documentID,
			change.TargetPath, expected,
		)
		if err != nil {
			return IngestResult{}, err
		}
		return IngestResult{Mutation: &result}, nil
	case ExternalDelete:
		expected, err := ingestor.expected(
			documentID, change.ExpectedEffectiveRevision,
		)
		if err != nil {
			return IngestResult{}, err
		}
		result, err := ingestor.service.Delete(
			ctx, change.Token, documentID, expected,
		)
		if err != nil {
			return IngestResult{}, err
		}
		return IngestResult{Mutation: &result}, nil
	default:
		return IngestResult{}, errors.New(
			"filehistory.external_change_invalid",
		)
	}
}

func (ingestor *Ingestor) copy(
	ctx context.Context,
	change ExternalChange,
) (IngestResult, error) {
	sourceChange := change
	sourceChange.Kind = ExternalStableSave
	sourceChange.DocumentID = change.SourceDocumentID
	sourceChange.TargetPath = change.SourcePath
	sourceChange.ContentProvided = false
	resolution, err := ingestor.resolver.Resolve(
		ctx, sourceChange, ingestor.service.List(),
	)
	if err != nil {
		return IngestResult{}, err
	}
	if resolution.RequiresConfirmation ||
		resolution.NewDocument ||
		!validUUID(resolution.DocumentID) {
		return IngestResult{
			Confirmation: &IdentityConfirmation{
				Reason: resolution.Reason,
				CandidateDocumentIDs: append(
					[]string(nil),
					resolution.CandidateDocumentIDs...,
				),
			},
		}, nil
	}
	source, err := ingestor.service.Inspect(resolution.DocumentID)
	if err != nil {
		return IngestResult{}, err
	}
	effective := revisionByID(source, source.EffectiveRevisionID)
	if effective == nil {
		return IngestResult{}, ErrStateCorrupt
	}
	content := change.Content
	if !change.ContentProvided {
		reader, err := ingestor.service.repository.Open(
			ctx, effective.ObjectID,
		)
		if err != nil {
			return IngestResult{}, err
		}
		content, err = io.ReadAll(reader)
		closeErr := reader.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return IngestResult{}, err
		}
	}
	documentID := change.NewDocumentID
	if documentID == "" {
		documentID = ingestor.newID()
	}
	if !validUUID(documentID) ||
		documentID == source.DocumentID {
		return IngestResult{}, errors.New(
			"filehistory.copy_document_id_invalid",
		)
	}
	mimeType := change.MimeType
	if mimeType == "" {
		mimeType = effective.MimeType
	}
	createdBy := change.CreatedBy
	deviceID := change.DeviceID
	result, err := ingestor.service.Save(ctx, SaveRequest{
		Token: change.Token, DocumentID: documentID,
		Path: change.TargetPath,
		Kind: RevisionAutosave, Content: content,
		MimeType: mimeType, CreatedBy: createdBy,
		DeviceID: deviceID, Comment: change.Comment,
	})
	if err != nil {
		return IngestResult{}, err
	}
	return IngestResult{Save: &result}, nil
}

func (ingestor *Ingestor) expected(
	documentID string,
	supplied *string,
) (*string, error) {
	if supplied != nil {
		return supplied, nil
	}
	document, err := ingestor.service.Inspect(documentID)
	if err != nil {
		return nil, err
	}
	return stringPointer(document.EffectiveRevisionID), nil
}

func confirmation(
	reason string,
	candidates []string,
) IdentityResolution {
	candidates = append([]string(nil), candidates...)
	sort.Strings(candidates)
	return IdentityResolution{
		RequiresConfirmation: true,
		CandidateDocumentIDs: candidates,
		Reason:               reason,
	}
}
