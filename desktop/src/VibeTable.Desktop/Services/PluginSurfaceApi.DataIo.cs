using System.Collections.Concurrent;
using System.Text.Json;
using System.Text.Json.Nodes;

namespace VibeTable.Desktop.Services;

internal sealed partial class PluginSurfaceApi
{
    // New plugin DataIO policy; ordinary plugin files keep their separate 1 MiB budget.
    private const long PluginDataIoBytes = 32L * 1024 * 1024;
    private readonly Func<PluginDataIoPort>? _dataIo;
    private readonly ConcurrentDictionary<string, ConcurrentDictionary<string, IoTask>> _ioTasks = new();
    private sealed class IoTask(PluginDataIoPort port, string id, string kind, string collection,
        SurfaceFile file, PluginSurfaceSession surface, ProductSidecarGenerationSnapshot generation)
    {
        internal PluginDataIoPort Port { get; } = port;
        internal string Id { get; } = id;
        internal string Kind { get; } = kind;
        internal string Collection { get; } = collection;
        internal SurfaceFile File { get; } = file;
        internal PluginSurfaceSession Surface { get; } = surface;
        internal ProductSidecarGenerationSnapshot Generation { get; } = generation;
        internal SemaphoreSlim Gate { get; } = new(1);
        internal JsonElement? Terminal { get; set; }
        internal int CancellationStarted;
        internal TaskCompletionSource<bool> CancelAck { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);
    }
    private sealed record ImportSource(SurfaceFile File, string Token, long Attempt, string Reservation, PluginSurfaceSession Surface, ProductSidecarGenerationSnapshot Generation);

    private async Task<object?> HandleDataIoAsync(PluginSurfaceSession surface, SurfaceState state,
        string method, JsonElement parameters, CancellationToken token)
    {
        if (_dataIo is null || _pickFile is null || _fileHost is null) throw Error("plugin_api_unsupported");
        var tasks = SurfaceMap(surface, state, _ioTasks);
        if (method is "data.io.status" or "data.io.cancel")
        {
            string id = ReadHandle(parameters, "taskId");
            if (!tasks.TryGetValue(id, out var task)) throw Error("plugin_source_denied");
            if (method == "data.io.cancel")
            {
                string phase = task.Port.Status(id).GetProperty("state").GetString()!;
                if (phase is "succeeded" or "failed" or "cancelled" or "aborted") return new { cancelRequested = false };
                EnsureIoCancellation(task);
                return new { cancelRequested = await task.CancelAck.Task.WaitAsync(token) };
            }
            await task.Gate.WaitAsync(token);
            try
            {
                if (task.Terminal is { } terminal) return terminal;
                var status = task.Port.Status(id);
                string phase = status.GetProperty("state").GetString()!;
                if (phase is not ("succeeded" or "failed" or "cancelled" or "aborted")) return SafeIoStatus(status);
                if (phase == "succeeded" && task.Kind == "data.previewImport")
                {
                    JsonElement preview = status.GetProperty("result");
                    if (preview.GetProperty("summary").GetProperty("errorRows").GetInt32() != 0)
                    {
                        task.Terminal = JsonSerializer.SerializeToElement(new
                        {
                            taskId = id,
                            kind = task.Kind,
                            state = phase,
                            progress = status.GetProperty("progress"),
                            result = new
                            {
                                planId = (string?)null,
                                affectedCount = 0,
                                summary = preview.GetProperty("summary"),
                                rows = ImportSampleRows(preview)
                            }
                        }, Json);
                        await RevokeSurfaceFileAsync(task.File);
                        await DropImportGrantAsync(surface, task.Generation, task.File.GrantId);
                        return task.Terminal.Value;
                    }
                    string importToken = preview.GetProperty("token").GetProperty("token").GetString()!;
                    if (state.Plans.Count >= 8) throw Error("plugin_plan_limit");
                    JsonElement frozen = await CallAsync(surface, "import.freeze", JsonSerializer.SerializeToElement(new
                    { token = importToken, grantId = task.File.GrantId, collection = task.Collection }), token);
                    var reservation = await task.File.Broker.HandleAsync("reservePluginImport", JsonSerializer.SerializeToElement(new
                    { grantId = task.File.GrantId, runId = task.File.RunId, token = importToken }), token);
                    JsonElement plan = frozen.GetProperty("plan").Clone();
                    string planId = Guid.NewGuid().ToString("N");
                    var source = new ImportSource(task.File, importToken, frozen.GetProperty("attempt").GetInt64(), reservation.GetProperty("reservationId").GetString()!, surface, task.Generation);
                    if (!_surfaces.TryUseCurrent(surface, () => !state.Lifetime.IsCancellationRequested && state.Plans.Count < 8
                        && state.Plans.TryAdd(planId, new Plan(plan, plan.GetProperty("operations").GetArrayLength(), DateTimeOffset.UtcNow.AddMinutes(2), source))))
                    { ReleaseImportSource(source); throw Error("plugin_session_stale"); }
                    task.Terminal = JsonSerializer.SerializeToElement(new
                    {
                        taskId = id,
                        kind = task.Kind,
                        state = phase,
                        progress = status.GetProperty("progress"),
                        result = new { planId, affectedCount = plan.GetProperty("operations").GetArrayLength(), summary = preview.GetProperty("summary"), rows = ImportSampleRows(preview) }
                    }, Json);
                }
                else
                {
                    task.Terminal = SafeIoStatus(status);
                    await RevokeSurfaceFileAsync(task.File);
                    if (task.Kind == "data.previewImport") await DropImportGrantAsync(surface, task.Generation, task.File.GrantId);
                }
                return task.Terminal.Value;
            }
            catch { await RevokeSurfaceFileAsync(task.File); throw; }
            finally { task.Gate.Release(); }
        }
        bool export = method == "data.export.start";
        if (export) ExactFile(parameters, "collection", "fields", "query", "format", "suggestedName");
        else ExactFile(parameters, "collection", "schemaRevision", "mode", "upsertKey", "columnMapping");
        string collection = parameters.GetProperty("collection").GetString() ?? throw Error("plugin_capability_invalid");
        if (collection.Length is 0 or > 128) throw Error("plugin_capability_invalid");
        string format = export ? parameters.GetProperty("format").GetString()! : "";
        if (export && format is not ("csv" or "xlsx")) throw Error("plugin_capability_invalid");
        string suggested = export ? SafeFileName(parameters.GetProperty("suggestedName").GetString()) : "";
        JsonElement fields = export ? parameters.GetProperty("fields").Clone() : JsonSerializer.SerializeToElement(Array.Empty<string>());
        async Task Authorize(CancellationToken call)
        {
            await AuthorizeFileAsync(surface, export, call);
            await CallAsync(surface, "data.ioProfile", JsonSerializer.SerializeToElement(new
            { collection, operation = export ? "read" : "create", fields, mode = export ? "" : parameters.GetProperty("mode").GetString() }), call);
        }
        await Authorize(token);
        foreach (var old in tasks.Where(pair => pair.Value.Terminal is not null).ToArray())
            if (tasks.Count >= 8) tasks.TryRemove(old.Key, out _);
        if (tasks.Count >= 8) throw Error("plugin_concurrency_limit");
        var generation = _generation() ?? throw Error("plugin_session_stale");
        PluginDataIoPort port = _dataIo();
        var owned = SurfaceMap(surface, state, _surfaceFiles);
        if (owned.Count >= 8) throw Error("plugin_file_limit");
        string? path = await _pickFile(() => export ? _fileHost.SelectExportTarget(format, suggested) : _fileHost.SelectImportSource());
        if (path is null) return null;
        token.ThrowIfCancellationRequested();
        string runId = "plugin-io-" + Guid.NewGuid().ToString("N");
        var descriptor = await port.Files.IssueAsync(path, export, runId, token, ownerCommit: action =>
            generation.TryUseCurrent(() => _surfaces.TryUseCurrent(surface, () =>
            { if (state.Lifetime.IsCancellationRequested) return false; action(); return true; })),
            purpose: export ? "export" : "import", byteLimit: PluginDataIoBytes, authorize: Authorize);
        string grantId = descriptor.GetProperty("grantId").GetString()!;
        var file = new SurfaceFile(port.Files, runId, grantId, export, surface.SurfaceToken);
        if (!generation.TryUseCurrent(() => _surfaces.TryUseCurrent(surface, () =>
            !state.Lifetime.IsCancellationRequested && owned.Count < 8 && owned.TryAdd(grantId, file))))
        { await port.Files.RevokeRunAsync(runId); throw Error("plugin_session_stale"); }
        var request = JsonNode.Parse(parameters.GetRawText())!.AsObject();
        request.Remove("fields"); request.Remove("suggestedName");
        request["grantId"] = grantId;
        var principal = SurfacePrincipal(surface);
        principal["requestId"] = runId;
        request["pluginScope"] = new JsonObject
        {
            ["principal"] = principal,
            ["runId"] = runId,
            ["fields"] = JsonNode.Parse(fields.GetRawText()),
            ["query"] = export ? request["query"]?.DeepClone() : new JsonObject()
        };
        if (export) request["query"] = new JsonObject();
        string kind = export ? "data.export" : "data.previewImport";
        try
        {
            JsonElement taskStatus = await port.Start(kind, JsonSerializer.SerializeToElement(request), token);
            string id = taskStatus.GetProperty("taskId").GetString()!;
            var task = new IoTask(port, id, kind, collection, file, surface, generation);
            if (!_surfaces.TryUseCurrent(surface, () => !state.Lifetime.IsCancellationRequested && tasks.Count < 8 && tasks.TryAdd(id, task)))
            { await port.Cancel(id, CancellationToken.None); throw Error("plugin_session_stale"); }
            return new { taskId = id };
        }
        catch { owned.TryRemove(grantId, out _); await port.Files.RevokeRunAsync(runId); throw; }
    }

    private static IReadOnlyList<JsonElement> ImportSampleRows(JsonElement preview)
    {
        var rows = new List<JsonElement>(); int bytes = 0;
        foreach (var row in preview.GetProperty("rows").EnumerateArray())
        {
            int size = System.Text.Encoding.UTF8.GetByteCount(row.GetRawText());
            if (rows.Count >= 20 || bytes + size > 128 * 1024) break;
            rows.Add(row.Clone()); bytes += size;
        }
        return rows;
    }
    private static JsonElement SafeIoStatus(JsonElement status)
    {
        var safe = JsonNode.Parse(status.GetRawText())!.AsObject();
        // Worker errors may contain internal plan/grant details; expose only a stable status code.
        if (safe["error"] is not null) safe["error"] = JsonSerializer.SerializeToNode(new { code = "plugin_data_io_failed", message = "插件数据任务未完成，请核对任务。" });
        if (safe["kind"]?.GetValue<string>() == "data.export" && safe["state"]?.GetValue<string>() is "failed" or "aborted")
            safe["outcome"] = "unknown";
        return JsonSerializer.SerializeToElement(safe);
    }
    private void ReleaseDataIo(string surfaceToken)
    {
        if (!_ioTasks.TryRemove(surfaceToken, out var tasks)) return;
        foreach (var task in tasks.Values) EnsureIoCancellation(task);
    }
    private void EnsureIoCancellation(IoTask task)
    {
        if (Interlocked.CompareExchange(ref task.CancellationStarted, 1, 0) == 0) TrackCleanup(CancelIoAsync(task));
    }
    private async Task CancelIoAsync(IoTask task)
    {
        try
        {
            using var budget = new CancellationTokenSource(TimeSpan.FromSeconds(5));
            try { await task.Port.Cancel(task.Id, budget.Token); }
            finally { await RevokeSurfaceFileAsync(task.File); }
            task.CancelAck.TrySetResult(true);
            await task.Port.WaitTerminal(task.Id, budget.Token);
            if (task.Kind == "data.previewImport") await DropImportGrantAsync(task.Surface, task.Generation, task.File.GrantId);
        }
        catch (Exception error)
        { task.CancelAck.TrySetException(error); System.Diagnostics.Trace.TraceWarning($"Plugin DataIO cancellation failed: {error}"); }
    }
    private void ReleaseImportSource(ImportSource? source)
    { if (source is not null) TrackCleanup(ReleaseImportSourceAsync(source)); }
    private async Task ReleaseImportSourceAsync(ImportSource source)
    {
        await RevokeSurfaceFileAsync(source.File);
        await DropImportGrantAsync(source.Surface, source.Generation, source.File.GrantId);
    }
    private async Task DropImportGrantAsync(PluginSurfaceSession surface, ProductSidecarGenerationSnapshot generation, string grantId)
    {
        try
        {
            using var budget = new CancellationTokenSource(TimeSpan.FromSeconds(5));
            await CallAsync(surface, "import.drop", JsonSerializer.SerializeToElement(new { grantId }), budget.Token, auditGeneration: generation);
        }
        catch (Exception error) { System.Diagnostics.Trace.TraceWarning($"Plugin import cleanup unavailable: {error}"); }
    }
    private async Task SettleImportSourceAsync(PluginSurfaceSession surface, ImportSource? source, string outcome)
    {
        if (source is null) return;
        try
        {
            using var budget = new CancellationTokenSource(TimeSpan.FromSeconds(5));
            if (outcome != "unknown")
            {
                await source.File.Broker.HandleAsync("settleImport", JsonSerializer.SerializeToElement(new
                { reservationId = source.Reservation, outcome = outcome == "succeeded" ? "consumed" : "released" }), budget.Token);
                await CallAsync(surface, "import.settle", JsonSerializer.SerializeToElement(new
                {
                    contract = "vibetable.import-plans.v1",
                    token = source.Token,
                    attempt = source.Attempt,
                    outcome = outcome == "succeeded" ? "committed" : "rejected"
                }), budget.Token, auditGeneration: source.Generation);
            }
        }
        catch (Exception error) { System.Diagnostics.Trace.TraceWarning($"Plugin import settlement unavailable: {error}"); }
        finally { await RevokeSurfaceFileAsync(source.File); }
    }
    private static JsonObject SurfacePrincipal(PluginSurfaceSession surface) => new()
    {
        ["projectKey"] = surface.ProjectKey,
        ["pluginId"] = surface.PluginId,
        ["actionId"] = surface.ActionId,
        ["entry"] = surface.Entry,
        ["packageHash"] = surface.Revision.PackageHash,
        ["sessionEpoch"] = surface.Context!.SessionGeneration,
        ["installationRevision"] = surface.InstallationRevision,
        ["installationId"] = surface.InstallationId,
        ["surfaceToken"] = surface.SurfaceToken,
        ["activeCollection"] = surface.ActiveCollection,
    };
}
