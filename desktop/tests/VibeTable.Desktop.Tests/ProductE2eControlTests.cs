using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductE2eControlTests
{
    [TestMethod]
    public void HostShutdownBudget_CoversSequentialWorkspaceProcessStops()
    {
        TimeSpan supervisorStops =
            BackendLaunchOptions.DefaultStopTimeout
            + PocketBaseLaunchOptions.DefaultStopTimeout;

        Assert.IsTrue(MainWindow.WorkspaceSessionShutdownTimeout > supervisorStops);
        Assert.IsTrue(MainWindow.WorkspaceSessionShutdownTimeout < TimeSpan.FromSeconds(30));
    }

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
    public void HostStartupOptions_RecognizesAutoStartFlag()
    {
        Assert.IsTrue(HostStartupOptions.Parse(["--autostart"]).AutoStart);

        HostStartupOptions combined = HostStartupOptions.Parse(
            ["--autostart", "--dev-data-root", @"C:\vibetable-dev"]);
        Assert.IsTrue(combined.AutoStart);
        Assert.AreEqual(@"C:\vibetable-dev", combined.DevelopmentDataRoot);

        Assert.IsFalse(HostStartupOptions.Parse(Array.Empty<string>()).AutoStart);
        Assert.IsFalse(HostStartupOptions.Parse(["--test-mode"]).AutoStart);
    }

    [TestMethod]
    public void HostStartupOptions_AcceptsTrayLifecycleOnlyInTestMode()
    {
        HostStartupOptions testMode = HostStartupOptions.Parse(
            ["--test-mode", "--test-mode-tray-lifecycle"]);
        HostStartupOptions production = HostStartupOptions.Parse(
            ["--test-mode-tray-lifecycle"]);

        Assert.IsTrue(testMode.TestModeTrayLifecycle);
        Assert.IsFalse(production.TestModeTrayLifecycle);
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

    [TestMethod]
    public async Task DocumentPicker_ReadsOnlyFixedControlFile()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-document-e2e-control-{Guid.NewGuid():N}");
        string controls = Path.Combine(root, "controls");
        string document = Path.Combine(root, "diff-source.txt");
        Directory.CreateDirectory(controls);
        await File.WriteAllTextAsync(document, "historical content\n");
        await File.WriteAllTextAsync(
            Path.Combine(controls, "document-source.txt"),
            document);

        try
        {
            var picker = new TestModeLocalDocumentFilePicker(controls);

            string? selected = await picker.PickFileAsync(
                DocumentFilePickPurpose.Import,
                suggestedFileName: null,
                CancellationToken.None);

            Assert.AreEqual(Path.GetFullPath(document), selected);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public void WorkspacePathPicker_CompositionUsesControlsOnlyInTestMode()
    {
        HostStartupOptions testMode = HostStartupOptions.Parse(
            ["--test-mode", "--e2e-controls-dir", @"C:\controls"]);
        HostStartupOptions production = HostStartupOptions.Parse(
            ["--e2e-controls-dir", @"C:\controls"]);

        Assert.IsInstanceOfType<TestModeWorkspacePathPicker>(
            VibeTable.Desktop.MainWindow.CreateWorkspacePathPicker(testMode));
        Assert.IsInstanceOfType<WindowsWorkspacePathPicker>(
            VibeTable.Desktop.MainWindow.CreateWorkspacePathPicker(production));
    }
}
