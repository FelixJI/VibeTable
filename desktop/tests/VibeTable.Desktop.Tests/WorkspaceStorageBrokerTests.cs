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
            foreach (string action in new[]
                     {
                         "convertTopology",
                         "releaseActivityCache",
                     })
            {
                WorkspaceRegistryException error =
                    await Assert.ThrowsExactlyAsync<
                        WorkspaceRegistryException>(() =>
                        broker.PreviewAsync(
                            JsonSerializer.SerializeToElement(new
                            {
                                workspaceId = Guid.NewGuid().ToString("D"),
                                action,
                                targetMode = (string?)null,
                                selectedRootGrant = (string?)null,
                            }),
                            selectedRoot: null,
                            CancellationToken.None));
                Assert.AreEqual(
                    "workspace.storage_verification_unavailable",
                    error.Code);
            }
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
