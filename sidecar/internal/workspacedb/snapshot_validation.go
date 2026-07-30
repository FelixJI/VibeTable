package workspacedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

const SupportedBusinessSchemaVersion uint64 = 1

var (
	ErrSnapshotDatabaseInvalid   = errors.New("workspace.snapshot_database_invalid")
	ErrBusinessSchemaUnsupported = errors.New(
		"workspace.business_schema_unsupported",
	)
)

type sqliteDeserializer interface {
	Deserialize([]byte) error
}

var requiredSnapshotTables = map[string][]string{
	"_collections": {
		"id", "system", "type", "name", "fields", "indexes",
		"options", "created", "updated",
	},
	"_migrations": {"file", "applied"},
	"workspace_v2_mutation_receipts": {
		"mutation_revision", "workspace_id", "session_epoch",
		"fence_epoch", "claim_id", "kind", "identity",
		"audit_source_sequence", "committed_at",
	},
	"vibetable_audit_outbox": {
		"event_id", "source_epoch", "source_sequence",
		"mutation_identity", "payload_hash", "payload_json",
		"occurred_at", "status", "attempts",
	},
}

var optionalSnapshotTables = map[string][]string{
	"_vibetable_sidecar_meta": {"key", "value", "updated"},
}

// ValidateSnapshot validates an immutable PocketBase database image entirely
// in memory. It is shared by local capture verification, replica ingestion and
// package import, so none of those paths can publish a merely hash-consistent
// but structurally unusable database.
func ValidateSnapshot(
	ctx context.Context,
	raw []byte,
	businessSchemaVersion uint64,
) error {
	if businessSchemaVersion != SupportedBusinessSchemaVersion {
		return ErrBusinessSchemaUnsupported
	}
	if len(raw) == 0 {
		return ErrSnapshotDatabaseInvalid
	}
	database, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		return err
	}
	defer database.Close()
	connection, err := database.Conn(ctx)
	if err != nil {
		if contextError := databaseContextError(ctx, err); contextError != nil {
			return contextError
		}
		return err
	}
	defer connection.Close()
	if err := connection.Raw(func(driverConnection any) error {
		deserializer, ok := driverConnection.(sqliteDeserializer)
		if !ok {
			return errors.New("workspace.sqlite_deserialize_unavailable")
		}
		return deserializer.Deserialize(raw)
	}); err != nil {
		return invalidDatabaseError(ctx, "deserialize", err)
	}
	var quickCheck string
	if err := connection.QueryRowContext(
		ctx, "PRAGMA quick_check",
	).Scan(&quickCheck); err != nil {
		return invalidDatabaseError(ctx, "quick_check", err)
	}
	if quickCheck != "ok" {
		return fmt.Errorf(
			"%w: quick_check=%q",
			ErrSnapshotDatabaseInvalid,
			quickCheck,
		)
	}
	foreignKeys, err := connection.QueryContext(
		ctx, "PRAGMA foreign_key_check",
	)
	if err != nil {
		return invalidDatabaseError(ctx, "foreign_key_check", err)
	}
	if foreignKeys.Next() {
		_ = foreignKeys.Close()
		return fmt.Errorf(
			"%w: foreign_key_check",
			ErrSnapshotDatabaseInvalid,
		)
	}
	if err := errors.Join(foreignKeys.Err(), foreignKeys.Close()); err != nil {
		return invalidDatabaseError(ctx, "foreign_key_check", err)
	}
	for table, requiredColumns := range requiredSnapshotTables {
		columns, err := snapshotTableColumns(ctx, connection, table)
		if err != nil {
			return err
		}
		for _, column := range requiredColumns {
			if _, found := columns[column]; !found {
				return fmt.Errorf(
					"%w: missing %s.%s",
					ErrSnapshotDatabaseInvalid,
					table,
					column,
				)
			}
		}
	}
	for table, requiredColumns := range optionalSnapshotTables {
		columns, found, err := optionalSnapshotTableColumns(
			ctx,
			connection,
			table,
		)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		for _, column := range requiredColumns {
			if _, found := columns[column]; !found {
				return fmt.Errorf(
					"%w: missing %s.%s",
					ErrSnapshotDatabaseInvalid,
					table,
					column,
				)
			}
		}
	}
	for _, constraint := range []struct {
		table   string
		columns []string
		kind    string
	}{
		{"_collections", []string{"id"}, "primary key"},
		{"_migrations", []string{"file"}, "primary key"},
		{
			"workspace_v2_mutation_receipts",
			[]string{"mutation_revision"},
			"primary key",
		},
	} {
		matches, err := snapshotPrimaryKeyMatches(
			ctx,
			connection,
			constraint.table,
			constraint.columns,
		)
		if err != nil {
			return err
		}
		if !matches {
			return fmt.Errorf(
				"%w: %s.%s %s",
				ErrSnapshotDatabaseInvalid,
				constraint.table,
				strings.Join(constraint.columns, ","),
				constraint.kind,
			)
		}
	}
	for _, constraint := range []struct {
		table   string
		columns []string
	}{
		{"_collections", []string{"name"}},
		{
			"workspace_v2_mutation_receipts",
			[]string{
				"workspace_id", "session_epoch", "fence_epoch",
				"claim_id", "kind", "identity",
			},
		},
		{"vibetable_audit_outbox", []string{"event_id"}},
		{
			"vibetable_audit_outbox",
			[]string{"source_epoch", "source_sequence"},
		},
	} {
		matches, err := snapshotUniqueConstraintMatches(
			ctx,
			connection,
			constraint.table,
			constraint.columns,
		)
		if err != nil {
			return err
		}
		if !matches {
			return fmt.Errorf(
				"%w: %s.%s unique index",
				ErrSnapshotDatabaseInvalid,
				constraint.table,
				strings.Join(constraint.columns, ","),
			)
		}
	}
	return nil
}

func optionalSnapshotTableColumns(
	ctx context.Context,
	connection *sql.Conn,
	table string,
) (map[string]struct{}, bool, error) {
	columns, err := snapshotTableColumns(ctx, connection, table)
	if err == nil {
		return columns, true, nil
	}
	if !errors.Is(err, ErrSnapshotDatabaseInvalid) {
		return nil, false, err
	}
	var count int
	if queryErr := connection.QueryRowContext(
		ctx,
		`SELECT count(*) FROM sqlite_schema
		 WHERE type = 'table' AND name = ?`,
		table,
	).Scan(&count); queryErr != nil {
		return nil, false, invalidDatabaseError(
			ctx,
			"sqlite_schema "+table,
			queryErr,
		)
	}
	return nil, count == 1, nil
}

func snapshotTableColumns(
	ctx context.Context,
	connection *sql.Conn,
	table string,
) (map[string]struct{}, error) {
	rows, err := connection.QueryContext(
		ctx,
		"SELECT name FROM pragma_table_info(?)",
		table,
	)
	if err != nil {
		return nil, invalidDatabaseError(ctx, "table_info "+table, err)
	}
	defer rows.Close()
	columns := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, invalidDatabaseError(ctx, "table_info "+table, err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, invalidDatabaseError(ctx, "table_info "+table, err)
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf(
			"%w: missing table %s",
			ErrSnapshotDatabaseInvalid,
			table,
		)
	}
	return columns, nil
}

func snapshotPrimaryKeyMatches(
	ctx context.Context,
	connection *sql.Conn,
	table string,
	expected []string,
) (bool, error) {
	rows, err := connection.QueryContext(
		ctx,
		"SELECT name, pk FROM pragma_table_info(?) ORDER BY pk",
		table,
	)
	if err != nil {
		return false, invalidDatabaseError(ctx, "table_info "+table, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		var primaryKeyOrdinal int
		if err := rows.Scan(&name, &primaryKeyOrdinal); err != nil {
			return false, invalidDatabaseError(
				ctx,
				"table_info "+table,
				err,
			)
		}
		if primaryKeyOrdinal > 0 {
			columns = append(columns, name)
		}
	}
	if err := rows.Err(); err != nil {
		return false, invalidDatabaseError(ctx, "table_info "+table, err)
	}
	return equalColumnList(columns, expected), nil
}

func snapshotUniqueConstraintMatches(
	ctx context.Context,
	connection *sql.Conn,
	table string,
	expected []string,
) (bool, error) {
	rows, err := connection.QueryContext(
		ctx,
		`SELECT name, "unique", partial FROM pragma_index_list(?)`,
		table,
	)
	if err != nil {
		return false, invalidDatabaseError(ctx, "index_list "+table, err)
	}
	var uniqueIndexes []string
	for rows.Next() {
		var name string
		var unique int
		var partial int
		if err := rows.Scan(&name, &unique, &partial); err != nil {
			_ = rows.Close()
			return false, invalidDatabaseError(
				ctx,
				"index_list "+table,
				err,
			)
		}
		if unique != 1 || partial != 0 {
			continue
		}
		uniqueIndexes = append(uniqueIndexes, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, invalidDatabaseError(ctx, "index_list "+table, err)
	}
	for _, name := range uniqueIndexes {
		escaped := strings.ReplaceAll(name, "'", "''")
		indexRows, err := connection.QueryContext(
			ctx,
			"PRAGMA index_info('"+escaped+"')",
		)
		if err != nil {
			continue
		}
		var columns []string
		for indexRows.Next() {
			var sequence int
			var columnID int
			var column string
			if err := indexRows.Scan(
				&sequence, &columnID, &column,
			); err != nil {
				columns = nil
				break
			}
			columns = append(columns, column)
		}
		rowErr := indexRows.Err()
		closeErr := indexRows.Close()
		if rowErr != nil || closeErr != nil {
			return false, invalidDatabaseError(
				ctx,
				"index_info "+table,
				errors.Join(rowErr, closeErr),
			)
		}
		if equalColumnList(columns, expected) {
			return true, nil
		}
	}
	return false, nil
}

func equalColumnList(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func invalidDatabaseError(
	ctx context.Context,
	stage string,
	err error,
) error {
	if contextError := databaseContextError(ctx, err); contextError != nil {
		return contextError
	}
	return fmt.Errorf(
		"%w: %s: %v",
		ErrSnapshotDatabaseInvalid,
		stage,
		err,
	)
}

func databaseContextError(ctx context.Context, err error) error {
	if contextError := ctx.Err(); contextError != nil {
		return contextError
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}
