using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WebViewNavigationPolicyTests
{
    [TestMethod]
    public void AppPolicy_AllowsOnlyVirtualAppOrigin()
    {
        Assert.IsTrue(WebViewNavigationPolicy.IsAppNavigation(
            "https://app.vibetable.local/index.html#/tables"));
        Assert.IsFalse(WebViewNavigationPolicy.IsAppNavigation(
            "http://app.vibetable.local/index.html"));
        Assert.IsFalse(WebViewNavigationPolicy.IsAppNavigation(
            "https://app.vibetable.local:444/index.html"));
        Assert.IsFalse(WebViewNavigationPolicy.IsAppNavigation(
            "http://127.0.0.1:49152/admin/"));
        Assert.IsFalse(WebViewNavigationPolicy.IsAppNavigation("data:text/html,hello"));
    }

    [TestMethod]
    public void AdminPolicy_AllowsOnlyConfiguredDirectusOrigin()
    {
        const string directus = "http://127.0.0.1:49152";

        Assert.IsTrue(WebViewNavigationPolicy.IsAdminNavigation(
            "http://127.0.0.1:49152/admin/content/orders?layout=tabular", directus));
        Assert.IsFalse(WebViewNavigationPolicy.IsAdminNavigation(
            "http://127.0.0.1:49153/admin/", directus));
        Assert.IsFalse(WebViewNavigationPolicy.IsAdminNavigation(
            "http://localhost:49152/admin/", directus));
        Assert.IsFalse(WebViewNavigationPolicy.IsAdminNavigation(
            "https://app.vibetable.local/index.html", directus));
        Assert.IsFalse(WebViewNavigationPolicy.IsAdminNavigation(
            "file:///C:/temp/admin.html", directus));
    }

    [TestMethod]
    public void NewWindowPolicy_KeepsTrustedLinksAndBlocksUnsafeSchemes()
    {
        Assert.AreEqual(WebViewLinkDisposition.CurrentView,
            WebViewNavigationPolicy.ClassifyAppNewWindow(
                "https://app.vibetable.local/help"));
        Assert.AreEqual(WebViewLinkDisposition.ExternalBrowser,
            WebViewNavigationPolicy.ClassifyAppNewWindow("https://directus.io/docs"));
        Assert.AreEqual(WebViewLinkDisposition.Block,
            WebViewNavigationPolicy.ClassifyAppNewWindow("file:///C:/secret.txt"));
        Assert.AreEqual(WebViewLinkDisposition.Block,
            WebViewNavigationPolicy.ClassifyAppNewWindow("not a uri"));
    }
}
