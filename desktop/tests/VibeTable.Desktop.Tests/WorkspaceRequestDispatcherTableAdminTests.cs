using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceTableRequestControllerTests
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
        IProductDataRpcGateway? currentGateway = null;
        var controller = new WorkspaceTableRequestController(
            new TableWorkspaceService(tableGateway),
            new FakeDatabasePicker("local://configured"),
            sink,
            () => currentGateway);
        var transport = new SchemaCaptureTransport();
        await using var client = new JsonRpcClient(transport);
        using var productGateway = new JsonRpcProductDataGateway(client);
        currentGateway = productGateway;
        using var payload = JsonDocument.Parse(
            """
            {
              "displayName": "  Orders  ",
              "tableId": "renderer_controlled",
              "physicalName": "renderer_controlled",
              "fields": [{"fieldId":"renderer_controlled"}]
            }
            """);

        await controller.DispatchAsync(new RoutedWebRequest(
            "tableAdmin.createRequested",
            "create-1",
            payload.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply? changed =
            await sink.WaitForAsync("database.collectionsChanged", 4_000);
        Assert.IsNotNull(changed);
        FakeWebReplySink.Reply[] collectionChanges = sink.Replies
            .Where(reply => reply.Type == "database.collectionsChanged")
            .ToArray();
        Assert.AreEqual(2, collectionChanges.Length);
        Assert.AreEqual("create-1", collectionChanges[0].RequestId);
        Assert.IsNull(collectionChanges[1].RequestId);
        string correlatedPayload = JsonSerializer.Serialize(
            collectionChanges[0].Payload);
        StringAssert.Contains(
            correlatedPayload,
            @"""createdTableId"":""tbl_created""");
        Assert.IsFalse(correlatedPayload.Contains(
            "deletedTableId",
            StringComparison.Ordinal));
        string broadcastPayload = JsonSerializer.Serialize(
            collectionChanges[1].Payload);
        Assert.IsFalse(broadcastPayload.Contains(
            "createdTableId",
            StringComparison.Ordinal));
        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "operation.failed"));

        var calls = transport.Calls;
        CollectionAssert.AreEqual(
            new[] { "schema.table.create" },
            calls.Select(call => call.Method).ToArray());
        JsonElement intent = calls[0].Parameters;
        Assert.AreEqual("Orders", intent.GetProperty("displayName").GetString());
        Assert.IsTrue(intent.GetProperty("operationId").GetString()!
            .StartsWith("table-create-", StringComparison.Ordinal));
        Assert.AreEqual("desktop-host", intent.GetProperty("actor").GetProperty("id").GetString());
        Assert.IsFalse(intent.TryGetProperty("tableId", out _));
        Assert.IsFalse(intent.TryGetProperty("physicalName", out _));
        Assert.IsFalse(intent.TryGetProperty("fields", out _));
    }

    [TestMethod]
    public async Task CreateTable_RejectsInvalidDisplayNameBeforeBackendLookup()
    {
        var sink = new FakeWebReplySink();
        var controller = new WorkspaceTableRequestController(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            sink,
            () => null);
        using var payload = JsonDocument.Parse(
            """{"displayName":"bad\u0001name"}""");

        await controller.DispatchAsync(new RoutedWebRequest(
            "tableAdmin.createRequested",
            "create-invalid",
            payload.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        string serialized = JsonSerializer.Serialize(failure.Payload);
        StringAssert.Contains(serialized, @"""code"":""BAD_PAYLOAD""");
    }

    [TestMethod]
    public async Task CreateTable_TimesOutTheWholeLifecycleAndSuppressesLateRefresh()
    {
        var time = new ManualTimeProvider();
        var refreshStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var lateRefresh = new TaskCompletionSource<TableSummary>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var tableGateway = new FakeTableRpcGateway
        {
            ListTablesOverride = _ =>
            {
                refreshStarted.TrySetResult();
                return lateRefresh.Task;
            },
        };
        var sink = new FakeWebReplySink();
        IProductDataRpcGateway? currentGateway = null;
        var controller = new WorkspaceTableRequestController(
            new TableWorkspaceService(tableGateway),
            new FakeDatabasePicker("local://configured"),
            sink,
            () => currentGateway,
            schemaLifecycleTimeout: TimeSpan.FromSeconds(5),
            timeProvider: time);
        var transport = new SchemaCaptureTransport();
        await using var client = new JsonRpcClient(transport);
        using var productGateway = new JsonRpcProductDataGateway(client);
        currentGateway = productGateway;
        using var payload = JsonDocument.Parse("""{"displayName":"Orders"}""");

        Task dispatch = controller.DispatchAsync(new RoutedWebRequest(
            "tableAdmin.createRequested",
            "create-timeout",
            payload.RootElement.Clone(),
            string.Empty));
        await refreshStarted.Task;
        time.Advance(TimeSpan.FromSeconds(5));
        await dispatch;

        FakeWebReplySink.Reply[] failures = sink.Replies
            .Where(reply => reply.Type == "operation.failed")
            .ToArray();
        Assert.AreEqual(1, failures.Length);
        Assert.AreEqual("create-timeout", failures[0].RequestId);
        StringAssert.Contains(
            JsonSerializer.Serialize(failures[0].Payload),
            @"""code"":""SCHEMA_LIFECYCLE_TIMEOUT""");
        Assert.IsFalse(sink.Replies.Any(reply =>
            reply.Type == "database.collectionsChanged"));

        lateRefresh.SetResult(new TableSummary(
            ["tbl_late"],
            Array.Empty<string>(),
            new Dictionary<string, string> { ["tbl_late"] = "Orders" }));
        await Task.Yield();
        Assert.AreEqual(1, sink.Replies.Count);
    }

    [TestMethod]
    public async Task DeleteTable_MapsTheNativeDeadlineToOneCorrelatedFailure()
    {
        var time = new ManualTimeProvider();
        var transport = new NoResponseTransport();
        await using var client = new JsonRpcClient(transport);
        using var productGateway = new JsonRpcProductDataGateway(client);
        var sink = new FakeWebReplySink();
        var controller = new WorkspaceTableRequestController(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            sink,
            () => productGateway,
            schemaLifecycleTimeout: TimeSpan.FromSeconds(5),
            timeProvider: time);
        using var payload = JsonDocument.Parse("""{"collection":"tbl_orders"}""");

        Task dispatch = controller.DispatchAsync(new RoutedWebRequest(
            "tableAdmin.deleteRequested",
            "delete-timeout",
            payload.RootElement.Clone(),
            string.Empty));
        await transport.Written;
        time.Advance(TimeSpan.FromSeconds(5));
        await dispatch;

        FakeWebReplySink.Reply[] failures = sink.Replies
            .Where(reply => reply.Type == "operation.failed")
            .ToArray();
        Assert.AreEqual(1, failures.Length);
        Assert.AreEqual("delete-timeout", failures[0].RequestId);
        StringAssert.Contains(
            JsonSerializer.Serialize(failures[0].Payload),
            @"""code"":""SCHEMA_LIFECYCLE_TIMEOUT""");
        Assert.IsFalse(sink.Replies.Any(reply =>
            reply.Type == "database.collectionsChanged"));
    }

    [TestMethod]
    public async Task CreateTable_MapsWorkspaceSessionCancellationWithoutLateSuccess()
    {
        using var session = new CancellationTokenSource();
        using var nextSession = new CancellationTokenSource();
        CancellationToken activeSessionToken = session.Token;
        var refreshStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var lateRefresh = new TaskCompletionSource<TableSummary>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var tableGateway = new FakeTableRpcGateway
        {
            ListTablesOverride = _ =>
            {
                refreshStarted.TrySetResult();
                return lateRefresh.Task;
            },
        };
        var sink = new FakeWebReplySink();
        IProductDataRpcGateway? currentGateway = null;
        var controller = new WorkspaceTableRequestController(
            new TableWorkspaceService(tableGateway),
            new FakeDatabasePicker("local://configured"),
            sink,
            () => currentGateway,
            sessionToken: () => activeSessionToken);
        var transport = new SchemaCaptureTransport();
        await using var client = new JsonRpcClient(transport);
        using var productGateway = new JsonRpcProductDataGateway(client);
        currentGateway = productGateway;
        using var payload = JsonDocument.Parse("""{"displayName":"Orders"}""");

        Task dispatch = controller.DispatchAsync(new RoutedWebRequest(
            "tableAdmin.createRequested",
            "create-cancelled",
            payload.RootElement.Clone(),
            string.Empty));
        await refreshStarted.Task;
        activeSessionToken = nextSession.Token;
        session.Cancel();
        await dispatch;

        Assert.AreEqual(1, sink.Replies.Count);
        FakeWebReplySink.Reply failure = sink.Replies[0];
        Assert.AreEqual("operation.failed", failure.Type);
        Assert.AreEqual("create-cancelled", failure.RequestId);
        StringAssert.Contains(
            JsonSerializer.Serialize(failure.Payload),
            @"""code"":""SCHEMA_LIFECYCLE_CANCELLED""");

        lateRefresh.SetResult(new TableSummary(
            ["tbl_late"],
            Array.Empty<string>(),
            new Dictionary<string, string> { ["tbl_late"] = "Orders" }));
        await Task.Yield();
        Assert.AreEqual(1, sink.Replies.Count);
    }

    [TestMethod]
    public void HandlesOnlyTheClosedTableCommandUnion()
    {
        foreach (string type in new[]
        {
            "database.openRequested",
            "table.selected",
            "table.updateCellRequested",
            "table.insertRowRequested",
            "table.deleteRowsRequested",
            "table.previewPasteRequested",
            "table.applyPasteRequested",
            "history.queryRequested",
            "history.previewRestoreRequested",
            "history.applyRestoreRequested",
            "tableAdmin.createRequested",
            "tableAdmin.deleteRequested",
        })
        {
            Assert.IsTrue(WorkspaceTableRequestController.Handles(type), type);
        }
        Assert.IsFalse(WorkspaceTableRequestController.Handles("schema.rawRequested"));
        Assert.IsFalse(WorkspaceTableRequestController.Handles("table.pageRequested"));
        Assert.IsFalse(WorkspaceTableRequestController.Handles("rpc.invoke"));
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
                "schema.table.create" => JsonSerializer.SerializeToElement(new
                {
                    contract = "vibetable.schema.v2",
                    operationId = parameters.GetProperty("operationId").GetString(),
                    tableId = "tbl_created",
                    displayName = parameters.GetProperty("displayName").GetString(),
                    schemaRevision = "schema_0001",
                }),
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

    private sealed class NoResponseTransport : IJsonLineTransport
    {
        private readonly CancellationTokenSource _closed = new();
        private readonly TaskCompletionSource _written = new(
            TaskCreationOptions.RunContinuationsAsynchronously);

        public Task Written => _written.Task;

        public async Task<JsonElement?> ReadAsync(
            CancellationToken cancellationToken)
        {
            try
            {
                await Task.Delay(Timeout.InfiniteTimeSpan, _closed.Token);
            }
            catch (OperationCanceledException) when (_closed.IsCancellationRequested)
            {
                return null;
            }
            return null;
        }

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            _written.TrySetResult();
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync()
        {
            _closed.Cancel();
            _closed.Dispose();
            return ValueTask.CompletedTask;
        }
    }
}
