package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/pocketbase/pocketbase/core"
	wb "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	"golang.org/x/text/cases"
)

// SurfaceService owns each independently authored Interface as one revisioned aggregate.
// Definition validation deliberately does not require live tables, fields or plugins.
type SurfaceService struct{ metadata *Service }

func NewSurface(app core.App) *SurfaceService { return &SurfaceService{metadata: New(app)} }

type SurfaceError struct {
	Code    string
	Message string
	Path    *string
}

func (err *SurfaceError) Error() string { return err.Message }
func surfaceError(code, message string, path ...string) *SurfaceError {
	e := &SurfaceError{Code: code, Message: message}
	if len(path) != 0 {
		e.Path = &path[0]
	}
	return e
}
func surfaceConflict() error {
	return surfaceError("surface.edit_conflict", "Interface changed elsewhere.", "expectedRevision")
}
func surfacePersistence(err error) error {
	if err == nil || err == writecoordinator.ErrBusinessReplay {
		return err
	}
	var domain *SurfaceError
	if errors.As(err, &domain) {
		return err
	}
	var storage *Error
	if errors.As(err, &storage) {
		switch storage.Code {
		case "metadata.revision_conflict":
			return surfaceConflict()
		case "metadata.idempotency_conflict":
			return surfaceError("surface.idempotency_conflict", "idempotencyKey was used for another Interface request.", "idempotencyKey")
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return surfaceError("surface.persistence_failed", "Interface persistence failed.")
}
func (service *SurfaceService) List(ctx context.Context) (wb.InterfaceListResult, error) {
	result := wb.InterfaceListResult{Items: []wb.InterfaceListEntry{}}
	items, err := service.metadata.List(ctx, NamespaceInterfaces)
	if err != nil {
		return result, err
	}
	for _, item := range items {
		snapshot, err := surfaceSnapshot(item)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, wb.InterfaceListEntry{InterfaceId: snapshot.Definition.InterfaceId, Name: snapshot.Definition.Name, Revision: snapshot.Revision})
	}
	fold := cases.Fold()
	sort.Slice(result.Items, func(i, j int) bool {
		left, right := result.Items[i], result.Items[j]
		a, b := fold.String(left.Name), fold.String(right.Name)
		if a == b {
			return left.InterfaceId < right.InterfaceId
		}
		return a < b
	})
	return result, nil
}
func (service *SurfaceService) Load(ctx context.Context, id string) (wb.InterfaceSnapshot, error) {
	if err := requireSurfaceID(id); err != nil {
		return wb.InterfaceSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return wb.InterfaceSnapshot{}, err
	}
	item, err := surfaceCurrent(service.metadata.app, id)
	if err != nil {
		return wb.InterfaceSnapshot{}, err
	}
	if item == nil {
		return wb.InterfaceSnapshot{}, surfaceError("surface.not_found", "Interface not found.")
	}
	return surfaceSnapshot(*item)
}
func (service *SurfaceService) Commit(ctx context.Context, request wb.InterfaceCommitRequest) (wb.InterfaceSnapshot, error) {
	if err := validateSurfaceDefinition(request.Definition); err != nil {
		return wb.InterfaceSnapshot{}, err
	}
	if request.IdempotencyKey == "" {
		return wb.InterfaceSnapshot{}, surfaceError("surface.idempotency_key_required", "idempotencyKey is required.", "idempotencyKey")
	}
	requestHash, err := hashValue(struct {
		Method  string
		Request wb.InterfaceCommitRequest
	}{"interface.commit", request})
	if err != nil {
		return wb.InterfaceSnapshot{}, surfacePersistence(err)
	}
	result, err := executeIdempotent(service.metadata, ctx, request.IdempotencyKey, requestHash,
		func(tx core.App) (wb.InterfaceSnapshot, []metadataChange, error) {
			current, err := surfaceCurrent(tx, request.Definition.InterfaceId)
			if err != nil {
				return wb.InterfaceSnapshot{}, nil, err
			}
			if (current == nil && request.ExpectedRevision != nil) || (current != nil && (request.ExpectedRevision == nil || *request.ExpectedRevision != current.Revision)) {
				return wb.InterfaceSnapshot{}, nil, surfaceConflict()
			}
			if err := validateIdempotencyKey(request.IdempotencyKey); err != nil {
				return wb.InterfaceSnapshot{}, nil, err
			}
			raw, err := json.Marshal(request.Definition)
			if err != nil {
				return wb.InterfaceSnapshot{}, nil, err
			}
			expected := ""
			if request.ExpectedRevision != nil {
				expected = *request.ExpectedRevision
			}
			item, change, err := service.metadata.upsert(tx, NamespaceInterfaces, ItemMutation{LogicalID: request.Definition.InterfaceId, Payload: raw, ExpectedRevision: expected}, "", "")
			if err != nil {
				return wb.InterfaceSnapshot{}, nil, err
			}
			snapshot, err := surfaceSnapshot(item)
			return snapshot, []metadataChange{change}, err
		}, func(*wb.InterfaceSnapshot, string, []string) {}, func(*wb.InterfaceSnapshot) {}, func() error {
			return writecoordinator.ReplayedBusinessWrite(ctx, "metadata.interfaces.upsert", request.IdempotencyKey)
		})
	return result, surfacePersistence(err)
}
func (service *SurfaceService) Delete(ctx context.Context, request wb.InterfaceDeleteRequest) (wb.InterfaceDeleteResult, error) {
	if err := requireSurfaceID(request.InterfaceId); err != nil {
		return wb.InterfaceDeleteResult{}, err
	}
	if request.ExpectedRevision == "" {
		return wb.InterfaceDeleteResult{}, surfaceError("surface.revision_required", "expectedRevision is required.", "expectedRevision")
	}
	if request.IdempotencyKey == "" {
		return wb.InterfaceDeleteResult{}, surfaceError("surface.idempotency_key_required", "idempotencyKey is required.", "idempotencyKey")
	}
	requestHash, err := hashValue(struct {
		Method  string
		Request wb.InterfaceDeleteRequest
	}{"interface.delete", request})
	if err != nil {
		return wb.InterfaceDeleteResult{}, surfacePersistence(err)
	}
	result, err := executeIdempotent(service.metadata, ctx, request.IdempotencyKey, requestHash,
		func(tx core.App) (wb.InterfaceDeleteResult, []metadataChange, error) {
			current, err := surfaceCurrent(tx, request.InterfaceId)
			if err != nil {
				return wb.InterfaceDeleteResult{}, nil, err
			}
			if current == nil {
				return wb.InterfaceDeleteResult{}, nil, surfaceError("surface.not_found", "Interface not found.")
			}
			if current.Revision != request.ExpectedRevision {
				return wb.InterfaceDeleteResult{}, nil, surfaceConflict()
			}
			if err := validateIdempotencyKey(request.IdempotencyKey); err != nil {
				return wb.InterfaceDeleteResult{}, nil, err
			}
			change, err := service.metadata.delete(tx, NamespaceInterfaces, ItemDelete{LogicalID: request.InterfaceId, ExpectedRevision: request.ExpectedRevision}, "", "")
			return wb.InterfaceDeleteResult{InterfaceId: request.InterfaceId}, []metadataChange{change}, err
		}, func(*wb.InterfaceDeleteResult, string, []string) {}, func(*wb.InterfaceDeleteResult) {}, func() error {
			return writecoordinator.ReplayedBusinessWrite(ctx, "metadata.interfaces.delete", request.IdempotencyKey)
		})
	return result, surfacePersistence(err)
}
func surfaceCurrent(app core.App, id string) (*Item, error) {
	collection, err := resolveCollection(app, NamespaceInterfaces)
	if err != nil {
		return nil, err
	}
	record, err := findRecord(app, collection, id)
	if err != nil || record == nil {
		return nil, err
	}
	item, err := itemFromRecord(NamespaceInterfaces, record)
	return &item, err
}
func surfaceSnapshot(item Item) (wb.InterfaceSnapshot, error) {
	var definition wb.InterfaceDefinition
	if DecodeSurfaceDefinition(item.Payload, &definition) != nil || validateSurfaceDefinition(definition) != nil {
		return wb.InterfaceSnapshot{}, surfaceError("surface.storage_invalid", "Stored Interface definition is invalid.")
	}
	if definition.InterfaceId != item.LogicalID {
		return wb.InterfaceSnapshot{}, surfaceError("surface.storage_invalid", "Stored Interface identity does not match its definition.")
	}
	return wb.InterfaceSnapshot{Definition: definition, Revision: item.Revision}, nil
}
