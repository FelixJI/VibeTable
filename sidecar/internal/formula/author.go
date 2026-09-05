package formula

import (
	"sort"
	"strings"

	"github.com/google/cel-go/parser/gen"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

var displayAggregateFunctions = map[string]string{
	"SUM":     "relationSum",
	"AVERAGE": "relationAverage",
	"MIN":     "relationMin",
	"MAX":     "relationMax",
	"COUNT":   "relationCount",
	"COUNTA":  "relationCountValues",
}

type displayToken struct {
	start int
	end   int
	name  string
}

// CanonicalizeExecutionDisplaySource turns user-facing {display name} tokens
// into the permanent physical identifiers consumed and stored by CEL. Target
// schemas are keyed by the source relation field's physical name.
func CanonicalizeExecutionDisplaySource(
	definition schemaexecution.Table,
	targets map[string]schemaexecution.Table,
	displaySource string,
) (string, *Error) {
	v2Targets := make(map[string]V2Table, len(targets))
	for name, target := range targets {
		v2Targets[name] = V2Table{TableID: target.Snapshot.TableID, Fields: target.Snapshot.Fields}
	}
	result, err := AuthorV2Document(V2Table{TableID: definition.Snapshot.TableID, Fields: definition.Snapshot.Fields}, v2Targets, workbench.FormulaAuthorDocument{DisplaySource: displaySource, DocumentRevision: 1})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.CanonicalSource), nil
}

func scanDisplayTokens(source string) ([]displayToken, *Error) {
	tokens := []displayToken{}
	cursor := 0
	lexemes := authorSyntaxLexemes(source)
	maps := mapLiteralStarts(source, lexemes)
	for _, lexeme := range lexemes {
		index := lexeme.start
		if index < cursor {
			continue
		}
		switch lexeme.kind {
		case gen.CELLexerLBRACE:
			if maps[index] {
				continue
			}
			endOffset := strings.IndexByte(source[index+1:], '}')
			if endOffset < 0 {
				return nil, formulaError("formula.syntax", "field token is not closed", nil)
			}
			end := index + 1 + endOffset
			name := strings.TrimSpace(source[index+1 : end])
			if name == "" {
				return nil, formulaError("formula.syntax", "field token is empty", nil)
			}
			tokens = append(tokens, displayToken{start: index, end: end + 1, name: name})
			cursor = end + 1
		case gen.CELLexerIDENTIFIER:
			if lexeme.text == "REF" && index > 0 && source[index-1] == '#' && lexeme.end < len(source) && source[lexeme.end] == '!' {
				tokens = append(tokens, displayToken{start: index - 1, end: lexeme.end + 1, name: "#REF!"})
				cursor = lexeme.end + 1
			}
		}
	}
	return tokens, nil
}

func fieldsByDisplayName(fields []v2.FieldDefinition) map[string][]v2.FieldDefinition {
	result := make(map[string][]v2.FieldDefinition, len(fields))
	for _, field := range fields {
		result[field.DisplayName] = append(result[field.DisplayName], field)
	}
	return result
}

func uniqueDisplayField(
	fields map[string][]v2.FieldDefinition,
	displayName string,
	scope string,
) (v2.FieldDefinition, *Error) {
	matches := fields[displayName]
	if len(matches) == 1 {
		return matches[0], nil
	}
	details := map[string]any{"displayName": displayName, "scope": scope}
	if len(matches) > 1 {
		fieldIDs := make([]string, 0, len(matches))
		for _, field := range matches {
			fieldIDs = append(fieldIDs, field.Identity.FieldID)
		}
		sort.Strings(fieldIDs)
		details["fieldIds"] = fieldIDs
		return v2.FieldDefinition{}, formulaError(
			"formula.dependency", "field display name is ambiguous; rename one field", details,
		)
	}
	return v2.FieldDefinition{}, formulaError(
		"formula.dependency", "field display name was not found", details,
	)
}

// A colon at the brace's own delimiter depth, or an empty brace pair, belongs
// to CEL map syntax. Nested author references remain independent lexical atoms.
// Bound labels were already masked, so punctuation in their display names
// cannot change this distinction.
func mapLiteralStarts(source string, lexemes []authorLexeme) map[int]bool {
	result := map[int]bool{}
	var delimiters []authorLexeme
	for index, lexeme := range lexemes {
		switch lexeme.kind {
		case gen.CELLexerLBRACE, gen.CELLexerLBRACKET, gen.CELLexerLPAREN:
			delimiters = append(delimiters, lexeme)
			if lexeme.kind == gen.CELLexerLBRACE && index+1 < len(lexemes) && lexemes[index+1].kind == gen.CELLexerRBRACE && emptyMapBody(source[lexeme.end:lexemes[index+1].start]) {
				result[lexeme.start] = true
			}
		case gen.CELLexerCOLON:
			if len(delimiters) > 0 && delimiters[len(delimiters)-1].kind == gen.CELLexerLBRACE {
				result[delimiters[len(delimiters)-1].start] = true
			}
		case gen.CELLexerRBRACE, gen.CELLexerRPRACKET, gen.CELLexerRPAREN:
			if len(delimiters) > 0 {
				delimiters = delimiters[:len(delimiters)-1]
			}
		}
	}
	return result
}

func emptyMapBody(source string) bool {
	covered := 0
	for _, lexeme := range authorLexemes(source) {
		if lexeme.kind != gen.CELLexerWHITESPACE && lexeme.kind != gen.CELLexerCOMMENT {
			return false
		}
		covered += lexeme.end - lexeme.start
	}
	// CEL skips characters that are valid in display labels, such as Chinese.
	// Unlexed bytes must not turn a nonempty field label into an empty map.
	return covered == len(source)
}
