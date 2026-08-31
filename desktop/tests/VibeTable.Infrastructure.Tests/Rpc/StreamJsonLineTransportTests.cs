using System;
using System.IO;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Infrastructure.Tests.Rpc;

[TestClass]
public sealed class StreamJsonLineTransportTests
{
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

    private static readonly JsonSerializerOptions JsonOptions =
        new(JsonSerializerDefaults.Web);

    [TestMethod]
    public async Task WriteAsync_EmitsCompactUtf8JsonAndSingleNewline()
    {
        using var stream = new MemoryStream();
        await using var transport = new StreamJsonLineTransport(stream, stream);

        var payload = new { Hello = "World", Number = 42 };
        var line = JsonSerializer.Serialize(payload, JsonOptions);

        await transport.WriteAsync(line, CancellationToken.None);

        var written = Encoding.UTF8.GetString(stream.ToArray());
        Assert.AreEqual("{\"hello\":\"World\",\"number\":42}\n", written);
    }

    [TestMethod]
    public async Task WriteAsync_DoesNotEmitBom()
    {
        using var stream = new MemoryStream();
        await using var transport = new StreamJsonLineTransport(stream, stream);

        await transport.WriteAsync("{\"a\":1}", CancellationToken.None);

        var bytes = stream.ToArray();
        Assert.IsFalse(bytes.Length >= 3 && bytes[0] == 0xEF && bytes[1] == 0xBB && bytes[2] == 0xBF,
            "Transport must not write a UTF-8 BOM.");
    }

    [TestMethod]
    public async Task WriteAsync_CancellationAfterFrameStarts_CompletesTheFrame()
    {
        await using var stream = new PausedPartialWriteStream();
        await using var transport = new StreamJsonLineTransport(new MemoryStream(), stream);
        using var cancellation = new CancellationTokenSource();

        Task write = transport.WriteAsync("{\"id\":\"1\"}", cancellation.Token);
        try
        {
            await stream.WaitUntilPausedAsync().WaitAsync(TimeSpan.FromSeconds(2));
            cancellation.Cancel();
        }
        finally
        {
            stream.Resume();
        }

        await write.WaitAsync(TimeSpan.FromSeconds(2));
        await transport.WriteAsync("{\"id\":\"2\"}", CancellationToken.None)
            .WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(
            "{\"id\":\"1\"}\n{\"id\":\"2\"}\n",
            Encoding.UTF8.GetString(stream.ToArray()));
    }

    [TestMethod]
    public async Task WriteAsync_CancellationWhileWaitingForGate_DoesNotStartFrame()
    {
        await using var stream = new PausedPartialWriteStream();
        await using var transport = new StreamJsonLineTransport(new MemoryStream(), stream);
        using var cancellation = new CancellationTokenSource();
        Task firstWrite = transport.WriteAsync("{\"id\":\"1\"}", CancellationToken.None);
        try
        {
            await stream.WaitUntilPausedAsync().WaitAsync(TimeSpan.FromSeconds(2));
            Task cancelledWrite = transport.WriteAsync("{\"id\":\"2\"}", cancellation.Token);
            cancellation.Cancel();
            await AssertThrowsAsync<OperationCanceledException>(
                () => cancelledWrite.WaitAsync(TimeSpan.FromSeconds(2)));
        }
        finally
        {
            stream.Resume();
        }
        await firstWrite.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(
            "{\"id\":\"1\"}\n",
            Encoding.UTF8.GetString(stream.ToArray()));
    }

    [TestMethod]
    public async Task WriteAsync_AcceptsFrameAtExactWireLimit()
    {
        using var stream = new MemoryStream();
        await using var transport = new StreamJsonLineTransport(new MemoryStream(), stream);
        string line = new('a', StreamJsonLineTransport.MaxFrameBytes - 1);

        await transport.WriteAsync(line, CancellationToken.None);

        Assert.AreEqual(StreamJsonLineTransport.MaxFrameBytes, stream.Length);
        Assert.AreEqual((byte)'\n', stream.GetBuffer()[stream.Length - 1]);
    }

    [TestMethod]
    public async Task WriteAsync_RejectsFrameWhoseNewlineExceedsLimit()
    {
        using var stream = new MemoryStream();
        await using var transport = new StreamJsonLineTransport(new MemoryStream(), stream);
        string line = new('a', StreamJsonLineTransport.MaxFrameBytes);

        await AssertThrowsAsync<RpcException>(
            () => transport.WriteAsync(line, CancellationToken.None));

        Assert.AreEqual(0, stream.Length);
    }

    [TestMethod]
    public async Task ReadAsync_ReturnsParsedJsonElement()
    {
        var payload = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":42}\n";
        using var stream = new MemoryStream(Encoding.UTF8.GetBytes(payload));
        stream.Position = 0;
        await using var transport = new StreamJsonLineTransport(stream, new MemoryStream());

        var element = await transport.ReadAsync(CancellationToken.None);

        Assert.IsNotNull(element);
        var value = element!.Value;
        Assert.AreEqual("2.0", value.GetProperty("jsonrpc").GetString());
        Assert.AreEqual(42, value.GetProperty("result").GetInt32());
    }

    [TestMethod]
    public async Task ReadAsync_ReturnsNullOnCleanEof()
    {
        // Empty buffer => immediate clean EOF.
        using var stream = new MemoryStream();
        await using var transport = new StreamJsonLineTransport(stream, new MemoryStream());

        var element = await transport.ReadAsync(CancellationToken.None);

        Assert.IsNull(element, "Clean EOF must surface as null.");
    }

    [TestMethod]
    public async Task ReadAsync_ReturnsNullWhenOnlyTrailingNewlineRemains()
    {
        // A frame followed by EOF should yield the frame then null.
        var payload = "{\"id\":\"a\"}\n";
        using var stream = new MemoryStream(Encoding.UTF8.GetBytes(payload));
        await using var transport = new StreamJsonLineTransport(stream, new MemoryStream());

        var first = await transport.ReadAsync(CancellationToken.None);
        var second = await transport.ReadAsync(CancellationToken.None);

        Assert.IsNotNull(first);
        Assert.IsNull(second, "No more frames must surface as null (clean EOF).");
    }

    [TestMethod]
    public async Task ReadAsync_TwoFramesInSuccession()
    {
        var payload = "{\"id\":\"1\"}\n{\"id\":\"2\"}\n";
        using var stream = new MemoryStream(Encoding.UTF8.GetBytes(payload));
        await using var transport = new StreamJsonLineTransport(stream, new MemoryStream());

        var first = await transport.ReadAsync(CancellationToken.None);
        var second = await transport.ReadAsync(CancellationToken.None);

        Assert.IsNotNull(first);
        Assert.IsNotNull(second);
        Assert.AreEqual("1", first!.Value.GetProperty("id").GetString());
        Assert.AreEqual("2", second!.Value.GetProperty("id").GetString());
    }

    [TestMethod]
    public async Task ReadAsync_ThrowsOnInvalidJson()
    {
        var payload = "{ not valid json }\n";
        using var stream = new MemoryStream(Encoding.UTF8.GetBytes(payload));
        await using var transport = new StreamJsonLineTransport(stream, new MemoryStream());

        await AssertThrowsAsync<JsonException>(async () =>
            await transport.ReadAsync(CancellationToken.None));
    }

    [TestMethod]
    public async Task ReadAsync_RejectsFrameAboveFourMebibytes()
    {
        // 4 MiB = 4 * 1024 * 1024 bytes. A line at exactly the limit must be
        // accepted; one byte over must be rejected before parsing.
        const int Limit = 4 * 1024 * 1024;

        var exactly = BuildLineOfLength(Limit);
        using (var exactlyStream = new MemoryStream(Encoding.UTF8.GetBytes(exactly)))
        {
            exactlyStream.Position = 0;
            await using var transport = new StreamJsonLineTransport(exactlyStream, new MemoryStream());
            var element = await transport.ReadAsync(CancellationToken.None);
            Assert.IsNotNull(element, "A frame at exactly the byte limit must be accepted.");
        }

        var over = BuildLineOfLength(Limit + 1);
        using (var overStream = new MemoryStream(Encoding.UTF8.GetBytes(over)))
        {
            overStream.Position = 0;
            await using var transport = new StreamJsonLineTransport(overStream, new MemoryStream());
            await AssertThrowsAsync<RpcException>(async () =>
                await transport.ReadAsync(CancellationToken.None));
        }
    }

    [TestMethod]
    public async Task DisposeAsync_IsIdempotent()
    {
        var stream = new MemoryStream();
        var transport = new StreamJsonLineTransport(stream, new MemoryStream());

        await transport.DisposeAsync();
        await transport.DisposeAsync();
    }

    private static string BuildLineOfLength(int totalBytesIncludingNewline)
    {
        // totalBytesIncludingNewline counts the trailing '\n'.
        if (totalBytesIncludingNewline < 2)
        {
            throw new ArgumentException("must be >= 2", nameof(totalBytesIncludingNewline));
        }

        const string Prefix = "{\"a\":\"";
        const string Suffix = "\"}";
        var payloadBytes = totalBytesIncludingNewline - 1; // minus the '\n'
        var fillerBytes = payloadBytes - (Prefix.Length + Suffix.Length);
        if (fillerBytes < 0)
        {
            // Too short to use the JSON envelope shape — pad with a bare string
            // of the requested size. JSON validity is not asserted for tiny sizes.
            return new string('a', payloadBytes);
        }

        return Prefix + new string('a', fillerBytes) + Suffix + "\n";
    }

    private sealed class PausedPartialWriteStream : MemoryStream
    {
        private readonly TaskCompletionSource _paused = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource _resume = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        private int _pauseFirstWrite = 1;

        public Task WaitUntilPausedAsync() => _paused.Task;

        public void Resume() => _resume.TrySetResult();

        public override async ValueTask WriteAsync(
            ReadOnlyMemory<byte> buffer,
            CancellationToken cancellationToken = default)
        {
            if (Interlocked.Exchange(ref _pauseFirstWrite, 0) != 0)
            {
                int prefixLength = Math.Max(1, buffer.Length / 2);
                await base.WriteAsync(
                    buffer[..prefixLength],
                    CancellationToken.None);
                _paused.TrySetResult();
                await _resume.Task;
                cancellationToken.ThrowIfCancellationRequested();
                await base.WriteAsync(
                    buffer[prefixLength..],
                    CancellationToken.None);
                return;
            }
            await base.WriteAsync(buffer, cancellationToken);
        }
    }
}
