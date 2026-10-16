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
        // Volatile flag: ownership of the file grant and import plan moved to a minted
        // plan; cancellation cleanup must then neither revoke nor drop them.
        internal int Transferred;
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
                if (phase is not ("succeeded" or "failed" or "cancelled" or "aborted"))
                {
                    EnsureIoCancellation(task);
                    return new { cancelRequested = await task.CancelAck.Task.WaitAsync(token) };
                }
                // The worker already reached a terminal state by itself. Settle through
                // the same Gate-protected path as status so the preview plan handoff or
                // drop runs and the file grant and task slot are released now; a racing
                // status or cancel only replays the recorded terminal, never a second handoff.
                await SettleIoTaskAsync(surface, state, task, token);
                return new { cancelRequested = false };
            }
            return await SettleIoTaskAsync(surface, state, task, token);
        }
        bool export = method == "data.export.start";
        if (export) ExactFile(parameters, "collection", "fields", "query", "format", "suggestedName");
        else ExactFile(parameters, "collection", "schemaRevision", "mode", "upsertKey", "columnMapping");
        string collection = parameters.GetProperty("collection").GetString() ?? throw Error("plugin_capability_invalid");
        if (collection.Length is 0 or > 128) throw Error("plugin_capability_invalid");
        string format = export ? parameters.GetProperty("format").GetString()! : "";
        if (export && format is not ("csv" or "xlsx")) throw Error("plugin_capability_invalid");
        string importMode = export ? "" : parameters.GetProperty("mode").GetString() ?? "";
        if (!export && importMode is not ("create_only" or "upsert")) throw Error("plugin_capability_invalid");
        if (importMode == "upsert")
        {
            // Upsert matches rows by an approved key before preview starts; a null or empty
            // key would otherwise be silently normalized as updates until the authoritative
            // mint rejects the whole batch.
            JsonElement upsertKey = parameters.GetProperty("upsertKey");
            if (upsertKey.ValueKind != JsonValueKind.String || upsertKey.GetString()!.Length is 0 or > 128)
                throw Error("plugin_capability_invalid");
        }
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

    /// <summary>
    /// The single Gate-protected settlement shared by data.io.status and data.io.cancel:
    /// the first caller to observe a worker terminal performs the preview plan handoff
    /// (or drop) and releases the file grant and task slot; later callers only replay
    /// the recorded terminal, so concurrent status/cancel cannot duplicate the handoff.
    /// </summary>
    private async Task<JsonElement> SettleIoTaskAsync(PluginSurfaceSession surface, SurfaceState state,
        IoTask task, CancellationToken token)
    {
        await task.Gate.WaitAsync(token);
        try
        {
            if (task.Terminal is { } terminal) return terminal;
            var status = task.Port.Status(task.Id);
            string phase = status.GetProperty("state").GetString()!;
            if (phase is not ("succeeded" or "failed" or "cancelled" or "aborted")) return SafeIoStatus(status);
            bool transferred = false;
            ImportSource? reservedSource = null;
            try
            {
                if (phase == "succeeded" && task.Kind == "data.previewImport")
                {
                    JsonElement preview = status.GetProperty("result");
                    if (preview.GetProperty("summary").GetProperty("errorRows").GetInt32() != 0)
                    {
                        task.Terminal = JsonSerializer.SerializeToElement(new
                        {
                            taskId = task.Id,
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
                    }
                    else
                    {
                        // The finished preview already minted a Go import plan for this grant;
                        // every failure below must still settle this task so its task slot,
                        // file grant and minted plan are released now instead of wedging the
                        // surface until the 10-minute plan TTL expires.
                        string importToken = preview.GetProperty("token").GetProperty("token").GetString()!;
                        if (state.Plans.Count >= 8) throw Error("plugin_plan_limit");
                        JsonElement frozen = await CallAsync(surface, "import.freeze", JsonSerializer.SerializeToElement(new
                        { token = importToken, grantId = task.File.GrantId, collection = task.Collection }), token);
                        JsonElement plan = frozen.GetProperty("plan").Clone();
                        long attempt = frozen.GetProperty("attempt").GetInt64();
                        var reservation = await task.File.Broker.HandleAsync("reservePluginImport", JsonSerializer.SerializeToElement(new
                        { grantId = task.File.GrantId, runId = task.File.RunId, token = importToken }), token);
                        string planId = Guid.NewGuid().ToString("N");
                        // This request owns the reservation until the plan handoff succeeds.
                        reservedSource = new ImportSource(task.File, importToken, attempt, reservation.GetProperty("reservationId").GetString()!, surface, task.Generation);
                        JsonElement receipt = JsonSerializer.SerializeToElement(new
                        {
                            taskId = task.Id,
                            kind = task.Kind,
                            state = phase,
                            progress = status.GetProperty("progress"),
                            result = new { planId, affectedCount = plan.GetProperty("operations").GetArrayLength(), summary = preview.GetProperty("summary"), rows = ImportSampleRows(preview) }
                        }, Json);
                        if (!_surfaces.TryUseCurrent(surface, () => !state.Lifetime.IsCancellationRequested && state.Plans.Count < 8
                            && state.Plans.TryAdd(planId, new Plan(plan, plan.GetProperty("operations").GetArrayLength(), DateTimeOffset.UtcNow.AddMinutes(2), reservedSource))))
                            throw Error("plugin_session_stale");
                        task.Terminal = receipt;
                        // Ownership of the file grant and import plan moved to the plan;
                        // an in-flight port cancellation must not revoke or drop them.
                        Volatile.Write(ref task.Transferred, 1);
                        transferred = true;
                    }
                }
                else task.Terminal = SafeIoStatus(status);
            }
            catch (Exception error) when (task.Terminal is null)
            { task.Terminal = FailedIoTerminal(task, status, error, state.Plans.Count); }
            if (!transferred)
            {
                if (reservedSource is not null) await SettleImportSourceAsync(surface, reservedSource, "rejected");
                else await RevokeSurfaceFileAsync(task.File);
                if (task.Kind == "data.previewImport") await DropImportGrantAsync(surface, task.Generation, task.File.GrantId);
            }
            return task.Terminal.Value;
        }
        // Ownership may already have moved to a minted plan; never revoke that grant twice.
        catch when (Volatile.Read(ref task.Transferred) == 0)
        { await RevokeSurfaceFileAsync(task.File); throw; }
        finally { task.Gate.Release(); }
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
        // A failed/aborted export stays "unknown" unless the worker attached its
        // positive not-committed receipt (the failure provably preceded the native
        // finishWrite(commit:true) submission). A missing receipt must never default
        // to not-committed; the internal receipt itself never crosses to plugins.
        string? receipt = (safe["commitReceipt"] as JsonValue)?.GetValue<string>();
        safe.Remove("commitReceipt");
        if (safe["kind"]?.GetValue<string>() == "data.export" && safe["state"]?.GetValue<string>() is "failed" or "aborted"
            && receipt != "not_committed")
            safe["outcome"] = "unknown";
        else safe.Remove("outcome");
        return JsonSerializer.SerializeToElement(safe);
    }
    private static JsonElement FailedIoTerminal(IoTask task, JsonElement status, Exception error, int pendingPlans)
    {
        // A finished preview whose plan handoff failed can never be committed from
        // this task; expose only a stable terminal code, never internal details.
        string code = error is PluginSurfacePolicyException { Code: "plugin_plan_limit" } || pendingPlans >= 8
            ? "plugin_plan_limit" : "plugin_data_io_failed";
        object progress = status.TryGetProperty("progress", out JsonElement current) ? current : new { done = 0, total = 0, message = "" };
        return JsonSerializer.SerializeToElement(new
        {
            taskId = task.Id,
            kind = task.Kind,
            state = "failed",
            progress,
            error = new { code, message = "插件数据任务未完成，请核对任务。" }
        }, Json);
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
        // Set when the Gate stayed contended past the whole 5s budget: the owed
        // ownership decision is then completed in the finally, unbudgeted but still
        // Gate-protected, because the settlement always releases the Gate in its own
        // finally. Cleanup is never skipped and a completed handoff is never revoked.
        Exception? owedOwnership = null;
        Exception? failure = null;
        try
        {
            using var budget = new CancellationTokenSource(TimeSpan.FromSeconds(5));
            try { await task.Port.Cancel(task.Id, budget.Token); }
            catch (Exception error) { failure = error; }
            // The revoke ownership decision runs under the settlement's Gate and inside
            // the same caller-visible 5s budget: a legitimate settlement holding the
            // Gate for its freeze round trips surfaces here as a timely cancellation
            // failure instead of stalling the acknowledgement far past its budget.
            try { await CancelIoOwnershipAsync(task, revoke: true, budget.Token); }
            catch (OperationCanceledException) when (budget.IsCancellationRequested)
            {
                owedOwnership = new OperationCanceledException(
                    "Plugin DataIO cancellation budget expired while the settlement held the Gate.");
                failure ??= owedOwnership;
            }
            if (failure is not null) throw failure;
            task.CancelAck.TrySetResult(true);
            await task.Port.WaitTerminal(task.Id, budget.Token);
            if (task.Kind == "data.previewImport") await CancelIoOwnershipAsync(task, revoke: false, CancellationToken.None);
        }
        catch (Exception error)
        { task.CancelAck.TrySetException(error); System.Diagnostics.Trace.TraceWarning($"Plugin DataIO cancellation failed: {error}"); }
        finally
        {
            if (owedOwnership is not null)
            {
                try
                {
                    // Complete the remaining cancellation path once the Gate frees up:
                    // the revoke (or the skip after a completed handoff), then the
                    // terminal wait and the Go grant drop with their own bounded budget.
                    await CancelIoOwnershipAsync(task, revoke: true, CancellationToken.None);
                    using var cleanupBudget = new CancellationTokenSource(TimeSpan.FromSeconds(5));
                    await task.Port.WaitTerminal(task.Id, cleanupBudget.Token);
                    if (task.Kind == "data.previewImport") await CancelIoOwnershipAsync(task, revoke: false, CancellationToken.None);
                }
                catch (Exception error) { System.Diagnostics.Trace.TraceWarning($"Plugin DataIO cancellation cleanup failed: {error}"); }
            }
        }
    }

    /// <summary>
    /// One Gate-protected ownership decision for a cancelled task: the revoke/drop either
    /// precedes any handoff (a later settlement then fails its freeze/reserve against the
    /// dead grant) or observes the completed handoff and keeps the plan's grant alive.
    /// The token bounds only the Gate wait; once held, the decision always runs to
    /// completion, so a cancelled wait never leaves ownership half-decided.
    /// </summary>
    private async Task CancelIoOwnershipAsync(IoTask task, bool revoke, CancellationToken token)
    {
        await task.Gate.WaitAsync(token);
        try
        {
            if (Volatile.Read(ref task.Transferred) != 0) return;
            if (revoke) await RevokeSurfaceFileAsync(task.File);
            else await DropImportGrantAsync(task.Surface, task.Generation, task.File.GrantId);
        }
        finally { task.Gate.Release(); }
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
            if (outcome != "unknown")
            {
                // The host reservation and the captured-generation Go claim settle
                // independently, each with its own bounded budget: a broker that
                // consumes its whole budget must not hand the Go settlement an
                // already-cancelled token. Worst case is now the sum of both
                // budgets (10s) instead of one shared 5s; revoke is unchanged.
                try
                {
                    using var reservationBudget = new CancellationTokenSource(TimeSpan.FromSeconds(5));
                    await source.File.Broker.HandleAsync("settleImport", JsonSerializer.SerializeToElement(new
                    { reservationId = source.Reservation, outcome = outcome == "succeeded" ? "consumed" : "released" }), reservationBudget.Token);
                }
                catch (Exception reservationError) { System.Diagnostics.Trace.TraceWarning($"Plugin import reservation settlement unavailable: {reservationError}"); }
                try
                {
                    using var claimBudget = new CancellationTokenSource(TimeSpan.FromSeconds(5));
                    await CallAsync(surface, "import.settle", JsonSerializer.SerializeToElement(new
                    {
                        contract = "vibetable.import-plans.v1",
                        token = source.Token,
                        attempt = source.Attempt,
                        outcome = outcome == "succeeded" ? "committed" : "rejected"
                    }), claimBudget.Token, auditGeneration: source.Generation);
                }
                catch (Exception claimError) { System.Diagnostics.Trace.TraceWarning($"Plugin import settlement unavailable: {claimError}"); }
            }
        }
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
