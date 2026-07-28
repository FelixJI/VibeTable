using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

public interface IWorkspaceRequestDrainHook
{
    Task DrainAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken);
    void Resume(Guid workspaceId, ulong sessionEpoch);
}

/// <summary>
/// Validates workspace envelopes and owns the complete lifetime of requests
/// admitted for one epoch. Drain stops admission, cancels all leases, and
/// waits until each lease is disposed before Sidecar high-watermark drain.
/// </summary>
public sealed class WorkspaceSessionEnvelopeFilter :
    IWorkspaceRequestDrainHook,
    IDisposable
{
    private readonly object _gate = new();
    private readonly WorkspaceSessionManager _sessions;
    private EpochState _epoch = EpochState.Empty();
    private ulong _lastAcceptedSequence;
    private bool _disposed;

    public WorkspaceSessionEnvelopeFilter(WorkspaceSessionManager sessions)
    {
        _sessions = sessions
            ?? throw new ArgumentNullException(nameof(sessions));
        EpochState? retired;
        lock (_gate)
            retired = SynchronizeLocked(_sessions.Current);
        Retire(retired);
        _sessions.Changed += OnSessionChanged;
    }

    public bool TryCapture(
        WorkspaceWireScope? scope,
        out WorkspaceRequestEpochLease? lease)
    {
        lease = null;
        if (scope is null || !_sessions.Accept(scope))
            return false;
        EpochState? retired;
        bool accepted = false;
        lock (_gate)
        {
            if (_disposed || !_sessions.Accept(scope))
                return false;
            WorkspaceSessionV2 current = _sessions.Current;
            retired = SynchronizeLocked(current);
            if (_epoch.Accepting &&
                _epoch.WorkspaceId == scope.WorkspaceId &&
                _epoch.SessionEpoch == scope.SessionEpoch &&
                current.State is (
                    WorkspaceSessionState.OpenedReadOnly or
                    WorkspaceSessionState.OpenedWritable or
                    WorkspaceSessionState.OpenedProvisional) &&
                current.Phase == WorkspaceSessionPhase.Idle &&
                scope.Sequence > _lastAcceptedSequence)
            {
                _lastAcceptedSequence = scope.Sequence;
                _epoch.AddLease();
                lease = new WorkspaceRequestEpochLease(
                    scope,
                    _epoch.CancellationToken,
                    _epoch.CompleteLease);
                accepted = true;
            }
        }
        Retire(retired);
        return accepted;
    }

    /// <summary>
    /// Admits the switch/close request envelope without making that request
    /// part of the epoch it is about to drain. Sequence replay protection is
    /// still applied, but the lifecycle request cannot wait on itself.
    /// </summary>
    public bool TryAdmitLifecycleRequest(WorkspaceWireScope? scope)
    {
        if (scope is null || !_sessions.Accept(scope))
            return false;
        EpochState? retired;
        bool accepted = false;
        lock (_gate)
        {
            if (_disposed || !_sessions.Accept(scope))
                return false;
            WorkspaceSessionV2 current = _sessions.Current;
            retired = SynchronizeLocked(current);
            if (_epoch.Accepting &&
                _epoch.WorkspaceId == scope.WorkspaceId &&
                _epoch.SessionEpoch == scope.SessionEpoch &&
                current.State is (
                    WorkspaceSessionState.OpenedReadOnly or
                    WorkspaceSessionState.OpenedWritable or
                    WorkspaceSessionState.OpenedProvisional) &&
                current.Phase == WorkspaceSessionPhase.Idle &&
                scope.Sequence > _lastAcceptedSequence)
            {
                _lastAcceptedSequence = scope.Sequence;
                accepted = true;
            }
        }
        Retire(retired);
        return accepted;
    }

    public ulong ReserveHostSequence(Guid workspaceId, ulong sessionEpoch)
    {
        lock (_gate)
        {
            if (_disposed ||
                _epoch.WorkspaceId != workspaceId ||
                _epoch.SessionEpoch != sessionEpoch)
                throw new InvalidOperationException(
                    "The workspace epoch is no longer active.");
            return checked(++_lastAcceptedSequence);
        }
    }

    public bool IsCurrent(WorkspaceRequestEpochLease? lease)
        => lease is not null &&
            !lease.CancellationToken.IsCancellationRequested &&
            _sessions.Accept(lease.Scope);

    public async Task DrainAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken)
    {
        EpochState epoch;
        lock (_gate)
        {
            if (_disposed ||
                _epoch.WorkspaceId != workspaceId ||
                _epoch.SessionEpoch != sessionEpoch)
                throw new InvalidOperationException(
                    "The workspace epoch is no longer active.");
            _epoch.Accepting = false;
            epoch = _epoch;
        }
        Task drained = epoch.BeginDrain();
        await drained.WaitAsync(cancellationToken).ConfigureAwait(false);
    }

    public void Resume(Guid workspaceId, ulong sessionEpoch)
    {
        EpochState? previous = null;
        lock (_gate)
        {
            if (_disposed ||
                _epoch.WorkspaceId != workspaceId ||
                _epoch.SessionEpoch != sessionEpoch)
                return;
            previous = _epoch;
            _epoch = new EpochState(
                workspaceId,
                sessionEpoch,
                accepting: true);
        }
        previous.DisposeWhenDrained();
    }

    public void Dispose()
    {
        EpochState previous;
        lock (_gate)
        {
            if (_disposed)
                return;
            _disposed = true;
            _sessions.Changed -= OnSessionChanged;
            previous = _epoch;
            _epoch = EpochState.Empty();
        }
        previous.BeginDrain();
        previous.DisposeWhenDrained();
    }

    private void OnSessionChanged(
        object? sender,
        WorkspaceSessionChangedEventArgs args)
    {
        EpochState? retired = null;
        lock (_gate)
        {
            if (!_disposed)
                retired = SynchronizeLocked(args.Session);
        }
        Retire(retired);
    }

    private EpochState? SynchronizeLocked(WorkspaceSessionV2 session)
    {
        if (_epoch.WorkspaceId == session.WorkspaceId &&
            _epoch.SessionEpoch == session.SessionEpoch)
        {
            if (session.State is (
                    WorkspaceSessionState.OpenedReadOnly or
                    WorkspaceSessionState.OpenedWritable or
                    WorkspaceSessionState.OpenedProvisional) &&
                session.Phase == WorkspaceSessionPhase.Idle &&
                !_epoch.CancellationToken.IsCancellationRequested)
                _epoch.Accepting = true;
            return null;
        }

        EpochState previous = _epoch;
        _epoch = new EpochState(
            session.WorkspaceId,
            session.SessionEpoch,
            accepting: session.State is (
                WorkspaceSessionState.OpenedReadOnly or
                WorkspaceSessionState.OpenedWritable or
                WorkspaceSessionState.OpenedProvisional) &&
                session.Phase == WorkspaceSessionPhase.Idle);
        _lastAcceptedSequence = 0;
        return previous;
    }

    private static void Retire(EpochState? epoch)
    {
        if (epoch is null)
            return;
        epoch.BeginDrain();
        epoch.DisposeWhenDrained();
    }

    private sealed class EpochState
    {
        private readonly object _gate = new();
        private readonly CancellationTokenSource _cancellation = new();
        private TaskCompletionSource _drained = CompletedSource();
        private int _leases;
        private bool _disposeWhenDrained;
        private bool _cancellationRequested;

        public EpochState(
            Guid? workspaceId,
            ulong sessionEpoch,
            bool accepting)
        {
            WorkspaceId = workspaceId;
            SessionEpoch = sessionEpoch;
            Accepting = accepting;
        }

        public Guid? WorkspaceId { get; }
        public ulong SessionEpoch { get; }
        public bool Accepting { get; set; }
        public CancellationToken CancellationToken => _cancellation.Token;

        public static EpochState Empty()
            => new(null, 0, accepting: false);

        public void AddLease()
        {
            lock (_gate)
            {
                if (_leases++ == 0)
                    _drained = NewSource();
            }
        }

        public void CompleteLease()
        {
            bool dispose = false;
            lock (_gate)
            {
                if (_leases == 0)
                    return;
                if (--_leases == 0)
                {
                    _drained.TrySetResult();
                    dispose = _disposeWhenDrained;
                }
            }
            if (dispose)
                _cancellation.Dispose();
        }

        public Task BeginDrain()
        {
            bool cancel;
            Task drained;
            lock (_gate)
            {
                Accepting = false;
                cancel = !_cancellationRequested;
                _cancellationRequested = true;
                drained = _drained.Task;
            }
            if (cancel)
            {
                try
                {
                    _cancellation.Cancel();
                }
                catch (ObjectDisposedException)
                {
                    // A completed, retired epoch may already be disposed.
                }
            }
            return drained;
        }

        public void DisposeWhenDrained()
        {
            bool dispose;
            lock (_gate)
            {
                _disposeWhenDrained = true;
                dispose = _leases == 0;
            }
            if (dispose)
                _cancellation.Dispose();
        }

        private static TaskCompletionSource NewSource()
            => new(TaskCreationOptions.RunContinuationsAsynchronously);

        private static TaskCompletionSource CompletedSource()
        {
            TaskCompletionSource source = NewSource();
            source.SetResult();
            return source;
        }
    }
}

public sealed class WorkspaceRequestEpochLease : IDisposable
{
    private Action? _complete;

    internal WorkspaceRequestEpochLease(
        WorkspaceWireScope scope,
        CancellationToken cancellationToken,
        Action complete)
    {
        Scope = scope;
        CancellationToken = cancellationToken;
        _complete = complete;
    }

    public WorkspaceWireScope Scope { get; }
    public CancellationToken CancellationToken { get; }

    public void Dispose()
        => Interlocked.Exchange(ref _complete, null)?.Invoke();
}
