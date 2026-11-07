package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
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
	return mux, store, context.Background()
}

func pluginAuditPageParamsJSON(store *pluginstore.Service, extra string) string {
	return `{"projectKey":"` + store.ProjectKey() + `","pluginId":"pmvt.project-manager"` + extra + `}`
}

type pluginAuditPageReply struct {
	Events []struct {
		EventID     string  `json:"eventId"`
		StartedAt   string  `json:"startedAt"`
		EventType   string  `json:"eventType"`
		Outcome     string  `json:"outcome"`
		RunID       *string `json:"runId"`
		PackageHash string  `json:"packageHash"`
		DurationMs  *int64  `json:"durationMs"`
	} `json:"events"`
	NextCursor *string `json:"nextCursor"`
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
	seen := make([]string, 0, len(all))
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
		for _, event := range page.Events {
			seen = append(seen, event.EventID)
		}
		// Raw keys, not a decoded struct: proving the projection carries only
		// the summary metadata and never a details document.
		var rawEvents []map[string]json.RawMessage
		if err := json.Unmarshal(envelope.Result, &struct{ Events *[]map[string]json.RawMessage }{&rawEvents}); err != nil {
			t.Fatal(err)
		}
		for index, raw := range rawEvents {
			if len(raw) != 7 {
				t.Fatalf("summary event %d carries %d keys (%v), want exactly the 7 metadata fields", index, len(raw), rawKeys(raw))
			}
		}
		pages++
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if pages < 2 {
		t.Fatalf("expected the fixed small page to split %d events across pages, got %d", len(all), pages)
	}
	if strings.Join(seen, ",") != strings.Join(all, ",") {
		t.Fatalf("cursor chain lost, duplicated or reordered events: got %d of %d", len(seen), len(all))
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

// seedCustomAuditEvent writes one legal audit event with caller-chosen
// metadata strings; validateAuditPayload limits none of their lengths.
func seedCustomAuditEvent(t *testing.T, store *pluginstore.Service, eventID, runID string) {
	t.Helper()
	event, err := json.Marshal(map[string]any{
		"eventId":    eventID,
		"projectKey": store.ProjectKey(), "pluginId": "pmvt.project-manager",
		"pluginVersion": "0.5.0", "packageHash": "sha256:custom",
		"eventType": "model.progress", "outcome": "succeeded", "actor": "local-user",
		"startedAt": "2026-11-07T19:05:58Z", "runId": runID, "details": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordAudit(context.Background(), event); err != nil {
		t.Fatal("custom audit write failed", err)
	}
}

// The byte limit includes array framing even for a one-item page.
func TestPluginListAuditPageCountsArrayFraming(t *testing.T) {
	_, store, ctx := pluginAuditPageFixture(t)
	seedCustomAuditEvent(t, store, "array-boundary", "sample")
	items, _, _, err := store.ListAuditSummaryPage(ctx, store.ProjectKey(), "pmvt.project-manager", 0, 50, 4096)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int{len(encoded), len(encoded) - 1} {
		page, _, more, err := store.ListAuditSummaryPage(ctx, store.ProjectKey(), "pmvt.project-manager", 0, 50, budget)
		if budget == len(encoded) {
			if err != nil || more || len(page) != 1 {
				t.Fatalf("exact array budget: count=%d more=%v error=%v", len(page), more, err)
			}
		} else if failure, ok := err.(*pluginstore.Error); !ok || failure.Code != pluginstore.AuditSummaryPageTooLargeCode {
			t.Fatalf("array framing exceeded budget without explicit failure: %v", err)
		}
	}
}

func TestPluginListAuditPageCountsEscapedJsonBytes(t *testing.T) {
	mux, store, ctx := pluginAuditPageFixture(t)
	// 100k quote characters: ~100k raw but ~200k serialized, under budget.
	for index := 0; index < 3; index++ {
		seedCustomAuditEvent(t, store, fmt.Sprintf("escape-%04d", index), strings.Repeat(`"`, 100_000))
	}
	seen := []string{}
	cursor := ""
	for {
		envelope := schemaProductRequestForMethod(t, mux, ctx, "plugin.listAuditPage",
			pluginAuditPageParamsJSON(store, `,"cursor":"`+cursor+`"`), schemaListWire)
		if envelope.Error != nil {
			t.Fatalf("escaped audit summary page failed: %+v", envelope.Error)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(envelope.Result, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields["events"]) > pluginstore.AuditSummaryPageBudgetBytes {
			t.Fatalf("escaped JSON array exceeds page budget: %d", len(fields["events"]))
		}
		var page pluginAuditPageReply
		if err := json.Unmarshal(envelope.Result, &page); err != nil {
			t.Fatal(err)
		}
		for _, event := range page.Events {
			seen = append(seen, event.EventID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if strings.Join(seen, ",") != "escape-0000,escape-0001,escape-0002" {
		t.Fatalf("escaped chain did not cover the scope exactly once: %v", seen)
	}
}

// TestPluginListAuditPageFailsClosedWhenOneEventExceedsBudget pins the
// pathological record whose serialized summary alone exceeds the page byte
// budget: the page must reject with its own distinguishable error instead of
// answering an unbounded envelope, while the frozen single-event read keeps
// returning the identical original payload.
func TestPluginListAuditPageFailsClosedWhenOneEventExceedsBudget(t *testing.T) {
	mux, store, ctx := pluginAuditPageFixture(t)
	// ~263k raw characters stay under the 512 KiB raw-length estimate but
	// escape to over 512 KiB of JSON: the old estimator would have returned
	// this page as a bounded success.
	seedCustomAuditEvent(t, store, "oversized-run", strings.Repeat(`"`, 263_000))
	project := store.ProjectKey()
	before, err := store.GetAudit(ctx, project, "pmvt.project-manager", "oversized-run")
	if err != nil || len(before) == 0 {
		t.Fatalf("GetAudit before = %d bytes, %v", len(before), err)
	}
	envelope := schemaProductRequestForMethod(t, mux, ctx, "plugin.listAuditPage",
		pluginAuditPageParamsJSON(store, ""), schemaListWire)
	if envelope.Error == nil {
		t.Fatalf("over-budget single summary event was answered: %s", envelope.Result)
	}
	encoded, encodeErr := json.Marshal(envelope)
	if encodeErr != nil || !strings.Contains(string(encoded), "plugin.audit_page_too_large") {
		t.Fatalf("over-budget failure is not distinguishable: %s (%v)", encoded, encodeErr)
	}
	after, err := store.GetAudit(ctx, project, "pmvt.project-manager", "oversized-run")
	if err != nil || string(after) != string(before) {
		t.Fatal("GetAudit payload changed across the rejected summary page")
	}
}

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
