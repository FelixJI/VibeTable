using System.Net.Http;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Owns one typed caller's Product HTTP readiness, not the runtime or Python client.
/// Python starts validate the captured ready client; Go starts validate the
/// current runtime and canonical Sidecar snapshot independently.
/// </summary>
internal sealed partial class HostProductRpcInvoker : IDisposable
{
    private static readonly JsonSerializerOptions WireOptions = new(JsonSerializerDefaults.Web);
    private readonly object _gate = new();
    private readonly JsonRpcClient? _client;
    private readonly ProductSidecarGenerationSnapshot _snapshot;
    private readonly IWorkspaceHostEpochLeaseSource _leases;
    private readonly Func<Func<bool>, bool> _tryUseCurrent;
    private readonly Func<Func<bool>, bool> _tryUseGoCurrent;
    private readonly ProductRpcRouteSelector _routes;
    private readonly HostDataIoTaskRegistry _taskOwner;
    private readonly ProductSidecarHttpGateway _sidecar;
    private readonly CancellationTokenSource _lifetime = new();
    private Task? _ready;
    private bool _disposed;

    internal JsonRpcClient? Client => _client;
    internal HostDataIoTaskRegistry TaskOwner => _taskOwner;

    internal HostProductRpcInvoker(
        JsonRpcClient? client,
        ProductSidecarGenerationSnapshot snapshot,
        IWorkspaceHostEpochLeaseSource leases,
        Func<Func<bool>, bool> tryUseCurrent,
        ProductRpcRouteSelector? routes = null,
        HttpMessageHandler? handler = null,
        Func<Func<bool>, bool>? tryUseGoCurrent = null,
        HostDataIoTaskRegistry? taskOwner = null)
    {
        _client = client;
        _snapshot = snapshot ?? throw new ArgumentNullException(nameof(snapshot));
        _leases = leases ?? throw new ArgumentNullException(nameof(leases));
        _tryUseCurrent = tryUseCurrent ?? throw new ArgumentNullException(nameof(tryUseCurrent));
        _tryUseGoCurrent = tryUseGoCurrent ?? _tryUseCurrent;
        _routes = routes ?? ProductRpcRouteSelector.Default;
        _taskOwner = taskOwner ?? new HostDataIoTaskRegistry();
        _taskOwner.BindClient(client, snapshot.Identity);
        _sidecar = new ProductSidecarHttpGateway(snapshot.Context, snapshot.Identity,
            snapshot.Registrations, handler);
    }

    internal async Task<JsonElement> InvokeAsync(
        string method, JsonElement parameters, CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        ProductRpcCapabilityCatalog catalog = ProductDataRpcRegistry.TryGet(method, out var endpoint)
            ? endpoint.CapabilityCatalog : ProductRpcCapabilityCatalog.Product;
        ProductRpcRoute route = default;
        bool native = IsNativeFileMethod(method);
        if (!native && !_routes.TrySelectProduct(method, catalog, out route))
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
                if (native)
                {
                    EnsureCurrent(lease, call.Token);
                    result = await InvokeNativeFileAsync(method, parameters, call.Token).ConfigureAwait(false);
                }
                else if (route == ProductRpcRoute.HostDataIo)
                {
                    result = await InvokeTaskAsync(method, parameters, call.Token)
                        .ConfigureAwait(false);
                }
                else if (route == ProductRpcRoute.PythonBff)
                {
                    result = await StartCurrent(() => _client!.InvokeAsync<JsonElement, JsonElement>(
                        method, parameters, call.Token)).ConfigureAwait(false);
                }
                else
                {
                    JsonElement wire = JsonSerializer.SerializeToElement(lease.Scope, WireOptions);
                    ProductSidecarForwardResult response = await StartCurrent(() => _sidecar.ForwardAsync(
                        Guid.NewGuid().ToString("D"), method, wire, parameters, call.Token),
                        go: true).ConfigureAwait(false);
                    result = response switch
                    {
                        ProductSidecarSuccess success => success.Result,
                        ProductSidecarFailure failure => throw new RpcRemoteException(
                            failure.Error.Code, failure.Error.Message, failure.Error.Data),
                        _ => throw new InvalidOperationException("Invalid Product RPC response."),
                    };
                }
                EnsureCurrent(lease, call.Token, (route is ProductRpcRoute.GoSidecar or ProductRpcRoute.HostDataIo) && !native);
                return result;
            }
            catch (OperationCanceledException) when (lifetime.IsCancellationRequested
                && !token.IsCancellationRequested && !lease.CancellationToken.IsCancellationRequested)
            {
                // Retiring this binding is a service outage, not caller cancellation.
                throw Unavailable();
            }
            catch
            {
                // A late failure belongs to the retired binding just as a late result does.
                EnsureCurrent(lease, call.Token, (route is ProductRpcRoute.GoSidecar or ProductRpcRoute.HostDataIo) && !native);
                throw;
            }
        }
    }

    private async Task<JsonElement> InvokeTaskAsync(
        string method, JsonElement parameters, CancellationToken token)
    {
        if (method == "task.status")
        {
            string id = parameters.GetProperty("taskId").GetString()
                ?? throw new JsonException("Task ID is required.");
            return _taskOwner.Status(id);
        }
        if (method == "task.cancel")
        {
            string id = parameters.GetProperty("taskId").GetString()
                ?? throw new JsonException("Task ID is required.");
            var (snapshot, client) = _taskOwner.RequestCancel(id);
            if (client is not null && ReferenceEquals(client, _client))
            {
                try
                {
                    await StartCurrent(() => client.InvokeAsync<JsonElement, bool>(
                        "task.cancelExecution", parameters, token)).ConfigureAwait(false);
                }
                catch (Exception) when (!token.IsCancellationRequested)
                {
                    // Transport retirement settles the Host record. A lost cancel
                    // reply is never treated as a confirmed worker cancellation.
                }
            }
            return snapshot;
        }
        if (method != "task.create")
            throw new JsonException("Unknown Host Data IO task method.");
        string kind = parameters.GetProperty("kind").GetString()
            ?? throw new JsonException("Task kind is required.");
        JsonElement taskParams = parameters.GetProperty("params");
        JsonRpcClient clientForStart = _client
            ?? throw Unavailable();
        var (taskId, _) = _taskOwner.Admit(
            clientForStart, _snapshot.Identity, kind);
        try
        {
            JsonElement request = JsonSerializer.SerializeToElement(new
            {
                taskId,
                kind,
                @params = taskParams,
            }, WireOptions);
            JsonElement accepted = await StartCurrent(() =>
                clientForStart.InvokeAsync<JsonElement, JsonElement>(
                    "task.startExecution", request, token)).ConfigureAwait(false);
            if (!accepted.TryGetProperty("accepted", out JsonElement confirmed)
                || confirmed.ValueKind != JsonValueKind.True)
                throw new JsonException("Worker did not acknowledge Data IO execution.");
            return _taskOwner.Status(taskId);
        }
        catch
        {
            _taskOwner.AbortTask(taskId,
                "数据任务启动回执不可确认；业务提交结果待核实，请核对数据后重新预览。");
            throw;
        }
    }
    internal async Task<JsonElement> ExecuteExportAsync(JsonElement parameters, CancellationToken token)
    {
        if (_client is null)
            throw Unavailable();
        using WorkspaceRequestEpochLease lease = CaptureLease();
        EnsureCurrent(lease, token);
        string grantId = parameters.GetProperty("grantId").GetString()
            ?? throw new JsonException("Export grant is required.");
        try
        {
            JsonElement status = await InvokeAsync("task.create", JsonSerializer.SerializeToElement(
                new { kind = "data.export", @params = parameters }), token).ConfigureAwait(false);
            string taskId = status.GetProperty("taskId").GetString()
                ?? throw new JsonException("Export task ID is missing.");
            while (true)
            {
                EnsureCurrent(lease, token);
                string? state = status.GetProperty("state").GetString();
                if (state == "succeeded") return status.GetProperty("result").Clone();
                if (state == "failed") throw new InvalidOperationException("Export failed.");
                if (state == "cancelled") throw new OperationCanceledException("Export cancelled.");
                if (state == "aborted") throw new InvalidOperationException(
                    "Export execution was interrupted; the output outcome is unknown.");
                if (state is not ("queued" or "running")) throw new JsonException("Invalid export state.");
                await Task.Delay(100, token).ConfigureAwait(false);
                status = await InvokeAsync("task.status", JsonSerializer.SerializeToElement(new { taskId }), token)
                    .ConfigureAwait(false);
            }
        }
        finally
        {
            // The known grant also identifies tasks whose create reply was lost.
            // Only this captured client's export grant can be retired after epoch cancellation.
            // Keep our lease until the server has closed its writers; never use a new binding.
            using var cleanup = new CancellationTokenSource(TimeSpan.FromSeconds(10));
            await Files.RevokeAsync(grantId).ConfigureAwait(false);
            JsonElement settled = await _client!.InvokeAsync<JsonElement, JsonElement>(
                "task.settleExport", JsonSerializer.SerializeToElement(new { grantId }), cleanup.Token)
                .ConfigureAwait(false);
            if (settled.GetProperty("grantId").GetString() != grantId
                || !settled.GetProperty("settled").GetBoolean())
                throw new InvalidOperationException("Export cleanup was not confirmed.");
        }
    }

    private void EnsureCurrent(WorkspaceRequestEpochLease lease, CancellationToken token, bool go = false)
    {
        token.ThrowIfCancellationRequested();
        if (!_leases.IsCurrent(lease)
            || !(go ? _tryUseGoCurrent : _tryUseCurrent)(() => true))
            throw Unavailable();
    }

    private async Task InitializeAsync(CancellationToken lifetime)
    {
        // A caller may stop waiting, but drain must still own the actual HTTP request.
        using WorkspaceRequestEpochLease lease = CaptureLease();
        using var handshake = CancellationTokenSource.CreateLinkedTokenSource(
            lifetime, lease.CancellationToken);
        await StartCurrent(() => _sidecar.GetCapabilitiesAsync(handshake.Token), go: true)
            .ConfigureAwait(false);
    }

    private WorkspaceRequestEpochLease CaptureLease()
    {
        if (!_leases.TryCaptureHost(Guid.Parse(_snapshot.Identity.WorkspaceId),
                _snapshot.Identity.SessionEpoch, Guid.NewGuid(), out WorkspaceRequestEpochLease? lease)
            || lease is null)
            throw Unavailable();
        return lease;
    }

    private Task<T> StartCurrent<T>(Func<Task<T>> start, bool go = false)
    {
        Task<T>? pending = null;
        if (!(go ? _tryUseGoCurrent : _tryUseCurrent)(
                () => { pending = start(); return true; }))
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
        if (_files is not null) _client!.UnregisterHostFileHandler(_files);
        _sidecar.Dispose();
        _lifetime.Dispose();
    }

    private static BackendUnavailableException Unavailable() =>
        new("The host Product RPC binding is no longer current.");
}
