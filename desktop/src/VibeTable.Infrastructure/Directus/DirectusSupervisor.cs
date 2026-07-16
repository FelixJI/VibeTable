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
/// Lifecycle: <see cref="StartAsync"/> installs + integrity-verifies the npm
/// Directus dependency (<see cref="DirectusPackageManager"/>), materializes the
/// runtime <c>.env</c> (<see cref="DirectusEnvMaterializer"/>), bootstraps the
/// DB + seeds the VibeTable schema (<see cref="DirectusSchemaBootstrapper"/>),
/// then starts <c>directus start</c> directly via the bundled node. Readiness
/// is polled against <c>http://localhost:&lt;port&gt;/server/ping</c>. The port
/// (which may have auto-evaded conflicts) is exposed via <see cref="BaseUrl"/>
/// so the host can set <c>VIBETABLE_DIRECTUS_URL</c>.
/// </para>
/// <para>
/// The child is bound to a Windows Job Object (kill-on-close) so the Directus
/// process tree is never orphaned, even on a host crash — mirroring the
/// backend supervisor's guarantee.
/// </para>
/// </remarks>
public sealed class DirectusSupervisor : IAsyncDisposable
{
    /// <summary>
    /// Files copied from the packaged template into the per-user runtime
    /// directory. Only the npm manifest + lockfile + env template are needed
    /// now that the supervisor drives Directus directly (no run.py/install.py
    /// on the runtime path).
    /// </summary>
    private static readonly string[] RuntimeTemplateFiles =
    {
        "package.json",
        "package-lock.json",
        ".env.template",
    };
    private readonly DirectusLaunchOptions _options;
    private readonly DirectusPackageManager _packageManager;
    private readonly object _stateGate = new();
    private readonly StringBuilder _stderrBuffer = new();
    private DirectusState _state = DirectusState.Stopped;
    private Process? _process;
    private JobObject? _job;
    private Task? _stderrTask;
    private string? _baseUrl;
    private string? _nodeExe;
    private (DirectusSchemaBootstrapper Bootstrapper, IDictionary<string, string> Env)? _pendingSchemaApply;

    public DirectusSupervisor(DirectusLaunchOptions options) : this(options, packageManager: null) { }

    /// <param name="packageManager">Optional injected package manager (for tests
    /// that need to fake install/verify). When null, a default
    /// <see cref="DirectusPackageManager"/> is used.</param>
    public DirectusSupervisor(DirectusLaunchOptions options, DirectusPackageManager? packageManager)
    {
        _options = options ?? throw new ArgumentNullException(nameof(options));
        if (string.IsNullOrWhiteSpace(_options.LocalDirectusDirectory))
        {
            throw new ArgumentException(
                "DirectusLaunchOptions.LocalDirectusDirectory must be non-empty.",
                nameof(options));
        }
        _packageManager = packageManager ?? new DirectusPackageManager();
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
            // Stage 2: npm ci + integrity verification (no run.py involvement).
            await RunInstallAsync(cancellationToken).ConfigureAwait(false);

            // Stage 3: the host now owns everything run.py used to do.
            // 1. Materialize .env (secrets generated/preserved, port resolved).
            bool alreadyBootstrapped = File.Exists(
                Path.Combine(_options.LocalDirectusDirectory, ".bootstrapped"));
            string? bsEmail, bsPassword;
            _options.Environment.TryGetValue("VIBETABLE_DIRECTUS_BOOTSTRAP_EMAIL", out bsEmail);
            _options.Environment.TryGetValue("VIBETABLE_DIRECTUS_BOOTSTRAP_PASSWORD", out bsPassword);
            var env = DirectusEnvMaterializer.Materialize(
                _options.LocalDirectusDirectory, bsEmail, bsPassword, alreadyBootstrapped);
            // 2. Resolve a free port + persist it into .env.
            int requestedPort = int.TryParse(
                env.GetValueOrDefault("PORT", "8055"), out int rp) ? rp : 8055;
            int port = DirectusEnvMaterializer.PickFreePort(requestedPort);
            if (port != requestedPort)
            {
                env["PORT"] = port.ToString();
                DirectusEnvMaterializer.WriteEnv(
                    Path.Combine(_options.LocalDirectusDirectory, ".env"), env);
            }
            // 3. Stage the bulk-mutation extension so Directus loads it.
            DeployExtension();
            // 4. Bootstrap the DB + seed the VibeTable schema (idempotent).
            await BootstrapAsync(env, cancellationToken).ConfigureAwait(false);
            // 5. Start directus directly (no run.py): node <directus-cli> start.
            (_process, _job) = SpawnDirectus(port, env);

            _baseUrl = $"http://localhost:{port}";
            await WaitForPingAsync(_baseUrl, cancellationToken).ConfigureAwait(false);

            // 6. Now that directus is reachable, seed the VibeTable schema (REST).
            //    Bootstrap (DB tables) already ran above; this is the collection/
            //    relation/policy seeding. Idempotent (skipped once .schema-applied).
            if (_pendingSchemaApply is { } pending)
            {
                string adminEmail = pending.Env.TryGetValue("ADMIN_EMAIL", out string? em) ? em : "admin@local";
                string adminPassword = pending.Env.TryGetValue("ADMIN_PASSWORD", out string? pw) ? pw : "";
                await pending.Bootstrapper.ApplySchemaIfFirstBootAsync(
                    _baseUrl!,
                    adminEmail,
                    adminPassword,
                    Path.Combine(_options.ResourceRoot, "directus", "blueprints", "vibetable-empty.json"),
                    _options.LocalDirectusDirectory,
                    cancellationToken).ConfigureAwait(false);
                await pending.Bootstrapper.DisposeAsync().ConfigureAwait(false);
                _pendingSchemaApply = null;
            }

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
        // Ensure the npm Directus dependency is installed and integrity-verified
        // BEFORE anything else, so install/verification failures surface here
        // with a clear step. The bundled node (runtime/node/) is preferred.
        _nodeExe = NodeRuntime.FindNode(appBaseDirectory: AppContext.BaseDirectory);
        if (_nodeExe is null)
        {
            // The MainWindow path already prompts the user when Node is missing;
            // reaching here means this supervisor was constructed directly
            // without that guard. Fail loudly.
            throw new InvalidOperationException(
                "Node.js 24.x not found (neither bundled nor on PATH); cannot install Directus.");
        }
        await _packageManager
            .EnsureInstalledAsync(_nodeExe, _options.LocalDirectusDirectory, cancellationToken)
            .ConfigureAwait(false);
    }

    /// <summary>
    /// Runs <c>directus bootstrap</c> (internal tables + admin) then seeds the
    /// VibeTable schema via REST — both idempotent, both previously done by
    /// run.py. Delegates to <see cref="DirectusSchemaBootstrapper"/>.
    /// </summary>
    private async Task BootstrapAsync(IDictionary<string, string> env, CancellationToken cancellationToken)
    {
        await using var bootstrapper = new DirectusSchemaBootstrapper();
        await bootstrapper.BootstrapDatabaseAsync(
            _nodeExe!, _options.LocalDirectusDirectory, env, cancellationToken).ConfigureAwait(false);

        // Schema apply needs a reachable server: we have not started directus yet,
        // so defer it to after SpawnDirectus+ping. Hand off via a continuation.
        _pendingSchemaApply = (bootstrapper, env);
    }

    /// <summary>
    /// Stages the built bulk-mutation extension into the local Directus
    /// extensions/ dir so the endpoint registers. Port of run.py's
    /// link_bulk_mutation_extension.
    /// </summary>
    private void DeployExtension()
    {
        string source = Path.Combine(_options.ResourceRoot, "directus", "extensions",
            "vibetable-bulk-mutation", "dist", "index.js");
        string pkg = Path.Combine(_options.ResourceRoot, "directus", "extensions",
            "vibetable-bulk-mutation", "package.json");
        if (!File.Exists(source))
        {
            // Extension is optional for basic operation; warn but do not fail.
            return;
        }
        string targetDir = Path.Combine(_options.LocalDirectusDirectory, "extensions",
            "vibetable-bulk-mutation");
        Directory.CreateDirectory(targetDir);
        try { File.Copy(pkg, Path.Combine(targetDir, "package.json"), overwrite: true); }
        catch { /* best-effort */ }
        string distTarget = Path.Combine(targetDir, "dist");
        if (Directory.Exists(distTarget))
        {
            Directory.Delete(distTarget, recursive: true);
        }
        CopyDirectory(Path.GetDirectoryName(source)!, distTarget);
    }

    private static void CopyDirectory(string source, string target)
    {
        Directory.CreateDirectory(target);
        foreach (string file in Directory.GetFiles(source))
        {
            File.Copy(file, Path.Combine(target, Path.GetFileName(file)), overwrite: true);
        }
        foreach (string dir in Directory.GetDirectories(source))
        {
            CopyDirectory(dir, Path.Combine(target, Path.GetFileName(dir)));
        }
    }

    /// <summary>
    /// Starts <c>directus start</c> directly via the bundled node — no run.py.
    /// The child reads .env from its working directory (already materialized).
    /// </summary>
    private (Process, JobObject) SpawnDirectus(int port, IDictionary<string, string> env)
    {
        string cli = Path.Combine(_options.LocalDirectusDirectory, "node_modules", "directus", "cli.js");
        var psi = new ProcessStartInfo
        {
            FileName = _nodeExe!,
            Arguments = $"\"{cli}\" start",
            UseShellExecute = false,
            RedirectStandardError = true,
            RedirectStandardOutput = true,
            RedirectStandardInput = false,
            CreateNoWindow = true,
            StandardOutputEncoding = Encoding.UTF8,
            StandardErrorEncoding = Encoding.UTF8,
            WorkingDirectory = _options.LocalDirectusDirectory,
        };
        // Pass the materialized env; scrub bootstrap creds from the runtime env.
        foreach (var kv in env)
        {
            if (kv.Key is "ADMIN_EMAIL" or "ADMIN_PASSWORD")
            {
                continue;
            }
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
            throw new InvalidOperationException($"Failed to spawn directus start: {ex.Message}", ex);
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
    /// Polls <c>/server/ping</c> until Directus answers or the startup timeout
    /// elapses. Throws if the child exits first.
    /// </summary>
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
