package filehistory

import (
	"errors"
	"sort"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

// RevisionObject describes one immutable object referenced by a validated
// FileHistory root. Callers can verify the bytes without reimplementing the
// document tree, path, effective-leaf, version and metadata invariants.
type RevisionObject struct {
	ObjectID    objectrepo.ObjectID
	ContentHash string
	Size        int64
}

// ValidateRootPayload applies the same strict structural checks used when the
// authoritative FileHistory service opens a root. Object bytes are deliberately
// verified by the caller so repository readers can enforce their own aggregate
// resource limits.
func ValidateRootPayload(
	payload []byte,
	workspaceID string,
) ([]RevisionObject, error) {
	var root rootPayload
	if err := decodeStrict(payload, &root); err != nil ||
		!supportedRootFormat(root.FormatVersion) ||
		root.WorkspaceID != workspaceID {
		return nil, errors.Join(ErrStateCorrupt, err)
	}
	if err := validateRootVersion(root); err != nil {
		return nil, errors.Join(ErrStateCorrupt, err)
	}
	if err := validateRootResourceLimits(root); err != nil {
		return nil, errors.Join(ErrStateCorrupt, err)
	}
	documents := make(map[string]Document, len(root.Documents))
	objects := map[objectrepo.ObjectID]RevisionObject{}
	for _, document := range root.Documents {
		if document.WorkspaceID != workspaceID {
			return nil, ErrStateCorrupt
		}
		if err := validateDocument(document); err != nil {
			return nil, errors.Join(ErrStateCorrupt, err)
		}
		if _, exists := documents[document.DocumentID]; exists {
			return nil, ErrStateCorrupt
		}
		documents[document.DocumentID] = cloneDocument(document)
		for _, revision := range document.Revisions {
			value := RevisionObject{
				ObjectID:    revision.ObjectID,
				ContentHash: revision.ContentHash,
				Size:        revision.Size,
			}
			if existing, found := objects[value.ObjectID]; found &&
				(existing.ContentHash != value.ContentHash ||
					existing.Size != value.Size) {
				return nil, ErrStateCorrupt
			}
			objects[value.ObjectID] = value
		}
	}
	if err := validatePaths(documents); err != nil {
		return nil, errors.Join(ErrStateCorrupt, err)
	}
	if err := validateLocalSequences(documents); err != nil {
		return nil, errors.Join(ErrStateCorrupt, err)
	}
	result := make([]RevisionObject, 0, len(objects))
	for _, value := range objects {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].ObjectID < result[right].ObjectID
	})
	return result, nil
}

func validateRootResourceLimits(root rootPayload) error {
	if len(root.Documents) > MaxRootDocuments {
		return ErrResourceLimit
	}
	totalRevisions := 0
	for _, document := range root.Documents {
		if len(document.Revisions) > MaxRootRevisions-totalRevisions {
			return ErrResourceLimit
		}
		totalRevisions += len(document.Revisions)
	}
	return nil
}
