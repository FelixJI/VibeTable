using System;
using System.Buffers;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Public code hosts whose Release API can serve installable plugin packages.
/// Release metadata is always read from the host's official API; only GitHub
/// asset downloads may be routed through the user's configured download proxy.
/// </summary>
internal enum PluginReleaseHost
{
    GitHub,
    Codeberg,
}

/// <summary>Host-specific release API and trust profile.</summary>
internal sealed record PluginReleaseHostProfile(
    PluginReleaseHost Host,
    string DisplayName,
    string ErrorCodePrefix,
    string WebHost,
    string ApiBaseUri,
    string Accept,
    string? ApiVersionHeader,
    string? ApiVersionValue,
    bool RequiresUploadedState,
    bool UsesChecksumSidecar,
    Regex OwnerPattern)
{
    public string Error(string suffix) => ErrorCodePrefix + suffix;
}

internal static class PluginReleaseHosts
{
    private static readonly Regex GitHubOwnerPattern = new(
        "^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$",
        RegexOptions.CultureInvariant);
    private static readonly Regex CodebergOwnerPattern = new(
        "^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,38}[A-Za-z0-9])?$",
        RegexOptions.CultureInvariant);

    public static readonly PluginReleaseHostProfile GitHub = new(
        PluginReleaseHost.GitHub,
        "GitHub",
        "PLUGIN_GITHUB_",
        "github.com",
        "https://api.github.com/repos",
        "application/vnd.github+json",
        "X-GitHub-Api-Version",
        "2022-11-28",
        RequiresUploadedState: true,
        UsesChecksumSidecar: false,
        OwnerPattern: GitHubOwnerPattern);

    // Codeberg (Forgejo) may publish an empty `digest`; the release's unique
    // `<asset>.sha256` attachment is the authoritative checksum there.
    public static readonly PluginReleaseHostProfile Codeberg = new(
        PluginReleaseHost.Codeberg,
        "Codeberg",
        "PLUGIN_CODEBERG_",
        "codeberg.org",
        "https://codeberg.org/api/v1/repos",
        "application/json",
        ApiVersionHeader: null,
        ApiVersionValue: null,
        RequiresUploadedState: false,
        UsesChecksumSidecar: true,
        OwnerPattern: CodebergOwnerPattern);

    public static readonly IReadOnlyList<PluginReleaseHostProfile> All = [GitHub, Codeberg];

    /// <summary>GitHub download mirrors that users may paste in front of a
    /// github.com URL. The pasted mirror routes that single asset download;
    /// otherwise the application update-proxy setting decides the route.</summary>
    public static readonly IReadOnlyList<string> GitHubMirrorPrefixes =
    [
        "https://ghfast.top/",
        "https://ghproxy.net/",
        "https://gh-proxy.com/",
    ];

    public static PluginReleaseHostProfile Get(PluginReleaseHost host) =>
        All.First(profile => profile.Host == host);

    public static PluginReleaseHostProfile? ForWebHost(string host) =>
        All.FirstOrDefault(profile => profile.WebHost.Equals(host, StringComparison.OrdinalIgnoreCase));
}

/// <summary>
/// A user-typed remote plugin source resolved to exactly one public release:
/// a bare GitHub <c>owner/repo</c>, a github.com / codeberg.org repository,
/// release or asset URL, or such a GitHub URL wrapped by a known mirror.
/// A pasted mirror (<see cref="Mirror"/>) routes this one GitHub asset download
/// only; it never wraps Codeberg and never changes persisted preferences.
/// </summary>
internal sealed record RemotePluginReleaseReference(
    PluginReleaseHost Host,
    string Owner,
    string Name,
    string? Tag,
    string? AssetName,
    string? Mirror = null)
{
    internal const int MaxLength = 1024;
    internal const string PackageExtension = ".vtplugin";
    private const int MaxSegmentLength = 255;

    private static readonly Regex NamePattern = new(
        "^[A-Za-z0-9._-]{1,100}$",
        RegexOptions.CultureInvariant);

    public string Slug => $"{Owner}/{Name}";

    public bool IsLatest => Tag is null;

    public static RemotePluginReleaseReference Parse(string? value)
    {
        string candidate = value?.Trim() ?? "";
        if (candidate.Length == 0 || candidate.Length > MaxLength)
        {
            throw Invalid();
        }
        if (candidate.Contains("://", StringComparison.Ordinal))
        {
            return ParseUrl(candidate, allowMirror: true, mirror: null);
        }
        foreach (PluginReleaseHostProfile profile in PluginReleaseHosts.All)
        {
            if (candidate.StartsWith(profile.WebHost + "/", StringComparison.OrdinalIgnoreCase))
            {
                return ParseUrl("https://" + candidate, allowMirror: false, mirror: null);
            }
        }
        // Bare coordinates stay GitHub for backward compatibility.
        return FromSegments(PluginReleaseHosts.GitHub, SplitSegments(candidate), mirror: null);
    }

    private static RemotePluginReleaseReference ParseUrl(string candidate, bool allowMirror, string? mirror)
    {
        if (allowMirror)
        {
            foreach (string known in PluginReleaseHosts.GitHubMirrorPrefixes)
            {
                if (candidate.StartsWith(known, StringComparison.OrdinalIgnoreCase))
                {
                    return ParseUrl(candidate[known.Length..], allowMirror: false, mirror: known);
                }
            }
        }
        if (!Uri.TryCreate(candidate, UriKind.Absolute, out Uri? uri)
            || uri.Scheme != Uri.UriSchemeHttps
            || !uri.IsDefaultPort
            || !string.IsNullOrEmpty(uri.UserInfo)
            || !string.IsNullOrEmpty(uri.Query)
            || !string.IsNullOrEmpty(uri.Fragment))
        {
            throw Invalid();
        }
        PluginReleaseHostProfile? profile = PluginReleaseHosts.ForWebHost(uri.Host);
        // A pasted GitHub mirror may only wrap a github.com URL, never Codeberg.
        if (profile is null || (mirror is not null && profile.Host != PluginReleaseHost.GitHub))
        {
            throw Invalid();
        }
        return FromSegments(profile, SplitSegments(uri.AbsolutePath), mirror);
    }

    private static string[] SplitSegments(string path) =>
        path.Split('/', StringSplitOptions.RemoveEmptyEntries)
            .Select(Uri.UnescapeDataString)
            .ToArray();

    private static RemotePluginReleaseReference FromSegments(
        PluginReleaseHostProfile profile,
        string[] segments,
        string? mirror)
    {
        if (segments.Length < 2 || !profile.OwnerPattern.IsMatch(segments[0]))
        {
            throw Invalid();
        }
        string name = segments[1].EndsWith(".git", StringComparison.OrdinalIgnoreCase)
            ? segments[1][..^4]
            : segments[1];
        if (!NamePattern.IsMatch(name) || name is "." or "..")
        {
            throw Invalid();
        }

        string? tag = null;
        string? assetName = null;
        string[] path = segments[2..];
        if (path.Length > 0)
        {
            if (!IsLiteral(path[0], "releases"))
            {
                throw Invalid();
            }
            string[] rest = path[1..];
            switch (rest.Length)
            {
                case 0:
                    break;
                case 1 when IsLiteral(rest[0], "latest"):
                    break;
                case 2 when IsLiteral(rest[0], "tag"):
                    tag = ValidSegment(rest[1]);
                    break;
                case 3 when IsLiteral(rest[0], "download"):
                    tag = ValidSegment(rest[1]);
                    assetName = ValidAssetName(rest[2]);
                    break;
                case 3 when IsLiteral(rest[0], "latest") && IsLiteral(rest[1], "download"):
                    assetName = ValidAssetName(rest[2]);
                    break;
                default:
                    throw Invalid();
            }
        }
        return new RemotePluginReleaseReference(profile.Host, segments[0], name, tag, assetName, mirror);
    }

    private static string ValidAssetName(string value)
    {
        string segment = ValidSegment(value);
        if (!segment.EndsWith(PackageExtension, StringComparison.OrdinalIgnoreCase))
        {
            throw Invalid();
        }
        return segment;
    }

    private static string ValidSegment(string value)
    {
        if (value.Length == 0
            || value.Length > MaxSegmentLength
            || value is "." or ".."
            || value.IndexOf('/') >= 0
            || value.IndexOf('\\') >= 0
            || value.Any(character => char.IsControl(character)))
        {
            throw Invalid();
        }
        return value;
    }

    private static bool IsLiteral(string segment, string literal) =>
        segment.Equals(literal, StringComparison.OrdinalIgnoreCase);

    private static GitHubPluginSourceException Invalid() => new(
        "插件来源必须是 GitHub 的 owner/repo，或 github.com / codeberg.org 的仓库、Release 或 .vtplugin 资产地址。",
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
    /// <summary>
    /// Downloads the single formal <c>.vtplugin</c> asset of the remote release
    /// addressed by <paramref name="source"/>: a bare GitHub owner/repo, a
    /// github.com / codeberg.org repository, release or asset URL, or such a
    /// GitHub URL wrapped by a known download mirror.
    /// </summary>
    Task<DownloadedPluginPackage> DownloadAsync(
        string source,
        CancellationToken token);
}

internal sealed class GitHubPluginPackageSource : IGitHubPluginPackageSource
{
    internal const long MaxPackageBytes = 64L * 1024 * 1024;
    private const int MetadataLimitBytes = 2 * 1024 * 1024;
    private const int MaxChecksumBytes = 4096;

    private static readonly Regex ChecksumFilePattern = new(
        @"\A([0-9A-Fa-f]{64}) {1,2}\*?([^\r\n]+)\r?\n?\z",
        RegexOptions.CultureInvariant);
    private static readonly Regex CodebergAttachmentPath = new(
        @"^/attachments/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/?$",
        RegexOptions.CultureInvariant);
    private static readonly UTF8Encoding StrictUtf8 = new(encoderShouldEmitUTF8Identifier: false, throwOnInvalidBytes: true);

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
    }

    public async Task<DownloadedPluginPackage> DownloadAsync(
        string source,
        CancellationToken token)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        RemotePluginReleaseReference reference = RemotePluginReleaseReference.Parse(source);
        PluginReleaseHostProfile profile = PluginReleaseHosts.Get(reference.Host);
        RemotePluginReleaseDto release = await ReadReleaseAsync(reference, profile, token)
            .ConfigureAwait(false);
        RemotePluginAssetDto asset = SelectPackageAsset(reference, profile, release);
        Uri directUri = ParseDownloadUri(reference, profile, release, asset);
        string expectedDigest = profile.UsesChecksumSidecar
            ? await ReadSidecarDigestAsync(reference, profile, release, asset, token)
                .ConfigureAwait(false)
            : ParseApiDigest(profile, asset.Digest);

        // Codeberg never uses the GitHub download proxy. A mirror the user
        // pasted routes this one GitHub asset download only: persisted
        // preferences are neither read nor changed. Without a pasted mirror,
        // GitHub follows the same user-selected channel as application updates.
        Uri downloadUri = directUri;
        if (reference.Mirror is not null)
        {
            downloadUri = new Uri(reference.Mirror + directUri.AbsoluteUri);
        }
        else if (profile.Host == PluginReleaseHost.GitHub)
        {
            AppPreferences preferences = _readPreferences();
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
                    profile.Error("PROXY_INVALID"),
                    exception);
            }
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
                profile,
                token).ConfigureAwait(false);
            if (!CryptographicOperations.FixedTimeEquals(
                    Convert.FromHexString(actualDigest),
                    Convert.FromHexString(expectedDigest)))
            {
                throw new GitHubPluginSourceException(
                    $"{profile.DisplayName} 插件资产的 SHA-256 与 Release 校验信息不一致。",
                    profile.Error("DIGEST_MISMATCH"));
            }
            File.Move(temporary, destination);
            return new DownloadedPluginPackage(
                destination,
                reference.Slug,
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

    private async Task<RemotePluginReleaseDto> ReadReleaseAsync(
        RemotePluginReleaseReference reference,
        PluginReleaseHostProfile profile,
        CancellationToken token)
    {
        string releaseSegment = reference.Tag is null
            ? "latest"
            : "tags/" + Uri.EscapeDataString(reference.Tag);
        var uri = new Uri(
            $"{profile.ApiBaseUri}/{Uri.EscapeDataString(reference.Owner)}/"
            + $"{Uri.EscapeDataString(reference.Name)}/releases/{releaseSegment}");
        using var request = new HttpRequestMessage(HttpMethod.Get, uri);
        request.Headers.Accept.ParseAdd(profile.Accept);
        if (profile.ApiVersionHeader is not null)
        {
            request.Headers.TryAddWithoutValidation(profile.ApiVersionHeader, profile.ApiVersionValue);
        }
        using HttpResponseMessage response = await _httpClient.SendAsync(
            request,
            HttpCompletionOption.ResponseHeadersRead,
            token).ConfigureAwait(false);
        if (response.StatusCode == HttpStatusCode.NotFound)
        {
            throw new GitHubPluginSourceException(
                $"{profile.DisplayName} 仓库不存在、不可公开访问，或请求的 Release 不存在。",
                profile.Error("RELEASE_NOT_FOUND"));
        }
        if (!response.IsSuccessStatusCode)
        {
            throw new GitHubPluginSourceException(
                $"{profile.DisplayName} 插件 Release 检查失败（HTTP {(int)response.StatusCode}）。",
                profile.Error("RELEASE_HTTP_FAILED"));
        }
        if (response.Content.Headers.ContentLength > MetadataLimitBytes)
        {
            throw InvalidMetadata(profile);
        }
        await response.Content.LoadIntoBufferAsync(MetadataLimitBytes, token).ConfigureAwait(false);
        byte[] payload = await response.Content.ReadAsByteArrayAsync(token).ConfigureAwait(false);
        if (payload.Length > MetadataLimitBytes)
        {
            throw InvalidMetadata(profile);
        }
        try
        {
            RemotePluginReleaseDto? release = JsonSerializer.Deserialize<RemotePluginReleaseDto>(payload);
            if (release is null
                || release.Draft
                || release.Prerelease
                || release.PublishedAt is null
                || string.IsNullOrWhiteSpace(release.TagName))
            {
                throw InvalidMetadata(profile);
            }
            return release;
        }
        catch (JsonException exception)
        {
            throw new GitHubPluginSourceException(
                $"{profile.DisplayName} 插件 Release metadata 格式无效。",
                profile.Error("RELEASE_INVALID"),
                exception);
        }
    }

    private static RemotePluginAssetDto SelectPackageAsset(
        RemotePluginReleaseReference reference,
        PluginReleaseHostProfile profile,
        RemotePluginReleaseDto release)
    {
        if (reference.Tag is not null
            && !string.Equals(release.TagName, reference.Tag, StringComparison.Ordinal))
        {
            throw new GitHubPluginSourceException(
                $"{profile.DisplayName} 返回的 Release tag 与请求的 tag 不一致。",
                profile.Error("TAG_MISMATCH"));
        }
        RemotePluginAssetDto[] candidates = (release.Assets ?? [])
            .Where(asset => (!profile.RequiresUploadedState || asset.State == "uploaded")
                && asset.Name?.EndsWith(
                    RemotePluginReleaseReference.PackageExtension,
                    StringComparison.OrdinalIgnoreCase) == true)
            .ToArray();
        if (candidates.Length != 1)
        {
            throw new GitHubPluginSourceException(
                "正式 Release 必须且只能包含一个可安装的 .vtplugin 资产。",
                profile.Error("ASSET_AMBIGUOUS"));
        }
        RemotePluginAssetDto asset = candidates[0];
        if (reference.AssetName is not null
            && !string.Equals(asset.Name, reference.AssetName, StringComparison.Ordinal))
        {
            throw new GitHubPluginSourceException(
                $"{profile.DisplayName} Release 中唯一的 .vtplugin 资产与请求的文件名不一致。",
                profile.Error("ASSET_NAME_MISMATCH"));
        }
        if (asset.Size <= 0 || asset.Size > MaxPackageBytes)
        {
            throw new GitHubPluginSourceException(
                $"{profile.DisplayName} 插件资产大小无效或超过 64 MiB 上限。",
                profile.Error("ASSET_SIZE_INVALID"));
        }
        return asset;
    }

    /// <summary>
    /// Codeberg: the unique <c>&lt;asset&gt;.sha256</c> attachment of the same
    /// release is the one authoritative checksum. The API <c>digest</c> field
    /// (empty on Codeberg in practice) is ignored so that a release never has
    /// two competing checksum sources.
    /// </summary>
    private async Task<string> ReadSidecarDigestAsync(
        RemotePluginReleaseReference reference,
        PluginReleaseHostProfile profile,
        RemotePluginReleaseDto release,
        RemotePluginAssetDto asset,
        CancellationToken token)
    {
        string checksumName = (asset.Name ?? "") + ".sha256";
        RemotePluginAssetDto[] candidates = (release.Assets ?? [])
            .Where(candidate => string.Equals(candidate.Name, checksumName, StringComparison.Ordinal))
            .ToArray();
        if (candidates.Length != 1
            || candidates[0].Size <= 0
            || candidates[0].Size > MaxChecksumBytes)
        {
            throw new GitHubPluginSourceException(
                "Codeberg Release 必须包含唯一的同名 .sha256 校验附件（不超过 4 KiB）。",
                profile.Error("CHECKSUM_ASSET_INVALID"));
        }
        Uri checksumUri = ParseDownloadUri(reference, profile, release, candidates[0]);
        string content = await DownloadChecksumAsync(checksumUri, profile, token)
            .ConfigureAwait(false);
        Match match = ChecksumFilePattern.Match(content);
        if (!match.Success
            || !string.Equals(match.Groups[2].Value, asset.Name, StringComparison.Ordinal))
        {
            throw InvalidChecksum(profile);
        }
        return match.Groups[1].Value.ToLowerInvariant();
    }

    private async Task<string> DownloadChecksumAsync(
        Uri uri,
        PluginReleaseHostProfile profile,
        CancellationToken token)
    {
        using HttpResponseMessage response = await _httpClient.GetAsync(
            uri,
            HttpCompletionOption.ResponseHeadersRead,
            token).ConfigureAwait(false);
        if (!response.IsSuccessStatusCode)
        {
            throw new GitHubPluginSourceException(
                $"{profile.DisplayName} .sha256 附件下载失败（HTTP {(int)response.StatusCode}）。",
                profile.Error("CHECKSUM_DOWNLOAD_FAILED"));
        }
        if (response.Content.Headers.ContentLength > MaxChecksumBytes)
        {
            throw InvalidChecksum(profile);
        }
        await using Stream input = await response.Content.ReadAsStreamAsync(token)
            .ConfigureAwait(false);
        byte[] buffer = new byte[MaxChecksumBytes + 1];
        int total = 0;
        int read;
        while ((read = await input.ReadAsync(buffer.AsMemory(total), token).ConfigureAwait(false)) > 0)
        {
            total += read;
            if (total > MaxChecksumBytes)
            {
                throw InvalidChecksum(profile);
            }
        }
        try
        {
            return StrictUtf8.GetString(buffer, 0, total);
        }
        catch (DecoderFallbackException exception)
        {
            throw new GitHubPluginSourceException(
                "Codeberg .sha256 附件格式无效或指向了其他文件。",
                profile.Error("CHECKSUM_INVALID"),
                exception);
        }
    }

    private async Task<string> DownloadAsync(
        Uri downloadUri,
        string destination,
        long expectedBytes,
        PluginReleaseHostProfile profile,
        CancellationToken token)
    {
        using HttpResponseMessage response = await _httpClient.GetAsync(
            downloadUri,
            HttpCompletionOption.ResponseHeadersRead,
            token).ConfigureAwait(false);
        if (!response.IsSuccessStatusCode)
        {
            throw new GitHubPluginSourceException(
                $"{profile.DisplayName} 插件资产下载失败（HTTP {(int)response.StatusCode}）。",
                profile.Error("DOWNLOAD_HTTP_FAILED"));
        }
        long? contentLength = response.Content.Headers.ContentLength;
        if (contentLength is > MaxPackageBytes || contentLength is <= 0)
        {
            throw new GitHubPluginSourceException(
                $"{profile.DisplayName} 插件下载响应大小无效或超过 64 MiB 上限。",
                profile.Error("DOWNLOAD_SIZE_INVALID"));
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
                        $"{profile.DisplayName} 插件下载超过 64 MiB 上限。",
                        profile.Error("DOWNLOAD_SIZE_INVALID"));
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
                $"{profile.DisplayName} 插件下载字节数与 Release metadata 不一致。",
                profile.Error("DOWNLOAD_SIZE_MISMATCH"));
        }
        return Convert.ToHexString(hash.GetHashAndReset()).ToLowerInvariant();
    }

    private static Uri ParseDownloadUri(
        RemotePluginReleaseReference reference,
        PluginReleaseHostProfile profile,
        RemotePluginReleaseDto release,
        RemotePluginAssetDto asset)
    {
        if (!Uri.TryCreate(asset.BrowserDownloadUrl, UriKind.Absolute, out Uri? uri)
            || uri.Scheme != Uri.UriSchemeHttps
            || !uri.IsDefaultPort
            || !uri.Host.Equals(profile.WebHost, StringComparison.OrdinalIgnoreCase)
            || !string.IsNullOrEmpty(uri.UserInfo)
            || !string.IsNullOrEmpty(uri.Query)
            || !string.IsNullOrEmpty(uri.Fragment)
            || !IsTrustedAssetPath(reference, profile, release, uri, asset.Name ?? ""))
        {
            throw new GitHubPluginSourceException(
                $"{profile.DisplayName} Release 返回了不受信任的插件下载地址。",
                profile.Error("DOWNLOAD_URL_UNTRUSTED"));
        }
        return uri;
    }

    private static bool IsTrustedAssetPath(
        RemotePluginReleaseReference reference,
        PluginReleaseHostProfile profile,
        RemotePluginReleaseDto release,
        Uri uri,
        string assetName)
    {
        // Forgejo may answer with either the canonical release path or an
        // opaque /attachments/<uuid> path for the same release asset.
        if (profile.UsesChecksumSidecar && CodebergAttachmentPath.IsMatch(uri.AbsolutePath))
        {
            return true;
        }
        string[] segments = uri.AbsolutePath
            .Split('/', StringSplitOptions.RemoveEmptyEntries)
            .Select(Uri.UnescapeDataString)
            .ToArray();
        // Owner/name match case-insensitively (users may type a lower-case
        // slug); the tag and the file name are exact.
        return segments.Length == 6
            && segments[0].Equals(reference.Owner, StringComparison.OrdinalIgnoreCase)
            && segments[1].Equals(reference.Name, StringComparison.OrdinalIgnoreCase)
            && segments[2].Equals("releases", StringComparison.OrdinalIgnoreCase)
            && segments[3].Equals("download", StringComparison.OrdinalIgnoreCase)
            && segments[4].Equals(release.TagName, StringComparison.Ordinal)
            && segments[5].Equals(assetName, StringComparison.Ordinal);
    }

    private static string ParseApiDigest(PluginReleaseHostProfile profile, string? value)
    {
        const string prefix = "sha256:";
        if (value is null || !value.StartsWith(prefix, StringComparison.OrdinalIgnoreCase))
        {
            throw InvalidDigest(profile);
        }
        string digest = value[prefix.Length..].ToLowerInvariant();
        if (digest.Length != 64 || digest.Any(character => !Uri.IsHexDigit(character)))
        {
            throw InvalidDigest(profile);
        }
        return digest;
    }

    private static GitHubPluginSourceException InvalidDigest(PluginReleaseHostProfile profile) => new(
        $"{profile.DisplayName} 插件资产缺少有效的 SHA-256 digest。",
        profile.Error("DIGEST_INVALID"));

    private static GitHubPluginSourceException InvalidChecksum(PluginReleaseHostProfile profile) => new(
        $"{profile.DisplayName} .sha256 附件格式无效或指向了其他文件。",
        profile.Error("CHECKSUM_INVALID"));

    private static GitHubPluginSourceException InvalidMetadata(PluginReleaseHostProfile profile) => new(
        $"{profile.DisplayName} 插件 Release metadata 格式无效。",
        profile.Error("RELEASE_INVALID"));

    private static HttpClient CreateHttpClient() => new(new HttpClientHandler
    {
        AllowAutoRedirect = true,
        AutomaticDecompression = DecompressionMethods.All,
    });

    private sealed record RemotePluginReleaseDto(
        [property: JsonPropertyName("tag_name")] string? TagName,
        [property: JsonPropertyName("draft")] bool Draft,
        [property: JsonPropertyName("prerelease")] bool Prerelease,
        // GitHub and Forgejo both publish an RFC 3339 date-time string; the
        // built-in DateTimeOffset? reader accepts exactly that and rejects
        // numbers or malformed values as JsonException (invalid metadata).
        [property: JsonPropertyName("published_at")] DateTimeOffset? PublishedAt,
        [property: JsonPropertyName("assets")] RemotePluginAssetDto[]? Assets);

    // `digest` is a GitHub-only field (Codeberg has none; its checksum lives in
    // the `<asset>.sha256` sidecar attachment), so it is never read for Codeberg.
    private sealed record RemotePluginAssetDto(
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
