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
    private int _settled;
    private int _reported;
    private int _admitted;
    private int _retired;
    private string? _failureReason;
    internal ITimer? DeadlineTimer { get; set; }
    internal CancellationTokenRegistration RetirementRegistration { get; set; }

    public bool MarkSettling() => Interlocked.Exchange(ref _settled, 1) == 0;
    public bool RecordFailure(string reason)
        => Interlocked.CompareExchange(ref _failureReason, reason, null) is null;
    public string? FailureReason => Volatile.Read(ref _failureReason);
    public bool MarkReported() => Interlocked.Exchange(ref _reported, 1) == 0;
    public bool Admitted => Volatile.Read(ref _admitted) != 0;
    public bool MarkAdmitted() => Interlocked.Exchange(ref _admitted, 1) == 0;
    public bool Retired => Volatile.Read(ref _retired) != 0;
    public void Retire() => Interlocked.Exchange(ref _retired, 1);
}

/// <summary>
/// One-shot exact-URI admission for dashboard blob downloads plus their
/// settlement through the session file broker. Every other download is the
/// WebView's default behaviour and is rejected by the bridge.
/// </summary>
internal sealed class DashboardExportDownloadGate
{
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
            if (_registrations.Values.Count(item => !item.Admitted) >= 8) return false;
            var admission = new DashboardDownloadAdmission(
                target.Id,
                blobUrl,
                target.TemporaryPath,
                target.DisplayName,
                declaredByteLength,
                broker);
            _registrations[target.Id] = admission;
            admission.DeadlineTimer = broker.DownloadTime.CreateTimer(
                _ => _ = ExpireAsync(admission), null, Timeout.InfiniteTimeSpan,
                Timeout.InfiniteTimeSpan);
            admission.DeadlineTimer.Change(
                TimeSpan.FromTicks(Math.Max(0, (target.Deadline - broker.DownloadTime.GetUtcNow()).Ticks)),
                Timeout.InfiniteTimeSpan);
            admission.RetirementRegistration = broker.DownloadRetirement.Register(
                () => RetireUnadmitted(admission));
            if (admission.Retired) admission.RetirementRegistration.Unregister();
            return !admission.Retired;
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
            foreach (DashboardDownloadAdmission candidate in _registrations.Values)
            {
                if (!string.Equals(candidate.BlobUrl, downloadUri, StringComparison.Ordinal))
                    continue;
                if (candidate.Retired || !candidate.MarkAdmitted()) continue;
                admission = candidate;
                return true;
            }
        }
        return false;
    }

    private void RetireUnadmitted(DashboardDownloadAdmission admission)
    {
        lock (_gate)
        {
            if (admission.Admitted) return;
            admission.Retire();
            _registrations.Remove(admission.Token);
        }
        // Cancellation may run under the broker lock; settlement must stay asynchronous.
        _ = SettleAsync(admission, completed: false, "SessionRetired");
    }

    public async Task<bool?> RetireAsync(string token, HostSessionFileBroker broker)
    {
        DashboardDownloadAdmission? admission;
        lock (_gate)
        {
            if (_registrations.TryGetValue(token, out admission)
                && !ReferenceEquals(admission.Broker, broker)) return null;
        }
        bool? retired = await broker.RetireDownloadAsync(token, () =>
        {
            admission?.Retire();
            admission?.DeadlineTimer?.Dispose();
        }).ConfigureAwait(false);
        if (retired is true && admission is { Admitted: false })
            await SettleAsync(admission, completed: false, "SessionRetired").ConfigureAwait(false);
        return retired;
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
                : admission.Retired ? "cancelled"
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
        lock (_gate) _registrations.Remove(admission.Token);
        admission.DeadlineTimer?.Dispose();
        admission.RetirementRegistration.Unregister();
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
            if (outcome != "failed") reason = error is TimeoutException ? "SAVE_TIMEOUT" : error.GetType().Name;
            outcome = "failed";
            reason = reason ?? error.GetType().Name;
            Trace.TraceError(DiagnosticEvent.Failure(
                "VibeTable.Desktop.DashboardExportDownloadGate",
                admission.Token,
                error.GetType().Name));
        }
        Report(admission, outcome, reason);
    }

    private void Report(DashboardDownloadAdmission admission, string outcome, string? reason)
    {
        if (!admission.MarkReported()) return;
        _postSettled(new
        {
            token = admission.Token,
            outcome,
            reason,
        });
    }

    private async Task ExpireAsync(DashboardDownloadAdmission admission)
    {
        lock (_gate) _registrations.Remove(admission.Token);
        admission.DeadlineTimer?.Dispose();
        admission.RetirementRegistration.Unregister();
        try
        {
            await admission.Broker.ExpireDownloadAsync(admission.Token, () =>
            {
                admission.RecordFailure("SAVE_TIMEOUT");
                Report(admission, "failed", "SAVE_TIMEOUT");
            }).ConfigureAwait(false);
        }
        catch (Exception error)
        {
            admission.RecordFailure("SAVE_TIMEOUT");
            Report(admission, "failed", "SAVE_TIMEOUT");
            Trace.TraceError(DiagnosticEvent.Failure(
                "VibeTable.Desktop.DashboardExportDownloadGate", admission.Token, error.GetType().Name));
        }
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
}
