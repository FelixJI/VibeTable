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
    public void SchemaLifecycleTimeoutDoesNotReuseDashboardPolicy()
    {
        TimeSpan dashboardTimeout = TimeSpan.FromMilliseconds(30);

        TimeSpan schemaTimeout =
            WorkspaceRequestDispatcher.ResolveSchemaLifecycleTimeout(null);

        Assert.AreEqual(SchemaLifecycleBudget.DefaultTimeout, schemaTimeout);
        Assert.AreNotEqual(dashboardTimeout, schemaTimeout);
    }

    [TestMethod]
    public void DispatcherComposesControllerOwnedRoutesWithoutFallbackUnion()
    {
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            new FakeWebReplySink());

        Assert.IsTrue(dispatcher.Handles("table.queryRequested"));
        Assert.IsTrue(dispatcher.Handles("dashboard.cancelRequested"));
        Assert.IsTrue(dispatcher.Handles("interface.commitRequested"));
        Assert.IsTrue(dispatcher.Handles("document.listRequested"));
        Assert.IsFalse(dispatcher.Handles("plugin.catalog.list"));
        Assert.IsFalse(dispatcher.Handles("unknown.request"));
    }

    [TestMethod]
    public async Task UnhandledFailureNamesTheOriginatingOperationWithoutLeakingDetails()
    {
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            sink);
        using var document = JsonDocument.Parse("""{"table":"missing"}""");

        dispatcher.Dispatch(new RoutedWebRequest(
            "table.selected",
            null,
            document.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        JsonElement payload = JsonSerializer.SerializeToElement(failure.Payload);
        Assert.AreEqual("WORKSPACE_ERROR", payload.GetProperty("code").GetString());
        Assert.AreEqual("Workspace operation failed.", payload.GetProperty("message").GetString());
        Assert.AreEqual("table.selected", payload.GetProperty("operation").GetString());
    }

    [TestMethod]
    public async Task TableSelection_SubscriberBackendFailureUsesProgrammerDefectFallback()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(
            new[] { "records" },
            Array.Empty<string>(),
            TestDisplayNames.For("records"));
        gateway.CursorOpenResults["records"] = EmptyPage("records");
        var workspace = new TableWorkspaceService(gateway);
        await workspace.OpenDatabaseAsync("db");
        workspace.Notification += _ =>
            throw new BackendUnavailableException("subscriber failed");
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            workspace,
            new FakeDatabasePicker("local://configured"),
            sink);
        using var document = JsonDocument.Parse("""{"table":"records"}""");

        dispatcher.Dispatch(new RoutedWebRequest(
            "table.selected",
            "select-subscriber",
            document.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        JsonElement payload = JsonSerializer.SerializeToElement(failure.Payload);
        Assert.AreEqual("WORKSPACE_ERROR", payload.GetProperty("code").GetString());
        Assert.AreEqual("table.selected", payload.GetProperty("operation").GetString());
    }

    [TestMethod]
    public async Task TableSelection_ReportsStableUnavailableAfterRecoveryDeadline()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(
            new[] { "records" },
            Array.Empty<string>(),
            TestDisplayNames.For("records"));
        var lateAttempt = new TaskCompletionSource<TablePage>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var attemptStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int attempts = 0;
        gateway.CursorOpenOverride = (_, _, _) =>
        {
            attempts += 1;
            if (attempts == 1)
            {
                return Task.FromException<TablePage>(
                    new BackendUnavailableException("sidecar restarting"));
            }
            attemptStarted.TrySetResult();
            return lateAttempt.Task;
        };
        gateway.EditSchemaResults["records"] = new EditSchemaResult(
            "records",
            "schema-records",
            "primary_key",
            RowKeyStable: true,
            Editable: true,
            Array.Empty<ColumnEditSchema>());
        var time = new ManualTimeProvider();
        var workspace = new TableWorkspaceService(
            gateway,
            selectionRecoveryTimeout: TimeSpan.FromSeconds(3),
            timeProvider: time);
        await workspace.OpenDatabaseAsync("db");
        var notifications = new List<TableNotification>();
        workspace.Notification += notifications.Add;
        var sink = new FakeWebReplySink();
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://configured"),
            sink,
            () => null);
        using var document = JsonDocument.Parse("""{"table":"records"}""");

        Task recoveryScheduled = time.WaitForScheduledTimersAsync(2);
        Task selection = controller.DispatchAsync(new RoutedWebRequest(
            "table.selected",
            null,
            document.RootElement.Clone(),
            string.Empty));
        await recoveryScheduled;
        time.Advance(TimeSpan.FromMilliseconds(25));
        await attemptStarted.Task;
        time.Advance(TimeSpan.FromMilliseconds(2_975));

        Assert.IsTrue(selection.IsCompleted,
            "the controller operation must stabilize even if the RPC ignores cancellation");
        await selection;

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        JsonElement payload = JsonSerializer.SerializeToElement(failure.Payload);
        Assert.AreEqual("BACKEND_UNAVAILABLE", payload.GetProperty("code").GetString());
        Assert.AreEqual("table.selected", payload.GetProperty("operation").GetString());

        lateAttempt.SetResult(EmptyPage("records"));
        await Task.Yield();
        Assert.AreEqual(0, notifications.Count,
            "late recovery completion must publish neither dataset nor schema");
        Assert.AreEqual(2, attempts);
    }

    [TestMethod]
    [Timeout(2_000)]
    public async Task TableSelection_SessionCloseSilencesIgnoredCancellationAndLateSuccess()
    {
        using var session = new CancellationTokenSource();
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(
            new[] { "records" },
            Array.Empty<string>(),
            TestDisplayNames.For("records"));
        var readStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var lateRead = new TaskCompletionSource<TablePage>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        gateway.CursorOpenOverride = (_, _, _) =>
        {
            readStarted.TrySetResult();
            return lateRead.Task;
        };
        gateway.EditSchemaResults["records"] = new EditSchemaResult(
            "records",
            "schema-records",
            "primary_key",
            RowKeyStable: true,
            Editable: true,
            Array.Empty<ColumnEditSchema>());
        var workspace = new TableWorkspaceService(gateway);
        await workspace.OpenDatabaseAsync("db");
        var notifications = new List<TableNotification>();
        workspace.Notification += notifications.Add;
        var sink = new FakeWebReplySink();
        int tokenCaptures = 0;
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://configured"),
            sink,
            () => null,
            sessionToken: () =>
            {
                tokenCaptures += 1;
                return session.Token;
            });
        using var document = JsonDocument.Parse("""{"table":"records"}""");

        var request = new RoutedWebRequest(
            "table.selected",
            "select-session",
            document.RootElement.Clone(),
            string.Empty);
        Task selection = controller.DispatchAsync(request);
        await readStarted.Task;
        session.Cancel();
        await selection;
        lateRead.SetResult(EmptyPage("records"));
        await Task.Yield();

        Assert.AreEqual(1, tokenCaptures,
            "the controller must capture one stable session token per selection");
        Assert.AreEqual(0, notifications.Count,
            "session close must suppress late dataset and schema notifications");
        Assert.AreEqual(0, sink.Replies.Count,
            "session close is silent and must not post a correlated failure");
    }

    [TestMethod]
    [Timeout(2_000)]
    public async Task TableSelection_SessionCloseSilencesIgnoredSchemaCancellation()
    {
        using var session = new CancellationTokenSource();
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(
            new[] { "records" },
            Array.Empty<string>(),
            TestDisplayNames.For("records"));
        gateway.CursorOpenResults["records"] = EmptyPage("records");
        var schemaStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var lateSchema = new TaskCompletionSource<EditSchemaResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        gateway.EditSchemaOverride = (_, _) =>
        {
            schemaStarted.TrySetResult();
            return lateSchema.Task;
        };
        var workspace = new TableWorkspaceService(gateway);
        await workspace.OpenDatabaseAsync("db");
        var notifications = new List<TableNotification>();
        workspace.Notification += notifications.Add;
        var sink = new FakeWebReplySink();
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://configured"),
            sink,
            () => null,
            sessionToken: () => session.Token);
        using var document = JsonDocument.Parse("""{"table":"records"}""");

        var request = new RoutedWebRequest(
            "table.selected",
            "select-schema-session",
            document.RootElement.Clone(),
            string.Empty);
        Task selection = controller.DispatchAsync(request);
        await schemaStarted.Task;
        Assert.AreEqual(1, notifications.Count);
        Assert.AreEqual("table.datasetReady", notifications[0].Type);

        session.Cancel();
        await selection;
        lateSchema.SetResult(new EditSchemaResult(
            "records",
            "schema-late",
            "primary_key",
            RowKeyStable: true,
            Editable: true,
            Array.Empty<ColumnEditSchema>()));
        await Task.Yield();

        Assert.AreEqual(1, notifications.Count,
            "late schema must not publish after session close");
        Assert.AreEqual(0, sink.Replies.Count,
            "session close during schema read must remain silent");
    }

    [TestMethod]
    public async Task TableSelection_SessionCloseWinsRecoveryDeadlineWithoutReply()
    {
        using var session = new CancellationTokenSource();
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(
            new[] { "records" },
            Array.Empty<string>(),
            TestDisplayNames.For("records"));
        gateway.CursorOpenOverride = (_, _, _) =>
            Task.FromException<TablePage>(
                new BackendUnavailableException("sidecar restarting"));
        var time = new ManualTimeProvider();
        var workspace = new TableWorkspaceService(
            gateway,
            selectionRecoveryTimeout: TimeSpan.FromMilliseconds(25),
            timeProvider: time);
        await workspace.OpenDatabaseAsync("db");
        var sink = new FakeWebReplySink();
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://configured"),
            sink,
            () => null,
            sessionToken: () => session.Token);
        using var document = JsonDocument.Parse("""{"table":"records"}""");

        Task timersScheduled = time.WaitForScheduledTimersAsync(2);
        Task selection = controller.DispatchAsync(new RoutedWebRequest(
            "table.selected",
            "select-deadline-session",
            document.RootElement.Clone(),
            string.Empty));
        await timersScheduled;
        time.BeforeTimerFire = session.Cancel;
        time.Advance(TimeSpan.FromMilliseconds(25));
        await selection;

        Assert.IsTrue(session.IsCancellationRequested);
        Assert.AreEqual(0, sink.Replies.Count,
            "session ownership outranks an exhausted deadline and stays silent");
    }

    [TestMethod]
    public async Task TableSelection_DoesNotPublishSchemaAfterDatasetReentersNewSelection()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(
            new[] { "alpha", "beta" },
            Array.Empty<string>(),
            TestDisplayNames.For("alpha", "beta"));
        gateway.CursorOpenResults["alpha"] = EmptyPage("alpha");
        gateway.CursorOpenResults["beta"] = EmptyPage("beta");
        gateway.EditSchemaResults["alpha"] = new EditSchemaResult(
            "alpha",
            "schema-alpha",
            "primary_key",
            RowKeyStable: true,
            Editable: true,
            Array.Empty<ColumnEditSchema>());
        var workspace = new TableWorkspaceService(gateway);
        await workspace.OpenDatabaseAsync("db");
        var notifications = new List<TableNotification>();
        Task<bool>? betaSelection = null;
        workspace.Notification += notification =>
        {
            notifications.Add(notification);
            if (notification.Type == "table.datasetReady"
                && notification.Page?.Table == "alpha")
            {
                betaSelection = workspace.SelectTableAsync("beta");
            }
        };
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://configured"),
            new FakeWebReplySink(),
            () => null);
        using var document = JsonDocument.Parse("""{"table":"alpha"}""");

        await controller.DispatchAsync(new RoutedWebRequest(
            "table.selected",
            null,
            document.RootElement.Clone(),
            string.Empty));
        Assert.IsNotNull(betaSelection);
        Assert.IsTrue(await betaSelection);

        var schemas = notifications
            .Where(notification => notification.Type == "table.editSchemaLoaded")
            .Select(notification => notification.MutationResult?.Result)
            .OfType<EditSchemaResult>()
            .ToList();
        Assert.IsFalse(schemas.Any(schema => schema.Table == "alpha"),
            "alpha schema must not borrow beta's generation after reentrant selection");
    }

    [TestMethod]
    public void ProductControllerHandlesOnlyRegisteredProductAndRelationRequests()
    {
        foreach (string type in ProductDataRpcRegistry.RequestTypes)
            Assert.IsTrue(ProductDataRequestController.Handles(type), type);
        foreach (string type in RelationLookupRpcRegistry.RequestTypes)
            Assert.IsTrue(ProductDataRequestController.Handles(type), type);
        Assert.IsFalse(ProductDataRequestController.Handles("rpc.invoke"));
        Assert.IsFalse(ProductDataRequestController.Handles("schema.rawRequested"));
    }

    [TestMethod]
    public async Task TableQuery_ForwardsCanonicalAstWithoutRepairingFields()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.QueryWindowResults["records"] = new TablePage(
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
                "groups": [
                  {"field": "amount", "direction": "asc", "bucket": "number", "numberInterval": 50},
                  {"field": "created", "direction": "desc", "bucket": "month"}
                ],
                "summaries": [
                  {"field": "amount", "function": "sum"}
                ],
                "offset": 25,
                "limit": 500,
                "groupOffset": 100,
                "groupLimit": 50,
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

        Assert.AreEqual(2, gateway.RawViewQueries.Count,
            "grouped queries use the same opaque AST for cursor rows and aggregates");
        JsonElement query = gateway.RawViewQueries[0];
        Assert.AreEqual("needle", query.GetProperty("keyword").GetString());
        Assert.AreEqual("not-forwarded", query.GetProperty("ignored").GetString());
        Assert.AreEqual(
            "not-forwarded",
            query.GetProperty("filters")[0].GetProperty("ignored").GetString());
        JsonElement composite = query.GetProperty("filters")[1].GetProperty("value");
        Assert.AreEqual(2, composite[0].GetProperty("rank").GetInt32());
        Assert.AreEqual(3, composite[1].GetInt32());
        Assert.IsTrue(composite[2].GetBoolean());
    }

    [TestMethod]
    public async Task TableQuery_ForwardsUnknownOperatorsForSidecarValidation()
    {
        var gateway = new FakeTableRpcGateway();
        var coordinator = new GridStateCoordinator(gateway, _ => { });
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(gateway),
            new FakeDatabasePicker("local://configured"),
            sink,
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

        Assert.AreEqual(
            "raw_sql",
            gateway.RawViewQueries.Single().GetProperty("filters")[0]
                .GetProperty("operator").GetString());
        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "operation.failed"));
    }

    [TestMethod]
    public async Task TableQuery_ForwardsNestedFilterGroupsWithoutFlatteningThem()
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
                  {
                    "logic": "OR",
                    "groupLogic": "OR",
                    "filters": [
                      {"field": "status", "operator": "eq", "value": "open"},
                      {"field": "priority", "operator": "eq", "value": "urgent"}
                    ]
                  }
                ]
              }
            }
            """);

        dispatcher.Dispatch(new RoutedWebRequest(
            "table.queryRequested",
            "query-nested",
            document.RootElement.Clone(),
            string.Empty));
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 150);

        JsonElement group = gateway.RawViewQueries.Single().GetProperty("filters")[0];
        Assert.AreEqual("OR", group.GetProperty("logic").GetString());
        Assert.AreEqual("OR", group.GetProperty("groupLogic").GetString());
        Assert.AreEqual(2, group.GetProperty("filters").GetArrayLength());
        Assert.AreEqual("status", group.GetProperty("filters")[0].GetProperty("field").GetString());
        Assert.AreEqual("priority", group.GetProperty("filters")[1].GetProperty("field").GetString());
    }

    [TestMethod]
    public async Task ProductQuery_WaitsForReplacementGatewayDuringBackendRecovery()
    {
        await using var staleClient = new JsonRpcClient(new QueryTransport());
        using var staleGateway = new JsonRpcProductDataGateway(staleClient);
        var sink = new FakeWebReplySink();
        var controller = new ProductDataRequestController(sink);
        controller.SetGateway(staleGateway);
        staleGateway.Dispose();

        using var document = JsonDocument.Parse(
            """{"tableId":"tbl_records","query":{"filters":[],"sorts":[],"offset":0,"limit":100}}""");
        Task dispatch = controller.DispatchAsync(new RoutedWebRequest(
            "query.page",
            "recovering-query",
            document.RootElement.Clone(),
            string.Empty));

        await Task.Delay(50);
        await using var readyClient = new JsonRpcClient(new QueryTransport());
        using var readyGateway = new JsonRpcProductDataGateway(readyClient);
        controller.SetGateway(readyGateway);
        await dispatch;

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
        var controller = new ProductDataRequestController(
            sink,
            readRecoveryTimeout: TimeSpan.FromMilliseconds(75));
        controller.SetGateway(staleGateway);
        staleGateway.Dispose();

        using var document = JsonDocument.Parse(
            """{"tableId":"tbl_records","query":{"filters":[],"sorts":[],"offset":0,"limit":100}}""");
        await controller.DispatchAsync(new RoutedWebRequest(
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
        var controller = new ProductDataRequestController(sink);
        controller.SetGateway(staleGateway);
        staleGateway.Dispose();

        using var document = JsonDocument.Parse(
            """{"tableId":"tbl_records","operations":[]}""");
        await controller.DispatchAsync(new RoutedWebRequest(
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

    [TestMethod]
    public async Task FieldApply_PublishesExactlyOneTerminalSuccess()
    {
        var sink = new FakeWebReplySink();
        var controller = new ProductDataRequestController(sink);
        await using var client = new JsonRpcClient(new QueryTransport());
        using var gateway = new JsonRpcProductDataGateway(client);
        controller.SetGateway(gateway);
        using var document = JsonDocument.Parse(
            """
            {
              "planId": "plan-1",
              "planHash": "hash-1",
              "operationId": "operation-1",
              "actor": {"id": "tester", "kind": "user"},
              "confirmations": []
            }
            """);

        await controller.DispatchAsync(new RoutedWebRequest(
            "field.change.apply",
            "field-apply-1",
            document.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply[] terminalReplies = sink.Replies
            .Where(reply => reply.RequestId == "field-apply-1")
            .ToArray();
        Assert.HasCount(1, terminalReplies);
        Assert.AreEqual("field.change.apply", terminalReplies[0].Type);
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
            string method = request.RootElement.GetProperty("method").GetString()!;
            string result = method == "field.change.apply"
                ? """
                  {
                    "contract": "vibetable.schema.v2",
                    "operationId": "operation-1",
                    "planId": "plan-1",
                    "action": "update",
                    "tableId": "tbl_records",
                    "fieldId": "fld_title",
                    "schemaRevision": "schema_0002",
                    "definition": null,
                    "migrationJobId": ""
                  }
                  """
                : """
                  {
                    "rows": [],
                    "total": 0,
                    "snapshot": {"schemaRevision": "schema_0001"}
                  }
                  """;
            using var response = JsonDocument.Parse(
                $$"""
                {
                  "jsonrpc": "2.0",
                  "id": "{{id}}",
                  "result": {{result}}
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

    private static TablePage EmptyPage(string table)
        => new(
            table,
            Array.Empty<ColumnSchema>(),
            Array.Empty<Dictionary<string, object?>>(),
            0,
            TableWorkspaceLimits.MaxPageLimit,
            0,
            "remote");

}
