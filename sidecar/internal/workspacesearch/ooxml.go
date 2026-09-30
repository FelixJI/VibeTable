package workspacesearch

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// OOXML visible-text support matrix (issue #410).
//
// Extraction models "currently visible body text" per format. Parts are
// located only through package relationships (root officeDocument, then the
// per-part rels); unreferenced parts are never indexed. Results stay
// document-level: no page, sheet/cell, or slide coordinates are invented.
//
//	Region                        | DOCX   | XLSX              | PPTX
//	------------------------------+--------+-------------------+-------------------
//	 main story                   | covered| n/a (per cell)    | covered (rels order)
//	 referenced headers/footers   | covered| headerFooter not  | n/a
//	                              |        | covered           |
//	 referenced notes slides      | n/a    | n/a               | covered (via slide rels)
//	 text boxes inside main story | covered (walked with the story) | n/a | n/a
//	 tracked insertions           | covered| n/a               | n/a
//	 tracked deletions            | excluded (not current text) | n/a | n/a
//	 field instructions           | excluded; cached results kept | n/a | cached a:fld kept
//	 typed cell values            | n/a    | covered           | n/a
//	 footnote/endnote/comment     | indexed + extract.ooxml_partial when referenced (regions not covered)
//
// Honest boundaries, each surfaced as indexed text plus
// extract.ooxml_partial instead of silent loss: a shared-string cell whose
// index is missing or out of range, a formula without a cached <v>, and a
// date-formatted cell (the raw serial number is indexed; numFmt rendering
// is not performed). Formulas are never recalculated. mc:Fallback
// duplicates are skipped. Damaged relationship wiring fails closed
// (extract.ooxml_part_failed). PDF and non-OOXML formats keep their own
// providers.

const (
	xmlNamespaceXML        = "http://www.w3.org/XML/1998/namespace"
	ooxmlRelationshipNS    = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	nsPackageRelationships = "http://schemas.openxmlformats.org/package/2006/relationships"
	nsWordprocessingML     = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsSpreadsheetML        = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	nsPresentationML       = "http://schemas.openxmlformats.org/presentationml/2006/main"
	nsDrawingML            = "http://schemas.openxmlformats.org/drawingml/2006/main"
	nsMarkupCompatibility  = "http://schemas.openxmlformats.org/markup-compatibility/2006"
	ooxmlPartialCode       = "extract.ooxml_partial"
)

var (
	errOOXMLPartUnavailable = errors.New("ooxml part unavailable")
	errOOXMLPartUnreadable  = errors.New("ooxml part unreadable")
	errOOXMLRelUnresolvable = errors.New("ooxml relationship unresolvable")
	errOOXMLAmbiguousMain   = errors.New("ooxml ambiguous main part")
)

// ooxmlPackage enforces archive budgets once and resolves parts strictly by
// package relationships afterwards.
type ooxmlPackage struct {
	entries map[string]*zip.File
}

// normalizeOOXMLPartName canonicalizes a zip entry name for part lookup.
// ok=false marks package escapes (absolute paths, ../ traversal).
func normalizeOOXMLPartName(name string) (string, bool) {
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if clean == "" || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") ||
		strings.Contains(clean, "/../") {
		return "", false
	}
	return clean, true
}

// collectOOXMLPackage enforces the archive budgets over every entry (not
// only text parts), rejects duplicate or escaping entry names, and indexes
// the central directory by normalized part name.
func collectOOXMLPackage(
	ctx context.Context,
	payload []byte,
	limits ExtractionLimits,
) (*ooxmlPackage, ExtractionResult) {
	archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return nil, extractionError(ExtractionFailed, "extract.ooxml_invalid")
	}
	if len(archive.File) > limits.MaximumZIPEntries {
		return nil, extractionError(ExtractionResourceLimited, "extract.zip_entries_limit")
	}
	entries := make(map[string]*zip.File, len(archive.File))
	var total int64
	for _, file := range archive.File {
		if err := ctx.Err(); err != nil {
			return nil, extractionContextError(ctx)
		}
		if file.UncompressedSize64 > uint64(limits.MaximumPartBytes) {
			return nil, extractionError(ExtractionResourceLimited, "extract.zip_part_limit")
		}
		total += int64(file.UncompressedSize64)
		if total > limits.MaximumUncompressed {
			return nil, extractionError(ExtractionResourceLimited, "extract.zip_total_limit")
		}
		name, ok := normalizeOOXMLPartName(file.Name)
		if !ok {
			return nil, extractionError(ExtractionFailed, "extract.ooxml_invalid")
		}
		if _, duplicate := entries[name]; duplicate {
			return nil, extractionError(ExtractionFailed, "extract.ooxml_invalid")
		}
		entries[name] = file
	}
	return &ooxmlPackage{entries: entries}, ExtractionResult{}
}

// readOOXMLPart reads one package part within the declared size and context
// budgets. Missing parts report errOOXMLPartUnavailable.
func (pkg *ooxmlPackage) readOOXMLPart(
	ctx context.Context, name string, limits ExtractionLimits,
) ([]byte, error) {
	file, found := pkg.entries[name]
	if !found {
		return nil, errOOXMLPartUnavailable
	}
	part, err := file.Open()
	if err != nil {
		return nil, errOOXMLPartUnreadable
	}
	payload, readErr := readLimitedContext(ctx, part, limits.MaximumPartBytes+1)
	closeErr := part.Close()
	if readErr != nil || closeErr != nil {
		if errors.Is(readErr, context.Canceled) ||
			errors.Is(readErr, context.DeadlineExceeded) {
			return nil, readErr
		}
		return nil, errOOXMLPartUnreadable
	}
	if int64(len(payload)) > limits.MaximumPartBytes {
		return nil, errOOXMLPartUnreadable
	}
	return payload, nil
}

func ooxmlPartFailure(ctx context.Context, err error) ExtractionResult {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return extractionContextError(ctx)
	}
	return extractionError(ExtractionFailed, "extract.ooxml_part_failed")
}

// ooxmlRelationship is one package relationship entry.
type ooxmlRelationship struct {
	Target string
	Type   string
}

// relationshipPartName derives the standard rels part name for a part
// ("word/document.xml" -> "word/_rels/document.xml.rels").
func relationshipPartName(partName string) string {
	dir, base := path.Split(partName)
	return path.Join(path.Dir(dir), "_rels", base+".rels")
}

// loadRelationships reads and parses a rels part. A missing rels part
// reports errOOXMLPartUnavailable; a corrupt one fails closed.
func (pkg *ooxmlPackage) loadRelationships(
	ctx context.Context, partName string, limits ExtractionLimits,
) (map[string]ooxmlRelationship, error) {
	payload, err := pkg.readOOXMLPart(ctx, relationshipPartName(partName), limits)
	if err != nil {
		return nil, err
	}
	return parseRelationships(ctx, payload)
}

// mainPartName resolves the officeDocument part through the root
// relationships, falling back to standardName only when the package has no
// root rels at all (legacy minimal packages). Multiple officeDocument
// relationships or an unreadable root rels fail closed.
func (pkg *ooxmlPackage) mainPartName(
	ctx context.Context, standardName string, limits ExtractionLimits,
) (string, bool, error) {
	relsPayload, err := pkg.readOOXMLPart(ctx, "_rels/.rels", limits)
	if errors.Is(err, errOOXMLPartUnavailable) {
		if _, exists := pkg.entries[standardName]; exists {
			return standardName, true, nil
		}
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	relationships, err := parseRelationships(ctx, relsPayload)
	if err != nil {
		return "", false, err
	}
	var candidates []string
	for _, relationship := range relationships {
		if !strings.HasSuffix(relationship.Type, "/officeDocument") {
			continue
		}
		name, ok := resolvePackagePart("", relationship.Target)
		if !ok {
			continue
		}
		if _, exists := pkg.entries[name]; exists {
			candidates = append(candidates, name)
		}
	}
	sort.Strings(candidates)
	if len(candidates) != 1 {
		if len(candidates) > 1 {
			return "", false, errOOXMLAmbiguousMain
		}
		return "", false, nil
	}
	return candidates[0], true, nil
}

// resolveReferencedPart resolves the part for relationship typeSuffix from
// already-parsed relationships relative to baseDir. A missing relationship,
// an escaping target, or a target absent from the package reports
// errOOXMLRelUnresolvable; the caller decides between fail-closed and an
// honest partial marker.
func resolveReferencedPart(
	relationships map[string]ooxmlRelationship,
	baseDir, id, typeSuffix string,
) (string, error) {
	relationship, ok := relationships[id]
	if !ok || !strings.HasSuffix(relationship.Type, typeSuffix) {
		return "", errOOXMLRelUnresolvable
	}
	name, resolvable := resolvePackagePart(baseDir, relationship.Target)
	if !resolvable || !strings.HasSuffix(name, ".xml") {
		return "", errOOXMLRelUnresolvable
	}
	return name, nil
}

// extractDOCX extracts currently visible body text from the officeDocument
// part located through the root relationships (standard name fallback for
// rels-less packages), plus the header/footer parts referenced by
// w:headerReference/w:footerReference through the main part's rels.
func extractDOCX(ctx context.Context, pkg *ooxmlPackage, limits ExtractionLimits) ExtractionResult {
	mainName, found, err := pkg.mainPartName(ctx, "word/document.xml", limits)
	if err != nil {
		return ooxmlPartFailure(ctx, err)
	}
	if !found {
		return extractionError(ExtractionFailed, "extract.ooxml_part_failed")
	}
	mainPayload, err := pkg.readOOXMLPart(ctx, mainName, limits)
	if err != nil {
		return ooxmlPartFailure(ctx, err)
	}
	budget := newOOXMLTextBudget(limits.MaximumTextCodePoints)
	rules := wordprocessingStoryRules()
	var references []string
	if err := walkOOXMLStory(ctx, mainPayload, rules, budget, func(id string) {
		references = append(references, id)
	}); err != nil {
		return ooxmlPartFailure(ctx, err)
	}
	if len(references) > 0 {
		relationships, relsErr := pkg.loadRelationships(ctx, mainName, limits)
		if relsErr != nil {
			if errors.Is(relsErr, errOOXMLPartUnavailable) {
				budget.partial = true
			} else {
				return ooxmlPartFailure(ctx, relsErr)
			}
		} else {
			seen := map[string]bool{}
			baseDir := path.Dir(mainName)
			for _, id := range references {
				name, resolveErr := resolveReferencedPart(relationships, baseDir, id, "/header")
				if errors.Is(resolveErr, errOOXMLRelUnresolvable) {
					name, resolveErr = resolveReferencedPart(relationships, baseDir, id, "/footer")
				}
				if errors.Is(resolveErr, errOOXMLRelUnresolvable) ||
					seen[name] {
					if errors.Is(resolveErr, errOOXMLRelUnresolvable) {
						budget.partial = true
					}
					continue
				}
				seen[name] = true
				headerPayload, headerErr := pkg.readOOXMLPart(ctx, name, limits)
				if headerErr != nil {
					if errors.Is(headerErr, errOOXMLPartUnavailable) {
						budget.partial = true
						continue
					}
					return ooxmlPartFailure(ctx, headerErr)
				}
				if err := walkOOXMLStory(ctx, headerPayload, rules, budget, nil); err != nil {
					return ooxmlPartFailure(ctx, err)
				}
			}
		}
	}
	return budget.finish()
}

// extractPPTX extracts slides in p:sldIdLst order resolved through the
// presentation rels, then each slide's referenced notes slide. Unreferenced
// slide or notes parts are never indexed; missing presentation wiring fails
// closed; a single unreadable referenced slide or notes part is reported as
// indexed text plus extract.ooxml_partial.
func extractPPTX(ctx context.Context, pkg *ooxmlPackage, limits ExtractionLimits) ExtractionResult {
	mainName, found, err := pkg.mainPartName(ctx, "ppt/presentation.xml", limits)
	if err != nil {
		return ooxmlPartFailure(ctx, err)
	}
	if !found {
		return extractionError(ExtractionFailed, "extract.ooxml_part_failed")
	}
	presentationPayload, err := pkg.readOOXMLPart(ctx, mainName, limits)
	if err != nil {
		return ooxmlPartFailure(ctx, err)
	}
	relationships, err := pkg.loadRelationships(ctx, mainName, limits)
	if err != nil {
		return ooxmlPartFailure(ctx, err)
	}
	slideOrder, err := orderedRelationshipIDs(ctx, presentationPayload, nsPresentationML, "sldId")
	if err != nil {
		return ooxmlPartFailure(ctx, err)
	}
	if len(slideOrder) == 0 {
		return extractionError(ExtractionFailed, "extract.ooxml_part_failed")
	}
	baseDir := path.Dir(mainName)
	budget := newOOXMLTextBudget(limits.MaximumTextCodePoints)
	for _, id := range slideOrder {
		slideName, resolveErr := resolveReferencedPart(relationships, baseDir, id, "/slide")
		if resolveErr != nil {
			budget.partial = true
			continue
		}
		slidePayload, slideErr := pkg.readOOXMLPart(ctx, slideName, limits)
		if slideErr != nil {
			if errors.Is(slideErr, errOOXMLPartUnavailable) {
				budget.partial = true
				continue
			}
			return ooxmlPartFailure(ctx, slideErr)
		}
		if err := walkOOXMLStory(ctx, slidePayload, presentationStoryRules(), budget, nil); err != nil {
			return ooxmlPartFailure(ctx, err)
		}
		slideRelationships, notesRelsErr := pkg.loadRelationships(ctx, slideName, limits)
		if notesRelsErr != nil {
			if !errors.Is(notesRelsErr, errOOXMLPartUnavailable) {
				return ooxmlPartFailure(ctx, notesRelsErr)
			}
			continue
		}
		notesName, notesErr := relationshipByType(slideRelationships, path.Dir(slideName), "/notesSlide")
		if notesErr != nil {
			budget.partial = true
			continue
		}
		notesPayload, notesReadErr := pkg.readOOXMLPart(ctx, notesName, limits)
		if notesReadErr != nil {
			if errors.Is(notesReadErr, errOOXMLPartUnavailable) {
				budget.partial = true
				continue
			}
			return ooxmlPartFailure(ctx, notesReadErr)
		}
		if err := walkOOXMLStory(ctx, notesPayload, presentationStoryRules(), budget, nil); err != nil {
			return ooxmlPartFailure(ctx, err)
		}
	}
	return budget.finish()
}

// relationshipByType resolves the single relationship whose Type ends with
// typeSuffix (deterministic by Id order) relative to baseDir.
func relationshipByType(
	relationships map[string]ooxmlRelationship, baseDir, typeSuffix string,
) (string, error) {
	ids := make([]string, 0, len(relationships))
	for id := range relationships {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		name, err := resolveReferencedPart(relationships, baseDir, id, typeSuffix)
		if err == nil {
			return name, nil
		}
	}
	return "", errOOXMLRelUnresolvable
}

// extractXLSX extracts only actually present cells from the worksheet parts
// listed by the workbook rels: shared strings are dereferenced by real cell
// references, every cell type maps to its displayed value, formulas are
// never recalculated, and unreferenced worksheets or shared strings are
// never indexed.
func extractXLSX(ctx context.Context, pkg *ooxmlPackage, limits ExtractionLimits) ExtractionResult {
	mainName, found, err := pkg.mainPartName(ctx, "xl/workbook.xml", limits)
	if err != nil {
		return ooxmlPartFailure(ctx, err)
	}
	if !found {
		return extractionError(ExtractionFailed, "extract.ooxml_part_failed")
	}
	workbookPayload, err := pkg.readOOXMLPart(ctx, mainName, limits)
	if err != nil {
		return ooxmlPartFailure(ctx, err)
	}
	relationships, err := pkg.loadRelationships(ctx, mainName, limits)
	if err != nil {
		return ooxmlPartFailure(ctx, err)
	}
	sheetOrder, err := orderedRelationshipIDs(ctx, workbookPayload, nsSpreadsheetML, "sheet")
	if err != nil {
		return ooxmlPartFailure(ctx, err)
	}
	if len(sheetOrder) == 0 {
		return extractionError(ExtractionFailed, "extract.ooxml_part_failed")
	}
	shared := []string{}
	sharedUnreadable := false
	if sharedName, sharedErr := relationshipByType(relationships, path.Dir(mainName), "/sharedStrings"); sharedErr == nil {
		sharedPayload, readErr := pkg.readOOXMLPart(ctx, sharedName, limits)
		if readErr != nil {
			if errors.Is(readErr, errOOXMLPartUnavailable) {
				sharedUnreadable = true
			} else {
				return ooxmlPartFailure(ctx, readErr)
			}
		} else {
			shared, err = parseSharedStrings(ctx, sharedPayload)
			if err != nil {
				return ooxmlPartFailure(ctx, err)
			}
		}
	}
	dateStyles := []bool{}
	if stylesName, stylesErr := relationshipByType(relationships, path.Dir(mainName), "/styles"); stylesErr == nil {
		stylesPayload, readErr := pkg.readOOXMLPart(ctx, stylesName, limits)
		if readErr != nil {
			if !errors.Is(readErr, errOOXMLPartUnavailable) {
				return ooxmlPartFailure(ctx, readErr)
			}
		} else {
			dateStyles, err = parseDateStyles(ctx, stylesPayload)
			if err != nil {
				return ooxmlPartFailure(ctx, err)
			}
		}
	}
	baseDir := path.Dir(mainName)
	budget := newOOXMLTextBudget(limits.MaximumTextCodePoints)
	budget.partial = sharedUnreadable
	for _, id := range sheetOrder {
		sheetName, resolveErr := resolveReferencedPart(relationships, baseDir, id, "/worksheet")
		if resolveErr != nil {
			budget.partial = true
			continue
		}
		sheetPayload, sheetErr := pkg.readOOXMLPart(ctx, sheetName, limits)
		if sheetErr != nil {
			if errors.Is(sheetErr, errOOXMLPartUnavailable) {
				budget.partial = true
				continue
			}
			return ooxmlPartFailure(ctx, sheetErr)
		}
		if err := worksheetVisibleCells(ctx, sheetPayload, shared, dateStyles, budget); err != nil {
			return ooxmlPartFailure(ctx, err)
		}
	}
	return budget.finish()
}

// orderedRelationshipIDs returns the r:id attributes of itemElement entries
// in document order (used for p:sldId and sheet lists).
func orderedRelationshipIDs(
	ctx context.Context, payload []byte, namespace, itemElement string,
) ([]string, error) {
	order := make([]string, 0, 8)
	err := walkStrictXML(ctx, payload, func(decoder *xml.Decoder, token xml.Token) error {
		element, ok := token.(xml.StartElement)
		if !ok || element.Name.Local != itemElement || element.Name.Space != namespace {
			return nil
		}
		for _, attribute := range element.Attr {
			if isRelationshipIDAttribute(attribute) {
				order = append(order, attribute.Value)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

func isRelationshipIDAttribute(attribute xml.Attr) bool {
	return attribute.Name.Local == "id" && attribute.Name.Space == ooxmlRelationshipNS
}

// parseRelationships maps relationship Id -> entry. Only Relationship
// elements in the package relationships namespace are recognized.
func parseRelationships(ctx context.Context, payload []byte) (map[string]ooxmlRelationship, error) {
	relationships := map[string]ooxmlRelationship{}
	err := walkStrictXML(ctx, payload, func(decoder *xml.Decoder, token xml.Token) error {
		element, ok := token.(xml.StartElement)
		if !ok || element.Name.Local != "Relationship" ||
			element.Name.Space != nsPackageRelationships {
			return nil
		}
		var id string
		var relationship ooxmlRelationship
		for _, attribute := range element.Attr {
			switch attribute.Name.Local {
			case "Id":
				id = attribute.Value
			case "Target":
				relationship.Target = attribute.Value
			case "Type":
				relationship.Type = attribute.Value
			}
		}
		if id != "" && relationship.Target != "" {
			relationships[id] = relationship
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return relationships, nil
}

// resolvePackagePart resolves a relationship target relative to baseDir
// ("" resolves against the package root), rejecting traversal outside the
// package.
func resolvePackagePart(baseDir, target string) (string, bool) {
	target = strings.ReplaceAll(target, "\\", "/")
	resolved := target
	if strings.HasPrefix(resolved, "/") {
		resolved = resolved[1:]
	} else if baseDir != "" {
		resolved = baseDir + "/" + resolved
	}
	resolved = path.Clean(resolved)
	if resolved == "" || resolved == "." || resolved == ".." ||
		strings.HasPrefix(resolved, "../") || strings.Contains(resolved, "/../") {
		return "", false
	}
	return resolved, true
}

// walkStrictXML drives a strict XML decoder token loop with cooperative
// context cancellation between tokens.
func walkStrictXML(
	ctx context.Context,
	payload []byte,
	visit func(*xml.Decoder, xml.Token) error,
) error {
	decoder := xml.NewDecoder(bytes.NewReader(payload))
	decoder.Strict = true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := visit(decoder, token); err != nil {
			return err
		}
	}
}

// ooxmlStoryRules names the element namespaces one story part uses.
type ooxmlStoryRules struct {
	textNS, paragraphNS, breakNS, tabNS string
	captureWordprocessingReferences     bool
}

func wordprocessingStoryRules() ooxmlStoryRules {
	return ooxmlStoryRules{
		textNS: nsWordprocessingML, paragraphNS: nsWordprocessingML,
		breakNS: nsWordprocessingML, tabNS: nsWordprocessingML,
		captureWordprocessingReferences: true,
	}
}

func presentationStoryRules() ooxmlStoryRules {
	return ooxmlStoryRules{
		// Slide and notes text bodies are DrawingML: paragraphs are a:p
		// inside p:txBody, runs are a:r/a:t. The presentation namespace only
		// wraps the part, so paragraph boundaries must match DrawingML or
		// distinct paragraphs would silently merge into one word.
		textNS:      nsDrawingML,
		paragraphNS: nsDrawingML,
		breakNS:     nsDrawingML,
	}
}

// walkOOXMLStory joins text runs without separators so words split across
// runs stay searchable, honors xml:space, renders tabs and breaks as
// boundaries, separates paragraphs with newlines, skips mc:Fallback
// duplicates, reports w:headerReference/w:footerReference relationship ids
// through onReference (DOCX), and marks footnote/endnote/comment references
// as uncovered partial coverage.
func walkOOXMLStory(
	ctx context.Context,
	payload []byte,
	rules ooxmlStoryRules,
	budget *ooxmlTextBudget,
	onReference func(id string),
) error {
	decoder := xml.NewDecoder(bytes.NewReader(payload))
	decoder.Strict = true
	var line strings.Builder
	lineRunes := 0
	var fallbackDepth, moveFromDepth int
	inText, preserveSpace := false, false
	pendingCap := budget.pendingRuneCap()
	flush := func() {
		budget.writeLine(line.String())
		line.Reset()
		lineRunes = 0
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if fallbackDepth > 0 || moveFromDepth > 0 {
			switch element := token.(type) {
			case xml.StartElement:
				if isElement(element, nsMarkupCompatibility, "Fallback") {
					fallbackDepth++
				}
				if isElement(element, nsWordprocessingML, "moveFrom") {
					moveFromDepth++
				}
			case xml.EndElement:
				if isEndElement(element, nsMarkupCompatibility, "Fallback") {
					fallbackDepth--
				}
				if isEndElement(element, nsWordprocessingML, "moveFrom") {
					moveFromDepth--
				}
			}
			continue
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch {
			case isElement(element, rules.textNS, "t"):
				inText = true
				preserveSpace = preservesXMLSpace(element.Attr)
			case isElement(element, nsMarkupCompatibility, "Fallback"):
				fallbackDepth++
			case rules.captureWordprocessingReferences &&
				isElement(element, nsWordprocessingML, "txbxContent"):
				// A text box is its own visible block: close the story line
				// before its first paragraph and again after the last one.
				flush()
			case isElement(element, rules.tabNS, "tab"):
				if lineRunes < pendingCap {
					line.WriteByte('\t')
					lineRunes++
				}
			case isElement(element, rules.breakNS, "br"),
				isElement(element, rules.breakNS, "cr"):
				if lineRunes < pendingCap {
					line.WriteByte('\n')
					lineRunes++
				}
			case rules.captureWordprocessingReferences &&
				(isElement(element, nsWordprocessingML, "moveFrom")):
				moveFromDepth++
			case rules.captureWordprocessingReferences &&
				(isElement(element, nsWordprocessingML, "headerReference") ||
					isElement(element, nsWordprocessingML, "footerReference")):
				if onReference != nil {
					for _, attribute := range element.Attr {
						if isRelationshipIDAttribute(attribute) {
							onReference(attribute.Value)
						}
					}
				}
			case rules.captureWordprocessingReferences &&
				(isElement(element, nsWordprocessingML, "footnoteReference") ||
					isElement(element, nsWordprocessingML, "endnoteReference") ||
					isElement(element, nsWordprocessingML, "commentReference")):
				budget.partial = true
			}
		case xml.EndElement:
			switch {
			case isEndElement(element, rules.textNS, "t"):
				inText, preserveSpace = false, false
			case isEndElement(element, rules.paragraphNS, "p"):
				// Every paragraph closes its own line, including paragraphs
				// nested in w:txbxContent: story text before and after a text
				// box must never merge with the box text into one word.
				flush()
			case rules.captureWordprocessingReferences &&
				isEndElement(element, nsWordprocessingML, "txbxContent"):
				flush()
			}
		case xml.CharData:
			if !inText || budget.overflow {
				continue
			}
			value := string(element)
			if !preserveSpace {
				value = strings.TrimSpace(value)
			}
			lineRunes = appendRuneCapped(&line, lineRunes, value, pendingCap)
		}
	}
	flush()
	return nil
}

func isElement(element xml.StartElement, namespace, local string) bool {
	return element.Name.Local == local && element.Name.Space == namespace
}

func isEndElement(element xml.EndElement, namespace, local string) bool {
	return element.Name.Local == local && element.Name.Space == namespace
}

func preservesXMLSpace(attributes []xml.Attr) bool {
	for _, attribute := range attributes {
		if attribute.Name.Space == xmlNamespaceXML &&
			attribute.Name.Local == "space" && attribute.Value == "preserve" {
			return true
		}
	}
	return false
}

// parseSharedStrings returns shared string items in sst order, joining rich
// text runs exactly as the spreadsheet displays them. Item accumulation is
// bounded by the part budget already enforced by the caller.
func parseSharedStrings(ctx context.Context, payload []byte) ([]string, error) {
	var items []string
	var item strings.Builder
	inItem, inText, preserveSpace := false, false, false
	err := walkStrictXML(ctx, payload, func(decoder *xml.Decoder, token xml.Token) error {
		switch element := token.(type) {
		case xml.StartElement:
			switch {
			case isElement(element, nsSpreadsheetML, "si"):
				inItem = true
				item.Reset()
			case inItem && isElement(element, nsSpreadsheetML, "t"):
				inText = true
				preserveSpace = preservesXMLSpace(element.Attr)
			}
		case xml.EndElement:
			switch {
			case isEndElement(element, nsSpreadsheetML, "si"):
				if inItem {
					items = append(items, item.String())
					inItem = false
				}
			case isEndElement(element, nsSpreadsheetML, "t"):
				inText, preserveSpace = false, false
			}
		case xml.CharData:
			if !inText {
				return nil
			}
			value := string(element)
			if !preserveSpace {
				value = strings.TrimSpace(value)
			}
			item.WriteString(value)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// parseDateStyles maps cellXfs indexes to whether their numFmt renders a
// date or time. Custom numFmt format codes are inspected for date/time
// tokens; the built-in date format ids are matched explicitly.
func parseDateStyles(ctx context.Context, payload []byte) ([]bool, error) {
	customDate := map[int64]bool{}
	dateStyles := []bool{}
	inNumFmts, inCellXfs := false, false
	err := walkStrictXML(ctx, payload, func(decoder *xml.Decoder, token xml.Token) error {
		switch element := token.(type) {
		case xml.StartElement:
			switch {
			case isElement(element, nsSpreadsheetML, "numFmts"):
				inNumFmts = true
			case isElement(element, nsSpreadsheetML, "cellXfs"):
				inCellXfs = true
			case inNumFmts && isElement(element, nsSpreadsheetML, "numFmt"):
				var id int64
				code := ""
				for _, attribute := range element.Attr {
					switch attribute.Name.Local {
					case "numFmtId":
						id, _ = strconv.ParseInt(attribute.Value, 10, 64)
					case "formatCode":
						code = attribute.Value
					}
				}
				customDate[id] = isDateFormatCode(code)
			case inCellXfs && isElement(element, nsSpreadsheetML, "xf"):
				var id int64 = -1
				for _, attribute := range element.Attr {
					if attribute.Name.Local == "numFmtId" {
						id, _ = strconv.ParseInt(attribute.Value, 10, 64)
					}
				}
				dateStyles = append(dateStyles, isDateNumFmtID(id, customDate))
			}
		case xml.EndElement:
			switch {
			case isEndElement(element, nsSpreadsheetML, "numFmts"):
				inNumFmts = false
			case isEndElement(element, nsSpreadsheetML, "cellXfs"):
				inCellXfs = false
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dateStyles, nil
}

// isDateNumFmtID covers the built-in date/time format ids 14-22 and 45-47
// plus custom formats already classified as dates.
func isDateNumFmtID(id int64, customDate map[int64]bool) bool {
	if customDate[id] {
		return true
	}
	return (id >= 14 && id <= 22) || (id >= 45 && id <= 47)
}

// isDateFormatCode reports whether a custom format code renders a date or
// time. Quoted literals are ignored before looking for date/time tokens.
func isDateFormatCode(code string) bool {
	var significant strings.Builder
	inQuote := false
	for _, value := range code {
		switch {
		case value == '"':
			inQuote = !inQuote
		case !inQuote:
			significant.WriteRune(unicode.ToLower(value))
		}
	}
	format := significant.String()
	for _, token := range []string{"y", "d", "h", "s", "am", "pm"} {
		if strings.Contains(format, token) {
			return true
		}
	}
	// A lone m is month-or-minute: it only reaches a date when combined
	// with another date token, which the checks above already caught.
	return false
}

// worksheetVisibleCells streams each present cell's displayed value into
// the budget: shared strings are dereferenced, booleans render TRUE/FALSE,
// error codes and cached formula strings surface verbatim, untyped values
// surface as their numeric literal (date-formatted cells therefore surface
// as serial numbers with a partial marker), inline strings are read
// directly, and formulas without a cached value contribute nothing. Each
// intentional non-coverage marks the budget partial instead of silently
// dropping content.
func worksheetVisibleCells(
	ctx context.Context,
	payload []byte,
	shared []string,
	dateStyles []bool,
	budget *ooxmlTextBudget,
) error {
	var value strings.Builder
	var inline strings.Builder
	valueRunes, inlineRunes := 0, 0
	cellType, cellStyle := "", -1
	inCell, inValue, inFormula, inInline := false, false, false, false
	hadFormula := false
	hadCachedValue := false
	inText, preserveSpace := false, false
	pendingCap := budget.pendingRuneCap()
	closeCell := func() {
		text, partial := cellVisibleText(
			cellType, value.String(), inline.String(), shared,
		)
		if hadFormula && (!hadCachedValue ||
			(cellType != "str" && strings.TrimSpace(value.String()) == "")) {
			// Formula without an indexable cache: uncalculated numeric
			// formulas surface as <f>…</f><v></v> (openpyxl), so a blank
			// non-string cache is still missing. Only t="str" legally caches
			// an empty string. Never recalculated, never fabricated.
			partial = true
		}
		if cellStyle >= 0 && cellStyle < len(dateStyles) && dateStyles[cellStyle] {
			// Date-formatted cell surfaces as its raw serial number.
			partial = true
		}
		budget.partial = budget.partial || partial
		budget.writeLine(text)
		value.Reset()
		inline.Reset()
		valueRunes, inlineRunes = 0, 0
		cellType, cellStyle, inFormula, hadFormula, hadCachedValue = "", -1, false, false, false
		inCell = false
	}
	err := walkStrictXML(ctx, payload, func(decoder *xml.Decoder, token xml.Token) error {
		switch element := token.(type) {
		case xml.StartElement:
			switch {
			case isElement(element, nsSpreadsheetML, "c"):
				if inCell {
					closeCell()
				}
				inCell = true
				cellType, cellStyle, hadFormula, hadCachedValue = "", -1, false, false
				for _, attribute := range element.Attr {
					switch attribute.Name.Local {
					case "t":
						cellType = attribute.Value
					case "s":
						if index, err := strconv.Atoi(attribute.Value); err == nil && index >= 0 {
							cellStyle = index
						}
					}
				}
			case inCell && isElement(element, nsSpreadsheetML, "f"):
				inFormula = true
				hadFormula = true
			case inCell && !inFormula && isElement(element, nsSpreadsheetML, "v"):
				inValue = true
				value.Reset()
				// Any <v> counts as a present cache element; whether it is
				// indexable is decided at cell close from its content and
				// the declared cell type.
				hadCachedValue = true
			case inCell && isElement(element, nsSpreadsheetML, "is"):
				inInline = true
				inline.Reset()
			case inInline && isElement(element, nsSpreadsheetML, "t"):
				inText = true
				preserveSpace = preservesXMLSpace(element.Attr)
			}
		case xml.EndElement:
			switch {
			case isEndElement(element, nsSpreadsheetML, "c"):
				if inCell {
					closeCell()
				}
			case isEndElement(element, nsSpreadsheetML, "f"):
				inFormula = false
			case isEndElement(element, nsSpreadsheetML, "v"):
				inValue = false
			case isEndElement(element, nsSpreadsheetML, "is"):
				inInline = false
			case isEndElement(element, nsSpreadsheetML, "t"):
				inText, preserveSpace = false, false
			}
		case xml.CharData:
			if budget.overflow {
				return nil
			}
			text := string(element)
			switch {
			case inValue:
				valueRunes = appendRuneCapped(&value, valueRunes, text, pendingCap)
			case inText:
				if !preserveSpace {
					text = strings.TrimSpace(text)
				}
				inlineRunes = appendRuneCapped(&inline, inlineRunes, text, pendingCap)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if inCell {
		closeCell()
	}
	return nil
}

// cellVisibleText maps one cell to its displayed value and reports whether
// an intentional non-coverage applied (out-of-range shared string index).
func cellVisibleText(cellType, value, inline string, shared []string) (string, bool) {
	value = strings.TrimSpace(value)
	switch cellType {
	case "s":
		index, err := strconv.Atoi(value)
		if err != nil || index < 0 || index >= len(shared) {
			return "", true
		}
		return shared[index], false
	case "inlineStr", "inline":
		return inline, false
	case "str", "e":
		return value, false
	case "b":
		switch value {
		case "1":
			return "TRUE", false
		case "0":
			return "FALSE", false
		default:
			return "", false
		}
	default:
		return value, false
	}
}

// ooxmlTextBudget bounds accumulated visible text while parts stream in:
// once the code-point limit is reached no further text is stored, while
// parsing continues so later corrupt parts still fail closed. partial marks
// referenced content that could not be read or intentionally uncovered
// regions (indexed + extract.ooxml_partial).
type ooxmlTextBudget struct {
	output                   strings.Builder
	limit, runes, limitBytes int
	overflow, partial        bool
}

func newOOXMLTextBudget(limit int) *ooxmlTextBudget {
	return &ooxmlTextBudget{limit: limit}
}

// pendingRuneCap is the rune bound for one pending line or cell value: the
// text limit plus one rune, so a line that alone reaches the bound always
// trips the truncation path in writeLine instead of silently fitting and
// losing its tail. It also bounds memory for a single huge XML CharData.
func (budget *ooxmlTextBudget) pendingRuneCap() int {
	return budget.limit + 1
}

// appendRuneCapped appends chunk to builder, counting runes, and stops at
// runeCap while keeping every appended rune intact (no mid-rune cuts).
func appendRuneCapped(builder *strings.Builder, count int, chunk string, runeCap int) int {
	if count >= runeCap {
		return count
	}
	remaining := runeCap - count
	if utf8.RuneCountInString(chunk) <= remaining {
		builder.WriteString(chunk)
		return count + utf8.RuneCountInString(chunk)
	}
	builder.WriteString(prefixAtRuneLimit(chunk, remaining))
	return runeCap
}

// writeLine appends one visible line (paragraph or cell) with its trailing
// boundary trimmed; blank lines contribute nothing.
func (budget *ooxmlTextBudget) writeLine(line string) {
	if budget.overflow {
		return
	}
	value := strings.TrimRight(line, " \t\n")
	if strings.TrimSpace(value) == "" {
		return
	}
	value += "\n"
	if budget.runes+utf8.RuneCountInString(value) <= budget.limit {
		budget.output.WriteString(value)
		budget.runes += utf8.RuneCountInString(value)
		if budget.runes == budget.limit {
			budget.limitBytes = budget.output.Len()
		}
		return
	}
	budget.output.WriteString(prefixAtRuneLimit(value, budget.limit-budget.runes))
	budget.runes = budget.limit
	budget.limitBytes = budget.output.Len()
	budget.overflow = true
}

// prefixAtRuneLimit returns the longest byte prefix of value holding at
// most remaining runes.
func prefixAtRuneLimit(value string, remaining int) string {
	if remaining <= 0 {
		return ""
	}
	count := 0
	for index := range value {
		if count == remaining {
			return value[:index]
		}
		count++
	}
	return value
}

func (budget *ooxmlTextBudget) finish() ExtractionResult {
	if budget.overflow {
		code := "extract.text_limit"
		return ExtractionResult{
			Status:    ExtractionTruncated,
			Text:      budget.output.String()[:budget.limitBytes],
			ErrorCode: &code,
		}
	}
	text := strings.TrimSpace(budget.output.String())
	if budget.partial {
		code := ooxmlPartialCode
		return ExtractionResult{
			Status: ExtractionIndexed, Text: text, ErrorCode: &code,
		}
	}
	return ExtractionResult{Status: ExtractionIndexed, Text: text}
}
