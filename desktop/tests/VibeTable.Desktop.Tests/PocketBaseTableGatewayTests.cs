using System.Diagnostics;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Diagnostics;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PocketBaseTableGatewayTests
{
    [TestMethod]
    [DataRow("[]")]
    [DataRow("null")]
    public async Task Review10NonObjectRpcErrorDataKeepsTheUnsubmittedFact(string data)
    {
        var transport = new ProductTransport(); transport.RespondError("schema.getTable", -32603, "original internal failure", data);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        var service = new TableWorkspaceService(gateway); var notifications = new List<TableNotification>(); service.Notification += notifications.Add;
        using var barrier = new TableMutationReadBarrier();
        var controller = new WorkspaceTableRequestController(service, new FakeDatabasePicker(null), new FakeWebReplySink(), () => null) { MutationReads = barrier };
        var request = new RoutedWebRequest("table.insertRowRequested", "preflight", JsonSerializer.SerializeToElement(new { table = "orders", values = new { title = "After" }, schemaRevision = "schema_0001" }), "");
        await controller.DispatchAsync(request);
        var error = notifications.Single().MutationResult!.Error!.Value;
        Assert.AreEqual(MutationErrorKind.Unknown, error.Kind); Assert.AreEqual("original internal failure", error.Message); Assert.IsTrue(error.NotSubmitted);
        Assert.IsFalse(transport.Methods.Contains("mutation.apply")); await barrier.WaitAsync(request, CancellationToken.None);
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task Review10SerializationFailureIsBeforeTheActualApplyInvocation(bool insert)
    {
        const string recordId = "copiedrow000001";
        string digest = "sha256:" + new string('a', 64);
        var transport = new ProductTransport(); transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond("query.readRows", JsonSerializer.Serialize(new { rows = new[] { new Dictionary<string, object?> {
            ["id"] = recordId, ["title"] = "Before", ["__vibetableDigest"] = digest,
        } } }));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        var service = new TableWorkspaceService(gateway); var notifications = new List<TableNotification>(); service.Notification += notifications.Add;
        using var barrier = new TableMutationReadBarrier();
        var request = new RoutedWebRequest("table.queryRequested", "serialization", JsonSerializer.SerializeToElement(new { table = "orders" }), "");
        await barrier.TrackAsync(request, async settled => {
            settled(false);
            if (insert) await service.InsertRowAsync("orders", new Dictionary<string, object?> { ["id"] = recordId, ["title"] = new UnserializableMutationValue() }, "schema_0001", onWriteSettled: settled);
            else await service.UpdateCellAsync("orders", recordId, "title", "Before", new UnserializableMutationValue(), "schema_0001", onWriteSettled: settled);
        });
        MutationError error = notifications.Single().MutationResult!.Error!.Value;
        Assert.AreEqual(MutationErrorKind.Unknown, error.Kind); Assert.IsTrue(error.NotSubmitted);
        Assert.IsFalse(transport.Methods.Contains("mutation.apply")); await barrier.WaitAsync(request, CancellationToken.None);
    }

    private sealed class UnserializableMutationValue
    {
        public string Value => throw new InvalidOperationException("private serialization failure");
    }

    [TestMethod]
    [DataRow("update", "rpc")]
    [DataRow("insert", "rpc")]
    [DataRow("delete", "rpc")]
    [DataRow("update", "parse")]
    [DataRow("insert", "parse")]
    [DataRow("delete", "parse")]
    [DataRow("update", "transport")]
    [DataRow("insert", "transport")]
    [DataRow("delete", "transport")]
    [DataRow("update", "rows-parse")]
    public async Task Review10AllPreApplyFailuresKeepTheirCategoryAndReleaseTheReadBarrier(string kind, string failure)
    {
        string digest = "sha256:" + new string('a', 64);
        var blocked = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var transport = new ProductTransport();
        if (failure == "rpc") transport.RespondError("schema.getTable", -32150, "original schema error", """{"code":"query.schema.failed"}""");
        else transport.RespondAfter("schema.getTable", failure == "parse" ? "{}" : Schema("orders"), failure == "transport" ? blocked.Task : Task.CompletedTask);
        if (failure == "rows-parse") transport.Respond("query.readRows", """{"rows":{}}""");
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        var service = new TableWorkspaceService(gateway); var sink = new FakeWebReplySink();
        var notifications = new List<TableNotification>(); service.Notification += notifications.Add;
        using var barrier = new TableMutationReadBarrier();
        var controller = new WorkspaceTableRequestController(service, new FakeDatabasePicker(null), sink, () => null) { MutationReads = barrier };
        object payload = kind switch {
            "update" => new { table = "orders", rowKey = "copiedrow000001", column = "title", oldValue = "Before", newValue = "After", schemaRevision = "schema_0001" },
            "insert" => new { table = "orders", values = new { title = "After" }, schemaRevision = "schema_0001" },
            _ => (object)new { table = "orders", rows = new[] { new { rowKey = "copiedrow000001", expectedDigest = digest } }, schemaRevision = "schema_0001" },
        };
        Task write = controller.DispatchAsync(new RoutedWebRequest("table." + (kind == "update" ? "updateCellRequested" : kind == "insert" ? "insertRowRequested" : "deleteRowsRequested"),
            "preflight", JsonSerializer.SerializeToElement(payload), ""));
        if (failure == "transport") { await transport.WaitForMethodCountAsync(1); transport.CloseReadLoop(); }
        await write.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.IsFalse(transport.Methods.Contains("mutation.apply"));
        MutationError error = notifications.Single().MutationResult!.Error!.Value;
        Assert.AreEqual(failure == "transport" ? MutationErrorKind.BackendUnavailable : MutationErrorKind.Unknown, error.Kind);
        if (failure == "rpc") { Assert.AreEqual("original schema error", error.Message); Assert.AreEqual("query.schema.failed", error.Code); }
        Assert.IsTrue(JsonSerializer.SerializeToElement(error).TryGetProperty("NotSubmitted", out var fact) && fact.GetBoolean());
        TableNotificationPresenter.Post(sink, notifications.Single());
        Assert.IsTrue(JsonSerializer.SerializeToElement(sink.Replies.Single().Payload).GetProperty("notSubmitted").GetBoolean());
        await barrier.WaitAsync(new RoutedWebRequest("table.queryRequested", "recovery", JsonSerializer.SerializeToElement(new { table = "orders" }), ""), CancellationToken.None);
    }

    [TestMethod]
    [DataRow("delete", "schema")]
    [DataRow("delete", "digest")]
    [DataRow("update", "schema")]
    [DataRow("update", "empty-id")]
    [DataRow("delete", "empty-id")]
    [DataRow("insert", "empty-id")]
    public async Task Review9DefinitePreflightRejectionAllowsTheNextRecoveryQuery(string kind, string failure)
    {
        string digest = "sha256:" + new string('a', 64);
        var transport = new ProductTransport(); transport.Respond("schema.getTable", Schema("orders"));
        // The real query.readRows decoder rejects empty ids before reading product rows.
        transport.RespondError("query.readRows", -32602, "rowIds must contain non-empty strings", "{}");
        transport.RespondError("mutation.apply", -32602, "recordId must be non-empty", "{}");
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        var service = new TableWorkspaceService(gateway); var sink = new FakeWebReplySink();
        var notifications = new List<TableNotification>(); service.Notification += notifications.Add;
        using var barrier = new TableMutationReadBarrier();
        var controller = new WorkspaceTableRequestController(service, new FakeDatabasePicker(null), sink, () => null) { MutationReads = barrier };
        object? rowKey = failure == "empty-id" ? null : "copiedrow000001";
        string schemaRevision = failure == "schema" ? "schema_0000" : "schema_0001";
        object payload = kind switch
        {
            "update" => new { table = "orders", rowKey, column = "title", oldValue = "Before", newValue = "After", schemaRevision },
            "delete" => new { table = "orders", rows = new[] { new { rowKey, expectedDigest = failure == "digest" ? "invalid-nonempty" : digest } }, schemaRevision },
            _ => new { table = "orders", values = new Dictionary<string, object?> { ["id"] = rowKey, ["title"] = "After" }, schemaRevision },
        };
        await controller.DispatchAsync(new RoutedWebRequest("table." + (kind == "update" ? "updateCellRequested" : kind == "delete" ? "deleteRowsRequested" : "insertRowRequested"),
            "preflight", JsonSerializer.SerializeToElement(payload), ""));
        Assert.IsFalse(transport.Methods.Contains("mutation.apply"));
        Assert.AreEqual(failure == "schema" ? MutationErrorKind.SchemaMismatch : MutationErrorKind.Validation,
            notifications.Single().MutationResult!.Error!.Value.Kind);
        var readGateway = new FakeTableRpcGateway();
        readGateway.CursorOpenResults["orders"] = new TablePage("orders", [], [], 0, 100, 0, "remote");
        var time = new ManualTimeProvider();
        var reader = new GridRequestController(new GridStateCoordinator(readGateway, _ => { }, time), sink) { MutationReads = barrier };
        Task query = reader.DispatchAsync(new RoutedWebRequest("table.queryRequested", "recovery",
            JsonSerializer.SerializeToElement(new { table = "orders", query = new { } }), ""));
        time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));
        await query.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual("table.pageLoaded", sink.Replies.Single(reply => reply.RequestId == "recovery").Type);
    }

    [TestMethod]
    [DataRow("update")]
    [DataRow("insert")]
    public async Task Review9SchemaMismatchDuringCommittedReadbackRemainsUnknown(string kind)
    {
        const string recordId = "copiedrow000001";
        string digest = "sha256:" + new string('a', 64);
        var releaseReceipt = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var transport = new ProductTransport(); transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond("query.readRows", JsonSerializer.Serialize(new { rows = new[] { new Dictionary<string, object?> {
            ["id"] = recordId, ["title"] = "Before", ["__vibetableDigest"] = digest,
        } } }));
        transport.RespondAfter("mutation.apply", JsonSerializer.Serialize(new { newRevision = "data_0002",
            affectedRows = new[] { new { recordId, operation = kind, digest } } }), releaseReceipt.Task);
        transport.Respond("query.selectionOpen", SelectionProjectionJson("schema_0002", 2, recordId, null, false));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        var service = new TableWorkspaceService(gateway); var sink = new FakeWebReplySink();
        var notifications = new List<TableNotification>(); service.Notification += notifications.Add;
        using var barrier = new TableMutationReadBarrier();
        var controller = new WorkspaceTableRequestController(service, new FakeDatabasePicker(null), sink, () => null) { MutationReads = barrier };
        object payload = kind == "update"
            ? new { table = "orders", rowKey = recordId, column = "title", oldValue = "Before", newValue = "After", schemaRevision = "schema_0001" }
            : (object)new { table = "orders", values = new Dictionary<string, object?> { ["id"] = recordId, ["title"] = "After" }, schemaRevision = "schema_0001" };
        Task write = controller.DispatchAsync(new RoutedWebRequest("table." + (kind == "update" ? "updateCellRequested" : "insertRowRequested"),
            "committed-before-schema-change", JsonSerializer.SerializeToElement(payload), ""));
        await transport.WaitForMethodCountAsync(kind == "update" ? 3 : 2);
        try
        {
            // A real query selection refreshes the shared schema cache while the committed receipt is delayed.
            await gateway.OpenTableSelectionAsync("orders", JsonSerializer.SerializeToElement(new { }), CancellationToken.None);
        }
        finally { releaseReceipt.TrySetResult(); await write; }
        Assert.AreEqual(1, transport.Methods.Count(method => method == "mutation.apply"));
        Assert.AreEqual(MutationErrorKind.Unknown, notifications.Single().MutationResult!.Error!.Value.Kind);
        Assert.IsFalse(notifications.Single().MutationResult!.Error!.Value.NotSubmitted);
        await Assert.ThrowsAsync<TableMutationUnconfirmedException>(() => barrier.WaitAsync(new RoutedWebRequest("table.queryRequested", "recovery",
            JsonSerializer.SerializeToElement(new { table = "orders" }), ""), CancellationToken.None));
    }

    [TestMethod]
    [DataRow("schema")]
    [DataRow("unknown-field")]
    [DataRow("read-only")]
    [DataRow("id")]
    public async Task Review7InsertPreflightRejectsWithoutPoisoningRecovery(string failure)
    {
        JsonObject number = V2Field("number00", "number00", "Contract number", "autoNumber");
        number["autoNumber"] = new JsonObject { ["prefix"] = "HT-", ["start"] = 1, ["width"] = 6 };
        number["display"]!["kind"] = "readonly";
        string schema = failure == "read-only" ? SchemaWithFields("orders", number) : Schema("orders");
        var transport = new ProductTransport(); transport.Respond("schema.getTable", schema);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        var service = new TableWorkspaceService(gateway); var sink = new FakeWebReplySink();
        var notifications = new List<TableNotification>(); service.Notification += notifications.Add;
        using var barrier = new TableMutationReadBarrier();
        var controller = new WorkspaceTableRequestController(service, new FakeDatabasePicker(null), sink, () => null) { MutationReads = barrier };
        var values = new Dictionary<string, object?> { [failure == "unknown-field" ? "missing" : failure == "read-only" ? "f_number00" : "title"] = "After" };
        if (failure == "id") values["id"] = "!";
        await controller.DispatchAsync(new RoutedWebRequest("table.insertRowRequested", "preflight",
            JsonSerializer.SerializeToElement(new { table = "orders", values, schemaRevision = failure == "schema" ? "schema_0000" : "schema_0001" }), ""));
        Assert.IsFalse(transport.Methods.Contains("mutation.apply"));
        Assert.AreNotEqual(MutationErrorKind.Unknown, notifications.Single().MutationResult!.Error!.Value.Kind);
        await barrier.WaitAsync(new RoutedWebRequest("table.queryRequested", "read-after-rejection",
            JsonSerializer.SerializeToElement(new { table = "orders" }), ""), CancellationToken.None);
    }

    [TestMethod]
    [DataRow("update", "apply")]
    [DataRow("update", "readback")]
    [DataRow("insert", "apply")]
    [DataRow("insert", "readback")]
    [DataRow("delete", "apply")]
    public async Task EarlyTransportFailureAfterSubmissionIsUnknown(string kind, string stage)
    {
        const string recordId = "copiedrow000001";
        string digest = "sha256:" + new string('a', 64);
        var blocked = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        string row = JsonSerializer.Serialize(new { rows = new[] { new Dictionary<string, object?> { ["id"] = recordId, ["title"] = "Before", ["__vibetableDigest"] = digest } } });
        if (kind == "update") transport.Respond("query.readRows", row);
        string receipt = JsonSerializer.Serialize(new { newRevision = "data_0002", affectedRows = new[] { new { recordId, operation = kind, digest } } });
        transport.RespondAfter("mutation.apply", receipt, stage == "apply" ? blocked.Task : Task.CompletedTask);
        if (stage == "readback") transport.RespondAfter("query.readRows", row, blocked.Task);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        var service = new TableWorkspaceService(gateway);
        var notifications = new List<TableNotification>(); service.Notification += notifications.Add;
        Task<bool> write = kind switch {
            "update" => service.UpdateCellAsync("orders", recordId, "title", "Before", "After", "schema_0001", requestId: "early-failure"),
            "insert" => service.InsertRowAsync("orders", new Dictionary<string, object?> { ["id"] = recordId, ["title"] = "After" }, "schema_0001", "early-failure"),
            _ => service.DeleteRowsAsync("orders", new[] { ((object)recordId, digest) }, "schema_0001", "early-failure"),
        };
        await transport.WaitForMethodCountAsync((kind == "update" ? 3 : 2) + (stage == "readback" ? 1 : 0));
        transport.CloseReadLoop();
        Assert.IsFalse(await write.WaitAsync(TimeSpan.FromSeconds(2)));
        Assert.AreEqual(MutationErrorKind.Unknown, notifications.Single().MutationResult!.Error!.Value.Kind);
        Assert.IsFalse(notifications.Single().MutationResult!.Error!.Value.NotSubmitted);
        Assert.AreEqual("early-failure", notifications.Single().RequestId);
        Assert.AreEqual(1, transport.Methods.Count(method => method == "mutation.apply"));
    }

    [TestMethod]
    [DataRow("apply")]
    [DataRow("readback")]
    public async Task CancellationAfterSubmissionCannotClaimZeroWrites(string stage)
    {
        string digest = "sha256:" + new string('a', 64);
        var blocked = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        string row = JsonSerializer.Serialize(new { rows = new[] { new Dictionary<string, object?> { ["id"] = "row-1", ["title"] = "Before", ["__vibetableDigest"] = digest } } });
        transport.Respond("query.readRows", row);
        transport.RespondAfter("mutation.apply", JsonSerializer.Serialize(new { newRevision = "data_0002", affectedRows = new[] { new { recordId = "row-1", operation = "update", digest } } }),
            stage == "apply" ? blocked.Task : Task.CompletedTask);
        if (stage == "readback") transport.RespondAfter("query.readRows", row, blocked.Task);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        using var cancellation = new CancellationTokenSource();
        Task<UpdateCellResult> write = gateway.UpdateCellAsync("orders", "row-1", "title", "Before", "After", "schema_0001", cancellation.Token);
        await transport.WaitForMethodCountAsync(stage == "apply" ? 3 : 4); cancellation.Cancel();
        var failure = await Assert.ThrowsExactlyAsync<InvalidOperationException>(() => write);
        Assert.AreEqual(MutationErrorKind.Unknown, MutationErrorMapper.Map(failure).Kind);
        Assert.IsInstanceOfType<OperationCanceledException>(failure.InnerException);
        Assert.AreEqual(1, transport.Methods.Count(method => method == "mutation.apply"));
    }

    [TestMethod]
    public async Task RecoveryQueryWaitsForEarlierHostPreflightAndCommittedReceipt()
    {
        string rowDigest = "sha256:" + new string('a', 64);
        var releasePreflight = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.RespondAfter("query.readRows", JsonSerializer.Serialize(new { rows = new[] {
            new Dictionary<string, object?> { ["id"] = "row-1", ["title"] = "Before", ["__vibetableDigest"] = rowDigest },
        } }), releasePreflight.Task);
        transport.Respond("mutation.apply", JsonSerializer.Serialize(new {
            affectedRows = new[] { new { recordId = "row-1", operation = "update", digest = rowDigest } }, newRevision = "data_0002",
        }));
        transport.Respond("query.readRows", JsonSerializer.Serialize(new { rows = new[] {
            new Dictionary<string, object?> { ["id"] = "row-1", ["title"] = "After", ["__vibetableDigest"] = rowDigest },
        } }));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        var queryGateway = new FakeTableRpcGateway();
        queryGateway.WindowReadOverride = (_, _, _, _) => new TablePage("orders", [], [new Dictionary<string, object?> {
            ["rowKey"] = "row-1", ["title"] = transport.Methods.Contains("mutation.apply") ? "After" : "Before",
        }], 0, 100, 1, "remote");
        var sink = new FakeWebReplySink();
        using var dispatcher = new WorkspaceRequestDispatcher(new TableWorkspaceService(gateway), new FakeDatabasePicker(null), sink,
            new GridStateCoordinator(queryGateway, _ => { }));
        Task write = dispatcher.DispatchAsyncForTesting(new RoutedWebRequest("table.updateCellRequested", "old-write",
            JsonSerializer.SerializeToElement(new { table = "orders", rowKey = "row-1", column = "title", oldValue = "Before", newValue = "After", schemaRevision = "schema_0001", expectedDigest = rowDigest }), ""));
        await transport.WaitForMethodCountAsync(2);
        Task query = dispatcher.DispatchAsyncForTesting(new RoutedWebRequest("table.queryRequested", "recovery-query",
            JsonSerializer.SerializeToElement(new { table = "orders", query = new { limit = 100 } }), ""));
        try
        {
            await Task.Delay(GridStateCoordinator.QueryDebounceMs + 100);
            Assert.IsFalse(transport.Methods.Contains("mutation.apply"), "the original write is still in actual gateway preflight");
            Assert.HasCount(0, queryGateway.RawViewQueries, "recovery must not read an old snapshot before that write can submit");
        }
        finally { releasePreflight.TrySetResult(); await write; await query; }
        TablePage page = (TablePage)sink.Replies.Single(reply => reply.RequestId == "recovery-query").Payload!;
        Assert.AreEqual("After", page.Rows.Single()["title"]);
    }

    [TestMethod]
    [DataRow(false, "mismatch")]
    [DataRow(true, "mismatch")]
    [DataRow(false, "missing-receipt")]
    [DataRow(true, "missing-receipt")]
    [DataRow(false, "missing-readback")]
    [DataRow(true, "missing-readback")]
    [DataRow(false, "different-record")]
    [DataRow(true, "different-operation")]
    [DataRow(false, "match")]
    [DataRow(true, "match")]
    public async Task MutationReadbackMustMatchItsCommittedReceipt(bool insert, string scenario)
    {
        const string recordId = "copiedrow000001";
        string committedDigest = "sha256:" + new string('a', 64);
        string readbackDigest = scenario == "mismatch" ? "sha256:" + new string('b', 64) : committedDigest;
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        if (!insert)
        {
            transport.Respond("query.readRows", JsonSerializer.Serialize(new { rows = new[] {
                new Dictionary<string, object?> { ["id"] = recordId, ["title"] = "Before", ["__vibetableDigest"] = committedDigest },
            } }));
        }
        object[] affected = scenario == "missing-receipt" ? [] : [new
        {
            recordId = scenario == "different-record" ? "other-record" : recordId,
            operation = scenario == "different-operation" ? "delete" : insert ? "insert" : "update",
            revision = "row_0002", digest = committedDigest,
        }];
        transport.Respond("mutation.apply", JsonSerializer.Serialize(new {
            contractVersion = "2.0", status = "applied", changeSetId = "change-1", affectedRows = affected,
            computedFields = new { }, newRevision = "data_0002", emittedEvents = Array.Empty<string>(), warnings = Array.Empty<object>(),
        }));
        var readback = new Dictionary<string, object?> { ["id"] = recordId, ["title"] = "Normalized authority" };
        if (scenario != "missing-readback") readback["__vibetableDigest"] = readbackDigest;
        var releaseReadback = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        transport.RespondAfter("query.readRows", JsonSerializer.Serialize(new { rows = new[] { readback } }), releaseReadback.Task);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        var service = new TableWorkspaceService(gateway);
        var notifications = new List<TableNotification>();
        service.Notification += notifications.Add;
        Task<bool> pending = insert
            ? service.InsertRowAsync("orders", new Dictionary<string, object?> { ["id"] = recordId, ["title"] = "User input" }, "schema_0001", "readback-request")
            : service.UpdateCellAsync("orders", recordId, "title", "Before", "User input", "schema_0001", requestId: "readback-request");
        await transport.WaitForMethodCountAsync(insert ? 3 : 4);
        Assert.IsFalse(pending.IsCompleted); // Commit succeeded; its separate readback is still in flight.
        releaseReadback.SetResult();
        bool applied = await pending;
        Assert.AreEqual(scenario == "match", applied);
        Assert.HasCount(1, notifications);
        Assert.AreEqual("readback-request", notifications[0].RequestId);
        if (scenario == "match")
        {
            Assert.AreEqual(insert ? "table.rowsInserted" : "table.editCommitted", notifications[0].Type);
            object? result = notifications[0].MutationResult!.Result;
            Assert.AreEqual("Normalized authority", insert
                ? ((InsertRowResult)result!).Row["title"] : ((UpdateCellResult)result!).StoredValue);
        }
        else
        {
            Assert.AreEqual("table.editRejected", notifications[0].Type);
            Assert.AreEqual(MutationErrorKind.Unknown, notifications[0].MutationResult!.Error!.Value.Kind);
        }
        Assert.AreEqual(1, transport.Methods.Count(method => method == "mutation.apply"));
    }

    [TestMethod]
    public async Task InsertCopiedRowOmitsRelationLabelsAndPreservesRelationIds()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", RelationalSchema("orders"));
        transport.Respond("mutation.apply", """
            {"contractVersion":"2.0","status":"applied","changeSetId":"change-1",
             "affectedRows":[{"recordId":"copiedrow000001","operation":"insert","revision":"row_0002",
             "digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],
             "computedFields":{},"newRevision":"data_0002","emittedEvents":[],"warnings":[]}
            """);
        transport.Respond("query.readRows", """
            {"rows":[{"id":"copiedrow000001","f_customer":"customer0000001",
            "__vibetableRelationLabels":{"f_customer":{"customer0000001":"Acme"}},
            "__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));
        var result = await gateway.InsertRowAsync("orders", new Dictionary<string, object?>
        {
            ["id"] = "copiedrow000001",
            ["f_customer"] = "customer0000001",
            ["__vibetableRelationLabels"] = JsonSerializer.SerializeToElement(new
            {
                f_customer = new Dictionary<string, string> { ["customer0000001"] = "Acme" },
            }),
        }, "schema_0001", CancellationToken.None);

        Assert.AreEqual("customer0000001", result.Row["f_customer"]);
        Assert.IsTrue(result.Row.ContainsKey("__vibetableRelationLabels"));
        Assert.IsFalse(transport.Serialized.Contains("__vibetableRelationLabels", StringComparison.Ordinal));
        StringAssert.Contains(transport.Serialized, "\"f_customer\":\"customer0000001\"");
    }
    [TestMethod]
    public async Task CatalogAndQueryViewUseOnlyClosedProductMethods()
    {
        var transport = new ProductTransport();
        transport.Respond(
            "schema.list",
            """{"tables":[""" + Schema("orders") + "]}");
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.view",
            ViewResponse("""
            {"rows":[{"id":"row-1","title":"Hello",
             "__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],
             "offset":0,"limit":100,
             "filteredRows":1,"totalRows":1,
             "snapshot":{"snapshotId":"00000000000000000000000000000000","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
             "databaseId":"local","table":"orders","schemaRevision":"schema_0001",
             "dataRevision":1,"normalizedQuery":{"keyword":"","filters":[],"sorts":[],"offset":0,"limit":100}}}
            """));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        var opened = await gateway.OpenDatabaseAsync("ignored", CancellationToken.None);
        var page = await QueryViewAsync(gateway, "orders", 0, 100);

        CollectionAssert.AreEqual(new[] { "orders" }, opened.Tables.ToArray());
        Assert.AreEqual("Orders", opened.DisplayNames["orders"]);
        Assert.AreEqual("row-1", page.Rows[0]["rowKey"]);
        Assert.AreEqual("title", page.Columns[1].Name);
        Assert.AreEqual("text", page.Columns[1].DataType);
        CollectionAssert.AreEqual(
            new[] { "eq", "is_null" },
            page.Columns[1].FilterOperators!.ToArray());
        CollectionAssert.AreEqual(
            new[] { "schema.list", "schema.getTable", "query.view" },
            transport.Methods);
        Assert.IsFalse(transport.Serialized.Contains(
            "local",
            StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public async Task RendererColumnsUseCompositeRelationAndLookupCatalogIds()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", RelationalSchema("orders"));
        transport.Respond(
            "query.view",
            ViewResponse("""
            {"rows":[],"offset":0,"limit":100,"filteredRows":0,"totalRows":0,
             "snapshot":{"snapshotId":"00000000000000000000000000000000",
             "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
             "databaseId":"local","table":"orders","schemaRevision":"schema_0001",
             "dataRevision":1,"normalizedQuery":{"offset":0,"limit":100}}}
            """));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        var page = await QueryViewAsync(gateway, "orders", 0, 100);

        Assert.AreEqual(
            "orders.fld_customer",
            page.Columns.Single(column => column.Name == "f_customer").RelationId);
        Assert.AreEqual(
            "orders.fld_customer_name",
            page.Columns.Single(column => column.Name == "f_customer_name").LookupId);
    }

    [TestMethod]
    [DoNotParallelize]
    public async Task UpdateChecksCurrentValueAndCommitsThroughMutationKernel()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.readRows",
            """{"rows":[{"id":"row-1","title":"Before","__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}""");
        transport.Respond(
            "mutation.apply",
            """
            {"contractVersion":"2.0","status":"applied","changeSetId":"change-1",
             "affectedRows":[{"recordId":"row-1","operation":"update","revision":"row_0002",
             "digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}],
             "computedFields":{},"newRevision":"data_0002","emittedEvents":[],"warnings":[]}
            """);
        transport.Respond(
            "query.readRows",
            """{"rows":[{"id":"row-1","title":"After","__vibetableDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}""");
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        using var listener = new EditDiagnosticListener { ThrowOnWrite = true };
        Trace.Listeners.Add(listener);
        try
        {
            var result = await gateway.UpdateCellAsync(
                "orders",
                "row-1",
                "title",
                "Before",
                "After",
                "schema_0001",
                CancellationToken.None);

            Assert.AreEqual("After", result.StoredValue);
            Assert.AreEqual(2, result.Revision.DataRevision);
            CollectionAssert.AreEqual(
                new[]
                {
                    "schema.getTable",
                    "query.readRows",
                    "mutation.apply",
                    "query.readRows",
                },
                transport.Methods);
            StringAssert.Contains(
                transport.Serialized,
                @"""operations"":[{""kind"":""update"",""recordId"":""row-1""");
            StringAssert.Contains(
                transport.Serialized,
                @"""expectedDigest"":""sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa""");
            Assert.HasCount(0, listener.Lines);
        }
        finally
        {
            Trace.Listeners.Remove(listener);
        }
    }

    [TestMethod]
    public async Task UpdateUsesDigestCapturedWhenCellEditingStarted()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.readRows",
            """{"rows":[{"id":"row-1","title":"Before","__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}""");
        transport.Respond(
            "mutation.apply",
            """
            {"contractVersion":"2.0","status":"applied","changeSetId":"change-1",
             "affectedRows":[{"recordId":"row-1","operation":"update","revision":"row_0002",
             "digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}],
             "computedFields":{},"newRevision":"data_0002","emittedEvents":[],"warnings":[]}
            """);
        transport.Respond(
            "query.readRows",
            """{"rows":[{"id":"row-1","title":"After","__vibetableDigest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}]}""");
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        await gateway.UpdateCellAsync(
            "orders",
            "row-1",
            "title",
            "Before",
            "After",
            "schema_0001",
            CancellationToken.None,
            "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb");

        StringAssert.Contains(
            transport.Serialized,
            @"""expectedDigest"":""sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb""");
    }

    [TestMethod]
    public async Task StaleCellValueStopsBeforeMutation()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.readRows",
            """{"rows":[{"id":"row-1","title":"Changed elsewhere","__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}""");
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        await Assert.ThrowsExactlyAsync<TableEditConflictException>(() =>
            gateway.UpdateCellAsync(
                "orders",
                "row-1",
                "title",
                "Before",
                "After",
                "schema_0001",
                CancellationToken.None));

        Assert.IsFalse(transport.Methods.Contains("mutation.apply"));
    }

    [TestMethod]
    public async Task StaleCheckIgnoresJsonObjectPropertyOrder()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.readRows",
            """
            {"rows":[{"id":"row-1","title":{"b":[1,2],"a":{"y":true,"x":"v"}},
            "__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}
            """);
        transport.Respond(
            "mutation.apply",
            """
            {"contractVersion":"2.0","status":"applied","changeSetId":"change-1",
             "affectedRows":[{"recordId":"row-1","operation":"update","revision":"row_0002",
             "digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}],
             "computedFields":{},"newRevision":"data_0002","emittedEvents":[],"warnings":[]}
            """);
        transport.Respond(
            "query.readRows",
            """
            {"rows":[{"id":"row-1","title":{"saved":true},
            "__vibetableDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));
        var oldValue = new Dictionary<string, object?>
        {
            ["a"] = new Dictionary<string, object?>
            {
                ["x"] = "v",
                ["y"] = true,
            },
            ["b"] = new object?[] { 1, 2 },
        };

        await gateway.UpdateCellAsync(
            "orders",
            "row-1",
            "title",
            oldValue,
            new Dictionary<string, object?> { ["saved"] = true },
            "schema_0001",
            CancellationToken.None);

        Assert.IsTrue(transport.Methods.Contains("mutation.apply"));
    }

    [TestMethod]
    public async Task StaleCheckTreatsJsonArrayOrderAsMeaningful()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.readRows",
            """
            {"rows":[{"id":"row-1","title":[1,2],
            "__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        await Assert.ThrowsExactlyAsync<TableEditConflictException>(() =>
            gateway.UpdateCellAsync(
                "orders",
                "row-1",
                "title",
                new object?[] { 2, 1 },
                new object?[] { 1, 2, 3 },
                "schema_0001",
                CancellationToken.None));

        Assert.IsFalse(transport.Methods.Contains("mutation.apply"));
    }

    [TestMethod]
    [DataRow("none", "delete")]
    [DataRow("deletedAt", "archive")]
    public async Task DeleteUsesSchemaArchivePolicyAndAuthoritativeDigest(
        string archiveMode,
        string operationKind)
    {
        var transport = new ProductTransport();
        transport.Respond(
            "schema.getTable",
            Schema("orders").Replace(
                @"""mode"":""none""",
                $@"""mode"":""{archiveMode}""",
                StringComparison.Ordinal));
        transport.Respond(
            "mutation.apply",
            """
            {"contractVersion":"2.0","status":"applied","changeSetId":"change-1",
             "affectedRows":[],"computedFields":{},"newRevision":"data_0002",
             "emittedEvents":[],"warnings":[]}
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        await gateway.DeleteRowsAsync(
            "orders",
            new[]
            {
                (
                    (object)"row-1",
                    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
            },
            "schema_0001",
            CancellationToken.None);

        StringAssert.Contains(
            transport.Serialized,
            $@"""kind"":""{operationKind}""");
        StringAssert.Contains(
            transport.Serialized,
            @"""expectedDigest"":""sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa""");
    }

    [TestMethod]
    public async Task DeleteRejectsMissingDigestBeforeMutation()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        var rejection = await Assert.ThrowsExactlyAsync<TableMutationPreflightException>(() =>
            gateway.DeleteRowsAsync(
                "orders",
                new[] { ((object)"row-1", "") },
                "schema_0001",
                CancellationToken.None));

        Assert.AreEqual(MutationErrorKind.Validation, MutationErrorMapper.Map(rejection).Kind);
        Assert.IsFalse(transport.Methods.Contains("mutation.apply"));
    }

    [TestMethod]
    public async Task EditSchemaNormalizesJsonAndMultiSelectEditors()
    {
        var transport = new ProductTransport();
        JsonObject metadata = V2Field("metadata", "metadata", "Metadata", "json");
        metadata["json"] = new JsonObject
        {
            ["rootType"] = "object",
            ["maxSize"] = 1024,
            ["schema"] = new JsonObject { ["type"] = "object" },
        };
        JsonObject tagsField = V2Field("tags0000", "tags0000", "Tags", "multiSelect");
        tagsField["select"] = new JsonObject
        {
            ["options"] = new JsonArray(
                new JsonObject
                {
                    ["optionId"] = "opt_aaaaaaaa",
                    ["label"] = "A",
                    ["color"] = "red",
                    ["order"] = 0,
                    ["state"] = "active",
                },
                new JsonObject
                {
                    ["optionId"] = "opt_bbbbbbbb",
                    ["label"] = "B",
                    ["color"] = "blue",
                    ["order"] = 1,
                    ["state"] = "active",
                }),
        };
        string itemSchema = SchemaWithFields("items", metadata, tagsField);
        transport.Respond("schema.getTable", itemSchema);
        transport.Respond("schema.getTable", itemSchema);
        transport.Respond(
            "query.view",
            ViewResponse("""
            {"rows":[],"offset":0,"limit":10,"filteredRows":0,"totalRows":0,
             "snapshot":{"snapshotId":"00000000000000000000000000000000",
             "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
             "databaseId":"local","table":"items","schemaRevision":"schema_0001",
             "dataRevision":1,"normalizedQuery":{"offset":0,"limit":10}}}
            """));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        var schema = await gateway.GetEditSchemaAsync(
            "items", CancellationToken.None);
        var json = schema.Columns.Single(column => column.Name == "f_metadata");
        var tags = schema.Columns.Single(column => column.Name == "f_tags0000");

        Assert.AreEqual("json", json.DataType);
        Assert.AreEqual("json", json.Editor["kind"]);
        Assert.IsNotNull(json.Editor["schema"]);
        Assert.AreEqual("multi_select", tags.Editor["kind"]);
        CollectionAssert.AreEqual(
            new object?[] { "opt_aaaaaaaa", "opt_bbbbbbbb" },
            (object?[])tags.Editor["options"]!);
        var page = await QueryViewAsync(gateway, "items", 1, 10);
        var tagsColumn = page.Columns.Single(column => column.Name == "f_tags0000");
        Assert.AreEqual("multiSelect", tagsColumn.FilterInput);
        CollectionAssert.AreEqual(
            new[] { "opt_aaaaaaaa", "opt_bbbbbbbb" },
            tagsColumn.FilterOptions!.Select(option => option.Value).ToArray());
        Assert.IsNotNull(tagsColumn.EnumOptions);
        CollectionAssert.AreEqual(new[] { "opt_aaaaaaaa", "opt_bbbbbbbb" },
            tagsColumn.EnumOptions.Select(option => option.OptionId).ToArray());
        Assert.AreEqual("blue", tagsColumn.EnumOptions[1].Color);
        Assert.AreEqual("active", tagsColumn.EnumOptions[1].State);
    }

    [TestMethod]
    public async Task AutoNumberIsReadonlyTextAndSupportsEmptyRecordInsertion()
    {
        var transport = new ProductTransport();
        JsonObject number = V2Field("number00", "number00", "Contract number", "autoNumber");
        number["autoNumber"] = new JsonObject
        {
            ["prefix"] = "HT-", ["start"] = 1, ["width"] = 6,
        };
        number["display"]!["kind"] = "readonly";
        transport.Respond("schema.getTable", SchemaWithFields("items", number));
        transport.Respond("mutation.apply", """
            {"contractVersion":"2.0","status":"applied","changeSetId":"change-1",
             "affectedRows":[{"recordId":"numberrow000001","operation":"insert","revision":"row_0002",
             "digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],
             "computedFields":{},"newRevision":"data_0002","emittedEvents":[],"warnings":[]}
            """);
        transport.Respond("query.readRows", """
            {"rows":[{"id":"numberrow000001","f_number00":"HT-000001",
             "__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        EditSchemaResult schema = await gateway.GetEditSchemaAsync("items", CancellationToken.None);
        ColumnEditSchema column = schema.Columns.Single(item => item.Name == "f_number00");
        Assert.AreEqual("autoNumber", column.DataType);
        Assert.IsFalse(column.Editable);
        Assert.IsFalse(schema.Editable);
        InsertRowResult inserted = await gateway.InsertRowAsync("items",
            new Dictionary<string, object?> { ["id"] = "numberrow000001" },
            "schema_0001", CancellationToken.None);
        Assert.AreEqual("HT-000001", inserted.Row["f_number00"]);
        StringAssert.Contains(transport.Serialized, "\"values\":{}");
        await Assert.ThrowsAsync<InvalidOperationException>(() => gateway.InsertRowAsync("items",
            new Dictionary<string, object?> { ["f_number00"] = "HT-999999" },
            "schema_0001", CancellationToken.None));
        Assert.AreEqual(1, transport.Methods.Count(method => method == "mutation.apply"));
    }

    [TestMethod]
    public async Task EditSchemaAcceptsGeoPointWithNullJsonSpecification()
    {
        var transport = new ProductTransport();
        JsonObject title = V2Field("title000", "title000", "Title", "text");
        JsonObject location = V2Field("location0", "location0", "Location", "geoPoint");
        location["json"] = null;
        transport.Respond(
            "schema.getTable",
            SchemaWithFields("items", title, location));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        EditSchemaResult schema = await gateway.GetEditSchemaAsync(
            "items", CancellationToken.None);

        ColumnEditSchema text = schema.Columns.Single(column => column.Name == "f_title000");
        ColumnEditSchema geoPoint = schema.Columns.Single(column => column.Name == "f_location0");
        Assert.IsTrue(text.Editable);
        Assert.AreEqual("json", geoPoint.Editor["kind"]);
        Assert.IsNull(geoPoint.Editor["schema"]);
    }

    [TestMethod]
    public async Task NumberEditorKeepsOnlyRealStorageConstraints()
    {
        var transport = new ProductTransport();
        JsonObject price = V2Field("price0000", "price0000", "Price", "number");
        price["display"]!["displayScale"] = 2;
        price["display"]!["precision"] = "exact";
        price["constraints"]!["range"] = new JsonObject { ["min"] = 0, ["max"] = 1000 };
        transport.Respond("schema.getTable", SchemaWithFields("items", price));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        EditSchemaResult schema = await gateway.GetEditSchemaAsync(
            "items", CancellationToken.None);
        ColumnEditSchema column = schema.Columns.Single(item => item.Name == "f_price0000");

        Assert.AreEqual("number", column.Editor["kind"]);
        Assert.AreEqual("decimal", column.Editor["storage"]);
        // Display-only scale/precision must not leak into the editor: a column
        // shown with 2 decimals still accepts raw input like 1.234567.
        Assert.IsFalse(column.Editor.ContainsKey("scale"));
        Assert.IsFalse(column.Editor.ContainsKey("precision"));
        Assert.AreEqual(0L, column.Editor["minValue"]);
        Assert.AreEqual(1000L, column.Editor["maxValue"]);
    }

    [TestMethod]
    public async Task NumericColumnCarriesCompleteDisplaySpec()
    {
        var transport = new ProductTransport();
        JsonObject price = V2Field("price0000", "price0000", "Price", "number");
        price["display"] = new JsonObject
        {
            ["kind"] = "number",
            ["preset"] = "currency",
            ["displayScale"] = 4,
            ["scaleMode"] = "fixed",
            ["trimTrailingZeros"] = false,
            ["useGrouping"] = true,
            ["currency"] = "CNY",
            ["percentStorage"] = "ratio",
            ["unit"] = null,
            ["precision"] = "exact",
            ["timezone"] = "system",
            ["mode"] = "default",
            ["indent"] = 0,
            ["trueLabel"] = "是",
            ["falseLabel"] = "否",
        };
        transport.Respond("schema.getTable", SchemaWithFields("items", price));
        transport.Respond(
            "query.view",
            ViewResponse("""
            {"rows":[],"offset":0,"limit":100,"filteredRows":0,"totalRows":0,
             "snapshot":{"snapshotId":"00000000000000000000000000000000",
             "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
             "databaseId":"local","table":"items","schemaRevision":"schema_0001",
             "dataRevision":1,"normalizedQuery":{"offset":0,"limit":100}}}
            """));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        var page = await QueryViewAsync(gateway, "items", 0, 100);

        var column = page.Columns.Single(item => item.Name == "f_price0000");
        Assert.IsNotNull(column.Display);
        Assert.AreEqual("currency", column.Display.Preset);
        Assert.AreEqual(4L, column.Display.DisplayScale);
        Assert.AreEqual("fixed", column.Display.ScaleMode);
        Assert.IsFalse(column.Display.TrimTrailingZeros);
        Assert.IsTrue(column.Display.UseGrouping);
        Assert.AreEqual("CNY", column.Display.Currency);
        Assert.AreEqual("ratio", column.Display.PercentStorage);
        Assert.IsNull(column.Display.Unit);
    }

    [TestMethod]
    public async Task FormulaListColumnCarriesDeclaredElementType()
    {
        var transport = new ProductTransport();
        JsonObject numbers = V2Field("numbers0", "numbers0", "Numbers", "formula");
        numbers["formula"] = new JsonObject
        {
            ["language"] = "cel-v2", ["source"] = "UNIQUE([1.0, 2.0, 1.0])",
            ["resultType"] = "json", ["resultElementType"] = "number",
        };
        numbers["storage"]!["kind"] = "computed";
        numbers["display"]!["kind"] = "readonly";
        JsonObject schema = JsonNode.Parse(SchemaWithFields("items", numbers))!.AsObject();
        JsonObject jsonCapability = schema["capabilities"]!.AsArray()[0]!.DeepClone().AsObject();
        jsonCapability["logicalType"] = "json";
        schema["capabilities"]!.AsArray().Add(jsonCapability);
        transport.Respond("schema.getTable", schema.ToJsonString());
        transport.Respond("query.view", ViewResponse("""
            {"rows":[{"id":"row-1","f_numbers0":[1,2]}],
             "offset":0,"limit":100,"filteredRows":1,"totalRows":1,
             "snapshot":{"snapshotId":"00000000000000000000000000000000",
             "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
             "databaseId":"local","table":"items","schemaRevision":"schema_0001",
             "dataRevision":1,"normalizedQuery":{"offset":0,"limit":100}}}
            """));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        var page = await QueryViewAsync(gateway, "items", 0, 100);
        var column = page.Columns.Single(item => item.Name == "f_numbers0");
        Assert.AreEqual("json", column.DataType);
        Assert.IsFalse(column.Editable);
        using var wire = JsonDocument.Parse(JsonSerializer.Serialize(column, new JsonSerializerOptions(JsonSerializerDefaults.Web)));
        Assert.AreEqual("number", wire.RootElement.GetProperty("resultElementType").GetString());
        Assert.AreEqual("[1,2]", JsonSerializer.Serialize(page.Rows[0]["f_numbers0"]));
    }

    [TestMethod]
    public async Task FormulaColumnUsesDeclaredResultTypeInsteadOfNumberStorage()
    {
        var transport = new ProductTransport();
        JsonObject doubled = V2Field("doubled0", "doubled0", "Doubled", "formula");
        doubled["formula"] = new JsonObject
        {
            ["language"] = "cel-v1",
            ["source"] = "quantity * 2",
            ["resultType"] = "number",
        };
        doubled["storage"]!["kind"] = "computed";
        doubled["storage"]!["options"]!["onlyInt"] = true;
        doubled["display"]!["kind"] = "readonly";
        transport.Respond("schema.getTable", SchemaWithFields(
            "items", doubled, V2Field("quantity", "quantity", "Quantity", "number")));
        transport.Respond(
            "query.view",
            ViewResponse("""
            {"rows":[{"id":"row-1","doubled":10}],
             "offset":0,"limit":100,"filteredRows":1,"totalRows":1,
             "snapshot":{"snapshotId":"00000000000000000000000000000000",
             "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
             "databaseId":"local","table":"items","schemaRevision":"schema_0001",
             "dataRevision":1,"normalizedQuery":{"offset":0,"limit":100}}}
            """));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        var page = await QueryViewAsync(gateway, "items", 0, 100);

        var formula = page.Columns.Single(column => column.Name == "f_doubled0");
        Assert.AreEqual("integer", formula.DataType);
        Assert.IsFalse(formula.Editable);
    }

    [TestMethod]
    [DataRow("formula", "number", "decimal", "number", "gt")]
    [DataRow("formula", "dateTime", "datetime", "dateTime", "gt")]
    [DataRow("formula", "bool", "boolean", "boolean", "eq")]
    [DataRow("lookup", "countRecords", "decimal", "number", "gt")]
    [DataRow("lookup", "countNonEmpty", "decimal", "number", "gt")]
    [DataRow("lookup", "countDistinct", "decimal", "number", "gt")]
    [DataRow("lookup", "sum", "decimal", "number", "gt")]
    [DataRow("lookup", "average", "decimal", "number", "gt")]
    [DataRow("lookup", "min", "decimal", "number", "gt")]
    [DataRow("lookup", "max", "decimal", "number", "gt")]
    [DataRow("lookup", "values", "json", "text", "containsAny")]
    [DataRow("lookup", "distinct", "json", "text", "containsAny")]
    public async Task ComputedColumnFiltersUseDeclaredResultCapabilities(
        string kind, string resultOrAggregation, string dataType, string filterInput, string filterOperator)
    {
        JsonObject field = V2Field("computed", "computed", "Computed", kind);
        if (kind == "formula")
        {
            field["formula"] = new JsonObject
            {
                ["language"] = "cel-v1", ["source"] = "0",
                ["resultType"] = resultOrAggregation,
            };
        }
        else
        {
            field["lookup"] = new JsonObject
            {
                ["aggregation"] = resultOrAggregation,
                ["targetFieldId"] = "fld_amount",
                ["path"] = new JsonArray(new JsonObject { ["relationFieldId"] = "fld_source" }),
            };
        }
        field["storage"]!["kind"] = "computed";
        field["storage"]!["options"]!["onlyInt"] = false;
        JsonObject schema = JsonNode.Parse(SchemaWithFields("orders", field))!.AsObject();
        JsonObject template = schema["capabilities"]![0]!.DeepClone().AsObject();
        schema["capabilities"] = new JsonArray(new[] { "formula", "lookup", "number", "dateTime", "bool" }
            .Select(type =>
            {
                JsonObject capability = template.DeepClone().AsObject();
                capability["logicalType"] = type;
                capability["filterOperators"] = type switch
                {
                    "number" or "dateTime" => new JsonArray("eq", "gt", "gte", "lt", "lte"),
                    "lookup" => new JsonArray("containsAny", "containsAll"),
                    _ => new JsonArray("eq", "ne"),
                };
                return (JsonNode)capability;
            }).ToArray());
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", schema.ToJsonString());
        transport.Respond("query.view", ViewResponse(PageResponse("schema_0001", 0, "f_computed")));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));

        TablePage page = await QueryViewAsync(gateway, "orders", 0, 100);
        ColumnSchema column = page.Columns.Single(item => item.Name == "f_computed");

        Assert.AreEqual(dataType, column.DataType);
        Assert.AreEqual(filterInput, column.FilterInput);
        CollectionAssert.Contains(column.FilterOperators!.ToArray(), filterOperator);
        Assert.AreEqual(kind, column.Kind);
        Assert.AreEqual(kind == "lookup" ? "orders.fld_computed" : null, column.LookupId);
        Assert.IsFalse(column.Editable);
        CollectionAssert.AreEqual(new[] { "schema.getTable", "query.view" }, transport.Methods);
    }

    [TestMethod]
    public async Task ViewQueryCarriesGroupsAndParsesFullResultSummaries()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.view",
            """
            {
              "page":{"rows":[{"id":"row-1","title":"Hello"}],
                "offset":0,"limit":1,"filteredRows":12500,"totalRows":25000,
                "snapshot":{"snapshotId":"00000000000000000000000000000000",
                  "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                  "databaseId":"local","table":"orders","schemaRevision":"schema_0001",
                  "dataRevision":1,"normalizedQuery":{"offset":0,"limit":1}}},
              "groupRows":[{"key":["east","open"],"count":3000,"summaries":[5000],
                "parentCount":7000,"parentSummaries":[12345]}],
              "groupOffset":0,"groupLimit":100,"hasMoreGroups":false
            }
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        var page = await gateway.QueryTableViewRawAsync(
            "orders",
            JsonSerializer.SerializeToElement(new
            {
                keyword = "",
                filters = Array.Empty<object>(),
                sorts = Array.Empty<object>(),
                offset = 0,
                limit = 1,
                groups = new[] { new { field = "title" } },
                summaries = new[] { new { field = "amount", function = "sum" } },
                groupOffset = 0,
                groupLimit = 100,
            }),
            CancellationToken.None);

        Assert.AreEqual(12500, page.FilteredRows);
        Assert.AreEqual("east", page.GroupRows![0].Key[0]);
        Assert.AreEqual(3000L, page.GroupRows[0].Count);
        Assert.AreEqual(5000L, page.GroupRows[0].Summaries[0]);
        Assert.AreEqual(7000L, page.GroupRows[0].ParentCount);
        Assert.AreEqual(12345L, page.GroupRows[0].ParentSummaries![0]);
        CollectionAssert.AreEqual(
            new[] { "schema.getTable", "query.view" },
            transport.Methods);
        StringAssert.Contains(transport.Serialized, "\"groups\"");
        StringAssert.Contains(transport.Serialized, "\"summaries\"");
    }

    [TestMethod]
    public async Task RawViewQueryPreservesUnknownAstValuesForSidecarValidation()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.view",
            """
            {
              "page":{"rows":[],"offset":0,"limit":50,"filteredRows":0,"totalRows":0,
                "snapshot":{"snapshotId":"00000000000000000000000000000000",
                  "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                  "databaseId":"local","table":"orders","schemaRevision":"schema_0001",
                  "dataRevision":1,"normalizedQuery":{"offset":0,"limit":50}}},
              "groupRows":[],"groupOffset":0,"groupLimit":100,"hasMoreGroups":false
            }
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));
        using var query = JsonDocument.Parse(
            """{"filters":[{"field":"title","operator":"raw_sql","value":{"x":1}}],"sorts":[],"offset":0,"limit":50,"groups":[],"summaries":[],"groupOffset":0,"groupLimit":100}""");

        await gateway.QueryTableViewRawAsync(
            "orders", query.RootElement, CancellationToken.None);

        StringAssert.Contains(transport.Serialized, "\"operator\":\"raw_sql\"");
        StringAssert.Contains(transport.Serialized, "\"value\":{\"x\":1}");
    }

    [TestMethod]
    public async Task ActiveCursorOpenUsesAtomicProjectionAndContinuesWithOpaqueToken()
    {
        var transport = new ProductTransport();
        transport.Respond(
            "query.selectionOpen",
            SelectionProjectionJson("schema_0001", 1, "row-1", "opaque-2", true));
        transport.Respond("query.cursorFetch", CursorWindow("row-2", null, false));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));
        using var query = JsonDocument.Parse(
            """{"filters":[{"field":"title","operator":"raw_sql","value":{"x":1}}],"sorts":[],"limit":500,"groups":[{"field":"title"}],"summaries":[]}""");

        TablePage first = await gateway.OpenTableCursorRawAsync(
            "orders", query.RootElement, CancellationToken.None);
        TablePage second = await gateway.FetchTableCursorAsync(
            first.NextCursor!, CancellationToken.None);

        Assert.AreEqual("opaque-2", first.NextCursor);
        Assert.IsTrue(first.HasMore);
        Assert.AreEqual("row-2", second.Rows[0]["rowKey"]);
        Assert.IsFalse(second.HasMore);
        CollectionAssert.AreEqual(
            new[] { "query.selectionOpen", "query.cursorFetch" },
            transport.Methods);
        StringAssert.Contains(transport.Serialized, "\"operator\":\"raw_sql\"");
        StringAssert.Contains(transport.Serialized, "\"cursor\":\"opaque-2\"");
        Assert.IsFalse(transport.Serialized.Contains("\"groups\"", StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task OlderLateSelectionCannotDowngradeTheSchemaCache()
    {
        var transport = new ProductTransport();
        var releaseOld = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        transport.RespondAfter(
            "query.selectionOpen",
            SelectionProjectionJson("schema_0001", 1, "row-old", null, false),
            releaseOld.Task);
        transport.Respond(
            "query.selectionOpen",
            SelectionProjectionJson("schema_0002", 2, "row-new", null, false));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));
        using var query = JsonDocument.Parse("""{"filters":[],"sorts":[],"limit":500}""");

        Task<TableSelectionProjection> old = gateway.OpenTableSelectionAsync(
            "orders", query.RootElement, CancellationToken.None);
        await transport.WaitForMethodCountAsync(1);
        TableSelectionProjection newer = await gateway.OpenTableSelectionAsync(
            "orders", query.RootElement, CancellationToken.None);
        releaseOld.SetResult();
        TableSelectionProjection older = await old;
        EditSchemaResult cached = await gateway.GetEditSchemaAsync(
            "orders", CancellationToken.None);

        Assert.AreEqual("schema_0002", newer.EditSchema.SchemaRevision);
        Assert.AreEqual("schema_0001", older.EditSchema.SchemaRevision);
        Assert.AreEqual("schema_0002", cached.SchemaRevision);
        CollectionAssert.AreEqual(
            new[] { "query.selectionOpen", "query.selectionOpen" },
            transport.Methods);
    }

    [TestMethod]
    public async Task OlderLateSchemaReadCannotDowngradeSelectionSchemaCache()
    {
        var transport = new ProductTransport();
        var releaseOldSchema = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        transport.RespondAfter(
            "schema.getTable",
            Schema("orders"),
            releaseOldSchema.Task);
        transport.Respond(
            "query.selectionOpen",
            SelectionProjectionJson("schema_0002", 2, "row-new", null, false));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));
        using var query = JsonDocument.Parse("""{"filters":[],"sorts":[],"limit":500}""");

        Task<EditSchemaResult> oldRead = gateway.GetEditSchemaAsync(
            "orders", CancellationToken.None);
        await transport.WaitForMethodCountAsync(1);
        TableSelectionProjection newer = await gateway.OpenTableSelectionAsync(
            "orders", query.RootElement, CancellationToken.None);
        releaseOldSchema.SetResult();
        EditSchemaResult completedOldRead = await oldRead;
        EditSchemaResult cached = await gateway.GetEditSchemaAsync(
            "orders", CancellationToken.None);

        Assert.AreEqual("schema_0002", newer.EditSchema.SchemaRevision);
        Assert.AreEqual("schema_0002", completedOldRead.SchemaRevision);
        Assert.AreEqual("schema_0002", cached.SchemaRevision);
        CollectionAssert.AreEqual(
            new[] { "schema.getTable", "query.selectionOpen" },
            transport.Methods);
    }

    [TestMethod]
    public async Task SelectionProjectionUsesOneRevisionMatchedProductRpc()
    {
        var transport = new ProductTransport();
        transport.Respond(
            "query.selectionOpen",
            JsonSerializer.Serialize(new
            {
                schemaSnapshot = JsonNode.Parse(Schema("orders")),
                cursorWindow = JsonNode.Parse(CursorWindow("row-1", "opaque-2", true)),
            }));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));
        using var query = JsonDocument.Parse(
            """{"filters":[],"sorts":[],"limit":500,"groups":[],"summaries":[]}""");

        TableSelectionProjection projection = await gateway.OpenTableSelectionAsync(
            "orders", query.RootElement, CancellationToken.None);

        Assert.AreEqual("schema_0001", projection.Page.QuerySnapshot!.SchemaRevision);
        Assert.AreEqual("schema_0001", projection.EditSchema.SchemaRevision);
        Assert.AreEqual("row-1", projection.Page.Rows[0]["rowKey"]);
        CollectionAssert.AreEqual(new[] { "query.selectionOpen" }, transport.Methods);
    }

    [TestMethod]
    public async Task SelectionProjectionRejectsMismatchedDataRevisionWithoutRetry()
    {
        var transport = new ProductTransport();
        string mismatchedWindow = CursorWindow("row-1", null, false)
            .Replace("\"dataRevision\":1", "\"dataRevision\":2", StringComparison.Ordinal);
        transport.Respond(
            "query.selectionOpen",
            JsonSerializer.Serialize(new
            {
                schemaSnapshot = JsonNode.Parse(Schema("orders")),
                cursorWindow = JsonNode.Parse(mismatchedWindow),
            }));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));
        using var query = JsonDocument.Parse(
            """{"filters":[],"sorts":[],"limit":500}""");

        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.OpenTableSelectionAsync(
                "orders", query.RootElement, CancellationToken.None));

        CollectionAssert.AreEqual(new[] { "query.selectionOpen" }, transport.Methods);
    }

    [TestMethod]
    public async Task CursorStaleProductCodeSurvivesTheDesktopErrorMapper()
    {
        var transport = new ProductTransport();
        transport.RespondError(
            "query.cursorFetch",
            -32150,
            "Product data error",
            """{"kind":"product_data_error","message":"cursor changed","code":"query.cursor_stale","path":"cursor","details":{},"retryable":false}""");
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        RpcRemoteException exception = await Assert.ThrowsExactlyAsync<RpcRemoteException>(
            () => gateway.FetchTableCursorAsync("opaque", CancellationToken.None));
        MutationError mapped = MutationErrorMapper.Map(exception);

        Assert.AreEqual("query.cursor_stale", mapped.Code);
        Assert.AreEqual("cursor changed", mapped.Message);
    }

    [TestMethod]
    public async Task VolatileCursorSnapshotCarriesClockPeriodThroughValidation()
    {
        var transport = new ProductTransport();
        transport.Respond("query.cursorFetch", CursorWindow("row-1", null, false)
            .Replace(
                "\"normalizedQuery\":",
                "\"clockPeriod\":\"2026-12-01T00:00:00Z\",\"normalizedQuery\":",
                StringComparison.Ordinal));
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond("query.validateSnapshot", """
            {"valid":true,"currentDataRevision":1,"currentSchemaRevision":"schema_0001"}
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        TablePage page = await gateway.FetchTableCursorAsync(
            "opaque", CancellationToken.None);

        Assert.AreEqual("2026-12-01T00:00:00Z", page.QuerySnapshot!.OptionalClockPeriod);
        SnapshotValidation validation = await gateway.ValidateSnapshotAsync(
            page.QuerySnapshot, null, CancellationToken.None);

        Assert.IsTrue(validation.Valid);
        StringAssert.Contains(
            transport.Serialized,
            "\"clockPeriod\":\"2026-12-01T00:00:00Z\"");
    }

    [TestMethod]
    public async Task OrdinaryCursorSnapshotOmitsClockPeriodOnTheWire()
    {
        var transport = new ProductTransport();
        transport.Respond("query.cursorFetch", CursorWindow("row-1", null, false));
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond("query.validateSnapshot", """
            {"valid":true,"currentDataRevision":1,"currentSchemaRevision":"schema_0001"}
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client));

        TablePage page = await gateway.FetchTableCursorAsync(
            "opaque", CancellationToken.None);

        Assert.IsNull(page.QuerySnapshot!.OptionalClockPeriod);
        await gateway.ValidateSnapshotAsync(page.QuerySnapshot, null, CancellationToken.None);

        Assert.IsFalse(transport.Serialized.Contains(
            "clockPeriod",
            StringComparison.Ordinal));
    }

    private static string CursorWindow(string rowId, string? nextCursor, bool hasMore)
        => JsonSerializer.Serialize(new
        {
            rows = new[] { new { id = rowId, title = "Hello" } },
            filteredRows = 50_000,
            totalRows = 50_000,
            querySnapshot = new
            {
                snapshotId = "00000000000000000000000000000000",
                digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                databaseId = "local",
                table = "orders",
                schemaRevision = "schema_0001",
                dataRevision = 1,
                normalizedQuery = new { offset = 0, limit = 500 },
            },
            nextCursor,
            hasMore,
        });

    private static string SelectionProjectionJson(
        string schemaRevision,
        int dataRevision,
        string rowId,
        string? nextCursor,
        bool hasMore)
    {
        JsonObject schema = JsonNode.Parse(Schema("orders"))!.AsObject();
        schema["schemaRevision"] = schemaRevision;
        schema["dataRevision"] = dataRevision;
        JsonObject cursor = JsonNode.Parse(CursorWindow(rowId, nextCursor, hasMore))!.AsObject();
        JsonObject snapshot = cursor["querySnapshot"]!.AsObject();
        snapshot["schemaRevision"] = schemaRevision;
        snapshot["dataRevision"] = dataRevision;
        return new JsonObject
        {
            ["schemaSnapshot"] = schema,
            ["cursorWindow"] = cursor,
        }.ToJsonString();
    }

    private static Task<TablePage> QueryViewAsync(
        PocketBaseTableGateway gateway,
        string table,
        int offset,
        int limit)
        => gateway.QueryTableViewRawAsync(
            table,
            JsonSerializer.SerializeToElement(new
            {
                keyword = "",
                filters = Array.Empty<object>(),
                sorts = Array.Empty<object>(),
                offset,
                limit,
                groups = Array.Empty<object>(),
                summaries = Array.Empty<object>(),
                groupOffset = 0,
                groupLimit = 100,
            }),
            CancellationToken.None);

    private static string ViewResponse(string page)
        => $"{{\"page\":{page},\"groupRows\":[],\"groupOffset\":0," +
            "\"groupLimit\":100,\"hasMoreGroups\":false}";

    private static string Schema(string table)
    {
        string fixture = File.ReadAllText(ProductCatalogFixturePath());
        JsonObject catalog = JsonNode.Parse(fixture)!.AsObject();
        JsonObject rpcCase = catalog["rpcCases"]!.AsArray()
            .Select(item => item!.AsObject())
            .Single(item => item["method"]!.GetValue<string>() == "schema.getTable");
        JsonObject schema = rpcCase["success"]!["result"]!.DeepClone().AsObject();
        schema["tableId"] = table;
        schema["displayName"] = "Orders";
        schema["schemaRevision"] = "schema_0001";
        JsonObject field = schema["fields"]!.AsArray()[0]!.AsObject();
        JsonObject identity = field["identity"]!.AsObject();
        identity["fieldId"] = "fld_title001";
        identity["physicalName"] = "title";
        identity["providerFieldId"] = "pb_title001";
        field["displayName"] = "Title";
        field["logicalType"] = "text";
        schema["capabilities"]!.AsArray()[0]!["filterOperators"] =
            new JsonArray("eq", "is_null");
        return schema.ToJsonString();
    }

    private static JsonObject V2Field(
        string fieldId,
        string physicalName,
        string displayName,
        string logicalType)
    {
        JsonObject schema = JsonNode.Parse(Schema("fixture"))!.AsObject();
        JsonObject field = schema["fields"]!.AsArray()[0]!.DeepClone().AsObject();
        JsonObject identity = field["identity"]!.AsObject();
        identity["fieldId"] = $"fld_{fieldId}";
        identity["physicalName"] = $"f_{physicalName}";
        identity["providerFieldId"] = $"pb_{fieldId}";
        field["displayName"] = displayName;
        field["logicalType"] = logicalType;
        return field;
    }

    private static string SchemaWithFields(string table, params JsonObject[] fields)
    {
        JsonObject schema = JsonNode.Parse(Schema(table))!.AsObject();
        schema["fields"] = new JsonArray(
            fields.Select(field => field.DeepClone()).ToArray());
        JsonObject capabilityTemplate = schema["capabilities"]!.AsArray()[0]!.AsObject();
        schema["capabilities"] = new JsonArray(fields
            .Select(field =>
            {
                JsonObject capability = capabilityTemplate.DeepClone().AsObject();
                capability["logicalType"] = field["logicalType"]!.GetValue<string>();
                capability["filterOperators"] = new JsonArray("eq", "is_null");
                return (JsonNode)capability;
            })
            .ToArray());
        return schema.ToJsonString();
    }

    private static string ProductCatalogFixturePath()
    {
        DirectoryInfo? directory = new(AppContext.BaseDirectory);
        while (directory is not null)
        {
            string candidate = Path.Combine(
                directory.FullName,
                "contracts",
                "v2",
                "fixtures",
                "product-rpc-catalog.json");
            if (File.Exists(candidate))
            {
                return candidate;
            }
            directory = directory.Parent;
        }

        throw new FileNotFoundException(
            "Could not locate contracts/v2/fixtures/product-rpc-catalog.json.");
    }

    private static string RelationalSchema(string table)
    {
        JsonObject relation = V2Field(
            "customer", "customer", "Customer", "relation");
        relation["relation"] = new JsonObject
        {
            ["targetTableId"] = "customers",
            ["cardinality"] = "one",
            ["deletePolicy"] = "setNull",
            ["displayFieldId"] = "fld_name0001",
        };
        JsonObject lookup = V2Field(
            "customer_name", "customer_name", "Customer name", "lookup");
        lookup["lookup"] = new JsonObject
        {
            ["path"] = new JsonArray(new JsonObject
            {
                ["relationFieldId"] = "fld_customer",
            }),
            ["targetFieldId"] = "fld_name0001",
        };
        return SchemaWithFields(table, relation, lookup);
    }

    private static string PageResponse(
        string schemaRevision,
        int offset,
        string field) =>
        """
        {"rows":[{"id":"row-1","__FIELD__":"value",
        "__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],
        "offset":__OFFSET__,"limit":100,"filteredRows":1,"totalRows":201,
        "snapshot":{"snapshotId":"00000000000000000000000000000000",
        "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "databaseId":"local","table":"orders","schemaRevision":"__SCHEMA__",
        "dataRevision":1,"normalizedQuery":{"offset":__OFFSET__,"limit":100}}}
        """
        .Replace("__FIELD__", field, StringComparison.Ordinal)
        .Replace(
            "__OFFSET__",
            offset.ToString(System.Globalization.CultureInfo.InvariantCulture),
            StringComparison.Ordinal)
        .Replace("__SCHEMA__", schemaRevision, StringComparison.Ordinal);

    [TestMethod]
    [DoNotParallelize]
    [DataRow("preflight", "RPC:-32150:schema.table.not_found", "schema.table.not_found")]
    [DataRow("preflight", "RPC:-32150", "schema.table.not_found\nprivate-user-value-path-password")]
    [DataRow("guard", "TableEditConflictException", null)]
    [DataRow("apply", "RPC:-32150:mutation.digest_conflict", null)]
    [DataRow("readback", "RPC:-32150:query.storage.failed", "query.storage.failed")]
    [DataRow("readback", "RPC:-32150", "query.private-user-value-path-password")]
    [DataRow("receipt", "InvalidOperationException", null)]
    public async Task UpdateFailureRecordsOnlyStageAndSafeCode(
        string stage,
        string expectedCode,
        string? productCode)
    {
        var transport = new ProductTransport();
        const string privateText = "private-user-value-path-password";
        const string before = """
            {"rows":[{"id":"row-1","title":"Before",
             "__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}
            """;
        if (stage == "preflight")
        {
            transport.RespondError("schema.getTable", -32150, privateText,
                JsonSerializer.Serialize(new { code = productCode, message = privateText }));
        }
        else
        {
            transport.Respond("schema.getTable", Schema("orders"));
            transport.Respond("query.readRows", stage == "guard"
                ? before.Replace("Before", "Changed", StringComparison.Ordinal) : before);
            if (stage == "apply")
            {
                transport.RespondError("mutation.apply", -32150, privateText,
                    """{"code":"mutation.digest_conflict","details":{"path":"private-user-value-path-password"}}""");
            }
            else if (stage is "readback" or "receipt")
            {
                transport.Respond("mutation.apply", """
                    {"newRevision":"invalid","affectedRows":[{"recordId":"row-1","operation":"update",
                     "digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}
                    """);
                if (stage == "readback")
                    transport.RespondError("query.readRows", -32150, privateText,
                        JsonSerializer.Serialize(new { code = productCode, details = new { path = privateText } }));
                else
                    transport.Respond("query.readRows", before);
            }
        }
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        var service = new TableWorkspaceService(gateway);
        var notifications = new List<TableNotification>();
        service.Notification += notifications.Add;
        using var listener = new EditDiagnosticListener();
        Trace.Listeners.Add(listener);
        try
        {
            Assert.IsFalse(await service.UpdateCellAsync(
                "orders", "row-1", "title", "Before", privateText, "schema_0001",
                requestId: privateText));
        }
        finally
        {
            Trace.Listeners.Remove(listener);
        }

        Assert.HasCount(1, listener.Lines);
        string line = listener.Lines[0];
        Assert.IsTrue(DiagnosticLogLine.IsSafe(line));
        using JsonDocument diagnostic = JsonDocument.Parse(line);
        Assert.AreEqual("table.updateCell." + stage,
            diagnostic.RootElement.GetProperty("event").GetString());
        Assert.AreEqual(expectedCode, diagnostic.RootElement.GetProperty("errorCode").GetString());
        Assert.AreEqual(JsonValueKind.Null, diagnostic.RootElement.GetProperty("requestId").ValueKind);
        Assert.IsFalse(line.Contains(privateText, StringComparison.Ordinal));
        Assert.IsFalse(line.Contains("orders", StringComparison.Ordinal));
        Assert.IsFalse(line.Contains("row-1", StringComparison.Ordinal));
        Assert.IsFalse(line.Contains("Before", StringComparison.Ordinal));
        Assert.HasCount(1, notifications);
        Assert.AreEqual("table.editRejected", notifications[0].Type);
        Assert.AreEqual(privateText, notifications[0].RequestId);
        Assert.AreEqual(stage == "guard" ? MutationErrorKind.EditConflict : MutationErrorKind.Unknown,
            notifications[0].MutationResult!.Error!.Value.Kind);
        Assert.AreEqual(stage is "preflight" or "guard" ? 0 : 1,
            transport.Methods.Count(method => method == "mutation.apply"));
    }

    [TestMethod]
    [DoNotParallelize]
    public async Task UpdateDiagnosticSinkFailureDoesNotReplaceTheOriginalFailure()
    {
        var transport = new ProductTransport();
        transport.RespondError("schema.getTable", -32150, "original failure", "{}");
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        using var listener = new EditDiagnosticListener { ThrowOnWrite = true };
        Trace.Listeners.Add(listener);
        try
        {
            TableMutationPreflightException failure = await Assert.ThrowsExactlyAsync<TableMutationPreflightException>(() =>
                gateway.UpdateCellAsync("orders", "row-1", "title", "Before", "After",
                    "schema_0001", CancellationToken.None));
            var error = (RpcRemoteException)failure.InnerException!;
            Assert.IsTrue(MutationErrorMapper.Map(failure).NotSubmitted);
            Assert.AreEqual(-32150, error.Code);
            Assert.AreEqual("original failure", error.Message);
        }
        finally
        {
            Trace.Listeners.Remove(listener);
        }
        Assert.HasCount(1, listener.Lines);
    }

    [TestMethod]
    [DoNotParallelize]
    [DataRow("mutation.field.invalid_value", true)]
    [DataRow("mutation.formula.failed", true)]
    [DataRow("mutation.table.read_only", true)]
    [DataRow("mutation.lookup.storage_failed", true)]
    [DataRow("lookup.aggregation.value_not_numeric", true)]
    [DataRow("formula.divide_by_zero", true)]
    [DataRow("field.migration.write_locked", true)]
    [DataRow("mutation.future_code", false)]
    [DataRow("mutation.field.invalid_value:private-user-value-path-password", false)]
    [DataRow("formula.author.token", false)]
    [DataRow("mutation.record.already_exists", false)]
    [DataRow("field.migration.write_locked\nprivate-user-value-path-password", false)]
    public async Task UpdateFailureRetainsOnlyFixedCodesFromTheUpdatePath(
        string productCode,
        bool persisted)
    {
        const string privateText = "private-user-value-path-password";
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond("query.readRows", """
            {"rows":[{"id":"row-1","title":"Before",
             "__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}
            """);
        string errorData = JsonSerializer.Serialize(new
        {
            code = productCode,
            message = privateText,
            details = new { path = privateText, accessToken = privateText },
        });
        transport.RespondError("mutation.apply", -32150, privateText, errorData);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(new JsonRpcProductDataGateway(client));
        using var listener = new EditDiagnosticListener();
        Trace.Listeners.Add(listener);
        try
        {
            RpcRemoteException error = await Assert.ThrowsExactlyAsync<RpcRemoteException>(() =>
                gateway.UpdateCellAsync("orders", "row-1", "title", "Before", privateText,
                    "schema_0001", CancellationToken.None));
            Assert.AreEqual(privateText, error.Message);
            Assert.AreEqual(errorData, error.ErrorData!.Value.GetRawText());
        }
        finally
        {
            Trace.Listeners.Remove(listener);
        }

        Assert.HasCount(1, listener.Lines);
        string line = listener.Lines[0];
        Assert.IsTrue(DiagnosticLogLine.IsSafe(line));
        using JsonDocument diagnostic = JsonDocument.Parse(line);
        Assert.AreEqual("table.updateCell.apply",
            diagnostic.RootElement.GetProperty("event").GetString());
        Assert.AreEqual(persisted ? "RPC:-32150:" + productCode : "RPC:-32150",
            diagnostic.RootElement.GetProperty("errorCode").GetString());
        Assert.IsFalse(line.Contains(privateText, StringComparison.Ordinal));
    }
    private sealed class EditDiagnosticListener : TraceListener
    {
        public List<string> Lines { get; } = [];
        public bool ThrowOnWrite { get; init; }
        public override void Write(string? message) { }
        public override void WriteLine(string? message)
        {
            if (message is null || !message.Contains(
                    "VibeTable.Desktop.PocketBaseTableGateway", StringComparison.Ordinal)) return;
            Lines.Add(message);
            if (ThrowOnWrite) throw new IOException("diagnostic sink failure");
        }
    }

    private sealed class ProductTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming =
            Channel.CreateUnbounded<JsonElement?>();
        private readonly Dictionary<string, Queue<(string Json, bool IsError, Task? Gate)>> _responses =
            new(StringComparer.Ordinal);
        private TaskCompletionSource _methodObserved = NewMethodObserved();

        public List<string> Methods { get; } = [];
        public string Serialized { get; private set; } = "";

        public void Respond(string method, string result)
            => RespondAfter(method, result, Task.CompletedTask);

        public void RespondAfter(string method, string result, Task gate)
        {
            if (!_responses.TryGetValue(
                    method,
                    out Queue<(string Json, bool IsError, Task? Gate)>? queue))
            {
                queue = new Queue<(string Json, bool IsError, Task? Gate)>();
                _responses[method] = queue;
            }
            queue.Enqueue((result, false, gate));
        }

        public void RespondError(string method, int code, string message, string data)
        {
            if (!_responses.TryGetValue(
                    method,
                    out Queue<(string Json, bool IsError, Task? Gate)>? queue))
            {
                queue = new Queue<(string Json, bool IsError, Task? Gate)>();
                _responses[method] = queue;
            }
            queue.Enqueue((
                JsonSerializer.Serialize(new
                {
                    code,
                    message,
                    data = JsonDocument.Parse(data).RootElement.Clone(),
                }),
                true,
                null));
        }

        public async Task WaitForMethodCountAsync(int count)
        {
            while (Methods.Count < count)
            {
                Task observed = _methodObserved.Task;
                if (Methods.Count < count)
                {
                    await observed.WaitAsync(TimeSpan.FromSeconds(2));
                }
            }
        }

        public void CloseReadLoop() => _incoming.Writer.TryComplete();

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            Serialized += line;
            using var request = JsonDocument.Parse(line);
            string id = request.RootElement.GetProperty("id").GetString()!;
            string method = request.RootElement.GetProperty("method").GetString()!;
            Methods.Add(method);
            TaskCompletionSource observed = _methodObserved;
            _methodObserved = NewMethodObserved();
            observed.TrySetResult();
            if (!_responses.TryGetValue(
                    method,
                    out Queue<(string Json, bool IsError, Task? Gate)>? queue)
                || queue.Count == 0)
            {
                throw new InvalidOperationException($"No response for {method}.");
            }
            (string json, bool isError, Task? gate) = queue.Dequeue();
            using var result = JsonDocument.Parse(json);
            JsonElement response = isError
                ? JsonSerializer.SerializeToElement(new
                {
                    jsonrpc = "2.0",
                    id,
                    error = result.RootElement.Clone(),
                })
                : JsonSerializer.SerializeToElement(new
                {
                    jsonrpc = "2.0",
                    id,
                    result = result.RootElement.Clone(),
                });
            if (gate is null || gate.IsCompletedSuccessfully)
            {
                _incoming.Writer.TryWrite(response);
            }
            else
            {
                _ = CompleteAfterAsync(gate, response);
            }
            return Task.CompletedTask;
        }

        private async Task CompleteAfterAsync(Task gate, JsonElement response)
        {
            await gate.ConfigureAwait(false);
            _incoming.Writer.TryWrite(response);
        }

        private static TaskCompletionSource NewMethodObserved()
            => new(TaskCreationOptions.RunContinuationsAsynchronously);

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }
}
