package importvalue

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestImportAdapterAndNormalizeWriteShareRawInputCorpus(t *testing.T) {
	t.Parallel()
	corpus := loadFieldValueEntryCorpus(t)
	for _, test := range corpus.Cases {
		test := test
		t.Run(test.ID, func(t *testing.T) {
			t.Parallel()
			definition := importDefinition(t, test.LogicalType)
			if len(test.SelectOptions) > 0 {
				definition.Select = &v2.SelectSpec{}
				for _, option := range test.SelectOptions {
					definition.Select.Options = append(
						definition.Select.Options,
						v2.SelectOption{
							OptionID: option.OptionID,
							Label:    option.Label,
							State:    v2.OptionActive,
						},
					)
				}
			}
			kernel := fieldvalue.New()
			rawInput, err := kernel.NormalizeRawInput(
				context.Background(), definition, test.RawValue,
			)
			if err != nil {
				t.Fatal(err)
			}
			rawResult, err := kernel.NormalizeWrite(
				context.Background(), definition, fieldvalue.Insert, rawInput,
			)
			if err != nil {
				t.Fatal(err)
			}
			typedResult, err := kernel.NormalizeWrite(
				context.Background(), definition, fieldvalue.Insert,
				fieldvalue.Input{Supplied: true, Value: test.ProductValue},
			)
			if err != nil {
				t.Fatal(err)
			}
			imported, err := normalizeImportedCell(
				context.Background(), kernel, definition, test.RawValue,
			)
			if err != nil {
				t.Fatal(err)
			}
			if !jsonEquivalent(rawResult.ProductValue, test.ProductValue) ||
				!jsonEquivalent(typedResult.ProductValue, test.ProductValue) ||
				!jsonEquivalent(imported.ProductValue, test.ProductValue) ||
				!reflect.DeepEqual(imported, rawResult) ||
				!reflect.DeepEqual(typedResult, rawResult) {
				t.Fatalf(
					"raw=%#v typed=%#v imported=%#v expected=%#v",
					rawResult, typedResult, imported, test.ProductValue,
				)
			}
		})
	}
}

func jsonEquivalent(left any, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil &&
		string(leftJSON) == string(rightJSON)
}

type fieldValueEntryCorpus struct {
	Cases []fieldValueEntryCase `json:"cases"`
}

type fieldValueEntryCase struct {
	ID            string             `json:"id"`
	Field         string             `json:"field"`
	LogicalType   v2.LogicalType     `json:"logicalType"`
	RawValue      any                `json:"rawValue"`
	ProductValue  any                `json:"productValue"`
	SelectOptions []fieldValueOption `json:"selectOptions"`
}

type fieldValueOption struct {
	OptionID string `json:"optionId"`
	Label    string `json:"label"`
}

func loadFieldValueEntryCorpus(t *testing.T) fieldValueEntryCorpus {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve shared field-value corpus")
	}
	path := filepath.Join(
		filepath.Dir(filename), "..", "..", "..", "contracts", "schema-v2",
		"fixtures", "field-value-entry-corpus.json",
	)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var corpus fieldValueEntryCorpus
	if err := json.Unmarshal(payload, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Cases) == 0 {
		t.Fatal("shared field-value corpus must not be empty")
	}
	return corpus
}

func importDefinition(t *testing.T, logicalType v2.LogicalType) v2.FieldDefinition {
	t.Helper()
	capability, err := v2.CapabilityFor(logicalType)
	if err != nil {
		t.Fatal(err)
	}
	return v2.FieldDefinition{
		Contract: v2.Contract,
		Identity: v2.FieldIdentity{
			FieldID: "fld_01JIMPORT1", PhysicalName: "f_01jimport1",
			ProviderFieldID: "pb_01JIMPORT1",
		},
		DisplayName: "Import", LogicalType: logicalType,
		Lifecycle:   v2.Lifecycle{State: v2.LifecycleActive},
		Value:       capability.Recommended.Value,
		Constraints: capability.Recommended.Constraints,
		Storage:     capability.Recommended.Storage,
		Display:     capability.Recommended.Display,
		File:        capability.Recommended.File,
		JSON:        capability.Recommended.JSON,
	}
}
