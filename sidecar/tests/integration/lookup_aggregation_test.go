package integration_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
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
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

var lookupAggregationFixedSample = []any{float64(10), float64(20), float64(0), nil, float64(10)}

var lookupAggregationModes = []string{
	v2.LookupAggregationValues, v2.LookupAggregationDistinct,
	v2.LookupAggregationCountRecords, v2.LookupAggregationCountNonEmpty,
	v2.LookupAggregationCountDistinct, v2.LookupAggregationSum,
	v2.LookupAggregationAverage, v2.LookupAggregationMin, v2.LookupAggregationMax,
}

// lookupAggregationOracle recomputes the expected aggregation independently of
// the sidecar reducer, so page sizes, scans and cursors are checked against a
// plain loop over the fixture values.
func lookupAggregationOracle(t *testing.T, mode string, values []any) any {
	t.Helper()
	switch mode {
	case v2.LookupAggregationValues:
		return values
	case v2.LookupAggregationDistinct:
		seen := map[string]bool{}
		result := []any{}
		for _, value := range values {
			key := fmt.Sprintf("%v|%T", value, value)
			if !seen[key] {
				seen[key] = true
				result = append(result, value)
			}
		}
		return result
	case v2.LookupAggregationCountRecords:
		return float64(len(values))
	case v2.LookupAggregationCountNonEmpty:
		count := 0
		for _, value := range values {
			if value != nil && value != "" {
				count++
			}
		}
		return float64(count)
	case v2.LookupAggregationCountDistinct:
		keys := map[string]bool{}
		for _, value := range values {
			if value == nil || value == "" {
				continue
			}
			keys[fmt.Sprintf("%v|%T", value, value)] = true
		}
		return float64(len(keys))
	case v2.LookupAggregationSum, v2.LookupAggregationAverage,
		v2.LookupAggregationMin, v2.LookupAggregationMax:
		total, count := 0.0, 0
		minimum, maximum := 0.0, 0.0
		for _, value := range values {
			number, ok := value.(float64)
			if !ok {
				continue
			}
			if count == 0 || number < minimum {
				minimum = number
			}
			if count == 0 || number > maximum {
				maximum = number
			}
			total += number
			count++
		}
		if count == 0 {
			if mode == v2.LookupAggregationSum {
				return 0.0
			}
			return nil
		}
		switch mode {
		case v2.LookupAggregationSum:
			return total
		case v2.LookupAggregationAverage:
			return total / float64(count)
		case v2.LookupAggregationMin:
			return minimum
		default:
			return maximum
		}
	default:
		t.Fatalf("oracle has no mode %q", mode)
		return nil
	}
}

func aggregationProbeField(physicalName string, spec v2.LookupSpec) v2.FieldDefinition {
	return v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_" + physicalName, PhysicalName: physicalName},
		LogicalType: v2.LogicalLookup,
		Lookup:      &spec,
	}
}

// lookupPathRawValues models the historical path read for the values shape:
// an unset number survives as the provider's zero, not as null. Aggregations
// read the presence-gated product value instead and see null.
func lookupPathRawValues(values []any) []any {
	raw := make([]any, len(values))
	for index, value := range values {
		raw[index] = value
		if value == nil {
			raw[index] = float64(0)
		}
	}
	return raw
}

// TestLookupAggregationConditionMatchesFullSetOracleIndependentOfPaging builds
// 155 matched source rows (31 copies of the fixed sample) so duplicates cross
// the 100-entry provenance page, then checks every aggregation against an
// independent oracle through grid pages, source detail pages and the single
// record calculation path.
func TestLookupAggregationConditionMatchesFullSetOracleIndependentOfPaging(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "聚合订单", "agg_orders")
	materials := createV2IntegrationTable(t, ctx, app, "聚合物料", "agg_materials")
	orderCode := createV2IntegrationField(t, ctx, app, orders.TableID, fieldDraftForIntegration(t, v2.LogicalText, "订单编码"), "agg_order_code")
	sourceCode := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalText, "物料编码"), "agg_source_code")
	enabled := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalBool, "启用"), "agg_enabled")
	amount := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalNumber, "数量"), "agg_amount")

	set := func(record *core.Record, field v2.ApplyReceipt, value any) {
		record.Set(field.Definition.Identity.PhysicalName, value)
		record.Set(field.Definition.Value.Presence.PhysicalName, true)
	}
	materialCollection, err := app.FindCollectionByNameOrId(materials.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]any, 0, 155)
	for index := range 155 {
		record := core.NewRecord(materialCollection)
		record.Id = fmt.Sprintf("aggmaterial%04d", index)
		set(record, sourceCode, "MAT-1")
		set(record, enabled, true)
		sample := lookupAggregationFixedSample[index%5]
		if sample != nil {
			set(record, amount, sample)
		}
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
		values = append(values, sample)
	}
	orderCollection, err := app.FindCollectionByNameOrId(orders.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	order := core.NewRecord(orderCollection)
	order.Id = "aggorder0000001"
	set(order, orderCode, "MAT-1")
	if err := app.Save(order); err != nil {
		t.Fatal(err)
	}
	definition, err := schemaexecution.Describe(ctx, app, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	calculator := lookup.NewCalculator()
	for _, mode := range lookupAggregationModes {
		t.Run(mode, func(t *testing.T) {
			field := aggregationProbeField("f_agg_probe", v2.LookupSpec{
				Path: []v2.LookupPathStep{}, TargetFieldID: amount.FieldID,
				Aggregation: mode,
				Condition: &v2.LookupCondition{
					SourceTableID: materials.TableID, Match: "all",
					Rules: []v2.LookupConditionRule{{
						SourceFieldID: sourceCode.FieldID, Operator: "eq",
						Operand: &v2.LookupOperand{Kind: "field", FieldID: orderCode.FieldID},
					}},
				},
			})
			want := lookupAggregationOracle(t, mode, values)
			for _, page := range []struct{ offset, limit int }{
				{0, 100}, {100, 100}, {0, 1},
			} {
				cell, pageErr := calculator.CalculateFieldPage(ctx, app, definition, order, field, page.offset, page.limit)
				if pageErr != nil {
					t.Fatalf("page %d/%d: %v", page.offset, page.limit, pageErr)
				}
				if !reflect.DeepEqual(cell.Value, want) {
					t.Fatalf("page %d/%d value = %#v, want %#v", page.offset, page.limit, cell.Value, want)
				}
				visible := min(page.limit, 155-page.offset)
				if visible < 0 {
					visible = 0
				}
				if len(cell.Provenance) != visible || cell.ProvenanceTotal != 155 ||
					!cell.ProvenanceTotalKnown || cell.ProvenanceHasMore != (155 > page.offset+visible) {
					t.Fatalf("page %d/%d provenance = %#v", page.offset, page.limit, cell)
				}
			}
		})
	}
}

// TestLookupAggregationPathMatchesFullSetOracleWithCrossPageDuplicates wires a
// many relation whose link list repeats records across page boundaries, then
// checks every aggregation against an independent oracle and confirms a
// values-mode field sharing the same path still gets the paged shape while the
// aggregated field reads the complete set.
func TestLookupAggregationPathMatchesFullSetOracleWithCrossPageDuplicates(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "路径订单", "path_orders")
	materials := createV2IntegrationTable(t, ctx, app, "路径物料", "path_materials")
	orderName := createV2IntegrationField(t, ctx, app, orders.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), "path_order_name")
	materialName := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), "path_material_name")
	amount := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalNumber, "数量"), "path_amount")
	link := createV2IntegrationRelation(t, ctx, app, orders.TableID, orderName.FieldID,
		materials.TableID, materialName.FieldID, "明细", "订单", "many", "path_order_lines")

	materialCollection, err := app.FindCollectionByNameOrId(materials.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 40)
	for index := range 40 {
		record := core.NewRecord(materialCollection)
		record.Id = fmt.Sprintf("pathmaterial%03d", index)
		// Nil samples leave both the physical value and its presence companion
		// unset, so the stored row carries a genuine missing number.
		if sample := lookupAggregationFixedSample[index%5]; sample != nil {
			record.Set(amount.Definition.Identity.PhysicalName, sample)
			record.Set(amount.Definition.Value.Presence.PhysicalName, true)
		}
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, record.Id)
	}
	// 200 links: every record repeats five times, interleaved across pages.
	links := make([]string, 0, 200)
	values := make([]any, 0, 200)
	for index := range 200 {
		links = append(links, ids[index%40])
		values = append(values, lookupAggregationFixedSample[index%40%5])
	}
	orderCollection, err := app.FindCollectionByNameOrId(orders.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	order := core.NewRecord(orderCollection)
	order.Id = "pathorder000001"
	// SetRaw keeps the repeated provider links verbatim.
	order.SetRaw(link.Definition.Identity.PhysicalName, links)
	definition, err := schemaexecution.Describe(ctx, app, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	calculator := lookup.NewCalculator()
	for _, mode := range lookupAggregationModes {
		t.Run(mode, func(t *testing.T) {
			field := aggregationProbeField("f_agg_probe", v2.LookupSpec{
				Path:          []v2.LookupPathStep{{RelationFieldID: link.FieldID}},
				TargetFieldID: amount.FieldID, Aggregation: mode,
			})
			want := lookupAggregationOracle(t, mode, values)
			for _, page := range []struct{ offset, limit int }{
				{0, 100}, {100, 100}, {150, 50},
			} {
				expected := want
				if mode == v2.LookupAggregationValues {
					// The values shape keeps its page window over the historical
					// raw read (unset numbers surface as zero, not null).
					raw := lookupPathRawValues(values)
					expected = raw[page.offset:min(page.offset+page.limit, len(raw))]
				}
				cell, pageErr := calculator.CalculateFieldPage(ctx, app, definition, order, field, page.offset, page.limit)
				if pageErr != nil {
					t.Fatalf("page %d/%d: %v", page.offset, page.limit, pageErr)
				}
				if !reflect.DeepEqual(cell.Value, expected) {
					gotValues := cell.Value.([]any)
					wantValues := expected.([]any)
					for index := 0; index < len(wantValues) && index < len(gotValues); index++ {
						if !reflect.DeepEqual(gotValues[index], wantValues[index]) {
							t.Fatalf("page %d/%d first mismatch at %d: got %#v (%T), want %#v (%T)", page.offset, page.limit, index, gotValues[index], gotValues[index], wantValues[index], wantValues[index])
						}
					}
					t.Fatalf("page %d/%d value lengths %d/%d", page.offset, page.limit, len(gotValues), len(wantValues))
				}
				visible := min(page.limit, 200-page.offset)
				if visible < 0 {
					visible = 0
				}
				if len(cell.Provenance) != visible || cell.ProvenanceTotal != 200 || !cell.ProvenanceTotalKnown {
					t.Fatalf("page %d/%d provenance = %#v", page.offset, page.limit, cell)
				}
			}
		})
	}
	// Persisted fields over the same relation path share one traversal; the
	// values cell keeps its page window while the aggregation reads everything.
	valuesDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "明细数量")
	valuesDraft.Lookup = &v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: link.FieldID}},
		TargetFieldID: amount.FieldID,
	}
	valuesLookup := createV2IntegrationField(t, ctx, app, orders.TableID, valuesDraft, "path_values_lookup")
	sumDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "明细求和")
	sumDraft.Lookup = &v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: link.FieldID}},
		TargetFieldID: amount.FieldID, Aggregation: v2.LookupAggregationSum,
	}
	sumLookup := createV2IntegrationField(t, ctx, app, orders.TableID, sumDraft, "path_sum_lookup")
	definition, err = schemaexecution.Describe(ctx, app, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	batch, batchErr := lookup.NewCalculator().CalculateCellsBatch(ctx, app, definition, []*core.Record{order}, nil)
	if batchErr != nil {
		t.Fatal(batchErr)
	}
	valuesCell := batch[order.Id][valuesLookup.Definition.Identity.PhysicalName]
	if len(valuesCell.Value.([]any)) != 100 || valuesCell.ProvenanceTotal != 200 || !valuesCell.ProvenanceHasMore {
		t.Fatalf("shared values cell = %#v", valuesCell)
	}
	if got := batch[order.Id][sumLookup.Definition.Identity.PhysicalName].Value; !reflect.DeepEqual(got, lookupAggregationOracle(t, v2.LookupAggregationSum, values)) {
		t.Fatalf("shared sum cell = %#v", got)
	}
}

// TestLookupAggregationReadsNumericFormulaPathSourceAndRejectsText pins the
// numeric source rules at runtime: an already-supported numeric formula
// participates through its stored envelope, while text and boolean values are
// explicit errors rather than silent zeros.
func TestLookupAggregationReadsNumericFormulaPathSourceAndRejectsText(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "公式订单", "form_orders")
	materials := createV2IntegrationTable(t, ctx, app, "公式物料", "form_materials")
	orderName := createV2IntegrationField(t, ctx, app, orders.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), "form_order_name")
	materialName := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), "form_material_name")
	notes := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalText, "备注"), "form_notes")
	formulaDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "税额")
	formulaDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "1.5"}
	tax := createV2IntegrationFormula(t, ctx, app, materials.TableID, formulaDraft, "form_tax")
	link := createV2IntegrationRelation(t, ctx, app, orders.TableID, orderName.FieldID,
		materials.TableID, materialName.FieldID, "明细", "订单", "many", "form_order_lines")

	materialsDefinition, err := schemaapi.New(app).Describe(ctx, materials.TableID)
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(computed.New(
			lookup.NewCalculator(),
			formula.NewCalculator(formula.NewCompiler(formula.DefaultLimits())),
		)),
	)
	ids := []string{}
	for index := range 3 {
		id := fmt.Sprintf("formmaterial%03d", index)
		if _, err := kernel.Apply(ctx, mutationRequest(
			materials.TableID, materialsDefinition.Snapshot.SchemaRevision,
			fmt.Sprintf("form-material-%d", index),
			mutation.Operation{
				Kind: mutation.OperationInsert, RecordID: &id,
				Values: map[string]any{
					materialName.Definition.Identity.PhysicalName: fmt.Sprintf("物料%d", index),
					notes.Definition.Identity.PhysicalName:        "备注",
				},
			},
		)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	orderCollection, err := app.FindCollectionByNameOrId(orders.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	order := core.NewRecord(orderCollection)
	order.Id = "formorder000001"
	order.SetRaw(link.Definition.Identity.PhysicalName, append([]string{}, ids...))
	definition, err := schemaexecution.Describe(ctx, app, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	calculator := lookup.NewCalculator()
	formulaSum := aggregationProbeField("f_formula_sum", v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: link.FieldID}},
		TargetFieldID: tax.FieldID, Aggregation: v2.LookupAggregationSum,
	})
	cell, err := calculator.CalculateFieldPage(ctx, app, definition, order, formulaSum, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if cell.Value != 4.5 || cell.ProvenanceTotal != 3 {
		t.Fatalf("formula-source sum = %#v", cell)
	}
	average := aggregationProbeField("f_formula_avg", v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: link.FieldID}},
		TargetFieldID: tax.FieldID, Aggregation: v2.LookupAggregationAverage,
	})
	if cell, err = calculator.CalculateFieldPage(ctx, app, definition, order, average, 0, 100); err != nil || cell.Value != 1.5 {
		t.Fatalf("formula-source average = %#v, %v", cell.Value, err)
	}

	textSum := aggregationProbeField("f_text_sum", v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: link.FieldID}},
		TargetFieldID: notes.FieldID, Aggregation: v2.LookupAggregationSum,
	})
	_, err = calculator.CalculateFieldPage(ctx, app, definition, order, textSum, 0, 100)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "lookup.aggregation.value_not_numeric" {
		t.Fatalf("text-source sum error = %v", err)
	}
}

// TestLookupAggregationSchemaValidationRejectsNonNumericSources pins the
// cross-table validation seams: field-change planning blocks a numeric summary
// over a text path target, and condition preparation blocks the same for a
// condition target.
func TestLookupAggregationSchemaValidationRejectsNonNumericSources(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "校验订单", "valid_orders")
	materials := createV2IntegrationTable(t, ctx, app, "校验物料", "valid_materials")
	orderName := createV2IntegrationField(t, ctx, app, orders.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), "valid_order_name")
	materialName := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), "valid_material_name")
	link := createV2IntegrationRelation(t, ctx, app, orders.TableID, orderName.FieldID,
		materials.TableID, materialName.FieldID, "明细", "订单", "many", "valid_order_lines")

	draft := fieldDraftForIntegration(t, v2.LogicalLookup, "文本求和")
	draft.Lookup = &v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: link.FieldID}},
		TargetFieldID: materialName.FieldID, Aggregation: v2.LookupAggregationSum,
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	revisions, err := catalog.Revisions(ctx, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: orders.TableID,
		ExpectedSchemaRev: revisions.Schema, Draft: &draft,
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanApply {
		t.Fatalf("text path sum plan was applicable: %#v", plan.Errors)
	}
	found := false
	for _, diagnostic := range plan.Errors {
		if diagnostic.Code == "field.lookup.aggregation_target_not_numeric" {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan diagnostics = %#v", plan.Errors)
	}

	source, err := schemaexecution.Describe(ctx, app, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	spec := v2.LookupSpec{
		Path: []v2.LookupPathStep{}, TargetFieldID: materialName.FieldID,
		Aggregation: v2.LookupAggregationAverage,
		Condition: &v2.LookupCondition{
			SourceTableID: materials.TableID, Match: "all",
			Rules: []v2.LookupConditionRule{{
				SourceFieldID: materialName.FieldID, Operator: "is_not_null",
			}},
		},
	}
	if err := queryschema.ValidateLookupCondition(ctx, app, source, spec); err == nil {
		t.Fatal("text condition average was accepted")
	} else if !strings.Contains(err.Error(), "lookup.aggregation.target_not_numeric") {
		t.Fatalf("condition validation error = %v", err)
	}
}

// TestLookupAggregationMutationRefreshesThroughJobsAndQuery pins the AC
// refresh contract end to end: value changes, duplicate arrivals and removals,
// match entry/exit and the final-match deletion all re-materialize the stored
// aggregate through the production mutation + durable fan-out wiring.
func TestLookupAggregationMutationRefreshesThroughJobsAndQuery(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "刷新订单", "refresh_orders")
	materials := createV2IntegrationTable(t, ctx, app, "刷新物料", "refresh_materials")
	orderCode := createV2IntegrationField(t, ctx, app, orders.TableID, fieldDraftForIntegration(t, v2.LogicalText, "订单编码"), "refresh_order_code")
	sourceCode := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalText, "物料编码"), "refresh_source_code")
	amount := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalNumber, "数量"), "refresh_amount")
	condition := &v2.LookupCondition{
		SourceTableID: materials.TableID, Match: "all",
		Rules: []v2.LookupConditionRule{{
			SourceFieldID: sourceCode.FieldID, Operator: "eq",
			Operand: &v2.LookupOperand{Kind: "field", FieldID: orderCode.FieldID},
		}},
	}
	countDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "非空数")
	countDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{}, TargetFieldID: amount.FieldID,
		Aggregation: v2.LookupAggregationCountNonEmpty, Condition: condition,
	}
	countLookup := createV2IntegrationField(t, ctx, app, orders.TableID, countDraft, "refresh_count_lookup")
	sumDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "求和")
	sumDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{}, TargetFieldID: amount.FieldID,
		Aggregation: v2.LookupAggregationSum, Condition: condition,
	}
	sumLookup := createV2IntegrationField(t, ctx, app, orders.TableID, sumDraft, "refresh_sum_lookup")

	ordersDefinition, err := schemaapi.New(app).Describe(ctx, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	materialsDefinition, err := schemaapi.New(app).Describe(ctx, materials.TableID)
	if err != nil {
		t.Fatal(err)
	}
	jobService := jobs.New(app, nil)
	defer jobService.Shutdown()
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(computed.New(lookup.NewCalculator())),
		mutation.WithPublisher(jobService),
		mutation.WithComputationInvalidator(jobService),
		mutation.WithPublishContext(jobService.PublishContext()),
	)
	jobService.SetKernel(kernel)
	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(app, querySource)
	orderID := "refreshorder001"
	materialID := "refreshmatrl001"
	orderCodeName := orderCode.Definition.Identity.PhysicalName
	sourceCodeName := sourceCode.Definition.Identity.PhysicalName
	amountName := amount.Definition.Identity.PhysicalName
	countName := countLookup.Definition.Identity.PhysicalName
	sumName := sumLookup.Definition.Identity.PhysicalName

	drain := func(stage string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			records, err := app.FindAllRecords("vibetable_jobs")
			if err != nil {
				t.Fatal(err)
			}
			active, failed := []string{}, []string{}
			for _, record := range records {
				switch state := record.GetString("state"); state {
				case "complete":
				case "failed", "cancelled":
					failed = append(failed, record.Id)
				default:
					active = append(active, record.Id+" "+state)
				}
			}
			if len(active) == 0 {
				if len(failed) > 0 {
					t.Fatalf("%s: recalculation jobs failed: %v", stage, failed)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: recalculation jobs did not settle: %v", stage, active)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	apply := func(stage string, table v2IntegrationTable, revision string, key string, operation mutation.Operation) {
		t.Helper()
		if _, err := kernel.Apply(ctx, mutationRequest(table.TableID, revision, key, operation)); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		drain(stage)
	}
	assertAggregate := func(stage string, wantCount, wantSum any) {
		t.Helper()
		record, err := app.FindRecordById(orders.PhysicalName, orderID)
		if err != nil {
			t.Fatalf("%s: reload order: %v", stage, err)
		}
		if got := relatedcomputation.ProjectStored(record.GetRaw(countName)); got != wantCount {
			t.Fatalf("%s: stored countNonEmpty = %#v, want %#v", stage, got, wantCount)
		}
		if got := relatedcomputation.ProjectStored(record.GetRaw(sumName)); got != wantSum {
			t.Fatalf("%s: stored sum = %#v, want %#v", stage, got, wantSum)
		}
	}

	// Zero matches: counts and sum stay zero (never null for count/sum).
	apply("order insert", orders, ordersDefinition.Snapshot.SchemaRevision, "refresh-order-insert",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &orderID,
			Values: map[string]any{orderCodeName: "MAT-1"}})
	assertAggregate("zero matches", float64(0), float64(0))

	// Match entry with an explicit zero value: zero is non-empty.
	apply("material insert", materials, materialsDefinition.Snapshot.SchemaRevision, "refresh-material-insert",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &materialID,
			Values: map[string]any{sourceCodeName: "MAT-1", amountName: float64(0)}})
	assertAggregate("match entered", float64(1), float64(0))

	// Business value change propagates.
	apply("value change", materials, materialsDefinition.Snapshot.SchemaRevision, "refresh-material-revalue",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &materialID,
			Values: map[string]any{amountName: float64(7)}})
	assertAggregate("value changed", float64(1), float64(7))

	// Duplicate arrival: a second matching row joins the set.
	secondID := "refreshmatrl002"
	apply("duplicate insert", materials, materialsDefinition.Snapshot.SchemaRevision, "refresh-material-second",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &secondID,
			Values: map[string]any{sourceCodeName: "MAT-1", amountName: float64(7)}})
	assertAggregate("duplicate arrived", float64(2), float64(14))

	// Match exit clears one row.
	apply("match exit", materials, materialsDefinition.Snapshot.SchemaRevision, "refresh-material-unmatch",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &secondID,
			Values: map[string]any{sourceCodeName: "MAT-9"}})
	assertAggregate("match exited", float64(1), float64(7))

	// Final-match deletion returns the empty-set contract.
	apply("final delete", materials, materialsDefinition.Snapshot.SchemaRevision, "refresh-material-delete",
		mutation.Operation{Kind: mutation.OperationDelete, RecordID: &materialID})
	assertAggregate("last match deleted", float64(0), float64(0))

	// The query port exposes the aggregates as numbers usable for filtering.
	page, err := port.QueryPage(ctx, orders.TableID, query.TableQuery{
		Limit: 10,
		Filters: []query.FilterExpression{{
			Field: sumName, Operator: query.OperatorLessEq, Value: float64(0),
		}},
		Sorts: []query.SortCondition{{Field: countName, Direction: query.SortDescending}},
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range page.Rows {
		if fmt.Sprint(row["id"]) == orderID {
			found = true
			if fmt.Sprint(row[sumName]) != "0" {
				t.Fatalf("query-port sum = %#v", row[sumName])
			}
		}
	}
	if !found {
		t.Fatalf("numeric filter dropped the aggregated row: %#v", page.Rows)
	}
}

// TestLookupAggregationEnforcesBudgetAndCancellation pins that complete-set
// aggregation stays bounded by the shared materialization budget and honors
// cancellation — no silent truncation, no raised limits.
func TestLookupAggregationEnforcesBudgetAndCancellation(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "预算订单", "budget_orders")
	materials := createV2IntegrationTable(t, ctx, app, "预算物料", "budget_materials")
	orderName := createV2IntegrationField(t, ctx, app, orders.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), "budget_order_name")
	materialName := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), "budget_material_name")
	notes := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalText, "备注"), "budget_notes")
	link := createV2IntegrationRelation(t, ctx, app, orders.TableID, orderName.FieldID,
		materials.TableID, materialName.FieldID, "明细", "订单", "many", "budget_order_lines")

	materialCollection, err := app.FindCollectionByNameOrId(materials.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	materialCollection.Fields.GetByName(notes.Definition.Identity.PhysicalName).(*core.TextField).Max = 2 << 20
	if err := app.Save(materialCollection); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for index := range 32 {
		record := core.NewRecord(materialCollection)
		record.Id = fmt.Sprintf("budgetmatrl%04d", index)
		record.Set(notes.Definition.Identity.PhysicalName, strings.Repeat("x", 1<<20))
		record.Set(notes.Definition.Value.Presence.PhysicalName, true)
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, record.Id)
	}
	orderCollection, err := app.FindCollectionByNameOrId(orders.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	order := core.NewRecord(orderCollection)
	order.Id = "budgetorder0001"
	order.SetRaw(link.Definition.Identity.PhysicalName, ids)
	definition, err := schemaexecution.Describe(ctx, app, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	calculator := lookup.NewCalculator()
	countField := aggregationProbeField("f_budget_count", v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: link.FieldID}},
		TargetFieldID: notes.FieldID, Aggregation: v2.LookupAggregationCountRecords,
	})
	_, err = calculator.CalculateFieldPage(ctx, app, definition, order, countField, 0, 100)
	var budgetError *mutation.ProductError
	if !errors.As(err, &budgetError) || budgetError.Code != "lookup.value.too_expensive" {
		t.Fatalf("aggregation budget = %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = calculator.CalculateFieldPage(cancelled, app, definition, order, countField, 0, 100)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("aggregation cancellation = %v", err)
	}
}

// TestLookupAggregationRejectsStaleComputedSources pins the AC5 boundary for
// computed sources: a numeric formula whose stored envelope no longer matches
// its row revision surfaces an explicit dependency error instead of feeding
// the old cached value into the aggregate.
func TestLookupAggregationRejectsStaleComputedSources(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "过期订单", "stale_orders")
	materials := createV2IntegrationTable(t, ctx, app, "过期物料", "stale_materials")
	orderName := createV2IntegrationField(t, ctx, app, orders.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), "stale_order_name")
	materialName := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), "stale_material_name")
	formulaDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "税额")
	formulaDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "2.0"}
	tax := createV2IntegrationFormula(t, ctx, app, materials.TableID, formulaDraft, "stale_tax")
	link := createV2IntegrationRelation(t, ctx, app, orders.TableID, orderName.FieldID,
		materials.TableID, materialName.FieldID, "明细", "订单", "many", "stale_order_lines")

	materialsDefinition, err := schemaapi.New(app).Describe(ctx, materials.TableID)
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(computed.New(
			lookup.NewCalculator(),
			formula.NewCalculator(formula.NewCompiler(formula.DefaultLimits())),
		)),
	)
	id := "stalematrl00001"
	if _, err := kernel.Apply(ctx, mutationRequest(
		materials.TableID, materialsDefinition.Snapshot.SchemaRevision, "stale-material-insert",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &id,
			Values: map[string]any{materialName.Definition.Identity.PhysicalName: "物料"}},
	)); err != nil {
		t.Fatal(err)
	}
	orderCollection, err := app.FindCollectionByNameOrId(orders.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	order := core.NewRecord(orderCollection)
	order.Id = "staleorder00001"
	order.SetRaw(link.Definition.Identity.PhysicalName, []string{id})
	definition, err := schemaexecution.Describe(ctx, app, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	sumField := aggregationProbeField("f_stale_sum", v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: link.FieldID}},
		TargetFieldID: tax.FieldID, Aggregation: v2.LookupAggregationSum,
	})
	calculator := lookup.NewCalculator()
	if cell, err := calculator.CalculateFieldPage(ctx, app, definition, order, sumField, 0, 100); err != nil || cell.Value != 2.0 {
		t.Fatalf("fresh formula source sum = %#v, %v", cell.Value, err)
	}
	// Bypass the kernel so the stored formula envelope stops matching its row
	// revision; the aggregate must fail closed with a dependency error.
	if _, err := app.DB().NewQuery(fmt.Sprintf(
		"UPDATE `%s` SET `__vt_row_revision`=`__vt_row_revision`+1 WHERE `id`={:id}", materials.PhysicalName,
	)).Bind(dbx.Params{"id": id}).Execute(); err != nil {
		t.Fatal(err)
	}
	_, err = calculator.CalculateFieldPage(ctx, app, definition, order, sumField, 0, 100)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "lookup.aggregation.source_stale" {
		t.Fatalf("stale formula source error = %v", err)
	}
}

// TestLookupAggregationDescribeReportsDecimalForNumericSummaries pins the
// public typing seam: numeric aggregations are decimal number/one outputs
// regardless of the source field's own storage, so countRecords over text and
// average over an integer-storage number both describe as decimal.
func TestLookupAggregationDescribeReportsDecimalForNumericSummaries(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "类型订单", "typedesc_orders")
	materials := createV2IntegrationTable(t, ctx, app, "类型物料", "typedesc_materials")
	orderName := createV2IntegrationField(t, ctx, app, orders.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), "typedesc_order_name")
	materialName := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), "typedesc_material_name")
	notes := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalText, "备注"), "typedesc_notes")
	integerDraft := fieldDraftForIntegration(t, v2.LogicalNumber, "整数数量")
	integerDraft.Storage.Options.OnlyInt = true
	integerAmount := createV2IntegrationField(t, ctx, app, materials.TableID, integerDraft, "typedesc_integer_amount")
	link := createV2IntegrationRelation(t, ctx, app, orders.TableID, orderName.FieldID,
		materials.TableID, materialName.FieldID, "明细", "订单", "many", "typedesc_order_lines")

	countDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "备注计数")
	countDraft.Lookup = &v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: link.FieldID}},
		TargetFieldID: notes.FieldID, Aggregation: v2.LookupAggregationCountRecords,
	}
	countLookup := createV2IntegrationField(t, ctx, app, orders.TableID, countDraft, "typedesc_count_lookup")
	averageDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "整数平均")
	averageDraft.Lookup = &v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: link.FieldID}},
		TargetFieldID: integerAmount.FieldID, Aggregation: v2.LookupAggregationAverage,
	}
	averageLookup := createV2IntegrationField(t, ctx, app, orders.TableID, averageDraft, "typedesc_average_lookup")

	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	catalogResult, err := relation.New(app, query.NewPort(app, querySource), nil).Describe(ctx, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	byPhysical := map[string]relation.LookupDescriptor{}
	for _, lookup := range catalogResult.Lookups {
		byPhysical[lookup.PhysicalName] = lookup
	}
	count, found := byPhysical[countLookup.Definition.Identity.PhysicalName]
	if !found {
		t.Fatalf("count lookup missing from catalog: %#v", catalogResult.Lookups)
	}
	if count.OutputStorage != "decimal" || count.ResultCardinality != "one" || count.Aggregation != v2.LookupAggregationCountRecords {
		t.Fatalf("text countRecords descriptor = %#v", count)
	}
	average, found := byPhysical[averageLookup.Definition.Identity.PhysicalName]
	if !found {
		t.Fatalf("average lookup missing from catalog: %#v", catalogResult.Lookups)
	}
	if average.OutputStorage != "decimal" || average.ResultCardinality != "one" || average.Aggregation != v2.LookupAggregationAverage {
		t.Fatalf("integer average descriptor = %#v", average)
	}
}
