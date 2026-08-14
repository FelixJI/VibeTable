using System;
using System.Diagnostics;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Diagnostics;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Owns the complete Dashboard request lifecycle behind one closed host interface.
/// The workspace dispatcher delegates named messages and owns none of the Dashboard
/// validation, correlation, cancellation, timeout, concurrency, or error state machine.
/// </summary>
public sealed class DashboardRequestController
{
    private readonly IWebReplySink _reply;
    private readonly CorrelatedRequestRunner<IDashboardRpcGateway> _runner;
    private readonly SemaphoreSlim _queryGate = new(6, 6);
    private CancellationToken _sessionToken;

    public DashboardRequestController(
        IWebReplySink reply,
        TimeSpan requestTimeout,
        Func<CancellationToken>? sessionToken = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _runner = new CorrelatedRequestRunner<IDashboardRpcGateway>(
            _reply,
            requestTimeout,
            sessionToken ?? (() => _sessionToken),
            new CorrelatedRequestPolicy(
                "仪表盘服务尚未连接。",
                "NOT_AUTHENTICATED",
                "仪表盘请求必须携带 requestId。",
                "BAD_PAYLOAD",
                "仪表盘请求标识重复。",
                "DASHBOARD_DUPLICATE_REQUEST",
                "仪表盘请求已取消。",
                "DASHBOARD_CANCELLED",
                "仪表盘请求超时。",
                "DASHBOARD_TIMEOUT",
                exception =>
                {
                    DashboardErrorMapper.Failure failure = DashboardErrorMapper.Map(exception);
                    return new CorrelatedRequestFailure(failure.Message, failure.Code);
                },
                (requestType, code) => Trace.TraceError(DiagnosticEvent.Failure(
                    "VibeTable.Desktop.DashboardRequestController",
                    requestType,
                    code))));
    }

    public void SetGateway(
        IDashboardRpcGateway gateway,
        CancellationToken sessionToken = default)
    {
        ArgumentNullException.ThrowIfNull(gateway);
        _sessionToken = sessionToken;
        _runner.SetGateway(gateway);
    }

    public static bool Handles(string requestType)
        => requestType is
            "dashboard.listRequested" or
            "dashboard.readRequested" or
            "dashboard.manifestRequested" or
            "dashboard.queryRequested" or
            "dashboard.saveRequested" or
            "dashboard.deleteRequested" or
            "dashboard.cancelRequested";

    public Task DispatchAsync(RoutedWebRequest request)
        => request.Type switch
        {
            "dashboard.listRequested" => RunAsync(
                request,
                "dashboard.listLoaded",
                false,
                (gateway, token) => gateway.ListDashboardsAsync(token)),
            "dashboard.readRequested" => ReadAsync(request),
            "dashboard.manifestRequested" => RunAsync(
                request,
                "dashboard.manifestLoaded",
                false,
                LoadManifestAsync),
            "dashboard.queryRequested" => QueryAsync(request),
            "dashboard.saveRequested" => SaveAsync(request),
            "dashboard.deleteRequested" => DeleteAsync(request),
            "dashboard.cancelRequested" => CancelAsync(request),
            _ => RejectUnknownAsync(request),
        };

    private Task ReadAsync(RoutedWebRequest request)
    {
        string? dashboardId = GetString(request.Payload, "dashboardId");
        if (string.IsNullOrWhiteSpace(dashboardId))
            return RejectPayloadAsync(request, "缺少仪表盘标识。");
        return RunAsync(
            request,
            "dashboard.loaded",
            false,
            (gateway, token) => gateway.ReadDashboardWorkspaceAsync(dashboardId, token));
    }

    private Task QueryAsync(RoutedWebRequest request)
    {
        if (!TryDeserialize(request.Payload, out ExecuteDashboardQueryParams? parameters)
            || parameters is null
            || string.IsNullOrWhiteSpace(parameters.PanelType)
            || parameters.Query is null)
            return RejectPayloadAsync(request, "仪表盘查询参数无效。");
        parameters = parameters with { RequestId = request.RequestId };
        return RunAsync(
            request,
            "dashboard.queryLoaded",
            true,
            (gateway, token) => gateway.ExecuteDashboardQueryAsync(parameters, token));
    }

    private Task SaveAsync(RoutedWebRequest request)
    {
        if (!TryDeserialize(request.Payload, out SaveDashboardDraftParams? parameters)
            || parameters is null
            || string.IsNullOrWhiteSpace(parameters.Name)
            || string.IsNullOrWhiteSpace(parameters.IdempotencyKey))
            return RejectPayloadAsync(request, "仪表盘草稿参数无效。");
        return RunAsync(
            request,
            "dashboard.saved",
            false,
            (gateway, token) => gateway.SaveDashboardDraftAsync(parameters, token));
    }

    private Task DeleteAsync(RoutedWebRequest request)
    {
        string? dashboardId = GetString(request.Payload, "dashboardId");
        if (string.IsNullOrWhiteSpace(dashboardId))
            return RejectPayloadAsync(request, "缺少仪表盘标识。");
        return RunAsync(
            request,
            "dashboard.deleted",
            false,
            (gateway, token) => gateway.DeleteDashboardAsync(dashboardId, token));
    }

    private Task CancelAsync(RoutedWebRequest request)
    {
        string? targetRequestId = GetString(request.Payload, "targetRequestId");
        if (string.IsNullOrWhiteSpace(targetRequestId))
            return RejectPayloadAsync(request, "缺少待取消的请求标识。");
        _runner.Cancel(targetRequestId);
        return Task.CompletedTask;
    }

    private async Task RunAsync<TResult>(
        RoutedWebRequest request,
        string responseType,
        bool isQuery,
        Func<IDashboardRpcGateway, CancellationToken, Task<TResult>> operation)
        => await _runner.RunAsync(
            request,
            responseType,
            operation,
            isQuery ? _queryGate : null).ConfigureAwait(false);

    private static async Task<DashboardManifestBundle> LoadManifestAsync(
        IDashboardRpcGateway gateway,
        CancellationToken token)
    {
        Task<PanelManifestResult> manifest = gateway.GetPanelManifestAsync(token);
        Task<DashboardQueryLimits> limits = gateway.GetDashboardQueryLimitsAsync(token);
        await Task.WhenAll(manifest, limits).ConfigureAwait(false);
        return new DashboardManifestBundle(
            await manifest.ConfigureAwait(false),
            await limits.ConfigureAwait(false));
    }

    private Task RejectPayloadAsync(RoutedWebRequest request, string message)
    {
        _reply.PostOperationFailed(request.RequestId, message, "BAD_PAYLOAD");
        return Task.CompletedTask;
    }

    private Task RejectUnknownAsync(RoutedWebRequest request)
    {
        _reply.PostOperationFailed(request.RequestId, "仪表盘请求类型无效。", "UNKNOWN_TYPE");
        return Task.CompletedTask;
    }

    private static string? GetString(JsonElement payload, string property)
        => payload.ValueKind == JsonValueKind.Object
            && payload.TryGetProperty(property, out JsonElement value)
            && value.ValueKind == JsonValueKind.String
                ? value.GetString()
                : null;

    private static bool TryDeserialize<T>(JsonElement payload, out T? value)
    {
        try
        {
            if (payload.ValueKind != JsonValueKind.Object)
            {
                value = default;
                return false;
            }
            value = payload.Deserialize<T>(new JsonSerializerOptions(JsonSerializerDefaults.Web)
            {
                // Backend canonicalization sorts object keys. JSON object member order is
                // not semantic, so a workspace query must remain valid when its `kind`
                // discriminator is no longer the first member after a read/save round trip.
                AllowOutOfOrderMetadataProperties = true,
            });
            return value is not null;
        }
        catch (JsonException)
        {
            value = default;
            return false;
        }
        catch (NotSupportedException)
        {
            value = default;
            return false;
        }
    }
}
