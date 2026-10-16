using System.Collections.Concurrent;
using System.IO;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal sealed partial class PluginSurfaceApi
{
    private readonly Func<HostSessionFileBroker>? _files;
    private readonly INativeProductFileHost? _fileHost;
    private readonly Func<Func<string?>, Task<string?>>? _pickFile;
    private readonly ConcurrentDictionary<string, ConcurrentDictionary<string, SurfaceFile>> _surfaceFiles = new();
    private sealed class SurfaceFile(HostSessionFileBroker broker, string runId, string grantId, bool write, string surfaceToken)
    {
        internal HostSessionFileBroker Broker { get; } = broker;
        internal string RunId { get; } = runId;
        internal string GrantId { get; } = grantId;
        internal bool Write { get; } = write;
        internal string SurfaceToken { get; } = surfaceToken;
        internal SemaphoreSlim Gate { get; } = new(1);
        internal string? Transfer { get; set; }
        internal bool Complete { get; set; }
    }

    private ConcurrentDictionary<string, T> SurfaceMap<T>(PluginSurfaceSession surface, SurfaceState state,
        ConcurrentDictionary<string, ConcurrentDictionary<string, T>> maps)
    {
        ConcurrentDictionary<string, T>? map = null;
        if (!_surfaces.TryUseCurrent(surface, () =>
        {
            if (state.Lifetime.IsCancellationRequested || !_states.TryGetValue(surface.SurfaceToken, out var current) || !ReferenceEquals(current, state)) return false;
            map = maps.GetOrAdd(surface.SurfaceToken, _ => new(StringComparer.Ordinal)); return true;
        })) throw Error("plugin_session_stale");
        return map!;
    }

    private async Task<object?> HandleFileAsync(PluginSurfaceSession surface, SurfaceState state,
        string method, JsonElement parameters, CancellationToken token)
    {
        if (_files is null || _fileHost is null || _pickFile is null) throw Error("plugin_api_unsupported");
        var owned = SurfaceMap(surface, state, _surfaceFiles);
        if (method is "file.pickRead" or "file.pickWrite")
        {
            bool write = method == "file.pickWrite";
            string suggested = "plugin-output.bin";
            if (write)
            {
                ExactFile(parameters, "suggestedName", "mediaType");
                suggested = SafeFileName(parameters.GetProperty("suggestedName").GetString());
                if (parameters.GetProperty("mediaType").ValueKind != JsonValueKind.String
                    || parameters.GetProperty("mediaType").GetString()!.Length > 128) throw Error("plugin_capability_invalid");
            }
            else
            {
                ExactFile(parameters, "mediaTypes");
                var media = parameters.GetProperty("mediaTypes");
                if (media.ValueKind != JsonValueKind.Array || media.GetArrayLength() > 16
                    || media.EnumerateArray().Any(item => item.ValueKind != JsonValueKind.String || item.GetString()!.Length > 128)) throw Error("plugin_capability_invalid");
            }
            await AuthorizeFileAsync(surface, write, token);
            if (owned.Count >= 8) throw Error("plugin_file_limit");
            var generation = _generation() ?? throw Error("plugin_session_stale");
            HostSessionFileBroker broker = _files();
            // The captured principal and broker precede the modal picker, which pumps messages.
            string? path = await _pickFile(() => write ? _fileHost.SelectAttachmentTarget(suggested)
                : _fileHost.SelectAttachmentSources(replacement: true).Paths.SingleOrDefault());
            if (path is null) return null;
            token.ThrowIfCancellationRequested();
            await AuthorizeFileAsync(surface, write, token);
            string runId = "plugin-file-" + Guid.NewGuid().ToString("N");
            JsonElement descriptor = await broker.IssueAsync(path, write, runId, token,
                write ? parameters.GetProperty("mediaType").GetString() : "application/octet-stream",
                action => generation.TryUseCurrent(() => _surfaces.TryUseCurrent(surface, () =>
                { if (state.Lifetime.IsCancellationRequested) return false; action(); return true; }))).ConfigureAwait(false);
            string id = descriptor.GetProperty("grantId").GetString()!;
            var file = new SurfaceFile(broker, runId, id, write, surface.SurfaceToken);
            // Retire may race the await; no orphan grant may enter a replacement surface.
            if (!generation.TryUseCurrent(() => _surfaces.TryUseCurrent(surface, () =>
                !state.Lifetime.IsCancellationRequested && owned.Count < 8 && owned.TryAdd(id, file))))
            { await broker.RevokeRunAsync(runId); throw Error("plugin_session_stale"); }
            return new { grantId = id, displayName = descriptor.GetProperty("displayName").GetString(), mediaType = descriptor.GetProperty("mimeType").GetString() };
        }
        string grantId = parameters.TryGetProperty("grantId", out var grant) && grant.ValueKind == JsonValueKind.String
            ? grant.GetString()! : throw Error("plugin_capability_invalid");
        if (method == "file.release")
        {
            ExactFile(parameters, "grantId");
            if (owned.TryRemove(grantId, out var released)) await released.Broker.RevokeRunAsync(released.RunId);
            return new { released = true };
        }
        if (!owned.TryGetValue(grantId, out var active)) throw Error("plugin_file_denied");
        await active.Gate.WaitAsync(token);
        bool submitting = false;
        try
        {
            bool writing = method == "file.write";
            if (active.Write != writing || active.Complete) throw Error("plugin_file_denied");
            await AuthorizeFileAsync(surface, writing, token);
            if (active.Transfer is null)
            {
                var opened = await active.Broker.HandleAsync(writing ? "openWrite" : "openRead",
                    JsonSerializer.SerializeToElement(new { grantId, runId = active.RunId }), token);
                active.Transfer = opened.GetProperty("transferId").GetString()!;
            }
            if (!writing)
            {
                ExactFile(parameters, "grantId");
                var block = await active.Broker.HandleAsync("read", JsonSerializer.SerializeToElement(new
                { transferId = active.Transfer, maxBytes = 256 * 1024 }), token);
                if (block.GetProperty("eof").GetBoolean())
                {
                    await active.Broker.HandleAsync("closeRead", JsonSerializer.SerializeToElement(new { transferId = active.Transfer }), token);
                    active.Complete = true;
                }
                return block;
            }
            ExactFile(parameters, "grantId", "offset", "base64", "finish");
            var result = await active.Broker.HandleAsync("write", JsonSerializer.SerializeToElement(new
            { transferId = active.Transfer, offset = parameters.GetProperty("offset").GetInt64(), base64 = parameters.GetProperty("base64").GetString() }), token);
            if (!parameters.GetProperty("finish").GetBoolean()) return result;
            await AuthorizeFileAsync(surface, true, token);
            // Go authorization precedes the synchronous native generation/surface commit gate;
            // Go revocation and OS replacement are not a cross-system atomic transaction.
            submitting = true;
            var receipt = await active.Broker.HandleAsync("finishWrite", JsonSerializer.SerializeToElement(new
            { transferId = active.Transfer, commit = true }), token);
            active.Complete = true;
            return receipt;
        }
        catch
        {
            owned.TryRemove(grantId, out _);
            await RevokeSurfaceFileAsync(active);
            if (submitting) throw Error("plugin_commit_unknown");
            throw;
        }
        finally { active.Gate.Release(); }
    }

    private Task<JsonElement> AuthorizeFileAsync(PluginSurfaceSession surface, bool write, CancellationToken token)
        => CallAsync(surface, "file.authorize", JsonSerializer.SerializeToElement(new { operation = write ? "pickWrite" : "pickRead" }), token);

    private void ReleaseFiles(string surfaceToken)
    {
        if (!_surfaceFiles.TryRemove(surfaceToken, out var files)) return;
        foreach (var file in files.Values) TrackCleanup(RevokeSurfaceFileAsync(file));
    }
    private async Task RevokeSurfaceFileAsync(SurfaceFile file)
    {
        if (_surfaceFiles.TryGetValue(file.SurfaceToken, out var owned)) owned.TryRemove(file.GrantId, out _);
        try { await file.Broker.RevokeRunAsync(file.RunId).ConfigureAwait(false); }
        catch (Exception error) { System.Diagnostics.Trace.TraceError($"Plugin file cleanup failed: {error}"); }
    }
    private static void ExactFile(JsonElement parameters, params string[] keys)
    {
        if (parameters.ValueKind != JsonValueKind.Object || parameters.EnumerateObject().Count() != keys.Length
            || parameters.EnumerateObject().Any(item => !keys.Contains(item.Name, StringComparer.Ordinal))) throw Error("plugin_capability_invalid");
    }
    private static string SafeFileName(string? name)
    {
        if (string.IsNullOrWhiteSpace(name) || name.Length > 255 || name != Path.GetFileName(name)
            || name.IndexOfAny(Path.GetInvalidFileNameChars()) >= 0) throw Error("plugin_capability_invalid");
        return name;
    }
}
