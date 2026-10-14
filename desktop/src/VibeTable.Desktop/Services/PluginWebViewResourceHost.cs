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
    private sealed record InstallationRegistration(PluginPackageRevision Revision, string? InstallationId, int InstallationRevision);
    private readonly Dictionary<(string ProjectKey, string PluginId), InstallationRegistration>
        _installations = [];
    private readonly Dictionary<string, PluginPackageRevision> _origins =
        new(StringComparer.OrdinalIgnoreCase);
    private readonly Dictionary<(string ProjectKey, string PluginId, string Entry, string ActionId), string>
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

    internal event Action<string>? SurfaceClosed
    {
        add => _surfaces.Closed += value;
        remove => _surfaces.Closed -= value;
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
        PluginPackageRevision revision,
        string? installationId = null,
        int installationRevision = 0)
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
                && (!string.Equals(existing.Revision.PackageHash, revision.PackageHash, StringComparison.Ordinal)
                    || !string.Equals(existing.InstallationId, installationId, StringComparison.Ordinal)
                    || existing.InstallationRevision != installationRevision);
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
            _installations[(projectKey, pluginId)] = new(revision, installationId, installationRevision);
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
        string packageHash,
        string? installationId = null,
        int installationRevision = 0)
    {
        if (!File.Exists(packageSource) && !Directory.Exists(packageSource))
        {
            return false;
        }
        RegisterInstalled(
            projectKey,
            pluginId,
            PluginPackageRevision.Create(packageSource, packageHash),
            installationId, installationRevision);
        return true;
    }

    internal bool TryGetInstallationIdentity(string projectKey, string pluginId, out string? installationId)
    {
        lock (_gate)
        {
            bool found = _installations.TryGetValue((projectKey, pluginId), out var current);
            installationId = current?.InstallationId;
            return found;
        }
    }

    internal bool UnregisterInstalledIfCurrent(string projectKey, string pluginId, string? installationId)
        => UnregisterInstalled(projectKey, pluginId, installationId, matchIdentity: true);

    internal void RetireAbsentInstallations(string projectKey, IEnumerable<string> pluginIds)
    {
        var retained = new HashSet<string>(pluginIds, StringComparer.Ordinal);
        string[] absent;
        lock (_gate)
            absent = _installations.Keys.Where(key => key.ProjectKey == projectKey && !retained.Contains(key.PluginId))
                .Select(key => key.PluginId).ToArray();
        foreach (string pluginId in absent) UnregisterInstalled(projectKey, pluginId);
    }

    public bool UnregisterInstalled(string projectKey, string pluginId)
        => UnregisterInstalled(projectKey, pluginId, null, matchIdentity: false);

    private bool UnregisterInstalled(string projectKey, string pluginId, string? installationId, bool matchIdentity)
    {
        lock (_gate)
        {
            if (!_installations.TryGetValue((projectKey, pluginId), out var current)
                || matchIdentity && !string.Equals(current.InstallationId, installationId, StringComparison.Ordinal))
            {
                return false;
            }
            _installations.Remove((projectKey, pluginId));
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
        string entryPath,
        string actionId = "",
        string presentation = "surface")
    {
        PluginPackageRevision revision;
        string normalizedEntry = PluginResourceHost.NormalizePackagePath(entryPath);
        if (PluginResourceHost.IsHostReservedPath(normalizedEntry)
            || presentation is "application" or "headless"
                && !PluginResourceHost.SupportsPresentationEntry(presentation, normalizedEntry))
            throw new PluginSurfacePolicyException("PLUGIN_ENTRY_TYPE_INVALID", "Plugin entry is unavailable for its presentation.");
        lock (_gate)
        {
            if (!_installations.TryGetValue((projectKey, pluginId), out var registered))
            {
                throw new PluginSurfacePolicyException(
                    "PLUGIN_REVISION_NOT_REGISTERED",
                    "Plugin package revision is not registered with the host.");
            }
            revision = registered.Revision;
            var key = (projectKey, pluginId, normalizedEntry, actionId);
            if (_surfaceTokens.TryGetValue(key, out string? existingToken)
                && _surfaces.TryGet(existingToken, out var existing))
            {
                return existing!;
            }
            // headless actions load a host-owned controlled document that
            // imports the declared in-package JS entry on the plugin origin.
            Uri? documentUri = presentation == "headless"
                ? PluginResourceHost.BuildHeadlessDocumentUri(revision, normalizedEntry)
                : null;
            var opened = _surfaces.Open(projectKey, pluginId, revision, normalizedEntry, documentUri);
            _surfaceTokens[key] = opened.SurfaceToken;
            return opened;
        }
    }

    public bool CloseSurface(string surfaceToken)
    {
        ForgetSurfaceToken(surfaceToken);
        return _surfaces.Close(surfaceToken);
    }
    internal bool TryGetSurface(string token, out PluginSurfaceSession? surface) => _surfaces.TryGet(token, out surface);

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
        foreach (PluginPackageRevision revision in _installations.Values.Select(item => item.Revision))
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
