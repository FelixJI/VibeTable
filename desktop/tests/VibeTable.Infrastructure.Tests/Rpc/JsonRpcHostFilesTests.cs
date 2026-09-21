using System;
using System.Text.Json;
using System.Threading;
using System.Threading.Channels;
using System.Threading.Tasks;
using Microsoft.VisualStudio.TestTools.UnitTesting;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Infrastructure.Tests.Rpc;

[TestClass]
public sealed class JsonRpcHostFilesTests
{
    [TestMethod]
    public async Task HostCallbackDoesNotConsumeNormalRpcReplyOrBlockReader()
    {
        var transport = new Peer();
        await using var client = new JsonRpcClient(transport);
        var handler = new Handler();
        client.RegisterHostFileHandler(handler);
        await transport.In.Writer.WriteAsync(JsonSerializer.SerializeToElement(new
        { jsonrpc = "2.0", id = "host-file:0123456789", method = "host.file.describe", @params = new { grantId = "opaque" } }));
        await handler.Entered.Task.WaitAsync(TimeSpan.FromSeconds(2));
        Task<JsonElement> ordinary = client.InvokeAsync<object, JsonElement>("task.status", new { taskId = "task-1" }, CancellationToken.None);
        JsonElement request = await transport.Out.Reader.ReadAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(2));
        await transport.In.Writer.WriteAsync(JsonSerializer.SerializeToElement(new { jsonrpc = "2.0", id = request.GetProperty("id").GetString(), result = new { state = "running" } }));
        Assert.AreEqual("running", (await ordinary.WaitAsync(TimeSpan.FromSeconds(2))).GetProperty("state").GetString());
        Assert.IsFalse(handler.Release.Task.IsCompleted);
        handler.Release.SetResult();
        JsonElement callback = await transport.Out.Reader.ReadAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual("host-file:0123456789", callback.GetProperty("id").GetString());
        Assert.AreEqual("opaque", callback.GetProperty("result").GetProperty("grantId").GetString());
    }

    [TestMethod]
    public async Task ClosingProcessRetiresAndJoinsOutstandingNativeCallback()
    {
        var transport = new Peer();
        var client = new JsonRpcClient(transport);
        var handler = new Handler();
        client.RegisterHostFileHandler(handler);
        await transport.In.Writer.WriteAsync(JsonSerializer.SerializeToElement(new
        { jsonrpc = "2.0", id = "host-file:0123456789", method = "host.file.read", @params = new { transferId = "t" } }));
        await handler.Entered.Task.WaitAsync(TimeSpan.FromSeconds(2));
        await client.DisposeAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual(1, handler.Retired);
        Assert.IsTrue(handler.Completed.Task.IsCompleted);
    }

    [TestMethod]
    public async Task UnknownCallbackMethodCannotReachNativeHandler()
    {
        var transport = new Peer();
        await using var client = new JsonRpcClient(transport);
        var handler = new Handler();
        client.RegisterHostFileHandler(handler);
        await transport.In.Writer.WriteAsync(JsonSerializer.SerializeToElement(new
        { jsonrpc = "2.0", id = "host-file:0123456789", method = "host.file.execute", @params = new { path = "private" } }));
        JsonElement reply = await transport.Out.Reader.ReadAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual(-32098, reply.GetProperty("error").GetProperty("code").GetInt32());
        Assert.IsFalse(reply.GetRawText().Contains("private", StringComparison.Ordinal));
        Assert.IsFalse(handler.Entered.Task.IsCompleted);
    }

    private sealed class Handler : IHostFileRequestHandler
    {
        internal readonly TaskCompletionSource Entered = new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal readonly TaskCompletionSource Release = new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal readonly TaskCompletionSource Completed = new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal int Retired;
        public async Task<JsonElement> HandleAsync(string action, JsonElement parameters, CancellationToken token)
        {
            Entered.TrySetResult();
            try { await Release.Task.WaitAsync(token); return parameters; }
            finally { Completed.TrySetResult(); }
        }
        public void Retire() => Interlocked.Increment(ref Retired);
    }

    private sealed class Peer : IJsonLineTransport
    {
        internal readonly Channel<JsonElement> In = Channel.CreateUnbounded<JsonElement>();
        internal readonly Channel<JsonElement> Out = Channel.CreateUnbounded<JsonElement>();
        public async Task<JsonElement?> ReadAsync(CancellationToken token)
            => await In.Reader.WaitToReadAsync(token) ? await In.Reader.ReadAsync(token) : null;
        public Task WriteAsync(string line, CancellationToken token)
            => Out.Writer.WriteAsync(JsonDocument.Parse(line).RootElement.Clone(), token).AsTask();
        public ValueTask DisposeAsync() { In.Writer.TryComplete(); return ValueTask.CompletedTask; }
    }
}
