using System.Net.Http;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

// A fresh, lease-bound Go read. This adapter does not require a Python client
// and does not retain a catalog, schema revision or task projection.
internal sealed class ProductRealtimeCatalog : IDisposable
{
    private readonly ProductSidecarHttpGateway _gateway;
    private readonly Func<Func<bool>, bool> _current;

    internal ProductRealtimeCatalog(ProductSidecarGenerationSnapshot snapshot,
        Func<Func<bool>, bool> current, HttpMessageHandler? handler)
    {
        _gateway = new(snapshot.Context, snapshot.Identity, snapshot.Registrations, handler);
        _current = current;
    }

    internal async Task<TableSummary> ReadAsync(WorkspaceRequestEpochLease lease, CancellationToken token)
    {
        await Start(() => _gateway.GetCapabilitiesAsync(token)).ConfigureAwait(false);
        var options = new JsonSerializerOptions(JsonSerializerDefaults.Web);
        ProductSidecarForwardResult response = await Start(() => _gateway.ForwardAsync(
            Guid.NewGuid().ToString("D"), "schema.list", JsonSerializer.SerializeToElement(lease.Scope, options),
            JsonSerializer.SerializeToElement(new { }), token)).ConfigureAwait(false);
        token.ThrowIfCancellationRequested();
        if (!_current(() => true)) throw new BackendUnavailableException("Realtime catalog generation retired.");
        return response switch
        {
            ProductSidecarSuccess success => PocketBaseTableGateway.ParseTableSummary(success.Result),
            ProductSidecarFailure failure => throw new RpcRemoteException(
                failure.Error.Code, "Realtime catalog read failed.", null),
            _ => throw new InvalidOperationException("Invalid realtime catalog response."),
        };
    }

    private Task<T> Start<T>(Func<Task<T>> start)
    {
        Task<T>? pending = null;
        if (!_current(() => { pending = start(); return true; }))
            throw new BackendUnavailableException("Realtime catalog generation retired.");
        return pending!;
    }

    public void Dispose() => _gateway.Dispose();
}
