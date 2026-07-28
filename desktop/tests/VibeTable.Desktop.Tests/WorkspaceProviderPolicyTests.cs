using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceProviderPolicyTests
{
    [TestMethod]
    public void ProbeFailureIsNotConvertedIntoDriveTypeApproval()
    {
        var expected = new WorkspaceRegistryException(
            "workspace.storage_probe_failed",
            "injected");
        WorkspaceProviderPolicy policy = WorkspaceProviderPolicy.CreateForTests(
            new Dictionary<WorkspaceStorageKind, bool>
            {
                [WorkspaceStorageKind.Fixed] = true,
            },
            (_, _, _) => throw expected);

        WorkspaceRegistryException actual =
            Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                policy.ProbeAndEnsureSupported(Path.GetTempPath()));

        Assert.AreSame(expected, actual);
    }

    [TestMethod]
    public void NonFixedProviderIsBlockedByCurrentEvidencePolicy()
    {
        WorkspaceProviderPolicy policy = WorkspaceProviderPolicy.CreateForTests(
            new Dictionary<WorkspaceStorageKind, bool>
            {
                [WorkspaceStorageKind.Fixed] = true,
            },
            (root, _, _) => new WorkspaceStorageObservation(
                WorkspaceStorageKind.Network,
                WorkspaceCoordinationStrength.Advisory,
                1024,
                false,
                DateTimeOffset.UtcNow));

        WorkspaceRegistryException error =
            Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                policy.ProbeAndEnsureSupported(Path.GetTempPath()));

        Assert.AreEqual("workspace.provider_blocked", error.Code);
    }
}
