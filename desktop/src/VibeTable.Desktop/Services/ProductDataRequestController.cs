using System.Diagnostics;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Diagnostics;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Owns the closed product-data and relation/Lookup request lifecycle. It
/// hides registry validation, workspace epoch protection, and
/// stable renderer error mapping behind one dispatch interface.
/// </summary>
public sealed class ProductDataRequestController
{
    private readonly IWebReplySink _reply;
    private readonly ProductRpcRouteSelector _routeSelector;
    private readonly WorkspaceSessionEnvelopeFilter? _sessionEnvelopeFilter;
    private readonly object _gatewayGate = new();
    private readonly FieldChangeProtectionPlanLedger _fieldChangePlans = new();
    private IProductDataRpcGateway? _gateway;
    private IProductSidecarRpcForwarder? _sidecarForwarder;

    public ProductDataRequestController(
        IWebReplySink reply,
        WorkspaceSessionEnvelopeFilter? sessionEnvelopeFilter = null)
        : this(
            reply,
            ProductRpcRouteSelector.Default,
            sessionEnvelopeFilter)
    {
    }

    internal ProductDataRequestController(
        IWebReplySink reply,
        ProductRpcRouteSelector routeSelector,
        WorkspaceSessionEnvelopeFilter? sessionEnvelopeFilter = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _routeSelector = routeSelector
            ?? throw new ArgumentNullException(nameof(routeSelector));
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

    internal void SetProductSidecarForwarder(
        IProductSidecarRpcForwarder forwarder)
    {
        ArgumentNullException.ThrowIfNull(forwarder);
        lock (_gatewayGate)
            _sidecarForwarder = forwarder;
    }

    internal bool ClearProductSidecarForwarder(
        IProductSidecarRpcForwarder expected)
    {
        ArgumentNullException.ThrowIfNull(expected);
        lock (_gatewayGate)
        {
            if (!ReferenceEquals(_sidecarForwarder, expected))
                return false;
            _sidecarForwarder = null;
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
        using WorkspaceRequestEpochLease? requestLease = epochLease;
        if (!RelationLookupRpcRegistry.TryGet(request.Type, out var endpoint))
        {
            RejectUnknown(request);
            return;
        }
        if (!_routeSelector.TrySelectRelation(
                request.Type,
                out ProductRpcRoute relationRoute))
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
        var (gateway, sidecarForwarder, _) = CaptureProductContext(request.Scope);
        if (relationRoute == ProductRpcRoute.PythonBff && gateway is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "数据服务尚未就绪。",
                "NOT_AUTHENTICATED");
            return;
        }
        try
        {
            JsonElement result;
            if (relationRoute == ProductRpcRoute.GoSidecar)
            {
                if (string.IsNullOrWhiteSpace(request.RequestId)
                    || request.Wire.ValueKind != JsonValueKind.Object)
                {
                    RejectPayload(request);
                    return;
                }
                if (sidecarForwarder is null)
                    throw new BackendUnavailableException(
                        "The Product Sidecar route is not bound.");
                ProductSidecarForwardResult forwarded =
                    await sidecarForwarder.ForwardAsync(
                        request.RequestId,
                        request.Type,
                        request.Wire,
                        request.Payload,
                        epochLease?.CancellationToken
                            ?? CancellationToken.None).ConfigureAwait(false);
                result = forwarded switch
                {
                    ProductSidecarSuccess success => success.Result,
                    ProductSidecarFailure failure => throw new RpcRemoteException(
                        failure.Error.Code,
                        failure.Error.Message,
                        failure.Error.Data),
                    _ => throw new BackendUnavailableException(
                        "The Product Sidecar returned an unknown outcome."),
                };
            }
            else
            {
                result = await endpoint.InvokeAsync(
                    gateway!,
                    request.Payload,
                    epochLease?.CancellationToken ?? CancellationToken.None).ConfigureAwait(false);
            }
            if (!IsRequestCurrent(epochLease))
            {
                PostRetiredRelationRequest();
                return;
            }
            _reply.PostResponse(request.Type, request.RequestId, result);
        }
        catch (Exception) when (!IsRequestCurrent(epochLease))
        {
            PostRetiredRelationRequest();
        }
        catch (JsonException)
        {
            RejectPayload(request);
        }
        catch (RpcRemoteException exception) when (exception.Code == -32602)
        {
            RejectPayload(request);
        }
        catch (Exception exception) when (exception is BackendUnavailableException
            or ObjectDisposedException
            || exception is RpcRemoteException { Code: -32030 })
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

        void PostRetiredRelationRequest()
        {
            if (string.IsNullOrWhiteSpace(request.RequestId)) return;
            _reply.PostOperationFailed(
                request.RequestId,
                "The workspace request was cancelled because its session ended.",
                "workspace.session_stale");
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
        if (!_routeSelector.TrySelectProduct(
                request.Type,
                endpoint.CapabilityCatalog,
                out ProductRpcRoute route))
        {
            RejectUnknown(request);
            epochLease?.Dispose();
            return;
        }
        (IProductDataRpcGateway? gateway,
            IProductSidecarRpcForwarder? sidecarForwarder,
            FieldChangeProtectionLedgerContext protectionContext) =
            CaptureProductContext(request.Scope);
        try
        {
            JsonElement forwardedPayload = await ProtectMutationAsync(
                request,
                endpoint,
                epochLease,
                protectionContext).ConfigureAwait(false);
            if (!CanCompleteRequest(request, epochLease))
                return;
            JsonElement result;
            if (route == ProductRpcRoute.GoSidecar)
            {
                if (string.IsNullOrWhiteSpace(request.RequestId)
                    || request.Wire.ValueKind != JsonValueKind.Object)
                {
                    RejectPayload(request);
                    return;
                }
                if (sidecarForwarder is null)
                    throw new BackendUnavailableException(
                        "The Product Sidecar route is not bound.");
                ProductSidecarForwardResult forwarded =
                    await sidecarForwarder.ForwardAsync(
                        request.RequestId,
                        request.Type,
                        request.Wire,
                        forwardedPayload,
                        epochLease?.CancellationToken
                            ?? CancellationToken.None).ConfigureAwait(false);
                if (!CanCompleteRequest(request, epochLease))
                    return;
                if (forwarded is ProductSidecarFailure failure)
                {
                    PostSidecarFailure(request, failure.Error);
                    return;
                }
                if (forwarded is not ProductSidecarSuccess success)
                    throw new BackendUnavailableException(
                        "The Product Sidecar returned an unknown outcome.");
                result = success.Result;
            }
            else
            {
                result = gateway is null
                    ? throw new BackendUnavailableException(
                        "The local data service is not ready.")
                    : await endpoint.InvokeAsync(
                        gateway,
                        forwardedPayload,
                        epochLease?.CancellationToken
                            ?? CancellationToken.None).ConfigureAwait(false);
            }
            if (!CanCompleteRequest(request, epochLease))
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
            PostRetiredRequest(request);
        }
        catch (RpcRemoteException exception)
            when (exception.ErrorData is JsonElement data
                && ProductRpcErrorMapper.TryMap(data, out _))
        {
            if (!CanCompleteRequest(request, epochLease))
                return;
            ProductRpcErrorMapper.TryMap(exception.ErrorData!.Value, out var mapped);
            _reply.PostResponse(request.Type, request.RequestId, mapped);
        }
        catch (RpcRemoteException exception) when (exception.Code == -32602)
        {
            if (CanCompleteRequest(request, epochLease))
                RejectPayload(request);
        }
        catch (WorkspaceRegistryException exception)
        {
            if (CanCompleteRequest(request, epochLease))
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
            if (!CanCompleteRequest(request, epochLease))
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
            if (!CanCompleteRequest(request, epochLease))
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
        IProductSidecarRpcForwarder? SidecarForwarder,
        FieldChangeProtectionLedgerContext ProtectionContext)
        CaptureProductContext(WorkspaceWireScope? scope)
    {
        lock (_gatewayGate)
        {
            return (
                _gateway,
                _sidecarForwarder,
                _fieldChangePlans.BeginRequest(scope));
        }
    }

    private void PostSidecarFailure(
        RoutedWebRequest request,
        ProductSidecarRpcError error)
    {
        if (error.Code == -32602)
        {
            RejectPayload(request);
            return;
        }
        if (error.Code == -32150
            && error.Data is JsonElement data
            && ProductRpcErrorMapper.TryMap(data, out JsonElement mapped))
        {
            _reply.PostResponse(request.Type, request.RequestId, mapped);
            return;
        }
        TraceFailure(request.Type, "PRODUCT_RPC_FAILED");
        _reply.PostOperationFailed(
            request.RequestId,
            "Product data operation failed.",
            "PRODUCT_DATA_FAILED");
    }

    private bool CanCompleteRequest(
        RoutedWebRequest request, WorkspaceRequestEpochLease? epochLease)
    {
        if (IsRequestCurrent(epochLease)) return true;
        PostRetiredRequest(request);
        return false;
    }

    private void PostRetiredRequest(RoutedWebRequest request)
    {
        if (string.IsNullOrWhiteSpace(request.RequestId)) return;
        _reply.PostOperationFailed(
            request.RequestId,
            "The workspace request was cancelled because its session ended.",
            "workspace.session_stale");
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
