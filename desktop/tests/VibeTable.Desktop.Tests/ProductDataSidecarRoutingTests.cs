using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductDataSidecarRoutingTests
{
    // Transport fixture only: domain qualification uses snapshots signed by the real QueryPort.
    internal const string SnapshotPayload = """
        {"snapshot":{"snapshotId":"fixture","digest":"fixture-signature","databaseId":"db-1","table":"records","schemaRevision":"schema-1","dataRevision":7,"normalizedQuery":{"keyword":"名称","filters":[{"field":"名称","operator":"contains","value":"值"}],"sorts":[{"field":"名称","direction":"asc"}],"offset":0,"limit":20}},"currentQuery":{"keyword":"名称","filters":[{"field":"名称","operator":"contains","value":"值"}],"sorts":[{"field":"名称","direction":"asc"}],"offset":0,"limit":20}}
        """;

    internal const string LookupQueryPayload = """
        {"contract":"vibetable.lookup-query.v1","collection":"orders","fieldRefs":["customer_name"],"query":{"offset":0,"limit":50},"requestGeneration":7,"schemaRevision":"schema-1","permissionRevision":"schema-1","lookupRevision":"lookup-1"}
        """;

    [TestMethod]
    [DataRow("schema.describe", true)]
    [DataRow("schema.describe", false)]
    [DataRow("lookup.list", true)]
    [DataRow("lookup.list", false)]
    [DataRow("field.settings.describe", true)]
    [DataRow("field.settings.describe", false)]
    [DataRow("lookup.valuePage", true)]
    [DataRow("lookup.valuePage", false)]
    [DataRow("relation.searchTargets", true)]
    [DataRow("relation.searchTargets", false)]
    [DataRow("relation.previewDelta", true)]
    [DataRow("relation.previewDelta", false)]
    [DataRow("lookup.query", true)]
    [DataRow("lookup.query", false)]
    public async Task CatalogReadUsesGeneratedGoOwnerWithoutPythonFallback(string method, bool bound)
    {
        var sink = new FakeWebReplySink();
        var sidecar = method == "field.settings.describe"
            ? new ControlledProductSidecarForwarder((call, _) =>
                Task.FromResult<ProductSidecarForwardResult>(new ProductSidecarSuccess(
                    call.Wire.Clone(), FieldSettingsResult())))
            : SuccessForwarder();
        var pythonTransport = new CountingQueryTransport();
        await using var pythonClient = new JsonRpcClient(pythonTransport);
        using var pythonGateway = new JsonRpcProductDataGateway(pythonClient);
        var controller = new ProductDataRequestController(sink);
        controller.SetGateway(pythonGateway);
        if (bound) controller.SetProductSidecarForwarder(sidecar);
        RoutedWebRequest request = QueryRequest("describe-go") with
        {
            Type = method,
            Payload = method == "field.settings.describe"
                ? FieldSettingsParameters()
                : method == "lookup.query"
                ? JsonSerializer.Deserialize<JsonElement>(LookupQueryPayload)
                : method == "lookup.valuePage"
                ? JsonSerializer.SerializeToElement(new { collection = "records", fieldRef = "owner.name", sourceRecordId = "record-1", schemaRevision = "s1", permissionRevision = "p1", lookupRevision = "l1", offset = 0, limit = 10 })
                : method == "relation.searchTargets"
                ? JsonSerializer.SerializeToElement(new { relationId = "records.owner" })
                : method == "relation.previewDelta"
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
            if (method == "field.settings.describe")
                Assert.IsTrue(JsonElement.DeepEquals(FieldSettingsResult(),
                    Assert.IsInstanceOfType<JsonElement>(reply.Payload)));
        }
        else
        {
            StringAssert.Contains(JsonSerializer.Serialize(reply.Payload), "BACKEND_UNAVAILABLE");
        }
    }

    [TestMethod]
    [DataRow(true, 0)]
    [DataRow(false, 0)]
    [DataRow(true, -32602)]
    [DataRow(true, -32150)]
    public async Task SnapshotGoTransportPreservesPayloadResultAndPublicError(bool withCurrentQuery, int errorCode)
    {
        JsonElement complete = JsonSerializer.Deserialize<JsonElement>(SnapshotPayload);
        JsonElement payload = withCurrentQuery ? complete : JsonSerializer.SerializeToElement(new
        {
            snapshot = complete.GetProperty("snapshot"),
        });
        JsonElement result = JsonSerializer.SerializeToElement(new
        {
            valid = false, reason = "application_write", currentDataRevision = 8,
            currentSchemaRevision = "schema-1",
        });
        JsonElement errorData = JsonSerializer.SerializeToElement(new
        {
            kind = "product_data_error", code = "query.invalid_snapshot", message = "快照无效。",
            path = "snapshot.digest", details = new { reason = "签名失效" }, retryable = false,
        });
        var sidecar = new ControlledProductSidecarForwarder((call, _) =>
            Task.FromResult<ProductSidecarForwardResult>(errorCode == 0
                ? new ProductSidecarSuccess(call.Wire.Clone(), result)
                : new ProductSidecarFailure(call.Wire.Clone(), new ProductSidecarRpcError(
                    errorCode, "failure", errorCode == -32150 ? errorData : null))));
        var pythonTransport = new CountingQueryTransport();
        await using var client = new JsonRpcClient(pythonTransport);
        using var gateway = new JsonRpcProductDataGateway(client);
        var sink = new FakeWebReplySink();
        var controller = new ProductDataRequestController(sink);
        controller.SetGateway(gateway);
        controller.SetProductSidecarForwarder(sidecar);
        RoutedWebRequest request = QueryRequest("snapshot-go") with
        {
            Type = "query.validateSnapshot", Payload = payload,
        };
        await controller.DispatchAsync(request);
        Assert.AreEqual(0, pythonTransport.WriteCount);
        ProductSidecarForwardCall call = sidecar.Calls.Single();
        Assert.AreEqual(request.Type, call.Method);
        Assert.AreEqual(request.RequestId, call.RequestId);
        Assert.IsTrue(JsonElement.DeepEquals(payload, call.Parameters));
        Assert.IsTrue(JsonElement.DeepEquals(request.Wire, call.Wire));
        FakeWebReplySink.Reply reply = sink.Replies.Single();
        Assert.AreEqual(request.RequestId, reply.RequestId);
        Assert.AreEqual(errorCode == -32602 ? "operation.failed" : request.Type, reply.Type);
        JsonElement response = JsonSerializer.SerializeToElement(reply.Payload);
        if (errorCode == 0)
            Assert.IsTrue(JsonElement.DeepEquals(result, response));
        else if (errorCode == -32602)
            Assert.AreEqual("BAD_PAYLOAD", response.GetProperty("code").GetString());
        else
        {
            JsonElement error = response.GetProperty("error");
            Assert.AreEqual("query.invalid_snapshot", error.GetProperty("code").GetString());
            Assert.AreEqual("快照无效。", error.GetProperty("message").GetString());
            Assert.AreEqual("snapshot.digest", error.GetProperty("path").GetString());
            Assert.IsTrue(JsonElement.DeepEquals(errorData.GetProperty("details"), error.GetProperty("details")));
            Assert.IsFalse(error.GetProperty("retryable").GetBoolean());
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
    [DataRow("query.page")]
    [DataRow("field.settings.describe")]
    public async Task UnavailableGoBindingIsSentOnceWithoutPythonFallback(string method)
    {
        var sink = new FakeWebReplySink();
        var sidecar = new ControlledProductSidecarForwarder((_, _) =>
            throw new BackendUnavailableException("sidecar restarting"));
        var pythonTransport = new CountingQueryTransport();
        await using var pythonClient = new JsonRpcClient(pythonTransport);
        using var pythonGateway = new JsonRpcProductDataGateway(pythonClient);
        var controller = new ProductDataRequestController(sink,
            method == "query.page" ? SelectorFor(method, "goSidecar") : ProductRpcRouteSelector.Default);
        controller.SetGateway(pythonGateway);
        controller.SetProductSidecarForwarder(sidecar);

        RoutedWebRequest request = QueryRequest("unavailable-go");
        if (method == "field.settings.describe")
            request = request with { Type = method, Payload = FieldSettingsParameters() };
        await controller.DispatchAsync(request);

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
    [DataRow("contentProfile.load", "content_profile.not_found", true)]
    [DataRow("contentProfile.load", "content_profile.field_missing", true)]
    [DataRow("contentProfile.load", "content_model.future_error", false)]
    [DataRow("query.page", "content_profile.not_found", false)]
    public async Task ContentGoErrorsReachRendererWithoutBroadeningOtherMethods(
        string method, string code, bool accepted)
    {
        JsonElement errorData = JsonSerializer.SerializeToElement(new
        {
            kind = "content_model_error", code, message = "Content profile not found.", path = "",
        });
        var sink = new FakeWebReplySink();
        var controller = new ProductDataRequestController(sink);
        controller.SetProductSidecarForwarder(FailureForwarder(
            new ProductSidecarRpcError(-32180, "Content model error", errorData)));
        RoutedWebRequest request = QueryRequest("content-error") with
        {
            Type = method,
            Payload = method == "contentProfile.load"
                ? JsonSerializer.SerializeToElement(new { tableId = "records" })
                : QueryRequest("content-error").Payload,
        };
        await controller.DispatchAsync(request);
        FakeWebReplySink.Reply reply = sink.Replies.Single();
        Assert.AreEqual(accepted ? method : "operation.failed", reply.Type);
        JsonElement payload = JsonSerializer.SerializeToElement(reply.Payload);
        if (accepted)
        {
            Assert.AreEqual(code, payload.GetProperty("error").GetProperty("code").GetString());
            Assert.AreEqual("", payload.GetProperty("error").GetProperty("path").GetString());
        }
        else
        {
            Assert.AreEqual("PRODUCT_DATA_FAILED", payload.GetProperty("code").GetString());
        }
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
    [DataRow("lookup.valuePage", -32602, "BAD_PAYLOAD")]
    [DataRow("relation.previewDelta", -32602, "BAD_PAYLOAD")]
    [DataRow("lookup.valuePage", -32030, "BACKEND_UNAVAILABLE")]
    [DataRow("relation.previewDelta", -32030, "BACKEND_UNAVAILABLE")]
    [DataRow("lookup.valuePage", -32150, "RELATION_LOOKUP_FAILED")]
    [DataRow("relation.searchTargets", -32602, "BAD_PAYLOAD")]
    [DataRow("relation.searchTargets", -32030, "BACKEND_UNAVAILABLE")]
    [DataRow("relation.searchTargets", -32150, "RELATION_LOOKUP_FAILED")]
    [DataRow("relation.previewDelta", -32150, "RELATION_LOOKUP_FAILED")]
    [DataRow("lookup.query", -32602, "BAD_PAYLOAD")]
    [DataRow("lookup.query", -32030, "BACKEND_UNAVAILABLE")]
    [DataRow("lookup.query", -32150, "RELATION_LOOKUP_FAILED")]
    public async Task RelationGoErrorPreservesExistingRendererMapping(string method, int code, string expected)
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
            Type = method,
            Payload = method == "lookup.query"
                ? JsonSerializer.Deserialize<JsonElement>(LookupQueryPayload)
                : method == "lookup.valuePage" ? JsonSerializer.SerializeToElement(new { collection = "records", fieldRef = "owner.name", sourceRecordId = "record-1", schemaRevision = "s1", permissionRevision = "p1", lookupRevision = "l1", offset = 0, limit = 10 })
                : method == "relation.searchTargets" ? JsonSerializer.SerializeToElement(new { relationId = "records.owner" })
                : JsonSerializer.SerializeToElement(new { relationId = "records.owner", sourceItemId = "record-1",
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
    [DataRow(-32602, false, "BAD_PAYLOAD")]
    [DataRow(-32030, false, "PRODUCT_DATA_FAILED")]
    [DataRow(-32150, false, "PRODUCT_DATA_FAILED")]
    [DataRow(-32150, true, "field.not_found")]
    public async Task FieldSettingsGoErrorsPreserveThePythonProductMapping(
        int code, bool publicDomainError, string expectedCode)
    {
        const string method = "field.settings.describe";
        JsonElement? data = publicDomainError ? JsonSerializer.SerializeToElement(new
        {
            kind = "product_data_error", code = "field.not_found", path = "fieldId",
            message = "field was not found", details = new { fieldId = "标题 Cafe\u0301" },
            retryable = false,
        }) : null;
        foreach (bool goOwner in new[] { false, true })
        {
            var sink = new FakeWebReplySink();
            var pythonTransport = new CountingQueryTransport(id => JsonSerializer.SerializeToElement(new
            {
                jsonrpc = "2.0", id, error = new { code, message = "failure", data },
            }));
            await using var client = new JsonRpcClient(pythonTransport);
            using var gateway = new JsonRpcProductDataGateway(client);
            var controller = new ProductDataRequestController(sink, goOwner
                ? ProductRpcRouteSelector.Default : SelectorFor(method, "pythonBff"));
            controller.SetGateway(gateway);
            var sidecar = FailureForwarder(new ProductSidecarRpcError(code, "failure", data));
            controller.SetProductSidecarForwarder(sidecar);
            await controller.DispatchAsync(QueryRequest("field-settings-failure") with
            {
                Type = method, Payload = FieldSettingsParameters(),
            });

            FakeWebReplySink.Reply reply = sink.Replies.Single();
            Assert.AreEqual(publicDomainError ? method : "operation.failed", reply.Type);
            JsonElement payload = JsonSerializer.SerializeToElement(reply.Payload);
            if (publicDomainError)
            {
                JsonElement expected = JsonSerializer.SerializeToElement(new
                {
                    error = new
                    {
                        code = expectedCode, path = "fieldId", message = "field was not found",
                        details = new { fieldId = "标题 Cafe\u0301" }, retryable = false,
                    },
                });
                Assert.IsTrue(JsonElement.DeepEquals(expected, payload));
            }
            else
            {
                Assert.AreEqual(expectedCode, payload.GetProperty("code").GetString());
            }
            Assert.AreEqual(goOwner ? 1 : 0, sidecar.CallCount);
            Assert.AreEqual(goOwner ? 0 : 1, pythonTransport.WriteCount);
        }
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

    internal static JsonElement FieldSettingsParameters() => JsonSerializer.SerializeToElement(new
    {
        tableId = "tbl_records", fieldId = "标题 Cafe\u0301 👩🏽‍💻",
    });

    internal static JsonElement FieldSettingsResult() => JsonSerializer.SerializeToElement(new
    {
        contract = "vibetable.schema.v2", tableId = "tbl_records", fieldId = "标题 Cafe\u0301 👩🏽‍💻",
        schemaRevision = "schema_1", dataRevision = 7, definition = (object?)null,
        capabilities = Array.Empty<object>(), recommendedDefaultsVersion = 1,
    });

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
