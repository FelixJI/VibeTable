using System.Net;
using System.Net.Http;
using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class QueryCursorOwnerCompositionTests
{
    private const string Cursor = "opaque/游标 +==";

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task DefaultHostOwnerContinuesPythonSelectionOnGoWithoutFallback(bool fetchFails)
    {
        // Script only the transport peers: the typed gateway, default policy,
        // Host invoker and Product HTTP envelope handling are production code.
        var leases = new EpochLeases();
        var snapshot = new ProductSidecarGenerationSnapshot(leases, 1,
            new PocketBaseAdminContext(new Uri("http://127.0.0.1:12345/bootstrap"),
                new Uri("http://127.0.0.1:12345/"), "X-VibeTable-Session", "cursor-test"),
            new ProductSidecarIdentity("11111111-1111-4111-8111-111111111111", 7, 3,
                "22222222-2222-4222-8222-222222222222"),
            ProductRpcCapabilityManifest.Default.GetProductSidecarRegistrations());
        var python = new SelectionTransport();
        await using var client = new JsonRpcClient(python);
        using var http = new CursorHttpPeer(snapshot, fetchFails);
        using var gateway = new JsonRpcProductDataGateway(
            new HostProductRpcInvoker(client, snapshot, leases, action => action(), handler: http));
        JsonElement parameters = Json("""{"tableId":"orders","query":{"offset":0,"limit":1}}""");

        JsonElement selection = await gateway.OpenSelectionProjectionAsync(parameters, CancellationToken.None);
        string cursor = selection.GetProperty("cursorWindow").GetProperty("nextCursor").GetString()!;

        Assert.AreEqual(Cursor, cursor);
        Assert.AreEqual("query.selectionOpen", python.Calls.Single().GetProperty("method").GetString());
        Assert.IsTrue(JsonElement.DeepEquals(parameters, python.Calls.Single().GetProperty("params")));
        Assert.AreEqual(0, http.Handshakes);
        Assert.AreEqual(0, http.Calls.Count);

        JsonElement fetch = JsonSerializer.SerializeToElement(new { cursor });
        if (fetchFails)
        {
            RpcRemoteException error = await Assert.ThrowsExactlyAsync<RpcRemoteException>(() =>
                gateway.FetchQueryCursorAsync(fetch, CancellationToken.None));
            Assert.AreEqual(-32150, error.Code);
            Assert.AreEqual("query.cursor_stale", error.ErrorData!.Value.GetProperty("code").GetString());
        }
        else
        {
            JsonElement window = await gateway.FetchQueryCursorAsync(fetch, CancellationToken.None);
            Assert.AreEqual("row-2", window.GetProperty("rows")[0].GetProperty("id").GetString());
            Assert.IsFalse(window.GetProperty("rows")[0].GetProperty("value").GetBoolean());
            Assert.IsFalse(window.GetProperty("hasMore").GetBoolean());
            Assert.AreEqual(JsonValueKind.Null, window.GetProperty("nextCursor").ValueKind);
            Assert.IsTrue(window.TryGetProperty("querySnapshot", out _));
        }

        Assert.AreEqual(1, python.Calls.Count, "Go success or failure must never invoke Python cursorFetch.");
        Assert.AreEqual(1, http.Handshakes);
        JsonElement call = http.Calls.Single();
        Assert.AreEqual("query.cursorFetch", call.GetProperty("method").GetString());
        Assert.IsTrue(JsonElement.DeepEquals(fetch, call.GetProperty("params")));
        JsonElement wire = call.GetProperty("wire");
        Assert.AreEqual(snapshot.Identity.WorkspaceId, wire.GetProperty("workspaceId").GetString());
        Assert.AreEqual(snapshot.Identity.SessionEpoch, wire.GetProperty("sessionEpoch").GetUInt64());
        Assert.AreEqual(2UL, wire.GetProperty("sequence").GetUInt64());
        Assert.IsTrue(Guid.TryParse(wire.GetProperty("operationId").GetString(), out _));
        Assert.AreEqual(3, leases.Captured, "Selection, fetch and the HTTP handshake each own a lease.");
        Assert.AreEqual(leases.Captured, leases.Completed);
    }

    private static JsonElement Json(string text)
    {
        using JsonDocument document = JsonDocument.Parse(text);
        return document.RootElement.Clone();
    }

    private static JsonElement Window(bool terminal) => JsonSerializer.SerializeToElement(new
    {
        rows = new[] { new { id = terminal ? "row-2" : "row-1", value = false } },
        nextCursor = terminal ? null : Cursor,
        hasMore = !terminal,
        filteredRows = 2,
        totalRows = 2,
        querySnapshot = new
        {
            snapshotId = "scripted-snapshot", digest = "scripted-digest", databaseId = "database-1",
            table = "orders", schemaRevision = "schema_1", dataRevision = 0,
            normalizedQuery = new { offset = 0, limit = 1 },
        },
    });

    private sealed class SelectionTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _responses = Channel.CreateUnbounded<JsonElement?>();
        internal List<JsonElement> Calls { get; } = [];

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _responses.Reader.ReadAsync(cancellationToken).AsTask();

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            cancellationToken.ThrowIfCancellationRequested();
            JsonElement call = Json(line);
            Calls.Add(call);
            Assert.AreEqual("query.selectionOpen", call.GetProperty("method").GetString(),
                "Only selectionOpen remains on the Python transport.");
            _responses.Writer.TryWrite(JsonSerializer.SerializeToElement(new
            {
                jsonrpc = "2.0", id = call.GetProperty("id"),
                result = new { schemaSnapshot = new { tableId = "orders" }, cursorWindow = Window(false) },
            }));
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync()
        {
            _responses.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }

    private sealed class CursorHttpPeer(ProductSidecarGenerationSnapshot snapshot, bool fetchFails)
        : HttpMessageHandler
    {
        internal int Handshakes { get; private set; }
        internal List<JsonElement> Calls { get; } = [];

        protected override async Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request, CancellationToken cancellationToken)
        {
            if (request.Method == HttpMethod.Get)
            {
                Handshakes++;
                Assert.AreEqual("/api/vibetable/v2/product/capabilities", request.RequestUri!.AbsolutePath);
                return Reply(JsonSerializer.SerializeToElement(new
                {
                    contractVersion = "2.0", workspaceId = snapshot.Identity.WorkspaceId,
                    sessionEpoch = snapshot.Identity.SessionEpoch, fenceEpoch = snapshot.Identity.FenceEpoch,
                    claimId = snapshot.Identity.ClaimId,
                    rpcMethods = snapshot.Registrations.Select(item => item.Method),
                    registrations = snapshot.Registrations.Select(item => new { method = item.Method, scope = item.Scope }),
                }));
            }
            Assert.AreEqual(HttpMethod.Post, request.Method);
            Assert.AreEqual("/api/vibetable/v2/product/rpc", request.RequestUri!.AbsolutePath);
            JsonElement call = Json(await request.Content!.ReadAsStringAsync(cancellationToken));
            Calls.Add(call);
            if (fetchFails)
            {
                return Reply(JsonSerializer.SerializeToElement(new
                {
                    jsonrpc = "2.0", id = call.GetProperty("id"), wire = call.GetProperty("wire"),
                    error = new
                    {
                        code = -32150, message = "Product data error",
                        data = new { kind = "product_data_error", code = "query.cursor_stale", path = "cursor", message = "Cursor is stale", details = new { }, retryable = false },
                    },
                }));
            }
            return Reply(JsonSerializer.SerializeToElement(new
            {
                jsonrpc = "2.0", id = call.GetProperty("id"), wire = call.GetProperty("wire"), result = Window(true),
            }));
        }

        private static HttpResponseMessage Reply(JsonElement body) => new(HttpStatusCode.OK)
        {
            Content = new StringContent(body.GetRawText()),
        };
    }

    private sealed class EpochLeases : IWorkspaceHostEpochLeaseSource
    {
        internal int Captured { get; private set; }
        internal int Completed { get; private set; }

        public bool TryCaptureHost(Guid workspaceId, ulong sessionEpoch, Guid operationId,
            out WorkspaceRequestEpochLease? lease)
        {
            lease = new WorkspaceRequestEpochLease(new WorkspaceWireScope
            {
                Scope = "workspace", WorkspaceId = workspaceId, SessionEpoch = sessionEpoch,
                OperationId = operationId, Sequence = (ulong)++Captured,
            }, CancellationToken.None, () => Completed++);
            return true;
        }

        public bool IsCurrent(WorkspaceRequestEpochLease? lease) => lease is not null;
    }
}
