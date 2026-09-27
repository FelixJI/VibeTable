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
        using var dispatcher = new PluginRequestDispatcher(reply, surfaces, picker, resources,
            projectContext: ReadyContext,
            sharedRpc: (method, _, _) =>
            {
                Assert.AreEqual("plugin.listCatalog", method);
                return Task.FromResult(JsonSerializer.SerializeToElement(new[] { gateway.CatalogSnapshot }));
            });

        await dispatcher.DispatchAsync(Request(
            "plugin.catalog.list",
            "request-1",
            """{"projectKey":"project-1"}"""));

        Assert.AreEqual("plugin.catalog.list", reply.ResponseType);
        Assert.AreEqual("request-1", reply.RequestId);
        Assert.IsInstanceOfType<PluginRuntimeSnapshot[]>(reply.Payload);
        Assert.AreEqual(0, gateway.ListCalls);
        Assert.IsFalse(dispatcher.HasGateway);
        Assert.IsNull(reply.FailureCode);
    }

    [TestMethod]
    [DataRow(false), DataRow(true)]
    public async Task ExecutionWaitsForLazyGatewayButCatalogAndHostTasksDoNotStartIt(bool switchContext)
    {
        var reply = new RecordingReplySink();
        PluginProjectContext context = ReadyContext();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var attached = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        int starts = 0;
        PluginRequestDispatcher dispatcher = null!;
        using (dispatcher = new PluginRequestDispatcher(reply, surfaces,
            new FakePluginPackageSourcePicker(null), resources, projectContext: () => context,
            sharedRpc: (_, _, _) => Task.FromResult(JsonSerializer.SerializeToElement(new[] { gateway.CatalogSnapshot })),
            ensureGateway: async token =>
            {
                starts++;
                entered.TrySetResult();
                await attached.Task.WaitAsync(token);
                dispatcher.SetGateway(gateway);
            }))
        {
            await dispatcher.DispatchAsync(Request("plugin.catalog.list", "catalog", "{}"));
            await dispatcher.DispatchAsync(Request("plugin.task.get", "task", """{"taskId":"missing"}"""));
            await dispatcher.DispatchAsync(Request("plugin.task.cancel", "cancel", """{"taskId":"missing"}"""));
            Assert.AreEqual(0, starts);
            Task execution = dispatcher.DispatchAsync(Request("plugin.action.describe", "describe",
                """{"pluginId":"clean","actionId":"run"}"""));
            await entered.Task.WaitAsync(TimeSpan.FromSeconds(2));
            Assert.IsFalse(execution.IsCompleted);
            Assert.IsFalse(dispatcher.HasGateway);
            if (switchContext) context = context with { SessionGeneration = 2 };
            attached.SetResult();
            await execution.WaitAsync(TimeSpan.FromSeconds(2));
            Assert.AreEqual(1, starts);
            if (switchContext) Assert.AreEqual("PLUGIN_TASK_STALE", reply.FailureCode);
            else Assert.AreEqual("plugin.action.describe", reply.ResponseType);
        }
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
        string planId = ((PluginRuntimeInstallPlan)reply.Payload!).PlanId;

        await dispatcher.DispatchAsync(Request(
            "plugin.install.commit",
            "commit-failed",
            $$"""{"planId":"{{planId}}","projectRevision":"r1"}"""));

        Assert.AreEqual("PLUGIN_OPERATION_FAILED", reply.FailureCode);
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
        // The host generated the plan identity and the backend echoed it.
        StringAssert.StartsWith(gateway.InspectRequest?.PlanId, "plugin-plan-");
        Assert.AreEqual(gateway.InspectRequest?.PlanId, plan.PlanId);
        Assert.AreEqual(PluginRequestDispatcher.HostManagedSource, plan.SourceLocation);
        Assert.IsFalse(JsonSerializer.Serialize(reply.Payload).Contains(nativePath, StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public async Task InspectRejectsAForeignPlanIdentityEchoBeforeAdmission()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        gateway.EchoOverridePlanIds.Enqueue("plan-forged");
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(@"C:\trusted\clean.vtplugin"),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-forged-echo",
            """{"projectKey":"project-1","projectRevision":"1","sourceLocation":"host-picker"}"""));

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        // The forged identity is never admitted: its commit is unknown.
        await dispatcher.DispatchAsync(Request(
            "plugin.install.commit",
            "commit-forged-echo",
            """{"planId":"plan-forged","projectRevision":"1"}"""));
        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.IsNull(gateway.CommitRequest);
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
        string stalePlanId = ((PluginRuntimeInstallPlan)reply.Payload!).PlanId;
        context = context with { ProjectRevision = "2", SessionGeneration = 2 };
        dispatcher.SetProjectContext(context);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.commit",
            "commit-old-session",
            $$"""{"planId":"{{stalePlanId}}","projectRevision":"1"}"""));

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.IsNull(gateway.CommitRequest);
    }

    [TestMethod]
    public async Task ExplicitCancelDisposesTheDownloadedLeaseWithoutAnyBackendRoundTrip()
    {
        string downloadedPath = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-cancel-no-backend-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(downloadedPath, "downloaded");
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
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.github.inspect",
            "inspect-cancel-no-backend",
            """{"projectKey":"project-1","projectRevision":"1","repository":"owner/repo"}"""));
        string cancelledPlanId = ((PluginRuntimeInstallPlan)reply.Payload!).PlanId;

        await dispatcher.DispatchAsync(Request(
            "plugin.install.cancel",
            "cancel-no-backend",
            $$"""{"planId":"{{cancelledPlanId}}"}"""));

        // The host lease registry is the sole cancel authority: the download
        // lease is taken and disposed locally, Python is never contacted.
        Assert.AreEqual(new PluginInstallCancelResult(true), reply.Payload);
        Assert.IsNull(reply.FailureCode);
        Assert.IsFalse(File.Exists(downloadedPath));
    }

    [TestMethod]
    public async Task CancellingAnUnknownPlanNeverReachesTheBackend()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.install.cancel",
            "cancel-unknown",
            """{"planId":"plan-ghost"}"""));

        Assert.AreEqual(new PluginInstallCancelResult(false), reply.Payload);
        Assert.IsNull(reply.FailureCode);
    }

    [TestMethod]
    public async Task ColdStateUnknownPlanCancelNeverStartsTheBackend()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        int starts = 0;
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            projectContext: ReadyContext,
            ensureGateway: _ =>
            {
                starts++;
                return Task.CompletedTask;
            });

        await dispatcher.DispatchAsync(Request(
            "plugin.install.cancel",
            "cancel-cold",
            """{"planId":"plan-cold"}"""));

        // Cancel is host-owned: with no client ever started, an unknown plan
        // is answered locally without waking the backend.
        Assert.AreEqual(new PluginInstallCancelResult(false), reply.Payload);
        Assert.IsNull(reply.FailureCode);
        Assert.AreEqual(0, starts);
        Assert.IsFalse(dispatcher.HasGateway);
    }

    [TestMethod]
    public async Task GatewayTerminationRetiresInstallPlansWhileKeepingTheGoEpochAlive()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var replacement = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(@"C:\trusted\clean.vtplugin"),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-before-termination",
            """{"projectKey":"project-1","projectRevision":"1","sourceLocation":"host-picker"}"""));
        string terminatedPlanId = ((PluginRuntimeInstallPlan)reply.Payload!).PlanId;

        // A dead Python client retires its plan bindings immediately even
        // though the shared Go epoch stays alive.
        gateway.RaiseTerminated();
        await dispatcher.DispatchAsync(Request(
            "plugin.install.commit",
            "commit-after-termination",
            $$"""{"planId":"{{terminatedPlanId}}","projectRevision":"1"}"""));

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.IsNull(gateway.CommitRequest);

        // Same Go epoch: a restarted client is rebound without any authority
        // transition and fresh plans are admitted again.
        dispatcher.SetGatewayAfterAuthorityTransition(replacement, ReadyContext());
        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-after-termination",
            """{"projectKey":"project-1","projectRevision":"1","sourceLocation":"host-picker"}"""));

        Assert.AreEqual("plugin.install.inspect", reply.ResponseType);
    }

    [TestMethod]
    public async Task CommitResultArrivingAfterGatewayTerminationIsNotProjected()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        var pendingCommit = new TaskCompletionSource<PluginRuntimeSnapshot>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        using var gateway = new FakePluginGateway { PendingCommit = pendingCommit };
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(@"C:\trusted\clean.vtplugin"),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-late-commit",
            """{"projectKey":"project-1","projectRevision":"1","sourceLocation":"host-picker"}"""));
        string latePlanId = ((PluginRuntimeInstallPlan)reply.Payload!).PlanId;

        Task committing = dispatcher.DispatchAsync(Request(
            "plugin.install.commit",
            "commit-late-result",
            $$"""{"planId":"{{latePlanId}}","projectRevision":"1"}"""));
        await gateway.CommitStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        gateway.RaiseTerminated();
        pendingCommit.SetResult(FakePluginGateway.DefaultSnapshot);
        await committing.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.AreNotEqual("plugin.install.commit", reply.ResponseType);
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
        string activePlanId = ((PluginRuntimeInstallPlan)reply.Payload!).PlanId;

        Task committing = dispatcher.DispatchAsync(Request(
            "plugin.install.commit",
            "commit-active",
            $$"""{"planId":"{{activePlanId}}","projectRevision":"1"}"""));
        await gateway.CommitStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        context = context with { SessionGeneration = 2 };
        dispatcher.SetProjectContext(context);
        Assert.IsTrue(gateway.CommitToken.IsCancellationRequested);
        await committing.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.AreNotEqual("plugin.install.commit", reply.ResponseType);
    }

    [TestMethod]
    public async Task SuccessfulCommitProjectsTheLeasedFullPlanOnce()
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
        string successPlanId = ((PluginRuntimeInstallPlan)reply.Payload!).PlanId;

        await dispatcher.DispatchAsync(Request(
            "plugin.install.commit",
            "commit-successful",
            $$"""{"planId":"{{successPlanId}}","projectRevision":"1"}"""));

        Assert.AreEqual("plugin.install.commit", reply.ResponseType);
        Assert.IsNull(reply.FailureCode);
        // The execution payload is the full plan from the consumed lease, not
        // the renderer's plan-id-only commit DTO.
        Assert.IsNotNull(gateway.CommitRequest);
        Assert.AreEqual(successPlanId, gateway.CommitRequest!.Plan.PlanId);
        Assert.AreEqual("project-1", gateway.CommitRequest.Plan.ProjectKey);
        Assert.AreEqual("com.acme.clean", gateway.CommitRequest.Plan.Manifest.PluginId);
        Assert.AreEqual("package.vtplugin", gateway.CommitRequest.Plan.SourceLocation);
        Assert.AreEqual("1", gateway.CommitRequest.ProjectRevision);
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
        string upgradeTransitionPlanId = ((PluginRuntimeInstallPlan)reply.Payload!).PlanId;

        Task upgrading = dispatcher.DispatchAsync(Request(
            "plugin.lifecycle.upgrade",
            "upgrade-transition",
            $$"""{"projectKey":"project-1","pluginId":"com.acme.clean","planId":"{{upgradeTransitionPlanId}}","projectRevision":"1"}"""));
        await gateway.UpgradeStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        context = context with { SessionGeneration = 2 };
        dispatcher.SetProjectContext(context);
        await upgrading.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.IsTrue(gateway.UpgradeToken.IsCancellationRequested);
        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
    }

    [TestMethod]
    public async Task SameProjectAndRevisionInANewSessionReleasesAcceptedPlanAndDeletesPackage()
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

        // The transition invalidates the accepted plan and deletes its
        // download locally; the dead plan cannot be committed again.
        context = context with { SessionGeneration = 2 };
        dispatcher.SetProjectContext(context);
        string retiredPlanId = ((PluginRuntimeInstallPlan)reply.Payload!).PlanId;
        await dispatcher.DispatchAsync(Request(
            "plugin.install.commit",
            "commit-retired-plan",
            $$"""{"planId":"{{retiredPlanId}}","projectRevision":"1"}"""));

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.IsNull(gateway.CommitRequest);
        Assert.IsFalse(File.Exists(downloadedPath));
    }

    [TestMethod]
    public async Task UpgradePluginMismatchConsumesPlanBeforeGatewayUpgrade()
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
        string mismatchPlanId = ((PluginRuntimeInstallPlan)reply.Payload!).PlanId;

        await dispatcher.DispatchAsync(Request(
            "plugin.lifecycle.upgrade",
            "upgrade-mismatch",
            $$"""{"projectKey":"project-1","pluginId":"com.acme.other","planId":"{{mismatchPlanId}}","projectRevision":"1"}"""));

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.IsNull(gateway.UpgradeRequest);
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
            : new HostInstallPlanLease(plan, binding, package);
        owned.Package?.Dispose();

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

        bool started = registry.TryStartOperation(
            owned,
            token => gateway.CommitInstallAsync(
                new PluginCommitInstallExecutionParams(
                    "project-1",
                    FakePluginGateway.InstallPlan("plan-commit-race", "project-1", "1"),
                    "1"),
                token),
            out Task<PluginRuntimeSnapshot>? pending);

        Assert.IsFalse(started);
        Assert.IsNull(pending);
        Assert.IsNull(gateway.CommitRequest);
        await owned.DisposeAsync();
    }

    [TestMethod]
    public async Task DisposingAnUnstartedOperationReleasesPackageAndAuthorityLease()
    {
        string packagePath = Path.Combine(
            Path.GetTempPath(), $"vibetable-cleanup-budget-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(packagePath, "downloaded");
        var package = new DownloadedPluginPackage(
            packagePath, "owner/repo", "v1", "plugin.vtplugin", new string('a', 64));
        using var authority = new ProductAuthorityEpoch();
        PluginProjectContext context = ReadyContext();
        authority.Transition(context);
        using var gateway = new FakePluginGateway();
        var registry = new HostInstallPlanLeaseRegistry(authority);
        registry.SetGatewayAfterAuthorityTransition(gateway, context);
        HostInstallPlanBinding binding = registry.Capture()!;
        Assert.IsTrue(registry.TryAdmit(
            binding,
            FakePluginGateway.InstallPlan("plan-budget", "project-1", "1"),
            package,
            out _));
        Assert.IsTrue(registry.TryBeginOperation(
            "plan-budget", null, out HostInstallPlanOperation? operation, out _));

        await operation!.DisposeAsync();

        // Disposal is purely local: the package lease is released and the
        // authority operation is returned without any backend round trip.
        Assert.IsFalse(File.Exists(packagePath));
        Assert.IsFalse(registry.TryTake("plan-budget", out _));
    }

    [TestMethod]
    public async Task StaleInspectionDeletesPackageWithoutRemoteCancellation()
    {
        string packagePath = Path.Combine(
            Path.GetTempPath(), $"vibetable-stale-budget-{Guid.NewGuid():N}.vtplugin");
        File.WriteAllText(packagePath, "downloaded");
        PluginProjectContext context = ReadyContext();
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        var pendingInspection = new TaskCompletionSource<PluginRuntimeInstallPlan>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        using var oldGateway = new FakePluginGateway();
        oldGateway.PendingInspections.Enqueue(pendingInspection);
        using var newGateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            filePicker: null,
            githubSource: new FakeGitHubPluginPackageSource(packagePath),
            projectContext: () => context);
        dispatcher.SetGateway(oldGateway);

        Task inspection = dispatcher.DispatchAsync(Request(
            "plugin.install.github.inspect",
            "inspect-stale-budget",
            """{"projectKey":"project-1","projectRevision":"1","repository":"owner/repo"}"""));
        await oldGateway.InspectStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        dispatcher.SetGateway(newGateway);
        pendingInspection.SetResult(
            FakePluginGateway.InstallPlan("plan-stale-budget", "project-1", "1"));
        await inspection.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.IsFalse(File.Exists(packagePath));
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
        Assert.IsNull(newGateway.InspectRequest);
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

        Assert.AreEqual("PLUGIN_INSTALL_PLAN_STALE", reply.FailureCode);
        Assert.IsNull(newGateway.InspectRequest);
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
        // The backend echoes the host-generated plan identity.
        Assert.AreEqual(gateway.InspectRequest?.PlanId, plan.PlanId);
        Assert.IsTrue(File.Exists(downloadedPath));

        await dispatcher.DispatchAsync(Request(
            "plugin.install.cancel",
            "cancel-github",
            $$"""{"planId":"{{plan.PlanId}}"}"""));

        Assert.AreEqual(new PluginInstallCancelResult(true), reply.Payload);
        Assert.IsFalse(File.Exists(downloadedPath));
    }

    [TestMethod]
    public async Task ReplacingGatewayReleasesTheRemotePackage()
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
        string racedPlanId = ((PluginRuntimeInstallPlan)reply.Payload!).PlanId;
        using var barrier = new Barrier(2);

        Task cancel = Task.Run(async () =>
        {
            barrier.SignalAndWait();
            try
            {
                await dispatcher.DispatchAsync(Request(
                    "plugin.install.cancel",
                    "cancel-dispose-race",
                    $$"""{"planId":"{{racedPlanId}}"}"""));
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
        string remotePlanId = ((PluginRuntimeInstallPlan)reply.Payload!).PlanId;
        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-local",
            """{"projectKey":"project-1","projectRevision":"r1","sourceLocation":"host-picker"}"""));

        Assert.IsTrue(File.Exists(downloadedPath));
        await dispatcher.DispatchAsync(Request(
            "plugin.install.cancel",
            "cancel-remote",
            $$"""{"planId":"{{remotePlanId}}"}"""));
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
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);

        gateway.RaiseCatalogChanged();
        Assert.IsNull(reply.NotificationType, "Python catalog notifications are retired.");
        PluginEventEnvelope projected = dispatcher.ProjectCatalogEvent(JsonSerializer.SerializeToElement(
            new PluginEventEnvelope(PluginContractVersions.Event, "plugin.catalog.changed", "project-1",
                gateway.CatalogSnapshot.PluginId, gateway.CatalogSnapshot.Revision,
                JsonSerializer.SerializeToElement(gateway.CatalogSnapshot))));
        string serialized = JsonSerializer.Serialize(projected);
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
            new FakePluginFilePicker(selectedPath),
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        // File requests belong to an admitted run; the host admits the task
        // through plugin.action.start before the picker event can be owned.
        await dispatcher.DispatchAsync(Request(
            "plugin.action.start", "start-file-capability", StartActionPayload));

        dispatcher.SetWorkspaceContext(ReadyContext());
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
            string currentCacheRoot = packageRoot;
            using var dispatcher = new PluginRequestDispatcher(
                reply,
                surfaces,
                new FakePluginPackageSourcePicker(null),
                resources,
                projectContext: ReadyContext,
                sharedRpc: (_, _, _) => Task.FromResult(JsonSerializer.SerializeToElement(new[] { gateway.CatalogSnapshot })),
                packageCacheRoot: _ => currentCacheRoot);
            string retained = PluginRetainedPackage.PathFor(packageRoot, gateway.CatalogSnapshot.PackageHash)!;
            using (var package = System.IO.Compression.ZipFile.Open(retained, System.IO.Compression.ZipArchiveMode.Create))
                package.CreateEntry("ui/index.html");
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

            // A relocated workspace must not reuse the original source path or
            // the old runtime's retained resource when its local cache is absent.
            currentCacheRoot = Path.Combine(packageRoot, "relocated", "state", "plugin-packages");
            await dispatcher.DispatchAsync(Request(
                "plugin.catalog.list", "catalog-relocated-empty",
                """{"projectKey":"project-1"}"""));
            serialized = JsonSerializer.Serialize(reply.Payload);
            Assert.IsFalse(serialized.Contains("surfaceToken", StringComparison.Ordinal));
            Assert.IsFalse(serialized.Contains("plugins.vibetable.local", StringComparison.Ordinal));
            Assert.IsFalse(serialized.Contains(packageRoot, StringComparison.OrdinalIgnoreCase));

            Directory.CreateDirectory(currentCacheRoot);
            File.Copy(retained, PluginRetainedPackage.PathFor(currentCacheRoot, gateway.CatalogSnapshot.PackageHash)!);
            await dispatcher.DispatchAsync(Request(
                "plugin.catalog.list", "catalog-relocated-retained",
                """{"projectKey":"project-1"}"""));
            Assert.IsTrue(JsonSerializer.Serialize(reply.Payload).Contains("surfaceToken", StringComparison.Ordinal));
        }
        finally
        {
            Directory.Delete(packageRoot, recursive: true);
        }
    }

    private const string StartActionPayload = """{"projectKey":"project-1","pluginId":"com.acme.clean","actionId":"clean","context":{"contract":"vibetable.command-context.v1","projectKey":"project-1","collection":null,"selectedKeys":[],"querySnapshot":null,"locale":"zh-CN","theme":"light","density":"comfortable","user":{},"hostVersion":"1.0.0"},"input":{}}""";

    private static PluginRuntimeTaskSnapshot TaskState(
        PluginRuntimeTaskSnapshot started,
        string state,
        bool cancelRequested = false) => started with { State = state, CancelRequested = cancelRequested };

    [TestMethod]
    public async Task TaskQueryAndCancelAreOwnedByHostWithoutPublicGatewayQuery()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        dispatcher.SetWorkspaceContext(ReadyContext());

        await dispatcher.DispatchAsync(Request(
            "plugin.action.start", "start-1", StartActionPayload));
        PluginRuntimeTaskSnapshot started = AssertIsTaskSnapshot(reply.Payload);
        StringAssert.StartsWith(started.TaskId, "plugin-task-");
        StringAssert.StartsWith(started.RunId, "plugin-run-");
        gateway.RaiseTaskChanged(TaskState(started, "running"));
        await dispatcher.DispatchAsync(Request(
            "plugin.task.get", "get-running",
            $$"""{"taskId":"{{started.TaskId}}"}"""));
        var running = AssertIsTaskSnapshot(reply.Payload);
        Assert.AreEqual("running", running.State);

        await dispatcher.DispatchAsync(Request(
            "plugin.task.cancel", "cancel-1",
            $$"""{"taskId":"{{started.TaskId}}"}"""));
        var cancelling = AssertIsTaskSnapshot(reply.Payload);
        Assert.IsTrue(cancelling.CancelRequested);
        Assert.AreEqual(1, gateway.CancelTaskCalls);

        gateway.RaiseTaskChanged(TaskState(started, "succeeded", cancelRequested: true), revision: 2);
        await dispatcher.DispatchAsync(Request(
            "plugin.task.cancel", "cancel-late",
            $$"""{"taskId":"{{started.TaskId}}"}"""));
        var succeeded = AssertIsTaskSnapshot(reply.Payload);
        Assert.AreEqual("succeeded", succeeded.State);
        Assert.AreEqual(1, gateway.CancelTaskCalls);
    }

    [TestMethod]
    public async Task GatewayTerminationSettlesAbortedWithUnknownCommitBoundary()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        dispatcher.SetWorkspaceContext(ReadyContext());
        await dispatcher.DispatchAsync(Request(
            "plugin.action.start", "start-aborted", StartActionPayload));
        PluginRuntimeTaskSnapshot started = AssertIsTaskSnapshot(reply.Payload);
        gateway.RaiseTaskChanged(TaskState(started, "running"));

        gateway.RaiseTerminated();

        Assert.AreEqual("plugin.task.changed", reply.NotificationType);
        string settlement = JsonSerializer.Serialize(reply.NotificationPayload);
        Assert.IsTrue(settlement.Contains("\"aborted\"", StringComparison.Ordinal));
        Assert.IsTrue(settlement.Contains("plugin_task_aborted", StringComparison.Ordinal));
        Assert.IsTrue(settlement.Contains("\"commitOutcome\":\"unknown\"", StringComparison.Ordinal));

        await dispatcher.DispatchAsync(Request(
            "plugin.task.get", "get-aborted",
            $$"""{"taskId":"{{started.TaskId}}"}"""));
        var aborted = AssertIsTaskSnapshot(reply.Payload);
        Assert.AreEqual("aborted", aborted.State);
        Assert.AreEqual("plugin_task_aborted", aborted.Error!.Code);

        gateway.RaiseTaskChanged(TaskState(started, "succeeded"), revision: 5);
        await dispatcher.DispatchAsync(Request(
            "plugin.task.get", "get-after-late-report",
            $$"""{"taskId":"{{started.TaskId}}"}"""));
        Assert.AreEqual("aborted", AssertIsTaskSnapshot(reply.Payload).State);
    }

    [TestMethod]
    public async Task StaleConfirmationFromARetiredGenerationIsRejectedAsExpired()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        dispatcher.SetWorkspaceContext(ReadyContext());
        await dispatcher.DispatchAsync(Request(
            "plugin.action.start", "start-confirm", StartActionPayload));
        PluginRuntimeTaskSnapshot started = AssertIsTaskSnapshot(reply.Payload);
        gateway.RaiseInteractionRequested(started.RunId);
        await dispatcher.DispatchAsync(Request(
            "plugin.interaction.resolve",
            "resolve-live",
            $$"""{"runId":"{{started.RunId}}","interactionId":"interaction-1","decision":"approved"}"""));
        Assert.AreEqual(1, gateway.ResolveInteractionCalls);

        gateway.RaiseTerminated();
        await dispatcher.DispatchAsync(Request(
            "plugin.interaction.resolve",
            "resolve-late",
            $$"""{"runId":"{{started.RunId}}","interactionId":"interaction-1","decision":"approved"}"""));

        var result = Assert.IsInstanceOfType<PluginRuntimeInteractionResolveResult>(reply.Payload);
        Assert.AreEqual("expired", result.Status);
        Assert.AreEqual(1, gateway.ResolveInteractionCalls);
    }

    [TestMethod]
    public async Task HostConfirmationUsesExactIdentityAndSingleDecision()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply, surfaces, new FakePluginPackageSourcePicker(null),
            resources, projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        dispatcher.SetWorkspaceContext(ReadyContext());
        await dispatcher.DispatchAsync(Request(
            "plugin.action.start", "start-exact-confirm", StartActionPayload));
        string runId = AssertIsTaskSnapshot(reply.Payload).RunId;
        gateway.RaiseInteractionRequested(runId);

        await dispatcher.DispatchAsync(Request("plugin.interaction.resolve", "wrong-confirm",
            $$"""{"runId":"{{runId}}","interactionId":"wrong","decision":"approved"}"""));
        Assert.AreEqual("expired",
            Assert.IsInstanceOfType<PluginRuntimeInteractionResolveResult>(reply.Payload).Status);
        Assert.AreEqual(0, gateway.ResolveInteractionCalls);

        await dispatcher.DispatchAsync(Request("plugin.interaction.resolve", "first-confirm",
            $$"""{"runId":"{{runId}}","interactionId":"interaction-1","decision":"approved"}"""));
        Assert.AreEqual("approved",
            Assert.IsInstanceOfType<PluginRuntimeInteractionResolveResult>(reply.Payload).Decision);
        await dispatcher.DispatchAsync(Request("plugin.interaction.resolve", "repeat-confirm",
            $$"""{"runId":"{{runId}}","interactionId":"interaction-1","decision":"rejected"}"""));
        var repeated = Assert.IsInstanceOfType<PluginRuntimeInteractionResolveResult>(reply.Payload);
        Assert.AreEqual("already-resolved", repeated.Status);
        Assert.AreEqual("approved", repeated.Decision);
        Assert.AreEqual(1, gateway.ResolveInteractionCalls);
    }

    [TestMethod]
    public async Task ForeignStartContextNeverReachesExecutor()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply, surfaces, new FakePluginPackageSourcePicker(null),
            resources, projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        string foreign = StartActionPayload.Replace(
            """context":{"contract":"vibetable.command-context.v1","projectKey":"project-1" """.Trim(),
            """context":{"contract":"vibetable.command-context.v1","projectKey":"foreign" """.Trim(),
            StringComparison.Ordinal);
        await dispatcher.DispatchAsync(Request("plugin.action.start", "foreign-context", foreign));
        Assert.AreEqual("PLUGIN_TASK_STALE", reply.FailureCode);
        Assert.IsFalse(gateway.StartCalled.Task.IsCompleted);
    }

    [TestMethod]
    public async Task FileRequestCannotBeReplayedAfterItsNativeResolution()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply, surfaces, new FakePluginPackageSourcePicker(null),
            resources, new FakePluginFilePicker(@"C:\trusted\output.csv"),
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        dispatcher.SetWorkspaceContext(ReadyContext());
        await dispatcher.DispatchAsync(Request(
            "plugin.action.start", "start-file-once", StartActionPayload));
        gateway.RaiseFileRequested();
        await gateway.FileResolution.Task.WaitAsync(TimeSpan.FromSeconds(2));
        gateway.RaiseFileRequested();
        Assert.AreEqual(1, gateway.FileResolutions);
    }
    [TestMethod]
    public async Task NativeFileSelectionReturningAfterGenerationLossIsDropped()
    {
        var pickerGate = new TaskCompletionSource<string?>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var replacement = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            new GatedPluginFilePicker(pickerGate.Task),
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        await dispatcher.DispatchAsync(Request(
            "plugin.action.start", "start-file", StartActionPayload));

        dispatcher.SetWorkspaceContext(ReadyContext());
        gateway.RaiseFileRequested();
        dispatcher.SetGateway(replacement);
        pickerGate.TrySetResult(@"C:\trusted\late.csv");
        await Task.Delay(50);

        Assert.AreEqual(0, gateway.FileResolutions);
        Assert.AreEqual(0, replacement.FileResolutions);
    }

    [TestMethod]
    public async Task EarlyExecutionReportsAreNotDowngradedByTheStartResponse()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        var pendingStart = new TaskCompletionSource<PluginRuntimeTaskSnapshot>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        using var gateway = new FakePluginGateway { PendingStart = pendingStart };
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        dispatcher.SetWorkspaceContext(ReadyContext());

        // Notifications can beat the start response on the wire: the running
        // and even the terminal report arrive before the queued echo.
        Task starting = dispatcher.DispatchAsync(Request(
            "plugin.action.start", "start-early-reports", StartActionPayload));
        await gateway.StartCalled.Task.WaitAsync(TimeSpan.FromSeconds(2));
        PluginRuntimeTaskSnapshot started = gateway.LastStarted;
        gateway.RaiseTaskChanged(started with { State = "running" });
        gateway.RaiseTaskChanged(started with
        {
            State = "succeeded",
            Result = new PluginRuntimeResult(
                PluginContractVersions.Result,
                "success",
                "written",
                [],
                null,
                [],
                null,
                []),
        }, revision: 2);
        pendingStart.TrySetResult(started with { State = "queued" });
        await starting.WaitAsync(TimeSpan.FromSeconds(2));

        await dispatcher.DispatchAsync(Request(
            "plugin.task.get", "get-after-early-reports",
            $$"""{"taskId":"{{started.TaskId}}"}"""));
        var snapshot = AssertIsTaskSnapshot(reply.Payload);
        Assert.AreEqual("succeeded", snapshot.State);
        Assert.AreEqual("written", snapshot.Result!.Summary);
        // A cancel arriving after the early success cannot rewrite it.
        await dispatcher.DispatchAsync(Request(
            "plugin.task.cancel", "cancel-after-early-success",
            $$"""{"taskId":"{{started.TaskId}}"}"""));
        Assert.AreEqual("succeeded", AssertIsTaskSnapshot(reply.Payload).State);
        Assert.AreEqual(0, gateway.CancelTaskCalls);
    }

    [TestMethod]
    public async Task RegistryKeepsBoundedTerminalHistoryAcrossClientLoss()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var first = new FakePluginGateway();
        using var second = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(first);
        dispatcher.SetWorkspaceContext(ReadyContext());
        PluginRuntimeTaskSnapshot? oldest = null;
        PluginRuntimeTaskSnapshot? newest = null;
        for (int index = 0; index < 300; index++)
        {
            await dispatcher.DispatchAsync(Request(
                "plugin.action.start", $"start-history-{index}", StartActionPayload));
            PluginRuntimeTaskSnapshot started = AssertIsTaskSnapshot(reply.Payload);
            first.RaiseTaskChanged(started with { State = "succeeded" });
            if (index == 0) oldest = started;
            newest = started;
        }

        // Replacing the client settles nothing (all terminal) but must keep
        // the recent terminal states queryable for the same workspace.
        dispatcher.SetGateway(second);
        await dispatcher.DispatchAsync(Request(
            "plugin.task.get", "get-newest-after-rebind",
            $$"""{"taskId":"{{newest!.TaskId}}"}"""));
        Assert.AreEqual("succeeded", AssertIsTaskSnapshot(reply.Payload).State);

        await dispatcher.DispatchAsync(Request(
            "plugin.task.get", "get-oldest-pruned",
            $$"""{"taskId":"{{oldest!.TaskId}}"}"""));
        Assert.AreEqual("PLUGIN_TASK_NOT_FOUND", reply.FailureCode);
    }

    [TestMethod]
    public async Task AuthorityLossBeforeGatewayClearKeepsAbortedQueryable()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        dispatcher.SetWorkspaceContext(ReadyContext());
        await dispatcher.DispatchAsync(Request(
            "plugin.action.start", "start-binding-loss", StartActionPayload));
        PluginRuntimeTaskSnapshot started = AssertIsTaskSnapshot(reply.Payload);
        gateway.RaiseTaskChanged(TaskState(started, "running"));

        // Real OnRuntimeBindingChanged wiring: the authority transition nulls
        // the admission context first, then the gateway is cleared.
        dispatcher.SetProjectContextAfterAuthorityTransition(null);
        dispatcher.ClearGatewayAfterAuthorityTransition(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.task.get", "get-after-binding-loss",
            $$"""{"taskId":"{{started.TaskId}}"}"""));
        var aborted = AssertIsTaskSnapshot(reply.Payload);
        Assert.AreEqual("aborted", aborted.State);
        Assert.AreEqual("plugin_task_aborted", aborted.Error!.Code);
    }

    [TestMethod]
    public async Task ForeignWorkspaceSessionCannotQueryAnotherWorkspacesTask()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            projectContext: ReadyContext);
        dispatcher.SetGateway(gateway);
        dispatcher.SetWorkspaceContext(ReadyContext());
        await dispatcher.DispatchAsync(Request(
            "plugin.action.start", "start-foreign", StartActionPayload));
        PluginRuntimeTaskSnapshot started = AssertIsTaskSnapshot(reply.Payload);

        dispatcher.SetWorkspaceContext(new PluginProjectContext(
            "local:other", "other:2", 2));
        await dispatcher.DispatchAsync(Request(
            "plugin.task.get", "get-foreign",
            $$"""{"taskId":"{{started.TaskId}}"}"""));

        Assert.AreEqual("PLUGIN_TASK_NOT_FOUND", reply.FailureCode);
    }

    private static PluginRuntimeTaskSnapshot AssertIsTaskSnapshot(object? payload)
        => Assert.IsInstanceOfType<PluginRuntimeTaskSnapshot>(payload)
            ?? throw new InvalidOperationException("task snapshot payload is required");

    private sealed class GatedPluginFilePicker(Task<string?> selection) : IPluginFilePicker
    {
        public Task<string?> PickAsync(
            PluginRuntimeFileRequest request,
            CancellationToken token) => selection;
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
            "project-1", "com.acme.clean", "1.0.0", "sha256:" + new string('a', 64),
            "package", "package.vtplugin", Manifest,
            new Dictionary<string, IReadOnlyDictionary<string, JsonElement>>(),
            "enabled", null, 1);
        private static readonly PluginRuntimeTaskSnapshot Task = new(
            "task-1", "run-1", "com.acme.clean", "1.0.0", "clean", "project-1",
            null, 0, PluginRisk.Read, "queued", false, null, null, null);

        public int ListCalls { get; private set; }
        public PluginInspectInstallExecutionParams? InspectRequest { get; private set; }
        /// <summary>Forces a foreign plan-id echo to simulate a misbehaving backend.</summary>
        public Queue<string> EchoOverridePlanIds { get; } = new();
        public Queue<TaskCompletionSource<PluginRuntimeInstallPlan>> PendingInspections { get; } = new();
        public TaskCompletionSource InspectStarted { get; } = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        public TaskCompletionSource StartCalled { get; } = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        public TaskCompletionSource<PluginRuntimeTaskSnapshot>? PendingStart { get; set; }
        public PluginCommitInstallExecutionParams? CommitRequest { get; private set; }
        public TaskCompletionSource CommitStarted { get; } = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        public TaskCompletionSource<PluginRuntimeSnapshot>? PendingCommit { get; set; }
        public CancellationToken CommitToken { get; private set; }
        public PluginUpgradeExecutionParams? UpgradeRequest { get; private set; }
        public TaskCompletionSource UpgradeStarted { get; } = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        public TaskCompletionSource<PluginRuntimeSnapshot>? PendingUpgrade { get; set; }
        public CancellationToken UpgradeToken { get; private set; }
        public PluginRuntimeSnapshot CatalogSnapshot { get; set; } = DefaultSnapshot;
        public PluginRuntimeTaskSnapshot? StartResult { get; set; }
        public PluginRuntimeTaskSnapshot LastStarted { get; private set; } = Task;
        public int CancelTaskCalls { get; private set; }
        public int ResolveInteractionCalls { get; private set; }
        public Exception? CommitFailure { get; init; }
        public TaskCompletionSource<(string RequestId, string? SelectedPath)> FileResolution { get; } = new(
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
        public event Action? Terminated
        {
            add => _terminated += value;
            remove => _terminated -= value;
        }
        private Action? _terminated;

        public void RaiseTerminated() => _terminated?.Invoke();

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

        public void RaiseTaskChanged(PluginRuntimeTaskSnapshot snapshot, int revision = 1)
        {
            _taskChanged?.Invoke(new PluginEventEnvelope(
                PluginContractVersions.Event,
                "plugin.task.changed",
                snapshot.ProjectKey,
                snapshot.TaskId,
                revision,
                JsonSerializer.SerializeToElement(snapshot)));
        }

        public void RaiseInteractionRequested(string runId)
        {
            var interaction = new PluginRuntimeInteractionSnapshot(
                runId, "project-1", "com.acme.clean", "clean", "desktop-host",
                null, new PluginRuntimePendingConfirmation(
                    "interaction-1", PluginRisk.Write, "确认", 
                    new PluginRuntimeConfirmationPreview([], [], 1, []), 1_800_000_000),
                false);
            _interactionRequested?.Invoke(new PluginEventEnvelope(
                PluginContractVersions.Event,
                "plugin.interaction.requested",
                "project-1",
                runId,
                1,
                JsonSerializer.SerializeToElement(interaction)));
        }

        public void RaiseFileRequested()
        {
            var request = new PluginRuntimeFileRequest(
                "file-1", LastStarted.RunId, "project-1", CatalogSnapshot.PluginId,
                LastStarted.ActionId, "write", [], "plugin-output.csv", "text/csv", 1_800_000_000);
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

        public Task<PluginRuntimeInstallPlan> InspectInstallAsync(
            PluginInspectInstallExecutionParams request,
            CancellationToken token)
        {
            InspectRequest = request;
            InspectStarted.TrySetResult();
            if (PendingInspections.TryDequeue(out TaskCompletionSource<PluginRuntimeInstallPlan>? pending))
            {
                return pending.Task;
            }
            // The backend echoes the host-generated identity unless the test
            // explicitly forces a foreign one.
            string planId = EchoOverridePlanIds.TryDequeue(out string? forcedPlanId)
                ? forcedPlanId
                : request.PlanId;
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
        public Task<PluginRuntimeSnapshot> CommitInstallAsync(
            PluginCommitInstallExecutionParams request,
            CancellationToken token)
        {
            CommitRequest = request;
            CommitToken = token;
            CommitStarted.TrySetResult();
            if (PendingCommit is not null) return PendingCommit.Task;
            return CommitFailure is null
                ? System.Threading.Tasks.Task.FromResult(CatalogSnapshot)
                : System.Threading.Tasks.Task.FromException<PluginRuntimeSnapshot>(CommitFailure);
        }
        public Task<PluginRuntimeSnapshot> SetEnabledAsync(PluginSetEnabledParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(CatalogSnapshot);
        public Task<PluginRuntimeSnapshot> UpgradeAsync(
            PluginUpgradeExecutionParams request,
            CancellationToken token)
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
        {
            // The host generates the identity; the executor only echoes it.
            LastStarted = (StartResult ?? Task) with
            {
                TaskId = request.TaskId ?? Task.TaskId,
                RunId = request.RunId ?? Task.RunId,
            };
            StartCalled.TrySetResult();
            return PendingStart?.Task
                ?? System.Threading.Tasks.Task.FromResult(LastStarted);
        }
        public Task<bool> ResolveFileAsync(PluginRuntimeFileRequest request, string? selectedPath, CancellationToken token)
        {
            FileResolutions += 1;
            FileResolution.TrySetResult((request.RequestId, selectedPath));
            return System.Threading.Tasks.Task.FromResult(true);
        }
        public int FileResolutions { get; private set; }
        public Task<bool> CancelTaskAsync(PluginTaskParams request, CancellationToken token)
        {
            CancelTaskCalls += 1;
            return System.Threading.Tasks.Task.FromResult(true);
        }
        public Task<PluginRuntimeInteractionResolveResult> ResolveInteractionAsync(PluginResolveInteractionParams request, CancellationToken token)
        {
            ResolveInteractionCalls += 1;
            return System.Threading.Tasks.Task.FromResult(new PluginRuntimeInteractionResolveResult("resolved", "rejected"));
        }
        public void Dispose() { }
    }
}
