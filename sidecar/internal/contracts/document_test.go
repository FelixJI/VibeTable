package contracts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

func TestV1GoldenFixturesRoundTripWithoutLoss(t *testing.T) {
	fixtureDir := repositoryFixtureDir(t)
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", fixtureDir, err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	expected := []string{
		"data-changed-event.json",
		"formula-error.json",
		"managed-attachment-ref.json",
		"mutation-receipt.json",
		"mutation-request.json",
		"product-error.json",
		"rpc-catalog.json",
		"table-definition.json",
		"task-changed-event.json",
	}
	if !reflect.DeepEqual(names, expected) {
		t.Fatalf("fixture set changed: got %v, want %v", names, expected)
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join(fixtureDir, name))
			if err != nil {
				t.Fatal(err)
			}
			document, err := Decode(bytes.NewReader(source))
			if err != nil {
				t.Fatalf("Decode(): %v", err)
			}

			var encoded bytes.Buffer
			if err := Encode(&encoded, document); err != nil {
				t.Fatalf("Encode(): %v", err)
			}
			roundTripped, err := Decode(&encoded)
			if err != nil {
				t.Fatalf("Decode(round trip): %v", err)
			}
			if !reflect.DeepEqual(document, roundTripped) {
				t.Fatalf("round trip changed fixture:\nsource=%#v\nresult=%#v", document, roundTripped)
			}

			// A second semantic comparison against the source protects the
			// custom json.Number path from accidental numeric coercion.
			var sourceValue any
			decoder := json.NewDecoder(bytes.NewReader(source))
			decoder.UseNumber()
			if err := decoder.Decode(&sourceValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sourceValue, map[string]any(roundTripped)) {
				t.Fatal("round trip did not preserve source JSON semantics")
			}
		})
	}
}

func TestDecodeRejectsWrongVersionAndTrailingJSON(t *testing.T) {
	for _, payload := range []string{
		`{"contractVersion":"2.0"}`,
		`{"contractVersion":"1.0"} {}`,
		`[]`,
	} {
		if _, err := Decode(strings.NewReader(payload)); err == nil {
			t.Fatalf("Decode(%q) unexpectedly succeeded", payload)
		}
	}
}

func TestRPCCatalogPinsEveryMethodAndEventEnvelope(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(repositoryFixtureDir(t), "rpc-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		RPCMethods  []string `json:"rpcMethods"`
		EventTopics []string `json:"eventTopics"`
		RPCCases    []struct {
			Method       string         `json:"method"`
			ResultModel  string         `json:"resultModel"`
			ResultSchema map[string]any `json:"resultSchema"`
			Request      struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			} `json:"request"`
			Success struct {
				ID     string `json:"id"`
				Result any    `json:"result"`
			} `json:"success"`
			Error struct {
				ID string `json:"id"`
			} `json:"error"`
		} `json:"rpcCases"`
		EventCases []struct {
			Topic string `json:"topic"`
			Event struct {
				Topic     string `json:"topic"`
				EventType string `json:"eventType"`
			} `json:"event"`
		} `json:"eventCases"`
	}
	if err := json.Unmarshal(source, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.RPCMethods) != len(catalog.RPCCases) {
		t.Fatalf("rpc golden count = %d, want %d", len(catalog.RPCCases), len(catalog.RPCMethods))
	}
	for index, method := range catalog.RPCMethods {
		item := catalog.RPCCases[index]
		if item.Method != method || item.Request.Method != method ||
			item.Request.ID != item.Success.ID || item.Request.ID != item.Error.ID {
			t.Fatalf("rpc golden %q is inconsistent: %#v", method, item)
		}
		if item.ResultModel == "" || item.ResultSchema == nil {
			t.Fatalf("rpc golden %q has no method-specific result model/schema", method)
		}
		if result, ok := item.Success.Result.(map[string]any); ok {
			_, hasContract := result["contractVersion"]
			_, hasMethod := result["method"]
			_, hasStatus := result["status"]
			if hasContract && hasMethod && hasStatus {
				t.Fatalf("rpc golden %q still uses the generic placeholder result", method)
			}
		}
	}
	if len(catalog.EventTopics) != len(catalog.EventCases) {
		t.Fatalf("event golden count = %d, want %d", len(catalog.EventCases), len(catalog.EventTopics))
	}
	for index, topic := range catalog.EventTopics {
		item := catalog.EventCases[index]
		wireTopic := item.Event.Topic
		if wireTopic == "" {
			wireTopic = item.Event.EventType
		}
		if item.Topic != topic || wireTopic != topic {
			t.Fatalf("event golden %q is inconsistent: %#v", topic, item)
		}
	}
}

func TestRPCCatalogPinsHighRiskMethodSpecificResponseDTOs(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(repositoryFixtureDir(t), "rpc-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		RPCCases []struct {
			Method      string `json:"method"`
			ResultModel string `json:"resultModel"`
			Success     struct {
				Result any `json:"result"`
			} `json:"success"`
		} `json:"rpcCases"`
	}
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.UseNumber()
	if err := decoder.Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	cases := make(map[string]struct {
		model  string
		result any
	}, len(catalog.RPCCases))
	for _, item := range catalog.RPCCases {
		cases[item.Method] = struct {
			model  string
			result any
		}{item.ResultModel, item.Success.Result}
	}

	importCase := cases["data.applyImport"]
	if importCase.model != "ApplyImportResult" {
		t.Fatalf("data.applyImport model = %q", importCase.model)
	}
	requireExactObjectKeys(t, importCase.result,
		"collection", "createdCount", "updatedCount", "failedRows", "chunks", "requestIds")

	for _, method := range []string{"mutation.apply", "file.applyHostChange"} {
		item := cases[method]
		if item.model != "MutationReceipt" {
			t.Fatalf("%s model = %q", method, item.model)
		}
		requireExactObjectKeys(t, item.result,
			"contractVersion", "status", "changeSetId", "affectedRows",
			"computedFields", "newRevision", "emittedEvents", "warnings")
		raw, err := json.Marshal(item.result)
		if err != nil {
			t.Fatal(err)
		}
		var receipt mutation.Receipt
		if err := mutation.DecodeStrict(raw, &receipt); err != nil {
			t.Fatalf("%s does not round-trip through mutation.Receipt: %v", method, err)
		}
	}

	table := cases["schema.getTable"]
	if table.model != "TableDefinition" {
		t.Fatalf("schema.getTable model = %q", table.model)
	}
	requireObjectKeys(t, table.result, "tableId", "schemaRevision", "fields")
	rawTable, err := json.Marshal(table.result)
	if err != nil {
		t.Fatal(err)
	}
	var definition schema.TableDefinition
	tableDecoder := json.NewDecoder(bytes.NewReader(rawTable))
	tableDecoder.DisallowUnknownFields()
	if err := tableDecoder.Decode(&definition); err != nil {
		t.Fatalf("schema.getTable does not decode as schema.TableDefinition: %v", err)
	}

	plugins := cases["plugin.listCatalog"]
	if plugins.model != "PluginSnapshotList" {
		t.Fatalf("plugin.listCatalog model = %q", plugins.model)
	}
	list, ok := plugins.result.([]any)
	if !ok || len(list) == 0 {
		t.Fatal("plugin.listCatalog result must be a non-empty array")
	}
	requireObjectKeys(t, list[0], "projectKey", "pluginId", "version", "packageHash", "manifest")
}

func requireObjectKeys(t *testing.T, value any, keys ...string) {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("result is %T, want object", value)
	}
	for _, key := range keys {
		if _, exists := object[key]; !exists {
			t.Fatalf("result is missing %q", key)
		}
	}
}

func requireExactObjectKeys(t *testing.T, value any, keys ...string) {
	t.Helper()
	requireObjectKeys(t, value, keys...)
	object := value.(map[string]any)
	if len(object) != len(keys) {
		t.Fatalf("result keys = %v, want exactly %v", reflect.ValueOf(object).MapKeys(), keys)
	}
}

func repositoryFixtureDir(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	return filepath.Clean(filepath.Join(
		filepath.Dir(sourceFile),
		"..", "..", "..", "contracts", "v1", "fixtures",
	))
}
