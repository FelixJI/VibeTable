package v2_test

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestSharedFieldFixtureStrictlyDecodesAndValidates(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../../../contracts/schema-v2/fixtures/field-definition.json")
	if err != nil {
		t.Fatal(err)
	}
	var definition v2.FieldDefinition
	if err := v2.StrictDecode(raw, &definition); err != nil {
		t.Fatal(err)
	}
	if err := v2.Validate(definition); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(roundTrip, &after); err != nil {
		t.Fatal(err)
	}
	if !v2JSONEqual(before, after) {
		t.Fatalf("fixture shape drifted:\n%s", roundTrip)
	}
}

func TestValidateRejectsEnabledDefaultOutsideFieldSemantics(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../../../contracts/schema-v2/fixtures/field-definition.json")
	if err != nil {
		t.Fatal(err)
	}
	var definition v2.FieldDefinition
	if err := v2.StrictDecode(raw, &definition); err != nil {
		t.Fatal(err)
	}
	definition.Value.Default.Enabled = true
	definition.Value.Default.Value = "not-a-number"
	if err := v2.Validate(definition); err == nil {
		t.Fatal("invalid enabled number default was accepted")
	}
}

func TestCapabilityMatrixCoversEveryLogicalTypeWithExplicitDefaults(t *testing.T) {
	t.Parallel()
	if len(v2.LogicalTypes) != 18 {
		t.Fatalf("logical type matrix drifted: %d", len(v2.LogicalTypes))
	}
	for _, logicalType := range v2.LogicalTypes {
		capability, err := v2.CapabilityFor(logicalType)
		if err != nil {
			t.Fatalf("%s: %v", logicalType, err)
		}
		if capability.LogicalType != logicalType ||
			capability.Recommended.DefaultsVersion != v2.DefaultsVersion ||
			capability.Recommended.Value.Default.Enabled ||
			capability.Recommended.Value.Default.Value != nil ||
			capability.Recommended.Value.Default.Source != v2.DefaultRecommended {
			t.Fatalf("%s has incomplete recommended defaults: %#v", logicalType, capability)
		}
		if capability.NeedsPresence != (capability.Recommended.Value.Presence.Mode == v2.PresenceCompanion) {
			t.Fatalf("%s presence capability disagrees with defaults", logicalType)
		}
	}
}

func TestRecommendedFileDefaultsMatchProductDecision(t *testing.T) {
	t.Parallel()
	recommended, err := v2.RecommendedDefaults(v2.LogicalFile)
	if err != nil {
		t.Fatal(err)
	}
	if recommended.File == nil ||
		recommended.File.MaxFiles != 1 ||
		recommended.File.MaxBytesPerFile != 5*1024*1024 ||
		recommended.File.Protected ||
		len(recommended.File.AllowedMIMETypes) != 0 ||
		len(recommended.File.Thumbs) != 0 {
		t.Fatalf("unexpected file defaults: %#v", recommended.File)
	}
	capability, err := v2.CapabilityFor(v2.LogicalFile)
	if err != nil {
		t.Fatal(err)
	}
	if capability.SupportsRequired {
		t.Fatal("file capability exposed required before atomic pre-insert upload exists")
	}
}

func TestRecommendedSingleSelectDefaultsSatisfyItsCardinalityContract(t *testing.T) {
	t.Parallel()
	recommended, err := v2.RecommendedDefaults(v2.LogicalSelect)
	if err != nil {
		t.Fatal(err)
	}
	if recommended.Constraints.Selection.Max == nil ||
		*recommended.Constraints.Selection.Max != 1 {
		t.Fatalf(
			"single-select max must be one: %#v",
			recommended.Constraints.Selection,
		)
	}
}

func TestFieldDefinitionStrictlyRejectsUnknownProperties(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(validNumberDefinition())
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["providerSecret"] = true
	raw, _ = json.Marshal(object)
	var decoded v2.FieldDefinition
	if err := v2.StrictDecode(raw, &decoded); err == nil {
		t.Fatal("unknown field property was accepted")
	}
}

func TestFormulaDraftStrictlyRejectsClientSuppliedResultType(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"formula":{"language":"cel-v1","source":"1","resultType":"number"}}`)
	var draft v2.FieldDraft
	if err := v2.StrictDecode(raw, &draft); err == nil {
		t.Fatal("client-supplied formula resultType was accepted")
	}
}

func TestEverySharedNegativeFieldFixtureIsRejected(t *testing.T) {
	t.Parallel()
	baseRaw, err := os.ReadFile(
		"../../../../contracts/schema-v2/fixtures/field-definition.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	casesRaw, err := os.ReadFile(
		"../../../../contracts/schema-v2/fixtures/invalid/field-definition-cases.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(baseRaw, &base); err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string   `json:"name"`
		Path   []string `json:"path"`
		Value  any      `json:"value"`
		Remove bool     `json:"remove"`
	}
	if err := json.Unmarshal(casesRaw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range cases {
		t.Run(testCase.Name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var candidate map[string]any
			_ = json.Unmarshal(raw, &candidate)
			target := candidate
			for _, segment := range testCase.Path[:len(testCase.Path)-1] {
				target = target[segment].(map[string]any)
			}
			key := testCase.Path[len(testCase.Path)-1]
			if testCase.Remove {
				delete(target, key)
			} else {
				target[key] = testCase.Value
			}
			raw, _ = json.Marshal(candidate)
			var definition v2.FieldDefinition
			decodeErr := v2.StrictDecode(raw, &definition)
			if decodeErr == nil {
				decodeErr = v2.Validate(definition)
			}
			if decodeErr == nil {
				t.Fatalf("shared invalid case was accepted: %s", raw)
			}
		})
	}
}

func TestValidatePreservesZeroDefaultAndRequiresCompanionIdentity(t *testing.T) {
	t.Parallel()
	definition := validNumberDefinition()
	definition.Value.Default.Enabled = true
	definition.Value.Default.Value = json.Number("0")
	definition.Value.Default.Source = v2.DefaultUser
	if err := v2.Validate(definition); err != nil {
		t.Fatal(err)
	}
	definition.Value.Presence.PhysicalName = ""
	var productErr *v2.ProductError
	if err := v2.Validate(definition); !errors.As(err, &productErr) ||
		productErr.Path != "value.presence.physicalName" {
		t.Fatalf("expected stable presence error, got %#v", err)
	}
}

func TestValidateRejectsUnsupportedUniqueAndUnstableSelectOptions(t *testing.T) {
	t.Parallel()
	file := validNumberDefinition()
	file.LogicalType = v2.LogicalFile
	file.Storage.Kind = v2.StorageFile
	file.Display.Kind = v2.DisplayFile
	file.File = &v2.FileSpec{MaxFiles: 1, MaxBytesPerFile: 5 * 1024 * 1024}
	file.Constraints.Unique.Enabled = true
	var productErr *v2.ProductError
	if err := v2.Validate(file); !errors.As(err, &productErr) ||
		productErr.Code != "field.capability.unsupported" {
		t.Fatalf("expected unsupported unique, got %#v", err)
	}

	selectField := validNumberDefinition()
	selectField.LogicalType = v2.LogicalSelect
	selectField.Storage.Kind = v2.StorageSelect
	selectField.Display.Kind = v2.DisplaySelect
	one := 1
	selectField.Constraints.Selection.Max = &one
	selectField.Select = &v2.SelectSpec{Options: []v2.SelectOption{{
		OptionID: "status", Label: "进行中", Color: "blue", Order: 10, State: v2.OptionActive,
	}}}
	if err := v2.Validate(selectField); !errors.As(err, &productErr) ||
		productErr.Path != "select.options[0].optionId" {
		t.Fatalf("expected opaque option error, got %#v", err)
	}
}

func TestLookupUsesOneToEightRelationStepsAndRejectsLegacyAggregateSettings(t *testing.T) {
	t.Parallel()
	definition := validNumberDefinition()
	recommended, err := v2.RecommendedDefaults(v2.LogicalLookup)
	if err != nil {
		t.Fatal(err)
	}
	definition.LogicalType = v2.LogicalLookup
	definition.Value = recommended.Value
	definition.Constraints = recommended.Constraints
	definition.Storage = recommended.Storage
	definition.Display = recommended.Display
	definition.Lookup = &v2.LookupSpec{TargetFieldID: "fld_target"}
	for index := 0; index < 8; index++ {
		definition.Lookup.Path = append(definition.Lookup.Path, v2.LookupPathStep{
			RelationFieldID: "fld_relation_" + string(rune('a'+index)),
		})
	}
	if err := v2.Validate(definition); err != nil {
		t.Fatalf("eight-hop Lookup was rejected: %v", err)
	}
	definition.Lookup.Path = append(definition.Lookup.Path, v2.LookupPathStep{
		RelationFieldID: "fld_relation_ninth",
	})
	var productErr *v2.ProductError
	if err := v2.Validate(definition); !errors.As(err, &productErr) ||
		productErr.Code != "lookup.path.depth_limit" || productErr.Path != "lookup.path" {
		t.Fatalf("expected stable path limit error, got %#v", err)
	}
	definition.Lookup.Path = definition.Lookup.Path[:8]
	definition.Lookup.Path[0].RelationFieldID = ""
	if err := v2.Validate(definition); !errors.As(err, &productErr) ||
		productErr.Code != "lookup.path.invalid" {
		t.Fatalf("expected stable invalid path error, got %#v", err)
	}

	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["lookup"].(map[string]any)["aggregate"] = "sum"
	raw, _ = json.Marshal(object)
	var decoded v2.FieldDefinition
	if err := v2.StrictDecode(raw, &decoded); err == nil {
		t.Fatal("legacy Lookup aggregate setting was accepted")
	}
}

func validNumberDefinition() v2.FieldDefinition {
	identity := v2.FieldIdentity{
		FieldID: "fld_01JABCDE", PhysicalName: "f_01jabcde", ProviderFieldID: "pb_01JABCDE",
	}
	presence := v2.PresenceSpec{
		Mode: v2.PresenceCompanion, ProviderFieldID: "pb_01JPRESEN",
		PhysicalName: "__vt_has_f_01jabcde",
	}
	recommended, _ := v2.RecommendedDefaults(v2.LogicalNumber)
	recommended.Value.Presence = presence
	return v2.FieldDefinition{
		Contract: v2.Contract, Identity: identity, DisplayName: "金额", Help: "",
		LogicalType: v2.LogicalNumber, Lifecycle: v2.Lifecycle{State: v2.LifecycleActive},
		Value: recommended.Value, Constraints: recommended.Constraints,
		Storage: recommended.Storage, Display: recommended.Display,
	}
}

func v2JSONEqual(left, right any) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftRaw) == string(rightRaw)
}
