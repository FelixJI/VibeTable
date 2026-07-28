using System.Diagnostics;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class SnapshotPackageBrokerTests
{
    [TestMethod]
    public void ExactObjectValidationRejectsMissingAndAdditionalFields()
    {
        using JsonDocument exact = JsonDocument.Parse(
            """{"pathGrant":"grant","credential":null}""");
        using JsonDocument missing = JsonDocument.Parse(
            """{"pathGrant":"grant"}""");
        using JsonDocument additional = JsonDocument.Parse(
            """{"pathGrant":"grant","credential":null,"path":"secret"}""");

        Assert.IsTrue(SnapshotPackageBroker.HasExactProperties(
            exact.RootElement,
            "pathGrant",
            "credential"));
        Assert.IsFalse(SnapshotPackageBroker.HasExactProperties(
            missing.RootElement,
            "pathGrant",
            "credential"));
        Assert.IsFalse(SnapshotPackageBroker.HasExactProperties(
            additional.RootElement,
            "pathGrant",
            "credential"));
    }

    [TestMethod]
    public void RestartCleanupDeletesOnlyDeadBrokerOwnedUnregisteredRoots()
    {
        string managedRoot = Path.Combine(
            Path.GetTempPath(),
            "vibetable-package-cleanup-" + Guid.NewGuid().ToString("N"));
        try
        {
            Guid orphanId = Guid.NewGuid();
            string orphan = CreateWorkspace(managedRoot, orphanId);
            SnapshotPackageBroker.WriteOwnershipMarker(
                orphan,
                orphanId,
                DateTimeOffset.UtcNow.AddMinutes(10),
                ownerProcessId: int.MaxValue,
                ownerStartedAt: DateTimeOffset.UtcNow);

            Guid registeredId = Guid.NewGuid();
            string registered = CreateWorkspace(managedRoot, registeredId);
            SnapshotPackageBroker.WriteOwnershipMarker(
                registered,
                registeredId,
                DateTimeOffset.UtcNow.AddMinutes(10),
                ownerProcessId: int.MaxValue,
                ownerStartedAt: DateTimeOffset.UtcNow);

            Guid unknownId = Guid.NewGuid();
            string unknown = CreateWorkspace(managedRoot, unknownId);

            SnapshotPackageBroker.CleanupOwnedOrphans(
                managedRoot,
                new HashSet<Guid> { registeredId });

            Assert.IsFalse(Directory.Exists(orphan));
            Assert.IsTrue(Directory.Exists(registered));
            Assert.IsTrue(Directory.Exists(unknown));
        }
        finally
        {
            if (Directory.Exists(managedRoot))
                Directory.Delete(managedRoot, recursive: true);
        }
    }

    [TestMethod]
    public void RestartCleanupUsesCreationJournalForPreMarkerCrash()
    {
        string managedRoot = Path.Combine(
            Path.GetTempPath(),
            "vibetable-package-journal-" + Guid.NewGuid().ToString("N"));
        try
        {
            Directory.CreateDirectory(managedRoot);
            Guid workspaceId = Guid.NewGuid();
            string staging = Path.Combine(
                managedRoot,
                ".creating-" + workspaceId.ToString("D"));
            _ = WorkspaceLayout.Create(
                staging,
                "staged",
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.Convenient,
                workspaceId: workspaceId);
            string journal = Path.Combine(
                managedRoot,
                $".creating-{workspaceId:D}.broker.json");
            File.WriteAllText(
                journal,
                JsonSerializer.Serialize(
                    new
                    {
                        kind = "vibetable.snapshot-package-target.v1",
                        workspaceId,
                        createdAt = DateTimeOffset.UtcNow,
                        expiresAt = DateTimeOffset.UtcNow.AddMinutes(10),
                        ownerProcessId = int.MaxValue,
                        ownerStartedAt = DateTimeOffset.UtcNow,
                    },
                    WorkspaceV2Json.StrictOptions));

            SnapshotPackageBroker.CleanupOwnedOrphans(
                managedRoot,
                new HashSet<Guid>());

            Assert.IsFalse(Directory.Exists(staging));
            Assert.IsFalse(File.Exists(journal));
        }
        finally
        {
            if (Directory.Exists(managedRoot))
                Directory.Delete(managedRoot, recursive: true);
        }
    }

    [TestMethod]
    public void RestartCleanupPreservesExpiredLeaseWhileOriginalOwnerIsAlive()
    {
        string managedRoot = CreateManagedRoot("live-owner");
        try
        {
            DateTimeOffset now = DateTimeOffset.UtcNow;
            DateTimeOffset ownerStartedAt = now.AddHours(-2);
            Guid workspaceId = Guid.NewGuid();
            string root = CreateWorkspace(managedRoot, workspaceId);
            SnapshotPackageBroker.WriteOwnershipMarker(
                root,
                workspaceId,
                now.AddMinutes(1),
                ownerProcessId: 4242,
                ownerStartedAt: ownerStartedAt);

            SnapshotPackageBroker.CleanupOwnedOrphans(
                managedRoot,
                new HashSet<Guid>(),
                now.AddMinutes(2),
                (processId, startedAt) =>
                {
                    Assert.AreEqual(4242, processId);
                    Assert.AreEqual(ownerStartedAt, startedAt);
                    return SnapshotPackageBroker.BrokerOwnerLiveness.Alive;
                });

            Assert.IsTrue(
                Directory.Exists(root),
                "An expired lease must not override proof that its owner is alive.");
        }
        finally
        {
            DeleteManagedRoot(managedRoot);
        }
    }

    [TestMethod]
    public void RestartCleanupUsesLeaseOnlyWhenOwnerLivenessIsUnknown()
    {
        string managedRoot = CreateManagedRoot("unknown-owner");
        try
        {
            DateTimeOffset now = DateTimeOffset.UtcNow;
            Guid leasedId = Guid.NewGuid();
            string leased = CreateWorkspace(managedRoot, leasedId);
            SnapshotPackageBroker.WriteOwnershipMarker(
                leased,
                leasedId,
                now.AddMinutes(10),
                ownerProcessId: 4242,
                ownerStartedAt: now.AddHours(-1));

            Guid expiredId = Guid.NewGuid();
            string expired = CreateWorkspace(managedRoot, expiredId);
            SnapshotPackageBroker.WriteOwnershipMarker(
                expired,
                expiredId,
                now.AddMinutes(1),
                ownerProcessId: 4243,
                ownerStartedAt: now.AddHours(-1));

            SnapshotPackageBroker.CleanupOwnedOrphans(
                managedRoot,
                new HashSet<Guid>(),
                now.AddMinutes(2),
                (_, _) =>
                    SnapshotPackageBroker.BrokerOwnerLiveness.Unknown);

            Assert.IsTrue(Directory.Exists(leased));
            Assert.IsFalse(Directory.Exists(expired));
        }
        finally
        {
            DeleteManagedRoot(managedRoot);
        }
    }

    [TestMethod]
    public void RestartCleanupDeletesReusedOrDeadOwnerEvenBeforeLeaseExpiry()
    {
        string managedRoot = CreateManagedRoot("dead-owner");
        try
        {
            DateTimeOffset now = DateTimeOffset.UtcNow;
            Guid workspaceId = Guid.NewGuid();
            string root = CreateWorkspace(managedRoot, workspaceId);
            SnapshotPackageBroker.WriteOwnershipMarker(
                root,
                workspaceId,
                now.AddHours(1),
                ownerProcessId: 4242,
                ownerStartedAt: now.AddHours(-1));

            SnapshotPackageBroker.CleanupOwnedOrphans(
                managedRoot,
                new HashSet<Guid>(),
                now,
                (_, _) => SnapshotPackageBroker.BrokerOwnerLiveness.Dead);

            Assert.IsFalse(
                Directory.Exists(root),
                "A PID with a different start time is a dead original owner.");
        }
        finally
        {
            DeleteManagedRoot(managedRoot);
        }
    }

    [TestMethod]
    public void ProcessProbeRejectsPidReuseByComparingExactStartTime()
    {
        using Process process = Process.GetCurrentProcess();
        DateTimeOffset startedAt = process.StartTime.ToUniversalTime();

        Assert.AreEqual(
            SnapshotPackageBroker.BrokerOwnerLiveness.Alive,
            SnapshotPackageBroker.ProbeOwnerLiveness(
                process.Id,
                startedAt));
        Assert.AreEqual(
            SnapshotPackageBroker.BrokerOwnerLiveness.Dead,
            SnapshotPackageBroker.ProbeOwnerLiveness(
                process.Id,
                startedAt.AddSeconds(1)));
    }

    [TestMethod]
    public void OwnershipHeartbeatAtomicallyRenewsUnknownOwnerLease()
    {
        string managedRoot = CreateManagedRoot("heartbeat");
        try
        {
            DateTimeOffset now = DateTimeOffset.UtcNow;
            DateTimeOffset ownerStartedAt = now.AddHours(-1);
            Guid workspaceId = Guid.NewGuid();
            string root = CreateWorkspace(managedRoot, workspaceId);
            SnapshotPackageBroker.WriteOwnershipMarker(
                root,
                workspaceId,
                now.AddMinutes(1),
                ownerProcessId: 4242,
                ownerStartedAt: ownerStartedAt);

            Assert.IsTrue(SnapshotPackageBroker.RenewOwnershipMarker(
                root,
                workspaceId,
                now.AddMinutes(2),
                TimeSpan.FromMinutes(10),
                ownerProcessId: 4242,
                ownerStartedAt: ownerStartedAt));

            SnapshotPackageBroker.CleanupOwnedOrphans(
                managedRoot,
                new HashSet<Guid>(),
                now.AddMinutes(11),
                (_, _) => SnapshotPackageBroker.BrokerOwnerLiveness.Unknown);

            Assert.IsTrue(Directory.Exists(root));

            SnapshotPackageBroker.CleanupOwnedOrphans(
                managedRoot,
                new HashSet<Guid>(),
                now.AddMinutes(13),
                (_, _) => SnapshotPackageBroker.BrokerOwnerLiveness.Unknown);

            Assert.IsFalse(Directory.Exists(root));
        }
        finally
        {
            DeleteManagedRoot(managedRoot);
        }
    }

    private static string CreateManagedRoot(string suffix)
        => Path.Combine(
            Path.GetTempPath(),
            $"vibetable-package-{suffix}-" + Guid.NewGuid().ToString("N"));

    private static void DeleteManagedRoot(string managedRoot)
    {
        if (Directory.Exists(managedRoot))
            Directory.Delete(managedRoot, recursive: true);
    }

    private static string CreateWorkspace(
        string managedRoot,
        Guid workspaceId)
    {
        string root = Path.Combine(managedRoot, workspaceId.ToString("D"));
        _ = WorkspaceLayout.Create(
            root,
            workspaceId.ToString("N"),
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.Convenient,
            workspaceId: workspaceId);
        return root;
    }
}
