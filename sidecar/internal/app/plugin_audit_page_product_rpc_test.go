package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pocketbase/dbx"
	pocketbase "github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

// seedAuditEvents appends count immutable audit events for one plugin. Each
// payload carries an oversized details document so the complete history far
// exceeds the 4 MiB Product response gate that froze #572.
func seedAuditEvents(t *testing.T, store *pluginstore.Service, count, detailsBytes int) []string {
	t.Helper()
	ctx := context.Background()
	project := store.ProjectKey()
	details := map[string]any{"blob": strings.Repeat("x", detailsBytes)}
	ids := make([]string, 0, count)
	for index := 0; index < count; index++ {
		event, err := json.Marshal(map[string]any{
			"eventId":       fmt.Sprintf("audit-page-%04d", index),
			"projectKey":    project,
			"pluginId":      "pmvt.project-manager",
			"pluginVersion": "0.5.0",
			"packageHash":   "sha256:" + strings.Repeat("a", 20) + fmt.Sprintf("%04d", index),
			"eventType":     []string{"install", "model.progress", "transaction.preview"}[index%3],
			"outcome":       []string{"succeeded", "failed"}[index%2],
			"actor":         "local-user",
			"startedAt":     "2026-11-07T19:05:58Z",
			"runId":         nil,
			"details":       details,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.RecordAudit(ctx, event); err != nil {
			t.Fatal("synthetic audit write failed", err)
		}
		ids = append(ids, fmt.Sprintf("audit-page-%04d", index))
	}
	return ids
}

// The full dispatcher fixture mirrors the production wiring: every Go-owned
// method is registered, the Product HTTP boundary serializes each response.
func pluginAuditPageFixture(t *testing.T) (http.Handler, *pluginstore.Service, context.Context) {
	mux, store, ctx, _ := pluginAuditPageFixtureWithApp(t)
	return mux, store, ctx
}

func pluginAuditPageFixtureWithApp(t *testing.T) (http.Handler, *pluginstore.Service, context.Context, *pocketbase.PocketBase) {
	t.Helper()
	pb := historyProductStore(t, t.TempDir())
	store := pluginstore.New(pb, "11111111-1111-4111-8111-111111111111")
	r := router.NewRouter(func(writer http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: pb, Event: router.Event{Response: writer, Request: request}}, nil
	})
	registrations := PluginSharedStateRegistrations(store)
	for _, descriptor := range productcapabilities.CurrentOwnerRPCDescriptors(productcapabilities.GoSidecar) {
		if !strings.HasPrefix(descriptor.Method, "plugin.") {
			registrations = append(registrations, productrpc.Registration{
				Method: descriptor.Method, Scope: descriptor.Scope,
				ValidateParams: func(json.RawMessage) error { t.Fatal("unrelated validation"); return nil },
				Handler:        func(context.Context, json.RawMessage) (any, error) { t.Fatal("unrelated handler"); return nil, nil },
			})
		}
	}
	dispatcher, err := productrpc.New(
		productrpc.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111",
			SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		registrations...,
	)
	if err != nil {
		t.Fatal(err)
	}
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	return mux, store, context.Background(), pb
}

func pluginAuditPageParamsJSON(store *pluginstore.Service, extra string) string {
	return `{"projectKey":"` + store.ProjectKey() + `","pluginId":"pmvt.project-manager"` + extra + `}`
}

type pluginAuditPageReply struct {
	Events     []pluginstore.AuditSummaryItem `json:"events"`
	NextCursor *string                        `json:"nextCursor"`
}

// walkAuditSummaryPages drives the real Product HTTP chain to its terminal
// page and returns every delivered item in order together with the page count.
func walkAuditSummaryPages(t *testing.T, mux http.Handler, store *pluginstore.Service, ctx context.Context) ([]pluginstore.AuditSummaryItem, int) {
	t.Helper()
	seen := make([]pluginstore.AuditSummaryItem, 0)
	cursor := ""
	pages := 0
	for {
		envelope := schemaProductRequestForMethod(t, mux, ctx, "plugin.listAuditPage",
			pluginAuditPageParamsJSON(store, `,"cursor":"`+cursor+`"`), schemaListWire)
		if envelope.Error != nil {
			t.Fatalf("audit summary page failed: %+v", envelope.Error)
		}
		if len(envelope.Result) >= maxProductRPCResponseBytes {
			t.Fatalf("audit summary page envelope is unbounded: %d bytes", len(envelope.Result))
		}
		var page pluginAuditPageReply
		if err := json.Unmarshal(envelope.Result, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Events) == 0 {
			t.Fatal("summary page is empty before the cursor chain ends")
		}
		if len(page.Events) > pluginstore.AuditSummaryPageSize {
			t.Fatalf("summary page returned %d events over the fixed page size", len(page.Events))
		}
		seen = append(seen, page.Events...)
		pages++
		if page.NextCursor == nil {
			return seen, pages
		}
		cursor = *page.NextCursor
	}
}

// captureAuditRaw snapshots every stored payload through GetAudit before
// the summary pages are read, so the projection can be proven not to touch
// the frozen full-history document.
func captureAuditRaw(t *testing.T, store *pluginstore.Service, ids []string) map[string]json.RawMessage {
	t.Helper()
	ctx := context.Background()
	raws := make(map[string]json.RawMessage, len(ids))
	for _, id := range ids {
		raw, err := store.GetAudit(ctx, store.ProjectKey(), "pmvt.project-manager", id)
		if err != nil || len(raw) == 0 {
			t.Fatalf("GetAudit(%s) = %d bytes, %v", id, len(raw), err)
		}
		var original map[string]json.RawMessage
		if err := json.Unmarshal(raw, &original); err != nil {
			t.Fatalf("GetAudit(%s) is not one JSON object: %v", id, err)
		}
		raws[id] = raw
	}
	return raws
}

// assertAuditRawPreserved pins the frozen boundary of the projection: every
// stored payload must stay byte-for-byte reachable through GetAudit after the
// bounded pages were served.
func assertAuditRawPreserved(t *testing.T, store *pluginstore.Service, before map[string]json.RawMessage) {
	t.Helper()
	ctx := context.Background()
	for id, want := range before {
		after, err := store.GetAudit(ctx, store.ProjectKey(), "pmvt.project-manager", id)
		if err != nil {
			t.Fatalf("GetAudit(%s) failed after the pages: %v", id, err)
		}
		if string(after) != string(want) {
			t.Fatalf("GetAudit(%s) payload changed across the bounded pages", id)
		}
	}
}

// TestPluginListAuditOversizedHistoryStillFailsClosedAtTheWriter is the #572
// regression: the frozen full-history method keeps its complete-list answer,
// so a workspace whose audit history exceeds the 4 MiB envelope cap keeps
// failing at the formal HTTP writer seam. The bounded summary page below is
// the product fix; this assertion pins the unfrozen boundary it must not
// weaken.
func TestPluginListAuditOversizedHistoryStillFailsClosedAtTheWriter(t *testing.T) {
	mux, store, ctx := pluginAuditPageFixture(t)
	// 70 events at 70 KiB details mirror the diagnosed #572 shape: a
	// complete history far over the 4 MiB Product envelope cap.
	seedAuditEvents(t, store, 70, 70<<10)
	envelope := schemaProductRequestForMethod(t, mux, ctx, "plugin.listAudit", pluginAuditPageParamsJSON(store, ""), schemaListWire)
	if envelope.Error == nil {
		t.Fatal("oversized full audit history unexpectedly succeeded at the writer boundary")
	}
	if envelope.Error.Code != productrpc.CodeInternalError {
		t.Fatalf("full audit history writer failure code = %d, want %d", envelope.Error.Code, productrpc.CodeInternalError)
	}
}

// TestPluginListAuditPageBoundsOversizedHistory proves the summary pages
// succeed page by page where the full history is rejected: every page stays
// under the envelope cap, details never cross the projection, and the cursor
// chain covers every stored event id exactly once without gaps.
func TestPluginListAuditPageBoundsOversizedHistory(t *testing.T) {
	mux, store, ctx := pluginAuditPageFixture(t)
	all := seedAuditEvents(t, store, 70, 70<<10) // > 4 MiB total payload.
	before := captureAuditRaw(t, store, all)
	seen, pages := walkAuditSummaryPages(t, mux, store, ctx)
	if pages < 2 {
		t.Fatalf("expected the fixed small page to split %d events across pages, got %d", len(all), pages)
	}
	ids := make([]string, 0, len(seen))
	for _, event := range seen {
		ids = append(ids, event.EventID)
	}
	if strings.Join(ids, ",") != strings.Join(all, ",") {
		t.Fatalf("cursor chain lost, duplicated or reordered events: got %d of %d", len(ids), len(all))
	}
	for _, event := range seen {
		if event.MetadataLimited {
			t.Fatalf("bounded legacy metadata of seeded events must not raise metadataLimited: %+v", event)
		}
	}
	assertAuditRawPreserved(t, store, before)
}

// TestPluginListAuditPageCountsArrayFraming pins the actual json.Marshal
// accounting on worst-case sized items: a page closes exactly when the array
// brackets plus separators plus escaped item bytes would exceed the budget,
// and the internal paging contract rejects budgets that can never fit one
// bounded item.
func TestPluginListAuditPageCountsArrayFraming(t *testing.T) {
	_, store, ctx := pluginAuditPageFixture(t)
	// Two maximally escaped items: each is far over the internal item ceiling
	// so exact framing boundaries also sit above the request guard.
	controlRunes := strings.Repeat("\u0001", pluginstore.AuditSummaryPreviewRunes+4096)
	for _, id := range []string{"array-first", "array-second"} {
		seedRawAuditEvent(t, store, customAuditEvent(store, id, func(event map[string]any) {
			event["eventType"] = controlRunes
			event["outcome"] = controlRunes
			event["packageHash"] = controlRunes
			event["runId"] = controlRunes
		}))
	}
	both, _, _, err := store.ListAuditSummaryPage(ctx, store.ProjectKey(), "pmvt.project-manager", 0, 50, pluginstore.AuditSummaryPageBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(both) != 2 {
		t.Fatalf("framing fixture page = %d items, want 2", len(both))
	}
	encodedFirst, err := json.Marshal(both[0])
	if err != nil {
		t.Fatal(err)
	}
	encodedSecond, err := json.Marshal(both[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(encodedFirst) < pluginstore.AuditSummaryItemBudgetBytes/2 {
		t.Fatalf("framing fixture item = %d bytes, too small to straddle the ceiling guard", len(encodedFirst))
	}
	exact := 2 + len(encodedFirst) + 1 + len(encodedSecond)
	for _, budget := range []int{exact, exact - 1} {
		page, next, more, pageErr := store.ListAuditSummaryPage(ctx, store.ProjectKey(), "pmvt.project-manager", 0, 50, budget)
		if pageErr != nil {
			t.Fatalf("budget %d: %v", budget, pageErr)
		}
		if budget == exact {
			if len(page) != 2 || more {
				t.Fatalf("exact two-item budget: count=%d more=%v", len(page), more)
			}
			continue
		}
		// One separator byte short: the second bounded item closes the page
		// and stays the next page's first event — nothing is skipped.
		if len(page) != 1 || !more || next == 0 {
			t.Fatalf("separator boundary: count=%d more=%v next=%d", len(page), more, next)
		}
	}
	if _, _, _, err := store.ListAuditSummaryPage(ctx, store.ProjectKey(), "pmvt.project-manager", 0, 50, pluginstore.AuditSummaryItemBudgetBytes-1); err == nil {
		t.Fatal("budget below the bounded item ceiling was accepted")
	} else if failure, ok := err.(*pluginstore.Error); !ok || failure.Code != "plugin.request_invalid" {
		t.Fatalf("budget below the ceiling must be request invalid: %v", err)
	}
}

// seedCustomAuditEvent writes one legal audit event with caller-chosen
// metadata strings; validateAuditPayload limits none of their lengths.
func seedCustomAuditEvent(t *testing.T, store *pluginstore.Service, eventID, runID string) {
	t.Helper()
	seedRawAuditEvent(t, store, map[string]any{
		"eventId":    eventID,
		"projectKey": store.ProjectKey(), "pluginId": "pmvt.project-manager",
		"pluginVersion": "0.5.0", "packageHash": "sha256:custom",
		"eventType": "model.progress", "outcome": "succeeded", "actor": "local-user",
		"startedAt": "2026-11-07T19:05:58Z", "runId": runID, "details": map[string]any{},
	})
}

// seedRawAuditEvent writes one legal audit payload verbatim. The frozen write
// validation never type-checks runId/durationMs and limits no metadata text
// length, so legacy shapes stay legal here exactly as in production.
func seedRawAuditEvent(t *testing.T, store *pluginstore.Service, event map[string]any) {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordAudit(context.Background(), raw); err != nil {
		t.Fatalf("synthetic audit write failed for %v: %v", event["eventId"], err)
	}
}

func customAuditEvent(store *pluginstore.Service, eventID string, mutate func(map[string]any)) map[string]any {
	event := map[string]any{
		"eventId":    eventID,
		"projectKey": store.ProjectKey(), "pluginId": "pmvt.project-manager",
		"pluginVersion": "0.5.0", "packageHash": "sha256:custom",
		"eventType": "model.progress", "outcome": "succeeded", "actor": "local-user",
		"startedAt": "2026-11-07T19:05:58Z", "runId": nil, "details": map[string]any{},
	}
	if mutate != nil {
		mutate(event)
	}
	return event
}

// TestPluginListAuditPageCountsEscapedJsonBytes pins the escaped-byte budget:
// quote-heavy legacy runIds serialize to several times their raw length, so
// the projection previews them and the page bytes stay bounded while the
// cursor chain still covers the scope exactly once.
func TestPluginListAuditPageCountsEscapedJsonBytes(t *testing.T) {
	mux, store, ctx := pluginAuditPageFixture(t)
	// 100k quote characters each: ~100k raw but ~200k serialized unbounded.
	for index := 0; index < 3; index++ {
		seedCustomAuditEvent(t, store, fmt.Sprintf("escape-%04d", index), strings.Repeat(`"`, 100_000))
	}
	before := captureAuditRaw(t, store, []string{"escape-0000", "escape-0001", "escape-0002"})
	seen, pages := walkAuditSummaryPages(t, mux, store, ctx)
	ids := make([]string, 0, len(seen))
	for _, event := range seen {
		ids = append(ids, event.EventID)
		if !event.MetadataLimited {
			t.Fatalf("escaped runId preview must raise metadataLimited: %+v", event)
		}
		if runID := derefString(event.RunID); utf8.RuneCountInString(runID) > pluginstore.AuditSummaryPreviewRunes {
			t.Fatalf("escaped runId preview exceeds the preview limit: %d runes", utf8.RuneCountInString(runID))
		}
	}
	if strings.Join(ids, ",") != "escape-0000,escape-0001,escape-0002" {
		t.Fatalf("escaped chain did not cover the scope exactly once: %v", ids)
	}
	if pages != 1 {
		t.Fatalf("three previewed events must fit one page, got %d", pages)
	}
	assertAuditRawPreserved(t, store, before)
}

// TestPluginListAuditPageProjectsUnboundedMetadataBetweenNormalEvents is the
// core #572 remediation contract: an overlong legacy record sitting between
// normal records no longer fails the whole page. The real HTTP chain delivers
// every eventId complete and in order, overlong display metadata becomes a
// rune-safe preview with the explicit omission marker plus metadataLimited,
// normal rows stay unflagged, and every original payload survives untouched
// on the frozen reads.
func TestPluginListAuditPageProjectsUnboundedMetadataBetweenNormalEvents(t *testing.T) {
	mux, store, ctx := pluginAuditPageFixture(t)
	overlong := strings.Repeat("模", 40_000) // 40k multibyte runes, far over the preview limit.
	seedCustomAuditEvent(t, store, "between-first", "run-normal")
	seedRawAuditEvent(t, store, customAuditEvent(store, "between-huge", func(event map[string]any) {
		event["eventType"] = overlong
		event["outcome"] = overlong
		event["packageHash"] = overlong
		event["runId"] = overlong
	}))
	seedCustomAuditEvent(t, store, "between-last", "run-normal")
	before := captureAuditRaw(t, store, []string{"between-first", "between-huge", "between-last"})
	seen, pages := walkAuditSummaryPages(t, mux, store, ctx)
	if pages != 1 {
		t.Fatalf("three bounded events must fit one page, got %d", pages)
	}
	ids := make([]string, 0, len(seen))
	for _, event := range seen {
		ids = append(ids, event.EventID)
	}
	if strings.Join(ids, ",") != "between-first,between-huge,between-last" {
		t.Fatalf("chain lost or reordered events around the overlong record: %v", ids)
	}
	huge := seen[1]
	if !huge.MetadataLimited {
		t.Fatal("overlong metadata must raise metadataLimited")
	}
	for name, value := range map[string]string{
		"eventType": huge.EventType, "outcome": huge.Outcome,
		"packageHash": huge.PackageHash, "runId": derefString(huge.RunID),
	} {
		if runes := utf8.RuneCountInString(value); runes != pluginstore.AuditSummaryPreviewRunes {
			t.Fatalf("%s preview = %d runes, want exactly the bounded limit", name, runes)
		}
		if !strings.HasSuffix(value, "…") {
			t.Fatalf("%s preview lacks the explicit omission marker", name)
		}
	}
	for _, index := range []int{0, 2} {
		if seen[index].MetadataLimited {
			t.Fatalf("normal neighbor must stay unflagged: %+v", seen[index])
		}
		if seen[index].EventType != "model.progress" || derefString(seen[index].RunID) != "run-normal" {
			t.Fatalf("normal neighbor metadata changed: %+v", seen[index])
		}
	}
	assertAuditRawPreserved(t, store, before)
}

// TestPluginListAuditPageProjectsNonStandardOptionalLegacyValues pins the
// tolerant optional handling: legacy records with non-standard runId or
// durationMs values the frozen writes accepted must never fail their page —
// they project to null with metadataLimited, while normal null/absent values
// stay null without a false flag and exact integers are kept verbatim.
func TestPluginListAuditPageProjectsNonStandardOptionalLegacyValues(t *testing.T) {
	mux, store, ctx := pluginAuditPageFixture(t)
	cases := []struct {
		id       string
		mutate   func(map[string]any)
		limited  bool
		runID    *string
		duration *int64
	}{
		{"opt-null", func(e map[string]any) { e["runId"] = nil; e["durationMs"] = nil }, false, nil, nil},
		{"opt-absent", func(e map[string]any) { delete(e, "runId"); delete(e, "durationMs") }, false, nil, nil},
		{"opt-values", func(e map[string]any) {
			e["runId"] = "run-42"
			e["durationMs"] = 1500
		}, false, ptr("run-42"), i64(1500)},
		{"opt-run-number", func(e map[string]any) { e["runId"] = 12345 }, true, nil, nil},
		{"opt-run-object", func(e map[string]any) { e["runId"] = map[string]any{"nested": true} }, true, nil, nil},
		{"opt-run-array", func(e map[string]any) { e["runId"] = []any{"a"} }, true, nil, nil},
		{"opt-duration-float", func(e map[string]any) { e["durationMs"] = 12.5 }, true, nil, nil},
		{"opt-duration-float-zero", func(e map[string]any) { e["durationMs"] = json.RawMessage("1000.0") }, true, nil, nil},
		{"opt-duration-string", func(e map[string]any) { e["durationMs"] = "fast" }, true, nil, nil},
		{"opt-duration-numeric-string", func(e map[string]any) { e["durationMs"] = "42" }, true, nil, nil},
		{"opt-duration-exponent", func(e map[string]any) { e["durationMs"] = json.RawMessage("1e3") }, true, nil, nil},
		{"opt-duration-bool", func(e map[string]any) { e["durationMs"] = false }, true, nil, nil},
		{"opt-duration-negative", func(e map[string]any) { e["durationMs"] = -1 }, true, nil, nil},
		{"opt-duration-zero", func(e map[string]any) { e["durationMs"] = 0 }, false, nil, i64(0)},
		{"opt-duration-safe-max", func(e map[string]any) { e["durationMs"] = int64(9007199254740991) }, false, nil, i64(9007199254740991)},
		{"opt-duration-unsafe", func(e map[string]any) { e["durationMs"] = int64(9007199254740992) }, true, nil, nil},
	}
	ids := make([]string, 0, len(cases))
	for _, testCase := range cases {
		seedRawAuditEvent(t, store, customAuditEvent(store, testCase.id, testCase.mutate))
		ids = append(ids, testCase.id)
	}
	before := captureAuditRaw(t, store, ids)
	seen, _ := walkAuditSummaryPages(t, mux, store, ctx)
	if len(seen) != len(cases) {
		t.Fatalf("chain delivered %d of %d events", len(seen), len(cases))
	}
	for index, testCase := range cases {
		event := seen[index]
		if event.EventID != testCase.id {
			t.Fatalf("chain reordered events: got %+v", seen)
		}
		if event.MetadataLimited != testCase.limited {
			t.Fatalf("%s metadataLimited = %v, want %v", testCase.id, event.MetadataLimited, testCase.limited)
		}
		if event.RunID == nil != (testCase.runID == nil) || (event.RunID != nil && *event.RunID != *testCase.runID) {
			t.Fatalf("%s runId = %v, want %v", testCase.id, event.RunID, testCase.runID)
		}
		if event.DurationMs == nil != (testCase.duration == nil) || (event.DurationMs != nil && *event.DurationMs != *testCase.duration) {
			t.Fatalf("%s durationMs = %v, want %v", testCase.id, event.DurationMs, testCase.duration)
		}
	}
	assertAuditRawPreserved(t, store, before)
}

// TestPluginListAuditPageNormalizesLongFractionStartedAt pins the timestamp
// contract: legal RFC3339 fractions of any length stay valid dates under the
// existing time semantics. Fractions beyond nanosecond precision normalize
// bounded and raise metadataLimited; zero-padding and offsets are cosmetic
// and never flagged; the stored original stays untouched.
func TestPluginListAuditPageNormalizesLongFractionStartedAt(t *testing.T) {
	mux, store, ctx := pluginAuditPageFixture(t)
	cases := []struct {
		id         string
		startedAt  string
		limited    bool
		normalized string
	}{
		{"ts-plain", "2026-11-07T19:05:58Z", false, "2026-11-07T19:05:58Z"},
		{"ts-nanos", "2026-11-07T19:05:58.123456789Z", false, "2026-11-07T19:05:58.123456789Z"},
		{"ts-padded", "2026-11-07T19:05:58.1000000000000Z", false, "2026-11-07T19:05:58.1Z"},
		{"ts-offset", "2026-11-08T03:05:58.500+08:00", false, "2026-11-08T03:05:58.5+08:00"},
		{"ts-overlong", "2026-11-07T19:05:58.123456789123456789Z", true, "2026-11-07T19:05:58.123456789Z"},
		// Go's RFC3339 parser (and therefore the frozen write validation)
		// accepts ',' as the fraction separator; both separators must count.
		{"ts-comma-nanos", "2026-11-07T19:05:58,123456789Z", false, "2026-11-07T19:05:58.123456789Z"},
		{"ts-comma-padded", "2026-11-07T19:05:58,1000000000000Z", false, "2026-11-07T19:05:58.1Z"},
		{"ts-comma-overlong", "2026-11-07T19:05:58,123456789123456789Z", true, "2026-11-07T19:05:58.123456789Z"},
	}
	ids := make([]string, 0, len(cases))
	for _, testCase := range cases {
		seedRawAuditEvent(t, store, customAuditEvent(store, testCase.id, func(event map[string]any) {
			event["startedAt"] = testCase.startedAt
		}))
		ids = append(ids, testCase.id)
	}
	before := captureAuditRaw(t, store, ids)
	seen, _ := walkAuditSummaryPages(t, mux, store, ctx)
	for index, testCase := range cases {
		event := seen[index]
		if event.MetadataLimited != testCase.limited {
			t.Fatalf("%s metadataLimited = %v, want %v", testCase.id, event.MetadataLimited, testCase.limited)
		}
		if event.StartedAt == nil || *event.StartedAt != testCase.normalized {
			t.Fatalf("%s startedAt = %v, want %q", testCase.id, event.StartedAt, testCase.normalized)
		}
	}
	assertAuditRawPreserved(t, store, before)
}

// TestPluginListAuditPageBoundedItemAlwaysFitsPageBudget proves the
// construction bound: even worst-case escaped previews (control runes that
// serialize to six bytes each) keep every item far below both the internal
// ceiling and the fixed 512 KiB page budget, so a history consisting only of
// pathological records still pages through the real HTTP chain to its end
// without skipping a single eventId.
func TestPluginListAuditPageBoundedItemAlwaysFitsPageBudget(t *testing.T) {
	mux, store, ctx := pluginAuditPageFixture(t)
	const count = 60
	controlRunes := strings.Repeat("\u0001", pluginstore.AuditSummaryPreviewRunes+4096)
	ids := make([]string, 0, count)
	for index := 0; index < count; index++ {
		id := worstCaseEventID(index)
		seedRawAuditEvent(t, store, customAuditEvent(store, id, func(event map[string]any) {
			event["eventType"] = controlRunes
			event["outcome"] = controlRunes
			event["packageHash"] = controlRunes
			event["runId"] = controlRunes
		}))
		ids = append(ids, id)
	}
	before := captureAuditRaw(t, store, ids)
	seen, pages := walkAuditSummaryPages(t, mux, store, ctx)
	if len(seen) != count {
		t.Fatalf("worst-case chain delivered %d of %d events", len(seen), count)
	}
	if pages < 2 {
		t.Fatalf("worst-case events must split across pages, got %d", pages)
	}
	for index, event := range seen {
		if event.EventID != ids[index] {
			t.Fatalf("worst-case chain reordered events: got %s, want %s", event.EventID, ids[index])
		}
		if !event.MetadataLimited {
			t.Fatalf("worst-case preview must raise metadataLimited: %+v", event)
		}
		if encoded, err := json.Marshal(event); err != nil || len(encoded) > pluginstore.AuditSummaryItemBudgetBytes {
			t.Fatalf("bounded item %d exceeds the construction ceiling: %d bytes (%v)", index, len(encoded), err)
		}
	}
	assertAuditRawPreserved(t, store, before)
}

// worstCaseEventID keeps the full item_key budget (512 chars) of the shared
// state migration while staying distinct per seeded index.
func worstCaseEventID(index int) string {
	return strings.Repeat("e", 508) + fmt.Sprintf("%04d", index)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func ptr(value string) *string { return &value }

func i64(value int64) *int64 { return &value }

// TestPluginListAuditPageScopeAndCursorGuards pins request validation: an
// unknown plugin yields a clean empty first page, forged or cross-scope
// cursors are rejected as invalid params without leaking other scopes, and
// the boundary cursor after the newest event settles an empty terminal page.
func TestPluginListAuditPageScopeAndCursorGuards(t *testing.T) {
	mux, store, ctx := pluginAuditPageFixture(t)
	seedAuditEvents(t, store, pluginstore.AuditSummaryPageSize+3, 64)
	empty := schemaProductRequestForMethod(t, mux, ctx, "plugin.listAuditPage",
		`{"projectKey":"`+store.ProjectKey()+`","pluginId":"unknown.plugin"}`, schemaListWire)
	if empty.Error != nil {
		t.Fatalf("unknown plugin first page failed: %+v", empty.Error)
	}
	var emptyPage pluginAuditPageReply
	if err := json.Unmarshal(empty.Result, &emptyPage); err != nil {
		t.Fatal(err)
	}
	if len(emptyPage.Events) != 0 || emptyPage.NextCursor != nil {
		t.Fatalf("unknown plugin page = %+v, want empty terminal page", emptyPage)
	}
	// Walk the chain to the terminal page and keep the last issued cursor.
	terminal := ""
	for {
		envelope := schemaProductRequestForMethod(t, mux, ctx, "plugin.listAuditPage",
			pluginAuditPageParamsJSON(store, `,"cursor":"`+terminal+`"`), schemaListWire)
		if envelope.Error != nil {
			t.Fatalf("audit summary page failed: %+v", envelope.Error)
		}
		var page pluginAuditPageReply
		if err := json.Unmarshal(envelope.Result, &page); err != nil {
			t.Fatal(err)
		}
		if page.NextCursor == nil {
			if len(page.Events) != 3 {
				t.Fatalf("terminal short page returned %d events, want the 3 remainder", len(page.Events))
			}
			break
		}
		terminal = *page.NextCursor
	}
	// A cursor at or past the newest seq settles an empty terminal page.
	last := encodePluginAuditPageCursor(pluginAuditPageCursor{
		ProjectKey: store.ProjectKey(), PluginID: "pmvt.project-manager", AfterSeq: pluginstore.AuditSummaryPageSize + 3,
	})
	end := schemaProductRequestForMethod(t, mux, ctx, "plugin.listAuditPage",
		pluginAuditPageParamsJSON(store, `,"cursor":"`+last+`"`), schemaListWire)
	if end.Error != nil {
		t.Fatalf("boundary cursor failed: %+v", end.Error)
	}
	var endPage pluginAuditPageReply
	if err := json.Unmarshal(end.Result, &endPage); err != nil {
		t.Fatal(err)
	}
	if len(endPage.Events) != 0 || endPage.NextCursor != nil {
		t.Fatalf("boundary page = %+v, want empty terminal page", endPage)
	}
	for name, cursor := range map[string]string{
		"malformed":     "not-base64!!",
		"foreign-json":  base64.RawURLEncoding.EncodeToString([]byte(`{"projectKey":"other"}`)),
		"zero-seq":      encodePluginAuditPageCursor(pluginAuditPageCursor{ProjectKey: store.ProjectKey(), PluginID: "pmvt.project-manager"}),
		"cross-plugin":  encodePluginAuditPageCursor(pluginAuditPageCursor{ProjectKey: store.ProjectKey(), PluginID: "other.plugin", AfterSeq: 1}),
		"cross-project": encodePluginAuditPageCursor(pluginAuditPageCursor{ProjectKey: "local:00000000000000000000000000000000", PluginID: "pmvt.project-manager", AfterSeq: 1}),
	} {
		envelope := schemaProductRequestForMethod(t, mux, ctx, "plugin.listAuditPage",
			pluginAuditPageParamsJSON(store, `,"cursor":"`+cursor+`"`), schemaListWire)
		if envelope.Error == nil || envelope.Error.Code != productrpc.CodeInvalidParams {
			t.Fatalf("%s cursor accepted: %+v", name, envelope.Error)
		}
	}
}

func rawKeys(raw map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestPluginListAuditPageProjectionKeySet pins the frozen projection shape:
// exactly the eight summary metadata fields cross the wire, the details
// document never appears, and the raw keys stay stable across pages.
func TestPluginListAuditPageProjectionKeySet(t *testing.T) {
	mux, store, ctx := pluginAuditPageFixture(t)
	all := seedAuditEvents(t, store, 55, 512)
	cursor := ""
	for {
		envelope := schemaProductRequestForMethod(t, mux, ctx, "plugin.listAuditPage",
			pluginAuditPageParamsJSON(store, `,"cursor":"`+cursor+`"`), schemaListWire)
		if envelope.Error != nil {
			t.Fatalf("audit summary page failed: %+v", envelope.Error)
		}
		var raw struct {
			Events []map[string]json.RawMessage `json:"events"`
		}
		if err := json.Unmarshal(envelope.Result, &raw); err != nil {
			t.Fatal(err)
		}
		for index, event := range raw.Events {
			if len(event) != 8 {
				t.Fatalf("summary event %d carries %d keys (%v), want exactly the 8 metadata fields", index, len(event), rawKeys(event))
			}
			if _, leaked := event["details"]; leaked {
				t.Fatalf("summary event %d leaked the details document", index)
			}
		}
		var page pluginAuditPageReply
		if err := json.Unmarshal(envelope.Result, &page); err != nil {
			t.Fatal(err)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	_ = all
}

// wireAuditSummaryEvent mirrors the public wire shape with the nullable
// startedAt the summary contract declares; asserting against this local
// struct keeps the test honest about what actually crosses HTTP.
type wireAuditSummaryEvent struct {
	EventID         string  `json:"eventId"`
	StartedAt       *string `json:"startedAt"`
	EventType       string  `json:"eventType"`
	Outcome         string  `json:"outcome"`
	RunID           *string `json:"runId"`
	PackageHash     string  `json:"packageHash"`
	DurationMs      *int64  `json:"durationMs"`
	MetadataLimited bool    `json:"metadataLimited"`
}

func walkWireAuditSummaries(t *testing.T, mux http.Handler, store *pluginstore.Service, ctx context.Context) []wireAuditSummaryEvent {
	t.Helper()
	var collected []wireAuditSummaryEvent
	cursor := ""
	for {
		envelope := schemaProductRequestForMethod(t, mux, ctx, "plugin.listAuditPage",
			pluginAuditPageParamsJSON(store, `,"cursor":"`+cursor+`"`), schemaListWire)
		if envelope.Error != nil {
			t.Fatalf("audit summary page failed: %+v", envelope.Error)
		}
		var page struct {
			Events     []wireAuditSummaryEvent `json:"events"`
			NextCursor *string                 `json:"nextCursor"`
		}
		if err := json.Unmarshal(envelope.Result, &page); err != nil {
			t.Fatal(err)
		}
		collected = append(collected, page.Events...)
		if page.NextCursor == nil {
			return collected
		}
		cursor = *page.NextCursor
	}
}

// captureStoredAuditRaw snapshots the original payload_json column bytes of
// every audit record of the scope straight from this test's own synthetic
// database. Unlike GetAudit it never re-encodes, so it stays a valid
// "before" witness even for HTML-heavy payloads the frozen full read keeps
// rejecting at its 4 MiB writer boundary.
func captureStoredAuditRaw(t *testing.T, pb *pocketbase.PocketBase, store *pluginstore.Service) map[string]json.RawMessage {
	t.Helper()
	rows, err := pb.FindRecordsByFilter("vibetable_plugin_records",
		"kind={:kind} && project_key={:project} && plugin_id={:plugin}",
		"+seq, +id", 1000, 0, dbx.Params{
			"kind": "audit", "project": store.ProjectKey(), "plugin": "pmvt.project-manager",
		})
	if err != nil {
		t.Fatal(err)
	}
	raws := make(map[string]json.RawMessage, len(rows))
	for _, row := range rows {
		column, ok := row.GetRaw("payload_json").(pbtypes.JSONRaw)
		if !ok || len(column) == 0 {
			t.Fatalf("stored audit row %s has no raw payload column", row.GetString("item_key"))
		}
		raws[row.GetString("item_key")] = json.RawMessage(column)
	}
	if len(raws) == 0 {
		t.Fatal("no stored audit rows found for the synthetic scope")
	}
	return raws
}

func assertStoredAuditRawPreserved(t *testing.T, pb *pocketbase.PocketBase, store *pluginstore.Service, before map[string]json.RawMessage) {
	t.Helper()
	after := captureStoredAuditRaw(t, pb, store)
	if len(after) != len(before) {
		t.Fatalf("stored audit row count changed: %d -> %d", len(before), len(after))
	}
	for id, want := range before {
		if string(after[id]) != string(want) {
			t.Fatalf("stored payload of %s changed across the summary pages", id)
		}
	}
}

// TestPluginListAuditPageReadsLegallyStoredHtmlHeavyRawPayloads pins the
// frozen writer/read split: RecordAudit legally stores a payload whose
// details carry 800k literal '<' bytes (under 1 MiB raw, far over 4 MiB once
// the legacy reader's HTML-escaping json.Marshal expands it). The bounded
// summary read must page across it at its true stored size, reach the normal
// neighbors, and leave the stored bytes untouched; the old GetAudit reader
// keeps its own frozen boundary and is deliberately not the witness here.
func TestPluginListAuditPageReadsLegallyStoredHtmlHeavyRawPayloads(t *testing.T) {
	mux, store, ctx, pb := pluginAuditPageFixtureWithApp(t)
	seedCustomAuditEvent(t, store, "html-first", "run-normal")
	// Encode without HTML escaping so the stored document keeps the literal
	// '<' bytes — exactly what a formal writer preserving original text saves.
	heavy := customAuditEvent(store, "html-heavy", func(event map[string]any) {
		event["runId"] = "run-normal"
	})
	heavy["details"] = map[string]any{"blob": strings.Repeat("<", 800_000)}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(heavy); err != nil {
		t.Fatal(err)
	}
	raw := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	if len(raw) >= 1<<20 {
		t.Fatalf("html-heavy fixture payload = %d bytes, want under PocketBase's 1 MiB raw bound", len(raw))
	}
	if _, err := store.RecordAudit(context.Background(), raw); err != nil {
		t.Fatal("formal RecordAudit rejected the html-heavy payload", err)
	}
	seedCustomAuditEvent(t, store, "html-last", "run-normal")
	before := captureStoredAuditRaw(t, pb, store)
	if len(before) != 3 {
		t.Fatalf("synthetic scope stored %d rows, want 3", len(before))
	}
	if !bytes.Equal(before["html-heavy"], raw) {
		t.Fatal("formal RecordAudit changed the original HTML-heavy payload")
	}

	collected := walkWireAuditSummaries(t, mux, store, ctx)
	ids := make([]string, 0, len(collected))
	for _, event := range collected {
		ids = append(ids, event.EventID)
	}
	if strings.Join(ids, ",") != "html-first,html-heavy,html-last" {
		t.Fatalf("chain lost or reordered events around the html-heavy record: %v", ids)
	}
	heavyEvent := collected[1]
	if heavyEvent.MetadataLimited {
		t.Fatalf("html-heavy details never cross the projection: %+v", heavyEvent)
	}
	if heavyEvent.EventType != "model.progress" || derefString(heavyEvent.RunID) != "run-normal" {
		t.Fatalf("html-heavy summary metadata changed: %+v", heavyEvent)
	}
	for _, index := range []int{0, 2} {
		if collected[index].MetadataLimited {
			t.Fatalf("normal neighbor must stay unflagged: %+v", collected[index])
		}
	}
	assertStoredAuditRawPreserved(t, pb, store, before)
}

// TestPluginListAuditPageEmitsLegalRfc3339OrNullStartedAt pins the summary
// wire's startedAt: normal legal offsets keep their representation; offsets
// of ±24h or more (which Go's frozen write validation accepted and plain
// formatting would emit illegally) shift the same instant to UTC and raise
// metadataLimited; when even the UTC instant leaves the four-digit year
// range the summary carries an explicit null instead of a fabricated date.
type startedAtCase struct {
	id         string
	startedAt  string
	limited    bool
	normalized *string
}

func TestPluginListAuditPageEmitsLegalRfc3339OrNullStartedAt(t *testing.T) {
	inUTC := func(value string) *string { return &value }
	cases := []startedAtCase{
		{"off-normal", "2026-11-08T03:05:58.5+08:00", false,
			inUTC("2026-11-08T03:05:58.5+08:00")},
		{"off-plus-24", "2026-11-07T19:05:58+24:00", true,
			inUTC("2026-11-06T19:05:58Z")},
		{"off-minus-24", "2026-11-07T19:05:58-24:00", true,
			inUTC("2026-11-08T19:05:58Z")},
		{"off-plus-24-60", "2026-11-07T19:05:58+24:60", true,
			inUTC("2026-11-06T18:05:58Z")},
		// The UTC shift would leave the legal four-digit year range; the
		// summary must answer null instead of truncating or faking a year.
		{"off-year-underflow", "0000-01-01T00:00:00+24:00", true, nil},
		{"off-year-overflow", "9999-12-31T23:59:59-24:00", true, nil},
		{"off-year-zero", "0000-01-01T00:00:00Z", true, nil},
		{"off-normalized-year-zero", "0001-01-01T00:00:00+24:00", true, nil},
		{"off-year-zero-representable", "0000-12-31T23:00:00-01:00", true,
			inUTC("0001-01-01T00:00:00Z")},
	}
	mux, store, ctx := pluginAuditPageFixture(t)
	ids := make([]string, 0, len(cases))
	for _, testCase := range cases {
		seedRawAuditEvent(t, store, customAuditEvent(store, testCase.id, func(event map[string]any) {
			event["startedAt"] = testCase.startedAt
		}))
		ids = append(ids, testCase.id)
	}
	before := captureAuditRaw(t, store, ids)
	collected := walkWireAuditSummaries(t, mux, store, ctx)
	if len(collected) != len(cases) {
		t.Fatalf("chain delivered %d of %d events", len(collected), len(cases))
	}
	for index, testCase := range cases {
		event := collected[index]
		if event.EventID != testCase.id {
			t.Fatalf("chain reordered events: got %+v", collected)
		}
		if event.MetadataLimited != testCase.limited {
			t.Fatalf("%s metadataLimited = %v, want %v", testCase.id, event.MetadataLimited, testCase.limited)
		}
		if (event.StartedAt == nil) != (testCase.normalized == nil) ||
			(event.StartedAt != nil && *event.StartedAt != *testCase.normalized) {
			t.Fatalf("%s startedAt = %v, want %v", testCase.id, event.StartedAt, testCase.normalized)
		}
		if event.StartedAt != nil {
			instant, err := time.Parse(time.RFC3339, *event.StartedAt)
			original, originalErr := time.Parse(time.RFC3339, testCase.startedAt)
			if err != nil || originalErr != nil || !instant.Equal(original) {
				t.Fatalf("%s startedAt %q is not the same legal RFC3339 instant (%v, %v)",
					testCase.id, *event.StartedAt, err, originalErr)
			}
		}
	}
	assertAuditRawPreserved(t, store, before)
}
