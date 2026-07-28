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
}
