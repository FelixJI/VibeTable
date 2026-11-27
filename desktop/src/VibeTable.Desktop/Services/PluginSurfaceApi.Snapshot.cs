using System.Text;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>
/// #590 data.openSnapshot Host side. Go materializes every source inside one
/// transaction and answers with the bounded in-memory corpus; the Host keeps
/// that corpus per surface (2 handles per surface, 8 total, 5 minute absolute
/// expiry), re-validates it with Go before every page and every cross-source
/// batch (snapshot.validate: session, current authorization, schema revisions of
/// every source, exactly once per request) and then slices rows out of the
/// immutable corpus at that authorization point. Plain data writes never touch a
/// handle; nothing is served from a handle the authority stopped endorsing.
/// </summary>
internal sealed partial class PluginSurfaceApi
{
    private const int SnapshotBytes = 512 * 1024;
    private const string SnapshotContract = "vibetable.plugin-snapshot.v1";
    /// <summary>One snapshot.readBatch crosses sources but carries at most this many rows (empty sources ride along).</summary>
    private const int SnapshotBatchRows = 200;
    /// <summary>Item JSON budget of one batch across source boundaries; only the batch's very first row is unconditional.</summary>
    private const int SnapshotBatchBytes = 256 * 1024;
    private readonly object _snapshotGate = new();
    /// <summary>In-flight snapshot.open reservations of every surface, published or being retired alike.</summary>
    private int _snapshotReservations;
    private sealed record Snapshot(JsonElement Content, JsonElement Validation, DateTimeOffset Expires, string RequestId);

    /// <summary>
    /// Reserves snapshot capacity before the authority starts materializing: the per-surface
    /// (2) and total (8) limits count published handles plus in-flight opens, so parallel opens
    /// can never make the authority build a corpus the Host would refuse to publish. The
    /// reservation becomes the published handle under the same gate (PublishSnapshot) or is
    /// handed back by ReleaseSnapshotOpen from the open's finally.
    /// </summary>
    private void ReserveSnapshotOpen(SurfaceState state)
    {
        lock (_snapshotGate)
        {
            if (state.Snapshots.Count + state.SnapshotOpens >= 2 || _states.Values.Sum(item => item.Snapshots.Count) + _snapshotReservations >= 8) throw Error("plugin_snapshot_limit");
            state.SnapshotOpens++;
            _snapshotReservations++;
        }
    }

    private bool PublishSnapshot(SurfaceState state, string handle, Snapshot snapshot, CancellationToken token)
    {
        lock (_snapshotGate)
        {
            token.ThrowIfCancellationRequested();
            if (state.Lifetime.IsCancellationRequested) return false;
            if (!state.Snapshots.TryAdd(handle, snapshot)) return false;
            state.SnapshotOpens--;
            _snapshotReservations--;
            return true;
        }
    }

    private void ReleaseSnapshotOpen(SurfaceState state)
    {
        lock (_snapshotGate) { state.SnapshotOpens--; _snapshotReservations--; }
    }

    private async Task<(string Handle, object Result)> OpenSnapshotAsync(PluginSurfaceSession surface, SurfaceState state, string requestId, JsonElement parameters, CancellationToken token)
    {
        ExactFile(parameters, "contract", "sources");
        ReserveSnapshotOpen(state);
        bool published = false;
        try
        {
            JsonElement content = await CallAsync(surface, "snapshot.open", parameters, token);
            if (Encoding.UTF8.GetByteCount(content.GetRawText()) > SnapshotBytes || content.GetProperty("contract").GetString() != SnapshotContract) throw Error("plugin_snapshot_limit");
            // The validation request replays the plugin's own sources plus the version vector
            // Go produced; Go re-authorizes the projections and compares schema revisions.
            var validation = JsonSerializer.SerializeToElement(new { contract = SnapshotContract, sources = parameters.GetProperty("sources"), versions = content.GetProperty("versions") });
            var sources = content.GetProperty("sources").EnumerateArray().Select(item => new
            { collection = item.GetProperty("collection"), fields = item.GetProperty("fields"), computedFields = item.GetProperty("computedFields"), filteredRows = item.GetProperty("filteredRows"), totalRows = item.GetProperty("totalRows"), complete = item.GetProperty("complete") }).ToArray();
            string handle = Guid.NewGuid().ToString("N");
            var snapshot = new Snapshot(content, validation, DateTimeOffset.UtcNow.AddMinutes(5), requestId);
            var generation = _generation() ?? throw Error("plugin_session_stale");
            if (!generation.TryUseCurrent(() => _surfaces.TryUseCurrent(surface, () => published = PublishSnapshot(state, handle, snapshot, token)))) throw Error("plugin_session_stale");
            return (handle, new { contract = SnapshotContract, snapshotId = handle, versions = content.GetProperty("versions"), sources, expiresAt = snapshot.Expires });
        }
        finally { if (!published) ReleaseSnapshotOpen(state); }
    }

    private void SweepSnapshots()
    {
        foreach (var state in _states.Values)
            foreach (var pair in state.Snapshots)
                if (pair.Value.Expires <= DateTimeOffset.UtcNow) state.Snapshots.TryRemove(pair.Key, out _);
    }

    private async Task ValidateSnapshotAsync(PluginSurfaceSession surface, SurfaceState state, string handle, Snapshot snapshot, CancellationToken token)
    {
        try
        {
            if (snapshot.Expires <= DateTimeOffset.UtcNow || !state.Snapshots.TryGetValue(handle, out var current) || !ReferenceEquals(current, snapshot)) throw Error("plugin_snapshot_stale");
            await CallAsync(surface, "snapshot.validate", snapshot.Validation, token);
            token.ThrowIfCancellationRequested();
            if (!state.Snapshots.TryGetValue(handle, out current) || !ReferenceEquals(current, snapshot)) throw Error("plugin_snapshot_stale");
        }
        catch { state.Snapshots.TryRemove(handle, out _); throw; }
    }

    private async Task<object?> UseSnapshotAsync(PluginSurfaceSession surface, SurfaceState state, string method, JsonElement parameters, CancellationToken token)
    {
        bool batch = method == "snapshot.readBatch";
        if (!batch && method != "snapshot.page") throw Error("plugin_api_unsupported");
        if (batch) ExactFile(parameters, "snapshotId", "next");
        else ExactFile(parameters, "snapshotId", "collection", "offset", "pageSize");
        string handle = parameters.GetProperty("snapshotId").GetString() ?? throw Error("plugin_capability_invalid");
        if (!state.Snapshots.TryGetValue(handle, out var snapshot)) throw Error("plugin_snapshot_stale");
        // Exactly one authority round trip per request; the slices below come from the very
        // corpus this validation endorsed. Any rejection (authority, cancel, timeout) drops the handle.
        await ValidateSnapshotAsync(surface, state, handle, snapshot, token);
        var sources = snapshot.Content.GetProperty("sources").EnumerateArray().ToArray();
        if (batch) return ReadSnapshotBatch(snapshot, handle, sources, parameters.GetProperty("next"));
        string collection = parameters.GetProperty("collection").GetString() ?? throw Error("plugin_capability_invalid");
        JsonElement source = sources.FirstOrDefault(item => item.GetProperty("collection").GetString() == collection);
        if (source.ValueKind != JsonValueKind.Object) throw Error("plugin_read_denied");
        int offset = parameters.GetProperty("offset").GetInt32(), size = parameters.GetProperty("pageSize").GetInt32();
        int total = source.GetProperty("items").GetArrayLength();
        if (offset < 0 || offset > total || size < 1 || size > 200) throw Error("plugin_capability_invalid");
        int end = Math.Min(offset + size, total);
        var (items, rowGuards) = SliceSnapshotSource(source, offset, end);
        return new { snapshotId = handle, collection, items, rowGuards, nextOffset = end == total ? (int?)null : end, versions = snapshot.Content.GetProperty("versions"), complete = source.GetProperty("complete") };
    }

    /// <summary>
    /// Cross-source bounded read: consecutive source chunks in request order from the
    /// requested position, at most SnapshotBatchRows rows. The item JSON of the whole batch
    /// stays within SnapshotBatchBytes across source boundaries too; only the batch's very
    /// first row is unconditional, so every batch makes progress while a source whose next
    /// row no longer fits starts the following batch with a fresh budget. Empty sources are
    /// kept as zero-row chunks. The reply's `next` is the explicit position the following
    /// call must pass back (null once every source is exhausted).
    /// </summary>
    private object ReadSnapshotBatch(Snapshot snapshot, string handle, JsonElement[] sources, JsonElement next)
    {
        int index = 0, from = 0;
        if (next.ValueKind != JsonValueKind.Null)
        {
            ExactFile(next, "collection", "offset");
            string collection = next.GetProperty("collection").GetString() ?? throw Error("plugin_capability_invalid");
            index = Array.FindIndex(sources, source => source.GetProperty("collection").GetString() == collection);
            if (index < 0) throw Error("plugin_read_denied");
            if (next.GetProperty("offset").ValueKind != JsonValueKind.Number || !next.GetProperty("offset").TryGetInt32(out from) || from < 0) throw Error("plugin_capability_invalid");
        }
        var chunks = new List<object>();
        var versions = snapshot.Content.GetProperty("versions");
        int rows = 0, bytes = 0, last = -1;
        string? pendingCollection = null;
        int pendingOffset = 0;
        for (int i = index; i < sources.Length; i++)
        {
            JsonElement source = sources[i];
            string collection = source.GetProperty("collection").GetString()!;
            var corpus = source.GetProperty("items");
            int total = corpus.GetArrayLength();
            int offset = i == index ? from : 0;
            if (offset > total) throw Error("plugin_capability_invalid");
            int end = offset;
            bool cut = false;
            foreach (var row in corpus.EnumerateArray().Skip(offset))
            {
                // Row and item-byte budgets belong to the whole batch. Only the batch's very
                // first row is unconditional; every further row, across source boundaries too,
                // must fit the remaining byte budget.
                int size = Encoding.UTF8.GetByteCount(row.GetRawText());
                if (rows >= SnapshotBatchRows || (rows > 0 && bytes + size > SnapshotBatchBytes)) { cut = true; break; }
                bytes += size;
                rows++;
                end++;
            }
            if (cut && end == offset)
            {
                // Nothing of this source fits any more: it starts the next batch.
                pendingCollection = collection;
                pendingOffset = offset;
                break;
            }
            var (items, rowGuards) = SliceSnapshotSource(source, offset, end);
            chunks.Add(new { collection, items, rowGuards, nextOffset = end == total ? (int?)null : end, complete = source.GetProperty("complete") });
            last = i;
            if (end < total)
            {
                pendingCollection = collection;
                pendingOffset = end;
                break;
            }
        }
        // The reply names exactly where the following batch must continue: inside a source the
        // budget cut, or at the first source this batch did not reach; null after the last one.
        object? following = pendingCollection is { } resume ? new { collection = resume, offset = pendingOffset }
            : last + 1 < sources.Length && sources[last + 1].GetProperty("collection").GetString() is { } upcoming ? new { collection = upcoming, offset = 0 } : null;
        return new { snapshotId = handle, chunks, next = following, versions };
    }

    /// <summary>Rows [offset, end) of one snapshot source plus the row guards the projection can address.</summary>
    private static (JsonElement[] Items, Dictionary<string, JsonElement> RowGuards) SliceSnapshotSource(JsonElement source, int offset, int end)
    {
        var page = source.GetProperty("items").EnumerateArray().Skip(offset).Take(end - offset).ToArray();
        // Row guards follow the data.query contract: keyed by row id, served only when the
        // projection carries `id` (otherwise the plugin could not address a guard anyway).
        bool idProjected = source.GetProperty("fields").EnumerateArray().Any(field => field.GetString() == "id");
        var guards = source.GetProperty("rowGuards");
        var rowGuards = new Dictionary<string, JsonElement>(StringComparer.Ordinal);
        if (idProjected)
            foreach (var row in page)
            {
                string? id = row.TryGetProperty("id", out var idCell) && idCell.ValueKind == JsonValueKind.String ? idCell.GetString() : null;
                if (id is not null && guards.ValueKind == JsonValueKind.Object && guards.TryGetProperty(id, out var guard)) rowGuards[id] = guard.Clone();
            }
        return (page, rowGuards);
    }
}
