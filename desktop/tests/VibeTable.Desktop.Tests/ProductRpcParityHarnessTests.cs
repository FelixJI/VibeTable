using System.Text.Json;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductRpcParityHarnessTests
{
    [TestMethod]
    public async Task ReadParityPreservesUnicodeNullZeroAndArrayOrder()
    {
        JsonElement expected = ParseClone(
            """{"title":"表格","blank":null,"count":0,"rows":[{"id":"二"},{"id":"一"}]}""");
        var pythonTransport = PythonSuccess(expected);
        await using var pythonClient = new JsonRpcClient(pythonTransport);
        using var pythonGateway = new JsonRpcProductDataGateway(pythonClient);
        var sidecar = new ControlledProductSidecarForwarder((call, _) =>
            Task.FromResult<ProductSidecarForwardResult>(
                new ProductSidecarSuccess(call.Wire.Clone(), expected.Clone())));
        var harness = new ProductRpcParityHarness();

        await harness.AssertEquivalentAsync(
            "query.page",
            Wire(),
            QueryParameters(),
            pythonGateway,
            sidecar,
            CancellationToken.None);

        Assert.AreEqual(1, pythonTransport.WriteCount);
        Assert.AreEqual(1, sidecar.CallCount);
    }

    [TestMethod]
    public async Task ReadParityRejectsArrayOrderMismatch()
    {
        JsonElement pythonResult = ParseClone("""{"rows":[1,2]}""");
        JsonElement goResult = ParseClone("""{"rows":[2,1]}""");
        var pythonTransport = PythonSuccess(pythonResult);
        await using var pythonClient = new JsonRpcClient(pythonTransport);
        using var pythonGateway = new JsonRpcProductDataGateway(pythonClient);
        var sidecar = new ControlledProductSidecarForwarder((call, _) =>
            Task.FromResult<ProductSidecarForwardResult>(
                new ProductSidecarSuccess(call.Wire.Clone(), goResult.Clone())));
        var harness = new ProductRpcParityHarness();

        InvalidOperationException error =
            await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
                harness.AssertEquivalentAsync(
                    "query.page",
                    Wire(),
                    QueryParameters(),
                    pythonGateway,
                    sidecar,
                    CancellationToken.None));

        Assert.AreEqual("Product RPC parity mismatch.", error.Message);
    }

    [TestMethod]
    public async Task ReadParityComparesExactRemoteErrors()
    {
        JsonElement data = ParseClone("""{"path":"查询","actual":0}""");
        var pythonTransport = PythonError(
            -32602,
            "Invalid params.",
            data);
        await using var pythonClient = new JsonRpcClient(pythonTransport);
        using var pythonGateway = new JsonRpcProductDataGateway(pythonClient);
        var sidecar = new ControlledProductSidecarForwarder((call, _) =>
            Task.FromResult<ProductSidecarForwardResult>(
                new ProductSidecarFailure(
                    call.Wire.Clone(),
                    new ProductSidecarRpcError(
                        -32602,
                        "Invalid params.",
                        data.Clone()))));
        var harness = new ProductRpcParityHarness();

        await harness.AssertEquivalentAsync(
            "query.page",
            Wire(),
            QueryParameters(),
            pythonGateway,
            sidecar,
            CancellationToken.None);

        Assert.AreEqual(1, pythonTransport.WriteCount);
        Assert.AreEqual(1, sidecar.CallCount);
    }

    [TestMethod]
    public async Task HarnessRejectsMutationBeforeEitherTransportIsCalled()
    {
        var pythonTransport = PythonSuccess(
            ParseClone("""{"ok":true}"""));
        await using var pythonClient = new JsonRpcClient(pythonTransport);
        using var pythonGateway = new JsonRpcProductDataGateway(pythonClient);
        var sidecar = new ControlledProductSidecarForwarder((call, _) =>
            Task.FromResult<ProductSidecarForwardResult>(
                new ProductSidecarSuccess(call.Wire.Clone(), ParseClone("{}"))));
        var harness = new ProductRpcParityHarness();

        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            harness.AssertEquivalentAsync(
                "mutation.apply",
                Wire(),
                ParseClone("""{"tableId":"tbl_records","operations":[]}"""),
                pythonGateway,
                sidecar,
                CancellationToken.None));

        Assert.AreEqual(0, pythonTransport.WriteCount);
        Assert.AreEqual(0, sidecar.CallCount);
    }

    private static JsonElement Wire()
        => ParseClone(
            """{"scope":"workspace","workspaceId":"11111111-1111-4111-8111-111111111111","sessionEpoch":7,"operationId":"22222222-2222-4222-8222-222222222222","sequence":0}""");

    private static JsonElement QueryParameters()
        => ParseClone(
            """{"tableId":"tbl_records","query":{"filters":[],"sorts":[],"offset":0,"limit":100}}""");

    private static JsonElement ParseClone(string json)
    {
        using JsonDocument document = JsonDocument.Parse(json);
        return document.RootElement.Clone();
    }

    private static CountingQueryTransport PythonSuccess(JsonElement result)
        => new(id => JsonSerializer.SerializeToElement(new
        {
            jsonrpc = "2.0",
            id,
            result,
        }));

    private static CountingQueryTransport PythonError(
        int code,
        string message,
        JsonElement? data)
        => new(id => JsonSerializer.SerializeToElement(new
        {
            jsonrpc = "2.0",
            id,
            error = new { code, message, data },
        }));
}

/// <summary>
/// Test-only migration oracle. Production routing never references this type
/// and therefore cannot shadow-send a Product RPC to both transports.
/// </summary>
internal sealed class ProductRpcParityHarness
{
    internal async Task AssertEquivalentAsync(
        string method,
        JsonElement wire,
        JsonElement parameters,
        IProductDataRpcGateway pythonGateway,
        IProductSidecarRpcForwarder sidecarForwarder,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(pythonGateway);
        ArgumentNullException.ThrowIfNull(sidecarForwarder);
        if (!ProductDataRpcRegistry.TryGet(method, out ProductDataRpcEndpoint endpoint)
            || endpoint.MutatesWorkspace
            || endpoint.CapabilityCatalog != ProductRpcCapabilityCatalog.Product
            || !ProductRpcCapabilityManifest.Default.TryGet(
                method,
                out ProductRpcCapability capability)
            || capability.Effect != "read")
        {
            throw new InvalidOperationException(
                "Product RPC parity is restricted to generated read methods.");
        }

        ParityOutcome python = await InvokePythonAsync(
            endpoint,
            pythonGateway,
            parameters,
            cancellationToken);
        ProductSidecarForwardResult forwarded =
            await sidecarForwarder.ForwardAsync(
                "parity-probe",
                method,
                wire,
                parameters,
                cancellationToken);
        ParityOutcome sidecar = forwarded switch
        {
            ProductSidecarSuccess success => new(success.Result, null),
            ProductSidecarFailure failure => new(null, failure.Error),
            _ => throw new InvalidOperationException("Product RPC parity mismatch."),
        };
        if (!Equivalent(python, sidecar))
            throw new InvalidOperationException("Product RPC parity mismatch.");
    }

    private static async Task<ParityOutcome> InvokePythonAsync(
        ProductDataRpcEndpoint endpoint,
        IProductDataRpcGateway gateway,
        JsonElement parameters,
        CancellationToken cancellationToken)
    {
        try
        {
            JsonElement result = await endpoint.InvokeAsync(
                gateway,
                parameters,
                cancellationToken);
            return new ParityOutcome(result, null);
        }
        catch (RpcRemoteException error)
        {
            return new ParityOutcome(
                null,
                new ProductSidecarRpcError(
                    error.Code,
                    error.Message,
                    error.ErrorData));
        }
    }

    private static bool Equivalent(ParityOutcome left, ParityOutcome right)
    {
        if (left.Result is JsonElement leftResult
            && right.Result is JsonElement rightResult)
            return JsonElement.DeepEquals(leftResult, rightResult);
        if (left.Error is not ProductSidecarRpcError leftError
            || right.Error is not ProductSidecarRpcError rightError)
            return false;
        return leftError.Code == rightError.Code
            && leftError.Message == rightError.Message
            && Equivalent(leftError.Data, rightError.Data);
    }

    private static bool Equivalent(JsonElement? left, JsonElement? right)
        => left is null
            ? right is null
            : right is JsonElement rightValue
                && JsonElement.DeepEquals(left.Value, rightValue);

    private sealed record ParityOutcome(
        JsonElement? Result,
        ProductSidecarRpcError? Error);
}
