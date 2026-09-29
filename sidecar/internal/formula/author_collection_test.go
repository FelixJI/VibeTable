package formula

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func collectionAuthorFixture() (V2Table, map[string]V2Table) {
	localContract := scalarField("contract_id", "f_contract", textType)
	localContract.DisplayName = "合同"
	localStart := scalarField("start_id", "f_start", dateTimeType)
	localStart.DisplayName = "开始日期"
	lines := relationField("lines_id", "f_lines", "line_items")
	lines.DisplayName = "明细"

	shipContract := scalarField("ship_contract_id", "f_ship_contract", textType)
	shipContract.DisplayName = "合同"
	shipAmount := scalarField("ship_amount_id", "f_ship_amount", numberType)
	shipAmount.DisplayName = "金额"
	shipTable := V2Table{TableID: "tbl_ship", DisplayName: "出货",
		Fields: []v2.FieldDefinition{shipContract, shipAmount}}

	returnContract := scalarField("return_contract_id", "f_return_contract", textType)
	returnContract.DisplayName = "合同"
	returnTable := V2Table{TableID: "tbl_return", DisplayName: "退货",
		Fields: []v2.FieldDefinition{returnContract}}

	amount := scalarField("amount_id", "f_amount", numberType)
	amount.DisplayName = "金额"

	definition := V2Table{
		TableID:     "tbl_orders",
		DisplayName: "订单",
		Fields:      []v2.FieldDefinition{localContract, localStart, lines},
		AuthorTables: map[string]V2Table{
			"tbl_ship": shipTable, "tbl_return": returnTable,
		},
	}
	return definition, map[string]V2Table{"f_lines": {
		TableID: "line_items", DisplayName: "明细表", Fields: []v2.FieldDefinition{amount},
	}}
}

func authorCollectionSnapshot(table V2Table) v2.SchemaSnapshot {
	return v2.SchemaSnapshot{
		Contract:       v2.Contract,
		TableID:        table.TableID,
		DisplayName:    table.DisplayName,
		SchemaRevision: "schema_1",
		Fields:         table.Fields,
	}
}

// compileCollectionAuthorCanonical proves an authored canonical source is
// accepted by the existing cel-v2 compiler; the author layer never invents
// language the compiler rejects.
func compileCollectionAuthorCanonical(t *testing.T, definition V2Table, extraSources map[string]v2.SchemaSnapshot, canonical string) {
	t.Helper()
	formula := formulaField("total_id", "f_total", numberType, canonical)
	formula.Formula.Language = "cel-v2"
	snapshot := authorCollectionSnapshot(definition)
	snapshot.Fields = append(append([]v2.FieldDefinition(nil), definition.Fields...), formula)
	sources := map[string]v2.SchemaSnapshot{}
	for _, table := range definition.AuthorTables {
		sources[table.TableID] = authorCollectionSnapshot(table)
	}
	for id, source := range extraSources {
		sources[id] = source
	}
	execution := schemaexecution.Table{
		Snapshot:       snapshot,
		FormulaSources: sources,
		FormulaRuntime: map[string]schemaexecution.FormulaRuntime{
			"total_id": {Version: 1, Status: "ready"},
		},
	}
	if _, err := NewCompiler(DefaultLimits()).CompileExecutionTable(execution); err != nil {
		t.Fatalf("compiler rejects canonical %q: %v", canonical, err)
	}
}

func TestAuthorCollectionTableRangeFilterProjectionRoundTrip(t *testing.T) {
	definition, targets := collectionAuthorFixture()
	display := "SUM(PROJECT(FILTER(TABLE({出货}), CurrentValue.{合同} == {合同}), CurrentValue.{金额}))"
	authored, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: display, DocumentRevision: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical := `SUM(PROJECT(FILTER(TABLE("tbl_ship"), CurrentValue.f_ship_contract == f_contract), CurrentValue.f_ship_amount))`
	if authored.CanonicalSource != canonical {
		t.Fatalf("canonical = %q, want %q", authored.CanonicalSource, canonical)
	}
	if len(authored.Document.Tokens) != 4 {
		t.Fatalf("tokens = %#v", authored.Document.Tokens)
	}
	tableToken := authored.Document.Tokens[0]
	if tableToken.Kind != "table" || tableToken.FieldId != nil || tableToken.TableId == nil ||
		*tableToken.TableId != "tbl_ship" || tableToken.RelationFieldId != nil || tableToken.TargetFieldId != nil {
		t.Fatalf("table token = %#v", tableToken)
	}
	sourceToken := authored.Document.Tokens[1]
	if sourceToken.Kind != "sourceField" || sourceToken.FieldId == nil || *sourceToken.FieldId != "ship_contract_id" ||
		sourceToken.TableId == nil || *sourceToken.TableId != "tbl_ship" || sourceToken.RelationFieldId != nil || sourceToken.TargetFieldId != nil {
		t.Fatalf("source field token = %#v", sourceToken)
	}
	coordinates, coordErr := indexAuthorSource(authored.Document.DisplaySource)
	if coordErr != nil {
		t.Fatal(coordErr)
	}
	for index, want := range []string{"{出货}", "{合同}", "{合同}", "{金额}"} {
		span, spanErr := coordinates.byteRange(authored.Document.Tokens[index].Range)
		if spanErr != nil || authored.Document.DisplaySource[span.Start:span.End] != want {
			t.Fatalf("token %d range = %q (%v), want %q", index,
				authored.Document.DisplaySource[span.Start:span.End], spanErr, want)
		}
	}
	restored, err := RestoreV2AuthorDocument(definition, targets, canonical, 7)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Document.DisplaySource != display || restored.CanonicalSource != canonical {
		t.Fatalf("restored = %#v", restored)
	}
	if !reflect.DeepEqual(restored.Document.Tokens, authored.Document.Tokens) {
		t.Fatalf("restored tokens = %#v, want %#v", restored.Document.Tokens, authored.Document.Tokens)
	}
	again, err := AuthorV2Document(definition, targets, restored.Document)
	if err != nil {
		t.Fatal(err)
	}
	if again.CanonicalSource != canonical || !reflect.DeepEqual(again.Document, restored.Document) {
		t.Fatalf("round trip = %#v", again)
	}
	compileCollectionAuthorCanonical(t, definition, nil, canonical)
}

func TestAuthorCollectionScopeSurvivesListAndMapLiterals(t *testing.T) {
	definition, targets := collectionAuthorFixture()
	// A list literal before the collection call must not leave an unclosed
	// bracket on the delimiter stack: the SUMIF predicate and projection
	// still bind their CurrentValue fields to the TABLE range.
	display := "double(COUNT(FILTER([1.0], CurrentValue > 0.0))) + SUMIF(TABLE({出货}), CurrentValue.{合同} == {合同}, CurrentValue.{金额})"
	authored, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: display, DocumentRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical := `double(COUNT(FILTER([1.0], CurrentValue > 0.0))) + SUMIF(TABLE("tbl_ship"), CurrentValue.f_ship_contract == f_contract, CurrentValue.f_ship_amount)`
	if authored.CanonicalSource != canonical {
		t.Fatalf("canonical = %q, want %q", authored.CanonicalSource, canonical)
	}
	if len(authored.Document.Tokens) != 4 {
		t.Fatalf("tokens = %#v", authored.Document.Tokens)
	}
	if authored.Document.Tokens[0].Kind != "table" || *authored.Document.Tokens[0].TableId != "tbl_ship" {
		t.Fatalf("table token = %#v", authored.Document.Tokens[0])
	}
	if local := authored.Document.Tokens[2]; local.Kind != "field" || local.TableId != nil {
		t.Fatalf("outer field token = %#v", local)
	}
	for _, index := range []int{1, 3} {
		token := authored.Document.Tokens[index]
		if token.Kind != "sourceField" || *token.TableId != "tbl_ship" {
			t.Fatalf("source field token = %#v", token)
		}
	}
	restored, err := RestoreV2AuthorDocument(definition, targets, canonical, 1)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Document.DisplaySource != display {
		t.Fatalf("restored display = %q", restored.Document.DisplaySource)
	}
	again, err := AuthorV2Document(definition, targets, restored.Document)
	if err != nil {
		t.Fatal(err)
	}
	if again.CanonicalSource != canonical || !reflect.DeepEqual(again.Document, restored.Document) {
		t.Fatalf("round trip = %#v", again)
	}
	compileCollectionAuthorCanonical(t, definition, nil, canonical)

	// Commas inside a map literal must not split call arguments either.
	mapDisplay := `size({"k": 1, "j": 2}) + COUNTIF(TABLE({出货}), CurrentValue.{合同} == {合同})`
	mapResult, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: mapDisplay, DocumentRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	mapCanonical := `size({"k": 1, "j": 2}) + COUNTIF(TABLE("tbl_ship"), CurrentValue.f_ship_contract == f_contract)`
	if mapResult.CanonicalSource != mapCanonical {
		t.Fatalf("map canonical = %q, want %q", mapResult.CanonicalSource, mapCanonical)
	}
	if len(mapResult.Document.Tokens) != 3 || mapResult.Document.Tokens[2].Kind != "field" || mapResult.Document.Tokens[1].Kind != "sourceField" || *mapResult.Document.Tokens[1].TableId != "tbl_ship" {
		t.Fatalf("map tokens = %#v", mapResult.Document.Tokens)
	}
}

func TestAuthorCollectionTwoTableBranchesIsolateCurrentValue(t *testing.T) {
	definition, targets := collectionAuthorFixture()
	display := "IF(true, COUNTIF(TABLE({出货}), CurrentValue.{合同} == {合同}), COUNTIF(TABLE({退货}), CurrentValue.{合同} == {合同}))"
	authored, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: display, DocumentRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical := `IF(true, COUNTIF(TABLE("tbl_ship"), CurrentValue.f_ship_contract == f_contract), COUNTIF(TABLE("tbl_return"), CurrentValue.f_return_contract == f_contract))`
	if authored.CanonicalSource != canonical {
		t.Fatalf("canonical = %q, want %q", authored.CanonicalSource, canonical)
	}
	tokens := authored.Document.Tokens
	if len(tokens) != 6 {
		t.Fatalf("tokens = %#v", tokens)
	}
	if tokens[1].Kind != "sourceField" || *tokens[1].TableId != "tbl_ship" || *tokens[1].FieldId != "ship_contract_id" {
		t.Fatalf("ship branch token = %#v", tokens[1])
	}
	if tokens[4].Kind != "sourceField" || *tokens[4].TableId != "tbl_return" || *tokens[4].FieldId != "return_contract_id" {
		t.Fatalf("return branch token = %#v", tokens[4])
	}
	// The bare {合同} stays the outer local field even with same-named source fields.
	for _, index := range []int{2, 5} {
		if tokens[index].Kind != "field" || *tokens[index].FieldId != "contract_id" {
			t.Fatalf("outer token %d = %#v", index, tokens[index])
		}
	}
	restored, err := RestoreV2AuthorDocument(definition, targets, canonical, 1)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Document.DisplaySource != display {
		t.Fatalf("restored display = %q", restored.Document.DisplaySource)
	}
	again, err := AuthorV2Document(definition, targets, restored.Document)
	if err != nil {
		t.Fatal(err)
	}
	if again.CanonicalSource != canonical || !reflect.DeepEqual(again.Document, restored.Document) {
		t.Fatalf("round trip = %#v", again)
	}
}

func TestAuthorCollectionRelationRangeProject(t *testing.T) {
	definition, targets := collectionAuthorFixture()
	display := "SUM(PROJECT(FILTER({明细}, CurrentValue.{金额} > 0.0), CurrentValue.{金额})) + double(COUNTIF({明细}, CurrentValue.{金额} > 0.0))"
	authored, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: display, DocumentRevision: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical := "SUM(PROJECT(FILTER(f_lines, CurrentValue.f_amount > 0.0), CurrentValue.f_amount)) + double(COUNTIF(f_lines, CurrentValue.f_amount > 0.0))"
	if authored.CanonicalSource != canonical {
		t.Fatalf("canonical = %q, want %q", authored.CanonicalSource, canonical)
	}
	for index, token := range authored.Document.Tokens {
		if index%3 == 0 {
			if token.Kind != "relation" || *token.FieldId != "lines_id" {
				t.Fatalf("relation token %d = %#v", index, token)
			}
			continue
		}
		if token.Kind != "sourceField" || *token.TableId != "line_items" || *token.FieldId != "amount_id" {
			t.Fatalf("source token %d = %#v", index, token)
		}
	}
	restored, err := RestoreV2AuthorDocument(definition, targets, canonical, 2)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Document.DisplaySource != display {
		t.Fatalf("restored display = %q", restored.Document.DisplaySource)
	}
	again, err := AuthorV2Document(definition, targets, restored.Document)
	if err != nil {
		t.Fatal(err)
	}
	if again.CanonicalSource != canonical || !reflect.DeepEqual(again.Document, restored.Document) {
		t.Fatalf("round trip = %#v", again)
	}
	compileCollectionAuthorCanonical(t, definition,
		map[string]v2.SchemaSnapshot{"line_items": authorCollectionSnapshot(targets["f_lines"])}, canonical)
}

func TestAuthorCollectionAggregateNamesKeepCollectionCalls(t *testing.T) {
	definition, targets := collectionAuthorFixture()
	cases := []struct{ display, canonical string }{
		{"SUM({明细}.{金额})", `relationSum(f_lines, "f_amount")`},
		{"COUNT({明细})", "relationCount(f_lines)"},
		{"COUNT(TABLE({出货}))", `COUNT(TABLE("tbl_ship"))`},
		{"SUM(PROJECT(TABLE({出货}), CurrentValue.{金额}))", `SUM(PROJECT(TABLE("tbl_ship"), CurrentValue.f_ship_amount))`},
		{"AVERAGE([1.0, 2.0])", "AVERAGE([1.0, 2.0])"},
		{"SUM([1.0, 2.0]) + COUNTA([\"\", 1])", `SUM([1.0, 2.0]) + COUNTA(["", 1])`},
		{"MIN(8, 3)", "MIN(8, 3)"},
		{"min(8, 3)", "min(8, 3)"},
		{"max(min(8, 3), 2)", "max(min(8, 3), 2)"},
	}
	for _, test := range cases {
		t.Run(test.display, func(t *testing.T) {
			result, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
				DisplaySource: test.display, DocumentRevision: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.CanonicalSource != test.canonical {
				t.Fatalf("canonical = %q, want %q", result.CanonicalSource, test.canonical)
			}
		})
	}
}

func TestAuthorCollectionStableTokensSurviveRenameAndDelete(t *testing.T) {
	definition, targets := collectionAuthorFixture()
	display := "SUM(PROJECT(FILTER(TABLE({出货}), CurrentValue.{合同} == {合同}), CurrentValue.{金额}))"
	authored, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: display, DocumentRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical := authored.CanonicalSource

	renamed, _ := collectionAuthorFixture()
	ship := renamed.AuthorTables["tbl_ship"]
	ship.DisplayName = "出货单"
	ship.Fields[0].DisplayName = "合同号"
	renamed.AuthorTables["tbl_ship"] = ship
	renamed.Fields[0].DisplayName = "本地合同"
	afterRename, err := AuthorV2Document(renamed, targets, authored.Document)
	if err != nil {
		t.Fatal(err)
	}
	if afterRename.CanonicalSource != canonical {
		t.Fatalf("rename changed canonical: %q", afterRename.CanonicalSource)
	}
	wantDisplay := "SUM(PROJECT(FILTER(TABLE({出货单}), CurrentValue.{合同号} == {本地合同}), CurrentValue.{金额}))"
	if afterRename.Document.DisplaySource != wantDisplay {
		t.Fatalf("renamed display = %q, want %q", afterRename.Document.DisplaySource, wantDisplay)
	}
	if *afterRename.Document.Tokens[0].TableId != "tbl_ship" || *afterRename.Document.Tokens[1].FieldId != "ship_contract_id" {
		t.Fatalf("renamed identities = %#v", afterRename.Document.Tokens)
	}

	deleted, _ := collectionAuthorFixture()
	shipDel := deleted.AuthorTables["tbl_ship"]
	shipDel.Fields = shipDel.Fields[1:]
	deleted.AuthorTables["tbl_ship"] = shipDel
	broken, err := AuthorV2Document(deleted, targets, authored.Document)
	assertFormulaCode(t, err, "formula.reference")
	if broken == nil {
		t.Fatal("missing deleted result")
	}
	wantBroken := "SUM(PROJECT(FILTER(TABLE({出货}), CurrentValue.#REF! == {合同}), CurrentValue.{金额}))"
	if broken.Document.DisplaySource != wantBroken {
		t.Fatalf("deleted display = %q, want %q", broken.Document.DisplaySource, wantBroken)
	}
	if len(broken.Document.Tokens) != 4 || broken.Document.Tokens[1].Kind != "sourceField" ||
		*broken.Document.Tokens[1].FieldId != "ship_contract_id" || *broken.Document.Tokens[1].TableId != "tbl_ship" {
		t.Fatalf("deleted tokens = %#v", broken.Document.Tokens)
	}
	again, err := AuthorV2Document(deleted, targets, broken.Document)
	assertFormulaCode(t, err, "formula.reference")
	if !reflect.DeepEqual(again.Document, broken.Document) {
		t.Fatalf("deleted identity changed: %#v", again.Document)
	}

	noTable, _ := collectionAuthorFixture()
	delete(noTable.AuthorTables, "tbl_ship")
	gone, err := AuthorV2Document(noTable, targets, authored.Document)
	assertFormulaCode(t, err, "formula.reference")
	if gone == nil || !strings.Contains(gone.Document.DisplaySource, "TABLE(#REF!)") {
		t.Fatalf("deleted table = %#v", gone)
	}
	if gone.Document.Tokens[0].Kind != "table" || *gone.Document.Tokens[0].TableId != "tbl_ship" {
		t.Fatalf("deleted table token = %#v", gone.Document.Tokens[0])
	}
	restored, err := RestoreV2AuthorDocument(noTable, targets, canonical, 5)
	assertFormulaCode(t, err, "formula.reference")
	if restored == nil || restored.CanonicalSource != canonical || !strings.Contains(restored.Document.DisplaySource, "TABLE(#REF!)") {
		t.Fatalf("deleted table restore = %#v", restored)
	}
}

func TestAuthorCollectionRestoreCanonicalPreservesBytes(t *testing.T) {
	definition, targets := collectionAuthorFixture()
	missing := `SUM(PROJECT(TABLE("tbl_missing"), CurrentValue.f_gone))`
	restored, err := RestoreV2AuthorDocument(definition, targets, missing, 3)
	assertFormulaCode(t, err, "formula.reference")
	if restored == nil {
		t.Fatal("missing restore result")
	}
	if restored.CanonicalSource != missing {
		t.Fatalf("restore rewrote canonical bytes: %q", restored.CanonicalSource)
	}
	wantDisplay := "SUM(PROJECT(TABLE(#REF!), CurrentValue.#REF!))"
	if restored.Document.DisplaySource != wantDisplay || len(restored.Document.Tokens) != 0 {
		t.Fatalf("missing restore display = %#v", restored.Document)
	}
	again, err := AuthorV2Document(definition, targets, restored.Document)
	assertFormulaCode(t, err, "formula.reference")
	if again == nil || again.CanonicalSource != wantDisplay || !reflect.DeepEqual(again.Document, restored.Document) {
		t.Fatalf("missing round trip = %#v", again)
	}

	valid := "SUM(PROJECT(FILTER(f_lines, CurrentValue.f_amount > 0.0), CurrentValue.f_amount))"
	restoredValid, err := RestoreV2AuthorDocument(definition, targets, valid, 4)
	if err != nil {
		t.Fatal(err)
	}
	wantValidDisplay := "SUM(PROJECT(FILTER({明细}, CurrentValue.{金额} > 0.0), CurrentValue.{金额}))"
	if restoredValid.Document.DisplaySource != wantValidDisplay || restoredValid.CanonicalSource != valid {
		t.Fatalf("relation restore = %#v", restoredValid)
	}
	authoredValid, err := AuthorV2Document(definition, targets, restoredValid.Document)
	if err != nil {
		t.Fatal(err)
	}
	if authoredValid.CanonicalSource != valid || !reflect.DeepEqual(authoredValid.Document, restoredValid.Document) {
		t.Fatalf("relation round trip = %#v", authoredValid)
	}
}

func TestAuthorCollectionRejectsForgedAndMisplacedTokens(t *testing.T) {
	definition, targets := collectionAuthorFixture()
	base, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: "COUNTIF(TABLE({出货}), CurrentValue.{合同} == {合同})", DocumentRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		code   string
		mutate func(*workbench.FormulaAuthorDocument)
	}{
		{"table with fieldId", "formula.author.token", func(d *workbench.FormulaAuthorDocument) {
			id := "contract_id"
			d.Tokens[0].FieldId = &id
		}},
		{"table without tableId", "formula.author.token", func(d *workbench.FormulaAuthorDocument) {
			d.Tokens[0].TableId = nil
		}},
		{"sourceField without tableId", "formula.author.token", func(d *workbench.FormulaAuthorDocument) {
			d.Tokens[1].TableId = nil
		}},
		{"sourceField without fieldId", "formula.author.token", func(d *workbench.FormulaAuthorDocument) {
			d.Tokens[1].FieldId = nil
		}},
		{"sourceField with relation identity", "formula.author.token", func(d *workbench.FormulaAuthorDocument) {
			id := "lines_id"
			d.Tokens[1].RelationFieldId = &id
		}},
		{"table token at field position", "formula.author.token", func(d *workbench.FormulaAuthorDocument) {
			tableID := "tbl_ship"
			d.Tokens[2] = workbench.FormulaAuthorToken{Kind: "table", TableId: &tableID, Range: d.Tokens[2].Range}
		}},
		{"sourceField token without CurrentValue", "formula.author.token", func(d *workbench.FormulaAuthorDocument) {
			fieldID, tableID := "ship_contract_id", "tbl_ship"
			d.Tokens[2] = workbench.FormulaAuthorToken{Kind: "sourceField", FieldId: &fieldID, TableId: &tableID, Range: d.Tokens[2].Range}
		}},
		{"table token covering TABLE prefix", "formula.author.range", func(d *workbench.FormulaAuthorDocument) {
			d.DisplaySource = "TABLE({出货})"
			d.Tokens = d.Tokens[:1]
			d.Tokens[0].Range.Start = workbench.FormulaTextPosition{Line: 1, Character: 0}
			d.Tokens[0].Range.End = workbench.FormulaTextPosition{Line: 1, Character: 11}
		}},
		{"sourceField token covering CurrentValue prefix", "formula.author.range", func(d *workbench.FormulaAuthorDocument) {
			d.DisplaySource = "COUNTIF(TABLE({出货}), CurrentValue.{合同})"
			d.Tokens = d.Tokens[:2]
			d.Tokens[1].Range.Start = workbench.FormulaTextPosition{Line: 1, Character: 21}
			d.Tokens[1].Range.End = workbench.FormulaTextPosition{Line: 1, Character: 37}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := base.Document
			document.Tokens = append([]workbench.FormulaAuthorToken(nil), document.Tokens...)
			test.mutate(&document)
			result, err := AuthorV2Document(definition, targets, document)
			if result != nil || err == nil {
				t.Fatalf("accepted forged binding: %#v / %v", result, err)
			}
			assertFormulaCode(t, err, test.code)
		})
	}

	// A source field token whose table no longer matches its range keeps the
	// identity but resolves to #REF!; it never re-binds by display name.
	mismatch := base.Document
	mismatch.Tokens = append([]workbench.FormulaAuthorToken(nil), mismatch.Tokens...)
	other := "tbl_return"
	mismatch.Tokens[1].TableId = &other
	reference, err := AuthorV2Document(definition, targets, mismatch)
	assertFormulaCode(t, err, "formula.reference")
	if reference == nil || !strings.Contains(reference.Document.DisplaySource, "CurrentValue.#REF!") ||
		reference.Document.Tokens[1].Kind != "sourceField" || *reference.Document.Tokens[1].TableId != "tbl_return" {
		t.Fatalf("mismatch = %#v", reference)
	}
}

func TestAuthorCollectionGuardsScopeLiteralsAndCurrentValueName(t *testing.T) {
	definition, targets := collectionAuthorFixture()
	_, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: "SUM(CurrentValue.{金额})", DocumentRevision: 1,
	})
	assertFormulaCode(t, err, "formula.dependency")
	_, err = AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: "FILTER({合同}, CurrentValue.{金额} > 1.0)", DocumentRevision: 1,
	})
	assertFormulaCode(t, err, "formula.type")
	_, err = AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: "COUNT(TABLE({未知表}))", DocumentRevision: 1,
	})
	assertFormulaCode(t, err, "formula.dependency")

	ambiguous, _ := collectionAuthorFixture()
	returnTable := ambiguous.AuthorTables["tbl_return"]
	returnTable.DisplayName = "出货"
	ambiguous.AuthorTables["tbl_return"] = returnTable
	_, err = AuthorV2Document(ambiguous, targets, workbench.FormulaAuthorDocument{
		DisplaySource: "COUNT(TABLE({出货}))", DocumentRevision: 1,
	})
	assertFormulaCode(t, err, "formula.dependency")

	literal := `"TABLE({出货}) CurrentValue.{合同}" + COUNT(TABLE({出货})) // CurrentValue.{金额} TABLE({x})`
	result, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: literal, DocumentRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `"TABLE({出货}) CurrentValue.{合同}" + COUNT(TABLE("tbl_ship")) // CurrentValue.{金额} TABLE({x})`
	if result.CanonicalSource != want || len(result.Document.Tokens) != 1 {
		t.Fatalf("literal canonical = %q, tokens = %#v", result.CanonicalSource, result.Document.Tokens)
	}

	named, _ := collectionAuthorFixture()
	currentValue := scalarField("current_id", "f_current_value", textType)
	currentValue.DisplayName = "CurrentValue"
	named.Fields = append([]v2.FieldDefinition(nil), named.Fields...)
	named.Fields = append(named.Fields, currentValue)
	namedResult, err := AuthorV2Document(named, targets, workbench.FormulaAuthorDocument{
		DisplaySource: "COUNTIF(TABLE({出货}), CurrentValue.{合同} == {CurrentValue})", DocumentRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCanonical := `COUNTIF(TABLE("tbl_ship"), CurrentValue.f_ship_contract == f_current_value)`
	if namedResult.CanonicalSource != wantCanonical {
		t.Fatalf("CurrentValue-named field canonical = %q", namedResult.CanonicalSource)
	}
	outer := namedResult.Document.Tokens[2]
	if outer.Kind != "field" || *outer.FieldId != "current_id" {
		t.Fatalf("CurrentValue-named token = %#v", outer)
	}
}
