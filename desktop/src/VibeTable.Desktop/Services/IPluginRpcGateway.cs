using System;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Aggregate WPF boundary for complete plugin use cases. The interface is
/// intentionally closed: callers cannot provide a Python method name.
/// </summary>
public interface IPluginRpcGateway : IDisposable
{
    event Action<PluginEventEnvelope>? CatalogChanged;
    event Action<PluginEventEnvelope>? TaskChanged;
    event Action<PluginEventEnvelope>? InteractionRequested;
    event Action<PluginEventEnvelope>? FileRequested;

    Task<PluginRuntimeSnapshot[]> ListCatalogAsync(
        PluginCatalogListParams request, CancellationToken token);
    Task<PluginRuntimeAuditEvent[]> ListAuditAsync(
        PluginAuditListParams request, CancellationToken token);
    Task<PluginRuntimeAuditEvent[]> ListPendingCleanupAsync(
        PluginCatalogListParams request, CancellationToken token);
    Task<PluginRuntimeInstallPlan> InspectInstallAsync(
        PluginInspectInstallParams request, CancellationToken token);
    Task<PluginRuntimeSnapshot> CommitInstallAsync(
        PluginCommitInstallParams request, CancellationToken token);
    Task<PluginRuntimeExternalFlowCandidate[]> ListExternalFlowCandidatesAsync(
        PluginListExternalFlowCandidatesParams request, CancellationToken token);
    Task<PluginRuntimeFlowBindingSnapshot> BindExternalFlowAsync(
        PluginBindExternalFlowParams request, CancellationToken token);
    Task<PluginRuntimeSnapshot> SetEnabledAsync(
        PluginSetEnabledParams request, CancellationToken token);
    Task<PluginRuntimeSnapshot> UpgradeAsync(
        PluginUpgradeParams request, CancellationToken token);
    Task<PluginRuntimeSnapshot> RollbackAsync(
        PluginRollbackParams request, CancellationToken token);
    Task<PluginRuntimeSnapshot> ResolveDriftAsync(
        PluginResolveDriftParams request, CancellationToken token);
    Task<PluginRuntimeUninstallResult> UninstallAsync(
        PluginUninstallParams request, CancellationToken token);
    Task<PluginRuntimeActionAvailability> DescribeActionAsync(
        PluginDescribeActionParams request, CancellationToken token);
    Task<PluginRuntimeTaskSnapshot> StartActionAsync(
        PluginStartActionParams request, CancellationToken token);
    Task<PluginRuntimeInteractionResolveResult> ResolveInteractionAsync(
        PluginResolveInteractionParams request, CancellationToken token);
    Task<bool> ResolveFileAsync(
        PluginResolveFileParams request, CancellationToken token);
    Task<PluginRuntimeTaskSnapshot> CancelTaskAsync(
        PluginTaskParams request, CancellationToken token);
    Task<PluginRuntimeTaskSnapshot> GetTaskAsync(
        PluginTaskParams request, CancellationToken token);
}
