namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ManualTimeProviderTests
{
    [TestMethod]
    public void WaitForScheduledTimersAsync_CompletesEveryEligibleWaiter()
    {
        var time = new ManualTimeProvider();
        Task firstWaiter = time.WaitForScheduledTimersAsync(1);
        Task secondWaiter = time.WaitForScheduledTimersAsync(1);
        Task twoTimerWaiter = time.WaitForScheduledTimersAsync(2);

        using ITimer first = time.CreateTimer(
            static _ => { },
            null,
            TimeSpan.FromSeconds(1),
            Timeout.InfiniteTimeSpan);

        Assert.IsTrue(firstWaiter.IsCompleted);
        Assert.IsTrue(secondWaiter.IsCompleted);
        Assert.IsFalse(twoTimerWaiter.IsCompleted);

        using ITimer second = time.CreateTimer(
            static _ => { },
            null,
            TimeSpan.FromSeconds(2),
            Timeout.InfiniteTimeSpan);

        Assert.IsTrue(twoTimerWaiter.IsCompleted);
    }
}
