package replica

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type syncerFunc func(context.Context, SyncTask) error

func (function syncerFunc) Sync(
	ctx context.Context,
	task SyncTask,
) error {
	return function(ctx, task)
}

func TestPersistentQueueRetriesIdempotentlyAcrossRestart(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	path := sqliteTestPath(t, "retry.db")
	queue, err := OpenPersistentQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	queue.now = func() time.Time { return now }
	var (
		journalMode string
		synchronous int
	)
	if err := queue.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if err := queue.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" || synchronous != 2 {
		t.Fatalf(
			"queue durability mode = %s/%d, want wal/FULL",
			journalMode,
			synchronous,
		)
	}
	task := SyncTask{
		TaskID: "task-1", WorkspaceID: "workspace-1",
		SnapshotID: "snapshot-1",
	}
	if err := queue.Enqueue(task); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(task); err != nil {
		t.Fatalf("idempotent enqueue failed: %v", err)
	}
	if err := queue.Enqueue(SyncTask{
		TaskID: "task-1", WorkspaceID: "workspace-2",
		SnapshotID: "snapshot-1",
	}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("conflicting idempotency key accepted: %v", err)
	}
	transient := errors.New("network unavailable")
	if err := queue.Drain(
		context.Background(),
		syncerFunc(func(context.Context, SyncTask) error {
			return transient
		}),
	); err != nil {
		t.Fatal(err)
	}
	tasks, err := queue.List()
	if err != nil || len(tasks) != 1 ||
		tasks[0].Attempts != 1 ||
		tasks[0].LastError != transient.Error() ||
		!tasks[0].NextAttempt.Equal(now.Add(2*time.Second)) {
		t.Fatalf("retry state = %#v, %v", tasks, err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenPersistentQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return now.Add(3 * time.Second) }
	var calls atomic.Int32
	if err := reopened.Drain(
		context.Background(),
		syncerFunc(func(_ context.Context, actual SyncTask) error {
			calls.Add(1)
			if actual.TaskID != task.TaskID {
				t.Fatalf("unexpected task: %#v", actual)
			}
			return nil
		}),
	); err != nil {
		t.Fatal(err)
	}
	tasks, err = reopened.List()
	if err != nil || calls.Load() != 1 ||
		len(tasks) != 1 || !tasks[0].Completed {
		t.Fatalf("completed state = %#v calls=%d err=%v", tasks, calls.Load(), err)
	}
}

func TestQueueDoesNotHoldMutexDuringNetworkIO(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	queue, err := OpenPersistentQueue(sqliteTestPath(t, "retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	queue.now = func() time.Time { return now }
	if err := queue.Enqueue(SyncTask{
		TaskID: "task-1", WorkspaceID: "workspace-1",
		SnapshotID: "snapshot-1",
	}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	drained := make(chan error, 1)
	var once sync.Once
	go func() {
		drained <- queue.Drain(
			context.Background(),
			syncerFunc(func(context.Context, SyncTask) error {
				once.Do(func() {
					close(entered)
					<-release
				})
				return nil
			}),
		)
	}()
	<-entered
	enqueued := make(chan error, 1)
	go func() {
		enqueued <- queue.Enqueue(SyncTask{
			TaskID: "task-2", WorkspaceID: "workspace-1",
			SnapshotID: "snapshot-2",
		})
	}()
	select {
	case err := <-enqueued:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("enqueue blocked behind network I/O")
	}
	close(release)
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
}

func TestPersistentQueueRecoversExpiredInflightLeaseAfterRestart(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	path := sqliteTestPath(t, "retry.db")
	queue, err := OpenPersistentQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	queue.now = func() time.Time { return now }
	queue.leaseDuration = time.Second
	if err := queue.Enqueue(SyncTask{
		TaskID: "task-1", WorkspaceID: "workspace-1",
		SnapshotID: "snapshot-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := queue.claimNext(
		context.Background(), now, now.Add(time.Second),
	); err != nil || !found {
		t.Fatalf("claim = %v, %v", found, err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenPersistentQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return now.Add(2 * time.Second) }
	var calls atomic.Int32
	if err := reopened.Drain(
		context.Background(),
		syncerFunc(func(context.Context, SyncTask) error {
			calls.Add(1)
			return nil
		}),
	); err != nil {
		t.Fatal(err)
	}
	tasks, err := reopened.List()
	if err != nil || calls.Load() != 1 ||
		len(tasks) != 1 || !tasks[0].Completed {
		t.Fatalf("recovered tasks = %#v calls=%d err=%v", tasks, calls.Load(), err)
	}
}

func TestPersistentQueueConcurrentDrainersClaimTaskOnce(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	path := sqliteTestPath(t, "retry.db")
	left, err := OpenPersistentQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := OpenPersistentQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	left.now = func() time.Time { return now }
	right.now = func() time.Time { return now }
	if err := left.Enqueue(SyncTask{
		TaskID: "task-1", WorkspaceID: "workspace-1",
		SnapshotID: "snapshot-1",
	}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	syncer := syncerFunc(func(context.Context, SyncTask) error {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return nil
	})
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, queue := range []*Queue{left, right} {
		go func(queue *Queue) {
			<-start
			results <- queue.Drain(context.Background(), syncer)
		}(queue)
	}
	close(start)
	for index := 0; index < 2; index++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("network sync called %d times", calls.Load())
	}
}
