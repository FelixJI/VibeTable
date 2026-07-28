using VibeTable.Desktop.Services;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ShellBootstrapTests
{
    [TestMethod]
    public async Task EmptyRegistryStillLoadsGlobalShell()
    {
        using var fixture = new ShellFixture();

        ShellBootstrapResult result = await fixture.Bootstrap.StartAsync();

        Assert.IsTrue(result.RegistryAvailable);
        Assert.HasCount(0, result.Workspaces);
        Assert.AreEqual(1, fixture.WebView.LoadCalls);
    }

    [TestMethod]
    public async Task CorruptRegistryDoesNotBlockGlobalShell()
    {
        using var fixture = new ShellFixture();
        Directory.CreateDirectory(
            Path.Combine(fixture.Root, "VibeTable", "shell"));
        File.WriteAllText(
            Path.Combine(
                fixture.Root,
                "VibeTable",
                "shell",
                "workspace-registry-v2.json"),
            "{not-json");

        ShellBootstrapResult result = await fixture.Bootstrap.StartAsync();

        Assert.IsFalse(result.RegistryAvailable);
        Assert.AreEqual("workspace.registry_corrupt", result.RegistryErrorCode);
        Assert.HasCount(0, result.Workspaces);
        Assert.AreEqual(1, fixture.WebView.LoadCalls);
    }

    [TestMethod]
    public async Task OfflineWorkspaceEntryDoesNotRequireManifestOrKeyToLoadShell()
    {
        using var fixture = new ShellFixture();
        var layout = WorkspaceLayout.Create(
            Path.Combine(fixture.Root, "missing-volume"),
            "离线工作区",
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.Convenient);
        Guid workspaceId = layout.Manifest.WorkspaceId;
        fixture.Registry.Register(new WorkspaceRegistryEntryV2
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = workspaceId,
            DisplayName = "离线工作区",
            SelectedRoot = Path.Combine(fixture.Root, "missing-volume"),
            ActivityRoot = null,
            StorageKind = WorkspaceStorageKind.Removable,
            CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
            LastOpenedAt = DateTimeOffset.UtcNow,
            LastKnownHealth = WorkspaceHealth.Offline,
            LastSnapshotAt = null,
            LastSyncAt = null,
            PendingSync = true,
        });
        Directory.Delete(layout.SelectedRoot, recursive: true);

        ShellBootstrapResult result = await fixture.Bootstrap.StartAsync();

        Assert.IsTrue(result.RegistryAvailable);
        Assert.AreEqual(workspaceId, result.Workspaces.Single().WorkspaceId);
        Assert.AreEqual(WorkspaceHealth.Offline, result.Workspaces.Single().LastKnownHealth);
        Assert.AreEqual(1, fixture.WebView.LoadCalls);
    }

    [TestMethod]
    public async Task ConcurrentStartsShareOneShellLoad()
    {
        using var fixture = new ShellFixture();

        await Task.WhenAll(
            fixture.Bootstrap.StartAsync(),
            fixture.Bootstrap.StartAsync(),
            fixture.Bootstrap.StartAsync());

        Assert.AreEqual(1, fixture.WebView.LoadCalls);
    }

    private sealed class ShellFixture : IDisposable
    {
        public ShellFixture()
        {
            Root = Path.Combine(
                Path.GetTempPath(),
                "vibetable-shell-" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(Root);
            WebView = new CountingWebViewBridge();
            Registry = new WorkspaceRegistry(Root);
            Bootstrap = new ShellBootstrap(Registry, WebView);
        }

        public string Root { get; }
        public WorkspaceRegistry Registry { get; }
        public CountingWebViewBridge WebView { get; }
        public ShellBootstrap Bootstrap { get; }

        public void Dispose()
        {
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

    private sealed class CountingWebViewBridge : IWebViewBridge
    {
        public int LoadCalls { get; private set; }

        public Task LoadAsync(CancellationToken cancellationToken)
        {
            LoadCalls++;
            return Task.CompletedTask;
        }
    }
}
