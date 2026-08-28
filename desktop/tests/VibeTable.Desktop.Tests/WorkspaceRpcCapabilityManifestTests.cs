using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceRpcCapabilityManifestTests
{
    [TestMethod]
    public void RouterUsesInjectedWorkspaceRpcCapabilityManifest()
    {
        var dispatched = new List<RoutedWebRequest>();
        WorkspaceRpcCapabilityManifest manifest =
            WorkspaceRpcCapabilityManifest.CreateForTests(
                new WorkspaceRpcCapability(
                    "test.public",
                    "global",
                    "test",
                    WorkspaceRpcAudience.RendererPublic),
                new WorkspaceRpcCapability(
                    "workspace.list",
                    "global",
                    "test",
                    WorkspaceRpcAudience.RendererInternal));
        var router = new WebMessageRouter(dispatched.Add, manifest)
        {
            IsReady = true,
        };

        HostReplyMessage? accepted = router.Route(JsonSerializer.Serialize(new
        {
            type = "workspace.v2.request",
            requestId = "test-public",
            wire = new
            {
                scope = "global",
                operationId = Guid.NewGuid(),
                sequence = 1,
            },
            payload = new { method = "test.public", @params = new { } },
        }));
        HostReplyMessage? internalReply = router.Route(JsonSerializer.Serialize(new
        {
            type = "workspace.v2.request",
            requestId = "test-internal",
            payload = new { method = "workspace.list", @params = new { } },
        }));
        HostReplyMessage? absentDefault = router.Route(JsonSerializer.Serialize(new
        {
            type = "workspace.v2.request",
            requestId = "test-absent",
            payload = new { method = "snapshot.list", @params = new { } },
        }));

        Assert.IsNull(accepted);
        Assert.HasCount(1, dispatched);
        Assert.AreEqual("test.public", dispatched[0].V2Method);
        Assert.AreEqual("CAPABILITY_NOT_PUBLIC", internalReply?.Payload?.Code);
        Assert.AreEqual("UNKNOWN_V2_METHOD", absentDefault?.Payload?.Code);
    }

    [TestMethod]
    public void RouterRejectsGeneratedHostOnlyMethods()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(dispatched.Add) { IsReady = true };

        foreach (string method in new[]
        {
            "fileHistory.materializeDiffPair",
            "fileHistory.assertEffectiveRevision",
        })
        {
            HostReplyMessage? reply = router.Route(JsonSerializer.Serialize(new
            {
                type = "workspace.v2.request",
                requestId = $"host-only-{method}",
                payload = new { method, @params = new { } },
            }));

            Assert.AreEqual("CAPABILITY_NOT_PUBLIC", reply?.Payload?.Code, method);
        }

        Assert.HasCount(0, dispatched);
    }

    public static IEnumerable<object[]> InvalidManifests()
    {
        yield return ["""{"contractVersion":"2.0"}"""];
        yield return ["""{"contractVersion":"2.0","methods":[],"unknown":true}"""];
        yield return [
            """{"contractVersion":"2.0","contractVersion":"2.0","methods":[]}"""
        ];
        yield return [
            """
            {
              "contractVersion":"2.0",
              "methods":[
                {"method":"duplicate","scope":"global","capabilityId":"test","audience":"rendererPublic"},
                {"method":"duplicate","scope":"global","capabilityId":"test","audience":"rendererPublic"}
              ]
            }
            """
        ];
        yield return [
            """
            {
              "contractVersion":"2.0",
              "methods":[
                {"method":"stale.scope","scope":"session","capabilityId":"test","audience":"rendererPublic"}
              ]
            }
            """
        ];
        yield return [
            """
            {
              "contractVersion":"2.0",
              "methods":[
                {"method":"unknown.audience","scope":"global","capabilityId":"test","audience":"rendererTrusted"}
              ]
            }
            """
        ];
    }

    [TestMethod]
    [DynamicData(nameof(InvalidManifests))]
    public void ParserRejectsMalformedManifest(string json)
    {
        Assert.ThrowsExactly<JsonException>(
            () => WorkspaceRpcCapabilityManifest.Parse(json));
    }
}
