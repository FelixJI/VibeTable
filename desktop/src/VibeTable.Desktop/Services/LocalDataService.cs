using System;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Product-facing lifecycle boundary for the private local data process.
/// PocketBase launch details and the session credential never cross it.
/// </summary>
public interface ILocalDataService : IAsyncDisposable
{
    Task StartAsync(CancellationToken cancellationToken);
    LocalDataStatus GetStatus();
    Task StopAsync(CancellationToken cancellationToken);
    bool OpenAdmin();
}

public enum LocalDataState
{
    Stopped = 0,
    Starting = 1,
    Ready = 2,
    Stopping = 3,
    Faulted = 4,
}

/// <summary>
/// Secret-free status intended for shell presentation.
/// </summary>
public sealed record LocalDataStatus(
    LocalDataState State,
    bool IsReady,
    bool CanOpenAdmin,
    int? ExitCode,
    string? Error);

public interface ILocalDataAdminLauncher
{
    void Open(Uri uri);
}

public sealed class LocalDataService : ILocalDataService
{
    private readonly IPocketBaseSupervisor _supervisor;
    private readonly ILocalDataAdminLauncher? _adminLauncher;

    public LocalDataService(
        IPocketBaseSupervisor supervisor,
        ILocalDataAdminLauncher? adminLauncher = null)
    {
        _supervisor = supervisor
            ?? throw new ArgumentNullException(nameof(supervisor));
        _adminLauncher = adminLauncher;
    }

    public Task StartAsync(CancellationToken cancellationToken)
        => _supervisor.StartAsync(cancellationToken);

    public LocalDataStatus GetStatus()
    {
        PocketBaseStatus status = _supervisor.GetStatus();
        LocalDataState state = status.State switch
        {
            PocketBaseState.Stopped => LocalDataState.Stopped,
            PocketBaseState.Starting => LocalDataState.Starting,
            PocketBaseState.Ready => LocalDataState.Ready,
            PocketBaseState.Stopping => LocalDataState.Stopping,
            PocketBaseState.Faulted => LocalDataState.Faulted,
            _ => throw new ArgumentOutOfRangeException(
                nameof(status), status.State, "Unknown local data state."),
        };
        return new LocalDataStatus(
            state,
            state == LocalDataState.Ready,
            state == LocalDataState.Ready
                && status.AdminAvailable
                && _supervisor.GetAdminContext() is not null,
            status.ExitCode,
            status.Error);
    }

    public Task StopAsync(CancellationToken cancellationToken)
        => _supervisor.StopAsync(cancellationToken);

    public bool OpenAdmin() => false;

    public ValueTask DisposeAsync() => _supervisor.DisposeAsync();
}
