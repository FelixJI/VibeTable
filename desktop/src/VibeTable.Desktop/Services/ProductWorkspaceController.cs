using VibeTable.Contracts;
using VibeTable.Infrastructure.Backend;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Owns product workspace projection startup, duplicate-open exclusion, cached
/// bootstrap state, and table notification presentation. Runtime composition
/// only tells it when bindings may have changed.
/// </summary>
internal sealed class ProductWorkspaceController : IDisposable
{
    private readonly IWebReplySink _reply;
    private readonly ProductionWorkspaceRuntimeFactory _runtime;
    private readonly WorkspaceSessionManager _sessions;
    private readonly IDatabasePicker _databasePicker;
    private readonly TableWorkspaceService _workspace;
    private readonly GridStateCoordinator _coordinator;
    private readonly Func<bool> _isRendererReady;
    private readonly Func<bool> _isClosing;
    private readonly Func<bool> _hasProductGateway;
    private readonly Action<string> _trace;
    private readonly DatabaseOpenTerminalPublisher _terminals;
    private readonly string _hostVersion;
    private readonly object _openScheduleGate = new();
    private readonly Func<int, TimeSpan> _retryDelay;
    private readonly Func<TimeSpan, Task> _delay;
    private readonly Func<bool> _guards;
    private readonly Func<PluginProjectContext?> _pluginContext;
    private readonly Func<bool> _workspaceExpected;
    private readonly ProductAuthorityEpoch _authority;
    private readonly bool _ownsAuthority;
    private readonly PluginProjectContextBindingRegistry _databaseOpens;
    private readonly bool _ownsDatabaseOpens;
    private CachedOpen? _snapshot;
    private Task? _openWorker;
    private PluginProjectContextOpenStart? _queuedOpen;
    private PluginProjectContextBinding? _activeOpen;
    private int _disposed;

    private const int OpenRetryLimit = 12;

    private static TimeSpan DefaultOpenRetryDelay(int attempt) => TimeSpan.FromMilliseconds(
        Math.Min(4000, 500 * (1 << Math.Min(Math.Max(attempt - 1, 0), 3))));

    public ProductWorkspaceController(
        IWebReplySink reply,
        ProductionWorkspaceRuntimeFactory runtime,
        WorkspaceSessionManager sessions,
        IDatabasePicker databasePicker,
        TableWorkspaceService workspace,
        GridStateCoordinator coordinator,
        Func<bool> isRendererReady,
        Func<bool> isClosing,
        Func<bool> hasProductGateway,
        Action<string> trace,
        string hostVersion,
        Func<int, TimeSpan>? retryDelay = null,
        Func<TimeSpan, Task>? delay = null,
        Func<bool>? guards = null,
        Func<PluginProjectContext?>? pluginContext = null,
        Func<bool>? workspaceExpected = null,
        ProductAuthorityEpoch? authority = null,
        PluginProjectContextBindingRegistry? databaseOpens = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _runtime = runtime ?? throw new ArgumentNullException(nameof(runtime));
        _sessions = sessions ?? throw new ArgumentNullException(nameof(sessions));
        _databasePicker = databasePicker ?? throw new ArgumentNullException(nameof(databasePicker));
        _workspace = workspace ?? throw new ArgumentNullException(nameof(workspace));
        _coordinator = coordinator ?? throw new ArgumentNullException(nameof(coordinator));
        _isRendererReady = isRendererReady
            ?? throw new ArgumentNullException(nameof(isRendererReady));
        _isClosing = isClosing ?? throw new ArgumentNullException(nameof(isClosing));
        _hasProductGateway = hasProductGateway
            ?? throw new ArgumentNullException(nameof(hasProductGateway));
        _trace = trace ?? throw new ArgumentNullException(nameof(trace));
        _terminals = new DatabaseOpenTerminalPublisher(_reply, _trace);
        _hostVersion = string.IsNullOrWhiteSpace(hostVersion) ? "unknown" : hostVersion;
        _retryDelay = retryDelay ?? DefaultOpenRetryDelay;
        _delay = delay ?? Task.Delay;
        _guards = guards ?? GuardsSatisfied;
        _pluginContext = pluginContext
            ?? (() => PluginProjectContext.FromSession(_sessions.Current));
        _workspaceExpected = workspaceExpected
            ?? (() => _sessions.Current.WorkspaceId is not null
                || _runtime.CurrentWorkspace is not null);
        _authority = authority ?? new ProductAuthorityEpoch();
        _ownsAuthority = authority is null;
        if (_ownsAuthority) _authority.Transition(_pluginContext());
        _databaseOpens = databaseOpens
            ?? new PluginProjectContextBindingRegistry(_authority);
        _ownsDatabaseOpens = databaseOpens is null;
        if (_ownsDatabaseOpens)
            _databaseOpens.SetAfterAuthorityTransition(_pluginContext());
    }

    public void ResetBinding() => _snapshot = null;

    public void OpenWhenReady()
    {
        if (Volatile.Read(ref _disposed) != 0 || _isClosing())
            return;
        if (!WorkspaceExpected())
            return;
        _ = RequestOpenAsync();
    }

    public void OnNotification(TableNotification notification)
        => TableNotificationPresenter.Post(_reply, notification);

    private bool WorkspaceExpected()
        => _workspaceExpected();

    private bool GuardsSatisfied()
    {
        WorkspaceSessionV2 session = _sessions.Current;
        return _isRendererReady()
            && _runtime.CurrentBackend?.State == BackendState.Ready
            && _hasProductGateway()
            && _runtime.CurrentWorkspace?.WorkspaceId == session.WorkspaceId
            && ProductWorkspaceOpenPolicy.CanProject(session);
    }

    internal Task SuperviseOpenAsync() => RequestOpenAsync();

    private Task RequestOpenAsync()
    {
        lock (_openScheduleGate)
        {
            if (_openWorker is not null
                && ((_activeOpen is not null && _databaseOpens.IsCurrent(_activeOpen))
                    || (_queuedOpen?.Binding is { } queuedBinding
                        && _databaseOpens.IsCurrent(queuedBinding))))
            {
                return _openWorker;
            }
            if (_queuedOpen?.Binding is { } retiredQueuedBinding)
                _databaseOpens.Release(retiredQueuedBinding);
            _queuedOpen = _databaseOpens.BeginOrCoalesceHostOpen();
            _openWorker ??= RunOpenQueueAsync();
            return _openWorker;
        }
    }

    private async Task RunOpenQueueAsync()
    {
        await Task.Yield();
        try
        {
            while (true)
            {
                PluginProjectContextOpenStart? request;
                lock (_openScheduleGate)
                {
                    request = _queuedOpen;
                    _queuedOpen = null;
                    _activeOpen = request?.Binding;
                }
                if (request is null) return;
                try
                {
                    await SuperviseOpenRequestAsync(request).ConfigureAwait(false);
                }
                finally
                {
                    if (request.Binding is { } binding)
                        _databaseOpens.Release(binding);
                    lock (_openScheduleGate)
                    {
                        if (ReferenceEquals(_activeOpen, request.Binding))
                            _activeOpen = null;
                    }
                }
            }
        }
        finally
        {
            lock (_openScheduleGate)
            {
                _openWorker = null;
                if (_queuedOpen is not null && Volatile.Read(ref _disposed) == 0)
                    _openWorker = RunOpenQueueAsync();
            }
        }
    }

    private async Task SuperviseOpenRequestAsync(PluginProjectContextOpenStart request)
    {
        // A sidecar crash recycles the session generation: the backend is
        // rebound while the replacement sidecar is still becoming healthy,
        // and any single open attempt that lands inside that window either
        // fails its guards, resolves no source, or throws against the
        // retiring generation. Nothing else re-invokes this projection, so
        // a permanently stranded empty renderer would be the user-visible
        // outcome. Supervise the transient window instead of attempting
        // exactly once.
        PluginProjectContextBinding? binding = request.Binding;
        _terminals.PostRetiredCancellations(request.RetiredOpenIds, "superseded");
        if (binding is null) return;
        bool attempted = false;
        for (int attempt = 1; attempt <= OpenRetryLimit; attempt += 1)
        {
            if (_isClosing() || Volatile.Read(ref _disposed) != 0)
                return;
            if (!_databaseOpens.IsCurrent(binding)) return;
            if (_guards())
            {
                attempted = true;
                if (!_databaseOpens.IsCurrent(binding)) return;
                try
                {
                    string? source = await _databasePicker.PickDatabaseAsync();
                    if (source is not null)
                    {
                        if (!_databaseOpens.IsCurrent(binding)) return;
                        DatabaseOpenResult result = _snapshot is { } cached
                            && cached.Source == source
                            && cached.AuthorityGeneration == binding.ContextGeneration
                                ? cached.Result
                                : await _workspace.PrepareDatabaseOpenAsync(
                                    source,
                                    binding.SessionToken).ConfigureAwait(false);
                        if (_isClosing() || Volatile.Read(ref _disposed) != 0)
                            return;
                        if (!_databaseOpens.IsCurrent(binding)) return;
                        object projection = ProductDatabaseOpenedProjection.Create(
                            result,
                            binding.Context,
                            new
                            {
                                id = "local-user",
                                displayName = "本地用户",
                            },
                            _hostVersion);
                        try
                        {
                            bool completed = _databaseOpens.TryComplete(binding, () =>
                            {
                                using DatabaseOpenCommit commit = DatabaseOpenCommit.Begin(
                                    _workspace,
                                    _coordinator,
                                    source,
                                    result);
                                commit.Enqueue(() =>
                                {
                                    _reply.PostNotification("database.opened", projection);
                                    _snapshot = new CachedOpen(
                                        source,
                                        binding.ContextGeneration,
                                        result);
                                });
                            });
                            if (!completed) return;
                        }
                        catch (Exception exception)
                        {
                            _trace(
                                "Product workspace success terminal failed; " +
                                $"exception={exception.GetType().Name}");
                            _databaseOpens.TryComplete(binding, () =>
                            {
                                _reply.PostOperationFailed(
                                    null,
                                    "本地工作区启动失败，请重试。",
                                    "PRODUCT_WORKSPACE_OPEN_FAILED");
                            });
                        }
                        return;
                    }
                    _trace(
                        $"Product workspace source unresolved yet; " +
                        $"attempt={attempt}");
                }
                catch (OperationCanceledException)
                    when (binding.SessionToken.IsCancellationRequested)
                {
                    return;
                }
                catch (Exception exception)
                {
                    _trace(
                        $"Product workspace open failed transiently; " +
                        $"attempt={attempt}; " +
                        $"exception={exception.GetType().Name}");
                }
            }
            if (!_databaseOpens.IsCurrent(binding)) return;
            await _delay(_retryDelay(attempt)).ConfigureAwait(false);
        }
        if (attempted)
        {
            _trace("Product workspace open failed; budget exhausted");
            if (_databaseOpens.IsCurrent(binding))
            {
                _databaseOpens.TryComplete(binding, () =>
                {
                    _reply.PostOperationFailed(
                        null,
                        "本地工作区启动失败，请重试。",
                        "PRODUCT_WORKSPACE_OPEN_FAILED");
                });
            }
        }
    }

    public void Dispose()
    {
        // A queued open may still be awaiting an RPC while WPF closes. Retire
        // publication immediately; the operation lease keeps its cancellation
        // source alive until that async continuation unwinds.
        Interlocked.Exchange(ref _disposed, 1);
        PluginProjectContextBinding? queuedBinding;
        lock (_openScheduleGate)
        {
            queuedBinding = _queuedOpen?.Binding;
            _queuedOpen = null;
        }
        if (queuedBinding is not null) _databaseOpens.Release(queuedBinding);
        if (_ownsDatabaseOpens) _databaseOpens.Dispose();
        if (_ownsAuthority) _authority.Dispose();
    }

    private sealed record CachedOpen(
        string Source,
        long AuthorityGeneration,
        DatabaseOpenResult Result);

}

internal static class TableNotificationPresenter
{
    public static void Post(IWebReplySink reply, TableNotification notification)
    {
        ArgumentNullException.ThrowIfNull(reply);
        if (notification.MutationResult is not null)
        {
            object? payload = notification.MutationResult.Success
                ? notification.MutationResult.Result
                : ToWebMutationError(
                    notification.MutationResult.Error,
                    notification.MutationResult.Kind);
            if (string.IsNullOrWhiteSpace(notification.RequestId))
                reply.PostNotification(notification.Type, payload);
            else
                reply.PostResponse(notification.Type, notification.RequestId, payload);
            return;
        }
        reply.PostNotification(
            notification.Type,
            new
            {
                table = notification.Page?.Table,
                columns = notification.Page?.Columns,
                rows = notification.Page?.Rows,
                offset = notification.Page?.Offset,
                limit = notification.Page?.Limit,
                totalRows = notification.Page?.TotalRows,
                mode = notification.Page?.Mode,
                filteredRows = notification.Page?.FilteredRows,
                querySnapshot = notification.Page?.QuerySnapshot,
                revision = notification.Page?.Revision,
                groupRows = notification.Page?.GroupRows,
                groupOffset = notification.Page?.GroupOffset,
                groupLimit = notification.Page?.GroupLimit,
                hasMoreGroups = notification.Page?.HasMoreGroups,
                nextCursor = notification.Page?.NextCursor,
                hasMore = notification.Page?.HasMore,
            });
    }

    private static object? ToWebMutationError(MutationError? error, string operation)
    {
        if (error is null)
            return null;
        return new
        {
            kind = MutationErrorMapper.ToWireKind(error.Value.Kind),
            operation,
            code = error.Value.Code,
            message = error.Value.Message,
            currentRow = error.Value.CurrentRow,
            conflictingRowKeys = error.Value.ConflictingRowKeys,
            fieldErrors = error.Value.FieldErrors,
        };
    }
}
