using System.Net;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class HostProductRpcInvokerTests
{
    [TestMethod]
    public async Task TypedHostSchemaReadUsesPolicySelectedSidecarOnly()
    {
        await using var fixture = await HostFixture.OpenAsync();
        using JsonRpcProductDataGateway gateway = fixture.Gateway();

        JsonElement result = await gateway.ListTablesAsync(Json("{}"), CancellationToken.None);

        Assert.AreEqual("表格", result.GetProperty("tables")[0].GetString());
        Assert.AreEqual(0, fixture.Python.WriteCount);
        Assert.AreEqual(1, fixture.Http.Handshakes);
        JsonElement wire = fixture.Http.Calls.Single().GetProperty("wire");
        Assert.AreEqual(fixture.Session.WorkspaceId!.Value.ToString("D"),
            wire.GetProperty("workspaceId").GetString());
        Assert.AreEqual(fixture.Session.SessionEpoch, wire.GetProperty("sessionEpoch").GetUInt64());
        Assert.IsTrue(Guid.TryParse(wire.GetProperty("operationId").GetString(), out _));
        Assert.AreEqual(1UL, wire.GetProperty("sequence").GetUInt64());
        Assert.IsFalse(wire.TryGetProperty("fenceEpoch", out _));
    }

    [TestMethod]
    public async Task TypedHostFileListUsesPolicySelectedSidecarOnly()
    {
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.Result = Json("""
            {"attachments":[{"contractVersion":"2.0","tableId":"订单","recordId":"r-1","fieldId":"附件","storedName":"stored.pdf","originalName":"发票.pdf","mimeType":"application/pdf","size":7,"sha256":"existing-authoritative-hash","downloadCapability":"download-capability","thumbnails":[]}]}
            """);
        using JsonRpcProductDataGateway gateway = fixture.Gateway();

        JsonElement result = await gateway.ListAttachmentRefsAsync(
            Json("""{"tableId":"订单","recordId":"r-1","fieldId":"附件"}"""), CancellationToken.None);

        JsonElement attachment = result.GetProperty("attachments")[0];
        Assert.AreEqual("发票.pdf", attachment.GetProperty("originalName").GetString());
        Assert.AreEqual("download-capability", attachment.GetProperty("downloadCapability").GetString());
        Assert.AreEqual(0, attachment.GetProperty("thumbnails").GetArrayLength());
        Assert.AreEqual(0, fixture.Python.WriteCount);
        Assert.AreEqual("file.list", fixture.Http.Calls.Single().GetProperty("method").GetString());
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task WorkspaceCloseRetiresDebouncedGridQueryWithoutFailure(bool correlated)
    {
        await using var fixture = await HostFixture.OpenAsync();
        using PocketBaseTableGateway tableGateway = fixture.TableGateway();
        var time = new ManualTimeProvider();
        var sink = new FakeWebReplySink();
        var coordinator = new GridStateCoordinator(
            tableGateway, notification => TableNotificationPresenter.Post(sink, notification), time);
        var controller = fixture.GridController(coordinator, sink);
        var scope = new WorkspaceWireScope
        {
            Scope = "workspace",
            WorkspaceId = fixture.Session.WorkspaceId!.Value,
            SessionEpoch = fixture.Session.SessionEpoch,
            OperationId = Guid.NewGuid(),
            Sequence = 1,
        };
        Task query = controller.DispatchAsync(new RoutedWebRequest(
            "table.queryRequested", correlated ? "diagnostic-query" : null,
            Json("""{"table":"orders","query":{}}"""), "", scope));
        Assert.AreEqual(0, fixture.Http.Calls.Count);
        await fixture.CloseAsync().WaitAsync(TimeSpan.FromSeconds(3));
        time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));
        await query.WaitAsync(TimeSpan.FromSeconds(3));
        Assert.AreEqual(0, fixture.Http.Handshakes);
        Assert.AreEqual(0, fixture.Http.Calls.Count);
        Assert.AreEqual(0, fixture.Python.WriteCount);
        var failures = sink.Replies.Where(reply => reply.Type == "operation.failed").ToList();
        string evidence = string.Join(", ", failures.Select(reply =>
        {
            JsonElement payload = JsonSerializer.SerializeToElement(reply.Payload);
            return $"requestId={reply.RequestId ?? "null"}; " +
                $"operation={payload.GetProperty("operation").GetString()}; " +
                $"messageLength={payload.GetProperty("message").GetString()?.Length}; " +
                $"code={payload.GetProperty("code").GetString() ?? "null"}";
        }));
        Assert.AreEqual(0, failures.Count, evidence);
    }
    [TestMethod]
    [DataRow(false, false)]
    [DataRow(false, true)]
    [DataRow(true, false)]
    [DataRow(true, true)]
    public async Task CurrentGridReadsKeepSuccessAndFailureNotifications(bool cursor, bool fails)
    {
        await using var fixture = await HostFixture.OpenAsync();
        var time = new ManualTimeProvider();
        var sink = new FakeWebReplySink();
        var page = GridPage();
        var gateway = new FakeTableRpcGateway();
        gateway.CursorOpenResults["orders"] = page;
        var coordinator = new GridStateCoordinator(gateway,
            notification => TableNotificationPresenter.Post(sink, notification), time);
        // Establish the active query needed by cursor appends before observing the request.
        Task<TablePage> initial = coordinator.RequestQueryAsync("orders", Json("{}"), default);
        time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));
        await initial;
        gateway.CursorOpenOverride = (_, _, _) => fails
            ? Task.FromException<TablePage>(new InvalidOperationException("current read failed"))
            : Task.FromResult(page);
        gateway.CursorFetchOverride = (_, _) => fails
            ? Task.FromException<TablePage>(new InvalidOperationException("current read failed"))
            : Task.FromResult(page);
        var controller = fixture.GridController(coordinator, sink);
        Task request = controller.DispatchAsync(GridRequest(fixture.Session, cursor, 1));
        if (!cursor) time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));
        await request.WaitAsync(TimeSpan.FromSeconds(3));

        FakeWebReplySink.Reply reply = sink.Replies.Single();
        Assert.IsNull(reply.RequestId);
        JsonElement payload = JsonSerializer.SerializeToElement(reply.Payload,
            new JsonSerializerOptions(JsonSerializerDefaults.Web));
        if (fails)
        {
            Assert.AreEqual("operation.failed", reply.Type);
            Assert.AreEqual(cursor ? "query.cursor" : "query", payload.GetProperty("operation").GetString());
            Assert.AreEqual("current read failed", payload.GetProperty("message").GetString());
        }
        else
        {
            Assert.AreEqual(cursor ? "table.windowLoaded" : "table.datasetReady", reply.Type);
            Assert.AreEqual("orders", payload.GetProperty("table").GetString());
            Assert.AreEqual("cursor-next", payload.GetProperty("nextCursor").GetString());
            Assert.IsTrue(payload.GetProperty("hasMore").GetBoolean());
            Assert.AreEqual(7, payload.GetProperty("querySnapshot").GetProperty("dataRevision").GetInt32());
        }
    }

    [TestMethod]
    [DataRow(false, false)]
    [DataRow(false, true)]
    [DataRow(true, false)]
    [DataRow(true, true)]
    public async Task RetiredGridReadsCannotPublishLateFailureOrPage(bool cursor, bool close)
    {
        await using var fixture = await HostFixture.OpenAsync();
        var time = new ManualTimeProvider();
        var sink = new FakeWebReplySink();
        var page = GridPage();
        var gateway = new FakeTableRpcGateway();
        gateway.CursorOpenResults["orders"] = page;
        var coordinator = new GridStateCoordinator(gateway,
            notification => TableNotificationPresenter.Post(sink, notification), time);
        Task<TablePage> initial = coordinator.RequestQueryAsync("orders", Json("{}"), default);
        time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));
        await initial;
        var pending = new TaskCompletionSource<TablePage>();
        CancellationToken readToken = default;
        gateway.CursorOpenOverride = (_, _, token) => { readToken = token; return pending.Task; };
        gateway.CursorFetchOverride = (_, token) => { readToken = token; return pending.Task; };
        var controller = fixture.GridController(coordinator, sink);
        Task request = controller.DispatchAsync(GridRequest(fixture.Session, cursor, 1));
        if (!cursor) time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));
        Assert.IsFalse(readToken.IsCancellationRequested);
        Task closeTask = close ? fixture.CloseAsync() : Task.CompletedTask;
        Task<TablePage>? replacement = null;
        if (!close)
        {
            gateway.CursorOpenOverride = (_, _, _) => Task.FromResult(page);
            replacement = coordinator.RequestQueryAsync("orders", Json("{}"), default);
        }
        Assert.IsTrue(readToken.IsCancellationRequested);
        if (close)
            pending.SetException(new InvalidOperationException("retired read failed"));
        else
            pending.SetResult(page);
        await Task.WhenAll(request, closeTask).WaitAsync(TimeSpan.FromSeconds(3));
        if (replacement is not null)
        {
            time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));
            Assert.AreSame(page, await replacement.WaitAsync(TimeSpan.FromSeconds(3)));
        }
        Assert.AreEqual(0, sink.Replies.Count);
    }

    private static TablePage GridPage() => new("orders", [], [], 0, 100, 1000, "remote",
        QuerySnapshot: new QuerySnapshot("snapshot", "digest", "database", "orders", "schema_1", 7,
            new Dictionary<string, object?>()), NextCursor: "cursor-next", HasMore: true);

    private static RoutedWebRequest GridRequest(WorkspaceSessionV2 session, bool cursor, ulong sequence)
        => new(cursor ? "table.cursorRequested" : "table.queryRequested", null,
            cursor ? Json("""{"cursor":"cursor-next"}""") : Json("""{"table":"orders","query":{}}"""), "",
            new WorkspaceWireScope
            {
                Scope = "workspace", WorkspaceId = session.WorkspaceId!.Value,
                SessionEpoch = session.SessionEpoch, OperationId = Guid.NewGuid(), Sequence = sequence,
            });
    private static JsonElement Json(string text)
    {
        using JsonDocument document = JsonDocument.Parse(text);
        return document.RootElement.Clone();
    }

    [TestMethod]
    public async Task StrictSchemaReadValidatesTheSelectedSidecarResponse()
    {
        await using var fixture = await HostFixture.OpenAsync();
        using JsonRpcProductDataGateway gateway = fixture.Gateway();

        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() => gateway.GetTableSchemaAsync(
            Json("""{"tableId":"orders"}"""), CancellationToken.None));

        Assert.AreEqual(0, fixture.Python.WriteCount);
        Assert.AreEqual("schema.getTable", fixture.Http.Calls.Single().GetProperty("method").GetString());
    }

    [TestMethod]
    [DataRow(false, false)]
    [DataRow(true, false)]
    [DataRow(false, true)]
    [DataRow(true, true)]
    public async Task RetiredGenerationCannotPublishALateReply(bool remoteError, bool fieldSettings)
    {
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.BeforeReply = (_, _) =>
        {
            fixture.Current = false;
            return Task.CompletedTask;
        };
        fixture.Http.Error = remoteError ? Json("""{"code":-32602,"message":"Invalid params"}""") : null;
        if (fieldSettings) fixture.Http.Result = ProductDataSidecarRoutingTests.FieldSettingsResult();
        using JsonRpcProductDataGateway gateway = fixture.Gateway(useGeneratedPolicy: true);

        await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() => fieldSettings
            ? gateway.DescribeFieldSettingsAsync(
                ProductDataSidecarRoutingTests.FieldSettingsParameters(), CancellationToken.None)
            : gateway.ListTablesAsync(Json("{}"), CancellationToken.None));

        Assert.AreEqual(0, fixture.Python.WriteCount);
        Assert.AreEqual(1, fixture.Http.Calls.Count);
    }

    [TestMethod]
    public async Task CallerCancellationDoesNotCancelAnotherCallsSharedHandshake()
    {
        await using var fixture = await HostFixture.OpenAsync();
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Http.BeforeHandshake = token => release.Task.WaitAsync(token);
        using JsonRpcProductDataGateway gateway = fixture.Gateway();
        using var cancelled = new CancellationTokenSource();
        Task<JsonElement> first = gateway.ListTablesAsync(Json("{}"), cancelled.Token);
        await fixture.Http.HandshakeEntered.Task.WaitAsync(TimeSpan.FromSeconds(3));
        Task<JsonElement> second = gateway.ListTablesAsync(Json("{}"), CancellationToken.None);
        cancelled.Cancel();
        try
        {
            await Assert.ThrowsAsync<OperationCanceledException>(() => first.WaitAsync(TimeSpan.FromSeconds(3)));
            Assert.IsFalse(second.IsCompleted);
        }
        finally
        {
            release.TrySetResult();
        }
        Assert.AreEqual("表格", (await second.WaitAsync(TimeSpan.FromSeconds(3)))
            .GetProperty("tables")[0].GetString());
        await gateway.ListTablesAsync(Json("{}"), CancellationToken.None);
        Assert.AreEqual(1, fixture.Http.Handshakes);
        Assert.AreEqual(2, fixture.Http.Calls.Count);
        Assert.AreEqual(0, fixture.Python.WriteCount);
    }

    [TestMethod]
    public async Task SeparateTypedGatewayOwnsItsOwnCapabilitiesHandshake()
    {
        await using var fixture = await HostFixture.OpenAsync();
        using JsonRpcProductDataGateway first = fixture.Gateway();
        using JsonRpcProductDataGateway second = fixture.Gateway();

        await first.ListTablesAsync(Json("{}"), CancellationToken.None);
        await second.ListTablesAsync(Json("{}"), CancellationToken.None);

        Assert.AreEqual(2, fixture.Http.Handshakes);
    }

    [TestMethod]
    public async Task ClosedWorkspaceAndRetiredBindingNeverSend()
    {
        await using var fixture = await HostFixture.OpenAsync();
        using JsonRpcProductDataGateway gateway = fixture.Gateway();
        fixture.Current = false;
        await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() =>
            gateway.ListTablesAsync(Json("{}"), CancellationToken.None));
        fixture.Current = true;
        await fixture.CloseAsync();
        await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() =>
            gateway.ListTablesAsync(Json("{}"), CancellationToken.None));

        Assert.AreEqual(0, fixture.Http.Handshakes);
        Assert.AreEqual(0, fixture.Python.WriteCount);
    }

    [TestMethod]
    public async Task WorkspaceCloseCancelsAndDrainsTheHostRequest()
    {
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.BeforeReply = (_, token) => Task.Delay(Timeout.Infinite, token);
        using JsonRpcProductDataGateway gateway = fixture.Gateway();
        Task<JsonElement> pending = gateway.ListTablesAsync(Json("{}"), CancellationToken.None);
        await fixture.Http.RpcEntered.Task.WaitAsync(TimeSpan.FromSeconds(3));

        await fixture.CloseAsync().WaitAsync(TimeSpan.FromSeconds(3));
        await Assert.ThrowsAsync<OperationCanceledException>(() => pending.WaitAsync(TimeSpan.FromSeconds(3)));

        Assert.AreEqual(1, fixture.Http.Calls.Count);
        Assert.AreEqual(0, fixture.Python.WriteCount);
    }

    [TestMethod]
    public async Task DisposeCancelsTheOwnedHandshake()
    {
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.BeforeHandshake = token => Task.Delay(Timeout.Infinite, token);
        using JsonRpcProductDataGateway gateway = fixture.Gateway();
        Task<JsonElement> pending = gateway.ListTablesAsync(Json("{}"), CancellationToken.None);
        await fixture.Http.HandshakeEntered.Task.WaitAsync(TimeSpan.FromSeconds(3));

        gateway.Dispose();
        await Assert.ThrowsAsync<OperationCanceledException>(() => pending.WaitAsync(TimeSpan.FromSeconds(3)));

        Assert.AreEqual(0, fixture.Http.Calls.Count);
        await Assert.ThrowsExactlyAsync<ObjectDisposedException>(() =>
            gateway.ListTablesAsync(Json("{}"), CancellationToken.None));
    }

    [TestMethod]
    public async Task WorkspaceDrainOwnsTheSharedHandshakeUntilHttpActuallyEnds()
    {
        await using var fixture = await HostFixture.OpenAsync();
        var cancelled = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Http.BeforeHandshake = async token =>
        {
            using var registration = token.Register(() => cancelled.TrySetResult());
            await release.Task;
            token.ThrowIfCancellationRequested();
        };
        using JsonRpcProductDataGateway gateway = fixture.Gateway();
        Task<JsonElement> pending = gateway.ListTablesAsync(Json("{}"), CancellationToken.None);
        await fixture.Http.HandshakeEntered.Task.WaitAsync(TimeSpan.FromSeconds(3));
        Task close = fixture.CloseAsync();
        try
        {
            await cancelled.Task.WaitAsync(TimeSpan.FromSeconds(3));
            await Assert.ThrowsAsync<OperationCanceledException>(() => pending.WaitAsync(TimeSpan.FromSeconds(3)));
            Assert.IsFalse(close.IsCompleted, "The HTTP peer still owns the epoch even after its waiter cancelled.");
        }
        finally
        {
            release.TrySetResult();
        }
        await close.WaitAsync(TimeSpan.FromSeconds(3));
        Assert.AreEqual(0, fixture.Http.Calls.Count);
    }

    [TestMethod]
    public async Task CurrentRemoteErrorKeepsItsCodeMessageAndDataWithoutFallback()
    {
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.Error = Json("""{"code":-32602,"message":"参数无效","data":{"field":"limit"}}""");
        using JsonRpcProductDataGateway gateway = fixture.Gateway();

        RpcRemoteException error = await Assert.ThrowsExactlyAsync<RpcRemoteException>(() =>
            gateway.ListTablesAsync(Json("{}"), CancellationToken.None));

        Assert.AreEqual(-32602, error.Code);
        Assert.AreEqual("参数无效", error.Message);
        Assert.AreEqual("limit", error.ErrorData!.Value.GetProperty("field").GetString());
        Assert.AreEqual(0, fixture.Python.WriteCount);
    }

    [TestMethod]
    public async Task WorkspaceCatalogKeepsPythonAndMissingProductOwnerFailsClosed()
    {
        JsonElement expected = Json("""{"contract":"vibetable.schema.v2","fields":[]}""");
        await using var fixture = await HostFixture.OpenAsync(id =>
            JsonSerializer.SerializeToElement(new { jsonrpc = "2.0", id, result = expected }));
        using JsonRpcProductDataGateway gateway = fixture.Gateway();
        JsonElement recycled = await gateway.ListRecycledFieldsAsync(
            Json("""{"tableId":"orders"}"""), CancellationToken.None);
        Assert.IsTrue(JsonElement.DeepEquals(expected, recycled));
        Assert.AreEqual(1, fixture.Python.WriteCount);
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.DescribeFieldSettingsAsync(Json("""{"tableId":"orders"}"""), CancellationToken.None));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.QueryPageAsync(Json("{}"), CancellationToken.None));

        Assert.AreEqual(1, fixture.Python.WriteCount);
        Assert.AreEqual(0, fixture.Http.Handshakes);
    }

    [TestMethod]
    public async Task TypedHistoryParametersKeepExplicitNullsOnTheSelectedRoute()
    {
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.Result = Json("""
            {"collection":"orders","itemId":null,"changeSets":[],"total":0,
             "capabilityHash":"test-capability","schemaRevision":"schema_1"}
            """);
        using JsonRpcProductDataGateway gateway = fixture.Gateway(useGeneratedPolicy: true);

        HistoryPage result = await gateway.ReadHistoryAsync(
            new ReadChangeSetsParams("orders", null, 25, 0), CancellationToken.None);

        Assert.AreEqual("orders", result.Collection);
        JsonElement parameters = fixture.Http.Calls.Single().GetProperty("params");
        Assert.AreEqual(JsonValueKind.Null, parameters.GetProperty("itemId").ValueKind);
        Assert.AreEqual(JsonValueKind.Null, parameters.GetProperty("actions").ValueKind);
        Assert.AreEqual(25, parameters.GetProperty("limit").GetInt32());
        Assert.AreEqual(0, fixture.Python.WriteCount);
    }

    [TestMethod]
    public async Task FieldSettingsDescriptionUsesGeneratedGoPolicyWithoutPython()
    {
        JsonElement expected = ProductDataSidecarRoutingTests.FieldSettingsResult();
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.Result = expected;
        using JsonRpcProductDataGateway gateway = fixture.Gateway(useGeneratedPolicy: true);
        JsonElement parameters = ProductDataSidecarRoutingTests.FieldSettingsParameters();
        JsonElement result = await gateway.DescribeFieldSettingsAsync(
            parameters, CancellationToken.None);
        Assert.IsTrue(JsonElement.DeepEquals(expected, result));
        Assert.AreEqual(0, fixture.Python.WriteCount);
        Assert.AreEqual(1, fixture.Http.Handshakes);
        JsonElement call = fixture.Http.Calls.Single();
        Assert.AreEqual("field.settings.describe", call.GetProperty("method").GetString());
        Assert.IsTrue(JsonElement.DeepEquals(parameters, call.GetProperty("params")));
        JsonElement wire = call.GetProperty("wire");
        Assert.AreEqual("workspace", wire.GetProperty("scope").GetString());
        Assert.AreEqual(fixture.Session.WorkspaceId!.Value.ToString("D"),
            wire.GetProperty("workspaceId").GetString());
        Assert.AreEqual(fixture.Session.SessionEpoch, wire.GetProperty("sessionEpoch").GetUInt64());
    }


    private const string SurfaceSnapshotJson = """{"definition":{"contractVersion":"1.0","interfaceId":"if-orders","name":"Orders","bindings":[{"bindingId":"orders","query":{"contractVersion":"1.0","tableId":"orders","fields":["title"],"filters":[],"sorts":[],"cursor":null,"pageSize":50},"variables":[]}],"actions":[{"actionId":"navigate","kind":"navigate","bindingId":null,"targetPageId":"main","pluginId":null,"pluginActionId":null,"requiresConfirmation":false}],"pages":[{"pageId":"main","title":"Orders","elements":[{"elementId":"nav","kind":"navigation","bindingId":null,"actionId":"navigate","text":null,"width":"full","children":[]}]}]},"revision":"stored-revision"}""";

    [TestMethod]
    public async Task SurfaceControllerUsesFourTypedProductMethodsAndRegularResponses()
    {
        await using var fixture = await HostFixture.OpenAsync();
        using JsonRpcProductDataGateway gateway = fixture.Gateway(useGeneratedPolicy: true);
        var sink = new SurfaceReplySink();
        var controller = new SurfaceRequestController(sink, TimeSpan.FromSeconds(5));
        controller.SetGateway(gateway);
        (string Method, string Request, string Response, string Parameters, string Result)[] cases =
        [
            ("interface.list", "interface.listRequested", "interface.listLoaded", "{}", """{"items":[{"interfaceId":"if-orders","name":"Orders","revision":"stored-revision"}]}"""),
            ("interface.load", "interface.loadRequested", "interface.loaded", """{"interfaceId":"if-orders"}""", SurfaceSnapshotJson),
            ("interface.commit", "interface.commitRequested", "interface.committed", $$"""{"definition":{{Json(SurfaceSnapshotJson).GetProperty("definition")}},"expectedRevision":null,"idempotencyKey":"commit-key"}""", SurfaceSnapshotJson),
            ("interface.delete", "interface.deleteRequested", "interface.deleted", """{"interfaceId":"if-orders","expectedRevision":"stored-revision","idempotencyKey":"delete-key"}""", """{"interfaceId":"if-orders"}"""),
        ];
        foreach (var item in cases)
        {
            fixture.Http.Result = Json(item.Result);
            await controller.DispatchAsync(new(item.Request, item.Method, Json(item.Parameters), ""));
            Assert.AreEqual(item.Response, sink.Type);
            Assert.AreEqual(item.Method, sink.RequestId);
            Assert.IsNull(sink.ErrorCode);
            Assert.IsTrue(JsonElement.DeepEquals(Json(item.Result), sink.Payload));
            JsonElement call = fixture.Http.Calls.Last();
            Assert.AreEqual(item.Method, call.GetProperty("method").GetString());
            Assert.IsTrue(JsonElement.DeepEquals(Json(item.Parameters), call.GetProperty("params")));
        }
        Assert.AreEqual(1, fixture.Http.Handshakes);
        Assert.AreEqual(4, fixture.Http.Calls.Count);
        Assert.AreEqual(0, fixture.Python.WriteCount);
    }

    [TestMethod]
    [DataRow("surface.not_found", null, "surface.not_found")]
    [DataRow("surface.edit_conflict", "expectedRevision", "surface.edit_conflict")]
    [DataRow("surface.idempotency_conflict", "idempotencyKey", "surface.idempotency_conflict")]
    [DataRow("surface.name_required", "", "SURFACE_OPERATION_FAILED")]
    [DataRow("surface.future_error", null, "SURFACE_OPERATION_FAILED")]
    [DataRow("contentProfile.not_found", null, "SURFACE_OPERATION_FAILED")]
    public async Task SurfaceControllerMapsOnlyItsKnownSafeFailures(string code, string? path, string expected)
    {
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.Error = SurfaceError(code, path);
        using JsonRpcProductDataGateway gateway = fixture.Gateway(useGeneratedPolicy: true);
        var sink = new SurfaceReplySink();
        var controller = new SurfaceRequestController(sink, TimeSpan.FromSeconds(5));
        controller.SetGateway(gateway);
        await controller.DispatchAsync(new("interface.loadRequested", "surface-error", Json("""{"interfaceId":"if-orders"}"""), ""));
        Assert.AreEqual("operation.failed", sink.Type);
        Assert.AreEqual("surface-error", sink.RequestId);
        Assert.AreEqual(expected, sink.ErrorCode);
        Assert.AreEqual(0, fixture.Python.WriteCount);
    }

    [TestMethod]
    public async Task SurfaceErrorRetainsExplicitEmptyPathAndCannotCrossMethods()
    {
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.Error = SurfaceError("surface.name_required", "");
        using JsonRpcProductDataGateway product = fixture.Gateway(useGeneratedPolicy: true);
        ISurfaceRpcGateway surface = product;
        RpcRemoteException error = await Assert.ThrowsExactlyAsync<RpcRemoteException>(() => surface.LoadAsync("if-orders", CancellationToken.None));
        Assert.AreEqual(-32170, error.Code);
        Assert.AreEqual("", error.ErrorData!.Value.GetProperty("path").GetString());
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() => product.ListTablesAsync(Json("{}"), CancellationToken.None));
        Assert.AreEqual(0, fixture.Python.WriteCount);
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task SurfaceControllerCannotPublishAfterCancellationOrRetirement(bool retire)
    {
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.Result = Json(SurfaceSnapshotJson);
        using var cancellation = new CancellationTokenSource();
        fixture.Http.BeforeReply = (_, _) =>
        {
            if (retire) fixture.Current = false;
            else cancellation.Cancel();
            return Task.CompletedTask;
        };
        using JsonRpcProductDataGateway gateway = fixture.Gateway(useGeneratedPolicy: true);
        var sink = new SurfaceReplySink();
        var controller = new SurfaceRequestController(sink, TimeSpan.FromSeconds(5), () => cancellation.Token);
        controller.SetGateway(gateway);
        await controller.DispatchAsync(new("interface.loadRequested", "retired", Json("""{"interfaceId":"if-orders"}"""), ""));
        Assert.AreEqual("operation.failed", sink.Type);
        Assert.AreEqual(retire ? "SURFACE_BACKEND_UNAVAILABLE" : "SURFACE_CANCELLED", sink.ErrorCode);
        Assert.AreEqual(0, fixture.Python.WriteCount);
    }

    [TestMethod]
    public async Task SurfaceProductParserRejectsMalformedDomainDataWithoutPythonFallback()
    {
        await using var fixture = await HostFixture.OpenAsync();
        using JsonRpcProductDataGateway product = fixture.Gateway(useGeneratedPolicy: true);
        ISurfaceRpcGateway surface = product;
        foreach (string data in new[]
        {
            """{"kind":"surface_error","message":"missing","code":"surface.not_found","path":null}""",
            """{"kind":"surface_error","message":"missing","code":"surface.not_found","extra":true}""",
            """{"kind":"surface_error","message":"missing"}""",
            """{"kind":"product_data_error","message":"missing","code":"surface.not_found"}""",
        })
        {
            fixture.Http.Error = Json($$"""{"code":-32170,"message":"Interface error","data":{{data}}}""");
            await Assert.ThrowsExactlyAsync<InvalidOperationException>(() => surface.LoadAsync("if-orders", CancellationToken.None));
        }
        Assert.AreEqual(0, fixture.Python.WriteCount);
    }

    private static JsonElement SurfaceError(string code, string? path)
    {
        var data = new Dictionary<string, object> { ["kind"] = "surface_error", ["message"] = "Interface rejected.", ["code"] = code };
        if (path is not null) data["path"] = path;
        return JsonSerializer.SerializeToElement(new { code = -32170, message = "Interface error", data });
    }

    private sealed class SurfaceReplySink : IWebReplySink
    {
        internal string? Type { get; private set; }
        internal string? RequestId { get; private set; }
        internal string? ErrorCode { get; private set; }
        internal JsonElement Payload { get; private set; }
        public void PostNotification(string type, object? payload) => Assert.Fail("Unexpected notification.");
        public void PostResponse(string type, string? requestId, object? payload)
        {
            Type = type; RequestId = requestId; ErrorCode = null;
            Payload = JsonSerializer.SerializeToElement(payload, new JsonSerializerOptions(JsonSerializerDefaults.Web));
        }
        public void PostOperationFailed(string? requestId, string message, string? code = null, string? operation = null, string? operationId = null)
        {
            Type = "operation.failed"; RequestId = requestId; ErrorCode = code;
        }
    }

    private sealed class HostFixture : IAsyncDisposable
    {
        private readonly string _root = Path.Combine(Path.GetTempPath(),
            "vibetable-host-rpc-" + Guid.NewGuid().ToString("N"));
        private readonly WorkspaceSessionManager _sessions;
        private readonly WorkspaceSessionEnvelopeFilter _leases;
        private readonly JsonRpcClient _client;
        private ProductSidecarGenerationSnapshot _snapshot = null!;

        private HostFixture(Func<string, JsonElement>? response)
        {
            _sessions = new WorkspaceSessionManager(new WorkspaceRegistry(_root), new RuntimeFactory());
            _leases = new WorkspaceSessionEnvelopeFilter(_sessions);
            _sessions.SetRequestDrainHook(_leases);
            Python = new CountingQueryTransport(response);
            _client = new JsonRpcClient(Python);
        }

        internal CountingQueryTransport Python { get; }
        internal ProductHttpPeer Http { get; private set; } = null!;
        internal WorkspaceSessionV2 Session { get; private set; } = null!;
        internal bool Current { get; set; } = true;
        internal Task CloseAsync() => _sessions.CloseAsync("host-rpc-test");

        internal static async Task<HostFixture> OpenAsync(Func<string, JsonElement>? response = null)
        {
            var fixture = new HostFixture(response);
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(Path.Combine(fixture._root, "workspace"),
                "Host RPC", WorkspaceStorageMode.Direct, WorkspaceEncryptionMode.Convenient);
            var registry = new WorkspaceRegistry(fixture._root);
            registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = "2.0",
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = "Host RPC",
                SelectedRoot = layout.SelectedRoot,
                ActivityRoot = null,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = null,
                LastSyncAt = null,
                PendingSync = false,
            });
            fixture.Session = await fixture._sessions.OpenAsync(layout.Manifest.WorkspaceId,
                WorkspaceOpenMode.ReadOnly);
            fixture._snapshot = new ProductSidecarGenerationSnapshot(fixture, 1,
                new PocketBaseAdminContext(new Uri("http://127.0.0.1:12345/bootstrap"),
                    new Uri("http://127.0.0.1:12345/"), "X-VibeTable-Session", "test-session"),
                new ProductSidecarIdentity(layout.Manifest.WorkspaceId.ToString("D"),
                    fixture.Session.SessionEpoch, 3, "22222222-2222-4222-8222-222222222222"),
                [new("field.settings.describe", "workspace"), new("file.list", "workspace"), new("history.read", "workspace"), new("interface.commit", "workspace"), new("interface.delete", "workspace"), new("interface.list", "workspace"), new("interface.load", "workspace"), new("schema.getTable", "workspace"), new("schema.list", "workspace")]);
            fixture.Http = new ProductHttpPeer(fixture._snapshot);
            return fixture;
        }

        internal PocketBaseTableGateway TableGateway() => new(
            Gateway(useGeneratedPolicy: true), new JsonRpcWorkspaceSupportGateway(_client));

        internal GridRequestController GridController(GridStateCoordinator coordinator, IWebReplySink sink)
            => new(coordinator, sink, sessions: _leases);
        internal JsonRpcProductDataGateway Gateway(bool useGeneratedPolicy = false) => new(
            new HostProductRpcInvoker(_client, _snapshot, _leases,
                action => Current && action(),
                useGeneratedPolicy
                    ? ProductRpcRouteSelector.Default
                    : new ProductRpcRouteSelector(ProductRpcCapabilityManifest.CreateForTests(
                        new ProductRpcCapability("history.read", "workspace", "hostOnly",
                            "history.read", "goSidecar", "read"),
                        new ProductRpcCapability("file.list", "workspace", "hostOnly",
                            "file.attachment", "goSidecar", "read"),
                        new ProductRpcCapability("schema.getTable", "workspace", "hostOnly",
                            "schema.read", "goSidecar", "read"),
                        new ProductRpcCapability("schema.list", "workspace", "hostOnly",
                            "schema.read", "goSidecar", "read"))), Http));

        public async ValueTask DisposeAsync()
        {
            await _client.DisposeAsync();
            _leases.Dispose();
            await _sessions.DisposeAsync();
            Http.Dispose();
            Directory.Delete(_root, recursive: true);
        }
    }

    private sealed class ProductHttpPeer(ProductSidecarGenerationSnapshot snapshot) : HttpMessageHandler
    {
        internal int Handshakes { get; private set; }
        internal List<JsonElement> Calls { get; } = [];
        internal Func<JsonElement, CancellationToken, Task>? BeforeReply { get; set; }
        internal Func<CancellationToken, Task>? BeforeHandshake { get; set; }
        internal JsonElement? Error { get; set; }
        internal JsonElement Result { get; set; } = Json("""{"tables":["表格"]}""");
        internal TaskCompletionSource HandshakeEntered { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal TaskCompletionSource RpcEntered { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);

        protected override async Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request, CancellationToken cancellationToken)
        {
            if (request.Method == HttpMethod.Get)
            {
                Handshakes++;
                HandshakeEntered.TrySetResult();
                if (BeforeHandshake is not null)
                    await BeforeHandshake(cancellationToken);
                return Reply(JsonSerializer.SerializeToElement(new
                {
                    contractVersion = "2.0",
                    workspaceId = snapshot.Identity.WorkspaceId,
                    sessionEpoch = snapshot.Identity.SessionEpoch,
                    fenceEpoch = snapshot.Identity.FenceEpoch,
                    claimId = snapshot.Identity.ClaimId,
                    rpcMethods = snapshot.Registrations.Select(item => item.Method),
                    registrations = snapshot.Registrations.Select(item => new { method = item.Method, scope = item.Scope }),
                }));
            }
            JsonElement call = Json(await request.Content!.ReadAsStringAsync(cancellationToken));
            Calls.Add(call);
            RpcEntered.TrySetResult();
            if (BeforeReply is not null)
                await BeforeReply(call, cancellationToken);
            if (Error is { } error)
                return Reply(JsonSerializer.SerializeToElement(new
                {
                    jsonrpc = "2.0",
                    id = call.GetProperty("id"),
                    wire = call.GetProperty("wire"),
                    error,
                }));
            return Reply(JsonSerializer.SerializeToElement(new
            {
                jsonrpc = "2.0",
                id = call.GetProperty("id"),
                wire = call.GetProperty("wire"),
                result = Result,
            }));
        }

        private static HttpResponseMessage Reply(JsonElement body) => new(HttpStatusCode.OK)
        {
            Content = new StringContent(body.GetRawText()),
        };
    }

    private sealed class RuntimeFactory : IWorkspaceRuntimeFactory
    {
        public IWorkspaceRuntime Create(WorkspaceRegistryEntryV2 workspace, ulong sessionEpoch)
            => new RuntimePeer(workspace.WorkspaceId, sessionEpoch);
    }

    private sealed class RuntimePeer(Guid workspaceId, ulong sessionEpoch) : IWorkspaceRuntime
    {
        public Guid WorkspaceId => workspaceId;
        public ulong SessionEpoch => sessionEpoch;
        public Task StartAsync(WorkspaceOpenMode mode, WorkspaceActivationBudget budget) => Task.CompletedTask;
        public Task VerifyAsync(WorkspaceActivationBudget budget) => Task.CompletedTask;
        public Task DrainAsync(CancellationToken cancellationToken) => Task.CompletedTask;
        public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;
        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }
}
