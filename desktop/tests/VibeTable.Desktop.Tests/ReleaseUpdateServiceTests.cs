using System.IO.Compression;
using System.Net;
using System.Security.Cryptography;
using System.Text;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ReleaseUpdateServiceTests
{
    private string? _root;

    [TestCleanup]
    public void Cleanup()
    {
        if (_root is not null && Directory.Exists(_root))
        {
            Directory.Delete(_root, recursive: true);
        }
    }

    [TestMethod]
    public async Task ClientSelectsHighestSemanticVersionAndBuildsRangeNotes()
    {
        byte[] package = Encoding.UTF8.GetBytes("package");
        string digest = Convert.ToHexString(SHA256.HashData(package)).ToLowerInvariant();
        string json = $$"""
            [
              {"tag_name":"v1.2.0","name":"March","body":"one","html_url":"https://github.com/FelixJI/VibeTable/releases/tag/v1.2.0","draft":false,"prerelease":false,"published_at":"2026-03-01T00:00:00Z","assets":[]},
              {"tag_name":"v1.10.0","name":"October","body":"two","html_url":"https://github.com/FelixJI/VibeTable/releases/tag/v1.10.0","draft":false,"prerelease":false,"published_at":"2026-10-01T00:00:00Z","assets":[{"name":"VibeTable-v1.10.0-win-x64.zip","state":"uploaded","size":{{package.Length}},"digest":"sha256:{{digest}}","browser_download_url":"https://github.com/FelixJI/VibeTable/releases/download/v1.10.0/VibeTable-v1.10.0-win-x64.zip"}]},
              {"tag_name":"v2.0.0","name":"Preview","body":"skip","html_url":"https://github.com/FelixJI/VibeTable/releases/tag/v2.0.0","draft":false,"prerelease":true,"published_at":"2026-11-01T00:00:00Z","assets":[]},
              {"tag_name":"v1.0.0","name":"Current","body":"current","html_url":"https://github.com/FelixJI/VibeTable/releases/tag/v1.0.0","draft":false,"prerelease":false,"published_at":"2026-01-01T00:00:00Z","assets":[]}
            ]
            """;
        using var http = new HttpClient(new DelegateHandler((request, _) =>
        {
            Assert.AreEqual(GitHubReleaseUpdateClient.ReleasesUri, request.RequestUri);
            return JsonResponse(json);
        }));
        var client = new GitHubReleaseUpdateClient(http);

        ReleaseUpdateCandidate? candidate = await client.CheckAsync(
            "1.0.0",
            UpdateProxyOptions.GhProxyNet,
            null,
            CancellationToken.None);

        Assert.IsNotNull(candidate);
        Assert.AreEqual("1.10.0", candidate.Version.ToString());
        Assert.StartsWith("https://ghproxy.net/https://github.com/", candidate.DownloadUri.AbsoluteUri);
        CollectionAssert.AreEqual(
            new[] { "1.10.0", "1.2.0" },
            candidate.Releases.Select(item => item.Version).ToArray());
        Assert.IsFalse(candidate.NotesTruncated);
    }

    [TestMethod]
    public async Task ClientRejectsReleaseAssetWithoutGitHubDigest()
    {
        const string json = """
            [{"tag_name":"v1.1.0","name":"bad","body":"","html_url":"https://github.com/FelixJI/VibeTable/releases/tag/v1.1.0","draft":false,"prerelease":false,"published_at":"2026-01-01T00:00:00Z","assets":[{"name":"VibeTable-v1.1.0-win-x64.zip","state":"uploaded","size":4,"digest":null,"browser_download_url":"https://github.com/FelixJI/VibeTable/releases/download/v1.1.0/VibeTable-v1.1.0-win-x64.zip"}]}]
            """;
        using var http = new HttpClient(new DelegateHandler((_, _) => JsonResponse(json)));
        var client = new GitHubReleaseUpdateClient(http);

        ReleaseUpdateException exception = await Assert.ThrowsExactlyAsync<ReleaseUpdateException>(
            () => client.CheckAsync(
                "1.0.0",
                UpdateProxyOptions.Direct,
                null,
                CancellationToken.None));

        Assert.AreEqual("UPDATE_ASSET_INVALID", exception.Code);
    }

    [TestMethod]
    public async Task ClientReturnsNoCandidateWhenCurrentVersionIsLatest()
    {
        const string json = """
            [{"tag_name":"v1.0.0","name":"Current","body":"","html_url":"https://github.com/FelixJI/VibeTable/releases/tag/v1.0.0","draft":false,"prerelease":false,"published_at":"2026-01-01T00:00:00Z","assets":[]}]
            """;
        using var http = new HttpClient(new DelegateHandler((_, _) => JsonResponse(json)));
        var client = new GitHubReleaseUpdateClient(http);

        ReleaseUpdateCandidate? candidate = await client.CheckAsync(
            "1.0.0",
            UpdateProxyOptions.Direct,
            null,
            CancellationToken.None);

        Assert.IsNull(candidate);
    }

    [TestMethod]
    public void ProxyRewritingRequiresHttpsAndKeepsGitHubAsTheSourceUrl()
    {
        var direct = new Uri(
            "https://github.com/FelixJI/VibeTable/releases/download/v1.1.0/package.zip");

        Uri proxied = UpdateDownloadProxy.Rewrite(
            direct,
            UpdateProxyOptions.Custom,
            "https://proxy.example/base");

        Assert.AreEqual(
            "https://proxy.example/base/https://github.com/FelixJI/VibeTable/releases/download/v1.1.0/package.zip",
            proxied.AbsoluteUri);
        ReleaseUpdateException exception = Assert.ThrowsExactly<ReleaseUpdateException>(() =>
            UpdateDownloadProxy.Rewrite(
                direct,
                UpdateProxyOptions.Custom,
                "http://proxy.example/"));
        Assert.AreEqual("UPDATE_PROXY_INVALID", exception.Code);
    }

    [TestMethod]
    public async Task StagerVerifiesDigestAndPackageIdentity()
    {
        byte[] archive = CreatePackageArchive("1.1.0");
        string digest = Convert.ToHexString(SHA256.HashData(archive)).ToLowerInvariant();
        string installRoot = CreateInstalledPackage("1.0.0");
        using var http = new HttpClient(new DelegateHandler((_, _) =>
            new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new ByteArrayContent(archive),
            }));
        var stager = new ReleasePackageStager(http);
        var candidate = new ReleaseUpdateCandidate(
            new StableReleaseVersion(1, 1, 0),
            new Uri("https://github.com/update.zip"),
            digest,
            archive.Length,
            "https://github.com/release",
            [],
            false);

        string planPath = await stager.StageAsync(
            candidate,
            installRoot,
            "1.0.0",
            123,
            CancellationToken.None);
        UpdateApplyPlan plan = UpdateProcessCommand.ReadAndValidatePlan(planPath);

        Assert.AreEqual("1.1.0", plan.TargetVersion);
        Assert.IsTrue(File.Exists(Path.Combine(plan.SourceRoot, "VibeTable.Next.exe")));
        Assert.AreEqual("user", File.ReadAllText(Path.Combine(installRoot, "user-data.db")));
    }

    [TestMethod]
    public void ApplyReplacesOnlyOwnedEntriesAndPreservesUnknownUserFiles()
    {
        string target = CreateInstalledPackage("1.0.0");
        string stage = Path.Combine(Path.GetDirectoryName(target)!, ".VibeTable.Next.update-test");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            new string('a', 64));

        UpdateProcessCommand.ApplyPackageOwnedFiles(plan);

        Assert.AreEqual("new", File.ReadAllText(Path.Combine(target, "VibeTable.Next.exe")));
        Assert.AreEqual("user", File.ReadAllText(Path.Combine(target, "user-data.db")));
        Assert.AreEqual(
            "old",
            File.ReadAllText(Path.Combine(stage, "backup", "VibeTable.Next.exe")));
    }

    [TestMethod]
    public void ApplyRollsBackOwnedEntriesWhenNewPackageIsIncomplete()
    {
        string target = CreateInstalledPackage("1.0.0");
        string stage = Path.Combine(Path.GetDirectoryName(target)!, ".VibeTable.Next.update-rollback");
        string source = Path.Combine(stage, "package", "VibeTable");
        Directory.CreateDirectory(source);
        File.WriteAllText(Path.Combine(source, "VibeTable.Next.exe"), "new");
        File.WriteAllText(Path.Combine(source, "release.json"), ReleaseJson("1.1.0"));
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            new string('b', 64));

        Assert.ThrowsExactly<ReleaseUpdateException>(() =>
            UpdateProcessCommand.ApplyPackageOwnedFiles(plan));

        Assert.AreEqual("old", File.ReadAllText(Path.Combine(target, "VibeTable.Next.exe")));
        Assert.AreEqual("user", File.ReadAllText(Path.Combine(target, "user-data.db")));
        Assert.IsTrue(Directory.Exists(Path.Combine(target, "resources")));
    }

    [TestMethod]
    public void CleanupValidationRequiresTargetToContainTheNewVersion()
    {
        string container = Root();
        string target = Path.Combine(container, "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");
        string stage = Path.Combine(container, ".VibeTable.Next.update-cleanup");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            new string('c', 64));
        string planPath = Path.Combine(stage, "update-plan.json");
        File.WriteAllText(planPath, System.Text.Json.JsonSerializer.Serialize(plan));

        Assert.ThrowsExactly<ReleaseUpdateException>(() =>
            UpdateProcessCommand.ReadAndValidatePlan(planPath));
        UpdateApplyPlan validated = UpdateProcessCommand.ReadAndValidatePlan(
            planPath,
            targetAlreadyUpdated: true);

        Assert.AreEqual("1.1.0", validated.TargetVersion);
    }

    [TestMethod]
    public void ExtractRejectsZipSlipWithoutWritingOutsideDestination()
    {
        string root = Root();
        string archivePath = Path.Combine(root, "unsafe.zip");
        using (ZipArchive archive = ZipFile.Open(archivePath, ZipArchiveMode.Create))
        {
            ZipArchiveEntry entry = archive.CreateEntry("VibeTable/../outside.txt");
            using StreamWriter writer = new(entry.Open());
            writer.Write("unsafe");
        }
        string destination = Path.Combine(root, "extract");

        ReleaseUpdateException exception = Assert.ThrowsExactly<ReleaseUpdateException>(() =>
            ReleasePackageStager.ExtractVerifiedPackage(archivePath, destination, "1.1.0"));

        Assert.AreEqual("UPDATE_ARCHIVE_UNSAFE", exception.Code);
        Assert.IsFalse(File.Exists(Path.Combine(root, "outside.txt")));
    }

    [TestMethod]
    public void ExtractRejectsWindowsAlternateDataStreamNames()
    {
        string root = Root();
        string archivePath = Path.Combine(root, "ads.zip");
        using (ZipArchive archive = ZipFile.Open(archivePath, ZipArchiveMode.Create))
        {
            WriteEntry(archive, "VibeTable/resources/config.json:payload", "unsafe");
        }

        ReleaseUpdateException exception = Assert.ThrowsExactly<ReleaseUpdateException>(() =>
            ReleasePackageStager.ExtractVerifiedPackage(
                archivePath,
                Path.Combine(root, "ads-extract"),
                "1.1.0"));

        Assert.AreEqual("UPDATE_ARCHIVE_UNSAFE", exception.Code);
    }

    private string CreateInstalledPackage(string version)
    {
        string root = Path.Combine(Root(), "VibeTable.Next");
        CreatePackageTree(root, version, "old");
        File.WriteAllText(Path.Combine(root, "user-data.db"), "user");
        return root;
    }

    private static void CreatePackageTree(string root, string version, string executableContent)
    {
        Directory.CreateDirectory(Path.Combine(root, "resources"));
        File.WriteAllText(Path.Combine(root, "VibeTable.Next.exe"), executableContent);
        File.WriteAllText(Path.Combine(root, "release.json"), ReleaseJson(version));
        File.WriteAllText(Path.Combine(root, "resources", "publish-layout.json"), "{}");
    }

    private static byte[] CreatePackageArchive(string version)
    {
        using var buffer = new MemoryStream();
        using (var archive = new ZipArchive(buffer, ZipArchiveMode.Create, leaveOpen: true))
        {
            WriteEntry(archive, "VibeTable/VibeTable.Next.exe", "new");
            WriteEntry(archive, "VibeTable/release.json", ReleaseJson(version));
            WriteEntry(archive, "VibeTable/resources/publish-layout.json", "{}");
        }
        return buffer.ToArray();
    }

    private static void WriteEntry(ZipArchive archive, string name, string content)
    {
        ZipArchiveEntry entry = archive.CreateEntry(name);
        using StreamWriter writer = new(entry.Open(), Encoding.UTF8);
        writer.Write(content);
    }

    private static string ReleaseJson(string version) =>
        $$"""{"product":"VibeTable","version":"{{version}}","platform":"windows","architecture":"x64"}""";

    private string Root()
    {
        _root ??= Path.Combine(
            Environment.CurrentDirectory,
            "build",
            "desktop-update-tests",
            Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(_root);
        return _root;
    }

    private static HttpResponseMessage JsonResponse(string json) => new(HttpStatusCode.OK)
    {
        Content = new StringContent(json, Encoding.UTF8, "application/json"),
    };

    private sealed class DelegateHandler(
        Func<HttpRequestMessage, CancellationToken, HttpResponseMessage> handler)
        : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request,
            CancellationToken cancellationToken) =>
            Task.FromResult(handler(request, cancellationToken));
    }
}
