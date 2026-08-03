package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
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
	catalog := schemaapi.New(app)

	authors, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("authors", "authors", []schema.FieldDefinition{
			field("name_id", "name", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	authorRelation := field(
		"author_id", "author",
		schema.FieldKindRelation, schema.DataTypeRelation,
	)
	authorRelation.Relation = &schema.RelationSpec{
		TargetTableID: "authors", Cardinality: "one", DeletePolicy: "setNull",
	}
	authorRelation.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintRelation, TargetTableID: "authors",
		Cardinality: "one", DeletePolicy: "setNull",
	}}
	authorName := field(
		"author_name_id", "author_name",
		schema.FieldKindLookup, schema.DataTypeLookup,
	)
	authorName.StorageType = schema.StorageText
	authorName.ReadOnly = true
	authorName.Lookup = &schema.LookupSpec{
		RelationFieldID: "author_id",
		TargetFieldID:   "name_id",
		Aggregate:       "first",
	}
	articles, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("articles", "articles", []schema.FieldDefinition{
			field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText),
			authorRelation,
			authorName,
		}),
		ExpectedRevision: 0,
	})
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
		"articles",
		articles.SchemaRevision,
		"lookup-missing-author",
		mutation.Operation{
			Kind: mutation.OperationInsert,
			Values: map[string]any{
				"title":  "Invalid",
				"author": missingAuthorID,
			},
		},
	))
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.relation.target_not_found" ||
		productErr.Path == nil ||
		*productErr.Path != "operations[0].values.author" {
		t.Fatalf("missing relation target error = %#v", err)
	}
	authorID := "authorrecord001"
	if _, err := kernel.Apply(ctx, mutationRequest(
		"authors",
		authors.SchemaRevision,
		"lookup-author",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &authorID,
			Values: map[string]any{"name": "Ada"},
		},
	)); err != nil {
		t.Fatalf("insert author: %#v", err)
	}
	articleID := "articlerecord01"
	receipt, err := kernel.Apply(ctx, mutationRequest(
		"articles",
		articles.SchemaRevision,
		"lookup-article",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &articleID,
			Values: map[string]any{"title": "Notes", "author": authorID},
		},
	))
	if err != nil {
		t.Fatalf("insert article: %#v", err)
	}
	if got := receipt.ComputedFields[articleID]["author_name"]; got != "Ada" {
		t.Fatalf("computed lookup = %#v", got)
	}
	collection, _ := app.FindCollectionByNameOrId("articles")
	record, err := app.FindRecordById(collection, articleID)
	if err != nil || record.GetString("author_name") != "Ada" {
		t.Fatalf("stored lookup = %#v, err=%v", record, err)
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
	catalogResult, err := relationService.Describe(ctx, "articles")
	if err != nil || len(catalogResult.Relations) != 1 ||
		len(catalogResult.Lookups) != 1 {
		t.Fatalf("relation catalog = %#v, err=%v", catalogResult, err)
	}
	search, err := relationService.SearchTargets(ctx, relation.SearchRequest{
		RelationID: "articles.author_id", Query: "Ada", Limit: 20,
	})
	if err != nil || len(search.Items) != 1 ||
		search.Items[0].RecordID != authorID ||
		search.Items[0].Label != "Ada" {
		t.Fatalf("relation search = %#v, err=%v", search, err)
	}
	delta := relation.DeltaRequest{
		RelationID:     "articles.author_id",
		SourceRecordID: articleID,
		SchemaRevision: articles.SchemaRevision,
		Removes: []relation.TargetRef{{
			TableID: "authors", RecordID: authorID, Label: "Ada",
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
	if err != nil || record.GetString("author") != "" ||
		record.GetString("author_name") != "" {
		t.Fatalf("cleared relation record = %#v, err=%v", record, err)
	}
}

func TestLookupCalculatorTraversesSavedMultiHopPath(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	catalog := schemaapi.New(app)

	companies, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"lookup_companies",
			"lookup_companies",
			[]schema.FieldDefinition{
				field(
					"name_id", "name",
					schema.FieldKindScalar, schema.DataTypeShortText,
				),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	companyRelation := field(
		"company_id", "company",
		schema.FieldKindRelation, schema.DataTypeRelation,
	)
	companyRelation.Relation = &schema.RelationSpec{
		TargetTableID: "lookup_companies",
		Cardinality:   "one",
		DeletePolicy:  "setNull",
	}
	companyRelation.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintRelation, TargetTableID: "lookup_companies",
		Cardinality: "one", DeletePolicy: "setNull",
	}}
	customers, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"lookup_customers",
			"lookup_customers",
			[]schema.FieldDefinition{companyRelation},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	customerRelation := field(
		"customer_id", "customer",
		schema.FieldKindRelation, schema.DataTypeRelation,
	)
	customerRelation.Relation = &schema.RelationSpec{
		TargetTableID: "lookup_customers",
		Cardinality:   "one",
		DeletePolicy:  "setNull",
	}
	customerRelation.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintRelation, TargetTableID: "lookup_customers",
		Cardinality: "one", DeletePolicy: "setNull",
	}}
	companyName := field(
		"company_name_id", "company_name",
		schema.FieldKindLookup, schema.DataTypeLookup,
	)
	companyName.StorageType = schema.StorageText
	companyName.ReadOnly = true
	companyName.Lookup = &schema.LookupSpec{
		RelationFieldID: "customer_id",
		Path: []schema.LookupPathStep{
			{RelationFieldID: "customer_id"},
			{RelationFieldID: "company_id"},
		},
		TargetFieldID: "name_id",
		Aggregate:     "first",
	}
	orders, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"lookup_orders",
			"lookup_orders",
			[]schema.FieldDefinition{customerRelation, companyName},
		),
		ExpectedRevision: 0,
	})
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
		"lookup_companies", companies.SchemaRevision, "multihop-company",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &companyID,
			Values: map[string]any{"name": "Analytical Engines"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	customerID := "lookupcustomer1"
	if _, err := kernel.Apply(ctx, mutationRequest(
		"lookup_customers", customers.SchemaRevision, "multihop-customer",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &customerID,
			Values: map[string]any{"company": companyID},
		},
	)); err != nil {
		t.Fatal(err)
	}
	orderID := "lookuporder0001"
	receipt, err := kernel.Apply(ctx, mutationRequest(
		"lookup_orders", orders.SchemaRevision, "multihop-order",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &orderID,
			Values: map[string]any{"customer": customerID},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if got := receipt.ComputedFields[orderID]["company_name"]; got != "Analytical Engines" {
		t.Fatalf("multi-hop lookup = %#v", got)
	}
	reloaded, err := catalog.Describe(ctx, "lookup_orders")
	if err != nil || len(reloaded.Fields[1].Lookup.EffectivePath()) != 2 {
		t.Fatalf("reloaded lookup path = %#v, err=%v", reloaded.Fields[1].Lookup, err)
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
	).Describe(ctx, "lookup_orders")
	if err != nil || len(described.Lookups) != 1 ||
		len(described.Lookups[0].Path) != 2 ||
		described.Lookups[0].Path[1].RelationID !=
			"lookup_customers.company_id" {
		t.Fatalf("described multi-hop lookup = %#v, err=%v", described, err)
	}

	if _, err := kernel.Apply(ctx, mutationRequest(
		"lookup_companies", companies.SchemaRevision, "multihop-company-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &companyID,
			Values: map[string]any{"name": "Difference Engines"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	jobRecord, err := app.FindFirstRecordByFilter(
		"vibetable_jobs",
		"job_type='formula_fanout' && source_table_id='lookup_orders'",
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
	orderCollection, _ := app.FindCollectionByNameOrId("lookup_orders")
	orderRecord, err := app.FindRecordById(orderCollection, orderID)
	if err != nil ||
		orderRecord.GetString("company_name") != "Difference Engines" {
		t.Fatalf("multi-hop fanout result = %#v, err=%v", orderRecord, err)
	}
}

func TestFormulaRelationFanoutJobResumesAfterCancellation(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	catalog := schemaapi.New(app)
	authors, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"fanout_authors", "fanout_authors",
			[]schema.FieldDefinition{
				field(
					"name_id", "name",
					schema.FieldKindScalar,
					schema.DataTypeShortText,
				),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	relationField := field(
		"author_id", "author",
		schema.FieldKindRelation, schema.DataTypeRelation,
	)
	relationField.Relation = &schema.RelationSpec{
		TargetTableID: "fanout_authors",
		Cardinality:   "one",
		DeletePolicy:  "setNull",
	}
	relationField.Constraints = []schema.FieldConstraint{{
		Kind:          schema.ConstraintRelation,
		TargetTableID: "fanout_authors",
		Cardinality:   "one",
		DeletePolicy:  "setNull",
	}}
	label := field(
		"label_id", "author_label",
		schema.FieldKindFormula, schema.DataTypeFormula,
	)
	label.StorageType = schema.StorageText
	label.ReadOnly = true
	label.Formula = &schema.FormulaSpec{
		Language: "cel-v1", Source: "author.name",
		ResultType: schema.DataTypeShortText,
		Version:    1, Status: "draft",
	}
	lookupField := field(
		"lookup_name_id", "lookup_name",
		schema.FieldKindLookup, schema.DataTypeLookup,
	)
	lookupField.StorageType = schema.StorageText
	lookupField.ReadOnly = true
	lookupField.Lookup = &schema.LookupSpec{
		RelationFieldID: "author_id",
		TargetFieldID:   "name_id",
		Aggregate:       "first",
	}
	articles, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"fanout_articles", "fanout_articles",
			[]schema.FieldDefinition{
				relationField, lookupField, label,
			},
		),
		ExpectedRevision: 0,
	})
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
		"fanout_authors", authors.SchemaRevision,
		"fanout-author-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &authorID,
			Values: map[string]any{"name": "Before"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	articleID := "fanoutarticle01"
	if _, err := kernel.Apply(ctx, mutationRequest(
		"fanout_articles", articles.SchemaRevision,
		"fanout-article-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &articleID,
			Values: map[string]any{"author": authorID},
		},
	)); err != nil {
		t.Fatal(err)
	}
	receipt, err := kernel.Apply(ctx, mutationRequest(
		"fanout_authors", authors.SchemaRevision,
		"fanout-author-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &authorID,
			Values: map[string]any{"name": "After"},
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
	collection, _ := app.FindCollectionByNameOrId("fanout_articles")
	article, err := app.FindRecordById(collection, articleID)
	if err != nil || article.GetString("author_label") != "After" ||
		article.GetString("lookup_name") != "After" {
		t.Fatalf("fan-out materialized article = %#v, err=%v", article, err)
	}
}

func TestFormulaDereferencesValidatedRelationTarget(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	catalog := schemaapi.New(app)

	authors, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"formula_authors",
			"formula_authors",
			[]schema.FieldDefinition{
				field(
					"name_id", "name",
					schema.FieldKindScalar,
					schema.DataTypeShortText,
				),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	author := field(
		"author_id", "author",
		schema.FieldKindRelation, schema.DataTypeRelation,
	)
	author.Relation = &schema.RelationSpec{
		TargetTableID: "formula_authors",
		Cardinality:   "one",
		DeletePolicy:  "setNull",
	}
	author.Constraints = []schema.FieldConstraint{{
		Kind:          schema.ConstraintRelation,
		TargetTableID: "formula_authors",
		Cardinality:   "one",
		DeletePolicy:  "setNull",
	}}
	label := field(
		"label_id", "author_label",
		schema.FieldKindFormula, schema.DataTypeFormula,
	)
	label.StorageType = schema.StorageText
	label.ReadOnly = true
	label.Formula = &schema.FormulaSpec{
		Language: "cel-v1", Source: "author.name",
		ResultType: schema.DataTypeShortText,
		Version:    1,
		Status:     "draft",
	}
	articles, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"formula_articles",
			"formula_articles",
			[]schema.FieldDefinition{author, label},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := app.FindRecordsByFilter(
		"vibetable_formula_dependencies",
		"source_table_id='formula_articles'",
		"",
		10,
		0,
	)
	if err != nil || len(dependencies) != 1 ||
		dependencies[0].GetString("target_field_id") != "name_id" {
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
		"formula_authors",
		authors.SchemaRevision,
		"formula-author-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &authorID,
			Values: map[string]any{"name": "Grace"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	blankArticleID := "blankarticle001"
	blankReceipt, err := kernel.Apply(ctx, mutationRequest(
		"formula_articles",
		articles.SchemaRevision,
		"formula-blank-article-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &blankArticleID,
			Values: map[string]any{},
		},
	))
	if err != nil {
		t.Fatalf("nullable relation formula must permit a blank relation: %v", err)
	}
	if got := blankReceipt.ComputedFields[blankArticleID]["author_label"]; got != nil {
		t.Fatalf("blank relation formula = %#v, want nil", got)
	}
	articleID := "formarticle0001"
	receipt, err := kernel.Apply(ctx, mutationRequest(
		"formula_articles",
		articles.SchemaRevision,
		"formula-article-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &articleID,
			Values: map[string]any{"author": authorID},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if got := receipt.ComputedFields[articleID]["author_label"]; got != "Grace" {
		t.Fatalf("relation formula = %#v", got)
	}

	invalid := articles
	invalid.SchemaRevision = "schema_1"
	invalid.Fields[1].Formula = &schema.FormulaSpec{
		Language: "cel-v1", Source: "author.missing",
		ResultType: schema.DataTypeShortText,
		Version:    1,
		Status:     "draft",
	}
	_, err = catalog.ValidateChange(ctx, schemaapi.Change{
		Definition: invalid, ExpectedRevision: 1,
	})
	var schemaErr *schema.ProductError
	if !errors.As(err, &schemaErr) ||
		schemaErr.Code != "schema.formula.target_field_not_found" {
		t.Fatalf("invalid relation formula error = %#v", err)
	}
}
