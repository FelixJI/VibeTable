using System.IO;
using System.Runtime.Versioning;
using System.Security;

namespace VibeTable.Workspace.Storage;

/// <summary>
/// Validates and normalizes all internal paths used by the workspace version kernel.
///
/// Invariants enforced:
/// <list type="bullet">
/// <item>All internal paths must be normalized relative paths.</item>
/// <item>Absolute paths, <c>..</c> traversal, ADS (NTFS alternate data streams),
/// device paths and symlinks/reparse points escaping the workspace root are rejected.</item>
/// <item>The workspace root must be a real directory (not a reparse point).</item>
/// </list>
/// </summary>
public static class WorkspacePathGuard
{
    /// <summary>
    /// Characters that indicate an NTFS Alternate Data Stream reference (e.g.
    /// <c>file.txt:Zone.Identifier</c>). These are rejected to prevent bypassing
    /// the path guard.
    /// </summary>
    private static readonly char[] AdsSeparators = [':'];

    private static readonly HashSet<string> ReservedDeviceNames = new(
        StringComparer.OrdinalIgnoreCase)
    {
        "CON", "PRN", "AUX", "NUL", "CLOCK$",
        "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
        "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
    };

    /// <summary>
    /// Validate a relative path and return it normalized with forward slashes.
    /// Throws <see cref="InvalidOperationException"/> if the path is unsafe.
    /// </summary>
    public static string ValidateRelativePath(string relativePath)
    {
        if (string.IsNullOrWhiteSpace(relativePath))
            throw new InvalidOperationException("relative path is empty");

        // Reject absolute paths.
        if (Path.IsPathRooted(relativePath))
            throw new InvalidOperationException(
                $"absolute paths are not allowed: {relativePath}");

        // Reject NTFS ADS (e.g. "file.txt:stream").
        if (relativePath.IndexOfAny(AdsSeparators) >= 0)
            throw new InvalidOperationException(
                $"NTFS alternate data streams are not allowed: {relativePath}");

        // Reject device paths (e.g. \\.\, \\?\, COM1).
        if (relativePath.StartsWith(@"\\.", StringComparison.Ordinal) ||
            relativePath.StartsWith(@"\\?", StringComparison.Ordinal))
            throw new InvalidOperationException(
                $"device paths are not allowed: {relativePath}");

        // Normalize separators to forward-slash for cross-platform consistency.
        var normalized = relativePath.Replace('\\', '/');

        // Reject path traversal.
        var segments = normalized.Split('/');
        foreach (var segment in segments)
        {
            if (segment is "." or "..")
                throw new InvalidOperationException(
                    $"dot path segments are not allowed: {relativePath}");
            // Reject empty segments from doubled separators or leading/trailing slash.
            if (segment.Length == 0 && segments.Length > 1)
                throw new InvalidOperationException(
                    $"path has empty segment: {relativePath}");
            if (segment.EndsWith(' ') || segment.EndsWith('.'))
                throw new InvalidOperationException(
                    $"path segments may not end with a space or dot: {relativePath}");
            if (segment.Any(char.IsControl))
                throw new InvalidOperationException(
                    $"path contains control characters: {relativePath}");

            // Windows resolves device names even when an extension is present
            // (for example NUL.txt). Reject them in every path segment.
            string deviceCandidate = Path.GetFileNameWithoutExtension(segment);
            if (ReservedDeviceNames.Contains(deviceCandidate))
                throw new InvalidOperationException(
                    $"reserved device names are not allowed: {relativePath}");
        }

        return normalized;
    }

    /// <summary>
    /// Resolve a validated relative path against the workspace root and verify the
    /// result is still inside the root. Also rejects reparse points (symlinks/junctions)
    /// that would escape.
    /// </summary>
    [SupportedOSPlatform("windows")]
    public static string ResolveAndCheck(string workspaceRoot, string relativePath)
    {
        var validated = ValidateRelativePath(relativePath);
        var root = Path.GetFullPath(workspaceRoot);
        if (!Directory.Exists(root))
            throw new DirectoryNotFoundException($"workspace root does not exist: {root}");
        if (IsReparsePoint(root))
            throw new InvalidOperationException(
                $"workspace root may not be a reparse point: {root}");
        if (!root.EndsWith(Path.DirectorySeparatorChar))
            root += Path.DirectorySeparatorChar;

        var full = Path.GetFullPath(Path.Combine(root, validated));

        // The resolved path must start with the root.
        if (!full.StartsWith(root, StringComparison.OrdinalIgnoreCase))
            throw new InvalidOperationException(
                $"path escapes workspace root: {relativePath} -> {full}");

        // Reject every existing reparse point in the ancestry, not only the
        // immediate parent. Non-existing tail segments are safe to resolve but
        // must be checked again immediately before a later write operation.
        string current = root.TrimEnd(Path.DirectorySeparatorChar);
        var relativeSegments = validated.Split('/');
        for (int index = 0; index < relativeSegments.Length; index++)
        {
            current = Path.Combine(current, relativeSegments[index]);
            bool existsAsDirectory = Directory.Exists(current);
            bool existsAsFile = File.Exists(current);
            if ((existsAsDirectory || existsAsFile) && IsReparsePoint(current))
                throw new InvalidOperationException(
                    $"path traverses a reparse point: {current}");
            if (existsAsFile && index < relativeSegments.Length - 1)
                throw new InvalidOperationException(
                    $"path traverses a file: {current}");
        }

        return full;
    }

    /// <summary>
    /// Returns true if the directory is a reparse point (symlink/junction).
    /// </summary>
    [SupportedOSPlatform("windows")]
    public static bool IsReparsePoint(string path)
    {
        if (!Directory.Exists(path) && !File.Exists(path))
            return false;
        var attrs = File.GetAttributes(path);
        return attrs.HasFlag(FileAttributes.ReparsePoint);
    }

    /// <summary>
    /// Directories and file patterns that the watcher must ignore:
    /// <c>.backup</c>, <c>.backup/.staging</c>, temp files, conflict copies.
    /// </summary>
    public static readonly string[] IgnoredNames =
    [
        ".backup",
        ".staging",
        ".git",
        "Thumbs.db",
        ".DS_Store",
    ];

    /// <summary>
    /// Returns true if a path component should be ignored by the watcher.
    /// Checks Synology conflict patterns (<c>*.conflict*</c>) and the
    /// <see cref="IgnoredNames"/> list.
    /// </summary>
    public static bool ShouldIgnore(string name)
    {
        foreach (var ignored in IgnoredNames)
        {
            if (string.Equals(name, ignored, StringComparison.OrdinalIgnoreCase))
                return true;
        }
        // Synology conflict copies: "file (conflict John-PC 2024-01-15).docx"
        if (name.Contains("(conflict", StringComparison.OrdinalIgnoreCase))
            return true;
        // Office temp lock files: "~$file.docx"
        if (name.StartsWith("~$", StringComparison.Ordinal))
            return true;
        return false;
    }
}
