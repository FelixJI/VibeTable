package snapshot

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func snapshotDatabaseForTest(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`
		CREATE TABLE _collections (
			id TEXT PRIMARY KEY,
			system BOOLEAN NOT NULL,
			type TEXT NOT NULL,
			name TEXT NOT NULL UNIQUE,
			fields JSON NOT NULL,
			indexes JSON NOT NULL,
			options JSON NOT NULL,
			created TEXT NOT NULL,
			updated TEXT NOT NULL
		);
		CREATE TABLE _migrations (
			file TEXT PRIMARY KEY,
			applied INTEGER NOT NULL
		);
		CREATE TABLE workspace_v2_mutation_receipts (
			mutation_revision INTEGER PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			session_epoch INTEGER NOT NULL,
			fence_epoch INTEGER NOT NULL,
			claim_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			identity TEXT NOT NULL,
			audit_source_sequence INTEGER NOT NULL,
			committed_at TEXT NOT NULL,
			UNIQUE (
				workspace_id, session_epoch, fence_epoch,
				claim_id, kind, identity
			)
		);
		CREATE TABLE vibetable_audit_outbox (
			event_id TEXT PRIMARY KEY,
			source_epoch TEXT NOT NULL,
			source_sequence INTEGER NOT NULL,
			mutation_identity TEXT NOT NULL,
			payload_hash TEXT NOT NULL,
			payload_json BLOB NOT NULL,
			occurred_at TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL,
			UNIQUE(source_epoch, source_sequence)
		);
	`)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
