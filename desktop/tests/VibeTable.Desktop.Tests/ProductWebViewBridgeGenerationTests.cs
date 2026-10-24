using System.IO;
using System.Text.Json;
using System.Windows;
using System.Windows.Threading;
using Microsoft.Web.WebView2.Wpf;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Desktop.ViewModels;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Backend;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// #482 renderer-generation regression: replacing the control or sealing for
/// recovery retires the generation, and a notification queued by the OLD
/// generation is dropped instead of being posted to a newer renderer.
/// </summary>
[TestClass]
public sealed class ProductWebViewBridgeGenerationTests
{
    private sealed class StaBridge : IDisposable
    {
        private readonly Thread _thread;
        private readonly TaskCompletionSource _ready = new(TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource _stopped = new(TaskCreationOptions.RunContinuationsAsynchronously);
        private ProductWebViewBridge? _bridge;
        private Window? _window;
        private WebView2? _control;
        public readonly List<Action> Queued = [];
        public readonly List<bool> Commits = [];
        private readonly object _captureGate = new();

        public StaBridge()
        {
            _thread = new Thread(() =>
            {
                try
                {
                    _window = new Window();
                    _control = new WebView2();
                    _bridge = new ProductWebViewBridge(
                        _window,
                        _control,
                        new WebMessageRouter(_ => { }),
                        new PluginWebViewResourceHost(new PluginResourceHost(), new PluginSurfaceSessionManager()),
                        readiness: null,
                        processFailed: _ => { });
                    _bridge.UiEmission = action => { lock (_captureGate) Queued.Add(action); };
                    _bridge.EmissionCommitted += posted => { lock (_captureGate) Commits.Add(posted); };
                    _ready.TrySetResult();
                    Dispatcher.Run();
                }
                finally
                {
                    _stopped.TrySetResult();
                }
            });
            _thread.SetApartmentState(ApartmentState.STA);
            _thread.IsBackground = true;
            _thread.Start();
            if (!_ready.Task.Wait(TimeSpan.FromSeconds(10)))
            {
                throw new TimeoutException("STA bridge construction timed out.");
            }
        }

        public ProductWebViewBridge Bridge => _bridge!;
        public WebView2 Control => _control!;

        public WebView2 CreateReplacementControl()
        {
            return _window!.Dispatcher.Invoke(() => new WebView2())
                ?? throw new InvalidOperationException("replacement control creation failed");
        }

        internal Task DispatchAsync(Action action, CancellationToken token)
            => _window!.Dispatcher.InvokeAsync(action, DispatcherPriority.Normal, token).Task;

        internal void UseDispatcherQueue()
            => Bridge.UiEmission = action => _window!.Dispatcher.BeginInvoke(action);

        public void PumpQueued()
        {
            Action[] pending;
            lock (_captureGate)
            {
                pending = Queued.ToArray();
                Queued.Clear();
            }
            foreach (Action action in pending)
            {
                action();
            }
        }

        public void Dispose()
        {
            _window?.Dispatcher.InvokeShutdown();
            if (!_stopped.Task.Wait(TimeSpan.FromSeconds(10)))
            {
                throw new TimeoutException("STA bridge shutdown timed out.");
            }
            _window = null;
            _control = null;
            _bridge = null;
        }
    }

    private sealed class UnusedPackagePicker : IPluginPackageSourcePicker
    {
        public Task<string?> PickAsync(PluginPackagePickKind kind, CancellationToken token)
            => throw new InvalidOperationException("History replay must not pick a package.");
    }

    [TestMethod]
    [DataRow("current")]
    [DataRow("closed")]
    [DataRow("new-epoch")]
    public async Task RecoveredReceiptKeepsItsSessionGateUntilTheActualBridgeQueuePump(string transition)
    {
        var (manager, filter, root) = await PluginRequestDispatcherTests.OpenRetirementSessionAsync();
        try
        {
            using var harness = new StaBridge();
            var original = PluginProjectContext.FromSession(manager.Current)!;
            PluginProjectContext? current = original;
            Guid workspaceId = manager.Current.WorkspaceId!.Value;
            var surfaces = new PluginSurfaceSessionManager();
            using var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            var pending = new TaskCompletionSource<JsonElement>(TaskCreationOptions.RunContinuationsAsynchronously);
            int gateCalls = 0;
            using var dispatcher = new PluginRequestDispatcher(harness.Bridge, surfaces, new UnusedPackagePicker(), resources,
                githubSource: null, projectContext: () => current,
                sharedRpc: (_, _, _) => pending.Task,
                sharedReadEmitGate: captured =>
                {
                    var gate = PluginRequestDispatcher.CaptureSharedReadEmitGate(manager, filter, captured);
                    return post => { gateCalls++; return gate(post); };
                });
            dispatcher.SetProjectContext(original); dispatcher.SetWorkspaceContext(original);
            var binding = dispatcher.Tasks.CaptureNative()!;
            dispatcher.Tasks.NativeTask(binding, new PluginRuntimeTaskSnapshot("recovered", "recovered-run", "com.acme.original", "1", "save",
                original.ProjectKey, null, 1, PluginRisk.Write, "running", false, null, null, null,
                InstallationId: "original-installation", InstallationRevision: 2, SurfaceToken: "retired-surface"));
            dispatcher.Tasks.FinishNative("recovered", "succeeded");
            using var payload = JsonDocument.Parse(JsonSerializer.Serialize(new { projectKey = original.ProjectKey }));
            Task reading = dispatcher.DispatchAsync(new RoutedWebRequest("plugin.catalog.list", "new-renderer-catalog", payload.RootElement.Clone(), ""));
            try
            {
                Assert.AreEqual(1, gateCalls, "history was admitted before entering the real bridge UI queue");
                Assert.HasCount(1, harness.Queued);
                long rendererGeneration = harness.Bridge.RendererGeneration;
                if (transition != "current")
                {
                    await manager.CloseAsync("retire-queued-history"); current = null;
                    if (transition == "new-epoch")
                    {
                        await manager.OpenAsync(workspaceId, WorkspaceOpenMode.Writable);
                        current = PluginProjectContext.FromSession(manager.Current)!;
                        Assert.AreEqual(original.ProjectKey, current.ProjectKey);
                        Assert.IsGreaterThan(original.SessionGeneration, current.SessionGeneration);
                        dispatcher.SetWorkspaceContext(current);
                    }
                }
                Assert.AreEqual(rendererGeneration, harness.Bridge.RendererGeneration, "this exercises session retirement within one renderer");
                harness.PumpQueued();
                Assert.AreEqual(2, gateCalls, "the captured session gate must execute again at the actual bridge emission moment");
                if (transition == "current")
                {
                    // No CoreWebView2 is started in this synthetic test. Reaching
                    // commit proves the unchanged session admits the actual post;
                    // the real dispatcher receipt payload is covered separately.
                    Assert.HasCount(1, harness.Commits);
                }
                else Assert.IsEmpty(harness.Commits, "a retired session must not even reach the wire-post callback");
            }
            finally
            {
                pending.TrySetResult(JsonSerializer.SerializeToElement(Array.Empty<PluginRuntimeSnapshot>()));
                await reading;
                harness.PumpQueued();
            }
        }
        finally
        {
            await manager.DisposeAsync();
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    [DataRow("current")]
    [DataRow("authority")]
    [DataRow("epoch")]
    [DataRow("drain-resume")]
    [DataRow("renderer")]
    public async Task OpenedCatalogKeepsItsAdmissionAfterTerminalReleaseUntilTheRealQueuePump(string transition)
    {
        var (manager, filter, root) = await PluginRequestDispatcherTests.OpenRetirementSessionAsync();
        try
        {
            using var harness = new StaBridge();
            using var authority = new ProductAuthorityEpoch();
            using var opens = new PluginProjectContextBindingRegistry(authority);
            PluginProjectContext context = PluginProjectContext.FromSession(manager.Current)!;
            authority.Transition(context);
            opens.SetAfterAuthorityTransition(context);
            var gateway = new FakeTableRpcGateway();
            gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(["orders"], [], TestDisplayNames.For("orders"));
            var workspace = new TableWorkspaceService(gateway);
            var controller = new WorkspaceTableRequestController(workspace, new FakeDatabasePicker("db"),
                harness.Bridge, () => null, pluginBindings: opens, sessionEnvelopeFilter: filter);
            await controller.DispatchAsync(new RoutedWebRequest("database.openRequested", null,
                JsonSerializer.SerializeToElement(new { openId = "queued-open" }), ""));
            Assert.HasCount(1, harness.Queued, "the logical open has completed and released before the pump");
            if (transition == "authority")
            {
                authority.Transition(context);
                opens.SetAfterAuthorityTransition(context);
            }
            else if (transition == "epoch") await manager.CloseAsync("retire-queued-catalog");
            else if (transition == "drain-resume")
            {
                Guid id = manager.Current.WorkspaceId!.Value;
                ulong epoch = manager.Current.SessionEpoch;
                await filter.DrainAsync(id, epoch, CancellationToken.None).WaitAsync(TimeSpan.FromSeconds(2));
                filter.Resume(id, epoch);
            }
            else if (transition == "renderer") harness.Bridge.SealForRendererRecovery();
            harness.PumpQueued();
            if (transition is "current" or "renderer")
                Assert.HasCount(1, harness.Commits);
            else
                Assert.IsEmpty(harness.Commits, "a terminal's released operation token cannot revive its retired admission");
        }
        finally
        {
            await manager.DisposeAsync();
            filter.Dispose();
            Directory.Delete(root, recursive: true);
        }
    }
    [TestMethod]
    [DataRow("create", "current")]
    [DataRow("create", "epoch")]
    [DataRow("create", "drain-resume")]
    [DataRow("create", "renderer")]
    [DataRow("delete", "current")]
    [DataRow("delete", "epoch")]
    [DataRow("delete", "drain-resume")]
    [DataRow("delete", "renderer")]
    public async Task TableAdminCatalogResponsesAndCompanionBroadcastsKeepTheirRealPumpScope(string operation, string transition)
    {
        var (manager, filter, root) = await PluginRequestDispatcherTests.OpenRetirementSessionAsync();
        try
        {
            using var harness = new StaBridge();
            var tables = new FakeTableRpcGateway
            {
                ListTablesResult = new TableSummary(["tbl_created"], ["view_recent"],
                    new Dictionary<string, string> { ["tbl_created"] = "Orders", ["view_recent"] = "Recent" }),
            };
            await using var client = new JsonRpcClient(new WorkspaceTableRequestControllerTests.SchemaCaptureTransport());
            using var gateway = new JsonRpcProductDataGateway(client);
            var workspace = new TableWorkspaceService(tables);
            var controller = new WorkspaceTableRequestController(workspace, new FakeDatabasePicker("db"), harness.Bridge,
                () => gateway, NoDatabaseOpenRoute.Instance, sessionEnvelopeFilter: filter);
            await controller.DispatchAsync(new RoutedWebRequest($"tableAdmin.{operation}Requested", "queued-admin",
                JsonSerializer.SerializeToElement(operation == "create"
                    ? (object)new { displayName = "Orders" } : new { collection = "tbl_orders" }), "",
                new WorkspaceWireScope
                {
                    Scope = "workspace", WorkspaceId = manager.Current.WorkspaceId!.Value,
                    SessionEpoch = manager.Current.SessionEpoch, OperationId = Guid.NewGuid(), Sequence = 1,
                }));
            Assert.HasCount(2, harness.Queued, "both the correlated receipt and its broadcast must use the real queue");
            if (transition == "epoch") await manager.CloseAsync("retire-admin");
            else if (transition == "drain-resume")
            {
                Guid id = manager.Current.WorkspaceId!.Value;
                ulong epoch = manager.Current.SessionEpoch;
                await filter.DrainAsync(id, epoch, CancellationToken.None).WaitAsync(TimeSpan.FromSeconds(2));
                filter.Resume(id, epoch);
            }
            else if (transition == "renderer") harness.Bridge.SealForRendererRecovery();
            harness.PumpQueued();
            if (transition is "current" or "renderer") Assert.HasCount(2, harness.Commits);
            else Assert.IsEmpty(harness.Commits);
        }
        finally
        {
            await manager.DisposeAsync();
            filter.Dispose();
            Directory.Delete(root, recursive: true);
        }
    }
    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task CatalogTurnsOrderOrdinaryEnqueuesAndRealtimePostsOnTheActualDispatcher(bool realtimeFirst)
    {
        using var harness = new StaBridge();
        harness.UseDispatcherQueue();
        var order = new System.Collections.Concurrent.ConcurrentQueue<string>();
        harness.Bridge.EmissionCommitted += _ => order.Enqueue("admin");
        var tables = new FakeTableRpcGateway
        {
            ListTablesResult = new TableSummary(["tbl_created"], [], TestDisplayNames.For("tbl_created")),
        };
        var workspace = new TableWorkspaceService(tables);
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var dispatched = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var delivery = new ProductRealtimeDelivery(async (action, token) =>
        {
            dispatched.TrySetResult();
            if (realtimeFirst) await release.Task.WaitAsync(token);
            await harness.DispatchAsync(action, token);
        }, () => true, (_, _) => order.Enqueue("realtime"));
        delivery.SetReady(RendererReadyPhase.Business);
        async Task Refresh()
        {
            using var turn = workspace.BeginCatalogTurn();
            await turn.EnterAsync(CancellationToken.None);
            await delivery.PostAsync(0, post =>
            {
                workspace.UpdateKnownCatalog(tables.ListTablesResult);
                post("database.collectionsChanged", new { });
                return true;
            }, CancellationToken.None);
        }
        await using var client = new JsonRpcClient(new WorkspaceTableRequestControllerTests.SchemaCaptureTransport());
        using var gateway = new JsonRpcProductDataGateway(client);
        var controller = new WorkspaceTableRequestController(workspace, new FakeDatabasePicker("db"), harness.Bridge,
            () => gateway, NoDatabaseOpenRoute.Instance);
        Task? refresh = realtimeFirst ? Refresh() : null;
        if (realtimeFirst) await dispatched.Task.WaitAsync(TimeSpan.FromSeconds(2));
        Task create = controller.DispatchAsync(new RoutedWebRequest("tableAdmin.createRequested", "ordered-create",
            JsonSerializer.SerializeToElement(new { displayName = "Orders" }), ""));
        if (realtimeFirst)
        {
            Assert.IsFalse(create.IsCompleted);
            release.TrySetResult();
        }
        await create.WaitAsync(TimeSpan.FromSeconds(2));
        refresh ??= Refresh();
        await refresh.WaitAsync(TimeSpan.FromSeconds(2));
        await harness.DispatchAsync(() => { }, CancellationToken.None);
        CollectionAssert.AreEqual(realtimeFirst
            ? new[] { "realtime", "admin", "admin" } : new[] { "admin", "admin", "realtime" }, order.ToArray());
    }
    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task OpenedNonCooperativeReadRetiresOnDrainBeforeAuthorityNotificationAndCannotRevive(bool hostDriven)
    {
        var (manager, filter, root) = await PluginRequestDispatcherTests.OpenRetirementSessionAsync();
        var raw = new TaskCompletionSource<DatabaseOpenResult>(TaskCreationOptions.RunContinuationsAsynchronously);
        var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        try
        {
            using var authority = new ProductAuthorityEpoch();
            using var opens = new PluginProjectContextBindingRegistry(authority);
            PluginProjectContext context = PluginProjectContext.FromSession(manager.Current)!;
            authority.Transition(context);
            opens.SetAfterAuthorityTransition(context);
            var tables = new FakeTableRpcGateway
            {
                OpenDatabaseWithTokenOverride = (_, _) => { entered.TrySetResult(); return raw.Task; },
            };
            var workspace = new TableWorkspaceService(tables);
            var sink = new FakeWebReplySink();
            var renderer = new WorkspaceTableRequestController(workspace, new FakeDatabasePicker("db"), sink,
                () => null, pluginBindings: opens, sessionEnvelopeFilter: filter);
            await using var runtime = new ProductionWorkspaceRuntimeFactory(
                () => throw new InvalidOperationException("unused runtime"), () => new BackendLaunchOptions { Command = "unused" });
            using var host = new ProductWorkspaceController(sink, runtime, manager, new FakeDatabasePicker("db"), workspace,
                () => true, () => false, () => true, _ => { }, "test", guards: () => true,
                pluginContext: () => context, workspaceExpected: () => true, authority: authority,
                databaseOpens: opens, sessionEnvelopeFilter: filter);
            Task pending = hostDriven ? host.SuperviseOpenAsync() : renderer.DispatchAsync(new RoutedWebRequest(
                "database.openRequested", null, JsonSerializer.SerializeToElement(new { openId = "old-open" }), ""));
            await entered.Task.WaitAsync(TimeSpan.FromSeconds(2));
            try
            {
                Guid id = manager.Current.WorkspaceId!.Value;
                ulong epoch = manager.Current.SessionEpoch;
                await filter.DrainAsync(id, epoch, CancellationToken.None).WaitAsync(TimeSpan.FromSeconds(2));
                filter.Resume(id, epoch);
                await pending.WaitAsync(TimeSpan.FromSeconds(2));
                Assert.IsFalse(raw.Task.IsCompleted, "local retirement must not wait for the non-cooperative transport");
                tables.OpenDatabaseWithTokenOverride = (_, _) => Task.FromResult(
                    new DatabaseOpenResult(["fresh"], [], TestDisplayNames.For("fresh")));
                if (hostDriven) await host.SuperviseOpenAsync().WaitAsync(TimeSpan.FromSeconds(2));
                else await renderer.DispatchAsync(new RoutedWebRequest("database.openRequested", null,
                    JsonSerializer.SerializeToElement(new { openId = "fresh-open" }), "")).WaitAsync(TimeSpan.FromSeconds(2));
                raw.TrySetResult(new DatabaseOpenResult(["stale"], [], TestDisplayNames.For("stale")));
                var opened = sink.Replies.Where(reply => reply.Type == "database.opened").ToArray();
                Assert.HasCount(1, opened);
                Assert.AreEqual("fresh", JsonSerializer.SerializeToElement(opened[0].Payload).GetProperty("tables")[0].GetString());
            }
            finally
            {
                raw.TrySetResult(new DatabaseOpenResult(["stale"], [], TestDisplayNames.For("stale")));
                await pending.WaitAsync(TimeSpan.FromSeconds(2));
            }
        }
        finally
        {
            await manager.DisposeAsync();
            filter.Dispose();
            Directory.Delete(root, recursive: true);
        }
    }
    [TestMethod]
    public void ControlReplacementAndRecoverySealRetireTheRendererGeneration()
    {
        using var harness = new StaBridge();
        long before = harness.Bridge.RendererGeneration;
        Assert.AreEqual(0, before);

        WebView2 replacement = harness.CreateReplacementControl();
        harness.Bridge.AttachControl(replacement);
        Assert.AreEqual(1, harness.Bridge.RendererGeneration);

        harness.Bridge.SealForRendererRecovery();
        Assert.AreEqual(2, harness.Bridge.RendererGeneration);
    }

    [TestMethod]
    public async Task NavigatedRecoveryTimeoutRequiresFullRetryToReloadAndAwaitBusinessReadiness()
    {
        using var harness = new StaBridge();
        var cache = typeof(ProductWebViewBridge).GetField("_loadTask",
            System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!;
        cache.SetValue(harness.Bridge, Task.CompletedTask);
        var backend = new FakeBackendLifecycle();
        var business = new TaskCompletionSource();
        var freshNavigation = new TaskCompletionSource();
        int recoveries = 0;
        MainWindowViewModel vm = null!;
        vm = new MainWindowViewModel(backend, harness.Bridge, recoverRenderer: async reason =>
        {
            recoveries++;
            Assert.IsTrue(vm.TryBeginRendererRecovery(rendererFailure: reason.StartsWith("renderer:")));
            harness.Bridge.SealForRendererRecovery();
            harness.Bridge.AttachControl(harness.CreateReplacementControl());
            // Substitute only navigation completion at the existing bridge cache seam.
            cache.SetValue(harness.Bridge, recoveries == 1 ? Task.CompletedTask : freshNavigation.Task);
            await harness.Bridge.LoadAsync(CancellationToken.None);
            await business.Task;
            vm.CompleteRendererRecovery();
        });
        await vm.StartAsync(); Assert.AreEqual(StartupState.Ready, vm.State);
        vm.RecoverRendererCommand.Execute(null);
        Assert.AreEqual(StartupState.LoadingWeb, vm.State);
        Assert.IsTrue(((Task)cache.GetValue(harness.Bridge)!).IsCompletedSuccessfully);
        business.SetException(new TimeoutException("business app.ready was not received"));
        Assert.AreEqual(StartupState.Faulted, vm.State);
        Assert.IsTrue(vm.IsHostFallbackVisible);
        business = new TaskCompletionSource();
        long retiredGeneration = harness.Bridge.RendererGeneration;
        await vm.StartAsync();
        Assert.AreEqual(0, backend.StopCalls);
        Assert.AreEqual(2, recoveries, "full retry must re-enter the real renderer reset/reload boundary");
        Assert.IsTrue(harness.Bridge.RendererGeneration > retiredGeneration);
        Assert.AreEqual(StartupState.LoadingWeb, vm.State);
        freshNavigation.SetResult();
        Assert.AreEqual(StartupState.LoadingWeb, vm.State, "navigation success is not business app.ready");
        Assert.IsTrue(vm.IsHostFallbackVisible);
        business.SetResult();
        Assert.AreEqual(StartupState.Ready, vm.State);
        Assert.IsFalse(vm.IsRendererRecovering);
    }

    [TestMethod]
    public void QueuedNotificationFromARetiredGenerationIsDropped()
    {
        using var harness = new StaBridge();
        // Queued while generation 0 is current (no core attached yet — the
        // commit result must still be observable).
        harness.Bridge.PostNotification("host.startupStateChanged", new { phase = "starting" });
        harness.Bridge.SealForRendererRecovery();

        // Pump AFTER the seal: the queued emission belongs to the retired
        // generation and must not reach the new renderer.
        harness.PumpQueued();
        CollectionAssert.AreEqual(new bool[] { false }, harness.Commits.ToArray(),
            "a retired generation's queued notification must be dropped, not posted");
    }
}
