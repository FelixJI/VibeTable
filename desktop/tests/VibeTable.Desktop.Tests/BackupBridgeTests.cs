using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class BackupBridgeTests
{
    [TestMethod]
    public async Task DispatcherForwardsOnlyClosedBackupMethodsAndCorrelatesReplies()
    {
        var transport = new BackupTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            sink);
        dispatcher.SetProductDataGateway(gateway);

        dispatcher.Dispatch(Request("backup.list", "list-1", "{}"));
        Assert.AreEqual("list-1", (await sink.WaitForAsync("backup.list"))!.RequestId);

        dispatcher.Dispatch(Request(
            "backup.create",
            "create-1",
            """{"name":"manual_20260724_101500.zip"}"""));
        Assert.IsTrue(SpinWait.SpinUntil(
            () => sink.Replies.Any(reply => reply.RequestId == "create-1"),
            2_000));

        dispatcher.Dispatch(Request(
            "backup.restore",
            "restore-1",
            """{"name":"manual_20260724_101500.zip","confirmed":true}"""));
        Assert.IsTrue(SpinWait.SpinUntil(
            () => sink.Replies.Any(reply => reply.RequestId == "restore-1"),
            2_000));

        CollectionAssert.AreEqual(
            new[] { "backup.list", "backup.create", "backup.restore" },
            transport.Methods);
        StringAssert.Contains(transport.Serialized, @"""confirmed"":true");
        Assert.IsFalse(transport.Serialized.Contains(
            "sessionSecret",
            StringComparison.OrdinalIgnoreCase));
        Assert.IsFalse(transport.Serialized.Contains(
            @"""path""",
            StringComparison.OrdinalIgnoreCase));
        Assert.IsFalse(transport.Serialized.Contains(
            @"""url""",
            StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public async Task DispatcherRejectsUnconfirmedRestoreBeforePythonTransport()
    {
        var transport = new BackupTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            sink);
        dispatcher.SetProductDataGateway(gateway);

        dispatcher.Dispatch(Request(
            "backup.restore",
            "restore-bad",
            """{"name":"manual_20260724_101500.zip","confirmed":false}"""));

        var failure = await sink.WaitForFailedAsync();
        Assert.AreEqual("restore-bad", failure!.RequestId);
        Assert.HasCount(0, transport.Methods);
    }

    private static RoutedWebRequest Request(
        string type,
        string requestId,
        string payload)
    {
        using var document = JsonDocument.Parse(payload);
        return new RoutedWebRequest(
            type,
            requestId,
            document.RootElement.Clone(),
            string.Empty);
    }

    private sealed class BackupTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming =
            Channel.CreateUnbounded<JsonElement?>();

        public List<string> Methods { get; } = [];
        public string Serialized { get; private set; } = string.Empty;

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            Serialized += line;
            using var request = JsonDocument.Parse(line);
            string id = request.RootElement.GetProperty("id").GetString()!;
            string method = request.RootElement.GetProperty("method").GetString()!;
            Methods.Add(method);
            string result = method switch
            {
                "backup.list" => """{"backups":[]}""",
                "backup.create" =>
                    """{"backup":{"name":"manual_20260724_101500.zip","size":1,"modified":"2026-07-24T10:15:00Z","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"integrityValid":true}""",
                _ => """{"status":"restarting"}""",
            };
            using var response = JsonDocument.Parse(
                $$"""{"jsonrpc":"2.0","id":"{{id}}","result":{{result}}}""");
            _incoming.Writer.TryWrite(response.RootElement.Clone());
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }
}
