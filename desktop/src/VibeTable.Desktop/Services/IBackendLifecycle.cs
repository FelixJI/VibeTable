using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Backend process lifecycle as seen from the WPF shell. Hides the concrete
/// <c>PythonBackendSupervisor</c> so the ViewModel (and its unit tests) can
/// drive a startup state machine without spawning a real child process.
/// </summary>
/// <remarks>
/// <para>
/// The implementation owned by <c>MainWindow</c> adapts
/// <c>VibeTable.Infrastructure.Backend.PythonBackendSupervisor</c>; tests inject a
/// synchronous fake. The contract is intentionally minimal: the shell only
/// needs to start/stop the supervised process — it does not negotiate the
/// JSON-RPC handshake directly (that lives inside the supervisor).
/// </para>
/// </remarks>
public interface IBackendLifecycle
{
    /// <summary>
    /// Starts the supervised backend. Throws on any failure (spawn error,
    /// handshake timeout, protocol mismatch); the caller surfaces the failure
    /// by moving the startup state machine to <c>Faulted</c>.
    /// </summary>
    Task StartAsync(CancellationToken cancellationToken);

    /// <summary>
    /// Graceful stop. Best-effort — must not throw from the caller's
    /// perspective (Dispose / window-close path). Idempotent.
    /// </summary>
    Task StopAsync(CancellationToken cancellationToken);
}
