using System.Collections.Concurrent;
using System.Net.Http;
using System.Text;
using System.Text.Encodings.Web;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>Closed frame API. Installation identity comes from Host, never plugin JSON.</summary>
internal sealed partial class PluginSurfaceApi : IDisposable
{
    internal const string Contract = "vibetable.plugin-api.v1";
    private const int MaxBytes = 1024 * 1024;
    // Host-only attachment.stage base64 envelope; ordinary frame/HTTP requests stay at MaxBytes.
    internal const int AttachmentStageRequestBytes = 2 * MaxBytes;
    private static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web)
    { UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow };
    // This encoder is only for the Host-to-Go application/json stage body, never HTML or frame output.
    // Keeping base64 '+' literal makes every allowed 1 MiB file fit the existing 2 MiB wire budget.
    private static readonly JsonSerializerOptions AttachmentStageJson = new()
    { Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping };
    private readonly PluginSurfaceSessionManager _surfaces;
    private readonly HostPluginTaskRegistry _tasks;
    private readonly Func<ProductSidecarGenerationSnapshot?> _generation;
    private readonly Func<ProductSidecarGenerationSnapshot, Func<bool>, bool>? _subscriptionSource;
    private readonly IWebReplySink _reply;
    private readonly HttpClient _http;
    private readonly Func<PluginSurfaceSession, string, CancellationToken, Task<bool>>? _preparePrint;
    private readonly Action? _showPrint;
    private readonly Func<string, Task>? _releasePrint;
    private readonly ConcurrentDictionary<string, SurfaceState> _states = new(StringComparer.Ordinal);
    private sealed record NativeWrite(PluginProjectContext Context, TaskCompletionSource Completion);
    private readonly ConcurrentDictionary<string, Task> _cleanups = new(StringComparer.Ordinal);
    private readonly ConcurrentDictionary<string, NativeWrite> _writes = new(StringComparer.Ordinal);

    internal PluginSurfaceApi(PluginSurfaceSessionManager surfaces, HostPluginTaskRegistry tasks,
        Func<ProductSidecarGenerationSnapshot?> generation,
        IWebReplySink reply,
        HttpMessageHandler? handler = null,
        Func<ProductSidecarGenerationSnapshot, Func<bool>, bool>? subscriptionSource = null,
        Func<PluginSurfaceSession, string, CancellationToken, Task<bool>>? preparePrint = null,
        Action? showPrint = null,
        Func<string, Task>? releasePrint = null,
        Func<HostSessionFileBroker>? files = null, INativeProductFileHost? fileHost = null,
        Func<Func<string?>, Task<string?>>? pickFile = null, Func<PluginDataIoPort>? dataIo = null)
    {
        _preparePrint = preparePrint; _showPrint = showPrint; _releasePrint = releasePrint;
        _dataIo = dataIo; _files = files; _fileHost = fileHost; _pickFile = pickFile;
        _surfaces = surfaces; _tasks = tasks; _generation = generation; _reply = reply;
        _subscriptionSource = subscriptionSource;
        _surfaces.Closed += RetireState;
        _http = new HttpClient(handler ?? new SocketsHttpHandler { UseProxy = false, AllowAutoRedirect = false }) { Timeout = Timeout.InfiniteTimeSpan };
    }

    internal async Task<string> HandleAsync(string frameToken, string source, string raw)
    {
        string? requestId = null;
        bool committing = false;
        bool committed = false;
        bool printOpened = false;
        SurfaceState? state = null;
        ApiRequest? request = null;
        bool admitted = false;
        try
        {
            if (Encoding.UTF8.GetByteCount(raw) > MaxBytes)
            {
                requestId = ReadOversizeRequestId(raw);
                throw Error("plugin_message_too_large");
            }
            request = JsonSerializer.Deserialize<ApiRequest>(raw, Json) ?? throw Error("plugin_capability_invalid");
            requestId = request.RequestId;
            if (request.Contract != Contract) throw Error("plugin_api_unsupported");
            if (request.SurfaceToken != frameToken || string.IsNullOrWhiteSpace(requestId) || requestId.Length > 128)
                throw Error("plugin_source_denied");
            PluginSurfaceSession surface = CurrentSurface(frameToken, source);
            state = _states.GetOrAdd(frameToken, _ => new SurfaceState(surface));
            if (!_surfaces.IsActive(frameToken)) { RetireState(frameToken); throw Error("plugin_source_denied"); }
            if (state.Identity.Context != surface.Context || state.Identity.InstallationRevision != surface.InstallationRevision
                || state.Identity.InstallationId != surface.InstallationId
                || state.Identity.ActiveCollection != surface.ActiveCollection || state.Identity.ActionId != surface.ActionId
                || state.Identity.Risk != surface.Risk || state.Identity.Entry != surface.Entry
                || state.Identity.Presentation != surface.Presentation) throw Error("plugin_session_stale");
            foreach (var expired in state.Plans.Where(pair => pair.Value.Expires < DateTimeOffset.UtcNow))
                if (state.Plans.TryRemove(expired.Key, out var oldPlan)) { ReleaseImportSource(oldPlan.Source); TrackCleanup(DropAttachmentStagesAsync(surface, oldPlan.Attachments)); }
            foreach (var expired in state.Cursors.Where(pair => pair.Value.Expires < DateTimeOffset.UtcNow)) state.Cursors.TryRemove(expired.Key, out _);
            using var deadline = CancellationTokenSource.CreateLinkedTokenSource(state.Lifetime.Token);
            deadline.CancelAfter((request.Method == "mutation.submit" || request.Method is "file.pickRead" or "file.pickWrite" or "data.import.start" or "data.export.start" or "attachments.previewChange") ? TimeSpan.FromMinutes(2) : TimeSpan.FromSeconds(15));
            if (request.Method == "request.cancel")
            {
                var target = ReadHandle(request.Params, "requestId");
                CancelSubscriptionRequest(frameToken, target);
                if (state.Pending.TryGetValue(target, out var active)) { state.Cancelled[target] = true; await active.CancelAsync(); }
                return Reply(requestId, new { cancelRequested = true });
            }
            if (request.Method == "data.io.cancel")
            {
                // Cancel admission stays independent of state.Pending so a full normal
                // budget can always be cancelled, yet it is still bounded: a client that
                // bypasses the SDK cannot park unbounded 15s cancel-ack handlers per surface.
                if (!state.Cancelling.TryAdd(requestId, true)) throw Error("plugin_request_duplicate");
                if (state.Cancelling.Count > 8) { state.Cancelling.TryRemove(requestId, out _); throw Error("plugin_concurrency_limit"); }
                try { await HandleDataIoAsync(surface, state, request.Method, request.Params, deadline.Token); }
                finally { state.Cancelling.TryRemove(requestId, out _); }
                return Reply(requestId, new { cancelRequested = true });
            }
            if (request.Method == "file.release")
            {
                await HandleFileAsync(surface, state, request.Method, request.Params, deadline.Token);
                return Reply(requestId, new { released = true });
            }
            // Like cancellation, releasing a handle must remain possible when normal requests fill the budget.
            if (request.Method == "data.unsubscribe")
            {
                Unsubscribe(surface.SurfaceToken, ReadHandle(request.Params, "subscriptionId"));
                return Reply(requestId, new { unsubscribed = true });
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
                case "output.print":
                    {
                        if (request.Params.ValueKind != JsonValueKind.Object || request.Params.EnumerateObject().Any())
                            throw Error("plugin_capability_invalid");
                        if (surface.Presentation != "application") throw Error("plugin_print_denied");
                        if (_preparePrint is null || _showPrint is null || _releasePrint is null) throw Error("plugin_api_unsupported");
                        var generation = _generation() ?? throw Error("plugin_session_stale");
                        string preparationId = Guid.NewGuid().ToString("N");
                        bool printInvoked = false;
                        try
                        {
                            if (!await _preparePrint(surface, preparationId, deadline.Token)) throw Error("plugin_print_denied");
                            // Preparation is asynchronous; recheck Go installation authority after it, before native output.
                            await CallAsync(surface, "context.read", JsonSerializer.SerializeToElement(new { }), deadline.Token);
                            deadline.Token.ThrowIfCancellationRequested();
                            if (!generation.TryUseCurrent(() => _surfaces.TryUseCurrent(surface, () =>
                            {
                                deadline.Token.ThrowIfCancellationRequested();
                                printInvoked = true;
                                try { _showPrint(); }
                                catch (Exception) { throw Error("plugin_print_failed"); }
                                printOpened = true;
                                return true;
                            }))) throw Error("plugin_session_stale");
                        }
                        finally
                        {
                            // A throwing native call has unknown dialog state; keep its isolated print range.
                            if (!printInvoked) await _releasePrint(preparationId);
                        }
                        result = new { opened = true };
                        break;
                    }
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
                case "attachments.list":
                case "attachments.read":
                case "attachments.previewChange":
                    result = await HandleAttachmentAsync(surface, state, request.Method, request.Params, deadline.Token);
                    break;
                case "data.import.start":
                case "data.export.start":
                case "data.io.status":
                case "data.io.cancel":
                    result = await HandleDataIoAsync(surface, state, request.Method, request.Params, deadline.Token);
                    break;
                case "file.pickRead":
                case "file.pickWrite":
                case "file.read":
                case "file.write":
                case "file.release":
                    result = await HandleFileAsync(surface, state, request.Method, request.Params, deadline.Token);
                    if (result is JsonElement fileReceipt && fileReceipt.TryGetProperty("committed", out var fileCommitted) && fileCommitted.ValueKind == JsonValueKind.True) committed = true;
                    break;
                case "data.subscribe":
                    result = await SubscribeAsync(surface, state, requestId, request.Params, deadline.Token);
                    break;
                case "data.query":
                    {
                        var parameters = JsonNode.Parse(request.Params.GetRawText()) as JsonObject ?? throw Error("plugin_capability_invalid");
                        string? cursor = parameters["cursor"]?.GetValue<string>();
                        parameters.Remove("cursor");
                        Cursor? prior = null;
                        // The cursor binds the structured query; reordered object properties are still the same query.
                        if (cursor is not null && (!state.Cursors.TryRemove(cursor, out prior) || !JsonNode.DeepEquals(prior.Query, parameters) || prior.Expires < DateTimeOffset.UtcNow)) throw Error("plugin_cursor_invalid");
                        JsonElement page = await CallAsync(surface, request.Method, JsonSerializer.SerializeToElement(parameters), deadline.Token,
                            cursor: prior?.GoCursor, dependencies: prior?.Dependencies);
                        var output = JsonNode.Parse(page.GetRawText())!.AsObject();
                        string? next = output["nextCursor"]?.GetValue<string>();
                        if (!string.IsNullOrEmpty(next))
                        {
                            if (state.Cursors.Count >= 64) throw Error("plugin_query_limit");
                            string handle = Guid.NewGuid().ToString("N");
                            state.Cursors[handle] = new(parameters.DeepClone()!.AsObject(), next, page.GetProperty("dependencies").Clone(), DateTimeOffset.UtcNow.AddMinutes(5));
                            output["nextCursor"] = handle;
                        }
                        output.Remove("dependencies");
                        result = output;
                        break;
                    }
                case "mutation.preview":
                    {
                        if (surface.Risk is not ("write" or "destructive")) throw Error("plugin_write_denied");
                        if (request.Params.GetProperty("operations").EnumerateArray().Any(item => item.GetProperty("kind").GetString() == "setAttachments")) throw Error("plugin_capability_invalid");
                        JsonElement preview = await CallAsync(surface, request.Method, request.Params, deadline.Token);
                        if (state.Plans.Count >= 8) throw Error("plugin_plan_limit");
                        string handle = Guid.NewGuid().ToString("N");
                        if (!_surfaces.TryUseCurrent(surface, () => !state.Lifetime.IsCancellationRequested && state.Plans.Count < 8
                            && state.Plans.TryAdd(handle, new Plan(request.Params.Clone(), preview.GetProperty("affectedCount").GetInt32(), DateTimeOffset.UtcNow.AddMinutes(2))))) throw Error("plugin_plan_limit");
                        result = new { planId = handle, affectedCount = preview.GetProperty("affectedCount").GetInt32() };
                        break;
                    }
                case "mutation.submit":
                    {
                        string handle = ReadHandle(request.Params, "planId");
                        if (!state.Plans.TryRemove(handle, out var plan) || plan.Expires < DateTimeOffset.UtcNow)
                        {
                            // The admission sweep can miss a plan expiring during this request;
                            // once removed here nothing else owns its import source or stages.
                            ReleaseImportSource(plan?.Source);
                            if (plan?.Attachments is not null) TrackCleanup(DropAttachmentStagesAsync(surface, plan.Attachments));
                            throw Error("plugin_plan_stale");
                        }
                        string taskId = $"plugin-task-{Guid.NewGuid():N}";
                        string runId = $"plugin-run-{Guid.NewGuid():N}";
                        HostPluginTaskBinding binding;
                        (CancellationToken Token, PluginEventEnvelope Envelope) task;
                        ProductSidecarGenerationSnapshot auditGeneration;
                        bool registered = false;
                        try
                        {
                            binding = _tasks.CaptureNative() ?? throw Error("plugin_session_stale");
                            if (binding.Context != surface.Context) throw Error("plugin_session_stale");
                            var snapshot = new PluginRuntimeTaskSnapshot(taskId, runId, surface.PluginId, surface.PluginVersion, surface.ActionId, surface.ProjectKey,
                                plan.Payload.GetProperty("collection").GetString(), plan.Count, surface.Risk, "running", false, null, null, null, surface.InstallationRevision, surface.InstallationId, surface.SurfaceToken);
                            auditGeneration = _generation() ?? throw Error("plugin_session_stale");
                            task = _tasks.NativeTask(binding, snapshot, surface.Presentation is "application" or "headless" || plan.Source is not null || plan.Attachments is not null);
                            registered = true;
                            _reply.PostNotification("plugin.task.changed", task.Envelope);
                        }
                        catch (Exception error)
                        {
                            // The plan is already removed from the surface and no business run
                            // started; without the execution finally below nothing else would
                            // release its reserved import source or staged attachments.
                            await SettleImportSourceAsync(surface, plan.Source, "failed");
                            await DropAttachmentStagesAsync(surface, plan.Attachments);
                            if (registered)
                            {
                                // FinishNative converges the admitted task to a terminal state
                                // before its envelope is posted; a failing terminal notification
                                // must not mask the original error or the cleanup above.
                                try
                                {
                                    Finish(taskId, "failed", error: SafeError(error is PluginSurfacePolicyException policy ? policy.Code
                                        : error is SurfaceAuthorityException authority ? authority.Code : "plugin_request_failed"));
                                }
                                catch (Exception notifyError) { System.Diagnostics.Trace.TraceWarning($"Plugin task terminal notification failed: {notifyError}"); }
                            }
                            throw;
                        }
                        using var operation = CancellationTokenSource.CreateLinkedTokenSource(deadline.Token, task.Token);
                        var completion = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
                        _writes[runId] = new(binding.Context, completion);
                        bool started = false;
                        string outcome = "failed";
                        string errorCode = "";
                        try
                        {
                            if (plan.Source is { } importSource)
                                await CallAsync(surface, "import.bind", JsonSerializer.SerializeToElement(new
                                { contract = "vibetable.import-plans.v1", token = importSource.Token, attempt = importSource.Attempt, idempotencyPrefix = runId }), operation.Token);
                            await CallAsync(surface, "audit.start", JsonSerializer.SerializeToElement(new { taskId, plan = plan.Payload }, Json), operation.Token, commitId: runId);
                            started = true;
                            // A plugin can request confirmation, but cannot supply or resolve a decision.
                            var confirmation = _tasks.NativeConfirmation(taskId, plan.Payload, plan.Count);
                            _reply.PostNotification("plugin.interaction.requested", confirmation.Envelope);
                            bool approved = await confirmation.Decision.WaitAsync(operation.Token);
                            operation.Token.ThrowIfCancellationRequested();
                            CurrentSurface(frameToken, source);
                            if (!approved) { outcome = "rejected"; Finish(taskId, "cancelled"); result = new { status = "rejected", taskId }; break; }
                            await CallAsync(surface, "mutation.preview", plan.Payload, operation.Token);
                            operation.Token.ThrowIfCancellationRequested();
                            committing = true;
                            JsonElement receipt = await CallAsync(surface, "mutation.commit", plan.Payload, operation.Token, commitId: runId);
                            committed = true;
                            outcome = "succeeded";
                            Finish(taskId, "succeeded", new PluginRuntimeResult(PluginContractVersions.Result, "success", $"已保存 {plan.Count} 条修改", [], null, [], null, []));
                            result = new { status = "succeeded", taskId, receipt };
                        }
                        catch (Exception ex)
                        {
                            if (ex is SurfaceAuthorityException { NotCommitted: true } rejection && IsCommitRejection(rejection.Code)) committing = false;
                            bool timedOut = deadline.IsCancellationRequested && !state.Lifetime.IsCancellationRequested && !state.Cancelled.ContainsKey(requestId);
                            string code = committing ? "plugin_commit_unknown" : ex switch
                            {
                                OperationCanceledException => timedOut ? "plugin_timeout" : "plugin_cancel_requested",
                                PluginSurfacePolicyException policy => policy.Code,
                                SurfaceAuthorityException authorityError => authorityError.Code,
                                _ => "plugin_request_failed",
                            };
                            outcome = committing ? "unknown" : code == "plugin_cancel_requested" ? "cancelled" : "failed";
                            errorCode = code;
                            Finish(taskId, committing ? "aborted" : code == "plugin_cancel_requested" ? "cancelled" : "failed", error: SafeError(code));
                            throw Error(code);
                        }
                        finally
                        {
                            // Success is durably audited in Go's business transaction. Other
                            // outcomes use the original run/generation, even after retirement.
                            if (started && outcome != "succeeded")
                            {
                                using var auditDeadline = new CancellationTokenSource(TimeSpan.FromSeconds(5));
                                try
                                {
                                    await CallAsync(surface, "audit.finish", JsonSerializer.SerializeToElement(new { outcome, errorCode }, Json), auditDeadline.Token,
                                        commitId: runId, auditGeneration: auditGeneration);
                                }
                                catch (Exception)
                                {
                                    System.Diagnostics.Trace.TraceWarning($"Native plugin terminal audit unavailable for run {runId}; durable started event remains unknown.");
                                }
                            }
                            await SettleImportSourceAsync(surface, plan.Source, outcome);
                            await DropAttachmentStagesAsync(surface, plan.Attachments);
                            _writes.TryRemove(runId, out _);
                            completion.TrySetResult();
                        }
                        break;
                    }
                default: throw Error("plugin_api_unsupported");
            }
            if (!committed && !printOpened) CurrentSurface(frameToken, source);
            string response = request.Method is "file.read" or "attachments.read"
                ? ReadBlockReply(requestId, result) : Reply(requestId, result);
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

    // Only correlates a rejected envelope. Never admits identity or reads params.
    private static string? ReadOversizeRequestId(string raw)
    {
        Span<byte> header = stackalloc byte[4096];
        int count = Encoding.UTF8.GetBytes(raw.AsSpan(0, Math.Min(raw.Length, 1024)), header);
        var reader = new Utf8JsonReader(header[..count], isFinalBlock: false, state: default);
        try
        {
            if (!reader.Read() || reader.TokenType != JsonTokenType.StartObject) return null;
            int seen = 0;
            string? id = null;
            while (reader.Read())
            {
                if (reader.TokenType != JsonTokenType.PropertyName || reader.CurrentDepth != 1) return null;
                if (reader.ValueTextEquals("params")) return seen == 15 ? id : null;
                int field = reader.ValueTextEquals("contract") ? 1 : reader.ValueTextEquals("surfaceToken") ? 2
                    : reader.ValueTextEquals("requestId") ? 4 : reader.ValueTextEquals("method") ? 8 : 0;
                if (field == 0 || (seen & field) != 0 || !reader.Read() || reader.TokenType != JsonTokenType.String) return null;
                seen |= field;
                if (field == 4)
                {
                    id = reader.GetString();
                    if (string.IsNullOrWhiteSpace(id) || id.Length > 128) return null;
                }
            }
        }
        catch (JsonException) { /* Malformed or incomplete bounded headers have no safe correlation. */ }
        return null;
    }

    private PluginSurfaceSession CurrentSurface(string token, string source)
    {
        if (!_surfaces.TryGet(token, out var surface) || surface is null || surface.ApiVersion != "3.x"
            || surface.Context is null || string.IsNullOrWhiteSpace(surface.InstallationId) || !IsSameDocumentSource(surface.DocumentUri, source)) throw Error("plugin_source_denied");
        var generation = _generation();
        if (generation is null || generation.Identity.SessionEpoch != surface.Context.SessionGeneration
            || $"local:{Guid.Parse(generation.Identity.WorkspaceId):N}" != surface.ProjectKey
            || !generation.TryUseCurrent(() => true)) throw Error("plugin_session_stale");
        return surface;
    }

    /// <summary>
    /// A single-entry SPA may route internal pages through fragments; the
    /// document identity is scheme/host/path/query, so only fragment-only
    /// differences are accepted as the same document.
    /// </summary>
    private static bool IsSameDocumentSource(Uri document, string source)
    {
        if (string.Equals(document.AbsoluteUri, source, StringComparison.Ordinal)) return true;
        return Uri.TryCreate(source, UriKind.Absolute, out var current)
            && string.Equals(
                document.GetLeftPart(UriPartial.Query),
                current.GetLeftPart(UriPartial.Query),
                StringComparison.Ordinal);
    }

    private async Task<JsonElement> CallAsync(PluginSurfaceSession surface, string method, JsonElement parameters,
        CancellationToken token, string? cursor = null, JsonElement? dependencies = null, string? commitId = null,
        ProductSidecarGenerationSnapshot? auditGeneration = null)
    {
        bool terminalAudit = (method is "audit.finish" or "attachment.drop" or "import.drop" or "import.settle") && auditGeneration is not null;
        if (!terminalAudit) CurrentSurface(surface.SurfaceToken, surface.DocumentUri.AbsoluteUri);
        var generation = auditGeneration ?? _generation() ?? throw Error("plugin_session_stale");
        var payload = SurfacePrincipal(surface);
        payload["method"] = method;
        payload["requestId"] = commitId ?? Guid.NewGuid().ToString("N");
        payload["request"] = JsonNode.Parse(parameters.GetRawText());
        if (cursor is not null) payload["cursor"] = cursor;
        if (dependencies is not null) payload["dependencies"] = JsonNode.Parse(dependencies.Value.GetRawText());
        if (method == "context.read" && surface.InvocationContext is { } invocation)
            payload["invocationContext"] = JsonSerializer.SerializeToNode(new
            { invocation.SelectedKeys, invocation.QuerySnapshot, invocation.User }, Json);
        byte[] bytes = Encoding.UTF8.GetBytes(payload.ToJsonString(method == "attachment.stage" ? AttachmentStageJson : null));
        if (bytes.Length > (method == "attachment.stage" ? AttachmentStageRequestBytes : MaxBytes)) throw Error("plugin_message_too_large");
        using var request = new HttpRequestMessage(HttpMethod.Post, new Uri(generation.Context.Origin, "/api/vibetable/v3/plugins/surface"));
        request.Headers.Add(generation.Context.SessionHeaderName, generation.Context.SessionSecret);
        request.Content = new ByteArrayContent(bytes);
        request.Content.Headers.ContentType = new("application/json");
        Task<HttpResponseMessage>? sending = null;
        if (terminalAudit) sending = _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, token);
        else if (!generation.TryUseCurrent(() => { sending = _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, token); return true; })) throw Error("plugin_session_stale");
        using var response = await sending!;
        using var stream = await response.Content.ReadAsStreamAsync(token);
        using var memory = new System.IO.MemoryStream();
        byte[] buffer = new byte[8192];
        int count;
        while ((count = await stream.ReadAsync(buffer, token)) > 0)
        { if (memory.Length + count > MaxBytes) throw Error("plugin_message_too_large"); memory.Write(buffer, 0, count); }
        using var document = JsonDocument.Parse(memory.ToArray());
        if (!response.IsSuccessStatusCode)
            throw new SurfaceAuthorityException(document.RootElement.TryGetProperty("code", out var code) ? code.GetString() ?? "plugin_request_failed" : "plugin_request_failed",
                document.RootElement.TryGetProperty("commitOutcome", out var outcome) && outcome.ValueKind == JsonValueKind.String && outcome.GetString() == "not-committed");
        // A fully read and validated commit receipt settles the captured run even
        // when retirement follows the response. Other responses require a live session.
        if (method == "mutation.commit") ValidateReceipt(document.RootElement, parameters);
        else if (!terminalAudit) CurrentSurface(surface.SurfaceToken, surface.DocumentUri.AbsoluteUri);
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
            string expected = operation.GetProperty("kind").GetString() switch { "create" => "insert", "delete" => "delete", "setAttachments" => "setAttachments", _ => "update" };
            if (!row.TryGetProperty("operation", out var kind) || kind.GetString() != expected
                || !row.TryGetProperty("recordId", out var id) || id.ValueKind != JsonValueKind.String || string.IsNullOrEmpty(id.GetString())
                || expected != "insert" && id.GetString() != operation.GetProperty("primaryKey").GetString()) throw Error("plugin_commit_unknown");
        }
    }
    private static string ReadHandle(JsonElement parameters, string name)
    { if (parameters.ValueKind != JsonValueKind.Object || parameters.EnumerateObject().Count() != 1 || !parameters.TryGetProperty(name, out var value) || value.ValueKind != JsonValueKind.String || string.IsNullOrWhiteSpace(value.GetString())) throw Error("plugin_capability_invalid"); return value.GetString()!; }
    private static PluginSurfacePolicyException Error(string code) => new(code, "插件请求未完成。");
    private static bool IsCommitRejection(string code) => code is
        "plugin_read_denied" or "plugin_write_denied" or "plugin_cursor_stale" or "plugin_api_unsupported" or "plugin_capability_invalid" or "plugin_guard_required" or "plugin_audit_invalid"
        or "mutation.relation.restricted" or "mutation.digest_conflict" or "mutation.revision_conflict" or "mutation.schema_revision_conflict" or "mutation.idempotency_conflict" or "mutation.record.not_found";
    private sealed class SurfaceAuthorityException(string code, bool notCommitted) : Exception
    { internal string Code { get; } = code; internal bool NotCommitted { get; } = notCommitted; }
    private static string ReadBlockReply(string? id, object? result)
    {
        if (result is not JsonElement block) throw Error("plugin_capability_invalid");
        string encoded = block.GetProperty("base64").GetString() ?? throw Error("plugin_capability_invalid");
        if (encoded.Length > ((256 * 1024 + 2) / 3) * 4) throw Error("plugin_file_limit");
        byte[] bytes = Convert.FromBase64String(encoded);
        if (bytes.Length > 256 * 1024) throw Error("plugin_file_limit");
        using var output = new System.IO.MemoryStream();
        using (var writer = new Utf8JsonWriter(output))
        {
            writer.WriteStartObject();
            writer.WriteString("contract", Contract);
            writer.WriteString("requestId", id);
            writer.WriteStartObject("result");
            // Only base64 is emitted compactly. Other strings retain the default JSON encoder.
            // The complete JSON is posted with CoreWebView2Frame.PostWebMessageAsJson, never embedded in HTML.
            writer.WriteBase64String("base64", bytes);
            writer.WriteBoolean("eof", block.GetProperty("eof").GetBoolean());
            writer.WriteEndObject();
            writer.WriteEndObject();
        }
        return Encoding.UTF8.GetString(output.ToArray());
    }

    private static string Reply(string? id, object? result) => JsonSerializer.Serialize(new { contract = Contract, requestId = id, result }, Json);
    internal void Retire(string token)
    { _surfaces.Close(token); RetireState(token); }
    private void RetireState(string token)
    {
        ReleaseSubscriptions(token);
        ReleaseFiles(token);
        ReleaseDataIo(token);
        _attachmentRefs.TryRemove(token, out _);
        if (_states.TryRemove(token, out var state))
        { state.Lifetime.Cancel(); state.Lifetime.Dispose(); foreach (var plan in state.Plans.Values) { ReleaseImportSource(plan.Source); TrackCleanup(DropAttachmentStagesAsync(state.Identity, plan.Attachments)); } state.Plans.Clear(); state.Cursors.Clear(); }
    }
    public void Dispose() { _surfaces.Closed -= RetireState; foreach (string token in _states.Keys) Retire(token); _ = DisposeHttpAsync(); }
    private void TrackCleanup(Task cleanup)
    {
        if (cleanup.IsCompleted) return;
        string id = Guid.NewGuid().ToString("N"); _cleanups[id] = cleanup;
        _ = ObserveCleanupAsync(id, cleanup);
    }
    private async Task ObserveCleanupAsync(string id, Task cleanup)
    { try { await cleanup.ConfigureAwait(false); } finally { _cleanups.TryRemove(id, out _); } }
    private async Task DisposeHttpAsync()
    { try { await DrainAsync().ConfigureAwait(false); } finally { _http.Dispose(); } }
    internal async Task DrainAsync(PluginProjectContext? nextContext = null)
    {
        foreach (var pair in _states) if (pair.Value.Identity.Context != nextContext) Retire(pair.Key);
        try { await Task.WhenAll(_writes.Values.Where(value => value.Context != nextContext).Select(value => value.Completion.Task).Concat(_cleanups.Values)).WaitAsync(TimeSpan.FromSeconds(6)); }
        catch (TimeoutException) { /* Unfinished durable started events remain unknown. */ }
    }
    internal bool HasWrites => !_writes.IsEmpty;
    private sealed record ApiRequest(string Contract, string SurfaceToken, string RequestId, string Method, JsonElement Params);
    private sealed record Cursor(JsonObject Query, string GoCursor, JsonElement Dependencies, DateTimeOffset Expires);
    private sealed record Plan(JsonElement Payload, int Count, DateTimeOffset Expires, ImportSource? Source = null, AttachmentStages? Attachments = null);
    private sealed class SurfaceState(PluginSurfaceSession identity)
    {
        internal Func<string, Func<Func<bool>, bool>, CancellationToken, Task<bool>>? FrameNotifier { get; set; }
        internal PluginSurfaceSession Identity { get; } = identity;
        internal CancellationTokenSource Lifetime { get; } = new();
        internal ConcurrentDictionary<string, CancellationTokenSource> Pending { get; } = new();
        // In-flight data.io.cancel requestIds; bounded independently of Pending.
        internal ConcurrentDictionary<string, bool> Cancelling { get; } = new();
        internal ConcurrentDictionary<string, bool> Cancelled { get; } = new();
        internal ConcurrentDictionary<string, bool> Seen { get; } = new();
        internal ConcurrentQueue<string> Order { get; } = new();
        internal ConcurrentDictionary<string, Cursor> Cursors { get; } = new();
        internal ConcurrentDictionary<string, Plan> Plans { get; } = new();
    }
}
