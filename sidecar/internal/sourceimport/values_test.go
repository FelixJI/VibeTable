package sourceimport

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func valueSnapshot() Snapshot {
	return Snapshot{
		Contract: Contract, Provider: "prov-cloud", ContainerID: "space-1",
		DisplayName: "云端空间", Version: "v2026.10.01",
		ReadWindow: ReadWindow{StartedAt: "2026-10-01T08:00:00Z", FinishedAt: "2026-10-01T08:05:00Z", Consistency: "snapshot"},
		Tables: []Table{{
			ID: "src_orders", Name: "Orders", Version: "tv1", PrimaryFieldID: "f_title",
			Fields: []Field{
				{ID: "f_title", Name: "Title", Kind: "text", ValueKind: v2.LogicalText},
				{ID: "f_count", Name: "Count", Kind: "number", ValueKind: v2.LogicalNumber},
				{ID: "f_shipped", Name: "Shipped", Kind: "bool", ValueKind: v2.LogicalBool},
				{ID: "f_note", Name: "Note", Kind: "text", ValueKind: v2.LogicalText},
				{ID: "f_code", Name: "Code", Kind: "text", ValueKind: v2.LogicalText},
				{ID: "f_blank", Name: "Blank", Kind: "text", ValueKind: v2.LogicalText},
				{ID: "f_prec", Name: "Precise", Kind: "number", ValueKind: v2.LogicalNumber},
				{ID: "f_stage", Name: "Stage", Kind: "select", ValueKind: v2.LogicalSelect,
					Options: []Option{
						{ID: "opt_open", Label: "Open"},
						{ID: "opt_done", Label: "Done"},
					}},
				{ID: "f_tags", Name: "Tags", Kind: "multiSelect", ValueKind: v2.LogicalMultiSelect,
					Options: []Option{
						{ID: "t_red", Label: "Red"},
						{ID: "t_blue", Label: "Blue"},
					}},
				{ID: "f_calc", Name: "Calc", Kind: "formula", ValueKind: v2.LogicalFormula,
					Definition: "SUM({f_count})"},
				{ID: "f_person", Name: "Owner", Kind: "person", ValueKind: v2.LogicalText},
				{ID: "f_mystery", Name: "Mystery", Kind: "unknown", ValueKind: v2.LogicalJSON},
			},
			Records: []Record{{ID: "r1", Values: map[string]any{
				"f_title":   "O1",
				"f_count":   json.Number("0"),
				"f_shipped": false,
				"f_note":    nil,
				"f_code":    "007",
				"f_blank":   "",
				"f_prec":    json.Number("0.12345678901234567890"),
				"f_stage":   "opt_done",
				"f_tags":    []any{"t_red", "t_blue"},
			}}},
		}},
	}
}

func valueOptions() Options {
	return Options{SelectedTableIDs: []string{"src_orders"}}
}

func fieldBlocked(plan Plan, fieldID, code string) bool {
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.FieldID == fieldID && diagnostic.Code == code && diagnostic.Blocking {
			return true
		}
	}
	return false
}

func TestPreviewValueSemanticsKeepZeroFalseNullEmptyAndLeadingZeros(t *testing.T) {
	plan, err := Preview(context.Background(), valueSnapshot(), valueOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Untouched plain values must never become diagnostics.
	for _, fieldID := range []string{"f_count", "f_shipped", "f_note", "f_code", "f_blank", "f_stage", "f_tags"} {
		if fieldBlocked(plan, fieldID, "source_import.value_invalid") {
			t.Fatalf("plain value %s was rejected: %#v", fieldID, plan.Diagnostics)
		}
	}
	// The plan keeps the frozen source values verbatim; no display coercion.
	values := plan.Tables[0].Records[0].Values
	if values["f_code"] != "007" || values["f_blank"] != "" || values["f_note"] != nil ||
		values["f_shipped"] != false || values["f_count"] != json.Number("0") {
		t.Fatalf("source values were coerced: %#v", values)
	}
	// Precision loss blocks without a confirmed exact snapshot, and the three
	// unverified source kinds block without confirmed snapshot/skip decisions.
	if !fieldBlocked(plan, "f_prec", "source_import.value_invalid") {
		t.Fatalf("precision loss must block or require a confirmed snapshot: %#v", plan.Diagnostics)
	}
	for _, fieldID := range []string{"f_calc", "f_person", "f_mystery"} {
		if !fieldBlocked(plan, fieldID, "source_import.field_strategy") {
			t.Fatalf("unconfirmed %s must block native migration: %#v", fieldID, plan.Diagnostics)
		}
	}
	if plan.CanApply {
		t.Fatal("unresolved value issues must keep the plan inapplicable")
	}
}

func TestPreviewConfirmedDecisionsSnapshotSkipAndRetainDefinitions(t *testing.T) {
	options := valueOptions()
	options.Decisions = []Decision{
		{TableID: "src_orders", FieldID: "f_prec", Policy: PolicySnapshot, TargetKind: v2.LogicalText, Confirmed: true},
		{TableID: "src_orders", FieldID: "f_calc", Policy: PolicySnapshot, TargetKind: v2.LogicalText, Confirmed: true},
		{TableID: "src_orders", FieldID: "f_person", Policy: PolicySkip, Confirmed: true},
		{TableID: "src_orders", FieldID: "f_mystery", Policy: PolicySnapshot, TargetKind: v2.LogicalText, Confirmed: true},
	}
	plan, err := Preview(context.Background(), valueSnapshot(), options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply || blockingCount(plan) != 0 {
		t.Fatalf("confirmed decisions must resolve every blocker: %#v", plan.Diagnostics)
	}
	calc := planFieldOf(t, plan, "src_orders", "f_calc")
	if calc.Policy != PolicySnapshot || calc.Source.Definition != "SUM({f_count})" {
		t.Fatalf("snapshot definition was not retained: %#v", calc)
	}
	if person := planFieldOf(t, plan, "src_orders", "f_person"); person.Policy != PolicySkip {
		t.Fatalf("person field was not skipped: %#v", person)
	}
	// Unconfirmed decisions never downgrade silently.
	for name, decision := range map[string]Decision{
		"snapshot": {TableID: "src_orders", FieldID: "f_calc", Policy: PolicySnapshot, TargetKind: v2.LogicalText},
		"skip":     {TableID: "src_orders", FieldID: "f_person", Policy: PolicySkip},
	} {
		options := valueOptions()
		options.Decisions = []Decision{decision}
		plan, err := Preview(context.Background(), valueSnapshot(), options, nil)
		if err != nil {
			t.Fatal(err)
		}
		if plan.CanApply || !fieldBlocked(plan, decision.FieldID, "source_import.field_strategy") {
			t.Fatalf("unconfirmed %s decision must block: %#v", name, plan.Diagnostics)
		}
	}
}

func TestPreviewSelectAndMultiSelectMapIdentitiesNotLabels(t *testing.T) {
	labelValue := valueSnapshot()
	labelValue.Tables[0].Records[0].Values["f_stage"] = "Done"
	if plan, err := Preview(context.Background(), labelValue, valueOptions(), nil); err != nil ||
		!fieldBlocked(plan, "f_stage", "source_import.value_invalid") {
		t.Fatalf("labels must never act as option identities: %v %#v", err, plan.Diagnostics)
	}
	unknownOption := valueSnapshot()
	unknownOption.Tables[0].Records[0].Values["f_tags"] = []any{"t_red", "Open"}
	if plan, err := Preview(context.Background(), unknownOption, valueOptions(), nil); err != nil ||
		!fieldBlocked(plan, "f_tags", "source_import.value_invalid") {
		t.Fatalf("unknown multi-select identities must block: %v %#v", err, plan.Diagnostics)
	}
	// A snapshot onto select needs the explicit source option mapping.
	noOptions := valueSnapshot()
	noOptions.Tables[0].Fields = append(noOptions.Tables[0].Fields,
		Field{ID: "f_stage2", Name: "Stage 2", Kind: "select", ValueKind: v2.LogicalSelect})
	options := valueOptions()
	options.Decisions = []Decision{{TableID: "src_orders", FieldID: "f_stage2",
		Policy: PolicySnapshot, TargetKind: v2.LogicalSelect, Confirmed: true}}
	if plan, err := Preview(context.Background(), noOptions, options, nil); err != nil ||
		!fieldBlocked(plan, "f_stage2", "source_import.field_strategy") {
		t.Fatalf("snapshot onto select without declared options must block: %v %#v", err, plan.Diagnostics)
	}
	// A declared mapping resolves snapshot select values by source option ID.
	mapped := valueOptions()
	mapped.Decisions = []Decision{{TableID: "src_orders", FieldID: "f_stage",
		Policy: PolicySnapshot, TargetKind: v2.LogicalSelect, Confirmed: true}}
	if plan, err := Preview(context.Background(), valueSnapshot(), mapped, nil); err != nil ||
		fieldBlocked(plan, "f_stage", "source_import.value_invalid") {
		t.Fatalf("declared option mapping must resolve snapshot select: %v %#v", err, plan.Diagnostics)
	}
}

func fileSnapshot() Snapshot {
	return Snapshot{
		Contract: Contract, Provider: "prov-cloud", ContainerID: "space-1",
		DisplayName: "云端空间", Version: "v2026.10.01",
		ReadWindow: ReadWindow{StartedAt: "2026-10-01T08:00:00Z", FinishedAt: "2026-10-01T08:05:00Z", Consistency: "snapshot"},
		Tables: []Table{{
			ID: "src_files", Name: "Files", Version: "tv1", PrimaryFieldID: "f_name",
			Fields: []Field{
				{ID: "f_name", Name: "Name", Kind: "text", ValueKind: v2.LogicalText},
				{ID: "f_doc", Name: "Docs", Kind: string(v2.LogicalFile), ValueKind: v2.LogicalFile},
			},
			Records: []Record{{ID: "r1", Values: map[string]any{
				"f_name": "F1",
				"f_doc":  []any{"att1", "att2"},
			}}},
		}},
		Attachments: []Attachment{
			{ID: "att1", TableID: "src_files", RecordID: "r1", FieldID: "f_doc",
				Name: "report.pdf", MIME: "application/pdf", Size: 6 << 20},
			{ID: "att2", TableID: "src_files", RecordID: "r1", FieldID: "f_doc",
				Name: "photo.png", MIME: "image/png; charset=binary", Size: 7 << 20},
		},
	}
}

func fileOptions() Options {
	return Options{SelectedTableIDs: []string{"src_files"}}
}

func TestPreviewAttachmentMetadataOnlyNeverClaimsExecution(t *testing.T) {
	plan, err := Preview(context.Background(), fileSnapshot(), fileOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply || blockingCount(plan) != 0 {
		t.Fatalf("valid attachment metadata blocked: %#v", plan.Diagnostics)
	}
	if len(plan.Attachments) != 2 {
		t.Fatalf("selected attachments = %#v", plan.Attachments)
	}
	pending := false
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Code == "source_import.attachment_pending" {
			pending = !diagnostic.Blocking && strings.Contains(diagnostic.Message, "成功上传")
		}
	}
	if !pending {
		t.Fatalf("metadata-only preview must disclose the byte transfer gate: %#v", plan.Diagnostics)
	}
	// Attachment-derived limits are frozen into the reviewed draft before any
	// definition or value validation widens them after the fact.
	documents := planFieldOf(t, plan, "src_files", "f_doc")
	if documents.Draft.File == nil || documents.Draft.File.MaxFiles != 2 ||
		documents.Draft.File.MaxBytesPerFile != 7<<20 {
		t.Fatalf("file limits were not derived before validation: %#v", documents.Draft.File)
	}
}

func TestPreviewAttachmentUnsafeMetadataBlocks(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Snapshot)
		code     string
		unsafeID string
	}{
		{name: "dot name", mutate: func(s *Snapshot) { s.Attachments[0].Name = "." }, code: "source_import.attachment_metadata", unsafeID: "att1"},
		{name: "dotdot name", mutate: func(s *Snapshot) { s.Attachments[0].Name = ".." }, code: "source_import.attachment_metadata", unsafeID: "att1"},
		{name: "path separator", mutate: func(s *Snapshot) { s.Attachments[0].Name = "dir/report.pdf" }, code: "source_import.attachment_metadata", unsafeID: "att1"},
		{name: "windows separator", mutate: func(s *Snapshot) { s.Attachments[0].Name = `dir\report.pdf` }, code: "source_import.attachment_metadata", unsafeID: "att1"},
		{name: "control character", mutate: func(s *Snapshot) { s.Attachments[0].Name = "re\nport.pdf" }, code: "source_import.attachment_metadata", unsafeID: "att1"},
		{name: "blank name", mutate: func(s *Snapshot) { s.Attachments[0].Name = " " }, code: "source_import.attachment_metadata", unsafeID: "att1"},
		{name: "missing mime", mutate: func(s *Snapshot) { s.Attachments[0].MIME = "" }, code: "source_import.attachment_metadata", unsafeID: "att1"},
		{name: "malformed mime", mutate: func(s *Snapshot) { s.Attachments[0].MIME = "not-a-mime" }, code: "source_import.attachment_metadata", unsafeID: "att1"},
		{name: "wildcard mime", mutate: func(s *Snapshot) { s.Attachments[0].MIME = "image/*" }, code: "source_import.attachment_metadata", unsafeID: "att1"},
		{name: "zero bytes", mutate: func(s *Snapshot) { s.Attachments[0].Size = 0 }, code: "source_import.attachment_metadata", unsafeID: "att1"},
		{name: "oversized single file", mutate: func(s *Snapshot) { s.Attachments[0].Size = 33 << 20 }, code: "source_import.attachment_metadata", unsafeID: "att1"},
		{name: "missing metadata", mutate: func(s *Snapshot) { s.Attachments = s.Attachments[:1] }, code: "source_import.attachment_metadata", unsafeID: "att2"},
		{name: "duplicate identity", mutate: func(s *Snapshot) {
			s.Attachments = append(s.Attachments, s.Attachments[1])
		}, code: "source_import.attachment_identity", unsafeID: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := fileSnapshot()
			test.mutate(&snapshot)
			plan, err := Preview(context.Background(), snapshot, fileOptions(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if plan.CanApply || !hasBlocking(plan, test.code) {
				t.Fatalf("expected blocking %s, got %#v", test.code, plan.Diagnostics)
			}
			for _, attachment := range plan.Attachments {
				if attachment.ID == test.unsafeID {
					t.Fatalf("unsafe attachment entered the plan: %#v", plan.Attachments)
				}
			}
		})
	}
}

func spreadAttachmentSnapshot(t *testing.T, files int, size int64) Snapshot {
	t.Helper()
	snapshot := fileSnapshot()
	snapshot.Attachments = nil
	table := &snapshot.Tables[0]
	table.Records = nil
	for record := 0; record < (files+99)/100; record++ {
		values := map[string]any{"f_name": fmt.Sprintf("F%d", record+1), "f_doc": []any{}}
		ids := values["f_doc"].([]any)
		for index := 0; index < 100 && record*100+index < files; index++ {
			id := fmt.Sprintf("att%04d", record*100+index)
			snapshot.Attachments = append(snapshot.Attachments, Attachment{
				ID: id, TableID: "src_files", RecordID: fmt.Sprintf("r%d", record+1),
				FieldID: "f_doc", Name: id + ".bin", MIME: "application/octet-stream", Size: size,
			})
			ids = append(ids, id)
		}
		values["f_doc"] = ids
		table.Records = append(table.Records, Record{ID: fmt.Sprintf("r%d", record+1), Values: values})
	}
	return snapshot
}

func TestPreviewAttachmentCapacityNeverExceedsExistingStaging(t *testing.T) {
	plan, err := Preview(context.Background(), spreadAttachmentSnapshot(t, 500, 1), fileOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply || hasBlocking(plan, "source_import.attachment_capacity") {
		t.Fatalf("500 staged files within budget must pass: %#v", plan.Diagnostics)
	}
	overCount, err := Preview(context.Background(), spreadAttachmentSnapshot(t, 501, 1), fileOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if overCount.CanApply || !hasBlocking(overCount, "source_import.attachment_capacity") {
		t.Fatalf("501 files must exceed the narrowed budget: %#v", overCount.Diagnostics)
	}
	for _, diagnostic := range overCount.Diagnostics {
		if diagnostic.Code == "source_import.attachment_capacity" && strings.Contains(diagnostic.Message, "缩小") {
			if !strings.Contains(diagnostic.Message, fmt.Sprint(maxSelectedAttachmentFiles)) {
				t.Fatalf("capacity diagnostic must state the real bound: %s", diagnostic.Message)
			}
		}
	}
	// Five 30MiB files are individually staged yet jointly over budget.
	oversize := spreadAttachmentSnapshot(t, 5, 30<<20)
	overSize, err := Preview(context.Background(), oversize, fileOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if overSize.CanApply || !hasBlocking(overSize, "source_import.attachment_capacity") {
		t.Fatalf("150MiB must exceed the 128MiB budget: %#v", overSize.Diagnostics)
	}
}

func TestCanonicalValuePreservesExactnessAndMapsOptionIdentities(t *testing.T) {
	if value, err := CanonicalValue(FieldPlan{Policy: PolicyNative},
		v2.FieldDefinition{LogicalType: v2.LogicalNumber}, nil); value != nil || err != nil {
		t.Fatalf("nil canonical = %#v, %v", value, err)
	}
	number := v2.FieldDefinition{LogicalType: v2.LogicalNumber}
	if value, err := CanonicalValue(FieldPlan{Policy: PolicyNative}, number, json.Number("0.1")); err != nil ||
		value != json.Number("0.1") {
		t.Fatalf("exact number = %#v, %v", value, err)
	}
	if _, err := CanonicalValue(FieldPlan{Policy: PolicyNative}, number,
		json.Number("0.10000000000000000001")); err == nil ||
		!strings.Contains(err.Error(), "精度") {
		t.Fatalf("precision loss must block: %v", err)
	}
	textSnapshot := FieldPlan{Policy: PolicySnapshot}
	textDefinition := v2.FieldDefinition{LogicalType: v2.LogicalText}
	if value, err := CanonicalValue(textSnapshot, textDefinition,
		json.Number("0.12345678901234567890")); err != nil ||
		value != "0.12345678901234567890" {
		t.Fatalf("text snapshot lost precision: %#v, %v", value, err)
	}
	if value, err := CanonicalValue(textSnapshot, textDefinition, true); err != nil || value != "true" {
		t.Fatalf("bool snapshot text = %#v, %v", value, err)
	}
	source := Field{Options: []Option{{ID: "src_a"}, {ID: "src_b"}}}
	definition := v2.FieldDefinition{LogicalType: v2.LogicalSelect, Select: &v2.SelectSpec{
		Options: []v2.SelectOption{{OptionID: "opt_local_a"}, {OptionID: "opt_local_b"}},
	}}
	snapshotSelect := FieldPlan{Policy: PolicySnapshot, Source: source}
	if value, err := CanonicalValue(snapshotSelect, definition, "src_a"); err != nil ||
		value != "opt_local_a" {
		t.Fatalf("snapshot select mapping = %#v, %v", value, err)
	}
	if _, err := CanonicalValue(snapshotSelect, definition, "Open label"); err == nil {
		t.Fatal("labels must never map to option identities")
	}
	mismatched := definition
	mismatched.Select = &v2.SelectSpec{Options: []v2.SelectOption{{OptionID: "opt_local_a"}}}
	if _, err := CanonicalValue(snapshotSelect, mismatched, "src_a"); err == nil {
		t.Fatal("incomplete option mapping must block")
	}
	multi := v2.FieldDefinition{LogicalType: v2.LogicalMultiSelect, Select: definition.Select}
	if value, err := CanonicalValue(snapshotSelect, multi, []any{"src_b", "src_a"}); err != nil ||
		fmt.Sprint(value) != "[opt_local_b opt_local_a]" {
		t.Fatalf("snapshot multi mapping = %#v, %v", value, err)
	}
}
