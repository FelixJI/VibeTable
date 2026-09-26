using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceSessionManagerTests
{
    [TestMethod]
    [DataRow("ready")]
    [DataRow("failed")]
    [DataRow("retired")]
    [DataRow("cancelled")]
    [DataRow("host-failed")]
    [DataRow("host-retired")]
    [DataRow("runtime-retired")]
    [DataRow("timeout")]
    public async Task OpenWaitsForProductGatewayActivation(string outcome)
    {
        var time = new ManualTimeProvider();
        using var fixture = new SessionFixture(time);
        WorkspaceRegistryEntryV2 workspace = fixture.AddWorkspace("Binding", "Binding");
        await using var production = new ProductionWorkspaceRuntimeFactory(
            new PocketBaseLaunchOptions
            {
                ExecutablePath = "sidecar.exe", DataDirectory = "unused",
                ExpectedIdentity = new PocketBaseExpectedIdentity("ready", "2.0", "0.40.1", "5", "hash"),
            },
            new BackendLaunchOptions { Command = "backend.exe" });
        await using var runtime = (ProductionWorkspaceRuntime)production.Create(workspace, 1);
        var authority = new ControlledGenerationAuthority();
        var binding = new ControlledSidecarBinding();
        var candidate = new ControlledGatewayCandidate(ignoreCancellation: true);
        var snapshot = new ProductSidecarGenerationSnapshot(runtime, 1,
            new PocketBaseAdminContext(new Uri("http://127.0.0.1:8090/_/"),
                new Uri("http://127.0.0.1:8090/"), "X-VibeTable-Session", "test-secret"),
            new ProductSidecarIdentity(workspace.WorkspaceId.ToString("D"), 1, 1,
                Guid.NewGuid().ToString("D")), []);
        authority.SetCurrent(snapshot);
        using var lifecycle = new ProductSidecarGatewayLifecycle(authority, binding, _ => candidate);
        int clientReady = 0;
        production.ClientReady += () => clientReady++;
        var hostStarted = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var hostReady = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        production.RegisterProductSidecarGatewayLifecycle(lifecycle, async token =>
        {
            if (!await MainWindow.CompleteProductGatewayBindingAsync(
                    lifecycle.TryReplaceAsync(snapshot, token), async () =>
                    {
                        hostStarted.TrySetResult();
                        return await hostReady.Task.WaitAsync(token);
                    }))
                throw new BackendUnavailableException("The binding was retired.");
        });
        fixture.RuntimeFactory.Verify = budget => budget.RunAsync(
            WorkspaceActivationStage.Verification, token => production.ActivateAsync(runtime, token));
        int opened = 0;
        fixture.Manager.Changed += (_, args) =>
        {
            if (args.Session.State == WorkspaceSessionState.OpenedWritable) opened++;
        };
        using var caller = new CancellationTokenSource();
        Task<WorkspaceSessionV2> opening = fixture.Manager.OpenAsync(
            workspace.WorkspaceId, WorkspaceOpenMode.Writable, caller.Token);
        TimeSpan timeout = TimeSpan.FromSeconds(5);
        await candidate.HandshakeStarted.Task.WaitAsync(timeout);
        bool handshakePending = !opening.IsCompleted && opened == 0 && !fixture.Manager.Current.Writable;
        Assert.IsNull(PluginProjectContext.FromSession(fixture.Manager.Current));
        Assert.AreEqual(0, clientReady);
        if (outcome == "retired") authority.SetCurrent(null);
        if (outcome == "cancelled") caller.Cancel();
        if (outcome == "timeout") time.Advance(WorkspaceActivationPolicy.Default.VerificationTimeout);
        if (outcome == "failed") candidate.FailHandshake(new InvalidOperationException("handshake failed"));
        else candidate.CompleteHandshake();
        if (outcome is "ready" or "host-failed" or "host-retired" or "runtime-retired")
        {
            await hostStarted.Task.WaitAsync(timeout);
            Assert.IsFalse(opening.IsCompleted, "workspace.open escaped the pending host gateway binding");
            Assert.AreEqual(0, opened);
            if (outcome == "runtime-retired") production.Deactivate(runtime);
            if (outcome == "host-failed") hostReady.SetException(new InvalidOperationException("host failed"));
            else hostReady.SetResult(outcome != "host-retired");
        }
        if (outcome == "ready")
        {
            Assert.AreEqual(WorkspaceSessionState.OpenedWritable, (await opening.WaitAsync(timeout)).State);
            Assert.AreSame(candidate, binding.Current);
            Assert.IsNotNull(PluginProjectContext.FromSession(fixture.Manager.Current));
        }
        else
        {
            if (outcome == "cancelled")
                await Assert.ThrowsAsync<OperationCanceledException>(() => opening.WaitAsync(timeout));
            else if (outcome == "timeout")
                await Assert.ThrowsExactlyAsync<WorkspaceActivationTimeoutException>(() => opening.WaitAsync(timeout));
            else if (outcome is "failed" or "host-failed")
                await Assert.ThrowsExactlyAsync<InvalidOperationException>(() => opening.WaitAsync(timeout));
            else
                await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() => opening.WaitAsync(timeout));
            Assert.AreEqual(WorkspaceSessionState.Closed, fixture.Manager.Current.State);
            Assert.IsNull(production.CurrentWorkspace);
        }
        Assert.IsTrue(handshakePending, "workspace.open escaped the pending capability handshake");
        Assert.AreEqual(outcome == "ready" ? 1 : 0, opened);
        Assert.AreEqual(outcome == "ready" ? 1 : 0, clientReady);
    }

    [TestMethod]
    public async Task OpenAndCloseRotateEpochAndOwnAtMostOneRuntime()
    {
        using var fixture = new SessionFixture();
        var first = fixture.AddWorkspace("一号", "One");

        var opened = await fixture.Manager.OpenAsync(first.WorkspaceId, WorkspaceOpenMode.Writable);
        var closed = await fixture.Manager.CloseAsync("user-close");

        Assert.AreEqual(WorkspaceSessionState.OpenedWritable, opened.State);
        Assert.IsTrue(opened.Writable);
        Assert.AreEqual(WorkspaceSessionState.Closed, closed.State);
        Assert.AreEqual(1, fixture.RuntimeFactory.MaximumActive);
        Assert.AreEqual(1, fixture.Protection.Calls.Count);
        Assert.AreEqual("user-close", fixture.Protection.Calls[0].Reason);
    }

    [TestMethod]
    public async Task SwitchFailureRestoresPreviousWorkspaceWithNewEpoch()
    {
        using var fixture = new SessionFixture();
        var first = fixture.AddWorkspace("一号", "One");
        var second = fixture.AddWorkspace("二号", "Two");
        await fixture.Manager.OpenAsync(first.WorkspaceId, WorkspaceOpenMode.Writable);
        var oldEpoch = fixture.Manager.Current.SessionEpoch;
        fixture.RuntimeFactory.FailNextStartFor = second.WorkspaceId;

        var error = await Assert.ThrowsExactlyAsync<WorkspaceSwitchException>(
            () => fixture.Manager.SwitchAsync(second.WorkspaceId, WorkspaceOpenMode.Writable));

        Assert.AreEqual(first.WorkspaceId, error.RolledBackSession.WorkspaceId);
        Assert.AreEqual(first.WorkspaceId, fixture.Manager.Current.WorkspaceId);
        Assert.AreEqual(WorkspaceSessionState.OpenedWritable, fixture.Manager.Current.State);
        Assert.IsTrue(fixture.Manager.Current.SessionEpoch > oldEpoch);
        Assert.AreEqual(1, fixture.RuntimeFactory.MaximumActive);
    }

    [TestMethod]
    public async Task OpenFailureLeavesNoPublishedWorkspaceSession()
    {
        using var fixture = new SessionFixture();
        var workspace = fixture.AddWorkspace("故障", "Failure");
        fixture.RuntimeFactory.FailNextStartFor = workspace.WorkspaceId;

        await Assert.ThrowsExactlyAsync<WorkspaceActivationTimeoutException>(
            () => fixture.Manager.OpenAsync(
                workspace.WorkspaceId,
                WorkspaceOpenMode.Writable));

        Assert.IsNull(fixture.Manager.Current.WorkspaceId);
        Assert.AreEqual(WorkspaceSessionState.Closed, fixture.Manager.Current.State);
        Assert.IsFalse(fixture.Manager.Current.Writable);
        Assert.AreEqual(0, fixture.RuntimeFactory.Active);
        Assert.AreEqual(1, fixture.RuntimeFactory.StopCalls);
        Assert.AreEqual(1, fixture.RuntimeFactory.DisposeCalls);
        Assert.AreEqual(1, fixture.RuntimeFactory.Created);
        Assert.AreEqual(0, fixture.Lease.Active);
        Assert.AreEqual(
            WorkspaceActivationOutcome.TimedOut,
            fixture.Manager.LastActivationReport!.Outcome);
    }

    [TestMethod]
    public async Task OpenTimeoutPublishesClosedEvenWhenRuntimeCleanupFails()
    {
        using var fixture = new SessionFixture();
        var workspace = fixture.AddWorkspace("清理失败", "CleanupFailure");
        fixture.RuntimeFactory.FailNextStartFor = workspace.WorkspaceId;
        fixture.RuntimeFactory.FailNextStopFor = workspace.WorkspaceId;

        AggregateException error =
            await Assert.ThrowsExactlyAsync<AggregateException>(() =>
                fixture.Manager.OpenAsync(
                    workspace.WorkspaceId,
                    WorkspaceOpenMode.Writable));

        Assert.IsInstanceOfType<WorkspaceActivationTimeoutException>(
            error.InnerExceptions[0]);
        Assert.AreEqual(WorkspaceSessionState.Closed, fixture.Manager.Current.State);
        Assert.AreEqual(WorkspaceSessionPhase.Idle, fixture.Manager.Current.Phase);
        Assert.IsNull(fixture.Manager.Current.WorkspaceId);
        Assert.AreEqual(1, fixture.RuntimeFactory.StopCalls);
        Assert.AreEqual(1, fixture.RuntimeFactory.DisposeCalls);
        Assert.AreEqual(0, fixture.Lease.Active);
    }

    [TestMethod]
    public async Task FailureBeforePreviousRuntimeStopsKeepsBoundEpoch()
    {
        using var fixture = new SessionFixture();
        var first = fixture.AddWorkspace("一号", "One");
        var second = fixture.AddWorkspace("二号", "Two");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        fixture.RuntimeFactory.FailNextDrainFor = first.WorkspaceId;

        WorkspaceSwitchException error =
            await Assert.ThrowsExactlyAsync<WorkspaceSwitchException>(
                () => fixture.Manager.SwitchAsync(
                    second.WorkspaceId,
                    WorkspaceOpenMode.Writable));

        Assert.AreEqual(opened.SessionEpoch, error.RolledBackSession.SessionEpoch);
        Assert.AreEqual(
            fixture.RuntimeFactory.BoundSessionEpoch,
            error.RolledBackSession.SessionEpoch);
        Assert.AreEqual(first.WorkspaceId, error.RolledBackSession.WorkspaceId);
        Assert.AreEqual(WorkspaceSessionState.OpenedWritable, error.RolledBackSession.State);
    }

    [TestMethod]
    public async Task CloseFailureBeforeRuntimeStopsRestoresOriginalSession()
    {
        using var fixture = new SessionFixture();
        var first = fixture.AddWorkspace("一号", "One");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        fixture.RuntimeFactory.FailNextDrainFor = first.WorkspaceId;

        await Assert.ThrowsExactlyAsync<InvalidOperationException>(
            () => fixture.Manager.CloseAsync("injected-close-failure"));

        Assert.AreEqual(first.WorkspaceId, fixture.Manager.Current.WorkspaceId);
        Assert.AreEqual(opened.SessionEpoch, fixture.Manager.Current.SessionEpoch);
        Assert.AreEqual(
            WorkspaceSessionState.OpenedWritable,
            fixture.Manager.Current.State);
        Assert.IsTrue(fixture.Manager.Current.Writable);
        Assert.AreEqual(1, fixture.Lease.Active);
    }

    [TestMethod]
    public async Task DisposeReleasesSessionLifetimeLease()
    {
        var fixture = new SessionFixture();
        try
        {
            var first = fixture.AddWorkspace("一号", "One");
            await fixture.Manager.OpenAsync(
                first.WorkspaceId,
                WorkspaceOpenMode.Writable);
            Assert.AreEqual(1, fixture.Lease.Active);

            await fixture.Manager.DisposeAsync();

            Assert.AreEqual(0, fixture.Lease.Active);
        }
        finally
        {
            fixture.Dispose();
        }
    }

    [TestMethod]
    public async Task RuntimeStopFailureStillReleasesWriterLease()
    {
        using var fixture = new SessionFixture();
        var first = fixture.AddWorkspace("一号", "One");
        await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        fixture.RuntimeFactory.FailNextStopFor = first.WorkspaceId;

        await Assert.ThrowsExactlyAsync<InvalidOperationException>(
            () => fixture.Manager.CloseAsync("injected-stop-failure"));

        Assert.AreEqual(0, fixture.Lease.Active);
        Assert.AreEqual(0, fixture.RuntimeFactory.Active);
        Assert.AreEqual(WorkspaceSessionState.Closed, fixture.Manager.Current.State);
    }

    [TestMethod]
    public async Task ProvisionalCloseCreatesProtectionSnapshot()
    {
        using var fixture = new SessionFixture();
        var first = fixture.AddWorkspace("一号", "One");
        fixture.Lease.GrantedMode = WorkspaceOpenMode.Provisional;
        await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);

        await fixture.Manager.CloseAsync("provisional-close");

        Assert.HasCount(1, fixture.Protection.Calls);
        Assert.AreEqual(
            "provisional-close",
            fixture.Protection.Calls[0].Reason);
    }

    [TestMethod]
    public async Task RestoreRestartStopsAndVerifiesSameWorkspaceWithNewEpoch()
    {
        using var fixture = new SessionFixture();
        var first = fixture.AddWorkspace("一号", "One");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);

        WorkspaceSessionV2 restarted =
            await fixture.Manager.RestartAfterRestoreAsync(
                first.WorkspaceId,
                opened.SessionEpoch);

        Assert.AreEqual(first.WorkspaceId, restarted.WorkspaceId);
        Assert.IsTrue(restarted.SessionEpoch > opened.SessionEpoch);
        Assert.AreEqual(
            WorkspaceSessionState.OpenedWritable,
            restarted.State);
        Assert.AreEqual(1, fixture.RuntimeFactory.Active);
        Assert.AreEqual(1, fixture.RuntimeFactory.MaximumActive);
        Assert.AreEqual(1, fixture.Lease.Active);
    }

    [TestMethod]
    public async Task OldEpochScopeIsDroppedAfterSwitch()
    {
        using var fixture = new SessionFixture();
        var first = fixture.AddWorkspace("一号", "One");
        var second = fixture.AddWorkspace("二号", "Two");
        var opened = await fixture.Manager.OpenAsync(first.WorkspaceId, WorkspaceOpenMode.Writable);
        var oldScope = new WorkspaceWireScope
        {
            Scope = "workspace",
            WorkspaceId = first.WorkspaceId,
            SessionEpoch = opened.SessionEpoch,
            OperationId = Guid.NewGuid(),
            Sequence = 3,
        };

        await fixture.Manager.SwitchAsync(second.WorkspaceId, WorkspaceOpenMode.Writable);

        Assert.IsFalse(fixture.Manager.Accept(oldScope));
        Assert.IsTrue(fixture.Manager.Accept(new WorkspaceWireScope
        {
            Scope = "workspace",
            WorkspaceId = second.WorkspaceId,
            SessionEpoch = fixture.Manager.Current.SessionEpoch,
            OperationId = Guid.NewGuid(),
            Sequence = 4,
        }, minimumSequence: 4));
    }

    [TestMethod]
    public async Task OneHundredSwitchesLeaveNoOverlappingRuntime()
    {
        using var fixture = new SessionFixture();
        var first = fixture.AddWorkspace("一号", "One");
        var second = fixture.AddWorkspace("二号", "Two");
        await fixture.Manager.OpenAsync(first.WorkspaceId, WorkspaceOpenMode.Writable);

        for (var index = 0; index < 100; index++)
        {
            var target = index % 2 == 0 ? second : first;
            await fixture.Manager.SwitchAsync(target.WorkspaceId, WorkspaceOpenMode.Writable);
        }

        Assert.AreEqual(1, fixture.RuntimeFactory.Active);
        Assert.AreEqual(1, fixture.RuntimeFactory.MaximumActive);
        Assert.AreEqual(101, fixture.RuntimeFactory.Created);
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task LateRegisteredWorkspaceAdvancesPersistedEpochAfterLease(bool changedDuringLease)
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 entry = fixture.AddWorkspace("Existing", "existing");
        var authority = new DesktopWorkspaceAuthorityStore();
        authority.Reserve(entry, 11);
        fixture.RuntimeFactory.UsePersistedAuthority = true;
        if (changedDuringLease)
            fixture.Lease.Acquiring = () => authority.Reserve(entry, 27);
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(entry.WorkspaceId, WorkspaceOpenMode.Writable);
        Assert.AreEqual(changedDuringLease ? 28UL : 12UL, opened.SessionEpoch);
        Assert.AreEqual(opened.SessionEpoch, authority.TryRead(entry)!.LastSessionEpoch);
        Assert.AreEqual(1, fixture.RuntimeFactory.Active);
    }
    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task InvalidPersistedEpochReleasesLeaseWithoutCreatingRuntime(bool corrupt)
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 entry = fixture.AddWorkspace("Existing", "existing");
        var authority = new DesktopWorkspaceAuthorityStore();
        authority.Reserve(entry, long.MaxValue);
        string path = Path.Combine(WorkspaceLayout.Paths(entry.SelectedRoot).Coordination, "desktop-runtime-authority.json");
        if (corrupt) File.WriteAllText(path, "{}");
        byte[] original = File.ReadAllBytes(path);
        fixture.RuntimeFactory.UsePersistedAuthority = true;
        WorkspaceRegistryException error = await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
            fixture.Manager.OpenAsync(entry.WorkspaceId, WorkspaceOpenMode.Writable));
        Assert.AreEqual(corrupt ? "workspace.authority_corrupt" : "workspace.session_epoch_invalid", error.Code);
        Assert.AreEqual(0, fixture.Lease.Active);
        Assert.AreEqual(0, fixture.RuntimeFactory.Created);
        CollectionAssert.AreEqual(original, File.ReadAllBytes(path));
    }
    private sealed class SessionFixture : IDisposable
    {
        public SessionFixture(TimeProvider? activationTimeProvider = null)
        {
            Root = Path.Combine(Path.GetTempPath(), "vibetable-session-" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(Root);
            Registry = new WorkspaceRegistry(Root);
            RuntimeFactory = new FakeRuntimeFactory();
            Protection = new FakeProtectionHook();
            Lease = new FakeLeaseHook();
            Manager = new WorkspaceSessionManager(
                Registry,
                RuntimeFactory,
                Protection,
                Lease,
                activationTimeProvider: activationTimeProvider);
        }

        public string Root { get; }
        public WorkspaceRegistry Registry { get; }
        public FakeRuntimeFactory RuntimeFactory { get; }
        public FakeProtectionHook Protection { get; }
        public FakeLeaseHook Lease { get; }
        public WorkspaceSessionManager Manager { get; }

        public WorkspaceRegistryEntryV2 AddWorkspace(string displayName, string folder)
        {
            var result = WorkspaceLayout.Create(
                Path.Combine(Root, folder),
                displayName,
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.Convenient);
            return Registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = result.Manifest.WorkspaceId,
                DisplayName = displayName,
                SelectedRoot = result.SelectedRoot,
                ActivityRoot = null,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = null,
                LastSyncAt = null,
                PendingSync = false,
            });
        }

        public void Dispose()
        {
            Manager.DisposeAsync().AsTask().GetAwaiter().GetResult();
            try
            {
                if (Directory.Exists(Root))
                    Directory.Delete(Root, recursive: true);
            }
            catch
            {
                // Best effort.
            }
        }
    }

    private sealed class FakeRuntimeFactory : IWorkspaceRuntimeFactory
    {
        public int Active { get; private set; }
        public int MaximumActive { get; private set; }
        public int Created { get; private set; }
        public int StopCalls { get; private set; }
        public int DisposeCalls { get; private set; }
        public Guid? FailNextStartFor { get; set; }
        public Guid? FailNextDrainFor { get; set; }
        public Guid? FailNextStopFor { get; set; }
        public ulong BoundSessionEpoch { get; private set; }
        public bool UsePersistedAuthority { get; set; }
        public Func<WorkspaceActivationBudget, Task>? Verify { get; set; }
        public ulong ReadLastSessionEpoch(WorkspaceRegistryEntryV2 workspace) =>
            UsePersistedAuthority ? new DesktopWorkspaceAuthorityStore().TryRead(workspace)?.LastSessionEpoch ?? 0 : 0;

        public IWorkspaceRuntime Create(WorkspaceRegistryEntryV2 workspace, ulong sessionEpoch)
        {
            if (UsePersistedAuthority)
                new DesktopWorkspaceAuthorityStore().Reserve(workspace, sessionEpoch);
            Created++;
            var fail = FailNextStartFor == workspace.WorkspaceId;
            if (fail)
                FailNextStartFor = null;
            BoundSessionEpoch = sessionEpoch;
            return new FakeRuntime(this, workspace.WorkspaceId, sessionEpoch, fail);
        }

        private sealed class FakeRuntime(
            FakeRuntimeFactory owner,
            Guid workspaceId,
            ulong sessionEpoch,
            bool failStart) : IWorkspaceRuntime
        {
            private bool _started;
            public Guid WorkspaceId { get; } = workspaceId;
            public ulong SessionEpoch { get; } = sessionEpoch;

            public Task StartAsync(
                WorkspaceOpenMode mode,
                WorkspaceActivationBudget budget)
            {
                if (failStart)
                {
                    throw new WorkspaceActivationTimeoutException(
                        WorkspaceActivationStage.Sidecar,
                        TimeSpan.FromSeconds(31));
                }
                _started = true;
                owner.Active++;
                owner.MaximumActive = Math.Max(owner.MaximumActive, owner.Active);
                return Task.CompletedTask;
            }

            public Task VerifyAsync(WorkspaceActivationBudget budget) =>
                owner.Verify?.Invoke(budget) ?? Task.CompletedTask;
            public Task DrainAsync(CancellationToken cancellationToken)
            {
                if (owner.FailNextDrainFor == WorkspaceId)
                {
                    owner.FailNextDrainFor = null;
                    throw new InvalidOperationException("injected drain failure");
                }
                return Task.CompletedTask;
            }

            public Task StopAsync(CancellationToken cancellationToken)
            {
                owner.StopCalls++;
                if (_started)
                {
                    _started = false;
                    owner.Active--;
                }
                if (owner.FailNextStopFor == WorkspaceId)
                {
                    owner.FailNextStopFor = null;
                    throw new InvalidOperationException(
                        "injected stop failure");
                }
                return Task.CompletedTask;
            }

            public ValueTask DisposeAsync()
            {
                owner.DisposeCalls++;
                return ValueTask.CompletedTask;
            }
        }
    }

    private sealed class FakeProtectionHook : IWorkspaceProtectionHook
    {
        public List<(Guid WorkspaceId, ulong Epoch, string Reason)> Calls { get; } = [];

        public Task ProtectAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            string reason,
            CancellationToken cancellationToken)
        {
            Calls.Add((workspaceId, sessionEpoch, reason));
            return Task.CompletedTask;
        }
    }

    private sealed class FakeLeaseHook : IWorkspaceLeaseHook
    {
        public int Active { get; private set; }
        public WorkspaceOpenMode? GrantedMode { get; set; }
        public Action? Acquiring { get; set; }

        public Task<WorkspaceOpenMode> AcquireAsync(
            WorkspaceRegistryEntryV2 workspace,
            WorkspaceOpenMode requestedMode,
            CancellationToken cancellationToken)
        {
            cancellationToken.ThrowIfCancellationRequested();
            Active++;
            Acquiring?.Invoke();
            return Task.FromResult(GrantedMode ?? requestedMode);
        }

        public Task ReleaseAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            CancellationToken cancellationToken)
        {
            if (Active > 0)
                Active--;
            return Task.CompletedTask;
        }
    }
}
