using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceRelinkTests
{
    [TestMethod]
    public void ExactIdentityRelinkPreservesHistoryAndRecoversHealth()
    {
        using var fixture = new RelinkFixture();
        WorkspaceRegistryEntryV2 original = fixture.AddWorkspace("Original");
        string relocated = Path.Combine(fixture.Root, "Relocated");
        Directory.Move(original.SelectedRoot, relocated);

        WorkspaceRegistryEntryV2 updated = MainWindow.RelinkWorkspaceEntry(
            fixture.Registry,
            fixture.Policy,
            fixture.Root,
            activeWorkspaceId: null,
            original.WorkspaceId,
            relocated);

        Assert.AreEqual(Path.GetFullPath(relocated), updated.SelectedRoot);
        Assert.AreEqual(original.LastOpenedAt, updated.LastOpenedAt);
        Assert.AreEqual(original.LastSnapshotAt, updated.LastSnapshotAt);
        Assert.AreEqual(original.LastSyncAt, updated.LastSyncAt);
        Assert.AreEqual(original.PendingSync, updated.PendingSync);
        Assert.AreEqual(WorkspaceHealth.Healthy, updated.LastKnownHealth);
    }

    [TestMethod]
    public void DifferentManifestIdentityCannotReplaceRegisteredWorkspace()
    {
        using var fixture = new RelinkFixture();
        WorkspaceRegistryEntryV2 original = fixture.AddWorkspace("Original");
        WorkspaceLayoutResult other = WorkspaceLayout.Create(
            Path.Combine(fixture.Root, "Other"),
            "Other",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.None);

        WorkspaceRegistryException error =
            Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                MainWindow.RelinkWorkspaceEntry(
                    fixture.Registry,
                    fixture.Policy,
                    fixture.Root,
                    activeWorkspaceId: null,
                    original.WorkspaceId,
                    other.SelectedRoot));

        Assert.AreEqual("workspace.identity_mismatch", error.Code);
        Assert.AreEqual(
            original.SelectedRoot,
            fixture.Registry.List().Single().SelectedRoot);
    }

    [TestMethod]
    public void RelinkCannotBypassStorageTopologyConversionPlan()
    {
        using var fixture = new RelinkFixture();
        WorkspaceRegistryEntryV2 original = fixture.AddWorkspace("Topology");
        string relocated = Path.Combine(fixture.Root, "MirroredClone");
        Directory.Move(original.SelectedRoot, relocated);
        WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(relocated);
        File.WriteAllText(
            Path.Combine(relocated, ".vibetable", "workspace.json"),
            JsonSerializer.Serialize(
                manifest with { StorageMode = WorkspaceStorageMode.Mirrored },
                WorkspaceV2Json.StrictOptions));

        WorkspaceRegistryException error =
            Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                MainWindow.RelinkWorkspaceEntry(
                    fixture.Registry,
                    fixture.Policy,
                    fixture.Root,
                    activeWorkspaceId: null,
                    original.WorkspaceId,
                    relocated));

        Assert.AreEqual("workspace.storage_topology_mismatch", error.Code);
        Assert.AreEqual(
            original.SelectedRoot,
            fixture.Registry.List().Single().SelectedRoot);
    }

    [TestMethod]
    public void ActiveWorkspaceIsRejectedBeforeProviderProbe()
    {
        using var fixture = new RelinkFixture();
        WorkspaceRegistryEntryV2 original = fixture.AddWorkspace("Original");
        int probes = fixture.ProbeCount;

        WorkspaceRegistryException error =
            Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                MainWindow.RelinkWorkspaceEntry(
                    fixture.Registry,
                    fixture.Policy,
                    fixture.Root,
                    original.WorkspaceId,
                    original.WorkspaceId,
                    original.SelectedRoot));

        Assert.AreEqual("workspace.session_open", error.Code);
        Assert.AreEqual(probes, fixture.ProbeCount);
    }

    [TestMethod]
    public void StorageMetricsMeasureDataAndFilesWithoutPlaceholderZeros()
    {
        using var fixture = new RelinkFixture();
        WorkspaceRegistryEntryV2 workspace = fixture.AddWorkspace("Metrics");
        WorkspacePaths paths = WorkspaceLayout.Paths(workspace.SelectedRoot);
        File.WriteAllBytes(
            Path.Combine(paths.Data, "business.db"),
            new byte[7]);
        File.WriteAllBytes(
            Path.Combine(paths.Files, "document.bin"),
            new byte[11]);

        (long logical, long physical) =
            MainWindow.MeasureWorkspaceStorage(workspace);

        Assert.AreEqual(18, logical);
        Assert.IsTrue(physical >= logical);
    }

    private sealed class RelinkFixture : IDisposable
    {
        public RelinkFixture()
        {
            Root = Path.Combine(
                Path.GetTempPath(),
                "vibetable-relink-" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(Root);
            Registry = new WorkspaceRegistry(Root);
            Policy = WorkspaceProviderPolicy.CreateForTests(
                new Dictionary<WorkspaceStorageKind, bool>
                {
                    [WorkspaceStorageKind.Fixed] = true,
                },
                (root, _, _) =>
                {
                    ProbeCount++;
                    return new WorkspaceStorageObservation(
                        WorkspaceStorageKind.Fixed,
                        WorkspaceCoordinationStrength.Strong,
                        1024,
                        false,
                        DateTimeOffset.UtcNow);
                });
        }

        public string Root { get; }
        public WorkspaceRegistry Registry { get; }
        public WorkspaceProviderPolicy Policy { get; }
        public int ProbeCount { get; private set; }

        public WorkspaceRegistryEntryV2 AddWorkspace(string name)
        {
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                Path.Combine(Root, name),
                name,
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.None);
            DateTimeOffset now = DateTimeOffset.UtcNow;
            return Registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = name,
                SelectedRoot = layout.SelectedRoot,
                ActivityRoot = null,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = now.AddMinutes(-3),
                LastKnownHealth = WorkspaceHealth.Offline,
                LastSnapshotAt = now.AddMinutes(-2),
                LastSyncAt = now.AddMinutes(-1),
                PendingSync = true,
            });
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
                // Best effort.
            }
        }
    }
}
