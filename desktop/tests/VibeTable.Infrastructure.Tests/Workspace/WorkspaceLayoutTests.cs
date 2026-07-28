using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Infrastructure.Tests.Workspace;

[TestClass]
public sealed class WorkspaceLayoutTests
{
    [TestMethod]
    public void CreateActivityRootCopiesMirroredIdentityWithoutChangingReplica()
    {
        using var fixture = new LayoutFixture();
        string selected = Path.Combine(fixture.Root, "replica");
        string firstActivity = Path.Combine(fixture.Root, "first-activity");
        string secondActivity = Path.Combine(fixture.Root, "second-activity");
        WorkspaceLayoutResult created = WorkspaceLayout.Create(
            selected,
            "Mirrored",
            WorkspaceStorageMode.Mirrored,
            WorkspaceEncryptionMode.None,
            firstActivity);
        string original = File.ReadAllText(
            Path.Combine(selected, ".vibetable", "workspace.json"));

        WorkspaceLayoutResult attached =
            WorkspaceLayout.CreateActivityRoot(
                selected,
                secondActivity);

        Assert.AreEqual(
            created.Manifest.WorkspaceId,
            attached.Manifest.WorkspaceId);
        Assert.IsTrue(Directory.Exists(
            Path.Combine(secondActivity, ".vibetable", "data")));
        Assert.AreEqual(
            original,
            File.ReadAllText(
                Path.Combine(selected, ".vibetable", "workspace.json")));
    }

    [TestMethod]
    public void DirectLayoutCreatesCompleteVibetableBoundary()
    {
        using var fixture = new LayoutFixture();
        var root = Path.Combine(fixture.Root, "Direct");

        var result = WorkspaceLayout.Create(
            root,
            "直接工作区",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.Protected);
        var paths = WorkspaceLayout.Paths(root);

        Assert.AreEqual(result.Manifest.WorkspaceId, WorkspaceLayout.ReadManifest(root).WorkspaceId);
        foreach (var directory in new[]
        {
            paths.Files, paths.Data, paths.Topology, paths.Objects, paths.Audit,
            paths.Snapshots, paths.Coordination, paths.Quarantine, paths.Temp,
        })
            Assert.IsTrue(Directory.Exists(directory), directory);
        Assert.IsFalse(Directory.Exists(Path.Combine(root, ".backup")));
    }

    [TestMethod]
    public void MirroredLayoutSeparatesReplicaFromActivityData()
    {
        using var fixture = new LayoutFixture();
        var selected = Path.Combine(fixture.Root, "Replica");
        var activity = Path.Combine(fixture.Root, "Activity");

        var result = WorkspaceLayout.Create(
            selected,
            "镜像工作区",
            WorkspaceStorageMode.Mirrored,
            WorkspaceEncryptionMode.Convenient,
            activity);

        Assert.AreEqual(selected, result.SelectedRoot);
        Assert.AreEqual(activity, result.ActivityRoot);
        Assert.IsFalse(Directory.Exists(WorkspaceLayout.Paths(selected).Data));
        Assert.IsTrue(Directory.Exists(WorkspaceLayout.Paths(activity).Data));
        Assert.AreEqual(
            WorkspaceLayout.ReadManifest(selected).WorkspaceId,
            WorkspaceLayout.ReadManifest(activity).WorkspaceId);
    }

    [TestMethod]
    public void ImportProvenanceIsDurableIdempotentAndImmutable()
    {
        using var fixture = new LayoutFixture();
        string root = Path.Combine(fixture.Root, "Imported");
        WorkspaceLayoutResult created = WorkspaceLayout.Create(
            root,
            "Imported",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.Convenient);
        Guid sourceWorkspaceId = Guid.NewGuid();
        Guid sourceSnapshotId = Guid.NewGuid();

        WorkspaceManifestV2 first = WorkspaceLayout.SetImportProvenance(
            root,
            sourceWorkspaceId,
            sourceSnapshotId);
        WorkspaceManifestV2 retried = WorkspaceLayout.SetImportProvenance(
            root,
            sourceWorkspaceId,
            sourceSnapshotId);

        Assert.AreNotEqual(
            sourceWorkspaceId,
            created.Manifest.WorkspaceId);
        Assert.AreEqual(
            sourceWorkspaceId,
            first.ImportedFromWorkspaceId);
        Assert.AreEqual(sourceSnapshotId, first.SourceSnapshotId);
        Assert.AreEqual(first, retried);
        Assert.AreEqual(first, WorkspaceLayout.ReadManifest(root));

        WorkspaceRegistryException conflict =
            Assert.ThrowsExactly<WorkspaceRegistryException>(
                () => WorkspaceLayout.SetImportProvenance(
                    root,
                    Guid.NewGuid(),
                    sourceSnapshotId));
        Assert.AreEqual(
            "workspace.import_provenance_conflict",
            conflict.Code);
        Assert.AreEqual(first, WorkspaceLayout.ReadManifest(root));
    }

    [TestMethod]
    public void ImportProvenanceRejectsReusingSourceWorkspaceIdentity()
    {
        using var fixture = new LayoutFixture();
        string root = Path.Combine(fixture.Root, "Imported");
        WorkspaceLayoutResult created = WorkspaceLayout.Create(
            root,
            "Imported",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.Convenient);

        WorkspaceRegistryException error =
            Assert.ThrowsExactly<WorkspaceRegistryException>(
                () => WorkspaceLayout.SetImportProvenance(
                    root,
                    created.Manifest.WorkspaceId,
                    Guid.NewGuid()));

        Assert.AreEqual("workspace.import_identity_conflict", error.Code);
        Assert.IsNull(
            WorkspaceLayout.ReadManifest(root).ImportedFromWorkspaceId);
    }

    [TestMethod]
    public void LegacyBackupLayoutIsExplicitlyRejected()
    {
        using var fixture = new LayoutFixture();
        var root = Path.Combine(fixture.Root, "Legacy");
        Directory.CreateDirectory(Path.Combine(root, ".backup"));

        var error = Assert.ThrowsExactly<WorkspaceRegistryException>(
            () => WorkspaceLayout.ReadManifest(root));

        Assert.AreEqual("workspace.legacy_layout_unsupported", error.Code);
    }

    [TestMethod]
    public void NonEmptyCreateTargetIsNeverAdopted()
    {
        using var fixture = new LayoutFixture();
        var root = Path.Combine(fixture.Root, "Existing");
        Directory.CreateDirectory(root);
        File.WriteAllText(Path.Combine(root, "user.txt"), "keep");

        var error = Assert.ThrowsExactly<WorkspaceRegistryException>(
            () => WorkspaceLayout.Create(
                root,
                "Existing",
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.None));

        Assert.AreEqual("workspace.create_target_not_empty", error.Code);
        Assert.AreEqual("keep", File.ReadAllText(Path.Combine(root, "user.txt")));
    }

    private sealed class LayoutFixture : IDisposable
    {
        public LayoutFixture()
        {
            Root = Path.Combine(Path.GetTempPath(), "vibetable-layout-" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(Root);
        }

        public string Root { get; }

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
