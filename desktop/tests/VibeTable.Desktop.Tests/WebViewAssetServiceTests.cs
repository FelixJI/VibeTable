using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WebViewAssetServiceTests
{
    [TestMethod]
    public void ResolveWebGridFolder_UsesPublishedResourcesLayout()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-web-assets-" + Guid.NewGuid().ToString("N"));
        string webGrid = Path.Combine(root, "resources", "web-grid");
        Directory.CreateDirectory(webGrid);
        try
        {
            Assert.AreEqual(
                webGrid,
                WebViewAssetService.ResolveWebGridFolder(root));
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public void AppUri_TargetsBundledIndexFile_NotVirtualDirectory()
    {
        var uriText = (string)typeof(WebViewAssetService)
            .GetField(nameof(WebViewAssetService.AppOrigin))!
            .GetRawConstantValue()!;

        var uri = new Uri(uriText);

        Assert.AreEqual("https", uri.Scheme);
        Assert.AreEqual(WebViewAssetService.AppHostName, uri.Host);
        Assert.AreEqual("/index.html", uri.AbsolutePath,
            "Virtual-host folder mappings do not provide a web-server directory index; navigate to the bundled entry file explicitly.");
    }
}
