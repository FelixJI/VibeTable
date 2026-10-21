using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal sealed record HostSourceImportOpenResult(bool Cancelled, string? TaskId);

internal interface IHostSourceImportActions
{
    Task<HostSourceImportOpenResult> OpenAsync(
        string provider, Action ensureCurrent, CancellationToken token);
}

/// <summary>
/// Opens the trusted native source wizard for one current workspace. The web
/// request carries a closed provider choice, never credentials or source data.
/// </summary>
internal sealed class HostSourceImportRequestController(
    IWebReplySink reply, WorkspaceSessionEnvelopeFilter sessions,
    IHostSourceImportActions actions)
{
    /// <summary>Wire identity of the only formal source wizard: the local
    /// Feishu .base file picker. Deliberately distinct from the retired
    /// online "feishu"/"wps" providers so a stale renderer's cloud request
    /// can never be misrouted to a file dialog.</summary>
    internal const string BaseFileProvider = "feishuBase";

    private static readonly string[] RetiredCloudProviders = ["feishu", "wps"];

    private int _opening;

    internal static bool Handles(string type) => type == "sourceImport.open";

    internal async Task DispatchAsync(RoutedWebRequest request)
    {
        if (!Handles(request.Type) || request.Payload.ValueKind != JsonValueKind.Object
            || request.Payload.EnumerateObject().Count() != 1
            || !request.Payload.TryGetProperty("provider", out JsonElement value)
            || value.ValueKind != JsonValueKind.String)
        {
            reply.PostOperationFailed(request.RequestId,
                "Invalid source import request.", "SOURCE_IMPORT_BAD_PAYLOAD");
            return;
        }
        string provider = value.GetString()!;
        if (RetiredCloudProviders.Contains(provider))
        {
            // Online Feishu/WPS reads are retired; only the local .base file
            // wizard exists. Reject explicitly instead of routing a cloud
            // request into a file dialog or vice versa.
            reply.PostOperationFailed(request.RequestId,
                "云服务来源导入已停用，请改用 .base 本地文件导入。", "SOURCE_IMPORT_PROVIDER_RETIRED");
            return;
        }
        if (provider != BaseFileProvider)
        {
            reply.PostOperationFailed(request.RequestId,
                "Invalid source import request.", "SOURCE_IMPORT_BAD_PAYLOAD");
            return;
        }
        if (request.Scope is not { } scope || !sessions.TryCapture(scope, out var lease))
        {
            reply.PostOperationFailed(request.RequestId,
                "Source import requires the current workspace.", "BAD_WORKSPACE_SCOPE");
            return;
        }
        using (lease)
        {
            if (Interlocked.CompareExchange(ref _opening, 1, 0) != 0)
            {
                reply.PostOperationFailed(request.RequestId,
                    "A source import window is already open.", "SOURCE_IMPORT_BUSY");
                return;
            }
            void EnsureCurrent()
            {
                lease!.CancellationToken.ThrowIfCancellationRequested();
                if (!sessions.IsCurrent(lease)) throw new OperationCanceledException();
            }
            try
            {
                EnsureCurrent();
                HostSourceImportOpenResult result = await actions.OpenAsync(
                    provider, EnsureCurrent, lease!.CancellationToken);
                EnsureCurrent();
                if (result.Cancelled != (result.TaskId is null)
                    || result.TaskId is { Length: 0 })
                    throw new InvalidOperationException("Invalid native import outcome.");
                reply.PostResponse(request.Type, request.RequestId, result);
            }
            catch (OperationCanceledException)
            {
                if (sessions.IsCurrent(lease))
                    reply.PostResponse(request.Type, request.RequestId,
                        new HostSourceImportOpenResult(true, null));
            }
            catch (Exception)
            {
                // Provider errors may contain credentials or temporary URLs.
                // The native wizard owns safe diagnostics; no raw text crosses
                // this response boundary or enters a trace.
                if (sessions.IsCurrent(lease))
                    reply.PostOperationFailed(request.RequestId,
                        "Source import could not be opened.", "SOURCE_IMPORT_UNAVAILABLE");
            }
            finally
            {
                Volatile.Write(ref _opening, 0);
            }
        }
    }
}

internal sealed class HostSourceImportActions(
    Func<string, Action, CancellationToken, Task<HostSourceImportOpenResult>> open)
    : IHostSourceImportActions
{
    public Task<HostSourceImportOpenResult> OpenAsync(
        string provider, Action ensureCurrent, CancellationToken token)
        => open(provider, ensureCurrent, token);
}
