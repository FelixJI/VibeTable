using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class GitHubPluginPackageSourceTests
{
    private const string PackageName = "weread-notes-dashboard-1.0.0.vtplugin";
    private const int MaxChecksumBytes = 4096;
    private const string GitHubDownload = "https://github.com/FelixJI/VibeTable-WeRead-Notes-Dashboard/releases/download/v1.0.0/"
        + PackageName;
    private const string CodebergAttachmentUuid = "6f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5";
    private const string CodebergPackage = "https://codeberg.org/felix/plugins/releases/download/v1.2.0/" + PackageName;
    private const string RepoDownload = "https://github.com/FelixJI/repo/releases/download/v1.0.0/" + PackageName;

    [TestMethod]
    [DataRow("FelixJI/VibeTable-WeRead-Notes-Dashboard", "GitHub", "FelixJI/VibeTable-WeRead-Notes-Dashboard", null, null)]
    [DataRow("  FelixJI/repo.git  ", "GitHub", "FelixJI/repo", null, null)]
    [DataRow("https://github.com/FelixJI/repo", "GitHub", "FelixJI/repo", null, null)]
    [DataRow("https://github.com/FelixJI/repo.git", "GitHub", "FelixJI/repo", null, null)]
    [DataRow("github.com/FelixJI/repo/releases", "GitHub", "FelixJI/repo", null, null)]
    [DataRow("https://github.com/FelixJI/repo/releases/latest", "GitHub", "FelixJI/repo", null, null)]
    [DataRow("FelixJI/repo/releases/tag/v1.0.0", "GitHub", "FelixJI/repo", "v1.0.0", null)]
    [DataRow("https://github.com/FelixJI/repo/releases/tag/v1.0.0", "GitHub", "FelixJI/repo", "v1.0.0", null)]
    [DataRow("https://github.com/FelixJI/repo/releases/download/v1.0.0/notes.vtplugin", "GitHub", "FelixJI/repo", "v1.0.0", "notes.vtplugin")]
    [DataRow("https://github.com/FelixJI/repo/releases/latest/download/notes.vtplugin", "GitHub", "FelixJI/repo", null, "notes.vtplugin")]
    [DataRow("https://github.com:443/FelixJI/repo/releases/tag/v1.0.0", "GitHub", "FelixJI/repo", "v1.0.0", null)]
    [DataRow("https://codeberg.org/felix/plugins", "Codeberg", "felix/plugins", null, null)]
    [DataRow("codeberg.org/felix.j/plugins.git/releases/tag/v1.2.0", "Codeberg", "felix.j/plugins", "v1.2.0", null)]
    [DataRow("https://codeberg.org/felix/plugins/releases/latest/download/plugin.vtplugin", "Codeberg", "felix/plugins", null, "plugin.vtplugin")]
    public void ParserAcceptsRepositoryReleaseAndAssetReferences(
        string value,
        string host,
        string slug,
        string? tag,
        string? assetName)
    {
        RemotePluginReleaseReference reference = RemotePluginReleaseReference.Parse(value);

        Assert.AreEqual(Enum.Parse<PluginReleaseHost>(host), reference.Host);
        Assert.AreEqual(slug, reference.Slug);
        Assert.AreEqual(tag, reference.Tag);
        Assert.AreEqual(assetName, reference.AssetName);
        Assert.IsNull(reference.Mirror);
    }

    [TestMethod]
    [DataRow("https://ghfast.top/https://github.com/FelixJI/repo/releases/tag/v1.0.0", "https://ghfast.top/", "v1.0.0")]
    [DataRow("https://ghproxy.net/https://github.com/FelixJI/repo", "https://ghproxy.net/", null)]
    [DataRow("HTTPS://GH-PROXY.COM/https://github.com/FelixJI/repo/releases/download/v1.0.0/notes.vtplugin", "https://gh-proxy.com/", "v1.0.0")]
    public void ParserKeepsThePastedGitHubMirrorOnTheReference(string value, string mirror, string? tag)
    {
        RemotePluginReleaseReference reference = RemotePluginReleaseReference.Parse(value);

        Assert.AreEqual(PluginReleaseHost.GitHub, reference.Host);
        Assert.AreEqual("FelixJI/repo", reference.Slug);
        Assert.AreEqual(tag, reference.Tag);
        Assert.AreEqual(mirror, reference.Mirror);
    }

    [TestMethod]
    [DataRow("")]
    [DataRow("   ")]
    [DataRow("owner")]
    [DataRow("owner/repo/branches/main")]
    [DataRow("https://example.com/owner/repo")]
    [DataRow("http://github.com/owner/repo")]
    [DataRow("https://gitlab.com/owner/repo")]
    [DataRow("https://github.com/owner/repo?tab=releases")]
    [DataRow("https://github.com/owner/repo#releases")]
    [DataRow("https://user@github.com/owner/repo")]
    [DataRow("https://github.com/-owner/repo")]
    [DataRow("https://github.com/owner/repo/releases/download/v1.0.0")]
    [DataRow("https://github.com/owner/repo/releases/download/v1.0.0/plugin.zip")]
    [DataRow("https://github.com/owner/repo/releases/download/v1.0.0/..")]
    [DataRow("https://github.com/owner/repo/releases/download/v1.0.0/a%2Fb.vtplugin")]
    [DataRow("https://github.com/owner/repo/releases/tag/")]
    [DataRow("https://github.com/owner/repo/releases/latest/download/")]
    [DataRow("https://github.com/owner/repo/releases/tag/v1.0.0/extra")]
    [DataRow("https://ghfast.top/https://ghproxy.net/https://github.com/owner/repo")]
    [DataRow("https://ghfast.top/https://codeberg.org/owner/repo")]
    [DataRow("https://ghproxy.net/codeberg.org/owner/repo")]
    [DataRow("https://ghfast.top")]
    [DataRow("https://ghfast.top//https://github.com/owner/repo")]
    [DataRow("https://github.com:8443/owner/repo")]
    [DataRow("https://codeberg.org:8443/owner/repo")]
    [DataRow("https://ghfast.top:8443/https://github.com/owner/repo")]
    public void ParserRejectsUnsupportedSources(string value)
    {
        GitHubPluginSourceException exception = Assert.ThrowsExactly<GitHubPluginSourceException>(
            () => RemotePluginReleaseReference.Parse(value));

        Assert.AreEqual("PLUGIN_GITHUB_REPOSITORY_INVALID", exception.Code);
    }

    [TestMethod]
    public void ParserRejectsOversizedSources()
    {
        GitHubPluginSourceException exception = Assert.ThrowsExactly<GitHubPluginSourceException>(
            () => RemotePluginReleaseReference.Parse("owner/" + new string('a', 1100)));

        Assert.AreEqual("PLUGIN_GITHUB_REPOSITORY_INVALID", exception.Code);
    }

    [TestMethod]
    public async Task DownloadUsesConfiguredGitHubProxyAndDeletesLeaseOnDispose()
    {
        byte[] package = Encoding.UTF8.GetBytes("verified plugin package");
        string digest = Sha256(package);
        var handler = new RecordingHandler(request =>
        {
            if (request.RequestUri!.Host == "api.github.com")
            {
                return JsonResponse(GitHubRelease(package.Length, digest, GitHubDownload));
            }
            Assert.AreEqual(
                "https://ghproxy.net/" + GitHubDownload,
                request.RequestUri.AbsoluteUri);
            return Bytes(package);
        });
        using var http = new HttpClient(handler);
        string root = NewRoot();
        try
        {
            using var source = new GitHubPluginPackageSource(
                root,
                () => new AppPreferences(false, false, AppSettingsUpdateProxyOption.GhproxyNet),
                http);
            string path;
            using (DownloadedPluginPackage lease = await source.DownloadAsync(
                       "FelixJI/VibeTable-WeRead-Notes-Dashboard",
                       CancellationToken.None))
            {
                path = lease.Path;
                Assert.IsTrue(File.Exists(path));
                Assert.AreEqual(digest, lease.Sha256);
                Assert.AreEqual("v1.0.0", lease.TagName);
                Assert.AreEqual(PackageName, lease.AssetName);
                Assert.AreEqual("FelixJI/VibeTable-WeRead-Notes-Dashboard", lease.Repository);
            }

            Assert.IsFalse(File.Exists(path));
            Assert.AreEqual(2, handler.Requests.Count);
            Assert.AreEqual("https://api.github.com/repos/FelixJI/VibeTable-WeRead-Notes-Dashboard/releases/latest",
                handler.Requests[0].RequestUri.AbsoluteUri);
            Assert.AreEqual("application/vnd.github+json", handler.Requests[0].Accept);
            Assert.AreEqual("2022-11-28", handler.Requests[0].ApiVersion);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task PinnedTagAndAssetRequestsHitTheTagEndpointAndTheDirectAsset()
    {
        byte[] package = Encoding.UTF8.GetBytes("pinned package");
        string digest = Sha256(package);
        var handler = new RecordingHandler(request =>
        {
            if (request.RequestUri!.Host == "api.github.com")
            {
                Assert.AreEqual(
                    "https://api.github.com/repos/FelixJI/repo/releases/tags/v1.0.0",
                    request.RequestUri.AbsoluteUri);
                return JsonResponse(GitHubRelease(package.Length, digest, RepoDownload, tag: "v1.0.0"));
            }
            Assert.AreEqual(RepoDownload, request.RequestUri.AbsoluteUri);
            return Bytes(package);
        });
        using var http = new HttpClient(handler);
        string root = NewRoot();
        try
        {
            using var source = new GitHubPluginPackageSource(root, () => AppPreferences.Default, http);

            using DownloadedPluginPackage lease = await source.DownloadAsync(
                "https://github.com/FelixJI/repo/releases/download/v1.0.0/" + PackageName,
                CancellationToken.None);

            Assert.AreEqual(digest, lease.Sha256);
            Assert.AreEqual("v1.0.0", lease.TagName);
            Assert.AreEqual(2, handler.Requests.Count);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    [DataRow("https://ghfast.top/", AppSettingsUpdateProxyOption.Direct)]
    [DataRow("https://ghfast.top/", AppSettingsUpdateProxyOption.GhproxyNet)]
    [DataRow("https://ghproxy.net/", AppSettingsUpdateProxyOption.Custom)]
    [DataRow("https://gh-proxy.com/", AppSettingsUpdateProxyOption.GhProxyCom)]
    public async Task PastedMirrorRoutesTheGitHubAssetDownloadWithoutReadingOrChangingPreferences(
        string mirror,
        string configuredProxy)
    {
        byte[] package = Encoding.UTF8.GetBytes("wrapped package");
        string digest = Sha256(package);
        var handler = new RecordingHandler(request =>
        {
            if (request.RequestUri!.Host == "api.github.com")
            {
                Assert.AreEqual(
                    "https://api.github.com/repos/FelixJI/repo/releases/tags/v1.0.0",
                    request.RequestUri.AbsoluteUri);
                return JsonResponse(GitHubRelease(package.Length, digest, RepoDownload, tag: "v1.0.0"));
            }
            return Bytes(package);
        });
        using var http = new HttpClient(handler);
        string root = NewRoot();
        int preferenceReads = 0;
        AppPreferences PreferenceReader()
        {
            preferenceReads++;
            return new AppPreferences(false, false, configuredProxy, "https://mirror.example/");
        }
        try
        {
            using var source = new GitHubPluginPackageSource(root, PreferenceReader, http);

            using DownloadedPluginPackage lease = await source.DownloadAsync(
                mirror + "https://github.com/FelixJI/repo/releases/download/v1.0.0/" + PackageName,
                CancellationToken.None);

            Assert.AreEqual(digest, lease.Sha256);
            Assert.AreEqual("v1.0.0", lease.TagName);
            Assert.AreEqual(2, handler.Requests.Count);
            Assert.AreEqual("api.github.com", handler.Requests[0].RequestUri.Host);
            Assert.AreEqual(mirror + RepoDownload, handler.Requests[1].RequestUri.AbsoluteUri);
            // The pasted mirror overrides this download only: the persisted
            // update-proxy setting is never consulted, so it cannot change.
            Assert.AreEqual(0, preferenceReads);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    [DataRow(AppSettingsUpdateProxyOption.Direct, RepoDownload)]
    [DataRow(AppSettingsUpdateProxyOption.GhproxyNet, "https://ghproxy.net/" + RepoDownload)]
    [DataRow(AppSettingsUpdateProxyOption.GhProxyCom, "https://gh-proxy.com/" + RepoDownload)]
    [DataRow(AppSettingsUpdateProxyOption.Custom, "https://mirror.example/" + RepoDownload)]
    public async Task WithoutAPastedMirrorTheGitHubAssetFollowsTheConfiguredProxy(
        string proxy,
        string expectedDownload)
    {
        byte[] package = Encoding.UTF8.GetBytes("routed package");
        string digest = Sha256(package);
        var handler = new RecordingHandler(request =>
            request.RequestUri!.Host == "api.github.com"
                ? JsonResponse(GitHubRelease(package.Length, digest, RepoDownload, tag: "v1.0.0"))
                : Bytes(package));
        using var http = new HttpClient(handler);
        string root = NewRoot();
        try
        {
            using var source = new GitHubPluginPackageSource(
                root,
                () => new AppPreferences(false, false, proxy, "https://mirror.example/"),
                http);

            using DownloadedPluginPackage lease = await source.DownloadAsync(
                "FelixJI/repo/releases/tag/v1.0.0",
                CancellationToken.None);

            Assert.AreEqual(digest, lease.Sha256);
            Assert.AreEqual(2, handler.Requests.Count);
            Assert.AreEqual("https://api.github.com/repos/FelixJI/repo/releases/tags/v1.0.0",
                handler.Requests[0].RequestUri.AbsoluteUri);
            Assert.AreEqual(expectedDownload, handler.Requests[1].RequestUri.AbsoluteUri);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    [DataRow("https://github.com/FelixJI/repo/releases/tag/v2.0.0", "PLUGIN_GITHUB_TAG_MISMATCH")]
    [DataRow("https://github.com/FelixJI/repo/releases/latest/download/other.vtplugin", "PLUGIN_GITHUB_ASSET_NAME_MISMATCH")]
    [DataRow("https://ghproxy.net/https://github.com/FelixJI/repo/releases/download/v2.0.0/" + PackageName, "PLUGIN_GITHUB_TAG_MISMATCH")]
    [DataRow("https://codeberg.org/felix/plugins/releases/download/v2.0.0/" + PackageName, "PLUGIN_CODEBERG_TAG_MISMATCH")]
    public async Task RequestedTagOrAssetNameMismatchFailsClosedBeforeAnyDownload(
        string source,
        string expectedCode)
    {
        byte[] package = Encoding.UTF8.GetBytes("mismatch package");
        string digest = Sha256(package);
        var handler = new RecordingHandler(request =>
        {
            if (request.RequestUri!.Host == "api.github.com")
                return JsonResponse(GitHubRelease(package.Length, digest, RepoDownload, tag: "v1.0.0"));
            return JsonResponse(CodebergRelease("v1.2.0", CodebergAssets(package, digest, CodebergPackage)));
        });
        using var http = new HttpClient(handler);
        string root = NewRoot();
        try
        {
            using var packageSource = new GitHubPluginPackageSource(root, () => AppPreferences.Default, http);

            GitHubPluginSourceException exception = await Assert.ThrowsExactlyAsync<GitHubPluginSourceException>(
                () => packageSource.DownloadAsync(source, CancellationToken.None));

            Assert.AreEqual(expectedCode, exception.Code);
            Assert.AreEqual(1, handler.Requests.Count);
            Assert.AreEqual(0, Directory.GetFiles(root).Length);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    [DataRow("https://evil.example/" + PackageName)]
    [DataRow("http://github.com/FelixJI/repo/releases/download/v1.0.0/" + PackageName)]
    [DataRow("https://github.com:8443/FelixJI/repo/releases/download/v1.0.0/" + PackageName)]
    [DataRow("https://github.com/other/repo/releases/download/v1.0.0/" + PackageName)]
    [DataRow("https://github.com/FelixJI/repo/releases/download/v0.9.0/" + PackageName)]
    [DataRow("https://github.com/FelixJI/repo/releases/download/v1.0.0/other.vtplugin")]
    [DataRow("https://github.com/FelixJI/repo/releases/download/v1.0.0/" + PackageName + "?download=1")]
    public async Task UntrustedGitHubDownloadUrlsAreRejectedBeforeAnyDownload(string downloadUrl)
    {
        byte[] package = Encoding.UTF8.GetBytes("untrusted package");
        var handler = new RecordingHandler(request =>
            JsonResponse(GitHubRelease(package.Length, Sha256(package), downloadUrl)));
        using var http = new HttpClient(handler);
        string root = NewRoot();
        try
        {
            using var source = new GitHubPluginPackageSource(root, () => AppPreferences.Default, http);

            GitHubPluginSourceException exception = await Assert.ThrowsExactlyAsync<GitHubPluginSourceException>(
                () => source.DownloadAsync("FelixJI/repo", CancellationToken.None));

            Assert.AreEqual("PLUGIN_GITHUB_DOWNLOAD_URL_UNTRUSTED", exception.Code);
            Assert.AreEqual(1, handler.Requests.Count);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    [DataRow("missing-sidecar", "PLUGIN_CODEBERG_CHECKSUM_ASSET_INVALID")]
    [DataRow("empty-sidecar", "PLUGIN_CODEBERG_CHECKSUM_ASSET_INVALID")]
    [DataRow("oversized-sidecar", "PLUGIN_CODEBERG_CHECKSUM_ASSET_INVALID")]
    [DataRow("sidecar-names-other-file", "PLUGIN_CODEBERG_CHECKSUM_INVALID")]
    [DataRow("sidecar-has-two-lines", "PLUGIN_CODEBERG_CHECKSUM_INVALID")]
    [DataRow("sidecar-not-utf8", "PLUGIN_CODEBERG_CHECKSUM_INVALID")]
    [DataRow("sidecar-url-untrusted", "PLUGIN_CODEBERG_DOWNLOAD_URL_UNTRUSTED")]
    [DataRow("sidecar-url-nonstandard-port", "PLUGIN_CODEBERG_DOWNLOAD_URL_UNTRUSTED")]
    [DataRow("package-mismatches-sidecar", "PLUGIN_CODEBERG_DIGEST_MISMATCH")]
    public async Task CodebergWithoutAValidMatchingSidecarFailsClosed(
        string variant,
        string expectedCode)
    {
        byte[] package = Encoding.UTF8.GetBytes("codeberg package");
        string digest = Sha256(package);
        byte[] sidecar = Encoding.UTF8.GetBytes($"{digest}  {PackageName}\n");
        string sidecarUrl = CodebergPackage + ".sha256";
        switch (variant)
        {
            case "missing-sidecar":
                sidecarUrl = "";
                break;
            case "empty-sidecar":
                sidecar = [];
                break;
            case "oversized-sidecar":
                sidecar = new byte[MaxChecksumBytes + 1];
                break;
            case "sidecar-names-other-file":
                sidecar = Encoding.UTF8.GetBytes($"{digest}  other.vtplugin\n");
                break;
            case "sidecar-has-two-lines":
                sidecar = Encoding.UTF8.GetBytes($"{digest}  {PackageName}\n{digest}  {PackageName}\n");
                break;
            case "sidecar-not-utf8":
                sidecar = [0xFF, 0xFE, 0x00];
                break;
            case "sidecar-url-untrusted":
                sidecarUrl = "https://evil.example/" + PackageName + ".sha256";
                break;
            case "sidecar-url-nonstandard-port":
                sidecarUrl = "https://codeberg.org:8443/felix/plugins/releases/download/v1.2.0/" + PackageName + ".sha256";
                break;
            case "package-mismatches-sidecar":
                sidecar = Encoding.UTF8.GetBytes($"{new string('b', 64)}  {PackageName}\n");
                break;
        }
        string assets = Asset(PackageName, package.Length, CodebergPackage)
            + (sidecarUrl.Length == 0 ? "" : "," + Asset(PackageName + ".sha256", sidecar.Length, sidecarUrl));
        var handler = new RecordingHandler(request =>
        {
            string path = request.RequestUri!.AbsolutePath;
            if (path.StartsWith("/api/v1/repos/", StringComparison.Ordinal))
                return JsonResponse(CodebergRelease("v1.2.0", assets));
            if (path == "/felix/plugins/releases/download/v1.2.0/" + PackageName + ".sha256")
                return Bytes(sidecar);
            Assert.AreEqual("/felix/plugins/releases/download/v1.2.0/" + PackageName, path);
            return Bytes(package);
        });
        using var http = new HttpClient(handler);
        string root = NewRoot();
        try
        {
            using var source = new GitHubPluginPackageSource(
                root,
                () => new AppPreferences(false, false, AppSettingsUpdateProxyOption.GhproxyNet),
                http);

            GitHubPluginSourceException exception = await Assert.ThrowsExactlyAsync<GitHubPluginSourceException>(
                () => source.DownloadAsync("https://codeberg.org/felix/plugins/releases/tag/v1.2.0", CancellationToken.None));

            Assert.AreEqual(expectedCode, exception.Code);
            Assert.IsTrue(handler.Requests.All(request => request.RequestUri.Host == "codeberg.org" && request.RequestUri.IsDefaultPort));
            Assert.AreEqual(0, Directory.GetFiles(root).Length);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    [DataRow(true)]
    [DataRow(false)]
    public async Task CodebergDownloadsDirectlyWithSidecarChecksumIgnoringGitHubProxy(bool attachmentUuidSidecar)
    {
        byte[] package = Encoding.UTF8.GetBytes("codeberg package");
        string digest = Sha256(package);
        byte[] sidecar = Encoding.UTF8.GetBytes($"{digest} *{PackageName}\r\n");
        string sidecarUrl = attachmentUuidSidecar
            ? $"https://codeberg.org/attachments/{CodebergAttachmentUuid}"
            : CodebergPackage + ".sha256";
        string assets = Asset(PackageName, package.Length, CodebergPackage)
            + "," + Asset(PackageName + ".sha256", sidecar.Length, sidecarUrl);
        var handler = new RecordingHandler(request =>
        {
            Assert.AreEqual("codeberg.org", request.RequestUri!.Host);
            if (request.RequestUri.AbsolutePath.StartsWith("/api/v1/repos/", StringComparison.Ordinal))
            {
                Assert.AreEqual("application/json", request.Headers.Accept.Single().MediaType);
                Assert.IsFalse(request.Headers.Contains("X-GitHub-Api-Version"));
                return JsonResponse(CodebergRelease("v1.2.0", assets));
            }
            return Bytes(request.RequestUri.AbsolutePath.EndsWith(PackageName, StringComparison.Ordinal) ? package : sidecar);
        });
        using var http = new HttpClient(handler);
        string root = NewRoot();
        try
        {
            using var source = new GitHubPluginPackageSource(
                root,
                () => new AppPreferences(false, false, AppSettingsUpdateProxyOption.GhproxyNet),
                http);

            using DownloadedPluginPackage lease = await source.DownloadAsync(
                "codeberg.org/felix/plugins/releases/tag/v1.2.0",
                CancellationToken.None);

            Assert.AreEqual(digest, lease.Sha256);
            Assert.AreEqual("v1.2.0", lease.TagName);
            Assert.AreEqual(PackageName, lease.AssetName);
            Assert.AreEqual("felix/plugins", lease.Repository);
            Assert.IsTrue(File.Exists(lease.Path));
            Assert.AreEqual(3, handler.Requests.Count);
            Assert.AreEqual("https://codeberg.org/api/v1/repos/felix/plugins/releases/tags/v1.2.0",
                handler.Requests[0].RequestUri.AbsoluteUri);
            Assert.AreEqual(sidecarUrl, handler.Requests[1].RequestUri.AbsoluteUri);
            Assert.AreEqual(CodebergPackage, handler.Requests[2].RequestUri.AbsoluteUri);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    [DataRow(true, "PLUGIN_GITHUB_RELEASE_INVALID")]
    [DataRow(false, "PLUGIN_GITHUB_ASSET_AMBIGUOUS")]
    public async Task DraftOrAmbiguousReleasesFailClosed(bool draft, string expectedCode)
    {
        byte[] package = Encoding.UTF8.GetBytes("ambiguous package");
        string digest = Sha256(package);
        string assets = Asset(PackageName, package.Length, GitHubDownload, "uploaded", "sha256:" + digest)
            + "," + Asset("other.vtplugin", package.Length, GitHubDownload, "uploaded", "sha256:" + digest);
        var handler = new RecordingHandler(request =>
            JsonResponse(GitHubRelease(package.Length, digest, GitHubDownload, draft: draft, assets: assets)));
        using var http = new HttpClient(handler);
        string root = NewRoot();
        try
        {
            using var source = new GitHubPluginPackageSource(root, () => AppPreferences.Default, http);

            GitHubPluginSourceException exception = await Assert.ThrowsExactlyAsync<GitHubPluginSourceException>(
                () => source.DownloadAsync("owner/repo", CancellationToken.None));

            Assert.AreEqual(expectedCode, exception.Code);
            Assert.AreEqual(1, handler.Requests.Count);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    [DataRow("owner/repo", "PLUGIN_GITHUB_RELEASE_INVALID")]
    [DataRow("codeberg.org/felix/plugins", "PLUGIN_CODEBERG_RELEASE_INVALID")]
    public async Task NonIsoPublishedAtIsInvalidMetadataOnBothHosts(string source, string expectedCode)
    {
        byte[] package = Encoding.UTF8.GetBytes("timestamp package");
        string digest = Sha256(package);
        string numeric = "\"published_at\": 1750000000";
        var handler = new RecordingHandler(request =>
        {
            if (request.RequestUri!.Host == "api.github.com")
                return JsonResponse(GitHubRelease(package.Length, digest, RepoDownload)
                    .Replace("\"published_at\": \"2026-08-10T00:00:00Z\"", numeric));
            return JsonResponse(CodebergRelease("v1.2.0", Asset(PackageName, package.Length, CodebergPackage))
                .Replace("\"published_at\": \"2026-05-01T10:00:00Z\"", numeric));
        });
        using var http = new HttpClient(handler);
        string root = NewRoot();
        try
        {
            using var packageSource = new GitHubPluginPackageSource(root, () => AppPreferences.Default, http);

            GitHubPluginSourceException exception = await Assert.ThrowsExactlyAsync<GitHubPluginSourceException>(
                () => packageSource.DownloadAsync(source, CancellationToken.None));

            Assert.AreEqual(expectedCode, exception.Code);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task DigestMismatchFailsClosedAndRemovesPartialDownload()
    {
        byte[] package = Encoding.UTF8.GetBytes("changed package");
        string expected = new('a', 64);
        var handler = new RecordingHandler(request =>
            request.RequestUri!.Host == "api.github.com"
                ? JsonResponse(GitHubRelease(package.Length, expected, GitHubDownload))
                : Bytes(package));
        using var http = new HttpClient(handler);
        string root = NewRoot();
        try
        {
            using var source = new GitHubPluginPackageSource(root, () => AppPreferences.Default, http);

            GitHubPluginSourceException exception =
                await Assert.ThrowsExactlyAsync<GitHubPluginSourceException>(() =>
                    source.DownloadAsync("FelixJI/VibeTable-WeRead-Notes-Dashboard", CancellationToken.None));

            Assert.AreEqual("PLUGIN_GITHUB_DIGEST_MISMATCH", exception.Code);
            Assert.AreEqual(0, Directory.GetFiles(root).Length);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    [DataRow("https://github.com/owner/repo", HttpStatusCode.NotFound, "PLUGIN_GITHUB_RELEASE_NOT_FOUND")]
    [DataRow("https://github.com/owner/repo", HttpStatusCode.Forbidden, "PLUGIN_GITHUB_RELEASE_HTTP_FAILED")]
    [DataRow("https://codeberg.org/owner/repo", HttpStatusCode.NotFound, "PLUGIN_CODEBERG_RELEASE_NOT_FOUND")]
    public async Task ReleaseLookupFailuresMapToHostSpecificCodes(
        string source,
        HttpStatusCode status,
        string expectedCode)
    {
        var handler = new RecordingHandler(_ => new HttpResponseMessage(status));
        using var http = new HttpClient(handler);
        string root = NewRoot();
        try
        {
            using var packageSource = new GitHubPluginPackageSource(root, () => AppPreferences.Default, http);

            GitHubPluginSourceException exception = await Assert.ThrowsExactlyAsync<GitHubPluginSourceException>(
                () => packageSource.DownloadAsync(source, CancellationToken.None));

            Assert.AreEqual(expectedCode, exception.Code);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    private static string Sha256(byte[] bytes) => Convert.ToHexString(SHA256.HashData(bytes)).ToLowerInvariant();

    private static HttpResponseMessage JsonResponse(string json) => new(HttpStatusCode.OK)
    {
        Content = new StringContent(json, Encoding.UTF8, "application/json"),
    };

    private static HttpResponseMessage Bytes(byte[] bytes) => new(HttpStatusCode.OK)
    {
        Content = new ByteArrayContent(bytes),
    };

    /// <summary>Forgejo release assets carry no digest; the API returns no digest field at all.</summary>
    private static string CodebergAssets(byte[] package, string digest, string packageUrl)
    {
        byte[] sidecar = Encoding.UTF8.GetBytes($"{digest}  {PackageName}\n");
        return Asset(PackageName, package.Length, packageUrl)
            + "," + Asset(PackageName + ".sha256", sidecar.Length, packageUrl + ".sha256");
    }

    private static string Asset(
        string name,
        long size,
        string url,
        string? state = null,
        string? digest = null)
    {
        string stateJson = state is null ? "" : ",\"state\":\"" + state + "\"";
        string digestJson = digest is null ? "" : ",\"digest\":\"" + digest + "\"";
        return $$"""
            {"name":"{{name}}","size":{{size}}{{stateJson}}{{digestJson}},"browser_download_url":"{{url}}"}
            """;
    }

    private static string GitHubRelease(
        long size,
        string digest,
        string downloadUrl,
        string tag = "v1.0.0",
        bool draft = false,
        string? assets = null) => $$"""
        {
          "tag_name": "{{tag}}",
          "draft": {{(draft ? "true" : "false")}},
          "prerelease": false,
          "published_at": "2026-08-10T00:00:00Z",
          "assets": [{{(assets ?? Asset(PackageName, size, downloadUrl, "uploaded", "sha256:" + digest))}}]
        }
        """;

    private static string CodebergRelease(string tag, string assets) => $$"""
        {
          "tag_name": "{{tag}}",
          "draft": false,
          "prerelease": false,
          "published_at": "2026-05-01T10:00:00Z",
          "assets": [{{assets}}]
        }
        """;

    private static string NewRoot()
    {
        string root = Path.Combine(Path.GetTempPath(), $"vibetable-github-plugin-{Guid.NewGuid():N}");
        Directory.CreateDirectory(root);
        return root;
    }

    private sealed record SentRequest(Uri RequestUri, string? Accept, string? ApiVersion);

    private sealed class RecordingHandler(
        Func<HttpRequestMessage, HttpResponseMessage> respond) : HttpMessageHandler
    {
        public List<SentRequest> Requests { get; } = [];

        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request,
            CancellationToken cancellationToken)
        {
            Requests.Add(new SentRequest(
                request.RequestUri!,
                request.Headers.Accept.Any() ? string.Join(", ", request.Headers.Accept.Select(media => media.MediaType)) : null,
                request.Headers.TryGetValues("X-GitHub-Api-Version", out var values) ? values.Single() : null));
            return Task.FromResult(respond(request));
        }
    }
}
