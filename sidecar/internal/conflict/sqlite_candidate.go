package conflict

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

var ErrCandidateDatabaseInvalid = errors.New(
	"conflict.candidate_database_invalid",
)

// SQLiteProjection is the immutable, whole-table view used by both conflict
// discovery and the production dependency scanner. Component identifiers are
// content hashes; DatabaseObjectID remains the independently verified Snapshot
// object from which an apply stage can rebuild the selected table.
type SQLiteProjection struct {
	Tables map[string]TableState
	Edges  map[string][]string
}

type sqliteCollectionProjection struct {
	ID         string
	Name       string
	Type       string
	System     bool
	Schema     json.RawMessage
	Records    []json.RawMessage
	Views      []string
	Attachment []json.RawMessage
	References map[string]struct{}
	Queries    []string
}

func ProjectSQLiteDatabase(
	ctx context.Context,
	database []byte,
	databaseObjectID string,
	attachmentObjectMaps ...map[string]string,
) (SQLiteProjection, error) {
	if len(database) == 0 || strings.TrimSpace(databaseObjectID) == "" {
		return SQLiteProjection{}, ErrCandidateDatabaseInvalid
	}
	file, err := os.CreateTemp("", "vibetable-conflict-candidate-*.db")
	if err != nil {
		return SQLiteProjection{}, err
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return SQLiteProjection{}, err
	}
	if _, err := file.Write(database); err != nil {
		_ = file.Close()
		return SQLiteProjection{}, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return SQLiteProjection{}, err
	}
	if err := file.Close(); err != nil {
		return SQLiteProjection{}, err
	}
	db, err := sql.Open(
		"sqlite",
		"file:"+strings.ReplaceAll(path, `\`, `/`)+"?mode=ro",
	)
	if err != nil {
		return SQLiteProjection{}, err
	}
	defer db.Close()
	// Collection projection reads record tables while iterating _collections.
	// Keep a small bounded read pool so the nested read cannot deadlock behind
	// the still-open metadata cursor.
	db.SetMaxOpenConns(4)
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(
		&integrity,
	); err != nil || integrity != "ok" {
		return SQLiteProjection{}, errors.Join(
			ErrCandidateDatabaseInvalid, err,
		)
	}
	collections, err := readSQLiteCollections(ctx, db)
	if err != nil {
		return SQLiteProjection{}, err
	}
	nameToID := make(map[string]string, len(collections))
	for _, collection := range collections {
		nameToID[strings.ToLower(collection.Name)] = collection.ID
		nameToID[strings.ToLower(collection.ID)] = collection.ID
	}
	edges := make(map[string][]string, len(collections)+1)
	tables := make(map[string]TableState, len(collections))
	attachmentObjects := map[string]string{}
	if len(attachmentObjectMaps) > 0 &&
		attachmentObjectMaps[0] != nil {
		attachmentObjects = attachmentObjectMaps[0]
	}
	for _, collection := range collections {
		references := map[string]struct{}{}
		for reference := range collection.References {
			if _, exists := nameToID[strings.ToLower(reference)]; exists {
				reference = nameToID[strings.ToLower(reference)]
			}
			if reference != collection.ID {
				references[reference] = struct{}{}
			}
		}
		for _, query := range collection.Queries {
			for _, reference := range exactSQLiteQueryReferences(
				query, nameToID,
			) {
				if reference != collection.ID {
					references[reference] = struct{}{}
				}
			}
		}
		edges[collection.ID] = sortedSet(references)
		sortedTableAttachmentObjects := sortedAttachmentObjects(
			collection.ID, attachmentObjects,
		)
		tableAttachments := map[string]any{
			"metadata": collection.Attachment,
			"objects":  sortedTableAttachmentObjects,
		}
		tableAttachmentObjectMap := make(
			map[string]string, len(sortedTableAttachmentObjects),
		)
		for _, item := range sortedTableAttachmentObjects {
			tableAttachmentObjectMap[item.Key] = item.ObjectID
		}
		tables[collection.ID] = TableState{
			TableID:             collection.ID,
			DisplayName:         collection.Name,
			DatabaseObjectID:    databaseObjectID,
			SchemaObjectID:      conflictComponentID(collection.Schema),
			RecordsObjectID:     conflictComponentID(mustJSON(collection.Records)),
			ViewsObjectID:       conflictComponentID(mustJSON(collection.Views)),
			AttachmentsObjectID: conflictComponentID(mustJSON(tableAttachments)),
			AttachmentObjects:   tableAttachmentObjectMap,
		}
	}
	metadataEdges, err := typedWorkspaceDependencyEdges(collections)
	if err != nil {
		return SQLiteProjection{}, err
	}
	for source, targets := range metadataEdges {
		edges[source] = mergeSQLiteDependencies(
			edges[source], targets,
		)
	}
	edges[WorkspaceSettingsItemID] = []string{}
	return SQLiteProjection{Tables: tables, Edges: edges}, nil
}

func typedWorkspaceDependencyEdges(
	collections []sqliteCollectionProjection,
) (map[string][]string, error) {
	byName := map[string]sqliteCollectionProjection{}
	collectionIDs := map[string]string{}
	edges := map[string][]string{}
	for _, collection := range collections {
		byName[collection.Name] = collection
		collectionIDs[collection.ID] = collection.ID
	}
	if tables, ok := byName["vibetable_tables"]; ok {
		for _, raw := range tables.Records {
			row, err := decodeCanonicalRecord(raw)
			if err != nil {
				return nil, err
			}
			logical := stringValue(row["table_id"])
			physical := stringValue(row["collection_id"])
			if logical != "" && physical != "" {
				if collectionIDs[physical] == "" {
					return nil, ErrDependencyIncomplete
				}
				collectionIDs[logical] = physical
				if err := addTypedDependencyPair(
					edges, tables.ID, physical,
				); err != nil {
					return nil, err
				}
			}
		}
	}
	addPair := func(left, right string) error {
		left = collectionIDs[left]
		right = collectionIDs[right]
		if left == "" || right == "" {
			return ErrDependencyIncomplete
		}
		if left != right {
			edges[left] = mergeSQLiteDependencies(
				edges[left], []string{right},
			)
			edges[right] = mergeSQLiteDependencies(
				edges[right], []string{left},
			)
		}
		return nil
	}
	for _, spec := range []struct {
		name   string
		left   string
		rights []string
	}{
		{"vibetable_relations", "source_table_id",
			[]string{"target_table_id"}},
		{"vibetable_formula_dependencies", "source_table_id",
			[]string{"target_table_id"}},
	} {
		collection, ok := byName[spec.name]
		if !ok {
			continue
		}
		for _, raw := range collection.Records {
			row, err := decodeCanonicalRecord(raw)
			if err != nil {
				return nil, err
			}
			left := stringValue(row[spec.left])
			if left == "" {
				return nil, ErrDependencyIncomplete
			}
			for _, field := range spec.rights {
				right := stringValue(row[field])
				if err := addPair(left, right); err != nil {
					return nil, err
				}
				if err := addPair(collection.ID, left); err != nil {
					return nil, err
				}
				if err := addPair(collection.ID, right); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, spec := range []struct {
		name      string
		directKey string
		jsonKeys  []string
	}{
		{"vibetable_lookups", "table_id",
			[]string{"path_json"}},
		{"vibetable_jobs", "source_table_id", nil},
	} {
		collection, ok := byName[spec.name]
		if !ok {
			continue
		}
		for _, raw := range collection.Records {
			row, err := decodeCanonicalRecord(raw)
			if err != nil {
				return nil, err
			}
			node := collection.ID
			var references []string
			if spec.directKey != "" {
				value := stringValue(row[spec.directKey])
				if value != "" {
					references = append(references, value)
				}
			}
			for _, key := range spec.jsonKeys {
				found, err := typedJSONTableReferences(
					row[key], collectionIDs,
				)
				if err != nil {
					return nil, err
				}
				references = append(references, found...)
			}
			if edges[node] == nil {
				edges[node] = []string{}
			}
			for _, reference := range references {
				tableID := collectionIDs[reference]
				if tableID == "" {
					return nil, ErrDependencyIncomplete
				}
				edges[node] = mergeSQLiteDependencies(
					edges[node], []string{tableID},
				)
				edges[tableID] = mergeSQLiteDependencies(
					edges[tableID], []string{node},
				)
			}
		}
	}
	for _, name := range []string{
		"vibetable_shared_settings",
		"vibetable_dashboards",
		"vibetable_panels",
		"vibetable_presets",
		"vibetable_content_versions",
		"vibetable_interfaces",
		"vibetable_content_profiles",
		"vibetable_record_document_links",
	} {
		collection, ok := byName[name]
		if !ok {
			continue
		}
		for _, raw := range collection.Records {
			row, err := decodeCanonicalRecord(raw)
			if err != nil || stringValue(row["logical_id"]) == "" {
				return nil, ErrDependencyIncomplete
			}
			payload, exists := row["payload_json"]
			if !exists {
				return nil, ErrDependencyIncomplete
			}
			references, err := typedJSONTableReferences(
				payload, collectionIDs,
			)
			if err != nil {
				return nil, err
			}
			if edges[collection.ID] == nil {
				edges[collection.ID] = []string{}
			}
			for _, reference := range references {
				tableID := collectionIDs[reference]
				if tableID == "" {
					return nil, ErrDependencyIncomplete
				}
				edges[collection.ID] = mergeSQLiteDependencies(
					edges[collection.ID], []string{tableID},
				)
				edges[tableID] = mergeSQLiteDependencies(
					edges[tableID], []string{collection.ID},
				)
			}
		}
	}
	return edges, nil
}

func addTypedDependencyPair(
	edges map[string][]string,
	left string,
	right string,
) error {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return ErrDependencyIncomplete
	}
	if left != right {
		edges[left] = mergeSQLiteDependencies(edges[left], []string{right})
		edges[right] = mergeSQLiteDependencies(edges[right], []string{left})
	}
	return nil
}

func decodeCanonicalRecord(raw json.RawMessage) (map[string]any, error) {
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, ErrDependencyIncomplete
	}
	return row, nil
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		text, _ := typed["value"].(string)
		return strings.TrimSpace(text)
	default:
		return ""
	}
}

func typedJSONTableReferences(
	value any,
	known map[string]string,
) ([]string, error) {
	raw := stringValue(value)
	if raw == "" {
		if value == nil {
			return nil, nil
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, ErrDependencyIncomplete
		}
		raw = string(encoded)
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, ErrDependencyIncomplete
	}
	set := map[string]struct{}{}
	var walk func(any, string) error
	walk = func(current any, key string) error {
		switch typed := current.(type) {
		case map[string]any:
			for childKey, child := range typed {
				if err := walk(child, childKey); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child, key); err != nil {
					return err
				}
			}
		case string:
			lower := strings.ToLower(key)
			if strings.Contains(lower, "table") ||
				strings.Contains(lower, "collection") {
				if known[typed] == "" {
					return ErrDependencyIncomplete
				}
				set[typed] = struct{}{}
			}
		default:
			lower := strings.ToLower(key)
			if strings.Contains(lower, "table") ||
				strings.Contains(lower, "collection") {
				return ErrDependencyIncomplete
			}
		}
		return nil
	}
	if err := walk(decoded, ""); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func mergeSQLiteDependencies(
	current []string,
	next []string,
) []string {
	set := map[string]struct{}{}
	for _, value := range append(current, next...) {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

type sqliteAttachmentObject struct {
	Key      string `json:"key"`
	ObjectID string `json:"objectId"`
}

func sortedAttachmentObjects(
	collectionID string,
	objects map[string]string,
) []sqliteAttachmentObject {
	prefix := collectionID + "/"
	result := make([]sqliteAttachmentObject, 0)
	for key, objectID := range objects {
		if strings.HasPrefix(key, prefix) {
			result = append(result, sqliteAttachmentObject{
				Key: key, ObjectID: objectID,
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Key < result[j].Key
	})
	return result
}

func readSQLiteCollections(
	ctx context.Context,
	db *sql.DB,
) ([]sqliteCollectionProjection, error) {
	columns, err := sqliteColumns(ctx, db, "_collections")
	if err != nil || len(columns) == 0 {
		return nil, errors.Join(ErrCandidateDatabaseInvalid, err)
	}
	idIndex := indexOf(columns, "id")
	nameIndex := indexOf(columns, "name")
	typeIndex := indexOf(columns, "type")
	systemIndex := indexOf(columns, "system")
	if idIndex < 0 || nameIndex < 0 ||
		typeIndex < 0 || systemIndex < 0 {
		return nil, ErrCandidateDatabaseInvalid
	}
	rows, err := db.QueryContext(
		ctx,
		"SELECT "+quotedColumns(columns)+" FROM "+quoteSQLite("_collections"),
	)
	if err != nil {
		return nil, errors.Join(ErrCandidateDatabaseInvalid, err)
	}
	defer rows.Close()
	var result []sqliteCollectionProjection
	for rows.Next() {
		values, err := scanSQLiteRow(rows, len(columns))
		if err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for index, column := range columns {
			row[column] = canonicalSQLiteValue(values[index])
		}
		id := sqliteString(values[idIndex])
		name := sqliteString(values[nameIndex])
		if strings.TrimSpace(id) == "" || strings.TrimSpace(name) == "" {
			return nil, ErrCandidateDatabaseInvalid
		}
		system := sqliteBool(values[systemIndex])
		if system {
			continue
		}
		schema, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		records, _, err := readSQLiteTableRows(ctx, db, name)
		if err != nil {
			return nil, err
		}
		views, err := sqliteViewsForTable(ctx, db, name)
		if err != nil {
			return nil, err
		}
		fileFields := map[string]struct{}{}
		references := map[string]struct{}{}
		for _, value := range values {
			collectCollectionMetadata(value, fileFields, references)
		}
		var queries []string
		for index, column := range columns {
			if strings.EqualFold(column, "viewQuery") ||
				strings.EqualFold(column, "view_query") {
				queries = append(
					queries, sqliteString(values[index]),
				)
			}
		}
		queries = append(queries, views...)
		attachments := attachmentRows(records, fileFields)
		result = append(result, sqliteCollectionProjection{
			ID: id, Name: name,
			Type:       sqliteString(values[typeIndex]),
			System:     system,
			Schema:     schema,
			Records:    records,
			Views:      views,
			Attachment: attachments,
			References: references,
			Queries:    queries,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func exactSQLiteQueryReferences(
	query string,
	known map[string]string,
) []string {
	set := map[string]struct{}{}
	for _, token := range sqliteQueryIdentifiers(query) {
		if id := known[strings.ToLower(token)]; id != "" {
			set[id] = struct{}{}
		}
	}
	return sortedSet(set)
}

func sqliteQueryIdentifiers(query string) []string {
	var result []string
	for index := 0; index < len(query); {
		switch {
		case query[index] == '\'':
			index++
			for index < len(query) {
				if query[index] != '\'' {
					index++
					continue
				}
				if index+1 < len(query) && query[index+1] == '\'' {
					index += 2
					continue
				}
				index++
				break
			}
		case index+1 < len(query) &&
			query[index] == '-' && query[index+1] == '-':
			index += 2
			for index < len(query) && query[index] != '\n' {
				index++
			}
		case index+1 < len(query) &&
			query[index] == '/' && query[index+1] == '*':
			index += 2
			for index+1 < len(query) &&
				(query[index] != '*' || query[index+1] != '/') {
				index++
			}
			if index+1 < len(query) {
				index += 2
			}
		case query[index] == '"' || query[index] == '`':
			quote := query[index]
			index++
			var token strings.Builder
			for index < len(query) {
				if query[index] != quote {
					token.WriteByte(query[index])
					index++
					continue
				}
				if index+1 < len(query) && query[index+1] == quote {
					token.WriteByte(quote)
					index += 2
					continue
				}
				index++
				break
			}
			if token.Len() != 0 {
				result = append(result, token.String())
			}
		case query[index] == '[':
			index++
			var token strings.Builder
			for index < len(query) {
				if query[index] != ']' {
					token.WriteByte(query[index])
					index++
					continue
				}
				if index+1 < len(query) && query[index+1] == ']' {
					token.WriteByte(']')
					index += 2
					continue
				}
				index++
				break
			}
			if token.Len() != 0 {
				result = append(result, token.String())
			}
		case sqliteIdentifierStart(query[index]):
			start := index
			index++
			for index < len(query) &&
				sqliteIdentifierPart(query[index]) {
				index++
			}
			result = append(result, query[start:index])
		default:
			index++
		}
	}
	return result
}

func sqliteIdentifierStart(value byte) bool {
	return value == '_' ||
		value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= 0x80
}

func sqliteIdentifierPart(value byte) bool {
	return sqliteIdentifierStart(value) ||
		value >= '0' && value <= '9' ||
		value == '$'
}

func sqliteColumns(
	ctx context.Context,
	db *sql.DB,
	table string,
) ([]string, error) {
	rows, err := db.QueryContext(
		ctx, "PRAGMA table_info("+quoteSQLite(table)+")",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var (
			ordinal      int
			name         string
			dataType     string
			notNull      int
			defaultValue any
			primaryKey   int
		)
		if err := rows.Scan(
			&ordinal, &name, &dataType, &notNull,
			&defaultValue, &primaryKey,
		); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}

func readSQLiteTableRows(
	ctx context.Context,
	db *sql.DB,
	table string,
) ([]json.RawMessage, string, error) {
	columns, err := sqliteColumns(ctx, db, table)
	if err != nil {
		return nil, "", err
	}
	if len(columns) == 0 {
		// View collections have no physical table in some PocketBase versions.
		return []json.RawMessage{}, "", nil
	}
	rows, err := db.QueryContext(
		ctx,
		"SELECT "+quotedColumns(columns)+" FROM "+quoteSQLite(table),
	)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var (
		result []json.RawMessage
		search strings.Builder
	)
	for rows.Next() {
		values, err := scanSQLiteRow(rows, len(columns))
		if err != nil {
			return nil, "", err
		}
		row := make(map[string]any, len(columns))
		for index, column := range columns {
			row[column] = canonicalSQLiteValue(values[index])
			search.WriteString(sqliteString(values[index]))
			search.WriteByte(' ')
		}
		raw, err := json.Marshal(row)
		if err != nil {
			return nil, "", err
		}
		result = append(result, raw)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	sort.Slice(result, func(i, j int) bool {
		return string(result[i]) < string(result[j])
	})
	return result, search.String(), nil
}

func sqliteViewsForTable(
	ctx context.Context,
	db *sql.DB,
	table string,
) ([]string, error) {
	rows, err := db.QueryContext(
		ctx,
		`SELECT name, sql FROM sqlite_schema
		 WHERE type = 'view' AND sql IS NOT NULL
		 ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var name, statement string
		if err := rows.Scan(&name, &statement); err != nil {
			return nil, err
		}
		if strings.Contains(
			strings.ToLower(statement),
			strings.ToLower(table),
		) {
			result = append(result, name+"\x00"+statement)
		}
	}
	return result, rows.Err()
}

func attachmentRows(
	records []json.RawMessage,
	fields map[string]struct{},
) []json.RawMessage {
	result := []json.RawMessage{}
	if len(fields) == 0 {
		return result
	}
	for _, raw := range records {
		var row map[string]any
		if json.Unmarshal(raw, &row) != nil {
			continue
		}
		attachment := map[string]any{"id": row["id"]}
		for field := range fields {
			attachment[field] = row[field]
		}
		encoded, err := json.Marshal(attachment)
		if err == nil {
			result = append(result, encoded)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return string(result[i]) < string(result[j])
	})
	return result
}

func collectCollectionMetadata(
	value any,
	fileFields map[string]struct{},
	references map[string]struct{},
) {
	raw := sqliteString(value)
	if raw == "" || (raw[0] != '{' && raw[0] != '[') {
		return
	}
	var decoded any
	if json.Unmarshal([]byte(raw), &decoded) != nil {
		return
	}
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case []any:
			for _, item := range typed {
				visit(item)
			}
		case map[string]any:
			fieldType, _ := typed["type"].(string)
			name, _ := typed["name"].(string)
			if fieldType == "file" && name != "" {
				fileFields[name] = struct{}{}
			}
			for key, item := range typed {
				if strings.EqualFold(key, "collectionId") {
					if reference, ok := item.(string); ok &&
						strings.TrimSpace(reference) != "" {
						references[reference] = struct{}{}
					}
				}
				visit(item)
			}
		}
	}
	visit(decoded)
}

func scanSQLiteRow(rows *sql.Rows, count int) ([]any, error) {
	values := make([]any, count)
	destinations := make([]any, count)
	for index := range values {
		destinations[index] = &values[index]
	}
	if err := rows.Scan(destinations...); err != nil {
		return nil, err
	}
	return values, nil
}

func canonicalSQLiteValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return map[string]string{
			"type":  "bytes",
			"value": base64.StdEncoding.EncodeToString(typed),
		}
	case int64:
		return map[string]string{
			"type": "integer", "value": strconv.FormatInt(typed, 10),
		}
	case float64:
		return map[string]string{
			"type": "real", "value": strconv.FormatFloat(typed, 'g', -1, 64),
		}
	case bool:
		return map[string]any{"type": "boolean", "value": typed}
	default:
		return map[string]string{
			"type": "text", "value": fmt.Sprint(typed),
		}
	}
}

func sqliteString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func sqliteBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case int64:
		return typed != 0
	case []byte:
		value = string(typed)
	}
	parsed, _ := strconv.ParseBool(fmt.Sprint(value))
	return parsed || fmt.Sprint(value) == "1"
}

func quotedColumns(columns []string) string {
	result := make([]string, len(columns))
	for index, column := range columns {
		result[index] = quoteSQLite(column)
	}
	return strings.Join(result, ",")
}

func quoteSQLite(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func indexOf(values []string, target string) int {
	for index, value := range values {
		if strings.EqualFold(value, target) {
			return index
		}
	}
	return -1
}

func conflictComponentID(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func mustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}
