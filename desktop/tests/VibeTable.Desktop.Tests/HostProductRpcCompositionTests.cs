using System.Net;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
[DoNotParallelize]
public sealed class HostProductRpcCompositionTests
{
    [TestMethod]
    public async Task HealthReaderUsesExpectedEpochLeaseAndSelectedProductOwner()
    {
        await using var fixture = await Fixture.OpenAsync();
        var reader = new CurrentRuntimeUpdateWorkspaceSchemaReader(fixture.Factory, fixture.Leases, fixture.Http);
        Assert.AreEqual(1, await reader.ReadTableCountAsync(fixture.Session, CancellationToken.None));
        Assert.AreEqual(fixture.Session.WorkspaceId, fixture.Http.LastWire.GetProperty("workspaceId").GetGuid());
        Assert.AreEqual(fixture.Session.SessionEpoch, fixture.Http.LastWire.GetProperty("sessionEpoch").GetUInt64());
        Assert.AreEqual(1, fixture.Http.ProductCalls);
    }

    [TestMethod]
    public async Task HealthReaderRetainsBindingMismatchCodeForLateReply()
    {
        await using var fixture = await Fixture.OpenAsync();
        var reader = new CurrentRuntimeUpdateWorkspaceSchemaReader(fixture.Factory, fixture.Leases, fixture.Http);
        fixture.Http.ReplyGate = new(TaskCreationOptions.RunContinuationsAsynchronously);
        Task<int> pending = reader.ReadTableCountAsync(fixture.Session, CancellationToken.None);
        try
        {
            await fixture.Http.RpcEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));
            await fixture.Backend.StopAsync(CancellationToken.None);
            await fixture.Backend.StartAsync(CancellationToken.None);
        }
        finally { fixture.Http.ReplyGate.TrySetResult(); }
        UpdateWorkspaceHealthException error = await Assert.ThrowsExactlyAsync<UpdateWorkspaceHealthException>(() => pending);
        Assert.AreEqual("update.workspace_probe_binding_mismatch", error.Code);
        Assert.AreEqual(1, fixture.Http.ProductCalls);
    }

    [TestMethod]
    [DataRow("workspace")]
    [DataRow("epoch")]
    [DataRow("closed")]
    [DataRow("draining")]
    public async Task HealthReaderRejectsWrongOrUnavailableEpochWithoutSending(string condition)
    {
        await using var fixture = await Fixture.OpenAsync();
        WorkspaceSessionV2 expected = fixture.Session;
        if (condition == "workspace") expected = expected with { WorkspaceId = Guid.NewGuid() };
        if (condition == "epoch") expected = expected with { SessionEpoch = expected.SessionEpoch + 1 };
        if (condition == "closed") await fixture.CloseAsync();
        if (condition == "draining")
            await fixture.Leases.DrainAsync(expected.WorkspaceId!.Value, expected.SessionEpoch, CancellationToken.None);
        try
        {
            var reader = new CurrentRuntimeUpdateWorkspaceSchemaReader(fixture.Factory, fixture.Leases, fixture.Http);
            UpdateWorkspaceHealthException error = await Assert.ThrowsExactlyAsync<UpdateWorkspaceHealthException>(
                () => reader.ReadTableCountAsync(expected, CancellationToken.None));
            Assert.AreEqual("update.workspace_probe_binding_mismatch", error.Code);
            Assert.AreEqual(0, fixture.Http.ProductCalls);
            Assert.AreEqual(0, fixture.Http.ProductHandshakes);
        }
        finally
        {
            if (condition == "draining") fixture.Leases.Resume(expected.WorkspaceId!.Value, expected.SessionEpoch);
        }
    }

    [TestMethod]
    [DataRow("null")]
    [DataRow("[]")]
    [DataRow("{}")]
    [DataRow("{\"tables\":{}}")]
    public async Task HealthReaderRetainsStrictSchemaResponseError(string response)
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Http.Result = Json(response);
        var reader = new CurrentRuntimeUpdateWorkspaceSchemaReader(fixture.Factory, fixture.Leases, fixture.Http);
        UpdateWorkspaceHealthException error = await Assert.ThrowsExactlyAsync<UpdateWorkspaceHealthException>(
            () => reader.ReadTableCountAsync(fixture.Session, CancellationToken.None));
        Assert.AreEqual("update.workspace_probe_response_invalid", error.Code);
    }

    [TestMethod]
    public async Task HealthReaderPreservesRemoteErrorAndCloseCancelsInflightProbe()
    {
        await using var fixture = await Fixture.OpenAsync();
        var reader = new CurrentRuntimeUpdateWorkspaceSchemaReader(fixture.Factory, fixture.Leases, fixture.Http);
        fixture.Http.Error = true;
        RpcRemoteException error = await Assert.ThrowsExactlyAsync<RpcRemoteException>(
            () => reader.ReadTableCountAsync(fixture.Session, CancellationToken.None));
        Assert.AreEqual(-32602, error.Code);
        fixture.Http.Error = false;
        fixture.Http.RpcEntered = new(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Http.ReplyGate = new(TaskCreationOptions.RunContinuationsAsynchronously);
        Task<int> pending = reader.ReadTableCountAsync(fixture.Session, CancellationToken.None);
        try
        {
            await fixture.Http.RpcEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));
            Task close = fixture.CloseAsync();
            await Assert.ThrowsAsync<OperationCanceledException>(() => pending.WaitAsync(TimeSpan.FromSeconds(5)));
            await close.WaitAsync(TimeSpan.FromSeconds(5));
        }
        finally { fixture.Http.ReplyGate.TrySetResult(); }
        Assert.AreEqual(2, fixture.Http.ProductCalls);
    }

    [TestMethod]
    public async Task LazyGatewayKeepsWorkspaceSupportOnReplacedPythonClient()
    {
        await using var fixture = await Fixture.OpenAsync();
        using var lazy = new LazyProductTableGateway(fixture.Leases, fixture.Http);
        lazy.Bind(fixture.Factory.CaptureHostProductRpcBinding()!);
        _ = await Assert.ThrowsExactlyAsync<RpcRemoteException>(() =>
            lazy.GetGridStateAsync("workspace", "orders", CancellationToken.None));
        await fixture.Backend.StopAsync(CancellationToken.None);
        await fixture.Backend.StartAsync(CancellationToken.None);
        lazy.Bind(fixture.Factory.CaptureHostProductRpcBinding()!);
        RpcRemoteException error = await Assert.ThrowsExactlyAsync<RpcRemoteException>(() =>
            lazy.GetGridStateAsync("workspace", "orders", CancellationToken.None));
        Assert.AreEqual(-32601, error.Code); // Reached the paired new fake backend, not a disposed client.
        Assert.AreEqual(0, fixture.Http.ProductCalls);
        Assert.AreEqual(0, fixture.Http.ProductHandshakes);
    }

    [TestMethod]
    public async Task LazyGatewayUsesSelectedProductOwnerAndReusesTheCompleteBinding()
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Http.Result = Json("""{"tables":[{"tableId":"orders","kind":"base","displayName":"Orders"}]}""");
        using var lazy = new LazyProductTableGateway(fixture.Leases, fixture.Http);
        lazy.Bind(fixture.Factory.CaptureHostProductRpcBinding()!);
        CollectionAssert.AreEqual(new[] { "orders" }, (await lazy.ListTablesAsync(CancellationToken.None)).Tables.ToArray());
        lazy.Bind(fixture.Factory.CaptureHostProductRpcBinding()!);
        _ = await lazy.ListTablesAsync(CancellationToken.None);
        Assert.AreEqual(1, fixture.Http.ProductHandshakes);
        lazy.Unbind();
        await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() => lazy.ListTablesAsync(CancellationToken.None));
        Assert.AreEqual(2, fixture.Http.ProductCalls);
    }

    [TestMethod]
    public async Task LazyGatewayRotatesOnSameClientNewSnapshotWithoutDisposingInflightWork()
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Http.Result = Json("""{"tables":[{"tableId":"orders","kind":"base","displayName":"Orders"}]}""");
        using var lazy = new LazyProductTableGateway(fixture.Leases, fixture.Http);
        HostProductRpcBinding old = fixture.Factory.CaptureHostProductRpcBinding()!;
        lazy.Bind(old);
        var reply = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Http.ReplyGate = reply;
        Task<TableSummary> pending = lazy.ListTablesAsync(CancellationToken.None);
        using var releaseReady = new ManualResetEventSlim();
        var ready = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.BeforeSidecarReady = () =>
        {
            ready.TrySetResult();
            Assert.IsTrue(releaseReady.Wait(TimeSpan.FromSeconds(5)));
        };
        Task? restarting = null;
        try
        {
            await fixture.Http.RpcEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));
            await fixture.Sidecar.StopAsync(CancellationToken.None);
            restarting = Task.Run(() => fixture.Sidecar.StartAsync(CancellationToken.None));
            await ready.Task.WaitAsync(TimeSpan.FromSeconds(5));
            HostProductRpcBinding current = fixture.Factory.CaptureHostProductRpcBinding()!;
            Assert.AreSame(old.Client, current.Client);
            lazy.Bind(current);
            Assert.IsFalse(pending.IsCompleted, "Rebinding must not dispose the retired gateway.");
            fixture.Http.ReplyGate = null;
            CollectionAssert.AreEqual(new[] { "orders" }, (await lazy.ListTablesAsync(CancellationToken.None)).Tables.ToArray());
            Assert.IsFalse(pending.IsCompleted);
            reply.TrySetResult();
            await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() => pending);
            Assert.AreEqual(2, fixture.Http.ProductHandshakes);
        }
        finally
        {
            reply.TrySetResult();
            releaseReady.Set();
            if (restarting is not null) await restarting.WaitAsync(TimeSpan.FromSeconds(5));
        }
    }

    [TestMethod]
    public async Task ReadyFactoryCapturesPairedClientAndUsesTypedSelectedRoute()
    {
        await using var fixture = await Fixture.OpenAsync();
        HostProductRpcBinding? binding = fixture.Factory.CaptureHostProductRpcBinding(fixture.Session);
        Assert.IsNotNull(binding);
        Assert.AreSame(fixture.Backend.Client, binding.Client);
        Assert.IsTrue(binding.Matches(fixture.Factory.CaptureHostProductRpcBinding()!));
        using var gateway = binding.CreateGateway(fixture.Leases, fixture.Http);
        JsonElement result = await gateway.ListTablesAsync(Json("{}"), CancellationToken.None);
        Assert.AreEqual("orders", result.GetProperty("tables")[0].GetString());
        Assert.AreEqual(1, fixture.Http.ProductCalls);
    }

    [TestMethod]
    [DataRow("workspace")]
    [DataRow("epoch")]
    [DataRow("starting")]
    public async Task CaptureRejectsWrongSessionAndNonReadyClient(string condition)
    {
        await using var fixture = await Fixture.OpenAsync();
        WorkspaceSessionV2 expected = condition switch
        {
            "workspace" => fixture.Session with { WorkspaceId = Guid.NewGuid() },
            "epoch" => fixture.Session with { SessionEpoch = fixture.Session.SessionEpoch + 1 },
            _ => fixture.Session,
        };
        using var cancellation = new CancellationTokenSource();
        Task? starting = null;
        if (condition == "starting")
        {
            await fixture.Backend.StopAsync(CancellationToken.None);
            fixture.BackendOptions.Environment["__VIBETABLE_HANDSHAKE_DELAY_SECONDS"] = "30";
            starting = fixture.Backend.StartAsync(cancellation.Token);
            Assert.AreEqual(BackendState.Starting, fixture.Backend.State);
            Assert.IsNotNull(fixture.Backend.Client);
        }
        try
        {
            Assert.IsNull(fixture.Factory.CaptureHostProductRpcBinding(expected));
            Assert.AreEqual(0, fixture.Http.ProductCalls);
        }
        finally
        {
            cancellation.Cancel();
            if (starting is not null)
                await Assert.ThrowsAsync<OperationCanceledException>(() => starting.WaitAsync(TimeSpan.FromSeconds(5)));
        }
    }

    [TestMethod]
    [DataRow("before")]
    [DataRow("result")]
    [DataRow("error")]
    public async Task ReplacedPythonClientRejectsOldBindingBeforeSendAndAfterReply(string phase)
    {
        await using var fixture = await Fixture.OpenAsync();
        HostProductRpcBinding old = fixture.Factory.CaptureHostProductRpcBinding()!;
        using var gateway = old.CreateGateway(fixture.Leases, fixture.Http);
        Task<JsonElement>? pending = null;
        fixture.Http.Error = phase == "error";
        if (phase != "before")
        {
            fixture.Http.ReplyGate = new(TaskCreationOptions.RunContinuationsAsynchronously);
            pending = gateway.ListTablesAsync(Json("{}"), CancellationToken.None);
        }
        try
        {
            if (pending is not null)
                await fixture.Http.RpcEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));
            await fixture.Backend.StopAsync(CancellationToken.None);
            await fixture.Backend.StartAsync(CancellationToken.None);
        }
        finally { fixture.Http.ReplyGate?.TrySetResult(); }
        HostProductRpcBinding current = fixture.Factory.CaptureHostProductRpcBinding()!;
        Assert.IsFalse(old.Matches(current));
        Assert.AreNotSame(old.Client, current.Client);
        await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() =>
            pending ?? gateway.ListTablesAsync(Json("{}"), CancellationToken.None));
        Assert.AreEqual(phase == "before" ? 0 : 1, fixture.Http.ProductCalls);
        Assert.AreEqual(phase == "before" ? 0 : 1, fixture.Http.ProductHandshakes);
    }

    [TestMethod]
    [DataRow("before")]
    [DataRow("result")]
    [DataRow("error")]
    public async Task SidecarReplacementWithSamePythonClientInvalidatesCanonicalBinding(string phase)
    {
        await using var fixture = await Fixture.OpenAsync();
        HostProductRpcBinding old = fixture.Factory.CaptureHostProductRpcBinding()!;
        using var gateway = old.CreateGateway(fixture.Leases, fixture.Http);
        using var releaseReady = new ManualResetEventSlim();
        var ready = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.BeforeSidecarReady = () =>
        {
            ready.TrySetResult();
            Assert.IsTrue(releaseReady.Wait(TimeSpan.FromSeconds(5)), "Ready observer was not released.");
        };
        fixture.Http.Error = phase == "error";
        Task<JsonElement>? pending = null;
        Task? restarting = null;
        try
        {
            if (phase != "before")
            {
                fixture.Http.ReplyGate = new(TaskCreationOptions.RunContinuationsAsynchronously);
                pending = gateway.ListTablesAsync(Json("{}"), CancellationToken.None);
                await fixture.Http.RpcEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));
            }
            await fixture.Sidecar.StopAsync(CancellationToken.None);
            restarting = Task.Run(() => fixture.Sidecar.StartAsync(CancellationToken.None));
            await ready.Task.WaitAsync(TimeSpan.FromSeconds(5));
            HostProductRpcBinding current = fixture.Factory.CaptureHostProductRpcBinding()!;
            Assert.AreSame(old.Client, current.Client);
            Assert.IsFalse(old.Matches(current));
            fixture.Http.ReplyGate?.TrySetResult();
            await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() =>
                pending ?? gateway.ListTablesAsync(Json("{}"), CancellationToken.None));
            Assert.AreEqual(phase == "before" ? 0 : 1, fixture.Http.ProductCalls);
            fixture.Http.Error = false;
            using var replacement = current.CreateGateway(fixture.Leases, fixture.Http);
            Assert.AreEqual("orders", (await replacement.ListTablesAsync(Json("{}"), CancellationToken.None))
                .GetProperty("tables")[0].GetString());
        }
        finally
        {
            fixture.Http.ReplyGate?.TrySetResult();
            releaseReady.Set();
            if (restarting is not null) await restarting.WaitAsync(TimeSpan.FromSeconds(5));
        }
    }

    [TestMethod]
    public async Task ClosedRuntimeCannotCaptureOrSendFromItsOldBinding()
    {
        await using var fixture = await Fixture.OpenAsync();
        HostProductRpcBinding binding = fixture.Factory.CaptureHostProductRpcBinding()!;
        using var gateway = binding.CreateGateway(fixture.Leases, fixture.Http);
        await fixture.CloseAsync();
        Assert.IsNull(fixture.Factory.CaptureHostProductRpcBinding());
        await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() =>
            gateway.ListTablesAsync(Json("{}"), CancellationToken.None));
        Assert.AreEqual(0, fixture.Http.ProductCalls);
        Assert.AreEqual(0, fixture.Http.ProductHandshakes);
    }

    [TestMethod]
    public async Task DefaultPolicyStillSelectsPythonAndDoesNotHandshakeProductHttp()
    {
        await using var fixture = await Fixture.OpenAsync(useTestPolicy: false);
        using var gateway = fixture.Factory.CaptureHostProductRpcBinding()!
            .CreateGateway(fixture.Leases, fixture.Http);
        RpcRemoteException error = await Assert.ThrowsExactlyAsync<RpcRemoteException>(() =>
            gateway.ListTablesAsync(Json("{}"), CancellationToken.None));
        Assert.AreEqual(-32601, error.Code); // Existing fake_backend has no business methods.
        Assert.AreEqual(0, fixture.Http.ProductCalls);
        Assert.AreEqual(0, fixture.Http.ProductHandshakes);
    }

    private static JsonElement Json(string value)
    {
        using JsonDocument document = JsonDocument.Parse(value);
        return document.RootElement.Clone();
    }

    private sealed class Fixture : IAsyncDisposable
    {
        private readonly string _root = Path.Combine(Path.GetTempPath(), "vibetable-binding-" + Guid.NewGuid().ToString("N"));
        private readonly SidecarPeer _peer = new();
        private readonly WorkspaceSessionManager _sessions;
        internal ProductionWorkspaceRuntimeFactory Factory { get; }
        internal WorkspaceSessionEnvelopeFilter Leases { get; }
        internal PythonBackendSupervisor Backend { get; private set; } = null!;
        internal BackendLaunchOptions BackendOptions { get; private set; } = null!;
        internal PocketBaseSupervisor Sidecar { get; private set; } = null!;
        internal HttpPeer Http { get; } = new();
        internal Action? BeforeSidecarReady { get; set; }
        internal WorkspaceSessionV2 Session => _sessions.Current;

        private Fixture(bool useTestPolicy)
        {
            DirectoryInfo directory = new(AppContext.BaseDirectory);
            while (!File.Exists(Path.Combine(directory.FullName, "pyproject.toml")))
                directory = directory.Parent ?? throw new InvalidOperationException("Repository not found.");
            Factory = new ProductionWorkspaceRuntimeFactory(
                () => new PocketBaseLaunchOptions
                {
                    ExecutablePath = "test-peer",
                    DataDirectory = "unused",
                    ExpectedIdentity = new("vibetable.sidecar.ready.v1", "v1", "0.40.1", "1", "migration-hash"),
                    StopTimeout = TimeSpan.FromSeconds(1),
                    CrashRestartLimit = 0,
                },
                () => new BackendLaunchOptions
                {
                    Command = "uv",
                    Arguments = $"run --frozen --no-sync python \"{Path.Combine(directory.FullName, "tests", "contract", "fake_backend.py")}\"",
                    WorkingDirectory = directory.FullName,
                    StopTimeout = TimeSpan.FromSeconds(1),
                },
                (sidecarOptions, backendOptions) =>
                {
                    Http.Environment = sidecarOptions.Environment;
                    Sidecar = new PocketBaseSupervisor(sidecarOptions, _peer, _peer);
                    // Hold the existing notification seam before runtime recovery observes Ready.
                    Sidecar.StatusChanged += (_, status) =>
                    {
                        if (status.State == PocketBaseState.Ready) BeforeSidecarReady?.Invoke();
                    };
                    BackendOptions = backendOptions;
                    Backend = new PythonBackendSupervisor(backendOptions);
                    return new(Sidecar, Backend, new WorkspaceV2HttpGateway(Sidecar, Http));
                },
                useTestPolicy ? ProductRpcCapabilityManifest.CreateForTests(new ProductRpcCapability(
                    "schema.list", "workspace", "hostOnly", "schema.read", "goSidecar", "read")) : null);
            _sessions = new WorkspaceSessionManager(new WorkspaceRegistry(_root), Factory);
            Leases = new WorkspaceSessionEnvelopeFilter(_sessions);
            _sessions.SetRequestDrainHook(Leases);
        }

        internal static async Task<Fixture> OpenAsync(bool useTestPolicy = true)
        {
            var fixture = new Fixture(useTestPolicy);
            try
            {
                WorkspaceLayoutResult layout = WorkspaceLayout.Create(Path.Combine(fixture._root, "workspace"),
                    "Binding", WorkspaceStorageMode.Direct, WorkspaceEncryptionMode.Convenient);
                new WorkspaceRegistry(fixture._root).Register(new WorkspaceRegistryEntryV2
                {
                    ContractVersion = "2.0",
                    WorkspaceId = layout.Manifest.WorkspaceId,
                    DisplayName = "Binding",
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
                await fixture._sessions.OpenAsync(layout.Manifest.WorkspaceId, WorkspaceOpenMode.ReadOnly);
                return fixture;
            }
            catch { await fixture.DisposeAsync(); throw; }
        }

        internal Task CloseAsync() => _sessions.CloseAsync("binding-test");

        public async ValueTask DisposeAsync()
        {
            await _sessions.DisposeAsync();
            Leases.Dispose();
            await Factory.DisposeAsync();
            Http.Dispose();
            Assert.IsTrue(_peer.Processes.All(process => process.HasExited));
            Directory.Delete(_root, recursive: true);
        }
    }

    private sealed class HttpPeer : HttpMessageHandler
    {
        internal IDictionary<string, string> Environment { get; set; } = null!;
        internal int ProductCalls { get; private set; }
        internal int ProductHandshakes { get; private set; }
        internal TaskCompletionSource RpcEntered { get; set; } = new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal TaskCompletionSource? ReplyGate { get; set; }
        internal bool Error { get; set; }
        internal JsonElement Result { get; set; } = Json("""{"tables":["orders"]}""");
        internal JsonElement LastWire { get; private set; }
        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token)
        {
            if (request.Method == HttpMethod.Get)
            {
                if (request.RequestUri!.AbsolutePath.Contains("/product/", StringComparison.Ordinal)) ProductHandshakes++;
                return Reply(JsonSerializer.SerializeToElement(new
                {
                    contractVersion = "2.0",
                    workspaceId = Environment["VIBETABLE_WORKSPACE_ID"],
                    sessionEpoch = ulong.Parse(Environment["VIBETABLE_WORKSPACE_SESSION_EPOCH"]),
                    fenceEpoch = ulong.Parse(Environment["VIBETABLE_WORKSPACE_FENCE_EPOCH"]),
                    claimId = Environment["VIBETABLE_WORKSPACE_CLAIM_ID"],
                    rpcMethods = new[] { "schema.list" },
                    registrations = new[] { new { method = "schema.list", scope = "workspace" } },
                }));
            }
            if (request.RequestUri!.AbsolutePath.EndsWith("/drain", StringComparison.Ordinal))
                return Reply(Json("""{"sourceEpoch":"test-epoch","sourceSequence":0,"chainHash":"test-chain"}"""));
            ProductCalls++;
            JsonElement call = Json(await request.Content!.ReadAsStringAsync(token));
            LastWire = call.GetProperty("wire").Clone();
            RpcEntered.TrySetResult();
            if (ReplyGate is not null) await ReplyGate.Task.WaitAsync(token);
            if (Error)
                return Reply(JsonSerializer.SerializeToElement(new
                {
                    jsonrpc = "2.0",
                    id = call.GetProperty("id"),
                    wire = call.GetProperty("wire"),
                    error = new { code = -32602, message = "Invalid params" },
                }));
            return Reply(JsonSerializer.SerializeToElement(new
            {
                jsonrpc = "2.0",
                id = call.GetProperty("id"),
                wire = call.GetProperty("wire"),
                result = Result,
            }));
        }
        private static HttpResponseMessage Reply(JsonElement result) => new(HttpStatusCode.OK)
        { Content = new StringContent(result.GetRawText()) };
    }

    private sealed class SidecarPeer : IPocketBaseProcessFactory, IPocketBaseHealthProbe
    {
        internal List<ProcessPeer> Processes { get; } = [];
        public IPocketBaseProcess Start(PocketBaseProcessStartRequest request)
        {
            var process = new ProcessPeer();
            Processes.Add(process);
            return process;
        }
        public Task<PocketBaseHealthStatus?> GetHealthAsync(Uri endpoint, string sessionSecret, CancellationToken token)
            => Task.FromResult<PocketBaseHealthStatus?>(new("ok", "ok", true, true,
                new("v1", "0.40.1", "1", "migration-hash")));
        public Task<bool> RequestShutdownAsync(Uri endpoint, string sessionSecret, CancellationToken token)
        { Processes[^1].KillProcessTree(); return Task.FromResult(true); }
    }

    private sealed class ProcessPeer : IPocketBaseProcess
    {
        private readonly TaskCompletionSource _exited = new(TaskCreationOptions.RunContinuationsAsynchronously);
        public int Id => 42;
        public bool HasExited => _exited.Task.IsCompleted;
        public int? ExitCode => HasExited ? 0 : null;
        public TextReader StandardOutput { get; } = new StringReader("""
            {"contract":"vibetable.sidecar.ready.v1","event":"sidecar.ready","address":"127.0.0.1:43125","pid":42,"build":{"version":"0.1.0-dev","commit":"unknown","buildTime":"unknown","contractVersion":"v1","pocketBaseVersion":"0.40.1","celVersion":"0.31.0","schemaVersion":"1","migrationHash":"migration-hash"}}
            """);
        public TextReader StandardError { get; } = new StringReader("");
        public event EventHandler? Exited;
        public void KillProcessTree() { if (_exited.TrySetResult()) Exited?.Invoke(this, EventArgs.Empty); }
        public Task WaitForExitAsync(CancellationToken token) => _exited.Task.WaitAsync(token);
        public ValueTask DisposeAsync() { StandardOutput.Dispose(); StandardError.Dispose(); return ValueTask.CompletedTask; }
    }
}
