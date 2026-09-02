using System.Net.Http;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Owns one typed caller's Product HTTP readiness, not the runtime or Python client.
/// The binding callback must atomically validate the captured runtime, Python client,
/// and canonical Sidecar snapshot while admitting the synchronous start action.
/// </summary>
internal sealed class HostProductRpcInvoker : IDisposable
{
    private static readonly JsonSerializerOptions WireOptions = new(JsonSerializerDefaults.Web);
    private readonly object _gate = new();
    private readonly JsonRpcClient _client;
    private readonly ProductSidecarGenerationSnapshot _snapshot;
    private readonly IWorkspaceHostEpochLeaseSource _leases;
    private readonly Func<Func<bool>, bool> _tryUseCurrent;
    private readonly ProductRpcRouteSelector _routes;
    private readonly ProductSidecarHttpGateway _sidecar;
    private readonly CancellationTokenSource _lifetime = new();
    private Task? _ready;
    private bool _disposed;

    internal JsonRpcClient Client => _client;

    internal HostProductRpcInvoker(
        JsonRpcClient client,
        ProductSidecarGenerationSnapshot snapshot,
        IWorkspaceHostEpochLeaseSource leases,
        Func<Func<bool>, bool> tryUseCurrent,
        ProductRpcRouteSelector? routes = null,
        HttpMessageHandler? handler = null)
    {
        _client = client ?? throw new ArgumentNullException(nameof(client));
        _snapshot = snapshot ?? throw new ArgumentNullException(nameof(snapshot));
        _leases = leases ?? throw new ArgumentNullException(nameof(leases));
        _tryUseCurrent = tryUseCurrent ?? throw new ArgumentNullException(nameof(tryUseCurrent));
        _routes = routes ?? ProductRpcRouteSelector.Default;
        _sidecar = new ProductSidecarHttpGateway(snapshot.Context, snapshot.Identity,
            snapshot.Registrations, handler);
    }

    internal async Task<JsonElement> InvokeAsync(
        string method, JsonElement parameters, CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        ProductRpcCapabilityCatalog catalog = ProductDataRpcRegistry.TryGet(method, out var endpoint)
            ? endpoint.CapabilityCatalog : ProductRpcCapabilityCatalog.Product;
        if (!_routes.TrySelectProduct(method, catalog, out ProductRpcRoute route))
            throw new InvalidOperationException("The Product RPC owner is unavailable.");
        using (WorkspaceRequestEpochLease lease = CaptureLease())
        {
            CancellationToken lifetime;
            lock (_gate)
            {
                ObjectDisposedException.ThrowIf(_disposed, this);
                lifetime = _lifetime.Token;
            }
            using var call = CancellationTokenSource.CreateLinkedTokenSource(
                token, lifetime, lease.CancellationToken);
            try
            {
                if (route == ProductRpcRoute.GoSidecar)
                {
                    Task ready;
                    lock (_gate)
                    {
                        ObjectDisposedException.ThrowIf(_disposed, this);
                        ready = _ready ??= InitializeAsync(lifetime);
                    }
                    await ready.WaitAsync(call.Token).ConfigureAwait(false);
                }
                JsonElement result;
                if (route == ProductRpcRoute.PythonBff)
                {
                    result = await StartCurrent(() => _client.InvokeAsync<JsonElement, JsonElement>(
                        method, parameters, call.Token)).ConfigureAwait(false);
                }
                else
                {
                    JsonElement wire = JsonSerializer.SerializeToElement(lease.Scope, WireOptions);
                    ProductSidecarForwardResult response = await StartCurrent(() => _sidecar.ForwardAsync(
                        Guid.NewGuid().ToString("D"), method, wire, parameters, call.Token)).ConfigureAwait(false);
                    result = response switch
                    {
                        ProductSidecarSuccess success => success.Result,
                        ProductSidecarFailure failure => throw new RpcRemoteException(
                            failure.Error.Code, failure.Error.Message, failure.Error.Data),
                        _ => throw new InvalidOperationException("Invalid Product RPC response."),
                    };
                }
                EnsureCurrent(lease, call.Token);
                return result;
            }
            catch
            {
                // A late failure belongs to the retired binding just as a late result does.
                EnsureCurrent(lease, call.Token);
                throw;
            }
        }
    }

    private void EnsureCurrent(WorkspaceRequestEpochLease lease, CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        if (!_leases.IsCurrent(lease) || !_tryUseCurrent(() => true))
            throw Unavailable();
    }

    private async Task InitializeAsync(CancellationToken lifetime)
    {
        // A caller may stop waiting, but drain must still own the actual HTTP request.
        using WorkspaceRequestEpochLease lease = CaptureLease();
        using var handshake = CancellationTokenSource.CreateLinkedTokenSource(
            lifetime, lease.CancellationToken);
        await StartCurrent(() => _sidecar.GetCapabilitiesAsync(handshake.Token)).ConfigureAwait(false);
    }

    private WorkspaceRequestEpochLease CaptureLease()
    {
        if (!_leases.TryCaptureHost(Guid.Parse(_snapshot.Identity.WorkspaceId),
                _snapshot.Identity.SessionEpoch, Guid.NewGuid(), out WorkspaceRequestEpochLease? lease)
            || lease is null)
            throw Unavailable();
        return lease;
    }

    private Task<T> StartCurrent<T>(Func<Task<T>> start)
    {
        Task<T>? pending = null;
        if (!_tryUseCurrent(() => { pending = start(); return true; }))
            throw Unavailable();
        return pending!;
    }

    public void Dispose()
    {
        lock (_gate)
        {
            if (_disposed) return;
            _disposed = true;
        }
        _lifetime.Cancel();
        _sidecar.Dispose();
        _lifetime.Dispose();
    }

    private static BackendUnavailableException Unavailable() =>
        new("The host Product RPC binding is no longer current.");
}
