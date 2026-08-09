namespace VibeTable.Workspace.Diff;

/// <summary>
/// Internal characterization adapter retained outside the public module surface.
/// </summary>
internal sealed class BinaryDiffAdapter : IDocumentDiffAdapter
{
    public IReadOnlyCollection<string> SupportedMimeTypes { get; } = Array.Empty<string>();

    public IReadOnlyCollection<string> SupportedExtensions { get; } = Array.Empty<string>();

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
            _ => throw new InvalidOperationException($"legacy binary diff failed: {outcome.Failure}"),
        };
    }

    private static DocumentContentSource FileSource(string path)
    {
        return new DocumentContentSource(
            Path.GetFileName(path),
            "application/octet-stream",
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
