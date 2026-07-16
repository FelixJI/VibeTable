using System;
using System.Collections.Generic;
using System.IO;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Parsed command-line options for the VibeTable host. Drives the smoke-test
/// <c>--test-mode</c> shell-readiness flow without changing the production
/// Directus path.
/// </summary>
/// <remarks>
/// <para>
/// Supported flags:
/// </para>
/// <list type="bullet">
/// <item><c>--test-mode</c>: writes a machine-readable readiness file after
/// backend, WebView2, and renderer startup complete.</item>
/// <item><c>--readiness-dir &lt;path&gt;</c>: directory where the readiness file
/// is written. Defaults to the system temp dir.</item>
/// </list>
/// <para>
/// <see cref="Parse"/> is forgiving: unknown flags are ignored so future flags
/// don't break older parsing.
/// </para>
/// </remarks>
public sealed class HostStartupOptions
{
    /// <summary>Bare flag: <c>--test-mode</c>.</summary>
    public bool TestMode { get; set; }

    /// <summary>
    /// Bare flag: <c>--directus-auto</c>. When set, the host itself ensures a
    /// local Directus 12 (SQLite) runtime is installed and running before the
    /// backend starts, then points the backend at it via
    /// <c>VIBETABLE_DIRECTUS_URL</c>. Used by single-machine VibeTable where no external
    /// Directus server exists.
    /// </summary>
    public bool DirectusAuto { get; set; }

    /// <summary>Readiness-file directory from <c>--readiness-dir &lt;path&gt;</c>.</summary>
    public string? ReadinessDir { get; set; }

    /// <summary>
    /// Parses <c>Environment.GetCommandLineArgs()</c>-style arguments into a
    /// <see cref="HostStartupOptions"/>. Unknown flags are ignored.
    /// </summary>
    public static HostStartupOptions Parse(IReadOnlyList<string> args)
    {
        var options = new HostStartupOptions();
        if (args is null)
        {
            return options;
        }
        for (int i = 0; i < args.Count; i++)
        {
            string arg = args[i];
            switch (arg)
            {
                case "--test-mode":
                    options.TestMode = true;
                    break;
                case "--directus-auto":
                    options.DirectusAuto = true;
                    break;
                case "--readiness-dir":
                    if (i + 1 < args.Count)
                    {
                        options.ReadinessDir = args[++i];
                    }
                    break;
            }
        }
        return options;
    }

    /// <summary>Convenience: parse <see cref="Environment.GetCommandLineArgs"/>.</summary>
    public static HostStartupOptions Current()
    {
        var args = Environment.GetCommandLineArgs();
        // The first element is the program path; skip it.
        var rest = new string[Math.Max(0, args.Length - 1)];
        for (int i = 1; i < args.Length; i++)
        {
            rest[i - 1] = args[i];
        }
        return Parse(rest);
    }

    /// <summary>
    /// Packaged single-machine releases auto-start their shipped local
    /// Directus runtime when no external URL is configured. Development keeps
    /// requiring the explicit <c>--directus-auto</c> flag.
    /// </summary>
    public static bool ShouldAutoStartLocalDirectus(
        bool explicitlyRequested,
        string? configuredUrl,
        string hostBaseDirectory)
    {
        if (explicitlyRequested)
        {
            return true;
        }
        if (!string.IsNullOrWhiteSpace(configuredUrl))
        {
            return false;
        }
        string root = Path.GetFullPath(hostBaseDirectory);
        return File.Exists(Path.Combine(root, "local-directus", "run.py"))
            && File.Exists(Path.Combine(root, "backend", "vibetable-backend.exe"));
    }
}
