using System.Globalization;
using System.Text;
using System.Xml.Linq;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml;

internal sealed record DocxLocatedParagraph(XElement Paragraph, DocumentDiffLocation Location);
internal sealed record DocxStructureIndex(IReadOnlyList<DocxLocatedParagraph> Paragraphs, bool Truncated);

internal static class DocxStructuralLocator
{
    private static readonly XNamespace W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";

    // One normalized part at a time. Package relationship resolution supplies the
    // section for headers/footers; text boxes are indexed separately, never as body text.
    public static DocxStructureIndex Index(XElement root, DocumentDiffPart part,
        int maxParagraphs, int sectionIndex = 0, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(root);
        ArgumentOutOfRangeException.ThrowIfNegative(maxParagraphs);
        ArgumentOutOfRangeException.ThrowIfNegative(sectionIndex);
        if (!Enum.IsDefined(part) || part == DocumentDiffPart.Worksheet)
            throw new ArgumentOutOfRangeException(nameof(part));
        var result = new List<DocxLocatedParagraph>();
        var tables = new Dictionary<XElement, int>();
        var rowCounts = new Dictionary<XElement, int>();
        var rows = new Dictionary<XElement, int>();
        var columns = new Dictionary<XElement, int>();
        var cells = new Dictionary<XElement, int>();
        string? heading = null;
        foreach (XElement element in Walk(root, 0))
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (element.Name == W + "tbl")
            {
                tables.Add(element, tables.Count);
                rowCounts.Add(element, 0);
            }
            else if (element.Name == W + "tr" && Nearest(element, "tbl") is { } table)
            {
                rows.Add(element, rowCounts[table]++);
                columns.Add(element, GridNumber(element.Element(W + "trPr")?.Element(W + "gridBefore"), 0));
            }
            else if (element.Name == W + "tc" && Nearest(element, "tr") is { } row)
            {
                cells.Add(element, columns[row]);
                int span = GridNumber(element.Element(W + "tcPr")?.Element(W + "gridSpan"), 1);
                if (span < 1)
                    throw new InvalidDataException("Cell grid span must be positive.");
                columns[row] = checked(columns[row] + span);
            }
            else if (element.Name == W + "p")
            {
                if (result.Count == maxParagraphs)
                    return new DocxStructureIndex(result.AsReadOnly(), true);
                XElement? properties = element.Element(W + "pPr");
                if (IsHeading(properties))
                {
                    string text = HeadingText(element);
                    if (!string.IsNullOrWhiteSpace(text))
                        heading = text;
                }
                XElement? cell = Nearest(element, "tc");
                XElement? cellTable = cell is null ? null : Nearest(cell, "tbl");
                XElement? cellRow = cell is null ? null : Nearest(cell, "tr");
                result.Add(new DocxLocatedParagraph(element, new DocumentDiffLocation(part,
                    sectionIndex: sectionIndex, paragraphIndex: result.Count, nearestHeading: heading,
                    tableIndex: cellTable is null ? null : tables[cellTable],
                    rowIndex: cellRow is null ? null : rows[cellRow],
                    columnIndex: cell is null ? null : cells[cell])));
                if (part == DocumentDiffPart.Body && properties?.Element(W + "sectPr") is not null)
                    sectionIndex = checked(sectionIndex + 1);
            }
        }
        return new DocxStructureIndex(result.AsReadOnly(), false);

        XElement? Nearest(XElement element, string name) => element.Ancestors()
            .TakeWhile(ancestor => ancestor != root.Parent).FirstOrDefault(ancestor => ancestor.Name == W + name);

        string HeadingText(XElement paragraph)
        {
            var text = new StringBuilder();
            foreach (XElement child in Walk(paragraph, 0))
            {
                if (child.Name != W + "t" || Nearest(child, "p") != paragraph)
                    continue;
                string value = child.Value;
                if (value.Length > OpenXmlExtractionLimits.MaxVisibleTextCharacters - text.Length)
                    throw new InvalidDataException("Heading exceeds the visible text budget.");
                text.Append(value);
            }
            return text.ToString();
        }

        IEnumerable<XElement> Walk(XElement element, int depth)
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (depth > 64)
                throw new InvalidDataException("Document structure is nested too deeply.");
            if (element != root && element.Name == W + "txbxContent")
                yield break;
            yield return element;
            foreach (XElement child in element.Elements())
                foreach (XElement descendant in Walk(child, depth + 1))
                    yield return descendant;
        }
    }

    private static int GridNumber(XElement? element, int fallback)
    {
        if (element is null)
            return fallback;
        if (!int.TryParse((string?)element.Attribute(W + "val"), NumberStyles.None,
            CultureInfo.InvariantCulture, out int value))
            throw new InvalidDataException("Invalid table grid coordinate.");
        return value;
    }

    private static bool IsHeading(XElement? properties)
    {
        string? level = (string?)properties?.Element(W + "outlineLvl")?.Attribute(W + "val");
        if (level is not null)
            return int.TryParse(level, out int outline) && outline is >= 0 and <= 8;
        string? style = (string?)properties?.Element(W + "pStyle")?.Attribute(W + "val");
        return style is { Length: 8 } && style.StartsWith("Heading", StringComparison.OrdinalIgnoreCase)
            && style[7] is >= '1' and <= '9';
    }
}
