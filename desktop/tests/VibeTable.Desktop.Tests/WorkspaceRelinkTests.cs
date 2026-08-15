using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceRelinkTests
{
    [TestMethod]
    public async Task ExactIdentityRelinkPreservesHistoryAndRecoversHealth()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-relink-exact-");
        WorkspaceRegistryEntryV2 original = fixture.AddDirectWorkspace("Original");
        string relocated = Path.Combine(fixture.Root, "Relocated");
        Directory.Move(original.SelectedRoot, relocated);

        await RelinkAsync(fixture, original.WorkspaceId, relocated);

        WorkspaceRegistryEntryV2 updated = fixture.Registry.List().Single();
        Assert.AreEqual(Path.GetFullPath(relocated), updated.SelectedRoot);
        Assert.AreEqual(original.LastOpenedAt, updated.LastOpenedAt);
        Assert.AreEqual(original.LastSnapshotAt, updated.LastSnapshotAt);
        Assert.AreEqual(original.LastSyncAt, updated.LastSyncAt);
        Assert.AreEqual(original.PendingSync, updated.PendingSync);
        Assert.AreEqual(WorkspaceHealth.Healthy, updated.LastKnownHealth);
    }

    [TestMethod]
    public async Task DifferentManifestIdentityCannotReplaceRegisteredWorkspace()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-relink-identity-");
        WorkspaceRegistryEntryV2 original = fixture.AddDirectWorkspace("Original");
        WorkspaceLayoutResult other = WorkspaceLayout.Create(
            Path.Combine(fixture.Root, "Other"),
            "Other",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.None);

        WorkspaceRegistryException error =
            await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
                RelinkAsync(fixture, original.WorkspaceId, other.SelectedRoot));

        Assert.AreEqual("workspace.identity_mismatch", error.Code);
        Assert.AreEqual(
            original.SelectedRoot,
            fixture.Registry.List().Single().SelectedRoot);
    }

    [TestMethod]
    public async Task RelinkCannotBypassStorageTopologyConversionPlan()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-relink-topology-");
        WorkspaceRegistryEntryV2 original = fixture.AddDirectWorkspace("Topology");
        string relocated = Path.Combine(fixture.Root, "MirroredClone");
        Directory.Move(original.SelectedRoot, relocated);
        WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(relocated);
        File.WriteAllText(
            Path.Combine(relocated, ".vibetable", "workspace.json"),
            JsonSerializer.Serialize(
                manifest with { StorageMode = WorkspaceStorageMode.Mirrored },
                WorkspaceV2Json.StrictOptions));

        WorkspaceRegistryException error =
            await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
                RelinkAsync(fixture, original.WorkspaceId, relocated));

        Assert.AreEqual("workspace.storage_topology_mismatch", error.Code);
        Assert.AreEqual(
            original.SelectedRoot,
            fixture.Registry.List().Single().SelectedRoot);
    }

    [TestMethod]
    public async Task ActiveWorkspaceIsRejectedBeforeProviderProbe()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-relink-active-");
        WorkspaceRegistryEntryV2 original = fixture.AddDirectWorkspace("Original");
        fixture.Session.CurrentSession = OpenSession(original.WorkspaceId);
        int probes = fixture.ProbeCount;

        WorkspaceRegistryException error =
            await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
                RelinkAsync(fixture, original.WorkspaceId, original.SelectedRoot));

        Assert.AreEqual("workspace.session_open", error.Code);
        Assert.AreEqual(probes, fixture.ProbeCount);
        Assert.AreEqual(0, fixture.Picker.WorkspacePickCount);
    }

    [TestMethod]
    public void StorageMetricsMeasureDataAndFilesWithoutPlaceholderZeros()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-storage-meter-");
        WorkspaceRegistryEntryV2 workspace = fixture.AddDirectWorkspace("Metrics");
        WorkspacePaths paths = WorkspaceLayout.Paths(workspace.SelectedRoot);
        File.WriteAllBytes(Path.Combine(paths.Data, "business.db"), new byte[7]);
        File.WriteAllBytes(Path.Combine(paths.Files, "document.bin"), new byte[11]);

        IWorkspaceStorageMeter meter = new WorkspaceStorageMeter();
        WorkspaceStorageMeasurement measurement = meter.Measure(workspace);

        Assert.AreEqual(18, measurement.LogicalSize);
        Assert.IsTrue(measurement.PhysicalSize >= measurement.LogicalSize);
    }

    private static async Task RelinkAsync(
        WorkspaceRegistryTopologyTestContext fixture,
        Guid workspaceId,
        string selectedRoot)
    {
        fixture.Picker.SelectedRoot = selectedRoot;
        await fixture.DispatchAsync("workspace.relink", new
        {
            workspaceId = workspaceId.ToString("D"),
            selectedRootGrant = "host-picker://workspace-root",
        });
    }

    private static WorkspaceSessionV2 OpenSession(Guid workspaceId) => new()
    {
        ContractVersion = WorkspaceV2Json.ContractVersion,
        WorkspaceId = workspaceId,
        SessionEpoch = 5,
        State = WorkspaceSessionState.OpenedWritable,
        OpenMode = WorkspaceOpenMode.Writable,
        Writable = true,
        Provisional = false,
        Phase = WorkspaceSessionPhase.Idle,
        ErrorCode = null,
    };
}
