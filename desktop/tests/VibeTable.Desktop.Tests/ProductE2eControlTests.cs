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
    public void HostStartupOptions_ConsumesExactUpdatedHealthTimeoutHoldOnce()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-update-health-timeout-{Guid.NewGuid():N}");
        string readiness = Path.Combine(root, "self-update-readiness");
        string controls = Path.Combine(root, "self-update-updated-controls");
        Directory.CreateDirectory(readiness);
        Directory.CreateDirectory(controls);
        string request = Path.Combine(
            controls,
            "self-update-health-timeout-hold.request");
        File.WriteAllText(request, string.Empty);

        try
        {
            HostStartupOptions options = HostStartupOptions.Parse([
                "--self-update-smoke",
                "--test-mode",
                "--readiness-dir", readiness,
                "--e2e-controls-dir", controls,
            ]);

            Assert.IsTrue(options.TryConsumeSelfUpdateHealthTimeoutHold());
            Assert.IsFalse(File.Exists(request));
            Assert.IsFalse(options.TryConsumeSelfUpdateHealthTimeoutHold());
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public void HostStartupOptions_LeavesHealthTimeoutHoldDormantUnlessEnvelopeIsExact()
    {
        var cases = new[]
        {
            ("no-smoke", false, true, "self-update-readiness",
                "self-update-updated-controls", true),
            ("no-test-mode", true, false, "self-update-readiness",
                "self-update-updated-controls", true),
            ("wrong-readiness", true, true, "other-readiness",
                "self-update-updated-controls", true),
            ("wrong-controls", true, true, "self-update-readiness",
                "other-controls", true),
            ("different-parents", true, true, "self-update-readiness",
                "self-update-updated-controls", false),
        };
        string root = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-update-health-timeout-dormant-{Guid.NewGuid():N}");
        try
        {
            foreach ((string name, bool smoke, bool testMode, string readinessName,
                string controlsName, bool sameParent) in cases)
            {
                string caseRoot = Path.Combine(root, name);
                string readiness = Path.Combine(caseRoot, readinessName);
                string controls = Path.Combine(
                    sameParent ? caseRoot : Path.Combine(caseRoot, "separate"),
                    controlsName);
                Directory.CreateDirectory(readiness);
                Directory.CreateDirectory(controls);
                string request = Path.Combine(
                    controls,
                    "self-update-health-timeout-hold.request");
                File.WriteAllText(request, string.Empty);
                var args = new List<string>();
                if (smoke)
                {
                    args.Add("--self-update-smoke");
                }
                if (testMode)
                {
                    args.Add("--test-mode");
                }
                args.AddRange([
                    "--readiness-dir", readiness,
                    "--e2e-controls-dir", controls,
                ]);

                HostStartupOptions options = HostStartupOptions.Parse(args);

                Assert.IsFalse(
                    options.TryConsumeSelfUpdateHealthTimeoutHold(),
                    name);
                Assert.IsTrue(File.Exists(request), name);
            }

            string exactRoot = Path.Combine(root, "missing-option");
            string exactReadiness = Path.Combine(exactRoot, "self-update-readiness");
            string exactControls = Path.Combine(exactRoot, "self-update-updated-controls");
            Directory.CreateDirectory(exactReadiness);
            Directory.CreateDirectory(exactControls);
            string exactRequest = Path.Combine(
                exactControls,
                "self-update-health-timeout-hold.request");
            File.WriteAllText(exactRequest, string.Empty);
            HostStartupOptions missingReadiness = HostStartupOptions.Parse([
                "--self-update-smoke",
                "--test-mode",
                "--e2e-controls-dir", exactControls,
            ]);
            HostStartupOptions missingControls = HostStartupOptions.Parse([
                "--self-update-smoke",
                "--test-mode",
                "--readiness-dir", exactReadiness,
            ]);

            Assert.IsFalse(missingReadiness.TryConsumeSelfUpdateHealthTimeoutHold());
            Assert.IsFalse(missingControls.TryConsumeSelfUpdateHealthTimeoutHold());
            Assert.IsTrue(File.Exists(exactRequest));

            File.Delete(exactRequest);
            HostStartupOptions missingRequest = HostStartupOptions.Parse([
                "--self-update-smoke",
                "--test-mode",
                "--readiness-dir", exactReadiness,
                "--e2e-controls-dir", exactControls,
            ]);
            Assert.IsFalse(missingRequest.TryConsumeSelfUpdateHealthTimeoutHold());
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public void HostStartupOptions_MoveArmsHoldEvenWhenClaimCleanupFails()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-update-health-claim-{Guid.NewGuid():N}");
        string readiness = Path.Combine(root, "self-update-readiness");
        string controls = Path.Combine(root, "self-update-updated-controls");
        Directory.CreateDirectory(readiness);
        Directory.CreateDirectory(controls);
        string request = Path.Combine(
            controls,
            "self-update-health-timeout-hold.request");
        File.WriteAllText(request, string.Empty);
        string staleClaim = request + ".claimed-stale";
        File.WriteAllText(staleClaim, string.Empty);
        string? claimed = null;
        try
        {
            HostStartupOptions options = HostStartupOptions.Parse([
                "--self-update-smoke",
                "--test-mode",
                "--readiness-dir", readiness,
                "--e2e-controls-dir", controls,
            ]);

            bool armed = options.TryConsumeSelfUpdateHealthTimeoutHold(
                cleanupClaim: path =>
                {
                    claimed = path;
                    throw new IOException("simulated claim cleanup failure");
                });

            Assert.IsTrue(armed);
            Assert.IsFalse(File.Exists(request));
            Assert.IsNotNull(claimed);
            Assert.IsTrue(File.Exists(claimed));
            Assert.IsTrue(File.Exists(staleClaim));
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public void HostStartupOptions_PathGuardRejectionLeavesHoldDormant()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-update-health-guard-{Guid.NewGuid():N}");
        string readiness = Path.Combine(root, "self-update-readiness");
        string controls = Path.Combine(root, "self-update-updated-controls");
        Directory.CreateDirectory(readiness);
        Directory.CreateDirectory(controls);
        string request = Path.Combine(
            controls,
            "self-update-health-timeout-hold.request");
        File.WriteAllText(request, string.Empty);
        try
        {
            HostStartupOptions options = HostStartupOptions.Parse([
                "--self-update-smoke",
                "--test-mode",
                "--readiness-dir", readiness,
                "--e2e-controls-dir", controls,
            ]);

            bool armed = options.TryConsumeSelfUpdateHealthTimeoutHold(
                pathGuard: (_, _, _) => throw new ReleaseUpdateException(
                    "simulated reparse chain",
                    "UPDATE_REPARSE_POINT_REJECTED"));

            Assert.IsFalse(armed);
            Assert.IsTrue(File.Exists(request));
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    [DataRow("readiness")]
    [DataRow("controls")]
    [DataRow("request")]
    public void HostStartupOptions_RejectsReparsePointInHealthHoldChain(string segment)
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-update-health-reparse-{segment}-{Guid.NewGuid():N}");
        string outside = Path.Combine(root, "outside");
        string readiness = Path.Combine(root, "self-update-readiness");
        string controls = Path.Combine(root, "self-update-updated-controls");
        string request = Path.Combine(
            controls,
            "self-update-health-timeout-hold.request");
        Directory.CreateDirectory(outside);
        string reparsePath;
        if (segment == "readiness")
        {
            Directory.CreateDirectory(controls);
            File.WriteAllText(request, string.Empty);
            reparsePath = readiness;
        }
        else if (segment == "controls")
        {
            Directory.CreateDirectory(readiness);
            File.WriteAllText(Path.Combine(outside, Path.GetFileName(request)), string.Empty);
            reparsePath = controls;
        }
        else
        {
            Directory.CreateDirectory(readiness);
            Directory.CreateDirectory(controls);
            reparsePath = request;
        }
        if (!TryCreateJunction(reparsePath, outside))
        {
            Directory.Delete(root, recursive: true);
            Assert.Inconclusive("当前 Windows 环境无法创建目录 junction。");
        }
        try
        {
            HostStartupOptions options = HostStartupOptions.Parse([
                "--self-update-smoke",
                "--test-mode",
                "--readiness-dir", readiness,
                "--e2e-controls-dir", controls,
            ]);

            Assert.IsFalse(options.TryConsumeSelfUpdateHealthTimeoutHold());
            Assert.IsTrue(File.Exists(request) || Directory.Exists(request));
        }
        finally
        {
            Directory.Delete(reparsePath);
            Directory.Delete(root, recursive: true);
        }
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

    private static bool TryCreateJunction(string junction, string target)
    {
        string command = Environment.GetEnvironmentVariable("COMSPEC") ?? "cmd.exe";
        var start = new System.Diagnostics.ProcessStartInfo
        {
            FileName = command,
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
        };
        foreach (string argument in new[] { "/d", "/c", "mklink", "/J", junction, target })
        {
            start.ArgumentList.Add(argument);
        }
        using System.Diagnostics.Process? process = System.Diagnostics.Process.Start(start);
        if (process is null)
        {
            return false;
        }
        process.WaitForExit();
        return process.ExitCode == 0;
    }
}
