using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductionWorkspaceHooksTests
{
    [TestMethod]
    public async Task StrongWriterLeaseConflictsAcrossHookInstancesUntilRelease()
    {
        string root = CreateRoot();
        try
        {
            WorkspaceRegistryEntryV2 workspace = Entry(
                root,
                WorkspaceCoordinationStrength.Strong);
            using var first = new WorkspaceCoordinationLeaseHook();
            using var second = new WorkspaceCoordinationLeaseHook();

            WorkspaceOpenMode granted = await first.AcquireAsync(
                workspace,
                WorkspaceOpenMode.Writable,
                CancellationToken.None);
            WorkspaceRegistryException conflict =
                await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(
                    () => second.AcquireAsync(
                        workspace,
                        WorkspaceOpenMode.Writable,
                        CancellationToken.None));

            Assert.AreEqual(WorkspaceOpenMode.Writable, granted);
            Assert.AreEqual("workspace.lease_conflict", conflict.Code);
            await first.ReleaseAsync(
                workspace.WorkspaceId,
                sessionEpoch: 1,
                CancellationToken.None);
            Assert.AreEqual(
                WorkspaceOpenMode.Writable,
                await second.AcquireAsync(
                    workspace,
                    WorkspaceOpenMode.Writable,
                    CancellationToken.None));
        }
        finally
        {
            TryDelete(root);
        }
    }

    [TestMethod]
    public async Task AdvisoryWriterIsProvisionalAndDoesNotClaimExclusiveLock()
    {
        string root = CreateRoot();
        try
        {
            WorkspaceRegistryEntryV2 workspace = Entry(
                root,
                WorkspaceCoordinationStrength.Advisory);
            using var first = new WorkspaceCoordinationLeaseHook();
            using var second = new WorkspaceCoordinationLeaseHook();

            Assert.AreEqual(
                WorkspaceOpenMode.Provisional,
                await first.AcquireAsync(
                    workspace,
                    WorkspaceOpenMode.Writable,
                    CancellationToken.None));
            Assert.AreEqual(
                WorkspaceOpenMode.Provisional,
                await second.AcquireAsync(
                    workspace,
                    WorkspaceOpenMode.Writable,
                    CancellationToken.None));
        }
        finally
        {
            TryDelete(root);
        }
    }

    [TestMethod]
    public async Task MirroredProvisionalWriterClaimsLocalActivityFence()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-production-hooks-mirrored-" +
            Guid.NewGuid().ToString("N"));
        string selected = Path.Combine(root, "replica");
        string activity = Path.Combine(root, "activity");
        try
        {
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                selected,
                "Mirrored hooks",
                WorkspaceStorageMode.Mirrored,
                WorkspaceEncryptionMode.None,
                activity);
            var workspace = new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = layout.Manifest.DisplayName,
                SelectedRoot = selected,
                ActivityRoot = activity,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength =
                    WorkspaceCoordinationStrength.Advisory,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = null,
                LastSyncAt = null,
                PendingSync = false,
            };
            using var first = new WorkspaceCoordinationLeaseHook();
            using var second = new WorkspaceCoordinationLeaseHook();

            Assert.AreEqual(
                WorkspaceOpenMode.Provisional,
                await first.AcquireAsync(
                    workspace,
                    WorkspaceOpenMode.Writable,
                    CancellationToken.None));
            WorkspaceRegistryException writerConflict =
                await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(
                    () => second.AcquireAsync(
                        workspace,
                        WorkspaceOpenMode.Writable,
                        CancellationToken.None));
            Assert.AreEqual("workspace.lease_conflict", writerConflict.Code);

            await using (WorkspaceStorageMaintenanceLease maintenance =
                WorkspaceStorageMaintenanceLease.Acquire(
                    activity,
                    workspace.WorkspaceId))
            {
                WorkspaceRegistryException maintenanceConflict =
                    await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(
                        () => maintenance.AcquireWriterFenceAsync(
                            activity,
                            CancellationToken.None));
                Assert.AreEqual(
                    "workspace.storage_writer_fence_conflict",
                    maintenanceConflict.Code);

                await first.ReleaseAsync(
                    workspace.WorkspaceId,
                    sessionEpoch: 1,
                    CancellationToken.None);
                await maintenance.AcquireWriterFenceAsync(
                    activity,
                    CancellationToken.None);
                WorkspaceRegistryException maintenanceIntentConflict =
                    await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(
                        () => second.AcquireAsync(
                            workspace,
                            WorkspaceOpenMode.Writable,
                            CancellationToken.None));
                Assert.AreEqual(
                    "workspace.storage_maintenance_conflict",
                    maintenanceIntentConflict.Code);
            }

            Assert.AreEqual(
                WorkspaceOpenMode.Provisional,
                await second.AcquireAsync(
                    workspace,
                    WorkspaceOpenMode.Writable,
                    CancellationToken.None));
        }
        finally
        {
            TryDelete(root);
        }
    }

    [TestMethod]
    public void ProtectionRequiresSynchronousReadyResult()
    {
        Guid operationId = Guid.NewGuid();
        JsonElement ready = JsonSerializer.SerializeToElement(new
        {
            operationId = operationId.ToString("D"),
            state = "ready",
            snapshotId = Guid.NewGuid().ToString("D"),
            mutationRevision = 7,
        });
        JsonElement queued = JsonSerializer.SerializeToElement(new
        {
            operationId = operationId.ToString("D"),
            state = "queued",
            snapshotId = Guid.NewGuid().ToString("D"),
            mutationRevision = 7,
        });

        ProtectionSnapshotReceipt receipt =
            SidecarWorkspaceProtectionHook.EnsureProtectionCompleted(
            ready,
            operationId);
        Assert.AreEqual(7UL, receipt.MutationRevision);
        JsonElement emptyBaseline = JsonSerializer.SerializeToElement(new
        {
            operationId = operationId.ToString("D"),
            state = "ready",
            snapshotId = Guid.NewGuid().ToString("D"),
            mutationRevision = 0,
        });
        Assert.AreEqual(
            0UL,
            SidecarWorkspaceProtectionHook.EnsureProtectionCompleted(
                emptyBaseline,
                operationId).MutationRevision);
        WorkspaceRegistryException error =
            Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                SidecarWorkspaceProtectionHook.EnsureProtectionCompleted(
                    queued,
                    operationId));
        Assert.AreEqual("workspace.protection_response_invalid", error.Code);
    }

    private static string CreateRoot()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-production-hooks-" + Guid.NewGuid().ToString("N"));
        WorkspaceLayout.Create(
            root,
            "Hooks",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.None);
        return root;
    }

    private static WorkspaceRegistryEntryV2 Entry(
        string root,
        WorkspaceCoordinationStrength coordinationStrength)
    {
        WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(root);
        return new WorkspaceRegistryEntryV2
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = manifest.WorkspaceId,
            DisplayName = manifest.DisplayName,
            SelectedRoot = root,
            ActivityRoot = null,
            StorageKind = WorkspaceStorageKind.Fixed,
            CoordinationStrength = coordinationStrength,
            LastOpenedAt = null,
            LastKnownHealth = WorkspaceHealth.Healthy,
            LastSnapshotAt = null,
            LastSyncAt = null,
            PendingSync = false,
        };
    }

    private static void TryDelete(string root)
    {
        try
        {
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
        catch
        {
            // Best effort.
        }
    }
}
