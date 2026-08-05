package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/query"
)

func TestQueryPortRealPocketBaseFilteringPagingAggregateAndSnapshot(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: queryTempDir(t), HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap(): %v", err)
	}
	defer func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	}()

	customers := core.NewBaseCollection("query_customers")
	customers.Fields.Add(
		&core.TextField{Name: "name"},
		&core.BoolField{Name: "active"},
	)
	if err := app.Save(customers); err != nil {
		t.Fatalf("save customers collection: %v", err)
	}
	alice := saveRecord(t, app, customers, map[string]any{"name": "Alice", "active": true})
	bob := saveRecord(t, app, customers, map[string]any{"name": "Bob", "active": false})

	orders := core.NewBaseCollection("query_orders")
	orders.Fields.Add(
		&core.TextField{Name: "name"},
		&core.NumberField{Name: "amount"},
		&core.TextField{Name: "status"},
		&core.TextField{Name: "notes"},
		&core.TextField{Name: "archive_status"},
		&core.DateField{Name: "deleted_at"},
		&core.DateField{Name: "order_date"},
		&core.JSONField{Name: "payload"},
		&core.RelationField{Name: "customer", CollectionId: customers.Id, MaxSelect: 1},
		&core.RelationField{Name: "watchers", CollectionId: customers.Id, MaxSelect: 5},
	)
	if err := app.Save(orders); err != nil {
		t.Fatalf("save orders collection: %v", err)
	}
	first := saveRecord(t, app, orders, map[string]any{
		"name": "Alpha", "amount": 100, "status": "open",
		"order_date": "2026-01-05 10:00:00.000Z",
		"payload":    types.JSONRaw(`{"nested":{"rank":2}}`),
		"customer":   alice.Id, "watchers": []string{alice.Id, bob.Id},
	})
	second := saveRecord(t, app, orders, map[string]any{
		"name": "Beta", "amount": 100, "status": "closed",
		"order_date": "2026-01-20 10:00:00.000Z",
		"payload":    types.JSONRaw(`{"nested":{"rank":3},"optional":null}`),
		"customer":   alice.Id, "watchers": []string{bob.Id},
	})
	saveRecord(t, app, orders, map[string]any{
		"name": "Gamma", "amount": 81, "status": "closed",
		"order_date": "2026-02-01 10:00:00.000Z",
		"payload":    types.JSONRaw(`{"nested":{"rank":1}}`),
		"customer":   bob.Id, "watchers": []string{alice.Id},
	})
	emptyRelations := saveRecord(t, app, orders, map[string]any{
		"name": "No relations", "amount": 40, "status": "open",
		"payload": types.JSONRaw(`null`),
	})
	archived := saveRecord(t, app, orders, map[string]any{
		"name": "Archived", "amount": 999, "status": "open",
		"archive_status": "archived",
		"customer":       alice.Id,
		"watchers":       []string{bob.Id},
	})

	source := &staticQuerySource{descriptor: query.TableDescriptor{
		DatabaseID: "local", TableID: "orders", PhysicalName: orders.Name,
		PrimaryKey: "id", SchemaRevision: "schema-1", DataRevision: 7,
		ArchiveMode: query.ArchiveModeStatus, ArchiveField: "archive_status",
		ArchiveValue: "archived",
		Fields: map[string]query.FieldDescriptor{
			"id":             {PhysicalName: "id", Type: query.FieldTypeText},
			"name":           {PhysicalName: "name", Type: query.FieldTypeText, Searchable: true},
			"amount":         {PhysicalName: "amount", Type: query.FieldTypeNumber},
			"status":         {PhysicalName: "status", Type: query.FieldTypeText},
			"notes":          {PhysicalName: "notes", Type: query.FieldTypeText},
			"archive_status": {PhysicalName: "archive_status", Type: query.FieldTypeText},
			"deleted_at":     {PhysicalName: "deleted_at", Type: query.FieldTypeDate},
			"order_date":     {PhysicalName: "order_date", Type: query.FieldTypeDate},
			"payload":        {PhysicalName: "payload", Type: query.FieldTypeJSON},
			"customer": {
				PhysicalName: "customer", Type: query.FieldTypeRelation,
				Relation: &query.RelationDescriptor{
					TableName: customers.Name, PrimaryKey: "id",
					Fields: map[string]query.FieldDescriptor{
						"name":   {PhysicalName: "name", Type: query.FieldTypeText},
						"active": {PhysicalName: "active", Type: query.FieldTypeBool},
					},
				},
			},
			"watchers": {
				PhysicalName: "watchers", Type: query.FieldTypeMultiRelation,
				Relation: &query.RelationDescriptor{
					TableName: customers.Name, PrimaryKey: "id", Multiple: true,
				},
			},
		},
	}}
	port := query.NewPort(app, source)
	ctx := context.Background()
	nullsLast := true
	input := query.TableQuery{
		Filters: []query.FilterExpression{
			{
				Filters: []query.FilterExpression{
					{Field: "payload.optional", Operator: query.OperatorIsNull},
					{Field: "status", Operator: query.OperatorEqual, Value: "closed"},
				},
				GroupLogic: query.LogicOr,
			},
			{
				Field: "customer.name", Operator: query.OperatorContains,
				Value: "Ali", Logic: query.LogicAnd,
			},
		},
		Sorts: []query.SortCondition{{
			Field: "amount", Direction: query.SortDescending, NullsLast: &nullsLast,
		}},
		Limit: 1,
	}

	pageOne, err := port.QueryPage(ctx, "orders", input)
	if err != nil {
		t.Fatalf("QueryPage(first): %v", err)
	}
	input.Offset = 1
	pageTwo, err := port.QueryPage(ctx, "orders", input)
	if err != nil {
		t.Fatalf("QueryPage(second): %v", err)
	}
	if pageOne.FilteredRows != 2 || pageOne.TotalRows != 4 {
		t.Fatalf("unexpected counts: filtered=%d total=%d", pageOne.FilteredRows, pageOne.TotalRows)
	}
	gotIDs := []any{pageOne.Rows[0]["id"], pageTwo.Rows[0]["id"]}
	sortedIDs := []string{first.Id, second.Id}
	sort.Strings(sortedIDs)
	wantIDs := []any{sortedIDs[0], sortedIDs[1]}
	if gotIDs[0] != wantIDs[0] || gotIDs[1] != wantIDs[1] {
		t.Fatalf("stable pages = %#v, want %#v", gotIDs, wantIDs)
	}
	view, err := port.ExecuteViewQuery(ctx, "orders", query.ViewQuery{
		Query: query.TableQuery{
			Filters: []query.FilterExpression{{
				Field: "amount", Operator: query.OperatorGreaterEq, Value: 80,
			}},
			Sorts: []query.SortCondition{{
				Field: "amount", Direction: query.SortDescending,
			}},
			Limit: 1,
		},
		Groups:     []query.GroupSpec{{Field: "status", Direction: query.SortAscending}},
		Summaries:  []query.SummarySpec{{Field: "amount", Function: query.AggregateSum}},
		GroupLimit: 1,
	})
	if err != nil {
		t.Fatalf("ExecuteViewQuery(first groups page): %v", err)
	}
	if len(view.Page.Rows) != 1 || view.Page.FilteredRows != 3 ||
		len(view.GroupRows) != 1 || !view.HasMoreGroups {
		t.Fatalf("view page/group cardinality = %#v", view)
	}
	if fmt.Sprint(view.GroupRows[0].Key[0]) != "closed" ||
		view.GroupRows[0].Count != 2 ||
		fmt.Sprint(view.GroupRows[0].Summaries[0]) != "181" {
		t.Fatalf("first full-result group = %#v", view.GroupRows[0])
	}
	secondGroups, err := port.ExecuteViewQuery(ctx, "orders", query.ViewQuery{
		Query: query.TableQuery{
			Filters: []query.FilterExpression{{
				Field: "amount", Operator: query.OperatorGreaterEq, Value: 80,
			}},
			Limit: 1,
		},
		Groups:      []query.GroupSpec{{Field: "status", Direction: query.SortAscending}},
		Summaries:   []query.SummarySpec{{Field: "amount", Function: query.AggregateSum}},
		GroupOffset: 1,
		GroupLimit:  1,
	})
	if err != nil || len(secondGroups.GroupRows) != 1 || secondGroups.HasMoreGroups ||
		fmt.Sprint(secondGroups.GroupRows[0].Key[0]) != "open" ||
		secondGroups.GroupRows[0].Count != 1 ||
		fmt.Sprint(secondGroups.GroupRows[0].Summaries[0]) != "100" {
		t.Fatalf("second full-result group page = %#v err=%v", secondGroups, err)
	}
	twoLevel, err := port.ExecuteViewQuery(ctx, "orders", query.ViewQuery{
		Query: query.TableQuery{
			Filters: []query.FilterExpression{{
				Field: "amount", Operator: query.OperatorGreaterEq, Value: 80,
			}},
			Limit: 1,
		},
		Groups: []query.GroupSpec{
			{Field: "status", Direction: query.SortAscending},
			{Field: "order_date", Bucket: query.GroupBucketMonth},
		},
		Summaries: []query.SummarySpec{
			{Field: "amount", Function: query.AggregateSum},
			{Field: "amount", Function: query.AggregateAvg},
		},
		GroupLimit: 1,
	})
	if err != nil || len(twoLevel.GroupRows) != 1 || !twoLevel.HasMoreGroups {
		t.Fatalf("two-level first page = %#v err=%v", twoLevel, err)
	}
	twoLevelRow := twoLevel.GroupRows[0]
	if twoLevelRow.ParentCount == nil || *twoLevelRow.ParentCount != 2 ||
		fmt.Sprint(twoLevelRow.ParentSummaries[0]) != "181" ||
		fmt.Sprint(twoLevelRow.ParentSummaries[1]) != "90.5" {
		t.Fatalf("two-level complete parent aggregate = %#v", twoLevelRow)
	}
	dateGroups, err := port.ExecuteViewQuery(ctx, "orders", query.ViewQuery{
		Query: query.TableQuery{
			Filters: []query.FilterExpression{{
				Field: "amount", Operator: query.OperatorGreaterEq, Value: 80,
			}},
			Limit: 1,
		},
		Groups: []query.GroupSpec{{Field: "order_date", Bucket: query.GroupBucketMonth}},
	})
	if err != nil || len(dateGroups.GroupRows) != 2 ||
		fmt.Sprint(dateGroups.GroupRows[0].Key[0]) != "2026-01" ||
		dateGroups.GroupRows[0].Count != 2 ||
		fmt.Sprint(dateGroups.GroupRows[1].Key[0]) != "2026-02" ||
		dateGroups.GroupRows[1].Count != 1 {
		t.Fatalf("date bucket groups = %#v err=%v", dateGroups.GroupRows, err)
	}
	numberGroups, err := port.ExecuteViewQuery(ctx, "orders", query.ViewQuery{
		Query: query.TableQuery{Limit: 1},
		Groups: []query.GroupSpec{{
			Field: "amount", Bucket: query.GroupBucketNumber, NumberInterval: 50,
		}},
	})
	if err != nil || len(numberGroups.GroupRows) != 3 ||
		fmt.Sprint(numberGroups.GroupRows[0].Key[0]) != "0" ||
		numberGroups.GroupRows[0].Count != 1 ||
		fmt.Sprint(numberGroups.GroupRows[1].Key[0]) != "50" ||
		numberGroups.GroupRows[1].Count != 1 ||
		fmt.Sprint(numberGroups.GroupRows[2].Key[0]) != "100" ||
		numberGroups.GroupRows[2].Count != 2 {
		t.Fatalf("number bucket groups = %#v err=%v", numberGroups.GroupRows, err)
	}
	if _, ok := pageOne.Rows[0]["payload"].(map[string]any); !ok {
		t.Fatalf("JSON did not round-trip as structured data: %#v", pageOne.Rows[0]["payload"])
	}
	jsonPage, err := port.QueryPage(ctx, "orders", query.TableQuery{
		Filters: []query.FilterExpression{{
			Field: "payload.nested.rank", Operator: query.OperatorGreater,
			Value: json.Number("1"),
		}},
		Limit: 10,
	})
	if err != nil ||
		jsonPage.FilteredRows != 2 ||
		!containsRowID(jsonPage.Rows, first.Id) ||
		!containsRowID(jsonPage.Rows, second.Id) {
		t.Fatalf("nested JSON numeric query: page=%#v err=%v", jsonPage, err)
	}

	relationPage, err := port.QueryPage(ctx, "orders", query.TableQuery{
		Filters: []query.FilterExpression{{
			Field: "watchers", Operator: query.OperatorEqual, Value: bob.Id,
		}},
		Limit: 10,
	})
	if err != nil || relationPage.FilteredRows != 2 {
		t.Fatalf("multi relation query: rows=%d err=%v", relationPage.FilteredRows, err)
	}

	injected, err := port.QueryPage(ctx, "orders", query.TableQuery{
		Filters: []query.FilterExpression{{
			Field: "name", Operator: query.OperatorEqual, Value: `' OR 1=1 --`,
		}},
		Limit: 10,
	})
	if err != nil || injected.FilteredRows != 0 || injected.TotalRows != 4 {
		t.Fatalf("injection value changed query semantics: %#v err=%v", injected, err)
	}

	_, err = port.QueryPage(ctx, "orders", query.TableQuery{
		Filters: []query.FilterExpression{{
			Field: "missing", Operator: query.OperatorEqual, Value: "x",
		}},
		Limit: 10,
	})
	var productErr *query.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "query.field.unknown" ||
		productErr.Path != "filters[0].field" {
		t.Fatalf("illegal field error = %#v", err)
	}

	aggregate, err := port.Aggregate(ctx, "orders", query.AggregateQuery{
		Filters: []query.FilterExpression{{
			Field: "watchers", Operator: query.OperatorEqual, Value: bob.Id,
		}},
		GroupBy: []string{"status"},
		Metrics: []query.AggregateMetric{
			{Function: query.AggregateCount, Alias: "row_count"},
			{Function: query.AggregateSum, Field: "amount", Alias: "total_amount"},
		},
	})
	if err != nil || len(aggregate.Rows) != 2 {
		t.Fatalf("Aggregate(): %#v err=%v", aggregate, err)
	}
	assertAggregateRows(t, aggregate.Rows)
	relationAggregate, err := port.Aggregate(ctx, "orders", query.AggregateQuery{
		GroupBy: []string{"customer.active"},
		Metrics: []query.AggregateMetric{{
			Function: query.AggregateCount, Alias: "row_count",
		}},
	})
	if err != nil {
		t.Fatalf("Aggregate(relation bool): %v", err)
	}
	boolGroups := 0
	for _, row := range relationAggregate.Rows {
		if row["customer.active"] == nil {
			continue
		}
		if _, ok := row["customer.active"].(bool); !ok {
			t.Fatalf("relation bool group was not decoded: %#v", row)
		}
		boolGroups++
	}
	if boolGroups != 2 {
		t.Fatalf("relation bool groups = %#v", relationAggregate.Rows)
	}
	_, err = port.Aggregate(ctx, "orders", query.AggregateQuery{
		GroupBy: []string{"status"},
		Metrics: []query.AggregateMetric{{
			Function: query.AggregateCount, Alias: "STATUS",
		}},
	})
	var aggregateErr *query.ProductError
	if !errors.As(err, &aggregateErr) ||
		aggregateErr.Code != "query.aggregate.duplicate_alias" {
		t.Fatalf("aggregate alias collision error = %#v", err)
	}

	readRows, err := port.ReadRows(ctx, "orders", []string{second.Id, first.Id})
	if err != nil || readRows[0]["id"] != second.Id || readRows[1]["id"] != first.Id {
		t.Fatalf("ReadRows order: %#v err=%v", readRows, err)
	}

	assertLogicalNulls(t, port, emptyRelations.Id)
	archivedPage, err := port.ReadRows(ctx, "orders", []string{archived.Id})
	if err != nil || len(archivedPage) != 0 {
		t.Fatalf("status archive leaked through ReadRows: %#v err=%v", archivedPage, err)
	}

	currentQuery := input
	currentQuery.Offset = 0
	validation, err := port.ValidateSnapshot(ctx, pageOne.Snapshot, &currentQuery)
	if err != nil || !validation.Valid {
		t.Fatalf("valid snapshot rejected: %#v err=%v", validation, err)
	}
	tampered := pageOne.Snapshot
	tampered.SnapshotID = "00000000000000000000000000000000"
	validation, err = port.ValidateSnapshot(ctx, tampered, nil)
	assertInvalidSnapshotError(t, validation, err)
	tampered = pageOne.Snapshot
	tampered.NormalizedQuery.Keyword = "forged"
	validation, err = port.ValidateSnapshot(ctx, tampered, nil)
	assertInvalidSnapshotError(t, validation, err)
	restartedPort := query.NewPort(
		app,
		source)

	validation, err = restartedPort.ValidateSnapshot(ctx, pageOne.Snapshot, nil)
	if err != nil || !validation.Valid {
		t.Fatalf("snapshot digest did not survive restart: %#v err=%v", validation, err)
	}
	repeated, err := port.QueryPage(ctx, "orders", currentQuery)
	if err != nil || repeated.Snapshot.SnapshotID == pageOne.Snapshot.SnapshotID {
		t.Fatalf("snapshot nonce was reused: %#v err=%v", repeated.Snapshot, err)
	}

	second.Set("deleted_at", "2026-07-24 12:00:00.000Z")
	if err := app.Save(second); err != nil {
		t.Fatalf("mark deletedAt archive: %v", err)
	}
	source.updateDescriptor(func(descriptor *query.TableDescriptor) {
		descriptor.ArchiveMode = query.ArchiveModeDeletedAt
		descriptor.ArchiveField = "deleted_at"
		descriptor.ArchiveValue = nil
		descriptor.DataRevision = 8
	})
	deletedAtPage, err := port.QueryPage(ctx, "orders", query.TableQuery{Limit: 10})
	if err != nil {
		t.Fatalf("deletedAt archive query: %v", err)
	}
	if containsRowID(deletedAtPage.Rows, second.Id) ||
		!containsRowID(deletedAtPage.Rows, archived.Id) ||
		deletedAtPage.TotalRows != 4 {
		t.Fatalf("deletedAt archive semantics: %#v", deletedAtPage)
	}
	source.updateDescriptor(func(descriptor *query.TableDescriptor) {
		descriptor.ArchiveMode = query.ArchiveModeStatus
		descriptor.ArchiveField = "archive_status"
		descriptor.ArchiveValue = "archived"
	})
	validation, err = port.ValidateSnapshot(ctx, pageOne.Snapshot, &currentQuery)
	if err != nil || validation.Valid || validation.Reason != "application_write" {
		t.Fatalf("stale snapshot accepted: %#v err=%v", validation, err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = port.QueryPage(cancelled, "orders", currentQuery)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query error = %#v", err)
	}
}

func TestQueryPortAggregateResultLimit(t *testing.T) {
	app := newBootstrappedApp(t)
	defer resetApp(t, app)
	if _, err := app.DB().NewQuery(
		"CREATE TABLE query_many_groups (id TEXT PRIMARY KEY, group_key TEXT NOT NULL)",
	).Execute(); err != nil {
		t.Fatalf("create aggregate groups: %v", err)
	}
	if _, err := app.DB().NewQuery(`
		WITH RECURSIVE sequence(value) AS (
			SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value < 1005
		)
		INSERT INTO query_many_groups (id, group_key)
		SELECT printf('id-%04d', value), printf('group-%04d', value) FROM sequence
	`).Execute(); err != nil {
		t.Fatalf("seed aggregate groups: %v", err)
	}
	source := &staticQuerySource{descriptor: query.TableDescriptor{
		DatabaseID: "local", TableID: "many", PhysicalName: "query_many_groups",
		PrimaryKey: "row_key", SchemaRevision: "schema-1", DataRevision: 1,
		Fields: map[string]query.FieldDescriptor{
			"row_key":   {PhysicalName: "id", Type: query.FieldTypeText},
			"group_key": {PhysicalName: "group_key", Type: query.FieldTypeText},
		},
	}}
	port := query.NewPort(
		app,
		source)

	input := query.AggregateQuery{
		GroupBy: []string{"group_key"},
		Metrics: []query.AggregateMetric{{
			Function: query.AggregateCount, Alias: "row_count",
		}},
	}
	result, err := port.Aggregate(context.Background(), "many", input)
	if err != nil || len(result.Rows) != 1000 {
		t.Fatalf("default aggregate cap: rows=%d err=%v", len(result.Rows), err)
	}
	input.Limit = 1005
	result, err = port.Aggregate(context.Background(), "many", input)
	if err != nil || len(result.Rows) != 1005 {
		t.Fatalf("explicit aggregate cap: rows=%d err=%v", len(result.Rows), err)
	}
	rows, err := port.ReadRows(
		context.Background(),
		"many",
		[]string{"id-0002", "id-0001"},
	)
	if err != nil ||
		rows[0]["row_key"] != "id-0002" ||
		rows[1]["row_key"] != "id-0001" {
		t.Fatalf("descriptor primary key order: rows=%#v err=%v", rows, err)
	}
}

func TestQueryPortPagesFiltersAndSortsTwentyFiveThousandRows(t *testing.T) {
	app := newBootstrappedApp(t)
	defer resetApp(t, app)
	if _, err := app.DB().NewQuery(`
		CREATE TABLE scale_query (
			id TEXT PRIMARY KEY NOT NULL,
			score INTEGER NOT NULL,
			group_name TEXT NOT NULL
		)
	`).Execute(); err != nil {
		t.Fatalf("create scale query table: %v", err)
	}
	if _, err := app.DB().NewQuery(`
		WITH RECURSIVE sequence(value) AS (
			SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value < 25000
		)
		INSERT INTO scale_query (id, score, group_name)
		SELECT
			printf('scale-%05d', value),
			value,
			CASE value % 2 WHEN 0 THEN 'even' ELSE 'odd' END
		FROM sequence
	`).Execute(); err != nil {
		t.Fatalf("seed 25k query rows: %v", err)
	}
	source := &staticQuerySource{descriptor: query.TableDescriptor{
		DatabaseID: "local", TableID: "scale", PhysicalName: "scale_query",
		PrimaryKey: "id", SchemaRevision: "schema-1", DataRevision: 1,
		Fields: map[string]query.FieldDescriptor{
			"id":         {PhysicalName: "id", Type: query.FieldTypeText},
			"score":      {PhysicalName: "score", Type: query.FieldTypeNumber},
			"group_name": {PhysicalName: "group_name", Type: query.FieldTypeText},
		},
	}}
	port := query.NewPort(
		app,
		source)

	input := query.TableQuery{
		Filters: []query.FilterExpression{{
			Field: "group_name", Operator: query.OperatorEqual, Value: "even",
		}},
		Sorts: []query.SortCondition{{
			Field: "score", Direction: query.SortDescending,
		}},
		Limit: 500,
	}
	startedAt := time.Now()
	seen := 0
	lastScore := 25_002
	for offset := 0; offset < 12_500; offset += input.Limit {
		input.Offset = offset
		page, err := port.QueryPage(context.Background(), "scale", input)
		if err != nil {
			t.Fatalf("query page at offset %d: %v", offset, err)
		}
		if page.TotalRows != 25_000 || page.FilteredRows != 12_500 ||
			len(page.Rows) != input.Limit {
			t.Fatalf(
				"page counts at offset %d: total=%d filtered=%d rows=%d",
				offset, page.TotalRows, page.FilteredRows, len(page.Rows),
			)
		}
		for _, row := range page.Rows {
			score64, ok := row["score"].(int64)
			if !ok {
				t.Fatalf("score type = %T", row["score"])
			}
			score := int(score64)
			if score >= lastScore || score%2 != 0 {
				t.Fatalf("sort/filter drift: score=%d previous=%d", score, lastScore)
			}
			lastScore = score
			seen++
		}
	}
	if seen != 12_500 || lastScore != 2 {
		t.Fatalf("paged rows=%d lastScore=%d", seen, lastScore)
	}
	t.Logf("25k filtered/sorted pagination completed in %s", time.Since(startedAt))
}

func TestQueryPageUsesOneConsistentTransaction(t *testing.T) {
	app := newBootstrappedApp(t)
	defer resetApp(t, app)
	if _, err := app.DB().NewQuery(
		"CREATE TABLE query_consistency (id TEXT PRIMARY KEY, name TEXT NOT NULL)",
	).Execute(); err != nil {
		t.Fatalf("create consistency table: %v", err)
	}
	if _, err := app.DB().NewQuery(`
		INSERT INTO query_consistency (id, name) VALUES
			('id-1', 'one'), ('id-2', 'two'), ('id-3', 'three')
	`).Execute(); err != nil {
		t.Fatalf("seed consistency table: %v", err)
	}
	source := &concurrentWriteSource{
		app: app,
		descriptor: query.TableDescriptor{
			DatabaseID: "local", TableID: "consistency", PhysicalName: "query_consistency",
			PrimaryKey: "id", SchemaRevision: "schema-1", DataRevision: 1,
			Fields: map[string]query.FieldDescriptor{
				"id":   {PhysicalName: "id", Type: query.FieldTypeText},
				"name": {PhysicalName: "name", Type: query.FieldTypeText},
			},
		},
		writeCommitted: make(chan error, 1),
		writeDone:      make(chan error, 1),
	}
	port := query.NewPort(
		app,
		source)

	page, err := port.QueryPage(
		context.Background(),
		"consistency",
		query.TableQuery{Limit: 10},
	)
	if err != nil {
		t.Fatalf("QueryPage(): %v", err)
	}
	select {
	case err := <-source.writeDone:
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent write remained blocked after query transaction")
	}
	if len(page.Rows) != 3 || page.FilteredRows != 3 || page.TotalRows != 3 {
		t.Fatalf("page mixed transaction snapshots: %#v", page)
	}
	validation, err := port.ValidateSnapshot(context.Background(), page.Snapshot, nil)
	if err != nil || validation.Valid || validation.Reason != "application_write" {
		t.Fatalf("concurrent write did not invalidate snapshot: %#v err=%v", validation, err)
	}
}

type staticQuerySource struct {
	mu         sync.RWMutex
	descriptor query.TableDescriptor
}

type concurrentWriteSource struct {
	app            core.App
	mu             sync.RWMutex
	once           sync.Once
	descriptor     query.TableDescriptor
	writeCommitted chan error
	writeDone      chan error
}

func (source *concurrentWriteSource) DescribeQueryTable(
	ctx context.Context,
	txApp core.App,
	_ string,
) (query.TableDescriptor, error) {
	var count int
	if err := txApp.ConcurrentDB().NewQuery(
		"SELECT COUNT(*) FROM query_consistency",
	).WithContext(ctx).Row(&count); err != nil {
		return query.TableDescriptor{}, err
	}
	source.mu.RLock()
	descriptor := source.descriptor
	source.mu.RUnlock()
	var concurrentWriteErr error
	source.once.Do(func() {
		go func() {
			_, err := source.app.ConcurrentDB().NewQuery(
				"INSERT INTO query_consistency (id, name) VALUES ('id-4', 'four')",
			).Execute()
			if err == nil {
				source.mu.Lock()
				source.descriptor.DataRevision = 2
				source.mu.Unlock()
			}
			source.writeCommitted <- err
			source.writeDone <- err
		}()
		// This is the transaction-test barrier: the external write must have
		// committed after the first transactional read, but before QueryPage
		// executes its rows/count/total reads.
		concurrentWriteErr = <-source.writeCommitted
	})
	if concurrentWriteErr != nil {
		return query.TableDescriptor{}, concurrentWriteErr
	}
	return descriptor, nil
}

func (source *staticQuerySource) DescribeQueryTable(
	context.Context,
	core.App,
	string,
) (query.TableDescriptor, error) {
	source.mu.RLock()
	defer source.mu.RUnlock()
	return source.descriptor, nil
}

func (source *staticQuerySource) setDataRevision(revision int64) {
	source.updateDescriptor(func(descriptor *query.TableDescriptor) {
		descriptor.DataRevision = revision
	})
}

func (source *staticQuerySource) updateDescriptor(
	update func(*query.TableDescriptor),
) {
	source.mu.Lock()
	defer source.mu.Unlock()
	update(&source.descriptor)
}

func assertLogicalNulls(
	t *testing.T,
	port query.QueryPort,
	emptyRowID string,
) {
	t.Helper()
	for _, field := range []string{"notes", "customer", "watchers", "payload"} {
		page, err := port.QueryPage(context.Background(), "orders", query.TableQuery{
			Filters: []query.FilterExpression{{
				Field: field, Operator: query.OperatorIsNull,
			}},
			Limit: 10,
		})
		if err != nil || !containsRowID(page.Rows, emptyRowID) {
			t.Fatalf("logical null %s: rows=%#v err=%v", field, page.Rows, err)
		}
	}
}

func assertAggregateRows(t *testing.T, rows []map[string]any) {
	t.Helper()
	got := make(map[string]string, len(rows))
	for _, row := range rows {
		got[fmt.Sprint(row["status"])] = fmt.Sprintf(
			"%v/%v",
			row["row_count"],
			row["total_amount"],
		)
	}
	if got["closed"] != "1/100" || got["open"] != "1/100" {
		t.Fatalf("aggregate values = %#v", rows)
	}
}

func assertInvalidSnapshotError(
	t *testing.T,
	validation query.SnapshotValidation,
	err error,
) {
	t.Helper()
	var productErr *query.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "query.snapshot.invalid" ||
		productErr.Path == "" ||
		validation != (query.SnapshotValidation{}) {
		t.Fatalf(
			"invalid snapshot result=%#v error=%#v, want query.snapshot.invalid",
			validation,
			err,
		)
	}
}

func containsRowID(rows []map[string]any, id string) bool {
	for _, row := range rows {
		if fmt.Sprint(row["id"]) == id {
			return true
		}
	}
	return false
}

func newBootstrappedApp(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: queryTempDir(t), HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap(): %v", err)
	}
	return app
}

func queryTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "vibetable-query-port-*")
	if err != nil {
		t.Fatalf("create query temp dir: %v", err)
	}
	t.Cleanup(func() {
		var cleanupErr error
		for range 20 {
			cleanupErr = os.RemoveAll(directory)
			if cleanupErr == nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Errorf("remove query temp dir: %v", cleanupErr)
	})
	return directory
}

func saveRecord(
	t *testing.T,
	app core.App,
	collection *core.Collection,
	values map[string]any,
) *core.Record {
	t.Helper()
	record := core.NewRecord(collection)
	for name, value := range values {
		record.Set(name, value)
	}
	if err := app.Save(record); err != nil {
		t.Fatalf("save %s record: %v", collection.Name, err)
	}
	return record
}
