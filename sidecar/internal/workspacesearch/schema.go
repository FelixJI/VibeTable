package workspacesearch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// searchProjectionSchemaVersion owns the derived text columns, their FTS
// tables, and the triggers that keep those indexes synchronized.
const searchProjectionSchemaVersion = 1

func initializeSearchSchema(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS search_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`INSERT OR IGNORE INTO search_meta(key,value) VALUES ('generation','0')`,
		`INSERT OR IGNORE INTO search_meta(key,value) VALUES ('business_outbox_rowid','0')`,
		`INSERT OR IGNORE INTO search_meta(key,value) VALUES ('file_head_revision','0')`,
		`INSERT OR IGNORE INTO search_meta(key,value) VALUES ('mutation_revision','0')`,
		`INSERT OR IGNORE INTO search_meta(key,value) VALUES ('rebuild_required','0')`,
		`CREATE TABLE IF NOT EXISTS search_documents (
			rowid INTEGER PRIMARY KEY,
			hit_id TEXT NOT NULL UNIQUE,
			kind TEXT NOT NULL,
			canonical_id TEXT NOT NULL,
			title TEXT NOT NULL,
			display_text TEXT NOT NULL DEFAULT '',
			projected_title TEXT NOT NULL,
			normalized_text TEXT NOT NULL,
			source_revision TEXT NOT NULL,
			revision_time TEXT NOT NULL,
			table_id TEXT,
			record_id TEXT,
			field_id TEXT,
			document_id TEXT,
			mime_type TEXT,
			extension TEXT,
			size_bytes INTEGER,
			status TEXT NOT NULL,
			is_current INTEGER NOT NULL CHECK(is_current IN (0,1)),
			metadata_json TEXT NOT NULL,
			open_target_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_search_identity
			ON search_documents(kind, canonical_id, is_current)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}

	version, exists, err := projectionSchemaVersion(ctx, tx)
	if err != nil {
		return err
	}
	if exists && version == searchProjectionSchemaVersion {
		columns, err := searchDocumentColumns(ctx, tx)
		if err != nil {
			return err
		}
		if !columns["display_text"] || !columns["projected_title"] {
			return errors.New("workspace_search.projection_schema_corrupt")
		}
		return nil
	}
	if exists && version != 0 {
		return errors.New("workspace_search.projection_schema_unsupported")
	}
	return migrateSearchProjection(ctx, tx)
}

func projectionSchemaVersion(ctx context.Context, tx *sql.Tx) (int, bool, error) {
	var raw string
	err := tx.QueryRowContext(
		ctx,
		`SELECT value FROM search_meta WHERE key='projection_schema_version'`,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	version, err := strconv.Atoi(raw)
	if err != nil || version < 0 {
		return 0, true, errors.New("workspace_search.projection_schema_corrupt")
	}
	return version, true, nil
}

func migrateSearchProjection(ctx context.Context, tx *sql.Tx) error {
	for _, name := range []string{
		"search_documents_ai", "search_documents_ad", "search_documents_au",
	} {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+name); err != nil {
			return err
		}
	}
	columns, err := searchDocumentColumns(ctx, tx)
	if err != nil {
		return err
	}
	if !columns["display_text"] {
		if _, err := tx.ExecContext(ctx,
			`ALTER TABLE search_documents ADD COLUMN display_text TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return err
		}
	}
	if !columns["projected_title"] {
		if _, err := tx.ExecContext(ctx,
			`ALTER TABLE search_documents ADD COLUMN projected_title TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return err
		}
	}
	if err := backfillSearchProjection(ctx, tx); err != nil {
		return err
	}
	for _, table := range []string{"search_terms", "search_cjk3"} {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+table); err != nil {
			return err
		}
	}
	for _, statement := range projectionSchemaStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	for _, table := range []string{"search_terms", "search_cjk3"} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+`
			(rowid,projected_title,normalized_text)
			SELECT rowid,projected_title,normalized_text FROM search_documents`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(
			ctx, `INSERT INTO `+table+`(`+table+`) VALUES('integrity-check')`,
		); err != nil {
			return fmt.Errorf("verify %s: %w", table, err)
		}
	}
	var documents int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM search_documents`).Scan(&documents); err != nil {
		return err
	}
	if documents > 0 {
		// A changed derived corpus invalidates cursors from the previous projection.
		if err := bumpGeneration(ctx, tx); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO search_meta(key,value) VALUES('projection_schema_version',?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		strconv.Itoa(searchProjectionSchemaVersion),
	)
	return err
}

func searchDocumentColumns(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(search_documents)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(
			&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey,
		); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func backfillSearchProjection(ctx context.Context, tx *sql.Tx) error {
	var afterRowID int64
	firstBatch := true
	for {
		query := `SELECT rowid,title,display_text
			FROM search_documents ORDER BY rowid LIMIT 256`
		args := []any{}
		if !firstBatch {
			query = `SELECT rowid,title,display_text
				FROM search_documents WHERE rowid>? ORDER BY rowid LIMIT 256`
			args = append(args, afterRowID)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		type document struct {
			rowID       int64
			title, body string
		}
		documents := make([]document, 0, 256)
		for rows.Next() {
			var value document
			if err := rows.Scan(&value.rowID, &value.title, &value.body); err != nil {
				rows.Close()
				return err
			}
			documents = append(documents, value)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(documents) == 0 {
			return nil
		}
		for _, value := range documents {
			if _, err := tx.ExecContext(ctx, `UPDATE search_documents
				SET projected_title=?, normalized_text=? WHERE rowid=?`,
				projectSearchText(value.title),
				projectSearchText(value.title+"\n"+value.body),
				value.rowID,
			); err != nil {
				return err
			}
			afterRowID = value.rowID
		}
		firstBatch = false
	}
}

func projectionSchemaStatements() []string {
	return []string{
		`CREATE VIRTUAL TABLE search_terms USING fts5(
			projected_title, normalized_text,
			content='search_documents', content_rowid='rowid',
			tokenize='unicode61 remove_diacritics 2', detail=full
		)`,
		`CREATE VIRTUAL TABLE search_cjk3 USING fts5(
			projected_title, normalized_text,
			content='search_documents', content_rowid='rowid',
			tokenize='trigram case_sensitive 0', detail=full
		)`,
		`CREATE TRIGGER search_documents_ai AFTER INSERT ON search_documents BEGIN
			INSERT INTO search_terms(rowid,projected_title,normalized_text)
			VALUES (new.rowid,new.projected_title,new.normalized_text);
			INSERT INTO search_cjk3(rowid,projected_title,normalized_text)
			VALUES (new.rowid,new.projected_title,new.normalized_text);
		END`,
		`CREATE TRIGGER search_documents_ad AFTER DELETE ON search_documents BEGIN
			INSERT INTO search_terms(search_terms,rowid,projected_title,normalized_text)
			VALUES ('delete',old.rowid,old.projected_title,old.normalized_text);
			INSERT INTO search_cjk3(search_cjk3,rowid,projected_title,normalized_text)
			VALUES ('delete',old.rowid,old.projected_title,old.normalized_text);
		END`,
		`CREATE TRIGGER search_documents_au AFTER UPDATE ON search_documents BEGIN
			INSERT INTO search_terms(search_terms,rowid,projected_title,normalized_text)
			VALUES ('delete',old.rowid,old.projected_title,old.normalized_text);
			INSERT INTO search_terms(rowid,projected_title,normalized_text)
			VALUES (new.rowid,new.projected_title,new.normalized_text);
			INSERT INTO search_cjk3(search_cjk3,rowid,projected_title,normalized_text)
			VALUES ('delete',old.rowid,old.projected_title,old.normalized_text);
			INSERT INTO search_cjk3(rowid,projected_title,normalized_text)
			VALUES (new.rowid,new.projected_title,new.normalized_text);
		END`,
	}
}
