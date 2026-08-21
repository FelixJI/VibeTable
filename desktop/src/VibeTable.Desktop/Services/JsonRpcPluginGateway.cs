using System;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>Fixed plugin-use-case adapter over the supervisor-owned pipe.</summary>
public sealed class JsonRpcPluginGateway : IPluginRpcGateway
{
    private static readonly JsonSerializerOptions JsonOptions =
        new(JsonSerializerDefaults.Web);

    private readonly JsonRpcClient _client;
    private bool _disposed;

    public JsonRpcPluginGateway(JsonRpcClient client)
    {
        _client = client ?? throw new ArgumentNullException(nameof(client));
        _client.NotificationReceived += OnNotificationReceived;
    }

    public event Action<PluginEventEnvelope>? CatalogChanged;
    public event Action<PluginEventEnvelope>? TaskChanged;
    public event Action<PluginEventEnvelope>? InteractionRequested;
    public event Action<PluginEventEnvelope>? FileRequested;

    public Task<PluginRuntimeSnapshot[]> ListCatalogAsync(
        PluginCatalogListParams request, CancellationToken token)
        => InvokeAsync<PluginCatalogListParams, PluginRuntimeSnapshot[]>(
            "plugin.listCatalog", request, token);

    public Task<PluginRuntimeAuditEvent[]> ListAuditAsync(
        PluginAuditListParams request, CancellationToken token)
        => InvokeAsync<PluginAuditListParams, PluginRuntimeAuditEvent[]>(
            "plugin.listAudit", request, token);

    public Task<PluginRuntimeAuditEvent[]> ListPendingCleanupAsync(
        PluginCatalogListParams request, CancellationToken token)
        => InvokeAsync<PluginCatalogListParams, PluginRuntimeAuditEvent[]>(
            "plugin.listPendingCleanup", request, token);

    public Task<PluginRuntimeInstallPlan> InspectInstallAsync(
        PluginInspectInstallParams request, CancellationToken token)
        => InvokeAsync<PluginInspectInstallParams, PluginRuntimeInstallPlan>(
            "plugin.inspectInstall", request, token);

    public Task<PluginRuntimeSnapshot> CommitInstallAsync(
        PluginCommitInstallParams request, CancellationToken token)
        => InvokeAsync<PluginCommitInstallParams, PluginRuntimeSnapshot>(
            "plugin.commitInstall", request, token);

    public Task<bool> CancelInstallAsync(
        PluginInstallCancelParams request, CancellationToken token)
        => InvokeAsync<PluginInstallCancelParams, bool>(
            "plugin.cancelInstall", request, token);

    public Task<PluginRuntimeSnapshot> SetEnabledAsync(
        PluginSetEnabledParams request, CancellationToken token)
        => InvokeAsync<PluginSetEnabledParams, PluginRuntimeSnapshot>(
            "plugin.setEnabled", request, token);

    public Task<PluginRuntimeSnapshot> UpgradeAsync(
        PluginUpgradeParams request, CancellationToken token)
        => InvokeAsync<PluginUpgradeParams, PluginRuntimeSnapshot>(
            "plugin.upgrade", request, token);

    public Task<PluginRuntimeSnapshot> RollbackAsync(
        PluginRollbackParams request, CancellationToken token)
        => InvokeAsync<PluginRollbackParams, PluginRuntimeSnapshot>(
            "plugin.rollback", request, token);

    public Task<PluginRuntimeUninstallResult> UninstallAsync(
        PluginUninstallParams request, CancellationToken token)
        => InvokeAsync<PluginUninstallParams, PluginRuntimeUninstallResult>(
            "plugin.uninstall", request, token);

    public Task<PluginRuntimeActionAvailability> DescribeActionAsync(
        PluginDescribeActionParams request, CancellationToken token)
        => InvokeAsync<PluginDescribeActionParams, PluginRuntimeActionAvailability>(
            "plugin.describeAction", request, token);

    public Task<PluginRuntimeTaskSnapshot> StartActionAsync(
        PluginStartActionParams request, CancellationToken token)
        => InvokeAsync<PluginStartActionParams, PluginRuntimeTaskSnapshot>(
            "plugin.startAction", request, token);

    public Task<PluginRuntimeInteractionResolveResult> ResolveInteractionAsync(
        PluginResolveInteractionParams request, CancellationToken token)
        => InvokeAsync<PluginResolveInteractionParams, PluginRuntimeInteractionResolveResult>(
            "plugin.resolveInteraction", request, token);

    public Task<bool> ResolveFileAsync(
        PluginResolveFileParams request, CancellationToken token)
        => InvokeAsync<PluginResolveFileParams, bool>(
            "plugin.resolveFile", request, token);

    public Task<PluginRuntimeTaskSnapshot> CancelTaskAsync(
        PluginTaskParams request, CancellationToken token)
        => InvokeAsync<PluginTaskParams, PluginRuntimeTaskSnapshot>(
            "plugin.cancelTask", request, token);

    public Task<PluginRuntimeTaskSnapshot> GetTaskAsync(
        PluginTaskParams request, CancellationToken token)
        => InvokeAsync<PluginTaskParams, PluginRuntimeTaskSnapshot>(
            "plugin.getTask", request, token);

    public void Dispose()
    {
        if (_disposed)
        {
            return;
        }
        _disposed = true;
        _client.NotificationReceived -= OnNotificationReceived;
    }

    private Task<TResult> InvokeAsync<TParams, TResult>(
        string method, TParams request, CancellationToken token)
        where TParams : notnull
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        return _client.InvokeAsync<TParams, TResult>(method, request, token);
    }

    private void OnNotificationReceived(string method, JsonElement parameters)
    {
        if (_disposed)
        {
            return;
        }

        string? expectedEventType = method switch
        {
            "plugin.catalogChanged" => "plugin.catalog.changed",
            "plugin.taskChanged" => "plugin.task.changed",
            "plugin.interactionRequested" => "plugin.interaction.requested",
            "plugin.fileRequested" => "plugin.file.requested",
            _ => null,
        };
        if (expectedEventType is null)
        {
            return;
        }

        PluginEventEnvelope? envelope;
        try
        {
            envelope = parameters.Deserialize<PluginEventEnvelope>(JsonOptions);
        }
        catch (JsonException)
        {
            return;
        }
        if (envelope is null
            || !string.Equals(envelope.Contract, PluginContractVersions.Event, StringComparison.Ordinal))
        {
            return;
        }
        if (!string.Equals(envelope.EventType, expectedEventType, StringComparison.Ordinal)
            || string.IsNullOrWhiteSpace(envelope.ProjectKey)
            || string.IsNullOrWhiteSpace(envelope.EntityId)
            || envelope.Revision < 1)
        {
            return;
        }

        switch (method)
        {
            case "plugin.catalogChanged":
                CatalogChanged?.Invoke(envelope);
                break;
            case "plugin.taskChanged":
                TaskChanged?.Invoke(envelope);
                break;
            case "plugin.interactionRequested":
                InteractionRequested?.Invoke(envelope);
                break;
            case "plugin.fileRequested":
                FileRequested?.Invoke(envelope);
                break;
        }
    }
}
