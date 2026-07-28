package workspacedb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestValidateSnapshotAcceptsRequiredPocketBaseSchema(t *testing.T) {
	raw := snapshotDatabaseFixture(t, true)
	if err := ValidateSnapshot(
		context.Background(),
		raw,
		SupportedBusinessSchemaVersion,
	); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSnapshotRejectsShapeOnlyCollectionTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE _collections (x TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSnapshot(
		context.Background(),
		raw,
		SupportedBusinessSchemaVersion,
	); !errors.Is(err, ErrSnapshotDatabaseInvalid) {
		t.Fatalf("invalid schema error = %v", err)
	}
}

func TestValidateSnapshotRejectsUnsupportedBusinessSchema(t *testing.T) {
	if err := ValidateSnapshot(
		context.Background(),
		snapshotDatabaseFixture(t, true),
		SupportedBusinessSchemaVersion+1,
	); !errors.Is(err, ErrBusinessSchemaUnsupported) {
		t.Fatalf("unsupported schema error = %v", err)
	}
}

func TestValidateSnapshotRequiresUniqueCollectionNames(t *testing.T) {
	if err := ValidateSnapshot(
		context.Background(),
		snapshotDatabaseFixture(t, false),
		SupportedBusinessSchemaVersion,
	); !errors.Is(err, ErrSnapshotDatabaseInvalid) {
		t.Fatalf("missing unique index error = %v", err)
	}
}

func TestValidateSnapshotRejectsMissingOrIncompleteAuditOutbox(t *testing.T) {
	tests := []struct {
		name     string
		mutation string
	}{
		{
			name:     "missing",
			mutation: `DROP TABLE vibetable_audit_outbox`,
		},
		{
			name: "missing production column",
			mutation: `
				DROP TABLE vibetable_audit_outbox;
				CREATE TABLE vibetable_audit_outbox (
					event_id TEXT PRIMARY KEY,
					source_epoch TEXT NOT NULL,
					source_sequence INTEGER NOT NULL,
					mutation_identity TEXT NOT NULL,
					payload_json BLOB NOT NULL,
					occurred_at TEXT NOT NULL,
					status TEXT NOT NULL,
					attempts INTEGER NOT NULL,
					UNIQUE(source_epoch, source_sequence)
				);`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := snapshotDatabaseFixtureWithMutation(
				t,
				true,
				test.mutation,
			)
			if err := ValidateSnapshot(
				context.Background(),
				raw,
				SupportedBusinessSchemaVersion,
			); !errors.Is(err, ErrSnapshotDatabaseInvalid) {
				t.Fatalf("invalid audit outbox error = %v", err)
			}
		})
	}
}

func TestValidateSnapshotRejectsMalformedPrimaryKeys(t *testing.T) {
	tests := []struct {
		name     string
		mutation string
	}{
		{
			name: "_collections id",
			mutation: `
				ALTER TABLE _collections RENAME TO old_collections;
				CREATE TABLE _collections (
					id TEXT NOT NULL,
					system BOOLEAN NOT NULL,
					type TEXT NOT NULL,
					name TEXT NOT NULL UNIQUE,
					fields JSON NOT NULL,
					indexes JSON NOT NULL,
					options JSON NOT NULL,
					created TEXT NOT NULL,
					updated TEXT NOT NULL
				);
				DROP TABLE old_collections;`,
		},
		{
			name: "_migrations file",
			mutation: `
				DROP TABLE _migrations;
				CREATE TABLE _migrations (
					file TEXT NOT NULL,
					applied INTEGER NOT NULL
				);`,
		},
		{
			name: "receipt mutation_revision",
			mutation: `
				DROP TABLE workspace_v2_mutation_receipts;
				CREATE TABLE workspace_v2_mutation_receipts (
					mutation_revision INTEGER NOT NULL,
					workspace_id TEXT NOT NULL,
					session_epoch INTEGER NOT NULL,
					fence_epoch INTEGER NOT NULL,
					claim_id TEXT NOT NULL,
					kind TEXT NOT NULL,
					identity TEXT NOT NULL,
					audit_source_sequence INTEGER NOT NULL,
					committed_at TEXT NOT NULL,
					UNIQUE(
						workspace_id, session_epoch, fence_epoch,
						claim_id, kind, identity
					)
				);`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := snapshotDatabaseFixtureWithMutation(
				t,
				true,
				test.mutation,
			)
			if err := ValidateSnapshot(
				context.Background(),
				raw,
				SupportedBusinessSchemaVersion,
			); !errors.Is(err, ErrSnapshotDatabaseInvalid) {
				t.Fatalf("invalid primary key error = %v", err)
			}
		})
	}
}

func TestValidateSnapshotRejectsMissingProductionUniqueConstraints(
	t *testing.T,
) {
	tests := []struct {
		name     string
		mutation string
	}{
		{
			name: "receipt identity",
			mutation: `
				DROP TABLE workspace_v2_mutation_receipts;
				CREATE TABLE workspace_v2_mutation_receipts (
					mutation_revision INTEGER PRIMARY KEY,
					workspace_id TEXT NOT NULL,
					session_epoch INTEGER NOT NULL,
					fence_epoch INTEGER NOT NULL,
					claim_id TEXT NOT NULL,
					kind TEXT NOT NULL,
					identity TEXT NOT NULL,
					audit_source_sequence INTEGER NOT NULL,
					committed_at TEXT NOT NULL
				);`,
		},
		{
			name: "audit event",
			mutation: `
				DROP TABLE vibetable_audit_outbox;
				CREATE TABLE vibetable_audit_outbox (
					event_id TEXT NOT NULL,
					source_epoch TEXT NOT NULL,
					source_sequence INTEGER NOT NULL,
					mutation_identity TEXT NOT NULL,
					payload_hash TEXT NOT NULL,
					payload_json BLOB NOT NULL,
					occurred_at TEXT NOT NULL,
					status TEXT NOT NULL,
					attempts INTEGER NOT NULL,
					UNIQUE(source_epoch, source_sequence)
				);`,
		},
		{
			name: "audit source sequence",
			mutation: `
				DROP TABLE vibetable_audit_outbox;
				CREATE TABLE vibetable_audit_outbox (
					event_id TEXT PRIMARY KEY,
					source_epoch TEXT NOT NULL,
					source_sequence INTEGER NOT NULL,
					mutation_identity TEXT NOT NULL,
					payload_hash TEXT NOT NULL,
					payload_json BLOB NOT NULL,
					occurred_at TEXT NOT NULL,
					status TEXT NOT NULL,
					attempts INTEGER NOT NULL
				);`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := snapshotDatabaseFixtureWithMutation(
				t,
				true,
				test.mutation,
			)
			if err := ValidateSnapshot(
				context.Background(),
				raw,
				SupportedBusinessSchemaVersion,
			); !errors.Is(err, ErrSnapshotDatabaseInvalid) {
				t.Fatalf("invalid unique constraint error = %v", err)
			}
		})
	}
}

func snapshotDatabaseFixture(t *testing.T, uniqueCollectionName bool) []byte {
	t.Helper()
	return snapshotDatabaseFixtureWithMutation(
		t,
		uniqueCollectionName,
		"",
	)
}

func snapshotDatabaseFixtureWithMutation(
	t *testing.T,
	uniqueCollectionName bool,
	mutation string,
) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	unique := ""
	if uniqueCollectionName {
		unique = ", UNIQUE(name)"
	}
	_, err = database.Exec(`
		PRAGMA foreign_keys = ON;
		CREATE TABLE _collections (
			id TEXT PRIMARY KEY,
			system BOOLEAN NOT NULL,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			fields JSON NOT NULL,
			indexes JSON NOT NULL,
			options JSON NOT NULL,
			created TEXT NOT NULL,
			updated TEXT NOT NULL
			` + unique + `
		);
		CREATE TABLE _migrations (
			file TEXT PRIMARY KEY,
			applied INTEGER NOT NULL
		);
		CREATE TABLE _vibetable_sidecar_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated TEXT NOT NULL
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
			UNIQUE(
				workspace_id, session_epoch, fence_epoch,
				claim_id, kind, identity
			)
		);
	`)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if mutation != "" {
		if _, err := database.Exec(mutation); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
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
