namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class FakeWebReplySinkTests
{
    [TestMethod]
    public async Task WaitForAsyncYieldsUntilMatchingReplyInsteadOfBlockingCaller()
    {
        var sink = new FakeWebReplySink();
        using var waitReturned = new ManualResetEventSlim();
        Task producer = Task.Factory.StartNew(
            () =>
            {
                Assert.IsTrue(waitReturned.Wait(TimeSpan.FromSeconds(1)));
                sink.PostNotification("ready", null);
            },
            CancellationToken.None,
            TaskCreationOptions.LongRunning,
            TaskScheduler.Default);

        Task<FakeWebReplySink.Reply?> pending = sink.WaitForAsync("ready", 250);
        waitReturned.Set();

        FakeWebReplySink.Reply? reply = await pending;
        await producer;
        Assert.IsNotNull(reply);
    }
}
