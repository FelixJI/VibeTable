using System.Text;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PluginWebViewResourceHostTests
{
    private const string PackageHash =
        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

    [TestMethod]
    public void InstalledRevisionServesCspProtectedResourcesAndBlocksPluginNetwork()
    {
        string root = CreatePackageFolder();
        try
        {
            var surfaces = new PluginSurfaceSessionManager();
            using var host = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            var revision = PluginPackageRevision.Create(root, PackageHash);
            host.RegisterInstalled("project-1", "com.acme.clean", revision);
            var surface = host.OpenSurface("project-1", "com.acme.clean", "ui/index.html");
            Assert.IsTrue(PluginWebViewResourceHost.IsPluginUri(surface.DocumentUri));

            var resource = host.Resolve(new PluginWebResourceRequest(
                surface.DocumentUri,
                PluginResourceRequestKind.Document,
                new Uri("https://app.vibetable.local/")));
            Assert.IsNotNull(resource);
            Assert.AreEqual(200, resource!.StatusCode);
            Assert.IsTrue(resource.Resource!.Headers["Content-Security-Policy"]
                .Contains("connect-src 'none'", StringComparison.Ordinal));
            resource.Resource.Dispose();

            var network = host.Resolve(new PluginWebResourceRequest(
                new Uri("https://api.example.test/data"),
                PluginResourceRequestKind.Fetch,
                surface.DocumentUri));
            Assert.IsNotNull(network);
            Assert.AreEqual(403, network!.StatusCode);
            Assert.IsNull(network.Resource);

            var navigation = host.Resolve(new PluginWebResourceRequest(
                new Uri("https://app.vibetable.local/"),
                PluginResourceRequestKind.Navigation,
                surface.DocumentUri));
            Assert.IsNotNull(navigation);
            Assert.AreEqual(403, navigation!.StatusCode);

            Assert.IsTrue(host.CloseSurface(surface.SurfaceToken));
            Assert.IsFalse(surfaces.IsActive(surface.SurfaceToken));
            Assert.IsTrue(host.IsRegisteredUri(surface.DocumentUri));
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public void UninstallRevokesRevisionOriginAndItsSurfaceTokens()
    {
        string root = CreatePackageFolder();
        try
        {
            var surfaces = new PluginSurfaceSessionManager();
            using var host = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            var revision = PluginPackageRevision.Create(root, PackageHash);
            host.RegisterInstalled("project-1", "com.acme.clean", revision);
            var surface = host.OpenSurface("project-1", "com.acme.clean", "ui/index.html");

            Assert.IsTrue(host.UnregisterInstalled("project-1", "com.acme.clean"));

            Assert.IsFalse(host.IsRegisteredUri(surface.DocumentUri));
            Assert.IsFalse(surfaces.IsActive(surface.SurfaceToken));
            var unknown = host.Resolve(new PluginWebResourceRequest(
                surface.DocumentUri,
                PluginResourceRequestKind.Document,
                new Uri("https://app.vibetable.local/")));
            Assert.IsNotNull(unknown);
            Assert.AreEqual(403, unknown!.StatusCode);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public void RegisteringUpgradeRevokesOldSurfaceTokenAndUsesNewOrigin()
    {
        string root = CreatePackageFolder();
        try
        {
            var surfaces = new PluginSurfaceSessionManager();
            using var host = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            host.RegisterInstalled(
                "project-1", "com.acme.clean",
                PluginPackageRevision.Create(root, PackageHash));
            var oldSurface = host.OpenSurface("project-1", "com.acme.clean", "ui/index.html");
            string newHash = new('f', 64);

            host.RegisterInstalled(
                "project-1", "com.acme.clean",
                PluginPackageRevision.Create(root, newHash));
            var upgraded = host.OpenSurface("project-1", "com.acme.clean", "ui/index.html");

            Assert.IsFalse(surfaces.IsActive(oldSurface.SurfaceToken));
            Assert.AreNotEqual(oldSurface.SurfaceToken, upgraded.SurfaceToken);
            Assert.AreNotEqual(oldSurface.DocumentUri.Host, upgraded.DocumentUri.Host);
            Assert.IsFalse(host.IsRegisteredUri(oldSurface.DocumentUri));
            Assert.IsTrue(host.IsRegisteredUri(upgraded.DocumentUri));
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    private static string CreatePackageFolder()
    {
        string root = Path.Combine(Path.GetTempPath(), $"vibetable-plugin-{Guid.NewGuid():N}");
        Directory.CreateDirectory(Path.Combine(root, "ui"));
        File.WriteAllText(
            Path.Combine(root, "ui", "index.html"),
            "<!doctype html><title>Plugin</title>",
            Encoding.UTF8);
        return root;
    }
}
