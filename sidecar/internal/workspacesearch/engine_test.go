package workspacesearch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
)

func TestSearchUsesLockedFTSNormalizesUnicodeAndSupportsCJK(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	enabled, err := engine.FTS5Enabled(ctx)
	if err != nil || !enabled {
		t.Fatalf("FTS5Enabled() = %v, %v", enabled, err)
	}
	upsert(t, engine, source("record", "record-1", "rev-1", "ＦＵＳＳ", "Fuß quarterly report", true))
	upsert(t, engine, source("file", "doc-1", "rev-2", "项目报告", "离线数据工作台交付", true))

	latin := query(t, engine, request("fuss"))
	cjk := query(t, engine, request("数据工作台"))
	shortCJK := query(t, engine, request("数据"))

	if len(latin.Hits) != 1 || latin.Hits[0].CanonicalId != "record-1" {
		t.Fatalf("latin hits = %#v", latin.Hits)
	}
	if len(cjk.Hits) != 1 || cjk.Hits[0].CanonicalId != "doc-1" {
		t.Fatalf("CJK hits = %#v", cjk.Hits)
	}
	if len(shortCJK.Hits) != 1 || shortCJK.Hits[0].CanonicalId != "doc-1" {
		t.Fatalf("short CJK fallback hits = %#v", shortCJK.Hits)
	}
}

func TestSearchTreatsCanonicalUnicodeFormsAsEquivalentWithoutRewritingResults(
	t *testing.T,
) {
	engine := testEngine(t)
	originalTitles := map[string]string{
		"nfc": "Café 👩🏽‍💻",
		"nfd": "Cafe\u0301 👩🏽‍💻",
	}
	for canonicalID, title := range originalTitles {
		upsert(t, engine, source(
			"record",
			canonicalID,
			"rev-1",
			title,
			"body must not replace the matching title",
			true,
		))
	}

	for _, searchText := range []string{
		"Café 👩🏽‍💻",
		"Cafe\u0301 👩🏽‍💻",
	} {
		result := query(t, engine, request(searchText))
		if len(result.Hits) != len(originalTitles) {
			t.Fatalf("query %q hits = %#v", searchText, result.Hits)
		}
		hitsByID := make(map[string]contracts.SearchHit, len(result.Hits))
		for _, hit := range result.Hits {
			hitsByID[hit.CanonicalId] = hit
		}
		for canonicalID, originalTitle := range originalTitles {
			hit, found := hitsByID[canonicalID]
			if !found || hit.Title != originalTitle || hit.Snippet == nil ||
				*hit.Snippet != originalTitle {
				t.Fatalf(
					"query %q result %q = %#v",
					searchText,
					canonicalID,
					hit,
				)
			}
		}
	}
}

func TestSearchDistinguishesEmojiSequencesWithoutRewritingResults(t *testing.T) {
	engine := testEngine(t)
	originalTitles := map[string]string{
		"exact":      "Engineer 👩🏽‍💻",
		"other-tone": "Engineer 👩🏾‍💻",
		"no-joiner":  "Engineer 👩🏽💻",
	}
	for canonicalID, title := range originalTitles {
		upsert(t, engine, source(
			"record",
			canonicalID,
			"rev-1",
			title,
			"body must not replace the matching title",
			true,
		))
	}

	for canonicalID, searchText := range map[string]string{
		"exact":      "👩🏽‍💻",
		"other-tone": "👩🏾‍💻",
		"no-joiner":  "👩🏽💻",
	} {
		result := query(t, engine, request(searchText))
		if len(result.Hits) != 1 || result.Hits[0].CanonicalId != canonicalID {
			t.Fatalf("query %q hits = %#v", searchText, result.Hits)
		}
		hit := result.Hits[0]
		if hit.Title != originalTitles[canonicalID] || hit.Snippet == nil ||
			*hit.Snippet != originalTitles[canonicalID] {
			t.Fatalf("query %q result = %#v", searchText, hit)
		}
	}
}

func TestSearchMatchesAdjacentEmojiClustersWithoutMergingThem(t *testing.T) {
	engine := testEngine(t)
	for canonicalID, title := range map[string]string{
		"pair":      "Pair 😀😁",
		"zwj":       "Engineer 👩🏽‍💻",
		"no-joiner": "Engineer 👩🏽💻",
	} {
		upsert(t, engine, source("record", canonicalID, "rev-1", title, "body", true))
	}

	for searchText, canonicalID := range map[string]string{
		"😀":    "pair",
		"😁":    "pair",
		"😀😁":   "pair",
		"👩🏽‍💻": "zwj",
		"👩🏽💻":  "no-joiner",
	} {
		result := query(t, engine, request(searchText))
		if len(result.Hits) != 1 || result.Hits[0].CanonicalId != canonicalID {
			t.Fatalf("query %q hits = %#v", searchText, result.Hits)
		}
	}
}

func TestSearchDistinguishesAtomicSymbolsInEmojiOnlyAndMixedText(t *testing.T) {
	engine := testEngine(t)
	for canonicalID, title := range map[string]string{
		"mixed-exact":    "数据 Engineer 👩",
		"mixed-modified": "数据 Engineer 👩🏽",
		"emoji-only":     "👨🏻‍🚀",
		"literal-token":  "数据 vtsymf09f91a9",
	} {
		upsert(t, engine, source(
			"record", canonicalID, "rev-1", title,
			"body must not replace the matching title", true,
		))
	}

	for name, test := range map[string]struct {
		query, canonicalID, title string
	}{
		"mixed": {
			query: "数据 Engineer 👩", canonicalID: "mixed-exact",
			title: "数据 Engineer 👩",
		},
		"emoji-only": {
			query: "👨🏻‍🚀", canonicalID: "emoji-only", title: "👨🏻‍🚀",
		},
		"emoji reserved prefix": {
			query: "👩", canonicalID: "mixed-exact", title: "数据 Engineer 👩",
		},
		"literal reserved prefix": {
			query: "vtsymf09f91a9", canonicalID: "literal-token",
			title: "数据 vtsymf09f91a9",
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := query(t, engine, request(test.query))
			if len(result.Hits) != 1 || result.Hits[0].CanonicalId != test.canonicalID {
				t.Fatalf("query %q hits = %#v", test.query, result.Hits)
			}
			hit := result.Hits[0]
			if hit.Title != test.title || hit.Snippet == nil || *hit.Snippet != test.title {
				t.Fatalf("query %q result = %#v", test.query, hit)
			}
		})
	}
}

func TestSearchPreservesEmojiCodepointsBeforeLexicalNormalization(t *testing.T) {
	engine := testEngine(t)
	const englandFlag = "🏴\U000e0067\U000e0062\U000e0065\U000e006e\U000e0067\U000e007f"
	for canonicalID, title := range map[string]string{
		"information-emoji": "Info ℹ️",
		"letter-with-vs":    "Info i️",
		"wavy-dash":         "Mark 〰️",
		"part-alternation":  "Mark 〽️",
		"emoji-heart":       "Heart ♥️",
		"text-heart":        "Heart ♥︎",
		"keycap":            "Count 1️⃣",
		"digit-with-vs":     "Count 1️",
		"regional-flag":     "Flag 🇨🇳",
		"regional-symbol":   "Flag 🇨",
		"tagged-flag":       "Flag " + englandFlag,
		"black-flag":        "Flag 🏴",
	} {
		upsert(t, engine, source("record", canonicalID, "rev-1", title, "body", true))
	}

	for canonicalID, searchText := range map[string]string{
		"information-emoji": "ℹ️",
		"letter-with-vs":    "i️",
		"wavy-dash":         "〰️",
		"part-alternation":  "〽️",
		"emoji-heart":       "♥️",
		"text-heart":        "♥︎",
		"keycap":            "1️⃣",
		"digit-with-vs":     "1️",
		"regional-flag":     "🇨🇳",
		"regional-symbol":   "🇨",
		"tagged-flag":       englandFlag,
		"black-flag":        "🏴",
	} {
		result := query(t, engine, request(searchText))
		if len(result.Hits) != 1 || result.Hits[0].CanonicalId != canonicalID {
			t.Fatalf("query %q hits = %#v", searchText, result.Hits)
		}
	}
}

func TestSearchFiltersHistoryPaginationAndStaleCursor(t *testing.T) {
	engine := testEngine(t)
	upsert(t, engine, source("file", "doc-1", "rev-1", "Report old", "alpha", true))
	upsert(t, engine, source("file", "doc-1", "rev-2", "Report current", "alpha", true))
	second := source("attachment", "att-1", "rev-3", "Attachment", "alpha", true)
	mime := "application/pdf"
	second.MIMEType = &mime
	upsert(t, engine, second)

	currentRequest := request("alpha")
	currentRequest.Limit = 1
	currentRequest.Filters = []contracts.SearchFilter{{
		Field: "kind", Operator: "eq", Value: "file",
	}}
	current := query(t, engine, currentRequest)
	if len(current.Hits) != 1 || current.Hits[0].SourceRevision != "rev-2" || current.NextCursor != nil {
		t.Fatalf("current result = %#v", current)
	}
	historyRequest := request("alpha")
	historyRequest.Scope = "history"
	historyRequest.Limit = 1
	history := query(t, engine, historyRequest)
	if history.NextCursor == nil {
		t.Fatal("history query did not return a cursor")
	}
	historyRequest.Cursor = history.NextCursor
	secondPage := query(t, engine, historyRequest)
	if len(secondPage.Hits) != 1 {
		t.Fatalf("second page = %#v", secondPage)
	}
	upsert(t, engine, source("record", "record-2", "rev-4", "New", "alpha", true))
	if _, err := engine.Query(context.Background(), historyRequest); err == nil || err.Error() != "workspace_search.cursor_stale" {
		t.Fatalf("stale cursor error = %v", err)
	}
}

func TestSearchRejectsMalformedAndTamperedCursorsWithoutOpeningAResult(t *testing.T) {
	engine := testEngine(t)
	upsert(t, engine, source("record", "one", "rev-1", "One", "alpha", true))
	upsert(t, engine, source("record", "two", "rev-1", "Two", "alpha", true))
	value := request("alpha")
	value.Limit = 1
	first := query(t, engine, value)
	if first.NextCursor == nil {
		t.Fatal("first page did not return a cursor")
	}
	valid := *first.NextCursor
	for _, cursor := range []string{"%", valid[:len(valid)-1] + "A"} {
		value.Cursor = &cursor
		if _, err := engine.Query(context.Background(), value); err == nil ||
			PublicErrorCode(err) != "workspace_search.cursor_stale" {
			t.Fatalf("cursor %q error = %v", cursor, err)
		}
	}
}

func TestSearchHandlesLongSnippetsHistoricalRowsAndClosedStorage(t *testing.T) {
	engine := testEngine(t)
	value := source(
		"record", "history", "rev-1", "Historical",
		strings.Repeat("prefix ", 45)+"needle", false,
	)
	upsert(t, engine, value)
	search := request("needle")
	search.Scope = "history"
	result := query(t, engine, search)
	if len(result.Hits) != 1 || result.Hits[0].Snippet == nil ||
		!strings.HasSuffix(*result.Hits[0].Snippet, "…") {
		t.Fatalf("long snippet result = %#v", result)
	}

	if code := PublicErrorCode(errors.New("private storage detail")); code != "workspace_search.internal_failed" {
		t.Fatalf("generic public error code = %q", code)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if err := engine.Upsert(context.Background(), value); err == nil {
		t.Fatal("upsert on closed storage unexpectedly succeeded")
	}
	if err := engine.Invalidate(context.Background()); err == nil {
		t.Fatal("invalidate on closed storage unexpectedly succeeded")
	}
	if _, err := engine.Status(context.Background()); err == nil {
		t.Fatal("status on closed storage unexpectedly succeeded")
	}
	if _, err := engine.Query(context.Background(), search); err == nil {
		t.Fatal("query on closed storage unexpectedly succeeded")
	}
}

func TestSearchRejectsUnknownFilterAndDoesNotInterpretFTSSyntax(t *testing.T) {
	engine := testEngine(t)
	upsert(t, engine, source("record", "record-1", "rev-1", "Literal", "alpha OR beta", true))
	result := query(t, engine, request("alpha OR beta"))
	if len(result.Hits) != 1 {
		t.Fatalf("quoted query hits = %#v", result.Hits)
	}
	invalid := request("alpha")
	invalid.Filters = []contracts.SearchFilter{{Field: "rawSql", Operator: "eq", Value: "1"}}
	if _, err := engine.Query(context.Background(), invalid); err == nil || err.Error() != "workspace_search.filter_invalid" {
		t.Fatalf("invalid filter error = %v", err)
	}
}

func TestSearchRejectsFilterOperatorAndValueTypeMismatches(t *testing.T) {
	engine := testEngine(t)
	for _, filter := range []contracts.SearchFilter{
		{Field: "sizeBytes", Operator: "contains", Value: "10"},
		{Field: "sizeBytes", Operator: "gte", Value: "10"},
		{Field: "revisionTime", Operator: "after", Value: true},
		{Field: "revisionTime", Operator: "after", Value: "not-a-date"},
		{Field: "kind", Operator: "eq", Value: "unknown"},
		{Field: "tableId", Operator: "gt", Value: "table-1"},
		{Field: "status", Operator: "contains", Value: nil},
	} {
		request := request("alpha")
		request.Filters = []contracts.SearchFilter{filter}
		if _, err := engine.Query(context.Background(), request); err == nil ||
			err.Error() != "workspace_search.filter_invalid" {
			t.Fatalf("filter %#v error = %v", filter, err)
		}
	}
}

func TestSearchStorageFaultsMapToStablePublicCodes(t *testing.T) {
	t.Run("corrupt", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "corrupt.db")
		if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(path)
		if err == nil || PublicErrorCode(err) != "workspace_search.corrupt" {
			t.Fatalf("error = %v, code = %s", err, PublicErrorCode(err))
		}
	})

	t.Run("busy", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "busy.db")
		engine, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer engine.Close()
		locker, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		locker.SetMaxOpenConns(1)
		defer locker.Close()
		if _, err := locker.Exec(`PRAGMA busy_timeout=10; BEGIN IMMEDIATE`); err != nil {
			t.Fatal(err)
		}
		defer locker.Exec(`ROLLBACK`)
		err = engine.Upsert(
			context.Background(),
			source("record", "record-1", "revision-1", "Busy", "alpha", true),
		)
		if err == nil || PublicErrorCode(err) != "workspace_search.busy" {
			t.Fatalf("error = %v, code = %s", err, PublicErrorCode(err))
		}
	})

	t.Run("full", func(t *testing.T) {
		engine := testEngine(t)
		var pages int
		if err := engine.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.db.Exec(`PRAGMA max_page_count=` + fmt.Sprint(pages)); err != nil {
			t.Fatal(err)
		}
		value := source("record", "record-large", "revision-1", "Full", strings.Repeat("x", 1<<20), true)
		err := engine.Upsert(context.Background(), value)
		if err == nil || PublicErrorCode(err) != "workspace_search.disk_full" {
			t.Fatalf("error = %v, code = %s", err, PublicErrorCode(err))
		}
	})
}

func TestOpenMigratesLegacySearchProjectionOnceAndPreservesCorpus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	createLegacySearchDatabase(t, path, legacySearchDatabaseOptions{includeDisplayText: true})

	engine, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })
	for canonicalID, searchText := range map[string]string{
		"legacy-zwj":       "👩🏽‍💻",
		"legacy-no-joiner": "👩🏽💻",
	} {
		result := query(t, engine, request(searchText))
		if result.Generation != 8 || len(result.Hits) != 1 ||
			result.Hits[0].CanonicalId != canonicalID {
			t.Fatalf("query %q result = %#v", searchText, result)
		}
		wantTitle := map[string]string{
			"legacy-zwj": "Legacy 👩🏽‍💻", "legacy-no-joiner": "Legacy 👩🏽💻",
		}[canonicalID]
		hit := result.Hits[0]
		if hit.Title != wantTitle || hit.Snippet == nil || *hit.Snippet != wantTitle {
			t.Fatalf("query %q hit = %#v", searchText, hit)
		}
	}
	bodyResult := query(t, engine, request("legacy body"))
	if len(bodyResult.Hits) != 2 {
		t.Fatalf("legacy body result = %#v", bodyResult)
	}
	for _, hit := range bodyResult.Hits {
		if hit.Snippet == nil || *hit.Snippet != "legacy body" {
			t.Fatalf("legacy body hit = %#v", hit)
		}
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	result := query(t, reopened, request("👩🏽‍💻"))
	if result.Generation != 8 || len(result.Hits) != 1 ||
		result.Hits[0].CanonicalId != "legacy-zwj" {
		t.Fatalf("idempotent reopen result = %#v", result)
	}
}

func TestOpenMigratesLegacySearchSchemaWithoutDisplayText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-without-display.db")
	createLegacySearchDatabase(t, path, legacySearchDatabaseOptions{})

	engine, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })
	result := query(t, engine, request("👩🏽‍💻"))
	if result.Generation != 8 || len(result.Hits) != 1 ||
		result.Hits[0].CanonicalId != "legacy-zwj" {
		t.Fatalf("migrated result = %#v", result)
	}
	hit := result.Hits[0]
	if hit.Title != "Legacy 👩🏽‍💻" || hit.Snippet == nil ||
		*hit.Snippet != "Legacy 👩🏽‍💻" {
		t.Fatalf("migrated hit = %#v", hit)
	}
	if bodyResult := query(t, engine, request("legacy body")); len(bodyResult.Hits) != 0 {
		t.Fatalf("missing legacy display body was recovered from derived text: %#v", bodyResult)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	result = query(t, reopened, request("👩🏽‍💻"))
	if result.Generation != 8 || len(result.Hits) != 1 ||
		result.Hits[0].CanonicalId != "legacy-zwj" {
		t.Fatalf("idempotent reopen result = %#v", result)
	}
}

func TestOpenRollsBackFailedLegacySearchProjectionMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-blocked.db")
	createLegacySearchDatabase(t, path, legacySearchDatabaseOptions{
		includeDisplayText: true,
		blockMigration:     true,
	})

	if engine, err := Open(path); err == nil {
		engine.Close()
		t.Fatal("blocked legacy migration unexpectedly succeeded")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var projectedColumns int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('search_documents') WHERE name='projected_title'`,
	).Scan(&projectedColumns); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var projectionVersions int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM search_meta WHERE key='projection_schema_version'`,
	).Scan(&projectionVersions); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var generation string
	if err := db.QueryRow(
		`SELECT value FROM search_meta WHERE key='generation'`,
	).Scan(&generation); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if projectedColumns != 0 || projectionVersions != 0 || generation != "7" {
		db.Close()
		t.Fatalf(
			"partial migration: projected columns=%d versions=%d generation=%q",
			projectedColumns, projectionVersions, generation,
		)
	}
	if _, err := db.Exec(`DROP TRIGGER block_projection_migration`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	engine, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	result := query(t, engine, request("👩🏽‍💻"))
	if result.Generation != 8 || len(result.Hits) != 1 ||
		result.Hits[0].CanonicalId != "legacy-zwj" {
		t.Fatalf("retried migration result = %#v", result)
	}
}

func TestOpenRejectsIncompatibleProjectionSchemas(t *testing.T) {
	for name, test := range map[string]struct {
		statements []string
		want       string
	}{
		"malformed version": {
			statements: []string{
				`CREATE TABLE search_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
				`INSERT INTO search_meta(key,value) VALUES
					('generation','0'),('projection_schema_version','invalid')`,
			},
			want: "workspace_search.projection_schema_corrupt",
		},
		"unsupported version": {
			statements: []string{
				`CREATE TABLE search_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
				`INSERT INTO search_meta(key,value) VALUES
					('generation','0'),('projection_schema_version','2')`,
			},
			want: "workspace_search.projection_schema_unsupported",
		},
		"current version missing projection column": {
			statements: []string{
				`CREATE TABLE search_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
				`INSERT INTO search_meta(key,value) VALUES
					('generation','0'),('projection_schema_version','1')`,
				`CREATE TABLE search_documents (
					rowid INTEGER PRIMARY KEY,
					kind TEXT NOT NULL,
					canonical_id TEXT NOT NULL,
					display_text TEXT NOT NULL DEFAULT '',
					is_current INTEGER NOT NULL
				)`,
			},
			want: "workspace_search.projection_schema_corrupt",
		},
		"metadata missing value column": {
			statements: []string{`CREATE TABLE search_meta (key TEXT PRIMARY KEY)`},
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "incompatible.db")
			execSearchDatabaseStatements(t, path, test.statements...)
			engine, err := Open(path)
			if engine != nil {
				engine.Close()
				t.Fatal("incompatible projection schema unexpectedly opened")
			}
			if err == nil {
				t.Fatal("incompatible projection schema did not return an error")
			}
			if test.want != "" && !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Open() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestOpenRollsBackCorruptLegacyProjectionState(t *testing.T) {
	t.Run("legacy FTS name belongs to a view", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "wrong-fts-kind.db")
		createLegacySearchDatabase(t, path, legacySearchDatabaseOptions{includeDisplayText: true})
		execSearchDatabaseStatements(t, path,
			`DROP TABLE search_terms`,
			`CREATE VIEW search_terms AS SELECT 'legacy' AS title, 'legacy' AS normalized_text`,
		)
		if engine, err := Open(path); err == nil {
			engine.Close()
			t.Fatal("legacy FTS view unexpectedly migrated")
		}
		assertLegacyProjectionState(t, path, 0, 0, "7")
	})

	t.Run("generation cannot be invalidated", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "blocked-generation.db")
		createLegacySearchDatabase(t, path, legacySearchDatabaseOptions{includeDisplayText: true})
		execSearchDatabaseStatements(t, path, `CREATE TRIGGER block_generation_migration
			BEFORE UPDATE OF value ON search_meta
			WHEN old.key='generation' BEGIN
				SELECT RAISE(ABORT, 'blocked generation migration');
			END`)
		if engine, err := Open(path); err == nil {
			engine.Close()
			t.Fatal("migration without cursor invalidation unexpectedly succeeded")
		}
		assertLegacyProjectionState(t, path, 0, 0, "7")
	})

	t.Run("legacy row has null title", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "null-title.db")
		execSearchDatabaseStatements(t, path,
			`CREATE TABLE search_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
			`INSERT INTO search_meta(key,value) VALUES ('generation','7')`,
			`CREATE TABLE search_documents (
				rowid INTEGER PRIMARY KEY,
				kind TEXT NOT NULL,
				canonical_id TEXT NOT NULL,
				title TEXT,
				normalized_text TEXT NOT NULL,
				is_current INTEGER NOT NULL
			)`,
			`INSERT INTO search_documents(kind,canonical_id,title,normalized_text,is_current)
				VALUES ('record','corrupt',NULL,'corrupt',1)`,
		)
		if engine, err := Open(path); err == nil {
			engine.Close()
			t.Fatal("legacy row with null title unexpectedly migrated")
		}
		assertLegacyProjectionState(t, path, 0, 0, "7")
	})
}

func TestProjectionCheckpointPromotesAtomicallyWithCorpus(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	first := ProjectionCheckpoint{
		BusinessOutboxRowID: 8, FileHeadRevision: 3, MutationRevision: 11,
	}
	if err := engine.RebuildProjection(
		ctx,
		[]SourceDocument{source("record", "record-1", "rev-1", "First", "alpha", true)},
		first,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if actual, err := engine.ProjectionCheckpoint(ctx); err != nil || actual != first {
		t.Fatalf("checkpoint = %#v, %v", actual, err)
	}

	invalid := source("record", "", "rev-2", "Invalid", "beta", true)
	second := ProjectionCheckpoint{
		BusinessOutboxRowID: 9, FileHeadRevision: 4, MutationRevision: 12,
	}
	if err := engine.RebuildProjection(ctx, []SourceDocument{invalid}, second, nil); err == nil {
		t.Fatal("invalid projection unexpectedly promoted")
	}
	if actual, err := engine.ProjectionCheckpoint(ctx); err != nil || actual != first {
		t.Fatalf("failed rebuild advanced checkpoint = %#v, %v", actual, err)
	}
	result := query(t, engine, request("alpha"))
	if len(result.Hits) != 1 || result.Hits[0].CanonicalId != "record-1" {
		t.Fatalf("failed rebuild replaced corpus = %#v", result.Hits)
	}
}

func TestInvalidateClearsProjectionCheckpoint(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	if err := engine.RebuildProjection(
		ctx,
		[]SourceDocument{source("file", "doc-1", "rev-1", "File", "alpha", true)},
		ProjectionCheckpoint{
			BusinessOutboxRowID: 12, FileHeadRevision: 5, MutationRevision: 16,
		},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := engine.Invalidate(ctx); err != nil {
		t.Fatal(err)
	}
	if actual, err := engine.ProjectionCheckpoint(ctx); err != nil || actual != (ProjectionCheckpoint{}) {
		t.Fatalf("checkpoint after invalidate = %#v, %v", actual, err)
	}
}

func TestInvalidatePersistsRebuildRequiredUntilSuccessfulPromotion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "search.db")
	engine, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.RebuildProjection(
		ctx,
		[]SourceDocument{source("record", "record-1", "rev-1", "Old", "stale", true)},
		ProjectionCheckpoint{MutationRevision: 7},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := engine.Invalidate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	status, err := reopened.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "degraded" || status.ErrorCode == nil ||
		*status.ErrorCode != "workspace_search.restore_rebuild_required" {
		t.Fatalf("status after restart = %#v", status)
	}
	if _, err := reopened.Query(ctx, request("stale")); err == nil ||
		err.Error() != "workspace_search.restore_rebuild_required" {
		t.Fatalf("query while rebuild is required = %v", err)
	}

	invalid := source("record", "", "rev-2", "Invalid", "invalid", true)
	if err := reopened.RebuildProjection(
		ctx, []SourceDocument{invalid}, ProjectionCheckpoint{MutationRevision: 8}, nil,
	); err == nil {
		t.Fatal("invalid rebuild unexpectedly succeeded")
	}
	status, err = reopened.Status(ctx)
	if err != nil || status.State != "degraded" {
		t.Fatalf("failed rebuild cleared required state: %#v, %v", status, err)
	}

	if err := reopened.RebuildProjection(
		ctx,
		[]SourceDocument{source("record", "record-2", "rev-2", "Restored", "current", true)},
		ProjectionCheckpoint{MutationRevision: 8},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	status, err = reopened.Status(ctx)
	if err != nil || status.State != "ready" || status.ErrorCode != nil {
		t.Fatalf("status after successful rebuild = %#v, %v", status, err)
	}
	result, err := reopened.Query(ctx, request("current"))
	if err != nil || len(result.Hits) != 1 || result.Hits[0].CanonicalId != "record-2" {
		t.Fatalf("rebuilt result = %#v, %v", result, err)
	}
}

func TestCorruptRebuildMarkerFailsClosedForStatusAndQuery(t *testing.T) {
	engine := testEngine(t)
	if _, err := engine.db.Exec(
		`UPDATE search_meta SET value='unknown' WHERE key='rebuild_required'`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Status(context.Background()); err == nil ||
		err.Error() != "workspace_search.rebuild_state_corrupt" {
		t.Fatalf("status with corrupt rebuild marker = %v", err)
	}
	if _, err := engine.Query(context.Background(), request("anything")); err == nil ||
		err.Error() != "workspace_search.rebuild_state_corrupt" {
		t.Fatalf("query with corrupt rebuild marker = %v", err)
	}
}

func TestApplyProjectionChangesUpsertsAndTombstonesAffectedSourcesAtomically(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	table, firstRecord, secondRecord := "table-1", "record-1", "record-2"
	first := source("record", table+":"+firstRecord, "rev-1", "First", "alpha", true)
	first.TableID, first.RecordID = &table, &firstRecord
	second := source("record", table+":"+secondRecord, "rev-1", "Second", "beta", true)
	second.TableID, second.RecordID = &table, &secondRecord
	if err := engine.RebuildProjection(
		ctx, []SourceDocument{first, second}, ProjectionCheckpoint{MutationRevision: 1}, nil,
	); err != nil {
		t.Fatal(err)
	}
	updated := first
	updated.SourceRevision, updated.Body = "rev-2", "gamma"
	checkpoint := ProjectionCheckpoint{
		BusinessOutboxRowID: 2, MutationRevision: 2,
	}
	if err := engine.ApplyProjectionChanges(ctx, ProjectionChanges{
		Records: []RecordProjectionKey{
			{TableID: table, RecordID: firstRecord},
			{TableID: table, RecordID: secondRecord},
		},
		Sources: []SourceDocument{updated},
	}, checkpoint); err != nil {
		t.Fatal(err)
	}
	if hits := query(t, engine, request("gamma")).Hits; len(hits) != 1 {
		t.Fatalf("updated hits = %#v", hits)
	}
	if hits := query(t, engine, request("beta")).Hits; len(hits) != 0 {
		t.Fatalf("tombstoned hits = %#v", hits)
	}
	if actual, err := engine.ProjectionCheckpoint(ctx); err != nil || actual != checkpoint {
		t.Fatalf("checkpoint = %#v, %v", actual, err)
	}
}

func TestRebuildReportsProgressPublishesStatusAndCanTombstone(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	initial, err := engine.Status(ctx)
	if err != nil || initial.State != "idle" || initial.Generation != 0 || initial.Processed != 0 {
		t.Fatalf("initial status = %#v, %v", initial, err)
	}

	sources := make([]SourceDocument, 101)
	for index := range sources {
		sources[index] = source(
			"record",
			fmt.Sprintf("record-%03d", index),
			"rev-1",
			fmt.Sprintf("Record %03d", index),
			"rebuild corpus",
			true,
		)
	}
	var progress [][2]int
	if err := engine.RebuildWithProgress(ctx, sources, func(processed, total int) {
		progress = append(progress, [2]int{processed, total})
	}); err != nil {
		t.Fatal(err)
	}
	if len(progress) != 2 || progress[0] != [2]int{100, 101} || progress[1] != [2]int{101, 101} {
		t.Fatalf("progress = %#v", progress)
	}
	ready, err := engine.Status(ctx)
	if err != nil || ready.State != "ready" || ready.Generation != 1 || ready.Processed != 101 ||
		ready.Total == nil || *ready.Total != 101 {
		t.Fatalf("ready status = %#v, %v", ready, err)
	}

	if err := engine.Tombstone(ctx, "record", "record-000", "rev-2", "2026-08-12T01:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if hits := query(t, engine, request("Record 000")).Hits; len(hits) != 0 {
		t.Fatalf("current tombstoned hits = %#v", hits)
	}
	history := request("Record 000")
	history.Scope = "history"
	if hits := query(t, engine, history).Hits; len(hits) != 2 {
		t.Fatalf("history tombstoned hits = %#v", hits)
	}

	if err := engine.Rebuild(ctx, sources[:1]); err != nil {
		t.Fatal(err)
	}
	if hits := query(t, engine, request("rebuild corpus")).Hits; len(hits) != 1 {
		t.Fatalf("rebuilt hits = %#v", hits)
	}
}

func TestCurrentFileProjectionStatesRoundTripsMetadataAndRejectsCorruption(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	documentID, mime, extension := "document-1", "text/plain", "txt"
	size := int64(42)
	file := source("file", documentID, "rev-7", "Notes", "offline", true)
	file.DocumentID, file.MIMEType, file.Extension, file.SizeBytes = &documentID, &mime, &extension, &size
	file.Status = "indexed"
	file.Metadata = []contracts.SearchMetadataItem{
		{Key: "relativePath", Value: "notes/today.txt"},
		{Key: "documentStatus", Value: "available"},
	}
	if err := engine.Upsert(ctx, file); err != nil {
		t.Fatal(err)
	}
	states, err := engine.CurrentFileProjectionStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := FileProjectionState{
		SourceRevision: "rev-7", Title: "Notes", RelativePath: "notes/today.txt",
		DocumentStatus: "available", MIMEType: mime, Extension: extension,
		SizeBytes: size, Status: "indexed",
	}
	if states[documentID] != want {
		t.Fatalf("state = %#v", states[documentID])
	}

	if _, err := engine.db.Exec(`UPDATE search_documents SET metadata_json='{' WHERE document_id=?`, documentID); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.CurrentFileProjectionStates(ctx); err == nil || err.Error() != "workspace_search.index_corrupt" {
		t.Fatalf("corrupt metadata error = %v", err)
	}
}

func TestProjectionMutationRejectsInvalidKeysAndCheckpoint(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	invalidCheckpoint := ProjectionCheckpoint{BusinessOutboxRowID: -1}
	if err := engine.RebuildProjection(ctx, nil, invalidCheckpoint, nil); err == nil ||
		err.Error() != "workspace_search.checkpoint_invalid" {
		t.Fatalf("rebuild checkpoint error = %v", err)
	}
	if err := engine.ApplyProjectionChanges(ctx, ProjectionChanges{}, invalidCheckpoint); err == nil ||
		err.Error() != "workspace_search.checkpoint_invalid" {
		t.Fatalf("incremental checkpoint error = %v", err)
	}
	for _, changes := range []ProjectionChanges{
		{Records: []RecordProjectionKey{{TableID: "", RecordID: "record-1"}}},
		{Records: []RecordProjectionKey{{TableID: "table-1", RecordID: " "}}},
		{Documents: []string{" "}},
	} {
		if err := engine.ApplyProjectionChanges(ctx, changes, ProjectionCheckpoint{}); err == nil ||
			err.Error() != "workspace_search.projection_key_invalid" {
			t.Fatalf("invalid projection key error = %v", err)
		}
	}
}

func TestProjectionCheckpointRejectsMissingAndCorruptMetadata(t *testing.T) {
	for _, test := range []struct {
		name, key, value string
		remove           bool
	}{
		{"missing business tail", "business_outbox_rowid", "", true},
		{"invalid business tail", "business_outbox_rowid", "bad", false},
		{"negative business tail", "business_outbox_rowid", "-1", false},
		{"invalid file tail", "file_head_revision", "bad", false},
		{"invalid mutation tail", "mutation_revision", "bad", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := testEngine(t)
			var err error
			if test.remove {
				_, err = engine.db.Exec(`DELETE FROM search_meta WHERE key=?`, test.key)
			} else {
				_, err = engine.db.Exec(`UPDATE search_meta SET value=? WHERE key=?`, test.value, test.key)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = engine.ProjectionCheckpoint(context.Background())
			if err == nil {
				t.Fatal("corrupt checkpoint unexpectedly accepted")
			}
		})
	}
}

func TestSearchAcceptsEverySortAndNumericFilterRepresentation(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	for index, numeric := range []any{int(12), int64(12), uint64(12), float64(12)} {
		value := source("file", fmt.Sprintf("doc-%d", index), "rev-1", fmt.Sprintf("Title %d", index), "alpha", true)
		size, documentID := int64(12), fmt.Sprintf("doc-%d", index)
		value.SizeBytes, value.DocumentID = &size, &documentID
		if err := engine.Upsert(ctx, value); err != nil {
			t.Fatal(err)
		}
		search := request("alpha")
		search.Filters = []contracts.SearchFilter{{Field: "sizeBytes", Operator: "eq", Value: numeric}}
		search.Sorts = []contracts.SearchSort{
			{Field: "score", Direction: "desc"},
			{Field: "revisionTime", Direction: "asc"},
			{Field: "title", Direction: "desc"},
		}
		if result, err := engine.Query(ctx, search); err != nil || len(result.Hits) == 0 {
			t.Fatalf("numeric %T result = %#v, %v", numeric, result, err)
		}
	}
	search := request("alpha")
	search.Sorts = []contracts.SearchSort{{Field: "sizeBytes", Direction: "asc"}}
	if _, err := engine.Query(ctx, search); err != nil {
		t.Fatal(err)
	}
}

func TestSearchRejectsEveryMalformedRequestDimension(t *testing.T) {
	tooManyFilters := make([]contracts.SearchFilter, 21)
	tooManySorts := make([]contracts.SearchSort, 4)
	for index := range tooManySorts {
		tooManySorts[index] = contracts.SearchSort{Field: "title", Direction: "asc"}
	}
	tests := []struct {
		name   string
		mutate func(*contracts.SearchRequest)
		code   string
	}{
		{"version", func(value *contracts.SearchRequest) { value.ContractVersion = "2.0" }, "workspace_search.request_invalid"},
		{"blank query", func(value *contracts.SearchRequest) { value.Query = " " }, "workspace_search.request_invalid"},
		{"logic", func(value *contracts.SearchRequest) { value.Logic = "xor" }, "workspace_search.request_invalid"},
		{"scope", func(value *contracts.SearchRequest) { value.Scope = "all" }, "workspace_search.request_invalid"},
		{"zero limit", func(value *contracts.SearchRequest) { value.Limit = 0 }, "workspace_search.request_invalid"},
		{"large limit", func(value *contracts.SearchRequest) { value.Limit = 201 }, "workspace_search.request_invalid"},
		{"many filters", func(value *contracts.SearchRequest) { value.Filters = tooManyFilters }, "workspace_search.request_invalid"},
		{"many sorts", func(value *contracts.SearchRequest) { value.Sorts = tooManySorts }, "workspace_search.request_invalid"},
		{"sort direction", func(value *contracts.SearchRequest) {
			value.Sorts = []contracts.SearchSort{{Field: "title", Direction: "up"}}
		}, "workspace_search.sort_invalid"},
		{"sort field", func(value *contracts.SearchRequest) {
			value.Sorts = []contracts.SearchSort{{Field: "raw", Direction: "asc"}}
		}, "workspace_search.sort_invalid"},
	}
	engine := testEngine(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := request("alpha")
			test.mutate(&value)
			_, err := engine.Query(context.Background(), value)
			if err == nil || PublicErrorCode(err) != test.code {
				t.Fatalf("error = %v, code = %s", err, PublicErrorCode(err))
			}
		})
	}
}

func TestSearchAcceptsAllFilterOperatorsWithOrLogic(t *testing.T) {
	engine := testEngine(t)
	tableID, fieldID, documentID := "table-1", "field-1", "document-1"
	mime, extension, size := "application/pdf", "pdf", int64(42)
	value := source("attachment", "attachment-1", "rev-1", "Filtered", "alpha", true)
	value.TableID, value.FieldID, value.DocumentID = &tableID, &fieldID, &documentID
	value.MIMEType, value.Extension, value.SizeBytes = &mime, &extension, &size
	upsert(t, engine, value)

	search := request("alpha")
	search.Logic = "or"
	search.Filters = []contracts.SearchFilter{
		{Field: "kind", Operator: "ne", Value: "file"},
		{Field: "tableId", Operator: "contains", Value: "table"},
		{Field: "fieldId", Operator: "eq", Value: fieldID},
		{Field: "mimeType", Operator: "ne", Value: "text/plain"},
		{Field: "extension", Operator: "contains", Value: "pd"},
		{Field: "status", Operator: "eq", Value: "active"},
		{Field: "sizeBytes", Operator: "gt", Value: 1},
		{Field: "sizeBytes", Operator: "gte", Value: 42},
		{Field: "sizeBytes", Operator: "lt", Value: 100},
		{Field: "sizeBytes", Operator: "lte", Value: 42},
		{Field: "revisionTime", Operator: "before", Value: "2026-08-13T00:00:00Z"},
		{Field: "revisionTime", Operator: "after", Value: "2026-08-11T00:00:00Z"},
	}
	if result, err := engine.Query(context.Background(), search); err != nil || len(result.Hits) != 1 {
		t.Fatalf("filtered result = %#v, %v", result, err)
	}
}

func TestRebuildCancellationRollsBackThePreviousGeneration(t *testing.T) {
	engine := testEngine(t)
	upsert(t, engine, source("record", "previous", "rev-1", "Previous", "stable corpus", true))
	sources := make([]SourceDocument, 101)
	for index := range sources {
		sources[index] = source("record", fmt.Sprintf("next-%d", index), "rev-1", "Next", "next corpus", true)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err := engine.RebuildWithProgress(ctx, sources, func(processed, _ int) {
		if processed == 100 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("rebuild error = %v", err)
	}
	if hits := query(t, engine, request("stable corpus")).Hits; len(hits) != 1 {
		t.Fatalf("rolled back hits = %#v", hits)
	}
}

func TestSourceValidationRejectsUnknownKindAndIncompleteIdentity(t *testing.T) {
	engine := testEngine(t)
	for _, value := range []SourceDocument{
		source("unknown", "record-1", "rev-1", "Unknown", "alpha", true),
		source("record", "", "rev-1", "No id", "alpha", true),
		source("record", "record-1", "", "No revision", "alpha", true),
		source("record", "record-1", "rev-1", "No time", "alpha", true),
	} {
		if value.Title == "No time" {
			value.RevisionTime = ""
		}
		err := engine.Upsert(context.Background(), value)
		if err == nil || PublicErrorCode(err) != "workspace_search.source_invalid" {
			t.Fatalf("source %#v error = %v", value, err)
		}
	}
}

func testEngine(t *testing.T) *Engine {
	t.Helper()
	engine, err := Open(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

type legacySearchDatabaseOptions struct {
	includeDisplayText bool
	blockMigration     bool
}

func createLegacySearchDatabase(
	t *testing.T,
	path string,
	options legacySearchDatabaseOptions,
) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	displayTextDefinition := ""
	if options.includeDisplayText {
		displayTextDefinition = "\n\t\t\tdisplay_text TEXT NOT NULL DEFAULT '',"
	}
	statements := []string{
		`CREATE TABLE search_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO search_meta(key,value) VALUES
			('generation','7'),
			('business_outbox_rowid','0'),
			('file_head_revision','0'),
			('mutation_revision','0'),
			('rebuild_required','0')`,
		`CREATE TABLE search_documents (
			rowid INTEGER PRIMARY KEY,
			hit_id TEXT NOT NULL UNIQUE,
			kind TEXT NOT NULL,
			canonical_id TEXT NOT NULL,
			title TEXT NOT NULL,` + displayTextDefinition + `
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
		`CREATE VIRTUAL TABLE search_terms USING fts5(
			title, normalized_text,
			content='search_documents', content_rowid='rowid',
			tokenize='unicode61 remove_diacritics 2', detail=full
		)`,
		`CREATE VIRTUAL TABLE search_cjk3 USING fts5(
			title, normalized_text,
			content='search_documents', content_rowid='rowid',
			tokenize='trigram case_sensitive 0', detail=full
		)`,
		`CREATE TRIGGER search_documents_ai AFTER INSERT ON search_documents BEGIN
			INSERT INTO search_terms(rowid,title,normalized_text)
			VALUES (new.rowid,new.title,new.normalized_text);
			INSERT INTO search_cjk3(rowid,title,normalized_text)
			VALUES (new.rowid,new.title,new.normalized_text);
		END`,
		`CREATE TRIGGER search_documents_ad AFTER DELETE ON search_documents BEGIN
			INSERT INTO search_terms(search_terms,rowid,title,normalized_text)
			VALUES ('delete',old.rowid,old.title,old.normalized_text);
			INSERT INTO search_cjk3(search_cjk3,rowid,title,normalized_text)
			VALUES ('delete',old.rowid,old.title,old.normalized_text);
		END`,
		`CREATE TRIGGER search_documents_au AFTER UPDATE ON search_documents BEGIN
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
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for canonicalID, title := range map[string]string{
		"legacy-zwj": "Legacy 👩🏽‍💻", "legacy-no-joiner": "Legacy 👩🏽💻",
	} {
		columns := []string{"hit_id", "kind", "canonical_id", "title"}
		arguments := []any{"record:" + canonicalID + ":rev-1", "record", canonicalID, title}
		if options.includeDisplayText {
			columns = append(columns, "display_text")
			arguments = append(arguments, "legacy body")
		}
		columns = append(columns,
			"normalized_text", "source_revision", "revision_time", "status",
			"is_current", "metadata_json", "open_target_json",
		)
		arguments = append(arguments,
			Normalize(title+"\nlegacy body"), "rev-1", "2026-08-12T00:00:00Z",
			"active", 1, "[]", `{"kind":"record"}`,
		)
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",")
		statement := `INSERT INTO search_documents(` + strings.Join(columns, ",") +
			`) VALUES(` + placeholders + `)`
		if _, err := db.Exec(statement, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	if options.blockMigration {
		if _, err := db.Exec(`CREATE TRIGGER block_projection_migration
			BEFORE UPDATE OF normalized_text ON search_documents BEGIN
				SELECT RAISE(ABORT, 'blocked projection migration');
			END`); err != nil {
			t.Fatal(err)
		}
	}
}

func execSearchDatabaseStatements(t *testing.T, path string, statements ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func assertLegacyProjectionState(
	t *testing.T,
	path string,
	wantProjectedColumns int,
	wantProjectionVersions int,
	wantGeneration string,
) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var projectedColumns int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('search_documents') WHERE name='projected_title'`,
	).Scan(&projectedColumns); err != nil {
		t.Fatal(err)
	}
	var projectionVersions int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM search_meta WHERE key='projection_schema_version'`,
	).Scan(&projectionVersions); err != nil {
		t.Fatal(err)
	}
	var generation string
	if err := db.QueryRow(
		`SELECT value FROM search_meta WHERE key='generation'`,
	).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if projectedColumns != wantProjectedColumns ||
		projectionVersions != wantProjectionVersions || generation != wantGeneration {
		t.Fatalf(
			"projection state: columns=%d versions=%d generation=%q",
			projectedColumns, projectionVersions, generation,
		)
	}
}

func source(kind, id, revision, title, body string, current bool) SourceDocument {
	return SourceDocument{
		Kind: kind, CanonicalID: id, SourceRevision: revision,
		Title: title, Body: body, RevisionTime: "2026-08-12T00:00:00Z",
		Status: "active", Current: current,
		Metadata:   []contracts.SearchMetadataItem{},
		OpenTarget: contracts.SearchOpenTarget{Kind: kind},
	}
}

func request(text string) contracts.SearchRequest {
	return contracts.SearchRequest{
		ContractVersion: "1.0", Query: text, Logic: "and",
		Filters: []contracts.SearchFilter{}, Sorts: []contracts.SearchSort{},
		Scope: "current", Limit: 20,
	}
}

func upsert(t *testing.T, engine *Engine, value SourceDocument) {
	t.Helper()
	if err := engine.Upsert(context.Background(), value); err != nil {
		t.Fatal(err)
	}
}

func query(t *testing.T, engine *Engine, value contracts.SearchRequest) Result {
	t.Helper()
	result, err := engine.Query(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
