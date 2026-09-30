using System.Text.Json;
using System.Text.Json.Serialization;
using VibeTable.Workspace.Diff;
using static VibeTable.DocumentDiff.OpenXml.OpenXmlReadSafety;

namespace VibeTable.DocumentDiff.Worker;

internal static class Program
{
    private const int MaxRequestBytes = 32 * 1024;
    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web)
    {
        UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow,
    };

    // No paths are accepted on the command line. The parent sends the bounded
    // request only after assigning this new process to its kill-on-close job.
    private static Task<int> Main(string[] args) => args.Length == 0
        ? RunAsync(Console.OpenStandardInput()) : Task.FromResult(2);

    internal static async Task<int> RunAsync(Stream requestStream)
    {
        try
        {
            using var bounded = new BudgetedEntryStream(requestStream,
                new ExpandedByteBudget(MaxRequestBytes), MaxRequestBytes);
            var request = await JsonSerializer.DeserializeAsync<NormalizeRequest>(bounded, JsonOptions)
                .ConfigureAwait(false);
            if (request is null || request.Version != 1 ||
                string.IsNullOrWhiteSpace(request.OperationDirectory) ||
                !Path.IsPathFullyQualified(request.OperationDirectory))
                return 2;
            string root = Path.GetFullPath(request.OperationDirectory);
            if (!Guid.TryParseExact(new DirectoryInfo(root).Name, "N", out Guid operationId) ||
                operationId == Guid.Empty)
                return 2;
            EnsureDirectory(root);
            EnsureDirectory(Path.Combine(root, "input"));
            EnsureDirectory(Path.Combine(root, "normalized"));
            foreach (string side in new[] { "historical", "effective" })
            {
                string sourcePath = Path.Combine(root, "input", side + ".content");
                string partialPath = Path.Combine(root, "normalized", side + ".final.docx.partial");
                string finalPath = Path.Combine(root, "normalized", side + ".final.docx");
                if ((File.GetAttributes(sourcePath) & (FileAttributes.ReparsePoint | FileAttributes.Directory)) != 0 ||
                    File.Exists(finalPath) || Directory.Exists(finalPath))
                    throw new IOException("Invalid normalization input or existing output.");
                var source = new DocumentContentSource(side + ".docx", null, new FileInfo(sourcePath).Length,
                    _ => ValueTask.FromResult<Stream>(new FileStream(sourcePath, FileMode.Open,
                        FileAccess.Read, FileShare.Read)));
                await using (var derived = new FileStream(partialPath, FileMode.CreateNew,
                                 FileAccess.ReadWrite, FileShare.None))
                    await DocxNormalizer.NormalizeAsync(source, derived).ConfigureAwait(false);
                File.Move(partialPath, finalPath, overwrite: false);
            }
            return 0;
        }
        catch (DiffBudgetExceededException) { return 3; }
        catch (NotSupportedException) { return 4; }
        catch (Exception error) when (error is IOException or InvalidDataException or UnauthorizedAccessException or
            JsonException or ArgumentException or System.Xml.XmlException or
            DocumentFormat.OpenXml.Packaging.OpenXmlPackageException)
        {
            // The host owns the registered partial files. No recursive cleanup
            // and no package text or local paths are returned over this protocol.
            return 2;
        }
    }

    private static void EnsureDirectory(string path)
    {
        for (DirectoryInfo? directory = new(path); directory is not null; directory = directory.Parent)
        {
            if ((directory.Attributes & (FileAttributes.Directory | FileAttributes.ReparsePoint)) != FileAttributes.Directory)
                throw new IOException("Normalization directory is missing or redirected.");
        }
    }

    private sealed record NormalizeRequest(int Version, string OperationDirectory);
}
