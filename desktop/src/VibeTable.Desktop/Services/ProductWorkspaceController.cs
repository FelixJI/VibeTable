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
    private readonly string _hostVersion;
    private readonly SemaphoreSlim _openGate = new(1, 1);
    private readonly Func<int, TimeSpan> _retryDelay;
    private DatabaseOpenResult? _snapshot;
    private int _disposed;

    private const int OpenRetryLimit = 8;

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
        Func<int, TimeSpan>? retryDelay = null)
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
        _hostVersion = string.IsNullOrWhiteSpace(hostVersion) ? "unknown" : hostVersion;
        _retryDelay = retryDelay ?? DefaultOpenRetryDelay;
    }

    public void ResetBinding() => _snapshot = null;

    public void OpenWhenReady()
    {
        WorkspaceSessionV2 session = _sessions.Current;
        if (Volatile.Read(ref _disposed) != 0
            || _isClosing()
            || !_isRendererReady()
            || _runtime.CurrentBackend?.State != BackendState.Ready
            || !_hasProductGateway()
            || _runtime.CurrentWorkspace?.WorkspaceId != session.WorkspaceId
            || !ProductWorkspaceOpenPolicy.CanProject(session))
        {
            return;
        }
        _ = OpenAsync();
    }

    public void OnNotification(TableNotification notification)
        => TableNotificationPresenter.Post(_reply, notification);

    internal async Task OpenAsync()
    {
        if (!await _openGate.WaitAsync(0))
            return;
        try
        {
            // A sidecar crash recycles the session generation: the backend is
            // rebound while the replacement sidecar is still becoming healthy,
            // and the single open attempt that lands inside that window fails
            // against the retiring generation. Nothing else re-invokes this
            // projection, so a permanently stranded empty renderer would be
            // the user-visible outcome. Retry the transient window instead.
            for (int attempt = 1; ; attempt += 1)
            {
                if (_isClosing() || Volatile.Read(ref _disposed) != 0)
                    return;
                try
                {
                    await OpenOnceAsync();
                    return;
                }
                catch (Exception exception) when (
                    attempt < OpenRetryLimit
                    && !_isClosing()
                    && Volatile.Read(ref _disposed) == 0)
                {
                    _trace(
                        $"Product workspace open failed transiently; " +
                        $"attempt={attempt}; " +
                        $"exception={exception.GetType().Name}");
                    await Task.Delay(_retryDelay(attempt));
                }
            }
        }
        catch (Exception exception)
        {
            _trace(
                $"Product workspace open failed; " +
                $"exception={exception.GetType().Name}");
            _reply.PostOperationFailed(
                null,
                "本地工作区启动失败，请重试。",
                "PRODUCT_WORKSPACE_OPEN_FAILED");
        }
        finally
        {
            _openGate.Release();
        }
    }

    private async Task OpenOnceAsync()
    {
        string? source = await _databasePicker.PickDatabaseAsync();
        if (source is null)
            return;
        DatabaseOpenResult result = _snapshot
            ?? await _workspace.OpenDatabaseAsync(source);
        _snapshot = result;
        if (_isClosing() || Volatile.Read(ref _disposed) != 0)
            return;
        _coordinator.SetDatabase("local");
        _reply.PostNotification(
            "database.opened",
            new
            {
                tables = result.Tables,
                views = result.Views,
                displayNames = result.DisplayNames,
                projectKey = "local:default",
                projectRevision = "1",
                currentUser = new
                {
                    id = "local-user",
                    displayName = "本地用户",
                },
                hostVersion = _hostVersion,
            });
    }

    public void Dispose()
    {
        // An OpenAsync operation may still own the semaphore while WPF closes.
        // Mark the controller retired so it cannot publish, and let that task
        // release the process-scoped gate instead of racing Dispose/Release.
        Interlocked.Exchange(ref _disposed, 1);
    }
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
