using System.Collections.Concurrent;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>Closed frame API. Installation identity comes from Host, never plugin JSON.</summary>
internal sealed class PluginSurfaceApi : IDisposable
{
    internal const string Contract = "vibetable.plugin-api.v1";
    private const int MaxBytes = 1024 * 1024;
    private static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web)
    { UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow };
    private readonly PluginSurfaceSessionManager _surfaces;
    private readonly HostPluginTaskRegistry _tasks;
    private readonly Func<ProductSidecarGenerationSnapshot?> _generation;
    private readonly IWebReplySink _reply;
    private readonly HttpClient _http;
    private readonly ConcurrentDictionary<string, SurfaceState> _states = new(StringComparer.Ordinal);

    internal PluginSurfaceApi(PluginSurfaceSessionManager surfaces, HostPluginTaskRegistry tasks,
        Func<ProductSidecarGenerationSnapshot?> generation,
        IWebReplySink reply,
        HttpMessageHandler? handler = null)
    {
        _surfaces = surfaces; _tasks = tasks; _generation = generation; _reply = reply;
        _http = new HttpClient(handler ?? new SocketsHttpHandler { UseProxy = false, AllowAutoRedirect = false }) { Timeout = Timeout.InfiniteTimeSpan };
    }

    internal async Task<string> HandleAsync(string frameToken, string source, string raw)
    {
        string? requestId = null;
        bool committing = false;
        SurfaceState? state = null;
        ApiRequest? request = null;
        bool admitted = false;
        try
        {
            if (Encoding.UTF8.GetByteCount(raw) > MaxBytes) throw Error("plugin_message_too_large");
            request = JsonSerializer.Deserialize<ApiRequest>(raw, Json) ?? throw Error("plugin_capability_invalid");
            requestId = request.RequestId;
            if (request.Contract != Contract) throw Error("plugin_api_unsupported");
            if (request.SurfaceToken != frameToken || string.IsNullOrWhiteSpace(requestId) || requestId.Length > 128)
                throw Error("plugin_source_denied");
            PluginSurfaceSession surface = CurrentSurface(frameToken, source);
            state = _states.GetOrAdd(frameToken, _ => new SurfaceState(surface));
            if (state.Identity.Context != surface.Context || state.Identity.InstallationRevision != surface.InstallationRevision
                || state.Identity.ActiveCollection != surface.ActiveCollection) throw Error("plugin_session_stale");
            foreach (var expired in state.Plans.Where(pair => pair.Value.Expires < DateTimeOffset.UtcNow)) state.Plans.TryRemove(expired.Key, out _);
            foreach (var expired in state.Cursors.Where(pair => pair.Value.Expires < DateTimeOffset.UtcNow)) state.Cursors.TryRemove(expired.Key, out _);
            using var deadline = CancellationTokenSource.CreateLinkedTokenSource(state.Lifetime.Token);
            deadline.CancelAfter(request.Method == "mutation.submit" ? TimeSpan.FromMinutes(2) : TimeSpan.FromSeconds(15));
            if (request.Method == "request.cancel")
            {
                var target = ReadHandle(request.Params, "requestId");
                if (state.Pending.TryGetValue(target, out var active)) { state.Cancelled[target] = true; await active.CancelAsync(); }
                return Reply(requestId, new { cancelRequested = true });
            }
            if (!state.Pending.TryAdd(requestId, deadline)) throw Error("plugin_request_duplicate");
            admitted = true;
            if (state.Pending.Count > 8) throw Error("plugin_concurrency_limit");
            if (!state.Seen.TryAdd(requestId, true)) throw Error("plugin_request_duplicate");
            state.Order.Enqueue(requestId);
            while (state.Order.Count > 256 && state.Order.TryDequeue(out var old)) state.Seen.TryRemove(old, out _);
            object? result;
            switch (request.Method)
            {
                case "context.read":
                    {
                        JsonElement approved = await CallAsync(surface, request.Method, request.Params, deadline.Token);
                        var context = JsonNode.Parse(approved.GetRawText())!.AsObject();
                        context["locale"] = surface.InvocationContext?.Locale ?? "zh-CN";
                        context["theme"] = surface.InvocationContext?.Theme ?? "light";
                        context["density"] = surface.InvocationContext?.Density ?? "comfortable";
                        context["hostVersion"] = typeof(PluginSurfaceApi).Assembly.GetName().Version?.ToString(3) ?? "unknown";
                        result = context;
                        break;
                    }
                case "storage.get":
                case "storage.set":
                case "storage.delete":
                case "data.describe":
                    result = await CallAsync(surface, request.Method, request.Params, deadline.Token);
                    break;
                case "data.query":
                    {
                        var parameters = JsonNode.Parse(request.Params.GetRawText()) as JsonObject ?? throw Error("plugin_capability_invalid");
                        string? cursor = parameters["cursor"]?.GetValue<string>();
                        parameters.Remove("cursor");
                        string query = parameters.ToJsonString();
                        Cursor? prior = null;
                        if (cursor is not null && (!state.Cursors.TryRemove(cursor, out prior) || prior.Query != query || prior.Expires < DateTimeOffset.UtcNow)) throw Error("plugin_cursor_invalid");
                        JsonElement page = await CallAsync(surface, request.Method, JsonSerializer.SerializeToElement(parameters), deadline.Token,
                            cursor: prior?.GoCursor, dependencies: prior?.Dependencies);
                        var output = JsonNode.Parse(page.GetRawText())!.AsObject();
                        string? next = output["nextCursor"]?.GetValue<string>();
                        if (!string.IsNullOrEmpty(next))
                        {
                            if (state.Cursors.Count >= 64) throw Error("plugin_query_limit");
                            string handle = Guid.NewGuid().ToString("N");
                            state.Cursors[handle] = new(query, next, page.GetProperty("dependencies").Clone(), DateTimeOffset.UtcNow.AddMinutes(5));
                            output["nextCursor"] = handle;
                        }
                        output.Remove("dependencies");
                        result = output;
                        break;
                    }
                case "mutation.preview":
                    {
                        JsonElement preview = await CallAsync(surface, request.Method, request.Params, deadline.Token);
                        if (state.Plans.Count >= 8) throw Error("plugin_plan_limit");
                        string handle = Guid.NewGuid().ToString("N");
                        state.Plans[handle] = new(request.Params.Clone(), preview.GetProperty("affectedCount").GetInt32(), DateTimeOffset.UtcNow.AddMinutes(2));
                        result = new { planId = handle, affectedCount = preview.GetProperty("affectedCount").GetInt32() };
                        break;
                    }
                case "mutation.submit":
                    {
                        string handle = ReadHandle(request.Params, "planId");
                        if (!state.Plans.TryRemove(handle, out var plan) || plan.Expires < DateTimeOffset.UtcNow) throw Error("plugin_plan_stale");
                        var binding = _tasks.CaptureNative() ?? throw Error("plugin_session_stale");
                        if (binding.Context != surface.Context) throw Error("plugin_session_stale");
                        string taskId = $"plugin-task-{Guid.NewGuid():N}";
                        string runId = $"plugin-run-{Guid.NewGuid():N}";
                        var snapshot = new PluginRuntimeTaskSnapshot(taskId, runId, surface.PluginId, surface.PluginVersion, "surface.save", surface.ProjectKey,
                            plan.Payload.GetProperty("collection").GetString(), plan.Count, "write", "running", false, null, null, null);
                        var task = _tasks.NativeTask(binding, snapshot);
                        _reply.PostNotification("plugin.task.changed", task.Envelope);
                        using var operation = CancellationTokenSource.CreateLinkedTokenSource(deadline.Token, task.Token);
                        try
                        {
                            // A plugin can request confirmation, but cannot supply or resolve a decision.
                            var confirmation = _tasks.NativeConfirmation(taskId, plan.Payload, plan.Count);
                            _reply.PostNotification("plugin.interaction.requested", confirmation.Envelope);
                            bool approved = await confirmation.Decision.WaitAsync(operation.Token);
                            operation.Token.ThrowIfCancellationRequested();
                            CurrentSurface(frameToken, source);
                            if (!approved) { Finish(taskId, "cancelled"); result = new { status = "rejected", taskId }; break; }
                            await CallAsync(surface, "mutation.preview", plan.Payload, operation.Token);
                            operation.Token.ThrowIfCancellationRequested();
                            committing = true;
                            JsonElement receipt = await CallAsync(surface, "mutation.commit", plan.Payload, operation.Token, commitId: runId);
                            ValidateReceipt(receipt, plan.Payload);
                            Finish(taskId, "succeeded", new PluginRuntimeResult(PluginContractVersions.Result, "success", $"已保存 {plan.Count} 条修改", [], null, [], null, []));
                            result = new { status = "succeeded", taskId, receipt };
                        }
                        catch (Exception ex)
                        {
                            if (ex is SurfaceAuthorityException) committing = false;
                            bool timedOut = deadline.IsCancellationRequested && !state.Lifetime.IsCancellationRequested && !state.Cancelled.ContainsKey(requestId);
                            string code = committing ? "plugin_commit_unknown" : ex switch
                            {
                                OperationCanceledException => timedOut ? "plugin_timeout" : "plugin_cancel_requested",
                                PluginSurfacePolicyException policy => policy.Code,
                                SurfaceAuthorityException authorityError => authorityError.Code,
                                _ => "plugin_request_failed",
                            };
                            Finish(taskId, committing ? "aborted" : code == "plugin_cancel_requested" ? "cancelled" : "failed", error: SafeError(code));
                            throw Error(code);
                        }
                        break;
                    }
                default: throw Error("plugin_api_unsupported");
            }
            CurrentSurface(frameToken, source);
            string response = Reply(requestId, result);
            if (Encoding.UTF8.GetByteCount(response) > MaxBytes) throw Error("plugin_message_too_large");
            return response;
        }
        catch (Exception ex)
        {
            string code = committing ? "plugin_commit_unknown" : ex switch
            {
                PluginSurfacePolicyException policy => policy.Code,
                SurfaceAuthorityException authorityError => authorityError.Code,
                OperationCanceledException when state?.Cancelled.ContainsKey(requestId ?? "") == true => "plugin_cancel_requested",
                OperationCanceledException => "plugin_timeout",
                _ => "plugin_capability_invalid",
            };
            return JsonSerializer.Serialize(new { contract = Contract, requestId, error = new { code, message = "插件请求未完成。" } }, Json);
        }
        finally { if (admitted && requestId is not null) { state?.Pending.TryRemove(requestId, out _); state?.Cancelled.TryRemove(requestId, out _); } }
    }

    private PluginSurfaceSession CurrentSurface(string token, string source)
    {
        if (!_surfaces.TryGet(token, out var surface) || surface is null || surface.ApiVersion != "3.x"
            || surface.Context is null || surface.DocumentUri.AbsoluteUri != source) throw Error("plugin_source_denied");
        var generation = _generation();
        if (generation is null || generation.Identity.SessionEpoch != surface.Context.SessionGeneration
            || $"local:{Guid.Parse(generation.Identity.WorkspaceId):N}" != surface.ProjectKey
            || !generation.TryUseCurrent(() => true)) throw Error("plugin_session_stale");
        return surface;
    }

    private async Task<JsonElement> CallAsync(PluginSurfaceSession surface, string method, JsonElement parameters,
        CancellationToken token, string? cursor = null, JsonElement? dependencies = null, string? commitId = null)
    {
        CurrentSurface(surface.SurfaceToken, surface.DocumentUri.AbsoluteUri);
        var generation = _generation() ?? throw Error("plugin_session_stale");
        var payload = new JsonObject
        {
            ["method"] = method,
            ["projectKey"] = surface.ProjectKey,
            ["pluginId"] = surface.PluginId,
            ["packageHash"] = surface.Revision.PackageHash,
            ["sessionEpoch"] = surface.Context!.SessionGeneration,
            ["installationRevision"] = surface.InstallationRevision,
            ["activeCollection"] = surface.ActiveCollection,
            ["requestId"] = commitId ?? Guid.NewGuid().ToString("N"),
            ["request"] = JsonNode.Parse(parameters.GetRawText()),
        };
        if (cursor is not null) payload["cursor"] = cursor;
        if (dependencies is not null) payload["dependencies"] = JsonNode.Parse(dependencies.Value.GetRawText());
        byte[] bytes = Encoding.UTF8.GetBytes(payload.ToJsonString());
        if (bytes.Length > MaxBytes) throw Error("plugin_message_too_large");
        using var request = new HttpRequestMessage(HttpMethod.Post, new Uri(generation.Context.Origin, "/api/vibetable/v3/plugins/surface"));
        request.Headers.Add(generation.Context.SessionHeaderName, generation.Context.SessionSecret);
        request.Content = new ByteArrayContent(bytes);
        request.Content.Headers.ContentType = new("application/json");
        Task<HttpResponseMessage>? sending = null;
        if (!generation.TryUseCurrent(() => { sending = _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, token); return true; })) throw Error("plugin_session_stale");
        using var response = await sending!;
        using var stream = await response.Content.ReadAsStreamAsync(token);
        using var memory = new System.IO.MemoryStream();
        byte[] buffer = new byte[8192];
        int count;
        while ((count = await stream.ReadAsync(buffer, token)) > 0)
        { if (memory.Length + count > MaxBytes) throw Error("plugin_message_too_large"); memory.Write(buffer, 0, count); }
        using var document = JsonDocument.Parse(memory.ToArray());
        if (!response.IsSuccessStatusCode)
            throw new SurfaceAuthorityException(document.RootElement.TryGetProperty("code", out var code) ? code.GetString() ?? "plugin_request_failed" : "plugin_request_failed");
        CurrentSurface(surface.SurfaceToken, surface.DocumentUri.AbsoluteUri);
        return document.RootElement.Clone();
    }

    private void Finish(string taskId, string state, PluginRuntimeResult? result = null, PluginRuntimeSafeError? error = null)
    { if (_tasks.FinishNative(taskId, state, result, error) is { } envelope) _reply.PostNotification("plugin.task.changed", envelope); }
    private static PluginRuntimeSafeError SafeError(string code) => new("vibetable.plugin-error.v1", code, "插件保存未完成，请核对任务及数据。", "none", null, null, null, new Dictionary<string, JsonElement>(), null);
    private static void ValidateReceipt(JsonElement receipt, JsonElement plan)
    {
        if (!receipt.TryGetProperty("contractVersion", out var version) || version.GetString() != "2.0"
            || !receipt.TryGetProperty("status", out var status) || status.GetString() is not ("applied" or "replayed")
            || !receipt.TryGetProperty("affectedRows", out var rows) || rows.ValueKind != JsonValueKind.Array
            || rows.GetArrayLength() != plan.GetProperty("operations").GetArrayLength()) throw Error("plugin_commit_unknown");
        int index = 0;
        foreach (var row in rows.EnumerateArray())
        {
            var operation = plan.GetProperty("operations")[index++];
            string expected = operation.GetProperty("kind").GetString() == "create" ? "insert" : "update";
            if (!row.TryGetProperty("operation", out var kind) || kind.GetString() != expected
                || !row.TryGetProperty("recordId", out var id) || id.ValueKind != JsonValueKind.String || string.IsNullOrEmpty(id.GetString())
                || expected == "update" && id.GetString() != operation.GetProperty("primaryKey").GetString()) throw Error("plugin_commit_unknown");
        }
    }
    private static string ReadHandle(JsonElement parameters, string name)
    { if (parameters.ValueKind != JsonValueKind.Object || parameters.EnumerateObject().Count() != 1 || !parameters.TryGetProperty(name, out var value) || value.ValueKind != JsonValueKind.String || string.IsNullOrWhiteSpace(value.GetString())) throw Error("plugin_capability_invalid"); return value.GetString()!; }
    private static PluginSurfacePolicyException Error(string code) => new(code, "插件请求未完成。");
    private sealed class SurfaceAuthorityException(string code) : Exception { internal string Code { get; } = code; }
    private static string Reply(string? id, object? result) => JsonSerializer.Serialize(new { contract = Contract, requestId = id, result }, Json);
    internal void Retire(string token)
    { if (_states.TryRemove(token, out var state)) { state.Lifetime.Cancel(); state.Lifetime.Dispose(); state.Plans.Clear(); state.Cursors.Clear(); } }
    public void Dispose() { foreach (string token in _states.Keys) Retire(token); _http.Dispose(); }
    private sealed record ApiRequest(string Contract, string SurfaceToken, string RequestId, string Method, JsonElement Params);
    private sealed record Cursor(string Query, string GoCursor, JsonElement Dependencies, DateTimeOffset Expires);
    private sealed record Plan(JsonElement Payload, int Count, DateTimeOffset Expires);
    private sealed class SurfaceState(PluginSurfaceSession identity)
    {
        internal PluginSurfaceSession Identity { get; } = identity;
        internal CancellationTokenSource Lifetime { get; } = new();
        internal ConcurrentDictionary<string, CancellationTokenSource> Pending { get; } = new();
        internal ConcurrentDictionary<string, bool> Cancelled { get; } = new();
        internal ConcurrentDictionary<string, bool> Seen { get; } = new();
        internal ConcurrentQueue<string> Order { get; } = new();
        internal ConcurrentDictionary<string, Cursor> Cursors { get; } = new();
        internal ConcurrentDictionary<string, Plan> Plans { get; } = new();
    }
}
