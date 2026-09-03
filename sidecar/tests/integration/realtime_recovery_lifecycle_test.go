package integration_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
)

func TestRealtimeRecoveryRejectsCorruptTasksInColdResumedAndLiveDelivery(t *testing.T) {
	for _, property := range []string{"state", "taskType", "occurredAt"} {
		t.Run(property, func(t *testing.T) {
			app := bootstrapApp(t, queryTempDir(t))
			defer resetApp(t, app)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hub := realtime.New(app)
			if err := hub.PersistTaskChanged(ctx, app, jobs.Snapshot{JobID: "before-corruption", State: "complete"}); err != nil {
				t.Fatal(err)
			}
			initial, err := hub.Subscribe(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			cursor := initial.Backlog[0].Cursor
			initial.Close()
			if err := hub.PersistTaskChanged(ctx, app, jobs.Snapshot{JobID: "corrupt-after-cursor", State: "complete"}); err != nil {
				t.Fatal(err)
			}
			if _, err := app.DB().NewQuery(`UPDATE vibetable_outbox
		SET payload_json=json_set(payload_json, {:property}, 'invalid')
		WHERE rowid=(SELECT max(rowid) FROM vibetable_outbox)`).Bind(dbx.Params{
				"property": "$." + property,
			}).Execute(); err != nil {
				t.Fatal(err)
			}
			requireRecoveryError(t, hub, ctx, "", "realtime.storage_corrupt")
			requireRecoveryError(t, hub, ctx, cursor, "realtime.storage_corrupt")
			var recoveryError *realtime.Error
			err = hub.Publish(ctx, mutation.DataChangedEvent{})
			if !errors.As(err, &recoveryError) || recoveryError.Code != "realtime.storage_corrupt" {
				t.Fatalf("live corruption error=%v", err)
			}
		})
	}
}

func TestRealtimeRecoveryCancellationReleasesHubWhileWriterIsHeld(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	liveCtx, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	hub := realtime.New(app)
	existing, err := hub.Subscribe(liveCtx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()

	releaseWriter := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWriter) }) }
	writerEntered := make(chan struct{})
	writerExited := make(chan struct{})
	writerResult := make(chan error, 1)
	var recoveryExited, closeExited, publishExited chan struct{}
	defer func() {
		release()
		for _, exited := range []chan struct{}{writerExited, recoveryExited, closeExited, publishExited} {
			if exited == nil {
				continue
			}
			select {
			case <-exited:
			case <-time.After(2 * time.Second):
				t.Error("test goroutine did not stop after releasing the writer")
			}
		}
	}()
	go func() {
		defer close(writerExited)
		writerResult <- app.RunInTransaction(func(_ core.App) error {
			close(writerEntered)
			<-releaseWriter
			return nil
		})
	}()
	select {
	case <-writerEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not acquire the real PocketBase transaction")
	}
	writerPool := app.NonconcurrentDB().(*dbx.DB).DB()
	waitCount := writerPool.Stats().WaitCount
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	recoveryResult := make(chan error, 1)
	recoveryExited = make(chan struct{})
	go func() {
		defer close(recoveryExited)
		subscription, err := hub.SubscribeRecoverable(requestCtx, "")
		if subscription != nil {
			subscription.Close()
		}
		recoveryResult <- err
	}()
	// SQL pool evidence proves recovery reached Begin's wait, without sleep.
	until := time.Now().Add(2 * time.Second)
	for writerPool.Stats().WaitCount == waitCount {
		if time.Now().After(until) {
			t.Fatal("recovery never reached the occupied writer connection")
		}
		runtime.Gosched()
	}
	cancelRequest()
	closeExited = make(chan struct{})
	go func() {
		defer close(closeExited)
		existing.Close()
	}()
	publishResult := make(chan error, 1)
	publishExited = make(chan struct{})
	go func() {
		defer close(publishExited)
		publishResult <- hub.Publish(liveCtx, mutation.DataChangedEvent{})
	}()
	var observeRecovery <-chan error = recoveryResult
	var observeClose <-chan struct{} = closeExited
	var observePublish <-chan error = publishResult
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for observeRecovery != nil || observeClose != nil || observePublish != nil {
		select {
		case err := <-observeRecovery:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("cancelled recovery error=%v", err)
			}
			observeRecovery = nil
		case <-observeClose:
			observeClose = nil
		case err := <-observePublish:
			if err != nil {
				t.Errorf("existing live publish failed: %v", err)
			}
			observePublish = nil
		case <-deadline.C:
			t.Errorf("cancelled recovery retained the hub lock: recoveryPending=%t closePending=%t publishPending=%t",
				observeRecovery != nil, observeClose != nil, observePublish != nil)
			release()
			return
		}
	}
	release()
	if err := <-writerResult; err != nil {
		t.Fatal(err)
	}
}
