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
public sealed class OpenXmlDiffAdapter : IDocumentDiffAdapter
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
        // Extract text lines from both Open XML packages.
        var linesA = ExtractTextLines(pathA);
        var linesB = ExtractTextLines(pathB);

        if (linesA.Count == 0 && linesB.Count == 0)
        {
            // Fall back to binary comparison if no text was extracted.
            var binAdapter = new BinaryDiffAdapter();
            return binAdapter.Diff(pathA, pathB);
        }

        // Use the TextDiffAdapter's LCS algorithm for line diff.
        var textAdapter = new TextDiffAdapter();
        var tempA = Path.GetTempFileName();
        var tempB = Path.GetTempFileName();
        try
        {
            File.WriteAllLines(tempA, linesA);
            File.WriteAllLines(tempB, linesB);
            return textAdapter.Diff(tempA, tempB);
        }
        finally
        {
            try { File.Delete(tempA); } catch { }
            try { File.Delete(tempB); } catch { }
        }
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
