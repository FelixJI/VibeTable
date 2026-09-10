package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func restoreProductRequest(t *testing.T, mux http.Handler, method, params, wire string, ctx context.Context) productrpc.ResponseEnvelope {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":"restore","method":"` + method + `","wire":` + wire + `,"params":` + params + `}`
	request := httptest.NewRequest(http.MethodPost, productRPCPath, strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	var response productrpc.ResponseEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestHistoryRestoreProductHTTPUsesSharedTokenAndSingleBusinessReceipt(t *testing.T) {
	for _, scenario := range []string{"commit", "conflict", "expired", "cancelled", "workspace-preview"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
			runtime, _, mux, table, item, pb := historyRestoreProductFixture(t, audit.WithClock(func() time.Time { return now }))
			page, err := runtime.ReadBusinessHistory(context.Background(), audit.ReadParams{TableID: table, ItemID: &item, Scope: "row", Limit: 20})
			if err != nil || len(page.ChangeSets) != 1 {
				t.Fatalf("history: %#v %v", page, err)
			}
			meta, err := pb.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": table})
			if err != nil {
				t.Fatal(err)
			}
			record, err := pb.FindRecordById(meta.GetString("collection_id"), item)
			if err != nil {
				t.Fatal(err)
			}
			field := ""
			for key, value := range record.PublicExport() {
				if value == "first" {
					field = key
				}
			}
			if field == "" {
				t.Fatal("missing title fixture")
			}
			record.Set(field, "second")
			if err := pb.Save(record); err != nil {
				t.Fatal(err)
			}
			count := func() int {
				var n int
				if err := pb.DB().NewQuery("SELECT COUNT(*) FROM workspace_v2_mutation_receipts").Row(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			before := count()
			previewParams, _ := json.Marshal(map[string]any{"collection": table, "itemId": item, "targetRevision": page.ChangeSets[0].RootRevisionID, "scope": "row"})
			previewResponse := restoreProductRequest(t, mux, "history.previewRestore", string(previewParams), schemaListWire, context.Background())
			if previewResponse.Error != nil {
				t.Fatalf("preview: %+v", previewResponse.Error)
			}
			var preview audit.Preview
			if err := json.Unmarshal(previewResponse.Result, &preview); err != nil {
				t.Fatal(err)
			}

			if scenario == "workspace-preview" {
				workspaceParams := strings.TrimSuffix(string(previewParams), "}") + `,"field":null}`
				response := runtime.Dispatcher().DispatchEnvelope(context.Background(), []byte(`{"jsonrpc":"2.0","id":"workspace-preview","method":"history.previewRestore","wire":`+schemaListWire+`,"params":`+workspaceParams+`}`))
				if response.Error != nil {
					t.Fatalf("workspace preview failed: %+v", response.Error)
				}
				var ok bool
				preview, ok = response.Result.(audit.Preview)
				if !ok {
					t.Fatal("workspace preview type")
				}
			}
			current, err := pb.FindRecordById(record.Collection(), item)
			if err != nil {
				t.Fatal(err)
			}
			if !preview.CanApply || preview.Token == "" || count() != before || current.GetString(field) != "second" {
				t.Fatalf("preview changed business state: %#v", preview)
			}
			applyParams, _ := json.Marshal(map[string]any{"collection": table, "itemId": item, "token": preview.Token})
			staleWire := strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":6`, 1)
			stale := restoreProductRequest(t, mux, "history.applyRestore", string(applyParams), staleWire, context.Background())
			if stale.Error == nil || stale.Error.Code != productrpc.CodeInvalidRequest || count() != before {
				t.Fatalf("stale apply reached owner: %+v", stale)
			}
			if scenario == "conflict" || scenario == "expired" {
				expected := "restore_token_expired"
				if scenario == "conflict" {
					expected = "restore_conflict"
					current.Set(field, "third")
					if err := pb.Save(current); err != nil {
						t.Fatal(err)
					}
				} else {
					now = now.Add(24 * time.Hour)
				}
				rejected := restoreProductRequest(t, mux, "history.applyRestore", string(applyParams), schemaListWire, context.Background())
				if rejected.Error == nil || rejected.Error.Data["code"] != expected || count() != before {
					t.Fatalf("invalid token wrote: %+v", rejected.Error)
				}
				return
			}
			if scenario == "cancelled" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				rejected := restoreProductRequest(t, mux, "history.applyRestore", string(applyParams), schemaListWire, ctx)
				if rejected.Error == nil || count() != before {
					t.Fatalf("cancelled apply wrote: %+v", rejected)
				}
			}

			applied := restoreProductRequest(t, mux, "history.applyRestore", string(applyParams), schemaListWire, context.Background())
			if applied.Error != nil {
				t.Fatalf("apply: %+v", applied.Error)
			}
			current, err = pb.FindRecordById(record.Collection(), item)
			if err != nil {
				t.Fatal(err)
			}
			if current.GetString(field) != "first" || count() != before+1 {
				t.Fatal("apply did not produce exactly one canonical write")
			}
			found, err := writecoordinator.HasPocketBaseReceiptIdentity(context.Background(), pb, "11111111-1111-4111-8111-111111111111", "history.restore", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
			if err != nil || !found {
				t.Fatalf("wire operation identity not persisted: %v %v", found, err)
			}
			replayWire := strings.Replace(schemaListWire, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "cccccccc-cccc-4ccc-8ccc-cccccccccccc", 1)
			replay := restoreProductRequest(t, mux, "history.applyRestore", string(applyParams), replayWire, context.Background())
			if replay.Error == nil || replay.Error.Data["code"] != "restore_token_unknown" || count() != before+1 {
				t.Fatalf("consumed token wrote twice: %+v", replay)
			}
		})
	}
}

func TestHistoryRestoreProductRequiresValidatedSubmitIdentity(t *testing.T) {
	owner := &restoreParityOwner{}
	_, err := historyApplyRestoreRegistration(owner).Handler(context.Background(), json.RawMessage(`{"collection":"a","itemId":"b","token":"c"}`))
	if err == nil || len(owner.requests) != 0 {
		t.Fatal("missing submit identity reached owner")
	}
}
