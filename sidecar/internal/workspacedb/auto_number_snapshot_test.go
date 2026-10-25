package workspacedb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"strings"
	"testing"
)

func TestAutoNumberSnapshotBindsTheDateSegmentToRecordCreation(t *testing.T) {
	defaults, _ := v2.RecommendedDefaults(v2.LogicalAutoNumber)
	definition := v2.FieldDefinition{Contract: v2.Contract, Identity: v2.FieldIdentity{FieldID: "fld_number0001", PhysicalName: "f_number0001", ProviderFieldID: "pb_number0001"}, DisplayName: "合同编号", LogicalType: v2.LogicalAutoNumber, Lifecycle: v2.Lifecycle{State: v2.LifecycleRetired}, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display, AutoNumber: &v2.AutoNumberSpec{Prefix: "HT-", Start: 1, Width: 4, DateFormat: v2.AutoNumberDateFormat}}
	retiredAt := "2026-10-08T00:00:00Z"
	definition.Lifecycle.RetiredAt = &retiredAt
	rawDefinition, _ := json.Marshal(definition)
	quoted := strings.ReplaceAll(string(rawDefinition), "'", "''")
	base := fmt.Sprintf(`CREATE TABLE vibetable_fields(table_id TEXT,field_id TEXT,physical_name TEXT,data_type TEXT,definition_v2_json TEXT,auto_number_state_json TEXT);CREATE TABLE vibetable_tables(table_id TEXT,collection_id TEXT);CREATE TABLE vibetable_audit_events(table_id TEXT,record_id TEXT,operation TEXT,sequence INTEGER,data_revision INTEGER,occurred_at TEXT);CREATE TABLE records(id TEXT,f_number0001 TEXT);INSERT INTO _collections VALUES('collection',0,'base','records','[]','[]','{}','','');INSERT INTO vibetable_tables VALUES('table','collection');INSERT INTO vibetable_fields VALUES('table','fld_number0001','f_number0001','autoNumber','%s','{"highWater":2}');`, quoted)
	envelope := func(sequence int, revision int, operation, occurred string) string {
		return fmt.Sprintf("INSERT INTO vibetable_audit_events(table_id,record_id,operation,sequence,data_revision,occurred_at) VALUES('table','row1','%s',%d,%d,'%s');", operation, sequence, revision, occurred)
	}
	row := func(value string) string {
		return fmt.Sprintf("INSERT INTO records(id,f_number0001) VALUES('row1','%s');", value)
	}
	for _, tc := range []struct {
		name   string
		ledger []string
		value  string
		valid  bool
	}{
		// 01:00Z and 17:00(+08) are the same UTC day: 2026-10-08.
		{"utc_midnight", []string{envelope(1, 1, "insert", "2026-10-08 01:00:00.000Z")}, "HT-202610080001", true},
		{"same_utc_day_other_zone", []string{envelope(1, 1, "insert", "2026-10-08 17:00:00.000+08:00")}, "HT-202610080002", true},
		{"wrong_day", []string{envelope(1, 1, "insert", "2026-10-09 01:00:00.000Z")}, "HT-202610080001", false},
		// Clock rollback after creation: the later update envelope carries an
		// earlier wall clock, but only inserts define the creation date.
		{"clock_rolled_back_update", []string{
			envelope(1, 1, "insert", "2026-10-08 23:30:00.000Z"),
			envelope(2, 2, "update", "2026-10-07 08:00:00.000Z"),
		}, "HT-202610080001", true},
		// Row-history restore re-inserts the same record id on a later UTC day;
		// the fresh number's date follows the latest insert envelope.
		{"restore_same_id_next_day", []string{
			envelope(1, 1, "insert", "2026-10-08 01:00:00.000Z"),
			envelope(2, 2, "delete", "2026-10-08 02:00:00.000Z"),
			envelope(3, 3, "insert", "2026-10-09 09:00:00.000Z"),
		}, "HT-202610090001", true},
		{"restore_stale_day_number", []string{
			envelope(1, 1, "insert", "2026-10-08 01:00:00.000Z"),
			envelope(2, 2, "delete", "2026-10-08 02:00:00.000Z"),
			envelope(3, 3, "insert", "2026-10-09 09:00:00.000Z"),
		}, "HT-202610080001", false},
		{"missing_created", nil, "HT-202610080001", false},
		{"only_update_envelopes", []string{envelope(1, 1, "update", "2026-10-08 01:00:00.000Z")}, "HT-202610080001", false},
		{"zero_date_empty_created", []string{envelope(1, 1, "insert", "")}, "HT-000101010001", false},
		{"zero_date_garbage_created", []string{envelope(1, 1, "insert", "oops")}, "HT-000101010001", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := ""
			for _, statement := range tc.ledger {
				ledger += statement
			}
			fixture := base + ledger + row(tc.value)
			raw := snapshotDatabaseFixtureWithMutation(t, true, fixture)
			err := ValidateSnapshot(context.Background(), raw, SupportedBusinessSchemaVersion)
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			if !tc.valid && !errors.Is(err, ErrSnapshotDatabaseInvalid) {
				t.Fatalf("date/creation mismatch accepted: %v", err)
			}
		})
	}
}

func TestAutoNumberSnapshotRejectsMissingMalformedAndRegressedState(t *testing.T) {
	defaults, _ := v2.RecommendedDefaults(v2.LogicalAutoNumber)
	definition := v2.FieldDefinition{Contract: v2.Contract, Identity: v2.FieldIdentity{FieldID: "fld_number0001", PhysicalName: "f_number0001", ProviderFieldID: "pb_number0001"}, DisplayName: "合同编号", LogicalType: v2.LogicalAutoNumber, Lifecycle: v2.Lifecycle{State: v2.LifecycleRetired}, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display, AutoNumber: &v2.AutoNumberSpec{Prefix: "HT-", Start: 1, Width: 6}}
	retiredAt := "2026-10-08T00:00:00Z"
	definition.Lifecycle.RetiredAt = &retiredAt
	rawDefinition, _ := json.Marshal(definition)
	quoted := strings.ReplaceAll(string(rawDefinition), "'", "''")
	for _, tc := range []struct {
		name, state, value string
		valid              bool
	}{{"deleted_highest", `{"highWater":5}`, "HT-000002", true}, {"duplicate", `{"highWater":5}`, "HT-000002", false}, {"missing_column", `{"highWater":5}`, "HT-000002", false}, {"missing_state_column", `{"highWater":5}`, "HT-000002", false}, {"metadata_mismatch", `{"highWater":5}`, "HT-000002", false}, {"missing", "NULL", "HT-000002", false}, {"empty", `{}`, "HT-000002", false}, {"negative", `{"highWater":-1}`, "HT-000002", false}, {"fraction", `{"highWater":2.5}`, "HT-000002", false}, {"regressed", `{"highWater":1}`, "HT-000002", false}, {"overflow", `{"highWater":9007199254740992}`, "HT-000002", false}, {"extra", `{"highWater":5,"next":6}`, "HT-000002", false}, {"malformed", `oops`, "HT-000002", false}, {"bad_value", `{"highWater":5}`, "HT-2", false}} {
		t.Run(tc.name, func(t *testing.T) {
			state := "'" + tc.state + "'"
			if tc.state == "NULL" {
				state = "NULL"
			}
			fixture := fmt.Sprintf(`CREATE TABLE vibetable_fields(table_id TEXT,field_id TEXT,physical_name TEXT,data_type TEXT,definition_v2_json TEXT,auto_number_state_json TEXT);CREATE TABLE vibetable_tables(table_id TEXT,collection_id TEXT);CREATE TABLE records(id TEXT,f_number0001 TEXT);INSERT INTO _collections VALUES('collection',0,'base','records','[]','[]','{}','','');INSERT INTO vibetable_tables VALUES('table','collection');INSERT INTO vibetable_fields VALUES('table','fld_number0001','f_number0001','autoNumber','%s',%s);INSERT INTO records(id,f_number0001) VALUES('row1','%s');`, quoted, state, tc.value)
			switch tc.name {
			case "duplicate":
				fixture += "INSERT INTO records(id,f_number0001) VALUES('row2','HT-000002');"
			case "missing_column":
				fixture = strings.Replace(fixture, "records(id TEXT,f_number0001 TEXT)", "records(id TEXT,other TEXT)", 1)
				fixture = strings.Replace(fixture, "INSERT INTO records(id,f_number0001) VALUES('row1','", "INSERT INTO records(id,other) VALUES('row1','", 1)
			case "missing_state_column":
				fixture = strings.Replace(fixture, ",auto_number_state_json TEXT", "", 1)
				fixture = strings.Replace(fixture, ","+state+");INSERT INTO records", ");INSERT INTO records", 1)
			case "metadata_mismatch":
				fixture = strings.Replace(fixture, "'autoNumber','", "'text','", 1)
			}
			raw := snapshotDatabaseFixtureWithMutation(t, true, fixture)
			err := ValidateSnapshot(context.Background(), raw, SupportedBusinessSchemaVersion)
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			if !tc.valid && !errors.Is(err, ErrSnapshotDatabaseInvalid) {
				t.Fatalf("invalid number state accepted: %v", err)
			}
		})
	}
}
