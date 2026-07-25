package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/formula"
)

func TestDecodeFormulaRequestIsStrictAndPreservesNumbers(t *testing.T) {
	var input formulaPreviewRequest
	err := decodeFormulaRequest(strings.NewReader(
		`{"definition":{},"row":{"count":9007199254740993},"changedFieldIds":[]}`,
	), &input)
	if err != nil {
		t.Fatalf("decode valid request: %v", err)
	}
	if input.Row["count"] != json.Number("9007199254740993") {
		t.Fatalf("number decoded as %#v (%T)", input.Row["count"], input.Row["count"])
	}

	for name, body := range map[string]string{
		"unknown":  `{"definition":{},"row":{},"changedFieldIds":[],"raw":"x"}`,
		"trailing": `{"definition":{},"row":{},"changedFieldIds":[]} {}`,
		"empty":    ``,
	} {
		t.Run(name, func(t *testing.T) {
			var request formulaPreviewRequest
			err := decodeFormulaRequest(strings.NewReader(body), &request)
			var formulaErr *formula.Error
			if !errors.As(err, &formulaErr) || formulaErr.Code != "formula.syntax" {
				t.Fatalf("error = %#v, want formula.syntax", err)
			}
		})
	}
}

func TestValidateFormulaDefinitionRejectsIncompleteContract(t *testing.T) {
	err := validateFormulaDefinition(formulaValidateRequest{}.Definition)
	if err == nil || err.Code != "formula.syntax" || err.Path == nil ||
		*err.Path != "definition.contractVersion" {
		t.Fatalf("error = %#v", err)
	}
	if err.Details["schemaCode"] != "schema.contract.unsupported_version" {
		t.Fatalf("details = %#v", err.Details)
	}
}
