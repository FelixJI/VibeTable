using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Channels;
using System.Threading.Tasks;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Infrastructure.Tests.Rpc;

[TestClass]
public sealed class JsonRpcClientTests
{
    private static readonly JsonSerializerOptions JsonOptions =
        new(JsonSerializerDefaults.Web);

    [TestMethod]
    public async Task InvokeAsync_SendsRequestWithMonotonicStringIds()
    {
        var transport = new FakeTransport();
        await using var client = new JsonRpcClient(transport);

        // Start both calls first so their TCSs are registered before any
        // response is produced. Then enqueue the matching responses.
        var firstCall = client.InvokeAsync<object, ValueResult>("add", new { x = 1 }, CancellationToken.None);
        var secondCall = client.InvokeAsync<object, ValueResult>("add", new { x = 2 }, CancellationToken.None);

        transport.EnqueueResult("1", JsonDocument.Parse("{\"value\":10}").RootElement.Clone());
        transport.EnqueueResult("2", JsonDocument.Parse("{\"value\":20}").RootElement.Clone());

        var first = await firstCall;
        var second = await secondCall;

        Assert.AreEqual(10, first.Value);
        Assert.AreEqual(20, second.Value);

        var sent = transport.CaptureSentJson();
        Assert.AreEqual(2, sent.Count);
        Assert.AreEqual("2.0", sent[0].GetProperty("jsonrpc").GetString());
        Assert.AreEqual("1", sent[0].GetProperty("id").GetString());
        Assert.AreEqual("2", sent[1].GetProperty("id").GetString());
        Assert.AreEqual("add", sent[0].GetProperty("method").GetString());
        Assert.AreEqual("add", sent[1].GetProperty("method").GetString());
    }

    [TestMethod]
    public async Task InvokeAsync_CorrelatesResponsesInReverseOrder()
    {
        // The key acceptance test: two concurrent calls, server replies in
        // REVERSE id order. Each call must still receive ITS OWN result.
        var transport = new FakeTransport();
        await using var client = new JsonRpcClient(transport);

        var firstCall = client.InvokeAsync<object, StringResult>("m1", new { }, CancellationToken.None);
        var secondCall = client.InvokeAsync<object, StringResult>("m2", new { }, CancellationToken.None);

        transport.EnqueueResult("2", JsonDocument.Parse("{\"value\":\"second-result\"}").RootElement.Clone());
        transport.EnqueueResult("1", JsonDocument.Parse("{\"value\":\"first-result\"}").RootElement.Clone());

        var first = await firstCall;
        var second = await secondCall;

        Assert.AreEqual("first-result", first.Value);
        Assert.AreEqual("second-result", second.Value);
    }

    [TestMethod]
    public async Task InvokeAsync_InterleavedNotificationDoesNotAffectPendingRequests()
    {
        // Server sends a notification (no id) BETWEEN the two responses. The
        // notification must be routed to NotificationReceived and must not
        // corrupt either pending TCS.
        var transport = new FakeTransport();
        await using var client = new JsonRpcClient(transport);

        string? notifiedMethod = null;
        JsonElement notifiedParams = default;
        var notified = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        client.NotificationReceived += (method, parameters) =>
        {
            notifiedMethod = method;
            notifiedParams = parameters;
            notified.TrySetResult(true);
        };

        var firstCall = client.InvokeAsync<object, StringResult>("m1", new { }, CancellationToken.None);
        var secondCall = client.InvokeAsync<object, StringResult>("m2", new { }, CancellationToken.None);

        transport.EnqueueNotification("progress", JsonDocument.Parse("{\"done\":3,\"total\":10}").RootElement.Clone());
        transport.EnqueueResult("1", JsonDocument.Parse("{\"value\":\"r1\"}").RootElement.Clone());
        transport.EnqueueResult("2", JsonDocument.Parse("{\"value\":\"r2\"}").RootElement.Clone());

        var first = await firstCall;
        var second = await secondCall;
        await notified.Task;

        Assert.AreEqual("r1", first.Value);
        Assert.AreEqual("r2", second.Value);
        Assert.AreEqual("progress", notifiedMethod);
        Assert.AreEqual(3, notifiedParams.GetProperty("done").GetInt32());
        Assert.AreEqual(10, notifiedParams.GetProperty("total").GetInt32());
    }

    [TestMethod]
    public async Task InvokeAsync_RpcErrorBecomesRpcException()
    {
        var transport = new FakeTransport();
        await using var client = new JsonRpcClient(transport);

        var call = client.InvokeAsync<object, ValueResult>("missing", new { }, CancellationToken.None);

        transport.EnqueueError("1", -32601, "method not found",
            JsonDocument.Parse("{\"hint\":\"nope\"}").RootElement.Clone());

        RpcRemoteException? caught = null;
        try
        {
            await call;
        }
        catch (RpcRemoteException thrown)
        {
            caught = thrown;
        }
        Assert.IsNotNull(caught, "Expected RpcRemoteException to be thrown.");
        Assert.AreEqual(-32601, caught.Code);
        Assert.IsTrue(caught.Message.Contains("method not found"));
        Assert.IsNotNull(caught.ErrorData);
        Assert.AreEqual("nope", caught.ErrorData!.Value.GetProperty("hint").GetString());
    }

    [TestMethod]
    public async Task InvokeAsync_CancellationRemovesPendingEntryAndCancels()
    {
        var transport = new FakeTransport();
        await using var client = new JsonRpcClient(transport);

        using var cts = new CancellationTokenSource();
        var call = client.InvokeAsync<object, ValueResult>("slow", new { }, cts.Token);

        // Ensure the request was written and the TCS is registered before
        // cancelling, otherwise the token.Register callback might race with
        // the TCS insertion.
        await transport.WaitForWriteAsync(TimeSpan.FromSeconds(2));

        cts.Cancel();

        await AssertThrowsAsync<OperationCanceledException>(async () => await call);
    }

    [TestMethod]
    public async Task ReaderEofFailsAllPendingCallsWithBackendUnavailable()
    {
        // No responses enqueued: the reader sees EOF as soon as the test
        // completes the channel. Both pending calls must fail with
        // BackendUnavailableException.
        var transport = new FakeTransport();
        await using var client = new JsonRpcClient(transport);

        var firstCall = client.InvokeAsync<object, ValueResult>("m1", new { }, CancellationToken.None);
        var secondCall = client.InvokeAsync<object, ValueResult>("m2", new { }, CancellationToken.None);

        transport.EnqueueEof();

        await AssertThrowsAsync<BackendUnavailableException>(async () => await firstCall);
        await AssertThrowsAsync<BackendUnavailableException>(async () => await secondCall);
    }

    [TestMethod]
    public async Task InvokeAsyncAfterReaderEofThrowsPromptlyAndDoesNotHang()
    {
        // Regression guard: once the reader has terminated (EOF here, but the
        // same logic covers transport failure), a SUBSEQUENT InvokeAsync must
        // throw BackendUnavailableException immediately rather than registering
        // a TCS that can never be resolved and hanging the awaiter forever.
        var transport = new FakeTransport();
        await using var client = new JsonRpcClient(transport);

        // First, drive a pending call to failure so we know the reader has
        // actually observed EOF and exited (the failure surfaces only after
        // FailAllPending + MarkReaderDead run on the reader thread).
        var firstCall = client.InvokeAsync<object, ValueResult>("m1", new { }, CancellationToken.None);
        transport.EnqueueEof();
        await AssertThrowsAsync<BackendUnavailableException>(async () => await firstCall);

        // Now the reader is gone. This call MUST throw synchronously-ish; if
        // the dead-reader check is missing it would hang on `await tcs.Task`.
        // Race against a 2s timeout so a regression fails the suite fast
        // instead of hanging it.
        var postEofCall = client.InvokeAsync<object, ValueResult>("m2", new { }, CancellationToken.None);

        using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(2));
        BackendUnavailableException? thrown = null;
        try
        {
            await postEofCall.WaitAsync(cts.Token);
            Assert.Fail("InvokeAsync after reader EOF should have thrown BackendUnavailableException.");
        }
        catch (BackendUnavailableException ex)
        {
            thrown = ex;
        }
        catch (OperationCanceledException)
        {
            Assert.Fail("InvokeAsync after reader EOF hung past the 2s timeout — dead-reader guard missing?");
        }
        Assert.IsNotNull(thrown, "Expected BackendUnavailableException to be thrown promptly.");
    }

    [TestMethod]
    public async Task InvokeAsync_UnknownResponseIdIsDiscarded()
    {
        // The server sends a response whose id was never requested. The
        // client must discard it and continue serving the real pending call.
        var transport = new FakeTransport();
        await using var client = new JsonRpcClient(transport);

        var call = client.InvokeAsync<object, ValueResult>("ping", new { }, CancellationToken.None);

        transport.EnqueueResult("not-ours", JsonDocument.Parse("{\"value\":\"ghost\"}").RootElement.Clone());
        transport.EnqueueResult("1", JsonDocument.Parse("{\"value\":42}").RootElement.Clone());

        var result = await call;

        Assert.AreEqual(42, result.Value);
    }

    private static async Task AssertThrowsAsync<TException>(Func<Task> action)
        where TException : Exception
    {
        try
        {
            await action();
        }
        catch (TException)
        {
            return;
        }
        catch (Exception ex)
        {
            Assert.Fail($"Expected {typeof(TException).Name}, got {ex.GetType().Name}: {ex.Message}");
        }
        Assert.Fail($"Expected {typeof(TException).Name} but no exception was thrown.");
    }

    public sealed record ValueResult(int Value);
    public sealed record StringResult(string Value);

    /// <summary>
    /// In-memory <see cref="IJsonLineTransport"/> backed by an unbounded
    /// channel. Tests enqueue frames (or an EOF) AFTER starting the calls
    /// they want to exercise, so the reader never races ahead of TCS
    /// registration. <see cref="ReadAsync"/> blocks until a frame is
    /// enqueued or the channel is completed (clean EOF).
    /// </summary>
    private sealed class FakeTransport : IJsonLineTransport
    {
        private readonly Channel<string> _frames = Channel.CreateUnbounded<string>(
            new UnboundedChannelOptions { SingleReader = true, SingleWriter = true });
        private readonly ConcurrentQueue<string> _sent = new();
        private readonly TaskCompletionSource<bool> _firstWrite =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private int _disposed;

        public void EnqueueResult(string id, JsonElement result)
        {
            var json = $"{{\"jsonrpc\":\"2.0\",\"id\":\"{id}\",\"result\":{result.GetRawText()}}}";
            _frames.Writer.WriteAsync(json).AsTask().GetAwaiter().GetResult();
        }

        public void EnqueueError(string id, int code, string message, JsonElement? data)
        {
            var dataPart = data is null ? "null" : data.Value.GetRawText();
            var json = $"{{\"jsonrpc\":\"2.0\",\"id\":\"{id}\"," +
                       $"\"error\":{{\"code\":{code},\"message\":{JsonSerializer.Serialize(message)},\"data\":{dataPart}}}}}";
            _frames.Writer.WriteAsync(json).AsTask().GetAwaiter().GetResult();
        }

        public void EnqueueNotification(string method, JsonElement parameters)
        {
            var json = $"{{\"jsonrpc\":\"2.0\",\"method\":{JsonSerializer.Serialize(method)}," +
                       $"\"params\":{parameters.GetRawText()}}}";
            _frames.Writer.WriteAsync(json).AsTask().GetAwaiter().GetResult();
        }

        public void EnqueueEof()
        {
            _frames.Writer.Complete();
        }

        public List<JsonElement> CaptureSentJson()
        {
            var list = new List<JsonElement>();
            foreach (var raw in _sent)
            {
                list.Add(JsonDocument.Parse(raw).RootElement.Clone());
            }
            return list;
        }

        public Task WaitForWriteAsync(TimeSpan timeout)
            => _firstWrite.Task.WaitAsync(timeout);

        public async Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
        {
            ThrowIfDisposed();
            if (!await _frames.Reader.WaitToReadAsync(cancellationToken).ConfigureAwait(false))
            {
                // Channel was completed — clean EOF.
                return null;
            }

            if (_frames.Reader.TryRead(out var json))
            {
                return JsonDocument.Parse(json).RootElement.Clone();
            }

            return null;
        }

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            ThrowIfDisposed();
            _sent.Enqueue(line);
            _firstWrite.TrySetResult(true);
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync()
        {
            Interlocked.Exchange(ref _disposed, 1);
            _frames.Writer.TryComplete();
            _firstWrite.TrySetCanceled();
            return default;
        }

        private void ThrowIfDisposed()
        {
            if (Volatile.Read(ref _disposed) != 0)
            {
                throw new ObjectDisposedException(nameof(FakeTransport));
            }
        }
    }
}
