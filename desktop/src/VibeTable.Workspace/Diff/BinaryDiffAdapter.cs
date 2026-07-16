using System.IO;
using System.Security.Cryptography;

namespace VibeTable.Workspace.Diff;

/// <summary>
/// Hash/size/MIME diff adapter for unknown binary formats.
///
/// All formats get hash-level comparison. Unknown binary types only report
/// changed/unchanged without content-level detail.
/// </summary>
public sealed class BinaryDiffAdapter : IDocumentDiffAdapter
{
    public IReadOnlyCollection<string> SupportedMimeTypes { get; } = Array.Empty<string>();

    public IReadOnlyCollection<string> SupportedExtensions { get; } = Array.Empty<string>();

    public DiffResult Diff(string pathA, string pathB)
    {
        var hashA = ComputeHash(pathA);
        var hashB = ComputeHash(pathB);
        var sizeA = new FileInfo(pathA).Length;
        var sizeB = new FileInfo(pathB).Length;

        if (hashA == hashB)
            return DiffResult.Unchanged;

        return new DiffResult(
            DiffSummaryKind.Changed,
            AddedLines: 0,
            RemovedLines: 0,
            Summary: $"changed (size {sizeA} → {sizeB}, hash {hashA[..8]}.. → {hashB[..8]}..)"
        );
    }

    private static string ComputeHash(string path)
    {
        using var stream = File.OpenRead(path);
        return Convert.ToHexString(SHA256.HashData(stream)).ToLowerInvariant();
    }
}
