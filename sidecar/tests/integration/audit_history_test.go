package integration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
)

func TestAuditHistoryRestoresRelationAndRecalculatesDependentFormula(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	authors := createV2IntegrationTable(
		t, ctx, app, "History authors", "history_authors_table",
	)
	authorName := createV2IntegrationField(
		t, ctx, app, authors.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"), "history_author_name",
	)
	articles := createV2IntegrationTable(
		t, ctx, app, "History articles", "history_articles_table",
	)
	articleTitle := createV2IntegrationField(
		t, ctx, app, articles.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"), "history_article_title",
	)
	author := createV2IntegrationRelation(
		t, ctx, app, articles.TableID, articleTitle.FieldID,
		authors.TableID, authorName.FieldID, "Author", "Articles", "one",
		"history_article_author",
	)
	authorLabelDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Author label")
	authorLabelDraft.Formula = &v2.FormulaDraftSpec{
		Language: "cel-v1", Source: `concat({Author}.{Name}, "")`,
	}
	authorLabel := createV2IntegrationFormula(
		t, ctx, app, articles.TableID, authorLabelDraft, "history_author_label",
	)
	if authorName.Definition == nil || author.Definition == nil || authorLabel.Definition == nil {
		t.Fatal("V2 audit relation fixture omitted field definitions")
	}
	authorNamePhysical := authorName.Definition.Identity.PhysicalName
	authorPhysical := author.Definition.Identity.PhysicalName
	authorLabelPhysical := authorLabel.Definition.Identity.PhysicalName
	catalog := schemaapi.New(app)
	authorRuntime, err := catalog.Describe(ctx, authors.TableID)
	if err != nil {
		t.Fatal(err)
	}
	articleRuntime, err := catalog.Describe(ctx, articles.TableID)
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(
		app, mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(computed.New(
			lookup.NewCalculator(),
			formula.NewCalculator(formula.NewCompiler(formula.DefaultLimits())),
		)),
	)
	apply := func(key, tableID, revision, recordID string, kind mutation.OperationKind, values map[string]any) mutation.Receipt {
		t.Helper()
		receipt, applyErr := kernel.Apply(ctx, mutation.Request{
			ContractVersion: mutation.ContractVersion,
			RequestID:       "relation_history_" + key,
			IdempotencyKey:  "relation_history_" + key,
			TableID:         tableID,
			SchemaRevision:  revision,
			Operations: []mutation.Operation{{
				Kind: kind, RecordID: &recordID, Values: values,
			}},
			Actor: mutation.Actor{Type: "user", ID: "local-user"},
		})
		if applyErr != nil {
			t.Fatalf("apply %s: %#v", key, applyErr)
		}
		return receipt
	}
	apply("author_a", authors.TableID, authorRuntime.Snapshot.SchemaRevision, "historyauthora0", mutation.OperationInsert, map[string]any{authorNamePhysical: "Alpha"})
	apply("author_b", authors.TableID, authorRuntime.Snapshot.SchemaRevision, "historyauthorb0", mutation.OperationInsert, map[string]any{authorNamePhysical: "Beta"})
	articleID := "historyarticle1"
	apply(
		"article_create", articles.TableID, articleRuntime.Snapshot.SchemaRevision, articleID,
		mutation.OperationInsert, map[string]any{authorPhysical: "historyauthora0"},
	)
	updated := apply(
		"article_update", articles.TableID, articleRuntime.Snapshot.SchemaRevision, articleID,
		mutation.OperationUpdate, map[string]any{authorPhysical: "historyauthorb0"},
	)
	if updated.ComputedFields[articleID][authorLabelPhysical] != "Beta" {
		t.Fatalf("relation update computed fields = %#v", updated.ComputedFields)
	}
	service, err := audit.New(app, kernel)

	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ReadChangeSets(ctx, audit.ReadParams{
		TableID: articles.TableID, ItemID: &articleID, Scope: "row", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	unavailableRevision := page.ChangeSets[0].RecordChanges[0].RevisionID
	targetRevision := page.ChangeSets[len(page.ChangeSets)-1].RecordChanges[0].RevisionID
	preview, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: articles.TableID, ItemID: articleID,
		TargetRevision: targetRevision, Scope: "row",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanApply ||
		len(preview.RelationChanges) != 1 ||
		len(preview.Restorable) != 1 ||
		preview.Restorable[0] != authorPhysical ||
		len(preview.Diagnostics) != 1 ||
		preview.Diagnostics[0].Field != authorLabelPhysical {
		t.Fatalf("relation restore preview = %#v", preview)
	}
	result, err := service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: articles.TableID, ItemID: articleID, Token: preview.Token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Item[authorPhysical] != "historyauthora0" ||
		result.Item[authorLabelPhysical] != "Alpha" {
		t.Fatalf("relation restore result = %#v", result.Item)
	}
	apply(
		"author_b_delete", authors.TableID, authorRuntime.Snapshot.SchemaRevision, "historyauthorb0",
		mutation.OperationDelete, nil,
	)
	unavailable, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: articles.TableID, ItemID: articleID,
		TargetRevision: unavailableRevision, Scope: "row",
	})
	if err != nil {
		t.Fatal(err)
	}
	if unavailable.CanApply ||
		len(unavailable.RelationChanges) != 1 ||
		unavailable.RelationChanges[0].TargetAvailable ||
		!hasDiagnostic(
			unavailable.Diagnostics, authorPhysical, "incompatible",
		) {
		t.Fatalf("unavailable relation restore preview = %#v", unavailable)
	}
}

func TestAuditHistoryReadsPreviewsConflictsAndRestoresThroughMutationKernel(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "Audit notes", "audit_notes_table")
	title := createV2IntegrationField(
		t, ctx, app, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"), "audit_notes_title",
	)
	computedDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Computed")
	computedDraft.Formula = &v2.FormulaDraftSpec{
		Language: "cel-v1", Source: "upper({Title})",
	}
	computed := createV2IntegrationFormula(
		t, ctx, app, table.TableID, computedDraft, "audit_notes_computed",
	)
	if title.Definition == nil || computed.Definition == nil {
		t.Fatal("V2 audit notes fixture omitted field definitions")
	}
	definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	titlePhysical := title.Definition.Identity.PhysicalName
	computedPhysical := computed.Definition.Identity.PhysicalName
	now := time.Date(2026, 7, 24, 8, 30, 0, 0, time.UTC)
	var sequence atomic.Int64
	kernel := mutation.New(
		app, mutation.MetadataSchemaSource{},
		mutation.WithClock(func() time.Time { return now }),
		mutation.WithFormulaCalculator(formula.NewCalculator(
			formula.NewCompiler(formula.DefaultLimits()),
		)),
		mutation.WithIDGenerator(func(kind string) string {
			next := sequence.Add(1)
			switch kind {
			case "record":
				return "auditrecord0001"
			case "changeSet":
				return fmt.Sprintf("change_%06d", next)
			case "event":
				return fmt.Sprintf("event_%06d", next)
			default:
				return fmt.Sprintf("%s_%06d", kind, next)
			}
		}),
	)
	apply := func(key, value string) mutation.Receipt {
		t.Helper()
		kind := mutation.OperationUpdate
		recordID := "auditrecord0001"
		if key == "insert" {
			kind = mutation.OperationInsert
		}
		receipt, applyErr := kernel.Apply(ctx, mutation.Request{
			ContractVersion: mutation.ContractVersion,
			RequestID:       "request_" + key, IdempotencyKey: "idem_" + key,
			TableID:        definition.Snapshot.TableID,
			SchemaRevision: definition.Snapshot.SchemaRevision,
			Operations: []mutation.Operation{{
				Kind: kind, RecordID: &recordID,
				Values: map[string]any{titlePhysical: value},
			}},
			Actor: mutation.Actor{Type: "user", ID: "local-user"},
		})
		if applyErr != nil {
			t.Fatalf("apply %s: %#v", key, applyErr)
		}
		return receipt
	}
	apply("insert", "first")
	apply("second", "second")

	service, err := audit.New(
		app, kernel,

		audit.WithClock(func() time.Time { return now }))

	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ReadChangeSets(ctx, audit.ReadParams{
		TableID: "missing_table", Scope: "table", Limit: 50,
	})
	var historyErr *audit.Error
	if !errors.As(err, &historyErr) ||
		historyErr.Code != "history.table_not_found" {
		t.Fatalf("missing table history error = %#v", err)
	}
	itemID := "auditrecord0001"
	page, err := service.ReadChangeSets(ctx, audit.ReadParams{
		TableID: definition.Snapshot.TableID, ItemID: &itemID,
		Scope: "row", Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.ChangeSets) != 2 ||
		page.ChangeSets[0].Action != "update" ||
		page.ChangeSets[1].Action != "create" ||
		page.ChangeSets[0].Actor == nil ||
		page.ChangeSets[0].Actor.UserID == nil ||
		*page.ChangeSets[0].Actor.UserID != "local-user" {
		t.Fatalf("unexpected history page %#v", page)
	}
	fieldName := title.FieldID
	cellPage, err := service.ReadChangeSets(ctx, audit.ReadParams{
		TableID: definition.Snapshot.TableID, ItemID: &itemID, Field: &fieldName,
		Scope: "cell", Limit: 50,
	})
	if err != nil || cellPage.Total != 2 ||
		len(cellPage.ChangeSets[0].ScalarChanges) != 1 ||
		cellPage.ChangeSets[0].ScalarChanges[0].Field != titlePhysical {
		t.Fatalf("cell history = %#v err=%v", cellPage, err)
	}
	targetRevision := page.ChangeSets[1].RecordChanges[0].RevisionID
	expiredPreview, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.Snapshot.TableID, ItemID: itemID,
		TargetRevision: targetRevision, Scope: "row",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	_, err = service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.Snapshot.TableID, ItemID: itemID, Token: expiredPreview.Token,
	})
	if !errors.As(err, &historyErr) || historyErr.Code != "restore_token_expired" {
		t.Fatalf("expired restore token error = %#v", err)
	}
	now = now.Add(-6 * time.Minute)

	preview, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.Snapshot.TableID, ItemID: itemID,
		TargetRevision: targetRevision, Scope: "row",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanApply ||
		len(preview.Restorable) != 1 ||
		preview.Restorable[0] != titlePhysical ||
		len(preview.Diagnostics) != 1 ||
		!hasDiagnostic(preview.Diagnostics, computedPhysical, "derived") {
		t.Fatalf("unexpected restore preview %#v", preview)
	}
	_, err = service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.Snapshot.TableID, ItemID: itemID, Token: preview.Token + "tampered",
	})
	if !errors.As(err, &historyErr) || historyErr.Code != "restore_token_unknown" {
		t.Fatalf("tampered restore token error = %#v", err)
	}

	apply("third", "third")
	_, err = service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.Snapshot.TableID, ItemID: itemID, Token: preview.Token,
	})
	if !errors.As(err, &historyErr) || historyErr.Code != "restore_conflict" {
		t.Fatalf("stale restore error = %#v", err)
	}

	preview, err = service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.Snapshot.TableID, ItemID: itemID,
		TargetRevision: targetRevision, Scope: "row",
	})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan audit.RestoreResult, 2)
	applyErrors := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, applyErr := service.ApplyRestore(ctx, audit.ApplyParams{
				TableID: definition.Snapshot.TableID, ItemID: itemID, Token: preview.Token,
			})
			if applyErr != nil {
				applyErrors <- applyErr
				return
			}
			results <- value
		}()
	}
	wait.Wait()
	close(results)
	close(applyErrors)
	var result audit.RestoreResult
	successes := 0
	for value := range results {
		result = value
		successes++
	}
	unknowns := 0
	for applyErr := range applyErrors {
		if errors.As(applyErr, &historyErr) &&
			historyErr.Code == "restore_token_unknown" {
			unknowns++
		} else {
			t.Fatalf("concurrent restore error: %#v", applyErr)
		}
	}
	if successes != 1 || unknowns != 1 {
		t.Fatalf("concurrent token claims: successes=%d unknowns=%d", successes, unknowns)
	}
	if result.NewRevisionID == nil ||
		result.Item[titlePhysical] != "first" ||
		result.Item[computedPhysical] != "FIRST" ||
		result.Receipt.Status != mutation.StatusApplied {
		t.Fatalf("restore result %#v", result)
	}
	_, err = service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.Snapshot.TableID, ItemID: itemID, Token: preview.Token,
	})
	if !errors.As(err, &historyErr) || historyErr.Code != "restore_token_unknown" {
		t.Fatalf("replayed restore token error = %#v", err)
	}

	recordA, recordB := "auditrecord0002", "auditrecord0003"
	_, err = kernel.Apply(ctx, mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       "request_batch", IdempotencyKey: "idem_batch",
		TableID:        definition.Snapshot.TableID,
		SchemaRevision: definition.Snapshot.SchemaRevision,
		Operations: []mutation.Operation{
			{Kind: mutation.OperationInsert, RecordID: &recordA, Values: map[string]any{titlePhysical: "a"}},
			{Kind: mutation.OperationInsert, RecordID: &recordB, Values: map[string]any{titlePhysical: "b"}},
		},
		Actor: mutation.Actor{Type: "import", ID: "batch-user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tablePage, err := service.ReadChangeSets(ctx, audit.ReadParams{
		TableID: definition.Snapshot.TableID, Scope: "table", Limit: 50,
	})
	if err != nil || len(tablePage.ChangeSets) == 0 ||
		tablePage.ChangeSets[0].AffectedRows != 2 ||
		len(tablePage.ChangeSets[0].RecordChanges) != 2 ||
		tablePage.ChangeSets[0].Action != "create" ||
		tablePage.ChangeSets[0].RootRevisionID !=
			tablePage.ChangeSets[0].RecordChanges[0].RevisionID ||
		len(tablePage.ChangeSets[0].RevisionIDs) != 2 ||
		tablePage.ChangeSets[0].RevisionIDs[1] !=
			tablePage.ChangeSets[0].RecordChanges[1].RevisionID ||
		tablePage.ChangeSets[0].Actor == nil ||
		tablePage.ChangeSets[0].Actor.UserID == nil ||
		*tablePage.ChangeSets[0].Actor.UserID != "batch-user" {
		t.Fatalf("batch history = %#v err=%v", tablePage, err)
	}
}

func TestAuditHistoryArchivedRestoreUsesHistoricalSnapshotAndRestoreAction(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(
		t, ctx, app, "History archived", "history_archived_table",
	)
	title := createV2IntegrationField(
		t, ctx, app, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"), "history_archived_title",
	)
	maxSelected := 1
	statusDraft := fieldDraftForIntegration(t, v2.LogicalSelect, "Status")
	statusDraft.Constraints.Selection.Min = 1
	statusDraft.Constraints.Selection.Max = &maxSelected
	statusDraft.Select = &v2.SelectSpec{Options: []v2.SelectOption{
		{OptionID: "opt_history_active", Label: "Active", State: v2.OptionActive},
		{OptionID: "opt_history_archived", Label: "Archived", State: v2.OptionActive},
	}}
	status := createV2IntegrationField(
		t, ctx, app, table.TableID, statusDraft, "history_archived_status",
	)
	computedDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Computed")
	computedDraft.Formula = &v2.FormulaDraftSpec{
		Language: "cel-v1", Source: "upper({Title})",
	}
	computed := createV2IntegrationFormula(
		t, ctx, app, table.TableID, computedDraft, "history_archived_computed",
	)
	if title.Definition == nil || status.Definition == nil || computed.Definition == nil {
		t.Fatal("V2 archived history fixture omitted field definitions")
	}
	lifecycle, err := schemacore.NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	statusFieldID := status.FieldID
	settings, err := lifecycle.Configure(ctx, v2.TableSettingsIntent{
		TableID: table.TableID, ExpectedSchemaRev: computed.SchemaRevision,
		ArchivePolicy: v2.ArchivePolicy{
			Mode: "status", FieldID: &statusFieldID, ArchivedValue: "opt_history_archived",
		},
		OperationID: "history_archived_policy",
		Actor:       v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Snapshot.SchemaRevision != settings.SchemaRevision {
		t.Fatalf(
			"described revision = %q, settings = %q",
			definition.Snapshot.SchemaRevision,
			settings.SchemaRevision,
		)
	}
	titlePhysical := title.Definition.Identity.PhysicalName
	statusPhysical := status.Definition.Identity.PhysicalName
	computedPhysical := computed.Definition.Identity.PhysicalName
	var sequence atomic.Int64
	kernel := mutation.New(
		app, mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(formula.NewCalculator(
			formula.NewCompiler(formula.DefaultLimits()),
		)),
		mutation.WithIDGenerator(func(kind string) string {
			return fmt.Sprintf("%s_%06d", kind, sequence.Add(1))
		}),
	)
	recordID := "auditrecord0101"
	apply := func(key string, operation mutation.Operation) {
		t.Helper()
		_, applyErr := kernel.Apply(ctx, mutationRequest(
			definition.Snapshot.TableID, definition.Snapshot.SchemaRevision, key, operation,
		))
		if applyErr != nil {
			t.Fatalf("%s: %#v", key, applyErr)
		}
	}
	apply("archived-insert", mutation.Operation{
		Kind: mutation.OperationInsert, RecordID: &recordID,
		Values: map[string]any{
			titlePhysical: "first", statusPhysical: "opt_history_active",
		},
	})
	apply("archived-update", mutation.Operation{
		Kind: mutation.OperationUpdate, RecordID: &recordID,
		Values: map[string]any{titlePhysical: "historical"},
	})
	target, err := app.FindFirstRecordByFilter(
		"vibetable_audit_events", "request_id='req-archived-update'",
	)
	if err != nil {
		t.Fatal(err)
	}
	apply("archived-newer", mutation.Operation{
		Kind: mutation.OperationUpdate, RecordID: &recordID,
		Values: map[string]any{titlePhysical: "newer"},
	})
	apply("archived-archive", mutation.Operation{
		Kind: mutation.OperationArchive, RecordID: &recordID,
	})

	service, err := audit.New(app, kernel)

	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.Snapshot.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "archived",
	})
	if err != nil {
		t.Fatalf("preview archived restore: %#v", err)
	}
	if !preview.CanApply || !containsString(preview.Restorable, titlePhysical) ||
		!containsString(preview.Restorable, statusPhysical) {
		t.Fatalf("archived preview = %#v", preview)
	}
	result, err := service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.Snapshot.TableID, ItemID: recordID, Token: preview.Token,
	})
	if err != nil {
		t.Fatalf("apply archived restore: %#v", err)
	}
	if result.Item[titlePhysical] != "historical" ||
		result.Item[statusPhysical] != "opt_history_active" ||
		result.Item[computedPhysical] != "HISTORICAL" {
		t.Fatalf("archived restore result = %#v", result)
	}
	itemID := recordID
	page, err := service.ReadChangeSets(ctx, audit.ReadParams{
		TableID: definition.Snapshot.TableID, ItemID: &itemID, Scope: "row", Limit: 50,
	})
	if err != nil || len(page.ChangeSets) == 0 || page.ChangeSets[0].Action != "restore" {
		t.Fatalf("restore history = %#v err=%v", page, err)
	}

	_, err = service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.Snapshot.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "archived",
	})
	var historyErr *audit.Error
	if !errors.As(err, &historyErr) || historyErr.Code != "restore_scope_mismatch" {
		t.Fatalf("active archived preview error = %#v", err)
	}
}

func TestAuditHistoryRestoreReportsSchemaDriftBeforeDigestConflict(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "History drift", "history_drift_table")
	title := createV2IntegrationField(
		t, ctx, app, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"), "history_drift_title",
	)
	if title.Definition == nil {
		t.Fatal("V2 history drift fixture omitted title definition")
	}
	definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	titlePhysical := title.Definition.Identity.PhysicalName
	var sequence atomic.Int64
	kernel := mutation.New(
		app, mutation.MetadataSchemaSource{},
		mutation.WithIDGenerator(func(kind string) string {
			return fmt.Sprintf("%s_%06d", kind, sequence.Add(1))
		}),
	)
	recordID := "auditrecord0102"
	_, err = kernel.Apply(ctx, mutationRequest(
		definition.Snapshot.TableID, definition.Snapshot.SchemaRevision, "drift-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{titlePhysical: "first"},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	target, err := app.FindFirstRecordByFilter(
		"vibetable_audit_events", "request_id='req-drift-insert'",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = kernel.Apply(ctx, mutationRequest(
		definition.Snapshot.TableID, definition.Snapshot.SchemaRevision, "drift-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{titlePhysical: "second"},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	service, err := audit.New(app, kernel)

	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.Snapshot.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "row",
	})
	if err != nil {
		t.Fatal(err)
	}
	createV2IntegrationField(
		t, ctx, app, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Note"), "history_drift_note",
	)
	_, err = service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.Snapshot.TableID, ItemID: recordID, Token: preview.Token,
	})
	var historyErr *audit.Error
	if !errors.As(err, &historyErr) || historyErr.Code != "schema_drift" {
		t.Fatalf("schema drift error = %#v", err)
	}
}

func TestAuditHistoryRestoresHardDeletedRecordAndRecalculatesFormula(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "History deleted", "history_deleted_table")
	title := createV2IntegrationField(
		t, ctx, app, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"), "history_deleted_title",
	)
	computedDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Computed")
	computedDraft.Formula = &v2.FormulaDraftSpec{
		Language: "cel-v1", Source: "upper({Title})",
	}
	computed := createV2IntegrationFormula(
		t, ctx, app, table.TableID, computedDraft, "history_deleted_computed",
	)
	if title.Definition == nil || computed.Definition == nil {
		t.Fatal("V2 hard delete fixture omitted field definitions")
	}
	definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	titlePhysical := title.Definition.Identity.PhysicalName
	computedPhysical := computed.Definition.Identity.PhysicalName
	var sequence atomic.Int64
	kernel := mutation.New(
		app, mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(formula.NewCalculator(
			formula.NewCompiler(formula.DefaultLimits()),
		)),
		mutation.WithIDGenerator(func(kind string) string {
			return fmt.Sprintf("%s_%06d", kind, sequence.Add(1))
		}),
	)
	recordID := "auditrecord0103"
	for _, step := range []struct {
		key       string
		operation mutation.Operation
	}{
		{"deleted-insert", mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{titlePhysical: "recover me"},
		}},
		{"deleted-delete", mutation.Operation{
			Kind: mutation.OperationDelete, RecordID: &recordID,
		}},
	} {
		if _, err := kernel.Apply(ctx, mutationRequest(
			definition.Snapshot.TableID,
			definition.Snapshot.SchemaRevision,
			step.key,
			step.operation,
		)); err != nil {
			t.Fatalf("%s: %#v", step.key, err)
		}
	}
	target, err := app.FindFirstRecordByFilter(
		"vibetable_audit_events", "request_id='req-deleted-delete'",
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := audit.New(app, kernel)

	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.Snapshot.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "row",
	})
	if err != nil || !preview.CanApply {
		t.Fatalf("deleted preview = %#v err=%v", preview, err)
	}
	result, err := service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.Snapshot.TableID, ItemID: recordID, Token: preview.Token,
	})
	if err != nil {
		t.Fatalf("restore deleted record: %#v", err)
	}
	if result.Item[titlePhysical] != "recover me" ||
		result.Item[computedPhysical] != "RECOVER ME" ||
		result.NewRevisionID == nil ||
		(result.Receipt.ChangeSetID != nil && *result.NewRevisionID == *result.Receipt.ChangeSetID) {
		t.Fatalf("deleted restore result = %#v", result)
	}
}

func TestAuditHistoryRejectsOversizedRestoreState(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "History large", "history_large_table")
	payload := createV2IntegrationField(
		t, ctx, app, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalJSON, "Payload"), "history_large_payload",
	)
	if payload.Definition == nil {
		t.Fatal("V2 large history fixture omitted payload definition")
	}
	definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	payloadPhysical := payload.Definition.Identity.PhysicalName
	var sequence atomic.Int64
	kernel := mutation.New(
		app, mutation.MetadataSchemaSource{},
		mutation.WithIDGenerator(func(kind string) string {
			return fmt.Sprintf("%s_%06d", kind, sequence.Add(1))
		}),
	)
	recordID := "auditrecord0104"
	_, err = kernel.Apply(ctx, mutationRequest(
		definition.Snapshot.TableID, definition.Snapshot.SchemaRevision, "large-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{payloadPhysical: map[string]any{
				"value": strings.Repeat("x", 300<<10),
			}},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	target, err := app.FindFirstRecordByFilter(
		"vibetable_audit_events", "request_id='req-large-insert'",
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := audit.New(app, kernel)

	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.Snapshot.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "row",
	})
	if err != nil || unchanged.CanApply {
		t.Fatalf("unchanged JSON preview = %#v err=%v", unchanged, err)
	}
	_, err = kernel.Apply(ctx, mutationRequest(
		definition.Snapshot.TableID, definition.Snapshot.SchemaRevision, "large-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{payloadPhysical: map[string]any{"value": "small"}},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.Snapshot.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "row",
	})
	var historyErr *audit.Error
	if !errors.As(err, &historyErr) || historyErr.Code != "restore.resource_limit" {
		t.Fatalf("oversized restore error = %#v", err)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func hasDiagnostic(
	values []audit.Diagnostic,
	field string,
	classification string,
) bool {
	for _, value := range values {
		if value.Field == field && value.Classification == classification {
			return true
		}
	}
	return false
}
