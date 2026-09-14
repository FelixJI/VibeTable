namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class FakeWebReplySinkTests
{
    [TestMethod]
    [Timeout(5_000)]
    public async Task WaitForAsyncYieldsUntilMatchingReplyInsteadOfBlockingCaller()
    {
        var sink = new FakeWebReplySink();
        // This checks yielding and delivery, not how quickly CI schedules the producer.
        Task<FakeWebReplySink.Reply?> pending = sink.WaitForAsync("ready", Timeout.Infinite);
        Assert.IsFalse(pending.IsCompleted);

        sink.PostNotification("ready", null);

        FakeWebReplySink.Reply? reply = await pending.WaitAsync(TimeSpan.FromSeconds(3));
        Assert.IsNotNull(reply);
        Assert.AreEqual("ready", reply.Type);
    }
}
