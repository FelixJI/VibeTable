package conflict

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestWorkspaceCandidatesUseBusinessIdentityAndKnownSharedDefinitions(t *testing.T) {
	business := sqliteCollectionProjection{ID: "pbc_records", Name: "physical_records"}
	definitions := sqliteCollectionProjection{ID: "pbc_tables", Name: "vibetable_tables", Records: []json.RawMessage{json.RawMessage(`{"table_id":"tbl_orders","collection_id":"pbc_records","physical_name":"physical_records","display_name":"Orders"}`)}}
	shared := sqliteCollectionProjection{ID: "pbc_settings", Name: "vibetable_shared_settings", Records: []json.RawMessage{json.RawMessage(`{"logical_id":"preferences","payload_json":"{\"tableId\":\"tbl_orders\"}"}`)}}
	collections := []sqliteCollectionProjection{business, definitions, shared, {ID: "pbc_audit", Name: "vibetable_audit_events"}, {ID: "pbc_auth", Name: "users"}, {ID: "pbc_unknown", Name: "vibetable_unknown"}, {ID: "pbc_jobs", Name: "vibetable_jobs"}, {ID: "pbc_attachments", Name: "vibetable_attachment_meta"}, {ID: "pbc_versions", Name: "vibetable_attachment_versions"}, {ID: "pbc_computations", Name: "vibetable_computation_dependencies"}}
	project := func(collections []sqliteCollectionProjection) (map[string]TableState, error) {
		tables := map[string]TableState{}
		for _, collection := range collections {
			tables[collection.ID] = TableState{TableID: collection.ID, DisplayName: collection.Name, DatabaseObjectID: "immutable-db"}
		}
		return workspaceConflictCandidates(collections, tables)
	}
	candidates, err := project(collections)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]struct {
		id   string
		kind ItemKind
		name string
	}{
		"pbc_records":      {"tbl_orders", TableItem, "Orders"},
		"pbc_tables":       {"schema:tables", SettingsItem, "tables"},
		"pbc_settings":     {"metadata:shared_settings", SettingsItem, "shared_settings"},
		"pbc_attachments":  {"schema:attachment_meta", SettingsItem, "attachment_meta"},
		"pbc_versions":     {"schema:attachment_versions", SettingsItem, "attachment_versions"},
		"pbc_computations": {"schema:computation_dependencies", SettingsItem, "computation_dependencies"},
	}
	if len(candidates) != len(expected) {
		t.Fatalf("candidate scope = %#v", candidates)
	}
	for physical, want := range expected {
		state := candidates[physical]
		if state.TableID != physical || state.ItemID != want.id || state.Kind != want.kind || state.DisplayName != want.name || state.DatabaseObjectID != "immutable-db" {
			t.Fatalf("%s=%#v", physical, state)
		}
	}
	graph, err := typedWorkspaceDependencyEdges(collections)
	if err != nil || !reflect.DeepEqual(graph[shared.ID], []string{business.ID}) {
		t.Fatalf("shared dependency = %#v %v", graph, err)
	}
	for _, row := range []string{
		`{"table_id":"tbl_orders","collection_id":"missing","physical_name":"physical_records","display_name":"Orders"}`,
		`{"table_id":"tbl_orders","collection_id":"pbc_records","physical_name":"wrong","display_name":"Orders"}`,
		`{"table_id":"","collection_id":"pbc_records","physical_name":"physical_records","display_name":"Orders"}`,
	} {
		changed := append([]sqliteCollectionProjection(nil), collections...)
		changed[1].Records = []json.RawMessage{json.RawMessage(row)}
		if _, err := project(changed); !errors.Is(err, ErrCandidateDatabaseInvalid) {
			t.Fatalf("invalid mapping accepted: %s %v", row, err)
		}
	}
	duplicate := append([]sqliteCollectionProjection(nil), collections...)
	duplicate[1].Records = append(append([]json.RawMessage(nil), definitions.Records...), definitions.Records[0])
	if _, err := project(duplicate); !errors.Is(err, ErrCandidateDatabaseInvalid) {
		t.Fatalf("duplicate mapping accepted: %v", err)
	}
	for _, row := range []string{
		`{"logical_id":"preferences","payload_json":"not-json"}`,
		`{"logical_id":"preferences","payload_json":"{\"tableId\":\"missing\"}"}`,
		`{"logical_id":"preferences"}`,
	} {
		changed := append([]sqliteCollectionProjection(nil), collections...)
		changed[2].Records = []json.RawMessage{json.RawMessage(row)}
		if _, err := typedWorkspaceDependencyEdges(changed); !errors.Is(err, ErrDependencyIncomplete) {
			t.Fatalf("invalid metadata accepted: %s %v", row, err)
		}
	}
}

func TestEquivalentDiscoveryRejectsOtherSourcesAndActualEdits(t *testing.T) {
	original := productionSet(true)
	original.Local.Tables = map[string]TableState{"table": {TableID: "table", ItemID: "tbl_table", Kind: TableItem, SchemaObjectID: "schema", RecordsObjectID: "records", ViewsObjectID: "views", AttachmentsObjectID: "attachments", DatabaseObjectID: "db-original"}}
	incoming := original
	incoming.Local.SnapshotID = "later-snapshot"
	incoming.Local.Revision++
	incoming.Local.BusinessDatabaseObjectID = "later-db"
	incoming.Local.Tables = map[string]TableState{"table": original.Local.Tables["table"]}
	later := incoming.Local.Tables["table"]
	later.DatabaseObjectID = "later-db"
	later.AttachmentObjects = map[string]string{}
	incoming.Local.Tables["table"] = later
	for _, state := range []State{StatePending, StateApplying, StateApplied} {
		existing := original
		existing.State = state
		existing.Revision = 19
		existing.RootPinIDs = []string{"original-pin"}
		if !EquivalentDiscovery(existing, incoming) {
			t.Fatalf("same content not equivalent in %s", state)
		}
	}
	for _, test := range []struct {
		name   string
		change func(*Set)
	}{
		{"workspace", func(s *Set) { s.WorkspaceID = "different" }},
		{"base", func(s *Set) { s.Base.SnapshotID = "different" }},
		{"remote", func(s *Set) { s.Replica.SnapshotID = "different" }},
		{"remote-revision", func(s *Set) { s.Replica.Revision++ }},
		{"remote-object", func(s *Set) { s.Replica.BusinessDatabaseObjectID = "different" }},
		{"settings", func(s *Set) { s.Local.Settings.ObjectID = "different" }},
		{"files", func(s *Set) { s.Local.Files = map[string]FileState{"doc": {DocumentID: "doc", ContentID: "different"}} }},
		{"attachments", func(s *Set) { s.Local.AttachmentObjects = map[string]string{"file": "different"} }},
		{"schema", func(s *Set) {
			v := s.Local.Tables["table"]
			v.SchemaObjectID = "different"
			s.Local.Tables = map[string]TableState{"table": v}
		}},
		{"records", func(s *Set) {
			v := s.Local.Tables["table"]
			v.RecordsObjectID = "different"
			s.Local.Tables = map[string]TableState{"table": v}
		}},
		{"views", func(s *Set) {
			v := s.Local.Tables["table"]
			v.ViewsObjectID = "different"
			s.Local.Tables = map[string]TableState{"table": v}
		}},
		{"deleted", func(s *Set) {
			v := s.Local.Tables["table"]
			v.Deleted = true
			s.Local.Tables = map[string]TableState{"table": v}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := incoming
			test.change(&changed)
			if EquivalentDiscovery(original, changed) {
				t.Fatal("different source/content collapsed")
			}
		})
	}
	original.ReplanRequired = true
	if EquivalentDiscovery(original, incoming) {
		t.Fatal("stale rejected plan reused")
	}
}
