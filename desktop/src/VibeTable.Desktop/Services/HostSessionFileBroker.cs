using System.IO;
using System.Text.Json;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>One workspace binding's native path authority and file streams.</summary>
internal sealed class HostSessionFileBroker : IHostFileRequestHandler
{
    private const int BlockBytes = 256 * 1024;
    private const long PluginBytes = 1024 * 1024;
    internal static readonly TimeSpan DownloadLifetime = TimeSpan.FromSeconds(120);
    internal TimeProvider DownloadTime => _time;
    internal CancellationToken DownloadRetirement => _retired.Token;
    private readonly Func<WorkspaceRequestEpochLease> _capture;
    private readonly Action<Action> _commitCurrent;
    private readonly TimeProvider _time;
    private readonly SemaphoreSlim _gate = new(1);
    private readonly CancellationTokenSource _retired = new();
    private CancellationTokenRegistration? _epochRegistration;
    private readonly Dictionary<string, Grant> _grants = new(StringComparer.Ordinal);
    private readonly Dictionary<string, Transfer> _transfers = new(StringComparer.Ordinal);
    private readonly Dictionary<string, Reservation> _reservations = new(StringComparer.Ordinal);
    private readonly Dictionary<string, Download> _downloads = new(StringComparer.Ordinal);
    private readonly Dictionary<string, Func<Task>> _terminations = new(StringComparer.Ordinal);
    private readonly TaskCompletionSource _drain = new(TaskCreationOptions.RunContinuationsAsynchronously);
    private readonly TimeSpan _drainDownloadSettlementTimeout;
    private int _retireStarted;

    internal HostSessionFileBroker(Func<WorkspaceRequestEpochLease> capture,
        Action<Action> commitCurrent, TimeProvider? time = null,
        TimeSpan? drainDownloadSettlementTimeout = null)
    {
        _capture = capture;
        _commitCurrent = commitCurrent;
        _time = time ?? TimeProvider.System;
        _drainDownloadSettlementTimeout = drainDownloadSettlementTimeout
            ?? TimeSpan.FromSeconds(10);
    }

    internal async Task<JsonElement> IssueAsync(string path, bool write, string? runId,
        CancellationToken token, string? mimeType = null,
        Func<Action, bool>? ownerCommit = null, string purpose = "ordinary", long? byteLimit = null,
        Func<CancellationToken, Task>? authorize = null)
    {
        if (authorize is not null) await authorize(token).ConfigureAwait(false);
        using WorkspaceRequestEpochLease lease = _capture();
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(token, lease.CancellationToken, _retired.Token);
        await _gate.WaitAsync(linked.Token).ConfigureAwait(false);
        try
        {
            // Installation precedes Opened. Bind retirement with the first admitted
            // file grant; construction must not require a business request lease.
            _epochRegistration ??= lease.CancellationToken.Register(Retire);
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
            if (runId is not null && length > (byteLimit ?? PluginBytes)) throw new IOException("Plugin file is too large.");
            var grant = new Grant("grant-" + Guid.NewGuid().ToString("N"), full, write, runId,
                _time.GetUtcNow().ToUnixTimeMilliseconds() / 1000.0 + 300, length, mimeType, ownerCommit, purpose, byteLimit ?? PluginBytes, authorize);
            _commitCurrent(() => { linked.Token.ThrowIfCancellationRequested(); CommitOwner(grant, () => _grants.Add(grant.Id, grant)); });
            return Descriptor(grant);
        }
        finally { _gate.Release(); }
    }

    internal Task<JsonElement> DescribeAsync(string id, CancellationToken token)
        => HandleAsync("describe", JsonSerializer.SerializeToElement(new { grantId = id }), token);

    /// <summary>
    /// Resolves the safe display facts of one import source grant: the short
    /// file name and the csv/xlsx source type (xlsm counts as xlsx, matching
    /// the worker reader). No absolute path, grant secret, or token escapes.
    /// </summary>
    internal async Task<(string SourceType, string SourceName)> DescribeImportSourceAsync(
        string grantId, CancellationToken token)
    {
        JsonElement descriptor = await HandleAsync(
            "describeImport",
            JsonSerializer.SerializeToElement(new { grantId }),
            token).ConfigureAwait(false);
        return (descriptor.GetProperty("sourceType").GetString()!,
            descriptor.GetProperty("displayName").GetString()!);
    }

    public async Task<JsonElement> HandleAsync(string action, JsonElement parameters, CancellationToken token)
    {
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(token, _retired.Token);
        Func<CancellationToken, Task>? authorize;
        await _gate.WaitAsync(linked.Token).ConfigureAwait(false);
        try
        {
            Grant? authority = parameters.TryGetProperty("grantId", out var grantId) && grantId.ValueKind == JsonValueKind.String
                && _grants.TryGetValue(grantId.GetString()!, out var grant) ? grant : null;
            if (authority is null && parameters.TryGetProperty("transferId", out var transferId)
                && transferId.ValueKind == JsonValueKind.String && _transfers.TryGetValue(transferId.GetString()!, out var transfer))
                authority = transfer.Grant;
            authorize = authority?.Authorize;
        }
        finally { _gate.Release(); }
        // HTTP authorization never runs under the broker's synchronous commit gate.
        if (authorize is not null) await authorize(linked.Token).ConfigureAwait(false);
        await _gate.WaitAsync(linked.Token).ConfigureAwait(false);
        try
        {
            linked.Token.ThrowIfCancellationRequested();
            return action switch
            {
                "describe" => Describe(parameters),
                "describeImport" => DescribeImport(parameters),
                "openRead" => Open(parameters, write: false),
                "openWrite" => Open(parameters, write: true),
                "read" => await ReadAsync(parameters, linked.Token).ConfigureAwait(false),
                "write" => await WriteAsync(parameters, linked.Token).ConfigureAwait(false),
                "closeRead" => await CloseReadAsync(parameters).ConfigureAwait(false),
                "finishWrite" => await FinishWriteAsync(parameters, linked.Token).ConfigureAwait(false),
                "reserveImport" => Reserve(parameters),
                "reservePluginImport" => Reserve(parameters, plugin: true),
                "settleImport" => Settle(parameters),
                _ => throw new JsonException("Unknown native file operation."),
            };
        }
        finally { _gate.Release(); }
    }

    private JsonElement Describe(JsonElement p)
    {
        bool plugin = p.TryGetProperty("runId", out _);
        Exact(p, plugin ? ["grantId", "runId"] : ["grantId"]);
        return Descriptor(Available(Text(p, "grantId"), write: null, runId: plugin ? Text(p, "runId") : null));
    }

    private JsonElement DescribeImport(JsonElement p)
    {
        Exact(p, "grantId");
        // History only needs immutable display facts; any live grant state
        // (available/reserved/reading) still describes the same source file.
        Grant grant = _grants.TryGetValue(Text(p, "grantId"), out Grant? found)
            && !found.Write
            ? found
            : throw new HostPathGrantException();
        string name = Path.GetFileName(grant.Path);
        string sourceType = Path.GetExtension(name).ToLowerInvariant() switch
        {
            ".csv" => "csv",
            ".xlsx" or ".xlsm" => "xlsx",
            _ => throw new HostPathGrantException(),
        };
        // IssueAsync already caps native file names at the frozen 256-char
        // history limit, so the display name needs no further mutation.
        return JsonSerializer.SerializeToElement(new
        {
            sourceType,
            displayName = name,
        });
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
            if (!write && grant.RunId is not null && stream.Length > grant.ByteLimit)
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
        if (transfer.Grant.RunId is not null && transfer.Offset > transfer.Grant.ByteLimit)
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
        if (bytes.Length > BlockBytes || (transfer.Grant.RunId is not null && transfer.Offset + bytes.Length > transfer.Grant.ByteLimit))
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
            transfer.Grant.State = transfer.Grant.RunId is not null && transfer.Grant.Purpose == "ordinary" && transfer.Eof ? "consumed" : "available";
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
                    CommitOwner(transfer.Grant, () =>
                    {
                        File.Move(transfer.Temporary!, transfer.Grant.Path, overwrite: true);
                        transfer.Outcome = true;
                        transfer.Grant.State = "consumed";
                    });
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

    private JsonElement Reserve(JsonElement p, bool plugin = false)
    {
        Exact(p, plugin ? ["grantId", "token", "runId"] : ["grantId", "token"]);
        string grantId = Text(p, "grantId"), plan = Text(p, "token");
        foreach ((string existingId, Reservation existing) in _reservations)
            if (existing.Grant.Id == grantId && existing.Plan == plan && existing.Outcome is null
                && existing.Grant.State == "reserved")
                return JsonSerializer.SerializeToElement(new { reservationId = existingId });
        Grant grant = Available(grantId, write: false, runId: plugin ? Text(p, "runId") : null);
        if (plugin && grant.Purpose != "import") throw new HostPathGrantException();
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

    /// <summary>
    /// Opens a host-driven download target: a validated native path plus a
    /// same-directory temporary file that WebView2 writes into directly.
    /// The caller must not hold any broker lock while waiting on a modal
    /// picker; resolve the path before calling this method.
    /// </summary>
    internal async Task<HostDownloadTarget> OpenDownloadTargetAsync(string path)
    {
        WorkspaceRequestEpochLease lease = _capture();
        try
        {
            await _gate.WaitAsync(_retired.Token).ConfigureAwait(false);
            try
            {
                _epochRegistration ??= lease.CancellationToken.Register(Retire);
                _retired.Token.ThrowIfCancellationRequested();
                PruneExpiredReceipts();
                if (_downloads.Values.Count(item => item.CleanupPending) >= 128)
                    throw new IOException("Too many pending dashboard cleanups.");
                if (_downloads.Values.Count(item => item.ReceiptExpiresAt is null) >= 16)
                    throw new InvalidOperationException("Too many dashboard downloads.");
                foreach (string receipt in _downloads.Where(pair => pair.Value.ReceiptExpiresAt is not null && !pair.Value.CleanupPending)
                    .OrderByDescending(pair => pair.Value.ReceiptExpiresAt).Skip(128)
                    .Select(pair => pair.Key).ToArray()) _downloads.Remove(receipt);
                if (!Path.IsPathFullyQualified(path) || path.StartsWith(@"\\.\", StringComparison.Ordinal)
                    || path.StartsWith(@"\\?\GLOBALROOT", StringComparison.OrdinalIgnoreCase))
                    throw new IOException("A native file path is required.");
                string full = Path.GetFullPath(path);
                string name = Path.GetFileName(full);
                if (name.Length is 0 or > 256 || name.IndexOfAny(Path.GetInvalidFileNameChars()) >= 0
                    || !Directory.Exists(Path.GetDirectoryName(full))) throw new IOException("Invalid selected file.");
                string temporary = Path.Combine(Path.GetDirectoryName(full)!, $".vibetable-{Guid.NewGuid():N}.tmp");
                string id = "download-" + Guid.NewGuid().ToString("N");
                var download = new Download(id, full, temporary, lease, _time.GetUtcNow() + DownloadLifetime);
                _commitCurrent(() => { _retired.Token.ThrowIfCancellationRequested(); _downloads.Add(id, download); });
                return new HostDownloadTarget(id, temporary, name, download.Deadline);
            }
            finally { _gate.Release(); }
        }
        catch
        {
            // Retirement may cancel the gate wait before the download entry
            // exists; the captured epoch lease must still be released so the
            // pending drain cannot wait on it forever.
            lease.Dispose();
            throw;
        }
    }

    /// <summary>
    /// Attaches the WebView-side termination for one admitted download. The
    /// drain path uses it to cancel the WebView2 download operation before
    /// any cleanup, so the temporary file is never deleted while WebView2
    /// still writes it.
    /// </summary>
    internal async Task<bool> AttachDownloadTerminationAsync(string id, Func<Task> terminate)
    {
        ArgumentNullException.ThrowIfNull(terminate);
        await _gate.WaitAsync().ConfigureAwait(false);
        try
        {
            if (IsRetired || !_downloads.TryGetValue(id, out Download? download)
                || download.Outcome is not null || _time.GetUtcNow() >= download.Deadline) return false;
            download.Admitted = true;
            lock (_terminations) _terminations[id] = terminate;
            return true;
        }
        finally { _gate.Release(); }
    }

    internal async Task ExpireDownloadAsync(string id, Action expired)
    {
        await InvalidateDownloadAsync(id, expired: true, expired).ConfigureAwait(false);
    }

    internal Task<bool?> RetireDownloadAsync(string id, Action retired)
        => InvalidateDownloadAsync(id, expired: false, retired);

    private async Task<bool?> InvalidateDownloadAsync(string id, bool expired, Action invalidated)
    {
        Func<Task>? terminate = null;
        await _gate.WaitAsync().ConfigureAwait(false);
        try
        {
            if (_downloads.TryGetValue(id, out Download? download))
            {
                if (download.Outcome is true) return false;
                if (download.Expired && expired) return true;
                download.Expired |= expired;
                download.Revoked |= !expired;
                download.Outcome = false;
                lock (_terminations) _terminations.TryGetValue(id, out terminate);
                if (terminate is null && !download.Admitted) FinalizeUncommitted(download);
            }
            else if (!expired) return null;
        }
        finally { _gate.Release(); }
        // The receipt means commits are now impossible, not that WebView stopped.
        invalidated();
        if (terminate is not null)
            await terminate().WaitAsync(_drainDownloadSettlementTimeout).ConfigureAwait(false);
        return true;
    }

    /// <summary>
    /// Settles one host download. A committed download atomically replaces
    /// the selected target through the epoch-checked commit boundary; any
    /// other outcome deletes the temporary file and leaves the target
    /// untouched. Settlement stays reachable after retirement so a WebView
    /// write that outlives the drain can always be cleaned up; committing
    /// after retirement is refused. Idempotent for a matching outcome.
    /// </summary>
    internal async Task CompleteDownloadAsync(string id, bool commit)
    {
        Download? download = null;
        await _gate.WaitAsync().ConfigureAwait(false);
        try
        {
            if (!_downloads.TryGetValue(id, out download))
                throw new InvalidOperationException("Dashboard download is unavailable.");
            if (download.Outcome is bool settled)
            {
                if (settled != commit)
                {
                    // CompleteDownloadAsync is a terminal callback. Even a
                    // revoked commit must finish cleanup before it is refused.
                    if (!settled) FinalizeUncommitted(download);
                    if (download.Expired) throw new TimeoutException("SAVE_TIMEOUT");
                    if (download.Revoked) throw new OperationCanceledException("Export retired.");
                    _retired.Token.ThrowIfCancellationRequested();
                    throw new IOException("Dashboard download outcome is already settled.");
                }
                // A drain-side settlement defers cleanup to the WebView cancel;
                // a matching late settlement performs it exactly once.
                if (!settled) FinalizeUncommitted(download);
            }
            else if (commit)
            {
                try
                {
                    _commitCurrent(() =>
                    {
                        download.Lease.CancellationToken.ThrowIfCancellationRequested();
                        _retired.Token.ThrowIfCancellationRequested();
                        if (_time.GetUtcNow() >= download.Deadline)
                            throw new TimeoutException("SAVE_TIMEOUT");
                        File.Move(download.Temporary, download.Target, overwrite: true);
                    });
                }
                catch
                {
                    download.Outcome = false;
                    FinalizeUncommitted(download);
                    throw;
                }
                download.Outcome = true;
                download.ReceiptExpiresAt = _time.GetUtcNow().AddMinutes(5).ToUnixTimeMilliseconds() / 1000.0;
                lock (_terminations) _terminations.Remove(id);
                download.Settlement.TrySetResult();
                download.Lease.Dispose();
            }
            else
            {
                download.Outcome = false;
                FinalizeUncommitted(download);
            }
        }
        finally { _gate.Release(); }
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

    internal async Task RevokeRunAsync(string runId)
    {
        await _gate.WaitAsync().ConfigureAwait(false);
        try
        {
            foreach (Grant grant in _grants.Values.Where(item => item.RunId == runId))
            {
                grant.State = "revoked";
                foreach (Transfer transfer in _transfers.Values.Where(item => ReferenceEquals(item.Grant, grant)))
                    await CloseAsync(transfer).ConfigureAwait(false);
            }
        }
        finally { _gate.Release(); }
    }
    public void Retire()
    {
        if (Interlocked.Exchange(ref _retireStarted, 1) != 0) return;
        _retired.Cancel();
        _ = DrainAsync();
    }

    public Task DrainCompletion => _drain.Task;
    internal bool IsRetired => _retired.IsCancellationRequested;

    private async Task DrainAsync()
    {
        var errors = new List<Exception>();
        var pending = new List<(Download Download, Func<Task> Terminate)>();
        await _gate.WaitAsync().ConfigureAwait(false);
        try
        {
            foreach (Grant grant in _grants.Values) grant.State = "revoked";
            foreach (Transfer transfer in _transfers.Values)
            {
                try { await CloseAsync(transfer).ConfigureAwait(false); }
                catch (Exception error) { errors.Add(error); }
            }
            foreach (KeyValuePair<string, Download> pair in _downloads)
            {
                // Retirement settles any in-flight download as cancelled so
                // an old session can never commit afterwards; the WebView
                // write itself is terminated below, and cleanup happens only
                // after that termination is observed.
                if (pair.Value.Outcome is null) pair.Value.Outcome = false;
                if (pair.Value.Outcome is not true)
                {
                    Func<Task>? terminate = TakeTermination(pair.Key);
                    if (terminate is not null) pending.Add((pair.Value, terminate));
                    else if (pair.Value.ReceiptExpiresAt is null || pair.Value.CleanupPending)
                    {
                        try { FinalizeUncommitted(pair.Value); }
                        catch (Exception error) { errors.Add(error); }
                    }
                }
            }
        }
        finally
        {
            _gate.Release();
        }
        if (pending.Count > 0)
        {
            // Terminate outside the gate: the resulting settlement needs the
            // gate for its cleanup, so terminate must not run under it.
            var settlements = new List<Task>();
            foreach ((Download download, Func<Task> terminate) in pending)
            {
                settlements.Add(download.Settlement.Task);
                try { settlements.Add(terminate()); }
                catch (Exception error) { errors.Add(error); }
            }
            try
            {
                await Task.WhenAll(settlements)
                    .WaitAsync(_drainDownloadSettlementTimeout).ConfigureAwait(false);
            }
            catch (Exception error)
            {
                // Preserve the writer and lease until a terminal callback.
                // Timeout is a drain failure, never proof that writing stopped.
                errors.Add(error);
            }
        }
        _epochRegistration?.Unregister();
        if (errors.Count > 0) _drain.TrySetException(errors);
        else _drain.TrySetResult();
    }

    private void PruneExpiredReceipts()
    {
        double now = _time.GetUtcNow().ToUnixTimeMilliseconds() / 1000.0;
        foreach (string id in _transfers.Where(pair => pair.Value.Closed && pair.Value.ReceiptExpiresAt <= now)
            .Select(pair => pair.Key).ToArray()) _transfers.Remove(id);
        foreach (string id in _reservations.Where(pair => pair.Value.Outcome is not null && pair.Value.ReceiptExpiresAt <= now)
            .Select(pair => pair.Key).ToArray()) _reservations.Remove(id);
        foreach (string id in _downloads.Where(pair => !pair.Value.CleanupPending
            && pair.Value.ReceiptExpiresAt is not null && pair.Value.ReceiptExpiresAt <= now)
            .Select(pair => pair.Key).ToArray())
        {
            _downloads.Remove(id);
            lock (_terminations) _terminations.Remove(id);
        }
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
        CommitOwner(grant, () => { });
        return grant;
    }

    private static void CommitOwner(Grant grant, Action action)
    {
        if (grant.OwnerCommit is null) action();
        else if (!grant.OwnerCommit(action)) throw new HostPathGrantException();
    }

    private Transfer FindTransfer(JsonElement p)
        => _transfers.TryGetValue(Text(p, "transferId"), out Transfer? transfer) ? transfer
            : throw new InvalidOperationException("File transfer is unavailable.");

    private Transfer Active(JsonElement p, bool write)
    {
        Transfer transfer = FindTransfer(p);
        if (transfer.Closed || transfer.Grant.Write != write || transfer.Grant.State != (write ? "writing" : "reading"))
            throw new InvalidOperationException("File transfer is not active.");
        CommitOwner(transfer.Grant, () => { });
        if (transfer.Grant.RunId is not null && transfer.Grant.ExpiresAt <= _time.GetUtcNow().ToUnixTimeMilliseconds() / 1000.0) throw new HostPathGrantException();
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
    private sealed record Grant(string Id, string Path, bool Write, string? RunId, double ExpiresAt, long? Size, string? MimeType, Func<Action, bool>? OwnerCommit, string Purpose, long ByteLimit, Func<CancellationToken, Task>? Authorize)
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
    /// <summary>
    /// Performs the cleanup of one uncommitted download: delete the
    /// temporary file, stamp the receipt, release the WebView termination,
    /// complete the settlement task, and release the epoch lease so a
    /// pending drain can finish. Only a terminal download or a registration
    /// with no admitted WebView writer may enter this cleanup.
    /// </summary>
    private void FinalizeUncommitted(Download download)
    {
        try
        {
            File.Delete(download.Temporary);
            download.CleanupPending = false;
            download.Settlement.TrySetResult();
        }
        catch (Exception error)
        {
            download.CleanupPending = true;
            download.Settlement.TrySetException(error);
            throw;
        }
        finally
        {
            // The writer is terminal even when deleting its residual file fails.
            // Keep its path for a matching cleanup retry or session drain, while
            // releasing the active slot and epoch lease independently.
            download.ReceiptExpiresAt ??= _time.GetUtcNow().AddMinutes(5).ToUnixTimeMilliseconds() / 1000.0;
            lock (_terminations) _terminations.Remove(download.Id);
            download.Lease.Dispose();
        }
    }

    private Func<Task>? TakeTermination(string id)
    {
        lock (_terminations)
        {
            return _terminations.Remove(id, out Func<Task>? terminate) ? terminate : null;
        }
    }

    private sealed record Download(string Id, string Target, string Temporary,
        WorkspaceRequestEpochLease Lease, DateTimeOffset Deadline)
    {
        internal bool Expired { get; set; }
        internal bool Revoked { get; set; }
        internal bool Admitted { get; set; }
        internal bool CleanupPending { get; set; }
        internal bool? Outcome { get; set; }
        internal double? ReceiptExpiresAt { get; set; }
        internal TaskCompletionSource Settlement { get; } =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
    }
}

/// <summary>One admitted host download before its WebView2 bytes arrive.</summary>
internal sealed record HostDownloadTarget(string Id, string TemporaryPath, string DisplayName,
    DateTimeOffset Deadline);
