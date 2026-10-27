using System.Text.Json;
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
            var catalog = await CallAsync(surface, "model.catalog", JsonSerializer.SerializeToElement(new { }), token);
            mappings = await NativePluginModelWindow.ChooseMappingAsync(catalog, token);
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
        if (!await NativePluginModelWindow.ConfirmAsync(preview, token)) return null;
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
            var result = await CallAsync(surface, "model.commit", JsonSerializer.SerializeToElement(new { requestId }), operation.Token, commitId: requestId);
            if (result.GetProperty("status").GetString() != "completed") throw Error("plugin_model_partial");
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
