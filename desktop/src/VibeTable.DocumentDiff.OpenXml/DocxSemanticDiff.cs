using System.Globalization;
using System.Text;
using System.Xml;
using System.Xml.Linq;
using VibeTable.Workspace.Diff;
using static VibeTable.DocumentDiff.OpenXml.OpenXmlReadSafety;

namespace VibeTable.DocumentDiff.OpenXml;

// Compares final-content copies. The owned worker performs revision normalization first.
internal static class DocxSemanticDiff
{
    private static readonly XNamespace W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";
    private const int MaxGroups = 20_000;
    private const int MaxLcsCells = 4_000_000;
    private const int MaxSnippetCharacters = 2048;

    public static async Task<DocumentDiffOutcome> CompareAsync(DocumentDiffRequest request,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(request);
        var comparison = new Comparison(cancellationToken);
        try
        {
            using var before = await DocxPackageReader.OpenAsync(request.Before, cancellationToken).ConfigureAwait(false);
            using var after = await DocxPackageReader.OpenAsync(request.After, cancellationToken).ConfigureAwait(false);
            comparison.Compare(DocxPartSelection.Read(before, cancellationToken),
                DocxPartSelection.Read(after, cancellationToken));
            return comparison.Result();
        }
        catch (OperationCanceledException) { return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.Cancelled); }
        catch (DiffBudgetExceededException) { comparison.Truncated = true; return comparison.Result(); }
        catch (Exception error) when (error is InvalidDataException or XmlException or ArgumentException)
        { return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.InvalidContent); }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException)
        { return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.Io); }
    }

    private sealed class Comparison(CancellationToken token)
    {
        private readonly List<DocumentDiffChange> _changes = [];
        private readonly Dictionary<XElement, IReadOnlyList<Paragraph>> _paragraphs = [];
        private readonly HashSet<XElement> _countedElements = [];
        private readonly HashSet<XElement> _countedRoots = [];
        private int _characters;
        private int _nodes;
        private int _cells = MaxLcsCells;
        private int _revisionId;
        private int _rawCount;
        public bool Truncated { get; set; }

        public void Compare(DocxPartSelection before, DocxPartSelection after)
        {
            CountSelection(before);
            CountSelection(after);
            CompareStory(before.MainDocument.Root!.Element(W + "body"),
                after.MainDocument.Root!.Element(W + "body"), DocumentDiffPart.Body, 0);
            var left = before.HeadersFooters.ToDictionary(BindingKey);
            var right = after.HeadersFooters.ToDictionary(BindingKey);
            foreach (var key in left.Keys.Union(right.Keys).OrderBy(k => k.SectionIndex)
                         .ThenBy(k => k.Kind).ThenBy(k => k.Variant, StringComparer.Ordinal))
            {
                token.ThrowIfCancellationRequested();
                left.TryGetValue(key, out var oldBinding);
                right.TryGetValue(key, out var newBinding);
                CompareStory(oldBinding?.Document.Root, newBinding?.Document.Root, key.Kind, key.SectionIndex);
                if (oldBinding?.IsDisplayed != newBinding?.IsDisplayed)
                    Add(DocumentDiffChangeKind.Other, new(key.Kind, sectionIndex: key.SectionIndex), null, null);
            }
            if (before.SectionCount != after.SectionCount)
                Add(DocumentDiffChangeKind.Other, new(DocumentDiffPart.Body), null, null);
        }

        private static (int SectionIndex, DocumentDiffPart Kind, string Variant) BindingKey(DocxHeaderFooterBinding binding)
            => (binding.SectionIndex, binding.Kind, binding.Variant);

        private void CompareStory(XElement? before, XElement? after, DocumentDiffPart part, int section)
        {
            var oldParagraphs = Read(before, part, section);
            var newParagraphs = Read(after, part, section);
            var oldContainers = oldParagraphs.GroupBy(p => p.Container).ToDictionary(g => g.Key, g => g.ToArray());
            var newContainers = newParagraphs.GroupBy(p => p.Container).ToDictionary(g => g.Key, g => g.ToArray());
            foreach (string key in oldContainers.Keys.Union(newContainers.Keys).Order(StringComparer.Ordinal))
            {
                token.ThrowIfCancellationRequested();
                Paragraph[] oldItems = oldContainers.GetValueOrDefault(key) ?? [];
                Paragraph[] newItems = newContainers.GetValueOrDefault(key) ?? [];
                string[] oldText = oldItems.Select(p => p.Text).ToArray();
                string[] newText = newItems.Select(p => p.Text).ToArray();
                int budget = TakeCells(oldText, newText);
                var alignment = DocxParagraphAligner.Align(oldText, newText, budget, token);
                Truncated |= alignment.Truncated;
                foreach (var span in alignment.Spans)
                {
                    token.ThrowIfCancellationRequested();
                    if (span.EqualText)
                    {
                        for (int i = 0; i < span.BeforeCount; i++)
                            CompareParagraph(oldItems[span.BeforeStart + i], newItems[span.AfterStart + i]);
                    }
                    else if (!alignment.Truncated && span.BeforeCount == 1 && span.AfterCount == 1)
                        CompareParagraph(oldItems[span.BeforeStart], newItems[span.AfterStart]);
                    else
                    {
                        // ponytail: ambiguous N:M spans stay delete/insert; require real anchors before pairing.
                        for (int i = 0; i < span.BeforeCount; i++)
                            CompareParagraph(oldItems[span.BeforeStart + i], null, alignment.Truncated);
                        for (int i = 0; i < span.AfterCount; i++)
                            CompareParagraph(null, newItems[span.AfterStart + i], alignment.Truncated);
                    }
                }
            }
            XElement[] oldTables = Tables(before);
            XElement[] newTables = Tables(after);
            for (int i = 0; i < Math.Max(oldTables.Length, newTables.Length); i++)
            {
                token.ThrowIfCancellationRequested();
                string oldShape = i < oldTables.Length ? TableShape(oldTables[i]) : "";
                string newShape = i < newTables.Length ? TableShape(newTables[i]) : "";
                if (oldShape != newShape)
                    Add(DocumentDiffChangeKind.Table, new(part, sectionIndex: section, tableIndex: i),
                        ShapeSnippet(oldShape), ShapeSnippet(newShape));
            }
        }

        private void CountSelection(DocxPartSelection selection)
        {
            CountXml(selection.MainDocument.Root!);
            foreach (var binding in selection.HeadersFooters) CountXml(binding.Document.Root!);
            foreach (var note in selection.Notes) CountXml(note.Document.Root!);
        }

        private void CountXml(XElement root)
        {
            if (!_countedRoots.Add(root)) return;
            foreach (XElement element in root.DescendantsAndSelf())
            {
                token.ThrowIfCancellationRequested();
                if (!_countedElements.Add(element)) continue;
                _nodes = checked(_nodes + 1 + element.Attributes().Count());
                if (_nodes > 250_000) throw new DiffBudgetExceededException();
                if (element.Name.Namespace == W && (element.Name.LocalName is "ins" or "del" or "moveFrom" or "moveTo"
                    || element.Name.LocalName.EndsWith("Change", StringComparison.Ordinal)))
                    throw new InvalidDataException("Semantic comparison requires normalized final content.");
            }
        }

        private IReadOnlyList<Paragraph> Read(XElement? root, DocumentDiffPart part, int section)
        {
            if (root is null) return [];
            if (!_paragraphs.TryGetValue(root, out var cached))
            {
                var index = DocxStructuralLocator.Index(root, part, 250_000, section, token);
                Truncated |= index.Truncated;
                var result = new List<Paragraph>();
                int bodyLane = 0;
                int fieldDepth = 0;
                XElement? previousTopTable = null;
                foreach (var located in index.Paragraphs)
                {
                    token.ThrowIfCancellationRequested();
                    XElement paragraph = located.Paragraph;
                    XElement? cell = paragraph.Ancestors(W + "tc").FirstOrDefault();
                    XElement? topTable = paragraph.Ancestors(W + "tbl").LastOrDefault();
                    if (previousTopTable != topTable && (previousTopTable is not null || topTable is not null)) bodyLane++;
                    previousTopTable = topTable;
                    string container = cell is null ? "body:" + bodyLane.ToString(CultureInfo.InvariantCulture)
                        : $"cell:{located.Location.TableIndex}:{located.Location.RowIndex}:{located.Location.ColumnIndex}";
                    var runs = ReadRuns(paragraph, ref fieldDepth);
                    string text = string.Concat(runs.Select(run => run.Text));
                    _characters = checked(_characters + text.Length);
                    if (_characters > OpenXmlExtractionLimits.MaxVisibleTextCharacters) throw new DiffBudgetExceededException();
                    string? heading = located.Location.NearestHeading is { } originalHeading ? BoundText(originalHeading, 256) : null;
                    if (heading?.Length == 0) heading = null;
                    var location = new DocumentDiffLocation(part, sectionIndex: located.Location.SectionIndex,
                        paragraphIndex: located.Location.ParagraphIndex, nearestHeading: heading,
                        tableIndex: located.Location.TableIndex, rowIndex: located.Location.RowIndex,
                        columnIndex: located.Location.ColumnIndex);
                    result.Add(new(paragraph, location, container, runs, text));
                }
                cached = result.AsReadOnly();
                _paragraphs.Add(root, cached);
            }
            // A physical header can be inherited by several sections; retain each binding's location.
            return part == DocumentDiffPart.Body ? cached : cached.Select(p => p with
            {
                Location = new(part, sectionIndex: section, paragraphIndex: p.Location.ParagraphIndex,
                    nearestHeading: p.Location.NearestHeading, tableIndex: p.Location.TableIndex,
                    rowIndex: p.Location.RowIndex, columnIndex: p.Location.ColumnIndex),
            }).ToArray();
        }

        private IReadOnlyList<DocxStyledText> ReadRuns(XElement paragraph, ref int fieldDepth)
        {
            var output = new List<DocxStyledText>();
            foreach (XElement run in paragraph.Descendants(W + "r").Where(r => r.Ancestors(W + "p").FirstOrDefault() == paragraph
                         && !r.Ancestors(W + "txbxContent").Any()))
            {
                token.ThrowIfCancellationRequested();
                var text = new StringBuilder();
                foreach (XElement child in run.Elements())
                {
                    if (child.Name == W + "fldChar")
                    {
                        string? kind = (string?)child.Attribute(W + "fldCharType");
                        if (kind == "begin") fieldDepth++;
                        else if (kind == "end") fieldDepth = Math.Max(0, fieldDepth - 1);
                        continue;
                    }
                    if (fieldDepth != 0 || run.Ancestors(W + "fldSimple").Any()) continue;
                    if (child.Name == W + "t") text.Append(child.Value);
                    else if (child.Name == W + "tab") text.Append('\t');
                    else if (child.Name == W + "br" && (string?)child.Attribute(W + "type") is null or "textWrapping"
                             || child.Name == W + "cr") text.Append('\n');
                }
                if (text.Length > 0)
                {
                    XElement? properties = CanonicalRun(run.Element(W + "rPr"));
                    if (properties?.Element(W + "rFonts") is { } fonts)
                    {
                        foreach (XAttribute font in fonts.Attributes().ToArray())
                        {
                            string value = BoundText(font.Value, 256);
                            if (value.Length == 0) font.Remove(); else font.Value = value;
                        }
                        if (!fonts.HasAttributes) fonts.Remove();
                        if (!properties.HasElements) properties = null;
                    }
                    output.Add(new(text.ToString(), properties));
                }
            }
            return output.AsReadOnly();
        }

        private void CompareParagraph(Paragraph? before, Paragraph? after, bool coarse = false)
        {
            token.ThrowIfCancellationRequested();
            var location = after?.Location ?? before!.Location;
            string oldText = before?.Text ?? "";
            string newText = after?.Text ?? "";
            if (before is null || after is null)
            {
                if (oldText.Length == 0 && newText.Length == 0)
                { Add(DocumentDiffChangeKind.Other, location, null, null); return; }
            }
            if (coarse)
            {
                Add(oldText.Length == 0 ? DocumentDiffChangeKind.Insert : newText.Length == 0
                        ? DocumentDiffChangeKind.Delete : DocumentDiffChangeKind.Replace, location,
                    Snippet(before?.Runs, DocumentDiffRichRunRole.Deleted),
                    Snippet(after?.Runs, DocumentDiffRichRunRole.Inserted));
                return;
            }
            int budget = TakeCells(CjkDiffTokenizer.Tokenize(oldText).ToArray(), CjkDiffTokenizer.Tokenize(newText).ToArray());
            var diff = DocxTextDiffer.Compare(oldText, newText, budget, token);
            if (diff.Truncated)
            {
                Truncated = true;
                CompareParagraph(before, after, coarse: true);
                return;
            }
            var runs = DocxRevisionRuns.Build(diff, before?.Runs ?? [], after?.Runs ?? [], ref _revisionId);
            var grouped = DocxChangeGrouper.Group(runs, location, MaxGroups - _changes.Count, token);
            _rawCount = checked(_rawCount + grouped.Summary.RawRevisionCount);
            Truncated |= grouped.Truncated;
            foreach (var change in grouped.Changes)
            {
                var oldSnippet = Clip(change.Before);
                var newSnippet = Clip(change.After);
                _changes.Add(new(change.ChangeId, SnippetKind(change.Kind, oldSnippet, newSnippet),
                    change.Location, oldSnippet, newSnippet, change.Confidence));
            }
            if (before is not null && after is not null && CanonicalParagraph(before.Element.Element(W + "pPr"))
                != CanonicalParagraph(after.Element.Element(W + "pPr")))
                Add(oldText.Length > 0 && newText.Length > 0 ? DocumentDiffChangeKind.Format : DocumentDiffChangeKind.Other,
                    location, ParagraphFormattingSnippet(before), ParagraphFormattingSnippet(after));
        }

        private DocumentDiffRichSnippet? ParagraphFormattingSnippet(Paragraph paragraph)
        {
            string canonical = CanonicalParagraph(paragraph.Element.Element(W + "pPr"));
            var labels = new Dictionary<string, string>(StringComparer.Ordinal)
            {
                ["jc"] = "对齐", ["ind"] = "缩进", ["spacing"] = "间距",
                ["keepNext"] = "与下段同页", ["keepLines"] = "段内不分页",
                ["widowControl"] = "孤行控制", ["contextualSpacing"] = "同样式段间距",
                ["bidi"] = "从右到左", ["left"] = "左", ["right"] = "右",
                ["start"] = "起点", ["end"] = "终点", ["firstLine"] = "首行",
                ["hanging"] = "悬挂", ["before"] = "段前", ["after"] = "段后",
                ["line"] = "行距", ["lineRule"] = "行距规则", ["val"] = "值",
                ["leftChars"] = "左", ["rightChars"] = "右", ["firstLineChars"] = "首行",
                ["hangingChars"] = "悬挂", ["beforeAutospacing"] = "自动段前间距",
                ["afterAutospacing"] = "自动段后间距",
            };
            var settings = new List<string>();
            if (canonical.Length > 0)
                foreach (XElement property in XElement.Parse(canonical).Elements())
                {
                    string name = property.Name.LocalName;
                    foreach (XAttribute attribute in property.Attributes())
                    {
                        string key = attribute.Name.LocalName;
                        string value = attribute.Value;
                        if (name == "jc") value = value switch
                        {
                            "left" or "start" => "居左", "right" or "end" => "居右",
                            "center" => "居中", "both" => "两端对齐", "distribute" => "分散对齐",
                            _ => value,
                        };
                        else if (name is "ind" or "spacing" && key != "lineRule" && !key.EndsWith("Autospacing", StringComparison.Ordinal))
                        {
                            bool multiple = name == "spacing" && key == "line" &&
                                ((string?)property.Attribute("lineRule") ?? "auto") == "auto";
                            value = (decimal.Parse(value, CultureInfo.InvariantCulture) /
                                (multiple ? 240 : key.EndsWith("Chars", StringComparison.Ordinal) ? 100 : 20))
                                .ToString("0.##", CultureInfo.InvariantCulture) +
                                (multiple ? " 倍" : key.EndsWith("Chars", StringComparison.Ordinal) ? " 字符" : " 磅");
                        }
                        else if (key == "lineRule") value = value switch
                        { "auto" => "自动", "atleast" => "最小值", "exact" => "固定值", _ => value };
                        else if (key == "val" || key.EndsWith("Autospacing", StringComparison.Ordinal)) value = value == "1" ? "是" : "否";
                        settings.Add(labels.GetValueOrDefault(name, name) +
                            (key == "val" ? "" : "·" + labels.GetValueOrDefault(key, key)) + "：" + value);
                    }
                }
            string description = settings.Count == 0 ? "未指定直接段落格式" : string.Join("；", settings);
            return Snippet([new("段落格式：" + description + "\n", null), .. paragraph.Runs],
                DocumentDiffRichRunRole.Changed);
        }

        private int TakeCells(IReadOnlyList<string> before, IReadOnlyList<string> after)
        {
            int prefix = 0;
            while (prefix < before.Count && prefix < after.Count && before[prefix] == after[prefix])
            { token.ThrowIfCancellationRequested(); prefix++; }
            int oldEnd = before.Count, newEnd = after.Count;
            while (oldEnd > prefix && newEnd > prefix && before[oldEnd - 1] == after[newEnd - 1])
            { token.ThrowIfCancellationRequested(); oldEnd--; newEnd--; }
            long needed = oldEnd > prefix && newEnd > prefix ? ((long)oldEnd - prefix + 1) * ((long)newEnd - prefix + 1) : 0;
            int available = _cells;
            _cells -= (int)Math.Min(needed, _cells);
            return available;
        }

        private void Add(DocumentDiffChangeKind kind, DocumentDiffLocation location,
            DocumentDiffRichSnippet? before, DocumentDiffRichSnippet? after)
        {
            _rawCount = checked(_rawCount + (kind == DocumentDiffChangeKind.Replace ? 2 : 1));
            if (_changes.Count == MaxGroups) { Truncated = true; return; }
            _changes.Add(new(Guid.NewGuid(), SnippetKind(kind, before, after), location, before, after, DocumentDiffConfidence.Normalized));
        }

        private static DocumentDiffChangeKind SnippetKind(DocumentDiffChangeKind kind,
            DocumentDiffRichSnippet? before, DocumentDiffRichSnippet? after) => kind switch
        {
            DocumentDiffChangeKind.Insert when after is null => DocumentDiffChangeKind.Other,
            DocumentDiffChangeKind.Delete when before is null => DocumentDiffChangeKind.Other,
            DocumentDiffChangeKind.Format or DocumentDiffChangeKind.Replace when before is null || after is null => DocumentDiffChangeKind.Other,
            _ => kind,
        };

        private DocumentDiffRichSnippet? ShapeSnippet(string text) => text.Length == 0 ? null
            : Snippet([new(text, null)], DocumentDiffRichRunRole.Changed);

        private DocumentDiffRichSnippet? Snippet(IReadOnlyList<DocxStyledText>? runs, DocumentDiffRichRunRole role)
            => runs is null ? null : Clip(DocxRichSnippetBuilder.Build(runs, role));

        private string BoundText(string text, int limit)
        {
            if (text.Length <= limit) return text;
            Truncated = true;
            int length = 0;
            var elements = StringInfo.GetTextElementEnumerator(text);
            while (elements.MoveNext())
            {
                token.ThrowIfCancellationRequested();
                int end = elements.ElementIndex + elements.GetTextElement().Length;
                if (end > limit) break;
                length = end;
            }
            return text[..length];
        }

        private DocumentDiffRichSnippet? Clip(DocumentDiffRichSnippet? snippet)
        {
            if (snippet is null) return null;
            string text = string.Concat(snippet.Runs.Select(r => r.Text));
            int length = BoundText(text, MaxSnippetCharacters).Length;
            if (length == 0) return null;
            var output = new List<DocumentDiffRichRun>();
            foreach (var run in snippet.Runs)
            {
                if (length == 0) break;
                int count = Math.Min(length, run.Text.Length);
                output.Add(new(run.Text[..count], run.Role, run.Bold, run.Italic, run.Underline, run.Strike,
                    run.FontSizePt, run.FontFamily, run.Foreground, run.Background, run.StyleName));
                length -= count;
            }
            return new DocumentDiffRichSnippet(output);
        }

        public DocumentDiffOutcome Result()
        {
            int Count(DocumentDiffChangeKind kind) => _changes.Count(c => c.Kind == kind);
            var summary = new DocumentDiffSummary(_changes.Count, _rawCount, Count(DocumentDiffChangeKind.Insert),
                Count(DocumentDiffChangeKind.Delete), Count(DocumentDiffChangeKind.Replace), 0, Count(DocumentDiffChangeKind.Format),
                Count(DocumentDiffChangeKind.Table), 0, Count(DocumentDiffChangeKind.Other));
            var coverage = new DocumentDiffCoverage([
                new(DocumentDiffCoverageArea.VisibleText, Truncated ? DocumentDiffCoverageStatus.Partial : DocumentDiffCoverageStatus.Covered),
                new(DocumentDiffCoverageArea.Structure, DocumentDiffCoverageStatus.Partial),
                new(DocumentDiffCoverageArea.Formatting, DocumentDiffCoverageStatus.Partial),
                new(DocumentDiffCoverageArea.Tables, DocumentDiffCoverageStatus.Partial),
                new(DocumentDiffCoverageArea.HeadersFooters, Truncated ? DocumentDiffCoverageStatus.Partial : DocumentDiffCoverageStatus.Covered),
                new(DocumentDiffCoverageArea.Notes, DocumentDiffCoverageStatus.NotCovered),
                new(DocumentDiffCoverageArea.TextBoxes, DocumentDiffCoverageStatus.NotCovered),
                new(DocumentDiffCoverageArea.Comments, DocumentDiffCoverageStatus.NotCovered),
                new(DocumentDiffCoverageArea.Images, DocumentDiffCoverageStatus.NotCovered),
                new(DocumentDiffCoverageArea.Fields, DocumentDiffCoverageStatus.NotCovered),
                new(DocumentDiffCoverageArea.Pagination, DocumentDiffCoverageStatus.NotCovered),
            ], Truncated);
            var warnings = new List<DocumentDiffWarning> { DocumentDiffWarning.PartialCoverage };
            if (Truncated) warnings.Add(DocumentDiffWarning.ResultTruncated);
            return DocumentDiffOutcome.Changed with
            {
                Details = new DocumentDiffDetails(DocumentDiffFormat.Docx, _changes.AsReadOnly(), coverage)
                { Summary = summary, Warnings = warnings.AsReadOnly() },
            };
        }
    }

    private sealed record Paragraph(XElement Element, DocumentDiffLocation Location, string Container,
        IReadOnlyList<DocxStyledText> Runs, string Text);

    private static XElement[] Tables(XElement? root) => root?.Descendants(W + "tbl")
        .Where(t => !t.Ancestors(W + "txbxContent").Any()).ToArray() ?? [];

    private static string TableShape(XElement table)
    {
        var shape = new StringBuilder("表格：");
        foreach (XElement row in table.Descendants(W + "tr").Where(r => r.Ancestors(W + "tbl").FirstOrDefault() == table))
        {
            XElement? properties = row.Element(W + "trPr");
            shape.Append("行（前置列").Append(Grid(properties, "gridBefore", "0"))
                .Append("，后置列").Append(Grid(properties, "gridAfter", "0")).Append("）：");
            foreach (XElement cell in row.Descendants(W + "tc").Where(c => c.Ancestors(W + "tr").FirstOrDefault() == row))
            {
                XElement? cellProperties = cell.Element(W + "tcPr");
                string merge = cellProperties?.Element(W + "vMerge") is { } vertical
                    ? (string?)vertical.Attribute(W + "val") ?? "continue" : "none";
                if (merge is not ("none" or "continue" or "restart"))
                    throw new InvalidDataException("Invalid vertical merge value.");
                shape.Append("单元格（跨度").Append(Grid(cellProperties, "gridSpan", "1"))
                    .Append("，合并").Append(merge).Append("）；");
            }
            shape.Append("行结束；");
        }
        return shape.ToString();

        static string Grid(XElement? properties, string name, string fallback) =>
            Integer((string?)properties?.Element(W + name)?.Attribute(W + "val") ?? fallback);
    }
    private static XElement? CanonicalRun(XElement? properties)
    {
        if (properties is null) return null;
        var result = new XElement(W + "rPr");
        foreach (string name in new[] { "b", "i", "strike", "u", "sz", "rFonts", "color", "highlight" })
        {
            XElement? element = properties.Element(W + name);
            if (element is null) continue;
            if (name is "b" or "i" or "strike")
            { result.Add(new XElement(W + name, new XAttribute(W + "val", OnOff((string?)element.Attribute(W + "val"))))); continue; }
            if (name == "rFonts")
            {
                var fonts = new XElement(W + name);
                foreach (string attribute in new[] { "ascii", "hAnsi", "eastAsia", "cs" })
                    if ((string?)element.Attribute(W + attribute) is { Length: > 0 } font) fonts.Add(new XAttribute(W + attribute, font));
                if (fonts.HasAttributes) result.Add(fonts);
                continue;
            }
            string? value = (string?)element.Attribute(W + "val");
            if (name == "u") value ??= "single";
            if (value is null) continue;
            value = name == "sz" ? Integer(value) : value.ToLowerInvariant();
            result.Add(new XElement(W + name, new XAttribute(W + "val", value)));
        }
        return result.HasElements ? result : null;
    }

    private static string CanonicalParagraph(XElement? properties)
    {
        if (properties is null) return "";
        var result = new XElement("properties");
        foreach (string name in new[] { "jc", "ind", "spacing", "keepNext", "keepLines", "widowControl", "contextualSpacing", "bidi" })
        {
            XElement? element = properties.Element(W + name);
            if (element is null) continue;
            var normalized = new XElement(name);
            if (name is not ("jc" or "ind" or "spacing")) normalized.SetAttributeValue("val", OnOff((string?)element.Attribute(W + "val")));
            else
            {
                string[] attributes = name == "jc" ? ["val"] : name == "ind"
                    ? ["left", "right", "start", "end", "firstLine", "hanging", "leftChars", "rightChars", "firstLineChars", "hangingChars"]
                    : ["before", "after", "line", "lineRule", "beforeAutospacing", "afterAutospacing"];
                foreach (string attribute in attributes.Order(StringComparer.Ordinal))
                    if ((string?)element.Attribute(W + attribute) is { } value)
                        normalized.SetAttributeValue(attribute, attribute is "val" or "lineRule" ? value.ToLowerInvariant()
                            : attribute.EndsWith("Autospacing", StringComparison.Ordinal) ? OnOff(value) : Integer(value));
            }
            if (normalized.HasAttributes) result.Add(normalized);
        }
        return result.HasElements ? result.ToString(SaveOptions.DisableFormatting) : "";
    }

    private static string OnOff(string? value) => value?.ToLowerInvariant() switch
    {
        null or "true" or "on" or "1" => "1",
        "false" or "off" or "0" => "0",
        _ => throw new InvalidDataException("Invalid direct on/off formatting."),
    };

    private static string Integer(string value) => long.TryParse(value, NumberStyles.Integer, CultureInfo.InvariantCulture, out long parsed)
        ? parsed.ToString(CultureInfo.InvariantCulture) : throw new InvalidDataException("Invalid direct numeric formatting.");
}
