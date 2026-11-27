using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>
/// #590 data.openSnapshot Host side. Go materializes every source inside one
/// transaction and answers with the bounded in-memory corpus; the Host keeps
/// that corpus per surface (2 handles per surface, 8 total, 5 minute absolute
/// expiry), re-validates it with Go before every page and every cross-source
/// batch (snapshot.validate: session, current authorization, schema revisions of
/// every source and the completed model record the corpus was authorized under,
/// exactly once per request) and then slices rows out of the
/// immutable corpus at that authorization point. Plain data writes never touch a
/// handle; nothing is served from a handle the authority stopped endorsing, and
/// row guards (data.query contract: keyed by row id, projected or not) are served
/// only while validate reports the plugin may currently read id. A cancellation
/// between the endorsed slice and its delivery ends the existing handle as well.
/// </summary>
internal sealed partial class PluginSurfaceApi
{
    /// <summary>Corpus budget (every row and guard of every source plus the version vector) measured the way the bridge reply encodes it.</summary>
    private const int SnapshotBytes = 512 * 1024;
    private const string SnapshotContract = "vibetable.plugin-snapshot.v1";
    /// <summary>One snapshot.readBatch crosses sources but carries at most this many rows (empty sources ride along).</summary>
    private const int SnapshotBatchRows = 200;
    /// <summary>Budget of one batch across source boundaries as the bridge encodes the reply: every chunk it takes from a source (its two containers and fixed members) and every row (item, served guard, separators); only the batch's very first chunk and very first row are unconditional.</summary>
    private const int SnapshotBatchBytes = 256 * 1024;
    /// <summary>Upper bound of the fixed members of a snapshot reply (envelope, ids, position) outside the version vector, the rows and the collection names, which are measured where they appear.</summary>
    private const int SnapshotEnvelopeBytes = 1024;
    /// <summary>Absolute lifetime of a handle from its open; the validate round trip re-checks it after every await.</summary>
    internal TimeSpan SnapshotLifetime { get; set; } = TimeSpan.FromMinutes(5);
    private readonly object _snapshotGate = new();
    /// <summary>In-flight snapshot.open reservations of every surface, published or being retired alike.</summary>
    private int _snapshotReservations;
    /// <summary>
    /// Binding is the one Host-only value Go fixed inside the open transaction: the audit sequence of the
    /// completed model record the corpus was authorized under (null without one). Validate replays it and Go
    /// compares it with what it resolves now, so a legitimate remap that kept every projection readable
    /// still ends the handle; it never reaches the plugin. ChunkBytes holds, per source, the
    /// Reply-encoded cost of every chunk a batch may take from it (containers and fixed members
    /// included); RowBytes, per source and row, the Reply-encoded cost of the row plus the guard it
    /// may be served with; VersionBytes and EnvelopeBytes the Reply-encoded size of the version
    /// vector and the upper bound of everything else outside chunks and rows.
    /// </summary>
    private sealed record Snapshot(JsonElement Content, JsonElement Validation, DateTimeOffset Expires, string RequestId, long? Binding, int[][] RowBytes, int[] ChunkBytes, int VersionBytes, int EnvelopeBytes);

    /// <summary>
    /// Exact UTF-8 size of a JSON value as the bridge reply encodes it: strings go through the default
    /// encoder again, so CJK or HTML-sensitive content expands to \uXXXX (up to six times its raw JSON
    /// size) while numbers keep their raw text. Raw JSON length is not a frame budget.
    /// </summary>
    private static int ReplyBytes(JsonElement value) => JsonSerializer.SerializeToUtf8Bytes(value, Json).Length;

    /// <summary>
    /// Reserves snapshot capacity before the authority starts materializing: the per-surface
    /// (2) and total (8) limits count published handles plus in-flight opens, so parallel opens
    /// can never make the authority build a corpus the Host would refuse to publish. Handles whose
    /// absolute lifetime ran out count against the limits only until a sweep removes them, so the
    /// admission sweeps first. The reservation becomes the published handle under the same gate
    /// (PublishSnapshot) or is handed back by ReleaseSnapshotOpen from the open's finally.
    /// </summary>
    private void ReserveSnapshotOpen(SurfaceState state)
    {
        lock (_snapshotGate)
        {
            SweepSnapshots();
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
            if (content.GetProperty("contract").GetString() != SnapshotContract) throw Error("plugin_snapshot_limit");
            // The authority names the record the corpus was authorized under: a positive audit sequence or
            // null. It stays Host-only and is replayed verbatim on every validate.
            if (!content.TryGetProperty("binding", out var binding) || !(binding.ValueKind == JsonValueKind.Null || binding.ValueKind == JsonValueKind.Number && binding.TryGetInt64(out var sequence) && sequence >= 1)) throw Error("plugin_capability_invalid");
            long? bound = binding.ValueKind == JsonValueKind.Null ? null : binding.GetInt64();
            // The corpus keeps one [id, digest] pair per row aligned with items (null where id was
            // not granted at open); a corpus that cannot be sliced that way is never published. Its
            // budget is measured the way the bridge will encode it, row by row, so every row's
            // serving cost is known before the first page: the corpus (rows, guards, version vector)
            // stays within SnapshotBytes and each row fits one reply on its own, because a batch's
            // first row is unconditional.
            int versionBytes = ReplyBytes(content.GetProperty("versions"));
            int corpus = versionBytes, nameBytes = 0;
            var rowBytes = new List<int[]>();
            var chunkBytes = new List<int>();
            foreach (var source in content.GetProperty("sources").EnumerateArray())
            {
                var guards = source.GetProperty("rowGuards");
                var items = source.GetProperty("items");
                if (guards.ValueKind != JsonValueKind.Array || guards.GetArrayLength() != items.GetArrayLength()) throw Error("plugin_snapshot_limit");
                var collection = source.GetProperty("collection");
                nameBytes = Math.Max(nameBytes, ReplyBytes(collection));
                // Every chunk a batch may take from this source, empty or not, costs its two containers,
                // its fixed members and the separator in front of it (1000 is as wide as any nextOffset
                // of a corpus of at most 4096 rows, null included).
                chunkBytes.Add(JsonSerializer.SerializeToUtf8Bytes(new { collection, items = Array.Empty<JsonElement>(), rowGuards = new Dictionary<string, JsonElement>(), nextOffset = 1000, complete = source.GetProperty("complete") }, Json).Length + 1);
                var sizes = new int[items.GetArrayLength()];
                for (int index = 0; index < sizes.Length; index++)
                    // The row as a batch reply carries it: the item and its separator plus, when its
                    // guard can be served, the member it becomes ("id":digest) with its own separator.
                    sizes[index] = ReplyBytes(items[index]) + 1 + (TryGuard(guards[index], out var id, out var digest) ? ReplyBytes(id) + ReplyBytes(digest) + 2 : 0);
                corpus += sizes.Sum();
                rowBytes.Add(sizes);
            }
            // Fixed members outside the rows: the reply envelope, ids, keys and the position, plus the
            // one collection name a page or a batch's `next` repeats (chunk names are charged with
            // their chunk). Each row must fit one reply on its own: a batch's first row is unconditional.
            int envelope = SnapshotEnvelopeBytes + nameBytes;
            for (int i = 0; i < chunkBytes.Count; i++)
                foreach (int size in rowBytes[i])
                    if (envelope + versionBytes + chunkBytes[i] + size > MaxBytes) throw Error("plugin_snapshot_limit");
            if (corpus > SnapshotBytes) throw Error("plugin_snapshot_limit");
            // The validation request replays the plugin's own sources, the version vector Go produced
            // and the binding Go fixed; Go re-authorizes the projections, compares schema revisions and
            // checks that the same completed record still applies.
            var validation = JsonSerializer.SerializeToElement(new { contract = SnapshotContract, sources = parameters.GetProperty("sources"), versions = content.GetProperty("versions"), binding = bound });
            // Every page and batch of this handle starts with that request. An open reply may sit near
            // the frame limit while returning few rows, and the plugin's sources plus the version
            // vector can push the validate request past it: checked with the very encoding CallAsync
            // applies, so such a corpus is refused as a whole here, before a handle that could never
            // have served a page is published.
            try { EncodeCall(surface, "snapshot.validate", validation, Guid.NewGuid().ToString("N"), null, null, null); }
            catch (PluginSurfacePolicyException e) when (e.Code == "plugin_message_too_large") { throw Error("plugin_snapshot_limit"); }
            var sources = content.GetProperty("sources").EnumerateArray().Select(item => new
            { collection = item.GetProperty("collection"), fields = item.GetProperty("fields"), computedFields = item.GetProperty("computedFields"), filteredRows = item.GetProperty("filteredRows"), totalRows = item.GetProperty("totalRows"), complete = item.GetProperty("complete") }).ToArray();
            string handle = Guid.NewGuid().ToString("N");
            var snapshot = new Snapshot(content, validation, DateTimeOffset.UtcNow.Add(SnapshotLifetime), requestId, bound, rowBytes.ToArray(), chunkBytes.ToArray(), versionBytes, envelope);
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

    /// <summary>
    /// Exactly one authority round trip per request. After the await nothing is trusted from
    /// before it: the handle must still be live, unexpired (absolute) and the very same snapshot,
    /// and the authority's endorsement must be explicit (valid === true). The reply also names,
    /// per collection, whether row guards keyed by row id may be served at this authorization
    /// point; a reply without it serves none. Any failure here ends the handle.
    /// </summary>
    private async Task<HashSet<string>> ValidateSnapshotAsync(PluginSurfaceSession surface, SurfaceState state, string handle, Snapshot snapshot, CancellationToken token)
    {
        try
        {
            if (!Live(state, handle, snapshot)) throw Error("plugin_snapshot_stale");
            JsonElement reply = await CallAsync(surface, "snapshot.validate", snapshot.Validation, token);
            // The authority round trip may have outlived the handle: absolute expiry and liveness
            // are re-checked before anything is sliced out of the corpus.
            token.ThrowIfCancellationRequested();
            if (!Live(state, handle, snapshot)) throw Error("plugin_snapshot_stale");
            if (reply.ValueKind != JsonValueKind.Object || !reply.TryGetProperty("valid", out var valid) || valid.ValueKind != JsonValueKind.True) throw Error("plugin_snapshot_stale");
            var identity = new HashSet<string>(StringComparer.Ordinal);
            if (reply.TryGetProperty("identity", out var map) && map.ValueKind == JsonValueKind.Object)
                foreach (var entry in map.EnumerateObject()) if (entry.Value.ValueKind == JsonValueKind.True) identity.Add(entry.Name);
            return identity;
        }
        catch { state.Snapshots.TryRemove(handle, out _); throw; }
    }

    /// <summary>Live means: still published, and the very same snapshot the request resolved.</summary>
    private static bool Live(SurfaceState state, string handle, Snapshot snapshot)
        => snapshot.Expires > DateTimeOffset.UtcNow && state.Snapshots.TryGetValue(handle, out var current) && ReferenceEquals(current, snapshot);

    private async Task<(string Handle, object Result)> UseSnapshotAsync(PluginSurfaceSession surface, SurfaceState state, string method, JsonElement parameters, CancellationToken token)
    {
        bool batch = method == "snapshot.readBatch";
        if (!batch && method != "snapshot.page") throw Error("plugin_api_unsupported");
        if (batch) ExactFile(parameters, "snapshotId", "next");
        else ExactFile(parameters, "snapshotId", "collection", "offset", "pageSize");
        string handle = parameters.GetProperty("snapshotId").GetString() ?? throw Error("plugin_capability_invalid");
        if (!state.Snapshots.TryGetValue(handle, out var snapshot)) throw Error("plugin_snapshot_stale");
        // Exactly one authority round trip per request; the slices below come from the very
        // corpus this validation endorsed at the authorization point it reported.
        var identity = await ValidateSnapshotAsync(surface, state, handle, snapshot, token);
        var sources = snapshot.Content.GetProperty("sources").EnumerateArray().ToArray();
        try
        {
            object result;
            if (batch) result = ReadSnapshotBatch(snapshot, handle, sources, parameters.GetProperty("next"), identity);
            else result = ReadSnapshotPage(snapshot, handle, sources, parameters, identity);
            // Slicing is the last step before delivery. A cancellation that landed meanwhile
            // ends the existing handle (the page is never delivered, so it is never re-served from
            // it); a position or shape rejection keeps the handle, it is the plugin's to correct.
            token.ThrowIfCancellationRequested();
            return (handle, result);
        }
        catch (OperationCanceledException) { state.Snapshots.TryRemove(handle, out _); throw; }
    }

    private static object ReadSnapshotPage(Snapshot snapshot, string handle, JsonElement[] sources, JsonElement parameters, HashSet<string> identity)
    {
        string collection = parameters.GetProperty("collection").GetString() ?? throw Error("plugin_capability_invalid");
        JsonElement source = sources.FirstOrDefault(item => item.GetProperty("collection").GetString() == collection);
        if (source.ValueKind != JsonValueKind.Object) throw Error("plugin_read_denied");
        int offset = parameters.GetProperty("offset").GetInt32(), size = parameters.GetProperty("pageSize").GetInt32();
        int total = source.GetProperty("items").GetArrayLength();
        if (offset < 0 || offset > total || size < 1 || size > 200) throw Error("plugin_capability_invalid");
        int end = Math.Min(offset + size, total);
        var (items, rowGuards) = SliceSnapshotSource(source, offset, end, identity.Contains(collection));
        return new { snapshotId = handle, collection, items, rowGuards, nextOffset = end == total ? (int?)null : end, versions = snapshot.Content.GetProperty("versions"), complete = source.GetProperty("complete") };
    }

    /// <summary>
    /// Cross-source bounded read: consecutive source chunks in request order from the
    /// requested position, at most SnapshotBatchRows rows. The whole reply stays within the
    /// frame: the version vector plus everything the batch carries (every chunk's containers and
    /// fixed members, every row's item, served guard and separators, measured the way the bridge
    /// encodes them) stays within SnapshotBatchBytes on top of each other across source boundaries
    /// too; only the batch's very first chunk and very first row are unconditional, so every batch
    /// makes progress while a source whose chunk or next row no longer fits starts the following
    /// batch as a whole. Empty sources are kept as zero-row chunks. The reply's `next` is the
    /// explicit position the following call must pass back (null once every source is exhausted).
    /// </summary>
    private object ReadSnapshotBatch(Snapshot snapshot, string handle, JsonElement[] sources, JsonElement next, HashSet<string> identity)
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
        // One budget for the whole batch, counted as the bridge encodes the reply: the batch budget
        // and the frame (envelope, version vector and everything counted so far). Chunk and row costs
        // were fixed at open; nothing counted ever crosses a budget.
        bool Fits(int cost) => bytes + cost <= SnapshotBatchBytes && snapshot.EnvelopeBytes + snapshot.VersionBytes + bytes + cost <= MaxBytes;
        for (int i = index; i < sources.Length; i++)
        {
            JsonElement source = sources[i];
            string collection = source.GetProperty("collection").GetString()!;
            var corpus = source.GetProperty("items");
            int total = corpus.GetArrayLength();
            int offset = i == index ? from : 0;
            if (offset > total) throw Error("plugin_capability_invalid");
            if (chunks.Count > 0 && !Fits(snapshot.ChunkBytes[i]))
            {
                // Not even this source's chunk fits any more: it starts the following batch as a whole.
                pendingCollection = collection;
                pendingOffset = offset;
                break;
            }
            bytes += snapshot.ChunkBytes[i];
            int end = offset;
            bool cut = false;
            for (int position = offset; position < total; position++)
            {
                // Row budgets belong to the whole batch. Only the batch's very first row is
                // unconditional; every further row, across source boundaries too, must fit the
                // remaining budgets.
                int size = snapshot.RowBytes[i][position];
                if (rows >= SnapshotBatchRows || rows > 0 && !Fits(size)) { cut = true; break; }
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
            var (items, rowGuards) = SliceSnapshotSource(source, offset, end, identity.Contains(collection));
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

    /// <summary>A corpus guard that may be served: a [id, digest] pair with a non-empty string id and a string digest.</summary>
    private static bool TryGuard(JsonElement pair, out JsonElement id, out JsonElement digest)
    {
        id = digest = default;
        if (pair.ValueKind != JsonValueKind.Array || pair.GetArrayLength() != 2 || pair[0].ValueKind != JsonValueKind.String || !(pair[0].GetString() is { Length: > 0 }) || pair[1].ValueKind != JsonValueKind.String) return false;
        id = pair[0];
        digest = pair[1];
        return true;
    }

    /// <summary>
    /// Rows [offset, end) of one snapshot source plus the row guards of exactly that range. Guards
    /// follow the data.query contract: keyed by row id for every row, projected or not, whenever
    /// the current authorization lets the plugin read id (identity). The corpus keeps them aligned
    /// with items, one [id, digest] pair per row (null where id was not granted at open), so any
    /// offset slices without the public rows carrying an unprojected id; without identity nothing
    /// keyed by a row id leaves the Host.
    /// </summary>
    private static (JsonElement[] Items, Dictionary<string, JsonElement> RowGuards) SliceSnapshotSource(JsonElement source, int offset, int end, bool identity)
    {
        var items = source.GetProperty("items");
        var page = items.EnumerateArray().Skip(offset).Take(end - offset).ToArray();
        var rowGuards = new Dictionary<string, JsonElement>(StringComparer.Ordinal);
        var guards = source.GetProperty("rowGuards");
        if (!identity || guards.ValueKind != JsonValueKind.Array || guards.GetArrayLength() != items.GetArrayLength()) return (page, rowGuards);
        int index = 0;
        foreach (var pair in guards.EnumerateArray())
        {
            if (index >= end) break;
            if (index >= offset && TryGuard(pair, out var id, out var digest)) rowGuards[id.GetString()!] = digest.Clone();
            index++;
        }
        return (page, rowGuards);
    }
}
