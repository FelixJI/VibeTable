package metadata

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
	"regexp"
	"sort"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"
	mutationpkg "github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const idempotencyTTL = 24 * time.Hour

var (
	logicalIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	idempotencyPattern = regexp.MustCompile(
		`^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$`,
	)
	collectionByNamespace = map[Namespace]string{
		NamespaceSharedSettings:     "vibetable_shared_settings",
		NamespaceDashboards:         "vibetable_dashboards",
		NamespacePanels:             "vibetable_panels",
		NamespacePresets:            "vibetable_presets",
		NamespaceIdentifierMappings: "vibetable_identifier_mappings",
		NamespaceContentVersions:    "vibetable_content_versions",
	}
)

type Service struct {
	app   core.App
	now   func() time.Time
	newID func(kind string) string
}

type Option func(*Service)

func WithIDGenerator(generator func(kind string) string) Option {
	return func(service *Service) {
		service.newID = generator
	}
}

func New(app core.App, options ...Option) *Service {
	service := &Service{
		app: app,
		now: func() time.Time {
			return time.Now().UTC()
		},
		newID: func(kind string) string {
			return kind + "_" + security.RandomString(12)
		},
	}
	for _, option := range options {
		option(service)
	}
	return service
}

type metadataChange struct {
	namespace Namespace
	logicalID string
	operation mutationpkg.OperationKind
	before    *Item
	after     *Item
}

func (service *Service) List(
	ctx context.Context,
	namespace Namespace,
) ([]Item, error) {
	collection, err := resolveCollection(service.app, namespace)
	if err != nil {
		return nil, err
	}
	records, err := service.app.FindRecordsByFilter(
		collection, "", "", 0, 0,
	)
	if err != nil {
		return nil, storageError()
	}
	items := make([]Item, 0, len(records))
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if record.GetString("logical_id") == "" {
			continue
		}
		item, itemErr := itemFromRecord(namespace, record)
		if itemErr != nil {
			return nil, itemErr
		}
		items = append(items, item)
	}
	sort.Slice(items, func(left, right int) bool {
		return items[left].LogicalID < items[right].LogicalID
	})
	return items, nil
}

func (service *Service) Upsert(
	ctx context.Context,
	request UpsertRequest,
) (MutationReceipt, error) {
	canonical, err := validateUpsertRequest(request)
	if err != nil {
		return MutationReceipt{}, err
	}
	request.Payload = canonical
	requestHash, err := hashValue(struct {
		Operation        string          `json:"operation"`
		Namespace        Namespace       `json:"namespace"`
		LogicalID        string          `json:"logicalId"`
		Payload          json.RawMessage `json:"payload"`
		ExpectedRevision string          `json:"expectedRevision"`
	}{
		Operation: "upsert", Namespace: request.Namespace,
		LogicalID: request.LogicalID, Payload: request.Payload,
		ExpectedRevision: request.ExpectedRevision,
	})
	if err != nil {
		return MutationReceipt{}, invalidRequest(
			"", "metadata request cannot be canonicalized",
		)
	}
	return executeIdempotent(
		service, ctx, request.IdempotencyKey, requestHash,
		func(txApp core.App) (
			MutationReceipt, []metadataChange, error,
		) {
			item, change, applyErr := service.upsert(
				txApp, request.Namespace, ItemMutation{
					LogicalID:        request.LogicalID,
					Payload:          request.Payload,
					ExpectedRevision: request.ExpectedRevision,
				},
				"", "",
			)
			return MutationReceipt{
				ReceiptTrace: ReceiptTrace{Status: StatusApplied},
				Item:         item,
			}, []metadataChange{change}, applyErr
		},
		func(
			receipt *MutationReceipt,
			changeSetID string,
			emittedEvents []string,
		) {
			receipt.ChangeSetID = changeSetID
			receipt.EmittedEvents = emittedEvents
		},
		func(receipt *MutationReceipt) {
			receipt.Status = StatusReplayed
		},
	)
}

func (service *Service) Delete(
	ctx context.Context,
	request DeleteRequest,
) (DeleteReceipt, error) {
	if err := validateNamespace(request.Namespace); err != nil {
		return DeleteReceipt{}, err
	}
	if err := validateLogicalID(request.LogicalID, "logicalId"); err != nil {
		return DeleteReceipt{}, err
	}
	if err := validateExpectedRevision(
		request.ExpectedRevision, "expectedRevision", false,
	); err != nil {
		return DeleteReceipt{}, err
	}
	if err := validateIdempotencyKey(request.IdempotencyKey); err != nil {
		return DeleteReceipt{}, err
	}
	requestHash, err := hashValue(struct {
		Operation        string    `json:"operation"`
		Namespace        Namespace `json:"namespace"`
		LogicalID        string    `json:"logicalId"`
		ExpectedRevision string    `json:"expectedRevision"`
	}{
		Operation: "delete", Namespace: request.Namespace,
		LogicalID:        request.LogicalID,
		ExpectedRevision: request.ExpectedRevision,
	})
	if err != nil {
		return DeleteReceipt{}, invalidRequest(
			"", "metadata request cannot be canonicalized",
		)
	}
	return executeIdempotent(
		service, ctx, request.IdempotencyKey, requestHash,
		func(txApp core.App) (
			DeleteReceipt, []metadataChange, error,
		) {
			changes := []metadataChange{}
			if request.Namespace == NamespaceDashboards {
				panelCollection, resolveErr := resolveCollection(
					txApp, NamespacePanels,
				)
				if resolveErr != nil {
					return DeleteReceipt{}, nil, resolveErr
				}
				panelRecords, findErr := txApp.FindRecordsByFilter(
					panelCollection,
					"dashboard_id={:dashboard}",
					"+logical_id",
					0,
					0,
					dbx.Params{"dashboard": request.LogicalID},
				)
				if findErr != nil {
					return DeleteReceipt{}, nil, storageError()
				}
				for _, panelRecord := range panelRecords {
					panel, itemErr := itemFromRecord(
						NamespacePanels, panelRecord,
					)
					if itemErr != nil {
						return DeleteReceipt{}, nil, itemErr
					}
					if deleteErr := txApp.Delete(panelRecord); deleteErr != nil {
						return DeleteReceipt{}, nil, storageError()
					}
					changes = append(changes, metadataChange{
						namespace: NamespacePanels,
						logicalID: panel.LogicalID,
						operation: mutationpkg.OperationDelete,
						before:    &panel,
					})
				}
			}
			change, err := service.delete(
				txApp, request.Namespace, ItemDelete{
					LogicalID:        request.LogicalID,
					ExpectedRevision: request.ExpectedRevision,
				},
				"", "",
			)
			if err != nil {
				return DeleteReceipt{}, nil, err
			}
			changes = append(changes, change)
			return DeleteReceipt{
				ReceiptTrace: ReceiptTrace{Status: StatusApplied},
				Namespace:    request.Namespace,
				LogicalID:    request.LogicalID,
				Deleted:      true,
			}, changes, nil
		},
		func(
			receipt *DeleteReceipt,
			changeSetID string,
			emittedEvents []string,
		) {
			receipt.ChangeSetID = changeSetID
			receipt.EmittedEvents = emittedEvents
		},
		func(receipt *DeleteReceipt) {
			receipt.Status = StatusReplayed
		},
	)
}

func (service *Service) CommitDashboard(
	ctx context.Context,
	request DashboardCommitRequest,
) (DashboardCommitReceipt, error) {
	if err := validateIdempotencyKey(request.IdempotencyKey); err != nil {
		return DashboardCommitReceipt{}, err
	}
	dashboard, err := canonicalMutation(
		request.Dashboard, "dashboard",
	)
	if err != nil {
		return DashboardCommitReceipt{}, dashboardError(err)
	}
	panels := make([]ItemMutation, len(request.Panels))
	seen := map[string]string{}
	for index, panel := range request.Panels {
		path := "panels[" + integerString(index) + "]"
		normalized, normalizeErr := canonicalMutation(panel, path)
		if normalizeErr != nil {
			return DashboardCommitReceipt{}, dashboardError(normalizeErr)
		}
		if previous, exists := seen[normalized.LogicalID]; exists {
			return DashboardCommitReceipt{}, dashboardInvalid(
				path+".logicalId",
				"panel logicalId is duplicated by "+previous,
			)
		}
		seen[normalized.LogicalID] = path
		panels[index] = normalized
	}
	deletes := append([]ItemDelete(nil), request.DeletePanels...)
	for index, panel := range deletes {
		path := "deletePanels[" + integerString(index) + "]"
		if err := validateLogicalID(
			panel.LogicalID, path+".logicalId",
		); err != nil {
			return DashboardCommitReceipt{}, dashboardError(err)
		}
		if err := validateExpectedRevision(
			panel.ExpectedRevision,
			path+".expectedRevision",
			false,
		); err != nil {
			return DashboardCommitReceipt{}, dashboardError(err)
		}
		if previous, exists := seen[panel.LogicalID]; exists {
			return DashboardCommitReceipt{}, dashboardInvalid(
				path+".logicalId",
				"panel logicalId is duplicated by "+previous,
			)
		}
		seen[panel.LogicalID] = path
	}
	request.Dashboard = dashboard
	request.Panels = panels
	request.DeletePanels = deletes
	requestHash, err := hashValue(struct {
		Operation    string         `json:"operation"`
		Dashboard    ItemMutation   `json:"dashboard"`
		Panels       []ItemMutation `json:"panels"`
		DeletePanels []ItemDelete   `json:"deletePanels"`
	}{
		Operation:    "dashboard.commit",
		Dashboard:    request.Dashboard,
		Panels:       request.Panels,
		DeletePanels: request.DeletePanels,
	})
	if err != nil {
		return DashboardCommitReceipt{}, dashboardInvalid(
			"", "dashboard request cannot be canonicalized",
		)
	}
	return executeIdempotent(
		service, ctx, request.IdempotencyKey, requestHash,
		func(txApp core.App) (
			DashboardCommitReceipt, []metadataChange, error,
		) {
			changes := make([]metadataChange, 0,
				1+len(request.Panels)+len(request.DeletePanels),
			)
			dashboardItem, dashboardChange, applyErr := service.upsert(
				txApp, NamespaceDashboards, request.Dashboard,
				"dashboard", "",
			)
			if applyErr != nil {
				return DashboardCommitReceipt{}, nil, applyErr
			}
			changes = append(changes, dashboardChange)
			panelItems := make([]Item, 0, len(request.Panels))
			for index, panel := range request.Panels {
				item, panelChange, panelErr := service.upsert(
					txApp, NamespacePanels, panel,
					"panels["+integerString(index)+"]",
					request.Dashboard.LogicalID,
				)
				if panelErr != nil {
					return DashboardCommitReceipt{}, nil, panelErr
				}
				panelItems = append(panelItems, item)
				changes = append(changes, panelChange)
			}
			deletedIDs := make(
				[]string, 0, len(request.DeletePanels),
			)
			for index, panel := range request.DeletePanels {
				panelChange, deleteErr := service.delete(
					txApp, NamespacePanels, panel,
					"deletePanels["+integerString(index)+"]",
					request.Dashboard.LogicalID,
				)
				if deleteErr != nil {
					return DashboardCommitReceipt{}, nil, deleteErr
				}
				deletedIDs = append(deletedIDs, panel.LogicalID)
				changes = append(changes, panelChange)
			}
			return DashboardCommitReceipt{
				ReceiptTrace: ReceiptTrace{Status: StatusApplied},
				Dashboard:    dashboardItem,
				Panels:       panelItems, DeletedPanelIDs: deletedIDs,
			}, changes, nil
		},
		func(
			receipt *DashboardCommitReceipt,
			changeSetID string,
			emittedEvents []string,
		) {
			receipt.ChangeSetID = changeSetID
			receipt.EmittedEvents = emittedEvents
		},
		func(receipt *DashboardCommitReceipt) {
			receipt.Status = StatusReplayed
		},
	)
}

func (service *Service) upsert(
	app core.App,
	namespace Namespace,
	mutation ItemMutation,
	pathPrefix string,
	dashboardID string,
) (Item, metadataChange, error) {
	collection, err := resolveCollection(app, namespace)
	if err != nil {
		return Item{}, metadataChange{}, err
	}
	record, err := findRecord(app, collection, mutation.LogicalID)
	if err != nil {
		return Item{}, metadataChange{}, err
	}
	actualRevision := ""
	var before *Item
	if record != nil {
		if namespace == NamespacePanels &&
			dashboardID != "" &&
			record.GetString("dashboard_id") != dashboardID {
			return Item{}, metadataChange{}, dashboardInvalid(
				joinPath(pathPrefix, "logicalId"),
				"panel belongs to another dashboard",
			)
		}
		item, itemErr := itemFromRecord(namespace, record)
		if itemErr != nil {
			return Item{}, metadataChange{}, itemErr
		}
		actualRevision = item.Revision
		before = &item
	}
	if actualRevision != mutation.ExpectedRevision {
		return Item{}, metadataChange{}, revisionConflict(
			joinPath(pathPrefix, "expectedRevision"),
			mutation.ExpectedRevision, actualRevision,
		)
	}
	if record == nil {
		record = core.NewRecord(collection)
	}
	record.Set("logical_id", mutation.LogicalID)
	record.Set("payload_json", pbtypes.JSONRaw(mutation.Payload))
	setLegacyFields(
		record, namespace, mutation.LogicalID,
		mutation.Payload, dashboardID, service.now(),
	)
	if err := app.Save(record); err != nil {
		return Item{}, metadataChange{}, storageError()
	}
	item, err := itemFromRecord(namespace, record)
	if err != nil {
		return Item{}, metadataChange{}, err
	}
	operation := mutationpkg.OperationInsert
	if before != nil {
		operation = mutationpkg.OperationUpdate
	}
	return item, metadataChange{
		namespace: namespace,
		logicalID: mutation.LogicalID,
		operation: operation,
		before:    before,
		after:     &item,
	}, nil
}

func (service *Service) delete(
	app core.App,
	namespace Namespace,
	deletion ItemDelete,
	pathPrefix string,
	dashboardID string,
) (metadataChange, error) {
	collection, err := resolveCollection(app, namespace)
	if err != nil {
		return metadataChange{}, err
	}
	record, err := findRecord(app, collection, deletion.LogicalID)
	if err != nil {
		return metadataChange{}, err
	}
	if record == nil {
		return metadataChange{}, &Error{
			Code:    "metadata.not_found",
			Path:    joinPath(pathPrefix, "logicalId"),
			Message: "metadata item was not found",
		}
	}
	if namespace == NamespacePanels &&
		dashboardID != "" &&
		record.GetString("dashboard_id") != dashboardID {
		return metadataChange{}, dashboardInvalid(
			joinPath(pathPrefix, "logicalId"),
			"panel belongs to another dashboard",
		)
	}
	item, err := itemFromRecord(namespace, record)
	if err != nil {
		return metadataChange{}, err
	}
	if item.Revision != deletion.ExpectedRevision {
		return metadataChange{}, revisionConflict(
			joinPath(pathPrefix, "expectedRevision"),
			deletion.ExpectedRevision, item.Revision,
		)
	}
	if err := app.Delete(record); err != nil {
		return metadataChange{}, storageError()
	}
	return metadataChange{
		namespace: namespace,
		logicalID: deletion.LogicalID,
		operation: mutationpkg.OperationDelete,
		before:    &item,
	}, nil
}

func executeIdempotent[T any](
	service *Service,
	ctx context.Context,
	key string,
	requestHash string,
	apply func(core.App) (T, []metadataChange, error),
	decorate func(*T, string, []string),
	markReplayed func(*T),
) (T, error) {
	var result T
	err := service.app.RunInTransaction(func(txApp core.App) (transactionErr error) {
		defer func() {
			if transactionErr == nil {
				transactionErr = writecoordinator.PersistPocketBaseReceipt(
					ctx,
					txApp,
					service.now(),
				)
			}
		}()
		if err := ctx.Err(); err != nil {
			return err
		}
		idempotencyKey := "metadata:" + key
		stored, err := txApp.FindFirstRecordByFilter(
			"vibetable_idempotency_keys",
			"key={:key}",
			dbx.Params{"key": idempotencyKey},
		)
		if err == nil {
			if !stored.GetDateTime("expires_at").Time().After(
				service.now(),
			) {
				if deleteErr := txApp.Delete(stored); deleteErr != nil {
					return storageError()
				}
				err = sql.ErrNoRows
			} else {
				if stored.GetString("request_hash") != requestHash {
					return &Error{
						Code:    "metadata.idempotency_conflict",
						Path:    "idempotencyKey",
						Message: "idempotency key was used for another request",
					}
				}
				if stored.GetString("status") != "applied" {
					return storageError()
				}
				raw, marshalErr := json.Marshal(
					stored.GetRaw("receipt_json"),
				)
				if marshalErr != nil ||
					json.Unmarshal(raw, &result) != nil {
					return storageError()
				}
				markReplayed(&result)
				return nil
			}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return storageError()
		}
		applied, changes, applyErr := apply(txApp)
		if applyErr != nil {
			return applyErr
		}
		changeSetID := service.newID("changeSet")
		emittedEvents, traceErr := service.saveTrace(
			txApp,
			changeSetID,
			"metadata:"+key,
			changes,
		)
		if traceErr != nil {
			return traceErr
		}
		decorate(&applied, changeSetID, emittedEvents)
		result = applied
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return storageError()
		}
		collection, findErr := txApp.FindCollectionByNameOrId(
			"vibetable_idempotency_keys",
		)
		if findErr != nil {
			return storageError()
		}
		stored = core.NewRecord(collection)
		stored.Set("key", idempotencyKey)
		stored.Set("request_hash", requestHash)
		stored.Set("status", "applied")
		stored.Set("receipt_json", pbtypes.JSONRaw(raw))
		stored.Set("expires_at", service.now().Add(idempotencyTTL))
		if saveErr := txApp.Save(stored); saveErr != nil {
			return storageError()
		}
		return nil
	})
	return result, err
}

func (service *Service) saveTrace(
	app core.App,
	changeSetID string,
	requestID string,
	changes []metadataChange,
) ([]string, error) {
	if changeSetID == "" || len(changes) == 0 {
		return nil, storageError()
	}
	orderedNamespaces := make([]Namespace, 0, len(changes))
	grouped := make(map[Namespace][]metadataChange, len(changes))
	for _, change := range changes {
		if _, exists := grouped[change.namespace]; !exists {
			orderedNamespaces = append(
				orderedNamespaces, change.namespace,
			)
		}
		grouped[change.namespace] = append(
			grouped[change.namespace], change,
		)
	}
	revisions := make(map[Namespace]int64, len(grouped))
	for _, namespace := range orderedNamespaces {
		revision, err := nextMetadataRevision(app, namespace)
		if err != nil {
			return nil, err
		}
		revisions[namespace] = revision
	}
	occurredAt := service.now()
	sequence := 0
	for _, change := range changes {
		sequence++
		if err := saveMetadataAudit(
			app,
			changeSetID,
			sequence,
			revisions[change.namespace],
			requestID,
			change,
			occurredAt,
		); err != nil {
			return nil, err
		}
	}
	eventIDs := make([]string, 0, len(orderedNamespaces))
	for _, namespace := range orderedNamespaces {
		eventID := service.newID("event")
		event := metadataChangedEvent(
			eventID,
			changeSetID,
			namespace,
			revisions[namespace],
			grouped[namespace],
			occurredAt,
		)
		if err := saveMetadataOutbox(app, event); err != nil {
			return nil, err
		}
		eventIDs = append(eventIDs, eventID)
	}
	return eventIDs, nil
}

func nextMetadataRevision(
	app core.App,
	namespace Namespace,
) (int64, error) {
	var current int64
	if err := app.ConcurrentDB().NewQuery(`
		SELECT COUNT(DISTINCT change_set_id)
		FROM vibetable_audit_events
		WHERE table_id = {:table}
	`).Bind(dbx.Params{
		"table": metadataTableID(namespace),
	}).Row(&current); err != nil || current < 0 ||
		current >= 1<<53-1 {
		return 0, storageError()
	}
	return current + 1, nil
}

func saveMetadataAudit(
	app core.App,
	changeSetID string,
	sequence int,
	dataRevision int64,
	requestID string,
	change metadataChange,
	occurredAt time.Time,
) error {
	collection, err := app.FindCollectionByNameOrId(
		"vibetable_audit_events",
	)
	if err != nil {
		return storageError()
	}
	record := core.NewRecord(collection)
	record.Set("change_set_id", changeSetID)
	record.Set("sequence", sequence)
	record.Set("data_revision", dataRevision)
	record.Set("table_id", metadataTableID(change.namespace))
	record.Set("record_id", change.logicalID)
	record.Set("operation", string(change.operation))
	if err := setAuditImage(record, "before_json", change.before); err != nil {
		return err
	}
	if err := setAuditImage(record, "after_json", change.after); err != nil {
		return err
	}
	record.Set("schema_revision", 1)
	record.Set("request_id", requestID)
	record.Set("actor_type", "system")
	record.Set("actor_id", "vibetable.metadata")
	record.Set("occurred_at", occurredAt.UTC())
	if err := app.Save(record); err != nil {
		return storageError()
	}
	return nil
}

func setAuditImage(
	record *core.Record,
	field string,
	item *Item,
) error {
	if item == nil {
		return nil
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return storageError()
	}
	record.Set(field, pbtypes.JSONRaw(raw))
	return nil
}

func metadataChangedEvent(
	eventID string,
	changeSetID string,
	namespace Namespace,
	dataRevision int64,
	changes []metadataChange,
	occurredAt time.Time,
) mutationpkg.DataChangedEvent {
	recordIDs := make([]string, 0, len(changes))
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		if _, exists := seen[change.logicalID]; exists {
			continue
		}
		seen[change.logicalID] = struct{}{}
		recordIDs = append(recordIDs, change.logicalID)
	}
	return mutationpkg.DataChangedEvent{
		ContractVersion: mutationpkg.ContractVersion,
		Topic:           "data.changed",
		EventID:         eventID,
		Sequence:        dataRevision,
		OccurredAt:      occurredAt.UTC().Format(time.RFC3339),
		SchemaRevision:  "schema_0001",
		DataRevision:    fmt.Sprintf("data_%04d", dataRevision),
		ChangeSetID:     &changeSetID,
		TableID:         metadataTableID(namespace),
		RecordIDs:       recordIDs,
		Operation:       metadataEventOperation(changes),
	}
}

func metadataEventOperation(
	changes []metadataChange,
) mutationpkg.DataChangeOperation {
	if len(changes) == 0 {
		return mutationpkg.DataChangeUpdate
	}
	first := changes[0].operation
	for _, change := range changes[1:] {
		if change.operation != first {
			return mutationpkg.DataChangeUpdate
		}
	}
	switch first {
	case mutationpkg.OperationInsert:
		return mutationpkg.DataChangeInsert
	case mutationpkg.OperationDelete:
		return mutationpkg.DataChangeDelete
	default:
		return mutationpkg.DataChangeUpdate
	}
}

func saveMetadataOutbox(
	app core.App,
	event mutationpkg.DataChangedEvent,
) error {
	collection, err := app.FindCollectionByNameOrId(
		"vibetable_outbox",
	)
	if err != nil {
		return storageError()
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return storageError()
	}
	record := core.NewRecord(collection)
	record.Set("event_id", event.EventID)
	record.Set("topic", event.Topic)
	record.Set("payload_json", pbtypes.JSONRaw(raw))
	record.Set("status", "pending")
	record.Set("attempts", 0)
	if err := app.Save(record); err != nil {
		return storageError()
	}
	return nil
}

func metadataTableID(namespace Namespace) string {
	return "metadata:" + string(namespace)
}

func findRecord(
	app core.App,
	collection *core.Collection,
	logicalID string,
) (*core.Record, error) {
	record, err := app.FindFirstRecordByFilter(
		collection,
		"logical_id={:logicalId}",
		dbx.Params{"logicalId": logicalID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError()
	}
	return record, nil
}

func itemFromRecord(
	namespace Namespace,
	record *core.Record,
) (Item, error) {
	raw, err := json.Marshal(record.GetRaw("payload_json"))
	if err != nil {
		return Item{}, storageError()
	}
	canonical, revision, err := canonicalPayload(raw)
	if err != nil {
		return Item{}, storageError()
	}
	return Item{
		Namespace: namespace,
		LogicalID: record.GetString("logical_id"),
		Payload:   canonical,
		Revision:  revision,
	}, nil
}

func validateUpsertRequest(
	request UpsertRequest,
) (json.RawMessage, error) {
	if err := validateNamespace(request.Namespace); err != nil {
		return nil, err
	}
	if err := validateLogicalID(request.LogicalID, "logicalId"); err != nil {
		return nil, err
	}
	if err := validateExpectedRevision(
		request.ExpectedRevision, "expectedRevision", true,
	); err != nil {
		return nil, err
	}
	if err := validateIdempotencyKey(request.IdempotencyKey); err != nil {
		return nil, err
	}
	canonical, _, err := canonicalPayload(request.Payload)
	if err != nil {
		return nil, invalidRequest("payload", "payload must be one JSON value")
	}
	return canonical, nil
}

func canonicalMutation(
	mutation ItemMutation,
	path string,
) (ItemMutation, error) {
	if err := validateLogicalID(
		mutation.LogicalID, joinPath(path, "logicalId"),
	); err != nil {
		return ItemMutation{}, err
	}
	if err := validateExpectedRevision(
		mutation.ExpectedRevision,
		joinPath(path, "expectedRevision"),
		true,
	); err != nil {
		return ItemMutation{}, err
	}
	canonical, _, err := canonicalPayload(mutation.Payload)
	if err != nil {
		return ItemMutation{}, invalidRequest(
			joinPath(path, "payload"),
			"payload must be one JSON value",
		)
	}
	mutation.Payload = canonical
	return mutation, nil
}

func canonicalPayload(
	raw []byte,
) (json.RawMessage, string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, "", io.EOF
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, "", err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, "", errors.New("multiple JSON values")
		}
		return nil, "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical,
		"sha256:" + hex.EncodeToString(sum[:]),
		nil
}

func hashValue(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func resolveCollection(
	app core.App,
	namespace Namespace,
) (*core.Collection, error) {
	if err := validateNamespace(namespace); err != nil {
		return nil, err
	}
	collection, err := app.FindCollectionByNameOrId(
		collectionByNamespace[namespace],
	)
	if err != nil {
		return nil, storageError()
	}
	return collection, nil
}

func validateNamespace(namespace Namespace) error {
	if _, ok := collectionByNamespace[namespace]; !ok {
		return &Error{
			Code:    "metadata.namespace.invalid",
			Path:    "namespace",
			Message: "metadata namespace is not allowlisted",
		}
	}
	return nil
}

func validateLogicalID(value, path string) error {
	if !logicalIDPattern.MatchString(value) {
		return invalidRequest(
			path,
			"logicalId must use 1-128 safe identifier characters",
		)
	}
	return nil
}

func validateExpectedRevision(
	value string,
	path string,
	allowCreate bool,
) error {
	if value == "" && allowCreate {
		return nil
	}
	if len(value) != 71 ||
		!stringsHasOnlyHex(value[7:]) ||
		value[:7] != "sha256:" {
		return invalidRequest(
			path,
			"expectedRevision must be an sha256 content revision",
		)
	}
	return nil
}

func validateIdempotencyKey(value string) error {
	if !idempotencyPattern.MatchString(value) {
		return invalidRequest(
			"idempotencyKey",
			"idempotencyKey must use 1-192 safe identifier characters",
		)
	}
	return nil
}

func stringsHasOnlyHex(value string) bool {
	for _, char := range value {
		if !((char >= '0' && char <= '9') ||
			(char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func setLegacyFields(
	record *core.Record,
	namespace Namespace,
	logicalID string,
	payload json.RawMessage,
	dashboardID string,
	now time.Time,
) {
	nextRevision := record.GetInt("revision") + 1
	if nextRevision < 1 {
		nextRevision = 1
	}
	switch namespace {
	case NamespaceSharedSettings:
		record.Set("key", logicalID)
		record.Set("value_json", pbtypes.JSONRaw(payload))
		record.Set("revision", nextRevision)
	case NamespaceDashboards:
		record.Set("dashboard_id", logicalID)
		record.Set("layout_json", pbtypes.JSONRaw(payload))
		record.Set("revision", nextRevision)
	case NamespacePanels:
		record.Set("panel_id", logicalID)
		if dashboardID == "" &&
			record.GetString("dashboard_id") == "" {
			dashboardID = "metadata"
		}
		if dashboardID != "" {
			record.Set("dashboard_id", dashboardID)
		}
		record.Set("query_json", pbtypes.JSONRaw(payload))
		record.Set("revision", nextRevision)
	case NamespacePresets:
		record.Set("preset_id", logicalID)
		record.Set("scope", "metadata")
		record.Set("projection_json", pbtypes.JSONRaw(payload))
		record.Set("revision", nextRevision)
	case NamespaceIdentifierMappings:
		record.Set("entity_kind", "metadata")
		record.Set("physical_name", logicalID)
		record.Set("display_name", logicalID)
		record.Set("origin", "metadata")
		record.Set("status", "active")
	case NamespaceContentVersions:
		record.Set("table_id", "metadata")
		record.Set("record_id", logicalID)
		record.Set("name", logicalID)
		record.Set("change_set_id", logicalID)
		if record.GetString("created_at") == "" {
			record.Set("created_at", now)
		}
	}
}

func revisionConflict(
	path, expected, actual string,
) *Error {
	return &Error{
		Code:    "metadata.revision_conflict",
		Path:    path,
		Message: "metadata revision does not match",
		Details: map[string]any{
			"expected": expected,
			"actual":   actual,
		},
	}
}

func invalidRequest(path, message string) *Error {
	return &Error{
		Code: "metadata.request.invalid",
		Path: path, Message: message,
	}
}

func dashboardInvalid(path, message string) *Error {
	return &Error{
		Code: "metadata.dashboard.invalid",
		Path: path, Message: message,
	}
}

func dashboardError(err error) error {
	var productErr *Error
	if !errors.As(err, &productErr) {
		return err
	}
	if productErr.Code == "metadata.request.invalid" {
		productErr.Code = "metadata.dashboard.invalid"
	}
	return productErr
}

func storageError() *Error {
	return &Error{
		Code:      "metadata.storage.failed",
		Message:   "metadata storage operation failed",
		Retryable: true,
	}
}

func joinPath(prefix, field string) string {
	if prefix == "" {
		return field
	}
	return prefix + "." + field
}

func integerString(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
