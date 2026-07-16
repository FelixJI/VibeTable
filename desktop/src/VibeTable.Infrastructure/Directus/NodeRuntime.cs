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
/// (<c>&lt;appBase&gt;/runtime/node/node.exe</c>). This makes the packaged app
/// work with zero user-side Node installation; the developer checkout also
/// ships this directory so dev runs are self-contained.</item>
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
    /// beside <paramref name="appBaseDirectory"/>, else null. Does NOT version
    /// check — the caller does that so a single <c>-v</c> probe covers both
    /// resolution paths.
    /// </summary>
    public static string? ResolveBundledNode(string? appBaseDirectory)
    {
        if (string.IsNullOrWhiteSpace(appBaseDirectory))
        {
            return null;
        }
        string bundled = Path.GetFullPath(Path.Combine(appBaseDirectory, BundledNodeRelativePath, "node.exe"));
        return File.Exists(bundled) ? bundled : null;
    }

    private static string? ResolveOnPath()
    {
        // Prefer a direct "node" lookup so the OS PATH resolution applies; fall
        // back to the .exe form on Windows.
        foreach (string name in new[] { "node", "node.exe" })
        {
            try
            {
                using var proc = Process.Start(new ProcessStartInfo
                {
                    FileName = name,
                    Arguments = "-v",
                    UseShellExecute = false,
                    RedirectStandardOutput = true,
                    RedirectStandardError = true,
                    CreateNoWindow = true,
                });
                if (proc is not null && proc.WaitForExit(3000) && proc.ExitCode == 0)
                {
                    return name;
                }
            }
            catch
            {
                // Not on PATH in this form; try the next.
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
