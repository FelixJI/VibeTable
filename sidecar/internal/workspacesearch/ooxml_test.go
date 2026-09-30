package workspacesearch

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	contracts "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
)

// Real OOXML namespaces: inline fixtures must prove legal standard packages,
// never pseudo namespaces. The committed tests/fixtures/ooxml packages stay
// the independent visible-text oracle.
const (
	testWordNS         = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	testRelNS          = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	testDrawingNS      = "http://schemas.openxmlformats.org/drawingml/2006/main"
	testPresentationNS = "http://schemas.openxmlformats.org/presentationml/2006/main"
	testSpreadsheetNS  = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	testPackageRelNS   = "http://schemas.openxmlformats.org/package/2006/relationships"
	testForeignNS      = "http://example.test/foreign"
)

func testRelType(suffix string) string { return testRelNS + "/" + suffix }

// loadOOXMLPackageFixture zips the committed synthetic package parts under
// tests/fixtures/ooxml/<name> and returns the archive plus the independent
// expected visible text (expected.txt keeps one trailing newline on disk).
func loadOOXMLPackageFixture(t *testing.T, name string) ([]byte, string) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "tests", "fixtures", "ooxml", name)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var expected string
	var names []string
	contents := map[string][]byte{}
	read := func(relative string, data []byte) {
		if relative == "expected.txt" {
			expected = strings.TrimSuffix(string(data), "\n")
			return
		}
		names = append(names, relative)
		contents[relative] = data
	}
	for _, entry := range entries {
		if entry.IsDir() {
			directory := filepath.Join(root, entry.Name())
			walk := filepath.WalkDir(directory, func(path string, item fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if item.IsDir() {
					return nil
				}
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				read(filepath.ToSlash(relative), data)
				return nil
			})
			if walk != nil {
				t.Fatal(walk)
			}
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		read(entry.Name(), data)
	}
	if expected == "" {
		t.Fatalf("fixture %s is missing expected.txt", name)
	}
	sort.Strings(names)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range names {
		part, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(contents[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes(), expected
}

func extractFixture(t *testing.T, name, fileName, mime string) ExtractionResult {
	t.Helper()
	payload, _ := loadOOXMLPackageFixture(t, name)
	return Extract(context.Background(), fileName, mime, bytes.NewReader(payload), DefaultExtractionLimits)
}

// TestDOCXVisibleTextMatchesCommittedOracle fixes the visible-text
// semantics: runs join without inserted separators, xml:space is honored,
// tabs and paragraph boundaries are preserved, and the split-run package
// produces exactly the same visible text as the equivalent unsplit package.
func TestDOCXVisibleTextMatchesCommittedOracle(t *testing.T) {
	split := extractFixture(t, "docx-contract-split", "contract-split.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	if split.Status != ExtractionIndexed || split.ErrorCode != nil {
		t.Fatalf("split Extract() = %#v", split)
	}
	whole := extractFixture(t, "docx-contract-whole", "contract-whole.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	if whole.Status != ExtractionIndexed || whole.ErrorCode != nil {
		t.Fatalf("whole Extract() = %#v", whole)
	}
	if split.Text != whole.Text {
		t.Fatalf("split/whole visible text diverged:\nsplit = %q\nwhole = %q", split.Text, whole.Text)
	}
	_, expected := loadOOXMLPackageFixture(t, "docx-contract-split")
	if split.Text != expected {
		t.Fatalf("DOCX visible text = %q, want oracle %q", split.Text, expected)
	}
}

// TestDOCXExcludesDeletedMovedFromAndFieldInstructions asserts the current
// visible body excludes tracked deletions and move sources, keeps move
// targets, excludes field instructions while keeping rendered field
// results, and never joins two paragraphs into one word.
func TestDOCXExcludesDeletedMovedFromAndFieldInstructions(t *testing.T) {
	body := fmt.Sprintf(`<w:document xmlns:w="%s" xmlns:r="%s"><w:body>
	  <w:p>
	    <w:del w:id="1" w:author="a"><w:r><w:delText>DeletedVisibleText 已删除正文</w:delText></w:r></w:del>
	    <w:moveFrom w:id="2" w:author="a"><w:r><w:t>MovedAwaySourceText 已迁出原文</w:t></w:r></w:moveFrom>
	    <w:moveTo w:id="3" w:author="a"><w:r><w:t>MovedTargetText 已迁入</w:t></w:r></w:moveTo>
	    <w:r><w:instrText xml:space="preserve"> HYPERLINK "mailto:nobody@example.test" </w:instrText></w:r>
	    <w:fldSimple w:instr=" REF ContractNumber "><w:r><w:t>VisibleFieldResult</w:t></w:r></w:fldSimple>
	  </w:p>
	  <w:p><w:r><w:t>合同</w:t></w:r></w:p>
	  <w:p><w:r><w:t>编号</w:t></w:r></w:p>
	</w:body></w:document>`, testWordNS, testRelNS)
	result := Extract(context.Background(), "revisions.docx", "", bytes.NewReader(ooxml(t, "word/document.xml", body)), DefaultExtractionLimits)
	if result.Status != ExtractionIndexed ||
		result.Text != "MovedTargetText 已迁入VisibleFieldResult\n合同\n编号" {
		t.Fatalf("DOCX visible text = %#v", result)
	}
	for _, forbidden := range []string{
		"DeletedVisibleText", "已删除正文", "HYPERLINK", "mailto",
		"MovedAwaySourceText", "已迁出原文",
	} {
		if strings.Contains(result.Text, forbidden) {
			t.Fatalf("DOCX visible text indexed excluded content %q: %q", forbidden, result.Text)
		}
	}
}

// TestDOCXKeepsTabsBreaksAndReferencedHeaderFooterParts pins whitespace
// semantics plus relationship-driven header/footer coverage: only parts
// referenced through w:headerReference/w:footerReference in the main part's
// rels are indexed; an unreferenced stray header part is not.
func TestDOCXKeepsTabsBreaksAndReferencedHeaderFooterParts(t *testing.T) {
	payload := ooxmlMany(t, map[string]string{
		"word/document.xml": fmt.Sprintf(`<w:document xmlns:w="%s" xmlns:r="%s"><w:body>
		  <w:p><w:r><w:t>第一行</w:t><w:br/><w:t>第二行</w:t><w:tab/><w:t>右侧</w:t></w:r></w:p>
		  <w:sectPr>
		    <w:headerReference w:type="default" r:id="rIdH1"/>
		    <w:footerReference w:type="default" r:id="rIdF1"/>
		  </w:sectPr>
		</w:body></w:document>`, testWordNS, testRelNS),
		"word/_rels/document.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rIdH1" Type="%s" Target="header1.xml"/>
		  <Relationship Id="rIdF1" Type="%s" Target="footer1.xml"/>
		</Relationships>`, testPackageRelNS, testRelType("header"), testRelType("footer")),
		"word/header1.xml": fmt.Sprintf(`<w:hdr xmlns:w="%s"><w:p><w:r><w:t>页眉可见 HeaderVisible</w:t></w:r></w:p></w:hdr>`, testWordNS),
		"word/footer1.xml": fmt.Sprintf(`<w:ftr xmlns:w="%s"><w:p><w:r><w:t>页脚可见 FooterVisible</w:t></w:r></w:p></w:ftr>`, testWordNS),
		"word/header9.xml": fmt.Sprintf(`<w:hdr xmlns:w="%s"><w:p><w:r><w:t>UNREFERENCED-HEADER-SENTINEL</w:t></w:r></w:p></w:hdr>`, testWordNS),
	})
	result := Extract(context.Background(), "layout.docx", "", bytes.NewReader(payload), DefaultExtractionLimits)
	if result.Status != ExtractionIndexed {
		t.Fatalf("layout Extract() = %#v", result)
	}
	if !strings.Contains(result.Text, "第一行\n第二行\t右侧") {
		t.Fatalf("layout text lost tab/line-break boundaries: %q", result.Text)
	}
	for _, want := range []string{"页眉可见 HeaderVisible", "页脚可见 FooterVisible"} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("referenced header/footer text %q missing: %q", want, result.Text)
		}
	}
	if strings.Contains(result.Text, "UNREFERENCED-HEADER-SENTINEL") {
		t.Fatalf("unreferenced header part was indexed: %q", result.Text)
	}
}

// TestXLSXFormulaWithoutCacheMarksPartialAndCacheClearsIt isolates the
// formula-cache honesty contract from every other partial source:
// uncalculated numeric formulas — written by openpyxl as <f>…</f><v></v>
// — warn (indexed + extract.ooxml_partial), while a real cache never
// warns; only a t="str" empty string is a legal empty cache. Formulas are
// never recalculated.
func TestXLSXFormulaWithoutCacheMarksPartialAndCacheClearsIt(t *testing.T) {
	cases := []struct {
		name        string
		cell        string
		wantText    string
		wantPartial bool
	}{
		{"formula without v", `<c r="B1"><f>NOW()</f></c>`, "", true},
		{"numeric formula with empty cache", `<c r="B1"><f>SUM(1,2)</f><v/></c>`, "", true},
		{"string formula with empty cache", `<c r="B1" t="str"><f>A1&amp;""</f><v/></c>`, "", false},
		{"formula with numeric cache", `<c r="B1"><f>SUM(1,2)</f><v>3</v></c>`, "3", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			payload := ooxmlMany(t, map[string]string{
				"xl/workbook.xml": fmt.Sprintf(`<workbook xmlns="%s" xmlns:r="%s"><sheets>
				  <sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
					testSpreadsheetNS, testRelNS),
				"xl/_rels/workbook.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
				  <Relationship Id="rId1" Type="%s" Target="worksheets/sheet1.xml"/></Relationships>`,
					testPackageRelNS, testRelType("worksheet")),
				"xl/worksheets/sheet1.xml": fmt.Sprintf(`<worksheet xmlns="%s"><sheetData>
				  <row r="1"><c r="A1"><v>42</v></c>%s</row>
				</sheetData></worksheet>`, testSpreadsheetNS, test.cell),
			})
			result := Extract(context.Background(), "formula-cache.xlsx", "", bytes.NewReader(payload), DefaultExtractionLimits)
			wantBody := "42"
			if test.wantText != "" {
				wantBody += "\n" + test.wantText
			}
			if result.Status != ExtractionIndexed || result.Text != wantBody {
				t.Fatalf("Extract() = %#v, want indexed %q", result, wantBody)
			}
			if test.wantPartial && (result.ErrorCode == nil || *result.ErrorCode != ooxmlPartialCode) {
				t.Fatalf("Extract() = %#v, want partial code %q", result, ooxmlPartialCode)
			}
			if !test.wantPartial && result.ErrorCode != nil {
				t.Fatalf("cached formula must not warn: %#v", result)
			}
		})
	}
}

// TestDOCXIndexesTextBoxesWalkedWithTheStory proves the matrix claim that
// text boxes inside the main story are covered as part of the story walk,
// including paragraph boundaries: nested w:txbxContent w:p paragraphs flush
// on their own, so story text before and after the box never merges with
// the box text into a fake word, and the mc:Fallback duplicate is skipped.
func TestDOCXIndexesTextBoxesWalkedWithTheStory(t *testing.T) {
	body := fmt.Sprintf(`<w:document xmlns:w="%s" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"><w:body>
	  <w:p>
	    <w:r><w:t>正文旁</w:t></w:r>
	    <mc:AlternateContent>
	      <mc:Choice><w:drawing><w:txbxContent><w:p><w:r><w:t>文本框内容 TextBoxBody</w:t></w:r></w:p></w:txbxContent></w:drawing></mc:Choice>
	      <mc:Fallback><w:pict><w:txbxContent><w:p><w:r><w:t>TextBoxBody</w:t></w:r></w:p></w:txbxContent></w:pict></mc:Fallback>
	    </mc:AlternateContent>
	    <w:r><w:t>正文后</w:t></w:r>
	  </w:p>
	</w:body></w:document>`, testWordNS)
	result := Extract(
		context.Background(), "textbox.docx", "",
		bytes.NewReader(ooxml(t, "word/document.xml", body)), DefaultExtractionLimits,
	)
	if result.Status != ExtractionIndexed {
		t.Fatalf("textbox Extract() = %#v", result)
	}
	if result.Text != "正文旁\n文本框内容 TextBoxBody\n正文后" {
		t.Fatalf("textbox text must keep story and box paragraph boundaries: %q", result.Text)
	}
	if strings.Count(result.Text, "TextBoxBody") != 1 {
		t.Fatalf("textbox text must appear once (Choice, not Fallback): %q", result.Text)
	}
}

// TestDOCXMarkUncoveredReferencesPartial asserts footnote/endnote/comment
// references surface indexed text plus extract.ooxml_partial: those regions
// are intentionally not covered and the result says so.
func TestDOCXMarkUncoveredReferencesPartial(t *testing.T) {
	body := fmt.Sprintf(`<w:document xmlns:w="%s" xmlns:r="%s"><w:body>
	  <w:p><w:r><w:t>正文可见</w:t></w:r>
	    <w:footnoteReference w:id="1"/>
	    <w:commentReference w:id="2"/>
	  </w:p>
	</w:body></w:document>`, testWordNS, testRelNS)
	result := Extract(
		context.Background(), "refs.docx", "",
		bytes.NewReader(ooxml(t, "word/document.xml", body)), DefaultExtractionLimits,
	)
	if result.Status != ExtractionIndexed || result.Text != "正文可见" ||
		result.ErrorCode == nil || *result.ErrorCode != ooxmlPartialCode {
		t.Fatalf("uncovered references = %#v", result)
	}
}

// TestXLSXVisibleTextDereferencesSharedStringsByRealCellsOnly fixes the
// cell semantics against the committed oracle. The oracle itself carries an
// out-of-range shared reference and a formula without a cached value, so
// the honest result is indexed text plus extract.ooxml_partial.
func TestXLSXVisibleTextDereferencesSharedStringsByRealCellsOnly(t *testing.T) {
	result := extractFixture(t, "xlsx-ledger", "ledger.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	if result.Status != ExtractionIndexed ||
		result.ErrorCode == nil || *result.ErrorCode != ooxmlPartialCode {
		t.Fatalf("xlsx Extract() = %#v", result)
	}
	_, expected := loadOOXMLPackageFixture(t, "xlsx-ledger")
	if result.Text != expected {
		t.Fatalf("XLSX visible text = %q, want oracle %q", result.Text, expected)
	}
	for _, forbidden := range []string{
		"SENTINEL-UNREFERENCED-STRING", "SUM(1,2)", "SUM(A1:B1)",
	} {
		if strings.Contains(result.Text, forbidden) {
			t.Fatalf("XLSX visible text leaked non-visible content %q: %q", forbidden, result.Text)
		}
	}
}

// TestXLSXHonestPartialForMissingCacheOutOfRangeAndDates pins every
// intentional non-coverage: formulas without a cached value, out-of-range
// shared string indexes, and date-formatted cells (serial indexed, rendered
// date not covered) all surface indexed text plus extract.ooxml_partial.
func TestXLSXHonestPartialForMissingCacheOutOfRangeAndDates(t *testing.T) {
	payload := ooxmlMany(t, map[string]string{
		"xl/workbook.xml": fmt.Sprintf(`<workbook xmlns="%s" xmlns:r="%s"><sheets>
		  <sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			testSpreadsheetNS, testRelNS),
		"xl/_rels/workbook.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId1" Type="%s" Target="worksheets/sheet1.xml"/>
		  <Relationship Id="rId2" Type="%s" Target="strings.xml"/>
		  <Relationship Id="rId3" Type="%s" Target="styles.xml"/>
		</Relationships>`, testPackageRelNS,
			testRelType("worksheet"), testRelType("sharedStrings"), testRelType("styles")),
		"xl/strings.xml": fmt.Sprintf(`<sst xmlns="%s">
		  <si><t>可见Cell文本</t></si>
		  <si><t>SENTINEL-UNREFERENCED</t></si>
		</sst>`, testSpreadsheetNS),
		"xl/styles.xml": fmt.Sprintf(`<styleSheet xmlns="%s">
		  <numFmts><numFmt numFmtId="164" formatCode="yyyy-mm-dd"/></numFmts>
		  <cellXfs>
		    <xf numFmtId="0"/>
		    <xf numFmtId="164"/>
		  </cellXfs>
		</styleSheet>`, testSpreadsheetNS),
		"xl/worksheets/sheet1.xml": fmt.Sprintf(`<worksheet xmlns="%s"><sheetData>
		  <row r="1">
		    <c r="A1" t="s"><v>0</v></c>
		    <c r="B1" s="1"><v>46023</v></c>
		    <c r="C1"><f>NOW()</f></c>
		  </row>
		</sheetData></worksheet>`, testSpreadsheetNS),
	})
	result := Extract(context.Background(), "partial.xlsx", "", bytes.NewReader(payload), DefaultExtractionLimits)
	if result.Status != ExtractionIndexed || result.Text != "可见Cell文本\n46023" ||
		result.ErrorCode == nil || *result.ErrorCode != ooxmlPartialCode {
		t.Fatalf("honest partial xlsx = %#v", result)
	}
	if strings.Contains(result.Text, "NOW") || strings.Contains(result.Text, "SENTINEL") {
		t.Fatalf("xlsx leaked hidden content: %q", result.Text)
	}

	clean := ooxmlMany(t, map[string]string{
		"xl/workbook.xml": fmt.Sprintf(`<workbook xmlns="%s" xmlns:r="%s"><sheets>
		  <sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			testSpreadsheetNS, testRelNS),
		"xl/_rels/workbook.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId1" Type="%s" Target="worksheets/sheet1.xml"/>
		</Relationships>`, testPackageRelNS, testRelType("worksheet")),
		"xl/worksheets/sheet1.xml": fmt.Sprintf(`<worksheet xmlns="%s"><sheetData>
		  <row r="1"><c r="A1"><v>42</v></c><c r="B1"><f>SUM(1,2)</f><v>3</v></c></row>
		</sheetData></worksheet>`, testSpreadsheetNS),
	})
	cleanResult := Extract(context.Background(), "clean.xlsx", "", bytes.NewReader(clean), DefaultExtractionLimits)
	if cleanResult.Status != ExtractionIndexed || cleanResult.Text != "42\n3" ||
		cleanResult.ErrorCode != nil {
		t.Fatalf("fully covered xlsx = %#v", cleanResult)
	}
}

// TestXLSXResolvesNonStandardPartLocations proves relationship resolution by
// Type accepts legal non-standard part paths instead of guessing by
// directory prefix.
func TestXLSXResolvesNonStandardPartLocations(t *testing.T) {
	payload := ooxmlMany(t, map[string]string{
		"_rels/.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId1" Type="%s" Target="book/main.xml"/>
		</Relationships>`, testPackageRelNS, testRelType("officeDocument")),
		"book/main.xml": fmt.Sprintf(`<workbook xmlns="%s" xmlns:r="%s"><sheets>
		  <sheet name="S1" sheetId="1" r:id="rIdW"/></sheets></workbook>`,
			testSpreadsheetNS, testRelNS),
		"book/_rels/main.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rIdW" Type="%s" Target="../sheets/panel.xml"/>
		  <Relationship Id="rIdS" Type="%s" Target="../strings/custom.xml"/>
		</Relationships>`, testPackageRelNS,
			testRelType("worksheet"), testRelType("sharedStrings")),
		"sheets/panel.xml": fmt.Sprintf(`<worksheet xmlns="%s"><sheetData>
		  <row r="1"><c r="A1" t="s"><v>0</v></c></row>
		</sheetData></worksheet>`, testSpreadsheetNS),
		"strings/custom.xml": fmt.Sprintf(`<sst xmlns="%s"><si><t>非标准位置SharedString</t></si></sst>`, testSpreadsheetNS),
	})
	result := Extract(context.Background(), "custom.xlsx", "", bytes.NewReader(payload), DefaultExtractionLimits)
	if result.Status != ExtractionIndexed || result.Text != "非标准位置SharedString" ||
		result.ErrorCode != nil {
		t.Fatalf("non-standard xlsx locations = %#v", result)
	}
}

// TestXLSXBadRelationshipsFailClosedOrMarkPartial distinguishes corrupt
// relationship wiring (fail closed) from a single missing referenced part
// (indexed text plus the partial marker).
func TestXLSXBadRelationshipsFailClosedOrMarkPartial(t *testing.T) {
	missingPart := ooxmlMany(t, map[string]string{
		"xl/workbook.xml": fmt.Sprintf(`<workbook xmlns="%s" xmlns:r="%s"><sheets>
		  <sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			testSpreadsheetNS, testRelNS),
		"xl/_rels/workbook.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId1" Type="%s" Target="worksheets/absent.xml"/>
		</Relationships>`, testPackageRelNS, testRelType("worksheet")),
	})
	result := Extract(context.Background(), "missing.xlsx", "", bytes.NewReader(missingPart), DefaultExtractionLimits)
	if result.Status != ExtractionIndexed || result.Text != "" ||
		result.ErrorCode == nil || *result.ErrorCode != ooxmlPartialCode {
		t.Fatalf("missing sheet part = %#v", result)
	}

	corruptRels := ooxmlMany(t, map[string]string{
		"xl/workbook.xml": fmt.Sprintf(`<workbook xmlns="%s" xmlns:r="%s"><sheets>
		  <sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			testSpreadsheetNS, testRelNS),
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="broken`,
	})
	assertExtractionCode(
		t,
		Extract(context.Background(), "corrupt.xlsx", "", bytes.NewReader(corruptRels), DefaultExtractionLimits),
		ExtractionFailed, "extract.ooxml_part_failed",
	)
}

// TestPPTXFollowsPresentationRelationshipOrder pins slide ordering by the
// package relationships, run joining inside a paragraph, notes coverage,
// and the exclusion of unreferenced slide parts.
func TestPPTXFollowsPresentationRelationshipOrder(t *testing.T) {
	result := extractFixture(t, "pptx-slides-reordered", "deck.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
	if result.Status != ExtractionIndexed || result.ErrorCode != nil {
		t.Fatalf("pptx Extract() = %#v", result)
	}
	_, expected := loadOOXMLPackageFixture(t, "pptx-slides-reordered")
	if result.Text != expected {
		t.Fatalf("PPTX visible text = %q, want oracle %q", result.Text, expected)
	}
	if strings.Contains(result.Text, "UNREFERENCED-SLIDE-SENTINEL") {
		t.Fatalf("unreferenced slide part was indexed: %q", result.Text)
	}
}

// TestPPTXFailsClosedWithoutRelationshipsAndReportsPartialForDamagedOnes
// pins the hard AC4 boundary: a presentation without readable presentation
// wiring fails closed (no silent filename-order fallback), while a single
// unresolvable sldId stays indexed with the partial marker.
func TestPPTXFailsClosedWithoutRelationshipsAndReportsPartialForDamagedOnes(t *testing.T) {
	payload := ooxmlMany(t, map[string]string{
		"ppt/slides/slide1.xml": fmt.Sprintf(`<p:sld xmlns:a="%s" xmlns:p="%s"><a:p><a:r><a:t>First Slide</a:t></a:r></a:p></p:sld>`,
			testDrawingNS, testPresentationNS),
		"ppt/slides/slide2.xml": fmt.Sprintf(`<p:sld xmlns:a="%s" xmlns:p="%s"><a:p><a:r><a:t>Second Slide</a:t></a:r></a:p></p:sld>`,
			testDrawingNS, testPresentationNS),
	})
	result := Extract(context.Background(), "fallback.pptx", "", bytes.NewReader(payload), DefaultExtractionLimits)
	assertExtractionCode(t, result, ExtractionFailed, "extract.ooxml_part_failed")

	damaged := ooxmlMany(t, map[string]string{
		"ppt/presentation.xml": fmt.Sprintf(`<p:presentation xmlns:r="%s" xmlns:p="%s"><p:sldIdLst>
		  <p:sldId id="256" r:id="rIdGone"/><p:sldId id="257" r:id="rId1"/></p:sldIdLst></p:presentation>`,
			testRelNS, testPresentationNS),
		"ppt/_rels/presentation.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId1" Type="%s" Target="slides/slide1.xml"/></Relationships>`,
			testPackageRelNS, testRelType("slide")),
		"ppt/slides/slide1.xml": fmt.Sprintf(`<p:sld xmlns:a="%s" xmlns:p="%s"><a:p><a:r><a:t>Reachable Slide</a:t></a:r></a:p></p:sld>`,
			testDrawingNS, testPresentationNS),
	})
	damagedResult := Extract(context.Background(), "damaged.pptx", "", bytes.NewReader(damaged), DefaultExtractionLimits)
	if damagedResult.Status != ExtractionIndexed || damagedResult.ErrorCode == nil ||
		*damagedResult.ErrorCode != ooxmlPartialCode ||
		damagedResult.Text != "Reachable Slide" {
		t.Fatalf("damaged pptx = %#v", damagedResult)
	}
}

// TestPPTXNotesFollowSlideRelationshipsToAnyLocation proves notes coverage
// resolves through each slide's relationships by Type, accepting legal
// non-standard part paths.
func TestPPTXNotesFollowSlideRelationshipsToAnyLocation(t *testing.T) {
	payload := ooxmlMany(t, map[string]string{
		"ppt/presentation.xml": fmt.Sprintf(`<p:presentation xmlns:r="%s" xmlns:p="%s"><p:sldIdLst>
		  <p:sldId id="256" r:id="rId1"/></p:sldIdLst></p:presentation>`,
			testRelNS, testPresentationNS),
		"ppt/_rels/presentation.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId1" Type="%s" Target="slides/slide1.xml"/></Relationships>`,
			testPackageRelNS, testRelType("slide")),
		"ppt/slides/slide1.xml": fmt.Sprintf(`<p:sld xmlns:a="%s" xmlns:p="%s"><a:p><a:r><a:t>SlideBody 合同编号</a:t></a:r></a:p></p:sld>`,
			testDrawingNS, testPresentationNS),
		"ppt/slides/_rels/slide1.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId9" Type="%s" Target="../../speaker/notes.xml"/></Relationships>`,
			testPackageRelNS, testRelType("notesSlide")),
		"speaker/notes.xml": fmt.Sprintf(`<p:notes xmlns:a="%s" xmlns:p="%s"><a:p><a:r><a:t>演讲备注 Notes Custom Location</a:t></a:r></a:p></p:notes>`,
			testDrawingNS, testPresentationNS),
	})
	result := Extract(context.Background(), "notes.pptx", "", bytes.NewReader(payload), DefaultExtractionLimits)
	if result.Status != ExtractionIndexed ||
		result.Text != "SlideBody 合同编号\n演讲备注 Notes Custom Location" ||
		result.ErrorCode != nil {
		t.Fatalf("custom notes location = %#v", result)
	}
}

// TestOOXMLRejectsDuplicateZipEntriesAndEscapingNames pins the archive hard
// boundary: duplicate entries and normalized path escapes invalidate the
// whole package instead of being silently merged or skipped.
func TestOOXMLRejectsDuplicateZipEntriesAndEscapingNames(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range []string{"word/document.xml", "word/document.xml"} {
		part, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(`<w:document/>`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	assertExtractionCode(
		t,
		Extract(context.Background(), "dup.docx", "", bytes.NewReader(archive.Bytes()), DefaultExtractionLimits),
		ExtractionFailed, "extract.ooxml_invalid",
	)

	escape := ooxml(t, "word/../../secret.xml", `<w:document/>`)
	assertExtractionCode(
		t,
		Extract(context.Background(), "escape.docx", "", bytes.NewReader(escape), DefaultExtractionLimits),
		ExtractionFailed, "extract.ooxml_invalid",
	)
}

// TestOOXMLRejectsForeignNamespaceText proves element matching is
// namespace-aware: t/p/c elements in a foreign namespace are never treated
// as visible body text.
func TestOOXMLRejectsForeignNamespaceText(t *testing.T) {
	docx := fmt.Sprintf(`<w:document xmlns:w="%s" xmlns:x="%s"><w:body>
	  <w:p><w:r><w:t>正文可见</w:t></w:r><x:r><x:t>FOREIGN-NAMESPACE-TEXT</x:t></x:r></w:p>
	</w:body></w:document>`, testWordNS, testForeignNS)
	result := Extract(
		context.Background(), "foreign.docx", "",
		bytes.NewReader(ooxml(t, "word/document.xml", docx)), DefaultExtractionLimits,
	)
	if result.Status != ExtractionIndexed || result.Text != "正文可见" {
		t.Fatalf("foreign-namespace docx = %#v", result)
	}

	xlsx := ooxmlMany(t, map[string]string{
		"xl/workbook.xml": fmt.Sprintf(`<workbook xmlns="%s" xmlns:r="%s"><sheets>
		  <sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			testSpreadsheetNS, testRelNS),
		"xl/_rels/workbook.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId1" Type="%s" Target="worksheets/sheet1.xml"/></Relationships>`,
			testPackageRelNS, testRelType("worksheet")),
		"xl/worksheets/sheet1.xml": fmt.Sprintf(`<worksheet xmlns="%s" xmlns:x="%s"><sheetData>
		  <row r="1"><c r="A1"><v>42</v></c><x:c r="B1"><x:v>FOREIGN-CELL</x:v></x:c></row>
		</sheetData></worksheet>`, testSpreadsheetNS, testForeignNS),
	})
	xlsxResult := Extract(context.Background(), "foreign.xlsx", "", bytes.NewReader(xlsx), DefaultExtractionLimits)
	if xlsxResult.Status != ExtractionIndexed || xlsxResult.Text != "42" {
		t.Fatalf("foreign-namespace xlsx = %#v", xlsxResult)
	}
}

// TestOOXMLRejectsAmbiguousRootOfficeDocument proves a root rels naming
// more than one officeDocument target fails closed instead of picking one
// arbitrarily.
func TestOOXMLRejectsAmbiguousRootOfficeDocument(t *testing.T) {
	payload := ooxmlMany(t, map[string]string{
		"_rels/.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId1" Type="%s" Target="word/document.xml"/>
		  <Relationship Id="rId2" Type="%s" Target="word/other.xml"/>
		</Relationships>`, testPackageRelNS,
			testRelType("officeDocument"), testRelType("officeDocument")),
		"word/document.xml": fmt.Sprintf(`<w:document xmlns:w="%s"><w:body><w:p><w:r><w:t>One</w:t></w:r></w:p></w:body></w:document>`, testWordNS),
		"word/other.xml":    fmt.Sprintf(`<w:document xmlns:w="%s"><w:body><w:p><w:r><w:t>Two</w:t></w:r></w:p></w:body></w:document>`, testWordNS),
	})
	assertExtractionCode(
		t,
		Extract(context.Background(), "ambiguous.docx", "", bytes.NewReader(payload), DefaultExtractionLimits),
		ExtractionFailed, "extract.ooxml_part_failed",
	)
}

// TestOOXMLDamagedPackagesAndReferencesStayHonest pins every damaged-input
// contract: corrupt relationship wiring fails closed, a single unreadable
// referenced part stays indexed with extract.ooxml_partial, duplicate
// references dedupe, and optional parts (styles) degrade without inventing
// coverage.
func TestOOXMLDamagedPackagesAndReferencesStayHonest(t *testing.T) {
	docNS := fmt.Sprintf(`xmlns:w="%s" xmlns:r="%s"`, testWordNS, testRelNS)
	helloDoc := fmt.Sprintf(`<w:document %s><w:body><w:p><w:r><w:t>正文可见</w:t></w:r></w:p></w:body></w:document>`, docNS)
	withHeader := func(reference string) string {
		return fmt.Sprintf(`<w:document %s><w:body>
		  <w:p><w:r><w:t>正文可见</w:t></w:r></w:p>
		  <w:sectPr>%s</w:sectPr>
		</w:body></w:document>`, docNS, reference)
	}
	headerReference := `<w:headerReference w:type="default" r:id="rIdH1"/>`
	headerPart := fmt.Sprintf(`<w:hdr xmlns:w="%s"><w:p><w:r><w:t>页眉可见</w:t></w:r></w:p></w:hdr>`, testWordNS)
	relsFor := func(body string) string {
		return fmt.Sprintf(`<Relationships xmlns="%s">%s</Relationships>`, testPackageRelNS, body)
	}
	presentationDeck := func(target string) map[string]string {
		return map[string]string{
			"ppt/presentation.xml": fmt.Sprintf(`<p:presentation xmlns:r="%s" xmlns:p="%s"><p:sldIdLst>
			  <p:sldId id="256" r:id="rId1"/></p:sldIdLst></p:presentation>`, testRelNS, testPresentationNS),
			"ppt/_rels/presentation.xml.rels": relsFor(fmt.Sprintf(
				`<Relationship Id="rId1" Type="%s" Target="%s"/>`, testRelType("slide"), target)),
			"ppt/slides/slide1.xml": fmt.Sprintf(`<p:sld xmlns:a="%s" xmlns:p="%s"><a:p><a:r><a:t>SlideBody</a:t></a:r></a:p></p:sld>`,
				testDrawingNS, testPresentationNS),
		}
	}
	xlsxBook := func(relsBody string, sheetBody string, extras map[string]string) map[string]string {
		entries := map[string]string{
			"xl/workbook.xml": fmt.Sprintf(`<workbook xmlns="%s" xmlns:r="%s"><sheets>
			  <sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>`, testSpreadsheetNS, testRelNS),
			"xl/_rels/workbook.xml.rels": relsFor(relsBody),
			"xl/worksheets/sheet1.xml": fmt.Sprintf(`<worksheet xmlns="%s"><sheetData>%s</sheetData></worksheet>`,
				testSpreadsheetNS, sheetBody),
		}
		for name, content := range extras {
			entries[name] = content
		}
		return entries
	}
	tests := []struct {
		name       string
		fileName   string
		entries    map[string]string
		wantStatus ExtractionStatus
		wantCode   string
		wantText   string
	}{
		{
			"docx corrupt root rels", "a.docx",
			map[string]string{"_rels/.rels": `<Relationships xmlns="` + testPackageRelNS, "word/document.xml": helloDoc},
			ExtractionFailed, "extract.ooxml_part_failed", "",
		},
		{
			"docx escaping main target", "a.docx",
			map[string]string{
				"_rels/.rels": relsFor(fmt.Sprintf(
					`<Relationship Id="rId1" Type="%s" Target="../evil.xml"/>`, testRelType("officeDocument"))),
				"word/document.xml": helloDoc,
			},
			ExtractionFailed, "extract.ooxml_part_failed", "",
		},
		{
			"docx header reference without rels part", "a.docx",
			map[string]string{"word/document.xml": withHeader(headerReference)},
			ExtractionIndexed, ooxmlPartialCode, "正文可见",
		},
		{
			"docx header reference id missing", "a.docx",
			map[string]string{
				"word/document.xml": withHeader(headerReference),
				"word/_rels/document.xml.rels": relsFor(fmt.Sprintf(
					`<Relationship Id="rIdOther" Type="%s" Target="header1.xml"/>`, testRelType("header"))),
				"word/header1.xml": headerPart,
			},
			ExtractionIndexed, ooxmlPartialCode, "正文可见",
		},
		{
			"docx duplicate header references dedupe", "a.docx",
			map[string]string{
				"word/document.xml": withHeader(headerReference + headerReference),
				"word/_rels/document.xml.rels": relsFor(fmt.Sprintf(
					`<Relationship Id="rIdH1" Type="%s" Target="header1.xml"/>`, testRelType("header"))),
				"word/header1.xml": headerPart,
			},
			ExtractionIndexed, "", "正文可见\n页眉可见",
		},
		{
			"docx corrupt referenced header part", "a.docx",
			map[string]string{
				"word/document.xml": withHeader(headerReference),
				"word/_rels/document.xml.rels": relsFor(fmt.Sprintf(
					`<Relationship Id="rIdH1" Type="%s" Target="header1.xml"/>`, testRelType("header"))),
				"word/header1.xml": `<w:hdr xmlns:w="`,
			},
			ExtractionFailed, "extract.ooxml_part_failed", "",
		},
		{
			"pptx missing presentation rels", "a.pptx",
			map[string]string{
				"ppt/presentation.xml": fmt.Sprintf(`<p:presentation xmlns:r="%s" xmlns:p="%s"><p:sldIdLst>
			  <p:sldId id="256" r:id="rId1"/></p:sldIdLst></p:presentation>`, testRelNS, testPresentationNS),
				"ppt/slides/slide1.xml": fmt.Sprintf(`<p:sld xmlns:a="%s" xmlns:p="%s"><a:p><a:r><a:t>SlideBody</a:t></a:r></a:p></p:sld>`,
					testDrawingNS, testPresentationNS),
			},
			ExtractionFailed, "extract.ooxml_part_failed", "",
		},
		{
			"pptx empty slide list", "a.pptx",
			map[string]string{
				"ppt/presentation.xml":            fmt.Sprintf(`<p:presentation xmlns:r="%s" xmlns:p="%s"><p:sldIdLst/></p:presentation>`, testRelNS, testPresentationNS),
				"ppt/_rels/presentation.xml.rels": relsFor(""),
			},
			ExtractionFailed, "extract.ooxml_part_failed", "",
		},
		{
			"pptx slide target escapes", "a.pptx",
			presentationDeck("../../evil.xml"),
			ExtractionIndexed, ooxmlPartialCode, "",
		},
		{
			"pptx notes target part missing", "a.pptx",
			func() map[string]string {
				entries := presentationDeck("slides/slide1.xml")
				entries["ppt/slides/_rels/slide1.xml.rels"] = relsFor(fmt.Sprintf(
					`<Relationship Id="rId9" Type="%s" Target="notesSlides/absent.xml"/>`, testRelType("notesSlide")))
				return entries
			}(),
			ExtractionIndexed, ooxmlPartialCode, "SlideBody",
		},
		{
			"pptx corrupt slide rels", "a.pptx",
			func() map[string]string {
				entries := presentationDeck("slides/slide1.xml")
				entries["ppt/slides/_rels/slide1.xml.rels"] = `<Relationships xmlns="broken`
				return entries
			}(),
			ExtractionFailed, "extract.ooxml_part_failed", "",
		},
		{
			"xlsx missing workbook rels", "a.xlsx",
			map[string]string{
				"xl/workbook.xml": fmt.Sprintf(`<workbook xmlns="%s" xmlns:r="%s"><sheets>
			  <sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>`, testSpreadsheetNS, testRelNS),
			},
			ExtractionFailed, "extract.ooxml_part_failed", "",
		},
		{
			"xlsx sheet relationship id missing", "a.xlsx",
			xlsxBook(fmt.Sprintf(`<Relationship Id="rIdOther" Type="%s" Target="worksheets/sheet1.xml"/>`, testRelType("worksheet")),
				`<row r="1"><c r="A1"><v>1</v></c></row>`, nil),
			ExtractionIndexed, ooxmlPartialCode, "",
		},
		{
			"xlsx shared strings part missing", "a.xlsx",
			xlsxBook(fmt.Sprintf(
				`<Relationship Id="rId1" Type="%s" Target="worksheets/sheet1.xml"/>
			 <Relationship Id="rId2" Type="%s" Target="strings/absent.xml"/>`,
				testRelType("worksheet"), testRelType("sharedStrings")),
				`<row r="1"><c r="A1"><v>42</v></c></row>`, nil),
			ExtractionIndexed, ooxmlPartialCode, "42",
		},
		{
			"xlsx styles part missing is optional", "a.xlsx",
			xlsxBook(fmt.Sprintf(
				`<Relationship Id="rId1" Type="%s" Target="worksheets/sheet1.xml"/>
			 <Relationship Id="rId3" Type="%s" Target="styles/absent.xml"/>`,
				testRelType("worksheet"), testRelType("styles")),
				`<row r="1"><c r="A1"><v>42</v></c></row>`, nil),
			ExtractionIndexed, "", "42",
		},
		{
			"xlsx corrupt styles part", "a.xlsx",
			xlsxBook(fmt.Sprintf(
				`<Relationship Id="rId1" Type="%s" Target="worksheets/sheet1.xml"/>
			 <Relationship Id="rId3" Type="%s" Target="styles.xml"/>`,
				testRelType("worksheet"), testRelType("styles")),
				`<row r="1"><c r="A1"><v>42</v></c></row>`,
				map[string]string{"xl/styles.xml": `<styleSheet xmlns="` + testSpreadsheetNS}),
			ExtractionFailed, "extract.ooxml_part_failed", "",
		},
		{
			"xlsx non-numeric shared index stays honest", "a.xlsx",
			xlsxBook(fmt.Sprintf(
				`<Relationship Id="rId1" Type="%s" Target="worksheets/sheet1.xml"/>
			 <Relationship Id="rId2" Type="%s" Target="strings.xml"/>`,
				testRelType("worksheet"), testRelType("sharedStrings")),
				`<row r="1"><c r="A1" t="s"><v>not-a-number</v></c><c r="B1" t="b"><v>2</v></c></row>`,
				map[string]string{"xl/strings.xml": fmt.Sprintf(`<sst xmlns="%s"><si><t>可见</t></si></sst>`, testSpreadsheetNS)}),
			ExtractionIndexed, ooxmlPartialCode, "",
		},
		{
			"xlsx invalid style attribute falls back to plain value", "a.xlsx",
			xlsxBook(fmt.Sprintf(`<Relationship Id="rId1" Type="%s" Target="worksheets/sheet1.xml"/>`, testRelType("worksheet")),
				`<row r="1"><c r="A1" s="not-a-number"><v>42</v></c></row>`, nil),
			ExtractionIndexed, "", "42",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := ooxmlMany(t, test.entries)
			result := Extract(context.Background(), test.fileName, "", bytes.NewReader(payload), DefaultExtractionLimits)
			if result.Status != test.wantStatus || result.Text != test.wantText {
				t.Fatalf("Extract() = %#v, want %s/%q", result, test.wantStatus, test.wantText)
			}
			if test.wantCode == "" {
				if result.ErrorCode != nil {
					t.Fatalf("Extract() = %#v, want no error code", result)
				}
				return
			}
			if result.ErrorCode == nil || *result.ErrorCode != test.wantCode {
				t.Fatalf("Extract() = %#v, want code %q", result, test.wantCode)
			}
		})
	}
}

// TestOOXMLBudgetTruncatesStreamedTextExactly proves the text budget binds
// while parts stream in: DOCX paragraphs, XLSX cells, and PPTX runs all cut
// exactly at the code-point limit with extract.text_limit.
func TestOOXMLBudgetTruncatesStreamedTextExactly(t *testing.T) {
	limits := DefaultExtractionLimits
	limits.MaximumTextCodePoints = 8
	docx := ooxml(t, "word/document.xml", fmt.Sprintf(
		`<w:document xmlns:w="%s"><w:body><w:p><w:r><w:t>合同编号</w:t></w:r></w:p><w:p><w:r><w:t>合同编号</w:t></w:r></w:p><w:p><w:r><w:t>合同编号</w:t></w:r></w:p></w:body></w:document>`,
		testWordNS))
	docxResult := Extract(context.Background(), "b.docx", "", bytes.NewReader(docx), limits)
	if docxResult.Status != ExtractionTruncated || docxResult.Text != "合同编号\n合同编" ||
		docxResult.ErrorCode == nil || *docxResult.ErrorCode != "extract.text_limit" {
		t.Fatalf("docx budget = %#v", docxResult)
	}

	xlsx := ooxmlMany(t, map[string]string{
		"xl/workbook.xml": fmt.Sprintf(`<workbook xmlns="%s" xmlns:r="%s"><sheets>
		  <sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>`, testSpreadsheetNS, testRelNS),
		"xl/_rels/workbook.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId1" Type="%s" Target="worksheets/sheet1.xml"/></Relationships>`,
			testPackageRelNS, testRelType("worksheet")),
		"xl/worksheets/sheet1.xml": fmt.Sprintf(`<worksheet xmlns="%s"><sheetData>
		  <row r="1"><c r="A1"><v>12345</v></c><c r="B1"><v>67890</v></c></row>
		</sheetData></worksheet>`, testSpreadsheetNS),
	})
	xlsxResult := Extract(context.Background(), "b.xlsx", "", bytes.NewReader(xlsx), limits)
	if xlsxResult.Status != ExtractionTruncated || xlsxResult.Text != "12345\n67" {
		t.Fatalf("xlsx budget = %#v", xlsxResult)
	}

	pptx := ooxmlMany(t, map[string]string{
		"ppt/presentation.xml": fmt.Sprintf(`<p:presentation xmlns:r="%s" xmlns:p="%s"><p:sldIdLst>
		  <p:sldId id="256" r:id="rId1"/></p:sldIdLst></p:presentation>`, testRelNS, testPresentationNS),
		"ppt/_rels/presentation.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId1" Type="%s" Target="slides/slide1.xml"/></Relationships>`,
			testPackageRelNS, testRelType("slide")),
		"ppt/slides/slide1.xml": fmt.Sprintf(`<p:sld xmlns:a="%s" xmlns:p="%s"><a:p><a:r><a:t>abcdefghij</a:t></a:r></a:p></p:sld>`,
			testDrawingNS, testPresentationNS),
	})
	pptxResult := Extract(context.Background(), "b.pptx", "", bytes.NewReader(pptx), limits)
	if pptxResult.Status != ExtractionTruncated || pptxResult.Text != "abcdefgh" {
		t.Fatalf("pptx budget = %#v", pptxResult)
	}
}

// TestXLSXCustomDateFormatQuotedLiteralsStayClassified covers custom numFmt
// classification including quoted literal segments.
func TestXLSXCustomDateFormatQuotedLiteralsStayClassified(t *testing.T) {
	payload := ooxmlMany(t, map[string]string{
		"xl/workbook.xml": fmt.Sprintf(`<workbook xmlns="%s" xmlns:r="%s"><sheets>
		  <sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>`, testSpreadsheetNS, testRelNS),
		"xl/_rels/workbook.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId1" Type="%s" Target="worksheets/sheet1.xml"/>
		  <Relationship Id="rId3" Type="%s" Target="styles.xml"/></Relationships>`,
			testPackageRelNS, testRelType("worksheet"), testRelType("styles")),
		"xl/styles.xml": fmt.Sprintf(`<styleSheet xmlns="%s">
		  <numFmts>
		    <numFmt numFmtId="164" formatCode="&quot;Q&quot;yyyy&quot;年&quot;mm&quot;月&quot;"/>
		    <numFmt numFmtId="165" formatCode="&quot;总量&quot;0.0m"/>
		  </numFmts>
		  <cellXfs><xf numFmtId="164"/><xf numFmtId="165"/><xf numFmtId="0"/></cellXfs>
		</styleSheet>`, testSpreadsheetNS),
		"xl/worksheets/sheet1.xml": fmt.Sprintf(`<worksheet xmlns="%s"><sheetData>
		  <row r="1">
		    <c r="A1" s="0"><v>46023</v></c>
		    <c r="B1" s="1"><v>9.5</v></c>
		    <c r="C1" s="2"><v>100</v></c>
		  </row>
		</sheetData></worksheet>`, testSpreadsheetNS),
	})
	result := Extract(context.Background(), "dates.xlsx", "", bytes.NewReader(payload), DefaultExtractionLimits)
	if result.Status != ExtractionIndexed || result.Text != "46023\n9.5\n100" ||
		result.ErrorCode == nil || *result.ErrorCode != ooxmlPartialCode {
		t.Fatalf("quoted-literal date formats = %#v", result)
	}
}

// TestPPTXDrawingMLParagraphsSeparateWithinASlide is the positive control
// for the paragraph namespace: real slides and notes use DrawingML a:p
// inside p:txBody, paragraphs never merge into one word, and runs inside
// one paragraph join into the same searchable word.
func TestPPTXDrawingMLParagraphsSeparateWithinASlide(t *testing.T) {
	payload := ooxmlMany(t, map[string]string{
		"ppt/presentation.xml": fmt.Sprintf(`<p:presentation xmlns:r="%s" xmlns:p="%s"><p:sldIdLst>
		  <p:sldId id="256" r:id="rId1"/></p:sldIdLst></p:presentation>`, testRelNS, testPresentationNS),
		"ppt/_rels/presentation.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId1" Type="%s" Target="slides/slide1.xml"/></Relationships>`,
			testPackageRelNS, testRelType("slide")),
		"ppt/slides/slide1.xml": fmt.Sprintf(`<p:sld xmlns:a="%s" xmlns:p="%s"><p:cSld><p:spTree><p:sp><p:txBody>
		  <a:p><a:r><a:t>合同</a:t></a:r><a:r><a:rPr b="1"/><a:t>编号</a:t></a:r></a:p>
		  <a:p><a:r><a:t>离线数据</a:t></a:r></a:p>
		  <a:p><a:r><a:t>Platform</a:t></a:r></a:p>
		</p:txBody></p:sp></p:spTree></p:cSld></p:sld>`,
			testDrawingNS, testPresentationNS),
	})
	result := Extract(context.Background(), "paragraphs.pptx", "", bytes.NewReader(payload), DefaultExtractionLimits)
	if result.Status != ExtractionIndexed || result.Text != "合同编号\n离线数据\nPlatform" ||
		result.ErrorCode != nil {
		t.Fatalf("DrawingML paragraphs = %#v", result)
	}
}

// TestOOXMLGiantRunsKeepTruncationEvidence proves a single huge XML
// CharData cannot silently drop its tail: the pending-line rune cap keeps
// one rune beyond the budget, so the result is truncated with the exact
// prefix instead of a clean-looking indexed text.
func TestOOXMLGiantRunsKeepTruncationEvidence(t *testing.T) {
	limits := DefaultExtractionLimits
	limits.MaximumTextCodePoints = 5
	docx := ooxml(t, "word/document.xml", fmt.Sprintf(
		`<w:document xmlns:w="%s"><w:body><w:p><w:r><w:t>0123456789abcdefghij</w:t></w:r></w:p></w:body></w:document>`,
		testWordNS))
	docxResult := Extract(context.Background(), "giant.docx", "", bytes.NewReader(docx), limits)
	if docxResult.Status != ExtractionTruncated || docxResult.Text != "01234" ||
		docxResult.ErrorCode == nil || *docxResult.ErrorCode != "extract.text_limit" {
		t.Fatalf("giant docx run = %#v", docxResult)
	}

	xlsx := ooxmlMany(t, map[string]string{
		"xl/workbook.xml": fmt.Sprintf(`<workbook xmlns="%s" xmlns:r="%s"><sheets>
		  <sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>`, testSpreadsheetNS, testRelNS),
		"xl/_rels/workbook.xml.rels": fmt.Sprintf(`<Relationships xmlns="%s">
		  <Relationship Id="rId1" Type="%s" Target="worksheets/sheet1.xml"/></Relationships>`,
			testPackageRelNS, testRelType("worksheet")),
		"xl/worksheets/sheet1.xml": fmt.Sprintf(`<worksheet xmlns="%s"><sheetData>
		  <row r="1"><c r="A1"><v>9876543210</v></c></row>
		</sheetData></worksheet>`, testSpreadsheetNS),
	})
	xlsxResult := Extract(context.Background(), "giant.xlsx", "", bytes.NewReader(xlsx), limits)
	if xlsxResult.Status != ExtractionTruncated || xlsxResult.Text != "98765" {
		t.Fatalf("giant xlsx cell = %#v", xlsxResult)
	}
}

// TestPDFUnknownDecodingIsNotReportedAsNoTextLayer pins the honest coverage
// classification: fully undecodable text evidence is unsupported (never
// noTextLayer), mixed documents stay indexed with the partial-decode error
// code, and true image-only pages keep noTextLayer.
func TestPDFUnknownDecodingIsNotReportedAsNoTextLayer(t *testing.T) {
	undecodable := Extract(
		context.Background(), "cid.pdf", "application/pdf",
		bytes.NewReader(pdfQualificationDocument(t, []byte(`BT /F1 12 Tf <8081fe> Tj ET`), false)),
		DefaultExtractionLimits,
	)
	assertExtractionCode(t, undecodable, ExtractionUnsupported, "extract.unsupported")
	if undecodable.Text != "" {
		t.Fatalf("undecodable pdf leaked text: %q", undecodable.Text)
	}

	controlOnly := Extract(
		context.Background(), "control.pdf", "application/pdf",
		bytes.NewReader(pdfQualificationDocument(t, []byte(`BT /F1 12 Tf <0008000f> Tj ET`), false)),
		DefaultExtractionLimits,
	)
	assertExtractionCode(t, controlOnly, ExtractionUnsupported, "extract.unsupported")

	mixed := Extract(
		context.Background(), "mixed.pdf", "application/pdf",
		bytes.NewReader(pdfQualificationDocument(t, []byte(`BT /F1 12 Tf (Visible 合同文本) Tj <8081fe> Tj ET`), false)),
		DefaultExtractionLimits,
	)
	if mixed.Status != ExtractionIndexed || mixed.ErrorCode == nil ||
		*mixed.ErrorCode != "extract.pdf_partial_decode" ||
		!strings.Contains(mixed.Text, "Visible 合同文本") {
		t.Fatalf("mixed pdf = %#v", mixed)
	}

	scan := Extract(
		context.Background(), "scan.pdf", "application/pdf",
		bytes.NewReader(pdfQualificationDocument(t, []byte(`q 1 0 0 1 0 0 cm Q`), false)),
		DefaultExtractionLimits,
	)
	assertExtractionCode(t, scan, ExtractionNoTextLayer, "extract.pdf_no_text")
}

// TestVisibleTextFlowsFromExtractionThroughIndexToQuery covers the AC5
// wiring semantics at the engine boundary: current/history scope, deleted
// tombstones, zero-hit versus invalid queries, and extraction statuses that
// never turn queries into errors.
func TestVisibleTextFlowsFromExtractionThroughIndexToQuery(t *testing.T) {
	splitPayload, _ := loadOOXMLPackageFixture(t, "docx-contract-split")
	extraction := Extract(
		context.Background(), "contract-split.docx", "", bytes.NewReader(splitPayload),
		DefaultExtractionLimits,
	)
	if extraction.Status != ExtractionIndexed {
		t.Fatalf("fixture extraction = %#v", extraction)
	}
	engine := testEngine(t)
	upsert(t, engine, SourceDocument{
		Kind: "file", CanonicalID: "doc-contract", SourceRevision: "rev-1",
		Title: "合同草稿", Body: "已删除草稿 legacy visible body", RevisionTime: "2026-08-12T00:00:00Z",
		Status: "indexed", Current: false,
		Metadata: []contracts.SearchMetadataItem{}, OpenTarget: contracts.SearchOpenTarget{Kind: "file"},
	})
	upsert(t, engine, SourceDocument{
		Kind: "file", CanonicalID: "doc-contract", SourceRevision: "rev-2",
		Title: "合同正文", Body: extraction.Text, RevisionTime: "2026-09-12T00:00:00Z",
		Status: "indexed", Current: true,
		Metadata: []contracts.SearchMetadataItem{}, OpenTarget: contracts.SearchOpenTarget{Kind: "file"},
	})
	upsert(t, engine, SourceDocument{
		Kind: "file", CanonicalID: "doc-paragraphs", SourceRevision: "rev-1",
		Title: "段落边界", Body: "合同\n编号", RevisionTime: "2026-09-12T00:00:00Z",
		Status: "indexed", Current: true,
		Metadata: []contracts.SearchMetadataItem{}, OpenTarget: contracts.SearchOpenTarget{Kind: "file"},
	})
	upsert(t, engine, SourceDocument{
		Kind: "file", CanonicalID: "doc-scan", SourceRevision: "rev-1",
		Title: "扫描件", Body: "", RevisionTime: "2026-09-12T00:00:00Z",
		Status: string(ExtractionNoTextLayer), Current: true,
		Metadata: []contracts.SearchMetadataItem{}, OpenTarget: contracts.SearchOpenTarget{Kind: "file"},
	})

	current := request("合同编号")
	result := query(t, engine, current)
	if len(result.Hits) != 1 || result.Hits[0].CanonicalId != "doc-contract" ||
		result.Hits[0].SourceRevision != "rev-2" {
		t.Fatalf("contiguous CJK hits = %#v", result.Hits)
	}
	if hit := query(t, engine, request("编号")); len(hit.Hits) != 2 {
		t.Fatalf("short CJK hits = %#v", hit.Hits)
	}

	history := request("已删除草稿")
	history.Scope = "history"
	if stale := query(t, engine, history); len(stale.Hits) != 1 || stale.Hits[0].SourceRevision != "rev-1" {
		t.Fatalf("history scope hits = %#v", stale.Hits)
	}
	currentOnly := request("已删除草稿")
	if fresh := query(t, engine, currentOnly); len(fresh.Hits) != 0 {
		t.Fatalf("current scope exposed stale body: %#v", fresh.Hits)
	}

	if err := engine.Tombstone(
		context.Background(), "file", "doc-paragraphs", "rev-2", "2026-09-13T00:00:00Z",
	); err != nil {
		t.Fatal(err)
	}
	if deleted := query(t, engine, request("段落边界")); len(deleted.Hits) != 0 {
		t.Fatalf("tombstoned title stayed searchable: %#v", deleted.Hits)
	}

	missing := query(t, engine, request("NoSuchVisibleToken"))
	if len(missing.Hits) != 0 || missing.NextCursor != nil {
		t.Fatalf("zero-hit query = %#v", missing)
	}
	if _, err := engine.Query(context.Background(), request("   ")); err == nil {
		t.Fatal("blank query was accepted")
	}
	if unsupported := query(t, engine, request("扫描件")); len(unsupported.Hits) != 1 {
		t.Fatalf("unsupported-status document hid its title: %#v", unsupported.Hits)
	}
}

// BenchmarkExtractDOCXVisibleTextAtTextLimit measures extraction cost at the
// full default text budget (2,000,000 code points): a ~21 MB story part is
// parsed once and truncated exactly at the rune limit.
func BenchmarkExtractDOCXVisibleTextAtTextLimit(b *testing.B) {
	var story strings.Builder
	story.WriteString(fmt.Sprintf(`<w:document xmlns:w="%s"><w:body>`, testWordNS))
	paragraph := `<w:p><w:r><w:t xml:space="preserve">合同编号数据workbench</w:t></w:r></w:p>`
	paragraphs := 2_000_000/utf8.RuneCountInString("合同编号数据workbench") + 10
	for index := 0; index < paragraphs; index++ {
		story.WriteString(paragraph)
	}
	story.WriteString(`</w:body></w:document>`)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	part, err := writer.Create("word/document.xml")
	if err != nil {
		b.Fatal(err)
	}
	if _, err := part.Write([]byte(story.String())); err != nil {
		b.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		b.Fatal(err)
	}
	payload := archive.Bytes()
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		result := Extract(
			context.Background(), "budget.docx", "", bytes.NewReader(payload),
			DefaultExtractionLimits,
		)
		if result.Status != ExtractionTruncated ||
			utf8.RuneCountInString(result.Text) != DefaultExtractionLimits.MaximumTextCodePoints {
			b.Fatalf("budget extraction = %s runes=%d", result.Status, utf8.RuneCountInString(result.Text))
		}
	}
}

// Interrupt every observed read/XML boundary on the small legal packages.
// This pins cancellation and deadline classification without timing races.
func TestOOXMLOrderedExtractionInterruptionsKeepTheirStatus(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		extract func(context.Context, *ooxmlPackage, ExtractionLimits) ExtractionResult
	}{
		{"xlsx-ledger", extractXLSX},
		{"pptx-slides-reordered", extractPPTX},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			payload, _ := loadOOXMLPackageFixture(t, fixture.name)
			pkg, failure := collectOOXMLPackage(context.Background(), payload, DefaultExtractionLimits)
			if pkg == nil {
				t.Fatalf("invalid legal fixture: %#v", failure)
			}
			probe := &ooxmlInterruptionContext{Context: context.Background(), done: make(chan struct{})}
			if result := fixture.extract(probe, pkg, DefaultExtractionLimits); result.Status != ExtractionIndexed {
				t.Fatalf("uninterrupted fixture: %#v", result)
			}
			for _, interruption := range []struct {
				err    error
				status ExtractionStatus
				code   string
			}{
				{context.Canceled, ExtractionCancelled, "extract.cancelled"},
				{context.DeadlineExceeded, ExtractionResourceLimited, "extract.timeout"},
			} {
				for boundary := 1; boundary <= probe.checks; boundary++ {
					ctx := &ooxmlInterruptionContext{Context: context.Background(), done: make(chan struct{}), at: boundary, failure: interruption.err}
					result := fixture.extract(ctx, pkg, DefaultExtractionLimits)
					if result.Status != interruption.status || result.ErrorCode == nil || *result.ErrorCode != interruption.code {
						t.Fatalf("%v at boundary %d: %#v", interruption.err, boundary, result)
					}
				}
			}
		})
	}
}

type ooxmlInterruptionContext struct {
	context.Context
	done        chan struct{}
	checks, at  int
	failure     error
	interrupted bool
}

func (ctx *ooxmlInterruptionContext) Done() <-chan struct{} { return ctx.done }
func (ctx *ooxmlInterruptionContext) Err() error {
	ctx.checks++
	if ctx.at > 0 && ctx.checks >= ctx.at {
		if !ctx.interrupted {
			close(ctx.done)
			ctx.interrupted = true
		}
		return ctx.failure
	}
	return nil
}
