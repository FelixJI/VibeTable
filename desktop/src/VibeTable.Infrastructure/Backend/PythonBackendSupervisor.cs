using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Runtime.ExceptionServices;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Diagnostics;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Infrastructure.Backend;

/// <summary>
/// Spawns and supervises the Python backend process: owns the
/// <see cref="System.Diagnostics.Process"/>, a Windows Job Object with
/// kill-on-close, the <see cref="JsonRpcClient"/> over the redirected
/// stdin/stdout, and the <see cref="BackendState"/> lifecycle.
/// </summary>
/// <remarks>
/// <para>
/// The supervisor is the single owner of the backend process for a host
/// session. <see cref="StartAsync"/> performs a synchronous handshake over
/// the JSON-RPC client and only transitions to <see cref="BackendState.Ready"/>
/// once <c>system.handshake</c> returns a protocol-matching result. Any
/// failure (spawn error, timeout, protocol mismatch, unexpected exit) flips
/// the state to <see cref="BackendState.Faulted"/> and rethrows to the caller.
/// </para>
/// <para>
/// <b>Process isolation.</b> The child is created with
/// <see cref="ProcessStartInfo.UseShellExecute"/>=false, redirected
/// stdin/stdout/stderr, UTF-8 encodings, and no visible window. It is bound
/// to a Job Object carrying <c>JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE</c> so that
/// a host crash or a <see cref="Dispose"/> reliably kills the child (and any
/// descendants it later spawns) — no orphaned <c>python.exe</c> can outlive
/// the host.
/// </para>
/// <para>
/// <b>Graceful vs forced stop.</b> <see cref="StopAsync"/> closes the
/// redirected stdin so a well-behaved backend that exits on EOF shuts down
/// cleanly within <see cref="BackendLaunchOptions.StopTimeout"/>. If the
/// process is still alive at the deadline, the supervisor force-kills it
/// (still inside the Job Object, so the kill is belt-and-braces) and reports
/// <see cref="ExitCode"/> as the observed value, or
/// <see cref="ForcedKillExitCode"/> when we had to terminate it ourselves.
/// </para>
/// <para>
/// <b>stderr capture.</b> stderr is read on a background task into an
/// in-memory buffer (and optionally tee'd to <see cref="BackendLaunchOptions.LogPath"/>)
/// so diagnostics are available even when the backend faulted before the
/// first RPC.
/// </para>
/// <para>
/// Lifecycle entry points are serialized. A caller that arrives during a
/// stop waits for the owning process generation to finish teardown instead
/// of observing an intermediate state or returning while readers still own
/// the retired process streams.
/// </para>
/// </remarks>
public sealed class PythonBackendSupervisor : IBackendSupervisor
{
    /// <summary>
    /// Synthetic exit code reported by <see cref="ExitCode"/> when the
    /// supervisor had to force-kill the process at the stop deadline. The
    /// process' actual exit code is meaningless in that case (killed, not
    /// returned), so we surface this sentinel instead.
    /// </summary>
    public const int ForcedKillExitCode = -1;

    private static readonly string ClientVersion =
        ApplicationVersion.FromAssembly(typeof(PythonBackendSupervisor).Assembly);
    private const string ProtocolVersion = "1.0";

    private readonly BackendLaunchOptions _options;
    private readonly SemaphoreSlim _lifecycle = new(1, 1);
    private readonly object _stateGate = new();
    private readonly object _disposeGate = new();

    private BackendState _state = BackendState.Stopped;
    private ProcessGeneration? _generation;
    private string _lastStdError = string.Empty;
    private bool _lastHasExited;
    private int? _lastExitCode;
    private Task? _disposeTask;
    private int _disposed;

    public PythonBackendSupervisor(BackendLaunchOptions options)
    {
        _options = options ?? throw new ArgumentNullException(nameof(options));
        if (string.IsNullOrWhiteSpace(_options.Command))
        {
            throw new ArgumentException(
                "BackendLaunchOptions.Command must be non-empty.", nameof(options));
        }
    }

    /// <summary>
    /// Current lifecycle state. Safe to read from any thread; reflects the
    /// most recently published transition.
    /// </summary>
    public BackendState State
    {
        get
        {
            lock (_stateGate)
            {
                return _state;
            }
        }
    }

    /// <summary>
    /// The <see cref="JsonRpcClient"/> over the backend's redirected
    /// stdin/stdout. Available after <see cref="StartAsync"/> returns
    /// successfully; <c>null</c> before that.
    /// </summary>
    public JsonRpcClient? Client
    {
        get
        {
            lock (_stateGate)
            {
                return _generation?.Client;
            }
        }
    }

    /// <summary>
    /// Admits a short synchronous action against the exact Ready client while
    /// holding the state gate. The action must not call lifecycle entry points
    /// or wait for asynchronous work; an RPC start may capture its pending task
    /// for awaiting outside the action.
    /// This does not lease the client beyond the action or prevent process exit.
    /// </summary>
    internal bool TryUseReadyClient(Func<JsonRpcClient, bool> action)
    {
        ArgumentNullException.ThrowIfNull(action);
        lock (_stateGate)
        {
            return Volatile.Read(ref _disposed) == 0
                && _state == BackendState.Ready
                && _generation?.Client is { } client
                && action(client);
        }
    }

    /// <summary>
    /// Raised on every state transition. Handlers run on the thread that
    /// published the change.
    /// </summary>
    public event Action<object?, BackendState>? StateChanged;

    /// <summary>
    /// Raised for each diagnostic line written by the backend to stderr.
    /// stdout is reserved for JSON-RPC frames and is never exposed as a log
    /// stream. Handlers run on the background stderr reader and must return
    /// promptly.
    /// </summary>
    public event Action<object?, string>? LogReceived;

    /// <summary>
    /// True once the child process has exited (cleanly or otherwise).
    /// </summary>
    public bool HasExited
    {
        get
        {
            lock (_stateGate)
            {
                return _generation?.HasExited ?? _lastHasExited;
            }
        }
    }

    /// <summary>
    /// The child's observed exit code, when available.
    /// <see cref="ForcedKillExitCode"/> indicates the supervisor force-killed
    /// the process at the stop deadline.
    /// </summary>
    public int? ExitCode
    {
        get
        {
            lock (_stateGate)
            {
                return _generation?.ExitCode ?? _lastExitCode;
            }
        }
    }

    /// <summary>
    /// Spawns the backend, binds it to a kill-on-close Job Object, performs
    /// the <c>system.handshake</c>, and transitions to
    /// <see cref="BackendState.Ready"/>. Throws on any failure (spawn error,
    /// timeout, protocol mismatch, unexpected exit) and leaves the supervisor
    /// in <see cref="BackendState.Faulted"/>.
    /// </summary>
    public async Task StartAsync(CancellationToken cancellationToken)
    {
        ThrowIfDisposed();
        await _lifecycle.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            ThrowIfDisposed();
            BackendState state = State;
            if (state is BackendState.Ready or BackendState.Starting
                or BackendState.Stopping)
            {
                throw new InvalidOperationException(
                    $"Cannot StartAsync in state {state}.");
            }

            ProcessGeneration? previous = Volatile.Read(ref _generation);
            if (previous is not null)
            {
                await TeardownGenerationAsync(previous, requestGraceful: false)
                    .ConfigureAwait(false);
            }

            TimeSpan stopTimeout = ValidateStopTimeout(_options.StopTimeout);

            lock (_stateGate)
            {
                _lastStdError = string.Empty;
                _lastHasExited = false;
                _lastExitCode = null;
                TransitionLocked(BackendState.Starting);
            }

            ProcessGeneration? generation = null;
            try
            {
                (Process process, JobObject job) = SpawnProcessAndBindJob();
                generation = new ProcessGeneration(process, job, stopTimeout);
                lock (_stateGate)
                {
                    _generation = generation;
                }

                generation.ExitHandler = (_, _) => OnProcessExited(generation);
                process.Exited += generation.ExitHandler;
                generation.StderrTask = StartStderrCapture(generation);

                var transport = new StreamJsonLineTransport(
                    process.StandardOutput.BaseStream,
                    process.StandardInput.BaseStream);
                generation.Transport = transport;
                var client = new JsonRpcClient(transport);
                generation.Client = client;

                using var handshakeCts = CancellationTokenSource.CreateLinkedTokenSource(
                    cancellationToken);
                handshakeCts.CancelAfter(_options.StartupTimeout);

                try
                {
                    var result = await client.InvokeAsync<
                            HandshakeParams,
                            HandshakeResult>(
                        "system.handshake",
                        new HandshakeParams(ClientVersion, ProtocolVersion),
                        handshakeCts.Token)
                        .ConfigureAwait(false);

                    if (!string.Equals(
                            result.ProtocolVersion,
                            ProtocolVersion,
                            StringComparison.Ordinal))
                    {
                        throw new InvalidOperationException(
                            $"Backend protocol mismatch: requested '{ProtocolVersion}'" +
                            $" but backend reported '{result.ProtocolVersion}'.");
                    }

                    if (!generation.TryMarkReady())
                    {
                        throw new InvalidOperationException(
                            "Backend exited before Ready transition.");
                    }
                    lock (_stateGate)
                    {
                        if (!ReferenceEquals(_generation, generation)
                            || _state == BackendState.Faulted
                            || generation.HasExited)
                        {
                            throw new InvalidOperationException(
                                "Backend exited before Ready transition.");
                        }
                        TransitionLocked(BackendState.Ready);
                    }
                }
                catch (OperationCanceledException) when (
                    !cancellationToken.IsCancellationRequested)
                {
                    throw new TimeoutException(
                        $"Backend did not complete system.handshake within " +
                        $"{_options.StartupTimeout.TotalSeconds:F1}s.");
                }
                catch (RpcException ex) when (
                    ex is BackendUnavailableException
                    || ex.Message.Contains(
                        "handshake",
                        StringComparison.OrdinalIgnoreCase))
                {
                    throw new InvalidOperationException(
                        $"system.handshake failed: {ex.Message}", ex);
                }
            }
            catch
            {
                lock (_stateGate)
                {
                    if (_state != BackendState.Faulted)
                    {
                        TransitionLocked(BackendState.Faulted);
                    }
                    if (generation is null)
                    {
                        _lastHasExited = true;
                    }
                }
                if (generation is not null)
                {
                    await TeardownGenerationAsync(
                        generation,
                        requestGraceful: false).ConfigureAwait(false);
                }
                throw;
            }
        }
        finally
        {
            _lifecycle.Release();
        }
    }

    /// <summary>
    /// Graceful stop: closes stdin (so a well-behaved backend exits on EOF),
    /// waits up to <see cref="BackendLaunchOptions.StopTimeout"/>, then
    /// force-kills if the process is still alive. Safe to call from a faulted
    /// <see cref="StartAsync"/>: the supervisor preserves the Faulted state
    /// (the process is already gone) and just releases resources.
    /// </summary>
    public async Task StopAsync(CancellationToken cancellationToken)
    {
        if (Volatile.Read(ref _disposed) != 0)
        {
            Task? disposeTask;
            lock (_disposeGate)
            {
                disposeTask = _disposeTask;
            }
            if (disposeTask is not null)
            {
                await disposeTask.ConfigureAwait(false);
            }
            return;
        }

        await StopCoreAsync(cancellationToken).ConfigureAwait(false);
    }

    private async Task StopCoreAsync(CancellationToken cancellationToken)
    {
        await _lifecycle.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            ProcessGeneration? generation = Volatile.Read(ref _generation);
            BackendState state = State;
            if (state == BackendState.Stopped && generation is null)
            {
                return;
            }

            if (state == BackendState.Faulted)
            {
                if (generation is not null)
                {
                    await TeardownGenerationAsync(
                        generation,
                        requestGraceful: false).ConfigureAwait(false);
                }
                return;
            }

            if (generation is null)
            {
                lock (_stateGate)
                {
                    TransitionLocked(BackendState.Stopped);
                }
                return;
            }

            lock (_stateGate)
            {
                TransitionLocked(BackendState.Stopping);
            }
            try
            {
                await TeardownGenerationAsync(generation, requestGraceful: true)
                    .ConfigureAwait(false);
            }
            finally
            {
                lock (_stateGate)
                {
                    TransitionLocked(BackendState.Stopped);
                }
            }
        }
        finally
        {
            _lifecycle.Release();
        }

        cancellationToken.ThrowIfCancellationRequested();
    }

    /// <summary>
    /// Returns the captured stderr text for the active generation, or the
    /// most recently retired generation when no backend is running.
    /// </summary>
    public string GetStdErrorLog()
    {
        lock (_stateGate)
        {
            return _generation?.GetStdErrorLog() ?? _lastStdError;
        }
    }

    public ValueTask DisposeAsync()
    {
        lock (_disposeGate)
        {
            _disposeTask ??= DisposeCoreAsync();
            return new ValueTask(_disposeTask);
        }
    }

    private async Task DisposeCoreAsync()
    {
        Interlocked.Exchange(ref _disposed, 1);
        try
        {
            await StopCoreAsync(CancellationToken.None).ConfigureAwait(false);
        }
        catch
        {
            // Dispose must not throw.
        }
    }

    // ---------- internals ----------

    private (Process Process, JobObject Job) SpawnProcessAndBindJob()
    {
        var psi = new ProcessStartInfo
        {
            FileName = _options.Command,
            Arguments = _options.Arguments,
            UseShellExecute = false,
            RedirectStandardInput = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            CreateNoWindow = true,
            StandardOutputEncoding = Encoding.UTF8,
            StandardErrorEncoding = Encoding.UTF8,
            // Leave stdin at the default UTF-8 (no encoding property exposed
            // for input on all TFMs); the transport writes raw bytes.
        };

        if (!string.IsNullOrEmpty(_options.WorkingDirectory))
        {
            psi.WorkingDirectory = _options.WorkingDirectory;
        }

        foreach (var kv in _options.Environment)
        {
            psi.Environment[kv.Key] = kv.Value;
        }

        var process = new Process
        {
            StartInfo = psi,
            EnableRaisingEvents = true,
        };

        JobObject job;
        try
        {
            job = JobObject.Create();
        }
        catch
        {
            process.Dispose();
            throw;
        }

        // Create the Job Object BEFORE Start so we can assign immediately. On
        // Windows, Process.Start raises Win32Exception directly when the file
        // cannot be found (instead of returning false); catch it so the
        // supervisor can transition to Faulted uniformly.
        try
        {
            if (!process.Start())
            {
                int code = System.Runtime.InteropServices.Marshal.GetLastWin32Error();
                throw new InvalidOperationException(
                    $"Failed to spawn backend '{_options.Command} {_options.Arguments}'. " +
                    $"Win32 error {code}.");
            }
        }
        catch (System.ComponentModel.Win32Exception ex)
        {
            process.Dispose();
            job.Dispose();
            throw new InvalidOperationException(
                $"Failed to spawn backend '{_options.Command} {_options.Arguments}': " +
                $"{ex.Message}", ex);
        }

        // Bind the child to the Job Object ASAP so descendants are covered.
        // SafeProcessHandle gives us the OS handle we need to call
        // AssignProcessToJobObject.
        if (JobObject.IsSupported)
        {
            try
            {
                job.AssignProcess(process.SafeHandle.DangerousGetHandle());
            }
            catch
            {
                // Soft-failure: Dispose path still force-kills via Process.Kill.
                // We deliberately don't surface this — the supervisor must
                // still function (with degraded cleanup) if Job Object
                // assignment is denied.
            }
        }

        return (process, job);
    }

    private Task StartStderrCapture(ProcessGeneration generation)
        => Task.Run(async () =>
        {
            RotatingLogSink? logWriter = null;
            try
            {
                if (!string.IsNullOrEmpty(_options.LogPath))
                {
                    logWriter = new RotatingLogSink(_options.LogPath!);
                }

                var sr = generation.Process.StandardError;
                // ReadLineAsync returns null at EOF.
                while (true)
                {
                    string? line;
                    try
                    {
                        line = await sr.ReadLineAsync().ConfigureAwait(false);
                    }
                    catch
                    {
                        // Reader failed (process gone). Stop the capture.
                        break;
                    }
                    if (line is null)
                    {
                        break;
                    }
                    generation.AppendStdError(line);
                    PublishLogLine(generation, line);
                    if (logWriter is not null && DiagnosticLogLine.IsSafe(line))
                    {
                        try
                        {
                            await logWriter.WriteLineAsync(line).ConfigureAwait(false);
                        }
                        catch
                        {
                            // Best-effort.
                        }
                    }
                }
            }
            catch
            {
                // The capture task must never fault — it would surface as an
                // unobserved exception. Swallow and exit.
            }
            finally
            {
                if (logWriter is not null)
                {
                    await logWriter.DisposeAsync().ConfigureAwait(false);
                }
            }
        });

    private void PublishLogLine(ProcessGeneration generation, string line)
    {
        Action<object?, string>? handler;
        lock (_stateGate)
        {
            if (!ReferenceEquals(_generation, generation))
            {
                return;
            }
            handler = LogReceived;
        }
        if (handler is null)
        {
            return;
        }
        try
        {
            handler(this, line);
        }
        catch
        {
            // A C# log consumer must never stop draining backend stderr.
        }
    }

    private void OnProcessExited(ProcessGeneration generation)
    {
        int previous = generation.ObserveExit();

        lock (_stateGate)
        {
            if (ReferenceEquals(_generation, generation)
                && previous is ProcessGeneration.Starting or ProcessGeneration.Ready
                && _state is BackendState.Starting or BackendState.Ready)
            {
                TransitionLocked(BackendState.Faulted);
            }
        }
    }

    private async Task TeardownGenerationAsync(
        ProcessGeneration generation,
        bool requestGraceful)
    {
        generation.MarkStopping();
        Exception? teardownFailure = null;
        if (generation.ExitHandler is not null)
        {
            try
            {
                generation.Process.Exited -= generation.ExitHandler;
            }
            catch (Exception ex)
            {
                teardownFailure ??= ex;
            }
        }

        if (requestGraceful && !generation.HasExited)
        {
            try
            {
                await SafeCloseStandardInputAsync(generation.Process)
                    .ConfigureAwait(false);

                using var stopCts = new CancellationTokenSource(generation.StopTimeout);
                try
                {
                    await generation.Process.WaitForExitAsync(stopCts.Token)
                        .ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    // Forceful cleanup below owns the remaining lifetime.
                }
            }
            catch (Exception ex)
            {
                teardownFailure ??= ex;
            }
        }

        if (!generation.HasExited)
        {
            bool killIssued = false;
            try
            {
                generation.Process.Kill(entireProcessTree: true);
                killIssued = true;
            }
            catch
            {
                // Closing the generation Job Object is the final hammer.
            }
            if (killIssued)
            {
                generation.MarkForceKilled();
            }
        }

        // The job is generation-owned. Closing it before the final join
        // guarantees descendants cannot retain stdout/stderr handles.
        try
        {
            generation.Job.Dispose();
        }
        catch (Exception ex)
        {
            teardownFailure ??= ex;
        }

        try
        {
            if (!generation.HasExited)
            {
                await generation.Process.WaitForExitAsync(CancellationToken.None)
                    .ConfigureAwait(false);
            }
        }
        catch (Exception ex)
        {
            teardownFailure ??= ex;
        }
        generation.CaptureExitCode();

        if (generation.Client is not null)
        {
            try
            {
                await generation.Client.DisposeAsync().ConfigureAwait(false);
            }
            catch (Exception ex)
            {
                // Awaiting a faulted dispose still joins the client reader.
                teardownFailure ??= ex;
            }
        }
        else if (generation.Transport is not null)
        {
            try
            {
                await generation.Transport.DisposeAsync().ConfigureAwait(false);
            }
            catch (Exception ex)
            {
                teardownFailure ??= ex;
            }
        }

        if (generation.StderrTask is not null)
        {
            try
            {
                await generation.StderrTask.ConfigureAwait(false);
            }
            catch (Exception ex)
            {
                // Awaiting a faulted task still joins the stderr pump.
                teardownFailure ??= ex;
            }
        }

        try
        {
            generation.Job.Dispose();
        }
        catch (Exception ex)
        {
            teardownFailure ??= ex;
        }
        try
        {
            generation.Process.EnableRaisingEvents = false;
        }
        catch
        {
            // Best-effort.
        }
        generation.CaptureExitCode();
        try
        {
            generation.Process.Dispose();
        }
        catch (Exception ex)
        {
            teardownFailure ??= ex;
        }
        lock (_stateGate)
        {
            if (ReferenceEquals(_generation, generation))
            {
                _lastStdError = generation.GetStdErrorLog();
                _lastHasExited = generation.HasExited;
                _lastExitCode = generation.ExitCode;
                _generation = null;
            }
        }

        if (teardownFailure is not null)
        {
            ExceptionDispatchInfo.Capture(teardownFailure).Throw();
        }
    }

    private async ValueTask SafeCloseStandardInputAsync(Process process)
    {
        // Closing the StreamWriter (StandardInput) flushes any pending bytes
        // and signals EOF on the pipe. We do this on a background thread so a
        // stuck flush can't block the stop deadline.
        await Task.Run(() =>
        {
            try
            {
                process.StandardInput.Close();
            }
            catch
            {
                // Already closed or process gone.
            }
        }).ConfigureAwait(false);
    }

    private void ThrowIfDisposed()
    {
        if (Volatile.Read(ref _disposed) != 0)
        {
            throw new ObjectDisposedException(nameof(PythonBackendSupervisor));
        }
    }

    private static TimeSpan ValidateStopTimeout(TimeSpan stopTimeout)
    {
        const double MaximumTimerMilliseconds = uint.MaxValue - 1d;
        if (stopTimeout <= TimeSpan.Zero
            || stopTimeout.TotalMilliseconds > MaximumTimerMilliseconds)
        {
            throw new ArgumentOutOfRangeException(
                nameof(BackendLaunchOptions.StopTimeout),
                stopTimeout,
                "Stop timeout must be positive and supported by the system timer.");
        }
        return stopTimeout;
    }

    private sealed class ProcessGeneration
    {
        internal const int Starting = 0;
        internal const int Ready = 1;
        internal const int Exited = 2;
        internal const int Stopping = 3;

        private readonly object _exitGate = new();
        private readonly StringBuilder _stderr = new();
        private int _phase = Starting;
        private int _hasExited;
        private bool _forceKilled;
        private bool _exitCodeSet;
        private int _exitCode;

        internal ProcessGeneration(
            Process process,
            JobObject job,
            TimeSpan stopTimeout)
        {
            Process = process;
            Job = job;
            StopTimeout = stopTimeout;
        }

        internal Process Process { get; }
        internal JobObject Job { get; }
        internal TimeSpan StopTimeout { get; }
        internal StreamJsonLineTransport? Transport { get; set; }
        internal JsonRpcClient? Client { get; set; }
        internal Task? StderrTask { get; set; }
        internal EventHandler? ExitHandler { get; set; }

        internal bool HasExited
        {
            get
            {
                if (Volatile.Read(ref _hasExited) != 0)
                {
                    return true;
                }
                CaptureExitCode();
                return Volatile.Read(ref _hasExited) != 0;
            }
        }

        internal int? ExitCode
        {
            get
            {
                CaptureExitCode();
                lock (_exitGate)
                {
                    return _exitCodeSet ? _exitCode : null;
                }
            }
        }

        internal bool TryMarkReady()
            => Interlocked.CompareExchange(ref _phase, Ready, Starting) == Starting;

        internal void MarkStopping()
            => Interlocked.Exchange(ref _phase, Stopping);

        internal int ObserveExit()
        {
            CaptureExitCode();
            Volatile.Write(ref _hasExited, 1);
            return Interlocked.Exchange(ref _phase, Exited);
        }

        internal void MarkForceKilled()
        {
            lock (_exitGate)
            {
                _forceKilled = true;
            }
        }

        internal void CaptureExitCode()
        {
            lock (_exitGate)
            {
                if (_exitCodeSet)
                {
                    return;
                }
                try
                {
                    if (!Process.HasExited)
                    {
                        return;
                    }
                    _exitCode = _forceKilled
                        ? ForcedKillExitCode
                        : Process.ExitCode;
                    _exitCodeSet = true;
                    Volatile.Write(ref _hasExited, 1);
                }
                catch
                {
                    // The final teardown join will retry before disposal.
                }
            }
        }

        internal void AppendStdError(string line)
        {
            lock (_stderr)
            {
                _stderr.AppendLine(line);
            }
        }

        internal string GetStdErrorLog()
        {
            lock (_stderr)
            {
                return _stderr.ToString();
            }
        }
    }

    private void TransitionLocked(BackendState next)
    {
        // Must be called under _stateGate.
        _state = next;
        // Fire the event outside the lock to avoid re-entrancy from handlers
        // that read State (which takes the lock). Snapshot the delegate first.
        var handler = StateChanged;
        if (handler is not null)
        {
            Task.Run(() =>
            {
                try
                {
                    handler(this, next);
                }
                catch
                {
                    // Handler exceptions must not break the supervisor.
                }
            });
        }
    }
}
