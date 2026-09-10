using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class GridPresentationRouterTests
{
    [TestMethod]
    [DataRow("gridState.get")]
    [DataRow("gridState.save")]
    public void HostGridRouteRequiresWorkspaceScopeAndPreservesIt(string method)
    {
        List<RoutedWebRequest> requests = [];
        var policy = ProductRpcCapabilityManifest.CreateForTests(new ProductRpcCapability(
            method, "workspace", "rendererPublic", "grid.state", "wpfHost", "read"));
        var router = new WebMessageRouter(requests.Add, WorkspaceRpcCapabilityManifest.Default, policy)
        { IsReady = true };
        var missing = router.Route(JsonSerializer.Serialize(new
        { type = method, requestId = "missing", payload = new { table = "orders" } }));
        Assert.AreEqual("BAD_WORKSPACE_SCOPE", missing?.Payload?.Code);
        Guid workspaceId = Guid.NewGuid();
        var accepted = router.Route(JsonSerializer.Serialize(new
        {
            type = method,
            requestId = "current",
            payload = new { table = "orders" },
            scope = new { scope = "workspace", workspaceId, sessionEpoch = 3,
                sequence = 0, operationId = Guid.NewGuid() },
        }));
        Assert.IsNull(accepted);
        Assert.HasCount(1, requests);
        Assert.AreEqual(workspaceId, requests[0].Scope?.WorkspaceId);
    }

    [TestMethod]
    [DataRow("pythonBff", "workspace", "rendererPublic")]
    [DataRow("goSidecar", "workspace", "rendererPublic")]
    [DataRow("wpfHost", "global", "rendererPublic")]
    [DataRow("wpfHost", "workspace", "hostOnly")]
    public void HostGridRouteRejectsWrongCapability(string owner, string scope, string audience)
    {
        List<RoutedWebRequest> requests = [];
        var policy = ProductRpcCapabilityManifest.CreateForTests(new ProductRpcCapability(
            "gridState.get", scope, audience, "grid.state", owner, "read"));
        var router = new WebMessageRouter(requests.Add, WorkspaceRpcCapabilityManifest.Default, policy)
        { IsReady = true };
        var reply = router.Route("""{"type":"gridState.get","requestId":"get","payload":{"table":"orders"}}""");
        Assert.AreEqual("CAPABILITY_NOT_PUBLIC", reply?.Payload?.Code);
        Assert.HasCount(0, requests);
    }
}
