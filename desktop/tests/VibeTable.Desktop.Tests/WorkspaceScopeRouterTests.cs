using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceScopeRouterTests
{
    [TestMethod]
    public void ValidWorkspaceScopeIsProjectedOntoRoutedRequest()
    {
        RoutedWebRequest? dispatched = null;
        var workspaceId = Guid.NewGuid();
        var operationId = Guid.NewGuid();
        var router = new WebMessageRouter(request => dispatched = request)
        {
            IsReady = true,
        };

        HostReplyMessage? reply = router.Route(
            $$"""
            {
              "type": "query.page",
              "requestId": "query-1",
              "scope": {
                "scope": "workspace",
                "workspaceId": "{{workspaceId:D}}",
                "sessionEpoch": 7,
                "operationId": "{{operationId:D}}",
                "sequence": 11
              },
              "payload": {
                "tableId": "tbl_records",
                "query": {"filters":[],"sorts":[],"offset":0,"limit":100}
              }
            }
            """);

        Assert.IsNull(reply);
        Assert.IsNotNull(dispatched?.Scope);
        Assert.AreEqual(workspaceId, dispatched.Scope.WorkspaceId);
        Assert.AreEqual<ulong>(7, dispatched.Scope.SessionEpoch);
        Assert.AreEqual(operationId, dispatched.Scope.OperationId);
        Assert.AreEqual<ulong>(11, dispatched.Scope.Sequence);
    }

    [TestMethod]
    public void UnknownWorkspaceScopeFieldIsRejectedBeforeDispatch()
    {
        var dispatchCount = 0;
        var router = new WebMessageRouter(_ => dispatchCount++)
        {
            IsReady = true,
        };

        HostReplyMessage? reply = router.Route(
            $$"""
            {
              "type": "query.page",
              "requestId": "query-2",
              "scope": {
                "scope": "workspace",
                "workspaceId": "{{Guid.NewGuid():D}}",
                "sessionEpoch": 7,
                "operationId": "{{Guid.NewGuid():D}}",
                "sequence": 11,
                "unexpected": true
              },
              "payload": {}
            }
            """);

        Assert.AreEqual(0, dispatchCount);
        Assert.IsNotNull(reply);
        Assert.AreEqual("operation.failed", reply.Type);
        Assert.AreEqual("BAD_WORKSPACE_SCOPE", reply.Payload?.Code);
    }
}
