// Package importhistory owns the durable projection of file import results.
// It is the only persistent record of import outcomes: entries start as
// interrupted, are promoted to succeeded exclusively inside the business
// mutation transaction that committed the rows, and receive failed/cancelled/
// aborted only from the Host worker's terminal execution reports. The store
// never persists tokens, grants, original file paths or credentials.
package importhistory

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

// CollectionName is the internal locked PocketBase collection backing the
// projection. It is not part of the business table registry and is never
// exposed through the standard PocketBase REST surface.
const CollectionName = "vibetable_import_history"

// ListLimit is the fixed newest-first cap of the GET projection.
const ListLimit = 200

// Entry states. interrupted is the initial projection state — the persisted
// history never claims a live import.
const (
	StateInterrupted = "interrupted"
	StateSucceeded   = "succeeded"
	StateFailed      = "failed"
	StateCancelled   = "cancelled"
	StateAborted     = "aborted"
)

// Commit evidence states. unknown means no committed evidence exists; it is
// never interpreted as "zero rows written".
const (
	CommitStateUnknown   = "unknown"
	CommitStateCommitted = "committed"
)

// Fixed safe diagnostic codes carried by errorCode.
const (
	CodeInterrupted = "import.interrupted"
	CodeFailed      = "import.failed"
	CodeCancelled   = "import.cancelled"
	CodeAborted     = "import.aborted"
)

// SupportedSourceTypes are the accepted normalized source formats. xlsm is
// projected as xlsx by the Host and normalized again here defensively.
const (
	SourceCSV  = "csv"
	SourceXLSX = "xlsx"
)

const maxSessionEpoch = int64(1<<53 - 1)

var (
	taskIDPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	idempotencyPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	collectionPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	sourceNameForbidden = regexp.MustCompile(`[\x00-\x1f/]`)
)

// Error is the stable product error of the import history projection.
type Error struct {
	Code    string         `json:"code"`
	Path    string         `json:"path"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

func (err *Error) Error() string {
	return err.Code + " at " + err.Path + ": " + err.Message
}

func newError(code, path, message string, details map[string]any) *Error {
	if details == nil {
		details = map[string]any{}
	}
	return &Error{Code: code, Path: path, Message: message, Details: details}
}

func invalid(path, message string) *Error {
	return newError("import_history.request.invalid", path, message, nil)
}

func storageFailure() *Error {
	return newError("import_history.storage.failed", "", "import history storage failed", nil)
}

// Entry is the wire projection returned by start, finish and the list port.
// Counts and terminal timestamps are nil while unknown.
type Entry struct {
	TaskID string `json:"taskId"`
	// IdempotencyKey is the Go-side attribution binding; it is deliberately
	// not part of the public wire entry.
	IdempotencyKey string  `json:"-"`
	Collection     string  `json:"collection"`
	SourceType     string  `json:"sourceType"`
	SourceName     string  `json:"sourceName"`
	State          string  `json:"state"`
	CommitState    string  `json:"commitState"`
	CreatedCount   *int64  `json:"createdCount"`
	UpdatedCount   *int64  `json:"updatedCount"`
	StartedAt      string  `json:"startedAt"`
	FinishedAt     *string `json:"finishedAt"`
	SessionEpoch   uint64  `json:"sessionEpoch"`
	ErrorCode      *string `json:"errorCode"`
}

// StartInput is the validated start binding minted by the Host before the
// import worker executes.
type StartInput struct {
	TaskID         string
	Collection     string
	SourceType     string
	SourceName     string
	IdempotencyKey string
	SessionEpoch   uint64
}

// TerminalState reports whether the state is a terminal projection state.
func TerminalState(state string) bool {
	switch state {
	case StateSucceeded, StateFailed, StateCancelled, StateAborted:
		return true
	default:
		return false
	}
}

// NormalizeSourceType folds the accepted source formats onto csv/xlsx.
func NormalizeSourceType(sourceType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(sourceType)) {
	case "csv":
		return SourceCSV, true
	case "xlsx", "xlsm":
		return SourceXLSX, true
	default:
		return "", false
	}
}

func (input StartInput) validate() error {
	if !taskIDPattern.MatchString(input.TaskID) {
		return invalid("taskId", "taskId is required and must stay a bounded identifier")
	}
	if !collectionPattern.MatchString(input.Collection) {
		return invalid("collection", "collection is required and must stay a bounded identifier")
	}
	normalized, ok := NormalizeSourceType(input.SourceType)
	if !ok {
		return invalid("sourceType", "sourceType must be csv or xlsx")
	}
	input.SourceType = normalized
	if !validSourceName(input.SourceName) {
		return invalid("sourceName", "sourceName must be a short file name, not a path or URL")
	}
	if !idempotencyPattern.MatchString(input.IdempotencyKey) {
		return invalid("idempotencyKey", "idempotencyKey is required and must stay a bounded identifier")
	}
	if int64(input.SessionEpoch) < 1 || int64(input.SessionEpoch) > maxSessionEpoch {
		return invalid("sessionEpoch", "sessionEpoch must be at least 1")
	}
	return nil
}

// validSourceName accepts only a safe short display file name bounded by the
// Host file name contract (256 unicode characters). Path separators and
// control characters are rejected so the projection never persists an
// original path, URL or credential-shaped value.
func validSourceName(sourceName string) bool {
	trimmed := strings.TrimSpace(sourceName)
	if trimmed == "" || trimmed != sourceName ||
		utf8.RuneCountInString(sourceName) > 256 {
		return false
	}
	if sourceNameForbidden.MatchString(sourceName) || strings.Contains(sourceName, "\\") {
		return false
	}
	if strings.Contains(strings.ToLower(sourceName), "://") {
		return false
	}
	if strings.Contains(sourceName, ":") {
		return false
	}
	return true
}

// Store projects import outcomes into the internal PocketBase collection.
// All mutating operations take the executing core.App so the same helpers run
// inside the coordinated mutation transaction when required.
type Store struct {
	now func() time.Time
}

// StoreOption customizes the store.
type StoreOption func(*Store)

// WithClock pins the projection clock for tests.
func WithClock(clock func() time.Time) StoreOption {
	return func(store *Store) {
		if clock != nil {
			store.now = clock
		}
	}
}

// NewStore creates the projection store.
func NewStore(options ...StoreOption) *Store {
	store := &Store{now: func() time.Time { return time.Now().UTC() }}
	for _, option := range options {
		option(store)
	}
	return store
}

// Start records the import binding before the worker executes. The taskId is
// idempotent: replaying an identical start returns the persisted entry, while
// any binding change is rejected. A different task reusing the same
// idempotency key for the same collection is rejected by the unique
// attribution index. The write runs in one PocketBase transaction that also
// persists the coordinated workspace business receipt, mirroring the metadata
// and mutation patterns.
func (store *Store) Start(
	ctx context.Context,
	app core.App,
	input StartInput,
) (Entry, error) {
	if err := input.validate(); err != nil {
		return Entry{}, err
	}
	input.SourceType, _ = NormalizeSourceType(input.SourceType)
	var entry Entry
	err := app.RunInTransaction(func(txApp core.App) (transactionErr error) {
		defer func() {
			if transactionErr == nil {
				transactionErr = writecoordinator.PersistPocketBaseReceipt(
					ctx, txApp, store.now(),
				)
			}
		}()
		if err := ctx.Err(); err != nil {
			return err
		}
		existing, err := findByTaskID(txApp, input.TaskID)
		if err != nil {
			return err
		}
		if existing != nil {
			entry = entryFromRecord(existing)
			if entry.Collection != input.Collection ||
				entry.SourceType != input.SourceType ||
				entry.SourceName != input.SourceName ||
				entry.IdempotencyKey != input.IdempotencyKey ||
				entry.SessionEpoch != input.SessionEpoch {
				return newError(
					"import_history.task_conflict", "taskId",
					"taskId is already bound to a different import", nil,
				)
			}
			return nil
		}
		duplicate, err := txApp.FindRecordsByFilter(
			CollectionName,
			"idempotency_key={:key}",
			"",
			0,
			0,
			dbx.Params{"key": input.IdempotencyKey},
		)
		if err != nil {
			return storageFailure()
		}
		// The Host may pass either the logical tableId or one of its physical
		// aliases; attribution is reconciled through the schema catalog so the
		// same import target cannot be attributed twice under two spellings.
		aliases, err := tableAliases(txApp, input.Collection)
		if err != nil {
			return err
		}
		for _, existing := range duplicate {
			if aliases[existing.GetString("collection")] {
				return newError(
					"import_history.idempotency_conflict", "idempotencyKey",
					"idempotency key already attributes an import for this collection", nil,
				)
			}
		}
		collection, err := txApp.FindCollectionByNameOrId(CollectionName)
		if err != nil {
			return storageFailure()
		}
		record := core.NewRecord(collection)
		record.Set("task_id", input.TaskID)
		record.Set("collection", input.Collection)
		record.Set("source_type", input.SourceType)
		record.Set("source_name", input.SourceName)
		record.Set("idempotency_key", input.IdempotencyKey)
		record.Set("session_epoch", int64(input.SessionEpoch))
		record.Set("state", StateInterrupted)
		record.Set("commit_state", CommitStateUnknown)
		record.Set("error_code", CodeInterrupted)
		record.Set("started_at", store.now().UTC())
		if err := txApp.Save(record); err != nil {
			if isUniqueViolation(err) {
				return newError(
					"import_history.idempotency_conflict", "idempotencyKey",
					"idempotency key already attributes an import for this collection", nil,
				)
			}
			return storageFailure()
		}
		entry = entryFromRecord(record)
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// Finish records a Host-reported terminal execution outcome. Only the
// non-terminal projection may transition, and a Go-committed success can
// never be overwritten. Repeating a terminal report is idempotent and keeps
// the first terminal truth. The write shares one PocketBase transaction with
// the coordinated workspace business receipt.
func (store *Store) Finish(
	ctx context.Context,
	app core.App,
	taskID, state string,
) (Entry, error) {
	if !taskIDPattern.MatchString(taskID) {
		return Entry{}, invalid("taskId", "taskId is required and must stay a bounded identifier")
	}
	errorCode, ok := errorCodeForState(state)
	if !ok {
		return Entry{}, invalid("state", "state must be failed, cancelled or aborted")
	}
	var entry Entry
	err := app.RunInTransaction(func(txApp core.App) (transactionErr error) {
		defer func() {
			if transactionErr == nil {
				transactionErr = writecoordinator.PersistPocketBaseReceipt(
					ctx, txApp, store.now(),
				)
			}
		}()
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := findByTaskID(txApp, taskID)
		if err != nil {
			return err
		}
		if record == nil {
			return newError(
				"import_history.task_not_found", "taskId",
				"import history entry was not found", nil,
			)
		}
		entry = entryFromRecord(record)
		if TerminalState(entry.State) {
			// First terminal truth wins; a committed success can never regress.
			return nil
		}
		record.Set("state", state)
		record.Set("error_code", errorCode)
		record.Set("finished_at", store.now().UTC())
		if err := txApp.Save(record); err != nil {
			return storageFailure()
		}
		entry = entryFromRecord(record)
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// Get returns the current projection of a task.
func (store *Store) Get(ctx context.Context, app core.App, taskID string) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	record, err := findByTaskID(app, taskID)
	if err != nil {
		return Entry{}, err
	}
	if record == nil {
		return Entry{}, newError(
			"import_history.task_not_found", "taskId",
			"import history entry was not found", nil,
		)
	}
	return entryFromRecord(record), nil
}

// List returns the newest-first bounded projection.
func (store *Store) List(ctx context.Context, app core.App) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := app.FindRecordsByFilter(
		CollectionName, "", "-started_at,task_id", ListLimit, 0, nil,
	)
	if err != nil {
		return nil, storageFailure()
	}
	entries := make([]Entry, 0, len(records))
	for _, record := range records {
		entries = append(entries, entryFromRecord(record))
	}
	return entries, nil
}

// CommitApplied promotes the projection inside the business mutation
// transaction: the durable success is written by the exact transaction that
// committed the rows, so a rollback discards it. Counts are derived only from
// the Go Receipt. Entries whose key or table do not match are left untouched,
// and an already-succeeded projection is never rewritten.
func (store *Store) CommitApplied(
	app core.App,
	idempotencyKey, collection string,
	receipt mutation.Receipt,
) error {
	if idempotencyKey == "" || collection == "" ||
		receipt.Status != mutation.StatusApplied {
		return nil
	}
	// The mutation request carries the logical tableId while the Host may
	// have started the projection with a physical alias (or vice versa);
	// reconcile through the schema catalog instead of assuming both values
	// are always identical.
	aliases, err := tableAliases(app, collection)
	if err != nil {
		return storageFailure()
	}
	candidates, err := app.FindRecordsByFilter(
		CollectionName,
		"idempotency_key={:key}",
		"",
		0,
		0,
		dbx.Params{"key": idempotencyKey},
	)
	if err != nil {
		return storageFailure()
	}
	var record *core.Record
	for _, candidate := range candidates {
		if aliases[candidate.GetString("collection")] {
			// The (idempotency_key, collection) uniqueness plus catalog aliasing
			// leave at most one attributable entry per key and target table.
			record = candidate
			break
		}
	}
	if record == nil || record.GetString("state") == StateSucceeded {
		return nil
	}
	created, updated, importShaped := receiptCounts(receipt)
	if !importShaped {
		// Only insert/update operations are import-shaped. A mutation
		// carrying archive/delete/setAttachments is never attributed as this
		// import's success; the projection keeps waiting for real evidence.
		return nil
	}
	record.Set("state", StateSucceeded)
	record.Set("commit_state", CommitStateCommitted)
	record.Set("created_count", created)
	record.Set("updated_count", updated)
	record.Set("finished_at", store.now().UTC())
	record.Set("error_code", nil)
	if err := app.Save(record); err != nil {
		return storageFailure()
	}
	return nil
}

// receiptCounts derives the import counts from the authoritative receipt
// operations only. The final result reports whether every operation was
// import-shaped (insert/update); anything else refuses attribution.
func receiptCounts(receipt mutation.Receipt) (int64, int64, bool) {
	var created, updated int64
	for _, row := range receipt.AffectedRows {
		switch row.Operation {
		case mutation.OperationInsert:
			created++
		case mutation.OperationUpdate:
			updated++
		default:
			return 0, 0, false
		}
	}
	return created, updated, true
}

// Kernel is the authoritative mutation seam the projection wraps. It mirrors
// the narrow interface registered by the mutation routes.
type Kernel interface {
	Preview(context.Context, mutation.Request) (mutation.PreviewResult, error)
	ApplyWithCommit(
		context.Context,
		mutation.Request,
		func(core.App, mutation.Receipt) error,
	) (mutation.Receipt, error)
}

// ProjectedKernel adapts a mutation kernel so every apply promotes a matching
// import projection inside the same business transaction.
type ProjectedKernel struct {
	kernel Kernel
	store  *Store
}

// Project wraps the mutation kernel used by the mutation routes. Replay paths
// never invoke the commit callback, so a replayed import leaves the already
// persisted success untouched.
func Project(kernel Kernel, store *Store) *ProjectedKernel {
	return &ProjectedKernel{kernel: kernel, store: store}
}

// Preview passes through unchanged.
func (projected *ProjectedKernel) Preview(
	ctx context.Context,
	request mutation.Request,
) (mutation.PreviewResult, error) {
	return projected.kernel.Preview(ctx, request)
}

// Apply commits the business mutation and the import projection atomically.
func (projected *ProjectedKernel) Apply(
	ctx context.Context,
	request mutation.Request,
) (mutation.Receipt, error) {
	return projected.kernel.ApplyWithCommit(
		ctx,
		request,
		func(app core.App, receipt mutation.Receipt) error {
			return projected.store.CommitApplied(
				app, request.IdempotencyKey, request.TableID, receipt,
			)
		},
	)
}

func findByTaskID(app core.App, taskID string) (*core.Record, error) {
	record, err := app.FindFirstRecordByFilter(
		CollectionName,
		"task_id={:task}",
		dbx.Params{"task": taskID},
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, storageFailure()
	}
	return record, nil
}

// tableAliases resolves the canonical table identity through the existing
// schema catalog (vibetable_tables): an identifier is accepted as the logical
// tableId, the physical collection name or the PocketBase collection id, and
// every accepted alias of the same table maps onto one attribution identity.
// Unknown identifiers degrade to exact-string matching so projections for
// deleted tables still settle.
func tableAliases(app core.App, identifier string) (map[string]bool, error) {
	aliases := map[string]bool{identifier: true}
	record, err := app.FindFirstRecordByFilter(
		"vibetable_tables",
		"table_id={:id} || physical_name={:id} || collection_id={:id}",
		dbx.Params{"id": identifier},
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return aliases, nil
		}
		return nil, storageFailure()
	}
	if record == nil {
		return aliases, nil
	}
	aliases[record.GetString("table_id")] = true
	aliases[record.GetString("physical_name")] = true
	aliases[record.GetString("collection_id")] = true
	return aliases, nil
}

func errorCodeForState(state string) (string, bool) {
	switch state {
	case StateFailed:
		return CodeFailed, true
	case StateCancelled:
		return CodeCancelled, true
	case StateAborted:
		return CodeAborted, true
	default:
		return "", false
	}
}

func entryFromRecord(record *core.Record) Entry {
	entry := Entry{
		TaskID:         record.GetString("task_id"),
		IdempotencyKey: record.GetString("idempotency_key"),
		Collection:     record.GetString("collection"),
		SourceType:     record.GetString("source_type"),
		SourceName:     record.GetString("source_name"),
		State:          record.GetString("state"),
		CommitState:    record.GetString("commit_state"),
		StartedAt:      formatTimestamp(record.GetDateTime("started_at")),
		SessionEpoch:   uint64(record.GetInt("session_epoch")),
	}
	// Counts are evidence-bound: without committed evidence the wire entry
	// reports unknown (null) counts regardless of storage zero values.
	if entry.CommitState == CommitStateCommitted {
		created := int64(record.GetInt("created_count"))
		updated := int64(record.GetInt("updated_count"))
		entry.CreatedCount = &created
		entry.UpdatedCount = &updated
	}
	if finished := record.GetDateTime("finished_at"); !finished.IsZero() {
		formatted := formatTimestamp(finished)
		entry.FinishedAt = &formatted
	}
	if code := record.GetString("error_code"); code != "" {
		entry.ErrorCode = &code
	}
	return entry
}

func formatTimestamp(value pbtypes.DateTime) string {
	if value.IsZero() {
		return ""
	}
	// PocketBase persists date fields at millisecond precision; normalize the
	// projection to stable RFC3339 with that precision.
	return value.Time().UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano)
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(
		strings.ToLower(err.Error()), "unique constraint failed",
	)
}
