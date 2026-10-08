package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

type describeOracleCorpus struct {
	Tables map[string]v2.SchemaSnapshot `json:"tables"`
	Cases  []describeOracleCase         `json:"cases"`
}
type describeOracleCase struct {
	TableID    string                 `json:"tableId"`
	Generation json.Number            `json:"generation"`
	Catalog    relation.CatalogResult `json:"catalog"`
	Describe   json.RawMessage        `json:"describe"`
	LookupList json.RawMessage        `json:"lookupList"`
}

func TestSchemaDescribeProjectionCorpus(t *testing.T) {
	wire, err := os.ReadFile("testdata/schema_describe_oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	assertDescribeOracle(t, wire)
}

func TestSchemaDescribeProjectionPreservesLookupLoadError(t *testing.T) {
	wire, err := os.ReadFile("testdata/schema_describe_oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus describeOracleCorpus
	if err := json.Unmarshal(wire, &corpus); err != nil {
		t.Fatal(err)
	}
	// The second captured table has real cross-table lookup paths.
	sample := corpus.Cases[1]
	for _, failure := range []error{context.Canceled, fmt.Errorf("field storage failure")} {
		got, err := projectSchemaDescribe(corpus.Tables[sample.TableID], sample.Catalog, sample.Generation, func(string) (v2.SchemaSnapshot, error) { return v2.SchemaSnapshot{}, failure })
		if got != nil || err != failure {
			t.Fatalf("lookup read error lost: got=%v err=%v", got, err)
		}
	}
}

func TestSchemaDescribeProjectionRejectsInvalidSnapshot(t *testing.T) {
	got, err := projectSchemaDescribe(v2.SchemaSnapshot{}, relation.CatalogResult{}, json.Number("1"), func(string) (v2.SchemaSnapshot, error) {
		t.Fatal("invalid snapshot must fail before loading lookup targets")
		return v2.SchemaSnapshot{}, nil
	})
	if got != nil || err == nil || !strings.Contains(err.Error(), "validate schema Product snapshot") {
		t.Fatalf("invalid snapshot: got=%v err=%v", got, err)
	}
}

func TestSchemaDescribeProjectionRejectsBrokenLookupMetadata(t *testing.T) {
	for _, sample := range []struct {
		name   string
		lookup *v2.LookupSpec
		want   string
	}{
		{"missing definition", nil, "Lookup field omitted its lookup definition"},
		{"empty path", &v2.LookupSpec{}, "Lookup field omitted its relation path"},
		{"missing relation", &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: "missing"}}}, "Lookup target field is unavailable"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			got, err := describeLookupType(v2.SchemaSnapshot{}, v2.FieldDefinition{Lookup: sample.lookup}, nil, func(string) (v2.SchemaSnapshot, error) {
				t.Fatal("invalid path must fail before loading targets")
				return v2.SchemaSnapshot{}, nil
			})
			if got != "" || err == nil || err.Error() != sample.want {
				t.Fatalf("got=%q err=%v; want %q", got, err, sample.want)
			}
		})
	}
}

func assertDescribeOracle(t *testing.T, wire []byte) {
	t.Helper()
	var corpus describeOracleCorpus
	if err := json.Unmarshal(wire, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, sample := range corpus.Cases {
		t.Run(sample.TableID, func(t *testing.T) {
			reads := map[string]int{}
			got, err := projectSchemaDescribe(corpus.Tables[sample.TableID], sample.Catalog, sample.Generation, func(id string) (v2.SchemaSnapshot, error) {
				reads[id]++
				if reads[id] > 1 {
					t.Fatalf("lookup target %s was not request-cached", id)
				}
				table, found := corpus.Tables[id]
				if !found {
					return table, fmt.Errorf("uncaptured target %s", id)
				}
				return table, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			assertColumnDisplayWiring(t, corpus.Tables[sample.TableID], got)
			// The frozen wire predates the column `display` field and the relation
			// display projection keys. Project away exactly those new keys so every
			// historical field stays compared; both new contracts are asserted
			// separately below.
			stripColumnDisplayField(got)
			stripRelationDisplayFields(got)
			actual, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var wantValue, gotValue any
			if err := json.Unmarshal(sample.Describe, &wantValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(actual, &gotValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotValue, wantValue) {
				t.Fatalf("got %s\nwant %s", actual, sample.Describe)
			}
			var lookupList struct {
				Revision string `json:"lookupRevision"`
			}
			if err := json.Unmarshal(sample.LookupList, &lookupList); err != nil {
				t.Fatal(err)
			}
			if lookupList.Revision == "" {
				t.Fatal("old lookup.list consumer did not produce a revision")
			}
			if got["schema"].(map[string]any)["lookupRevision"] != lookupList.Revision {
				t.Fatal("old lookup.list consumer revision differs")
			}
		})
	}
}

// stripColumnDisplayField removes only the newly added `display` key from
// every projected column. It must not touch any historical wire field.
func stripColumnDisplayField(result map[string]any) {
	columns := result["schema"].(map[string]any)["columns"].([]any)
	for _, column := range columns {
		if record, ok := column.(map[string]any); ok {
			delete(record, "display")
		}
	}
}

// assertColumnDisplayWiring pins the new column `display` contract against
// the frozen corpus inputs: every field column carries the authoritative
// DisplaySpec of its field verbatim, and the system id column stays null.
func assertColumnDisplayWiring(t *testing.T, snapshot v2.SchemaSnapshot, result map[string]any) {
	t.Helper()
	columns := result["schema"].(map[string]any)["columns"].([]any)
	byFieldID := map[string]map[string]any{}
	for _, column := range columns {
		record := column.(map[string]any)
		if fieldID, ok := record["fieldId"].(string); ok {
			byFieldID[fieldID] = record
		}
	}
	for _, field := range snapshot.Fields {
		column, found := byFieldID[field.Identity.FieldID]
		if !found {
			t.Fatalf("column for field %s missing", field.Identity.FieldID)
		}
		got, err := json.Marshal(column["display"])
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(field.Display)
		if err != nil {
			t.Fatal(err)
		}
		var gotValue, wantValue any
		if err := json.Unmarshal(got, &gotValue); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(want, &wantValue); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotValue, wantValue) {
			t.Fatalf("column display for %s: got %s want %s", field.Identity.FieldID, got, want)
		}
	}
	if idColumn, found := byFieldID["id"]; found {
		if idColumn["display"] != nil {
			t.Fatalf("system id column must not carry a display spec: %v", idColumn["display"])
		}
	}
}

// stripRelationDisplayFields removes only the newly added relation display
// projection keys (displayFieldId, displayFieldInfo, fallbackDisplayFieldInfo)
// from every normalized relation. It must not touch any historical field.
func stripRelationDisplayFields(result map[string]any) {
	relations := result["schema"].(map[string]any)["normalizedRelations"].([]any)
	for _, item := range relations {
		if record, ok := item.(map[string]any); ok {
			delete(record, "displayFieldId")
			delete(record, "displayFieldInfo")
			delete(record, "fallbackDisplayFieldInfo")
		}
	}
}

// TestSchemaDescribeProjectsRelationDisplayInfo pins that the normalized
// relation entries forward the relation catalog's display projection keys
// verbatim, null when the catalog descriptor carries none.
func TestSchemaDescribeProjectsRelationDisplayInfo(t *testing.T) {
	wire, err := os.ReadFile("testdata/schema_describe_oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus describeOracleCorpus
	if err := json.Unmarshal(wire, &corpus); err != nil {
		t.Fatal(err)
	}
	sample := corpus.Cases[0]
	catalog := sample.Catalog
	spec := v2.DisplaySpec{Kind: v2.DisplayNumber, Preset: "number", DisplayScale: 4, ScaleMode: "max", UseGrouping: true}
	for index := range catalog.Relations {
		if catalog.Relations[index].SourceFieldID == "" {
			continue
		}
		catalog.Relations[index].DisplayFieldID = "fld_pt602dxd6ayafnq2n1f4"
		catalog.Relations[index].DisplayFieldInfo = &relation.DisplayFieldInfo{
			FieldID: "fld_pt602dxd6ayafnq2n1f4", DataType: "decimal", Display: &spec,
		}
		catalog.Relations[index].FallbackDisplayFieldInfo = nil
		break
	}
	got, err := projectSchemaDescribe(corpus.Tables[sample.TableID], catalog, sample.Generation, func(id string) (v2.SchemaSnapshot, error) {
		return corpus.Tables[id], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	relations := got["schema"].(map[string]any)["normalizedRelations"].([]any)
	first := relations[0].(map[string]any)
	if first["displayFieldId"] != "fld_pt602dxd6ayafnq2n1f4" {
		t.Fatalf("displayFieldId = %#v", first["displayFieldId"])
	}
	info := first["displayFieldInfo"].(map[string]any)
	if info["fieldId"] != "fld_pt602dxd6ayafnq2n1f4" || info["dataType"] != "decimal" || info["display"] == nil {
		t.Fatalf("displayFieldInfo = %#v", info)
	}
	if first["fallbackDisplayFieldInfo"] != nil {
		t.Fatalf("fallbackDisplayFieldInfo = %#v", first["fallbackDisplayFieldInfo"])
	}
	for _, item := range relations[1:] {
		record := item.(map[string]any)
		if record["displayFieldId"] != nil || record["displayFieldInfo"] != nil || record["fallbackDisplayFieldInfo"] != nil {
			t.Fatalf("undescribed relation carried display info: %#v", record)
		}
	}
}

// TestSchemaDescribeProjectsDisplaySpecWithExplicitShape locks the complete
// wire shape of the projected column display by decoding the actual JSON wire
// and comparing it against a hand-written expectation, independent of the
// projection implementation.
func TestSchemaDescribeProjectsDisplaySpecWithExplicitShape(t *testing.T) {
	wire, err := os.ReadFile("testdata/schema_describe_oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus describeOracleCorpus
	if err := json.Unmarshal(wire, &corpus); err != nil {
		t.Fatal(err)
	}
	sample := corpus.Cases[0]
	got, err := projectSchemaDescribe(corpus.Tables[sample.TableID], sample.Catalog, sample.Generation, func(id string) (v2.SchemaSnapshot, error) {
		table, found := corpus.Tables[id]
		if !found {
			return v2.SchemaSnapshot{}, fmt.Errorf("uncaptured target %s", id)
		}
		return table, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	resultWire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(resultWire, &decoded); err != nil {
		t.Fatal(err)
	}
	displayByField := map[string]any{}
	for _, column := range decoded["schema"].(map[string]any)["columns"].([]any) {
		record := column.(map[string]any)
		displayByField[record["fieldId"].(string)] = record["display"]
	}
	// Hand-written wire expectation for the captured number field with
	// displayScale 4 (frozen corpus input), decoded through the same JSON path.
	wantNumber := `{
		"kind": "number", "preset": "number", "displayScale": 4, "scaleMode": "max",
		"trimTrailingZeros": true, "useGrouping": true, "currency": "CNY",
		"percentStorage": "ratio", "unit": null, "precision": "minute",
		"timezone": "system", "mode": "default", "indent": 0,
		"trueLabel": "是", "falseLabel": "否"
	}`
	var wantNumberValue any
	if err := json.Unmarshal([]byte(wantNumber), &wantNumberValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(displayByField["fld_pt602dxd6ayafnq2n1f4"], wantNumberValue) {
		t.Fatalf("number column display = %#v", displayByField["fld_pt602dxd6ayafnq2n1f4"])
	}
	formulaDisplay, _ := displayByField["fld_2mjn8rpxh8gbc4hwh322"].(map[string]any)
	if formulaDisplay == nil || formulaDisplay["kind"] != "readonly" || formulaDisplay["displayScale"] != float64(2) {
		t.Fatalf("formula column display = %#v", formulaDisplay)
	}
	if displayByField["id"] != nil {
		t.Fatalf("system id column must not carry a display spec: %#v", displayByField["id"])
	}
}

func TestSchemaDescribeProjectsFormulaListElementType(t *testing.T) {
	raw, err := os.ReadFile("testdata/schema_describe_oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus describeOracleCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	sample := corpus.Cases[0]
	snapshot := corpus.Tables[sample.TableID]
	compiler := formula.NewCompiler(formula.DefaultLimits())
	inferred, failure := compiler.InferV2Value(formula.V2Table{TableID: snapshot.TableID, Fields: snapshot.Fields}, "UNIQUE([1.0, 2.0, 1.0])")
	if failure != nil || inferred.LogicalType != v2.LogicalJSON || inferred.ElementType != v2.LogicalNumber {
		t.Fatalf("real list inference = %#v, %v", inferred, failure)
	}
	for i := range snapshot.Fields {
		field := &snapshot.Fields[i]
		if field.LogicalType == v2.LogicalFormula {
			field.Formula = &v2.FormulaSpec{Language: "cel-v2", Source: "UNIQUE([1.0, 2.0, 1.0])", ResultType: inferred.LogicalType, ResultElementType: inferred.ElementType}
			field.Storage.Options.OnlyInt = false
		}
	}
	got, err := projectSchemaDescribe(snapshot, sample.Catalog, sample.Generation, func(id string) (v2.SchemaSnapshot, error) { return corpus.Tables[id], nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range got["schema"].(map[string]any)["columns"].([]any) {
		column := item.(map[string]any)
		if column["kind"] == "formula" {
			if column["dataType"] != "json" || column["resultElementType"] != v2.LogicalNumber || column["editable"] != false {
				t.Fatalf("typed Formula list projection = %#v", column)
			}
			return
		}
	}
	t.Fatal("Formula list column missing")
}
