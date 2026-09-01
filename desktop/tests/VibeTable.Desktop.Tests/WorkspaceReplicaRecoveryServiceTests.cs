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
    private static readonly TimeSpan SignalTimeout = TimeSpan.FromSeconds(5);

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
        int currentChanged = 0;
        int bindingChanged = 0;
        int clientReady = 0;
        IProductSidecarGenerationAuthority generations = fixture.RuntimeFactory;
        generations.CurrentChanged += () => currentChanged++;
        fixture.RuntimeFactory.BindingChanged += () => bindingChanged++;
        fixture.RuntimeFactory.ClientReady += () => clientReady++;
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
        string authorityPath = Path.Combine(
            WorkspaceLayout.Paths(receipt.ActivityRoot!).Coordination,
            "desktop-runtime-authority.json");
        DesktopWorkspaceAuthority persisted =
            JsonSerializer.Deserialize<DesktopWorkspaceAuthority>(
                File.ReadAllText(authorityPath),
                WorkspaceV2Json.StrictOptions)!;
        Assert.AreEqual<ulong>(0, persisted.LastSessionEpoch);
        Assert.AreEqual(
            persisted.FenceEpoch.ToString(),
            start.Environment["VIBETABLE_WORKSPACE_FENCE_EPOCH"]);
        Assert.AreEqual(
            persisted.ClaimId.ToString("D").ToLowerInvariant(),
            start.Environment["VIBETABLE_WORKSPACE_CLAIM_ID"]);
        Assert.IsNull(fixture.RuntimeFactory.CaptureProductSidecarGeneration());
        Assert.AreEqual(0, currentChanged);
        Assert.AreEqual(0, bindingChanged);
        Assert.AreEqual(0, clientReady);

        await using var runtime = (ProductionWorkspaceRuntime)
            fixture.RuntimeFactory.Create(fixture.Workspace, 1);
        Assert.AreEqual(
            persisted.ClaimId.ToString("D").ToLowerInvariant(),
            runtime.SidecarEnvironment["VIBETABLE_WORKSPACE_CLAIM_ID"]);
        Assert.AreEqual(
            persisted.FenceEpoch.ToString(),
            runtime.SidecarEnvironment["VIBETABLE_WORKSPACE_FENCE_EPOCH"]);
        Assert.AreEqual(
            "1",
            runtime.SidecarEnvironment["VIBETABLE_WORKSPACE_SESSION_EPOCH"]);
        DesktopWorkspaceAuthority advanced =
            JsonSerializer.Deserialize<DesktopWorkspaceAuthority>(
                File.ReadAllText(authorityPath),
                WorkspaceV2Json.StrictOptions)!;
        Assert.AreEqual<ulong>(1, advanced.LastSessionEpoch);

        await using var nextFactory = new ProductionWorkspaceRuntimeFactory(
            fixture.Options(),
            new BackendLaunchOptions
            {
                Command = Path.Combine(fixture.Root, "backend-next.exe"),
            },
            [fixture.Workspace]);
        Assert.AreEqual<ulong>(1, nextFactory.InitialSessionEpoch);
        await using var nextRuntime = (ProductionWorkspaceRuntime)
            nextFactory.Create(fixture.Workspace, 2);
        Assert.AreEqual(
            persisted.ClaimId.ToString("D").ToLowerInvariant(),
            nextRuntime.SidecarEnvironment["VIBETABLE_WORKSPACE_CLAIM_ID"]);
        Assert.AreEqual(
            persisted.FenceEpoch.ToString(),
            nextRuntime.SidecarEnvironment["VIBETABLE_WORKSPACE_FENCE_EPOCH"]);
        advanced = JsonSerializer.Deserialize<DesktopWorkspaceAuthority>(
            File.ReadAllText(authorityPath),
            WorkspaceV2Json.StrictOptions)!;
        Assert.AreEqual<ulong>(2, advanced.LastSessionEpoch);
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
        Assert.AreEqual(
            0,
            Directory.GetDirectories(
                fixture.Root,
                ".activity.vibetable-recovering-*",
                SearchOption.TopDirectoryOnly).Length);
        Assert.AreEqual(
            before,
            File.ReadAllText(Path.Combine(
                fixture.Workspace.SelectedRoot,
                ".vibetable",
                "workspace.json")));
    }

    [TestMethod]
    public async Task FailedSidecarRemovesOwnedManifestStaging()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        fixture.Runner.Handler = start =>
        {
            CreateRecoveredLayout(
                fixture.Workspace.SelectedRoot,
                start.Environment["VIBETABLE_ACTIVITY_ROOT"]!);
            return new TrustedSidecarProcessResult(2, string.Empty);
        };

        WorkspaceRegistryException error =
            await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
                fixture.Service.RecoverAndPublishAsync(
                    fixture.Workspace,
                    CancellationToken.None));

        Assert.AreEqual("workspace.replica_request_invalid", error.Code);
        Assert.IsFalse(Directory.Exists(fixture.Workspace.ActivityRoot));
        Assert.AreEqual(
            0,
            Directory.GetDirectories(
                fixture.Root,
                ".activity.vibetable-recovering-*",
                SearchOption.TopDirectoryOnly).Length);
    }

    [TestMethod]
    public async Task IncompleteRecoveredLayoutUsesStableErrorAndRemovesStaging()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        fixture.Runner.Handler = start =>
        {
            string staging = start.Environment["VIBETABLE_ACTIVITY_ROOT"]!;
            CreateRecoveredLayout(fixture.Workspace.SelectedRoot, staging);
            File.Delete(Path.Combine(
                WorkspaceLayout.Paths(staging).Data,
                "data.db"));
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

        Assert.AreEqual("replica.recovery_install_failed", error.Code);
        Assert.IsFalse(Directory.Exists(fixture.Workspace.ActivityRoot));
        Assert.AreEqual(0, RecoveryArtifacts(fixture).Length);
    }

    [TestMethod]
    public async Task ForeignManifestStagingIsPreservedForDiagnosis()
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
            return new TrustedSidecarProcessResult(1, string.Empty);
        };

        _ = await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
            fixture.Service.RecoverAndPublishAsync(
                fixture.Workspace,
                CancellationToken.None));

        string[] retained = RecoveryArtifacts(fixture);
        Assert.AreEqual(1, retained.Length);
        Assert.AreEqual(
            foreignWorkspaceId,
            WorkspaceLayout.ReadManifest(retained[0]).WorkspaceId);
    }

    [TestMethod]
    public async Task ForeignAuthorityIsPreservedBehindStablePublishError()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        const string foreignAuthority = "foreign-authority-owner";
        fixture.Runner.Handler = start =>
        {
            string staging = start.Environment["VIBETABLE_ACTIVITY_ROOT"]!;
            CreateRecoveredLayout(fixture.Workspace.SelectedRoot, staging);
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

        WorkspaceRegistryException error =
            await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
                fixture.Service.RecoverAndPublishAsync(
                    fixture.Workspace,
                    CancellationToken.None));

        Assert.AreEqual(
            "replica.recovery_install_failed",
            error.Code);
        Assert.IsInstanceOfType<IOException>(error.InnerException);
        string retained = RecoveryArtifacts(fixture).Single();
        Assert.AreEqual(
            foreignAuthority,
            File.ReadAllText(Path.Combine(
                WorkspaceLayout.Paths(retained).Coordination,
                "desktop-runtime-authority.json")));
        Assert.IsFalse(Directory.Exists(fixture.Workspace.ActivityRoot));
    }

    [TestMethod]
    public async Task FinalCompetitorIsPreservedAndClaimCanRetry()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        Directory.CreateDirectory(fixture.Workspace.ActivityRoot!);
        int attempts = 0;
        fixture.Runner.Handler = start =>
        {
            Assert.IsFalse(Directory.Exists(fixture.Workspace.ActivityRoot));
            string staging = start.Environment["VIBETABLE_ACTIVITY_ROOT"]!;
            CreateRecoveredLayout(fixture.Workspace.SelectedRoot, staging);
            if (attempts++ == 0)
            {
                Directory.CreateDirectory(fixture.Workspace.ActivityRoot!);
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
        Assert.AreEqual(
            "foreign-final-owner",
            File.ReadAllText(Path.Combine(
                fixture.Workspace.ActivityRoot!,
                "owner.txt")));
        Assert.AreEqual(0, RecoveryArtifacts(fixture).Length);

        Directory.Delete(fixture.Workspace.ActivityRoot!, recursive: true);
        WorkspaceReplicaReceipt retry =
            await fixture.Service.RecoverAndPublishAsync(
                fixture.Workspace,
                CancellationToken.None);
        Assert.IsTrue(Directory.Exists(retry.ActivityRoot));
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
            fixture.RuntimeFactory,
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
    public async Task CallerCancellationStopsInFlightReplicaAndReleasesClaim()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        var started = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var canceled = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Runner.AsyncHandler = async (_, cancellationToken) =>
        {
            started.SetResult();
            try
            {
                await Task.Delay(
                    Timeout.InfiniteTimeSpan,
                    cancellationToken);
            }
            catch (OperationCanceledException)
            {
                canceled.SetResult();
                throw new OperationCanceledException(cancellationToken);
            }
            throw new InvalidOperationException("Unreachable.");
        };
        using var caller = new CancellationTokenSource();
        Task<WorkspaceReplicaReceipt> recovery =
            fixture.Service.RecoverAndPublishAsync(
                fixture.Workspace,
                caller.Token);
        await started.Task.WaitAsync(SignalTimeout);
        caller.Cancel();

        _ = await Assert.ThrowsExactlyAsync<OperationCanceledException>(
            () => recovery.WaitAsync(SignalTimeout));
        await canceled.Task.WaitAsync(SignalTimeout);
        Assert.IsFalse(Directory.Exists(fixture.Workspace.ActivityRoot));
        Assert.AreEqual(0, RecoveryArtifacts(fixture).Length);

        fixture.Runner.AsyncHandler = null;
        fixture.Runner.Handler = start =>
        {
            string staging = start.Environment["VIBETABLE_ACTIVITY_ROOT"]!;
            CreateRecoveredLayout(fixture.Workspace.SelectedRoot, staging);
            return Success(
                "recover",
                fixture.Workspace.WorkspaceId,
                staging,
                "restored");
        };
        WorkspaceReplicaReceipt retry =
            await fixture.Service.RecoverAndPublishAsync(
                fixture.Workspace,
                CancellationToken.None);
        Assert.IsTrue(Directory.Exists(retry.ActivityRoot));
    }

    [TestMethod]
    public async Task CompletedOneShotPublishesBeforeLaterCallerCancellation()
    {
        using var fixture = new ReplicaFixture(removeActivity: true);
        var completed = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var returnResult = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Runner.AsyncHandler = async (start, _) =>
        {
            string staging = start.Environment["VIBETABLE_ACTIVITY_ROOT"]!;
            CreateRecoveredLayout(fixture.Workspace.SelectedRoot, staging);
            completed.SetResult();
            await returnResult.Task.WaitAsync(SignalTimeout);
            return Success(
                "recover",
                fixture.Workspace.WorkspaceId,
                staging,
                "restored");
        };
        using var caller = new CancellationTokenSource();
        Task<WorkspaceReplicaReceipt> recovery =
            fixture.Service.RecoverAndPublishAsync(
                fixture.Workspace,
                caller.Token);
        await completed.Task.WaitAsync(SignalTimeout);
        caller.Cancel();
        returnResult.SetResult();

        WorkspaceReplicaReceipt receipt =
            await recovery.WaitAsync(SignalTimeout);
        Assert.IsTrue(caller.IsCancellationRequested);
        Assert.AreEqual(
            Path.GetFullPath(fixture.Workspace.ActivityRoot!),
            receipt.ActivityRoot);
        Assert.IsTrue(Directory.Exists(receipt.ActivityRoot));
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
                RuntimeFactory,
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
            TimeSpan startupTimeout) => new()
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

    private static string[] RecoveryArtifacts(ReplicaFixture fixture)
        => Directory.GetDirectories(
                fixture.Root,
                "*vibetable-*",
                SearchOption.TopDirectoryOnly)
            .Where(path => Path.GetFileName(path).Contains(
                "recovering",
                StringComparison.Ordinal))
            .ToArray();

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
            Task<TrustedSidecarProcessResult>>? AsyncHandler
        { get; set; }
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
