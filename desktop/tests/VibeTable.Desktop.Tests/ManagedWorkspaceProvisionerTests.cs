using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Storage;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ManagedWorkspaceProvisionerTests
{
    [TestMethod]
    public void EnsurePreferred_CreatesVisibleWorkspaceAndReusesIt()
    {
        using var fixture = new Fixture();
        var first = fixture.Provisioner.EnsurePreferred();
        var second = fixture.Provisioner.EnsurePreferred();

        Assert.AreEqual(first.WorkspaceId, second.WorkspaceId);
        Assert.IsTrue(Directory.Exists(first.Root));
        Assert.IsTrue(File.Exists(Path.Combine(first.Root, ".backup", "workspace.json")));
        Assert.AreEqual(first.Root, fixture.Mounts.ResolveRoot(first.WorkspaceId));
    }

    [TestMethod]
    public void EnsurePreferred_DoesNotAdoptNonEmptyUnmanagedFolder()
    {
        using var fixture = new Fixture();
        string occupied = Path.Combine(fixture.DocumentsRoot, "VibeTable 工作区");
        Directory.CreateDirectory(occupied);
        File.WriteAllText(Path.Combine(occupied, "keep.txt"), "user data");

        var selected = fixture.Provisioner.EnsurePreferred();

        Assert.AreNotEqual(occupied, selected.Root);
        Assert.AreEqual("user data", File.ReadAllText(Path.Combine(occupied, "keep.txt")));
    }

    [TestMethod]
    public void EnsurePreferred_UsesMostRecentValidMount()
    {
        using var fixture = new Fixture();
        string root = Path.Combine(fixture.BaseRoot, "mounted");
        Directory.CreateDirectory(Path.Combine(root, ".backup"));
        var manifest = new WorkspaceManifest(1, "workspace-existing", "Existing", "2026-07-20T00:00:00Z");
        new AtomicJsonStore().Write(Path.Combine(root, ".backup", "workspace.json"), manifest);
        fixture.Mounts.Mount(
            manifest.WorkspaceId,
            root,
            manifest.Name,
            fixture.PartitionKey);

        var selected = fixture.Provisioner.EnsurePreferred();

        Assert.AreEqual(manifest.WorkspaceId, selected.WorkspaceId);
        Assert.AreEqual(root, selected.Root);
    }

    [TestMethod]
    public void EnsurePreferred_DoesNotReuseAnotherServerOrLegacyUnpartitionedMount()
    {
        using var fixture = new Fixture();
        string legacyRoot = Path.Combine(fixture.BaseRoot, "legacy-mounted");
        Directory.CreateDirectory(Path.Combine(legacyRoot, ".backup"));
        var legacy = new WorkspaceManifest(
            1,
            "workspace-legacy",
            "Legacy",
            "2026-07-20T00:00:00Z");
        new AtomicJsonStore().Write(
            Path.Combine(legacyRoot, ".backup", "workspace.json"),
            legacy);
        fixture.Mounts.Mount(legacy.WorkspaceId, legacyRoot, legacy.Name);

        var local = fixture.Provisioner.EnsurePreferred();
        var remote = new ManagedWorkspaceProvisioner(
            fixture.Mounts,
            "remote:https://example.test|owner@example.com",
            fixture.DocumentsRoot).EnsurePreferred();

        Assert.AreNotEqual(legacy.WorkspaceId, local.WorkspaceId);
        Assert.AreNotEqual(local.WorkspaceId, remote.WorkspaceId);
        Assert.AreNotEqual(local.Root, remote.Root);
        Assert.AreEqual(
            fixture.PartitionKey,
            fixture.Mounts.ReadAll().Single(
                entry => entry.WorkspaceId == local.WorkspaceId).PartitionKey);
    }

    private sealed class Fixture : IDisposable
    {
        public Fixture()
        {
            BaseRoot = Path.Combine(Path.GetTempPath(), "vibetable-managed-workspace-" + Guid.NewGuid().ToString("N"));
            DocumentsRoot = Path.Combine(BaseRoot, "documents");
            Directory.CreateDirectory(DocumentsRoot);
            Mounts = new WorkspaceMountStore(Path.Combine(BaseRoot, "local"));
            Provisioner = new ManagedWorkspaceProvisioner(
                Mounts,
                PartitionKey,
                DocumentsRoot);
        }

        public string BaseRoot { get; }
        public string DocumentsRoot { get; }
        public string PartitionKey { get; } = "local:default|owner@example.com";
        public WorkspaceMountStore Mounts { get; }
        public ManagedWorkspaceProvisioner Provisioner { get; }

        public void Dispose()
        {
            if (Directory.Exists(BaseRoot)) Directory.Delete(BaseRoot, recursive: true);
        }
    }
}
