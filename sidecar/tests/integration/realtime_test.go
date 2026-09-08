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
	table, title := createV2IntegrationTableWithField(
		t, ctx, app, "Realtime notes", "Title", "op_realtime_notes",
	)
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
		table.TableID,
		title.SchemaRevision,
		"realtime-insert",
		mutation.Operation{
			Kind:     mutation.OperationInsert,
			RecordID: &recordID,
			Values: map[string]any{
				title.Definition.Identity.PhysicalName: "first",
			},
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

func TestRealtimeCatchupDoesNotSkipPendingPublicationForExistingSubscriber(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := realtime.New(app)
	existing, err := hub.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	running := jobs.Snapshot{
		JobID: "job-subscriber-catchup", State: "running",
		Progress: jobs.Progress{Completed: 1, Total: 2},
	}
	// Commit before the original publisher gets to drain, as in a jobs transaction.
	if err := hub.PersistTaskChanged(ctx, app, running); err != nil {
		t.Fatal(err)
	}
	joining, err := hub.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer joining.Close()
	if len(joining.Backlog) != 1 {
		t.Fatalf("joining backlog = %#v", joining.Backlog)
	}
	if err := hub.PublishTaskChanged(ctx, running); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-existing.Events:
		if event.ID != joining.Backlog[0].ID {
			t.Fatalf("existing subscriber received %q instead of committed event", event.ID)
		}
	default:
		t.Fatal("joining catchup skipped committed task for existing subscriber")
	}
	select {
	case duplicate := <-joining.Events:
		t.Fatalf("joining subscriber duplicated its backlog: %#v", duplicate)
	default:
	}
	completed := running
	completed.State = "complete"
	if err := hub.PublishTaskChanged(ctx, completed); err != nil {
		t.Fatal(err)
	}
	for label, subscription := range map[string]*realtime.Subscription{
		"existing": existing, "joining": joining,
	} {
		select {
		case event := <-subscription.Events:
			var snapshot realtime.TaskChangedEvent
			if err := json.Unmarshal(event.Payload, &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.TaskID != running.JobID || snapshot.State != "succeeded" {
				t.Fatalf("%s terminal snapshot = %#v", label, snapshot)
			}
		default:
			t.Fatalf("%s subscriber missed terminal task snapshot", label)
		}
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
	fixture := createFormulaBackfillFixture(t, ctx, app, "Cold queued task", "cold_realtime")
	service := jobs.New(app, mutation.New(app, mutation.MetadataSchemaSource{}), jobs.WithTaskPublisher(hub))
	defer service.Shutdown()
	queued, err := service.StartFormulaBackfill(ctx, fixture.definition.Snapshot.TableID, fixture.definition.Snapshot.SchemaRevision)
	if err != nil {
		t.Fatal(err)
	}
	// Build the pre-existing window without replaying the retention scan for
	// every seed row. Restore the exact installed trigger in the same transaction
	// before exercising all boundary writes through PocketBase Save.
	var retentionSQL string
	if err := app.DB().NewQuery(`SELECT sql FROM sqlite_master
		WHERE type = 'trigger' AND name = 'vibetable_outbox_retain_latest'`).Row(&retentionSQL); err != nil {
		t.Fatal(err)
	}
	if retentionSQL == "" {
		t.Fatal("production retention trigger is missing")
	}
	events := make([]mutation.DataChangedEvent, 10_000)
	for index := range events {
		events[index] = realtimeDataEvent(index + 1)
	}
	raw, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.RunInTransaction(func(txApp core.App) error {
		if _, err := txApp.DB().NewQuery("DROP TRIGGER vibetable_outbox_retain_latest").Execute(); err != nil {
			return err
		}
		if _, err := txApp.DB().NewQuery(`
			INSERT INTO vibetable_outbox (id, event_id, topic, payload_json, status, attempts)
			SELECT printf('retention%06d', CAST(key AS INTEGER) + 1),
				json_extract(value, '$.eventId'), 'data.changed', value, 'pending', 0
			FROM json_each({:events}) ORDER BY CAST(key AS INTEGER)
		`).Bind(map[string]any{"events": string(raw)}).Execute(); err != nil {
			return err
		}
		_, err := txApp.DB().NewQuery(retentionSQL).Execute()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for index := 10_001; index <= 10_005; index++ {
		saveRealtimeOutboxEvent(t, app, realtimeDataEvent(index))
	}
	// Hub catchup also limits results, so query the authority itself to prove
	// the real trigger removed old rows rather than hiding them in a page.
	var retained int
	if err := app.DB().NewQuery("SELECT COUNT(*) FROM vibetable_outbox").Row(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 10_000 {
		t.Fatalf("retained authority rows = %d", retained)
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
	recovered, err := hub.SubscribeRecoverable(ctx, expiredCursor)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	projection := recoveredProjection(t, recovered)
	if len(projection.ActiveFormulaTasks) != 1 || projection.ActiveFormulaTasks[0].TaskID != queued.JobID ||
		len(projection.TerminalNotifications) != 0 {
		t.Fatalf("cold activity was not recovered from authority: %+v", projection)
	}
	if recovered.Backlog[0].Cursor != backlog.Backlog[len(backlog.Backlog)-1].Cursor {
		t.Fatal("recovery cursor does not describe the retained snapshot")
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

func TestRealtimeCatchupKeepsPendingDurableEventsForExistingSubscribers(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := realtime.New(app)
	existing, err := hub.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()

	event := realtimeDataEvent(1)
	saveRealtimeOutboxEvent(t, app, event)
	joining, err := hub.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer joining.Close()
	if len(joining.Backlog) != 1 || joining.Backlog[0].ID != event.EventID {
		t.Fatalf("joining backlog = %#v", joining.Backlog)
	}

	if err := hub.Publish(ctx, event); err != nil {
		t.Fatal(err)
	}
	select {
	case delivered := <-existing.Events:
		if delivered.ID != event.EventID {
			t.Fatalf("existing event = %#v", delivered)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("existing subscriber missed pending durable event")
	}
	select {
	case duplicate := <-joining.Events:
		t.Fatalf("joining subscriber duplicated replayed event %#v", duplicate)
	default:
	}

	next := realtimeDataEvent(2)
	saveRealtimeOutboxEvent(t, app, next)
	if err := hub.Publish(ctx, next); err != nil {
		t.Fatal(err)
	}
	for name, subscription := range map[string]*realtime.Subscription{
		"existing": existing,
		"joining":  joining,
	} {
		select {
		case delivered := <-subscription.Events:
			if delivered.ID != next.EventID {
				t.Fatalf("%s next event = %#v", name, delivered)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s subscriber missed next durable event", name)
		}
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
