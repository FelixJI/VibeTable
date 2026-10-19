using System;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>Fixed plugin-use-case adapter over the supervisor-owned pipe.</summary>
public sealed class JsonRpcPluginGateway : IPluginRpcGateway
{
    private readonly JsonRpcClient _client;
    private bool _disposed;

    public JsonRpcPluginGateway(JsonRpcClient client)
    {
        _client = client ?? throw new ArgumentNullException(nameof(client));
    }

    /// <inheritdoc />
    public event Action? Terminated
    {
        add => _client.Terminated += value;
        remove => _client.Terminated -= value;
    }

    public Task<PluginRuntimeInstallPlan> InspectInstallAsync(
        PluginInspectInstallExecutionParams request, CancellationToken token)
        => InvokeAsync<PluginInspectInstallExecutionParams, PluginRuntimeInstallPlan>(
            "plugin.inspectInstall", request, token);

    public Task<PluginRuntimeSnapshot> CommitInstallAsync(
        PluginCommitInstallExecutionParams request, CancellationToken token)
        => InvokeAsync<PluginCommitInstallExecutionParams, PluginRuntimeSnapshot>(
            "plugin.commitInstall", request, token);

    public Task<PluginRuntimeSnapshot> UpgradeAsync(
        PluginUpgradeExecutionParams request, CancellationToken token)
        => InvokeAsync<PluginUpgradeExecutionParams, PluginRuntimeSnapshot>(
            "plugin.upgrade", request, token);

    public Task<PluginRuntimeSnapshot> SetEnabledExecutionAsync(PluginSetEnabledExecutionParams request, CancellationToken token)
        => InvokeAsync<PluginSetEnabledExecutionParams, PluginRuntimeSnapshot>("plugin.setEnabledExecution", request, token);

    public Task<PluginRuntimeSnapshot> RollbackAsync(
        PluginRollbackParams request, CancellationToken token)
        => InvokeAsync<PluginRollbackParams, PluginRuntimeSnapshot>(
            "plugin.rollback", request, token);

    public Task<PluginRuntimeUninstallResult> UninstallAsync(
        PluginUninstallParams request, CancellationToken token)
        => InvokeAsync<PluginUninstallParams, PluginRuntimeUninstallResult>(
            "plugin.uninstall", request, token);

    public void Dispose()
    {
        if (_disposed)
        {
            return;
        }
        _disposed = true;
    }

    private Task<TResult> InvokeAsync<TParams, TResult>(
        string method, TParams request, CancellationToken token)
        where TParams : notnull
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        return _client.InvokeAsync<TParams, TResult>(method, request, token);
    }

}
