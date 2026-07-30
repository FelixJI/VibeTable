using System.Net;
using System.Text;
using System.Text.Json;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceV2HttpGatewayTests
{
    private const string WorkspaceId =
        "11111111-1111-4111-8111-111111111111";
    private const string ClaimId =
        "22222222-2222-4222-8222-222222222222";

    [TestMethod]
    public async Task CapabilitiesAreReadFromAuthenticatedPrivateEndpoint()
    {
        var handler = new RecordingHandler(request =>
        {
            Assert.AreEqual(
                "private-secret",
                request.Headers.GetValues("X-VibeTable-Session").Single());
            Assert.AreEqual(
                "/api/vibetable/v2/capabilities",
                request.RequestUri!.AbsolutePath);
            return Json(
                $$"""
                {
                  "contractVersion":"2.0",
                  "workspaceId":"{{WorkspaceId}}",
                  "sessionEpoch":7,
                  "fenceEpoch":3,
                  "claimId":"{{ClaimId}}",
                  "rpcMethods":["retention.get","snapshot.list"],
                  "registrations":[]
                }
                """);
        });
        using var gateway = Gateway(handler);

        WorkspaceV2SidecarCapabilities capabilities =
            await gateway.GetCapabilitiesAsync(CancellationToken.None);

        Assert.AreEqual(WorkspaceId, capabilities.WorkspaceId);
        Assert.AreEqual<ulong>(7, capabilities.SessionEpoch);
        CollectionAssert.AreEqual(
            new[] { "retention.get", "snapshot.list" },
            capabilities.RpcMethods.ToArray());
    }

    [TestMethod]
    public async Task ForwardPreservesWireAndCarriesBoundPathGrantPrivately()
    {
        Guid operationId = Guid.NewGuid();
        using JsonDocument wireDocument = JsonDocument.Parse(
            $$"""
            {"scope":"workspace","workspaceId":"{{WorkspaceId}}","sessionEpoch":7,
             "operationId":"{{operationId:D}}","sequence":4}
            """);
        using JsonDocument paramsDocument = JsonDocument.Parse(
            """{"snapshotId":"snap-1","pathGrant":"host-path-grant://grant-1","encryption":"none"}""");
        string path = Path.Combine(Path.GetTempPath(), "export.vtsnapshot");
        var binding = new WorkspaceSidecarPathGrant(
            "host-path-grant://grant-1",
            "snapshot.export",
            operationId,
            "snapshot-export",
            path);
        var handler = new RecordingHandler(request =>
        {
            string encoded = request.Headers
                .GetValues("X-VibeTable-Path-Grant")
                .Single();
            string json = DecodeBase64Url(encoded);
            using JsonDocument grant = JsonDocument.Parse(json);
            Assert.AreEqual(
                Path.GetFullPath(path),
                grant.RootElement.GetProperty("path").GetString());
            Assert.AreEqual(
                operationId.ToString("D"),
                grant.RootElement.GetProperty("operationId").GetString());
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement requestWire = body.RootElement.GetProperty("wire");
            Assert.IsTrue(
                JsonElement.DeepEquals(
                    wireDocument.RootElement,
                    requestWire));
            return Json(
                "{\"jsonrpc\":\"2.0\",\"id\":\"request-1\",\"wire\":"
                + requestWire.GetRawText()
                + ",\"result\":{\"displayName\":\"export.vtsnapshot\","
                + "\"sha256\":\"abc\"}}");
        });
        using var gateway = Gateway(handler);

        WorkspaceV2ForwardResult result = await gateway.ForwardAsync(
            "request-1",
            "snapshot.export",
            wireDocument.RootElement,
            paramsDocument.RootElement,
            binding,
            CancellationToken.None);

        Assert.IsNull(result.Error);
        Assert.AreEqual(
            "export.vtsnapshot",
            result.Result!.Value.GetProperty("displayName").GetString());
        Assert.IsTrue(
            JsonElement.DeepEquals(
                wireDocument.RootElement,
                result.Wire));
    }

    [TestMethod]
    public async Task ForwardRejectsAResponseFromAnotherWireScope()
    {
        using JsonDocument wire = JsonDocument.Parse(
            $$"""
            {"scope":"workspace","workspaceId":"{{WorkspaceId}}","sessionEpoch":7,
             "operationId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","sequence":4}
            """);
        using JsonDocument parameters = JsonDocument.Parse("{}");
        var handler = new RecordingHandler(_ => Json(
            """
            {"jsonrpc":"2.0","id":"request-2",
             "wire":{"scope":"global","operationId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","sequence":4},
             "result":{}}
            """));
        using var gateway = Gateway(handler);

        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.ForwardAsync(
                "request-2",
                "retention.get",
                wire.RootElement,
                parameters.RootElement,
                null,
                CancellationToken.None));
    }

    [TestMethod]
    public async Task DrainUsesAuthenticatedHostOnlyEndpointAndParsesWatermark()
    {
        var handler = new RecordingHandler(request =>
        {
            Assert.AreEqual(
                "/api/vibetable/v2/workspace/drain",
                request.RequestUri!.AbsolutePath);
            Assert.AreEqual(
                "private-secret",
                request.Headers.GetValues("X-VibeTable-Session").Single());
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            Assert.AreEqual(
                30000,
                body.RootElement.GetProperty("deadlineMs").GetInt64());
            Assert.AreEqual(1, body.RootElement.EnumerateObject().Count());
            return Json(
                """
                {"sourceEpoch":7,"sourceSequence":42,"chainHash":"sha256:abc"}
                """);
        });
        using var gateway = Gateway(handler);

        WorkspaceDrainHighWatermark watermark = await gateway.DrainAsync(
            TimeSpan.FromSeconds(30),
            CancellationToken.None);

        Assert.AreEqual<ulong>(7, watermark.SourceEpoch);
        Assert.AreEqual<ulong>(42, watermark.SourceSequence);
        Assert.AreEqual("sha256:abc", watermark.ChainHash);
    }

    private static WorkspaceV2HttpGateway Gateway(
        HttpMessageHandler handler)
        => new(
            () => new PocketBaseAdminContext(
                new Uri(
                    "http://127.0.0.1:43125/api/vibetable/v1/admin/bootstrap"),
                new Uri("http://127.0.0.1:43125/"),
                "X-VibeTable-Session",
                "private-secret"),
            handler);

    private static HttpResponseMessage Json(string body)
        => new(HttpStatusCode.OK)
        {
            Content = new StringContent(
                body,
                Encoding.UTF8,
                "application/json"),
        };

    private static string DecodeBase64Url(string value)
    {
        string padded = value.Replace('-', '+').Replace('_', '/');
        padded += new string('=', (4 - padded.Length % 4) % 4);
        return Encoding.UTF8.GetString(Convert.FromBase64String(padded));
    }

    private sealed class RecordingHandler(
        Func<HttpRequestMessage, HttpResponseMessage> responder)
        : HttpMessageHandler
    {
        private readonly Func<HttpRequestMessage, Task<HttpResponseMessage>>
            _responder = request => Task.FromResult(responder(request));

        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request,
            CancellationToken cancellationToken)
            => _responder(request);
    }
}
