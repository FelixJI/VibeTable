using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Infrastructure.Tests.Workspace;

[TestClass]
public sealed class WorkspaceStorageProbeTests
{
    [TestMethod]
    public void RemoteProtocolClassificationAcceptsOnlyTheWindowsSmbProtocol()
    {
        Assert.AreEqual(
            WorkspaceRemoteProtocol.Smb,
            WindowsRemoteProtocolProbe.Classify(0x00020000));
        Assert.AreEqual(
            WorkspaceRemoteProtocol.Other,
            WindowsRemoteProtocolProbe.Classify(0x002B0000));
    }

    [TestMethod]
    public void SystemCloudFilesProbeClassifiesNestedPathWithoutCallerRoots()
    {
        var root = Path.Combine(
            Path.GetTempPath(),
            "VibeTable-StorageProbeTests",
            Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            var result = new WorkspaceStorageProbe(
                path => path == Path.GetFullPath(root)).Probe(root);

            Assert.AreEqual(
                WorkspaceStorageKind.RegisteredCloud,
                result.StorageKind);
            Assert.AreEqual(
                WorkspaceCoordinationStrength.Advisory,
                result.CoordinationStrength);
            Assert.IsNull(result.RegisteredCloudRoot);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public void NestedRegisteredCloudRootIsClassifiedAsAdvisory()
    {
        var root = Path.Combine(
            Path.GetTempPath(),
            "VibeTable-StorageProbeTests",
            Guid.NewGuid().ToString("N"));
        var cloud = Path.Combine(root, "Cloud");
        var workspace = Path.Combine(cloud, "nested", "workspace");
        Directory.CreateDirectory(workspace);
        try
        {
            var result = new WorkspaceStorageProbe().Probe(
                workspace,
                registeredCloudRoots: [cloud]);

            Assert.AreEqual(WorkspaceStorageKind.RegisteredCloud, result.StorageKind);
            Assert.AreEqual(WorkspaceCoordinationStrength.Advisory, result.CoordinationStrength);
            Assert.AreEqual(Path.GetFullPath(cloud), result.RegisteredCloudRoot);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public void ProbeReturnsOnlyAfterItsArtifactsAreInvisibleToWorkspaceCreation()
    {
        string parent = Path.Combine(
            Path.GetTempPath(),
            "VibeTable-StorageProbeTests",
            Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(parent);
        try
        {
            for (int index = 0; index < 32; index++)
            {
                string root = Path.Combine(parent, $"workspace-{index:D2}");
                Directory.CreateDirectory(root);

                _ = new WorkspaceStorageProbe().Probe(root);

                Assert.IsFalse(Directory.EnumerateFileSystemEntries(root).Any());
                WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                    root,
                    $"Probe {index:D2}",
                    WorkspaceStorageMode.Direct,
                    WorkspaceEncryptionMode.Convenient);
                Assert.AreEqual(Path.GetFullPath(root), layout.SelectedRoot);
                Directory.Delete(root, recursive: true);
            }
        }
        finally
        {
            if (Directory.Exists(parent))
                Directory.Delete(parent, recursive: true);
        }
    }
}
