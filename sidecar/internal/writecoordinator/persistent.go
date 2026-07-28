package writecoordinator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type persistedState struct {
	Token
	MutationRevision uint64
	SnapshotSequence uint64
	HighWatermark
}

type persistentStore struct {
	db      *sql.DB
	faultMu sync.RWMutex
	fault   PersistenceFaultInjector
}

// ReadPersistentMutationRevision reads the replica-required high-watermark
// from an existing coordination database without creating or migrating it.
// Committed revisions come from coordination_state. A prepared intent is
// conservatively included because its external authoritative apply may have
// committed before the process failed to finish the coordinator transaction.
func ReadPersistentMutationRevision(
	ctx context.Context,
	databasePath string,
	workspaceID string,
) (uint64, error) {
	if strings.TrimSpace(databasePath) == "" ||
		strings.TrimSpace(workspaceID) == "" {
		return 0, ErrInvalidIdentity
	}
	absolute, err := filepath.Abs(databasePath)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return 0, fmt.Errorf("open coordination high-watermark: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("workspace.write.database_invalid")
	}
	stagedPath, cleanup, err := stageCoordinationDatabaseForRead(absolute)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	db, err := sql.Open("sqlite", stagedPath)
	if err != nil {
		return 0, err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, `PRAGMA query_only=ON`); err != nil {
		return 0, err
	}

	var persistedWorkspaceID string
	var committedRevision uint64
	var preparedRevision uint64
	err = db.QueryRowContext(ctx, `
		SELECT state.workspace_id,
		       state.mutation_revision,
		       COALESCE(MAX(intent.mutation_revision), 0)
		FROM coordination_state AS state
		LEFT JOIN mutation_intents AS intent
		  ON intent.state = 'prepared'
		WHERE state.singleton = 1
		GROUP BY state.workspace_id, state.mutation_revision`,
	).Scan(
		&persistedWorkspaceID,
		&committedRevision,
		&preparedRevision,
	)
	if err != nil {
		return 0, fmt.Errorf("read coordination high-watermark: %w", err)
	}
	if persistedWorkspaceID != workspaceID {
		return 0, ErrInvalidIdentity
	}
	if preparedRevision > committedRevision {
		return preparedRevision, nil
	}
	return committedRevision, nil
}

func stageCoordinationDatabaseForRead(
	sourcePath string,
) (string, func(), error) {
	directory, err := os.MkdirTemp("", "vibetable-coordination-read-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	targetPath := filepath.Join(directory, "coordination.db")
	for _, suffix := range []string{"", "-wal"} {
		source, err := os.Open(sourcePath + suffix)
		if errors.Is(err, os.ErrNotExist) && suffix != "" {
			continue
		}
		if err != nil {
			cleanup()
			return "", nil, err
		}
		target, targetErr := os.OpenFile(
			targetPath+suffix,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0o600,
		)
		if targetErr == nil {
			_, targetErr = io.Copy(target, source)
		}
		closeErr := source.Close()
		if target != nil {
			closeErr = errors.Join(closeErr, target.Close())
		}
		if err := errors.Join(targetErr, closeErr); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return targetPath, cleanup, nil
}

func (store *persistentStore) setFaultInjector(injector PersistenceFaultInjector) {
	store.faultMu.Lock()
	defer store.faultMu.Unlock()
	store.fault = injector
}

func (store *persistentStore) inject(point PersistenceFaultPoint) error {
	store.faultMu.RLock()
	injector := store.fault
	store.faultMu.RUnlock()
	if injector == nil {
		return nil
	}
	return injector(point)
}

func openPersistentStore(
	path string,
	initial Token,
) (*persistentStore, persistedState, uint64, error) {
	if path == "" {
		return nil, persistedState{}, 0, errors.New("workspace.write.database_path_required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, persistedState{}, 0, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, persistedState{}, 0, err
	}
	db.SetMaxOpenConns(1)
	store := &persistentStore{db: db}
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		PRAGMA foreign_keys=ON;
		CREATE TABLE IF NOT EXISTS coordination_state (
			singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
			workspace_id TEXT NOT NULL,
			session_epoch INTEGER NOT NULL,
			fence_epoch INTEGER NOT NULL,
			claim_id TEXT NOT NULL,
			mutation_revision INTEGER NOT NULL,
			snapshot_sequence INTEGER NOT NULL,
			audit_source_epoch TEXT NOT NULL,
			audit_source_sequence INTEGER NOT NULL,
			audit_chain_hash TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS mutation_intents (
			mutation_revision INTEGER PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			session_epoch INTEGER NOT NULL,
			fence_epoch INTEGER NOT NULL,
			claim_id TEXT NOT NULL,
			state TEXT NOT NULL,
			prepared_at TEXT NOT NULL,
			finished_at TEXT
		);
		CREATE TABLE IF NOT EXISTS capture_intents (
			snapshot_sequence INTEGER PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			session_epoch INTEGER NOT NULL,
			fence_epoch INTEGER NOT NULL,
			claim_id TEXT NOT NULL,
			mutation_revision INTEGER NOT NULL,
			state TEXT NOT NULL,
			database_view TEXT NOT NULL,
			topology_root TEXT NOT NULL,
			file_root TEXT NOT NULL,
			audit_anchor TEXT NOT NULL,
			prepared_at TEXT NOT NULL,
			captured_at TEXT
		);
	`); err != nil {
		_ = db.Close()
		return nil, persistedState{}, 0, fmt.Errorf("initialize coordination database: %w", err)
	}
	state, found, err := store.loadState(context.Background())
	if err != nil {
		_ = db.Close()
		return nil, persistedState{}, 0, err
	}
	if !found {
		state = persistedState{Token: initial}
		if err := store.insertInitial(context.Background(), state); err != nil {
			_ = db.Close()
			return nil, persistedState{}, 0, err
		}
	} else if !tokensEqual(state.Token, initial) {
		_ = db.Close()
		return nil, persistedState{}, 0, ErrStaleToken
	}
	var prepared uint64
	err = db.QueryRow(
		`SELECT COALESCE(MAX(mutation_revision), 0)
		 FROM mutation_intents WHERE state = 'prepared'`,
	).Scan(&prepared)
	if err != nil {
		_ = db.Close()
		return nil, persistedState{}, 0, err
	}
	if _, err := db.Exec(
		`UPDATE capture_intents SET state = 'abandoned', captured_at = ?
		 WHERE state = 'prepared'`,
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		_ = db.Close()
		return nil, persistedState{}, 0, err
	}
	return store, state, prepared, nil
}

func (store *persistentStore) insertInitial(ctx context.Context, state persistedState) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO coordination_state (
			singleton, workspace_id, session_epoch, fence_epoch, claim_id,
			mutation_revision, snapshot_sequence, audit_source_epoch,
			audit_source_sequence, audit_chain_hash
		) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		state.WorkspaceID,
		state.SessionEpoch,
		state.FenceEpoch,
		state.ClaimID,
		state.MutationRevision,
		state.SnapshotSequence,
		state.SourceEpoch,
		state.SourceSequence,
		state.ChainHash,
	)
	return err
}

func (store *persistentStore) loadState(ctx context.Context) (persistedState, bool, error) {
	var state persistedState
	err := store.db.QueryRowContext(ctx, `
		SELECT workspace_id, session_epoch, fence_epoch, claim_id,
		       mutation_revision, snapshot_sequence, audit_source_epoch,
		       audit_source_sequence, audit_chain_hash
		FROM coordination_state WHERE singleton = 1`,
	).Scan(
		&state.WorkspaceID,
		&state.SessionEpoch,
		&state.FenceEpoch,
		&state.ClaimID,
		&state.MutationRevision,
		&state.SnapshotSequence,
		&state.SourceEpoch,
		&state.SourceSequence,
		&state.ChainHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return persistedState{}, false, nil
	}
	return state, err == nil, err
}

func (store *persistentStore) persistState(ctx context.Context, state persistedState) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE coordination_state
		SET workspace_id = ?, session_epoch = ?, fence_epoch = ?, claim_id = ?,
		    mutation_revision = ?, snapshot_sequence = ?, audit_source_epoch = ?,
		    audit_source_sequence = ?, audit_chain_hash = ?
		WHERE singleton = 1`,
		state.WorkspaceID,
		state.SessionEpoch,
		state.FenceEpoch,
		state.ClaimID,
		state.MutationRevision,
		state.SnapshotSequence,
		state.SourceEpoch,
		state.SourceSequence,
		state.ChainHash,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return errors.Join(ErrRecoveryRequired, err)
	}
	return nil
}

func (store *persistentStore) prepareMutation(
	ctx context.Context,
	token Token,
	revision uint64,
	at time.Time,
) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO mutation_intents (
			mutation_revision, workspace_id, session_epoch, fence_epoch,
			claim_id, state, prepared_at
		) VALUES (?, ?, ?, ?, ?, 'prepared', ?)
		ON CONFLICT(mutation_revision) DO UPDATE SET
			workspace_id = excluded.workspace_id,
			session_epoch = excluded.session_epoch,
			fence_epoch = excluded.fence_epoch,
			claim_id = excluded.claim_id,
			state = 'prepared',
			prepared_at = excluded.prepared_at,
			finished_at = NULL
		WHERE mutation_intents.state = 'failed'`,
		revision,
		token.WorkspaceID,
		token.SessionEpoch,
		token.FenceEpoch,
		token.ClaimID,
		at.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (store *persistentStore) finishMutation(
	ctx context.Context,
	token Token,
	revision uint64,
	snapshotSequence uint64,
	committed bool,
	at time.Time,
) error {
	if committed {
		if err := store.inject(FaultBeforeFinishCommittedMutation); err != nil {
			return err
		}
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	state := "failed"
	if committed {
		state = "committed"
		result, err := transaction.ExecContext(ctx, `
			UPDATE coordination_state SET mutation_revision = ?
			WHERE singleton = 1 AND workspace_id = ? AND session_epoch = ?
			      AND fence_epoch = ? AND claim_id = ?
			      AND snapshot_sequence = ? AND mutation_revision < ?`,
			revision,
			token.WorkspaceID,
			token.SessionEpoch,
			token.FenceEpoch,
			token.ClaimID,
			snapshotSequence,
			revision,
		)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil || affected != 1 {
			return errors.Join(ErrStaleToken, err)
		}
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE mutation_intents SET state = ?, finished_at = ?
		WHERE mutation_revision = ? AND state = 'prepared'`,
		state,
		at.UTC().Format(time.RFC3339Nano),
		revision,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return errors.Join(ErrRecoveryRequired, err)
	}
	return transaction.Commit()
}

func (store *persistentStore) prepareCapture(
	ctx context.Context,
	intent CaptureIntent,
	highWatermark HighWatermark,
	at time.Time,
) error {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(ctx, `
		UPDATE coordination_state SET snapshot_sequence = ?
		WHERE singleton = 1 AND workspace_id = ? AND session_epoch = ?
		      AND fence_epoch = ? AND claim_id = ?
		      AND mutation_revision = ? AND snapshot_sequence = ?`,
		intent.SnapshotSequence,
		intent.Token.WorkspaceID,
		intent.Token.SessionEpoch,
		intent.Token.FenceEpoch,
		intent.Token.ClaimID,
		intent.MutationRevision,
		intent.SnapshotSequence-1,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return errors.Join(ErrStaleToken, err)
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO capture_intents (
			snapshot_sequence, workspace_id, session_epoch, fence_epoch,
			claim_id, mutation_revision, state, database_view,
			topology_root, file_root, audit_anchor, prepared_at
		) VALUES (?, ?, ?, ?, ?, ?, 'prepared', '', '', '', ?, ?)`,
		intent.SnapshotSequence,
		intent.Token.WorkspaceID,
		intent.Token.SessionEpoch,
		intent.Token.FenceEpoch,
		intent.Token.ClaimID,
		intent.MutationRevision,
		highWatermark.ChainHash,
		at.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}
	return transaction.Commit()
}

func (store *persistentStore) finishCapture(
	ctx context.Context,
	intent CaptureIntent,
	roots FrozenRoots,
	state string,
	at time.Time,
) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE capture_intents
		SET state = ?, database_view = ?, topology_root = ?, file_root = ?,
		    audit_anchor = ?, captured_at = ?
		WHERE snapshot_sequence = ? AND workspace_id = ?
		      AND session_epoch = ? AND fence_epoch = ? AND claim_id = ?
		      AND mutation_revision = ? AND state = 'prepared'`,
		state,
		roots.DatabaseView,
		string(roots.TopologyRoot),
		string(roots.FileRoot),
		roots.AuditAnchor,
		at.UTC().Format(time.RFC3339Nano),
		intent.SnapshotSequence,
		intent.Token.WorkspaceID,
		intent.Token.SessionEpoch,
		intent.Token.FenceEpoch,
		intent.Token.ClaimID,
		intent.MutationRevision,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return errors.Join(ErrRecoveryRequired, err)
	}
	return nil
}

func (store *persistentStore) captureIntent(
	ctx context.Context,
	snapshotSequence uint64,
) (CaptureIntentRecord, error) {
	var (
		record     CaptureIntentRecord
		preparedAt string
		capturedAt sql.NullString
	)
	err := store.db.QueryRowContext(ctx, `
		SELECT workspace_id, session_epoch, fence_epoch, claim_id,
		       mutation_revision, snapshot_sequence, state, database_view,
		       topology_root, file_root, audit_anchor, prepared_at, captured_at
		FROM capture_intents WHERE snapshot_sequence = ?`,
		snapshotSequence,
	).Scan(
		&record.Token.WorkspaceID,
		&record.Token.SessionEpoch,
		&record.Token.FenceEpoch,
		&record.Token.ClaimID,
		&record.MutationRevision,
		&record.SnapshotSequence,
		&record.State,
		&record.DatabaseView,
		&record.TopologyRoot,
		&record.FileRoot,
		&record.AuditAnchor,
		&preparedAt,
		&capturedAt,
	)
	if err != nil {
		return CaptureIntentRecord{}, err
	}
	record.PreparedAt, err = time.Parse(time.RFC3339Nano, preparedAt)
	if err != nil {
		return CaptureIntentRecord{}, err
	}
	if capturedAt.Valid {
		record.CapturedAt, err = time.Parse(time.RFC3339Nano, capturedAt.String)
	}
	return record, err
}

func (store *persistentStore) close() error {
	if store == nil || store.db == nil {
		return nil
	}
	_, checkpointErr := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	_, journalErr := store.db.Exec("PRAGMA journal_mode=DELETE")
	closeErr := store.db.Close()
	store.db = nil
	return errors.Join(checkpointErr, journalErr, closeErr)
}
