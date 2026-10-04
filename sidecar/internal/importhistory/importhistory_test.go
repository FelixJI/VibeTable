package importhistory_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/importhistory"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestCommitAppliedRefusesNonImportOperations(t *testing.T) {
	app := newMigratedApp(t)
	store := importhistory.NewStore()
	entry, err := store.Start(context.Background(), app, startInput("task-ops"))
	if err != nil {
		t.Fatal(err)
	}
	// archive/delete/setAttachments-shaped receipts must never be attributed
	// as this import's success; the existing BFF import flow only emits
	// insert/update, so any other operation refuses attribution entirely.
	for _, operation := range []mutation.OperationKind{
		mutation.OperationArchive,
		mutation.OperationDelete,
		mutation.OperationSetAttachments,
		mutation.OperationRestore,
	} {
		receipt := appliedReceipt(0, 0)
		receipt.AffectedRows = []mutation.AffectedRow{{Operation: operation}}
		if err := store.CommitApplied(app, entry.IdempotencyKey, entry.Collection, receipt); err != nil {
			t.Fatal(err)
		}
		current, err := store.Get(context.Background(), app, "task-ops")
		if err != nil || current.State != importhistory.StateInterrupted ||
			current.CommitState != importhistory.CommitStateUnknown ||
			current.CreatedCount != nil {
			t.Fatalf("%s attributed import success: %#v, %v", operation, current, err)
		}
	}
	// A mixed insert+delete batch is equally refused.
	mixed := appliedReceipt(1, 0)
	mixed.AffectedRows = append(mixed.AffectedRows, mutation.AffectedRow{Operation: mutation.OperationDelete})
	if err := store.CommitApplied(app, entry.IdempotencyKey, entry.Collection, mixed); err != nil {
		t.Fatal(err)
	}
	current, err := store.Get(context.Background(), app, "task-ops")
	if err != nil || current.State != importhistory.StateInterrupted {
		t.Fatalf("mixed batch attributed import success: %#v, %v", current, err)
	}
}

func TestStartPersistsWorkspaceReceiptInSameTransaction(t *testing.T) {
	app := newMigratedApp(t)
	if err := writecoordinator.EnsurePocketBaseReceiptTable(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	store := importhistory.NewStore()
	bound, err := writecoordinator.WithBusinessIntent(
		context.Background(),
		writecoordinator.WriteIntent{
			Token: writecoordinator.Token{
				WorkspaceID:  "11111111-1111-4111-8111-111111111111",
				SessionEpoch: 7, FenceEpoch: 3,
				ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			},
			MutationRevision: 41,
			AuditSourceEpoch: "business-v2",
		},
		"import_history.start",
		"task-receipt",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Start(bound, app, startInput("task-receipt")); err != nil {
		t.Fatal(err)
	}
	committed, err := writecoordinator.HasPocketBaseReceiptIdentity(
		context.Background(), app,
		"11111111-1111-4111-8111-111111111111",
		"import_history.start", "task-receipt",
	)
	if err != nil || !committed {
		t.Fatalf("coordinated start receipt = %v, %v", committed, err)
	}

	// Finish persists the same receipt contract for its coordinated identity.
	finishBound, err := writecoordinator.WithBusinessIntent(
		context.Background(),
		writecoordinator.WriteIntent{
			Token: writecoordinator.Token{
				WorkspaceID:  "11111111-1111-4111-8111-111111111111",
				SessionEpoch: 7, FenceEpoch: 3,
				ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			},
			MutationRevision: 42,
			AuditSourceEpoch: "business-v2",
		},
		"import_history.finish",
		"task-receipt",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finish(finishBound, app, "task-receipt", importhistory.StateCancelled); err != nil {
		t.Fatal(err)
	}
	committed, err = writecoordinator.HasPocketBaseReceiptIdentity(
		context.Background(), app,
		"11111111-1111-4111-8111-111111111111",
		"import_history.finish", "task-receipt",
	)
	if err != nil || !committed {
		t.Fatalf("coordinated finish receipt = %v, %v", committed, err)
	}
	final, err := store.Get(context.Background(), app, "task-receipt")
	if err != nil || final.State != importhistory.StateCancelled {
		t.Fatalf("finish entry = %#v, %v", final, err)
	}
}

func newMigratedApp(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	pb := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  t.TempDir(),
		HideStartBanner: true,
	})
	migrations.Register(pb)
	if err := pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		event := &core.TerminateEvent{App: pb}
		if err := pb.OnTerminate().Trigger(event, func(event *core.TerminateEvent) error {
			return event.App.ResetBootstrapState()
		}); err != nil {
			t.Error(err)
		}
	})
	if err := pb.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return pb
}

func fixedClock(start time.Time) func() time.Time {
	current := start
	return func() time.Time {
		current = current.Add(time.Second)
		return current
	}
}

func startInput(taskID string) importhistory.StartInput {
	return importhistory.StartInput{
		TaskID:         taskID,
		Collection:     "tbl_imports",
		SourceType:     "csv",
		SourceName:     "订单-2026.csv",
		IdempotencyKey: "import-" + taskID,
		SessionEpoch:   7,
	}
}

func TestStartIsTaskIdempotentAndRejectsBindingChanges(t *testing.T) {
	app := newMigratedApp(t)
	store := importhistory.NewStore(importhistory.WithClock(fixedClock(
		time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
	)))

	entry, err := store.Start(context.Background(), app, startInput("task-0001"))
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != importhistory.StateInterrupted ||
		entry.CommitState != importhistory.CommitStateUnknown ||
		entry.CreatedCount != nil || entry.UpdatedCount != nil ||
		entry.FinishedAt != nil {
		t.Fatalf("initial entry = %#v", entry)
	}
	if entry.ErrorCode == nil || *entry.ErrorCode != importhistory.CodeInterrupted {
		t.Fatalf("initial errorCode = %v", entry.ErrorCode)
	}
	if entry.StartedAt == "" || !strings.HasSuffix(entry.StartedAt, "Z") {
		t.Fatalf("startedAt = %q", entry.StartedAt)
	}

	replayed, err := store.Start(context.Background(), app, startInput("task-0001"))
	if err != nil || replayed.TaskID != entry.TaskID ||
		replayed.StartedAt != entry.StartedAt {
		t.Fatalf("start replay = %#v, %v", replayed, err)
	}

	changed := startInput("task-0001")
	changed.Collection = "tbl_other"
	_, err = store.Start(context.Background(), app, changed)
	assertCode(t, err, "import_history.task_conflict")

	changed = startInput("task-0001")
	changed.IdempotencyKey = "import-other"
	_, err = store.Start(context.Background(), app, changed)
	assertCode(t, err, "import_history.task_conflict")

	// The same idempotency key cannot attribute a second task to the same
	// collection: the unique attribution index rejects the false double
	// attribution.
	duplicate := startInput("task-0002")
	duplicate.IdempotencyKey = startInput("task-0001").IdempotencyKey
	_, err = store.Start(context.Background(), app, duplicate)
	assertCode(t, err, "import_history.idempotency_conflict")
}

func TestStartRejectsCredentialShapedAndPathLikeValues(t *testing.T) {
	app := newMigratedApp(t)
	store := importhistory.NewStore()
	cases := []struct {
		name   string
		mutate func(importhistory.StartInput) importhistory.StartInput
	}{
		{"original path", func(input importhistory.StartInput) importhistory.StartInput {
			input.SourceName = `C:\Users\felji\Documents\订单.csv`
			return input
		}},
		{"url", func(input importhistory.StartInput) importhistory.StartInput {
			input.SourceName = "https://example.com/orders.csv"
			return input
		}},
		{"posix path", func(input importhistory.StartInput) importhistory.StartInput {
			input.SourceName = "orders/final.csv"
			return input
		}},
		{"drive prefix", func(input importhistory.StartInput) importhistory.StartInput {
			input.SourceName = "C:orders.csv"
			return input
		}},
		{"control characters", func(input importhistory.StartInput) importhistory.StartInput {
			input.SourceName = "orders\x01.csv"
			return input
		}},
		{"empty", func(input importhistory.StartInput) importhistory.StartInput {
			input.SourceName = ""
			return input
		}},
		{"bad source type", func(input importhistory.StartInput) importhistory.StartInput {
			input.SourceType = "json"
			return input
		}},
		{"zero session epoch", func(input importhistory.StartInput) importhistory.StartInput {
			input.SessionEpoch = 0
			return input
		}},
		{"bad task id", func(input importhistory.StartInput) importhistory.StartInput {
			input.TaskID = "../escape"
			return input
		}},
		{"empty idempotency", func(input importhistory.StartInput) importhistory.StartInput {
			input.IdempotencyKey = ""
			return input
		}},
	}
	for _, testCase := range cases {
		_, err := store.Start(context.Background(), app, testCase.mutate(startInput("task-shape")))
		if !errors.As(err, new(*importhistory.Error)) {
			t.Fatalf("%s: error = %#v", testCase.name, err)
		} else if err.(*importhistory.Error).Code != "import_history.request.invalid" {
			t.Fatalf("%s: code = %s", testCase.name, err.(*importhistory.Error).Code)
		}
	}
	// xlsm folds onto the xlsx projection and the Host file name contract
	// accepts up to 256 unicode characters.
	folded := startInput("task-xlsm")
	folded.SourceType = "xlsm"
	entry, err := store.Start(context.Background(), app, folded)
	if err != nil || entry.SourceType != importhistory.SourceXLSX {
		t.Fatalf("xlsm fold = %#v, %v", entry, err)
	}
	exact := startInput("task-length")
	exact.SourceName = strings.Repeat("订", 256)
	if _, err := store.Start(context.Background(), app, exact); err != nil {
		t.Fatalf("256-rune source name rejected: %v", err)
	}
	tooLong := startInput("task-length-2")
	tooLong.SourceName = strings.Repeat("订", 257)
	_, err = store.Start(context.Background(), app, tooLong)
	if codeOf(err) != "import_history.request.invalid" {
		t.Fatalf("257-rune source name = %v", err)
	}
}

func TestFinishKeepsFirstTerminalTruthAndNeverOverridesCommit(t *testing.T) {
	app := newMigratedApp(t)
	store := importhistory.NewStore(importhistory.WithClock(fixedClock(
		time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
	)))
	if _, err := store.Start(context.Background(), app, startInput("task-finish")); err != nil {
		t.Fatal(err)
	}

	finished, err := store.Finish(context.Background(), app, "task-finish", importhistory.StateFailed)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != importhistory.StateFailed ||
		finished.CommitState != importhistory.CommitStateUnknown ||
		finished.FinishedAt == nil ||
		finished.ErrorCode == nil || *finished.ErrorCode != importhistory.CodeFailed ||
		finished.CreatedCount != nil || finished.UpdatedCount != nil {
		t.Fatalf("failed entry = %#v", finished)
	}

	// Repeated finish is idempotent and keeps the first terminal state.
	repeated, err := store.Finish(context.Background(), app, "task-finish", importhistory.StateFailed)
	if err != nil || repeated.State != importhistory.StateFailed ||
		*repeated.FinishedAt != *finished.FinishedAt {
		t.Fatalf("repeated finish = %#v, %v", repeated, err)
	}
	swapped, err := store.Finish(context.Background(), app, "task-finish", importhistory.StateCancelled)
	if err != nil || swapped.State != importhistory.StateFailed {
		t.Fatalf("swapped finish = %#v, %v", swapped, err)
	}

	_, err = store.Finish(context.Background(), app, "task-finish", "succeeded")
	assertCode(t, err, "import_history.request.invalid")
	_, err = store.Finish(context.Background(), app, "task-missing", importhistory.StateCancelled)
	assertCode(t, err, "import_history.task_not_found")

	// A Go-committed success can never be overwritten by a late finish.
	committed, err := store.Start(context.Background(), app, startInput("task-committed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitApplied(app, committed.IdempotencyKey, committed.Collection, appliedReceipt(2, 3)); err != nil {
		t.Fatal(err)
	}
	late, err := store.Finish(context.Background(), app, "task-committed", importhistory.StateAborted)
	if err != nil || late.State != importhistory.StateSucceeded ||
		late.CommitState != importhistory.CommitStateCommitted {
		t.Fatalf("late finish = %#v, %v", late, err)
	}
}

func TestCommitAppliedDerivesCountsFromReceiptAndRollsBackWithTransaction(t *testing.T) {
	app := newMigratedApp(t)
	store := importhistory.NewStore(importhistory.WithClock(fixedClock(
		time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
	)))
	entry, err := store.Start(context.Background(), app, startInput("task-commit"))
	if err != nil {
		t.Fatal(err)
	}

	if err := store.CommitApplied(
		app, entry.IdempotencyKey, entry.Collection, appliedReceipt(2, 3),
	); err != nil {
		t.Fatal(err)
	}
	committed, err := store.Get(context.Background(), app, "task-commit")
	if err != nil {
		t.Fatal(err)
	}
	if committed.State != importhistory.StateSucceeded ||
		committed.CommitState != importhistory.CommitStateCommitted ||
		committed.CreatedCount == nil || *committed.CreatedCount != 2 ||
		committed.UpdatedCount == nil || *committed.UpdatedCount != 3 ||
		committed.FinishedAt == nil || committed.ErrorCode != nil {
		t.Fatalf("committed entry = %#v", committed)
	}

	// Non-import keys are a strict no-op, including the same key against a
	// different collection.
	if err := store.CommitApplied(app, "unrelated-key", "tbl_imports", appliedReceipt(9, 9)); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitApplied(app, entry.IdempotencyKey, "tbl_other", appliedReceipt(9, 9)); err != nil {
		t.Fatal(err)
	}
	untouched, err := store.Get(context.Background(), app, "task-commit")
	if err != nil || untouched.CreatedCount == nil || *untouched.CreatedCount != 2 {
		t.Fatalf("no-op commit changed entry: %#v, %v", untouched, err)
	}

	// The promotion must be discarded together with the business transaction
	// when the transaction rolls back after the commit callback ran.
	pending, err := store.Start(context.Background(), app, startInput("task-rollback"))
	if err != nil {
		t.Fatal(err)
	}
	rollbackErr := app.RunInTransaction(func(txApp core.App) error {
		if err := store.CommitApplied(
			txApp, pending.IdempotencyKey, pending.Collection, appliedReceipt(1, 1),
		); err != nil {
			return err
		}
		promoted, err := store.Get(context.Background(), txApp, "task-rollback")
		if err != nil || promoted.State != importhistory.StateSucceeded {
			t.Fatalf("in-transaction entry = %#v, %v", promoted, err)
		}
		return errors.New("business mutation failed")
	})
	if rollbackErr == nil {
		t.Fatal("transaction unexpectedly committed")
	}
	after, err := store.Get(context.Background(), app, "task-rollback")
	if err != nil {
		t.Fatal(err)
	}
	if after.State != importhistory.StateInterrupted ||
		after.CommitState != importhistory.CommitStateUnknown ||
		after.CreatedCount != nil || after.FinishedAt != nil {
		t.Fatalf("rolled back entry = %#v", after)
	}
}

func TestProjectionPersistsAcrossReopenIndependentOfIdempotencyCache(t *testing.T) {
	dataDir := t.TempDir()
	newApp := func(t *testing.T) *pocketbase.PocketBase {
		t.Helper()
		pb := pocketbase.NewWithConfig(pocketbase.Config{
			DefaultDataDir:  dataDir,
			HideStartBanner: true,
		})
		migrations.Register(pb)
		if err := pb.Bootstrap(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			event := &core.TerminateEvent{App: pb}
			if err := pb.OnTerminate().Trigger(event, func(event *core.TerminateEvent) error {
				return event.App.ResetBootstrapState()
			}); err != nil {
				t.Error(err)
			}
		})
		if err := pb.RunAllMigrations(); err != nil {
			t.Fatal(err)
		}
		return pb
	}

	first := newApp(t)
	store := importhistory.NewStore()
	entry, err := store.Start(context.Background(), first, startInput("task-reopen"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitApplied(first, entry.IdempotencyKey, entry.Collection, appliedReceipt(5, 0)); err != nil {
		t.Fatal(err)
	}

	// Simulate the 24h idempotency cache expiring: the cache record is gone
	// while the projection has to survive.
	if _, err := first.DB().NewQuery("DELETE FROM vibetable_idempotency_keys").Execute(); err != nil {
		t.Fatal(err)
	}

	reopened := newApp(t)
	items, err := store.List(context.Background(), reopened)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].TaskID != "task-reopen" ||
		items[0].State != importhistory.StateSucceeded ||
		items[0].CommitState != importhistory.CommitStateCommitted ||
		items[0].CreatedCount == nil || *items[0].CreatedCount != 5 {
		t.Fatalf("reopened projection = %#v", items)
	}
}

func TestListOrdersNewestFirst(t *testing.T) {
	app := newMigratedApp(t)
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	store := importhistory.NewStore()
	for index, taskID := range []string{"task-old", "task-new", "task-mid"} {
		at := base.Add(time.Duration(index) * time.Minute)
		pinned := importhistory.NewStore(importhistory.WithClock(func() time.Time { return at }))
		if _, err := pinned.Start(context.Background(), app, startInput(taskID)); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.List(context.Background(), app)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 ||
		items[0].TaskID != "task-mid" ||
		items[1].TaskID != "task-new" ||
		items[2].TaskID != "task-old" {
		t.Fatalf("order = %#v", items)
	}
}

func TestProjectedKernelAppliesAndProjectsAtomically(t *testing.T) {
	app := newMigratedApp(t)
	store := importhistory.NewStore()
	committed := false
	kernel := importhistory.Project(fakeKernel{
		applyWithCommit: func(
			ctx context.Context,
			request mutation.Request,
			commit func(core.App, mutation.Receipt) error,
		) (mutation.Receipt, error) {
			receipt := appliedReceipt(1, 2)
			if commit == nil {
				return receipt, nil
			}
			if err := commit(app, receipt); err != nil {
				return mutation.Receipt{}, err
			}
			committed = true
			return receipt, nil
		},
	}, store)

	entry, err := store.Start(context.Background(), app, startInput("task-kernel"))
	if err != nil {
		t.Fatal(err)
	}
	request := mutation.Request{
		IdempotencyKey: entry.IdempotencyKey,
		TableID:        entry.Collection,
	}
	if _, err := kernel.Apply(context.Background(), request); err != nil || !committed {
		t.Fatalf("apply = %v, committed=%v", err, committed)
	}
	after, err := store.Get(context.Background(), app, "task-kernel")
	if err != nil || after.State != importhistory.StateSucceeded ||
		after.CreatedCount == nil || *after.CreatedCount != 1 ||
		after.UpdatedCount == nil || *after.UpdatedCount != 2 {
		t.Fatalf("projected entry = %#v, %v", after, err)
	}
}

type fakeKernel struct {
	applyWithCommit func(
		context.Context,
		mutation.Request,
		func(core.App, mutation.Receipt) error,
	) (mutation.Receipt, error)
}

func (kernel fakeKernel) Preview(
	context.Context, mutation.Request,
) (mutation.PreviewResult, error) {
	return mutation.PreviewResult{}, nil
}

func (kernel fakeKernel) ApplyWithCommit(
	ctx context.Context,
	request mutation.Request,
	commit func(core.App, mutation.Receipt) error,
) (mutation.Receipt, error) {
	return kernel.applyWithCommit(ctx, request, commit)
}

func TestCommitAppliedReconcilesCatalogAliasesAcrossIdentifiers(t *testing.T) {
	app := newMigratedApp(t)
	registerCatalogTable(t, app, "tbl_orders", "table_orders_9f2a", "pbc_orders_31")
	store := importhistory.NewStore()

	// The Host started the projection with the physical collection name while
	// the mutation request carries the logical tableId.
	started := startInput("task-alias")
	started.Collection = "table_orders_9f2a"
	entry, err := store.Start(context.Background(), app, started)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitApplied(
		app, entry.IdempotencyKey, "tbl_orders", appliedReceipt(4, 1),
	); err != nil {
		t.Fatal(err)
	}
	committed, err := store.Get(context.Background(), app, "task-alias")
	if err != nil || committed.State != importhistory.StateSucceeded ||
		committed.CommitState != importhistory.CommitStateCommitted ||
		committed.CreatedCount == nil || *committed.CreatedCount != 4 {
		t.Fatalf("alias-committed entry = %#v, %v", committed, err)
	}

	// A second task cannot double-attribute the same table under a different
	// spelling of the same idempotency key.
	duplicate := startInput("task-alias-2")
	duplicate.IdempotencyKey = entry.IdempotencyKey
	duplicate.Collection = "tbl_orders"
	_, err = store.Start(context.Background(), app, duplicate)
	if codeOf(err) != "import_history.idempotency_conflict" {
		t.Fatalf("alias duplicate attribution = %#v", err)
	}

	// A genuinely different table with the same key stays independent.
	other := startInput("task-alias-3")
	other.IdempotencyKey = entry.IdempotencyKey
	other.Collection = "tbl_other_table"
	if _, err := store.Start(context.Background(), app, other); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitApplied(
		app, entry.IdempotencyKey, "tbl_other_table", appliedReceipt(9, 0),
	); err != nil {
		t.Fatal(err)
	}
	independent, err := store.Get(context.Background(), app, "task-alias-3")
	if err != nil || independent.State != importhistory.StateSucceeded ||
		independent.CreatedCount == nil || *independent.CreatedCount != 9 {
		t.Fatalf("independent table commit = %#v, %v", independent, err)
	}
	first, err := store.Get(context.Background(), app, "task-alias")
	if err != nil || *first.CreatedCount != 4 {
		t.Fatalf("first entry mutated by other table = %#v, %v", first, err)
	}
}

func registerCatalogTable(
	t *testing.T,
	app core.App,
	tableID, physicalName, collectionID string,
) {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("vibetable_tables")
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("table_id", tableID)
	record.Set("collection_id", collectionID)
	record.Set("physical_name", physicalName)
	record.Set("display_name", "目录表")
	record.Set("kind", "base")
	record.Set("schema_revision", 1)
	record.Set("archive_policy", "none")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	// Mirror a physical business collection so the catalog row is consistent.
	physical := core.NewBaseCollection(physicalName)
	physical.Id = collectionID
	if err := app.Save(physical); err != nil {
		t.Fatal(err)
	}
}

func codeOf(err error) string {
	if err == nil {
		return ""
	}
	var productErr *importhistory.Error
	if errors.As(err, &productErr) {
		return productErr.Code
	}
	return err.Error()
}

func appliedReceipt(created, updated int) mutation.Receipt {
	rows := make([]mutation.AffectedRow, 0, created+updated)
	for range created {
		rows = append(rows, mutation.AffectedRow{
			Operation: mutation.OperationInsert,
		})
	}
	for range updated {
		rows = append(rows, mutation.AffectedRow{
			Operation: mutation.OperationUpdate,
		})
	}
	return mutation.Receipt{
		ContractVersion: mutation.ContractVersion,
		Status:          mutation.StatusApplied,
		AffectedRows:    rows,
	}
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", code)
	}
	var productErr *importhistory.Error
	if !errors.As(err, &productErr) || productErr.Code != code {
		t.Fatalf("error = %#v, want code %s", err, code)
	}
}
