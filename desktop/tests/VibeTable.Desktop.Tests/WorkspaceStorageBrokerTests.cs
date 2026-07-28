using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceStorageBrokerTests
{
    [TestMethod]
    public async Task RelocationPlanSurvivesBrokerRestartAndUpdatesRegistry()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-broker-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        var registry = new WorkspaceRegistry(root);
        var runtimes = new TestRuntimeFactory();
        var sessions = new WorkspaceSessionManager(
            registry,
            runtimes);
        try
        {
            string source = Path.Combine(root, "source");
            string target = Path.Combine(root, "target");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                source,
                "季度规划",
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.Convenient);
            File.WriteAllText(
                Path.Combine(source, "files", "plan.txt"),
                "durable");
            registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = layout.Manifest.DisplayName,
                SelectedRoot = source,
                ActivityRoot = null,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength =
                    WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = null,
                LastSyncAt = null,
                PendingSync = false,
            });
            WorkspaceProviderPolicy policy = FixedProviderPolicy();
            var first = new WorkspaceStorageBroker(
                registry,
                sessions,
                policy,
                root);
            JsonElement previewParameters =
                JsonSerializer.SerializeToElement(new
                {
                    workspaceId =
                        layout.Manifest.WorkspaceId.ToString("D"),
                    action = "relocate",
                    targetMode = "direct",
                    selectedRootGrant = "host-path-grant://test",
                });

            JsonElement preview = await first.PreviewAsync(
                previewParameters,
                target,
                CancellationToken.None);
            Guid planId = Guid.Parse(
                preview.GetProperty("planId").GetString()!);

            var restarted = new WorkspaceStorageBroker(
                registry,
                sessions,
                policy,
                root);
            _ = await sessions.OpenAsync(
                layout.Manifest.WorkspaceId,
                WorkspaceOpenMode.Writable);
            JsonElement applied = await restarted.ApplyAsync(
                JsonSerializer.SerializeToElement(new
                {
                    planId = planId.ToString("D"),
                    confirmation = "季度规划",
                }),
                CancellationToken.None);

            Assert.AreEqual(
                "applied",
                applied.GetProperty("status").GetString());
            Assert.AreEqual(
                WorkspaceSessionState.Closed,
                sessions.Current.State);
            Assert.AreEqual(
                Path.GetFullPath(target),
                registry.List().Single().SelectedRoot);
            Assert.AreEqual(
                "durable",
                File.ReadAllText(Path.Combine(
                    target,
                    "files",
                    "plan.txt")));
            Assert.IsTrue(
                Directory.Exists(source),
                "Relocation retains the verified source copy.");
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task CloseFailureAbortsBeforeRelocationCopy()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-close-failure-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        var registry = new WorkspaceRegistry(root);
        var runtimes = new TestRuntimeFactory();
        var sessions = new WorkspaceSessionManager(registry, runtimes);
        try
        {
            string source = Path.Combine(root, "source");
            string target = Path.Combine(root, "target");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                source,
                "不可停服",
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.Convenient);
            registry.Register(Entry(layout, source));
            var broker = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root);
            JsonElement preview = await broker.PreviewAsync(
                JsonSerializer.SerializeToElement(new
                {
                    workspaceId =
                        layout.Manifest.WorkspaceId.ToString("D"),
                    action = "relocate",
                    targetMode = "direct",
                    selectedRootGrant = "host-path-grant://test",
                }),
                target,
                CancellationToken.None);
            _ = await sessions.OpenAsync(
                layout.Manifest.WorkspaceId,
                WorkspaceOpenMode.Writable);
            runtimes.FailDrain = true;

            await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
                broker.ApplyAsync(
                    JsonSerializer.SerializeToElement(new
                    {
                        planId = preview.GetProperty("planId").GetString(),
                        confirmation = "不可停服",
                    }),
                    CancellationToken.None));

            Assert.AreEqual(
                Path.GetFullPath(source),
                registry.List().Single().SelectedRoot);
            Assert.IsFalse(
                File.Exists(Path.Combine(target, ".vibetable", "workspace.json")));
            Assert.AreEqual(
                WorkspaceSessionState.OpenedWritable,
                sessions.Current.State);
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task ActivePreviewIgnoresWriterLockAndApplySealsAfterProtection()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-active-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        var registry = new WorkspaceRegistry(root);
        var runtimes = new TestRuntimeFactory();
        string source = Path.Combine(root, "source");
        string target = Path.Combine(root, "target");
        WorkspaceLayoutResult layout = WorkspaceLayout.Create(
            source,
            "活动迁移",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.Convenient);
        registry.Register(Entry(layout, source));
        var protection = new WritingProtectionHook(source);
        using var coordinationLease = new WorkspaceCoordinationLeaseHook();
        var sessions = new WorkspaceSessionManager(
            registry,
            runtimes,
            protection,
            coordinationLease);
        try
        {
            _ = await sessions.OpenAsync(
                layout.Manifest.WorkspaceId,
                WorkspaceOpenMode.Writable);
            var broker = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root);

            JsonElement preview = await broker.PreviewAsync(
                RelocationPreview(layout.Manifest.WorkspaceId),
                target,
                CancellationToken.None);
            JsonElement applied = await broker.ApplyAsync(
                ApplyParameters(
                    preview.GetProperty("planId").GetString()!,
                    "活动迁移"),
                CancellationToken.None);

            Assert.AreEqual(
                "applied",
                applied.GetProperty("status").GetString());
            Assert.AreEqual(1, protection.Calls);
            Assert.AreEqual(
                "created by protection",
                File.ReadAllText(Path.Combine(
                    target,
                    "files",
                    "protected.txt")));
            Assert.IsFalse(
                File.Exists(Path.Combine(
                    WorkspaceLayout.Paths(target).Coordination,
                    "desktop-writer.lock")));
            Assert.IsFalse(
                File.Exists(Path.Combine(
                    WorkspaceLayout.Paths(source).Coordination,
                    "storage-maintenance.lock")));
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task MaintenanceIntentBlocksCompetingWriterUntilRegistryPublish()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-concurrency-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        var registry = new WorkspaceRegistry(root);
        var sessions = new WorkspaceSessionManager(
            registry,
            new TestRuntimeFactory());
        using var competingLease = new WorkspaceCoordinationLeaseHook();
        try
        {
            string source = Path.Combine(root, "source");
            string target = Path.Combine(root, "target");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                source,
                "并发隔离",
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.Convenient);
            WorkspaceRegistryEntryV2 entry = Entry(layout, source);
            registry.Register(entry);
            var checkpoint = new CompetingWriterCheckpoint(
                competingLease,
                entry);
            var broker = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                checkpoint);
            JsonElement preview = await broker.PreviewAsync(
                RelocationPreview(layout.Manifest.WorkspaceId),
                target,
                CancellationToken.None);

            _ = await broker.ApplyAsync(
                ApplyParameters(
                    preview.GetProperty("planId").GetString()!,
                    "并发隔离"),
                CancellationToken.None);

            Assert.AreEqual(
                "workspace.storage_maintenance_conflict",
                checkpoint.CompetingError?.Code);
            Assert.AreEqual(
                Path.GetFullPath(target),
                registry.List().Single().SelectedRoot);
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task CrashAfterVerifiedCopyResumesWithoutPublishingStaleSource()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-crash-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        var registry = new WorkspaceRegistry(root);
        var sessions = new WorkspaceSessionManager(
            registry,
            new TestRuntimeFactory());
        try
        {
            string source = Path.Combine(root, "source");
            string target = Path.Combine(root, "target");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                source,
                "崩溃恢复",
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.Convenient);
            File.WriteAllText(
                Path.Combine(source, "files", "durable.txt"),
                "durable");
            registry.Register(Entry(layout, source));
            var crash = new ThrowOnceCheckpoint("after-copy");
            var first = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                crash);
            JsonElement preview = await first.PreviewAsync(
                RelocationPreview(layout.Manifest.WorkspaceId),
                target,
                CancellationToken.None);
            JsonElement apply = ApplyParameters(
                preview.GetProperty("planId").GetString()!,
                "崩溃恢复");

            await Assert.ThrowsExactlyAsync<InjectedStorageCrash>(
                () => first.ApplyAsync(apply, CancellationToken.None));

            Assert.AreEqual(
                Path.GetFullPath(source),
                registry.List().Single().SelectedRoot);
            Assert.AreEqual(
                "durable",
                File.ReadAllText(Path.Combine(
                    target,
                    "files",
                    "durable.txt")));

            var restarted = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root);
            _ = await restarted.ApplyAsync(
                apply,
                CancellationToken.None);

            Assert.AreEqual(
                Path.GetFullPath(target),
                registry.List().Single().SelectedRoot);
            Assert.IsTrue(Directory.Exists(source));
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task TopologyAndCacheActionsFailClosedWithoutReceipt()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-unsupported-" + Guid.NewGuid().ToString("N"));
        var registry = new WorkspaceRegistry(root);
        var sessions = new WorkspaceSessionManager(
            registry,
            new TestRuntimeFactory());
        try
        {
            var broker = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root);
            string directRoot = Path.Combine(root, "direct");
            WorkspaceLayoutResult direct = WorkspaceLayout.Create(
                directRoot,
                "No replica",
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.Convenient);
            registry.Register(Entry(direct, directRoot));
            WorkspaceRegistryException convertError =
                await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(
                    () => broker.PreviewAsync(
                        JsonSerializer.SerializeToElement(new
                        {
                            workspaceId =
                                direct.Manifest.WorkspaceId.ToString("D"),
                            action = "convertTopology",
                            targetMode = "mirrored",
                            selectedRootGrant = "host-path-grant://test",
                        }),
                        Path.Combine(root, "remote"),
                        CancellationToken.None));
            Assert.AreEqual(
                "workspace.storage_replica_capability_unavailable",
                convertError.Code);

            string replicaRoot = Path.Combine(root, "replica");
            string activityRoot = Path.Combine(root, "activity");
            WorkspaceLayoutResult mirrored = WorkspaceLayout.Create(
                replicaRoot,
                "No verified replica",
                WorkspaceStorageMode.Mirrored,
                WorkspaceEncryptionMode.Convenient,
                activityRoot);
            registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = mirrored.Manifest.WorkspaceId,
                DisplayName = mirrored.Manifest.DisplayName,
                SelectedRoot = replicaRoot,
                ActivityRoot = activityRoot,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength =
                    WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = null,
                LastSyncAt = null,
                PendingSync = false,
            });
            WorkspaceRegistryException releaseError =
                await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(
                    () => broker.PreviewAsync(
                        JsonSerializer.SerializeToElement(new
                        {
                            workspaceId =
                                mirrored.Manifest.WorkspaceId.ToString("D"),
                            action = "releaseActivityCache",
                            targetMode = (string?)null,
                            selectedRootGrant = (string?)null,
                        }),
                        selectedRoot: null,
                        CancellationToken.None));
            Assert.AreEqual(
                "workspace.storage_replica_capability_unavailable",
                releaseError.Code);
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task DirectToMirroredRequiresReceiptAndPublishesAdvisoryTopology()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-convert-" + Guid.NewGuid().ToString("N"));
        var registry = new WorkspaceRegistry(root);
        var sessions = new WorkspaceSessionManager(
            registry,
            new TestRuntimeFactory());
        try
        {
            string source = Path.Combine(root, "direct");
            string replica = Path.Combine(root, "replica");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                source,
                "拓扑转换",
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.Convenient);
            registry.Register(Entry(layout, source));
            var verified = new FakeReplicaRecovery();
            var broker = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                replicas: verified);

            JsonElement preview = await broker.PreviewAsync(
                JsonSerializer.SerializeToElement(new
                {
                    workspaceId = layout.Manifest.WorkspaceId.ToString("D"),
                    action = "convertTopology",
                    targetMode = "mirrored",
                    selectedRootGrant = "host-path-grant://test",
                }),
                replica,
                CancellationToken.None);
            JsonElement applied = await broker.ApplyAsync(
                ApplyParameters(
                    preview.GetProperty("planId").GetString()!,
                    "拓扑转换"),
                CancellationToken.None);

            Assert.AreEqual(
                "applied",
                applied.GetProperty("status").GetString());
            WorkspaceRegistryEntryV2 current = registry.List().Single();
            Assert.AreEqual(Path.GetFullPath(replica), current.SelectedRoot);
            Assert.AreEqual(Path.GetFullPath(source), current.ActivityRoot);
            Assert.AreEqual(
                WorkspaceCoordinationStrength.Advisory,
                current.CoordinationStrength);
            Assert.AreEqual(
                WorkspaceStorageMode.Mirrored,
                WorkspaceLayout.ReadManifest(source).StorageMode);
            Assert.AreEqual(
                WorkspaceStorageMode.Mirrored,
                WorkspaceLayout.ReadManifest(replica).StorageMode);
            Assert.AreEqual(1, verified.InitializeCalls);
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task DirectToMirroredResumesAfterRegistryPublishCrash()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-convert-publish-resume-" +
            Guid.NewGuid().ToString("N"));
        var registry = new WorkspaceRegistry(root);
        var sessions = new WorkspaceSessionManager(
            registry,
            new TestRuntimeFactory());
        try
        {
            string source = Path.Combine(root, "direct");
            string replica = Path.Combine(root, "replica");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                source,
                "发布断点续转",
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.None);
            registry.Register(Entry(layout, source));
            var replicas = new FakeReplicaRecovery();
            var crashing = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                new ThrowOnceCheckpoint(
                    "after-conversion-registry-publish"),
                replicas);
            JsonElement preview = await crashing.PreviewAsync(
                JsonSerializer.SerializeToElement(new
                {
                    workspaceId = layout.Manifest.WorkspaceId.ToString("D"),
                    action = "convertTopology",
                    targetMode = "mirrored",
                    selectedRootGrant = "host-path-grant://test",
                }),
                replica,
                CancellationToken.None);
            JsonElement apply = ApplyParameters(
                preview.GetProperty("planId").GetString()!,
                "发布断点续转");

            _ = await Assert.ThrowsExactlyAsync<InjectedStorageCrash>(
                () => crashing.ApplyAsync(apply, CancellationToken.None));
            Assert.AreEqual(
                Path.GetFullPath(replica),
                registry.List().Single().SelectedRoot);

            var resumed = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                replicas: replicas);
            JsonElement applied = await resumed.ApplyAsync(
                apply,
                CancellationToken.None);

            Assert.AreEqual(
                "applied",
                applied.GetProperty("status").GetString());
            WorkspaceRegistryEntryV2 current = registry.List().Single();
            Assert.AreEqual(Path.GetFullPath(source), current.ActivityRoot);
            Assert.IsFalse(current.PendingSync);
            Assert.AreEqual(
                WorkspaceCoordinationStrength.Advisory,
                current.CoordinationStrength);
            Assert.AreEqual(1, replicas.InitializeCalls);
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task FailedInitialReplicaVerificationRollsBackTopology()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-convert-fail-" + Guid.NewGuid().ToString("N"));
        var registry = new WorkspaceRegistry(root);
        var sessions = new WorkspaceSessionManager(
            registry,
            new TestRuntimeFactory());
        try
        {
            string source = Path.Combine(root, "direct");
            string replica = Path.Combine(root, "replica");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                source,
                "失败回滚",
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.None);
            registry.Register(Entry(layout, source));
            var verified = new FakeReplicaRecovery { FailInitialize = true };
            var broker = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                replicas: verified);
            JsonElement preview = await broker.PreviewAsync(
                JsonSerializer.SerializeToElement(new
                {
                    workspaceId = layout.Manifest.WorkspaceId.ToString("D"),
                    action = "convertTopology",
                    targetMode = "mirrored",
                    selectedRootGrant = "host-path-grant://test",
                }),
                replica,
                CancellationToken.None);

            _ = await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(
                () => broker.ApplyAsync(
                    ApplyParameters(
                        preview.GetProperty("planId").GetString()!,
                        "失败回滚"),
                    CancellationToken.None));

            WorkspaceRegistryEntryV2 current = registry.List().Single();
            Assert.AreEqual(Path.GetFullPath(source), current.SelectedRoot);
            Assert.IsNull(current.ActivityRoot);
            Assert.AreEqual(
                WorkspaceStorageMode.Direct,
                WorkspaceLayout.ReadManifest(source).StorageMode);
            Assert.IsFalse(Directory.Exists(replica));
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task ReleaseCacheDeletesActivityOnlyAfterIndependentVerify()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-release-" + Guid.NewGuid().ToString("N"));
        var registry = new WorkspaceRegistry(root);
        var sessions = new WorkspaceSessionManager(
            registry,
            new TestRuntimeFactory());
        try
        {
            string replica = Path.Combine(root, "replica");
            string activity = Path.Combine(root, "activity");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                replica,
                "释放缓存",
                WorkspaceStorageMode.Mirrored,
                WorkspaceEncryptionMode.None,
                activity);
            var entry = new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = layout.Manifest.DisplayName,
                SelectedRoot = replica,
                ActivityRoot = activity,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = DateTimeOffset.UtcNow,
                LastSyncAt = DateTimeOffset.UtcNow,
                PendingSync = false,
            };
            registry.Register(entry);
            var verified = new FakeReplicaRecovery();
            var broker = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                replicas: verified);
            JsonElement preview = await broker.PreviewAsync(
                JsonSerializer.SerializeToElement(new
                {
                    workspaceId = entry.WorkspaceId.ToString("D"),
                    action = "releaseActivityCache",
                    targetMode = (string?)null,
                    selectedRootGrant = (string?)null,
                }),
                selectedRoot: null,
                CancellationToken.None);

            _ = await broker.ApplyAsync(
                ApplyParameters(
                    preview.GetProperty("planId").GetString()!,
                    "释放缓存"),
                CancellationToken.None);

            Assert.AreEqual(1, verified.VerifyCalls);
            Assert.IsFalse(Directory.Exists(activity));
            Assert.IsTrue(Directory.Exists(replica));
            WorkspaceRegistryEntryV2 current = registry.List().Single();
            Assert.AreEqual(activity, current.ActivityRoot);
            Assert.AreEqual(WorkspaceHealth.Offline, current.LastKnownHealth);
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task ReleaseCacheResumesAfterDeleteBeforeRegistryHealth()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-release-resume-" +
            Guid.NewGuid().ToString("N"));
        var registry = new WorkspaceRegistry(root);
        var sessions = new WorkspaceSessionManager(
            registry,
            new TestRuntimeFactory());
        try
        {
            string replica = Path.Combine(root, "replica");
            string activity = Path.Combine(root, "activity");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                replica,
                "释放断点恢复",
                WorkspaceStorageMode.Mirrored,
                WorkspaceEncryptionMode.None,
                activity);
            registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = layout.Manifest.DisplayName,
                SelectedRoot = replica,
                ActivityRoot = activity,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = DateTimeOffset.UtcNow,
                LastSyncAt = DateTimeOffset.UtcNow,
                PendingSync = false,
            });
            var replicas = new FakeReplicaRecovery();
            var crashing = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                new ThrowOnceCheckpoint("after-release-cache-delete"),
                replicas);
            JsonElement preview = await crashing.PreviewAsync(
                JsonSerializer.SerializeToElement(new
                {
                    workspaceId = layout.Manifest.WorkspaceId.ToString("D"),
                    action = "releaseActivityCache",
                    targetMode = (string?)null,
                    selectedRootGrant = (string?)null,
                }),
                selectedRoot: null,
                CancellationToken.None);
            JsonElement apply = ApplyParameters(
                preview.GetProperty("planId").GetString()!,
                "释放断点恢复");

            _ = await Assert.ThrowsExactlyAsync<InjectedStorageCrash>(
                () => crashing.ApplyAsync(apply, CancellationToken.None));
            Assert.IsFalse(Directory.Exists(activity));

            var resumed = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                replicas: replicas);
            _ = await resumed.ApplyAsync(apply, CancellationToken.None);

            WorkspaceRegistryEntryV2 current = registry.List().Single();
            Assert.IsFalse(Directory.Exists(activity));
            Assert.AreEqual(WorkspaceHealth.Offline, current.LastKnownHealth);
            Assert.AreEqual(1, replicas.VerifyCalls);
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task ActiveReleaseRequiresReplicaToCoverProtectionHighWatermark()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-release-high-watermark-" +
            Guid.NewGuid().ToString("N"));
        var registry = new WorkspaceRegistry(root);
        var protection = new SynchronizedProtectionHook(5);
        var sessions = new WorkspaceSessionManager(
            registry,
            new TestRuntimeFactory(),
            protection);
        try
        {
            string replica = Path.Combine(root, "replica");
            string activity = Path.Combine(root, "activity");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                replica,
                "高水位释放",
                WorkspaceStorageMode.Mirrored,
                WorkspaceEncryptionMode.None,
                activity);
            var entry = new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = layout.Manifest.DisplayName,
                SelectedRoot = replica,
                ActivityRoot = activity,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = DateTimeOffset.UtcNow,
                LastSyncAt = DateTimeOffset.UtcNow,
                PendingSync = false,
            };
            registry.Register(entry);
            await sessions.OpenAsync(
                entry.WorkspaceId,
                WorkspaceOpenMode.Writable);
            var replicas = new FakeReplicaRecovery
            {
                MutationRevision = 4,
            };
            var broker = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                replicas: replicas);
            JsonElement preview = await broker.PreviewAsync(
                JsonSerializer.SerializeToElement(new
                {
                    workspaceId = entry.WorkspaceId.ToString("D"),
                    action = "releaseActivityCache",
                    targetMode = (string?)null,
                    selectedRootGrant = (string?)null,
                }),
                selectedRoot: null,
                CancellationToken.None);
            JsonElement apply = ApplyParameters(
                preview.GetProperty("planId").GetString()!,
                "高水位释放");

            WorkspaceRegistryException unsafeRelease =
                await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(
                    () => broker.ApplyAsync(apply, CancellationToken.None));
            Assert.AreEqual(
                "workspace.release_cache_unsafe",
                unsafeRelease.Code);
            Assert.IsTrue(Directory.Exists(activity));
            Assert.AreEqual(1, protection.SynchronizeCalls);

            replicas.MutationRevision = 5;
            _ = await broker.ApplyAsync(apply, CancellationToken.None);
            Assert.IsFalse(Directory.Exists(activity));
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task InactiveReleaseUsesDurableReceiptInsteadOfRegistryPendingSync()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-inactive-release-high-watermark-" +
            Guid.NewGuid().ToString("N"));
        var registry = new WorkspaceRegistry(root);
        var sessions = new WorkspaceSessionManager(
            registry,
            new TestRuntimeFactory());
        try
        {
            string replica = Path.Combine(root, "replica");
            string activity = Path.Combine(root, "activity");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                replica,
                "崩溃重开释放",
                WorkspaceStorageMode.Mirrored,
                WorkspaceEncryptionMode.None,
                activity);
            var entry = new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = layout.Manifest.DisplayName,
                SelectedRoot = replica,
                ActivityRoot = activity,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Degraded,
                LastSnapshotAt = DateTimeOffset.UtcNow,
                LastSyncAt = DateTimeOffset.UtcNow,
                // Simulates a crash after the authoritative commit but before
                // the advisory registry projection was refreshed.
                PendingSync = true,
            };
            registry.Register(entry);
            var replicas = new FakeReplicaRecovery
            {
                MutationRevision = 4,
                RequiredMutationRevision = 5,
            };
            var broker = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                replicas: replicas);
            JsonElement preview = await broker.PreviewAsync(
                JsonSerializer.SerializeToElement(new
                {
                    workspaceId = entry.WorkspaceId.ToString("D"),
                    action = "releaseActivityCache",
                    targetMode = (string?)null,
                    selectedRootGrant = (string?)null,
                }),
                selectedRoot: null,
                CancellationToken.None);
            JsonElement apply = ApplyParameters(
                preview.GetProperty("planId").GetString()!,
                "崩溃重开释放");

            WorkspaceRegistryException unsafeRelease =
                await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(
                    () => broker.ApplyAsync(apply, CancellationToken.None));
            Assert.AreEqual(
                "workspace.release_cache_unsafe",
                unsafeRelease.Code);
            Assert.IsTrue(Directory.Exists(activity));

            replicas.MutationRevision = 5;
            _ = await broker.ApplyAsync(apply, CancellationToken.None);

            WorkspaceRegistryEntryV2 current = registry.List().Single();
            Assert.IsFalse(Directory.Exists(activity));
            Assert.IsFalse(current.PendingSync);
            Assert.AreEqual(WorkspaceHealth.Offline, current.LastKnownHealth);
            Assert.AreEqual(2, replicas.VerifyCalls);
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task InactiveReleaseRejectsAnotherProcessMirroredWriter()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-inactive-release-writer-" +
            Guid.NewGuid().ToString("N"));
        var registry = new WorkspaceRegistry(root);
        var sessions = new WorkspaceSessionManager(
            registry,
            new TestRuntimeFactory());
        try
        {
            string replica = Path.Combine(root, "replica");
            string activity = Path.Combine(root, "activity");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                replica,
                "跨进程释放",
                WorkspaceStorageMode.Mirrored,
                WorkspaceEncryptionMode.None,
                activity);
            var entry = new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = layout.Manifest.DisplayName,
                SelectedRoot = replica,
                ActivityRoot = activity,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength =
                    WorkspaceCoordinationStrength.Advisory,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = DateTimeOffset.UtcNow,
                LastSyncAt = DateTimeOffset.UtcNow,
                PendingSync = false,
            };
            registry.Register(entry);
            var replicas = new FakeReplicaRecovery();
            var broker = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                replicas: replicas);
            using var competingProcess =
                new WorkspaceCoordinationLeaseHook();
            Assert.AreEqual(
                WorkspaceOpenMode.Provisional,
                await competingProcess.AcquireAsync(
                    entry,
                    WorkspaceOpenMode.Writable,
                    CancellationToken.None));
            JsonElement preview = await broker.PreviewAsync(
                JsonSerializer.SerializeToElement(new
                {
                    workspaceId = entry.WorkspaceId.ToString("D"),
                    action = "releaseActivityCache",
                    targetMode = (string?)null,
                    selectedRootGrant = (string?)null,
                }),
                selectedRoot: null,
                CancellationToken.None);
            JsonElement apply = ApplyParameters(
                preview.GetProperty("planId").GetString()!,
                "跨进程释放");

            WorkspaceRegistryException conflict =
                await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(
                    () => broker.ApplyAsync(apply, CancellationToken.None));
            Assert.AreEqual(
                "workspace.storage_writer_fence_conflict",
                conflict.Code);
            Assert.IsTrue(Directory.Exists(activity));
            Assert.AreEqual(0, replicas.VerifyCalls);

            await competingProcess.ReleaseAsync(
                entry.WorkspaceId,
                sessionEpoch: 1,
                CancellationToken.None);
            _ = await broker.ApplyAsync(apply, CancellationToken.None);

            Assert.IsFalse(Directory.Exists(activity));
            Assert.AreEqual(1, replicas.VerifyCalls);
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task MirroredToDirectVerifiesReplicaAndCopiesActivityState()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-to-direct-" + Guid.NewGuid().ToString("N"));
        var registry = new WorkspaceRegistry(root);
        var sessions = new WorkspaceSessionManager(
            registry,
            new TestRuntimeFactory());
        try
        {
            string replica = Path.Combine(root, "replica");
            string activity = Path.Combine(root, "activity");
            string direct = Path.Combine(root, "direct");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                replica,
                "转为直连",
                WorkspaceStorageMode.Mirrored,
                WorkspaceEncryptionMode.None,
                activity);
            File.WriteAllText(
                Path.Combine(activity, "files", "kept.txt"),
                "kept");
            var entry = new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = layout.Manifest.DisplayName,
                SelectedRoot = replica,
                ActivityRoot = activity,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = DateTimeOffset.UtcNow,
                LastSyncAt = DateTimeOffset.UtcNow,
                PendingSync = false,
            };
            registry.Register(entry);
            var verified = new FakeReplicaRecovery();
            var broker = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                replicas: verified);
            JsonElement preview = await broker.PreviewAsync(
                JsonSerializer.SerializeToElement(new
                {
                    workspaceId = entry.WorkspaceId.ToString("D"),
                    action = "convertTopology",
                    targetMode = "direct",
                    selectedRootGrant = "host-path-grant://test",
                }),
                direct,
                CancellationToken.None);

            _ = await broker.ApplyAsync(
                ApplyParameters(
                    preview.GetProperty("planId").GetString()!,
                    "转为直连"),
                CancellationToken.None);

            WorkspaceRegistryEntryV2 current = registry.List().Single();
            Assert.AreEqual(Path.GetFullPath(direct), current.SelectedRoot);
            Assert.IsNull(current.ActivityRoot);
            Assert.AreEqual(
                WorkspaceStorageMode.Direct,
                WorkspaceLayout.ReadManifest(direct).StorageMode);
            Assert.AreEqual(
                "kept",
                File.ReadAllText(Path.Combine(direct, "files", "kept.txt")));
            Assert.AreEqual(1, verified.VerifyCalls);
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task MirroredToDirectResumesAfterVerifiedCopyCrash()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-storage-convert-resume-" +
            Guid.NewGuid().ToString("N"));
        var registry = new WorkspaceRegistry(root);
        var sessions = new WorkspaceSessionManager(
            registry,
            new TestRuntimeFactory());
        try
        {
            string replica = Path.Combine(root, "replica");
            string activity = Path.Combine(root, "activity");
            string direct = Path.Combine(root, "direct");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                replica,
                "断点续转",
                WorkspaceStorageMode.Mirrored,
                WorkspaceEncryptionMode.None,
                activity);
            File.WriteAllText(
                Path.Combine(activity, "files", "durable.txt"),
                "durable");
            registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = layout.Manifest.DisplayName,
                SelectedRoot = replica,
                ActivityRoot = activity,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = DateTimeOffset.UtcNow,
                LastSyncAt = DateTimeOffset.UtcNow,
                PendingSync = false,
            });
            var replicas = new FakeReplicaRecovery();
            var crashing = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                new ThrowOnceCheckpoint("after-conversion-copy"),
                replicas);
            JsonElement preview = await crashing.PreviewAsync(
                JsonSerializer.SerializeToElement(new
                {
                    workspaceId = layout.Manifest.WorkspaceId.ToString("D"),
                    action = "convertTopology",
                    targetMode = "direct",
                    selectedRootGrant = "host-path-grant://test",
                }),
                direct,
                CancellationToken.None);
            JsonElement apply = ApplyParameters(
                preview.GetProperty("planId").GetString()!,
                "断点续转");
            _ = await Assert.ThrowsExactlyAsync<InjectedStorageCrash>(
                () => crashing.ApplyAsync(apply, CancellationToken.None));

            var resumed = new WorkspaceStorageBroker(
                registry,
                sessions,
                FixedProviderPolicy(),
                root,
                replicas: replicas);
            _ = await resumed.ApplyAsync(apply, CancellationToken.None);

            Assert.AreEqual(
                "durable",
                File.ReadAllText(Path.Combine(direct, "files", "durable.txt")));
            Assert.AreEqual(
                WorkspaceStorageMode.Direct,
                WorkspaceLayout.ReadManifest(direct).StorageMode);
            Assert.IsNull(registry.List().Single().ActivityRoot);
        }
        finally
        {
            await sessions.DisposeAsync();
            if (Directory.Exists(root))
                Directory.Delete(root, recursive: true);
        }
    }

    private static WorkspaceProviderPolicy FixedProviderPolicy()
        => WorkspaceProviderPolicy.CreateForTests(
            new Dictionary<WorkspaceStorageKind, bool>
            {
                [WorkspaceStorageKind.Fixed] = true,
            },
            (path, _, _) => new WorkspaceStorageObservation(
                WorkspaceStorageKind.Fixed,
                WorkspaceCoordinationStrength.Strong,
                1024 * 1024,
                true,
                DateTimeOffset.UtcNow));

    private static WorkspaceRegistryEntryV2 Entry(
        WorkspaceLayoutResult layout,
        string root)
        => new()
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = layout.Manifest.WorkspaceId,
            DisplayName = layout.Manifest.DisplayName,
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

    private static JsonElement RelocationPreview(Guid workspaceId)
        => JsonSerializer.SerializeToElement(new
        {
            workspaceId = workspaceId.ToString("D"),
            action = "relocate",
            targetMode = "direct",
            selectedRootGrant = "host-path-grant://test",
        });

    private static JsonElement ApplyParameters(
        string planId,
        string confirmation)
        => JsonSerializer.SerializeToElement(new
        {
            planId,
            confirmation,
        });

    private sealed class WritingProtectionHook(string sourceRoot) :
        IWorkspaceProtectionHook
    {
        public int Calls { get; private set; }

        public Task ProtectAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            string reason,
            CancellationToken cancellationToken)
        {
            Calls++;
            File.WriteAllText(
                Path.Combine(sourceRoot, "files", "protected.txt"),
                "created by protection");
            return Task.CompletedTask;
        }
    }

    private sealed class CompetingWriterCheckpoint(
        WorkspaceCoordinationLeaseHook lease,
        WorkspaceRegistryEntryV2 workspace) :
        IWorkspaceStorageFailureInjector
    {
        public WorkspaceRegistryException? CompetingError { get; private set; }

        public void Checkpoint(string checkpoint)
        {
            if (checkpoint != "after-seal")
                return;
            CompetingError = Assert.ThrowsExactly<WorkspaceRegistryException>(
                () => lease.AcquireAsync(
                        workspace,
                        WorkspaceOpenMode.Writable,
                        CancellationToken.None)
                    .GetAwaiter()
                    .GetResult());
        }
    }

    private sealed class ThrowOnceCheckpoint(string target) :
        IWorkspaceStorageFailureInjector
    {
        private bool _thrown;

        public void Checkpoint(string checkpoint)
        {
            if (!_thrown && checkpoint == target)
            {
                _thrown = true;
                throw new InjectedStorageCrash();
            }
        }
    }

    private sealed class InjectedStorageCrash : Exception;

    private sealed class FakeReplicaRecovery :
        IWorkspaceReplicaRecoveryService
    {
        public bool FailInitialize { get; set; }
        public ulong MutationRevision { get; set; } = 1;
        public ulong? RequiredMutationRevision { get; set; }
        public int InitializeCalls { get; private set; }
        public int VerifyCalls { get; private set; }

        public Task<WorkspaceReplicaReceipt> InitializeAsync(
            WorkspaceRegistryEntryV2 workspace,
            CancellationToken cancellationToken)
        {
            InitializeCalls++;
            if (FailInitialize)
                throw new WorkspaceRegistryException(
                    "replica.verification_invalid",
                    "Injected replica verification failure.");
            return Task.FromResult(Receipt(workspace, "initialize"));
        }

        public Task<WorkspaceReplicaReceipt> VerifyAsync(
            WorkspaceRegistryEntryV2 workspace,
            CancellationToken cancellationToken)
        {
            VerifyCalls++;
            return Task.FromResult(Receipt(workspace, "verify"));
        }

        public Task<WorkspaceReplicaReceipt> RecoverAndPublishAsync(
            WorkspaceRegistryEntryV2 workspace,
            CancellationToken cancellationToken)
            => Task.FromResult(Receipt(workspace, "recover"));

        public bool RequiresRecovery(WorkspaceRegistryEntryV2 workspace)
            => false;

        private WorkspaceReplicaReceipt Receipt(
            WorkspaceRegistryEntryV2 workspace,
            string operation)
            => new(
                operation,
                workspace.WorkspaceId,
                Guid.NewGuid(),
                Guid.NewGuid(),
                1,
                "checkpoint",
                "sha256:" + new string('a', 64),
                DateTimeOffset.UtcNow,
                operation == "recover" ? workspace.ActivityRoot : null,
                MutationRevision,
                RequiredMutationRevision ?? MutationRevision);
    }

    private sealed class SynchronizedProtectionHook(
        ulong mutationRevision) : IWorkspaceProtectionHook
    {
        public int SynchronizeCalls { get; private set; }

        public Task ProtectAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            string reason,
            CancellationToken cancellationToken)
            => Task.CompletedTask;

        public Task<ulong> ProtectAndSynchronizeAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            string reason,
            CancellationToken cancellationToken)
        {
            SynchronizeCalls++;
            return Task.FromResult(mutationRevision);
        }
    }

    private sealed class TestRuntimeFactory : IWorkspaceRuntimeFactory
    {
        public bool FailDrain { get; set; }

        public IWorkspaceRuntime Create(
            WorkspaceRegistryEntryV2 workspace,
            ulong sessionEpoch)
            => new Runtime(
                this,
                workspace.WorkspaceId,
                sessionEpoch);

        private sealed class Runtime(
            TestRuntimeFactory owner,
            Guid workspaceId,
            ulong sessionEpoch) : IWorkspaceRuntime
        {
            public Guid WorkspaceId { get; } = workspaceId;
            public ulong SessionEpoch { get; } = sessionEpoch;
            public Task StartAsync(
                WorkspaceOpenMode mode,
                CancellationToken cancellationToken)
                => Task.CompletedTask;
            public Task VerifyAsync(CancellationToken cancellationToken)
                => Task.CompletedTask;
            public Task DrainAsync(CancellationToken cancellationToken)
                => owner.FailDrain
                    ? throw new InvalidOperationException(
                        "injected drain failure")
                    : Task.CompletedTask;
            public Task StopAsync(CancellationToken cancellationToken)
                => Task.CompletedTask;
            public ValueTask DisposeAsync() => ValueTask.CompletedTask;
        }
    }
}
