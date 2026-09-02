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
    [DataRow(false)]
    [DataRow(true)]
    public async Task RetiredGenerationCannotPublishALateReply(bool remoteError)
    {
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.BeforeReply = (_, _) =>
        {
            fixture.Current = false;
            return Task.CompletedTask;
        };
        fixture.Http.Error = remoteError ? Json("""{"code":-32602,"message":"Invalid params"}""") : null;
        using JsonRpcProductDataGateway gateway = fixture.Gateway();

        await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() => gateway.ListTablesAsync(
            Json("{}"), CancellationToken.None));

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
        await using var fixture = await HostFixture.OpenAsync();
        using JsonRpcProductDataGateway gateway = fixture.Gateway();
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.DescribeFieldSettingsAsync(Json("""{"tableId":"orders"}"""), CancellationToken.None));
        Assert.AreEqual(1, fixture.Python.WriteCount);
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
        using JsonRpcProductDataGateway gateway = fixture.Gateway();

        HistoryPage result = await gateway.ReadHistoryAsync(
            new ReadChangeSetsParams("orders", null, 25, 0), CancellationToken.None);

        Assert.AreEqual("orders", result.Collection);
        JsonElement parameters = fixture.Http.Calls.Single().GetProperty("params");
        Assert.AreEqual(JsonValueKind.Null, parameters.GetProperty("itemId").ValueKind);
        Assert.AreEqual(JsonValueKind.Null, parameters.GetProperty("actions").ValueKind);
        Assert.AreEqual(25, parameters.GetProperty("limit").GetInt32());
        Assert.AreEqual(0, fixture.Python.WriteCount);
    }

    private sealed class HostFixture : IAsyncDisposable
    {
        private readonly string _root = Path.Combine(Path.GetTempPath(),
            "vibetable-host-rpc-" + Guid.NewGuid().ToString("N"));
        private readonly WorkspaceSessionManager _sessions;
        private readonly WorkspaceSessionEnvelopeFilter _leases;
        private readonly JsonRpcClient _client;
        private ProductSidecarGenerationSnapshot _snapshot = null!;

        private HostFixture()
        {
            _sessions = new WorkspaceSessionManager(new WorkspaceRegistry(_root), new RuntimeFactory());
            _leases = new WorkspaceSessionEnvelopeFilter(_sessions);
            _sessions.SetRequestDrainHook(_leases);
            _client = new JsonRpcClient(Python);
        }

        internal CountingQueryTransport Python { get; } = new();
        internal ProductHttpPeer Http { get; private set; } = null!;
        internal WorkspaceSessionV2 Session { get; private set; } = null!;
        internal bool Current { get; set; } = true;
        internal Task CloseAsync() => _sessions.CloseAsync("host-rpc-test");

        internal static async Task<HostFixture> OpenAsync()
        {
            var fixture = new HostFixture();
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
                [new("history.read", "workspace"), new("schema.getTable", "workspace"), new("schema.list", "workspace")]);
            fixture.Http = new ProductHttpPeer(fixture._snapshot);
            return fixture;
        }

        internal JsonRpcProductDataGateway Gateway() => new(
            new HostProductRpcInvoker(_client, _snapshot, _leases,
                action => Current && action(),
                new ProductRpcRouteSelector(ProductRpcCapabilityManifest.CreateForTests(
                    new ProductRpcCapability("history.read", "workspace", "hostOnly",
                        "history.read", "goSidecar", "read"),
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
