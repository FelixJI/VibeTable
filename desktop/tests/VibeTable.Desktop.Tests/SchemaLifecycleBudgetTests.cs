using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class SchemaLifecycleBudgetTests
{
    [TestMethod]
    public async Task AbsoluteDeadlineCompletesEvenWhenTheAdapterIgnoresCancellation()
    {
        var time = new ManualTimeProvider();
        using var budget = SchemaLifecycleBudget.Begin(
            TimeSpan.FromSeconds(5),
            CancellationToken.None,
            time);
        var lateResult = new TaskCompletionSource<string>(
            TaskCreationOptions.RunContinuationsAsynchronously);

        Task<string> operation = budget.RunAsync(
            SchemaLifecycleStage.Apply,
            _ => lateResult.Task);
        time.Advance(TimeSpan.FromSeconds(5));

        SchemaLifecycleTimeoutException error =
            await Assert.ThrowsExactlyAsync<SchemaLifecycleTimeoutException>(
                () => operation);
        Assert.AreEqual(SchemaLifecycleStage.Apply, error.Stage);

        lateResult.SetResult("late-success");
        await Task.Yield();
        Assert.AreEqual(TaskStatus.Faulted, operation.Status);
    }

    [TestMethod]
    public async Task StagesShareOneDeadlineInsteadOfResettingTheirTimeouts()
    {
        var time = new ManualTimeProvider();
        using var budget = SchemaLifecycleBudget.Begin(
            TimeSpan.FromSeconds(10),
            CancellationToken.None,
            time);

        await budget.RunAsync(
            SchemaLifecycleStage.Apply,
            _ =>
            {
                time.Advance(TimeSpan.FromSeconds(7));
                return Task.FromResult("applied");
            });
        var refresh = new TaskCompletionSource<string>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        Task<string> operation = budget.RunAsync(
            SchemaLifecycleStage.Refresh,
            _ => refresh.Task);
        time.Advance(TimeSpan.FromSeconds(3));

        SchemaLifecycleTimeoutException error =
            await Assert.ThrowsExactlyAsync<SchemaLifecycleTimeoutException>(
                () => operation);
        Assert.AreEqual(SchemaLifecycleStage.Refresh, error.Stage);
    }

    [TestMethod]
    public async Task CallerCancellationIsNotReportedAsADeadline()
    {
        var time = new ManualTimeProvider();
        using var caller = new CancellationTokenSource();
        using var budget = SchemaLifecycleBudget.Begin(
            TimeSpan.FromSeconds(10),
            caller.Token,
            time);
        Task<string> operation = budget.RunAsync(
            SchemaLifecycleStage.Apply,
            token => Task.Delay(Timeout.InfiniteTimeSpan, token)
                .ContinueWith(
                    _ => "unreachable",
                    CancellationToken.None,
                    TaskContinuationOptions.ExecuteSynchronously,
                    TaskScheduler.Default));

        caller.Cancel();

        await Assert.ThrowsAsync<OperationCanceledException>(() => operation);
    }

    [TestMethod]
    public async Task CallerCancellationWinsWhenRequestedBeforeDeadlineCallback()
    {
        for (var iteration = 0; iteration < 25; iteration += 1)
        {
            var time = new ManualTimeProvider();
            using var caller = new CancellationTokenSource();
            using var budget = SchemaLifecycleBudget.Begin(
                TimeSpan.FromSeconds(5),
                caller.Token,
                time);
            var adapter = new TaskCompletionSource<string>(
                TaskCreationOptions.RunContinuationsAsynchronously);
            Task<string> operation = budget.RunAsync(
                SchemaLifecycleStage.Apply,
                _ => adapter.Task);

            time.BeforeTimerFire = caller.Cancel;
            time.Advance(TimeSpan.FromSeconds(5));

            await Assert.ThrowsAsync<OperationCanceledException>(() => operation);
        }
    }

    [TestMethod]
    public async Task FailedStageReleasesItsDeadlineTimer()
    {
        var time = new ManualTimeProvider();
        using var caller = new CancellationTokenSource();
        using var budget = SchemaLifecycleBudget.Begin(
            TimeSpan.FromSeconds(5),
            caller.Token,
            time);

        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            budget.RunAsync<string>(
                SchemaLifecycleStage.Apply,
                _ => throw new InvalidOperationException("expected")));

        Assert.AreEqual(0, time.ScheduledTimerCount);
    }

    [TestMethod]
    public async Task DeadlineWinsWhenCancellationIsRequestedAfterItsCallback()
    {
        for (var iteration = 0; iteration < 25; iteration += 1)
        {
            var time = new ManualTimeProvider();
            using var caller = new CancellationTokenSource();
            using var budget = SchemaLifecycleBudget.Begin(
                TimeSpan.FromSeconds(5),
                caller.Token,
                time);
            var adapter = new TaskCompletionSource<string>(
                TaskCreationOptions.RunContinuationsAsynchronously);
            Task<string> operation = budget.RunAsync(
                SchemaLifecycleStage.Apply,
                _ => adapter.Task);

            time.AfterTimerFire = caller.Cancel;
            time.Advance(TimeSpan.FromSeconds(5));

            await Assert.ThrowsExactlyAsync<SchemaLifecycleTimeoutException>(
                () => operation);
        }
    }
}
