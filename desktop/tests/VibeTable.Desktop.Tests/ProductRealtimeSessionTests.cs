using System.Net;
using System.Text;
using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductRealtimeSessionTests
{
    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task TerminalFailureIsPostedOnlyToItsCurrentRenderer(bool retireRenderer)
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Http.CatalogFailure = true;
        fixture.Delivery.SetReady();
        await fixture.NextConnection();
        await fixture.NextFailure();
        Action post = await fixture.NextPost();
        if (retireRenderer) fixture.Delivery.Retire();
        post();
        if (retireRenderer) Assert.HasCount(0, fixture.Posted);
        else
        {
            Assert.HasCount(1, fixture.Posted);
            Assert.AreEqual("operation.failed", fixture.Posted[0].Topic);
            Assert.AreEqual("realtime.stream", fixture.Posted[0].Payload.GetProperty("operation").GetString());
            Assert.AreEqual("realtime.stopped", fixture.Posted[0].Payload.GetProperty("code").GetString());
            Assert.DoesNotContain("secret", fixture.Posted[0].Payload.ToString());
        }
        Assert.IsFalse(fixture.Delayed.Task.IsCompleted);
    }

    [TestMethod]
    public async Task EpochDrainCancelsQueuedDeliveryWithoutReportingAnOperationalFailure()
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Delivery.SetReady();
        await fixture.NextConnection();
        Action stale = await fixture.NextPost();
        WorkspaceSessionV2 session = fixture.Sessions.Current;
        await fixture.Leases.DrainAsync(session.WorkspaceId!.Value, session.SessionEpoch,
            CancellationToken.None).WaitAsync(TimeSpan.FromSeconds(5));
        await fixture.Owner.DisposeAsync();
        stale();
        Assert.HasCount(0, fixture.Posted);
        Assert.IsFalse(fixture.Failures.Reader.TryRead(out _));
    }

    [TestMethod]
    public async Task CatalogFailureCannotPostRecoveryOrTurnIntoATransportRetry()
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Http.CatalogFailure = true;
        fixture.Delivery.SetReady();
        await fixture.NextConnection();
        string failure = await fixture.NextFailure();
        Assert.DoesNotContain("secret", failure);
        Assert.HasCount(0, fixture.Posted);
        Assert.IsFalse(fixture.Delayed.Task.IsCompleted);
        Assert.IsTrue(fixture.Http.Streams[0].Disposed);
    }

    [TestMethod]
    public async Task ActualPostFailureDoesNotCommitBookmarkAndNewRendererStartsCold()
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.PostFailure = true;
        fixture.Delivery.SetReady();
        await fixture.NextConnection();
        (await fixture.NextPost())();
        Assert.DoesNotContain("secret", await fixture.NextFailure());
        Assert.IsFalse(fixture.Delayed.Task.IsCompleted);
        fixture.PostFailure = false;
        fixture.Delivery.Retire();
        fixture.Delivery.SetReady();
        Assert.IsNull(await fixture.NextConnection());
        Assert.HasCount(0, fixture.Posted);
    }

    [TestMethod]
    public async Task SidecarReplacementRetainsOnlyTheActuallyPostedBookmark()
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Delivery.SetReady();
        await fixture.NextConnection();
        (await fixture.NextPost())();
        await fixture.Delayed.Task.WaitAsync(TimeSpan.FromSeconds(5));
        ProductSidecarGenerationSnapshot old = fixture.Snapshot!;
        fixture.Snapshot = new ProductSidecarGenerationSnapshot(old.RuntimeAuthority, 2,
            old.Context, old.Identity, old.Registrations.ToArray());
        fixture.Authority.SetCurrent(fixture.Snapshot);
        Assert.AreEqual("rt:7", await fixture.NextConnection());
        Assert.IsTrue(fixture.Http.Streams[0].Disposed);
    }

    [TestMethod]
    public async Task RetiredRendererDropsQueuedRecoveryAndReopensColdWithoutWaitingForUi()
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Delivery.SetReady();
        await fixture.NextConnection();
        Action stale = await fixture.NextPost();
        fixture.Delivery.Retire();
        fixture.Delivery.SetReady();
        Assert.IsNull(await fixture.NextConnection());
        stale();
        Assert.HasCount(0, fixture.Posted);
        Assert.IsTrue(fixture.Http.Streams[0].Disposed);
        fixture.Delivery.SetReady();
        Assert.IsFalse(fixture.Http.Connections.Reader.TryRead(out _));
    }

    [TestMethod]
    public async Task ClosingSessionDrainsQueuedUiDeliveryWithoutPostingOrWaitingForDispatcher()
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Delivery.SetReady();
        await fixture.NextConnection();
        Action stale = await fixture.NextPost();
        await fixture.Sessions.CloseAsync("test-close").WaitAsync(TimeSpan.FromSeconds(5));
        stale();
        Assert.HasCount(0, fixture.Posted);
        Assert.IsTrue(fixture.Http.Streams[0].Disposed);
        Assert.IsFalse(fixture.Failures.Reader.TryRead(out _));
    }

    [TestMethod]
    public async Task RetiredSidecarDropsQueuedPostEvenWhenTheWorkspaceIdentityIsUnchanged()
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Delivery.SetReady();
        await fixture.NextConnection();
        Action stale = await fixture.NextPost();
        ProductSidecarGenerationSnapshot old = fixture.Snapshot!;
        fixture.Snapshot = new ProductSidecarGenerationSnapshot(old.RuntimeAuthority, 2,
            old.Context, old.Identity, old.Registrations.ToArray());
        fixture.Authority.SetCurrent(fixture.Snapshot);
        Assert.IsNull(await fixture.NextConnection());
        stale();
        Assert.HasCount(0, fixture.Posted);
        Assert.IsTrue(fixture.Http.Streams[0].Disposed);
    }

    [TestMethod]
    [DataRow(404, "realtime.cursor_unknown", true)]
    [DataRow(422, "realtime.cursor_future", true)]
    [DataRow(500, "realtime.unavailable", false)]
    [DataRow(500, "unrecognized", true)]
    [DataRow(503, "realtime.storage_failed", true)]
    public async Task NonRetryableOrMismatchedHttpErrorsNeverResetCursorOrReconnect(int status, string code, bool retryable)
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Http.StreamReplies.Enqueue(new HttpResponseMessage((HttpStatusCode)status)
        {
            Content = new StringContent(JsonSerializer.Serialize(new { code, retryable, message = "secret" }),
                Encoding.UTF8, "application/json"),
        });
        fixture.Delivery.SetReady();
        await fixture.NextConnection();
        string failure = await fixture.Failures.Reader.ReadAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(5));
        Assert.DoesNotContain("secret", failure);
        Assert.IsFalse(fixture.Delayed.Task.IsCompleted);
        Assert.IsFalse(fixture.Http.Connections.Reader.TryRead(out _));
        Assert.HasCount(0, fixture.Posted);
    }

    [TestMethod]
    public async Task ExplicitRetryableCapacityResponseBacksOffBeforeConnectingAgain()
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Http.StreamReplies.Enqueue(new HttpResponseMessage(HttpStatusCode.ServiceUnavailable)
        {
            Content = new StringContent("""{"code":"realtime.capacity","message":"private detail","retryable":true}""",
                Encoding.UTF8, "application/json"),
        });
        fixture.Delivery.SetReady();
        Assert.IsNull(await fixture.NextConnection());
        await fixture.Delayed.Task.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.IsFalse(fixture.Http.Connections.Reader.TryRead(out _));
        fixture.ResumeDelay.TrySetResult();
        Assert.IsNull(await fixture.NextConnection());
        Assert.IsFalse(fixture.Failures.Reader.TryRead(out _));
    }

    [TestMethod]
    public async Task RecoveryReadsFreshCatalogAndResumesOnlyAfterActualRendererPost()
    {
        await using var fixture = await Fixture.OpenAsync();
        Assert.IsFalse(fixture.Http.Connections.Reader.TryRead(out _));
        fixture.Delivery.SetReady();
        Assert.IsNull(await fixture.NextConnection());
        Action post = await fixture.NextPost();
        Assert.HasCount(0, fixture.Posted);
        Assert.AreEqual(1, fixture.Http.CatalogReads);
        post();
        await fixture.Delayed.Task.WaitAsync(TimeSpan.FromSeconds(5));
        CollectionAssert.AreEqual(new[] { "database.collectionsChanged", "realtime.recovered" },
            fixture.Posted.Select(item => item.Topic).ToArray());
        Assert.AreEqual("fresh-table", fixture.Posted[0].Payload.GetProperty("tables")[0].GetString());
        Assert.AreEqual("fresh-view", fixture.Posted[0].Payload.GetProperty("views")[0].GetString());
        Assert.AreEqual("Fresh view", fixture.Posted[0].Payload.GetProperty("displayNames").GetProperty("fresh-view").GetString());
        fixture.ResumeDelay.TrySetResult();
        Assert.AreEqual("rt:7", await fixture.NextConnection());
    }

    private sealed class Fixture : IAsyncDisposable
    {
        private readonly string _root = Path.Combine(Directory.GetCurrentDirectory(),
            "build", "qa", "realtime-tests", Guid.NewGuid().ToString("N"));
        private readonly Channel<Action> _dispatch = Channel.CreateUnbounded<Action>();
        internal readonly TaskCompletionSource Delayed = NewSignal();
        internal readonly TaskCompletionSource ResumeDelay = NewSignal();
        internal readonly List<(string Topic, JsonElement Payload)> Posted = [];
        internal readonly Channel<string> Failures = Channel.CreateUnbounded<string>();
        internal WorkspaceSessionManager Sessions { get; private set; } = null!;
        internal WorkspaceSessionEnvelopeFilter Leases { get; private set; } = null!;
        internal ProductRealtimeDelivery Delivery { get; private set; } = null!;
        internal ProductRealtimeSession Owner { get; private set; } = null!;
        internal HttpPeer Http { get; private set; } = null!;
        internal ControlledGenerationAuthority Authority { get; } = new();
        internal ProductSidecarGenerationSnapshot? Snapshot { get; set; }
        internal bool PostFailure { get; set; }

        internal static async Task<Fixture> OpenAsync()
        {
            var fixture = new Fixture();
            var registry = new WorkspaceRegistry(fixture._root);
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(Path.Combine(fixture._root, "workspace"),
                "Realtime", WorkspaceStorageMode.Direct, WorkspaceEncryptionMode.Convenient);
            registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = "2.0", WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = "Realtime", SelectedRoot = layout.SelectedRoot, ActivityRoot = null,
                StorageKind = WorkspaceStorageKind.Fixed, CoordinationStrength = WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = null, LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = null, LastSyncAt = null, PendingSync = false,
            });
            fixture.Sessions = new WorkspaceSessionManager(registry, new RuntimeFactory());
            WorkspaceSessionV2 session = await fixture.Sessions.OpenAsync(layout.Manifest.WorkspaceId,
                WorkspaceOpenMode.ReadOnly);
            fixture.Leases = new WorkspaceSessionEnvelopeFilter(fixture.Sessions);
            fixture.Sessions.SetRequestDrainHook(fixture.Leases);
            fixture.Snapshot = new ProductSidecarGenerationSnapshot(fixture, 1,
                new PocketBaseAdminContext(new Uri("http://127.0.0.1:8123/bootstrap"),
                    new Uri("http://127.0.0.1:8123/"), "X-VibeTable-Session", "test-secret"),
                new ProductSidecarIdentity(session.WorkspaceId!.Value.ToString("D"), session.SessionEpoch,
                    1, "22222222-2222-4222-8222-222222222222"), [new("schema.list", "workspace")]);
            fixture.Authority.SetCurrent(fixture.Snapshot);
            fixture.Http = new HttpPeer(fixture.Snapshot);
            fixture.Delivery = new ProductRealtimeDelivery(async (action, token) =>
            {
                var completed = NewSignal();
                await fixture._dispatch.Writer.WriteAsync(() =>
                {
                    try { action(); completed.TrySetResult(); }
                    catch (Exception error) { completed.TrySetException(error); }
                }, token);
                await completed.Task.WaitAsync(token);
            }, () => true, (topic, payload) =>
            {
                if (fixture.PostFailure) throw new InvalidOperationException("secret renderer failure");
                fixture.Posted.Add((topic,
                    JsonSerializer.SerializeToElement(payload, new JsonSerializerOptions(JsonSerializerDefaults.Web))));
            });
            fixture.Owner = new ProductRealtimeSession(fixture.Authority, () => fixture.Snapshot,
                fixture.Sessions, fixture.Leases, fixture.Delivery, _ => { },
                code => fixture.Failures.Writer.TryWrite(code), fixture.Http, async (_, token) =>
                {
                    fixture.Delayed.TrySetResult();
                    await fixture.ResumeDelay.Task.WaitAsync(token);
                });
            return fixture;
        }

        internal async Task<string?> NextConnection() =>
            await Http.Connections.Reader.ReadAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(5));
        internal async Task<Action> NextPost() =>
            await _dispatch.Reader.ReadAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(5));
        internal async Task<string> NextFailure() =>
            await Failures.Reader.ReadAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(5));

        public async ValueTask DisposeAsync()
        {
            await Owner.DisposeAsync();
            await Sessions.DisposeAsync();
            Leases.Dispose();
            Http.Dispose();
        }
    }

    private sealed class HttpPeer(ProductSidecarGenerationSnapshot snapshot) : HttpMessageHandler
    {
        internal Channel<string?> Connections { get; } = Channel.CreateUnbounded<string?>();
        internal int CatalogReads { get; private set; }
        internal bool CatalogFailure { get; set; }
        internal List<TestSseStream> Streams { get; } = [];
        internal Queue<HttpResponseMessage> StreamReplies { get; } = new();

        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token)
        {
            if (request.RequestUri!.AbsolutePath.EndsWith("/events", StringComparison.Ordinal))
            {
                await Connections.Writer.WriteAsync(request.Headers.TryGetValues("Last-Event-ID", out var values)
                    ? values.Single() : null, token);
                if (StreamReplies.TryDequeue(out HttpResponseMessage? reply)) return reply;
                var source = new TestSseStream(Encoding.UTF8.GetBytes(Streams.Count == 0
                    ? "id: rt:7\nevent: realtime.recovered\ndata: {\"contractVersion\":\"2.0\",\"topic\":\"realtime.recovered\",\"activeFormulaTasks\":[],\"terminalNotifications\":[]}\n\n"
                    : ""), blockAtEnd: Streams.Count != 0);
                Streams.Add(source);
                var response = new HttpResponseMessage(HttpStatusCode.OK) { Content = new StreamContent(source) };
                response.Content.Headers.ContentType = new("text/event-stream");
                return response;
            }
            if (request.Method == HttpMethod.Get)
                return Reply(new
                {
                    contractVersion = "2.0", workspaceId = snapshot.Identity.WorkspaceId,
                    sessionEpoch = snapshot.Identity.SessionEpoch, fenceEpoch = snapshot.Identity.FenceEpoch,
                    claimId = snapshot.Identity.ClaimId, rpcMethods = snapshot.Registrations.Select(item => item.Method),
                    registrations = snapshot.Registrations.Select(item => new { method = item.Method, scope = item.Scope }),
                });
            using JsonDocument call = JsonDocument.Parse(await request.Content!.ReadAsStringAsync(token));
            Assert.AreEqual("schema.list", call.RootElement.GetProperty("method").GetString());
            CatalogReads++;
            if (CatalogFailure) return Reply(new
            {
                jsonrpc = "2.0", id = call.RootElement.GetProperty("id"), wire = call.RootElement.GetProperty("wire"),
                error = new { code = -32603, message = "secret catalog failure" },
            });
            return Reply(new
            {
                jsonrpc = "2.0", id = call.RootElement.GetProperty("id"), wire = call.RootElement.GetProperty("wire"),
                result = new { tables = new[]
                {
                    new { tableId = "fresh-table", kind = "base", displayName = "Fresh" },
                    new { tableId = "fresh-view", kind = "view", displayName = "Fresh view" },
                } },
            });
        }

        private static HttpResponseMessage Reply(object value) => new(HttpStatusCode.OK)
        { Content = new StringContent(JsonSerializer.Serialize(value)) };
    }

    private static TaskCompletionSource NewSignal() => new(TaskCreationOptions.RunContinuationsAsynchronously);
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
