using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceActivationBudgetTests
{
    [TestMethod]
    public async Task LegacyBoundaryDoesNotResetTheSharedActivationDeadline()
    {
        var time = new ManualTimeProvider();
        var traces = new List<string>();
        var policy = new WorkspaceActivationPolicy(
            totalTimeout: TimeSpan.FromSeconds(105),
            sidecarTimeout: TimeSpan.FromSeconds(60),
            backendTimeout: TimeSpan.FromSeconds(30),
            verificationTimeout: TimeSpan.FromSeconds(10));
        using var budget = WorkspaceActivationBudget.Begin(
            Guid.Parse("11111111-1111-4111-8111-111111111111"),
            7,
            policy,
            CancellationToken.None,
            time,
            traces.Add);
        var sidecarReady = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);

        Task sidecar = budget.RunAsync(
            WorkspaceActivationStage.Sidecar,
            token => sidecarReady.Task.WaitAsync(token));
        time.Advance(TimeSpan.FromSeconds(31));
        sidecarReady.SetResult();
        await sidecar;

        await budget.RunAsync(
            WorkspaceActivationStage.Backend,
            _ =>
            {
                time.Advance(TimeSpan.FromSeconds(2));
                return Task.CompletedTask;
            });
        await budget.RunAsync(
            WorkspaceActivationStage.Verification,
            _ => Task.CompletedTask);
        WorkspaceActivationReport report = budget.Complete();

        Assert.AreEqual(WorkspaceActivationOutcome.Completed, report.Outcome);
        Assert.AreEqual(TimeSpan.FromSeconds(33), report.Elapsed);
        CollectionAssert.AreEqual(
            new[]
            {
                WorkspaceActivationStage.Sidecar,
                WorkspaceActivationStage.Backend,
                WorkspaceActivationStage.Verification,
            },
            report.Stages.Select(stage => stage.Stage).ToArray());
        Assert.IsTrue(traces.Any(message =>
            message.StartsWith(
                "workspace.activation.sidecar.completed ",
                StringComparison.Ordinal)));
    }

    [TestMethod]
    public async Task AbsoluteDeadlineCancelsTheCurrentStageWithoutStartingAnotherAttempt()
    {
        var time = new ManualTimeProvider();
        var policy = new WorkspaceActivationPolicy(
            totalTimeout: TimeSpan.FromSeconds(71),
            sidecarTimeout: TimeSpan.FromSeconds(35),
            backendTimeout: TimeSpan.FromSeconds(30),
            verificationTimeout: TimeSpan.FromSeconds(5));
        using var budget = WorkspaceActivationBudget.Begin(
            Guid.Parse("22222222-2222-4222-8222-222222222222"),
            9,
            policy,
            CancellationToken.None,
            time);
        var attempts = 0;

        await budget.RunAsync(
            WorkspaceActivationStage.Sidecar,
            _ =>
            {
                attempts += 1;
                time.Advance(TimeSpan.FromSeconds(30));
                return Task.CompletedTask;
            });
        time.Advance(TimeSpan.FromSeconds(20));
        Task backend = budget.RunAsync(
            WorkspaceActivationStage.Backend,
            async token =>
            {
                attempts += 1;
                await Task.Delay(Timeout.InfiniteTimeSpan, token);
            });

        time.Advance(TimeSpan.FromSeconds(21));
        WorkspaceActivationTimeoutException error =
            await Assert.ThrowsExactlyAsync<WorkspaceActivationTimeoutException>(
                () => backend);

        Assert.AreEqual(WorkspaceActivationStage.Backend, error.Stage);
        Assert.AreEqual(2, attempts);
        Assert.AreEqual(
            WorkspaceActivationOutcome.TimedOut,
            budget.Report.Outcome);
        Assert.AreEqual(
            WorkspaceActivationStage.Backend,
            budget.Report.LastStage);
    }

    [TestMethod]
    public void PolicyRejectsANestedBudgetThatCanOutliveItsTotalDeadline()
    {
        Assert.ThrowsExactly<ArgumentOutOfRangeException>(() =>
            new WorkspaceActivationPolicy(
                totalTimeout: TimeSpan.FromSeconds(60),
                sidecarTimeout: TimeSpan.FromSeconds(30),
                backendTimeout: TimeSpan.FromSeconds(30),
                verificationTimeout: TimeSpan.FromSeconds(1)));
    }

    [TestMethod]
    public async Task CallerCancellationIsReportedSeparatelyFromDeadlineExpiry()
    {
        using var caller = new CancellationTokenSource();
        using var budget = WorkspaceActivationBudget.Begin(
            Guid.Parse("55555555-5555-4555-8555-555555555555"),
            15,
            WorkspaceActivationPolicy.Default,
            caller.Token);
        Task stage = budget.RunAsync(
            WorkspaceActivationStage.Sidecar,
            token => Task.Delay(Timeout.InfiniteTimeSpan, token));

        caller.Cancel();

        await Assert.ThrowsAsync<OperationCanceledException>(() => stage);
        WorkspaceActivationReport report = budget.Fail(new OperationCanceledException());
        Assert.AreEqual(WorkspaceActivationOutcome.Cancelled, report.Outcome);
        Assert.AreEqual(
            WorkspaceActivationOutcome.Cancelled,
            report.Stages.Single().Outcome);
    }

    [TestMethod]
    public async Task SupervisorTimeoutUsesTheStableActivationTimeoutContract()
    {
        using var budget = WorkspaceActivationBudget.Begin(
            Guid.Parse("66666666-6666-4666-8666-666666666666"),
            17,
            WorkspaceActivationPolicy.Default,
            CancellationToken.None);

        WorkspaceActivationTimeoutException error =
            await Assert.ThrowsExactlyAsync<WorkspaceActivationTimeoutException>(() =>
                budget.RunAsync(
                    WorkspaceActivationStage.Sidecar,
                    _ => Task.FromException(new TimeoutException("supervisor timeout"))));

        Assert.AreEqual(WorkspaceActivationStage.Sidecar, error.Stage);
        Assert.IsInstanceOfType<TimeoutException>(error.InnerException);
        Assert.AreEqual(
            WorkspaceActivationOutcome.TimedOut,
            budget.Report.Outcome);
    }

    [TestMethod]
    public async Task StageCannotCompleteAfterIgnoringItsExpiredDeadlineToken()
    {
        var time = new ManualTimeProvider();
        using var budget = WorkspaceActivationBudget.Begin(
            Guid.Parse("77777777-7777-4777-8777-777777777777"),
            19,
            WorkspaceActivationPolicy.Default,
            CancellationToken.None,
            time);

        WorkspaceActivationTimeoutException error =
            await Assert.ThrowsExactlyAsync<WorkspaceActivationTimeoutException>(() =>
                budget.RunAsync(
                    WorkspaceActivationStage.Sidecar,
                    _ =>
                    {
                        time.Advance(TimeSpan.FromSeconds(31));
                        return Task.CompletedTask;
                    }));

        Assert.AreEqual(WorkspaceActivationStage.Sidecar, error.Stage);
        Assert.AreEqual(
            WorkspaceActivationOutcome.TimedOut,
            budget.Report.Outcome);
    }
}
