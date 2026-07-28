package replica

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

var (
	ErrTaskInvalid   = errors.New("replica.task_invalid")
	ErrTaskConflict  = errors.New("replica.task_conflict")
	ErrTaskLeaseLost = errors.New("replica.task_lease_lost")
	ErrQueueClosed   = errors.New("replica.queue_closed")
)

type SyncTask struct {
	TaskID      string    `json:"taskId"`
	WorkspaceID string    `json:"workspaceId"`
	SnapshotID  string    `json:"snapshotId"`
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"nextAttempt"`
	LastError   string    `json:"lastError,omitempty"`
	Completed   bool      `json:"completed"`
	InFlight    bool      `json:"inFlight"`
}

type Syncer interface {
	Sync(context.Context, SyncTask) error
}

type queueClaim struct {
	task       SyncTask
	leaseToken string
}

type Queue struct {
	mu       sync.Mutex
	tasks    map[string]SyncTask
	inflight map[string]string
	db       *sql.DB
	closed   atomic.Bool

	now           func() time.Time
	leaseDuration time.Duration
}

func NewQueue() *Queue {
	return &Queue{
		tasks:         map[string]SyncTask{},
		inflight:      map[string]string{},
		now:           func() time.Time { return time.Now().UTC() },
		leaseDuration: time.Minute,
	}
}

func OpenPersistentQueue(path string) (*Queue, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("replica.queue_path_required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		PRAGMA busy_timeout=5000;
		CREATE TABLE IF NOT EXISTS replica_sync_tasks (
			task_id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			snapshot_id TEXT NOT NULL,
			attempts INTEGER NOT NULL,
			next_attempt INTEGER NOT NULL,
			last_error TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN (
				'queued', 'inflight', 'completed'
			)),
			lease_token TEXT NOT NULL,
			lease_until INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS replica_sync_tasks_due
		ON replica_sync_tasks(state, next_attempt, lease_until, task_id);
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Queue{
		db:            db,
		now:           func() time.Time { return time.Now().UTC() },
		leaseDuration: time.Minute,
	}, nil
}

func (queue *Queue) Enqueue(task SyncTask) error {
	if queue == nil {
		return ErrTaskInvalid
	}
	if queue.closed.Load() {
		return ErrQueueClosed
	}
	if err := validateTask(task); err != nil {
		return ErrTaskInvalid
	}
	if task.NextAttempt.IsZero() {
		task.NextAttempt = queue.now().UTC()
	} else {
		task.NextAttempt = task.NextAttempt.UTC()
	}
	task.Completed = false
	task.InFlight = false
	task.LastError = ""
	if queue.db != nil {
		return queue.enqueuePersistent(context.Background(), task)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if existing, found := queue.tasks[task.TaskID]; found {
		if existing.WorkspaceID == task.WorkspaceID &&
			existing.SnapshotID == task.SnapshotID {
			return nil
		}
		return ErrTaskConflict
	}
	queue.tasks[task.TaskID] = task
	return nil
}

func (queue *Queue) Drain(ctx context.Context, syncer Syncer) error {
	if queue == nil || syncer == nil {
		return errors.New("replica.syncer_required")
	}
	if queue.closed.Load() {
		return ErrQueueClosed
	}
	for {
		now := queue.now().UTC()
		claim, found, err := queue.claimNext(
			ctx, now, now.Add(queue.leaseDuration),
		)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}

		// The durable task lease is already committed. No Queue mutex or
		// SQLite transaction is held while network I/O runs.
		syncErr := syncer.Sync(ctx, claim.task)
		finishedAt := queue.now().UTC()
		persistCtx := context.WithoutCancel(ctx)
		if syncErr != nil {
			if err := queue.failClaim(
				persistCtx, claim, syncErr, finishedAt,
			); err != nil {
				return err
			}
			continue
		}
		if err := queue.completeClaim(persistCtx, claim); err != nil {
			return err
		}
	}
}

func (queue *Queue) List(
	contexts ...context.Context,
) ([]SyncTask, error) {
	if queue == nil {
		return nil, errors.New("replica.queue_required")
	}
	if queue.closed.Load() {
		return nil, ErrQueueClosed
	}
	ctx := context.Background()
	if len(contexts) > 0 && contexts[0] != nil {
		ctx = contexts[0]
	}
	if queue.db != nil {
		return queue.listPersistent(ctx)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	result := make([]SyncTask, 0, len(queue.tasks))
	for id, task := range queue.tasks {
		task.InFlight = queue.inflight[id] != ""
		result = append(result, task)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].TaskID < result[right].TaskID
	})
	return result, nil
}

func (queue *Queue) Close() error {
	if queue == nil {
		return nil
	}
	if queue.db == nil {
		queue.closed.Store(true)
		return nil
	}
	_, checkpointErr := queue.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	closeErr := queue.db.Close()
	queue.db = nil
	queue.closed.Store(true)
	return errors.Join(checkpointErr, closeErr)
}

func (queue *Queue) claimNext(
	ctx context.Context,
	now time.Time,
	leaseUntil time.Time,
) (queueClaim, bool, error) {
	token := uuid.NewString()
	if queue.db != nil {
		var (
			task       SyncTask
			nextUnix   int64
			completed  int
			leaseToken string
		)
		err := queue.db.QueryRowContext(ctx, `
			UPDATE replica_sync_tasks
			SET state = 'inflight', lease_token = ?, lease_until = ?
			WHERE task_id = (
				SELECT task_id FROM replica_sync_tasks
				WHERE (
					state = 'queued' AND next_attempt <= ?
				) OR (
					state = 'inflight' AND lease_until <= ?
				)
				ORDER BY next_attempt, task_id LIMIT 1
			)
			RETURNING task_id, workspace_id, snapshot_id, attempts,
			          next_attempt, last_error,
			          CASE WHEN state = 'completed' THEN 1 ELSE 0 END,
			          lease_token`,
			token,
			leaseUntil.UnixNano(),
			now.UnixNano(),
			now.UnixNano(),
		).Scan(
			&task.TaskID,
			&task.WorkspaceID,
			&task.SnapshotID,
			&task.Attempts,
			&nextUnix,
			&task.LastError,
			&completed,
			&leaseToken,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return queueClaim{}, false, nil
		}
		if err != nil {
			return queueClaim{}, false, err
		}
		task.NextAttempt = time.Unix(0, nextUnix).UTC()
		if validateTask(task) != nil ||
			leaseToken != token || completed != 0 {
			return queueClaim{}, false, errors.Join(
				ErrTaskLeaseLost,
			)
		}
		task.InFlight = true
		return queueClaim{task: task, leaseToken: token}, true, nil
	}

	queue.mu.Lock()
	defer queue.mu.Unlock()
	ids := make([]string, 0, len(queue.tasks))
	for id := range queue.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		task := queue.tasks[id]
		if task.Completed || task.NextAttempt.After(now) ||
			queue.inflight[id] != "" {
			continue
		}
		queue.inflight[id] = token
		task.InFlight = true
		return queueClaim{task: task, leaseToken: token}, true, nil
	}
	return queueClaim{}, false, nil
}

func (queue *Queue) completeClaim(
	ctx context.Context,
	claim queueClaim,
) error {
	if queue.db != nil {
		result, err := queue.db.ExecContext(ctx, `
			UPDATE replica_sync_tasks
			SET state = 'completed', last_error = '',
			    lease_token = '', lease_until = 0
			WHERE task_id = ? AND state = 'inflight'
			      AND lease_token = ?`,
			claim.task.TaskID,
			claim.leaseToken,
		)
		return requireOneTask(result, err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.inflight[claim.task.TaskID] != claim.leaseToken {
		return ErrTaskLeaseLost
	}
	task := queue.tasks[claim.task.TaskID]
	task.Completed = true
	task.InFlight = false
	task.LastError = ""
	queue.tasks[claim.task.TaskID] = task
	delete(queue.inflight, claim.task.TaskID)
	return nil
}

func (queue *Queue) failClaim(
	ctx context.Context,
	claim queueClaim,
	syncErr error,
	now time.Time,
) error {
	attempts := claim.task.Attempts + 1
	delay := time.Second * time.Duration(1<<min(attempts, 10))
	nextAttempt := now.Add(delay)
	if queue.db != nil {
		result, err := queue.db.ExecContext(ctx, `
			UPDATE replica_sync_tasks
			SET state = 'queued', attempts = ?, next_attempt = ?,
			    last_error = ?, lease_token = '', lease_until = 0
			WHERE task_id = ? AND state = 'inflight'
			      AND lease_token = ?`,
			attempts,
			nextAttempt.UnixNano(),
			syncErr.Error(),
			claim.task.TaskID,
			claim.leaseToken,
		)
		return requireOneTask(result, err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.inflight[claim.task.TaskID] != claim.leaseToken {
		return ErrTaskLeaseLost
	}
	task := queue.tasks[claim.task.TaskID]
	task.Attempts = attempts
	task.NextAttempt = nextAttempt
	task.LastError = syncErr.Error()
	task.InFlight = false
	queue.tasks[claim.task.TaskID] = task
	delete(queue.inflight, claim.task.TaskID)
	return nil
}

func (queue *Queue) enqueuePersistent(
	ctx context.Context,
	task SyncTask,
) error {
	_, err := queue.db.ExecContext(ctx, `
		INSERT INTO replica_sync_tasks (
			task_id, workspace_id, snapshot_id, attempts,
			next_attempt, last_error, state, lease_token, lease_until
		) VALUES (?, ?, ?, ?, ?, '', 'queued', '', 0)
		ON CONFLICT(task_id) DO NOTHING`,
		task.TaskID,
		task.WorkspaceID,
		task.SnapshotID,
		task.Attempts,
		task.NextAttempt.UnixNano(),
	)
	if err != nil {
		return err
	}
	var workspaceID, snapshotID string
	if err := queue.db.QueryRowContext(ctx, `
		SELECT workspace_id, snapshot_id FROM replica_sync_tasks
		WHERE task_id = ?`,
		task.TaskID,
	).Scan(&workspaceID, &snapshotID); err != nil {
		return err
	}
	if workspaceID != task.WorkspaceID ||
		snapshotID != task.SnapshotID {
		return ErrTaskConflict
	}
	return nil
}

func (queue *Queue) listPersistent(
	ctx context.Context,
) ([]SyncTask, error) {
	rows, err := queue.db.QueryContext(ctx, `
		SELECT task_id, workspace_id, snapshot_id, attempts,
		       next_attempt, last_error, state
		FROM replica_sync_tasks ORDER BY task_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []SyncTask
	for rows.Next() {
		var (
			task     SyncTask
			nextUnix int64
			state    string
		)
		if err := rows.Scan(
			&task.TaskID,
			&task.WorkspaceID,
			&task.SnapshotID,
			&task.Attempts,
			&nextUnix,
			&task.LastError,
			&state,
		); err != nil {
			return nil, err
		}
		task.NextAttempt = time.Unix(0, nextUnix).UTC()
		if err := validateTask(task); err != nil {
			return nil, err
		}
		switch state {
		case "queued":
		case "inflight":
			task.InFlight = true
		case "completed":
			task.Completed = true
		default:
			return nil, ErrTaskInvalid
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func validateTask(task SyncTask) error {
	if strings.TrimSpace(task.TaskID) == "" ||
		strings.TrimSpace(task.WorkspaceID) == "" ||
		strings.TrimSpace(task.SnapshotID) == "" ||
		task.Attempts < 0 {
		return ErrTaskInvalid
	}
	return nil
}

func requireOneTask(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrTaskLeaseLost
	}
	return nil
}
