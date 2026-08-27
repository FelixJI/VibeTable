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
              {"tag_name":"v1.10.0","name":"October","body":"two","html_url":"https://github.com/FelixJI/VibeTable/releases/tag/v1.10.0","draft":false,"prerelease":false,"published_at":"2026-10-01T00:00:00Z","assets":[{"name":"VibeTable-v1.10.0-win-x64.zip","state":"uploaded","size":{{package.Length}},"digest":"sha256:{{digest}}","browser_download_url":"https://github.com/FelixJI/VibeTable/releases/download/v1.10.0/VibeTable-v1.10.0-win-x64.zip"},{"name":"VibeTable-v1.10.0-win-x64.zip.sha256","state":"uploaded","size":100,"digest":null,"browser_download_url":"https://github.com/FelixJI/VibeTable/releases/download/v1.10.0/VibeTable-v1.10.0-win-x64.zip.sha256"}]},
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
        Assert.StartsWith("https://ghproxy.net/https://github.com/", candidate.ChecksumUri.AbsoluteUri);
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
    public async Task ClientRejectsReleaseWithoutSameChannelChecksumAsset()
    {
        byte[] package = Encoding.UTF8.GetBytes("package");
        string digest = Convert.ToHexString(SHA256.HashData(package)).ToLowerInvariant();
        string json = $$"""
            [{"tag_name":"v1.1.0","name":"missing checksum","body":"","html_url":"https://github.com/FelixJI/VibeTable/releases/tag/v1.1.0","draft":false,"prerelease":false,"published_at":"2026-01-01T00:00:00Z","assets":[{"name":"VibeTable-v1.1.0-win-x64.zip","state":"uploaded","size":{{package.Length}},"digest":"sha256:{{digest}}","browser_download_url":"https://github.com/FelixJI/VibeTable/releases/download/v1.1.0/VibeTable-v1.1.0-win-x64.zip"}]}]
            """;
        using var http = new HttpClient(new DelegateHandler((_, _) => JsonResponse(json)));
        var client = new GitHubReleaseUpdateClient(http);

        ReleaseUpdateException exception = await Assert.ThrowsExactlyAsync<ReleaseUpdateException>(
            () => client.CheckAsync(
                "1.0.0",
                UpdateProxyOptions.GhProxyNet,
                null,
                CancellationToken.None));

        Assert.AreEqual("UPDATE_CHECKSUM_ASSET_INVALID", exception.Code);
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
    public async Task CoordinatorDisablesInstallForDevelopmentAndTestHosts()
    {
        const string json = """
            [{"tag_name":"v1.0.0","name":"Current","body":"","html_url":"https://github.com/FelixJI/VibeTable/releases/tag/v1.0.0","draft":false,"prerelease":false,"published_at":"2026-01-01T00:00:00Z","assets":[]}]
            """;
        using var http = new HttpClient(new DelegateHandler((_, _) => JsonResponse(json)));
        var coordinator = new ReleaseUpdateCoordinator(
            Root(),
            "1.0.0",
            new GitHubReleaseUpdateClient(http),
            new ReleasePackageStager(),
            installationEnabled: false);

        ReleaseUpdateCheckResult result = await coordinator.CheckAsync(
            AppPreferences.Default,
            CancellationToken.None);
        ReleaseUpdateException exception = await Assert.ThrowsExactlyAsync<ReleaseUpdateException>(
            () => coordinator.LaunchUpdateAsync(CancellationToken.None));

        Assert.IsFalse(result.CanInstall);
        StringAssert.Contains(result.InstallUnavailableReason, "开发或测试模式");
        Assert.AreEqual("UPDATE_INSTALL_DISABLED", exception.Code);
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
        using var http = new HttpClient(new DelegateHandler((request, _) =>
            new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = request.RequestUri!.AbsoluteUri.EndsWith(".sha256", StringComparison.Ordinal)
                    ? new StringContent($"{digest}  VibeTable-v1.1.0-win-x64.zip\n", Encoding.UTF8)
                    : new ByteArrayContent(archive),
            }));
        var stager = new ReleasePackageStager(http);
        var candidate = new ReleaseUpdateCandidate(
            new StableReleaseVersion(1, 1, 0),
            "VibeTable-v1.1.0-win-x64.zip",
            new Uri("https://github.com/update.zip"),
            new Uri("https://github.com/update.zip.sha256"),
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
    public async Task StagerRejectsChecksumThatDisagreesWithGitHubDigest()
    {
        byte[] archive = CreatePackageArchive("1.1.0");
        string digest = Convert.ToHexString(SHA256.HashData(archive)).ToLowerInvariant();
        string installRoot = CreateInstalledPackage("1.0.0");
        using var http = new HttpClient(new DelegateHandler((request, _) =>
            new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = request.RequestUri!.AbsoluteUri.EndsWith(".sha256", StringComparison.Ordinal)
                    ? new StringContent($"{new string('0', 64)}  VibeTable-v1.1.0-win-x64.zip\n", Encoding.UTF8)
                    : new ByteArrayContent(archive),
            }));
        var stager = new ReleasePackageStager(http);
        var candidate = new ReleaseUpdateCandidate(
            new StableReleaseVersion(1, 1, 0),
            "VibeTable-v1.1.0-win-x64.zip",
            new Uri("https://github.com/update.zip"),
            new Uri("https://github.com/update.zip.sha256"),
            digest,
            archive.Length,
            "https://github.com/release",
            [],
            false);

        ReleaseUpdateException exception = await Assert.ThrowsExactlyAsync<ReleaseUpdateException>(
            () => stager.StageAsync(candidate, installRoot, "1.0.0", 123, CancellationToken.None));

        Assert.AreEqual("UPDATE_CHECKSUM_DIGEST_MISMATCH", exception.Code);
    }

    [TestMethod]
    public async Task StagerRejectsChecksumForAnotherArchiveName()
    {
        byte[] archive = CreatePackageArchive("1.1.0");
        string digest = Convert.ToHexString(SHA256.HashData(archive)).ToLowerInvariant();
        string installRoot = CreateInstalledPackage("1.0.0");
        using var http = new HttpClient(new DelegateHandler((request, _) =>
            new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = request.RequestUri!.AbsoluteUri.EndsWith(".sha256", StringComparison.Ordinal)
                    ? new StringContent($"{digest}  another.zip\n", Encoding.UTF8)
                    : new ByteArrayContent(archive),
            }));
        var stager = new ReleasePackageStager(http);
        var candidate = new ReleaseUpdateCandidate(
            new StableReleaseVersion(1, 1, 0),
            "VibeTable-v1.1.0-win-x64.zip",
            new Uri("https://github.com/update.zip"),
            new Uri("https://github.com/update.zip.sha256"),
            digest,
            archive.Length,
            "https://github.com/release",
            [],
            false);

        ReleaseUpdateException exception = await Assert.ThrowsExactlyAsync<ReleaseUpdateException>(
            () => stager.StageAsync(candidate, installRoot, "1.0.0", 123, CancellationToken.None));

        Assert.AreEqual("UPDATE_CHECKSUM_INVALID", exception.Code);
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
    [DoNotParallelize]
    public void PendingUpdateKeepsStageAndBackupUntilShellReadyConfirmation()
    {
        string container = Root();
        string target = Path.Combine(container, "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");
        string stage = Path.Combine(container, ".VibeTable.Next.update-pending");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        string backup = Path.Combine(stage, "backup");
        Directory.CreateDirectory(backup);
        string backupSentinel = Path.Combine(backup, "VibeTable.Next.exe");
        File.WriteAllText(backupSentinel, "old");
        string token = new('e', 64);
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            token,
            SmokeTest: true);
        File.WriteAllText(
            Path.Combine(stage, "update-plan.json"),
            System.Text.Json.JsonSerializer.Serialize(plan));
        string? previous = Environment.GetEnvironmentVariable(
            UpdateProcessCommand.SmokeTokenEnvironmentVariable);

        try
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                token);
            bool recognized = UpdateProcessCommand.TryCreateActivationGate(
                [
                    "--cleanup-update",
                    stage,
                    "--updater-pid",
                    int.MaxValue.ToString(System.Globalization.CultureInfo.InvariantCulture),
                    "--update-token",
                    token,
                    "--self-update-smoke",
                    "--test-mode",
                    "--readiness-dir",
                    Path.Combine(container, "readiness"),
                ],
                out IUpdateActivationGate? gate,
                runningRoot: target);

            Assert.IsTrue(recognized);
            Assert.IsNotNull(gate);
            Assert.IsTrue(Directory.Exists(stage));
            Assert.AreEqual("old", File.ReadAllText(backupSentinel));
            Assert.IsFalse(UpdateProcessCommand.TryCreateActivationGate(
                [
                    "--cleanup-update",
                    stage,
                    "--updater-pid",
                    int.MaxValue.ToString(System.Globalization.CultureInfo.InvariantCulture),
                    "--update-token",
                    token,
                    "--update-token",
                    token,
                    "--self-update-smoke",
                ],
                out _,
                runningRoot: target));
        }
        finally
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                previous);
        }
    }

    [TestMethod]
    [DoNotParallelize]
    public async Task ShellReadyConfirmationCleansStageAndWritesCompletionOnce()
    {
        string container = Root();
        string target = Path.Combine(container, "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");
        string stage = Path.Combine(container, ".VibeTable.Next.update-confirm");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        string backup = Path.Combine(stage, "backup");
        Directory.CreateDirectory(backup);
        File.WriteAllText(Path.Combine(backup, "VibeTable.Next.exe"), "old");
        string token = new('f', 64);
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            token,
            SmokeTest: true);
        File.WriteAllText(
            Path.Combine(stage, "update-plan.json"),
            System.Text.Json.JsonSerializer.Serialize(plan));
        string? previous = Environment.GetEnvironmentVariable(
            UpdateProcessCommand.SmokeTokenEnvironmentVariable);

        try
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                token);
            Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
                [
                    "--cleanup-update",
                    stage,
                    "--updater-pid",
                    int.MaxValue.ToString(System.Globalization.CultureInfo.InvariantCulture),
                    "--update-token",
                    token,
                    "--self-update-smoke",
                ],
                out IUpdateActivationGate? gate,
                runningRoot: target));
            Assert.IsNotNull(gate);

            gate.ConfirmShellReady();
            gate.ConfirmShellReady();
            Assert.IsTrue(await gate.Completion.WaitAsync(TimeSpan.FromSeconds(5)));

            Assert.IsFalse(Directory.Exists(stage));
            using System.Text.Json.JsonDocument completion =
                System.Text.Json.JsonDocument.Parse(File.ReadAllText(Path.Combine(
                    target,
                    UpdateProcessCommand.SmokeCompletionFileName)));
            Assert.AreEqual(
                token,
                completion.RootElement.GetProperty("token").GetString());
            Assert.AreEqual(
                "1.1.0",
                completion.RootElement.GetProperty("targetVersion").GetString());
            Assert.AreEqual(
                Environment.ProcessId,
                completion.RootElement.GetProperty("processId").GetInt32());
            Assert.IsTrue(DateTimeOffset.TryParse(
                completion.RootElement.GetProperty("confirmedAt").GetString(),
                out _));
        }
        finally
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                previous);
        }
    }

    [TestMethod]
    [DoNotParallelize]
    public async Task ShellReadyConfirmationRetainsBackupWhenTargetIdentityChanged()
    {
        string container = Root();
        string target = Path.Combine(container, "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");
        string stage = Path.Combine(container, ".VibeTable.Next.update-revalidate");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        string backup = Path.Combine(stage, "backup");
        Directory.CreateDirectory(backup);
        string backupSentinel = Path.Combine(backup, "VibeTable.Next.exe");
        File.WriteAllText(backupSentinel, "old");
        string token = new('a', 64);
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            token,
            SmokeTest: true);
        File.WriteAllText(
            Path.Combine(stage, "update-plan.json"),
            System.Text.Json.JsonSerializer.Serialize(plan));
        string? previous = Environment.GetEnvironmentVariable(
            UpdateProcessCommand.SmokeTokenEnvironmentVariable);

        try
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                token);
            Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
                [
                    "--cleanup-update",
                    stage,
                    "--updater-pid",
                    int.MaxValue.ToString(System.Globalization.CultureInfo.InvariantCulture),
                    "--update-token",
                    token,
                    "--self-update-smoke",
                ],
                out IUpdateActivationGate? gate,
                runningRoot: target));
            Assert.IsNotNull(gate);

            CreatePackageTree(target, "1.0.0", "changed");
            gate.ConfirmShellReady();

            Assert.IsFalse(await gate.Completion.WaitAsync(TimeSpan.FromSeconds(5)));
            Assert.IsTrue(Directory.Exists(stage));
            Assert.AreEqual("old", File.ReadAllText(backupSentinel));
            Assert.IsFalse(File.Exists(Path.Combine(
                target,
                UpdateProcessCommand.SmokeCompletionFileName)));
        }
        finally
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                previous);
        }
    }

    [TestMethod]
    [DoNotParallelize]
    public async Task ShellReadyConfirmationIsNonBlockingAndSingleUse()
    {
        string container = Root();
        string target = Path.Combine(container, "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");
        string stage = Path.Combine(container, ".VibeTable.Next.update-async");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        string token = new('b', 64);
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            token,
            SmokeTest: true);
        File.WriteAllText(
            Path.Combine(stage, "update-plan.json"),
            System.Text.Json.JsonSerializer.Serialize(plan));
        string? previous = Environment.GetEnvironmentVariable(
            UpdateProcessCommand.SmokeTokenEnvironmentVariable);
        using var cleanupEntered = new ManualResetEventSlim();
        using var cleanupRelease = new ManualResetEventSlim();
        int cleanupCalls = 0;

        try
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                token);
            Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
                [
                    "--cleanup-update",
                    stage,
                    "--updater-pid",
                    int.MaxValue.ToString(System.Globalization.CultureInfo.InvariantCulture),
                    "--update-token",
                    token,
                    "--self-update-smoke",
                ],
                out IUpdateActivationGate? gate,
                runningRoot: target,
                waitForUpdaterExit: _ => Task.FromResult(true),
                cleanupStage: _ =>
                {
                    cleanupEntered.Set();
                    cleanupRelease.Wait(TimeSpan.FromSeconds(5));
                    Interlocked.Increment(ref cleanupCalls);
                    return true;
                }));
            Assert.IsNotNull(gate);

            Task confirmationCall = Task.Run(gate.ConfirmShellReady);
            try
            {
                Assert.IsTrue(cleanupEntered.Wait(TimeSpan.FromSeconds(2)));
                await confirmationCall.WaitAsync(TimeSpan.FromSeconds(1));
                gate.ConfirmShellReady();
                Assert.IsFalse(gate.Completion.IsCompleted);
                Assert.AreEqual(0, Volatile.Read(ref cleanupCalls));
            }
            finally
            {
                cleanupRelease.Set();
            }
            Assert.IsTrue(await gate.Completion.WaitAsync(TimeSpan.FromSeconds(5)));
            Assert.AreEqual(1, Volatile.Read(ref cleanupCalls));
        }
        finally
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                previous);
        }
    }

    [TestMethod]
    [DoNotParallelize]
    public async Task FailedCleanupRetainsStageAndAllowsANewGateToRetry()
    {
        string container = Root();
        string target = Path.Combine(container, "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");
        string stage = Path.Combine(container, ".VibeTable.Next.update-retry");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        string backup = Path.Combine(stage, "backup");
        Directory.CreateDirectory(backup);
        File.WriteAllText(Path.Combine(backup, "VibeTable.Next.exe"), "old");
        string token = new('c', 64);
        var plan = new UpdateApplyPlan(
            1, target, source, stage, 123, "1.0.0", "1.1.0", token, SmokeTest: true);
        File.WriteAllText(
            Path.Combine(stage, "update-plan.json"),
            System.Text.Json.JsonSerializer.Serialize(plan));
        string? previous = Environment.GetEnvironmentVariable(
            UpdateProcessCommand.SmokeTokenEnvironmentVariable);
        string[] arguments =
        [
            "--cleanup-update",
            stage,
            "--updater-pid",
            int.MaxValue.ToString(System.Globalization.CultureInfo.InvariantCulture),
            "--update-token",
            token,
            "--self-update-smoke",
        ];

        try
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                token);
            Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
                arguments,
                out IUpdateActivationGate? first,
                runningRoot: target,
                waitForUpdaterExit: _ => Task.FromResult(true),
                cleanupStage: _ => false));
            Assert.IsNotNull(first);
            first.ConfirmShellReady();
            Assert.IsFalse(await first.Completion.WaitAsync(TimeSpan.FromSeconds(5)));
            Assert.IsTrue(Directory.Exists(stage));

            Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
                arguments,
                out IUpdateActivationGate? retry,
                runningRoot: target));
            Assert.IsNotNull(retry);
            retry.ConfirmShellReady();
            Assert.IsTrue(await retry.Completion.WaitAsync(TimeSpan.FromSeconds(5)));
            Assert.IsFalse(Directory.Exists(stage));
        }
        finally
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                previous);
        }
    }

    [TestMethod]
    [DoNotParallelize]
    public async Task UpdaterWaitFailureRetainsStageAndBackup()
    {
        string container = Root();
        string target = Path.Combine(container, "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");
        string stage = Path.Combine(container, ".VibeTable.Next.update-wait-failure");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        string backup = Path.Combine(stage, "backup");
        Directory.CreateDirectory(backup);
        string backupSentinel = Path.Combine(backup, "VibeTable.Next.exe");
        File.WriteAllText(backupSentinel, "old");
        string token = new('d', 64);
        var plan = new UpdateApplyPlan(
            1, target, source, stage, 123, "1.0.0", "1.1.0", token, SmokeTest: true);
        File.WriteAllText(
            Path.Combine(stage, "update-plan.json"),
            System.Text.Json.JsonSerializer.Serialize(plan));
        string? previous = Environment.GetEnvironmentVariable(
            UpdateProcessCommand.SmokeTokenEnvironmentVariable);

        try
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                token);
            Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
                [
                    "--cleanup-update", stage,
                    "--updater-pid", int.MaxValue.ToString(),
                    "--update-token", token,
                    "--self-update-smoke",
                ],
                out IUpdateActivationGate? gate,
                runningRoot: target,
                waitForUpdaterExit: _ => Task.FromResult(false)));
            Assert.IsNotNull(gate);
            gate.ConfirmShellReady();

            Assert.IsFalse(await gate.Completion.WaitAsync(TimeSpan.FromSeconds(5)));
            Assert.IsTrue(Directory.Exists(stage));
            Assert.AreEqual("old", File.ReadAllText(backupSentinel));
            Assert.IsFalse(File.Exists(Path.Combine(
                target,
                UpdateProcessCommand.SmokeCompletionFileName)));
        }
        finally
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                previous);
        }
    }

    [TestMethod]
    [DoNotParallelize]
    public async Task ConcurrentActivationGatesAllowOnlyOneCleanupWinner()
    {
        string container = Root();
        string target = Path.Combine(container, "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");
        string stage = Path.Combine(container, ".VibeTable.Next.update-race");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        Directory.CreateDirectory(Path.Combine(stage, "backup"));
        string token = new('9', 64);
        var plan = new UpdateApplyPlan(
            1, target, source, stage, 123, "1.0.0", "1.1.0", token, SmokeTest: true);
        File.WriteAllText(
            Path.Combine(stage, "update-plan.json"),
            System.Text.Json.JsonSerializer.Serialize(plan));
        string? previous = Environment.GetEnvironmentVariable(
            UpdateProcessCommand.SmokeTokenEnvironmentVariable);
        string[] arguments =
        [
            "--cleanup-update", stage,
            "--updater-pid", int.MaxValue.ToString(),
            "--update-token", token,
            "--self-update-smoke",
        ];

        try
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                token);
            Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
                arguments, out IUpdateActivationGate? first, runningRoot: target));
            Assert.IsTrue(UpdateProcessCommand.TryCreateActivationGate(
                arguments, out IUpdateActivationGate? second, runningRoot: target));
            Assert.IsNotNull(first);
            Assert.IsNotNull(second);

            await Task.WhenAll(
                Task.Run(first.ConfirmShellReady),
                Task.Run(second.ConfirmShellReady));
            bool[] results = await Task.WhenAll(
                first.Completion.WaitAsync(TimeSpan.FromSeconds(5)),
                second.Completion.WaitAsync(TimeSpan.FromSeconds(5)));

            Assert.AreEqual(1, results.Count(result => result));
            Assert.IsFalse(
                Directory.Exists(stage),
                Directory.Exists(stage)
                    ? string.Join(", ", Directory.EnumerateFileSystemEntries(
                        stage, "*", SearchOption.AllDirectories))
                    : "stage absent");
            Assert.IsTrue(File.Exists(Path.Combine(
                target,
                UpdateProcessCommand.SmokeCompletionFileName)));
        }
        finally
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                previous);
        }
    }

    [TestMethod]
    [DoNotParallelize]
    public void SmokePlanRequiresTheMatchingProcessEnvironmentToken()
    {
        string target = CreateInstalledPackage("1.0.0");
        string stage = Path.Combine(Path.GetDirectoryName(target)!, ".VibeTable.Next.update-smoke");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        string token = new('d', 64);
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            token,
            SmokeTest: true);
        string planPath = Path.Combine(stage, "update-plan.json");
        File.WriteAllText(planPath, System.Text.Json.JsonSerializer.Serialize(plan));
        string? previous = Environment.GetEnvironmentVariable(
            UpdateProcessCommand.SmokeTokenEnvironmentVariable);

        try
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                null);
            ReleaseUpdateException rejected = Assert.ThrowsExactly<ReleaseUpdateException>(() =>
                UpdateProcessCommand.ReadAndValidatePlan(planPath));
            Assert.AreEqual("UPDATE_PLAN_INVALID", rejected.Code);

            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                token);
            UpdateApplyPlan validated = UpdateProcessCommand.ReadAndValidatePlan(planPath);
            Assert.IsTrue(validated.SmokeTest);
        }
        finally
        {
            Environment.SetEnvironmentVariable(
                UpdateProcessCommand.SmokeTokenEnvironmentVariable,
                previous);
        }
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
