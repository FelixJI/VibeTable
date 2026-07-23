using System;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>Dashboard adapter over the supervisor-owned local JSON-RPC pipe.</summary>
public sealed class JsonRpcDashboardGateway : IDashboardRpcGateway
{
    private readonly JsonRpcClient _client;

    public JsonRpcDashboardGateway(JsonRpcClient client)
        => _client = client ?? throw new ArgumentNullException(nameof(client));

    public Task<DashboardsResult> ListDashboardsAsync(CancellationToken token)
        => _client.InvokeAsync<ListDashboardsParams, DashboardsResult>(
            "directus.listDashboards", new(), token);

    public Task<DashboardWorkspaceResult> ReadDashboardWorkspaceAsync(
        string dashboardId, CancellationToken token)
        => _client.InvokeAsync<DashboardWorkspaceParams, DashboardWorkspaceResult>(
            "directus.readDashboardWorkspace", new(dashboardId), token);

    public Task<SaveDashboardDraftResult> SaveDashboardDraftAsync(
        SaveDashboardDraftParams parameters, CancellationToken token)
        => _client.InvokeAsync<SaveDashboardDraftParams, SaveDashboardDraftResult>(
            "directus.saveDashboardDraft", parameters, token);

    public Task<DeleteDashboardResult> DeleteDashboardAsync(
        string dashboardId, CancellationToken token)
        => _client.InvokeAsync<DashboardWorkspaceParams, DeleteDashboardResult>(
            "directus.deleteDashboardWorkspace", new(dashboardId), token);

    public Task<DashboardQueryResult> ExecuteDashboardQueryAsync(
        ExecuteDashboardQueryParams parameters, CancellationToken token)
        => _client.InvokeAsync<ExecuteDashboardQueryParams, DashboardQueryResult>(
            "directus.executeDashboardQuery", parameters, token);

    public Task<DashboardQueryLimits> GetDashboardQueryLimitsAsync(CancellationToken token)
        => _client.InvokeAsync<DirectusEmptyParams, DashboardQueryLimits>(
            "directus.dashboardQueryLimits", new(), token);

    public Task<PanelManifestResult> GetPanelManifestAsync(CancellationToken token)
        => _client.InvokeAsync<DirectusEmptyParams, PanelManifestResult>(
            "directus.panelManifest", new(), token);
}
