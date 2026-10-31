using VibeTable.Desktop.Services;
using VibeTable.Infrastructure;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductDataRootTests
{
    private string _root = null!;

    [TestInitialize]
    public void Initialize()
    {
        _root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-product-data-root-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(_root);
    }

    [TestCleanup]
    public void Cleanup()
    {
        Directory.Delete(_root, recursive: true);
    }

    [TestMethod]
    public void TestModeReadinessRootWinsOverEverything()
    {
        string readiness = Path.Combine(_root, "readiness");
        var options = new HostStartupOptions
        {
            TestMode = true,
            ReadinessDir = readiness,
            DevelopmentDataRoot = Path.Combine(_root, "dev"),
        };

        Assert.AreEqual(
            Path.Combine(Path.GetFullPath(readiness), "local-data"),
            ProductDataRoot.Resolve(options));
    }

    [TestMethod]
    public void DevelopmentRootWinsWhenNotTesting()
    {
        var options = new HostStartupOptions
        {
            DevelopmentDataRoot = Path.Combine(_root, "dev"),
        };

        Assert.AreEqual(
            Path.GetFullPath(Path.Combine(_root, "dev")),
            ProductDataRoot.Resolve(options));
    }

    [TestMethod]
    public void PlainLaunchUsesMarkerOrStandardRoot()
    {
        // The marker cannot be probed at the real AppContext.BaseDirectory in
        // tests; the marker branch is covered by LaunchPathsTests, so this
        // locks the standard default plus the logs subdirectory contract.
        string localAppData = Path.Combine(_root, "local");
        string standard = LaunchPaths.ResolveStandardDataRoot(localAppData);

        Assert.AreEqual(
            standard,
            LaunchPaths.ResolveProductDataRoot(_root, localAppData));
        Assert.AreEqual(
            Path.Combine(Path.Combine(_root, "dev"), "logs"),
            ProductDataRoot.LogsDirectory(
                new HostStartupOptions { DevelopmentDataRoot = Path.Combine(_root, "dev") }));
    }
}
