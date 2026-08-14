package workspacesearch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"modernc.org/sqlite"

	contracts "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
)

const ContractVersion = "1.0"

const restoreRebuildRequiredCode = "workspace_search.restore_rebuild_required"

type SourceDocument struct {
	Kind           string
	CanonicalID    string
	Title          string
	Body           string
	SourceRevision string
	RevisionTime   string
	TableID        *string
	RecordID       *string
	FieldID        *string
	DocumentID     *string
	MIMEType       *string
	Extension      *string
	SizeBytes      *int64
	Status         string
	Current        bool
	Metadata       []contracts.SearchMetadataItem
	OpenTarget     contracts.SearchOpenTarget
}

// HitFromSource creates the renderer-safe identity/open target projection used
// when a stale hit is re-read directly from its authority before navigation.
// Query-specific score, snippet, and highlights intentionally remain empty.
func HitFromSource(source SourceDocument) contracts.SearchHit {
	return contracts.SearchHit{
		ContractVersion: ContractVersion,
		HitId:           sourceHitID(source),
		Kind:            source.Kind,
		CanonicalId:     source.CanonicalID,
		Title:           source.Title,
		Highlights:      []string{},
		SourceRevision:  source.SourceRevision,
		RevisionTime:    source.RevisionTime,
		Metadata:        source.Metadata,
		OpenTarget:      source.OpenTarget,
	}
}

type Result struct {
	Hits       []contracts.SearchHit `json:"hits"`
	NextCursor *string               `json:"nextCursor"`
	Generation int64                 `json:"generation"`
}

type Error struct {
	Code string
	Path string
}

func (err *Error) Error() string { return err.Code }

type Engine struct {
	db *sql.DB
}

// ProjectionCheckpoint binds one promoted search generation to the exact
// durable source tails observed before collection started. Events committed
// after those tails are replayed by the projection worker on its next pass.
type ProjectionCheckpoint struct {
	BusinessOutboxRowID int64
	FileHeadRevision    uint64
	MutationRevision    uint64
}

type RecordProjectionKey struct {
	TableID  string
	RecordID string
}

type ProjectionChanges struct {
	Records   []RecordProjectionKey
	Documents []string
	Sources   []SourceDocument
}

// FileProjectionState is the current-file subset needed to detect every
// searchable metadata change without extracting unchanged file contents.
type FileProjectionState struct {
	SourceRevision string
	Title          string
	RelativePath   string
	DocumentStatus string
	MIMEType       string
	Extension      string
	SizeBytes      int64
	Status         string
}

func Open(path string) (*Engine, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	engine := &Engine{db: db}
	if err := engine.initialize(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return engine, nil
}

func (engine *Engine) Close() error { return engine.db.Close() }

func (engine *Engine) initialize(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=250`,
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
		`CREATE VIRTUAL TABLE IF NOT EXISTS search_terms USING fts5(
			title, normalized_text,
			content='search_documents', content_rowid='rowid',
			tokenize='unicode61 remove_diacritics 2', detail=full
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS search_cjk3 USING fts5(
			title, normalized_text,
			content='search_documents', content_rowid='rowid',
			tokenize='trigram case_sensitive 0', detail=full
		)`,
		`CREATE TRIGGER IF NOT EXISTS search_documents_ai AFTER INSERT ON search_documents BEGIN
			INSERT INTO search_terms(rowid,title,normalized_text)
			VALUES (new.rowid,new.title,new.normalized_text);
			INSERT INTO search_cjk3(rowid,title,normalized_text)
			VALUES (new.rowid,new.title,new.normalized_text);
		END`,
		`CREATE TRIGGER IF NOT EXISTS search_documents_ad AFTER DELETE ON search_documents BEGIN
			INSERT INTO search_terms(search_terms,rowid,title,normalized_text)
			VALUES ('delete',old.rowid,old.title,old.normalized_text);
			INSERT INTO search_cjk3(search_cjk3,rowid,title,normalized_text)
			VALUES ('delete',old.rowid,old.title,old.normalized_text);
		END`,
		`CREATE TRIGGER IF NOT EXISTS search_documents_au AFTER UPDATE ON search_documents BEGIN
			INSERT INTO search_terms(search_terms,rowid,title,normalized_text)
			VALUES ('delete',old.rowid,old.title,old.normalized_text);
			INSERT INTO search_terms(rowid,title,normalized_text)
			VALUES (new.rowid,new.title,new.normalized_text);
			INSERT INTO search_cjk3(search_cjk3,rowid,title,normalized_text)
			VALUES ('delete',old.rowid,old.title,old.normalized_text);
			INSERT INTO search_cjk3(rowid,title,normalized_text)
			VALUES (new.rowid,new.title,new.normalized_text);
		END`,
	}
	for _, statement := range statements {
		if _, err := engine.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize workspace search: %w", err)
		}
	}
	if err := ensureDisplayTextColumn(ctx, engine.db); err != nil {
		return err
	}
	return nil
}

// PublicErrorCode reduces storage-engine details to the stable, content-free
// search diagnostics exposed across the workspace RPC boundary.
func PublicErrorCode(err error) string {
	var productErr *Error
	if errors.As(err, &productErr) {
		return productErr.Code
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() & 0xff {
		case 5, 6: // SQLITE_BUSY / SQLITE_LOCKED
			return "workspace_search.busy"
		case 11, 26: // SQLITE_CORRUPT / SQLITE_NOTADB
			return "workspace_search.corrupt"
		case 13: // SQLITE_FULL
			return "workspace_search.disk_full"
		case 8, 10, 14: // SQLITE_READONLY / SQLITE_IOERR / SQLITE_CANTOPEN
			return "workspace_search.storage_failed"
		}
	}
	return "workspace_search.internal_failed"
}

func ensureDisplayTextColumn(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(search_documents)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == "display_text" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = db.ExecContext(ctx, `ALTER TABLE search_documents ADD COLUMN display_text TEXT NOT NULL DEFAULT ''`)
	return err
}

func (engine *Engine) FTS5Enabled(ctx context.Context) (bool, error) {
	var enabled int
	err := engine.db.QueryRowContext(
		ctx, `SELECT sqlite_compileoption_used('ENABLE_FTS5')`,
	).Scan(&enabled)
	return enabled == 1, err
}

func Normalize(value string) string {
	return norm.NFKC.String(cases.Fold().String(norm.NFKC.String(value)))
}

func (engine *Engine) Upsert(ctx context.Context, source SourceDocument) error {
	tx, err := engine.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertSource(ctx, tx, source); err != nil {
		return err
	}
	if err := bumpGeneration(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func insertSource(ctx context.Context, tx *sql.Tx, source SourceDocument) error {
	if err := validateSource(source); err != nil {
		return err
	}
	metadata, err := json.Marshal(source.Metadata)
	if err != nil {
		return err
	}
	openTarget, err := json.Marshal(source.OpenTarget)
	if err != nil {
		return err
	}
	if source.Current {
		if _, err := tx.ExecContext(ctx,
			`UPDATE search_documents SET is_current=0
			 WHERE kind=? AND canonical_id=? AND is_current=1`,
			source.Kind, source.CanonicalID); err != nil {
			return err
		}
	}
	hitID := sourceHitID(source)
	_, err = tx.ExecContext(ctx, `INSERT INTO search_documents(
		hit_id,kind,canonical_id,title,display_text,normalized_text,source_revision,revision_time,
		table_id,record_id,field_id,document_id,mime_type,extension,size_bytes,status,
		is_current,metadata_json,open_target_json
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(hit_id) DO UPDATE SET
		title=excluded.title, display_text=excluded.display_text,
		normalized_text=excluded.normalized_text,
		revision_time=excluded.revision_time, table_id=excluded.table_id,
		record_id=excluded.record_id, field_id=excluded.field_id,
		document_id=excluded.document_id, mime_type=excluded.mime_type,
		extension=excluded.extension, size_bytes=excluded.size_bytes,
		status=excluded.status, is_current=excluded.is_current,
		metadata_json=excluded.metadata_json,
		open_target_json=excluded.open_target_json`,
		hitID, source.Kind, source.CanonicalID, source.Title, source.Body,
		Normalize(source.Title+"\n"+source.Body), source.SourceRevision,
		source.RevisionTime, source.TableID, source.RecordID, source.FieldID,
		source.DocumentID, source.MIMEType, source.Extension, source.SizeBytes,
		source.Status, boolInt(source.Current), string(metadata), string(openTarget))
	return err
}

func sourceHitID(source SourceDocument) string {
	return source.Kind + ":" + source.CanonicalID + ":" + source.SourceRevision
}

// Rebuild replaces the complete derived corpus in one WAL transaction. Active
// readers retain the previous generation until verification and commit finish;
// cancellation or failure rolls the staging writes back.
func (engine *Engine) Rebuild(ctx context.Context, sources []SourceDocument) error {
	return engine.RebuildWithProgress(ctx, sources, nil)
}

func (engine *Engine) RebuildWithProgress(
	ctx context.Context,
	sources []SourceDocument,
	progress func(processed, total int),
) error {
	return engine.rebuildWithProgress(ctx, sources, nil, progress)
}

// RebuildProjection atomically promotes the rebuilt corpus and its source
// checkpoint. A crash or cancellation therefore exposes either the previous
// complete generation/checkpoint pair or the next complete pair, never a
// mixed state.
func (engine *Engine) RebuildProjection(
	ctx context.Context,
	sources []SourceDocument,
	checkpoint ProjectionCheckpoint,
	progress func(processed, total int),
) error {
	if checkpoint.BusinessOutboxRowID < 0 {
		return errors.New("workspace_search.checkpoint_invalid")
	}
	return engine.rebuildWithProgress(ctx, sources, &checkpoint, progress)
}

func (engine *Engine) rebuildWithProgress(
	ctx context.Context,
	sources []SourceDocument,
	checkpoint *ProjectionCheckpoint,
	progress func(processed, total int),
) error {
	tx, err := engine.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM search_documents`); err != nil {
		return err
	}
	for index, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := insertSource(ctx, tx, source); err != nil {
			return err
		}
		if progress != nil && ((index+1)%100 == 0 || index+1 == len(sources)) {
			progress(index+1, len(sources))
		}
	}
	for _, table := range []string{"search_terms", "search_cjk3"} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+`(`+table+`) VALUES('integrity-check')`); err != nil {
			return fmt.Errorf("verify %s: %w", table, err)
		}
	}
	if err := bumpGeneration(ctx, tx); err != nil {
		return err
	}
	if checkpoint != nil {
		if err := setProjectionCheckpoint(ctx, tx, *checkpoint); err != nil {
			return err
		}
	}
	if err := setRebuildRequired(ctx, tx, false); err != nil {
		return err
	}
	return tx.Commit()
}

// ApplyProjectionChanges idempotently replaces only affected business rows
// and file documents, then advances the durable source checkpoint in the same
// transaction. Deleted sources are represented by an affected key with no
// replacement document.
func (engine *Engine) ApplyProjectionChanges(
	ctx context.Context,
	changes ProjectionChanges,
	checkpoint ProjectionCheckpoint,
) error {
	if checkpoint.BusinessOutboxRowID < 0 {
		return errors.New("workspace_search.checkpoint_invalid")
	}
	tx, err := engine.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, key := range changes.Records {
		if strings.TrimSpace(key.TableID) == "" || strings.TrimSpace(key.RecordID) == "" {
			return errors.New("workspace_search.projection_key_invalid")
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM search_documents
			WHERE kind IN ('record','attachment') AND table_id=? AND record_id=?`,
			key.TableID, key.RecordID,
		); err != nil {
			return err
		}
	}
	for _, documentID := range changes.Documents {
		if strings.TrimSpace(documentID) == "" {
			return errors.New("workspace_search.projection_key_invalid")
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM search_documents WHERE kind='file' AND document_id=?`,
			documentID,
		); err != nil {
			return err
		}
	}
	for _, source := range changes.Sources {
		if err := insertSource(ctx, tx, source); err != nil {
			return err
		}
	}
	for _, table := range []string{"search_terms", "search_cjk3"} {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+table+`(`+table+`) VALUES('integrity-check')`,
		); err != nil {
			return fmt.Errorf("verify %s: %w", table, err)
		}
	}
	if err := bumpGeneration(ctx, tx); err != nil {
		return err
	}
	if err := setProjectionCheckpoint(ctx, tx, checkpoint); err != nil {
		return err
	}
	return tx.Commit()
}

func (engine *Engine) CurrentFileProjectionStates(
	ctx context.Context,
) (map[string]FileProjectionState, error) {
	rows, err := engine.db.QueryContext(ctx, `
		SELECT document_id, source_revision, title, metadata_json,
		       COALESCE(mime_type, ''), COALESCE(extension, ''),
		       COALESCE(size_bytes, 0), status
		FROM search_documents
		WHERE kind='file' AND is_current=1 AND document_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]FileProjectionState{}
	for rows.Next() {
		var documentID, metadataRaw string
		var state FileProjectionState
		if err := rows.Scan(
			&documentID,
			&state.SourceRevision,
			&state.Title,
			&metadataRaw,
			&state.MIMEType,
			&state.Extension,
			&state.SizeBytes,
			&state.Status,
		); err != nil {
			return nil, err
		}
		var metadata []contracts.SearchMetadataItem
		if err := json.Unmarshal([]byte(metadataRaw), &metadata); err != nil {
			return nil, errors.New("workspace_search.index_corrupt")
		}
		for _, item := range metadata {
			switch item.Key {
			case "relativePath":
				state.RelativePath, _ = item.Value.(string)
			case "documentStatus":
				state.DocumentStatus, _ = item.Value.(string)
			}
		}
		result[documentID] = state
	}
	return result, rows.Err()
}

func (engine *Engine) ProjectionCheckpoint(
	ctx context.Context,
) (ProjectionCheckpoint, error) {
	var businessRaw, fileRaw, mutationRaw string
	if err := engine.db.QueryRowContext(
		ctx,
		`SELECT value FROM search_meta WHERE key='business_outbox_rowid'`,
	).Scan(&businessRaw); err != nil {
		return ProjectionCheckpoint{}, err
	}
	if err := engine.db.QueryRowContext(
		ctx,
		`SELECT value FROM search_meta WHERE key='mutation_revision'`,
	).Scan(&mutationRaw); err != nil {
		return ProjectionCheckpoint{}, err
	}
	if err := engine.db.QueryRowContext(
		ctx,
		`SELECT value FROM search_meta WHERE key='file_head_revision'`,
	).Scan(&fileRaw); err != nil {
		return ProjectionCheckpoint{}, err
	}
	var checkpoint ProjectionCheckpoint
	if _, err := fmt.Sscan(businessRaw, &checkpoint.BusinessOutboxRowID); err != nil {
		return ProjectionCheckpoint{}, errors.New("workspace_search.checkpoint_corrupt")
	}
	if _, err := fmt.Sscan(fileRaw, &checkpoint.FileHeadRevision); err != nil {
		return ProjectionCheckpoint{}, errors.New("workspace_search.checkpoint_corrupt")
	}
	if _, err := fmt.Sscan(mutationRaw, &checkpoint.MutationRevision); err != nil {
		return ProjectionCheckpoint{}, errors.New("workspace_search.checkpoint_corrupt")
	}
	if checkpoint.BusinessOutboxRowID < 0 {
		return ProjectionCheckpoint{}, errors.New("workspace_search.checkpoint_corrupt")
	}
	return checkpoint, nil
}

func setProjectionCheckpoint(
	ctx context.Context,
	tx *sql.Tx,
	checkpoint ProjectionCheckpoint,
) error {
	values := []struct {
		key   string
		value any
	}{
		{"business_outbox_rowid", checkpoint.BusinessOutboxRowID},
		{"file_head_revision", checkpoint.FileHeadRevision},
		{"mutation_revision", checkpoint.MutationRevision},
	}
	for _, item := range values {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO search_meta(key,value) VALUES(?,?)
			 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			item.key,
			fmt.Sprint(item.value),
		); err != nil {
			return err
		}
	}
	return nil
}

func (engine *Engine) rebuildRequired(ctx context.Context) (bool, error) {
	var raw string
	if err := engine.db.QueryRowContext(
		ctx,
		`SELECT value FROM search_meta WHERE key='rebuild_required'`,
	).Scan(&raw); err != nil {
		return false, err
	}
	switch raw {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, errors.New("workspace_search.rebuild_state_corrupt")
	}
}

func setRebuildRequired(ctx context.Context, tx *sql.Tx, required bool) error {
	value := "0"
	if required {
		value = "1"
	}
	_, err := tx.ExecContext(
		ctx,
		`INSERT INTO search_meta(key,value) VALUES('rebuild_required',?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		value,
	)
	return err
}

func (engine *Engine) Status(ctx context.Context) (contracts.SearchStatus, error) {
	generation, err := engine.generation(ctx)
	if err != nil {
		return contracts.SearchStatus{}, err
	}
	var total int64
	if err := engine.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM search_documents`).Scan(&total); err != nil {
		return contracts.SearchStatus{}, err
	}
	rebuildRequired, err := engine.rebuildRequired(ctx)
	if err != nil {
		return contracts.SearchStatus{}, err
	}
	if rebuildRequired {
		code := restoreRebuildRequiredCode
		return contracts.SearchStatus{
			State: "degraded", Generation: generation, Processed: total,
			Total: &total, ErrorCode: &code,
		}, nil
	}
	state := "ready"
	if generation == 0 && total == 0 {
		state = "idle"
	}
	return contracts.SearchStatus{
		State: state, Generation: generation, Processed: total, Total: &total,
	}, nil
}

func (engine *Engine) Tombstone(
	ctx context.Context, kind, canonicalID, sourceRevision, revisionTime string,
) error {
	openTarget := contracts.SearchOpenTarget{Kind: kind}
	return engine.Upsert(ctx, SourceDocument{
		Kind: kind, CanonicalID: canonicalID, Title: canonicalID,
		SourceRevision: sourceRevision, RevisionTime: revisionTime,
		Status: "deleted", Current: true, OpenTarget: openTarget,
	})
}

// Invalidate removes every derived row and advances generation atomically.
// Restore callers use it before background work resumes, so no SearchHit from
// the pre-restore authority can be opened against the restored workspace.
func (engine *Engine) Invalidate(ctx context.Context) error {
	tx, err := engine.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM search_documents`); err != nil {
		return err
	}
	if err := bumpGeneration(ctx, tx); err != nil {
		return err
	}
	if err := setProjectionCheckpoint(ctx, tx, ProjectionCheckpoint{}); err != nil {
		return err
	}
	if err := setRebuildRequired(ctx, tx, true); err != nil {
		return err
	}
	return tx.Commit()
}

func (engine *Engine) Query(
	ctx context.Context, request contracts.SearchRequest,
) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	rebuildRequired, err := engine.rebuildRequired(ctx)
	if err != nil {
		return Result{}, err
	}
	if rebuildRequired {
		return Result{}, &Error{Code: restoreRebuildRequiredCode}
	}
	generation, err := engine.generation(ctx)
	if err != nil {
		return Result{}, err
	}
	fingerprint := requestFingerprint(request)
	offset := 0
	if request.Cursor != nil {
		cursor, err := decodeCursor(*request.Cursor)
		if err != nil || cursor.Generation != generation || cursor.Fingerprint != fingerprint {
			return Result{}, &Error{Code: "workspace_search.cursor_stale", Path: "cursor"}
		}
		offset = cursor.Offset
	}
	normalized := Normalize(strings.TrimSpace(request.Query))
	from, scoreExpression, matchWhere, args := searchPlan(normalized)
	where := []string{matchWhere}
	if request.Scope == "current" {
		where = append(where, "d.is_current=1", "d.status<>'deleted'")
	}
	filterSQL, filterArgs, err := compileFilters(request)
	if err != nil {
		return Result{}, err
	}
	if filterSQL != "" {
		where = append(where, filterSQL)
		args = append(args, filterArgs...)
	}
	order := compileSort(request.Sorts, scoreExpression)
	query := `SELECT d.hit_id,d.kind,d.canonical_id,d.title,d.display_text,d.source_revision,
		d.revision_time,d.metadata_json,d.open_target_json,` + scoreExpression + ` AS score
		FROM ` + from + ` JOIN search_documents d ON d.rowid=f.rowid
		WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	args = append(args, request.Limit+1, offset)
	rows, err := engine.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Result{}, fmt.Errorf("query workspace search: %w", err)
	}
	defer rows.Close()
	hits := make([]contracts.SearchHit, 0, request.Limit+1)
	for rows.Next() {
		var hit contracts.SearchHit
		var displayText, metadataRaw, openTargetRaw string
		if err := rows.Scan(
			&hit.HitId, &hit.Kind, &hit.CanonicalId, &hit.Title,
			&displayText, &hit.SourceRevision, &hit.RevisionTime, &metadataRaw,
			&openTargetRaw, &hit.Score,
		); err != nil {
			return Result{}, err
		}
		hit.ContractVersion = ContractVersion
		if err := json.Unmarshal([]byte(metadataRaw), &hit.Metadata); err != nil {
			return Result{}, err
		}
		if err := json.Unmarshal([]byte(openTargetRaw), &hit.OpenTarget); err != nil {
			return Result{}, err
		}
		snippet := makeSnippet(hit.Title, displayText, normalized)
		hit.Snippet = &snippet
		hit.Highlights = []string{normalized}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	var next *string
	if int64(len(hits)) > request.Limit {
		hits = hits[:request.Limit]
		value := encodeCursor(searchCursor{
			Generation: generation, Fingerprint: fingerprint,
			Offset: offset + int(request.Limit),
		})
		next = &value
	}
	return Result{Hits: hits, NextCursor: next, Generation: generation}, nil
}

func searchPlan(normalized string) (string, string, string, []any) {
	if hasCJK(normalized) && utf8.RuneCountInString(normalized) >= 3 {
		return "search_cjk3 f", "-bm25(search_cjk3)",
			"search_cjk3 MATCH ?", []any{quoteFTS(normalized)}
	}
	if hasCJK(normalized) {
		return "search_terms f", "0.0", "d.normalized_text LIKE ? ESCAPE '\\'",
			[]any{"%" + escapeLike(normalized) + "%"}
	}
	return "search_terms f", "-bm25(search_terms)",
		"search_terms MATCH ?", []any{quoteFTS(normalized)}
}

func compileFilters(request contracts.SearchRequest) (string, []any, error) {
	parts := make([]string, 0, len(request.Filters))
	args := make([]any, 0, len(request.Filters))
	for index, filter := range request.Filters {
		if !validSearchFilter(filter) {
			return "", nil, &Error{
				Code: "workspace_search.filter_invalid",
				Path: fmt.Sprintf("filters.%d", index),
			}
		}
		column := map[string]string{
			"kind": "d.kind", "tableId": "d.table_id", "fieldId": "d.field_id",
			"mimeType": "d.mime_type", "extension": "d.extension",
			"sizeBytes": "d.size_bytes", "revisionTime": "d.revision_time",
			"status": "d.status",
		}[filter.Field]
		if column == "" {
			return "", nil, &Error{Code: "workspace_search.filter_invalid", Path: fmt.Sprintf("filters.%d.field", index)}
		}
		op := map[string]string{
			"eq": "=", "ne": "!=", "gt": ">", "gte": ">=", "lt": "<",
			"lte": "<=", "before": "<", "after": ">",
		}[filter.Operator]
		if filter.Operator == "contains" {
			value, ok := filter.Value.(string)
			if !ok || filter.Field == "sizeBytes" {
				return "", nil, &Error{Code: "workspace_search.filter_invalid", Path: fmt.Sprintf("filters.%d.value", index)}
			}
			parts = append(parts, column+" LIKE ? ESCAPE '\\'")
			args = append(args, "%"+escapeLike(value)+"%")
			continue
		}
		if op == "" {
			return "", nil, &Error{Code: "workspace_search.filter_invalid", Path: fmt.Sprintf("filters.%d.operator", index)}
		}
		parts = append(parts, column+" "+op+" ?")
		args = append(args, filter.Value)
	}
	if len(parts) == 0 {
		return "", nil, nil
	}
	joiner := " AND "
	if request.Logic == "or" {
		joiner = " OR "
	}
	return "(" + strings.Join(parts, joiner) + ")", args, nil
}

func validSearchFilter(filter contracts.SearchFilter) bool {
	switch filter.Field {
	case "kind":
		value, ok := filter.Value.(string)
		return ok && (filter.Operator == "eq" || filter.Operator == "ne") &&
			(value == "record" || value == "attachment" || value == "file")
	case "tableId", "fieldId", "mimeType", "extension", "status":
		value, ok := filter.Value.(string)
		return ok && strings.TrimSpace(value) != "" &&
			(filter.Operator == "eq" || filter.Operator == "ne" || filter.Operator == "contains")
	case "sizeBytes":
		value, ok := numericFilterValue(filter.Value)
		return ok && value >= 0 && (filter.Operator == "eq" || filter.Operator == "ne" ||
			filter.Operator == "gt" || filter.Operator == "gte" ||
			filter.Operator == "lt" || filter.Operator == "lte")
	case "revisionTime":
		value, ok := filter.Value.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return false
		}
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return false
		}
		return filter.Operator == "eq" || filter.Operator == "ne" ||
			filter.Operator == "gt" || filter.Operator == "gte" ||
			filter.Operator == "lt" || filter.Operator == "lte" ||
			filter.Operator == "before" || filter.Operator == "after"
	default:
		return false
	}
}

func numericFilterValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

func compileSort(sorts []contracts.SearchSort, score string) string {
	parts := make([]string, 0, len(sorts)+1)
	for _, sort := range sorts {
		column := map[string]string{
			"score": score, "revisionTime": "d.revision_time",
			"title": "d.title", "sizeBytes": "d.size_bytes",
		}[sort.Field]
		direction := "ASC"
		if sort.Direction == "desc" {
			direction = "DESC"
		}
		parts = append(parts, column+" "+direction)
	}
	if len(parts) == 0 {
		parts = append(parts, score+" DESC")
	}
	parts = append(parts, "d.hit_id ASC")
	return strings.Join(parts, ",")
}

func validateSource(source SourceDocument) error {
	if source.Kind != "record" && source.Kind != "attachment" && source.Kind != "file" {
		return &Error{Code: "workspace_search.source_invalid", Path: "kind"}
	}
	if source.CanonicalID == "" || source.SourceRevision == "" || source.RevisionTime == "" {
		return &Error{Code: "workspace_search.source_invalid", Path: "identity"}
	}
	return nil
}

func validateRequest(request contracts.SearchRequest) error {
	if request.ContractVersion != ContractVersion || strings.TrimSpace(request.Query) == "" {
		return &Error{Code: "workspace_search.request_invalid", Path: "query"}
	}
	if request.Logic != "and" && request.Logic != "or" {
		return &Error{Code: "workspace_search.request_invalid", Path: "logic"}
	}
	if request.Scope != "current" && request.Scope != "history" {
		return &Error{Code: "workspace_search.request_invalid", Path: "scope"}
	}
	if request.Limit < 1 || request.Limit > 200 || len(request.Filters) > 20 || len(request.Sorts) > 3 {
		return &Error{Code: "workspace_search.request_invalid", Path: "limit"}
	}
	for index, sort := range request.Sorts {
		if sort.Direction != "asc" && sort.Direction != "desc" {
			return &Error{Code: "workspace_search.sort_invalid", Path: fmt.Sprintf("sorts.%d.direction", index)}
		}
		if sort.Field != "score" && sort.Field != "revisionTime" && sort.Field != "title" && sort.Field != "sizeBytes" {
			return &Error{Code: "workspace_search.sort_invalid", Path: fmt.Sprintf("sorts.%d.field", index)}
		}
	}
	return nil
}

func bumpGeneration(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE search_meta SET value=CAST(value AS INTEGER)+1 WHERE key='generation'`)
	return err
}

func (engine *Engine) generation(ctx context.Context) (int64, error) {
	var generation int64
	err := engine.db.QueryRowContext(ctx,
		`SELECT CAST(value AS INTEGER) FROM search_meta WHERE key='generation'`,
	).Scan(&generation)
	return generation, err
}

type searchCursor struct {
	Generation  int64  `json:"generation"`
	Fingerprint string `json:"fingerprint"`
	Offset      int    `json:"offset"`
}

func encodeCursor(cursor searchCursor) string {
	payload, _ := json.Marshal(cursor)
	checksum := sha256.Sum256(payload)
	envelope := append(payload, checksum[:]...)
	return base64.RawURLEncoding.EncodeToString(envelope)
}

func decodeCursor(value string) (searchCursor, error) {
	wire, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(wire) <= sha256.Size {
		return searchCursor{}, errors.New("invalid cursor")
	}
	payload, supplied := wire[:len(wire)-sha256.Size], wire[len(wire)-sha256.Size:]
	checksum := sha256.Sum256(payload)
	if !equalBytes(supplied, checksum[:]) {
		return searchCursor{}, errors.New("invalid cursor checksum")
	}
	var cursor searchCursor
	err = json.Unmarshal(payload, &cursor)
	return cursor, err
}

func requestFingerprint(request contracts.SearchRequest) string {
	request.Cursor = nil
	payload, _ := json.Marshal(request)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func quoteFTS(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}
func hasCJK(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}
func makeSnippet(title, body, query string) string {
	if strings.Contains(Normalize(title), query) {
		return title
	}
	compact := strings.Join(strings.Fields(body), " ")
	if utf8.RuneCountInString(compact) <= 240 {
		return compact
	}
	return string([]rune(compact)[:240]) + "…"
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var result byte
	for index := range left {
		result |= left[index] ^ right[index]
	}
	return result == 0
}
