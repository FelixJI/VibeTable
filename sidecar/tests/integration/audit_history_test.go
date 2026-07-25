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
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestAuditHistoryRestoresRelationAndRecalculatesDependentFormula(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	catalog := schemaapi.New(app)
	authors, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("history_authors", "history_authors", []schema.FieldDefinition{
			field("author_name_id", "name", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	author := field("author_id", "author", schema.FieldKindRelation, schema.DataTypeRelation)
	author.Relation = &schema.RelationSpec{
		TargetTableID: authors.TableID, Cardinality: "one", DeletePolicy: "setNull",
	}
	author.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintRelation, TargetTableID: authors.TableID,
		Cardinality: "one", DeletePolicy: "setNull",
	}}
	authorLabel := field(
		"author_label_id", "author_label",
		schema.FieldKindFormula, schema.DataTypeFormula,
	)
	authorLabel.ReadOnly = true
	authorLabel.StorageType = schema.StorageText
	authorLabel.Formula = &schema.FormulaSpec{
		Language: "cel-v1", Source: "author.name",
		ResultType: schema.DataTypeShortText, Version: 1, Status: "ready",
	}
	articles, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"history_articles", "history_articles",
			[]schema.FieldDefinition{author, authorLabel},
		),
		ExpectedRevision: 0,
	})
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
	apply("author_a", authors.TableID, authors.SchemaRevision, "historyauthora0", mutation.OperationInsert, map[string]any{"name": "Alpha"})
	apply("author_b", authors.TableID, authors.SchemaRevision, "historyauthorb0", mutation.OperationInsert, map[string]any{"name": "Beta"})
	articleID := "historyarticle1"
	apply(
		"article_create", articles.TableID, articles.SchemaRevision, articleID,
		mutation.OperationInsert, map[string]any{"author": "historyauthora0"},
	)
	apply(
		"article_update", articles.TableID, articles.SchemaRevision, articleID,
		mutation.OperationUpdate, map[string]any{"author": "historyauthorb0"},
	)
	service, err := audit.New(
		app, kernel, mutation.MetadataSchemaSource{},
		[]byte(strings.Repeat("r", 32)),
	)
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
		preview.Restorable[0] != "author" ||
		len(preview.Diagnostics) != 1 ||
		preview.Diagnostics[0].Field != "author_label" {
		t.Fatalf("relation restore preview = %#v", preview)
	}
	result, err := service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: articles.TableID, ItemID: articleID, Token: preview.Token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Item["author"] != "historyauthora0" ||
		result.Item["author_label"] != "Alpha" {
		t.Fatalf("relation restore result = %#v", result.Item)
	}
	apply(
		"author_b_delete", authors.TableID, authors.SchemaRevision, "historyauthorb0",
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
			unavailable.Diagnostics, "author", "incompatible",
		) {
		t.Fatalf("unavailable relation restore preview = %#v", unavailable)
	}
}

func TestAuditHistoryReadsPreviewsConflictsAndRestoresThroughMutationKernel(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	title := field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText)
	secret := field("secret_id", "secret", schema.FieldKindScalar, schema.DataTypeSecret)
	computed := field(
		"computed_id", "computed", schema.FieldKindFormula, schema.DataTypeFormula,
	)
	computed.ReadOnly = true
	computed.Formula = &schema.FormulaSpec{
		Language: "cel-v1", Source: "upper(title)",
		ResultType: schema.DataTypeShortText, Version: 1, Status: "ready",
	}
	computed.StorageType = schema.StorageText
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition:       baseTable("audit_notes", "audit_notes", []schema.FieldDefinition{title, secret, computed}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
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
			TableID: definition.TableID, SchemaRevision: definition.SchemaRevision,
			Operations: []mutation.Operation{{
				Kind: kind, RecordID: &recordID,
				Values: map[string]any{"title": value, "secret": "private-" + value},
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
		app, kernel, mutation.MetadataSchemaSource{},
		[]byte(strings.Repeat("a", 32)),
		audit.WithClock(func() time.Time { return now }),
	)
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
		TableID: definition.TableID, ItemID: &itemID,
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
	for _, change := range page.ChangeSets[0].ScalarChanges {
		if change.Field == "secret" {
			t.Fatalf("sensitive field leaked through history: %#v", page.ChangeSets[0])
		}
	}
	fieldName := "title_id"
	cellPage, err := service.ReadChangeSets(ctx, audit.ReadParams{
		TableID: definition.TableID, ItemID: &itemID, Field: &fieldName,
		Scope: "cell", Limit: 50,
	})
	if err != nil || cellPage.Total != 2 ||
		len(cellPage.ChangeSets[0].ScalarChanges) != 1 ||
		cellPage.ChangeSets[0].ScalarChanges[0].Field != "title" {
		t.Fatalf("cell history = %#v err=%v", cellPage, err)
	}
	secretField := "secret_id"
	_, err = service.ReadChangeSets(ctx, audit.ReadParams{
		TableID: definition.TableID, ItemID: &itemID, Field: &secretField,
		Scope: "cell", Limit: 50,
	})
	if !errors.As(err, &historyErr) ||
		historyErr.Code != "history.field_unreadable" {
		t.Fatalf("sensitive cell history error = %#v", err)
	}
	targetRevision := page.ChangeSets[1].RecordChanges[0].RevisionID
	expiredPreview, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: itemID,
		TargetRevision: targetRevision, Scope: "row",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	_, err = service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.TableID, ItemID: itemID, Token: expiredPreview.Token,
	})
	if !errors.As(err, &historyErr) || historyErr.Code != "restore_token_expired" {
		t.Fatalf("expired restore token error = %#v", err)
	}
	now = now.Add(-6 * time.Minute)

	preview, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: itemID,
		TargetRevision: targetRevision, Scope: "row",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanApply ||
		len(preview.Restorable) != 1 ||
		preview.Restorable[0] != "title" ||
		len(preview.Diagnostics) != 2 ||
		!hasDiagnostic(preview.Diagnostics, "secret", "sensitive") ||
		!hasDiagnostic(preview.Diagnostics, "computed", "derived") {
		t.Fatalf("unexpected restore preview %#v", preview)
	}
	_, err = service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.TableID, ItemID: itemID, Token: preview.Token + "tampered",
	})
	if !errors.As(err, &historyErr) || historyErr.Code != "restore_token_unknown" {
		t.Fatalf("tampered restore token error = %#v", err)
	}

	apply("third", "third")
	_, err = service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.TableID, ItemID: itemID, Token: preview.Token,
	})
	if !errors.As(err, &historyErr) || historyErr.Code != "restore_conflict" {
		t.Fatalf("stale restore error = %#v", err)
	}

	preview, err = service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: itemID,
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
				TableID: definition.TableID, ItemID: itemID, Token: preview.Token,
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
		result.Item["title"] != "first" ||
		result.Item["computed"] != "FIRST" ||
		result.Receipt.Status != mutation.StatusApplied {
		t.Fatalf("restore result %#v", result)
	}
	_, err = service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.TableID, ItemID: itemID, Token: preview.Token,
	})
	if !errors.As(err, &historyErr) || historyErr.Code != "restore_token_unknown" {
		t.Fatalf("replayed restore token error = %#v", err)
	}

	recordA, recordB := "auditrecord0002", "auditrecord0003"
	_, err = kernel.Apply(ctx, mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       "request_batch", IdempotencyKey: "idem_batch",
		TableID: definition.TableID, SchemaRevision: definition.SchemaRevision,
		Operations: []mutation.Operation{
			{Kind: mutation.OperationInsert, RecordID: &recordA, Values: map[string]any{"title": "a"}},
			{Kind: mutation.OperationInsert, RecordID: &recordB, Values: map[string]any{"title": "b"}},
		},
		Actor: mutation.Actor{Type: "import", ID: "batch-user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tablePage, err := service.ReadChangeSets(ctx, audit.ReadParams{
		TableID: definition.TableID, Scope: "table", Limit: 50,
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
	maxSelected := 1
	status := field("status_id", "status", schema.FieldKindScalar, schema.DataTypeSelect)
	status.Nullable = false
	status.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintEnum, Multiple: false, MinSelected: 1,
		MaxSelected: &maxSelected,
		Options: []schema.SelectOption{
			{Value: "active", DisplayName: "Active"},
			{Value: "archived", DisplayName: "Archived"},
		},
	}}
	computed := formulaField(
		"computed_id", "computed", schema.DataTypeShortText, "upper(title)",
	)
	definition := baseTable("history_archived", "history_archived", []schema.FieldDefinition{
		field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText),
		status,
		computed,
	})
	definition.ArchivePolicy = schema.ArchivePolicy{
		Mode: schema.ArchiveModeStatus, FieldID: stringAddress("status_id"),
		ArchivedValue: "archived",
	}
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: definition, ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
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
			definition.TableID, definition.SchemaRevision, key, operation,
		))
		if applyErr != nil {
			t.Fatalf("%s: %#v", key, applyErr)
		}
	}
	apply("archived-insert", mutation.Operation{
		Kind: mutation.OperationInsert, RecordID: &recordID,
		Values: map[string]any{"title": "first", "status": "active"},
	})
	apply("archived-update", mutation.Operation{
		Kind: mutation.OperationUpdate, RecordID: &recordID,
		Values: map[string]any{"title": "historical"},
	})
	target, err := app.FindFirstRecordByFilter(
		"vibetable_audit_events", "request_id='req-archived-update'",
	)
	if err != nil {
		t.Fatal(err)
	}
	apply("archived-newer", mutation.Operation{
		Kind: mutation.OperationUpdate, RecordID: &recordID,
		Values: map[string]any{"title": "newer"},
	})
	apply("archived-archive", mutation.Operation{
		Kind: mutation.OperationArchive, RecordID: &recordID,
	})

	service, err := audit.New(
		app, kernel, mutation.MetadataSchemaSource{},
		[]byte(strings.Repeat("b", 32)),
	)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "archived",
	})
	if err != nil {
		t.Fatalf("preview archived restore: %#v", err)
	}
	if !preview.CanApply || !containsString(preview.Restorable, "title") ||
		!containsString(preview.Restorable, "status") {
		t.Fatalf("archived preview = %#v", preview)
	}
	result, err := service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.TableID, ItemID: recordID, Token: preview.Token,
	})
	if err != nil {
		t.Fatalf("apply archived restore: %#v", err)
	}
	if result.Item["title"] != "historical" ||
		result.Item["status"] != "active" ||
		result.Item["computed"] != "HISTORICAL" {
		t.Fatalf("archived restore result = %#v", result)
	}
	itemID := recordID
	page, err := service.ReadChangeSets(ctx, audit.ReadParams{
		TableID: definition.TableID, ItemID: &itemID, Scope: "row", Limit: 50,
	})
	if err != nil || len(page.ChangeSets) == 0 || page.ChangeSets[0].Action != "restore" {
		t.Fatalf("restore history = %#v err=%v", page, err)
	}

	_, err = service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: recordID,
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
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("history_drift", "history_drift", []schema.FieldDefinition{
			field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	var sequence atomic.Int64
	kernel := mutation.New(
		app, mutation.MetadataSchemaSource{},
		mutation.WithIDGenerator(func(kind string) string {
			return fmt.Sprintf("%s_%06d", kind, sequence.Add(1))
		}),
	)
	recordID := "auditrecord0102"
	_, err = kernel.Apply(ctx, mutationRequest(
		definition.TableID, definition.SchemaRevision, "drift-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{"title": "first"},
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
		definition.TableID, definition.SchemaRevision, "drift-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{"title": "second"},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	service, err := audit.New(
		app, kernel, mutation.MetadataSchemaSource{},
		[]byte(strings.Repeat("c", 32)),
	)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "row",
	})
	if err != nil {
		t.Fatal(err)
	}
	definition.Fields = append(definition.Fields,
		field("note_id", "note", schema.FieldKindScalar, schema.DataTypeShortText),
	)
	if _, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: definition, ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.TableID, ItemID: recordID, Token: preview.Token,
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
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("history_deleted", "history_deleted", []schema.FieldDefinition{
			field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText),
			formulaField("computed_id", "computed", schema.DataTypeShortText, "upper(title)"),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
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
			Values: map[string]any{"title": "recover me"},
		}},
		{"deleted-delete", mutation.Operation{
			Kind: mutation.OperationDelete, RecordID: &recordID,
		}},
	} {
		if _, err := kernel.Apply(ctx, mutationRequest(
			definition.TableID, definition.SchemaRevision, step.key, step.operation,
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
	service, err := audit.New(
		app, kernel, mutation.MetadataSchemaSource{},
		[]byte(strings.Repeat("d", 32)),
	)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "row",
	})
	if err != nil || !preview.CanApply {
		t.Fatalf("deleted preview = %#v err=%v", preview, err)
	}
	result, err := service.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.TableID, ItemID: recordID, Token: preview.Token,
	})
	if err != nil {
		t.Fatalf("restore deleted record: %#v", err)
	}
	if result.Item["title"] != "recover me" ||
		result.Item["computed"] != "RECOVER ME" ||
		result.NewRevisionID == nil ||
		(result.Receipt.ChangeSetID != nil && *result.NewRevisionID == *result.Receipt.ChangeSetID) {
		t.Fatalf("deleted restore result = %#v", result)
	}
}

func TestAuditHistoryRejectsOversizedRestoreState(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("history_large", "history_large", []schema.FieldDefinition{
			field("payload_id", "payload", schema.FieldKindScalar, schema.DataTypeJSON),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	var sequence atomic.Int64
	kernel := mutation.New(
		app, mutation.MetadataSchemaSource{},
		mutation.WithIDGenerator(func(kind string) string {
			return fmt.Sprintf("%s_%06d", kind, sequence.Add(1))
		}),
	)
	recordID := "auditrecord0104"
	_, err = kernel.Apply(ctx, mutationRequest(
		definition.TableID, definition.SchemaRevision, "large-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{"payload": map[string]any{
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
	service, err := audit.New(
		app, kernel, mutation.MetadataSchemaSource{},
		[]byte(strings.Repeat("e", 32)),
	)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "row",
	})
	if err != nil || unchanged.CanApply {
		t.Fatalf("unchanged JSON preview = %#v err=%v", unchanged, err)
	}
	_, err = kernel.Apply(ctx, mutationRequest(
		definition.TableID, definition.SchemaRevision, "large-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{"payload": map[string]any{"value": "small"}},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: recordID,
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
