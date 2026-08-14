using System;
using System.Collections.Generic;
using System.IO;
using System.Net.Http;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Infrastructure.Diagnostics;

namespace VibeTable.Infrastructure.PocketBase;

/// <summary>
/// Owns the private PocketBase sidecar process, one-way readiness handshake,
/// authenticated health probe, diagnostics, crash recovery, and process-tree cleanup.
/// </summary>
public sealed class PocketBaseSupervisor : IPocketBaseSupervisor
{
    private const string ReadyEvent = "sidecar.ready";
    private const string SessionSecretEnvironment =
        "VIBETABLE_SIDECAR_SESSION_SECRET";
    private const string SessionHeaderName = "X-VibeTable-Session";
    private const string HealthPath = "/api/vibetable/v1/health";
    private const string ShutdownPath = "/api/vibetable/v1/shutdown";
    private const string AdminBootstrapPath =
        "/api/vibetable/v1/admin/bootstrap";

    private readonly PocketBaseLaunchOptions _options;
    private readonly IPocketBaseProcessFactory _processFactory;
    private readonly IPocketBaseHealthProbe _healthProbe;
    private readonly SemaphoreSlim _lifecycle = new(1, 1);
    private readonly object _statusGate = new();
    private readonly object _recoveryGate = new();
    private readonly StringBuilder _log = new();

    private ProcessGeneration? _generation;
    private PocketBaseStatus _status =
        new(PocketBaseState.Stopped, null, false, null, null);
    private CancellationTokenSource? _recoveryCts;
    private Task? _recoveryTask;
    private int _restartAttempts;
    private int _restartSuppressed = 1;
    private int _disposed;

    public PocketBaseSupervisor(PocketBaseLaunchOptions options)
        : this(options, new SystemPocketBaseProcessFactory(), new HttpPocketBaseHealthProbe())
    {
    }

    public PocketBaseSupervisor(
        PocketBaseLaunchOptions options,
        IPocketBaseProcessFactory processFactory,
        IPocketBaseHealthProbe healthProbe)
    {
        _options = options ?? throw new ArgumentNullException(nameof(options));
        _options.Validate();
        _processFactory = processFactory
            ?? throw new ArgumentNullException(nameof(processFactory));
        _healthProbe = healthProbe
            ?? throw new ArgumentNullException(nameof(healthProbe));
    }

    public event Action<object?, PocketBaseStatus>? StatusChanged;
    public event Action<object?, string>? LogReceived;

    public PocketBaseStatus GetStatus()
    {
        lock (_statusGate)
        {
            return _status;
        }
    }

    public Uri? GetAdminUri()
    {
        PocketBaseStatus status = GetStatus();
        return status.State == PocketBaseState.Ready
            && status.AdminAvailable
            && status.BaseAddress is not null
                ? new Uri(status.BaseAddress, "_/")
                : null;
    }

    public PocketBaseAdminContext? GetAdminContext()
    {
        ProcessGeneration? generation = Volatile.Read(ref _generation);
        if (generation is null) return null;

        lock (generation.TransitionGate)
        {
            if (generation.Phase != ProcessGeneration.Ready
                || generation.Process.HasExited
                || generation.BaseAddress is null
                || string.IsNullOrEmpty(generation.SessionSecret))
            {
                return null;
            }

            return new PocketBaseAdminContext(
                new Uri(
                    generation.BaseAddress,
                    AdminBootstrapPath.TrimStart('/')),
                generation.BaseAddress,
                SessionHeaderName,
                generation.SessionSecret);
        }
    }

    /// <summary>
    /// Copies the current private loopback origin and ephemeral credential
    /// directly into a child backend environment. The credential is never
    /// returned in status DTOs or exposed to the renderer.
    /// </summary>
    public void ConfigureBackendEnvironment(
        IDictionary<string, string> environment)
    {
        ArgumentNullException.ThrowIfNull(environment);
        ProcessGeneration? generation = Volatile.Read(ref _generation);
        if (generation is null)
        {
            throw new InvalidOperationException(
                "Local data sidecar is not ready.");
        }
        lock (generation.TransitionGate)
        {
            if (generation.Phase != ProcessGeneration.Ready
                || generation.BaseAddress is null
                || string.IsNullOrEmpty(generation.SessionSecret))
            {
                throw new InvalidOperationException(
                    "Local data sidecar is not ready.");
            }
            environment["VIBETABLE_SIDECAR_URL"] =
                generation.BaseAddress.GetLeftPart(UriPartial.Authority);
            environment[SessionSecretEnvironment] = generation.SessionSecret;
        }
    }

    public string GetSanitizedLog()
    {
        lock (_log)
        {
            return _log.ToString();
        }
    }

    public async Task StartAsync(CancellationToken cancellationToken)
    {
        ThrowIfDisposed();
        await _lifecycle.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            ThrowIfDisposed();
            PocketBaseState state = GetStatus().State;
            if (state is PocketBaseState.Starting or PocketBaseState.Ready
                or PocketBaseState.Stopping)
            {
                throw new InvalidOperationException(
                    $"Local data sidecar cannot start from state {state}.");
            }

            SuppressAndCancelRecovery();
            ProcessGeneration? previous = Volatile.Read(ref _generation);
            if (previous is not null)
            {
                await TeardownGenerationAsync(previous, requestGraceful: false)
                    .ConfigureAwait(false);
            }

            _restartAttempts = 0;
            Volatile.Write(ref _restartSuppressed, 0);
            await StartGenerationAsync(cancellationToken).ConfigureAwait(false);
        }
        finally
        {
            _lifecycle.Release();
        }
    }

    public async Task StopAsync(CancellationToken cancellationToken)
    {
        SuppressAndCancelRecovery();
        await _lifecycle.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            ProcessGeneration? generation = Volatile.Read(ref _generation);
            if (GetStatus().State == PocketBaseState.Stopped && generation is null)
            {
                return;
            }

            PublishStatus(new PocketBaseStatus(
                PocketBaseState.Stopping,
                GetStatus().BaseAddress,
                false,
                generation?.Process.ExitCode,
                null));

            // Once stop has changed externally visible state, cleanup is bounded
            // by our own timeout and must finish even if the caller cancels.
            try
            {
                if (generation is not null)
                {
                    await TeardownGenerationAsync(generation, requestGraceful: true)
                        .ConfigureAwait(false);
                }
            }
            finally
            {
                PublishStatus(new PocketBaseStatus(
                    PocketBaseState.Stopped, null, false, null, null));
            }
        }
        finally
        {
            _lifecycle.Release();
        }

        cancellationToken.ThrowIfCancellationRequested();
    }

    public async ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0)
        {
            return;
        }

        try
        {
            await StopAsync(CancellationToken.None).ConfigureAwait(false);
        }
        finally
        {
            _lifecycle.Dispose();
            lock (_recoveryGate)
            {
                _recoveryCts?.Dispose();
                _recoveryCts = null;
                _recoveryTask = null;
            }
            if (_healthProbe is IDisposable disposable)
            {
                disposable.Dispose();
            }
        }
    }

    private async Task StartGenerationAsync(CancellationToken cancellationToken)
    {
        PublishStatus(new PocketBaseStatus(
            PocketBaseState.Starting, null, false, null, null));

        ProcessGeneration? generation = null;
        try
        {
            string sessionSecret = GenerateSessionSecret();
            var environment = new Dictionary<string, string>(
                _options.Environment,
                StringComparer.Ordinal)
            {
                [SessionSecretEnvironment] = sessionSecret,
            };
            var request = new PocketBaseProcessStartRequest(
                _options.ExecutablePath,
                _options.WorkingDirectory,
                BuildArguments(),
                environment);
            IPocketBaseProcess process = _processFactory.Start(request);
            generation = new ProcessGeneration(process, sessionSecret);
            generation.ExitHandler = (_, _) => OnProcessExited(generation);
            process.Exited += generation.ExitHandler;
            Volatile.Write(ref _generation, generation);

            generation.StderrPump = PumpDiagnosticsAsync(
                process.StandardError,
                sessionSecret);

            using var startupCts =
                CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
            startupCts.CancelAfter(_options.StartupTimeout);

            string? readyLine;
            try
            {
                readyLine = await process.StandardOutput
                    .ReadLineAsync(startupCts.Token)
                    .ConfigureAwait(false);
            }
            catch (OperationCanceledException)
                when (!cancellationToken.IsCancellationRequested)
            {
                throw new TimeoutException(
                    $"Local data sidecar did not publish readiness within " +
                    $"{_options.StartupTimeout}.");
            }

            ReadyRecord ready = ParseAndValidateReady(readyLine, process.Id);
            generation.StdoutPump = DrainStdoutAsync(
                process.StandardOutput,
                sessionSecret);
            generation.BaseAddress =
                new Uri($"http://{ready.Address}/", UriKind.Absolute);

            await WaitForHealthAsync(
                generation,
                new Uri(generation.BaseAddress, HealthPath.TrimStart('/')),
                startupCts.Token,
                cancellationToken).ConfigureAwait(false);

            // Exited and Ready compete through one atomic phase transition.
            // If Exited won, Ready can never be published for this generation.
            lock (generation.TransitionGate)
            {
                if (process.HasExited
                    || generation.Phase != ProcessGeneration.Starting)
                {
                    throw new InvalidOperationException(
                        "Local data sidecar exited before becoming ready.");
                }
                generation.Phase = ProcessGeneration.Ready;
                PublishStatus(new PocketBaseStatus(
                    PocketBaseState.Ready,
                    generation.BaseAddress,
                    true,
                    null,
                    null));
            }
        }
        catch (Exception exception)
        {
            int? exitCode = generation?.Process.ExitCode;
            string message = Sanitize(exception.Message, generation?.SessionSecret);
            PublishStatus(new PocketBaseStatus(
                PocketBaseState.Faulted,
                null,
                false,
                exitCode,
                message));
            if (generation is not null)
            {
                await TeardownGenerationAsync(generation, requestGraceful: false)
                    .ConfigureAwait(false);
            }
            throw;
        }
    }

    private IReadOnlyList<string> BuildArguments()
    {
        var arguments = new List<string>
        {
            "--data-dir",
            _options.DataDirectory,
        };
        // stdout is the one-line readiness protocol. PocketBase's --dev mode
        // emits SQL traces to stdout before that record, so the desktop host
        // must never enable it for a supervised process.
        return arguments;
    }

    private async Task WaitForHealthAsync(
        ProcessGeneration generation,
        Uri healthEndpoint,
        CancellationToken startupToken,
        CancellationToken callerToken)
    {
        Exception? lastFailure = null;
        try
        {
            while (true)
            {
                callerToken.ThrowIfCancellationRequested();
                startupToken.ThrowIfCancellationRequested();
                if (generation.Process.HasExited
                    || Volatile.Read(ref generation.Phase) != ProcessGeneration.Starting)
                {
                    throw new InvalidOperationException(
                        "Local data sidecar exited before its health check succeeded.");
                }
                try
                {
                    PocketBaseHealthStatus? health = await _healthProbe
                        .GetHealthAsync(
                            healthEndpoint,
                            generation.SessionSecret,
                            startupToken)
                        .ConfigureAwait(false);
                    if (health is not null)
                    {
                        ValidateBuildIdentity(health.Build, "health");
                        if (string.Equals(health.Status, "ok", StringComparison.Ordinal)
                            && string.Equals(
                                health.PocketBase,
                                "ok",
                                StringComparison.Ordinal)
                            && health.SchemaReady
                            && health.StorageWritable)
                        {
                            return;
                        }
                    }
                }
                catch (OperationCanceledException)
                {
                    throw;
                }
                catch (HttpRequestException exception)
                {
                    lastFailure = exception;
                }
                catch (IOException exception)
                {
                    lastFailure = exception;
                }

                await Task.Delay(_options.HealthPollInterval, startupToken)
                    .ConfigureAwait(false);
            }
        }
        catch (OperationCanceledException)
            when (!callerToken.IsCancellationRequested)
        {
            throw new TimeoutException(
                "Local data sidecar health check timed out. " +
                (lastFailure is null
                    ? "The endpoint did not report ready."
                    : Sanitize(lastFailure.Message, generation.SessionSecret)));
        }
    }

    private ReadyRecord ParseAndValidateReady(string? json, int processId)
    {
        if (string.IsNullOrWhiteSpace(json))
        {
            throw new InvalidOperationException(
                "Local data sidecar exited without a readiness record.");
        }

        ReadyRecord? ready;
        try
        {
            ready = JsonSerializer.Deserialize<ReadyRecord>(
                json,
                new JsonSerializerOptions { PropertyNameCaseInsensitive = true });
        }
        catch (JsonException exception)
        {
            throw new InvalidOperationException(
                "Local data sidecar published an invalid readiness record: " +
                exception.Message,
                exception);
        }

        PocketBaseExpectedIdentity expected = _options.ExpectedIdentity!;
        if (ready is null
            || !string.Equals(
                ready.Contract,
                expected.ReadyContract,
                StringComparison.Ordinal)
            || !string.Equals(ready.Event, ReadyEvent, StringComparison.Ordinal)
            || ready.Pid != processId)
        {
            throw new InvalidOperationException(
                "Local data sidecar readiness contract or process id did not match.");
        }
        ValidateBuildIdentity(ready.Build, "readiness");
        if (!TryValidateLoopbackAddress(ready.Address))
        {
            throw new InvalidOperationException(
                "Local data sidecar readiness address was not an assigned IPv4 loopback port.");
        }
        return ready;
    }

    private void ValidateBuildIdentity(
        PocketBaseBuildIdentity? actual,
        string source)
    {
        PocketBaseExpectedIdentity expected = _options.ExpectedIdentity!;
        if (actual is null
            || !string.Equals(
                actual.ContractVersion,
                expected.ContractVersion,
                StringComparison.Ordinal)
            || !string.Equals(
                actual.PocketBaseVersion,
                expected.PocketBaseVersion,
                StringComparison.Ordinal)
            || !string.Equals(
                actual.SchemaVersion,
                expected.SchemaVersion,
                StringComparison.Ordinal)
            || !string.Equals(
                actual.MigrationHash,
                expected.MigrationHash,
                StringComparison.Ordinal))
        {
            throw new InvalidOperationException(
                $"Local data sidecar {source} identity did not match the expected package.");
        }
    }

    private static bool TryValidateLoopbackAddress(string? address)
    {
        if (string.IsNullOrWhiteSpace(address)
            || !Uri.TryCreate($"http://{address}/", UriKind.Absolute, out Uri? uri))
        {
            return false;
        }
        return string.Equals(uri.Host, "127.0.0.1", StringComparison.Ordinal)
            && uri.Port is >= 1 and <= 65535
            && !address.EndsWith(":0", StringComparison.Ordinal);
    }

    private async Task PumpDiagnosticsAsync(
        TextReader reader,
        string sessionSecret)
    {
        RotatingLogSink? writer = null;
        try
        {
            if (!string.IsNullOrWhiteSpace(_options.LogPath))
            {
                writer = new RotatingLogSink(_options.LogPath);
            }

            while (await reader.ReadLineAsync().ConfigureAwait(false) is { } line)
            {
                string safe = Sanitize(line, sessionSecret);
                lock (_log)
                {
                    _log.AppendLine(safe);
                }
                if (writer is not null && DiagnosticLogLine.IsSafe(safe))
                {
                    await writer.WriteLineAsync(safe).ConfigureAwait(false);
                }
                try
                {
                    LogReceived?.Invoke(this, safe);
                }
                catch
                {
                    // Diagnostics consumers never control process lifetime.
                }
            }
        }
        catch
        {
            // A broken diagnostic stream cannot deadlock or fault the sidecar.
        }
        finally
        {
            if (writer is not null)
            {
                await writer.DisposeAsync().ConfigureAwait(false);
            }
        }
    }

    private static async Task DrainStdoutAsync(
        TextReader reader,
        string sessionSecret)
    {
        try
        {
            while (await reader.ReadLineAsync().ConfigureAwait(false) is { } line)
            {
                // stdout is reserved for the one-way machine protocol. Still
                // sanitize while draining so this pump owns a generation secret
                // and can never accidentally forward a raw late protocol line.
                _ = Sanitize(line, sessionSecret);
            }
        }
        catch
        {
            // Process exit closes the stream.
        }
    }

    private async Task TeardownGenerationAsync(
        ProcessGeneration generation,
        bool requestGraceful)
    {
        Interlocked.Exchange(
            ref generation.Phase,
            ProcessGeneration.Stopping);
        if (generation.ExitHandler is not null)
        {
            generation.Process.Exited -= generation.ExitHandler;
        }

        try
        {
            bool gracefulRequested = false;
            using var stopCts = new CancellationTokenSource(_options.StopTimeout);
            if (requestGraceful
                && !generation.Process.HasExited
                && generation.BaseAddress is not null)
            {
                try
                {
                    gracefulRequested = await _healthProbe.RequestShutdownAsync(
                        new Uri(
                            generation.BaseAddress,
                            ShutdownPath.TrimStart('/')),
                        generation.SessionSecret,
                        stopCts.Token).ConfigureAwait(false);
                }
                catch (HttpRequestException)
                {
                    // Broken loopback transport falls back to process cleanup.
                }
                catch (IOException)
                {
                    // Broken loopback transport falls back to process cleanup.
                }
                catch (OperationCanceledException)
                {
                    // The same bounded stop token governs the forceful fallback.
                }
            }

            if (gracefulRequested && !generation.Process.HasExited)
            {
                try
                {
                    await generation.Process.WaitForExitAsync(stopCts.Token)
                        .ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    // Forceful cleanup below.
                }
            }

            if (!generation.Process.HasExited)
            {
                generation.Process.KillProcessTree();
                try
                {
                    await generation.Process.WaitForExitAsync(stopCts.Token)
                        .ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    // Dispose and the kill-on-close job are the final boundary.
                }
            }
        }
        finally
        {
            try
            {
                await generation.Process.DisposeAsync().ConfigureAwait(false);
            }
            finally
            {
                if (ReferenceEquals(
                    Interlocked.CompareExchange(ref _generation, null, generation),
                    generation))
                {
                    // Cleared only if this is still the active generation.
                }
                await ObserveAndReleasePumpsAsync(generation).ConfigureAwait(false);
            }
        }
    }

    private static async Task ObserveAndReleasePumpsAsync(
        ProcessGeneration generation)
    {
        Task stdout = generation.StdoutPump ?? Task.CompletedTask;
        Task stderr = generation.StderrPump ?? Task.CompletedTask;
        Task both = Task.WhenAll(stdout, stderr);
        try
        {
            await both.WaitAsync(TimeSpan.FromSeconds(1)).ConfigureAwait(false);
            generation.ReleaseSecret();
        }
        catch
        {
            // Keep the generation's secret until both delayed pumps finish.
            _ = ReleaseSecretAfterPumpsAsync(generation, both);
        }
    }

    private static async Task ReleaseSecretAfterPumpsAsync(
        ProcessGeneration generation,
        Task pumps)
    {
        try
        {
            await pumps.ConfigureAwait(false);
        }
        catch
        {
            // Pump failures are diagnostics-only.
        }
        finally
        {
            generation.ReleaseSecret();
        }
    }

    private void OnProcessExited(ProcessGeneration generation)
    {
        lock (generation.TransitionGate)
        {
            int previous = generation.Phase;
            generation.Phase = ProcessGeneration.Exited;
            if (previous != ProcessGeneration.Ready
                || Volatile.Read(ref _restartSuppressed) != 0
                || !ReferenceEquals(Volatile.Read(ref _generation), generation))
            {
                return;
            }

            PublishStatus(new PocketBaseStatus(
                PocketBaseState.Faulted,
                null,
                false,
                generation.Process.ExitCode,
                "Local data sidecar exited unexpectedly."));
        }
        BeginRecovery(generation);
    }

    private void BeginRecovery(ProcessGeneration crashedGeneration)
    {
        lock (_recoveryGate)
        {
            if (Volatile.Read(ref _restartSuppressed) != 0
                || _restartAttempts >= _options.CrashRestartLimit)
            {
                return;
            }

            _recoveryCts?.Dispose();
            _recoveryCts = new CancellationTokenSource();
            CancellationTokenSource owner = _recoveryCts;
            _recoveryTask = Task.Run(
                () => RecoverAsync(
                    crashedGeneration,
                    owner,
                    owner.Token));
        }
    }

    private async Task RecoverAsync(
        ProcessGeneration crashedGeneration,
        CancellationTokenSource owner,
        CancellationToken cancellationToken)
    {
        ProcessGeneration? stale = crashedGeneration;
        try
        {
            while (_restartAttempts < _options.CrashRestartLimit)
            {
                int attempt = Interlocked.Increment(ref _restartAttempts);
                TimeSpan delay = GetRecoveryDelay(attempt);
                await Task.Delay(delay, cancellationToken).ConfigureAwait(false);
                await _lifecycle.WaitAsync(cancellationToken).ConfigureAwait(false);
                try
                {
                    if (Volatile.Read(ref _restartSuppressed) != 0)
                    {
                        return;
                    }

                    if (stale is not null
                        && ReferenceEquals(
                            Volatile.Read(ref _generation),
                            stale))
                    {
                        await TeardownGenerationAsync(
                            stale,
                            requestGraceful: false).ConfigureAwait(false);
                    }
                    stale = null;

                    try
                    {
                        await StartGenerationAsync(cancellationToken)
                            .ConfigureAwait(false);
                        return;
                    }
                    catch (OperationCanceledException)
                        when (cancellationToken.IsCancellationRequested)
                    {
                        return;
                    }
                    catch
                    {
                        // The failed generation has already been cleaned up and
                        // published Faulted. Retry while the cap allows.
                    }
                }
                finally
                {
                    _lifecycle.Release();
                }
            }
        }
        catch (OperationCanceledException)
            when (cancellationToken.IsCancellationRequested)
        {
            // Explicit Start, Stop, or Dispose owns the next transition.
        }
        finally
        {
            lock (_recoveryGate)
            {
                if (ReferenceEquals(_recoveryCts, owner))
                {
                    _recoveryCts.Dispose();
                    _recoveryCts = null;
                    _recoveryTask = null;
                }
            }
        }
    }

    private TimeSpan GetRecoveryDelay(int attempt)
    {
        double multiplier = Math.Pow(2, Math.Max(0, attempt - 1));
        double ticks = Math.Min(
            _options.CrashRestartInitialDelay.Ticks * multiplier,
            _options.CrashRestartMaximumDelay.Ticks);
        return TimeSpan.FromTicks((long)ticks);
    }

    private void SuppressAndCancelRecovery()
    {
        Volatile.Write(ref _restartSuppressed, 1);
        lock (_recoveryGate)
        {
            _recoveryCts?.Cancel();
        }
    }

    private void PublishStatus(PocketBaseStatus status)
    {
        lock (_statusGate)
        {
            _status = status;
        }
        try
        {
            StatusChanged?.Invoke(this, status);
        }
        catch
        {
            // Status observers never control process lifetime.
        }
    }

    private static string Sanitize(string value, string? sessionSecret)
        => string.IsNullOrEmpty(sessionSecret)
            ? value
            : value.Replace(
                sessionSecret,
                "[REDACTED]",
                StringComparison.Ordinal);

    private static string GenerateSessionSecret()
        => Convert.ToHexString(RandomNumberGenerator.GetBytes(32))
            .ToLowerInvariant();

    private void ThrowIfDisposed()
    {
        if (Volatile.Read(ref _disposed) != 0)
        {
            throw new ObjectDisposedException(nameof(PocketBaseSupervisor));
        }
    }

    private sealed class ProcessGeneration
    {
        internal const int Starting = 0;
        internal const int Ready = 1;
        internal const int Exited = 2;
        internal const int Stopping = 3;

        private string _sessionSecret;

        internal ProcessGeneration(
            IPocketBaseProcess process,
            string sessionSecret)
        {
            Process = process;
            _sessionSecret = sessionSecret;
        }

        internal IPocketBaseProcess Process { get; }
        internal object TransitionGate { get; } = new();
        internal string SessionSecret => _sessionSecret;
        internal EventHandler? ExitHandler { get; set; }
        internal Task? StdoutPump { get; set; }
        internal Task? StderrPump { get; set; }
        internal Uri? BaseAddress { get; set; }
        internal int Phase = Starting;

        internal void ReleaseSecret() => _sessionSecret = string.Empty;
    }

    private sealed record ReadyRecord(
        string? Contract,
        string? Event,
        string? Address,
        int Pid,
        PocketBaseBuildIdentity? Build);
}
