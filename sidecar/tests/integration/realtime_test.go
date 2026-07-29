package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func saveRealtimeOutboxEvent(
	t *testing.T,
	app core.App,
	event mutation.DataChangedEvent,
) {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("vibetable_outbox")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("event_id", event.EventID)
	record.Set("topic", event.Topic)
	record.Set("payload_json", types.JSONRaw(raw))
	record.Set("status", "pending")
	record.Set("attempts", 0)
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
}

func realtimeDataEvent(index int) mutation.DataChangedEvent {
	return mutation.DataChangedEvent{
		ContractVersion: mutation.ContractVersion,
		Topic:           "data.changed",
		EventID:         fmt.Sprintf("evt_retention_%05d", index),
		Sequence:        int64(index + 1),
		OccurredAt:      time.Unix(int64(index+1), 0).UTC().Format(time.RFC3339),
		SchemaRevision:  "schema_0001",
		DataRevision:    fmt.Sprintf("data_%04d", index+1),
		TableID:         "retention_table",
		RecordIDs:       []string{fmt.Sprintf("record_%05d", index)},
		Operation:       "update",
	}
}

func TestRealtimeHubDeliversLiveCatchupAndRejectsUnknownCursor(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("realtime_notes", "realtime_notes", []schema.FieldDefinition{
			field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.New(app)
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithPublisher(hub),
	)
	live, err := hub.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if len(live.Backlog) != 0 {
		t.Fatalf("initial backlog = %#v", live.Backlog)
	}
	recordID := "realtimerecord1"
	request := mutationRequest(
		"realtime_notes",
		definition.SchemaRevision,
		"realtime-insert",
		mutation.Operation{
			Kind:     mutation.OperationInsert,
			RecordID: &recordID,
			Values:   map[string]any{"title": "first"},
		},
	)
	receipt, err := kernel.Apply(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var delivered realtime.Event
	select {
	case delivered = <-live.Events:
	case <-time.After(2 * time.Second):
		t.Fatal("live event was not delivered")
	}
	if delivered.ID != receipt.EmittedEvents[0] ||
		delivered.Topic != "data.changed" {
		t.Fatalf("live event = %#v", delivered)
	}

	catchup, err := hub.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer catchup.Close()
	if len(catchup.Backlog) != 1 ||
		catchup.Backlog[0].ID != delivered.ID {
		t.Fatalf("catchup backlog = %#v", catchup.Backlog)
	}
	resumed, err := hub.Subscribe(ctx, delivered.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if len(resumed.Backlog) != 0 {
		t.Fatalf("resumed backlog = %#v", resumed.Backlog)
	}
	_, err = hub.Subscribe(ctx, "unknown-event")
	var realtimeErr *realtime.Error
	if !errors.As(err, &realtimeErr) ||
		realtimeErr.Code != "realtime.cursor_unknown" {
		t.Fatalf("unknown cursor error = %#v", err)
	}

	replayed, err := kernel.Apply(ctx, request)
	if err != nil || replayed.Status != mutation.StatusReplayed {
		t.Fatalf("replay = %#v, err=%v", replayed, err)
	}
	select {
	case duplicate := <-live.Events:
		t.Fatalf("replay emitted duplicate event %#v", duplicate)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRealtimeTaskCancellationAdvancesSequenceAndKeepsIdentity(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	hub := realtime.New(app)
	subscription, err := hub.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	running := jobs.Snapshot{
		JobID:    "job-cancelled-1",
		State:    "running",
		Progress: jobs.Progress{Completed: 100, Total: 1_000},
	}
	cancelled := running
	cancelled.State = "cancelled"
	if err := hub.PublishTaskChanged(ctx, running); err != nil {
		t.Fatal(err)
	}
	if err := hub.PublishTaskChanged(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	var events []realtime.Event
	for len(events) < 2 {
		select {
		case event := <-subscription.Events:
			events = append(events, event)
		case <-time.After(2 * time.Second):
			t.Fatal("task event was not delivered")
		}
	}
	var runningEvent, cancelledEvent realtime.TaskChangedEvent
	if err := json.Unmarshal(events[0].Payload, &runningEvent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(events[1].Payload, &cancelledEvent); err != nil {
		t.Fatal(err)
	}
	if cancelledEvent.State != "cancelled" ||
		cancelledEvent.Sequence <= runningEvent.Sequence ||
		cancelledEvent.EventID == runningEvent.EventID {
		t.Fatalf(
			"running=%#v cancelled=%#v",
			runningEvent,
			cancelledEvent,
		)
	}
}

func TestRealtimeOutboxRetainsTenThousandAndClassifiesDurableCursors(
	t *testing.T,
) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	hub := realtime.New(app)
	saveRealtimeOutboxEvent(t, app, realtimeDataEvent(0))
	initial, err := hub.Subscribe(ctx, "")
	if err != nil || len(initial.Backlog) != 1 {
		t.Fatalf("initial backlog=%d err=%v", len(initial.Backlog), err)
	}
	expiredCursor := initial.Backlog[0].Cursor
	initial.Close()
	for index := 1; index <= 10_005; index++ {
		saveRealtimeOutboxEvent(t, app, realtimeDataEvent(index))
	}
	backlog, err := hub.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer backlog.Close()
	if len(backlog.Backlog) != 10_000 {
		t.Fatalf("retained backlog = %d", len(backlog.Backlog))
	}
	if backlog.Backlog[0].ID != realtimeDataEvent(6).EventID ||
		backlog.Backlog[len(backlog.Backlog)-1].ID != realtimeDataEvent(10_005).EventID {
		t.Fatalf(
			"retained range = %s..%s",
			backlog.Backlog[0].ID,
			backlog.Backlog[len(backlog.Backlog)-1].ID,
		)
	}
	resumed, err := hub.Subscribe(ctx, backlog.Backlog[0].Cursor)
	if err != nil || len(resumed.Backlog) != 9_999 {
		t.Fatalf("retained cursor backlog=%d err=%v", len(resumed.Backlog), err)
	}
	resumed.Close()
	for cursor, code := range map[string]string{
		expiredCursor:  "realtime.cursor_expired",
		"rt:999999999": "realtime.cursor_unknown",
		"rt:invalid":   "realtime.cursor_unknown",
		"legacy-gone":  "realtime.catchup_limit",
	} {
		_, err := hub.Subscribe(ctx, cursor)
		var realtimeErr *realtime.Error
		if !errors.As(err, &realtimeErr) || realtimeErr.Code != code {
			t.Fatalf("cursor %q error = %#v", cursor, err)
		}
	}
}

func TestRealtimeLiveDrainUsesDurableRowIDOrderWhenLaterPublishWins(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	hub := realtime.New(app)
	subscription, err := hub.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	first := realtimeDataEvent(1)
	second := realtimeDataEvent(2)
	saveRealtimeOutboxEvent(t, app, first)
	saveRealtimeOutboxEvent(t, app, second)
	if err := hub.Publish(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := hub.Publish(ctx, first); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for len(ids) < 2 {
		select {
		case event := <-subscription.Events:
			ids = append(ids, event.ID)
		case <-time.After(2 * time.Second):
			t.Fatal("durable live drain timed out")
		}
	}
	if ids[0] != first.EventID || ids[1] != second.EventID {
		t.Fatalf("durable live order = %#v", ids)
	}
	select {
	case duplicate := <-subscription.Events:
		t.Fatalf("late publisher duplicated event %#v", duplicate)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRealtimeResumeAtWindowTailAdvancesLiveHighWater(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	hub := realtime.New(app)
	first := realtimeDataEvent(10)
	second := realtimeDataEvent(11)
	saveRealtimeOutboxEvent(t, app, first)
	initial, err := hub.Subscribe(ctx, "")
	if err != nil || len(initial.Backlog) != 1 {
		t.Fatalf("initial backlog=%d err=%v", len(initial.Backlog), err)
	}
	cursor := initial.Backlog[0].Cursor
	initial.Close()
	resumed, err := hub.Subscribe(ctx, cursor)
	if err != nil || len(resumed.Backlog) != 0 {
		t.Fatalf("resumed backlog=%d err=%v", len(resumed.Backlog), err)
	}
	defer resumed.Close()
	saveRealtimeOutboxEvent(t, app, second)
	if err := hub.Publish(ctx, second); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-resumed.Events:
		if event.ID != second.EventID {
			t.Fatalf("resumed live event = %#v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resumed live event timed out")
	}
	select {
	case duplicate := <-resumed.Events:
		t.Fatalf("resumed stream duplicated history %#v", duplicate)
	case <-time.After(100 * time.Millisecond):
	}
}
