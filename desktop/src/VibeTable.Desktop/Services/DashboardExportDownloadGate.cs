using System.Diagnostics;
using System.IO;
using VibeTable.Infrastructure.Diagnostics;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>One admitted dashboard download before settlement.</summary>
internal sealed record DashboardDownloadAdmission(
    string Token,
    string BlobUrl,
    string TemporaryPath,
    string DisplayName,
    long DeclaredByteLength,
    HostSessionFileBroker Broker)
{
    internal DateTime RegisteredAt { get; init; } = DateTime.UtcNow;
    private int _settled;
    private string? _failureReason;

    public bool MarkSettling() => Interlocked.Exchange(ref _settled, 1) == 0;
    public bool RecordFailure(string reason)
        => Interlocked.CompareExchange(ref _failureReason, reason, null) is null;
    public string? FailureReason => Volatile.Read(ref _failureReason);
}

/// <summary>
/// One-shot exact-URI admission for dashboard blob downloads plus their
/// settlement through the session file broker. Every other download is the
/// WebView's default behaviour and is rejected by the bridge.
/// </summary>
internal sealed class DashboardExportDownloadGate
{
    private static readonly TimeSpan RegistrationLifetime = TimeSpan.FromMinutes(5);

    /// <summary>
    /// The single download budget shared by the request check and the
    /// settlement check. It bounds the bytes WebView2 may write for one
    /// dashboard export; no content hash is added.
    /// </summary>
    internal const long MaxExportBytes = 32L * 1024 * 1024;

    private readonly Action<object?> _postSettled;
    private readonly Action<string>? _trace;
    private readonly object _gate = new();
    private readonly Dictionary<string, DashboardDownloadAdmission> _registrations =
        new(StringComparer.Ordinal);

    public DashboardExportDownloadGate(
        Action<object?> postSettled,
        Action<string>? trace = null)
    {
        _postSettled = postSettled ?? throw new ArgumentNullException(nameof(postSettled));
        _trace = trace;
    }

    /// <summary>Registers the exact blob URL for one pending download.</summary>
    public bool TryRegister(
        HostDownloadTarget target,
        HostSessionFileBroker broker,
        string blobUrl,
        long declaredByteLength)
    {
        ArgumentException.ThrowIfNullOrEmpty(blobUrl);
        if (declaredByteLength is <= 0 or > MaxExportBytes) return false;
        lock (_gate)
        {
            PruneExpired();
            if (_registrations.Count >= 8) return false;
            _registrations[target.Id] = new DashboardDownloadAdmission(
                target.Id,
                blobUrl,
                target.TemporaryPath,
                target.DisplayName,
                declaredByteLength,
                broker);
            return true;
        }
    }

    /// <summary>
    /// Matches a download URI against the pending registrations. The exact
    /// blob URL is consumed on the first match; no second download may reuse
    /// the registration.
    /// </summary>
    public bool TryAdmit(string? downloadUri, out DashboardDownloadAdmission? admission)
    {
        admission = null;
        if (string.IsNullOrEmpty(downloadUri)) return false;
        lock (_gate)
        {
            PruneExpired();
            foreach (DashboardDownloadAdmission candidate in _registrations.Values)
            {
                if (!string.Equals(candidate.BlobUrl, downloadUri, StringComparison.Ordinal))
                    continue;
                admission = candidate;
                _registrations.Remove(candidate.Token);
                return true;
            }
        }
        return false;
    }

    /// <summary>
    /// Settles one download. Before a commit replaces the user-selected
    /// target, the bytes WebView2 actually wrote are verified against the
    /// declared length and the fixed budget; a violation is a truthful
    /// failure, never a cancelled outcome. An interrupted download only
    /// cleans up its temporary file.
    /// </summary>
    public Task SettleAsync(
        DashboardDownloadAdmission admission,
        bool completed,
        string? interruptReason)
        => SettleAsync(
            admission,
            admission.FailureReason is not null ? "failed"
                : completed ? "saved"
                : interruptReason is "UserCanceled" or "Expired" or "SessionRetired"
                    ? "cancelled" : "failed",
            admission.FailureReason ?? interruptReason);

    private async Task SettleAsync(
        DashboardDownloadAdmission admission,
        string outcome,
        string? reason)
    {
        ArgumentNullException.ThrowIfNull(admission);
        if (!admission.MarkSettling()) return;
        bool commit = outcome == "saved";
        if (commit && admission.Broker.IsRetired)
        {
            commit = false;
            outcome = "cancelled";
            reason = "SessionRetired";
        }
        if (commit)
        {
            long? actual = TryMeasure(admission.TemporaryPath);
            if (actual is null)
            {
                commit = false;
                outcome = "failed";
                reason = "TEMPORARY_UNAVAILABLE";
                _trace?.Invoke(
                    $"Dashboard download temporary file is missing; token={admission.Token}");
            }
            else if (actual.Value > MaxExportBytes)
            {
                commit = false;
                outcome = "failed";
                reason = "EXPORT_TOO_LARGE";
                _trace?.Invoke(
                    $"Dashboard download exceeded the byte budget; " +
                    $"token={admission.Token}; declared={admission.DeclaredByteLength}; " +
                    $"actual={actual.Value}");
            }
            else if (actual.Value != admission.DeclaredByteLength)
            {
                commit = false;
                outcome = "failed";
                reason = "BYTE_LENGTH_MISMATCH";
                _trace?.Invoke(
                    $"Dashboard download byte length mismatch; " +
                    $"token={admission.Token}; declared={admission.DeclaredByteLength}; " +
                    $"actual={actual.Value}");
            }
        }
        try
        {
            await admission.Broker.CompleteDownloadAsync(admission.Token, commit)
                .ConfigureAwait(false);
        }
        catch (Exception error) when (
            error is OperationCanceledException or ObjectDisposedException
            or BackendUnavailableException)
        {
            if (outcome == "saved")
            {
                outcome = "cancelled";
                reason = reason ?? error.GetType().Name;
            }
            _trace?.Invoke(
                $"Dashboard download settle rejected; token={admission.Token}; " +
                $"exception={error.GetType().Name}");
        }
        catch (Exception error)
        {
            if (outcome != "failed") reason = error.GetType().Name;
            outcome = "failed";
            reason = reason ?? error.GetType().Name;
            Trace.TraceError(DiagnosticEvent.Failure(
                "VibeTable.Desktop.DashboardExportDownloadGate",
                admission.Token,
                error.GetType().Name));
        }
        _postSettled(new
        {
            token = admission.Token,
            outcome,
            reason,
        });
    }

    private static long? TryMeasure(string path)
    {
        try
        {
            return new FileInfo(path).Length;
        }
        catch (IOException)
        {
            return null;
        }
        catch (UnauthorizedAccessException)
        {
            return null;
        }
        catch (NotSupportedException)
        {
            return null;
        }
    }

    private void PruneExpired()
    {
        // Called under the gate lock. Registrations without an admitted
        // download expire after the fixed lifetime so stale grants cannot
        // accumulate; settled admissions are removed on admit.
        DateTime now = DateTime.UtcNow;
        foreach (KeyValuePair<string, DashboardDownloadAdmission> expired in _registrations
            .Where(pair => now - pair.Value.RegisteredAt > RegistrationLifetime)
            .ToArray())
        {
            _registrations.Remove(expired.Key);
            // Release the broker download and tell the renderer the truthful
            // outcome instead of leaving the promise to a renderer timeout.
            _ = SettleAsync(expired.Value, completed: false, "Expired");
        }
    }
}
