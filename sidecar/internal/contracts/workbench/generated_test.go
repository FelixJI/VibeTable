package workbench

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type negativeFixture struct {
	Name    string         `json:"name"`
	Model   string         `json:"model"`
	Payload map[string]any `json:"payload"`
}

func TestSharedFixturesValidateAgainstDraft202012Schema(t *testing.T) {
	root := repositoryRoot(t)
	schemaPath := filepath.Join(root, "contracts", "workbench", "workbench.schema.json")
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()

	positiveRaw, err := os.ReadFile(filepath.Join(root, "contracts", "workbench", "fixtures", "positive.json"))
	if err != nil {
		t.Fatal(err)
	}
	var positives map[string]any
	if err := json.Unmarshal(positiveRaw, &positives); err != nil {
		t.Fatal(err)
	}
	for model, payload := range positives {
		schema, compileErr := compiler.Compile(schemaPath + "#/$defs/" + model)
		if compileErr != nil {
			t.Fatalf("compile %s: %v", model, compileErr)
		}
		if validateErr := schema.Validate(payload); validateErr != nil {
			t.Fatalf("positive %s: %v", model, validateErr)
		}
	}

	negativeRaw, err := os.ReadFile(filepath.Join(root, "contracts", "workbench", "fixtures", "negative.json"))
	if err != nil {
		t.Fatal(err)
	}
	var negatives []negativeFixture
	if err := json.Unmarshal(negativeRaw, &negatives); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range negatives {
		schema, compileErr := compiler.Compile(schemaPath + "#/$defs/" + fixture.Model)
		if compileErr != nil {
			t.Fatalf("compile %s: %v", fixture.Model, compileErr)
		}
		if validateErr := schema.Validate(fixture.Payload); validateErr == nil {
			t.Fatalf("negative fixture passed: %s", fixture.Name)
		}
	}
}

func TestGeneratedGoDTORejectsUnknownFieldsAtDecodeBoundary(t *testing.T) {
	raw := []byte(`{
		"query":"report","operator":"and","sources":["file"],
		"includeHistory":false,"cursor":null,"limit":50,"rawSql":"select 1"
	}`)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request SearchRequest
	if err := decoder.Decode(&request); err == nil {
		t.Fatal("generated DTO accepted an unknown wire field")
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
}
