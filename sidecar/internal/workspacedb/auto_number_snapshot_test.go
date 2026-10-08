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
			fixture := fmt.Sprintf(`CREATE TABLE vibetable_fields(table_id TEXT,field_id TEXT,physical_name TEXT,data_type TEXT,definition_v2_json TEXT,auto_number_state_json TEXT);CREATE TABLE vibetable_tables(table_id TEXT,collection_id TEXT);CREATE TABLE records(f_number0001 TEXT);INSERT INTO _collections VALUES('collection',0,'base','records','[]','[]','{}','','');INSERT INTO vibetable_tables VALUES('table','collection');INSERT INTO vibetable_fields VALUES('table','fld_number0001','f_number0001','autoNumber','%s',%s);INSERT INTO records VALUES('%s');`, quoted, state, tc.value)
			switch tc.name {
			case "duplicate":
				fixture += "INSERT INTO records VALUES('HT-000002');"
			case "missing_column":
				fixture = strings.Replace(fixture, "records(f_number0001 TEXT)", "records(other TEXT)", 1)
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
