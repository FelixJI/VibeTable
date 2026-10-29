using System.Text;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal sealed partial class PluginSurfaceApi
{
    private const int DatasetBytes = 512 * 1024;
    private readonly object _datasetGate = new();
    private readonly Timer _datasetExpiry;
    private sealed record Dataset(JsonElement Content, JsonElement Validation, DateTimeOffset Expires, string RequestId);

    private async Task<(string Handle, object Result)> OpenDatasetAsync(PluginSurfaceSession surface, SurfaceState state, string requestId, JsonElement parameters, CancellationToken token)
    {
        ExactFile(parameters, "contract", "sources");
        lock (_datasetGate)
            if (state.Datasets.Count >= 2 || _states.Values.Sum(item => item.Datasets.Count) >= 8) throw Error("plugin_dataset_limit");
        JsonElement content = await CallAsync(surface, "dataset.open", parameters, token);
        if (Encoding.UTF8.GetByteCount(content.GetRawText()) > DatasetBytes || content.GetProperty("contract").GetString() != "vibetable.plugin-dataset.v1") throw Error("plugin_dataset_limit");
        var validation = JsonSerializer.SerializeToElement(new { contract = "vibetable.plugin-dataset.v1", sources = content.GetProperty("scope"), versions = content.GetProperty("versions") });
        var summaries = content.GetProperty("sources").EnumerateArray().Select(item => new
        { collection = item.GetProperty("collection"), fields = item.GetProperty("fields"), filteredRows = item.GetProperty("filteredRows"), totalRows = item.GetProperty("totalRows"), complete = item.GetProperty("complete") }).ToArray();
        string handle = Guid.NewGuid().ToString("N");
        var dataset = new Dataset(content, validation, DateTimeOffset.UtcNow.AddMinutes(5), requestId);
        var generation = _generation() ?? throw Error("plugin_session_stale");
        if (!generation.TryUseCurrent(() => _surfaces.TryUseCurrent(surface, () =>
        {
            lock (_datasetGate)
            {
                token.ThrowIfCancellationRequested();
                if (state.Lifetime.IsCancellationRequested) return false;
                if (state.Datasets.Count >= 2 || _states.Values.Sum(item => item.Datasets.Count) >= 8) throw Error("plugin_dataset_limit");
                return state.Datasets.TryAdd(handle, dataset);
            }
        }))) throw Error("plugin_session_stale");
        return (handle, new { contract = "vibetable.plugin-dataset.v1", datasetId = handle, versions = content.GetProperty("versions"), sources = summaries, expiresAt = dataset.Expires });
    }

    private void SweepDatasets()
    {
        foreach (var state in _states.Values)
            foreach (var pair in state.Datasets)
                if (pair.Value.Expires <= DateTimeOffset.UtcNow) state.Datasets.TryRemove(pair.Key, out _);
    }

    private async Task ValidateDatasetAsync(PluginSurfaceSession surface, SurfaceState state, string handle, Dataset dataset, CancellationToken token)
    {
        try
        {
            if (dataset.Expires <= DateTimeOffset.UtcNow || !state.Datasets.TryGetValue(handle, out var current) || !ReferenceEquals(current, dataset)) throw Error("plugin_dataset_stale");
            await CallAsync(surface, "dataset.validate", dataset.Validation, token);
            token.ThrowIfCancellationRequested();
            if (!state.Datasets.TryGetValue(handle, out current) || !ReferenceEquals(current, dataset)) throw Error("plugin_dataset_stale");
        }
        catch { state.Datasets.TryRemove(handle, out _); throw; }
    }

    private async Task<object?> UseDatasetAsync(PluginSurfaceSession surface, SurfaceState state, string method, JsonElement parameters, CancellationToken token)
    {
        if (method == "dataset.page") ExactFile(parameters, "datasetId", "collection", "offset", "pageSize");
        else if (method == "dataset.export") ExactFile(parameters, "datasetId", "collection", "suggestedName");
        else ExactFile(parameters, "datasetId", "collection");
        string handle = parameters.GetProperty("datasetId").GetString() ?? throw Error("plugin_capability_invalid");
        if (!state.Datasets.TryGetValue(handle, out var dataset)) throw Error("plugin_dataset_stale");
        await ValidateDatasetAsync(surface, state, handle, dataset, token);
        string collection = parameters.GetProperty("collection").GetString() ?? throw Error("plugin_capability_invalid");
        JsonElement source = dataset.Content.GetProperty("sources").EnumerateArray().FirstOrDefault(item => item.GetProperty("collection").GetString() == collection);
        if (source.ValueKind != JsonValueKind.Object) throw Error("plugin_read_denied");
        if (method == "dataset.summary") return new { datasetId = handle, collection, versions = dataset.Content.GetProperty("versions"), complete = source.GetProperty("summary").GetProperty("complete"), summary = source.GetProperty("summary") };
        if (method == "dataset.export") return await ExportDatasetAsync(surface, state, handle, dataset, source, parameters, token);
        int offset = parameters.GetProperty("offset").GetInt32(), size = parameters.GetProperty("pageSize").GetInt32();
        var rows = source.GetProperty("items");
        if (offset < 0 || offset > rows.GetArrayLength() || size < 1 || size > 200) throw Error("plugin_capability_invalid");
        int end = Math.Min(offset + size, rows.GetArrayLength());
        return new { datasetId = handle, collection, items = rows.EnumerateArray().Skip(offset).Take(size), nextOffset = end == rows.GetArrayLength() ? (int?)null : end, versions = dataset.Content.GetProperty("versions"), complete = source.GetProperty("complete") };
    }

    private async Task<object?> ExportDatasetAsync(PluginSurfaceSession surface, SurfaceState state, string handle, Dataset dataset, JsonElement source, JsonElement parameters, CancellationToken token)
    {
        if (!source.GetProperty("complete").GetBoolean()) throw Error("plugin_dataset_pending");
        var picked = await HandleFileAsync(surface, state, "file.pickWrite", JsonSerializer.SerializeToElement(new
        { suggestedName = parameters.GetProperty("suggestedName").GetString(), mediaType = "text/csv" }), token);
        if (picked is null) return null;
        string grant = JsonSerializer.SerializeToElement(picked).GetProperty("grantId").GetString()!;
        if (!_surfaceFiles.TryGetValue(surface.SurfaceToken, out var ownedFiles) || !ownedFiles.TryGetValue(grant, out var file)) throw Error("plugin_session_stale");
        try
        {
            var fields = source.GetProperty("fields").EnumerateArray().Select(item => item.GetString()!).ToArray();
            static string Csv(string text) => "\"" + text.Replace("\"", "\"\"", StringComparison.Ordinal) + "\"";
            var csv = new StringBuilder(string.Join(",", fields.Select(Csv))).Append("\r\n");
            foreach (var row in source.GetProperty("items").EnumerateArray())
            {
                csv.AppendJoin(",", fields.Select(field =>
                {
                    var cell = row.GetProperty(field);
                    if (cell.ValueKind == JsonValueKind.Object && cell.TryGetProperty("fresh", out _)) cell = cell.GetProperty("value");
                    return Csv(cell.ValueKind == JsonValueKind.String ? cell.GetString()! : cell.GetRawText());
                })).Append("\r\n");
            }
            byte[] bytes = Encoding.UTF8.GetBytes(csv.ToString());
            // Reuse ordinary file grants and the existing atomic replacement boundary.
            for (int offset = 0; offset < bytes.Length; offset += 256 * 1024)
            {
                await ValidateDatasetAsync(surface, state, handle, dataset, token);
                int count = Math.Min(256 * 1024, bytes.Length - offset);
                await HandleFileAsync(surface, state, "file.write", JsonSerializer.SerializeToElement(new
                { grantId = grant, offset, base64 = Convert.ToBase64String(bytes, offset, count), finish = offset + count == bytes.Length }), token);
            }
            return JsonSerializer.SerializeToElement(new { committed = true, datasetId = handle, versions = dataset.Content.GetProperty("versions"), rowsWritten = source.GetProperty("items").GetArrayLength() });
        }
        finally { await RevokeSurfaceFileAsync(file); }
    }
}
