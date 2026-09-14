using System;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;


namespace VibeTable.Desktop.Services;

/// <summary>Dashboard adapter through the shared Host Product generation.</summary>
public sealed class JsonRpcDashboardGateway : IDashboardRpcGateway
{
    private readonly JsonRpcProductDataGateway _product;

    public JsonRpcDashboardGateway(JsonRpcProductDataGateway product)
        => _product = product ?? throw new ArgumentNullException(nameof(product));

    public Task<DashboardsResult> ListDashboardsAsync(CancellationToken token)
        => _product.InvokeDashboardAsync<ListDashboardsParams, DashboardsResult>(
            "insights.listDashboards", new(), token);

    public Task<DashboardWorkspaceResult> ReadDashboardWorkspaceAsync(
        string dashboardId, CancellationToken token)
        => _product.InvokeDashboardAsync<DashboardWorkspaceParams, DashboardWorkspaceResult>(
            "insights.readDashboardWorkspace", new(dashboardId), token);

    public Task<SaveDashboardDraftResult> SaveDashboardDraftAsync(
        SaveDashboardDraftParams parameters, CancellationToken token)
        => _product.InvokeDashboardAsync<SaveDashboardDraftParams, SaveDashboardDraftResult>(
            "insights.saveDashboardDraft", parameters, token);

    public Task<DeleteDashboardResult> DeleteDashboardAsync(
        string dashboardId, CancellationToken token)
        => _product.InvokeDashboardAsync<DashboardWorkspaceParams, DeleteDashboardResult>(
            "insights.deleteDashboardWorkspace", new(dashboardId), token);

    public Task<DashboardQueryResult> ExecuteDashboardQueryAsync(
        ExecuteDashboardQueryParams parameters, CancellationToken token)
        => _product.InvokeDashboardAsync<ExecuteDashboardQueryParams, DashboardQueryResult>(
            "insights.executeDashboardQuery", parameters, token);

    public Task<DashboardQueryLimits> GetDashboardQueryLimitsAsync(CancellationToken token)
        => _product.InvokeDashboardAsync<InsightsEmptyParams, DashboardQueryLimits>(
            "insights.dashboardQueryLimits", new(), token);

    public Task<PanelManifestResult> GetPanelManifestAsync(CancellationToken token)
        => _product.InvokeDashboardAsync<InsightsEmptyParams, PanelManifestResult>(
            "insights.panelManifest", new(), token);
}
