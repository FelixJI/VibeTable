using System;
using System.IO;

namespace VibeTable.Infrastructure;

/// <summary>Product-only install, executable, and data paths.</summary>
internal static class LaunchPaths
{
    /// <summary>Directory beside the executable that holds all runtime data
    /// while portable mode is enabled.</summary>
    public const string PortableDataDirectoryName = "data";

    /// <summary>Marker file that opts the installation into portable mode.
    /// Its presence (not its content) is the single source of truth.</summary>
    public const string PortableModeMarkerFileName = "portable.marker";

    /// <summary>File written into the previous data root when the user flips
    /// the portable-mode switch; the next launch migrates data roots and
    /// removes it.</summary>
    public const string ModeSwitchPendingFileName = "mode-switch-pending.json";

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
            Path.Combine(baseDirectory, "resources", "sidecar", name));
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

    public static bool IsPortableMode(string baseDirectory) =>
        File.Exists(Path.Combine(
            ResolvePortableDataRoot(baseDirectory),
            PortableModeMarkerFileName));

    public static string ResolvePortableDataRoot(string baseDirectory) =>
        Path.GetFullPath(Path.Combine(baseDirectory, PortableDataDirectoryName));

    public static string ResolveStandardDataRoot(string localAppData) =>
        Path.GetFullPath(Path.Combine(localAppData, "VibeTable"));

    /// <summary>
    /// The data root the product uses right now: the portable
    /// <c>&lt;install&gt;/data</c> directory when the marker opts in, otherwise
    /// the per-user standard root. Explicit test/dev overrides are resolved by
    /// the desktop composition root, not here.
    /// </summary>
    public static string ResolveProductDataRoot(string baseDirectory, string localAppData) =>
        IsPortableMode(baseDirectory)
            ? ResolvePortableDataRoot(baseDirectory)
            : ResolveStandardDataRoot(localAppData);

    /// <summary>
    /// Guards that mutable sidecar state never lives inside the sidecar binary
    /// directory, so replacing the packaged binary cannot corrupt data.
    /// </summary>
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
