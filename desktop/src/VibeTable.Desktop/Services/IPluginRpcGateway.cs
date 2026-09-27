using System;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

// Private-to-the-host execution payloads for the install lifecycle. The
// public renderer DTOs stay frozen in VibeTable.Contracts; only the dispatcher
// constructs these from an already-consumed host lease, so the renderer can
// never forge a full plan. They are public only because the closed gateway
// interface is public; no renderer-reachable surface exposes them.
public sealed record PluginInspectInstallExecutionParams(
    string ProjectKey,
    string ProjectRevision,
    string SourceLocation,
    string PlanId);
public sealed record PluginCommitInstallExecutionParams(
    string ProjectKey,
    PluginRuntimeInstallPlan Plan,
    string ProjectRevision);
public sealed record PluginUpgradeExecutionParams(
    string ProjectKey,
    string PluginId,
    PluginRuntimeInstallPlan Plan,
    string ProjectRevision);

/// <summary>
/// Aggregate WPF boundary for complete plugin use cases. The interface is
/// intentionally closed: callers cannot provide a Python method name.
/// </summary>
public interface IPluginRpcGateway : IDisposable
{
    event Action<PluginEventEnvelope>? TaskChanged;
    event Action<PluginEventEnvelope>? InteractionRequested;
    event Action<PluginEventEnvelope>? FileRequested;

    /// <summary>Raised once when the backing client transport terminates.</summary>
    event Action? Terminated;

    /// <summary>Inspects a host-selected source under a host-generated plan id.</summary>
    Task<PluginRuntimeInstallPlan> InspectInstallAsync(
        PluginInspectInstallExecutionParams request, CancellationToken token);

    /// <summary>Commits an install from the full plan consumed out of the host lease.</summary>
    Task<PluginRuntimeSnapshot> CommitInstallAsync(
        PluginCommitInstallExecutionParams request, CancellationToken token);

    /// <summary>Upgrades a plugin from the full plan consumed out of the host lease.</summary>
    Task<PluginRuntimeSnapshot> UpgradeAsync(
        PluginUpgradeExecutionParams request, CancellationToken token);

    // plugin.cancelInstall has no gateway entry: cancellation is host-owned
    // and is answered by taking and disposing the install-plan lease.
    Task<PluginRuntimeSnapshot> RollbackAsync(
        PluginRollbackParams request, CancellationToken token);
    Task<PluginRuntimeUninstallResult> UninstallAsync(
        PluginUninstallParams request, CancellationToken token);
    Task<PluginRuntimeActionAvailability> DescribeActionAsync(
        PluginDescribeActionParams request, CancellationToken token);
    Task<PluginRuntimeTaskSnapshot> StartActionAsync(
        PluginStartActionParams request, CancellationToken token);
    Task<PluginRuntimeInteractionResolveResult> ResolveInteractionAsync(
        PluginResolveInteractionParams request, CancellationToken token);
    Task<bool> ResolveFileAsync(
        PluginRuntimeFileRequest request, string? selectedPath, CancellationToken token);
    Task RevokeRunFileGrantsAsync(string runId) => Task.CompletedTask;
    Task<bool> CancelTaskAsync(
        PluginTaskParams request, CancellationToken token);
}
