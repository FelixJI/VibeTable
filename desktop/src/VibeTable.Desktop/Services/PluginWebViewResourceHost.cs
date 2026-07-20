using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using Microsoft.Web.WebView2.Core;

namespace VibeTable.Desktop.Services;

public sealed record PluginWebResourceRequest(
    Uri Target,
    PluginResourceRequestKind Kind,
    Uri? Initiator);

public sealed record PluginWebResourceResolution(
    int StatusCode,
    string Reason,
    PluginResourceResponse? Resource);

/// <summary>
/// Production registry and WebView2 adapter for immutable plugin origins.
/// Registered resources are served locally; plugin-initiated network requests
/// are answered with 403 before they can leave the WebView.
/// </summary>
public sealed class PluginWebViewResourceHost : IDisposable
{
    private const string Filter = "*";
    private const string PluginHostSuffix = ".plugins.vibetable.local";

    private readonly object _gate = new();
    private readonly PluginResourceHost _resources;
    private readonly PluginSurfaceSessionManager _surfaces;
    private readonly Dictionary<(string ProjectKey, string PluginId), PluginPackageRevision>
        _installations = [];
    private readonly Dictionary<string, PluginPackageRevision> _origins =
        new(StringComparer.OrdinalIgnoreCase);
    private readonly Dictionary<(string ProjectKey, string PluginId, string Entry), string>
        _surfaceTokens = [];
    private CoreWebView2? _core;
    private bool _disposed;

    public PluginWebViewResourceHost(
        PluginResourceHost resources,
        PluginSurfaceSessionManager surfaces)
    {
        _resources = resources ?? throw new ArgumentNullException(nameof(resources));
        _surfaces = surfaces ?? throw new ArgumentNullException(nameof(surfaces));
    }

    public void Attach(CoreWebView2 core)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        ArgumentNullException.ThrowIfNull(core);
        if (ReferenceEquals(_core, core))
        {
            return;
        }
        DetachCore();
        _core = core;
        core.AddWebResourceRequestedFilter(
            Filter,
            CoreWebView2WebResourceContext.All,
            CoreWebView2WebResourceRequestSourceKinds.All);
        core.WebResourceRequested += OnWebResourceRequested;
    }

    public void RegisterInstalled(
        string projectKey,
        string pluginId,
        PluginPackageRevision revision)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        if (string.IsNullOrWhiteSpace(projectKey) || string.IsNullOrWhiteSpace(pluginId))
        {
            throw new ArgumentException("Project and plugin identity are required.");
        }
        ArgumentNullException.ThrowIfNull(revision);
        bool revisionChanged;
        lock (_gate)
        {
            revisionChanged = _installations.TryGetValue((projectKey, pluginId), out var existing)
                && !string.Equals(existing.PackageHash, revision.PackageHash, StringComparison.Ordinal);
            if (revisionChanged)
            {
                foreach (var key in _surfaceTokens.Keys
                    .Where(key => string.Equals(key.ProjectKey, projectKey, StringComparison.Ordinal)
                        && string.Equals(key.PluginId, pluginId, StringComparison.Ordinal))
                    .ToArray())
                {
                    _surfaceTokens.Remove(key);
                }
            }
            _installations[(projectKey, pluginId)] = revision;
            RebuildOrigins();
        }
        if (revisionChanged)
        {
            _surfaces.CloseForInstallation(projectKey, pluginId);
        }
    }

    public bool TryRegisterInstalled(
        string projectKey,
        string pluginId,
        string packageSource,
        string packageHash)
    {
        if (!File.Exists(packageSource) && !Directory.Exists(packageSource))
        {
            return false;
        }
        RegisterInstalled(
            projectKey,
            pluginId,
            PluginPackageRevision.Create(packageSource, packageHash));
        return true;
    }

    public bool UnregisterInstalled(string projectKey, string pluginId)
    {
        PluginPackageRevision? removed;
        lock (_gate)
        {
            if (!_installations.Remove((projectKey, pluginId), out removed))
            {
                return false;
            }
            foreach (var key in _surfaceTokens.Keys
                .Where(key => string.Equals(key.ProjectKey, projectKey, StringComparison.Ordinal)
                    && string.Equals(key.PluginId, pluginId, StringComparison.Ordinal))
                .ToArray())
            {
                _surfaceTokens.Remove(key);
            }
            RebuildOrigins();
        }
        _surfaces.CloseForInstallation(projectKey, pluginId);
        return true;
    }

    public PluginSurfaceSession OpenSurface(
        string projectKey,
        string pluginId,
        string entryPath)
    {
        PluginPackageRevision revision;
        string normalizedEntry = PluginResourceHost.NormalizePackagePath(entryPath);
        lock (_gate)
        {
            if (!_installations.TryGetValue((projectKey, pluginId), out revision!))
            {
                throw new PluginSurfacePolicyException(
                    "PLUGIN_REVISION_NOT_REGISTERED",
                    "Plugin package revision is not registered with the host.");
            }
            var key = (projectKey, pluginId, normalizedEntry);
            if (_surfaceTokens.TryGetValue(key, out string? existingToken)
                && _surfaces.TryGet(existingToken, out var existing))
            {
                return existing!;
            }
            var opened = _surfaces.Open(projectKey, pluginId, revision, normalizedEntry);
            _surfaceTokens[key] = opened.SurfaceToken;
            return opened;
        }
    }

    public bool CloseSurface(string surfaceToken)
    {
        ForgetSurfaceToken(surfaceToken);
        return _surfaces.Close(surfaceToken);
    }

    public void ForgetSurfaceToken(string surfaceToken)
    {
        lock (_gate)
        {
            foreach (var key in _surfaceTokens
                .Where(pair => string.Equals(pair.Value, surfaceToken, StringComparison.Ordinal))
                .Select(pair => pair.Key)
                .ToArray())
            {
                _surfaceTokens.Remove(key);
            }
        }
    }

    public void CloseAllSurfaces()
    {
        lock (_gate)
        {
            _surfaceTokens.Clear();
        }
        _surfaces.CloseAll();
    }

    public bool IsRegisteredUri(Uri uri)
    {
        if (!IsPluginUri(uri))
        {
            return false;
        }
        lock (_gate)
        {
            return _origins.ContainsKey(uri.Host);
        }
    }

    public static bool IsPluginUri(Uri? uri)
        => uri is not null
            && string.Equals(uri.Scheme, Uri.UriSchemeHttps, StringComparison.Ordinal)
            && uri.Host.EndsWith(PluginHostSuffix, StringComparison.OrdinalIgnoreCase);

    public PluginWebResourceResolution? Resolve(PluginWebResourceRequest request)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        ArgumentNullException.ThrowIfNull(request);
        PluginPackageRevision? targetRevision;
        bool targetLooksLikePlugin = IsPluginUri(request.Target);
        bool initiatedByPlugin = IsPluginUri(request.Initiator);
        lock (_gate)
        {
            _origins.TryGetValue(request.Target.Host, out targetRevision);
            initiatedByPlugin = initiatedByPlugin
                && request.Initiator is not null
                && _origins.ContainsKey(request.Initiator.Host);
        }

        if (targetRevision is null)
        {
            return targetLooksLikePlugin || initiatedByPlugin
                ? Denied("Plugin network and unknown plugin origins are blocked.")
                : null;
        }
        if (!PluginResourceHost.IsRequestAllowed(targetRevision, request.Target, request.Kind))
        {
            return Denied("Plugin resource request kind is blocked.");
        }

        string path = Uri.UnescapeDataString(request.Target.AbsolutePath).TrimStart('/');
        try
        {
            var resource = _resources.Open(targetRevision, path, request.Kind);
            return new PluginWebResourceResolution(200, "OK", resource);
        }
        catch (PluginResourcePolicyException ex) when (ex.Code == "PLUGIN_RESOURCE_NOT_FOUND")
        {
            return new PluginWebResourceResolution(404, "Not Found", null);
        }
        catch (PluginResourcePolicyException)
        {
            return Denied("Plugin resource request is invalid.");
        }
    }

    public void Dispose()
    {
        if (_disposed)
        {
            return;
        }
        _disposed = true;
        DetachCore();
        lock (_gate)
        {
            _installations.Clear();
            _origins.Clear();
            _surfaceTokens.Clear();
        }
        _surfaces.CloseAll();
    }

    private void OnWebResourceRequested(
        object? sender,
        CoreWebView2WebResourceRequestedEventArgs args)
    {
        var core = sender as CoreWebView2 ?? _core;
        if (core is null || !Uri.TryCreate(args.Request.Uri, UriKind.Absolute, out var target))
        {
            return;
        }
        Uri? initiator = TryReadInitiator(args.Request.Headers);
        var resolution = Resolve(new PluginWebResourceRequest(
            target,
            ToRequestKind(args.ResourceContext, args.RequestedSourceKind),
            initiator));
        if (resolution is null)
        {
            return;
        }

        Stream content = resolution.Resource?.Content ?? new MemoryStream();
        string headers = resolution.Resource is null
            ? "Content-Type: text/plain; charset=utf-8\r\nCache-Control: no-store"
            : string.Join(
                "\r\n",
                resolution.Resource.Headers.Select(pair => $"{pair.Key}: {pair.Value}"))
                + $"\r\nContent-Type: {resolution.Resource.ContentType}";
        args.Response = core.Environment.CreateWebResourceResponse(
            content,
            resolution.StatusCode,
            resolution.Reason,
            headers);
    }

    private static Uri? TryReadInitiator(CoreWebView2HttpRequestHeaders headers)
    {
        foreach (string name in new[] { "Referer", "Origin" })
        {
            string value;
            try
            {
                value = headers.GetHeader(name);
            }
            catch
            {
                continue;
            }
            if (!string.IsNullOrWhiteSpace(value)
                && !string.Equals(value, "null", StringComparison.OrdinalIgnoreCase)
                && Uri.TryCreate(value, UriKind.Absolute, out var uri))
            {
                return uri;
            }
        }
        return null;
    }

    private static PluginResourceRequestKind ToRequestKind(
        CoreWebView2WebResourceContext context,
        CoreWebView2WebResourceRequestSourceKinds sourceKind)
    {
        if ((sourceKind & CoreWebView2WebResourceRequestSourceKinds.ServiceWorker) != 0)
        {
            return PluginResourceRequestKind.ServiceWorker;
        }
        return context switch
        {
            CoreWebView2WebResourceContext.Document => PluginResourceRequestKind.Document,
            CoreWebView2WebResourceContext.Stylesheet => PluginResourceRequestKind.Style,
            CoreWebView2WebResourceContext.Image => PluginResourceRequestKind.Image,
            CoreWebView2WebResourceContext.Font => PluginResourceRequestKind.Font,
            CoreWebView2WebResourceContext.Script => PluginResourceRequestKind.Script,
            CoreWebView2WebResourceContext.XmlHttpRequest => PluginResourceRequestKind.XmlHttpRequest,
            CoreWebView2WebResourceContext.Fetch => PluginResourceRequestKind.Fetch,
            CoreWebView2WebResourceContext.EventSource => PluginResourceRequestKind.EventSource,
            CoreWebView2WebResourceContext.Websocket => PluginResourceRequestKind.WebSocket,
            _ => PluginResourceRequestKind.Navigation,
        };
    }

    private static PluginWebResourceResolution Denied(string reason)
        => new(403, "Forbidden", null);

    private void RebuildOrigins()
    {
        _origins.Clear();
        foreach (PluginPackageRevision revision in _installations.Values)
        {
            _origins[revision.VirtualHostName] = revision;
        }
    }

    private void DetachCore()
    {
        if (_core is null)
        {
            return;
        }
        try
        {
            _core.WebResourceRequested -= OnWebResourceRequested;
            _core.RemoveWebResourceRequestedFilter(
                Filter,
                CoreWebView2WebResourceContext.All,
                CoreWebView2WebResourceRequestSourceKinds.All);
        }
        catch
        {
            // WebView teardown may already have released the COM object.
        }
        _core = null;
    }
}
