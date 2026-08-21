namespace VibeTable.Desktop.Tests;

internal sealed class ManualTimeProvider : TimeProvider
{
    private readonly object _gate = new();
    private readonly List<ManualTimer> _timers = [];
    private readonly List<ScheduledTimerWaiter> _scheduledTimerWaiters = [];
    private long _timestamp;

    public Action? BeforeTimerFire { get; set; }

    public Action? AfterTimerFire { get; set; }

    public override long TimestampFrequency => TimeSpan.TicksPerSecond;

    public int ScheduledTimerCount
    {
        get
        {
            lock (_gate)
                return CountScheduledTimers();
        }
    }

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

    public Task WaitForScheduledTimersAsync(int expectedCount)
    {
        if (expectedCount < 0)
            throw new ArgumentOutOfRangeException(nameof(expectedCount));
        lock (_gate)
        {
            if (CountScheduledTimers() >= expectedCount)
                return Task.CompletedTask;
            var reached = new TaskCompletionSource(
                TaskCreationOptions.RunContinuationsAsynchronously);
            _scheduledTimerWaiters.Add(new ScheduledTimerWaiter(expectedCount, reached));
            return reached.Task;
        }
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
            BeforeTimerFire?.Invoke();
            next.Fire();
            AfterTimerFire?.Invoke();
        }
    }

    private void Schedule(ManualTimer timer, TimeSpan dueTime)
    {
        List<TaskCompletionSource>? reached = null;
        lock (_gate)
        {
            if (!_timers.Contains(timer))
                _timers.Add(timer);
            timer.DueTimestamp = dueTime == Timeout.InfiniteTimeSpan
                ? null
                : checked(_timestamp + dueTime.Ticks);
            int scheduledCount = CountScheduledTimers();
            for (int index = _scheduledTimerWaiters.Count - 1; index >= 0; index--)
            {
                ScheduledTimerWaiter waiter = _scheduledTimerWaiters[index];
                if (scheduledCount < waiter.ExpectedCount)
                    continue;
                reached ??= [];
                reached.Add(waiter.Reached);
                _scheduledTimerWaiters.RemoveAt(index);
            }
        }
        if (reached is not null)
        {
            foreach (TaskCompletionSource waiter in reached)
                waiter.TrySetResult();
        }
    }

    private int CountScheduledTimers()
        => _timers.Count(timer => timer.DueTimestamp is not null);

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

    private readonly record struct ScheduledTimerWaiter(
        int ExpectedCount,
        TaskCompletionSource Reached);
}
