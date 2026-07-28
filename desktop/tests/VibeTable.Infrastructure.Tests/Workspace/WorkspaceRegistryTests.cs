using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Infrastructure.Tests.Workspace;

[TestClass]
public sealed class WorkspaceRegistryTests
{
    [TestMethod]
    public void Register_AllowsDuplicateNamesButKeepsUuidIdentity()
    {
        using var fixture = new RegistryFixture();
        var first = fixture.Create("同名", "A");
        var second = fixture.Create("同名", "B");

        var entries = fixture.Registry.List();

        Assert.AreEqual(2, entries.Count);
        Assert.AreNotEqual(first.WorkspaceId, second.WorkspaceId);
        Assert.IsTrue(entries.All(entry => entry.DisplayName == "同名"));
    }

    [TestMethod]
    public void Registry_RetainsOfflinePathAndHealth()
    {
        using var fixture = new RegistryFixture();
        var entry = fixture.Create("离线工作区", "Offline");
        Directory.Delete(entry.SelectedRoot, recursive: true);

        var updated = fixture.Registry.UpdateHealth(
            entry.WorkspaceId,
            new WorkspaceHealthObservation(WorkspaceHealth.Offline, PendingSync: true));

        Assert.AreEqual(WorkspaceHealth.Offline, updated.LastKnownHealth);
        Assert.IsTrue(updated.PendingSync);
        Assert.AreEqual(entry.SelectedRoot, fixture.Registry.List().Single().SelectedRoot);
    }

    [TestMethod]
    public void Registry_RejectsManifestUuidMismatch()
    {
        using var fixture = new RegistryFixture();
        var first = fixture.Create("A", "A");
        var conflicting = first with
        {
            WorkspaceId = Guid.NewGuid(),
            DisplayName = "B",
        };
        var error = Assert.ThrowsExactly<WorkspaceRegistryException>(
            () => fixture.Registry.Register(conflicting));
        Assert.AreEqual("workspace.identity_mismatch", error.Code);
    }

    [TestMethod]
    public void Registry_CorruptOrUnknownFormatFailsClosed()
    {
        using var fixture = new RegistryFixture();
        fixture.Create("A", "A");
        var path = Path.Combine(fixture.Root, "VibeTable", "workspace-registry-v2.json");
        File.WriteAllText(path, """{"formatVersion":99,"workspaces":[]}""");

        var error = Assert.ThrowsExactly<WorkspaceRegistryException>(
            fixture.Registry.List);
        Assert.AreEqual("workspace.registry_version_unsupported", error.Code);
    }

    [TestMethod]
    public void DeletePlanRequiresIdentityAndExactConfirmation()
    {
        using var fixture = new RegistryFixture();
        var entry = fixture.Create("删除我", "Delete");
        var plan = fixture.Registry.PlanPermanentDelete(entry.WorkspaceId);

        var confirmation = Assert.ThrowsExactly<WorkspaceRegistryException>(
            () => fixture.Registry.ApplyPermanentDelete(plan, "删除"));
        Assert.AreEqual("workspace.delete_confirmation_invalid", confirmation.Code);
        Assert.IsTrue(Directory.Exists(entry.SelectedRoot));

        fixture.Registry.ApplyPermanentDelete(plan, "删除我");

        Assert.IsFalse(Directory.Exists(entry.SelectedRoot));
        Assert.AreEqual(0, fixture.Registry.List().Count);
    }

    private sealed class RegistryFixture : IDisposable
    {
        public RegistryFixture()
        {
            Root = Path.Combine(Path.GetTempPath(), "vibetable-registry-" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(Root);
            Registry = new WorkspaceRegistry(Root);
        }

        public string Root { get; }
        public WorkspaceRegistry Registry { get; }

        public WorkspaceRegistryEntryV2 Create(string displayName, string folder)
        {
            var selected = Path.Combine(Root, folder);
            var layout = WorkspaceLayout.Create(
                selected,
                displayName,
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.Convenient);
            var entry = new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = displayName,
                SelectedRoot = selected,
                ActivityRoot = null,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = null,
                LastSyncAt = null,
                PendingSync = false,
            };
            return Registry.Register(entry);
        }

        public void Dispose()
        {
            try
            {
                if (Directory.Exists(Root))
                    Directory.Delete(Root, recursive: true);
            }
            catch
            {
                // Best effort test cleanup.
            }
        }
    }
}
