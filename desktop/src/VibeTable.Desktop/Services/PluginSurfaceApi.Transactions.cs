using System.Text.Json;
using System.Text.Json.Nodes;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

internal sealed partial class PluginSurfaceApi
{
    private const string TransactionContract = "vibetable.plugin-transactions.v1";

    private async Task<object?> HandleTransactionAsync(PluginSurfaceSession surface, string method, JsonElement parameters, CancellationToken token)
    {
        if (method == "transaction.preview")
        {
            var preview = await CallAsync(surface, method, parameters, token);
            string? requested = parameters.TryGetProperty("requestId", out var id) ? id.GetString() : null;
            ValidateTransactionResult(preview, surface, requested);
            return preview;
        }
        if (method == "transaction.status")
        {
            string id = ReadHandle(parameters, "requestId");
            var result = await CallAsync(surface, method, parameters, token);
            ValidateTransactionResult(result, surface, id);
            return result;
        }
        if (surface.Risk is not ("write" or "destructive")) throw Error("plugin_write_denied");
        JsonElement confirmationPlan;
        JsonElement? previewPlan = null;
        int count;
        string title;
        if (method == "transaction.configure")
        {
            if (parameters.ValueKind != JsonValueKind.Object || parameters.EnumerateObject().Count() != 1
                || !parameters.TryGetProperty("declaration", out var declaration) || declaration.ValueKind != JsonValueKind.Object
                || declaration.GetProperty("contract").GetString() != TransactionContract) throw Error("plugin_capability_invalid");
            // Go matches this exact declaration to the installed manifest before registering anything.
            var model = await CallAsync(surface, "model.read", JsonSerializer.SerializeToElement(new { }), token);
            if (model.GetProperty("status").GetString() != "completed") throw Error("transaction.model_required");
            var decimals = declaration.TryGetProperty("decimals", out var decimalFields)
                ? decimalFields.EnumerateArray().Select(value => value.Clone()) : [];
            var operations = declaration.GetProperty("rules").EnumerateArray()
                .Concat(decimals).Select(value =>
                {
                    var display = JsonNode.Parse(value.GetRawText())!.AsObject();
                    display["binding"] = JsonNode.Parse(model.GetProperty("bindings").GetProperty(value.GetProperty("entity").GetString()!).GetRawText());
                    if (value.TryGetProperty("target", out var target) && target.GetString() is { Length: > 0 } targetEntity)
                        display["targetBinding"] = JsonNode.Parse(model.GetProperty("bindings").GetProperty(targetEntity).GetRawText());
                    return display;
                }).ToArray();
            if (operations.Length is < 1 or > 128) throw Error("transaction.rules_invalid");
            confirmationPlan = JsonSerializer.SerializeToElement(new { collection = declaration.GetProperty("modelId"), operations });
            count = operations.Length;
            title = "登记以下精确业务规则；插件停用或卸载后，普通编辑、导入和撤销仍受这些规则保护";
        }
        else
        {
            string id = ReadHandle(parameters, "requestId");
            var prepared = await CallAsync(surface, "transaction.status", parameters, token);
            ValidateTransactionResult(prepared, surface, id);
            if (prepared.GetProperty("status").GetString() != "prepared") return prepared;
            previewPlan = prepared;
            var bindings = prepared.GetProperty("model").GetProperty("bindings");
            var newIds = prepared.GetProperty("newIds");
            var explicitOperations = prepared.GetProperty("plan").GetProperty("operations").EnumerateArray().Select(op =>
            {
                string entity = op.GetProperty("entity").GetString()!;
                var display = JsonNode.Parse(op.GetRawText())!.AsObject();
                display["tableId"] = bindings.GetProperty(entity).GetProperty("tableId").GetString();
                display["fieldBindings"] = JsonNode.Parse(bindings.GetProperty(entity).GetProperty("fields").GetRawText());
                if (op.GetProperty("kind").GetString() == "create")
                    display["recordId"] = newIds.GetProperty(entity).GetProperty(op.GetProperty("tempId").GetString()!).GetString();
                return display;
            }).ToDictionary(display => (display["tableId"]!.GetValue<string>(), display["recordId"]!.GetValue<string>()));
            var operations = prepared.GetProperty("effects").EnumerateArray().Select(effect =>
            {
                string table = effect.GetProperty("tableId").GetString()!;
                string record = effect.GetProperty("recordId").GetString()!;
                bool explicitWrite = explicitOperations.TryGetValue((table, record), out var requested);
                var display = JsonNode.Parse(effect.GetRawText())!.AsObject();
                string kind = effect.GetProperty("operation").GetString()!;
                display["kind"] = kind == "insert" ? "create" : kind;
                display["derived"] = !explicitWrite;
                if (requested is not null) display["requested"] = requested;
                foreach (var entity in bindings.EnumerateObject().Where(entity => entity.Value.GetProperty("tableId").GetString() == table))
                    display["binding"] = JsonNode.Parse(entity.Value.GetRawText());
                return display;
            }).ToArray();
            confirmationPlan = JsonSerializer.SerializeToElement(new { collection = bindings, operations });
            count = operations.Length;
            title = "一次原子提交以下跨表精确计划（含派生关联写入）；影响范围或修订变化将整笔拒绝，请显式刷新后重新确认";
        }

        string taskId = $"plugin-task-{Guid.NewGuid():N}";
        string runId = $"plugin-run-{Guid.NewGuid():N}";
        var binding = _tasks.CaptureNative() ?? throw Error("plugin_session_stale");
        if (binding.Context != surface.Context) throw Error("plugin_session_stale");
        var snapshot = new PluginRuntimeTaskSnapshot(taskId, runId, surface.PluginId, surface.PluginVersion, surface.ActionId, surface.ProjectKey,
            null, count, surface.Risk, "running", false, null, null, null, surface.InstallationRevision, surface.InstallationId, surface.SurfaceToken);
        var task = _tasks.NativeTask(binding, snapshot, nativeOnlyApproval: true);
        using var operation = CancellationTokenSource.CreateLinkedTokenSource(token, task.Token);
        var completion = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        _writes[taskId] = new(binding.Context, completion);
        bool committing = false;
        try
        {
            _reply.PostNotification("plugin.task.changed", task.Envelope);
            var summary = new List<IReadOnlyDictionary<string, JsonElement>>();
            if (method == "transaction.configure") summary.Add(new Dictionary<string, JsonElement>
                { ["modelId"] = parameters.GetProperty("declaration").GetProperty("modelId").Clone(),
                    ["version"] = parameters.GetProperty("declaration").GetProperty("version").Clone() });
            else
            {
                summary.Add(new Dictionary<string, JsonElement> { ["previewRevision"] = previewPlan!.Value.GetProperty("previewRevision").Clone(),
                    ["dataRevisions"] = previewPlan.Value.GetProperty("dataRevisions").Clone() });
                foreach (var group in confirmationPlan.GetProperty("operations").EnumerateArray().GroupBy(op => op.GetProperty("tableId").GetString()))
                    summary.Add(new Dictionary<string, JsonElement> { ["tableId"] = JsonSerializer.SerializeToElement(group.Key),
                        ["operations"] = JsonSerializer.SerializeToElement(group.GroupBy(op => op.GetProperty("kind").GetString()).ToDictionary(kinds => kinds.Key!, kinds => kinds.Count())) });
            }
            var confirmation = _tasks.NativeConfirmation(taskId, confirmationPlan, count, title, summary, exact: true);
            _reply.PostNotification("plugin.interaction.requested", confirmation.Envelope);
            bool approved = await confirmation.Decision.WaitAsync(operation.Token);
            operation.Token.ThrowIfCancellationRequested();
            if (!approved) { FinishTransaction(taskId, "cancelled"); return null; }
            CurrentSurface(surface.SurfaceToken, surface.DocumentUri.AbsoluteUri);
            committing = true;
            var commitParameters = previewPlan is { } preparedPlan ? JsonSerializer.SerializeToElement(new
                { requestId = preparedPlan.GetProperty("requestId").GetString(), previewRevision = preparedPlan.GetProperty("previewRevision").GetInt64() }) : parameters;
            var result = await CallAsync(surface, method == "transaction.configure" ? method : "transaction.commit", commitParameters,
                operation.Token, commitId: runId, transactionPlan: previewPlan);
            // A trusted receipt settles success even when cancellation/retirement races its delivery.
            FinishTransaction(taskId, "succeeded", new PluginRuntimeResult(PluginContractVersions.Result, "success",
                method == "transaction.configure" ? "业务规则登记完成" : "跨表事务提交完成", [], null, [], null, []));
            return result;
        }
        catch (Exception error)
        {
            if (error is SurfaceAuthorityException { NotCommitted: true }) committing = false;
            string code = committing ? "plugin_commit_unknown" : error switch
            {
                OperationCanceledException => "plugin_cancel_requested",
                PluginSurfacePolicyException policy => policy.Code,
                SurfaceAuthorityException authority => authority.Code,
                _ => "plugin_request_failed",
            };
            FinishTransaction(taskId, committing ? "aborted" : code == "plugin_cancel_requested" ? "cancelled" : "failed", error: SafeError(code));
            throw Error(code);
        }
        finally { _writes.TryRemove(taskId, out _); completion.TrySetResult(); }
    }

    private void FinishTransaction(string taskId, string state, PluginRuntimeResult? result = null, PluginRuntimeSafeError? error = null)
    {
        var envelope = _tasks.FinishNative(taskId, state, result, error);
        if (envelope is null) return;
        try { _reply.PostNotification("plugin.task.changed", envelope); }
        catch (Exception notificationError) { System.Diagnostics.Trace.TraceWarning("plugin_transaction.notification_failed:" + notificationError.GetType().Name); }
    }

    private static bool SameJson(JsonElement first, JsonElement second)
        => JsonNode.DeepEquals(JsonNode.Parse(first.GetRawText()), JsonNode.Parse(second.GetRawText()));

    private static void ValidateTransactionConfiguration(JsonElement result, JsonElement parameters)
    {
        var declaration = parameters.GetProperty("declaration");
        if (result.GetProperty("contract").GetString() != TransactionContract || result.GetProperty("status").GetString() != "configured"
            || result.GetProperty("modelId").GetString() != declaration.GetProperty("modelId").GetString()
            || result.GetProperty("version").GetInt64() != declaration.GetProperty("version").GetInt64()) throw Error("plugin_commit_unknown");
    }

    private static void ValidateTransactionResult(JsonElement result, PluginSurfaceSession surface, string? requestId, JsonElement? preview = null)
    {
        try
        {
            if (result.GetProperty("contract").GetString() != TransactionContract
                || result.GetProperty("requestId").GetString() is not { Length: > 0 and <= 128 } actualId
                || requestId is not null && requestId != actualId) throw Error("plugin_commit_unknown");
            string? status = result.GetProperty("status").GetString();
            if (status == "unknown" && preview is null) return;
            if (!Guid.TryParse(result.GetProperty("workspaceId").GetString(), out var workspace)
                || $"local:{workspace:N}" != surface.ProjectKey || result.GetProperty("installationId").GetString() != surface.InstallationId)
                throw Error("plugin_commit_unknown");
            if (status == "prepared")
            {
                if (preview is not null || result.GetProperty("installationRevision").GetInt32() != surface.InstallationRevision
                    || result.GetProperty("plan").GetProperty("contract").GetString() != "vibetable.transaction-plan.v1"
                    || result.GetProperty("plan").GetProperty("operations").GetArrayLength() is < 1 or > 1000
                    || result.GetProperty("plan").GetProperty("modelId").GetString() != result.GetProperty("declaration").GetProperty("modelId").GetString()
                    || result.GetProperty("previewRevision").GetInt64() < 1
                    || result.GetProperty("effects").GetArrayLength() is < 1 or > 1000
                    || result.GetProperty("dataRevisions").ValueKind != JsonValueKind.Object
                    || result.GetProperty("newIds").ValueKind != JsonValueKind.Object
                    || result.GetProperty("model").GetProperty("bindings").ValueKind != JsonValueKind.Object) throw Error("plugin_commit_unknown");
                var effects = new Dictionary<(string Table, string Record), string>();
                foreach (var effect in result.GetProperty("effects").EnumerateArray())
                {
                    string table = effect.GetProperty("tableId").GetString()!;
                    string record = effect.GetProperty("recordId").GetString()!;
                    string kind = effect.GetProperty("operation").GetString()!;
                    if (string.IsNullOrEmpty(table) || string.IsNullOrEmpty(record) || kind is not ("insert" or "update" or "delete" or "archive" or "restore")
                        || string.IsNullOrEmpty(effect.GetProperty("schemaRevision").GetString())
                        || !result.GetProperty("dataRevisions").TryGetProperty(table, out var baseline) || string.IsNullOrEmpty(baseline.GetString())
                        || kind != "insert" && (!effect.TryGetProperty("expectedDigest", out var digest) || string.IsNullOrEmpty(digest.GetString()))
                        || !effects.TryAdd((table, record), kind)) throw Error("plugin_commit_unknown");
                }
                foreach (var op in result.GetProperty("plan").GetProperty("operations").EnumerateArray())
                {
                    string entity = op.GetProperty("entity").GetString()!;
                    string kind = op.GetProperty("kind").GetString()!;
                    string table = result.GetProperty("model").GetProperty("bindings").GetProperty(entity).GetProperty("tableId").GetString()!;
                    string record = kind == "create" ? result.GetProperty("newIds").GetProperty(entity).GetProperty(op.GetProperty("tempId").GetString()!).GetString()!
                        : op.GetProperty("recordId").GetString()!;
                    if (!effects.TryGetValue((table, record), out var actual) || actual != (kind == "create" ? "insert" : kind)) throw Error("plugin_commit_unknown");
                }
                return;
            }
            if (status != "applied") throw Error("plugin_commit_unknown");
            var receipt = result.GetProperty("receipt");
            if (receipt.GetProperty("status").GetString() != "applied" || string.IsNullOrEmpty(receipt.GetProperty("changeSetId").GetString())
                || receipt.GetProperty("rows").GetArrayLength() is < 1 or > 1000 || result.GetProperty("newIds").ValueKind != JsonValueKind.Object
                || receipt.GetProperty("revisions").ValueKind != JsonValueKind.Object || receipt.GetProperty("emittedEvents").ValueKind != JsonValueKind.Array
                || receipt.GetProperty("warnings").ValueKind != JsonValueKind.Array) throw Error("plugin_commit_unknown");
            var rows = new Dictionary<(string Table, string Record), string>();
            foreach (var row in receipt.GetProperty("rows").EnumerateArray())
            {
                string table = row.GetProperty("tableId").GetString()!;
                string record = row.GetProperty("recordId").GetString()!;
                string kind = row.GetProperty("operation").GetString()!;
                if (string.IsNullOrEmpty(table) || string.IsNullOrEmpty(record) || kind is not ("insert" or "update" or "delete" or "archive" or "restore")
                    || string.IsNullOrEmpty(row.GetProperty("revision").GetString()) || string.IsNullOrEmpty(row.GetProperty("digest").GetString())
                    || !receipt.GetProperty("revisions").TryGetProperty(table, out var revision) || string.IsNullOrEmpty(revision.GetString())
                    || !rows.TryAdd((table, record), kind)) throw Error("plugin_commit_unknown");
            }
            foreach (var entity in result.GetProperty("newIds").EnumerateObject())
                foreach (var mapping in entity.Value.EnumerateObject())
                    if (string.IsNullOrEmpty(mapping.Value.GetString())
                        || !rows.Any(row => row.Value == "insert" && row.Key.Record == mapping.Value.GetString())) throw Error("plugin_commit_unknown");
            if (preview is not { } expected) return;
            if (!SameJson(result.GetProperty("newIds"), expected.GetProperty("newIds"))) throw Error("plugin_commit_unknown");
            if (rows.Count != expected.GetProperty("effects").GetArrayLength()) throw Error("plugin_commit_unknown");
            foreach (var effect in expected.GetProperty("effects").EnumerateArray())
                if (!rows.TryGetValue((effect.GetProperty("tableId").GetString()!, effect.GetProperty("recordId").GetString()!), out var kind)
                    || kind != effect.GetProperty("operation").GetString()) throw Error("plugin_commit_unknown");
            foreach (var op in expected.GetProperty("plan").GetProperty("operations").EnumerateArray())
            {
                string entity = op.GetProperty("entity").GetString()!;
                string kind = op.GetProperty("kind").GetString()!;
                string table = expected.GetProperty("model").GetProperty("bindings").GetProperty(entity).GetProperty("tableId").GetString()!;
                string record = kind == "create" ? expected.GetProperty("newIds").GetProperty(entity).GetProperty(op.GetProperty("tempId").GetString()!).GetString()!
                    : op.GetProperty("recordId").GetString()!;
                if (!rows.TryGetValue((table, record), out var actual) || actual != (kind == "create" ? "insert" : kind)) throw Error("plugin_commit_unknown");
            }
        }
        catch (Exception error) when (error is KeyNotFoundException or InvalidOperationException or FormatException or ArgumentException)
        { throw Error("plugin_commit_unknown"); }
    }
}
