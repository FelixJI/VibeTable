package schema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

func TestTypedEnumCodecKeepsStringsAndDistinguishesJSONScalarTypes(t *testing.T) {
	maxSelected := 1
	field := FieldDefinition{
		PhysicalName: "status", DataType: DataTypeSelect, StorageType: StorageSelect,
		Nullable: true,
		Constraints: []FieldConstraint{{
			Kind: ConstraintEnum, MaxSelected: &maxSelected,
			Options: []SelectOption{
				{Value: json.Number("1"), DisplayName: "Number one"},
				{Value: "1", DisplayName: "String one"},
				{Value: true, DisplayName: "Enabled"},
			},
		}},
	}
	values, err := EnumStorageValues(field)
	if err != nil {
		t.Fatal(err)
	}
	if values[1] != "1" {
		t.Fatalf("string storage = %q, want unchanged", values[1])
	}
	if values[0] == values[1] || !strings.HasPrefix(values[0], typedEnumStoragePrefix) ||
		!strings.HasPrefix(values[2], typedEnumStoragePrefix) {
		t.Fatalf("typed storage values collapsed: %#v", values)
	}
	compiled, err := CompileField(field, nil)
	if err != nil {
		t.Fatal(err)
	}
	selectField, ok := compiled.(*core.SelectField)
	if !ok || !reflect.DeepEqual(selectField.Values, values) {
		t.Fatalf("compiled select = %#v, want values %#v", compiled, values)
	}
	for index, candidate := range []any{json.Number("1"), "1", true} {
		if err := ValidateFieldValue(field, candidate); err != nil {
			t.Fatalf("validate option %d: %v", index, err)
		}
		stored, err := EncodeSelectValueForStorage(field, candidate, values)
		if err != nil {
			t.Fatalf("encode option %d: %v", index, err)
		}
		decoded := DecodeSelectValueFromStorage(field, stored)
		if !ProductValuesEqual(decoded, candidate) {
			t.Fatalf("round trip %d = %#v (%T), want %#v (%T)",
				index, decoded, decoded, candidate, candidate)
		}
	}
}

func TestTypedEnumCodecSupportsLegacyTypedStorageWithoutChangingStringEnums(t *testing.T) {
	maxSelected := 1
	numberField := FieldDefinition{
		PhysicalName: "priority", DataType: DataTypeSelect,
		Constraints: []FieldConstraint{{
			Kind: ConstraintEnum, MaxSelected: &maxSelected,
			Options: []SelectOption{{
				Value: json.Number("1"), DisplayName: "One",
			}},
		}},
	}
	stored, err := EncodeSelectValueForStorage(
		numberField,
		json.Number("1"),
		[]string{"1"},
	)
	if err != nil || stored != "1" {
		t.Fatalf("legacy typed storage = %#v, %v", stored, err)
	}
	if decoded := DecodeSelectValueFromStorage(numberField, "1"); !ProductValuesEqual(decoded, json.Number("1")) {
		t.Fatalf("legacy typed decode = %#v", decoded)
	}

	reserved := typedEnumStoragePrefix + "n:MQ"
	stringField := FieldDefinition{
		PhysicalName: "code", DataType: DataTypeSelect,
		Constraints: []FieldConstraint{{
			Kind: ConstraintEnum, MaxSelected: &maxSelected,
			Options: []SelectOption{{Value: reserved, DisplayName: "Literal"}},
		}},
	}
	values, err := EnumStorageValues(stringField)
	if err != nil || len(values) != 1 || values[0] != reserved {
		t.Fatalf("reserved-looking string changed: %#v, %v", values, err)
	}
	if decoded := DecodeSelectValueFromStorage(stringField, reserved); decoded != reserved {
		t.Fatalf("reserved-looking string decoded as %#v", decoded)
	}
}

func TestTypedEnumCodecRejectsAmbiguousStorageCollision(t *testing.T) {
	maxSelected := 1
	numberOnly := FieldDefinition{
		DataType: DataTypeSelect,
		Constraints: []FieldConstraint{{
			Kind: ConstraintEnum, MaxSelected: &maxSelected,
			Options: []SelectOption{{
				Value: json.Number("1"), DisplayName: "Number",
			}},
		}},
	}
	numberStorage, err := EnumStorageValues(numberOnly)
	if err != nil {
		t.Fatal(err)
	}
	collision := numberOnly
	collision.Constraints = []FieldConstraint{{
		Kind: ConstraintEnum, MaxSelected: &maxSelected,
		Options: []SelectOption{
			{Value: json.Number("1"), DisplayName: "Number"},
			{Value: numberStorage[0], DisplayName: "Literal string"},
		},
	}}
	if _, err := EnumStorageValues(collision); err == nil {
		t.Fatal("ambiguous typed/string storage collision was accepted")
	}
}
