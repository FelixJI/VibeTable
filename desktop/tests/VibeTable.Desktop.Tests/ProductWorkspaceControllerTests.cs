using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductWorkspaceControllerTests
{
    private sealed class Fixture : IDisposable
    {
        public Fixture(
            IWebReplySink? reply = null,
            Func<TimeSpan, Task>? delay = null,
            Func<bool>? workspaceExpected = null)
        {
            Root = Path.Combine(
                Path.GetTempPath(),
                "vibetable-product-workspace-" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(Root);
            Gateway = new FakeTableRpcGateway();
            Reply = new FakeWebReplySink();
            ReplySink = reply ?? Reply;
            RuntimeFactory = new ProductionWorkspaceRuntimeFactory(
                () => throw new InvalidOperationException(
                    "sidecar template must not be resolved by these tests"),
                () => new BackendLaunchOptions
                {
                    Command = "backend.exe",
                });
            Sessions = new WorkspaceSessionManager(
                new WorkspaceRegistry(Root),
                RuntimeFactory);
            Traces = new List<string>();
            Authority = new ProductAuthorityEpoch();
            CurrentContext = new PluginProjectContext(
                "local:test-workspace", "test-workspace:1", 1);
            Authority.Transition(CurrentContext);
            DatabaseOpens = new PluginProjectContextBindingRegistry(Authority);
            DatabaseOpens.SetAfterAuthorityTransition(CurrentContext);
            Workspace = new TableWorkspaceService(Gateway);
            Coordinator = new GridStateCoordinator(Gateway, _ => { });
            Controller = new ProductWorkspaceController(
                ReplySink,
                RuntimeFactory,
                Sessions,
                new FixedDatabasePicker("local://workspace/test"),
                Workspace,
                Coordinator,
                () => true,
                () => false,
                () => true,
                message => Traces.Add(message),
                "test-host",
                retryDelay: _ => TimeSpan.Zero,
                delay: delay,
                guards: () => GuardsResult(),
                pluginContext: () => CurrentContext,
                workspaceExpected: workspaceExpected,
                authority: Authority,
                databaseOpens: DatabaseOpens);
        }

        public string Root { get; }
        public FakeTableRpcGateway Gateway { get; }
        public FakeWebReplySink Reply { get; }
        public IWebReplySink ReplySink { get; }
        public ProductionWorkspaceRuntimeFactory RuntimeFactory { get; }
        public WorkspaceSessionManager Sessions { get; }
        public List<string> Traces { get; }
        public ProductAuthorityEpoch Authority { get; }
        public PluginProjectContextBindingRegistry DatabaseOpens { get; }
        public PluginProjectContext CurrentContext { get; set; }
        public TableWorkspaceService Workspace { get; }
        public GridStateCoordinator Coordinator { get; }
        public Func<bool> GuardsResult { get; set; } = () => true;
        public ProductWorkspaceController Controller { get; }

        public void Transition(PluginProjectContext context)
        {
            CurrentContext = context;
            Authority.Transition(context);
            DatabaseOpens.SetAfterAuthorityTransition(context);
        }

        public void Dispose()
        {
            Controller.Dispose();
            DatabaseOpens.Dispose();
            Authority.Dispose();
            Sessions.DisposeAsync().AsTask().GetAwaiter().GetResult();
            RuntimeFactory.DisposeAsync().AsTask().GetAwaiter().GetResult();
            try
            {
                Directory.Delete(Root, recursive: true);
            }
            catch (IOException)
            {
            }
            catch (UnauthorizedAccessException)
            {
            }
        }
    }

    private sealed class FixedDatabasePicker(string source) : IDatabasePicker
    {
        public Task<string?> PickDatabaseAsync() => Task.FromResult<string?>(source);
    }

    private static DatabaseOpenResult OpenResult() => new(
        ["tbl_attachments"],
        Array.Empty<string>(),
        new Dictionary<string, string>
        {
            ["tbl_attachments"] = "Attachments",
        });

    [TestMethod]
    public async Task OpenAsyncRetriesTransientSidecarRecycleFailures()
    {
        using var fixture = new Fixture();
        int attempts = 0;
        fixture.Gateway.OpenDatabaseOverride = _ =>
        {
            attempts++;
            return attempts < 3
                ? Task.FromException<DatabaseOpenResult>(
                    new InvalidOperationException("sidecar recycling"))
                : Task.FromResult(OpenResult());
        };

        await fixture.Controller.SuperviseOpenAsync();

        Assert.AreEqual(3, attempts);
        Assert.AreEqual(3, fixture.Gateway.OpenDatabaseCalls.Count);
        var opened = fixture.Reply.Replies.Single(
            reply => reply.Type == "database.opened");
        Assert.IsNotNull(opened.Payload);
        Assert.IsFalse(fixture.Reply.Replies.Any(
            reply => reply.Type == "operation.failed"));
    }

    [TestMethod]
    public async Task SupervisedOpenWaitsForGuardsDuringSessionRecycle()
    {
        using var fixture = new Fixture();
        fixture.Gateway.DatabaseOpenResults["local://workspace/test"] = OpenResult();
        int guardChecks = 0;
        fixture.GuardsResult = () =>
        {
            guardChecks++;
            // The recycled session generation is not projectable for the
            // first polls: the backend is still rebinding.
            return guardChecks > 2;
        };

        await fixture.Controller.SuperviseOpenAsync();

        Assert.IsTrue(guardChecks >= 3);
        Assert.AreEqual(1, fixture.Gateway.OpenDatabaseCalls.Count);
        Assert.IsNotNull(fixture.Reply.Replies.Single(
            reply => reply.Type == "database.opened"));
    }

    [TestMethod]
    public async Task OpenAsyncPostsOperationFailedAfterRetryBudget()
    {
        using var fixture = new Fixture();
        fixture.Gateway.OpenDatabaseOverride = _ =>
            Task.FromException<DatabaseOpenResult>(
                new InvalidOperationException("sidecar unavailable"));

        await fixture.Controller.SuperviseOpenAsync();

        Assert.AreEqual(
            12,
            fixture.Gateway.OpenDatabaseCalls.Count,
            "the retry budget must stay bounded so genuine failures surface");
        var failed = fixture.Reply.Replies.Single(
            reply => reply.Type == "operation.failed");
        Assert.IsNotNull(failed.Payload);
    }

    [TestMethod]
    public async Task LatestGenerationOpenPublishesOnlyReplacementContextAfterOldCompletion()
    {
        using var fixture = new Fixture();
        var firstStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var first = new TaskCompletionSource<DatabaseOpenResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int calls = 0;
        fixture.Gateway.OpenDatabaseWithTokenOverride = (_, _) =>
        {
            if (Interlocked.Increment(ref calls) == 1)
            {
                firstStarted.TrySetResult();
                return first.Task;
            }
            return Task.FromResult(new DatabaseOpenResult(
                ["current_records"],
                [],
                TestDisplayNames.For("current_records")));
        };

        Task openingA = fixture.Controller.SuperviseOpenAsync();
        await firstStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        fixture.Transition(new PluginProjectContext(
            "local:replacement-workspace", "replacement:2", 2));
        Task openingB = fixture.Controller.SuperviseOpenAsync();
        first.SetResult(new DatabaseOpenResult(
            ["stale_records"],
            [],
            TestDisplayNames.For("stale_records")));

        await Task.WhenAll(openingA, openingB);

        FakeWebReplySink.Reply opened = fixture.Reply.Replies.Single(
            reply => reply.Type == "database.opened");
        JsonElement payload = JsonSerializer.SerializeToElement(opened.Payload);
        Assert.AreEqual(
            "local:replacement-workspace",
            payload.GetProperty("projectKey").GetString());
        Assert.AreEqual("local://workspace/test", fixture.Workspace.CurrentDatabase);
        Assert.AreEqual(2, calls);
    }

    [TestMethod]
    public async Task SameAuthorityRequestDuringPendingOpenCoalescesIntoOneTerminal()
    {
        using var fixture = new Fixture();
        var started = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var pending = new TaskCompletionSource<DatabaseOpenResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Gateway.OpenDatabaseWithTokenOverride = (_, _) =>
        {
            started.TrySetResult();
            return pending.Task;
        };

        Task first = fixture.Controller.SuperviseOpenAsync();
        await started.Task.WaitAsync(TimeSpan.FromSeconds(2));
        Task second = fixture.Controller.SuperviseOpenAsync();
        pending.SetResult(OpenResult());

        await Task.WhenAll(first, second);

        Assert.AreEqual(1, fixture.Gateway.OpenDatabaseCalls.Count);
        Assert.AreEqual(1, fixture.Reply.Replies.Count(
            reply => reply.Type == "database.opened"));
    }

    [TestMethod]
    public async Task SupersededGuardWaitSkipsOldRpcAndOpensOnlyReplacement()
    {
        using var fixture = new Fixture();
        var guardEntered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        using var releaseGuard = new ManualResetEventSlim();
        fixture.GuardsResult = () =>
        {
            guardEntered.TrySetResult();
            releaseGuard.Wait();
            return true;
        };
        fixture.Gateway.DatabaseOpenResults["local://workspace/test"] = OpenResult();

        Task first = fixture.Controller.SuperviseOpenAsync();
        await guardEntered.Task.WaitAsync(TimeSpan.FromSeconds(2));
        fixture.Transition(new PluginProjectContext(
            "local:replacement-workspace", "replacement:2", 2));
        Task second = fixture.Controller.SuperviseOpenAsync();
        releaseGuard.Set();

        await Task.WhenAll(first, second);

        Assert.AreEqual(1, fixture.Gateway.OpenDatabaseCalls.Count);
        FakeWebReplySink.Reply opened = fixture.Reply.Replies.Single(
            reply => reply.Type == "database.opened");
        JsonElement payload = JsonSerializer.SerializeToElement(opened.Payload);
        Assert.AreEqual(
            "local:replacement-workspace",
            payload.GetProperty("projectKey").GetString());
    }

    [TestMethod]
    public async Task RendererOpenSupersedesPendingHostOpenAcrossSharedCoordinator()
    {
        using var fixture = new Fixture();
        var hostStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var pendingHost = new TaskCompletionSource<DatabaseOpenResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Gateway.OpenDatabaseWithTokenOverride = (path, _) =>
        {
            if (path == "local://workspace/test")
            {
                hostStarted.TrySetResult();
                return pendingHost.Task;
            }
            return Task.FromResult(new DatabaseOpenResult(
                ["renderer_records"], [], TestDisplayNames.For("renderer_records")));
        };
        using var renderer = new WorkspaceRequestDispatcher(
            fixture.Workspace,
            new FakeDatabasePicker("local://renderer"),
            fixture.Reply,
            fixture.Coordinator,
            authority: fixture.Authority,
            databaseOpens: fixture.DatabaseOpens);

        Task host = fixture.Controller.SuperviseOpenAsync();
        await hostStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        await renderer.DispatchAsyncForTesting(OpenRequest("renderer-latest"));
        pendingHost.SetResult(OpenResult());
        await host;

        FakeWebReplySink.Reply opened = fixture.Reply.Replies.Single(
            reply => reply.Type == "database.opened");
        JsonElement payload = JsonSerializer.SerializeToElement(opened.Payload);
        Assert.AreEqual("renderer-latest", payload.GetProperty("openId").GetString());
        Assert.AreEqual("local://renderer", fixture.Workspace.CurrentDatabase);
    }

    [TestMethod]
    public async Task HostOpenSupersedesPendingRendererOpenAcrossSharedCoordinator()
    {
        using var fixture = new Fixture();
        var rendererStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var pendingRenderer = new TaskCompletionSource<DatabaseOpenResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Gateway.OpenDatabaseWithTokenOverride = (path, _) =>
        {
            if (path == "local://renderer")
            {
                rendererStarted.TrySetResult();
                return pendingRenderer.Task;
            }
            return Task.FromResult(OpenResult());
        };
        using var renderer = new WorkspaceRequestDispatcher(
            fixture.Workspace,
            new FakeDatabasePicker("local://renderer"),
            fixture.Reply,
            fixture.Coordinator,
            authority: fixture.Authority,
            databaseOpens: fixture.DatabaseOpens);

        Task rendererOpen = renderer.DispatchAsyncForTesting(
            OpenRequest("renderer-pending"));
        await rendererStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        await fixture.Controller.SuperviseOpenAsync();
        pendingRenderer.SetResult(new DatabaseOpenResult(
            ["renderer_records"], [], TestDisplayNames.For("renderer_records")));
        await rendererOpen;

        Assert.AreEqual(1, fixture.Reply.Replies.Count(
            reply => reply.Type == "database.opened"));
        FakeWebReplySink.Reply cancelled = fixture.Reply.Replies.Single(
            reply => reply.Type == "database.openCancelled");
        Assert.AreEqual(
            "renderer-pending",
            JsonSerializer.SerializeToElement(cancelled.Payload)
                .GetProperty("openId").GetString());
        Assert.AreEqual("local://workspace/test", fixture.Workspace.CurrentDatabase);
    }

    [TestMethod]
    public async Task RendererSupersessionDuringRetryGapPermanentlyRetiresHostRequest()
    {
        var retryEntered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseRetry = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        using var fixture = new Fixture(
            delay: _ =>
            {
                retryEntered.TrySetResult();
                return releaseRetry.Task;
            });
        int hostCalls = 0;
        fixture.Gateway.OpenDatabaseWithTokenOverride = (source, _) =>
        {
            if (source == "local://workspace/test")
            {
                hostCalls += 1;
                return Task.FromException<DatabaseOpenResult>(
                    new InvalidOperationException("transient host failure"));
            }
            return Task.FromResult(new DatabaseOpenResult(
                ["renderer_records"], [], TestDisplayNames.For("renderer_records")));
        };
        using var renderer = new WorkspaceRequestDispatcher(
            fixture.Workspace,
            new FakeDatabasePicker("local://renderer"),
            fixture.Reply,
            fixture.Coordinator,
            authority: fixture.Authority,
            databaseOpens: fixture.DatabaseOpens);

        Task host = fixture.Controller.SuperviseOpenAsync();
        await retryEntered.Task.WaitAsync(TimeSpan.FromSeconds(2));
        await renderer.DispatchAsyncForTesting(OpenRequest("renderer-during-retry"));
        releaseRetry.TrySetResult();
        await host;

        Assert.AreEqual(1, hostCalls, "a retired logical request must never mint a retry lease");
        FakeWebReplySink.Reply opened = fixture.Reply.Replies.Single(
            reply => reply.Type == "database.opened");
        Assert.AreEqual(
            "renderer-during-retry",
            JsonSerializer.SerializeToElement(opened.Payload)
                .GetProperty("openId").GetString());
    }

    [TestMethod]
    public async Task OpenWhenReadyIgnoresRetiredTerminalSinkFailureAndWorkerRemainsReusable()
    {
        var sink = new ThrowOnCancelledReplySink();
        using var fixture = new Fixture(sink, workspaceExpected: () => true);
        var rendererStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var pendingRenderer = new TaskCompletionSource<DatabaseOpenResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Gateway.OpenDatabaseWithTokenOverride = (source, _) =>
        {
            if (source == "local://renderer")
            {
                rendererStarted.TrySetResult();
                return pendingRenderer.Task;
            }
            return Task.FromResult(OpenResult());
        };
        using var renderer = new WorkspaceRequestDispatcher(
            fixture.Workspace,
            new FakeDatabasePicker("local://renderer"),
            sink,
            fixture.Coordinator,
            authority: fixture.Authority,
            databaseOpens: fixture.DatabaseOpens);

        Task rendererOpen = renderer.DispatchAsyncForTesting(
            OpenRequest("renderer-retired-by-host"));
        await rendererStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        fixture.Controller.OpenWhenReady();
        await sink.FirstOpened.Task.WaitAsync(TimeSpan.FromSeconds(2));
        pendingRenderer.TrySetResult(new DatabaseOpenResult(
            ["renderer_records"], [], TestDisplayNames.For("renderer_records")));
        await rendererOpen;

        await fixture.Controller.SuperviseOpenAsync();

        Assert.AreEqual(2, sink.OpenedCount);
        Assert.AreEqual("local://workspace/test", fixture.Workspace.CurrentDatabase);
        Assert.IsTrue(fixture.Traces.Any(trace => trace.Contains(
            "Database open cancellation terminal failed",
            StringComparison.Ordinal)));
    }

    [TestMethod]
    public async Task HostAndRendererReopenShareCanonicalGridDatabaseIdentity()
    {
        using var fixture = new Fixture();
        fixture.Gateway.DatabaseOpenResults["local://workspace/test"] = OpenResult();
        fixture.Gateway.SelectionProjectionResults["tbl_attachments"] =
            Projection("tbl_attachments");

        await fixture.Controller.SuperviseOpenAsync();
        await fixture.Coordinator.SwitchTableAsync("tbl_attachments");
        fixture.Coordinator.RequestSave(new GridState());
        await fixture.Coordinator.FlushAsync();

        using var renderer = new WorkspaceRequestDispatcher(
            fixture.Workspace,
            new FakeDatabasePicker("local://workspace/test"),
            fixture.Reply,
            fixture.Coordinator,
            authority: fixture.Authority,
            databaseOpens: fixture.DatabaseOpens);
        await renderer.DispatchAsyncForTesting(OpenRequest("renderer-reopen-same-source"));
        await fixture.Coordinator.SwitchTableAsync("tbl_attachments");
        fixture.Coordinator.RequestSave(new GridState());
        await fixture.Coordinator.FlushAsync();

        CollectionAssert.AreEqual(
            new[] { "local://workspace/test", "local://workspace/test" },
            fixture.Gateway.SavedGridStates.Select(state => state.DatabaseId).ToArray());
    }

    [TestMethod]
    public async Task HostDrivenOpenedSinkFailureLeavesNoAdmissionAndOneFailureTerminal()
    {
        var sink = new ThrowOnOpenedReplySink();
        using var fixture = new Fixture(sink);
        fixture.Gateway.DatabaseOpenResults["local://workspace/old"] = OpenResult();
        await fixture.Workspace.OpenDatabaseAsync("local://workspace/old");
        fixture.Gateway.DatabaseOpenResults["local://workspace/test"] = OpenResult();

        await fixture.Controller.SuperviseOpenAsync();

        Assert.AreEqual("local://workspace/old", fixture.Workspace.CurrentDatabase);
        Assert.AreEqual(1, sink.FailedCount);
    }

    [TestMethod]
    public async Task HostDrivenOpenedSinkObservesAdmittedDatabaseDuringPost()
    {
        Fixture? captured = null;
        var sink = new StateObservingReplySink(
            () => captured?.Workspace.CurrentDatabase);
        using var fixture = new Fixture(sink);
        captured = fixture;
        fixture.Gateway.DatabaseOpenResults["local://workspace/test"] = OpenResult();

        await fixture.Controller.SuperviseOpenAsync();

        Assert.AreEqual("local://workspace/test", sink.DatabaseDuringOpened);
        Assert.AreEqual(0, sink.FailedCount);
    }

    private sealed class ThrowOnOpenedReplySink : IWebReplySink
    {
        public int FailedCount { get; private set; }

        public void PostNotification(string type, object? payload)
        {
            if (type == "database.opened")
                throw new InvalidOperationException("synthetic opened sink failure");
        }

        public void PostResponse(string type, string? requestId, object? payload)
        {
        }

        public void PostOperationFailed(
            string? requestId,
            string message,
            string? code = null,
            string? operation = null,
            string? operationId = null)
        {
            FailedCount += 1;
        }
    }

    private sealed class ThrowOnCancelledReplySink : IWebReplySink
    {
        public bool ThrowOnCancelled { get; set; } = true;
        public int OpenedCount { get; private set; }
        public TaskCompletionSource FirstOpened { get; } = new(
            TaskCreationOptions.RunContinuationsAsynchronously);

        public void PostNotification(string type, object? payload)
        {
            if (type == "database.openCancelled" && ThrowOnCancelled)
                throw new InvalidOperationException("synthetic cancellation sink failure");
            if (type == "database.opened")
            {
                OpenedCount += 1;
                FirstOpened.TrySetResult();
            }
        }

        public void PostResponse(string type, string? requestId, object? payload)
        {
        }

        public void PostOperationFailed(
            string? requestId,
            string message,
            string? code = null,
            string? operation = null,
            string? operationId = null)
        {
        }
    }

    private static TableSelectionProjection Projection(string table)
        => new(
            new TablePage(
                table,
                [],
                [],
                0,
                TableWorkspaceLimits.MaxPageLimit,
                0,
                "remote"),
            new EditSchemaResult(
                table,
                "schema_0001",
                "fake-row-key",
                RowKeyStable: true,
                Editable: false,
                []));

    private static RoutedWebRequest OpenRequest(string openId)
    {
        using JsonDocument payload = JsonDocument.Parse(
            $$"""{"openId":"{{openId}}"}""");
        return new RoutedWebRequest(
            "database.openRequested",
            openId,
            payload.RootElement.Clone(),
            string.Empty);
    }

    private sealed class StateObservingReplySink(Func<string?> currentDatabase)
        : IWebReplySink
    {
        public string? DatabaseDuringOpened { get; private set; }
        public int FailedCount { get; private set; }

        public void PostNotification(string type, object? payload)
        {
            if (type == "database.opened") DatabaseDuringOpened = currentDatabase();
        }

        public void PostResponse(string type, string? requestId, object? payload)
        {
        }

        public void PostOperationFailed(
            string? requestId,
            string message,
            string? code = null,
            string? operation = null,
            string? operationId = null)
        {
            FailedCount += 1;
        }
    }
}
