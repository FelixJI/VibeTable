using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WebViewAssetServiceTests
{
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
