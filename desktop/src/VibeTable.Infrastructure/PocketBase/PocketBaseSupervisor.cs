using System;
using System.Collections.Generic;
using System.Diagnostics;
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
    [ThreadStatic]
    private static PocketBaseSupervisor? s_statusDispatcher;
    private static readonly IEqualityComparer<Exception> s_exceptionIdentity =
        ReferenceEqualityComparer.Instance;

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
    private readonly AsyncFifoGate _lifecycle = new();
    private readonly object _generationGate = new();
    private readonly object _retirementGate = new();
    private readonly object _statusGate = new();
    private readonly object _recoveryGate = new();
    private readonly StringBuilder _log = new();
    private readonly Queue<StatusNotification> _statusNotifications = [];
    private readonly HashSet<Task> _activeStopTasks = [];

    private ProcessGeneration? _generation;
    private PocketBaseStatus _status =
        new(PocketBaseState.Stopped, null, false, null, null);
    private CancellationTokenSource? _recoveryCts;
    private Task? _recoveryTask;
    private int _restartAttempts;
    private int _restartSuppressed = 1;
    private long _retirementEpoch;
    private int _pendingStartAdmissions;
    private Task? _disposeTask;
    private int _disposed;
    private bool _dispatchingStatus;
    private int _statusDispatchReservations;
    private bool _statusDispatchPending;
    private PocketBaseStartupTimings? _lastStartupTimings;

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
    internal Func<Task>? StartAdmissionBarrierForTests { get; set; }
    public PocketBaseStartupTimings? LastStartupTimings =>
        Volatile.Read(ref _lastStartupTimings);

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
        lock (_generationGate)
        {
            ProcessGeneration? generation = _generation;
            if (generation is null
                || generation.Epoch != _retirementEpoch
                || generation.Phase != ProcessGeneration.Ready
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
        lock (_generationGate)
        {
            ProcessGeneration? generation = _generation;
            if (generation is null
                || generation.Epoch != _retirementEpoch
                || generation.Phase != ProcessGeneration.Ready
                || generation.Process.HasExited
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
        bool awaitReadyDelivery = !ReferenceEquals(s_statusDispatcher, this);
        ThrowIfDisposed();
        cancellationToken.ThrowIfCancellationRequested();
        (Task recoveryTask, Exception? failure, long admissionEpoch) =
            CancelAndCaptureRecovery(onlyIfActive: true, registerStartAdmission: true);
        AsyncFifoGate.Lease? lifecycle = null;
        Task? readyDelivered = null;
        try
        {
            if (StartAdmissionBarrierForTests is { } barrier)
                await barrier().ConfigureAwait(false);
            lifecycle = await _lifecycle.EnterAsync(CancellationToken.None)
                .ConfigureAwait(false);
            ThrowIfDisposed();
            PocketBaseState state = GetStatus().State;
            if ((state is PocketBaseState.Starting or PocketBaseState.Ready
                    or PocketBaseState.Stopping)
                && !CanReplaceCurrentGeneration())
            {
                RestoreRejectedStart(admissionEpoch);
                throw new InvalidOperationException(
                    $"Local data sidecar cannot start from state {state}.");
            }

            (Task currentRecoveryTask, Exception? currentCancellationFailure, _) =
                CancelAndCaptureRecovery();
            failure = CombineFailures(failure, currentCancellationFailure);
            failure = await ObserveTaskFailureAsync(recoveryTask, failure)
                .ConfigureAwait(false);
            if (!ReferenceEquals(currentRecoveryTask, recoveryTask))
            {
                failure = await ObserveTaskFailureAsync(
                    currentRecoveryTask,
                    failure).ConfigureAwait(false);
            }

            ProcessGeneration? previous;
            lock (_generationGate)
                previous = _generation;
            if (previous is not null)
            {
                await TeardownGenerationAsync(previous, requestGraceful: false)
                    .ConfigureAwait(false);
            }

            if (failure is not null || cancellationToken.IsCancellationRequested)
            {
                lock (_generationGate)
                {
                    if (Volatile.Read(ref _disposed) == 0 && _generation is null)
                    {
                        _restartSuppressed = 1;
                        _ = CommitStatus(new PocketBaseStatus(
                            PocketBaseState.Stopped, null, false, null, null));
                    }
                }
                if (failure is not null)
                    throw failure;
                cancellationToken.ThrowIfCancellationRequested();
            }

            long generationEpoch;
            lock (_retirementGate)
            {
                ThrowIfDisposed();
                lock (_generationGate)
                {
                    ThrowIfDisposed();
                    _restartAttempts = 0;
                    _restartSuppressed = 0;
                    generationEpoch = ++_retirementEpoch;
                }
            }
            StartGenerationResult started = await StartGenerationAsync(
                cancellationToken,
                generationEpoch).ConfigureAwait(false);
            readyDelivered = started.ReadyDelivered;
        }
        finally
        {
            lifecycle?.Dispose();
            ReleaseStartAdmission();
            DrainStatusNotifications();
            if (readyDelivered is not null && awaitReadyDelivery)
                await readyDelivered.ConfigureAwait(false);
        }
    }

    public async Task StopAsync(CancellationToken cancellationToken)
    {
        cancellationToken.ThrowIfCancellationRequested();
        TaskCompletionSource? owner = null;
        Task operation;
        lock (_retirementGate)
        {
            if (_disposeTask is not null)
            {
                operation = _disposeTask;
            }
            else
            {
                owner = new TaskCompletionSource(
                    TaskCreationOptions.RunContinuationsAsynchronously);
                operation = owner.Task;
                _activeStopTasks.Add(operation);
            }
        }
        if (owner is not null)
        {
            _ = CompleteStopAsync(owner);
        }

        await operation.ConfigureAwait(false);
        cancellationToken.ThrowIfCancellationRequested();
    }

    private async Task StopUnderLifecycleAsync()
    {
        ProcessGeneration? generation;
        lock (_generationGate)
            generation = _generation;
        if (GetStatus().State == PocketBaseState.Stopped && generation is null)
        {
            return;
        }

        _ = CommitStatus(new PocketBaseStatus(
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
            _ = CommitStatus(new PocketBaseStatus(
                PocketBaseState.Stopped, null, false, null, null));
        }
    }

    public ValueTask DisposeAsync()
    {
        TaskCompletionSource owner;
        Task disposeTask;
        Task activeStops;
        lock (_retirementGate)
        {
            if (_disposeTask is not null)
            {
                return new ValueTask(_disposeTask);
            }

            owner = new TaskCompletionSource(
                TaskCreationOptions.RunContinuationsAsynchronously);
            disposeTask = owner.Task;
            _disposeTask = disposeTask;
            activeStops = _activeStopTasks.Count == 0
                ? Task.CompletedTask
                : Task.WhenAll(_activeStopTasks);
            Volatile.Write(ref _disposed, 1);
        }

        _ = CompleteDisposeAsync(owner, activeStops);
        return new ValueTask(disposeTask);
    }

    private async Task CompleteStopAsync(TaskCompletionSource owner)
    {
        Exception? failure = await RunStopOperationAsync().ConfigureAwait(false);
        lock (_retirementGate)
        {
            CompleteOwner(owner, failure);
            _activeStopTasks.Remove(owner.Task);
        }
        DrainStatusNotifications();
    }

    private async Task CompleteDisposeAsync(
        TaskCompletionSource owner,
        Task activeStops)
    {
        Exception? failure = null;
        try
        {
            await activeStops.ConfigureAwait(false);
        }
        catch (Exception exception)
        {
            failure = GetTaskFailure(activeStops, exception);
        }
        Exception? finalStopFailure =
            await RunStopOperationAsync().ConfigureAwait(false);
        failure = CombineFailures(failure, finalStopFailure);

        lock (_recoveryGate)
        {
            _recoveryCts?.Dispose();
            _recoveryCts = null;
            _recoveryTask = null;
        }
        try
        {
            if (_healthProbe is IDisposable disposable)
            {
                disposable.Dispose();
            }
        }
        catch (Exception exception)
        {
            failure = CombineFailures(failure, exception);
        }

        CompleteOwner(owner, failure);
        DrainStatusNotifications();
    }

    private async Task<Exception?> RunStopOperationAsync()
    {
        (Task recoveryTask, Exception? failure, _) = CancelAndCaptureRecovery();
        AsyncFifoGate.Lease lifecycle =
            await _lifecycle.EnterAsync(CancellationToken.None).ConfigureAwait(false);
        try
        {
            (Task currentRecoveryTask, Exception? currentCancellationFailure, _) =
                CancelAndCaptureRecovery();
            failure = CombineFailures(failure, currentCancellationFailure);
            failure = await ObserveTaskFailureAsync(recoveryTask, failure)
                .ConfigureAwait(false);
            if (!ReferenceEquals(currentRecoveryTask, recoveryTask))
            {
                failure = await ObserveTaskFailureAsync(
                    currentRecoveryTask,
                    failure).ConfigureAwait(false);
            }
            try
            {
                await StopUnderLifecycleAsync().ConfigureAwait(false);
            }
            catch (Exception exception)
            {
                failure = CombineFailures(failure, exception);
            }
        }
        finally
        {
            lifecycle.Dispose();
        }
        return failure;
    }

    private static async Task<Exception?> ObserveTaskFailureAsync(
        Task task,
        Exception? failure)
    {
        try
        {
            await task.ConfigureAwait(false);
        }
        catch (Exception exception)
        {
            failure = CombineFailures(failure, GetTaskFailure(task, exception));
        }
        return failure;
    }

    internal async Task WaitForRecoveryQuiescenceAsync()
    {
        Exception? failure = null;
        while (true)
        {
            Task? recoveryTask;
            lock (_generationGate)
            {
                lock (_recoveryGate)
                    recoveryTask = _recoveryTask;
            }
            if (recoveryTask is null)
            {
                if (failure is not null)
                    throw failure;
                return;
            }
            failure = await ObserveTaskFailureAsync(recoveryTask, failure)
                .ConfigureAwait(false);
        }
    }

    private static Exception? CombineFailures(Exception? current, Exception? next)
    {
        if (next is null)
            return current;
        return current is null
            ? next
            : CollapseFailures(new AggregateException(current, next));
    }

    private static Exception GetTaskFailure(Task task, Exception observed)
    {
        AggregateException? aggregate = task.Exception;
        if (aggregate is null)
            return observed;
        return CollapseFailures(aggregate);
    }

    private static Exception CollapseFailures(AggregateException aggregate)
    {
        Exception[] failures = aggregate.Flatten().InnerExceptions
            .Distinct(s_exceptionIdentity)
            .ToArray();
        return failures.Length == 1
            ? failures[0]
            : new AggregateException(failures);
    }

    private static void CompleteOwner(
        TaskCompletionSource owner,
        Exception? failure)
    {
        if (failure is null)
        {
            owner.SetResult();
        }
        else
        {
            owner.SetException(failure);
        }
    }

    private (Task RecoveryTask, Exception? Failure, long AdmissionEpoch)
        CancelAndCaptureRecovery(
            bool onlyIfActive = false,
            bool registerStartAdmission = false)
    {
        CancellationTokenSource? recoveryCancellation;
        Task recoveryTask;
        long admissionEpoch;
        lock (_generationGate)
        {
            if (registerStartAdmission)
                _pendingStartAdmissions++;
            lock (_recoveryGate)
            {
                if (onlyIfActive && _recoveryTask is null)
                {
                    return (Task.CompletedTask, null, _retirementEpoch);
                }
                _restartSuppressed = 1;
                admissionEpoch = ++_retirementEpoch;
                recoveryCancellation = _recoveryCts;
                recoveryTask = _recoveryTask ?? Task.CompletedTask;
            }
        }

        Exception? failure = null;
        try
        {
            recoveryCancellation?.Cancel();
        }
        catch (ObjectDisposedException)
        {
            // A completed recovery can release its owner after capture.
        }
        catch (Exception exception)
        {
            failure = exception;
        }
        return (recoveryTask, failure, admissionEpoch);
    }

    private void ReleaseStartAdmission()
    {
        lock (_generationGate)
        {
            _pendingStartAdmissions--;
            if (_pendingStartAdmissions == 0 && _generation is { } current)
                BeginRecoveryUnsafe(current);
        }
    }

    private bool CanReplaceCurrentGeneration()
    {
        lock (_generationGate)
        {
            return _generation is null
                || _generation.Phase == ProcessGeneration.Exited;
        }
    }

    private void RestoreRejectedStart(long admissionEpoch)
    {
        lock (_generationGate)
        {
            ProcessGeneration? generation = _generation;
            if (_retirementEpoch == admissionEpoch
                && Volatile.Read(ref _disposed) == 0
                && generation is not null
                && generation.Phase == ProcessGeneration.Ready
                && !generation.Process.HasExited)
            {
                generation.Epoch = _retirementEpoch;
                _restartSuppressed = 0;
            }
        }
    }

    private async Task<StartGenerationResult> StartGenerationAsync(
        CancellationToken cancellationToken,
        long expectedRetirementEpoch)
    {
        long startedAt = Stopwatch.GetTimestamp();
        long? spawnedAt = null;
        long? readyAt = null;
        Volatile.Write(ref _lastStartupTimings, null);
        lock (_generationGate)
        {
            EnsureGenerationEpoch(expectedRetirementEpoch, cancellationToken);
            _ = CommitStatus(new PocketBaseStatus(
                PocketBaseState.Starting, null, false, null, null));
        }

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
            spawnedAt = Stopwatch.GetTimestamp();
            generation = new ProcessGeneration(
                process,
                sessionSecret,
                expectedRetirementEpoch);
            generation.ExitHandler = (_, _) => OnProcessExited(generation);
            process.Exited += generation.ExitHandler;
            lock (_generationGate)
            {
                EnsureGenerationEpoch(expectedRetirementEpoch, cancellationToken);
                if (_generation is not null)
                {
                    throw new InvalidOperationException(
                        "Local data sidecar generation was not retired.");
                }
                _generation = generation;
            }

            generation.StderrPump = PumpDiagnosticsAsync(
                process.StandardError,
                sessionSecret,
                generation.PumpCancellation);

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
            readyAt = Stopwatch.GetTimestamp();
            generation.StdoutPump = DrainStdoutAsync(
                process.StandardOutput,
                sessionSecret,
                generation.PumpCancellation);
            generation.BaseAddress =
                new Uri($"http://{ready.Address}/", UriKind.Absolute);

            await WaitForHealthAsync(
                generation,
                new Uri(generation.BaseAddress, HealthPath.TrimStart('/')),
                startupCts.Token,
                cancellationToken).ConfigureAwait(false);
            long healthyAt = Stopwatch.GetTimestamp();
            Volatile.Write(
                ref _lastStartupTimings,
                new PocketBaseStartupTimings(
                    Stopwatch.GetElapsedTime(startedAt, spawnedAt.Value),
                    Stopwatch.GetElapsedTime(spawnedAt.Value, readyAt.Value),
                    Stopwatch.GetElapsedTime(readyAt.Value, healthyAt),
                    "health"));

            // Exited and Ready compete through one atomic phase transition.
            // If Exited won, Ready can never be published for this generation.
            lock (_generationGate)
            {
                if (process.HasExited
                    || !ReferenceEquals(_generation, generation)
                    || generation.Phase != ProcessGeneration.Starting
                    || generation.Epoch != expectedRetirementEpoch
                    || _retirementEpoch != expectedRetirementEpoch
                    || _restartSuppressed != 0
                    || Volatile.Read(ref _disposed) != 0)
                {
                    cancellationToken.ThrowIfCancellationRequested();
                    throw new InvalidOperationException(
                        "Local data sidecar exited before becoming ready.");
                }
                generation.Phase = ProcessGeneration.Ready;
                Task readyDelivered = CommitStatus(new PocketBaseStatus(
                    PocketBaseState.Ready,
                    generation.BaseAddress,
                    true,
                    null,
                    null));
                return new StartGenerationResult(readyDelivered);
            }
        }
        catch (Exception exception)
        {
            long failedAt = Stopwatch.GetTimestamp();
            Volatile.Write(
                ref _lastStartupTimings,
                new PocketBaseStartupTimings(
                    spawnedAt is null
                        ? null
                        : Stopwatch.GetElapsedTime(startedAt, spawnedAt.Value),
                    spawnedAt is null || readyAt is null
                        ? null
                        : Stopwatch.GetElapsedTime(spawnedAt.Value, readyAt.Value),
                    readyAt is null
                        ? null
                        : Stopwatch.GetElapsedTime(readyAt.Value, failedAt),
                    spawnedAt is null
                        ? "spawn"
                        : readyAt is null ? "ready-record" : "health"));
            int? exitCode = generation?.Process.ExitCode;
            string message = Sanitize(exception.Message, generation?.SessionSecret);
            lock (_generationGate)
            {
                if (Volatile.Read(ref _disposed) == 0
                    && _restartSuppressed == 0
                    && _retirementEpoch == expectedRetirementEpoch
                    && ReferenceEquals(_generation, generation))
                {
                    _ = CommitStatus(new PocketBaseStatus(
                        PocketBaseState.Faulted,
                        null,
                        false,
                        exitCode,
                        message));
                }
            }
            if (generation is not null)
            {
                await TeardownGenerationAsync(generation, requestGraceful: false)
                    .ConfigureAwait(false);
            }
            throw;
        }
    }

    private void EnsureGenerationEpoch(
        long expectedRetirementEpoch,
        CancellationToken cancellationToken)
    {
        ThrowIfDisposed();
        if (_restartSuppressed != 0
            || _retirementEpoch != expectedRetirementEpoch)
        {
            cancellationToken.ThrowIfCancellationRequested();
            throw new InvalidOperationException(
                "Local data sidecar start was superseded.");
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
                bool generationIsStarting;
                lock (_generationGate)
                {
                    generationIsStarting = ReferenceEquals(_generation, generation)
                        && generation.Epoch == _retirementEpoch
                        && generation.Phase == ProcessGeneration.Starting
                        && _restartSuppressed == 0;
                }
                if (generation.Process.HasExited || !generationIsStarting)
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
                catch (OperationCanceledException exception)
                    when (!callerToken.IsCancellationRequested
                        && !startupToken.IsCancellationRequested)
                {
                    // HttpClient uses OperationCanceledException for its own
                    // per-request timeout. Keep polling while the supervisor's
                    // overall startup budget and caller are still active.
                    lastFailure = exception;
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
        string sessionSecret,
        CancellationToken cancellationToken)
    {
        RotatingLogSink? writer = null;
        try
        {
            if (!string.IsNullOrWhiteSpace(_options.LogPath))
            {
                writer = new RotatingLogSink(_options.LogPath);
            }

            while (await reader.ReadLineAsync(cancellationToken).ConfigureAwait(false)
                is { } line)
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
        string sessionSecret,
        CancellationToken cancellationToken)
    {
        try
        {
            while (await reader.ReadLineAsync(cancellationToken).ConfigureAwait(false)
                is { } line)
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
        lock (_generationGate)
        {
            generation.Phase = ProcessGeneration.Stopping;
            if (generation.ExitHandler is not null)
            {
                generation.Process.Exited -= generation.ExitHandler;
            }
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
                lock (_generationGate)
                {
                    if (ReferenceEquals(_generation, generation))
                    {
                        _generation = null;
                    }
                }
                await CancelJoinAndReleasePumpsAsync(generation).ConfigureAwait(false);
            }
        }
    }

    private static async Task CancelJoinAndReleasePumpsAsync(
        ProcessGeneration generation)
    {
        CancellationTokenSource? cancellation = generation.TakePumpCancellation();
        if (cancellation is null)
            return;
        Task stdout = generation.StdoutPump ?? Task.CompletedTask;
        Task stderr = generation.StderrPump ?? Task.CompletedTask;
        try
        {
            try
            {
                cancellation.Cancel();
            }
            catch
            {
                // Pump cancellation callbacks are diagnostics-only.
            }
            await Task.WhenAll(stdout, stderr).ConfigureAwait(false);
        }
        catch
        {
            // Pump failures are diagnostics-only.
        }
        finally
        {
            generation.ReleaseSecret();
            cancellation.Dispose();
        }
    }

    private void OnProcessExited(ProcessGeneration generation)
    {
        StatusDispatchReservation? reservation = null;
        lock (_generationGate)
        {
            if (!ReferenceEquals(_generation, generation)
                || generation.Phase != ProcessGeneration.Ready)
            {
                return;
            }
            generation.Phase = ProcessGeneration.Exited;
            if (generation.Epoch != _retirementEpoch
                || _restartSuppressed != 0
                || Volatile.Read(ref _disposed) != 0)
            {
                return;
            }

            reservation = CommitUnexpectedExitStatus(new PocketBaseStatus(
                PocketBaseState.Faulted,
                null,
                false,
                generation.Process.ExitCode,
                "Local data sidecar exited unexpectedly."));
        }
        try
        {
            BeginRecovery(generation);
        }
        finally
        {
            reservation.Dispose();
        }
    }

    private void BeginRecovery(ProcessGeneration crashedGeneration)
    {
        lock (_generationGate)
            BeginRecoveryUnsafe(crashedGeneration);
    }

    private void BeginRecoveryUnsafe(ProcessGeneration crashedGeneration)
    {
        if (!CanRecoverGenerationUnsafe(crashedGeneration))
            return;
        long expectedRetirementEpoch = crashedGeneration.Epoch;
        lock (_recoveryGate)
        {
            if (_recoveryTask is not null)
                return;
            _recoveryCts = new CancellationTokenSource();
            CancellationTokenSource owner = _recoveryCts;
            _recoveryTask = Task.Run(
                () => RecoverAsync(
                    crashedGeneration,
                    expectedRetirementEpoch,
                    owner,
                    owner.Token));
        }
    }

    private async Task RecoverAsync(
        ProcessGeneration crashedGeneration,
        long expectedRetirementEpoch,
        CancellationTokenSource owner,
        CancellationToken cancellationToken)
    {
        ProcessGeneration? stale = crashedGeneration;
        try
        {
            while (true)
            {
                int attempt;
                lock (_generationGate)
                {
                    if (!CanContinueRecoveryUnsafe(
                        stale,
                        expectedRetirementEpoch))
                    {
                        return;
                    }
                    attempt = ++_restartAttempts;
                }
                TimeSpan delay = GetRecoveryDelay(attempt);
                await Task.Delay(delay, cancellationToken).ConfigureAwait(false);
                AsyncFifoGate.Lease lifecycle =
                    await _lifecycle.EnterAsync(cancellationToken)
                        .ConfigureAwait(false);
                StatusDispatchReservation? reservation = null;
                try
                {
                    lock (_generationGate)
                    {
                        if (!CanContinueRecoveryUnsafe(
                            stale,
                            expectedRetirementEpoch,
                            enforceAttemptCap: false))
                        {
                            return;
                        }
                    }

                    if (stale is not null)
                    {
                        await TeardownGenerationAsync(
                            stale,
                            requestGraceful: false).ConfigureAwait(false);
                    }
                    stale = null;

                    try
                    {
                        reservation = ReserveStatusDispatch();
                        await StartGenerationAsync(
                            cancellationToken,
                            expectedRetirementEpoch)
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
                        lock (_generationGate)
                        {
                            if (!CanContinueRecoveryUnsafe(
                                stale: null,
                                expectedRetirementEpoch: expectedRetirementEpoch,
                                enforceAttemptCap: false))
                            {
                                throw;
                            }
                        }
                        // The active epoch owns this startup failure. Its failed
                        // generation is clean, so retry while the cap allows.
                    }
                }
                finally
                {
                    lifecycle.Dispose();
                    reservation?.Dispose();
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
            lock (_generationGate)
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
                if (_generation is { } current)
                    BeginRecoveryUnsafe(current);
            }
        }
    }

    private bool CanRecoverGenerationUnsafe(ProcessGeneration generation)
        => ReferenceEquals(_generation, generation)
            && generation.Phase == ProcessGeneration.Exited
            && generation.Epoch == _retirementEpoch
            && _restartSuppressed == 0
            && _pendingStartAdmissions == 0
            && Volatile.Read(ref _disposed) == 0
            && _restartAttempts < _options.CrashRestartLimit;

    private bool CanContinueRecoveryUnsafe(
        ProcessGeneration? stale,
        long expectedRetirementEpoch,
        bool enforceAttemptCap = true)
        => _restartSuppressed == 0
            && Volatile.Read(ref _disposed) == 0
            && _retirementEpoch == expectedRetirementEpoch
            && (!enforceAttemptCap
                || _restartAttempts < _options.CrashRestartLimit)
            && (stale is null
                ? _generation is null
                : ReferenceEquals(_generation, stale)
                    && stale.Phase == ProcessGeneration.Exited
                    && stale.Epoch == expectedRetirementEpoch);

    private TimeSpan GetRecoveryDelay(int attempt)
    {
        double multiplier = Math.Pow(2, Math.Max(0, attempt - 1));
        double ticks = Math.Min(
            _options.CrashRestartInitialDelay.Ticks * multiplier,
            _options.CrashRestartMaximumDelay.Ticks);
        return TimeSpan.FromTicks((long)ticks);
    }

    private Task CommitStatus(PocketBaseStatus status)
    {
        var delivered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        lock (_statusGate)
        {
            CommitStatusUnsafe(status, delivered);
        }
        return delivered.Task;
    }

    private StatusDispatchReservation CommitUnexpectedExitStatus(
        PocketBaseStatus status)
    {
        var delivered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        lock (_statusGate)
        {
            ReserveStatusDispatchUnsafe();
            CommitStatusUnsafe(status, delivered);
        }
        return new StatusDispatchReservation(this);
    }

    private void CommitStatusUnsafe(
        PocketBaseStatus status,
        TaskCompletionSource delivered)
    {
        _status = status;
        _statusNotifications.Enqueue(new StatusNotification(
            status,
            StatusChanged,
            delivered));
    }

    private void DrainStatusNotifications()
    {
        if (TryBeginStatusDispatch())
            DispatchStatusNotifications();
    }

    private StatusDispatchReservation ReserveStatusDispatch()
    {
        lock (_statusGate)
            ReserveStatusDispatchUnsafe();
        return new StatusDispatchReservation(this);
    }

    private void ReserveStatusDispatchUnsafe()
    {
        checked
        {
            _statusDispatchReservations++;
        }
        if (!_dispatchingStatus)
        {
            _dispatchingStatus = true;
            _statusDispatchPending = true;
        }
    }

    private void ReleaseStatusDispatchReservation()
    {
        bool schedule = false;
        lock (_statusGate)
        {
            _statusDispatchReservations--;
            if (_statusDispatchReservations == 0 && _statusDispatchPending)
            {
                _statusDispatchPending = false;
                schedule = true;
            }
        }
        if (schedule)
            _ = Task.Run(DispatchStatusNotifications);
    }

    private bool TryBeginStatusDispatch()
    {
        lock (_statusGate)
        {
            if (_dispatchingStatus)
                return false;
            _dispatchingStatus = true;
            return true;
        }
    }

    private void DispatchStatusNotifications()
    {
        PocketBaseSupervisor? previousDispatcher = s_statusDispatcher;
        s_statusDispatcher = this;
        try
        {
            while (true)
            {
                StatusNotification notification;
                lock (_statusGate)
                {
                    if (_statusDispatchReservations != 0)
                    {
                        _statusDispatchPending = true;
                        return;
                    }
                    if (!_statusNotifications.TryDequeue(out notification))
                    {
                        _dispatchingStatus = false;
                        return;
                    }
                }
                try
                {
                    if (notification.Observers is not { } observers)
                        continue;
                    foreach (Delegate callback in observers.GetInvocationList())
                    {
                        try
                        {
                            ((Action<object?, PocketBaseStatus>)callback)(
                                this,
                                notification.Status);
                        }
                        catch
                        {
                            // One observer never controls process lifetime or prevents
                            // the remaining commit-time snapshot from seeing the event.
                        }
                    }
                }
                finally
                {
                    notification.Delivered.TrySetResult();
                }
            }
        }
        finally
        {
            s_statusDispatcher = previousDispatcher;
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

    private readonly record struct StatusNotification(
        PocketBaseStatus Status,
        Action<object?, PocketBaseStatus>? Observers,
        TaskCompletionSource Delivered);

    private readonly record struct StartGenerationResult(Task ReadyDelivered);

    private sealed class StatusDispatchReservation(
        PocketBaseSupervisor owner) : IDisposable
    {
        private PocketBaseSupervisor? _owner = owner;

        public void Dispose()
            => Interlocked.Exchange(ref _owner, null)
                ?.ReleaseStatusDispatchReservation();
    }

    private sealed class ProcessGeneration
    {
        internal const int Starting = 0;
        internal const int Ready = 1;
        internal const int Exited = 2;
        internal const int Stopping = 3;

        private string _sessionSecret;
        private CancellationTokenSource? _pumpCancellation = new();

        internal ProcessGeneration(
            IPocketBaseProcess process,
            string sessionSecret,
            long epoch)
        {
            Process = process;
            _sessionSecret = sessionSecret;
            Epoch = epoch;
        }

        internal IPocketBaseProcess Process { get; }
        internal string SessionSecret => _sessionSecret;
        internal CancellationToken PumpCancellation => _pumpCancellation!.Token;
        internal EventHandler? ExitHandler { get; set; }
        internal Task? StdoutPump { get; set; }
        internal Task? StderrPump { get; set; }
        internal Uri? BaseAddress { get; set; }
        internal long Epoch { get; set; }
        internal int Phase = Starting;

        internal CancellationTokenSource? TakePumpCancellation()
            => Interlocked.Exchange(ref _pumpCancellation, null);

        internal void ReleaseSecret() => _sessionSecret = string.Empty;
    }

    private sealed record ReadyRecord(
        string? Contract,
        string? Event,
        string? Address,
        int Pid,
        PocketBaseBuildIdentity? Build);
}
