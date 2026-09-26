package pluginstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	// The legacy plugins.db is only ever opened read-only; the pure-Go
	// driver matches the sidecar's existing SQLite candidate reader.
	_ "modernc.org/sqlite"
)

// legacyRecord mirrors one row of the legacy plugin_records table.
type legacyRecord struct {
	Kind       string
	ProjectKey string
	PluginID   string
	ItemKey    string
	Payload    json.RawMessage
	Seq        int64
}

var legacyKinds = map[string]struct{}{
	KindInstallation: {},
	KindRevision:     {},
	KindSetting:      {},
	KindAudit:        {},
}

// sqliteURIPathEscaper escapes the URI metacharacters that could change the
// read-only query semantics while keeping the proven "file:"+path+"?mode=ro"
// form used by the sidecar's existing SQLite candidate reader.
var sqliteURIPathEscaper = strings.NewReplacer(
	"%", "%25",
	"?", "%3F",
	"#", "%23",
)

func readOnlySQLiteURI(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	return "file:" + sqliteURIPathEscaper.Replace(strings.ReplaceAll(absolute, `\`, "/")) + "?mode=ro"
}

// readLegacyPluginRecords opens the legacy plugins.db read-only and returns
// the four record kinds in stored order. The file is never written and the
// caller only receives copies of the row payloads.
func readLegacyPluginRecords(
	ctx context.Context,
	sourcePath string,
) ([]record, error) {
	uri := readOnlySQLiteURI(sourcePath)
	if uri == "" {
		return nil, legacySourceInvalid(nil)
	}
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, legacySourceInvalid(err)
	}
	defer db.Close()
	rows, err := db.QueryContext(
		ctx,
		"SELECT kind, project_key, plugin_id, item_key, payload, rowid "+
			"FROM plugin_records ORDER BY rowid ASC",
	)
	if err != nil {
		return nil, legacySourceInvalid(err)
	}
	defer rows.Close()
	result := make([]record, 0)
	seen := make(map[string]struct{})
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var row legacyRecord
		var payload string
		var rowID int64
		if err := rows.Scan(
			&row.Kind, &row.ProjectKey, &row.PluginID,
			&row.ItemKey, &payload, &rowID,
		); err != nil {
			return nil, legacySourceInvalid(err)
		}
		_ = rowID // ordering comes from ORDER BY rowid; the value is unused
		if _, ok := legacyKinds[row.Kind]; !ok {
			return nil, legacySourceInvalid(fmt.Errorf("unknown kind %q", row.Kind))
		}
		if row.ProjectKey == "" || row.PluginID == "" || row.ItemKey == "" ||
			!json.Valid([]byte(payload)) {
			return nil, legacySourceInvalid(fmt.Errorf("row identity or payload is empty"))
		}
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%s",
			row.Kind, row.ProjectKey, row.PluginID, row.ItemKey)
		if _, duplicate := seen[key]; duplicate {
			return nil, legacySourceInvalid(fmt.Errorf("duplicate row %s", key))
		}
		seen[key] = struct{}{}
		result = append(result, record{
			Kind: row.Kind, ProjectKey: row.ProjectKey,
			PluginID: row.PluginID, ItemKey: row.ItemKey,
			Payload: json.RawMessage(payload), Seq: 0,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, legacySourceInvalid(err)
	}
	return result, nil
}

func legacySourceInvalid(err error) *Error {
	message := "legacy plugin source could not be read"
	if err != nil {
		// The detail stays on the internal seam; only the stable code and a
		// generic message cross the public boundary.
		message += ": " + err.Error()
	}
	return &Error{
		Code:    "plugin.import_source_invalid",
		Message: message,
	}
}
