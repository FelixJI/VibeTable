using System.Collections.Concurrent;
using System.IO;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal sealed partial class PluginSurfaceApi
{
    private sealed record AttachmentRef(string Collection, string RecordId, string FieldId,
        string StoredName, string Name, DateTimeOffset Expires);
    private sealed record AttachmentStages(ProductSidecarGenerationSnapshot Generation, string[] Handles);
    private readonly ConcurrentDictionary<string, ConcurrentDictionary<string, AttachmentRef>> _attachmentRefs = new();

    private async Task<object?> HandleAttachmentAsync(PluginSurfaceSession surface, SurfaceState state,
        string method, JsonElement parameters, CancellationToken token)
    {
        var refs = SurfaceMap(surface, state, _attachmentRefs);
        foreach (var expired in refs.Where(pair => pair.Value.Expires < DateTimeOffset.UtcNow)) refs.TryRemove(expired.Key, out _);
        if (method == "attachments.read")
        {
            ExactFile(parameters, "attachmentId", "offset");
            string id = parameters.GetProperty("attachmentId").GetString() ?? throw Error("plugin_capability_invalid");
            if (!refs.TryGetValue(id, out var attachment)) throw Error("plugin_file_denied");
            return await CallAsync(surface, "attachment.read", JsonSerializer.SerializeToElement(new
            {
                collection = attachment.Collection,
                recordId = attachment.RecordId,
                fieldId = attachment.FieldId,
                storedName = attachment.StoredName,
                offset = parameters.GetProperty("offset").GetInt64()
            }), token);
        }
        if (method == "attachments.list")
        {
            ExactFile(parameters, "collection", "recordId", "fieldId");
            JsonElement response = await CallAsync(surface, "attachment.list", parameters, token);
            var items = response.GetProperty("items");
            var output = new List<object>();
            if (!_surfaces.TryUseCurrent(surface, () =>
            {
                if (state.Lifetime.IsCancellationRequested) return false;
                var pending = new List<(string Id, AttachmentRef Reference, JsonElement Item)>();
                foreach (var item in items.EnumerateArray())
                {
                    var reference = new AttachmentRef(parameters.GetProperty("collection").GetString()!, parameters.GetProperty("recordId").GetString()!,
                        parameters.GetProperty("fieldId").GetString()!, item.GetProperty("storedName").GetString()!,
                        item.GetProperty("displayName").GetString()!, DateTimeOffset.UtcNow.AddMinutes(5));
                    string? existing = refs.FirstOrDefault(pair => pair.Value.Collection == reference.Collection && pair.Value.RecordId == reference.RecordId
                        && pair.Value.FieldId == reference.FieldId && pair.Value.StoredName == reference.StoredName).Key;
                    pending.Add((existing ?? Guid.NewGuid().ToString("N"), reference, item));
                }
                if (refs.Count + pending.Count(value => !refs.ContainsKey(value.Id)) > 64) throw Error("plugin_file_limit");
                foreach (var value in pending)
                {
                    refs[value.Id] = value.Reference;
                    output.Add(new
                    {
                        attachmentId = value.Id,
                        displayName = value.Item.GetProperty("displayName").GetString(),
                        mediaType = value.Item.GetProperty("mediaType").GetString(),
                        sizeBytes = value.Item.GetProperty("sizeBytes").GetInt64()
                    });
                }
                return true;
            })) throw Error("plugin_session_stale");
            return new { items = output };
        }
        ExactFile(parameters, "collection", "recordId", "fieldId", "expectedDigest", "removeAttachmentIds", "addFile");
        if (surface.Risk == "read" || _files is null || _fileHost is null || _pickFile is null) throw Error("plugin_write_denied");
        string collection = parameters.GetProperty("collection").GetString()!;
        string record = parameters.GetProperty("recordId").GetString()!;
        string field = parameters.GetProperty("fieldId").GetString()!;
        var removeIds = parameters.GetProperty("removeAttachmentIds");
        if (removeIds.ValueKind != JsonValueKind.Array || removeIds.GetArrayLength() > 8) throw Error("plugin_capability_invalid");
        var removed = new List<string>();
        foreach (var id in removeIds.EnumerateArray())
        {
            if (!refs.TryGetValue(id.GetString()!, out var owned) || owned.Collection != collection || owned.RecordId != record || owned.FieldId != field)
                throw Error("plugin_file_denied");
            removed.Add(owned.StoredName);
        }
        var generation = _generation() ?? throw Error("plugin_session_stale");
        var handles = new List<string>();
        HostSessionFileBroker broker = _files();
        string runId = "plugin-attachment-" + Guid.NewGuid().ToString("N");
        try
        {
            if (parameters.GetProperty("addFile").GetBoolean())
            {
                await AuthorizeFileAsync(surface, false, token);
                // Check the canonical field/record grant before showing a native picker.
                await CallAsync(surface, "attachment.list", JsonSerializer.SerializeToElement(new { collection, recordId = record, fieldId = field }), token);
                string? path = await _pickFile(() => _fileHost.SelectAttachmentSources(replacement: true).Paths.SingleOrDefault());
                if (path is null) return null;
                var descriptor = await broker.IssueAsync(path, false, runId, token, ownerCommit: action =>
                    generation.TryUseCurrent(() => _surfaces.TryUseCurrent(surface, () =>
                    { if (state.Lifetime.IsCancellationRequested) return false; action(); return true; })));
                string grantId = descriptor.GetProperty("grantId").GetString()!;
                var opened = await broker.HandleAsync("openRead", JsonSerializer.SerializeToElement(new { grantId, runId }), token);
                string transferId = opened.GetProperty("transferId").GetString()!;
                using var content = new MemoryStream();
                try
                {
                    for (; ; )
                    {
                        await AuthorizeFileAsync(surface, false, token);
                        var block = await broker.HandleAsync("read", JsonSerializer.SerializeToElement(new { transferId, maxBytes = 256 * 1024 }), token);
                        byte[] bytes = Convert.FromBase64String(block.GetProperty("base64").GetString()!);
                        if (content.Length + bytes.Length > 1024 * 1024) throw Error("plugin_file_limit");
                        content.Write(bytes);
                        if (block.GetProperty("eof").GetBoolean()) break;
                    }
                }
                finally { await broker.HandleAsync("closeRead", JsonSerializer.SerializeToElement(new { transferId }), CancellationToken.None); }
                string handle = "plugin-upload-" + Guid.NewGuid().ToString("N");
                handles.Add(handle); // Remember before send: a lost stage reply must still be dropped.
                await CallAsync(surface, "attachment.stage", JsonSerializer.SerializeToElement(new
                {
                    collection,
                    recordId = record,
                    fieldId = field,
                    handle,
                    fileGrantId = grantId,
                    runId,
                    name = descriptor.GetProperty("displayName").GetString(),
                    base64 = Convert.ToBase64String(content.ToArray())
                }), token);
            }
            if (handles.Count == 0 && removed.Count == 0) throw Error("plugin_capability_invalid");
            if (state.Plans.Count >= 8) throw Error("plugin_plan_limit");
            var plan = JsonSerializer.SerializeToElement(new
            {
                contract = "vibetable.surface-mutation-plan.v1",
                collection,
                schemaRevision = (await CallAsync(surface, "data.describe", JsonSerializer.SerializeToElement(new
                { accepts = new[] { "vibetable.plugin-data.v2" }, collection }), token)).GetProperty("schemaRevision").GetString(),
                operations = new[] { new { kind = "setAttachments", primaryKey = record, expectedDigest = parameters.GetProperty("expectedDigest").GetString(),
                    fieldId = field, uploadHandles = handles.ToArray(), removeStoredNames = removed.ToArray() } }
            });
            await CallAsync(surface, "mutation.preview", plan, token);
            string planId = Guid.NewGuid().ToString("N");
            var cleanup = new AttachmentStages(generation, handles.ToArray());
            if (!_surfaces.TryUseCurrent(surface, () => !state.Lifetime.IsCancellationRequested && state.Plans.Count < 8
                && state.Plans.TryAdd(planId, new Plan(plan, 1, DateTimeOffset.UtcNow.AddMinutes(2), Attachments: cleanup)))) throw Error("plugin_session_stale");
            handles.Clear();
            return new { planId, affectedCount = 1 };
        }
        finally
        {
            await broker.RevokeRunAsync(runId);
            if (handles.Count > 0) await DropAttachmentStagesAsync(surface, new(generation, handles.ToArray()));
        }
    }
    private async Task DropAttachmentStagesAsync(PluginSurfaceSession surface, AttachmentStages? stages)
    {
        if (stages is null || stages.Handles.Length == 0) return;
        try
        {
            using var deadline = new CancellationTokenSource(TimeSpan.FromSeconds(5));
            await CallAsync(surface, "attachment.drop", JsonSerializer.SerializeToElement(new { handles = stages.Handles }), deadline.Token, auditGeneration: stages.Generation);
        }
        catch (Exception error) { System.Diagnostics.Trace.TraceWarning($"Plugin attachment stage cleanup unavailable: {error}"); }
    }
}
