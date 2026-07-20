using System;
using System.Collections.Generic;
using System.IO;

namespace VibeTable.Infrastructure.Directus;

/// <summary>
/// Configuration for spawning the local Directus 12 runtime (SQLite) that
/// ships with single-machine VibeTable. The C# host runs an app-private
/// <c>npm ci</c>, materializes configuration, bootstraps the database, and
/// starts Directus directly through its CLI.
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
    /// Resolved local-Directus directory (contains <c>package.json</c>,
    /// <c>package-lock.json</c>, <c>.env.template</c>; <c>node_modules</c> is
    /// pulled here at first launch). Required.
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
    /// Forces a real package structure/native-module verification even when
    /// the cached install marker is fresh. Used while the first-run experience
    /// is incomplete, so a previous failed attempt can never grant trust to a
    /// partially installed runtime.
    /// </summary>
    public bool ForcePackageVerification { get; set; }

    /// <summary>
    /// Additional environment variables for the Directus child (additive over
    /// the host environment).
    /// </summary>
    public IDictionary<string, string> Environment { get; } =
        new Dictionary<string, string>(StringComparer.Ordinal);

    /// <summary>
    /// Resolves options for the running host: packaged layout first
    /// (<c>&lt;baseDir&gt;/local-directus/</c> copied into the per-user writable
    /// runtime), then dev (<c>&lt;repoRoot&gt;/scripts/local_directus/</c>).
    /// Returns null if the local-Directus directory cannot be found. The host
    /// drives Directus via the bundled Node regardless of layout.
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

        // The host drives Directus via the bundled Node (DirectusPackageManager +
        // DirectusSupervisor spawn `node <directus-cli>`), so the options only
        // need the runtime directory layout — not a Python/Nuitka runner.
        bool isPackaged = File.Exists(Path.Combine(baseDirectory, "backend", "vibetable-backend.exe"));
        if (isPackaged)
        {
            return new DirectusLaunchOptions
            {
                // Per-user writable runtime dir; the template is copied in here.
                LocalDirectusDirectory = Path.Combine(
                    localStateRoot ?? Path.Combine(
                        System.Environment.GetFolderPath(
                            System.Environment.SpecialFolder.LocalApplicationData),
                        "VibeTable"),
                    "directus"),
                TemplateDirectory = localDirectus,
                ResourceRoot = baseDirectory,
            };
        }

        return new DirectusLaunchOptions
        {
            LocalDirectusDirectory = localDirectus,
            ResourceRoot = LaunchPaths.FindRepositoryRoot(baseDirectory) ?? baseDirectory,
        };
    }

}
