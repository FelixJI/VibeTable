package audit

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

const (
	restoreTTL               = 5 * time.Minute
	maxRestoreToken          = 256
	maxRestorePatchBytes     = 256 << 10
	maxRestoreTokenBytes     = 16 << 20
	maxHistoryLimit          = 100
	maxHistoryScanEvents     = 10_000
	maxArchivedScanRecords   = 10_000
	maxHistorySearchLength   = 512
	maxHistoryActions        = 16
	maxHistoryIdentifierSize = 128
)

type MutationKernel interface {
	Apply(context.Context, mutation.Request) (mutation.Receipt, error)
}

type SchemaSource interface {
	Describe(context.Context, core.App, string) (schema.TableDefinition, error)
}

type Service struct {
	app     core.App
	kernel  MutationKernel
	schemas SchemaSource
	files   AttachmentHistory
	ledger  *auditledger.Ledger
	secret  []byte
	now     func() time.Time

	mu         sync.Mutex
	tokens     map[string]restoreState
	tokenBytes int
}

type restoreState struct {
	tableID        string
	recordID       string
	targetRevision string
	schemaRevision string
	currentDigest  string
	patch          map[string]any
	insert         bool
	scope          string
	archiveField   string
	attachments    []attachments.RestorePlan
	expiresAt      time.Time
	size           int
}

type Option func(*Service)

type AttachmentHistory interface {
	PreviewRestore(
		context.Context,
		core.App,
		schema.TableDefinition,
		string,
		string,
		[]string,
		[]string,
	) (attachments.RestorePlan, error)
	StageRestore(
		context.Context,
		core.App,
		attachments.RestorePlan,
		string,
	) (mutation.AttachmentChange, func(), error)
}

func WithClock(clock func() time.Time) Option {
	return func(service *Service) { service.now = clock }
}

func WithAttachmentHistory(history AttachmentHistory) Option {
	return func(service *Service) { service.files = history }
}

// WithLedgerHistory makes the append-only external ledger authoritative for
// history reads and restore targets. It is required for Workspace V2, whose
// replaceable business database can move backwards during snapshot restore.
func WithLedgerHistory(ledger *auditledger.Ledger) Option {
	return func(service *Service) { service.ledger = ledger }
}

func New(
	app core.App,
	kernel MutationKernel,
	schemas SchemaSource,
	secret []byte,
	options ...Option,
) (*Service, error) {
	if app == nil || kernel == nil || schemas == nil {
		return nil, errors.New("audit service dependencies are required")
	}
	if len(secret) < 32 {
		return nil, errors.New("audit restore secret must contain at least 32 bytes")
	}
	service := &Service{
		app: app, kernel: kernel, schemas: schemas,
		secret: append([]byte(nil), secret...),
		now:    func() time.Time { return time.Now().UTC() },
		tokens: map[string]restoreState{},
	}
	for _, option := range options {
		option(service)
	}
	return service, nil
}

func (service *Service) ReadChangeSets(
	ctx context.Context,
	params ReadParams,
) (Page, error) {
	if err := validateReadParams(params); err != nil {
		return Page{}, err
	}
	definition, err := service.describe(ctx, params.TableID)
	if err != nil {
		return Page{}, err
	}
	fieldName, err := resolveFieldName(definition, params.Field)
	if err != nil {
		return Page{}, err
	}
	recordID := params.RecordID
	if recordID == nil && (params.Scope == "row" || params.Scope == "cell") {
		recordID = params.ItemID
	}
	events, err := service.readHistoryEvents(
		ctx,
		params.TableID,
		recordID,
		params.ActorID,
	)
	if err != nil {
		return Page{}, historyError("history.storage_failed", "history could not be read", true)
	}
	if len(events) > maxHistoryScanEvents {
		return Page{}, historyError(
			"history.resource_limit",
			"history query exceeds the safe scan limit",
			false,
		)
	}
	var archivedIDs map[string]struct{}
	if params.Scope == "archived" {
		archivedIDs, err = service.archivedRecordIDs(definition)
		if err != nil {
			return Page{}, err
		}
	}
	groups, archivedDefaults, err := groupEvents(
		events, definition, fieldName, params, archivedIDs,
	)
	if err != nil {
		return Page{}, err
	}
	total := len(groups)
	start := params.Offset
	if start > total {
		start = total
	}
	end := start + params.Limit
	if end > total {
		end = total
	}
	capabilityHash, err := hashJSON(definition)
	if err != nil {
		return Page{}, historyError("history.storage_failed", "schema identity could not be computed", true)
	}
	return Page{
		Collection: params.TableID, ItemID: params.ItemID,
		ChangeSets: groups[start:end], Total: total,
		CapabilityHash: capabilityHash, SchemaRevision: definition.SchemaRevision,
		Scope: params.Scope, Field: params.Field, HasMore: end < total,
		ArchivedDefaultRevisionIDs: archivedDefaults,
	}, nil
}

func (service *Service) PreviewRestore(
	ctx context.Context,
	params PreviewParams,
) (Preview, error) {
	if params.TableID == "" || params.ItemID == "" || params.TargetRevision == "" ||
		len(params.TableID) > maxHistoryIdentifierSize ||
		len(params.ItemID) > maxHistoryIdentifierSize ||
		len(params.TargetRevision) > maxHistoryIdentifierSize {
		return Preview{}, historyError("restore.request_invalid", "restore scope is incomplete", false)
	}
	if params.Scope == "" {
		params.Scope = "row"
	}
	if params.Scope != "row" && params.Scope != "cell" && params.Scope != "archived" {
		return Preview{}, historyError("restore.scope_invalid", "restore scope is invalid", false)
	}
	if params.Scope == "cell" && params.Field == nil {
		return Preview{}, historyError("restore.request_invalid", "cell restore requires a field", false)
	}
	if params.Field != nil &&
		(len(*params.Field) == 0 || len(*params.Field) > maxHistoryIdentifierSize) {
		return Preview{}, historyError("restore.request_invalid", "restore field is invalid", false)
	}
	definition, err := service.describe(ctx, params.TableID)
	if err != nil {
		return Preview{}, err
	}
	fieldName, err := resolveFieldName(definition, params.Field)
	if err != nil {
		return Preview{}, err
	}
	event, err := service.findHistoryEvent(ctx, params.TargetRevision)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Preview{}, historyError("target_revision_invalid", "target revision was not found", false)
		}
		return Preview{}, historyError("history.storage_failed", "target revision could not be read", true)
	}
	if event.GetString("table_id") != params.TableID ||
		event.GetString("record_id") != params.ItemID {
		return Preview{}, historyError("target_revision_invalid", "target revision belongs to another record", false)
	}
	if err := validateEvent(event); err != nil {
		return Preview{}, err
	}
	targetKey := "after_json"
	operation := mutation.OperationKind(event.GetString("operation"))
	if operation == mutation.OperationDelete || operation == mutation.OperationArchive {
		targetKey = "before_json"
	}
	target, err := decodeImage(event.GetRaw(targetKey))
	if err != nil || target == nil {
		return Preview{}, historyError("target_revision_invalid", "target revision has no restorable image", false)
	}
	current, exists, err := service.readCurrent(definition, params.ItemID)
	if err != nil {
		return Preview{}, err
	}
	archiveField := ""
	if params.Scope == "archived" {
		archiveField, err = archivedFieldName(definition)
		if err != nil {
			return Preview{}, err
		}
		if !exists || !isArchived(definition, archiveField, current[archiveField]) {
			return Preview{}, historyError(
				"restore_scope_mismatch",
				"record is not archived",
				false,
			)
		}
	}
	currentDigest, err := digestRow(current)
	if err != nil {
		return Preview{}, historyError("history.storage_failed", "current row identity could not be computed", true)
	}

	scalarChanges := []ScalarFieldChange{}
	relationChanges := []RelationFieldChange{}
	diagnostics := []Diagnostic{}
	patch := map[string]any{}
	attachmentPlans := []attachments.RestorePlan{}
	restorable := []string{}
	known := make(map[string]schema.FieldDefinition, len(definition.Fields))
	for _, field := range definition.Fields {
		known[field.PhysicalName] = field
	}
	for name := range target {
		if name == "id" {
			continue
		}
		if _, ok := known[name]; !ok {
			diagnostics = append(diagnostics, diagnostic(
				name, "schema_retired", "field_deleted",
				"Field is no longer present in the current schema.",
			))
		}
	}
	for _, field := range definition.Fields {
		if fieldName != nil && field.PhysicalName != *fieldName {
			continue
		}
		targetValue, present := target[field.PhysicalName]
		if !present {
			continue
		}
		currentValue := current[field.PhysicalName]
		if wireEqual(currentValue, targetValue) {
			continue
		}
		if isSensitiveField(field) {
			diagnostics = append(diagnostics, diagnostic(
				field.PhysicalName, "sensitive", "field_sensitive",
				"Sensitive fields are excluded from history restore.",
			))
			continue
		}
		if field.Kind == schema.FieldKindRelation {
			relationChanges = append(relationChanges, relationChange(field, currentValue, targetValue))
			available, availabilityErr := service.relationTargetsAvailable(
				ctx, field, targetValue,
			)
			if availabilityErr != nil {
				return Preview{}, availabilityErr
			}
			if !available {
				relationChanges[len(relationChanges)-1].TargetAvailable = false
				diagnostics = append(diagnostics, diagnostic(
					field.PhysicalName, "incompatible", "relation_target_unavailable",
					"A historical relation target is no longer available.",
				))
				continue
			}
			patch[field.PhysicalName] = targetValue
			restorable = append(restorable, field.PhysicalName)
			continue
		}
		scalarChanges = append(scalarChanges, ScalarFieldChange{
			Field: field.PhysicalName, Before: currentValue, After: targetValue,
		})
		if field.ReadOnly || field.Kind == schema.FieldKindFormula ||
			field.Kind == schema.FieldKindLookup || field.Kind == schema.FieldKindSystem {
			diagnostics = append(diagnostics, diagnostic(
				field.PhysicalName, "derived", "field_generated",
				"Computed and system fields are recalculated and cannot be written directly.",
			))
			continue
		}
		if field.Kind == schema.FieldKindAttachment {
			if service.files == nil {
				diagnostics = append(diagnostics, diagnostic(
					field.PhysicalName, "incompatible", "attachment_requires_manifest",
					"Managed attachment history is unavailable.",
				))
				continue
			}
			plan, planErr := service.files.PreviewRestore(
				ctx,
				service.app,
				definition,
				params.ItemID,
				field.FieldID,
				stringValues(targetValue),
				stringValues(currentValue),
			)
			if planErr != nil {
				return Preview{}, attachmentHistoryError(planErr)
			}
			attachmentPlans = append(attachmentPlans, plan)
			restorable = append(restorable, field.PhysicalName)
			continue
		}
		patch[field.PhysicalName] = targetValue
		restorable = append(restorable, field.PhysicalName)
	}
	sort.Strings(restorable)
	storedPatch, patchSize, err := cloneJSONMap(patch)
	if err != nil {
		return Preview{}, historyError("restore.token_failed", "restore patch could not be secured", true)
	}
	planRaw, err := json.Marshal(attachmentPlans)
	if err != nil {
		return Preview{}, historyError("restore.token_failed", "attachment restore plan could not be secured", true)
	}
	patchSize += len(planRaw)
	if patchSize > maxRestorePatchBytes {
		return Preview{}, historyError(
			"restore.resource_limit",
			"restore patch exceeds the safe size limit",
			false,
		)
	}
	state := restoreState{
		tableID: params.TableID, recordID: params.ItemID,
		targetRevision: params.TargetRevision, schemaRevision: definition.SchemaRevision,
		currentDigest: currentDigest, patch: storedPatch, insert: !exists,
		scope: params.Scope, archiveField: archiveField,
		attachments: attachmentPlans, size: patchSize,
		expiresAt: service.now().UTC().Add(restoreTTL),
	}
	token, err := service.issueToken(state)
	if err != nil {
		var historyErr *Error
		if errors.As(err, &historyErr) {
			return Preview{}, historyErr
		}
		return Preview{}, historyError("restore.token_failed", "restore token could not be created", true)
	}
	return Preview{
		Collection: params.TableID, ItemID: params.ItemID,
		TargetRevision: params.TargetRevision, CurrentHash: currentDigest,
		SchemaRevision: definition.SchemaRevision, ScalarChanges: scalarChanges,
		RelationChanges: relationChanges, Diagnostics: diagnostics,
		Token: token, ExpiresAt: state.expiresAt.Format(time.RFC3339),
		Scope: params.Scope, Field: params.Field,
		CanApply:   len(patch) > 0 || len(attachmentPlans) > 0,
		Restorable: restorable,
	}, nil
}

func (service *Service) ApplyRestore(
	ctx context.Context,
	params ApplyParams,
) (RestoreResult, error) {
	if params.TableID == "" || params.ItemID == "" || params.Token == "" ||
		len(params.TableID) > maxHistoryIdentifierSize ||
		len(params.ItemID) > maxHistoryIdentifierSize ||
		len(params.Token) > 2048 {
		return RestoreResult{}, historyError("restore.request_invalid", "restore request is invalid", false)
	}
	state, err := service.claimToken(params)
	if err != nil {
		return RestoreResult{}, err
	}
	if len(state.patch) == 0 && len(state.attachments) == 0 {
		return RestoreResult{}, historyError("restore_no_fields", "restore preview has no writable changes", false)
	}
	definition, err := service.describe(ctx, state.tableID)
	if err != nil {
		return RestoreResult{}, err
	}
	if definition.SchemaRevision != state.schemaRevision {
		return RestoreResult{}, historyError("schema_drift", "schema changed after restore preview", false)
	}
	current, exists, err := service.readCurrent(definition, state.recordID)
	if err != nil {
		return RestoreResult{}, err
	}
	if exists != !state.insert {
		return RestoreResult{}, historyError("restore_conflict", "record existence changed after preview", false)
	}
	digest, err := digestRow(current)
	if err != nil || digest != state.currentDigest {
		return RestoreResult{}, historyError("restore_conflict", "record changed after restore preview", false)
	}
	tokenHash := sha256.Sum256([]byte(params.Token))
	requestIdentity := hex.EncodeToString(tokenHash[:12])
	attachmentChanges := make(
		[]mutation.AttachmentChange, 0, len(state.attachments),
	)
	cleanups := make([]func(), 0, len(state.attachments))
	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()
	for _, plan := range state.attachments {
		change, cleanup, stageErr := service.files.StageRestore(
			ctx, service.app, plan, requestIdentity,
		)
		if stageErr != nil {
			return RestoreResult{}, attachmentHistoryError(stageErr)
		}
		cleanups = append(cleanups, cleanup)
		attachmentChanges = append(attachmentChanges, change)
	}
	operations := make([]mutation.Operation, 0, 2+len(attachmentChanges))
	var expectedDigest *string
	if state.insert {
		operations = append(operations, mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: stringPointer(state.recordID),
			Values: cloneMap(state.patch),
		})
		operations = appendAttachmentOperations(operations, state.recordID, attachmentChanges)
		expectedDigest = nil
	} else if state.scope == "archived" {
		values := cloneMap(state.patch)
		delete(values, state.archiveField)
		if len(values) > 0 {
			operations = append(operations, mutation.Operation{
				Kind: mutation.OperationUpdate, RecordID: stringPointer(state.recordID),
				Values: values,
			})
		}
		operations = appendAttachmentOperations(operations, state.recordID, attachmentChanges)
		operations = append(operations, mutation.Operation{
			Kind: mutation.OperationRestore, RecordID: stringPointer(state.recordID),
		})
		expectedDigest = &state.currentDigest
	} else {
		if len(state.patch) > 0 {
			operations = append(operations, mutation.Operation{
				Kind: mutation.OperationUpdate, RecordID: stringPointer(state.recordID),
				Values: cloneMap(state.patch),
			})
		}
		operations = appendAttachmentOperations(operations, state.recordID, attachmentChanges)
		expectedDigest = &state.currentDigest
	}
	receipt, err := service.kernel.Apply(ctx, mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       "restore_" + requestIdentity,
		IdempotencyKey:  "restore_" + requestIdentity,
		TableID:         state.tableID,
		SchemaRevision:  state.schemaRevision,
		Operations:      operations,
		Actor:           mutation.Actor{Type: "restore", ID: "local-history"},
		ExpectedDigest:  expectedDigest,
	})
	if err != nil {
		var productErr *mutation.ProductError
		if errors.As(err, &productErr) {
			if productErr.Code == "mutation.digest_conflict" ||
				productErr.Code == "mutation.revision_conflict" ||
				productErr.Code == "mutation.schema_revision_conflict" ||
				productErr.Code == "mutation.record.already_exists" ||
				productErr.Code == "mutation.record.not_found" {
				return RestoreResult{}, historyError("restore_conflict", "record changed after restore preview", false)
			}
			if productErr.Code == "mutation.storage.failed" ||
				productErr.Code == "mutation.internal.failed" {
				return RestoreResult{}, historyError(
					"history.storage_failed",
					"restore could not be committed",
					true,
				)
			}
			return RestoreResult{}, &Error{
				Code:      "restore_validation_failed",
				Message:   "restore no longer satisfies current table constraints",
				Details:   map[string]any{"mutationCode": productErr.Code},
				Retryable: false,
			}
		}
		return RestoreResult{}, err
	}
	newRevisionID, err := service.restoreRevisionID(receipt, state.recordID)
	if err != nil {
		service.restoreClaim(params.Token, state)
		return RestoreResult{}, err
	}
	item, _, err := service.readCurrentByTableID(ctx, state.tableID, state.recordID)
	if err != nil {
		// The mutation is already committed. Reinsert the claimed token so a
		// retry can replay the stable idempotency key and complete the read.
		service.restoreClaim(params.Token, state)
		return RestoreResult{}, err
	}
	return RestoreResult{
		Collection: state.tableID, ItemID: state.recordID,
		RestoredToRevision: state.targetRevision,
		NewRevisionID:      newRevisionID, Item: item, Receipt: receipt,
	}, nil
}

func (service *Service) restoreRevisionID(
	receipt mutation.Receipt,
	recordID string,
) (*string, error) {
	if receipt.ChangeSetID == nil {
		return nil, historyError("revision_not_created", "restore did not create a revision", true)
	}
	records, err := service.app.FindRecordsByFilter(
		"vibetable_audit_events",
		"change_set_id={:changeSet} && record_id={:record}",
		"-sequence",
		1,
		0,
		dbx.Params{"changeSet": *receipt.ChangeSetID, "record": recordID},
	)
	if err != nil || len(records) != 1 {
		return nil, historyError("revision_not_created", "restore did not create a revision", true)
	}
	revisionID := records[0].Id
	return &revisionID, nil
}

func appendAttachmentOperations(
	operations []mutation.Operation,
	recordID string,
	changes []mutation.AttachmentChange,
) []mutation.Operation {
	for _, change := range changes {
		operations = append(operations, mutation.Operation{
			Kind:     mutation.OperationSetAttachments,
			RecordID: stringPointer(recordID),
			FieldID:  change.FieldID,
			UploadHandles: append(
				[]string(nil), change.UploadHandles...,
			),
			RemoveStoredNames: append(
				[]string(nil), change.RemoveStoredNames...,
			),
		})
	}
	return operations
}

func stringValues(value any) []string {
	if value == nil {
		return []string{}
	}
	if text, ok := value.(string); ok {
		if text == "" {
			return []string{}
		}
		return []string{text}
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Slice &&
		reflected.Kind() != reflect.Array {
		return []string{}
	}
	result := make([]string, 0, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		text, ok := reflected.Index(index).Interface().(string)
		if ok && text != "" {
			result = append(result, text)
		}
	}
	return result
}

func attachmentHistoryError(err error) error {
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) {
		return err
	}
	switch productErr.Code {
	case "attachment.history_missing":
		return historyError(
			"restore_attachment_missing",
			"a historical attachment is unavailable",
			false,
		)
	case "attachment.history_corrupt":
		return historyError(
			"restore_attachment_corrupt",
			"a historical attachment failed integrity validation",
			false,
		)
	case "attachment.history_changed":
		return historyError(
			"restore_conflict",
			"attachment state changed after restore preview",
			false,
		)
	case "attachment.history_policy_mismatch",
		"attachment.history_limit",
		"attachment.history_invalid":
		return &Error{
			Code:      "restore_validation_failed",
			Message:   "historical attachments no longer satisfy the current field policy",
			Details:   map[string]any{"mutationCode": productErr.Code},
			Retryable: false,
		}
	default:
		if productErr.Retryable ||
			strings.HasSuffix(productErr.Code, "_failed") {
			return historyError(
				"history.storage_failed",
				"attachment history could not be accessed",
				true,
			)
		}
		return &Error{
			Code:      "restore_validation_failed",
			Message:   "historical attachments cannot be restored",
			Details:   map[string]any{"mutationCode": productErr.Code},
			Retryable: false,
		}
	}
}

func (service *Service) claimToken(params ApplyParams) (restoreState, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if !service.validToken(params.Token) {
		return restoreState{}, historyError("restore_token_unknown", "restore token is unknown", false)
	}
	state, ok := service.tokens[params.Token]
	if !ok {
		return restoreState{}, historyError("restore_token_unknown", "restore token is unknown", false)
	}
	delete(service.tokens, params.Token)
	service.tokenBytes -= state.size
	if !service.now().Before(state.expiresAt) {
		return restoreState{}, historyError("restore_token_expired", "restore token has expired", false)
	}
	if state.tableID != params.TableID || state.recordID != params.ItemID {
		return restoreState{}, historyError("restore_scope_mismatch", "restore token scope does not match", false)
	}
	return state, nil
}

func (service *Service) restoreClaim(token string, state restoreState) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.now().Before(state.expiresAt) &&
		len(service.tokens) < maxRestoreToken &&
		service.tokenBytes+state.size <= maxRestoreTokenBytes {
		service.tokens[token] = state
		service.tokenBytes += state.size
	}
}

func (service *Service) issueToken(state restoreState) (string, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.now()
	for token, stored := range service.tokens {
		if !now.Before(stored.expiresAt) {
			delete(service.tokens, token)
			service.tokenBytes -= stored.size
		}
	}
	if len(service.tokens) >= maxRestoreToken ||
		service.tokenBytes+state.size > maxRestoreTokenBytes {
		return "", historyError(
			"restore.capacity_exhausted",
			"restore preview capacity is temporarily exhausted",
			true,
		)
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(nonce)
	mac := hmac.New(sha256.New, service.secret)
	_, _ = mac.Write([]byte(payload))
	token := "rst1." + payload + "." + hex.EncodeToString(mac.Sum(nil))
	service.tokens[token] = state
	service.tokenBytes += state.size
	return token, nil
}

func (service *Service) validToken(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "rst1" {
		return false
	}
	provided, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, service.secret)
	_, _ = mac.Write([]byte(parts[1]))
	return hmac.Equal(provided, mac.Sum(nil))
}

func (service *Service) readCurrentByTableID(
	ctx context.Context,
	tableID, recordID string,
) (map[string]any, bool, error) {
	definition, err := service.describe(ctx, tableID)
	if err != nil {
		return nil, false, err
	}
	return service.readCurrent(definition, recordID)
}

func (service *Service) readCurrent(
	definition schema.TableDefinition,
	recordID string,
) (map[string]any, bool, error) {
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": definition.TableID},
	)
	if err != nil {
		return nil, false, historyError("history.storage_failed", "table metadata could not be read", true)
	}
	collection, err := service.app.FindCollectionByNameOrId(meta.GetString("collection_id"))
	if err != nil {
		return nil, false, historyError("history.storage_failed", "table storage could not be read", true)
	}
	record, err := service.app.FindRecordById(collection, recordID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{}, false, nil
		}
		return nil, false, historyError("history.storage_failed", "record could not be read", true)
	}
	return productRow(definition, record), true, nil
}

func (service *Service) relationTargetsAvailable(
	ctx context.Context,
	field schema.FieldDefinition,
	value any,
) (bool, error) {
	if field.Relation == nil || field.Relation.TargetTableID == "" {
		return false, nil
	}
	targetIDs, valid := relationTargetIDs(value)
	if !valid {
		return false, nil
	}
	if field.Relation.Cardinality == "one" && len(targetIDs) > 1 {
		return false, nil
	}
	for _, targetID := range targetIDs {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		_, exists, err := service.readCurrentByTableID(
			ctx, field.Relation.TargetTableID, targetID,
		)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, nil
		}
	}
	return true, nil
}

func relationTargetIDs(value any) ([]string, bool) {
	switch typed := value.(type) {
	case nil:
		return []string{}, true
	case string:
		if typed == "" {
			return []string{}, true
		}
		return []string{typed}, true
	case []string:
		return append([]string(nil), typed...), true
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			id, ok := item.(string)
			if !ok || id == "" {
				return nil, false
			}
			result = append(result, id)
		}
		return result, true
	default:
		return nil, false
	}
}

func groupEvents(
	events []*core.Record,
	definition schema.TableDefinition,
	fieldName *string,
	params ReadParams,
	archivedIDs map[string]struct{},
) ([]ChangeSet, map[string]string, error) {
	type group struct {
		events []*core.Record
	}
	order := []string{}
	groups := map[string]*group{}
	archivedDefaults := map[string]string{}
	for _, event := range events {
		if archivedIDs != nil {
			if _, archived := archivedIDs[event.GetString("record_id")]; !archived {
				continue
			}
		}
		if err := validateEvent(event); err != nil {
			return nil, nil, err
		}
		changeSetID := event.GetString("change_set_id")
		if groups[changeSetID] == nil {
			groups[changeSetID] = &group{}
			order = append(order, changeSetID)
		}
		groups[changeSetID].events = append(groups[changeSetID].events, event)
		if event.GetString("operation") == string(mutation.OperationArchive) {
			if _, exists := archivedDefaults[event.GetString("record_id")]; !exists {
				archivedDefaults[event.GetString("record_id")] = event.Id
			}
		}
	}
	result := make([]ChangeSet, 0, len(order))
	for _, changeSetID := range order {
		events := groups[changeSetID].events
		sort.Slice(events, func(left, right int) bool {
			return events[left].GetFloat("sequence") < events[right].GetFloat("sequence")
		})
		changeSet, include, err := buildChangeSet(events, definition, fieldName, params)
		if err != nil {
			return nil, nil, err
		}
		if include {
			result = append(result, changeSet)
		}
	}
	return result, archivedDefaults, nil
}

func (service *Service) archivedRecordIDs(
	definition schema.TableDefinition,
) (map[string]struct{}, error) {
	archiveName, err := archivedFieldName(definition)
	if err != nil {
		return nil, err
	}
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": definition.TableID},
	)
	if err != nil {
		return nil, historyError("history.storage_failed", "table metadata could not be read", true)
	}
	collection, err := service.app.FindCollectionByNameOrId(meta.GetString("collection_id"))
	if err != nil {
		return nil, historyError("history.storage_failed", "table storage could not be read", true)
	}
	records, err := service.app.FindRecordsByFilter(
		collection, "", "", maxArchivedScanRecords+1, 0,
	)
	if err != nil {
		return nil, historyError("history.storage_failed", "archived records could not be read", true)
	}
	if len(records) > maxArchivedScanRecords {
		return nil, historyError(
			"history.resource_limit",
			"archived history exceeds the safe scan limit",
			false,
		)
	}
	result := map[string]struct{}{}
	for _, record := range records {
		if isArchived(definition, archiveName, record.GetRaw(archiveName)) {
			result[record.Id] = struct{}{}
		}
	}
	return result, nil
}

func buildChangeSet(
	events []*core.Record,
	definition schema.TableDefinition,
	fieldName *string,
	params ReadParams,
) (ChangeSet, bool, error) {
	first := events[0]
	actorID := first.GetString("actor_id")
	displayName := first.GetString("actor_display_name")
	actor := &Actor{}
	if first.GetString("actor_type") != "system" {
		actor.UserID = &actorID
	}
	if displayName != "" {
		actor.DisplayName = &displayName
	}
	activityID := first.GetString("change_set_id")
	changeSet := ChangeSet{
		RootRevisionID: first.Id, ChangeSetID: activityID, ActivityID: &activityID,
		Timestamp: first.GetDateTime("occurred_at").Time().UTC().Format(time.RFC3339),
		Actor:     actor, ScalarChanges: []ScalarFieldChange{},
		RelationChange: []RelationFieldChange{}, RevisionIDs: []string{},
		RecordChanges: []RecordChange{},
	}
	actions := map[string]struct{}{}
	itemIDs := map[string]struct{}{}
	for _, event := range events {
		if event.GetString("request_id") != first.GetString("request_id") ||
			event.GetString("actor_type") != first.GetString("actor_type") ||
			event.GetString("actor_id") != first.GetString("actor_id") {
			return ChangeSet{}, false, historyError(
				"history.storage_corrupt",
				"audit change set metadata is inconsistent",
				false,
			)
		}
		before, err := decodeImage(event.GetRaw("before_json"))
		if err != nil {
			return ChangeSet{}, false, historyError("history.storage_corrupt", "audit before image is invalid", false)
		}
		after, err := decodeImage(event.GetRaw("after_json"))
		if err != nil {
			return ChangeSet{}, false, historyError("history.storage_corrupt", "audit after image is invalid", false)
		}
		action := historyAction(
			event.GetString("operation"),
			event.GetString("actor_type"),
		)
		scalars, relations := diffImages(before, after, definition, fieldName)
		if len(scalars) == 0 && len(relations) == 0 {
			continue
		}
		recordID := event.GetString("record_id")
		record := RecordChange{
			RevisionID: event.Id, ItemID: recordID, Action: action,
			ScalarChanges: scalars, RelationChange: relations,
			RecordLabel: recordLabel(after, before),
		}
		changeSet.RecordChanges = append(changeSet.RecordChanges, record)
		changeSet.RevisionIDs = append(changeSet.RevisionIDs, event.Id)
		changeSet.ScalarChanges = append(changeSet.ScalarChanges, scalars...)
		changeSet.RelationChange = append(changeSet.RelationChange, relations...)
		actions[action] = struct{}{}
		itemIDs[recordID] = struct{}{}
	}
	if len(changeSet.RecordChanges) == 0 {
		return ChangeSet{}, false, nil
	}
	if len(actions) == 1 {
		for action := range actions {
			changeSet.Action = action
		}
	} else {
		changeSet.Action = "batch"
	}
	changeSet.AffectedRows = len(itemIDs)
	if len(itemIDs) == 1 {
		id := changeSet.RecordChanges[0].ItemID
		changeSet.ItemID = &id
		changeSet.RecordLabel = changeSet.RecordChanges[0].RecordLabel
	}
	if !matchesFilters(changeSet, params) {
		return ChangeSet{}, false, nil
	}
	return changeSet, true, nil
}

func diffImages(
	before, after map[string]any,
	definition schema.TableDefinition,
	fieldName *string,
) ([]ScalarFieldChange, []RelationFieldChange) {
	scalars := []ScalarFieldChange{}
	relations := []RelationFieldChange{}
	for _, field := range definition.Fields {
		if fieldName != nil && field.PhysicalName != *fieldName {
			continue
		}
		if isSensitiveField(field) {
			continue
		}
		beforeValue, afterValue := before[field.PhysicalName], after[field.PhysicalName]
		if reflect.DeepEqual(beforeValue, afterValue) {
			continue
		}
		if field.Kind == schema.FieldKindRelation {
			relations = append(relations, relationChange(field, beforeValue, afterValue))
		} else {
			scalars = append(scalars, ScalarFieldChange{
				Field: field.PhysicalName, Before: beforeValue, After: afterValue,
			})
		}
	}
	return scalars, relations
}

func isSensitiveField(field schema.FieldDefinition) bool {
	return field.DataType == schema.DataTypeSecret ||
		field.DataType == schema.DataTypeHash
}

func relationChange(
	field schema.FieldDefinition,
	before, after any,
) RelationFieldChange {
	beforeID, afterID := nullableString(before), nullableString(after)
	kind := "m2o"
	var beforeDisplay, afterDisplay *string
	if field.Relation != nil && field.Relation.Cardinality != "one" {
		kind = "m2m"
		beforeID, afterID = nil, nil
		beforeDisplay = relationDisplayValue(before)
		afterDisplay = relationDisplayValue(after)
	}
	var target *string
	if field.Relation != nil {
		target = &field.Relation.TargetTableID
	}
	return RelationFieldChange{
		Field: field.PhysicalName, Kind: kind,
		RelatedCollection: target, RelatedItemID: afterID,
		BeforeItemID: beforeID, AfterItemID: afterID,
		BeforeDisplayValue: beforeDisplay, AfterDisplayValue: afterDisplay,
		TargetAvailable: true,
	}
}

func relationDisplayValue(value any) *string {
	ids, valid := relationTargetIDs(value)
	if !valid || len(ids) == 0 {
		return nil
	}
	display := strings.Join(ids, ", ")
	return &display
}

func matchesFilters(changeSet ChangeSet, params ReadParams) bool {
	if len(params.Actions) > 0 {
		allowed := map[string]struct{}{}
		for _, action := range params.Actions {
			allowed[action] = struct{}{}
		}
		found := false
		for _, record := range changeSet.RecordChanges {
			if _, ok := allowed[record.Action]; ok {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	timestamp, timestampErr := time.Parse(time.RFC3339, changeSet.Timestamp)
	if timestampErr != nil {
		return false
	}
	if params.DateFrom != nil {
		start, _ := time.Parse(time.RFC3339, *params.DateFrom)
		if timestamp.Before(start) {
			return false
		}
	}
	if params.DateTo != nil {
		end, _ := time.Parse(time.RFC3339, *params.DateTo)
		if timestamp.After(end) {
			return false
		}
	}
	if strings.TrimSpace(params.Search) == "" {
		return true
	}
	raw, _ := json.Marshal(changeSet)
	return strings.Contains(strings.ToLower(string(raw)), strings.ToLower(strings.TrimSpace(params.Search)))
}

func validateReadParams(params ReadParams) error {
	if params.TableID == "" || len(params.TableID) > maxHistoryIdentifierSize {
		return historyError("history.request_invalid", "tableId is required", false)
	}
	if params.Scope == "" {
		return historyError("history.request_invalid", "scope is required", false)
	}
	switch params.Scope {
	case "table", "row", "cell", "archived":
	default:
		return historyError("history.request_invalid", "history scope is invalid", false)
	}
	if (params.Scope == "row" || params.Scope == "cell") && params.ItemID == nil {
		return historyError("history.request_invalid", "row history requires an itemId", false)
	}
	if params.Scope == "cell" && params.Field == nil {
		return historyError("history.request_invalid", "cell history requires a field", false)
	}
	if params.ItemID != nil && (len(*params.ItemID) == 0 || len(*params.ItemID) > maxHistoryIdentifierSize) {
		return historyError("history.request_invalid", "itemId is invalid", false)
	}
	if params.RecordID != nil &&
		(len(*params.RecordID) == 0 || len(*params.RecordID) > maxHistoryIdentifierSize) {
		return historyError("history.request_invalid", "recordId is invalid", false)
	}
	if params.ItemID != nil && params.RecordID != nil && *params.ItemID != *params.RecordID {
		return historyError("history.request_invalid", "itemId and recordId conflict", false)
	}
	if params.Field != nil && (len(*params.Field) == 0 || len(*params.Field) > maxHistoryIdentifierSize) {
		return historyError("history.request_invalid", "field is invalid", false)
	}
	if params.ActorID != nil && len(*params.ActorID) > maxHistoryIdentifierSize {
		return historyError("history.request_invalid", "actorId is invalid", false)
	}
	if len(params.Search) > maxHistorySearchLength || len(params.Actions) > maxHistoryActions {
		return historyError("history.request_invalid", "history filters are too large", false)
	}
	for _, action := range params.Actions {
		switch action {
		case "create", "update", "delete", "restore", "batch":
		default:
			return historyError("history.request_invalid", "history action is invalid", false)
		}
	}
	if params.Limit < 1 || params.Limit > maxHistoryLimit || params.Offset < 0 {
		return historyError("history.request_invalid", "history paging is invalid", false)
	}
	var dateFrom, dateTo *time.Time
	for index, value := range []*string{params.DateFrom, params.DateTo} {
		if value != nil {
			parsed, err := time.Parse(time.RFC3339, *value)
			if err != nil {
				return historyError("history.request_invalid", "history timestamps must be RFC3339 UTC", false)
			}
			_, offset := parsed.Zone()
			if offset != 0 {
				return historyError("history.request_invalid", "history timestamps must be RFC3339 UTC", false)
			}
			if index == 0 {
				dateFrom = &parsed
			} else {
				dateTo = &parsed
			}
		}
	}
	if dateFrom != nil && dateTo != nil && dateTo.Before(*dateFrom) {
		return historyError("history.request_invalid", "history date range is invalid", false)
	}
	return nil
}

func resolveFieldName(
	definition schema.TableDefinition,
	requested *string,
) (*string, error) {
	if requested == nil {
		return nil, nil
	}
	for _, field := range definition.Fields {
		if field.FieldID == *requested || field.PhysicalName == *requested {
			if isSensitiveField(field) {
				return nil, historyError(
					"history.field_unreadable",
					"history field is not readable",
					false,
				)
			}
			name := field.PhysicalName
			return &name, nil
		}
	}
	return nil, historyError("history.field_not_found", "history field was not found", false)
}

func validateEvent(record *core.Record) error {
	requiredText := []string{
		"change_set_id", "table_id", "record_id", "operation",
		"request_id", "actor_type", "actor_id",
	}
	for _, field := range requiredText {
		if record.GetString(field) == "" {
			return historyError("history.storage_corrupt", "audit event is incomplete", false)
		}
	}
	sequence := record.GetFloat("sequence")
	dataRevision := record.GetFloat("data_revision")
	schemaRevision := record.GetFloat("schema_revision")
	if sequence < 1 || math.Trunc(sequence) != sequence ||
		dataRevision < 1 || math.Trunc(dataRevision) != dataRevision ||
		schemaRevision < 1 || math.Trunc(schemaRevision) != schemaRevision ||
		record.GetDateTime("occurred_at").IsZero() {
		return historyError("history.storage_corrupt", "audit event counters are invalid", false)
	}
	switch mutation.OperationKind(record.GetString("operation")) {
	case mutation.OperationInsert, mutation.OperationUpdate,
		mutation.OperationArchive, mutation.OperationRestore,
		mutation.OperationDelete, mutation.OperationSetAttachments:
	default:
		return historyError("history.storage_corrupt", "audit event operation is invalid", false)
	}
	return nil
}

func decodeImage(value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil || string(raw) == "null" {
		return map[string]any{}, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func productRow(
	definition schema.TableDefinition,
	record *core.Record,
) map[string]any {
	result := make(map[string]any, len(definition.Fields)+1)
	result["id"] = record.Id
	for _, field := range definition.Fields {
		result[field.PhysicalName] = record.GetRaw(field.PhysicalName)
	}
	return result
}

func digestRow(row map[string]any) (string, error) {
	raw, err := json.Marshal(row)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func hashJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func historyAction(operation string, actorType string) string {
	if actorType == "restore" {
		return "restore"
	}
	switch mutation.OperationKind(operation) {
	case mutation.OperationInsert:
		return "create"
	case mutation.OperationArchive, mutation.OperationDelete:
		return "delete"
	case mutation.OperationRestore:
		return "restore"
	default:
		return "update"
	}
}

func (service *Service) describe(
	ctx context.Context,
	tableID string,
) (schema.TableDefinition, error) {
	definition, err := service.schemas.Describe(ctx, service.app, tableID)
	if err == nil {
		return definition, nil
	}
	var productErr *schema.ProductError
	if errors.As(err, &productErr) {
		if strings.HasSuffix(productErr.Code, ".not_found") {
			return schema.TableDefinition{}, historyError(
				"history.table_not_found",
				"history table was not found",
				false,
			)
		}
		return schema.TableDefinition{}, historyError(
			"history.storage_failed",
			"history schema could not be read",
			true,
		)
	}
	var mutationErr *mutation.ProductError
	if errors.As(err, &mutationErr) {
		if strings.HasSuffix(mutationErr.Code, ".not_found") {
			return schema.TableDefinition{}, historyError(
				"history.table_not_found",
				"history table was not found",
				false,
			)
		}
		return schema.TableDefinition{}, historyError(
			"history.storage_failed",
			"history schema could not be read",
			true,
		)
	}
	return schema.TableDefinition{}, err
}

func archivedFieldName(definition schema.TableDefinition) (string, error) {
	if definition.ArchivePolicy.Mode == schema.ArchiveModeNone ||
		definition.ArchivePolicy.FieldID == nil {
		return "", historyError("archive_not_supported", "table has no archive policy", false)
	}
	for _, field := range definition.Fields {
		if field.FieldID == *definition.ArchivePolicy.FieldID {
			return field.PhysicalName, nil
		}
	}
	return "", historyError("archive_not_supported", "archive field is unavailable", false)
}

func isArchived(
	definition schema.TableDefinition,
	archiveField string,
	value any,
) bool {
	switch definition.ArchivePolicy.Mode {
	case schema.ArchiveModeStatus:
		return reflect.DeepEqual(value, definition.ArchivePolicy.ArchivedValue) ||
			fmt.Sprint(value) == fmt.Sprint(definition.ArchivePolicy.ArchivedValue)
	case schema.ArchiveModeDeletedAt:
		if value == nil {
			return false
		}
		return fmt.Sprint(value) != ""
	default:
		return false
	}
}

func cloneJSONMap(value map[string]any) (map[string]any, int, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, 0, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, 0, err
	}
	return result, len(raw), nil
}

func wireEqual(left, right any) bool {
	if reflect.DeepEqual(left, right) {
		return true
	}
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	var leftValue, rightValue any
	if json.Unmarshal(leftRaw, &leftValue) != nil ||
		json.Unmarshal(rightRaw, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func diagnostic(field, classification, code, message string) Diagnostic {
	return Diagnostic{
		Field: field, Classification: classification,
		Severity: "warning", Code: code, Message: message,
	}
}

func historyError(code, message string, retryable bool) *Error {
	return &Error{
		Code: code, Message: message,
		Details: map[string]any{}, Retryable: retryable,
	}
}

func recordLabel(images ...map[string]any) *string {
	for _, image := range images {
		for _, candidate := range []string{"title", "name", "display_name", "id"} {
			if value, ok := image[candidate]; ok && value != nil {
				text := fmt.Sprint(value)
				if text != "" {
					if len(text) > 512 {
						text = text[:512]
					}
					return &text
				}
			}
		}
	}
	return nil
}

func nullableString(value any) *string {
	if value == nil {
		return nil
	}
	text := fmt.Sprint(value)
	if text == "" {
		return nil
	}
	return &text
}

func stringPointer(value string) *string {
	return &value
}

func cloneMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
