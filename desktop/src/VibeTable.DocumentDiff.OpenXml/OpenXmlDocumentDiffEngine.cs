using System.IO.Compression;
using System.Text;
using System.Xml;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml;

using static OpenXmlReadSafety;

internal static class OpenXmlExtractionLimits
{
    internal const long MaxNonSeekablePackageBytes = 64L * 1024 * 1024;
    internal const int MaxPackageEntries = 4_096;
    internal const long MaxExpandedXmlBytes = 64L * 1024 * 1024;
    internal const long MaxXmlPartBytes = 16L * 1024 * 1024;
    internal const int MaxVisibleTextCharacters = 4 * 1024 * 1024;
}

public sealed class OpenXmlDocumentDiffEngine : IDocumentDiffEngine
{
    private const string DocxMime =
        "application/vnd.openxmlformats-officedocument.wordprocessingml.document";
    private const string PptxMime =
        "application/vnd.openxmlformats-officedocument.presentationml.presentation";
    private const string XlsxMime =
        "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet";
    private const string WordNamespace =
        "http://schemas.openxmlformats.org/wordprocessingml/2006/main";
    private const string SpreadsheetNamespace =
        "http://schemas.openxmlformats.org/spreadsheetml/2006/main";
    private const string DrawingNamespace =
        "http://schemas.openxmlformats.org/drawingml/2006/main";

    private readonly DocumentDiffEngine _core = new();

    public async Task<DocumentDiffOutcome> CompareAsync(
        DocumentDiffRequest request,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(request);
        var beforeFormat = Classify(request.Before);
        var afterFormat = Classify(request.After);
        if (beforeFormat == OpenXmlFormat.None && afterFormat == OpenXmlFormat.None)
        {
            return await _core.CompareAsync(request, cancellationToken).ConfigureAwait(false);
        }

        if (beforeFormat == OpenXmlFormat.None || beforeFormat != afterFormat)
        {
            return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.Unsupported);
        }

        var binaryOutcome = await _core.CompareAsync(request, cancellationToken).ConfigureAwait(false);
        if (binaryOutcome.Kind != DocumentDiffOutcomeKind.Changed)
        {
            return binaryOutcome;
        }

        try
        {
            var beforeText = await ExtractVisibleTextAsync(
                beforeFormat,
                request.Before,
                cancellationToken).ConfigureAwait(false);
            var afterText = await ExtractVisibleTextAsync(
                afterFormat,
                request.After,
                cancellationToken).ConfigureAwait(false);
            var textOutcome = await _core.CompareAsync(
                new DocumentDiffRequest(TextSource(beforeText), TextSource(afterText)),
                cancellationToken).ConfigureAwait(false);
            return textOutcome.Kind == DocumentDiffOutcomeKind.Identical
                ? DocumentDiffOutcome.Changed
                : textOutcome;
        }
        catch (OperationCanceledException)
        {
            return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.Cancelled);
        }
        catch (InvalidDataException)
        {
            return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.InvalidContent);
        }
        catch (XmlException)
        {
            return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.InvalidContent);
        }
        catch (IOException)
        {
            return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.Io);
        }
        catch (UnauthorizedAccessException)
        {
            return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.Io);
        }
        catch (DiffBudgetExceededException)
        {
            return DocumentDiffOutcome.Changed;
        }
    }

    private static Task<string> ExtractVisibleTextAsync(
        OpenXmlFormat format,
        DocumentContentSource source,
        CancellationToken cancellationToken)
    {
        return format switch
        {
            OpenXmlFormat.Docx => ExtractDocxTextAsync(source, cancellationToken),
            OpenXmlFormat.Xlsx => ExtractXlsxTextAsync(source, cancellationToken),
            OpenXmlFormat.Pptx => ExtractPptxTextAsync(source, cancellationToken),
            _ => Task.FromException<string>(new NotSupportedException()),
        };
    }

    private static OpenXmlFormat Classify(DocumentContentSource source)
    {
        if (source.MimeType is not null)
        {
            if (source.MimeType.Equals(DocxMime, StringComparison.OrdinalIgnoreCase))
            {
                return OpenXmlFormat.Docx;
            }

            if (source.MimeType.Equals(XlsxMime, StringComparison.OrdinalIgnoreCase))
            {
                return OpenXmlFormat.Xlsx;
            }

            if (source.MimeType.Equals(PptxMime, StringComparison.OrdinalIgnoreCase))
            {
                return OpenXmlFormat.Pptx;
            }
        }

        return Path.GetExtension(source.Name).ToLowerInvariant() switch
        {
            ".docx" => OpenXmlFormat.Docx,
            ".xlsx" => OpenXmlFormat.Xlsx,
            ".pptx" => OpenXmlFormat.Pptx,
            _ => OpenXmlFormat.None,
        };
    }

    private static async Task<string> ExtractDocxTextAsync(
        DocumentContentSource source,
        CancellationToken cancellationToken)
    {
        await using var sourceStream = await source.OpenReadAsync(cancellationToken)
            .ConfigureAwait(false);
        await using var seekable = await EnsureSeekableAsync(source, sourceStream, cancellationToken)
            .ConfigureAwait(false);
        using var archive = new ZipArchive(seekable, ZipArchiveMode.Read, leaveOpen: true);
        var expandedBudget = ValidateArchive(archive);
        var entries = archive.Entries
            .Where(entry => IsWordTextPart(entry.FullName))
            .OrderBy(entry => entry.FullName, StringComparer.Ordinal)
            .ToArray();
        if (entries.Length == 0)
        {
            throw new InvalidDataException();
        }

        var visibleBudget = new VisibleTextBudget();
        var output = new StringBuilder();
        foreach (var entry in entries)
        {
            cancellationToken.ThrowIfCancellationRequested();
            using var entryStream = OpenBoundedEntry(entry, expandedBudget);
            using var reader = XmlReader.Create(entryStream, SecureXmlSettings());
            StringBuilder? paragraph = null;
            while (reader.Read())
            {
                cancellationToken.ThrowIfCancellationRequested();
                if (reader.NodeType == XmlNodeType.Element
                    && reader.LocalName == "p"
                    && reader.NamespaceURI == WordNamespace)
                {
                    paragraph = new StringBuilder();
                }
                else if (paragraph is not null
                    && reader.NodeType == XmlNodeType.Element
                    && reader.LocalName == "t"
                    && reader.NamespaceURI == WordNamespace)
                {
                    AppendElementText(reader, paragraph, visibleBudget);
                }
                else if (paragraph is not null
                    && reader.NodeType == XmlNodeType.EndElement
                    && reader.LocalName == "p"
                    && reader.NamespaceURI == WordNamespace)
                {
                    AppendVisibleLine(
                        output,
                        paragraph.ToString(),
                        visibleBudget,
                        countValue: false);
                    paragraph = null;
                }
            }
        }

        return output.ToString();
    }

    private static bool IsWordTextPart(string name)
    {
        return name.Equals("word/document.xml", StringComparison.OrdinalIgnoreCase)
            || name.StartsWith("word/header", StringComparison.OrdinalIgnoreCase)
            || name.StartsWith("word/footer", StringComparison.OrdinalIgnoreCase);
    }

    private static async Task<string> ExtractXlsxTextAsync(
        DocumentContentSource source,
        CancellationToken cancellationToken)
    {
        await using var sourceStream = await source.OpenReadAsync(cancellationToken)
            .ConfigureAwait(false);
        await using var seekable = await EnsureSeekableAsync(source, sourceStream, cancellationToken)
            .ConfigureAwait(false);
        using var archive = new ZipArchive(seekable, ZipArchiveMode.Read, leaveOpen: true);
        var expandedBudget = ValidateArchive(archive);
        var sharedStringsEntry = archive.GetEntry("xl/sharedStrings.xml");
        var sharedStrings = sharedStringsEntry is null
            ? []
            : ReadSharedStrings(
                sharedStringsEntry,
                expandedBudget,
                cancellationToken);
        var worksheets = archive.Entries
            .Where(entry => entry.FullName.StartsWith(
                "xl/worksheets/",
                StringComparison.OrdinalIgnoreCase))
            .Where(entry => entry.FullName.EndsWith(".xml", StringComparison.OrdinalIgnoreCase))
            .OrderBy(entry => entry.FullName, StringComparer.Ordinal)
            .ToArray();
        if (worksheets.Length == 0)
        {
            throw new InvalidDataException();
        }

        var cellBudget = new VisibleTextBudget();
        var outputBudget = new VisibleTextBudget();
        var cells = new StringBuilder();
        foreach (var worksheet in worksheets)
        {
            cancellationToken.ThrowIfCancellationRequested();
            using var entryStream = OpenBoundedEntry(worksheet, expandedBudget);
            using var reader = XmlReader.Create(entryStream, SecureXmlSettings());
            while (reader.Read())
            {
                cancellationToken.ThrowIfCancellationRequested();
                if (reader.NodeType != XmlNodeType.Element
                    || reader.LocalName != "c"
                    || reader.NamespaceURI != SpreadsheetNamespace)
                {
                    continue;
                }

                var value = ReadCell(
                    reader,
                    sharedStrings,
                    cellBudget,
                    cancellationToken);
                if (value is not null)
                {
                    AppendVisibleLine(cells, value, outputBudget);
                }
            }
        }

        return cells.ToString();
    }

    private static List<string> ReadSharedStrings(
        ZipArchiveEntry entry,
        ExpandedByteBudget expandedBudget,
        CancellationToken cancellationToken)
    {
        using var entryStream = OpenBoundedEntry(entry, expandedBudget);
        using var reader = XmlReader.Create(entryStream, SecureXmlSettings());
        var sharedStrings = new List<string>();
        var visibleBudget = new VisibleTextBudget();
        while (reader.Read())
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (reader.NodeType != XmlNodeType.Element
                || reader.LocalName != "si"
                || reader.NamespaceURI != SpreadsheetNamespace)
            {
                continue;
            }

            using var itemReader = reader.ReadSubtree();
            var text = new StringBuilder();
            while (itemReader.Read())
            {
                cancellationToken.ThrowIfCancellationRequested();
                if (itemReader.NodeType == XmlNodeType.Element
                    && itemReader.LocalName == "t"
                    && itemReader.NamespaceURI == SpreadsheetNamespace)
                {
                    AppendElementText(itemReader, text, visibleBudget);
                }
            }

            sharedStrings.Add(text.ToString());
        }

        return sharedStrings;
    }

    private static string? ReadCell(
        XmlReader cellReader,
        IReadOnlyList<string> sharedStrings,
        VisibleTextBudget visibleBudget,
        CancellationToken cancellationToken)
    {
        var cellType = cellReader.GetAttribute("t");
        using var subtree = cellReader.ReadSubtree();
        string? rawValue = null;
        var inlineText = new StringBuilder();
        while (subtree.Read())
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (subtree.NodeType != XmlNodeType.Element
                || subtree.NamespaceURI != SpreadsheetNamespace)
            {
                continue;
            }

            if (subtree.LocalName == "v")
            {
                var value = new StringBuilder();
                AppendElementText(subtree, value, visibleBudget);
                rawValue = value.ToString();
            }
            else if (subtree.LocalName == "t")
            {
                AppendElementText(subtree, inlineText, visibleBudget);
            }
        }

        if (cellType == "s")
        {
            if (!int.TryParse(rawValue, out var index)
                || index < 0
                || index >= sharedStrings.Count)
            {
                throw new InvalidDataException();
            }

            return sharedStrings[index];
        }

        if (cellType == "inlineStr")
        {
            return inlineText.ToString();
        }

        return rawValue;
    }

    private static async Task<string> ExtractPptxTextAsync(
        DocumentContentSource source,
        CancellationToken cancellationToken)
    {
        await using var sourceStream = await source.OpenReadAsync(cancellationToken)
            .ConfigureAwait(false);
        await using var seekable = await EnsureSeekableAsync(source, sourceStream, cancellationToken)
            .ConfigureAwait(false);
        using var archive = new ZipArchive(seekable, ZipArchiveMode.Read, leaveOpen: true);
        var expandedBudget = ValidateArchive(archive);
        var entries = archive.Entries
            .Where(entry => entry.FullName.StartsWith(
                    "ppt/slides/",
                    StringComparison.OrdinalIgnoreCase)
                || entry.FullName.StartsWith(
                    "ppt/notesSlides/",
                    StringComparison.OrdinalIgnoreCase))
            .Where(entry => entry.FullName.EndsWith(".xml", StringComparison.OrdinalIgnoreCase))
            .OrderBy(entry => entry.FullName, StringComparer.Ordinal)
            .ToArray();
        if (entries.Length == 0)
        {
            throw new InvalidDataException();
        }

        var visibleBudget = new VisibleTextBudget();
        var output = new StringBuilder();
        foreach (var entry in entries)
        {
            cancellationToken.ThrowIfCancellationRequested();
            using var entryStream = OpenBoundedEntry(entry, expandedBudget);
            using var reader = XmlReader.Create(entryStream, SecureXmlSettings());
            StringBuilder? paragraph = null;
            while (reader.Read())
            {
                cancellationToken.ThrowIfCancellationRequested();
                if (reader.NodeType == XmlNodeType.Element
                    && reader.LocalName == "p"
                    && reader.NamespaceURI == DrawingNamespace)
                {
                    paragraph = new StringBuilder();
                }
                else if (paragraph is not null
                    && reader.NodeType == XmlNodeType.Element
                    && reader.LocalName == "t"
                    && reader.NamespaceURI == DrawingNamespace)
                {
                    AppendElementText(reader, paragraph, visibleBudget);
                }
                else if (paragraph is not null
                    && reader.NodeType == XmlNodeType.EndElement
                    && reader.LocalName == "p"
                    && reader.NamespaceURI == DrawingNamespace)
                {
                    AppendVisibleLine(
                        output,
                        paragraph.ToString(),
                        visibleBudget,
                        countValue: false);
                    paragraph = null;
                }
            }
        }

        return output.ToString();
    }

    private static void AppendElementText(
        XmlReader reader,
        StringBuilder target,
        VisibleTextBudget budget)
    {
        if (reader.IsEmptyElement)
        {
            return;
        }
        int depth = reader.Depth;
        while (reader.Read())
        {
            if (reader.NodeType == XmlNodeType.EndElement && reader.Depth == depth)
            {
                return;
            }
            if (reader.NodeType is not (
                    XmlNodeType.Text or
                    XmlNodeType.CDATA or
                    XmlNodeType.Whitespace or
                    XmlNodeType.SignificantWhitespace))
            {
                continue;
            }
            string value = reader.Value;
            budget.Consume(value.Length);
            target.Append(value);
        }
        throw new XmlException("Visible text element was not closed.");
    }

    private static void AppendVisibleLine(
        StringBuilder output,
        string value,
        VisibleTextBudget budget,
        bool countValue = true)
    {
        if (output.Length > 0)
        {
            budget.Consume(1);
            output.Append('\n');
        }
        if (countValue)
        {
            budget.Consume(value.Length);
        }
        output.Append(value);
    }

    private static DocumentContentSource TextSource(string text)
    {
        var bytes = Encoding.UTF8.GetBytes(text);
        return new DocumentContentSource(
            "openxml-visible-text.txt",
            "text/plain",
            bytes.Length,
            _ => ValueTask.FromResult<Stream>(new MemoryStream(bytes, writable: false)));
    }

    private enum OpenXmlFormat
    {
        None,
        Docx,
        Xlsx,
        Pptx,
    }

    private sealed class VisibleTextBudget
    {
        private int _consumed;

        public void Consume(int count)
        {
            _consumed = checked(_consumed + count);
            if (_consumed > OpenXmlExtractionLimits.MaxVisibleTextCharacters)
            {
                throw new DiffBudgetExceededException();
            }
        }
    }

}
