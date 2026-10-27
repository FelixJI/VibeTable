using System.Collections.Concurrent;
using System.IO;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal sealed partial class PluginSurfaceApi
{
    internal const string OutputContract = "vibetable.plugin-output.v1";
    internal const int OutputMaxBytes = 32 * 1024 * 1024;
    private const int OutputBlockBytes = 256 * 1024;
    private readonly ConcurrentDictionary<string, ConcurrentDictionary<string, SurfaceOutput>> _outputs = new();
    private sealed class SurfaceOutput(string id, string name, string mediaType, int size, JsonElement source)
    {
        internal string Id { get; } = id;
        internal string Name { get; } = name;
        internal string MediaType { get; } = mediaType;
        internal int Size { get; } = size;
        internal JsonElement Source { get; } = source;
        internal DateTimeOffset Expires { get; } = DateTimeOffset.UtcNow.AddMinutes(10);
        internal MemoryStream Content { get; } = new();
        internal SemaphoreSlim Gate { get; } = new(1);
        internal CancellationTokenSource Lifetime { get; } = new();
        internal bool Sealed;
        internal readonly ConcurrentDictionary<string, JsonElement> Saves = new(StringComparer.Ordinal);
    }

    private async Task<object?> HandleOutputAsync(PluginSurfaceSession surface, SurfaceState state,
        string method, JsonElement parameters, CancellationToken token)
    {
        var owned = SurfaceMap(surface, state, _outputs);
        foreach (var expired in owned.Where(pair => pair.Value.Expires < DateTimeOffset.UtcNow))
            if (owned.TryRemove(expired.Key, out var old)) TrackCleanup(DisposeOutputAsync(old));
        if (method is "output.create" or "output.reopen")
        {
            if (method == "output.create") ExactFile(parameters, "contract", "suggestedName", "mediaType", "sizeBytes", "source");
            else ExactFile(parameters, "contract", "artifactId");
            if (parameters.GetProperty("contract").GetString() != OutputContract) throw Error("plugin_api_unsupported");
            string name, mediaType;
            int size;
            JsonElement source;
            JsonElement reopened = default;
            if (method == "output.reopen")
            {
                reopened = await CallAsync(surface, "output.open", JsonSerializer.SerializeToElement(new
                { artifactId = OutputText(parameters, "artifactId", 128) }), token);
                name = SafeFileName(reopened.GetProperty("displayName").GetString());
                mediaType = OutputText(reopened, "mediaType", 128);
                size = reopened.GetProperty("sizeBytes").GetInt32();
                source = OutputSource(reopened.GetProperty("source"));
            }
            else
            {
                name = SafeFileName(parameters.GetProperty("suggestedName").GetString());
                mediaType = OutputText(parameters, "mediaType", 128);
                size = parameters.GetProperty("sizeBytes").GetInt32();
                source = OutputSource(parameters.GetProperty("source"));
                await AuthorizeFileAsync(surface, true, token);
            }
            if (size is <= 0 or > OutputMaxBytes) throw Error("plugin_file_limit");
            string id = "output-" + Guid.NewGuid().ToString("N");
            var output = new SurfaceOutput(id, name, mediaType, size, source);
            if (!_surfaces.TryUseCurrent(surface, () =>
            {
                lock (owned)
                    return !state.Lifetime.IsCancellationRequested && owned.Count < 2 && owned.TryAdd(id, output);
            })) throw Error("plugin_file_limit");
            try
            {
                if (method == "output.reopen")
                {
                    using var reopening = CancellationTokenSource.CreateLinkedTokenSource(token, output.Lifetime.Token);
                    await output.Gate.WaitAsync(reopening.Token);
                    try
                    {
                        reopening.Token.ThrowIfCancellationRequested();
                        string artifactId = OutputText(parameters, "artifactId", 128);
                        for (int offset = 0; offset < size;)
                        {
                            var block = await CallAsync(surface, "output.read", JsonSerializer.SerializeToElement(new { artifactId, offset }), reopening.Token);
                            byte[] bytes = Convert.FromBase64String(block.GetProperty("base64").GetString()!);
                            if (bytes.Length is <= 0 or > OutputBlockBytes || offset + bytes.Length > size) throw Error("plugin_file_limit");
                            await output.Content.WriteAsync(bytes, reopening.Token);
                            offset += bytes.Length;
                            if (block.GetProperty("eof").GetBoolean() != (offset == size)) throw Error("plugin_capability_invalid");
                        }
                        reopening.Token.ThrowIfCancellationRequested();
                        output.Sealed = true;
                    }
                    finally { output.Gate.Release(); }
                }
                return new { outputId = id, contract = OutputContract, maxBytes = OutputMaxBytes, blockBytes = OutputBlockBytes, sizeBytes = size, source };
            }
            catch
            {
                owned.TryRemove(id, out _);
                await DisposeOutputAsync(output);
                throw;
            }
        }
        string outputId = OutputText(parameters, "outputId", 128);
        if (method == "output.release")
        {
            ExactFile(parameters, "outputId");
            if (owned.TryRemove(outputId, out var released))
            {
                released.Lifetime.Cancel();
                TrackCleanup(DisposeOutputAsync(released));
            }
            return new { released = true };
        }
        if (!owned.TryGetValue(outputId, out var active)) throw Error("plugin_file_denied");
        if (method == "output.status")
        {
            ExactFile(parameters, "outputId", "saveId");
            return active.Saves.TryGetValue(OutputText(parameters, "saveId", 128), out var receipt)
                ? receipt : throw Error("plugin_output_unknown");
        }
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(token, active.Lifetime.Token);
        await active.Gate.WaitAsync(linked.Token);
        try
        {
            linked.Token.ThrowIfCancellationRequested();
            if (method == "output.write")
            {
                ExactFile(parameters, "outputId", "offset", "base64");
                if (active.Sealed || parameters.GetProperty("offset").GetInt64() != active.Content.Length) throw Error("plugin_capability_invalid");
                string encoded = OutputText(parameters, "base64", ((OutputBlockBytes + 2) / 3) * 4);
                byte[] bytes = Convert.FromBase64String(encoded);
                if (bytes.Length is <= 0 or > OutputBlockBytes || active.Content.Length + bytes.Length > active.Size) throw Error("plugin_file_limit");
                await active.Content.WriteAsync(bytes, linked.Token);
                return new { offset = active.Content.Length };
            }
            if (method == "output.seal")
            {
                ExactFile(parameters, "outputId");
                if (active.Content.Length != active.Size) throw Error("plugin_capability_invalid");
                active.Sealed = true;
                return new { outputId, sealedOutput = true, source = active.Source };
            }
            if (!active.Sealed) throw Error("plugin_capability_invalid");
            if (method == "output.save") return await SaveOutputAsync(surface, state, active, parameters, linked.Token);
            if (method == "output.previewArchive") return await PreviewOutputArchiveAsync(surface, state, active, parameters, linked.Token);
            throw Error("plugin_api_unsupported");
        }
        finally { active.Gate.Release(); }
    }

    private async Task<JsonElement> SaveOutputAsync(PluginSurfaceSession surface, SurfaceState state,
        SurfaceOutput output, JsonElement parameters, CancellationToken token)
    {
        ExactFile(parameters, "outputId", "saveId");
        string saveId = OutputText(parameters, "saveId", 128);
        if (output.Saves.TryGetValue(saveId, out var prior)) return prior;
        if (_files is null || _fileHost is null || _pickFile is null) throw Error("plugin_api_unsupported");
        if (output.Saves.Count >= 16) throw Error("plugin_file_limit");
        await AuthorizeFileAsync(surface, true, token);
        var generation = _generation() ?? throw Error("plugin_session_stale");
        HostSessionFileBroker broker = _files();
        output.Saves[saveId] = OutputReceipt(output, saveId, "pending");
        string runId = "plugin-output-save-" + Guid.NewGuid().ToString("N");
        string? transferId = null;
        string? displayName = null;
        try
        {
            string? path = await _pickFile(() => _fileHost.SelectAttachmentTarget(output.Name));
            if (path is null) return output.Saves[saveId] = OutputReceipt(output, saveId, "cancelled");
            token.ThrowIfCancellationRequested();
            await AuthorizeFileAsync(surface, true, token);
            var grant = await broker.IssueAsync(path, true, runId, token, output.MediaType,
                action => generation.TryUseCurrent(() => _surfaces.TryUseCurrent(surface, () =>
                { if (state.Lifetime.IsCancellationRequested || output.Lifetime.IsCancellationRequested) return false; action(); return true; })),
                purpose: "output", byteLimit: OutputMaxBytes,
                authorize: current => AuthorizeFileAsync(surface, true, current));
            displayName = grant.GetProperty("displayName").GetString();
            var opened = await broker.HandleAsync("openWrite", JsonSerializer.SerializeToElement(new
            { grantId = grant.GetProperty("grantId").GetString(), runId }), token);
            transferId = opened.GetProperty("transferId").GetString()!;
            byte[] content = output.Content.GetBuffer();
            for (int offset = 0; offset < output.Size; offset += OutputBlockBytes)
                await broker.HandleAsync("write", JsonSerializer.SerializeToElement(new
                { transferId, offset, base64 = Convert.ToBase64String(content, offset, Math.Min(OutputBlockBytes, output.Size - offset)) }), token);
            await broker.HandleAsync("finishWrite", JsonSerializer.SerializeToElement(new { transferId, commit = true }), token);
            return output.Saves[saveId] = OutputReceipt(output, saveId, "succeeded", displayName);
        }
        catch (Exception error)
        {
            // The broker is the commit authority even if cancellation/cleanup hid its acknowledgement.
            if (transferId is not null && await broker.WasWriteCommittedAsync(transferId))
                return output.Saves[saveId] = OutputReceipt(output, saveId, "succeeded", displayName);
            string code = error is OperationCanceledException ? "plugin_cancel_requested"
                : error is SurfaceAuthorityException authority ? authority.Code
                : error is PluginSurfacePolicyException policy ? policy.Code : "plugin_output_save_failed";
            return output.Saves[saveId] = OutputReceipt(output, saveId,
                error is OperationCanceledException ? "cancelled" : "failed", displayName, code);
        }
        finally
        {
            try { await broker.RevokeRunAsync(runId); }
            catch (Exception error) { System.Diagnostics.Trace.TraceWarning($"Output grant cleanup failed: {error}"); }
        }
    }

    private async Task<object> PreviewOutputArchiveAsync(PluginSurfaceSession surface, SurfaceState state,
        SurfaceOutput output, JsonElement parameters, CancellationToken token)
    {
        ExactFile(parameters, "outputId", "collection", "recordId", "fieldId", "expectedDigest");
        if (surface.Risk == "read") throw Error("plugin_write_denied");
        if (state.Plans.Count >= 8) throw Error("plugin_plan_limit");
        string collection = OutputText(parameters, "collection", 128), record = OutputText(parameters, "recordId", 128), field = OutputText(parameters, "fieldId", 128);
        string handle = "plugin-output-upload-" + Guid.NewGuid().ToString("N");
        var generation = _generation() ?? throw Error("plugin_session_stale");
        bool handedOff = false;
        try
        {
            await CallAsync(surface, "output.stage.begin", JsonSerializer.SerializeToElement(new
            { handle, artifactId = output.Id, collection, recordId = record, fieldId = field,
                name = output.Name, mediaType = output.MediaType, sizeBytes = output.Size, source = output.Source }), token);
            byte[] bytes = output.Content.GetBuffer();
            for (int offset = 0; offset < output.Size; offset += OutputBlockBytes)
                await CallAsync(surface, "output.stage.write", JsonSerializer.SerializeToElement(new
                { handle, offset, base64 = Convert.ToBase64String(bytes, offset, Math.Min(OutputBlockBytes, output.Size - offset)) }), token);
            await CallAsync(surface, "output.stage.finish", JsonSerializer.SerializeToElement(new { handle }), token);
            var description = await CallAsync(surface, "data.describe", JsonSerializer.SerializeToElement(new { accepts = new[] { "vibetable.plugin-data.v2" }, collection }), token);
            var plan = JsonSerializer.SerializeToElement(new
            {
                contract = "vibetable.surface-mutation-plan.v1", collection,
                schemaRevision = description.GetProperty("schemaRevision").GetString(),
                operations = new[] { new { kind = "setAttachments", primaryKey = record, fieldId = field,
                    expectedDigest = parameters.GetProperty("expectedDigest").GetString(), uploadHandles = new[] { handle }, removeStoredNames = Array.Empty<string>() } }
            });
            await CallAsync(surface, "mutation.preview", plan, token);
            string planId = Guid.NewGuid().ToString("N");
            if (!_surfaces.TryUseCurrent(surface, () => !state.Lifetime.IsCancellationRequested && state.Plans.Count < 8
                && state.Plans.TryAdd(planId, new Plan(plan, 1, DateTimeOffset.UtcNow.AddMinutes(2), Attachments: new(generation, [handle])))))
                throw Error("plugin_session_stale");
            handedOff = true;
            return new { planId, affectedCount = 1, artifactId = output.Id, source = output.Source };
        }
        finally
        {
            if (!handedOff) await DropAttachmentStagesAsync(surface, new(generation, [handle]));
        }
    }

    private static JsonElement OutputReceipt(SurfaceOutput output, string saveId, string state,
        string? name = null, string? code = null) => JsonSerializer.SerializeToElement(new
        { outputId = output.Id, saveId, state, committed = state == "succeeded", bytes = state == "succeeded" ? output.Size : 0,
            displayName = name ?? output.Name, source = output.Source, error = code is null ? null : new { code } });
    private static string OutputText(JsonElement value, string key, int maximum)
        => value.GetProperty(key) is { ValueKind: JsonValueKind.String } text && !string.IsNullOrWhiteSpace(text.GetString())
            && text.GetString()!.Length <= maximum ? text.GetString()! : throw Error("plugin_capability_invalid");
    private static JsonElement OutputSource(JsonElement source)
    {
        ExactFile(source, "templateVersion", "dataVersion", "purpose");
        foreach (string key in new[] { "templateVersion", "dataVersion", "purpose" }) OutputText(source, key, 256);
        return source.Clone();
    }
    private void ReleaseOutputs(string token)
    {
        if (!_outputs.TryRemove(token, out var outputs)) return;
        foreach (var output in outputs.Values)
        { output.Lifetime.Cancel(); TrackCleanup(DisposeOutputAsync(output)); }
    }
    private static async Task DisposeOutputAsync(SurfaceOutput output)
    {
        output.Lifetime.Cancel();
        await output.Gate.WaitAsync();
        try { output.Content.Dispose(); }
        finally { output.Gate.Release(); }
    }
}
