package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
)

func TestPluginDataRealAuthorityPaginationAndGrants(t *testing.T) {
	pb := schemaProductStore(t)
	ctx := context.Background()
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "Plugin synthetic 550", OperationID: "plugin-data-table", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	number := createSchemaProductField(t, pb, table.TableID, v2.LogicalNumber, "Amount", "plugin-data-number").Definition
	date := createSchemaProductField(t, pb, table.TableID, v2.LogicalDate, "Date", "plugin-data-date").Definition
	boolean := createSchemaProductField(t, pb, table.TableID, v2.LogicalBool, "Enabled", "plugin-data-bool").Definition
	hidden := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Secret", "plugin-data-hidden").Definition

	target, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "ungranted target title", OperationID: "plugin-data-target", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	label := createSchemaProductField(t, pb, target.TableID, v2.LogicalText, "ungranted target label", "plugin-data-target-label").Definition
	defaults, _ := v2.RecommendedDefaults(v2.LogicalRelation)
	link := applySchemaProductField(t, pb, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID, Draft: &v2.FieldDraft{DisplayName: "Link", LogicalType: v2.LogicalRelation, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display, Relation: &v2.RelationSpec{TargetTableID: target.TableID, Cardinality: "one", DeletePolicy: "setNull", DisplayField: label.Identity.FieldID}}, RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "Backlink", ReciprocalCardinality: "many", SourceDisplayFieldID: hidden.Identity.FieldID}}, "plugin-data-link").Definition
	targetDescription, err := schemaexecution.Describe(ctx, pb, target.TableID)
	if err != nil {
		t.Fatal(err)
	}
	targetCollection, err := pb.FindCollectionByNameOrId(targetDescription.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	targetRecord := core.NewRecord(targetCollection)
	targetRecord.Set(label.Identity.PhysicalName, "ungranted business value")
	if err = pb.Save(targetRecord); err != nil {
		t.Fatal(err)
	}
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := source.DescribeQueryTable(ctx, pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(descriptor.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	if err := pb.RunInTransaction(func(tx core.App) error {
		for i := 0; i < 550; i++ {
			r := core.NewRecord(collection)
			r.Set(number.Identity.PhysicalName, i/3)
			r.Set(number.Value.Presence.PhysicalName, i != 0)
			r.Set(boolean.Identity.PhysicalName, i%2 == 1)
			r.Set(boolean.Value.Presence.PhysicalName, true)
			r.Set(date.Identity.PhysicalName, fmt.Sprintf("2025-01-%02d 00:00:00.000Z", i%28+1))
			r.Set(date.Value.Presence.PhysicalName, i != 1)
			r.Set(hidden.Identity.PhysicalName, "never-visible")
			r.Set(link.Identity.PhysicalName, targetRecord.Id)
			r.Set(link.Value.Presence.PhysicalName, true)
			if err := tx.Save(r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	const workspaceID = "0f8f4a3b-2c1d-4e5f-8091-a2b3c4d5e6f7"
	store := pluginstore.New(pb, workspaceID)
	oracle, err := os.ReadFile("../../../contracts/v2/plugin-catalog-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Cases []struct {
			Name   string          `json:"name"`
			Result json.RawMessage `json:"result"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(oracle, &document); err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(document.Cases[0].Result, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["projectKey"] = store.ProjectKey()
	snapshot["status"] = "enabled"
	snapshot["revision"] = 1
	manifest := snapshot["manifest"].(map[string]any)
	manifest["compatibility"].(map[string]any)["pluginApi"] = "2.x"
	manifest["permissions"].(map[string]any)["data"] = []any{map[string]any{"collection": table.TableID, "operations": []string{"read", "query"}, "fields": []string{"id", number.Identity.FieldID, date.Identity.FieldID, boolean.Identity.FieldID, link.Identity.FieldID}}}
	raw, _ := json.Marshal(snapshot)
	if _, err = store.SaveInstallation(ctx, raw, nil); err != nil {
		t.Fatal(err)
	}
	input := pluginDataRequest{ProjectKey: store.ProjectKey(), PluginID: snapshot["pluginId"].(string), InstallationRevision: 1, ActiveCollection: table.TableID}
	call := func(operation string, request any, cursor string, deps map[string]pluginComputedRevision) (map[string]any, error) {
		input.Operation = operation
		input.Request, _ = json.Marshal(request)
		input.Cursor = cursor
		input.Dependencies = deps
		return executePluginData(ctx, pb, source, workspaceID, input)
	}
	metadata, err := call("describe", pluginDescribeRequest{Accepts: []string{pluginDataContract}, Collection: table.TableID}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	metadataJSON, _ := json.Marshal(metadata)
	for _, secret := range []string{target.TableID, label.Identity.FieldID, "ungranted target title", "ungranted target label", "ungranted business value"} {
		if strings.Contains(string(metadataJSON), secret) {
			t.Fatalf("target metadata leaked %s", secret)
		}
	}
	catalogResult, err := call("describe", pluginDescribeRequest{Accepts: []string{pluginDataContract}}, "", nil)
	if err != nil || len(catalogResult["tables"].([]any)) != 1 {
		t.Fatalf("authorized catalog=%#v err=%v", catalogResult, err)
	}
	fields := metadata["fields"].([]any)
	if len(fields) != 5 {
		t.Fatalf("authorized fields = %v", fields)
	}
	size := 200
	for _, sortID := range []string{number.Identity.FieldID, date.Identity.FieldID} {
		request := pluginQueryRequest{Contract: "vibetable.plugin-query.v2", Collection: table.TableID, Fields: []string{"id", number.Identity.FieldID, date.Identity.FieldID, boolean.Identity.FieldID, link.Identity.FieldID}, Sorts: []query.SortCondition{{Field: sortID, Direction: query.SortAscending}}, PageSize: &size}
		ids := map[string]bool{}
		captured := []any{}
		aliases := map[string]string{number.Identity.FieldID: "fld_amount", date.Identity.FieldID: "fld_date", boolean.Identity.FieldID: "fld_enabled", link.Identity.FieldID: "fld_link"}
		cursor := ""
		var deps map[string]pluginComputedRevision
		lengths := []int{}
		for {
			page, err := call("query", request, cursor, deps)
			if err != nil {
				t.Fatal(err)
			}
			items := page["items"].([]any)
			if sortID == number.Identity.FieldID {
				copyItems := []any{}
				for _, rawRow := range items {
					row := rawRow.(map[string]any)
					copyItems = append(copyItems, map[string]any{"id": fmt.Sprintf("record-%03d", len(ids)+len(copyItems)+1), "fld_amount": row[number.Identity.FieldID], "fld_date": row[date.Identity.FieldID], "fld_enabled": row[boolean.Identity.FieldID], "fld_link": "target-record"})
				}
				next := any(nil)
				if page["nextCursor"].(*string) != nil {
					next = fmt.Sprintf("fixture-page-%d", len(captured)+2)
				}
				captured = append(captured, map[string]any{"contract": "vibetable.plugin-query-page.v2", "items": copyItems, "nextCursor": next, "filteredRows": 550, "totalRows": 550, "schemaRevision": "schema-fixture", "dataRevision": page["dataRevision"], "complete": page["complete"]})
			}
			lengths = append(lengths, len(items))
			for _, item := range items {
				row := item.(map[string]any)
				id := row["id"].(string)
				if ids[id] {
					t.Fatalf("duplicate %s", id)
				}
				ids[id] = true
				if row[link.Identity.FieldID] != targetRecord.Id {
					t.Fatalf("relation original ID changed=%#v", row)
				}
				if _, ok := row[hidden.Identity.FieldID]; ok {
					t.Fatal("hidden field leaked")
				}
			}
			if page["filteredRows"] != int64(550) && page["filteredRows"] != 550 {
				t.Fatalf("count = %#v", page)
			}
			cursor = ""
			if next := page["nextCursor"].(*string); next != nil {
				cursor = *next
			}
			deps = page["dependencies"].(map[string]pluginComputedRevision)
			if cursor == "" {
				break
			}
		}
		if target := os.Getenv("VIBETABLE_PLUGIN_DATA_CAPTURE"); target != "" && sortID == number.Identity.FieldID {
			normalized := []any{}
			for _, rawField := range metadata["fields"].([]any) {
				f := rawField.(map[string]any)
				if alias, ok := aliases[f["fieldId"].(string)]; ok {
					f["fieldId"] = alias
				}
				normalized = append(normalized, f)
			}
			description := map[string]any{"contract": pluginDataContract, "collection": "articles", "displayName": "Plugin synthetic 550", "schemaRevision": "schema-fixture", "fields": normalized}
			fixture := map[string]any{"producer": "sidecar/internal/app/plugin_data_routes_test.go TestPluginDataRealAuthorityPaginationAndGrants", "normalization": "table, field, record IDs and cursor handles replaced by fixture identities; schema revision replaced by schema-fixture; values and capability metadata unchanged", "description": description, "request": map[string]any{"contract": "vibetable.plugin-query.v2", "collection": "articles", "fields": []string{"id", "fld_amount", "fld_date", "fld_enabled", "fld_link"}, "sorts": []any{map[string]any{"field": "fld_amount", "direction": "asc"}}, "pageSize": 200}, "pages": captured, "rejectCases": []any{
				map[string]any{"name": "hidden projection", "patch": map[string]any{"fields": []string{"fld_hidden"}}, "code": "plugin_read_denied"},
				map[string]any{"name": "hidden filter", "patch": map[string]any{"filters": []any{map[string]any{"field": "fld_hidden", "operator": "eq", "value": "never-visible"}}}, "code": "plugin_read_denied"},
				map[string]any{"name": "hidden sort", "patch": map[string]any{"sorts": []any{map[string]any{"field": "fld_hidden"}}}, "code": "plugin_read_denied"},
				map[string]any{"name": "unauthorized table ID selection", "patch": map[string]any{"collection": "unavailable", "ids": []string{"record-001"}}, "code": "plugin_read_denied"},
				map[string]any{"name": "relation target path", "patch": map[string]any{"fields": []string{"fld_amount.label"}}, "code": "plugin_read_denied"},
				map[string]any{"name": "raw SQL", "patch": map[string]any{"sql": "select *"}, "code": "plugin_capability_invalid"},
				map[string]any{"name": "page size exceeds budget", "patch": map[string]any{"pageSize": 201}, "code": "plugin_query_limit"},
				map[string]any{"name": "unsupported operator", "patch": map[string]any{"filters": []any{map[string]any{"field": "fld_amount", "operator": "regex", "value": ".*"}}}, "code": "query.operator.unsupported"},
			}}
			encoded, _ := json.MarshalIndent(fixture, "", "  ")
			if err := os.WriteFile(target, append(encoded, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if len(ids) != 550 || fmt.Sprint(lengths) != "[200 200 150]" {
			t.Fatalf("rows=%d pages=%v", len(ids), lengths)
		}
	}

	// The narrowed plugin descriptor must strip every relation label source
	// (raw-ID projection is asserted by the pagination loop above, target
	// privacy by the metadata secret scan near the top).
	scoped := &pluginQuerySource{
		source: source,
		grant: pluginReadGrant{
			Collection: table.TableID,
			Operations: []string{"read", "query"},
			Fields:     []string{"id", number.Identity.FieldID, date.Identity.FieldID, boolean.Identity.FieldID, link.Identity.FieldID},
		},
		request: pluginQueryRequest{
			Contract:   "vibetable.plugin-query.v2",
			Collection: table.TableID,
			Fields:     []string{"id", link.Identity.FieldID},
		},
	}
	narrowed, _, err := scoped.DescribeSelectionTable(ctx, pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	relation := narrowed.Fields[link.Identity.PhysicalName].Relation
	if relation == nil || relation.DisplayField != "" || relation.PrimaryDisplayField != "" || len(relation.Fields) != 0 || len(relation.PresenceFields) != 0 {
		t.Fatalf("plugin relation descriptor kept ungranted label sources: %#v", relation)
	}

	fixtureRaw, err := os.ReadFile("../../../tests/contract/fixtures/plugin-capabilities-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Request     map[string]any `json:"request"`
		RejectCases []struct {
			Name  string         `json:"name"`
			Patch map[string]any `json:"patch"`
			Code  string         `json:"code"`
		} `json:"rejectCases"`
	}
	if err = json.Unmarshal(fixtureRaw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, item := range fixture.RejectCases {
		request := map[string]any{}
		for k, v := range fixture.Request {
			request[k] = v
		}
		for k, v := range item.Patch {
			request[k] = v
		}
		encoded, _ := json.Marshal(request)
		wire := strings.NewReplacer("articles", table.TableID, "fld_amount", number.Identity.FieldID, "fld_date", date.Identity.FieldID, "fld_enabled", boolean.Identity.FieldID, "fld_hidden", hidden.Identity.FieldID, "fld_link", link.Identity.FieldID).Replace(string(encoded))
		input.Operation = "query"
		input.Request = json.RawMessage(wire)
		input.Cursor = ""
		input.Dependencies = nil
		_, err := executePluginData(ctx, pb, source, workspaceID, input)
		var product *query.ProductError
		if !errors.As(err, &product) || product.Code != item.Code {
			t.Fatalf("conformance %s: %v want %s", item.Name, err, item.Code)
		}
	}
	for _, request := range []pluginQueryRequest{
		{Contract: "vibetable.plugin-query.v2", Collection: table.TableID, Fields: []string{hidden.Identity.FieldID}},
		{Contract: "vibetable.plugin-query.v2", Collection: table.TableID, Fields: []string{"id"}, Filters: []query.FilterExpression{{Field: hidden.Identity.FieldID, Operator: query.OperatorEqual, Value: "never-visible"}}},
		{Contract: "vibetable.plugin-query.v2", Collection: table.TableID, Fields: []string{"id"}, Sorts: []query.SortCondition{{Field: hidden.Identity.FieldID}}},
	} {
		if _, err := call("query", request, "", nil); err == nil {
			t.Fatalf("accepted unauthorized request %#v", request)
		}
	}

	if os.Getenv("VIBETABLE_PLUGIN_HOST_CONTRACT") == "1" {
		const testSecret = "1111111111111111111111111111111111111111111111111111111111111111"
		secret, err := auth.Parse(testSecret)
		if err != nil {
			t.Fatal(err)
		}
		r := router.NewRouter(func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
			return &core.RequestEvent{App: pb, Event: router.Event{Response: w, Request: req}}, nil
		})
		bindVibetableSessionAuth(r, secret)
		registerPluginDataRoutes(r, pb, source, workspaceID, 7)
		registerPluginStoreRoutes(r, store, nil)
		mux, err := r.BuildMux()
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(mux)
		defer server.Close()
		response, err := http.Post(server.URL+pluginDataPath, "application/json", bytes.NewBufferString(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatal("unauthenticated data admitted")
		}
		settings, _ := json.Marshal(map[string]any{"url": server.URL, "secret": testSecret, "projectKey": store.ProjectKey(), "pluginId": input.PluginID, "collection": table.TableID, "number": number.Identity.FieldID, "date": date.Identity.FieldID, "hidden": hidden.Identity.FieldID})
		command := exec.Command("uv", "run", "--frozen", "--no-sync", "python", "-m", "tests.contract.plugin_query_host_driver")
		command.Dir = "../../.."
		command.Stdin = bytes.NewReader(settings)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("real Node/BFF/Go driver: %v\n%s", err, output)
		}
		t.Log(string(output))
	}

	selected, err := pb.FindRecordsByFilter(collection.Name, "", "+id", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{selected[0].Id, selected[1].Id}
	byID, err := call("query", pluginQueryRequest{Contract: "vibetable.plugin-query.v2", Collection: table.TableID, Fields: []string{"id", boolean.Identity.FieldID}, IDs: &ids}, "", nil)
	if err != nil || len(byID["items"].([]any)) != 2 {
		t.Fatalf("ID read=%#v err=%v", byID, err)
	}
	empty, err := call("query", pluginQueryRequest{Contract: "vibetable.plugin-query.v2", Collection: table.TableID, Fields: []string{"id"}, Filters: []query.FilterExpression{{Field: number.Identity.FieldID, Operator: query.OperatorGreater, Value: 1000}}}, "", nil)
	if err != nil || len(empty["items"].([]any)) != 0 || empty["filteredRows"] != int64(0) || empty["totalRows"] != int64(550) {
		t.Fatalf("empty=%#v err=%v", empty, err)
	}
	base := pluginQueryRequest{Contract: "vibetable.plugin-query.v2", Collection: table.TableID, Fields: []string{"id"}, PageSize: &size}
	first, err := call("query", base, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	metadataRecord, err := pb.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", map[string]any{"table": table.TableID})
	if err != nil {
		t.Fatal(err)
	}
	metadataRecord.Set("data_revision", metadataRecord.GetInt("data_revision")+1)
	if err = pb.Save(metadataRecord); err != nil {
		t.Fatal(err)
	}
	if _, err = call("query", base, *first["nextCursor"].(*string), first["dependencies"].(map[string]pluginComputedRevision)); err == nil {
		t.Fatal("accepted data-stale cursor")
	}
	first, err = call("query", base, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	metadataRecord.Set("schema_revision", metadataRecord.GetInt("schema_revision")+1)
	if err = pb.Save(metadataRecord); err != nil {
		t.Fatal(err)
	}
	if _, err = call("query", base, *first["nextCursor"].(*string), first["dependencies"].(map[string]pluginComputedRevision)); err == nil {
		t.Fatal("accepted schema-stale cursor")
	}
	refreshed, err := call("describe", pluginDescribeRequest{Accepts: []string{pluginDataContract}, Collection: table.TableID}, "", nil)
	if err != nil || refreshed["schemaRevision"] == metadata["schemaRevision"] {
		t.Fatal("metadata revision did not refresh")
	}
	snapshot["revision"] = 2
	raw, _ = json.Marshal(snapshot)
	expectedInstall := int64(1)
	if _, err = store.SaveInstallation(ctx, raw, &expectedInstall); err != nil {
		t.Fatal(err)
	}
	if _, err = call("query", base, "", nil); err == nil {
		t.Fatal("accepted installation revision change")
	}
	input.InstallationRevision = 2
	revision := int64(2)
	snapshot["revision"] = 3
	snapshot["status"] = "disabled"
	raw, _ = json.Marshal(snapshot)
	if _, err = store.SaveInstallation(ctx, raw, &revision); err != nil {
		t.Fatal(err)
	}
	if _, err = call("query", base, "", nil); err == nil {
		t.Fatal("accepted revoked installation")
	}
	input.InstallationRevision = 2
	if _, err := call("describe", pluginDescribeRequest{Accepts: []string{pluginDataContract}}, "", nil); err == nil {
		t.Fatal("accepted stale installation")
	}
}

// Keep real enqueueing without a runner racing the manually staged formula state.
type pluginComputedBackfillQueue struct{ *jobs.Service }

func (pluginComputedBackfillQueue) Start(string) bool { return false }

func TestPluginDataComputedFreshnessAndDependencyCursor(t *testing.T) {
	pb := schemaProductStore(t)
	ctx := context.Background()
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	create := func(name string) v2.TableCreateReceipt {
		r, e := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: name, OperationID: "plugin-computed-" + name, Actor: v2.Actor{ID: "local-user", Kind: "user"}})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	leaf := create("leaf")
	root := create("root")
	amount := createSchemaProductField(t, pb, leaf.TableID, v2.LogicalNumber, "Amount", "plugin-computed-amount").Definition
	defaults, _ := v2.RecommendedDefaults(v2.LogicalFormula)
	catalog := fieldchange.NewCatalog(pb)
	revisions, err := catalog.Revisions(ctx, root.TableID)
	if err != nil {
		t.Fatal(err)
	}
	backfill := jobs.New(pb, nil)
	t.Cleanup(backfill.Shutdown)
	plans := fieldchange.NewPocketBasePlanStore(pb)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	intent := v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: root.TableID, ExpectedSchemaRev: revisions.Schema, Actor: actor, Draft: &v2.FieldDraft{DisplayName: "Related total", LogicalType: v2.LogicalFormula, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display, Formula: &v2.FormulaDraftSpec{Language: "cel-v2", Source: fmt.Sprintf(`SUM(PROJECT(TABLE("%s"), CurrentValue.%s))`, leaf.TableID, amount.Identity.PhysicalName)}}}
	plan, err := fieldchange.NewPlanner(catalog, catalog, plans, v2.NewIdentityAllocator(nil)).Plan(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := fieldchange.NewExecutor(pb, plans, fieldchange.WithFormulaBackfillScheduler(pluginComputedBackfillQueue{backfill})).Apply(ctx, v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "plugin-computed-apply", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	formula := *receipt.Definition
	runtime, err := pb.FindFirstRecordByFilter("vibetable_formulas", "table_id={:table}", map[string]any{"table": root.TableID})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Set("status", "ready")
	if err = pb.Save(runtime); err != nil {
		t.Fatal(err)
	}
	described, err := schemaexecution.Describe(ctx, pb, root.TableID)
	if err != nil {
		t.Fatal(err)
	}
	expectation, err := relatedcomputation.ExpectationFor(ctx, pb, root.TableID, described.Snapshot.Fields, formula.Identity.FieldID, 1)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(described.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r := core.NewRecord(collection)
		r.Set(relatedcomputation.RowRevisionField, 1)
		r.Set(formula.Identity.PhysicalName, relatedcomputation.Ready(0, relatedcomputation.CellVersion{DefinitionVersion: expectation.DefinitionVersion, SourceDataRevision: 1, DependencyWatermark: expectation.DependencyWatermark}))
		if err = pb.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	const workspaceID = "0f8f4a3b-2c1d-4e5f-8091-a2b3c4d5e6f7"
	store := pluginstore.New(pb, workspaceID)
	oracle, err := os.ReadFile("../../../contracts/v2/plugin-catalog-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Cases []struct {
			Result json.RawMessage `json:"result"`
		} `json:"cases"`
	}
	_ = json.Unmarshal(oracle, &document)
	var snapshot map[string]any
	_ = json.Unmarshal(document.Cases[0].Result, &snapshot)
	snapshot["projectKey"] = store.ProjectKey()
	snapshot["status"] = "enabled"
	snapshot["revision"] = 1
	manifest := snapshot["manifest"].(map[string]any)
	manifest["compatibility"].(map[string]any)["pluginApi"] = "2.x"
	manifest["permissions"].(map[string]any)["data"] = []any{map[string]any{"collection": root.TableID, "operations": []string{"read", "query"}, "fields": []string{"id", formula.Identity.FieldID}}}
	raw, _ := json.Marshal(snapshot)
	if _, err = store.SaveInstallation(ctx, raw, nil); err != nil {
		t.Fatal(err)
	}
	source, _ := queryschema.New(pb.DataDir())
	size := 1
	request := pluginQueryRequest{Contract: "vibetable.plugin-query.v2", Collection: root.TableID, Fields: []string{"id", formula.Identity.FieldID}, PageSize: &size}
	payload, _ := json.Marshal(request)
	input := pluginDataRequest{Operation: "query", ProjectKey: store.ProjectKey(), PluginID: snapshot["pluginId"].(string), InstallationRevision: 1, Request: payload}
	page, err := executePluginData(ctx, pb, source, workspaceID, input)
	if err != nil {
		t.Fatal(err)
	}
	cell := page["items"].([]any)[0].(map[string]any)[formula.Identity.FieldID].(map[string]any)
	if cell["fresh"] != true || cell["value"] != float64(0) {
		t.Fatalf("fresh JSON = %#v", cell)
	}
	input.Cursor = *page["nextCursor"].(*string)
	input.Dependencies = page["dependencies"].(map[string]pluginComputedRevision)
	leafMetadata, err := pb.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", map[string]any{"table": leaf.TableID})
	if err != nil {
		t.Fatal(err)
	}
	if err = relatedcomputation.AdvanceInputRevisions(ctx, pb, leaf.TableID, []v2.FieldDefinition{*amount}, map[string]any{amount.Identity.PhysicalName: 0}, map[string]any{amount.Identity.PhysicalName: 1}, "update", int64(leafMetadata.GetInt("data_revision")+1)); err != nil {
		t.Fatal(err)
	}
	leafMetadata.Set("data_revision", leafMetadata.GetInt("data_revision")+1)
	if err = pb.Save(leafMetadata); err != nil {
		t.Fatal(err)
	}
	if _, err = executePluginData(ctx, pb, source, workspaceID, input); err == nil {
		t.Fatal("accepted dependency-stale continuation")
	}
	input.Cursor = ""
	input.Dependencies = nil
	page, err = executePluginData(ctx, pb, source, workspaceID, input)
	if err != nil {
		t.Fatal(err)
	}
	cell = page["items"].([]any)[0].(map[string]any)[formula.Identity.FieldID].(map[string]any)
	if cell["fresh"] != false || cell["value"] != nil || page["complete"] != false {
		t.Fatalf("stale envelope leaked = %#v", page)
	}
	if _, ok := cell["diagnostic"]; ok {
		t.Fatal("internal computed diagnostic leaked")
	}
}
