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
            if (segment == "..")
                throw new InvalidOperationException(
                    $"path traversal (..) is not allowed: {relativePath}");
            // Reject empty segments from doubled separators or leading/trailing slash.
            if (segment.Length == 0 && segments.Length > 1)
                throw new InvalidOperationException(
                    $"path has empty segment: {relativePath}");
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
        if (!root.EndsWith(Path.DirectorySeparatorChar))
            root += Path.DirectorySeparatorChar;

        var full = Path.GetFullPath(Path.Combine(root, validated));

        // The resolved path must start with the root.
        if (!full.StartsWith(root, StringComparison.OrdinalIgnoreCase))
            throw new InvalidOperationException(
                $"path escapes workspace root: {relativePath} -> {full}");

        // Reject reparse points (symlinks/junctions) on the resolved path.
        var parent = Path.GetDirectoryName(full);
        if (parent is not null && Directory.Exists(parent))
        {
            var parentInfo = new DirectoryInfo(parent);
            if (parentInfo.Attributes.HasFlag(FileAttributes.ReparsePoint))
                throw new InvalidOperationException(
                    $"path traverses a reparse point: {parent}");
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
