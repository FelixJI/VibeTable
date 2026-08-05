package query_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/query"
)

func TestNormalizeEnforcesProductFilterTreeBudgets(t *testing.T) {
	fiftyOne := make([]query.FilterExpression, 51)
	for index := range fiftyOne {
		fiftyOne[index] = query.FilterExpression{
			Field: "amount", Operator: query.OperatorEqual, Value: index,
		}
	}
	tooDeep := query.FilterExpression{Filters: []query.FilterExpression{{
		Filters: []query.FilterExpression{{
			Filters: []query.FilterExpression{{
				Filters: []query.FilterExpression{{
					Field: "amount", Operator: query.OperatorEqual, Value: 1,
				}},
			}},
		}},
	}}}
	for name, filters := range map[string][]query.FilterExpression{
		"more than 50 predicates":  {{Filters: fiftyOne}},
		"more than 3 group levels": {tooDeep},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := query.Normalize(query.TableQuery{Filters: filters})
			var productErr *query.ProductError
			if !errors.As(err, &productErr) ||
				(productErr.Code != "query.filter.limit" && productErr.Code != "query.filter.depth") {
				t.Fatalf("Normalize() error = %#v", err)
			}
		})
	}
}

func TestCompilerMatchesGoldenOperatorsAndParameterizesValues(t *testing.T) {
	descriptor := descriptorFixture()
	input := query.TableQuery{
		Filters: []query.FilterExpression{
			{Field: "name", Operator: query.OperatorContains, Value: "abc", Logic: query.LogicAnd},
			{Field: "amount", Operator: query.OperatorGreater, Value: float64(100), Logic: query.LogicAnd},
			{Field: "status", Operator: query.OperatorIn, Value: []any{"draft", "open"}, Logic: query.LogicOr},
			{Field: "created_at", Operator: query.OperatorBetween, Value: []any{"2026-01-01T00:00:00Z", "2026-12-31T23:59:59Z"}, Logic: query.LogicAnd},
			{Field: "notes", Operator: query.OperatorIsNull, Logic: query.LogicAnd},
		},
		Sorts:  []query.SortCondition{{Field: "amount", Direction: query.SortDescending, NullsLast: boolPtr(true)}},
		Offset: 0,
		Limit:  100,
	}

	plan, err := query.Compile(descriptor, input)
	if err != nil {
		t.Fatalf("Compile(): %v", err)
	}
	if strings.Contains(plan.SQL, "2026-01-01") || strings.Contains(plan.SQL, "draft") {
		t.Fatalf("compiled SQL embedded a user value: %s", plan.SQL)
	}
	if len(plan.Params) != 8 {
		t.Fatalf("parameter count = %d, want 8 including page bounds (%#v)", len(plan.Params), plan.Params)
	}
	if !strings.Contains(plan.SQL, `ORDER BY "amount" IS NULL ASC, "amount" DESC, "id" ASC`) {
		t.Fatalf("stable sort is missing: %s", plan.SQL)
	}
}

func TestPresenceCompanionControlsFilterSortAndAggregateSemantics(t *testing.T) {
	descriptor := descriptorFixture()
	descriptor.PresenceFields = map[string]string{"amount": "__present_amount"}

	plan, err := query.Compile(descriptor, query.TableQuery{
		Filters: []query.FilterExpression{{
			Field: "amount", Operator: query.OperatorEqual, Value: float64(0),
		}},
		Sorts: []query.SortCondition{{
			Field: "amount", Direction: query.SortAscending,
		}},
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("Compile(presence query): %v", err)
	}
	productSQL := `(CASE WHEN "__present_amount" THEN "amount" ELSE NULL END)`
	if !strings.Contains(plan.SQL, productSQL+" = ") {
		t.Fatalf("explicit zero filter bypassed presence: %s", plan.SQL)
	}
	if !strings.Contains(plan.SQL, productSQL+" IS NULL ASC") {
		t.Fatalf("null ordering bypassed presence: %s", plan.SQL)
	}

	aggregateSQL, _, err := query.CompileAggregate(descriptor, query.AggregateQuery{
		Metrics: []query.AggregateMetric{{
			Alias: "total_amount", Function: query.AggregateSum, Field: "amount",
		}},
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("CompileAggregate(presence): %v", err)
	}
	if !strings.Contains(aggregateSQL, "SUM("+productSQL+")") {
		t.Fatalf("aggregate bypassed presence: %s", aggregateSQL)
	}
}

func TestCompilerNormalizesDateTimeOffsetsToPocketBaseUTC(t *testing.T) {
	plan, err := query.Compile(autoDateDescriptorFixture(), query.TableQuery{
		Filters: []query.FilterExpression{
			{
				Field: "created_at", Operator: query.OperatorEqual,
				Value: "2026-07-24T14:30:00+02:00", Logic: query.LogicAnd,
			},
			{
				Field: "created_at", Operator: query.OperatorBetween,
				Value: []any{
					"2026-07-24T12:30:00Z",
					"2026-07-24T12:30:00.000Z",
				},
				Logic: query.LogicAnd,
			},
		},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Compile(): %v", err)
	}
	var timestampCount int
	for _, value := range plan.Params {
		if value == "2026-07-24 12:30:00.000Z" {
			timestampCount++
		}
	}
	if timestampCount != 3 {
		t.Fatalf("normalized timestamp params = %#v", plan.Params)
	}
}

func TestCompilerRejectsInvalidDateFilterText(t *testing.T) {
	for _, value := range []string{
		"not-a-date",
		"2026-07-24",
		"2026-07-24 12:30:00.000Z",
	} {
		_, err := query.Compile(autoDateDescriptorFixture(), query.TableQuery{
			Filters: []query.FilterExpression{{
				Field: "created_at", Operator: query.OperatorEqual,
				Value: value, Logic: query.LogicAnd,
			}},
			Limit: 10,
		})
		assertProductError(t, err, "query.filter.invalid_value")
	}
}

func TestCompilerRejectsUnknownFieldAndInjectionPayload(t *testing.T) {
	descriptor := descriptorFixture()
	_, err := query.Compile(descriptor, query.TableQuery{
		Filters: []query.FilterExpression{{
			Field: "name; DROP TABLE orders", Operator: query.OperatorEqual, Value: "x",
		}},
		Limit: 10,
	})
	productErr, ok := err.(*query.ProductError)
	if !ok || productErr.Code != "query.field.unknown" || productErr.Path != "filters[0].field" {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestExistingTableQueryGoldenDecodesWithEquivalentSemantics(t *testing.T) {
	goldenPath := filepath.Join(
		"..", "..", "..", "tests", "contract", "fixtures", "table-query.json",
	)
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read existing query golden %s: %v", goldenPath, err)
	}
	var input query.TableQuery
	if err := json.Unmarshal(golden, &input); err != nil {
		t.Fatalf("decode existing golden: %v", err)
	}
	plan, err := query.Compile(descriptorFixture(), input)
	if err != nil {
		t.Fatalf("compile existing golden: %v", err)
	}
	if len(input.Filters) != 5 || len(plan.Params) != 8 {
		t.Fatalf("golden semantics changed: filters=%d params=%#v", len(input.Filters), plan.Params)
	}
	wantSQL := `SELECT "amount" AS "amount", "created_at" AS "created_at", "id" AS "id", "name" AS "name", "notes" AS "notes", "status" AS "status" FROM "orders" WHERE ((((instr(CAST("name" AS TEXT), {:p0}) > 0) AND ("amount" > {:p1})) OR ("status" IN ({:p2}, {:p3}))) AND ("created_at" BETWEEN {:p4} AND {:p5})) AND (("notes" IS NULL OR "notes" = '')) ORDER BY "amount" IS NULL ASC, "amount" DESC, "id" ASC LIMIT {:limit} OFFSET {:offset}`
	if plan.SQL != wantSQL {
		t.Fatalf("golden SQL changed:\n got: %s\nwant: %s", plan.SQL, wantSQL)
	}
	if plan.CountSQL != `SELECT COUNT(*) FROM "orders" WHERE ((((instr(CAST("name" AS TEXT), {:p0}) > 0) AND ("amount" > {:p1})) OR ("status" IN ({:p2}, {:p3}))) AND ("created_at" BETWEEN {:p4} AND {:p5})) AND (("notes" IS NULL OR "notes" = ''))` {
		t.Fatalf("golden count SQL changed: %s", plan.CountSQL)
	}
}

func TestCompilerRejectsConflictingNestedLogicAndTypeCoercion(t *testing.T) {
	descriptor := descriptorFixture()
	_, err := query.Compile(descriptor, query.TableQuery{
		Filters: []query.FilterExpression{{
			Filters: []query.FilterExpression{
				{Field: "name", Operator: query.OperatorEqual, Value: "a"},
				{Field: "status", Operator: query.OperatorEqual, Value: "open", Logic: query.LogicOr},
			},
			GroupLogic: query.LogicAnd,
		}},
		Limit: 10,
	})
	assertProductError(t, err, "query.filter.conflicting_group_logic")

	_, err = query.Compile(descriptor, query.TableQuery{
		Filters: []query.FilterExpression{{
			Field: "amount", Operator: query.OperatorGreater, Value: "100",
		}},
		Limit: 10,
	})
	assertProductError(t, err, "query.filter.invalid_value")

	_, err = query.Compile(descriptor, query.TableQuery{
		Filters: []query.FilterExpression{{
			Field: "name", Operator: query.OperatorGreater, Value: "a",
		}},
		Limit: 10,
	})
	assertProductError(t, err, "query.operator.type_mismatch")
}

func TestCompilerSupportsStrictJSONScalarOperators(t *testing.T) {
	descriptor := descriptorFixture()
	descriptor.Fields["payload"] = query.FieldDescriptor{
		PhysicalName: "payload", Type: query.FieldTypeJSON,
	}

	plan, err := query.Compile(descriptor, query.TableQuery{
		Filters: []query.FilterExpression{
			{
				Field: "payload.nested.rank", Operator: query.OperatorGreater,
				Value: json.Number("9007199254740993"),
			},
			{
				Field: "payload.nested.label", Operator: query.OperatorStartsWith,
				Value: "vip",
			},
			{
				Field: "payload.nested.enabled", Operator: query.OperatorIn,
				Value: []any{true, false}, Logic: query.LogicAnd,
			},
		},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Compile(JSON scalar filters): %v", err)
	}
	if got := plan.Params["p0"]; got != int64(9007199254740993) {
		t.Fatalf("large JSON number was not preserved: %#v", got)
	}
	for _, fragment := range []string{
		`json_type("payload", '$.nested.rank') IN ('integer', 'real')`,
		`json_type("payload", '$.nested.label') = 'text'`,
		`json_type("payload", '$.nested.enabled') IN ('true', 'false')`,
	} {
		if !strings.Contains(plan.SQL, fragment) {
			t.Fatalf("JSON scalar type guard %q missing from %s", fragment, plan.SQL)
		}
	}
	for name, filter := range map[string]query.FilterExpression{
		"equal": {
			Field: "payload.nested.label", Operator: query.OperatorEqual, Value: "vip",
		},
		"not equal": {
			Field: "payload.nested.enabled", Operator: query.OperatorNotEqual, Value: false,
		},
		"between": {
			Field: "payload.nested.rank", Operator: query.OperatorBetween,
			Value: []any{json.Number("1"), json.Number("3")},
		},
		"ends with": {
			Field: "payload.nested.label", Operator: query.OperatorEndsWith, Value: "customer",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := query.Compile(descriptor, query.TableQuery{
				Filters: []query.FilterExpression{filter},
				Limit:   10,
			}); err != nil {
				t.Fatalf("Compile(%s): %v", name, err)
			}
		})
	}

	for name, filter := range map[string]query.FilterExpression{
		"numeric comparison rejects string": {
			Field: "payload.nested.rank", Operator: query.OperatorGreater, Value: "2",
		},
		"string operation rejects number": {
			Field: "payload.nested.label", Operator: query.OperatorContains, Value: 2,
		},
		"membership rejects mixed scalar types": {
			Field: "payload.nested.value", Operator: query.OperatorIn,
			Value: []any{"2", float64(2)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := query.Compile(descriptor, query.TableQuery{
				Filters: []query.FilterExpression{filter},
				Limit:   10,
			})
			assertProductError(t, err, "query.filter.invalid_value")
		})
	}
}

func TestCompilerSupportsWholeJSONTextContainmentForHeaderFilters(t *testing.T) {
	descriptor := descriptorFixture()
	descriptor.Fields["payload"] = query.FieldDescriptor{
		PhysicalName: "payload", Type: query.FieldTypeJSON,
	}
	plan, err := query.Compile(descriptor, query.TableQuery{
		Filters: []query.FilterExpression{{
			Field: "payload", Operator: query.OperatorContains, Value: "8",
		}},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Compile(whole JSON contains): %v", err)
	}
	if !strings.Contains(
		plan.SQL,
		`instr(CAST("payload" AS TEXT), {:p0}) > 0`,
	) || plan.Params["p0"] != "8" {
		t.Fatalf("whole JSON containment plan = %s, %#v", plan.SQL, plan.Params)
	}
}

func TestNormalizeCanonicalizesEquivalentSorts(t *testing.T) {
	implicit, err := query.Normalize(query.TableQuery{
		Sorts: []query.SortCondition{
			{Field: "name"},
			{Field: "name", Direction: query.SortDescending, NullsLast: boolPtr(false)},
		},
	})
	if err != nil {
		t.Fatalf("Normalize(implicit): %v", err)
	}
	explicit, err := query.Normalize(query.TableQuery{
		Sorts: []query.SortCondition{{
			Field: "name", Direction: query.SortAscending, NullsLast: boolPtr(true),
		}},
	})
	if err != nil {
		t.Fatalf("Normalize(explicit): %v", err)
	}
	if len(implicit.Sorts) != 1 ||
		implicit.Sorts[0].NullsLast == nil ||
		!*implicit.Sorts[0].NullsLast {
		t.Fatalf("implicit sorts were not canonicalized: %#v", implicit.Sorts)
	}
	implicitJSON, _ := json.Marshal(implicit)
	explicitJSON, _ := json.Marshal(explicit)
	if string(implicitJSON) != string(explicitJSON) {
		t.Fatalf(
			"equivalent sorts normalized differently:\nimplicit=%s\nexplicit=%s",
			implicitJSON,
			explicitJSON,
		)
	}
}

func TestCompilerArchiveModesAreExplicitAndFailClosed(t *testing.T) {
	descriptor := descriptorFixture()
	descriptor.Fields["archive_status"] = query.FieldDescriptor{
		PhysicalName: "archive_status", Type: query.FieldTypeText,
	}
	descriptor.ArchiveMode = query.ArchiveModeStatus
	descriptor.ArchiveField = "archive_status"
	descriptor.ArchiveValue = "archived"
	plan, err := query.Compile(descriptor, query.TableQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Compile(status archive): %v", err)
	}
	if !strings.Contains(
		plan.SQL,
		`("archive_status" IS NULL OR "archive_status" = '' OR "archive_status" != {:p0})`,
	) {
		t.Fatalf("status archive predicate missing: %s", plan.SQL)
	}

	descriptor.ArchiveMode = query.ArchiveModeDeletedAt
	descriptor.ArchiveField = "missing"
	_, err = query.Compile(descriptor, query.TableQuery{Limit: 10})
	assertProductError(t, err, "query.archive.invalid")
}

func TestAggregateCompilerRejectsOutputCollisionsAndBoundsRows(t *testing.T) {
	descriptor := descriptorFixture()
	tests := []struct {
		name  string
		input query.AggregateQuery
		code  string
	}{
		{
			name: "duplicate group",
			input: query.AggregateQuery{
				GroupBy: []string{"status", "status"},
				Metrics: []query.AggregateMetric{{Function: query.AggregateCount, Alias: "count"}},
			},
			code: "query.aggregate.duplicate_group",
		},
		{
			name: "group alias collision",
			input: query.AggregateQuery{
				GroupBy: []string{"status"},
				Metrics: []query.AggregateMetric{{Function: query.AggregateCount, Alias: "status"}},
			},
			code: "query.aggregate.duplicate_alias",
		},
		{
			name: "duplicate metric alias",
			input: query.AggregateQuery{
				Metrics: []query.AggregateMetric{
					{Function: query.AggregateCount, Alias: "count"},
					{Function: query.AggregateCount, Alias: "count"},
				},
			},
			code: "query.aggregate.duplicate_alias",
		},
		{
			name: "invalid metric type",
			input: query.AggregateQuery{
				Metrics: []query.AggregateMetric{{
					Function: query.AggregateSum, Field: "name", Alias: "sum_name",
				}},
			},
			code: "query.aggregate.type_mismatch",
		},
		{
			name: "limit too large",
			input: query.AggregateQuery{
				Metrics: []query.AggregateMetric{{Function: query.AggregateCount, Alias: "count"}},
				Limit:   5001,
			},
			code: "query.aggregate.invalid_limit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := query.CompileAggregate(descriptor, test.input)
			assertProductError(t, err, test.code)
		})
	}

	sql, params, err := query.CompileAggregate(descriptor, query.AggregateQuery{
		GroupBy: []string{"status"},
		Metrics: []query.AggregateMetric{{Function: query.AggregateCount, Alias: "count"}},
	})
	if err != nil {
		t.Fatalf("CompileAggregate(default limit): %v", err)
	}
	if !strings.HasSuffix(sql, " LIMIT {:aggregate_limit}") || params["aggregate_limit"] != 1000 {
		t.Fatalf("aggregate limit missing: sql=%s params=%#v", sql, params)
	}
}

func assertProductError(t *testing.T, err error, code string) {
	t.Helper()
	productErr, ok := err.(*query.ProductError)
	if !ok || productErr.Code != code {
		t.Fatalf("error = %#v, want %s", err, code)
	}
}

func descriptorFixture() query.TableDescriptor {
	return query.TableDescriptor{
		TableID: "orders", PhysicalName: "orders", PrimaryKey: "id",
		Fields: map[string]query.FieldDescriptor{
			"id":     {PhysicalName: "id", Type: query.FieldTypeText},
			"name":   {PhysicalName: "name", Type: query.FieldTypeText, Searchable: true},
			"amount": {PhysicalName: "amount", Type: query.FieldTypeNumber},
			"status": {PhysicalName: "status", Type: query.FieldTypeText},
			"created_at": {
				PhysicalName: "created_at",
				Type:         query.FieldTypeDate,
			},
			"notes": {PhysicalName: "notes", Type: query.FieldTypeText},
		},
	}
}

func autoDateDescriptorFixture() query.TableDescriptor {
	descriptor := descriptorFixture()
	field := descriptor.Fields["created_at"]
	field.AutoDate = true
	descriptor.Fields["created_at"] = field
	return descriptor
}

func boolPtr(value bool) *bool { return &value }
