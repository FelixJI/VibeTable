using System;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class GitHubPluginPackageSourceTests
{
    [TestMethod]
    [DataRow("FelixJI/VibeTable-WeRead-Notes-Dashboard")]
    [DataRow("https://github.com/FelixJI/VibeTable-WeRead-Notes-Dashboard")]
    [DataRow("https://github.com/FelixJI/VibeTable-WeRead-Notes-Dashboard.git")]
    public void RepositoryParserAcceptsCanonicalPublicRepositoryForms(string value)
    {
        GitHubPluginRepository repository = GitHubPluginRepository.Parse(value);

        Assert.AreEqual("FelixJI/VibeTable-WeRead-Notes-Dashboard", repository.Slug);
    }

    [TestMethod]
    [DataRow("https://example.com/owner/repo")]
    [DataRow("owner/repo/releases/latest")]
    [DataRow("owner")]
    [DataRow("owner/repo?ref=main")]
    public void RepositoryParserRejectsNonRepositoryInputs(string value)
    {
        GitHubPluginSourceException exception = Assert.ThrowsExactly<GitHubPluginSourceException>(
            () => GitHubPluginRepository.Parse(value));

        Assert.AreEqual("PLUGIN_GITHUB_REPOSITORY_INVALID", exception.Code);
    }

    [TestMethod]
    public async Task DownloadLatestUsesConfiguredProxyAndDeletesLeaseOnDispose()
    {
        byte[] package = Encoding.UTF8.GetBytes("verified plugin package");
        string digest = Convert.ToHexString(SHA256.HashData(package)).ToLowerInvariant();
        var handler = new RecordingHandler(request =>
        {
            if (request.RequestUri!.Host == "api.github.com")
            {
                return JsonResponse(ReleaseJson(package.Length, digest));
            }
            Assert.AreEqual(
                "https://ghproxy.net/https://github.com/FelixJI/"
                + "VibeTable-WeRead-Notes-Dashboard/releases/download/v1.0.0/"
                + "weread-notes-dashboard-1.0.0.vtplugin",
                request.RequestUri.AbsoluteUri);
            return new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new ByteArrayContent(package),
            };
        });
        using var http = new HttpClient(handler);
        string root = NewRoot();
        try
        {
            using var source = new GitHubPluginPackageSource(
                root,
                () => new AppPreferences(false, false, UpdateProxyOptions.GhProxyNet),
                http);
            string path;
            using (DownloadedPluginPackage lease = await source.DownloadLatestAsync(
                       "FelixJI/VibeTable-WeRead-Notes-Dashboard",
                       CancellationToken.None))
            {
                path = lease.Path;
                Assert.IsTrue(File.Exists(path));
                Assert.AreEqual(digest, lease.Sha256);
                Assert.AreEqual("v1.0.0", lease.TagName);
            }
            Assert.IsFalse(File.Exists(path));
            Assert.AreEqual(2, handler.Requests.Count);
            Assert.AreEqual("api.github.com", handler.Requests[0].Host);
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
                ? JsonResponse(ReleaseJson(package.Length, expected))
                : new HttpResponseMessage(HttpStatusCode.OK)
                {
                    Content = new ByteArrayContent(package),
                });
        using var http = new HttpClient(handler);
        string root = NewRoot();
        try
        {
            using var source = new GitHubPluginPackageSource(
                root,
                () => AppPreferences.Default,
                http);

            GitHubPluginSourceException exception =
                await Assert.ThrowsExactlyAsync<GitHubPluginSourceException>(() =>
                    source.DownloadLatestAsync("owner/repo", CancellationToken.None));

            Assert.AreEqual("PLUGIN_GITHUB_DIGEST_MISMATCH", exception.Code);
            Assert.AreEqual(0, Directory.GetFiles(root).Length);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    private static HttpResponseMessage JsonResponse(string json) => new(HttpStatusCode.OK)
    {
        Content = new StringContent(json, Encoding.UTF8, "application/json"),
    };

    private static string ReleaseJson(int size, string digest) => $$"""
        {
          "tag_name": "v1.0.0",
          "draft": false,
          "prerelease": false,
          "published_at": "2026-08-10T00:00:00Z",
          "assets": [{
            "name": "weread-notes-dashboard-1.0.0.vtplugin",
            "size": {{size}},
            "state": "uploaded",
            "digest": "sha256:{{digest}}",
            "browser_download_url": "https://github.com/FelixJI/VibeTable-WeRead-Notes-Dashboard/releases/download/v1.0.0/weread-notes-dashboard-1.0.0.vtplugin"
          }]
        }
        """;

    private static string NewRoot()
    {
        string root = System.IO.Path.Combine(
            System.IO.Path.GetTempPath(),
            $"vibetable-github-plugin-{Guid.NewGuid():N}");
        Directory.CreateDirectory(root);
        return root;
    }

    private sealed class RecordingHandler(
        Func<HttpRequestMessage, HttpResponseMessage> respond) : HttpMessageHandler
    {
        public System.Collections.Generic.List<Uri> Requests { get; } = [];

        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request,
            CancellationToken cancellationToken)
        {
            Requests.Add(request.RequestUri!);
            return Task.FromResult(respond(request));
        }
    }
}
