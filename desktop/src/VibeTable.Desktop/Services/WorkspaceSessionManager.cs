using System.Text.Json;
using System.Runtime.ExceptionServices;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

public interface IWorkspaceRuntime : IAsyncDisposable
{
    Guid WorkspaceId { get; }
    ulong SessionEpoch { get; }
    Task StartAsync(WorkspaceOpenMode mode, CancellationToken cancellationToken);
    Task VerifyAsync(CancellationToken cancellationToken);
    Task DrainAsync(CancellationToken cancellationToken);
    Task ResumeAsync(
        WorkspaceOpenMode mode,
        CancellationToken cancellationToken) => Task.CompletedTask;
    Task StopAsync(CancellationToken cancellationToken);
}

public interface IWorkspaceRuntimeFactory
{
    ulong InitialSessionEpoch => 0;

    IWorkspaceRuntime Create(
        WorkspaceRegistryEntryV2 workspace,
        ulong sessionEpoch);
}

public interface IWorkspaceProtectionHook
{
    Task ProtectAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        string reason,
        CancellationToken cancellationToken);

    async Task<ulong> ProtectAndSynchronizeAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        string reason,
        CancellationToken cancellationToken)
    {
        await ProtectAsync(
            workspaceId,
            sessionEpoch,
            reason,
            cancellationToken);
        return 0;
    }
}

public interface IWorkspaceProtectionReceiptHook : IWorkspaceProtectionHook
{
    Task<ProtectionSnapshotReceipt> CaptureAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        string reason,
        CancellationToken cancellationToken);
}

public interface IWorkspaceLeaseHook
{
    Task<WorkspaceOpenMode> AcquireAsync(
        WorkspaceRegistryEntryV2 workspace,
        WorkspaceOpenMode requestedMode,
        CancellationToken cancellationToken);
    Task ReleaseAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken);
}

public interface IWorkspacePreOpenHook
{
    Task PrepareAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken);
}

public sealed class WorkspaceSessionManager : IAsyncDisposable
{
    private readonly SemaphoreSlim _gate = new(1, 1);
    private readonly WorkspaceRegistry _registry;
    private readonly IWorkspaceRuntimeFactory _runtimeFactory;
    private readonly IWorkspaceProtectionHook _protection;
    private readonly IWorkspaceLeaseHook _lease;
    private readonly IWorkspacePreOpenHook _preOpen;
    private IWorkspaceRequestDrainHook _requestDrain =
        NoopWorkspaceRequestDrainHook.Instance;
    private IWorkspaceRuntime? _runtime;
    private WorkspaceRegistryEntryV2? _currentEntry;
    private WorkspaceSessionV2 _current = ClosedSession(0);
    private ulong _nextEpoch;
    private ulong _eventSequence;
    private bool _disposed;

    public WorkspaceSessionManager(
        WorkspaceRegistry registry,
        IWorkspaceRuntimeFactory runtimeFactory,
        IWorkspaceProtectionHook? protection = null,
        IWorkspaceLeaseHook? lease = null,
        IWorkspacePreOpenHook? preOpen = null)
    {
        _registry = registry ?? throw new ArgumentNullException(nameof(registry));
        _runtimeFactory = runtimeFactory ?? throw new ArgumentNullException(nameof(runtimeFactory));
        _nextEpoch = runtimeFactory.InitialSessionEpoch;
        _protection = protection ?? NoopWorkspaceProtectionHook.Instance;
        _lease = lease ?? NoopWorkspaceLeaseHook.Instance;
        _preOpen = preOpen ?? NoopWorkspacePreOpenHook.Instance;
    }

    public WorkspaceSessionV2 Current => _current;
    public ulong? LastProtectionMutationRevision { get; private set; }

    public event EventHandler<WorkspaceSessionChangedEventArgs>? Changed;

    public void SetRequestDrainHook(IWorkspaceRequestDrainHook requestDrain)
    {
        ArgumentNullException.ThrowIfNull(requestDrain);
        _requestDrain = requestDrain;
    }

    public async Task<WorkspaceSessionV2> OpenAsync(
        Guid workspaceId,
        WorkspaceOpenMode requestedMode,
        CancellationToken cancellationToken = default)
    {
        await _gate.WaitAsync(cancellationToken);
        try
        {
            ThrowIfDisposed();
            if (_runtime is not null)
                throw new InvalidOperationException("A workspace session is already open.");
            var entry = FindEntry(workspaceId);
            return await OpenCoreAsync(
                entry,
                requestedMode,
                WorkspaceSessionState.Opening,
                cancellationToken);
        }
        finally
        {
            _gate.Release();
        }
    }

    public async Task<WorkspaceSessionV2> SwitchAsync(
        Guid targetWorkspaceId,
        WorkspaceOpenMode requestedMode,
        CancellationToken cancellationToken = default)
    {
        await _gate.WaitAsync(cancellationToken);
        try
        {
            ThrowIfDisposed();
            if (_runtime is null || _currentEntry is null || _current.WorkspaceId is null)
                return await OpenCoreAsync(
                    FindEntry(targetWorkspaceId),
                    requestedMode,
                    WorkspaceSessionState.Opening,
                    cancellationToken);
            if (_current.WorkspaceId == targetWorkspaceId)
                return _current;

            var previousEntry = _currentEntry;
            var previousMode = _current.OpenMode;
            var previousEpoch = _current.SessionEpoch;
            try
            {
                Publish(_current with
                {
                    State = WorkspaceSessionState.Switching,
                    Phase = WorkspaceSessionPhase.Draining,
                    Writable = false,
                });
                await _requestDrain.DrainAsync(
                    previousEntry.WorkspaceId,
                    previousEpoch,
                    cancellationToken);
                if (previousMode != WorkspaceOpenMode.ReadOnly)
                {
                    Publish(_current with
                    {
                        Phase = WorkspaceSessionPhase.Protecting,
                    });
                    await _protection.ProtectAsync(
                        previousEntry.WorkspaceId,
                        previousEpoch,
                        "workspace-switch",
                        cancellationToken);
                }
                Publish(_current with
                {
                    Phase = WorkspaceSessionPhase.Draining,
                });
                await _runtime.DrainAsync(cancellationToken);
                Publish(_current with { Phase = WorkspaceSessionPhase.Stopping });
                await StopDisposeAndReleaseCurrentAsync(
                    previousEntry.WorkspaceId,
                    previousEpoch,
                    cancellationToken);
                return await OpenCoreAsync(
                    FindEntry(targetWorkspaceId),
                    requestedMode,
                    WorkspaceSessionState.Switching,
                    cancellationToken);
            }
            catch (Exception switchError)
            {
                try
                {
                    // Rollback is a safety action and must not inherit the
                    // caller's already-cancelled switch token.
                    if (_runtime is not null
                        && _currentEntry?.WorkspaceId == previousEntry.WorkspaceId
                        && _runtime.SessionEpoch == previousEpoch)
                    {
                        Publish(new WorkspaceSessionV2
                        {
                            ContractVersion = WorkspaceV2Json.ContractVersion,
                            WorkspaceId = previousEntry.WorkspaceId,
                            SessionEpoch = previousEpoch,
                            State = WorkspaceSessionState.Switching,
                            OpenMode = previousMode,
                            Writable = false,
                            Provisional =
                                previousMode == WorkspaceOpenMode.Provisional,
                            Phase = WorkspaceSessionPhase.RollingBack,
                            ErrorCode = "workspace.switch_failed",
                        });
                        await _runtime.ResumeAsync(
                            previousMode,
                            CancellationToken.None);
                        _requestDrain.Resume(
                            previousEntry.WorkspaceId,
                            previousEpoch);
                        var restored = _current with
                        {
                            State = previousMode switch
                            {
                                WorkspaceOpenMode.Writable => WorkspaceSessionState.OpenedWritable,
                                WorkspaceOpenMode.ReadOnly => WorkspaceSessionState.OpenedReadOnly,
                                _ => WorkspaceSessionState.OpenedProvisional,
                            },
                            Phase = WorkspaceSessionPhase.Idle,
                            Writable = previousMode == WorkspaceOpenMode.Writable,
                            Provisional = previousMode == WorkspaceOpenMode.Provisional,
                            ErrorCode = null,
                        };
                        Publish(restored);
                        throw new WorkspaceSwitchException(
                            "Workspace switch failed before the previous runtime stopped.",
                            switchError,
                            restored);
                    }
                    var rollback = await OpenCoreAsync(
                        previousEntry,
                        previousMode,
                        WorkspaceSessionState.Switching,
                        CancellationToken.None);
                    throw new WorkspaceSwitchException(
                        "Target workspace could not be opened; the previous workspace was restored.",
                        switchError,
                        rollback);
                }
                catch (WorkspaceSwitchException)
                {
                    throw;
                }
                catch (Exception rollbackError)
                {
                    Publish(new WorkspaceSessionV2
                    {
                        ContractVersion = WorkspaceV2Json.ContractVersion,
                        WorkspaceId = previousEntry.WorkspaceId,
                        SessionEpoch = _current.SessionEpoch,
                        State = WorkspaceSessionState.Failed,
                        OpenMode = previousMode,
                        Writable = false,
                        Provisional = false,
                        Phase = WorkspaceSessionPhase.RollingBack,
                        ErrorCode = "workspace.rollback_failed",
                    });
                    throw new AggregateException(switchError, rollbackError);
                }
            }
        }
        finally
        {
            _gate.Release();
        }
    }

    public async Task<WorkspaceSessionV2> CloseAsync(
        string reason,
        CancellationToken cancellationToken = default,
        bool synchronizeReplica = false)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(reason);
        await _gate.WaitAsync(cancellationToken);
        try
        {
            ThrowIfDisposed();
            if (_runtime is null || _current.WorkspaceId is null)
                return _current;
            var workspaceId = _current.WorkspaceId.Value;
            var epoch = _current.SessionEpoch;
            WorkspaceOpenMode previousMode = _current.OpenMode;
            try
            {
                Publish(_current with
                {
                    State = WorkspaceSessionState.Switching,
                    Phase = WorkspaceSessionPhase.Draining,
                    Writable = false,
                });
                await _requestDrain.DrainAsync(
                    workspaceId,
                    epoch,
                    cancellationToken);
                if (previousMode != WorkspaceOpenMode.ReadOnly ||
                    synchronizeReplica)
                {
                    Publish(_current with
                    {
                        Phase = WorkspaceSessionPhase.Protecting,
                    });
                    if (synchronizeReplica)
                    {
                        ulong revision =
                            await _protection.ProtectAndSynchronizeAsync(
                                workspaceId,
                                epoch,
                                reason,
                                cancellationToken);
                        LastProtectionMutationRevision = revision;
                    }
                    else
                    {
                        await _protection.ProtectAsync(
                            workspaceId,
                            epoch,
                            reason,
                            cancellationToken);
                    }
                    Publish(_current with
                    {
                        Phase = WorkspaceSessionPhase.Draining,
                    });
                }
                await _runtime.DrainAsync(cancellationToken);
                Publish(_current with
                {
                    Phase = WorkspaceSessionPhase.Stopping,
                });
                await StopDisposeAndReleaseCurrentAsync(
                    workspaceId,
                    epoch,
                    cancellationToken);
                Publish(ClosedSession(epoch));
                return _current;
            }
            catch
            {
                if (_runtime is not null
                    && _currentEntry?.WorkspaceId == workspaceId
                    && _runtime.SessionEpoch == epoch)
                {
                    Publish(_current with
                    {
                        State = WorkspaceSessionState.Switching,
                        Phase = WorkspaceSessionPhase.RollingBack,
                        Writable = false,
                        ErrorCode = "workspace.close_failed",
                    });
                    await _runtime.ResumeAsync(
                        previousMode,
                        CancellationToken.None);
                    _requestDrain.Resume(workspaceId, epoch);
                    Publish(_current with
                    {
                        State = previousMode switch
                        {
                            WorkspaceOpenMode.Writable =>
                                WorkspaceSessionState.OpenedWritable,
                            WorkspaceOpenMode.ReadOnly =>
                                WorkspaceSessionState.OpenedReadOnly,
                            _ => WorkspaceSessionState.OpenedProvisional,
                        },
                        Phase = WorkspaceSessionPhase.Idle,
                        Writable =
                            previousMode == WorkspaceOpenMode.Writable,
                        Provisional =
                            previousMode == WorkspaceOpenMode.Provisional,
                        ErrorCode = null,
                    });
                }
                else
                {
                    try
                    {
                        await _lease.ReleaseAsync(
                            workspaceId,
                            epoch,
                            CancellationToken.None);
                    }
                    finally
                    {
                        Publish(ClosedSession(epoch));
                    }
                }
                throw;
            }
        }
        finally
        {
            _gate.Release();
        }
    }

    public async Task<WorkspaceSessionV2> RestartAfterRestoreAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken = default)
    {
        await _gate.WaitAsync(cancellationToken);
        try
        {
            ThrowIfDisposed();
            if (_runtime is null ||
                _currentEntry?.WorkspaceId != workspaceId ||
                _runtime.SessionEpoch != sessionEpoch)
                throw new WorkspaceRegistryException(
                    "workspace.session_stale",
                    "The restored workspace session is no longer active.");
            WorkspaceRegistryEntryV2 entry = _currentEntry;
            WorkspaceOpenMode mode = _current.OpenMode;
            Publish(_current with
            {
                State = WorkspaceSessionState.Switching,
                Phase = WorkspaceSessionPhase.Stopping,
                Writable = false,
            });
            await _requestDrain.DrainAsync(
                workspaceId,
                sessionEpoch,
                cancellationToken);
            await StopDisposeAndReleaseCurrentAsync(
                workspaceId,
                sessionEpoch,
                CancellationToken.None);
            return await OpenCoreAsync(
                entry,
                mode,
                WorkspaceSessionState.Switching,
                cancellationToken);
        }
        finally
        {
            _gate.Release();
        }
    }

    public Task<WorkspaceSessionV2> RestartAfterHostMaintenanceAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken = default)
        => RestartAfterRestoreAsync(
            workspaceId,
            sessionEpoch,
            cancellationToken);

    public bool Accept(WorkspaceWireScope scope, ulong minimumSequence = 0)
    {
        if (_current.WorkspaceId is null)
            return false;
        try
        {
            scope.Validate();
            scope.EnsureCurrent(
                _current.WorkspaceId.Value,
                _current.SessionEpoch,
                minimumSequence);
            return true;
        }
        catch (Exception exception) when (
            exception is JsonException or InvalidOperationException)
        {
            return false;
        }
    }

    public async ValueTask DisposeAsync()
    {
        if (_disposed)
            return;
        await _gate.WaitAsync();
        try
        {
            if (_disposed)
                return;
            _disposed = true;
            if (_runtime is not null)
            {
                Guid workspaceId = _runtime.WorkspaceId;
                ulong sessionEpoch = _runtime.SessionEpoch;
                await StopDisposeAndReleaseCurrentAsync(
                    workspaceId,
                    sessionEpoch,
                    CancellationToken.None);
            }
            Publish(ClosedSession(_current.SessionEpoch));
        }
        finally
        {
            _gate.Release();
            _gate.Dispose();
        }
    }

    private async Task<WorkspaceSessionV2> OpenCoreAsync(
        WorkspaceRegistryEntryV2 entry,
        WorkspaceOpenMode requestedMode,
        WorkspaceSessionState transitionState,
        CancellationToken cancellationToken)
    {
        var epoch = NextEpoch();
        LastProtectionMutationRevision = null;
        await _preOpen.PrepareAsync(entry, cancellationToken);
        var grantedMode = await _lease.AcquireAsync(
            entry,
            requestedMode,
            cancellationToken);
        IWorkspaceRuntime runtime;
        try
        {
            runtime = _runtimeFactory.Create(entry, epoch);
        }
        catch
        {
            await _lease.ReleaseAsync(
                entry.WorkspaceId,
                epoch,
                CancellationToken.None);
            throw;
        }
        if (runtime.WorkspaceId != entry.WorkspaceId || runtime.SessionEpoch != epoch)
        {
            await runtime.DisposeAsync();
            await _lease.ReleaseAsync(
                entry.WorkspaceId,
                epoch,
                CancellationToken.None);
            throw new InvalidOperationException("Runtime identity does not match session.");
        }
        _runtime = runtime;
        _currentEntry = entry;
        Publish(new WorkspaceSessionV2
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = entry.WorkspaceId,
            SessionEpoch = epoch,
            State = transitionState,
            OpenMode = grantedMode,
            Writable = false,
            Provisional = grantedMode == WorkspaceOpenMode.Provisional,
            Phase = WorkspaceSessionPhase.Starting,
            ErrorCode = null,
        });
        try
        {
            await runtime.StartAsync(grantedMode, cancellationToken);
            Publish(_current with { Phase = WorkspaceSessionPhase.Binding });
            Publish(_current with { Phase = WorkspaceSessionPhase.Verifying });
            await runtime.VerifyAsync(cancellationToken);
            var openedState = grantedMode switch
            {
                WorkspaceOpenMode.ReadOnly => WorkspaceSessionState.OpenedReadOnly,
                WorkspaceOpenMode.Writable => WorkspaceSessionState.OpenedWritable,
                WorkspaceOpenMode.Provisional => WorkspaceSessionState.OpenedProvisional,
                _ => throw new ArgumentOutOfRangeException(nameof(grantedMode)),
            };
            Publish(_current with
            {
                State = openedState,
                Phase = WorkspaceSessionPhase.Idle,
                Writable = grantedMode == WorkspaceOpenMode.Writable,
                Provisional = grantedMode == WorkspaceOpenMode.Provisional,
            });
            _registry.Register(entry with
            {
                LastOpenedAt = DateTimeOffset.UtcNow,
                LastKnownHealth = WorkspaceHealth.Healthy,
            });
            return _current;
        }
        catch (Exception openError)
        {
            try
            {
                await StopDisposeAndReleaseCurrentAsync(
                    entry.WorkspaceId,
                    epoch,
                    CancellationToken.None);
                Publish(ClosedSession(epoch));
            }
            catch (Exception cleanupError)
            {
                throw new AggregateException(openError, cleanupError);
            }
            ExceptionDispatchInfo.Capture(openError).Throw();
            throw;
        }
    }

    public async Task ProtectCurrentAsync(
        string reason,
        CancellationToken cancellationToken = default)
        => _ = await ProtectCurrentWithReceiptAsync(reason, cancellationToken);

    public async Task<ProtectionSnapshotReceipt?> ProtectCurrentWithReceiptAsync(
        string reason,
        CancellationToken cancellationToken = default)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(reason);
        await _gate.WaitAsync(cancellationToken);
        try
        {
            ThrowIfDisposed();
            if (_runtime is null ||
                _current.WorkspaceId is not Guid workspaceId ||
                _current.OpenMode == WorkspaceOpenMode.ReadOnly ||
                _current.State is not (
                    WorkspaceSessionState.OpenedWritable or
                    WorkspaceSessionState.OpenedProvisional) ||
                _current.Phase != WorkspaceSessionPhase.Idle)
                throw new WorkspaceRegistryException(
                    "workspace.protection_not_writable",
                    "The current workspace session cannot run a protection snapshot.");
            if (_protection is IWorkspaceProtectionReceiptHook receiptHook)
                return await receiptHook.CaptureAsync(
                    workspaceId,
                    _current.SessionEpoch,
                    reason,
                    cancellationToken);
            await _protection.ProtectAsync(
                workspaceId,
                _current.SessionEpoch,
                reason,
                cancellationToken);
            return null;
        }
        finally
        {
            _gate.Release();
        }
    }

    private async Task StopDisposeAndReleaseCurrentAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken)
    {
        var runtime = _runtime;
        _runtime = null;
        _currentEntry = null;
        List<Exception> failures = [];
        if (runtime is not null)
        {
            try
            {
                await runtime.StopAsync(cancellationToken);
            }
            catch (Exception exception)
            {
                failures.Add(exception);
            }
            try
            {
                await runtime.DisposeAsync();
            }
            catch (Exception exception)
            {
                failures.Add(exception);
            }
        }
        try
        {
            await _lease.ReleaseAsync(
                workspaceId,
                sessionEpoch,
                CancellationToken.None);
        }
        catch (Exception exception)
        {
            failures.Add(exception);
        }
        if (failures.Count == 1)
            ExceptionDispatchInfo.Capture(failures[0]).Throw();
        if (failures.Count > 1)
            throw new AggregateException(failures);
    }

    private WorkspaceRegistryEntryV2 FindEntry(Guid workspaceId) =>
        _registry.List().SingleOrDefault(entry => entry.WorkspaceId == workspaceId)
        ?? throw new WorkspaceRegistryException(
            "workspace.not_registered",
            "Workspace is not registered on this device.");

    private ulong NextEpoch() => ++_nextEpoch;

    private void Publish(WorkspaceSessionV2 session)
    {
        session.Validate();
        _current = session;
        Changed?.Invoke(
            this,
            new WorkspaceSessionChangedEventArgs(session, ++_eventSequence));
    }

    private void ThrowIfDisposed() =>
        ObjectDisposedException.ThrowIf(_disposed, this);

    private static WorkspaceSessionV2 ClosedSession(ulong epoch) => new()
    {
        ContractVersion = WorkspaceV2Json.ContractVersion,
        WorkspaceId = null,
        SessionEpoch = epoch,
        State = WorkspaceSessionState.Closed,
        OpenMode = WorkspaceOpenMode.ReadOnly,
        Writable = false,
        Provisional = false,
        Phase = WorkspaceSessionPhase.Idle,
        ErrorCode = null,
    };
}

public sealed class NoopWorkspacePreOpenHook : IWorkspacePreOpenHook
{
    public static NoopWorkspacePreOpenHook Instance { get; } = new();
    private NoopWorkspacePreOpenHook() { }

    public Task PrepareAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken)
        => Task.CompletedTask;
}

public sealed record WorkspaceSessionChangedEventArgs(
    WorkspaceSessionV2 Session,
    ulong Sequence);

public sealed class WorkspaceSwitchException(
    string message,
    Exception innerException,
    WorkspaceSessionV2 rolledBackSession) : Exception(message, innerException)
{
    public WorkspaceSessionV2 RolledBackSession { get; } = rolledBackSession;
}

public sealed class NoopWorkspaceProtectionHook : IWorkspaceProtectionHook
{
    public static NoopWorkspaceProtectionHook Instance { get; } = new();
    private NoopWorkspaceProtectionHook() { }
    public Task ProtectAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        string reason,
        CancellationToken cancellationToken) => Task.CompletedTask;
}

public sealed class NoopWorkspaceLeaseHook : IWorkspaceLeaseHook
{
    public static NoopWorkspaceLeaseHook Instance { get; } = new();
    private NoopWorkspaceLeaseHook() { }

    public Task<WorkspaceOpenMode> AcquireAsync(
        WorkspaceRegistryEntryV2 workspace,
        WorkspaceOpenMode requestedMode,
        CancellationToken cancellationToken) => Task.FromResult(requestedMode);

    public Task ReleaseAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken) => Task.CompletedTask;
}

internal sealed class NoopWorkspaceRequestDrainHook :
    IWorkspaceRequestDrainHook
{
    public static NoopWorkspaceRequestDrainHook Instance { get; } = new();
    private NoopWorkspaceRequestDrainHook() { }

    public Task DrainAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken)
        => Task.CompletedTask;

    public void Resume(Guid workspaceId, ulong sessionEpoch)
    {
    }
}
