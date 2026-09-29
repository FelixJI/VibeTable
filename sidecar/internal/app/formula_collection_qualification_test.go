package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// Replays S37's exact 165 source/3 ledger fixture through the public preview
// handler, retaining prior formulas as each next draft is added. Timings are
// observations; resource protection is not a throughput SLO.
func TestFormulaCollectionPreviewS37Qualification(t *testing.T) {
	pb := schemaProductStore(t)
	ctx := context.Background()
	invoke := formulaTestInvoker(t, formulaDomain{app: pb, compiler: formula.NewAppCompiler(pb)})
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	createTable := func(name string) v2.TableCreateReceipt {
		t.Helper()
		table, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: name, OperationID: "s37-" + name, Actor: v2.Actor{ID: "local-user", Kind: "user"}})
		if err != nil {
			t.Fatal(err)
		}
		return table
	}
	ship, ledger := createTable("shipments"), createTable("ledger")
	field := func(table, name string, kind v2.LogicalType) v2.FieldDefinition {
		return *createSchemaProductField(t, pb, table, kind, name, "s37-"+table+"-"+name).Definition
	}
	contract := field(ship.TableID, "合同", v2.LogicalText)
	status := field(ship.TableID, "状态", v2.LogicalText)
	amount := field(ship.TableID, "金额", v2.LogicalNumber)
	code := field(ship.TableID, "编码", v2.LogicalText)
	day := field(ship.TableID, "日期", v2.LogicalDate)
	currentContract := field(ledger.TableID, "合同", v2.LogicalText)
	start := field(ledger.TableID, "开始日期", v2.LogicalDate)
	end := field(ledger.TableID, "结束日期", v2.LogicalDate)
	collection := func(table string) *core.Collection {
		definition, err := schemaexecution.Describe(ctx, pb, table)
		if err != nil {
			t.Fatal(err)
		}
		result, err := pb.FindCollectionByNameOrId(definition.PhysicalName)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	shipCollection, ledgerCollection := collection(ship.TableID), collection(ledger.TableID)
	set := func(record *core.Record, field v2.FieldDefinition, value any) {
		record.Set(field.Identity.PhysicalName, value)
		if field.Value.Presence.PhysicalName != "" {
			record.Set(field.Value.Presence.PhysicalName, true)
		}
	}
	type shipment struct {
		contract, status, code, day string
		amount                      float64
	}
	shipments := make([]shipment, 0, 165)
	for i := 0; i < 165; i++ {
		item := shipment{"合同甲", "已发货", fmt.Sprintf("甲-%03d", i%40), fmt.Sprintf("2026-03-%02d", 1+i%30), float64(1 + i%9)}
		id := fmt.Sprintf("ship%011d", i)
		if i == 1 || i >= 153 {
			item.status = "待发货"
		}
		if i == 160 {
			item = shipment{"合同乙", "已发货", "乙-001", "2026-03-10", 7}
		}
		if i > 160 {
			item = shipment{"其它合同", "已发货", fmt.Sprintf("噪-%d", i-161), "2026-03-15", 5}
			id = fmt.Sprintf("noise%010d", i-161)
		}
		shipments = append(shipments, item)
		record := core.NewRecord(shipCollection)
		record.Id = id
		for _, entry := range []struct {
			field v2.FieldDefinition
			value any
		}{{contract, item.contract}, {status, item.status}, {amount, item.amount}, {code, item.code}, {day, item.day}} {
			set(record, entry.field, entry.value)
		}
		if err := pb.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	rows := make([]map[string]any, 3)
	wants := make([][]any, 3)
	for i, name := range []string{"合同甲", "合同乙", "合同丙"} {
		from, to := "2026-03-01", "2026-03-31"
		if i == 0 {
			from, to = "2026-03-05", "2026-03-19"
		}
		rows[i] = map[string]any{currentContract.Identity.PhysicalName: name, start.Identity.PhysicalName: from, end.Identity.PhysicalName: to}
		record := core.NewRecord(ledgerCollection)
		record.Id = fmt.Sprintf("ledgerrow%06d", (i+1)*100)
		set(record, currentContract, name)
		set(record, start, from)
		set(record, end, to)
		if err := pb.Save(record); err != nil {
			t.Fatal(err)
		}
		var sum, count, window float64
		codes, seen := []string{}, map[string]bool{}
		// Independent scalar oracle; no product compiler/query results are used.
		for _, item := range shipments {
			if item.contract != name {
				continue
			}
			if item.status == "已发货" {
				sum += item.amount
				count++
			}
			if item.day >= from && item.day <= to {
				window += item.amount
			}
			if !seen[item.code] {
				seen[item.code] = true
				codes = append(codes, item.code)
			}
		}
		wants[i] = []any{sum, count, strings.Join(codes, ", "), window}
	}
	backfill := jobs.New(pb, nil)
	// Persist normal field-change jobs, but keep this preview measurement free
	// of asynchronous materialization (there is no mutation kernel here).
	backfill.Shutdown()
	recommended, err := v2.RecommendedDefaults(v2.LogicalFormula)
	if err != nil {
		t.Fatal(err)
	}
	basePredicate := fmt.Sprintf("CurrentValue.%s == %s", contract.Identity.PhysicalName, currentContract.Identity.PhysicalName)
	shippedPredicate := basePredicate + fmt.Sprintf(" && CurrentValue.%s == %q", status.Identity.PhysicalName, "已发货")
	cases := []struct {
		name, source string
		result       v2.LogicalType
	}{
		{"shipped", fmt.Sprintf("SUMIF(TABLE(%q), %s, CurrentValue.%s)", ship.TableID, shippedPredicate, amount.Identity.PhysicalName), v2.LogicalNumber},
		{"count", fmt.Sprintf("COUNTIF(TABLE(%q), %s)", ship.TableID, shippedPredicate), v2.LogicalNumber},
		{"codes", fmt.Sprintf("ARRAYJOIN(UNIQUE(PROJECT(FILTER(TABLE(%q), %s), CurrentValue.%s)), %q)", ship.TableID, basePredicate, code.Identity.PhysicalName, ", "), v2.LogicalText},
		{"window", fmt.Sprintf("SUMIF(TABLE(%q), %s && CurrentValue.%s >= %s && CurrentValue.%s <= %s, CurrentValue.%s)", ship.TableID, basePredicate, day.Identity.PhysicalName, start.Identity.PhysicalName, day.Identity.PhysicalName, end.Identity.PhysicalName, amount.Identity.PhysicalName), v2.LogicalNumber},
	}
	var schemaReads, sourceReads atomic.Int64
	seenDB := map[*dbx.DB]bool{}
	for _, database := range []*dbx.DB{pb.ConcurrentDB().(*dbx.DB), pb.NonconcurrentDB().(*dbx.DB)} {
		if seenDB[database] {
			continue
		}
		seenDB[database] = true
		previous := database.QueryLogFunc
		database.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, sqlRows *sql.Rows, err error) {
			if strings.Contains(statement, "vibetable_fields") {
				schemaReads.Add(1)
			}
			if strings.Contains(statement, "FROM `"+shipCollection.Name+"`") || strings.Contains(statement, "FROM \""+shipCollection.Name+"\"") {
				sourceReads.Add(1)
			}
			if previous != nil {
				previous(ctx, elapsed, statement, sqlRows, err)
			}
		}
		defer func() { database.QueryLogFunc = previous }()
	}
	for stage, item := range cases {
		candidate := v2.FieldDefinition{Contract: v2.Contract, Identity: v2.FieldIdentity{FieldID: "fld_formula_preview", PhysicalName: "f_formula_preview", ProviderFieldID: "pb_formula_preview"}, DisplayName: item.name, LogicalType: v2.LogicalFormula, Lifecycle: v2.Lifecycle{State: v2.LifecycleActive}, Value: recommended.Value, Constraints: recommended.Constraints, Storage: recommended.Storage, Display: recommended.Display, Formula: &v2.FormulaSpec{Language: "cel-v2", Source: item.source, ResultType: item.result}}
		candidate.Storage.Options.OnlyInt = item.name == "count"
		for sample := 0; sample < 13; sample++ {
			rowIndex := 0
			if sample >= 11 {
				rowIndex = sample - 10
			}
			schemaReads.Store(0)
			sourceReads.Store(0)
			started := time.Now()
			result, err := invoke("formula.preview", map[string]any{"tableId": ledger.TableID, "field": formulaTestWireField(t, candidate), "row": rows[rowIndex], "changedFieldIds": []string{}})
			elapsed := time.Since(started)
			var public *productrpc.PublicError
			var details map[string]any
			if errors.As(err, &public) {
				details = public.Details
			}
			t.Logf("S37 stage=%s sample=%d row=%d wall=%s schemaReads=%d sourceReads=%d error=%v details=%v", item.name, sample, rowIndex, elapsed, schemaReads.Load(), sourceReads.Load(), err, details)
			if err != nil {
				t.Fatal(err)
			}
			if schemaReads.Load() != 2 {
				t.Fatalf("preview reloaded source schemas: %d reads, want root + source", schemaReads.Load())
			}
			value := result.(map[string]any)["values"].(map[string]any)[candidate.Identity.PhysicalName]
			want := wants[rowIndex][stage]
			if item.name == "count" {
				want = int64(want.(float64))
			}
			if !reflect.DeepEqual(value, want) {
				t.Fatalf("%s row%d = %#v, want %#v", item.name, rowIndex, value, wants[rowIndex][stage])
			}
		}
		catalog := fieldchange.NewCatalog(pb)
		revisions, err := catalog.Revisions(ctx, ledger.TableID)
		if err != nil {
			t.Fatal(err)
		}
		store := fieldchange.NewPocketBasePlanStore(pb)
		intent := v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: ledger.TableID, ExpectedSchemaRev: revisions.Schema, Actor: v2.Actor{ID: "local-user", Kind: "user"}, Draft: &v2.FieldDraft{DisplayName: item.name, LogicalType: v2.LogicalFormula, Value: recommended.Value, Constraints: recommended.Constraints, Storage: recommended.Storage, Display: recommended.Display, Formula: &v2.FormulaDraftSpec{Language: "cel-v2", Source: item.source}}}
		plan, err := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil)).Plan(ctx, intent)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := fieldchange.NewExecutor(pb, store, fieldchange.WithFormulaBackfillScheduler(backfill)).Apply(ctx, v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "s37-formula-" + item.name, Actor: intent.Actor})
		if err != nil {
			t.Fatal(err)
		}
		for i := range rows {
			rows[i][receipt.Definition.Identity.PhysicalName] = wants[i][stage]
		}
	}
}
