using System;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Infrastructure.PocketBase;

public interface IPocketBaseSupervisor : IAsyncDisposable
{
    event Action<object?, PocketBaseStatus>? StatusChanged;

    Task StartAsync(CancellationToken cancellationToken);
    PocketBaseStatus GetStatus();
    Task StopAsync(CancellationToken cancellationToken);
    Uri? GetAdminUri();
}
