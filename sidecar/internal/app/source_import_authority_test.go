package app

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/sourceimport"
)

type sourceImportAuthorityFixture struct {
	app       core.App
	authority *sourceImportAuthority
	admitted  []string
}

func newSourceImportAuthorityFixture(t *testing.T) *sourceImportAuthorityFixture {
	t.Helper()
	pb := schemaProductStore(t)
	fixture := &sourceImportAuthorityFixture{app: pb}
	gate := businessWriteGate(func(ctx context.Context, kind, identity string, apply func(context.Context) error) error {
		fixture.admitted = append(fixture.admitted, kind+"/"+identity)
		return apply(ctx)
	})
	r := router.NewRouter(func(writer http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: writer, Request: request}}, nil
	})
	domain := registerFieldRoutes(r, pb, nil, nil, nil, nil, gate)
	fixture.authority = newSourceImportAuthority(domain, mutation.New(pb, mutation.MetadataSchemaSource{}), gate)
	return fixture
}

func sourceImportTextDraft(t *testing.T, name string) *v2.FieldDraft {
	t.Helper()
	defaults, err := v2.RecommendedDefaults(v2.LogicalText)
	if err != nil {
		t.Fatal(err)
	}
	return &v2.FieldDraft{
		DisplayName: name, LogicalType: v2.LogicalText, Value: defaults.Value,
		Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
	}
}

func (fixture *sourceImportAuthorityFixture) table(t *testing.T, name string) v2.TableCreateReceipt {
	t.Helper()
	receipt, err := fixture.authority.CreateTable(context.Background(), v2.TableCreateIntent{
		DisplayName: name, OperationID: "source-table-" + name, Actor: v2.Actor{ID: "source-job", Kind: "user"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func (fixture *sourceImportAuthorityFixture) field(t *testing.T, table v2.TableCreateReceipt) v2.ApplyReceipt {
	t.Helper()
	receipt, err := fixture.authority.ChangeField(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID, ExpectedSchemaRev: table.SchemaRevision,
		Draft: sourceImportTextDraft(t, "名称"), Actor: v2.Actor{ID: "source-job", Kind: "user"},
	}, "source-field-"+table.TableID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func sourceImportInsert(field v2.ApplyReceipt, key string) mutation.Request {
	recordID := "sourcerecord001"
	return mutation.Request{
		ContractVersion: mutation.ContractVersion, RequestID: "request-" + key, IdempotencyKey: key,
		TableID: field.TableID, SchemaRevision: field.SchemaRevision,
		Operations: []mutation.Operation{{Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{field.FieldID: "00123"}}},
		Actor: mutation.Actor{Type: "import", ID: "source-job"},
	}
}

func TestSourceImportAuthorityProjectsBusinessTransactionsAndReplays(t *testing.T) {
	fixture := newSourceImportAuthorityFixture(t)
	ctx := context.Background()
	projectionErr := errors.New("source receipt storage failed")
	intent := v2.TableCreateIntent{
		DisplayName: "事务测试", OperationID: "source-table-transaction", Actor: v2.Actor{ID: "source-job", Kind: "user"},
	}
	var rolledBackTable string
	_, err := fixture.authority.CreateTable(ctx, intent, func(tx core.App, receipt v2.TableCreateReceipt) error {
		rolledBackTable = receipt.TableID
		if _, err := fieldchange.NewCatalog(tx).Revisions(ctx, receipt.TableID); err != nil {
			t.Fatalf("table is not visible to its commit projection: %v", err)
		}
		return projectionErr
	})
	if !errors.Is(err, projectionErr) || rolledBackTable == "" {
		t.Fatalf("table projection failure = %v, table = %q", err, rolledBackTable)
	}
	if _, err := fixture.authority.Describe(ctx, rolledBackTable); err == nil {
		t.Fatal("table survived its receipt rollback")
	}
	tableCalls := 0
	table, err := fixture.authority.CreateTable(ctx, intent, func(core.App, v2.TableCreateReceipt) error {
		tableCalls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	replayedTable, err := fixture.authority.CreateTable(ctx, intent, func(core.App, v2.TableCreateReceipt) error {
		tableCalls++
		return projectionErr
	})
	if err != nil || tableCalls != 1 || replayedTable.TableID != table.TableID {
		t.Fatalf("table replay = %#v, calls = %d, error = %v", replayedTable, tableCalls, err)
	}
	fieldIntent := v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID, ExpectedSchemaRev: table.SchemaRevision,
		Draft: sourceImportTextDraft(t, "名称"), Actor: intent.Actor,
	}
	fieldCalls := 0
	_, err = fixture.authority.ChangeField(ctx, fieldIntent, "source-field-rollback", func(tx core.App, receipt v2.ApplyReceipt) error {
		fieldCalls++
		if _, err := fieldchange.NewCatalog(tx).Field(ctx, receipt.TableID, receipt.FieldID); err != nil {
			t.Fatalf("field is not visible to its commit projection: %v", err)
		}
		return projectionErr
	})
	if !errors.Is(err, projectionErr) || fieldCalls != 1 {
		t.Fatalf("field projection failure = %v, calls = %d", err, fieldCalls)
	}
	snapshot, err := fixture.authority.Describe(ctx, table.TableID)
	if err != nil || len(snapshot.Fields) != 0 || snapshot.SchemaRevision != table.SchemaRevision {
		t.Fatalf("field survived its receipt rollback: %#v, %v", snapshot, err)
	}
	field, err := fixture.authority.ChangeField(ctx, fieldIntent, "source-field-committed", func(core.App, v2.ApplyReceipt) error {
		fieldCalls++
		return nil
	})
	if err != nil || fieldCalls != 2 {
		t.Fatalf("field commit = %#v, calls = %d, error = %v", field, fieldCalls, err)
	}
	request := sourceImportInsert(field, "source-batch-rollback")
	mutationCalls := 0
	_, err = fixture.authority.Mutate(ctx, request, func(tx core.App, receipt mutation.Receipt) error {
		mutationCalls++
		if len(receipt.AffectedRows) != 1 {
			t.Fatalf("projection received invalid receipt: %#v", receipt)
		}
		metadata, err := tx.FindFirstRecordByData("vibetable_tables", "table_id", table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.FindRecordById(metadata.GetString("collection_id"), *request.Operations[0].RecordID); err != nil {
			t.Fatalf("row is not visible to its commit projection: %v", err)
		}
		return projectionErr
	})
	if !errors.Is(err, projectionErr) || mutationCalls != 1 {
		t.Fatalf("mutation projection failure = %v, calls = %d", err, mutationCalls)
	}
	metadata, err := fixture.app.FindFirstRecordByData("vibetable_tables", "table_id", table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.FindRecordById(metadata.GetString("collection_id"), *request.Operations[0].RecordID); err == nil {
		t.Fatal("row survived its receipt rollback")
	}
	request = sourceImportInsert(field, "source-batch-committed")
	receipt, err := fixture.authority.Mutate(ctx, request, func(core.App, mutation.Receipt) error {
		mutationCalls++
		return nil
	})
	if err != nil || receipt.Status != mutation.StatusApplied {
		t.Fatalf("mutation commit = %#v, %v", receipt, err)
	}
	replayed, err := fixture.authority.Mutate(ctx, request, func(core.App, mutation.Receipt) error {
		mutationCalls++
		return projectionErr
	})
	if err != nil || replayed.Status != mutation.StatusReplayed || mutationCalls != 2 ||
		!reflect.DeepEqual(receipt.AffectedRows, replayed.AffectedRows) {
		t.Fatalf("mutation replay = %#v, calls = %d, error = %v", replayed, mutationCalls, err)
	}
	if fixture.admitted[len(fixture.admitted)-1] != "mutation.apply/"+request.IdempotencyKey {
		t.Fatalf("mutation did not retain its business admission key: %v", fixture.admitted)
	}
}

func TestSourceImportAuthorityRejectsCallerSchemaAndDataFences(t *testing.T) {
	fixture := newSourceImportAuthorityFixture(t)
	ctx := context.Background()
	table := fixture.table(t, "fences")
	field := fixture.field(t, table)
	_, err := fixture.authority.ChangeField(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID, ExpectedSchemaRev: table.SchemaRevision,
		Draft: sourceImportTextDraft(t, "过期字段"), Actor: v2.Actor{ID: "source-job", Kind: "user"},
	}, "source-field-stale", func(core.App, v2.ApplyReceipt) error {
		t.Fatal("stale field plan reached commit")
		return nil
	})
	if err == nil {
		t.Fatal("adapter refreshed a stale schema fence")
	}
	request := sourceImportInsert(field, "source-batch-stale")
	request.SchemaRevision = table.SchemaRevision
	if _, err := fixture.authority.Mutate(ctx, request, nil); err == nil {
		t.Fatal("mutation accepted a stale schema fence")
	}
	request = sourceImportInsert(field, "source-batch-before-required")
	if _, err := fixture.authority.Mutate(ctx, request, nil); err != nil {
		t.Fatal(err)
	}
	draft := sourceImportTextDraft(t, "名称")
	draft.Value.Required = true
	staleData := int64(0)
	intent := v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: table.TableID, FieldID: field.FieldID,
		ExpectedSchemaRev: field.SchemaRevision, ExpectedDataRevision: &staleData,
		Draft: draft, Actor: v2.Actor{ID: "source-job", Kind: "user"},
	}
	if _, err := fixture.authority.ChangeField(ctx, intent, "source-required-stale", nil); err == nil {
		t.Fatal("required update accepted a stale data fence")
	}
	snapshot, err := fixture.authority.Describe(ctx, table.TableID)
	if err != nil || len(snapshot.Fields) != 1 || snapshot.Fields[0].Value.Required || snapshot.DataRevision != 1 {
		t.Fatalf("failed fences changed authority: %#v, %v", snapshot, err)
	}
	intent.ExpectedDataRevision = &snapshot.DataRevision
	if _, err := fixture.authority.ChangeField(ctx, intent, "source-required-current", nil); err != nil {
		t.Fatalf("current required constraint update failed: %v", err)
	}
}

func TestSourceImportAuthorityRollsBackNameTakenAfterPreview(t *testing.T) {
	fixture := newSourceImportAuthorityFixture(t)
	ctx := context.Background()
	existing := fixture.table(t, "已被占用")
	before, err := fixture.authority.Describe(ctx, existing.TableID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.authority.CreateTable(ctx, v2.TableCreateIntent{
		DisplayName: "  已被占用  ", OperationID: "source-late-name-conflict",
		Actor: v2.Actor{ID: "source-job", Kind: "user"},
	}, func(core.App, v2.TableCreateReceipt) error {
		t.Fatal("name-conflicting create reached the import journal")
		return nil
	})
	var productErr *sourceimport.Error
	if !errors.As(err, &productErr) || productErr.Code != "source_import.target_name" {
		t.Fatalf("name conflict = %v", err)
	}
	tables, err := fixture.app.FindAllRecords("vibetable_tables")
	if err != nil || len(tables) != 1 || tables[0].GetString("table_id") != existing.TableID {
		t.Fatalf("name conflict left an extra table: count = %d, error = %v", len(tables), err)
	}
	after, err := fixture.authority.Describe(ctx, existing.TableID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("existing table changed: before = %#v, after = %#v, error = %v", before, after, err)
	}
}

func TestSourceImportAuthorityCreatesReciprocalPairWithoutDangerConfirmation(t *testing.T) {
	fixture := newSourceImportAuthorityFixture(t)
	ctx := context.Background()
	left := fixture.field(t, fixture.table(t, "left"))
	right := fixture.field(t, fixture.table(t, "right"))
	defaults, err := v2.RecommendedDefaults(v2.LogicalRelation)
	if err != nil {
		t.Fatal(err)
	}
	intent := v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: left.TableID, ExpectedSchemaRev: left.SchemaRevision,
		Actor: v2.Actor{ID: "source-job", Kind: "user"},
		Draft: &v2.FieldDraft{
			DisplayName: "关联", LogicalType: v2.LogicalRelation, Value: defaults.Value,
			Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
			Relation: &v2.RelationSpec{TargetTableID: right.TableID, DisplayField: right.FieldID,
				Cardinality: "many", DeletePolicy: "restrict"},
		},
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "反向", ReciprocalCardinality: "many", SourceDisplayFieldID: left.FieldID},
	}
	called := false
	result, err := fixture.authority.ChangeField(ctx, intent, "source-relation-pair", func(tx core.App, receipt v2.ApplyReceipt) error {
		called = true
		if len(receipt.Related) != 1 {
			t.Fatalf("pair projection omitted reciprocal: %#v", receipt)
		}
		if _, err := fieldchange.NewCatalog(tx).Field(ctx, right.TableID, receipt.Related[0].FieldID); err != nil {
			t.Fatalf("reciprocal was not in the same transaction: %v", err)
		}
		return nil
	})
	if err != nil || !called || result.Definition == nil || result.Definition.Relation.PairID == "" {
		t.Fatalf("paired relation = %#v, projection = %v, error = %v", result, called, err)
	}
	intent.ExpectedSchemaRev = result.SchemaRevision
	intent.Draft.Relation.DeletePolicy = "cascade"
	if _, err := fixture.authority.ChangeField(ctx, intent, "source-relation-danger", nil); err == nil {
		t.Fatal("source import silently confirmed a cascade relation")
	}
}

type sourceImportFixedPlanner struct{ plan v2.FieldChangePlan }

func (planner sourceImportFixedPlanner) Plan(context.Context, v2.FieldChangeIntent) (v2.FieldChangePlan, error) {
	return planner.plan, nil
}

type sourceImportForbiddenExecutor struct{ t *testing.T }

func (executor sourceImportForbiddenExecutor) Apply(context.Context, v2.ApplyRequest) (v2.ApplyReceipt, error) {
	executor.t.Fatal("unsupported source field plan reached the executor")
	return v2.ApplyReceipt{}, nil
}

func TestSourceImportAuthorityRejectsUnsettledOrUnconfirmedPlans(t *testing.T) {
	fixture := newSourceImportAuthorityFixture(t)
	for _, test := range []struct {
		name string
		plan v2.FieldChangePlan
		code string
	}{
		{"blocked", v2.FieldChangePlan{}, "source_import.schema.blocked"},
		{"diagnostics", v2.FieldChangePlan{CanApply: true, Errors: []v2.Diagnostic{{Code: "field.invalid"}}}, "source_import.schema.blocked"},
		{"async", v2.FieldChangePlan{CanApply: true, CreatesMigration: true}, "source_import.schema.migration_required"},
		{"danger", v2.FieldChangePlan{CanApply: true, Classes: []v2.ChangeClass{v2.ClassDanger}}, "source_import.schema.unsupported"},
		{"migration", v2.FieldChangePlan{CanApply: true, Classes: []v2.ChangeClass{v2.ClassMigration}}, "source_import.schema.unsupported"},
		{"confirmation", v2.FieldChangePlan{CanApply: true, Confirmations: []string{"protection"}}, "source_import.schema.confirmation_required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapted, err := schemacore.New(fieldchange.NewCatalog(fixture.app), sourceImportFixedPlanner{test.plan}, sourceImportForbiddenExecutor{t})
			if err != nil {
				t.Fatal(err)
			}
			authority := newSourceImportAuthority(schemaFieldChangeDomain{core: adapted}, nil)
			_, err = authority.ChangeField(context.Background(), v2.FieldChangeIntent{Action: v2.ActionCreate}, "source-plan-check", nil)
			var productErr *sourceimport.Error
			if !errors.As(err, &productErr) || productErr.Code != test.code {
				t.Fatalf("rejection = %v, want %s", err, test.code)
			}
		})
	}
}
