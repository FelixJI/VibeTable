package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type failingLiveDataPublisher struct{}

func (failingLiveDataPublisher) Publish(
	context.Context,
	mutation.DataChangedEvent,
) error {
	return errors.New("live realtime unavailable")
}

func TestLookupCalculatorMaterializesDirectRelationInMutation(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	authors := createV2IntegrationTable(t, ctx, app, "Authors", "lookup_authors_table")
	authorName := createV2IntegrationField(t, ctx, app, authors.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"), "lookup_authors_name")
	articles := createV2IntegrationTable(t, ctx, app, "Articles", "lookup_articles_table")
	title := createV2IntegrationField(t, ctx, app, articles.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"), "lookup_articles_title")
	authorRelation := createV2IntegrationRelation(t, ctx, app, articles.TableID,
		title.FieldID, authors.TableID, authorName.FieldID, "Author", "Articles", "one",
		"lookup_articles_author")
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Author name")
	lookupDraft.Lookup = &v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: authorRelation.FieldID}},
		TargetFieldID: authorName.FieldID,
	}
	authorLookup := createV2IntegrationField(t, ctx, app, articles.TableID, lookupDraft,
		"lookup_articles_author_name")
	if authorName.Definition == nil || title.Definition == nil || authorRelation.Definition == nil ||
		authorLookup.Definition == nil {
		t.Fatalf("incomplete V2 receipts: %#v %#v %#v %#v", authorName, title, authorRelation, authorLookup)
	}
	authorsDefinition, err := schemaapi.New(app).Describe(ctx, authors.TableID)
	if err != nil {
		t.Fatal(err)
	}
	articlesDefinition, err := schemaapi.New(app).Describe(ctx, articles.TableID)
	if err != nil {
		t.Fatal(err)
	}

	calculator := computed.New(
		formula.NewCalculator(formula.NewCompiler(formula.DefaultLimits())),
		lookup.NewCalculator(),
	)
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(calculator),
	)
	missingAuthorID := "missingauthor001"
	_, err = kernel.Apply(ctx, mutationRequest(
		articles.TableID,
		articlesDefinition.Snapshot.SchemaRevision,
		"lookup-missing-author",
		mutation.Operation{
			Kind: mutation.OperationInsert,
			Values: map[string]any{
				title.Definition.Identity.PhysicalName:          "Invalid",
				authorRelation.Definition.Identity.PhysicalName: missingAuthorID,
			},
		},
	))
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.relation.target_not_found" ||
		productErr.Path == nil ||
		*productErr.Path != "operations[0].values."+authorRelation.Definition.Identity.PhysicalName {
		t.Fatalf("missing relation target error = %#v", err)
	}
	authorID := "authorrecord001"
	if _, err := kernel.Apply(ctx, mutationRequest(
		authors.TableID,
		authorsDefinition.Snapshot.SchemaRevision,
		"lookup-author",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &authorID,
			Values: map[string]any{authorName.Definition.Identity.PhysicalName: "Ada"},
		},
	)); err != nil {
		t.Fatalf("insert author: %#v", err)
	}
	articleID := "articlerecord01"
	receipt, err := kernel.Apply(ctx, mutationRequest(
		articles.TableID,
		articlesDefinition.Snapshot.SchemaRevision,
		"lookup-article",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &articleID,
			Values: map[string]any{title.Definition.Identity.PhysicalName: "Notes", authorRelation.Definition.Identity.PhysicalName: authorID},
		},
	))
	if err != nil {
		t.Fatalf("insert article: %#v", err)
	}
	if got := receipt.ComputedFields[articleID][authorLookup.Definition.Identity.PhysicalName]; got != "Ada" {
		t.Fatalf("computed lookup = %#v", got)
	}
	collection, _ := app.FindCollectionByNameOrId(articles.PhysicalName)
	record, err := app.FindRecordById(collection, articleID)
	if err != nil || relatedcomputation.ProjectStored(record.GetRaw(authorLookup.Definition.Identity.PhysicalName)) != "Ada" {
		t.Fatalf("stored lookup = %#v, err=%v", record, err)
	}
	execution, err := schemaexecution.Describe(ctx, app, articles.TableID)
	if err != nil {
		t.Fatal(err)
	}
	cells, err := lookup.NewCalculator().CalculateCells(ctx, app, execution, record)
	if err != nil {
		t.Fatal(err)
	}
	cell := cells[authorLookup.Definition.Identity.PhysicalName]
	if cell.State != "ok" || cell.Value != "Ada" || len(cell.Provenance) != 1 ||
		cell.Provenance[0].Collection != authors.TableID ||
		cell.Provenance[0].CollectionLabel != "Authors" ||
		cell.Provenance[0].ItemID != authorID ||
		cell.Provenance[0].RecordLabel != "Ada" ||
		cell.Provenance[0].FieldID != authorName.FieldID ||
		cell.Provenance[0].FieldLabel != authorName.Definition.DisplayName ||
		cell.Provenance[0].Value != "Ada" {
		t.Fatalf("lookup provenance = %#v", cell)
	}

	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	relationService := relation.New(
		app,
		query.NewPort(app, querySource),
		kernel,
	)
	catalogResult, err := relationService.Describe(ctx, articles.TableID)
	if err != nil || len(catalogResult.Relations) != 1 ||
		len(catalogResult.Lookups) != 1 ||
		catalogResult.LookupMaxDepth != v2.MaxLookupPathDepth {
		t.Fatalf("relation catalog = %#v, err=%v", catalogResult, err)
	}
	groupedLookup, err := relationService.QueryLookups(ctx, relation.LookupQueryRequest{
		TableID: articles.TableID, SchemaRevision: articlesDefinition.Snapshot.SchemaRevision,
		Query: query.TableQuery{Limit: 100},
		Groups: []query.GroupSpec{{
			Field: authorLookup.Definition.Identity.PhysicalName,
		}},
		GroupLimit: 1,
	})
	if err != nil || len(groupedLookup.GroupRows) != 1 || groupedLookup.HasMoreGroups ||
		fmt.Sprint(groupedLookup.GroupRows[0].Key[0]) != "Ada" {
		t.Fatalf("fresh lookup groups = %#v, err=%v", groupedLookup, err)
	}
	search, err := relationService.SearchTargets(ctx, relation.SearchRequest{
		RelationID: articles.TableID + "." + authorRelation.FieldID, Query: "Ada", Limit: 20,
	})
	if err != nil || len(search.Items) != 1 ||
		search.Items[0].RecordID != authorID ||
		search.Items[0].Label != "Ada" {
		t.Fatalf("relation search = %#v, err=%v", search, err)
	}
	createdTarget, err := relationService.CreateTarget(ctx, relation.CreateTargetRequest{
		RelationID: articles.TableID + "." + authorRelation.FieldID, Label: "Grace",
		RequestID: "create-related-author", IdempotencyKey: "create-related-author",
		Actor: mutation.Actor{Type: "user", ID: "local"},
	})
	if err != nil || createdTarget.Target.TableID != authors.TableID ||
		createdTarget.Target.RecordID == "" || createdTarget.Target.Label != "Grace" ||
		createdTarget.Receipt.Status != mutation.StatusApplied {
		t.Fatalf("created relation target = %#v, err=%v", createdTarget, err)
	}
	createdSearch, err := relationService.SearchTargets(ctx, relation.SearchRequest{
		RelationID: articles.TableID + "." + authorRelation.FieldID, Query: "Grace", Limit: 20,
	})
	if err != nil || len(createdSearch.Items) != 1 ||
		createdSearch.Items[0].RecordID != createdTarget.Target.RecordID {
		t.Fatalf("created target search = %#v, err=%v", createdSearch, err)
	}
	lookupPage, err := relationService.QueryLookups(ctx, relation.LookupQueryRequest{
		TableID: articles.TableID, SchemaRevision: articlesDefinition.Snapshot.SchemaRevision,
		Query: query.TableQuery{Limit: 100},
	})
	if err != nil || len(lookupPage.Rows) != 1 {
		t.Fatalf("lookup query = %#v, err=%v", lookupPage, err)
	}
	queryCell, ok := lookupPage.Rows[0][authorLookup.Definition.Identity.PhysicalName].(lookup.CellValue)
	if !ok || queryCell.Value != "Ada" || len(queryCell.Provenance) != 1 ||
		queryCell.Provenance[0].ItemID != authorID {
		t.Fatalf("lookup query cell = %#v", lookupPage.Rows[0]["author_name"])
	}
	broken := core.NewRecord(collection)
	broken.Set(title.Definition.Identity.PhysicalName, "Broken reference")
	if err := app.Save(broken); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().NewQuery(fmt.Sprintf(
		"UPDATE `%s` SET `%s`={:target} WHERE `id`={:id}", collection.Name, authorRelation.Definition.Identity.PhysicalName,
	)).Bind(dbx.Params{"target": "missingauthor01", "id": broken.Id}).Execute(); err != nil {
		t.Fatal(err)
	}
	lookupPage, err = relationService.QueryLookups(ctx, relation.LookupQueryRequest{
		TableID: articles.TableID, SchemaRevision: articlesDefinition.Snapshot.SchemaRevision,
		Query: query.TableQuery{Limit: 100},
	})
	if err != nil || len(lookupPage.Rows) != 2 {
		t.Fatalf("lookup query with missing source = %#v, err=%v", lookupPage, err)
	}
	var missingCell lookup.CellValue
	for _, row := range lookupPage.Rows {
		if row["id"] == broken.Id {
			missingCell, _ = row[authorLookup.Definition.Identity.PhysicalName].(lookup.CellValue)
		}
	}
	if missingCell.State != "invalid" || missingCell.Diagnostic == nil ||
		missingCell.Diagnostic.Code != "lookup.value.source_missing" {
		t.Fatalf("missing-source cell = %#v", missingCell)
	}
	delta := relation.DeltaRequest{
		RelationID:     articles.TableID + "." + authorRelation.FieldID,
		SourceRecordID: articleID,
		SchemaRevision: articlesDefinition.Snapshot.SchemaRevision,
		Removes: []relation.TargetRef{{
			TableID: authors.TableID, RecordID: authorID, Label: "Ada",
		}},
		Adds:           []relation.TargetRef{},
		RequestID:      "relation-clear",
		IdempotencyKey: "relation-clear",
		Actor:          mutation.Actor{Type: "user", ID: "local"},
	}
	preview, err := relationService.PreviewDelta(ctx, delta)
	if err != nil || !preview.CanApply ||
		len(preview.Current) != 1 || len(preview.Result) != 0 {
		t.Fatalf("relation delta preview = %#v, err=%v", preview, err)
	}
	applied, err := relationService.ApplyDelta(ctx, delta)
	if err != nil || len(applied.Current) != 0 ||
		applied.Receipt.Status != mutation.StatusApplied {
		t.Fatalf("relation delta apply = %#v, err=%v", applied, err)
	}
	record, err = app.FindRecordById(collection, articleID)
	if err != nil || record.GetString(authorRelation.Definition.Identity.PhysicalName) != "" ||
		relatedcomputation.ProjectStored(record.GetRaw(authorLookup.Definition.Identity.PhysicalName)) != nil {
		t.Fatalf("cleared relation record = %#v, err=%v", record, err)
	}
}

func TestLookupCalculatorTraversesSavedMultiHopPath(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	companies := createV2IntegrationTable(t, ctx, app, "Lookup companies", "multihop_companies_table")
	companyName := createV2IntegrationField(t, ctx, app, companies.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"), "multihop_companies_name")
	customers := createV2IntegrationTable(t, ctx, app, "Lookup customers", "multihop_customers_table")
	customerName := createV2IntegrationField(t, ctx, app, customers.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"), "multihop_customers_name")
	companyRelation := createV2IntegrationRelation(t, ctx, app, customers.TableID,
		customerName.FieldID, companies.TableID, companyName.FieldID, "Company", "Customers", "one",
		"multihop_customers_company")
	orders := createV2IntegrationTable(t, ctx, app, "Lookup orders", "multihop_orders_table")
	orderName := createV2IntegrationField(t, ctx, app, orders.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"), "multihop_orders_name")
	customerRelation := createV2IntegrationRelation(t, ctx, app, orders.TableID,
		orderName.FieldID, customers.TableID, customerName.FieldID, "Customer", "Orders", "one",
		"multihop_orders_customer")
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Company name")
	lookupDraft.Lookup = &v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: customerRelation.FieldID}, {RelationFieldID: companyRelation.FieldID}},
		TargetFieldID: companyName.FieldID,
	}
	companyLookup := createV2IntegrationField(t, ctx, app, orders.TableID, lookupDraft,
		"multihop_orders_company_name")
	if companyName.Definition == nil || customerName.Definition == nil || orderName.Definition == nil ||
		companyRelation.Definition == nil || customerRelation.Definition == nil || companyLookup.Definition == nil {
		t.Fatalf("incomplete V2 receipts: %#v %#v %#v %#v %#v %#v", companyName, customerName, orderName, companyRelation, customerRelation, companyLookup)
	}
	companiesDefinition, err := schemaapi.New(app).Describe(ctx, companies.TableID)
	if err != nil {
		t.Fatal(err)
	}
	customersDefinition, err := schemaapi.New(app).Describe(ctx, customers.TableID)
	if err != nil {
		t.Fatal(err)
	}
	ordersDefinition, err := schemaapi.New(app).Describe(ctx, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	jobService := jobs.New(
		app,
		nil,
		jobs.WithRunContext(cancelled),
	)
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(computed.New(lookup.NewCalculator())),
		mutation.WithPublisher(jobService),
	)
	jobService.SetKernel(kernel)
	companyID := "lookupcompany01"
	if _, err := kernel.Apply(ctx, mutationRequest(
		companies.TableID, companiesDefinition.Snapshot.SchemaRevision, "multihop-company",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &companyID,
			Values: map[string]any{companyName.Definition.Identity.PhysicalName: "Analytical Engines"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	customerID := "lookupcustomer1"
	if _, err := kernel.Apply(ctx, mutationRequest(
		customers.TableID, customersDefinition.Snapshot.SchemaRevision, "multihop-customer",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &customerID,
			Values: map[string]any{companyRelation.Definition.Identity.PhysicalName: companyID},
		},
	)); err != nil {
		t.Fatal(err)
	}
	orderID := "lookuporder0001"
	receipt, err := kernel.Apply(ctx, mutationRequest(
		orders.TableID, ordersDefinition.Snapshot.SchemaRevision, "multihop-order",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &orderID,
			Values: map[string]any{customerRelation.Definition.Identity.PhysicalName: customerID},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if got := receipt.ComputedFields[orderID][companyLookup.Definition.Identity.PhysicalName]; got != "Analytical Engines" {
		t.Fatalf("multi-hop lookup = %#v", got)
	}
	reloadedLookup := integrationFieldByID(ordersDefinition, companyLookup.FieldID)
	if err != nil || reloadedLookup == nil || reloadedLookup.Lookup == nil ||
		len(reloadedLookup.Lookup.Path) != 2 {
		t.Fatalf("reloaded lookup path = %#v, err=%v", reloadedLookup, err)
	}
	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	described, err := relation.New(
		app,
		query.NewPort(
			app,
			querySource),

		kernel,
	).Describe(ctx, orders.TableID)
	if err != nil || len(described.Lookups) != 1 ||
		len(described.Lookups[0].Path) != 2 ||
		described.Lookups[0].Path[1].RelationID !=
			customers.TableID+"."+companyRelation.FieldID {
		t.Fatalf("described multi-hop lookup = %#v, err=%v", described, err)
	}

	updateReceipt, err := kernel.Apply(ctx, mutationRequest(
		companies.TableID, companiesDefinition.Snapshot.SchemaRevision, "multihop-company-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &companyID,
			Values: map[string]any{companyName.Definition.Identity.PhysicalName: "Difference Engines"},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(updateReceipt.EmittedEvents) != 1 {
		t.Fatalf("company update emitted events = %#v", updateReceipt.EmittedEvents)
	}
	jobRecord, err := app.FindFirstRecordByFilter(
		"vibetable_jobs",
		"job_type='formula_fanout' && source_event_id={:event} && source_table_id={:table}",
		dbx.Params{"event": updateReceipt.EmittedEvents[0], "table": orders.TableID},
	)
	if err != nil {
		t.Fatalf("multi-hop fanout job was not persisted: %v", err)
	}
	restarted := jobs.New(app, kernel)
	defer restarted.Shutdown()
	restarted.ResumePending(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, getErr := restarted.Get(ctx, jobRecord.Id)
		if getErr == nil && snapshot.State == "complete" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, err := restarted.Get(ctx, jobRecord.Id)
	if err != nil || snapshot.State != "complete" ||
		snapshot.Progress.Completed != 1 {
		t.Fatalf("multi-hop fanout snapshot = %#v, err=%v", snapshot, err)
	}
	orderCollection, _ := app.FindCollectionByNameOrId(orders.PhysicalName)
	orderRecord, err := app.FindRecordById(orderCollection, orderID)
	if err != nil ||
		relatedcomputation.ProjectStored(orderRecord.GetRaw(companyLookup.Definition.Identity.PhysicalName)) != "Difference Engines" {
		t.Fatalf("multi-hop fanout result = %#v, err=%v", orderRecord, err)
	}
}

func TestFormulaRelationFanoutJobResumesAfterCancellation(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	authors := createV2IntegrationTable(t, ctx, app, "Fanout authors", "fanout_authors_table")
	authorName := createV2IntegrationField(t, ctx, app, authors.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"), "fanout_authors_name")
	articles := createV2IntegrationTable(t, ctx, app, "Fanout articles", "fanout_articles_table")
	articleName := createV2IntegrationField(t, ctx, app, articles.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"), "fanout_articles_name")
	relationField := createV2IntegrationRelation(t, ctx, app, articles.TableID,
		articleName.FieldID, authors.TableID, authorName.FieldID, "Author", "Articles", "one",
		"fanout_articles_author")
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Lookup name")
	lookupDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{{RelationFieldID: relationField.FieldID}}, TargetFieldID: authorName.FieldID,
	}
	lookupField := createV2IntegrationField(t, ctx, app, articles.TableID, lookupDraft,
		"fanout_articles_lookup")
	labelDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Author label")
	labelDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: `concat({Author}.{Name}, "")`}
	label := createV2IntegrationFormula(t, ctx, app, articles.TableID, labelDraft,
		"fanout_articles_label")
	if authorName.Definition == nil || relationField.Definition == nil || lookupField.Definition == nil || label.Definition == nil {
		t.Fatalf("incomplete V2 receipts: %#v %#v %#v %#v", authorName, relationField, lookupField, label)
	}
	authorsDefinition, err := schemaapi.New(app).Describe(ctx, authors.TableID)
	if err != nil {
		t.Fatal(err)
	}
	articlesDefinition, err := schemaapi.New(app).Describe(ctx, articles.TableID)
	if err != nil {
		t.Fatal(err)
	}

	hub := realtime.New(app)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	jobService := jobs.New(
		app, nil,
		jobs.WithTaskPublisher(hub),
		jobs.WithDataPublisher(hub),
		jobs.WithRunContext(cancelled),
	)
	defer jobService.Shutdown()
	calculator := computed.New(
		lookup.NewCalculator(),
		formula.NewCalculator(
			formula.NewCompiler(formula.DefaultLimits()),
		),
	)
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(calculator),
		mutation.WithPublisher(jobService),
	)
	jobService.SetKernel(kernel)
	authorID := "fanoutauthor001"
	if _, err := kernel.Apply(ctx, mutationRequest(
		authors.TableID, authorsDefinition.Snapshot.SchemaRevision,
		"fanout-author-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &authorID,
			Values: map[string]any{authorName.Definition.Identity.PhysicalName: "Before"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	articleID := "fanoutarticle01"
	if _, err := kernel.Apply(ctx, mutationRequest(
		articles.TableID, articlesDefinition.Snapshot.SchemaRevision,
		"fanout-article-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &articleID,
			Values: map[string]any{relationField.Definition.Identity.PhysicalName: authorID},
		},
	)); err != nil {
		t.Fatal(err)
	}
	receipt, err := kernel.Apply(ctx, mutationRequest(
		authors.TableID, authorsDefinition.Snapshot.SchemaRevision,
		"fanout-author-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &authorID,
			Values: map[string]any{authorName.Definition.Identity.PhysicalName: "After"},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.EmittedEvents) != 1 {
		t.Fatalf("target mutation emitted events = %#v", receipt.EmittedEvents)
	}
	var fanoutJobID string
	var fanoutJob *core.Record
	deadline := time.Now().Add(2 * time.Second)
	for fanoutJobID == "" && time.Now().Before(deadline) {
		record, findErr := app.FindFirstRecordByFilter(
			"vibetable_jobs",
			"job_type='formula_fanout'",
		)
		if findErr == nil {
			fanoutJobID = record.Id
			fanoutJob = record
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fanoutJobID == "" {
		t.Fatal("formula fan-out job was not persisted")
	}
	if err := app.Delete(fanoutJob); err != nil {
		t.Fatalf("remove initial fan-out job: %v", err)
	}

	outbox, err := app.FindFirstRecordByFilter(
		"vibetable_outbox",
		"event_id={:event}",
		dbx.Params{"event": receipt.EmittedEvents[0]},
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(outbox.GetRaw("payload_json"))
	if err != nil {
		t.Fatal(err)
	}
	var committedEvent mutation.DataChangedEvent
	if err := mutation.DecodeStrict(raw, &committedEvent); err != nil {
		t.Fatal(err)
	}
	liveFailureService := jobs.New(
		app,
		kernel,
		jobs.WithDataPublisher(failingLiveDataPublisher{}),
		jobs.WithRunContext(cancelled),
	)
	if err := liveFailureService.Publish(ctx, committedEvent); err == nil {
		t.Fatal("live publish failure was not returned")
	}
	liveFailureService.Shutdown()
	failedLiveJob, err := app.FindFirstRecordByFilter(
		"vibetable_jobs",
		"job_type='formula_fanout' && source_event_id={:event}",
		dbx.Params{"event": receipt.EmittedEvents[0]},
	)
	if err != nil {
		t.Fatalf("live failure lost durable fan-out job: %v", err)
	}
	if err := app.Delete(failedLiveJob); err != nil {
		t.Fatalf("simulate pre-enqueue crash gap: %v", err)
	}

	restarted := jobs.New(
		app, kernel,
		jobs.WithTaskPublisher(hub),
		jobs.WithDataPublisher(hub),
	)
	defer restarted.Shutdown()
	restarted.ResumePending(ctx)
	fanoutJobID = ""
	deadline = time.Now().Add(2 * time.Second)
	for fanoutJobID == "" && time.Now().Before(deadline) {
		record, findErr := app.FindFirstRecordByFilter(
			"vibetable_jobs",
			"job_type='formula_fanout' && source_event_id={:event}",
			dbx.Params{"event": receipt.EmittedEvents[0]},
		)
		if findErr == nil {
			fanoutJobID = record.Id
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fanoutJobID == "" {
		t.Fatal("startup recovery did not recreate fan-out from durable outbox")
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, getErr := restarted.Get(ctx, fanoutJobID)
		if getErr == nil && snapshot.State == "complete" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, err := restarted.Get(ctx, fanoutJobID)
	if err != nil || snapshot.State != "complete" ||
		snapshot.Progress.Completed != 1 ||
		snapshot.Progress.Total != 1 {
		t.Fatalf(
			"resumed fan-out snapshot = %#v, job error=%#v, err=%v",
			snapshot, snapshot.Error, err,
		)
	}
	collection, _ := app.FindCollectionByNameOrId(articles.PhysicalName)
	article, err := app.FindRecordById(collection, articleID)
	if err != nil || relatedcomputation.ProjectStored(article.GetRaw(label.Definition.Identity.PhysicalName)) != "After" ||
		relatedcomputation.ProjectStored(article.GetRaw(lookupField.Definition.Identity.PhysicalName)) != "After" {
		t.Fatalf("fan-out materialized article = %#v, err=%v", article, err)
	}
}

func TestFormulaDereferencesValidatedRelationTarget(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	authors := createV2IntegrationTable(t, ctx, app, "Formula authors", "formula_authors_table")
	authorName := createV2IntegrationField(t, ctx, app, authors.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"), "formula_authors_name")
	articles := createV2IntegrationTable(t, ctx, app, "Formula articles", "formula_articles_table")
	articleName := createV2IntegrationField(t, ctx, app, articles.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"), "formula_articles_name")
	author := createV2IntegrationRelation(t, ctx, app, articles.TableID,
		articleName.FieldID, authors.TableID, authorName.FieldID, "Author", "Articles", "one",
		"formula_articles_author")
	labelDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Author label")
	labelDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: `concat({Author}.{Name}, "")`}
	label := createV2IntegrationFormula(t, ctx, app, articles.TableID, labelDraft,
		"formula_articles_label")
	if authorName.Definition == nil || author.Definition == nil || label.Definition == nil {
		t.Fatalf("incomplete V2 receipts: %#v %#v %#v", authorName, author, label)
	}
	authorsDefinition, err := schemaapi.New(app).Describe(ctx, authors.TableID)
	if err != nil {
		t.Fatal(err)
	}
	articlesDefinition, err := schemaapi.New(app).Describe(ctx, articles.TableID)
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := app.FindRecordsByFilter(
		"vibetable_formula_dependencies",
		fmt.Sprintf("source_table_id=%q", articles.TableID),
		"",
		10,
		0,
	)
	if err != nil || len(dependencies) != 1 ||
		dependencies[0].GetString("target_field_id") != authorName.FieldID {
		t.Fatalf("formula dependency metadata = %#v, err=%v", dependencies, err)
	}

	calculator := computed.New(
		lookup.NewCalculator(),
		formula.NewCalculator(
			formula.NewCompiler(formula.DefaultLimits()),
		),
	)
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(calculator),
	)
	authorID := "formauthor00001"
	if _, err := kernel.Apply(ctx, mutationRequest(
		authors.TableID,
		authorsDefinition.Snapshot.SchemaRevision,
		"formula-author-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &authorID,
			Values: map[string]any{authorName.Definition.Identity.PhysicalName: "Grace"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	blankArticleID := "blankarticle001"
	blankReceipt, err := kernel.Apply(ctx, mutationRequest(
		articles.TableID,
		articlesDefinition.Snapshot.SchemaRevision,
		"formula-blank-article-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &blankArticleID,
			Values: map[string]any{},
		},
	))
	if err != nil {
		t.Fatalf("nullable relation formula must permit a blank relation: %v", err)
	}
	if got := blankReceipt.ComputedFields[blankArticleID][label.Definition.Identity.PhysicalName]; got != nil {
		t.Fatalf("blank relation formula = %#v, want nil", got)
	}
	articleID := "formarticle0001"
	receipt, err := kernel.Apply(ctx, mutationRequest(
		articles.TableID,
		articlesDefinition.Snapshot.SchemaRevision,
		"formula-article-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &articleID,
			Values: map[string]any{author.Definition.Identity.PhysicalName: authorID},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if got := receipt.ComputedFields[articleID][label.Definition.Identity.PhysicalName]; got != "Grace" {
		t.Fatalf("relation formula = %#v", got)
	}

	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	revisions, err := catalog.Revisions(ctx, articles.TableID)
	if err != nil {
		t.Fatal(err)
	}
	invalidDraft := fieldDraftForIntegration(t, v2.LogicalFormula, label.Definition.DisplayName)
	invalidDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: `concat({Author}.{Missing}, "")`}
	_, err = planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: articles.TableID, FieldID: label.FieldID,
		ExpectedSchemaRev: revisions.Schema, Draft: &invalidDraft,
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	var formulaErr *formula.Error
	if !errors.As(err, &formulaErr) || formulaErr.Code != "formula.dependency" ||
		formulaErr.Details["scope"] != "relation target" ||
		formulaErr.Details["displayName"] != "Missing" {
		t.Fatalf("invalid relation formula error = %#v", err)
	}
}
