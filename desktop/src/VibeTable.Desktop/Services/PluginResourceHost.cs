using System;
using System.Collections.Generic;
using System.IO;
using System.IO.Compression;
using System.Linq;
using System.Text.RegularExpressions;

namespace VibeTable.Desktop.Services;

public sealed record PluginPackageRevision(
    string PackageRoot,
    string PackageHash,
    string VirtualHostName,
    Uri Origin)
{
    private static readonly Regex Sha256Pattern =
        new("^[a-f0-9]{64}$", RegexOptions.CultureInvariant);

    public static PluginPackageRevision Create(string packageRoot, string packageHash)
    {
        if (string.IsNullOrWhiteSpace(packageRoot))
        {
            throw new ArgumentException("Package root is required.", nameof(packageRoot));
        }
        string normalizedHash = packageHash?.Trim().ToLowerInvariant() ?? string.Empty;
        if (!Sha256Pattern.IsMatch(normalizedHash))
        {
            throw new ArgumentException("Package hash must be a 64-character SHA-256 hex value.", nameof(packageHash));
        }

        string root = Path.GetFullPath(packageRoot);
        // Keep every DNS label valid while preserving the complete immutable
        // package hash as the origin key.
        string host = $"{normalizedHash[..32]}.{normalizedHash[32..]}.plugins.vibetable.local";
        return new PluginPackageRevision(
            root,
            normalizedHash,
            host,
            new Uri($"https://{host}/", UriKind.Absolute));
    }
}

public enum PluginResourceRequestKind
{
    Document,
    Script,
    Style,
    Image,
    Font,
    Worker,
    Fetch,
    XmlHttpRequest,
    WebSocket,
    EventSource,
    RemoteImport,
    ServiceWorker,
    Navigation,
}

public sealed record PluginResourceResponse(
    Stream Content,
    string ContentType,
    IReadOnlyDictionary<string, string> Headers) : IDisposable
{
    public void Dispose() => Content.Dispose();
}

public sealed class PluginResourcePolicyException : Exception
{
    public PluginResourcePolicyException(string code, string message) : base(message)
        => Code = code;

    public string Code { get; }
}

/// <summary>
/// Resolves immutable package resources and defines the WebView interception
/// policy for plugin-owned origins. It never performs a network request.
/// </summary>
public sealed class PluginResourceHost
{
    public const string ContentSecurityPolicy =
        "default-src 'none'; " +
        "script-src 'self'; " +
        "style-src 'self' 'unsafe-inline'; " +
        "img-src 'self' data:; " +
        "font-src 'self'; " +
        "worker-src 'self'; " +
        "connect-src 'none'; " +
        "object-src 'none'; " +
        "frame-src 'none'; " +
        "frame-ancestors https://app.vibetable.local; " +
        "base-uri 'none'; " +
        "form-action 'none'";

    private static readonly IReadOnlyDictionary<string, string> ContentTypes =
        new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase)
        {
            [".html"] = "text/html; charset=utf-8",
            [".css"] = "text/css; charset=utf-8",
            [".js"] = "text/javascript; charset=utf-8",
            [".mjs"] = "text/javascript; charset=utf-8",
            [".json"] = "application/json; charset=utf-8",
            [".svg"] = "image/svg+xml",
            [".png"] = "image/png",
            [".jpg"] = "image/jpeg",
            [".jpeg"] = "image/jpeg",
            [".gif"] = "image/gif",
            [".webp"] = "image/webp",
            [".woff"] = "font/woff",
            [".woff2"] = "font/woff2",
        };

    public PluginResourceResponse Open(
        PluginPackageRevision revision,
        string requestedPath,
        PluginResourceRequestKind kind)
    {
        ArgumentNullException.ThrowIfNull(revision);
        string normalized = NormalizePackagePath(requestedPath);
        var target = new Uri(revision.Origin, normalized);
        if (!IsRequestAllowed(revision, target, kind))
        {
            throw new PluginResourcePolicyException(
                "PLUGIN_RESOURCE_DENIED",
                "The requested plugin resource kind is not allowed.");
        }

        if (File.Exists(revision.PackageRoot))
        {
            return OpenArchive(revision.PackageRoot, normalized);
        }
        if (!Directory.Exists(revision.PackageRoot))
        {
            throw new PluginResourcePolicyException(
                "PLUGIN_RESOURCE_NOT_FOUND",
                "Plugin package revision is unavailable.");
        }

        string fullPath = Path.GetFullPath(Path.Combine(
            revision.PackageRoot,
            normalized.Replace('/', Path.DirectorySeparatorChar)));
        string rootPrefix = revision.PackageRoot.TrimEnd(
            Path.DirectorySeparatorChar,
            Path.AltDirectorySeparatorChar) + Path.DirectorySeparatorChar;
        if (!fullPath.StartsWith(rootPrefix, StringComparison.OrdinalIgnoreCase))
        {
            throw new PluginResourcePolicyException(
                "PLUGIN_PATH_ESCAPE",
                "Plugin resource path escaped the package root.");
        }
        RejectReparsePoints(revision.PackageRoot, fullPath);
        if (!File.Exists(fullPath))
        {
            throw new PluginResourcePolicyException(
                "PLUGIN_RESOURCE_NOT_FOUND",
                "Plugin resource was not found.");
        }

        return new PluginResourceResponse(
            File.Open(fullPath, FileMode.Open, FileAccess.Read, FileShare.Read),
            GetContentType(fullPath),
            CreateHeaders());
    }

    public static string NormalizePackagePath(string requestedPath)
    {
        if (string.IsNullOrWhiteSpace(requestedPath))
        {
            throw Policy("PLUGIN_PATH_INVALID", "Plugin resource path is required.");
        }
        if (requestedPath.Contains('\\')
            || requestedPath.Contains('?')
            || requestedPath.Contains('#')
            || Uri.TryCreate(requestedPath, UriKind.Absolute, out _))
        {
            throw Policy("PLUGIN_PATH_INVALID", "Plugin resource path must be package-relative.");
        }

        string decoded;
        try
        {
            decoded = Uri.UnescapeDataString(requestedPath);
        }
        catch (UriFormatException)
        {
            throw Policy("PLUGIN_PATH_INVALID", "Plugin resource path encoding is invalid.");
        }
        if (!string.Equals(Uri.UnescapeDataString(decoded), decoded, StringComparison.Ordinal))
        {
            throw Policy("PLUGIN_PATH_AMBIGUOUS", "Repeated path decoding is not allowed.");
        }
        if (decoded.StartsWith("/", StringComparison.Ordinal)
            || Path.IsPathRooted(decoded))
        {
            throw Policy("PLUGIN_PATH_INVALID", "Plugin resource path must be package-relative.");
        }

        string[] segments = decoded.Split('/');
        if (segments.Any(segment =>
            segment.Length == 0
            || segment is "." or ".."
            || segment.Contains(':')
            || !string.Equals(segment, segment.Trim(), StringComparison.Ordinal)))
        {
            throw Policy("PLUGIN_PATH_INVALID", "Plugin resource path is not canonical.");
        }
        return string.Join('/', segments);
    }

    public static bool IsRequestAllowed(
        PluginPackageRevision revision,
        Uri target,
        PluginResourceRequestKind kind)
    {
        ArgumentNullException.ThrowIfNull(revision);
        ArgumentNullException.ThrowIfNull(target);
        bool sameOrigin = string.Equals(
            revision.Origin.GetLeftPart(UriPartial.Authority),
            target.GetLeftPart(UriPartial.Authority),
            StringComparison.OrdinalIgnoreCase);
        if (!sameOrigin || !string.Equals(target.Scheme, Uri.UriSchemeHttps, StringComparison.Ordinal))
        {
            return false;
        }

        return kind is PluginResourceRequestKind.Document
            or PluginResourceRequestKind.Script
            or PluginResourceRequestKind.Style
            or PluginResourceRequestKind.Image
            or PluginResourceRequestKind.Font
            or PluginResourceRequestKind.Worker;
    }

    private static void RejectReparsePoints(string packageRoot, string fullPath)
    {
        if (Directory.Exists(packageRoot)
            && (File.GetAttributes(packageRoot) & FileAttributes.ReparsePoint) != 0)
        {
            throw Policy(
                "PLUGIN_RESOURCE_REPARSE_POINT",
                "Symbolic links and reparse points are not served.");
        }
        string relative = Path.GetRelativePath(packageRoot, fullPath);
        string current = packageRoot;
        foreach (string segment in relative.Split(
            Path.DirectorySeparatorChar,
            StringSplitOptions.RemoveEmptyEntries))
        {
            current = Path.Combine(current, segment);
            if (File.Exists(current) || Directory.Exists(current))
            {
                var attributes = File.GetAttributes(current);
                if ((attributes & FileAttributes.ReparsePoint) != 0)
                {
                    throw Policy(
                        "PLUGIN_RESOURCE_REPARSE_POINT",
                        "Symbolic links and reparse points are not served.");
                }
            }
        }
    }

    private static PluginResourceResponse OpenArchive(string archivePath, string normalized)
    {
        if (!string.Equals(Path.GetExtension(archivePath), ".vtplugin", StringComparison.OrdinalIgnoreCase)
            || (File.GetAttributes(archivePath) & FileAttributes.ReparsePoint) != 0)
        {
            throw Policy(
                "PLUGIN_RESOURCE_ARCHIVE_INVALID",
                "Plugin resource archive is invalid.");
        }
        using var file = File.Open(archivePath, FileMode.Open, FileAccess.Read, FileShare.Read);
        using var archive = new ZipArchive(file, ZipArchiveMode.Read, leaveOpen: false);
        var matches = archive.Entries
            .Where(entry => string.Equals(entry.FullName, normalized, StringComparison.Ordinal))
            .ToArray();
        if (matches.Length != 1 || matches[0].FullName.EndsWith("/", StringComparison.Ordinal))
        {
            throw Policy(
                "PLUGIN_RESOURCE_NOT_FOUND",
                "Plugin resource was not found.");
        }
        const long maxResourceBytes = 32L * 1024 * 1024;
        if (matches[0].Length > maxResourceBytes)
        {
            throw Policy(
                "PLUGIN_RESOURCE_TOO_LARGE",
                "Plugin resource exceeds the host response limit.");
        }
        var content = new MemoryStream((int)matches[0].Length);
        using (Stream source = matches[0].Open())
        {
            source.CopyTo(content);
        }
        content.Position = 0;
        return new PluginResourceResponse(
            content,
            GetContentType(normalized),
            CreateHeaders());
    }

    private static string GetContentType(string path)
    {
        string extension = Path.GetExtension(path);
        return ContentTypes.TryGetValue(extension, out string? known)
            ? known
            : "application/octet-stream";
    }

    private static IReadOnlyDictionary<string, string> CreateHeaders()
        => new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase)
        {
            ["Cache-Control"] = "public, max-age=31536000, immutable",
            ["X-Content-Type-Options"] = "nosniff",
            ["Content-Security-Policy"] = ContentSecurityPolicy,
        };

    private static PluginResourcePolicyException Policy(string code, string message)
        => new(code, message);
}
