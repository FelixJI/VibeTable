using System.IO;
using System.Text;
using System.Text.Json;
using Microsoft.Win32;

namespace VibeTable.Desktop.Services;

/// <summary>Read-only preservation; neither renderer content nor paths are accepted.</summary>
internal sealed class LegacyLayoutRequestController
{
    internal const int PageBytes = 256 * 1024;
    internal const int DiagnosticPreviewLimit = 100;
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
                    request.Type == "legacyLayout.reportRequested" ? "reason" : null)))
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
        if (!export) return await ReadPagesAsync(gateway, null, token).ConfigureAwait(true);
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
            try
            {
                await using var output = new FileStream(target.TemporaryPath, FileMode.Create,
                    FileAccess.Write, FileShare.None, 64 * 1024, useAsync: true);
                await ReadPagesAsync(gateway, output, linked.Token).ConfigureAwait(true);
                await output.FlushAsync(linked.Token).ConfigureAwait(true);
            }
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
    private static async Task<object> ReadPagesAsync(JsonRpcProductDataGateway gateway,
        Stream? output, CancellationToken token)
    {
        string[] collections = ["interfaces", "dashboards", "panels"];
        long[] counts = [0, 0, 0];
        long diagnosticCount = 0;
        bool previewTruncated = false;
        var diagnostics = new List<object>();
        string? collectedAt = null;
        async Task Write(string value)
        {
            if (output is not null) await output.WriteAsync(Encoding.UTF8.GetBytes(value), token).ConfigureAwait(true);
        }
        await Write("{\"contractVersion\":\"1.0\"");
        for (int index = 0; index < collections.Length; index++)
        {
            string collection = collections[index], after = "", currentId = "", revision = "";
            int offset = 0;
            await Write(",\"" + collection + "\":[");
            while (true)
            {
                token.ThrowIfCancellationRequested();
                JsonElement page = await gateway.ReadLegacyLayoutSnapshotAsync(collection, after, offset, token).ConfigureAwait(true);
                token.ThrowIfCancellationRequested();
                if (page.GetProperty("contractVersion").GetString() != "2.0"
                    || page.GetProperty("collection").GetString() != collection)
                    throw new IOException("Invalid preservation page.");
                collectedAt ??= page.GetProperty("collectedAt").GetString();
                byte[] chunk = page.GetProperty("chunk").GetBytesFromBase64();
                string recordId = page.GetProperty("recordId").GetString() ?? "";
                string nextAfter = page.GetProperty("nextAfterRecordId").GetString() ?? "";
                int nextOffset = page.GetProperty("nextRecordOffset").GetInt32();
                bool done = page.GetProperty("recordDone").GetBoolean();
                if (page.GetProperty("collectionDone").GetBoolean())
                {
                    if (offset != 0 || chunk.Length != 0 || recordId != "" || nextAfter != after || nextOffset != 0 || done)
                        throw new IOException("Invalid terminal preservation cursor.");
                    break;
                }
                string pageRevision = page.GetProperty("revision").GetString() ?? "";
                if (recordId.Length != 15 || !recordId.All(char.IsAsciiLetterOrDigit)
                    || string.CompareOrdinal(recordId, after) <= 0 || chunk.Length is < 1 or > PageBytes
                    || (offset != 0 && (recordId != currentId || pageRevision != revision))
                    || (done ? nextAfter != recordId || nextOffset != 0
                        : nextAfter != after || chunk.Length != PageBytes || nextOffset != checked(offset + chunk.Length))
                    || nextOffset > 1 << 30)
                    throw new IOException("Non-progressing preservation cursor.");
                if (offset == 0)
                {
                    if (counts[index] != 0) await Write(",");
                    counts[index] = checked(counts[index] + 1);
                    currentId = recordId;
                    revision = pageRevision;
                    int count = page.GetProperty("diagnosticCount").GetInt32();
                    if (count < 0) throw new IOException("Invalid diagnostic count.");
                    diagnosticCount = checked(diagnosticCount + count);
                    previewTruncated |= page.GetProperty("previewTruncated").GetBoolean();
                    foreach (JsonElement diagnostic in page.GetProperty("diagnostics").EnumerateArray())
                    {
                        if (diagnostics.Count >= DiagnosticPreviewLimit) break;
                        diagnostics.Add(new { code = diagnostic.GetProperty("code").GetString(),
                            message = diagnostic.GetProperty("message").GetString(),
                            severity = diagnostic.GetProperty("severity").GetString(),
                            itemId = $"{collection}/{page.GetProperty("logicalId").GetString()}",
                            path = diagnostic.TryGetProperty("path", out var path) ? path.GetString() : null });
                    }
                }
                if (output is not null) await output.WriteAsync(chunk, token).ConfigureAwait(true);
                after = nextAfter;
                offset = nextOffset;
            }
            await Write("]");
        }
        await Write(",\"collectedAt\":" + JsonSerializer.Serialize(collectedAt) + "}");
        return new { interfaces = counts[0], dashboards = counts[1], panels = counts[2], diagnostics,
            diagnosticCount, diagnosticsTruncated = previewTruncated || diagnosticCount > diagnostics.Count, collectedAt };
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
