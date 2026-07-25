using System.IO.Compression;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PluginResourceHostTests
{
    private const string PackageHash =
        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

    [TestMethod]
    public void PackageRevisionUsesImmutableHashDerivedIndependentOrigin()
    {
        var revision = PluginPackageRevision.Create(@"C:\plugins\clean", PackageHash);

        Assert.AreEqual(
            "https://0123456789abcdef0123456789abcdef.0123456789abcdef0123456789abcdef.plugins.vibetable.local",
            revision.Origin.AbsoluteUri.TrimEnd('/'));
        Assert.AreEqual(
            "0123456789abcdef0123456789abcdef.0123456789abcdef0123456789abcdef.plugins.vibetable.local",
            revision.VirtualHostName);
    }

    [TestMethod]
    public void PackageRevisionAcceptsContractSha256Digest()
    {
        var revision = PluginPackageRevision.Create(
            @"C:\plugins\clean",
            $"sha256:{PackageHash}");

        Assert.AreEqual(PackageHash, revision.PackageHash);
        Assert.AreEqual(
            "0123456789abcdef0123456789abcdef.0123456789abcdef0123456789abcdef.plugins.vibetable.local",
            revision.VirtualHostName);
    }

    [TestMethod]
    public void NormalizeResourcePathRejectsTraversalAbsoluteAndDoubleEncoding()
    {
        Assert.AreEqual("ui/index.html", PluginResourceHost.NormalizePackagePath("ui/index.html"));

        foreach (var path in new[]
        {
            "../secret.txt",
            "ui/../../secret.txt",
            "/windows/system.ini",
            @"C:\windows\system.ini",
            "ui\\index.html",
            "ui//index.html",
            "ui/%252e%252e/secret.txt",
            "https://example.test/a.js",
        })
        {
            Assert.Throws<PluginResourcePolicyException>(
                () => PluginResourceHost.NormalizePackagePath(path),
                path);
        }
    }

    [TestMethod]
    public void ResponsePolicyPinsNoNetworkCspAndRejectsNetworkPrimitives()
    {
        var revision = PluginPackageRevision.Create(@"C:\plugins\clean", PackageHash);

        Assert.IsTrue(PluginResourceHost.ContentSecurityPolicy.Contains("default-src 'none'", StringComparison.Ordinal));
        Assert.IsTrue(PluginResourceHost.ContentSecurityPolicy.Contains("connect-src 'none'", StringComparison.Ordinal));
        Assert.IsTrue(PluginResourceHost.ContentSecurityPolicy.Contains("form-action 'none'", StringComparison.Ordinal));
        Assert.IsTrue(PluginResourceHost.ContentSecurityPolicy.Contains(
            "frame-ancestors https://app.vibetable.local",
            StringComparison.Ordinal));
        Assert.IsFalse(PluginResourceHost.ContentSecurityPolicy.Contains(
            "frame-ancestors 'none'",
            StringComparison.Ordinal));

        var sameOrigin = new Uri(revision.Origin, "/ui/app.js");
        Assert.IsTrue(PluginResourceHost.IsRequestAllowed(
            revision, sameOrigin, PluginResourceRequestKind.Script));
        foreach (var kind in new[]
        {
            PluginResourceRequestKind.Fetch,
            PluginResourceRequestKind.XmlHttpRequest,
            PluginResourceRequestKind.WebSocket,
            PluginResourceRequestKind.EventSource,
            PluginResourceRequestKind.RemoteImport,
            PluginResourceRequestKind.ServiceWorker,
        })
        {
            Assert.IsFalse(PluginResourceHost.IsRequestAllowed(revision, sameOrigin, kind), kind.ToString());
        }
        Assert.IsFalse(PluginResourceHost.IsRequestAllowed(
            revision, new Uri("https://cdn.example.test/app.js"), PluginResourceRequestKind.Script));
    }

    [TestMethod]
    public void OpenReadsExactResourceFromVtpluginArchiveWithSecurityHeaders()
    {
        string archivePath = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-resource-{Guid.NewGuid():N}.vtplugin");
        try
        {
            using (var archive = ZipFile.Open(archivePath, ZipArchiveMode.Create))
            {
                var entry = archive.CreateEntry("ui/index.html");
                using var writer = new StreamWriter(entry.Open());
                writer.Write("<main>local plugin</main>");
            }

            var revision = PluginPackageRevision.Create(archivePath, PackageHash);
            using var response = new PluginResourceHost().Open(
                revision,
                "ui/index.html",
                PluginResourceRequestKind.Document);
            using var reader = new StreamReader(response.Content);

            Assert.AreEqual("<main>local plugin</main>", reader.ReadToEnd());
            Assert.AreEqual("text/html; charset=utf-8", response.ContentType);
            Assert.AreEqual(
                PluginResourceHost.ContentSecurityPolicy,
                response.Headers["Content-Security-Policy"]);
        }
        finally
        {
            File.Delete(archivePath);
        }
    }
}
