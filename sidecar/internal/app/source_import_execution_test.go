package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/sourceimport"
)

func sourceExecutionSnapshot() sourceimport.Snapshot {
	text := func(id, name string) sourceimport.Field {
		return sourceimport.Field{ID: id, Name: name, Kind: "text", ValueKind: v2.LogicalText}
	}
	relation := func(id, target, reverse, cardinality string) sourceimport.Field {
		return sourceimport.Field{ID: id, Name: id, Kind: "relation", ValueKind: v2.LogicalRelation,
			Relation: &sourceimport.Relation{TargetTableID: target, TargetFieldID: reverse, Cardinality: cardinality}}
	}
	row := func(id, code string, links map[string]any) sourceimport.Record {
		values := map[string]any{"title": "重复显示值", "code": code}
		for field, value := range links {
			values[field] = value
		}
		return sourceimport.Record{ID: id, Values: values}
	}
	return sourceimport.Snapshot{
		Contract: sourceimport.Contract, Provider: "synthetic", ContainerID: "container-1", DisplayName: "合成迁移", Version: "v1",
		ReadWindow: sourceimport.ReadWindow{StartedAt: "2026-10-04T08:00:00Z", FinishedAt: "2026-10-04T08:01:00Z", Consistency: "snapshot"},
		Tables: []sourceimport.Table{
			{ID: "a", Name: "同名来源表", Version: "a1", PrimaryFieldID: "title",
				Fields: []sourceimport.Field{text("title", "名称"), text("code", "编码"), relation("ab", "b", "ba", "many")},
				Records: []sourceimport.Record{
					row("r1", "a-001", map[string]any{"ab": []string{"r1", "r2"}}),
					row("r2", "a-002", map[string]any{"ab": []string{"r2"}}),
				}},
			{ID: "b", Name: "同名来源表", Version: "b1", PrimaryFieldID: "title",
				Fields: []sourceimport.Field{text("title", "名称"), text("code", "编码"), relation("ba", "a", "ab", "many"), relation("bc", "c", "", "one")},
				Records: []sourceimport.Record{
					row("r1", "b-001", map[string]any{"ba": []string{"r1"}, "bc": "r1"}),
					row("r2", "b-002", map[string]any{"ba": []string{"r1", "r2"}, "bc": "r2"}),
				}},
			{ID: "c", Name: "同名来源表", Version: "c1", PrimaryFieldID: "title",
				Fields: []sourceimport.Field{text("title", "名称"), text("code", "编码"), relation("ca", "a", "", "one")},
				Records: []sourceimport.Record{
					row("r1", "c-001", map[string]any{"ca": "r2"}),
					row("r2", "c-002", map[string]any{"ca": "r1"}),
				}},
		},
	}
}

func sourceExecutionOrdinarySnapshot(count int) sourceimport.Snapshot {
	source := sourceExecutionSnapshot()
	for index := range source.Tables {
		table := &source.Tables[index]
		table.Fields = table.Fields[:2]
		rows := 1
		if table.ID == "a" {
			rows = count
		}
		table.Records = make([]sourceimport.Record, rows)
		for row := range table.Records {
			table.Records[row] = sourceimport.Record{ID: fmt.Sprintf("r%04d", row),
				Values: map[string]any{"title": "重复显示值", "code": fmt.Sprintf("%s-%04d", table.ID, row)}}
		}
	}
	return source
}

func sourceExecutionPreview(t *testing.T, source sourceimport.Snapshot, existing []string) sourceimport.Plan {
	t.Helper()
	options := sourceimport.Options{SelectedTableIDs: []string{"c", "a", "b"}, ConfirmReverse: true,
		TargetNames: []sourceimport.TargetName{{TableID: "a", Name: "迁移 A"}, {TableID: "b", Name: "迁移 B"}, {TableID: "c", Name: "迁移 C"}}}
	plan, err := sourceimport.Preview(context.Background(), source, options, existing)
	if err != nil || !plan.CanApply {
		t.Fatalf("synthetic preview blocked: %v, %#v", err, plan.Diagnostics)
	}
	return plan
}

type sourceExecutionProbe struct {
	sourceimport.Authority
	requests       []mutation.Request
	recordCalls    int
	relationCalls  int
	rejectRecord   int
	rejectRelation bool
	loseRecordAck  int
	beforeMutation func(mutation.Request)
	afterCommit    func(mutation.Request)
}

func (probe *sourceExecutionProbe) Mutate(ctx context.Context, request mutation.Request, commit func(core.App, mutation.Receipt) error) (mutation.Receipt, error) {
	probe.requests = append(probe.requests, request)
	if probe.beforeMutation != nil {
		probe.beforeMutation(request)
	}
	isRecord := request.Operations[0].Kind == mutation.OperationInsert
	if isRecord {
		probe.recordCalls++
		if probe.recordCalls == probe.rejectRecord {
			return mutation.Receipt{}, errors.New("synthetic record batch rejected before submit")
		}
	} else {
		probe.relationCalls++
		if probe.rejectRelation {
			return mutation.Receipt{}, errors.New("synthetic relation batch rejected before submit")
		}
	}
	receipt, err := probe.Authority.Mutate(ctx, request, commit)
	if err != nil {
		return receipt, err
	}
	if probe.afterCommit != nil {
		probe.afterCommit(request)
	}
	if isRecord && probe.recordCalls == probe.loseRecordAck {
		return mutation.Receipt{}, errors.New("synthetic response lost after commit")
	}
	return receipt, nil
}

type sourceExecutionReadFault struct {
	sourceimport.Journal
	failKey string
	failed  bool
}

func (journal *sourceExecutionReadFault) FindBatch(ctx context.Context, job, batch string) (sourceimport.Batch, bool, error) {
	if batch == journal.failKey && !journal.failed {
		journal.failed = true
		return sourceimport.Batch{}, false, errors.New("synthetic receipt read unavailable")
	}
	return journal.Journal.FindBatch(ctx, job, batch)
}

// Read actual PocketBase business facts independently of the import journal.
func sourceExecutionFacts(t *testing.T, app core.App) map[string]int {
	t.Helper()
	facts := map[string]int{}
	for _, name := range []string{"vibetable_tables", "vibetable_fields", "vibetable_relations", "vibetable_audit_events", "vibetable_outbox"} {
		records, err := app.FindAllRecords(name)
		if err != nil {
			t.Fatal(err)
		}
		facts[name] = len(records)
	}
	tables, err := app.FindAllRecords("vibetable_tables")
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		rows, err := app.FindAllRecords(table.GetString("collection_id"))
		if err != nil {
			t.Fatal(err)
		}
		facts[table.GetString("table_id")+"/rows"] = len(rows)
		facts[table.GetString("table_id")+"/data"] = table.GetInt("data_revision")
	}
	return facts
}

func sourceExecutionMappings(t *testing.T, result sourceimport.Result) map[sourceimport.Key]sourceimport.Mapping {
	t.Helper()
	mappings := map[sourceimport.Key]sourceimport.Mapping{}
	for _, batch := range result.Batches {
		for _, mapping := range batch.Mappings {
			if _, exists := mappings[mapping.Source]; exists {
				t.Fatalf("duplicate composite mapping: %#v", mapping.Source)
			}
			mappings[mapping.Source] = mapping
		}
	}
	return mappings
}

func sourceExecutionAssertRows(t *testing.T, fixture *sourceImportAuthorityFixture, source sourceimport.Snapshot, result sourceimport.Result) {
	t.Helper()
	mappings := sourceExecutionMappings(t, result)
	key := func(table, field, row string) sourceimport.Key {
		return sourceimport.Key{Provider: source.Provider, ContainerID: source.ContainerID, TableID: table, FieldID: field, RecordID: row}
	}
	for _, table := range source.Tables {
		localTable := mappings[key(table.ID, "", "")].LocalID
		metadata, err := fixture.app.FindFirstRecordByData("vibetable_tables", "table_id", localTable)
		if err != nil {
			t.Fatalf("target mapping did not resolve: %s: %v", table.ID, err)
		}
		if mappings[key(table.ID, "", "")].Collection != metadata.GetString("physical_name") {
			t.Fatal("management target did not retain the authoritative physical collection")
		}
		snapshot, err := fixture.authority.Describe(context.Background(), localTable)
		if err != nil {
			t.Fatal(err)
		}
		definitions := map[string]v2.FieldDefinition{}
		for _, definition := range snapshot.Fields {
			definitions[definition.Identity.FieldID] = definition
		}
		rows, err := fixture.app.FindAllRecords(metadata.GetString("collection_id"))
		if err != nil || len(rows) != len(table.Records) {
			t.Fatalf("target row count for %s = %d, want %d: %v", table.ID, len(rows), len(table.Records), err)
		}
		for _, row := range table.Records {
			local, err := fixture.app.FindRecordById(metadata.GetString("collection_id"), mappings[key(table.ID, "", row.ID)].LocalID)
			if err != nil {
				t.Fatalf("composite record mapping %s/%s failed: %v", table.ID, row.ID, err)
			}
			for _, field := range table.Fields {
				definition, found := definitions[mappings[key(table.ID, field.ID, "")].LocalID]
				if !found {
					t.Fatalf("field mapping missing: %s/%s", table.ID, field.ID)
				}
				if definition.Value.Required != field.Required {
					t.Fatalf("required constraint was not restored: %s/%s", table.ID, field.ID)
				}
				if field.Relation == nil {
					if actual := local.GetString(definition.Identity.PhysicalName); actual != row.Values[field.ID] {
						t.Fatalf("record identity crossed tables: %s/%s/%s = %q, want %v", table.ID, row.ID, field.ID, actual, row.Values[field.ID])
					}
					continue
				}
				var sourceRefs []string
				switch value := row.Values[field.ID].(type) {
				case string:
					sourceRefs = []string{value}
				case []string:
					sourceRefs = value
				default:
					t.Fatalf("invalid synthetic relation value: %T", value)
				}
				want := make([]string, len(sourceRefs))
				for index, ref := range sourceRefs {
					want[index] = mappings[key(field.Relation.TargetTableID, "", ref)].LocalID
					if want[index] == "" {
						t.Fatal("target record mapping is missing")
					}
				}
				actual := local.GetStringSlice(definition.Identity.PhysicalName)
				sort.Strings(want)
				sort.Strings(actual)
				if !reflect.DeepEqual(actual, want) {
					t.Fatalf("relation changed identity: %s/%s/%s = %v, want %v", table.ID, row.ID, field.ID, actual, want)
				}
			}
		}
	}
}

func sourceExecutionAssertCounts(t *testing.T, fixture *sourceImportAuthorityFixture, result sourceimport.Result, created, unknown, pending int) {
	t.Helper()
	if result.Created != created || result.UnknownRecords != unknown || result.NotSubmitted != pending || len(result.Targets) != 3 {
		t.Fatalf("incorrect partial outcome: state=%s created=%d unknown=%d pending=%d targets=%d; want %d/%d/%d/3",
			result.State, result.Created, result.UnknownRecords, result.NotSubmitted, len(result.Targets), created, unknown, pending)
	}
	actual := 0
	for _, target := range result.Targets {
		metadata, err := fixture.app.FindFirstRecordByData("vibetable_tables", "table_id", target.TableID)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := fixture.app.FindAllRecords(metadata.GetString("collection_id"))
		if err != nil {
			t.Fatal(err)
		}
		actual += len(rows)
	}
	if actual != created {
		t.Fatalf("durable counts differ from actual PocketBase rows: actual=%d reported=%d", actual, created)
	}
}

func TestSourceImportExecutionPreviewsWithoutWritesAndPreservesCompositeRelations(t *testing.T) {
	for _, reordered := range []bool{false, true} {
		t.Run(fmt.Sprintf("reordered=%v", reordered), func(t *testing.T) {
			fixture := newSourceImportAuthorityFixture(t)
			existing := fixture.field(t, fixture.table(t, "existing"))
			if _, err := fixture.authority.Mutate(context.Background(), sourceImportInsert(existing, "existing-row"), nil); err != nil {
				t.Fatal(err)
			}
			source := sourceExecutionSnapshot()
			if reordered {
				source.Tables[0], source.Tables[2] = source.Tables[2], source.Tables[0]
				for index := range source.Tables {
					table := &source.Tables[index]
					for left, right := 0, len(table.Fields)-1; left < right; left, right = left+1, right-1 {
						table.Fields[left], table.Fields[right] = table.Fields[right], table.Fields[left]
					}
					table.Records[0], table.Records[1] = table.Records[1], table.Records[0]
				}
			}
			before := sourceExecutionFacts(t, fixture.app)
			plan := sourceExecutionPreview(t, source, []string{"existing"})
			if after := sourceExecutionFacts(t, fixture.app); !reflect.DeepEqual(before, after) {
				t.Fatalf("preview changed business authority: before=%v after=%v", before, after)
			}
			journal := sourceimport.NewJournal(fixture.app)
			probe := &sourceExecutionProbe{Authority: fixture.authority}
			probe.afterCommit = func(mutation.Request) {
				intermediate, err := journal.Read(context.Background(), "cycle-job")
				if err != nil || intermediate.State == "succeeded" {
					t.Fatalf("job succeeded before all stages settled: %#v, %v", intermediate, err)
				}
			}
			result, err := sourceimport.NewExecutor(probe, journal, nil).Execute(context.Background(), plan, "cycle-job", 7)
			if err != nil || result.State != "succeeded" || result.Stage != "settled" || probe.relationCalls == 0 {
				t.Fatalf("cycle migration outcome: %#v, %v", result, err)
			}
			sourceExecutionAssertCounts(t, fixture, result, 6, 0, 0)
			sourceExecutionAssertRows(t, fixture, source, result)
			after := sourceExecutionFacts(t, fixture.app)
			for _, suffix := range []string{"/rows", "/data"} {
				key := existing.TableID + suffix
				if after[key] != before[key] {
					t.Fatalf("existing business table changed: %s = %d, was %d", key, after[key], before[key])
				}
			}
			// Close and reopen PocketBase, then inspect persisted relationships
			// and receipts without relying on the executor's identity maps.
			if err := fixture.app.ResetBootstrapState(); err != nil {
				t.Fatal(err)
			}
			if err := fixture.app.Bootstrap(); err != nil {
				t.Fatal(err)
			}
			persisted, err := sourceimport.NewJournal(fixture.app).Read(context.Background(), "cycle-job")
			if err != nil || !reflect.DeepEqual(result, persisted) {
				t.Fatalf("durable result differs: %v", err)
			}
			sourceExecutionAssertRows(t, fixture, source, persisted)
		})
	}
}

func TestSourceImportExecutionRestoresRequiredRelationsAfterCycleWrites(t *testing.T) {
	fixture := newSourceImportAuthorityFixture(t)
	source := sourceExecutionSnapshot()
	for tableIndex := range source.Tables {
		for fieldIndex := range source.Tables[tableIndex].Fields {
			field := &source.Tables[tableIndex].Fields[fieldIndex]
			if field.Relation != nil {
				field.Required = true
			}
		}
	}
	result, err := sourceimport.NewExecutor(fixture.authority, sourceimport.NewJournal(fixture.app), nil).Execute(context.Background(), sourceExecutionPreview(t, source, nil), "required-cycle", 7)
	if err != nil || result.State != "succeeded" {
		t.Fatalf("required cycle did not settle: %#v, %v", result, err)
	}
	sourceExecutionAssertRows(t, fixture, source, result)
}

func TestSourceImportExecution2501RowsStayBoundedAndDuplicateReceiptsDoNotWrite(t *testing.T) {
	fixture := newSourceImportAuthorityFixture(t)
	source := sourceExecutionOrdinarySnapshot(2501)
	plan := sourceExecutionPreview(t, source, nil)
	journal := sourceimport.NewJournal(fixture.app)
	probe := &sourceExecutionProbe{Authority: fixture.authority}
	result, err := sourceimport.NewExecutor(probe, journal, nil).Execute(context.Background(), plan, "large-job", 7)
	if err != nil || result.State != "succeeded" {
		t.Fatalf("large migration: state=%s error=%v", result.State, err)
	}
	sourceExecutionAssertCounts(t, fixture, result, 2503, 0, 0)
	sourceExecutionAssertRows(t, fixture, source, result)
	if probe.recordCalls != 9 {
		t.Fatalf("2501 + 1 + 1 records produced %d batches, want 7 + 1 + 1", probe.recordCalls)
	}
	for _, request := range probe.requests {
		if len(request.Operations) > 400 {
			t.Fatalf("source batch exceeded 400 operations: %d", len(request.Operations))
		}
	}
	request := probe.requests[0]
	batch, found, err := journal.FindBatch(context.Background(), result.JobID, request.IdempotencyKey)
	if err != nil || !found || batch.Created != 400 {
		t.Fatalf("durable first batch = %#v, %v, %v", batch, found, err)
	}
	before := sourceExecutionFacts(t, fixture.app)
	if err := fixture.app.RunInTransaction(func(tx core.App) error { return journal.Commit(tx, batch) }); err != nil {
		t.Fatalf("identical durable batch was not idempotent: %v", err)
	}
	duplicate, err := fixture.authority.Mutate(context.Background(), request, func(core.App, mutation.Receipt) error {
		t.Fatal("duplicate mutation invoked the result projection again")
		return nil
	})
	if err != nil || duplicate.Status != mutation.StatusReplayed {
		t.Fatalf("same batch mutation did not replay: %#v, %v", duplicate, err)
	}
	if after := sourceExecutionFacts(t, fixture.app); !reflect.DeepEqual(before, after) {
		t.Fatalf("duplicate batch changed rows, revisions, audit or events: before=%v after=%v", before, after)
	}
	again, err := journal.Read(context.Background(), result.JobID)
	if err != nil || !reflect.DeepEqual(result, again) {
		t.Fatalf("duplicate receipt changed persistent result: %v", err)
	}
}

func TestSourceImportExecutionReportsSecondThirdAndRelationFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		batch    int
		relation bool
		created  int
		pending  int
	}{
		{"second", 2, false, 400, 603},
		{"third", 3, false, 800, 203},
		{"relations", 0, true, 6, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSourceImportAuthorityFixture(t)
			source := sourceExecutionOrdinarySnapshot(1001)
			if test.relation {
				source = sourceExecutionSnapshot()
			}
			journal := sourceimport.NewJournal(fixture.app)
			probe := &sourceExecutionProbe{Authority: fixture.authority, rejectRecord: test.batch, rejectRelation: test.relation}
			result, err := sourceimport.NewExecutor(probe, journal, nil).Execute(context.Background(), sourceExecutionPreview(t, source, nil), "failure-job", 7)
			if err == nil || result.State != "failed" || result.UnknownBatch != "" {
				t.Fatalf("definite failure was misreported: %#v, %v", result, err)
			}
			stage := "records"
			if test.relation {
				stage = "relations"
			}
			if result.Stage != stage {
				t.Fatalf("failed stage=%s, want %s", result.Stage, stage)
			}
			sourceExecutionAssertCounts(t, fixture, result, test.created, 0, test.pending)
			if test.relation && probe.relationCalls != 1 {
				t.Fatal("execution continued after relation rejection")
			}
			if !test.relation && probe.recordCalls != test.batch {
				t.Fatal("execution continued after record rejection")
			}
		})
	}
}

func TestSourceImportExecutionReconcilesLostAckWithoutResubmitting(t *testing.T) {
	fixture := newSourceImportAuthorityFixture(t)
	source := sourceExecutionSnapshot()
	journal := sourceimport.NewJournal(fixture.app)
	probe := &sourceExecutionProbe{Authority: fixture.authority, loseRecordAck: 1}
	result, err := sourceimport.NewExecutor(probe, journal, nil).Execute(context.Background(), sourceExecutionPreview(t, source, nil), "lost-ack-job", 7)
	if err != nil || result.State != "succeeded" {
		t.Fatalf("lost acknowledgement was not reconciled: %#v, %v", result, err)
	}
	seen := map[string]bool{}
	for _, request := range probe.requests {
		if seen[request.IdempotencyKey] {
			t.Fatalf("replayed business submission instead of reading receipt: %s", request.IdempotencyKey)
		}
		seen[request.IdempotencyKey] = true
	}
	if probe.recordCalls != 3 {
		t.Fatalf("lost acknowledgement produced %d record submissions, want 3", probe.recordCalls)
	}
	sourceExecutionAssertCounts(t, fixture, result, 6, 0, 0)
	sourceExecutionAssertRows(t, fixture, source, result)
}

func TestSourceImportExecutionUnknownReceiptReadDoesNotDoubleCountCommittedRows(t *testing.T) {
	fixture := newSourceImportAuthorityFixture(t)
	journal := sourceimport.NewJournal(fixture.app)
	faultJournal := &sourceExecutionReadFault{Journal: journal}
	probe := &sourceExecutionProbe{Authority: fixture.authority, loseRecordAck: 1}
	probe.afterCommit = func(request mutation.Request) { faultJournal.failKey = request.IdempotencyKey }
	result, err := sourceimport.NewExecutor(probe, faultJournal, nil).Execute(context.Background(), sourceExecutionPreview(t, sourceExecutionOrdinarySnapshot(401), nil), "unknown-job", 7)
	if err == nil || !faultJournal.failed || result.State != "interrupted" || probe.recordCalls != 1 {
		t.Fatalf("unknown submit did not stop: state=%s calls=%d error=%v", result.State, probe.recordCalls, err)
	}
	// The final read can see the first batch's durable commit. Those 400 rows
	// must not simultaneously appear in the unknown subset or be subtracted twice.
	sourceExecutionAssertCounts(t, fixture, result, 400, 0, 3)
	persisted, err := journal.Read(context.Background(), result.JobID)
	if err != nil || !reflect.DeepEqual(result, persisted) {
		t.Fatalf("unknown result differs from durable facts: %v", err)
	}
}

func TestSourceImportExecutionPreservesUnknownSubsetWhenNoCommitCanBeProven(t *testing.T) {
	fixture := newSourceImportAuthorityFixture(t)
	journal := sourceimport.NewJournal(fixture.app)
	faultJournal := &sourceExecutionReadFault{Journal: journal}
	probe := &sourceExecutionProbe{Authority: fixture.authority, rejectRecord: 1}
	probe.beforeMutation = func(request mutation.Request) { faultJournal.failKey = request.IdempotencyKey }
	result, err := sourceimport.NewExecutor(probe, faultJournal, nil).Execute(context.Background(), sourceExecutionPreview(t, sourceExecutionOrdinarySnapshot(401), nil), "unproven-job", 7)
	if err == nil || !faultJournal.failed || result.State != "unknown" || result.UnknownBatch == "" || probe.recordCalls != 1 {
		t.Fatalf("unproven submission was treated as zero writes or resumed: %#v, %v", result, err)
	}
	// The observer could not read the receipt after the failed call. The
	// executor may not manufacture a definite zero-write outcome from that.
	sourceExecutionAssertCounts(t, fixture, result, 0, 400, 3)
}

func TestSourceImportExecutionCancellationStopsAfterCommittedBatch(t *testing.T) {
	fixture := newSourceImportAuthorityFixture(t)
	journal := sourceimport.NewJournal(fixture.app)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probe := &sourceExecutionProbe{Authority: fixture.authority}
	probe.afterCommit = func(mutation.Request) { cancel() }
	result, err := sourceimport.NewExecutor(probe, journal, nil).Execute(ctx, sourceExecutionPreview(t, sourceExecutionOrdinarySnapshot(801), nil), "cancel-job", 7)
	if !errors.Is(err, context.Canceled) || result.State != "cancelled" || probe.recordCalls != 1 || probe.relationCalls != 0 {
		t.Fatalf("cancel continued or erased committed truth: state=%s recordCalls=%d relationCalls=%d error=%v", result.State, probe.recordCalls, probe.relationCalls, err)
	}
	sourceExecutionAssertCounts(t, fixture, result, 400, 0, 403)
}
