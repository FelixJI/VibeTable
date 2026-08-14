using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
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
/// This type is not thread-safe for concurrent <see cref="StartAsync"/>/
/// <see cref="StopAsync"/> calls; callers serialize lifecycle transitions.
/// Re-entrant <see cref="StopAsync"/> from inside a faulted
/// <see cref="StartAsync"/> is supported (the inner call no-ops).
/// </para>
/// </remarks>
public sealed class PythonBackendSupervisor : IAsyncDisposable
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
    private readonly object _stateGate = new();
    private readonly StringBuilder _stderrBuffer = new();
    private readonly TaskCompletionSource<bool> _exitedTcs =
        new(TaskCreationOptions.RunContinuationsAsynchronously);

    private BackendState _state = BackendState.Stopped;
    private Process? _process;
    private JobObject? _job;
    private StreamJsonLineTransport? _transport;
    private JsonRpcClient? _client;
    private Task? _stderrTask;
    private bool _stopInProgress;
    private int _exitCode;
    private bool _exitCodeSet;
    private bool _forceKilled;
    private bool _hasExited;
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
                return _client;
            }
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
    public bool HasExited => Volatile.Read(ref _hasExited);

    /// <summary>
    /// The child's observed exit code, when available.
    /// <see cref="ForcedKillExitCode"/> indicates the supervisor force-killed
    /// the process at the stop deadline.
    /// </summary>
    public int? ExitCode =>
        _exitCodeSet ? _exitCode : (int?)null;

    /// <summary>
    /// Spawns the backend, binds it to a kill-on-close Job Object, performs
    /// the <c>system.handshake</c>, and transitions to
    /// <see cref="BackendState.Ready"/>. Throws on any failure (spawn error,
    /// timeout, protocol mismatch, unexpected exit) and leaves the supervisor
    /// in <see cref="BackendState.Faulted"/>.
    /// </summary>
    public async Task StartAsync(CancellationToken cancellationToken)
    {
        lock (_stateGate)
        {
            if (_state is BackendState.Ready or BackendState.Starting)
            {
                throw new InvalidOperationException(
                    $"Cannot StartAsync in state {_state}.");
            }
            if (_disposed != 0)
            {
                throw new ObjectDisposedException(nameof(PythonBackendSupervisor));
            }
            TransitionLocked(BackendState.Starting);
        }

        bool succeeded = false;
        try
        {
            (_process, _job) = SpawnProcessAndBindJob();
            StartStderrCapture(_process);

            // Construct the transport over the redirected streams and a client
            // to drive the handshake. The client starts its reader loop
            // immediately (see JsonRpcClient ctor).
            var transport = new StreamJsonLineTransport(
                _process.StandardOutput.BaseStream,
                _process.StandardInput.BaseStream);
            _transport = transport;
            var client = new JsonRpcClient(transport);
            _client = client;

            // Wire the process Exited event AFTER the client is constructed so
            // the handshake cancellation below can race with an exit. The
            // exited handler never pre-empts an already-completed handshake.
            _process.EnableRaisingEvents = true;
            _process.Exited += OnProcessExited;

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

                // Protocol-mismatch guard: the backend returned a result with
                // a different protocol version. Transition to Faulted.
                if (!string.Equals(
                        result.ProtocolVersion,
                        ProtocolVersion,
                        StringComparison.Ordinal))
                {
                    throw new InvalidOperationException(
                        $"Backend protocol mismatch: requested '{ProtocolVersion}'" +
                        $" but backend reported '{result.ProtocolVersion}'.");
                }

                lock (_stateGate)
                {
                    if (_state == BackendState.Faulted)
                    {
                        // The process exited between handshake success and now.
                        throw new InvalidOperationException(
                            "Backend exited before Ready transition.");
                    }
                    TransitionLocked(BackendState.Ready);
                }
                succeeded = true;
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
                || (ex.Message.Contains("handshake", StringComparison.OrdinalIgnoreCase)))
            {
                // Backend gone mid-handshake OR rejected by the dispatcher.
                // Surface as a fatal start failure.
                throw new InvalidOperationException(
                    $"system.handshake failed: {ex.Message}", ex);
            }
        }
        finally
        {
            if (!succeeded)
            {
                // Transition to Faulted and tear down everything. StopAsync
                // from inside this finally would re-enter; do the teardown
                // directly to avoid recursion guards.
                FaultAndTeardownAsync().GetAwaiter().GetResult();
            }
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
        if (_disposed != 0)
        {
            return;
        }

        lock (_stateGate)
        {
            if (_state == BackendState.Faulted)
            {
                // The process is already gone. Do NOT transition to Stopped;
                // Faulted is a terminal state and tests assert it stays.
                // Release any leftover process/job handles.
                TeardownProcess_locked(graceful: false);
                return;
            }
            if (_state == BackendState.Stopped)
            {
                return;
            }
            if (_stopInProgress)
            {
                return;
            }
            _stopInProgress = true;
            TransitionLocked(BackendState.Stopping);
        }

        try
        {
            // Graceful: close stdin so the backend sees EOF and exits.
            try
            {
                if (_process is not null && !_process.HasExited)
                {
                    await SafeCloseStandardInputAsync(_process).ConfigureAwait(false);
                }
            }
            catch
            {
                // Closing stdin must never block stop. If it throws, fall
                // through to the force-kill path.
            }

            // Wait for the process to exit on its own within the deadline.
            using var forceCts = CancellationTokenSource.CreateLinkedTokenSource(
                cancellationToken);
            forceCts.CancelAfter(_options.StopTimeout);
            bool exited = false;
            try
            {
                await _process!.WaitForExitAsync(forceCts.Token).ConfigureAwait(false);
                exited = true;
            }
            catch (OperationCanceledException) when (
                !cancellationToken.IsCancellationRequested)
            {
                // StopTimeout elapsed; fall through to force-kill.
            }

            if (!exited && _process is not null && !_process.HasExited)
            {
                try
                {
                    _process.Kill(entireProcessTree: true);
                    _forceKilled = true;
                }
                catch
                {
                    // Best-effort; the Job Object will kill on Dispose regardless.
                }
                try
                {
                    await _process.WaitForExitAsync(cancellationToken)
                        .WaitAsync(TimeSpan.FromSeconds(5), cancellationToken)
                        .ConfigureAwait(false);
                }
                catch
                {
                    // The Job Object close in Dispose is the final hammer.
                }
            }

            CaptureExitCode();
            TeardownProcess_locked(graceful: !_forceKilled);
            TransitionLocked(BackendState.Stopped);
        }
        finally
        {
            lock (_stateGate)
            {
                _stopInProgress = false;
            }
        }
    }

    /// <summary>
    /// Returns the captured stderr text written by the backend so far.
    /// Available even when the supervisor faulted before the first RPC.
    /// </summary>
    public string GetStdErrorLog()
    {
        lock (_stderrBuffer)
        {
            return _stderrBuffer.ToString();
        }
    }

    public async ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0)
        {
            return;
        }

        try
        {
            if (_state is not (BackendState.Stopped or BackendState.Faulted))
            {
                // Best-effort graceful stop from Dispose.
                try
                {
                    await StopAsync(CancellationToken.None).ConfigureAwait(false);
                }
                catch
                {
                    // Dispose must not throw.
                }
            }
            else if (_state == BackendState.Faulted)
            {
                // Already faulted; just make sure resources are released.
                // Preserve the Faulted terminal state.
                lock (_stateGate)
                {
                    TeardownProcess_locked(graceful: false);
                }
            }
        }
        catch
        {
            // Dispose must not throw.
        }

        // Dispose the client last so any in-flight RPC reads drain the
        // transport before we close it.
        if (_client is not null)
        {
            try
            {
                await _client.DisposeAsync().ConfigureAwait(false);
            }
            catch
            {
                // Dispose must not throw.
            }
        }

        try
        {
            if (_stderrTask is not null)
            {
                // Give the stderr capture a chance to finish; it exits on EOF
                // when the process is torn down.
                await _stderrTask.WaitAsync(TimeSpan.FromSeconds(1),
                    CancellationToken.None).ConfigureAwait(false);
            }
        }
        catch
        {
            // Best-effort drain.
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

    private void StartStderrCapture(Process process)
    {
        _stderrTask = Task.Run(async () =>
        {
            RotatingLogSink? logWriter = null;
            try
            {
                if (!string.IsNullOrEmpty(_options.LogPath))
                {
                    logWriter = new RotatingLogSink(_options.LogPath!);
                }

                var sr = process.StandardError;
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
                    lock (_stderrBuffer)
                    {
                        _stderrBuffer.AppendLine(line);
                    }
                    PublishLogLine(line);
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
    }

    private void PublishLogLine(string line)
    {
        var handler = LogReceived;
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

    private void OnProcessExited(object? sender, EventArgs e)
    {
        // Mark the exit-tcs so any waiter wakes. If this exit was
        // unexpected (i.e. we are in Starting/Ready), transition to Faulted.
        _exitedTcs.TrySetResult(true);
        Volatile.Write(ref _hasExited, true);
        CaptureExitCode();

        lock (_stateGate)
        {
            if (_state is BackendState.Starting or BackendState.Ready)
            {
                TransitionLocked(BackendState.Faulted);
            }
        }
    }

    private void CaptureExitCode()
    {
        try
        {
            if (_process is not null && _process.HasExited && !_exitCodeSet)
            {
                if (_forceKilled)
                {
                    _exitCode = ForcedKillExitCode;
                }
                else
                {
                    _exitCode = _process.ExitCode;
                }
                _exitCodeSet = true;
                Volatile.Write(ref _hasExited, true);
            }
        }
        catch
        {
            // Race: process not fully cleaned up. Leave the exit code unset.
        }
    }

    private async Task FaultAndTeardownAsync()
    {
        lock (_stateGate)
        {
            if (_state != BackendState.Faulted)
            {
                TransitionLocked(BackendState.Faulted);
            }
            // If the process was never spawned (e.g. executable not found),
            // there is no child to observe — mark it as "exited" so callers
            // querying HasExited after a faulted start see a vacuous "gone".
            if (_process is null)
            {
                Volatile.Write(ref _hasExited, true);
            }
        }

        // Kill the process if it is still running. We don't close stdin
        // (graceful) here because we are on the fault path: kill hard so the
        // caller's StopAsync finalizes cleanly.
        try
        {
            if (_process is not null && !_process.HasExited)
            {
                try
                {
                    _process.Kill(entireProcessTree: true);
                    _forceKilled = true;
                }
                catch
                {
                    // The Job Object Dispose below is the backstop.
                }
            }
        }
        catch
        {
            // Best-effort.
        }

        CaptureExitCode();

        // Give the process a moment to exit so HasExited is true on return.
        if (_process is not null && !_process.HasExited)
        {
            try
            {
                await _process.WaitForExitAsync()
                    .WaitAsync(TimeSpan.FromSeconds(2), CancellationToken.None)
                    .ConfigureAwait(false);
            }
            catch
            {
                // Best-effort.
            }
        }

        // Tear down transport + client, but DO NOT transition state to Stopped
        // — the caller's StopAsync will run from the finally above and the
        // Faulted state is preserved until then.
        if (_client is not null)
        {
            try
            {
                await _client.DisposeAsync().ConfigureAwait(false);
                _client = null;
            }
            catch
            {
                // Best-effort.
            }
        }
    }

    private void TeardownProcess_locked(bool graceful)
    {
        // Releases process/job handles. Must be called under _stateGate.
        if (_process is not null)
        {
            try
            {
                _process.EnableRaisingEvents = false;
            }
            catch
            {
                // Best-effort.
            }
            try
            {
                if (!_process.HasExited)
                {
                    try
                    {
                        _process.Kill(entireProcessTree: true);
                        _forceKilled = true;
                    }
                    catch
                    {
                        // The Job Object close below is the backstop.
                    }
                    // Wait briefly for the kill to take effect so HasExited
                    // is observable after return.
                    try
                    {
                        _process.WaitForExit(2000);
                    }
                    catch
                    {
                        // Best-effort.
                    }
                }
            }
            catch
            {
                // Best-effort.
            }

            CaptureExitCode();
            Volatile.Write(ref _hasExited, true);
            _process.Dispose();
            _process = null!;
        }

        if (_job is not null)
        {
            // Closing the job handle triggers kill-on-close for any process
            // that was still alive (e.g. grandchild that escaped the
            // Process.Kill tree).
            _job.Dispose();
            _job = null;
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
