using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

internal sealed record HostInstallPlanBinding(
    IPluginRpcGateway Gateway,
    long GatewayGeneration,
    PluginProjectContext Context,
    ProductAuthoritySnapshot Authority);

internal sealed record HostInstallPlanLease(
    string PlanId,
    string PluginId,
    HostInstallPlanBinding Binding,
    DownloadedPluginPackage? Package);

internal sealed class HostInstallPlanCleanup(
    TimeSpan timeout,
    TimeProvider timeProvider,
    Action<string>? trace)
{
    public async Task<bool> ReleaseAsync(
        HostInstallPlanLease lease,
        CancellationToken cancellationToken = default)
    {
        lease.Package?.Dispose();
        return await CancelRemoteAsync(
            lease.Binding.Gateway,
            lease.PlanId,
            cancellationToken).ConfigureAwait(false);
    }

    public async Task<bool> CancelRemoteAsync(
        IPluginRpcGateway gateway,
        string planId,
        CancellationToken cancellationToken = default)
    {
        Task<bool>? cancellation = null;
        try
        {
            using var deadline = new CancellationTokenSource(timeout, timeProvider);
            using var linked = CancellationTokenSource.CreateLinkedTokenSource(
                cancellationToken,
                deadline.Token);
            cancellation = gateway.CancelInstallAsync(
                new PluginInstallCancelParams(planId),
                linked.Token);
            return await cancellation.WaitAsync(timeout, timeProvider, cancellationToken)
                .ConfigureAwait(false);
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            Observe(cancellation);
            throw;
        }
        catch (Exception exception) when (
            exception is TimeoutException or OperationCanceledException)
        {
            Observe(cancellation);
            SafeTrace("PLUGIN_INSTALL_CANCEL_TIMEOUT");
            return false;
        }
        catch
        {
            Observe(cancellation);
            SafeTrace("PLUGIN_INSTALL_CANCEL_FAILED");
            return false;
        }
    }

    private static void Observe(Task<bool>? cancellation)
    {
        if (cancellation is not null)
            _ = ObserveLateCancellationAsync(cancellation);
    }

    private void SafeTrace(string code)
    {
        try
        {
            trace?.Invoke(code);
        }
        catch
        {
        }
    }

    private static async Task ObserveLateCancellationAsync(Task<bool> cancellation)
    {
        try
        {
            await cancellation.ConfigureAwait(false);
        }
        catch
        {
        }
    }
}

internal sealed class HostInstallPlanOperation(
    HostInstallPlanLease plan,
    ProductAuthorityEpoch.ProductAuthorityOperationLease authority,
    HostInstallPlanCleanup cleanup) : IAsyncDisposable
{
    private int _completed;
    private int _disposed;

    public HostInstallPlanLease Plan { get; } = plan;
    public ProductAuthorityEpoch.ProductAuthorityOperationLease Authority { get; } = authority;

    public void Complete() => Interlocked.Exchange(ref _completed, 1);

    public async ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0) return;
        try
        {
            Plan.Package?.Dispose();
            if (Volatile.Read(ref _completed) == 0)
            {
                await cleanup.CancelRemoteAsync(
                    Plan.Binding.Gateway,
                    Plan.PlanId).ConfigureAwait(false);
            }
        }
        finally
        {
            Authority.Dispose();
        }
    }
}

/// <summary>
/// Owns host-side install-plan and downloaded-package leases. Every gateway or
/// context transition and every admission/consumption is serialized by one
/// lock; cancellation and disposal are deliberately performed by the caller
/// after ownership has left the lock.
/// </summary>
internal sealed class HostInstallPlanLeaseRegistry
{
    private readonly object _gate = new();
    private readonly Dictionary<string, HostInstallPlanLease> _leases =
        new(StringComparer.Ordinal);
    private IPluginRpcGateway? _gateway;
    private PluginProjectContext? _context;
    private long _gatewayGeneration;
    private readonly ProductAuthorityEpoch _authority;
    private readonly HostInstallPlanCleanup _cleanup;

    public HostInstallPlanLeaseRegistry(
        ProductAuthorityEpoch authority,
        TimeSpan? cleanupTimeout = null,
        TimeProvider? timeProvider = null,
        Action<string>? cleanupTrace = null)
    {
        _authority = authority ?? throw new ArgumentNullException(nameof(authority));
        TimeSpan timeout = cleanupTimeout ?? TimeSpan.FromSeconds(2);
        if (timeout <= TimeSpan.Zero)
            throw new ArgumentOutOfRangeException(nameof(cleanupTimeout));
        _cleanup = new HostInstallPlanCleanup(
            timeout,
            timeProvider ?? TimeProvider.System,
            cleanupTrace);
    }

    internal HostInstallPlanCleanup Cleanup => _cleanup;

    public IReadOnlyList<HostInstallPlanLease> SetGateway(
        IPluginRpcGateway gateway,
        PluginProjectContext? context)
    {
        _authority.Transition(context);
        return SetGatewayAfterAuthorityTransition(gateway, context);
    }

    public IReadOnlyList<HostInstallPlanLease> SetGatewayAfterAuthorityTransition(
        IPluginRpcGateway gateway,
        PluginProjectContext? context)
    {
        lock (_gate)
        {
            IReadOnlyList<HostInstallPlanLease> released = DrainLocked();
            _gateway = gateway;
            _context = context;
            _gatewayGeneration += 1;
            return released;
        }
    }

    public IReadOnlyList<HostInstallPlanLease> ClearGateway(IPluginRpcGateway expected)
    {
        _authority.Transition(null);
        return ClearGatewayAfterAuthorityTransition(expected);
    }

    public IReadOnlyList<HostInstallPlanLease> ClearGatewayAfterAuthorityTransition(
        IPluginRpcGateway expected)
    {
        lock (_gate)
        {
            if (!ReferenceEquals(_gateway, expected)) return [];
            IReadOnlyList<HostInstallPlanLease> released = DrainLocked();
            _gateway = null;
            _gatewayGeneration += 1;
            return released;
        }
    }

    public IReadOnlyList<HostInstallPlanLease> SetContext(PluginProjectContext? context)
    {
        _authority.Transition(context);
        return SetContextAfterAuthorityTransition(context);
    }

    public IReadOnlyList<HostInstallPlanLease> SetContextAfterAuthorityTransition(
        PluginProjectContext? context)
    {
        lock (_gate)
        {
            IReadOnlyList<HostInstallPlanLease> released = DrainLocked();
            _context = context;
            return released;
        }
    }

    public HostInstallPlanBinding? Capture()
    {
        lock (_gate)
        {
            ProductAuthoritySnapshot snapshot = _authority.Snapshot();
            return _gateway is null
                || _context is null
                || snapshot.Context != _context
                ? null
                : new HostInstallPlanBinding(
                    _gateway,
                    _gatewayGeneration,
                    _context,
                    snapshot);
        }
    }

    public bool TryAdmit(
        HostInstallPlanBinding binding,
        PluginRuntimeInstallPlan plan,
        DownloadedPluginPackage? package,
        out HostInstallPlanLease? replaced)
    {
        lock (_gate)
        {
            replaced = null;
            if (!IsCurrentLocked(binding)
                || plan.ProjectKey != binding.Context.ProjectKey
                || plan.ProjectRevision != binding.Context.ProjectRevision) return false;
            _leases.Remove(plan.PlanId, out replaced);
            _leases.Add(plan.PlanId, new HostInstallPlanLease(
                plan.PlanId, plan.Manifest.PluginId, binding, package));
            return true;
        }
    }

    public bool TryTake(string planId, out HostInstallPlanLease? lease)
    {
        lock (_gate) return _leases.Remove(planId, out lease);
    }

    public bool TryBeginOperation(
        string planId,
        string? expectedPluginId,
        out HostInstallPlanOperation? operation,
        out HostInstallPlanLease? rejected)
    {
        lock (_gate)
        {
            operation = null;
            rejected = null;
            if (!_leases.Remove(planId, out HostInstallPlanLease? plan)
                || plan is null) return false;
            if (!IsCurrentLocked(plan.Binding)
                || expectedPluginId is not null
                    && !string.Equals(
                        plan.PluginId,
                        expectedPluginId,
                        StringComparison.Ordinal)
                || !_authority.TryAcquire(
                    plan.Binding.Authority,
                    out ProductAuthorityEpoch.ProductAuthorityOperationLease? authorityLease)
                || authorityLease is null)
            {
                rejected = plan;
                return false;
            }
            operation = new HostInstallPlanOperation(
                plan,
                authorityLease,
                _cleanup);
            return true;
        }
    }

    private bool IsCurrentLocked(HostInstallPlanBinding binding) =>
        ReferenceEquals(_gateway, binding.Gateway)
        && _gatewayGeneration == binding.GatewayGeneration
        && _context == binding.Context
        && _authority.IsCurrent(binding.Authority);

    private IReadOnlyList<HostInstallPlanLease> DrainLocked()
    {
        HostInstallPlanLease[] leases = [.. _leases.Values];
        _leases.Clear();
        return leases;
    }
}
