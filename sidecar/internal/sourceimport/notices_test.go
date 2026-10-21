package sourceimport

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func noticesSnapshot(notices []string) Snapshot {
	return Snapshot{
		Contract: Contract, Provider: "prov-local", ContainerID: "file-1",
		DisplayName: "本地 base 文件", Version: "v1",
		ReadWindow: ReadWindow{StartedAt: "2026-10-04T08:00:00Z", FinishedAt: "2026-10-04T08:01:00Z", Consistency: "snapshot"},
		Notices:    notices,
		Tables: []Table{{
			ID: "src_a", Name: "订单", Version: "tv1", PrimaryFieldID: "f_title",
			Fields:  []Field{{ID: "f_title", Name: "标题", Kind: "text", ValueKind: v2.LogicalText}},
			Records: []Record{{ID: "r1", Values: map[string]any{"f_title": "A"}}},
		}},
	}
}

func noticesOptions() Options {
	return Options{SelectedTableIDs: []string{"src_a"}}
}

// Source notices are disclosures about the whole file, not per-selection
// facts: the wizard acknowledges the complete set, so the reviewed plan keeps
// every entry verbatim even when only a subset of tables is selected.
func TestPreviewFreezesSourceNoticesVerbatimRegardlessOfSelection(t *testing.T) {
	notices := []string{
		"文件包含 2 个仪表盘：仪表盘不会随导入迁移",
		"表「订单」的字段「计算」是公式：计算值未随文件导出，迁移后该列将为空",
	}
	plan, err := Preview(context.Background(), noticesSnapshot(notices), noticesOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply {
		t.Fatalf("notices must never block a valid plan: %#v", plan.Diagnostics)
	}
	if !reflect.DeepEqual(plan.Notices, notices) {
		t.Fatalf("plan notices were altered: %#v", plan.Notices)
	}
	for _, diagnostic := range plan.Diagnostics {
		if strings.Contains(diagnostic.Message, "仪表盘") || strings.Contains(diagnostic.Message, "公式") {
			t.Fatalf("notices must stay out of diagnostics: %#v", plan.Diagnostics)
		}
	}
}

func TestPreviewAcceptsBoundaryLengthSourceNotice(t *testing.T) {
	boundary := []string{strings.Repeat("告", MaxSourceNoticeLength)}
	plan, err := Preview(context.Background(), noticesSnapshot(boundary), noticesOptions(), nil)
	if err != nil || len(plan.Notices) != 1 {
		t.Fatalf("max-length notice must be accepted: %v %#v", err, plan.Notices)
	}
}

func TestPreviewRejectsMalformedSourceNotices(t *testing.T) {
	cases := map[string][]string{
		"empty":          {"有效告知", ""},
		"padded":         {" 前后空白"},
		"multiline":      {"第一行\n第二行"},
		"control char":   {"包含\x00字符"},
		"overlong entry": {strings.Repeat("告", MaxSourceNoticeLength+1)},
	}
	for name, notices := range cases {
		t.Run(name, func(t *testing.T) {
			plan, err := Preview(context.Background(), noticesSnapshot(notices), noticesOptions(), nil)
			if err == nil {
				t.Fatalf("malformed notice %q was accepted: %#v", name, plan.Notices)
			}
			sourceError, ok := err.(*Error)
			if !ok || sourceError.Code != "source_import.notices_invalid" {
				t.Fatalf("expected notices_invalid, got %v", err)
			}
		})
	}
}

// Count boundary semantics: 255 and 256 entries persist verbatim; only a
// strictly larger list truncates, keeping 255 entries plus one explicit
// marker (the marker reserves the 256th slot, so nothing is dropped
// silently at the exact boundary).
func TestPreviewSourceNoticesCountBoundaries(t *testing.T) {
	makeNotices := func(count int) []string {
		entries := make([]string, count)
		for index := range entries {
			entries[index] = "告知 " + strings.Repeat("a", index%17) + " 号"
		}
		return entries
	}
	for _, count := range []int{255, 256} {
		plan, err := Preview(context.Background(), noticesSnapshot(makeNotices(count)), noticesOptions(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Notices) != count {
			t.Fatalf("%d entries were altered: kept %d", count, len(plan.Notices))
		}
	}
	total := 257
	plan, err := Preview(context.Background(), noticesSnapshot(makeNotices(total)), noticesOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Notices) != MaxSourceNotices {
		t.Fatalf("truncated notices = %d, want %d", len(plan.Notices), MaxSourceNotices)
	}
	kept := plan.Notices[:MaxSourceNotices-1]
	if !reflect.DeepEqual(kept, makeNotices(total)[:MaxSourceNotices-1]) {
		t.Fatal("kept prefix was altered")
	}
	marker := plan.Notices[len(plan.Notices)-1]
	if !strings.Contains(marker, "共 257 条") || !strings.Contains(marker, "仅保留前 255 条") ||
		!strings.Contains(marker, "未随本回执持久化") {
		t.Fatalf("truncation marker lost the explicit disclosure: %q", marker)
	}
	if len(plan.Diagnostics) != 0 {
		t.Fatalf("notice truncation must not emit diagnostics: %#v", plan.Diagnostics)
	}
}

// The serialized notices share the result_json column with provenance; the
// byte budget truncates with the same explicit marker before the envelope
// can approach the column limit.
func TestPreviewTruncatesSourceNoticesOnByteBudget(t *testing.T) {
	heavy := strings.Repeat("告", MaxSourceNoticeLength) // 512 runes ≈ 1538 JSON bytes
	total := MaxSourceNoticeBytes/1538 + 10
	entries := make([]string, total)
	for index := range entries {
		entries[index] = heavy
	}
	plan, err := Preview(context.Background(), noticesSnapshot(entries), noticesOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Notices) >= total {
		t.Fatalf("byte budget did not truncate: kept %d of %d", len(plan.Notices), total)
	}
	marker := plan.Notices[len(plan.Notices)-1]
	if !strings.Contains(marker, "KiB 持久化预算") || !strings.Contains(marker, "已在导入窗口确认") {
		t.Fatalf("byte-budget marker lost the explicit disclosure: %q", marker)
	}
	keptBytes := 0
	for _, notice := range plan.Notices[:len(plan.Notices)-1] {
		keptBytes += len(notice)
	}
	if keptBytes > MaxSourceNoticeBytes {
		t.Fatalf("kept notices exceed the byte budget: %d", keptBytes)
	}
}

// The budget counts JSON-encoded bytes: <>& escaping expands a 512-byte
// notice to ~3 KiB on the wire, and once freezing stops the kept list is a
// strict prefix — a later short entry never sneaks in after a rejected long
// one, matching the marker's "仅保留前 N 条" claim.
func TestPreviewSourceNoticesBudgetUsesEncodedBytesAndKeepsPrefix(t *testing.T) {
	escaped := strings.Repeat("<&>&", 128) // 512 runes, 512 raw bytes, ~3074 encoded bytes
	total := 64
	entries := make([]string, total)
	for index := range entries {
		entries[index] = escaped
	}
	// Raw-byte accounting (64 × 512 = 32 KiB) would keep everything; the
	// encoded budget (~64 × 3074 ≈ 192 KiB) must truncate well before the end.
	plan, err := Preview(context.Background(), noticesSnapshot(entries), noticesOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Notices) >= total {
		t.Fatalf("encoded budget did not truncate: kept %d of %d", len(plan.Notices), total)
	}
	kept := len(plan.Notices) - 1
	if !reflect.DeepEqual(plan.Notices[:kept], entries[:kept]) {
		t.Fatal("kept notices are not a prefix of the source disclosures")
	}
	marker := plan.Notices[len(plan.Notices)-1]
	if !strings.Contains(marker, "仅保留前 ") {
		t.Fatalf("marker lost the kept-count disclosure: %q", marker)
	}

	// Mixed shapes: freezing stops at the first over-budget entry and short
	// entries after it stay out, while their shape is still validated.
	// 42 escaped entries ≈ 129 KiB fit; the 43rd would cross the 128 KiB cap.
	const prefixCount = MaxSourceNoticeBytes / 3074
	mixed := make([]string, 0, prefixCount+2)
	for i := 0; i < prefixCount; i++ {
		mixed = append(mixed, escaped)
	}
	mixed = append(mixed, strings.Repeat("&", MaxSourceNoticeLength))
	mixed = append(mixed, "短告知")
	plan, err = Preview(context.Background(), noticesSnapshot(mixed), noticesOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	kept = len(plan.Notices) - 1
	if kept != prefixCount || !reflect.DeepEqual(plan.Notices[:kept], mixed[:kept]) {
		t.Fatalf("prefix semantics broken: kept %d of %d", kept, len(mixed))
	}
	// Shape violations after the stop still fail closed.
	invalid := append([]string{}, mixed...)
	invalid = append(invalid, "尾部畸形 ")
	if _, err := Preview(context.Background(), noticesSnapshot(invalid), noticesOptions(), nil); err == nil {
		t.Fatal("malformed notice after the truncation point was accepted")
	}
}

// Disclosure truncation must happen before the whole-source capacity check.
func TestPreviewNoticesDoNotConsumeSnapshotCapacityBeforeFreezing(t *testing.T) {
	heavy := strings.Repeat("告", MaxSourceNoticeLength) // 512 runes ≈ 1538 JSON bytes
	entries := make([]string, MaxSnapshotBytes/1538+2048)
	for index := range entries {
		entries[index] = heavy
	}
	source := noticesSnapshot(entries)
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= MaxSnapshotBytes {
		t.Fatalf("fixture must exceed the source capacity before freezing: %d bytes", len(raw))
	}
	plan, err := Preview(context.Background(), source, noticesOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply {
		t.Fatalf("notice-heavy source must stay valid: %#v", plan.Diagnostics)
	}
	if len(plan.Notices) >= len(entries) {
		t.Fatalf("notices were not truncated: kept %d of %d", len(plan.Notices), len(entries))
	}
	if !strings.Contains(plan.Notices[len(plan.Notices)-1], "仅保留前 ") {
		t.Fatalf("truncation marker missing: %#v", plan.Notices)
	}
}

// Freezing notices must not exempt oversized business data from the capacity limit.
func TestPreviewStillRejectsOversizedBusinessAfterNoticeFreeze(t *testing.T) {
	source := noticesSnapshot([]string{strings.Repeat("告", MaxSourceNoticeLength)})
	source.Tables[0].Records = []Record{{ID: "r1", Values: map[string]any{
		"f_title": strings.Repeat("x", MaxSnapshotBytes),
	}}}
	_, err := Preview(context.Background(), source, noticesOptions(), nil)
	sourceError, ok := err.(*Error)
	if !ok || sourceError.Code != "source_import.source_invalid" {
		t.Fatalf("oversized business payload must stay rejected, got %v", err)
	}
}

func TestNoticeBudgetIncludesArrayAndTruncationMarker(t *testing.T) {
	entries := make([]string, MaxSourceNotices)
	for i := range entries {
		entries[i] = strings.Repeat("a", 510)
	}
	plan, err := Preview(context.Background(), noticesSnapshot(entries), noticesOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan.Notices)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > MaxSourceNoticeBytes {
		t.Fatalf("complete notices JSON exceeds budget: %d > %d", len(encoded), MaxSourceNoticeBytes)
	}
	if !strings.Contains(plan.Notices[len(plan.Notices)-1], "未随本回执持久化") {
		t.Fatal("over-budget array requires an explicit truncation marker")
	}
}
