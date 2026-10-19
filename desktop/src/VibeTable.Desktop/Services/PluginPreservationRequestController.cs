using System.IO;
using System.IO.Compression;
using System.Text.Json;
using Microsoft.Win32;

namespace VibeTable.Desktop.Services;

/// <summary>Host-owned archival export; neither source paths nor file contents come from the renderer.</summary>
internal sealed class PluginPreservationRequestController
{
    private static readonly TimeSpan Deadline = TimeSpan.FromMinutes(5);
    private readonly IWebReplySink _reply;
    private readonly WorkspaceSessionEnvelopeFilter _sessions;
    private readonly Func<JsonRpcProductDataGateway?> _gateway;
    private readonly Func<HostSessionFileBroker?> _files;
    private readonly Func<PluginProjectContext?> _context;
    private readonly Func<string, string?> _cacheRoot;
    private readonly Func<string?> _selectTarget;
    private readonly SemaphoreSlim _gate = new(1);
    private readonly CorrelatedRequestRunner<PluginPreservationRequestController> _runner;

    internal PluginPreservationRequestController(IWebReplySink reply,
        WorkspaceSessionEnvelopeFilter sessions, Func<JsonRpcProductDataGateway?> gateway,
        Func<HostSessionFileBroker?> files, Func<PluginProjectContext?> context,
        Func<string, string?> cacheRoot, Func<string?> selectTarget)
    {
        _reply = reply;
        _sessions = sessions;
        _gateway = gateway;
        _files = files;
        _context = context;
        _cacheRoot = cacheRoot;
        _selectTarget = selectTarget;
        _runner = new(reply, Deadline, () => CancellationToken.None,
            new("插件保全服务不可用。", "BACKEND_UNAVAILABLE", "缺少请求标识。", "BAD_PAYLOAD",
                "请求正在处理。", "DUPLICATE_REQUEST", "已取消保全。", "CANCELLED",
                "保全请求超时。", "TIMEOUT",
                _ => new("插件保全失败，原包与设置未改变。", "PLUGIN_PRESERVATION_FAILED"), (_, _) => { }));
        _runner.SetGateway(this);
    }

    internal static bool Handles(string type) => type is "pluginPreservation.exportRequested"
        or "pluginPreservation.cancelRequested";

    internal Task DispatchAsync(RoutedWebRequest request)
    {
        if (request.Payload.ValueKind != JsonValueKind.Object)
            return Reject(request);
        if (request.Type == "pluginPreservation.cancelRequested")
        {
            if (request.Payload.EnumerateObject().Count() != 1
                || !request.Payload.TryGetProperty("targetRequestId", out var target)
                || target.ValueKind != JsonValueKind.String || string.IsNullOrWhiteSpace(target.GetString()))
                return Reject(request);
            if (request.Scope is null || !_sessions.TryCapture(request.Scope, out var lease))
                return Reject(request);
            using (lease) _runner.Cancel(target.GetString()!);
            return Task.CompletedTask;
        }
        if (request.Payload.EnumerateObject().Count() != 3
            || !request.Payload.TryGetProperty("pluginId", out var plugin)
            || plugin.ValueKind != JsonValueKind.String || string.IsNullOrWhiteSpace(plugin.GetString())
            || !request.Payload.TryGetProperty("installationId", out var installation)
            || installation.ValueKind != JsonValueKind.String || string.IsNullOrWhiteSpace(installation.GetString())
            || !request.Payload.TryGetProperty("installationRevision", out var revision)
            || revision.ValueKind != JsonValueKind.Number || !revision.TryGetInt32(out int expectedRevision) || expectedRevision < 1)
            return Reject(request);
        return _runner.RunAsync(request, "pluginPreservation.exportCompleted",
            (_, token) => ExportAsync(request.RequestId!, plugin.GetString()!, installation.GetString()!, expectedRevision, token),
            _gate, _sessions);
    }

    private Task Reject(RoutedWebRequest request)
    {
        _reply.PostOperationFailed(request.RequestId, "插件保全参数或工作区无效。", "BAD_PAYLOAD");
        return Task.CompletedTask;
    }

    private async Task<object> ExportAsync(string requestId, string pluginId, string installationId,
        int revision, CancellationToken token)
    {
        var context = _context() ?? throw new IOException("No workspace context.");
        var gateway = _gateway() ?? throw new IOException("No workspace gateway.");
        var broker = _files() ?? throw new IOException("No file broker.");
        string? cache = _cacheRoot(context.ProjectKey);
        string? path = _selectTarget();
        token.ThrowIfCancellationRequested();
        if (path is null) return new { cancelled = true, outputDisplayName = (string?)null, missingPackages = 0 };
        HostDownloadTarget target = await broker.OpenDownloadTargetAsync(path, Deadline).ConfigureAwait(true);
        var finished = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        bool committed = false;
        try
        {
            if (!await broker.AttachDownloadTerminationAsync(target.Id, () => finished.Task).ConfigureAwait(true))
                throw new OperationCanceledException("Workspace retired.");
            using var linked = CancellationTokenSource.CreateLinkedTokenSource(token, broker.DownloadRetirement);
            int missing;
            try
            {
                await using var output = new FileStream(target.TemporaryPath, FileMode.Create, FileAccess.Write,
                    FileShare.None, 64 * 1024, useAsync: true);
                missing = await WriteArchiveAsync(output,
                    (after, offset, ct) => gateway.ReadPluginPreservationAsync(context.ProjectKey, pluginId,
                        installationId, revision, after, offset, ct), cache, linked.Token).ConfigureAwait(true);
                await output.FlushAsync(linked.Token).ConfigureAwait(true);
            }
            finally { finished.TrySetResult(); }
            linked.Token.ThrowIfCancellationRequested();
            if (_context() != context) throw new OperationCanceledException("Workspace changed.");
            await broker.CompleteDownloadAsync(target.Id, commit: true,
                commitRequest: apply => _runner.Commit(requestId, apply)).ConfigureAwait(true);
            committed = true;
            return new { cancelled = false, outputDisplayName = target.DisplayName, missingPackages = missing };
        }
        finally
        {
            finished.TrySetResult();
            if (!committed) await broker.CompleteDownloadAsync(target.Id, commit: false).ConfigureAwait(true);
        }
    }

    internal static async Task<int> WriteArchiveAsync(Stream output,
        Func<string, int, CancellationToken, Task<JsonElement>> readPage, string? cacheRoot, CancellationToken token)
    {
        using var archive = new ZipArchive(output, ZipArchiveMode.Create, leaveOpen: true);
        var packages = new HashSet<string>(StringComparer.Ordinal);
        var diagnostics = new List<string>();
        await using (Stream records = archive.CreateEntry("records.json").Open())
        {
            await records.WriteAsync("["u8.ToArray(), token);
            string after = "", current = "", revision = "";
            int offset = 0;
            bool first = true;
            using var record = new MemoryStream();
            while (true)
            {
                token.ThrowIfCancellationRequested();
                JsonElement page = await readPage(after, offset, token).ConfigureAwait(true);
                token.ThrowIfCancellationRequested();
                if (page.GetProperty("contractVersion").GetString() != "1.0") throw new IOException("Invalid preservation contract.");
                byte[] chunk = page.GetProperty("chunk").GetBytesFromBase64();
                string id = page.GetProperty("recordId").GetString() ?? "";
                string next = page.GetProperty("nextAfterRecordId").GetString() ?? "";
                int nextOffset = page.GetProperty("nextRecordOffset").GetInt32();
                bool done = page.GetProperty("recordDone").GetBoolean();
                if (page.GetProperty("collectionDone").GetBoolean())
                {
                    if (offset != 0 || chunk.Length != 0 || id != "" || next != after || nextOffset != 0 || done)
                        throw new IOException("Invalid terminal cursor.");
                    break;
                }
                string pageRevision = page.GetProperty("revision").GetString() ?? "";
                if (id.Length != 15 || !id.All(char.IsAsciiLetterOrDigit) || string.CompareOrdinal(id, after) <= 0
                    || chunk.Length is < 1 or > LegacyLayoutRequestController.PageBytes
                    || (offset != 0 && (current != id || revision != pageRevision))
                    || (done ? next != id || nextOffset != 0 : next != after
                        || chunk.Length != LegacyLayoutRequestController.PageBytes || nextOffset != checked(offset + chunk.Length)))
                    throw new IOException("Invalid or changed preservation record.");
                if (offset == 0) { current = id; revision = pageRevision; record.SetLength(0); record.Position = 0; }
                // Stored payloads are capped at 4 MiB; JSON string escaping can expand that sixfold.
                if (record.Length + chunk.Length > 32 * 1024 * 1024) throw new IOException("Invalid oversized stored record.");
                record.Write(chunk);
                if (done)
                {
                    using var document = JsonDocument.Parse(record.ToArray());
                    JsonElement value = document.RootElement;
                    if (value.GetProperty("recordId").GetString() != id) throw new IOException("Record identity mismatch.");
                    string raw = value.GetProperty("rawPayload").GetString() ?? throw new IOException("Missing original payload.");
                    if (value.GetProperty("kind").GetString() is "installation" or "revision")
                    {
                        try
                        {
                            using var payload = JsonDocument.Parse(raw);
                            if (payload.RootElement.TryGetProperty("packageHash", out var hash)
                                && hash.ValueKind == JsonValueKind.String && PluginRetainedPackage.PathFor(cacheRoot, hash.GetString()!) is not null)
                                packages.Add(hash.GetString()!);
                            else diagnostics.Add($"{id}: package identity unavailable; original payload preserved.");
                        }
                        catch (JsonException) { diagnostics.Add($"{id}: invalid original JSON preserved."); }
                    }
                    if (!first) await records.WriteAsync(","u8.ToArray(), token);
                    record.Position = 0;
                    await record.CopyToAsync(records, token);
                    first = false;
                }
                after = next;
                offset = nextOffset;
            }
            await records.WriteAsync("]"u8.ToArray(), token);
        }
        int missing = diagnostics.Count;
        foreach (string hash in packages.Order(StringComparer.Ordinal))
        {
            token.ThrowIfCancellationRequested();
            string path = PluginRetainedPackage.PathFor(cacheRoot, hash)!;
            if (!File.Exists(path))
            {
                missing++;
                diagnostics.Add($"{hash}: original package is not available on this device.");
                continue;
            }
            if ((File.GetAttributes(path) & FileAttributes.ReparsePoint) != 0
                || (File.GetAttributes(Path.GetDirectoryName(path)!) & FileAttributes.ReparsePoint) != 0)
                throw new IOException("Package cache must not resolve through a link.");
            await using var source = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read,
                64 * 1024, useAsync: true);
            await using Stream entry = archive.CreateEntry($"packages/{hash[7..]}.vtplugin", CompressionLevel.NoCompression).Open();
            await source.CopyToAsync(entry, token);
        }
        await using Stream manifest = archive.CreateEntry("preservation.json").Open();
        await JsonSerializer.SerializeAsync(manifest, new { contractVersion = "1.0", missingPackages = missing, diagnostics }, cancellationToken: token);
        return missing;
    }
}

internal static class PluginPreservationSaveDialog
{
    internal static string? Select(System.Windows.Window owner, string? controls)
    {
        if (controls is not null)
        {
            if (File.Exists(Path.Combine(controls, "plugin-preservation-cancel.txt"))) return null;
            string control = Path.Combine(controls, "plugin-preservation-target.txt");
            if (File.Exists(control))
            {
                string path = File.ReadAllText(control).Trim();
                if (!Path.IsPathFullyQualified(path)) throw new IOException("Absolute test output required.");
                return path;
            }
        }
        var dialog = new SaveFileDialog { FileName = "vibetable-plugin-preservation.zip", DefaultExt = ".zip",
            AddExtension = true, OverwritePrompt = true, Filter = "插件保全材料|*.zip", Title = "保存插件原包与设置" };
        return dialog.ShowDialog(owner) == true ? dialog.FileName : null;
    }
}
