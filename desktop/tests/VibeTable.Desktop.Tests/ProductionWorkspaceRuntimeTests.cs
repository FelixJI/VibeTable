using VibeTable.Contracts;
using System.Text.Json;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductionWorkspaceRuntimeTests
{
    [TestMethod]
    public async Task ShellStartDoesNotCreateOrBindADataRuntime()
    {
        await using var factory = Factory();

        await factory.StartAsync(CancellationToken.None);

        Assert.IsNull(factory.CurrentBackend);
        Assert.IsNull(factory.CurrentSidecar);
        Assert.IsNull(factory.CurrentV2Gateway);
    }

    [TestMethod]
    public async Task ProductSidecarLifecycleHasOneCompositionOwner()
    {
        await using var factory = Factory();
        using var first = new FakeProductSidecarLifecycle();
        using var second = new FakeProductSidecarLifecycle();

        factory.RegisterProductSidecarGatewayLifecycle(first);
        factory.RegisterProductSidecarGatewayLifecycle(first);

        Assert.ThrowsExactly<InvalidOperationException>(() =>
            factory.RegisterProductSidecarGatewayLifecycle(second));
    }

    [TestMethod]
    public async Task MissingSidecarIsResolvedOnlyWhenAWorkspaceOpens()
    {
        int resolutions = 0;
        await using var factory = new ProductionWorkspaceRuntimeFactory(
            () =>
            {
                resolutions++;
                throw new FileNotFoundException("sidecar missing");
            },
            () => new BackendLaunchOptions
            {
                Command = "backend.exe",
            });

        await factory.StartAsync(CancellationToken.None);

        Assert.AreEqual(0, resolutions);
        string root = CreateRoot();
        try
        {
            Assert.ThrowsExactly<FileNotFoundException>(() =>
                factory.Create(Entry(root), 1));
            Assert.AreEqual(1, resolutions);
            Assert.IsNull(factory.CurrentSidecar);
        }
        finally
        {
            TryDelete(root);
        }
    }

    [TestMethod]
    public async Task CreateBindsCanonicalIdentityAndActivityDataRoot()
    {
        string root = CreateRoot();
        try
        {
            WorkspaceRegistryEntryV2 entry = Entry(root);
            await using var factory = Factory();
            await using var runtime = (ProductionWorkspaceRuntime)
                factory.Create(entry, 7);

            Assert.AreEqual(
                Path.Combine(root, ".vibetable", "data"),
                runtime.DataDirectory);
            AssertAuthority(runtime.SidecarEnvironment, entry.WorkspaceId, 7);
            AssertAuthority(runtime.BackendEnvironment, entry.WorkspaceId, 7);
            Assert.IsFalse(runtime.SidecarEnvironment.ContainsKey(
                "VIBETABLE_REPLICA_ROOT"));
            Assert.AreEqual(
                runtime.SidecarEnvironment[
                    "VIBETABLE_WORKSPACE_CLAIM_ID"],
                runtime.BackendEnvironment[
                    "VIBETABLE_WORKSPACE_CLAIM_ID"]);
        }
        finally
        {
            TryDelete(root);
        }
    }

    [TestMethod]
    public async Task MirroredRuntimeBindsSelectedReplicaRootOnlyToSidecar()
    {
        string container = Path.Combine(
            Path.GetTempPath(),
            "vibetable-mirrored-runtime-" + Guid.NewGuid().ToString("N"));
        string selectedRoot = Path.Combine(container, "replica");
        string activityRoot = Path.Combine(container, "activity");
        WorkspaceLayoutResult layout = WorkspaceLayout.Create(
            selectedRoot,
            "Mirrored runtime",
            WorkspaceStorageMode.Mirrored,
            WorkspaceEncryptionMode.Convenient,
            activityRoot);
        var entry = new WorkspaceRegistryEntryV2
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = layout.Manifest.WorkspaceId,
            DisplayName = layout.Manifest.DisplayName,
            SelectedRoot = selectedRoot,
            ActivityRoot = activityRoot,
            StorageKind = WorkspaceStorageKind.Fixed,
            CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
            LastOpenedAt = null,
            LastKnownHealth = WorkspaceHealth.Healthy,
            LastSnapshotAt = null,
            LastSyncAt = null,
            PendingSync = false,
        };
        try
        {
            await using var factory = Factory();
            await using var runtime = (ProductionWorkspaceRuntime)
                factory.Create(entry, 3);

            Assert.AreEqual(
                Path.GetFullPath(selectedRoot),
                runtime.SidecarEnvironment["VIBETABLE_REPLICA_ROOT"]);
            Assert.IsFalse(runtime.BackendEnvironment.ContainsKey(
                "VIBETABLE_REPLICA_ROOT"));
            Assert.AreEqual(
                Path.Combine(activityRoot, ".vibetable", "data"),
                runtime.DataDirectory);
        }
        finally
        {
            TryDelete(container);
        }
    }

    [TestMethod]
    public async Task AuthoritySurvivesFactoryRestartAndEpochAdvances()
    {
        string root = CreateRoot();
        try
        {
            WorkspaceRegistryEntryV2 entry = Entry(root);
            string firstClaim;
            await using (var firstFactory = Factory())
            {
                await using var first = (ProductionWorkspaceRuntime)
                    firstFactory.Create(entry, 11);
                firstClaim = first.SidecarEnvironment[
                    "VIBETABLE_WORKSPACE_CLAIM_ID"];
            }
            await using var secondFactory = Factory([entry]);
            Assert.AreEqual<ulong>(11, secondFactory.InitialSessionEpoch);
            await using var second = (ProductionWorkspaceRuntime)
                secondFactory.Create(entry, 12);

            Assert.AreEqual(
                firstClaim,
                second.SidecarEnvironment[
                    "VIBETABLE_WORKSPACE_CLAIM_ID"]);
            Assert.AreEqual(
                "12",
                second.SidecarEnvironment[
                    "VIBETABLE_WORKSPACE_SESSION_EPOCH"]);
            Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                secondFactory.Create(entry, 12));
        }
        finally
        {
            TryDelete(root);
        }
    }

    [TestMethod]
    public async Task OnboardingAuthorityIsReusedByFirstRuntimeSession()
    {
        string root = CreateRoot();
        try
        {
            WorkspaceRegistryEntryV2 entry = Entry(root);
            await using var factory = Factory();
            WorkspaceRepositoryAuthority onboarding =
                factory.PrepareRepositoryOnboarding(entry);
            await using var runtime = (ProductionWorkspaceRuntime)
                factory.Create(entry, 1);

            Assert.AreEqual(
                onboarding.ClaimId.ToString("D").ToLowerInvariant(),
                runtime.SidecarEnvironment[
                    "VIBETABLE_WORKSPACE_CLAIM_ID"]);
            Assert.AreEqual(
                onboarding.FenceEpoch.ToString(),
                runtime.SidecarEnvironment[
                    "VIBETABLE_WORKSPACE_FENCE_EPOCH"]);
        }
        finally
        {
            TryDelete(root);
        }
    }

    [TestMethod]
    public async Task DetachedClaimBlocksWorkspaceAndCanonicalPathsAcrossFactories()
    {
        string container = Path.Combine(
            Path.GetTempPath(),
            "vibetable-detached-claims-" + Guid.NewGuid().ToString("N"));
        string firstSelected = Path.Combine(container, "first-selected");
        string firstActivity = Path.Combine(container, "first-activity");
        string secondSelected = Path.Combine(container, "second-selected");
        WorkspaceLayoutResult firstLayout = WorkspaceLayout.Create(
            firstSelected,
            "First detached claim",
            WorkspaceStorageMode.Mirrored,
            WorkspaceEncryptionMode.None,
            firstActivity);
        WorkspaceLayoutResult secondLayout = WorkspaceLayout.Create(
            secondSelected,
            "Second detached claim",
            WorkspaceStorageMode.Mirrored,
            WorkspaceEncryptionMode.None,
            Path.Combine(container, "second-activity"));
        WorkspaceRegistryEntryV2 first = MirroredEntry(
            firstLayout,
            firstSelected,
            firstActivity);
        string firstStaging = Path.Combine(container, ".first-recovering");
        WorkspaceRegistryEntryV2 second = MirroredEntry(
            secondLayout,
            secondSelected,
            Path.Combine(container, ".", ".first-recovering"));
        WorkspaceRegistryEntryV2 sameWorkspaceOtherFinal = first with
        {
            ActivityRoot = secondLayout.ActivityRoot,
        };
        WorkspaceRegistryEntryV2 differentWorkspaceSameFinal = second with
        {
            ActivityRoot = firstActivity,
        };
        WorkspaceRegistryEntryV2 differentWorkspaceOtherFinal = second with
        {
            ActivityRoot = secondLayout.ActivityRoot,
        };
        Directory.Delete(firstActivity, recursive: true);
        Directory.Delete(secondLayout.ActivityRoot!, recursive: true);
        try
        {
            await using var firstFactory = Factory();
            await using var secondFactory = Factory();
            using DesktopWorkspaceAuthorityStore.DetachedReservation claim =
                firstFactory.PrepareDetachedRepositoryRecovery(
                    first,
                    firstStaging);

            WorkspaceRegistryException workspaceConflict =
                Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                    secondFactory.Create(first, 1));
            WorkspaceRegistryException prepareConflict =
                Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                    secondFactory.PrepareRepositoryOnboarding(first));
            WorkspaceRegistryException prepareSameFinal =
                Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                    secondFactory.PrepareRepositoryOnboarding(
                        differentWorkspaceSameFinal));
            WorkspaceRegistryException reserveSameFinal =
                Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                    secondFactory.Create(differentWorkspaceSameFinal, 1));
            WorkspaceRegistryException prepareSameStaging =
                Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                    secondFactory.PrepareRepositoryOnboarding(second));
            WorkspaceRegistryException reserveSameStaging =
                Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                    secondFactory.Create(second, 1));
            WorkspaceRegistryException finalToStagingConflict =
                Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                    secondFactory.PrepareDetachedRepositoryRecovery(
                        second,
                        firstActivity));
            WorkspaceRegistryException sameWorkspaceConflict =
                Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                    secondFactory.PrepareDetachedRepositoryRecovery(
                        sameWorkspaceOtherFinal,
                        Path.Combine(container, ".same-workspace-staging")));
            WorkspaceRegistryException sameFinalConflict =
                Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                    secondFactory.PrepareDetachedRepositoryRecovery(
                        differentWorkspaceSameFinal,
                        Path.Combine(container, ".same-final-staging")));
            WorkspaceRegistryException sameStagingConflict =
                Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                    secondFactory.PrepareDetachedRepositoryRecovery(
                        differentWorkspaceOtherFinal,
                        firstStaging));

            Assert.AreEqual(
                "workspace.authority_detached_active",
                workspaceConflict.Code);
            Assert.AreEqual(
                workspaceConflict.Code,
                prepareConflict.Code);
            Assert.AreEqual(
                workspaceConflict.Code,
                finalToStagingConflict.Code);
            Assert.AreEqual(workspaceConflict.Code, sameWorkspaceConflict.Code);
            Assert.AreEqual(workspaceConflict.Code, sameFinalConflict.Code);
            Assert.AreEqual(workspaceConflict.Code, sameStagingConflict.Code);
            Assert.AreEqual(workspaceConflict.Code, prepareSameFinal.Code);
            Assert.AreEqual(workspaceConflict.Code, reserveSameFinal.Code);
            Assert.AreEqual(workspaceConflict.Code, prepareSameStaging.Code);
            Assert.AreEqual(workspaceConflict.Code, reserveSameStaging.Code);
            Assert.IsFalse(Directory.Exists(firstActivity));
            Assert.IsFalse(Directory.Exists(firstStaging));

            claim.Dispose();
            claim.Dispose();
            using DesktopWorkspaceAuthorityStore.DetachedReservation retry =
                secondFactory.PrepareDetachedRepositoryRecovery(
                    second,
                    firstActivity);
        }
        finally
        {
            TryDelete(container);
        }
    }

    [TestMethod]
    public async Task QuarantinedCleanupPathRemainsClaimedUntilReservationReleases()
    {
        string container = Path.Combine(
            Path.GetTempPath(),
            "vibetable-detached-cleanup-" + Guid.NewGuid().ToString("N"));
        string selected = Path.Combine(container, "selected");
        string activity = Path.Combine(container, "activity");
        WorkspaceLayoutResult layout = WorkspaceLayout.Create(
            selected,
            "Cleanup claim",
            WorkspaceStorageMode.Mirrored,
            WorkspaceEncryptionMode.None,
            activity);
        WorkspaceRegistryEntryV2 entry = MirroredEntry(
            layout,
            selected,
            activity);
        Directory.Delete(activity, recursive: true);
        string staging = Path.Combine(container, ".activity-recovering");
        try
        {
            await using var owner = Factory();
            await using var competitor = Factory();
            DesktopWorkspaceAuthorityStore.DetachedReservation claim =
                owner.PrepareDetachedRepositoryRecovery(entry, staging);
            Directory.CreateDirectory(Path.Combine(staging, ".vibetable"));
            File.WriteAllText(
                Path.Combine(staging, ".vibetable", "workspace.json"),
                JsonSerializer.Serialize(
                    layout.Manifest,
                    WorkspaceV2Json.StrictOptions));
            var quarantined = new TaskCompletionSource<string>(
                TaskCreationOptions.RunContinuationsAsynchronously);
            var release = new TaskCompletionSource(
                TaskCreationOptions.RunContinuationsAsynchronously);
            claim.CleanupQuarantinedForTests = path =>
            {
                quarantined.SetResult(path);
                release.Task.WaitAsync(TimeSpan.FromSeconds(5))
                    .GetAwaiter().GetResult();
            };
            Task disposing = Task.Run(claim.Dispose);
            string cleanup = await quarantined.Task.WaitAsync(
                TimeSpan.FromSeconds(5));
            WorkspaceRegistryEntryV2 other = entry with
            {
                WorkspaceId = Guid.NewGuid(),
                ActivityRoot = cleanup,
            };
            try
            {
                Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                    competitor.PrepareRepositoryOnboarding(other));
                Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                    competitor.Create(other, 1));
            }
            finally
            {
                release.TrySetResult();
                await disposing.WaitAsync(TimeSpan.FromSeconds(5));
            }
        }
        finally
        {
            TryDelete(container);
        }
    }

    private static ProductionWorkspaceRuntimeFactory Factory(
        IEnumerable<WorkspaceRegistryEntryV2>? entries = null)
        => new(
            new PocketBaseLaunchOptions
            {
                ExecutablePath = "vibetable-pb.exe",
                DataDirectory = "unused",
                ExpectedIdentity = new PocketBaseExpectedIdentity(
                    "vibetable.sidecar.ready.v1",
                    "2.0",
                    "0.40.1",
                    "5",
                    "hash"),
            },
            new BackendLaunchOptions
            {
                Command = "vibetable-backend.exe",
                Arguments = string.Empty,
            },
            entries);

    private static string CreateRoot()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-production-runtime-" + Guid.NewGuid().ToString("N"));
        WorkspaceLayout.Create(
            root,
            "Runtime",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.None);
        return root;
    }

    private static WorkspaceRegistryEntryV2 Entry(string root)
    {
        WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(root);
        return new WorkspaceRegistryEntryV2
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = manifest.WorkspaceId,
            DisplayName = manifest.DisplayName,
            SelectedRoot = root,
            ActivityRoot = null,
            StorageKind = WorkspaceStorageKind.Fixed,
            CoordinationStrength = WorkspaceCoordinationStrength.Strong,
            LastOpenedAt = null,
            LastKnownHealth = WorkspaceHealth.Healthy,
            LastSnapshotAt = null,
            LastSyncAt = null,
            PendingSync = false,
        };
    }

    private static WorkspaceRegistryEntryV2 MirroredEntry(
        WorkspaceLayoutResult layout,
        string selectedRoot,
        string activityRoot)
        => new()
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = layout.Manifest.WorkspaceId,
            DisplayName = layout.Manifest.DisplayName,
            SelectedRoot = selectedRoot,
            ActivityRoot = activityRoot,
            StorageKind = WorkspaceStorageKind.Fixed,
            CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
            LastOpenedAt = null,
            LastKnownHealth = WorkspaceHealth.Healthy,
            LastSnapshotAt = null,
            LastSyncAt = null,
            PendingSync = false,
        };

    private static void AssertAuthority(
        IReadOnlyDictionary<string, string> environment,
        Guid workspaceId,
        ulong sessionEpoch)
    {
        Assert.AreEqual(
            workspaceId.ToString("D").ToLowerInvariant(),
            environment["VIBETABLE_WORKSPACE_ID"]);
        Assert.AreEqual(
            sessionEpoch.ToString(),
            environment["VIBETABLE_WORKSPACE_SESSION_EPOCH"]);
        Assert.AreEqual(
            "1",
            environment["VIBETABLE_WORKSPACE_FENCE_EPOCH"]);
        Assert.IsTrue(Guid.TryParse(
            environment["VIBETABLE_WORKSPACE_CLAIM_ID"],
            out Guid claimId));
        Assert.AreNotEqual(Guid.Empty, claimId);
    }

    private static void TryDelete(string root)
    {
        try
        {
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
        catch
        {
            // Best effort after process-free unit tests.
        }
    }

    private sealed class FakeProductSidecarLifecycle : IProductSidecarGatewayLifecycle
    {
        public Task<bool> TryReplaceAsync(
            ProductSidecarGenerationSnapshot snapshot,
            CancellationToken cancellationToken)
            => Task.FromResult(true);

        public bool Clear(ProductSidecarGenerationSnapshot expectedSnapshot) => true;
        public void Dispose() { }
    }
}
