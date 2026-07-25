using System;
using System.Collections.Generic;
using System.IO;

namespace VibeTable.Infrastructure.Backend;

/// <summary>
/// Configuration for spawning the Python backend process.
/// </summary>
/// <remarks>
/// <para>
/// Defaults target the dev workflow (<c>uv run python -m backend</c>) from the
/// repository root. Production builds replace <see cref="Command"/> and
/// <see cref="Arguments"/> with the packaged launcher emitted by the build
/// pipeline, and may set <see cref="LogPath"/> to a writable log directory.
/// </para>
/// <para>
/// <see cref="StartupTimeout"/> bounds the handshake round-trip — the
/// supervisor transitions to <see cref="BackendState.Faulted"/> if
/// <c>system.handshake</c> does not complete within the window. Tests override
/// this to a small value so a regression fails fast rather than hanging the
/// suite.
/// </para>
/// </remarks>
public sealed class BackendLaunchOptions
{
    private const string PackagedBackendRelativePath = "backend/vibetable-backend.exe";

    /// <summary>
    /// Default dev command: <c>uv</c> on PATH.
    /// </summary>
    public const string DefaultCommand = "uv";

    /// <summary>
    /// Default dev arguments: <c>run python -m backend</c>.
    /// </summary>
    public const string DefaultArguments = "run python -m backend";

    /// <summary>
    /// Default startup (handshake) timeout: 10 seconds. Generous enough for
    /// <c>uv</c> cold-start in dev; tests override to ~2s.
    /// </summary>
    public static readonly TimeSpan DefaultStartupTimeout = TimeSpan.FromSeconds(10);

    /// <summary>
    /// Default stop timeout: 5 seconds before the supervisor force-kills the
    /// process.
    /// </summary>
    public static readonly TimeSpan DefaultStopTimeout = TimeSpan.FromSeconds(5);

    /// <summary>
    /// Executable to launch. Defaults to <see cref="DefaultCommand"/> (<c>uv</c>).
    /// May be an absolute path, a bare name resolved via PATH, or a packaged
    /// launcher path produced by the build pipeline.
    /// </summary>
    public string Command { get; set; } = DefaultCommand;

    /// <summary>
    /// Arguments to pass to <see cref="Command"/>. Defaults to
    /// <see cref="DefaultArguments"/> (<c>run python -m backend</c>).
    /// </summary>
    public string Arguments { get; set; } = DefaultArguments;

    /// <summary>
    /// Working directory for the spawned process. Defaults to the current
    /// directory; dev runs set this to the repo root so <c>uv</c> can resolve
    /// the project.
    /// </summary>
    public string? WorkingDirectory { get; set; }

    /// <summary>
    /// Timeout for the <c>system.handshake</c> round-trip after spawn. The
    /// supervisor transitions to <see cref="BackendState.Faulted"/> if the
    /// handshake does not complete within this window.
    /// </summary>
    public TimeSpan StartupTimeout { get; set; } = DefaultStartupTimeout;

    /// <summary>
    /// Grace period between closing stdin and force-killing the process on
    /// stop. A well-behaved backend exits on EOF well within this window.
    /// </summary>
    public TimeSpan StopTimeout { get; set; } = DefaultStopTimeout;

    /// <summary>
    /// Additional environment variables to set on the spawned process
    /// (additive — the supervisor inherits the host environment). Tests use
    /// this to inject failure modes into the fake backend.
    /// </summary>
    public IDictionary<string, string> Environment { get; } =
        new Dictionary<string, string>(StringComparer.Ordinal);

    /// <summary>
    /// Optional path to a log file that receives the backend's captured
    /// stderr. When null (default), stderr is buffered in memory and exposed
    /// via <see cref="PythonBackendSupervisor.GetStdErrorLog"/>.
    /// </summary>
    public string? LogPath { get; set; }

    /// <summary>
    /// Resolves the backend command for the installed or development layout.
    /// Packaged builds use <c>backend/vibetable-backend.exe</c> beside the host;
    /// development builds first honor the validated interpreter passed by
    /// <c>scripts/dev.py</c> in <c>VIBETABLE_PYTHON</c>, then use the
    /// repository's own <c>.venv</c>. If neither is available, the resolver
    /// falls back to <c>uv run</c> from that same root.
    /// </summary>
    public static BackendLaunchOptions ResolveForHost(string? hostBaseDirectory = null)
    {
        string baseDirectory = Path.GetFullPath(
            string.IsNullOrWhiteSpace(hostBaseDirectory)
                ? AppContext.BaseDirectory
                : hostBaseDirectory);

        string packagedBackend = Path.GetFullPath(
            Path.Combine(baseDirectory, PackagedBackendRelativePath));
        if (File.Exists(packagedBackend))
        {
            return new BackendLaunchOptions
            {
                Command = packagedBackend,
                Arguments = string.Empty,
                WorkingDirectory = Path.GetDirectoryName(packagedBackend),
                // A one-folder PyInstaller backend performs antivirus and
                // DLL-loader work on its first launch. Keep the dev timeout
                // strict while allowing a cold installed start to finish.
                StartupTimeout = TimeSpan.FromSeconds(30),
            };
        }

        string? repositoryRoot = FindRepositoryRoot(baseDirectory);
        if (repositoryRoot is null)
        {
            return new BackendLaunchOptions { WorkingDirectory = baseDirectory };
        }

        string? launcherPython = System.Environment.GetEnvironmentVariable("VIBETABLE_PYTHON");
        if (!string.IsNullOrWhiteSpace(launcherPython) && File.Exists(launcherPython))
        {
            return new BackendLaunchOptions
            {
                Command = Path.GetFullPath(launcherPython),
                Arguments = "-m backend",
                WorkingDirectory = repositoryRoot,
            };
        }

        string venvPython = Path.Combine(
            repositoryRoot, ".venv", "Scripts", "python.exe");
        if (File.Exists(venvPython))
        {
            return new BackendLaunchOptions
            {
                Command = venvPython,
                Arguments = "-m backend",
                WorkingDirectory = repositoryRoot,
            };
        }

        return new BackendLaunchOptions { WorkingDirectory = repositoryRoot };
    }

    private static string? FindRepositoryRoot(string startDirectory) =>
        LaunchPaths.FindRepositoryRoot(startDirectory);
}
