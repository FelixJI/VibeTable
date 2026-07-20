using System;
using System.Collections.Generic;
using System.IO;
using VibeTable.Infrastructure;

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

    /// <summary>
    /// Bare flag: <c>--no-directus-auto</c>. When set, the host never starts a
    /// local Directus even when it otherwise would (packaged layout, dev
    /// layout, or an explicit <c>--directus-auto</c>). This is the "just start
    /// the WPF host" escape hatch — useful for connecting to an already-running
    /// external Directus via <c>VIBETABLE_DIRECTUS_URL</c>, or for debugging the
    /// host/UI in isolation. Highest priority in
    /// <see cref="ShouldAutoStartLocalDirectus"/>.
    /// </summary>
    public bool NoDirectusAuto { get; set; }

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
                case "--no-directus-auto":
                    options.NoDirectusAuto = true;
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
    /// Decides whether the host should auto-start a local Directus 12 runtime.
    /// Priority (highest first):
    /// <list type="number">
    /// <item><paramref name="explicitlyDisabled"/> (<c>--no-directus-auto</c>)
    /// → <c>false</c>. The explicit escape hatch always wins.</item>
    /// <item><paramref name="explicitlyRequested"/> (<c>--directus-auto</c>)
    /// → <c>true</c>.</item>
    /// <item>A non-empty <paramref name="configuredUrl"/> → <c>false</c>
    /// (an external Directus is already configured via
    /// <c>VIBETABLE_DIRECTUS_URL</c>).</item>
    /// <item>Packaged layout (a <c>local-directus/run.py</c> AND a
    /// <c>backend/vibetable-backend.exe</c> beside the host) → <c>true</c>.</item>
    /// <item>Development layout (the repo's <c>scripts/local_directus</c> is
    /// discoverable by walking up from <paramref name="hostBaseDirectory"/>)
    /// → <c>true</c>. This makes a bare dev run of the WPF host bring up the
    /// full stack without any flag.</item>
    /// <item>Otherwise → <c>false</c>.</item>
    /// </list>
    /// </summary>
    public static bool ShouldAutoStartLocalDirectus(
        bool explicitlyRequested,
        string? configuredUrl,
        string hostBaseDirectory,
        bool explicitlyDisabled = false)
    {
        if (explicitlyDisabled)
        {
            return false;
        }
        if (explicitlyRequested)
        {
            return true;
        }
        if (!string.IsNullOrWhiteSpace(configuredUrl))
        {
            return false;
        }
        string root = Path.GetFullPath(hostBaseDirectory);
        // Packaged layout resolves <host>/local-directus; development resolves
        // the repository's scripts/local_directus. The C# host owns both paths
        // directly—legacy run.py/install.py launchers no longer exist.
        return LaunchPaths.ResolveLocalDirectusDirectory(root) is not null;
    }
}
