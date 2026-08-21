using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Infrastructure.PocketBase;

public interface IPocketBaseSupervisor : IAsyncDisposable
{
    event Action<object?, PocketBaseStatus>? StatusChanged;

    PocketBaseStartupTimings? LastStartupTimings { get; }
    Task StartAsync(CancellationToken cancellationToken);
    PocketBaseStatus GetStatus();
    Task StopAsync(CancellationToken cancellationToken);
    Uri? GetAdminUri();
    PocketBaseAdminContext? GetAdminContext();
    void ConfigureBackendEnvironment(IDictionary<string, string> environment);
}

public sealed record PocketBaseAdminContext(
    Uri BootstrapUri,
    Uri Origin,
    string SessionHeaderName,
    string SessionSecret);
