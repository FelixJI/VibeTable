using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductE2eControlTests
{
    [TestMethod]
    public void HostStartupOptions_IgnoresControlDirectoryOutsideTestMode()
    {
        HostStartupOptions options = HostStartupOptions.Parse(
            ["--e2e-controls-dir", @"C:\controls"]);

        Assert.IsFalse(options.TestMode);
        Assert.IsNull(options.E2eControlsDir);
    }

    [TestMethod]
    public void HostStartupOptions_AcceptsControlDirectoryInTestMode()
    {
        HostStartupOptions options = HostStartupOptions.Parse(
            ["--test-mode", "--e2e-controls-dir", @"C:\controls"]);

        Assert.IsTrue(options.TestMode);
        Assert.AreEqual(@"C:\controls", options.E2eControlsDir);
    }

    [TestMethod]
    public void HostStartupOptions_ParsesExplicitDevelopmentDataRoot()
    {
        HostStartupOptions options = HostStartupOptions.Parse(
            ["--dev-data-root", @"C:\vibetable-dev"]);

        Assert.IsFalse(options.TestMode);
        Assert.AreEqual(
            @"C:\vibetable-dev",
            options.DevelopmentDataRoot);
    }

    [TestMethod]
    public async Task PluginPackagePicker_ReadsOnlyFixedControlFile()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-e2e-control-{Guid.NewGuid():N}");
        string controls = Path.Combine(root, "controls");
        string plugin = Path.Combine(root, "plugin");
        Directory.CreateDirectory(controls);
        Directory.CreateDirectory(plugin);
        await File.WriteAllTextAsync(
            Path.Combine(controls, "plugin-source.txt"),
            plugin);

        try
        {
            var picker = new TestModePluginPackageSourcePicker(controls);

            string? selected = await picker.PickAsync(
                PluginPackagePickKind.Folder,
                CancellationToken.None);

            Assert.AreEqual(Path.GetFullPath(plugin), selected);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }
}
