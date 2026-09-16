package conflict

import (
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/metadata"
)

// Schema/attachment definitions use their normalized table reference columns.
// Selection and dependency scanning share these descriptors; jobs remain in the
// dependency graph but execution state is not a synchronized setting.
var workspaceSchemaRelations = []struct {
	name, left string
	rights     []string
}{
	{"vibetable_relations", "source_table_id", []string{"target_table_id"}},
	{"vibetable_formula_dependencies", "source_table_id", []string{"target_table_id"}},
	{"vibetable_computation_dependencies", "source_table_id", []string{"target_table_id"}},
}
var workspaceSchemaReferences = []struct {
	name, directKey string
	jsonKeys        []string
	synchronized    bool
}{
	{"vibetable_fields", "table_id", nil, true},
	{"vibetable_formulas", "table_id", nil, true},
	{"vibetable_lookups", "table_id", []string{"path_json"}, true},
	{"vibetable_attachment_meta", "table_id", nil, true},
	{"vibetable_attachment_versions", "table_id", nil, true},
	{"vibetable_jobs", "source_table_id", nil, false},
}

func workspaceConflictCandidates(collections []sqliteCollectionProjection, tables map[string]TableState) (map[string]TableState, error) {
	candidates := map[string]TableState{}
	byName := map[string]sqliteCollectionProjection{}
	for _, collection := range collections {
		byName[collection.Name] = collection
	}
	definitions, hasDefinitions := byName["vibetable_tables"]
	if hasDefinitions {
		logicalIDs := map[string]bool{}
		for _, raw := range definitions.Records {
			row, err := decodeCanonicalRecord(raw)
			if err != nil {
				return nil, err
			}
			logical := stringValue(row["table_id"])
			physical := stringValue(row["collection_id"])
			name := stringValue(row["display_name"])
			table, exists := tables[physical]
			if logical == "" || physical == "" || name == "" || !exists ||
				logicalIDs[logical] || candidates[physical].TableID != "" ||
				stringValue(row["physical_name"]) != table.DisplayName {
				return nil, ErrCandidateDatabaseInvalid
			}
			logicalIDs[logical] = true
			table.ItemID, table.Kind, table.DisplayName = logical, TableItem, name
			tables[physical] = table
			candidates[physical] = table
		}
	}
	schemaNames := map[string]bool{}
	if hasDefinitions {
		schemaNames[definitions.Name] = true
	}
	for _, spec := range workspaceSchemaRelations {
		schemaNames[spec.name] = true
	}
	for _, spec := range workspaceSchemaReferences {
		if spec.synchronized {
			schemaNames[spec.name] = true
		}
	}
	for _, collection := range collections {
		namespace, shared := metadata.NamespaceForCollection(collection.Name)
		if !shared && !schemaNames[collection.Name] {
			continue
		}
		if candidates[collection.ID].TableID != "" {
			return nil, ErrCandidateDatabaseInvalid
		}
		table := tables[collection.ID]
		if shared {
			table.ItemID = "metadata:" + string(namespace)
			table.DisplayName = string(namespace)
		} else {
			table.ItemID = "schema:" + strings.TrimPrefix(collection.Name, "vibetable_")
			table.DisplayName = strings.TrimPrefix(collection.Name, "vibetable_")
		}
		table.Kind = SettingsItem
		tables[collection.ID] = table
		candidates[collection.ID] = table
	}
	return candidates, nil
}
