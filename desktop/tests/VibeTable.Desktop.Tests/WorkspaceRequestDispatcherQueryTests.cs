using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceRequestDispatcherQueryTests
{
    [TestMethod]
    public async Task TableQuery_ForwardsTypedFiltersAndSortsToGateway()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.QueryTablePageResults["records"] = new TablePage(
            "records",
            Array.Empty<ColumnSchema>(),
            Array.Empty<Dictionary<string, object?>>(),
            0,
            500,
            1,
            "server");
        var coordinator = new GridStateCoordinator(gateway, _ => { });
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(gateway),
            new FakeDatabasePicker("local://configured"),
            new FakeWebReplySink(),
            coordinator);
        using var document = JsonDocument.Parse("""
            {
              "table": "records",
              "query": {
                "keyword": "needle",
                "filters": [
                  {
                    "field": "payload",
                    "operator": "contains",
                    "value": "8",
                    "logic": "AND",
                    "ignored": "not-forwarded"
                  },
                  {
                    "field": "metadata",
                    "operator": "in",
                    "value": [{"rank": 2}, 3, true]
                  }
                ],
                "sorts": [
                  {"field": "payload", "direction": "desc", "nullsLast": false}
                ],
                "offset": 25,
                "limit": 500,
                "ignored": "not-forwarded"
              }
            }
            """);

        dispatcher.Dispatch(new RoutedWebRequest(
            "table.queryRequested",
            "query-1",
            document.RootElement.Clone(),
            ""));
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 150);

        var query = gateway.QueryTablePageQueries.Single();
        Assert.AreEqual("needle", query.Keyword);
        Assert.AreEqual(25, query.Offset);
        Assert.AreEqual(500, query.Limit);
        Assert.AreEqual(2, query.Filters?.Count);
        Assert.AreEqual("payload", query.Filters![0].Field);
        Assert.AreEqual(FilterOperators.Contains, query.Filters[0].Operator);
        Assert.AreEqual("8", query.Filters[0].Value);
        Assert.AreEqual("AND", query.Filters[0].Logic);
        var compositeValue = (object?[])query.Filters[1].Value!;
        var objectValue = (Dictionary<string, object?>)compositeValue[0]!;
        Assert.AreEqual(2L, objectValue["rank"]);
        Assert.AreEqual(3L, compositeValue[1]);
        Assert.AreEqual(true, compositeValue[2]);
        Assert.AreEqual(1, query.Sorts?.Count);
        Assert.AreEqual("payload", query.Sorts![0].Field);
        Assert.AreEqual("desc", query.Sorts[0].Direction);
        Assert.IsFalse(query.Sorts[0].NullsLast);
    }

    [TestMethod]
    public async Task TableQuery_DropsUnknownOperatorsAndUnknownObjectFields()
    {
        var gateway = new FakeTableRpcGateway();
        var coordinator = new GridStateCoordinator(gateway, _ => { });
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(gateway),
            new FakeDatabasePicker("local://configured"),
            new FakeWebReplySink(),
            coordinator);
        using var document = JsonDocument.Parse("""
            {
              "table": "records",
              "query": {
                "filters": [
                  {"field": "payload", "operator": "raw_sql", "value": "x"}
                ]
              }
            }
            """);

        dispatcher.Dispatch(new RoutedWebRequest(
            "table.queryRequested",
            "query-2",
            document.RootElement.Clone(),
            ""));
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 150);

        var query = gateway.QueryTablePageQueries.Single();
        Assert.AreEqual(0, query.Filters?.Count);
    }

    [TestMethod]
    public async Task ProductQuery_WaitsForReplacementGatewayDuringBackendRecovery()
    {
        await using var staleClient = new JsonRpcClient(new QueryTransport());
        using var staleGateway = new JsonRpcProductDataGateway(staleClient);
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            sink);
        dispatcher.SetProductDataGateway(staleGateway);
        staleGateway.Dispose();

        using var document = JsonDocument.Parse(
            """{"tableId":"tbl_records","query":{"filters":[],"sorts":[],"offset":0,"limit":100}}""");
        dispatcher.Dispatch(new RoutedWebRequest(
            "query.page",
            "recovering-query",
            document.RootElement.Clone(),
            string.Empty));

        await Task.Delay(50);
        await using var readyClient = new JsonRpcClient(new QueryTransport());
        using var readyGateway = new JsonRpcProductDataGateway(readyClient);
        dispatcher.SetProductDataGateway(readyGateway);

        FakeWebReplySink.Reply? reply = await sink.WaitForAsync("query.page", 4_000);
        Assert.IsNotNull(reply);
        Assert.AreEqual("recovering-query", reply.RequestId);
        Assert.IsFalse(sink.Replies.Any(item => item.Type == "operation.failed"));
    }

    [TestMethod]
    public async Task ProductQuery_ReportsStableUnavailableCodeWhenRecoveryDeadlineExpires()
    {
        await using var staleClient = new JsonRpcClient(new QueryTransport());
        using var staleGateway = new JsonRpcProductDataGateway(staleClient);
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            sink,
            readRecoveryTimeout: TimeSpan.FromMilliseconds(75));
        dispatcher.SetProductDataGateway(staleGateway);
        staleGateway.Dispose();

        using var document = JsonDocument.Parse(
            """{"tableId":"tbl_records","query":{"filters":[],"sorts":[],"offset":0,"limit":100}}""");
        dispatcher.Dispatch(new RoutedWebRequest(
            "query.page",
            "unavailable-query",
            document.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        string payload = JsonSerializer.Serialize(failure.Payload);
        StringAssert.Contains(payload, @"""code"":""BACKEND_UNAVAILABLE""");
        Assert.IsFalse(payload.Contains("PRODUCT_DATA_FAILED", StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task ProductWrite_IsNotRetriedWhenGatewayWasDisposed()
    {
        await using var staleClient = new JsonRpcClient(new QueryTransport());
        using var staleGateway = new JsonRpcProductDataGateway(staleClient);
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            sink);
        dispatcher.SetProductDataGateway(staleGateway);
        staleGateway.Dispose();

        using var document = JsonDocument.Parse(
            """{"tableId":"tbl_records","operations":[]}""");
        dispatcher.Dispatch(new RoutedWebRequest(
            "mutation.apply",
            "unsafe-write",
            document.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        string payload = JsonSerializer.Serialize(failure.Payload);
        StringAssert.Contains(payload, @"""code"":""BACKEND_UNAVAILABLE""");
        Assert.IsFalse(sink.Replies.Any(item => item.Type == "mutation.apply"));
    }

    private sealed class QueryTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming =
            Channel.CreateUnbounded<JsonElement?>();

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            using var request = JsonDocument.Parse(line);
            string id = request.RootElement.GetProperty("id").GetString()!;
            using var response = JsonDocument.Parse(
                $$"""
                {
                  "jsonrpc": "2.0",
                  "id": "{{id}}",
                  "result": {
                    "rows": [],
                    "total": 0,
                    "snapshot": {"schemaRevision": "schema_0001"}
                  }
                }
                """);
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
