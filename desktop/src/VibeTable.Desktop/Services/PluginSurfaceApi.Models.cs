using System.Text.Json;
using System.Text.Json.Nodes;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

internal sealed partial class PluginSurfaceApi
{
    private int _modelConfiguring;
    private sealed record ConfigureModel(string Mode);
    private async Task<object?> HandleModelAsync(PluginSurfaceSession surface, string method, JsonElement parameters, CancellationToken token)
    {
        if (method is "model.read" or "model.status") return await CallAsync(surface, method, parameters, token);
        if (Interlocked.CompareExchange(ref _modelConfiguring, 1, 0) != 0) throw Error("plugin_concurrency_limit");
        try { return await ConfigureModelAsync(surface, parameters, token); }
        finally { Interlocked.Exchange(ref _modelConfiguring, 0); }
    }

    private async Task<object?> ConfigureModelAsync(PluginSurfaceSession surface, JsonElement parameters, CancellationToken token)
    {
        var request = parameters.Deserialize<ConfigureModel>(Json) ?? throw Error("plugin_capability_invalid");
        if (request.Mode is not ("create" or "map")) throw Error("plugin_capability_invalid");
        var generation = _generation() ?? throw Error("plugin_session_stale");
        JsonElement? mappings = null;
        if (request.Mode == "map")
        {
            Task<JsonElement> ReadCatalog(JsonElement page, CancellationToken ct) => CallAsync(surface, "model.catalog", page, ct);
            var catalog = await ReadModelCatalogAsync(ReadCatalog, token);
            mappings = await NativePluginModelWindow.ChooseMappingAsync(catalog,
                (table, revision, ct) => ReadModelCatalogAsync(ReadCatalog, ct, table, revision), token);
            if (mappings is null) return null;
        }
        var payload = mappings is null ? JsonSerializer.SerializeToElement(new { mode = request.Mode })
            : JsonSerializer.SerializeToElement(new { mode = request.Mode, mappings });
        var preview = await CallAsync(surface, "model.preview", payload, token);
        if (preview.GetProperty("status").GetString() == "completed")
        {
            await RefreshModelCatalogAsync(generation, token);
            return preview;
        }
        if (!await _confirmModel(preview, token)) return null;
        CurrentSurface(surface.SurfaceToken, surface.DocumentUri.AbsoluteUri);
        string requestId = preview.GetProperty("requestId").GetString()!;
        string taskId = $"plugin-task-{Guid.NewGuid():N}";
        var binding = _tasks.CaptureNative() ?? throw Error("plugin_session_stale");
        if (binding.Context != surface.Context) throw Error("plugin_session_stale");
        var snapshot = new PluginRuntimeTaskSnapshot(taskId, requestId, surface.PluginId, surface.PluginVersion, surface.ActionId, surface.ProjectKey,
            null, preview.GetProperty("total").GetInt32(), "write", "running", false, null, null, null, surface.InstallationRevision, surface.InstallationId, surface.SurfaceToken);
        var task = _tasks.NativeTask(binding, snapshot, nativeOnlyApproval: true);
        using var operation = CancellationTokenSource.CreateLinkedTokenSource(token, task.Token);
        var completion = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        _writes[requestId] = new(binding.Context, completion);
        try
        {
            _reply.PostNotification("plugin.task.changed", task.Envelope);
            var result = await CallAsync(surface, "model.commit", JsonSerializer.SerializeToElement(new { requestId }), operation.Token, commitId: requestId, modelPlan: preview);
            var finished = _tasks.FinishNative(taskId, "succeeded", new PluginRuntimeResult(PluginContractVersions.Result, "success", "数据模型配置完成", [], null, [], null, []));
            if (finished is not null) _reply.PostNotification("plugin.task.changed", finished);
            await RefreshModelCatalogAsync(generation, token);
            return result;
        }
        catch
        {
            var finished = _tasks.FinishNative(taskId, "failed", error: SafeError("plugin_model_status_required"));
            if (finished is not null) _reply.PostNotification("plugin.task.changed", finished);
            throw;
        }
        finally { _writes.TryRemove(requestId, out _); completion.TrySetResult(); }
    }

    internal static async Task<JsonElement> ReadModelCatalogAsync(Func<JsonElement, CancellationToken, Task<JsonElement>> read,
        CancellationToken token, string? tableId = null, string? schemaRevision = null)
    {
        var request = new JsonObject();
        string itemsKey = tableId is null ? "tables" : "fields";
        string nextKey = tableId is null ? "nextAfterTableId" : "nextOffset";
        string cursorKey = tableId is null ? "afterTableId" : "offset";
        if (tableId is not null) { request["tableId"] = tableId; request["schemaRevision"] = schemaRevision; }
        JsonObject? result = null;
        var items = new JsonArray();
        do
        {
            token.ThrowIfCancellationRequested();
            var page = await read(JsonSerializer.SerializeToElement(request), token);
            if (tableId is not null && (page.GetProperty("tableId").GetString() != tableId
                || page.GetProperty("schemaRevision").GetString() != schemaRevision)) throw Error("plugin_plan_stale");
            result ??= JsonNode.Parse(page.GetRawText())!.AsObject();
            foreach (var item in page.GetProperty(itemsKey).EnumerateArray()) items.Add(JsonNode.Parse(item.GetRawText()));
            var next = page.GetProperty(nextKey);
            if (next.ValueKind == JsonValueKind.Null) break;
            if (tableId is null ? string.CompareOrdinal(next.GetString(), request[cursorKey]?.GetValue<string>() ?? "") <= 0
                : next.GetInt32() <= (request[cursorKey]?.GetValue<int>() ?? 0)) throw Error("plugin_plan_stale");
            request[cursorKey] = JsonNode.Parse(next.GetRawText());
        } while (true);
        result[itemsKey] = items;
        result.Remove(nextKey);
        return JsonSerializer.SerializeToElement(result);
    }

    private static void ValidateModelReceipt(JsonElement receipt, PluginSurfaceSession surface, JsonElement request, JsonElement? plan)
    {
        if (plan is not { } expected || receipt.ValueKind != JsonValueKind.Object
            || !receipt.TryGetProperty("contract", out var contract) || contract.GetString() != "vibetable.plugin-model.v1"
            || !receipt.TryGetProperty("requestId", out var requestId) || requestId.GetString() != request.GetProperty("requestId").GetString()
            || !receipt.TryGetProperty("installationId", out var installation) || installation.GetString() != surface.InstallationId
            || !receipt.TryGetProperty("installationRevision", out var revision) || !revision.TryGetInt64(out long installedRevision) || installedRevision != surface.InstallationRevision
            || !receipt.TryGetProperty("status", out var status) || status.GetString() != "completed"
            || !receipt.TryGetProperty("total", out var total) || !total.TryGetInt32(out int count) || count < 0 || count != expected.GetProperty("total").GetInt32()
            || !receipt.TryGetProperty("completed", out var completed) || !completed.TryGetInt32(out int done) || done != count
            || !receipt.TryGetProperty("model", out var model) || !JsonNode.DeepEquals(JsonNode.Parse(model.GetRawText()), JsonNode.Parse(expected.GetProperty("model").GetRawText())))
            throw Error("plugin_model_status_required");
    }

    private async Task RefreshModelCatalogAsync(ProductSidecarGenerationSnapshot generation, CancellationToken token)
    {
        if (_refreshModelCatalog is null) return;
        try { await _refreshModelCatalog(generation, token); }
        catch (Exception error)
        {
            // Catalog projection cannot downgrade a committed model; its owner reports scoped failures.
            System.Diagnostics.Trace.TraceWarning("plugin_model.catalog_refresh_failed:" + error.GetType().Name);
        }
    }
}
