package workspacev2

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSnapshotImportPlanBindingMigratesLegacyStateFailClosed(
	t *testing.T,
) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workspace-v2.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		CREATE TABLE snapshot_import_plans (
			plan_id TEXT PRIMARY KEY,
			source_path TEXT NOT NULL,
			source_hash TEXT NOT NULL,
			expires_at TEXT NOT NULL
		);
		INSERT INTO snapshot_import_plans (
			plan_id, source_path, source_hash, expires_at
		) VALUES (
			'legacy-plan', 'C:\legacy.vtsnapshot', 'sha256:legacy',
			'2026-07-29T00:00:00Z'
		);
	`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	legacy, err := store.snapshotImportPlan(ctx, "legacy-plan")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.SourceSize != -1 || legacy.SourceIdentity != "" {
		t.Fatalf("legacy binding=%#v", legacy)
	}

	bound := snapshotImportPlan{
		PlanID:         "bound-plan",
		SourcePath:     `C:\bound.vtsnapshot`,
		SourceHash:     "sha256:bound",
		SourceSize:     42,
		SourceIdentity: "windows:volume:file",
		ExpiresAt:      "2026-07-29T01:00:00Z",
	}
	if err := store.putSnapshotImportPlan(ctx, bound); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.snapshotImportPlan(ctx, bound.PlanID)
	if err != nil || loaded != bound {
		t.Fatalf("loaded=%#v want=%#v err=%v", loaded, bound, err)
	}
}
