using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Infrastructure.Backend;

/// <summary>
/// Lifecycle surface required by the product runtime. Process and RPC details
/// remain owned by the concrete supervisor.
/// </summary>
public interface IBackendSupervisor : IAsyncDisposable
{
    BackendState State { get; }
    Task StartAsync(CancellationToken cancellationToken);
    Task StopAsync(CancellationToken cancellationToken);
}
