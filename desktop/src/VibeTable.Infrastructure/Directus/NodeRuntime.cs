using System;
using System.Diagnostics;
using System.IO;

namespace VibeTable.Infrastructure.Directus;

/// <summary>
/// Detects a usable Node.js runtime on the machine. The local Directus
/// runtime needs Node.js 24.x to run <c>npm install</c> / <c>directus start</c>.
/// </summary>
public static class NodeRuntime
{
    /// <summary>
    /// Minimum supported Node major version. Matches the repository's
    /// <c>.nvmrc</c> (24.x). isolated-vm@6.1.2 ships a win32-x64 prebuilt
    /// binary for ABI137 = Node 24, so 24 is the floor.
    /// </summary>
    public const int MinimumMajorVersion = 24;

    /// <summary>
    /// Finds <c>node.exe</c> on PATH and verifies its major version is at least
    /// <see cref="MinimumMajorVersion"/>. Returns null if Node is absent or too
    /// old (the caller then prompts the user to install it).
    /// </summary>
    public static string? FindNode(int minMajor = MinimumMajorVersion)
    {
        string? node = ResolveOnPath();
        if (node is null || !File.Exists(node))
        {
            return null;
        }

        if (!TryGetMajorVersion(node, out int major) || major < minMajor)
        {
            return null;
        }

        return node;
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
