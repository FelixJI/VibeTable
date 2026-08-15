package schemav2wire

import (
	"encoding/json"
	"os"
	"testing"
)

func TestGeneratedFieldDefinitionStrictlyDecodesSharedFixture(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../../../contracts/schema-v2/fixtures/field-definition.json")
	if err != nil {
		t.Fatal(err)
	}
	var definition FieldDefinition
	if err := StrictDecode(raw, &definition); err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	value := object["value"].(map[string]any)
	value["localQualification"] = true
	invalid, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if err := StrictDecode(invalid, &definition); err == nil {
		t.Fatal("generated wire DTO accepted an unknown nested property")
	}
}
