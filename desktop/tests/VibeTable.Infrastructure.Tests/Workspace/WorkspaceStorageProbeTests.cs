using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Infrastructure.Tests.Workspace;

[TestClass]
public sealed class WorkspaceStorageProbeTests
{
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
}
