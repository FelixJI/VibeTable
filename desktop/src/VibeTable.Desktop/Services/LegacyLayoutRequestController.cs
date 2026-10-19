using System.IO;
using System.Text;
using System.Text.Json;
using Microsoft.Win32;

namespace VibeTable.Desktop.Services;

/// <summary>Read-only preservation; neither renderer content nor paths are accepted.</summary>
internal sealed class LegacyLayoutRequestController
{
    internal const int MaxSnapshotBytes = 16 * 1024 * 1024;
    private readonly IWebReplySink _reply;
    private readonly WorkspaceSessionEnvelopeFilter _sessions;
    private readonly Func<JsonRpcProductDataGateway?> _gateway;
    private readonly Func<HostSessionFileBroker?> _files;
    private readonly Func<string?> _selectTarget;
    private readonly CorrelatedRequestRunner<LegacyLayoutRequestController> _runner;
    private readonly SemaphoreSlim _gate = new(1);

    internal LegacyLayoutRequestController(IWebReplySink reply,
        WorkspaceSessionEnvelopeFilter sessions, Func<JsonRpcProductDataGateway?> gateway,
        Func<HostSessionFileBroker?> files, Func<string?> selectTarget)
    {
        _reply = reply;
        _sessions = sessions;
        _gateway = gateway;
        _files = files;
        _selectTarget = selectTarget;
        _runner = new(reply, TimeSpan.FromMinutes(5), () => CancellationToken.None,
            new("旧布局服务不可用。", "BACKEND_UNAVAILABLE", "缺少请求标识。", "BAD_PAYLOAD",
                "请求正在处理。", "DUPLICATE_REQUEST", "已取消保全。", "CANCELLED",
                "保全请求超时。", "TIMEOUT",
                _ => new("旧布局保全失败，原定义未改变。", "LEGACY_PRESERVATION_FAILED"), (_, _) => { }));
        _runner.SetGateway(this);
    }

    internal static bool Handles(string type) => type is "legacyLayout.reportRequested"
        or "legacyLayout.exportRequested" or "legacyLayout.cancelRequested";

    internal Task DispatchAsync(RoutedWebRequest request)
    {
        if (request.Payload.ValueKind != JsonValueKind.Object
            || request.Payload.EnumerateObject().Any(property => property.Name !=
                (request.Type == "legacyLayout.cancelRequested" ? "targetRequestId" :
                    request.Type == "legacyLayout.reportRequested" ? "reason" : "suggestedName")))
        {
            _reply.PostOperationFailed(request.RequestId, "保全参数无效。", "BAD_PAYLOAD");
            return Task.CompletedTask;
        }
        if (request.Payload.EnumerateObject().Any(property => property.Value.ValueKind != JsonValueKind.String)
            || (request.Payload.TryGetProperty("reason", out var reason) && reason.GetString() is not ("detect" or "refresh"))
            || (request.Type == "legacyLayout.cancelRequested" && (!request.Payload.TryGetProperty("targetRequestId", out var cancelId) || string.IsNullOrWhiteSpace(cancelId.GetString()))))
        {
            _reply.PostOperationFailed(request.RequestId, "保全参数无效。", "BAD_PAYLOAD");
            return Task.CompletedTask;
        }
        if (request.Type == "legacyLayout.cancelRequested")
        {
            if (request.Scope is null || !_sessions.TryCapture(request.Scope, out var lease))
            {
                _reply.PostOperationFailed(request.RequestId, "工作区会话无效。", "BAD_WORKSPACE_SCOPE");
                return Task.CompletedTask;
            }
            using (lease)
                if (request.Payload.TryGetProperty("targetRequestId", out var target)
                    && target.ValueKind == JsonValueKind.String) _runner.Cancel(target.GetString()!);
            return Task.CompletedTask;
        }
        return _runner.RunAsync(request,
            request.Type == "legacyLayout.reportRequested" ? "legacyLayout.reportLoaded" : "legacyLayout.exportCompleted",
            (_, token) => ExecuteAsync(request.RequestId!, request.Type == "legacyLayout.exportRequested", token), _gate, _sessions);
    }

    private async Task<object> ExecuteAsync(string requestId, bool export, CancellationToken token)
    {
        JsonRpcProductDataGateway gateway = _gateway() ?? throw new InvalidOperationException("No workspace.");
        HostSessionFileBroker? broker = export ? _files() : null;
        JsonElement snapshot = await gateway.ReadLegacyLayoutSnapshotAsync(token).ConfigureAwait(true);
        byte[] bytes = Encoding.UTF8.GetBytes(snapshot.GetRawText());
        if (bytes.Length > MaxSnapshotBytes) throw new IOException("Snapshot exceeds preservation budget.");
        token.ThrowIfCancellationRequested();
        if (!export)
        {
            var diagnostics = new List<object>();
            foreach (string key in new[] { "interfaces", "dashboards", "panels" })
                foreach (JsonElement item in snapshot.GetProperty(key).EnumerateArray())
                    if (item.TryGetProperty("diagnostics", out var entries))
                        foreach (JsonElement diagnostic in entries.EnumerateArray())
                            diagnostics.Add(new { code = diagnostic.GetProperty("code").GetString(),
                                message = diagnostic.GetProperty("message").GetString(),
                                severity = diagnostic.GetProperty("severity").GetString(),
                                itemId = $"{key}/{item.GetProperty("logicalId").GetString()}",
                                path = diagnostic.TryGetProperty("path", out var path) ? path.GetString() : null });
            return new { interfaces = snapshot.GetProperty("interfaces").GetArrayLength(),
                dashboards = snapshot.GetProperty("dashboards").GetArrayLength(),
                panels = snapshot.GetProperty("panels").GetArrayLength(), diagnostics,
                collectedAt = snapshot.GetProperty("collectedAt").GetString() };
        }
        if (broker is null) throw new InvalidOperationException("No file broker.");
        string? pathName = _selectTarget();
        token.ThrowIfCancellationRequested();
        if (pathName is null) return new { cancelled = true, outputDisplayName = (string?)null };
        HostDownloadTarget target = await broker.OpenDownloadTargetAsync(pathName).ConfigureAwait(true);
        var finished = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        bool committed = false;
        try
        {
            if (!await broker.AttachDownloadTerminationAsync(target.Id, () => finished.Task).ConfigureAwait(true))
                throw new OperationCanceledException("Workspace retired.");
            using var linked = CancellationTokenSource.CreateLinkedTokenSource(token, broker.DownloadRetirement);
            try { await File.WriteAllBytesAsync(target.TemporaryPath, bytes, linked.Token).ConfigureAwait(true); }
            finally { finished.TrySetResult(); }
            linked.Token.ThrowIfCancellationRequested();
            await broker.CompleteDownloadAsync(target.Id, commit: true,
                commitRequest: apply => _runner.Commit(requestId, apply)).ConfigureAwait(true);
            committed = true;
            return new { cancelled = false, outputDisplayName = target.DisplayName };
        }
        finally
        {
            finished.TrySetResult();
            if (!committed) await broker.CompleteDownloadAsync(target.Id, commit: false).ConfigureAwait(true);
        }
    }
}

internal static class LegacyLayoutSaveDialog
{
    internal static string? Select(System.Windows.Window owner, string? controls)
    {
        if (controls is not null)
        {
            if (File.Exists(Path.Combine(controls, "legacy-layout-export-cancel.txt"))) return null;
            string control = Path.Combine(controls, "legacy-layout-export-target.txt");
            if (File.Exists(control))
            {
                string path = File.ReadAllText(control).Trim();
                if (!Path.IsPathFullyQualified(path)) throw new IOException("Absolute test output required.");
                return path;
            }
        }
        var dialog = new SaveFileDialog { FileName = "vibetable-legacy-layouts.json", DefaultExt = ".json",
            AddExtension = true, OverwritePrompt = true, Filter = "JSON 保全材料|*.json", Title = "保存旧布局保全材料" };
        return dialog.ShowDialog(owner) == true ? dialog.FileName : null;
    }
}
