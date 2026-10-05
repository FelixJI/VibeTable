package sourceimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func textField(id string) Field {
	return Field{ID: id, Name: "Field " + id, Kind: "text", ValueKind: v2.LogicalText}
}

func relationField(id, target, reverse, cardinality string, required bool) Field {
	return Field{ID: id, Name: "Field " + id, Kind: string(v2.LogicalRelation),
		ValueKind: v2.LogicalRelation, Required: required,
		Relation: &Relation{TargetTableID: target, TargetFieldID: reverse, Cardinality: cardinality}}
}

// relationSnapshot covers the hardest CP1 shape: a three-table relation cycle
// (projects→people→tasks→projects), one bidirectional one/many pair, multi
// value relations and composite record identities ("r1"/"r2" reused in every
// table without cross-table conflation).
func relationSnapshot() Snapshot {
	projects := Table{ID: "src_projects", Name: "Projects", Version: "tv1", PrimaryFieldID: "f_title",
		Fields: []Field{
			textField("f_title"),
			relationField("f_owner", "src_people", "f_projects", "one", true),
			relationField("f_peer", "src_people", "", "many", false),
		},
		Records: []Record{
			{ID: "r1", Values: map[string]any{"f_title": "P1", "f_owner": "r1", "f_peer": []any{"r1", "r2"}}},
			{ID: "r2", Values: map[string]any{"f_title": "P2", "f_owner": "r2", "f_peer": []any{"r2"}}},
		}}
	people := Table{ID: "src_people", Name: "People", Version: "tv1", PrimaryFieldID: "f_name",
		Fields: []Field{
			textField("f_name"),
			relationField("f_projects", "src_projects", "f_owner", "many", false),
			relationField("f_peer", "src_tasks", "", "many", false),
		},
		Records: []Record{
			{ID: "r1", Values: map[string]any{"f_name": "Alice", "f_projects": []any{"r1"}, "f_peer": []any{"r1", "r2"}}},
			{ID: "r2", Values: map[string]any{"f_name": "Bob", "f_projects": []any{"r2"}, "f_peer": []any{"r2"}}},
		}}
	tasks := Table{ID: "src_tasks", Name: "Tasks", Version: "tv1", PrimaryFieldID: "f_title",
		Fields: []Field{
			textField("f_title"),
			relationField("f_assignees", "src_people", "", "many", false),
			relationField("f_peer", "src_projects", "", "many", false),
		},
		Records: []Record{
			{ID: "r1", Values: map[string]any{"f_title": "T1", "f_assignees": []any{"r1", "r2"}, "f_peer": []any{"r1"}}},
			{ID: "r2", Values: map[string]any{"f_title": "T2", "f_assignees": []any{"r2"}, "f_peer": []any{"r2"}}},
		}}
	return Snapshot{
		Contract: Contract, Provider: "prov-cloud", ContainerID: "space-1",
		DisplayName: "云端空间", Version: "v2026.10.01",
		ReadWindow: ReadWindow{StartedAt: "2026-10-01T08:00:00Z", FinishedAt: "2026-10-01T08:05:00Z", Consistency: "snapshot"},
		Tables:     []Table{projects, people, tasks},
	}
}

func allSelectedOptions() Options {
	return Options{
		SelectedTableIDs: []string{"src_projects", "src_people", "src_tasks"},
		ConfirmReverse:   true,
		// Two one-sided relations target src_people; one reciprocal keeps the
		// default name and the other is renamed through an explicit decision.
		Decisions: []Decision{{
			TableID: "src_projects", FieldID: "f_peer", Policy: PolicyNative,
			TargetName: "人员关联项目",
		}},
	}
}

func cloneSnapshot(t *testing.T, snapshot Snapshot) Snapshot {
	t.Helper()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var copy Snapshot
	if err := decoder.Decode(&copy); err != nil {
		t.Fatal(err)
	}
	return copy
}

func planFieldOf(t *testing.T, plan Plan, tableID, fieldID string) FieldPlan {
	t.Helper()
	for _, table := range plan.Tables {
		if table.SourceID != tableID {
			continue
		}
		for _, field := range table.Fields {
			if field.Source.ID == fieldID {
				return field
			}
		}
	}
	t.Fatalf("plan field %s/%s was not planned", tableID, fieldID)
	return FieldPlan{}
}

func hasBlocking(plan Plan, code string) bool {
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Code == code && diagnostic.Blocking {
			return true
		}
	}
	return false
}

func blockingCount(plan Plan) int {
	total := 0
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Blocking {
			total++
		}
	}
	return total
}

func TestPreviewPlansCyclicBidirectionalAndMultiValueRelations(t *testing.T) {
	plan, err := Preview(context.Background(), relationSnapshot(), allSelectedOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply || blockingCount(plan) != 0 {
		t.Fatalf("relation migration blocked: %#v", plan.Diagnostics)
	}
	// Plans are ordered by stable source identity, not provider order.
	if len(plan.Tables) != 3 ||
		plan.Tables[0].SourceID != "src_people" ||
		plan.Tables[1].SourceID != "src_projects" ||
		plan.Tables[2].SourceID != "src_tasks" {
		t.Fatalf("unexpected table order: %#v", plan.Tables)
	}
	owner := planFieldOf(t, plan, "src_projects", "f_owner")
	if !owner.Deferred || owner.Draft.Relation == nil ||
		owner.Draft.Relation.TargetTableID != "src_people" ||
		owner.Draft.Relation.Cardinality != "one" ||
		owner.Draft.Value.Required {
		t.Fatalf("required relation must defer edge writes without insert-time required: %#v", owner)
	}
	assignees := planFieldOf(t, plan, "src_tasks", "f_assignees")
	if assignees.Draft.Relation == nil || assignees.Draft.Relation.Cardinality != "many" {
		t.Fatalf("multi value relation lost cardinality: %#v", assignees)
	}
	// Composite identities: "r1"/"r2" in each table stay distinct; shared ids
	// resolve per target table without relation_target diagnostics.
	if hasBlocking(plan, "source_import.relation_target") || hasBlocking(plan, "source_import.relation_edges") {
		t.Fatalf("composite identity conflation: %#v", plan.Diagnostics)
	}
	if len(plan.Fields) != 9 {
		t.Fatalf("provenance summaries = %d, want one per selected field", len(plan.Fields))
	}
	for _, summary := range plan.Fields {
		if summary.Source.Provider != "prov-cloud" || summary.Source.ContainerID != "space-1" ||
			summary.Source.TableID == "" || summary.Source.FieldID == "" ||
			summary.Policy != PolicyNative || summary.Kind == "" {
			t.Fatalf("incomplete provenance summary: %#v", summary)
		}
	}
}

func TestPreviewBlocksRelationSelectionTargetsAndInconsistentEdges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Snapshot, *Options)
		code   string
	}{
		{
			name: "relation target table not selected",
			mutate: func(_ *Snapshot, options *Options) {
				options.SelectedTableIDs = []string{"src_projects"}
			},
			code: "source_import.relation_selection",
		},
		{
			name: "referenced record does not exist",
			mutate: func(snapshot *Snapshot, _ *Options) {
				snapshot.Tables[0].Records[0].Values["f_peer"] = []any{"r1", "missing"}
			},
			code: "source_import.relation_target",
		},
		{
			name: "bidirectional reverse edge missing",
			mutate: func(snapshot *Snapshot, _ *Options) {
				snapshot.Tables[1].Records[0].Values["f_projects"] = []any{}
			},
			code: "source_import.relation_edges",
		},
		{
			name: "reverse field creation unconfirmed",
			mutate: func(_ *Snapshot, options *Options) {
				options.ConfirmReverse = false
			},
			code: "source_import.reverse_confirmation",
		},
		{
			name: "relation field used as primary display",
			mutate: func(snapshot *Snapshot, _ *Options) {
				snapshot.Tables[1].PrimaryFieldID = "f_projects"
			},
			code: "source_import.primary_field",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := cloneSnapshot(t, relationSnapshot())
			options := allSelectedOptions()
			test.mutate(&snapshot, &options)
			plan, err := Preview(context.Background(), snapshot, options, nil)
			if err != nil {
				t.Fatal(err)
			}
			if plan.CanApply || !hasBlocking(plan, test.code) {
				t.Fatalf("expected blocking %s, got canApply=%t diagnostics=%#v", test.code, plan.CanApply, plan.Diagnostics)
			}
		})
	}
}

func renameSnapshot() Snapshot {
	return Snapshot{
		Contract: Contract, Provider: "prov-cloud", ContainerID: "space-1",
		DisplayName: "云端空间", Version: "v2026.10.01",
		ReadWindow: ReadWindow{StartedAt: "2026-10-01T08:00:00Z", FinishedAt: "2026-10-01T08:05:00Z", Consistency: "snapshot"},
		Tables: []Table{
			{ID: "src_a", Name: "Orders", Version: "tv1", PrimaryFieldID: "f_name",
				Fields:  []Field{textField("f_name")},
				Records: []Record{{ID: "r1", Values: map[string]any{"f_name": "A1"}}}},
			{ID: "src_b", Name: "Orders", Version: "tv1", PrimaryFieldID: "f_name",
				Fields: []Field{textField("f_name"),
					relationField("f_link", "src_a", "", "one", false)},
				Records: []Record{{ID: "r1", Values: map[string]any{"f_name": "B1", "f_link": "r1"}}}},
		},
	}
}

func renameOptions() Options {
	return Options{
		SelectedTableIDs: []string{"src_a", "src_b"},
		TargetNames: []TargetName{
			{TableID: "src_a", Name: "Orders A"},
			{TableID: "src_b", Name: "Orders B"},
		},
		ConfirmReverse: true,
	}
}

func TestPreviewRequiresExplicitRenameAndKeepsCompositeIdentities(t *testing.T) {
	options := renameOptions()
	options.TargetNames = nil
	plan, err := Preview(context.Background(), renameSnapshot(), options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanApply || !hasBlocking(plan, "source_import.target_name") {
		t.Fatalf("duplicate names must require explicit rename: %#v", plan.Diagnostics)
	}
	renamed, err := Preview(context.Background(), renameSnapshot(), renameOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !renamed.CanApply || blockingCount(renamed) != 0 {
		t.Fatalf("explicit rename must unblock: %#v", renamed.Diagnostics)
	}
	if renamed.Tables[0].Name != "Orders A" || renamed.Tables[1].Name != "Orders B" {
		t.Fatalf("renamed targets = %#v", renamed.Tables)
	}
	// The same record id "r1" exists in both tables; the relation must resolve
	// against src_a's r1 instead of conflating identities.
	if hasBlocking(renamed, "source_import.relation_target") {
		t.Fatalf("composite identity was conflated: %#v", renamed.Diagnostics)
	}
}

func TestPreviewBlocksUnknownNamingAndDecisionReferences(t *testing.T) {
	tests := []struct {
		name    string
		options func() Options
		code    string
	}{
		{
			name: "target name references unselected table",
			options: func() Options {
				options := renameOptions()
				options.TargetNames = append(options.TargetNames, TargetName{TableID: "src_c", Name: "Ghost"})
				return options
			},
			code: "source_import.target_name_unknown",
		},
		{
			name: "duplicate target naming",
			options: func() Options {
				options := renameOptions()
				options.TargetNames = append(options.TargetNames, TargetName{TableID: "src_a", Name: "Again"})
				return options
			},
			code: "source_import.duplicate_target_name",
		},
		{
			name: "decision references unselected table",
			options: func() Options {
				options := renameOptions()
				options.Decisions = append(options.Decisions,
					Decision{TableID: "src_c", FieldID: "f_x", Policy: PolicySkip, Confirmed: true})
				return options
			},
			code: "source_import.decision_unknown",
		},
		{
			name: "decision references unknown field",
			options: func() Options {
				options := renameOptions()
				options.Decisions = append(options.Decisions,
					Decision{TableID: "src_a", FieldID: "f_missing", Policy: PolicySkip, Confirmed: true})
				return options
			},
			code: "source_import.decision_unknown",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := Preview(context.Background(), renameSnapshot(), test.options(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if plan.CanApply || !hasBlocking(plan, test.code) {
				t.Fatalf("expected blocking %s, got canApply=%t diagnostics=%#v", test.code, plan.CanApply, plan.Diagnostics)
			}
		})
	}
}

func TestPreviewTruncationKeepsBlockingFactsAndExplainsItself(t *testing.T) {
	snapshot := renameSnapshot()
	options := renameOptions()
	for index := range 150 {
		options.Decisions = append(options.Decisions, Decision{
			TableID: "src_a", FieldID: fmt.Sprintf("f_unknown_%03d", index),
			Policy: PolicySkip, Confirmed: true,
		})
	}
	plan, err := Preview(context.Background(), snapshot, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanApply {
		t.Fatal("150 blocking references must keep the plan inapplicable")
	}
	if len(plan.Diagnostics) != MaxDiagnostics {
		t.Fatalf("visible diagnostics = %d, want capped at %d", len(plan.Diagnostics), MaxDiagnostics)
	}
	last := plan.Diagnostics[len(plan.Diagnostics)-1]
	if last.Code != "source_import.diagnostics_truncated" {
		t.Fatalf("truncation notice missing: %#v", last)
	}
	if !strings.Contains(last.Message, "150") || !strings.Contains(last.Message, "99") ||
		!strings.Contains(last.Message, "阻断") {
		t.Fatalf("truncation notice lacks counts or blocking facts: %s", last.Message)
	}
	for _, diagnostic := range plan.Diagnostics[:99] {
		if diagnostic.Code != "source_import.decision_unknown" || !diagnostic.Blocking {
			t.Fatalf("kept slot lost a blocking fact: %#v", diagnostic)
		}
	}
}

func TestPreviewIgnoresSourceOrdering(t *testing.T) {
	first, err := Preview(context.Background(), relationSnapshot(), allSelectedOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	permuted := relationSnapshot()
	permuted.Tables = []Table{permuted.Tables[2], permuted.Tables[1], permuted.Tables[0]}
	for ti := range permuted.Tables {
		table := &permuted.Tables[ti]
		reversed := make([]Field, 0, len(table.Fields))
		for fi := len(table.Fields) - 1; fi >= 0; fi-- {
			reversed = append(reversed, table.Fields[fi])
		}
		table.Fields = reversed
		records := make([]Record, len(table.Records))
		for ri := range table.Records {
			records[len(table.Records)-1-ri] = table.Records[ri]
		}
		table.Records = records
	}
	second, err := Preview(context.Background(), permuted, allSelectedOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("source ordering changed the plan:\n%#v\n%#v", first, second)
	}
}

func TestPreviewReadWindowAndObservationInvalidation(t *testing.T) {
	windowed := relationSnapshot()
	windowed.ReadWindow.Consistency = "window"
	plan, err := Preview(context.Background(), windowed, allSelectedOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	noticed := false
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Code == "source_import.read_window" && !diagnostic.Blocking {
			noticed = true
		}
	}
	if !noticed {
		t.Fatalf("window reads must disclose their consistency limits: %#v", plan.Diagnostics)
	}
	invalid := relationSnapshot()
	invalid.ReadWindow.Consistency = "paged"
	if _, err := Preview(context.Background(), invalid, allSelectedOptions(), nil); err == nil {
		t.Fatal("invalid read consistency was accepted")
	}
	plan, err = Preview(context.Background(), relationSnapshot(), allSelectedOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	observation := Observation{Version: plan.Version, TableVersions: map[string]string{
		"src_projects": "tv1", "src_people": "tv1", "src_tasks": "tv1",
	}}
	if err := plan.CheckObservation(observation); err != nil {
		t.Fatalf("matching observation rejected: %v", err)
	}
	stale := observation
	stale.Version = "v2026.11.01"
	var productErr *Error
	if err := plan.CheckObservation(stale); !errors.As(err, &productErr) ||
		productErr.Code != "source_import.source_changed" {
		t.Fatalf("stale source version accepted: %v", err)
	}
	missingTable := observation
	delete(missingTable.TableVersions, "src_people")
	if err := plan.CheckObservation(missingTable); !errors.As(err, &productErr) ||
		productErr.Code != "source_import.source_changed" {
		t.Fatalf("missing table version accepted: %v", err)
	}
}

func TestPreviewCancellationReturnsAnError(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Preview(cancelled, relationSnapshot(), allSelectedOptions(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Preview() error = %v", err)
	}
	// Cancellation must also surface from deep value validation instead of
	// silently producing an incomplete plan.
	draft, draftErr := planField(
		Field{ID: "f_title", Name: "Title", Kind: "text", ValueKind: v2.LogicalText},
		Decision{TableID: "src_projects", FieldID: "f_title", Policy: PolicyNative},
	)
	if draftErr != nil {
		t.Fatal(draftErr)
	}
	plan := Plan{CanApply: true, Tables: []TablePlan{{
		SourceID: "src_projects",
		Fields:   []FieldPlan{draft},
		Records:  []Record{{ID: "r1", Values: map[string]any{"f_title": "P1"}}},
	}}}
	if err := validatePlanValues(cancelled, &plan, map[Key]bool{}, func(string, string, string, string, string, bool) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled validatePlanValues() error = %v", err)
	}
}

func TestPreviewOneSidedReciprocalNamingNeedsExplicitRenames(t *testing.T) {
	// Both one-sided relations into src_people default their reciprocals to
	// the same fixed name; nothing is auto-renamed or guessed from labels.
	conflicting := allSelectedOptions()
	conflicting.Decisions = nil
	plan, err := Preview(context.Background(), relationSnapshot(), conflicting, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanApply || !hasBlocking(plan, "source_import.field_name_conflict") {
		t.Fatalf("duplicate reciprocal names must block: %#v", plan.Diagnostics)
	}
	found := false
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Code == "source_import.field_name_conflict" &&
			strings.Contains(diagnostic.Message, reciprocalDefaultName) {
			found = true
		}
	}
	if !found {
		t.Fatalf("conflict diagnostic must name the colliding column: %#v", plan.Diagnostics)
	}
	// Renaming one reciprocal through an explicit decision resolves it.
	resolved := allSelectedOptions()
	resolved.Decisions = []Decision{{
		TableID: "src_projects", FieldID: "f_peer", Policy: PolicyNative,
		TargetName: "人员关联项目",
	}}
	plan, err = Preview(context.Background(), relationSnapshot(), resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply || blockingCount(plan) != 0 {
		t.Fatalf("explicit reciprocal rename must resolve the conflict: %#v", plan.Diagnostics)
	}
}

func TestPreviewExistingColumnNamedLikeReciprocalConflicts(t *testing.T) {
	snapshot := relationSnapshot()
	snapshot.Tables[1].Fields = append(snapshot.Tables[1].Fields,
		Field{ID: "f_label", Name: reciprocalDefaultName, Kind: "text", ValueKind: v2.LogicalText})
	plan, err := Preview(context.Background(), snapshot, allSelectedOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanApply || !hasBlocking(plan, "source_import.field_name_conflict") {
		t.Fatalf("existing column named like the reciprocal must conflict: %#v", plan.Diagnostics)
	}
	options := allSelectedOptions()
	options.Decisions = append(options.Decisions, Decision{
		TableID: "src_tasks", FieldID: "f_assignees", Policy: PolicyNative,
		TargetName: "任务负责人",
	})
	plan, err = Preview(context.Background(), snapshot, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply || blockingCount(plan) != 0 {
		t.Fatalf("renaming the reciprocal must resolve the conflict: %#v", plan.Diagnostics)
	}
}

func TestPreviewSameNameColumnsRequireExplicitRename(t *testing.T) {
	snapshot := renameSnapshot()
	snapshot.Tables[0].Fields = append(snapshot.Tables[0].Fields,
		Field{ID: "f_amount2", Name: "Field f_name", Kind: "number", ValueKind: v2.LogicalNumber})
	plan, err := Preview(context.Background(), snapshot, renameOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanApply || !hasBlocking(plan, "source_import.field_name_conflict") {
		t.Fatalf("duplicate column names must require explicit rename: %#v", plan.Diagnostics)
	}
	options := renameOptions()
	options.Decisions = append(options.Decisions, Decision{
		TableID: "src_a", FieldID: "f_amount2", Policy: PolicyNative, TargetName: "金额副本",
	})
	plan, err = Preview(context.Background(), snapshot, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply || blockingCount(plan) != 0 {
		t.Fatalf("explicit column rename must resolve the conflict: %#v", plan.Diagnostics)
	}
	renamed := planFieldOf(t, plan, "src_a", "f_amount2")
	if renamed.Draft.DisplayName != "金额副本" {
		t.Fatalf("TargetName did not rename the planned column: %q", renamed.Draft.DisplayName)
	}
}

func TestPreviewAllowsFilePrimaryDisplay(t *testing.T) {
	snapshot := renameSnapshot()
	snapshot.Tables[0].Fields[0] = Field{
		ID: "f_name", Name: "Field f_name", Kind: string(v2.LogicalFile), ValueKind: v2.LogicalFile,
	}
	snapshot.Tables[0].Records = []Record{{ID: "r1", Values: map[string]any{"f_name": nil}}}
	// src_b keeps its one-sided relation into src_a; its own primary stays text.
	plan, err := Preview(context.Background(), snapshot, renameOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply || hasBlocking(plan, "source_import.primary_field") {
		t.Fatalf("file primaries are display-eligible locally and must not block: %#v", plan.Diagnostics)
	}
}

func TestPreviewProvenanceSummaryBudget(t *testing.T) {
	snapshot := renameSnapshot()
	for ti := range snapshot.Tables {
		table := &snapshot.Tables[ti]
		for fi := range table.Fields {
			if table.Fields[fi].ID == "f_name" && table.ID == "src_a" {
				table.Fields[fi].Definition = strings.Repeat("SUM({value})+", 1) + strings.Repeat("x", 600*1024)
			}
		}
	}
	plan, err := Preview(context.Background(), snapshot, renameOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanApply || !hasBlocking(plan, "source_import.provenance_capacity") {
		t.Fatalf("oversized provenance must block: %#v", plan.Diagnostics)
	}
	big := planFieldOf(t, plan, "src_a", "f_name")
	if len(big.Source.Definition) != 600*1024+len("SUM({value})+") {
		t.Fatalf("expression was silently truncated: %d bytes", len(big.Source.Definition))
	}
}
