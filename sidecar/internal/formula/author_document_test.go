package formula

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/cel-go/cel"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func authorFixture() (V2Table, map[string]V2Table) {
	lines := relationField("lines_id", "f_lines", "line_items")
	lines.DisplayName = "明细"
	shipping := scalarField("shipping_id", "f_shipping", numberType)
	shipping.DisplayName = "运费"
	amount := scalarField("amount_id", "f_amount", numberType)
	amount.DisplayName = "金额"
	return V2Table{TableID: "orders", Fields: []v2.FieldDefinition{lines, shipping}}, map[string]V2Table{"f_lines": {TableID: "line_items", Fields: []v2.FieldDefinition{amount}}}
}

func TestAuthorDocumentRestoresCompilerAcceptedMemberCall(t *testing.T) {
	field := scalarField("text_id", "f_text", textType)
	field.DisplayName = "文本"
	definition := V2Table{TableID: "table", Fields: []v2.FieldDefinition{field}}
	canonical := "f_text.size()"
	if _, _, err := NewCompiler(DefaultLimits()).InferV2Source(definition, canonical); err != nil {
		t.Fatalf("existing compiler rejects fixture: %v", err)
	}
	restored, err := RestoreV2AuthorDocument(definition, nil, canonical, 1)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Document.DisplaySource != "{文本}.size()" || len(restored.Document.Tokens) != 1 || restored.Document.Tokens[0].Kind != "field" {
		t.Fatalf("member projection = %#v", restored)
	}
	authored, err := AuthorV2Document(definition, nil, restored.Document)
	if err != nil {
		t.Fatal(err)
	}
	if authored.CanonicalSource != canonical {
		t.Fatalf("member round trip = %q", authored.CanonicalSource)
	}
}

func TestAuthorDocumentBoundLabelsAreLexicalAtoms(t *testing.T) {
	for _, name := range []string{"金}额", "金{额", "金\"额", "金'额", "金\r\n😀额", "金) + SUM(额", "金}.{额", "金//额"} {
		t.Run(name, func(t *testing.T) {
			definition, targets := authorFixture()
			definition.Fields[0].DisplayName = name
			definition.Fields[1].DisplayName = name
			target := targets["f_lines"]
			target.Fields[0].DisplayName = name
			targets["f_lines"] = target
			canonical := `f_shipping + relationSum(f_lines, "f_amount") + relationCount(f_lines)`
			restored, err := RestoreV2AuthorDocument(definition, targets, canonical, 5)
			if err != nil {
				t.Fatal(err)
			}
			authored, err := AuthorV2Document(definition, targets, restored.Document)
			if err != nil {
				t.Fatal(err)
			}
			if authored.CanonicalSource != canonical || !reflect.DeepEqual(authored.Document, restored.Document) {
				t.Fatalf("bound label changed semantics: %#v", authored)
			}
			definition.Fields[1].DisplayName = "再次改名"
			renamed, err := AuthorV2Document(definition, targets, authored.Document)
			if err != nil {
				t.Fatal(err)
			}
			if renamed.CanonicalSource != canonical {
				t.Fatalf("stale bound label changed canonical: %#v", renamed)
			}
			renamed.Document.DisplaySource += " + {再次改名}"
			mixed, err := AuthorV2Document(definition, targets, renamed.Document)
			if err != nil || mixed.CanonicalSource != canonical+" + f_shipping" {
				t.Fatalf("bound label interfered with unbound paste: %#v / %v", mixed, err)
			}
		})
	}
}

func TestAuthorDocumentStableIdentityRenameAndRoundTrip(t *testing.T) {
	definition, targets := authorFixture()
	document := workbench.FormulaAuthorDocument{DisplaySource: "SUM({明细}.{金额}) + {运费}", DocumentRevision: 19}
	authored, err := AuthorV2Document(definition, targets, document)
	if err != nil {
		t.Fatal(err)
	}
	canonical := `relationSum(f_lines, "f_amount") + f_shipping`
	if authored.CanonicalSource != canonical || authored.Document.DocumentRevision != 19 || len(authored.Document.Tokens) != 2 {
		t.Fatalf("author = %#v", authored)
	}
	bound := authored.Document.Tokens[0]
	if bound.Kind != "relationTarget" || bound.FieldId != "amount_id" || *bound.RelationFieldId != "lines_id" || *bound.TargetFieldId != "amount_id" || bound.Range.Start.Character != 4 || bound.Range.End.Character != 13 {
		t.Fatalf("target token = %#v", bound)
	}
	definition.Fields[0].DisplayName = "订单😀"
	definition.Fields[1].DisplayName = "新运费"
	target := targets["f_lines"]
	target.Fields[0].DisplayName = "总额"
	duplicate := scalarField("other_id", "f_other", numberType)
	duplicate.DisplayName = "总额"
	target.Fields = append(target.Fields, duplicate)
	targets["f_lines"] = target
	renamed, err := AuthorV2Document(definition, targets, authored.Document)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.CanonicalSource != canonical || renamed.Document.DisplaySource != "SUM({订单😀}.{总额}) + {新运费}" {
		t.Fatalf("renamed = %#v", renamed)
	}
	if renamed.Document.Tokens[0].Range.End.Character != 15 {
		t.Fatalf("emoji range = %#v", renamed.Document.Tokens[0].Range)
	}
	restored, err := RestoreV2AuthorDocument(definition, targets, canonical, 20)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Document.DisplaySource != renamed.Document.DisplaySource || restored.CanonicalSource != canonical {
		t.Fatalf("restored = %#v", restored)
	}
	again, err := AuthorV2Document(definition, targets, restored.Document)
	if err != nil {
		t.Fatal(err)
	}
	if again.CanonicalSource != canonical {
		t.Fatalf("round trip = %q", again.CanonicalSource)
	}
	_, err = AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{DisplaySource: renamed.Document.DisplaySource, DocumentRevision: 21})
	assertFormulaCode(t, err, "formula.dependency")
}

func TestAuthorRelationTokenMatchesContractAndCountRoundTrip(t *testing.T) {
	definition, targets := authorFixture()
	result, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{DisplaySource: "COUNT({明细})", DocumentRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	token := result.Document.Tokens[0]
	if token.Kind != "relation" || token.FieldId != "lines_id" || token.RelationFieldId == nil || *token.RelationFieldId != token.FieldId || token.TargetFieldId != nil {
		t.Fatalf("relation contract = %#v", token)
	}
	restored, err := RestoreV2AuthorDocument(definition, targets, result.CanonicalSource, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Document, result.Document) {
		t.Fatalf("COUNT round trip = %#v", restored)
	}
	result.Document.Tokens[0].RelationFieldId = nil
	_, err = AuthorV2Document(definition, targets, result.Document)
	assertFormulaCode(t, err, "formula.author.token")
}

func TestAuthorSourceMapUsesCompilerErrorSpanWithoutTrimming(t *testing.T) {
	definition, targets := authorFixture()
	source := " \r\n '😀' == '😀' ? {运费} : missing"
	result, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{DisplaySource: source, DocumentRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, _, compileErr := NewCompiler(DefaultLimits()).InferV2Source(definition, result.CanonicalSource)
	assertFormulaCode(t, compileErr, "formula.dependency")
	if compileErr.SourceSpan == nil {
		t.Fatal("compiler discarded CEL location")
	}
	value, ok := result.SourceMap.DisplayRange(*compileErr.SourceSpan)
	if !ok || value.Start.Line != 1 || value.Start.Character != 23 {
		t.Fatalf("compiler display range = %#v (%v)", value, compileErr)
	}
	coordinates, _ := indexAuthorSource(source)
	span, rangeErr := coordinates.byteRange(value)
	if rangeErr != nil || source[span.Start:span.End] != "m" {
		t.Fatalf("diagnostic does not point to missing: %#v / %v", value, rangeErr)
	}
}

func FuzzAuthorDocumentCanonicalRoundTrip(f *testing.F) {
	for _, seed := range []string{"", "中文😀", "quote\"slash\\", "line\r\n{运费} SUM(f_shipping)"} {
		f.Add(seed, uint8(0))
		f.Add(seed, uint8(3))
	}
	f.Fuzz(func(t *testing.T, literal string, variant uint8) {
		if len(literal) > 128 || !utf8.ValidString(literal) {
			t.Skip()
		}
		definition, targets := authorFixture()
		functions := []string{"SUM", "AVERAGE", "MIN", "MAX", "COUNTA"}
		display := strconv.Quote(literal) + " == " + strconv.Quote(literal) + " ? " + functions[int(variant)%len(functions)] + "({明细}.{金额}) : {运费}"
		authored, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{DisplaySource: display, DocumentRevision: int64(variant) + 1})
		if err != nil {
			t.Fatal(err)
		}
		definition.Fields[1].DisplayName = "改名😀" + literal
		target := targets["f_lines"]
		target.Fields[0].DisplayName = "目标" + literal
		targets["f_lines"] = target
		restored, err := RestoreV2AuthorDocument(definition, targets, authored.CanonicalSource, authored.Document.DocumentRevision)
		if err != nil {
			t.Fatal(err)
		}
		again, err := AuthorV2Document(definition, targets, restored.Document)
		if err != nil {
			t.Fatal(err)
		}
		if again.CanonicalSource != authored.CanonicalSource || !reflect.DeepEqual(again.Document, restored.Document) {
			t.Fatalf("round trip changed canonical or normalized tokens: %#v / %#v", authored, again)
		}
		for _, token := range again.Document.Tokens {
			coordinates, _ := indexAuthorSource(again.Document.DisplaySource)
			if _, err := coordinates.byteRange(token.Range); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestAuthorDocumentDeletionRetainsIdentityAcrossEdits(t *testing.T) {
	for _, removeTarget := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "target"}[removeTarget], func(t *testing.T) {
			definition, targets := authorFixture()
			display := "{运费}"
			if removeTarget {
				display = "SUM({明细}.{金额})"
			}
			original, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{DisplaySource: display, DocumentRevision: 1})
			if err != nil {
				t.Fatal(err)
			}
			if removeTarget {
				replacement := scalarField("replacement_id", "f_replacement", numberType)
				replacement.DisplayName = "金额"
				targets["f_lines"] = V2Table{TableID: "line_items", Fields: []v2.FieldDefinition{replacement}}
			} else {
				definition.Fields = definition.Fields[:1]
				replacement := scalarField("replacement_id", "f_replacement", numberType)
				replacement.DisplayName = "运费"
				definition.Fields = append(definition.Fields, replacement)
			}
			deleted, err := AuthorV2Document(definition, targets, original.Document)
			assertFormulaCode(t, err, "formula.reference")
			if deleted == nil || !strings.Contains(deleted.Document.DisplaySource, "#REF!") || len(deleted.Document.Tokens) != 1 || deleted.Document.Tokens[0].FieldId != original.Document.Tokens[0].FieldId || err.Message != "#REF!" {
				t.Fatalf("deleted = %#v; %v", deleted, err)
			}
			again, err := AuthorV2Document(definition, targets, deleted.Document)
			assertFormulaCode(t, err, "formula.reference")
			if !reflect.DeepEqual(again.Document, deleted.Document) {
				t.Fatalf("deleted identity changed: %#v", again)
			}
			restored, err := RestoreV2AuthorDocument(definition, targets, original.CanonicalSource, 3)
			assertFormulaCode(t, err, "formula.reference")
			if restored == nil || restored.CanonicalSource != original.CanonicalSource || !strings.Contains(restored.Document.DisplaySource, "#REF!") {
				t.Fatalf("missing restore = %#v", restored)
			}
		})
	}
}

func TestAuthorDocumentRejectsForgedRangesAndIdentityCombinations(t *testing.T) {
	definition, targets := authorFixture()
	base, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{DisplaySource: "{运费}", DocumentRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*workbench.FormulaAuthorDocument)
	}{
		{"partial", func(d *workbench.FormulaAuthorDocument) { d.Tokens[0].Range.End.Character-- }},
		{"overlap", func(d *workbench.FormulaAuthorDocument) { d.Tokens = append(d.Tokens, d.Tokens[0]) }},
		{"field kind", func(d *workbench.FormulaAuthorDocument) { d.Tokens[0].Kind = "relation" }},
		{"unknown kind", func(d *workbench.FormulaAuthorDocument) { d.Tokens[0].Kind = "anything" }},
		{"id combination", func(d *workbench.FormulaAuthorDocument) { id := "amount_id"; d.Tokens[0].TargetFieldId = &id }},
		{"literal", func(d *workbench.FormulaAuthorDocument) {
			d.DisplaySource = `"{运费}"`
			d.Tokens[0].Range.Start.Character = 1
			d.Tokens[0].Range.End.Character = 5
		}},
		{"missing literal", func(d *workbench.FormulaAuthorDocument) {
			d.DisplaySource = `"#REF!"`
			d.Tokens[0].FieldId = "deleted"
			d.Tokens[0].Range.Start.Character = 1
			d.Tokens[0].Range.End.Character = 6
		}},
		{"comment", func(d *workbench.FormulaAuthorDocument) {
			d.DisplaySource = "// {运费}"
			d.Tokens[0].Range.Start.Character = 3
			d.Tokens[0].Range.End.Character = 7
		}},
		{"surrogate", func(d *workbench.FormulaAuthorDocument) {
			d.DisplaySource = "😀{运费}"
			d.Tokens[0].Range.Start.Character = 1
			d.Tokens[0].Range.End.Character = 6
		}},
		{"revision", func(d *workbench.FormulaAuthorDocument) { d.DocumentRevision = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := base.Document
			document.Tokens = append([]workbench.FormulaAuthorToken(nil), document.Tokens...)
			test.mutate(&document)
			result, err := AuthorV2Document(definition, targets, document)
			if result != nil || err == nil {
				t.Fatalf("accepted invalid binding: %#v / %v", result, err)
			}
		})
	}
	path, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{DisplaySource: "SUM({明细}.{金额})", DocumentRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	wrong := "shipping_id"
	path.Document.Tokens[0].TargetFieldId = &wrong
	_, err = AuthorV2Document(definition, targets, path.Document)
	assertFormulaCode(t, err, "formula.author.token")
	_, err = AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{DisplaySource: strings.Repeat("x", DefaultSourceLimit+1), DocumentRevision: 1})
	assertFormulaCode(t, err, "formula.resource_limit")
}

func TestAuthorDocumentPreservesCELLiteralsAndComments(t *testing.T) {
	definition, targets := authorFixture()
	for _, literal := range []string{`"{运费} SUM(f_shipping)"`, `'{运费} SUM(f_shipping)'`, `r'{运费}\SUM(f_shipping)'`, `b'{运费}'`, `'''{运费} SUM(f_shipping)'''`, `"""{运费}
SUM(f_shipping)"""`, `"escaped \" {运费} SUM(f_shipping)"`} {
		display := literal + " + {运费} // {运费} SUM(f_shipping)"
		authored, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{DisplaySource: display, DocumentRevision: 1})
		if err != nil {
			t.Fatal(err)
		}
		want := literal + " + f_shipping // {运费} SUM(f_shipping)"
		if authored.CanonicalSource != want || len(authored.Document.Tokens) != 1 {
			t.Fatalf("literal altered: %q -> %q", display, authored.CanonicalSource)
		}
		restored, err := RestoreV2AuthorDocument(definition, targets, want, 1)
		if err != nil {
			t.Fatal(err)
		}
		if restored.Document.DisplaySource != display {
			t.Fatalf("literal restore = %q", restored.Document.DisplaySource)
		}
	}
}

func TestAuthorSourceMapMapsRealCELDiagnosticAfterUnicodeAndAggregate(t *testing.T) {
	definition, targets := authorFixture()
	display := "'😀' == '😀' ? SUM({明细}.{金额}) :\r\n  {运费} + missing"
	authored, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{DisplaySource: display, DocumentRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := cel.NewEnv(cel.Variable("f_shipping", cel.DoubleType), cel.Variable("f_lines", cel.DynType))
	_, issues := env.Compile(authored.CanonicalSource)
	if issues == nil || issues.Err() == nil {
		t.Fatal("expected actual CEL diagnostics")
	}
	foundMissing := false
	for _, diagnostic := range issues.Errors() {
		rangeValue, ok := authored.SourceMap.CELRange(diagnostic.Location.Line(), diagnostic.Location.Column())
		if !ok {
			t.Fatalf("unmapped diagnostic: %v", diagnostic)
		}
		if strings.Contains(diagnostic.Message, "missing") {
			foundMissing = true
			if rangeValue.Start.Line != 1 || rangeValue.Start.Character != 9 {
				t.Fatalf("UTF16 diagnostic range = %#v", rangeValue)
			}
		}
	}
	if !foundMissing {
		t.Fatalf("missing diagnostic: %v", issues)
	}
	start := strings.Index(authored.CanonicalSource, `"f_amount"`)
	rangeValue, ok := authored.SourceMap.DisplayRange(SourceSpan{Start: start + 1, End: start + 4})
	if !ok || rangeValue != authored.Document.Tokens[0].Range {
		t.Fatalf("aggregate mapping = %#v", rangeValue)
	}
	if _, ok := authored.SourceMap.DisplayRange(SourceSpan{Start: -1, End: 2}); ok {
		t.Fatal("negative span accepted")
	}
}
