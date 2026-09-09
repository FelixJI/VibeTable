package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/relationpair"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

type inspectFixture struct {
	app    core.App
	tables [2]v2IntegrationTable
	fields [2]v2.FieldDefinition
	ids    [2]string
}

func newInspectFixture(t *testing.T, self bool) inspectFixture {
	t.Helper()
	app := bootstrapApp(t, queryTempDir(t))
	t.Cleanup(func() { resetApp(t, app) })
	ctx := context.Background()
	source, sourceTitle := createV2IntegrationTableWithField(t, ctx, app, "Sources", "Name", "inspect_source")
	target, targetTitle := source, sourceTitle
	if !self {
		target, targetTitle = createV2IntegrationTableWithField(t, ctx, app, "Targets", "Name", "inspect_target")
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	draft := fieldDraftForIntegration(t, v2.LogicalRelation, "Target")
	draft.Relation = &v2.RelationSpec{TargetTableID: target.TableID, Cardinality: "one", DeletePolicy: "setNull", DisplayField: targetTitle.FieldID}
	rev, err := catalog.Revisions(ctx, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	planned, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: source.TableID,
		ExpectedSchemaRev: rev.Schema, Draft: &draft, Actor: actor,
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "Sources", ReciprocalCardinality: "one", SourceDisplayFieldID: sourceTitle.FieldID}})
	if err != nil || !planned.CanApply {
		t.Fatalf("pair plan: %#v %v", planned.Errors, err)
	}
	receipt, err := fieldchange.NewExecutor(app, store).Apply(ctx, v2.ApplyRequest{PlanID: planned.PlanID, PlanHash: planned.PlanHash, OperationID: "inspect_pair", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	f := inspectFixture{app: app, tables: [2]v2IntegrationTable{source, target}, fields: [2]v2.FieldDefinition{*receipt.Definition, *receipt.Related[0].Definition}, ids: [2]string{"inspectsourc001", "inspecttarge001"}}
	for _, side := range []int{1, 0} {
		revisions, err := catalog.Revisions(ctx, f.tables[side].TableID)
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]any{}
		if side == 0 {
			values[f.fields[0].Identity.FieldID] = f.ids[1]
		}
		_, err = mutation.New(app, mutation.MetadataSchemaSource{}).Apply(ctx, mutationRequest(f.tables[side].TableID, revisions.Schema,
			fmt.Sprintf("inspect_insert_%d", side), mutation.Operation{Kind: mutation.OperationInsert, RecordID: &f.ids[side], Values: map[string]any{}, RawValues: values}))
		if err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f inspectFixture) request() relationpair.Request {
	return relationpair.Request{TableID: f.tables[0].TableID, FieldID: f.fields[0].Identity.FieldID, Limit: 100}
}

func (f inspectFixture) writeRaw(t *testing.T, side int, value string) {
	t.Helper()
	_, err := f.app.DB().NewQuery(fmt.Sprintf(`UPDATE "%s" SET "%s"={:value} WHERE id={:id}`, f.tables[side].PhysicalName, f.fields[side].Identity.PhysicalName)).
		Bind(dbx.Params{"value": value, "id": f.ids[side]}).Execute()
	if err != nil {
		t.Fatal(err)
	}
}

func (f inspectFixture) state(t *testing.T) map[string][]dbx.NullStringMap {
	t.Helper()
	result := map[string][]dbx.NullStringMap{}
	for _, name := range []string{f.tables[0].PhysicalName, f.tables[1].PhysicalName, "vibetable_tables", "vibetable_fields", "vibetable_relations", "vibetable_audit_events", "vibetable_outbox", "vibetable_idempotency_keys"} {
		var rows []dbx.NullStringMap
		if err := f.app.DB().NewQuery(fmt.Sprintf(`SELECT * FROM "%s" ORDER BY id`, name)).All(&rows); err != nil {
			t.Fatal(err)
		}
		result[name] = rows
	}
	return result
}

func TestRelationPairInspectHealthyAndSelfRelationAreReadOnly(t *testing.T) {
	for _, self := range []bool{false, true} {
		t.Run(fmt.Sprint(self), func(t *testing.T) {
			f := newInspectFixture(t, self)
			before := f.state(t)
			report, err := relationpair.Inspect(context.Background(), f.app, f.request())
			if err != nil || !report.Complete || !report.PageComplete || !report.Finished || len(report.Counts) != 0 {
				t.Fatalf("healthy report: %#v %v", report, err)
			}
			if report.PairID != f.fields[0].Relation.PairID || report.Endpoints[1].FieldID != f.fields[1].Identity.FieldID {
				t.Fatalf("unstable identity: %#v", report)
			}
			if !reflect.DeepEqual(before, f.state(t)) {
				t.Fatal("inspection changed authority")
			}
		})
	}
}

func TestRelationPairInspectReportsCorruptLinksAndPresence(t *testing.T) {
	for _, kind := range []string{"dangling", "duplicate", "missing_reciprocal", "presence_mismatch", "invalid_value", "scan_limit"} {
		t.Run(kind, func(t *testing.T) {
			f := newInspectFixture(t, false)
			switch kind {
			case "dangling":
				f.writeRaw(t, 0, "missingtarget01")
			case "duplicate":
				f.writeRaw(t, 0, `["`+f.ids[1]+`","`+f.ids[1]+`"]`)
			case "missing_reciprocal":
				f.writeRaw(t, 1, "")
			case "presence_mismatch":
				_, err := f.app.DB().NewQuery(fmt.Sprintf(`UPDATE "%s" SET "%s"=0 WHERE id={:id}`, f.tables[1].PhysicalName, f.fields[1].Value.Presence.PhysicalName)).Bind(dbx.Params{"id": f.ids[1]}).Execute()
				if err != nil {
					t.Fatal(err)
				}
			case "invalid_value":
				f.writeRaw(t, 0, `{"broken":true}`)
			case "scan_limit":
				f.writeRaw(t, 0, strings.Repeat("x", 65537))
			}
			before := f.state(t)
			report, err := relationpair.Inspect(context.Background(), f.app, f.request())
			if err != nil || report.Counts[kind] == 0 {
				t.Fatalf("missing %s: %#v %v", kind, report, err)
			}
			if kind == "duplicate" && report.Counts["one_conflict"] == 0 {
				t.Fatal("one cardinality conflict was hidden")
			}
			if (kind == "invalid_value" || kind == "scan_limit") && report.Complete {
				t.Fatal("unreadable links reported complete")
			}
			if !reflect.DeepEqual(before, f.state(t)) {
				t.Fatal("diagnostic changed corrupt authority")
			}
		})
	}
}

func TestRelationPairInspectMetadataDamageDoesNotLookHealthy(t *testing.T) {
	for _, kind := range []string{"pair", "mirror", "missing", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			f := newInspectFixture(t, false)
			switch kind {
			case "pair", "invalid":
				definition := f.fields[1]
				copied := *definition.Relation
				definition.Relation = &copied
				if kind == "pair" {
					definition.Relation.PairID = "pair_other"
				} else {
					definition.Relation = nil
				}
				raw, _ := json.Marshal(definition)
				_, err := f.app.DB().NewQuery(`UPDATE vibetable_fields SET definition_v2_json={:raw} WHERE field_id={:id}`).Bind(dbx.Params{"raw": string(raw), "id": definition.Identity.FieldID}).Execute()
				if err != nil {
					t.Fatal(err)
				}
			case "mirror":
				_, err := f.app.DB().NewQuery(`UPDATE vibetable_relations SET pair_id='other' WHERE source_field_id={:id}`).Bind(dbx.Params{"id": f.fields[1].Identity.FieldID}).Execute()
				if err != nil {
					t.Fatal(err)
				}
			case "missing":
				_, err := f.app.DB().NewQuery(`DELETE FROM vibetable_fields WHERE field_id={:id}`).Bind(dbx.Params{"id": f.fields[1].Identity.FieldID}).Execute()
				if err != nil {
					t.Fatal(err)
				}
			}
			before := f.state(t)
			report, err := relationpair.Inspect(context.Background(), f.app, f.request())
			if err != nil || report.Counts["metadata_asymmetric"]+report.Counts["metadata_invalid"] == 0 {
				t.Fatalf("metadata report: %#v %v", report, err)
			}
			if !reflect.DeepEqual(before, f.state(t)) {
				t.Fatal("metadata inspection wrote authority")
			}
		})
	}
}

func TestRelationPairInspectPaginationKeepsEarlierFindingsAndRejectsRevisionDrift(t *testing.T) {
	f := newInspectFixture(t, false)
	for i := range 5 {
		_, err := f.app.DB().NewQuery(fmt.Sprintf(`INSERT INTO "%s" (id,"%s","%s") VALUES ({:id},'missing',1)`, f.tables[0].PhysicalName, f.fields[0].Identity.PhysicalName, f.fields[0].Value.Presence.PhysicalName)).
			Bind(dbx.Params{"id": fmt.Sprintf("extra%010d", i)}).Execute()
		if err != nil {
			t.Fatal(err)
		}
	}
	req := f.request()
	req.Limit = 2
	first, err := relationpair.Inspect(context.Background(), f.app, req)
	if err != nil || first.Next == nil || first.Complete || !first.PageComplete {
		t.Fatalf("first page: %#v %v", first, err)
	}
	seen, findings := first.RowsScanned[0], first.Counts["dangling"]
	req.Cursor = first.Next
	for {
		page, err := relationpair.Inspect(context.Background(), f.app, req)
		if err != nil {
			t.Fatal(err)
		}
		if page.RowsScanned[0] > 2 {
			t.Fatal("page exceeded limit")
		}
		seen += page.RowsScanned[0]
		findings += page.Counts["dangling"]
		if page.Next == nil {
			if !page.Finished || !page.Complete {
				t.Fatalf("incomplete terminal page: %#v", page)
			}
			break
		}
		req.Cursor = page.Next
	}
	if seen != 6 || findings != 5 {
		t.Fatalf("scan skipped/duplicated rows: %d/%d", seen, findings)
	}
	req.Cursor = first.Next
	_, err = f.app.DB().NewQuery(`UPDATE vibetable_tables SET data_revision=data_revision+1 WHERE table_id={:id}`).Bind(dbx.Params{"id": f.tables[1].TableID}).Execute()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relationpair.Inspect(context.Background(), f.app, req); !errors.Is(err, relationpair.ErrRevisionChanged) {
		t.Fatalf("accepted changed counterpart: %v", err)
	}
}

func TestRelationPairInspectCancellationAndReadFailureNeverReturnHealthy(t *testing.T) {
	f := newInspectFixture(t, false)
	before := f.state(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := relationpair.Inspect(ctx, f.app, f.request()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled inspect: %v", err)
	}
	if !reflect.DeepEqual(before, f.state(t)) {
		t.Fatal("cancellation wrote authority")
	}
	_, err := f.app.DB().NewQuery(fmt.Sprintf(`ALTER TABLE "%s" DROP COLUMN "%s"`, f.tables[0].PhysicalName, f.fields[0].Value.Presence.PhysicalName)).Execute()
	if err != nil {
		t.Fatal(err)
	}
	if report, err := relationpair.Inspect(context.Background(), f.app, f.request()); err == nil || report.Complete {
		t.Fatalf("missing physical column treated as empty: %#v %v", report, err)
	}
}

func TestRelationPairInspectBoundsSamplesAndRetainsIncompletePrefix(t *testing.T) {
	f := newInspectFixture(t, false)
	for i := range 80 {
		_, err := f.app.DB().NewQuery(fmt.Sprintf(`INSERT INTO "%s" (id,"%s","%s") VALUES ({:id},'missing',1)`, f.tables[0].PhysicalName, f.fields[0].Identity.PhysicalName, f.fields[0].Value.Presence.PhysicalName)).Bind(dbx.Params{"id": fmt.Sprintf("extra%010d", i)}).Execute()
		if err != nil {
			t.Fatal(err)
		}
	}
	report, err := relationpair.Inspect(context.Background(), f.app, f.request())
	if err != nil || !report.Complete || report.Counts["dangling"] != 80 || len(report.Samples) != 50 || !report.SamplesTruncated {
		t.Fatalf("sample bound lost total/coverage: %#v %v", report, err)
	}
	_, err = f.app.DB().NewQuery(fmt.Sprintf(`UPDATE "%s" SET "%s"={:value} WHERE id='extra0000000000'`, f.tables[0].PhysicalName, f.fields[0].Identity.PhysicalName)).Bind(dbx.Params{"value": strings.Repeat("x", 65537)}).Execute()
	if err != nil {
		t.Fatal(err)
	}
	request := f.request()
	request.Limit = 50
	first, err := relationpair.Inspect(context.Background(), f.app, request)
	if err != nil || first.PageComplete || first.Next == nil || !first.Next.Incomplete {
		t.Fatalf("lost incomplete prefix: %#v %v", first, err)
	}
	request.Cursor = first.Next
	last, err := relationpair.Inspect(context.Background(), f.app, request)
	if err != nil || !last.PageComplete || !last.Finished || last.Complete {
		t.Fatalf("last readable page claimed full coverage: %#v %v", last, err)
	}
}

func TestRelationPairInspectUsesBoundedTargetBatchesAndCancellation(t *testing.T) {
	f := newInspectFixture(t, false)
	ids := []string{f.ids[1]}
	for i := range 400 {
		id := fmt.Sprintf("batch%010d", i)
		_, err := f.app.DB().NewQuery(fmt.Sprintf(`INSERT INTO "%s" (id,"%s","%s") VALUES ({:id},{:source},1)`, f.tables[1].PhysicalName, f.fields[1].Identity.PhysicalName, f.fields[1].Value.Presence.PhysicalName)).Bind(dbx.Params{"id": id, "source": f.ids[0]}).Execute()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	raw, _ := json.Marshal(ids)
	f.writeRaw(t, 0, string(raw))
	db := f.app.ConcurrentDB().(*dbx.DB)
	previous := db.QueryLogFunc
	defer func() { db.QueryLogFunc = previous }()
	var targetBatches []string
	db.QueryLogFunc = func(_ context.Context, _ time.Duration, query string, _ *sql.Rows, _ error) {
		if strings.Contains(query, `FROM "`+f.tables[1].PhysicalName+`" AS r WHERE id IN`) {
			targetBatches = append(targetBatches, query)
		}
	}
	report, err := relationpair.Inspect(context.Background(), f.app, f.request())
	if err != nil || report.Next == nil || report.Counts["one_conflict"] == 0 {
		t.Fatalf("bounded scan: %#v %v", report, err)
	}
	if len(targetBatches) != 3 {
		t.Fatalf("expected 200/200/1 target batches, got %d", len(targetBatches))
	}
	for _, batch := range targetBatches {
		if strings.Count(batch, "batch") > 200 {
			t.Fatal("target batch exceeded 200 IDs")
		}
	}
	db.QueryLogFunc = previous
	before := f.state(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db.QueryLogFunc = func(_ context.Context, _ time.Duration, query string, _ *sql.Rows, _ error) {
		if strings.Contains(query, " AS r WHERE ") {
			cancel()
		}
	}
	if _, err := relationpair.Inspect(ctx, f.app, f.request()); !errors.Is(err, context.Canceled) {
		t.Fatalf("midscan cancellation: %v", err)
	}
	db.QueryLogFunc = previous
	if !reflect.DeepEqual(before, f.state(t)) {
		t.Fatal("cancelled scan wrote authority")
	}
}

func TestRelationPairInspectRejectsForeignCursorAndMidscanRevisionChange(t *testing.T) {
	f := newInspectFixture(t, false)
	first, err := relationpair.Inspect(context.Background(), f.app, f.request())
	if err != nil {
		t.Fatal(err)
	}
	request := f.request()
	request.Cursor = &relationpair.Cursor{PairID: "foreign-pair", Endpoints: first.Endpoints}
	if _, err := relationpair.Inspect(context.Background(), f.app, request); !errors.Is(err, relationpair.ErrRevisionChanged) {
		t.Fatalf("foreign pair accepted: %v", err)
	}
	db := f.app.ConcurrentDB().(*dbx.DB)
	previous := db.QueryLogFunc
	defer func() { db.QueryLogFunc = previous }()
	changed := false
	db.QueryLogFunc = func(_ context.Context, _ time.Duration, query string, _ *sql.Rows, _ error) {
		if !changed && strings.Contains(query, " AS r WHERE ") {
			changed = true
			_, err := f.app.DB().NewQuery(`UPDATE vibetable_tables SET schema_revision=schema_revision+1 WHERE table_id={:id}`).Bind(dbx.Params{"id": f.tables[1].TableID}).Execute()
			if err != nil {
				t.Error(err)
			}
		}
	}
	if _, err := relationpair.Inspect(context.Background(), f.app, f.request()); !errors.Is(err, relationpair.ErrRevisionChanged) {
		t.Fatalf("midscan change accepted: %v", err)
	}
	if !changed {
		t.Fatal("revision fault was not injected")
	}
}

func TestRelationPairInspectMetadataOnlyReportAlsoRejectsRevisionDrift(t *testing.T) {
	f := newInspectFixture(t, false)
	broken := f.fields[0]
	broken.Relation = nil
	raw, _ := json.Marshal(broken)
	if _, err := f.app.DB().NewQuery(`UPDATE vibetable_fields SET definition_v2_json={:raw} WHERE field_id={:id}`).Bind(dbx.Params{"raw": string(raw), "id": broken.Identity.FieldID}).Execute(); err != nil {
		t.Fatal(err)
	}
	db := f.app.ConcurrentDB().(*dbx.DB)
	previous := db.QueryLogFunc
	defer func() { db.QueryLogFunc = previous }()
	changed := false
	db.QueryLogFunc = func(_ context.Context, _ time.Duration, query string, _ *sql.Rows, _ error) {
		if !changed && strings.Contains(query, "FROM vibetable_fields") {
			changed = true
			_, err := f.app.DB().NewQuery(`UPDATE vibetable_tables SET schema_revision=schema_revision+1 WHERE table_id={:id}`).Bind(dbx.Params{"id": f.tables[0].TableID}).Execute()
			if err != nil {
				t.Error(err)
			}
		}
	}
	if _, err := relationpair.Inspect(context.Background(), f.app, f.request()); !errors.Is(err, relationpair.ErrRevisionChanged) {
		t.Fatalf("metadata-only revision drift: %v", err)
	}
	if !changed {
		t.Fatal("metadata fault was not injected")
	}
}
