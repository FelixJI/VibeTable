package integration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestFormulaAuthorCatalogMissingReferenceCannotBindDisplayName(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "Missing refs", "author_missing_table")
	field := createV2IntegrationField(t, ctx, app, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalNumber, "#REF!"), "author_ref_label")
	catalog := fieldchange.NewCatalog(app)
	restored, err := catalog.RestoreFormulaDocument(ctx, table.TableID, "f_missing_author", 1)
	var diagnostic *formula.Error
	if !errors.As(err, &diagnostic) || diagnostic.Code != "formula.reference" || restored == nil {
		t.Fatalf("missing canonical diagnostic = %v", err)
	}
	_, err = catalog.AuthorFormulaDocument(ctx, table.TableID, restored.Document)
	if !errors.As(err, &diagnostic) || diagnostic.Code != "formula.reference" {
		t.Fatalf("missing marker rebound to a real field: %v", err)
	}
	actual, err := catalog.AuthorFormulaDocument(ctx, table.TableID, workbench.FormulaAuthorDocument{
		DisplaySource: "{#REF!}", DocumentRevision: 2,
	})
	if err != nil || len(actual.Document.Tokens) != 1 || actual.Document.Tokens[0].FieldId != field.FieldID {
		t.Fatalf("explicit real field reference rejected: %v", err)
	}
}

func TestFormulaAuthorCatalogRoundTripAfterTargetRename(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	target := createV2IntegrationTable(t, ctx, app, "明细", "author_target_table")
	amount := createV2IntegrationField(t, ctx, app, target.TableID,
		fieldDraftForIntegration(t, v2.LogicalNumber, "金额"), "author_amount")
	source := createV2IntegrationTable(t, ctx, app, "订单", "author_source_table")
	name := createV2IntegrationField(t, ctx, app, source.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "名称"), "author_name")
	relation := createV2IntegrationRelation(t, ctx, app, source.TableID, name.FieldID,
		target.TableID, amount.FieldID, "明细", "订单", "many", "author_relation")
	catalog := fieldchange.NewCatalog(app)
	document := workbench.FormulaAuthorDocument{
		DisplaySource: "SUM({明细}.{金额})", DocumentRevision: 7,
	}
	authored, err := catalog.AuthorFormulaDocument(ctx, source.TableID, document)
	if err != nil {
		t.Fatal(err)
	}
	if len(authored.Document.Tokens) != 1 {
		t.Fatalf("author tokens = %#v", authored.Document.Tokens)
	}
	token := authored.Document.Tokens[0]
	if token.Kind != "relationTarget" || token.FieldId != amount.FieldID ||
		token.RelationFieldId == nil || *token.RelationFieldId != relation.FieldID ||
		token.TargetFieldId == nil || *token.TargetFieldId != amount.FieldID {
		t.Fatalf("author token identities = %#v", token)
	}
	count, err := catalog.AuthorFormulaDocument(ctx, source.TableID, workbench.FormulaAuthorDocument{
		DisplaySource: "COUNT({明细})", DocumentRevision: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(count.Document.Tokens) != 1 || count.Document.Tokens[0].Kind != "relation" ||
		count.Document.Tokens[0].RelationFieldId == nil ||
		*count.Document.Tokens[0].RelationFieldId != relation.FieldID {
		t.Fatalf("relation token differs from workbench contract fixture: %#v", count.Document)
	}
	if _, err := catalog.AuthorFormulaDocument(ctx, source.TableID, count.Document); err != nil {
		t.Fatalf("bound relation token rejected: %v", err)
	}
	draft := fieldDraftForIntegration(t, v2.LogicalFormula, "合计")
	draft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: authored.CanonicalSource}
	computed := createV2IntegrationFormula(t, ctx, app, source.TableID, draft, "author_formula")
	if computed.Definition == nil || computed.Definition.Formula.Source != authored.CanonicalSource {
		t.Fatalf("persisted formula = %#v", computed.Definition)
	}

	// Rename through the normal schema planner/executor, not a metadata shortcut.
	rename := fieldDraftForIntegration(t, v2.LogicalNumber, "金}额😀")
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	revisions, err := catalog.Revisions(ctx, target.TableID)
	if err != nil {
		t.Fatal(err)
	}
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: target.TableID, FieldID: amount.FieldID,
		ExpectedSchemaRev: revisions.Schema, Draft: &rename, Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply {
		t.Fatalf("rename blocked: %#v", plan.Errors)
	}
	if _, err := fieldchange.NewExecutor(app, store).Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "author_rename", Actor: actor,
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := catalog.Field(ctx, source.TableID, computed.FieldID)
	if err != nil || stored.Formula.Source != authored.CanonicalSource {
		t.Fatalf("rename changed canonical: %#v, %v", stored, err)
	}
	restored, err := catalog.RestoreFormulaDocument(ctx, source.TableID, stored.Formula.Source, 8)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Document.DisplaySource != "SUM({明细}.{金}额😀})" ||
		restored.Document.DocumentRevision != 8 {
		t.Fatalf("restored document = %#v", restored.Document)
	}
	roundTrip, err := catalog.AuthorFormulaDocument(ctx, source.TableID, restored.Document)
	if err != nil || roundTrip.CanonicalSource != stored.Formula.Source {
		t.Fatalf("restored special-character token rejected: %#v, %v", roundTrip, err)
	}
	// An editor holding the old display text still resolves its bound stable IDs.
	rebound, err := catalog.AuthorFormulaDocument(ctx, source.TableID, authored.Document)
	if err != nil || rebound.CanonicalSource != stored.Formula.Source ||
		rebound.Document.DisplaySource != restored.Document.DisplaySource {
		t.Fatalf("stale display rebinding = %#v, %v", rebound, err)
	}
	inspection, err := catalog.InspectFormulaDraft(ctx, source.TableID, rebound.CanonicalSource)
	if err != nil || inspection.CanonicalSource != rebound.CanonicalSource {
		t.Fatalf("existing compiler inspection = %#v, %v", inspection, err)
	}
	for _, scalarSource := range []string{"min(8, 3)", "max(8, 3)"} {
		scalar, err := catalog.RestoreFormulaDocument(ctx, source.TableID, scalarSource, 9)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip, err := catalog.AuthorFormulaDocument(ctx, source.TableID, scalar.Document)
		if err != nil || roundTrip.CanonicalSource != scalarSource {
			t.Fatalf("native scalar function changed: %#v, %v", roundTrip, err)
		}
		inspection, err := catalog.InspectFormulaDraft(ctx, source.TableID, roundTrip.CanonicalSource)
		if err != nil || inspection.CanonicalSource != scalarSource {
			t.Fatalf("native scalar inspection = %#v, %v", inspection, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := catalog.AuthorFormulaDocument(cancelled, source.TableID, document); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled author = %v", err)
	}
}
