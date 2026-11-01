using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductWebViewEnvironmentOptionsTests
{
    [TestMethod]
    public void BuildEnvironmentOptions_DisablesBackgroundNetworkFeatures()
    {
        var options = ProductWebViewBridge.BuildEnvironmentOptions();

        StringAssert.Contains(options.AdditionalBrowserArguments, "--disable-background-networking");
        StringAssert.Contains(options.AdditionalBrowserArguments, "--disable-component-update");
        StringAssert.Contains(options.AdditionalBrowserArguments, "--disable-domain-reliability");
        StringAssert.Contains(options.AdditionalBrowserArguments, "--disable-sync");
        StringAssert.Contains(options.AdditionalBrowserArguments, "--no-first-run");
        StringAssert.Contains(
            options.AdditionalBrowserArguments,
            "--proxy-server=http://127.0.0.1:9");
        StringAssert.Contains(
            options.AdditionalBrowserArguments,
            "MAP * 127.0.0.1");
        StringAssert.Contains(options.AdditionalBrowserArguments, "--disable-quic");
        Assert.IsFalse(
            options.AdditionalBrowserArguments.Contains(
                "--inprivate",
                StringComparison.Ordinal));
        StringAssert.Contains(
            options.AdditionalBrowserArguments,
            "msEdgeHubAppsAutoOpenOutlookV2");
        StringAssert.Contains(
            options.AdditionalBrowserArguments,
            "msEdgeOSAccountInfoSubstrate");
        StringAssert.Contains(
            options.AdditionalBrowserArguments,
            "msM365LinksImplicitSignin");
        Assert.IsFalse(options.AllowSingleSignOnUsingOSPrimaryAccount);
        Assert.IsFalse(options.AreBrowserExtensionsEnabled);
        Assert.IsFalse(options.EnableTrackingPrevention);
        Assert.IsTrue(options.IsCustomCrashReportingEnabled);
    }

    [TestMethod]
    public void BuildEnvironmentOptions_AppendsTestOnlyArguments()
    {
        var options = ProductWebViewBridge.BuildEnvironmentOptions(
            "--remote-debugging-port=9222 --disable-gpu",
            testMode: true);

        StringAssert.Contains(options.AdditionalBrowserArguments, "--disable-background-networking");
        Assert.IsTrue(options.AdditionalBrowserArguments.EndsWith(
            "--remote-debugging-port=9222 --disable-gpu",
            StringComparison.Ordinal));
    }

    [TestMethod]
    public void BuildEnvironmentOptions_RejectsOverridesOutsideTestMode()
    {
        Assert.ThrowsExactly<InvalidOperationException>(() =>
            ProductWebViewBridge.BuildEnvironmentOptions(
                "--remote-debugging-port=9222"));
    }

    [TestMethod]
    [DataRow("--disable-web-security")]
    [DataRow("--proxy-server=https://attacker.invalid")]
    [DataRow("--remote-debugging-port=0")]
    [DataRow("--remote-debugging-port=70000")]
    [DataRow("--remote-debugging-port=9222 --user-data-dir=C:\\stolen")]
    public void BuildEnvironmentOptions_RejectsDangerousTestModeSwitches(
        string arguments)
    {
        Assert.ThrowsExactly<InvalidOperationException>(() =>
            ProductWebViewBridge.BuildEnvironmentOptions(
                arguments,
                testMode: true));
    }

    [TestMethod]
    public void ResolveUserDataFolder_FallsBackToTempOnlyWithoutARoot()
    {
        string first = ProductWebViewBridge.ResolveUserDataFolder(
            isolatedRoot: null,
            processId: 101);
        string second = ProductWebViewBridge.ResolveUserDataFolder(
            isolatedRoot: null,
            processId: 202);

        string expected = Path.Combine(
            Path.GetTempPath(),
            "VibeTable",
            "webview2-udd");
        Assert.AreEqual(expected, first);
        Assert.AreEqual(expected, second);
        Assert.IsFalse(first.Contains("p101", StringComparison.Ordinal));
        Assert.IsFalse(second.Contains("p202", StringComparison.Ordinal));
    }

    [TestMethod]
    public void ResolveUserDataFolder_IsolatesOnlyAnExplicitE2eRootByProcess()
    {
        string e2eRoot = Path.Combine(
            Path.GetTempPath(),
            "vibetable-e2e-profile-root");

        string first = ProductWebViewBridge.ResolveUserDataFolder(
            e2eRoot,
            processId: 101);
        string second = ProductWebViewBridge.ResolveUserDataFolder(
            e2eRoot,
            processId: 202);

        Assert.AreEqual(
            Path.Combine(Path.GetFullPath(e2eRoot), "p101"),
            first);
        Assert.AreEqual(
            Path.Combine(Path.GetFullPath(e2eRoot), "p202"),
            second);
        Assert.AreNotEqual(first, second);
    }

    [TestMethod]
    public void ResolveUserDataFolder_ReusesExplicitDataRootProfile()
    {
        string dataRootProfile = Path.Combine(
            Path.GetTempPath(),
            "vibetable-data-root-profile");

        string first = ProductWebViewBridge.ResolveUserDataFolder(
            dataRootProfile,
            processId: 101,
            stableIsolatedRoot: true);
        string second = ProductWebViewBridge.ResolveUserDataFolder(
            dataRootProfile,
            processId: 202,
            stableIsolatedRoot: true);

        Assert.AreEqual(Path.GetFullPath(dataRootProfile), first);
        Assert.AreEqual(first, second);
    }
}
