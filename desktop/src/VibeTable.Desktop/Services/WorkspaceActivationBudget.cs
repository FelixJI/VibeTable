using System.Globalization;

namespace VibeTable.Desktop.Services;

public enum WorkspaceActivationStage
{
    Manifest = 0,
    Sidecar = 1,
    Backend = 2,
    Verification = 3,
}

public enum WorkspaceActivationOutcome
{
    InProgress = 0,
    Completed = 1,
    Failed = 2,
    TimedOut = 3,
    Cancelled = 4,
}

public sealed class WorkspaceActivationPolicy
{
    public static readonly TimeSpan DefaultVerificationTimeout =
        TimeSpan.FromSeconds(10);
    public static readonly TimeSpan DefaultSchedulingHeadroom =
        TimeSpan.FromSeconds(5);
    public static WorkspaceActivationPolicy Default { get; } =
        FromStageTimeouts(
            TimeSpan.FromSeconds(30),
            TimeSpan.FromSeconds(10));

    public WorkspaceActivationPolicy(
        TimeSpan totalTimeout,
        TimeSpan sidecarTimeout,
        TimeSpan backendTimeout,
        TimeSpan verificationTimeout)
    {
        if (sidecarTimeout <= TimeSpan.Zero)
            throw new ArgumentOutOfRangeException(nameof(sidecarTimeout));
        if (backendTimeout <= TimeSpan.Zero)
            throw new ArgumentOutOfRangeException(nameof(backendTimeout));
        if (verificationTimeout <= TimeSpan.Zero)
            throw new ArgumentOutOfRangeException(nameof(verificationTimeout));
        if (totalTimeout <= sidecarTimeout + backendTimeout)
        {
            throw new ArgumentOutOfRangeException(
                nameof(totalTimeout),
                "The activation deadline must exceed the sidecar and backend stage budgets.");
        }
        TotalTimeout = totalTimeout;
        SidecarTimeout = sidecarTimeout;
        BackendTimeout = backendTimeout;
        VerificationTimeout = verificationTimeout;
    }

    public TimeSpan TotalTimeout { get; }
    public TimeSpan SidecarTimeout { get; }
    public TimeSpan BackendTimeout { get; }
    public TimeSpan VerificationTimeout { get; }

    public static WorkspaceActivationPolicy FromStageTimeouts(
        TimeSpan sidecarTimeout,
        TimeSpan backendTimeout)
    {
        TimeSpan total = sidecarTimeout
            + backendTimeout
            + DefaultVerificationTimeout
            + DefaultSchedulingHeadroom;
        return new WorkspaceActivationPolicy(
            total,
            sidecarTimeout,
            backendTimeout,
            DefaultVerificationTimeout);
    }

    internal TimeSpan LimitFor(WorkspaceActivationStage stage) => stage switch
    {
        WorkspaceActivationStage.Manifest => VerificationTimeout,
        WorkspaceActivationStage.Sidecar => SidecarTimeout,
        WorkspaceActivationStage.Backend => BackendTimeout,
        WorkspaceActivationStage.Verification => VerificationTimeout,
        _ => throw new ArgumentOutOfRangeException(nameof(stage)),
    };
}

public sealed record WorkspaceActivationStageTiming(
    WorkspaceActivationStage Stage,
    TimeSpan Duration,
    WorkspaceActivationOutcome Outcome);

public sealed record WorkspaceActivationReport(
    Guid WorkspaceId,
    ulong SessionEpoch,
    TimeSpan Elapsed,
    WorkspaceActivationStage? LastStage,
    WorkspaceActivationOutcome Outcome,
    IReadOnlyList<WorkspaceActivationStageTiming> Stages);

public sealed class WorkspaceActivationTimeoutException : TimeoutException
{
    public const string ErrorCode = "workspace.activation_timeout";

    public WorkspaceActivationTimeoutException(
        WorkspaceActivationStage stage,
        TimeSpan elapsed,
        Exception? innerException = null)
        : base(
            "workspace.activation_timeout: "
            + $"stage={StageName(stage)}; "
            + $"elapsedMs={elapsed.TotalMilliseconds.ToString("F0", CultureInfo.InvariantCulture)}",
            innerException)
    {
        Stage = stage;
    }

    public WorkspaceActivationStage Stage { get; }
    public string Code => ErrorCode;

    private static string StageName(WorkspaceActivationStage stage) => stage switch
    {
        WorkspaceActivationStage.Manifest => "manifest",
        WorkspaceActivationStage.Sidecar => "sidecar",
        WorkspaceActivationStage.Backend => "backend",
        WorkspaceActivationStage.Verification => "verify",
        _ => "unknown",
    };
}

/// <summary>
/// Owns one absolute workspace activation deadline and all stage evidence.
/// Callers declare stages; they cannot reset or independently extend the deadline.
/// </summary>
public sealed class WorkspaceActivationBudget : IDisposable
{
    private readonly Guid _workspaceId;
    private readonly ulong _sessionEpoch;
    private readonly WorkspaceActivationPolicy _policy;
    private readonly CancellationToken _callerToken;
    private readonly TimeProvider _timeProvider;
    private readonly Action<string> _trace;
    private readonly long _startedAt;
    private readonly List<WorkspaceActivationStageTiming> _stages = [];
    private WorkspaceActivationOutcome _outcome = WorkspaceActivationOutcome.InProgress;
    private WorkspaceActivationStage? _lastStage;
    private bool _stageRunning;
    private bool _disposed;

    private WorkspaceActivationBudget(
        Guid workspaceId,
        ulong sessionEpoch,
        WorkspaceActivationPolicy policy,
        CancellationToken callerToken,
        TimeProvider timeProvider,
        Action<string>? trace)
    {
        if (workspaceId == Guid.Empty)
            throw new ArgumentException("Workspace id must be non-empty.", nameof(workspaceId));
        _workspaceId = workspaceId;
        _sessionEpoch = sessionEpoch;
        _policy = policy ?? throw new ArgumentNullException(nameof(policy));
        _callerToken = callerToken;
        _timeProvider = timeProvider ?? throw new ArgumentNullException(nameof(timeProvider));
        _trace = trace ?? (_ => { });
        _startedAt = _timeProvider.GetTimestamp();
    }

    public WorkspaceActivationReport Report => Snapshot();

    public static WorkspaceActivationBudget Begin(
        Guid workspaceId,
        ulong sessionEpoch,
        WorkspaceActivationPolicy policy,
        CancellationToken callerToken,
        TimeProvider? timeProvider = null,
        Action<string>? trace = null)
        => new(
            workspaceId,
            sessionEpoch,
            policy,
            callerToken,
            timeProvider ?? TimeProvider.System,
            trace);

    public async Task RunAsync(
        WorkspaceActivationStage stage,
        Func<CancellationToken, Task> action)
    {
        ArgumentNullException.ThrowIfNull(action);
        ObjectDisposedException.ThrowIf(_disposed, this);
        if (_outcome != WorkspaceActivationOutcome.InProgress)
            throw new InvalidOperationException("Workspace activation is already complete.");
        if (_stageRunning)
            throw new InvalidOperationException("Workspace activation stages cannot overlap.");

        _callerToken.ThrowIfCancellationRequested();
        TimeSpan remaining = _policy.TotalTimeout - Elapsed();
        if (remaining <= TimeSpan.Zero)
        {
            Record(stage, TimeSpan.Zero, WorkspaceActivationOutcome.TimedOut);
            throw new WorkspaceActivationTimeoutException(stage, Elapsed());
        }
        TimeSpan stageTimeout = _policy.LimitFor(stage);
        TimeSpan allowed = remaining < stageTimeout ? remaining : stageTimeout;
        long stageStartedAt = _timeProvider.GetTimestamp();
        _stageRunning = true;
        using var deadline = CancellationTokenSource.CreateLinkedTokenSource(_callerToken);
        var deadlineExpired = 0;
        using ITimer timer = _timeProvider.CreateTimer(
            _ =>
            {
                Interlocked.Exchange(ref deadlineExpired, 1);
                try
                {
                    deadline.Cancel();
                }
                catch (ObjectDisposedException)
                {
                    // Stage completion won the race with its deadline callback.
                }
            },
            null,
            allowed,
            Timeout.InfiniteTimeSpan);
        try
        {
            await action(deadline.Token).ConfigureAwait(false);
            timer.Change(Timeout.InfiniteTimeSpan, Timeout.InfiniteTimeSpan);
            TimeSpan stageElapsed = _timeProvider.GetElapsedTime(stageStartedAt);
            if (_callerToken.IsCancellationRequested)
                throw new OperationCanceledException(_callerToken);
            if (Volatile.Read(ref deadlineExpired) != 0
                || stageElapsed >= allowed
                || Elapsed() >= _policy.TotalTimeout)
            {
                Record(
                    stage,
                    stageElapsed,
                    WorkspaceActivationOutcome.TimedOut);
                throw new WorkspaceActivationTimeoutException(stage, Elapsed());
            }
            Record(
                stage,
                stageElapsed,
                WorkspaceActivationOutcome.Completed);
        }
        catch (OperationCanceledException) when (
            !_callerToken.IsCancellationRequested
            && Volatile.Read(ref deadlineExpired) != 0)
        {
            Record(
                stage,
                _timeProvider.GetElapsedTime(stageStartedAt),
                WorkspaceActivationOutcome.TimedOut);
            throw new WorkspaceActivationTimeoutException(stage, Elapsed());
        }
        catch (OperationCanceledException) when (_callerToken.IsCancellationRequested)
        {
            Record(
                stage,
                _timeProvider.GetElapsedTime(stageStartedAt),
                WorkspaceActivationOutcome.Cancelled);
            throw;
        }
        catch (WorkspaceActivationTimeoutException)
        {
            throw;
        }
        catch (TimeoutException error)
        {
            Record(
                stage,
                _timeProvider.GetElapsedTime(stageStartedAt),
                WorkspaceActivationOutcome.TimedOut);
            throw new WorkspaceActivationTimeoutException(stage, Elapsed(), error);
        }
        catch
        {
            Record(
                stage,
                _timeProvider.GetElapsedTime(stageStartedAt),
                WorkspaceActivationOutcome.Failed);
            throw;
        }
        finally
        {
            _stageRunning = false;
        }
    }

    public WorkspaceActivationReport Complete()
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        if (_stageRunning)
            throw new InvalidOperationException("A workspace activation stage is still running.");
        if (_outcome == WorkspaceActivationOutcome.InProgress)
        {
            _outcome = WorkspaceActivationOutcome.Completed;
            Trace("completed", null, _outcome);
        }
        return Snapshot();
    }

    public WorkspaceActivationReport Fail(Exception error)
    {
        ArgumentNullException.ThrowIfNull(error);
        ObjectDisposedException.ThrowIf(_disposed, this);
        if (_outcome == WorkspaceActivationOutcome.InProgress)
        {
            _outcome = error is WorkspaceActivationTimeoutException
                ? WorkspaceActivationOutcome.TimedOut
                : error is OperationCanceledException
                    && _callerToken.IsCancellationRequested
                    ? WorkspaceActivationOutcome.Cancelled
                : WorkspaceActivationOutcome.Failed;
        }
        string eventName = _outcome switch
        {
            WorkspaceActivationOutcome.TimedOut => "timed_out",
            WorkspaceActivationOutcome.Cancelled => "cancelled",
            _ => "failed",
        };
        Trace(eventName, _lastStage, _outcome);
        return Snapshot();
    }

    internal void RecordSidecarStartup(
        TimeSpan? spawnDuration,
        TimeSpan? readyRecordDuration,
        TimeSpan? healthDuration,
        string lastStage)
    {
        TraceSidecarPhase("spawn", spawnDuration);
        TraceSidecarPhase("ready_record", readyRecordDuration);
        TraceSidecarPhase("health", healthDuration);
        _trace(
            "workspace.activation.sidecar.last_stage "
            + $"workspaceId={_workspaceId:D}; "
            + $"sessionEpoch={_sessionEpoch.ToString(CultureInfo.InvariantCulture)}; "
            + $"stage={lastStage}");
    }

    public void Dispose()
    {
        _disposed = true;
    }

    private void Record(
        WorkspaceActivationStage stage,
        TimeSpan duration,
        WorkspaceActivationOutcome outcome)
    {
        _lastStage = stage;
        _stages.Add(new WorkspaceActivationStageTiming(stage, duration, outcome));
        if (outcome is WorkspaceActivationOutcome.TimedOut
            or WorkspaceActivationOutcome.Cancelled)
        {
            _outcome = outcome;
        }
        Trace(outcome switch
        {
            WorkspaceActivationOutcome.Completed => "completed",
            WorkspaceActivationOutcome.TimedOut => "timed_out",
            WorkspaceActivationOutcome.Cancelled => "cancelled",
            _ => "failed",
        }, stage, outcome);
    }

    private WorkspaceActivationReport Snapshot() => new(
        _workspaceId,
        _sessionEpoch,
        Elapsed(),
        _lastStage,
        _outcome,
        _stages.ToArray());

    private TimeSpan Elapsed() => _timeProvider.GetElapsedTime(_startedAt);

    private void Trace(
        string eventName,
        WorkspaceActivationStage? stage,
        WorkspaceActivationOutcome outcome)
    {
        string stageName = stage switch
        {
            WorkspaceActivationStage.Manifest => "manifest",
            WorkspaceActivationStage.Sidecar => "sidecar",
            WorkspaceActivationStage.Backend => "backend",
            WorkspaceActivationStage.Verification => "verify",
            _ => "activation",
        };
        _trace(
            $"workspace.activation.{stageName}.{eventName} "
            + $"workspaceId={_workspaceId:D}; "
            + $"sessionEpoch={_sessionEpoch.ToString(CultureInfo.InvariantCulture)}; "
            + $"elapsedMs={Elapsed().TotalMilliseconds.ToString("F0", CultureInfo.InvariantCulture)}; "
            + $"outcome={outcome.ToString().ToLowerInvariant()}");
    }

    private void TraceSidecarPhase(string phase, TimeSpan? duration)
    {
        if (duration is null) return;
        _trace(
            $"workspace.activation.sidecar.{phase}.completed "
            + $"workspaceId={_workspaceId:D}; "
            + $"sessionEpoch={_sessionEpoch.ToString(CultureInfo.InvariantCulture)}; "
            + $"durationMs={duration.Value.TotalMilliseconds.ToString("F0", CultureInfo.InvariantCulture)}");
    }
}
