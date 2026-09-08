package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

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
