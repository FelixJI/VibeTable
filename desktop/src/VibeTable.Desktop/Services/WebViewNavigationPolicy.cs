using System;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Pure URI policy shared by the two WebView2 hosts. Keeping the policies
/// separate prevents the trusted administration origin from leaking into the main
/// application renderer's navigation boundary.
/// </summary>
public static class WebViewNavigationPolicy
{
    public static bool IsAppNavigation(string? uri)
    {
        return TryGetAbsoluteHttpUri(uri, out var candidate)
            && string.Equals(candidate.Scheme, Uri.UriSchemeHttps, StringComparison.OrdinalIgnoreCase)
            && string.Equals(candidate.Host, WebViewAssetService.AppHostName, StringComparison.OrdinalIgnoreCase)
            && candidate.IsDefaultPort;
    }

    public static bool IsAdminNavigation(string? uri, string? adminBaseUrl)
    {
        if (!TryGetAbsoluteHttpUri(uri, out var candidate)
            || !TryGetAbsoluteHttpUri(adminBaseUrl, out var allowed))
        {
            return false;
        }

        return string.Equals(candidate.Scheme, allowed.Scheme, StringComparison.OrdinalIgnoreCase)
            && string.Equals(candidate.Host, allowed.Host, StringComparison.OrdinalIgnoreCase)
            && candidate.Port == allowed.Port;
    }

    public static WebViewLinkDisposition ClassifyAppNewWindow(string? uri)
        => ClassifyNewWindow(uri, IsAppNavigation(uri));

    public static WebViewLinkDisposition ClassifyAdminNewWindow(
        string? uri,
        string? adminBaseUrl)
        => ClassifyNewWindow(uri, IsAdminNavigation(uri, adminBaseUrl));

    private static WebViewLinkDisposition ClassifyNewWindow(string? uri, bool trusted)
    {
        if (trusted)
        {
            return WebViewLinkDisposition.CurrentView;
        }

        if (!Uri.TryCreate(uri, UriKind.Absolute, out var candidate))
        {
            return WebViewLinkDisposition.Block;
        }

        return IsHttpScheme(candidate.Scheme)
            ? WebViewLinkDisposition.ExternalBrowser
            : WebViewLinkDisposition.Block;
    }

    private static bool TryGetAbsoluteHttpUri(string? value, out Uri uri)
    {
        if (Uri.TryCreate(value, UriKind.Absolute, out var parsed)
            && IsHttpScheme(parsed.Scheme))
        {
            uri = parsed;
            return true;
        }

        uri = null!;
        return false;
    }

    private static bool IsHttpScheme(string scheme)
        => string.Equals(scheme, Uri.UriSchemeHttp, StringComparison.OrdinalIgnoreCase)
            || string.Equals(scheme, Uri.UriSchemeHttps, StringComparison.OrdinalIgnoreCase);
}

public enum WebViewLinkDisposition
{
    Block,
    CurrentView,
    ExternalBrowser,
}
