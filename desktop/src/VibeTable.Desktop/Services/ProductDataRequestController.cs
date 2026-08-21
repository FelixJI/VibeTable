using System.Diagnostics;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Diagnostics;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Owns the closed product-data and relation/Lookup request lifecycle. It
/// hides registry validation, workspace epoch protection, recovery reads, and
/// stable renderer error mapping behind one dispatch interface.
/// </summary>
public sealed class ProductDataRequestController
{
    private static readonly TimeSpan RecoveryReadPollInterval =
        TimeSpan.FromMilliseconds(25);

    private readonly IWebReplySink _reply;
    private readonly WorkspaceSessionEnvelopeFilter? _sessionEnvelopeFilter;
    private readonly TimeSpan _readRecoveryTimeout;
    private readonly object _gatewayGate = new();
    private readonly FieldChangeProtectionPlanLedger _fieldChangePlans = new();
    private IProductDataRpcGateway? _gateway;

    public ProductDataRequestController(
        IWebReplySink reply,
        TimeSpan? readRecoveryTimeout = null,
        WorkspaceSessionEnvelopeFilter? sessionEnvelopeFilter = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _readRecoveryTimeout = readRecoveryTimeout ?? TimeSpan.FromSeconds(3);
        _sessionEnvelopeFilter = sessionEnvelopeFilter;
    }

    public IProductDataRpcGateway? CurrentGateway
    {
        get
        {
            lock (_gatewayGate)
                return _gateway;
        }
    }

    public static bool Handles(string requestType)
        => ProductDataRpcRegistry.Contains(requestType)
            || RelationLookupRpcRegistry.Contains(requestType);

    public void SetGateway(IProductDataRpcGateway gateway)
    {
        ArgumentNullException.ThrowIfNull(gateway);
        lock (_gatewayGate)
        {
            _gateway = gateway;
            _fieldChangePlans.ResetGateway();
        }
    }

    public bool ClearGateway(IProductDataRpcGateway expected)
    {
        ArgumentNullException.ThrowIfNull(expected);
        lock (_gatewayGate)
        {
            if (!ReferenceEquals(_gateway, expected))
                return false;
            _gateway = null;
            _fieldChangePlans.ResetGateway();
            return true;
        }
    }

    public Task DispatchAsync(RoutedWebRequest request)
        => ProductDataRpcRegistry.Contains(request.Type)
            ? DispatchProductAsync(request)
            : RelationLookupRpcRegistry.Contains(request.Type)
                ? DispatchRelationLookupAsync(request)
                : RejectUnknownAsync(request);

    private async Task DispatchRelationLookupAsync(RoutedWebRequest request)
    {
        if (!RelationLookupRpcRegistry.TryGet(request.Type, out var endpoint))
        {
            RejectUnknown(request);
            return;
        }
        if (!endpoint.IsValidPayload(request.Payload))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                $"{request.Type} has an invalid payload.",
                "BAD_PAYLOAD");
            return;
        }
        IRelationLookupRpcGateway? gateway = CurrentGateway;
        if (gateway is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "数据服务尚未就绪。",
                "NOT_AUTHENTICATED");
            return;
        }
        try
        {
            JsonElement result = await endpoint.InvokeAsync(
                gateway,
                request.Payload,
                CancellationToken.None).ConfigureAwait(false);
            _reply.PostResponse(request.Type, request.RequestId, result);
        }
        catch (JsonException)
        {
            RejectPayload(request);
        }
        catch (RpcRemoteException exception) when (exception.Code == -32602)
        {
            RejectPayload(request);
        }
        catch (RpcRemoteException exception) when (exception.Code == -32030)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "Local data service is unavailable.",
                "BACKEND_UNAVAILABLE");
        }
        catch (Exception)
        {
            TraceFailure(request.Type, "RELATION_LOOKUP_FAILED");
            _reply.PostOperationFailed(
                request.RequestId,
                "Relation or lookup operation failed.",
                "RELATION_LOOKUP_FAILED");
        }
    }

    private async Task DispatchProductAsync(RoutedWebRequest request)
    {
        WorkspaceRequestEpochLease? epochLease = null;
        if (_sessionEnvelopeFilter is not null
            && !_sessionEnvelopeFilter.TryCapture(request.Scope, out epochLease))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "Workspace request belongs to a stale or invalid session.",
                "BAD_WORKSPACE_SCOPE");
            return;
        }

        if (!ProductDataRpcRegistry.TryGet(request.Type, out var endpoint))
        {
            RejectUnknown(request);
            epochLease?.Dispose();
            return;
        }
        if (!endpoint.IsValidPayload(request.Payload))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                $"{request.Type} has an invalid payload.",
                "BAD_PAYLOAD");
            epochLease?.Dispose();
            return;
        }
        if (endpoint.MutatesWorkspace
            && _sessionEnvelopeFilter?.Current.OpenMode
                == WorkspaceOpenMode.ReadOnly)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "The workspace is open read-only.",
                "WORKSPACE_READ_ONLY");
            epochLease?.Dispose();
            return;
        }
        (IProductDataRpcGateway? gateway,
            FieldChangeProtectionLedgerContext protectionContext) =
            CaptureProductContext(request.Scope);
        try
        {
            JsonElement forwardedPayload = await ProtectMutationAsync(
                request,
                endpoint,
                epochLease,
                protectionContext).ConfigureAwait(false);
            if (!IsRequestCurrent(epochLease))
                return;
            JsonElement result = string.Equals(
                request.Type,
                "query.page",
                StringComparison.Ordinal)
                    ? await InvokeRecoverableReadAsync(
                        endpoint,
                        request.Payload,
                        gateway,
                        epochLease).ConfigureAwait(false)
                    : gateway is null
                        ? throw new BackendUnavailableException(
                            "The local data service is not ready.")
                        : await endpoint.InvokeAsync(
                            gateway,
                            forwardedPayload,
                            epochLease?.CancellationToken
                                ?? CancellationToken.None).ConfigureAwait(false);
            if (!IsRequestCurrent(epochLease))
                return;
            endpoint.ProtectionPolicy?.ObserveSuccessfulResponse(
                result,
                _fieldChangePlans,
                protectionContext);
            _reply.PostResponse(request.Type, request.RequestId, result);
        }
        catch (OperationCanceledException)
            when (epochLease?.CancellationToken.IsCancellationRequested == true)
        {
        }
        catch (RpcRemoteException exception)
            when (exception.ErrorData is JsonElement data
                && ProductRpcErrorMapper.TryMap(data, out _))
        {
            if (!IsRequestCurrent(epochLease))
                return;
            ProductRpcErrorMapper.TryMap(exception.ErrorData!.Value, out var mapped);
            _reply.PostResponse(request.Type, request.RequestId, mapped);
        }
        catch (RpcRemoteException exception) when (exception.Code == -32602)
        {
            if (IsRequestCurrent(epochLease))
                RejectPayload(request);
        }
        catch (WorkspaceRegistryException exception)
        {
            if (IsRequestCurrent(epochLease))
            {
                _reply.PostOperationFailed(
                    request.RequestId,
                    exception.Message,
                    exception.Code);
            }
        }
        catch (Exception exception)
            when (exception is BackendUnavailableException
                or ObjectDisposedException)
        {
            if (!IsRequestCurrent(epochLease))
                return;
            Trace.TraceWarning(
                $"Product data backend unavailable ({request.Type}): {exception.Message}");
            _reply.PostOperationFailed(
                request.RequestId,
                "Local data service is reconnecting.",
                "BACKEND_UNAVAILABLE");
        }
        catch (Exception)
        {
            if (!IsRequestCurrent(epochLease))
                return;
            TraceFailure(request.Type, "PRODUCT_RPC_FAILED");
            _reply.PostOperationFailed(
                request.RequestId,
                "Product data operation failed.",
                "PRODUCT_DATA_FAILED");
        }
        finally
        {
            epochLease?.Dispose();
        }
    }

    private async Task<JsonElement> ProtectMutationAsync(
        RoutedWebRequest request,
        ProductDataRpcEndpoint endpoint,
        WorkspaceRequestEpochLease? epochLease,
        FieldChangeProtectionLedgerContext protectionContext)
    {
        ProtectionSnapshotPolicy? policy = endpoint.ProtectionPolicy;
        if (policy is null)
            return request.Payload;
        ProtectionSnapshotReceipt? protection = null;
        if (policy.RequiresSnapshot(
                request.Payload,
                _fieldChangePlans,
                protectionContext)
            && _sessionEnvelopeFilter is not null)
        {
            protection =
                await _sessionEnvelopeFilter.ProtectCurrentWithReceiptAsync(
                    $"before-{request.Type}",
                    epochLease?.CancellationToken
                        ?? CancellationToken.None).ConfigureAwait(false);
        }
        return policy.RewritePayload(request.Payload, protection);
    }

    private (
        IProductDataRpcGateway? Gateway,
        FieldChangeProtectionLedgerContext ProtectionContext)
        CaptureProductContext(WorkspaceWireScope? scope)
    {
        lock (_gatewayGate)
        {
            return (
                _gateway,
                _fieldChangePlans.BeginRequest(scope));
        }
    }

    private async Task<JsonElement> InvokeRecoverableReadAsync(
        ProductDataRpcEndpoint endpoint,
        JsonElement payload,
        IProductDataRpcGateway? initialGateway,
        WorkspaceRequestEpochLease? epochLease)
    {
        IProductDataRpcGateway? attemptedGateway = initialGateway;
        Exception? lastFailure = null;
        long deadline = Stopwatch.GetTimestamp()
            + (long)(_readRecoveryTimeout.TotalSeconds * Stopwatch.Frequency);
        while (true)
        {
            epochLease?.CancellationToken.ThrowIfCancellationRequested();
            if (!IsRequestCurrent(epochLease))
            {
                throw new OperationCanceledException(
                    epochLease?.CancellationToken ?? CancellationToken.None);
            }
            if (attemptedGateway is not null)
            {
                try
                {
                    JsonElement result = await endpoint.InvokeAsync(
                        attemptedGateway,
                        payload,
                        epochLease?.CancellationToken
                            ?? CancellationToken.None).ConfigureAwait(false);
                    if (!IsRequestCurrent(epochLease))
                    {
                        throw new OperationCanceledException(
                            epochLease?.CancellationToken
                                ?? CancellationToken.None);
                    }
                    return result;
                }
                catch (Exception exception)
                    when (exception is BackendUnavailableException
                        or ObjectDisposedException)
                {
                    lastFailure = exception;
                }
            }
            IProductDataRpcGateway? replacement = CurrentGateway;
            if (replacement is not null
                && !ReferenceEquals(replacement, attemptedGateway))
            {
                attemptedGateway = replacement;
                continue;
            }
            if (Stopwatch.GetTimestamp() >= deadline)
            {
                throw new BackendUnavailableException(
                    "The local data service did not recover before the read deadline.",
                    lastFailure ?? new InvalidOperationException(
                        "No product data gateway is currently available."));
            }
            await Task.Delay(
                RecoveryReadPollInterval,
                epochLease?.CancellationToken
                    ?? CancellationToken.None).ConfigureAwait(false);
        }
    }

    private bool IsRequestCurrent(WorkspaceRequestEpochLease? epochLease)
        => epochLease is null
            || _sessionEnvelopeFilter?.IsCurrent(epochLease) == true;

    private void RejectPayload(RoutedWebRequest request)
        => _reply.PostOperationFailed(
            request.RequestId,
            "Invalid request payload.",
            "BAD_PAYLOAD");

    private Task RejectUnknownAsync(RoutedWebRequest request)
    {
        RejectUnknown(request);
        return Task.CompletedTask;
    }

    private void RejectUnknown(RoutedWebRequest request)
        => _reply.PostOperationFailed(
            request.RequestId,
            "Unknown product data request.",
            "UNKNOWN_TYPE");

    private static void TraceFailure(string operation, string code)
        => Trace.TraceError(DiagnosticEvent.Failure(
            "VibeTable.Desktop.ProductDataRequestController",
            operation,
            code));
}
