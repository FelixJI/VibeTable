using System.Net;
using System.Text;
using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductRealtimeSessionTests
{
    [TestMethod]
    public async Task ExplicitCatalogRefreshImmediatelyMakesNewTargetSelectable()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult([], [], TestDisplayNames.For());
        gateway.SelectionProjectionResults["fresh-table"] = new TableSelectionProjection(
            new TablePage("fresh-table", [], [], 0, 500, 0, "remote"),
            new EditSchemaResult("fresh-table", "schema_0001", "id", true, false, []));
        var workspace = new TableWorkspaceService(gateway);
        await workspace.OpenDatabaseAsync("db");
        await using var fixture = await Fixture.OpenAsync(applyCatalog: workspace.UpdateKnownCatalog);
        fixture.HoldStreamWithoutRecovery();
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
        await fixture.NextConnection();
        Task refresh = fixture.Owner.RefreshCatalogAsync(fixture.Snapshot!, CancellationToken.None);
        Action post = await fixture.NextPost();
        await Assert.ThrowsExactlyAsync<ArgumentException>(() => workspace.SelectTableAsync("fresh-table"));
        Assert.HasCount(0, fixture.Posted);
        post();
        await refresh.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.IsTrue(await workspace.SelectTableAsync("fresh-table"));
        Assert.AreEqual("fresh-table", fixture.Posted.Single().Payload.GetProperty("tables")[0].GetString());
        Assert.AreEqual("database.collectionsChanged", fixture.Posted.Single().Topic);
        Assert.AreEqual(1, fixture.Http.CatalogReads);
        Assert.HasCount(1, gateway.OpenDatabaseCalls, "Catalog refresh must not reopen the workspace.");
    }

    [TestMethod]
    [DataRow("epoch")]
    [DataRow("generation")]
    [DataRow("renderer")]
    public async Task ExplicitCatalogRefreshCannotProjectIntoRetiredBinding(string retired)
    {
        int applications = 0;
        await using var fixture = await Fixture.OpenAsync(applyCatalog: _ => applications++);
        fixture.HoldStreamWithoutRecovery();
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
        await fixture.NextConnection();
        Task refresh = fixture.Owner.RefreshCatalogAsync(fixture.Snapshot!, CancellationToken.None);
        Action stale = await fixture.NextPost();
        if (retired == "renderer") fixture.Delivery.Retire();
        else if (retired == "generation")
        {
            fixture.Snapshot = null;
            fixture.Authority.SetCurrent(null);
        }
        else
        {
            WorkspaceSessionV2 session = fixture.Sessions.Current;
            await fixture.Leases.DrainAsync(session.WorkspaceId!.Value, session.SessionEpoch, CancellationToken.None);
        }
        stale();
        await refresh.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.AreEqual(0, applications);
        Assert.HasCount(0, fixture.Posted);
    }

    [TestMethod]
    [DataRow("epoch")]
    [DataRow("renderer")]
    [DataRow("generation")]
    [DataRow("caller")]
    public async Task ExplicitCatalogRefreshRetiresANonCooperativeReadAndReleasesItsTurn(string retired)
    {
        var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
        await using var fixture = await Fixture.OpenAsync(workspace: workspace);
        fixture.HoldStreamWithoutRecovery();
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
        await fixture.NextConnection();
        fixture.Http.CatalogReadEntered = NewSignal();
        fixture.Http.CatalogReadRelease = NewSignal();
        using var caller = new CancellationTokenSource();
        Task refresh = fixture.Owner.RefreshCatalogAsync(fixture.Snapshot!, caller.Token);
        await fixture.Http.CatalogReadEntered.Task.WaitAsync(TimeSpan.FromSeconds(2));
        try
        {
            if (retired == "epoch")
            {
                var session = fixture.Sessions.Current;
                await fixture.Leases.DrainAsync(session.WorkspaceId!.Value, session.SessionEpoch,
                    CancellationToken.None).WaitAsync(TimeSpan.FromSeconds(2));
            }
            else if (retired == "renderer") fixture.Delivery.Retire();
            else if (retired == "generation")
            {
                fixture.Snapshot = null;
                fixture.Authority.SetCurrent(null);
            }
            else caller.Cancel();
            await refresh.WaitAsync(TimeSpan.FromSeconds(2));
            Assert.IsFalse(fixture.Http.CatalogReadRelease.Task.IsCompleted);
            using var successor = workspace.BeginCatalogTurn();
            await successor.EnterAsync(CancellationToken.None).WaitAsync(TimeSpan.FromSeconds(2));
            Assert.IsEmpty(fixture.Posted);
            Assert.IsFalse(fixture.Failures.Reader.TryRead(out _));
        }
        finally
        {
            caller.Cancel();
            fixture.Http.CatalogReadRelease.TrySetResult();
            await refresh.WaitAsync(TimeSpan.FromSeconds(2));
        }
    }
    [TestMethod]
    public async Task ExplicitCatalogReadFailureReportsSeparateSafeProjectionFailure()
    {
        int applications = 0;
        await using var fixture = await Fixture.OpenAsync(applyCatalog: _ => applications++);
        fixture.HoldStreamWithoutRecovery();
        fixture.Http.CatalogFailure = true;
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
        await fixture.NextConnection();
        Task refresh = fixture.Owner.RefreshCatalogAsync(fixture.Snapshot!, CancellationToken.None);
        (await fixture.NextPost())();
        await refresh.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.AreEqual(0, applications);
        Assert.AreEqual("operation.failed", fixture.Posted.Single().Topic);
        Assert.AreEqual("source_import.catalog_refresh_failed", fixture.Posted.Single().Payload.GetProperty("code").GetString());
        Assert.DoesNotContain("secret", fixture.Posted.Single().Payload.ToString());
        Assert.AreEqual(1, fixture.Http.CatalogReads);
    }

    [TestMethod]
    public async Task FirstOpenWaitsForBusinessReadyAndDuplicateReadyKeepsTheCommittedBookmark()
    {
        await using var fixture = await Fixture.OpenAsync(initiallyClosed: true);
        var controller = new HostLifecycleRequestController(new FakeWebReplySink(),
            new HostLifecycleActions(fixture.Delivery.SetReady, () => { }, () => { },
                () => false, _ => Task.CompletedTask));
        void Ready(string payload) => controller.Dispatch(new RoutedWebRequest(
            "app.ready", null, JsonSerializer.Deserialize<JsonElement>(payload), ""));
        Ready("{}");
        await fixture.Sessions.OpenAsync(Guid.Parse(fixture.Snapshot!.Identity.WorkspaceId),
            WorkspaceOpenMode.ReadOnly);
        // Host has published opened, but the renderer's first open reply is still held.
        using (var timeout = new CancellationTokenSource(TimeSpan.FromMilliseconds(150)))
            await Assert.ThrowsAsync<OperationCanceledException>(() =>
                fixture.Http.Connections.Reader.ReadAsync(timeout.Token).AsTask());
        Ready("""{"phase":"business"}""");
        Assert.IsNull(await fixture.NextConnection());
        (await fixture.NextPost())();
        await fixture.Delayed.Task.WaitAsync(TimeSpan.FromSeconds(5));
        Ready("""{"phase":"business"}""");
        Ready("""{"phase":"shell"}""");
        Assert.IsFalse(fixture.Http.Connections.Reader.TryRead(out _));
        fixture.ResumeDelay.TrySetResult();
        Assert.AreEqual("rt:7", await fixture.NextConnection());
        fixture.Delivery.Retire();
        Ready("{}");
        Assert.IsNull(fixture.Delivery.Current);
        Ready("""{"phase":"business"}""");
        Assert.IsNull(await fixture.NextConnection());
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task TerminalFailureIsPostedOnlyToItsCurrentRenderer(bool retireRenderer)
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Http.CatalogFailure = true;
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
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
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
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
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
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
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
        await fixture.NextConnection();
        (await fixture.NextPost())();
        Assert.DoesNotContain("secret", await fixture.NextFailure());
        Assert.IsFalse(fixture.Delayed.Task.IsCompleted);
        fixture.PostFailure = false;
        fixture.Delivery.Retire();
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
        Assert.IsNull(await fixture.NextConnection());
        Assert.HasCount(0, fixture.Posted);
    }

    [TestMethod]
    public async Task SidecarReplacementRetainsOnlyTheActuallyPostedBookmark()
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
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
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
        await fixture.NextConnection();
        Action stale = await fixture.NextPost();
        fixture.Delivery.Retire();
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
        Assert.IsNull(await fixture.NextConnection());
        stale();
        Assert.HasCount(0, fixture.Posted);
        Assert.IsTrue(fixture.Http.Streams[0].Disposed);
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
        Assert.IsFalse(fixture.Http.Connections.Reader.TryRead(out _));
    }

    [TestMethod]
    public async Task ClosingSessionDrainsQueuedUiDeliveryWithoutPostingOrWaitingForDispatcher()
    {
        await using var fixture = await Fixture.OpenAsync();
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
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
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
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
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
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
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
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
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
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

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task PluginCatalogUsesHostProjectionOnlyInsideCurrentRendererDelivery(bool retired)
    {
        int projections = 0;
        await using var fixture = await Fixture.OpenAsync(projectPluginCatalog: payload =>
        {
            projections++;
            return new PluginEventEnvelope(PluginContractVersions.Event, "plugin.catalog.changed",
                "local:workspace", "plugin", 2,
                JsonSerializer.SerializeToElement(new { sourceLocation = "host-managed" }));
        });
        fixture.Http.StreamReplies.Enqueue(new HttpResponseMessage(HttpStatusCode.OK)
        {
            Content = new StringContent("id: rt:9\nevent: plugin.catalog.changed\ndata: "
                + "{\"contractVersion\":\"2.0\",\"topic\":\"plugin.catalog.changed\",\"snapshot\":{\"sourceLocation\":\"private-source\"}}\n\n",
                Encoding.UTF8, "text/event-stream"),
        });
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
        await fixture.NextConnection();
        Action post = await fixture.NextPost();
        Assert.AreEqual(0, projections);
        if (retired) fixture.Delivery.Retire();
        post();
        Assert.AreEqual(retired ? 0 : 1, projections);
        if (retired) Assert.HasCount(0, fixture.Posted);
        else
        {
            Assert.HasCount(1, fixture.Posted);
            Assert.AreEqual("plugin.catalog.changed", fixture.Posted[0].Topic);
            Assert.DoesNotContain("private-source", fixture.Posted[0].Payload.ToString());
            await fixture.Delayed.Task.WaitAsync(TimeSpan.FromSeconds(5));
            fixture.ResumeDelay.TrySetResult();
            Assert.AreEqual("rt:9", await fixture.NextConnection());
        }
    }

    [TestMethod]
    public async Task RealSseFeedsAuthorizedFrameInvalidationAndNeverCopiesRowPayload()
    {
        SubscriptionClient? client = null;
        await using var fixture = await Fixture.OpenAsync(observePluginData: (snapshot, topic, payload, gate) =>
            client!.Api.OfferInvalidation(snapshot, topic, payload, gate));
        using var owned = client = new SubscriptionClient(fixture);
        string id = await client.Subscribe();
        fixture.Http.StreamReplies.Enqueue(DataStream("visible", "hidden", "visible"));
        fixture.StartSubscriptionStream();
        await fixture.NextConnection();
        (await fixture.NextPost())();
        (await client.NextFrame())();
        Assert.HasCount(1, client.Notices);
        var notice = client.Notices[0];
        Assert.AreEqual("data.invalidated", notice.GetProperty("event").GetString());
        Assert.AreEqual(id, notice.GetProperty("subscriptionId").GetString());
        Assert.AreEqual("visible", notice.GetProperty("collection").GetString());
        Assert.HasCount(6, notice.EnumerateObject().ToArray());
        Assert.DoesNotContain("secret", notice.GetRawText());
        (await fixture.NextPost())(); // Hidden table never enters the subscriber's queue.
        (await fixture.NextPost())();
        (await client.NextFrame())();
        await client.Api.WaitForSubscriptionDeliveryAsync().WaitAsync(TimeSpan.FromSeconds(5));
        Assert.HasCount(2, client.Notices);
        Assert.AreEqual(3, client.Http.Authorizations); // Initial grant plus both actual deliveries.
    }

    [TestMethod]
    [DataRow("unsubscribe")]
    [DataRow("close")]
    [DataRow("installation")]
    [DataRow("epoch")]
    [DataRow("epoch-reopen")]
    [DataRow("installation-replace")]
    [DataRow("generation")]
    [DataRow("renderer")]
    public async Task QueuedFramePostCannotSurviveRetirement(string retirement)
    {
        SubscriptionClient? client = null;
        await using var fixture = await Fixture.OpenAsync(observePluginData: (snapshot, topic, payload, gate) =>
            client!.Api.OfferInvalidation(snapshot, topic, payload, gate));
        using var owned = client = new SubscriptionClient(fixture);
        string id = await client.Subscribe();
        fixture.Http.StreamReplies.Enqueue(DataStream("visible"));
        fixture.StartSubscriptionStream();
        await fixture.NextConnection();
        (await fixture.NextPost())();
        Action late = await client.NextFrame();
        switch (retirement)
        {
            case "unsubscribe": await client.Call("data.unsubscribe", new { subscriptionId = id }); break;
            case "close": client.Surfaces.Close(client.Surface.SurfaceToken); break;
            case "installation": client.Surfaces.CloseForInstallation(client.Context.ProjectKey, client.Surface.PluginId); break;
            case "epoch": await fixture.Sessions.CloseAsync("subscription-epoch"); break;
            case "epoch-reopen":
                var workspace = fixture.Sessions.Current.WorkspaceId!.Value;
                await fixture.Sessions.CloseAsync("subscription-epoch-reopen");
                await fixture.Sessions.OpenAsync(workspace, WorkspaceOpenMode.ReadOnly);
                break;
            case "installation-replace":
                client.Surfaces.BindIdentity(client.Surface.SurfaceToken, client.Context, 2, "3.x", "1.0.0", "read", "read", "ui/index.html", installationId: "replacement");
                break;
            case "generation":
                var prior = fixture.Snapshot!;
                fixture.Snapshot = new ProductSidecarGenerationSnapshot(prior.RuntimeAuthority, 2, prior.Context, prior.Identity, prior.Registrations.ToArray());
                fixture.Authority.SetCurrent(fixture.Snapshot);
                break;
            case "renderer": client.FrameCurrent = false; break;
        }
        late();
        await client.Api.WaitForSubscriptionDeliveryAsync().WaitAsync(TimeSpan.FromSeconds(5));
        Assert.HasCount(0, client.Notices);
        Assert.AreEqual(0, client.Api.SubscriptionCount);
    }

    [TestMethod]
    public async Task BusySseCoalescesWhileFrameIsHeldAndRevocationClosesSubscription()
    {
        SubscriptionClient? client = null;
        await using var fixture = await Fixture.OpenAsync(observePluginData: (snapshot, topic, payload, gate) =>
            client!.Api.OfferInvalidation(snapshot, topic, payload, gate));
        using var owned = client = new SubscriptionClient(fixture);
        await client.Subscribe();
        fixture.Http.StreamReplies.Enqueue(DataStream(Enumerable.Repeat("visible", 50).ToArray()));
        fixture.StartSubscriptionStream();
        await fixture.NextConnection();
        (await fixture.NextPost())();
        Action held = await client.NextFrame();
        for (int i = 1; i < 50; i++) (await fixture.NextPost())();
        Assert.AreEqual(2, client.Http.Authorizations);
        Assert.HasCount(0, client.Notices);
        // The platform SSE has delivered every event despite the plugin frame being held.
        Assert.HasCount(50, fixture.Posted);
        client.Http.Denied = true;
        held();
        (await client.NextFrame())();
        await client.Api.WaitForSubscriptionDeliveryAsync().WaitAsync(TimeSpan.FromSeconds(5));
        Assert.HasCount(2, client.Notices);
        Assert.AreEqual("data.subscription.closed", client.Notices[1].GetProperty("event").GetString());
        Assert.AreEqual("plugin_read_denied", client.Notices[1].GetProperty("code").GetString());
        Assert.IsFalse(client.Notices[1].TryGetProperty("collection", out _));
        Assert.AreEqual(0, client.Api.SubscriptionCount);
        Assert.AreEqual(3, client.Http.Authorizations);
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task FailedAuthorizationOrStoppedSseExplicitlyClosesLiveUpdates(bool stoppedStream)
    {
        SubscriptionClient? client = null;
        await using var fixture = await Fixture.OpenAsync(observePluginData: (snapshot, topic, payload, gate) =>
            client!.Api.OfferInvalidation(snapshot, topic, payload, gate));
        using var owned = client = new SubscriptionClient(fixture);
        client.Http.Denied = true;
        var denied = await client.Call("data.subscribe", new { collection = "visible" });
        Assert.AreEqual("plugin_read_denied", denied.GetProperty("error").GetProperty("code").GetString());
        Assert.AreEqual(0, client.Api.SubscriptionCount);
        client.Http.Denied = false;
        await client.Subscribe();
        if (stoppedStream)
        {
            fixture.Http.StreamReplies.Enqueue(new(HttpStatusCode.OK)
            { Content = new StringContent("id: rt:1\nevent: unsupported\ndata: {}\n\n", Encoding.UTF8, "text/event-stream") });
        }
        else
        {
            client.Http.TransportFailure = true;
            fixture.Http.StreamReplies.Enqueue(DataStream("visible"));
        }
        fixture.StartSubscriptionStream();
        await fixture.NextConnection();
        if (stoppedStream) await fixture.NextFailure();
        (await fixture.NextPost())();
        (await client.NextFrame())();
        await client.Api.WaitForSubscriptionDeliveryAsync().WaitAsync(TimeSpan.FromSeconds(5));
        Assert.HasCount(1, client.Notices);
        Assert.AreEqual("data.subscription.closed", client.Notices[0].GetProperty("event").GetString());
        Assert.AreEqual("plugin_subscription_failed", client.Notices[0].GetProperty("code").GetString());
        Assert.DoesNotContain("private", client.Notices[0].GetRawText());
        Assert.AreEqual(0, client.Api.SubscriptionCount);
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task StoppedStreamRejectsFirstAndInflightSubscriptions(bool inflight)
    {
        SubscriptionClient? client = null;
        await using var fixture = await Fixture.OpenAsync(observePluginData: (snapshot, topic, payload, gate) =>
            client!.Api.OfferInvalidation(snapshot, topic, payload, gate));
        using var owned = client = new SubscriptionClient(fixture);
        fixture.Http.StreamReplies.Enqueue(new(HttpStatusCode.OK)
        { Content = new StringContent("id: rt:1\nevent: unsupported\ndata: {}\n\n", Encoding.UTF8, "text/event-stream") });
        Task<JsonElement>? pending = null;
        if (inflight)
        {
            client.Http.AuthorizationHold = NewSignal();
            pending = client.Call("data.subscribe", new { collection = "visible" });
            await client.Http.AuthorizationStarted.Task.WaitAsync(TimeSpan.FromSeconds(5));
        }
        fixture.StartSubscriptionStream();
        await fixture.NextConnection();
        await fixture.NextFailure();
        // Fatal is already known before the renderer notification is pumped.
        client.Http.AuthorizationHold?.TrySetResult();
        var rejected = await (pending ?? client.Call("data.subscribe", new { collection = "visible" }));
        Assert.AreEqual("plugin_subscription_failed", rejected.GetProperty("error").GetProperty("code").GetString());
        Assert.AreEqual(0, client.Api.SubscriptionCount);
        (await fixture.NextPost())();
        // A late ordinary event does not revive the stopped producer.
        client.Api.OfferInvalidation(fixture.Snapshot!, "data.changed",
            JsonSerializer.SerializeToElement(new { tableId = "visible" }), action => action());
        rejected = await client.Call("data.subscribe", new { collection = "visible" });
        Assert.AreEqual("plugin_subscription_failed", rejected.GetProperty("error").GetProperty("code").GetString());
        // A genuinely new renderer binding may start its own stream and re-admit.
        fixture.HoldStreamWithoutRecovery();
        fixture.Delivery.Retire();
        fixture.StartSubscriptionStream();
        await fixture.NextConnection();
        Assert.IsFalse((await client.Call("data.subscribe", new { collection = "visible" })).TryGetProperty("error", out _));
    }

    [TestMethod]
    public async Task ClosedCallbackCannotResubscribeToStoppedStream()
    {
        SubscriptionClient? client = null;
        await using var fixture = await Fixture.OpenAsync(observePluginData: (snapshot, topic, payload, gate) =>
            client!.Api.OfferInvalidation(snapshot, topic, payload, gate));
        using var owned = client = new SubscriptionClient(fixture);
        await client.Subscribe();
        Task<JsonElement>? resubscribe = null;
        client.OnNotice = _ => resubscribe = client.Call("data.subscribe", new { collection = "visible" });
        fixture.Http.StreamReplies.Enqueue(new(HttpStatusCode.OK)
        { Content = new StringContent("id: rt:1\nevent: unsupported\ndata: {}\n\n", Encoding.UTF8, "text/event-stream") });
        fixture.StartSubscriptionStream();
        await fixture.NextConnection();
        await fixture.NextFailure();
        (await fixture.NextPost())();
        (await client.NextFrame())();
        await client.Api.WaitForSubscriptionDeliveryAsync().WaitAsync(TimeSpan.FromSeconds(5));
        Assert.IsNotNull(resubscribe);
        Assert.AreEqual("plugin_subscription_failed", (await resubscribe).GetProperty("error").GetProperty("code").GetString());
        Assert.AreEqual(0, client.Api.SubscriptionCount);
    }

    [TestMethod]
    public async Task GenerationAdmissionNeverRecapturesOwningSupervisor()
    {
        SubscriptionClient? client = null;
        await using var fixture = await Fixture.OpenAsync(observePluginData: (snapshot, topic, payload, gate) =>
            client!.Api.OfferInvalidation(snapshot, topic, payload, gate));
        // Use the production admission implementation; detect the forbidden callback at
        // the lock boundary instead of constructing an uninterruptible deadlock.
        var type = typeof(PocketBaseAdminContext).Assembly.GetType("VibeTable.Infrastructure.PocketBase.PocketBaseGenerationContext")!;
        var context = Activator.CreateInstance(type,
            System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic, null,
            new object[] { 1L, fixture.Snapshot!.Context }, null)!;
        var gate = type.GetMethod("TryUseCurrent", System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!
            .CreateDelegate<Func<Func<bool>, bool>>(context);
        object admission = type.GetField("_admissionGate", System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!.GetValue(context)!;
        var prior = fixture.Snapshot;
        fixture.Snapshot = new(prior.RuntimeAuthority, prior.SidecarGenerationId, prior.Context, prior.Identity, prior.Registrations.ToArray(), gate);
        fixture.Authority.SetCurrent(fixture.Snapshot);
        int forbiddenCaptures = 0;
        using var owned = client = new SubscriptionClient(fixture, capture: () =>
        {
            if (Monitor.IsEntered(admission))
            {
                Interlocked.Increment(ref forbiddenCaptures);
                throw new InvalidOperationException("Supervisor capture under generation admission.");
            }
            return fixture.Snapshot;
        });
        var result = await client.Call("data.subscribe", new { collection = "visible" });
        Assert.AreEqual(0, forbiddenCaptures);
        Assert.IsFalse(result.TryGetProperty("error", out _));
        fixture.Http.StreamReplies.Enqueue(DataStream("visible"));
        fixture.StartSubscriptionStream();
        await fixture.NextConnection();
        (await fixture.NextPost())();
        (await client.NextFrame())();
        await client.Api.WaitForSubscriptionDeliveryAsync().WaitAsync(TimeSpan.FromSeconds(5));
        Assert.AreEqual(0, forbiddenCaptures);
        Assert.HasCount(1, client.Notices);
        // Retirement still seals both new admissions and queued actual posts.
        client.Api.OfferInvalidation(fixture.Snapshot!, "data.changed",
            JsonSerializer.SerializeToElement(new { tableId = "visible" }),
            action => fixture.Authority.TryUseCurrent(fixture.Snapshot!, action));
        Action queued = await client.NextFrame();
        type.GetMethod("Retire", System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!.Invoke(context, null);
        queued();
        await client.Api.WaitForSubscriptionDeliveryAsync().WaitAsync(TimeSpan.FromSeconds(5));
        Assert.HasCount(1, client.Notices);
        Assert.AreEqual(0, forbiddenCaptures);
        Assert.IsTrue((await client.Call("data.subscribe", new { collection = "visible" })).TryGetProperty("error", out _));
    }

    [TestMethod]
    public async Task SubscriptionBudgetsOwnershipAndRepeatedMountsReleaseExactly()
    {
        await using var fixture = await Fixture.OpenAsync();
        using var client = new SubscriptionClient(fixture);
        var surfaces = new List<PluginSurfaceSession> { client.Surface };
        var ids = new List<string>();
        for (int page = 0; page < 8; page++)
        {
            var surface = page == 0 ? client.Surface : client.OpenSurface();
            if (page > 0) surfaces.Add(surface);
            for (int i = 0; i < 8; i++) ids.Add(await client.Subscribe(surface));
            var denied = await client.Call("data.subscribe", new { collection = "visible" }, surface);
            Assert.AreEqual("plugin_subscription_limit", denied.GetProperty("error").GetProperty("code").GetString());
        }
        Assert.AreEqual(64, client.Api.SubscriptionCount);
        var overflow = client.OpenSurface();
        Assert.AreEqual("plugin_subscription_limit", (await client.Call("data.subscribe", new { collection = "visible" }, overflow)).GetProperty("error").GetProperty("code").GetString());
        await client.Call("data.unsubscribe", new { subscriptionId = ids[0] }, overflow);
        Assert.AreEqual(64, client.Api.SubscriptionCount); // A handle cannot be transferred to another surface.
        foreach (var surface in surfaces) client.Surfaces.Close(surface.SurfaceToken);
        Assert.AreEqual(0, client.Api.SubscriptionCount);
        for (int i = 0; i < 25; i++)
        {
            var surface = client.OpenSurface();
            await client.Subscribe(surface);
            client.Surfaces.Close(surface.SurfaceToken);
            Assert.AreEqual(0, client.Api.SubscriptionCount);
        }
        var active = client.OpenSurface();
        await client.Subscribe(active);
        // A completed subscribe can still be cancelled by its original request id.
        await client.Call("request.cancel", new { requestId = client.LastSubscribeRequest }, active);
        Assert.AreEqual(0, client.Api.SubscriptionCount);
    }

    [TestMethod]
    public async Task UnsubscribeCannotBeBlockedByTheNormalRequestBudget()
    {
        await using var fixture = await Fixture.OpenAsync();
        using var client = new SubscriptionClient(fixture);
        string id = await client.Subscribe();
        client.Http.DescribeHold = new(TaskCreationOptions.RunContinuationsAsynchronously);
        Task<JsonElement>[] reads = Enumerable.Range(0, 8).Select(_ => client.Call("data.describe", new { })).ToArray();
        await client.Http.EightDescribes.Task.WaitAsync(TimeSpan.FromSeconds(5));
        try
        {
            var result = await client.Call("data.unsubscribe", new { subscriptionId = id });
            Assert.IsFalse(result.TryGetProperty("error", out _));
            Assert.AreEqual(0, client.Api.SubscriptionCount);
        }
        finally { client.Http.DescribeHold.TrySetResult(); await Task.WhenAll(reads); }
    }

    private static HttpResponseMessage DataStream(params string[] tables)
    {
        string text = string.Concat(tables.Select((table, index) => $"id: rt:{index + 1}\nevent: data.changed\ndata: "
            + JsonSerializer.Serialize(new { contractVersion = "2.0", topic = "data.changed", tableId = table,
                recordIds = new[] { "secret-row" }, hiddenField = "secret-value" }) + "\n\n"));
        var content = new StreamContent(new TestSseStream(Encoding.UTF8.GetBytes(text), blockAtEnd: true));
        content.Headers.ContentType = new("text/event-stream");
        return new(HttpStatusCode.OK) { Content = content };
    }

    [TestMethod]
    public async Task CatalogOrdering_ImportRefreshWaitsForAnOlderPausedCreateRefresh()
    {
        var tableGateway = new FakeTableRpcGateway();
        tableGateway.DatabaseOpenResults["db"] = new DatabaseOpenResult([], [], TestDisplayNames.For());
        tableGateway.SelectionProjectionResults["fresh-table"] = new TableSelectionProjection(
            new TablePage("fresh-table", [], [], 0, 500, 0, "remote"),
            new EditSchemaResult("fresh-table", "schema_0001", "id", true, false, []));
        var workspace = new TableWorkspaceService(tableGateway);
        await workspace.OpenDatabaseAsync("db");
        await using var fixture = await Fixture.OpenAsync(
            applyCatalog: workspace.UpdateKnownCatalog, workspace: workspace);
        fixture.HoldStreamWithoutRecovery();
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
        await fixture.NextConnection();

        // The renderer create pauses its catalog refresh read: it observed the
        // catalog BEFORE the source import committed.
        var createRead = new TaskCompletionSource<TableSummary>(TaskCreationOptions.RunContinuationsAsynchronously);
        int createReads = 0;
        tableGateway.ListTablesOverride = _ =>
        {
            Interlocked.Increment(ref createReads);
            return createRead.Task;
        };
        var sink = new FakeWebReplySink();
        await using var createClient = new JsonRpcClient(new SchemaCreateTransport());
        using var createGateway = new JsonRpcProductDataGateway(createClient);
        var controller = new WorkspaceTableRequestController(
            workspace, new FakeDatabasePicker("db"), sink, () => createGateway,
            NoDatabaseOpenRoute.Instance);
        Task create = controller.DispatchAsync(new RoutedWebRequest(
            "tableAdmin.createRequested", "create-1",
            JsonSerializer.SerializeToElement(new { displayName = "订单" }), string.Empty));
        while (Volatile.Read(ref createReads) == 0) await Task.Yield();

        // The import refresh competes for the same catalog. It must not start
        // its authoritative read until the paused create turn publishes.
        Task refresh = fixture.Owner.RefreshCatalogAsync(fixture.Snapshot!, CancellationToken.None);
        for (int i = 0; i < 50 && fixture.Http.CatalogReads == 0; i++)
            await Task.Delay(10);
        Assert.AreEqual(0, fixture.Http.CatalogReads,
            "the import refresh read must wait for the paused create turn");

        // The create turn resumes with its older snapshot and publishes first.
        createRead.TrySetResult(new TableSummary(
            ["tbl_created"], [], TestDisplayNames.For("tbl_created")));
        await create.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.HasCount(2, sink.Replies.Where(reply =>
            reply.Type == "database.collectionsChanged").ToArray());

        // Only now may the queued refresh read and publish the newer catalog.
        Action importPost = await fixture.NextPost();
        importPost();
        await refresh.WaitAsync(TimeSpan.FromSeconds(5));

        Assert.IsTrue(await workspace.SelectTableAsync("fresh-table"),
            "the newest catalog must win after both entries settled");
        string createResponse = JsonSerializer.Serialize(sink.Replies
            .Single(reply => reply.RequestId == "create-1").Payload);
        StringAssert.Contains(createResponse, "tbl_created");
    }

    [TestMethod]
    public async Task CatalogOrdering_CreateRefreshWaitsForAHeldImportRefreshPublish()
    {
        var tableGateway = new FakeTableRpcGateway();
        tableGateway.DatabaseOpenResults["db"] = new DatabaseOpenResult([], [], TestDisplayNames.For());
        tableGateway.SelectionProjectionResults["fresh-table"] = new TableSelectionProjection(
            new TablePage("fresh-table", [], [], 0, 500, 0, "remote"),
            new EditSchemaResult("fresh-table", "schema_0001", "id", true, false, []));
        tableGateway.SelectionProjectionResults["tbl_created"] = new TableSelectionProjection(
            new TablePage("tbl_created", [], [], 0, 500, 0, "remote"),
            new EditSchemaResult("tbl_created", "schema_0001", "id", true, false, []));
        var workspace = new TableWorkspaceService(tableGateway);
        await workspace.OpenDatabaseAsync("db");
        await using var fixture = await Fixture.OpenAsync(
            applyCatalog: workspace.UpdateKnownCatalog, workspace: workspace);
        fixture.HoldStreamWithoutRecovery();
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
        await fixture.NextConnection();

        // The import refresh reads the newer catalog but its renderer publish
        // is held at the delivery pump.
        Task refresh = fixture.Owner.RefreshCatalogAsync(fixture.Snapshot!, CancellationToken.None);
        Action importPost = await fixture.NextPost();

        // The create must not read while the import publish is still in flight.
        var createRead = new TaskCompletionSource<TableSummary>(TaskCreationOptions.RunContinuationsAsynchronously);
        int createReads = 0;
        tableGateway.ListTablesOverride = _ =>
        {
            Interlocked.Increment(ref createReads);
            return createRead.Task;
        };
        var sink = new FakeWebReplySink();
        await using var createClient = new JsonRpcClient(new SchemaCreateTransport());
        using var createGateway = new JsonRpcProductDataGateway(createClient);
        var controller = new WorkspaceTableRequestController(
            workspace, new FakeDatabasePicker("db"), sink, () => createGateway,
            NoDatabaseOpenRoute.Instance);
        Task create = controller.DispatchAsync(new RoutedWebRequest(
            "tableAdmin.createRequested", "create-2",
            JsonSerializer.SerializeToElement(new { displayName = "订单" }), string.Empty));
        for (int i = 0; i < 50 && Volatile.Read(ref createReads) == 0; i++)
            await Task.Delay(10);
        Assert.AreEqual(0, createReads,
            "the create refresh read must wait for the import publish turn");

        importPost();
        await refresh.WaitAsync(TimeSpan.FromSeconds(5));

        // The create turn now reads the authoritative post-import catalog.
        createRead.TrySetResult(new TableSummary(
            ["fresh-table", "tbl_created"], ["fresh-view"],
            new Dictionary<string, string>
            {
                ["fresh-table"] = "Fresh",
                ["fresh-view"] = "Fresh view",
                ["tbl_created"] = "订单",
            }));
        await create.WaitAsync(TimeSpan.FromSeconds(5));

        Assert.IsTrue(await workspace.SelectTableAsync("fresh-table"));
        Assert.IsTrue(await workspace.SelectTableAsync("tbl_created"),
            "the reverse order must keep the create selection");
    }

    [TestMethod]
    public async Task CatalogOrdering_RecoveredRefreshWaitsForAnOlderPausedCreateRefresh()
    {
        var tableGateway = new FakeTableRpcGateway();
        tableGateway.DatabaseOpenResults["db"] = new DatabaseOpenResult([], [], TestDisplayNames.For());
        tableGateway.SelectionProjectionResults["fresh-table"] = new TableSelectionProjection(
            new TablePage("fresh-table", [], [], 0, 500, 0, "remote"),
            new EditSchemaResult("fresh-table", "schema_0001", "id", true, false, []));
        var workspace = new TableWorkspaceService(tableGateway);
        await workspace.OpenDatabaseAsync("db");
        await using var fixture = await Fixture.OpenAsync(
            applyCatalog: workspace.UpdateKnownCatalog, workspace: workspace);

        // The renderer create pauses its catalog refresh read first.
        var createRead = new TaskCompletionSource<TableSummary>(TaskCreationOptions.RunContinuationsAsynchronously);
        int createReads = 0;
        tableGateway.ListTablesOverride = _ =>
        {
            Interlocked.Increment(ref createReads);
            return createRead.Task;
        };
        var sink = new FakeWebReplySink();
        await using var createClient = new JsonRpcClient(new SchemaCreateTransport());
        using var createGateway = new JsonRpcProductDataGateway(createClient);
        var controller = new WorkspaceTableRequestController(
            workspace, new FakeDatabasePicker("db"), sink, () => createGateway,
            NoDatabaseOpenRoute.Instance);
        Task create = controller.DispatchAsync(new RoutedWebRequest(
            "tableAdmin.createRequested", "create-3",
            JsonSerializer.SerializeToElement(new { displayName = "订单" }), string.Empty));
        while (Volatile.Read(ref createReads) == 0) await Task.Yield();

        // The SSE reconnect recovery competes for the same catalog.
        fixture.Delivery.SetReady(RendererReadyPhase.Business);
        Assert.IsNull(await fixture.NextConnection());
        for (int i = 0; i < 50 && fixture.Http.CatalogReads == 0; i++)
            await Task.Delay(10);
        Assert.AreEqual(0, fixture.Http.CatalogReads,
            "the recovered catalog read must wait for the paused create turn");

        createRead.TrySetResult(new TableSummary(
            ["tbl_created"], [], TestDisplayNames.For("tbl_created")));
        await create.WaitAsync(TimeSpan.FromSeconds(5));

        Action recoveredPost = await fixture.NextPost();
        recoveredPost();
        await fixture.Delayed.Task.WaitAsync(TimeSpan.FromSeconds(5));

        Assert.IsTrue(await workspace.SelectTableAsync("fresh-table"),
            "the recovered refresh must land after the create turn");
        CollectionAssert.AreEqual(new[] { "database.collectionsChanged", "realtime.recovered" },
            fixture.Posted.Select(item => item.Topic).ToArray());
    }

    private sealed class SchemaCreateTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming = Channel.CreateUnbounded<JsonElement?>();

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            using var request = JsonDocument.Parse(line);
            string id = request.RootElement.GetProperty("id").GetString()!;
            string method = request.RootElement.GetProperty("method").GetString()!;
            Assert.AreEqual("schema.table.create", method);
            JsonElement parameters = request.RootElement.GetProperty("params").Clone();
            JsonElement result = JsonSerializer.SerializeToElement(new
            {
                contract = "vibetable.schema.v2",
                operationId = parameters.GetProperty("operationId").GetString(),
                tableId = "tbl_created",
                displayName = parameters.GetProperty("displayName").GetString(),
                schemaRevision = "schema_0001",
            });
            _incoming.Writer.TryWrite(JsonSerializer.SerializeToElement(new
            {
                jsonrpc = "2.0",
                id,
                result,
            }));
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }

    private sealed class SubscriptionClient : IDisposable, IWebReplySink
    {
        internal readonly PluginSurfaceSessionManager Surfaces = new();
        internal readonly SubscriptionPeer Http = new();
        internal readonly List<JsonElement> Notices = [];
        private readonly Channel<Action> _frames = Channel.CreateUnbounded<Action>();
        private readonly ProductAuthorityEpoch _authority = new();
        internal PluginProjectContext Context { get; }
        internal PluginSurfaceSession Surface { get; }
        internal PluginSurfaceApi Api { get; }
        internal bool FrameCurrent = true;
        internal string? LastSubscribeRequest;
        internal Action<JsonElement>? OnNotice;
        internal SubscriptionClient(Fixture fixture, Func<ProductSidecarGenerationSnapshot?>? capture = null)
        {
            Context = PluginProjectContext.FromSession(fixture.Sessions.Current)!;
            _authority.Transition(Context);
            var tasks = new HostPluginTaskRegistry(_authority);
            tasks.SetWorkspaceContext(Context);
            // Establish the real ready binding, holding only the synthetic HTTP stream
            // until the test supplies its response. Admission always uses the real owner.
            fixture.Http.StreamStart = NewSignal();
            fixture.Delivery.SetReady(RendererReadyPhase.Business);
            Api = new(Surfaces, tasks, capture ?? (() => fixture.Snapshot), this, Http,
                subscriptionSource: fixture.Owner.TryUseSubscriptionSource);
            Surface = OpenSurface();
        }
        internal PluginSurfaceSession OpenSurface()
        {
            var opened = Surfaces.Open(Context.ProjectKey, "com.example.subscription",
                PluginPackageRevision.Create("C:/plugins/synthetic", new string('a', 64)), "ui/index.html");
            Surfaces.BindIdentity(opened.SurfaceToken, Context, 1, "3.x", "1.0.0", "read", "read", "ui/index.html", installationId: "installation");
            Surfaces.TryGet(opened.SurfaceToken, out var surface);
            Api.BindFrameNotifier(opened.SurfaceToken, ProductWebViewBridge.CreatePluginFrameNotifier(async (action, token) =>
            {
                var completion = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
                await _frames.Writer.WriteAsync(() => { action(); completion.TrySetResult(); }, token);
                await completion.Task.WaitAsync(token);
            }, () => FrameCurrent, json =>
            {
                var notice = JsonDocument.Parse(json).RootElement.Clone();
                Notices.Add(notice);
                OnNotice?.Invoke(notice);
            }));
            return surface!;
        }
        internal async Task<JsonElement> Call(string method, object parameters, PluginSurfaceSession? surface = null)
        {
            surface ??= Surface;
            string requestId = Guid.NewGuid().ToString("N");
            if (method == "data.subscribe") LastSubscribeRequest = requestId;
            string wire = JsonSerializer.Serialize(new { contract = PluginSurfaceApi.Contract, surfaceToken = surface.SurfaceToken,
                requestId, method, @params = parameters });
            string result = await Api.HandleAsync(surface.SurfaceToken, surface.DocumentUri.AbsoluteUri, wire);
            return JsonDocument.Parse(result).RootElement.Clone();
        }
        internal async Task<string> Subscribe(PluginSurfaceSession? surface = null)
            => (await Call("data.subscribe", new { collection = "visible" }, surface)).GetProperty("result").GetProperty("subscriptionId").GetString()!;
        internal async Task<Action> NextFrame() => await _frames.Reader.ReadAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(5));
        public void Dispose() { Api.Dispose(); _authority.Dispose(); }
        public void PostNotification(string type, object? payload) { }
        public void PostResponse(string type, string? requestId, object? payload) { }
        public void PostOperationFailed(string? requestId, string message, string? code = null, string? operation = null, string? operationId = null) { }
    }

    private sealed class SubscriptionPeer : HttpMessageHandler
    {
        internal int Authorizations;
        internal TaskCompletionSource? AuthorizationHold;
        internal readonly TaskCompletionSource AuthorizationStarted = NewSignal();
        internal bool Denied;
        internal bool TransportFailure;
        internal TaskCompletionSource? DescribeHold;
        internal readonly TaskCompletionSource EightDescribes = new(TaskCreationOptions.RunContinuationsAsynchronously);
        private int _describes;
        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token)
        {
            var call = JsonDocument.Parse(await request.Content!.ReadAsStringAsync(token)).RootElement;
            if (call.GetProperty("method").GetString() == "data.describe")
            {
                if (Interlocked.Increment(ref _describes) == 8) EightDescribes.TrySetResult();
                if (DescribeHold is { } hold) await hold.Task.WaitAsync(token);
                return new(HttpStatusCode.OK) { Content = new StringContent("{}") };
            }
            Assert.AreEqual("data.authorizeSubscription", call.GetProperty("method").GetString());
            Assert.AreEqual("installation", call.GetProperty("installationId").GetString());
            Interlocked.Increment(ref Authorizations);
            AuthorizationStarted.TrySetResult();
            if (AuthorizationHold is { } authorizationHold) await authorizationHold.Task.WaitAsync(token);
            if (TransportFailure) throw new HttpRequestException("private transport detail must not reach a plugin");
            return new(Denied ? HttpStatusCode.Conflict : HttpStatusCode.OK)
            { Content = new StringContent(Denied ? "{\"code\":\"plugin_read_denied\"}" : "{\"collection\":\"visible\"}") };
        }
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

        internal static async Task<Fixture> OpenAsync(bool initiallyClosed = false,
            Func<JsonElement, PluginEventEnvelope>? projectPluginCatalog = null,
            Action<TableSummary>? applyCatalog = null,
            Action<ProductSidecarGenerationSnapshot, string, JsonElement, Func<Func<bool>, bool>>? observePluginData = null,
            TableWorkspaceService? workspace = null)
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
            WorkspaceSessionV2 session = initiallyClosed
                ? fixture.Sessions.Current with { WorkspaceId = layout.Manifest.WorkspaceId, SessionEpoch = 1 }
                : await fixture.Sessions.OpenAsync(layout.Manifest.WorkspaceId, WorkspaceOpenMode.ReadOnly);
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
                fixture.Sessions, fixture.Leases, fixture.Delivery, applyCatalog ?? (_ => { }),
                code => fixture.Failures.Writer.TryWrite(code), fixture.Http, async (_, token) =>
                {
                    fixture.Delayed.TrySetResult();
                    await fixture.ResumeDelay.Task.WaitAsync(token);
                }, projectPluginCatalog, observePluginData,
                workspace is null ? null : workspace.BeginCatalogTurn);
            return fixture;
        }

        internal void StartSubscriptionStream()
        {
            Delivery.SetReady(RendererReadyPhase.Business);
            Http.StreamStart!.TrySetResult();
        }

        internal void HoldStreamWithoutRecovery()
        {
            var content = new StreamContent(new TestSseStream([], blockAtEnd: true));
            content.Headers.ContentType = new("text/event-stream");
            Http.StreamReplies.Enqueue(new HttpResponseMessage(HttpStatusCode.OK) { Content = content });
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
        internal TaskCompletionSource? StreamStart;
        internal Channel<string?> Connections { get; } = Channel.CreateUnbounded<string?>();
        internal int CatalogReads { get; private set; }
        internal bool CatalogFailure { get; set; }
        internal TaskCompletionSource? CatalogReadEntered;
        internal TaskCompletionSource? CatalogReadRelease;
        internal List<TestSseStream> Streams { get; } = [];
        internal Queue<HttpResponseMessage> StreamReplies { get; } = new();

        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token)
        {
            if (request.RequestUri!.AbsolutePath.EndsWith("/events", StringComparison.Ordinal))
            {
                if (StreamStart is { } start) await start.Task.WaitAsync(token);
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
            CatalogReadEntered?.TrySetResult();
            if (CatalogReadRelease is { } release) await release.Task;
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
