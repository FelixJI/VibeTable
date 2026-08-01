using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.IO.Compression;
using System.Linq;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Security.Cryptography;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

internal sealed record ReleaseUpdateNote(
    string Version,
    string Title,
    string Body,
    DateTimeOffset? PublishedAt,
    string ReleaseUrl);

internal sealed record ReleaseUpdateCheckResult(
    string CurrentVersion,
    string LatestVersion,
    bool UpdateAvailable,
    bool CanInstall,
    string? InstallUnavailableReason,
    long DownloadBytes,
    string? ReleaseUrl,
    bool NotesTruncated,
    IReadOnlyList<ReleaseUpdateNote> Releases);

internal sealed record ReleaseUpdateCandidate(
    StableReleaseVersion Version,
    Uri DownloadUri,
    string Sha256,
    long Size,
    string ReleaseUrl,
    IReadOnlyList<ReleaseUpdateNote> Releases,
    bool NotesTruncated);

internal readonly record struct StableReleaseVersion(int Major, int Minor, int Patch)
    : IComparable<StableReleaseVersion>
{
    private static readonly Regex Pattern = new(
        @"^v?(\d+)\.(\d+)\.(\d+)$",
        RegexOptions.CultureInvariant);

    public static bool TryParse(string? value, out StableReleaseVersion version)
    {
        version = default;
        Match match = Pattern.Match(value?.Trim() ?? "");
        if (!match.Success
            || !int.TryParse(match.Groups[1].Value, NumberStyles.None,
                CultureInfo.InvariantCulture, out int major)
            || !int.TryParse(match.Groups[2].Value, NumberStyles.None,
                CultureInfo.InvariantCulture, out int minor)
            || !int.TryParse(match.Groups[3].Value, NumberStyles.None,
                CultureInfo.InvariantCulture, out int patch))
        {
            return false;
        }
        version = new StableReleaseVersion(major, minor, patch);
        return true;
    }

    public int CompareTo(StableReleaseVersion other)
    {
        int major = Major.CompareTo(other.Major);
        if (major != 0) return major;
        int minor = Minor.CompareTo(other.Minor);
        return minor != 0 ? minor : Patch.CompareTo(other.Patch);
    }

    public override string ToString() => $"{Major}.{Minor}.{Patch}";

    public static bool operator >(StableReleaseVersion left, StableReleaseVersion right) =>
        left.CompareTo(right) > 0;

    public static bool operator <(StableReleaseVersion left, StableReleaseVersion right) =>
        left.CompareTo(right) < 0;

    public static bool operator >=(StableReleaseVersion left, StableReleaseVersion right) =>
        left.CompareTo(right) >= 0;

    public static bool operator <=(StableReleaseVersion left, StableReleaseVersion right) =>
        left.CompareTo(right) <= 0;
}

internal static class UpdateDownloadProxy
{
    private static readonly IReadOnlyDictionary<string, Uri> FixedPrefixes =
        new Dictionary<string, Uri>(StringComparer.Ordinal)
        {
            [UpdateProxyOptions.GhProxyNet] = new("https://ghproxy.net/"),
            [UpdateProxyOptions.GhProxyCom] = new("https://gh-proxy.com/"),
        };

    public static Uri Rewrite(
        Uri directDownloadUri,
        string proxy,
        string? customProxyUrl)
    {
        ArgumentNullException.ThrowIfNull(directDownloadUri);
        if (directDownloadUri.Scheme != Uri.UriSchemeHttps
            || !directDownloadUri.Host.Equals("github.com", StringComparison.OrdinalIgnoreCase))
        {
            throw new ReleaseUpdateException(
                "GitHub 返回了不受信任的更新下载地址。",
                "UPDATE_DOWNLOAD_URL_UNTRUSTED");
        }
        if (proxy == UpdateProxyOptions.Direct) return directDownloadUri;

        Uri prefix;
        if (proxy == UpdateProxyOptions.Custom)
        {
            prefix = ParseCustomPrefix(customProxyUrl);
        }
        else if (!FixedPrefixes.TryGetValue(proxy, out prefix!))
        {
            throw new ReleaseUpdateException(
                "选择的 GitHub 下载代理无效。",
                "UPDATE_PROXY_INVALID");
        }
        return new Uri(prefix.AbsoluteUri + directDownloadUri.AbsoluteUri);
    }

    internal static Uri ParseCustomPrefix(string? value)
    {
        string normalized = value?.Trim() ?? "";
        if (!normalized.EndsWith("/", StringComparison.Ordinal)) normalized += "/";
        if (!Uri.TryCreate(normalized, UriKind.Absolute, out Uri? uri)
            || uri.Scheme != Uri.UriSchemeHttps
            || string.IsNullOrWhiteSpace(uri.Host)
            || !string.IsNullOrEmpty(uri.UserInfo)
            || !string.IsNullOrEmpty(uri.Query)
            || !string.IsNullOrEmpty(uri.Fragment))
        {
            throw new ReleaseUpdateException(
                "自定义 GitHub 代理必须是 HTTPS 前缀地址，且不能包含账号、查询参数或片段。",
                "UPDATE_PROXY_INVALID");
        }
        return uri;
    }
}

internal sealed class GitHubReleaseUpdateClient
{
    internal static readonly Uri ReleasesUri = new(
        "https://api.github.com/repos/FelixJI/VibeTable/releases?per_page=100");

    private readonly HttpClient _httpClient;

    public GitHubReleaseUpdateClient()
        : this(new HttpClient(new HttpClientHandler
        {
            AllowAutoRedirect = true,
            AutomaticDecompression = System.Net.DecompressionMethods.All,
        }))
    {
    }

    internal GitHubReleaseUpdateClient(HttpClient httpClient)
    {
        _httpClient = httpClient;
        _httpClient.Timeout = TimeSpan.FromSeconds(20);
        if (!_httpClient.DefaultRequestHeaders.UserAgent.Any())
        {
            _httpClient.DefaultRequestHeaders.UserAgent.Add(
                new ProductInfoHeaderValue("VibeTable", "1"));
        }
        _httpClient.DefaultRequestHeaders.Accept.Add(
            new MediaTypeWithQualityHeaderValue("application/vnd.github+json"));
        _httpClient.DefaultRequestHeaders.TryAddWithoutValidation(
            "X-GitHub-Api-Version",
            "2022-11-28");
    }

    public async Task<ReleaseUpdateCandidate?> CheckAsync(
        string currentVersion,
        string proxy,
        string? customProxyUrl,
        CancellationToken cancellationToken)
    {
        if (!StableReleaseVersion.TryParse(currentVersion, out StableReleaseVersion current))
        {
            throw new ReleaseUpdateException(
                "当前程序版本不是可更新的稳定版本。",
                "UPDATE_CURRENT_VERSION_INVALID");
        }

        using HttpResponseMessage response = await _httpClient.GetAsync(
            ReleasesUri,
            HttpCompletionOption.ResponseHeadersRead,
            cancellationToken).ConfigureAwait(false);
        if (!response.IsSuccessStatusCode)
        {
            throw new ReleaseUpdateException(
                $"GitHub Release 检查失败（HTTP {(int)response.StatusCode}）。",
                "UPDATE_CHECK_HTTP_FAILED");
        }

        await using Stream stream = await response.Content.ReadAsStreamAsync(cancellationToken)
            .ConfigureAwait(false);
        GitHubReleaseDto[]? payload;
        try
        {
            payload = await JsonSerializer.DeserializeAsync<GitHubReleaseDto[]>(
                stream,
                cancellationToken: cancellationToken).ConfigureAwait(false);
        }
        catch (JsonException exception)
        {
            throw new ReleaseUpdateException(
                "GitHub Release 响应格式无效。",
                "UPDATE_CHECK_RESPONSE_INVALID",
                exception);
        }

        List<(StableReleaseVersion Version, GitHubReleaseDto Release)> stable = [];
        foreach (GitHubReleaseDto release in payload ?? [])
        {
            if (release.Draft || release.Prerelease || release.PublishedAt is null
                || !StableReleaseVersion.TryParse(release.TagName, out StableReleaseVersion version))
            {
                continue;
            }
            stable.Add((version, release));
        }
        stable.Sort((left, right) => right.Version.CompareTo(left.Version));
        (StableReleaseVersion Version, GitHubReleaseDto Release)[] newer = stable
            .Where(item => item.Version > current)
            .ToArray();
        if (newer.Length == 0) return null;
        (StableReleaseVersion Version, GitHubReleaseDto Release) latest = newer[0];

        string assetName = $"VibeTable-v{latest.Version}-win-x64.zip";
        GitHubAssetDto? asset = (latest.Release.Assets ?? []).FirstOrDefault(item =>
            item.State == "uploaded" && item.Name == assetName);
        if (asset is null
            || asset.Size <= 0
            || !TryParseSha256Digest(asset.Digest, out string? sha256)
            || !Uri.TryCreate(asset.BrowserDownloadUrl, UriKind.Absolute, out Uri? downloadUri))
        {
            throw new ReleaseUpdateException(
                $"GitHub Release v{latest.Version} 缺少可验证的 Windows x64 更新包。",
                "UPDATE_ASSET_INVALID");
        }

        IReadOnlyList<ReleaseUpdateNote> notes = stable
            .Where(item => item.Version > current && item.Version <= latest.Version)
            .Select(item => new ReleaseUpdateNote(
                item.Version.ToString(),
                string.IsNullOrWhiteSpace(item.Release.Name)
                    ? $"v{item.Version}"
                    : item.Release.Name!,
                TruncateReleaseBody(item.Release.Body),
                item.Release.PublishedAt,
                item.Release.HtmlUrl ?? ""))
            .ToArray();
        bool currentReleaseIsPresent = stable.Any(item => item.Version == current);
        bool notesTruncated = !currentReleaseIsPresent
            && stable.Count > 0
            && current < stable.Min(item => item.Version);

        return new ReleaseUpdateCandidate(
            latest.Version,
            UpdateDownloadProxy.Rewrite(downloadUri, proxy, customProxyUrl),
            sha256!,
            asset.Size,
            latest.Release.HtmlUrl ?? "",
            notes,
            notesTruncated);
    }

    private static bool TryParseSha256Digest(string? digest, out string? sha256)
    {
        sha256 = null;
        const string prefix = "sha256:";
        if (digest is null || !digest.StartsWith(prefix, StringComparison.OrdinalIgnoreCase))
        {
            return false;
        }
        string value = digest[prefix.Length..];
        if (value.Length != 64 || value.Any(character => !Uri.IsHexDigit(character)))
        {
            return false;
        }
        sha256 = value.ToLowerInvariant();
        return true;
    }

    private static string TruncateReleaseBody(string? body)
    {
        const int maxCharacters = 128 * 1024;
        string value = body?.Trim() ?? "";
        return value.Length <= maxCharacters
            ? value
            : value[..maxCharacters] + "\n\n…";
    }

    private sealed record GitHubReleaseDto(
        [property: JsonPropertyName("tag_name")] string? TagName,
        [property: JsonPropertyName("name")] string? Name,
        [property: JsonPropertyName("body")] string? Body,
        [property: JsonPropertyName("html_url")] string? HtmlUrl,
        [property: JsonPropertyName("draft")] bool Draft,
        [property: JsonPropertyName("prerelease")] bool Prerelease,
        [property: JsonPropertyName("published_at")] DateTimeOffset? PublishedAt,
        [property: JsonPropertyName("assets")] GitHubAssetDto[]? Assets);

    private sealed record GitHubAssetDto(
        [property: JsonPropertyName("name")] string? Name,
        [property: JsonPropertyName("state")] string? State,
        [property: JsonPropertyName("size")] long Size,
        [property: JsonPropertyName("digest")] string? Digest,
        [property: JsonPropertyName("browser_download_url")] string? BrowserDownloadUrl);
}

internal sealed record InstalledPackageIdentity(
    string Product,
    string Version,
    string Platform,
    string Architecture)
{
    public static InstalledPackageIdentity Read(string root)
    {
        string manifestPath = Path.Combine(Path.GetFullPath(root), "release.json");
        try
        {
            using FileStream stream = File.OpenRead(manifestPath);
            InstalledPackageIdentity? identity = JsonSerializer.Deserialize<InstalledPackageIdentity>(
                stream,
                new JsonSerializerOptions { PropertyNameCaseInsensitive = true });
            if (identity is null
                || identity.Product != "VibeTable"
                || identity.Platform != "windows"
                || identity.Architecture != "x64"
                || !StableReleaseVersion.TryParse(identity.Version, out _))
            {
                throw new InvalidDataException("release.json identity is incomplete.");
            }
            return identity;
        }
        catch (Exception exception) when (exception is
            IOException or JsonException or InvalidDataException)
        {
            throw new ReleaseUpdateException(
                "当前程序不是可安全原地更新的 VibeTable Windows x64 发布包。",
                "UPDATE_INSTALL_LAYOUT_UNSUPPORTED",
                exception);
        }
    }
}

internal sealed record UpdateApplyPlan(
    int SchemaVersion,
    string TargetRoot,
    string SourceRoot,
    string StagingRoot,
    int ParentProcessId,
    string CurrentVersion,
    string TargetVersion,
    string Token);

internal sealed class ReleasePackageStager
{
    internal const long MaxArchiveBytes = 2L * 1024 * 1024 * 1024;
    internal const long MaxExpandedBytes = 8L * 1024 * 1024 * 1024;
    internal const int MaxEntries = 20_000;
    private readonly HttpClient _httpClient;

    public ReleasePackageStager()
        : this(new HttpClient(new HttpClientHandler { AllowAutoRedirect = true }))
    {
    }

    internal ReleasePackageStager(HttpClient httpClient)
    {
        _httpClient = httpClient;
        _httpClient.Timeout = TimeSpan.FromMinutes(30);
    }

    public async Task<string> StageAsync(
        ReleaseUpdateCandidate candidate,
        string installRoot,
        string currentVersion,
        int parentProcessId,
        CancellationToken cancellationToken)
    {
        string targetRoot = NormalizeDirectory(installRoot);
        InstalledPackageIdentity current = InstalledPackageIdentity.Read(targetRoot);
        if (!StableReleaseVersion.TryParse(current.Version, out StableReleaseVersion installed)
            || !StableReleaseVersion.TryParse(currentVersion, out StableReleaseVersion running)
            || installed != running)
        {
            throw new ReleaseUpdateException(
                "运行版本与安装目录中的 release.json 不一致，已拒绝更新。",
                "UPDATE_INSTALL_IDENTITY_MISMATCH");
        }
        if (candidate.Size > MaxArchiveBytes)
        {
            throw new ReleaseUpdateException(
                "更新包超过允许的大小上限。",
                "UPDATE_ASSET_TOO_LARGE");
        }

        DirectoryInfo? parent = Directory.GetParent(targetRoot.TrimEnd(Path.DirectorySeparatorChar));
        if (parent is null)
        {
            throw new ReleaseUpdateException(
                "无法确定更新暂存目录。",
                "UPDATE_STAGING_UNAVAILABLE");
        }
        string stageRoot = Path.Combine(
            parent.FullName,
            $".VibeTable.Next.update-{candidate.Version}-{Guid.NewGuid():N}");
        Directory.CreateDirectory(stageRoot);
        string archivePath = Path.Combine(stageRoot, "package.zip");
        await DownloadVerifiedAsync(candidate, archivePath, cancellationToken)
            .ConfigureAwait(false);

        string packageContainer = Path.Combine(stageRoot, "package");
        string sourceRoot = ExtractVerifiedPackage(
            archivePath,
            packageContainer,
            candidate.Version.ToString());
        var plan = new UpdateApplyPlan(
            1,
            targetRoot,
            sourceRoot,
            stageRoot,
            parentProcessId,
            current.Version,
            candidate.Version.ToString(),
            Convert.ToHexString(RandomNumberGenerator.GetBytes(32)).ToLowerInvariant());
        string planPath = Path.Combine(stageRoot, "update-plan.json");
        await using FileStream planStream = new(
            planPath,
            FileMode.CreateNew,
            FileAccess.Write,
            FileShare.None);
        await JsonSerializer.SerializeAsync(
            planStream,
            plan,
            cancellationToken: cancellationToken).ConfigureAwait(false);
        await planStream.FlushAsync(cancellationToken).ConfigureAwait(false);
        return planPath;
    }

    private async Task DownloadVerifiedAsync(
        ReleaseUpdateCandidate candidate,
        string archivePath,
        CancellationToken cancellationToken)
    {
        using HttpResponseMessage response = await _httpClient.GetAsync(
            candidate.DownloadUri,
            HttpCompletionOption.ResponseHeadersRead,
            cancellationToken).ConfigureAwait(false);
        if (!response.IsSuccessStatusCode)
        {
            throw new ReleaseUpdateException(
                $"更新包下载失败（HTTP {(int)response.StatusCode}）。",
                "UPDATE_DOWNLOAD_FAILED");
        }
        if (response.Content.Headers.ContentLength is long contentLength
            && contentLength != candidate.Size)
        {
            throw new ReleaseUpdateException(
                "更新包下载长度与 GitHub Release 元数据不一致。",
                "UPDATE_DOWNLOAD_SIZE_MISMATCH");
        }

        await using Stream input = await response.Content.ReadAsStreamAsync(cancellationToken)
            .ConfigureAwait(false);
        await using FileStream output = new(
            archivePath,
            FileMode.CreateNew,
            FileAccess.Write,
            FileShare.None,
            1024 * 1024,
            FileOptions.Asynchronous | FileOptions.SequentialScan);
        using IncrementalHash hash = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
        byte[] buffer = new byte[1024 * 1024];
        long total = 0;
        int read;
        while ((read = await input.ReadAsync(buffer, cancellationToken).ConfigureAwait(false)) > 0)
        {
            total += read;
            if (total > candidate.Size || total > MaxArchiveBytes)
            {
                throw new ReleaseUpdateException(
                    "更新包下载超过 GitHub 声明的大小。",
                    "UPDATE_DOWNLOAD_SIZE_MISMATCH");
            }
            hash.AppendData(buffer, 0, read);
            await output.WriteAsync(buffer.AsMemory(0, read), cancellationToken)
                .ConfigureAwait(false);
        }
        await output.FlushAsync(cancellationToken).ConfigureAwait(false);
        string actual = Convert.ToHexString(hash.GetHashAndReset()).ToLowerInvariant();
        if (total != candidate.Size)
        {
            throw new ReleaseUpdateException(
                "更新包下载不完整。",
                "UPDATE_DOWNLOAD_SIZE_MISMATCH");
        }
        if (!CryptographicOperations.FixedTimeEquals(
                Convert.FromHexString(actual),
                Convert.FromHexString(candidate.Sha256)))
        {
            throw new ReleaseUpdateException(
                "更新包 SHA-256 与 GitHub Release 元数据不一致。",
                "UPDATE_DOWNLOAD_DIGEST_MISMATCH");
        }
    }

    internal static string ExtractVerifiedPackage(
        string archivePath,
        string destination,
        string expectedVersion)
    {
        Directory.CreateDirectory(destination);
        string destinationRoot = NormalizeDirectory(destination);
        var seen = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
        long expanded = 0;
        int entryCount = 0;
        using ZipArchive archive = ZipFile.OpenRead(archivePath);
        foreach (ZipArchiveEntry entry in archive.Entries)
        {
            entryCount++;
            if (entryCount > MaxEntries)
            {
                throw new ReleaseUpdateException(
                    "更新包文件数量超过安全上限。",
                    "UPDATE_ARCHIVE_LIMIT_EXCEEDED");
            }
            if (entry.FullName.Contains('\\')
                || entry.FullName.StartsWith("/", StringComparison.Ordinal))
            {
                throw UnsafeArchive(entry.FullName);
            }
            string[] parts = entry.FullName.Split('/', StringSplitOptions.RemoveEmptyEntries);
            if (parts.Length == 0 || parts[0] != "VibeTable"
                || entry.FullName.Length > 4096
                || parts.Any(IsUnsafeWindowsPathPart))
            {
                throw UnsafeArchive(entry.FullName);
            }
            if (IsSymbolicLink(entry)) throw UnsafeArchive(entry.FullName);
            if (entry.FullName.EndsWith("/", StringComparison.Ordinal)) continue;

            string relative = Path.Combine(parts.Skip(1).ToArray());
            if (string.IsNullOrEmpty(relative) || !seen.Add(relative))
            {
                throw UnsafeArchive(entry.FullName);
            }
            expanded = checked(expanded + entry.Length);
            if (expanded > MaxExpandedBytes)
            {
                throw new ReleaseUpdateException(
                    "更新包展开大小超过安全上限。",
                    "UPDATE_ARCHIVE_LIMIT_EXCEEDED");
            }
            string outputPath = Path.GetFullPath(Path.Combine(destinationRoot, "VibeTable", relative));
            string packageRoot = NormalizeDirectory(Path.Combine(destinationRoot, "VibeTable"));
            if (!outputPath.StartsWith(packageRoot, StringComparison.OrdinalIgnoreCase))
            {
                throw UnsafeArchive(entry.FullName);
            }
            Directory.CreateDirectory(Path.GetDirectoryName(outputPath)!);
            using Stream input = entry.Open();
            using FileStream output = new(outputPath, FileMode.CreateNew, FileAccess.Write, FileShare.None);
            input.CopyTo(output);
        }

        string sourceRoot = Path.Combine(destinationRoot, "VibeTable");
        ValidatePackageRoot(sourceRoot, expectedVersion);
        return NormalizeDirectory(sourceRoot);
    }

    internal static void ValidatePackageRoot(string sourceRoot, string expectedVersion)
    {
        string root = NormalizeDirectory(sourceRoot);
        var allowed = new HashSet<string>(StringComparer.OrdinalIgnoreCase)
        {
            "VibeTable.Next.exe",
            "release.json",
            "resources",
        };
        foreach (string path in Directory.EnumerateFileSystemEntries(root))
        {
            if (!allowed.Contains(Path.GetFileName(path)))
            {
                throw new ReleaseUpdateException(
                    "更新包包含包契约之外的根目录条目。",
                    "UPDATE_PACKAGE_LAYOUT_INVALID");
            }
        }
        if (!File.Exists(Path.Combine(root, "VibeTable.Next.exe"))
            || !Directory.Exists(Path.Combine(root, "resources")))
        {
            throw new ReleaseUpdateException(
                "更新包缺少桌面程序或资源目录。",
                "UPDATE_PACKAGE_LAYOUT_INVALID");
        }
        InstalledPackageIdentity identity = InstalledPackageIdentity.Read(root);
        if (identity.Version != expectedVersion)
        {
            throw new ReleaseUpdateException(
                "更新包版本与 GitHub Release 不一致。",
                "UPDATE_PACKAGE_IDENTITY_MISMATCH");
        }
    }

    internal static string NormalizeDirectory(string path) =>
        Path.GetFullPath(path).TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar)
        + Path.DirectorySeparatorChar;

    private static bool IsSymbolicLink(ZipArchiveEntry entry) =>
        ((entry.ExternalAttributes >> 16) & 0xF000) == 0xA000;

    private static bool IsUnsafeWindowsPathPart(string part) =>
        part is "." or ".."
        || part.Length > 255
        || part.EndsWith(' ')
        || part.EndsWith('.')
        || part.IndexOfAny(Path.GetInvalidFileNameChars()) >= 0;

    private static ReleaseUpdateException UnsafeArchive(string entry) => new(
        $"更新包包含不安全路径：{entry}",
        "UPDATE_ARCHIVE_UNSAFE");
}

internal sealed class ReleaseUpdateCoordinator
{
    private readonly GitHubReleaseUpdateClient _client;
    private readonly ReleasePackageStager _stager;
    private readonly string _installRoot;
    private readonly string _currentVersion;
    private readonly object _gate = new();
    private ReleaseUpdateCandidate? _candidate;

    public ReleaseUpdateCoordinator(string installRoot, string currentVersion)
        : this(
            installRoot,
            currentVersion,
            new GitHubReleaseUpdateClient(),
            new ReleasePackageStager())
    {
    }

    internal ReleaseUpdateCoordinator(
        string installRoot,
        string currentVersion,
        GitHubReleaseUpdateClient client,
        ReleasePackageStager stager)
    {
        _installRoot = installRoot;
        _currentVersion = currentVersion;
        _client = client;
        _stager = stager;
    }

    public async Task<ReleaseUpdateCheckResult> CheckAsync(
        AppPreferences preferences,
        CancellationToken cancellationToken)
    {
        ReleaseUpdateCandidate? candidate = await _client.CheckAsync(
            _currentVersion,
            preferences.UpdateProxy,
            preferences.CustomUpdateProxyUrl,
            cancellationToken).ConfigureAwait(false);
        lock (_gate) _candidate = candidate;

        bool canInstall = true;
        string? unavailableReason = null;
        try
        {
            InstalledPackageIdentity identity = InstalledPackageIdentity.Read(_installRoot);
            if (identity.Version != _currentVersion)
            {
                throw new ReleaseUpdateException(
                    "运行版本与安装目录不一致。",
                    "UPDATE_INSTALL_IDENTITY_MISMATCH");
            }
        }
        catch (ReleaseUpdateException exception)
        {
            canInstall = false;
            unavailableReason = exception.Message;
        }

        return new ReleaseUpdateCheckResult(
            _currentVersion,
            candidate?.Version.ToString() ?? _currentVersion,
            candidate is not null,
            canInstall,
            unavailableReason,
            candidate?.Size ?? 0,
            candidate?.ReleaseUrl,
            candidate?.NotesTruncated ?? false,
            candidate?.Releases ?? []);
    }

    public async Task LaunchUpdateAsync(CancellationToken cancellationToken)
    {
        ReleaseUpdateCandidate? candidate;
        lock (_gate) candidate = _candidate;
        if (candidate is null)
        {
            throw new ReleaseUpdateException(
                "请先检查更新，再安装已确认的新版本。",
                "UPDATE_CHECK_REQUIRED");
        }

        string planPath = await _stager.StageAsync(
            candidate,
            _installRoot,
            _currentVersion,
            Environment.ProcessId,
            cancellationToken).ConfigureAwait(false);
        UpdateApplyPlan plan = UpdateProcessCommand.ReadAndValidatePlan(planPath);
        string updater = Path.Combine(plan.SourceRoot, "VibeTable.Next.exe");
        var start = new ProcessStartInfo
        {
            FileName = updater,
            WorkingDirectory = plan.SourceRoot,
            UseShellExecute = false,
        };
        start.ArgumentList.Add("--apply-update");
        start.ArgumentList.Add(planPath);
        using Process? process = Process.Start(start);
        if (process is null)
        {
            throw new ReleaseUpdateException(
                "无法启动进程外更新器。",
                "UPDATE_APPLIER_START_FAILED");
        }
    }
}

internal sealed class ReleaseUpdateException : Exception
{
    public ReleaseUpdateException(string message, string code, Exception? innerException = null)
        : base(message, innerException)
    {
        Code = code;
    }

    public string Code { get; }
}
