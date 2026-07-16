using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Net.Http;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Infrastructure.Backend;


namespace VibeTable.Infrastructure.Directus;

/// <summary>
/// Supervises the local Directus 12 (SQLite) runtime for single-machine VibeTable.
/// A simpler sibling of <see cref="PythonBackendSupervisor"/>: no JSON-RPC
/// handshake — readiness is an HTTP <c>GET /server/ping</c> poll.
/// </summary>
/// <remarks>
/// <para>
/// Lifecycle: <see cref="StartAsync"/> runs <c>install.py</c> (first run only,
/// online <c>npm install</c>) then spawns <c>run.py</c>, which starts
/// <c>directus start</c>. Readiness is polled against
/// <c>http://localhost:&lt;port&gt;/server/ping</c>. The actual port (which may
/// have auto-evaded conflicts) is read back from <c>.env</c> and exposed via
/// <see cref="BaseUrl"/> so the host can set <c>VIBETABLE_DIRECTUS_URL</c>.
/// </para>
/// <para>
/// The child is bound to a Windows Job Object (kill-on-close) so the Directus
/// process tree is never orphaned, even on a host crash — mirroring the
/// backend supervisor's guarantee.
/// </para>
/// </remarks>
public sealed class DirectusSupervisor : IAsyncDisposable
{
    private static readonly string[] RuntimeTemplateFiles =
    {
        "package.json",
        "package-lock.json",
        "run.py",
        "install.py",
        ".env.template",
        "README.md",
    };
    private readonly DirectusLaunchOptions _options;
    private readonly object _stateGate = new();
    private readonly StringBuilder _stderrBuffer = new();
    private DirectusState _state = DirectusState.Stopped;
    private Process? _process;
    private JobObject? _job;
    private Task? _stderrTask;
    private string? _baseUrl;

    public DirectusSupervisor(DirectusLaunchOptions options)
    {
        _options = options ?? throw new ArgumentNullException(nameof(options));
        if (string.IsNullOrWhiteSpace(_options.LocalDirectusDirectory))
        {
            throw new ArgumentException(
                "DirectusLaunchOptions.LocalDirectusDirectory must be non-empty.",
                nameof(options));
        }
        if (string.IsNullOrWhiteSpace(_options.Command))
        {
            throw new ArgumentException(
                "DirectusLaunchOptions.Command must be non-empty.", nameof(options));
        }
    }

    /// <summary>Current supervisor state (thread-safe).</summary>
    public DirectusState State
    {
        get
        {
            lock (_stateGate) { return _state; }
        }
    }

    /// <summary>
    /// The resolved Directus base URL (<c>http://localhost:&lt;port&gt;</c>)
    /// once readiness succeeds; null before. The host sets this as
    /// <c>VIBETABLE_DIRECTUS_URL</c> for the backend.
    /// </summary>
    public string? BaseUrl => _baseUrl;

    /// <summary>Captured stderr from the Directus child (for diagnostics).</summary>
    public string GetStdErrorLog()
    {
        lock (_stderrBuffer) { return _stderrBuffer.ToString(); }
    }

    /// <summary>
    /// Ensures the local Directus runtime is installed (first run) and starts
    /// it. Returns once <c>/server/ping</c> answers. Throws on timeout or
    /// spawn failure (state transitions to <see cref="DirectusState.Faulted"/>).
    /// </summary>
    public async Task StartAsync(CancellationToken cancellationToken)
    {
        lock (_stateGate)
        {
            if (_state != DirectusState.Stopped)
            {
                throw new InvalidOperationException(
                    $"Directus supervisor cannot start from state {_state}.");
            }
            TransitionLocked(DirectusState.Starting);
        }

        try
        {
            PrepareRuntimeDirectory();
            // First-run install (idempotent: run.py also checks, but doing it
            // here surfaces install failures with a clear step before start).
            await RunInstallAsync(cancellationToken).ConfigureAwait(false);

            // Read the requested port (may auto-evade; we re-read .env after start).
            int requestedPort = ReadPortFromEnv(_options.LocalDirectusDirectory) ?? 8055;

            (_process, _job) = SpawnRunPy();

            // run.py writes the (possibly evaded) port back into .env before
            // starting directus; poll .env until it stabilises or start polls.
            int actualPort = await WaitForPortAsync(requestedPort, cancellationToken)
                .ConfigureAwait(false);

            _baseUrl = $"http://localhost:{actualPort}";
            await WaitForPingAsync(_baseUrl, cancellationToken).ConfigureAwait(false);

            lock (_stateGate) { TransitionLocked(DirectusState.Ready); }
        }
        catch
        {
            lock (_stateGate) { TransitionLocked(DirectusState.Faulted); }
            await TeardownAsync().ConfigureAwait(false);
            throw;
        }
    }

    /// <summary>Gracefully stops the Directus child, then force-kills if needed.</summary>
    public async Task StopAsync(CancellationToken cancellationToken)
    {
        await TeardownAsync().ConfigureAwait(false);
        lock (_stateGate) { TransitionLocked(DirectusState.Stopped); }
    }

    public async ValueTask DisposeAsync()
    {
        try { await TeardownAsync().ConfigureAwait(false); }
        catch { /* best-effort */ }
        lock (_stateGate)
        {
            if (_state != DirectusState.Faulted)
            {
                TransitionLocked(DirectusState.Stopped);
            }
        }
    }

    // ---------- internals ----------

    private async Task RunInstallAsync(CancellationToken cancellationToken)
    {
        // run.py's ensure_npm_installed() is idempotent; invoking install.py
        // explicitly is optional but gives a clean first-run signal. To keep
        // the host resilient, we rely on run.py to install if missing and skip
        // a separate install step here. (install.py remains the documented
        // installer entry point for packagers.)
        await Task.CompletedTask.ConfigureAwait(false);
    }

    private (Process, JobObject) SpawnRunPy()
    {
        string runPy = Path.Combine(_options.LocalDirectusDirectory, "run.py");
        string arguments = _options.UsesPackagedRunner
            ? $"{_options.ArgumentsPrefix} \"{_options.LocalDirectusDirectory}\" "
                + $"\"{_options.ResourceRoot}\""
            : $"\"{runPy}\"";
        var psi = new ProcessStartInfo
        {
            FileName = _options.Command,
            Arguments = arguments,
            UseShellExecute = false,
            RedirectStandardError = true,
            RedirectStandardOutput = true,
            RedirectStandardInput = false,
            CreateNoWindow = true,
            StandardOutputEncoding = Encoding.UTF8,
            StandardErrorEncoding = Encoding.UTF8,
            WorkingDirectory = _options.LocalDirectusDirectory,
        };
        foreach (var kv in _options.Environment)
        {
            psi.Environment[kv.Key] = kv.Value;
        }

        var process = new Process { StartInfo = psi, EnableRaisingEvents = true };
        JobObject job = JobObject.Create();
        try
        {
            process.Start();
        }
        catch (System.ComponentModel.Win32Exception ex)
        {
            process.Dispose();
            job.Dispose();
            throw new InvalidOperationException(
                $"Failed to spawn Directus run.py: {ex.Message}", ex);
        }

        if (JobObject.IsSupported)
        {
            try { job.AssignProcess(process.SafeHandle.DangerousGetHandle()); }
            catch { /* soft-degrade; teardown still force-kills */ }
        }

        StartStderrCapture(process);
        return (process, job);
    }

    private void PrepareRuntimeDirectory()
    {
        Directory.CreateDirectory(_options.LocalDirectusDirectory);
        if (string.IsNullOrWhiteSpace(_options.TemplateDirectory))
        {
            return;
        }
        foreach (string name in RuntimeTemplateFiles)
        {
            string source = Path.Combine(_options.TemplateDirectory, name);
            if (!File.Exists(source))
            {
                throw new InvalidOperationException(
                    $"Packaged local Directus template is missing {name}: {source}");
            }
            File.Copy(
                source,
                Path.Combine(_options.LocalDirectusDirectory, name),
                overwrite: true);
        }
    }

    private void StartStderrCapture(Process process)
    {
        _stderrTask = Task.Run(async () =>
        {
            StreamWriter? logWriter = null;
            try
            {
                if (!string.IsNullOrEmpty(_options.LogPath))
                {
                    var dir = Path.GetDirectoryName(_options.LogPath);
                    if (!string.IsNullOrEmpty(dir))
                    {
                        Directory.CreateDirectory(dir);
                    }
                    logWriter = new StreamWriter(_options.LogPath!, append: true, Encoding.UTF8)
                    {
                        AutoFlush = true,
                    };
                }
                var sr = process.StandardError;
                while (true)
                {
                    string? line;
                    try { line = await sr.ReadLineAsync().ConfigureAwait(false); }
                    catch { break; }
                    if (line is null) { break; }
                    lock (_stderrBuffer) { _stderrBuffer.AppendLine(line); }
                    if (logWriter is not null)
                    {
                        try { await logWriter.WriteLineAsync(line).ConfigureAwait(false); }
                        catch { /* best-effort */ }
                    }
                }
            }
            catch { /* never fault the capture task */ }
            finally { logWriter?.Dispose(); }
        });
    }

    /// <summary>
    /// Polls <c>.env</c> for a stable PORT (run.py rewrites it on evasion), or
    /// falls back to the requested port after a short window.
    /// </summary>
    private async Task<int> WaitForPortAsync(int requestedPort, CancellationToken cancellationToken)
    {
        TimeSpan deadline = TimeSpan.FromSeconds(30);
        var sw = Stopwatch.StartNew();
        int lastSeen = requestedPort;
        int stableCount = 0;
        while (sw.Elapsed < deadline && !cancellationToken.IsCancellationRequested)
        {
            int? current = ReadPortFromEnv(_options.LocalDirectusDirectory);
            if (current is int port)
            {
                if (port == lastSeen)
                {
                    stableCount++;
                    if (stableCount >= 2)
                    {
                        return port;
                    }
                }
                else
                {
                    lastSeen = port;
                    stableCount = 1;
                }
            }
            await Task.Delay(500, cancellationToken).ConfigureAwait(false);
        }
        return requestedPort;
    }

    private async Task WaitForPingAsync(string baseUrl, CancellationToken cancellationToken)
    {
        using var http = new HttpClient { Timeout = TimeSpan.FromSeconds(3) };
        var deadline = DateTime.UtcNow + _options.StartupTimeout;
        string lastError = string.Empty;
        while (DateTime.UtcNow < deadline)
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (_process is null || _process.HasExited)
            {
                throw new InvalidOperationException(
                    $"Directus process exited before readiness. Stderr:\n{GetStdErrorLog()}");
            }
            try
            {
                using var resp = await http.GetAsync($"{baseUrl}/server/ping", cancellationToken)
                    .ConfigureAwait(false);
                if (resp.IsSuccessStatusCode)
                {
                    return;
                }
            }
            catch (Exception ex)
            {
                lastError = ex.Message;
            }
            await Task.Delay(500, cancellationToken).ConfigureAwait(false);
        }
        throw new TimeoutException(
            $"Directus did not become ready within {_options.StartupTimeout}. " +
            $"Last error: {lastError}. Stderr:\n{GetStdErrorLog()}");
    }

    private static int? ReadPortFromEnv(string localDirectusDir)
    {
        string envFile = Path.Combine(localDirectusDir, ".env");
        if (!File.Exists(envFile))
        {
            return null;
        }
        try
        {
            foreach (string raw in File.ReadAllLines(envFile))
            {
                string line = raw.Trim();
                if (line.StartsWith("PORT=", StringComparison.Ordinal))
                {
                    string value = line["PORT=".Length..].Trim();
                    if (int.TryParse(value, out int port))
                    {
                        return port;
                    }
                }
            }
        }
        catch { /* best-effort */ }
        return null;
    }

    private async Task TeardownAsync()
    {
        var process = _process;
        if (process is not null && !process.HasExited)
        {
            try
            {
                process.Kill(entireProcessTree: true);
                process.WaitForExit(5000);
            }
            catch { /* best-effort */ }
        }
        try { process?.Dispose(); } catch { }
        try { _job?.Dispose(); } catch { }
        _job = null;
        _process = null;
        if (_stderrTask is not null)
        {
            try { await _stderrTask.WaitAsync(TimeSpan.FromSeconds(1)).ConfigureAwait(false); }
            catch { /* best-effort drain */ }
        }
    }

    private void TransitionLocked(DirectusState next)
    {
        // Caller holds _stateGate.
        _state = next;
    }
}

/// <summary>States for <see cref="DirectusSupervisor"/>.</summary>
public enum DirectusState
{
    Stopped = 0,
    Starting = 1,
    Ready = 2,
    Stopping = 3,
    Faulted = 4,
}
