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
