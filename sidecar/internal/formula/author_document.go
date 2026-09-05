package formula

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/antlr4-go/antlr/v4"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/parser/gen"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// AuthorResult is an editing projection; only CanonicalSource is persisted.
// A missing reference returns both a result and formula.reference so the editor
// can show #REF! without forgetting a supplied stable identity.
type AuthorResult struct {
	Document        workbench.FormulaAuthorDocument
	CanonicalSource string
	SourceMap       AuthorSourceMap
}

type authorSegment struct {
	canonical, display SourceSpan
	unchanged          bool
}

type AuthorSourceMap struct {
	canonical string
	display   authorCoordinates
	segments  []authorSegment
}

// DisplayRange maps canonical UTF-8 byte offsets to display UTF-16 coordinates.
// Any overlap with a rewritten reference maps to its entire visible token.
func (m AuthorSourceMap) DisplayRange(span SourceSpan) (workbench.FormulaTextRange, bool) {
	if span.Start < 0 || span.End < span.Start || span.End > len(m.canonical) {
		return workbench.FormulaTextRange{}, false
	}
	start, end := -1, -1
	for _, segment := range m.segments {
		if span.Start == span.End {
			if span.Start < segment.canonical.Start || span.Start >= segment.canonical.End {
				continue
			}
		} else if span.End <= segment.canonical.Start || span.Start >= segment.canonical.End {
			continue
		}
		a, b := segment.display.Start, segment.display.End
		if segment.unchanged {
			a += max(span.Start, segment.canonical.Start) - segment.canonical.Start
			b = segment.display.Start + min(span.End, segment.canonical.End) - segment.canonical.Start
		}
		if start < 0 {
			start = a
		}
		end = b
	}
	if start < 0 && span.Start == len(m.canonical) && span.End == span.Start {
		if len(m.segments) > 0 {
			start = m.segments[len(m.segments)-1].display.End
		} else {
			start = 0
		}
		end = start
	}
	a, okA := m.display.positions[start]
	b, okB := m.display.positions[end]
	return workbench.FormulaTextRange{Start: a, End: b}, okA && okB
}

// CELRange accepts CEL's one-based line and zero-based Unicode scalar column.
func (m AuthorSourceMap) CELRange(line, column int) (workbench.FormulaTextRange, bool) {
	span, ok := celLocationSpan(m.canonical, line, column)
	if !ok {
		return workbench.FormulaTextRange{}, false
	}
	return m.DisplayRange(span)
}

func celLocationSpan(source string, line, column int) (SourceSpan, bool) {
	if line < 1 || column < 0 {
		return SourceSpan{}, false
	}
	currentLine, currentColumn := 1, 0
	for offset, r := range source {
		if currentLine == line && currentColumn == column {
			return SourceSpan{Start: offset, End: offset + utf8.RuneLen(r)}, true
		}
		if r == '\n' {
			currentLine++
			currentColumn = 0
		} else {
			currentColumn++
		}
	}
	return SourceSpan{Start: len(source), End: len(source)}, currentLine == line && currentColumn == column
}

type authorEdit struct {
	span               SourceSpan
	display, canonical string
	token              *workbench.FormulaAuthorToken
	missing            *Error
}

type authorLexeme struct {
	kind, start, end int
	text             string
}

// Reuse CEL's own lexer for string, bytes, raw/triple quote and comment boundaries.
// Display labels are not CEL, so lexical errors inside {...} are intentionally
// left to the author scanner; the existing compiler remains the syntax owner.
func authorLexemes(source string) []authorLexeme {
	offsets := make([]int, 0, utf8.RuneCountInString(source)+1)
	for offset := range source {
		offsets = append(offsets, offset)
	}
	offsets = append(offsets, len(source))
	lexer := gen.NewCELLexer(antlr.NewInputStream(source))
	lexer.RemoveErrorListeners()
	var result []authorLexeme
	for token := lexer.NextToken(); token.GetTokenType() != antlr.TokenEOF; token = lexer.NextToken() {
		result = append(result, authorLexeme{kind: token.GetTokenType(), start: offsets[token.GetStart()], end: offsets[token.GetStop()+1], text: token.GetText()})
	}
	return result
}

func renderAuthor(source string, revision int64, edits []authorEdit, restoring bool) (*AuthorResult, *Error) {
	sort.Slice(edits, func(i, j int) bool { return edits[i].span.Start < edits[j].span.Start })
	result := &AuthorResult{Document: workbench.FormulaAuthorDocument{DocumentRevision: revision, Tokens: []workbench.FormulaAuthorToken{}}}
	var display, canonical strings.Builder
	segments := []authorSegment{}
	type pendingToken struct {
		token workbench.FormulaAuthorToken
		span  SourceSpan
	}
	var pending []pendingToken
	var missing *Error
	var missingSpan SourceSpan
	appendText := func(d, c string, unchanged bool) {
		segment := authorSegment{display: SourceSpan{Start: display.Len(), End: display.Len() + len(d)}, canonical: SourceSpan{Start: canonical.Len(), End: canonical.Len() + len(c)}, unchanged: unchanged}
		display.WriteString(d)
		canonical.WriteString(c)
		segments = append(segments, segment)
	}
	cursor := 0
	for _, edit := range edits {
		if edit.span.Start < cursor {
			return nil, formulaError("formula.author.range", "author replacements overlap", nil)
		}
		if edit.span.Start > cursor {
			raw := source[cursor:edit.span.Start]
			appendText(raw, raw, true)
		}
		start := display.Len()
		c := edit.canonical
		if restoring {
			c = source[edit.span.Start:edit.span.End]
		}
		appendText(edit.display, c, edit.display == c)
		span := SourceSpan{Start: start, End: display.Len()}
		if edit.token != nil {
			pending = append(pending, pendingToken{token: *edit.token, span: span})
		}
		if edit.missing != nil && missing == nil {
			missing = edit.missing
			missingSpan = span
		}
		cursor = edit.span.End
	}
	if cursor < len(source) {
		raw := source[cursor:]
		appendText(raw, raw, true)
	}
	result.Document.DisplaySource = display.String()
	result.CanonicalSource = canonical.String()
	coordinates, err := indexAuthorSource(result.Document.DisplaySource)
	if err != nil {
		return nil, err
	}
	for _, item := range pending {
		item.token.Range = workbench.FormulaTextRange{Start: coordinates.positions[item.span.Start], End: coordinates.positions[item.span.End]}
		result.Document.Tokens = append(result.Document.Tokens, item.token)
	}
	result.SourceMap = AuthorSourceMap{canonical: result.CanonicalSource, display: coordinates, segments: segments}
	if missing != nil {
		missing.Details["range"] = workbench.FormulaTextRange{Start: coordinates.positions[missingSpan.Start], End: coordinates.positions[missingSpan.End]}
	}
	return result, missing
}

func authorReferenceError(details map[string]any) *Error {
	return formulaError("formula.reference", "#REF!", details)
}

func authorFieldByID(fields []v2.FieldDefinition, id string) (v2.FieldDefinition, bool) {
	for _, field := range fields {
		if field.Identity.FieldID == id {
			return field, true
		}
	}
	return v2.FieldDefinition{}, false
}

func authorToken(field v2.FieldDefinition, relation *v2.FieldDefinition) workbench.FormulaAuthorToken {
	token := workbench.FormulaAuthorToken{Kind: "field", FieldId: field.Identity.FieldID}
	if relation != nil {
		token.Kind = "relationTarget"
		root, target := relation.Identity.FieldID, field.Identity.FieldID
		token.RelationFieldId = &root
		token.TargetFieldId = &target
	} else if field.LogicalType == v2.LogicalRelation {
		token.Kind = "relation"
		root := field.Identity.FieldID
		token.RelationFieldId = &root
	}
	return token
}

func authorLabel(field v2.FieldDefinition) string { return "{" + field.DisplayName + "}" }

func authorTarget(definition V2Table, targets map[string]V2Table, relation v2.FieldDefinition) (V2Table, *Error) {
	if relation.LogicalType != v2.LogicalRelation || relation.Relation == nil {
		return V2Table{}, formulaError("formula.author.token", "field path root is not a relation", nil)
	}
	target, ok := targets[relation.Identity.PhysicalName]
	if !ok || target.TableID != relation.Relation.TargetTableID {
		return V2Table{}, authorReferenceError(map[string]any{"fieldId": relation.Identity.FieldID, "tableId": relation.Relation.TargetTableID})
	}
	return target, nil
}

// AuthorV2Document resolves supplied tokens by stable ID and unbound pasted
// references by unique display name. Labels never override a supplied identity.
func AuthorV2Document(definition V2Table, targets map[string]V2Table, document workbench.FormulaAuthorDocument) (*AuthorResult, *Error) {
	if document.DocumentRevision <= 0 {
		return nil, formulaError("formula.author.revision", "documentRevision must be positive", nil)
	}
	if len(document.DisplaySource) > DefaultSourceLimit || len(document.Tokens) > DefaultSourceLimit {
		return nil, formulaError("formula.resource_limit", "author document exceeds the source limit", map[string]any{"limit": DefaultSourceLimit})
	}
	coordinates, err := indexAuthorSource(document.DisplaySource)
	if err != nil {
		return nil, err
	}
	bindings := map[SourceSpan]workbench.FormulaAuthorToken{}
	var previous SourceSpan
	ordered := append([]workbench.FormulaAuthorToken(nil), document.Tokens...)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i].Range.Start, ordered[j].Range.Start
		return a.Line < b.Line || a.Line == b.Line && a.Character < b.Character
	})
	for i, token := range ordered {
		span, rangeErr := coordinates.byteRange(token.Range)
		if rangeErr != nil {
			return nil, rangeErr
		}
		if i > 0 && span.Start < previous.End {
			return nil, formulaError("formula.author.range", "author token ranges overlap", nil)
		}
		previous = span
		if token.FieldId == "" || (token.Kind != "field" && token.Kind != "relation" && token.Kind != "relationTarget") {
			return nil, formulaError("formula.author.token", "author token identity or kind is invalid", nil)
		}
		if token.Kind == "relationTarget" {
			if token.RelationFieldId == nil || *token.RelationFieldId == "" || token.TargetFieldId == nil || *token.TargetFieldId != token.FieldId {
				return nil, formulaError("formula.author.token", "relation target identity combination is invalid", nil)
			}
		} else if token.Kind == "relation" {
			if token.RelationFieldId == nil || *token.RelationFieldId != token.FieldId || token.TargetFieldId != nil {
				return nil, formulaError("formula.author.token", "relation token must repeat its root identity", nil)
			}
		} else if token.RelationFieldId != nil || token.TargetFieldId != nil {
			return nil, formulaError("formula.author.token", "local token has relation target identity", nil)
		}
		bindings[span] = token
	}
	// Stable tokens are lexical atoms, including punctuation in their stale
	// labels. Equal-byte placeholders preserve offsets while CEL's lexer checks
	// that each atom is outside literals/comments. Only unbound text is scanned
	// for display names; labels never get another chance to choose an identity.
	masked := []byte(document.DisplaySource)
	for span, binding := range bindings {
		label := document.DisplaySource[span.Start:span.End]
		if label != "#REF!" && (!strings.HasPrefix(label, "{") || !strings.HasSuffix(label, "}") ||
			len(label) <= 2 || binding.Kind == "relationTarget" && !strings.Contains(label, "}.{")) {
			return nil, formulaError("formula.author.range", "token must cover a complete reference label", nil)
		}
		for offset := span.Start; offset < span.End; offset++ {
			masked[offset] = ' '
		}
		masked[span.Start] = '0'
	}
	maskedSource := string(masked)
	lexemes := authorLexemes(maskedSource)
	atomStarts := make(map[int]bool, len(bindings))
	for _, lexeme := range lexemes {
		if lexeme.kind == gen.CELLexerNUM_INT && lexeme.text == "0" {
			atomStarts[lexeme.start] = true
		}
	}
	scanned, err := scanDisplayTokens(maskedSource)
	if err != nil {
		return nil, err
	}
	for span := range bindings {
		if !atomStarts[span.Start] {
			return nil, formulaError("formula.author.range", "token must be outside literals and comments", nil)
		}
		scanned = append(scanned, displayToken{start: span.Start, end: span.End})
	}
	sort.Slice(scanned, func(i, j int) bool { return scanned[i].start < scanned[j].start })
	var edits []authorEdit
	for i := 0; i < len(scanned); i++ {
		first := scanned[i]
		span := SourceSpan{Start: first.start, End: first.end}
		targetName := ""
		if i+1 < len(scanned) && document.DisplaySource[first.end:scanned[i+1].start] == "." {
			_, firstBound := bindings[span]
			_, nextBound := bindings[SourceSpan{Start: scanned[i+1].start, End: scanned[i+1].end}]
			if firstBound || nextBound {
				return nil, formulaError("formula.author.range", "relation target token must cover the entire field path", nil)
			}
			i++
			span.End = scanned[i].end
			targetName = scanned[i].name
		}
		binding, bound := bindings[span]
		var field v2.FieldDefinition
		var root *v2.FieldDefinition
		var resolveErr *Error
		if bound {
			delete(bindings, span)
			id := binding.FieldId
			if binding.Kind == "relationTarget" {
				id = *binding.RelationFieldId
			}
			local, ok := authorFieldByID(definition.Fields, id)
			if !ok {
				resolveErr = authorReferenceError(map[string]any{"fieldId": id})
			} else if binding.Kind == "relationTarget" {
				root = &local
				target, targetErr := authorTarget(definition, targets, local)
				resolveErr = targetErr
				if targetErr == nil {
					field, ok = authorFieldByID(target.Fields, binding.FieldId)
					if !ok {
						resolveErr = authorReferenceError(map[string]any{"fieldId": binding.FieldId, "relationFieldId": id})
					}
				}
			} else {
				field = local
				if (local.LogicalType == v2.LogicalRelation) != (binding.Kind == "relation") {
					return nil, formulaError("formula.author.token", "token kind differs from its stable field", nil)
				}
			}
		} else {
			local, localErr := uniqueDisplayField(fieldsByDisplayName(definition.Fields), first.name, "local")
			resolveErr = localErr
			if localErr == nil {
				field = local
				if targetName != "" {
					root = &local
					target, targetErr := authorTarget(definition, targets, local)
					resolveErr = targetErr
					if targetErr == nil {
						field, resolveErr = uniqueDisplayField(fieldsByDisplayName(target.Fields), targetName, "relation target")
					}
				}
			}
		}
		if resolveErr != nil {
			if resolveErr.Code != "formula.reference" {
				resolveErr.Details["range"] = workbench.FormulaTextRange{Start: coordinates.positions[span.Start], End: coordinates.positions[span.End]}
				return nil, resolveErr
			}
			var token *workbench.FormulaAuthorToken
			if bound {
				token = &binding
			}
			edits = append(edits, authorEdit{span: span, display: "#REF!", canonical: "#REF!", token: token, missing: resolveErr})
			continue
		}
		token := authorToken(field, root)
		display, canonical := authorLabel(field), field.Identity.PhysicalName
		if root != nil {
			display = authorLabel(*root) + "." + display
			canonical = root.Identity.PhysicalName + "." + canonical
			function := precedingFunction(document.DisplaySource, span.Start)
			if name := displayAggregateFunctions[function]; name != "" && name != "relationCount" {
				canonical = root.Identity.PhysicalName + ", " + strconv.Quote(field.Identity.PhysicalName)
			}
		}
		edits = append(edits, authorEdit{span: span, display: display, canonical: canonical, token: &token})
	}
	if len(bindings) > 0 {
		return nil, formulaError("formula.author.range", "token must cover one complete reference outside literals", nil)
	}
	for _, lexeme := range lexemes {
		if lexeme.kind != gen.CELLexerIDENTIFIER {
			continue
		}
		inside := false
		for _, edit := range edits {
			if lexeme.start >= edit.span.Start && lexeme.start < edit.span.End {
				inside = true
				break
			}
		}
		if inside {
			continue
		}
		if canonical := displayAggregateFunctions[strings.ToUpper(lexeme.text)]; canonical != "" && strings.HasPrefix(strings.TrimSpace(document.DisplaySource[lexeme.end:]), "(") {
			edits = append(edits, authorEdit{span: SourceSpan{Start: lexeme.start, End: lexeme.end}, display: lexeme.text, canonical: canonical})
		}
	}
	return renderAuthor(document.DisplaySource, document.DocumentRevision, edits, false)
}

// RestoreV2AuthorDocument projects persisted CEL without rewriting its bytes.
func RestoreV2AuthorDocument(definition V2Table, targets map[string]V2Table, canonicalSource string, documentRevision int64) (*AuthorResult, *Error) {
	if documentRevision <= 0 {
		return nil, formulaError("formula.author.revision", "documentRevision must be positive", nil)
	}
	if len(canonicalSource) > DefaultSourceLimit {
		return nil, formulaError("formula.resource_limit", "canonical source exceeds the source limit", map[string]any{"limit": DefaultSourceLimit})
	}
	if _, err := indexAuthorSource(canonicalSource); err != nil {
		return nil, err
	}
	locals := map[string]v2.FieldDefinition{}
	for _, field := range definition.Fields {
		locals[field.Identity.PhysicalName] = field
	}
	var tokens []authorLexeme
	for _, token := range authorLexemes(canonicalSource) {
		if token.kind != gen.CELLexerWHITESPACE && token.kind != gen.CELLexerCOMMENT {
			tokens = append(tokens, token)
		}
	}
	var edits []authorEdit
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if token.kind != gen.CELLexerIDENTIFIER {
			continue
		}
		for display, canonical := range displayAggregateFunctions {
			if token.text == canonical && i+1 < len(tokens) && tokens[i+1].kind == gen.CELLexerLPAREN {
				edits = append(edits, authorEdit{span: SourceSpan{Start: token.start, End: token.end}, display: display})
				break
			}
		}
		if i+1 < len(tokens) && tokens[i+1].kind == gen.CELLexerLPAREN {
			continue
		}
		if i > 0 && tokens[i-1].kind == gen.CELLexerDOT {
			continue
		}
		field, exists := locals[token.text]
		if !exists {
			if strings.HasPrefix(token.text, "f_") {
				edits = append(edits, authorEdit{span: SourceSpan{Start: token.start, End: token.end}, display: "#REF!", missing: authorReferenceError(map[string]any{"physicalName": token.text})})
			}
			continue
		}
		span := SourceSpan{Start: token.start, End: token.end}
		display := authorLabel(field)
		bound := authorToken(field, nil)
		targetName := ""
		endIndex := i
		if i+2 < len(tokens) && tokens[i+1].kind == gen.CELLexerDOT && tokens[i+2].kind == gen.CELLexerIDENTIFIER &&
			(i+3 >= len(tokens) || tokens[i+3].kind != gen.CELLexerLPAREN) {
			targetName = tokens[i+2].text
			endIndex = i + 2
		} else if i >= 2 && tokens[i-1].kind == gen.CELLexerLPAREN && i+2 < len(tokens) && tokens[i+1].kind == gen.CELLexerCOMMA && tokens[i+2].kind == gen.CELLexerSTRING {
			for _, canonical := range displayAggregateFunctions {
				if tokens[i-2].text == canonical && canonical != "relationCount" {
					env, _ := cel.NewEnv()
					parsed, issues := env.Parse(tokens[i+2].text)
					if issues == nil || issues.Err() == nil {
						targetName = parsed.Expr().GetConstExpr().GetStringValue()
						endIndex = i + 2
					}
					break
				}
			}
		}
		var missing *Error
		if targetName != "" {
			span.End = tokens[endIndex].end
			target, targetErr := authorTarget(definition, targets, field)
			missing = targetErr
			var targetField v2.FieldDefinition
			found := false
			if missing == nil {
				for _, candidate := range target.Fields {
					if candidate.Identity.PhysicalName == targetName {
						targetField = candidate
						found = true
						break
					}
				}
				if !found {
					missing = authorReferenceError(map[string]any{"physicalName": targetName, "relationFieldId": field.Identity.FieldID})
				}
			}
			if missing == nil {
				display = authorLabel(field) + "." + authorLabel(targetField)
				bound = authorToken(targetField, &field)
			} else {
				display = "#REF!"
			}
			i = endIndex
		}
		var binding *workbench.FormulaAuthorToken
		if missing == nil {
			binding = &bound
		}
		edits = append(edits, authorEdit{span: span, display: display, token: binding, missing: missing})
	}
	return renderAuthor(canonicalSource, documentRevision, edits, true)
}
