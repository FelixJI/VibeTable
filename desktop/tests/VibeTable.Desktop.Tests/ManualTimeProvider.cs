namespace VibeTable.Desktop.Tests;

internal sealed class ManualTimeProvider : TimeProvider
{
    private readonly object _gate = new();
    private readonly List<ManualTimer> _timers = [];
    private long _timestamp;

    public override long TimestampFrequency => TimeSpan.TicksPerSecond;

    public override DateTimeOffset GetUtcNow()
    {
        lock (_gate)
            return DateTimeOffset.UnixEpoch + TimeSpan.FromTicks(_timestamp);
    }

    public override long GetTimestamp()
    {
        lock (_gate)
            return _timestamp;
    }

    public override ITimer CreateTimer(
        TimerCallback callback,
        object? state,
        TimeSpan dueTime,
        TimeSpan period)
    {
        ArgumentNullException.ThrowIfNull(callback);
        var timer = new ManualTimer(this, callback, state);
        timer.Change(dueTime, period);
        return timer;
    }

    public void Advance(TimeSpan duration)
    {
        if (duration < TimeSpan.Zero)
            throw new ArgumentOutOfRangeException(nameof(duration));
        long target;
        lock (_gate)
            target = checked(_timestamp + duration.Ticks);
        while (true)
        {
            ManualTimer? next;
            lock (_gate)
            {
                next = _timers
                    .Where(timer => timer.DueTimestamp is not null)
                    .OrderBy(timer => timer.DueTimestamp)
                    .FirstOrDefault(timer => timer.DueTimestamp <= target);
                if (next is null)
                {
                    _timestamp = target;
                    return;
                }
                _timestamp = next.DueTimestamp!.Value;
                next.RescheduleAfterFire();
            }
            next.Fire();
        }
    }

    private void Schedule(ManualTimer timer, TimeSpan dueTime)
    {
        lock (_gate)
        {
            if (!_timers.Contains(timer))
                _timers.Add(timer);
            timer.DueTimestamp = dueTime == Timeout.InfiniteTimeSpan
                ? null
                : checked(_timestamp + dueTime.Ticks);
        }
    }

    private void Remove(ManualTimer timer)
    {
        lock (_gate)
        {
            _timers.Remove(timer);
            timer.DueTimestamp = null;
        }
    }

    private sealed class ManualTimer(
        ManualTimeProvider owner,
        TimerCallback callback,
        object? state) : ITimer
    {
        private TimeSpan _period = Timeout.InfiniteTimeSpan;
        private bool _disposed;

        public long? DueTimestamp { get; set; }

        public bool Change(TimeSpan dueTime, TimeSpan period)
        {
            if (_disposed) return false;
            if (dueTime < TimeSpan.Zero && dueTime != Timeout.InfiniteTimeSpan)
                throw new ArgumentOutOfRangeException(nameof(dueTime));
            _period = period;
            owner.Schedule(this, dueTime);
            return true;
        }

        public void Dispose()
        {
            if (_disposed) return;
            _disposed = true;
            owner.Remove(this);
        }

        public ValueTask DisposeAsync()
        {
            Dispose();
            return ValueTask.CompletedTask;
        }

        public void Fire()
        {
            if (!_disposed) callback(state);
        }

        public void RescheduleAfterFire()
        {
            DueTimestamp = _period == Timeout.InfiniteTimeSpan
                ? null
                : checked(owner._timestamp + _period.Ticks);
        }
    }
}
