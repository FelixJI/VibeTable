package formula

import (
	"sort"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/parser/gen"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// The collection author layer binds collection references without a second
// formula parser. CEL's own lexer already isolates strings and comments, and
// every display reference is masked to one identifier atom, so a balanced
// delimiter walk over the syntax lexemes is enough to find TABLE({表})
// selections and the CurrentValue.{字段} scope of each
// FILTER/SUMIF/COUNTIF/PROJECT call. Nested collection language limits stay
// owned by the compiler.

// authorAtomPlaceholder replaces every masked display reference. It must be
// a plain identifier: a numeric placeholder after "CurrentValue." would lex
// as one float literal ".0" and swallow both the dot and the atom start.
const authorAtomPlaceholder = "x"

type authorItem struct {
	span       SourceSpan
	name       string
	targetName string
	binding    *workbench.FormulaAuthorToken
	missing    bool

	// Context assigned by classifyAuthorItems before resolution.
	context      string // "field", "table" or "sourceField"
	contextTable *V2Table
	contextError *Error
}

type authorLexemeRange struct {
	start, end int
}

// authorCallFrame is one identifier call with its top-level argument ranges
// in lexeme indices. Delimiters inside strings never produce lexemes, and
// commas inside maps or lists do not split call arguments.
type authorCallFrame struct {
	function string
	open     int
	close    int
	args     []authorLexemeRange
	argStart int
}

type authorStructure struct {
	lexemes []authorLexeme
	frames  []*authorCallFrame
	parent  []int
	frameAt []int
	callAt  map[int]int
	lexAt   map[int]int
}

func analyzeAuthorStructure(lexemes []authorLexeme) *authorStructure {
	structure := &authorStructure{
		lexemes: lexemes,
		frameAt: make([]int, len(lexemes)),
		callAt:  map[int]int{},
		lexAt:   map[int]int{},
	}
	for index := range lexemes {
		structure.frameAt[index] = -1
		if _, exists := structure.lexAt[lexemes[index].start]; !exists {
			structure.lexAt[lexemes[index].start] = index
		}
	}
	type authorDelimiter struct {
		kind  int
		frame int
	}
	var stack []authorDelimiter
	for index := range lexemes {
		innermost := -1
		for i := len(stack) - 1; i >= 0; i-- {
			if stack[i].kind == gen.CELLexerLPAREN {
				innermost = stack[i].frame
				break
			}
		}
		structure.frameAt[index] = innermost
		switch lexemes[index].kind {
		case gen.CELLexerLPAREN:
			frame := &authorCallFrame{open: index, close: -1, argStart: index + 1}
			if index > 0 && lexemes[index-1].kind == gen.CELLexerIDENTIFIER &&
				(index < 2 || lexemes[index-2].kind != gen.CELLexerDOT) {
				frame.function = lexemes[index-1].text
			}
			structure.frames = append(structure.frames, frame)
			structure.parent = append(structure.parent, innermost)
			structure.callAt[index] = len(structure.frames) - 1
			stack = append(stack, authorDelimiter{kind: gen.CELLexerLPAREN, frame: len(structure.frames) - 1})
		case gen.CELLexerLBRACE, gen.CELLexerLBRACKET:
			stack = append(stack, authorDelimiter{kind: lexemes[index].kind, frame: -1})
		case gen.CELLexerCOMMA:
			if len(stack) > 0 && stack[len(stack)-1].kind == gen.CELLexerLPAREN && innermost >= 0 {
				frame := structure.frames[innermost]
				frame.args = append(frame.args, authorLexemeRange{start: frame.argStart, end: index})
				frame.argStart = index + 1
			}
		case gen.CELLexerRBRACE:
			// Closing delimiters pair with their opening kind; the lexer's open
			// and close constants differ, so compare against the opening kind.
			if len(stack) > 0 && stack[len(stack)-1].kind == gen.CELLexerLBRACE {
				stack = stack[:len(stack)-1]
			}
		case gen.CELLexerRPRACKET:
			if len(stack) > 0 && stack[len(stack)-1].kind == gen.CELLexerLBRACKET {
				stack = stack[:len(stack)-1]
			}
		case gen.CELLexerRPAREN:
			if len(stack) > 0 && stack[len(stack)-1].kind == gen.CELLexerLPAREN && innermost >= 0 {
				frame := structure.frames[innermost]
				if index > frame.argStart || len(frame.args) > 0 {
					frame.args = append(frame.args, authorLexemeRange{start: frame.argStart, end: index})
				}
				frame.close = index
				stack = stack[:len(stack)-1]
			}
		}
	}
	return structure
}

func (structure *authorStructure) argumentOf(frame, index int) (int, bool) {
	if frame < 0 || frame >= len(structure.frames) {
		return -1, false
	}
	for argument, argumentRange := range structure.frames[frame].args {
		if index >= argumentRange.start && index < argumentRange.end {
			return argument, true
		}
	}
	return -1, false
}

func (structure *authorStructure) isAfterCurrentValue(index int) bool {
	return index >= 2 && structure.lexemes[index-1].kind == gen.CELLexerDOT &&
		structure.lexemes[index-2].kind == gen.CELLexerIDENTIFIER &&
		structure.lexemes[index-2].text == "CurrentValue"
}

// authorScopeArguments lists the predicate and projection positions whose
// CurrentValue fields belong to the call's own range.
var authorScopeArguments = map[string][]int{
	"FILTER":  {1},
	"SUMIF":   {1, 2},
	"COUNTIF": {1},
	"PROJECT": {1},
}

// authorRangeFunction lists calls that keep record semantics when nested as
// a range; everything else (PROJECT, aggregates) yields scalars.
func authorRangeFunction(name string) bool {
	switch name {
	case "TABLE", "FILTER":
		return true
	}
	return false
}

func authorContainsInt(values []int, value int) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// sourceFieldScope returns the range argument of the collection call whose
// CurrentValue the lexeme at index selects a field of. Range positions of a
// collection call keep looking outward, so a nested FILTER range still binds
// its inner predicate to the inner range.
func (structure *authorStructure) sourceFieldScope(index int) (authorLexemeRange, bool) {
	if !structure.isAfterCurrentValue(index) {
		return authorLexemeRange{}, false
	}
	for frame := structure.frameAt[index]; frame >= 0; frame = structure.parent[frame] {
		call := structure.frames[frame]
		allowed := authorScopeArguments[call.function]
		if len(allowed) == 0 {
			continue
		}
		argument, inside := structure.argumentOf(frame, index)
		if !inside || !authorContainsInt(allowed, argument) {
			continue
		}
		if len(call.args) == 0 {
			return authorLexemeRange{}, false
		}
		return call.args[0], true
	}
	return authorLexemeRange{}, false
}

// tableArgument reports whether the lexeme at index is the whole single
// argument of a TABLE(...) selection.
func (structure *authorStructure) tableArgument(index int) bool {
	frame := structure.frameAt[index]
	if frame < 0 {
		return false
	}
	call := structure.frames[frame]
	if call.function != "TABLE" || len(call.args) != 1 {
		return false
	}
	return call.args[0].start == index && call.args[0].end == index+1
}

// authorStructureSource masks every display reference to one numeric atom so
// the delimiter walk sees references, including punctuation in their labels,
// as indivisible arguments.
func authorStructureSource(source string, items []authorItem) string {
	masked := []byte(source)
	for _, item := range items {
		if item.span.Start < 0 || item.span.End > len(masked) || item.span.Start >= item.span.End {
			continue
		}
		for offset := item.span.Start + 1; offset < item.span.End; offset++ {
			masked[offset] = ' '
		}
		masked[item.span.Start] = authorAtomPlaceholder[0]
	}
	return string(masked)
}

// authorItems merges scanned labels and stable bindings into ordered display
// references. Consecutive unbound labels separated by "." form one relation
// path, exactly as the previous inline loop did.
func authorItems(
	source string,
	scanned []displayToken,
	bindings map[SourceSpan]workbench.FormulaAuthorToken,
) ([]authorItem, map[int]int, *Error) {
	items := []authorItem{}
	byStart := map[int]int{}
	for i := 0; i < len(scanned); i++ {
		first := scanned[i]
		item := authorItem{span: SourceSpan{Start: first.start, End: first.end}, name: first.name}
		item.missing = source[item.span.Start:item.span.End] == "#REF!"
		if i+1 < len(scanned) && source[item.span.End:scanned[i+1].start] == "." {
			next := SourceSpan{Start: scanned[i+1].start, End: scanned[i+1].end}
			_, firstBound := bindings[item.span]
			_, nextBound := bindings[next]
			if firstBound || nextBound {
				return nil, nil, formulaError("formula.author.range", "relation target token must cover the entire field path", nil)
			}
			i++
			item.span.End = scanned[i].end
			item.targetName = scanned[i].name
			item.missing = item.missing || source[scanned[i].start:scanned[i].end] == "#REF!"
		}
		if binding, bound := bindings[item.span]; bound {
			delete(bindings, item.span)
			item.binding = &binding
		}
		byStart[item.span.Start] = len(items)
		items = append(items, item)
	}
	if len(bindings) > 0 {
		return nil, nil, formulaError("formula.author.range", "token must cover one complete reference outside literals", nil)
	}
	return items, byStart, nil
}

// classifyAuthorItems assigns each display reference its collection context.
// Only whole masked atoms participate; literals and comments never get here.
func classifyAuthorItems(
	structure *authorStructure,
	items []authorItem,
	byStart map[int]int,
	definition V2Table,
	targets map[string]V2Table,
) *Error {
	for index := range items {
		item := &items[index]
		lexemeIndex, ok := structure.lexAt[item.span.Start]
		if !ok {
			return formulaError("formula.author.range", "token must be outside literals and comments", nil)
		}
		if structure.isAfterCurrentValue(lexemeIndex) {
			item.context = "sourceField"
			scope, scoped := structure.sourceFieldScope(lexemeIndex)
			if !scoped {
				return formulaError("formula.dependency", "CurrentValue fields are only available inside collection predicates and projections", nil)
			}
			table, err := resolveAuthorRange(structure, scope.start, scope.end, items, byStart, definition, targets)
			if err != nil {
				if err.Code != "formula.reference" {
					return err
				}
				item.contextError = err
			} else {
				item.contextTable = &table
			}
			continue
		}
		if structure.tableArgument(lexemeIndex) {
			item.context = "table"
			continue
		}
		item.context = "field"
	}
	return nil
}

// resolveAuthorRange maps a display collection range to its source table.
// Ranges are static: a TABLE({...}) selection, a local relation label, or a
// FILTER over another range. Scalar ranges never resolve to a table.
func resolveAuthorRange(
	structure *authorStructure,
	start, end int,
	items []authorItem,
	byStart map[int]int,
	definition V2Table,
	targets map[string]V2Table,
) (V2Table, *Error) {
	invalid := formulaError("formula.type", "collection range must be a selected table or relation", nil)
	if start < 0 || end <= start || end > len(structure.lexemes) {
		return V2Table{}, invalid
	}
	if end == start+1 {
		lexeme := structure.lexemes[start]
		index, ok := byStart[lexeme.start]
		if lexeme.kind != gen.CELLexerIDENTIFIER || lexeme.text != authorAtomPlaceholder || !ok {
			return V2Table{}, invalid
		}
		item := items[index]
		// A TABLE argument always resolves through the table catalog, never
		// through the local field list.
		if structure.tableArgument(start) {
			if item.binding != nil {
				if item.binding.Kind != "table" {
					return V2Table{}, invalid
				}
				tableID := *item.binding.TableId
				table, exists := authorTableByID(definition, tableID)
				if !exists || table.DisplayName == "" {
					return V2Table{}, authorReferenceError(map[string]any{"tableId": tableID})
				}
				return table, nil
			}
			if item.missing {
				return V2Table{}, authorReferenceError(nil)
			}
			if item.targetName != "" {
				return V2Table{}, invalid
			}
			return uniqueDisplayTable(authorTablesByDisplayName(definition), item.name)
		}
		if item.binding != nil {
			switch item.binding.Kind {
			case "table":
				tableID := *item.binding.TableId
				table, exists := authorTableByID(definition, tableID)
				if !exists || table.DisplayName == "" {
					return V2Table{}, authorReferenceError(map[string]any{"tableId": tableID})
				}
				return table, nil
			case "relation", "relationTarget":
				id := *item.binding.FieldId
				if item.binding.Kind == "relationTarget" {
					id = *item.binding.RelationFieldId
				}
				relation, exists := authorFieldByID(definition.Fields, id)
				if !exists {
					return V2Table{}, authorReferenceError(map[string]any{"fieldId": id})
				}
				return authorTarget(definition, targets, relation)
			}
			return V2Table{}, invalid
		}
		if item.targetName != "" {
			return V2Table{}, invalid
		}
		if item.missing {
			return V2Table{}, authorReferenceError(nil)
		}
		relation, err := uniqueDisplayField(fieldsByDisplayName(definition.Fields), item.name, "local")
		if err != nil {
			return V2Table{}, err
		}
		if relation.LogicalType != v2.LogicalRelation {
			return V2Table{}, invalid
		}
		return authorTarget(definition, targets, relation)
	}
	if structure.lexemes[start].kind != gen.CELLexerIDENTIFIER {
		return V2Table{}, invalid
	}
	frame, ok := structure.callAt[start+1]
	if !ok || !authorRangeFunction(structure.frames[frame].function) || structure.frames[frame].close != end-1 {
		return V2Table{}, invalid
	}
	args := structure.frames[frame].args
	if len(args) == 0 {
		return V2Table{}, formulaError("formula.type", "collection range is invalid", nil)
	}
	return resolveAuthorRange(structure, args[0].start, args[0].end, items, byStart, definition, targets)
}

// resolveCanonicalRange maps a persisted canonical range expression to its
// source table: TABLE("id") calls, relation identifiers, or a FILTER over
// another range. It reads no data and adds no fallback resolver.
func resolveCanonicalRange(
	structure *authorStructure,
	start, end int,
	locals map[string]v2.FieldDefinition,
	definition V2Table,
	targets map[string]V2Table,
) (V2Table, *Error) {
	invalid := formulaError("formula.type", "collection range must be a table or relation", nil)
	if start < 0 || end <= start || end > len(structure.lexemes) {
		return V2Table{}, invalid
	}
	if end == start+1 {
		lexeme := structure.lexemes[start]
		if lexeme.kind != gen.CELLexerIDENTIFIER {
			return V2Table{}, invalid
		}
		field, ok := locals[lexeme.text]
		if !ok {
			return V2Table{}, formulaError("formula.dependency", "collection range is not an available field", nil)
		}
		if field.LogicalType != v2.LogicalRelation {
			return V2Table{}, invalid
		}
		return authorTarget(definition, targets, field)
	}
	if structure.lexemes[start].kind != gen.CELLexerIDENTIFIER {
		return V2Table{}, invalid
	}
	frame, ok := structure.callAt[start+1]
	if !ok || !authorRangeFunction(structure.frames[frame].function) || structure.frames[frame].close != end-1 {
		return V2Table{}, invalid
	}
	call := structure.frames[frame]
	if call.function == "TABLE" {
		if len(call.args) != 1 || call.args[0].end != call.args[0].start+1 ||
			structure.lexemes[call.args[0].start].kind != gen.CELLexerSTRING {
			return V2Table{}, formulaError("formula.type", "TABLE requires a static table identity", nil)
		}
		tableID, parsed := authorStringLexeme(structure.lexemes[call.args[0].start].text)
		if !parsed || tableID == "" {
			return V2Table{}, formulaError("formula.dependency", "TABLE requires an active static table identity", nil)
		}
		table, exists := authorTableByID(definition, tableID)
		if !exists {
			return V2Table{}, authorReferenceError(map[string]any{"tableId": tableID})
		}
		return table, nil
	}
	if len(call.args) == 0 {
		return V2Table{}, formulaError("formula.type", "collection range is invalid", nil)
	}
	return resolveCanonicalRange(structure, call.args[0].start, call.args[0].end, locals, definition, targets)
}

// authorTableByID resolves a stable table identity among the authoring table
// itself and the active tables supplied for authoring. It never falls back to
// a data source or a display-name guess.
func authorTableByID(definition V2Table, tableID string) (V2Table, bool) {
	if tableID == "" {
		return V2Table{}, false
	}
	if tableID == definition.TableID {
		return definition, true
	}
	table, ok := definition.AuthorTables[tableID]
	return table, ok
}

func authorTablesByDisplayName(definition V2Table) map[string][]V2Table {
	result := map[string][]V2Table{}
	add := func(table V2Table) {
		if table.DisplayName == "" {
			return
		}
		for _, existing := range result[table.DisplayName] {
			if existing.TableID == table.TableID {
				return
			}
		}
		result[table.DisplayName] = append(result[table.DisplayName], table)
	}
	add(definition)
	ids := make([]string, 0, len(definition.AuthorTables))
	for id := range definition.AuthorTables {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		add(definition.AuthorTables[id])
	}
	return result
}

func uniqueDisplayTable(tables map[string][]V2Table, displayName string) (V2Table, *Error) {
	matches := tables[displayName]
	if len(matches) == 1 {
		return matches[0], nil
	}
	details := map[string]any{"displayName": displayName, "scope": "table"}
	if len(matches) > 1 {
		tableIDs := make([]string, 0, len(matches))
		for _, table := range matches {
			tableIDs = append(tableIDs, table.TableID)
		}
		sort.Strings(tableIDs)
		details["tableIds"] = tableIDs
		return V2Table{}, formulaError("formula.dependency", "table display name is ambiguous; rename one table", details)
	}
	return V2Table{}, formulaError("formula.dependency", "table display name was not found", details)
}

// authorStringLexeme reads a CEL string literal through CEL's own parser.
func authorStringLexeme(text string) (string, bool) {
	env, _ := cel.NewEnv()
	parsed, issues := env.Parse(text)
	if issues != nil && issues.Err() != nil {
		return "", false
	}
	constant := parsed.Expr().GetConstExpr()
	if constant == nil {
		return "", false
	}
	return constant.GetStringValue(), true
}

// authorShorthandKind reports "relation" or "relationTarget" when the call at
// the identifier lexeme index has exactly one argument that is one whole
// resolved relation shorthand reference.
func authorShorthandKind(structure *authorStructure, identifier int, shorthands map[int]string) string {
	if identifier+1 >= len(structure.lexemes) || structure.lexemes[identifier+1].kind != gen.CELLexerLPAREN {
		return ""
	}
	frame, ok := structure.callAt[identifier+1]
	if !ok {
		return ""
	}
	args := structure.frames[frame].args
	if len(args) != 1 || args[0].end != args[0].start+1 {
		return ""
	}
	return shorthands[structure.lexemes[args[0].start].start]
}
