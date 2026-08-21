using System;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

internal sealed record ProductAuthoritySnapshot(
    long Generation,
    PluginProjectContext? Context);

/// <summary>
/// Provides the single linearization point shared by database opens and
/// plugin-plan mutations. A transition retires and cancels every operation
/// captured from the previous epoch; token sources are disposed only after
/// their last operation releases them.
/// </summary>
public sealed class ProductAuthorityEpoch : IDisposable
{
    private readonly object _gate = new();
    private EpochState _current = new(0, null, new CancellationTokenSource());
    private bool _disposed;

    internal ProductAuthoritySnapshot Snapshot()
    {
        lock (_gate) return new(_current.Generation, _current.Context);
    }

    internal void Transition(
        PluginProjectContext? context,
        CancellationToken sessionToken = default)
    {
        EpochState retired;
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            retired = _current;
            retired.Retired = true;
            retired.RetirementCount += 1;
            _current = new EpochState(
                retired.Generation + 1,
                context,
                CancellationTokenSource.CreateLinkedTokenSource(sessionToken));
        }
        CancelRetired(retired);
    }

    internal bool IsCurrent(ProductAuthoritySnapshot snapshot)
    {
        lock (_gate)
        {
            return !_disposed
                && _current.Generation == snapshot.Generation
                && _current.Context == snapshot.Context
                && !_current.Source.IsCancellationRequested;
        }
    }

    internal bool TryAcquire(
        ProductAuthoritySnapshot expected,
        out ProductAuthorityOperationLease? lease)
    {
        lock (_gate)
        {
            if (_disposed
                || _current.Generation != expected.Generation
                || _current.Context != expected.Context
                || _current.Context is null
                || _current.Source.IsCancellationRequested)
            {
                lease = null;
                return false;
            }
            _current.ReferenceCount += 1;
            lease = new ProductAuthorityOperationLease(this, _current);
            return true;
        }
    }

    internal bool TryAcquireCurrent(out ProductAuthorityOperationLease? lease)
        => TryAcquire(Snapshot(), out lease);

    internal bool TryStart<T>(
        ProductAuthorityOperationLease lease,
        Func<CancellationToken, Task<T>> start,
        out Task<T>? operation)
    {
        ArgumentNullException.ThrowIfNull(lease);
        ArgumentNullException.ThrowIfNull(start);
        lock (_gate)
        {
            if (!OwnsCurrentLocked(lease) || lease.Started || lease.TerminalClaimed)
            {
                operation = null;
                return false;
            }
            lease.Started = true;
            operation = start(lease.State.Source.Token);
            return true;
        }
    }

    internal bool TryFinish(ProductAuthorityOperationLease lease, Action terminal)
    {
        ArgumentNullException.ThrowIfNull(lease);
        ArgumentNullException.ThrowIfNull(terminal);
        lock (_gate)
        {
            if (!OwnsCurrentLocked(lease) || lease.TerminalClaimed) return false;
            terminal();
            lease.TerminalClaimed = true;
            return true;
        }
    }

    public void Dispose()
    {
        EpochState retired;
        lock (_gate)
        {
            if (_disposed) return;
            _disposed = true;
            retired = _current;
            retired.Retired = true;
            retired.RetirementCount += 1;
        }
        CancelRetired(retired);
    }

    private bool OwnsCurrentLocked(ProductAuthorityOperationLease lease) =>
        ReferenceEquals(lease.Owner, this)
        && ReferenceEquals(lease.State, _current)
        && !lease.Released
        && !_disposed
        && !_current.Retired
        && !_current.Source.IsCancellationRequested;

    private void Release(ProductAuthorityOperationLease lease)
    {
        CancellationTokenSource? source = null;
        lock (_gate)
        {
            if (lease.Released) return;
            lease.Released = true;
            lease.State.ReferenceCount -= 1;
            if (lease.State.Retired
                && lease.State.ReferenceCount == 0
                && lease.State.RetirementCount == 0)
            {
                source = lease.State.Source;
            }
        }
        source?.Dispose();
    }

    private void CancelRetired(EpochState retired)
    {
        try
        {
            retired.Source.Cancel();
        }
        catch (AggregateException)
        {
            // Operation callbacks cannot roll back an authority transition.
        }
        finally
        {
            CancellationTokenSource? source = null;
            lock (_gate)
            {
                retired.RetirementCount -= 1;
                if (retired.ReferenceCount == 0 && retired.RetirementCount == 0)
                    source = retired.Source;
            }
            source?.Dispose();
        }
    }

    internal sealed class EpochState(
        long generation,
        PluginProjectContext? context,
        CancellationTokenSource source)
    {
        public long Generation { get; } = generation;
        public PluginProjectContext? Context { get; } = context;
        public CancellationTokenSource Source { get; } = source;
        public int ReferenceCount { get; set; }
        public int RetirementCount { get; set; }
        public bool Retired { get; set; }
    }

    internal sealed class ProductAuthorityOperationLease(
        ProductAuthorityEpoch owner,
        EpochState state) : IDisposable
    {
        internal ProductAuthorityEpoch Owner { get; } = owner;
        internal EpochState State { get; } = state;
        internal bool Started { get; set; }
        internal bool TerminalClaimed { get; set; }
        internal bool Released { get; set; }

        public ProductAuthoritySnapshot Snapshot => new(State.Generation, State.Context);
        public PluginProjectContext Context => State.Context
            ?? throw new InvalidOperationException("Authority context is unavailable.");
        public CancellationToken Token => State.Source.Token;

        public void Dispose() => Owner.Release(this);
    }
}
