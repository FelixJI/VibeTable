using System.Text.Json;
using VibeTable.DocumentDiff.OpenXml;
using VibeTable.Workspace.Diff;
using static VibeTable.DocumentDiff.OpenXml.OpenXmlReadSafety;

namespace VibeTable.DocumentDiff.Worker;

internal static class DocxComparisonWriter
{
    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web);

    public static async Task WriteAsync(string root, bool existingRevisions)
    {
        DocumentContentSource Source(string side)
        {
            string path = Path.Combine(root, "normalized", side + ".final.docx");
            return new DocumentContentSource(side + ".docx", null, new FileInfo(path).Length,
                _ => ValueTask.FromResult<Stream>(new FileStream(path, FileMode.Open,
                    FileAccess.Read, FileShare.Read)));
        }
        DocumentDiffOutcome outcome = await DocxSemanticDiff.CompareAsync(
            new DocumentDiffRequest(Source("historical"), Source("effective")), CancellationToken.None)
            .ConfigureAwait(false);
        DocumentDiffDetails details = outcome.Details
            ?? throw new InvalidDataException("DOCX comparison did not return bounded details.");
        DocumentDiffSummary summary = details.Summary
            ?? throw new InvalidDataException("DOCX comparison did not return a summary.");
        if (details.Changes.Count > 20_000 || summary.TotalChangeGroups != details.Changes.Count)
            throw new DiffBudgetExceededException();
        string indexPartial = Path.Combine(root, "index", "changes.jsonl.partial");
        await using (var index = new FileStream(indexPartial, FileMode.CreateNew, FileAccess.Write, FileShare.None))
        {
            foreach (DocumentDiffChange change in details.Changes)
            {
                byte[] line = JsonSerializer.SerializeToUtf8Bytes(change, JsonOptions);
                if (line.Length + 1 > 64 * 1024 - 1024 || index.Position + line.Length + 1 > 64L * 1024 * 1024)
                    throw new DiffBudgetExceededException();
                await index.WriteAsync(line).ConfigureAwait(false);
                await index.WriteAsync("\n"u8.ToArray()).ConfigureAwait(false);
            }
        }
        File.Move(indexPartial, Path.Combine(root, "index", "changes.jsonl"), overwrite: false);
        var warnings = details.Warnings.ToList();
        if (existingRevisions && !warnings.Contains(DocumentDiffWarning.ExistingRevisionsNormalized))
            warnings.Add(DocumentDiffWarning.ExistingRevisionsNormalized);
        var result = new DocumentDiffWorkerResult(2, summary, details.Coverage, warnings);
        byte[] metadata = JsonSerializer.SerializeToUtf8Bytes(result, JsonOptions);
        if (metadata.Length > 256 * 1024) throw new DiffBudgetExceededException();
        string metadataPartial = Path.Combine(root, "index", "result.json.partial");
        await using (var output = new FileStream(metadataPartial, FileMode.CreateNew, FileAccess.Write, FileShare.None))
            await output.WriteAsync(metadata).ConfigureAwait(false);
        File.Move(metadataPartial, Path.Combine(root, "index", "result.json"), overwrite: false);
    }
}