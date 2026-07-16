using System;
using System.Diagnostics;
using System.IO;

namespace VibeTable.Infrastructure.Directus;

/// <summary>
/// Detects a usable Node.js runtime on the machine. The local Directus
/// runtime needs Node.js 24.x to run <c>npm install</c> / <c>directus start</c>.
/// </summary>
/// <remarks>
/// <para>
/// Resolution order (highest priority first):
/// </para>
/// <list type="number">
/// <item>A bundled portable Node shipped beside the app
/// (<c>&lt;appBase&gt;/runtime/node/node.exe</c>) or at the root of a developer
/// checkout. This makes packaged and repository runs self-contained.</item>
/// <item><c>node.exe</c> on the system PATH (version-checked). The fallback for
/// environments that have their own Node install.</item>
/// </list>
/// <para>
/// Both paths are version-gated against <see cref="MinimumMajorVersion"/>. A
/// bundled-but-too-old Node is not silently used; the caller falls through to
/// PATH and, if that also fails, prompts the user.
/// </para>
/// </remarks>
public static class NodeRuntime
{
    /// <summary>
    /// Relative path (from the host base directory) to the bundled portable
    /// Node executable. Kept as a constant so <c>DirectusPackageManager</c> and
    /// <c>DirectusLaunchOptions</c> resolve the same location.
    /// </summary>
    public const string BundledNodeRelativePath = "runtime/node";

    /// <summary>
    /// Minimum supported Node major version. Matches the repository's
    /// <c>.nvmrc</c> (24.x). isolated-vm@6.1.2 ships a win32-x64 prebuilt
    /// binary for ABI137 = Node 24, so 24 is the floor.
    /// </summary>
    public const int MinimumMajorVersion = 24;

    /// <summary>
    /// Finds a usable <c>node.exe</c>: the bundled portable Node first, then the
    /// system PATH. Returns null if Node is absent or too old (the caller then
    /// prompts the user to install it).
    /// </summary>
    /// <param name="minMajor">Minimum required major version.</param>
    /// <param name="appBaseDirectory">The host's base directory
    /// (<c>AppContext.BaseDirectory</c> in production). When supplied, a bundled
    /// <c>runtime/node/node.exe</c> here is tried before PATH. Pass null to
    /// only ever consult PATH (back-compat for callers that haven't been
    /// wired up yet).</param>
    public static string? FindNode(int minMajor = MinimumMajorVersion, string? appBaseDirectory = null)
    {
        // 1. Bundled portable Node — preferred, makes the app self-contained.
        string? bundled = ResolveBundledNode(appBaseDirectory);
        if (bundled is not null
            && TryGetMajorVersion(bundled, out int bundledMajor)
            && bundledMajor >= minMajor)
        {
            return bundled;
        }

        // 2. System PATH — fallback for dev or user-installed Node.
        string? onPath = ResolveOnPath();
        if (onPath is null || !File.Exists(onPath))
        {
            return null;
        }

        if (!TryGetMajorVersion(onPath, out int major) || major < minMajor)
        {
            return null;
        }

        return onPath;
    }

    /// <summary>
    /// Returns the absolute path to the bundled <c>node.exe</c> if it exists
    /// beside <paramref name="appBaseDirectory"/> or at a repository root above
    /// it, else null. Does NOT version
    /// check — the caller does that so a single <c>-v</c> probe covers both
    /// resolution paths.
    /// </summary>
    public static string? ResolveBundledNode(string? appBaseDirectory)
    {
        if (string.IsNullOrWhiteSpace(appBaseDirectory))
        {
            return null;
        }
        string baseDirectory;
        try
        {
            baseDirectory = Path.GetFullPath(appBaseDirectory);
        }
        catch (Exception ex) when (ex is ArgumentException or NotSupportedException or PathTooLongException)
        {
            return null;
        }

        // Packaged layout: runtime/node is directly beside the host.
        string bundled = Path.Combine(baseDirectory, BundledNodeRelativePath, "node.exe");
        if (File.Exists(bundled))
        {
            return Path.GetFullPath(bundled);
        }

        // Development layout: AppContext.BaseDirectory is normally deep under
        // desktop/src/.../bin/<configuration>/<tfm>. Walk to the Git checkout
        // and use its portable runtime without copying it on every build.
        for (DirectoryInfo? current = new(baseDirectory); current is not null; current = current.Parent)
        {
            if (!File.Exists(Path.Combine(current.FullName, ".nvmrc")))
            {
                continue;
            }

            string repositoryNode = Path.Combine(
                current.FullName, BundledNodeRelativePath, "node.exe");
            if (File.Exists(repositoryNode))
            {
                return Path.GetFullPath(repositoryNode);
            }
        }

        return null;
    }

    private static string? ResolveOnPath()
    {
        string? path = Environment.GetEnvironmentVariable("PATH");
        if (string.IsNullOrWhiteSpace(path))
        {
            return null;
        }

        string[] names = OperatingSystem.IsWindows()
            ? new[] { "node.exe", "node" }
            : new[] { "node" };
        foreach (string entry in path.Split(Path.PathSeparator, StringSplitOptions.RemoveEmptyEntries))
        {
            string directory = entry.Trim().Trim('"');
            foreach (string name in names)
            {
                try
                {
                    string candidate = Path.GetFullPath(Path.Combine(directory, name));
                    if (File.Exists(candidate))
                    {
                        // npm is resolved beside Node, so callers require an
                        // absolute path rather than a bare "node" command.
                        return candidate;
                    }
                }
                catch (Exception ex) when (
                    ex is ArgumentException or NotSupportedException or PathTooLongException)
                {
                    // Ignore malformed PATH entries and continue searching.
                }
            }
        }
        return null;
    }

    private static bool TryGetMajorVersion(string node, out int major)
    {
        major = 0;
        try
        {
            using var proc = Process.Start(new ProcessStartInfo
            {
                FileName = node,
                Arguments = "-v",
                UseShellExecute = false,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
                CreateNoWindow = true,
            });
            if (proc is null || !proc.WaitForExit(3000))
            {
                return false;
            }
            string version = proc.StandardOutput.ReadLine()?.Trim() ?? string.Empty;
            // Node prints "v24.18.0"; strip the leading 'v'.
            if (version.StartsWith("v", StringComparison.Ordinal))
            {
                version = version[1..];
            }
            var parts = version.Split('.');
            return parts.Length > 0 && int.TryParse(parts[0], out major);
        }
        catch
        {
            return false;
        }
    }
}
