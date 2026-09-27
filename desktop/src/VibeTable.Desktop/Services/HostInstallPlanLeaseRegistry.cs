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

/// <summary>
/// One admitted install plan together with its optional host-owned download.
/// The full plan is the host's execution payload: Python receives it verbatim
/// at commit/upgrade time and never keeps an acceptable-plan ledger of its own.
/// </summary>
internal sealed record HostInstallPlanLease(
    PluginRuntimeInstallPlan Plan,
    HostInstallPlanBinding Binding,
    DownloadedPluginPackage? Package)
{
    public string PlanId => Plan.PlanId;

    public string PluginId => Plan.Manifest.PluginId;
}

internal sealed class HostInstallPlanOperation(
    HostInstallPlanLease plan,
    ProductAuthorityEpoch.ProductAuthorityOperationLease authority) : IAsyncDisposable
{
    private int _disposed;

    public HostInstallPlanLease Plan { get; } = plan;
    public ProductAuthorityEpoch.ProductAuthorityOperationLease Authority { get; } = authority;

    public ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0) return ValueTask.CompletedTask;
        try
        {
            // There is no backend plan to cancel: the consumed lease and its
            // download are host-owned resources and their disposal is local.
            Plan.Package?.Dispose();
        }
        finally
        {
            Authority.Dispose();
        }
        return ValueTask.CompletedTask;
    }
}

/// <summary>
/// Sole authority for install-plan admission, consumption, cancellation and
/// invalidation. Every gateway or context transition and every
/// admission/consumption is serialized by one lock; package disposal and
/// authority-epoch transitions are deliberately performed by the caller after
/// ownership has left the lock. A terminated Python client retires its plan
/// bindings immediately without touching the shared Go epoch.
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

    public HostInstallPlanLeaseRegistry(ProductAuthorityEpoch authority)
    {
        _authority = authority ?? throw new ArgumentNullException(nameof(authority));
    }

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

    /// <summary>
    /// Retires the plan bindings of one gateway whose transport terminated.
    /// The shared authority epoch is deliberately untouched: a dead Python
    /// client must not retire the Go epoch that outlives it. Disposal of the
    /// released downloads happens outside the lock.
    /// </summary>
    public IReadOnlyList<HostInstallPlanLease> RetireGateway(IPluginRpcGateway expected)
    {
        ArgumentNullException.ThrowIfNull(expected);
        lock (_gate)
        {
            List<HostInstallPlanLease> released = [];
            foreach (HostInstallPlanLease lease in _leases.Values)
            {
                if (ReferenceEquals(lease.Binding.Gateway, expected))
                    released.Add(lease);
            }
            foreach (HostInstallPlanLease lease in released)
            {
                _leases.Remove(lease.PlanId);
            }
            if (ReferenceEquals(_gateway, expected))
            {
                _gateway = null;
                _gatewayGeneration += 1;
            }
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
            _leases.Add(plan.PlanId, new HostInstallPlanLease(plan, binding, package));
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
            operation = new HostInstallPlanOperation(plan, authorityLease);
            return true;
        }
    }

    /// <summary>
    /// Starts an already-consumed plan's execution only after re-verifying its
    /// binding inside the registry lock, so a gateway retirement or context
    /// transition that happened after consumption cannot reach the gateway.
    /// Lock order is registry first, authority second, as everywhere else.
    /// </summary>
    public bool TryStartOperation(
        HostInstallPlanOperation operation,
        Func<CancellationToken, Task<PluginRuntimeSnapshot>> start,
        out Task<PluginRuntimeSnapshot>? pending)
    {
        ArgumentNullException.ThrowIfNull(operation);
        ArgumentNullException.ThrowIfNull(start);
        lock (_gate)
        {
            pending = null;
            if (!IsCurrentLocked(operation.Plan.Binding)) return false;
            return _authority.TryStart(operation.Authority, start, out pending);
        }
    }

    /// <summary>
    /// Projects an execution result only while its binding is still current:
    /// a late result from a retired gateway is rejected without retiring the
    /// shared authority epoch.
    /// </summary>
    public bool TryFinishOperation(HostInstallPlanOperation operation, Action terminal)
    {
        ArgumentNullException.ThrowIfNull(operation);
        ArgumentNullException.ThrowIfNull(terminal);
        lock (_gate)
        {
            if (!IsCurrentLocked(operation.Plan.Binding)) return false;
            return _authority.TryFinish(operation.Authority, terminal);
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
