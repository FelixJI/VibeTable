using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductDataSidecarRoutingTests
{
    [TestMethod]
    [DataRow("schema.describe", true)]
    [DataRow("schema.describe", false)]
    [DataRow("lookup.list", true)]
    [DataRow("lookup.list", false)]
    [DataRow("relation.previewDelta", true)]
    [DataRow("relation.previewDelta", false)]
    public async Task CatalogReadUsesGeneratedGoOwnerWithoutPythonFallback(string method, bool bound)
    {
        var sink = new FakeWebReplySink();
        var sidecar = SuccessForwarder();
        var pythonTransport = new CountingQueryTransport();
        await using var pythonClient = new JsonRpcClient(pythonTransport);
        using var pythonGateway = new JsonRpcProductDataGateway(pythonClient);
        var controller = new ProductDataRequestController(sink);
        controller.SetGateway(pythonGateway);
        if (bound) controller.SetProductSidecarForwarder(sidecar);
        RoutedWebRequest request = QueryRequest("describe-go") with
        {
            Type = method,
            Payload = method == "relation.previewDelta"
                ? JsonSerializer.SerializeToElement(new { relationId = "records.owner", sourceItemId = "record-1",
                    expectedSchemaRevision = "schema-1", adds = Array.Empty<object>(),
                    removes = Array.Empty<object>(), idempotencyKey = "preview-test" })
                : method == "lookup.list"
                ? JsonSerializer.SerializeToElement(new { collection = "tbl_records" })
                : JsonSerializer.SerializeToElement(new
            {
                collection = "tbl_records", requestGeneration = 1,
                accepts = new[] { "vibetable.relation-capabilities.v1", "vibetable.lookup-query.v1" },
            }),
        };

        await controller.DispatchAsync(request);

        FakeWebReplySink.Reply? reply = bound
            ? await sink.WaitForAsync(method)
            : await sink.WaitForFailedAsync();
        Assert.IsNotNull(reply);
        Assert.AreEqual(bound ? 1 : 0, sidecar.CallCount);
        Assert.AreEqual(0, pythonTransport.WriteCount);
        if (bound)
        {
            ProductSidecarForwardCall call = sidecar.Calls.Single();
            Assert.AreEqual(method, call.Method);
            Assert.IsTrue(JsonElement.DeepEquals(request.Wire, call.Wire));
            Assert.IsTrue(JsonElement.DeepEquals(request.Payload, call.Parameters));
        }
        else
        {
            StringAssert.Contains(JsonSerializer.Serialize(reply.Payload), "BACKEND_UNAVAILABLE");
        }
    }

    [TestMethod]
    public async Task GoQueryUsesOneSidecarSendAndNeverCallsPythonGateway()
    {
        var sink = new FakeWebReplySink();
        var sidecar = new ControlledProductSidecarForwarder((call, _) =>
            Task.FromResult<ProductSidecarForwardResult>(
                new ProductSidecarSuccess(
                    call.Wire.Clone(),
                    JsonSerializer.SerializeToElement(new
                    {
                        rows = new object?[] { "值", null, 0 },
                        total = 3,
                    }))));
        var pythonTransport = new CountingQueryTransport();
        await using var pythonClient = new JsonRpcClient(pythonTransport);
        using var pythonGateway = new JsonRpcProductDataGateway(pythonClient);
        var controller = Controller(sink);
        controller.SetGateway(pythonGateway);
        controller.SetProductSidecarForwarder(sidecar);
        RoutedWebRequest request = QueryRequest("go-query");

        await controller.DispatchAsync(request);

        FakeWebReplySink.Reply? reply = await sink.WaitForAsync("query.page");
        Assert.IsNotNull(reply);
        Assert.AreEqual(1, sidecar.CallCount);
        Assert.AreEqual(0, pythonTransport.WriteCount);
        ProductSidecarForwardCall call = sidecar.Calls.Single();
        Assert.AreEqual("go-query", call.RequestId);
        Assert.AreEqual("query.page", call.Method);
        Assert.IsTrue(JsonElement.DeepEquals(request.Wire, call.Wire));
        Assert.IsTrue(JsonElement.DeepEquals(request.Payload, call.Parameters));
    }

    [TestMethod]
    public async Task MissingGoBindingFailsWithoutFallingBackToPython()
    {
        var sink = new FakeWebReplySink();
        var pythonTransport = new CountingQueryTransport();
        await using var pythonClient = new JsonRpcClient(pythonTransport);
        using var pythonGateway = new JsonRpcProductDataGateway(pythonClient);
        var controller = Controller(sink);
        controller.SetGateway(pythonGateway);

        await controller.DispatchAsync(QueryRequest("missing-binding"));

        FakeWebReplySink.Reply? reply = await sink.WaitForFailedAsync();
        Assert.IsNotNull(reply);
        StringAssert.Contains(
            JsonSerializer.Serialize(reply.Payload),
            @"""code"":""BACKEND_UNAVAILABLE""");
        Assert.AreEqual(0, pythonTransport.WriteCount);
    }

    [TestMethod]
    public async Task UnavailableGoBindingIsSentOnceWithoutPythonFallback()
    {
        var sink = new FakeWebReplySink();
        var sidecar = new ControlledProductSidecarForwarder((_, _) =>
            throw new BackendUnavailableException("sidecar restarting"));
        var pythonTransport = new CountingQueryTransport();
        await using var pythonClient = new JsonRpcClient(pythonTransport);
        using var pythonGateway = new JsonRpcProductDataGateway(pythonClient);
        var controller = Controller(sink);
        controller.SetGateway(pythonGateway);
        controller.SetProductSidecarForwarder(sidecar);

        await controller.DispatchAsync(QueryRequest("unavailable-go"));

        FakeWebReplySink.Reply? reply = await sink.WaitForFailedAsync();
        Assert.IsNotNull(reply);
        StringAssert.Contains(
            JsonSerializer.Serialize(reply.Payload),
            @"""code"":""BACKEND_UNAVAILABLE""");
        Assert.AreEqual(1, sidecar.CallCount);
        Assert.AreEqual(0, pythonTransport.WriteCount);
    }

    [TestMethod]
    public async Task GoInvalidParamsMapsToBadPayload()
    {
        var sink = new FakeWebReplySink();
        var sidecar = FailureForwarder(new ProductSidecarRpcError(
            -32602,
            "Invalid params.",
            null));
        var controller = Controller(sink);
        controller.SetProductSidecarForwarder(sidecar);

        await controller.DispatchAsync(QueryRequest("bad-params"));

        FakeWebReplySink.Reply? reply = await sink.WaitForFailedAsync();
        Assert.IsNotNull(reply);
        StringAssert.Contains(
            JsonSerializer.Serialize(reply.Payload),
            @"""code"":""BAD_PAYLOAD""");
    }

    [TestMethod]
    public async Task GoProductErrorUsesExistingSafeRendererMapper()
    {
        JsonElement errorData = JsonSerializer.SerializeToElement(new
        {
            kind = "product_data_error",
            message = "筛选条件无效。",
            code = "query.invalid_filter",
            path = (string?)null,
            details = new { field = "名称" },
            retryable = false,
        });
        var sink = new FakeWebReplySink();
        var controller = Controller(sink);
        controller.SetProductSidecarForwarder(FailureForwarder(
            new ProductSidecarRpcError(-32150, "Product error.", errorData)));

        await controller.DispatchAsync(QueryRequest("product-error"));

        FakeWebReplySink.Reply? reply = await sink.WaitForAsync("query.page");
        Assert.IsNotNull(reply);
        JsonElement payload = Assert.IsInstanceOfType<JsonElement>(reply.Payload);
        Assert.AreEqual(
            "query.invalid_filter",
            payload.GetProperty("error").GetProperty("code").GetString());
        Assert.AreEqual(
            "名称",
            payload.GetProperty("error").GetProperty("details")
                .GetProperty("field").GetString());
    }

    [TestMethod]
    [DataRow(-32602, "BAD_PAYLOAD")]
    [DataRow(-32030, "BACKEND_UNAVAILABLE")]
    [DataRow(-32150, "RELATION_LOOKUP_FAILED")]
    public async Task RelationGoErrorPreservesExistingRendererMapping(int code, string expected)
    {
        var sink = new FakeWebReplySink();
        var pythonTransport = new CountingQueryTransport();
        await using var client = new JsonRpcClient(pythonTransport);
        using var gateway = new JsonRpcProductDataGateway(client);
        var controller = new ProductDataRequestController(sink);
        controller.SetGateway(gateway);
        var sidecar = FailureForwarder(new ProductSidecarRpcError(code, "failure", null));
        controller.SetProductSidecarForwarder(sidecar);
        RoutedWebRequest request = QueryRequest("relation-failure") with
        {
            Type = "relation.previewDelta",
            Payload = JsonSerializer.SerializeToElement(new { relationId = "records.owner", sourceItemId = "record-1",
                    expectedSchemaRevision = "schema-1", adds = Array.Empty<object>(),
                    removes = Array.Empty<object>(), idempotencyKey = "preview-test" }),
        };
        await controller.DispatchAsync(request);
        FakeWebReplySink.Reply reply = sink.Replies.Single();
        Assert.AreEqual("operation.failed", reply.Type);
        Assert.AreEqual(expected, JsonSerializer.SerializeToElement(reply.Payload)
            .GetProperty("code").GetString());
        Assert.AreEqual(1, sidecar.CallCount);
        Assert.AreEqual(0, pythonTransport.WriteCount);
    }

    [TestMethod]
    public void WorkspaceDispatcherExposesOnlyConditionalSidecarBindingSeam()
    {
        var tableGateway = new FakeTableRpcGateway();
        using var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(tableGateway),
            new FakeDatabasePicker(null),
            new FakeWebReplySink(),
            NoDatabaseOpenRoute.Instance);
        var bound = SuccessForwarder();
        var other = SuccessForwarder();

        dispatcher.SetProductSidecarForwarder(bound);

        Assert.IsFalse(dispatcher.ClearProductSidecarForwarder(other));
        Assert.IsTrue(dispatcher.ClearProductSidecarForwarder(bound));
    }

    private static ProductDataRequestController Controller(FakeWebReplySink sink)
        => new(
            sink,
            SelectorFor("query.page", "goSidecar"));

    private static ProductRpcRouteSelector SelectorFor(string method, string owner)
        => new(ProductRpcCapabilityManifest.CreateForTests(
            new ProductRpcCapability(
                method,
                "workspace",
                "rendererPublic",
                $"product.{method}",
                owner,
                "read")));

    private static RoutedWebRequest QueryRequest(string requestId)
    {
        JsonElement payload = JsonSerializer.SerializeToElement(new
        {
            tableId = "tbl_records",
            query = new
            {
                filters = Array.Empty<object>(),
                sorts = Array.Empty<object>(),
                offset = 0,
                limit = 100,
            },
        });
        JsonElement wire = JsonSerializer.SerializeToElement(new
        {
            scope = "workspace",
            workspaceId = "11111111-1111-4111-8111-111111111111",
            sessionEpoch = 7,
            operationId = "22222222-2222-4222-8222-222222222222",
            sequence = 0,
        });
        return new RoutedWebRequest(
            "query.page",
            requestId,
            payload,
            string.Empty,
            Wire: wire);
    }

    private static ControlledProductSidecarForwarder FailureForwarder(
        ProductSidecarRpcError error)
        => new((call, _) => Task.FromResult<ProductSidecarForwardResult>(
            new ProductSidecarFailure(call.Wire.Clone(), error)));

    private static ControlledProductSidecarForwarder SuccessForwarder()
        => new((call, _) => Task.FromResult<ProductSidecarForwardResult>(
            new ProductSidecarSuccess(
                call.Wire.Clone(),
                JsonSerializer.SerializeToElement(new { rows = Array.Empty<object>() }))));
}

internal sealed record ProductSidecarForwardCall(
    string RequestId,
    string Method,
    JsonElement Wire,
    JsonElement Parameters);

internal sealed class ControlledProductSidecarForwarder : IProductSidecarRpcForwarder
{
    private readonly Func<
        ProductSidecarForwardCall,
        CancellationToken,
        Task<ProductSidecarForwardResult>> _forward;
    private readonly List<ProductSidecarForwardCall> _calls = [];

    internal ControlledProductSidecarForwarder(
        Func<ProductSidecarForwardCall, CancellationToken,
            Task<ProductSidecarForwardResult>> forward)
    {
        _forward = forward;
    }

    internal IReadOnlyList<ProductSidecarForwardCall> Calls => _calls;
    internal int CallCount => _calls.Count;

    public Task<ProductSidecarForwardResult> ForwardAsync(
        string requestId,
        string method,
        JsonElement wire,
        JsonElement parameters,
        CancellationToken cancellationToken)
    {
        var call = new ProductSidecarForwardCall(
            requestId,
            method,
            wire.Clone(),
            parameters.Clone());
        _calls.Add(call);
        return _forward(call, cancellationToken);
    }
}

internal sealed class CountingQueryTransport : IJsonLineTransport
{
    private readonly Channel<JsonElement?> _incoming =
        Channel.CreateUnbounded<JsonElement?>();
    private readonly Func<string, JsonElement> _response;
    private int _writeCount;

    internal CountingQueryTransport(Func<string, JsonElement>? response = null)
    {
        _response = response ?? (id => JsonSerializer.SerializeToElement(new
        {
            jsonrpc = "2.0",
            id,
            result = new { rows = Array.Empty<object>(), total = 0 },
        }));
    }

    internal int WriteCount => Volatile.Read(ref _writeCount);

    public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
        => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

    public Task WriteAsync(string line, CancellationToken cancellationToken)
    {
        Interlocked.Increment(ref _writeCount);
        using JsonDocument request = JsonDocument.Parse(line);
        string id = request.RootElement.GetProperty("id").GetString()!;
        _incoming.Writer.TryWrite(_response(id));
        return Task.CompletedTask;
    }

    public ValueTask DisposeAsync()
    {
        _incoming.Writer.TryComplete();
        return ValueTask.CompletedTask;
    }
}
