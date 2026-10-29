package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestPluginDatasetThreeTablesExactSnapshotBudgetsAndGrants(t *testing.T) {
	pb := schemaProductStore(t)
	ctx := context.Background()
	lifecycle, _ := schemacore.NewTableLifecycle(pb)
	request := pluginDatasetRequest{Contract: pluginDatasetContract}
	grants := []any{}
	var hidden v2.FieldDefinition
	jsonFieldID := ""
	ordinaryJSON := []map[string]any{{"fresh": true, "value": "x", "other": 42}, {"fresh": true}}
	for i := 0; i < 3; i++ {
		table, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: fmt.Sprintf("Synthetic dataset %d", i), OperationID: fmt.Sprintf("dataset-%d", i), Actor: v2.Actor{ID: "local-user", Kind: "user"}})
		if err != nil {
			t.Fatal(err)
		}
		amount := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Amount", fmt.Sprintf("dataset-amount-%d", i)).Definition
		group := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Group", fmt.Sprintf("dataset-group-%d", i)).Definition
		hidden = *createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Hidden", fmt.Sprintf("dataset-hidden-%d", i)).Definition
		var payload *v2.FieldDefinition
		if i == 0 {
			payload = createSchemaProductField(t, pb, table.TableID, v2.LogicalJSON, "Payload", "dataset-ordinary-json").Definition
			jsonFieldID = payload.Identity.FieldID
		}
		description, err := schemaexecution.Describe(ctx, pb, table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		collection, err := pb.FindCollectionByNameOrId(description.PhysicalName)
		if err != nil {
			t.Fatal(err)
		}
		if err = pb.RunInTransaction(func(tx core.App) error {
			for j := 0; j < 205; j++ {
				r := core.NewRecord(collection)
				r.Set(amount.Identity.PhysicalName, "0.01")
				r.Set(amount.Value.Presence.PhysicalName, true)
				r.Set(group.Identity.PhysicalName, fmt.Sprintf("SYNTHETIC-%d", j%2))
				r.Set(group.Value.Presence.PhysicalName, true)
				if j == 0 {
					r.Set(amount.Identity.PhysicalName, "9007199254740993.01")
				}
				if payload != nil && j < len(ordinaryJSON) {
					r.Set(payload.Identity.PhysicalName, ordinaryJSON[j])
					r.Set(payload.Value.Presence.PhysicalName, true)
				}
				if err := tx.Save(r); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// Oversized ungranted columns must never enter dataset/digest materialization.
		if _, err = pb.DB().NewQuery("UPDATE " + pb.DB().QuoteSimpleTableName(description.PhysicalName) + " SET " + pb.DB().QuoteSimpleColumnName(hidden.Identity.PhysicalName) + "={:large} WHERE id=(SELECT id FROM " + pb.DB().QuoteSimpleTableName(description.PhysicalName) + " LIMIT 1)").Bind(dbx.Params{"large": strings.Repeat("x", 1<<20)}).Execute(); err != nil {
			t.Fatal(err)
		}
		fields := []string{"id", amount.Identity.FieldID, group.Identity.FieldID}
		if payload != nil {
			fields = append(fields, payload.Identity.FieldID)
		}
		grants = append(grants, map[string]any{"collection": table.TableID, "operations": []string{"read", "query"}, "fields": fields})
		request.Sources = append(request.Sources, pluginDatasetSource{Query: pluginQueryRequest{Contract: "vibetable.plugin-query.v2", Collection: table.TableID, Fields: fields}, GroupBy: []string{group.Identity.FieldID}, Sums: []pluginDatasetSum{{Field: amount.Identity.FieldID, Format: exactdecimal.Format{Storage: "decimalText", Scale: 2, Digits: 38}}}})
	}
	const workspace = "0f8f4a3b-2c1d-4e5f-8091-a2b3c4d5e6f7"
	store := pluginstore.New(pb, workspace)
	oracle, err := os.ReadFile("../../../contracts/v2/plugin-catalog-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Cases []struct {
			Result json.RawMessage `json:"result"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(oracle, &document); err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err = json.Unmarshal(document.Cases[0].Result, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["projectKey"], snapshot["status"], snapshot["revision"] = store.ProjectKey(), "enabled", 1
	manifest := snapshot["manifest"].(map[string]any)
	manifest["$schema"] = "vibetable.plugin-manifest.v2"
	manifest["actions"] = []any{map[string]any{"actionId": "open", "risk": "read", "mode": "local", "invocation": "manual", "presentation": "application", "entry": "ui/index.html"}}
	manifest["compatibility"].(map[string]any)["pluginApi"] = "3.x"
	manifest["permissions"].(map[string]any)["data"] = grants
	raw, _ := json.Marshal(snapshot)
	if _, err = store.SaveInstallation(ctx, raw, nil); err != nil {
		t.Fatal(err)
	}
	source, _ := queryschema.New(pb.DataDir())
	input := pluginDataRequest{InstallationID: installationRecordID(t, store, snapshot["pluginId"].(string)), PackageHash: snapshot["packageHash"].(string), ProjectKey: store.ProjectKey(), PluginID: snapshot["pluginId"].(string), InstallationRevision: 1}
	call := func(operation string, r pluginDatasetRequest) (map[string]any, error) {
		input.Operation = operation
		input.Request, _ = json.Marshal(r)
		return executePluginData(ctx, pb, source, workspace, input)
	}
	opened, err := call("dataset.open", request)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range opened["sources"].([]any) {
		output := value.(map[string]any)
		if len(output["computedFields"].([]string)) != 0 {
			t.Fatal("ordinary fields were classified as computed")
		}
		if len(output["items"].([]map[string]any)) != 205 {
			t.Fatalf("source did not span >one page: %#v", output)
		}
		summary := output["summary"].(map[string]any)
		sum := summary["sums"].([]any)[0].(map[string]any)
		if sum["value"] != "9007199254740995.05" || sum["complete"] != true || len(summary["groups"].([]any)) != 2 {
			t.Fatalf("inexact dataset summary: %#v", summary)
		}
	}
	jsonRows := opened["sources"].([]any)[0].(map[string]any)["items"].([]map[string]any)
	seenJSON := map[string]bool{}
	for _, row := range jsonRows {
		if value := row[jsonFieldID]; value != nil {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			seenJSON[string(raw)] = true
		}
	}
	for _, value := range ordinaryJSON {
		raw, _ := json.Marshal(value)
		if !seenJSON[string(raw)] {
			t.Fatalf("ordinary JSON changed during dataset projection: %#v", seenJSON)
		}
	}
	if len(seenJSON) != len(ordinaryJSON) {
		t.Fatalf("ordinary JSON projection: %#v", seenJSON)
	}
	encoded, _ := json.Marshal(opened)
	if len(encoded) > pluginDatasetBytes || strings.Contains(string(encoded), strings.Repeat("x", 128)) {
		t.Fatal("unbounded or hidden result")
	}
	validation := request
	validation.Versions = opened["versions"].(map[string]pluginDatasetVersion)
	if _, err = call("dataset.validate", validation); err != nil {
		t.Fatal(err)
	}
	// Every table participates in the version vector, including rows beyond page one.
	metadata, err := pb.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": request.Sources[2].Query.Collection})
	if err != nil {
		t.Fatal(err)
	}
	metadata.Set("data_revision", metadata.GetInt("data_revision")+1)
	if err = pb.Save(metadata); err != nil {
		t.Fatal(err)
	}
	assertCode := func(code string, err error) {
		t.Helper()
		domain, ok := err.(*query.ProductError)
		if !ok || domain.Code != code {
			t.Fatalf("want %s, got %v", code, err)
		}
	}
	_, err = call("dataset.validate", validation)
	assertCode("plugin_dataset_stale", err)
	// Concurrent synthetic CRUD changes all three months atomically. A reader
	// may observe either epoch, but may never combine epochs across source pages.
	var writers sync.WaitGroup
	writerErrors := make(chan error, 1)
	writers.Add(1)
	go func() {
		defer writers.Done()
		for epoch := 1; epoch <= 6; epoch++ {
			err := pb.RunInTransaction(func(tx core.App) error {
				for _, item := range request.Sources {
					table, err := schemaexecution.Describe(ctx, tx, item.Query.Collection)
					if err != nil {
						return err
					}
					amount, _ := table.Field(item.Query.Fields[1])
					group, _ := table.Field(item.Query.Fields[2])
					quoted := tx.DB().QuoteSimpleTableName(table.PhysicalName)
					if _, err := tx.DB().NewQuery("DELETE FROM " + quoted + " WHERE id=(SELECT id FROM " + quoted + " WHERE " + tx.DB().QuoteSimpleColumnName(amount.Identity.PhysicalName) + "='0.01' LIMIT 1)").Execute(); err != nil {
						return err
					}
					collection, err := tx.FindCollectionByNameOrId(table.PhysicalName)
					if err != nil {
						return err
					}
					row := core.NewRecord(collection)
					row.Set(amount.Identity.PhysicalName, "0.01")
					row.Set(amount.Value.Presence.PhysicalName, true)
					row.Set(group.Identity.PhysicalName, "epoch")
					row.Set(group.Value.Presence.PhysicalName, true)
					if err := tx.Save(row); err != nil {
						return err
					}
					if _, err := tx.DB().NewQuery("UPDATE " + quoted + " SET " + tx.DB().QuoteSimpleColumnName(group.Identity.PhysicalName) + "={:epoch}").Bind(dbx.Params{"epoch": fmt.Sprintf("epoch-%d", epoch)}).Execute(); err != nil {
						return err
					}
					metadata, err := tx.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": item.Query.Collection})
					if err != nil {
						return err
					}
					metadata.Set("data_revision", metadata.GetInt("data_revision")+1)
					if err := tx.Save(metadata); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				writerErrors <- err
				return
			}
		}
	}()
	defer writers.Wait()
	for iteration := 0; iteration < 6; iteration++ {
		current, err := call("dataset.open", request)
		if err != nil {
			t.Fatal(err)
		}
		var observed any
		for _, item := range current["sources"].([]any) {
			out := item.(map[string]any)
			if len(out["items"].([]map[string]any)) != 205 {
				t.Fatal("CRUD snapshot mixed row counts")
			}
			groups := out["summary"].(map[string]any)["groups"].([]any)
			// The initial pre-writer snapshot has the same two groups on every source.
			key := groups[0].(map[string]any)["key"].([]any)[0].(map[string]any)["value"]
			if !strings.HasPrefix(fmt.Sprint(key), "epoch-") {
				key = "initial"
			}
			if observed == nil {
				observed = key
			}
			if observed != key {
				t.Fatalf("mixed multi-table epoch: %v != %v", observed, key)
			}
			if strings.HasPrefix(fmt.Sprint(key), "epoch-") && len(groups) != 1 {
				t.Fatal("mixed intra-source months")
			}
		}
	}
	writers.Wait()
	close(writerErrors)
	for err := range writerErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	description, _ := schemaexecution.Describe(ctx, pb, request.Sources[0].Query.Collection)
	field, _ := description.Field(request.Sources[0].Query.Fields[1])
	// Canonical bindings survive a user-visible rename; an open version still expires.
	beforeRename, err := call("dataset.open", request)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fieldchange.NewCatalog(pb)
	plans := fieldchange.NewPocketBasePlanStore(pb)
	draft := v2.FieldDraft{DisplayName: "Synthetic renamed amount", Help: field.Help, LogicalType: field.LogicalType, Value: field.Value, Constraints: field.Constraints, Storage: field.Storage, Display: field.Display}
	rename, err := fieldchange.NewPlanner(catalog, catalog, plans, v2.NewIdentityAllocator(nil)).Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: description.Snapshot.TableID, FieldID: field.Identity.FieldID, ExpectedSchemaRev: description.Snapshot.SchemaRevision, Draft: &draft, Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fieldchange.NewExecutor(pb, plans).Apply(ctx, v2.ApplyRequest{PlanID: rename.PlanID, PlanHash: rename.PlanHash, OperationID: "dataset-rename", Actor: rename.Intent.Actor}); err != nil {
		t.Fatal(err)
	}
	renameValidation := request
	renameValidation.Versions = beforeRename["versions"].(map[string]pluginDatasetVersion)
	_, err = call("dataset.validate", renameValidation)
	assertCode("plugin_dataset_stale", err)
	if _, err = call("dataset.open", request); err != nil {
		t.Fatalf("canonical rename broke dataset binding: %v", err)
	}
	// The bounded query rejects row overflow, invalid filters and cancelled reads.
	err = pb.RunInTransaction(func(tx core.App) error {
		d, err := source.DescribeQueryTable(ctx, tx, description.Snapshot.TableID)
		if err != nil {
			return err
		}
		d.ProjectionFields = []string{"id"}
		budget := pluginDatasetBytes
		_, _, _, err = query.ReadBounded(ctx, tx, d, query.TableQuery{}, 204, &budget)
		assertCode("plugin_dataset_limit", err)
		_, _, _, err = query.ReadBounded(ctx, tx, d, query.TableQuery{Filters: []query.FilterExpression{{Field: "unknown", Operator: query.OperatorEqual, Value: "x"}}}, 205, &budget)
		if err == nil {
			t.Fatal("invalid filter accepted")
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, _, _, err = query.ReadBounded(cancelled, tx, d, query.TableQuery{}, 205, &budget)
		if err != context.Canceled {
			t.Fatalf("cancelled bounded read: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	denied := request
	denied.Sources = append([]pluginDatasetSource{}, request.Sources...)
	denied.Sources[2].Query.Fields = append(append([]string{}, denied.Sources[2].Query.Fields...), hidden.Identity.FieldID)
	_, err = call("dataset.open", denied)
	assertCode("plugin_read_denied", err)
	// An unauthorized predicate/group cannot be hidden behind an allowed projection.
	denied.Sources[2] = request.Sources[2]
	denied.Sources[2].Query.Filters = []query.FilterExpression{{Field: hidden.Identity.FieldID, Operator: query.OperatorIsNull}}
	_, err = call("dataset.open", denied)
	assertCode("plugin_read_denied", err)
	denied.Sources[2] = request.Sources[2]
	denied.Sources[2].GroupBy = []string{hidden.Identity.FieldID}
	_, err = call("dataset.open", denied)
	assertCode("plugin_read_denied", err)
	// A granted projected big cell is refused before its bytes enter the driver.
	// Escaped JSON can overflow the complete envelope even when raw cells fit.
	quotedTable := pb.DB().QuoteSimpleTableName(description.PhysicalName)
	quotedField := pb.DB().QuoteSimpleColumnName(field.Identity.PhysicalName)
	var unknownRow string
	if err = pb.DB().NewQuery("SELECT id FROM " + quotedTable + " WHERE " + quotedField + "='0.01' LIMIT 1").Row(&unknownRow); err != nil {
		t.Fatal(err)
	}
	if _, err = pb.DB().NewQuery("UPDATE " + quotedTable + " SET " + quotedField + "='unknown' WHERE id={:id}").Bind(dbx.Params{"id": unknownRow}).Execute(); err != nil {
		t.Fatal(err)
	}
	unknownDataset, err := call("dataset.open", request)
	if err != nil {
		t.Fatal(err)
	}
	unknownSource := unknownDataset["sources"].([]any)[0].(map[string]any)
	unknownSummary := unknownSource["summary"].(map[string]any)
	if unknownSource["complete"] != true || unknownSummary["complete"] != false || unknownSummary["sums"].([]any)[0].(map[string]any)["unknownCount"] != 1 || unknownSummary["sums"].([]any)[0].(map[string]any)["value"] != nil {
		t.Fatalf("unknown amount falsely complete: %#v", unknownSource)
	}
	if _, err = pb.DB().NewQuery("UPDATE " + quotedTable + " SET " + quotedField + "='0.01' WHERE id={:id}").Bind(dbx.Params{"id": unknownRow}).Execute(); err != nil {
		t.Fatal(err)
	}
	_, err = pb.DB().NewQuery("UPDATE " + pb.DB().QuoteSimpleTableName(description.PhysicalName) + " SET " + pb.DB().QuoteSimpleColumnName(field.Identity.PhysicalName) + "={:escaped}").Bind(dbx.Params{"escaped": strings.Repeat("\x00", 512)}).Execute()
	if err != nil {
		t.Fatal(err)
	}
	_, err = call("dataset.open", request)
	assertCode("plugin_dataset_limit", err)
	_, err = pb.DB().NewQuery("UPDATE " + pb.DB().QuoteSimpleTableName(description.PhysicalName) + " SET " + pb.DB().QuoteSimpleColumnName(field.Identity.PhysicalName) + "={:large} WHERE id=(SELECT id FROM " + pb.DB().QuoteSimpleTableName(description.PhysicalName) + " LIMIT 1)").Bind(dbx.Params{"large": strings.Repeat("y", pluginDatasetBytes+1)}).Execute()
	if err != nil {
		t.Fatal(err)
	}
	_, err = call("dataset.open", request)
	assertCode("plugin_dataset_limit", err)
}

func TestPluginDatasetSummaryDistinguishesMissingPendingAndExactZero(t *testing.T) {
	source := pluginDatasetSource{GroupBy: []string{"group"}, Sums: []pluginDatasetSum{{Field: "amount", Format: exactdecimal.Format{Storage: "decimalText", Scale: 2, Digits: 38}}}}
	rows := []map[string]any{{"group": nil, "amount": "0.00"}, {"group": "", "amount": nil}, {"group": "known", "amount": "unknown"}, {"group": "known", "amount": ""}, {"group": "known", "amount": map[string]any{"state": "updating", "fresh": false, "value": nil}}}
	result, err := summarizePluginDataset(rows, source)
	if err != nil {
		t.Fatal(err)
	}
	sum := result["sums"].([]any)[0].(map[string]any)
	if sum["value"] != nil || sum["complete"] != false || sum["zeroCount"] != 1 || sum["nullCount"] != 1 || sum["unknownCount"] != 1 || sum["unallocatedCount"] != 1 || sum["pendingCount"] != 1 {
		t.Fatalf("missing states collapsed: %#v", sum)
	}
	groups := result["groups"].([]any)
	if len(groups) != 3 || groups[0].(map[string]any)["key"].([]any)[0].(map[string]any)["state"] != "null" || groups[1].(map[string]any)["key"].([]any)[0].(map[string]any)["state"] != "unallocated" {
		t.Fatalf("group allocation states collapsed: %#v", groups)
	}
}

func TestPluginDatasetRelationTargetsAndComputedDependenciesRequireGrants(t *testing.T) {
	f := newTransactionFixture(t)
	ctx := context.Background()
	source, _ := queryschema.New(f.pb.DataDir())
	head, line := f.model.Bindings["headers"], f.model.Bindings["lines"]
	installed := pluginInstallation{ModelGrants: []pluginReadGrant{{Collection: line.TableID, Operations: []string{"read", "query"}, Fields: []string{"id", line.Fields["header"]}}}}
	request := pluginDatasetRequest{Contract: pluginDatasetContract, Sources: []pluginDatasetSource{{Query: pluginQueryRequest{Contract: "vibetable.plugin-query.v2", Collection: line.TableID, Fields: []string{"id", line.Fields["header"]}}, GroupBy: []string{line.Fields["header"]}}}}
	operation := "dataset.open"
	call := func() (map[string]any, error) {
		var result map[string]any
		err := f.pb.RunInTransaction(func(tx core.App) error {
			var err error
			result, err = executePluginDataset(ctx, tx, source, installed, "", operation, request)
			return err
		})
		return result, err
	}
	assertCode := func(code string, err error) {
		t.Helper()
		domain, ok := err.(*query.ProductError)
		if !ok || domain.Code != code {
			t.Fatalf("want %s got %v", code, err)
		}
	}
	_, err := call()
	assertCode("plugin_read_denied", err)
	installed.ModelGrants = append(installed.ModelGrants, pluginReadGrant{Collection: head.TableID, Operations: []string{"read", "query"}, Fields: []string{"id"}})
	if _, err = call(); err != nil {
		t.Fatal(err)
	}
	number := createSchemaProductField(t, f.pb, head.TableID, v2.LogicalNumber, "Synthetic source", "dataset-computed-input").Definition
	defaults, _ := v2.RecommendedDefaults(v2.LogicalFormula)
	catalog := fieldchange.NewCatalog(f.pb)
	revisions, err := catalog.Revisions(ctx, head.TableID)
	if err != nil {
		t.Fatal(err)
	}
	plans := fieldchange.NewPocketBasePlanStore(f.pb)
	plan, err := fieldchange.NewPlanner(catalog, catalog, plans, v2.NewIdentityAllocator(nil)).Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: head.TableID, ExpectedSchemaRev: revisions.Schema, Actor: v2.Actor{ID: "local-user", Kind: "user"}, Draft: &v2.FieldDraft{DisplayName: "Synthetic pending", LogicalType: v2.LogicalFormula, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display, Formula: &v2.FormulaDraftSpec{Language: "cel-v1", Source: number.Identity.PhysicalName + " * 2"}}})
	if err != nil {
		t.Fatal(err)
	}
	backfill := jobs.New(f.pb, nil)
	t.Cleanup(backfill.Shutdown)
	receipt, err := fieldchange.NewExecutor(f.pb, plans, fieldchange.WithFormulaBackfillScheduler(pluginComputedBackfillQueue{backfill})).Apply(ctx, v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "dataset-computed", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	computed := receipt.Definition.Identity.FieldID
	installed.ModelGrants = []pluginReadGrant{{Collection: head.TableID, Operations: []string{"read", "query"}, Fields: []string{"id", computed}}}
	request.Sources = []pluginDatasetSource{{Query: pluginQueryRequest{Contract: "vibetable.plugin-query.v2", Collection: head.TableID, Fields: []string{"id", computed}}, Sums: []pluginDatasetSum{{Field: computed, Format: exactdecimal.Format{Storage: "integerUnits", Scale: 2, Digits: 38}}}}}
	_, err = call()
	assertCode("plugin_read_denied", err)
	installed.ModelGrants[0].Fields = append(installed.ModelGrants[0].Fields, number.Identity.FieldID)
	opened, err := call()
	if err != nil {
		t.Fatal(err)
	}
	output := opened["sources"].([]any)[0].(map[string]any)
	if fields := output["computedFields"].([]string); len(fields) != 1 || fields[0] != computed {
		t.Fatalf("computed field metadata is not canonical: %#v", fields)
	}
	summary := output["summary"].(map[string]any)
	if output["complete"] != false || summary["complete"] != false || summary["sums"].([]any)[0].(map[string]any)["value"] != nil {
		t.Fatalf("empty pending computed projection falsely complete: %#v", output)
	}
	request.Versions = opened["versions"].(map[string]pluginDatasetVersion)
	operation = "dataset.validate"
	runtime, err := f.pb.FindFirstRecordByFilter("vibetable_formulas", "table_id={:table} && field_id={:field}", dbx.Params{"table": head.TableID, "field": computed})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Set("status", "ready")
	if err = f.pb.Save(runtime); err != nil {
		t.Fatal(err)
	}
	_, err = call()
	assertCode("plugin_dataset_stale", err)
}
