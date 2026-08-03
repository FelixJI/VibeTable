using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceProviderPolicyTests
{
    [TestMethod]
    public void LoadFindsPackagedPolicyUnderResources()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-provider-policy-tests",
            Guid.NewGuid().ToString("N"));
        string policyPath = Path.Combine(
            root,
            "resources",
            "contracts",
            "v2",
            "provider-support.json");
        Directory.CreateDirectory(Path.GetDirectoryName(policyPath)!);
        File.WriteAllText(
            policyPath,
            """
            {
              "contractVersion": "2.0",
              "providers": {
                "fixed": {"creation": "enabled", "coordinationStrength": "strong"},
                "network": {"creation": "enabled", "coordinationStrength": "advisory", "protocol": "smb"},
                "registeredCloud": {"creation": "blockedPendingLab", "coordinationStrength": "advisory"},
                "userMarkedSync": {"creation": "blockedPendingLab", "coordinationStrength": "advisory"},
                "removable": {"creation": "blockedPendingLab", "coordinationStrength": "advisory"}
              }
            }
            """);
        try
        {
            WorkspaceProviderPolicy policy = WorkspaceProviderPolicy.Load(root);
            Assert.IsTrue(policy.MirroredCreationEnabled);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public void LoadRejectsNetworkPolicyWithoutExplicitSmbProtocol()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-provider-policy-tests",
            Guid.NewGuid().ToString("N"));
        string policyPath = Path.Combine(
            root,
            "contracts",
            "v2",
            "provider-support.json");
        Directory.CreateDirectory(Path.GetDirectoryName(policyPath)!);
        File.WriteAllText(
            policyPath,
            """
            {
              "contractVersion": "2.0",
              "providers": {
                "fixed": {"creation": "enabled", "coordinationStrength": "strong"},
                "network": {"creation": "disabled", "coordinationStrength": "advisory"},
                "registeredCloud": {"creation": "disabled", "coordinationStrength": "advisory"},
                "userMarkedSync": {"creation": "disabled", "coordinationStrength": "advisory"},
                "removable": {"creation": "disabled", "coordinationStrength": "advisory"}
              }
            }
            """);
        try
        {
            WorkspaceRegistryException error =
                Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                    WorkspaceProviderPolicy.Load(root));

            Assert.AreEqual("workspace.provider_policy_invalid", error.Code);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

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
                DateTimeOffset.UtcNow,
                RemoteProtocol: WorkspaceRemoteProtocol.Smb));

        WorkspaceRegistryException error =
            Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                policy.ProbeAndEnsureSupported(Path.GetTempPath()));

        Assert.AreEqual("workspace.provider_blocked", error.Code);
    }

    [TestMethod]
    public void EnabledNetworkProviderAcceptsSmbOnlyInMirroredMode()
    {
        WorkspaceProviderPolicy policy = WorkspaceProviderPolicy.CreateForTests(
            new Dictionary<WorkspaceStorageKind, bool>
            {
                [WorkspaceStorageKind.Network] = true,
            },
            (root, _, _) => new WorkspaceStorageObservation(
                WorkspaceStorageKind.Network,
                WorkspaceCoordinationStrength.Advisory,
                1024,
                false,
                DateTimeOffset.UtcNow,
                RemoteProtocol: WorkspaceRemoteProtocol.Smb));

        WorkspaceStorageObservation result = policy.ProbeAndEnsureSupported(
            Path.GetTempPath(),
            WorkspaceStorageMode.Mirrored);

        Assert.AreEqual(WorkspaceRemoteProtocol.Smb, result.RemoteProtocol);
        Assert.IsTrue(policy.MirroredCreationEnabled);
    }

    [TestMethod]
    public void BlockedNetworkProviderDoesNotAdvertiseMirroredCreation()
    {
        WorkspaceProviderPolicy policy = WorkspaceProviderPolicy.CreateForTests(
            new Dictionary<WorkspaceStorageKind, bool>
            {
                [WorkspaceStorageKind.Fixed] = true,
            },
            (root, _, _) => new WorkspaceStorageObservation(
                WorkspaceStorageKind.Fixed,
                WorkspaceCoordinationStrength.Strong,
                1024,
                false,
                DateTimeOffset.UtcNow));

        Assert.IsFalse(policy.MirroredCreationEnabled);
    }

    [TestMethod]
    public void EnabledNetworkProviderRejectsNonSmbProtocol()
    {
        WorkspaceProviderPolicy policy = WorkspaceProviderPolicy.CreateForTests(
            new Dictionary<WorkspaceStorageKind, bool>
            {
                [WorkspaceStorageKind.Network] = true,
            },
            (root, _, _) => new WorkspaceStorageObservation(
                WorkspaceStorageKind.Network,
                WorkspaceCoordinationStrength.Advisory,
                1024,
                false,
                DateTimeOffset.UtcNow,
                RemoteProtocol: WorkspaceRemoteProtocol.Other));

        WorkspaceRegistryException error =
            Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                policy.ProbeAndEnsureSupported(
                    Path.GetTempPath(),
                    WorkspaceStorageMode.Mirrored));

        Assert.AreEqual("workspace.network_protocol_unsupported", error.Code);
    }

    [TestMethod]
    public void DirectNetworkTargetRequiresMirroredBeforeProviderEvidenceCheck()
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
                policy.ProbeCreateTargetAndEnsureSupported(
                    Path.GetTempPath(),
                    WorkspaceStorageMode.Direct));

        Assert.AreEqual("workspace.storage_requires_mirrored", error.Code);
        Assert.AreEqual(
            "This non-fixed location requires mirrored storage mode.",
            error.Message);
    }

    [TestMethod]
    public void CreateTargetForwardsExplicitSyncClassification()
    {
        bool observed = false;
        WorkspaceProviderPolicy policy = WorkspaceProviderPolicy.CreateForTests(
            new Dictionary<WorkspaceStorageKind, bool>
            {
                [WorkspaceStorageKind.UserMarkedSync] = true,
            },
            (root, userMarkedSync, _) =>
            {
                observed = userMarkedSync;
                return new WorkspaceStorageObservation(
                    WorkspaceStorageKind.UserMarkedSync,
                    WorkspaceCoordinationStrength.Advisory,
                    1024,
                    false,
                    DateTimeOffset.UtcNow);
            });

        WorkspaceStorageObservation result =
            policy.ProbeCreateTargetAndEnsureSupported(
                Path.GetTempPath(),
                userMarkedSync: true);

        Assert.IsTrue(observed);
        Assert.AreEqual(
            WorkspaceStorageKind.UserMarkedSync,
            result.StorageKind);
    }

    [TestMethod]
    public void SuccessfulCreateProbeLeavesAnEmptyRootForWorkspaceLayout()
    {
        string target = Path.Combine(
            Path.GetTempPath(),
            "vibetable-provider-policy-tests",
            Guid.NewGuid().ToString("N"));
        WorkspaceProviderPolicy policy = WorkspaceProviderPolicy.CreateForTests(
            new Dictionary<WorkspaceStorageKind, bool>
            {
                [WorkspaceStorageKind.Fixed] = true,
            },
            (root, _, _) => new WorkspaceStorageObservation(
                WorkspaceStorageKind.Fixed,
                WorkspaceCoordinationStrength.Strong,
                1024,
                false,
                DateTimeOffset.UtcNow));

        try
        {
            _ = policy.ProbeCreateTargetAndEnsureSupported(target);

            Assert.IsTrue(Directory.Exists(target));
            Assert.IsFalse(Directory.EnumerateFileSystemEntries(target).Any());
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                target,
                "Managed",
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.Convenient);
            Assert.AreEqual(Path.GetFullPath(target), layout.SelectedRoot);
        }
        finally
        {
            if (Directory.Exists(target))
                Directory.Delete(target, recursive: true);
        }
    }
}
