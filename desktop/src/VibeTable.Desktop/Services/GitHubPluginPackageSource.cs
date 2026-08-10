using System;
using System.Buffers;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Security.Cryptography;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

internal sealed record GitHubPluginRepository(string Owner, string Name)
{
    private static readonly Regex OwnerPattern = new(
        "^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$",
        RegexOptions.CultureInvariant);
    private static readonly Regex NamePattern = new(
        "^[A-Za-z0-9._-]{1,100}$",
        RegexOptions.CultureInvariant);

    public string Slug => $"{Owner}/{Name}";

    public static GitHubPluginRepository Parse(string? value)
    {
        string candidate = value?.Trim() ?? "";
        if (Uri.TryCreate(candidate, UriKind.Absolute, out Uri? uri))
        {
            if (uri.Scheme != Uri.UriSchemeHttps
                || !uri.Host.Equals("github.com", StringComparison.OrdinalIgnoreCase)
                || !string.IsNullOrEmpty(uri.UserInfo)
                || !string.IsNullOrEmpty(uri.Query)
                || !string.IsNullOrEmpty(uri.Fragment))
            {
                throw InvalidRepository();
            }
            candidate = uri.AbsolutePath.Trim('/');
        }
        if (candidate.EndsWith(".git", StringComparison.OrdinalIgnoreCase))
        {
            candidate = candidate[..^4];
        }
        string[] parts = candidate.Split('/', StringSplitOptions.RemoveEmptyEntries);
        if (parts.Length != 2
            || !OwnerPattern.IsMatch(parts[0])
            || !NamePattern.IsMatch(parts[1])
            || parts[1] is "." or "..")
        {
            throw InvalidRepository();
        }
        return new GitHubPluginRepository(parts[0], parts[1]);
    }

    private static GitHubPluginSourceException InvalidRepository() => new(
        "GitHub 插件仓库必须使用 owner/repo 或标准 github.com 仓库地址。",
        "PLUGIN_GITHUB_REPOSITORY_INVALID");
}

internal sealed class DownloadedPluginPackage : IDisposable
{
    private string? _path;

    public DownloadedPluginPackage(
        string path,
        string repository,
        string tagName,
        string assetName,
        string sha256)
    {
        _path = System.IO.Path.GetFullPath(path);
        Repository = repository;
        TagName = tagName;
        AssetName = assetName;
        Sha256 = sha256;
    }

    public string Path => _path
        ?? throw new ObjectDisposedException(nameof(DownloadedPluginPackage));
    public string Repository { get; }
    public string TagName { get; }
    public string AssetName { get; }
    public string Sha256 { get; }

    public void Dispose()
    {
        string? path = Interlocked.Exchange(ref _path, null);
        if (path is null)
        {
            return;
        }
        try
        {
            File.Delete(path);
        }
        catch (IOException)
        {
            // A locked cache file is inert and will be replaced by a unique
            // name on the next download. Do not turn a completed install into
            // a failure solely because best-effort cache cleanup was delayed.
        }
        catch (UnauthorizedAccessException)
        {
            // The application preferences/download surface reports strict
            // write failures before a lease is returned. Cleanup stays best-effort.
        }
    }
}

internal interface IGitHubPluginPackageSource : IDisposable
{
    Task<DownloadedPluginPackage> DownloadLatestAsync(
        string repository,
        CancellationToken token);
}

internal sealed class GitHubPluginPackageSource : IGitHubPluginPackageSource
{
    internal const long MaxPackageBytes = 64L * 1024 * 1024;
    private const int MetadataLimitBytes = 2 * 1024 * 1024;

    private readonly string _downloadRoot;
    private readonly Func<AppPreferences> _readPreferences;
    private readonly HttpClient _httpClient;
    private readonly bool _ownsHttpClient;
    private bool _disposed;

    public GitHubPluginPackageSource(
        string downloadRoot,
        Func<AppPreferences> readPreferences)
        : this(downloadRoot, readPreferences, CreateHttpClient(), ownsHttpClient: true)
    {
    }

    internal GitHubPluginPackageSource(
        string downloadRoot,
        Func<AppPreferences> readPreferences,
        HttpClient httpClient,
        bool ownsHttpClient = false)
    {
        _downloadRoot = Path.GetFullPath(
            downloadRoot ?? throw new ArgumentNullException(nameof(downloadRoot)));
        _readPreferences = readPreferences
            ?? throw new ArgumentNullException(nameof(readPreferences));
        _httpClient = httpClient ?? throw new ArgumentNullException(nameof(httpClient));
        _ownsHttpClient = ownsHttpClient;
        _httpClient.Timeout = TimeSpan.FromSeconds(30);
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

    public async Task<DownloadedPluginPackage> DownloadLatestAsync(
        string repository,
        CancellationToken token)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        GitHubPluginRepository parsed = GitHubPluginRepository.Parse(repository);
        GitHubPluginReleaseDto release = await ReadLatestReleaseAsync(parsed, token)
            .ConfigureAwait(false);
        GitHubPluginAssetDto asset = SelectAsset(release);
        Uri directUri = ParseDownloadUri(asset.BrowserDownloadUrl);
        AppPreferences preferences = _readPreferences();
        Uri downloadUri;
        try
        {
            downloadUri = UpdateDownloadProxy.Rewrite(
                directUri,
                preferences.UpdateProxy,
                preferences.CustomUpdateProxyUrl);
        }
        catch (ReleaseUpdateException exception)
        {
            throw new GitHubPluginSourceException(
                exception.Message,
                "PLUGIN_GITHUB_PROXY_INVALID",
                exception);
        }

        Directory.CreateDirectory(_downloadRoot);
        string destination = Path.Combine(
            _downloadRoot,
            $"plugin-{Guid.NewGuid():N}.vtplugin");
        string temporary = destination + ".download";
        try
        {
            string actualDigest = await DownloadAsync(
                downloadUri,
                temporary,
                asset.Size,
                token).ConfigureAwait(false);
            string expectedDigest = ParseDigest(asset.Digest);
            if (!CryptographicOperations.FixedTimeEquals(
                    Convert.FromHexString(actualDigest),
                    Convert.FromHexString(expectedDigest)))
            {
                throw new GitHubPluginSourceException(
                    "GitHub 插件资产的 SHA-256 与 Release metadata 不一致。",
                    "PLUGIN_GITHUB_DIGEST_MISMATCH");
            }
            File.Move(temporary, destination);
            return new DownloadedPluginPackage(
                destination,
                parsed.Slug,
                release.TagName ?? "",
                asset.Name ?? "",
                actualDigest);
        }
        catch
        {
            File.Delete(temporary);
            File.Delete(destination);
            throw;
        }
    }

    public void Dispose()
    {
        if (_disposed)
        {
            return;
        }
        _disposed = true;
        if (_ownsHttpClient)
        {
            _httpClient.Dispose();
        }
    }

    private async Task<GitHubPluginReleaseDto> ReadLatestReleaseAsync(
        GitHubPluginRepository repository,
        CancellationToken token)
    {
        var uri = new Uri(
            $"https://api.github.com/repos/{Uri.EscapeDataString(repository.Owner)}/"
            + $"{Uri.EscapeDataString(repository.Name)}/releases/latest");
        using HttpResponseMessage response = await _httpClient.GetAsync(
            uri,
            HttpCompletionOption.ResponseHeadersRead,
            token).ConfigureAwait(false);
        if (response.StatusCode == HttpStatusCode.NotFound)
        {
            throw new GitHubPluginSourceException(
                "GitHub 仓库不存在、不可公开访问，或尚未发布正式 Release。",
                "PLUGIN_GITHUB_RELEASE_NOT_FOUND");
        }
        if (!response.IsSuccessStatusCode)
        {
            throw new GitHubPluginSourceException(
                $"GitHub 插件 Release 检查失败（HTTP {(int)response.StatusCode}）。",
                "PLUGIN_GITHUB_RELEASE_HTTP_FAILED");
        }
        if (response.Content.Headers.ContentLength > MetadataLimitBytes)
        {
            throw InvalidMetadata();
        }
        byte[] payload = await response.Content.ReadAsByteArrayAsync(token).ConfigureAwait(false);
        if (payload.Length > MetadataLimitBytes)
        {
            throw InvalidMetadata();
        }
        try
        {
            GitHubPluginReleaseDto? release = JsonSerializer.Deserialize<GitHubPluginReleaseDto>(payload);
            if (release is null
                || release.Draft
                || release.Prerelease
                || release.PublishedAt is null
                || string.IsNullOrWhiteSpace(release.TagName))
            {
                throw InvalidMetadata();
            }
            return release;
        }
        catch (JsonException exception)
        {
            throw new GitHubPluginSourceException(
                "GitHub 插件 Release metadata 格式无效。",
                "PLUGIN_GITHUB_RELEASE_INVALID",
                exception);
        }
    }

    private static GitHubPluginAssetDto SelectAsset(GitHubPluginReleaseDto release)
    {
        GitHubPluginAssetDto[] candidates = (release.Assets ?? [])
            .Where(asset => asset.State == "uploaded"
                && asset.Name?.EndsWith(".vtplugin", StringComparison.OrdinalIgnoreCase) == true)
            .ToArray();
        if (candidates.Length != 1)
        {
            throw new GitHubPluginSourceException(
                "正式 Release 必须且只能包含一个可安装的 .vtplugin 资产。",
                "PLUGIN_GITHUB_ASSET_AMBIGUOUS");
        }
        GitHubPluginAssetDto asset = candidates[0];
        if (asset.Size <= 0 || asset.Size > MaxPackageBytes)
        {
            throw new GitHubPluginSourceException(
                "GitHub 插件资产大小无效或超过 64 MiB 上限。",
                "PLUGIN_GITHUB_ASSET_SIZE_INVALID");
        }
        _ = ParseDigest(asset.Digest);
        _ = ParseDownloadUri(asset.BrowserDownloadUrl);
        return asset;
    }

    private async Task<string> DownloadAsync(
        Uri downloadUri,
        string destination,
        long expectedBytes,
        CancellationToken token)
    {
        using HttpResponseMessage response = await _httpClient.GetAsync(
            downloadUri,
            HttpCompletionOption.ResponseHeadersRead,
            token).ConfigureAwait(false);
        if (!response.IsSuccessStatusCode)
        {
            throw new GitHubPluginSourceException(
                $"GitHub 插件资产下载失败（HTTP {(int)response.StatusCode}）。",
                "PLUGIN_GITHUB_DOWNLOAD_HTTP_FAILED");
        }
        long? contentLength = response.Content.Headers.ContentLength;
        if (contentLength is > MaxPackageBytes || contentLength is <= 0)
        {
            throw new GitHubPluginSourceException(
                "GitHub 插件下载响应大小无效或超过 64 MiB 上限。",
                "PLUGIN_GITHUB_DOWNLOAD_SIZE_INVALID");
        }

        await using Stream input = await response.Content.ReadAsStreamAsync(token)
            .ConfigureAwait(false);
        await using FileStream output = new(
            destination,
            FileMode.CreateNew,
            FileAccess.Write,
            FileShare.None,
            64 * 1024,
            FileOptions.Asynchronous | FileOptions.SequentialScan);
        using IncrementalHash hash = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
        byte[] buffer = ArrayPool<byte>.Shared.Rent(64 * 1024);
        long total = 0;
        try
        {
            while (true)
            {
                int read = await input.ReadAsync(buffer.AsMemory(0, buffer.Length), token)
                    .ConfigureAwait(false);
                if (read == 0)
                {
                    break;
                }
                total += read;
                if (total > MaxPackageBytes)
                {
                    throw new GitHubPluginSourceException(
                        "GitHub 插件下载超过 64 MiB 上限。",
                        "PLUGIN_GITHUB_DOWNLOAD_SIZE_INVALID");
                }
                hash.AppendData(buffer, 0, read);
                await output.WriteAsync(buffer.AsMemory(0, read), token).ConfigureAwait(false);
            }
            await output.FlushAsync(token).ConfigureAwait(false);
        }
        finally
        {
            ArrayPool<byte>.Shared.Return(buffer);
        }
        if (total != expectedBytes || (contentLength.HasValue && total != contentLength.Value))
        {
            throw new GitHubPluginSourceException(
                "GitHub 插件下载字节数与 Release metadata 不一致。",
                "PLUGIN_GITHUB_DOWNLOAD_SIZE_MISMATCH");
        }
        return Convert.ToHexString(hash.GetHashAndReset()).ToLowerInvariant();
    }

    private static Uri ParseDownloadUri(string? value)
    {
        if (!Uri.TryCreate(value, UriKind.Absolute, out Uri? uri)
            || uri.Scheme != Uri.UriSchemeHttps
            || !uri.Host.Equals("github.com", StringComparison.OrdinalIgnoreCase))
        {
            throw new GitHubPluginSourceException(
                "GitHub Release 返回了不受信任的插件下载地址。",
                "PLUGIN_GITHUB_DOWNLOAD_URL_UNTRUSTED");
        }
        return uri;
    }

    private static string ParseDigest(string? value)
    {
        const string prefix = "sha256:";
        if (value is null || !value.StartsWith(prefix, StringComparison.OrdinalIgnoreCase))
        {
            throw InvalidDigest();
        }
        string digest = value[prefix.Length..];
        if (digest.Length != 64 || digest.Any(character => !Uri.IsHexDigit(character)))
        {
            throw InvalidDigest();
        }
        return digest.ToLowerInvariant();
    }

    private static GitHubPluginSourceException InvalidDigest() => new(
        "GitHub 插件资产缺少有效的 SHA-256 digest。",
        "PLUGIN_GITHUB_DIGEST_INVALID");

    private static GitHubPluginSourceException InvalidMetadata() => new(
        "GitHub 插件 Release metadata 格式无效。",
        "PLUGIN_GITHUB_RELEASE_INVALID");

    private static HttpClient CreateHttpClient() => new(new HttpClientHandler
    {
        AllowAutoRedirect = true,
        AutomaticDecompression = DecompressionMethods.All,
    });

    private sealed record GitHubPluginReleaseDto(
        [property: JsonPropertyName("tag_name")] string? TagName,
        [property: JsonPropertyName("draft")] bool Draft,
        [property: JsonPropertyName("prerelease")] bool Prerelease,
        [property: JsonPropertyName("published_at")] DateTimeOffset? PublishedAt,
        [property: JsonPropertyName("assets")] GitHubPluginAssetDto[]? Assets);

    private sealed record GitHubPluginAssetDto(
        [property: JsonPropertyName("name")] string? Name,
        [property: JsonPropertyName("size")] long Size,
        [property: JsonPropertyName("state")] string? State,
        [property: JsonPropertyName("digest")] string? Digest,
        [property: JsonPropertyName("browser_download_url")] string? BrowserDownloadUrl);
}

internal sealed class GitHubPluginSourceException : Exception
{
    public GitHubPluginSourceException(
        string message,
        string code,
        Exception? innerException = null)
        : base(message, innerException)
    {
        Code = code;
    }

    public string Code { get; }
}
