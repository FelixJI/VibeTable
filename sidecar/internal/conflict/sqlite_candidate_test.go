package conflict

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestProjectSQLiteDatabaseBuildsWholeTableComponentsAndDependencies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE _collections (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			system BOOLEAN NOT NULL,
			fields JSON NOT NULL,
			viewQuery TEXT
		);
		INSERT INTO _collections(id,name,type,system,fields,viewQuery) VALUES
		('projects','projects','base',0,
		 '[{"name":"cover","type":"file"}]',NULL),
		('tasks','tasks','base',0,
		 '[{"name":"project","type":"relation","collectionId":"projects"}]',NULL),
		('project_summary','project_summary','view',0,
		 '[]','SELECT id FROM "projects" WHERE note = ''tasks'''),
		('task_summary','task_summary','view',0,
		 '[]','SELECT id FROM "tasks"');
		CREATE TABLE projects (id TEXT PRIMARY KEY, title TEXT, cover JSON);
		CREATE TABLE tasks (
			id TEXT PRIMARY KEY,
			project TEXT REFERENCES projects(id),
			title TEXT
		);
		INSERT INTO projects VALUES ('p1','tasks','["cover.png"]');
		INSERT INTO tasks VALUES ('t1','p1','Ship');
		CREATE VIEW project_titles AS SELECT id,title FROM projects;
	`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := ProjectSQLiteDatabase(
		context.Background(),
		raw,
		"sha256:database",
		map[string]string{
			"projects/p1/cover.png": "sha256:cover-v1",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Tables) != 4 {
		t.Fatalf("tables = %#v", projection.Tables)
	}
	project := projection.Tables["projects"]
	if project.DatabaseObjectID != "sha256:database" ||
		project.SchemaObjectID == "" ||
		project.RecordsObjectID == "" ||
		project.ViewsObjectID == "" ||
		project.AttachmentsObjectID == "" {
		t.Fatalf("project = %#v", project)
	}
	if len(projection.Edges["tasks"]) != 1 ||
		projection.Edges["tasks"][0] != "projects" {
		t.Fatalf("edges = %#v", projection.Edges)
	}
	if containsSQLiteDependency(
		projection.Edges["projects"], "tasks",
	) {
		t.Fatalf("business record text created dependency: %#v",
			projection.Edges,
		)
	}
	if !containsSQLiteDependency(
		projection.Edges["project_summary"], "projects",
	) {
		t.Fatalf("exact view query dependency missing: %#v",
			projection.Edges,
		)
	}
	if containsSQLiteDependency(
		projection.Edges["project_summary"], "tasks",
	) {
		t.Fatalf("SQL string literal created dependency: %#v",
			projection.Edges,
		)
	}
	if !containsSQLiteDependency(
		projection.Edges["task_summary"], "tasks",
	) {
		t.Fatalf("quoted SQL identifier dependency missing: %#v",
			projection.Edges,
		)
	}
	attachmentChanged, err := ProjectSQLiteDatabase(
		context.Background(),
		raw,
		"sha256:database",
		map[string]string{
			"projects/p1/cover.png": "sha256:cover-v2",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if attachmentChanged.Tables["projects"].AttachmentsObjectID ==
		project.AttachmentsObjectID {
		t.Fatal("attachment binary object change was not projected")
	}
	if attachmentChanged.Tables["tasks"].AttachmentsObjectID !=
		projection.Tables["tasks"].AttachmentsObjectID {
		t.Fatal("unrelated table attachment projection changed")
	}

	_, err = db.Exec("UPDATE projects SET title='Two' WHERE id='p1'")
	if err == nil {
		t.Fatal("closed database unexpectedly accepted update")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE projects SET title='Two' WHERE id='p1'"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	changed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	next, err := ProjectSQLiteDatabase(
		context.Background(), changed, "sha256:next-database",
	)
	if err != nil {
		t.Fatal(err)
	}
	if next.Tables["projects"].RecordsObjectID == project.RecordsObjectID ||
		next.Tables["tasks"].RecordsObjectID !=
			projection.Tables["tasks"].RecordsObjectID {
		t.Fatalf("component isolation failed: before=%#v after=%#v",
			projection.Tables, next.Tables,
		)
	}
}

func TestProjectSQLiteDatabaseFailsClosedWhenCollectionsShapeIsIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE _collections (name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectSQLiteDatabase(
		context.Background(), raw, "sha256:database",
	); err != ErrCandidateDatabaseInvalid {
		t.Fatalf("incomplete _collections accepted: %v", err)
	}
}

func TestTypedWorkspaceDependencyEdgesUsesBusinessMetadata(t *testing.T) {
	record := func(value string) json.RawMessage {
		return json.RawMessage(value)
	}
	collections := []sqliteCollectionProjection{
		{ID: "projects", Name: "projects"},
		{ID: "tasks", Name: "tasks"},
		{
			ID:   "metadata-tables",
			Name: "vibetable_tables",
			Records: []json.RawMessage{
				record(`{"table_id":"project-logical","collection_id":"projects"}`),
				record(`{"table_id":"task-logical","collection_id":"tasks"}`),
			},
		},
		{
			ID:   "metadata-relations",
			Name: "vibetable_relations",
			Records: []json.RawMessage{
				record(`{"source_table_id":"task-logical","target_table_id":"project-logical","junction_table_id":""}`),
			},
		},
		{
			ID:   "metadata-panels",
			Name: "vibetable_panels",
			Records: []json.RawMessage{
				record(`{"logical_id":"panel-1","payload_json":"{\"tableId\":\"task-logical\"}"}`),
				record(`{"logical_id":"panel-2","payload_json":"{\"tableId\":\"project-logical\"}"}`),
			},
		},
	}
	edges, err := typedWorkspaceDependencyEdges(collections)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQLiteDependency(edges["tasks"], "projects") ||
		!containsSQLiteDependency(edges["projects"], "tasks") {
		t.Fatalf("relation closure missing: %#v", edges)
	}
	if !containsSQLiteDependency(edges["metadata-panels"], "tasks") ||
		!containsSQLiteDependency(edges["tasks"], "metadata-panels") ||
		!containsSQLiteDependency(edges["metadata-panels"], "projects") {
		t.Fatalf("panel query closure missing: %#v", edges)
	}
	if !containsSQLiteDependency(
		edges["metadata-relations"], "tasks",
	) || !containsSQLiteDependency(
		edges["metadata-relations"], "projects",
	) {
		t.Fatalf("metadata table closure missing: %#v", edges)
	}
}

func TestTypedWorkspaceDependencyEdgesFailsClosedOnDanglingReference(t *testing.T) {
	_, err := typedWorkspaceDependencyEdges([]sqliteCollectionProjection{
		{ID: "tasks", Name: "tasks"},
		{
			ID:   "metadata-panels",
			Name: "vibetable_panels",
			Records: []json.RawMessage{
				json.RawMessage(`{"logical_id":"panel-1","payload_json":"{\"tableId\":\"missing\"}"}`),
			},
		},
	})
	if err != ErrDependencyIncomplete {
		t.Fatalf("dangling metadata reference accepted: %v", err)
	}
}

func TestTypedWorkspaceDependencyEdgesRejectsLegacyMetadataProjection(t *testing.T) {
	_, err := typedWorkspaceDependencyEdges([]sqliteCollectionProjection{
		{ID: "tasks", Name: "tasks"},
		{
			ID:   "metadata-panels",
			Name: "vibetable_panels",
			Records: []json.RawMessage{
				json.RawMessage(`{"panel_id":"panel-1","query_json":"{\"tableId\":\"tasks\"}"}`),
			},
		},
	})
	if err != ErrDependencyIncomplete {
		t.Fatalf("legacy metadata projection accepted: %v", err)
	}
}

func TestTypedWorkspaceDependencyEdgesAcceptsCanonicalNullPayload(t *testing.T) {
	edges, err := typedWorkspaceDependencyEdges([]sqliteCollectionProjection{
		{
			ID:   "metadata-settings",
			Name: "vibetable_shared_settings",
			Records: []json.RawMessage{
				json.RawMessage(`{"logical_id":"empty-setting","payload_json":null}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("canonical null payload rejected: %v", err)
	}
	if dependencies := edges["metadata-settings"]; len(dependencies) != 0 {
		t.Fatalf("canonical null payload dependencies = %#v", dependencies)
	}
}

func containsSQLiteDependency(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
