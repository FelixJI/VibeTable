using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceRequestDispatcherQueryTests
{
    [TestMethod]
    public async Task CancelledDatabasePickerDoesNotForgeDatabaseOpened()
    {
        var sink = new FakeWebReplySink();
        using var bindings = ReadyBindings(
            new PluginProjectContext("local:workspace", "r1", 1));
        var gateway = new FakeTableRpcGateway();
        var controller = new WorkspaceTableRequestController(
            new TableWorkspaceService(gateway),
            new FakeDatabasePicker(null),
            sink,
            () => null,
            new GridStateCoordinator(gateway, _ => { }),
            pluginBindings: bindings);

        await controller.DispatchAsync(Request("database.openRequested", "open-cancel"));

        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "database.opened"));
        Assert.AreEqual(1, sink.Replies.Count(reply => reply.Type == "database.openCancelled"));
        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "operation.failed"));
    }

    [TestMethod]
    public async Task DatabaseOpenedProducerCarriesAuthoritativePluginContext()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["local://workspace"] = new DatabaseOpenResult(
            ["records"], [], TestDisplayNames.For("records"));
        var sink = new FakeWebReplySink();
        using var bindings = ReadyBindings(new PluginProjectContext(
            "local:authoritative-workspace", "authoritative:7", 7));
        var controller = new WorkspaceTableRequestController(
            new TableWorkspaceService(gateway),
            new FakeDatabasePicker("local://workspace"),
            sink,
            () => null,
            new GridStateCoordinator(gateway, _ => { }),
            pluginBindings: bindings);

        await controller.DispatchAsync(Request("database.openRequested", "open-ready"));

        FakeWebReplySink.Reply opened = sink.Replies.Single(
            reply => reply.Type == "database.opened");
        JsonElement payload = JsonSerializer.SerializeToElement(opened.Payload);
        Assert.AreEqual(
            "local:authoritative-workspace",
            payload.GetProperty("projectKey").GetString());
        Assert.AreEqual("authoritative:7", payload.GetProperty("projectRevision").GetString());
    }

    [TestMethod]
    public async Task OpenedSinkReentrantTableRequestObservesAdmittedDatabase()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["local://workspace"] = new DatabaseOpenResult(
            ["records"], [], TestDisplayNames.For("records"));
        gateway.SelectionProjectionResults["records"] = Projection("records");
        var workspace = new TableWorkspaceService(gateway);
        var grid = new GridStateCoordinator(gateway, _ => { });
        var sink = new ReentrantTableRequestSink(workspace);
        using var bindings = ReadyBindings(new PluginProjectContext(
            "local:workspace", "workspace:9", 9));
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://workspace"),
            sink,
            () => null,
            grid,
            pluginBindings: bindings);

        await controller.DispatchAsync(Request(
            "database.openRequested", "open-reentrant-table"));
        bool selected = await (sink.Selection
            ?? throw new AssertFailedException("opened sink did not issue table request"));

        Assert.IsTrue(selected);
        Assert.AreEqual("local://workspace", sink.DatabaseObservedDuringPost);
        Assert.AreEqual(0, sink.FailedCount);
        await grid.SwitchTableAsync("records");
        grid.RequestSave(new GridState());
        await grid.FlushAsync();
        Assert.AreEqual("local://workspace", gateway.SavedGridStates.Single().DatabaseId);
    }

    [TestMethod]
    [DataRow("unavailable")]
    [DataRow("switch")]
    [DataRow("session-token")]
    public async Task PendingDatabaseOpenCannotPublishIntoAChangedContext(string transition)
    {
        var started = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var pending = new TaskCompletionSource<DatabaseOpenResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var gateway = new FakeTableRpcGateway
        {
            OpenDatabaseOverride = _ =>
            {
                started.TrySetResult();
                return pending.Task;
            },
        };
        var workspace = new TableWorkspaceService(gateway);
        var sink = new FakeWebReplySink();
        using var session = new CancellationTokenSource();
        using var dispatcher = new WorkspaceRequestDispatcher(
            workspace,
            new FakeDatabasePicker("local://old-workspace"),
            sink,
            new GridStateCoordinator(gateway, _ => { }),
            pluginContext: () => new PluginProjectContext(
                "local:old-workspace", "old:7", 7));
        dispatcher.SetPluginProjectContext(
            new PluginProjectContext("local:old-workspace", "old:7", 7),
            session.Token);

        Task opening = dispatcher.DispatchAsyncForTesting(Request(
            "database.openRequested", $"open-{transition}"));
        await started.Task.WaitAsync(TimeSpan.FromSeconds(2));
        if (transition == "session-token")
        {
            session.Cancel();
        }
        else
        {
            dispatcher.SetPluginProjectContext(transition == "switch"
                ? new PluginProjectContext("local:new-workspace", "new:8", 8)
                : null);
        }
        pending.SetResult(new DatabaseOpenResult(
            ["old_records"], [], TestDisplayNames.For("old_records")));
        await opening;

        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "database.opened"));
        Assert.AreEqual(1, sink.Replies.Count(
            reply => reply.Type == "database.openCancelled"));
        Assert.IsNull(workspace.CurrentDatabase);
    }

    [TestMethod]
    public async Task RetiredDatabaseOpenTokenStaysRegistrableUntilOperationReleases()
    {
        var started = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var pending = new TaskCompletionSource<DatabaseOpenResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        CancellationToken capturedToken = default;
        var gateway = new FakeTableRpcGateway
        {
            OpenDatabaseWithTokenOverride = (_, token) =>
            {
                capturedToken = token;
                started.TrySetResult();
                return pending.Task;
            },
        };
        var sink = new FakeWebReplySink();
        using var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(gateway),
            new FakeDatabasePicker("local://old-workspace"),
            sink,
            new GridStateCoordinator(gateway, _ => { }),
            pluginContext: () => new PluginProjectContext(
                "local:old-workspace", "old:7", 7));

        Task opening = dispatcher.DispatchAsyncForTesting(Request(
            "database.openRequested", "open-register"));
        await started.Task.WaitAsync(TimeSpan.FromSeconds(2));
        dispatcher.SetPluginProjectContext(null);
        bool callbackObserved = false;
        using CancellationTokenRegistration registration = capturedToken.Register(
            () => callbackObserved = true);
        pending.SetResult(new DatabaseOpenResult(
            ["old_records"], [], TestDisplayNames.For("old_records")));
        await opening;

        Assert.IsTrue(callbackObserved);
        Assert.AreEqual(1, sink.Replies.Count(
            reply => reply.Type == "database.openCancelled"));
        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "database.opened"));
    }

    [TestMethod]
    public async Task NewerDatabaseOpenOwnsTheOnlyAdmissibleTerminal()
    {
        var firstStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var secondStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var first = new TaskCompletionSource<DatabaseOpenResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var second = new TaskCompletionSource<DatabaseOpenResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int call = 0;
        var gateway = new FakeTableRpcGateway
        {
            OpenDatabaseWithTokenOverride = (_, _) =>
            {
                if (Interlocked.Increment(ref call) == 1)
                {
                    firstStarted.TrySetResult();
                    return first.Task;
                }
                secondStarted.TrySetResult();
                return second.Task;
            },
        };
        var workspace = new TableWorkspaceService(gateway);
        var sink = new FakeWebReplySink();
        using var dispatcher = new WorkspaceRequestDispatcher(
            workspace,
            new FakeDatabasePicker("local://workspace"),
            sink,
            new GridStateCoordinator(gateway, _ => { }),
            pluginContext: () => new PluginProjectContext(
                "local:workspace", "workspace:9", 9));

        Task openingA = dispatcher.DispatchAsyncForTesting(Request(
            "database.openRequested", "open-a"));
        await firstStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        Task openingB = dispatcher.DispatchAsyncForTesting(Request(
            "database.openRequested", "open-b"));
        await secondStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));

        first.SetResult(new DatabaseOpenResult(
            ["stale_records"], [], TestDisplayNames.For("stale_records")));
        await openingA;

        Assert.IsNull(workspace.CurrentDatabase);
        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "database.opened"));
        FakeWebReplySink.Reply retired = sink.Replies.Single(
            reply => reply.Type == "database.openCancelled");
        Assert.AreEqual("open-a", Payload(retired).GetProperty("openId").GetString());

        second.SetResult(new DatabaseOpenResult(
            ["current_records"], [], TestDisplayNames.For("current_records")));
        await openingB;

        FakeWebReplySink.Reply opened = sink.Replies.Single(
            reply => reply.Type == "database.opened");
        Assert.AreEqual("open-b", Payload(opened).GetProperty("openId").GetString());
        Assert.AreEqual("local://workspace", workspace.CurrentDatabase);
    }

    [TestMethod]
    public async Task RetiredTerminalSinkFailureDoesNotCancelReplacementRendererOpen()
    {
        var firstStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var first = new TaskCompletionSource<DatabaseOpenResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int calls = 0;
        var gateway = new FakeTableRpcGateway
        {
            OpenDatabaseWithTokenOverride = (_, _) =>
            {
                if (Interlocked.Increment(ref calls) == 1)
                {
                    firstStarted.TrySetResult();
                    return first.Task;
                }
                return Task.FromResult(new DatabaseOpenResult(
                    ["records"], [], TestDisplayNames.For("records")));
            },
        };
        var sink = new ThrowOnCancelledReplySink();
        var workspace = new TableWorkspaceService(gateway);
        using var bindings = ReadyBindings(new PluginProjectContext(
            "local:workspace", "workspace:9", 9));
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://workspace"),
            sink,
            () => null,
            new GridStateCoordinator(gateway, _ => { }),
            pluginBindings: bindings);

        Task original = controller.DispatchAsync(Request(
            "database.openRequested", "renderer-old"));
        await firstStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        await controller.DispatchAsync(Request(
            "database.openRequested", "renderer-cancel-sink-throws"));
        first.TrySetResult(new DatabaseOpenResult(
            ["stale_records"], [], TestDisplayNames.For("stale_records")));
        await original;

        sink.ThrowOnCancelled = false;
        await controller.DispatchAsync(Request(
            "database.openRequested", "renderer-after-sink-failure"));

        Assert.AreEqual(2, sink.OpenedCount);
        Assert.AreEqual(0, sink.FailedCount);
        Assert.AreEqual("local://workspace", workspace.CurrentDatabase);
    }

    [TestMethod]
    public void AuthorityTransitionCompletesPluginTransferBeforeBestEffortTerminal()
    {
        using var authority = new ProductAuthorityEpoch();
        var order = new List<string>();
        int pluginCleanupCount = 0;
        using var transition = new ProductAuthorityTransitionCoordinator(
            authority,
            (_, _) =>
            {
                order.Add("database-retired");
                return ["renderer-open"];
            },
            _ =>
            {
                order.Add("plugin-transferred");
                pluginCleanupCount += 1;
            },
            _ =>
            {
                order.Add("renderer-terminal");
                throw new InvalidOperationException("synthetic sink failure");
            });

        transition.Transition(null);

        CollectionAssert.AreEqual(
            new[] { "database-retired", "plugin-transferred", "renderer-terminal" },
            order);
        Assert.AreEqual(1, pluginCleanupCount);
    }

    [TestMethod]
    public async Task MainWindowAuthorityTransitionInvalidatesOpenBeforePluginAuthorityMoves()
    {
        var started = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var pending = new TaskCompletionSource<DatabaseOpenResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var pluginTransitionEntered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        using var releasePluginTransition = new ManualResetEventSlim();
        var gateway = new FakeTableRpcGateway
        {
            OpenDatabaseOverride = _ =>
            {
                started.TrySetResult();
                return pending.Task;
            },
        };
        var workspace = new TableWorkspaceService(gateway);
        var sink = new FakeWebReplySink();
        using var authority = new ProductAuthorityEpoch();
        using var dispatcher = new WorkspaceRequestDispatcher(
            workspace,
            new FakeDatabasePicker("local://old-workspace"),
            sink,
            new GridStateCoordinator(gateway, _ => { }),
            pluginContext: () => new PluginProjectContext(
                "local:old-workspace", "old:7", 7),
            authority: authority);
        authority.Transition(new PluginProjectContext(
            "local:old-workspace", "old:7", 7));
        var transition = new ProductAuthorityTransitionCoordinator(
            authority,
            dispatcher.RetireDatabaseOpensAfterAuthorityTransition,
            _ =>
            {
                pluginTransitionEntered.TrySetResult();
                releasePluginTransition.Wait();
            },
            dispatcher.PostRetiredDatabaseOpenCancellations);

        Task opening = dispatcher.DispatchAsyncForTesting(Request(
            "database.openRequested", "open-during-transition"));
        await started.Task.WaitAsync(TimeSpan.FromSeconds(2));
        Task movingAuthority = Task.Run(() => transition.Transition(null));
        try
        {
            await pluginTransitionEntered.Task.WaitAsync(TimeSpan.FromSeconds(2));
            pending.SetResult(new DatabaseOpenResult(
                ["stale_records"], [], TestDisplayNames.For("stale_records")));
            await opening;

            Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "database.opened"));
            Assert.IsFalse(sink.Replies.Any(
                reply => reply.Type == "database.openCancelled"),
                "renderer terminals are posted only after both authorities transfer");
            Assert.IsNull(workspace.CurrentDatabase);
        }
        finally
        {
            releasePluginTransition.Set();
        }
        await movingAuthority;
        Assert.AreEqual(1, sink.Replies.Count(
            reply => reply.Type == "database.openCancelled"));
    }

    [TestMethod]
    public async Task DatabaseOpenFailurePublishesOneStableTerminalFailure()
    {
        var gateway = new FakeTableRpcGateway
        {
            OpenDatabaseOverride = _ => Task.FromException<DatabaseOpenResult>(
                new InvalidOperationException("backend details must stay private")),
        };
        var sink = new FakeWebReplySink();
        using var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(gateway),
            new FakeDatabasePicker("local://workspace"),
            sink,
            new GridStateCoordinator(gateway, _ => { }),
            pluginContext: () => new PluginProjectContext(
                "local:workspace", "workspace:9", 9));

        await dispatcher.DispatchAsyncForTesting(Request(
            "database.openRequested", "open-failure"));
        FakeWebReplySink.Reply? failure = sink.Replies.SingleOrDefault(
            reply => reply.Type == "operation.failed");

        Assert.IsNotNull(failure);
        Assert.AreEqual(1, sink.Replies.Count(reply => reply.Type == "operation.failed"));
        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "database.opened"));
        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "database.openCancelled"));
        JsonElement payload = JsonSerializer.SerializeToElement(failure.Payload);
        Assert.AreEqual("WORKSPACE_ERROR", payload.GetProperty("code").GetString());
        Assert.AreEqual(
            "database.openRequested",
            payload.GetProperty("operation").GetString());
        Assert.AreEqual(
            "Workspace operation failed.",
            payload.GetProperty("message").GetString());
        Assert.AreEqual("open-failure", payload.GetProperty("operationId").GetString());
    }

    [TestMethod]
    public async Task ThrowingOpenedSinkRollsBackAdmissionAndPublishesOneFailureTerminal()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["local://workspace"] = new DatabaseOpenResult(
            ["records"], [], TestDisplayNames.For("records"));
        gateway.DatabaseOpenResults["local://old"] = new DatabaseOpenResult(
            ["old_records"], [], TestDisplayNames.For("old_records"));
        gateway.SelectionProjectionResults["old_records"] = Projection("old_records");
        var workspace = new TableWorkspaceService(gateway);
        await workspace.OpenDatabaseAsync("local://old");
        var grid = new GridStateCoordinator(gateway, _ => { });
        grid.SetDatabase("grid-old");
        var sink = new ThrowOnOpenedReplySink();
        using var bindings = ReadyBindings(new PluginProjectContext(
            "local:workspace", "workspace:9", 9));
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://workspace"),
            sink,
            () => null,
            grid,
            pluginBindings: bindings);

        await controller.DispatchAsync(Request(
            "database.openRequested", "open-throwing-sink"));

        Assert.AreEqual("local://old", workspace.CurrentDatabase);
        Assert.IsTrue(await workspace.SelectTableAsync("old_records"));
        await grid.SwitchTableAsync("old_records");
        grid.RequestSave(new GridState());
        await grid.FlushAsync();
        Assert.AreEqual("grid-old", gateway.SavedGridStates.Single().DatabaseId);
        Assert.AreEqual(0, sink.OpenedCount);
        Assert.AreEqual(1, sink.FailedCount);
    }

    [TestMethod]
    public async Task InvalidOpenedProjectionPublishesOneFailureWithoutAdmission()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["local://workspace"] = new DatabaseOpenResult(
            ["records"], [], TestDisplayNames.For("records"));
        var workspace = new TableWorkspaceService(gateway);
        var sink = new FakeWebReplySink();
        using var bindings = ReadyBindings(new PluginProjectContext(
            "local:workspace", "", 9));
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://workspace"),
            sink,
            () => null,
            new GridStateCoordinator(gateway, _ => { }),
            pluginBindings: bindings);

        await controller.DispatchAsync(Request(
            "database.openRequested", "open-invalid-projection"));

        Assert.IsNull(workspace.CurrentDatabase);
        Assert.AreEqual(1, sink.Replies.Count(reply => reply.Type == "operation.failed"));
        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "database.opened"));
    }

    [TestMethod]
    public async Task ReusedOpenIdentityIsRejectedWithoutRetiringItsOriginalOperation()
    {
        var started = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var pending = new TaskCompletionSource<DatabaseOpenResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var gateway = new FakeTableRpcGateway
        {
            OpenDatabaseOverride = _ =>
            {
                started.TrySetResult();
                return pending.Task;
            },
        };
        var workspace = new TableWorkspaceService(gateway);
        var sink = new FakeWebReplySink();
        using var dispatcher = new WorkspaceRequestDispatcher(
            workspace,
            new FakeDatabasePicker("local://workspace"),
            sink,
            new GridStateCoordinator(gateway, _ => { }),
            pluginContext: () => new PluginProjectContext(
                "local:workspace", "workspace:9", 9));

        Task original = dispatcher.DispatchAsyncForTesting(Request(
            "database.openRequested", "open-reused"));
        await started.Task.WaitAsync(TimeSpan.FromSeconds(2));
        await dispatcher.DispatchAsyncForTesting(Request(
            "database.openRequested", "open-reused"));
        pending.SetResult(new DatabaseOpenResult(
            ["records"], [], TestDisplayNames.For("records")));
        await original;
        await dispatcher.DispatchAsyncForTesting(Request(
            "database.openRequested", "open-reused"));

        Assert.AreEqual(1, sink.Replies.Count(reply => reply.Type == "database.opened"));
        Assert.AreEqual(2, sink.Replies.Count(reply => reply.Type == "operation.failed"));
        JsonElement failed = Payload(sink.Replies.First(
            reply => reply.Type == "operation.failed"));
        Assert.AreEqual("DATABASE_OPEN_ID_REUSED", failed.GetProperty("code").GetString());
        Assert.AreEqual("local://workspace", workspace.CurrentDatabase);
    }

    [TestMethod]
    public void OpenIdentityHistoryIsBoundedByAuthoritativeSession()
    {
        using var bindings = ReadyBindings(new PluginProjectContext(
            "local:workspace", "workspace:9", 9));
        PluginProjectContextOpenStart first = bindings.BeginOpen("open-session-scoped");
        Assert.IsNotNull(first.Binding);
        Assert.IsTrue(bindings.TryClaimTerminal(first.Binding!));
        bindings.Release(first.Binding!);

        bindings.Set(new PluginProjectContext(
            "local:workspace", "workspace:9", 10));
        PluginProjectContextOpenStart replacement =
            bindings.BeginOpen("open-session-scoped");

        Assert.IsNotNull(replacement.Binding);
        Assert.IsTrue(bindings.TryClaimTerminal(replacement.Binding!));
        bindings.Release(replacement.Binding!);
    }

    [TestMethod]
    public void OpenIdentityReplayWindowIsCapacityBoundedWithoutEvictingActive()
    {
        using var bindings = ReadyBindings(new PluginProjectContext(
            "local:workspace", "workspace:9", 9));
        for (int index = 0;
             index <= PluginProjectContextBindingRegistry.RecentOpenIdentityCapacity;
             index += 1)
        {
            PluginProjectContextOpenStart start = bindings.BeginOpen($"recent-{index}");
            Assert.IsNotNull(start.Binding);
            Assert.IsTrue(bindings.TryClaimTerminal(start.Binding!));
            bindings.Release(start.Binding!);
        }

        PluginProjectContextOpenStart evicted = bindings.BeginOpen("recent-0");
        Assert.IsNotNull(evicted.Binding, "the oldest completed identity must age out");
        Assert.ThrowsExactly<InvalidOperationException>(
            () => bindings.BeginOpen("recent-0"),
            "the active identity must never be evicted or admitted twice");
        Assert.ThrowsExactly<InvalidOperationException>(
            () => bindings.BeginOpen(
                $"recent-{PluginProjectContextBindingRegistry.RecentOpenIdentityCapacity}"),
            "an identity inside the recent replay window must still be rejected");
        Assert.IsTrue(bindings.TryClaimTerminal(evicted.Binding!));
        bindings.Release(evicted.Binding!);
    }

    [TestMethod]
    public void SchemaLifecycleTimeoutDoesNotReuseDashboardPolicy()
    {
        TimeSpan dashboardTimeout = TimeSpan.FromMilliseconds(30);

        TimeSpan schemaTimeout =
            WorkspaceRequestDispatcher.ResolveSchemaLifecycleTimeout(null);

        Assert.AreEqual(SchemaLifecycleBudget.DefaultTimeout, schemaTimeout);
        Assert.AreNotEqual(dashboardTimeout, schemaTimeout);
    }

    private static RoutedWebRequest Request(string type, string requestId)
    {
        string rawPayload = type == "database.openRequested"
            ? $$"""{"openId":"{{requestId}}"}"""
            : "{}";
        using JsonDocument payload = JsonDocument.Parse(rawPayload);
        return new RoutedWebRequest(type, requestId, payload.RootElement.Clone(), string.Empty);
    }

    private static JsonElement Payload(FakeWebReplySink.Reply reply)
        => JsonSerializer.SerializeToElement(reply.Payload);

    private sealed class ThrowOnOpenedReplySink : IWebReplySink
    {
        public int OpenedCount { get; private set; }
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
        public int FailedCount { get; private set; }

        public void PostNotification(string type, object? payload)
        {
            if (type == "database.openCancelled" && ThrowOnCancelled)
                throw new InvalidOperationException("synthetic cancellation sink failure");
            if (type == "database.opened") OpenedCount += 1;
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

    private sealed class ReentrantTableRequestSink(TableWorkspaceService workspace)
        : IWebReplySink
    {
        public Task<bool>? Selection { get; private set; }
        public string? DatabaseObservedDuringPost { get; private set; }
        public int FailedCount { get; private set; }

        public void PostNotification(string type, object? payload)
        {
            if (type != "database.opened") return;
            DatabaseObservedDuringPost = workspace.CurrentDatabase;
            Selection = workspace.SelectTableAsync("records");
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

    private static PluginProjectContextBindingRegistry ReadyBindings(
        PluginProjectContext context,
        CancellationToken token = default)
    {
        var bindings = new PluginProjectContextBindingRegistry();
        bindings.Set(context, token);
        return bindings;
    }

    [TestMethod]
    public void DispatcherComposesControllerOwnedRoutesWithoutFallbackUnion()
    {
        using var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            new FakeWebReplySink(),
            NoDatabaseOpenRoute.Instance);

        Assert.IsTrue(dispatcher.Handles("table.queryRequested"));
        Assert.IsTrue(dispatcher.Handles("dashboard.cancelRequested"));
        Assert.IsTrue(dispatcher.Handles("interface.commitRequested"));
        Assert.IsTrue(dispatcher.Handles("document.listRequested"));
        Assert.IsFalse(dispatcher.Handles("database.openRequested"));
        Assert.IsFalse(dispatcher.Handles("plugin.catalog.list"));
        Assert.IsFalse(dispatcher.Handles("unknown.request"));
    }

    [TestMethod]
    public void DatabaseOpenRouteRequiresCompleteGridCommitDependency()
    {
        Assert.ThrowsExactly<ArgumentNullException>(() =>
            new WorkspaceTableRequestController(
                new TableWorkspaceService(new FakeTableRpcGateway()),
                new FakeDatabasePicker("local://configured"),
                new FakeWebReplySink(),
                () => null,
                (GridStateCoordinator)null!));
    }

    [TestMethod]
    public async Task UnhandledFailureNamesTheOriginatingOperationWithoutLeakingDetails()
    {
        var sink = new FakeWebReplySink();
        using var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            sink,
            NoDatabaseOpenRoute.Instance);
        using var document = JsonDocument.Parse("""{"table":"missing"}""");

        dispatcher.Dispatch(new RoutedWebRequest(
            "table.selected",
            null,
            document.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        JsonElement payload = JsonSerializer.SerializeToElement(failure.Payload);
        Assert.AreEqual("WORKSPACE_ERROR", payload.GetProperty("code").GetString());
        Assert.AreEqual("Workspace operation failed.", payload.GetProperty("message").GetString());
        Assert.AreEqual("table.selected", payload.GetProperty("operation").GetString());
    }

    [TestMethod]
    public async Task TableSelection_SubscriberBackendFailureUsesProgrammerDefectFallback()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(
            new[] { "records" },
            Array.Empty<string>(),
            TestDisplayNames.For("records"));
        gateway.SelectionProjectionResults["records"] = Projection("records");
        var workspace = new TableWorkspaceService(gateway);
        await workspace.OpenDatabaseAsync("db");
        workspace.Notification += _ =>
            throw new BackendUnavailableException("subscriber failed");
        var sink = new FakeWebReplySink();
        using var dispatcher = new WorkspaceRequestDispatcher(
            workspace,
            new FakeDatabasePicker("local://configured"),
            sink,
            NoDatabaseOpenRoute.Instance);
        using var document = JsonDocument.Parse("""{"table":"records"}""");

        dispatcher.Dispatch(new RoutedWebRequest(
            "table.selected",
            "select-subscriber",
            document.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        JsonElement payload = JsonSerializer.SerializeToElement(failure.Payload);
        Assert.AreEqual("WORKSPACE_ERROR", payload.GetProperty("code").GetString());
        Assert.AreEqual("table.selected", payload.GetProperty("operation").GetString());
    }

    [TestMethod]
    public async Task TableSelection_ReportsStableUnavailableAfterRecoveryDeadline()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(
            new[] { "records" },
            Array.Empty<string>(),
            TestDisplayNames.For("records"));
        var lateAttempt = new TaskCompletionSource<TableSelectionProjection>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var attemptStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int attempts = 0;
        gateway.SelectionOpenOverride = (_, _, _) =>
        {
            attempts += 1;
            if (attempts == 1)
            {
                return Task.FromException<TableSelectionProjection>(
                    new BackendUnavailableException("sidecar restarting"));
            }
            attemptStarted.TrySetResult();
            return lateAttempt.Task;
        };
        gateway.EditSchemaResults["records"] = new EditSchemaResult(
            "records",
            "schema-records",
            "primary_key",
            RowKeyStable: true,
            Editable: true,
            Array.Empty<ColumnEditSchema>());
        var time = new ManualTimeProvider();
        var workspace = new TableWorkspaceService(
            gateway,
            selectionRecoveryTimeout: TimeSpan.FromSeconds(3),
            timeProvider: time);
        await workspace.OpenDatabaseAsync("db");
        var notifications = new List<TableNotification>();
        workspace.Notification += notifications.Add;
        var sink = new FakeWebReplySink();
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://configured"),
            sink,
            () => null,
            NoDatabaseOpenRoute.Instance);
        using var document = JsonDocument.Parse("""{"table":"records"}""");

        Task recoveryScheduled = time.WaitForScheduledTimersAsync(2);
        Task selection = controller.DispatchAsync(new RoutedWebRequest(
            "table.selected",
            null,
            document.RootElement.Clone(),
            string.Empty));
        await recoveryScheduled;
        time.Advance(TimeSpan.FromMilliseconds(25));
        await attemptStarted.Task;
        time.Advance(TimeSpan.FromMilliseconds(2_975));

        Assert.IsTrue(selection.IsCompleted,
            "the controller operation must stabilize even if the RPC ignores cancellation");
        await selection;

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        JsonElement payload = JsonSerializer.SerializeToElement(failure.Payload);
        Assert.AreEqual("BACKEND_UNAVAILABLE", payload.GetProperty("code").GetString());
        Assert.AreEqual("table.selected", payload.GetProperty("operation").GetString());

        lateAttempt.SetResult(Projection("records"));
        await Task.Yield();
        Assert.AreEqual(0, notifications.Count,
            "late recovery completion must publish neither dataset nor schema");
        Assert.AreEqual(2, attempts);
    }

    [TestMethod]
    [Timeout(2_000)]
    public async Task TableSelection_SessionCloseSilencesIgnoredCancellationAndLateSuccess()
    {
        using var session = new CancellationTokenSource();
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(
            new[] { "records" },
            Array.Empty<string>(),
            TestDisplayNames.For("records"));
        var readStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var lateRead = new TaskCompletionSource<TableSelectionProjection>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        gateway.SelectionOpenOverride = (_, _, _) =>
        {
            readStarted.TrySetResult();
            return lateRead.Task;
        };
        gateway.EditSchemaResults["records"] = new EditSchemaResult(
            "records",
            "schema-records",
            "primary_key",
            RowKeyStable: true,
            Editable: true,
            Array.Empty<ColumnEditSchema>());
        var workspace = new TableWorkspaceService(gateway);
        await workspace.OpenDatabaseAsync("db");
        var notifications = new List<TableNotification>();
        workspace.Notification += notifications.Add;
        var sink = new FakeWebReplySink();
        int tokenCaptures = 0;
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://configured"),
            sink,
            () => null,
            NoDatabaseOpenRoute.Instance,
            sessionToken: () =>
            {
                tokenCaptures += 1;
                return session.Token;
            });
        using var document = JsonDocument.Parse("""{"table":"records"}""");

        var request = new RoutedWebRequest(
            "table.selected",
            "select-session",
            document.RootElement.Clone(),
            string.Empty);
        Task selection = controller.DispatchAsync(request);
        await readStarted.Task;
        session.Cancel();
        await selection;
        lateRead.SetResult(Projection("records"));
        await Task.Yield();

        Assert.AreEqual(1, tokenCaptures,
            "the controller must capture one stable session token per selection");
        Assert.AreEqual(0, notifications.Count,
            "session close must suppress late dataset and schema notifications");
        Assert.AreEqual(0, sink.Replies.Count,
            "session close is silent and must not post a correlated failure");
    }

    [TestMethod]
    [Timeout(2_000)]
    public async Task TableSelection_SessionCloseSilencesIgnoredProjectionCancellation()
    {
        using var session = new CancellationTokenSource();
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(
            new[] { "records" },
            Array.Empty<string>(),
            TestDisplayNames.For("records"));
        var projectionStarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var lateProjection = new TaskCompletionSource<TableSelectionProjection>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        gateway.SelectionOpenOverride = (_, _, _) =>
        {
            projectionStarted.TrySetResult();
            return lateProjection.Task;
        };
        var workspace = new TableWorkspaceService(gateway);
        await workspace.OpenDatabaseAsync("db");
        var notifications = new List<TableNotification>();
        workspace.Notification += notifications.Add;
        var sink = new FakeWebReplySink();
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://configured"),
            sink,
            () => null,
            NoDatabaseOpenRoute.Instance,
            sessionToken: () => session.Token);
        using var document = JsonDocument.Parse("""{"table":"records"}""");

        var request = new RoutedWebRequest(
            "table.selected",
            "select-schema-session",
            document.RootElement.Clone(),
            string.Empty);
        Task selection = controller.DispatchAsync(request);
        await projectionStarted.Task;
        Assert.AreEqual(0, notifications.Count);

        session.Cancel();
        await selection;
        lateProjection.SetResult(new TableSelectionProjection(
            EmptyPage("records"),
            new EditSchemaResult(
                "records",
                "schema-late",
                "primary_key",
                RowKeyStable: true,
                Editable: true,
                Array.Empty<ColumnEditSchema>())));
        await Task.Yield();

        Assert.AreEqual(0, notifications.Count,
            "late projection must not publish a dataset or schema after session close");
        Assert.AreEqual(0, sink.Replies.Count,
            "session close during schema read must remain silent");
    }

    [TestMethod]
    public async Task TableSelection_RetriesTheWholeProjectionAndUsesRecoveredSchema()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(
            new[] { "records" },
            Array.Empty<string>(),
            TestDisplayNames.For("records"));
        int attempts = 0;
        gateway.SelectionOpenOverride = (table, _, _) =>
        {
            attempts += 1;
            if (attempts == 1)
            {
                return Task.FromException<TableSelectionProjection>(
                    new BackendUnavailableException("sidecar restarting"));
            }
            return Task.FromResult(RevisionedProjection(
                table,
                "schema_0002",
                2,
                "recovered-row"));
        };
        gateway.EditSchemaOverride = (_, _) =>
            Task.FromException<EditSchemaResult>(
                new InvalidOperationException("selection must not perform a second schema RPC"));
        var time = new ManualTimeProvider();
        var workspace = new TableWorkspaceService(
            gateway,
            selectionRecoveryTimeout: TimeSpan.FromSeconds(1),
            timeProvider: time);
        await workspace.OpenDatabaseAsync("db");
        var notifications = new List<TableNotification>();
        workspace.Notification += notifications.Add;
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://configured"),
            new FakeWebReplySink(),
            () => null,
            NoDatabaseOpenRoute.Instance);
        using var document = JsonDocument.Parse("""{"table":"records"}""");

        Task recoveryScheduled = time.WaitForScheduledTimersAsync(2);
        Task selection = controller.DispatchAsync(new RoutedWebRequest(
            "table.selected",
            "recovered-projection",
            document.RootElement.Clone(),
            string.Empty));
        await recoveryScheduled;
        time.Advance(TimeSpan.FromMilliseconds(25));
        await selection;

        Assert.AreEqual(2, attempts);
        TablePage page = notifications
            .Single(notification => notification.Type == "table.datasetReady")
            .Page!;
        EditSchemaResult schema = notifications
            .Single(notification => notification.Type == "table.editSchemaLoaded")
            .MutationResult!.Result as EditSchemaResult
            ?? throw new AssertFailedException("missing recovered edit schema");
        Assert.AreEqual("recovered-row", page.Rows.Single()["rowKey"]);
        Assert.AreEqual("schema_0002", page.QuerySnapshot!.SchemaRevision);
        Assert.AreEqual(2, page.QuerySnapshot.DataRevision);
        Assert.AreEqual(page.QuerySnapshot.SchemaRevision, schema.SchemaRevision);
    }

    [TestMethod]
    public async Task TableSelection_SessionCloseWinsRecoveryDeadlineWithoutReply()
    {
        using var session = new CancellationTokenSource();
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(
            new[] { "records" },
            Array.Empty<string>(),
            TestDisplayNames.For("records"));
        gateway.SelectionOpenOverride = (_, _, _) =>
            Task.FromException<TableSelectionProjection>(
                new BackendUnavailableException("sidecar restarting"));
        var time = new ManualTimeProvider();
        var workspace = new TableWorkspaceService(
            gateway,
            selectionRecoveryTimeout: TimeSpan.FromMilliseconds(25),
            timeProvider: time);
        await workspace.OpenDatabaseAsync("db");
        var sink = new FakeWebReplySink();
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://configured"),
            sink,
            () => null,
            NoDatabaseOpenRoute.Instance,
            sessionToken: () => session.Token);
        using var document = JsonDocument.Parse("""{"table":"records"}""");

        Task timersScheduled = time.WaitForScheduledTimersAsync(2);
        Task selection = controller.DispatchAsync(new RoutedWebRequest(
            "table.selected",
            "select-deadline-session",
            document.RootElement.Clone(),
            string.Empty));
        await timersScheduled;
        time.BeforeTimerFire = session.Cancel;
        time.Advance(TimeSpan.FromMilliseconds(25));
        await selection;

        Assert.IsTrue(session.IsCancellationRequested);
        Assert.AreEqual(0, sink.Replies.Count,
            "session ownership outranks an exhausted deadline and stays silent");
    }

    [TestMethod]
    public async Task TableSelection_DoesNotPublishSchemaAfterDatasetReentersNewSelection()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult(
            new[] { "alpha", "beta" },
            Array.Empty<string>(),
            TestDisplayNames.For("alpha", "beta"));
        gateway.SelectionProjectionResults["alpha"] = Projection("alpha");
        gateway.SelectionProjectionResults["beta"] = Projection("beta");
        gateway.EditSchemaResults["alpha"] = new EditSchemaResult(
            "alpha",
            "schema-alpha",
            "primary_key",
            RowKeyStable: true,
            Editable: true,
            Array.Empty<ColumnEditSchema>());
        var workspace = new TableWorkspaceService(gateway);
        await workspace.OpenDatabaseAsync("db");
        var notifications = new List<TableNotification>();
        Task<bool>? betaSelection = null;
        workspace.Notification += notification =>
        {
            notifications.Add(notification);
            if (notification.Type == "table.datasetReady"
                && notification.Page?.Table == "alpha")
            {
                betaSelection = workspace.SelectTableAsync("beta");
            }
        };
        var controller = new WorkspaceTableRequestController(
            workspace,
            new FakeDatabasePicker("local://configured"),
            new FakeWebReplySink(),
            () => null,
            NoDatabaseOpenRoute.Instance);
        using var document = JsonDocument.Parse("""{"table":"alpha"}""");

        await controller.DispatchAsync(new RoutedWebRequest(
            "table.selected",
            null,
            document.RootElement.Clone(),
            string.Empty));
        Assert.IsNotNull(betaSelection);
        Assert.IsTrue(await betaSelection);

        var schemas = notifications
            .Where(notification => notification.Type == "table.editSchemaLoaded")
            .Select(notification => notification.MutationResult?.Result)
            .OfType<EditSchemaResult>()
            .ToList();
        Assert.IsFalse(schemas.Any(schema => schema.Table == "alpha"),
            "alpha schema must not borrow beta's generation after reentrant selection");
    }

    [TestMethod]
    public void ProductControllerHandlesOnlyRegisteredProductAndRelationRequests()
    {
        foreach (string type in ProductDataRpcRegistry.RequestTypes)
            Assert.IsTrue(ProductDataRequestController.Handles(type), type);
        foreach (string type in RelationLookupRpcRegistry.RequestTypes)
            Assert.IsTrue(ProductDataRequestController.Handles(type), type);
        Assert.IsFalse(ProductDataRequestController.Handles("rpc.invoke"));
        Assert.IsFalse(ProductDataRequestController.Handles("schema.rawRequested"));
    }

    [TestMethod]
    public async Task TableQuery_ForwardsCanonicalAstWithoutRepairingFields()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.QueryWindowResults["records"] = new TablePage(
            "records",
            Array.Empty<ColumnSchema>(),
            Array.Empty<Dictionary<string, object?>>(),
            0,
            500,
            1,
            "server");
        var coordinator = new GridStateCoordinator(gateway, _ => { });
        using var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(gateway),
            new FakeDatabasePicker("local://configured"),
            new FakeWebReplySink(),
            coordinator);
        using var document = JsonDocument.Parse("""
            {
              "table": "records",
              "query": {
                "keyword": "needle",
                "filters": [
                  {
                    "field": "payload",
                    "operator": "contains",
                    "value": "8",
                    "logic": "AND",
                    "ignored": "not-forwarded"
                  },
                  {
                    "field": "metadata",
                    "operator": "in",
                    "value": [{"rank": 2}, 3, true]
                  }
                ],
                "sorts": [
                  {"field": "payload", "direction": "desc", "nullsLast": false}
                ],
                "groups": [
                  {"field": "amount", "direction": "asc", "bucket": "number", "numberInterval": 50},
                  {"field": "created", "direction": "desc", "bucket": "month"}
                ],
                "summaries": [
                  {"field": "amount", "function": "sum"}
                ],
                "offset": 25,
                "limit": 500,
                "groupOffset": 100,
                "groupLimit": 50,
                "ignored": "not-forwarded"
              }
            }
            """);

        dispatcher.Dispatch(new RoutedWebRequest(
            "table.queryRequested",
            "query-1",
            document.RootElement.Clone(),
            ""));
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 150);

        Assert.AreEqual(2, gateway.RawViewQueries.Count,
            "grouped queries use the same opaque AST for cursor rows and aggregates");
        JsonElement query = gateway.RawViewQueries[0];
        Assert.AreEqual("needle", query.GetProperty("keyword").GetString());
        Assert.AreEqual("not-forwarded", query.GetProperty("ignored").GetString());
        Assert.AreEqual(
            "not-forwarded",
            query.GetProperty("filters")[0].GetProperty("ignored").GetString());
        JsonElement composite = query.GetProperty("filters")[1].GetProperty("value");
        Assert.AreEqual(2, composite[0].GetProperty("rank").GetInt32());
        Assert.AreEqual(3, composite[1].GetInt32());
        Assert.IsTrue(composite[2].GetBoolean());
    }

    [TestMethod]
    public async Task TableQuery_ForwardsUnknownOperatorsForSidecarValidation()
    {
        var gateway = new FakeTableRpcGateway();
        var coordinator = new GridStateCoordinator(gateway, _ => { });
        var sink = new FakeWebReplySink();
        using var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(gateway),
            new FakeDatabasePicker("local://configured"),
            sink,
            coordinator);
        using var document = JsonDocument.Parse("""
            {
              "table": "records",
              "query": {
                "filters": [
                  {"field": "payload", "operator": "raw_sql", "value": "x"}
                ]
              }
            }
            """);

        dispatcher.Dispatch(new RoutedWebRequest(
            "table.queryRequested",
            "query-2",
            document.RootElement.Clone(),
            ""));
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 150);

        Assert.AreEqual(
            "raw_sql",
            gateway.RawViewQueries.Single().GetProperty("filters")[0]
                .GetProperty("operator").GetString());
        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "operation.failed"));
    }

    [TestMethod]
    public async Task TableQuery_ForwardsNestedFilterGroupsWithoutFlatteningThem()
    {
        var gateway = new FakeTableRpcGateway();
        var coordinator = new GridStateCoordinator(gateway, _ => { });
        using var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(gateway),
            new FakeDatabasePicker("local://configured"),
            new FakeWebReplySink(),
            coordinator);
        using var document = JsonDocument.Parse("""
            {
              "table": "records",
              "query": {
                "filters": [
                  {
                    "logic": "OR",
                    "groupLogic": "OR",
                    "filters": [
                      {"field": "status", "operator": "eq", "value": "open"},
                      {"field": "priority", "operator": "eq", "value": "urgent"}
                    ]
                  }
                ]
              }
            }
            """);

        dispatcher.Dispatch(new RoutedWebRequest(
            "table.queryRequested",
            "query-nested",
            document.RootElement.Clone(),
            string.Empty));
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 150);

        JsonElement group = gateway.RawViewQueries.Single().GetProperty("filters")[0];
        Assert.AreEqual("OR", group.GetProperty("logic").GetString());
        Assert.AreEqual("OR", group.GetProperty("groupLogic").GetString());
        Assert.AreEqual(2, group.GetProperty("filters").GetArrayLength());
        Assert.AreEqual("status", group.GetProperty("filters")[0].GetProperty("field").GetString());
        Assert.AreEqual("priority", group.GetProperty("filters")[1].GetProperty("field").GetString());
    }

    [TestMethod]
    public async Task ProductQuery_WaitsForReplacementGatewayDuringBackendRecovery()
    {
        await using var staleClient = new JsonRpcClient(new QueryTransport());
        using var staleGateway = new JsonRpcProductDataGateway(staleClient);
        var sink = new FakeWebReplySink();
        var controller = new ProductDataRequestController(sink);
        controller.SetGateway(staleGateway);
        staleGateway.Dispose();

        using var document = JsonDocument.Parse(
            """{"tableId":"tbl_records","query":{"filters":[],"sorts":[],"offset":0,"limit":100}}""");
        Task dispatch = controller.DispatchAsync(new RoutedWebRequest(
            "query.page",
            "recovering-query",
            document.RootElement.Clone(),
            string.Empty));

        await Task.Delay(50);
        await using var readyClient = new JsonRpcClient(new QueryTransport());
        using var readyGateway = new JsonRpcProductDataGateway(readyClient);
        controller.SetGateway(readyGateway);
        await dispatch;

        FakeWebReplySink.Reply? reply = await sink.WaitForAsync("query.page", 4_000);
        Assert.IsNotNull(reply);
        Assert.AreEqual("recovering-query", reply.RequestId);
        Assert.IsFalse(sink.Replies.Any(item => item.Type == "operation.failed"));
    }

    [TestMethod]
    public async Task ProductQuery_ReportsStableUnavailableCodeWhenRecoveryDeadlineExpires()
    {
        await using var staleClient = new JsonRpcClient(new QueryTransport());
        using var staleGateway = new JsonRpcProductDataGateway(staleClient);
        var sink = new FakeWebReplySink();
        var controller = new ProductDataRequestController(
            sink,
            readRecoveryTimeout: TimeSpan.FromMilliseconds(75));
        controller.SetGateway(staleGateway);
        staleGateway.Dispose();

        using var document = JsonDocument.Parse(
            """{"tableId":"tbl_records","query":{"filters":[],"sorts":[],"offset":0,"limit":100}}""");
        await controller.DispatchAsync(new RoutedWebRequest(
            "query.page",
            "unavailable-query",
            document.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        string payload = JsonSerializer.Serialize(failure.Payload);
        StringAssert.Contains(payload, @"""code"":""BACKEND_UNAVAILABLE""");
        Assert.IsFalse(payload.Contains("PRODUCT_DATA_FAILED", StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task ProductWrite_IsNotRetriedWhenGatewayWasDisposed()
    {
        await using var staleClient = new JsonRpcClient(new QueryTransport());
        using var staleGateway = new JsonRpcProductDataGateway(staleClient);
        var sink = new FakeWebReplySink();
        var controller = new ProductDataRequestController(sink);
        controller.SetGateway(staleGateway);
        staleGateway.Dispose();

        using var document = JsonDocument.Parse(
            """{"tableId":"tbl_records","operations":[]}""");
        await controller.DispatchAsync(new RoutedWebRequest(
            "mutation.apply",
            "unsafe-write",
            document.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        string payload = JsonSerializer.Serialize(failure.Payload);
        StringAssert.Contains(payload, @"""code"":""BACKEND_UNAVAILABLE""");
        Assert.IsFalse(sink.Replies.Any(item => item.Type == "mutation.apply"));
    }

    [TestMethod]
    public async Task FieldApply_PublishesExactlyOneTerminalSuccess()
    {
        var sink = new FakeWebReplySink();
        var controller = new ProductDataRequestController(sink);
        await using var client = new JsonRpcClient(new QueryTransport());
        using var gateway = new JsonRpcProductDataGateway(client);
        controller.SetGateway(gateway);
        using var document = JsonDocument.Parse(
            """
            {
              "planId": "plan-1",
              "planHash": "hash-1",
              "operationId": "operation-1",
              "actor": {"id": "tester", "kind": "user"},
              "confirmations": []
            }
            """);

        await controller.DispatchAsync(new RoutedWebRequest(
            "field.change.apply",
            "field-apply-1",
            document.RootElement.Clone(),
            string.Empty));

        FakeWebReplySink.Reply[] terminalReplies = sink.Replies
            .Where(reply => reply.RequestId == "field-apply-1")
            .ToArray();
        Assert.HasCount(1, terminalReplies);
        Assert.AreEqual("field.change.apply", terminalReplies[0].Type);
    }

    private sealed class QueryTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming =
            Channel.CreateUnbounded<JsonElement?>();

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            using var request = JsonDocument.Parse(line);
            string id = request.RootElement.GetProperty("id").GetString()!;
            string method = request.RootElement.GetProperty("method").GetString()!;
            string result = method == "field.change.apply"
                ? """
                  {
                    "contract": "vibetable.schema.v2",
                    "operationId": "operation-1",
                    "planId": "plan-1",
                    "action": "update",
                    "tableId": "tbl_records",
                    "fieldId": "fld_title",
                    "schemaRevision": "schema_0002",
                    "definition": null,
                    "migrationJobId": ""
                  }
                  """
                : """
                  {
                    "rows": [],
                    "total": 0,
                    "snapshot": {"schemaRevision": "schema_0001"}
                  }
                  """;
            using var response = JsonDocument.Parse(
                $$"""
                {
                  "jsonrpc": "2.0",
                  "id": "{{id}}",
                  "result": {{result}}
                }
                """);
            _incoming.Writer.TryWrite(response.RootElement.Clone());
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }

    private static TablePage EmptyPage(string table)
        => new(
            table,
            Array.Empty<ColumnSchema>(),
            Array.Empty<Dictionary<string, object?>>(),
            0,
            TableWorkspaceLimits.MaxPageLimit,
            0,
            "remote");

    private static TableSelectionProjection Projection(string table)
        => new(
            EmptyPage(table),
            new EditSchemaResult(
                table,
                "schema_0001",
                "fake-row-key",
                RowKeyStable: true,
                Editable: false,
                Array.Empty<ColumnEditSchema>()));

    private static TableSelectionProjection RevisionedProjection(
        string table,
        string schemaRevision,
        int dataRevision,
        string rowKey)
    {
        var snapshot = new QuerySnapshot(
            "00000000000000000000000000000000",
            new string('a', 64),
            "local",
            table,
            schemaRevision,
            dataRevision,
            new Dictionary<string, object?> { ["offset"] = 0, ["limit"] = 500 });
        var page = new TablePage(
            table,
            Array.Empty<ColumnSchema>(),
            new[] { new Dictionary<string, object?> { ["rowKey"] = rowKey } },
            0,
            500,
            1,
            "remote",
            1,
            snapshot,
            new MutationRevision("fake", schemaRevision, dataRevision));
        return new TableSelectionProjection(
            page,
            new EditSchemaResult(
                table,
                schemaRevision,
                "fake-row-key",
                RowKeyStable: true,
                Editable: true,
                Array.Empty<ColumnEditSchema>()));
    }

}
