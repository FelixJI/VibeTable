using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceSessionManagerTests
{
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

    private sealed class SessionFixture : IDisposable
    {
        public SessionFixture()
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
                Lease);
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
        public Guid? FailNextStartFor { get; set; }
        public Guid? FailNextDrainFor { get; set; }
        public ulong BoundSessionEpoch { get; private set; }

        public IWorkspaceRuntime Create(WorkspaceRegistryEntryV2 workspace, ulong sessionEpoch)
        {
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

            public Task StartAsync(WorkspaceOpenMode mode, CancellationToken cancellationToken)
            {
                if (failStart)
                    throw new InvalidOperationException("injected start failure");
                _started = true;
                owner.Active++;
                owner.MaximumActive = Math.Max(owner.MaximumActive, owner.Active);
                return Task.CompletedTask;
            }

            public Task VerifyAsync(CancellationToken cancellationToken) => Task.CompletedTask;
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
                if (_started)
                {
                    _started = false;
                    owner.Active--;
                }
                return Task.CompletedTask;
            }

            public ValueTask DisposeAsync() => ValueTask.CompletedTask;
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

        public Task<WorkspaceOpenMode> AcquireAsync(
            WorkspaceRegistryEntryV2 workspace,
            WorkspaceOpenMode requestedMode,
            CancellationToken cancellationToken)
        {
            cancellationToken.ThrowIfCancellationRequested();
            Active++;
            return Task.FromResult(requestedMode);
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
