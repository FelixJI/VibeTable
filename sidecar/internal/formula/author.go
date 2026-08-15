package formula

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

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
	tokens, err := scanDisplayTokens(displaySource)
	if err != nil {
		return "", err
	}
	locals := fieldsByDisplayName(definition.Snapshot.Fields)
	var builder strings.Builder
	cursor := 0
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		builder.WriteString(displaySource[cursor:token.start])
		local, resolveErr := uniqueDisplayField(locals, token.name, "local")
		if resolveErr != nil {
			return "", resolveErr
		}
		if index+1 < len(tokens) && displaySource[token.end:tokens[index+1].start] == "." {
			next := tokens[index+1]
			if local.LogicalType != v2.LogicalRelation || local.Relation == nil {
				return "", formulaError(
					"formula.dependency", "field path root is not a relation",
					map[string]any{"displayName": token.name},
				)
			}
			targetDefinition, exists := targets[local.Identity.PhysicalName]
			if !exists {
				return "", formulaError(
					"formula.dependency", "relation target schema is unavailable",
					map[string]any{"fieldId": local.Identity.FieldID},
				)
			}
			target, targetErr := uniqueDisplayField(
				fieldsByDisplayName(targetDefinition.Snapshot.Fields), next.name, "relation target",
			)
			if targetErr != nil {
				return "", targetErr
			}
			function := precedingFunction(displaySource, token.start)
			if canonical, aggregate := displayAggregateFunctions[function]; aggregate &&
				canonical != "relationCount" {
				builder.WriteString(local.Identity.PhysicalName)
				builder.WriteString(", ")
				builder.WriteString(fmt.Sprintf("%q", target.Identity.PhysicalName))
			} else {
				builder.WriteString(local.Identity.PhysicalName)
				builder.WriteByte('.')
				builder.WriteString(target.Identity.PhysicalName)
			}
			cursor = next.end
			index++
			continue
		}
		builder.WriteString(local.Identity.PhysicalName)
		cursor = token.end
	}
	builder.WriteString(displaySource[cursor:])
	canonical := replaceDisplayFunctionNames(builder.String())
	return strings.TrimSpace(canonical), nil
}

func scanDisplayTokens(source string) ([]displayToken, *Error) {
	tokens := []displayToken{}
	inString := false
	escaped := false
	for index := 0; index < len(source); index++ {
		switch source[index] {
		case '\\':
			if inString {
				escaped = !escaped
			}
		case '"':
			if !escaped {
				inString = !inString
			}
			escaped = false
		case '{':
			if inString {
				escaped = false
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
			index = end
		default:
			escaped = false
		}
	}
	if inString {
		return nil, formulaError("formula.syntax", "formula string is not closed", nil)
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

func precedingFunction(source string, tokenStart int) string {
	index := tokenStart - 1
	for index >= 0 && unicode.IsSpace(rune(source[index])) {
		index--
	}
	if index < 0 || source[index] != '(' {
		return ""
	}
	index--
	for index >= 0 && unicode.IsSpace(rune(source[index])) {
		index--
	}
	end := index + 1
	for index >= 0 && (unicode.IsLetter(rune(source[index])) || source[index] == '_') {
		index--
	}
	return strings.ToUpper(source[index+1 : end])
}

func replaceDisplayFunctionNames(source string) string {
	var builder strings.Builder
	inString := false
	escaped := false
	for index := 0; index < len(source); {
		character := source[index]
		if character == '\\' && inString {
			builder.WriteByte(character)
			escaped = !escaped
			index++
			continue
		}
		if character == '"' {
			if !escaped {
				inString = !inString
			}
			escaped = false
			builder.WriteByte(character)
			index++
			continue
		}
		escaped = false
		if !inString && (unicode.IsLetter(rune(character)) || character == '_') {
			end := index + 1
			for end < len(source) &&
				(unicode.IsLetter(rune(source[end])) || source[end] == '_') {
				end++
			}
			identifier := source[index:end]
			lookahead := end
			for lookahead < len(source) && unicode.IsSpace(rune(source[lookahead])) {
				lookahead++
			}
			if lookahead < len(source) && source[lookahead] == '(' {
				if canonical := displayAggregateFunctions[strings.ToUpper(identifier)]; canonical != "" {
					identifier = canonical
				}
			}
			builder.WriteString(identifier)
			index = end
			continue
		}
		builder.WriteByte(character)
		index++
	}
	return builder.String()
}
