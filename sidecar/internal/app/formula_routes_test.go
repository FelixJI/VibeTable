package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/schemav2wire"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestDecodeFormulaRequestIsStrictAndPreservesNumbers(t *testing.T) {
	var input schemav2wire.FormulaPreviewRequest
	err := decodeFormulaRequest(strings.NewReader(
		`{"tableId":"orders","field":{},"row":{"count":9007199254740993},"changedFieldIds":[]}`,
	), &input)
	if err != nil {
		t.Fatalf("decode valid request: %v", err)
	}
	row, err := decodeFormulaRow(input.Row)
	if err != nil {
		t.Fatalf("decode row: %v", err)
	}
	if row["count"] != json.Number("9007199254740993") {
		t.Fatalf("number decoded as %#v (%T)", row["count"], row["count"])
	}

	for name, body := range map[string]string{
		"unknown":  `{"tableId":"orders","field":{},"row":{},"changedFieldIds":[],"raw":"x"}`,
		"trailing": `{"tableId":"orders","field":{},"row":{},"changedFieldIds":[]} {}`,
		"empty":    ``,
	} {
		t.Run(name, func(t *testing.T) {
			var request schemav2wire.FormulaPreviewRequest
			err := decodeFormulaRequest(strings.NewReader(body), &request)
			var formulaErr *formula.Error
			if !errors.As(err, &formulaErr) || formulaErr.Code != "formula.syntax" {
				t.Fatalf("error = %#v, want formula.syntax", err)
			}
		})
	}
}

func TestFormulaDraftErrorsPreserveFieldChangeDiagnostics(t *testing.T) {
	err := asFormulaError(&fieldchange.ProductError{
		Code: "schema.formula.relation_cardinality", Path: "draft.formula.source",
		Message: "many relations require an aggregate formula function",
		Details: map[string]any{"reference": "lines.amount"},
	})
	if err.Code != "schema.formula.relation_cardinality" || err.Path == nil ||
		*err.Path != "draft.formula.source" || err.Details["reference"] != "lines.amount" {
		t.Fatalf("mapped formula error = %#v", err)
	}
}

func TestValidateFormulaDefinitionRejectsIncompleteContract(t *testing.T) {
	err := formulaFieldError(v2.Validate(v2.FieldDefinition{}))
	if err == nil || err.Code != "formula.syntax" || err.Path == nil ||
		*err.Path != "field.contract" {
		t.Fatalf("error = %#v", err)
	}
	if err.Details["schemaCode"] != "field.contract.invalid" {
		t.Fatalf("details = %#v", err.Details)
	}
}
