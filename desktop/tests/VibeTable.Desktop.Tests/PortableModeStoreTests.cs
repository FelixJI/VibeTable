using VibeTable.Desktop.Services;
using VibeTable.Infrastructure;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PortableModeStoreTests
{
    private string _root = null!;
    private string _baseDirectory = null!;
    private string _localAppData = null!;

    [TestInitialize]
    public void Initialize()
    {
        _root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-portable-store-" + Guid.NewGuid().ToString("N"));
        _baseDirectory = Path.Combine(_root, "install");
        _localAppData = Path.Combine(_root, "local");
        Directory.CreateDirectory(_baseDirectory);
        Directory.CreateDirectory(_localAppData);
    }

    [TestCleanup]
    public void Cleanup()
    {
        Directory.Delete(_root, recursive: true);
    }

    [TestMethod]
    public void EnablingArmsMigrationBeforeCreatingTheMarker()
    {
        var store = new MarkerPortableModeStore(_baseDirectory, _localAppData);
        Assert.IsFalse(store.IsEnabled());

        store.SetEnabled(true);

        string portableRoot = LaunchPaths.ResolvePortableDataRoot(_baseDirectory);
        Assert.IsTrue(store.IsEnabled());
        Assert.IsTrue(File.Exists(Path.Combine(
            portableRoot,
            LaunchPaths.PortableModeMarkerFileName)));
        Assert.IsTrue(File.Exists(Path.Combine(
            LaunchPaths.ResolveStandardDataRoot(_localAppData),
            LaunchPaths.ModeSwitchPendingFileName)),
            "The outgoing standard root records the migration intent.");
    }

    [TestMethod]
    public void DisablingArmsMigrationThenRemovesTheMarker()
    {
        var store = new MarkerPortableModeStore(_baseDirectory, _localAppData);
        store.SetEnabled(true);

        store.SetEnabled(false);

        Assert.IsFalse(store.IsEnabled());
        Assert.IsTrue(File.Exists(Path.Combine(
            LaunchPaths.ResolvePortableDataRoot(_baseDirectory),
            LaunchPaths.ModeSwitchPendingFileName)),
            "The outgoing portable root records the migration back intent.");
    }

    [TestMethod]
    public void RepeatedSetEnabledIsANoOp()
    {
        var store = new MarkerPortableModeStore(_baseDirectory, _localAppData);
        store.SetEnabled(true);
        string pendingPath = Path.Combine(
            LaunchPaths.ResolveStandardDataRoot(_localAppData),
            LaunchPaths.ModeSwitchPendingFileName);
        File.Delete(pendingPath);

        store.SetEnabled(true);

        Assert.IsTrue(store.IsEnabled());
        Assert.IsFalse(File.Exists(pendingPath),
            "A no-op switch must not schedule another migration.");
    }

    [TestMethod]
    public void InMemoryStoreNeverTouchesTheFilesystem()
    {
        var store = new InMemoryPortableModeStore();
        Assert.IsFalse(store.IsEnabled());

        store.SetEnabled(true);

        Assert.IsTrue(store.IsEnabled());
        Assert.IsFalse(LaunchPaths.IsPortableMode(_baseDirectory));
    }
}
