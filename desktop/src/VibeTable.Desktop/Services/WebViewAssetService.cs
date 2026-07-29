using System;
using System.IO;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Resolves the on-disk folder that backs the hardened WebView2 virtual host
/// mapping (<c>https://app.vibetable.local/</c>).
/// </summary>
/// <remarks>
/// <para>
/// Two layouts are supported:
/// </para>
/// <list type="bullet">
/// <item><b>Dev</b>: the web-grid's Vite <c>dist/</c> output at
/// <c>&lt;repo&gt;/desktop/web-grid/dist</c>. The path is located by walking
/// up from the host assembly until the <c>desktop/web-grid/dist</c> marker is
/// found, so it works regardless of which <c>bin/Configuration</c> subtree the
/// app launched from.</item>
/// <item><b>Packaged</b>: a <c>web-grid</c> folder placed under
/// <c>AppContext.BaseDirectory/resources/web-grid</c>. The Phase A
/// packaging step copies the built <c>dist</c> output into this folder.</item>
/// </list>
/// <para>
/// The service does NOT verify the folder's contents beyond existence — the
/// WebView2 navigation will surface a load failure as a faulted
/// <c>IWebViewBridge.LoadAsync</c>, which the ViewModel routes to the
/// <c>Faulted</c> state.
/// </para>
/// </remarks>
public static class WebViewAssetService
{
    /// <summary>
    /// The virtual-host entry URI for the bundled web-grid. Navigation is
    /// gated to this URI's HTTPS origin (see <c>MainWindow.xaml.cs</c>).
    /// </summary>
    public const string AppOrigin = "https://app.vibetable.local/index.html";

    /// <summary>
    /// The host-name component (without scheme) used for the
    /// <c>SetVirtualHostNameToFolderMapping</c> call.
    /// </summary>
    public const string AppHostName = "app.vibetable.local";

    /// <summary>
    /// Resolves the absolute path to the web-grid folder to be mapped to
    /// <see cref="AppOrigin"/>. Returns null if neither the packaged nor the
    /// dev layout is present.
    /// </summary>
    public static string? ResolveWebGridFolder()
    {
        // 1. Packaged layout: <exe-dir>/resources/web-grid
        string baseDir = AppContext.BaseDirectory;
        string packaged = Path.Combine(
            baseDir,
            "resources",
            "web-grid");
        if (Directory.Exists(packaged))
        {
            return packaged;
        }

        // 2. Dev layout: walk up to find desktop/web-grid/dist
        string? dev = FindDevWebGridDist(baseDir);
        if (dev is not null)
        {
            return dev;
        }

        return null;
    }

    private static string? FindDevWebGridDist(string startDir)
    {
        // Walk up at most 6 levels looking for a "desktop/web-grid/dist" path.
        var dir = new DirectoryInfo(startDir);
        for (int i = 0; i < 8 && dir is not null; i++)
        {
            string candidate = Path.Combine(
                dir.FullName, "desktop", "web-grid", "dist");
            if (Directory.Exists(candidate))
            {
                return candidate;
            }
            dir = dir.Parent;
        }
        return null;
    }
}
