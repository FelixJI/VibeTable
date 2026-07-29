package mutation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/vibetable/vibetable/sidecar/internal/autodateobs"
	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const (
	maxRequestBytes = 1 << 20
	maxOperations   = 1000
	maxValues       = 512
)

var (
	rowRevisionPattern = regexp.MustCompile(`^row_[0-9]{4,}$`)
	rowDigestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type SchemaSource interface {
	Describe(ctx context.Context, app core.App, tableID string) (schema.TableDefinition, error)
}

type Kernel struct {
	app         core.App
	schemas     SchemaSource
	now         func() time.Time
	newID       func(kind string) string
	fault       func(point string) error
	formulas    FormulaCalculator
	attachments AttachmentManager
	publisher   Publisher
	publishCtx  context.Context
	coordinator *mutationCoordinator
}

type mutationCoordinator struct {
	gate chan struct{}

	mu     sync.Mutex
	active map[string]string
}

var coordinatorRegistry sync.Map

type PreviewResult struct {
	Definition schema.TableDefinition
	Operations []NormalizedOperation
}

type NormalizedOperation struct {
	Kind             OperationKind
	RecordID         *string
	Values           map[string]any
	ExpectedRevision *string
	ExpectedDigest   *string
	Attachment       *AttachmentChange
}

type AttachmentChange struct {
	FieldID           string
	UploadHandles     []string
	RemoveStoredNames []string
}

type AttachmentFinalizer func(core.App, *core.Record) error

type AttachmentManager interface {
	Prepare(
		context.Context,
		core.App,
		schema.TableDefinition,
		*core.Record,
		AttachmentChange,
	) (AttachmentFinalizer, error)
	CleanupRecord(
		context.Context,
		core.App,
		schema.TableDefinition,
		*core.Record,
	) error
}

type FormulaCalculator interface {
	Calculate(
		ctx context.Context,
		app core.App,
		definition schema.TableDefinition,
		record *core.Record,
	) (map[string]any, error)
}

type Publisher interface {
	Publish(ctx context.Context, event DataChangedEvent) error
}

type Option func(*Kernel)

func WithClock(clock func() time.Time) Option {
	return func(kernel *Kernel) { kernel.now = clock }
}

func WithIDGenerator(generator func(kind string) string) Option {
	return func(kernel *Kernel) { kernel.newID = generator }
}

func WithFaultInjector(injector func(point string) error) Option {
	return func(kernel *Kernel) { kernel.fault = injector }
}

func WithFormulaCalculator(calculator FormulaCalculator) Option {
	return func(kernel *Kernel) { kernel.formulas = calculator }
}

func WithAttachmentManager(manager AttachmentManager) Option {
	return func(kernel *Kernel) { kernel.attachments = manager }
}

func WithPublisher(publisher Publisher) Option {
	return func(kernel *Kernel) { kernel.publisher = publisher }
}

// WithPublishContext binds committed outbox delivery to the lifetime of the
// application component that owns the publisher. Request cancellation remains
// independent from this context.
func WithPublishContext(ctx context.Context) Option {
	return func(kernel *Kernel) {
		if ctx != nil {
			kernel.publishCtx = ctx
		}
	}
}

func New(app core.App, schemas SchemaSource, options ...Option) *Kernel {
	kernel := &Kernel{
		app: app, schemas: schemas,
		coordinator: coordinatorFor(app),
		now:         func() time.Time { return time.Now().UTC() },
		newID:       newMutationID,
		fault:       func(string) error { return nil },
		publishCtx:  context.Background(),
	}
	for _, option := range options {
		option(kernel)
	}
	return kernel
}

func newMutationID(kind string) string {
	if kind == "record" {
		return security.RandomStringWithAlphabet(
			core.DefaultIdLength,
			core.DefaultIdAlphabet,
		)
	}
	return kind + "_" + security.RandomString(12)
}

func coordinatorFor(app core.App) *mutationCoordinator {
	created := &mutationCoordinator{
		gate: make(chan struct{}, 1), active: map[string]string{},
	}
	if app == nil {
		return created
	}
	loaded, _ := coordinatorRegistry.LoadOrStore(app, created)
	return loaded.(*mutationCoordinator)
}

func (coordinator *mutationCoordinator) begin(
	key, requestHash string,
) (bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	activeHash, exists := coordinator.active[key]
	if !exists {
		coordinator.active[key] = requestHash
		return true, nil
	}
	if activeHash != requestHash {
		return false, mutationError(
			"mutation.idempotency_conflict", stringPointer("idempotencyKey"),
			"idempotency key is in use by a different request", nil, false,
		)
	}
	return false, nil
}

func (coordinator *mutationCoordinator) end(key, requestHash string) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.active[key] == requestHash {
		delete(coordinator.active, key)
	}
}

func (coordinator *mutationCoordinator) acquire(ctx context.Context) error {
	select {
	case coordinator.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (coordinator *mutationCoordinator) release() {
	<-coordinator.gate
}

func (kernel *Kernel) Preview(ctx context.Context, request Request) (PreviewResult, error) {
	return kernel.preview(ctx, kernel.app, request)
}

func (kernel *Kernel) preview(ctx context.Context, app core.App, request Request) (PreviewResult, error) {
	if err := validateRequestShape(request); err != nil {
		return PreviewResult{}, err
	}
	if kernel.schemas == nil {
		return PreviewResult{}, mutationError("mutation.schema.unavailable", nil, "schema source is unavailable", nil, false)
	}
	definition, err := kernel.schemas.Describe(ctx, app, request.TableID)
	if err != nil {
		return PreviewResult{}, err
	}
	if definition.TableID != request.TableID {
		return PreviewResult{}, mutationError("mutation.table.not_found", stringPointer("tableId"), "table was not found", nil, false)
	}
	if definition.Kind != schema.TableKindBase {
		return PreviewResult{}, mutationError(
			"mutation.table.read_only", stringPointer("tableId"),
			"only base tables accept mutations", nil, false,
		)
	}
	if definition.SchemaRevision != request.SchemaRevision {
		return PreviewResult{}, mutationError(
			"mutation.schema_revision_conflict", stringPointer("schemaRevision"),
			"schema revision does not match", map[string]any{
				"expected": request.SchemaRevision, "actual": definition.SchemaRevision,
			}, false,
		)
	}
	if !request.InternalBypassMigrationFence {
		if err := checkFieldMigrationFence(ctx, app, request.TableID); err != nil {
			return PreviewResult{}, err
		}
	}
	fields := make(map[string]schema.FieldDefinition, len(definition.Fields)*2)
	for _, field := range definition.Fields {
		fields[field.FieldID] = field
		fields[field.PhysicalName] = field
	}
	v2Fields, err := loadV2FieldDefinitions(ctx, app, request.TableID)
	if err != nil {
		return PreviewResult{}, err
	}
	v2ByAlias := make(map[string]v2.FieldDefinition, len(v2Fields)*2)
	for _, field := range v2Fields {
		v2ByAlias[field.Identity.FieldID] = field
		v2ByAlias[field.Identity.PhysicalName] = field
	}
	pendingRecordIDs := make(map[string]struct{}, len(request.Operations))
	for _, operation := range request.Operations {
		if operation.Kind == OperationInsert &&
			operation.RecordID != nil && *operation.RecordID != "" {
			pendingRecordIDs[*operation.RecordID] = struct{}{}
		}
	}
	normalized := make([]NormalizedOperation, 0, len(request.Operations))
	for operationIndex, operation := range request.Operations {
		path := fmt.Sprintf("operations[%d]", operationIndex)
		if operation.Kind == OperationSetAttachments {
			if operation.RecordID == nil || *operation.RecordID == "" {
				return PreviewResult{}, mutationError(
					"mutation.record.missing_id", stringPointer(path+".recordId"),
					"record id is required", nil, false,
				)
			}
			if kernel.attachments == nil {
				return PreviewResult{}, mutationError(
					"mutation.attachment.unavailable", stringPointer(path),
					"managed attachment service is unavailable", nil, false,
				)
			}
			field, ok := fields[operation.FieldID]
			if !ok || field.Kind != schema.FieldKindAttachment ||
				field.DataType != schema.DataTypeFile ||
				field.AttachmentPolicy == nil {
				return PreviewResult{}, mutationError(
					"mutation.attachment.invalid_field", stringPointer(path+".fieldId"),
					"attachment field was not found", nil, false,
				)
			}
			if len(operation.UploadHandles) == 0 &&
				len(operation.RemoveStoredNames) == 0 {
				return PreviewResult{}, mutationError(
					"mutation.attachment.empty", stringPointer(path),
					"attachment operation must upload or remove at least one file", nil, false,
				)
			}
			normalized = append(normalized, NormalizedOperation{
				Kind: operation.Kind, RecordID: operation.RecordID,
				ExpectedRevision: operation.ExpectedRevision,
				ExpectedDigest:   operation.ExpectedDigest,
				Attachment: &AttachmentChange{
					FieldID:           field.FieldID,
					UploadHandles:     append([]string(nil), operation.UploadHandles...),
					RemoveStoredNames: append([]string(nil), operation.RemoveStoredNames...),
				},
			})
			continue
		}
		if operation.Kind != OperationInsert && (operation.RecordID == nil || *operation.RecordID == "") {
			return PreviewResult{}, mutationError("mutation.record.missing_id", stringPointer(path+".recordId"), "record id is required", nil, false)
		}
		if operation.Kind == OperationArchive || operation.Kind == OperationRestore {
			if definition.ArchivePolicy.Mode == schema.ArchiveModeNone {
				return PreviewResult{}, mutationError("mutation.archive.unsupported", stringPointer(path), "table has no archive policy", nil, false)
			}
		}
		values := make(map[string]any, len(operation.Values)+len(operation.RawValues))
		suppliedV2 := make(
			map[string]struct{}, len(operation.Values)+len(operation.RawValues),
		)
		type suppliedValue struct {
			key   string
			value any
			raw   bool
		}
		supplied := make(
			[]suppliedValue, 0, len(operation.Values)+len(operation.RawValues),
		)
		for key, value := range operation.Values {
			supplied = append(supplied, suppliedValue{key: key, value: value})
		}
		for key, value := range operation.RawValues {
			supplied = append(
				supplied, suppliedValue{key: key, value: value, raw: true},
			)
		}
		sort.Slice(supplied, func(left, right int) bool {
			if supplied[left].raw != supplied[right].raw {
				return !supplied[left].raw
			}
			return supplied[left].key < supplied[right].key
		})
		seenFields := make(map[string]string, len(supplied))
		for _, entry := range supplied {
			valueContainer := "values"
			if entry.raw {
				valueContainer = "rawValues"
			}
			valuePath := path + "." + valueContainer + "." + entry.key
			field, ok := fields[entry.key]
			if !ok {
				return PreviewResult{}, mutationError(
					"mutation.field.unknown", stringPointer(valuePath),
					"mutation references an unknown field",
					map[string]any{"field": entry.key}, false,
				)
			}
			if previous, duplicate := seenFields[field.FieldID]; duplicate {
				return PreviewResult{}, mutationError(
					"mutation.field.duplicate", stringPointer(valuePath),
					"field was specified by more than one alias",
					map[string]any{"previousPath": previous}, false,
				)
			}
			seenFields[field.FieldID] = valuePath
		}
		for _, entry := range supplied {
			key, value := entry.key, entry.value
			valueContainer := "values"
			if entry.raw {
				valueContainer = "rawValues"
			}
			valuePath := path + "." + valueContainer + "." + key
			field := fields[key]
			v2Field, isV2 := v2ByAlias[key]
			if entry.raw && !isV2 {
				return PreviewResult{}, mutationError(
					"mutation.field.raw_unsupported", stringPointer(valuePath),
					"raw field input requires a Schema v2 field",
					map[string]any{"fieldId": field.FieldID}, false,
				)
			}
			if field.ReadOnly || field.Kind == schema.FieldKindFormula ||
				field.Kind == schema.FieldKindLookup || field.Kind == schema.FieldKindSystem {
				if field.Kind == schema.FieldKindSystem {
					autodateobs.Increment(autodateobs.ClientWriteRejected)
				}
				return PreviewResult{}, mutationError(
					"mutation.field.read_only", stringPointer(valuePath),
					"field is read-only", map[string]any{"fieldId": field.FieldID}, false,
				)
			}
			if field.Kind == schema.FieldKindAttachment {
				return PreviewResult{}, mutationError(
					"mutation.attachment.requires_operation", stringPointer(valuePath),
					"attachment fields require setAttachments", map[string]any{"fieldId": field.FieldID}, false,
				)
			}
			var v2Result *fieldvalue.Result
			if isV2 {
				mode := fieldvalue.Update
				if operation.Kind == OperationInsert {
					mode = fieldvalue.Insert
				}
				input := fieldvalue.Input{Supplied: true, Value: value}
				if entry.raw {
					rawInput, normalizeRawErr := fieldvalue.New().NormalizeRawInput(
						ctx, v2Field, value,
					)
					if normalizeRawErr != nil {
						return PreviewResult{}, mutationError(
							"mutation.field.invalid_value",
							stringPointer(valuePath),
							normalizeRawErr.Error(),
							map[string]any{"fieldId": field.FieldID},
							false,
						)
					}
					input = rawInput
				}
				result, normalizeErr := fieldvalue.New().NormalizeWrite(
					ctx, v2Field, mode, input,
				)
				if normalizeErr != nil {
					return PreviewResult{}, mutationError(
						"mutation.field.invalid_value",
						stringPointer(valuePath),
						normalizeErr.Error(),
						map[string]any{"fieldId": field.FieldID},
						false,
					)
				}
				v2Result = &result
				value = result.ProductValue
			}
			if isArchiveField(definition, field) {
				switch definition.ArchivePolicy.Mode {
				case schema.ArchiveModeDeletedAt:
					return PreviewResult{}, mutationError(
						"mutation.archive.requires_operation",
						stringPointer(valuePath),
						"deleted-at archive fields require archive or restore",
						nil, false,
					)
				case schema.ArchiveModeStatus:
					if schema.ProductValuesEqual(
						value,
						definition.ArchivePolicy.ArchivedValue,
					) {
						return PreviewResult{}, mutationError(
							"mutation.archive.requires_operation",
							stringPointer(valuePath),
							"the archived status requires an archive operation",
							nil, false,
						)
					}
				}
			}
			if _, duplicate := values[field.PhysicalName]; duplicate {
				return PreviewResult{}, mutationError(
					"mutation.field.duplicate", stringPointer(valuePath),
					"field was specified by more than one alias", nil, false,
				)
			}
			if v2Result != nil {
				if field.Kind == schema.FieldKindRelation {
					normalizedRelation, relationErr := validateRelationValue(
						ctx,
						app,
						definition,
						field,
						v2Result.ProductValue,
						pendingRecordIDs,
					)
					if relationErr != nil {
						return PreviewResult{}, withMutationPath(
							relationErr,
							valuePath,
						)
					}
					v2Result.ProductValue = normalizedRelation
					if v2Result.Present {
						v2Result.PhysicalValues[field.PhysicalName] = normalizedRelation
					}
				}
				for physicalName, physicalValue := range v2Result.PhysicalValues {
					values[physicalName] = physicalValue
				}
				suppliedV2[v2Field.Identity.FieldID] = struct{}{}
				continue
			}
			if field.Kind == schema.FieldKindRelation {
				normalizedRelation, err := validateRelationValue(
					ctx,
					app,
					definition,
					field,
					value,
					pendingRecordIDs,
				)
				if err != nil {
					return PreviewResult{}, withMutationPath(
						err,
						valuePath,
					)
				}
				value = normalizedRelation
			} else if err := schema.ValidateFieldValue(field, value); err != nil {
				var valueErr *schema.ValueError
				details := map[string]any{"fieldId": field.FieldID}
				if errors.As(err, &valueErr) && valueErr.InstancePath != "" {
					details["instancePath"] = valueErr.InstancePath
				}
				return PreviewResult{}, mutationError(
					"mutation.field.invalid_value",
					stringPointer(valuePath),
					err.Error(),
					details,
					false,
				)
			}
			values[field.PhysicalName] = value
		}
		if operation.Kind == OperationInsert {
			for _, v2Field := range v2Fields {
				if _, supplied := suppliedV2[v2Field.Identity.FieldID]; supplied {
					continue
				}
				result, normalizeErr := fieldvalue.New().NormalizeWrite(
					ctx, v2Field, fieldvalue.Insert,
					fieldvalue.Input{Supplied: false},
				)
				if normalizeErr != nil {
					return PreviewResult{}, mutationError(
						"mutation.field.invalid_value",
						stringPointer(path+".values."+v2Field.Identity.PhysicalName),
						normalizeErr.Error(),
						map[string]any{"fieldId": v2Field.Identity.FieldID},
						false,
					)
				}
				legacyField := fields[v2Field.Identity.FieldID]
				if result.Write && legacyField.Kind == schema.FieldKindRelation {
					normalizedRelation, relationErr := validateRelationValue(
						ctx,
						app,
						definition,
						legacyField,
						result.ProductValue,
						pendingRecordIDs,
					)
					if relationErr != nil {
						return PreviewResult{}, withMutationPath(
							relationErr,
							path+".values."+v2Field.Identity.PhysicalName,
						)
					}
					result.ProductValue = normalizedRelation
					if result.Present {
						result.PhysicalValues[legacyField.PhysicalName] = normalizedRelation
					}
				}
				for physicalName, physicalValue := range result.PhysicalValues {
					values[physicalName] = physicalValue
				}
			}
			for _, field := range definition.Fields {
				if _, managed := v2ByAlias[field.FieldID]; managed {
					continue
				}
				if _, supplied := values[field.PhysicalName]; supplied ||
					field.DefaultValue == nil ||
					field.ReadOnly ||
					field.Kind == schema.FieldKindAttachment ||
					field.Kind == schema.FieldKindFormula ||
					field.Kind == schema.FieldKindLookup ||
					field.Kind == schema.FieldKindSystem {
					continue
				}
				defaultValue, cloneErr := cloneProductValue(field.DefaultValue)
				if cloneErr != nil {
					return PreviewResult{}, mutationError(
						"mutation.schema.invalid_default",
						stringPointer(path+".values."+field.PhysicalName),
						"field default could not be applied",
						map[string]any{"fieldId": field.FieldID},
						false,
					)
				}
				if field.Kind != schema.FieldKindRelation {
					if err := schema.ValidateFieldValue(field, defaultValue); err != nil {
						return PreviewResult{}, mutationError(
							"mutation.schema.invalid_default",
							stringPointer(path+".values."+field.PhysicalName),
							"field default is invalid",
							map[string]any{"fieldId": field.FieldID},
							false,
						)
					}
				}
				values[field.PhysicalName] = defaultValue
			}
		}
		if err := validateM2AJunctionValues(
			ctx, app, definition, operation, values,
		); err != nil {
			return PreviewResult{}, withMutationPath(err, path+".values")
		}
		normalized = append(normalized, NormalizedOperation{
			Kind: operation.Kind, RecordID: operation.RecordID, Values: values,
			ExpectedRevision: operation.ExpectedRevision,
			ExpectedDigest:   operation.ExpectedDigest,
		})
	}
	return PreviewResult{Definition: definition, Operations: normalized}, nil
}

func cloneProductValue(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var cloned any
	if err := decoder.Decode(&cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func isArchiveField(
	definition schema.TableDefinition,
	field schema.FieldDefinition,
) bool {
	return definition.ArchivePolicy.FieldID != nil &&
		field.FieldID == *definition.ArchivePolicy.FieldID
}

func validateRequestShape(request Request) error {
	if request.ContractVersion != ContractVersion {
		return mutationError("mutation.contract.unsupported_version", stringPointer("contractVersion"), "contract version must be 1.0", nil, false)
	}
	if request.RequestID == "" || request.IdempotencyKey == "" || request.TableID == "" || request.SchemaRevision == "" {
		return mutationError("mutation.request.invalid", nil, "request identifiers and schema revision are required", nil, false)
	}
	if _, err := schema.ParseSchemaRevision(request.SchemaRevision); err != nil {
		return mutationError(
			"mutation.request.invalid", stringPointer("schemaRevision"),
			"schema revision is invalid", nil, false,
		)
	}
	if request.ExpectedRevision != nil &&
		!rowRevisionPattern.MatchString(*request.ExpectedRevision) {
		return mutationError(
			"mutation.request.invalid", stringPointer("expectedRevision"),
			"expected row revision is invalid", nil, false,
		)
	}
	if request.ExpectedDigest != nil &&
		!rowDigestPattern.MatchString(*request.ExpectedDigest) {
		return mutationError(
			"mutation.request.invalid", stringPointer("expectedDigest"),
			"expected row digest is invalid", nil, false,
		)
	}
	if request.Actor.ID == "" {
		return mutationError("mutation.actor.invalid", stringPointer("actor.id"), "actor id is required", nil, false)
	}
	for index, operation := range request.Operations {
		if operation.ExpectedRevision != nil &&
			!rowRevisionPattern.MatchString(*operation.ExpectedRevision) {
			return mutationError(
				"mutation.request.invalid",
				stringPointer(fmt.Sprintf("operations[%d].expectedRevision", index)),
				"expected row revision is invalid", nil, false,
			)
		}
		if operation.ExpectedDigest != nil &&
			!rowDigestPattern.MatchString(*operation.ExpectedDigest) {
			return mutationError(
				"mutation.request.invalid",
				stringPointer(fmt.Sprintf("operations[%d].expectedDigest", index)),
				"expected row digest is invalid", nil, false,
			)
		}
		if operation.Kind == OperationInsert &&
			(operation.ExpectedRevision != nil || operation.ExpectedDigest != nil) {
			return mutationError(
				"mutation.guard.invalid",
				stringPointer(fmt.Sprintf("operations[%d]", index)),
				"insert operations cannot carry row guards", nil, false,
			)
		}
	}
	switch request.Actor.Type {
	case "user", "system", "plugin", "import", "restore":
	default:
		return mutationError("mutation.actor.invalid", stringPointer("actor.type"), "unknown actor type", nil, false)
	}
	if len(request.Operations) == 0 || len(request.Operations) > maxOperations {
		return mutationError("mutation.batch.limit", stringPointer("operations"), "operation count is outside the supported limit", map[string]any{"max": maxOperations}, false)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return mutationError(
			"mutation.request.invalid", nil,
			"mutation request cannot be serialized", nil, false,
		)
	}
	if len(raw) > maxRequestBytes {
		return mutationError("mutation.body.limit", nil, "mutation request exceeds the supported body limit", map[string]any{"maxBytes": maxRequestBytes}, false)
	}
	for index, operation := range request.Operations {
		path := fmt.Sprintf("operations[%d]", index)
		switch operation.Kind {
		case OperationInsert, OperationUpdate:
			if operation.Values == nil {
				return mutationError(
					"mutation.operation.invalid", stringPointer(path+".values"),
					"insert and update values must be an object", nil, false,
				)
			}
			if operation.Kind == OperationUpdate &&
				len(operation.Values)+len(operation.RawValues) == 0 &&
				(request.Actor.Type != "system" ||
					(request.Actor.ID != "formula-backfill" &&
						request.Actor.ID != "formula-fanout")) {
				return mutationError(
					"mutation.operation.empty", stringPointer(path+".values"),
					"empty updates are reserved for formula recalculation jobs", nil, false,
				)
			}
			if len(operation.Values)+len(operation.RawValues) > maxValues {
				return mutationError("mutation.values.limit", stringPointer(path), "operation has too many values", map[string]any{"max": maxValues}, false)
			}
			if operation.FieldID != "" ||
				operation.UploadHandles != nil ||
				operation.RemoveStoredNames != nil {
				return mutationError(
					"mutation.operation.invalid", stringPointer(path),
					"insert and update contain attachment-only fields", nil, false,
				)
			}
		case OperationArchive, OperationRestore, OperationDelete:
			if operation.Values != nil || operation.RawValues != nil ||
				operation.FieldID != "" ||
				operation.UploadHandles != nil ||
				operation.RemoveStoredNames != nil {
				return mutationError(
					"mutation.operation.invalid", stringPointer(path),
					"record operation contains unsupported fields", nil, false,
				)
			}
		case OperationSetAttachments:
			if operation.Values != nil || operation.RawValues != nil {
				return mutationError(
					"mutation.operation.invalid", stringPointer(path),
					"attachment operation contains unsupported values", nil, false,
				)
			}
		default:
			return mutationError("mutation.operation.unsupported", stringPointer(path+".kind"), "unknown mutation operation", nil, false)
		}
	}
	return nil
}

func mutationError(code string, path *string, message string, details map[string]any, retryable bool) *ProductError {
	if details == nil {
		details = map[string]any{}
	}
	return &ProductError{
		ContractVersion: ContractVersion, Code: code, Path: path,
		Message: message, Details: details, Retryable: retryable,
	}
}

func stringPointer(value string) *string {
	return &value
}
