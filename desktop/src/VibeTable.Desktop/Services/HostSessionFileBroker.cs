using System.IO;
using System.Text.Json;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>One workspace binding's native path authority and file streams.</summary>
internal sealed class HostSessionFileBroker : IHostFileRequestHandler
{
    private const int BlockBytes = 256 * 1024;
    private const long PluginBytes = 1024 * 1024;
    private readonly Func<WorkspaceRequestEpochLease> _capture;
    private readonly Action<Action> _commitCurrent;
    private readonly TimeProvider _time;
    private readonly SemaphoreSlim _gate = new(1);
    private readonly CancellationTokenSource _retired = new();
    private readonly CancellationTokenRegistration _epochRegistration;
    private readonly Dictionary<string, Grant> _grants = new(StringComparer.Ordinal);
    private readonly Dictionary<string, Transfer> _transfers = new(StringComparer.Ordinal);
    private readonly Dictionary<string, Reservation> _reservations = new(StringComparer.Ordinal);
    private readonly TaskCompletionSource _drain = new(TaskCreationOptions.RunContinuationsAsynchronously);
    private int _retireStarted;

    internal HostSessionFileBroker(Func<WorkspaceRequestEpochLease> capture,
        Action<Action> commitCurrent, TimeProvider? time = null)
    {
        _capture = capture;
        _commitCurrent = commitCurrent;
        _time = time ?? TimeProvider.System;
        using WorkspaceRequestEpochLease lease = _capture();
        _epochRegistration = lease.CancellationToken.Register(Retire);
    }

    internal async Task<JsonElement> IssueAsync(string path, bool write, string? runId,
        CancellationToken token, string? mimeType = null)
    {
        using WorkspaceRequestEpochLease lease = _capture();
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(token, lease.CancellationToken, _retired.Token);
        await _gate.WaitAsync(linked.Token).ConfigureAwait(false);
        try
        {
            linked.Token.ThrowIfCancellationRequested();
            PruneExpiredReceipts();
            if (_grants.Count >= 128) throw new InvalidOperationException("Too many file grants.");
            if (!Path.IsPathFullyQualified(path) || path.StartsWith(@"\\.\", StringComparison.Ordinal)
                || path.StartsWith(@"\\?\GLOBALROOT", StringComparison.OrdinalIgnoreCase))
                throw new IOException("A native file path is required.");
            string full = Path.GetFullPath(path);
            string name = Path.GetFileName(full);
            if (name.Length is 0 or > 256 || name.IndexOfAny(Path.GetInvalidFileNameChars()) >= 0
                || !Directory.Exists(Path.GetDirectoryName(full))) throw new IOException("Invalid selected file.");
            long? length = write ? null : new FileInfo(full).Length;
            if (runId is not null && length > PluginBytes) throw new IOException("Plugin file is too large.");
            var grant = new Grant("grant-" + Guid.NewGuid().ToString("N"), full, write, runId,
                _time.GetUtcNow().ToUnixTimeMilliseconds() / 1000.0 + 300, length, mimeType);
            _commitCurrent(() => _grants.Add(grant.Id, grant));
            return Descriptor(grant);
        }
        finally { _gate.Release(); }
    }

    internal Task<JsonElement> DescribeAsync(string id, CancellationToken token)
        => HandleAsync("describe", JsonSerializer.SerializeToElement(new { grantId = id }), token);

    public async Task<JsonElement> HandleAsync(string action, JsonElement parameters, CancellationToken token)
    {
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(token, _retired.Token);
        await _gate.WaitAsync(linked.Token).ConfigureAwait(false);
        try
        {
            linked.Token.ThrowIfCancellationRequested();
            return action switch
            {
                "describe" => Describe(parameters),
                "openRead" => Open(parameters, write: false),
                "openWrite" => Open(parameters, write: true),
                "read" => await ReadAsync(parameters, linked.Token).ConfigureAwait(false),
                "write" => await WriteAsync(parameters, linked.Token).ConfigureAwait(false),
                "closeRead" => await CloseReadAsync(parameters).ConfigureAwait(false),
                "finishWrite" => await FinishWriteAsync(parameters, linked.Token).ConfigureAwait(false),
                "reserveImport" => Reserve(parameters),
                "settleImport" => Settle(parameters),
                _ => throw new JsonException("Unknown native file operation."),
            };
        }
        finally { _gate.Release(); }
    }

    private JsonElement Describe(JsonElement p)
    {
        Exact(p, "grantId");
        return Descriptor(Available(Text(p, "grantId"), write: null, runId: null));
    }

    private JsonElement Open(JsonElement p, bool write)
    {
        bool plugin = p.TryGetProperty("runId", out _);
        Exact(p, plugin ? ["grantId", "runId"] : ["grantId"]);
        Grant grant = Available(Text(p, "grantId"), write, plugin ? Text(p, "runId") : null);
        if (_transfers.Count >= 128) throw new InvalidOperationException("Too many native transfers.");
        WorkspaceRequestEpochLease lease = _capture();
        string? temporary = null;
        FileStream? stream = null;
        try
        {
            temporary = write ? Path.Combine(Path.GetDirectoryName(grant.Path)!, $".vibetable-{Guid.NewGuid():N}.tmp") : null;
            stream = new FileStream(temporary ?? grant.Path, write ? FileMode.CreateNew : FileMode.Open,
                write ? FileAccess.Write : FileAccess.Read, write ? FileShare.None : FileShare.Read,
                64 * 1024, FileOptions.Asynchronous | FileOptions.SequentialScan);
            if (!write && grant.RunId is not null && stream.Length > PluginBytes)
                throw new IOException("Plugin file is too large.");
            string id = "transfer-" + Guid.NewGuid().ToString("N");
            _commitCurrent(() =>
            {
                lease.CancellationToken.ThrowIfCancellationRequested();
                _transfers.Add(id, new Transfer(grant, stream, temporary, lease));
                grant.State = write ? "writing" : "reading";
            });
            return JsonSerializer.SerializeToElement(new { transferId = id, displayName = Path.GetFileName(grant.Path) });
        }
        catch
        {
            stream?.Dispose();
            try { if (temporary is not null) File.Delete(temporary); }
            finally { lease.Dispose(); }
            throw;
        }
    }

    private async Task<JsonElement> ReadAsync(JsonElement p, CancellationToken token)
    {
        Exact(p, "transferId", "maxBytes");
        Transfer transfer = Active(p, write: false);
        int maximum = p.GetProperty("maxBytes").GetInt32();
        if (maximum is <= 0 or > BlockBytes) throw new JsonException("Invalid native read size.");
        byte[] buffer = new byte[maximum];
        int read = await transfer.Stream.ReadAsync(buffer, token).ConfigureAwait(false);
        transfer.Offset += read;
        if (transfer.Grant.RunId is not null && transfer.Offset > PluginBytes)
            throw new IOException("Plugin file is too large.");
        transfer.Eof = read == 0;
        return JsonSerializer.SerializeToElement(new { base64 = Convert.ToBase64String(buffer, 0, read), eof = transfer.Eof });
    }

    private async Task<JsonElement> WriteAsync(JsonElement p, CancellationToken token)
    {
        Exact(p, "transferId", "offset", "base64");
        Transfer transfer = Active(p, write: true);
        if (p.GetProperty("offset").GetInt64() != transfer.Offset) throw new IOException("Out-of-order native write.");
        string encoded = Text(p, "base64");
        if (encoded.Length > (BlockBytes + 2) / 3 * 4) throw new IOException("Native write is too large.");
        byte[] bytes = Convert.FromBase64String(encoded);
        if (bytes.Length > BlockBytes || (transfer.Grant.RunId is not null && transfer.Offset + bytes.Length > PluginBytes))
            throw new IOException("Native write is too large.");
        await transfer.Stream.WriteAsync(bytes, token).ConfigureAwait(false);
        transfer.Offset += bytes.Length;
        return JsonSerializer.SerializeToElement(new { offset = transfer.Offset });
    }

    private async Task<JsonElement> CloseReadAsync(JsonElement p)
    {
        Exact(p, "transferId");
        Transfer transfer = FindTransfer(p);
        if (transfer.Grant.Write) throw new JsonException("Not a read transfer.");
        if (!transfer.Closed)
        {
            await CloseAsync(transfer).ConfigureAwait(false);
            transfer.Grant.State = transfer.Grant.RunId is not null && transfer.Eof ? "consumed" : "available";
        }
        return JsonSerializer.SerializeToElement(new { closed = true });
    }

    private async Task<JsonElement> FinishWriteAsync(JsonElement p, CancellationToken token)
    {
        Exact(p, "transferId", "commit");
        Transfer transfer = FindTransfer(p);
        if (!transfer.Grant.Write) throw new JsonException("Not a write transfer.");
        bool commit = p.GetProperty("commit").GetBoolean();
        if (transfer.Outcome is bool settled)
        {
            if (settled != commit) throw new IOException("Native transfer outcome is already settled.");
            return WriteReceipt(transfer);
        }
        if (transfer.Closed) throw new IOException("Native transfer closed without a commit.");
        try
        {
            if (commit)
            {
                await transfer.Stream.FlushAsync(token).ConfigureAwait(false);
                transfer.Stream.Flush(flushToDisk: true);
                await transfer.Stream.DisposeAsync().ConfigureAwait(false);
                token.ThrowIfCancellationRequested();
                _commitCurrent(() =>
                {
                    token.ThrowIfCancellationRequested();
                    File.Move(transfer.Temporary!, transfer.Grant.Path, overwrite: true);
                    transfer.Outcome = true;
                    transfer.Grant.State = "consumed";
                });
            }
            else
            {
                transfer.Outcome = false;
                transfer.Grant.State = "available";
            }
            return WriteReceipt(transfer);
        }
        finally
        {
            if (transfer.Outcome is null)
            {
                transfer.Outcome = false;
                transfer.Grant.State = "available";
            }
            await CloseAsync(transfer).ConfigureAwait(false);
        }
    }

    private JsonElement Reserve(JsonElement p)
    {
        Exact(p, "grantId", "token");
        string grantId = Text(p, "grantId"), plan = Text(p, "token");
        foreach ((string existingId, Reservation existing) in _reservations)
            if (existing.Grant.Id == grantId && existing.Plan == plan && existing.Outcome is null
                && existing.Grant.State == "reserved")
                return JsonSerializer.SerializeToElement(new { reservationId = existingId });
        Grant grant = Available(grantId, write: false, runId: null);
        if (_reservations.Count >= 128) throw new InvalidOperationException("Too many import reservations.");
        string id = "reservation-" + Guid.NewGuid().ToString("N");
        _reservations.Add(id, new Reservation(grant, plan));
        grant.State = "reserved";
        return JsonSerializer.SerializeToElement(new { reservationId = id });
    }

    private JsonElement Settle(JsonElement p)
    {
        Exact(p, "reservationId", "outcome");
        string id = Text(p, "reservationId"), outcome = Text(p, "outcome");
        if (outcome is not ("consumed" or "released") || !_reservations.TryGetValue(id, out Reservation? reservation))
            throw new JsonException("Invalid import settlement.");
        if (reservation.Outcome is not null && reservation.Outcome != outcome)
            throw new IOException("Import reservation is already settled.");
        if (reservation.Outcome == outcome)
            return JsonSerializer.SerializeToElement(new { reservationId = id, outcome });
        if (reservation.Grant.State != "reserved") throw new IOException("Import reservation is no longer active.");
        reservation.Outcome = outcome;
        reservation.ReceiptExpiresAt = _time.GetUtcNow().AddMinutes(5).ToUnixTimeMilliseconds() / 1000.0;
        reservation.Grant.State = outcome == "consumed" ? "consumed" : "available";
        return JsonSerializer.SerializeToElement(new { reservationId = id, outcome });
    }

    internal async Task RevokeAsync(string grantId)
    {
        await _gate.WaitAsync().ConfigureAwait(false);
        try
        {
            if (!_grants.TryGetValue(grantId, out Grant? grant)) return;
            grant.State = "revoked";
            foreach (Transfer transfer in _transfers.Values.Where(item => ReferenceEquals(item.Grant, grant)))
                await CloseAsync(transfer).ConfigureAwait(false);
        }
        finally { _gate.Release(); }
    }

    public void Retire()
    {
        if (Interlocked.Exchange(ref _retireStarted, 1) != 0) return;
        _retired.Cancel();
        _ = DrainAsync();
    }

    internal Task DrainCompletion => _drain.Task;

    private async Task DrainAsync()
    {
        await _gate.WaitAsync().ConfigureAwait(false);
        var errors = new List<Exception>();
        try
        {
            foreach (Grant grant in _grants.Values) grant.State = "revoked";
            foreach (Transfer transfer in _transfers.Values)
            {
                try { await CloseAsync(transfer).ConfigureAwait(false); }
                catch (Exception error) { errors.Add(error); }
            }
        }
        finally
        {
            _epochRegistration.Unregister();
            _gate.Release();
            if (errors.Count > 0) _drain.TrySetException(errors);
            else _drain.TrySetResult();
        }
    }

    private void PruneExpiredReceipts()
    {
        double now = _time.GetUtcNow().ToUnixTimeMilliseconds() / 1000.0;
        foreach (string id in _transfers.Where(pair => pair.Value.Closed && pair.Value.ReceiptExpiresAt <= now)
            .Select(pair => pair.Key).ToArray()) _transfers.Remove(id);
        foreach (string id in _reservations.Where(pair => pair.Value.Outcome is not null && pair.Value.ReceiptExpiresAt <= now)
            .Select(pair => pair.Key).ToArray()) _reservations.Remove(id);
        foreach (string id in _grants.Where(pair => pair.Value.ExpiresAt <= now
            && pair.Value.State is not ("reading" or "writing" or "reserved"))
            .Select(pair => pair.Key).ToArray()) _grants.Remove(id);
    }

    private async Task CloseAsync(Transfer transfer)
    {
        if (transfer.Closed) return;
        transfer.Closed = true;
        transfer.ReceiptExpiresAt = _time.GetUtcNow().AddMinutes(5).ToUnixTimeMilliseconds() / 1000.0;
        try
        {
            await transfer.Stream.DisposeAsync().ConfigureAwait(false);
            if (transfer.Temporary is not null) File.Delete(transfer.Temporary);
        }
        finally { transfer.Lease.Dispose(); }
    }

    private Grant Available(string id, bool? write, string? runId)
    {
        if (!_grants.TryGetValue(id, out Grant? grant) || grant.State != "available"
            || grant.ExpiresAt <= _time.GetUtcNow().ToUnixTimeMilliseconds() / 1000.0
            || (write.HasValue && grant.Write != write.Value) || grant.RunId != runId)
            throw new HostPathGrantException();
        return grant;
    }

    private Transfer FindTransfer(JsonElement p)
        => _transfers.TryGetValue(Text(p, "transferId"), out Transfer? transfer) ? transfer
            : throw new InvalidOperationException("File transfer is unavailable.");

    private Transfer Active(JsonElement p, bool write)
    {
        Transfer transfer = FindTransfer(p);
        if (transfer.Closed || transfer.Grant.Write != write || transfer.Grant.State != (write ? "writing" : "reading"))
            throw new InvalidOperationException("File transfer is not active.");
        return transfer;
    }

    private static JsonElement Descriptor(Grant grant) => JsonSerializer.SerializeToElement(new
    {
        grantId = grant.Id, purpose = grant.Write ? "export_target" : "import_source",
        direction = grant.Write ? "write" : "read", displayName = Path.GetFileName(grant.Path),
        sizeBytes = grant.Size, mimeType = grant.MimeType, expiresAt = grant.ExpiresAt,
    });
    private static JsonElement WriteReceipt(Transfer transfer) => JsonSerializer.SerializeToElement(new
    { committed = transfer.Outcome == true, bytes = transfer.Offset, displayName = Path.GetFileName(transfer.Grant.Path) });
    private static string Text(JsonElement p, string key) => p.GetProperty(key) is { ValueKind: JsonValueKind.String } value
        && !string.IsNullOrWhiteSpace(value.GetString()) ? value.GetString()! : throw new JsonException("Invalid native file parameter.");
    private static void Exact(JsonElement p, params string[] keys)
    {
        if (p.ValueKind != JsonValueKind.Object) throw new JsonException("Invalid native file request.");
        string[] actual = p.EnumerateObject().Select(item => item.Name).ToArray();
        if (actual.Length != keys.Length || actual.Distinct(StringComparer.Ordinal).Count() != keys.Length
            || actual.Any(key => !keys.Contains(key, StringComparer.Ordinal))) throw new JsonException("Invalid native file request.");
    }
    private sealed record Grant(string Id, string Path, bool Write, string? RunId, double ExpiresAt, long? Size, string? MimeType)
    { internal string State { get; set; } = "available"; }
    private sealed record Transfer(Grant Grant, FileStream Stream, string? Temporary, WorkspaceRequestEpochLease Lease)
    {
        internal long Offset { get; set; }
        internal bool Eof { get; set; }
        internal bool Closed { get; set; }
        internal bool? Outcome { get; set; }
        internal double ReceiptExpiresAt { get; set; }
    }
    private sealed record Reservation(Grant Grant, string Plan)
    {
        internal string? Outcome { get; set; }
        internal double ReceiptExpiresAt { get; set; }
    }
}