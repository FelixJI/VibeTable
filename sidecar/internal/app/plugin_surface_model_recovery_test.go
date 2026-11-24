package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

func schemaAudits(t *testing.T, app core.App, filter string, params dbx.Params) []*core.Record {
	t.Helper()
	rows, err := app.FindRecordsByFilter("vibetable_schema_audit", filter, "id", 0, 0, params)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func schemaAuditsByKey(t *testing.T, app core.App, collection string, key string) []*core.Record {
	t.Helper()
	rows, err := app.FindRecordsByFilter(collection, "kind='audit' && item_key={:key}", "id", 0, 0, dbx.Params{"key": key})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func mustGrants(t *testing.T, pb core.App, workspace string, input pluginSurfaceAPIRequest) pluginSurfaceInstallation {
	t.Helper()
	// surfaceInstallation resolves the grants of the newest completed state itself.
	installed, err := surfaceInstallation(context.Background(), pb, workspace, input)
	if err != nil {
		t.Fatal(err)
	}
	return installed
}

func mustProtected(t *testing.T, pb core.App, tableID string, fieldID string) {
	t.Helper()
	var protected *schemaerror.ProductError
	if err := pluginmodel.ProtectTable(context.Background(), pb, tableID); !errors.As(err, &protected) || protected.Code != "plugin.model.core_protected" {
		t.Fatalf("bound table %s lost protection: %v", tableID, err)
	}
	if err := pluginmodel.ProtectField(context.Background(), pb, tableID, fieldID, &v2.FieldDefinition{}, nil); !errors.As(err, &protected) || protected.Code != "plugin.model.core_protected" {
		t.Fatalf("bound field %s.%s lost protection: %v", tableID, fieldID, err)
	}
}

// interruptFieldRuns fails the apply of the step that creates the n-th field
// record of the request, inside the executor transaction. It returns how to
// stop failing and a counter of the field records created so far.
func interruptFieldRuns(t *testing.T, pb core.App, failing map[int]bool) (func(), *int) {
	t.Helper()
	creates := 0
	hook := pb.OnRecordCreate("vibetable_fields").BindFunc(func(event *core.RecordEvent) error {
		creates++
		if failing[creates] {
			return errors.New("synthetic field write interrupted")
		}
		return event.Next()
	})
	return func() { pb.OnRecordCreate("vibetable_fields").Unbind(hook) }, &creates
}

func auditSnapshot(row *core.Record) string {
	return strings.Join([]string{row.Id, row.GetString("operation_id"), row.GetString("plan_id"), row.GetString("action"), row.GetString("table_id"),
		row.GetString("field_id"), row.GetString("outcome"), row.GetString("error_code"), row.GetString("migration_job_id"), row.GetDateTime("occurred_at").String()}, "|")
}

func stepAuditRows(t *testing.T, pb core.App, stepID string) []*core.Record {
	t.Helper()
	return schemaAudits(t, pb, "operation_id>={:step} && operation_id<{:bound}", dbx.Params{"step": stepID, "bound": stepID + modelStepRetryBound})
}

// A field step that fails inside the executor leaves a failure receipt under
// the step's deterministic operation id. The receipt is append-only: resuming
// the same request applies the step under the step's next operation id,
// exactly once, while every earlier receipt keeps its number and plan.
func TestPluginModelCommitRetryUsesNewOperationIDAndKeepsFailureReceipts(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	preview := result.(modelPreview)
	// The second field step (tickets.title) fails inside the apply transaction
	// on its first two attempts: attempt 1 creates people.name then fails on
	// tickets.title; attempt 2 goes straight to tickets.title and fails again.
	unbind, creates := interruptFieldRuns(t, pb, map[int]bool{2: true, 3: true})
	attempt := func() (pluginmodel.State, error) {
		result, err := modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
		return result.(pluginmodel.State), err
	}
	partial, err := attempt()
	if err == nil || *creates != 2 {
		t.Fatalf("first interruption did not reach the executor transaction: creates=%d err=%v", *creates, err)
	}
	partial, err = attempt()
	unbind()
	if err == nil || *creates != 3 {
		t.Fatalf("second interruption did not reach the executor transaction: creates=%d err=%v", *creates, err)
	}
	if partial.Status != "partial" || partial.Completed != 4 || partial.ErrorCode == "" {
		t.Fatalf("inaccurate partial state: %+v", partial)
	}
	if len(mustGrants(t, pb, workspace, input).ModelGrants) != 0 {
		t.Fatal("partial model granted data access")
	}
	stepID := "model-" + preview.RequestID + "-tickets.title"
	receipts := stepAuditRows(t, pb, stepID)
	if len(receipts) != 2 {
		t.Fatalf("two interrupted attempts left %d audit rows: %+v", len(receipts), receipts)
	}
	first, second := receipts[0], receipts[1]
	if first.GetString("operation_id") != stepID {
		first, second = second, first
	}
	// The first receipt keeps the step's own number; the second attempt was
	// already a retry and recorded its failure under the step's next number.
	if first.GetString("operation_id") != stepID || second.GetString("operation_id") != stepID+modelStepRetrySeparator+"2" ||
		first.GetString("plan_id") == "" || second.GetString("plan_id") == "" {
		t.Fatalf("failure receipts are not numbered by attempt: %+v", receipts)
	}
	for _, receipt := range receipts {
		if receipt.GetString("outcome") != "failed" || receipt.GetString("error_code") != "field.change.apply_failed" ||
			receipt.GetString("table_id") != partial.Bindings["tickets"].TableID {
			t.Fatalf("failure receipt differs: %+v", receipt)
		}
	}
	kept := []string{auditSnapshot(first), auditSnapshot(second)}

	complete, err := attempt()
	if err != nil {
		t.Fatalf("resume was rejected by the earlier failure receipts: %v", err)
	}
	if complete.Status != "completed" || complete.Completed != len(preview.Steps) || complete.Total != len(preview.Steps) || complete.ErrorCode != "" {
		t.Fatalf("resume did not finish the install: %+v", complete)
	}
	for key, binding := range partial.Bindings {
		if complete.Bindings[key].TableID != binding.TableID {
			t.Fatalf("resume replaced table %s", key)
		}
		for field, id := range binding.Fields {
			if complete.Bindings[key].Fields[field] != id {
				t.Fatalf("resume replaced field %s.%s", key, field)
			}
		}
	}
	// The successful attempt is the step's third operation and the two failure
	// receipts are byte-for-byte what the failed attempts wrote.
	history := stepAuditRows(t, pb, stepID)
	if len(history) != 3 {
		t.Fatalf("step audit history = %d rows: %+v", len(history), history)
	}
	var applied *core.Record
	var appliedCount int
	for _, row := range history {
		if row.GetString("outcome") != "applied" {
			continue
		}
		appliedCount++
		applied = row
	}
	if appliedCount != 1 || applied.GetString("operation_id") != stepID+modelStepRetrySeparator+"3" ||
		applied.GetString("plan_id") == "" || applied.GetString("field_id") != complete.Bindings["tickets"].Fields["title"] {
		t.Fatalf("applied step audit differs: %+v", history)
	}
	current := map[string]string{}
	for _, row := range history {
		if row.GetString("outcome") == "failed" {
			current[row.Id] = auditSnapshot(row)
		}
	}
	if len(current) != 2 || current[first.Id] != kept[0] || current[second.Id] != kept[1] {
		t.Fatalf("resume rewrote a failure receipt: %v -> %v", kept, current)
	}
	fields, err := pb.CountRecords("vibetable_fields", dbx.HashExp{"table_id": complete.Bindings["tickets"].TableID})
	if err != nil {
		t.Fatal(err)
	}
	declared := 0
	for _, entity := range preview.Model.Entities {
		if entity.Key == "tickets" {
			declared = len(entity.Fields)
		}
	}
	if declared == 0 || fields != int64(declared) {
		t.Fatalf("tickets table has %d field rows for %d declared fields: the resumed step applied twice", fields, declared)
	}
	// Every other field step was applied once under its own id, and nothing
	// else failed.
	for _, step := range preview.Steps {
		if step.Kind == "table" || step.OperationID == stepID {
			continue
		}
		rows := schemaAudits(t, pb, "operation_id={:operation}", dbx.Params{"operation": step.OperationID})
		if len(rows) != 1 || rows[0].GetString("outcome") != "applied" {
			t.Fatalf("step %s audit rows: %+v", step.OperationID, rows)
		}
	}
	if rows := schemaAudits(t, pb, "outcome='failed'", dbx.Params{}); len(rows) != 2 {
		t.Fatalf("unexpected failure receipts: %+v", rows)
	}
	if rows := schemaAudits(t, pb, "outcome='applied' && action!='table.create'", dbx.Params{}); len(rows) != len(preview.Steps)-len(preview.Model.Entities) {
		t.Fatalf("applied field audits = %d, want one per field step", len(rows))
	}

	// Repeating the committed request replays the state without applying again.
	if replay, err := attempt(); err != nil || replay.Completed != len(preview.Steps) {
		t.Fatalf("committed request did not replay: %+v %v", replay, err)
	}
	if after := stepAuditRows(t, pb, stepID); len(after) != 3 {
		t.Fatalf("replay changed the audit history: %+v", after)
	}
	if fields, err = pb.CountRecords("vibetable_fields", dbx.HashExp{"table_id": complete.Bindings["tickets"].TableID}); err != nil || fields != int64(declared) {
		t.Fatalf("replay applied the step again: %d %v", fields, err)
	}

	// Grants and native protection come from the completed state only.
	installed := mustGrants(t, pb, workspace, input)
	if len(installed.ModelGrants) != len(preview.Model.Entities) {
		t.Fatalf("resumed model grants = %d, want %d", len(installed.ModelGrants), len(preview.Model.Entities))
	}
	for key, binding := range complete.Bindings {
		if installed.grant(binding.TableID, "") == nil {
			t.Fatalf("resumed model did not grant %s", key)
		}
		mustProtected(t, pb, binding.TableID, binding.Fields[firstField(preview.Model, key)])
	}
}

// A new operation id is only derived from a bare failure receipt of the same
// step. Any other audit row under the step id keeps the executor's unique
// operation_id protection: the retry is rejected, the row stays as it is and
// nothing is applied.
func TestPluginModelCommitRetryNeedsBareFailureReceiptOfSameStep(t *testing.T) {
	for _, scenario := range []string{"migration_job", "other_table", "other_action", "applied"} {
		t.Run(scenario, func(t *testing.T) {
			pb, workspace, input, domain := modelFixture(t)
			result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
			if err != nil {
				t.Fatal(err)
			}
			preview := result.(modelPreview)
			unbind, creates := interruptFieldRuns(t, pb, map[int]bool{2: true})
			result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
			unbind()
			if err == nil || *creates != 2 {
				t.Fatalf("interruption did not reach the executor transaction: creates=%d err=%v", *creates, err)
			}
			partial := result.(pluginmodel.State)
			ticketsTable := partial.Bindings["tickets"].TableID
			stepID := "model-" + preview.RequestID + "-tickets.title"
			receipt := schemaAudits(t, pb, "operation_id={:operation}", dbx.Params{"operation": stepID})
			if len(receipt) != 1 || receipt[0].GetString("outcome") != "failed" {
				t.Fatalf("failure receipt missing: %+v", receipt)
			}
			switch scenario {
			case "migration_job":
				receipt[0].Set("migration_job_id", "job_synthetic")
			case "other_table":
				receipt[0].Set("table_id", "tbl_other_table")
			case "other_action":
				receipt[0].Set("action", "field.update")
			case "applied":
				receipt[0].Set("outcome", "applied")
				receipt[0].Set("error_code", "")
			}
			if err = pb.Save(receipt[0]); err != nil {
				t.Fatal(err)
			}
			before := auditSnapshot(receipt[0])
			_, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
			if err == nil || !strings.Contains(err.Error(), "must be unique") {
				t.Fatalf("retry under a %s audit row returned %v", scenario, err)
			}
			after := stepAuditRows(t, pb, stepID)
			if len(after) != 1 || auditSnapshot(after[0]) != before {
				t.Fatalf("rejected retry changed the audit row: %s -> %+v", before, after)
			}
			if fields, err := pb.CountRecords("vibetable_fields", dbx.HashExp{"table_id": ticketsTable}); err != nil || fields != 0 {
				t.Fatalf("rejected retry applied the step: fields=%d %v", fields, err)
			}
		})
	}
}

func firstField(model pluginmodel.Declaration, entity string) string {
	for _, e := range model.Entities {
		if e.Key == entity {
			return e.Fields[0].Key
		}
	}
	return ""
}

// History is re-read after every installed step. The read must only cover the
// progress appended since the request validated its history: a full re-read per
// step made a large install pay for all earlier steps at every step.
func TestPluginModelCommitReadsOnlyNewProgressDuringSteps(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	const entities, scalars = 12, 5
	input = changeInstalledModel(t, pb, workspace, input, func(model *pluginmodel.Declaration) {
		model.ModelID = "scaled"
		model.Entities = nil
		for i := range entities {
			entity := pluginmodel.Entity{Key: fmt.Sprintf("entity_%02d", i), Label: fmt.Sprintf("实体%02d", i), Operations: []string{"read", "query"}}
			for f := range scalars {
				entity.Fields = append(entity.Fields, pluginmodel.Field{Key: fmt.Sprintf("field_%d", f), Label: fmt.Sprintf("字段%d", f), Type: v2.LogicalText})
			}
			next := (i + 1) % entities
			entity.Fields = append(entity.Fields, pluginmodel.Field{Key: "next", Label: "下一个", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: fmt.Sprintf("entity_%02d", next), Field: "previous", Cardinality: "many"}})
			if i > 0 {
				entity.Fields = append(entity.Fields, pluginmodel.Field{Key: "previous", Label: "上一个", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: fmt.Sprintf("entity_%02d", i-1), Field: "next", Cardinality: "one"}})
			}
			model.Entities = append(model.Entities, entity)
		}
		// Close the ring: the first entity owns the "one" side of the last link.
		model.Entities[0].Fields = append(model.Entities[0].Fields, pluginmodel.Field{Key: "previous", Label: "上一个", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: fmt.Sprintf("entity_%02d", entities-1), Field: "next", Cardinality: "one"}})
	})
	result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	preview := result.(modelPreview)
	if len(preview.Diagnostics) != 0 || len(preview.Steps) != entities*(1+scalars+1) {
		t.Fatalf("preview: diagnostics=%d steps=%d", len(preview.Diagnostics), len(preview.Steps))
	}
	var mu sync.Mutex
	full, ranged := 0, 0
	for _, database := range []*dbx.DB{pb.ConcurrentDB().(*dbx.DB), pb.NonconcurrentDB().(*dbx.DB)} {
		previous := database.QueryLogFunc
		database.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
			if previous != nil {
				previous(ctx, elapsed, statement, rows, err)
			}
			if !strings.Contains(statement, "vibetable_plugin_records") || !strings.Contains(statement, "model.progress") || !strings.HasPrefix(strings.TrimSpace(statement), "SELECT") {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if strings.Contains(statement, ".seq]] > ") {
				ranged++
			} else {
				full++
			}
		}
		t.Cleanup(func() { database.QueryLogFunc = previous })
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
	if err != nil {
		t.Fatal(err)
	}
	state := result.(pluginmodel.State)
	if state.Status != "completed" || state.Completed != len(preview.Steps) {
		t.Fatalf("scaled install: %+v", state)
	}
	mu.Lock()
	fullReads, rangeReads := full, ranged
	mu.Unlock()
	t.Logf("MODEL_HISTORY_READS steps=%d full=%d ranged=%d", len(preview.Steps), fullReads, rangeReads)
	// Entry, admission and completion validate the whole history. Nothing else
	// may: before the fix every step re-read all earlier steps twice.
	if fullReads > 8 {
		t.Fatalf("install re-read the full history %d times for %d steps", fullReads, len(preview.Steps))
	}
	if rangeReads < len(preview.Steps) {
		t.Fatalf("only %d of %d step re-authentications read newer progress", rangeReads, len(preview.Steps))
	}
}

// Progress appended after the commit took its history watermark must reach the
// re-authentication of the next step even when its key does not follow the
// current convention, and it must never be skipped: the step is not applied,
// the same request and every other authority check keep failing until the
// record is repaired, and the repaired request then finishes without gaps.
func TestPluginModelCommitRejectsStepOnCorruptProgressAppendedMidCommit(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	preview := result.(modelPreview)
	corrupted := false
	domain.gates = []businessWriteGate{func(ctx context.Context, kind, id string, write func(context.Context) error) error {
		if kind == "field.change.apply" && !corrupted {
			corrupted = true
			// The gate runs before the step transaction: the record is durable
			// when the step re-authenticates, and it stays afterwards.
			err := pb.RunInTransaction(func(tx core.App) error {
				installed, err := surfaceInstallation(ctx, tx, workspace, input)
				if err != nil {
					return err
				}
				_, err = pluginstore.New(tx, workspace).RecordAuditInTransaction(ctx, modelAudit(input, installed, "legacy-corrupt-progress", "model.progress", map[string]any{"contract": "invalid"}))
				return err
			})
			if err != nil {
				return err
			}
		}
		return write(ctx)
	}}
	result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
	if err == nil || !corrupted || !strings.Contains(err.Error(), "progress contract is invalid") {
		t.Fatalf("corrupt progress was not exercised: %v", err)
	}
	partial := result.(pluginmodel.State)
	if partial.Status != "partial" || partial.Completed != 3 {
		t.Fatalf("a step was applied on top of corrupt progress: %+v", partial)
	}
	corrupt := func() int {
		return len(schemaAuditsByKey(t, pb, "vibetable_plugin_records", "legacy-corrupt-progress"))
	}
	if corrupt() != 1 {
		t.Fatalf("%d corrupt records after the rejected step", corrupt())
	}
	if fields, err := pb.CountRecords("vibetable_fields", dbx.HashExp{"table_id": partial.Bindings["people"].TableID}); err != nil || fields != 0 {
		t.Fatalf("rejected step left %d field rows: %v", fields, err)
	}
	if _, err = surfaceInstallation(context.Background(), pb, workspace, input); err == nil {
		t.Fatal("corrupt history kept model authority")
	}
	if _, err = modelCall(t, pb, workspace, input, domain, "model.read", map[string]any{}); err == nil {
		t.Fatal("model.read skipped corrupt progress")
	}
	if _, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID}); err == nil {
		t.Fatal("resume skipped corrupt progress")
	}
	if fields, err := pb.CountRecords("vibetable_fields", dbx.HashExp{"table_id": partial.Bindings["people"].TableID}); err != nil || fields != 0 || corrupt() != 1 {
		t.Fatalf("rejected resume wrote progress: fields=%d corrupt=%d %v", fields, corrupt(), err)
	}
	// Once the corrupt record is repaired the same request finishes without
	// repeating or skipping a step.
	if _, err = pb.DB().NewQuery("DELETE FROM vibetable_plugin_records WHERE item_key={:key}").Bind(dbx.Params{"key": "legacy-corrupt-progress"}).Execute(); err != nil {
		t.Fatal(err)
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
	if err != nil {
		t.Fatalf("resume after repair: %v", err)
	}
	if done := result.(pluginmodel.State); done.Status != "completed" || done.Completed != len(preview.Steps) {
		t.Fatalf("resume after repair: %+v", done)
	}
	if len(mustGrants(t, pb, workspace, input).ModelGrants) != 3 {
		t.Fatal("resumed model did not grant its entities")
	}
	steps := 0
	for _, step := range preview.Steps {
		if step.Kind == "table" {
			continue
		}
		steps++
		rows := stepAuditRows(t, pb, step.OperationID)
		expected := map[string]string{step.OperationID: "applied"}
		if step.OperationID == preview.Steps[partial.Completed].OperationID {
			expected[step.OperationID] = "failed"
			expected[step.OperationID+"#2"] = "applied"
		}
		if len(rows) != len(expected) {
			t.Fatalf("step %s left %d audit rows, want %d", step.OperationID, len(rows), len(expected))
		}
		for _, row := range rows {
			id, outcome := row.GetString("operation_id"), row.GetString("outcome")
			if want, ok := expected[id]; !ok || want != outcome {
				t.Fatalf("step %s unexpected audit %s: %s", step.OperationID, id, outcome)
			}
		}
	}
	if steps == 0 {
		t.Fatal("preview has no field steps")
	}
}

// The per-step re-read is only cheap while it stays an indexed range: it must
// use the event lookup index (so do the full reads) and seek straight to the
// watermark instead of scanning the whole history.
func TestPluginModelProgressHistoryReadsStayIndexed(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	statements := map[string]string{}
	var mu sync.Mutex
	remember := func(kind, statement string) {
		mu.Lock()
		defer mu.Unlock()
		if _, exists := statements[kind]; !exists {
			statements[kind] = statement
		}
	}
	for _, database := range []*dbx.DB{pb.ConcurrentDB().(*dbx.DB), pb.NonconcurrentDB().(*dbx.DB)} {
		previous := database.QueryLogFunc
		database.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
			if previous != nil {
				previous(ctx, elapsed, statement, rows, err)
			}
			if !strings.Contains(statement, "vibetable_plugin_records") || !strings.Contains(statement, "model.progress") || !strings.HasPrefix(strings.TrimSpace(statement), "SELECT") {
				return
			}
			if strings.Contains(statement, ".seq]] > ") {
				remember("ranged", statement)
			} else {
				remember("full", statement)
			}
		}
		t.Cleanup(func() { database.QueryLogFunc = previous })
	}
	if _, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": result.(modelPreview).RequestID}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"full", "ranged"} {
		statement := statements[kind]
		if statement == "" {
			t.Fatalf("no %s history read happened during the commit", kind)
		}
		var plan []struct {
			Detail string `db:"detail"`
		}
		if err = pb.DB().NewQuery("EXPLAIN QUERY PLAN " + statement).All(&plan); err != nil {
			t.Fatalf("explain %s read: %v", kind, err)
		}
		var details []string
		for _, step := range plan {
			details = append(details, step.Detail)
		}
		joined := strings.Join(details, "\n")
		if !strings.Contains(joined, "idx_vibetable_plugin_records_event_lookup") || strings.Contains(joined, "TEMP B-TREE") {
			t.Fatalf("%s history read is not served by the event index:\n%s", kind, joined)
		}
		if kind == "ranged" && !strings.Contains(joined, "seq>?") {
			t.Fatalf("ranged history read does not seek to the watermark:\n%s", joined)
		}
		t.Logf("%s history read plan:\n%s", kind, joined)
	}
}

// Completed states recorded before the ":completed" key convention exist are
// the only authority for their install. They must keep granting, and an upgrade
// must still use them as its base.
func TestPluginModelCompletedStateUnderLegacyKeyKeepsAuthority(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	preview := result.(modelPreview)
	result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
	if err != nil {
		t.Fatal(err)
	}
	before := result.(pluginmodel.State)
	rows := schemaAuditsByKey(t, pb, "vibetable_plugin_records", preview.RequestID+":completed")
	if len(rows) != 1 {
		t.Fatalf("completed states for the request: %d", len(rows))
	}
	legacy := preview.RequestID + "-finished"
	if _, err = pb.DB().NewQuery("UPDATE vibetable_plugin_records SET item_key={:key} WHERE id={:id}").Bind(dbx.Params{"key": legacy, "id": rows[0].Id}).Execute(); err != nil {
		t.Fatal(err)
	}
	if len(schemaAuditsByKey(t, pb, "vibetable_plugin_records", legacy)) != 1 || len(schemaAuditsByKey(t, pb, "vibetable_plugin_records", preview.RequestID+":completed")) != 0 {
		t.Fatal("completed state was not moved under the legacy key")
	}
	if installed := mustGrants(t, pb, workspace, input); len(installed.ModelGrants) != 3 {
		t.Fatalf("completed state under a legacy key granted %d entities", len(installed.ModelGrants))
	}
	if result, err = modelCall(t, pb, workspace, input, domain, "model.status", map[string]any{"requestId": preview.RequestID}); err != nil || result.(pluginmodel.State).Status != "completed" {
		t.Fatalf("completed state under a legacy key is not reported: %v", err)
	}
	assets := before.Bindings["assets"]
	input = changeInstalledModel(t, pb, workspace, input, func(model *pluginmodel.Declaration) {
		model.Version = 2
		model.Entities[2].Fields = append(model.Entities[2].Fields, pluginmodel.Field{Key: "checked", Label: "已核对", Type: v2.LogicalBool})
	})
	result, err = modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	upgrade := result.(modelPreview)
	if len(upgrade.Diagnostics) != 0 || len(upgrade.Steps) != 1 {
		t.Fatalf("upgrade on a legacy-key install: diagnostics=%d steps=%d", len(upgrade.Diagnostics), len(upgrade.Steps))
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": upgrade.RequestID})
	if err != nil {
		t.Fatalf("upgrade on a legacy-key install failed: %v", err)
	}
	done := result.(pluginmodel.State)
	if done.Bindings["assets"].TableID != assets.TableID || done.Bindings["assets"].Fields["checked"] == "" {
		t.Fatalf("upgrade on a legacy-key install lost identities: %+v", done.Bindings["assets"])
	}
	if installed := mustGrants(t, pb, workspace, input); len(installed.ModelGrants) != 3 {
		t.Fatalf("upgrade on a legacy-key install granted %d entities", len(installed.ModelGrants))
	}
}
