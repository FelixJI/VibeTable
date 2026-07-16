namespace VibeTable.Workspace.Diff;

/// <summary>
/// The kind of diff result produced by an adapter.
/// </summary>
public enum DiffSummaryKind
{
    /// <summary>Files are byte-identical.</summary>
    Unchanged,
    /// <summary>Files differ; no content-level detail available.</summary>
    Changed,
    /// <summary>Files differ; content-level details are available (e.g. line diff).</summary>
    ChangedWithDetails,
}

/// <summary>
/// A high-level diff result for two file versions.
/// </summary>
/// <param name="Kind">Whether the files are unchanged, changed, or changed-with-details.</param>
/// <param name="AddedLines">Lines added (when available).</param>
/// <param name="RemovedLines">Lines removed (when available).</param>
/// <param name="Summary">Human-readable summary (e.g. "3 lines added, 1 removed").</param>
public sealed record DiffResult(
    DiffSummaryKind Kind,
    int AddedLines,
    int RemovedLines,
    string Summary
)
{
    public static DiffResult Unchanged { get; } = new(DiffSummaryKind.Unchanged, 0, 0, "unchanged");
}

/// <summary>
/// Interface for format-specific document diff adapters.
///
/// Each adapter knows how to compare two versions of a document in a specific
/// format (text, CSV, JSON, DOCX, XLSX, PPTX, PDF). The core version library
/// depends only on this interface; the OpenXml adapter lives in a separate
/// project so the core never depends on the OpenXml SDK.
/// </summary>
public interface IDocumentDiffAdapter
{
    /// <summary>
    /// The MIME types this adapter handles (e.g. "text/plain", "text/csv").
    /// </summary>
    IReadOnlyCollection<string> SupportedMimeTypes { get; }

    /// <summary>
    /// The file extensions this adapter handles (e.g. ".txt", ".csv").
    /// </summary>
    IReadOnlyCollection<string> SupportedExtensions { get; }

    /// <summary>
    /// Compute a diff between two file paths.
    /// </summary>
    /// <param name="pathA">Path to the first (before) file.</param>
    /// <param name="pathB">Path to the second (after) file.</param>
    DiffResult Diff(string pathA, string pathB);
}
