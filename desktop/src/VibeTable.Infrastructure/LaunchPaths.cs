using System;
using System.IO;

namespace VibeTable.Infrastructure;

/// <summary>Product-only install, executable, data, and backup paths.</summary>
internal static class LaunchPaths
{
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

    /// <summary>Resolve the bundled sidecar first, then a dev/QA build.</summary>
    public static string? ResolveSidecarBinary(string baseDirectory)
    {
        string name = OperatingSystem.IsWindows() ? "vibetable-pb.exe" : "vibetable-pb";
        string packaged = Path.GetFullPath(
            Path.Combine(baseDirectory, "sidecar", name));
        if (File.Exists(packaged))
        {
            return packaged;
        }

        string? repoRoot = FindRepositoryRoot(baseDirectory);
        if (repoRoot is null)
        {
            return null;
        }
        string dev = Path.Combine(repoRoot, "build", "dev", name);
        if (File.Exists(dev))
        {
            return dev;
        }
        string qa = Path.Combine(repoRoot, "build", "qa", name);
        return File.Exists(qa) ? qa : null;
    }

    public static string ResolveDataRoot(string localAppData) =>
        Path.GetFullPath(Path.Combine(localAppData, "VibeTable", "data"));

    public static string ResolveBackupRoot(string localAppData) =>
        Path.GetFullPath(Path.Combine(localAppData, "VibeTable", "backups"));

    public static void EnsureInstallAndDataAreSeparated(
        string installDirectory,
        string dataDirectory)
    {
        string install = WithSeparator(Path.GetFullPath(installDirectory));
        string data = WithSeparator(Path.GetFullPath(dataDirectory));
        if (install.StartsWith(data, StringComparison.OrdinalIgnoreCase)
            || data.StartsWith(install, StringComparison.OrdinalIgnoreCase))
        {
            throw new InvalidOperationException(
                "The VibeTable install and user-data directories must be separate.");
        }
    }

    private static string WithSeparator(string path) =>
        path.TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar)
        + Path.DirectorySeparatorChar;
}
