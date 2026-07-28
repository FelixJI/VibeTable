using VibeTable.Contracts;
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
                    "0.39.9",
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
}
