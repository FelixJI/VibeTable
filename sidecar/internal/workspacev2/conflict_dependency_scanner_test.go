package workspacev2

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	_ "modernc.org/sqlite"
)

func TestProductionConflictDependencyScannerIncludesIsolatedFileTableAndSettingsNodes(
	t *testing.T,
) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "candidate.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE _collections (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			system BOOLEAN NOT NULL,
			fields JSON NOT NULL
		);
		INSERT INTO _collections(id,name,type,system,fields)
		VALUES ('notes','notes','base',0,'[]');
		CREATE TABLE notes (id TEXT PRIMARY KEY, title TEXT);
		INSERT INTO notes VALUES ('n1','One');
	`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repository := objectrepo.NewMemory()
	authority := objectrepo.Authority{
		WorkspaceID: "workspace-1",
		FenceEpoch:  1,
		ClaimID:     "claim-1",
	}
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	receipt, err := repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: authority,
		Objects: []objectrepo.ObjectInput{
			{Name: "database", Content: database},
			{
				Name:    "settings",
				Content: []byte(`{"shared":true}`),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	databaseID := string(receipt.Objects["database"])
	settingsID := string(receipt.Objects["settings"])
	projection, err := conflictresolution.ProjectSQLiteDatabase(
		ctx, database, databaseID,
	)
	if err != nil {
		t.Fatal(err)
	}
	candidate := conflictresolution.Candidate{
		SnapshotID:               "snapshot-1",
		BusinessDatabaseObjectID: databaseID,
		Settings: conflictresolution.SettingsState{
			ObjectID: settingsID,
		},
		Files: map[string]conflictresolution.FileState{
			"document-1": {
				DocumentID: "document-1",
				Path:       "notes/one.md",
				ContentID:  "sha256:content",
			},
		},
		Tables: projection.Tables,
	}
	graph, err := (productionConflictDependencyScanner{
		repository: repository,
	}).ScanConflictDependencies(ctx, candidate, candidate, candidate)
	if err != nil {
		t.Fatal(err)
	}
	for _, itemID := range []string{
		"document-1",
		"notes",
		conflictresolution.WorkspaceSettingsItemID,
	} {
		dependencies, ok := graph.Edges[itemID]
		if !ok {
			t.Fatalf("missing graph node %q: %#v", itemID, graph.Edges)
		}
		if len(dependencies) != 0 {
			t.Fatalf("isolated node %q = %#v", itemID, dependencies)
		}
	}
}
