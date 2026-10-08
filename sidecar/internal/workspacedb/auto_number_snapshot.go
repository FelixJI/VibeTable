package workspacedb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/autonumber"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func validateAutoNumberSnapshot(ctx context.Context, connection *sql.Conn) error {
	columns, found, err := optionalSnapshotTableColumns(ctx, connection, "vibetable_fields")
	if err != nil || !found {
		return err
	}
	kindExpr := "''"
	if _, ok := columns["data_type"]; ok {
		kindExpr = "data_type"
	}
	stateExpr := "NULL"
	if _, ok := columns[autonumber.StateColumn]; ok {
		stateExpr = autonumber.StateColumn
	}
	definitionExpr := "NULL"
	if _, ok := columns["definition_v2_json"]; ok {
		definitionExpr = "definition_v2_json"
	}
	rows, err := connection.QueryContext(ctx, "SELECT table_id,field_id,physical_name,"+kindExpr+","+definitionExpr+","+stateExpr+" FROM vibetable_fields")
	if err != nil {
		return invalidDatabaseError(ctx, "auto number metadata", err)
	}
	type entry struct {
		table, field, physical, kind string
		definition, state            sql.NullString
	}
	entries := []entry{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.table, &e.field, &e.physical, &e.kind, &e.definition, &e.state); err != nil {
			_ = rows.Close()
			return invalidDatabaseError(ctx, "auto number metadata", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return invalidDatabaseError(ctx, "auto number metadata", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	seenFields := map[string]bool{}
	for _, e := range entries {
		var discriminator struct {
			LogicalType string `json:"logicalType"`
		}
		_ = json.Unmarshal([]byte(e.definition.String), &discriminator)
		if e.kind != "autoNumber" && discriminator.LogicalType != "autoNumber" {
			continue
		}
		fail := func(reason string) error {
			return fmt.Errorf("%w: auto number %s/%s: %s", ErrSnapshotDatabaseInvalid, e.table, e.field, reason)
		}
		var definition v2.FieldDefinition
		if err := v2.StrictDecode([]byte(e.definition.String), &definition); err != nil {
			return fail("invalid definition: " + err.Error())
		}
		if err := v2.Validate(definition); err != nil {
			return fail(err.Error())
		}
		if definition.LogicalType != v2.LogicalAutoNumber || e.kind != "autoNumber" || definition.AutoNumber == nil || definition.Identity.FieldID != e.field || definition.Identity.PhysicalName != e.physical {
			return fail("metadata/definition mismatch")
		}
		key := e.table + "/" + e.field
		if seenFields[key] {
			return fail("duplicate field metadata")
		}
		seenFields[key] = true
		highWater, err := autonumber.DecodeState([]byte(e.state.String), *definition.AutoNumber)
		if err != nil {
			return fail(err.Error())
		}
		var physicalTable string
		var owners int
		if err := connection.QueryRowContext(ctx, "SELECT MIN(c.name),COUNT(*) FROM vibetable_tables t JOIN _collections c ON c.id=t.collection_id WHERE t.table_id=?", e.table).Scan(&physicalTable, &owners); err != nil {
			return fail("owning table is missing or ambiguous: " + err.Error())
		}
		if owners != 1 {
			return fail("owning table is ambiguous")
		}
		quote := func(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
		values, err := connection.QueryContext(ctx, "SELECT "+quote(e.physical)+",typeof("+quote(e.physical)+") FROM "+quote(physicalTable))
		if err != nil {
			return fail("physical column unavailable: " + err.Error())
		}
		used := map[string]bool{}
		for values.Next() {
			var value sql.NullString
			var storageType string
			if err := values.Scan(&value, &storageType); err != nil {
				_ = values.Close()
				return fail(err.Error())
			}
			if !value.Valid || storageType != "text" {
				_ = values.Close()
				return fail("stored number is missing or not text")
			}
			sequence, err := v2.ParseAutoNumberValue(*definition.AutoNumber, value.String)
			if err != nil || sequence > highWater || used[value.String] {
				_ = values.Close()
				return fail("stored number has invalid format, exceeds highWater, or duplicates another row")
			}
			used[value.String] = true
		}
		err = values.Err()
		closeErr := values.Close()
		if err != nil {
			return fail(err.Error())
		}
		if closeErr != nil {
			return fail(closeErr.Error())
		}
	}
	return nil
}
