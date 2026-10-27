package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/tools/router"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

// Use the actual route encoder, including its json/v2 options and response envelope.
func modelCatalogWire(t *testing.T, result any) []byte {
	t.Helper()
	recorder := httptest.NewRecorder()
	event := router.Event{Request: httptest.NewRequest("POST", pluginSurfaceAPIPath, nil), Response: recorder}
	if err := event.JSON(200, result); err != nil {
		t.Fatal(err)
	}
	return recorder.Body.Bytes()
}

func TestPluginModelCatalogBoundsLongUnicodeSummaryWire(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	ctx := context.Background()
	table, err := domain.CreateTable(ctx, v2.TableCreateIntent{DisplayName: "长名称目录", OperationID: "unicode-catalog", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{}
	for i := 0; i < 34; i++ {
		field := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, strings.Repeat("😀", 9000)+fmt.Sprint(i), fmt.Sprintf("unicode-catalog-%d", i))
		wanted[field.FieldID] = true
	}
	schema, err := schemaapi.New(pb).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"tableId": table.TableID, "schemaRevision": schema.Snapshot.SchemaRevision}
	pages := 0
	for {
		result, err := modelCall(t, pb, workspace, input, domain, "model.catalog", request)
		if err != nil {
			t.Fatal(err)
		}
		wire := modelCatalogWire(t, result)
		if len(wire) > 1024*1024 {
			t.Fatalf("long Unicode summary page exceeds actual HTTP wire budget: %d", len(wire))
		}
		var page struct {
			Fields []struct {
				ID string `json:"fieldId"`
			} `json:"fields"`
			Next *int `json:"nextOffset"`
		}
		if err := json.Unmarshal(wire, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Fields) == 0 || len(page.Fields) > 32 {
			t.Fatal("empty or oversized field page")
		}
		for _, field := range page.Fields {
			if !wanted[field.ID] {
				t.Fatal("duplicate or foreign field")
			}
			delete(wanted, field.ID)
		}
		pages++
		if page.Next == nil {
			break
		}
		if previous, ok := request["offset"].(int); ok && *page.Next <= previous {
			t.Fatal("field offset did not advance")
		}
		request["offset"] = *page.Next
	}
	if len(wanted) != 0 || pages < 2 {
		t.Fatal("long Unicode summary candidates truncated")
	}
}

func TestPluginModelCatalogCountsWholeTablePageAndRejectsOversizedItem(t *testing.T) {
	item := map[string]any{"tableId": "tbl_budget", "displayName": "<>&\u2028😀", "schemaRevision": "schema_0001"}
	page := map[string]any{"model": "", "tables": []any{item}, "nextAfterTableId": nil}
	base := len(modelCatalogWire(t, page))
	page["model"] = strings.Repeat("x", maxMutationRequestBytes-base)
	if len(modelCatalogWire(t, page)) != maxMutationRequestBytes {
		t.Fatal("wire boundary fixture is not exact")
	}
	if fits, err := modelCatalogFits(page); !fits || err != nil {
		t.Fatalf("actual route-size boundary rejected: %v", err)
	}
	if added, err := appendModelCatalogItem(page, "tables", "nextAfterTableId", item, "tbl_budget"); added || err != nil {
		t.Fatalf("model/envelope excluded from table budget: added=%v err=%v", added, err)
	}
	if len(page["tables"].([]any)) != 1 || page["nextAfterTableId"] != nil {
		t.Fatal("rejected table changed the previous page")
	}
	page["tables"] = []any{}
	page["model"] = strings.Repeat("x", maxMutationRequestBytes-base+1)
	if _, err := appendModelCatalogItem(page, "tables", "nextAfterTableId", item, nil); surfaceErrorCode(t, err) != "plugin_message_too_large" {
		t.Fatalf("single unrepresentable summary was not explicitly rejected: %v", err)
	}
}

func TestPluginModelCatalogPagesSummariesAndSelectedFields(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	ctx := context.Background()
	tableIDs := map[string]bool{}
	selected := ""
	for i := 0; i < 34; i++ {
		table, err := domain.CreateTable(ctx, v2.TableCreateIntent{DisplayName: fmt.Sprintf("目录表 %02d", i), OperationID: fmt.Sprintf("catalog-%d", i), Actor: v2.Actor{ID: "local-user", Kind: "user"}})
		if err != nil {
			t.Fatal(err)
		}
		tableIDs[table.TableID] = true
		if i == 0 {
			selected = table.TableID
		}
		if i == 1 {
			// Each field fits the ordinary request budget; together these unrelated schemas do not.
			for j := 0; j < 8; j++ {
				defaults, _ := v2.RecommendedDefaults(v2.LogicalSelect)
				applySchemaProductField(t, pb, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID, Draft: &v2.FieldDraft{DisplayName: fmt.Sprintf("大选项%d", j), LogicalType: v2.LogicalSelect, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display, Select: &v2.SelectSpec{Options: []v2.SelectOption{{OptionID: fmt.Sprintf("opt_catalog_%08d", j), Label: strings.Repeat("x", 150000), State: v2.OptionActive}}}}}, fmt.Sprintf("large-catalog-%d", j))
			}
		}
	}
	wanted := map[string]bool{}
	for i := 0; i < 34; i++ {
		field := createSchemaProductField(t, pb, selected, v2.LogicalText, fmt.Sprintf("可选字段 %02d", i), fmt.Sprintf("catalog-field-%d", i))
		wanted[field.FieldID] = true
	}
	all, err := schemaapi.New(pb).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(all)
	if len(raw) <= 1024*1024 {
		t.Fatalf("fixture did not exceed old catalog budget: %d", len(raw))
	}
	request := map[string]any{}
	seen := map[string]bool{}
	revision := ""
	for {
		result, err := modelCall(t, pb, workspace, input, domain, "model.catalog", request)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ = json.Marshal(result)
		if len(raw) > 1024*1024 {
			t.Fatalf("catalog response exceeds budget: %d", len(raw))
		}
		var page struct {
			Tables []struct {
				TableID        string          `json:"tableId"`
				SchemaRevision string          `json:"schemaRevision"`
				Fields         json.RawMessage `json:"fields"`
			} `json:"tables"`
			Next *string `json:"nextAfterTableId"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Tables) > 32 {
			t.Fatalf("unbounded table page: %d", len(page.Tables))
		}
		for _, table := range page.Tables {
			if seen[table.TableID] || len(table.Fields) != 0 {
				t.Fatal("duplicate table or full schema leaked")
			}
			seen[table.TableID] = true
			if table.TableID == selected {
				revision = table.SchemaRevision
			}
		}
		if page.Next == nil {
			break
		}
		request["afterTableId"] = *page.Next
	}
	if len(seen) != len(tableIDs) || revision == "" {
		t.Fatalf("table candidates truncated: %d", len(seen))
	}
	request = map[string]any{"tableId": selected, "schemaRevision": revision}
	for {
		result, err := modelCall(t, pb, workspace, input, domain, "model.catalog", request)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ = json.Marshal(result)
		var page struct {
			Fields []struct {
				ID string `json:"fieldId"`
			} `json:"fields"`
			Next *int `json:"nextOffset"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		if len(raw) > 1024*1024 || len(page.Fields) > 32 {
			t.Fatal("unbounded field page")
		}
		for _, field := range page.Fields {
			if !wanted[field.ID] {
				t.Fatal("unexpected/duplicate field")
			}
			delete(wanted, field.ID)
		}
		if page.Next == nil {
			break
		}
		request["offset"] = *page.Next
	}
	if len(wanted) != 0 {
		t.Fatal("selected field candidates truncated")
	}
	if _, err := modelCall(t, pb, "22222222-2222-4222-8222-222222222222", input, domain, "model.catalog", request); err == nil {
		t.Fatal("foreign workspace catalog admitted")
	}
	createSchemaProductField(t, pb, selected, v2.LogicalText, "结构变化", "catalog-stale")
	if _, err := modelCall(t, pb, workspace, input, domain, "model.catalog", request); surfaceErrorCode(t, err) != "plugin_plan_stale" {
		t.Fatalf("stale schema: %v", err)
	}
	input.InstallationRevision++
	if _, err := modelCall(t, pb, workspace, input, domain, "model.catalog", map[string]any{}); surfaceErrorCode(t, err) != "plugin_cursor_stale" {
		t.Fatalf("stale installation: %v", err)
	}
}
