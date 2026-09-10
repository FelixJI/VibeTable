using System.Diagnostics;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceReplicaRecoveryServiceTests
{
    [TestMethod]
    public async Task VerifyUsesLocalActivityDataDirAndReturnsRequiredRevision()
    {
        using var fixture = new ReplicaFixture(removeActivity: false);
        fixture.Runner.Handler = _ => Success(
            "verify",
            fixture.Workspace.WorkspaceId,
            activityRoot: null,
            state: "healthy",
            mutationRevision: 7,
            requiredMutationRevision: 6);

        WorkspaceReplicaReceipt receipt = await fixture.Service.VerifyAsync(
            fixture.Workspace,
            CancellationToken.None);

        Assert.AreEqual(7UL, receipt.MutationRevision);
        Assert.AreEqual(6UL, receipt.RequiredMutationRevision);
        Assert.AreEqual(
            WorkspaceLayout.Paths(fixture.Workspace.ActivityRoot!).Data,
            fixture.Runner.StartInfo!.Environment[
                "VIBETABLE_SIDECAR_DATA_DIR"]);
    }

    [TestMethod]
    public void ReceiptRejectsRemoteMutationBehindRequiredRevision()
    {
        Guid workspaceId = Guid.NewGuid();
        TrustedSidecarProcessResult result = Success(
            "verify",
            workspaceId,
            activityRoot: null,
            state: "healthy",
            mutationRevision: 4,
            requiredMutationRevision: 5);

        WorkspaceRegistryException error =
            Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                WorkspaceReplicaRecoveryService.ParseReceipt(
                    result.StandardOutput,
                    workspaceId,
                    "verify",
                    expectedActivityRoot: null));

        Assert.AreEqual("workspace.replica_response_invalid", error.Code);
    }

    [TestMethod]
    public void ReceiptAllowsZeroRequiredRevision()
    {
        Guid workspaceId = Guid.NewGuid();
        TrustedSidecarProcessResult result = Success(
            "verify",
            workspaceId,
            activityRoot: null,
            state: "healthy",
            mutationRevision: 1,
            requiredMutationRevision: 0);

        WorkspaceReplicaReceipt receipt =
            WorkspaceReplicaRecoveryService.ParseReceipt(
                result.StandardOutput,
                workspaceId,
                "verify",
                expectedActivityRoot: null);

        Assert.AreEqual(0UL, receipt.RequiredMutationRevision);
    }

    [TestMethod]
    public async Task RecoverUsesEnvironmentOnlyAndAtomicallyPublishesActivityRoot()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        fixture.Runner.Handler = start =>
        {
            string staging = start.Environment["VIBETABLE_ACTIVITY_ROOT"]!;
            CreateRecoveredLayout(
                fixture.Workspace.SelectedRoot,
                staging);
            return Success(
                "recover",
                fixture.Workspace.WorkspaceId,
                staging,
                "restored");
        };

        WorkspaceReplicaReceipt receipt =
            await fixture.Service.RecoverAndPublishAsync(
                fixture.Workspace,
                CancellationToken.None);

        Assert.AreEqual(
            Path.GetFullPath(fixture.Workspace.ActivityRoot!),
            receipt.ActivityRoot);
        Assert.IsTrue(Directory.Exists(
            WorkspaceLayout.Paths(receipt.ActivityRoot!).Data));
        Assert.IsTrue(File.Exists(Path.Combine(
            WorkspaceLayout.Paths(receipt.ActivityRoot!).Coordination,
            "workspace-v2.db")));
        Assert.IsFalse(File.Exists(Path.Combine(
            WorkspaceLayout.Paths(receipt.ActivityRoot!).Metadata,
            "settings.json")));
        ProcessStartInfo start = fixture.Runner.StartInfo!;
        CollectionAssert.AreEqual(
            new[] { "--recover-workspace-replica" },
            start.ArgumentList.ToArray());
        Assert.IsFalse(start.ArgumentList.Any(argument =>
            argument.Contains(fixture.Root, StringComparison.OrdinalIgnoreCase)));
        Assert.AreEqual(
            fixture.Workspace.SelectedRoot,
            start.Environment["VIBETABLE_REPLICA_ROOT"]);
        Assert.IsNull(fixture.Runner.StandardInput);
    }

    [TestMethod]
    public async Task RecoverWithProductionAuthorityKeepsFinalRootDetachedUntilPublish()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        ProductionWorkspaceRuntimeFactory factory = fixture.RuntimeFactory;
        int currentChanged = 0;
        int bindingChanged = 0;
        int clientReady = 0;
        IProductSidecarGenerationAuthority generations = factory;
        generations.CurrentChanged += () => currentChanged++;
        factory.BindingChanged += () => bindingChanged++;
        factory.ClientReady += () => clientReady++;
        fixture.Runner.Handler = start =>
        {
            Assert.IsFalse(
                Directory.Exists(fixture.Workspace.ActivityRoot),
                "Detached recovery must not publish into the final root before the one-shot succeeds.");
            Assert.IsNull(factory.CaptureProductSidecarGeneration());
            string staging = start.Environment["VIBETABLE_ACTIVITY_ROOT"]!;
            CreateRecoveredLayout(
                fixture.Workspace.SelectedRoot,
                staging);
            return Success(
                "recover",
                fixture.Workspace.WorkspaceId,
                staging,
                "restored");
        };

        WorkspaceReplicaReceipt receipt = await fixture.Service.RecoverAndPublishAsync(
            fixture.Workspace,
            CancellationToken.None);

        Assert.AreEqual(
            Path.GetFullPath(fixture.Workspace.ActivityRoot!),
            receipt.ActivityRoot);
        ProcessStartInfo start = fixture.Runner.StartInfo!;
        string authorityPath = Path.Combine(
            WorkspaceLayout.Paths(receipt.ActivityRoot!).Coordination,
            "desktop-runtime-authority.json");
        DesktopWorkspaceAuthority persisted =
            JsonSerializer.Deserialize<DesktopWorkspaceAuthority>(
                File.ReadAllText(authorityPath),
                WorkspaceV2Json.StrictOptions)!;
        Assert.AreEqual<ulong>(0, persisted.LastSessionEpoch);
        Assert.AreEqual(
            "1",
            start.Environment["VIBETABLE_WORKSPACE_SESSION_EPOCH"]);
        Assert.AreEqual(
            persisted.FenceEpoch.ToString(),
            start.Environment["VIBETABLE_WORKSPACE_FENCE_EPOCH"]);
        Assert.AreEqual(
            persisted.ClaimId.ToString("D").ToLowerInvariant(),
            start.Environment["VIBETABLE_WORKSPACE_CLAIM_ID"]);
        Assert.IsNull(factory.CaptureProductSidecarGeneration());
        Assert.IsNull(factory.CurrentCapabilities);
        Assert.AreEqual(0, currentChanged);
        Assert.AreEqual(0, bindingChanged);
        Assert.AreEqual(0, clientReady);

        await using (var firstRuntime = (ProductionWorkspaceRuntime)
                     factory.Create(fixture.Workspace, 1))
            AssertRuntimeAuthority(firstRuntime, persisted);

        await using var nextFactory = new ProductionWorkspaceRuntimeFactory(
            fixture.Options(),
            new BackendLaunchOptions
            {
                Command = Path.Combine(fixture.Root, "backend.exe"),
            },
            [fixture.Workspace]);
        Assert.AreEqual<ulong>(1, nextFactory.InitialSessionEpoch);
        await using var secondRuntime = (ProductionWorkspaceRuntime)
            nextFactory.Create(fixture.Workspace, 2);
        AssertRuntimeAuthority(secondRuntime, persisted);
        persisted = JsonSerializer.Deserialize<DesktopWorkspaceAuthority>(
            File.ReadAllText(authorityPath),
            WorkspaceV2Json.StrictOptions)!;
        Assert.AreEqual<ulong>(2, persisted.LastSessionEpoch);
    }

    [TestMethod]
    public async Task InvalidReceiptDoesNotPublishOrModifySelectedRoot()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        string before = File.ReadAllText(Path.Combine(
            fixture.Workspace.SelectedRoot,
            ".vibetable",
            "workspace.json"));
        fixture.Runner.Handler = start =>
        {
            string staging = start.Environment["VIBETABLE_ACTIVITY_ROOT"]!;
            CreateRecoveredLayout(
                fixture.Workspace.SelectedRoot,
                staging);
            return Success(
                "recover",
                Guid.NewGuid(),
                staging,
                "restored");
        };

        WorkspaceRegistryException error =
            await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
                fixture.Service.RecoverAndPublishAsync(
                    fixture.Workspace,
                    CancellationToken.None));

        Assert.AreEqual("workspace.replica_response_invalid", error.Code);
        Assert.IsFalse(Directory.Exists(fixture.Workspace.ActivityRoot));
        AssertNoRecoveryStaging(fixture);
        Assert.AreEqual(
            before,
            File.ReadAllText(Path.Combine(
                fixture.Workspace.SelectedRoot,
                ".vibetable",
                "workspace.json")));
    }

    [TestMethod]
    public async Task InvalidRecoveredLayoutDoesNotPublishAndRemovesOwnedStaging()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        fixture.Runner.Handler = start =>
        {
            CreateRecoveredLayout(
                fixture.Workspace.SelectedRoot,
                start.Environment["VIBETABLE_ACTIVITY_ROOT"]!);
            File.Delete(Path.Combine(
                WorkspaceLayout.Paths(
                    start.Environment["VIBETABLE_ACTIVITY_ROOT"]!).Data,
                "data.db"));
            return Success(
                "recover",
                fixture.Workspace.WorkspaceId,
                start.Environment["VIBETABLE_ACTIVITY_ROOT"],
                "restored");
        };

        WorkspaceRegistryException error =
            await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
                fixture.Service.RecoverAndPublishAsync(
                    fixture.Workspace,
                    CancellationToken.None));

        Assert.AreEqual("replica.recovery_install_failed", error.Code);
        Assert.IsFalse(Directory.Exists(fixture.Workspace.ActivityRoot));
        AssertNoRecoveryStaging(fixture);
    }

    [TestMethod]
    public async Task InvalidRecoveredIdentityIsPreservedAndNotPublished()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        Guid foreignWorkspaceId = Guid.NewGuid();
        fixture.Runner.Handler = start =>
        {
            string staging = start.Environment["VIBETABLE_ACTIVITY_ROOT"]!;
            string metadata = Path.Combine(staging, ".vibetable");
            Directory.CreateDirectory(metadata);
            WorkspaceManifestV2 selected =
                WorkspaceLayout.ReadManifest(
                    fixture.Workspace.SelectedRoot);
            File.WriteAllText(
                Path.Combine(metadata, "workspace.json"),
                JsonSerializer.Serialize(
                    selected with { WorkspaceId = foreignWorkspaceId },
                    WorkspaceV2Json.StrictOptions));
            return Success(
                "recover",
                fixture.Workspace.WorkspaceId,
                staging,
                "restored");
        };

        _ = await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
            fixture.Service.RecoverAndPublishAsync(
                fixture.Workspace,
                CancellationToken.None));

        string[] retained = Directory.GetDirectories(
            fixture.Root,
            ".activity.vibetable-recovering-*",
            SearchOption.TopDirectoryOnly);
        Assert.AreEqual(1, retained.Length);
        Assert.AreEqual(
            foreignWorkspaceId,
            WorkspaceLayout.ReadManifest(retained[0]).WorkspaceId);
        Assert.IsFalse(Directory.Exists(fixture.Workspace.ActivityRoot));
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task FinalCompetitorIsPreservedAndRecoveryCanRetry(
        bool writeMarker)
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        int attempts = 0;
        fixture.Runner.Handler = start =>
        {
            string staging = start.Environment["VIBETABLE_ACTIVITY_ROOT"]!;
            CreateRecoveredLayout(
                fixture.Workspace.SelectedRoot,
                staging);
            if (attempts++ == 0)
            {
                Directory.CreateDirectory(fixture.Workspace.ActivityRoot!);
                if (writeMarker)
                    File.WriteAllText(
                        Path.Combine(fixture.Workspace.ActivityRoot!, "owner.txt"),
                        "foreign-final-owner");
            }
            return Success(
                "recover",
                fixture.Workspace.WorkspaceId,
                staging,
                "restored");
        };

        WorkspaceRegistryException error =
            await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
                fixture.Service.RecoverAndPublishAsync(
                    fixture.Workspace,
                    CancellationToken.None));

        Assert.AreEqual("replica.recovery_target_invalid", error.Code);
        Assert.IsTrue(Directory.Exists(fixture.Workspace.ActivityRoot));
        if (writeMarker)
            Assert.AreEqual(
                "foreign-final-owner",
                File.ReadAllText(Path.Combine(
                    fixture.Workspace.ActivityRoot!,
                    "owner.txt")));
        else
            Assert.IsFalse(Directory.EnumerateFileSystemEntries(
                fixture.Workspace.ActivityRoot!).Any());
        AssertNoRecoveryStaging(fixture);

        Directory.Delete(fixture.Workspace.ActivityRoot!, recursive: true);
        WorkspaceReplicaReceipt retried =
            await fixture.Service.RecoverAndPublishAsync(
                fixture.Workspace,
                CancellationToken.None);
        Assert.IsTrue(Directory.Exists(retried.ActivityRoot));
    }

    [TestMethod]
    public async Task ForeignStagingAuthorityIsNeverOverwrittenOrDeleted()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        const string foreignAuthority = "foreign-authority-owner";
        fixture.Runner.Handler = start =>
        {
            string staging = start.Environment["VIBETABLE_ACTIVITY_ROOT"]!;
            CreateRecoveredLayout(
                fixture.Workspace.SelectedRoot,
                staging);
            File.WriteAllText(
                Path.Combine(
                    WorkspaceLayout.Paths(staging).Coordination,
                    "desktop-runtime-authority.json"),
                foreignAuthority);
            return Success(
                "recover",
                fixture.Workspace.WorkspaceId,
                staging,
                "restored");
        };

        _ = await Assert.ThrowsExactlyAsync<IOException>(() =>
            fixture.Service.RecoverAndPublishAsync(
                fixture.Workspace,
                CancellationToken.None));

        string[] retained = Directory.GetDirectories(
            fixture.Root,
            ".activity.vibetable-recovering-*",
            SearchOption.TopDirectoryOnly);
        Assert.AreEqual(1, retained.Length);
        Assert.AreEqual(
            foreignAuthority,
            File.ReadAllText(Path.Combine(
                WorkspaceLayout.Paths(retained[0]).Coordination,
                "desktop-runtime-authority.json")));
        Assert.IsFalse(Directory.Exists(fixture.Workspace.ActivityRoot));
    }

    [TestMethod]
    public async Task ShortStartupTimeoutDoesNotLimitLongReplicaRecovery()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        fixture.Runner.AsyncHandler = async (start, cancellationToken) =>
        {
            await Task.Delay(
                TimeSpan.FromMilliseconds(75),
                cancellationToken);
            string staging = start.Environment["VIBETABLE_ACTIVITY_ROOT"]!;
            CreateRecoveredLayout(
                fixture.Workspace.SelectedRoot,
                staging);
            return Success(
                "recover",
                fixture.Workspace.WorkspaceId,
                staging,
                "restored");
        };
        var service = new WorkspaceReplicaRecoveryService(
            () => fixture.OptionsWithStartup(
                TimeSpan.FromMilliseconds(1)),
            fixture.RuntimeFactory.PrepareRepositoryOnboarding,
            fixture.RuntimeFactory.PrepareDetachedRepositoryRecovery,
            fixture.Runner,
            replicaOperationTimeout: TimeSpan.FromSeconds(2));

        WorkspaceReplicaReceipt receipt =
            await service.RecoverAndPublishAsync(
                fixture.Workspace,
                CancellationToken.None);

        Assert.AreEqual(
            fixture.Workspace.ActivityRoot,
            receipt.ActivityRoot);
        Assert.AreEqual(
            TimeSpan.FromHours(4),
            WorkspaceReplicaRecoveryService
                .DefaultReplicaOperationTimeout);
    }

    [TestMethod]
    public async Task CallerCancellationAfterOneShotStillStopsPublication()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        using var caller = new CancellationTokenSource();
        fixture.Runner.Handler = start =>
        {
            string staging = start.Environment["VIBETABLE_ACTIVITY_ROOT"]!;
            CreateRecoveredLayout(
                fixture.Workspace.SelectedRoot,
                staging);
            caller.Cancel();
            return Success(
                "recover",
                fixture.Workspace.WorkspaceId,
                staging,
                "restored");
        };
        var service = new WorkspaceReplicaRecoveryService(
            () => fixture.OptionsWithStartup(
                TimeSpan.FromMilliseconds(1)),
            fixture.RuntimeFactory.PrepareRepositoryOnboarding,
            fixture.RuntimeFactory.PrepareDetachedRepositoryRecovery,
            fixture.Runner,
            replicaOperationTimeout: TimeSpan.FromSeconds(2));

        try
        {
            _ = await service.RecoverAndPublishAsync(
                fixture.Workspace,
                caller.Token);
            Assert.Fail("Caller cancellation should stop replica recovery.");
        }
        catch (OperationCanceledException)
        {
            Assert.IsTrue(caller.IsCancellationRequested);
            Assert.IsFalse(Directory.Exists(fixture.Workspace.ActivityRoot));
            AssertNoRecoveryStaging(fixture);
        }
    }

    [TestMethod]
    public async Task HealthyActivityDoesNotTouchOfflineSelectedRoot()
    {
        using var fixture = new ReplicaFixture(removeActivity: false);
        Directory.Delete(fixture.Workspace.SelectedRoot, recursive: true);
        var hook = new WorkspaceReplicaPreOpenHook(
            fixture.Service,
            new WorkspaceRepositoryOnboardingService(
                fixture.Options,
                _ => new WorkspaceRepositoryAuthority(1, Guid.NewGuid()),
                fixture.Runner),
            new NoopRecoveryUi());

        await hook.PrepareAsync(
            fixture.Workspace,
            CancellationToken.None);

        Assert.IsNull(fixture.Runner.StartInfo);
    }

    [TestMethod]
    public void ReceiptRejectsExtraPropertiesAndNonAuthenticatedHashShape()
    {
        Guid workspaceId = Guid.NewGuid();
        string raw = JsonSerializer.Serialize(new
        {
            contractVersion = "2.0",
            operation = "verify",
            workspaceId,
            replicaId = Guid.NewGuid(),
            snapshotId = Guid.NewGuid(),
            catalogRevision = 1,
            checkpointId = "sha256:" + new string('b', 64),
            receiptHash = "not-a-sha256",
            verifiedAt = DateTimeOffset.UtcNow.ToString("O"),
            activityRoot = (string?)null,
            healthy = true,
            unexpected = true,
        });

        WorkspaceRegistryException error =
            Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                WorkspaceReplicaRecoveryService.ParseReceipt(
                    raw,
                    workspaceId,
                    "verify",
                    expectedActivityRoot: null));

        Assert.AreEqual("workspace.replica_response_invalid", error.Code);
    }

    [TestMethod]
    public void ReceiptRejectsNonSha256CheckpointId()
    {
        Guid workspaceId = Guid.NewGuid();
        string raw = JsonSerializer.Serialize(new
        {
            contractVersion = "2.0",
            operation = "verify",
            workspaceId,
            replicaId = Guid.NewGuid(),
            snapshotId = Guid.NewGuid(),
            catalogRevision = 1,
            checkpointId = "checkpoint",
            receiptHash = "sha256:" + new string('a', 64),
            verifiedAt = DateTimeOffset.UtcNow.ToString("O"),
            activityRoot = (string?)null,
            healthy = true,
        });

        WorkspaceRegistryException error =
            Assert.ThrowsExactly<WorkspaceRegistryException>(() =>
                WorkspaceReplicaRecoveryService.ParseReceipt(
                    raw,
                    workspaceId,
                    "verify",
                    expectedActivityRoot: null));

        Assert.AreEqual("workspace.replica_response_invalid", error.Code);
    }

    [TestMethod]
    public async Task MirroredEntryOnFixedVolumeIsAlwaysProvisional()
    {
        using var fixture = new ReplicaFixture(removeActivity: false);
        var incorrectlyProjectedStrong = fixture.Workspace with
        {
            CoordinationStrength = WorkspaceCoordinationStrength.Strong,
        };
        using var lease = new WorkspaceCoordinationLeaseHook();

        WorkspaceOpenMode granted = await lease.AcquireAsync(
            incorrectlyProjectedStrong,
            WorkspaceOpenMode.Writable,
            CancellationToken.None);

        Assert.AreEqual(WorkspaceOpenMode.Provisional, granted);
    }

    private static TrustedSidecarProcessResult Success(
        string operation,
        Guid workspaceId,
        string? activityRoot,
        string state,
        ulong mutationRevision = 1,
        ulong requiredMutationRevision = 1)
    {
        var payload = new Dictionary<string, object?>
        {
            ["contractVersion"] = "2.0",
            ["operation"] = operation,
            ["workspaceId"] = workspaceId.ToString("D"),
            ["replicaId"] = Guid.NewGuid().ToString("D"),
            ["snapshotId"] = Guid.NewGuid().ToString("D"),
            ["catalogRevision"] = 1UL,
            ["mutationRevision"] = mutationRevision,
            ["requiredMutationRevision"] = requiredMutationRevision,
            ["checkpointId"] = "sha256:" + new string('b', 64),
            ["receiptHash"] = "sha256:" + new string('a', 64),
            ["verifiedAt"] = DateTimeOffset.UtcNow.ToString("O"),
            ["activityRoot"] = activityRoot,
            [state] = true,
        };
        return new TrustedSidecarProcessResult(
            0,
            JsonSerializer.Serialize(payload));
    }

    private static void AssertRuntimeAuthority(
        ProductionWorkspaceRuntime runtime,
        DesktopWorkspaceAuthority authority)
    {
        Assert.AreEqual(
            authority.FenceEpoch.ToString(),
            runtime.SidecarEnvironment["VIBETABLE_WORKSPACE_FENCE_EPOCH"]);
        Assert.AreEqual(
            authority.ClaimId.ToString("D").ToLowerInvariant(),
            runtime.SidecarEnvironment["VIBETABLE_WORKSPACE_CLAIM_ID"]);
    }

    private static void AssertNoRecoveryStaging(ReplicaFixture fixture)
        => Assert.AreEqual(
            0,
            Directory.GetDirectories(
                fixture.Root,
                ".activity.vibetable-recovering-*",
                SearchOption.TopDirectoryOnly).Length);

    private static void CreateRecoveredLayout(
        string selectedRoot,
        string activityRoot)
    {
        WorkspaceManifestV2 manifest =
            WorkspaceLayout.ReadManifest(selectedRoot);
        Directory.CreateDirectory(activityRoot);
        Directory.CreateDirectory(Path.Combine(activityRoot, "files"));
        string metadata = Path.Combine(activityRoot, ".vibetable");
        Directory.CreateDirectory(metadata);
        foreach (string name in new[]
                 {
                     "data", "topology", "objects", "audit", "snapshots",
                     "coordination", "quarantine", "temp",
                 })
            Directory.CreateDirectory(Path.Combine(metadata, name));
        File.WriteAllBytes(
            Path.Combine(metadata, "data", "data.db"),
            [0x56, 0x54]);
        File.WriteAllBytes(
            Path.Combine(metadata, "coordination", "workspace-v2.db"),
            [0x56, 0x54]);
        File.WriteAllBytes(
            Path.Combine(
                metadata,
                "coordination",
                "write-coordinator.db"),
            [0x56, 0x54]);
        File.WriteAllText(
            Path.Combine(metadata, "workspace.json"),
            JsonSerializer.Serialize(manifest, WorkspaceV2Json.StrictOptions));
    }

    private sealed class ReplicaFixture : IDisposable
    {
        public ReplicaFixture(bool removeActivity)
        {
            Root = Path.Combine(
                Path.GetTempPath(),
                "vibetable-replica-desktop-" + Guid.NewGuid().ToString("N"));
            string selected = Path.Combine(Root, "selected");
            string activity = Path.Combine(Root, "activity");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                selected,
                "Replica",
                WorkspaceStorageMode.Mirrored,
                WorkspaceEncryptionMode.None,
                activity);
            Workspace = new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = layout.Manifest.DisplayName,
                SelectedRoot = selected,
                ActivityRoot = activity,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = null,
                LastSyncAt = null,
                PendingSync = false,
            };
            RuntimeFactory = new ProductionWorkspaceRuntimeFactory(
                Options(),
                new BackendLaunchOptions
                {
                    Command = Path.Combine(Root, "backend.exe"),
                });
            if (removeActivity)
                Directory.Delete(activity, recursive: true);
            else
            {
                _ = RuntimeFactory.PrepareRepositoryOnboarding(Workspace);
                CreateRecoveredLayout(selected, activity);
            }
            Runner = new FakeRunner();
            Service = new WorkspaceReplicaRecoveryService(
                Options,
                RuntimeFactory.PrepareRepositoryOnboarding,
                RuntimeFactory.PrepareDetachedRepositoryRecovery,
                Runner);
        }

        public string Root { get; }
        public WorkspaceRegistryEntryV2 Workspace { get; }
        public FakeRunner Runner { get; }
        public ProductionWorkspaceRuntimeFactory RuntimeFactory { get; }
        public WorkspaceReplicaRecoveryService Service { get; }

        public PocketBaseLaunchOptions Options()
            => OptionsWithStartup(TimeSpan.FromSeconds(5));

        public PocketBaseLaunchOptions OptionsWithStartup(
            TimeSpan startupTimeout)
            => new()
            {
                ExecutablePath = Path.Combine(Root, "sidecar.exe"),
                WorkingDirectory = Root,
                DataDirectory = Path.Combine(Root, "unused"),
                LogPath = Path.Combine(Root, "unused.log"),
                StartupTimeout = startupTimeout,
                StopTimeout = TimeSpan.FromSeconds(1),
                HealthPollInterval = TimeSpan.FromMilliseconds(10),
                CrashRestartLimit = 0,
                CrashRestartInitialDelay = TimeSpan.Zero,
                CrashRestartMaximumDelay = TimeSpan.Zero,
                ExpectedIdentity = new PocketBaseExpectedIdentity(
                    "vibetable.sidecar.ready.v1",
                    "2.0",
                    "0.40.1",
                    "5",
                    "hash"),
                Environment = new Dictionary<string, string>(),
            };

        public void Dispose()
        {
            RuntimeFactory.DisposeAsync().AsTask().GetAwaiter().GetResult();
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

    private sealed class FakeRunner : ITrustedSidecarProcessRunner
    {
        public Func<ProcessStartInfo, TrustedSidecarProcessResult>? Handler
        {
            get;
            set;
        }
        public Func<
            ProcessStartInfo,
            CancellationToken,
            Task<TrustedSidecarProcessResult>>?
            AsyncHandler
        {
            get;
            set;
        }
        public ProcessStartInfo? StartInfo { get; private set; }
        public string? StandardInput { get; private set; }

        public Task<TrustedSidecarProcessResult> RunAsync(
            ProcessStartInfo startInfo,
            string? standardInput,
            CancellationToken cancellationToken)
        {
            StartInfo = startInfo;
            StandardInput = standardInput;
            if (AsyncHandler is not null)
                return AsyncHandler(startInfo, cancellationToken);
            return Task.FromResult(
                Handler?.Invoke(startInfo)
                ?? new TrustedSidecarProcessResult(1, string.Empty));
        }
    }

    private sealed class NoopRecoveryUi : IWorkspaceRepositoryRecoveryUi
    {
        public void ConfirmRecoveryKey(
            string workspaceDisplayName,
            string recoveryKey)
        {
        }

        public string? PromptRecoveryKey(string workspaceDisplayName)
            => null;
    }
}
