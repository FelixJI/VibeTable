using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class RelationInspectionRoutingTests
{
    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task DefaultGoInspectionPreservesSuccessAndPublicRevisionFailure(bool failed)
    {
        const string method = "relation.inspectPair";
        Assert.IsTrue(ProductDataRequestController.Handles(method));
        Assert.IsTrue(ProductDataRpcRegistry.TryGet(method, out var endpoint));
        Assert.IsFalse(endpoint.MutatesWorkspace);
        var sink = new FakeWebReplySink();
        var controller = new ProductDataRequestController(sink);
        var result = JsonSerializer.SerializeToElement(new { finished = true, complete = true });
        var data = JsonSerializer.SerializeToElement(new {
            kind = "product_data_error", code = "relation.inspect.revision_changed",
            message = "检查期间数据已变化", path = (string?)null, details = new { }, retryable = false,
        });
        var forwarder = new ControlledProductSidecarForwarder((call, _) =>
            Task.FromResult<ProductSidecarForwardResult>(failed
                ? new ProductSidecarFailure(call.Wire, new ProductSidecarRpcError(-32150, "failure", data))
                : new ProductSidecarSuccess(call.Wire, result)));
        controller.SetProductSidecarForwarder(forwarder);
        // No Python gateway is bound; both outcomes must use the selected Go route.
        await controller.DispatchAsync(Request("""{"tableId":"orders","fieldId":"fld_link","limit":100}"""));
        Assert.AreEqual(1, forwarder.CallCount);
        Assert.AreEqual(method, forwarder.Calls.Single().Method);
        var reply = sink.Replies.Single();
        Assert.AreEqual(method, reply.Type);
        var payload = JsonSerializer.SerializeToElement(reply.Payload);
        if (failed)
            Assert.AreEqual("relation.inspect.revision_changed", payload.GetProperty("error").GetProperty("code").GetString());
        else
            Assert.IsTrue(JsonElement.DeepEquals(result, payload));
    }

    [TestMethod]
    [DataRow("{}")]
    [DataRow("{\"tableId\":\"orders\",\"fieldId\":\"fld_link\",\"limit\":0}")]
    [DataRow("{\"tableId\":\"orders\",\"fieldId\":\"fld_link\",\"password\":\"secret\"}")]
    [DataRow("{\"tableId\":\"orders\",\"fieldId\":\"fld_link\",\"cursor\":[]}")]
    public async Task InvalidInspectionPayloadNeverReachesGo(string raw)
    {
        var sink = new FakeWebReplySink();
        var controller = new ProductDataRequestController(sink);
        var forwarder = new ControlledProductSidecarForwarder((_, _) => throw new AssertFailedException("invalid inspection forwarded"));
        controller.SetProductSidecarForwarder(forwarder);
        await controller.DispatchAsync(Request(raw));
        Assert.AreEqual(0, forwarder.CallCount);
        var reply = sink.Replies.Single();
        Assert.AreEqual("operation.failed", reply.Type);
        Assert.AreEqual("BAD_PAYLOAD", JsonSerializer.SerializeToElement(reply.Payload).GetProperty("code").GetString());
    }

    private static RoutedWebRequest Request(string raw)
    {
        using var document = JsonDocument.Parse(raw);
        var wire = JsonSerializer.SerializeToElement(new {
            scope = "workspace", workspaceId = "11111111-1111-4111-8111-111111111111",
            sessionEpoch = 7, operationId = "22222222-2222-4222-8222-222222222222", sequence = 0,
        });
        return new RoutedWebRequest("relation.inspectPair", "inspection", document.RootElement.Clone(), string.Empty, Wire: wire);
    }
}
