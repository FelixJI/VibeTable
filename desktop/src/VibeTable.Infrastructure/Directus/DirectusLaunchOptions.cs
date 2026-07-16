using System;
using System.Collections.Generic;
using System.IO;

namespace VibeTable.Infrastructure.Directus;

/// <summary>
/// Configuration for spawning the local Directus 12 runtime (SQLite) that
/// ships with single-machine VibeTable. The runtime is introduced at first launch
/// via <c>scripts/local_directus/install.py</c> (online <c>npm install</c>,
/// app-private) and started every run via <c>run.py</c>.
/// </summary>
/// <remarks>
/// <para>
/// <see cref="StartupTimeout"/> bounds the readiness poll (<c>GET
/// /server/ping</c>) — Directus' first boot runs schema migrations, so it is
/// generous (2 minutes) by default.
/// </para>
/// </remarks>
public sealed class DirectusLaunchOptions
{
    /// <summary>
    /// Default startup (readiness-poll) timeout: 2 minutes. Directus' first
    /// boot runs schema migrations; subsequent boots are a few seconds.
    /// </summary>
    public static readonly TimeSpan DefaultStartupTimeout = TimeSpan.FromMinutes(2);

    /// <summary>
    /// Default stop timeout: 10 seconds before force-killing the Directus
    /// child process tree.
    /// </summary>
    public static readonly TimeSpan DefaultStopTimeout = TimeSpan.FromSeconds(10);

    /// <summary>
    /// Resolved local-Directus directory (contains <c>run.py</c>,
    /// <c>install.py</c>, <c>package.json</c>). Required.
    /// </summary>
    public string LocalDirectusDirectory { get; set; } = string.Empty;

    /// <summary>
    /// Optional immutable packaged template directory.  At startup its source
    /// files are refreshed into <see cref="LocalDirectusDirectory"/>, which is
    /// a per-user writable state directory. Null in development.
    /// </summary>
    public string? TemplateDirectory { get; set; }

    /// <summary>Release/repository root containing versioned Directus resources.</summary>
    public string ResourceRoot { get; set; } = string.Empty;

    /// <summary>
    /// Executable used to run the local Directus launcher. Development uses
    /// the repository virtual-environment Python; packaged builds reuse the
    /// standalone <c>backend/vibetable-backend.exe</c>. Required.
    /// </summary>
    public string Command { get; set; } = string.Empty;

    /// <summary>
    /// Arguments before the runtime directory. Empty in development, where
    /// the supervisor appends <c>run.py</c>; packaged builds use
    /// <c>--local-directus-runner</c> and append the local runtime directory.
    /// </summary>
    public string ArgumentsPrefix { get; set; } = string.Empty;

    /// <summary>True when <see cref="Command"/> is the packaged backend runner.</summary>
    public bool UsesPackagedRunner { get; set; }

    /// <summary>
    /// Timeout for the readiness poll after spawn.
    /// </summary>
    public TimeSpan StartupTimeout { get; set; } = DefaultStartupTimeout;

    /// <summary>
    /// Grace period before force-killing on stop.
    /// </summary>
    public TimeSpan StopTimeout { get; set; } = DefaultStopTimeout;

    /// <summary>
    /// Optional path to a log file receiving the Directus child's captured
    /// stderr. Null (default) keeps stderr in memory.
    /// </summary>
    public string? LogPath { get; set; }

    /// <summary>
    /// Additional environment variables for the Directus child (additive over
    /// the host environment).
    /// </summary>
    public IDictionary<string, string> Environment { get; } =
        new Dictionary<string, string>(StringComparer.Ordinal);

    /// <summary>
    /// Resolves options for the running host: packaged layout first
    /// (<c>&lt;baseDir&gt;/local-directus/</c> copied into the per-user writable
    /// runtime), then dev
    /// (<c>&lt;repoRoot&gt;/scripts/local_directus/</c> with the repo
    /// <c>.venv</c> Python). Returns null if the local-Directus directory or a
    /// packaged backend/dev Python runner cannot be found.
    /// </summary>
    public static DirectusLaunchOptions? ResolveForHost(
        string? hostBaseDirectory = null,
        string? localStateRoot = null)
    {
        string baseDirectory = Path.GetFullPath(
            string.IsNullOrWhiteSpace(hostBaseDirectory)
                ? AppContext.BaseDirectory
                : hostBaseDirectory);

        string? localDirectus = LaunchPaths.ResolveLocalDirectusDirectory(baseDirectory);
        if (localDirectus is null)
        {
            return null;
        }

        string packagedBackend = Path.GetFullPath(
            Path.Combine(baseDirectory, "backend", "vibetable-backend.exe"));
        if (File.Exists(packagedBackend))
        {
            return new DirectusLaunchOptions
            {
                LocalDirectusDirectory = Path.Combine(
                    localStateRoot ?? Path.Combine(
                        System.Environment.GetFolderPath(
                            System.Environment.SpecialFolder.LocalApplicationData),
                        "VibeTable"),
                    "directus"),
                TemplateDirectory = localDirectus,
                ResourceRoot = baseDirectory,
                Command = packagedBackend,
                ArgumentsPrefix = "--local-directus-runner",
                UsesPackagedRunner = true,
            };
        }

        string? python = ResolveDevelopmentPython(baseDirectory);
        if (python is null)
        {
            return null;
        }

        return new DirectusLaunchOptions
        {
            LocalDirectusDirectory = localDirectus,
            ResourceRoot = LaunchPaths.FindRepositoryRoot(baseDirectory) ?? baseDirectory,
            Command = python,
        };
    }

    private static string? ResolveDevelopmentPython(string baseDirectory)
    {
        // Dev: <repoRoot>/.venv/Scripts/python.exe.
        string? repoRoot = LaunchPaths.FindRepositoryRoot(baseDirectory);
        if (repoRoot is null)
        {
            return null;
        }

        string venv = Path.Combine(repoRoot, ".venv", "Scripts", "python.exe");
        return File.Exists(venv) ? venv : null;
    }
}
