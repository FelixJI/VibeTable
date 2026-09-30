package schemaexecution_test

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestDescribeCancelsWhileWaitingForSnapshotConnection(t *testing.T) {
	app := newTestApp(t)
	table := createTestExecutionTable(t, context.Background(), app, "取消快照", "cancel-snapshot")
	db := app.NonconcurrentDB().(*dbx.DB)
	held, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	var readErr error
	defer func() {
		_ = held.Rollback()
		<-finished
	}()
	before := db.DB().Stats().WaitCount
	go func() {
		_, readErr = schemaexecution.Describe(ctx, app, table.TableID)
		close(finished)
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for db.DB().Stats().WaitCount == before {
		select {
		case <-deadline.C:
			t.Fatal("Describe did not reach the occupied snapshot connection")
		default:
			runtime.Gosched()
		}
	}
	cancel()
	select {
	case <-finished:
		if !errors.Is(readErr, context.Canceled) {
			t.Fatalf("Describe error = %v; want context.Canceled", readErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Describe cancellation waited for the held transaction to release")
	}
}
