using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceReplicaStatusMonitorTests
{
    [TestMethod]
    public void StatusParserIsClosedAndEnforcesStatePendingInvariant()
    {
        WorkspaceReplicaStatus parsed =
            WorkspaceReplicaStatusMonitor.Parse(
                JsonSerializer.SerializeToElement(new
                {
                    coordinationStrength = "advisory",
                    syncState = "replicated",
                    pendingSync = false,
                }));

        Assert.AreEqual(
            WorkspaceCoordinationStrength.Advisory,
            parsed.CoordinationStrength);
        Assert.AreEqual("replicated", parsed.SyncState);
        Assert.IsFalse(parsed.PendingSync);
        _ = Assert.ThrowsExactly<InvalidOperationException>(() =>
            WorkspaceReplicaStatusMonitor.Parse(
                JsonSerializer.SerializeToElement(new
                {
                    coordinationStrength = "advisory",
                    syncState = "replicated",
                    pendingSync = true,
                })));
        _ = Assert.ThrowsExactly<InvalidOperationException>(() =>
            WorkspaceReplicaStatusMonitor.Parse(
                JsonSerializer.SerializeToElement(new
                {
                    coordinationStrength = "advisory",
                    syncState = "pending",
                    pendingSync = true,
                    unexpected = true,
                })));
    }

    [TestMethod]
    public void StatusProjectionUpdatesHealthPendingAndLastSync()
    {
        DateTimeOffset observedAt = DateTimeOffset.UtcNow;
        WorkspaceRegistryEntryV2 current = Entry() with
        {
            LastKnownHealth = WorkspaceHealth.Degraded,
            LastSyncAt = observedAt.AddHours(-1),
            PendingSync = true,
        };

        WorkspaceHealthObservation replicated =
            WorkspaceReplicaStatusMonitor.ProjectHealth(
                current,
                new WorkspaceReplicaStatus(
                    WorkspaceCoordinationStrength.Advisory,
                    "replicated",
                    PendingSync: false),
                observedAt);
        WorkspaceHealthObservation failed =
            WorkspaceReplicaStatusMonitor.ProjectHealth(
                current,
                new WorkspaceReplicaStatus(
                    WorkspaceCoordinationStrength.Advisory,
                    "failed",
                    PendingSync: true),
                observedAt);

        Assert.AreEqual(WorkspaceHealth.Healthy, replicated.Health);
        Assert.IsFalse(replicated.PendingSync);
        Assert.AreEqual(observedAt, replicated.LastSyncAt);
        Assert.AreEqual(WorkspaceHealth.Degraded, failed.Health);
        Assert.IsTrue(failed.PendingSync);
        Assert.IsNull(failed.LastSyncAt);
    }

    [TestMethod]
    public async Task PollingIsBoundToOneSessionAndStopsOnUnbind()
    {
        int calls = 0;
        var reached = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        await using var monitor = new WorkspaceReplicaStatusMonitor(
            (_, _, _) =>
            {
                if (Interlocked.Increment(ref calls) >= 3)
                    reached.TrySetResult();
                return Task.FromResult(new WorkspaceReplicaStatus(
                    WorkspaceCoordinationStrength.Advisory,
                    "pending",
                    PendingSync: true));
            },
            activeInterval: TimeSpan.FromMilliseconds(5),
            idleInterval: TimeSpan.FromMilliseconds(20),
            requestTimeout: TimeSpan.FromSeconds(1));
        Guid workspaceId = Guid.NewGuid();

        monitor.Bind(workspaceId, sessionEpoch: 7, enabled: true);
        await reached.Task.WaitAsync(TimeSpan.FromSeconds(2));
        monitor.Bind(Guid.Empty, sessionEpoch: 0, enabled: false);
        await Task.Delay(30);
        int stoppedAt = Volatile.Read(ref calls);
        await Task.Delay(40);

        Assert.AreEqual(stoppedAt, Volatile.Read(ref calls));
    }

    [TestMethod]
    public async Task ControllerRefreshProjectsRegistryEventAndBootstrapThroughPorts()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-replica-controller-");
        WorkspaceRegistryEntryV2 workspace = fixture.AddDirectWorkspace("Replica") with
        {
            ActivityRoot = Path.Combine(fixture.Root, "activity", "Replica"),
            CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
            LastKnownHealth = WorkspaceHealth.Degraded,
            PendingSync = true,
        };
        fixture.Registry.Unregister(workspace.WorkspaceId);
        fixture.Registry.Register(workspace);
        fixture.Session.CurrentWorkspace = workspace;
        fixture.Session.CurrentSession = new WorkspaceSessionV2
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = workspace.WorkspaceId,
            SessionEpoch = 9,
            State = WorkspaceSessionState.OpenedWritable,
            OpenMode = WorkspaceOpenMode.Writable,
            Writable = true,
            Provisional = false,
            Phase = WorkspaceSessionPhase.Idle,
            ErrorCode = null,
        };
        fixture.Session.CurrentCapabilities = new WorkspaceV2SidecarCapabilities(
            WorkspaceV2Json.ContractVersion,
            workspace.WorkspaceId.ToString("D"),
            9,
            1,
            Guid.NewGuid().ToString("D"),
            ["replica.status"]);
        var reply = new RecordingReplicaReply();
        var host = new SchedulingHost();
        var bootstrap = new CountingBootstrap();
        await using IWorkspaceReplicaStatusController controller =
            new WorkspaceReplicaStatusController(
                reply,
                host,
                fixture.Session,
                fixture.Registry,
                new FixedReplicaQuery(),
                bootstrap);
        controller.Bind(fixture.Session.CurrentSession);

        await controller.RefreshNowAsync(
            workspace.WorkspaceId,
            sessionEpoch: 9,
            CancellationToken.None);

        WorkspaceRegistryEntryV2 updated = fixture.Registry.List().Single();
        Assert.AreEqual(WorkspaceHealth.Healthy, updated.LastKnownHealth);
        Assert.IsFalse(updated.PendingSync);
        Assert.IsNotNull(updated.LastSyncAt);
        Assert.IsTrue(reply.Events >= 1);
        Assert.AreEqual(1, host.Scheduled);
        Assert.AreEqual(1, bootstrap.PostCount);
    }

    private static WorkspaceRegistryEntryV2 Entry() => new()
    {
        ContractVersion = WorkspaceV2Json.ContractVersion,
        WorkspaceId = Guid.NewGuid(),
        DisplayName = "Replica",
        SelectedRoot = Path.GetTempPath(),
        ActivityRoot = Path.Combine(Path.GetTempPath(), "activity"),
        StorageKind = WorkspaceStorageKind.Fixed,
        CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
        LastOpenedAt = null,
        LastKnownHealth = WorkspaceHealth.Unknown,
        LastSnapshotAt = null,
        LastSyncAt = null,
        PendingSync = false,
    };

    private sealed class FixedReplicaQuery : IWorkspaceReplicaStatusQuery
    {
        public Task<WorkspaceReplicaStatusEnvelope> ReadAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            CancellationToken cancellationToken) => Task.FromResult(
                new WorkspaceReplicaStatusEnvelope(
                    new WorkspaceReplicaStatus(
                        WorkspaceCoordinationStrength.Advisory,
                        "replicated",
                        PendingSync: false),
                    JsonSerializer.SerializeToElement(new
                    {
                        workspaceId = workspaceId.ToString("D"),
                        sessionEpoch,
                    })));
    }

    private sealed class RecordingReplicaReply : IWorkspaceProductReplySink
    {
        public int Events { get; private set; }
        public void PostNotification(string type, object? payload) { }
        public void PostWorkspaceV2Response(
            string? requestId,
            object payload,
            JsonElement wire)
        { }
        public void PostWorkspaceV2Event(object payload, JsonElement wire) => Events++;
    }

    private sealed class SchedulingHost : IWorkspaceProductHost
    {
        public bool IsRendererReady => true;
        public bool IsClosing => false;
        public bool HasDocumentWorkspace => false;
        public int Scheduled { get; private set; }

        public void Schedule(Action action)
        {
            Scheduled++;
            action();
        }

        public void OpenProductWorkspaceWhenReady() { }
        public void WriteError(string message) => Assert.Fail(message);
    }

    private sealed class CountingBootstrap : IWorkspaceBootstrapPublisher
    {
        public int PostCount { get; private set; }
        public void Post() => PostCount++;
    }
}
