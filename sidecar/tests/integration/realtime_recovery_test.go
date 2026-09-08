package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
)

func TestRealtimeRecoveryKeepsResumedAuthoritySeparateFromPastTerminal(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture := createFormulaBackfillFixture(t, ctx, app, "恢复 📦", "recovery_resumed")
	hub := realtime.New(app)
	service := jobs.New(app, mutation.New(app, mutation.MetadataSchemaSource{}), jobs.WithTaskPublisher(hub))
	defer service.Shutdown()
	started, err := service.StartFormulaBackfill(ctx, fixture.definition.Snapshot.TableID, fixture.definition.Snapshot.SchemaRevision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Cancel(ctx, started.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(ctx, started.JobID); err != nil {
		t.Fatal(err)
	}
	recovered, err := hub.SubscribeRecoverable(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	snapshot := recoveredProjection(t, recovered)
	if len(snapshot.ActiveFormulaTasks) != 1 || snapshot.ActiveFormulaTasks[0].TaskID != started.JobID || snapshot.ActiveFormulaTasks[0].State != "pending" {
		t.Fatalf("resumed authoritative activity = %+v", snapshot.ActiveFormulaTasks)
	}
	if len(snapshot.TerminalNotifications) != 1 || snapshot.TerminalNotifications[0].TaskID != started.JobID || snapshot.TerminalNotifications[0].State != "cancelled" {
		t.Fatalf("historical terminal notices = %+v", snapshot.TerminalNotifications)
	}
	legacy, err := hub.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	found := false
	for _, event := range legacy.Backlog {
		if event.ID == snapshot.TerminalNotifications[0].EventID {
			found = true
			var original realtime.TaskChangedEvent
			if err := json.Unmarshal(event.Payload, &original); err != nil {
				t.Fatal(err)
			}
			if original.OccurredAt != snapshot.TerminalNotifications[0].OccurredAt {
				t.Fatal("recovery rewrote the historical notification time")
			}
		}
	}
	if !found {
		t.Fatal("recovery fabricated a terminal event identity")
	}
}

func recoveredProjection(t *testing.T, subscription *realtime.Subscription) realtime.RecoverySnapshot {
	t.Helper()
	if len(subscription.Backlog) != 1 || subscription.Backlog[0].Topic != "realtime.recovered" {
		t.Fatalf("recovery backlog = %+v", subscription.Backlog)
	}
	var snapshot realtime.RecoverySnapshot
	if err := json.Unmarshal(subscription.Backlog[0].Payload, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestRealtimeRecoveryKeepsItsWatermarkPrivateAndResumesFromTheEmptyAnchor(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := realtime.New(app)
	empty, err := hub.SubscribeRecoverable(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	anchor := empty.Backlog[0].Cursor
	empty.Close()
	if anchor != "rt:0" {
		t.Fatalf("empty anchor=%q", anchor)
	}
	resumed, err := hub.SubscribeRecoverable(ctx, anchor)
	if err != nil || len(resumed.Backlog) != 0 {
		t.Fatalf("empty anchor could not resume: err=%v", err)
	}
	defer resumed.Close()
	legacy, err := hub.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	// The durable commit precedes live publication; joining must not steal its drain.
	terminal := jobs.Snapshot{JobID: "delayed-terminal", State: "cancelled"}
	if err := hub.PersistTaskChanged(ctx, app, terminal); err != nil {
		t.Fatal(err)
	}
	joining, err := hub.SubscribeRecoverable(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer joining.Close()
	snapshot := recoveredProjection(t, joining)
	if len(snapshot.TerminalNotifications) != 1 {
		t.Fatalf("terminal count=%d", len(snapshot.TerminalNotifications))
	}
	highWater := joining.Backlog[0].Cursor
	if err := hub.PublishTaskChanged(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	for label, subscription := range map[string]*realtime.Subscription{"legacy": legacy, "resumed": resumed} {
		select {
		case event := <-subscription.Events:
			if event.ID != snapshot.TerminalNotifications[0].EventID || event.Cursor != highWater {
				t.Fatalf("%s missed original commit: %+v", label, event)
			}
		default:
			t.Fatalf("%s lost delayed publication", label)
		}
	}
	select {
	case event := <-joining.Events:
		t.Fatalf("recovery replayed its own H: %+v", event)
	default:
	}
	data := realtimeDataEvent(1)
	saveRealtimeOutboxEvent(t, app, data)
	if err := hub.Publish(ctx, data); err != nil {
		t.Fatal(err)
	}
	for label, subscription := range map[string]*realtime.Subscription{"legacy": legacy, "resumed": resumed, "joining": joining} {
		select {
		case event := <-subscription.Events:
			if event.ID != data.EventID {
				t.Fatalf("%s received %q instead of next durable event", label, event.ID)
			}
		default:
			t.Fatalf("%s lost post-H commit", label)
		}
	}
	catchup, err := hub.SubscribeRecoverable(ctx, highWater)
	if err != nil {
		t.Fatal(err)
	}
	defer catchup.Close()
	if len(catchup.Backlog) != 1 || catchup.Backlog[0].ID != data.EventID {
		t.Fatalf("retained resume did not return just the post-H event: %+v", catchup.Backlog)
	}
	requireRecoveryError(t, hub, ctx, "rt:999", "realtime.cursor_future")
	saveRealtimeOutboxEvent(t, app, realtimeDataEvent(3))
	if _, err := app.DB().NewQuery(`DELETE FROM vibetable_outbox WHERE rowid=2`).Execute(); err != nil {
		t.Fatal(err)
	}
	requireRecoveryError(t, hub, ctx, "rt:2", "realtime.cursor_unknown")
	cancel()
	select {
	case _, open := <-joining.Events:
		if open {
			t.Fatal("cancelled recovery remained live")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled recovery did not close")
	}
	if subscription, err := hub.SubscribeRecoverable(ctx, highWater); subscription != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request admitted: subscription=%v err=%v", subscription, err)
	}
}

func TestRealtimeRecoveryResumesTheUndeliveredTailAfterQueueOverflow(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := realtime.New(app)
	subscription, err := hub.SubscribeRecoverable(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	// Fill the existing 256-event live queue without consuming it.
	for index := range 257 {
		event := realtimeDataEvent(index)
		saveRealtimeOutboxEvent(t, app, event)
		if err := hub.Publish(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	lastCursor, delivered := "", 0
	for {
		select {
		case event, open := <-subscription.Events:
			if open {
				lastCursor = event.Cursor
				delivered++
				continue
			}
			if delivered != 256 {
				t.Fatalf("overflow delivered %d events", delivered)
			}
			resumed, err := hub.SubscribeRecoverable(ctx, lastCursor)
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close()
			if len(resumed.Backlog) != 1 || resumed.Backlog[0].ID != realtimeDataEvent(256).EventID {
				t.Fatalf("overflow lost the undelivered durable tail: %+v", resumed.Backlog)
			}
			return
		case <-time.After(2 * time.Second):
			t.Fatal("overflow left the subscription open")
		}
	}
}
