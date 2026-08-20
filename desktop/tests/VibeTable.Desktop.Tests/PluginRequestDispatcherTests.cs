using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PluginRequestDispatcherTests
{
    private static PluginProjectContext ReadyContext() => new("project-1", "1", 1);

    [TestMethod]
    public async Task DispatchUsesCorrelatedClosedResponseTypeAndNeverGenericRpc()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        var picker = new FakePluginPackageSourcePicker(null);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(reply, surfaces, picker, resources);
        dispatcher.SetGateway(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.catalog.list",
            "request-1",
            """{"projectKey":"project-1"}"""));

        Assert.AreEqual("plugin.catalog.list", reply.ResponseType);
        Assert.AreEqual("request-1", reply.RequestId);
        Assert.IsInstanceOfType<PluginRuntimeSnapshot[]>(reply.Payload);
        Assert.AreEqual(1, gateway.ListCalls);
        Assert.IsNull(reply.FailureCode);
    }

    [TestMethod]
    public async Task UnexpectedCommitFailureWritesContentFreeScenarioDiagnostic()
    {
        var reply = new RecordingReplySink();
        var traces = new List<string>();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway
        {
            CommitFailure = new InvalidOperationException("native path must not escape"),
        };
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(@"C:\trusted\clean.vtplugin"),
            resources,
            filePicker: null,
            githubSource: null,
            diagnosticTrace: traces.Add,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-before-failure",
            """{"projectKey":"forged","projectRevision":"forged","sourceLocation":"host-picker"}"""));

        await dispatcher.DispatchAsync(Request(
            "plugin.install.commit",
            "commit-failed",
            """{"planId":"plan-1","projectRevision":"r1"}"""));

        Assert.AreEqual("PLUGIN_OPERATION_FAILED", reply.FailureCode);
        Assert.AreEqual(1, gateway.CancelCalls);
        CollectionAssert.AreEqual(
            new[]
            {
                "Plugin request failed; type=plugin.install.commit; " +
                "exception=InvalidOperationException",
            },
            traces);
        Assert.IsFalse(traces[0].Contains("native path", StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task SurfaceEventStaysLocalAndClosingEventRevokesToken()
    {
        const string hash =
            "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        var surface = surfaces.Open(
            PluginPackageRevision.Create(@"C:\plugins\clean", hash),
            "ui/index.html");
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources);

        await dispatcher.DispatchAsync(Request(
            "plugin.surface.event",
            "request-close",
            JsonSerializer.Serialize(new
            {
                contract = PluginContractVersions.Surface,
                surfaceToken = surface.SurfaceToken,
                @event = PluginSurfaceEvents.Close,
                payload = new { },
            })));

        Assert.AreEqual("plugin.surface.event", reply.ResponseType);
        Assert.AreEqual("request-close", reply.RequestId);
        Assert.IsFalse(surfaces.IsActive(surface.SurfaceToken));
        Assert.IsFalse(dispatcher.HasGateway);
    }

    [TestMethod]
    public async Task InspectInstallResolvesHostPickerWithoutReturningNativePathToWeb()
    {
        const string nativePath = @"C:\trusted\clean.vtplugin";
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(nativePath),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-1",
            """{"projectKey":"project-1","projectRevision":"r1","sourceLocation":"host-picker"}"""));

        Assert.AreEqual(nativePath, gateway.InspectRequest?.SourceLocation);
        Assert.AreEqual("project-1", gateway.InspectRequest?.ProjectKey);
        Assert.AreEqual("1", gateway.InspectRequest?.ProjectRevision);
        var plan = (PluginRuntimeInstallPlan)reply.Payload!;
        Assert.AreEqual(PluginRequestDispatcher.HostManagedSource, plan.SourceLocation);
        Assert.IsFalse(JsonSerializer.Serialize(reply.Payload).Contains(nativePath, StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public async Task CommitRejectsAPlanFromAnOldAuthoritativeSessionBeforeGateway()
    {
        PluginProjectContext context = ReadyContext();
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(@"C:\trusted\clean.vtplugin"),
            resources,
            projectContext: () => context);
        dispatcher.SetGateway(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-old-session",
            """{"projectKey":"forged","projectRevision":"forged","sourceLocation":"host-picker"}"""));
        context = context with { ProjectRevision = "2", SessionGeneration = 2 };
        dispatcher.SetProjectContext(context);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.commit",
            "commit-old-session",
            """{"planId":"plan-1","projectRevision":"1"}"""));

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.IsNull(gateway.CommitRequest);
        Assert.AreEqual("plan-1", gateway.CancelRequest?.PlanId);
    }

    [TestMethod]
    public async Task TransitionCancelsActiveCommitAndSuppressesItsSuccessProjection()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        PluginProjectContext context = ReadyContext();
        var pendingCommit = new TaskCompletionSource<PluginRuntimeSnapshot>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        using var gateway = new FakePluginGateway { PendingCommit = pendingCommit };
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(@"C:\trusted\clean.vtplugin"),
            resources,
            projectContext: () => context);
        dispatcher.SetGateway(gateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-active-commit",
            """{"projectKey":"project-1","projectRevision":"1","sourceLocation":"host-picker"}"""));

        Task committing = dispatcher.DispatchAsync(Request(
            "plugin.install.commit",
            "commit-active",
            """{"planId":"plan-1","projectRevision":"1"}"""));
        await gateway.CommitStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        context = context with { SessionGeneration = 2 };
        dispatcher.SetProjectContext(context);
        Assert.IsTrue(gateway.CommitToken.IsCancellationRequested);
        await committing.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.AreNotEqual("plugin.install.commit", reply.ResponseType);
        Assert.AreEqual(1, gateway.CancelCalls);
    }

    [TestMethod]
    public async Task SuccessfulCommitCompletesOperationWithoutBackendCancel()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(@"C:\trusted\clean.vtplugin"),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-successful-commit",
            """{"projectKey":"project-1","projectRevision":"1","sourceLocation":"host-picker"}"""));

        await dispatcher.DispatchAsync(Request(
            "plugin.install.commit",
            "commit-successful",
            """{"planId":"plan-1","projectRevision":"1"}"""));

        Assert.AreEqual("plugin.install.commit", reply.ResponseType);
        Assert.AreEqual(0, gateway.CancelCalls);
    }

    [TestMethod]
    public async Task TransitionCancelsIgnoredTokenUpgradeAndCleansPlanExactlyOnce()
    {
        PluginProjectContext context = ReadyContext();
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        var pendingUpgrade = new TaskCompletionSource<PluginRuntimeSnapshot>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        using var gateway = new FakePluginGateway { PendingUpgrade = pendingUpgrade };
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(@"C:\trusted\clean.vtplugin"),
            resources,
            projectContext: () => context);
        dispatcher.SetGateway(gateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-upgrade-transition",
            """{"projectKey":"project-1","projectRevision":"1","sourceLocation":"host-picker"}"""));

        Task upgrading = dispatcher.DispatchAsync(Request(
            "plugin.lifecycle.upgrade",
            "upgrade-transition",
            """{"projectKey":"project-1","pluginId":"com.acme.clean","planId":"plan-1","projectRevision":"1"}"""));
        await gateway.UpgradeStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        context = context with { SessionGeneration = 2 };
        dispatcher.SetProjectContext(context);
        await upgrading.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.IsTrue(gateway.UpgradeToken.IsCancellationRequested);
        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.AreEqual(1, gateway.CancelCalls);
    }

    [TestMethod]
    public async Task SameProjectAndRevisionInANewSessionCancelAcceptedPlanAndDeletePackage()
    {
        string downloadedPath = Path.Combine(
            Path.GetTempPath(), $"vibetable-new-session-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(downloadedPath, "downloaded");
        PluginProjectContext context = ReadyContext();
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            filePicker: null,
            githubSource: new FakeGitHubPluginPackageSource(downloadedPath),
            projectContext: () => context);
        dispatcher.SetGateway(gateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.github.inspect",
            "inspect-old-generation",
            """{"projectKey":"project-1","projectRevision":"1","repository":"owner/repo"}"""));

        context = context with { SessionGeneration = 2 };
        dispatcher.SetProjectContext(context);
        await gateway.CancelObserved.Task.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual("plan-1", gateway.CancelRequest?.PlanId);
        Assert.AreEqual(1, gateway.CancelCalls);
        Assert.IsFalse(File.Exists(downloadedPath));
    }

    [TestMethod]
    public async Task ThrowingCleanupTraceCannotInterruptBackgroundPlanAndPackageRelease()
    {
        string downloadedPath = Path.Combine(
            Path.GetTempPath(), $"vibetable-transition-sink-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(downloadedPath, "downloaded");
        var time = new ManualTimeProvider();
        var traces = new List<string>();
        var traceObserved = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        PluginProjectContext context = ReadyContext();
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway
        {
            PendingCancel = new TaskCompletionSource<bool>(
                TaskCreationOptions.RunContinuationsAsynchronously),
        };
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            filePicker: null,
            githubSource: new FakeGitHubPluginPackageSource(downloadedPath),
            projectContext: () => context,
            diagnosticTrace: message =>
            {
                traces.Add(message);
                traceObserved.TrySetResult();
                throw new InvalidOperationException("synthetic trace failure");
            },
            cleanupTimeout: TimeSpan.FromSeconds(1),
            cleanupTimeProvider: time);
        dispatcher.SetGateway(gateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.github.inspect",
            "inspect-before-throwing-terminal",
            """{"projectKey":"project-1","projectRevision":"1","repository":"owner/repo"}"""));

        context = context with { SessionGeneration = 2 };
        dispatcher.SetProjectContext(context);
        await gateway.CancelObserved.Task.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.IsFalse(File.Exists(downloadedPath));
        time.Advance(TimeSpan.FromSeconds(1));
        await traceObserved.Task.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(1, gateway.CancelCalls);
        CollectionAssert.AreEqual(
            new[]
            {
                "Plugin install cleanup failed; code=PLUGIN_INSTALL_CANCEL_TIMEOUT",
            },
            traces);
        Assert.IsNull(reply.FailureCode);
    }

    [TestMethod]
    public async Task UpgradePluginMismatchConsumesAndCancelsPlanBeforeGatewayUpgrade()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(@"C:\trusted\clean.vtplugin"),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-upgrade-mismatch",
            """{"projectKey":"project-1","projectRevision":"1","sourceLocation":"host-picker"}"""));

        await dispatcher.DispatchAsync(Request(
            "plugin.lifecycle.upgrade",
            "upgrade-mismatch",
            """{"projectKey":"project-1","pluginId":"com.acme.other","planId":"plan-1","projectRevision":"1"}"""));

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.IsNull(gateway.UpgradeRequest);
        Assert.AreEqual("plan-1", gateway.CancelRequest?.PlanId);
    }

    [TestMethod]
    [DataRow("context")]
    [DataRow("gateway")]
    [DataRow("dispose")]
    public async Task AdmissionRaceTransfersPlanAndPackageOwnershipExactlyOnce(string transition)
    {
        string downloadedPath = Path.Combine(
            Path.GetTempPath(), $"vibetable-admit-race-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(downloadedPath, "downloaded");
        var package = new DownloadedPluginPackage(
            downloadedPath, "owner/repo", "v1", "plugin.vtplugin", new string('a', 64));
        using var oldGateway = new FakePluginGateway();
        using var newGateway = new FakePluginGateway();
        using var authority = new ProductAuthorityEpoch();
        var registry = new HostInstallPlanLeaseRegistry(authority);
        PluginProjectContext context = ReadyContext();
        registry.SetGateway(oldGateway, context);
        HostInstallPlanBinding binding = registry.Capture()!;
        PluginRuntimeInstallPlan plan = FakePluginGateway.InstallPlan("plan-race", "project-1", "1");
        using var barrier = new Barrier(2);

        Task<bool> admit = Task.Run(() =>
        {
            barrier.SignalAndWait();
            return registry.TryAdmit(binding, plan, package, out _);
        });
        Task<IReadOnlyList<HostInstallPlanLease>> invalidate = Task.Run(() =>
        {
            barrier.SignalAndWait();
            return transition switch
            {
                "context" => registry.SetContext(context with { SessionGeneration = 2 }),
                "gateway" => registry.SetGateway(newGateway, context),
                _ => registry.ClearGateway(oldGateway),
            };
        });
        await Task.WhenAll(admit, invalidate);

        HostInstallPlanLease owned = admit.Result
            ? AssertSingle(invalidate.Result)
            : new HostInstallPlanLease("plan-race", "com.acme.clean", binding, package);
        await owned.Binding.Gateway.CancelInstallAsync(
            new PluginInstallCancelParams(owned.PlanId), CancellationToken.None);
        owned.Package?.Dispose();

        Assert.AreEqual(1, oldGateway.CancelCalls);
        Assert.IsFalse(File.Exists(downloadedPath));
        Assert.IsFalse(registry.TryTake("plan-race", out _));
    }

    [TestMethod]
    public async Task AuthorityTransitionAfterCommitLeasePreventsOldGatewayEntry()
    {
        using var authority = new ProductAuthorityEpoch();
        PluginProjectContext context = ReadyContext();
        authority.Transition(context);
        using var gateway = new FakePluginGateway();
        var registry = new HostInstallPlanLeaseRegistry(authority);
        registry.SetGatewayAfterAuthorityTransition(gateway, context);
        HostInstallPlanBinding binding = registry.Capture()!;
        Assert.IsTrue(registry.TryAdmit(
            binding,
            FakePluginGateway.InstallPlan("plan-commit-race", "project-1", "1"),
            null,
            out _));
        Assert.IsTrue(registry.TryBeginOperation(
            "plan-commit-race",
            null,
            out HostInstallPlanOperation? operation,
            out _));
        await using HostInstallPlanOperation owned = operation!;
        authority.Transition(context with { SessionGeneration = 2 });

        bool started = authority.TryStart(
            owned.Authority,
            token => gateway.CommitInstallAsync(
                new PluginCommitInstallParams("plan-commit-race", "1"),
                token),
            out Task<PluginRuntimeSnapshot>? pending);

        Assert.IsFalse(started);
        Assert.IsNull(pending);
        Assert.IsNull(gateway.CommitRequest);
        await owned.DisposeAsync();
        Assert.AreEqual(1, gateway.CancelCalls);
    }

    [TestMethod]
    public async Task NeverCompletingPlanCancelIsBudgetedAndStillReleasesLocalResources()
    {
        string packagePath = Path.Combine(
            Path.GetTempPath(), $"vibetable-cleanup-budget-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(packagePath, "downloaded");
        var package = new DownloadedPluginPackage(
            packagePath, "owner/repo", "v1", "plugin.vtplugin", new string('a', 64));
        var time = new ManualTimeProvider();
        var traces = new List<string>();
        using var authority = new ProductAuthorityEpoch();
        PluginProjectContext context = ReadyContext();
        authority.Transition(context);
        using var gateway = new FakePluginGateway
        {
            PendingCancel = new TaskCompletionSource<bool>(
                TaskCreationOptions.RunContinuationsAsynchronously),
        };
        var registry = new HostInstallPlanLeaseRegistry(
            authority,
            TimeSpan.FromSeconds(1),
            time,
            traces.Add);
        registry.SetGatewayAfterAuthorityTransition(gateway, context);
        HostInstallPlanBinding binding = registry.Capture()!;
        Assert.IsTrue(registry.TryAdmit(
            binding,
            FakePluginGateway.InstallPlan("plan-budget", "project-1", "1"),
            package,
            out _));
        Assert.IsTrue(registry.TryBeginOperation(
            "plan-budget", null, out HostInstallPlanOperation? operation, out _));

        ValueTask disposing = operation!.DisposeAsync();
        await gateway.CancelObserved.Task.WaitAsync(TimeSpan.FromSeconds(2));
        time.Advance(TimeSpan.FromSeconds(1));
        await disposing.AsTask().WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(1, gateway.CancelCalls);
        Assert.IsTrue(gateway.CancelToken.IsCancellationRequested);
        Assert.IsFalse(File.Exists(packagePath));
        CollectionAssert.AreEqual(
            new[] { "PLUGIN_INSTALL_CANCEL_TIMEOUT" }, traces);
    }

    [TestMethod]
    public async Task StaleInspectionCancelUsesBudgetAndDeletesPackageBeforeRemoteCompletion()
    {
        string packagePath = Path.Combine(
            Path.GetTempPath(), $"vibetable-stale-budget-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(packagePath, "downloaded");
        var time = new ManualTimeProvider();
        var traces = new List<string>();
        PluginProjectContext context = ReadyContext();
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        var pendingInspection = new TaskCompletionSource<PluginRuntimeInstallPlan>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        using var oldGateway = new FakePluginGateway
        {
            PendingCancel = new TaskCompletionSource<bool>(
                TaskCreationOptions.RunContinuationsAsynchronously),
        };
        oldGateway.PendingInspections.Enqueue(pendingInspection);
        using var newGateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            filePicker: null,
            githubSource: new FakeGitHubPluginPackageSource(packagePath),
            diagnosticTrace: message =>
            {
                traces.Add(message);
                throw new InvalidOperationException("synthetic trace failure");
            },
            projectContext: () => context,
            cleanupTimeout: TimeSpan.FromSeconds(1),
            cleanupTimeProvider: time);
        dispatcher.SetGateway(oldGateway);

        Task inspection = dispatcher.DispatchAsync(Request(
            "plugin.install.github.inspect",
            "inspect-stale-budget",
            """{"projectKey":"project-1","projectRevision":"1","repository":"owner/repo"}"""));
        await oldGateway.InspectStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        dispatcher.SetGateway(newGateway);
        pendingInspection.SetResult(
            FakePluginGateway.InstallPlan("plan-stale-budget", "project-1", "1"));
        await oldGateway.CancelObserved.Task.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.IsFalse(File.Exists(packagePath));
        time.Advance(TimeSpan.FromSeconds(1));
        await inspection.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.AreEqual(1, oldGateway.CancelCalls);
        CollectionAssert.AreEqual(
            new[]
            {
                "Plugin install cleanup failed; code=PLUGIN_INSTALL_CANCEL_TIMEOUT",
            },
            traces);
    }

    [TestMethod]
    public async Task ExplicitCancelUsesBudgetAndDeletesPackageBeforeRemoteCompletion()
    {
        string packagePath = Path.Combine(
            Path.GetTempPath(), $"vibetable-explicit-budget-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(packagePath, "downloaded");
        var time = new ManualTimeProvider();
        var traces = new List<string>();
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway
        {
            PendingCancel = new TaskCompletionSource<bool>(
                TaskCreationOptions.RunContinuationsAsynchronously),
        };
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            filePicker: null,
            githubSource: new FakeGitHubPluginPackageSource(packagePath),
            diagnosticTrace: message =>
            {
                traces.Add(message);
                throw new InvalidOperationException("synthetic trace failure");
            },
            projectContext: ReadyContext,
            cleanupTimeout: TimeSpan.FromSeconds(1),
            cleanupTimeProvider: time);
        dispatcher.SetGateway(gateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.github.inspect",
            "inspect-explicit-budget",
            """{"projectKey":"project-1","projectRevision":"1","repository":"owner/repo"}"""));

        Task cancelling = dispatcher.DispatchAsync(Request(
            "plugin.install.cancel",
            "cancel-explicit-budget",
            """{"planId":"plan-1"}"""));
        await gateway.CancelObserved.Task.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.IsFalse(File.Exists(packagePath));
        time.Advance(TimeSpan.FromSeconds(1));
        await cancelling.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(new PluginInstallCancelResult(true), reply.Payload);
        Assert.AreEqual(1, gateway.CancelCalls);
        CollectionAssert.AreEqual(
            new[]
            {
                "Plugin install cleanup failed; code=PLUGIN_INSTALL_CANCEL_TIMEOUT",
            },
            traces);
    }

    [TestMethod]
    public async Task ExplicitCancelCallerCancellationIsReportedAndStillObserved()
    {
        string packagePath = Path.Combine(
            Path.GetTempPath(), $"vibetable-explicit-caller-cancel-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(packagePath, "downloaded");
        int diagnosticCalls = 0;
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway
        {
            PendingCancel = new TaskCompletionSource<bool>(
                TaskCreationOptions.RunContinuationsAsynchronously),
        };
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            filePicker: null,
            githubSource: new FakeGitHubPluginPackageSource(packagePath),
            diagnosticTrace: _ =>
            {
                diagnosticCalls += 1;
                throw new InvalidOperationException("synthetic trace failure");
            },
            projectContext: ReadyContext,
            cleanupTimeout: TimeSpan.FromMinutes(1));
        dispatcher.SetGateway(gateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.github.inspect",
            "inspect-before-caller-cancel",
            """{"projectKey":"project-1","projectRevision":"1","repository":"owner/repo"}"""));

        using var caller = new CancellationTokenSource();
        Task cancelling = dispatcher.DispatchAsync(Request(
            "plugin.install.cancel",
            "cancel-by-caller",
            """{"planId":"plan-1"}"""), caller.Token);
        await gateway.CancelObserved.Task.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.IsFalse(File.Exists(packagePath));

        caller.Cancel();
        await cancelling.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual("PLUGIN_REQUEST_CANCELLED", reply.FailureCode);
        Assert.AreEqual(1, gateway.CancelCalls);
        Assert.AreEqual(0, diagnosticCalls);
    }

    [TestMethod]
    public async Task CleanupDeadlineReturnsFalseAndObservesPendingRemoteCancel()
    {
        var time = new ManualTimeProvider();
        var traces = new List<string>();
        using var gateway = new FakePluginGateway
        {
            PendingCancel = new TaskCompletionSource<bool>(
                TaskCreationOptions.RunContinuationsAsynchronously),
        };
        var cleanup = new HostInstallPlanCleanup(
            TimeSpan.FromSeconds(1),
            time,
            traces.Add);

        Task<bool> cancelling = cleanup.CancelRemoteAsync(gateway, "plan-deadline");
        await gateway.CancelObserved.Task.WaitAsync(TimeSpan.FromSeconds(2));
        time.Advance(TimeSpan.FromSeconds(1));

        Assert.IsFalse(await cancelling.WaitAsync(TimeSpan.FromSeconds(2)));
        Assert.AreEqual(1, gateway.CancelCalls);
        CollectionAssert.AreEqual(
            new[] { "PLUGIN_INSTALL_CANCEL_TIMEOUT" },
            traces);
    }

    [TestMethod]
    public async Task SynchronouslyFaultedRemoteCancelIsObservedAndTraceCannotEscape()
    {
        var traces = new List<string>();
        using var gateway = new FakePluginGateway
        {
            CancelFailure = new InvalidOperationException("synthetic synchronous failure"),
        };
        var cleanup = new HostInstallPlanCleanup(
            TimeSpan.FromSeconds(1),
            TimeProvider.System,
            code =>
            {
                traces.Add(code);
                throw new InvalidOperationException("synthetic trace failure");
            });

        bool cancelled = await cleanup.CancelRemoteAsync(gateway, "plan-sync-fault");

        Assert.IsFalse(cancelled);
        Assert.AreEqual(1, gateway.CancelCalls);
        CollectionAssert.AreEqual(
            new[] { "PLUGIN_INSTALL_CANCEL_FAILED" },
            traces);
    }

    private static HostInstallPlanLease AssertSingle(IReadOnlyList<HostInstallPlanLease> leases)
    {
        Assert.AreEqual(1, leases.Count);
        return leases[0];
    }

    [TestMethod]
    public async Task InspectFromAReplacedGatewayCannotBeAdmitted()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var oldGateway = new FakePluginGateway();
        using var newGateway = new FakePluginGateway();
        var pendingPlan = new TaskCompletionSource<PluginRuntimeInstallPlan>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        oldGateway.PendingInspections.Enqueue(pendingPlan);
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(@"C:\trusted\clean.vtplugin"),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(oldGateway);

        Task pending = dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-old-gateway",
            """{"projectKey":"project-1","projectRevision":"1","sourceLocation":"host-picker"}"""));
        await oldGateway.InspectStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        dispatcher.SetGateway(newGateway);
        pendingPlan.SetResult(FakePluginGateway.InstallPlan("plan-old", "project-1", "1"));
        await pending;

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.AreEqual("plan-old", oldGateway.CancelRequest?.PlanId);
        Assert.IsNull(newGateway.CancelRequest);
    }

    [TestMethod]
    [DataRow("context")]
    [DataRow("gateway")]
    [DataRow("dispose")]
    public async Task PendingRemoteInspectionCannotCrossLifecycleTransition(string transition)
    {
        string downloadedPath = Path.Combine(
            Path.GetTempPath(), $"vibetable-pending-transition-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(downloadedPath, "downloaded");
        PluginProjectContext context = ReadyContext();
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var oldGateway = new FakePluginGateway();
        using var newGateway = new FakePluginGateway();
        var pendingPlan = new TaskCompletionSource<PluginRuntimeInstallPlan>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        oldGateway.PendingInspections.Enqueue(pendingPlan);
        var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            filePicker: null,
            githubSource: new FakeGitHubPluginPackageSource(downloadedPath),
            projectContext: () => context);
        dispatcher.SetGateway(oldGateway);

        Task pending = dispatcher.DispatchAsync(Request(
            "plugin.install.github.inspect",
            $"inspect-before-{transition}",
            """{"projectKey":"project-1","projectRevision":"1","repository":"owner/repo"}"""));
        await oldGateway.InspectStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        switch (transition)
        {
            case "context":
                context = context with { SessionGeneration = 2 };
                dispatcher.SetProjectContext(context);
                break;
            case "gateway":
                dispatcher.SetGateway(newGateway);
                break;
            default:
                dispatcher.Dispose();
                break;
        }
        pendingPlan.SetResult(FakePluginGateway.InstallPlan("plan-old", "project-1", "1"));
        await pending;
        await oldGateway.CancelObserved.Task.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.AreEqual("plan-old", oldGateway.CancelRequest?.PlanId);
        Assert.AreEqual(1, oldGateway.CancelCalls);
        Assert.IsNull(newGateway.CancelRequest);
        Assert.IsFalse(File.Exists(downloadedPath));
        dispatcher.Dispose();
    }

    [TestMethod]
    public async Task InspectInstallRejectsRendererSuppliedNativePathBeforeGateway()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(@"C:\trusted\clean.vtplugin"),
            resources);
        dispatcher.SetGateway(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-raw",
            """{"projectKey":"project-1","projectRevision":"r1","sourceLocation":"C:/untrusted/evil.vtplugin"}"""));

        Assert.AreEqual("PLUGIN_SOURCE_NOT_HOST_SELECTED", reply.FailureCode);
        Assert.IsNull(gateway.InspectRequest);
    }

    [TestMethod]
    public async Task GitHubInspectUsesNativeDownloadAndCancelReleasesItsLease()
    {
        string downloadedPath = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-plugin-download-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(downloadedPath, "downloaded");
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        var github = new FakeGitHubPluginPackageSource(downloadedPath);
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            filePicker: null,
            githubSource: github,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.install.github.inspect",
            "inspect-github",
            """{"projectKey":"project-1","projectRevision":"r1","repository":"owner/repo"}"""));

        Assert.AreEqual("owner/repo", github.Repository);
        Assert.AreEqual(downloadedPath, gateway.InspectRequest?.SourceLocation);
        var plan = (PluginRuntimeInstallPlan)reply.Payload!;
        Assert.AreEqual(PluginRequestDispatcher.HostManagedSource, plan.SourceLocation);
        Assert.IsTrue(File.Exists(downloadedPath));

        await dispatcher.DispatchAsync(Request(
            "plugin.install.cancel",
            "cancel-github",
            """{"planId":"plan-1"}"""));

        Assert.AreEqual(new PluginInstallCancelResult(true), reply.Payload);
        Assert.AreEqual("plan-1", gateway.CancelRequest?.PlanId);
        Assert.IsFalse(File.Exists(downloadedPath));
    }

    [TestMethod]
    public async Task CancelReleasesRemotePackageAndReturnsOwnedWhenBackendCleanupFails()
    {
        string downloadedPath = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-plugin-download-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(downloadedPath, "downloaded");
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway
        {
            CancelFailure = new InvalidOperationException("backend cleanup failed"),
        };
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            filePicker: null,
            githubSource: new FakeGitHubPluginPackageSource(downloadedPath),
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.install.github.inspect",
            "inspect-github-failure",
            """{"projectKey":"project-1","projectRevision":"r1","repository":"owner/repo"}"""));
        await dispatcher.DispatchAsync(Request(
            "plugin.install.cancel",
            "cancel-github-failure",
            """{"planId":"plan-1"}"""));

        Assert.AreEqual(new PluginInstallCancelResult(true), reply.Payload);
        Assert.IsNull(reply.FailureCode);
        Assert.AreEqual("plan-1", gateway.CancelRequest?.PlanId);
        Assert.IsFalse(File.Exists(downloadedPath));
    }

    [TestMethod]
    public async Task ReplacingGatewayCancelsBackendPlanAndReleasesRemotePackage()
    {
        string downloadedPath = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-plugin-download-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(downloadedPath, "downloaded");
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var oldGateway = new FakePluginGateway();
        using var replacementGateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            filePicker: null,
            githubSource: new FakeGitHubPluginPackageSource(downloadedPath),
            projectContext: ReadyContext);
        dispatcher.SetGateway(oldGateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.github.inspect",
            "inspect-before-rebind",
            """{"projectKey":"project-1","projectRevision":"1","repository":"owner/repo"}"""));

        dispatcher.SetGateway(replacementGateway);
        await oldGateway.CancelObserved.Task.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual("plan-1", oldGateway.CancelRequest?.PlanId);
        Assert.IsFalse(File.Exists(downloadedPath));
        Assert.IsFalse(ReferenceEquals(oldGateway, replacementGateway));
    }

    [TestMethod]
    public async Task CancelAndDisposeRaceStillReleaseRemotePackage()
    {
        string downloadedPath = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-plugin-download-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(downloadedPath, "downloaded");
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            filePicker: null,
            githubSource: new FakeGitHubPluginPackageSource(downloadedPath),
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.github.inspect",
            "inspect-before-dispose-race",
            """{"projectKey":"project-1","projectRevision":"1","repository":"owner/repo"}"""));
        using var barrier = new Barrier(2);

        Task cancel = Task.Run(async () =>
        {
            barrier.SignalAndWait();
            try
            {
                await dispatcher.DispatchAsync(Request(
                    "plugin.install.cancel",
                    "cancel-dispose-race",
                    """{"planId":"plan-1"}"""));
            }
            catch (ObjectDisposedException)
            {
                // Dispose won the barrier; it owns the same cleanup transfer.
            }
        });
        Task dispose = Task.Run(() =>
        {
            barrier.SignalAndWait();
            dispatcher.Dispose();
        });
        await Task.WhenAll(cancel, dispose);

        Assert.IsFalse(File.Exists(downloadedPath));
    }

    [TestMethod]
    public async Task ANewInspectionDoesNotReleaseAnUnrelatedRemotePlan()
    {
        string downloadedPath = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-plugin-download-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(downloadedPath, "downloaded");
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        gateway.InspectPlanIds.Enqueue("plan-remote");
        gateway.InspectPlanIds.Enqueue("plan-local");
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(@"C:\trusted\local-plugin"),
            resources,
            filePicker: null,
            githubSource: new FakeGitHubPluginPackageSource(downloadedPath),
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.install.github.inspect",
            "inspect-remote",
            """{"projectKey":"project-1","projectRevision":"r1","repository":"owner/repo"}"""));
        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-local",
            """{"projectKey":"project-1","projectRevision":"r1","sourceLocation":"host-picker"}"""));

        Assert.IsTrue(File.Exists(downloadedPath));
        await dispatcher.DispatchAsync(Request(
            "plugin.install.cancel",
            "cancel-remote",
            """{"planId":"plan-remote"}"""));
        Assert.IsFalse(File.Exists(downloadedPath));
    }

    [TestMethod]
    public void CatalogEventRemovesNativeSourceLocationBeforeWebNotification()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources);
        dispatcher.SetGateway(gateway);

        gateway.RaiseCatalogChanged();

        Assert.AreEqual("plugin.catalog.changed", reply.NotificationType);
        string serialized = JsonSerializer.Serialize(reply.NotificationPayload);
        Assert.IsFalse(serialized.Contains("package.vtplugin", StringComparison.OrdinalIgnoreCase));
        Assert.IsTrue(serialized.Contains(PluginRequestDispatcher.HostManagedSource, StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task FileCapabilityUsesNativePickerAndReturnsPathOnlyToPythonGateway()
    {
        const string selectedPath = @"C:\trusted\plugin-output.csv";
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            new FakePluginFilePicker(selectedPath));
        dispatcher.SetGateway(gateway);

        gateway.RaiseFileRequested();
        await gateway.FileResolution.Task.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual("file-1", gateway.FileResolution.Task.Result.RequestId);
        Assert.AreEqual(selectedPath, gateway.FileResolution.Task.Result.SelectedPath);
        Assert.IsFalse(JsonSerializer.Serialize(reply).Contains(selectedPath, StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public async Task CatalogProjectionCreatesSafeCustomSurfaceWithoutExposingPackagePath()
    {
        string packageRoot = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-dispatcher-{Guid.NewGuid():N}");
        Directory.CreateDirectory(Path.Combine(packageRoot, "ui"));
        try
        {
            var action = new PluginRuntimeAction(
                "open-dashboard",
                new Dictionary<string, string> { ["en-US"] = "Open dashboard" },
                new Dictionary<string, string>(),
                "local",
                PluginRisk.Read,
                "manual",
                [],
                JsonSerializer.SerializeToElement(new { }),
                "dist/workers/open-dashboard.js",
                null,
                null,
                null);
            var manifest = FakePluginGateway.DefaultSnapshot.Manifest with
            {
                Actions = [action],
                Ui = JsonSerializer.SerializeToElement(new
                {
                    customViews = new[]
                    {
                        new
                        {
                            viewId = "dashboard",
                            actionId = "open-dashboard",
                            entry = "ui/index.html",
                        },
                    },
                }),
            };
            var reply = new RecordingReplySink();
            var surfaces = new PluginSurfaceSessionManager();
            var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            using var gateway = new FakePluginGateway
            {
                CatalogSnapshot = FakePluginGateway.DefaultSnapshot with
                {
                    SourceLocation = packageRoot,
                    Manifest = manifest,
                },
            };
            using var dispatcher = new PluginRequestDispatcher(
                reply,
                surfaces,
                new FakePluginPackageSourcePicker(null),
                resources);
            dispatcher.SetGateway(gateway);

            await dispatcher.DispatchAsync(Request(
                "plugin.catalog.list",
                "catalog-surface",
                """{"projectKey":"project-1"}"""));

            string serialized = JsonSerializer.Serialize(reply.Payload);
            Assert.IsFalse(serialized.Contains(packageRoot, StringComparison.OrdinalIgnoreCase));
            Assert.IsTrue(serialized.Contains("surfaceToken", StringComparison.Ordinal));
            Assert.IsTrue(serialized.Contains(
                ".plugins.vibetable.local/ui/index.html",
                StringComparison.Ordinal));
        }
        finally
        {
            Directory.Delete(packageRoot, recursive: true);
        }
    }

    private static RoutedWebRequest Request(string type, string requestId, string payload)
    {
        using var document = JsonDocument.Parse(payload);
        return new RoutedWebRequest(type, requestId, document.RootElement.Clone(), string.Empty);
    }

    private sealed class RecordingReplySink : IWebReplySink
    {
        public string? ResponseType { get; private set; }
        public string? RequestId { get; private set; }
        public object? Payload { get; private set; }
        public string? FailureCode { get; private set; }
        public string? NotificationType { get; private set; }
        public object? NotificationPayload { get; private set; }

        public void PostNotification(string type, object? payload)
        {
            NotificationType = type;
            NotificationPayload = payload;
        }

        public void PostResponse(string type, string? requestId, object? payload)
        {
            ResponseType = type;
            RequestId = requestId;
            Payload = payload;
        }

        public void PostOperationFailed(
            string? requestId,
            string message,
            string? code = null,
            string? operation = null,
            string? operationId = null)
        {
            RequestId = requestId;
            FailureCode = code;
        }
    }

    private sealed class FakePluginPackageSourcePicker(string? selectedPath)
        : IPluginPackageSourcePicker
    {
        public Task<string?> PickAsync(PluginPackagePickKind kind, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(selectedPath);
    }

    private sealed class FakeGitHubPluginPackageSource(string path)
        : IGitHubPluginPackageSource
    {
        public string? Repository { get; private set; }

        public Task<DownloadedPluginPackage> DownloadLatestAsync(
            string repository,
            CancellationToken token)
        {
            token.ThrowIfCancellationRequested();
            Repository = repository;
            return System.Threading.Tasks.Task.FromResult(new DownloadedPluginPackage(
                path,
                repository,
                "v1.0.0",
                "plugin.vtplugin",
                new string('a', 64)));
        }

        public void Dispose() { }
    }

    private sealed class FakePluginFilePicker(string? selectedPath) : IPluginFilePicker
    {
        public Task<string?> PickAsync(PluginRuntimeFileRequest request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(selectedPath);
    }

    private sealed class FakePluginGateway : IPluginRpcGateway
    {
        private static readonly PluginRuntimeManifest Manifest = new(
            "vibetable.plugin-manifest.v1", "com.acme.clean", "1.0.0",
            new Dictionary<string, string> { ["en-US"] = "Clean" },
            new Dictionary<string, string>(),
            JsonDocument.Parse("{}").RootElement.Clone(),
            JsonDocument.Parse("{}").RootElement.Clone(),
            [], JsonDocument.Parse("{}").RootElement.Clone());
        public static readonly PluginRuntimeSnapshot DefaultSnapshot = new(
            "project-1", "com.acme.clean", "1.0.0", new string('a', 64),
            "package", "package.vtplugin", Manifest,
            new Dictionary<string, IReadOnlyDictionary<string, JsonElement>>(),
            "enabled", null, 1);
        private static readonly PluginRuntimeTaskSnapshot Task = new(
            "task-1", "run-1", "com.acme.clean", "1.0.0", "clean", "project-1",
            null, 0, PluginRisk.Read, "queued", false, null, null, null);

        public int ListCalls { get; private set; }
        public PluginInspectInstallParams? InspectRequest { get; private set; }
        public Queue<string> InspectPlanIds { get; } = new();
        public Queue<TaskCompletionSource<PluginRuntimeInstallPlan>> PendingInspections { get; } = new();
        public TaskCompletionSource InspectStarted { get; } = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        public TaskCompletionSource CancelObserved { get; } = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        public PluginInstallCancelParams? CancelRequest { get; private set; }
        public int CancelCalls { get; private set; }
        public PluginCommitInstallParams? CommitRequest { get; private set; }
        public TaskCompletionSource CommitStarted { get; } = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        public TaskCompletionSource<PluginRuntimeSnapshot>? PendingCommit { get; set; }
        public CancellationToken CommitToken { get; private set; }
        public PluginUpgradeParams? UpgradeRequest { get; private set; }
        public TaskCompletionSource UpgradeStarted { get; } = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        public TaskCompletionSource<PluginRuntimeSnapshot>? PendingUpgrade { get; set; }
        public CancellationToken UpgradeToken { get; private set; }
        public PluginRuntimeSnapshot CatalogSnapshot { get; set; } = DefaultSnapshot;
        public Exception? CommitFailure { get; init; }
        public Exception? CancelFailure { get; init; }
        public TaskCompletionSource<bool>? PendingCancel { get; init; }
        public CancellationToken CancelToken { get; private set; }
        public TaskCompletionSource<PluginResolveFileParams> FileResolution { get; } = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        private Action<PluginEventEnvelope>? _catalogChanged;
        private Action<PluginEventEnvelope>? _taskChanged;
        private Action<PluginEventEnvelope>? _interactionRequested;
        private Action<PluginEventEnvelope>? _fileRequested;
        public event Action<PluginEventEnvelope>? CatalogChanged
        {
            add => _catalogChanged += value;
            remove => _catalogChanged -= value;
        }
        public event Action<PluginEventEnvelope>? TaskChanged
        {
            add => _taskChanged += value;
            remove => _taskChanged -= value;
        }
        public event Action<PluginEventEnvelope>? InteractionRequested
        {
            add => _interactionRequested += value;
            remove => _interactionRequested -= value;
        }
        public event Action<PluginEventEnvelope>? FileRequested
        {
            add => _fileRequested += value;
            remove => _fileRequested -= value;
        }

        public void RaiseCatalogChanged()
        {
            _catalogChanged?.Invoke(new PluginEventEnvelope(
                PluginContractVersions.Event,
                "plugin.catalog.changed",
                "project-1",
                CatalogSnapshot.PluginId,
                CatalogSnapshot.Revision,
                JsonSerializer.SerializeToElement(CatalogSnapshot)));
        }

        public void RaiseFileRequested()
        {
            var request = new PluginRuntimeFileRequest(
                "file-1", "run-1", "project-1", CatalogSnapshot.PluginId,
                "export", "write", [], "plugin-output.csv", "text/csv", 1_800_000_000);
            _fileRequested?.Invoke(new PluginEventEnvelope(
                PluginContractVersions.Event,
                "plugin.file.requested",
                "project-1",
                request.RequestId,
                1,
                JsonSerializer.SerializeToElement(request)));
        }

        public Task<PluginRuntimeSnapshot[]> ListCatalogAsync(
            PluginCatalogListParams request, CancellationToken token)
        {
            ListCalls++;
            return System.Threading.Tasks.Task.FromResult(new[] { CatalogSnapshot });
        }
        public Task<PluginRuntimeAuditEvent[]> ListAuditAsync(PluginAuditListParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(Array.Empty<PluginRuntimeAuditEvent>());
        public Task<PluginRuntimeAuditEvent[]> ListPendingCleanupAsync(PluginCatalogListParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(Array.Empty<PluginRuntimeAuditEvent>());

        public Task<PluginRuntimeInstallPlan> InspectInstallAsync(PluginInspectInstallParams request, CancellationToken token)
        {
            InspectRequest = request;
            InspectStarted.TrySetResult();
            if (PendingInspections.TryDequeue(out TaskCompletionSource<PluginRuntimeInstallPlan>? pending))
            {
                return pending.Task;
            }
            string planId = InspectPlanIds.TryDequeue(out string? configuredPlanId)
                ? configuredPlanId
                : "plan-1";
            return System.Threading.Tasks.Task.FromResult(InstallPlan(
                planId, request.ProjectKey, request.ProjectRevision));
        }

        public static PluginRuntimeInstallPlan InstallPlan(
            string planId,
            string projectKey,
            string projectRevision) => new(
                planId, projectKey, projectRevision, "package", "package.vtplugin",
                DefaultSnapshot.PackageHash, Manifest,
                new Dictionary<string, IReadOnlyDictionary<string, JsonElement>>());
        public Task<PluginRuntimeSnapshot> CommitInstallAsync(PluginCommitInstallParams request, CancellationToken token)
        {
            CommitRequest = request;
            CommitToken = token;
            CommitStarted.TrySetResult();
            if (PendingCommit is not null) return PendingCommit.Task;
            return CommitFailure is null
                ? System.Threading.Tasks.Task.FromResult(CatalogSnapshot)
                : System.Threading.Tasks.Task.FromException<PluginRuntimeSnapshot>(CommitFailure);
        }
        public Task<bool> CancelInstallAsync(PluginInstallCancelParams request, CancellationToken token)
        {
            CancelCalls += 1;
            CancelRequest = request;
            CancelToken = token;
            CancelObserved.TrySetResult();
            if (PendingCancel is not null) return PendingCancel.Task;
            return CancelFailure is null
                ? System.Threading.Tasks.Task.FromResult(true)
                : System.Threading.Tasks.Task.FromException<bool>(CancelFailure);
        }
        public Task<PluginRuntimeSnapshot> SetEnabledAsync(PluginSetEnabledParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(CatalogSnapshot);
        public Task<PluginRuntimeSnapshot> UpgradeAsync(PluginUpgradeParams request, CancellationToken token)
        {
            UpgradeRequest = request;
            UpgradeToken = token;
            UpgradeStarted.TrySetResult();
            return PendingUpgrade?.Task
                ?? System.Threading.Tasks.Task.FromResult(CatalogSnapshot);
        }
        public Task<PluginRuntimeSnapshot> RollbackAsync(PluginRollbackParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(CatalogSnapshot);
        public Task<PluginRuntimeUninstallResult> UninstallAsync(PluginUninstallParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(new PluginRuntimeUninstallResult(true, true));
        public Task<PluginRuntimeActionAvailability> DescribeActionAsync(PluginDescribeActionParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(new PluginRuntimeActionAvailability(true, []));
        public Task<PluginRuntimeTaskSnapshot> StartActionAsync(PluginStartActionParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(Task);
        public Task<PluginRuntimeInteractionResolveResult> ResolveInteractionAsync(PluginResolveInteractionParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(new PluginRuntimeInteractionResolveResult("resolved", "rejected"));
        public Task<bool> ResolveFileAsync(PluginResolveFileParams request, CancellationToken token)
        {
            FileResolution.TrySetResult(request);
            return System.Threading.Tasks.Task.FromResult(true);
        }
        public Task<PluginRuntimeTaskSnapshot> CancelTaskAsync(PluginTaskParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(Task);
        public Task<PluginRuntimeTaskSnapshot> GetTaskAsync(PluginTaskParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(Task);
        public void Dispose() { }
    }
}
