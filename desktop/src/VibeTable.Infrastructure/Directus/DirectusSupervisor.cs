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
    private Task? _stdoutTask;
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
    /// The resolved base URL (<c>http://{HOST}:{PORT}</c>). HOST defaults to
    /// <c>127.0.0.1</c> (set by <see cref="DirectusEnvMaterializer"/>). The
    /// WebView2 admin nav target and the injected session cookie's Domain
    /// MUST use the same host string this URL reports.
    /// </summary>
    public string? BaseUrl => _baseUrl;

    /// <summary>Raised whenever the Directus lifecycle state changes.</summary>
    public event Action<object?, DirectusState>? StateChanged;

    /// <summary>
    /// Raised for fine-grained startup stages while <see cref="State"/> is
    /// <see cref="DirectusState.Starting"/>.
    /// </summary>
    public event Action<object?, DirectusStartupProgress>? ProgressChanged;

    /// <summary>
    /// Raised for each line written by Directus to stdout or stderr. Directus
    /// uses both streams for diagnostics, so both must be drained continuously.
    /// Handlers run on background stream readers and must return promptly.
    /// </summary>
    public event Action<object?, string>? LogReceived;

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
            ReportProgress(
                DirectusStartupStage.PreparingRuntime,
                "Preparing the local Directus runtime directory.");
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
            // PORT default + parse fallback must track DirectusEnvMaterializer.DefaultPort
            // (not the legacy well-known 8055) so a template that loses its PORT line
            // never silently reverts to the old port.
            int requestedPort = int.TryParse(
                env.GetValueOrDefault("PORT", DirectusEnvMaterializer.DefaultPort.ToString()),
                out int rp) ? rp : DirectusEnvMaterializer.DefaultPort;
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
            ReportProgress(
                DirectusStartupStage.InitializingDatabase,
                alreadyBootstrapped
                    ? "The Directus database already exists; checking initialization state."
                    : "Creating the Directus database and local administrator.");
            await BootstrapAsync(env, cancellationToken).ConfigureAwait(false);
            // 5. Start directus directly (no run.py): node <directus-cli> start.
            ReportProgress(
                DirectusStartupStage.StartingService,
                $"Starting the local Directus service on port {port}.");
            (_process, _job) = SpawnDirectus(port, env);

            // HOST comes from the materialised .env (DirectusEnvMaterializer sets
            // 127.0.0.1). Use it so BaseUrl matches the cookie domain / nav target.
            // Verification: by inspection + Task 7 smoke (supervisor is process-heavy;
            // no isolated unit test for BaseUrl construction).
            string host = env.TryGetValue("HOST", out string? h) && !string.IsNullOrWhiteSpace(h)
                ? h
                : "localhost";
            _baseUrl = $"http://{host}:{port}";
            ReportProgress(
                DirectusStartupStage.WaitingForService,
                "Waiting for the Directus health endpoint to respond.");
            await WaitForPingAsync(_baseUrl, cancellationToken).ConfigureAwait(false);

            // 6. Now that directus is reachable, seed the VibeTable schema (REST).
            //    Bootstrap (DB tables) already ran above; this is the collection/
            //    relation/policy seeding. Idempotent (skipped once .schema-applied).
            if (_pendingSchemaApply is { } pending)
            {
                ReportProgress(
                    DirectusStartupStage.ApplyingSchema,
                    "Creating the initial VibeTable collections, relations, and permissions.");
                string adminEmail = pending.Env.TryGetValue("ADMIN_EMAIL", out string? em) ? em : "admin@local";
                string adminPassword = pending.Env.TryGetValue("ADMIN_PASSWORD", out string? pw) ? pw : "";
                try
                {
                    await pending.Bootstrapper.ApplySchemaIfFirstBootAsync(
                        _baseUrl!,
                        adminEmail,
                        adminPassword,
                        Path.Combine(_options.ResourceRoot, "directus", "blueprints", "vibetable-empty.json"),
                        _options.LocalDirectusDirectory,
                        cancellationToken).ConfigureAwait(false);
                }
                finally
                {
                    await pending.Bootstrapper.DisposeAsync().ConfigureAwait(false);
                    _pendingSchemaApply = null;
                }
            }

            lock (_stateGate)
            {
                if (_state == DirectusState.Faulted)
                {
                    throw new InvalidOperationException(
                        "Directus exited before the Ready transition.");
                }
                TransitionLocked(DirectusState.Ready);
            }
            ReportProgress(
                DirectusStartupStage.Ready,
                "Directus initialization is complete.");
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
        lock (_stateGate)
        {
            if (_state is not (DirectusState.Stopped or DirectusState.Faulted))
            {
                TransitionLocked(DirectusState.Stopping);
            }
        }
        await TeardownAsync().ConfigureAwait(false);
        lock (_stateGate) { TransitionLocked(DirectusState.Stopped); }
    }

    public async ValueTask DisposeAsync()
    {
        lock (_stateGate)
        {
            if (_state is not (DirectusState.Stopped or DirectusState.Faulted))
            {
                TransitionLocked(DirectusState.Stopping);
            }
        }
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
            .EnsureInstalledAsync(
                _nodeExe,
                _options.LocalDirectusDirectory,
                cancellationToken,
                progress => PublishProgress(progress),
                line => PublishLogLine(line),
                forceFullVerification: _options.ForcePackageVerification)
            .ConfigureAwait(false);
    }

    /// <summary>
    /// Runs <c>directus bootstrap</c> (internal tables + admin) then seeds the
    /// VibeTable schema via REST — both idempotent, both previously done by
    /// run.py. Delegates to <see cref="DirectusSchemaBootstrapper"/>.
    /// </summary>
    private async Task BootstrapAsync(IDictionary<string, string> env, CancellationToken cancellationToken)
    {
        var bootstrapper = new DirectusSchemaBootstrapper();
        try
        {
            await bootstrapper.BootstrapDatabaseAsync(
                _nodeExe!,
                _options.LocalDirectusDirectory,
                env,
                cancellationToken,
                line => PublishLogLine(line)).ConfigureAwait(false);

            // Schema apply needs a reachable server. Keep this bootstrapper alive
            // until Directus has started and the REST seeding step has finished.
            _pendingSchemaApply = (bootstrapper, env);
        }
        catch
        {
            await bootstrapper.DisposeAsync().ConfigureAwait(false);
            throw;
        }
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

        process.Exited += OnProcessExited;
        StartLogCapture(process);
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

    private void StartLogCapture(Process process)
    {
        // Directus writes normal operational logs to stdout. Leaving the
        // redirected stream unread can eventually fill the OS pipe and stall
        // the child, so always drain it even when there are no subscribers.
        _stdoutTask = Task.Run(async () =>
        {
            var sr = process.StandardOutput;
            while (true)
            {
                string? line;
                try { line = await sr.ReadLineAsync().ConfigureAwait(false); }
                catch { break; }
                if (line is null) { break; }
                PublishLogLine(line);
            }
        });

        StartStderrCapture(process);
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
                    PublishLogLine(line);
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

    private void PublishLogLine(string line)
    {
        var handler = LogReceived;
        if (handler is null)
        {
            return;
        }
        try { handler(this, line); }
        catch { /* a C# log consumer must never stop draining the child */ }
    }

    private void ReportProgress(
        DirectusStartupStage stage,
        string detail,
        bool usedFastPath = false)
        => PublishProgress(new DirectusStartupProgress(stage, detail, usedFastPath));

    private void PublishProgress(DirectusStartupProgress progress)
    {
        var handler = ProgressChanged;
        if (handler is null)
        {
            return;
        }
        try { handler(this, progress); }
        catch { /* observers must not break startup */ }
    }

    private void OnProcessExited(object? sender, EventArgs e)
    {
        lock (_stateGate)
        {
            if (_state is DirectusState.Starting or DirectusState.Ready)
            {
                TransitionLocked(DirectusState.Faulted);
            }
        }
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
        if (_pendingSchemaApply is { } pending)
        {
            _pendingSchemaApply = null;
            try { await pending.Bootstrapper.DisposeAsync().ConfigureAwait(false); }
            catch { /* best-effort cleanup after a failed spawn/readiness poll */ }
        }
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
        if (_stdoutTask is not null)
        {
            try { await _stdoutTask.WaitAsync(TimeSpan.FromSeconds(1)).ConfigureAwait(false); }
            catch { /* best-effort drain */ }
        }
        if (_stderrTask is not null)
        {
            try { await _stderrTask.WaitAsync(TimeSpan.FromSeconds(1)).ConfigureAwait(false); }
            catch { /* best-effort drain */ }
        }
    }

    private void TransitionLocked(DirectusState next)
    {
        // Caller holds _stateGate.
        if (_state == next)
        {
            return;
        }
        _state = next;
        var handler = StateChanged;
        if (handler is not null)
        {
            Task.Run(() =>
            {
                try { handler(this, next); }
                catch { /* observers must not break lifecycle transitions */ }
            });
        }
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
