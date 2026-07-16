using System;
using System.IO;

namespace VibeTable.Infrastructure;

/// <summary>
/// Shared path-resolution helpers for locating the repository root and the
/// local-Directus runtime directory. Internal so both the backend and Directus
/// launch options resolve consistently without duplicating the walk-up logic.
/// </summary>
internal static class LaunchPaths
{
    /// <summary>
    /// Walks up at most 12 levels from <paramref name="startDirectory"/>
    /// looking for a directory containing both <c>pyproject.toml</c> and
    /// <c>backend/</c> — the repo-root marker pair. Returns null if not found.
    /// </summary>
    public static string? FindRepositoryRoot(string startDirectory)
    {
        var directory = new DirectoryInfo(startDirectory);
        for (int depth = 0; depth < 12 && directory is not null; depth++)
        {
            if (File.Exists(Path.Combine(directory.FullName, "pyproject.toml"))
                && Directory.Exists(Path.Combine(directory.FullName, "backend")))
            {
                return directory.FullName;
            }
            directory = directory.Parent;
        }
        return null;
    }

    /// <summary>
    /// Resolves the local-Directus directory for the running host.
    /// Packaged-first (<c>&lt;baseDir&gt;/local-directus/</c>), then dev
    /// (<c>&lt;repoRoot&gt;/scripts/local_directus/</c>). Returns null if
    /// neither exists (the host must then refuse <c>--directus-auto</c>).
    /// </summary>
    public static string? ResolveLocalDirectusDirectory(string baseDirectory)
    {
        string packaged = Path.GetFullPath(Path.Combine(baseDirectory, "local-directus"));
        if (Directory.Exists(packaged))
        {
            return packaged;
        }

        string? repoRoot = FindRepositoryRoot(baseDirectory);
        if (repoRoot is null)
        {
            return null;
        }

        string dev = Path.Combine(repoRoot, "scripts", "local_directus");
        return Directory.Exists(dev) ? dev : null;
    }
}
