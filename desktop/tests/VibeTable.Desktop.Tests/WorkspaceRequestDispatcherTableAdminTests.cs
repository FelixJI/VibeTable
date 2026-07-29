using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceRequestDispatcherTableAdminTests
{
    [TestMethod]
    public async Task CreateTable_IgnoresRendererIdentitiesAndBootstrapsAnEmptyOpaqueTable()
    {
        var tableGateway = new FakeTableRpcGateway
        {
            ListTablesResult = new TableSummary(
                ["tbl_created"],
                Array.Empty<string>(),
                new Dictionary<string, string> { ["tbl_created"] = "Orders" }),
        };
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(tableGateway),
            new FakeDatabasePicker("local://configured"),
            sink);
        var transport = new SchemaCaptureTransport();
        await using var client = new JsonRpcClient(transport);
        using var productGateway = new JsonRpcProductDataGateway(client);
        dispatcher.SetProductDataGateway(productGateway);
        using var payload = JsonDocument.Parse(
            """
            {
              "displayName": "  Orders  ",
              "tableId": "renderer_controlled",
              "physicalName": "renderer_controlled",
              "fields": [{"fieldId":"renderer_controlled"}]
            }
            """);

        dispatcher.Dispatch(new RoutedWebRequest(
            "tableAdmin.createRequested",
            "create-1",
            payload.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply? changed =
            await sink.WaitForAsync("database.collectionsChanged", 4_000);
        Assert.IsNotNull(changed);
        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "operation.failed"));

        var calls = transport.Calls;
        CollectionAssert.AreEqual(
            new[] { "schema.validate", "schema.apply" },
            calls.Select(call => call.Method).ToArray());
        JsonElement validatedDefinition =
            calls[0].Parameters.GetProperty("definition");
        string tableId = validatedDefinition.GetProperty("tableId").GetString()!;
        string physicalName =
            validatedDefinition.GetProperty("physicalName").GetString()!;
        Assert.IsTrue(
            tableId.StartsWith("tbl_", StringComparison.Ordinal)
            && tableId.Length == 24);
        Assert.IsTrue(
            physicalName.StartsWith("t_", StringComparison.Ordinal)
            && physicalName.Length == 22);
        Assert.AreNotEqual("renderer_controlled", tableId);
        Assert.AreEqual("Orders", validatedDefinition.GetProperty("displayName").GetString());
        Assert.AreEqual(0, validatedDefinition.GetProperty("fields").GetArrayLength());
        Assert.AreEqual(0, validatedDefinition.GetProperty("indexes").GetArrayLength());
        Assert.AreEqual(
            tableId,
            calls[1].Parameters
                .GetProperty("definition")
                .GetProperty("tableId")
                .GetString());
    }

    [TestMethod]
    public async Task CreateTable_RejectsInvalidDisplayNameBeforeBackendLookup()
    {
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            sink);
        using var payload = JsonDocument.Parse(
            """{"displayName":"bad\u0001name"}""");

        dispatcher.Dispatch(new RoutedWebRequest(
            "tableAdmin.createRequested",
            "create-invalid",
            payload.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        string serialized = JsonSerializer.Serialize(failure.Payload);
        StringAssert.Contains(serialized, @"""code"":""BAD_PAYLOAD""");
    }

    private sealed class SchemaCaptureTransport : IJsonLineTransport
    {
        internal sealed record Call(string Method, JsonElement Parameters);

        private readonly Channel<JsonElement?> _incoming =
            Channel.CreateUnbounded<JsonElement?>();
        private readonly List<Call> _calls = [];
        private readonly object _gate = new();

        public Call[] Calls
        {
            get
            {
                lock (_gate)
                {
                    return _calls.ToArray();
                }
            }
        }

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            using var request = JsonDocument.Parse(line);
            JsonElement root = request.RootElement;
            string id = root.GetProperty("id").GetString()!;
            string method = root.GetProperty("method").GetString()!;
            JsonElement parameters = root.GetProperty("params").Clone();
            lock (_gate)
            {
                _calls.Add(new Call(method, parameters));
            }
            JsonElement result = method switch
            {
                "schema.validate" => JsonSerializer.SerializeToElement(new
                {
                    definition = parameters.GetProperty("definition"),
                    capabilities = new Dictionary<string, object>(),
                }),
                "schema.apply" => parameters.GetProperty("definition").Clone(),
                _ => throw new InvalidOperationException(
                    $"Unexpected RPC method: {method}"),
            };
            JsonElement response = JsonSerializer.SerializeToElement(new
            {
                jsonrpc = "2.0",
                id,
                result,
            });
            _incoming.Writer.TryWrite(response);
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }
}
