namespace VibeTable.Infrastructure.Backend;

/// <summary>
/// Lifecycle state of the supervised Python backend process.
/// </summary>
/// <remarks>
/// <para>
/// Legal transitions:
/// </para>
/// <list type="bullet">
/// <item>Happy path: <see cref="Stopped"/> → <see cref="Starting"/> →
/// <see cref="Ready"/> → <see cref="Stopping"/> → <see cref="Stopped"/>.</item>
/// <item>Failure path: <see cref="Starting"/> or <see cref="Ready"/> →
/// <see cref="Faulted"/> (handshake failure, timeout, unexpected exit,
/// protocol mismatch, executable not found).</item>
/// <item>From <see cref="Faulted"/>: a subsequent <c>StopAsync</c> may bring
/// the state to <see cref="Stopped"/> for cleanup symmetry, but the process
/// is already gone.</item>
/// </list>
/// <para>
/// The state is published atomically and surfaced through
/// <see cref="PythonBackendSupervisor.StateChanged"/>.
/// </para>
/// </remarks>
public enum BackendState
{
    /// <summary>
    /// The supervisor has not started a process, or the process has been
    /// stopped and fully disposed.
    /// </summary>
    Stopped = 0,

    /// <summary>
    /// The process has been spawned and the supervisor is performing the
    /// <c>system.handshake</c>. RPC is not yet generally available.
    /// </summary>
    Starting = 1,

    /// <summary>
    /// The handshake completed successfully; the <see cref="JsonRpcClient"/>
    /// is ready for general RPC traffic.
    /// </summary>
    Ready = 2,

    /// <summary>
    /// A stop is in progress: stdin has been closed and the supervisor is
    /// waiting for the process to exit, with a force-kill deadline pending.
    /// </summary>
    Stopping = 3,

    /// <summary>
    /// The backend failed irrecoverably (spawn failure, handshake timeout,
    /// protocol mismatch, or unexpected exit). The process is gone. The only
    /// legal next step is <see cref="PythonBackendSupervisor.StopAsync"/>.
    /// </summary>
    Faulted = 4,
}
