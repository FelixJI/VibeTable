package formula

import (
	"encoding/json"
	"fmt"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/parser/gen"
)

const ContractVersion = "2.0"

type SourceSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Error is the frozen v1 FormulaError wire shape.
type Error struct {
	ContractVersion string         `json:"contractVersion"`
	Code            string         `json:"code"`
	Path            *string        `json:"path"`
	Message         string         `json:"message"`
	Details         map[string]any `json:"details"`
	SourceSpan      *SourceSpan    `json:"sourceSpan"`
}

func (value *Error) Error() string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", value.Code, value.Message)
}

func (value Error) MarshalJSON() ([]byte, error) {
	type wire Error
	if value.ContractVersion == "" {
		value.ContractVersion = ContractVersion
	}
	if value.Details == nil {
		value.Details = map[string]any{}
	}
	return json.Marshal(wire(value))
}

func formulaError(code, message string, details map[string]any) *Error {
	if details == nil {
		details = map[string]any{}
	}
	return &Error{
		ContractVersion: ContractVersion,
		Code:            code,
		Message:         message,
		Details:         details,
	}
}

func formulaIssuesError(code, message, source string, issues *cel.Issues) *Error {
	result := formulaError(code, message, map[string]any{"reason": issues.Err().Error()})
	if diagnostics := issues.Errors(); len(diagnostics) > 0 {
		location := diagnostics[0].Location
		if span, ok := celLocationSpan(source, location.Line(), location.Column()); ok {
			// CEL points unknown function diagnostics at the opening parenthesis.
			// Reuse its lexer to highlight the function name in author coordinates.
			if code == "formula.dependency" {
				tokens := authorSyntaxLexemes(source)
				for index, token := range tokens {
					if token.start == span.Start && token.kind == gen.CELLexerLPAREN && index > 0 && tokens[index-1].kind == gen.CELLexerIDENTIFIER {
						span = SourceSpan{Start: tokens[index-1].start, End: tokens[index-1].end}
						break
					}
				}
			}
			result.SourceSpan = &span
		}
	}
	return result
}
