package sourceimport

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// sourceSemanticsSnapshot freezes the display-semantics subset of a provider
// snapshot: temporal zones, currency/percent number formats, and the lookup and
// system source kinds that need an explicit user strategy. Zero/false/null/
// empty/code/precision cases are covered by values_test.go and are not
// repeated here.
func sourceSemanticsSnapshot() Snapshot {
	number := func(id string, format NumberFormat) Field {
		return Field{ID: id, Name: id, Kind: "number", ValueKind: v2.LogicalNumber, NumberFormat: &format}
	}
	return Snapshot{
		Contract: Contract, Provider: "prov-cloud", ContainerID: "semantics",
		DisplayName: "语义迁移", Version: "v1",
		ReadWindow: ReadWindow{StartedAt: "2026-10-01T08:00:00Z", FinishedAt: "2026-10-01T08:05:00Z", Consistency: "snapshot"},
		Tables: []Table{{
			ID: "src_sem", Name: "语义", Version: "tv1", PrimaryFieldID: "f_title",
			Fields: []Field{
				{ID: "f_title", Name: "Title", Kind: "text", ValueKind: v2.LogicalText},
				{ID: "f_day", Name: "Day", Kind: "date", ValueKind: v2.LogicalDate, Timezone: "Asia/Shanghai"},
				{ID: "f_when", Name: "When", Kind: "dateTime", ValueKind: v2.LogicalDateTime, Timezone: "America/New_York"},
				{ID: "f_plain_when", Name: "PlainWhen", Kind: "dateTime", ValueKind: v2.LogicalDateTime},
				number("f_price", NumberFormat{DisplayScale: 2, ScaleMode: "fixed", UseGrouping: true,
					Currency: "EUR", PercentStorage: "ratio"}),
				number("f_rate", NumberFormat{DisplayScale: 1, ScaleMode: "max", TrimTrailingZeros: true,
					Currency: "USD", PercentStorage: "percent"}),
				number("f_units", NumberFormat{OnlyInt: true, DisplayScale: 0, ScaleMode: "fixed", UseGrouping: true,
					Currency: "CNY", PercentStorage: "ratio"}),
				{ID: "f_rollup", Name: "Rollup", Kind: "lookup", ValueKind: v2.LogicalLookup, Definition: "SUM(orders.amount)"},
				{ID: "f_created", Name: "Created", Kind: "system", ValueKind: v2.LogicalJSON, Definition: "created_time"},
			},
			Records: []Record{{ID: "r1", Values: map[string]any{
				"f_title": "S1", "f_day": "2026-03-01", "f_when": "2026-03-01T08:30:05+08:00",
				"f_plain_when": "2026-02-28T23:59:59Z", "f_price": json.Number("1234.5"),
				"f_rate": json.Number("0.15"), "f_units": json.Number("801"),
				"f_rollup": "1234.5", "f_created": "2026-01-01T00:00:00Z",
			}}},
		}},
	}
}

func sourceSemanticsOptions() Options {
	return Options{SelectedTableIDs: []string{"src_sem"}}
}

func TestPreviewFreezesTemporalZoneAndNumberDisplaySemantics(t *testing.T) {
	snapshot := sourceSemanticsSnapshot()
	// The temporal/number assertions are orthogonal to the lookup and system
	// kinds, so their confirmed strategies only unblock this fixture.
	options := sourceSemanticsOptions()
	options.Decisions = []Decision{
		{TableID: "src_sem", FieldID: "f_rollup", Policy: PolicySnapshot, TargetKind: v2.LogicalText, Confirmed: true},
		{TableID: "src_sem", FieldID: "f_created", Policy: PolicySkip, Confirmed: true},
	}
	plan, err := Preview(context.Background(), snapshot, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The shared FieldValueKernel validates every planned value in preview;
	// a blocking result would mean the kernel rejected the semantics path.
	if !plan.CanApply || blockingCount(plan) != 0 {
		t.Fatalf("native temporal/number semantics blocked: %#v", plan.Diagnostics)
	}
	// Source timezone declarations survive into the reviewed drafts; a field
	// without one keeps the recommended default instead of an invented zone.
	if day := planFieldOf(t, plan, "src_sem", "f_day"); day.Draft.Display.Timezone != "Asia/Shanghai" {
		t.Fatalf("date timezone was not retained: %#v", day.Draft.Display)
	}
	when := planFieldOf(t, plan, "src_sem", "f_when")
	if when.Draft.Display.Timezone != "America/New_York" {
		t.Fatalf("dateTime timezone was not retained: %#v", when.Draft.Display)
	}
	if plain := planFieldOf(t, plan, "src_sem", "f_plain_when"); plain.Draft.Display.Timezone != "system" {
		t.Fatalf("undeclared timezone left the recommended default: %#v", plain.Draft.Display)
	}
	price := planFieldOf(t, plan, "src_sem", "f_price")
	if price.Draft.Display.Currency != "EUR" || price.Draft.Display.DisplayScale != 2 ||
		price.Draft.Display.ScaleMode != "fixed" || !price.Draft.Display.UseGrouping ||
		price.Draft.Display.PercentStorage != "ratio" || price.Draft.Storage.Options.OnlyInt {
		t.Fatalf("currency number format was not frozen into the draft: %#v", price.Draft)
	}
	rate := planFieldOf(t, plan, "src_sem", "f_rate")
	if rate.Draft.Display.PercentStorage != "percent" || rate.Draft.Display.DisplayScale != 1 ||
		rate.Draft.Display.ScaleMode != "max" || !rate.Draft.Display.TrimTrailingZeros ||
		rate.Draft.Display.Currency != "USD" {
		t.Fatalf("percent number format was not frozen into the draft: %#v", rate.Draft)
	}
	units := planFieldOf(t, plan, "src_sem", "f_units")
	if !units.Draft.Storage.Options.OnlyInt || units.Draft.Display.DisplayScale != 0 ||
		units.Draft.Display.ScaleMode != "fixed" {
		t.Fatalf("integer number format was not frozen into the draft: %#v", units.Draft)
	}
	// The shared kernel owns the final canonical value: zones never rewrite
	// storage. An offset source instant canonicalizes to UTC, a zoneless date
	// stays a calendar date, and currency/percent formats never rescale numbers.
	kernel := fieldvalue.New()
	for _, test := range []struct {
		fieldID string
		want    any
	}{
		{"f_day", "2026-03-01"},
		{"f_when", "2026-03-01T00:30:05Z"},
		{"f_plain_when", "2026-02-28T23:59:59Z"},
		{"f_price", float64(1234.5)},
		{"f_rate", float64(0.15)},
		{"f_units", float64(801)},
	} {
		field := planFieldOf(t, plan, "src_sem", test.fieldID)
		definition, err := previewDefinition(context.Background(), field)
		if err != nil {
			t.Fatalf("%s definition: %v", test.fieldID, err)
		}
		normalized, err := kernel.NormalizeWrite(context.Background(), definition, fieldvalue.Insert,
			fieldvalue.Input{Supplied: true, Value: snapshot.Tables[0].Records[0].Values[test.fieldID]})
		if err != nil || !normalized.Write {
			t.Fatalf("%s kernel normalization: %#v, %v", test.fieldID, normalized, err)
		}
		if normalized.ProductValue != test.want {
			t.Fatalf("%s canonical = %#v, want %#v", test.fieldID, normalized.ProductValue, test.want)
		}
	}
}

func TestPreviewBlocksNativeLookupAndSystemKindsUntilConfirmed(t *testing.T) {
	native, err := Preview(context.Background(), sourceSemanticsSnapshot(), sourceSemanticsOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if native.CanApply ||
		!fieldBlocked(native, "f_rollup", "source_import.field_strategy") ||
		!fieldBlocked(native, "f_created", "source_import.field_strategy") {
		t.Fatalf("native lookup/system migration must block: %#v", native.Diagnostics)
	}
	// Snapshots may only target ordinary writable kinds: redirecting a lookup
	// onto another read-only kind is not a value snapshot.
	readonly := sourceSemanticsOptions()
	readonly.Decisions = []Decision{{TableID: "src_sem", FieldID: "f_rollup",
		Policy: PolicySnapshot, TargetKind: v2.LogicalLookup, Confirmed: true}}
	plan, err := Preview(context.Background(), sourceSemanticsSnapshot(), readonly, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanApply || !fieldBlocked(plan, "f_rollup", "source_import.field_strategy") {
		t.Fatalf("snapshot onto a read-only kind must block: %#v", plan.Diagnostics)
	}
	// Confirmed snapshot-to-text and confirmed skip resolve both strategies and
	// retain the source expression in the durable provenance shape.
	resolved := sourceSemanticsOptions()
	resolved.Decisions = []Decision{
		{TableID: "src_sem", FieldID: "f_rollup", Policy: PolicySnapshot, TargetKind: v2.LogicalText, Confirmed: true},
		{TableID: "src_sem", FieldID: "f_created", Policy: PolicySkip, Confirmed: true},
	}
	plan, err = Preview(context.Background(), sourceSemanticsSnapshot(), resolved, nil)
	if err != nil || !plan.CanApply || blockingCount(plan) != 0 {
		t.Fatalf("confirmed strategies must resolve every blocker: %v %#v", err, plan.Diagnostics)
	}
	rollup := planFieldOf(t, plan, "src_sem", "f_rollup")
	if rollup.Policy != PolicySnapshot || rollup.Draft.LogicalType != v2.LogicalText ||
		rollup.Source.Definition != "SUM(orders.amount)" {
		t.Fatalf("lookup snapshot was not planned as retained text: %#v", rollup)
	}
	created := planFieldOf(t, plan, "src_sem", "f_created")
	if created.Policy != PolicySkip {
		t.Fatalf("system field was not skipped: %#v", created)
	}
	summary := map[string]FieldSummary{}
	for _, field := range plan.Fields {
		summary[field.Source.FieldID] = field
	}
	if summary["f_rollup"].Policy != PolicySnapshot || summary["f_rollup"].Definition != "SUM(orders.amount)" ||
		summary["f_created"].Policy != PolicySkip {
		t.Fatalf("durable provenance lost the confirmed strategies: %#v", plan.Fields)
	}
}

// sourceSemanticsFanOutSnapshot builds the three-table topology used for the
// >399-target fan-out regression: one hub record pointing at every target row,
// a reciprocal many pair, and a one-cardinality detail pair.
func sourceSemanticsFanOutSnapshot(targets int) Snapshot {
	text := func(id string) Field {
		return Field{ID: id, Name: id, Kind: "text", ValueKind: v2.LogicalText}
	}
	relation := func(id, target, reverse, cardinality string) Field {
		return Field{ID: id, Name: id, Kind: "relation", ValueKind: v2.LogicalRelation,
			Relation: &Relation{TargetTableID: target, TargetFieldID: reverse, Cardinality: cardinality}}
	}
	refs := make([]string, targets)
	rows := make([]Record, targets)
	for index := 0; index < targets; index++ {
		id := fmt.Sprintf("t%04d", index+1)
		refs[index] = id
		rows[index] = Record{ID: id, Values: map[string]any{
			"title": fmt.Sprintf("目标 %d", index+1), "code": fmt.Sprintf("b-%04d", index+1), "ba": []string{"hub"},
		}}
	}
	return Snapshot{
		Contract: Contract, Provider: "prov-cloud", ContainerID: "fanout",
		DisplayName: "扇出预检", Version: "v1",
		ReadWindow: ReadWindow{StartedAt: "2026-10-01T08:00:00Z", FinishedAt: "2026-10-01T08:05:00Z", Consistency: "snapshot"},
		Tables: []Table{
			{ID: "a", Name: "扇出主表", Version: "a1", PrimaryFieldID: "title",
				Fields: []Field{text("title"), text("code"), relation("ab", "b", "ba", "many"), relation("ac", "c", "ca", "many")},
				Records: []Record{{ID: "hub", Values: map[string]any{
					"title": "中心记录", "code": "a-hub", "ab": refs, "ac": []string{"c1", "c2"},
				}}}},
			{ID: "b", Name: "扇出目标", Version: "b1", PrimaryFieldID: "title",
				Fields:  []Field{text("title"), text("code"), relation("ba", "a", "ab", "many")},
				Records: rows},
			{ID: "c", Name: "扇出明细", Version: "c1", PrimaryFieldID: "title",
				Fields: []Field{text("title"), text("code"), relation("ca", "a", "ac", "one")},
				Records: []Record{
					{ID: "c1", Values: map[string]any{"title": "明细一", "code": "c-0001", "ca": "hub"}},
					{ID: "c2", Values: map[string]any{"title": "明细二", "code": "c-0002", "ca": "hub"}},
				}},
		},
	}
}

func TestPreviewAdmitsOversizedSingleRecordRelationFanOut(t *testing.T) {
	const targets = 801
	snapshot := sourceSemanticsFanOutSnapshot(targets)
	options := Options{SelectedTableIDs: []string{"a", "b", "c"}, ConfirmReverse: true,
		TargetNames: []TargetName{
			{TableID: "a", Name: "迁移主表"}, {TableID: "b", Name: "迁移目标"}, {TableID: "c", Name: "迁移明细"},
		}}
	plan, err := Preview(context.Background(), snapshot, options, nil)
	if err != nil || !plan.CanApply || blockingCount(plan) != 0 {
		t.Fatalf("oversized fan-out must stay previewable: %v %#v", err, plan.Diagnostics)
	}
	// The frozen plan keeps the complete edge set verbatim; preview never
	// truncates, samples or splits a single record's relation list.
	for _, table := range plan.Tables {
		if table.SourceID != "a" {
			continue
		}
		if len(table.Records) != 1 {
			t.Fatalf("hub table planned %d records", len(table.Records))
		}
		refs, ok := table.Records[0].Values["ab"].([]any)
		if !ok || len(refs) != targets {
			t.Fatalf("planned hub edge set = %#T with %d entries, want %d strings", table.Records[0].Values["ab"], len(refs), targets)
		}
		seen := map[string]bool{}
		for _, ref := range refs {
			id, ok := ref.(string)
			if !ok || seen[id] {
				t.Fatalf("planned edge set contains a non-string or duplicate: %#v", ref)
			}
			seen[id] = true
		}
		for index := 1; index <= targets; index++ {
			if !seen[fmt.Sprintf("t%04d", index)] {
				t.Fatalf("planned edge set lost target t%04d", index)
			}
		}
	}
}
