using System.IO;

namespace VibeTable.Workspace.Diff;

/// <summary>
/// Line-level diff adapter for plain text, CSV and JSON files.
///
/// Uses a simple longest-common-subsequence (LCS) algorithm to compute
/// added/removed lines. This is intentionally lightweight (no third-party
/// dependency) — the OpenXml adapter handles rich document formats in a
/// separate project.
/// </summary>
public sealed class TextDiffAdapter : IDocumentDiffAdapter
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
        var linesA = File.ReadAllLines(pathA);
        var linesB = File.ReadAllLines(pathB);

        // Quick check: identical content.
        if (linesA.Length == linesB.Length)
        {
            bool identical = true;
            for (int i = 0; i < linesA.Length; i++)
            {
                if (linesA[i] != linesB[i])
                {
                    identical = false;
                    break;
                }
            }
            if (identical)
                return DiffResult.Unchanged;
        }

        // LCS-based line diff.
        var (added, removed) = ComputeLineDiff(linesA, linesB);
        return new DiffResult(
            DiffSummaryKind.ChangedWithDetails,
            AddedLines: added,
            RemovedLines: removed,
            Summary: $"{added} line(s) added, {removed} line(s) removed"
        );
    }

    /// <summary>
    /// Compute the number of added and removed lines using LCS.
    /// </summary>
    private static (int added, int removed) ComputeLineDiff(string[] a, string[] b)
    {
        var lcs = LongestCommonSubsequence(a, b);
        int removed = a.Length - lcs;
        int added = b.Length - lcs;
        return (added, removed);
    }

    /// <summary>
    /// Compute the length of the longest common subsequence of two line arrays.
    /// </summary>
    private static int LongestCommonSubsequence(string[] a, string[] b)
    {
        int m = a.Length;
        int n = b.Length;
        // Use a single-row DP to save memory.
        var prev = new int[n + 1];
        var curr = new int[n + 1];

        for (int i = 1; i <= m; i++)
        {
            for (int j = 1; j <= n; j++)
            {
                if (a[i - 1] == b[j - 1])
                    curr[j] = prev[j - 1] + 1;
                else
                    curr[j] = Math.Max(prev[j], curr[j - 1]);
            }
            (prev, curr) = (curr, prev);
            Array.Clear(curr);
        }

        return prev[n];
    }
}
