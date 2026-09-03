using System.Net;
using System.Text;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductRealtimeStreamTests
{
    private static readonly PocketBaseAdminContext Context = new(
        new Uri("http://127.0.0.1:8123/bootstrap"), new Uri("http://127.0.0.1:8123/"),
        "X-VibeTable-Session", "test-session-secret");

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task UnterminatedOversizedBlockIsRejectedBeforeEndOfStream(bool multiline)
    {
        using var source = new TestSseStream(Encoding.UTF8.GetBytes(multiline
            ? string.Concat(Enumerable.Repeat("data: x\n", 530_000))
            : "data: " + new string('x', 4 * 1024 * 1024 + 32 * 1024)));
        using var handler = new ReplyHandler(source);
        await Assert.ThrowsExactlyAsync<InvalidDataException>(async () =>
        {
            await foreach (var item in new ProductRealtimeStream(Context, handler).ReadAsync())
                Assert.Fail("An incomplete event must not be delivered.");
        });
        Assert.IsTrue(source.BytesRead < (multiline ? 4_240_000 : 4 * 1024 * 1024 + 32 * 1024));
        Assert.IsTrue(source.Disposed);
    }

    [TestMethod]
    public async Task JsonBeyondFourMiBIsRejectedInsideTheWireAllowance()
    {
        using var handler = new ReplyHandler(Frame(SizedPayload(4 * 1024 * 1024 + 1)));
        var stream = new ProductRealtimeStream(Context, handler);
        await Assert.ThrowsExactlyAsync<InvalidDataException>(async () =>
        {
            await foreach (var item in stream.ReadAsync()) { }
        });
    }

    [TestMethod]
    [DataRow("\n")]
    [DataRow("\r\n")]
    [DataRow("\r")]
    public async Task ExactJsonBudgetAndIndependentHeartbeatBlocksRemainValid(string newline)
    {
        string payload = SizedPayload(4 * 1024 * 1024);
        string frame = Frame(payload, "rt:9223372036854775807").Replace("\n", newline);
        using var handler = new ReplyHandler("\uFEFF" + frame + ": heartbeat" + newline + newline + frame);
        var received = new List<ProductRealtimeFrame>();
        await foreach (var item in new ProductRealtimeStream(Context, handler).ReadAsync("rt:0")) received.Add(item);
        Assert.HasCount(2, received);
        Assert.AreEqual("rt:9223372036854775807", received[0].Cursor);
        Assert.AreEqual("realtime.recovered", received[1].Payload.GetProperty("topic").GetString());
        Assert.AreEqual("rt:0", handler.After);
        Assert.AreEqual(1, handler.Sends);
    }

    [TestMethod]
    public async Task SplitCrLfAndPartialEofPreserveOnlyCompleteItems()
    {
        string complete = Frame("{\"contractVersion\":\"2.0\",\"topic\":\"task.changed\",\"eventId\":\"evt-real\"}",
            "rt:7", "task.changed");
        using var source = new TestSseStream(Encoding.UTF8.GetBytes(
            (": heartbeat\n\n" + complete + complete.TrimEnd('\n')).Replace("\n", "\r\n")), chunkSize: 1);
        using var handler = new ReplyHandler(source);
        var received = new List<ProductRealtimeFrame>();
        await foreach (var item in new ProductRealtimeStream(Context, handler).ReadAsync()) received.Add(item);
        Assert.HasCount(1, received);
        Assert.AreEqual("rt:7", received[0].Cursor);
        Assert.AreEqual("evt-real", received[0].Payload.GetProperty("eventId").GetString());
        Assert.IsTrue(source.Disposed);
        Assert.IsNull(handler.After);
    }

    [TestMethod]
    [DataRow("", "realtime.recovered", "{\"contractVersion\":\"2.0\",\"topic\":\"realtime.recovered\"}")]
    [DataRow("rt:01", "realtime.recovered", "{\"contractVersion\":\"2.0\",\"topic\":\"realtime.recovered\"}")]
    [DataRow("rt:9223372036854775808", "realtime.recovered", "{\"contractVersion\":\"2.0\",\"topic\":\"realtime.recovered\"}")]
    [DataRow("rt:0", "task.changed", "{\"contractVersion\":\"2.0\",\"topic\":\"task.changed\"}")]
    [DataRow("rt:1", "unknown", "{}")]
    [DataRow("rt:1", "data.changed", "{\"contractVersion\":\"2.0\",\"topic\":\"task.changed\"}")]
    [DataRow("rt:1", "data.changed", "[]")]
    [DataRow("rt:1", "data.changed", "{")]
    public async Task InvalidEnvelopeIsNeverDelivered(string cursor, string topic, string payload)
    {
        using var handler = new ReplyHandler(Frame(payload, cursor, topic));
        await Assert.ThrowsExactlyAsync<InvalidDataException>(async () =>
        {
            await foreach (var item in new ProductRealtimeStream(Context, handler).ReadAsync()) Assert.Fail();
        });
    }

    [TestMethod]
    public async Task MalformedUtf8CannotHideInAnUninspectedPayloadProperty()
    {
        byte[] bytes = Encoding.UTF8.GetBytes(Frame(SizedPayload(150)));
        bytes[Array.IndexOf(bytes, (byte)'x')] = 0xff;
        using var handler = new ReplyHandler(new TestSseStream(bytes));
        await Assert.ThrowsExactlyAsync<InvalidDataException>(async () =>
        {
            await foreach (var item in new ProductRealtimeStream(Context, handler).ReadAsync()) Assert.Fail();
        });
    }

    [TestMethod]
    public async Task CancellationDuringBlockedBodyReadReleasesTheResponse()
    {
        using var source = new TestSseStream([], blockAtEnd: true);
        using var handler = new ReplyHandler(source);
        using var cancellation = new CancellationTokenSource();
        await using var reader = new ProductRealtimeStream(Context, handler).ReadAsync(
            cancellationToken: cancellation.Token).GetAsyncEnumerator();
        Task<bool> reading = reader.MoveNextAsync().AsTask();
        await source.Blocked.Task.WaitAsync(TimeSpan.FromSeconds(5));
        cancellation.Cancel();
        await Assert.ThrowsAsync<OperationCanceledException>(() => reading);
        Assert.IsTrue(source.Disposed);
    }

    [TestMethod]
    public async Task EarlyBreakDisposesWithoutReadingOrAcknowledgingAnotherItem()
    {
        using var source = new TestSseStream(Encoding.UTF8.GetBytes(Frame(SizedPayload(150))), blockAtEnd: true);
        using var handler = new ReplyHandler(source);
        await foreach (var item in new ProductRealtimeStream(Context, handler).ReadAsync()) break;
        Assert.IsTrue(source.Disposed);
        Assert.IsFalse(source.Blocked.Task.IsCompleted);
        Assert.AreEqual(1, handler.Sends);
    }

    [TestMethod]
    [DataRow(400)]
    [DataRow(404)]
    [DataRow(422)]
    [DataRow(503)]
    public async Task HttpFailureExposesStatusButNeverReadsArbitraryResponseBody(int status)
    {
        using var source = new TestSseStream(Encoding.UTF8.GetBytes(Context.SessionSecret));
        using var handler = new ReplyHandler(source) { Status = (HttpStatusCode)status };
        HttpRequestException error = await Assert.ThrowsExactlyAsync<HttpRequestException>(async () =>
        {
            await foreach (var item in new ProductRealtimeStream(Context, handler).ReadAsync()) Assert.Fail();
        });
        Assert.AreEqual((HttpStatusCode)status, error.StatusCode);
        Assert.DoesNotContain(Context.SessionSecret, error.ToString());
        Assert.AreEqual(0, source.BytesRead);
        Assert.IsTrue(source.Disposed);
    }

    [TestMethod]
    public async Task BodyReadFailureUsesTheExistingSafeUnavailableError()
    {
        using var source = new TestSseStream([]) { ReadFailure = new IOException(Context.SessionSecret) };
        using var handler = new ReplyHandler(source);
        BackendUnavailableException error = await Assert.ThrowsExactlyAsync<BackendUnavailableException>(async () =>
        {
            await foreach (var item in new ProductRealtimeStream(Context, handler).ReadAsync()) Assert.Fail();
        });
        Assert.DoesNotContain(Context.SessionSecret, error.ToString());
        Assert.IsTrue(source.Disposed);
    }

    [TestMethod]
    public async Task HeadersWaitRemainsCallerCancellable()
    {
        using var handler = new ReplyHandler("") { BlockHeaders = true };
        using var cancellation = new CancellationTokenSource();
        await using var reader = new ProductRealtimeStream(Context, handler).ReadAsync(
            cancellationToken: cancellation.Token).GetAsyncEnumerator();
        Task<bool> reading = reader.MoveNextAsync().AsTask();
        await handler.HeadersEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));
        cancellation.Cancel();
        await Assert.ThrowsAsync<OperationCanceledException>(() => reading);
        Assert.AreEqual(1, handler.Sends);
    }

    [TestMethod]
    public async Task CallerCancellationWinsAHeadersReplyThatIgnoresCancellation()
    {
        using var cancellation = new CancellationTokenSource();
        using var source = new TestSseStream([]);
        using var handler = new ReplyHandler(source)
        { Status = HttpStatusCode.BadRequest, BeforeReply = cancellation.Cancel };
        await Assert.ThrowsAsync<OperationCanceledException>(async () =>
        {
            await foreach (var item in new ProductRealtimeStream(Context, handler).ReadAsync(
                cancellationToken: cancellation.Token)) Assert.Fail();
        });
        Assert.IsTrue(source.Disposed);
    }

    [TestMethod]
    public async Task InvalidCursorAndPreCancellationNeverSendCredentials()
    {
        using var handler = new ReplyHandler("");
        var stream = new ProductRealtimeStream(Context, handler);
        await Assert.ThrowsExactlyAsync<ArgumentException>(async () =>
        {
            await foreach (var item in stream.ReadAsync("rt:01")) Assert.Fail();
        });
        await Assert.ThrowsAsync<OperationCanceledException>(async () =>
        {
            await foreach (var item in stream.ReadAsync(cancellationToken: new(true))) Assert.Fail();
        });
        Assert.AreEqual(0, handler.Sends);
    }

    [TestMethod]
    public async Task SuccessWithoutSseContentTypeIsRejectedBeforeBodyRead()
    {
        using var source = new TestSseStream(Encoding.UTF8.GetBytes(Context.SessionSecret));
        using var handler = new ReplyHandler(source) { MediaType = "application/json" };
        await Assert.ThrowsExactlyAsync<InvalidDataException>(async () =>
        {
            await foreach (var item in new ProductRealtimeStream(Context, handler).ReadAsync()) Assert.Fail();
        });
        Assert.AreEqual(0, source.BytesRead);
        Assert.IsTrue(source.Disposed);
    }

    private static string SizedPayload(int bytes)
    {
        const string prefix = "{\"contractVersion\":\"2.0\",\"topic\":\"realtime.recovered\",\"padding\":\"测";
        return prefix + new string('x', bytes - Encoding.UTF8.GetByteCount(prefix) - 2) + "\"}";
    }

    private static string Frame(string payload, string cursor = "rt:0", string topic = "realtime.recovered")
        => $"id: {cursor}\nevent: {topic}\ndata: {payload}\n\n";

    private sealed class ReplyHandler(Stream source) : HttpMessageHandler
    {
        internal ReplyHandler(string wire) : this(new TestSseStream(Encoding.UTF8.GetBytes(wire))) { }
        internal HttpStatusCode Status { get; init; } = HttpStatusCode.OK;
        internal string MediaType { get; init; } = "text/event-stream";
        internal bool BlockHeaders { get; init; }
        internal Action? BeforeReply { get; init; }
        internal TaskCompletionSource HeadersEntered { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal int Sends { get; private set; }
        internal string? After { get; private set; }
        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token)
        {
            Sends++;
            HeadersEntered.TrySetResult();
            if (BlockHeaders) await Task.Delay(Timeout.Infinite, token);
            Assert.AreEqual("http://127.0.0.1:8123/api/vibetable/v2/events", request.RequestUri!.ToString());
            Assert.AreEqual(Context.SessionSecret, request.Headers.GetValues(Context.SessionHeaderName).Single());
            Assert.AreEqual("text/event-stream", request.Headers.Accept.Single().MediaType);
            After = request.Headers.TryGetValues("Last-Event-ID", out var values) ? values.Single() : null;
            var content = new StreamContent(source);
            content.Headers.ContentType = new(MediaType);
            BeforeReply?.Invoke();
            return new HttpResponseMessage(Status) { Content = content };
        }
    }
}
