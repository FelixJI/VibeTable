namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class FakeWebReplySinkTests
{
    [TestMethod]
    public async Task WaitForAsyncYieldsUntilMatchingReplyInsteadOfBlockingCaller()
    {
        var sink = new FakeWebReplySink();
        Task<FakeWebReplySink.Reply?> pending = sink.WaitForAsync("ready", 250);
        Assert.IsFalse(pending.IsCompleted);

        sink.PostNotification("ready", null);

        FakeWebReplySink.Reply? reply = await pending;
        Assert.IsNotNull(reply);
        Assert.AreEqual("ready", reply.Type);
    }
}
