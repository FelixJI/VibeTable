using System;
using System.Diagnostics;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts.Generated;
using VibeTable.Infrastructure.Diagnostics;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Owns the complete Interface request lifecycle behind one closed host boundary.
/// The workspace dispatcher delegates named messages and owns none of the Surface
/// validation, correlation, cancellation, timeout, or error state machine.
/// </summary>
public sealed class SurfaceRequestController
{
    private readonly IWebReplySink _reply;
    private readonly CorrelatedRequestRunner<ISurfaceRpcGateway> _runner;

    public SurfaceRequestController(
        IWebReplySink reply,
        TimeSpan requestTimeout,
        Func<CancellationToken>? sessionToken = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _runner = new CorrelatedRequestRunner<ISurfaceRpcGateway>(
            _reply,
            requestTimeout,
            sessionToken ?? (() => CancellationToken.None),
            new CorrelatedRequestPolicy(
                "界面服务尚未连接。",
                "SURFACE_BACKEND_UNAVAILABLE",
                "界面请求参数无效。",
                "BAD_PAYLOAD",
                "界面请求标识重复。",
                "SURFACE_DUPLICATE_REQUEST",
                "界面请求已取消。",
                "SURFACE_CANCELLED",
                "界面请求超时。",
                "SURFACE_TIMEOUT",
                exception =>
                {
                    SurfaceErrorMapper.Failure failure = SurfaceErrorMapper.Map(exception);
                    return new CorrelatedRequestFailure(failure.Message, failure.Code);
                },
                (requestType, code) => Trace.TraceError(DiagnosticEvent.Failure(
                    "VibeTable.Desktop.SurfaceRequestController",
                    requestType,
                    code))));
    }

    public void SetGateway(ISurfaceRpcGateway gateway) => _runner.SetGateway(gateway);

    public static bool Handles(string requestType)
        => requestType is
            "interface.listRequested" or
            "interface.loadRequested" or
            "interface.commitRequested" or
            "interface.deleteRequested" or
            "interface.cancelRequested";

    public Task DispatchAsync(RoutedWebRequest request)
        => request.Type switch
        {
            "interface.listRequested" => ListAsync(request),
            "interface.loadRequested" => LoadAsync(request),
            "interface.commitRequested" => CommitAsync(request),
            "interface.deleteRequested" => DeleteAsync(request),
            "interface.cancelRequested" => CancelAsync(request),
            _ => RejectUnknownAsync(request),
        };

    private Task ListAsync(RoutedWebRequest request)
    {
        if (!TryDeserialize<InterfaceListRequest>(request.Payload, out _))
            return RejectPayloadAsync(request);
        return RunAsync(request, "interface.listLoaded", (gateway, token) =>
            gateway.ListAsync(token));
    }

    private Task LoadAsync(RoutedWebRequest request)
    {
        if (!TryDeserialize(request.Payload, out InterfaceLoadRequest? parameters)
            || parameters is null
            || string.IsNullOrWhiteSpace(parameters.InterfaceId))
            return RejectPayloadAsync(request);
        return RunAsync(request, "interface.loaded", (gateway, token) =>
            gateway.LoadAsync(parameters.InterfaceId, token));
    }

    private Task CommitAsync(RoutedWebRequest request)
    {
        if (!TryDeserialize(request.Payload, out InterfaceCommitRequest? parameters)
            || parameters is null
            || parameters.Definition is null
            || string.IsNullOrWhiteSpace(parameters.Definition.InterfaceId)
            || string.IsNullOrWhiteSpace(parameters.IdempotencyKey))
            return RejectPayloadAsync(request);
        return RunAsync(request, "interface.committed", (gateway, token) =>
            gateway.CommitAsync(parameters, token));
    }

    private Task DeleteAsync(RoutedWebRequest request)
    {
        if (!TryDeserialize(request.Payload, out InterfaceDeleteRequest? parameters)
            || parameters is null
            || string.IsNullOrWhiteSpace(parameters.InterfaceId)
            || string.IsNullOrWhiteSpace(parameters.ExpectedRevision)
            || string.IsNullOrWhiteSpace(parameters.IdempotencyKey))
            return RejectPayloadAsync(request);
        return RunAsync(request, "interface.deleted", (gateway, token) =>
            gateway.DeleteAsync(parameters, token));
    }

    private Task CancelAsync(RoutedWebRequest request)
    {
        if (!TryDeserialize(request.Payload, out InterfaceCancelRequest? parameters)
            || parameters is null
            || string.IsNullOrWhiteSpace(parameters.TargetRequestId))
            return RejectPayloadAsync(request);
        _runner.Cancel(parameters.TargetRequestId);
        return Task.CompletedTask;
    }

    private async Task RunAsync<TResult>(
        RoutedWebRequest request,
        string responseType,
        Func<ISurfaceRpcGateway, CancellationToken, Task<TResult>> operation)
        => await _runner.RunAsync(request, responseType, operation).ConfigureAwait(false);

    private Task RejectPayloadAsync(RoutedWebRequest request)
    {
        _reply.PostOperationFailed(request.RequestId, "界面请求参数无效。", "BAD_PAYLOAD");
        return Task.CompletedTask;
    }

    private Task RejectUnknownAsync(RoutedWebRequest request)
    {
        _reply.PostOperationFailed(request.RequestId, "界面请求类型无效。", "UNKNOWN_TYPE");
        return Task.CompletedTask;
    }

    private static bool TryDeserialize<T>(JsonElement payload, out T? value)
    {
        try
        {
            if (payload.ValueKind != JsonValueKind.Object)
            {
                value = default;
                return false;
            }
            value = payload.Deserialize<T>(new JsonSerializerOptions(JsonSerializerDefaults.Web));
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
