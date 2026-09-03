package integration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
)

func TestRealtimeRecoveryIncludesTheWholeActiveSetAndRejectsOverflow(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Populate the persisted recovery boundary, not 32 concurrently running workers.
	if _, err := app.DB().NewQuery(`
		WITH RECURSIVE sequence(value) AS (
			SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value < 10000
		)
		INSERT INTO vibetable_jobs (
			id, job_type, state, cursor_json, progress_json, schema_revision,
			source_event_id, source_table_id, relation_field_id
		)
		SELECT printf('job%012d', value), 'formula_fanout', 'queued',
			'{"tableId":"table","lastRecordId":"last-row"}',
			'{"completed":1,"total":2}', 1,
			printf('event-%05d', value), 'table', printf('field-%05d', value)
		FROM sequence
	`).Execute(); err != nil {
		t.Fatal(err)
	}
	// Other owners share the collection with deliberately incompatible cursors.
	for index, family := range []string{"field_migration", "field_resource_cleanup"} {
		if _, err := app.DB().NewQuery(`INSERT INTO vibetable_jobs
			(id,job_type,state,cursor_json,progress_json,schema_revision)
			VALUES ({:id},{:family},'running','{}','{}',1)`,
		).Bind(dbx.Params{"id": fmt.Sprintf("other%010d", index), "family": family}).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	hub := realtime.New(app)
	subscription, err := hub.SubscribeRecoverable(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := recoveredProjection(t, subscription)
	subscription.Close()
	if len(snapshot.ActiveFormulaTasks) != 10000 || len(snapshot.TerminalNotifications) != 0 {
		t.Fatalf("active=%d terminal=%d", len(snapshot.ActiveFormulaTasks), len(snapshot.TerminalNotifications))
	}
	last := snapshot.ActiveFormulaTasks[9999]
	if last.TaskID != "job000000010000" || last.State != "pending" || last.Progress != 0.5 || last.Cursor == nil || *last.Cursor != "row:last-row" {
		t.Fatalf("last persisted task projection=%+v", last)
	}
	if _, err := app.DB().NewQuery(`INSERT INTO vibetable_jobs
		(id,job_type,state,cursor_json,progress_json,schema_revision)
		VALUES ('job000000010001','formula_backfill','queued','{"tableId":"table"}','{}',1)`).Execute(); err != nil {
		t.Fatal(err)
	}
	requireRecoveryError(t, hub, ctx, "", "realtime.recovery_limit")
}

func TestRealtimeRecoveryRejectsCorruptAuthorityWithoutLeakingSubscriptions(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := app.DB().NewQuery(`INSERT INTO vibetable_jobs
		(id,job_type,state,cursor_json,progress_json,error_json,schema_revision)
		VALUES ('corrupttask0001','formula_backfill','running','{"tableId":"table"}','{}',null,1)`).Execute(); err != nil {
		t.Fatal(err)
	}
	hub := realtime.New(app)
	for _, input := range []struct{ name, cursor, progress, taskError string }{
		{"cursor", `{}`, `{}`, `null`},
		{"progress", `{"tableId":"table"}`, `{"completed":2,"total":1}`, `null`},
		{"error", `{"tableId":"table"}`, `{}`, `{"code":3}`},
	} {
		t.Run(input.name, func(t *testing.T) {
			if _, err := app.DB().NewQuery(`UPDATE vibetable_jobs SET cursor_json={:cursor},
				progress_json={:progress},error_json={:error} WHERE id='corrupttask0001'`).Bind(dbx.Params{
				"cursor": input.cursor, "progress": input.progress, "error": input.taskError,
			}).Execute(); err != nil {
				t.Fatal(err)
			}
			// More than the hub's 32 slots proves failed recovery leaves no admission behind.
			for range 33 {
				requireRecoveryError(t, hub, ctx, "", "realtime.storage_corrupt")
			}
		})
	}
	if _, err := app.DB().NewQuery(`UPDATE vibetable_jobs SET error_json=null`).Execute(); err != nil {
		t.Fatal(err)
	}
	saveRealtimeOutboxEvent(t, app, realtimeDataEvent(0))
	if _, err := app.DB().NewQuery(`UPDATE vibetable_outbox SET payload_json='{}'`).Execute(); err != nil {
		t.Fatal(err)
	}
	requireRecoveryError(t, hub, ctx, "", "realtime.storage_corrupt")
}

func TestRealtimeRecoveryRejectsAnOversizedFrameInsteadOfTruncating(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := realtime.New(app)
	for index := range 33 {
		if err := hub.PersistTaskChanged(ctx, app, jobs.Snapshot{
			JobID: fmt.Sprintf("large-terminal-%d", index), State: "failed",
			Error: &jobs.JobError{Code: "job.example", Message: strings.Repeat("x", 128*1024)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	requireRecoveryError(t, hub, ctx, "", "realtime.recovery_limit")
}

func requireRecoveryError(t *testing.T, hub *realtime.Hub, ctx context.Context, after, code string) {
	t.Helper()
	subscription, err := hub.SubscribeRecoverable(ctx, after)
	if subscription != nil {
		subscription.Close()
		t.Fatal("failed recovery returned a subscription or partial bookmark")
	}
	var realtimeError *realtime.Error
	if !errors.As(err, &realtimeError) || realtimeError.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}
