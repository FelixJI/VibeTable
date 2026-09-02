using System.Runtime.ExceptionServices;
using System.Net.Http;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

internal enum UpdateWorkspaceHealthProbeStatus
{
    Healthy,
    SkippedNoRegisteredWorkspace,
}

internal sealed record UpdateWorkspaceHealthProbeReceipt(
    UpdateWorkspaceHealthProbeStatus Status,
    Guid? WorkspaceId,
    ulong? SessionEpoch,
    int? TableCount);

internal sealed class UpdateWorkspaceHealthException(
    string code,
    string message,
    Exception? innerException = null) : InvalidOperationException(message, innerException)
{
    public string Code { get; } = code;
}

internal interface IUpdateWorkspaceSchemaReader
{
    Task<int> ReadTableCountAsync(
        WorkspaceSessionV2 expectedSession,
        CancellationToken cancellationToken);
}

/// <summary>
/// Owns the complete post-update health transaction: select the most recent
/// workspace, open an exclusive read-only session, read schema metadata,
/// close the temporary session, publish readiness, and only then confirm the
/// pending update.
/// </summary>
internal sealed class UpdateActivationWorkspaceHealthGate
{
    private static readonly TimeSpan DefaultSchemaTimeout = TimeSpan.FromSeconds(10);

    private readonly WorkspaceRegistry _registry;
    private readonly IWorkspaceProductSessionPort _session;
    private readonly IUpdateWorkspaceSchemaReader _schema;
    private readonly TimeSpan _schemaTimeout;
    private readonly Action<UpdateWorkspaceHealthProbeReceipt>? _reportReady;
    private readonly Action<Exception>? _reportFailure;
    private readonly HostStartupOptions _startupOptions;
    private readonly Action? _crashAction;

    public UpdateActivationWorkspaceHealthGate(
        WorkspaceRegistry registry,
        IWorkspaceProductSessionPort session,
        IUpdateWorkspaceSchemaReader schema,
        TimeSpan? schemaTimeout = null,
        Action<UpdateWorkspaceHealthProbeReceipt>? reportReady = null,
        Action<Exception>? reportFailure = null,
        HostStartupOptions? startupOptions = null,
        Action? crashAction = null)
    {
        _registry = registry ?? throw new ArgumentNullException(nameof(registry));
        _session = session ?? throw new ArgumentNullException(nameof(session));
        _schema = schema ?? throw new ArgumentNullException(nameof(schema));
        _schemaTimeout = schemaTimeout ?? DefaultSchemaTimeout;
        if (_schemaTimeout <= TimeSpan.Zero)
        {
            throw new ArgumentOutOfRangeException(nameof(schemaTimeout));
        }
        _reportReady = reportReady;
        _reportFailure = reportFailure;
        _startupOptions = startupOptions ?? HostStartupOptions.Current();
        _crashAction = crashAction;
    }

    public async Task<UpdateWorkspaceHealthProbeReceipt> ConfirmAsync(
        IUpdateActivationSettlement settlement,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(settlement);
        _startupOptions.CrashSelfUpdateIfRequested(_crashAction);
        if (_startupOptions.TryConsumeSelfUpdateHealthTimeoutHold())
        {
            await HoldHealthSettlementUntilHostTerminationAsync(
                cancellationToken).ConfigureAwait(false);
        }
        bool opened = false;
        Exception? failure = null;
        UpdateWorkspaceHealthProbeReceipt? receipt = null;
        try
        {
            EnsureSessionIsClosed();
            WorkspaceRegistryEntryV2? workspace = _registry.List().FirstOrDefault();
            if (workspace is null)
            {
                receipt = new UpdateWorkspaceHealthProbeReceipt(
                    UpdateWorkspaceHealthProbeStatus.SkippedNoRegisteredWorkspace,
                    null,
                    null,
                    null);
            }
            else
            {
                WorkspaceSessionV2 openedSession = await _session.OpenAsync(
                    workspace.WorkspaceId,
                    WorkspaceOpenMode.ReadOnly,
                    switching: false,
                    cancellationToken).ConfigureAwait(false);
                opened = true;
                ValidateOpenedSession(openedSession, workspace.WorkspaceId);
                int tableCount = await ReadTableCountAsync(
                    openedSession,
                    cancellationToken).ConfigureAwait(false);
                receipt = new UpdateWorkspaceHealthProbeReceipt(
                    UpdateWorkspaceHealthProbeStatus.Healthy,
                    workspace.WorkspaceId,
                    openedSession.SessionEpoch,
                    tableCount);
            }
        }
        catch (Exception exception)
        {
            failure = exception;
        }

        if (opened)
        {
            try
            {
                await _session.CloseAsync(
                    "update-workspace-health-probe",
                    CancellationToken.None).ConfigureAwait(false);
            }
            catch (Exception closeException) when (failure is not null)
            {
                failure = new AggregateException(failure, closeException);
            }
            catch (Exception closeException)
            {
                failure = closeException;
            }
        }

        if (failure is not null)
        {
            await ReportFailureAndSettleAsync(settlement, failure).ConfigureAwait(false);
            ExceptionDispatchInfo.Capture(failure).Throw();
        }

        try
        {
            _reportReady?.Invoke(receipt!);
        }
        catch (Exception exception)
        {
            await ReportFailureAndSettleAsync(settlement, exception).ConfigureAwait(false);
            throw;
        }
        await settlement.CompleteHealthCheckAsync(
            new UpdateActivationHealth.Healthy(receipt!),
            cancellationToken).ConfigureAwait(false);
        return receipt!;
    }

    private async Task HoldHealthSettlementUntilHostTerminationAsync(
        CancellationToken cancellationToken)
    {
        while (true)
        {
            // The crash observer opens its exact process handle after the hold
            // is consumed, then supplies the same protected one-shot request.
            _startupOptions.CrashSelfUpdateIfRequested(_crashAction);
            await Task.Delay(TimeSpan.FromMilliseconds(50), cancellationToken).ConfigureAwait(false);
        }
    }

    private async Task ReportFailureAndSettleAsync(
        IUpdateActivationSettlement settlement,
        Exception failure)
    {
        try
        {
            _reportFailure?.Invoke(failure);
        }
        catch
        {
            // Diagnostics cannot replace the probe result or leave the
            // smoke gate waiting indefinitely.
        }
        await settlement.CompleteHealthCheckAsync(
            new UpdateActivationHealth.Failed(
                UpdateActivationFailureCode.WorkspaceHealthProbeFailed),
            CancellationToken.None).ConfigureAwait(false);
    }

    private async Task<int> ReadTableCountAsync(
        WorkspaceSessionV2 openedSession,
        CancellationToken cancellationToken)
    {
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(
            cancellationToken);
        timeout.CancelAfter(_schemaTimeout);
        try
        {
            return await _schema.ReadTableCountAsync(
                openedSession,
                timeout.Token).ConfigureAwait(false);
        }
        catch (OperationCanceledException exception)
            when (!cancellationToken.IsCancellationRequested
                  && timeout.IsCancellationRequested)
        {
            throw new UpdateWorkspaceHealthException(
                "update.workspace_probe_timeout",
                "更新后的工作区健康探测超时。",
                exception);
        }
    }

    private void EnsureSessionIsClosed()
    {
        if (_session.CurrentSession.State != WorkspaceSessionState.Closed)
        {
            throw new UpdateWorkspaceHealthException(
                "update.workspace_probe_busy",
                "更新后的工作区健康探测无法取得独占会话。");
        }
    }

    private static void ValidateOpenedSession(
        WorkspaceSessionV2 session,
        Guid expectedWorkspaceId)
    {
        if (session.WorkspaceId != expectedWorkspaceId
            || session.SessionEpoch == 0
            || session.State != WorkspaceSessionState.OpenedReadOnly
            || session.OpenMode != WorkspaceOpenMode.ReadOnly
            || session.Writable
            || session.Provisional
            || session.Phase != WorkspaceSessionPhase.Idle)
        {
            throw new UpdateWorkspaceHealthException(
                "update.workspace_probe_session_invalid",
                "更新后的工作区健康探测未取得有效的只读会话。");
        }
    }
}

/// <summary>
/// Reads schema metadata from the runtime bound to the exact session opened by
/// the health transaction. It intentionally does not depend on the product UI
/// gateway, whose binding is asynchronous.
/// </summary>
internal sealed class CurrentRuntimeUpdateWorkspaceSchemaReader(
    ProductionWorkspaceRuntimeFactory runtime,
    IWorkspaceHostEpochLeaseSource leases,
    HttpMessageHandler? handler = null) : IUpdateWorkspaceSchemaReader
{
    private static readonly JsonElement EmptyParameters =
        JsonSerializer.SerializeToElement(new { });

    private readonly ProductionWorkspaceRuntimeFactory _runtime = runtime
        ?? throw new ArgumentNullException(nameof(runtime));
    private readonly IWorkspaceHostEpochLeaseSource _leases = leases
        ?? throw new ArgumentNullException(nameof(leases));
    private readonly HttpMessageHandler? _handler = handler;

    public async Task<int> ReadTableCountAsync(
        WorkspaceSessionV2 expectedSession,
        CancellationToken cancellationToken)
    {
        HostProductRpcBinding binding = _runtime.CaptureHostProductRpcBinding(expectedSession)
            ?? throw BindingMismatch();
        using var gateway = binding.CreateGateway(_leases, _handler);
        JsonElement response;
        try
        {
            response = await gateway.ListTablesAsync(
                EmptyParameters,
                cancellationToken).ConfigureAwait(false);
        }
        catch (BackendUnavailableException exception)
        {
            throw BindingMismatch(exception);
        }
        if (response.ValueKind != JsonValueKind.Object
            || !response.TryGetProperty("tables", out JsonElement tables)
            || tables.ValueKind != JsonValueKind.Array)
        {
            throw new UpdateWorkspaceHealthException(
                "update.workspace_probe_response_invalid",
                "更新后的工作区 schema.list 响应无效。");
        }
        return tables.GetArrayLength();
    }

    private static UpdateWorkspaceHealthException BindingMismatch(Exception? innerException = null) => new(
        "update.workspace_probe_binding_mismatch",
        "更新后的工作区运行时身份与只读会话不一致。",
        innerException);
}
