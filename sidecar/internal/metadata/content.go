package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

// ContentService owns profile and record/document link validation, CAS and durable
// results. Its query source always receives the metadata transaction's app handle.
type ContentService struct {
	store    *Service
	source   query.Source
	describe func(context.Context, core.App, string) (v2.SchemaSnapshot, error)
}

func NewContentService(app core.App, source query.Source) *ContentService {
	return &ContentService{store: New(app), source: source, describe: func(ctx context.Context, tx core.App, tableID string) (v2.SchemaSnapshot, error) {
		table, err := schemaapi.New(tx).Describe(ctx, tableID)
		return table.Snapshot, err
	}}
}

type ContentError struct {
	Code    string
	Path    *string
	Message string
}

func (err *ContentError) Error() string { return err.Message }
func contentError(code, message string, path ...string) *ContentError {
	err := &ContentError{Code: code, Message: message}
	if len(path) > 0 {
		err.Path = &path[0]
	}
	return err
}
func contentConflict(code string) error {
	return contentError(code, "Content metadata changed elsewhere.")
}

func (service *ContentService) LoadProfile(ctx context.Context, tableID string) (workbench.ContentProfileSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return workbench.ContentProfileSnapshot{}, err
	}
	item, err := service.current(service.store.app, NamespaceContentProfiles, tableID)
	if err != nil {
		return workbench.ContentProfileSnapshot{}, err
	}
	if item == nil {
		return workbench.ContentProfileSnapshot{}, contentError("content_profile.not_found", "Content profile not found.")
	}
	return profileSnapshot(*item)
}

func (service *ContentService) CommitProfile(ctx context.Context, request workbench.ContentProfileCommitRequest) (workbench.ContentProfileSnapshot, error) {
	receipt, err := service.write(ctx, "contentProfile.commit", request.IdempotencyKey, request, func(tx core.App) (Item, metadataChange, error) {
		if err := service.validateProfile(ctx, tx, request.Profile); err != nil {
			return Item{}, metadataChange{}, err
		}
		if request.ExpectedRevision != nil && *request.ExpectedRevision == "" {
			return Item{}, metadataChange{}, contentConflict("content_profile.edit_conflict")
		}
		return service.replace(tx, NamespaceContentProfiles, request.Profile.TableId, request.Profile, optionalRevision(request.ExpectedRevision), "content_profile.edit_conflict")
	})
	if err != nil && err != writecoordinator.ErrBusinessReplay {
		return workbench.ContentProfileSnapshot{}, err
	}
	snapshot, projectionErr := profileSnapshot(receipt.Item)
	if projectionErr != nil {
		return workbench.ContentProfileSnapshot{}, projectionErr
	}
	return snapshot, err
}

func (service *ContentService) DeleteProfile(ctx context.Context, request workbench.ContentProfileDeleteRequest) (workbench.ContentProfileDeleteResult, error) {
	err := service.remove(ctx, "contentProfile.delete", NamespaceContentProfiles, request.TableId, request.ExpectedRevision, request.IdempotencyKey, request)
	return workbench.ContentProfileDeleteResult{TableId: request.TableId}, err
}

func (service *ContentService) ListLinks(ctx context.Context, tableID, recordID string) (workbench.RecordDocumentLinkListResult, error) {
	result := workbench.RecordDocumentLinkListResult{Items: []workbench.RecordDocumentLinkSnapshot{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	items, err := service.store.List(ctx, NamespaceRecordDocumentLinks)
	if err != nil {
		return result, err
	}
	for _, item := range items {
		snapshot, err := linkSnapshot(item)
		if err != nil {
			return result, err
		}
		if snapshot.Link.TableId == tableID && snapshot.Link.RecordId == recordID {
			result.Items = append(result.Items, snapshot)
		}
	}
	sort.Slice(result.Items, func(i, j int) bool {
		a, b := result.Items[i].Link, result.Items[j].Link
		if a.Order == b.Order {
			return a.LinkId < b.LinkId
		}
		return a.Order < b.Order
	})
	return result, nil
}

func (service *ContentService) CommitLink(ctx context.Context, request workbench.RecordDocumentLinkCommitRequest) (workbench.RecordDocumentLinkSnapshot, error) {
	receipt, err := service.write(ctx, "recordDocumentLink.commit", request.IdempotencyKey, request, func(tx core.App) (Item, metadataChange, error) {
		rows, err := query.NewPort(tx, service.source).ReadRows(ctx, request.Link.TableId, []string{request.Link.RecordId})
		if err != nil {
			if ctx.Err() != nil {
				return Item{}, metadataChange{}, ctx.Err()
			}
			return Item{}, metadataChange{}, contentError("record_document_link.record_lookup_failed", "Record could not be validated.")
		}
		if len(rows) != 1 {
			return Item{}, metadataChange{}, contentError("record_document_link.record_missing", "Record does not exist.", "recordId")
		}
		if request.ExpectedRevision != nil && *request.ExpectedRevision == "" {
			return Item{}, metadataChange{}, contentConflict("record_document_link.edit_conflict")
		}
		return service.replace(tx, NamespaceRecordDocumentLinks, request.Link.LinkId, request.Link, optionalRevision(request.ExpectedRevision), "record_document_link.edit_conflict")
	})
	if err != nil && err != writecoordinator.ErrBusinessReplay {
		return workbench.RecordDocumentLinkSnapshot{}, err
	}
	snapshot, projectionErr := linkSnapshot(receipt.Item)
	if projectionErr != nil {
		return workbench.RecordDocumentLinkSnapshot{}, projectionErr
	}
	return snapshot, err
}

func (service *ContentService) RepairLink(ctx context.Context, request workbench.RecordDocumentLinkRepairRequest) (workbench.RecordDocumentLinkSnapshot, error) {
	receipt, err := service.write(ctx, "recordDocumentLink.repair", request.IdempotencyKey, request, func(tx core.App) (Item, metadataChange, error) {
		current, err := service.current(tx, NamespaceRecordDocumentLinks, request.LinkId)
		if err != nil {
			return Item{}, metadataChange{}, err
		}
		if current == nil {
			return Item{}, metadataChange{}, contentError("record_document_link.not_found", "Link not found.")
		}
		if current.Revision != request.ExpectedRevision {
			return Item{}, metadataChange{}, contentConflict("record_document_link.edit_conflict")
		}
		snapshot, err := linkSnapshot(*current)
		if err != nil {
			return Item{}, metadataChange{}, err
		}
		snapshot.Link.DocumentId = request.DocumentId
		return service.replace(tx, NamespaceRecordDocumentLinks, request.LinkId, snapshot.Link, request.ExpectedRevision, "record_document_link.edit_conflict")
	})
	if err != nil && err != writecoordinator.ErrBusinessReplay {
		return workbench.RecordDocumentLinkSnapshot{}, err
	}
	snapshot, projectionErr := linkSnapshot(receipt.Item)
	if projectionErr != nil {
		return workbench.RecordDocumentLinkSnapshot{}, projectionErr
	}
	return snapshot, err
}

func (service *ContentService) DeleteLink(ctx context.Context, request workbench.RecordDocumentLinkDeleteRequest) (workbench.RecordDocumentLinkDeleteResult, error) {
	err := service.remove(ctx, "recordDocumentLink.delete", NamespaceRecordDocumentLinks, request.LinkId, request.ExpectedRevision, request.IdempotencyKey, request)
	return workbench.RecordDocumentLinkDeleteResult{LinkId: request.LinkId}, err
}

func (service *ContentService) current(app core.App, namespace Namespace, logicalID string) (*Item, error) {
	collection, err := resolveCollection(app, namespace)
	if err != nil {
		return nil, err
	}
	record, err := findRecord(app, collection, logicalID)
	if err != nil || record == nil {
		return nil, err
	}
	item, err := itemFromRecord(namespace, record)
	return &item, err
}

func (service *ContentService) replace(tx core.App, namespace Namespace, logicalID string, value any, expected, conflictCode string) (Item, metadataChange, error) {
	current, err := service.current(tx, namespace, logicalID)
	if err != nil {
		return Item{}, metadataChange{}, err
	}
	actual := ""
	if current != nil {
		actual = current.Revision
	}
	if actual != expected {
		return Item{}, metadataChange{}, contentConflict(conflictCode)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return Item{}, metadataChange{}, err
	}
	canonical, err := validateUpsertRequest(UpsertRequest{Namespace: namespace, LogicalID: logicalID, Payload: payload, ExpectedRevision: expected, IdempotencyKey: "content-validation"})
	if err != nil {
		return Item{}, metadataChange{}, err
	}
	return service.store.upsert(tx, namespace, ItemMutation{LogicalID: logicalID, Payload: canonical, ExpectedRevision: expected}, "", "")
}

func (service *ContentService) write(ctx context.Context, method, key string, request any, apply func(core.App) (Item, metadataChange, error)) (MutationReceipt, error) {
	if err := ctx.Err(); err != nil {
		return MutationReceipt{}, err
	}
	if err := validateIdempotencyKey(key); err != nil {
		return MutationReceipt{}, contentPersistenceError(err, false)
	}
	requestHash, err := hashValue(struct {
		Method  string `json:"method"`
		Request any    `json:"request"`
	}{method, request})
	if err != nil {
		return MutationReceipt{}, err
	}
	receipt, err := executeIdempotent(service.store, ctx, key, requestHash, func(tx core.App) (MutationReceipt, []metadataChange, error) {
		item, change, err := apply(tx)
		return MutationReceipt{ReceiptTrace: ReceiptTrace{Status: StatusApplied}, Item: item}, []metadataChange{change}, err
	}, func(receipt *MutationReceipt, id string, events []string) {
		receipt.ChangeSetID = id
		receipt.EmittedEvents = events
	}, func(receipt *MutationReceipt) { receipt.Status = StatusReplayed }, func() error { return contentBusinessReplay(ctx, method, key) })
	return receipt, contentPersistenceError(err, false)
}

func (service *ContentService) remove(ctx context.Context, method string, namespace Namespace, logicalID, expected, key string, request any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateIdempotencyKey(key); err != nil {
		return contentPersistenceError(err, true)
	}
	requestHash, err := hashValue(struct {
		Method  string `json:"method"`
		Request any    `json:"request"`
	}{method, request})
	if err != nil {
		return err
	}
	_, err = executeIdempotent(service.store, ctx, key, requestHash, func(tx core.App) (DeleteReceipt, []metadataChange, error) {
		item, err := service.current(tx, namespace, logicalID)
		if err != nil {
			return DeleteReceipt{}, nil, err
		}
		if item == nil {
			return DeleteReceipt{}, nil, contentError("content_model.not_found", "Metadata item not found.")
		}
		if item.Revision != expected {
			return DeleteReceipt{}, nil, contentConflict("content_model.edit_conflict")
		}
		change, err := service.store.delete(tx, namespace, ItemDelete{LogicalID: logicalID, ExpectedRevision: expected}, "", "")
		return DeleteReceipt{ReceiptTrace: ReceiptTrace{Status: StatusApplied}, Namespace: namespace, LogicalID: logicalID, Deleted: true}, []metadataChange{change}, err
	}, func(receipt *DeleteReceipt, id string, events []string) {
		receipt.ChangeSetID = id
		receipt.EmittedEvents = events
	}, func(receipt *DeleteReceipt) { receipt.Status = StatusReplayed }, func() error { return contentBusinessReplay(ctx, method, key) })
	return contentPersistenceError(err, true)
}

func contentPersistenceError(err error, deleting bool) error {
	if err == nil || err == writecoordinator.ErrBusinessReplay {
		return err
	}
	var domain *ContentError
	if errors.As(err, &domain) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if IsError(err, "metadata.revision_conflict") {
		return contentConflict("content_model.edit_conflict")
	}
	if IsError(err, "metadata.idempotency_conflict") {
		return contentError("content_model.idempotency_conflict", "Idempotency key was used for another content request.", "idempotencyKey")
	}
	if deleting {
		return contentError("content_model.persistence_failed", "Metadata delete failed.")
	}
	return contentError("content_model.persistence_failed", "Metadata write failed.")
}

func (service *ContentService) validateProfile(ctx context.Context, tx core.App, profile workbench.ContentProfile) error {
	snapshot, err := service.describe(ctx, tx, profile.TableId)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return contentError("content_profile.table_missing", "Content profile table not found.", "tableId")
	}
	types := map[string]v2.LogicalType{}
	for _, field := range snapshot.Fields {
		types[field.Identity.FieldID] = field.LogicalType
	}
	selected := []string{profile.TitleFieldId, profile.BodyFieldId}
	if profile.SummaryFieldId != nil {
		selected = append(selected, *profile.SummaryFieldId)
	}
	selected = append(selected, profile.SearchableFieldIds...)
	for _, id := range selected {
		if _, ok := types[id]; !ok {
			return contentError("content_profile.field_missing", "Content profile field is missing from SchemaCore.", id)
		}
	}
	text := func(kind v2.LogicalType) bool {
		switch kind {
		case v2.LogicalText, v2.LogicalEditor, v2.LogicalEmail, v2.LogicalURL, v2.LogicalSelect:
			return true
		}
		return false
	}
	if !text(types[profile.TitleFieldId]) {
		return contentError("content_profile.title_type_invalid", "Title field must be text-like.", "titleFieldId")
	}
	if types[profile.BodyFieldId] != v2.LogicalEditor {
		return contentError("content_profile.body_type_invalid", "Body field must be richText or longText.", "bodyFieldId")
	}
	if profile.SummaryFieldId != nil && !text(types[*profile.SummaryFieldId]) {
		return contentError("content_profile.summary_type_invalid", "Summary field must be text-like.", "summaryFieldId")
	}
	seen := map[string]bool{}
	for _, id := range profile.SearchableFieldIds {
		if seen[id] {
			return contentError("content_profile.search_field_duplicate", "Searchable fields must be unique.", "searchableFieldIds")
		}
		seen[id] = true
	}
	for _, id := range profile.SearchableFieldIds {
		if !text(types[id]) {
			return contentError("content_profile.search_field_invalid", "Searchable fields must be text-like.", "searchableFieldIds")
		}
	}
	return nil
}

func decodeStoredContent(method, field string, payload json.RawMessage) (any, error) {
	request, err := json.Marshal(map[string]any{
		field:              payload,
		"expectedRevision": nil,
		"idempotencyKey":   "read",
	})
	if err != nil {
		return nil, err
	}
	return DecodeContentParams(method, request)
}
func profileSnapshot(item Item) (workbench.ContentProfileSnapshot, error) {
	value, err := decodeStoredContent("contentProfile.commit", "profile", item.Payload)
	if err != nil {
		return workbench.ContentProfileSnapshot{}, contentError("content_model.storage_invalid", "Stored profile is invalid.")
	}
	return workbench.ContentProfileSnapshot{Profile: value.(workbench.ContentProfileCommitRequest).Profile, Revision: item.Revision}, nil
}
func linkSnapshot(item Item) (workbench.RecordDocumentLinkSnapshot, error) {
	value, err := decodeStoredContent("recordDocumentLink.commit", "link", item.Payload)
	if err != nil {
		return workbench.RecordDocumentLinkSnapshot{}, contentError("content_model.storage_invalid", "Stored link is invalid.")
	}
	return workbench.RecordDocumentLinkSnapshot{Link: value.(workbench.RecordDocumentLinkCommitRequest).Link, Revision: item.Revision}, nil
}
func optionalRevision(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// Only an exact, validated replay may abort the outer workspace write intent.
func contentBusinessReplay(ctx context.Context, method, key string) error {
	namespace, operation := "content_profiles", "upsert"
	if strings.HasPrefix(method, "recordDocumentLink.") {
		namespace = "record_document_links"
	}
	if strings.HasSuffix(method, ".delete") {
		operation = "delete"
	}
	return writecoordinator.ReplayedBusinessWrite(ctx, "metadata."+namespace+"."+operation, key)
}
