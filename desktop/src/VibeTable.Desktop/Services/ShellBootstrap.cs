using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Starts the device-global shell without waiting for a workspace runtime.
/// </summary>
public sealed class ShellBootstrap
{
    private readonly object _gate = new();
    private readonly WorkspaceRegistry _registry;
    private readonly IWebViewBridge _webView;
    private Task<ShellBootstrapResult>? _startTask;

    public ShellBootstrap(
        WorkspaceRegistry registry,
        IWebViewBridge webView)
    {
        _registry = registry ?? throw new ArgumentNullException(nameof(registry));
        _webView = webView ?? throw new ArgumentNullException(nameof(webView));
    }

    /// <summary>
    /// Reads the device registry and navigates the shell exactly once. A
    /// corrupt registry is reported to the shell caller but does not prevent
    /// the renderer from loading, so repair and empty-state UI remain usable.
    /// </summary>
    public Task<ShellBootstrapResult> StartAsync(
        CancellationToken cancellationToken = default)
    {
        Task<ShellBootstrapResult> startTask;
        lock (_gate)
        {
            _startTask ??= StartCoreAsync();
            startTask = _startTask;
        }
        return cancellationToken.CanBeCanceled
            ? startTask.WaitAsync(cancellationToken)
            : startTask;
    }

    private async Task<ShellBootstrapResult> StartCoreAsync()
    {
        IReadOnlyList<WorkspaceRegistryEntryV2> workspaces = [];
        string? registryErrorCode = null;
        try
        {
            workspaces = _registry.List();
        }
        catch (WorkspaceRegistryException exception)
        {
            registryErrorCode = exception.Code;
        }

        await _webView.LoadAsync(CancellationToken.None).ConfigureAwait(false);
        return new ShellBootstrapResult(workspaces, registryErrorCode);
    }
}

public sealed record ShellBootstrapResult(
    IReadOnlyList<WorkspaceRegistryEntryV2> Workspaces,
    string? RegistryErrorCode)
{
    public bool RegistryAvailable => RegistryErrorCode is null;
}
