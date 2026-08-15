using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts.Generated;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class JsonRpcSurfaceGatewayTests
{
    [TestMethod]
    public async Task GatewayUsesOnlyClosedInterfaceMethodsAndGeneratedDtos()
    {
        var transport = new SurfaceTransport();
        await using var client = new JsonRpcClient(transport);
        var gateway = new JsonRpcSurfaceGateway(client);

        InterfaceListResult listed = await gateway.ListAsync(CancellationToken.None);
        AssertMethod(transport, "interface.list");
        Assert.HasCount(1, listed.Items);

        InterfaceSnapshot loaded = await gateway.LoadAsync("if-orders", CancellationToken.None);
        AssertMethod(transport, "interface.load");
        Assert.AreEqual("if-orders", loaded.Definition.InterfaceId);
        Assert.AreEqual(
            "if-orders",
            transport.LastRequest.GetProperty("params").GetProperty("interfaceId").GetString());

        InterfaceSnapshot committed = await gateway.CommitAsync(
            new InterfaceCommitRequest
            {
                Definition = Definition(),
                ExpectedRevision = loaded.Revision,
                IdempotencyKey = "surface-save-1",
            },
            CancellationToken.None);
        AssertMethod(transport, "interface.commit");
        Assert.AreEqual(loaded.Revision, committed.Revision);

        InterfaceDeleteResult deleted = await gateway.DeleteAsync(
            new InterfaceDeleteRequest
            {
                InterfaceId = "if-orders",
                ExpectedRevision = loaded.Revision,
                IdempotencyKey = "surface-delete-1",
            },
            CancellationToken.None);
        AssertMethod(transport, "interface.delete");
        Assert.AreEqual("if-orders", deleted.InterfaceId);

        CollectionAssert.AreEqual(
            new[] { "interface.list", "interface.load", "interface.commit", "interface.delete" },
            transport.Methods);
    }

    private static void AssertMethod(SurfaceTransport transport, string expected)
        => Assert.AreEqual(expected, transport.LastRequest.GetProperty("method").GetString());

    private static InterfaceDefinition Definition()
        => new()
        {
            ContractVersion = "1.0",
            InterfaceId = "if-orders",
            Name = "Orders",
            Bindings = [],
            Actions = [],
            Pages =
            [
                new InterfacePage
                {
                    PageId = "list",
                    Title = "Orders",
                    Elements = [],
                },
            ],
        };

    private sealed class SurfaceTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming =
            Channel.CreateUnbounded<JsonElement?>();
        public JsonElement LastRequest { get; private set; }
        public List<string> Methods { get; } = [];

        public Task<JsonElement?> ReadAsync(CancellationToken token)
            => _incoming.Reader.ReadAsync(token).AsTask();

        public Task WriteAsync(string line, CancellationToken token)
        {
            using var request = JsonDocument.Parse(line);
            LastRequest = request.RootElement.Clone();
            string id = LastRequest.GetProperty("id").GetString()!;
            string method = LastRequest.GetProperty("method").GetString()!;
            Methods.Add(method);
            string result = method switch
            {
                "interface.list" =>
                    $$"""{"items":[{"interfaceId":"if-orders","name":"Orders","revision":"{{Revision}}"}]}""",
                "interface.load" or "interface.commit" => SnapshotJson,
                "interface.delete" => """{"interfaceId":"if-orders"}""",
                _ => "{}",
            };
            using var response = JsonDocument.Parse(
                $$"""{"jsonrpc":"2.0","id":"{{id}}","result":{{result}}}""");
            _incoming.Writer.TryWrite(response.RootElement.Clone());
            return Task.CompletedTask;
        }

        private const string Revision =
            "sha256:1111111111111111111111111111111111111111111111111111111111111111";
        private const string SnapshotJson =
            """{"definition":{"contractVersion":"1.0","interfaceId":"if-orders","name":"Orders","bindings":[],"actions":[],"pages":[{"pageId":"list","title":"Orders","elements":[]}]},"revision":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}""";

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }
}
