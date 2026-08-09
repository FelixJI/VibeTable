using System.IO;
using System.IO.Compression;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml;

/// <summary>
/// Diff adapter for Open XML formats (DOCX, XLSX, PPTX).
///
/// Open XML packages are ZIP archives containing XML parts. This adapter
/// extracts and compares the text-bearing XML parts (document.xml,
/// sheet1.xml, slide1.xml etc.) using line-level diff. It does NOT require
/// the DocumentFormat.OpenXml SDK — it reads the raw XML from the ZIP
/// archive and diffs the text content.
///
/// The adapter reports paragraph/worksheet/slide-level changes as line
/// additions/removals. Full structural diff (tables, styles, comments) is
/// a future enhancement; the hash comparison always catches structural
/// changes even when line-level detail is not available.
/// </summary>
internal sealed class OpenXmlDiffAdapter : IDocumentDiffAdapter
{
    public IReadOnlyCollection<string> SupportedMimeTypes { get; } = new[]
    {
        "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        "application/vnd.openxmlformats-officedocument.presentationml.presentation",
    };

    public IReadOnlyCollection<string> SupportedExtensions { get; } = new[]
    {
        ".docx", ".xlsx", ".pptx",
    };

    public DiffResult Diff(string pathA, string pathB)
    {
        var outcome = new OpenXmlDocumentDiffEngine().CompareAsync(
            new DocumentDiffRequest(FileSource(pathA), FileSource(pathB)),
            CancellationToken.None).GetAwaiter().GetResult();
        return outcome.Kind switch
        {
            DocumentDiffOutcomeKind.Identical => DiffResult.Unchanged,
            DocumentDiffOutcomeKind.Changed => new DiffResult(
                DiffSummaryKind.Changed,
                AddedLines: 0,
                RemovedLines: 0,
                Summary: "changed"),
            DocumentDiffOutcomeKind.ChangedWithDetails => new DiffResult(
                DiffSummaryKind.ChangedWithDetails,
                AddedLines: outcome.AddedLines ?? 0,
                RemovedLines: outcome.RemovedLines ?? 0,
                Summary: $"{outcome.AddedLines ?? 0} line(s) added, "
                    + $"{outcome.RemovedLines ?? 0} line(s) removed"),
            _ => throw new InvalidOperationException($"legacy OpenXml diff failed: {outcome.Failure}"),
        };
    }

    private static DocumentContentSource FileSource(string path)
    {
        return new DocumentContentSource(
            Path.GetFileName(path),
            MimeType(path),
            new FileInfo(path).Length,
            _ => ValueTask.FromResult<Stream>(new FileStream(
                path,
                FileMode.Open,
                FileAccess.Read,
                FileShare.Read,
                bufferSize: 64 * 1024,
                FileOptions.Asynchronous | FileOptions.SequentialScan)));
    }

    private static string? MimeType(string path)
    {
        return Path.GetExtension(path).ToLowerInvariant() switch
        {
            ".docx" => "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
            ".xlsx" => "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
            ".pptx" => "application/vnd.openxmlformats-officedocument.presentationml.presentation",
            _ => null,
        };
    }

    /// <summary>
    /// Extract text-bearing XML parts from an Open XML package and return
    /// their text content as lines for line-level diffing.
    /// </summary>
    private static List<string> ExtractTextLines(string path)
    {
        var result = new List<string>();
        try
        {
            using var archive = ZipFile.OpenRead(path);
            // Look for word/document.xml (DOCX), xl/worksheets/*.xml (XLSX),
            // ppt/slides/*.xml (PPTX).
            foreach (var entry in archive.Entries)
            {
                if (IsTextBearingPart(entry.FullName))
                {
                    using var reader = new StreamReader(entry.Open());
                    var content = reader.ReadToEnd();
                    // Split XML content into lines for diffing.
                    var lines = content.Split(['\n', '\r'], StringSplitOptions.RemoveEmptyEntries);
                    result.AddRange(lines);
                }
            }
        }
        catch
        {
            // If the file is not a valid ZIP/Open XML, return empty — the
            // caller falls back to binary comparison.
        }
        return result;
    }

    /// <summary>
    /// Returns true if the ZIP entry name represents a text-bearing XML part.
    /// </summary>
    private static bool IsTextBearingPart(string entryName)
    {
        return entryName.StartsWith("word/document.xml", StringComparison.OrdinalIgnoreCase)
            || entryName.StartsWith("word/header", StringComparison.OrdinalIgnoreCase)
            || entryName.StartsWith("word/footer", StringComparison.OrdinalIgnoreCase)
            || (entryName.StartsWith("xl/worksheets/", StringComparison.OrdinalIgnoreCase)
                && entryName.EndsWith(".xml", StringComparison.OrdinalIgnoreCase))
            || (entryName.StartsWith("ppt/slides/", StringComparison.OrdinalIgnoreCase)
                && entryName.EndsWith(".xml", StringComparison.OrdinalIgnoreCase))
            || entryName.StartsWith("ppt/notesSlides/", StringComparison.OrdinalIgnoreCase);
    }
}
