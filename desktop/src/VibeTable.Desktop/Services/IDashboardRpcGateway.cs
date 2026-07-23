using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>Closed, typed JSON-RPC boundary for the native dashboard surface.</summary>
public interface IDashboardRpcGateway
{
    Task<DashboardsResult> ListDashboardsAsync(CancellationToken token);
    Task<DashboardWorkspaceResult> ReadDashboardWorkspaceAsync(
        string dashboardId, CancellationToken token);
    Task<SaveDashboardDraftResult> SaveDashboardDraftAsync(
        SaveDashboardDraftParams parameters, CancellationToken token);
    Task<DeleteDashboardResult> DeleteDashboardAsync(
        string dashboardId, CancellationToken token);
    Task<DashboardQueryResult> ExecuteDashboardQueryAsync(
        ExecuteDashboardQueryParams parameters, CancellationToken token);
    Task<DashboardQueryLimits> GetDashboardQueryLimitsAsync(CancellationToken token);
    Task<PanelManifestResult> GetPanelManifestAsync(CancellationToken token);
}
