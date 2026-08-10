namespace VibeTable.Workspace.Diff;

/// <summary>
/// Line-level diff adapter for plain text, CSV and JSON files.
///
/// Retained only as an internal characterization adapter.
/// </summary>
internal sealed class TextDiffAdapter : IDocumentDiffAdapter
{
    public IReadOnlyCollection<string> SupportedMimeTypes { get; } = new[]
    {
        "text/plain",
        "text/csv",
        "application/json",
        "text/markdown",
        "text/html",
    };

    public IReadOnlyCollection<string> SupportedExtensions { get; } = new[]
    {
        ".txt", ".csv", ".json", ".md", ".html", ".log", ".xml", ".yaml", ".yml",
    };

    public DiffResult Diff(string pathA, string pathB)
    {
        var outcome = new DocumentDiffEngine().CompareAsync(
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
            _ => throw new InvalidOperationException($"legacy text diff failed: {outcome.Failure}"),
        };
    }

    private static DocumentContentSource FileSource(string path)
    {
        return new DocumentContentSource(
            Path.GetFileName(path),
            "text/plain",
            new FileInfo(path).Length,
            _ => ValueTask.FromResult<Stream>(new FileStream(
                path,
                FileMode.Open,
                FileAccess.Read,
                FileShare.Read,
                bufferSize: 64 * 1024,
                FileOptions.Asynchronous | FileOptions.SequentialScan)));
    }
}
